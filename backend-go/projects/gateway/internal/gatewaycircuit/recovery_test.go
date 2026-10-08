package gatewaycircuit

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// 恢复 sweep 状态机测试。语义参照 jobs 侧 circuitrecovery_test.go（类型独立，
// 不复用其 helper）；store 用真实 MemoryStore，探针以函数注入（Mock 优先）。

func recoveryTestClock(t *testing.T) (func() int64, func(int64)) {
	t.Helper()
	current := int64(1_000_000)
	return func() int64 { return current }, func(next int64) { current = next }
}

func recoverySuspectState(t *testing.T, scope Scope, generation int64, dispatchRevision string, nowMS int64) State {
	t.Helper()
	scopeKey, err := ScopeKey(scope)
	if err != nil {
		t.Fatalf("构造 scopeKey 失败: %v", err)
	}
	required := int64(2)
	count := int64(0)
	retryAt := nowMS
	return State{
		ScopeKey:                     scopeKey,
		Scope:                        scope,
		Phase:                        PhaseSuspect,
		Generation:                   generation,
		DispatchRevision:             dispatchRevision,
		TransitionID:                 "seed-suspect",
		ConfirmationFailuresRequired: &required,
		ConfirmationFailureCount:     &count,
		RetryAtMs:                    &retryAt,
		UpdatedAtMs:                  nowMS,
	}
}

func recoveryOpenState(t *testing.T, scope Scope, generation int64, dispatchRevision string, nowMS int64) State {
	t.Helper()
	state := recoverySuspectState(t, scope, generation, dispatchRevision, nowMS)
	state.Phase = PhaseOpen
	state.BackoffAttempt = 1
	return state
}

func recoveryFramingCompleteOutcome() TransportProbeOutcome {
	status := 200
	return TransportProbeOutcome{Kind: RecoveryProbeOutcomeFramingComplete, StatusCode: &status}
}

func recoveryStaticResolver(target RecoveryProbeTarget, found bool) RecoveryTargetResolver {
	return func(context.Context, State) (RecoveryProbeTarget, bool, error) {
		return target, found, nil
	}
}

func newTestRecoveryService(t *testing.T, store Store, nowMS func() int64, resolver RecoveryTargetResolver) *RecoveryService {
	t.Helper()
	counter := 0
	service, err := NewRecoveryService(store, resolver, RecoveryServiceOptions{
		BatchSize:       10,
		Concurrency:     2,
		LeaseDurationMS: 60_000,
		NowMS:           nowMS,
		CreateID: func() string {
			counter++
			return fmt.Sprintf("id-%d", counter)
		},
	})
	if err != nil {
		t.Fatalf("构造恢复服务失败: %v", err)
	}
	return service
}

// 正常恢复：SUSPECT 到点 → 确认租约 → framing_complete（disposition=closed）
// → CLOSED；每次 store mutation 触发 OnMutation。
func TestRecoverySweepFramingCompleteClosesSuspect(t *testing.T) {
	nowMS, advance := recoveryTestClock(t)
	store := newTestMemoryStore(t, 10, nowMS)
	seed := recoverySuspectState(t, accountScope("acc-recovery-1"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, int64Ptr(nowMS())); err != nil {
		t.Fatal(err)
	}
	var operations []CircuitOperation
	service := newTestRecoveryService(t, store, nowMS, recoveryStaticResolver(RecoveryProbeTarget{
		DispatchRevision: "5",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return recoveryFramingCompleteOutcome(), nil
		},
	}, true))
	service.onMutation = func(_ context.Context, event MutationEvent) error {
		operations = append(operations, event.Operation)
		return nil
	}

	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep 失败: %v", err)
	}
	if result.DueCount != 1 || result.LeasedCount != 1 || result.FramingCompleteCount != 1 {
		t.Fatalf("计数不符: %+v", result)
	}
	state, err := store.Get(context.Background(), accountScope("acc-recovery-1"), int64Ptr(nowMS()))
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != PhaseClosed {
		t.Fatalf("SUSPECT 探针达标后应 CLOSED，got %s", state.Phase)
	}
	// acquire_confirmation 与 complete_confirmation 都投影。
	if len(operations) != 2 ||
		operations[0] != OperationAcquireConfirmation ||
		operations[1] != OperationCompleteConfirmation {
		t.Fatalf("mutation 序列不符: %v", operations)
	}
	advance(nowMS() + 1)
}

// 端到端金丝雀链：OPEN 到点 → canary framing_complete → 进入 RECOVERING
// （首次进入 success 归零，store 既有语义）→ 到点 canary 累计 success
// （1/3 → 2/3 → 3/3）→ CLOSED。
func TestRecoveryOpenCanaryChainAdvancesToClosed(t *testing.T) {
	nowMS, advance := recoveryTestClock(t)
	store := newTestMemoryStore(t, 10, nowMS)
	seed := recoveryOpenState(t, accountScope("acc-recovery-2"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, int64Ptr(nowMS())); err != nil {
		t.Fatal(err)
	}
	service := newTestRecoveryService(t, store, nowMS, recoveryStaticResolver(RecoveryProbeTarget{
		DispatchRevision: "5",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return recoveryFramingCompleteOutcome(), nil
		},
	}, true))

	// 第一轮：OPEN → HALF_OPEN canary → framing_complete → RECOVERING(0)。
	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("第一轮 Sweep 失败: %v", err)
	}
	if result.FramingCompleteCount != 1 {
		t.Fatalf("第一轮 framingCompleteCount = %d: %+v", result.FramingCompleteCount, result)
	}
	scope := accountScope("acc-recovery-2")
	state, _ := store.Get(context.Background(), scope, int64Ptr(nowMS()))
	if state.Phase != PhaseRecovering || state.RecoverySuccessCount != 0 {
		t.Fatalf("第一轮后应 RECOVERING(0)，got %s(%d)", state.Phase, state.RecoverySuccessCount)
	}
	// 之后三轮：金丝雀租约（60s）到期后再次 due，success 累计到 3 闭合。
	for want := int64(1); want <= 3; want++ {
		advance(nowMS() + 61_000)
		if _, err := service.Sweep(context.Background()); err != nil {
			t.Fatalf("success=%d 轮 Sweep 失败: %v", want, err)
		}
		state, _ = store.Get(context.Background(), scope, int64Ptr(nowMS()))
		if want < 3 {
			if state.Phase != PhaseRecovering || state.RecoverySuccessCount != want {
				t.Fatalf("success=%d 轮后应 RECOVERING(%d)，got %s(%d)", want, want, state.Phase, state.RecoverySuccessCount)
			}
			continue
		}
		if state.Phase != PhaseClosed {
			t.Fatalf("三次金丝雀成功后应 CLOSED，got %s", state.Phase)
		}
	}
}

// OPEN 账号 transport_incomplete：重新 OPEN 进退避阶梯（BackoffAttempt+1），
// reason 记 background_probe:<kind>[:http_<status>]。
func TestRecoveryTransportIncompleteReopensWithBackoff(t *testing.T) {
	nowMS, advance := recoveryTestClock(t)
	store := newTestMemoryStore(t, 10, nowMS)
	seed := recoveryOpenState(t, accountScope("acc-recovery-3"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, int64Ptr(nowMS())); err != nil {
		t.Fatal(err)
	}
	status := 502
	service := newTestRecoveryService(t, store, nowMS, recoveryStaticResolver(RecoveryProbeTarget{
		DispatchRevision: "5",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return TransportProbeOutcome{Kind: RecoveryProbeOutcomeTransportIncomplete, FailureKind: RecoveryProbeFailureTimeout, StatusCode: &status}, nil
		},
	}, true))

	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep 失败: %v", err)
	}
	if result.TransportIncompleteCount != 1 {
		t.Fatalf("transportIncompleteCount = %d: %+v", result.TransportIncompleteCount, result)
	}
	state, _ := store.Get(context.Background(), accountScope("acc-recovery-3"), int64Ptr(nowMS()))
	if state.Phase != PhaseOpen {
		t.Fatalf("transport_failure 应重新 OPEN，got %s", state.Phase)
	}
	if state.BackoffAttempt != 2 {
		t.Fatalf("退避尝试应从 1 递增到 2: %d", state.BackoffAttempt)
	}
	if state.RetryAtMs == nil || *state.RetryAtMs <= nowMS() {
		t.Fatalf("必须带未来退避 deadline: %+v", state.RetryAtMs)
	}
	if state.Lease != nil {
		t.Fatal("重新 OPEN 后不应残留租约")
	}
	wantReason := "background_probe:timeout:http_502"
	if state.FailureReason == nil || *state.FailureReason != wantReason {
		t.Fatalf("failureReason = %v, want %q", state.FailureReason, wantReason)
	}
	advance(nowMS() + 1)
}

// SUSPECT 确认 transport_incomplete：记确认失败证据（1/2）并保持 SUSPECT，
// evidence key = sha256(background_confirmation:scopeKey:generation:leaseId)。
func TestRecoveryTransportIncompleteRecordsConfirmationEvidence(t *testing.T) {
	nowMS, advance := recoveryTestClock(t)
	store := newTestMemoryStore(t, 10, nowMS)
	seed := recoverySuspectState(t, accountScope("acc-recovery-4"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, int64Ptr(nowMS())); err != nil {
		t.Fatal(err)
	}
	status := 502
	service := newTestRecoveryService(t, store, nowMS, recoveryStaticResolver(RecoveryProbeTarget{
		DispatchRevision: "5",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return TransportProbeOutcome{Kind: RecoveryProbeOutcomeTransportIncomplete, FailureKind: RecoveryProbeFailureTimeout, StatusCode: &status}, nil
		},
	}, true))

	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep 失败: %v", err)
	}
	if result.TransportIncompleteCount != 1 {
		t.Fatalf("transportIncompleteCount = %d", result.TransportIncompleteCount)
	}
	state, _ := store.Get(context.Background(), accountScope("acc-recovery-4"), int64Ptr(nowMS()))
	if state.Phase != PhaseSuspect {
		t.Fatalf("确认失败(1/2)应保持 SUSPECT，got %s", state.Phase)
	}
	if state.ConfirmationFailureCount == nil || *state.ConfirmationFailureCount != 1 {
		t.Fatalf("confirmationFailureCount 应为 1: %+v", state.ConfirmationFailureCount)
	}
	// 首个 createID 消耗在 leaseId（id-1）。
	wantKey := BackgroundConfirmationEvidenceKey(seed, "id-1")
	if len(state.FailureEvidenceKeys) != 1 || state.FailureEvidenceKeys[0] != wantKey {
		t.Fatalf("应写入 1 条失败证据: %v", state.FailureEvidenceKeys)
	}
	advance(nowMS() + 1)
}

// unknown 中性：OPEN canary unknown → 回原相位（restore origin）并释放租约。
func TestRecoveryUnknownNeutralCanaryRestoresOrigin(t *testing.T) {
	nowMS, advance := recoveryTestClock(t)
	store := newTestMemoryStore(t, 10, nowMS)
	seed := recoveryOpenState(t, accountScope("acc-recovery-5"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, int64Ptr(nowMS())); err != nil {
		t.Fatal(err)
	}
	service := newTestRecoveryService(t, store, nowMS, recoveryStaticResolver(RecoveryProbeTarget{
		DispatchRevision: "5",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return TransportProbeOutcome{Kind: RecoveryProbeOutcomeUnknown, FailureKind: RecoveryProbeFailureTaskFailure}, nil
		},
	}, true))

	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep 失败: %v", err)
	}
	if result.UnknownCount != 1 {
		t.Fatalf("UnknownCount = %d", result.UnknownCount)
	}
	state, _ := store.Get(context.Background(), accountScope("acc-recovery-5"), int64Ptr(nowMS()))
	if state.Phase != PhaseOpen {
		t.Fatalf("unknown 应回原相位 OPEN，got %s", state.Phase)
	}
	if state.Lease != nil {
		t.Fatal("unknown 释放后不应残留租约")
	}
	advance(nowMS() + 1)
}

// credential_rejected（探测 401/403）：不推进恢复（不得 RECOVERING/CLOSED），
// OPEN 走 transport_failure 臂重新 OPEN + 退避，reason 用独立
// credential_rejected 段。
func TestRecoveryCredentialRejectedReopensCanary(t *testing.T) {
	nowMS, _ := recoveryTestClock(t)
	store := newTestMemoryStore(t, 10, nowMS)
	seed := recoveryOpenState(t, accountScope("acc-recovery-6"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, int64Ptr(nowMS())); err != nil {
		t.Fatal(err)
	}
	status := 401
	service := newTestRecoveryService(t, store, nowMS, recoveryStaticResolver(RecoveryProbeTarget{
		DispatchRevision: "5",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return TransportProbeOutcome{Kind: RecoveryProbeOutcomeCredentialRejected, StatusCode: &status}, nil
		},
	}, true))

	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep 失败: %v", err)
	}
	if result.CredentialRejectedCount != 1 {
		t.Fatalf("credentialRejectedCount = %d: %+v", result.CredentialRejectedCount, result)
	}
	if result.FramingCompleteCount != 0 {
		t.Fatalf("401 不得计入 framingComplete: %+v", result)
	}
	state, _ := store.Get(context.Background(), accountScope("acc-recovery-6"), int64Ptr(nowMS()))
	if state.Phase != PhaseOpen {
		t.Fatalf("401 后必须重新 OPEN，got %s", state.Phase)
	}
	if state.Phase == PhaseRecovering || state.Phase == PhaseClosed {
		t.Fatalf("401 不得推进恢复: %s", state.Phase)
	}
	if state.BackoffAttempt != 2 {
		t.Fatalf("退避尝试应从 1 递增到 2: %d", state.BackoffAttempt)
	}
	if state.RetryAtMs == nil || *state.RetryAtMs <= nowMS() {
		t.Fatalf("必须带未来退避 deadline: %+v", state.RetryAtMs)
	}
	if state.Lease != nil {
		t.Fatal("重新 OPEN 后不应残留租约")
	}
	wantReason := "background_probe:credential_rejected:http_401"
	if state.FailureReason == nil || *state.FailureReason != wantReason {
		t.Fatalf("failureReason = %v, want %q", state.FailureReason, wantReason)
	}
}

// credential_rejected：SUSPECT 确认探测 403 按确认失败计（transport_failure
// 臂），写入独立 evidence key，未达阈值保持 SUSPECT。
func TestRecoveryCredentialRejectedCountsConfirmationFailure(t *testing.T) {
	nowMS, advance := recoveryTestClock(t)
	store := newTestMemoryStore(t, 10, nowMS)
	seed := recoverySuspectState(t, accountScope("acc-recovery-7"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, int64Ptr(nowMS())); err != nil {
		t.Fatal(err)
	}
	status := 403
	service := newTestRecoveryService(t, store, nowMS, recoveryStaticResolver(RecoveryProbeTarget{
		DispatchRevision: "5",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return TransportProbeOutcome{Kind: RecoveryProbeOutcomeCredentialRejected, StatusCode: &status}, nil
		},
	}, true))

	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep 失败: %v", err)
	}
	if result.CredentialRejectedCount != 1 {
		t.Fatalf("credentialRejectedCount = %d: %+v", result.CredentialRejectedCount, result)
	}
	state, _ := store.Get(context.Background(), accountScope("acc-recovery-7"), int64Ptr(nowMS()))
	if state.Phase != PhaseSuspect {
		t.Fatalf("确认失败(1/2)应保持 SUSPECT，got %s", state.Phase)
	}
	if state.ConfirmationFailureCount == nil || *state.ConfirmationFailureCount != 1 {
		t.Fatalf("confirmationFailureCount 应为 1: %+v", state.ConfirmationFailureCount)
	}
	wantKey := BackgroundConfirmationEvidenceKey(seed, "id-1")
	if len(state.FailureEvidenceKeys) != 1 || state.FailureEvidenceKeys[0] != wantKey {
		t.Fatalf("应写入 401 的独立失败证据: %v", state.FailureEvidenceKeys)
	}
	wantReason := "background_probe:credential_rejected:http_403"
	if state.FailureReason == nil || *state.FailureReason != wantReason {
		t.Fatalf("failureReason = %v, want %q", state.FailureReason, wantReason)
	}
	advance(nowMS() + 1)
}

// fencing：dispatch revision 漂移时跳过探针，fenced 计数并 replace revision。
func TestRecoveryDispatchRevisionDriftFences(t *testing.T) {
	nowMS, _ := recoveryTestClock(t)
	store := newTestMemoryStore(t, 10, nowMS)
	seed := recoverySuspectState(t, accountScope("acc-recovery-8"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, int64Ptr(nowMS())); err != nil {
		t.Fatal(err)
	}
	service := newTestRecoveryService(t, store, nowMS, recoveryStaticResolver(RecoveryProbeTarget{
		DispatchRevision: "9",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			t.Fatal("revision 漂移时不应执行探针")
			return TransportProbeOutcome{}, nil
		},
	}, true))

	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep 失败: %v", err)
	}
	if result.FencedCount != 1 {
		t.Fatalf("FencedCount = %d, want 1: %+v", result.FencedCount, result)
	}
	state, _ := store.Get(context.Background(), accountScope("acc-recovery-8"), int64Ptr(nowMS()))
	if state.DispatchRevision != "9" {
		t.Fatalf("dispatchRevision 应已被替换为 9: %s", state.DispatchRevision)
	}
}

// fencing：stale generation 状态在 acquire 阶段被围栏（当前 store 无该
// generation 的记录时 acquire 返回 not_found / stale，均不计 leased 推进）。
func TestRecoveryUnknownTargetGenerationIsSkipped(t *testing.T) {
	nowMS, _ := recoveryTestClock(t)
	store := newTestMemoryStore(t, 10, nowMS)
	// 不 Restore：ListDue 不会选中；直接以 recover 单项验证 stale 围栏路径
	// 需要 store 中存在旧 generation——用 Restore+generation 递增制造。
	seed := recoverySuspectState(t, accountScope("acc-recovery-9"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, int64Ptr(nowMS())); err != nil {
		t.Fatal(err)
	}
	stale := seed
	stale.Generation = 0
	outcome, leased, err := newTestRecoveryService(t, store, nowMS, recoveryStaticResolver(RecoveryProbeTarget{
		DispatchRevision: "5",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			t.Fatal("stale generation 不应执行探针")
			return TransportProbeOutcome{}, nil
		},
	}, true)).recover(context.Background(), stale)
	if err != nil {
		t.Fatalf("recover 失败: %v", err)
	}
	if outcome != recoveryFenced && outcome != recoverySkipped {
		t.Fatalf("stale generation 应 fenced/skipped，got %s", outcome)
	}
	if leased {
		t.Fatal("stale generation 不应计入 leased")
	}
}

// 目标缺失：保守释放租约（unknown），租约被清除。
func TestRecoveryMissingTargetReleasesLease(t *testing.T) {
	nowMS, _ := recoveryTestClock(t)
	store := newTestMemoryStore(t, 10, nowMS)
	seed := recoverySuspectState(t, accountScope("acc-recovery-10"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, int64Ptr(nowMS())); err != nil {
		t.Fatal(err)
	}
	service := newTestRecoveryService(t, store, nowMS, recoveryStaticResolver(RecoveryProbeTarget{}, false))

	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep 失败: %v", err)
	}
	if result.UnknownCount != 1 {
		t.Fatalf("UnknownCount = %d", result.UnknownCount)
	}
	state, _ := store.Get(context.Background(), accountScope("acc-recovery-10"), int64Ptr(nowMS()))
	if state.Lease != nil {
		t.Fatal("unknown 释放后不应残留租约")
	}
}

// 探针执行超出租约 deadline：按 unknown/task_failure 结算（中性回原相位）。
func TestRecoveryProbeLeaseDeadlineYieldsUnknown(t *testing.T) {
	nowMS, _ := recoveryTestClock(t)
	store := newTestMemoryStore(t, 10, nowMS)
	seed := recoverySuspectState(t, accountScope("acc-recovery-11"), 1, "5", nowMS())
	if _, err := store.Restore(context.Background(), seed, int64Ptr(nowMS())); err != nil {
		t.Fatal(err)
	}
	service, err := NewRecoveryService(store, recoveryStaticResolver(RecoveryProbeTarget{
		DispatchRevision: "5",
		Probe: func(ctx context.Context) (TransportProbeOutcome, error) {
			<-ctx.Done()
			return TransportProbeOutcome{}, ctx.Err()
		},
	}, true), RecoveryServiceOptions{
		BatchSize:       10,
		Concurrency:     1,
		LeaseDurationMS: 30,
		NowMS:           nowMS,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep 失败: %v", err)
	}
	if result.LeasedCount != 1 {
		t.Fatalf("LeasedCount = %d", result.LeasedCount)
	}
	state, _ := store.Get(context.Background(), accountScope("acc-recovery-11"), int64Ptr(nowMS()+1_000))
	// 确认 unknown：退避保持 SUSPECT（中性，不计失败证据）。
	if state.Phase != PhaseSuspect {
		t.Fatalf("租约超时应按 unknown 回落 SUSPECT，got %s", state.Phase)
	}
	if len(state.FailureEvidenceKeys) != 0 {
		t.Fatalf("unknown 不计入失败证据: %v", state.FailureEvidenceKeys)
	}
}

// 相位过滤：仅 SUSPECT/OPEN/RECOVERING 可恢复；其他相位（HALF_OPEN 等）被
// recover 单项跳过。
func TestRecoverySkipsNonEligiblePhase(t *testing.T) {
	nowMS, _ := recoveryTestClock(t)
	store := newTestMemoryStore(t, 10, nowMS)
	seed := recoveryOpenState(t, accountScope("acc-recovery-12"), 1, "5", nowMS())
	seed.Phase = PhaseHalfOpen
	outcome, leased, err := newTestRecoveryService(t, store, nowMS, recoveryStaticResolver(RecoveryProbeTarget{
		DispatchRevision: "5",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			t.Fatal("非资格相位不应执行探针")
			return TransportProbeOutcome{}, nil
		},
	}, true)).recover(context.Background(), seed)
	if err != nil {
		t.Fatalf("recover 失败: %v", err)
	}
	if outcome != recoverySkipped || leased {
		t.Fatalf("HALF_OPEN 应跳过: outcome=%s leased=%v", outcome, leased)
	}
}

// identity 解析：owner 与 authorized 两形态 + 失败形态。
func TestParseRecoveryRuntimeIdentity(t *testing.T) {
	cases := []struct {
		name       string
		runtimeKey string
		want       RecoveryRuntimeIdentity
		wantOK     bool
	}{
		{
			name:       "owner 纯账户 ID",
			runtimeKey: "acc-1",
			want:       RecoveryRuntimeIdentity{Kind: "owner", AccountID: "acc-1"},
			wantOK:     true,
		},
		{
			name:       "owner 取首个冒号前段",
			runtimeKey: "acc-1:unexpected",
			want:       RecoveryRuntimeIdentity{Kind: "owner", AccountID: "acc-1"},
			wantOK:     true,
		},
		{
			name:       "authorized 四段",
			runtimeKey: "acc-1:authorized:sys-1:grp-1:authz-1",
			want: RecoveryRuntimeIdentity{
				Kind:            "authorized",
				AccountID:       "acc-1",
				SystemAccountID: "sys-1",
				GroupID:         "grp-1",
				AuthorizationID: "authz-1",
			},
			wantOK: true,
		},
		{
			name:       "authorized 段数不足",
			runtimeKey: "acc-1:authorized:sys-1:grp-1",
			wantOK:     false,
		},
		{
			name:       "authorized 空账户段",
			runtimeKey: ":authorized:sys-1:grp-1:authz-1",
			wantOK:     false,
		},
		{
			name:       "authorized 空段被剔后不足",
			runtimeKey: "acc-1:authorized:sys-1::authz-1",
			wantOK:     false,
		},
		{
			name:       "空键",
			runtimeKey: "",
			wantOK:     false,
		},
		{
			name:       "空白账户段",
			runtimeKey: "   ",
			wantOK:     false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := ParseRecoveryRuntimeIdentity(testCase.runtimeKey)
			if ok != testCase.wantOK {
				t.Fatalf("ok = %v, want %v", ok, testCase.wantOK)
			}
			if !testCase.wantOK {
				return
			}
			if got != testCase.want {
				t.Fatalf("identity = %+v, want %+v", got, testCase.want)
			}
		})
	}
}

// 探针 outcome 分类器（契约源 opsjobs probeoutcome.go 的 gateway 副本语义）。
func TestTransportProbeOutcomeFromResultClassification(t *testing.T) {
	status := func(code int) *int { return &code }
	cases := []struct {
		name      string
		result    ProbeResultSnapshot
		upstream  *UpstreamAttemptSnapshot
		canceled  bool
		timedOut  bool
		exhausted *bool
		want      TransportProbeOutcome
	}{
		{
			name:     "canceled",
			canceled: true,
			want:     TransportProbeOutcome{Kind: RecoveryProbeOutcomeUnknown, FailureKind: RecoveryProbeFailureCanceled},
		},
		{
			name:     "200 framing_complete",
			upstream: &UpstreamAttemptSnapshot{IsReal: true, IsCompletedReal: true, Status: status(200)},
			want:     TransportProbeOutcome{Kind: RecoveryProbeOutcomeFramingComplete, StatusCode: status(200)},
		},
		{
			name:     "401 credential_rejected",
			upstream: &UpstreamAttemptSnapshot{IsReal: true, IsCompletedReal: true, Status: status(401)},
			want:     TransportProbeOutcome{Kind: RecoveryProbeOutcomeCredentialRejected, StatusCode: status(401)},
		},
		{
			name:     "403 credential_rejected",
			upstream: &UpstreamAttemptSnapshot{IsReal: true, IsCompletedReal: true, Status: status(403)},
			want:     TransportProbeOutcome{Kind: RecoveryProbeOutcomeCredentialRejected, StatusCode: status(403)},
		},
		{
			name:     "invalid_probe_output 语义失败",
			upstream: &UpstreamAttemptSnapshot{IsReal: true, IsCompletedReal: true, Status: status(200)},
			result:   ProbeResultSnapshot{ErrorCode: "invalid_probe_output"},
			want:     TransportProbeOutcome{Kind: RecoveryProbeOutcomeFramingComplete, StatusCode: status(200), SemanticSuccess: recoveryBoolPtr(false)},
		},
		{
			name:     "上游超时 transport_incomplete",
			upstream: &UpstreamAttemptSnapshot{IsReal: true, TransportFailureKind: "timeout"},
			timedOut: true,
			want:     TransportProbeOutcome{Kind: RecoveryProbeOutcomeTransportIncomplete, FailureKind: RecoveryProbeFailureTimeout},
		},
		{
			name:      "本地超时未耗尽诊断预算 unknown",
			timedOut:  true,
			exhausted: recoveryBoolPtr(false),
			want:      TransportProbeOutcome{Kind: RecoveryProbeOutcomeUnknown, FailureKind: RecoveryProbeFailureTaskFailure},
		},
		{
			name:     "有真实尝试但未完成 transport_incomplete connection",
			upstream: &UpstreamAttemptSnapshot{IsReal: true},
			want:     TransportProbeOutcome{Kind: RecoveryProbeOutcomeTransportIncomplete, FailureKind: RecoveryProbeFailureConnection},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := TransportProbeOutcomeFromResult(testCase.result, testCase.upstream, testCase.canceled, testCase.timedOut, testCase.exhausted)
			if got.Kind != testCase.want.Kind || got.FailureKind != testCase.want.FailureKind {
				t.Fatalf("outcome = %+v, want %+v", got, testCase.want)
			}
			if (got.StatusCode == nil) != (testCase.want.StatusCode == nil) ||
				(got.StatusCode != nil && *got.StatusCode != *testCase.want.StatusCode) {
				t.Fatalf("statusCode = %v, want %v", got.StatusCode, testCase.want.StatusCode)
			}
			if (got.SemanticSuccess == nil) != (testCase.want.SemanticSuccess == nil) ||
				(got.SemanticSuccess != nil && *got.SemanticSuccess != *testCase.want.SemanticSuccess) {
				t.Fatalf("semanticSuccess = %v, want %v", got.SemanticSuccess, testCase.want.SemanticSuccess)
			}
		})
	}
}

// 结算映射：outcome → store 三值（credential_rejected 落 transport_failure）。
func TestRecoveryOutcomeVerdictMapping(t *testing.T) {
	status := 200
	cases := []struct {
		kind            RecoveryProbeOutcomeKind
		semanticSuccess *bool
		want            string
	}{
		{RecoveryProbeOutcomeFramingComplete, nil, OutcomeFramingComplete},
		{RecoveryProbeOutcomeFramingComplete, recoveryBoolPtr(true), OutcomeFramingComplete},
		{RecoveryProbeOutcomeFramingComplete, recoveryBoolPtr(false), OutcomeUnknown},
		{RecoveryProbeOutcomeTransportIncomplete, nil, OutcomeTransportFailure},
		{RecoveryProbeOutcomeCredentialRejected, nil, OutcomeTransportFailure},
		{RecoveryProbeOutcomeUnknown, nil, OutcomeUnknown},
	}
	for _, testCase := range cases {
		got := recoveryOutcomeVerdict(TransportProbeOutcome{Kind: testCase.kind, StatusCode: &status, SemanticSuccess: testCase.semanticSuccess})
		if got != testCase.want {
			t.Fatalf("kind=%s semantic=%v: verdict = %s, want %s", testCase.kind, testCase.semanticSuccess, got, testCase.want)
		}
	}
}

func recoveryBoolPtr(value bool) *bool { return &value }

// 防呆：reason 映射对未知 kind 恒空（对应 omitempty 缺席）。
func TestRecoveryFailureReasonEmptyForUnknown(t *testing.T) {
	if reason := recoveryFailureReason(TransportProbeOutcome{Kind: RecoveryProbeOutcomeUnknown}); reason != "" {
		t.Fatalf("unknown outcome 的 reason 应为空: %q", reason)
	}
	if reason := recoveryFailureReason(TransportProbeOutcome{Kind: RecoveryProbeOutcomeFramingComplete}); reason != "" {
		t.Fatalf("framing_complete outcome 的 reason 应为空: %q", reason)
	}
	status := 429
	got := recoveryFailureReason(TransportProbeOutcome{Kind: RecoveryProbeOutcomeTransportIncomplete, FailureKind: RecoveryProbeFailureConnection, StatusCode: &status})
	if !strings.HasPrefix(got, "background_probe:connection:http_429") {
		t.Fatalf("reason = %q", got)
	}
}
