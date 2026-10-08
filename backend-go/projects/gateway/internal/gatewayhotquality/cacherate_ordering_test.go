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
	// Exact tier-boundary rates belong to their own tier (section 5.5).
	for k := 0; k <= 10; k++ {
		rate := float64(k) / 10
		if got := cacheQuantumOf(rate); got != k {
			t.Fatalf("cacheQuantumOf(%.2f) = %d, want %d", rate, got, k)
		}
	}
	// Off-boundary rates floor normally.
	cases := []struct {
		rate float64
		want int
	}{{0.0, 0}, {0.09, 0}, {0.11, 1}, {0.19, 1}, {0.99, 9}, {0.999, 9}}
	for _, testCase := range cases {
		if got := cacheQuantumOf(testCase.rate); got != testCase.want {
			t.Fatalf("cacheQuantumOf(%v) = %d, want %d", testCase.rate, got, testCase.want)
		}
	}
}

// TestDecideHotQualityCandidateCacheRateLoopSample locks the section 5.4
// verdict: A(1s/0%) B(7s/20%) C(13s/40%) under cost_first (10s) resolves to
// B -> A -> C for every input permutation — the pairwise dead-zone cycle
// (B>A, C>B, A>C) must never leak into the result.
func TestDecideHotQualityCandidateCacheRateLoopSample(t *testing.T) {
	cacheRates := map[string]CacheRateWindow{
		"A": crWindow(0, 1_000_000),
		"B": crWindow(20, 1_000_000),
		"C": crWindow(40, 1_000_000),
	}
	build := func() []HotQualityCandidatePayload {
		return []HotQualityCandidatePayload{
			hqCandidate("A", normalTier(), crSpeed(1_000, true, nil)),
			hqCandidate("B", normalTier(), crSpeed(7_000, true, nil)),
			hqCandidate("C", normalTier(), crSpeed(13_000, true, nil)),
		}
	}
	// All 6 input permutations (index triples over A,B,C).
	for _, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		source := build()
		permuted := []HotQualityCandidatePayload{source[order[0]], source[order[1]], source[order[2]]}
		decision := crDecide(t, HotQualityModeCostFirst, permuted, cacheRates, nil)
		crAssertOrder(t, decision, "B", "A", "C")

		speedDecision := crDecide(t, HotQualityModeSpeedFirst, permuted, cacheRates, nil)
		// speed_first (5s): A qualified (diff 0); B (diff 6s) and C (12s)
		// unqualified. The unqualified pair skips the cache tier and is
		// decided by the materialized speed key, so the faster B keeps the
		// lead over the richer-cache C (speed dominance hard rule).
		crAssertOrder(t, speedDecision, "A", "B", "C")
	}

	// The same windows carried on the assembly-time snapshots (instead of the
	// decision-input map) must produce identical orders.
	snapshotCarried := build()
	snapshotRates := map[string]struct {
		rate      float64
		rateKnown bool
	}{"A": {0, true}, "B": {0.20, true}, "C": {0.40, true}}
	ewmaByLabel := map[string]float64{"A": 1_000, "B": 7_000, "C": 13_000}
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
// permutation, in both modes.
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
	shuffler := rand.New(rand.NewSource(20261008))
	for _, mode := range []HotQualityRoutingMode{HotQualityModeCostFirst, HotQualityModeSpeedFirst} {
		baseline := crDecide(t, mode, build(), cacheRates, nil)
		want := strings.Join(crLabels(baseline), ",")
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
		// cost_first: slow-rich diff 19s > 10s → unqualified despite q=9.
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
			hqCandidate("edge-in", normalTier(), crSpeed(10_000, true, nil)),      // diff 9s → qualified, q=0
			hqCandidate("edge-out", normalTier(), crSpeed(12_000, true, nil)),     // diff 11s → unqualified, q=9
		}
		cacheRates := map[string]CacheRateWindow{
			"baseline-fast": crWindow(50, 1_000_000),
			"edge-in":       crWindow(0, 1_000_000),
			"edge-out":      crWindow(90, 1_000_000),
		}
		// Key 3 separates edge-out from the qualified pair before any cache
		// comparison: the richer and merely-2s-slower candidate outside the
		// qualified set must not outrank edge-in. Inside the qualified set the
		// cache tier ranks baseline-fast (q=5) over edge-in (q=0).
		decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
		crAssertOrder(t, decision, "baseline-fast", "edge-in", "edge-out")
	})

	t.Run("both unqualified skip cache tier and compare speed", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("baseline-fast", normalTier(), crSpeed(1_000, true, nil)), // qualified, pins vBase
			hqCandidate("out-poor", normalTier(), crSpeed(20_000, true, nil)),     // diff 19s → unqualified, q=1
			hqCandidate("out-rich", normalTier(), crSpeed(30_000, true, nil)),     // diff 29s → unqualified, q=9
		}
		cacheRates := map[string]CacheRateWindow{
			"baseline-fast": crWindow(50, 1_000_000),
			"out-poor":      crWindow(10, 1_000_000),
			"out-rich":      crWindow(90, 1_000_000),
		}
		// Within the unqualified pair the cache tier is skipped entirely: the
		// EWMA-ascending speed key decides, so the 10%-cache but faster
		// candidate outranks the 90%-cache slower one. Speed beyond the
		// threshold is the judge, never the cache tier.
		decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
		crAssertOrder(t, decision, "baseline-fast", "out-poor", "out-rich")
		details := crDetailByAccount(t, decision)
		if details["out-poor"].SpeedQualified || details["out-rich"].SpeedQualified {
			t.Fatalf("qualified flags = %+v", details)
		}
		if details["out-poor"].CacheQuantum != 1 || details["out-rich"].CacheQuantum != 9 {
			t.Fatalf("quantums = %+v", details)
		}
	})

	t.Run("cache quantum descending inside qualified set", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("fast-poor", normalTier(), crSpeed(1_000, true, nil)),
			hqCandidate("nearby-rich", normalTier(), crSpeed(5_000, true, nil)),
		}
		cacheRates := map[string]CacheRateWindow{
			"fast-poor":   crWindow(0, 1_000_000),
			"nearby-rich": crWindow(90, 1_000_000),
		}
		// Both qualified (5s <= 10s); the richer cache tier wins over raw EWMA.
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
		hqCandidate("nearby-rich", normalTier(), crSpeed(7_000, true, nil)),
	}
	cacheRates := map[string]CacheRateWindow{
		"fast-poor":   crWindow(0, 1_000_000),
		"nearby-rich": crWindow(90, 1_000_000),
	}
	// cost_first default (10s): 6s diff qualified → cache tier wins.
	decision := crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil)
	crAssertOrder(t, decision, "nearby-rich", "fast-poor")
	if decision.Explanation.SpeedThresholdMs != SpeedDominanceThresholdCostFirstMs {
		t.Fatalf("threshold = %d", decision.Explanation.SpeedThresholdMs)
	}
	// speed_first default (5s): 6s diff unqualified → the fast candidate leads.
	speedDecision := crDecide(t, HotQualityModeSpeedFirst, candidates, cacheRates, nil)
	crAssertOrder(t, speedDecision, "fast-poor", "nearby-rich")
	if speedDecision.Explanation.SpeedThresholdMs != SpeedDominanceThresholdSpeedFirstMs {
		t.Fatalf("speed threshold = %d", speedDecision.Explanation.SpeedThresholdMs)
	}
	// Explicit override wins over the mode default.
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
			hqCandidate("sub-gate-rich", normalTier(), crSpeed(5_000, true, nil)),
			hqCandidate("fast-clean", normalTier(), crSpeed(1_000, true, nil)),
		}
		// The only cache data is below the sample gate: the layer disables the
		// cache key and pure EWMA ordering stands.
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
		// winner to the 99% cache tier (both stay qualified: 4s <= 10s).
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
		if details["rich"].CacheQuantum != 4 || details["mid"].CacheQuantum != 2 || details["no-data"].CacheQuantum != -1 {
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
		if details["a-below"].CacheQuantum != -1 || details["b-at-gate"].CacheQuantum != 5 {
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

	t.Run("quantum boundaries 9 vs 11 split, 10 vs 10 tie", func(t *testing.T) {
		candidates := []HotQualityCandidatePayload{
			hqCandidate("a-nine", normalTier(), crSpeed(1_000, true, nil)),
			hqCandidate("b-eleven", normalTier(), crSpeed(1_000, true, nil)),
		}
		cacheRates := map[string]CacheRateWindow{
			"a-nine":   crWindow(9, 1_000_000),
			"b-eleven": crWindow(11, 1_000_000),
		}
		// A 2pp difference still splits tiers 0 vs 1 (section 5.5 boundary
		// effect).
		crAssertOrder(t, crDecide(t, HotQualityModeCostFirst, candidates, cacheRates, nil), "b-eleven", "a-nine")

		sameTier := map[string]CacheRateWindow{
			"a-nine":   crWindow(10, 1_000_000),
			"b-eleven": crWindow(10, 1_000_000),
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
		hqCandidate("B", normalTier(), crSpeed(7_000, true, nil)),
		hqCandidate("C", normalTier(), crSpeed(13_000, true, nil)),
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
	if explanation.SpeedThresholdMs != SpeedDominanceThresholdCostFirstMs {
		t.Fatalf("threshold = %d", explanation.SpeedThresholdMs)
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
	wantQuantums := map[string]int{"A": 0, "B": 2, "C": 4}
	wantQualified := map[string]bool{"A": true, "B": true, "C": false}
	for _, detail := range details {
		if detail.CacheQuantum != wantQuantums[detail.AccountID] || detail.SpeedQualified != wantQualified[detail.AccountID] {
			t.Fatalf("detail = %+v", detail)
		}
		if detail.CacheHitRate == nil {
			t.Fatalf("detail %s missing rate", detail.AccountID)
		}
	}

	// No known EWMA anywhere → nil speed base; no cache data → disabled key.
	bare := crDecide(t, HotQualityModeCostFirst, []HotQualityCandidatePayload{hqCandidate("cold", normalTier(), nil)}, nil, nil)
	if bare.Explanation.SpeedBaseEwmaMs != nil || bare.Explanation.CacheRateEnabled {
		t.Fatalf("bare explanation = %+v", bare.Explanation)
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

	// With windows injected, the 90% tier outranks the 10% tier (both
	// qualified: identical speed signals, diff 0 <= threshold).
	injected, err := OrderGatewayAccountsByHotQuality(context.Background(), build(), GatewayHotQualityCandidateOrderInput[GatewayHotQualityAccountView]{
		Accounts: accounts, Base: baseView, Mode: HotQualityModeCostFirst,
		SystemAccountID: "sys", GroupID: "g1", RequestLane: "text",
		RequestID: "req-cache", NowMs: int64Ptr(1_000_000),
		CacheRates: map[string]CacheRateWindow{
			"acc-a": crWindow(10, 1_000_000),
			"acc-b": crWindow(90, 1_000_000),
		},
		CacheRateStale:   false,
		SpeedThresholdMs: SpeedDominanceThresholdCostFirstMs,
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
