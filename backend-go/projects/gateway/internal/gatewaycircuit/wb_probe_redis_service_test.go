package gatewaycircuit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// wbMemoryProbeStore 是 ProbeStateStore 的最小内存实现，语义对齐
// 共享 runtime-probe-state-store（代际单调、运行租约围栏、结算替换）。
type wbMemoryProbeStore struct {
	mu          sync.Mutex
	states      map[string]*ProbeState
	generations map[string]int64
}

func newWbProbeStore() *wbMemoryProbeStore {
	return &wbMemoryProbeStore{states: map[string]*ProbeState{}, generations: map[string]int64{}}
}

func (s *wbMemoryProbeStore) Get(_ context.Context, runtimeKey string) (*ProbeState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, ok := s.states[runtimeKey]; ok {
		clone := *state
		return &clone, nil
	}
	return nil, nil
}

func (s *wbMemoryProbeStore) NextGeneration(_ context.Context, runtimeKey string, _ int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generations[runtimeKey]++
	return s.generations[runtimeKey], nil
}

func (s *wbMemoryProbeStore) SetIfAbsent(_ context.Context, state ProbeState, _ int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.states[state.RuntimeKey]; ok {
		return false, nil
	}
	clone := state
	s.states[state.RuntimeKey] = &clone
	return true, nil
}

func (s *wbMemoryProbeStore) Merge(_ context.Context, state ProbeState, _ int64, options ProbeMergeOptions) (*ProbeState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.states[state.RuntimeKey]
	if !ok {
		clone := state
		s.states[state.RuntimeKey] = &clone
		return &clone, nil
	}
	merged := *current
	merged.Generation = state.Generation
	if state.ProbeRunID != nil && (merged.ProbeRunID == nil || !containsString(options.PreserveCurrentFields, "probeRunId")) {
		merged.ProbeRunID = state.ProbeRunID
	}
	if state.NextProbeAtMs != 0 {
		merged.NextProbeAtMs = state.NextProbeAtMs
	}
	fences := append([]string{}, merged.SourceFences...)
	for _, fence := range state.SourceFences {
		if !containsString(fences, fence) {
			fences = append(fences, fence)
		}
	}
	for _, union := range options.UnionArrayFields {
		if union.Field == "sourceFences" && len(fences) > union.MaxItems {
			fences = fences[:union.MaxItems]
		}
	}
	merged.SourceFences = fences
	s.states[state.RuntimeKey] = &merged
	clone := merged
	return &clone, nil
}

func (s *wbMemoryProbeStore) AcquireGenerationRun(_ context.Context, runtimeKey string, generation int64, ownerToken string, leaseUntilMs int64, _ int64) (*ProbeState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.states[runtimeKey]
	if !ok || current.Generation != generation {
		return nil, nil
	}
	if current.ProbeRunID != nil && current.ProbeRunUntilMs != nil && *current.ProbeRunUntilMs > leaseUntilMs-90_000 && *current.ProbeRunID != ownerToken {
		clone := *current
		return &clone, nil
	}
	next := *current
	next.ProbeRunID = strPtr(ownerToken)
	next.ProbeRunUntilMs = int64Ptr(leaseUntilMs)
	s.states[runtimeKey] = &next
	clone := next
	return &clone, nil
}

func (s *wbMemoryProbeStore) CommitGenerationRun(_ context.Context, next ProbeState, ownerToken string, _ int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.states[next.RuntimeKey]
	if !ok || current.Generation != next.Generation || current.ProbeRunID == nil || *current.ProbeRunID != ownerToken {
		return false, nil
	}
	clone := next
	s.states[next.RuntimeKey] = &clone
	return true, nil
}

func (s *wbMemoryProbeStore) ReplaceSettledGeneration(_ context.Context, next ProbeState, expectedGeneration int64, _ int64) (*ProbeState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.states[next.RuntimeKey]
	if !ok || current.Generation != expectedGeneration || current.Outcome == nil {
		return nil, nil
	}
	replaced := *current
	clone := next
	s.states[next.RuntimeKey] = &clone
	return &replaced, nil
}

// 探测协调器契约：新代际成为 owner；活跃租约期内后来者 join；
// 租约过期后可接管；结算后的代际只能被整体替换。
func TestWBProbeCoordinatorOwnershipLifecycle(t *testing.T) {
	now := int64(10_000)
	clock := &now
	store := newWbProbeStore()
	coordinator := NewProbeCoordinator(store, func() int64 { return *clock }, func() string { return "owner-1" })
	result, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "acc", ProbeKind: ProbeKindAccountHealthCheck, ConfigRevision: 3, NowMs: clock,
	})
	if err != nil || result.Disposition != ProbeDispositionOwner || result.Generation != 1 {
		t.Fatalf("first acquire = %+v err=%v", result, err)
	}
	// 租约期内加入。
	joiner := NewProbeCoordinator(store, func() int64 { return *clock }, func() string { return "joiner" })
	joined, err := joiner.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "acc", ProbeKind: ProbeKindAccountHealthCheck, ConfigRevision: 3, NowMs: clock,
	})
	if err != nil || joined.Disposition != ProbeDispositionJoined || joined.Generation != 1 {
		t.Fatalf("join acquire = %+v err=%v", joined, err)
	}
	// 租约过期后接管。
	*clock += defaultProbeLeaseMs + 1
	takeover := NewProbeCoordinator(store, func() int64 { return *clock }, func() string { return "owner-2" })
	taken, err := takeover.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "acc", ProbeKind: ProbeKindAccountHealthCheck, ConfigRevision: 3, NowMs: clock,
	})
	if err != nil || taken.Disposition != ProbeDispositionOwner || taken.OwnerToken != "owner-2" {
		t.Fatalf("takeover = %+v err=%v", taken, err)
	}
	// 结算后 ForceNewGeneration 必须替换已结算代际并带回旧结算。
	if settled, err := coordinator.Settle(context.Background(), SettleProbeInput{
		RuntimeKey: taken.RuntimeKey, Generation: taken.Generation, OwnerToken: "owner-2",
		Outcome: ProbeOutcomeSuccess, NowMs: clock,
	}); err != nil || !settled {
		t.Fatalf("settle = (%v, %v)", settled, err)
	}
	replacement := NewProbeCoordinator(store, func() int64 { return *clock }, func() string { return "owner-3" })
	replaced, err := replacement.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "acc", ProbeKind: ProbeKindAccountHealthCheck, ConfigRevision: 3,
		NowMs: clock, ForceNewGeneration: true, ExecutionRole: "source_dispatch",
		SourceFence: &ProbeSourceFence{StateKey: "sk", AccountID: "acc", SourceGeneration: 1, SourceFenceID: "0d7f7f7f-7f7f-4f7f-8f7f-7f7f7f7f7f7f"},
	})
	if err != nil || replaced.Disposition != ProbeDispositionOwner {
		t.Fatalf("replacement = %+v err=%v", replaced, err)
	}
	if replaced.ReplacedFenceSettlement == nil || replaced.ReplacedFenceSettlement.Outcome != ProbeOutcomeSuccess {
		t.Fatalf("replaced settlement = %+v", replaced.ReplacedFenceSettlement)
	}
}

// 源围栏契约：围栏列表按代际读取，非法编码被丢弃；测试存储替换生效。
func TestWBProbeSourceFencesAndTestStoreSwap(t *testing.T) {
	primary := newWbProbeStore()
	coordinator := NewProbeCoordinator(primary, func() int64 { return 1_000 }, func() string { return "owner" })
	secondary := newWbProbeStore()
	coordinator.SetStoreForTest(secondary)
	fence := ProbeSourceFence{StateKey: "sk", AccountID: "acc", SourceGeneration: 2, SourceFenceID: "0d7f7f7f-7f7f-4f7f-8f7f-7f7f7f7f7f7f"}
	result, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "acc", ProbeKind: ProbeKindCodexSourceAvoidance, ConfigRevision: 1,
		SourceFence: &fence,
	})
	if err != nil || result.Disposition != ProbeDispositionOwner {
		t.Fatalf("acquire = %+v err=%v", result, err)
	}
	fences, err := coordinator.SourceFences(context.Background(), result.RuntimeKey, result.Generation)
	if err != nil || len(fences) != 1 || fences[0].SourceFenceID != fence.SourceFenceID {
		t.Fatalf("fences = %+v err=%v", fences, err)
	}
	// 代际不匹配必须返回空列表。
	if fences, err := coordinator.SourceFences(context.Background(), result.RuntimeKey, result.Generation+1); err != nil || len(fences) != 0 {
		t.Fatalf("代际不匹配围栏 = %+v err=%v", fences, err)
	}
	if normalizedDuration(0) != 1 || normalizedRevision(0) != 1 {
		t.Fatal("归一化下限必须为 1")
	}
}

// 结算与派发围栏契约：Release 后只能被同围栏结算；围栏处置区分重试与终态。
func TestWBProbeDispatchFenceSettlement(t *testing.T) {
	now := int64(20_000)
	clock := &now
	store := newWbProbeStore()
	coordinator := NewProbeCoordinator(store, func() int64 { return *clock }, func() string { return "owner" })
	acquireFence := ProbeSourceFence{StateKey: "sk", AccountID: "acc", SourceGeneration: 1, SourceFenceID: "0d7f7f7f-7f7f-4f7f-8f7f-7f7f7f7f7f7f"}
	acquired, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "acc", ProbeKind: ProbeKindCodexSourceAvoidance, ConfigRevision: 1,
		NowMs: clock, SourceFence: &acquireFence,
	})
	if err != nil || acquired.Disposition != ProbeDispositionOwner {
		t.Fatalf("acquire = %+v err=%v", acquired, err)
	}
	released, err := coordinator.ReleaseForExecution(context.Background(), ReleaseProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, OwnerToken: acquired.OwnerToken, NowMs: clock,
	})
	if err != nil || !released {
		t.Fatalf("release = (%v, %v)", released, err)
	}
	fence := ProbeSourceFence{StateKey: "sk", AccountID: "acc", SourceGeneration: 1, SourceFenceID: "0d7f7f7f-7f7f-4f7f-8f7f-7f7f7f7f7f7f"}
	disposition, err := coordinator.SourceFenceSettlementDisposition(context.Background(), SourceFenceDispositionInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, SourceFence: fence, NowMs: clock,
	})
	if err != nil || disposition.Disposition != ProbeSettlementRetry {
		t.Fatalf("disposition = %+v err=%v", disposition, err)
	}
	settled, err := coordinator.SettleDispatchedBySourceFence(context.Background(), SettleDispatchedProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, SourceFence: fence,
		Outcome: ProbeOutcomeSuccess, NowMs: clock,
	})
	if err != nil || !settled {
		t.Fatalf("fenced settle = (%v, %v)", settled, err)
	}
	final, err := coordinator.SourceFenceSettlementDisposition(context.Background(), SourceFenceDispositionInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, SourceFence: fence, NowMs: clock,
	})
	if err != nil || final.Disposition != ProbeSettlementTerminal || final.CompletedOutcome != ProbeOutcomeSuccess {
		t.Fatalf("final disposition = %+v err=%v", final, err)
	}
}

// nopLogger 必须是安全 no-op，供生产默认接线使用。
func TestWBNopLoggerIsSafeNoOp(t *testing.T) {
	NopLogger.Info(map[string]any{"k": "v"}, "info")
	NopLogger.Warn(nil, "warn")
}

// Redis 存储确认生命周期契约：确认租约、完成与键轮换关闭在 Redis 上
// 与内存存储保持同语义，并覆盖 ReplaceDispatchRevision。
func TestWBRedisStoreConfirmationAndReplaceRevision(t *testing.T) {
	now := int64(0)
	clock := &now
	store, _ := newTestRedisStore(t, 100, func() int64 { return *clock })
	ctx := context.Background()
	scope := accountScope("acc")
	if _, err := store.Suspect(ctx, SuspectInput{
		Scope: scope, DispatchRevision: "7", TransitionID: "s1",
		Reason: "transport:connect failed", NowMs: clock,
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	// 疑态重试时间到达后才能申请确认租约。
	*clock = 3000
	acquireInput := AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "a1",
		LeaseID: "lease-1", LeaseUntilMs: 33_000, NowMs: clock,
	}
	acquireInput.ConfirmationEvidenceKey = strPtr(strings.Repeat("b", 64))
	acquired, err := store.AcquireConfirmationLease(ctx, acquireInput)
	if err != nil || acquired.Status != MutationApplied {
		t.Fatalf("acquire = (%s, %v)", acquired.Status, err)
	}
	// framing_complete + closed 处置把怀疑态推进到关闭。
	completed, err := store.CompleteConfirmation(ctx, CompleteConfirmationInput{
		Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "c1",
		LeaseID: "lease-1", Outcome: OutcomeFramingComplete,
		FramingCompleteDisposition: strPtr("closed"), NowMs: clock,
	})
	if err != nil || (completed.State.Phase != PhaseClosed && completed.State.Phase != PhaseRecovering) {
		t.Fatalf("complete = (%+v, %v)", completed, err)
	}
	// ReplaceDispatchRevision：终态下推进账本修订。
	replaced, err := store.ReplaceDispatchRevision(ctx, ReplaceDispatchRevisionInput{
		Scope: scope, DispatchRevision: "9", TransitionID: "r1", NowMs: clock,
	})
	if err != nil || replaced.Status != MutationApplied || replaced.State.DispatchRevision != "9" {
		t.Fatalf("replace = (%+v, %v)", replaced, err)
	}
	// 再次怀疑后用键轮换关闭：期望证据键必须匹配最后失败证据。
	suspect2, err := store.Suspect(ctx, SuspectInput{
		Scope: scope, DispatchRevision: "9", TransitionID: "s2",
		Reason: "transport:connect failed", FailureEvidenceKey: strPtr(strings.Repeat("a", 64)), NowMs: clock,
	})
	if err != nil || suspect2.State.Phase != PhaseSuspect {
		t.Fatalf("suspect 2 = (%+v, %v)", suspect2, err)
	}
	closed, err := store.CloseSuspectFromKeyRotation(ctx, CloseSuspectFromKeyRotationInput{
		Scope: scope, Generation: suspect2.State.Generation, DispatchRevision: "9", TransitionID: "k1",
		ExpectedFailureEvidenceKey: strings.Repeat("a", 64), NowMs: clock,
	})
	if err != nil || closed.State.Phase != PhaseClosed {
		t.Fatalf("key-rotation close = (%+v, %v)", closed, err)
	}
}

// CompleteConfirmation 升级契约：确认失败达到阈值后协议模型作用域开启，
// 账户作用域被父证据升级并广播 related 状态。
func TestWBServiceConfirmationFailureEscalatesToAccount(t *testing.T) {
	now := int64(0)
	clock := &now
	store := newNonExpiringMemoryStore(t)
	var mu sync.Mutex
	mutations := 0
	service, _ := newTestService(t, store, ServiceOptions{
		Now: func() int64 { return *clock },
		OnMutation: func(context.Context, MutationEvent) error {
			mu.Lock()
			mutations++
			mu.Unlock()
			return nil
		},
	})
	if _, err := service.SuspectForegroundFailure(context.Background(), suspectForegroundInput{
		scope:                        protocolModelScope(testAccount(), LaneText, strPtr("gpt-4o")),
		dispatchRevision:             revisionOf(t, testAccount()),
		confirmationFailuresRequired: int64Ptr(1),
		reason:                       "transport:connect failed",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 3000
	result, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: strPtr(strings.Repeat("b", 64)),
	})
	if err != nil || !result.Attempt.IsConfirmation() {
		t.Fatalf("confirmation prepare = %+v err=%v", result, err)
	}
	decision, err := result.Attempt.ReportTransportFailure(context.Background(), TransportFailure{
		Kind: TransportFailureKindTransport, Reason: "connect refused",
	})
	if err != nil {
		t.Fatalf("transport failure: %v", err)
	}
	// 单次确认失败即开启协议模型作用域：结算返回 blocked 并携带 OPEN 状态。
	if decision.Outcome != DecisionBlocked || decision.State.Phase != PhaseOpen {
		t.Fatalf("decision = (%s, %+v)", decision.Outcome, decision.State)
	}
	mu.Lock()
	defer mu.Unlock()
	if mutations == 0 {
		t.Fatal("升级必须广播变更事件")
	}
}

// OnMutation 失败必须让变更调用方失败关闭。
func TestWBServiceNotifyMutationErrorPropagates(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{
		OnMutation: func(context.Context, MutationEvent) error { return errors.New("事件订阅方失败") },
	})
	_, err := service.SuspectForegroundFailure(context.Background(), suspectForegroundInput{
		scope:            protocolModelScope(testAccount(), LaneText, strPtr("gpt-4o")),
		dispatchRevision: revisionOf(t, testAccount()),
		reason:           "transport:connect failed",
	})
	if err == nil || !strings.Contains(err.Error(), "事件订阅方失败") {
		t.Fatalf("订阅方失败必须上抛: %v", err)
	}
}

// 父作用域漂移契约：确认租约建立后父账户开启/换修订必须释放租约并阻塞。
func TestWBServiceReleasesConfirmationOnParentDrift(t *testing.T) {
	now := int64(0)
	clock := &now
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	if _, err := service.SuspectForegroundFailure(context.Background(), suspectForegroundInput{
		scope:                        protocolModelScope(testAccount(), LaneText, strPtr("gpt-4o")),
		dispatchRevision:             revisionOf(t, testAccount()),
		confirmationFailuresRequired: int64Ptr(2),
		reason:                       "transport:connect failed",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 3000
	result, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: strPtr(strings.Repeat("b", 64)),
	})
	if err != nil || !result.Attempt.IsConfirmation() {
		t.Fatalf("confirmation prepare = %+v err=%v", result, err)
	}
	confirmation := *result.Attempt.confirmation
	// 父账户开启 → 子作用域携带同一确认的请求必须被阻塞并释放租约。
	parent := ClosedState(accountScope("acc"), revisionOf(t, testAccount()), 1, "p1", 3000)
	parent.Phase = PhaseOpen
	if _, err := store.Restore(context.Background(), parent, int64Ptr(3000)); err != nil {
		t.Fatal(err)
	}
	blocked, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, Confirmation: &confirmation,
	})
	if err != nil || blocked.Outcome != PrepareBlocked {
		t.Fatalf("parent drift prepare = %+v err=%v", blocked, err)
	}
	// releaseAcquiredConfirmation 以 unknown 结果结算，父账户 OPEN 不得被子请求穿透。
	state, err := store.Get(context.Background(), confirmation.Scope, int64Ptr(3000))
	if err != nil || state.Phase != PhaseSuspect {
		t.Fatalf("子作用域必须保持怀疑态 = (%+v, %v)", state, err)
	}
}
