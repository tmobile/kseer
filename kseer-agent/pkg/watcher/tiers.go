package watcher

import (
	"time"

	"github.com/kseer/kseer-agent/pkg/config"
)

// Tier classifies how the agent treats a Kubernetes kind:
//
//   - TierP2 (default): legacy LIST+WATCH path with optional resync.
//   - TierP1: LIST+WATCH path PLUS a sequential resync loop that re-LISTs each
//     P1 kind and waits for the output buffer to flush before moving to the
//     next kind. P1 LISTs are non-paginated.
//   - TierP3: no WATCH at all. Periodic LIST only; each LIST emits EXISTING
//     events for every current object. The agent does not synthesize DELETED
//     events. Client queries determine "live" objects by filtering events to
//     within the most recent relistInterval window — anything older than one
//     interval is implicitly stale.
//   - TierP4: WATCH-only from the current point in time. No initial LIST of
//     existing objects, no periodic relist, no SEEDED sentinel, and never
//     resumes from a stored/seeder resourceVersion. The agent probes the
//     current collection RV and opens a WATCH from it, capturing only changes
//     going forward. Ideal for ephemeral, high-volume, stream-shaped kinds
//     like Event where backfilling state is pointless.
//   - TierP5: ignored entirely. Discovery skips dispatching for these kinds —
//     no LIST, no WATCH, no relist, not added to gvrKindMap or status counts.
type Tier int

const (
	TierP2 Tier = iota
	TierP1
	TierP3
	TierP4
	TierP5
)

// String returns a stable lowercase label for logging.
func (t Tier) String() string {
	switch t {
	case TierP1:
		return "p1"
	case TierP3:
		return "p3"
	case TierP4:
		return "p4"
	case TierP5:
		return "p5"
	default:
		return "p2"
	}
}

// tierAssignment is the resolved (tier, interval) for a given kind/apiVersion.
// Interval is the effective per-kind value: the entry's override if set,
// else the tier's global default. Zero for TierP2 (no resync) unless
// WatchConfig.ResyncInterval is non-zero, which is handled separately.
type tierAssignment struct {
	tier     Tier
	interval time.Duration
}

// tierResolver maps (apiVersion, kind) → tier assignment. Built once per
// cluster from WatchConfig.Tiers and consulted during resource discovery.
type tierResolver struct {
	// exact lookup: "<apiVersion>|<kind>" → assignment
	exact map[string]tierAssignment
	// fallback by kind name only (entries with empty apiVersion)
	byKind map[string]tierAssignment
}

func newTierResolver(t config.TierConfig) *tierResolver {
	r := &tierResolver{
		exact:  make(map[string]tierAssignment),
		byKind: make(map[string]tierAssignment),
	}

	for _, e := range t.P1.Kinds {
		interval := e.ResyncInterval
		if interval == 0 {
			interval = t.P1.ResyncInterval
		}
		a := tierAssignment{tier: TierP1, interval: interval}
		if e.APIVersion != "" {
			r.exact[e.APIVersion+"|"+e.Kind] = a
		} else {
			r.byKind[e.Kind] = a
		}
	}

	for _, e := range t.P3.Kinds {
		interval := e.RelistInterval
		if interval == 0 {
			interval = t.P3.RelistInterval
		}
		a := tierAssignment{tier: TierP3, interval: interval}
		if e.APIVersion != "" {
			r.exact[e.APIVersion+"|"+e.Kind] = a
		} else {
			r.byKind[e.Kind] = a
		}
	}

	for _, e := range t.P4.Kinds {
		a := tierAssignment{tier: TierP4}
		if e.APIVersion != "" {
			r.exact[e.APIVersion+"|"+e.Kind] = a
		} else {
			r.byKind[e.Kind] = a
		}
	}

	for _, e := range t.P5.Kinds {
		a := tierAssignment{tier: TierP5}
		if e.APIVersion != "" {
			r.exact[e.APIVersion+"|"+e.Kind] = a
		} else {
			r.byKind[e.Kind] = a
		}
	}

	return r
}

// resolve returns the tier and effective interval for the given (apiVersion,
// kind). Exact (apiVersion, kind) match wins over kind-only match. If neither
// matches, returns TierP2 with a zero interval.
func (r *tierResolver) resolve(apiVersion, kind string) tierAssignment {
	if r == nil {
		return tierAssignment{tier: TierP2}
	}
	if a, ok := r.exact[apiVersion+"|"+kind]; ok {
		return a
	}
	if a, ok := r.byKind[kind]; ok {
		return a
	}
	return tierAssignment{tier: TierP2}
}

// p1Kinds returns all (apiVersion, kind) pairs assigned to P1 along with
// their effective interval, in the order they were configured (P1.Kinds).
// Used by the watcher's sequential P1 resync loop to walk kinds deterministically.
func (r *tierResolver) p1Kinds(t config.TierConfig) []p1KindRef {
	out := make([]p1KindRef, 0, len(t.P1.Kinds))
	for _, e := range t.P1.Kinds {
		interval := e.ResyncInterval
		if interval == 0 {
			interval = t.P1.ResyncInterval
		}
		out = append(out, p1KindRef{
			apiVersion: e.APIVersion,
			kind:       e.Kind,
			interval:   interval,
		})
	}
	return out
}

type p1KindRef struct {
	apiVersion string
	kind       string
	interval   time.Duration
}
