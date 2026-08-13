package watcher

import (
	"testing"
	"time"

	"github.com/kseer/kseer-agent/pkg/config"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestParseGVRKey_RoundTrip(t *testing.T) {
	cases := []schema.GroupVersionResource{
		{Group: "", Version: "v1", Resource: "pods"},
		{Group: "apps", Version: "v1", Resource: "deployments"},
		{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"},
	}
	for _, gvr := range cases {
		key := gvrToString(gvr)
		got, err := parseGVRKey(key)
		if err != nil {
			t.Errorf("parseGVRKey(%q) error: %v", key, err)
			continue
		}
		if got != gvr {
			t.Errorf("round-trip: got %+v, want %+v", got, gvr)
		}
	}
}

func TestParseGVRKey_Invalid(t *testing.T) {
	if _, err := parseGVRKey("invalid"); err == nil {
		t.Errorf("expected error for malformed key")
	}
}

// computeP1DueTimes mirrors the dueTime initialization logic in
// runP1ResyncLoop. Keeping the test surface small — we don't spin up the
// goroutine, just verify the scheduling math.
func computeP1DueTimes(now time.Time, kinds []p1KindRef, lastSeed map[string]time.Time) map[int]time.Time {
	dueTimes := make(map[int]time.Time, len(kinds))
	for i, k := range kinds {
		if last, ok := lastSeed[k.kind]; ok {
			dueTimes[i] = last.Add(k.interval)
		} else {
			// Fail-safe: no recent SEEDED → due now.
			dueTimes[i] = now
		}
	}
	return dueTimes
}

func TestP1Scheduling_PastSEEDED_FiresImmediately(t *testing.T) {
	now := time.Now()
	kinds := []p1KindRef{{kind: "Namespace", interval: 5 * 24 * time.Hour}}
	lastSeed := map[string]time.Time{
		"Namespace": now.Add(-6 * 24 * time.Hour), // last seeded 6 days ago, > interval
	}
	due := computeP1DueTimes(now, kinds, lastSeed)
	if !due[0].Before(now) {
		t.Errorf("overdue kind should be scheduled in the past, got dueTime=%v now=%v", due[0], now)
	}
}

func TestP1Scheduling_RecentSEEDED_SchedulesAtInterval(t *testing.T) {
	now := time.Now()
	kinds := []p1KindRef{{kind: "Namespace", interval: 5 * 24 * time.Hour}}
	lastSeed := map[string]time.Time{
		"Namespace": now.Add(-1 * 24 * time.Hour), // 1 day ago
	}
	due := computeP1DueTimes(now, kinds, lastSeed)
	expected := now.Add(4 * 24 * time.Hour) // 4 days from now
	delta := due[0].Sub(expected)
	if delta < -time.Second || delta > time.Second {
		t.Errorf("expected due ~4d from now, got %v (delta %v)", due[0], delta)
	}
}

func TestP1Scheduling_NoLastSeed_FailsSafeToDueNow(t *testing.T) {
	now := time.Now()
	kinds := []p1KindRef{{kind: "Pod", interval: 5 * 24 * time.Hour}}
	due := computeP1DueTimes(now, kinds, nil) // empty last-seed map
	// Unseeded / partially-seeded kinds relist immediately so the seed is
	// (re)completed rather than waiting a full interval.
	if delta := due[0].Sub(now); delta < -time.Second || delta > time.Second {
		t.Errorf("expected fail-safe due now, got %v (delta %v)", due[0], delta)
	}
}

func TestP1Scheduling_PerKindIntervalOverride(t *testing.T) {
	now := time.Now()
	kinds := []p1KindRef{
		{kind: "Namespace", interval: 10 * time.Minute},
		{kind: "Node", interval: 1 * time.Hour},
	}
	lastSeed := map[string]time.Time{
		"Namespace": now.Add(-5 * time.Minute),
		"Node":      now.Add(-30 * time.Minute),
	}
	due := computeP1DueTimes(now, kinds, lastSeed)
	// Namespace: -5m + 10m = +5m
	if d := due[0].Sub(now); d < 4*time.Minute || d > 6*time.Minute {
		t.Errorf("Namespace expected +5m, got %v", d)
	}
	// Node: -30m + 1h = +30m
	if d := due[1].Sub(now); d < 29*time.Minute || d > 31*time.Minute {
		t.Errorf("Node expected +30m, got %v", d)
	}
}

// silence unused import when we narrow tests later
var _ = config.TierConfig{}
