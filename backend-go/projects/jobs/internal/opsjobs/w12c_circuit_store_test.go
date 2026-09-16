// 波次 w12c：补齐 opsjobs 内存电路存储与纯函数分支覆盖。
//
// 不可达/防御守卫登记（无自然触发路径，不做无语义强注入）：
//   - circuitmemorystore.go AcquireCanaryLease / acquireLease / checkedEntry 中
//     entry==nil 的 notFound 分支：validateIdentity 恒先拦截 entry==nil 并返回
//     notFound，后续 nil 分支不可达。
//   - circuitmemorystore.go AcquireCanaryLease 的 lease!=nil mismatch 分支：
//     持有租约的条目相位必为 HALF_OPEN，其前的相位检查恒先拦截。
//   - circuitmemorystore.go CompleteConfirmation 中 Normalize/证据键/
//     ConfirmationFailureCount 的错误分支与证据截断分支：条目状态在
//     freshEntry → safeNormalizeConfirmationState 已归一；且累计达到 required
//     前必然先转 OPEN，证据数无法超过 required+1。
//   - circuitmemorystore.go normalizeConfirmationState 的证据键错误分支：
//     该错误仅来源于 required 校验，而 required 已先校验通过。
//   - circuitstore.go AccountCircuitBackoffDelayMS 的 windowMS<=0 分支：
//     CircuitBackoffMS 各档位的抖动窗口恒为正。
//   - probeoutcome.go real-attempt 的 connection 兜底分支：upstream 非空时
//     localFailureKind 恒非空（或 statusCode 已先返回），分支不可达。
//   - balancedetect.go autoDetectWithLease 的 attemptErr!=nil 且 kind!=retry
//     分支：DetectAccountBalanceAdapterAttempt 出错时恒返回 retry。
//   - balancedetect.go intervalMinutes<1 分支：来源为常量 5。
//   - balancedetect.go balanceAttemptRetry 且 NextRefreshAt==nil 的兜底：
//     NextRefreshAt==nil 时 completeBalanceDetectionIntent 恒返回 true。
package opsjobs

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

func w12cIdentity(scope CircuitScope, generation int64, revision, transitionID string, nowMS int64) CircuitTransitionIdentity {
	return CircuitTransitionIdentity{
		Scope:            scope,
		Generation:       generation,
		DispatchRevision: revision,
		TransitionID:     transitionID,
		NowMS:            nowMS,
	}
}

func TestW12CMemoryStoreCanaryLeaseGuards(t *testing.T) {
	store := w7dMemoryStore(t, 4)
	scope := w9hScope("acc-w12c-canary")
	if _, err := store.Restore(context.Background(), w7dSuspectRaw(t, scope, 2), 1_000); err != nil {
		t.Fatal(err)
	}
	lease := CircuitLeaseSpec{LeaseID: "l-w12c", LeaseUntilMS: 5_000}

	// 旧 generation 与旧 dispatchRevision 围栏。
	staleGen := w12cIdentity(scope, 1, "7", "tx-gen", 2_000)
	if result, err := store.AcquireCanaryLease(context.Background(), staleGen, lease); err != nil || result.Status != CircuitMutationStaleGeneration {
		t.Fatalf("stale generation=%#v err=%v", result, err)
	}
	staleRev := w12cIdentity(scope, 2, "5", "tx-rev", 2_000)
	if result, err := store.AcquireCanaryLease(context.Background(), staleRev, lease); err != nil || result.Status != CircuitMutationStaleDispatchRevision {
		t.Fatalf("stale revision=%#v err=%v", result, err)
	}

	// 未知 scope → notFound。
	missing := w12cIdentity(w9hScope("acc-w12c-absent"), 1, "7", "tx-miss", 2_000)
	if result, err := store.AcquireCanaryLease(context.Background(), missing, lease); err != nil || result.Status != CircuitMutationNotFound {
		t.Fatalf("not found=%#v err=%v", result, err)
	}

	// 同 transitionID 重放 → idempotent（canary 需要 OPEN 且到期的条目）。
	replayScope := w9hScope("acc-w12c-replay")
	replayOpen := openState(t, replayScope, 2, "7", 1_000)
	replayOpen.RetryAtMS = int64Ptr(1_500)
	if _, err := store.Restore(context.Background(), replayOpen, 1_000); err != nil {
		t.Fatal(err)
	}
	replay := w12cIdentity(replayScope, 2, "7", "tx-replay", 2_000)
	if result, err := store.AcquireCanaryLease(context.Background(), replay, lease); err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("first=%#v err=%v", result, err)
	}
	if result, err := store.AcquireCanaryLease(context.Background(), replay, lease); err != nil || result.Status != CircuitMutationIdempotent {
		t.Fatalf("replay=%#v err=%v", result, err)
	}
}

func TestW12CMemoryStoreCanaryCompletionArms(t *testing.T) {
	store := w7dMemoryStore(t, 8)
	scope := w9hScope("acc-w12c-arms")
	open := openState(t, scope, 3, "7", 1_000)
	open.RetryAtMS = int64Ptr(50_000)
	if _, err := store.Restore(context.Background(), open, 1_000); err != nil {
		t.Fatal(err)
	}
	lease := CircuitLeaseSpec{LeaseID: "canary-w12c", LeaseUntilMS: 59_000}

	// not due：RetryAt 在未来。
	notDue := w12cIdentity(scope, 3, "7", "tx-not-due", 2_000)
	if result, err := store.AcquireCanaryLease(context.Background(), notDue, lease); err != nil || result.Status != CircuitMutationNotDue {
		t.Fatalf("not due=%#v err=%v", result, err)
	}
	// RetryAt 到期后 canary 可获取；租约截止必须晚于当前时间。
	due := w12cIdentity(scope, 3, "7", "tx-canary", 51_000)
	if _, err := store.AcquireCanaryLease(context.Background(), due, CircuitLeaseSpec{LeaseID: "early", LeaseUntilMS: 50_999}); err == nil {
		t.Fatal("过期租约截止必须拒绝")
	}
	if result, err := store.AcquireCanaryLease(context.Background(), due, lease); err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("canary=%#v err=%v", result, err)
	}
	// 持有租约期间重复 acquire（新 transition）→ stateMismatch。
	held := w12cIdentity(scope, 3, "7", "tx-canary-held", 51_000)
	if result, err := store.AcquireCanaryLease(context.Background(), held, CircuitLeaseSpec{LeaseID: "canary-2", LeaseUntilMS: 9_000}); err != nil || result.Status != CircuitMutationStateMismatch {
		t.Fatalf("held lease=%#v err=%v", result, err)
	}

	// CompleteCanary：租约不匹配 → lease mismatch。
	wrongLease := w12cIdentity(scope, 3, "7", "tx-wrong-lease", 52_000)
	if result, err := store.CompleteCanary(context.Background(), wrongLease, "other-lease", CircuitCompletion{Outcome: CircuitVerdictFramingComplete}); err != nil || result.Status != CircuitMutationLeaseMismatch {
		t.Fatalf("lease mismatch=%#v err=%v", result, err)
	}
	// unknown → 回退 OPEN（HalfOpenOrigin=account:OPEN）。
	unknown := w12cIdentity(scope, 3, "7", "tx-unknown", 52_000)
	if result, err := store.CompleteCanary(context.Background(), unknown, "canary-w12c", CircuitCompletion{Outcome: CircuitVerdictUnknown}); err != nil || result.Status != CircuitMutationApplied || result.State.Phase != CircuitPhaseOpen {
		t.Fatalf("unknown=%#v err=%v", result, err)
	}

	// SUSPECT → confirmation lease → framing complete 非 closed → enterRecovering
	// （enterRecovering 的 SUSPECT backoff 归零分支）。
	if _, err := store.Restore(context.Background(), w7dSuspectRaw(t, scope, 4), 60_000); err != nil {
		t.Fatal(err)
	}
	suspectIdentity := w12cIdentity(scope, 4, "7", "tx-suspect", 61_000)
	if result, err := store.AcquireConfirmationLease(context.Background(), suspectIdentity, CircuitLeaseSpec{LeaseID: "conf-w12c", LeaseUntilMS: 70_000}); err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("confirm acquire=%#v err=%v", result, err)
	}
	complete := suspectIdentity
	complete.TransitionID = "tx-suspect-complete"
	result, err := store.CompleteConfirmation(context.Background(), complete, "conf-w12c", CircuitCompletion{Outcome: CircuitVerdictFramingComplete, FramingCompleteDisposition: "recovering"})
	if err != nil || result.Status != CircuitMutationApplied || result.State.Phase != CircuitPhaseRecovering {
		t.Fatalf("suspect recovering=%#v err=%v", result, err)
	}

	// RECOVERING → canary（origin=recovery）→ transport failure → open。
	recovering := w12cIdentity(scope, 5, "7", "tx-recovering", 80_000)
	open5 := openState(t, scope, 5, "7", 80_000)
	if _, err := store.Restore(context.Background(), open5, 80_000); err != nil {
		t.Fatal(err)
	}
	if result, err := store.AcquireCanaryLease(context.Background(), recovering, CircuitLeaseSpec{LeaseID: "rc-w12c", LeaseUntilMS: 90_000}); err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("recovering canary=%#v err=%v", result, err)
	}
	failure := recovering
	failure.TransitionID = "tx-recovering-fail"
	if result, err := store.CompleteCanary(context.Background(), failure, "rc-w12c", CircuitCompletion{Outcome: CircuitVerdictTransportFailure, Reason: "w12c-fail"}); err != nil || result.State.Phase != CircuitPhaseOpen {
		t.Fatalf("transport failure=%#v err=%v", result, err)
	}
}

func TestW12CMemoryStoreConfirmationFailureEscalation(t *testing.T) {
	store := w7dMemoryStore(t, 8)
	scope := w9hScope("acc-w12c-esc")
	required := 2
	zero := 0
	seed := w7dSuspectRaw(t, scope, 2)
	seed.ConfirmationFailuresRequired = &required
	seed.ConfirmationFailureCount = &zero
	if _, err := store.Restore(context.Background(), seed, 1_000); err != nil {
		t.Fatal(err)
	}
	// 第一次独立失败证据：计数 1 < required 2 → 保持 SUSPECT。
	first := w12cIdentity(scope, 2, "7", "tx-esc-1", 2_000)
	if result, err := store.AcquireConfirmationLease(context.Background(), first, CircuitLeaseSpec{LeaseID: "esc-1", LeaseUntilMS: 5_000}); err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("acquire1=%#v err=%v", result, err)
	}
	completeFirst := first
	completeFirst.TransitionID = "tx-esc-1-done"
	result, err := store.CompleteConfirmation(context.Background(), completeFirst, "esc-1", CircuitCompletion{Outcome: CircuitVerdictTransportFailure, FailureEvidenceKey: strings.Repeat("a", 64), Reason: "w12c-esc-1"})
	if err != nil || result.Status != CircuitMutationApplied || result.State.Phase != CircuitPhaseSuspect {
		t.Fatalf("failure1=%#v err=%v", result, err)
	}
	// 同证据重放不累计：仍 SUSPECT。
	advance := w7dSuspectRaw(t, scope, 3)
	advance.ConfirmationFailuresRequired = &required
	count := 1
	advance.ConfirmationFailureCount = &count
	advance.FailureEvidenceKeys = []string{strings.Repeat("a", 64)}
	advance.UpdatedAtMS = 5_500
	if _, err := store.Restore(context.Background(), advance, 5_500); err != nil {
		t.Fatal(err)
	}
	second := w12cIdentity(scope, 3, "7", "tx-esc-2", 6_000)
	if result, err := store.AcquireConfirmationLease(context.Background(), second, CircuitLeaseSpec{LeaseID: "esc-2", LeaseUntilMS: 9_000}); err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("acquire2=%#v err=%v", result, err)
	}
	completeSecond := second
	completeSecond.TransitionID = "tx-esc-2-done"
	result, err = store.CompleteConfirmation(context.Background(), completeSecond, "esc-2", CircuitCompletion{Outcome: CircuitVerdictTransportFailure, FailureEvidenceKey: strings.Repeat("a", 64)})
	if err != nil || result.State.Phase != CircuitPhaseSuspect {
		t.Fatalf("failure2 应因重复证据不累计=%#v err=%v", result, err)
	}
	// 独立证据累计到 required → OPEN。
	third := w12cIdentity(scope, 4, "7", "tx-esc-3", 12_000)
	advance2 := w7dSuspectRaw(t, scope, 4)
	advance2.ConfirmationFailuresRequired = &required
	advance2.ConfirmationFailureCount = &count
	advance2.FailureEvidenceKeys = []string{strings.Repeat("a", 64)}
	advance2.UpdatedAtMS = 11_500
	if _, err := store.Restore(context.Background(), advance2, 11_500); err != nil {
		t.Fatal(err)
	}
	if result, err := store.AcquireConfirmationLease(context.Background(), third, CircuitLeaseSpec{LeaseID: "esc-3", LeaseUntilMS: 15_000}); err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("acquire3=%#v err=%v", result, err)
	}
	completeThird := third
	completeThird.TransitionID = "tx-esc-3-done"
	result, err = store.CompleteConfirmation(context.Background(), completeThird, "esc-3", CircuitCompletion{Outcome: CircuitVerdictTransportFailure, FailureEvidenceKey: strings.Repeat("b", 64)})
	if err != nil || result.State.Phase != CircuitPhaseOpen {
		t.Fatalf("escalate to open=%#v err=%v", result, err)
	}
}

func TestW12CMemoryStoreCompleteConfirmationUnknownBackoff(t *testing.T) {
	store := w7dMemoryStore(t, 4)
	scope := w9hScope("acc-w12c-unknown")
	if _, err := store.Restore(context.Background(), w7dSuspectRaw(t, scope, 2), 1_000); err != nil {
		t.Fatal(err)
	}
	identity := w12cIdentity(scope, 2, "7", "tx-unknown-acq", 2_000)
	if result, err := store.AcquireConfirmationLease(context.Background(), identity, CircuitLeaseSpec{LeaseID: "unk-1", LeaseUntilMS: 5_000}); err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("acquire=%#v err=%v", result, err)
	}
	complete := identity
	complete.TransitionID = "tx-unknown-done"
	result, err := store.CompleteConfirmation(context.Background(), complete, "unk-1", CircuitCompletion{Outcome: CircuitVerdictUnknown})
	if err != nil || result.Status != CircuitMutationApplied || result.State.BackoffAttempt != 1 {
		t.Fatalf("unknown backoff=%#v err=%v", result, err)
	}
	if result.State.RetryAtMS == nil || *result.State.RetryAtMS <= 2_000 {
		t.Fatalf("unknown 后应安排退避重试: %#v", result.State)
	}
}

func TestW12CMemoryStoreReplaceDispatchRevisionArms(t *testing.T) {
	// 空 revision / transition 拒绝。
	guardStore := w7dMemoryStore(t, 4)
	scope := w9hScope("acc-w12c-replace")
	if _, err := guardStore.ReplaceDispatchRevision(context.Background(), scope, "  ", "tx", 1_000); err == nil {
		t.Fatal("空 revision 必须拒绝")
	}
	if _, err := guardStore.ReplaceDispatchRevision(context.Background(), scope, "7", "", 1_000); err == nil {
		t.Fatal("空 transitionId 必须拒绝")
	}
	// 新 scope：两个活跃条目占满容量 → reserveCapacity 拒绝。
	fullStore := w7dMemoryStore(t, 2)
	if _, err := fullStore.Restore(context.Background(), openState(t, w9hScope("acc-w12c-holder"), 1, "7", 1_000), 1_000); err != nil {
		t.Fatal(err)
	}
	if _, err := fullStore.Restore(context.Background(), openState(t, w9hScope("acc-w12c-holder2"), 1, "7", 1_000), 1_000); err != nil {
		t.Fatal(err)
	}
	exhausted, err := fullStore.ReplaceDispatchRevision(context.Background(), w9hScope("acc-w12c-new"), "9", "tx-new", 2_000)
	if err != nil || exhausted.Status != CircuitMutationCapacityExhausted {
		t.Fatalf("capacity=%#v err=%v", exhausted, err)
	}
	// 落地新 revision 条目。
	store := w7dMemoryStore(t, 4)
	applied, err := store.ReplaceDispatchRevision(context.Background(), scope, "9", "tx-apply", 2_000)
	if err != nil || applied.Status != CircuitMutationApplied || applied.State.DispatchRevision != "9" {
		t.Fatalf("apply=%#v err=%v", applied, err)
	}
	// 同 revision 幂等；旧 revision 拒绝。
	same, err := store.ReplaceDispatchRevision(context.Background(), scope, "9", "tx-same", 2_100)
	if err != nil || same.Status != CircuitMutationIdempotent {
		t.Fatalf("same=%#v err=%v", same, err)
	}
	old, err := store.ReplaceDispatchRevision(context.Background(), scope, "3", "tx-old", 2_200)
	if err != nil || old.Status != CircuitMutationStaleDispatchRevision {
		t.Fatalf("old=%#v err=%v", old, err)
	}
}

func TestW12CMemoryStoreReplaceAccountDispatchRevisionGuards(t *testing.T) {
	store := w7dMemoryStore(t, 4)
	if _, err := store.ReplaceAccountDispatchRevision(context.Background(), "", "7", "tx", 1_000); err == nil {
		t.Fatal("空账户键必须拒绝")
	}
	if _, err := store.ReplaceAccountDispatchRevision(context.Background(), "acc", "", "tx", 1_000); err == nil {
		t.Fatal("空 revision 必须拒绝")
	}
	if _, err := store.ReplaceAccountDispatchRevision(context.Background(), "acc", "7", " ", 1_000); err == nil {
		t.Fatal("空 transition 必须拒绝")
	}
	// 授权子账户键也参与匹配。
	scope := accountScope("acc-w12c-acct:authorized:sys:group:auth")
	raw := w7dSuspectRaw(t, scope, 1)
	if _, err := store.Restore(context.Background(), raw, 1_000); err != nil {
		t.Fatal(err)
	}
	changed, err := store.ReplaceAccountDispatchRevision(context.Background(), "acc-w12c-acct", "9", "tx-bulk", 2_000)
	if err != nil || changed != 1 {
		t.Fatalf("authorized 匹配替换 changed=%d err=%v", changed, err)
	}
	// 同 revision 跳过。
	changed, err = store.ReplaceAccountDispatchRevision(context.Background(), "acc-w12c-acct", "9", "tx-bulk-2", 2_100)
	if err != nil || changed != 0 {
		t.Fatalf("重复替换应跳过 changed=%d err=%v", changed, err)
	}
}

func TestW12CMemoryStoreRestoreGuardsAndCapacity(t *testing.T) {
	store := w7dMemoryStore(t, 1)
	scope := w9hScope("acc-w12c-restore")
	raw := w7dSuspectRaw(t, scope, 2)
	badRequired := -1
	bad := raw
	bad.ConfirmationFailuresRequired = &badRequired
	if _, err := store.Restore(context.Background(), bad, 1_000); err == nil {
		t.Fatal("非法 required 必须拒绝")
	}
	mismatch := raw
	mismatch.ScopeKey = "w12c-forged-scope-key"
	if _, err := store.Restore(context.Background(), mismatch, 1_000); err == nil {
		t.Fatal("scopeKey 不一致必须拒绝")
	}
	if _, err := store.Restore(context.Background(), raw, 1_000); err != nil {
		t.Fatal(err)
	}
	// 旧 dispatch revision 拒绝；同代旧 UpdatedAt 幂等；容量满拒绝新 scope。
	older := raw
	older.DispatchRevision = "3"
	older.UpdatedAtMS = 2_000
	if result, err := store.Restore(context.Background(), older, 2_000); err != nil || result.Status != CircuitMutationStaleDispatchRevision {
		t.Fatalf("older=%#v err=%v", result, err)
	}
	stale := raw
	stale.UpdatedAtMS = 500
	if result, err := store.Restore(context.Background(), stale, 2_100); err != nil || result.Status != CircuitMutationIdempotent {
		t.Fatalf("stale=%#v err=%v", result, err)
	}
	if result, err := store.Restore(context.Background(), w7dSuspectRaw(t, w9hScope("acc-w12c-other"), 1), 2_200); err != nil || result.Status != CircuitMutationCapacityExhausted {
		t.Fatalf("capacity=%#v err=%v", result, err)
	}
	if got := store.Size(2_200); got != 1 {
		t.Fatalf("size=%d", got)
	}
}

func TestW12CMemoryStoreListDueAndCleanup(t *testing.T) {
	store := w7dMemoryStore(t, 4)
	if _, err := store.ListDue(context.Background(), 1_000, 0); err == nil {
		t.Fatal("limit<1 必须拒绝")
	}
	due := w7dSuspectRaw(t, w9hScope("acc-w12c-due"), 2)
	due.RetryAtMS = int64Ptr(2_000)
	if _, err := store.Restore(context.Background(), due, 1_000); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListDue(context.Background(), 3_000, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("due=%#v err=%v", items, err)
	}
}

func TestW12CMemoryStoreReserveCapacityEvictsOldestClosed(t *testing.T) {
	store := w7dMemoryStore(t, 2)
	scopeA := w9hScope("acc-w12c-cap-a")
	if _, err := store.Restore(context.Background(), w7dSuspectRaw(t, scopeA, 1), 1_000); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Restore(context.Background(), closedAccountCircuitState(w9hScope("acc-w12c-cap-b"), "7", 1, "tx-b", 500), 1_000); err != nil {
		t.Fatal(err)
	}
	// 满容量时 CLOSED 最旧条目可被驱逐。
	if result, err := store.Restore(context.Background(), w7dSuspectRaw(t, w9hScope("acc-w12c-cap-c"), 1), 2_000); err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("evict closed=%#v err=%v", result, err)
	}
	// 全部活跃 → saturated。
	if result, err := store.Restore(context.Background(), w7dSuspectRaw(t, w9hScope("acc-w12c-cap-d"), 1), 2_100); err != nil || result.Status != CircuitMutationCapacityExhausted {
		t.Fatalf("saturated=%#v err=%v", result, err)
	}
}

func TestW12CMemoryStoreClosedRetentionAndHalfOpenLeaseExpiry(t *testing.T) {
	store := w7dMemoryStore(t, 4)
	scope := w9hScope("acc-w12c-retention")
	if _, err := store.Restore(context.Background(), closedAccountCircuitState(scope, "7", 2, "tx-close", 1_000), 1_000); err != nil {
		t.Fatal(err)
	}
	if got := store.Size(1_000); got != 1 {
		t.Fatalf("保留期内 CLOSED 不应清理: %d", got)
	}
	if got := store.Size(1_000+store.closedRetentionMS+1); got != 0 {
		t.Fatalf("超过保留期应清理: %d", got)
	}

	// HALF_OPEN 租约过期回退原相位（origin 为空默认 OPEN）。
	canaryScope := w9hScope("acc-w12c-halffallback")
	open := openState(t, canaryScope, 2, "7", 1_000)
	open.RetryAtMS = int64Ptr(1_500)
	if _, err := store.Restore(context.Background(), open, 1_000); err != nil {
		t.Fatal(err)
	}
	identity := w12cIdentity(canaryScope, 2, "7", "tx-half", 2_000)
	if result, err := store.AcquireCanaryLease(context.Background(), identity, CircuitLeaseSpec{LeaseID: "half-exp", LeaseUntilMS: 3_000}); err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("half acquire=%#v err=%v", result, err)
	}
	state, err := store.Get(context.Background(), canaryScope, 9_000)
	if err != nil || state.Phase != CircuitPhaseOpen || state.Lease != nil {
		t.Fatalf("过期 HALF_OPEN 应回退 OPEN: %#v err=%v", state, err)
	}
}

func TestW12CMemoryStoreHierarchyCloseUnshadowsChildren(t *testing.T) {
	store := w7dMemoryStore(t, 8)
	parentScope := w9hScope("acc-w12c-parent")
	childScope := w9hScope("acc-w12c-child")
	childKey, err := AccountCircuitScopeKey(childScope)
	if err != nil {
		t.Fatal(err)
	}
	parent := w7dSuspectRaw(t, parentScope, 2)
	parent.IncidentID = "inc-parent"
	parent.ChildScopeKeys = []string{childKey}
	parent.ChildIncidentIDs = []string{"inc-child"}
	parent.RequiredRecoveryScopeKeys = []string{childKey}
	if _, err := store.Restore(context.Background(), parent, 1_000); err != nil {
		t.Fatal(err)
	}
	child := w7dSuspectRaw(t, childScope, 2)
	child.IncidentID = "inc-child"
	child.ShadowedByIncidentID = "inc-parent"
	if _, err := store.Restore(context.Background(), child, 1_000); err != nil {
		t.Fatal(err)
	}
	// SUSPECT → framing closed → close：父子投影与 unshadow。
	identity := w12cIdentity(parentScope, 2, "7", "tx-close-acq", 2_000)
	if result, err := store.AcquireConfirmationLease(context.Background(), identity, CircuitLeaseSpec{LeaseID: "close-1", LeaseUntilMS: 5_000}); err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("acquire=%#v err=%v", result, err)
	}
	complete := identity
	complete.TransitionID = "tx-close-done"
	result, err := store.CompleteConfirmation(context.Background(), complete, "close-1", CircuitCompletion{Outcome: CircuitVerdictFramingComplete, FramingCompleteDisposition: "closed"})
	if err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("close=%#v err=%v", result, err)
	}
	if len(result.RelatedStates) == 0 {
		t.Fatalf("close 应投影子作用域: %#v", result)
	}
	after, err := store.Get(context.Background(), childScope, 6_000)
	if err != nil || after.ShadowedByIncidentID != "" {
		t.Fatalf("子作用域应解除 shadow: %#v err=%v", after, err)
	}
}

func TestW12CMemoryStoreProjectParentShadowRelationships(t *testing.T) {
	store := w7dMemoryStore(t, 8)
	parentScope := w9hScope("acc-w12c-proj-parent")
	childScope := w9hScope("acc-w12c-proj-child")
	childKey, err := AccountCircuitScopeKey(childScope)
	if err != nil {
		t.Fatal(err)
	}
	parent := openState(t, parentScope, 2, "7", 2_000)
	parent.IncidentID = "inc-proj"
	parent.ChildScopeKeys = []string{childKey}
	parent.ChildIncidentIDs = []string{"inc-proj-child"}
	if _, err := store.Restore(context.Background(), parent, 2_000); err != nil {
		t.Fatal(err)
	}
	child := w7dSuspectRaw(t, childScope, 2)
	child.IncidentID = "inc-proj-child"
	child.UpdatedAtMS = 2_000
	if _, err := store.Restore(context.Background(), child, 1_500); err != nil {
		t.Fatal(err)
	}
	// 父 OPEN 投影：子未 shadow → shadow 关系回放。
	if result, err := store.Restore(context.Background(), parent, 2_500); err != nil || len(result.RelatedStates) != 1 {
		t.Fatalf("shadow 投影=%#v err=%v", result, err)
	}
	after, err := store.Get(context.Background(), childScope, 3_000)
	if err != nil || after.ShadowedByIncidentID != "inc-proj" {
		t.Fatalf("子应被 shadow: %#v err=%v", after, err)
	}
}

func TestW12CRecoveryEvidenceScopeKeyMatrix(t *testing.T) {
	store := w7dMemoryStore(t, 4)
	account := w7dSuspectRaw(t, w9hScope("acc-w12c-evi"), 2)
	account.RequiredRecoveryScopeKeys = []string{"scope-1", "scope-2"}
	account.RecoveryEvidenceScopeKeys = []string{"scope-1"}
	// 命中 required 且未合并 → merged。
	if got := store.nextRecoveryEvidenceScopeKeys(account, "scope-2"); len(got) != 2 {
		t.Fatalf("merged=%v", got)
	}
	// 已合并 → 原样。
	if got := store.nextRecoveryEvidenceScopeKeys(account, "scope-1"); len(got) != 1 {
		t.Fatalf("dedup=%v", got)
	}
	// 不在 required / 空 → 原样。
	if got := store.nextRecoveryEvidenceScopeKeys(account, "scope-x"); len(got) != 1 {
		t.Fatalf("unknown=%v", got)
	}
	if got := store.nextRecoveryEvidenceScopeKeys(account, "  "); len(got) != 1 {
		t.Fatalf("blank=%v", got)
	}
	// 无 required → 原样。
	noRequired := w7dSuspectRaw(t, w9hScope("acc-w12c-evi2"), 2)
	noRequired.RecoveryEvidenceScopeKeys = []string{"keep"}
	if got := store.nextRecoveryEvidenceScopeKeys(noRequired, "scope-1"); len(got) != 1 {
		t.Fatalf("no required=%v", got)
	}
	// 非 account scope → 原样。
	protocol := w7dSuspectRaw(t, protocolModelScope("acc-w12c-evi3", "openai", "text", "bucket"), 2)
	protocol.RecoveryEvidenceScopeKeys = []string{"proto"}
	if got := store.nextRecoveryEvidenceScopeKeys(protocol, "anything"); len(got) != 1 || got[0] != "proto" {
		t.Fatalf("protocol scope=%v", got)
	}
}

func TestW12CMemoryStoreRememberReplayTrimsToLimit(t *testing.T) {
	store := w7dMemoryStore(t, 4)
	entry := &memoryCircuitEntry{state: w7dSuspectRaw(t, w9hScope("acc-w12c-replay"), 1), replayIDs: map[string]struct{}{}}
	for index := 0; index < store.replayLimitPerScope+5; index++ {
		store.rememberReplay(entry, "tx-"+strings.Repeat("x", 1)+string(rune('a'+index%26))+string(rune(index)))
	}
	if len(entry.replayOrder) != store.replayLimitPerScope {
		t.Fatalf("重放记忆应裁剪到上限: %d", len(entry.replayOrder))
	}
}

func TestW12CMemoryStoreNormalizeConfirmationSuspiciousRetryDefaults(t *testing.T) {
	store := w7dMemoryStore(t, 4)
	scope := w9hScope("acc-w12c-norm")
	raw := w7dSuspectRaw(t, scope, 2)
	raw.RetryAtMS = nil
	raw.ConfirmationFailuresRequired = nil
	raw.ConfirmationFailureCount = nil
	raw.UpdatedAtMS = 4_000
	result, err := store.Restore(context.Background(), raw, 4_000)
	if err != nil || result.Status != CircuitMutationApplied {
		t.Fatalf("restore=%#v err=%v", result, err)
	}
	state, err := store.Get(context.Background(), scope, 4_100)
	if err != nil {
		t.Fatal(err)
	}
	if state.RetryAtMS == nil || *state.RetryAtMS != 4_000 {
		t.Fatalf("SUSPECT 缺省 RetryAt 应取 UpdatedAt: %#v", state)
	}
}

func TestW12CCircuitStorePureHelpers(t *testing.T) {
	// AccountCircuitBackoffDelayMS：低档直接返回 base。
	if got := AccountCircuitBackoffDelayMS(2, "", nil); got != CircuitBackoffMS[1] {
		t.Fatalf("低档退避=%d", got)
	}
	// jitterSeed → sha1 确定性抖动。
	first := AccountCircuitBackoffDelayMS(6, "seed-w12c", nil)
	second := AccountCircuitBackoffDelayMS(6, "seed-w12c", nil)
	if first != second {
		t.Fatalf("seed 抖动应确定: %d vs %d", first, second)
	}
	// random 注入 → base+random。
	if got := AccountCircuitBackoffDelayMS(6, "", func(base int64) int64 { return -5 }); got != CircuitBackoffMS[5]-5 {
		t.Fatalf("random 抖动=%d", got)
	}
	// isFailureEvidenceKey。
	if !isFailureEvidenceKey(strings.Repeat("0f", 32)) {
		t.Fatal("64 位十六进制应通过")
	}
	if isFailureEvidenceKey(strings.Repeat("g", 64)) || isFailureEvidenceKey("abc") {
		t.Fatal("非法 evidence key 必须拒绝")
	}
	// retryAtMS 缺省 → 最大值。
	if got := retryAtMS(CircuitState{}); got != int64(^uint64(0)>>1) {
		t.Fatalf("retryAtMS 缺省=%d", got)
	}
	// runtimeKeyMatchesDispatchRevisionTarget：authorized 前缀匹配与豁免。
	if !runtimeKeyMatchesDispatchRevisionTarget("acc:authorized:sys:g:a", "acc") {
		t.Fatal("authorized 前缀应匹配")
	}
	if runtimeKeyMatchesDispatchRevisionTarget("acc-other", "acc") {
		t.Fatal("无关键不得匹配")
	}
	// jitter NaN/Inf 随机源归零处理：归零后偏移落在 -window。
	if got := PassiveScheduleOffsetMS(60_000, func() float64 { return math.NaN() }); got != -30_000 {
		t.Fatalf("NaN 偏移应按 0 处理: %d", got)
	}
	if got := PassiveScheduleOffsetMS(60_000, func() float64 { return math.Inf(1) }); got != -30_000 {
		t.Fatalf("+Inf 偏移应按 0 处理: %d", got)
	}
	// newTimer 下限钳制与 stopTimer 空值。
	timer := newTimer(0)
	if timer == nil {
		t.Fatal("newTimer(0) 应返回 1ms timer")
	}
	stopTimer(timer)
	stopTimer(nil)
	// probeoutcome：invalid_probe_output → 语义失败标记。
	status := 200
	result := TransportProbeOutcomeFromResult(
		ProbeResultSnapshot{ErrorCode: "invalid_probe_output"},
		&UpstreamAttemptSnapshot{Status: &status, IsReal: true, IsCompletedReal: true},
		false, false, nil)
	if result.Kind != ProbeOutcomeFramingComplete || result.SemanticSuccess == nil || *result.SemanticSuccess {
		t.Fatalf("invalid_probe_output 应标记语义失败: %#v", result)
	}
	// 真实上游尝试但无状态码 → connection 分类；无上游 → task_failure。
	connection := TransportProbeOutcomeFromResult(ProbeResultSnapshot{}, &UpstreamAttemptSnapshot{IsReal: true}, false, false, nil)
	if connection.Kind != ProbeOutcomeTransportIncomplete || connection.FailureKind != ProbeFailureConnection {
		t.Fatalf("real attempt 应分类 connection: %#v", connection)
	}
	exhausted := true
	taskFailure := TransportProbeOutcomeFromResult(ProbeResultSnapshot{}, nil, false, false, &exhausted)
	if taskFailure.Kind != ProbeOutcomeUnknown || taskFailure.FailureKind != ProbeFailureTaskFailure {
		t.Fatalf("无上游应分类 task_failure: %#v", taskFailure)
	}
}

func TestW12CBalanceDetectRetryAndSummaryArms(t *testing.T) {
	// repo 注错：retry 分支的 CommitDetectionDue 错误上抛。
	errRepo := &w12cErrBalanceRepo{inner: newFakeBalanceRepo(), commitErr: errors.New("w12c: commit boom")}
	candidate := detectionCandidate("acc-w12c-retry")
	deps := testBalanceDeps(errRepo, &fakeBalanceDetector{
		result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotFresh}},
	}, balanceNowMS)
	deps.Detector = &fakeBalanceDetector{err: errors.New("w12c: detector boom")}
	if _, err := AutoDetectAccountBalanceCandidate(context.Background(), candidate, deps); err == nil {
		t.Fatal("commit 错误必须暴露")
	}

	// enable 错误：matched 路径错误上抛（RunBalanceAutoDetectionRecovery 331 分支）。
	enableErrRepo := &w12cErrBalanceRepo{inner: newFakeBalanceRepo(), enableErr: errors.New("w12c: enable boom")}
	enableErrRepo.inner.candidates = []BalanceDetectionCandidate{detectionCandidate("acc-w12c-err")}
	if _, err := RunBalanceAutoDetectionRecovery(context.Background(), testBalanceDeps(enableErrRepo, &fakeBalanceDetector{
		result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotFresh}},
	}, balanceNowMS)); err == nil {
		t.Fatal("enable 错误必须暴露")
	}

	// unsupported / retry / lease busy 汇总分支。
	unsupportedRepo := newFakeBalanceRepo()
	unsupportedRepo.candidates = []BalanceDetectionCandidate{detectionCandidate("acc-w12c-u1")}
	unsupportedSummary, err := RunBalanceAutoDetectionRecovery(context.Background(), testBalanceDeps(unsupportedRepo, &fakeBalanceDetector{
		result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotUnsupported}},
	}, balanceNowMS))
	if err != nil || unsupportedSummary.UnsupportedCount != 1 || unsupportedSummary.Outcome != "success" {
		t.Fatalf("unsupported summary=%+v err=%v", unsupportedSummary, err)
	}

	// detector 错误 → retry 意图提交成功 → retry 计数；提交被围栏拒绝 → stale 计数。
	retryRepo := newFakeBalanceRepo()
	retryRepo.candidates = []BalanceDetectionCandidate{detectionCandidate("acc-w12c-r1"), detectionCandidate("acc-w12c-r2")}
	retryRepo.commitResults["acc-w12c-r2"] = false
	retryDeps := testBalanceDeps(retryRepo, &fakeBalanceDetector{err: errors.New("w12c: detector boom")}, balanceNowMS)
	retrySummary, err := RunBalanceAutoDetectionRecovery(context.Background(), retryDeps)
	if err != nil || retrySummary.RetryCount != 1 || retrySummary.StaleCount != 1 {
		t.Fatalf("retry summary=%+v err=%v", retrySummary, err)
	}

	busyLease := &fakeBalanceLease{acquired: false}
	busyRepo := newFakeBalanceRepo()
	busyRepo.candidates = []BalanceDetectionCandidate{detectionCandidate("acc-w12c-b1")}
	busyDeps := testBalanceDeps(busyRepo, &fakeBalanceDetector{
		result: BalanceBuiltinQueryResult{Snapshot: BalanceSnapshot{Status: BalanceSnapshotFresh}},
	}, balanceNowMS)
	busyDeps.Lease = busyLease
	busySummary, err := RunBalanceAutoDetectionRecovery(context.Background(), busyDeps)
	if err != nil || busySummary.DeferredCount != 1 {
		t.Fatalf("lease busy summary=%+v err=%v", busySummary, err)
	}
}

// w12cErrBalanceRepo 在 fakeBalanceRepo 上注入方法级错误。
type w12cErrBalanceRepo struct {
	inner      *fakeBalanceRepo
	commitErr  error
	enableErr  error
	snapshotErr error
}

func (r *w12cErrBalanceRepo) ListDueCandidates(ctx context.Context, limit int) ([]BalanceDetectionCandidate, error) {
	return r.inner.ListDueCandidates(ctx, limit)
}

func (r *w12cErrBalanceRepo) CommitDetectionDue(ctx context.Context, input BalanceCommitDueInput) (bool, error) {
	if r.commitErr != nil {
		return false, r.commitErr
	}
	return r.inner.CommitDetectionDue(ctx, input)
}

func (r *w12cErrBalanceRepo) EnableDetectedQuery(ctx context.Context, input BalanceEnableInput) (bool, error) {
	if r.enableErr != nil {
		return false, r.enableErr
	}
	return r.inner.EnableDetectedQuery(ctx, input)
}

func (r *w12cErrBalanceRepo) ReplaceSnapshotIfCurrent(ctx context.Context, input BalanceSnapshotInput) (bool, error) {
	if r.snapshotErr != nil {
		return false, r.snapshotErr
	}
	return r.inner.ReplaceSnapshotIfCurrent(ctx, input)
}
