package circuitstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// newTestStore 构造 miniredis 驱动的 OpsJobsStore（容量 8 便于容量边界测试）。
func newTestStore(t *testing.T, capacity int64, now func() int64) (*OpsJobsStore, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	store, err := NewRedisStore(RedisStoreOptions{
		RedisURL:  "redis://" + server.Addr(),
		Namespace: "dev-space",
		Capacity:  capacity,
		Now:       now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewOpsJobsStore(store), server
}

func fixedNow() func() int64 {
	current := int64(1_000_000)
	return func() int64 { return current }
}

func accountScope(accountRuntimeKey string) opsjobs.CircuitScope {
	return opsjobs.CircuitScope{Kind: opsjobs.CircuitScopeAccount, AccountRuntimeKey: accountRuntimeKey}
}

// TestKeysMatchGatewayKeySpace 锁定键空间：与 Node/gateway 网关布局逐段一致
// （redisNamespacedKey 展开 + gateway-account-circuit 默认名 + 五个子键）。
func TestKeysMatchGatewayKeySpace(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := NewRedisStore(RedisStoreOptions{
		RedisURL:  "redis://" + server.Addr(),
		Namespace: "dev-space",
		Capacity:  1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	states, due, closed, escalation, saturated := store.Keys()
	prefix := "juhe-ai:dev-space:account-circuit:gateway-account-circuit"
	if states != prefix+":states" || due != prefix+":due" || closed != prefix+":closed" ||
		escalation != prefix+":escalation" || saturated != prefix+":capacity-saturated" {
		t.Fatalf("键空间与网关布局不一致: %s %s %s %s %s", states, due, closed, escalation, saturated)
	}
}

// TestScopeKeyMatchesOpsJobsContract 验证 wire 层 scope key 与 opsjobs 契约
// （Node 长度前缀编码）逐字节一致。
func TestScopeKeyMatchesOpsJobsContract(t *testing.T) {
	scopes := []opsjobs.CircuitScope{
		accountScope("acc-1"),
		{Kind: opsjobs.CircuitScopeKey, AccountRuntimeKey: "acc-1", KeyFingerprint: "fp"},
		{Kind: opsjobs.CircuitScopeProtocolModel, AccountRuntimeKey: "acc:1", ProtocolProfile: "openai", RequestLane: "text", ModelBucket: "gpt-4o"},
	}
	for _, scope := range scopes {
		want, err := opsjobs.AccountCircuitScopeKey(scope)
		if err != nil {
			t.Fatal(err)
		}
		got := MustScopeKey(convertScope(scope))
		if got != want {
			t.Fatalf("scopeKey 不一致: got %q want %q", got, want)
		}
	}
}

// TestStateConversionRoundTrip 验证 opsjobs.CircuitState ⇄ wire State 转换
// 保留全部语义字段（指针、切片、可选文本）。
func TestStateConversionRoundTrip(t *testing.T) {
	required := 2
	count := 1
	openedAt := int64(111)
	retryAt := int64(222)
	updatedAt := int64(333)
	leaseUntil := int64(999)
	state := opsjobs.CircuitState{
		ScopeKey:                     MustScopeKey(convertScope(accountScope("acc-1"))),
		Scope:                        accountScope("acc-1"),
		Phase:                        opsjobs.CircuitPhaseHalfOpen,
		Generation:                   4,
		DispatchRevision:             "7",
		TransitionID:                 "t-1",
		BackoffAttempt:               2,
		RecoverySuccessCount:         1,
		ConfirmationFailuresRequired: &required,
		ConfirmationFailureCount:     &count,
		FailureEvidenceKeys:          []string{"a", "b"},
		OpenedAtMS:                   &openedAt,
		RetryAtMS:                    &retryAt,
		FailureReason:                "background_probe:connection:http_502",
		Lease:                        &opsjobs.CircuitLease{Kind: opsjobs.CircuitLeaseHalfOpen, LeaseID: "lease-1", LeaseUntilMS: leaseUntil},
		HalfOpenOrigin:               "OPEN",
		IncidentID:                   "incident-1",
		ShadowedByIncidentID:         "parent-1",
		ChildIncidentIDs:             []string{"child-1"},
		ChildScopeKeys:               []string{"child-scope"},
		RequiredRecoveryScopeKeys:    []string{"child-scope"},
		RecoveryEvidenceScopeKeys:    []string{"child-scope"},
		UpdatedAtMS:                  updatedAt,
	}
	converted, err := toOpsState(fromOpsState(state))
	if err != nil {
		t.Fatal(err)
	}
	if converted.ScopeKey != state.ScopeKey || converted.Phase != state.Phase ||
		converted.Generation != state.Generation || converted.DispatchRevision != state.DispatchRevision ||
		converted.TransitionID != state.TransitionID || converted.BackoffAttempt != state.BackoffAttempt ||
		converted.RecoverySuccessCount != state.RecoverySuccessCount || converted.UpdatedAtMS != state.UpdatedAtMS {
		t.Fatalf("标量字段丢失: %+v", converted)
	}
	if *converted.ConfirmationFailuresRequired != required || *converted.ConfirmationFailureCount != count {
		t.Fatalf("确认计数丢失: %+v", converted)
	}
	if len(converted.FailureEvidenceKeys) != 2 || len(converted.ChildScopeKeys) != 1 {
		t.Fatalf("列表字段丢失: %+v", converted)
	}
	if converted.Lease == nil || converted.Lease.LeaseID != "lease-1" || converted.Lease.LeaseUntilMS != leaseUntil {
		t.Fatalf("租约丢失: %+v", converted)
	}
	if converted.OpenedAtMS == nil || *converted.OpenedAtMS != openedAt || converted.RetryAtMS == nil || *converted.RetryAtMS != retryAt {
		t.Fatalf("时间字段丢失: %+v", converted)
	}
	if converted.FailureReason != state.FailureReason || converted.HalfOpenOrigin != "OPEN" ||
		converted.IncidentID != "incident-1" || converted.ShadowedByIncidentID != "parent-1" {
		t.Fatalf("文本字段丢失: %+v", converted)
	}
}

// TestCircuitLifecycleThroughOpsPort 在 miniredis 上跑完整电路生命周期
// （suspect 语义由网关侧负责；jobs 侧从已 suspect 的状态开始走恢复链）：
// 植入 SUSPECT 状态 → acquire confirmation → complete framing(closed) →
// 关闭保留 → ListDue 收敛 → Restore 幂等 → ReplaceAccountDispatchRevision。
func TestCircuitLifecycleThroughOpsPort(t *testing.T) {
	now := fixedNow()
	store, _ := newTestStore(t, 8, now)
	ctx := context.Background()
	scope := accountScope("acc-1")

	// 不存在状态上的 identity 转移必须返回 not_found（状态机契约）。
	warm, err := store.AcquireCanaryLease(ctx, opsjobs.CircuitTransitionIdentity{
		Scope: scope, Generation: 99, DispatchRevision: "3", TransitionID: "warm", NowMS: now(),
	}, opsjobs.CircuitLeaseSpec{LeaseID: "warm", LeaseUntilMS: now() + 1000})
	if err != nil {
		t.Fatal(err)
	}
	if warm.Status != opsjobs.CircuitMutationNotFound {
		t.Fatalf("不存在状态 acquire 应返回 not_found: %s", warm.Status)
	}

	// 植入 SUSPECT 状态（模拟网关 suspect 后的到期恢复场景）。
	suspect := opsjobs.CircuitState{
		ScopeKey:                     MustScopeKey(convertScope(scope)),
		Scope:                        scope,
		Phase:                        opsjobs.CircuitPhaseSuspect,
		Generation:                   1,
		DispatchRevision:             "3",
		TransitionID:                 "incident-1",
		IncidentID:                   "incident-1",
		ConfirmationFailuresRequired: &[]int{2}[0],
		ConfirmationFailureCount:     &[]int{1}[0],
		FailureEvidenceKeys:          []string{suspectEvidenceKey()},
		RetryAtMS:                    &[]int64{now() - 10}[0],
		UpdatedAtMS:                  now() - 20,
	}
	restored, err := store.Restore(ctx, suspect, now())
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != opsjobs.CircuitMutationApplied {
		t.Fatalf("植入 SUSPECT 失败: %s", restored.Status)
	}

	// ListDue：retryAt 已到期 → 必须返回该状态。
	due, err := store.ListDue(ctx, now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Phase != opsjobs.CircuitPhaseSuspect {
		t.Fatalf("ListDue 应返回到期 SUSPECT: %+v", due)
	}

	// acquire confirmation lease。
	identity := opsjobs.CircuitTransitionIdentity{
		Scope: scope, Generation: 1, DispatchRevision: "3", TransitionID: "tr-acquire", NowMS: now(),
	}
	lease := opsjobs.CircuitLeaseSpec{LeaseID: "lease-1", LeaseUntilMS: now() + 5_000}
	acquired, err := store.AcquireConfirmationLease(ctx, identity, lease)
	if err != nil {
		t.Fatal(err)
	}
	if acquired.Status != opsjobs.CircuitMutationApplied || acquired.State.Lease == nil {
		t.Fatalf("acquire confirmation 失败: %s", acquired.Status)
	}

	// complete confirmation with framing_complete/closed → CLOSED。
	completeIdentity := identity
	completeIdentity.TransitionID = "tr-complete"
	completed, err := store.CompleteConfirmation(ctx, completeIdentity, "lease-1", opsjobs.CircuitCompletion{
		Outcome:                    opsjobs.CircuitVerdictFramingComplete,
		FramingCompleteDisposition: "closed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != opsjobs.CircuitMutationApplied || completed.State.Phase != opsjobs.CircuitPhaseClosed {
		t.Fatalf("complete confirmation 失败: %s %s", completed.Status, completed.State.Phase)
	}

	// CLOSED 不再到期。
	due, err = store.ListDue(ctx, now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("CLOSED 不应到期: %+v", due)
	}

	// Restore 同代幂等。
	replayed, err := store.Restore(ctx, suspect, now())
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Status != opsjobs.CircuitMutationIdempotent {
		t.Fatalf("同代 Restore 应幂等: %s", replayed.Status)
	}

	// ReplaceAccountDispatchRevision 关闭账户族全部作用域并返回计数。
	changed, err := store.ReplaceAccountDispatchRevision(ctx, "acc-1", "4", "tr-revision", now())
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Fatalf("revision 推进应关闭 1 个作用域，得到 %d", changed)
	}

	// ClearAccountEscalationEvidence：无证据返回 false（不报错）。
	cleared, err := store.ClearAccountEscalationEvidence(ctx, "acc-1", "4", "evidence-1", now())
	if err != nil {
		t.Fatal(err)
	}
	if cleared {
		t.Fatal("无升级证据时不得报告清除成功")
	}
}

func suspectEvidenceKey() string {
	return "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}

// TestCanaryRecoveringFlow 覆盖 OPEN → acquire canary → complete canary
// framing_complete → RECOVERING（halOpenOrigin=OPEN 时进入 recovering）。
func TestCanaryRecoveringFlow(t *testing.T) {
	now := fixedNow()
	store, _ := newTestStore(t, 8, now)
	ctx := context.Background()
	scope := accountScope("acc-2")

	openState := opsjobs.CircuitState{
		ScopeKey:         MustScopeKey(convertScope(scope)),
		Scope:            scope,
		Phase:            opsjobs.CircuitPhaseOpen,
		Generation:       2,
		DispatchRevision: "5",
		TransitionID:     "incident-2",
		IncidentID:       "incident-2",
		BackoffAttempt:   1,
		RetryAtMS:        &[]int64{now() - 5}[0],
		OpenedAtMS:       &[]int64{now() - 100}[0],
		UpdatedAtMS:      now() - 10,
	}
	if _, err := store.Restore(ctx, openState, now()); err != nil {
		t.Fatal(err)
	}
	identity := opsjobs.CircuitTransitionIdentity{
		Scope: scope, Generation: 2, DispatchRevision: "5", TransitionID: "tr-canary", NowMS: now(),
	}
	acquired, err := store.AcquireCanaryLease(ctx, identity, opsjobs.CircuitLeaseSpec{LeaseID: "lease-2", LeaseUntilMS: now() + 5_000})
	if err != nil {
		t.Fatal(err)
	}
	if acquired.Status != opsjobs.CircuitMutationApplied || acquired.State.Phase != opsjobs.CircuitPhaseHalfOpen {
		t.Fatalf("acquire canary 失败: %s %s", acquired.Status, acquired.State.Phase)
	}
	completeIdentity := identity
	completeIdentity.TransitionID = "tr-canary-complete"
	completed, err := store.CompleteCanary(ctx, completeIdentity, "lease-2", opsjobs.CircuitCompletion{Outcome: opsjobs.CircuitVerdictFramingComplete})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != opsjobs.CircuitMutationApplied || completed.State.Phase != opsjobs.CircuitPhaseRecovering {
		t.Fatalf("complete canary 应进入 RECOVERING: %s %s", completed.Status, completed.State.Phase)
	}
	if completed.State.RecoverySuccessCount != 0 {
		t.Fatalf("recovering 初始成功计数应为 0: %d", completed.State.RecoverySuccessCount)
	}
}

// TestProbeStateSettleBySourceFence 在 miniredis 上验证 fence 结算：
// dispatchPending 状态 + fence 命中 → 结算成功；重复结算/未知 fence → false。
func TestProbeStateSettleBySourceFence(t *testing.T) {
	server := miniredis.RunT(t)
	probeStore, err := NewProbeStateStore("redis://"+server.Addr(), "dev-space", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = probeStore.Close() })
	ctx := context.Background()
	runtimeKey := AvailabilityProbeRuntimeKey("acc-1", "codex_source_avoidance", 3)
	if runtimeKey != "availability:acc-1:codex_source_avoidance:r3" {
		t.Fatalf("runtime key 形状不一致: %s", runtimeKey)
	}
	fence := ProbeSourceFence{StateKey: "k-1", AccountID: "acc-1", SourceGeneration: 7, SourceFenceID: "2f0a1b3c-4d5e-6f70-8192-a3b4c5d6e7f8"}
	if _, err := NormalizeSourceFence(fence); err != nil {
		t.Fatal(err)
	}
	dispatchPending := true
	state := probeState{
		RuntimeKey:             sanitizeProbeKeyPart(runtimeKey),
		Generation:             11,
		NextProbeAtMs:          1_000,
		AccountRuntimeScope:    "acc-1",
		ProbeKind:              "codex_source_avoidance",
		ConfigRevision:         3,
		DispatchPending:        &dispatchPending,
		DispatchPendingUntilMs: &[]int64{time.Now().UnixMilli() + 60_000}[0],
		SourceFences:           &[]string{encodeSourceFence(fence)},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Set(probeStore.stateKey(runtimeKey), string(raw)); err != nil {
		t.Fatal(err)
	}

	// 未知 fence 不得结算。
	otherFence := fence
	otherFence.SourceFenceID = "0a0a0a0a-0b0b-0c0c-0d0d-0e0e0e0e0e0e"
	settled, err := probeStore.SettleDispatchedBySourceFence(ctx, runtimeKey, 11, otherFence, ProbeOutcomeSuccess, nil)
	if err != nil || settled {
		t.Fatalf("未知 fence 不得结算: %v %v", settled, err)
	}
	// 正确 fence + success → 结算成功。
	settled, err = probeStore.SettleDispatchedBySourceFence(ctx, runtimeKey, 11, fence, ProbeOutcomeSuccess, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !settled {
		t.Fatal("命中的 source fence 必须可结算")
	}
	// 已有 outcome（terminal）→ 不得重复结算。
	settled, err = probeStore.SettleDispatchedBySourceFence(ctx, runtimeKey, 11, fence, ProbeOutcomeSuccess, nil)
	if err != nil || settled {
		t.Fatalf("已结算 generation 不得重复结算: %v %v", settled, err)
	}
}
