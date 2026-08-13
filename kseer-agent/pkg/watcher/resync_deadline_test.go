package watcher

import (
	"testing"
	"time"

	"github.com/kseer/kseer-agent/pkg/config"
)

const testResyncInterval = 5 * 24 * time.Hour

func newDeadlineTestWatcher(interval time.Duration) *ClusterWatcher {
	return &ClusterWatcher{
		watchConfig:       config.WatchConfig{ResyncInterval: interval},
		gvrKindMap:        make(map[string]string),
		gvrTier:           make(map[string]Tier),
		kindLastSeed:      make(map[string]time.Time),
		gvrResyncDeadline: make(map[string]time.Time),
	}
}

func approxEqual(a, b time.Time) bool {
	d := a.Sub(b)
	return d > -time.Second && d < time.Second
}

func TestInitialP2ResyncDeadline_SeededRecent(t *testing.T) {
	now := time.Now()
	lastSeed := now.Add(-1 * 24 * time.Hour) // 1 day ago
	got := initialP2ResyncDeadline(now, lastSeed, true, testResyncInterval)
	want := lastSeed.Add(testResyncInterval) // ~4 days from now
	if !approxEqual(got, want) {
		t.Errorf("got %v, want lastSeed+interval %v", got, want)
	}
	if !got.After(now) {
		t.Errorf("recently-seeded kind should be scheduled in the future, got %v now %v", got, now)
	}
}

func TestInitialP2ResyncDeadline_SeededOverdue_InPast(t *testing.T) {
	now := time.Now()
	lastSeed := now.Add(-6 * 24 * time.Hour) // 6 days ago, > 5d interval
	got := initialP2ResyncDeadline(now, lastSeed, true, testResyncInterval)
	if !got.Before(now) {
		t.Errorf("overdue kind should be scheduled in the past, got %v now %v", got, now)
	}
}

func TestInitialP2ResyncDeadline_NotSeeded_DueNow(t *testing.T) {
	now := time.Now()
	got := initialP2ResyncDeadline(now, time.Time{}, false, testResyncInterval)
	if !approxEqual(got, now) {
		t.Errorf("unseeded kind should be due now, got %v now %v", got, now)
	}
}

// The deadline must not move when a watch reconnects (no LIST happened). The
// second read uses a later "now" but must return the same stored deadline.
func TestP2ResyncDeadline_StableAcrossReconnects(t *testing.T) {
	w := newDeadlineTestWatcher(testResyncInterval)
	gvrKey := "/v1/secrets"
	w.gvrKindMap[gvrKey] = "Secret"
	w.gvrTier[gvrKey] = TierP2
	w.kindLastSeed["Secret"] = time.Now().Add(-1 * 24 * time.Hour)

	first := w.p2ResyncDeadline(gvrKey, time.Now())
	// Simulate a reconnect some time later.
	second := w.p2ResyncDeadline(gvrKey, time.Now().Add(2*time.Hour))
	if !first.Equal(second) {
		t.Errorf("deadline slipped across reconnect: first %v second %v", first, second)
	}
}

// A real LIST advances the deadline to now+interval, and that advanced value
// survives subsequent reconnect reads.
func TestAdvanceP2ResyncDeadline_SetsNowPlusIntervalAndSticks(t *testing.T) {
	w := newDeadlineTestWatcher(testResyncInterval)
	gvrKey := "/v1/secrets"
	w.gvrKindMap[gvrKey] = "Secret"
	w.gvrTier[gvrKey] = TierP2
	w.kindLastSeed["Secret"] = time.Now().Add(-6 * 24 * time.Hour) // overdue

	listTime := time.Now()
	w.advanceP2ResyncDeadline(gvrKey, listTime)

	got := w.p2ResyncDeadline(gvrKey, time.Now().Add(time.Hour))
	want := listTime.Add(testResyncInterval)
	if !approxEqual(got, want) {
		t.Errorf("after LIST advance, got %v want %v", got, want)
	}
	// A fresh LIST means the next relist is a full interval out, not immediate.
	if !got.After(time.Now()) {
		t.Errorf("deadline after a fresh LIST should be in the future, got %v", got)
	}
}

// When the kind has no SEEDED timestamp (never/partially seeded, or the
// last-seed query failed so kindLastSeed is empty), the first arming is due
// now so the watch immediately re-LISTs.
func TestP2ResyncDeadline_FailSafeDueNow(t *testing.T) {
	w := newDeadlineTestWatcher(testResyncInterval)
	gvrKey := "/v1/configmaps"
	w.gvrKindMap[gvrKey] = "ConfigMap"
	w.gvrTier[gvrKey] = TierP2
	// kindLastSeed intentionally empty.

	now := time.Now()
	got := w.p2ResyncDeadline(gvrKey, now)
	if !approxEqual(got, now) {
		t.Errorf("fail-safe deadline should be due now, got %v now %v", got, now)
	}
	if time.Until(got) > time.Second {
		t.Errorf("fail-safe deadline should fire immediately, time.Until=%v", time.Until(got))
	}
}

func TestAdvanceP2ResyncDeadline_NoopWhenResyncDisabled(t *testing.T) {
	w := newDeadlineTestWatcher(0) // resync disabled
	gvrKey := "/v1/secrets"
	w.advanceP2ResyncDeadline(gvrKey, time.Now())
	if _, ok := w.gvrResyncDeadline[gvrKey]; ok {
		t.Errorf("advance should be a no-op when ResyncInterval <= 0")
	}
}
