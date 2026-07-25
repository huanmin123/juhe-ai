package gatewayhotquality

import (
	"math"
	"reflect"
	"testing"
)

func TestDecideCostFirstPreservesTierBeforeQuality(t *testing.T) {
	t.Parallel()
	input := testInput(RoutingModeCostFirst, testCandidate("fallback", "runtime-f", Tier{Fallback: true}, false, SampleStateKnown, ReliabilityHealthy, .99, 1), testCandidate("primary", "runtime-p", Tier{}, false, SampleStateKnown, ReliabilityUnhealthy, .01, 900))
	result, err := Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := candidateIDs(result.Ordered), []string{"primary", "fallback"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestDecideCostFirstUsesColdNeutralityAndQuality(t *testing.T) {
	t.Parallel()
	input := testInput(RoutingModeCostFirst, testCandidate("cold", "cold-key", Tier{}, false, SampleStateCold, ReliabilityUnhealthy, .1, 1), testCandidate("unknown", "unknown-key", Tier{}, false, SampleStateKnown, ReliabilityUnknown, .5, 100), testCandidate("healthy", "healthy-key", Tier{}, false, SampleStateKnown, ReliabilityHealthy, .2, 900))
	result, err := Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := candidateIDs(result.Ordered), []string{"healthy", "cold", "unknown"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestDecideNormalizesAbsentSnapshotToCold(t *testing.T) {
	t.Parallel()
	candidate := testCandidate("cold", "key", Tier{}, false, SampleStateKnown, ReliabilityHealthy, .9, 1)
	candidate.Snapshot = Snapshot{}
	decision, err := Decide(testInput(RoutingModeCostFirst, candidate))
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if got, want := decision.Ordered[0].Snapshot, (Snapshot{SampleState: SampleStateCold, Reliability: ReliabilityUnknown, EffectiveReliability: .5}); got != want {
		t.Fatalf("normalized snapshot = %#v, want %#v", got, want)
	}

	candidate.Snapshot = Snapshot{SampleState: SampleStateCold}
	if _, err := Decide(testInput(RoutingModeCostFirst, candidate)); err == nil {
		t.Fatal("Decide() accepted partially populated snapshot")
	}
}

func TestDecideSpeedFirstMovesOnlyLatencyDegradedCandidates(t *testing.T) {
	t.Parallel()
	input := testInput(RoutingModeSpeedFirst, testCandidate("primary-degraded", "a", Tier{}, true, SampleStateKnown, ReliabilityHealthy, .9, 10), testCandidate("fallback-fast", "b", Tier{Fallback: true}, false, SampleStateKnown, ReliabilityUnhealthy, .1, 1000))
	result, err := Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := candidateIDs(result.Ordered), []string{"fallback-fast", "primary-degraded"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestDecideRejectsCrossScopeAndDropsLaterRuntimeDuplicate(t *testing.T) {
	t.Parallel()
	input := testInput(RoutingModeCostFirst, testCandidate("first", "same", Tier{}, false, SampleStateKnown, ReliabilityHealthy, .9, 1), testCandidate("later", "same", Tier{}, false, SampleStateKnown, ReliabilityHealthy, .9, 1))
	result, err := Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	if result.DroppedDuplicateKey != 1 || len(result.Ordered) != 1 || result.Ordered[0].AccountID != "first" {
		t.Fatalf("duplicate decision = %#v", result)
	}
	input.Candidates[1].RouteScope = "other"
	if _, err := Decide(input); err == nil {
		t.Fatal("Decide() accepted cross-scope candidate")
	}
}

func TestDecideUsesStableBindingOrderAndRejectsNonFiniteMetrics(t *testing.T) {
	t.Parallel()
	first := testCandidate("z-account", "first", Tier{}, false, SampleStateKnown, ReliabilityUnknown, .5, 10)
	first.StableBindingOrder = 1
	second := testCandidate("a-account", "second", Tier{}, false, SampleStateKnown, ReliabilityUnknown, .5, 10)
	second.StableBindingOrder = 2
	result, err := Decide(testInput(RoutingModeCostFirst, second, first))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := candidateIDs(result.Ordered), []string{"z-account", "a-account"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for _, metric := range []float64{math.NaN(), math.Inf(1)} {
		bad := testCandidate("bad", "bad", Tier{}, false, SampleStateKnown, ReliabilityHealthy, metric, 1)
		if _, err := Decide(testInput(RoutingModeCostFirst, bad)); err == nil {
			t.Fatal("Decide() accepted non-finite reliability")
		}
		bad = testCandidate("bad", "bad", Tier{}, false, SampleStateKnown, ReliabilityHealthy, .5, metric)
		if _, err := Decide(testInput(RoutingModeCostFirst, bad)); err == nil {
			t.Fatal("Decide() accepted non-finite latency")
		}
	}
}

func testInput(mode RoutingMode, candidates ...Candidate) DecisionInput {
	return DecisionInput{Mode: mode, RouteScope: "scope", Candidates: candidates}
}
func testCandidate(account, key string, tier Tier, degraded bool, state SampleState, reliability Reliability, score, latency float64) Candidate {
	return Candidate{AccountID: account, RuntimeKey: key, RouteScope: "scope", Tier: tier, LatencyDegraded: degraded, Snapshot: Snapshot{SampleState: state, Reliability: reliability, EffectiveReliability: score, EWMAFirstByteMS: latency, P95FirstByteMS: latency}}
}
func candidateIDs(candidates []Candidate) []string {
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate.AccountID)
	}
	return result
}
