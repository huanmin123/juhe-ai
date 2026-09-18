package gatewaycircuit

// w14m 覆盖率补强（w14m 波次）：探针协调器 joinOrTakeOver/replaceSettledGeneration
// 的错误臂与合并尾臂、SettleDispatchedBySourceFence 参数归一化与接管失败臂、
// Settle 保留期归一化、terminal 处置与 settlement 空结果。
//
// w14m 不可达语句登记（保留未删、已甄别为证明不可达的防御臂）：
//   - store_memory.go CompleteConfirmation/AcquireConfirmationLease/
//     CloseSuspectFromObserver 内对 entry.state 的 NormalizeConfirmationFailuresRequired、
//     FailureEvidenceKeysOf、LastFailureEvidenceKey、ConfirmationFailureCountOf 错误臂：
//     入参 state 均已经 freshEntryLocked→normalizeConfirmationState 归一化校验，
//     二次校验不会失败（suspect 相位必归一化，closed 相位这些函数按定义成功）。
//   - types.go FailureEvidenceKeysOf 的 keep<0 臂：required 恒 >=1，keep=required+1 恒 >=2。
//   - jitter.go accountCircuitBackoffDelayMs 的 delay<1 臂：|offset|<=windowMs<=interval/2，
//     delay>=ceil(base/2)>=1。
//   - store_redis.go validateOperationPayload 中 NormalizeConfirmationFailuresRequired(nil,
//     Legacy) 错误臂已按授权删除（w14m 注释标记）。

import (
	"context"
	"errors"
	"testing"
)

// w14mProbeStub 按需覆写 ProbeStateStore 的单个方法，未覆写的方法回退到内嵌基座。
type w14mProbeStub struct {
	ProbeStateStore
	onGet         func(runtimeKey string) (*ProbeState, error)
	onSetIfAbsent func(state ProbeState) (bool, error)
	onNextGen     func() (int64, error)
	onMerge       func(state ProbeState) (*ProbeState, error)
	onAcquireRun  func(generation int64, ownerToken string) (*ProbeState, error)
	onReplace     func(next ProbeState, expectedGeneration int64) (*ProbeState, error)
}

func (s *w14mProbeStub) Get(ctx context.Context, runtimeKey string) (*ProbeState, error) {
	if s.onGet != nil {
		return s.onGet(runtimeKey)
	}
	return s.ProbeStateStore.Get(ctx, runtimeKey)
}

func (s *w14mProbeStub) SetIfAbsent(ctx context.Context, state ProbeState, retention int64) (bool, error) {
	if s.onSetIfAbsent != nil {
		return s.onSetIfAbsent(state)
	}
	return s.ProbeStateStore.SetIfAbsent(ctx, state, retention)
}

func (s *w14mProbeStub) NextGeneration(ctx context.Context, runtimeKey string, retention int64) (int64, error) {
	if s.onNextGen != nil {
		return s.onNextGen()
	}
	return s.ProbeStateStore.NextGeneration(ctx, runtimeKey, retention)
}

func (s *w14mProbeStub) Merge(ctx context.Context, state ProbeState, retention int64, options ProbeMergeOptions) (*ProbeState, error) {
	if s.onMerge != nil {
		return s.onMerge(state)
	}
	return s.ProbeStateStore.Merge(ctx, state, retention, options)
}

func (s *w14mProbeStub) AcquireGenerationRun(ctx context.Context, runtimeKey string, generation int64, ownerToken string, leaseUntil int64, retention int64) (*ProbeState, error) {
	if s.onAcquireRun != nil {
		return s.onAcquireRun(generation, ownerToken)
	}
	return s.ProbeStateStore.AcquireGenerationRun(ctx, runtimeKey, generation, ownerToken, leaseUntil, retention)
}

func (s *w14mProbeStub) ReplaceSettledGeneration(ctx context.Context, next ProbeState, expectedGeneration int64, retention int64) (*ProbeState, error) {
	if s.onReplace != nil {
		return s.onReplace(next, expectedGeneration)
	}
	return s.ProbeStateStore.ReplaceSettledGeneration(ctx, next, expectedGeneration, retention)
}

func TestW14MProbeJoinOrTakeOverGetErrorAfterLostRace(t *testing.T) {
	base := newMockProbeStore()
	getCalls := 0
	stub := &w14mProbeStub{ProbeStateStore: base}
	stub.onGet = func(key string) (*ProbeState, error) {
		getCalls++
		if getCalls == 1 {
			return nil, nil
		}
		return nil, errors.New("w14m-get-boom")
	}
	stub.onSetIfAbsent = func(ProbeState) (bool, error) { return false, nil }
	coordinator := NewProbeCoordinator(stub, func() int64 { return 1_000 }, func() string { return "w14m-owner" })
	if _, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{AccountRuntimeScope: "w14m-lost"}); err == nil {
		t.Fatal("join after lost race must surface store.Get error")
	}
}

func TestW14MProbeMergeErrorSurfaces(t *testing.T) {
	base := newMockProbeStore()
	key := AvailabilityProbeRuntimeKey("w14m-merge-err", ProbeKindAccountHealthCheck, 0)
	base.states[key] = &ProbeState{RuntimeKey: key, Generation: 3, NextProbeAtMs: 900}
	stub := &w14mProbeStub{ProbeStateStore: base}
	stub.onMerge = func(ProbeState) (*ProbeState, error) { return nil, errors.New("w14m-merge-boom") }
	mergeFence := testFence("w14m-merge")
	coordinator := NewProbeCoordinator(stub, func() int64 { return 1_000 }, func() string { return "w14m-owner" })
	if _, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "w14m-merge-err",
		ProbeKind:           ProbeKindAccountHealthCheck,
		SourceFence:         &mergeFence,
	}); err == nil {
		t.Fatal("merge error must surface")
	}
}

func TestW14MProbeMergeSettledThenReplacesGeneration(t *testing.T) {
	// joinOrTakeOver 内 merge 回来的状态已带 Outcome：立即以新代次替换已结算代次。
	base := newMockProbeStore()
	key := AvailabilityProbeRuntimeKey("w14m-late-fence", ProbeKindAccountHealthCheck, 0)
	base.states[key] = &ProbeState{RuntimeKey: key, Generation: 3, NextProbeAtMs: 900}
	stub := &w14mProbeStub{ProbeStateStore: base}
	settled := &ProbeState{
		RuntimeKey:     key,
		Generation:     3,
		NextProbeAtMs:  900,
		Outcome:        strPtr(ProbeOutcomeSuccess),
		CompletedAtMs:  int64Ptr(950),
		SourceFences:   []string{encodeSourceFence(testFence("w14m-late"))},
	}
	stub.onMerge = func(ProbeState) (*ProbeState, error) { return settled, nil }
	stub.onNextGen = func() (int64, error) { return 4, nil }
	stub.onReplace = func(next ProbeState, expectedGeneration int64) (*ProbeState, error) {
		if expectedGeneration != 3 {
			t.Fatalf("expectedGeneration = %d", expectedGeneration)
		}
		clone := next
		base.states[key] = &clone
		replaced := *settled
		return &replaced, nil
	}
	coordinator := NewProbeCoordinator(stub, func() int64 { return 1_000 }, func() string { return "w14m-owner" })
	lateFence := testFence("w14m-late")
	result, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "w14m-late-fence",
		ProbeKind:           ProbeKindAccountHealthCheck,
		ExecutionRole:       "source_dispatch",
		SourceFence:         &lateFence,
	})
	if err != nil {
		t.Fatalf("late fence replace: %v", err)
	}
	if result.Disposition != ProbeDispositionOwner || result.Generation != 4 {
		t.Fatalf("result = %+v", result)
	}
	if result.ReplacedFenceSettlement == nil || result.ReplacedFenceSettlement.Outcome != ProbeOutcomeSuccess {
		t.Fatalf("replaced settlement = %+v", result.ReplacedFenceSettlement)
	}
}

func TestW14MProbeTakeOverReadErrorAndNextProbeRetry(t *testing.T) {
	// 场景一：AcquireGenerationRun 返回 nil 后读取最新状态失败。
	base := newMockProbeStore()
	key := AvailabilityProbeRuntimeKey("w14m-takeover-err", ProbeKindAccountHealthCheck, 0)
	expired := &ProbeState{RuntimeKey: key, Generation: 2, NextProbeAtMs: 100}
	base.states[key] = expired
	stub := &w14mProbeStub{ProbeStateStore: base}
	stub.onAcquireRun = func(int64, string) (*ProbeState, error) { return nil, nil }
	stub.onGet = func(string) (*ProbeState, error) { return nil, errors.New("w14m-read-boom") }
	coordinator := NewProbeCoordinator(stub, func() int64 { return 1_000 }, func() string { return "w14m-owner" })
	if _, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{AccountRuntimeScope: "w14m-takeover-err"}); err == nil {
		t.Fatal("takeover read error must surface")
	}

	// 场景二：接管失败且最新状态只有 NextProbeAtMs → joined 返回该重试时间。
	base2 := newMockProbeStore()
	key2 := AvailabilityProbeRuntimeKey("w14m-takeover-next", ProbeKindAccountHealthCheck, 0)
	base2.states[key2] = &ProbeState{RuntimeKey: key2, Generation: 5, NextProbeAtMs: 5_555}
	stub2 := &w14mProbeStub{ProbeStateStore: base2}
	stub2.onAcquireRun = func(int64, string) (*ProbeState, error) { return nil, nil }
	stub2.onGet = func(string) (*ProbeState, error) {
		clone := *base2.states[key2]
		return &clone, nil
	}
	coordinator2 := NewProbeCoordinator(stub2, func() int64 { return 1_000 }, func() string { return "w14m-owner" })
	result, err := coordinator2.Acquire(context.Background(), ProbeAcquireInput{AccountRuntimeScope: "w14m-takeover-next"})
	if err != nil {
		t.Fatalf("takeover next retry: %v", err)
	}
	if result.Disposition != ProbeDispositionJoined || result.Generation != 5 || result.RetryAtMs != 5_555 {
		t.Fatalf("result = %+v", result)
	}
}

func TestW14MProbeReplaceSettledNilOutcomeRecursionAndJoin(t *testing.T) {
	settledKey := AvailabilityProbeRuntimeKey("w14m-rep-rec", ProbeKindAccountHealthCheck, 0)

	// 场景一：替换返回 nil 且读回仍带 Outcome → 递归替换一次成功。
	base := newMockProbeStore()
	base.states[settledKey] = &ProbeState{RuntimeKey: settledKey, Generation: 1, Outcome: strPtr(ProbeOutcomeSuccess)}
	stub := &w14mProbeStub{ProbeStateStore: base}
	replaceCalls := 0
	stub.onReplace = func(next ProbeState, expected int64) (*ProbeState, error) {
		replaceCalls++
		if replaceCalls == 1 {
			return nil, nil
		}
		clone := next
		base.states[settledKey] = &clone
		replaced := ProbeState{Generation: expected, Outcome: strPtr(ProbeOutcomeHealthFailure)}
		return &replaced, nil
	}
	stub.onNextGen = func() (int64, error) { return 9, nil }
	coordinator := NewProbeCoordinator(stub, func() int64 { return 1_000 }, func() string { return "w14m-owner" })
	result, err := coordinator.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "w14m-rep-rec",
		ProbeKind:           ProbeKindAccountHealthCheck,
		ForceNewGeneration:  true,
	})
	if err != nil {
		t.Fatalf("recursive replace: %v", err)
	}
	if result.Disposition != ProbeDispositionOwner || result.Generation != 9 || replaceCalls != 2 {
		t.Fatalf("result = %+v, replaceCalls = %d", result, replaceCalls)
	}

	// 场景二：替换返回 nil 且读回无 Outcome、租约未到期 → 回到 joinOrTakeOver 加入。
	base2 := newMockProbeStore()
	base2.states[settledKey] = &ProbeState{RuntimeKey: settledKey, Generation: 1, Outcome: strPtr(ProbeOutcomeSuccess)}
	stub2 := &w14mProbeStub{ProbeStateStore: base2}
	stub2.onReplace = func(ProbeState, int64) (*ProbeState, error) { return nil, nil }
	stub2.onGet = func(string) (*ProbeState, error) {
		return &ProbeState{RuntimeKey: settledKey, Generation: 2, ProbeRunUntilMs: int64Ptr(9_000)}, nil
	}
	coordinator2 := NewProbeCoordinator(stub2, func() int64 { return 1_000 }, func() string { return "w14m-owner" })
	joined, err := coordinator2.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "w14m-rep-rec",
		ProbeKind:           ProbeKindAccountHealthCheck,
		ForceNewGeneration:  true,
	})
	if err != nil {
		t.Fatalf("replace join fallback: %v", err)
	}
	if joined.Disposition != ProbeDispositionJoined || joined.Generation != 2 || joined.RetryAtMs != 9_000 {
		t.Fatalf("joined = %+v", joined)
	}

	// 场景三：替换返回 nil 且状态已消失 → joined 返回旧代次。
	base3 := newMockProbeStore()
	base3.states[settledKey] = &ProbeState{RuntimeKey: settledKey, Generation: 6, Outcome: strPtr(ProbeOutcomeSuccess)}
	stub3 := &w14mProbeStub{ProbeStateStore: base3}
	stub3.onReplace = func(ProbeState, int64) (*ProbeState, error) { return nil, nil }
	getCalls3 := 0
	stub3.onGet = func(string) (*ProbeState, error) {
		getCalls3++
		if getCalls3 == 1 {
			clone := *base3.states[settledKey]
			return &clone, nil
		}
		return nil, nil
	}
	coordinator3 := NewProbeCoordinator(stub3, func() int64 { return 1_000 }, func() string { return "w14m-owner" })
	vanished, err := coordinator3.Acquire(context.Background(), ProbeAcquireInput{
		AccountRuntimeScope: "w14m-rep-rec",
		ProbeKind:           ProbeKindAccountHealthCheck,
		ForceNewGeneration:  true,
	})
	if err != nil {
		t.Fatalf("vanished replace: %v", err)
	}
	if vanished.Disposition != ProbeDispositionJoined || vanished.Generation != 6 || vanished.RetryAtMs != 91_000 {
		t.Fatalf("vanished = %+v", vanished)
	}
}

func TestW14MProbeSettleRetentionAndDispatchTakeoverMiss(t *testing.T) {
	ctx := context.Background()
	// Settle 显式提供 RetentionMs → 归一化分支。
	base := newMockProbeStore()
	coordinator := NewProbeCoordinator(base, func() int64 { return 1_000 }, func() string { return "w14m-owner" })
	acquired, err := coordinator.Acquire(ctx, ProbeAcquireInput{AccountRuntimeScope: "w14m-settle-ret"})
	if err != nil || acquired.Disposition != ProbeDispositionOwner {
		t.Fatalf("acquire = (%+v, %v)", acquired, err)
	}
	retention := int64(60_000)
	settled, err := coordinator.Settle(ctx, SettleProbeInput{
		RuntimeKey:  acquired.RuntimeKey,
		Generation:  acquired.Generation,
		OwnerToken:  acquired.OwnerToken,
		Outcome:     ProbeOutcomeSuccess,
		RetentionMs: &retention,
	})
	if err != nil || !settled {
		t.Fatalf("settle = (%v, %v)", settled, err)
	}

	// SettleDispatchedBySourceFence：显式 Lease/Retention，接管得到他人租约 → false。
	key := AvailabilityProbeRuntimeKey("w14m-dispatch-miss", ProbeKindCodexSourceAvoidance, 0)
	base.states[key] = &ProbeState{
		RuntimeKey:             key,
		Generation:             2,
		DispatchPending:        boolPtr(true),
		DispatchPendingUntilMs: int64Ptr(2_000),
		SourceFences:           []string{encodeSourceFence(testFence("w14m-miss"))},
	}
	stub := &w14mProbeStub{ProbeStateStore: base}
	stub.onAcquireRun = func(generation int64, ownerToken string) (*ProbeState, error) {
		clone := *base.states[key]
		clone.ProbeRunID = strPtr("someone-else")
		return &clone, nil
	}
	coordinator2 := NewProbeCoordinator(stub, func() int64 { return 1_000 }, func() string { return "w14m-dispatch-owner" })
	lease := int64(5_000)
	ok, err := coordinator2.SettleDispatchedBySourceFence(ctx, SettleDispatchedProbeInput{
		RuntimeKey:  key,
		Generation:  2,
		SourceFence: testFence("w14m-miss"),
		Outcome:     ProbeOutcomeSuccess,
		LeaseMs:     &lease,
		RetentionMs: &retention,
	})
	if err != nil || ok {
		t.Fatalf("dispatch settle miss = (%v, %v)", ok, err)
	}
}

func TestW14MProbeSourceFenceTerminalAndNilSettlement(t *testing.T) {
	ctx := context.Background()
	key := AvailabilityProbeRuntimeKey("w14m-terminal", ProbeKindCodexSourceAvoidance, 0)
	base := newMockProbeStore()
	base.states[key] = &ProbeState{
		RuntimeKey:     key,
		Generation:     1,
		ProbeRunUntilMs: int64Ptr(500),
		SourceFences:   []string{encodeSourceFence(testFence("w14m-term"))},
	}
	coordinator := NewProbeCoordinator(base, func() int64 { return 1_000 }, func() string { return "w14m-owner" })
	disposition, err := coordinator.SourceFenceSettlementDisposition(ctx, SourceFenceDispositionInput{
		RuntimeKey:  key,
		Generation:  1,
		SourceFence: testFence("w14m-term"),
	})
	if err != nil || disposition.Disposition != ProbeSettlementTerminal {
		t.Fatalf("disposition = (%+v, %v)", disposition, err)
	}
	if settlementFromReplacedGeneration(ProbeState{Generation: 1}) != nil {
		t.Fatal("unsettled replaced generation must not produce a settlement")
	}
}
