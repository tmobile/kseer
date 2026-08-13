package watcher

import (
	"testing"
	"time"

	"github.com/kseer/kseer-agent/pkg/config"
)

func TestTierResolver_KindOnlyMatch(t *testing.T) {
	r := newTierResolver(config.TierConfig{
		P1: config.P1TierConfig{
			ResyncInterval: 5 * time.Minute,
			Kinds:          []config.KindTierEntry{{Kind: "Namespace"}},
		},
	})

	if got := r.resolve("v1", "Namespace"); got.tier != TierP1 || got.interval != 5*time.Minute {
		t.Errorf("v1/Namespace: got tier=%v interval=%v; want P1, 5m", got.tier, got.interval)
	}
	if got := r.resolve("foo/v1", "Namespace"); got.tier != TierP1 {
		t.Errorf("foo/v1/Namespace should still match by kind-only; got tier=%v", got.tier)
	}
	if got := r.resolve("v1", "Pod"); got.tier != TierP2 {
		t.Errorf("Pod should default to P2; got tier=%v", got.tier)
	}
}

func TestTierResolver_ExactMatchOverridesKindOnly(t *testing.T) {
	r := newTierResolver(config.TierConfig{
		P1: config.P1TierConfig{
			ResyncInterval: 5 * time.Minute,
			Kinds:          []config.KindTierEntry{{Kind: "BigCRD"}},
		},
		P3: config.P3TierConfig{
			RelistInterval: 1 * time.Hour,
			Kinds: []config.KindTierEntry{
				{Kind: "BigCRD", APIVersion: "foo/v2"},
			},
		},
	})

	// foo/v2 BigCRD is explicitly P3 — exact match wins.
	if got := r.resolve("foo/v2", "BigCRD"); got.tier != TierP3 {
		t.Errorf("foo/v2/BigCRD: got tier=%v; want P3 (exact match)", got.tier)
	}
	// foo/v1 BigCRD falls back to kind-only P1.
	if got := r.resolve("foo/v1", "BigCRD"); got.tier != TierP1 {
		t.Errorf("foo/v1/BigCRD: got tier=%v; want P1 (kind-only fallback)", got.tier)
	}
}

func TestTierResolver_PerKindIntervalOverride(t *testing.T) {
	r := newTierResolver(config.TierConfig{
		P1: config.P1TierConfig{
			ResyncInterval: 10 * time.Minute,
			Kinds: []config.KindTierEntry{
				{Kind: "Namespace"},                                     // inherits 10m
				{Kind: "Node", ResyncInterval: 1 * time.Minute},         // overrides
			},
		},
	})

	if got := r.resolve("v1", "Namespace"); got.interval != 10*time.Minute {
		t.Errorf("Namespace interval: got %v; want 10m", got.interval)
	}
	if got := r.resolve("v1", "Node"); got.interval != 1*time.Minute {
		t.Errorf("Node interval: got %v; want 1m (override)", got.interval)
	}
}

func TestTierResolver_P4Match(t *testing.T) {
	// P4 is the WATCH-only-from-now stream tier (e.g. Event).
	r := newTierResolver(config.TierConfig{
		P4: config.P4TierConfig{
			Kinds: []config.KindTierEntry{
				{Kind: "Event"},
			},
		},
	})
	if got := r.resolve("v1", "Event"); got.tier != TierP4 {
		t.Errorf("Event should be P4 (stream); got %v", got.tier)
	}
	if got := r.resolve("events.k8s.io/v1", "Event"); got.tier != TierP4 {
		t.Errorf("events.k8s.io/v1 Event should match P4 by kind-only; got %v", got.tier)
	}
	// Pod is not configured anywhere, should be P2.
	if got := r.resolve("v1", "Pod"); got.tier != TierP2 {
		t.Errorf("Pod should default to P2; got %v", got.tier)
	}
}

func TestTierResolver_P5Match(t *testing.T) {
	// P5 is the ignore tier.
	r := newTierResolver(config.TierConfig{
		P5: config.P5TierConfig{
			Kinds: []config.KindTierEntry{
				{Kind: "AuthRequest"},
				{Kind: "Lease", APIVersion: "coordination.k8s.io/v1"},
			},
		},
	})
	if got := r.resolve("v1", "AuthRequest"); got.tier != TierP5 {
		t.Errorf("AuthRequest should be P5 (ignore); got %v", got.tier)
	}
	if got := r.resolve("coordination.k8s.io/v1", "Lease"); got.tier != TierP5 {
		t.Errorf("coordination/v1 Lease should be P5 (exact); got %v", got.tier)
	}
	// Lease in a different apiVersion should NOT be P5 (exact-match only).
	if got := r.resolve("other/v1", "Lease"); got.tier == TierP5 {
		t.Errorf("other/v1 Lease should NOT be P5 (exact-match only)")
	}
	// Pod is not configured anywhere, should be P2.
	if got := r.resolve("v1", "Pod"); got.tier != TierP2 {
		t.Errorf("Pod should default to P2; got %v", got.tier)
	}
}

func TestTierResolver_NilSafe(t *testing.T) {
	var r *tierResolver
	if got := r.resolve("v1", "Pod"); got.tier != TierP2 {
		t.Errorf("nil resolver should default to P2; got %v", got.tier)
	}
}

func TestTierResolver_P1KindsEnumerationPreservesOrder(t *testing.T) {
	cfg := config.TierConfig{
		P1: config.P1TierConfig{
			ResyncInterval: 5 * time.Minute,
			Kinds: []config.KindTierEntry{
				{Kind: "Namespace"},
				{Kind: "Node"},
				{Kind: "ConfigMap", ResyncInterval: 30 * time.Second},
			},
		},
	}
	r := newTierResolver(cfg)

	got := r.p1Kinds(cfg)
	if len(got) != 3 {
		t.Fatalf("expected 3 P1 kinds, got %d", len(got))
	}
	if got[0].kind != "Namespace" || got[1].kind != "Node" || got[2].kind != "ConfigMap" {
		t.Errorf("order not preserved: %v", got)
	}
	if got[2].interval != 30*time.Second {
		t.Errorf("ConfigMap interval override lost: got %v", got[2].interval)
	}
	if got[0].interval != 5*time.Minute {
		t.Errorf("Namespace should inherit global 5m; got %v", got[0].interval)
	}
}
