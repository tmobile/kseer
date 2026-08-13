package watcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kseer/kseer-agent/pkg/config"
	"go.uber.org/zap"
)

func newRetryTestWatcher(retries int, backoff time.Duration) *ClusterWatcher {
	return &ClusterWatcher{
		clusterName: "test",
		logger:      zap.NewNop(),
		watchConfig: config.WatchConfig{
			SeederQueryRetries: retries,
			SeederQueryBackoff: backoff,
		},
	}
}

func TestQuerySeederWithRetry_FirstAttemptSucceeds(t *testing.T) {
	w := newRetryTestWatcher(2, 10*time.Millisecond)
	calls := 0
	err := w.querySeederWithRetry(context.Background(), "test", func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestQuerySeederWithRetry_RecoverAfterTransient(t *testing.T) {
	w := newRetryTestWatcher(2, 5*time.Millisecond)
	calls := 0
	err := w.querySeederWithRetry(context.Background(), "test", func() error {
		calls++
		if calls < 2 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil after retry, got %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
}

func TestQuerySeederWithRetry_ExhaustsAndReturnsLastErr(t *testing.T) {
	w := newRetryTestWatcher(2, 5*time.Millisecond)
	calls := 0
	target := errors.New("last")
	err := w.querySeederWithRetry(context.Background(), "test", func() error {
		calls++
		return target
	})
	if !errors.Is(err, target) {
		t.Fatalf("expected last error %v, got %v", target, err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls (1 + 2 retries), got %d", calls)
	}
}

func TestQuerySeederWithRetry_ContextCancellation(t *testing.T) {
	w := newRetryTestWatcher(5, 100*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := w.querySeederWithRetry(ctx, "test", func() error {
		calls++
		return errors.New("transient")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if calls < 1 {
		t.Errorf("expected at least 1 call before cancel, got %d", calls)
	}
}

func TestLastSeedQueryWindow_BoundsApplied(t *testing.T) {
	cases := []struct {
		name      string
		watchCfg  config.WatchConfig
		tierCfg   config.TierConfig
		wantAtLeast time.Duration
		wantAtMost  time.Duration
	}{
		{
			name:        "no intervals → minimum",
			watchCfg:    config.WatchConfig{},
			tierCfg:     config.TierConfig{},
			wantAtLeast: lastSeedQueryWindowMin,
			wantAtMost:  lastSeedQueryWindowMin,
		},
		{
			name:     "P2 5d interval → 2× = 10d",
			watchCfg: config.WatchConfig{ResyncInterval: 5 * 24 * time.Hour},
			tierCfg:  config.TierConfig{},
			wantAtLeast: 10 * 24 * time.Hour,
			wantAtMost:  10 * 24 * time.Hour,
		},
		{
			name:     "P3 100d interval → clamped to max 30d",
			watchCfg: config.WatchConfig{},
			tierCfg: config.TierConfig{
				P3: config.P3TierConfig{RelistInterval: 100 * 24 * time.Hour},
			},
			wantAtLeast: lastSeedQueryWindowMax,
			wantAtMost:  lastSeedQueryWindowMax,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &ClusterWatcher{
				watchConfig: tc.watchCfg,
				tierConfig:  tc.tierCfg,
			}
			got := w.lastSeedQueryWindow()
			if got < tc.wantAtLeast || got > tc.wantAtMost {
				t.Errorf("got %v, expected in [%v, %v]", got, tc.wantAtLeast, tc.wantAtMost)
			}
		})
	}
}
