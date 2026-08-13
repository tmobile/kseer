package watcher

import (
	"testing"
	"time"
)

// newSnapshotTestWatcher returns a ClusterWatcher with just enough state to
// exercise statusSnapshot — no Kubernetes client, no output manager.
func newSnapshotTestWatcher() *ClusterWatcher {
	return &ClusterWatcher{
		clusterName:    "test",
		gvrKindMap:     make(map[string]string),
		gvrTier:        make(map[string]Tier),
		kindLastEvent:  make(map[string]time.Time),
		kindEventCount: make(map[string]int64),
	}
}

func TestStatusSnapshot_SkipsSilentKinds(t *testing.T) {
	w := newSnapshotTestWatcher()
	w.gvrKindMap["/v1/pods"] = "Pod"
	w.gvrTier["/v1/pods"] = TierP2
	w.gvrKindMap["/v1/services"] = "Service"
	w.gvrTier["/v1/services"] = TierP2
	// Only Pod has ever sent an event; Service is silent.
	now := time.Now()
	w.kindLastEvent["Pod"] = now.Add(-30 * time.Second)
	w.kindEventCount["Pod"] = 5

	snap := w.statusSnapshot(now)
	if snap.KindCounts[TierP2] != 2 {
		t.Errorf("kind count summary should include both kinds: got %d", snap.KindCounts[TierP2])
	}
	if len(snap.KindStats) != 1 {
		t.Fatalf("KindStats should contain only the active kind: got %d", len(snap.KindStats))
	}
	if snap.KindStats[0].Kind != "Pod" {
		t.Errorf("expected Pod, got %q", snap.KindStats[0].Kind)
	}
}

func TestStatusSnapshot_DropsKindsOlderThan24h(t *testing.T) {
	w := newSnapshotTestWatcher()
	w.gvrKindMap["/v1/pods"] = "Pod"
	w.gvrTier["/v1/pods"] = TierP2
	w.gvrKindMap["/v1/configmaps"] = "ConfigMap"
	w.gvrTier["/v1/configmaps"] = TierP2

	now := time.Now()
	w.kindLastEvent["Pod"] = now.Add(-1 * time.Hour)         // recent
	w.kindLastEvent["ConfigMap"] = now.Add(-25 * time.Hour) // stale
	w.kindEventCount["Pod"] = 10
	w.kindEventCount["ConfigMap"] = 3

	snap := w.statusSnapshot(now)
	if len(snap.KindStats) != 1 {
		t.Fatalf("expected only Pod (ConfigMap is > 24h stale): got %d stats", len(snap.KindStats))
	}
	if snap.KindStats[0].Kind != "Pod" {
		t.Errorf("expected Pod, got %q", snap.KindStats[0].Kind)
	}
}

func TestStatusSnapshot_SortsByLastEventDescending(t *testing.T) {
	w := newSnapshotTestWatcher()
	w.gvrKindMap["/v1/a"] = "A"
	w.gvrKindMap["/v1/b"] = "B"
	w.gvrKindMap["/v1/c"] = "C"
	w.gvrTier["/v1/a"] = TierP2
	w.gvrTier["/v1/b"] = TierP2
	w.gvrTier["/v1/c"] = TierP2
	now := time.Now()
	w.kindLastEvent["A"] = now.Add(-10 * time.Minute)
	w.kindLastEvent["B"] = now.Add(-1 * time.Minute)
	w.kindLastEvent["C"] = now.Add(-5 * time.Minute)

	snap := w.statusSnapshot(now)
	if len(snap.KindStats) != 3 {
		t.Fatalf("expected 3 stats, got %d", len(snap.KindStats))
	}
	if snap.KindStats[0].Kind != "B" || snap.KindStats[1].Kind != "C" || snap.KindStats[2].Kind != "A" {
		t.Errorf("wrong sort order: %v %v %v", snap.KindStats[0].Kind, snap.KindStats[1].Kind, snap.KindStats[2].Kind)
	}
}

func TestRecordEvent_SkipsSEEDED(t *testing.T) {
	w := newSnapshotTestWatcher()
	w.recordEvent("Pod", "ADDED")
	w.recordEvent("Pod", "SEEDED")
	w.recordEvent("Pod", "MODIFIED")
	if w.kindEventCount["Pod"] != 2 {
		t.Errorf("SEEDED should not be counted: got %d, want 2", w.kindEventCount["Pod"])
	}
}

func TestDurationAgo_Format(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{500 * time.Millisecond, "0s_ago"},
		{30 * time.Second, "30s_ago"},
		{45 * time.Minute, "45m_ago"},
		{3 * time.Hour, "3h_ago"},
	}
	for _, c := range cases {
		if got := durationAgo(c.in); got != c.want {
			t.Errorf("durationAgo(%v) = %q; want %q", c.in, got, c.want)
		}
	}
}
