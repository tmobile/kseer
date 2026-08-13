package watcher

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// lastSeedQueryWindowMin is the floor for the last-seed lookup window. Even
// if no resync intervals are configured, we look back this far so P2 watch
// reconnects after long agent downtimes still find their last SEEDED.
const lastSeedQueryWindowMin = 7 * 24 * time.Hour

// lastSeedQueryWindowMax caps the lookup window so a misconfigured
// 30-day-plus interval doesn't make the query scan absurd amounts of data.
// 30 days fits comfortably within ClickHouse partition pruning given the
// chart's `PARTITION BY toYYYYMM(timestamp)` schema (1–2 partitions).
const lastSeedQueryWindowMax = 30 * 24 * time.Hour

// lastSeedQueryWindow computes the lookback window for GetLastSeedTimestampsByKind.
// Returned value is at least 2× the largest interval in play (P1 resync,
// P3 relist, P2 ResyncInterval) so transient missed cycles still surface a
// recent SEEDED row. Clamped to [lastSeedQueryWindowMin, lastSeedQueryWindowMax].
func (w *ClusterWatcher) lastSeedQueryWindow() time.Duration {
	var maxInterval time.Duration
	if w.watchConfig.ResyncInterval > maxInterval {
		maxInterval = w.watchConfig.ResyncInterval
	}
	if w.tierConfig.P1.ResyncInterval > maxInterval {
		maxInterval = w.tierConfig.P1.ResyncInterval
	}
	for _, k := range w.tierConfig.P1.Kinds {
		if k.ResyncInterval > maxInterval {
			maxInterval = k.ResyncInterval
		}
	}
	if w.tierConfig.P3.RelistInterval > maxInterval {
		maxInterval = w.tierConfig.P3.RelistInterval
	}
	for _, k := range w.tierConfig.P3.Kinds {
		if k.RelistInterval > maxInterval {
			maxInterval = k.RelistInterval
		}
	}
	window := 2 * maxInterval
	if window < lastSeedQueryWindowMin {
		window = lastSeedQueryWindowMin
	}
	if window > lastSeedQueryWindowMax {
		window = lastSeedQueryWindowMax
	}
	return window
}

// querySeederWithRetry wraps a ClickHouse seeder query with exponential
// backoff retry. Used at watcher startup so a transient CH unavailability
// (cold connection, brief outage, slow query under load) doesn't force the
// agent into a full-LIST or now+interval fallback when a quick retry would
// have succeeded.
//
// Behavior:
//   - Calls fn up to (1 + watchConfig.SeederQueryRetries) times.
//   - Backs off SeederQueryBackoff initially, doubling each attempt.
//   - Honors ctx cancellation (returns ctx.Err()).
//   - Returns the last error after exhausting retries — caller falls back.
//
// Caller convention: name is a short query identifier ("resume_rv",
// "last_seed_ts") used purely for logging.
func (w *ClusterWatcher) querySeederWithRetry(ctx context.Context, name string, fn func() error) error {
	retries := w.watchConfig.SeederQueryRetries
	if retries < 0 {
		retries = 0
	}
	backoff := w.watchConfig.SeederQueryBackoff
	if backoff <= 0 {
		backoff = 2 * time.Second
	}

	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt == retries {
			break
		}
		w.logger.Warn("CH seeder query failed, retrying",
			zap.String("query", name),
			zap.String("cluster", w.clusterName),
			zap.Int("attempt", attempt+1),
			zap.Int("retries", retries),
			zap.Duration("backoff", backoff),
			zap.Error(lastErr))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
	}
	return lastErr
}
