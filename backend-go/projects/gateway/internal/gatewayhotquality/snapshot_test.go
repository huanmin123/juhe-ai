package gatewayhotquality

import (
	"math"
	"testing"
)

func int64Ptr(value int64) *int64 { return &value }

func TestCreateHotQualitySnapshotWindowsAndRates(t *testing.T) {
	// current minute = 100 → 100_000ms; buckets at minutes 70(out), 96, 98, 99, 100
	buckets := []HotQualityBucketState{
		{MinuteStartedAtMs: 70 * 60_000}, // outside 30m window
		{MinuteStartedAtMs: 96 * 60_000, HotQualityCounters: HotQualityCounters{
			Attempts: 4, CompletedResponses: 3, ExplicitPolicyFailures: 1,
			FirstByteSampleCount: 2, FirstByteSumMs: 1500,
			FirstByteHistogram: HotQualityFirstByteHistogram{1, 1},
		}},
		{MinuteStartedAtMs: 98 * 60_000, HotQualityCounters: HotQualityCounters{
			Attempts: 2, LocalTransportFailures: 2, Timeouts: 1, IncompleteResponses: 1,
			LastFailureAtMs: int64Ptr(98 * 60_000),
		}},
		{MinuteStartedAtMs: 99 * 60_000, HotQualityCounters: HotQualityCounters{
			Attempts: 1, CompletedResponses: 1,
			FirstByteSampleCount: 1, FirstByteSumMs: 2500,
			FirstByteHistogram: HotQualityFirstByteHistogram{0, 1},
			LastCompletedAtMs:  int64Ptr(99 * 60_000),
		}},
	}
	state := HotQualitySnapshotState{
		ScopeKey:    "scope-key",
		Scope:       HotQualityScope{AccountRuntimeKey: "acc", ProtocolProfile: "p", RequestLane: "text", ModelFamily: "unknown"},
		Buckets:     buckets,
		ExpiresAtMs: 101 * 60_000,
	}
	snapshot := CreateHotQualitySnapshot(state, 100*60_000)

	if snapshot.ScopeKey != "scope-key" || snapshot.ExpiresAtMs != 101*60_000 {
		t.Fatalf("snapshot header = %+v", snapshot)
	}
	if len(snapshot.MinuteBuckets) != 3 {
		t.Fatalf("minuteBuckets = %d, want 3 (filtered)", len(snapshot.MinuteBuckets))
	}
	if snapshot.MinuteBuckets[0].MinuteStartedAtMs != 96*60_000 || snapshot.MinuteBuckets[2].MinuteStartedAtMs != 99*60_000 {
		t.Fatalf("minuteBuckets not sorted: %+v", snapshot.MinuteBuckets)
	}

	// window5m = minutes 96..100; quality attempts = completed 4 + transport 2 + policy 1
	w5 := snapshot.Window5m
	if w5.Minutes != 5 || w5.QualityAttempts != 7 || w5.CompletedResponses != 4 || w5.LocalTransportFailures != 2 {
		t.Fatalf("window5m = %+v", w5)
	}
	if w5.Attempts != 7 || w5.ExplicitPolicyFailures != 1 || w5.Timeouts != 1 {
		t.Fatalf("window5m counters = %+v", w5)
	}
	if w5.FirstByteSampleCount != 3 || w5.FirstByteSumMs != 4000 {
		t.Fatalf("window5m first byte = %+v", w5.HotQualityCounters)
	}
	// (4 + 2) / (7 + 4)
	if w5.AdjustedCompletionRate != 6.0/11.0 {
		t.Fatalf("window5m rate = %v", w5.AdjustedCompletionRate)
	}
	// window10m = minutes 91..100 → same buckets
	if snapshot.Window10m.QualityAttempts != 7 {
		t.Fatalf("window10m = %+v", snapshot.Window10m)
	}
	// window30m → same buckets (minute 70 excluded)
	if snapshot.Window30m.QualityAttempts != 7 {
		t.Fatalf("window30m = %+v", snapshot.Window30m)
	}

	wantReliability := (6.0/11.0)*0.6 + (6.0/11.0)*0.3 + (6.0/11.0)*0.1
	if math.Abs(snapshot.Reliability-wantReliability) > 1e-15 {
		t.Fatalf("reliability = %v, want %v", snapshot.Reliability, wantReliability)
	}
	if snapshot.Confidence != 0.7 {
		t.Fatalf("confidence = %v", snapshot.Confidence)
	}
	wantEffective := 0.5 + (wantReliability-0.5)*0.7
	if snapshot.EffectiveReliability != wantEffective {
		t.Fatalf("effectiveReliability = %v, want %v", snapshot.EffectiveReliability, wantEffective)
	}
	if snapshot.ReliabilityLevel != HotQualityReliabilityUnhealthy {
		t.Fatalf("reliabilityLevel = %s", snapshot.ReliabilityLevel)
	}
	if snapshot.SampleState != HotQualitySampleKnown {
		t.Fatalf("sampleState = %s", snapshot.SampleState)
	}
	// EWMA over minutes 96..100: avg1 = 750, avg2 = 2500 → 750*0.6 + 2500*0.4
	alpha := HotQualityFirstByteEwmaAlpha
	wantEwma := 750*(1-alpha) + 2500*alpha
	if snapshot.FirstByteEwma5m == nil || *snapshot.FirstByteEwma5m != wantEwma {
		t.Fatalf("firstByteEwma5m = %v, want %v", snapshot.FirstByteEwma5m, wantEwma)
	}
	// p95 of window10m histogram [1,2,...]: samples=3 → target=3 → cumulative 1,3 → bucket 1
	if snapshot.FirstByteP95Bucket10m == nil || *snapshot.FirstByteP95Bucket10m != 1 {
		t.Fatalf("firstByteP95Bucket10m = %v", snapshot.FirstByteP95Bucket10m)
	}
}

func TestCreateHotQualitySnapshotLevelsTable(t *testing.T) {
	testCases := []struct {
		name               string
		qualityAttempts5m  int64
		qualityAttempts10m int64
		effective          float64
		wantLevel          string
		wantSampleState    string
	}{
		{"cold", 0, 0, 0.5, "unknown", "cold"},
		{"warming", 1, 2, 0.9, "unknown", "warming"},
		{"healthy", 4, 5, 0.9, "healthy", "known"},
		{"unhealthy", 4, 5, 0.5, "unhealthy", "known"},
		{"uncertain mid", 4, 5, 0.7, "uncertain", "known"},
		{"uncertain low attempts", 2, 5, 0.9, "uncertain", "known"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := reliabilityLevel(testCase.qualityAttempts5m, testCase.qualityAttempts10m, testCase.effective); got != testCase.wantLevel {
				t.Fatalf("reliabilityLevel = %s, want %s", got, testCase.wantLevel)
			}
			if got := sampleState(testCase.qualityAttempts10m, testCase.qualityAttempts10m); got != testCase.wantSampleState {
				t.Fatalf("sampleState = %s, want %s", got, testCase.wantSampleState)
			}
		})
	}
}

func TestCreateHotQualitySnapshotClonesBuckets(t *testing.T) {
	lastCompleted := int64(42)
	bucket := HotQualityBucketState{
		MinuteStartedAtMs: 5 * 60_000,
		HotQualityCounters: HotQualityCounters{
			CompletedResponses: 1,
			LastCompletedAtMs:  &lastCompleted,
		},
	}
	state := HotQualitySnapshotState{
		ScopeKey:    "k",
		Scope:       HotQualityScope{AccountRuntimeKey: "a", ProtocolProfile: "p", RequestLane: "text", ModelFamily: "unknown"},
		Buckets:     []HotQualityBucketState{bucket},
		ExpiresAtMs: 10 * 60_000,
	}
	snapshot := CreateHotQualitySnapshot(state, 6*60_000)

	// Mutate the source bucket after snapshotting; the snapshot must stay intact.
	bucket.CompletedResponses = 99
	lastCompleted = 777
	state.Buckets[0].Attempts = 50

	if snapshot.MinuteBuckets[0].CompletedResponses != 1 {
		t.Fatalf("snapshot aliased bucket counters")
	}
	if *snapshot.MinuteBuckets[0].LastCompletedAtMs != 42 {
		t.Fatalf("snapshot aliased lastCompletedAtMs pointer")
	}
	if snapshot.MinuteBuckets[0].Attempts != 0 {
		t.Fatalf("snapshot aliased attempts")
	}
	// Window counters must not alias either.
	if snapshot.Window5m.LastCompletedAtMs == nil || *snapshot.Window5m.LastCompletedAtMs != 42 {
		t.Fatalf("window aliased lastCompletedAtMs")
	}
	*snapshot.Window5m.LastCompletedAtMs = 1
	if *snapshot.MinuteBuckets[0].LastCompletedAtMs != 42 {
		t.Fatalf("window and minute buckets alias the same pointer")
	}
}

func TestP95HistogramBucket(t *testing.T) {
	if p95HistogramBucket(HotQualityFirstByteHistogram{}, 0) != nil {
		t.Fatalf("empty samples must return nil")
	}
	// 20 samples → target = ceil(19) = 19; cumulative 10, 15, 20 → bucket 2
	histogram := HotQualityFirstByteHistogram{10, 5, 5}
	if got := p95HistogramBucket(histogram, 20); got == nil || *got != 2 {
		t.Fatalf("p95 = %v, want bucket 2", got)
	}
	// Overflow guard: cumulative never reaches target (impossible here) falls back to last bucket.
	if got := p95HistogramBucket(HotQualityFirstByteHistogram{0, 0, 0, 0, 0, 0, 0, 5}, 100); got == nil || *got != 7 {
		t.Fatalf("p95 fallback = %v", got)
	}
}

func TestFirstByteEwmaSkipsStaleBuckets(t *testing.T) {
	buckets := []HotQualityBucketState{
		{MinuteStartedAtMs: 90 * 60_000, HotQualityCounters: HotQualityCounters{FirstByteSampleCount: 1, FirstByteSumMs: 100}},
		{MinuteStartedAtMs: 97 * 60_000, HotQualityCounters: HotQualityCounters{FirstByteSampleCount: 2, FirstByteSumMs: 1000}},
		{MinuteStartedAtMs: 98 * 60_000, HotQualityCounters: HotQualityCounters{FirstByteSampleCount: 0, FirstByteSumMs: 9999}},
	}
	ewma := firstByteEwma(buckets, 100)
	if ewma == nil || *ewma != 500 {
		t.Fatalf("ewma = %v, want 500 (only minute 97 has samples)", ewma)
	}
	if firstByteEwma(nil, 100) != nil {
		t.Fatalf("no samples must return nil")
	}
}

func TestSelectionViewConversion(t *testing.T) {
	ewma := 12.5
	p95 := int64(3)
	snapshot := &HotQualitySnapshot{
		Window5m: HotQualityWindowSnapshot{
			QualityAttempts: 5,
			HotQualityCounters: HotQualityCounters{
				LastCompletedAtMs: int64Ptr(10), LastFailureAtMs: int64Ptr(11),
			},
		},
		Window10m:             HotQualityWindowSnapshot{QualityAttempts: 6},
		Window30m:             HotQualityWindowSnapshot{QualityAttempts: 7},
		EffectiveReliability:  0.8,
		ReliabilityLevel:      HotQualityReliabilityHealthy,
		SampleState:           HotQualitySampleKnown,
		FirstByteEwma5m:       &ewma,
		FirstByteP95Bucket10m: &p95,
	}
	view := snapshot.SelectionView()
	if view.Window5m.QualityAttempts != 5 || *view.Window5m.LastCompletedAtMs != 10 || *view.Window5m.LastFailureAtMs != 11 {
		t.Fatalf("view window5m = %+v", view.Window5m)
	}
	if view.EffectiveReliability != 0.8 || view.ReliabilityLevel != HotQualityReliabilityHealthy || view.SampleState != HotQualitySampleKnown {
		t.Fatalf("view scalars = %+v", view)
	}
	if view.FirstByteEwma5m == nil || *view.FirstByteEwma5m != 12.5 {
		t.Fatalf("view ewma = %v", view.FirstByteEwma5m)
	}
	if view.FirstByteP95Bucket10m == nil || *view.FirstByteP95Bucket10m != 3 {
		t.Fatalf("view p95 = %v", view.FirstByteP95Bucket10m)
	}
	if selectionViewOrNil(nil) != nil {
		t.Fatalf("nil snapshot must map to nil view")
	}
}
