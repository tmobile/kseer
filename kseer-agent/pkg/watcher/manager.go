package watcher

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kseer/kseer-agent/pkg/config"
	"github.com/kseer/kseer-agent/pkg/outputs"
	"github.com/kseer/kseer-agent/pkg/vault"
)

// PodShardChecker queries the events table for per-cluster unique pod counts,
// used to compute pod-count-weighted shard assignment at startup and on anchor changes.
type PodShardChecker interface {
	GetClusterPodCounts(ctx context.Context, start, end time.Time) (map[string]int64, error)
}

// Manager manages multiple cluster watchers
type Manager struct {
	watchers      map[string]*ClusterWatcher
	watchersMu    sync.RWMutex
	cancelFuncs   map[string]context.CancelFunc
	outputManager *outputs.Manager
	logger        *zap.Logger
	vaultClient   *vault.Client
	config        *config.Config
	// Global semaphore to limit cluster initialization (one at a time)
	clusterInitSemaphore chan struct{}
	// Global semaphore to limit total concurrent LIST operations across all clusters
	globalListSemaphore chan struct{}
	// Kubernetes client for local-cluster operations (StatefulSet scale-out)
	k8sClient   *kubernetes.Clientset // nil when not in-cluster
	namespace   string
	releaseName string
	// shardID is the effective shard index for this pod. Derived from KSEER_SHARD_ID
	// env var, config, or the StatefulSet pod hostname ordinal (e.g. "kseer-agent-3" → 3).
	shardID int
	// neededShardTotal is ceil(vaultClusterCount / clustersPerShard), updated each Vault refresh.
	// It drives both rendezvous assignment and auto scale-out.
	neededShardTotal int
	neededTotalMu    sync.RWMutex
	// seederChecker queries the ClickHouse output for per-kind resume RVs.
	// Distributed to each ClusterWatcher so seeded clusters skip the initial LIST.
	// nil when ClickHouse is not configured as an output.
	seederChecker SeederChecker
	// podShardChecker queries CH for per-cluster unique pod counts.
	// Used to compute pod-volume-weighted shard assignment when PodsPerShard > 0.
	podShardChecker   PodShardChecker
	clusterPodCounts  map[string]int64 // populated at startup / anchor change; nil = sorted-modulo
	shardWindowAnchor time.Time        // upper bound of the CH pod-count query window
}

// NewManager creates a new watcher manager
func NewManager(cfg *config.Config, outputManager *outputs.Manager, logger *zap.Logger) (*Manager, error) {
	var clusterInitSemaphore, globalListSemaphore chan struct{}

	// Create semaphores if concurrency limits are enabled
	if cfg.ConcurrencyControl.Enabled {
		if cfg.ConcurrencyControl.MaxClusterInits > 0 {
			clusterInitSemaphore = make(chan struct{}, cfg.ConcurrencyControl.MaxClusterInits)
		}
		if cfg.ConcurrencyControl.MaxGlobalLists > 0 {
			globalListSemaphore = make(chan struct{}, cfg.ConcurrencyControl.MaxGlobalLists)
		}
	}

	// Attempt in-cluster Kubernetes client for dynamic scale-out.
	// Logs a warning if unavailable (e.g. local dev environment); scale-out is disabled.
	k8sClient, err := getInClusterClient()
	if err != nil {
		logger.Warn("In-cluster Kubernetes client unavailable, dynamic scale-out disabled",
			zap.Error(err))
	}

	m := &Manager{
		watchers:             make(map[string]*ClusterWatcher),
		cancelFuncs:          make(map[string]context.CancelFunc),
		outputManager:        outputManager,
		logger:               logger,
		config:               cfg,
		clusterInitSemaphore: clusterInitSemaphore,
		globalListSemaphore:  globalListSemaphore,
		k8sClient:            k8sClient,
		namespace:            getNamespace(),
		releaseName:          os.Getenv("KSEER_RELEASE_NAME"),
		shardID:              getEffectiveShardID(cfg.Shard),
	}

	// Extract seeder checker from the ClickHouse output if configured.
	// ClickHouseOutput satisfies SeederChecker via structural typing — no
	// import of the clickhouse package is needed here.
	if out, ok := outputManager.GetOutput("clickhouse"); ok {
		if checker, ok := out.(SeederChecker); ok {
			m.seederChecker = checker
			logger.Info("ClickHouse per-kind resume RV enabled — seeded clusters will skip initial LIST")
		}
		if checker, ok := out.(PodShardChecker); ok {
			m.podShardChecker = checker
		}
	}

	// Initialize Vault client if configured
	if cfg.Vault != nil {
		vaultClient, err := vault.NewClient(cfg.Vault, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to create vault client: %w", err)
		}
		m.vaultClient = vaultClient
	}

	return m, nil
}

// getInClusterClient returns a Kubernetes client using the pod's service account.
func getInClusterClient() (*kubernetes.Clientset, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// getNamespace reads the pod's namespace from the service account projection,
// falling back to the POD_NAMESPACE env var (useful for local testing).
func getNamespace() string {
	data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return os.Getenv("POD_NAMESPACE")
	}
	return strings.TrimSpace(string(data))
}

// readShardAnchor reads the kseer.io/shard-window-anchor annotation from the
// agent StatefulSet. Returns the parsed date (at 00:00 UTC) or today's date
// if the annotation is absent or unparseable.
func (m *Manager) readShardAnchor(ctx context.Context) (time.Time, error) {
	if m.k8sClient == nil || m.releaseName == "" {
		return time.Time{}, fmt.Errorf("k8s client unavailable")
	}
	stsName := fmt.Sprintf("%s-agent", m.releaseName)
	sts, err := m.k8sClient.AppsV1().StatefulSets(m.namespace).Get(ctx, stsName, metav1.GetOptions{})
	if err != nil {
		return time.Time{}, err
	}
	const annotationKey = "kseer.io/shard-window-anchor"
	if dateStr, ok := sts.Annotations[annotationKey]; ok {
		if t, err := time.Parse("2006-01-02", dateStr); err == nil {
			return t, nil
		}
	}
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), nil
}

// patchShardAnchor sets the kseer.io/shard-window-anchor annotation on the
// agent StatefulSet. All other shards detect this change on their next Vault
// refresh and re-query CH with the new window. The patch is idempotent —
// concurrent patches from multiple shards writing the same date are safe.
func (m *Manager) patchShardAnchor(ctx context.Context, anchor time.Time) error {
	if m.k8sClient == nil || m.releaseName == "" {
		return fmt.Errorf("k8s client unavailable")
	}
	stsName := fmt.Sprintf("%s-agent", m.releaseName)
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"kseer.io/shard-window-anchor":%q}}}`,
		anchor.Format("2006-01-02"))
	_, err := m.k8sClient.AppsV1().StatefulSets(m.namespace).Patch(
		ctx, stsName, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// loadPodCounts queries CH for per-cluster pod counts using the given anchor
// as the exclusive upper bound of the 10-day window. Updates clusterPodCounts
// in place; sets it to nil on error so sorted-modulo fallback is used.
func (m *Manager) loadPodCounts(ctx context.Context, anchor time.Time) {
	start := anchor.AddDate(0, 0, -10)
	counts, err := m.podShardChecker.GetClusterPodCounts(ctx, start, anchor)
	if err != nil {
		m.logger.Warn("CH pod count query failed, falling back to sorted-modulo sharding",
			zap.Error(err))
		m.clusterPodCounts = nil
		return
	}
	m.clusterPodCounts = counts
	m.logger.Info("Loaded CH pod counts for weighted shard assignment",
		zap.String("window_start", start.Format("2006-01-02")),
		zap.String("window_end", anchor.Format("2006-01-02")),
		zap.Int("clusters_in_ch", len(counts)))
}

// getEffectiveShardID resolves this pod's shard index in priority order:
//  1. KSEER_SHARD_ID env var (explicit override / backward compat with Deployment model)
//  2. config.Shard.ID (set by viper from the env var)
//  3. StatefulSet hostname ordinal — parses the trailing integer from HOSTNAME
//     (e.g. pod "kseer-agent-3" → 3)
func getEffectiveShardID(s *config.ShardConfig) int {
	if id := os.Getenv("KSEER_SHARD_ID"); id != "" {
		if n, err := strconv.Atoi(id); err == nil {
			return n
		}
	}
	if s != nil && s.ID != 0 {
		return s.ID
	}
	hostname := os.Getenv("HOSTNAME")
	parts := strings.Split(hostname, "-")
	if len(parts) > 0 {
		if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			return n
		}
	}
	return 0
}

// updateNeededTotal recomputes and stores the needed shard count.
// When PodsPerShard > 0 and pod count data is available, uses pod-volume-based
// computation; otherwise falls back to ceil(vaultCount / clustersPerShard).
// Called at the start of each Vault refresh cycle before any cluster assignment.
func (m *Manager) updateNeededTotal(vaultNames []string) {
	s := m.config.Shard
	if s == nil {
		return
	}

	vaultCount := len(vaultNames)
	var needed int
	if s.PodsPerShard > 0 && m.clusterPodCounts != nil {
		totalPods := int64(0)
		for _, name := range vaultNames {
			totalPods += m.clusterPodCounts[name]
		}
		if totalPods > 0 {
			needed = int(math.Ceil(float64(totalPods) / float64(s.PodsPerShard)))
		} else {
			needed = 1
		}
	} else if s.ClustersPerShard > 0 {
		needed = (vaultCount + s.ClustersPerShard - 1) / s.ClustersPerShard
	} else {
		return
	}

	if needed < 1 {
		needed = 1
	}
	if s.MaxShards > 0 && needed > s.MaxShards {
		needed = s.MaxShards
	}
	if vaultCount > 0 && needed > vaultCount {
		needed = vaultCount
	}
	m.neededTotalMu.Lock()
	m.neededShardTotal = needed
	m.neededTotalMu.Unlock()
	m.logger.Debug("Updated needed shard total",
		zap.Int("vault_clusters", vaultCount),
		zap.Int("needed_total", needed))
}

// getShardTotal returns the authoritative total for rendezvous hashing.
// Prefers the Vault-computed neededShardTotal; falls back to config.Shard.Total
// (manual override), then 1 (no sharding).
func (m *Manager) getShardTotal() int {
	m.neededTotalMu.RLock()
	needed := m.neededShardTotal
	m.neededTotalMu.RUnlock()
	if needed > 0 {
		return needed
	}
	s := m.config.Shard
	if s != nil && s.Total > 0 {
		return s.Total
	}
	return 1
}

// sortedModuloWinner returns the shard ID that owns the named cluster using
// sorted modulo assignment. allNames must be pre-sorted alphabetically.
// Cluster at index i in the sorted list is assigned to shard i % total.
// This guarantees no shard receives more than ceil(N/total) clusters,
// providing a strict upper bound on per-pod load (unlike rendezvous hashing
// which only guarantees statistical balance).
func sortedModuloWinner(name string, allNames []string, total int) int {
	for i, n := range allNames {
		if n == name {
			return i % total
		}
	}
	return 0
}

// assignClustersByWeight bins clusters into shards using the LPT (Largest
// Processing Time First) heuristic. Returns a map of cluster → shard ID and
// the computed number of shards. Clusters absent from weights get weight 0.
// The sort is deterministic: heavy→light, ties broken alphabetically by name
// so all pods compute the same result.
//
// Zero-weight clusters (no ClickHouse history, e.g. newly added) are handled
// in a second pass after all weighted clusters are placed. They are assigned
// round-robin through bins sorted by their final load ascending (lightest
// bins first), so they fill the least-loaded shards evenly rather than all
// piling on whichever single bin had the minimum load after the weighted pass.
func assignClustersByWeight(allNames []string, weights map[string]int64, targetPerShard int64, maxShards int) (map[string]int, int) {
	totalWeight := int64(0)
	for _, name := range allNames {
		totalWeight += weights[name]
	}

	numShards := 1
	if totalWeight > 0 && targetPerShard > 0 {
		numShards = int(math.Ceil(float64(totalWeight) / float64(targetPerShard)))
	}
	if maxShards > 0 && numShards > maxShards {
		numShards = maxShards
	}
	if numShards > len(allNames) {
		numShards = len(allNames)
	}
	if numShards < 1 {
		numShards = 1
	}

	type cw struct {
		name   string
		weight int64
	}
	sorted := make([]cw, len(allNames))
	for i, name := range allNames {
		sorted[i] = cw{name, weights[name]} // absent → 0
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].weight != sorted[j].weight {
			return sorted[i].weight > sorted[j].weight
		}
		return sorted[i].name < sorted[j].name
	})

	binLoads := make([]int64, numShards)
	assignment := make(map[string]int, len(allNames))

	// Pass 1: LPT assignment for weighted (non-zero) clusters.
	// sorted is heavy→light so all weighted clusters come first.
	firstZero := len(sorted)
	for i, c := range sorted {
		if c.weight == 0 {
			firstZero = i
			break
		}
		minBin := 0
		for b := 1; b < numShards; b++ {
			if binLoads[b] < binLoads[minBin] {
				minBin = b
			}
		}
		assignment[c.name] = minBin
		binLoads[minBin] += c.weight
	}

	// Pass 2: distribute zero-weight clusters round-robin through bins sorted
	// by their post-LPT load (lightest first, ties broken by bin index).
	// This prevents all new/unknown clusters from piling on the single bin
	// that happened to have the minimum load after pass 1.
	sortedBins := make([]int, numShards)
	for i := range sortedBins {
		sortedBins[i] = i
	}
	sort.Slice(sortedBins, func(a, b int) bool {
		if binLoads[sortedBins[a]] != binLoads[sortedBins[b]] {
			return binLoads[sortedBins[a]] < binLoads[sortedBins[b]]
		}
		return sortedBins[a] < sortedBins[b]
	})
	for i, c := range sorted[firstZero:] {
		assignment[c.name] = sortedBins[i%numShards]
	}

	return assignment, numShards
}

// shouldHandleCluster reports whether this agent instance is responsible for
// the named cluster. When PodsPerShard > 0 and pod count data is available,
// uses LPT weighted bin-packing. Otherwise falls back to sorted modulo.
// Overrides are checked first so specific clusters can be pinned to a shard.
// allNames must be pre-sorted alphabetically and contain all current cluster names.
func (m *Manager) shouldHandleCluster(name string, allNames []string) bool {
	s := m.config.Shard
	if s == nil {
		return true
	}
	total := m.getShardTotal()
	if total <= 1 {
		return true
	}
	if id, ok := s.Overrides[name]; ok {
		return id == m.shardID
	}
	if s.PodsPerShard > 0 && m.clusterPodCounts != nil {
		assignment, _ := assignClustersByWeight(allNames, m.clusterPodCounts, s.PodsPerShard, s.MaxShards)
		return assignment[name] == m.shardID
	}
	return sortedModuloWinner(name, allNames, total) == m.shardID
}

// Start starts all watchers (static and dynamic)
func (m *Manager) Start(ctx context.Context) error {
	// Build sorted name list for static clusters (unused in practice when
	// Vault discovery is enabled, but required for shouldHandleCluster signature).
	staticNames := make([]string, len(m.config.Clusters))
	for i, c := range m.config.Clusters {
		staticNames[i] = c.Name
	}
	sort.Strings(staticNames)

	// Start static cluster watchers from config
	for _, clusterCfg := range m.config.Clusters {
		if !m.shouldHandleCluster(clusterCfg.Name, staticNames) {
			m.logger.Debug("Skipping cluster (not assigned to this shard)",
				zap.String("cluster", clusterCfg.Name))
			continue
		}
		if err := m.startClusterWatcher(ctx, clusterCfg); err != nil {
			m.logger.Error("Failed to start cluster watcher",
				zap.String("cluster", clusterCfg.Name),
				zap.Error(err))
			// Continue starting other watchers
		}
	}

	// Initialize pod-count-weighted shard assignment if configured.
	if m.config.Shard != nil && m.config.Shard.PodsPerShard > 0 && m.podShardChecker != nil {
		anchor, err := m.readShardAnchor(ctx)
		if err != nil {
			m.logger.Warn("Could not read shard window anchor, using today (UTC)",
				zap.Error(err))
			now := time.Now().UTC()
			anchor = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		}
		m.shardWindowAnchor = anchor
		m.loadPodCounts(ctx, anchor)
	}

	// Start dynamic cluster discovery if Vault is configured
	if m.vaultClient != nil {
		go m.watchVaultClusters(ctx)
	}

	if m.config.StatusLog.Enabled && m.config.StatusLog.Interval > 0 {
		go m.runStatusReporter(ctx)
	}

	return nil
}

// runStatusReporter emits the periodic operator-facing status log. Runs as a
// single per-process goroutine; iterates current watchers on each tick and
// asks each ClusterWatcher for a snapshot. The first tick fires after one
// interval (not at startup) so the agent has time to do initial discovery.
func (m *Manager) runStatusReporter(ctx context.Context) {
	ticker := time.NewTicker(m.config.StatusLog.Interval)
	defer ticker.Stop()
	m.logger.Info("Status reporter started",
		zap.Duration("interval", m.config.StatusLog.Interval),
		zap.Bool("per_kind_detail", m.config.StatusLog.PerKindDetail))

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.emitStatusReport()
		}
	}
}

func (m *Manager) emitStatusReport() {
	now := time.Now()
	m.watchersMu.RLock()
	snapshots := make([]ClusterStatus, 0, len(m.watchers))
	for _, w := range m.watchers {
		snapshots = append(snapshots, w.statusSnapshot(now))
	}
	m.watchersMu.RUnlock()

	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Cluster < snapshots[j].Cluster
	})

	total := m.getShardTotal()
	for _, s := range snapshots {
		totalKinds := s.KindCounts[TierP1] + s.KindCounts[TierP2] + s.KindCounts[TierP3]
		fields := []zap.Field{
			zap.String("cluster", s.Cluster),
			zap.Int("kinds", totalKinds),
			zap.Int("p1", s.KindCounts[TierP1]),
			zap.Int("p2", s.KindCounts[TierP2]),
			zap.Int("p3", s.KindCounts[TierP3]),
			zap.String("shard", fmt.Sprintf("%d/%d", m.shardID, total)),
		}
		if len(s.KindStats) > 0 {
			fields = append(fields,
				zap.String("last_event", durationAgo(now.Sub(s.KindStats[0].LastEvent))))
		} else {
			fields = append(fields, zap.String("last_event", "never"))
		}
		m.logger.Info("status", fields...)

		if !m.config.StatusLog.PerKindDetail {
			continue
		}
		for _, k := range s.KindStats {
			m.logger.Info("status_kind",
				zap.String("cluster", s.Cluster),
				zap.String("kind", k.Kind),
				zap.String("tier", k.Tier.String()),
				zap.Int64("events", k.Count),
				zap.String("last", durationAgo(now.Sub(k.LastEvent))))
		}
	}
}

// durationAgo formats a duration for the status log: "2s_ago", "15m_ago",
// "3h_ago". Sub-second values are clamped to "0s_ago".
func durationAgo(d time.Duration) string {
	if d < time.Second {
		return "0s_ago"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds_ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm_ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh_ago", int(d.Hours()))
}

// watchVaultClusters watches Vault for cluster changes
func (m *Manager) watchVaultClusters(ctx context.Context) {
	interval := m.config.Vault.RefreshInterval
	if interval == 0 {
		interval = 5 * time.Minute
	}

	m.logger.Info("Starting Vault cluster discovery",
		zap.Duration("interval", interval),
		zap.String("secret_path", m.config.Vault.SecretPath))

	clusterChan, err := m.vaultClient.WatchClusters(ctx, interval)
	if err != nil {
		m.logger.Error("Failed to start Vault watcher", zap.Error(err))
		return
	}

	// knownClusters tracks clusters this shard is currently assigned to and watching.
	knownClusters := make(map[string]bool)
	// allKnownClusters tracks ALL cluster names seen across all Vault polls,
	// used to detect when genuinely new clusters are added to Vault.
	allKnownClusters := make(map[string]bool)

	for clusters := range clusterChan {
		m.logger.Debug("Received cluster update from Vault",
			zap.Int("cluster_count", len(clusters)))

		// Re-check the shard window anchor. If it changed (e.g. patched by
		// another shard or by a Helm deploy), reload pod counts so all shards
		// converge to the same weighted assignment.
		if m.config.Shard != nil && m.config.Shard.PodsPerShard > 0 && m.podShardChecker != nil {
			if anchor, err := m.readShardAnchor(ctx); err == nil {
				if !anchor.Equal(m.shardWindowAnchor) {
					m.logger.Info("Shard window anchor changed, reloading pod counts",
						zap.String("old", m.shardWindowAnchor.Format("2006-01-02")),
						zap.String("new", anchor.Format("2006-01-02")))
					m.shardWindowAnchor = anchor
					m.loadPodCounts(ctx, anchor)
				}
			}
		}

		// Detect new clusters added to Vault (compared to all previously seen).
		newClustersDetected := false
		for name := range clusters {
			if !allKnownClusters[name] {
				newClustersDetected = true
				allKnownClusters[name] = true
			}
		}

		// When new clusters appear in Vault, advance the anchor to today so all
		// shards re-compute assignment with fresh pod count data.
		if newClustersDetected && m.config.Shard != nil && m.config.Shard.PodsPerShard > 0 && m.podShardChecker != nil {
			now := time.Now().UTC()
			today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
			if !today.Equal(m.shardWindowAnchor) {
				if err := m.patchShardAnchor(ctx, today); err != nil {
					m.logger.Warn("Failed to patch shard window anchor for new cluster",
						zap.Error(err))
				} else {
					m.logger.Info("Advanced shard window anchor for new Vault cluster",
						zap.String("anchor", today.Format("2006-01-02")))
					m.shardWindowAnchor = today
					m.loadPodCounts(ctx, today)
				}
			}
		}

		// Build sorted cluster name slice once per refresh cycle.
		// sortedModuloWinner requires a stable sort order so all pods agree.
		allNames := make([]string, 0, len(clusters))
		for name := range clusters {
			allNames = append(allNames, name)
		}
		sort.Strings(allNames)

		// 1. Update neededTotal BEFORE any cluster assignment.
		//    This ensures the assignment uses the correct total on the very first
		//    iteration, preventing shard 0 from handling all clusters before
		//    siblings are created.
		m.updateNeededTotal(allNames)

		currentClusters := make(map[string]bool)

		// 2. Start watchers for new clusters assigned to this shard.
		for clusterName, creds := range clusters {
			currentClusters[clusterName] = true

			if !m.shouldHandleCluster(clusterName, allNames) {
				continue
			}

			m.watchersMu.RLock()
			_, exists := m.watchers[clusterName]
			m.watchersMu.RUnlock()

			if exists {
				continue
			}

			// New cluster assigned to this shard.
			m.logger.Info("Discovered new cluster in Vault",
				zap.String("cluster", clusterName),
				zap.String("server", creds.Server))

			clusterCfg := m.vaultCredsToClusterConfig(creds)
			if err := m.startClusterWatcher(ctx, clusterCfg); err != nil {
				m.logger.Error("Failed to start watcher for new cluster",
					zap.String("cluster", clusterName),
					zap.Error(err))
			} else {
				knownClusters[clusterName] = true
			}
		}

		// 3. Stop watchers for clusters that were removed from Vault OR
		//    reassigned to a different shard due to a total change.
		for clusterName := range knownClusters {
			removedFromVault := !currentClusters[clusterName]
			reassignedAway := currentClusters[clusterName] && !m.shouldHandleCluster(clusterName, allNames)

			if removedFromVault || reassignedAway {
				if removedFromVault {
					m.logger.Info("Cluster removed from Vault, stopping watcher",
						zap.String("cluster", clusterName))
				} else {
					m.logger.Info("Cluster reassigned to different shard, stopping watcher",
						zap.String("cluster", clusterName))
				}
				m.stopClusterWatcher(clusterName)
				delete(knownClusters, clusterName)
			}
		}

		// 4. Patch StatefulSet replicas up or down as needed.
		m.maybeScale(ctx, allNames)
	}
}

// vaultCredsToClusterConfig converts Vault credentials to ClusterConfig
func (m *Manager) vaultCredsToClusterConfig(creds *vault.ClusterCredentials) config.ClusterConfig {
	dwc := m.config.DefaultWatchConfig
	initialBackoff := dwc.InitialBackoff
	if initialBackoff == 0 {
		initialBackoff = 1 * time.Second
	}
	maxBackoff := dwc.MaxBackoff
	if maxBackoff == 0 {
		maxBackoff = 5 * time.Minute
	}
	timeoutSeconds := dwc.TimeoutSeconds
	if timeoutSeconds == 0 {
		timeoutSeconds = 432000
	}
	listPageSize := dwc.ListPageSize
	if listPageSize == 0 {
		listPageSize = 500
	}
	return config.ClusterConfig{
		Name: creds.Name,
		ServiceAccount: &config.ServiceAccountAuth{
			Server:   creds.Server,
			Token:    creds.Token,
			CAData:   creds.CAData,
			Insecure: creds.Insecure,
		},
		// Use DefaultWatchConfig from config (configurable via configmap)
		WatchConfig: config.WatchConfig{
			InitialBackoff:     initialBackoff,
			MaxBackoff:         maxBackoff,
			TimeoutSeconds:     timeoutSeconds,
			AllowBookmarks:     true,
			ListPageSize:       listPageSize,
			MaxListsPerCluster: m.config.ConcurrencyControl.MaxListsPerCluster,
			ResyncInterval:     dwc.ResyncInterval,
			Tiers:              dwc.Tiers,
			SeederQueryRetries: dwc.SeederQueryRetries,
			SeederQueryBackoff: dwc.SeederQueryBackoff,
		},
	}
}

// startClusterWatcher starts a watcher for a specific cluster with a per-watcher
// context so it can be cancelled independently via stopClusterWatcher.
func (m *Manager) startClusterWatcher(ctx context.Context, clusterCfg config.ClusterConfig) error {
	m.watchersMu.Lock()

	// Check if already running
	if _, exists := m.watchers[clusterCfg.Name]; exists {
		m.watchersMu.Unlock()
		m.logger.Debug("Watcher already running for cluster",
			zap.String("cluster", clusterCfg.Name))
		return nil
	}

	// Create new watcher
	w, err := New(clusterCfg, m.outputManager, m.logger)
	if err != nil {
		m.watchersMu.Unlock()
		return fmt.Errorf("failed to create watcher: %w", err)
	}

	// Set global LIST semaphore
	w.SetGlobalListSemaphore(m.globalListSemaphore)

	// Attach seeder checker so this watcher can skip the initial LIST for
	// resource types that are already seeded in ClickHouse.
	if m.seederChecker != nil {
		w.SetSeederChecker(m.seederChecker)
	}

	// Derive a per-watcher context so this watcher can be stopped independently.
	watchCtx, cancel := context.WithCancel(ctx)
	m.cancelFuncs[clusterCfg.Name] = cancel
	m.watchers[clusterCfg.Name] = w
	m.watchersMu.Unlock()

	clusterName := clusterCfg.Name
	go func() {
		defer func() {
			m.watchersMu.Lock()
			delete(m.watchers, clusterName)
			delete(m.cancelFuncs, clusterName)
			m.watchersMu.Unlock()
		}()

		m.logger.Info("Starting cluster watcher", zap.String("cluster", clusterName))

		if err := w.StartWithInitSemaphore(watchCtx, m.clusterInitSemaphore); err != nil {
			m.logger.Error("Cluster watcher stopped with error",
				zap.String("cluster", clusterName),
				zap.Error(err))
		} else {
			m.logger.Info("Cluster watcher stopped", zap.String("cluster", clusterName))
		}
	}()

	return nil
}

// stopClusterWatcher cancels the per-watcher context, cleanly stopping the named watcher.
func (m *Manager) stopClusterWatcher(clusterName string) {
	m.watchersMu.Lock()
	defer m.watchersMu.Unlock()

	if cancel, ok := m.cancelFuncs[clusterName]; ok {
		cancel()
		delete(m.cancelFuncs, clusterName)
	}
	delete(m.watchers, clusterName)
}

// maybeScale patches the agent StatefulSet's replica count to match the number
// of shards needed for the current Vault cluster count. Handles both scale-out
// and scale-in. All shards independently compute the same neededTotal and patch
// to the same value — idempotent, last write wins, converges correctly.
// Note: StatefulSet scale-in always removes the highest-ordinal pod first. Clusters
// previously watched by the removed pod are re-assigned on the next Vault refresh
// cycle of the surviving pods (up to one refresh interval of gap).
func (m *Manager) maybeScale(ctx context.Context, vaultNames []string) {
	if m.k8sClient == nil || m.releaseName == "" {
		return
	}
	s := m.config.Shard
	if s == nil || (s.ClustersPerShard <= 0 && s.PodsPerShard <= 0) {
		return
	}

	totalClusters := len(vaultNames)
	var needed int
	if s.PodsPerShard > 0 && m.clusterPodCounts != nil {
		totalPods := int64(0)
		for _, name := range vaultNames {
			totalPods += m.clusterPodCounts[name]
		}
		if totalPods > 0 {
			needed = int(math.Ceil(float64(totalPods) / float64(s.PodsPerShard)))
		} else {
			needed = 1
		}
	} else if s.ClustersPerShard > 0 {
		needed = (totalClusters + s.ClustersPerShard - 1) / s.ClustersPerShard
	} else {
		return
	}
	if needed < 1 {
		needed = 1
	}
	if s.MaxShards > 0 && needed > s.MaxShards {
		m.logger.Warn("Capping shard count at maxShards limit",
			zap.Int("computed_shards", needed),
			zap.Int("max_shards", s.MaxShards))
		needed = s.MaxShards
	}
	if totalClusters > 0 && needed > totalClusters {
		needed = totalClusters
	}

	stsName := fmt.Sprintf("%s-agent", m.releaseName)
	sts, err := m.k8sClient.AppsV1().StatefulSets(m.namespace).Get(ctx, stsName, metav1.GetOptions{})
	if err != nil {
		m.logger.Warn("Failed to get agent StatefulSet for scale check", zap.Error(err))
		return
	}

	current := 1
	if sts.Spec.Replicas != nil {
		current = int(*sts.Spec.Replicas)
	}
	if needed == current {
		return
	}

	if needed > current {
		m.logger.Info("Scale-out triggered",
			zap.Int("vault_clusters", totalClusters),
			zap.Int("current_replicas", current),
			zap.Int("needed_replicas", needed))
	} else {
		m.logger.Info("Scale-in triggered",
			zap.Int("vault_clusters", totalClusters),
			zap.Int("current_replicas", current),
			zap.Int("needed_replicas", needed))
	}

	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, needed))
	_, err = m.k8sClient.AppsV1().StatefulSets(m.namespace).Patch(
		ctx, stsName, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		m.logger.Error("Failed to patch StatefulSet replicas", zap.Error(err))
	} else {
		m.logger.Info("Patched StatefulSet replicas", zap.Int("replicas", needed))
	}
}

// GetActiveWatchers returns the names of all active watchers
func (m *Manager) GetActiveWatchers() []string {
	m.watchersMu.RLock()
	defer m.watchersMu.RUnlock()

	names := make([]string, 0, len(m.watchers))
	for name := range m.watchers {
		names = append(names, name)
	}
	return names
}
