package watcher

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/time/rate"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	"github.com/kseer/kseer-agent/pkg/config"
	"github.com/kseer/kseer-agent/pkg/outputs"
	"github.com/kseer/kseer-agent/pkg/types"
)

// errResyncTriggered signals that runWatchEventLoop exited because the resync
// timer fired (not because the server closed the watch or the context was
// cancelled). The watchResource caller uses this to decide whether to wipe the
// bookmark and force a full re-LIST on the next iteration — wiping only on
// actual resync, not on every clean server-side watch close.
var errResyncTriggered = errors.New("resync timer triggered")

// SeederChecker retrieves the highest resourceVersion per Kubernetes kind
// seen in the output store for a given cluster. The watcher uses these
// per-kind RVs to open WATCH cursors, skipping the initial LIST for
// resource types that are already seeded.
type SeederChecker interface {
	// GetResumeRVsByKind returns a map of kind → resourceVersion for the cluster.
	// An empty map means the cluster has no data — all resources will LIST.
	// An error means the store is unreachable — all resources will LIST (safe fallback).
	GetResumeRVsByKind(ctx context.Context, cluster string) (map[string]string, error)
}

// SeedTracker is an optional extension implemented by outputs that support
// writing per-kind LIST completion sentinels. When the seederChecker also
// implements SeedTracker, the watcher writes a sentinel after every
// successful LIST and reads the most recent sentinel timestamp per kind on
// startup to anchor resync schedules across pod restarts.
type SeedTracker interface {
	// WriteListCompleteSentinel writes a SEEDED sentinel row for the given
	// kind + snapshot RV. Must be called only after Manager.Flush confirms
	// all EXISTING events for this kind are durable in the output store.
	WriteListCompleteSentinel(ctx context.Context, cluster, kind, snapshotRV string) error

	// GetLastSeedTimestampsByKind returns the most recent SEEDED row's
	// timestamp per kind for the given cluster, bounded to the past
	// `window` (used both for partition pruning and as a sanity cap; kinds
	// whose last SEEDED is older than the window are absent from the
	// result and treated as "fire immediately"). Used by the watcher to
	// anchor P1/P2/P3 resync schedules across pod restarts so that a
	// restart 4 days into a 5-day interval doesn't reset to a fresh 5-day
	// wait.
	GetLastSeedTimestampsByKind(ctx context.Context, cluster string, window time.Duration) (map[string]time.Time, error)
}

// ClusterWatcher watches resources in a Kubernetes cluster
type ClusterWatcher struct {
	clusterName     string
	config          *rest.Config
	dynamicClient   dynamic.Interface
	discoveryClient discovery.DiscoveryInterface
	outputManager   *outputs.Manager
	logger          *zap.Logger
	namespaces      []string
	resourceFilters []config.ResourceFilter
	stopCh          chan struct{}
	wg              sync.WaitGroup

	// Watch management
	watchedResourcesMu sync.RWMutex
	watchedResources   map[string]context.CancelFunc // key: GVR string, value: cancel function

	// Rate limiting
	rateLimiter *rate.Limiter

	// Watch configuration
	watchConfig config.WatchConfig

	// Concurrency control for LIST operations
	listSemaphore       chan struct{} // Per-cluster limit
	globalListSemaphore chan struct{} // Global limit across all clusters

	// Track if initial sync is complete
	initialSyncDone bool
	initialSyncMu   sync.Mutex

	// seederChecker queries the output store for per-kind resume RVs at startup.
	// nil when not configured.
	seederChecker SeederChecker

	// kindInitRVs holds the per-kind MAX resourceVersion from the output store,
	// set once in StartWithInitSemaphore. Used as the initial WATCH cursor for
	// each resource type. Empty map means the cluster is not seeded — all
	// resources will do a full LIST.
	kindInitRVsMu sync.RWMutex
	kindInitRVs   map[string]string // kind ("Secret") → resourceVersion

	// globalInitRV is the maximum RV across all kindInitRVs, computed once at
	// startup. Used as a tier-3 WATCH fallback when a specific kind's CH RV is
	// 410-Gone but other kinds are still active — avoids a full LIST for idle kinds.
	// Written once before watching starts; no mutex needed.
	globalInitRV string

	// gvrKindMap maps gvrKey → kind, built incrementally during discoverAndWatch.
	// Allows watchResource to look up kindInitRVs by GVR.
	// gvrTier maps gvrKey → tier (TierP1/TierP2/TierP3), populated alongside.
	// Both maps share gvrKindMu.
	gvrKindMu  sync.RWMutex
	gvrKindMap map[string]string // gvrKey → kind ("Secret")
	gvrTier    map[string]Tier   // gvrKey → tier

	// tierResolver maps (apiVersion, kind) to tiers. Built once in New() from
	// the cluster's WatchConfig.Tiers and consulted during discoverAndWatch.
	tierResolver *tierResolver
	tierConfig   config.TierConfig

	// gvrResyncDeadline holds the absolute next-resync time per GVR. It is
	// advanced to now+ResyncInterval only when a real LIST completes (see
	// watchResource); watch reconnects reuse the stored deadline as-is so
	// reconnect churn cannot push it out. Lazily initialized on the first
	// arming to lastSeed+interval (survives pod restart) or to "now" when
	// the last-seed timestamp is unknown (fail-safe: relist immediately).
	// Protected by gvrKindMu.
	gvrResyncDeadline map[string]time.Time

	// p1ResyncOnce guards the per-cluster P1 sequential resync goroutine.
	// discoverAndWatch can fire multiple times (CRD changes), but the resync
	// loop must only start once.
	p1ResyncOnce sync.Once

	// kindLastSeed holds the most recent SEEDED timestamp per kind, queried
	// once at watcher startup from ClickHouse. Used by P1/P2/P3 startup
	// paths to anchor the first resync deadline at lastSeed + interval
	// rather than now + interval — survives pod restart so a 4-day-uptime
	// pod restarting on a 5-day interval doesn't reset to a fresh 5-day
	// wait. Empty if CH is unavailable, the kind hasn't been seeded yet,
	// or its last SEEDED is older than the query window.
	// Read-only after Start*; no mutex needed.
	kindLastSeed map[string]time.Time

	// kindLastEvent and kindEventCount feed the periodic status log. Updated
	// on every successful Send (P1, P2, and P3 paths). Keyed by Kubernetes
	// Kind (e.g. "Pod"); two API versions of the same kind aggregate, which
	// matches how the status log presents per-kind activity. Kept under their
	// own mutex so write contention from high-rate handleObject goroutines
	// does not serialize with gvrKindMu reads done by the same path.
	kindStatsMu     sync.Mutex
	kindLastEvent   map[string]time.Time
	kindEventCount  map[string]int64

	// gvrBookmarkRVs tracks the latest BOOKMARK resourceVersion per GVR received
	// from a running watch. On reconnect within the same process, these are
	// preferred over kindInitRVs (they are more recent). This replaces the
	// emptyDir checkpoint for intra-process reconnect resumption.
	gvrBookmarkRVsMu sync.RWMutex
	gvrBookmarkRVs   map[string]string // gvrKey → last bookmarked RV
}

// New creates a new ClusterWatcher
func New(cfg config.ClusterConfig, outputManager *outputs.Manager, logger *zap.Logger) (*ClusterWatcher, error) {
	restConfig, err := buildConfigFromClusterConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build cluster config: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client: %w", err)
	}

	// Create rate limiter (10 QPS, burst of 15)
	rateLimiter := rate.NewLimiter(rate.Limit(10), 15)

	// Create semaphore to limit concurrent LIST operations per cluster (if enabled via global config)
	// This will be set later by manager if concurrency limits are enabled
	var listSemaphore chan struct{}
	if cfg.WatchConfig.MaxListsPerCluster > 0 {
		listSemaphore = make(chan struct{}, cfg.WatchConfig.MaxListsPerCluster)
	}

	return &ClusterWatcher{
		clusterName:      cfg.Name,
		config:           restConfig,
		dynamicClient:    dynamicClient,
		discoveryClient:  discoveryClient,
		outputManager:    outputManager,
		logger:           logger.With(zap.String("cluster", cfg.Name)),
		namespaces:       cfg.Namespaces,
		resourceFilters:  cfg.Resources,
		stopCh:           make(chan struct{}),
		watchedResources: make(map[string]context.CancelFunc),
		rateLimiter:      rateLimiter,
		watchConfig:      cfg.WatchConfig,
		listSemaphore:    listSemaphore,
		kindInitRVs:      make(map[string]string),
		gvrKindMap:       make(map[string]string),
		gvrTier:          make(map[string]Tier),
		gvrBookmarkRVs:   make(map[string]string),
		tierResolver:     newTierResolver(cfg.WatchConfig.Tiers),
		tierConfig:       cfg.WatchConfig.Tiers,
		kindLastEvent:      make(map[string]time.Time),
		kindEventCount:     make(map[string]int64),
		kindLastSeed:       make(map[string]time.Time),
		gvrResyncDeadline:  make(map[string]time.Time),
	}, nil
}

// SetGlobalListSemaphore sets the global LIST semaphore shared across all clusters
func (w *ClusterWatcher) SetGlobalListSemaphore(sem chan struct{}) {
	w.globalListSemaphore = sem
}

// SetSeederChecker attaches a SeederChecker to this watcher. When set,
// the watcher queries per-kind MAX resourceVersions from the output store on
// startup and uses them as WATCH cursors, skipping the initial LIST for
// already-seeded resource types.
func (w *ClusterWatcher) SetSeederChecker(checker SeederChecker) {
	w.seederChecker = checker
}

// Start starts watching resources
func (w *ClusterWatcher) Start(ctx context.Context) error {
	return w.StartWithInitSemaphore(ctx, nil)
}

// StartWithInitSemaphore starts watching resources with optional global init semaphore
func (w *ClusterWatcher) StartWithInitSemaphore(ctx context.Context, initSemaphore chan struct{}) error {
	w.logger.Info("Starting cluster watcher with raw watch endpoints")

	// Start CRD watcher first
	if err := w.startCRDWatcher(ctx); err != nil {
		w.logger.Warn("Failed to start CRD watcher", zap.Error(err))
	}

	// Acquire init semaphore if provided (limits concurrent cluster initializations)
	if initSemaphore != nil {
		w.logger.Info("Waiting for cluster initialization slot...")
		select {
		case initSemaphore <- struct{}{}:
			w.logger.Info("Acquired cluster initialization slot, starting initial sync")
			defer func() {
				<-initSemaphore
				w.logger.Info("Released cluster initialization slot")
			}()
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Query the output store on startup with two queries:
	//
	// 1. GetResumeRVsByKind — the most recent known RV per kind, used as the
	//    initial WATCH cursor so already-active kinds can skip the initial
	//    LIST. A failed query leaves kindInitRVs empty, so every kind LISTs.
	// 2. GetLastSeedTimestampsByKind (when the output implements SeedTracker)
	//    — the most recent SEEDED-sentinel timestamp per kind, used to anchor
	//    the P1/P2/P3 resync schedules across pod restarts.
	//
	// A SEEDED row is only written after a LIST fully completes, so the
	// presence of a timestamp also signals a completed seed. Kinds without a
	// recent SEEDED (never seeded, partially seeded after an OOMKill, or
	// outside the query window) are absent from (2) and the resync schedulers
	// treat them as "due now" — relisting immediately to (re)complete the
	// seed. This replaces the former seed-completion gating query.
	if w.seederChecker != nil {
		var rvsByKind map[string]string
		err := w.querySeederWithRetry(ctx, "resume_rv", func() error {
			var qerr error
			rvsByKind, qerr = w.seederChecker.GetResumeRVsByKind(ctx, w.clusterName)
			return qerr
		})
		if err != nil {
			w.logger.Warn("Resume RV check failed, will LIST all resources",
				zap.String("cluster", w.clusterName),
				zap.Error(err))
			rvsByKind = nil
		}

		if st, ok := w.seederChecker.(SeedTracker); ok {
			// Query last-seed timestamps so P1/P2/P3 schedules persist
			// across restarts. The window must cover the longest interval
			// in use; lastSeedQueryWindow computes it conservatively.
			window := w.lastSeedQueryWindow()
			var lastSeed map[string]time.Time
			err := w.querySeederWithRetry(ctx, "last_seed_ts", func() error {
				var qerr error
				lastSeed, qerr = st.GetLastSeedTimestampsByKind(ctx, w.clusterName, window)
				return qerr
			})
			if err != nil {
				w.logger.Warn("Last-seed timestamp check failed, kinds will relist (due now)",
					zap.String("cluster", w.clusterName),
					zap.Error(err))
			} else if lastSeed != nil {
				w.kindLastSeed = lastSeed
				w.logger.Info("Loaded last-seed timestamps from ClickHouse",
					zap.String("cluster", w.clusterName),
					zap.Int("kinds", len(lastSeed)),
					zap.Duration("window", window))
			}
		}

		if rvsByKind != nil {
			w.kindInitRVsMu.Lock()
			w.kindInitRVs = rvsByKind
			w.kindInitRVsMu.Unlock()

			// Compute global max RV for tier-3 fallback. Kubernetes RVs are decimal
			// strings; use integer comparison to avoid lexicographic ordering errors
			// (e.g. "9" > "10000" lexicographically).
			maxRV := int64(0)
			for _, rv := range rvsByKind {
				if v, err := strconv.ParseInt(rv, 10, 64); err == nil && v > maxRV {
					maxRV = v
				}
			}
			if maxRV > 0 {
				w.globalInitRV = strconv.FormatInt(maxRV, 10)
			}

			if len(rvsByKind) > 0 {
				w.logger.Info("Loaded per-kind resume RVs from ClickHouse",
					zap.String("cluster", w.clusterName),
					zap.Int("kinds", len(rvsByKind)))
			} else {
				w.logger.Info("Cluster not seeded in ClickHouse, will LIST all resources",
					zap.String("cluster", w.clusterName))
			}
		}
	}

	// Initial discovery
	if err := w.discoverAndWatch(ctx); err != nil {
		return fmt.Errorf("initial discovery failed: %w", err)
	}

	// Mark initial sync as complete
	w.initialSyncMu.Lock()
	w.initialSyncDone = true
	w.initialSyncMu.Unlock()

	// Wait for context cancellation
	<-ctx.Done()

	// Cancel all watches
	w.watchedResourcesMu.Lock()
	for _, cancel := range w.watchedResources {
		cancel()
	}
	w.watchedResourcesMu.Unlock()

	close(w.stopCh)
	w.wg.Wait()

	return nil
}

// startCRDWatcher starts watching CustomResourceDefinitions for changes
func (w *ClusterWatcher) startCRDWatcher(ctx context.Context) error {
	crdGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.watchCRDs(ctx, crdGVR)
	}()

	return nil
}

// watchCRDs watches CRDs and triggers discovery when they change
func (w *ClusterWatcher) watchCRDs(ctx context.Context, crdGVR schema.GroupVersionResource) {
	backoff := w.watchConfig.InitialBackoff
	initialSync := true

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := w.rateLimiter.Wait(ctx); err != nil {
			return
		}

		err := w.watchCRDResource(ctx, crdGVR, &initialSync)
		if err != nil {
			w.logger.Warn("CRD watch failed, reconnecting", zap.Error(err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				backoff = min(backoff*2, w.watchConfig.MaxBackoff)
			}
		} else {
			backoff = w.watchConfig.InitialBackoff
		}
	}
}

// watchCRDResource performs the LIST+WATCH for CRDs
func (w *ClusterWatcher) watchCRDResource(ctx context.Context, crdGVR schema.GroupVersionResource, initialSync *bool) error {
	// LIST CRDs
	list, err := w.dynamicClient.Resource(crdGVR).List(ctx, v1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list CRDs failed: %w", err)
	}

	// Only trigger discovery after initial sync
	if !*initialSync {
		w.logger.Info("CRD change detected during sync, triggering resource discovery")
		go w.discoverAndWatch(ctx) // Non-blocking
	}
	*initialSync = false

	// WATCH for CRD changes
	watchOpts := v1.ListOptions{
		ResourceVersion: list.GetResourceVersion(),
		TimeoutSeconds:  int64Ptr(int64(w.watchConfig.TimeoutSeconds)),
	}

	watcher, err := w.dynamicClient.Resource(crdGVR).Watch(ctx, watchOpts)
	if err != nil {
		return fmt.Errorf("watch CRDs failed: %w", err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("watch channel closed")
			}

			switch event.Type {
			case watch.Added, watch.Modified:
				// Trigger discovery when CRDs change
				if unstructuredObj, ok := event.Object.(*unstructured.Unstructured); ok {
					w.logger.Info("CRD change detected, triggering resource discovery",
						zap.String("crd", unstructuredObj.GetName()),
						zap.String("event", string(event.Type)))
				}
				go w.discoverAndWatch(ctx) // Non-blocking
			case watch.Error:
				if status, ok := event.Object.(*v1.Status); ok {
					return fmt.Errorf("watch error: %s (code %d)", status.Message, status.Code)
				}
				return fmt.Errorf("watch error")
			}
		}
	}
}

// discoverAndWatch discovers resources and starts the appropriate per-tier
// path for each. P1 and P2 kinds get the standard LIST+WATCH path (tier
// recorded in gvrTier so handleObject and the SEEDED emit can route to the
// right output buffer). P3 kinds get a relist-only loop. P4 kinds get a
// WATCH-only-from-now stream loop (no LIST, no relist, no seed). P5 kinds
// are ignored entirely. The single per-cluster P1 sequential resync loop is
// started lazily the first time a P1 kind is observed.
func (w *ClusterWatcher) discoverAndWatch(ctx context.Context) error {
	resources, err := w.discoverResources()
	if err != nil {
		return fmt.Errorf("failed to discover resources: %w", err)
	}

	newResourceCount := 0
	totalResourceCount := 0
	sawP1 := false

	for _, resource := range resources {
		if !w.shouldWatchResource(resource) {
			continue
		}

		gvr := schema.GroupVersionResource{
			Group:    resource.Group,
			Version:  resource.Version,
			Resource: resource.Name,
		}
		resourceKey := gvrToString(gvr)

		apiVersion := resource.Version
		if resource.Group != "" {
			apiVersion = resource.Group + "/" + resource.Version
		}
		assignment := w.tierResolver.resolve(apiVersion, resource.Kind)

		// P5 kinds are explicitly ignored. Skip before populating gvrKindMap
		// or gvrTier so they don't pollute status counts or the resync loop.
		if assignment.tier == TierP5 {
			w.logger.Debug("Skipping P5 (ignored) kind",
				zap.String("kind", resource.Kind),
				zap.String("apiVersion", apiVersion))
			continue
		}

		totalResourceCount++

		// Keep gvrKindMap and gvrTier current so handleObject and watchResource
		// can look up the kind/tier for an in-flight event.
		w.gvrKindMu.Lock()
		w.gvrKindMap[resourceKey] = resource.Kind
		w.gvrTier[resourceKey] = assignment.tier
		w.gvrKindMu.Unlock()

		if assignment.tier == TierP1 {
			sawP1 = true
		}

		w.watchedResourcesMu.RLock()
		_, alreadyWatching := w.watchedResources[resourceKey]
		w.watchedResourcesMu.RUnlock()

		if alreadyWatching {
			continue
		}

		switch assignment.tier {
		case TierP3:
			if err := w.startP3RelistLoop(ctx, gvr, assignment.interval); err != nil {
				w.logger.Warn("Failed to start P3 relist loop",
					zap.String("resource", resourceKey),
					zap.Error(err))
				continue
			}
		case TierP4:
			if err := w.startP4StreamLoop(ctx, gvr); err != nil {
				w.logger.Warn("Failed to start P4 stream loop",
					zap.String("resource", resourceKey),
					zap.Error(err))
				continue
			}
		default:
			// P1 and P2 share the LIST+WATCH path; tier-aware send routing is
			// done downstream in handleObject and the SEEDED emit site.
			if err := w.startWatchingResourceRaw(ctx, gvr); err != nil {
				w.logger.Warn("Failed to start watching resource",
					zap.String("resource", resourceKey),
					zap.Error(err))
				continue
			}
		}

		newResourceCount++
		w.logger.Info("Started resource path",
			zap.String("group", resource.Group),
			zap.String("version", resource.Version),
			zap.String("resource", resource.Name),
			zap.String("tier", assignment.tier.String()))
	}

	// Lazily start the per-cluster P1 sequential resync loop once we've seen
	// at least one P1 kind. Started under sync.Once so re-entries from CRD
	// change discovery don't spawn duplicates.
	if sawP1 {
		w.p1ResyncOnce.Do(func() {
			w.wg.Add(1)
			go func() {
				defer w.wg.Done()
				w.runP1ResyncLoop(ctx)
			}()
		})
	}

	if newResourceCount > 0 {
		w.logger.Info("Discovery complete",
			zap.Int("new_resources", newResourceCount),
			zap.Int("total_watched", totalResourceCount))
	} else {
		w.logger.Debug("No new resources discovered",
			zap.Int("total_watched", totalResourceCount))
	}

	return nil
}

// startWatchingResourceRaw starts watching a resource using raw LIST+WATCH
func (w *ClusterWatcher) startWatchingResourceRaw(ctx context.Context, gvr schema.GroupVersionResource) error {
	resourceKey := gvrToString(gvr)

	// Create cancellable context for this watch
	watchCtx, cancel := context.WithCancel(ctx)

	// Atomically check-and-reserve under the write lock. If a concurrent
	// discoverAndWatch goroutine already started this GVR between our read-check
	// and here, cancel the context we just created and return without starting a
	// duplicate goroutine.
	w.watchedResourcesMu.Lock()
	if _, exists := w.watchedResources[resourceKey]; exists {
		w.watchedResourcesMu.Unlock()
		cancel()
		return nil
	}
	w.watchedResources[resourceKey] = cancel
	w.watchedResourcesMu.Unlock()

	// Start watch loop in goroutine
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer cancel()
		w.watchLoop(watchCtx, gvr)
	}()

	return nil
}

// watchLoop implements reconnection logic for a resource watch
func (w *ClusterWatcher) watchLoop(ctx context.Context, gvr schema.GroupVersionResource) {
	backoff := w.watchConfig.InitialBackoff

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Rate limit API calls
		if err := w.rateLimiter.Wait(ctx); err != nil {
			return
		}

		err := w.watchResource(ctx, gvr)
		if err != nil {
			w.logger.Warn("Watch failed, reconnecting",
				zap.String("resource", gvrToString(gvr)),
				zap.Error(err),
				zap.Duration("backoff", backoff))

			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				backoff = min(backoff*2, w.watchConfig.MaxBackoff)
			}
		} else {
			backoff = w.watchConfig.InitialBackoff // Reset on clean exit
		}
	}
}

// clearGVRResumeState drops the per-GVR bookmark and per-kind init RV so the
// next watchResource iteration enters the full LIST path. Called only when the
// resync timer fires (errResyncTriggered) — not on other clean watch exits.
func (w *ClusterWatcher) clearGVRResumeState(gvr schema.GroupVersionResource) {
	gvrKey := gvrToString(gvr)
	w.gvrBookmarkRVsMu.Lock()
	delete(w.gvrBookmarkRVs, gvrKey)
	w.gvrBookmarkRVsMu.Unlock()

	w.gvrKindMu.RLock()
	kind := w.gvrKindMap[gvrKey]
	w.gvrKindMu.RUnlock()
	if kind != "" {
		w.kindInitRVsMu.Lock()
		delete(w.kindInitRVs, kind)
		w.kindInitRVsMu.Unlock()
	}
}

// watchResource performs LIST+WATCH for a single resource type.
//
// Resume RV priority:
//  1. Per-GVR bookmark from a prior watch run in this process (most recent).
//  2. Per-kind RV from the ClickHouse MAX query at startup (init path).
//  3. "" → full LIST (cluster not seeded, or seeder check failed).
//
// On a 410-Gone response the per-GVR bookmark is cleared and the function
// falls through to a full LIST, which re-seeds CH for this resource type.
func (w *ClusterWatcher) watchResource(ctx context.Context, gvr schema.GroupVersionResource) error {
	gvrKey := gvrToString(gvr)

	// Determine resume RV: prefer the in-memory bookmark (more recent than the
	// CH-derived init RV), then fall back to the per-kind CH init RV.
	resumeRV := ""
	w.gvrBookmarkRVsMu.RLock()
	resumeRV = w.gvrBookmarkRVs[gvrKey]
	w.gvrBookmarkRVsMu.RUnlock()

	if resumeRV == "" {
		w.gvrKindMu.RLock()
		kind := w.gvrKindMap[gvrKey]
		w.gvrKindMu.RUnlock()
		if kind != "" {
			w.kindInitRVsMu.RLock()
			resumeRV = w.kindInitRVs[kind]
			w.kindInitRVsMu.RUnlock()
		}
	}

	// If we have a resume RV, attempt to WATCH from it (skip LIST).
	ranTier2With410 := false
	if resumeRV != "" {
		wi, err := w.dynamicClient.Resource(gvr).Namespace(v1.NamespaceAll).Watch(ctx, v1.ListOptions{
			ResourceVersion:     resumeRV,
			AllowWatchBookmarks: w.watchConfig.AllowBookmarks,
			TimeoutSeconds:      int64Ptr(int64(w.watchConfig.TimeoutSeconds)),
		})
		if err == nil {
			loopErr := w.runWatchEventLoop(ctx, gvr, wi)
			wi.Stop()
			if errors.Is(loopErr, errResyncTriggered) {
				w.clearGVRResumeState(gvr)
				return nil
			}
			if !isExpiredResourceVersion(loopErr) {
				return loopErr // clean exit, context cancelled, or real error
			}
			// 410 Gone — RV too old; clear the bookmark and fall through.
			ranTier2With410 = true
			w.logger.Warn("Resume RV expired (410), trying global init RV",
				zap.String("resource", gvrKey),
				zap.String("resourceVersion", resumeRV))
			w.gvrBookmarkRVsMu.Lock()
			delete(w.gvrBookmarkRVs, gvrKey)
			w.gvrBookmarkRVsMu.Unlock()
		}
		// Watch() error (e.g. auth failure) — fall through to full LIST.
	}

	// Tier 3: try the global max RV across all kinds from the CH startup query.
	// This avoids a full LIST for idle kinds whose per-kind CH RV is stale but
	// other kinds in the same cluster are still active.
	//
	// Safety: if CH shows kind X at RV=1000 (stale), it means zero events for
	// kind X occurred after RV 1000. Watching X from a higher global RV (e.g.
	// 5000 from active Secrets) misses nothing — those events don't exist.
	//
	// Only attempted when tier 1+2 specifically got 410-Gone (not auth errors),
	// and only when globalInitRV differs from resumeRV (same RV would also 410).
	if ranTier2With410 && w.globalInitRV != "" && w.globalInitRV != resumeRV {
		wi, err := w.dynamicClient.Resource(gvr).Namespace(v1.NamespaceAll).Watch(ctx, v1.ListOptions{
			ResourceVersion:     w.globalInitRV,
			AllowWatchBookmarks: w.watchConfig.AllowBookmarks,
			TimeoutSeconds:      int64Ptr(int64(w.watchConfig.TimeoutSeconds)),
		})
		if err == nil {
			loopErr := w.runWatchEventLoop(ctx, gvr, wi)
			wi.Stop()
			if errors.Is(loopErr, errResyncTriggered) {
				w.clearGVRResumeState(gvr)
				return nil
			}
			if !isExpiredResourceVersion(loopErr) {
				return loopErr
			}
			w.logger.Warn("Global init RV also expired (410), falling back to full LIST",
				zap.String("resource", gvrKey),
				zap.String("globalInitRV", w.globalInitRV))
		}
		// 410-Gone or Watch() error — fall through to full LIST.
	}

	// --- Full LIST path ---

	// Acquire GLOBAL semaphore first (limits total LISTs across all clusters)
	if w.globalListSemaphore != nil {
		select {
		case w.globalListSemaphore <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Acquire per-cluster semaphore to limit concurrent LIST operations
	select {
	case w.listSemaphore <- struct{}{}:
	case <-ctx.Done():
		if w.globalListSemaphore != nil {
			<-w.globalListSemaphore
		}
		return ctx.Err()
	}

	list, err := w.listResourcePaginated(ctx, gvr)

	<-w.listSemaphore
	if w.globalListSemaphore != nil {
		<-w.globalListSemaphore
	}

	if err != nil {
		return fmt.Errorf("list failed: %w", err)
	}

	// Save the LIST RV as the bookmark for this GVR so reconnects use it
	// instead of the (now-stale) CH init RV.
	w.gvrBookmarkRVsMu.Lock()
	w.gvrBookmarkRVs[gvrKey] = list.GetResourceVersion()
	w.gvrBookmarkRVsMu.Unlock()

	// Advance the resync deadline: a real LIST just happened, so the next
	// periodic relist is one full interval out. This is the only site that
	// moves the deadline forward — startup LIST, resync-triggered re-LIST,
	// and 410-fallback LIST all funnel through here. Reconnects that resume
	// from a bookmark never reach this point, so they cannot slip the
	// schedule.
	w.advanceP2ResyncDeadline(gvrKey, time.Now())

	// Forward all existing objects as EXISTING (not ADDED).
	for i := range list.Items {
		w.handleObject(&list.Items[i], "EXISTING")
	}

	// Queue a SEEDED sentinel through the normal output channel so it batches
	// naturally with other events instead of forcing a per-kind flush + separate
	// single-row INSERT. Sentinel write failure is non-fatal — the next restart
	// will just re-LIST.
	w.gvrKindMu.RLock()
	kind := w.gvrKindMap[gvrKey]
	w.gvrKindMu.RUnlock()
	if kind != "" {
		sentinelObj := map[string]interface{}{
			"kind": kind,
			"metadata": map[string]interface{}{
				"resourceVersion": list.GetResourceVersion(),
			},
		}
		// Initial-LIST SEEDED for any tier flows through the regular buffer.
		// The atomic per-kind flush guarantee only applies to the periodic
		// P1 resync path, which uses SendP1+FlushP1 directly.
		if err := w.outputManager.Send(types.Event{
			Timestamp:   time.Now().UTC(),
			ClusterName: w.clusterName,
			Operation:   "SEEDED",
			Object:      sentinelObj,
		}); err != nil {
			w.logger.Warn("Failed to queue LIST complete sentinel",
				zap.String("resource", gvrKey),
				zap.String("kind", kind),
				zap.Error(err))
		}
	}

	// Start WATCH from the LIST resourceVersion.
	watchOpts := v1.ListOptions{
		ResourceVersion:     list.GetResourceVersion(),
		AllowWatchBookmarks: w.watchConfig.AllowBookmarks,
		TimeoutSeconds:      int64Ptr(int64(w.watchConfig.TimeoutSeconds)),
	}

	watcher, err := w.dynamicClient.Resource(gvr).Namespace(v1.NamespaceAll).Watch(ctx, watchOpts)
	if err != nil {
		return fmt.Errorf("watch failed: %w", err)
	}
	defer watcher.Stop()

	loopErr := w.runWatchEventLoop(ctx, gvr, watcher)
	if errors.Is(loopErr, errResyncTriggered) {
		w.clearGVRResumeState(gvr)
		return nil
	}
	return loopErr
}

// initialP2ResyncDeadline computes the first-arming resync deadline for a P2
// GVR. When the kind's most recent SEEDED timestamp is known, the deadline is
// lastSeed+interval (survives pod restart). Otherwise it is "now" (fail-safe:
// the kind is never/partially seeded or the last-seed query failed, so relist
// immediately to (re)complete the seed).
func initialP2ResyncDeadline(now time.Time, lastSeed time.Time, seeded bool, interval time.Duration) time.Time {
	if seeded {
		return lastSeed.Add(interval)
	}
	return now
}

// p2ResyncDeadline returns the absolute resync deadline for gvrKey, lazily
// initializing and storing it on first call. Subsequent calls (e.g. on watch
// reconnect) return the stored value unchanged, so reconnect churn cannot slip
// the schedule. The deadline only advances via advanceP2ResyncDeadline when a
// real LIST completes.
func (w *ClusterWatcher) p2ResyncDeadline(gvrKey string, now time.Time) time.Time {
	w.gvrKindMu.Lock()
	defer w.gvrKindMu.Unlock()
	if deadline, ok := w.gvrResyncDeadline[gvrKey]; ok {
		return deadline
	}
	var lastSeed time.Time
	var seeded bool
	if kind := w.gvrKindMap[gvrKey]; kind != "" {
		lastSeed, seeded = w.kindLastSeed[kind]
	}
	deadline := initialP2ResyncDeadline(now, lastSeed, seeded, w.watchConfig.ResyncInterval)
	w.gvrResyncDeadline[gvrKey] = deadline
	return deadline
}

// advanceP2ResyncDeadline pushes the resync deadline for gvrKey to now+interval.
// Called from the full-LIST path after a successful LIST — the only place the
// deadline moves forward, so reconnects that resume from a bookmark cannot
// affect the schedule. No-op when resync is disabled.
func (w *ClusterWatcher) advanceP2ResyncDeadline(gvrKey string, now time.Time) {
	if w.watchConfig.ResyncInterval <= 0 {
		return
	}
	w.gvrKindMu.Lock()
	w.gvrResyncDeadline[gvrKey] = now.Add(w.watchConfig.ResyncInterval)
	w.gvrKindMu.Unlock()
}

// runWatchEventLoop drains events from wi until the context is cancelled, the
// channel closes, or an error event is received. BOOKMARK events update the
// in-memory gvrBookmarkRVs map so that a subsequent reconnect can resume from
// the most recent RV without doing a full LIST.
func (w *ClusterWatcher) runWatchEventLoop(ctx context.Context, gvr schema.GroupVersionResource, wi watch.Interface) error {
	gvrKey := gvrToString(gvr)

	// If resync is enabled for this (P2) GVR, arm a timer from its absolute
	// resync deadline. When the timer fires the watch closes cleanly, causing
	// watchLoop to re-enter watchResource which does a full re-LIST.
	//
	// The deadline is stored per-GVR and only advanced when a real LIST
	// completes (see watchResource), so reconnects within the same process
	// re-arm from the *same* deadline and cannot push it out. It is lazily
	// initialized here on the first arming:
	//   - last SEEDED known  → lastSeed + interval (survives pod restart so a
	//     4-day-uptime pod on a 5-day interval picks up where it left off).
	//   - last SEEDED unknown (never seeded, partial seed, query failed) →
	//     "now" (fail-safe: relist immediately to (re)complete the seed).
	//
	// Only P2 arms this timer. P1 relists via runP1ResyncLoop and P3 via
	// runP3RelistLoop, so arming here for them would double-relist.
	var resyncCh <-chan time.Time
	if w.watchConfig.ResyncInterval > 0 {
		w.gvrKindMu.RLock()
		isP2 := w.gvrTier[gvrKey] == TierP2
		w.gvrKindMu.RUnlock()
		if isP2 {
			// A negative duration makes time.After fire immediately.
			deadline := w.p2ResyncDeadline(gvrKey, time.Now())
			resyncCh = time.After(time.Until(deadline))
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-resyncCh:
			w.logger.Debug("Resync timer fired, closing watch to force re-LIST",
				zap.String("resource", gvrKey),
				zap.Duration("resyncInterval", w.watchConfig.ResyncInterval))
			return errResyncTriggered
		case event, ok := <-wi.ResultChan():
			if !ok {
				return fmt.Errorf("watch channel closed")
			}

			// Advance the in-memory bookmark on BOOKMARK events (periodic
			// heartbeats from the API server carrying the current RV).
			if event.Type == watch.Bookmark {
				if obj, ok := event.Object.(*unstructured.Unstructured); ok {
					if rv := obj.GetResourceVersion(); rv != "" {
						w.gvrBookmarkRVsMu.Lock()
						w.gvrBookmarkRVs[gvrKey] = rv
						w.gvrBookmarkRVsMu.Unlock()
					}
				}
			}

			if err := w.handleWatchEvent(event); err != nil {
				return err
			}
		}
	}
}

// isExpiredResourceVersion reports whether err indicates that the resourceVersion
// used to open a WATCH has expired on the API server (HTTP 410 Gone). When true,
// the caller should clear the stored RV and fall back to a full LIST.
func isExpiredResourceVersion(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "code 410")
}

// listResourcePaginated performs a paginated LIST operation for resources
func (w *ClusterWatcher) listResourcePaginated(ctx context.Context, gvr schema.GroupVersionResource) (*unstructured.UnstructuredList, error) {
	var allItems []unstructured.Unstructured
	var finalResourceVersion string
	continueToken := ""
	pageSize := w.watchConfig.ListPageSize

	// If page size is negative, disable pagination
	if pageSize < 0 {
		list, err := w.dynamicClient.Resource(gvr).Namespace(v1.NamespaceAll).
			List(ctx, v1.ListOptions{})
		return list, err
	}

	for {
		listOptions := v1.ListOptions{
			Limit:    pageSize,
			Continue: continueToken,
		}

		list, err := w.dynamicClient.Resource(gvr).Namespace(v1.NamespaceAll).
			List(ctx, listOptions)
		if err != nil {
			return nil, fmt.Errorf("paginated list failed (page token: %s): %w", continueToken, err)
		}

		allItems = append(allItems, list.Items...)
		finalResourceVersion = list.GetResourceVersion()

		// Check if there are more pages
		continueToken = list.GetContinue()
		if continueToken == "" {
			break
		}
	}

	// Return a combined list with all items
	result := &unstructured.UnstructuredList{
		Items: allItems,
	}
	result.SetResourceVersion(finalResourceVersion)

	return result, nil
}

// handleWatchEvent processes a watch event
func (w *ClusterWatcher) handleWatchEvent(event watch.Event) error {
	switch event.Type {
	case watch.Added:
		w.handleObject(event.Object, "ADDED")
	case watch.Modified:
		w.handleObject(event.Object, "MODIFIED")
	case watch.Deleted:
		w.handleObject(event.Object, "DELETED")
	case watch.Error:
		if status, ok := event.Object.(*v1.Status); ok {
			return fmt.Errorf("watch error: %s (code %d)", status.Message, status.Code)
		}
		return fmt.Errorf("watch error")
	case watch.Bookmark:
		// Checkpoint event - no action needed
	}
	return nil
}

// discoverResources discovers all available resources in the cluster
func (w *ClusterWatcher) discoverResources() ([]v1.APIResource, error) {
	var allResources []v1.APIResource

	// Get all API groups
	groups, err := w.discoveryClient.ServerGroups()
	if err != nil {
		return nil, err
	}

	for _, group := range groups.Groups {
		for _, version := range group.Versions {
			gv := version.GroupVersion

			resourceList, err := w.discoveryClient.ServerResourcesForGroupVersion(gv)
			if err != nil {
				w.logger.Warn("Failed to get resources for group version",
					zap.String("groupVersion", gv),
					zap.Error(err),
				)
				continue
			}

			for _, resource := range resourceList.APIResources {
				// Only watch resources that support list and watch
				if !contains(resource.Verbs, "list") || !contains(resource.Verbs, "watch") {
					continue
				}

				// Skip subresources
				if strings.Contains(resource.Name, "/") {
					continue
				}

				// Parse group and version from groupVersion
				parts := strings.Split(gv, "/")
				if len(parts) == 1 {
					// Core API group (v1)
					resource.Group = ""
					resource.Version = parts[0]
				} else {
					resource.Group = parts[0]
					resource.Version = parts[1]
				}

				allResources = append(allResources, resource)
			}
		}
	}

	return allResources, nil
}

// shouldWatchResource determines if a resource should be watched
func (w *ClusterWatcher) shouldWatchResource(resource v1.APIResource) bool {
	// If no filters specified, watch everything
	if len(w.resourceFilters) == 0 {
		return true
	}

	// Check if resource matches any filter
	for _, filter := range w.resourceFilters {
		apiVersion := resource.Version
		if resource.Group != "" {
			apiVersion = resource.Group + "/" + resource.Version
		}

		if filter.APIVersion == apiVersion && filter.Kind == resource.Kind {
			return true
		}
	}

	return false
}

// handleObject handles a Kubernetes object event from the WATCH event loop.
// All WATCH events — for any tier — go to the regular output buffer. The
// dedicated P1 buffer is reserved for the periodic P1 resync flow, which
// needs atomic per-kind flushes; individual watch events have no such
// requirement and benefit from batching with general traffic.
func (w *ClusterWatcher) handleObject(obj interface{}, operation string) {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()

		unstructuredObj, ok := obj.(*unstructured.Unstructured)
		if !ok {
			w.logger.Error("Failed to convert object to unstructured")
			return
		}

		// Check namespace filter
		if len(w.namespaces) > 0 && !contains(w.namespaces, unstructuredObj.GetNamespace()) {
			return
		}

		event := w.objectToEvent(unstructuredObj, operation)
		if err := w.outputManager.Send(event); err != nil {
			w.logger.Error("Failed to send event",
				zap.String("kind", unstructuredObj.GetKind()),
				zap.String("namespace", unstructuredObj.GetNamespace()),
				zap.String("name", unstructuredObj.GetName()),
				zap.String("operation", operation),
				zap.Error(err),
			)
		} else {
			w.recordEvent(unstructuredObj.GetKind(), operation)
			w.logger.Debug("Sent event",
				zap.String("kind", unstructuredObj.GetKind()),
				zap.String("namespace", unstructuredObj.GetNamespace()),
				zap.String("name", unstructuredObj.GetName()),
				zap.String("operation", operation),
			)
		}
	}()
}

// recordEvent updates the per-kind activity stats consumed by the periodic
// status log. SEEDED sentinels are skipped so the log reflects "last K8s
// event observed" rather than "last LIST cycle completed".
func (w *ClusterWatcher) recordEvent(kind, operation string) {
	if kind == "" || operation == "SEEDED" {
		return
	}
	w.kindStatsMu.Lock()
	w.kindLastEvent[kind] = time.Now()
	w.kindEventCount[kind]++
	w.kindStatsMu.Unlock()
}

// objectToEvent converts an unstructured object to an event
func (w *ClusterWatcher) objectToEvent(obj *unstructured.Unstructured, operation string) types.Event {
	// Hash Secret data before creating the event
	processedObject := hashSecretData(obj.Object)

	// Sanitize keys containing periods (which can cause issues in ClickHouse/databases)
	//processedObject = sanitizeKeys(processedObject)

	return types.Event{
		Timestamp:   time.Now().UTC(),
		ClusterName: w.clusterName,
		Operation:   operation,
		Object:      processedObject,
	}
}

// redactedValue returns a random hex string to replace a secret value.
// Using crypto/rand ensures no mathematical relationship to the original value.
// hashSecretData replaces Secret data values with a redaction marker to prevent
// exposing sensitive information. Key names are preserved.
func hashSecretData(obj map[string]interface{}) map[string]interface{} {
	// Check if this is a Secret object
	kind, ok := obj["kind"].(string)
	if !ok || kind != "Secret" {
		return obj
	}

	// Create a deep copy to avoid mutating the original
	objCopy := deepCopyMap(obj)

	if data, ok := objCopy["data"].(map[string]interface{}); ok {
		redacted := make(map[string]interface{}, len(data))
		for key := range data {
			redacted[key] = "REDACTED"
		}
		objCopy["data"] = redacted
	}

	if stringData, ok := objCopy["stringData"].(map[string]interface{}); ok {
		redacted := make(map[string]interface{}, len(stringData))
		for key := range stringData {
			redacted[key] = "REDACTED"
		}
		objCopy["stringData"] = redacted
	}

	// Strip last-applied-configuration from Secret annotations — this annotation
	// is written by kubectl apply / ArgoCD / Flux and contains the full last-applied
	// manifest, which for Secrets includes the base64-encoded secret values.
	if meta, ok := objCopy["metadata"].(map[string]interface{}); ok {
		if annots, ok := meta["annotations"].(map[string]interface{}); ok {
			delete(annots, "kubectl.kubernetes.io/last-applied-configuration")
		}
	}

	return objCopy
}

// deepCopyMap creates a deep copy of a map[string]interface{}
func deepCopyMap(m map[string]interface{}) map[string]interface{} {
	copy := make(map[string]interface{})
	for k, v := range m {
		switch val := v.(type) {
		case map[string]interface{}:
			copy[k] = deepCopyMap(val)
		case []interface{}:
			copy[k] = deepCopySlice(val)
		default:
			copy[k] = v
		}
	}
	return copy
}

// deepCopySlice creates a deep copy of a []interface{}
func deepCopySlice(s []interface{}) []interface{} {
	copy := make([]interface{}, len(s))
	for i, v := range s {
		switch val := v.(type) {
		case map[string]interface{}:
			copy[i] = deepCopyMap(val)
		case []interface{}:
			copy[i] = deepCopySlice(val)
		default:
			copy[i] = v
		}
	}
	return copy
}

// sanitizeKeys replaces periods in map keys with underscores to prevent issues
// with databases that interpret periods as nested field accessors (e.g., ClickHouse)
// Optimized to only allocate new maps/slices when periods are actually found
func sanitizeKeys(obj map[string]interface{}) map[string]interface{} {
	needsSanitization := false

	// First pass: check if any key contains a period
	for key := range obj {
		if strings.Contains(key, ".") {
			needsSanitization = true
			break
		}
	}

	// If no periods found at this level, still need to check nested structures
	if !needsSanitization {
		// Check if we need to recurse into nested structures
		hasNested := false
		for _, value := range obj {
			switch value.(type) {
			case map[string]interface{}, []interface{}:
				hasNested = true
				break
			}
		}

		// No periods and no nested structures - return as-is
		if !hasNested {
			return obj
		}
	}

	// Sanitization needed - create new map
	sanitized := make(map[string]interface{}, len(obj))

	for key, value := range obj {
		// Replace periods with underscores in the key
		sanitizedKey := key
		if strings.Contains(key, ".") {
			sanitizedKey = strings.ReplaceAll(key, ".", "_")
		}

		// Recursively sanitize nested structures
		switch val := value.(type) {
		case map[string]interface{}:
			sanitized[sanitizedKey] = sanitizeKeys(val)
		case []interface{}:
			sanitized[sanitizedKey] = sanitizeKeysInSlice(val)
		default:
			sanitized[sanitizedKey] = value
		}
	}

	return sanitized
}

// sanitizeKeysInSlice recursively sanitizes keys in slices containing maps
func sanitizeKeysInSlice(s []interface{}) []interface{} {
	// Check if slice contains any maps that might need sanitization
	hasMap := false
	for _, item := range s {
		if _, ok := item.(map[string]interface{}); ok {
			hasMap = true
			break
		}
		if _, ok := item.([]interface{}); ok {
			hasMap = true
			break
		}
	}

	// No maps or nested slices - return as-is
	if !hasMap {
		return s
	}

	sanitized := make([]interface{}, len(s))

	for i, item := range s {
		switch val := item.(type) {
		case map[string]interface{}:
			sanitized[i] = sanitizeKeys(val)
		case []interface{}:
			sanitized[i] = sanitizeKeysInSlice(val)
		default:
			sanitized[i] = item
		}
	}

	return sanitized
}

// buildConfigFromClusterConfig builds a Kubernetes client configuration from ClusterConfig
func buildConfigFromClusterConfig(cfg config.ClusterConfig) (*rest.Config, error) {
	// Check if using service account authentication
	if cfg.ServiceAccount != nil {
		return buildConfigFromServiceAccount(cfg.ServiceAccount)
	}

	// Otherwise use kubeconfig
	return buildConfig(cfg.Kubeconfig, cfg.Context)
}

// buildConfigFromServiceAccount builds a Kubernetes client configuration from service account
func buildConfigFromServiceAccount(sa *config.ServiceAccountAuth) (*rest.Config, error) {
	if sa == nil {
		return nil, fmt.Errorf("service account configuration is nil")
	}

	restConfig := &rest.Config{
		Host: sa.Server,
	}

	// Load token
	var token string
	if sa.Token != "" {
		token = sa.Token
	} else if sa.TokenFile != "" {
		tokenBytes, err := os.ReadFile(sa.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read token file %q: %w", sa.TokenFile, err)
		}
		token = strings.TrimSpace(string(tokenBytes))
	}
	restConfig.BearerToken = token

	// Configure TLS
	if sa.Insecure {
		restConfig.TLSClientConfig.Insecure = true
	} else {
		// Load CA certificate
		if sa.CAData != "" {
			caBytes, err := base64.StdEncoding.DecodeString(sa.CAData)
			if err != nil {
				return nil, fmt.Errorf("failed to decode ca_data: %w", err)
			}
			restConfig.TLSClientConfig.CAData = caBytes
		} else if sa.CAFile != "" {
			caBytes, err := os.ReadFile(sa.CAFile)
			if err != nil {
				return nil, fmt.Errorf("failed to read CA file %q: %w", sa.CAFile, err)
			}
			restConfig.TLSClientConfig.CAData = caBytes
		}
	}

	return restConfig, nil
}

// buildConfig builds a Kubernetes client configuration
func buildConfig(kubeconfig, context string) (*rest.Config, error) {
	// If running in-cluster
	if kubeconfig == "" {
		config, err := rest.InClusterConfig()
		if err == nil {
			return config, nil
		}
	}

	// Use kubeconfig
	if kubeconfig == "" {
		if home := homedir.HomeDir(); home != "" {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}

	// Override context if specified
	if context != "" {
		configOverrides := &clientcmd.ConfigOverrides{
			CurrentContext: context,
		}
		config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
			configOverrides,
		).ClientConfig()
		if err != nil {
			return nil, err
		}
	}

	return config, nil
}

// contains checks if a string slice contains a value
func contains(slice []string, value string) bool {
	for _, item := range slice {
		if item == value {
			return true
		}
	}
	return false
}

// gvrToString converts a GroupVersionResource to a string key
func gvrToString(gvr schema.GroupVersionResource) string {
	return fmt.Sprintf("%s/%s/%s", gvr.Group, gvr.Version, gvr.Resource)
}

// int64Ptr returns a pointer to an int64
func int64Ptr(i int64) *int64 {
	return &i
}

// min returns the minimum of two durations
func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
