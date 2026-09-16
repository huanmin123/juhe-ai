package gatewayobs

// w11c: observer write-failure branches, request-context resolution and
// metric-key fallbacks.

import (
	"context"
	"errors"
	"testing"
)

type w11cFailingStore struct{}

func (w11cFailingStore) Record(ctx context.Context, observation Observation, nowMs int64) error {
	return errors.New("w11c store down")
}

func (w11cFailingStore) RecordBatch(ctx context.Context, entries []BatchEntry, nowMs int64) error {
	return errors.New("w11c store down")
}

func (w11cFailingStore) Snapshot(ctx context.Context) (Snapshot, error) {
	return Snapshot{}, errors.New("w11c store down")
}

func TestW11CObserverWriteFailures(t *testing.T) {
	// A nil store records the failure and reports false.
	nilStore := NewObserver(ObserverOptions{})
	if nilStore.RecordGatewayRoutingObservation(context.Background(), Observation{Kind: KindAttempt, Outcome: "failure"}, 1) {
		t.Fatalf("nil store must report false")
	}
	// A failing store does the same.
	failing := NewObserver(ObserverOptions{Store: w11cFailingStore{}})
	if failing.RecordGatewayRoutingObservation(context.Background(), Observation{Kind: KindAttempt, Outcome: "failure"}, 1) {
		t.Fatalf("failing store must report false")
	}
	// Invalid timestamps fall back to the injected clock.
	clocked := NewObserver(ObserverOptions{Store: NewMemoryGatewayRoutingObservabilityStore(), Now: func() int64 { return 42 }})
	if !clocked.RecordGatewayRoutingObservation(context.Background(), Observation{Kind: KindAttempt, Outcome: "success"}, -5) {
		t.Fatalf("clocked record must succeed")
	}
	snapshot, err := clocked.store.Snapshot(context.Background())
	if err != nil || snapshot.UpdatedAtMs != 42 {
		t.Fatalf("snapshot = (%+v, %v)", snapshot, err)
	}
	// The context source feeds ctx-less entry points.
	sourced := NewObserver(ObserverOptions{
		Store:         NewMemoryGatewayRoutingObservabilityStore(),
		ContextSource: func() context.Context { return context.WithValue(context.Background(), struct{}{}, "w11c") },
	})
	if got := sourced.requestContext(nil); got == nil {
		t.Fatalf("context source must supply a context")
	}
	if got := sourced.requestContext(context.Background()); got == nil {
		t.Fatalf("explicit context must win")
	}
	// Flush with an empty batch is a no-op; with a failing store it logs.
	failing.FlushPending(1)
	sourced.Observe(Observation{Kind: KindAttempt, Outcome: "success"}, 1)
}

func TestW11CMetricKeyFallbacks(t *testing.T) {
	// Unknown kinds produce an empty metric key.
	if GatewayRoutingObservationMetricKey(Observation{Kind: "bogus"}) != "" {
		t.Fatalf("unknown kind must map to an empty key")
	}
	// Known kinds map with lowercased outcomes.
	if got := GatewayRoutingObservationMetricKey(Observation{Kind: KindAttempt, Outcome: "FAILURE"}); got != "attempt.FAILURE" {
		t.Fatalf("attempt key = %s", got)
	}
	if got := GatewayRoutingObservationMetricKey(Observation{Kind: KindExploration, Outcome: "JOIN"}); got != "exploration.JOIN" {
		t.Fatalf("exploration key = %s", got)
	}
	if got := GatewayRoutingObservationMetricKey(Observation{Kind: KindTierEscape, Outcome: "UP"}); got != "tier_escape.UP" {
		t.Fatalf("tier escape key = %s", got)
	}
	if got := GatewayRoutingObservationMetricKey(Observation{Kind: KindBudget, Outcome: "EXHAUSTED"}); got != "budget.EXHAUSTED" {
		t.Fatalf("budget key = %s", got)
	}
	if got := GatewayRoutingObservationMetricKey(Observation{Kind: KindCircuitDispatch, Outcome: "BLOCKED", Phase: "SUSPECT"}); got != "circuit.dispatch.BLOCKED.suspect" {
		t.Fatalf("circuit dispatch key = %s", got)
	}
}

func TestW11CLowerASCIIAndSanitizerEdges(t *testing.T) {
	if lowerASCII("AbC") != "abc" {
		t.Fatalf("lowerASCII misbehaves")
	}
}
