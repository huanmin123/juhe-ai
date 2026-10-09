package gatewayhotquality

import "math"

// Cache-rate-aware within-tier ordering keys (design doc
// docs/functions/缓存率感知调度与用量缓存率展示设计.md sections 5/6/10).
//
// The comparator must satisfy a strict weak ordering, so "speed close to the
// peer" and "cache rates close to each other" are lifted from pairwise
// relations to global scalar keys materialized once per tier before sorting:
// never read the opposite candidate or apply relative thresholds inside the
// sort callback (section 5.2).

// Built-in constants (section 10; changing any of them is a behavior-contract
// change: update the design doc first, code in the same delivery).
const (
	// SpeedQualificationRatioCostFirst is the speed-window ratio k for
	// cost_first mode: the qualified window is max(k × smallest known in-tier
	// EWMA, SpeedQualificationFloorMs) unless an explicit override applies.
	SpeedQualificationRatioCostFirst = 3.0
	// SpeedAbsoluteCeilingCostFirstMs is the absolute EWMA ceiling (absCap) for
	// cost_first mode: a candidate with a known EWMA above it is never
	// speed-qualified, regardless of the window.
	SpeedAbsoluteCeilingCostFirstMs int64 = 10_000
	// CacheRateQuantumCostFirst is the cache-rate quantization step for
	// cost_first mode (20 percentage points per tier, section 5.5).
	CacheRateQuantumCostFirst = 0.20
	// SpeedQualificationRatioSpeedFirst is the speed-window ratio k for
	// speed_first mode.
	SpeedQualificationRatioSpeedFirst = 1.0
	// SpeedAbsoluteCeilingSpeedFirstMs is the absolute EWMA ceiling for
	// speed_first mode.
	SpeedAbsoluteCeilingSpeedFirstMs int64 = 5_000
	// CacheRateQuantumSpeedFirst is the cache-rate quantization step for
	// speed_first mode (10 percentage points per tier, section 5.5).
	CacheRateQuantumSpeedFirst = 0.10
	// SpeedQualificationFloorMs is the shared window lower bound: when
	// k × vBase falls below it, the window is lifted to this floor.
	SpeedQualificationFloorMs int64 = 1_000
	// CacheMinSampleInputTokens is the rolling-window input_tokens gate below
	// which a candidate has no valid cache-rate data.
	CacheMinSampleInputTokens int64 = 100_000
)

// Highest quantization tiers, pinning the contract values of floor(1 / tier
// width): cost_first floor(1/0.20) = 5, speed_first floor(1/0.10) = 10.
// Explicit integer constants instead of a float division — the double result
// of 1.0/0.20 (4.999…) would floor down to 4.
const (
	cacheQuantumUpperBoundCostFirst  = 5
	cacheQuantumUpperBoundSpeedFirst = 10
)

// speedQualificationParams carries one routing mode's speed-qualification and
// cache-quantization parameters; they resolve once per dispatch decision and
// stay constant across every tier of that decision.
type speedQualificationParams struct {
	// ratio is the window multiplier k (section 5.4: W = max(k × vBase,
	// SpeedQualificationFloorMs)).
	ratio float64
	// ceilingMs is the absolute EWMA ceiling (absCap) applied on top of the
	// window to EWMA-known candidates only.
	ceilingMs int64
	// cacheQuantum is the cache-rate tier width; cacheQuantumUpperBound is its
	// clamp ceiling floor(1 / cacheQuantum).
	cacheQuantum           float64
	cacheQuantumUpperBound int
}

// speedQualificationParamsForMode resolves the mode-backed parameter package
// once per dispatch decision (section 5.4/5.5).
func speedQualificationParamsForMode(mode HotQualityRoutingMode) speedQualificationParams {
	if mode == HotQualityModeSpeedFirst {
		return speedQualificationParams{
			ratio:                  SpeedQualificationRatioSpeedFirst,
			ceilingMs:              SpeedAbsoluteCeilingSpeedFirstMs,
			cacheQuantum:           CacheRateQuantumSpeedFirst,
			cacheQuantumUpperBound: cacheQuantumUpperBoundSpeedFirst,
		}
	}
	return speedQualificationParams{
		ratio:                  SpeedQualificationRatioCostFirst,
		ceilingMs:              SpeedAbsoluteCeilingCostFirstMs,
		cacheQuantum:           CacheRateQuantumCostFirst,
		cacheQuantumUpperBound: cacheQuantumUpperBoundCostFirst,
	}
}

// CacheRateWindow is one physical account's rolling 24h cache-read window
// (sections 8.1/8.2): cache-read tokens over total input tokens.
type CacheRateWindow struct {
	CacheReadTokens int64
	InputTokens     int64
}

// tierOrderKey carries the per-candidate scalar ordering keys materialized
// once per tier before sort.SliceStable runs (section 5.2 keys 3/4a/4b).
type tierOrderKey struct {
	// speedQualified: membership in the speed-qualified set (section 5.3).
	speedQualified bool
	// cacheQuantumEnabled: the cache-rate tier key is enabled for this layer
	// (at least one candidate carries a valid cache sample). Constant across
	// the tier; the comparator skips the dimension when false.
	cacheQuantumEnabled bool
	// cacheQuantum: floor(rate / mode tier width) clamped to 0..upper bound,
	// or -1 when the candidate itself has no valid cache data. The -1 sentinel
	// sorts after valid quanta and never means 0%.
	cacheQuantum int
	// cacheRate: the effective window rate for explanation reporting; nil when
	// the candidate has no valid cache data.
	cacheRate *float64
	// speedSignalRank: EWMA known = 0, P95-only = 1, no speed signal = 2.
	speedSignalRank int
	// ewmaMs is valid when speedSignalRank == 0.
	ewmaMs float64
	// p95Known / p95Bucket: the P95 bucket index is only ever compared against
	// another P95 bucket index; never converted to milliseconds and never
	// subtracted from thresholds.
	p95Known  bool
	p95Bucket float64
}

// tierOrderMaterialization reports the per-tier facts the decision explanation
// consumes (section 5.6). Decision-level fields reflect the tier of the
// baseline primary candidate.
type tierOrderMaterialization struct {
	// BaseEwmaMs is the smallest known in-tier EWMA (the speed base); nil when
	// no candidate has a known EWMA.
	BaseEwmaMs *float64
	// SpeedWindowMs is the tier's effective speed-qualification window W
	// (section 5.4): an explicit override wins, otherwise
	// max(k × BaseEwmaMs, SpeedQualificationFloorMs). 0 when the tier has no
	// known EWMA at all — the window is undefined and the explanation omits
	// the field.
	SpeedWindowMs int64
	// CacheRateEnabled reports whether at least one candidate carries a valid
	// cache sample, i.e. the cache-rate tier key participates in ordering.
	CacheRateEnabled bool
}

// applyCacheRateWindow injects a 24h window into an assembly-time selection
// snapshot (section 8.1): CacheWindowInputTokens keeps the raw window size
// while CacheHitRate is computed only when arithmetically well-formed
// (input > 0 and cache_read <= input). Validity gates that belong to ordering
// (the CacheMinSampleInputTokens sample gate) stay in the key materialization.
func applyCacheRateWindow(snapshot *HotQualitySelectionSnapshot, window CacheRateWindow) {
	if snapshot == nil {
		return
	}
	snapshot.CacheWindowInputTokens = window.InputTokens
	if window.InputTokens > 0 && window.CacheReadTokens >= 0 && window.CacheReadTokens <= window.InputTokens {
		rate := float64(window.CacheReadTokens) / float64(window.InputTokens)
		snapshot.CacheHitRate = &rate
	}
}

// materializeTierOrderKeys computes the scalar ordering keys for every
// candidate of one tier before sorting (section 5.2) and reports the tier
// facts used by the explanation. cacheRates maps physical account IDs to
// their 24h windows and is authoritative per account; accounts it does not
// cover fall back to the snapshot fields injected at assembly time. params
// and windowOverrideMs are decision-wide constants: the window W resolves
// once per tier as max(k × smallest known EWMA, floor) with the override
// winning outright (section 5.4), and the qualified verdict folds the
// absolute ceiling on top — both are per-candidate scalar booleans by the
// time the comparator runs.
func materializeTierOrderKeys[T any](
	tier []*indexedCandidate[T],
	cacheRates map[string]CacheRateWindow,
	params speedQualificationParams,
	windowOverrideMs int64,
) tierOrderMaterialization {
	result := tierOrderMaterialization{}
	hasKnownEwma := false
	baseEwma := 0.0
	for _, candidate := range tier {
		if ewma, ok := ewmaMsOf(candidate); ok {
			if !hasKnownEwma || ewma < baseEwma {
				baseEwma = ewma
				hasKnownEwma = true
			}
		}
	}
	windowMs := 0.0
	if hasKnownEwma {
		result.BaseEwmaMs = &baseEwma
		if windowOverrideMs > 0 {
			windowMs = float64(windowOverrideMs)
		} else {
			windowMs = math.Max(params.ratio*baseEwma, float64(SpeedQualificationFloorMs))
		}
		result.SpeedWindowMs = int64(windowMs)
	}
	for _, candidate := range tier {
		if cacheSampleValid(resolvedCacheWindow(candidate, cacheRates)) {
			result.CacheRateEnabled = true
			break
		}
	}

	ceilingMs := float64(params.ceilingMs)
	for _, candidate := range tier {
		ewma, ewmaKnown := ewmaMsOf(candidate)
		p95, p95Known := p95BucketOf(candidate)
		key := tierOrderKey{
			speedQualified:      true,
			cacheQuantumEnabled: result.CacheRateEnabled,
			cacheQuantum:        -1,
			ewmaMs:              ewma,
			p95Known:            p95Known,
			p95Bucket:           p95,
		}
		// Key 3: speed-qualified set (section 5.3). No known in-tier EWMA or
		// unknown candidate EWMA both mean qualified: speed-cold accounts stay
		// neutral instead of being punished for missing data (the absolute
		// ceiling only ever binds EWMA-known candidates). A known candidate
		// qualifies iff it sits within the tier window W and at or below the
		// absolute ceiling.
		if hasKnownEwma && ewmaKnown {
			key.speedQualified = ewma-baseEwma <= windowMs && ewma <= ceilingMs
		}
		// Key 4a: cache-rate quantum. Candidate data that fails the sample
		// gate counts as "no valid data" (section 5.6): the -1 sentinel stays
		// and the explanation rate stays nil; a fully disabled layer never
		// compares the dimension. Sorting is unaffected — the comparator only
		// reads cacheQuantum, and the gate already forced -1 here.
		if rate, windowInputTokens := resolvedCacheWindow(candidate, cacheRates); rate != nil && windowInputTokens >= CacheMinSampleInputTokens {
			key.cacheRate = rate
			key.cacheQuantum = cacheQuantumOf(params, *rate)
		}
		// Key 4b: materialized speed-signal rank.
		switch {
		case ewmaKnown:
			key.speedSignalRank = 0
		case p95Known:
			key.speedSignalRank = 1
		default:
			key.speedSignalRank = 2
		}
		candidate.orderKey = key
	}
	return result
}

// compareMaterializedSpeed compares the pre-materialized same-dimension speed
// keys (section 5.2 key 4b): rank 0 orders by EWMA milliseconds with the P95
// bucket index as an exact-EWMA-tie secondary; rank 1 orders P95-only
// candidates among themselves; rank 2 carries no speed signal. P95 bucket
// indexes are never compared against or converted to EWMA milliseconds and
// never participate in threshold arithmetic.
func compareMaterializedSpeed(left, right tierOrderKey) int {
	if left.speedSignalRank != right.speedSignalRank {
		return left.speedSignalRank - right.speedSignalRank
	}
	if left.speedSignalRank == 0 {
		if left.ewmaMs != right.ewmaMs {
			if left.ewmaMs < right.ewmaMs {
				return -1
			}
			return 1
		}
		if left.p95Known && right.p95Known && left.p95Bucket != right.p95Bucket {
			if left.p95Bucket < right.p95Bucket {
				return -1
			}
			return 1
		}
		return 0
	}
	if left.speedSignalRank == 1 && left.p95Bucket != right.p95Bucket {
		if left.p95Bucket < right.p95Bucket {
			return -1
		}
		return 1
	}
	return 0
}

// resolvedCacheWindow returns the effective 24h window (rate, window input
// tokens) for a candidate. The explicit window map (physical account ID key)
// is authoritative per account; the snapshot fields injected at assembly time
// stand otherwise (and keep their guarantee that a non-nil rate implies
// cache_read <= input).
func resolvedCacheWindow[T any](candidate *indexedCandidate[T], cacheRates map[string]CacheRateWindow) (*float64, int64) {
	if window, ok := cacheRates[candidate.base.AccountID]; ok {
		if window.InputTokens > 0 && window.CacheReadTokens >= 0 && window.CacheReadTokens <= window.InputTokens {
			rate := float64(window.CacheReadTokens) / float64(window.InputTokens)
			return &rate, window.InputTokens
		}
		return nil, window.InputTokens
	}
	if snapshot := candidate.base.HotQuality; snapshot != nil {
		return snapshot.CacheHitRate, snapshot.CacheWindowInputTokens
	}
	return nil, 0
}

// cacheSampleValid applies the ordering-layer validity gate: a valid sample
// needs a computable rate and at least CacheMinSampleInputTokens of window
// input (the assembly path already guarantees rate != nil implies
// cache_read <= input).
func cacheSampleValid(rate *float64, windowInputTokens int64) bool {
	return rate != nil && windowInputTokens >= CacheMinSampleInputTokens
}

// cacheQuantumSnapEpsilon is the near-integer snap window for the quotient of
// the quantization division, in tier units. Genuine off-boundary rates stay
// far above it (a 10^8-token window keeps rates >=1e-7 tiers away from a
// boundary) while binary floats put exact tier-boundary rates an ulp below
// their integer (0.30/0.10 -> 2.999..., 0.60/0.10 -> 5.999...,
// 0.70/0.10 -> 6.999...), which would drop an exact-boundary rate into the
// tier below and contradict the section 5.5 boundary semantics.
const cacheQuantumSnapEpsilon = 1e-9

// cacheQuantumOf quantizes a window rate into mode-backed percentage-point
// tiers (section 5.5), clamped to the contract range 0..floor(1/tier width).
// An exact tier-boundary rate (k × tier width) belongs to tier k. The tier
// width is a decision-wide constant, so the quantization stays global and the
// comparator never degenerates into a pairwise dead zone.
func cacheQuantumOf(params speedQualificationParams, rate float64) int {
	quotient := rate / params.cacheQuantum
	if snapped := math.Round(quotient); math.Abs(quotient-snapped) <= cacheQuantumSnapEpsilon {
		quotient = snapped
	}
	quantum := int(math.Floor(quotient))
	if quantum < 0 {
		quantum = 0
	}
	if quantum > params.cacheQuantumUpperBound {
		quantum = params.cacheQuantumUpperBound
	}
	return quantum
}

func ewmaMsOf[T any](candidate *indexedCandidate[T]) (float64, bool) {
	return normalizedOptionalDuration(candidate.base.HotQuality, func(snapshot *HotQualitySelectionSnapshot) *float64 {
		return snapshot.FirstByteEwma5m
	})
}

func p95BucketOf[T any](candidate *indexedCandidate[T]) (float64, bool) {
	return normalizedOptionalDuration(candidate.base.HotQuality, func(snapshot *HotQualitySelectionSnapshot) *float64 {
		return snapshot.FirstByteP95Bucket10m
	})
}
