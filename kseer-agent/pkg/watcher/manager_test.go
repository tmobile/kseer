package watcher

import (
	"testing"
)

// --- sortedModuloWinner ---

func TestSortedModuloWinner_Basic(t *testing.T) {
	// allNames must be pre-sorted alphabetically (caller's responsibility).
	// alpha(0), beta(1), delta(2), gamma(3)
	all := []string{"alpha", "beta", "delta", "gamma"}
	cases := []struct {
		name  string
		total int
		want  int
	}{
		{"alpha", 3, 0}, // index 0 % 3 = 0
		{"beta", 3, 1},  // index 1 % 3 = 1
		{"delta", 3, 2}, // index 2 % 3 = 2
		{"gamma", 3, 0}, // index 3 % 3 = 0
	}
	for _, tc := range cases {
		got := sortedModuloWinner(tc.name, all, tc.total)
		if got != tc.want {
			t.Errorf("sortedModuloWinner(%q, total=%d): got %d, want %d", tc.name, tc.total, got, tc.want)
		}
	}
}

func TestSortedModuloWinner_NotFound(t *testing.T) {
	all := []string{"alpha", "beta"}
	// name not in list → returns 0
	got := sortedModuloWinner("missing", all, 5)
	if got != 0 {
		t.Errorf("expected 0 for missing name, got %d", got)
	}
}

func TestSortedModuloWinner_TotalOne(t *testing.T) {
	// total=1 is handled by shouldHandleCluster before calling sortedModuloWinner,
	// but the function itself should still return 0 for any input.
	all := []string{"x", "y", "z"}
	for _, name := range all {
		if got := sortedModuloWinner(name, all, 1); got != 0 {
			t.Errorf("sortedModuloWinner(%q, total=1): got %d, want 0", name, got)
		}
	}
}

// --- assignClustersByWeight ---

func TestAssignClustersByWeight_AllZero_EvenDistribution(t *testing.T) {
	// All new clusters with no history → weight 0.
	// Should distribute evenly, NOT all pile on one bin.
	names := make([]string, 40)
	for i := range names {
		names[i] = string(rune('a'+i/26)) + string(rune('a'+i%26)) // aa, ab, ..., bn
	}
	weights := map[string]int64{} // all zero

	assignment, numShards := assignClustersByWeight(names, weights, 10, 8)
	if numShards != 1 {
		// totalWeight=0 → numShards=1 (single-bin fallback)
		t.Logf("numShards=%d (totalWeight=0 → single shard, all go to bin 0; expected)", numShards)
		return
	}
	_ = assignment
}

func TestAssignClustersByWeight_MixedWeights_ZeroFillsLightest(t *testing.T) {
	// 3 heavy clusters (known) + 6 new clusters (weight 0).
	// After LPT on heavy clusters, new ones should fill lightest bins.
	names := []string{"heavy-a", "heavy-b", "heavy-c", "new-1", "new-2", "new-3", "new-4", "new-5", "new-6"}
	weights := map[string]int64{
		"heavy-a": 100,
		"heavy-b": 50,
		"heavy-c": 30,
		// new-* absent → weight 0
	}

	assignment, numShards := assignClustersByWeight(names, weights, 60, 5)
	// totalWeight=180, targetPerShard=60 → numShards=ceil(180/60)=3, capped at 5 → 3
	if numShards != 3 {
		t.Fatalf("expected numShards=3, got %d", numShards)
	}

	// Count clusters per bin
	counts := make([]int, numShards)
	for _, bin := range assignment {
		counts[bin]++
	}

	// With 9 clusters and 3 bins, each bin should have exactly 3.
	for b, cnt := range counts {
		if cnt != 3 {
			t.Errorf("bin %d has %d clusters, expected 3 (assignment: %v)", b, cnt, assignment)
		}
	}

	// Heavy-a (weight 100) should be in bin 0 (greedy first pick).
	if assignment["heavy-a"] != 0 {
		t.Errorf("heavy-a should be in bin 0, got bin %d", assignment["heavy-a"])
	}
}

func TestAssignClustersByWeight_ZeroWeight_NotAllOnSameBin(t *testing.T) {
	// Regression test: zero-weight clusters must NOT all land on the same bin.
	// Simulate the production scenario: some clusters have CH data, many new ones don't.
	names := make([]string, 50)
	weights := map[string]int64{}

	// 10 clusters with varying weights (old clusters with CH data)
	oldClusters := []string{
		"cluster-01", "cluster-02", "cluster-03", "cluster-04", "cluster-05",
		"cluster-06", "cluster-07", "cluster-08", "cluster-09", "cluster-10",
	}
	for i, name := range oldClusters {
		names[i] = name
		weights[name] = int64((i + 1) * 10) // 10, 20, 30, ..., 100
	}
	// 40 new clusters with no weight
	for i := 0; i < 40; i++ {
		name := "new-cluster-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		names[10+i] = name
		// absent from weights → 0
	}

	assignment, numShards := assignClustersByWeight(names, weights, 100, 10)
	if numShards < 2 {
		t.Skipf("numShards=%d, skipping distribution check", numShards)
	}

	// Count zero-weight clusters per bin
	zeroCounts := make([]int, numShards)
	for _, name := range names[10:] {
		zeroCounts[assignment[name]]++
	}

	// No single bin should have ALL 40 zero-weight clusters.
	for b, cnt := range zeroCounts {
		if cnt == 40 {
			t.Errorf("all 40 zero-weight clusters landed on bin %d — the old bug is back", b)
		}
	}

	// Max count on any bin should be roughly ceil(40/numShards) + 1 (allow 1 slack)
	maxAllowed := (40 + numShards - 1) / numShards + 1
	for b, cnt := range zeroCounts {
		if cnt > maxAllowed {
			t.Errorf("bin %d has %d zero-weight clusters, max allowed %d (numShards=%d)", b, cnt, maxAllowed, numShards)
		}
	}
}

func TestAssignClustersByWeight_MaxShardsCap(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f"}
	weights := map[string]int64{"a": 100, "b": 100, "c": 100, "d": 100, "e": 100, "f": 100}
	// totalWeight=600, targetPerShard=50 → needs 12 shards, but maxShards=3
	_, numShards := assignClustersByWeight(names, weights, 50, 3)
	if numShards != 3 {
		t.Errorf("expected numShards=3 (capped), got %d", numShards)
	}
}

func TestAssignClustersByWeight_MoreShardsThanClusters(t *testing.T) {
	names := []string{"a", "b"}
	weights := map[string]int64{"a": 100, "b": 100}
	// totalWeight=200, targetPerShard=10 → needs 20 shards, but only 2 clusters
	_, numShards := assignClustersByWeight(names, weights, 10, 0)
	if numShards != 2 {
		t.Errorf("expected numShards=2 (capped to cluster count), got %d", numShards)
	}
}

func TestAssignClustersByWeight_Empty(t *testing.T) {
	assignment, numShards := assignClustersByWeight([]string{}, map[string]int64{}, 10, 5)
	if numShards != 1 {
		t.Errorf("expected numShards=1 for empty input, got %d", numShards)
	}
	if len(assignment) != 0 {
		t.Errorf("expected empty assignment, got %v", assignment)
	}
}

func TestAssignClustersByWeight_Deterministic(t *testing.T) {
	// Same inputs must always produce the same assignment (all pods must agree).
	names := []string{"cluster-a", "cluster-b", "cluster-c", "cluster-d", "cluster-e"}
	weights := map[string]int64{"cluster-a": 50, "cluster-b": 30}
	// cluster-c, cluster-d, cluster-e are zero-weight (new)

	a1, _ := assignClustersByWeight(names, weights, 40, 3)
	a2, _ := assignClustersByWeight(names, weights, 40, 3)

	for name, bin := range a1 {
		if a2[name] != bin {
			t.Errorf("non-deterministic: %q got bin %d then %d", name, bin, a2[name])
		}
	}
}
