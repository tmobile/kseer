package config

import (
	"strings"
	"testing"
	"time"
)

func TestValidateTierConfig_RequiresP1Interval(t *testing.T) {
	tc := &TierConfig{
		P1: P1TierConfig{
			Kinds: []KindTierEntry{{Kind: "Namespace"}},
		},
	}
	err := validateTierConfig(tc, "test")
	if err == nil || !strings.Contains(err.Error(), "tiers.p1.resync_interval") {
		t.Errorf("expected p1.resync_interval error; got %v", err)
	}
}

func TestValidateTierConfig_RequiresP3Interval(t *testing.T) {
	tc := &TierConfig{
		P3: P3TierConfig{
			Kinds: []KindTierEntry{{Kind: "BigCRD"}},
		},
	}
	err := validateTierConfig(tc, "test")
	if err == nil || !strings.Contains(err.Error(), "tiers.p3.relist_interval") {
		t.Errorf("expected p3.relist_interval error; got %v", err)
	}
}

func TestValidateTierConfig_RejectsKindInBothTiers(t *testing.T) {
	tc := &TierConfig{
		P1: P1TierConfig{
			ResyncInterval: time.Minute,
			Kinds:          []KindTierEntry{{Kind: "ConfigMap"}},
		},
		P3: P3TierConfig{
			RelistInterval: time.Hour,
			Kinds:          []KindTierEntry{{Kind: "ConfigMap"}},
		},
	}
	err := validateTierConfig(tc, "test")
	if err == nil || !strings.Contains(err.Error(), "both tiers.p1 and tiers.p3") {
		t.Errorf("expected double-tier error; got %v", err)
	}
}

func TestValidateTierConfig_RejectsKindInP1AndP4(t *testing.T) {
	tc := &TierConfig{
		P1: P1TierConfig{
			ResyncInterval: time.Minute,
			Kinds:          []KindTierEntry{{Kind: "Event"}},
		},
		P4: P4TierConfig{
			Kinds: []KindTierEntry{{Kind: "Event"}},
		},
	}
	err := validateTierConfig(tc, "test")
	if err == nil || !strings.Contains(err.Error(), "tiers.p1 and tiers.p4") {
		t.Errorf("expected p1/p4 overlap error; got %v", err)
	}
}

func TestValidateTierConfig_RejectsKindInP3AndP5(t *testing.T) {
	tc := &TierConfig{
		P3: P3TierConfig{
			RelistInterval: time.Hour,
			Kinds:          []KindTierEntry{{Kind: "Lease"}},
		},
		P5: P5TierConfig{
			Kinds: []KindTierEntry{{Kind: "Lease"}},
		},
	}
	err := validateTierConfig(tc, "test")
	if err == nil || !strings.Contains(err.Error(), "tiers.p3 and tiers.p5") {
		t.Errorf("expected p3/p5 overlap error; got %v", err)
	}
}

func TestValidateTierConfig_RejectsKindInP4AndP5(t *testing.T) {
	tc := &TierConfig{
		P4: P4TierConfig{
			Kinds: []KindTierEntry{{Kind: "Event"}},
		},
		P5: P5TierConfig{
			Kinds: []KindTierEntry{{Kind: "Event"}},
		},
	}
	err := validateTierConfig(tc, "test")
	if err == nil || !strings.Contains(err.Error(), "tiers.p4 and tiers.p5") {
		t.Errorf("expected p4/p5 overlap error; got %v", err)
	}
}

func TestValidateTierConfig_AllowsKindInBothWhenAPIVersionDisambiguates(t *testing.T) {
	tc := &TierConfig{
		P1: P1TierConfig{
			ResyncInterval: time.Minute,
			Kinds:          []KindTierEntry{{Kind: "Foo", APIVersion: "a/v1"}},
		},
		P3: P3TierConfig{
			RelistInterval: time.Hour,
			Kinds:          []KindTierEntry{{Kind: "Foo", APIVersion: "b/v1"}},
		},
	}
	if err := validateTierConfig(tc, "test"); err != nil {
		t.Errorf("expected no error when apiVersions differ; got %v", err)
	}
}

func TestValidateTierConfig_RequiresKindName(t *testing.T) {
	tc := &TierConfig{
		P1: P1TierConfig{
			ResyncInterval: time.Minute,
			Kinds:          []KindTierEntry{{Kind: ""}},
		},
	}
	err := validateTierConfig(tc, "test")
	if err == nil || !strings.Contains(err.Error(), "kind is required") {
		t.Errorf("expected kind-required error; got %v", err)
	}
}

func TestValidateTierConfig_EmptyTierConfigIsValid(t *testing.T) {
	if err := validateTierConfig(&TierConfig{}, "test"); err != nil {
		t.Errorf("empty tier config should validate; got %v", err)
	}
}
