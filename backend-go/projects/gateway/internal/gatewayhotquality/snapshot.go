package gatewayhotquality

import (
	"math"
	"sort"
)

// Hot quality snapshot projection mirroring
// backend/src/modules/gateway/runtime/hot-quality-snapshot.ts.

// HotQualitySnapshotState mirrors HotQualitySnapshotState.
type HotQualitySnapshotState struct {
	ScopeKey    string                  `json:"scopeKey"`
	Scope       HotQualityScope         `json:"scope"`
	Buckets     []HotQualityBucketState `json:"buckets"`
	ExpiresAtMs int64                   `json:"expiresAtMs"`
}

// CreateHotQualitySnapshot mirrors createHotQualitySnapshot. The float math
// keeps the Node evaluation order so reliability/confidence/EWMA values are
// bit-identical.
func CreateHotQualitySnapshot(state HotQualitySnapshotState, now int64) *HotQualitySnapshot {
	currentMinute := now / 60_000
	minuteBuckets := make([]HotQualityBucketState, 0, len(state.Buckets))
	for _, bucket := range state.Buckets {
		bucketMinute := bucket.MinuteStartedAtMs / 60_000
		if bucketMinute <= currentMinute && bucketMinute > currentMinute-HotQualityMinuteBucketCount {
			minuteBuckets = append(minuteBuckets, bucket)
		}
	}
	sort.SliceStable(minuteBuckets, func(left, right int) bool {
		return minuteBuckets[left].MinuteStartedAtMs < minuteBuckets[right].MinuteStartedAtMs
	})
	window5m := windowSnapshot(minuteBuckets, currentMinute, 5)
	window10m := windowSnapshot(minuteBuckets, currentMinute, 10)
	window30m := windowSnapshot(minuteBuckets, currentMinute, 30)
	reliability := window5m.AdjustedCompletionRate*0.6 +
		window10m.AdjustedCompletionRate*0.3 +
		window30m.AdjustedCompletionRate*0.1
	confidence := math.Min(1, float64(window10m.QualityAttempts)/10)
	effectiveReliability := 0.5 + (reliability-0.5)*confidence
	return &HotQualitySnapshot{
		ScopeKey:              state.ScopeKey,
		Scope:                 CloneHotQualityScope(state.Scope),
		MinuteBuckets:         cloneMinuteBuckets(minuteBuckets),
		Window5m:              window5m,
		Window10m:             window10m,
		Window30m:             window30m,
		Reliability:           reliability,
		Confidence:            confidence,
		EffectiveReliability:  effectiveReliability,
		ReliabilityLevel:      reliabilityLevel(window5m.QualityAttempts, window10m.QualityAttempts, effectiveReliability),
		SampleState:           sampleState(window30m.QualityAttempts, window10m.QualityAttempts),
		FirstByteEwma5m:       firstByteEwma(minuteBuckets, currentMinute),
		FirstByteP95Bucket10m: p95HistogramBucket(window10m.FirstByteHistogram, window10m.FirstByteSampleCount),
		ExpiresAtMs:           state.ExpiresAtMs,
	}
}

func windowSnapshot(buckets []HotQualityBucketState, currentMinute int64, minutes int) HotQualityWindowSnapshot {
	counters := emptyCounters()
	for _, bucket := range buckets {
		bucketMinute := bucket.MinuteStartedAtMs / 60_000
		if bucketMinute <= currentMinute-int64(minutes) {
			continue
		}
		mergeCounters(&counters, bucket)
	}
	qualityAttempts := addInt64(addInt64(counters.CompletedResponses, counters.LocalTransportFailures), counters.ExplicitPolicyFailures)
	window := HotQualityWindowSnapshot{
		HotQualityCounters:     cloneCounters(counters.HotQualityCounters),
		Minutes:                minutes,
		QualityAttempts:        qualityAttempts,
		AdjustedCompletionRate: float64(counters.CompletedResponses+2) / float64(qualityAttempts+4),
	}
	return window
}

func firstByteEwma(buckets []HotQualityBucketState, currentMinute int64) *float64 {
	var ewma *float64
	// HotQualityFirstByteEwmaAlpha is read into a variable so `1 - alpha`
	// stays a runtime double subtraction, matching Node bit-for-bit.
	alpha := HotQualityFirstByteEwmaAlpha
	for i := range buckets {
		bucket := &buckets[i]
		bucketMinute := bucket.MinuteStartedAtMs / 60_000
		if bucketMinute <= currentMinute-5 || bucket.FirstByteSampleCount == 0 {
			continue
		}
		average := float64(bucket.FirstByteSumMs) / float64(bucket.FirstByteSampleCount)
		if ewma == nil {
			value := average
			ewma = &value
		} else {
			value := *ewma*(1-alpha) + average*alpha
			ewma = &value
		}
	}
	return ewma
}

func p95HistogramBucket(histogram HotQualityFirstByteHistogram, samples int64) *int64 {
	if samples == 0 {
		return nil
	}
	target := int64(math.Ceil(float64(samples) * 0.95))
	cumulative := int64(0)
	for index := 0; index < len(HotQualityFirstByteBucketUpperBoundsMS); index++ {
		cumulative += histogram[index]
		if cumulative >= target {
			value := int64(index)
			return &value
		}
	}
	last := int64(len(HotQualityFirstByteBucketUpperBoundsMS) - 1)
	return &last
}

func reliabilityLevel(qualityAttempts5m int64, qualityAttempts10m int64, effectiveReliability float64) HotQualityReliabilityLevel {
	if qualityAttempts10m < 3 {
		return HotQualityReliabilityUnknown
	}
	if qualityAttempts5m >= 3 && effectiveReliability >= 0.85 {
		return HotQualityReliabilityHealthy
	}
	if qualityAttempts5m >= 3 && effectiveReliability < 0.6 {
		return HotQualityReliabilityUnhealthy
	}
	return HotQualityReliabilityUncertain
}

func sampleState(qualityAttempts30m int64, qualityAttempts10m int64) HotQualitySampleState {
	if qualityAttempts30m == 0 {
		return HotQualitySampleCold
	}
	if qualityAttempts10m < 3 {
		return HotQualitySampleWarming
	}
	return HotQualitySampleKnown
}

func mergeCounters(target *HotQualityBucketState, source HotQualityBucketState) {
	target.Attempts = addInt64(target.Attempts, source.Attempts)
	target.CompletedResponses = addInt64(target.CompletedResponses, source.CompletedResponses)
	target.UpstreamResponseFailures = addInt64(target.UpstreamResponseFailures, source.UpstreamResponseFailures)
	target.LocalTransportFailures = addInt64(target.LocalTransportFailures, source.LocalTransportFailures)
	target.Timeouts = addInt64(target.Timeouts, source.Timeouts)
	target.ReadInterruptions = addInt64(target.ReadInterruptions, source.ReadInterruptions)
	target.IncompleteResponses = addInt64(target.IncompleteResponses, source.IncompleteResponses)
	target.ExplicitPolicyFailures = addInt64(target.ExplicitPolicyFailures, source.ExplicitPolicyFailures)
	target.UnknownOutcomes = addInt64(target.UnknownOutcomes, source.UnknownOutcomes)
	target.ClientCancellations = addInt64(target.ClientCancellations, source.ClientCancellations)
	target.FirstByteSampleCount = addInt64(target.FirstByteSampleCount, source.FirstByteSampleCount)
	target.FirstByteSumMs = addInt64(target.FirstByteSumMs, source.FirstByteSumMs)
	for index := range target.FirstByteHistogram {
		target.FirstByteHistogram[index] = addInt64(target.FirstByteHistogram[index], source.FirstByteHistogram[index])
	}
	target.LastCompletedAtMs = maximumInt64Ptr(target.LastCompletedAtMs, source.LastCompletedAtMs)
	target.LastFailureAtMs = maximumInt64Ptr(target.LastFailureAtMs, source.LastFailureAtMs)
}

func cloneMinuteBuckets(buckets []HotQualityBucketState) []HotQualityMinuteBucket {
	clones := make([]HotQualityMinuteBucket, len(buckets))
	for i, bucket := range buckets {
		clones[i] = HotQualityMinuteBucket{
			HotQualityCounters: cloneCounters(bucket.HotQualityCounters),
			MinuteStartedAtMs:  bucket.MinuteStartedAtMs,
		}
	}
	return clones
}

func cloneCounters(counters HotQualityCounters) HotQualityCounters {
	cloned := counters
	cloned.FirstByteHistogram = HotQualityFirstByteHistogram(counters.FirstByteHistogram)
	// Node stores these as primitive numbers; deep-copy the pointers so a
	// snapshot never aliases store-internal bucket state (克隆防污染).
	cloned.LastCompletedAtMs = cloneInt64Ptr(counters.LastCompletedAtMs)
	cloned.LastFailureAtMs = cloneInt64Ptr(counters.LastFailureAtMs)
	return cloned
}

func cloneInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func emptyCounters() HotQualityBucketState {
	return HotQualityBucketState{
		HotQualityCounters: HotQualityCounters{},
	}
}

func addInt64(left int64, right int64) int64 {
	sum := left + right
	if sum > maxSafeInteger {
		return maxSafeInteger
	}
	return sum
}

func maximumInt64Ptr(left *int64, right *int64) *int64 {
	switch {
	case left == nil:
		return right
	case right == nil:
		return left
	}
	if *left >= *right {
		return left
	}
	return right
}
