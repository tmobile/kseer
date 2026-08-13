package watcher

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kseer/kseer-agent/pkg/types"
)

// p1LargeListWarnThreshold is the soft warning threshold for the first LIST
// of a P1 kind. Marking high-cardinality kinds (Pods, ConfigMaps in big
// clusters) as P1 will pull the full list into memory each resync — the
// agent still proceeds, but logs a warning so operators reconsider.
const p1LargeListWarnThreshold = 10000

// runP1ResyncLoop is the per-cluster sequential resync goroutine. It
// maintains a per-kind "next due" timestamp and walks one kind at a time,
// blocking on FlushP1 between kinds so the previous kind's events are
// durable in CH before the next LIST starts. Started lazily by
// discoverAndWatch when the first P1 kind is observed.
func (w *ClusterWatcher) runP1ResyncLoop(ctx context.Context) {
	kinds := w.tierResolver.p1Kinds(w.tierConfig)
	if len(kinds) == 0 {
		return
	}

	// First firing schedule:
	//   - If a recent SEEDED row exists in CH for this kind, anchor to
	//     lastSeed + interval (could be in the past → fires immediately).
	//   - Otherwise fire immediately (due now) so unseeded/partially-seeded
	//     kinds relist right away.
	//
	// This makes the resync schedule survive pod restarts: a 4-day-uptime
	// pod restarting on a 5-day interval picks up where it left off rather
	// than resetting to a fresh 5-day wait.
	now := time.Now()
	dueTimes := make(map[int]time.Time, len(kinds))
	for i, k := range kinds {
		if last, ok := w.kindLastSeed[k.kind]; ok {
			dueTimes[i] = last.Add(k.interval)
		} else {
			// No recent SEEDED row: never seeded, partially seeded after an
			// OOMKill, or the last-seed query failed. Fail-safe to "due now"
			// so the kind relists immediately and (re)completes its seed.
			// This preserves the partial-seed safety the seed-completion
			// query used to provide.
			dueTimes[i] = now
		}
	}

	w.logger.Info("Started P1 sequential resync loop",
		zap.Int("p1_kinds", len(kinds)),
		zap.Duration("default_interval", w.tierConfig.P1.ResyncInterval))

	firstListSeen := make(map[int]bool, len(kinds))

	for {
		// Pick the kind whose next-due time is soonest.
		nextIdx := -1
		var nextDue time.Time
		for i, due := range dueTimes {
			if nextIdx == -1 || due.Before(nextDue) {
				nextIdx = i
				nextDue = due
			}
		}

		wait := time.Until(nextDue)
		if wait < 0 {
			wait = 0
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		k := kinds[nextIdx]
		warn := !firstListSeen[nextIdx]
		if err := w.resyncP1Kind(ctx, k, warn); err != nil {
			if ctx.Err() != nil {
				return
			}
			w.logger.Warn("P1 resync failed for kind, will retry on next interval",
				zap.String("kind", k.kind),
				zap.String("apiVersion", k.apiVersion),
				zap.Error(err))
		}
		firstListSeen[nextIdx] = true
		dueTimes[nextIdx] = time.Now().Add(k.interval)
	}
}

// resyncP1Kind runs a single P1 LIST→flush→sentinel→flush cycle for one
// kind. Blocks until the previous step is durable in CH before moving to
// the next.
func (w *ClusterWatcher) resyncP1Kind(ctx context.Context, k p1KindRef, warnIfLarge bool) error {
	gvr, err := w.resolveP1GVR(k)
	if err != nil {
		return err
	}

	// Non-paginated LIST per the P1 contract. Documented constraint: P1 is
	// for low-cardinality kinds; high-cardinality kinds will OOM.
	list, err := w.dynamicClient.Resource(gvr).Namespace(v1.NamespaceAll).List(ctx, v1.ListOptions{})
	if err != nil {
		return fmt.Errorf("p1 list %s: %w", k.kind, err)
	}

	if warnIfLarge && len(list.Items) > p1LargeListWarnThreshold {
		w.logger.Warn("P1 kind has unexpectedly large item count; consider moving it to P2 or P3",
			zap.String("kind", k.kind),
			zap.String("apiVersion", k.apiVersion),
			zap.Int("items", len(list.Items)),
			zap.Int("threshold", p1LargeListWarnThreshold))
	}

	for i := range list.Items {
		obj := &list.Items[i]
		if len(w.namespaces) > 0 && !contains(w.namespaces, obj.GetNamespace()) {
			continue
		}
		event := w.objectToEvent(obj, "EXISTING")
		if err := w.outputManager.SendP1(event); err != nil {
			return fmt.Errorf("p1 send %s/%s: %w", k.kind, obj.GetName(), err)
		}
		w.recordEvent(k.kind, "EXISTING")
	}

	// Append the SEEDED sentinel to the in-flight batch and flush once. A
	// single ClickHouse INSERT commits the data + sentinel atomically, which
	// preserves the invariant that the sentinel only appears when the kind's
	// data is durable, and avoids the per-kind count=1 follow-up batch that
	// the prior two-flush sequence produced.
	snapshotRV := list.GetResourceVersion()
	sentinel := types.Event{
		Timestamp:   time.Now().UTC(),
		ClusterName: w.clusterName,
		Operation:   "SEEDED",
		Object: map[string]interface{}{
			"kind": k.kind,
			"metadata": map[string]interface{}{
				"resourceVersion": snapshotRV,
			},
		},
	}
	if err := w.outputManager.SendP1(sentinel); err != nil {
		return fmt.Errorf("p1 sentinel %s: %w", k.kind, err)
	}
	if err := w.outputManager.FlushP1(ctx); err != nil {
		return fmt.Errorf("p1 flush %s: %w", k.kind, err)
	}

	w.logger.Info("P1 resync complete for kind",
		zap.String("kind", k.kind),
		zap.String("apiVersion", k.apiVersion),
		zap.Int("items", len(list.Items)),
		zap.String("snapshotRV", snapshotRV))

	return nil
}

// resolveP1GVR finds the GroupVersionResource for a P1 kind entry. P1 uses
// the gvrKindMap populated by discoverAndWatch — this means the WATCH path
// must have observed the kind at least once before the first resync fires.
// That's guaranteed because discoverAndWatch runs at startup before the
// resync loop's first interval elapses.
//
// When KindTierEntry.APIVersion is set, only the matching GVR is returned;
// otherwise the first GVR with the given Kind wins.
func (w *ClusterWatcher) resolveP1GVR(k p1KindRef) (schema.GroupVersionResource, error) {
	w.gvrKindMu.RLock()
	defer w.gvrKindMu.RUnlock()

	for gvrKey, kind := range w.gvrKindMap {
		if kind != k.kind {
			continue
		}
		gvr, err := parseGVRKey(gvrKey)
		if err != nil {
			continue
		}
		if k.apiVersion != "" {
			apiVersion := gvr.Version
			if gvr.Group != "" {
				apiVersion = gvr.Group + "/" + gvr.Version
			}
			if apiVersion != k.apiVersion {
				continue
			}
		}
		return gvr, nil
	}
	return schema.GroupVersionResource{}, fmt.Errorf("p1 kind %q not yet discovered", k.kind)
}

// startP3RelistLoop spawns a goroutine that periodically LISTs the given GVR
// and emits each item as EXISTING. No WATCH is opened; client queries
// determine "live" objects by filtering events to the most recent
// relistInterval window.
func (w *ClusterWatcher) startP3RelistLoop(ctx context.Context, gvr schema.GroupVersionResource, interval time.Duration) error {
	resourceKey := gvrToString(gvr)

	loopCtx, cancel := context.WithCancel(ctx)
	w.watchedResourcesMu.Lock()
	if _, exists := w.watchedResources[resourceKey]; exists {
		w.watchedResourcesMu.Unlock()
		cancel()
		return nil
	}
	w.watchedResources[resourceKey] = cancel
	w.watchedResourcesMu.Unlock()

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer cancel()
		w.runP3RelistLoop(loopCtx, gvr, interval)
	}()
	return nil
}

func (w *ClusterWatcher) runP3RelistLoop(ctx context.Context, gvr schema.GroupVersionResource, interval time.Duration) {
	gvrKey := gvrToString(gvr)

	// First-LIST timing:
	//   - If a recent SEEDED row exists in CH for this kind, anchor the first
	//     fire at lastSeed + interval (clamped to >= 0 so an overdue kind
	//     fires immediately). Survives pod restart so we don't relist every
	//     time the agent boots.
	//   - Otherwise fire immediately (P3 has no WATCH-path initial LIST).
	w.gvrKindMu.RLock()
	kind := w.gvrKindMap[gvrKey]
	w.gvrKindMu.RUnlock()

	firstDelay := time.Duration(0)
	if kind != "" {
		if last, ok := w.kindLastSeed[kind]; ok {
			firstDelay = time.Until(last.Add(interval))
			if firstDelay < 0 {
				firstDelay = 0
			}
		}
	}

	w.logger.Info("Started P3 relist loop",
		zap.String("resource", gvrKey),
		zap.Duration("interval", interval),
		zap.Duration("first_delay", firstDelay))

	timer := time.NewTimer(firstDelay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		if err := w.runP3RelistOnce(ctx, gvr); err != nil {
			if ctx.Err() != nil {
				return
			}
			w.logger.Warn("P3 relist failed, will retry on next interval",
				zap.String("resource", gvrKey),
				zap.Error(err))
		}

		timer.Reset(interval)
	}
}

func (w *ClusterWatcher) runP3RelistOnce(ctx context.Context, gvr schema.GroupVersionResource) error {
	gvrKey := gvrToString(gvr)
	if err := w.rateLimiter.Wait(ctx); err != nil {
		return err
	}

	list, err := w.listResourcePaginated(ctx, gvr)
	if err != nil {
		return fmt.Errorf("p3 list: %w", err)
	}

	snapshotRV := list.GetResourceVersion()

	for i := range list.Items {
		obj := &list.Items[i]
		if len(w.namespaces) > 0 && !contains(w.namespaces, obj.GetNamespace()) {
			continue
		}
		event := w.objectToEvent(obj, "EXISTING")
		if err := w.outputManager.Send(event); err != nil {
			return fmt.Errorf("p3 send: %w", err)
		}
		w.recordEvent(obj.GetKind(), "EXISTING")
	}

	w.gvrKindMu.RLock()
	kind := w.gvrKindMap[gvrKey]
	w.gvrKindMu.RUnlock()

	if kind != "" {
		sentinel := types.Event{
			Timestamp:   time.Now().UTC(),
			ClusterName: w.clusterName,
			Operation:   "SEEDED",
			Object: map[string]interface{}{
				"kind": kind,
				"metadata": map[string]interface{}{
					"resourceVersion": snapshotRV,
				},
			},
		}
		if err := w.outputManager.Send(sentinel); err != nil {
			w.logger.Warn("Failed to queue P3 sentinel",
				zap.String("resource", gvrKey),
				zap.Error(err))
		}
	}

	w.logger.Info("P3 relist complete",
		zap.String("resource", gvrKey),
		zap.Int("items", len(list.Items)),
		zap.String("snapshotRV", snapshotRV))
	return nil
}

// startP4StreamLoop spawns a goroutine that opens a WATCH for the given GVR
// from the current point in time and streams subsequent changes. There is no
// initial LIST of existing objects, no periodic relist, and no SEEDED
// sentinel. Used for ephemeral, stream-shaped kinds (e.g. Event) where
// backfilling state is pointless.
func (w *ClusterWatcher) startP4StreamLoop(ctx context.Context, gvr schema.GroupVersionResource) error {
	resourceKey := gvrToString(gvr)

	loopCtx, cancel := context.WithCancel(ctx)
	w.watchedResourcesMu.Lock()
	if _, exists := w.watchedResources[resourceKey]; exists {
		w.watchedResourcesMu.Unlock()
		cancel()
		return nil
	}
	w.watchedResources[resourceKey] = cancel
	w.watchedResourcesMu.Unlock()

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer cancel()
		w.runP4StreamLoop(loopCtx, gvr)
	}()
	return nil
}

// runP4StreamLoop reconnects a WATCH for the given GVR, always starting from
// the current collection resourceVersion ("now") rather than any stored or
// seeder RV. Each (re)connect probes a fresh RV, so events that occur during
// a reconnect gap are not captured — acceptable for ephemeral stream kinds,
// and rare in practice given the multi-day watch timeout.
func (w *ClusterWatcher) runP4StreamLoop(ctx context.Context, gvr schema.GroupVersionResource) {
	backoff := w.watchConfig.InitialBackoff

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := w.rateLimiter.Wait(ctx); err != nil {
			return
		}

		err := w.streamFromNow(ctx, gvr)
		if err != nil {
			w.logger.Warn("P4 stream failed, reconnecting",
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

// streamFromNow probes the current collection RV with a metadata-only LIST
// (Limit: 1, items discarded) and opens a WATCH from that RV, draining events
// until the watch closes or ctx is cancelled. It deliberately does not consult
// kindInitRVs / globalInitRV / gvrBookmarkRVs and never emits a SEEDED
// sentinel — P4 captures only changes going forward.
func (w *ClusterWatcher) streamFromNow(ctx context.Context, gvr schema.GroupVersionResource) error {
	// Probe the current collection resourceVersion. Limit:1 keeps this cheap;
	// the returned items are ignored — we only want list.GetResourceVersion(),
	// which is the collection RV "now" to anchor the WATCH at.
	probe, err := w.dynamicClient.Resource(gvr).Namespace(v1.NamespaceAll).List(ctx, v1.ListOptions{Limit: 1})
	if err != nil {
		return fmt.Errorf("p4 rv probe: %w", err)
	}
	startRV := probe.GetResourceVersion()

	watcher, err := w.dynamicClient.Resource(gvr).Namespace(v1.NamespaceAll).Watch(ctx, v1.ListOptions{
		ResourceVersion:     startRV,
		AllowWatchBookmarks: w.watchConfig.AllowBookmarks,
		TimeoutSeconds:      int64Ptr(int64(w.watchConfig.TimeoutSeconds)),
	})
	if err != nil {
		return fmt.Errorf("p4 watch: %w", err)
	}
	defer watcher.Stop()

	w.logger.Info("Started P4 stream from current RV",
		zap.String("resource", gvrToString(gvr)),
		zap.String("startRV", startRV))

	// runWatchEventLoop arms a resync timer only for TierP2, so a P4 GVR gets
	// none and the loop can only return on ctx cancellation, channel close, or
	// a watch error — never errResyncTriggered. Returning its error directly
	// drives the reconnect logic in runP4StreamLoop.
	return w.runWatchEventLoop(ctx, gvr, watcher)
}

// parseGVRKey reverses gvrToString ("group/version/resource"). Empty group
// is preserved as "" so core resources round-trip correctly.
func parseGVRKey(key string) (schema.GroupVersionResource, error) {
	parts := strings.SplitN(key, "/", 3)
	if len(parts) != 3 {
		return schema.GroupVersionResource{}, fmt.Errorf("invalid gvr key: %q", key)
	}
	return schema.GroupVersionResource{
		Group:    parts[0],
		Version:  parts[1],
		Resource: parts[2],
	}, nil
}
