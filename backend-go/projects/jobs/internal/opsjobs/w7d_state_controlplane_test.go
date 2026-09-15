package opsjobs

// w7d（opsjobs 状态机/控制面批次）：MemoryCircuitStore 全生命周期状态机、
// CircuitRecoveryService 错误臂、ControlPlaneMaintenance 错误臂与
// listavailability 维护批分支。全部进程内 mock 闭环（内存电路 store、
// 内存 ledger/outbox/cursor、fake repo/overlay），不连接真实数据库/Redis。

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// MemoryCircuitStore：完整状态机
// ---------------------------------------------------------------------------

func w7dMemoryStore(t *testing.T, capacity int) *MemoryCircuitStore {
	t.Helper()
	store, err := NewMemoryCircuitStore(capacity, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func w7dSuspectRaw(t *testing.T, scope CircuitScope, generation int64) CircuitState {
	t.Helper()
	state := suspectState(t, scope, generation, "7", 1_000)
	state.ConfirmationFailuresRequired = nil
	state.ConfirmationFailureCount = nil
	state.RetryAtMS = nil
	return state
}

func TestW7DMemoryStoreNewRejectsBadCapacity(t *testing.T) {
	if _, err := NewMemoryCircuitStore(0, nil); err == nil {
		t.Fatal("capacity<1 必须拒绝")
	}
}

func TestW7DMemoryStoreConfirmationLeaseAndCompletion(t *testing.T) {
	store := w7dMemoryStore(t, 8)
	scope := accountScope("acc-1")
	raw := w7dSuspectRaw(t, scope, 2)
	if _, err := store.Restore(context.Background(), raw, 1_000); err != nil {
		t.Fatal(err)
	}
	if size := store.Size(1_000); size != 1 {
		t.Fatalf("Size 必须为 1: %d", size)
	}
	identity := CircuitTransitionIdentity{Scope: scope, Generation: 2, DispatchRevision: "7", TransitionID: "tx-acquire", NowMS: 2_000}
	// NotDue：RetryAt 在未来（同代恢复必须递增 UpdatedAtMS 才能覆盖旧条目）。
	raw.RetryAtMS = int64Ptr(5_000)
	raw.UpdatedAtMS = 1_500
	if _, err := store.Restore(context.Background(), raw, 1_500); err != nil {
		t.Fatal(err)
	}
	result, err := store.AcquireConfirmationLease(context.Background(), identity, CircuitLeaseSpec{LeaseID: "l1", LeaseUntilMS: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CircuitMutationNotDue {
		t.Fatalf("未到期必须 not_due: %s", result.Status)
	}
	// 到期后获取租约成功。
	raw.RetryAtMS = int64Ptr(1_000)
	raw.UpdatedAtMS = 1_600
	if _, err := store.Restore(context.Background(), raw, 1_600); err != nil {
		t.Fatal(err)
	}
	result, err = store.AcquireConfirmationLease(context.Background(), identity, CircuitLeaseSpec{LeaseID: "l1", LeaseUntilMS: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CircuitMutationApplied || result.State.Lease == nil || result.State.Lease.LeaseID != "l1" {
		t.Fatalf("租约必须应用: %#v", result)
	}
	// 幂等重放同一 transitionID。
	replay, err := store.AcquireConfirmationLease(context.Background(), identity, CircuitLeaseSpec{LeaseID: "l1", LeaseUntilMS: 10_000})
	if err != nil || replay.Status != CircuitMutationIdempotent {
		t.Fatalf("重放必须幂等: %s %v", replay.Status, err)
	}
	// 租约已存在（新 transitionID）→ state_mismatch。
	dupIdentity := identity
	dupIdentity.TransitionID = "tx-acquire-dup"
	result, err = store.AcquireConfirmationLease(context.Background(), dupIdentity, CircuitLeaseSpec{LeaseID: "l2", LeaseUntilMS: 10_000})
	if err != nil || result.Status != CircuitMutationStateMismatch {
		t.Fatalf("已有租约必须 mismatch: %s %v", result.Status, err)
	}

	// CompleteConfirmation：framing_complete + closed → CLOSED。
	completeIdentity := identity
	completeIdentity.TransitionID = "tx-complete"
	result, err = store.CompleteConfirmation(context.Background(), completeIdentity, "l1", CircuitCompletion{Outcome: CircuitVerdictFramingComplete, FramingCompleteDisposition: "closed"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CircuitMutationApplied || result.State.Phase != CircuitPhaseClosed {
		t.Fatalf("closed 收口必须应用: %#v", result)
	}
	// CLOSED 保留期内可见，过期后 freshEntry 清理。
	if state, err := store.Get(context.Background(), scope, 2_000); err != nil || state.Phase != CircuitPhaseClosed {
		t.Fatalf("保留期内 CLOSED 可见: %#v %v", state, err)
	}
	if state, err := store.Get(context.Background(), scope, 2_000+store.closedRetentionMS+1); err != nil || state.Phase != CircuitPhaseClosed || state.Generation != 0 {
		t.Fatalf("保留期后必须回落空白 CLOSED: %#v %v", state, err)
	}
}

func TestW7DMemoryStoreCompleteConfirmationEvidenceAccumulatesToOpen(t *testing.T) {
	store := w7dMemoryStore(t, 8)
	scope := accountScope("acc-2")
	two := 2
	raw := w7dSuspectRaw(t, scope, 1)
	raw.ConfirmationFailuresRequired = &two
	if _, err := store.Restore(context.Background(), raw, 1_000); err != nil {
		t.Fatal(err)
	}
	identity := CircuitTransitionIdentity{Scope: scope, Generation: 1, DispatchRevision: "7", TransitionID: "acq", NowMS: 1_000}
	lease := CircuitLeaseSpec{LeaseID: "l1", LeaseUntilMS: 9_000}
	if result, err := store.AcquireConfirmationLease(context.Background(), identity, lease); err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("租约获取失败: %#v %v", result, err)
	}
	// unknown 判定 → backoff+1，lease 清空。
	unknownIdentity := identity
	unknownIdentity.TransitionID = "unknown"
	result, err := store.CompleteConfirmation(context.Background(), unknownIdentity, "l1", CircuitCompletion{Outcome: CircuitVerdictUnknown})
	if err != nil || result.Status != CircuitMutationApplied || result.State.BackoffAttempt != 1 || result.State.Lease != nil {
		t.Fatalf("unknown 必须 backoff: %#v %v", result, err)
	}
	// transport failure 证据累计：第 1 次独立证据 → count 1 < required 2。
	raw2 := w7dSuspectRaw(t, scope, 1)
	raw2.BackoffAttempt = 1
	raw2.ConfirmationFailuresRequired = &two
	raw2.UpdatedAtMS = 2_000
	raw2.RetryAtMS = int64Ptr(2_000)
	if _, err := store.Restore(context.Background(), raw2, 2_000); err != nil {
		t.Fatal(err)
	}
	identity.NowMS = 2_500
	identity.TransitionID = "acq2"
	if result, err := store.AcquireConfirmationLease(context.Background(), identity, CircuitLeaseSpec{LeaseID: "l2", LeaseUntilMS: 9_000}); err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("第二次租约: %#v %v", result, err)
	}
	evidenceOne := strings.Repeat("11", 32)
	completeIdentity := identity
	completeIdentity.TransitionID = "fail-1"
	result, err = store.CompleteConfirmation(context.Background(), completeIdentity, "l2", CircuitCompletion{Outcome: CircuitVerdictTransportFailure, FailureEvidenceKey: evidenceOne, Reason: "probe:timeout"})
	if err != nil || result.Status != CircuitMutationApplied || result.State.Phase != CircuitPhaseSuspect {
		t.Fatalf("单次证据必须保持 SUSPECT: %#v %v", result, err)
	}
	if result.State.ConfirmationFailureCount == nil || *result.State.ConfirmationFailureCount != 1 || result.State.FailureReason != "probe:timeout" {
		t.Fatalf("证据计数/原因不正确: %#v", result.State)
	}
	// 同一证据重放不累计；新证据累计到 required=2 → OPEN。
	raw3 := w7dSuspectRaw(t, scope, 1)
	raw3.BackoffAttempt = 1
	raw3.ConfirmationFailuresRequired = &two
	raw3.ConfirmationFailureCount = intPtr(1)
	raw3.UpdatedAtMS = 3_000
	raw3.RetryAtMS = int64Ptr(3_000)
	raw3.FailureEvidenceKeys = []string{evidenceOne}
	if _, err := store.Restore(context.Background(), raw3, 3_000); err != nil {
		t.Fatal(err)
	}
	identity.NowMS = 3_500
	identity.TransitionID = "acq3"
	if result, err := store.AcquireConfirmationLease(context.Background(), identity, CircuitLeaseSpec{LeaseID: "l3", LeaseUntilMS: 9_000}); err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("第三次租约: %#v %v", result, err)
	}
	completeIdentity = identity
	completeIdentity.TransitionID = "fail-2"
	result, err = store.CompleteConfirmation(context.Background(), completeIdentity, "l3", CircuitCompletion{Outcome: CircuitVerdictTransportFailure, FailureEvidenceKey: strings.Repeat("22", 32)})
	if err != nil || result.Status != CircuitMutationApplied || result.State.Phase != CircuitPhaseOpen {
		t.Fatalf("第 2 份独立证据必须 OPEN: %#v %v", result, err)
	}
	if result.State.IncidentID == "" || result.State.OpenedAtMS == nil || result.State.BackoffAttempt != 1 {
		t.Fatalf("OPEN 状态不完整: %#v", result.State)
	}
}

func TestW7DMemoryStoreCanaryLifecycle(t *testing.T) {
	store := w7dMemoryStore(t, 8)
	scope := accountScope("acc-3")
	open := openState(t, scope, 4, "7", 1_000)
	open.RetryAtMS = int64Ptr(1_000)
	if _, err := store.Restore(context.Background(), open, 1_000); err != nil {
		t.Fatal(err)
	}
	identity := CircuitTransitionIdentity{Scope: scope, Generation: 4, DispatchRevision: "7", TransitionID: "canary-acq", NowMS: 2_000}

	// 错误阶段（SUSPECT 期望 OPEN）→ state_mismatch 由 AcquireCanaryLease 校验 phase != OPEN。
	// 先验证 canary 在 OPEN 上成功。
	result, err := store.AcquireCanaryLease(context.Background(), identity, CircuitLeaseSpec{LeaseID: "c1", LeaseUntilMS: 9_000})
	if err != nil || result.Status != CircuitMutationApplied || result.State.Phase != CircuitPhaseHalfOpen || result.State.HalfOpenOrigin != "OPEN" {
		t.Fatalf("canary 租约必须 HALF_OPEN/origin=OPEN: %#v %v", result, err)
	}
	// 有租约时再次获取（新 transitionID）→ mismatch。
	dupIdentity := identity
	dupIdentity.TransitionID = "canary-acq-dup"
	dup, err := store.AcquireCanaryLease(context.Background(), dupIdentity, CircuitLeaseSpec{LeaseID: "c2", LeaseUntilMS: 9_000})
	if err != nil || dup.Status != CircuitMutationStateMismatch {
		t.Fatalf("已有租约必须 mismatch: %s %v", dup.Status, err)
	}
	// CompleteCanary unknown → 回 OPEN origin。
	completeIdentity := identity
	completeIdentity.TransitionID = "canary-unknown"
	result, err = store.CompleteCanary(context.Background(), completeIdentity, "c1", CircuitCompletion{Outcome: CircuitVerdictUnknown})
	if err != nil || result.Status != CircuitMutationApplied || result.State.Phase != CircuitPhaseOpen {
		t.Fatalf("unknown 必须 restore origin: %#v %v", result, err)
	}
	// transport failure → 重新 OPEN（backoff+1）；再次获取租约需要新 transitionID
	// 与超过 backoff 窗口的时钟（unknown 已把 retryAt 推进到 now+CircuitBackoffMS[1]）。
	reacquireIdentity := identity
	reacquireIdentity.TransitionID = "canary-acq-2"
	reacquireIdentity.NowMS = 9_000
	if result, err = store.AcquireCanaryLease(context.Background(), reacquireIdentity, CircuitLeaseSpec{LeaseID: "c1", LeaseUntilMS: 19_000}); err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("重新 canary: %#v %v", result, err)
	}
	completeIdentity = reacquireIdentity
	completeIdentity.TransitionID = "canary-fail"
	result, err = store.CompleteCanary(context.Background(), completeIdentity, "c1", CircuitCompletion{Outcome: CircuitVerdictTransportFailure, Reason: "background_probe:timeout"})
	if err != nil || result.State.Phase != CircuitPhaseOpen || result.State.BackoffAttempt != 3 || result.State.FailureReason != "background_probe:timeout" {
		t.Fatalf("canary 失败必须回 OPEN: %#v %v", result, err)
	}
}

func TestW7DMemoryStoreRecoveringSuccessClosesAndTracksEvidence(t *testing.T) {
	store := w7dMemoryStore(t, 8)
	scope := accountScope("acc-4")
	childScope := protocolModelScope("acc-4", "p", "text", "b")
	childKey, err := AccountCircuitScopeKey(childScope)
	if err != nil {
		t.Fatal(err)
	}
	state := openState(t, scope, 9, "7", 1_000)
	state.Phase = CircuitPhaseRecovering
	state.RetryAtMS = int64Ptr(1_000)
	state.RequiredRecoveryScopeKeys = []string{childKey}
	state.ChildScopeKeys = []string{childKey}
	state.ChildIncidentIDs = []string{"child-incident"}
	if _, err := store.Restore(context.Background(), state, 1_000); err != nil {
		t.Fatal(err)
	}
	identity := CircuitTransitionIdentity{Scope: scope, Generation: 9, DispatchRevision: "7", TransitionID: "rec-acq", NowMS: 2_000}
	// RECOVERING origin 的 canary kind = recovery。
	result, err := store.AcquireCanaryLease(context.Background(), identity, CircuitLeaseSpec{LeaseID: "r1", LeaseUntilMS: 9_000})
	if err != nil || result.Status != CircuitMutationApplied || result.State.Lease.Kind != CircuitLeaseRecovery || result.State.HalfOpenOrigin != "RECOVERING" {
		t.Fatalf("recovery canary: %#v %v", result, err)
	}
	// 成功 1/3：进入 RECOVERING 并合并 evidence scope key。
	completeIdentity := identity
	completeIdentity.TransitionID = "rec-1"
	result, err = store.CompleteCanary(context.Background(), completeIdentity, "r1", CircuitCompletion{Outcome: CircuitVerdictFramingComplete, EvidenceScopeKey: childKey})
	if err != nil || result.State.Phase != CircuitPhaseRecovering || result.State.RecoverySuccessCount != 1 {
		t.Fatalf("第 1 次成功: %#v %v", result, err)
	}
	if len(result.State.RecoveryEvidenceScopeKeys) != 1 || result.State.RecoveryEvidenceScopeKeys[0] != childKey {
		t.Fatalf("evidence scope key 必须合并: %#v", result.State.RecoveryEvidenceScopeKeys)
	}
	// 第 2/3 次成功后 close（CLOSED 保留父子关系投影）。
	for index, transition := range []string{"rec-2", "rec-3"} {
		identity.NowMS = int64(3_000 + index)
		identity.TransitionID = "rec-acq-" + transition
		result, err = store.AcquireCanaryLease(context.Background(), identity, CircuitLeaseSpec{LeaseID: "r" + transition, LeaseUntilMS: 9_000})
		if err != nil || result.Status != CircuitMutationApplied {
			t.Fatalf("%s 租约: %#v %v", transition, result, err)
		}
		completeIdentity := identity
		completeIdentity.TransitionID = transition
		result, err = store.CompleteCanary(context.Background(), completeIdentity, "r"+transition, CircuitCompletion{Outcome: CircuitVerdictFramingComplete, EvidenceScopeKey: childKey})
		if err != nil {
			t.Fatal(err)
		}
	}
	if result.State.Phase != CircuitPhaseClosed {
		t.Fatalf("3 次成功必须 CLOSED: %#v", result.State)
	}
}

func TestW7DMemoryStoreLeaseExpiryNormalization(t *testing.T) {
	store := w7dMemoryStore(t, 8)
	scope := accountScope("acc-5")

	// confirmation 租约过期：freshEntry 清租约并把 retryAt 归到当前时刻。
	state := w7dSuspectRaw(t, scope, 1)
	state.RetryAtMS = nil
	state.Lease = &CircuitLease{Kind: CircuitLeaseConfirmation, LeaseID: "expired", LeaseUntilMS: 500}
	if _, err := store.Restore(context.Background(), state, 100); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), scope, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lease != nil || got.RetryAtMS == nil || *got.RetryAtMS != 1_000 {
		t.Fatalf("confirmation 租约过期必须归一化: %#v", got)
	}

	// half_open 租约过期：回落 HalfOpenOrigin。
	scope2 := accountScope("acc-5b")
	state2 := openState(t, scope2, 1, "7", 1_000)
	state2.Lease = &CircuitLease{Kind: CircuitLeaseHalfOpen, LeaseID: "expired", LeaseUntilMS: 500}
	state2.HalfOpenOrigin = "OPEN"
	if _, err := store.Restore(context.Background(), state2, 100); err != nil {
		t.Fatal(err)
	}
	got2, err := store.Get(context.Background(), scope2, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Phase != CircuitPhaseOpen || got2.Lease != nil || got2.HalfOpenOrigin != "" {
		t.Fatalf("half_open 租约过期必须回落 OPEN: %#v", got2)
	}

	// RECOVERING origin 回落。
	scope3 := accountScope("acc-5c")
	state3 := openState(t, scope3, 1, "7", 1_000)
	state3.Phase = CircuitPhaseHalfOpen
	state3.HalfOpenOrigin = "RECOVERING"
	state3.Lease = &CircuitLease{Kind: CircuitLeaseRecovery, LeaseID: "expired", LeaseUntilMS: 500}
	if _, err := store.Restore(context.Background(), state3, 100); err != nil {
		t.Fatal(err)
	}
	got3, err := store.Get(context.Background(), scope3, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if got3.Phase != CircuitPhaseRecovering {
		t.Fatalf("recovery 租约过期必须回落 RECOVERING: %#v", got3)
	}
}

func TestW7DMemoryStoreCapacityAndReplacement(t *testing.T) {
	store := w7dMemoryStore(t, 1)
	scopeA := accountScope("cap-a")
	scopeB := accountScope("cap-b")

	// 占用唯一容量：SUSPECT 条目无法被驱逐 → capacity_exhausted。
	stateA := w7dSuspectRaw(t, scopeA, 1)
	if _, err := store.Restore(context.Background(), stateA, 1_000); err != nil {
		t.Fatal(err)
	}
	result, err := store.Restore(context.Background(), w7dSuspectRaw(t, scopeB, 1), 1_000)
	if err != nil || result.Status != CircuitMutationCapacityExhausted {
		t.Fatalf("容量满必须 exhausted: %s %v", result.Status, err)
	}
	if result.State.FailureReason != "runtime_state_capacity_exhausted" {
		t.Fatalf("exhausted 投影不正确: %#v", result.State)
	}
	// CLOSED 条目在保留期内可被驱逐腾位。
	closedA := closedAccountCircuitState(scopeA, "7", 5, "tx", 1_000)
	if _, err := store.Restore(context.Background(), closedA, 2_000); err != nil {
		t.Fatal(err)
	}
	result, err = store.Restore(context.Background(), w7dSuspectRaw(t, scopeB, 1), 2_000)
	if err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("驱逐 CLOSED 后必须可写入: %s %v", result.Status, err)
	}
	if store.Size(2_000) != 1 {
		t.Fatalf("容量仍为 1: %d", store.Size(2_000))
	}

	// ReplaceDispatchRevision：对全新 scope 创建 CLOSED 条目（独立大容量 store）。
	replaceStore := w7dMemoryStore(t, 8)
	revisionResult, err := replaceStore.ReplaceDispatchRevision(context.Background(), scopeA, "9", "replace-1", 3_000)
	if err != nil || revisionResult.Status != CircuitMutationApplied {
		t.Fatalf("revision 替换: %#v %v", revisionResult, err)
	}
	// 同 revision → idempotent（非幂等 replay 路径）。
	revisionResult, err = replaceStore.ReplaceDispatchRevision(context.Background(), scopeA, "9", "replace-2", 3_000)
	if err != nil || revisionResult.Status != CircuitMutationIdempotent {
		t.Fatalf("同 revision 幂等: %#v %v", revisionResult, err)
	}
	// 更旧 revision → stale_dispatch_revision。
	revisionResult, err = replaceStore.ReplaceDispatchRevision(context.Background(), scopeA, "8", "replace-3", 3_000)
	if err != nil || revisionResult.Status != CircuitMutationStaleDispatchRevision {
		t.Fatalf("旧 revision 必须 stale: %#v %v", revisionResult, err)
	}
	// 幂等 replay（同 transitionID）。
	revisionResult, err = replaceStore.ReplaceDispatchRevision(context.Background(), scopeA, "9", "replace-1", 3_000)
	if err != nil || revisionResult.Status != CircuitMutationIdempotent {
		t.Fatalf("同 transitionID 重放幂等: %#v %v", revisionResult, err)
	}
	// 参数校验。
	if _, err := replaceStore.ReplaceDispatchRevision(context.Background(), scopeA, " ", "tx", 1); err == nil {
		t.Fatal("空 revision 必须拒绝")
	}
	if _, err := replaceStore.ReplaceDispatchRevision(context.Background(), scopeA, "9", " ", 1); err == nil {
		t.Fatal("空 transitionID 必须拒绝")
	}
	// ListDue limit 校验。
	if _, err := replaceStore.ListDue(context.Background(), 1, 0); err == nil {
		t.Fatal("limit<1 必须拒绝")
	}

	// ReplaceAccountDispatchRevision：owner 与 authorized 通道都命中。
	if _, err := replaceStore.Restore(context.Background(), w7dSuspectRaw(t, scopeB, 3), 4_000); err != nil {
		t.Fatal(err)
	}
	authorizedScope := scopeB
	authorizedScope.AccountRuntimeKey = "cap-b:authorized:sys:g:auth"
	authorizedKey, err := AccountCircuitScopeKey(authorizedScope)
	if err != nil {
		t.Fatal(err)
	}
	authorizedState := w7dSuspectRaw(t, authorizedScope, 3)
	authorizedState.ScopeKey = authorizedKey
	if _, err := replaceStore.Restore(context.Background(), authorizedState, 4_000); err != nil {
		t.Fatal(err)
	}
	changed, err := replaceStore.ReplaceAccountDispatchRevision(context.Background(), "cap-b", "11", "bulk", 5_000)
	if err != nil {
		t.Fatal(err)
	}
	// scopeB 与其 authorized 通道各一条。
	if changed != 2 {
		t.Fatalf("owner+authorized 必须都替换: %d", changed)
	}
	if _, err := store.ReplaceAccountDispatchRevision(context.Background(), "", "9", "tx", 1); err == nil {
		t.Fatal("空 runtimeKey 必须拒绝")
	}

	// ClearAccountEscalationEvidence：参数守卫 + 未记录返回 false。
	if _, err := store.ClearAccountEscalationEvidence(context.Background(), "", "rev", "ev", 1); err == nil {
		t.Fatal("空 accountRuntimeKey 必须拒绝")
	}
	if _, err := store.ClearAccountEscalationEvidence(context.Background(), "cap-b", "", "ev", 1); err == nil {
		t.Fatal("空 dispatchRevision 必须拒绝")
	}
	if _, err := store.ClearAccountEscalationEvidence(context.Background(), "cap-b", "rev", "", 1); err == nil {
		t.Fatal("空 evidenceId 必须拒绝")
	}
	if cleared, err := store.ClearAccountEscalationEvidence(context.Background(), "cap-b", "rev", "ev", 1); err != nil || cleared {
		t.Fatalf("内存实现未记录 escalation: %t %v", cleared, err)
	}
}

func TestW7DMemoryStoreHierarchyShadowAndUnshadow(t *testing.T) {
	store := w7dMemoryStore(t, 8)
	parentScope := accountScope("hier-a")
	childScope := protocolModelScope("hier-a", "p", "text", "b")
	childKey, err := AccountCircuitScopeKey(childScope)
	if err != nil {
		t.Fatal(err)
	}
	parentKey, _ := AccountCircuitScopeKey(parentScope)
	_ = parentKey

	child := suspectState(t, childScope, 1, "7", 1_000)
	child.IncidentID = "child-incident"
	if _, err := store.Restore(context.Background(), child, 1_000); err != nil {
		t.Fatal(err)
	}
	parent := openState(t, parentScope, 1, "7", 1_000)
	parent.IncidentID = "parent-incident"
	parent.TransitionID = "parent-tx"
	parent.ChildScopeKeys = []string{childKey}
	parent.ChildIncidentIDs = []string{"child-incident"}
	// 父 OPEN 投影把子 shadow（UpdatedAtMS 对齐才会投影）。
	child.UpdatedAtMS = parent.UpdatedAtMS
	if _, err := store.Restore(context.Background(), child, 1_000); err != nil {
		t.Fatal(err)
	}
	result, err := store.Restore(context.Background(), parent, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CircuitMutationApplied {
		t.Fatalf("父恢复必须 applied: %s", result.Status)
	}
	childState, err := store.Get(context.Background(), childScope, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if childState.ShadowedByIncidentID != "parent-incident" {
		t.Fatalf("子必须被父 shadow: %#v", childState)
	}
	// 幂等重放父状态：related 投影复现。
	result, err = store.Restore(context.Background(), parent, 1_000)
	if err != nil || result.Status != CircuitMutationIdempotent {
		t.Fatalf("同代同刻幂等: %#v %v", result, err)
	}

	// 父 CLOSED 关闭时 unshadow 子（同代更新必须递增 UpdatedAtMS）。
	closedParent := parent
	closedParent.Phase = CircuitPhaseClosed
	closedParent.UpdatedAtMS = 2_000
	result, err = store.Restore(context.Background(), closedParent, 2_000)
	if err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("父关闭: %#v %v", result, err)
	}
	childState, err = store.Get(context.Background(), childScope, 2_000)
	if err != nil {
		t.Fatal(err)
	}
	if childState.ShadowedByIncidentID != "" {
		t.Fatalf("父关闭必须 unshadow 子: %#v", childState)
	}
}

func TestW7DMemoryStoreRestoreValidation(t *testing.T) {
	store := w7dMemoryStore(t, 8)
	scope := accountScope("val-1")
	// ConfirmationFailuresRequired 非法。
	bad := w7dSuspectRaw(t, scope, 1)
	nine := 9
	bad.ConfirmationFailuresRequired = &nine
	if _, err := store.Restore(context.Background(), bad, 1); err == nil {
		t.Fatal("非法 required 必须拒绝")
	}
	// scopeKey 不匹配。
	mismatch := w7dSuspectRaw(t, scope, 1)
	mismatch.ScopeKey = "wrong"
	if _, err := store.Restore(context.Background(), mismatch, 1); err == nil {
		t.Fatal("scopeKey 不匹配必须拒绝")
	}
	// 旧 generation 幂等回落。
	newer := w7dSuspectRaw(t, scope, 5)
	if _, err := store.Restore(context.Background(), newer, 1_000); err != nil {
		t.Fatal(err)
	}
	result, err := store.Restore(context.Background(), w7dSuspectRaw(t, scope, 1), 1_000)
	if err != nil || result.Status != CircuitMutationIdempotent {
		t.Fatalf("旧 generation 必须幂等: %#v %v", result, err)
	}
}

func int64Ptr(value int64) *int64 { return &value }

func intPtr(value int) *int { return &value }

// ---------------------------------------------------------------------------
// CircuitRecoveryService：错误臂与特殊判定
// ---------------------------------------------------------------------------

type w7dFailingCircuitStore struct {
	MemoryCircuitStore
	listDueErr  error
	completeErr error
}

func (s *w7dFailingCircuitStore) ListDue(context.Context, int64, int) ([]CircuitState, error) {
	if s.listDueErr != nil {
		return nil, s.listDueErr
	}
	return s.MemoryCircuitStore.ListDue(context.Background(), 1_000, 10)
}

func TestW7DRecoveryServiceConstructorMatrix(t *testing.T) {
	store := w7dMemoryStore(t, 4)
	resolver := staticResolver(CircuitRecoveryProbeTarget{}, true)
	nowMS := func() int64 { return 1 }
	if _, err := NewCircuitRecoveryService(nil, resolver, CircuitRecoveryServiceOptions{NowMS: nowMS}); err == nil {
		t.Fatal("nil store 必须拒绝")
	}
	if _, err := NewCircuitRecoveryService(store, nil, CircuitRecoveryServiceOptions{NowMS: nowMS}); err == nil {
		t.Fatal("nil resolver 必须拒绝")
	}
	negativeInt := -1
	negativeInt64 := int64(-1)
	if _, err := NewCircuitRecoveryService(store, resolver, CircuitRecoveryServiceOptions{BatchSize: negativeInt, NowMS: nowMS}); err == nil {
		t.Fatal("batchSize<1 必须拒绝")
	}
	if _, err := NewCircuitRecoveryService(store, resolver, CircuitRecoveryServiceOptions{LeaseDurationMS: negativeInt64, NowMS: nowMS}); err == nil {
		t.Fatal("leaseDuration<1 必须拒绝")
	}
	if _, err := NewCircuitRecoveryService(store, resolver, CircuitRecoveryServiceOptions{Concurrency: negativeInt, NowMS: nowMS}); err == nil {
		t.Fatal("concurrency<1 必须拒绝")
	}
	if _, err := NewCircuitRecoveryService(store, resolver, CircuitRecoveryServiceOptions{}); err == nil {
		t.Fatal("缺时钟必须拒绝")
	}
	if randomID := NewRandomID(); len(randomID) != 32 {
		t.Fatalf("随机 ID 必须 128 位十六进制（32 字符）: %d", len(randomID))
	}
	if RuntimeAccountIDFromKey("acc:authorized:sys") != "acc" {
		t.Fatal("首个冒号前缀必须提取")
	}
	if RuntimeAccountIDFromKey("plain") != "plain" {
		t.Fatal("无冒号原样返回")
	}
}

func TestW7DRecoveryServiceSweepErrorsAndSkip(t *testing.T) {
	nowMS := func() int64 { return 1_000 }
	// ListDue 错误。
	failing := &w7dFailingCircuitStore{listDueErr: errors.New("w7d: list due boom")}
	service, err := NewCircuitRecoveryService(failing, staticResolver(CircuitRecoveryProbeTarget{}, true), CircuitRecoveryServiceOptions{NowMS: nowMS})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Sweep(context.Background()); err == nil || !strings.Contains(err.Error(), "读取账户电路到期状态失败") {
		t.Fatalf("ListDue 错误必须暴露: %v", err)
	}

	// 非 SUSPECT/OPEN/RECOVERING 状态跳过。
	store := w7dMemoryStore(t, 4)
	scope := accountScope("skip-a")
	closed := closedAccountCircuitState(scope, "7", 1, "tx", 1_000)
	if _, err := store.Restore(context.Background(), closed, 1_000); err != nil {
		t.Fatal(err)
	}
	service, err = NewCircuitRecoveryService(store, staticResolver(CircuitRecoveryProbeTarget{}, true), CircuitRecoveryServiceOptions{NowMS: nowMS})
	if err != nil {
		t.Fatal(err)
	}
	// HALF_OPEN 无租约时 circuitDueAtMS 为永不到期：不得进入 Sweep 批次。
	halfOpen := openState(t, scope, 2, "7", 1_000)
	halfOpen.Phase = CircuitPhaseHalfOpen
	halfOpen.RetryAtMS = int64Ptr(500)
	if _, err := store.Restore(context.Background(), halfOpen, 1_000); err != nil {
		t.Fatal(err)
	}
	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.DueCount != 0 {
		t.Fatalf("HALF_OPEN 无租约不得视为到期: %#v", result)
	}

	// 取消的 ctx：Sweep 以取消错误结束。
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Sweep(canceled); err == nil {
		t.Fatal("取消的 Sweep 必须报错")
	}
}

func TestW7DRecoveryServiceResolveArms(t *testing.T) {
	nowMS := func() int64 { return 1_000 }
	counter := 0
	newSeededStore := func(t *testing.T) *MemoryCircuitStore {
		store := w7dMemoryStore(t, 8)
		scope := accountScope("resolve-a")
		state := suspectState(t, scope, 1, "7", 1_000)
		state.RetryAtMS = int64Ptr(500)
		if _, err := store.Restore(context.Background(), state, 1_000); err != nil {
			t.Fatal(err)
		}
		return store
	}
	store := newSeededStore(t)
	options := CircuitRecoveryServiceOptions{BatchSize: 10, LeaseDurationMS: 60_000, NowMS: nowMS, CreateID: func() string {
		counter++
		return "id-" + string(rune('a'+counter))
	}}

	// resolver 错误 → releaseUnknown 以 unknown 完成 → 聚合错误。
	resolveBoom := errors.New("w7d: resolve boom")
	service, err := NewCircuitRecoveryService(store, func(context.Context, CircuitState) (CircuitRecoveryProbeTarget, bool, error) {
		return CircuitRecoveryProbeTarget{}, false, resolveBoom
	}, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Sweep(context.Background()); err == nil || !strings.Contains(err.Error(), "w7d: resolve boom") {
		t.Fatalf("resolve 错误必须聚合暴露: %v", err)
	}

	// target 缺失 → unknown 计数，无错误（独立 store 避免 resolver 错误臂副作用）。
	store2 := newSeededStore(t)
	service, err = NewCircuitRecoveryService(store2, staticResolver(CircuitRecoveryProbeTarget{}, false), options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.UnknownCount != 1 || result.LeasedCount != 1 {
		t.Fatalf("缺失目标必须 unknown: %#v", result)
	}

	// revision 漂移 → fenced（ReplaceDispatchRevision applied）。
	store3 := newSeededStore(t)
	drifted := staticResolver(CircuitRecoveryProbeTarget{DispatchRevision: "9"}, true)
	service, err = NewCircuitRecoveryService(store3, drifted, options)
	if err != nil {
		t.Fatal(err)
	}
	result, err = service.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.FencedCount != 1 {
		t.Fatalf("revision 漂移必须 fenced: %#v", result)
	}
}

func TestW7DRecoveryServiceProbeArmsAndEscalationClear(t *testing.T) {
	nowMS := func() int64 { return 1_000 }
	store := w7dMemoryStore(t, 8)
	// protocol_model SUSPECT：semantic 失败触发 ClearAccountEscalationEvidence。
	scope := protocolModelScope("esc-a", "p", "text", "b")
	state := suspectState(t, scope, 1, "7", 1_000)
	state.RetryAtMS = int64Ptr(500)
	if _, err := store.Restore(context.Background(), state, 1_000); err != nil {
		t.Fatal(err)
	}
	status := 200
	semanticFailure := staticResolver(CircuitRecoveryProbeTarget{DispatchRevision: "7", Probe: func(context.Context) (TransportProbeOutcome, error) {
		return TransportProbeOutcome{Kind: ProbeOutcomeFramingComplete, StatusCode: &status, SemanticSuccess: boolPtr(false)}, nil
	}}, true)
	options := CircuitRecoveryServiceOptions{BatchSize: 10, LeaseDurationMS: 60_000, NowMS: nowMS}
	service, err := NewCircuitRecoveryService(store, semanticFailure, options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.FramingCompleteCount != 1 {
		t.Fatalf("语义失败计入 framing_complete: %#v", result)
	}

	// probe 错误 → releaseUnknown → 聚合错误。
	probeBoom := errors.New("w7d: probe boom")
	boomStore := w7dMemoryStore(t, 8)
	scope2 := accountScope("esc-b")
	state2 := suspectState(t, scope2, 1, "7", 1_000)
	state2.RetryAtMS = int64Ptr(500)
	if _, err := boomStore.Restore(context.Background(), state2, 1_000); err != nil {
		t.Fatal(err)
	}
	service, err = NewCircuitRecoveryService(boomStore, staticResolver(CircuitRecoveryProbeTarget{DispatchRevision: "7", Probe: func(context.Context) (TransportProbeOutcome, error) {
		return TransportProbeOutcome{}, probeBoom
	}}, true), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Sweep(context.Background()); err == nil || !strings.Contains(err.Error(), "w7d: probe boom") {
		t.Fatalf("probe 错误必须聚合: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ControlPlaneMaintenance：错误臂
// ---------------------------------------------------------------------------

type w7dFailingCursorStore struct{}

func (w7dFailingCursorStore) Load(context.Context) (*IncidentCursor, error) {
	return nil, errors.New("w7d: cursor load boom")
}
func (w7dFailingCursorStore) Save(context.Context, IncidentCursor) error {
	return errors.New("w7d: cursor save boom")
}

type w7dFailingOutbox struct {
	claimErr   error
	ackErr     error
	releaseErr error
}

func (f *w7dFailingOutbox) Claim(context.Context, string, int64, int64, int) ([]OutboxEvent, error) {
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	return nil, nil
}
func (f *w7dFailingOutbox) Ack(context.Context, OutboxEvent, int64) (bool, error) {
	return false, f.ackErr
}
func (f *w7dFailingOutbox) ReleaseForReplay(context.Context, OutboxEvent, string, int64, int64) error {
	return f.releaseErr
}

func TestW7DControlPlaneConstructorGuards(t *testing.T) {
	store := w7dMemoryStore(t, 4)
	ledger := &fakeLedger{}
	outbox := &fakeOutbox{}
	nowMS := func() int64 { return 1 }
	if _, err := NewControlPlaneMaintenance(nil, ledger, outbox, ControlPlaneOptions{NowMS: nowMS}); err == nil {
		t.Fatal("nil store 必须拒绝")
	}
	if _, err := NewControlPlaneMaintenance(store, nil, outbox, ControlPlaneOptions{NowMS: nowMS}); err == nil {
		t.Fatal("nil ledger 必须拒绝")
	}
	if _, err := NewControlPlaneMaintenance(store, ledger, nil, ControlPlaneOptions{NowMS: nowMS}); err == nil {
		t.Fatal("nil outbox 必须拒绝")
	}
	if _, err := NewControlPlaneMaintenance(store, ledger, outbox, ControlPlaneOptions{}); err == nil {
		t.Fatal("缺时钟必须拒绝")
	}
	if _, err := NewControlPlaneMaintenance(store, ledger, outbox, ControlPlaneOptions{NowMS: nowMS, RetryDelayMS: -1}); err == nil {
		t.Fatal("负数值必须拒绝")
	}
	maintenance, err := NewControlPlaneMaintenance(store, ledger, outbox, ControlPlaneOptions{NowMS: nowMS})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(maintenance.ownerID, "circuit-bridge:") {
		t.Fatalf("ownerID 必须自动生成: %s", maintenance.ownerID)
	}
	// rebuildError 的 Error 文本即 reason。
	if (&rebuildError{reason: "x"}).Error() != "x" {
		t.Fatal("rebuildError 文本不正确")
	}
}

func TestW7DControlPlaneProjectPendingArms(t *testing.T) {
	store := w7dMemoryStore(t, 8)
	scopeKey, _ := AccountCircuitScopeKey(accountScope("cp-a"))
	incident := incidentRow(scopeKey, accountScope("cp-a"), CircuitIncidentOpen, 1_000)
	ledger := &fakeLedger{byScopeKey: map[string]CircuitIncidentRecord{scopeKey: incident}}

	// limit<1。
	maintenance, _ := newTestControlPlane(t, store, ledger, &fakeOutbox{}, nil)
	if _, err := maintenance.ProjectPending(context.Background(), 0); err == nil {
		t.Fatal("limit<1 必须拒绝")
	}

	// Claim 错误。
	failing := &w7dFailingOutbox{claimErr: errors.New("w7d: claim boom")}
	maintenance, _ = newTestControlPlane(t, store, ledger, failing, nil)
	if _, err := maintenance.ProjectPending(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "认领账户电路 outbox 失败") {
		t.Fatalf("claim 错误必须包装暴露: %v", err)
	}

	// dispatch_revision_changed 事件直达 ReplaceAccountDispatchRevision。
	if _, err := store.Restore(context.Background(), w7dSuspectRaw(t, accountScope("cp-b"), 1), 1_000); err != nil {
		t.Fatal(err)
	}
	outbox2 := &fakeOutbox{claims: [][]OutboxEvent{{{EventID: "e1", EventType: "dispatch_revision_changed", AccountRuntimeKey: "cp-b", DispatchRevision: 9, TransitionID: "tx-9"}}}}
	maintenance, _ = newTestControlPlane(t, store, ledger, outbox2, nil)
	count, err := maintenance.ProjectPending(context.Background(), 5)
	if err != nil || count != 1 {
		t.Fatalf("revision 事件必须 ack: %d %v", count, err)
	}

	// incident_projected 缺 scopeKey → release 重放。
	outbox3 := &fakeOutbox{claims: [][]OutboxEvent{{{EventID: "e2", EventType: "incident_projected"}}}}
	maintenance, _ = newTestControlPlane(t, store, ledger, outbox3, nil)
	if _, err := maintenance.ProjectPending(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if len(outbox3.released) != 1 || len(outbox3.acked) != 0 {
		t.Fatalf("投影失败必须 release: released=%d acked=%d", len(outbox3.released), len(outbox3.acked))
	}

	// ledger 缺失 → release。
	outbox4 := &fakeOutbox{claims: [][]OutboxEvent{{{EventID: "e3", EventType: "incident_projected", CircuitScopeKey: "missing"}}}}
	maintenance, _ = newTestControlPlane(t, store, ledger, outbox4, nil)
	if _, err := maintenance.ProjectPending(context.Background(), 5); err != nil || len(outbox4.released) != 1 {
		t.Fatalf("缺失 ledger 必须 release: released=%d err=%v", len(outbox4.released), err)
	}
}

func TestW7DControlPlaneReconcileArms(t *testing.T) {
	store := w7dMemoryStore(t, 8)
	scopeKey, _ := AccountCircuitScopeKey(accountScope("cp-r"))
	incident := incidentRow(scopeKey, accountScope("cp-r"), CircuitIncidentOpen, 1_000)

	// 未 ready（未 rebuild）→ ReconcileActive no-op。
	maintenance, _ := newTestControlPlane(t, store, &fakeLedger{}, &fakeOutbox{}, nil)
	count, err := maintenance.ReconcileActive(context.Background(), 5)
	if err != nil || count != 0 {
		t.Fatalf("未 ready 必须跳过: %d %v", count, err)
	}

	// ready 后 cursor load 错误。
	failingCursor := &w7dFailingCursorStore{}
	maintenance, _ = newTestControlPlane(t, store, &fakeLedger{}, &fakeOutbox{}, failingCursor)
	if _, err := maintenance.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	reconciler := &w7dFailingOutbox{}
	maintenance, _ = newTestControlPlane(t, store, &fakeLedger{}, reconciler, failingCursor)
	maintenance.globallyReady = true
	if _, err := maintenance.ReconcileActive(context.Background(), 5); err == nil || !strings.Contains(err.Error(), "读取账户电路 reconcile 游标失败") {
		t.Fatalf("cursor load 错误必须暴露: %v", err)
	}

	// ready 后 cursor save 错误（页遍历完成时写入最后行游标）。
	ledgerWithItem := &fakeLedger{items: []CircuitIncidentRecord{incident}}
	saveFailing := &w7dSaveFailingCursorStore{}
	maintenance, _ = newTestControlPlane(t, store, ledgerWithItem, &fakeOutbox{}, saveFailing)
	maintenance.globallyReady = true
	if _, err := maintenance.ReconcileActive(context.Background(), 5); err == nil || !strings.Contains(err.Error(), "持久化账户电路 reconcile 游标失败") {
		t.Fatalf("cursor save 错误必须暴露: %v", err)
	}

	// limit<1 在 ready 后拒绝。
	maintenance, _ = newTestControlPlane(t, store, &fakeLedger{}, &fakeOutbox{}, nil)
	if _, err := maintenance.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.ReconcileActive(context.Background(), 0); err == nil {
		t.Fatal("ready 后 limit<1 必须拒绝")
	}

	// RunMaintenance：EnsureRuntimeStateReady 走 Rebuild（空 ledger 成功 ready）。
	maintenance, _ = newTestControlPlane(t, store, &fakeLedger{}, &fakeOutbox{}, nil)
	total, err := maintenance.RunMaintenance(context.Background(), 5)
	if err != nil || !maintenance.IsReady() {
		t.Fatalf("RunMaintenance 必须完成冷启动: %d %v", total, err)
	}
}

type w7dSaveFailingCursorStore struct{ fakeCursorStore }

func (w7dSaveFailingCursorStore) Load(context.Context) (*IncidentCursor, error) { return nil, nil }
func (w7dSaveFailingCursorStore) Save(context.Context, IncidentCursor) error {
	return errors.New("w7d: save boom")
}

func TestW7DControlPlaneRebuildErrorArms(t *testing.T) {
	store := w7dMemoryStore(t, 8)

	// ledger 页序列非单调 → invalid cursor。
	scopeRB := accountScope("rb-a")
	keyRB, _ := AccountCircuitScopeKey(scopeRB)
	nonMonotonic := &fakeLedger{forcePages: []RebuildPage{
		{Items: []CircuitIncidentRecord{incidentRow(keyRB, scopeRB, CircuitIncidentOpen, 2_000)}, NextCursor: &IncidentCursor{UpdatedAtMS: 2_000, CircuitScopeKey: keyRB}},
		{Items: nil, NextCursor: &IncidentCursor{UpdatedAtMS: 1_000, CircuitScopeKey: keyRB + "-0"}},
	}}
	maintenance, _ := newTestControlPlane(t, store, nonMonotonic, &fakeOutbox{}, nil)
	result, err := maintenance.Rebuild(context.Background())
	if err == nil || !result.Blocked || result.Reason != RebuildReasonInvalidCursor {
		t.Fatalf("非单调游标必须 invalid: %#v %v", result, err)
	}

	// rebuild 中并发重入被拒绝。
	slowLedger := &w7dSlowLedger{inner: &fakeLedger{}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	maintenance, _ = newTestControlPlane(t, store, slowLedger, &fakeOutbox{}, nil)
	done := make(chan struct{})
	go func() { _, _ = maintenance.Rebuild(context.Background()); close(done) }()
	<-slowLedger.entered
	if _, err := maintenance.Rebuild(context.Background()); err == nil || !strings.Contains(err.Error(), "已在进行") {
		t.Fatalf("并发 rebuild 必须拒绝: %v", err)
	}
	close(slowLedger.release)
	<-done

	// EnsureRuntimeStateReady 在 rebuild 失败 reason 存在时不吞原始错误。
	blocked := &fakeLedger{forcePages: []RebuildPage{
		{Items: []CircuitIncidentRecord{incidentRow("bad", CircuitScope{Kind: "weird", AccountRuntimeKey: "x"}, CircuitIncidentOpen, 1_000)}},
	}}
	maintenance, _ = newTestControlPlane(t, store, blocked, &fakeOutbox{}, nil)
	ready, err := maintenance.EnsureRuntimeStateReady(context.Background())
	if ready {
		t.Fatal("blocked rebuild 不得 ready")
	}
	if err != nil {
		t.Fatalf("带 reason 的 rebuild 失败由 EnsureRuntimeStateReady 吞掉: %v", err)
	}

	// classifyControlPlaneError：64 字符截断与空文本回落。
	long := strings.Repeat("长", 64) + "extra"
	if got := classifyControlPlaneError(errors.New(long)); len([]rune(got)) > 64 {
		t.Fatalf("错误分类必须截断 64: %d", len([]rune(got)))
	}
	if got := classifyControlPlaneError(errors.New("   ")); got != "projector_error" {
		t.Fatalf("空白回落 projector_error: %q", got)
	}
}

type w7dSlowLedger struct {
	inner   *fakeLedger
	entered chan struct{}
	release chan struct{}
}

// entered/release 在首次调用时初始化，便于测试同步。
func (l *w7dSlowLedger) ListForRebuild(ctx context.Context, query RebuildPageQuery) (RebuildPage, error) {
	if l.entered != nil {
		l.entered <- struct{}{}
		<-l.release
		return l.inner.ListForRebuild(ctx, query)
	}
	return l.inner.ListForRebuild(ctx, query)
}
func (l *w7dSlowLedger) ListByRuntimeKeys(ctx context.Context, keys []string, include bool, now int64) ([]CircuitIncidentRecord, error) {
	return l.inner.ListByRuntimeKeys(ctx, keys, include, now)
}
func (l *w7dSlowLedger) GetByScopeKey(ctx context.Context, key string) (*CircuitIncidentRecord, error) {
	return l.inner.GetByScopeKey(ctx, key)
}

func TestW7DCursorAndScopeHelpers(t *testing.T) {
	left := IncidentCursor{UpdatedAtMS: 1, CircuitScopeKey: "a"}
	if compareCursor(left, IncidentCursor{UpdatedAtMS: 2, CircuitScopeKey: "a"}) != -1 {
		t.Fatal("时间早必须 -1")
	}
	if compareCursor(IncidentCursor{UpdatedAtMS: 2, CircuitScopeKey: "a"}, left) != 1 {
		t.Fatal("时间晚必须 1")
	}
	if compareCursor(left, IncidentCursor{UpdatedAtMS: 1, CircuitScopeKey: "b"}) != -1 {
		t.Fatal("scopeKey 早必须 -1")
	}
	if compareCursor(left, IncidentCursor{UpdatedAtMS: 1, CircuitScopeKey: ""}) != 1 {
		t.Fatal("scopeKey 晚必须 1")
	}
	if compareCursor(left, left) != 0 {
		t.Fatal("相同必须 0")
	}
	if _, err := requiredText("  ", "fingerprint"); err == nil {
		t.Fatal("空白必填文本必须拒绝")
	}
	if got, err := requiredText(" x ", "fingerprint"); err != nil || got != "x" {
		t.Fatalf("必填文本必须 trim: %q %v", got, err)
	}
	// hasUnresolvedChildIncident：缺失子映射返回 true。
	incident := CircuitIncidentRecord{AccountRuntimeKey: "acc", ChildIncidentIDs: []string{"child-1"}}
	if !hasUnresolvedChildIncident(incident, map[string]string{}) {
		t.Fatal("缺失子必须未解决")
	}
	resolved := incidentScopeKeyMap([]CircuitIncidentRecord{incident, {AccountRuntimeKey: "acc", IncidentID: "child-1", CircuitScopeKey: "k"}})
	if hasUnresolvedChildIncident(incident, resolved) {
		t.Fatal("解析后必须已解决")
	}
}
