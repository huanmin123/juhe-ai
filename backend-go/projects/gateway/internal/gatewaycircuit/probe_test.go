package gatewaycircuit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// mockProbeStore records calls and simulates the memory driver semantics of
// shared/runtime-probe-state-store.ts for the used surface.
type mockProbeStore struct {
	states map[string]*ProbeState
	gen    map[string]int64

	getCalls          int
	setIfAbsentCalled bool
}

func newMockProbeStore() *mockProbeStore {
	return &mockProbeStore{states: map[string]*ProbeState{}, gen: map[string]int64{}}
}

func (m *mockProbeStore) Get(_ context.Context, runtimeKey string) (*ProbeState, error) {
	m.getCalls++
	if state, ok := m.states[runtimeKey]; ok {
		clone := *state
		return &clone, nil
	}
	return nil, nil
}

func (m *mockProbeStore) NextGeneration(_ context.Context, runtimeKey string, _ int64) (int64, error) {
	m.gen[runtimeKey]++
	return m.gen[runtimeKey], nil
}

func (m *mockProbeStore) SetIfAbsent(_ context.Context, state ProbeState, _ int64) (bool, error) {
	m.setIfAbsentCalled = true
	if _, ok := m.states[state.RuntimeKey]; ok {
		return false, nil
	}
	clone := state
	m.states[state.RuntimeKey] = &clone
	return true, nil
}

func (m *mockProbeStore) Merge(_ context.Context, state ProbeState, _ int64, options ProbeMergeOptions) (*ProbeState, error) {
	current, ok := m.states[state.RuntimeKey]
	if !ok {
		return nil, nil
	}
	next := *current
	incoming := state
	for _, field := range options.PreserveCurrentFields {
		switch field {
		case "probeRunId":
			incoming.ProbeRunID = current.ProbeRunID
		case "probeRunUntilMs":
			incoming.ProbeRunUntilMs = current.ProbeRunUntilMs
		case "outcome":
			incoming.Outcome = current.Outcome
		case "completedAtMs":
			incoming.CompletedAtMs = current.CompletedAtMs
		}
	}
	next = incoming
	for _, union := range options.UnionArrayFields {
		if union.Field == "sourceFences" {
			seen := map[string]struct{}{}
			var merged []string
			for _, fence := range append(append([]string{}, current.SourceFences...), incoming.SourceFences...) {
				if _, ok := seen[fence]; ok {
					continue
				}
				seen[fence] = struct{}{}
				merged = append(merged, fence)
				if len(merged) >= union.MaxItems {
					break
				}
			}
			next.SourceFences = merged
		}
	}
	m.states[state.RuntimeKey] = &next
	clone := next
	return &clone, nil
}

func (m *mockProbeStore) AcquireGenerationRun(_ context.Context, runtimeKey string, generation int64, ownerToken string, leaseUntilMs int64, _ int64) (*ProbeState, error) {
	current, ok := m.states[runtimeKey]
	if !ok || current.Generation != generation {
		if current := m.states[runtimeKey]; current != nil {
			clone := *current
			return &clone, nil
		}
		return nil, nil
	}
	now := leaseUntilMs - 90_000
	current.ProbeRunID = strPtr(ownerToken)
	current.ProbeRunUntilMs = int64Ptr(leaseUntilMs)
	current.NextProbeAtMs = now
	clone := *current
	return &clone, nil
}

func (m *mockProbeStore) CommitGenerationRun(_ context.Context, next ProbeState, ownerToken string, _ int64) (bool, error) {
	current, ok := m.states[next.RuntimeKey]
	if !ok || current.Generation != next.Generation || current.ProbeRunID == nil || *current.ProbeRunID != ownerToken {
		return false, nil
	}
	clone := next
	m.states[next.RuntimeKey] = &clone
	return true, nil
}

func (m *mockProbeStore) ReplaceSettledGeneration(_ context.Context, next ProbeState, expectedGeneration int64, _ int64) (*ProbeState, error) {
	current, ok := m.states[next.RuntimeKey]
	if !ok || current.Generation != expectedGeneration || current.Outcome == nil {
		return nil, nil
	}
	replaced := *current
	clone := next
	m.states[next.RuntimeKey] = &clone
	return &replaced, nil
}

func testFence(tag string) ProbeSourceFence {
	return ProbeSourceFence{
		StateKey:         "state-" + tag,
		AccountID:        "acc",
		SourceGeneration: 3,
		SourceFenceID:    "0123abcd-0000-4000-8000-" + fmt.Sprintf("%012d", len(tag)),
	}
}

func TestProbeAcquireAbsentBecomesOwner(t *testing.T) {
	store := newMockProbeStore()
	coordinator := NewProbeCoordinator(store, func() int64 { return 1000 }, func() string { return "owner-1" })
	result, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "acc:authorized:s:g:a", ProbeKind: ProbeKindAccountHealthCheck,
		ConfigRevision: 1,
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if result.Disposition != ProbeDispositionOwner || result.OwnerToken != "owner-1" || result.Generation != 1 {
		t.Fatalf("result = %+v", result)
	}
	if !store.setIfAbsentCalled {
		t.Fatalf("expected SetIfAbsent")
	}
	state, _ := coordinator.GetState(context.Background(), result.RuntimeKey)
	if state == nil || state.ProbeRunID == nil || *state.ProbeRunID != "owner-1" {
		t.Fatalf("state = %+v", state)
	}
}

func TestProbeJoinWhileRunActive(t *testing.T) {
	store := newMockProbeStore()
	now := int64(1000)
	coordinator := NewProbeCoordinator(store, func() int64 { return now }, counterID())
	first, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "acc", ProbeKind: ProbeKindCodexSourceAvoidance, ConfigRevision: 2,
	})
	if err != nil || first.Disposition != ProbeDispositionOwner {
		t.Fatalf("first = (%s, %v)", first.Disposition, err)
	}
	// A second contender joins while the owner lease is alive.
	second, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "acc", ProbeKind: ProbeKindCodexSourceAvoidance, ConfigRevision: 2,
	})
	if err != nil || second.Disposition != ProbeDispositionJoined {
		t.Fatalf("second = (%s, %v)", second.Disposition, err)
	}
	if second.RetryAtMs != 1000+defaultProbeLeaseMs {
		t.Fatalf("retryAt = %d", second.RetryAtMs)
	}
	// After the lease expires a takeover wins the same generation.
	now += defaultProbeLeaseMs + 1
	third, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "acc", ProbeKind: ProbeKindCodexSourceAvoidance, ConfigRevision: 2,
	})
	if err != nil || third.Disposition != ProbeDispositionOwner || third.Generation != first.Generation {
		t.Fatalf("third = (%s, gen %d, %v)", third.Disposition, third.Generation, err)
	}
}

func counterID() func() string {
	next := 0
	return func() string {
		next++
		return fmt.Sprintf("owner-%d", next)
	}
}

func TestProbeSettleFencing(t *testing.T) {
	store := newMockProbeStore()
	coordinator := NewProbeCoordinator(store, func() int64 { return 1000 }, counterID())
	first, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "acc", ProbeKind: ProbeKindAccountHealthCheck, ConfigRevision: 1,
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// A stale owner token cannot settle.
	ok, err := coordinator.Settle(context.Background(), SettleProbeInput{
		RuntimeKey: first.RuntimeKey, Generation: first.Generation, OwnerToken: "stale", Outcome: ProbeOutcomeSuccess,
	})
	if err != nil || ok {
		t.Fatalf("stale settle = (%v, %v)", ok, err)
	}
	ok, err = coordinator.Settle(context.Background(), SettleProbeInput{
		RuntimeKey: first.RuntimeKey, Generation: first.Generation, OwnerToken: first.OwnerToken, Outcome: ProbeOutcomeSuccess,
	})
	if err != nil || !ok {
		t.Fatalf("owner settle = (%v, %v)", ok, err)
	}
	// A settled generation joins with the completed outcome.
	joined, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "acc", ProbeKind: ProbeKindAccountHealthCheck, ConfigRevision: 1,
	})
	if err != nil || joined.Disposition != ProbeDispositionJoined || joined.RetryAtMs != 1000 {
		t.Fatalf("join settled = (%s, %d, %v)", joined.Disposition, joined.RetryAtMs, err)
	}
	// forceNewGeneration replaces the settled generation and carries the
	// replaced fence settlement.
	replacement, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "acc", ProbeKind: ProbeKindAccountHealthCheck, ConfigRevision: 1,
		ForceNewGeneration: true, SourceFence: refFence(testFence("repl")), ExecutionRole: "source_dispatch",
	})
	if err != nil || replacement.Disposition != ProbeDispositionOwner {
		t.Fatalf("replacement = (%s, %v)", replacement.Disposition, err)
	}
	if replacement.ReplacedFenceSettlement == nil ||
		replacement.ReplacedFenceSettlement.Outcome != ProbeOutcomeSuccess ||
		replacement.ReplacedFenceSettlement.Generation != first.Generation {
		t.Fatalf("replaced settlement = %+v", replacement.ReplacedFenceSettlement)
	}
	if replacement.Generation == first.Generation {
		t.Fatalf("replacement must bump the generation")
	}
}

func refFence(fence ProbeSourceFence) *ProbeSourceFence {
	return &fence
}

func TestProbeReleaseForExecutionAndDispatchSettle(t *testing.T) {
	store := newMockProbeStore()
	coordinator := NewProbeCoordinator(store, func() int64 { return 1000 }, counterID())
	fence := testFence("dispatch")
	acquired, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "acc", ProbeKind: ProbeKindAccountHealthCheck, ConfigRevision: 1,
		SourceFence: &fence,
	})
	if err != nil || acquired.Disposition != ProbeDispositionOwner {
		t.Fatalf("acquire = (%s, %v)", acquired.Disposition, err)
	}
	released, err := coordinator.ReleaseForExecution(context.Background(), ReleaseProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, OwnerToken: acquired.OwnerToken,
	})
	if err != nil || !released {
		t.Fatalf("release = (%v, %v)", released, err)
	}
	// A source observer joins while the dispatch is pending.
	joined, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "acc", ProbeKind: ProbeKindAccountHealthCheck, ConfigRevision: 1,
		ExecutionRole: "source_dispatch",
	})
	if err != nil || joined.Disposition != ProbeDispositionJoined {
		t.Fatalf("join during dispatch = (%s, %v)", joined.Disposition, err)
	}
	// The disposition is retry while the dispatch window is open.
	disposition, err := coordinator.SourceFenceSettlementDisposition(context.Background(), SourceFenceDispositionInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, SourceFence: fence,
	})
	if err != nil || disposition.Disposition != ProbeSettlementRetry {
		t.Fatalf("disposition = (%+v, %v)", disposition, err)
	}
	// Settling by the source fence takes over the generation run and commits.
	settled, err := coordinator.SettleDispatchedBySourceFence(context.Background(), SettleDispatchedProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, SourceFence: fence, Outcome: ProbeOutcomeHealthFailure,
	})
	if err != nil || !settled {
		t.Fatalf("dispatch settle = (%v, %v)", settled, err)
	}
	// After a completed outcome the fence disposition is terminal.
	disposition, err = coordinator.SourceFenceSettlementDisposition(context.Background(), SourceFenceDispositionInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, SourceFence: fence,
	})
	if err != nil || disposition.Disposition != ProbeSettlementTerminal || disposition.CompletedOutcome != ProbeOutcomeHealthFailure {
		t.Fatalf("terminal disposition = (%+v, %v)", disposition, err)
	}
	// An unknown fence or generation is terminal too.
	disposition, err = coordinator.SourceFenceSettlementDisposition(context.Background(), SourceFenceDispositionInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation + 1, SourceFence: fence,
	})
	if err != nil || disposition.Disposition != ProbeSettlementTerminal {
		t.Fatalf("generation mismatch disposition = (%+v, %v)", disposition, err)
	}
}

func TestProbeSourceFenceEncoding(t *testing.T) {
	fence := testFence("enc")
	encoded := encodeSourceFence(fence)
	decoded := decodeSourceFence(encoded)
	if len(decoded) != 1 || decoded[0] != fence {
		t.Fatalf("roundtrip = %+v", decoded)
	}
	if len(decodeSourceFence("not json")) != 0 {
		t.Fatalf("garbage must decode to nothing")
	}
	bad := `["state","acc",3,"not-a-uuid"]`
	if len(decodeSourceFence(bad)) != 0 {
		t.Fatalf("invalid uuid must be dropped")
	}
	if AvailabilityProbeRuntimeKey("acc", ProbeKindAccountHealthCheck, 5) != "availability:acc:account_health_check:r5" {
		t.Fatalf("runtime key format failed")
	}
}

// failingProbeStore models a Redis outage.
type failingProbeStore struct{ mockProbeStore }

func (f *failingProbeStore) Get(_ context.Context, _ string) (*ProbeState, error) {
	return nil, errors.New("redis unavailable")
}

func TestProbeStoreErrorsPropagate(t *testing.T) {
	coordinator := NewProbeCoordinator(&failingProbeStore{}, nil, nil)
	if _, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "acc", ProbeKind: ProbeKindAccountHealthCheck, ConfigRevision: 1,
	}); err == nil || !strings.Contains(err.Error(), "redis unavailable") {
		t.Fatalf("expected the store error to propagate, got %v", err)
	}
}

func TestProbeRuntimeKeyRejectsEmptyScope(t *testing.T) {
	coordinator := NewProbeCoordinator(newMockProbeStore(), nil, nil)
	if _, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "   ", ProbeKind: ProbeKindAccountHealthCheck, ConfigRevision: 1,
	}); err == nil || !strings.Contains(err.Error(), "account runtime scope") {
		t.Fatalf("empty scope error = %v", err)
	}
}
