package watcher

import (
	"sort"
	"time"
)

// staleEventThreshold is the cut-off after which a per-kind entry is dropped
// from the status log. Kinds whose last event is older than this — or that
// have never sent an event — do not appear in the per-kind detail lines.
const staleEventThreshold = 24 * time.Hour

// ClusterStatus is the per-cluster snapshot consumed by the status reporter.
// Tier counts reflect every kind currently registered for watching (P1/P2/P3
// dispatch already happened in discoverAndWatch); KindStats is filtered to
// kinds with activity in the last staleEventThreshold.
type ClusterStatus struct {
	Cluster    string
	KindCounts map[Tier]int
	KindStats  []KindStat // sorted by LastEvent descending (most recent first)
}

// KindStat is one row in the per-kind detail section of the status log.
type KindStat struct {
	Kind      string
	Tier      Tier
	Count     int64
	LastEvent time.Time
}

// statusSnapshot builds a ClusterStatus reflecting the current set of watched
// kinds and the activity stats. Silent kinds and kinds whose last event is
// older than staleEventThreshold are excluded from KindStats. The summary
// counts in KindCounts are NOT filtered — they reflect everything the
// watcher is configured to handle.
func (w *ClusterWatcher) statusSnapshot(now time.Time) ClusterStatus {
	w.gvrKindMu.RLock()
	tiersByKind := make(map[string]Tier, len(w.gvrKindMap))
	counts := map[Tier]int{}
	for gvrKey, kind := range w.gvrKindMap {
		tier := w.gvrTier[gvrKey]
		counts[tier]++
		// Same kind under multiple apiVersions collapses; keep the first
		// tier observed (deterministic enough for display).
		if _, ok := tiersByKind[kind]; !ok {
			tiersByKind[kind] = tier
		}
	}
	w.gvrKindMu.RUnlock()

	w.kindStatsMu.Lock()
	stats := make([]KindStat, 0, len(w.kindLastEvent))
	for kind, last := range w.kindLastEvent {
		if now.Sub(last) > staleEventThreshold {
			continue
		}
		tier, ok := tiersByKind[kind]
		if !ok {
			continue // kind no longer tracked
		}
		stats = append(stats, KindStat{
			Kind:      kind,
			Tier:      tier,
			Count:     w.kindEventCount[kind],
			LastEvent: last,
		})
	}
	w.kindStatsMu.Unlock()

	sort.Slice(stats, func(i, j int) bool {
		return stats[i].LastEvent.After(stats[j].LastEvent)
	})

	return ClusterStatus{
		Cluster:    w.clusterName,
		KindCounts: counts,
		KindStats:  stats,
	}
}
