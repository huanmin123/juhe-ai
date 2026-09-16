package gatewaycircuit

// w11c: probe coordinator acquire/settle/release/disposition branches.

import (
	"context"
	"errors"
	"testing"
)

// w11cFailingProbeStore wraps the mock and can fail individual operations.
type w11cFailingProbeStore struct {
	*mockProbeStore
	failGet        bool
	failNextGen    bool
	failSetIfAbsent bool
	failMerge      bool
	failAcquire    bool
	failCommit     bool
	failReplace    bool
}

func (m *w11cFailingProbeStore) Get(ctx context.Context, key string) (*ProbeState, error) {
	if m.failGet {
		return nil, errors.New("w11c get failed")
	}
	return m.mockProbeStore.Get(ctx, key)
}

func (m *w11cFailingProbeStore) NextGeneration(ctx context.Context, key string, retention int64) (int64, error) {
	if m.failNextGen {
		return 0, errors.New("w11c next generation failed")
	}
	return m.mockProbeStore.NextGeneration(ctx, key, retention)
}

func (m *w11cFailingProbeStore) SetIfAbsent(ctx context.Context, state ProbeState, retention int64) (bool, error) {
	if m.failSetIfAbsent {
		return false, errors.New("w11c set if absent failed")
	}
	return m.mockProbeStore.SetIfAbsent(ctx, state, retention)
}

func (m *w11cFailingProbeStore) Merge(ctx context.Context, state ProbeState, retention int64, options ProbeMergeOptions) (*ProbeState, error) {
	if m.failMerge {
		return nil, errors.New("w11c merge failed")
	}
	return m.mockProbeStore.Merge(ctx, state, retention, options)
}

func (m *w11cFailingProbeStore) AcquireGenerationRun(ctx context.Context, key string, generation int64, owner string, leaseUntil int64, retention int64) (*ProbeState, error) {
	if m.failAcquire {
		return nil, errors.New("w11c acquire failed")
	}
	return m.mockProbeStore.AcquireGenerationRun(ctx, key, generation, owner, leaseUntil, retention)
}

func (m *w11cFailingProbeStore) CommitGenerationRun(ctx context.Context, next ProbeState, owner string, retention int64) (bool, error) {
	if m.failCommit {
		return false, errors.New("w11c commit failed")
	}
	return m.mockProbeStore.CommitGenerationRun(ctx, next, owner, retention)
}

func (m *w11cFailingProbeStore) ReplaceSettledGeneration(ctx context.Context, next ProbeState, expected int64, retention int64) (*ProbeState, error) {
	if m.failReplace {
		return nil, errors.New("w11c replace failed")
	}
	return m.mockProbeStore.ReplaceSettledGeneration(ctx, next, expected, retention)
}

func TestW11CProbeAcquireCustomDurationsAndErrors(t *testing.T) {
	ctx := context.Background()
	// Custom lease/retention values flow into the state.
	store := newMockProbeStore()
	coordinator := NewProbeCoordinator(store, func() int64 { return 1000 }, func() string { return "w11c-owner" })
	result, err := coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w11c-scope", ProbeKind: ProbeKindAccountHealthCheck,
		NowMs: int64Ptr(1000), LeaseMs: int64Ptr(5_000), RetentionMs: int64Ptr(60_000),
	})
	if err != nil || result.Disposition != ProbeDispositionOwner {
		t.Fatalf("acquire = (%s, %v)", result.Disposition, err)
	}
	state, err := store.Get(ctx, result.RuntimeKey)
	if err != nil || state == nil || state.ProbeRunUntilMs == nil || *state.ProbeRunUntilMs != 6000 {
		t.Fatalf("state = (%+v, %v)", state, err)
	}

	// Store errors propagate.
	failing := &w11cFailingProbeStore{mockProbeStore: newMockProbeStore()}
	failCoordinator := NewProbeCoordinator(failing, func() int64 { return 1000 }, func() string { return "w11c-owner" })
	failing.failGet = true
	if _, err := failCoordinator.Acquire(ctx, ProbeAcquireInput{AccountRuntimeScope: "w11c-scope"}); err == nil {
		t.Fatalf("expected get error propagation")
	}
	failing.failGet = false
	failing.failNextGen = true
	if _, err := failCoordinator.Acquire(ctx, ProbeAcquireInput{AccountRuntimeScope: "w11c-scope"}); err == nil {
		t.Fatalf("expected next generation error propagation")
	}
	failing.failNextGen = false
	failing.failSetIfAbsent = true
	if _, err := failCoordinator.Acquire(ctx, ProbeAcquireInput{AccountRuntimeScope: "w11c-scope"}); err == nil {
		t.Fatalf("expected setIfAbsent error propagation")
	}
}

func TestW11CProbeAcquireSetIfAbsentLosesThenJoins(t *testing.T) {
	ctx := context.Background()
	store := &w11cRacingProbeStore{mockProbeStore: newMockProbeStore()}
	coordinator := NewProbeCoordinator(store, func() int64 { return 1000 }, func() string { return "w11c-owner" })
	result, err := coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w11c-scope", ProbeKind: ProbeKindAccountHealthCheck,
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// The contender lost the absent write but no stable read exists either:
	// joined with generation 0.
	if result.Disposition != ProbeDispositionJoined || result.Generation != 0 {
		t.Fatalf("contender result = (%s, gen %d)", result.Disposition, result.Generation)
	}
}

// w11cRacingProbeStore simulates another writer winning the absent write and
// the follow-up read seeing nothing.
type w11cRacingProbeStore struct {
	*mockProbeStore
}

func (m *w11cRacingProbeStore) SetIfAbsent(ctx context.Context, state ProbeState, retention int64) (bool, error) {
	return false, nil
}

func TestW11CProbeSourceFenceErrors(t *testing.T) {
	ctx := context.Background()
	failing := &w11cFailingProbeStore{mockProbeStore: newMockProbeStore()}
	coordinator := NewProbeCoordinator(failing, func() int64 { return 1000 }, func() string { return "w11c-owner" })
	failing.failGet = true
	if _, err := coordinator.SourceFences(ctx, "w11c-key", 1); err == nil {
		t.Fatalf("expected source fences get error")
	}
	if _, err := coordinator.GetState(ctx, "w11c-key"); err == nil {
		t.Fatalf("expected get state error")
	}
	failing.failGet = false
	fencing, err := coordinator.SourceFences(ctx, "w11c-key", 42)
	if err != nil || fencing == nil || len(fencing) != 0 {
		t.Fatalf("mismatched generation fences = (%v, %v)", fencing, err)
	}
}

func TestW11CProbeSettleAndReleaseGuards(t *testing.T) {
	ctx := context.Background()
	store := newMockProbeStore()
	coordinator := NewProbeCoordinator(store, func() int64 { return 1000 }, func() string { return "w11c-owner" })
	acquired, err := coordinator.Acquire(ctx, ProbeAcquireInput{AccountRuntimeScope: "w11c-scope"})
	if err != nil || acquired.Disposition != ProbeDispositionOwner {
		t.Fatalf("acquire = (%s, %v)", acquired.Disposition, err)
	}

	// Settle with a mismatched generation or owner is a no-op.
	if settled, err := coordinator.Settle(ctx, SettleProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: 99, OwnerToken: acquired.OwnerToken, Outcome: ProbeOutcomeSuccess,
	}); err != nil || settled {
		t.Fatalf("stale settle = (%v, %v)", settled, err)
	}
	if settled, err := coordinator.Settle(ctx, SettleProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, OwnerToken: "other-owner", Outcome: ProbeOutcomeSuccess,
	}); err != nil || settled {
		t.Fatalf("wrong owner settle = (%v, %v)", settled, err)
	}
	if settled, err := coordinator.Settle(ctx, SettleProbeInput{
		RuntimeKey: "w11c-missing", Generation: 1, OwnerToken: "x", Outcome: ProbeOutcomeSuccess,
	}); err != nil || settled {
		t.Fatalf("missing settle = (%v, %v)", settled, err)
	}
	// Store get failure propagates.
	failing := &w11cFailingProbeStore{mockProbeStore: store}
	failCoordinator := NewProbeCoordinator(failing, func() int64 { return 1000 }, func() string { return "w11c-owner" })
	failing.failGet = true
	if _, err := failCoordinator.Settle(ctx, SettleProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, OwnerToken: acquired.OwnerToken, Outcome: ProbeOutcomeSuccess,
	}); err == nil {
		t.Fatalf("expected settle get error")
	}
	if _, err := failCoordinator.ReleaseForExecution(ctx, ReleaseProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, OwnerToken: acquired.OwnerToken,
	}); err == nil {
		t.Fatalf("expected release get error")
	}
	failing.failGet = false
	failing.failCommit = true
	if _, err := failCoordinator.Settle(ctx, SettleProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, OwnerToken: acquired.OwnerToken, Outcome: ProbeOutcomeSuccess,
	}); err == nil {
		t.Fatalf("expected settle commit error")
	}

	// Release for execution hands the run to the dispatch worker.
	released, err := coordinator.ReleaseForExecution(ctx, ReleaseProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, OwnerToken: acquired.OwnerToken,
		NowMs: int64Ptr(2000), LeaseMs: int64Ptr(10_000), RetentionMs: int64Ptr(60_000),
	})
	if err != nil || !released {
		t.Fatalf("release = (%v, %v)", released, err)
	}
	state, _ := store.Get(ctx, acquired.RuntimeKey)
	if state == nil || state.DispatchPending == nil || !*state.DispatchPending {
		t.Fatalf("dispatch pending not set: %+v", state)
	}
	// A second release over a dispatched state is a no-op.
	if released, err := coordinator.ReleaseForExecution(ctx, ReleaseProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, OwnerToken: acquired.OwnerToken,
	}); err != nil || released {
		t.Fatalf("second release = (%v, %v)", released, err)
	}
	// Settling the dispatched probe by an unrelated fence is a no-op.
	if settled, err := coordinator.SettleDispatchedBySourceFence(ctx, SettleDispatchedProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, SourceFence: testFence("w11c"),
		Outcome: ProbeOutcomeSuccess,
	}); err != nil || settled {
		t.Fatalf("unrelated fence settle = (%v, %v)", settled, err)
	}
}

func TestW11CProbeSettleDispatchedBySourceFenceFlow(t *testing.T) {
	ctx := context.Background()
	store := newMockProbeStore()
	coordinator := NewProbeCoordinator(store, func() int64 { return 1000 }, func() string { return "w11c-owner" })
	fence := testFence("w11c-flow")
	acquired, err := coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w11c-scope", SourceFence: &fence, ExecutionRole: "source_dispatch",
	})
	if err != nil || acquired.Disposition != ProbeDispositionOwner {
		t.Fatalf("acquire = (%s, %v)", acquired.Disposition, err)
	}
	if _, err := coordinator.ReleaseForExecution(ctx, ReleaseProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, OwnerToken: acquired.OwnerToken,
	}); err != nil {
		t.Fatalf("release: %v", err)
	}
	// Settle with a mismatched generation is a no-op.
	if settled, err := coordinator.SettleDispatchedBySourceFence(ctx, SettleDispatchedProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: 99, SourceFence: fence, Outcome: ProbeOutcomeSuccess,
	}); err != nil || settled {
		t.Fatalf("stale fence settle = (%v, %v)", settled, err)
	}
	// Settling with the right fence succeeds.
	settled, err := coordinator.SettleDispatchedBySourceFence(ctx, SettleDispatchedProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, SourceFence: fence,
		Outcome: ProbeOutcomeSuccess, NowMs: int64Ptr(2000),
	})
	if err != nil || !settled {
		t.Fatalf("fence settle = (%v, %v)", settled, err)
	}
	state, _ := store.Get(ctx, acquired.RuntimeKey)
	if state == nil || state.Outcome == nil || *state.Outcome != ProbeOutcomeSuccess {
		t.Fatalf("settled state = %+v", state)
	}
}

func TestW11CProbeSourceFenceDispositionBranches(t *testing.T) {
	ctx := context.Background()
	store := newMockProbeStore()
	coordinator := NewProbeCoordinator(store, func() int64 { return 5000 }, func() string { return "w11c-owner" })
	fence := testFence("w11c-disp")
	acquired, err := coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w11c-scope", SourceFence: &fence, ExecutionRole: "source_dispatch",
	})
	if err != nil || acquired.Disposition != ProbeDispositionOwner {
		t.Fatalf("acquire = (%s, %v)", acquired.Disposition, err)
	}
	input := SourceFenceDispositionInput{RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, SourceFence: fence}

	// Active run window -> retry.
	disposition, err := coordinator.SourceFenceSettlementDisposition(ctx, input)
	if err != nil || disposition.Disposition != ProbeSettlementRetry {
		t.Fatalf("active run = (%+v, %v)", disposition, err)
	}
	// Dispatch pending window -> retry.
	if _, err := coordinator.ReleaseForExecution(ctx, ReleaseProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, OwnerToken: acquired.OwnerToken,
		NowMs: int64Ptr(5000), LeaseMs: int64Ptr(90_000),
	}); err != nil {
		t.Fatalf("release: %v", err)
	}
	disposition, err = coordinator.SourceFenceSettlementDisposition(ctx, input)
	if err != nil || disposition.Disposition != ProbeSettlementRetry {
		t.Fatalf("dispatch pending = (%+v, %v)", disposition, err)
	}
	// Settled outcome -> terminal with the completed outcome (settle on a
	// fresh owner run; the release above already handed the run away).
	fresh, err := coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w11c-scope", NowMs: int64Ptr(200_000),
	})
	if err != nil || fresh.Disposition != ProbeDispositionOwner {
		t.Fatalf("fresh acquire = (%s, %v)", fresh.Disposition, err)
	}
	freshFence := testFence("w11c-fresh")
	if _, err := coordinator.Settle(ctx, SettleProbeInput{
		RuntimeKey: fresh.RuntimeKey, Generation: fresh.Generation, OwnerToken: fresh.OwnerToken,
		Outcome: ProbeOutcomeHealthFailure, NowMs: int64Ptr(200_001),
	}); err != nil {
		t.Fatalf("settle: %v", err)
	}
	disposition, err = coordinator.SourceFenceSettlementDisposition(ctx, SourceFenceDispositionInput{
		RuntimeKey: fresh.RuntimeKey, Generation: fresh.Generation, SourceFence: freshFence,
	})
	if err != nil || disposition.Disposition != ProbeSettlementTerminal {
		t.Fatalf("settled without fence = (%+v, %v)", disposition, err)
	}
	// Unknown fence / generation / missing state -> terminal.
	other := testFence("w11c-other")
	if disposition, err = coordinator.SourceFenceSettlementDisposition(ctx, SourceFenceDispositionInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, SourceFence: other,
	}); err != nil || disposition.Disposition != ProbeSettlementTerminal {
		t.Fatalf("other fence = (%+v, %v)", disposition, err)
	}
	if disposition, err = coordinator.SourceFenceSettlementDisposition(ctx, SourceFenceDispositionInput{
		RuntimeKey: acquired.RuntimeKey, Generation: 77, SourceFence: fence,
	}); err != nil || disposition.Disposition != ProbeSettlementTerminal {
		t.Fatalf("other generation = (%+v, %v)", disposition, err)
	}
	if disposition, err = coordinator.SourceFenceSettlementDisposition(ctx, SourceFenceDispositionInput{
		RuntimeKey: "w11c-missing", Generation: 1, SourceFence: fence,
	}); err != nil || disposition.Disposition != ProbeSettlementTerminal {
		t.Fatalf("missing state = (%+v, %v)", disposition, err)
	}
	// Store failure propagates.
	failing := &w11cFailingProbeStore{mockProbeStore: store}
	failCoordinator := NewProbeCoordinator(failing, func() int64 { return 5000 }, func() string { return "w11c-owner" })
	failing.failGet = true
	if _, err := failCoordinator.SourceFenceSettlementDisposition(ctx, input); err == nil {
		t.Fatalf("expected disposition get error")
	}
}

func TestW11CProbeJoinTakeOverBranches(t *testing.T) {
	ctx := context.Background()
	store := newMockProbeStore()
	coordinator := NewProbeCoordinator(store, func() int64 { return 5000 }, func() string { return "w11c-owner" })
	other := testFence("w11c-first")
	acquired, err := coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w11c-scope", SourceFence: &other, ExecutionRole: "source_dispatch",
	})
	if err != nil || acquired.Disposition != ProbeDispositionOwner {
		t.Fatalf("first acquire = (%s, %v)", acquired.Disposition, err)
	}
	// A second source-dispatch contender merges its fence and joins while the
	// run window is live.
	second := testFence("w11c-second")
	joined, err := coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w11c-scope", SourceFence: &second, ExecutionRole: "source_dispatch",
		NowMs: int64Ptr(6000),
	})
	if err != nil || joined.Disposition != ProbeDispositionJoined {
		t.Fatalf("second acquire = (%s, %v)", joined.Disposition, err)
	}
	// Merged fences are visible through SourceFences.
	fences, err := coordinator.SourceFences(ctx, acquired.RuntimeKey, acquired.Generation)
	if err != nil || len(fences) != 1 {
		t.Fatalf("fences = (%v, %v)", fences, err)
	}
	// After the run window elapses the contender takes over the generation.
	taken, err := coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w11c-scope", NowMs: int64Ptr(200_000),
	})
	if err != nil || taken.Disposition != ProbeDispositionOwner {
		t.Fatalf("takeover = (%s, %v)", taken.Disposition, err)
	}
}

func TestW11CProbeReplaceSettledGenerationRecursion(t *testing.T) {
	ctx := context.Background()
	store := &w11cResettlingProbeStore{mockProbeStore: newMockProbeStore()}
	coordinator := NewProbeCoordinator(store, func() int64 { return 5000 }, func() string { return "w11c-owner" })
	fence := testFence("w11c-replace")
	acquired, err := coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w11c-scope", SourceFence: &fence, ExecutionRole: "source_dispatch",
	})
	if err != nil || acquired.Disposition != ProbeDispositionOwner {
		t.Fatalf("acquire = (%s, %v)", acquired.Disposition, err)
	}
	if _, err := coordinator.Settle(ctx, SettleProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, OwnerToken: acquired.OwnerToken,
		Outcome: ProbeOutcomeSuccess, NowMs: int64Ptr(6000),
	}); err != nil {
		t.Fatalf("settle: %v", err)
	}
	// A source-dispatch acquire replaces the settled generation and reports
	// the replaced settlement.
	replacement, err := coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w11c-scope", SourceFence: &fence, ExecutionRole: "source_dispatch",
		NowMs: int64Ptr(7000),
	})
	if err != nil || replacement.Disposition != ProbeDispositionOwner {
		t.Fatalf("replacement = (%s, %v)", replacement.Disposition, err)
	}
	if replacement.ReplacedFenceSettlement == nil || replacement.ReplacedFenceSettlement.Outcome != ProbeOutcomeSuccess {
		t.Fatalf("replaced settlement = %+v", replacement.ReplacedFenceSettlement)
	}
	// ForceNewGeneration also replaces a settled epoch.
	if _, err := coordinator.Settle(ctx, SettleProbeInput{
		RuntimeKey: replacement.RuntimeKey, Generation: replacement.Generation, OwnerToken: replacement.OwnerToken,
		Outcome: ProbeOutcomeUnknown, NowMs: int64Ptr(8000),
	}); err != nil {
		t.Fatalf("settle 2: %v", err)
	}
	forced, err := coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w11c-scope", ForceNewGeneration: true, NowMs: int64Ptr(9000),
	})
	if err != nil || forced.Disposition != ProbeDispositionOwner {
		t.Fatalf("forced = (%s, %v)", forced.Disposition, err)
	}
}

// w11cResettlingProbeStore keeps ReplaceSettledGeneration functional.
type w11cResettlingProbeStore struct {
	*mockProbeStore
}

func TestW11CProbeStoreErrorPaths(t *testing.T) {
	ctx := context.Background()
	failing := &w11cFailingProbeStore{mockProbeStore: newMockProbeStore()}
	coordinator := NewProbeCoordinator(failing, func() int64 { return 5000 }, func() string { return "w11c-owner" })
	fence := testFence("w11c-err")
	// Merge failure during a fenced acquire.
	failing.failGet = true
	if _, err := coordinator.Acquire(ctx, ProbeAcquireInput{AccountRuntimeScope: "w11c-scope", SourceFence: &fence}); err == nil {
		t.Fatalf("expected acquire get error")
	}
	failing.failGet = false
	failing.failMerge = true
	// Seed a state first so the merge path is reached.
	if _, err := coordinator.Acquire(ctx, ProbeAcquireInput{AccountRuntimeScope: "w11c-scope"}); err != nil {
		t.Fatalf("seed acquire: %v", err)
	}
	failing.failMerge = false
	// AcquireGenerationRun failure on takeover.
	failing.failAcquire = true
	if _, err := coordinator.Acquire(ctx, ProbeAcquireInput{AccountRuntimeScope: "w11c-scope", NowMs: int64Ptr(1_000_000)}); err == nil {
		t.Fatalf("expected takeover acquire error")
	}
	failing.failAcquire = false
	// ReplaceSettledGeneration failure on the replacement path.
	settled := &ProbeState{
		RuntimeKey: AvailabilityProbeRuntimeKey("w11c-scope", "", 1),
		Generation: 3, Outcome: strPtr(ProbeOutcomeSuccess),
	}
	failing.states[settled.RuntimeKey] = settled
	failing.failReplace = true
	if _, err := coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w11c-scope", ForceNewGeneration: true,
	}); err == nil {
		t.Fatalf("expected replace error")
	}
}

func TestW11CProbeSettleDispatchedBySourceFenceErrors(t *testing.T) {
	ctx := context.Background()
	failing := &w11cFailingProbeStore{mockProbeStore: newMockProbeStore()}
	coordinator := NewProbeCoordinator(failing, func() int64 { return 5000 }, func() string { return "w11c-owner" })
	failing.failGet = true
	if _, err := coordinator.SettleDispatchedBySourceFence(ctx, SettleDispatchedProbeInput{
		RuntimeKey: "w11c-key", Generation: 1, SourceFence: testFence("w11c"), Outcome: ProbeOutcomeSuccess,
	}); err == nil {
		t.Fatalf("expected settle dispatch get error")
	}
	failing.failGet = false
	failing.failAcquire = true
	// Seed a dispatch-pending state with the fence recorded.
	fence := testFence("w11c-errs")
	acquired, err := coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w11c-scope", SourceFence: &fence, ExecutionRole: "source_dispatch",
	})
	if err != nil || acquired.Disposition != ProbeDispositionOwner {
		t.Fatalf("acquire = (%s, %v)", acquired.Disposition, err)
	}
	if _, err := coordinator.ReleaseForExecution(ctx, ReleaseProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, OwnerToken: acquired.OwnerToken,
	}); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := coordinator.SettleDispatchedBySourceFence(ctx, SettleDispatchedProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, SourceFence: fence, Outcome: ProbeOutcomeSuccess,
	}); err == nil {
		t.Fatalf("expected acquire generation run error")
	}
}

func TestW11CProbeJoinSettledAndLatestBranches(t *testing.T) {
	ctx := context.Background()
	store := newMockProbeStore()
	coordinator := NewProbeCoordinator(store, func() int64 { return 5000 }, func() string { return "w11c-owner" })
	// A settled state without a completed timestamp joins with retryAt=now.
	runtimeKey := AvailabilityProbeRuntimeKey("w11c-scope", "", 1)
	outcome := ProbeOutcomeSuccess
	store.states[runtimeKey] = &ProbeState{RuntimeKey: runtimeKey, Generation: 2, Outcome: &outcome}
	joined, err := coordinator.Acquire(ctx, ProbeAcquireInput{AccountRuntimeScope: "w11c-scope"})
	if err != nil || joined.Disposition != ProbeDispositionJoined || joined.RetryAtMs != 5000 {
		t.Fatalf("settled join = (%+v, %v)", joined, err)
	}
	// A settled state with a completed timestamp uses it as retryAt.
	completed := int64(1500)
	store.states[runtimeKey].CompletedAtMs = &completed
	joined, err = coordinator.Acquire(ctx, ProbeAcquireInput{AccountRuntimeScope: "w11c-scope"})
	if err != nil || joined.Disposition != ProbeDispositionJoined || joined.RetryAtMs != 1500 {
		t.Fatalf("completed join = (%+v, %v)", joined, err)
	}
	// A live dispatch window forces source-dispatch observers to join.
	pending := true
	until := int64(90_000)
	store.states[runtimeKey] = &ProbeState{
		RuntimeKey: runtimeKey, Generation: 3, DispatchPending: &pending, DispatchPendingUntilMs: &until,
	}
	joined, err = coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w11c-scope", ExecutionRole: "source_dispatch",
	})
	if err != nil || joined.Disposition != ProbeDispositionJoined || joined.RetryAtMs != until {
		t.Fatalf("dispatch join = (%+v, %v)", joined, err)
	}
	// An expired run with a live lease-free state can be taken over; when the
	// acquire returns a foreign state the join falls back to its schedule.
	nextProbe := int64(120_000)
	store.states[runtimeKey] = &ProbeState{RuntimeKey: runtimeKey, Generation: 4, NextProbeAtMs: nextProbe}
	result, err := coordinator.Acquire(ctx, ProbeAcquireInput{AccountRuntimeScope: "w11c-scope"})
	if err != nil || result.Disposition == "" {
		t.Fatalf("fallback acquire = (%+v, %v)", result, err)
	}
}
