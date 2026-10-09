package gatewayhotquality

import (
	"context"
	"math/rand"
	"strings"
	"testing"
)

// Cache-rate-aware within-tier ordering tests (design doc
// docs/functions/缓存率感知调度与用量缓存率展示设计.md sections 5/6/10/11.1).
// Reuses the hqCandidate / baseOf / normalTier helpers from
// hotquality_selection_test.go.

// crSpeed builds a known/healthy snapshot with an optionally known EWMA and an
// optional P95 bucket index (the bucket is never treated as milliseconds).
func crSpeed(ewmaMs float64, ewmaKnown bool, p95Bucket *float64) func(*HotQualityCandidate) {
	return func(candidate *HotQualityCandidate) {
		snapshot := &HotQualitySelectionSnapshot{SampleState: HotQualitySampleKnown, ReliabilityLevel: HotQualityReliabilityHealthy}
		if ewmaKnown {
			snapshot.FirstByteEwma5m = floatPtr(ewmaMs)
		}
		snapshot.FirstByteP95Bucket10m = p95Bucket
		candidate.HotQuality = snapshot
	}
}

// crWindow builds a 24h window whose rate is exactly percent/100: the token
// counts stay integral, so the quantization input is exact.
func crWindow(percent int, inputTokens int64) CacheRateWindow {
	return CacheRateWindow{
		CacheReadTokens: inputTokens * int64(percent) / 100,
		InputTokens:     inputTokens,
	}
}

// crCacheSnapshot carries the cache window on the assembly-time snapshot
// itself (the alternative injection source to the decision-input map).
func crCacheSnapshot(rate float64, rateKnown bool, windowInputTokens int64) func(*HotQualityCandidate) {
	return func(candidate *HotQualityCandidate) {
		if candidate.HotQuality == nil {
			candidate.HotQuality = &HotQualitySelectionSnapshot{SampleState: HotQualitySampleKnown, ReliabilityLevel: HotQualityReliabilityHealthy}
		}
		candidate.HotQuality.CacheWindowInputTokens = windowInputTokens
		if rateKnown {
			candidate.HotQuality.CacheHitRate = floatPtr(rate)
		}
	}
}

func crDecide(
	t *testing.T,
	mode HotQualityRoutingMode,
	candidates []HotQualityCandidatePayload,
	cacheRates map[string]CacheRateWindow,
	mutate func(*DecideHotQualityCandidateInput[HotQualityCandidatePayload]),
) *HotQualityCandidateDecision[HotQualityCandidatePayload] {
	t.Helper()
	input := DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
		RouteScopeKey: "scope",
		Mode:          mode,
		Base:          baseOf,
		Candidates:    candidates,
		CacheRates:    cacheRates,
	}
	if mutate != nil {
		mutate(&input)
	}
	decision, err := DecideHotQualityCandidate(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return decision
}

func crLabels(decision *HotQualityCandidateDecision[HotQualityCandidatePayload]) []string {
	labels := make([]string, 0, len(decision.OrderedCandidates))
	for _, payload := range decision.OrderedCandidates {
		labels = append(labels, payload.Payload.label)
	}
	return labels
}

func crAssertOrder(t *testing.T, decision *HotQualityCandidateDecision[HotQualityCandidatePayload], want ...string) {
	t.Helper()
	got := crLabels(decision)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func crDetailByAccount(t *testing.T, decision *HotQualityCandidateDecision[HotQualityCandidatePayload]) map[string]HotQualityCandidateOrderDetail {
	t.Helper()
	details := map[string]HotQualityCandidateOrderDetail{}
	for _, detail := range decision.Explanation.CandidateOrderDetails {
		if _, ok := details[detail.AccountID]; ok {
			t.Fatalf("duplicate detail for %s", detail.AccountID)
		}
		details[detail.AccountID] = detail
	}
	return details
}

func TestCacheRateQuantizationBoundaries(t *testing.T) {
	// Exact tier-boundary rates belong to their own tier, per mode tier width
	// (section 5.5): 20pp for cost_first, 10pp for speed_first.
	for k := 0; k <= 5; k++ {
		rate := float64(k) * CacheRateQuantumCostFirst
		if got := cacheQuantumOf(speedQualificationParamsForMode(HotQualityModeCostFirst), rate); got != k {
			t.Fatalf("cost_first cacheQuantumOf(%.2f) = %d, want %d", rate, got, k)
		}
	}
	for k := 0; k <= 10; k++ {
		rate := float64(k) / 10
		if got := cacheQuantumOf(speedQualificationParamsForMode(HotQualityModeSpeedFirst), rate); got != k {
			t.Fatalf("speed_first cacheQuantumOf(%.2f) = %d, want %d", rate, got, k)
		}
	}
	// Off-boundary rates floor normally (cost_first 20pp tiers).
	cases := []struct {
		rate float64
		want int
	}{{0.0, 0}, {0.19, 0}, {0.21, 1}, {0.39, 1}, {0.99, 4}, {0.999, 4}}
	for _, testCase := range cases {
		if got := cacheQuantumOf(speedQualificationParamsForMode(HotQualityModeCostFirst), testCase.rate); got != testCase.want {
			t.Fatalf("cost_first cacheQuantumOf(%v) = %d, want %d", testCase.rate, got, testCase.want)
		}
	}
	// Off-boundary rates floor normally (speed_first 10pp tiers): 9% and 11%
	// straddle the 10pp boundary and split into tiers 0 and 1.
	speedOffBoundary := []struct {
		rate float64
		want int
	}{{0.09, 0}, {0.11, 1}}
	for _, testCase := range speedOffBoundary {
		if got := cacheQuantumOf(speedQualificationParamsForMode(HotQualityModeSpeedFirst), testCase.rate); got != testCase.want {
			t.Fatalf("speed_first cacheQuantumOf(%v) = %d, want %d", testCase.rate, got, testCase.want)
		}
	}
	// The clamp upper bound is floor(1/tier width) per mode: 5 vs 10.
	if got := cacheQuantumOf(speedQualificationParamsForMode(HotQualityModeCostFirst), 1.0); got != 5 {
		t.Fatalf("cost_first clamp = %d, want 5", got)
	}
	if got := cacheQuantumOf(speedQualificationParamsForMode(HotQualityModeSpeedFirst), 1.0); got != 10 {
		t.Fatalf("speed_first clamp = %d, want 10", got)
	}
}

// TestCacheRateComparatorWeakOrderingProperties asserts the strict weak
// ordering properties of compareWithinTier directly over the production
// materialization path (section 11.1 P0): irreflexive except for full
// equality, antisymmetric, and transitive across a candidate set mixing every
// ordering dimension — window edge, absolute ceiling breach, cache tiers,
// qualified-set in/out, P95-only and no-signal candidates. Cache data rides
// the snapshot-injection path (nil cacheRates map) to cover that assembly
// route alongside the explicit-window-map route used elsewhere.
func TestCacheRateComparatorWeakOrderingProperties(t *testing.T) {
	compose := func(mutates ...func(*HotQualityCandidate)) func(*HotQualityCandidate) {
		return func(candidate *HotQualityCandidate) {
			for _, mutate := range mutates {
				mutate(candidate)
			}
		}
	}
	candidates := []HotQualityCandidatePayload{
		hqCandidate("fast-rich", normalTier(), compose(crSpeed(1_000, true, nil), crCacheSnapshot(0.90, true, 1_000_000))),
		hqCandidate("mid-poor", normalTier(), compose(crSpeed(2_000, true, nil), crCacheSnapshot(0.05, true, 1_000_000))),
		hqCandidate("mid-tie", normalTier(), compose(crSpeed(2_500, true, nil), crCacheSnapshot(0.25, true, 1_000_000))),
		hqCandidate("edge-window", normalTier(), compose(crSpeed(4_000, true, nil), crCacheSnapshot(0.45, true, 1_000_000))),
		hqCandidate("beyond-window", normalTier(), compose(crSpeed(6_000, true, nil), crCacheSnapshot(0.65, true, 1_000_000))),
		hqCandidate("beyond-ceiling", normalTier(), compose(crSpeed(12_000, true, nil), crCacheSnapshot(0.85, true, 1_000_000))),
		hqCandidate("p95-only", normalTier(), compose(crSpeed(0, false, floatPtr(3)), crCacheSnapshot(0.15, true, 1_000_000))),
		hqCandidate("no-signal", normalTier(), compose(crSpeed(0, false, nil), crCacheSnapshot(0.55, true, 1_000_000))),
	}
	normalized, _, err := normalizeCandidates(candidates, baseOf, "scope")
	if err != nil {
		t.Fatalf("normalizeCandidates: %v", err)
	}
	tier := candidatesOfTier(normalized, normalized[0].tierKey)
	if len(tier) != len(candidates) {
		t.Fatalf("tier split across keys: %d/%d", len(tier), len(candidates))
	}
	// cost_first: vBase=1s → W=max(3s,1s)=3s, ceiling 10s, 20pp tiers.
	materializeTierOrderKeys(tier, nil, speedQualificationParamsForMode(HotQualityModeCostFirst), 0)

	compare := compareWithinTier[HotQualityCandidatePayload]
	name := func(candidate *indexedCandidate[HotQualityCandidatePayload]) string { return candidate.base.AccountID }
	for i := range tier {
		self := compare(tier[i], tier[i])
		if self != 0 {
			t.Fatalf("irreflexivity violated for %s: %d", name(tier[i]), self)
		}
		for j := range tier {
			if i == j {
				continue
			}
			forward := compare(tier[i], tier[j])
			backward := compare(tier[j], tier[i])
			if forward != -backward {
				t.Fatalf("antisymmetry violated: %s vs %s: %d / %d", name(tier[i]), name(tier[j]), forward, backward)
			}
		}
	}
	for i := range tier {
		for j := range tier {
			for k := range tier {
				ab := compare(tier[i], tier[j])
				bc := compare(tier[j], tier[k])
				ac := compare(tier[i], tier[k])
				if ab < 0 && bc < 0 && ac >= 0 {
					t.Fatalf("transitivity violated: %s < %s < %s but not %s < %s",
						name(tier[i]), name(tier[j]), name(tier[k]), name(tier[i]), name(tier[k]))
				}
				if ab == 0 && bc == 0 && ac != 0 {
					t.Fatalf("equality transitivity violated: %s == %s == %s but %s vs %s = %d",
						name(tier[i]), name(tier[j]), name(tier[k]), name(tier[i]), name(tier[k]), ac)
				}
			}
		}
	}
}

// TestDecideHotQualityCandidateCacheRateLoopSample locks the section 5.4
// verdict with the relative window: A(1s/0%) B(4s/20%) C(7s/40%) under
// cost_first (W = max(3×1s, 1s) = 3s) resolves to B -> A -> C for every input
// permutation — B (diff 3s, exactly W) stays qualified, C (diff 6s) drops out
// of the qualified set and is judged by the speed key. The pairwise dead-zone
// cycle (B>A, C>B, A>C) must never leak into the result.
func TestDecideHotQualityCandidateCacheRateLoopSample(t *testing.T) {
	cacheRates := map[string]CacheRateWindow{
		"A": crWindow(0, 1_000_000),
		"B": crWindow(20, 1_000_000),
		"C": crWindow(40, 1_000_000),
	}
	build := func() []HotQualityCandidatePayload {
		return []HotQualityCandidatePayload{
			hqCandidate("A", normalTier(), crSpeed(1_000, true, nil)),
			hqCandidate("B", normalTier(), crSpeed(4_000, true, nil)),
			hqCandidate("C", normalTier(), crSpeed(7_000, true, nil)),
		}
	}
	// All 6 input permutations (index triples over A,B,C).
	for _, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		source := build()
		permuted := []HotQualityCandidatePayload{source[order[0]], source[order[1]], source[order[2]]}
		decision := crDecide(t, HotQualityModeCostFirst, permuted, cacheRates, nil)
		crAssertOrder(t, decision, "B", "A", "C")

		speedDecision := crDecide(t, HotQualityModeSpeedFirst, permuted, cacheRates, nil)
		// speed_first (W = max(1×1s, 1s) = 1s): A qualified (diff 0); B
		// (diff 3s) and C (6s) unqualified. The unqualified pair skips the
		// cache tier and is decided by the materialized speed key, so the
		// faster B keeps the lead over the richer-cache C (speed dominance
		// hard rule).
		crAssertOrder(t, speedDecision, "A", "B", "C")
	}

	// The same windows carried on the assembly-time snapshots (instead of the
	// decision-input map) must produce identical orders.
	snapshotCarried := build()
	snapshotRates := map[string]struct {
		rate      float64
		rateKnown bool
	}{"A": {0, true}, "B": {0.20, true}, "C": {0.40, true}}
	ewmaByLabel := map[string]float64{"A": 1_000, "B": 4_000, "C": 7_000}
	for index := range snapshotCarried {
		label := snapshotCarried[index].Candidate.AccountID
		mutate := crSpeed(ewmaByLabel[label], true, nil)
		mutate(&snapshotCarried[index].Candidate)
		window := snapshotRates[label]
		crCacheSnapshot(window.rate, window.rateKnown, 1_000_000)(&snapshotCarried[index].Candidate)
	}
	decision := crDecide(t, HotQualityModeCostFirst, snapshotCarried, nil, nil)
	crAssertOrder(t, decision, "B", "A", "C")
}

// TestDecideHotQualityCandidateCacheRateShuffleInvariance is the P0 property
// test: a mixed candidate set (EWMA-only / P95-only / no signal / valid cache /
// invalid cache) must produce the identical total order under every input
// permutation, in both modes. The pinned baseline orders lock the relative
// window/ceiling/quantum parameters: cost_first (W = max(3×1s, 1s) = 3s,
// ceiling 10s, 20pp tiers) qualifies everything except probe-3 (12s) and ranks
// probe-1(q4) → probe-4(q2) → probe-2(q1) → the -1 group by speed key → the
// unqualified tail ordered by the speed key (probe-3 rank 0, probe-5 rank 2);
// speed_first (W = 1s, ceiling 5s, 10pp tiers) additionally drops probe-2
// (diff 3s > 1s) into that tail ahead of probe-5.
func TestDecideHotQualityCandidateCacheRateShuffleInvariance(t *testing.T) {
	build := func() []HotQualityCandidatePayload {
		return []HotQualityCandidatePayload{
			hqCandidate("probe-1", normalTier(), crSpeed(1_000, true, nil)),         // EWMA fast, cache 85%
			hqCandidate("probe-2", normalTier(), crSpeed(4_000, true, nil)),         // EWMA mid, cache 20%
			hqCandidate("probe-3", normalTier(), crSpeed(12_000, true, nil)),        // EWMA slow, cache 40%
			hqCandidate("probe-4", normalTier(), crSpeed(0, false, floatPtr(3))),    // P95-only, cache 50%
			hqCandidate("probe-5", normalTier(), nil),                               // cold, no signals, no cache
			hqCandidate("probe-6", normalTier(), crSpeed(2_000, true, nil)),         // EWMA, no cache data
			hqCandidate("probe-7", normalTier(), crSpeed(0, false, floatPtr(1))),    // P95-only, sub-gate cache
			hqCandidate("probe-8", normalTier(), crSpeed(1_000, true, floatPtr(2))), // EWMA ties probe-1, bucket tiebreak
		}
	}
	cacheRates := map[string]CacheRateWindow{
		"probe-1": crWindow(85, 1_000_000),
		"probe-2": crWindow(20, 1_000_000),
		"probe-3": crWindow(40, 1_000_000),
		"probe-4": crWindow(50, 1_000_000),
		"probe-7": crWindow(99, 50_000), // below the sample gate: invalid
	}
	wantOrders := map[HotQualityRoutingMode]string{
		HotQualityModeCostFirst:  "probe-1,probe-4,probe-2,probe-8,probe-6,probe-7,probe-3,probe-5",
		HotQualityModeSpeedFirst: "probe-1,probe-4,probe-8,probe-6,probe-7,probe-2,probe-3,probe-5",
	}
	shuffler := rand.New(rand.NewSource(20261008))
	for _, mode := range []HotQualityRoutingMode{HotQualityModeCostFirst, HotQualityModeSpeedFirst} {
		baseline := crDecide(t, mode, build(), cacheRates, nil)
		want := strings.Join(crLabels(baseline), ",")
		if want != wantOrders[mode] {
			t.Fatalf("mode %s baseline order = %q, want %q", mode, want, wantOrders[mode])
		}
		if got := strings.Count(want, ",") + 1; got != 8 {
			t.Fatalf("candidate count = %d", got)
		}
		for iteration := 0; iteration < 24; iteration++ {
			candidates := build()
			shuffler.Shuffle(len(candidates), func(left, right int) {
				candidates[left], candidates[right] = candidates[right], candidates[left]
			})
			decision := crDecide(t, mode, candidates, cacheRates, nil)
			if got := strings.Join(crLabels(decision), ","); got != want {
				t.Fatalf("mode %s iteration %d: order %q, want %q", mode, iteration, got, want)
			}
		}
	}
}

func TestDecideHotQualityCandidateOrderKeySequence(t *testing.T) {
	t.Run("reliability dominates cache rate", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("healthy-low", normalTier(), crSpeed(1_000, true, nil)),
			hqCandidate("unhealthy-high", normalTier(), func(candidate *HotQualityCandidate) {
				candidate.HotQuality = &HotQualitySelectionSnapshot{
					SampleState:      HotQualitySampleKnown,
					ReliabilityLevel: HotQualityReliabilityUnhealthy,
					FirstByteEwma5m:  floatPtr(2_000),
				}
			}),
		}
		cacheRates := map[string]CacheRateWindow{
			"healthy-low":    crWindow(0, 1_000_000),
			"unhealthy-high": crWindow(90, 1_000_000),
		}
		crAssertOrder(t, crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil), "healthy-low", "unhealthy-high")
	})

	t.Run("qualified set beats higher cache tier", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("qualified-low", normalTier(), crSpeed(1_000, true, nil)),
			hqCandidate("slow-rich", normalTier(), crSpeed(20_000, true, nil)),
		}
		cacheRates := map[string]CacheRateWindow{
			"qualified-low": crWindow(0, 1_000_000),
			"slow-rich":     crWindow(90, 1_000_000),
		}
		// cost_first: W = 3s, slow-rich diff 19s > W and 20s > 10s ceiling →
		// unqualified despite q=4.
		decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
		crAssertOrder(t, decision, "qualified-low", "slow-rich")
		details := crDetailByAccount(t, decision)
		if !details["qualified-low"].SpeedQualified || details["slow-rich"].SpeedQualified {
			t.Fatalf("qualified flags = %+v", details)
		}
	})

	t.Run("qualified membership separates across the threshold edge", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("baseline-fast", normalTier(), crSpeed(1_000, true, nil)), // pins vBase
			hqCandidate("edge-in", normalTier(), crSpeed(10_000, true, nil)),      // diff 9s > W=3s (10s == ceiling) → unqualified
			hqCandidate("edge-out", normalTier(), crSpeed(12_000, true, nil)),     // diff 11s > W and 12s > ceiling → unqualified
		}
		cacheRates := map[string]CacheRateWindow{
			"baseline-fast": crWindow(50, 1_000_000),
			"edge-in":       crWindow(0, 1_000_000),
			"edge-out":      crWindow(90, 1_000_000),
		}
		// Key 3 separates the two unqualified candidates from baseline-fast
		// before any cache comparison: the richer and merely-2s-slower
		// candidate outside the qualified set must not outrank the qualified
		// baseline. The unqualified pair then falls to the speed key.
		decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
		crAssertOrder(t, decision, "baseline-fast", "edge-in", "edge-out")
		details := crDetailByAccount(t, decision)
		if !details["baseline-fast"].SpeedQualified || details["edge-in"].SpeedQualified || details["edge-out"].SpeedQualified {
			t.Fatalf("qualified flags = %+v", details)
		}
	})

	t.Run("both unqualified skip cache tier and compare speed", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("baseline-fast", normalTier(), crSpeed(1_000, true, nil)), // qualified, pins vBase
			hqCandidate("out-poor", normalTier(), crSpeed(20_000, true, nil)),     // diff 19s → unqualified, q=0
			hqCandidate("out-rich", normalTier(), crSpeed(30_000, true, nil)),     // diff 29s → unqualified, q=4
		}
		cacheRates := map[string]CacheRateWindow{
			"baseline-fast": crWindow(50, 1_000_000),
			"out-poor":      crWindow(10, 1_000_000),
			"out-rich":      crWindow(90, 1_000_000),
		}
		// Within the unqualified pair the cache tier is skipped entirely: the
		// EWMA-ascending speed key decides, so the 10%-cache but faster
		// candidate outranks the 90%-cache slower one. Speed beyond the
		// window/ceiling is the judge, never the cache tier.
		decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
		crAssertOrder(t, decision, "baseline-fast", "out-poor", "out-rich")
		details := crDetailByAccount(t, decision)
		if details["out-poor"].SpeedQualified || details["out-rich"].SpeedQualified {
			t.Fatalf("qualified flags = %+v", details)
		}
		if details["out-poor"].CacheQuantum != 0 || details["out-rich"].CacheQuantum != 4 {
			t.Fatalf("quantums = %+v", details)
		}
	})

	t.Run("cache quantum descending inside qualified set", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("fast-poor", normalTier(), crSpeed(1_000, true, nil)),
			hqCandidate("nearby-rich", normalTier(), crSpeed(3_000, true, nil)),
		}
		cacheRates := map[string]CacheRateWindow{
			"fast-poor":   crWindow(0, 1_000_000),
			"nearby-rich": crWindow(90, 1_000_000),
		}
		// Both qualified (diff 2s <= W = 3s, 3s <= 10s ceiling); the richer
		// cache tier wins over raw EWMA.
		crAssertOrder(t, crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil), "nearby-rich", "fast-poor")
	})

	t.Run("minus-one sentinel is not zero percent", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("z-valid-zero", normalTier(), crSpeed(1_000, true, nil)),
			hqCandidate("a-no-data", normalTier(), crSpeed(1_000, true, nil)),
		}
		cacheRates := map[string]CacheRateWindow{"z-valid-zero": crWindow(0, 1_000_000)}
		// Equal speeds and the no-data account wins the AccountID tiebreak, so
		// only the sentinel keeps the valid-0% candidate first.
		crAssertOrder(t, crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil), "z-valid-zero", "a-no-data")
	})

	t.Run("speed signal ranks ewma then p95-only then none", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("silent", normalTier(), nil),
			hqCandidate("p95-only", normalTier(), crSpeed(0, false, floatPtr(0))),
			hqCandidate("ewma-known", normalTier(), crSpeed(8_000, true, nil)),
		}
		decision := crDecide(t, HotQualityModeCostFirst, candidates, nil, nil)
		crAssertOrder(t, decision, "ewma-known", "p95-only", "silent")
	})

	t.Run("stable binding order then account id", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("b-late", normalTier(), func(candidate *HotQualityCandidate) {
				candidate.StableBindingOrder = 7
				candidate.HotQuality = &HotQualitySelectionSnapshot{SampleState: HotQualitySampleKnown, ReliabilityLevel: HotQualityReliabilityHealthy}
			}),
			hqCandidate("a-early", normalTier(), func(candidate *HotQualityCandidate) {
				candidate.StableBindingOrder = 3
				candidate.HotQuality = &HotQualitySelectionSnapshot{SampleState: HotQualitySampleKnown, ReliabilityLevel: HotQualityReliabilityHealthy}
			}),
			hqCandidate("a-second", normalTier(), func(candidate *HotQualityCandidate) {
				candidate.StableBindingOrder = 7
				candidate.HotQuality = &HotQualitySelectionSnapshot{SampleState: HotQualitySampleKnown, ReliabilityLevel: HotQualityReliabilityHealthy}
			}),
		}
		// Binding order groups first (3 < 7); the binding=7 tie then falls to
		// the AccountID key.
		crAssertOrder(t, crDecide(t, HotQualityModeCostFirst, candidates, nil, nil), "a-early", "a-second", "b-late")
	})
}

func TestDecideHotQualityCandidateSpeedThresholdModes(t *testing.T) {
	candidates := []HotQualityCandidatePayload{
		hqCandidate("fast-poor", normalTier(), crSpeed(1_000, true, nil)),
		hqCandidate("nearby-rich", normalTier(), crSpeed(4_000, true, nil)),
	}
	cacheRates := map[string]CacheRateWindow{
		"fast-poor":   crWindow(0, 1_000_000),
		"nearby-rich": crWindow(90, 1_000_000),
	}
	// cost_first: W = max(3×1s, 1s) = 3s; nearby-rich diff 3s == W and 4s <=
	// 10s ceiling → qualified, so the cache tier wins.
	decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
	crAssertOrder(t, decision, "nearby-rich", "fast-poor")
	if decision.Explanation.SpeedThresholdMs != 3_000 {
		t.Fatalf("threshold = %d, want the 3s window W", decision.Explanation.SpeedThresholdMs)
	}
	// speed_first: W = max(1×1s, 1s) = 1s; diff 3s > W → unqualified, the fast
	// candidate leads.
	speedDecision := crDecide(t, HotQualityModeSpeedFirst, candidates, cacheRates, nil)
	crAssertOrder(t, speedDecision, "fast-poor", "nearby-rich")
	if speedDecision.Explanation.SpeedThresholdMs != 1_000 {
		t.Fatalf("speed threshold = %d, want the 1s window W", speedDecision.Explanation.SpeedThresholdMs)
	}
	// Explicit override wins over the mode-backed window.
	override := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, func(input *DecideHotQualityCandidateInput[HotQualityCandidatePayload]) {
		input.SpeedThresholdMs = 1_000
	})
	crAssertOrder(t, override, "fast-poor", "nearby-rich")
	if override.Explanation.SpeedThresholdMs != 1_000 {
		t.Fatalf("override threshold = %d", override.Explanation.SpeedThresholdMs)
	}
}

func TestDecideHotQualityCandidateCacheKeyEnablement(t *testing.T) {
	t.Run("layer without valid data equals cache off", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("sub-gate-rich", normalTier(), crSpeed(2_500, true, nil)),
			hqCandidate("fast-clean", normalTier(), crSpeed(1_000, true, nil)),
		}
		// The only cache data is below the sample gate: the layer disables the
		// cache key and pure EWMA ordering stands (both stay qualified:
		// diff 1.5s <= W = 3s).
		cacheRates := map[string]CacheRateWindow{"sub-gate-rich": crWindow(99, 50_000)}
		decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
		crAssertOrder(t, decision, "fast-clean", "sub-gate-rich")
		if decision.Explanation.CacheRateEnabled {
			t.Fatal("cache key must be disabled without valid samples")
		}
		if decision.Explanation.SpeedBaseEwmaMs == nil || *decision.Explanation.SpeedBaseEwmaMs != 1_000 {
			t.Fatalf("speed base = %v", decision.Explanation.SpeedBaseEwmaMs)
		}

		// Raising the same account over the gate enables the key and flips the
		// winner to the 99% cache tier (both stay qualified: diff 1.5s <=
		// W = 3s).
		enabledRates := map[string]CacheRateWindow{"sub-gate-rich": crWindow(99, 1_000_000)}
		enabled := crDecide(t, HotQualityModeCostFirst, candidates, enabledRates, nil)
		crAssertOrder(t, enabled, "sub-gate-rich", "fast-clean")
		if !enabled.Explanation.CacheRateEnabled {
			t.Fatal("cache key must be enabled with a valid sample")
		}
	})

	t.Run("partial invalid data keeps the sentinel", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("no-data", normalTier(), nil),
			hqCandidate("rich", normalTier(), nil),
			hqCandidate("mid", normalTier(), nil),
		}
		cacheRates := map[string]CacheRateWindow{
			"rich": crWindow(40, 1_000_000),
			"mid":  crWindow(20, 1_000_000),
		}
		decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
		crAssertOrder(t, decision, "rich", "mid", "no-data")
		details := crDetailByAccount(t, decision)
		if details["rich"].CacheQuantum != 2 || details["mid"].CacheQuantum != 1 || details["no-data"].CacheQuantum != -1 {
			t.Fatalf("quantums = %+v", details)
		}
		if details["no-data"].CacheHitRate != nil {
			t.Fatalf("no-data rate = %v", details["no-data"].CacheHitRate)
		}
	})

	t.Run("sample gate boundary 99999 vs 100000", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("a-below", normalTier(), nil),
			hqCandidate("b-at-gate", normalTier(), nil),
		}
		cacheRates := map[string]CacheRateWindow{
			"a-below":   crWindow(90, 99_999),
			"b-at-gate": crWindow(50, 100_000),
		}
		decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
		crAssertOrder(t, decision, "b-at-gate", "a-below")
		details := crDetailByAccount(t, decision)
		if details["a-below"].CacheQuantum != -1 || details["b-at-gate"].CacheQuantum != 2 {
			t.Fatalf("quantums = %+v", details)
		}
		// Section 5.6: a sub-gate sample still carrying cache_read (input
		// 99_999 < 100_000) is "no valid data", so the materialized key leaves
		// cacheRate nil and the explanation detail omits CacheHitRate (the
		// detail fields project orderKey verbatim).
		if details["a-below"].CacheHitRate != nil {
			t.Fatalf("sub-gate rate = %v, want nil", details["a-below"].CacheHitRate)
		}
	})

	t.Run("cache read above input is void", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("void", normalTier(), nil),
			hqCandidate("honest", normalTier(), nil),
		}
		cacheRates := map[string]CacheRateWindow{
			"void":   {CacheReadTokens: 2_000_000, InputTokens: 1_000_000},
			"honest": crWindow(50, 1_000_000),
		}
		decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
		crAssertOrder(t, decision, "honest", "void")
		details := crDetailByAccount(t, decision)
		if details["void"].CacheHitRate != nil || details["void"].CacheQuantum != -1 {
			t.Fatalf("void detail = %+v", details["void"])
		}

		// A layer consisting only of void data disables the key entirely.
		voidOnly := crDecide(t, HotQualityModeCostFirst, candidates[:1], cacheRates, nil)
		if voidOnly.Explanation.CacheRateEnabled {
			t.Fatal("void-only layer must disable the cache key")
		}
	})

	t.Run("quantum boundaries 19 vs 21 split, 19 vs 19 tie", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("a-nine", normalTier(), crSpeed(1_000, true, nil)),
			hqCandidate("b-eleven", normalTier(), crSpeed(1_000, true, nil)),
		}
		cacheRates := map[string]CacheRateWindow{
			"a-nine":   crWindow(19, 1_000_000),
			"b-eleven": crWindow(21, 1_000_000),
		}
		// A 2pp difference across the 20pp boundary still splits tiers 0 vs 1
		// (section 5.5 boundary effect, cost_first tier width).
		crAssertOrder(t, crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil), "b-eleven", "a-nine")

		sameTier := map[string]CacheRateWindow{
			"a-nine":   crWindow(19, 1_000_000),
			"b-eleven": crWindow(19, 1_000_000),
		}
		// Same tier → equal key → AccountID tiebreak.
		crAssertOrder(t, crDecide(t, HotQualityModeCostFirst, candidates, sameTier, nil), "a-nine", "b-eleven")
	})
}

func TestDecideHotQualityCandidateEwmaUnknownAlwaysQualified(t *testing.T) {
	t.Run("ewma unknown stays qualified", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("known-poor", normalTier(), crSpeed(1_000, true, nil)),
			// Same reliability keys (warming+healthy) but no 5-minute EWMA.
			hqCandidate("sampleless-rich", normalTier(), crSpeed(0, false, nil)),
		}
		cacheRates := map[string]CacheRateWindow{
			"known-poor":      crWindow(0, 1_000_000),
			"sampleless-rich": crWindow(90, 1_000_000),
		}
		// The EWMA-unknown candidate must keep its qualified membership: with
		// equal reliability keys the cache tier then ranks it above the
		// valid-0% known account (an unqualified fallback would invert this).
		decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
		crAssertOrder(t, decision, "sampleless-rich", "known-poor")
		details := crDetailByAccount(t, decision)
		if !details["sampleless-rich"].SpeedQualified {
			t.Fatal("ewma-unknown candidate must stay qualified")
		}
	})

	t.Run("p95-only unknown stays qualified", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("known-poor", normalTier(), crSpeed(1_000, true, nil)),
			hqCandidate("p95-rich", normalTier(), crSpeed(0, false, floatPtr(4))),
		}
		cacheRates := map[string]CacheRateWindow{
			"known-poor": crWindow(0, 1_000_000),
			"p95-rich":   crWindow(90, 1_000_000),
		}
		crAssertOrder(t, crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil), "p95-rich", "known-poor")
	})
}

// TestDecideHotQualityCandidateP95Regression locks the section 5.3 P95 role:
// buckets are a same-dimension secondary under exact EWMA ties, P95-only
// candidates only compare among themselves, and a bucket index is never
// compared against or converted to EWMA milliseconds.
func TestDecideHotQualityCandidateP95Regression(t *testing.T) {
	t.Run("dual ewma tie breaks by bucket", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("bucket-five", normalTier(), crSpeed(1_000, true, floatPtr(5))),
			hqCandidate("bucket-two", normalTier(), crSpeed(1_000, true, floatPtr(2))),
		}
		crAssertOrder(t, crDecide(t, HotQualityModeCostFirst, candidates, nil, nil), "bucket-two", "bucket-five")
	})

	t.Run("tie without both buckets falls to account id", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("a-no-bucket", normalTier(), crSpeed(1_000, true, nil)),
			hqCandidate("b-bucket-two", normalTier(), crSpeed(1_000, true, floatPtr(2))),
		}
		// P95 compares only when both sides carry it; the bucket must not leak
		// into a one-sided comparison.
		crAssertOrder(t, crDecide(t, HotQualityModeCostFirst, candidates, nil, nil), "a-no-bucket", "b-bucket-two")
	})

	t.Run("p95-only candidates compare buckets among themselves", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("p95-three", normalTier(), crSpeed(0, false, floatPtr(3))),
			hqCandidate("p95-one", normalTier(), crSpeed(0, false, floatPtr(1))),
		}
		crAssertOrder(t, crDecide(t, HotQualityModeCostFirst, candidates, nil, nil), "p95-one", "p95-three")
	})

	t.Run("bucket index never competes with ewma milliseconds", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("ewma-slow", normalTier(), crSpeed(8_000, true, nil)),
			hqCandidate("p95-zero", normalTier(), crSpeed(0, false, floatPtr(0))),
		}
		// rank 0 beats rank 1 even though bucket 0 < 8000: if the bucket were
		// read as milliseconds the P95-only candidate would wrongly win.
		crAssertOrder(t, crDecide(t, HotQualityModeCostFirst, candidates, nil, nil), "ewma-slow", "p95-zero")
	})

	t.Run("ewma decides before buckets", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("fast-bucket-nine", normalTier(), crSpeed(300, true, floatPtr(9))),
			hqCandidate("slow-bucket-zero", normalTier(), crSpeed(500, true, floatPtr(0))),
		}
		crAssertOrder(t, crDecide(t, HotQualityModeCostFirst, candidates, nil, nil), "fast-bucket-nine", "slow-bucket-zero")
	})
}

func TestDecideHotQualityCandidateCacheRateExplanation(t *testing.T) {
	candidates := []HotQualityCandidatePayload{
		hqCandidate("A", normalTier(), crSpeed(1_000, true, nil)),
		hqCandidate("B", normalTier(), crSpeed(4_000, true, nil)),
		hqCandidate("C", normalTier(), crSpeed(7_000, true, nil)),
	}
	cacheRates := map[string]CacheRateWindow{
		"A": crWindow(0, 1_000_000),
		"B": crWindow(20, 1_000_000),
		"C": crWindow(40, 1_000_000),
	}
	decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, func(input *DecideHotQualityCandidateInput[HotQualityCandidatePayload]) {
		input.CacheRateStale = true
	})
	explanation := decision.Explanation
	if explanation.SpeedBaseEwmaMs == nil || *explanation.SpeedBaseEwmaMs != 1_000 {
		t.Fatalf("speed base = %v", explanation.SpeedBaseEwmaMs)
	}
	// SpeedThresholdMs echoes the baseline tier's effective window W =
	// max(3×1s, 1s) = 3s (field name unchanged, section 5.6).
	if explanation.SpeedThresholdMs != 3_000 {
		t.Fatalf("threshold = %d, want the 3s window W", explanation.SpeedThresholdMs)
	}
	if !explanation.CacheRateStale {
		t.Fatal("stale marker lost")
	}
	if !explanation.CacheRateEnabled {
		t.Fatal("cache key must be reported enabled")
	}
	// Details follow the final sort order B -> A -> C.
	details := explanation.CandidateOrderDetails
	if len(details) != 3 || details[0].AccountID != "B" || details[1].AccountID != "A" || details[2].AccountID != "C" {
		t.Fatalf("details = %+v", details)
	}
	wantQuantums := map[string]int{"A": 0, "B": 1, "C": 2}
	wantQualified := map[string]bool{"A": true, "B": true, "C": false}
	for _, detail := range details {
		if detail.CacheQuantum != wantQuantums[detail.AccountID] || detail.SpeedQualified != wantQualified[detail.AccountID] {
			t.Fatalf("detail = %+v", detail)
		}
		if detail.CacheHitRate == nil {
			t.Fatalf("detail %s missing rate", detail.AccountID)
		}
	}

	// No known EWMA anywhere → nil speed base, W undefined → the threshold
	// field stays 0 (omitted downstream); no cache data → disabled key.
	bare := crDecide(t, HotQualityModeCostFirst, []HotQualityCandidatePayload{hqCandidate("cold", normalTier(), nil)}, nil, nil)
	if bare.Explanation.SpeedBaseEwmaMs != nil || bare.Explanation.CacheRateEnabled {
		t.Fatalf("bare explanation = %+v", bare.Explanation)
	}
	if bare.Explanation.SpeedThresholdMs != 0 {
		t.Fatalf("bare threshold = %d, want 0 (field omitted without known EWMA)", bare.Explanation.SpeedThresholdMs)
	}

	// Decision-level facts describe the baseline primary's tier: the normal
	// tier (no cache data) decides, not the fallback tier with the valid
	// window.
	multiTier := []HotQualityCandidatePayload{
		hqCandidate("normal-tier", normalTier(), crSpeed(1_000, true, nil)),
		hqCandidate("fallback-tier", fallbackTier(), crSpeed(500, true, nil)),
	}
	fallbackRates := map[string]CacheRateWindow{"fallback-tier": crWindow(90, 1_000_000)}
	multi := crDecide(t, HotQualityModeCostFirst, multiTier, fallbackRates, nil)
	if multi.Explanation.BaselinePrimaryAccountID != "normal-tier" {
		t.Fatalf("baseline = %s", multi.Explanation.BaselinePrimaryAccountID)
	}
	if multi.Explanation.CacheRateEnabled {
		t.Fatal("cache key must report the baseline tier, not the fallback tier")
	}
	if multi.Explanation.SpeedBaseEwmaMs == nil || *multi.Explanation.SpeedBaseEwmaMs != 1_000 {
		t.Fatalf("multi-tier speed base = %v", multi.Explanation.SpeedBaseEwmaMs)
	}
	// The window W also comes from the baseline tier: 3s (normal tier,
	// vBase=1s), not the fallback tier's own max(3×0.5s, 1s) = 1.5s.
	if multi.Explanation.SpeedThresholdMs != 3_000 {
		t.Fatalf("multi-tier threshold = %d, want the baseline tier's 3s window", multi.Explanation.SpeedThresholdMs)
	}
}

func TestDecideHotQualityCandidateOrderOnlyPureEntry(t *testing.T) {
	wouldExplore := func() *SameTierExplorationDecisionState {
		return &SameTierExplorationDecisionState{
			Enabled:                      true,
			EligibleFirstPrimaryDispatch: true,
			Credit:                       1,
			Cursor:                       0,
			NowMs:                        10_000,
			KnownSampleStaleAfterMs:      60_000,
		}
	}
	build := func() []HotQualityCandidatePayload {
		return []HotQualityCandidatePayload{
			hqCandidate("primary", normalTier(), func(candidate *HotQualityCandidate) {
				candidate.StableBindingOrder = 0
				candidate.HotQuality = &HotQualitySelectionSnapshot{SampleState: HotQualitySampleWarming, ReliabilityLevel: HotQualityReliabilityHealthy}
			}),
			hqCandidate("cold-target", normalTier(), func(candidate *HotQualityCandidate) {
				candidate.StableBindingOrder = 1
			}),
		}
	}
	cacheRates := map[string]CacheRateWindow{"primary": crWindow(60, 1_000_000)}

	// Sanity: the full entry with the live state selects the exploration
	// target.
	full, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
		RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf,
		Candidates: build(), CacheRates: cacheRates, Exploration: wouldExplore(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if full.Explanation.Exploration.Status != ExplorationStatusSelected || full.SelectedCandidate.Payload.label != "cold-target" {
		t.Fatalf("full entry = %+v", full.Explanation.Exploration)
	}

	// The pure entry ignores the exploration state entirely: no target, no
	// panic, not_configured, baseline primary stays selected.
	pure, err := DecideHotQualityCandidateOrderOnly(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
		RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf,
		Candidates: build(), CacheRates: cacheRates, Exploration: wouldExplore(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pure.Explanation.Exploration.Status != ExplorationStatusNotConfigured {
		t.Fatalf("pure status = %s", pure.Explanation.Exploration.Status)
	}
	if pure.DispatchIntent != DispatchIntentPrimaryService || pure.Explanation.SelectionReason != SelectionReasonRankedPrimary {
		t.Fatalf("pure intent/reason = %s/%s", pure.DispatchIntent, pure.Explanation.SelectionReason)
	}
	if pure.SelectedCandidate == nil || pure.SelectedCandidate.Payload.label != "primary" {
		t.Fatalf("pure selected = %+v", pure.SelectedCandidate)
	}
	if len(pure.Explanation.DuplicateRuntimeAccountIDs) != 0 || pure.Explanation.CandidateOrderDetails == nil {
		t.Fatalf("pure explanation = %+v", pure.Explanation)
	}

	// Pure entry ordering equals the full entry without exploration, including
	// the cache-rate-aware within-tier keys.
	exploreFree, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
		RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf,
		Candidates: build(), CacheRates: cacheRates,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(crLabels(pure), ",") != strings.Join(crLabels(exploreFree), ",") {
		t.Fatalf("pure order %v != full order %v", crLabels(pure), crLabels(exploreFree))
	}

	// The nil-exploration behavior of the full entry is locked: no panic,
	// not_configured, baseline primary selected.
	nilState := exploreFree
	if nilState.Explanation.Exploration.Status != ExplorationStatusNotConfigured {
		t.Fatalf("nil-state status = %s", nilState.Explanation.Exploration.Status)
	}
}

func TestApplyCacheRateWindow(t *testing.T) {
	apply := func(window CacheRateWindow) *HotQualitySelectionSnapshot {
		snapshot := &HotQualitySelectionSnapshot{}
		applyCacheRateWindow(snapshot, window)
		return snapshot
	}
	valid := apply(CacheRateWindow{CacheReadTokens: 850_000, InputTokens: 1_000_000})
	if valid.CacheHitRate == nil || *valid.CacheHitRate != 0.85 || valid.CacheWindowInputTokens != 1_000_000 {
		t.Fatalf("valid = %+v", valid)
	}
	// cache_read > input voids the rate but keeps the raw window size.
	overrun := apply(CacheRateWindow{CacheReadTokens: 2_000_000, InputTokens: 1_000_000})
	if overrun.CacheHitRate != nil || overrun.CacheWindowInputTokens != 1_000_000 {
		t.Fatalf("overrun = %+v", overrun)
	}
	empty := apply(CacheRateWindow{})
	if empty.CacheHitRate != nil || empty.CacheWindowInputTokens != 0 {
		t.Fatalf("empty = %+v", empty)
	}
	negative := apply(CacheRateWindow{CacheReadTokens: -1, InputTokens: 100})
	if negative.CacheHitRate != nil {
		t.Fatalf("negative = %+v", negative)
	}
	// nil snapshots are a no-op (no snapshot → nothing to inject).
	applyCacheRateWindow(nil, CacheRateWindow{CacheReadTokens: 1, InputTokens: 1})
}

// TestOrderGatewayAccountsByHotQualityCacheRateInjection proves the runtime
// candidate-building loop injects the CacheRates windows by physical account
// ID: two accounts with byte-identical snapshots tie on every speed key, so
// only the injected cache tiers can flip the winner.
func TestOrderGatewayAccountsByHotQualityCacheRateInjection(t *testing.T) {
	build := func() *GatewayHotQualityRuntime {
		store := &mockHotQualityStore{t: t, snapshotOf: func(scope HotQualityScope) (*HotQualitySnapshot, error) {
			return snapshotFor(10, 0), nil
		}}
		runtime, _, _ := newTestRuntime(store, &mockExplorationStore{t: t})
		return runtime
	}
	accounts := []GatewayHotQualityAccountView{testAccount("acc-a", 0), testAccount("acc-b", 0)}
	orderOf := func(result *GatewayHotQualityCandidateOrderResult[GatewayHotQualityAccountView]) []string {
		ids := make([]string, 0, len(result.Accounts))
		for _, account := range result.Accounts {
			ids = append(ids, baseView(account).ID)
		}
		return ids
	}

	// Without CacheRates the tie falls through to the AccountID order.
	plain, err := OrderGatewayAccountsByHotQuality(context.Background(), build(), GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView]{
		Accounts: accounts, Base: baseView, Mode: HotQualityModeCostFirst,
		SystemAccountID: "sys", GroupID: "g1", RequestLane: "text",
		RequestID: "req-cache", NowMs: int64Ptr(1_000_000),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(orderOf(plain), ",") != "acc-a,acc-b" {
		t.Fatalf("plain order = %v", orderOf(plain))
	}

	// With windows injected, the 90% tier outranks the 10% tier (the layer
	// has no known EWMA at all, so every candidate is speed-qualified and
	// only the cache tiers can separate them).
	injected, err := OrderGatewayAccountsByHotQuality(context.Background(), build(), GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView]{
		Accounts: accounts, Base: baseView, Mode: HotQualityModeCostFirst,
		SystemAccountID: "sys", GroupID: "g1", RequestLane: "text",
		RequestID: "req-cache", NowMs: int64Ptr(1_000_000),
		CacheRates: map[string]CacheRateWindow{
			"acc-a": crWindow(10, 1_000_000),
			"acc-b": crWindow(90, 1_000_000),
		},
		CacheRateStale: false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(orderOf(injected), ",") != "acc-b,acc-a" {
		t.Fatalf("injected order = %v", orderOf(injected))
	}
	if len(injected.QualityReorderedTierKeys) == 0 {
		t.Fatalf("reordered tiers = %v", injected.QualityReorderedTierKeys)
	}
}

// TestDecideHotQualityCandidateRelativeSpeedWindow locks the relative
// speed-qualification contract (section 5.4): W = max(k × vBase,
// SpeedQualificationFloorMs) with an explicit override winning outright, and
// the absolute ceiling (absCap) applied independently on top of EWMA-known
// candidates only.
func TestDecideHotQualityCandidateRelativeSpeedWindow(t *testing.T) {
	t.Run("k boundary: diff exactly k*base qualifies, one ms more does not", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("base", normalTier(), crSpeed(1_000, true, nil)),
			hqCandidate("edge-in", normalTier(), crSpeed(4_000, true, nil)),
			hqCandidate("edge-out", normalTier(), crSpeed(4_001, true, nil)),
		}
		cacheRates := map[string]CacheRateWindow{
			"base":     crWindow(0, 1_000_000),
			"edge-in":  crWindow(90, 1_000_000),
			"edge-out": crWindow(90, 1_000_000),
		}
		// cost_first W = max(3×1s, 1s) = 3s: edge-in (diff exactly 3s,
		// 4s <= 10s ceiling) stays qualified and outranks the 0% base by cache
		// tier; edge-out misses the window by the smallest millisecond step
		// and is judged by the speed key instead.
		decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
		crAssertOrder(t, decision, "edge-in", "base", "edge-out")
		details := crDetailByAccount(t, decision)
		if !details["edge-in"].SpeedQualified || details["edge-out"].SpeedQualified {
			t.Fatalf("qualified flags = %+v", details)
		}
		if details["edge-in"].CacheQuantum != 4 {
			t.Fatalf("edge-in quantum = %d", details["edge-in"].CacheQuantum)
		}
	})

	t.Run("floor lifts the window when k*base falls below it", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("base", normalTier(), crSpeed(300, true, nil)),
			hqCandidate("edge-in", normalTier(), crSpeed(1_300, true, nil)),
			hqCandidate("edge-out", normalTier(), crSpeed(1_301, true, nil)),
		}
		cacheRates := map[string]CacheRateWindow{
			"base":     crWindow(0, 1_000_000),
			"edge-in":  crWindow(90, 1_000_000),
			"edge-out": crWindow(90, 1_000_000),
		}
		// cost_first k×vBase = 900ms < floor: W is lifted to 1s, so diff 1s
		// qualifies and diff 1s+1ms does not.
		decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
		crAssertOrder(t, decision, "edge-in", "base", "edge-out")
		if decision.Explanation.SpeedThresholdMs != SpeedQualificationFloorMs {
			t.Fatalf("threshold = %d, want the 1s floor", decision.Explanation.SpeedThresholdMs)
		}
		details := crDetailByAccount(t, decision)
		if !details["edge-in"].SpeedQualified || details["edge-out"].SpeedQualified {
			t.Fatalf("qualified flags = %+v", details)
		}
	})

	t.Run("absolute ceiling converges slow tiers", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("base", normalTier(), crSpeed(8_000, true, nil)),
			hqCandidate("within-ceiling", normalTier(), crSpeed(9_000, true, nil)),
			hqCandidate("beyond-ceiling", normalTier(), crSpeed(11_000, true, nil)),
		}
		cacheRates := map[string]CacheRateWindow{
			"base":           crWindow(0, 1_000_000),
			"within-ceiling": crWindow(90, 1_000_000),
			"beyond-ceiling": crWindow(90, 1_000_000),
		}
		// cost_first W = max(3×8s, 1s) = 24s: the 9s candidate (diff 1s <= W,
		// 9s <= 10s ceiling) qualifies while the 11s candidate (diff 3s <= W
		// but 11s > 10s) stays unqualified — the ceiling binds even when the
		// window is wide enough.
		decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
		crAssertOrder(t, decision, "within-ceiling", "base", "beyond-ceiling")
		if decision.Explanation.SpeedThresholdMs != 24_000 {
			t.Fatalf("threshold = %d, want the 24s window", decision.Explanation.SpeedThresholdMs)
		}
		details := crDetailByAccount(t, decision)
		if !details["within-ceiling"].SpeedQualified || details["beyond-ceiling"].SpeedQualified {
			t.Fatalf("qualified flags = %+v", details)
		}
	})

	t.Run("absolute ceiling boundary: EWMA exactly at the ceiling qualifies", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("base", normalTier(), crSpeed(4_000, true, nil)),
			hqCandidate("at-ceiling", normalTier(), crSpeed(10_000, true, nil)),
			hqCandidate("beyond-ceiling", normalTier(), crSpeed(10_001, true, nil)),
		}
		cacheRates := map[string]CacheRateWindow{
			"base":           crWindow(0, 1_000_000),
			"at-ceiling":     crWindow(90, 1_000_000),
			"beyond-ceiling": crWindow(90, 1_000_000),
		}
		// cost_first W = max(3×4s, 1s) = 12s: the 10s candidate sits exactly on
		// the ceiling (<= binds) with diff 6s <= W → qualified and outranks the
		// 0% base by cache tier; 10.001s misses the ceiling by 1ms → judged by
		// the speed key behind the qualified set.
		decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
		crAssertOrder(t, decision, "at-ceiling", "base", "beyond-ceiling")
		details := crDetailByAccount(t, decision)
		if !details["at-ceiling"].SpeedQualified || details["beyond-ceiling"].SpeedQualified {
			t.Fatalf("qualified flags = %+v", details)
		}
	})

	t.Run("absolute ceiling keeps fast layers no looser", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("base", normalTier(), crSpeed(1_500, true, nil)),
			hqCandidate("slow-rich", normalTier(), crSpeed(10_500, true, nil)),
		}
		cacheRates := map[string]CacheRateWindow{
			"base":      crWindow(0, 1_000_000),
			"slow-rich": crWindow(90, 1_000_000),
		}
		// Default W = max(3×1.5s, 1s) = 4.5s: diff 9s already misses the
		// window (and 10.5s > 10s ceiling) → unqualified.
		decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
		crAssertOrder(t, decision, "base", "slow-rich")
		// Widening the window via override must not relax the ceiling:
		// diff 9s <= 20s now, but 10.5s > 10s keeps the candidate out.
		override := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, func(input *DecideHotQualityCandidateInput[HotQualityCandidatePayload]) {
			input.SpeedThresholdMs = 20_000
		})
		crAssertOrder(t, override, "base", "slow-rich")
		if override.Explanation.SpeedThresholdMs != 20_000 {
			t.Fatalf("override threshold = %d", override.Explanation.SpeedThresholdMs)
		}
		details := crDetailByAccount(t, override)
		if details["slow-rich"].SpeedQualified {
			t.Fatalf("qualified flags = %+v", details)
		}
	})

	t.Run("override fixes the window while the ceiling still applies", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("base", normalTier(), crSpeed(1_500, true, nil)),
			hqCandidate("mid-rich", normalTier(), crSpeed(4_000, true, nil)),
			hqCandidate("slow-rich", normalTier(), crSpeed(12_000, true, nil)),
		}
		cacheRates := map[string]CacheRateWindow{
			"base":      crWindow(0, 1_000_000),
			"mid-rich":  crWindow(90, 1_000_000),
			"slow-rich": crWindow(90, 1_000_000),
		}
		// Default W = 4.5s: mid-rich (diff 2.5s) qualified, slow-rich beyond
		// the ceiling → mid-rich first.
		decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
		crAssertOrder(t, decision, "mid-rich", "base", "slow-rich")
		// Override 2s shrinks the window: mid-rich (diff 2.5s > 2s) drops out
		// and the speed key ranks the unqualified pair.
		shrunk := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, func(input *DecideHotQualityCandidateInput[HotQualityCandidatePayload]) {
			input.SpeedThresholdMs = 2_000
		})
		crAssertOrder(t, shrunk, "base", "mid-rich", "slow-rich")
		if shrunk.Explanation.SpeedThresholdMs != 2_000 {
			t.Fatalf("shrunk threshold = %d", shrunk.Explanation.SpeedThresholdMs)
		}
		// Override 20s widens the window past slow-rich's diff (10.5s <= 20s),
		// but the 10s ceiling still rejects it — the override never disables
		// the absolute cap.
		widened := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, func(input *DecideHotQualityCandidateInput[HotQualityCandidatePayload]) {
			input.SpeedThresholdMs = 20_000
		})
		crAssertOrder(t, widened, "mid-rich", "base", "slow-rich")
		if widened.Explanation.SpeedThresholdMs != 20_000 {
			t.Fatalf("widened threshold = %d", widened.Explanation.SpeedThresholdMs)
		}
		details := crDetailByAccount(t, widened)
		if !details["mid-rich"].SpeedQualified || details["slow-rich"].SpeedQualified {
			t.Fatalf("qualified flags = %+v", details)
		}
	})
}

// TestDecideHotQualityCandidateSpeedFirstParameterSet proves the speed_first
// parameter package (k=1, ceiling 5s, 10pp tier width) takes effect
// independently of cost_first.
func TestDecideHotQualityCandidateSpeedFirstParameterSet(t *testing.T) {
	t.Run("k=1 window is tighter than cost_first k=3", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("base", normalTier(), crSpeed(1_000, true, nil)),
			hqCandidate("nearby-rich", normalTier(), crSpeed(3_500, true, nil)),
		}
		cacheRates := map[string]CacheRateWindow{
			"base":        crWindow(0, 1_000_000),
			"nearby-rich": crWindow(90, 1_000_000),
		}
		// cost_first W = 3s: diff 2.5s <= W → qualified → cache tier wins.
		crAssertOrder(t, crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil), "nearby-rich", "base")
		// speed_first W = max(1×1s, 1s) = 1s: diff 2.5s > W → unqualified →
		// speed key decides.
		speedDecision := crDecide(t, HotQualityModeSpeedFirst, candidates, cacheRates, nil)
		crAssertOrder(t, speedDecision, "base", "nearby-rich")
		if speedDecision.Explanation.SpeedThresholdMs != 1_000 {
			t.Fatalf("speed threshold = %d", speedDecision.Explanation.SpeedThresholdMs)
		}
	})

	t.Run("5s ceiling applies on top of a wide override", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("base", normalTier(), crSpeed(2_000, true, nil)),
			hqCandidate("slow-rich", normalTier(), crSpeed(5_500, true, nil)),
		}
		cacheRates := map[string]CacheRateWindow{
			"base":      crWindow(0, 1_000_000),
			"slow-rich": crWindow(90, 1_000_000),
		}
		mutate := func(input *DecideHotQualityCandidateInput[HotQualityCandidatePayload]) {
			input.SpeedThresholdMs = 10_000
		}
		// cost_first ceiling 10s: 5.5s <= 10s → qualified with the same
		// override.
		crAssertOrder(t, crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, mutate), "slow-rich", "base")
		// speed_first ceiling 5s: 5.5s > 5s → unqualified despite diff 3.5s
		// <= override window 10s.
		speedDecision := crDecide(t, HotQualityModeSpeedFirst, candidates, cacheRates, mutate)
		crAssertOrder(t, speedDecision, "base", "slow-rich")
		if speedDecision.Explanation.SpeedThresholdMs != 10_000 {
			t.Fatalf("speed threshold = %d", speedDecision.Explanation.SpeedThresholdMs)
		}
		details := crDetailByAccount(t, speedDecision)
		if details["slow-rich"].SpeedQualified {
			t.Fatalf("qualified flags = %+v", details)
		}
	})

	t.Run("10pp tier width splits where cost_first ties", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("a-fifteen", normalTier(), crSpeed(1_000, true, nil)),
			hqCandidate("b-five", normalTier(), crSpeed(1_000, true, nil)),
		}
		cacheRates := map[string]CacheRateWindow{
			"a-fifteen": crWindow(15, 1_000_000),
			"b-five":    crWindow(5, 1_000_000),
		}
		// speed_first 10pp tiers: 15% → q=1 beats 5% → q=0.
		speedDecision := crDecide(t, HotQualityModeSpeedFirst, candidates, cacheRates, nil)
		crAssertOrder(t, speedDecision, "a-fifteen", "b-five")
		if details := crDetailByAccount(t, speedDecision); details["a-fifteen"].CacheQuantum != 1 || details["b-five"].CacheQuantum != 0 {
			t.Fatalf("speed_first quantums = %+v", details)
		}
		// cost_first 20pp tiers: both floor to q=0 → AccountID tiebreak
		// keeps the same order for a different reason.
		costDecision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
		crAssertOrder(t, costDecision, "a-fifteen", "b-five")
		if details := crDetailByAccount(t, costDecision); details["a-fifteen"].CacheQuantum != 0 || details["b-five"].CacheQuantum != 0 {
			t.Fatalf("cost_first quantums = %+v", details)
		}
	})
}

// TestDecideHotQualityCandidateCacheQuantumModeTierWidth locks the 20pp
// cost_first tier-width boundaries at the ordering level (19% → q=0,
// 20% → q=1, 21% → q=1).
func TestDecideHotQualityCandidateCacheQuantumModeTierWidth(t *testing.T) {
	candidates := []HotQualityCandidatePayload{
		hqCandidate("a-nineteen", normalTier(), crSpeed(1_000, true, nil)),
		hqCandidate("b-twenty", normalTier(), crSpeed(1_000, true, nil)),
		hqCandidate("c-twentyone", normalTier(), crSpeed(1_000, true, nil)),
	}
	cacheRates := map[string]CacheRateWindow{
		"a-nineteen":  crWindow(19, 1_000_000),
		"b-twenty":    crWindow(20, 1_000_000),
		"c-twentyone": crWindow(21, 1_000_000),
	}
	// b-twenty and c-twentyone share q=1 (20% and 21% both floor to tier 1)
	// and split by AccountID; a-nineteen (q=0) trails both.
	decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
	crAssertOrder(t, decision, "b-twenty", "c-twentyone", "a-nineteen")
	details := crDetailByAccount(t, decision)
	if details["a-nineteen"].CacheQuantum != 0 || details["b-twenty"].CacheQuantum != 1 || details["c-twentyone"].CacheQuantum != 1 {
		t.Fatalf("quantums = %+v", details)
	}
}

