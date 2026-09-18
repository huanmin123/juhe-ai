package gatewaycircuit

// w14e 覆盖率补强：定位 store_memory / store_redis / types 未覆盖的错误臂与
// 状态归一化分支。损坏内部状态使用包内直改 entries 的 Mock 方式触发。

import (
	"context"
	"strings"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
)

func w14eMemoryStore(t *testing.T, mutate func(*MemoryStoreOptions)) *MemoryStore {
	t.Helper()
	options := MemoryStoreOptions{Capacity: 10, Now: func() int64 { return 10_000 }, Random: func() float64 { return 0.5 }}
	if mutate != nil {
		mutate(&options)
	}
	store, err := NewMemoryStore(options)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	return store
}

// w14eRestoreOpen 在目标 scope 上直接恢复一个 open 相位条目。
func w14eRestoreOpen(t *testing.T, store *MemoryStore, scope Scope, revision, transitionID string, now *int64) MutationResult {
	t.Helper()
	state := ClosedState(scope, revision, 1, transitionID, *now)
	state.Phase = PhaseOpen
	openedAt := *now
	state.OpenedAtMs = &openedAt
	retryAt := *now + 1_000
	state.RetryAtMs = &retryAt
	result, err := store.Restore(context.Background(), state, now)
	if err != nil || result.Status != MutationApplied {
		t.Fatalf("restore open = (%s, %v)", result.Status, err)
	}
	return result
}

// w14eCorruptCFR 直接损坏指定 scope 条目的 confirmationFailuresRequired，
// 使 freshEntryLocked 的归一化返回错误（Mock 内部状态）。
func w14eCorruptCFR(t *testing.T, store *MemoryStore, scope Scope) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.entries[MustScopeKey(scope)]
	if !ok {
		t.Fatal("entry missing for corruption")
	}
	zero := int64(0)
	entry.state.ConfirmationFailuresRequired = &zero
}

// w14eSuspect 建立一个 suspect 相位条目。
func w14eSuspect(t *testing.T, store *MemoryStore, scope Scope, now *int64) MutationResult {
	t.Helper()
	result, err := store.Suspect(context.Background(), SuspectInput{
		Scope: scope, DispatchRevision: "5", TransitionID: "t1", Reason: "transport:connect failed", NowMs: now,
	})
	if err != nil || result.Status != MutationApplied {
		t.Fatalf("suspect = (%s, %v)", result.Status, err)
	}
	return result
}

func TestW14EMemoryStoreNormalizeErrorArms(t *testing.T) {
	ctx := context.Background()
	now := int64(10_000)
	scope := accountScope("w14e-acc")

	t.Run("suspect", func(t *testing.T) {
		store := w14eMemoryStore(t, nil)
		w14eSuspect(t, store, scope, &now)
		w14eCorruptCFR(t, store, scope)
		if _, err := store.Suspect(ctx, SuspectInput{Scope: scope, DispatchRevision: "5", TransitionID: "t2", Reason: "r", NowMs: &now}); err == nil {
			t.Fatal("corrupted CFR must fail suspect")
		}
	})

	t.Run("acquire confirmation lease", func(t *testing.T) {
		store := w14eMemoryStore(t, nil)
		w14eSuspect(t, store, scope, &now)
		w14eCorruptCFR(t, store, scope)
		if _, err := store.AcquireConfirmationLease(ctx, AcquireConfirmationLeaseInput{Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "t", LeaseID: "l", LeaseUntilMs: now + 1, NowMs: &now}); err == nil {
			t.Fatal("corrupted CFR must fail acquire confirmation lease")
		}
	})

	t.Run("close suspect observer and key rotation", func(t *testing.T) {
		store := w14eMemoryStore(t, nil)
		w14eSuspect(t, store, scope, &now)
		w14eCorruptCFR(t, store, scope)
		if _, err := store.CloseSuspectFromObserver(ctx, CloseSuspectFromObserverInput{Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "t", ExpectedFailureEvidenceKey: "e", ObserverEvidenceKey: "o", NowMs: &now}); err == nil {
			t.Fatal("corrupted CFR must fail close suspect from observer")
		}
		if _, err := store.CloseSuspectFromKeyRotation(ctx, CloseSuspectFromKeyRotationInput{Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "t", ExpectedFailureEvidenceKey: "e", NowMs: &now}); err == nil {
			t.Fatal("corrupted CFR must fail close suspect from key rotation")
		}
	})

	t.Run("complete confirmation and canary", func(t *testing.T) {
		store := w14eMemoryStore(t, nil)
		w14eSuspect(t, store, scope, &now)
		w14eCorruptCFR(t, store, scope)
		if _, err := store.CompleteConfirmation(ctx, CompleteConfirmationInput{Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "t", LeaseID: "l", Outcome: OutcomeUnknown, NowMs: &now}); err == nil {
			t.Fatal("corrupted CFR must fail complete confirmation")
		}
		if _, err := store.AcquireCanaryLease(ctx, AcquireCanaryLeaseInput{Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "t", LeaseID: "l", LeaseUntilMs: now + 1, NowMs: &now}); err == nil {
			t.Fatal("corrupted CFR must fail acquire canary lease")
		}
		if _, err := store.CompleteCanary(ctx, CompleteCanaryInput{Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "t", LeaseID: "l", Outcome: OutcomeFramingComplete, NowMs: &now}); err == nil {
			t.Fatal("corrupted CFR must fail complete canary")
		}
	})

	t.Run("escalation replace restore and get", func(t *testing.T) {
		store := w14eMemoryStore(t, nil)
		w14eSuspect(t, store, scope, &now)
		w14eCorruptCFR(t, store, scope)
		if _, err := store.ReplaceDispatchRevision(ctx, ReplaceDispatchRevisionInput{Scope: scope, DispatchRevision: "6", TransitionID: "t", NowMs: &now}); err == nil {
			t.Fatal("corrupted CFR must fail replace dispatch revision")
		}
		if _, err := store.Get(ctx, scope, &now); err == nil {
			t.Fatal("corrupted CFR must fail get")
		}
		cleanScope := accountScope("w14e-acc-clean")
		badCFR := int64(0)
		if _, err := store.Restore(ctx, ClosedState(cleanScope, "5", 1, "t", now), &now); err != nil {
			t.Fatalf("closed state restore must skip normalization: %v", err)
		}
		state := ClosedState(cleanScope, "5", 1, "t", now)
		state.Phase = PhaseSuspect
		state.ConfirmationFailuresRequired = &badCFR
		if _, err := store.Restore(ctx, state, &now); err == nil {
			t.Fatal("restore with invalid CFR must fail")
		}
	})
}

func TestW14EMemoryStoreConfirmationCountErrorArm(t *testing.T) {
	ctx := context.Background()
	now := int64(10_000)
	scope := accountScope("w14e-count")
	store := w14eMemoryStore(t, nil)

	badCount := int64(-5)
	state := ClosedState(scope, "5", 1, "t", now)
	state.Phase = PhaseSuspect
	state.ConfirmationFailureCount = &badCount
	if _, err := store.Restore(ctx, state, &now); err == nil {
		t.Fatal("restore with negative count must fail")
	}

	// 直接损坏后走 CompleteConfirmation 的计数错误臂。
	w14eSuspect(t, store, scope, &now)
	store.mu.Lock()
	store.entries[MustScopeKey(scope)].state.ConfirmationFailureCount = &badCount
	store.mu.Unlock()
	_, err := store.CompleteConfirmation(ctx, CompleteConfirmationInput{Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "t9", LeaseID: "nope", Outcome: OutcomeUnknown, NowMs: &now})
	if err == nil {
		t.Fatal("negative count must fail complete confirmation")
	}
}

func TestW14EMemoryStoreApplyAndReplayArms(t *testing.T) {
	ctx := context.Background()
	now := int64(10_000)
	scope := accountScope("w14e-apply")
	store := w14eMemoryStore(t, func(o *MemoryStoreOptions) { o.ReplayLimitPerScope = 2 })

	w14eSuspect(t, store, scope, &now)
	// 观察者关闭携带空 transitionID：匹配证据后进入 applyLocked 的空
	// transitionID 错误臂。
	expected := sha256Hex("suspect:t1")
	observer := sha256Hex("observer-close:w14e")
	if _, err := store.CloseSuspectFromObserver(ctx, CloseSuspectFromObserverInput{
		Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "",
		ExpectedFailureEvidenceKey: expected, ObserverEvidenceKey: observer, NowMs: &now,
	}); err == nil {
		t.Fatal("empty transition id must fail close")
	}
	// rememberReplayLocked 的重放窗口裁剪。
	for index := 0; index < 4; index++ {
		if _, err := store.ReplaceDispatchRevision(ctx, ReplaceDispatchRevisionInput{Scope: scope, DispatchRevision: "6", TransitionID: "close", NowMs: &now}); err != nil {
			t.Fatalf("replace: %v", err)
		}
		if _, err := store.Suspect(ctx, SuspectInput{Scope: scope, DispatchRevision: "7", TransitionID: "s-" + string(rune('a'+index)), Reason: "r", NowMs: &now}); err != nil {
			t.Fatalf("suspect: %v", err)
		}
	}
	store.mu.Lock()
	entry := store.entries[MustScopeKey(scope)]
	if len(entry.replayOrder) > 2 {
		t.Fatalf("replay window must be trimmed: %v", entry.replayOrder)
	}
	store.mu.Unlock()
}

func TestW14EMemoryStoreExpiredHalfOpenLease(t *testing.T) {
	ctx := context.Background()
	now := int64(10_000)
	clock := &now
	scope := accountScope("w14e-lease")
	store := w14eMemoryStore(t, func(o *MemoryStoreOptions) { o.Now = func() int64 { return *clock } })

	w14eRestoreOpen(t, store, scope, "5", "open-1", clock)
	*clock += 1_000
	// 打开的账户获取 half-open 租约后推进时钟使租约过期。
	canary, err := store.AcquireCanaryLease(ctx, AcquireCanaryLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "5",
		TransitionID: "canary-1", LeaseID: "lease-1", LeaseUntilMs: *clock + 100, NowMs: clock,
	})
	if err != nil || canary.Status != MutationApplied {
		t.Fatalf("canary acquire = (%s, %v)", canary.Status, err)
	}
	if canary.State.Phase != PhaseHalfOpen {
		t.Fatalf("phase = %s", canary.State.Phase)
	}
	*clock += 200
	// 过期租约在访问时被归一化回 open 相位（normalizeExpiredLeaseLocked 非确认分支）。
	state, err := store.Get(ctx, scope, clock)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if state.Phase != PhaseOpen || state.Lease != nil {
		t.Fatalf("expired half-open lease must restore origin: %+v", state)
	}
}

func TestW14EMemoryStoreCanaryLeaseArms(t *testing.T) {
	ctx := context.Background()
	now := int64(10_000)
	clock := &now
	scope := accountScope("w14e-canary")
	store := w14eMemoryStore(t, func(o *MemoryStoreOptions) { o.Now = func() int64 { return *clock } })

	w14eRestoreOpen(t, store, scope, "5", "open-1", clock)
	*clock += 1_000

	// 租约截止时间不晚于当前时间必须报错（memoryLease 错误臂）。
	if _, err := store.AcquireCanaryLease(ctx, AcquireCanaryLeaseInput{Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "c0", LeaseID: "l0", LeaseUntilMs: *clock, NowMs: clock}); err == nil {
		t.Fatal("lease until now must fail")
	}
	first, err := store.AcquireCanaryLease(ctx, AcquireCanaryLeaseInput{Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "c1", LeaseID: "l1", LeaseUntilMs: *clock + 500, NowMs: clock})
	if err != nil || first.Status != MutationApplied {
		t.Fatalf("first acquire = (%s, %v)", first.Status, err)
	}
	// 已持有租约时再次获取必须状态冲突。
	second, err := store.AcquireCanaryLease(ctx, AcquireCanaryLeaseInput{Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "c2", LeaseID: "l2", LeaseUntilMs: *clock + 500, NowMs: clock})
	if err != nil || second.Status != MutationStateMismatch {
		t.Fatalf("second acquire = (%s, %v)", second.Status, err)
	}
	// 租约不匹配的完成必须冲突。
	mismatch, err := store.CompleteCanary(ctx, CompleteCanaryInput{Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "c3", LeaseID: "other", Outcome: OutcomeFramingComplete, NowMs: clock})
	if err != nil || mismatch.Status != MutationLeaseMismatch {
		t.Fatalf("lease mismatch = (%s, %v)", mismatch.Status, err)
	}
	// unknown 结果恢复 open 相位（restoreCanaryOriginLocked）。
	unknown, err := store.CompleteCanary(ctx, CompleteCanaryInput{Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "c4", LeaseID: "l1", Outcome: OutcomeUnknown, NowMs: clock})
	if err != nil || unknown.Status != MutationApplied {
		t.Fatalf("unknown complete = (%s, %v)", unknown.Status, err)
	}
	if unknown.State.Phase != PhaseOpen {
		t.Fatalf("phase = %s", unknown.State.Phase)
	}
}

func TestW14EMemoryStoreGetSaturatedArms(t *testing.T) {
	ctx := context.Background()
	now := int64(10_000)
	clock := &now
	scopeA := accountScope("w14e-sat-a")
	store := w14eMemoryStore(t, func(o *MemoryStoreOptions) { o.Capacity = 1; o.Now = func() int64 { return *clock } })

	// 占满容量：open 状态条目无法被 closed 淘汰逻辑清理。
	w14eSuspect(t, store, scopeA, clock)
	exhausted, err := store.Suspect(ctx, SuspectInput{Scope: accountScope("w14e-sat-b"), DispatchRevision: "5", TransitionID: "t2", Reason: "r", NowMs: clock})
	if err != nil || exhausted.Status != MutationCapacityExhausted {
		t.Fatalf("capacity suspect = (%s, %v)", exhausted.Status, err)
	}
	// 容量饱和后 Get 缺失 scope 返回容量耗尽状态。
	saturated, err := store.Get(ctx, accountScope("w14e-sat-missing"), clock)
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	_ = exhausted
	if saturated.Phase != PhaseSuspect || saturated.FailureReason == nil || *saturated.FailureReason != "runtime_state_capacity_exhausted" {
		t.Fatalf("saturated get = %+v", saturated)
	}
	// 关闭唯一条目后，饱和标记仍在但清理可回收：返回 ClosedState。
	if _, err := store.ReplaceDispatchRevision(ctx, ReplaceDispatchRevisionInput{Scope: scopeA, DispatchRevision: "6", TransitionID: "close-1", NowMs: clock}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	closed, err := store.Get(ctx, accountScope("w14e-sat-missing-2"), clock)
	if err != nil {
		t.Fatalf("get after close: %v", err)
	}
	if closed.Phase != PhaseClosed || closed.Generation != 0 {
		t.Fatalf("closed get = %+v", closed)
	}
}

func TestW14EMemoryStoreEscalationArms(t *testing.T) {
	ctx := context.Background()
	now := int64(10_000)
	clock := &now
	store := w14eMemoryStore(t, func(o *MemoryStoreOptions) { o.Now = func() int64 { return *clock } })

	// 三个 open 相位的协议子作用域；distinctScopeThreshold 最小为 3。
	protocols := []Scope{
		{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "w14e-esc", ProtocolProfile: "profile-1", RequestLane: LaneText, ModelBucket: "gpt"},
		{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "w14e-esc", ProtocolProfile: "profile-2", RequestLane: LaneText, ModelBucket: "gpt"},
		{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "w14e-esc", ProtocolProfile: "profile-3", RequestLane: LaneText, ModelBucket: "gpt"},
	}
	for index, protocol := range protocols {
		w14eRestoreOpen(t, store, protocol, "5", "p-open-"+string(rune('1'+index)), clock)
	}
	w14eInput := func(protocol Scope, evidenceID, accountTransitionID string) ProtocolModelOpenEvidenceInput {
		return ProtocolModelOpenEvidenceInput{
			Scope: protocol, Generation: 1, DispatchRevision: "5", EvidenceID: evidenceID,
			AccountTransitionID: accountTransitionID, Reason: "protocol escalations",
			ConfirmedFailureCount: 1, DistinctScopeThreshold: 3, WindowMs: 60_000, MaxProtocolScopes: 8, NowMs: clock,
		}
	}

	// confirmedFailureCount / windowMs / maxProtocolScopes 校验在子条目检查之后。
	zeroCount := w14eInput(protocols[0], "e", "")
	zeroCount.ConfirmedFailureCount = 0
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, zeroCount); err == nil || !strings.Contains(err.Error(), "confirmedFailureCount") {
		t.Fatalf("zero confirmedFailureCount err = %v", err)
	}
	zeroWindow := w14eInput(protocols[0], "e", "")
	zeroWindow.WindowMs = 0
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, zeroWindow); err == nil {
		t.Fatal("zero windowMs must fail")
	}
	zeroScopes := w14eInput(protocols[0], "e", "")
	zeroScopes.MaxProtocolScopes = 0
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, zeroScopes); err == nil {
		t.Fatal("zero maxProtocolScopes must fail")
	}
	overThreshold := w14eInput(protocols[0], "e", "")
	overThreshold.MaxProtocolScopes = 3
	overThreshold.DistinctScopeThreshold = 4
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, overThreshold); err == nil || !strings.Contains(err.Error(), "distinctScopeThreshold") {
		t.Fatalf("threshold above maxProtocolScopes err = %v", err)
	}

	// 前两个 distinct scope 只记录；第三个触发升级路径但缺
	// accountTransitionID 必须报错（证据已先落账）。
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, w14eInput(protocols[0], "ev-a", "")); err != nil {
		t.Fatalf("first evidence: %v", err)
	}
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, w14eInput(protocols[1], "ev-b", "")); err != nil {
		t.Fatalf("second evidence: %v", err)
	}
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, w14eInput(protocols[2], "ev-c", "")); err == nil {
		t.Fatal("threshold reached without accountTransitionID must fail")
	}
	// 同 evidenceID 再次上报返回 idempotent。
	idempotent, err := store.RecordProtocolModelOpenEvidence(ctx, w14eInput(protocols[2], "ev-c", ""))
	if err != nil || idempotent.Status != EscalationIdempotent {
		t.Fatalf("idempotent = (%s, %v)", idempotent.Status, err)
	}
	// 新 evidence 在第三个 scope 完成真实升级。
	escalated, err := store.RecordProtocolModelOpenEvidence(ctx, w14eInput(protocols[2], "ev2", "acc-open-2"))
	if err != nil || escalated.Status != EscalationEscalated {
		t.Fatalf("escalation = (%s, %v)", escalated.Status, err)
	}
	if escalated.AccountState.Phase != PhaseOpen || escalated.AccountState.Generation != 1 {
		t.Fatalf("account state = %+v", escalated.AccountState)
	}
	if len(escalated.RelatedStates) == 0 {
		t.Fatalf("escalation must shadow children: %+v", escalated.RelatedStates)
	}
	// 已打开账户的重复升级返回 alreadyActive（attachAccountShadow 分支）。
	again, err := store.RecordProtocolModelOpenEvidence(ctx, w14eInput(protocols[0], "ev3", "acc-open-3"))
	if err != nil || again.Status != EscalationAlreadyActive {
		t.Fatalf("already active = (%s, %v)", again.Status, err)
	}
	// 升级后的账户 open 条目应当进入 due 列表。
	due, err := store.ListDue(ctx, *clock+10_000, 10)
	if err != nil || len(due) == 0 {
		t.Fatalf("due = %v, %v", due, err)
	}
	// ClearAccountEscalationEvidence 的输入与命中分支。
	if _, err := store.ClearAccountEscalationEvidence(ctx, ClearAccountEscalationEvidenceInput{EvidenceID: " ", AccountRuntimeKey: "a", DispatchRevision: "5", NowMs: clock}); err == nil {
		t.Fatal("empty evidenceId must fail")
	}
	if _, err := store.ClearAccountEscalationEvidence(ctx, ClearAccountEscalationEvidenceInput{EvidenceID: "e", AccountRuntimeKey: " ", DispatchRevision: "5", NowMs: clock}); err == nil {
		t.Fatal("empty runtime key must fail")
	}
	if _, err := store.ClearAccountEscalationEvidence(ctx, ClearAccountEscalationEvidenceInput{EvidenceID: "e", AccountRuntimeKey: "a", DispatchRevision: " ", NowMs: clock}); err == nil {
		t.Fatal("empty revision must fail")
	}
	cleared, err := store.ClearAccountEscalationEvidence(ctx, ClearAccountEscalationEvidenceInput{EvidenceID: "ev3", AccountRuntimeKey: "w14e-esc", DispatchRevision: "5", NowMs: clock})
	if err != nil || !cleared {
		t.Fatalf("clear = %v, %v", cleared, err)
	}
	if _, err := store.ClearAccountEscalationEvidence(ctx, ClearAccountEscalationEvidenceInput{EvidenceID: "ev3", AccountRuntimeKey: "w14e-esc", DispatchRevision: "5", NowMs: clock}); err != nil {
		t.Fatalf("second clear: %v", err)
	}
}

func TestW14EMemoryStoreReplaceAccountArms(t *testing.T) {
	ctx := context.Background()
	now := int64(10_000)
	clock := &now
	store := w14eMemoryStore(t, func(o *MemoryStoreOptions) { o.Now = func() int64 { return *clock } })

	if _, err := store.ListDue(ctx, *clock, 0); err == nil {
		t.Fatal("zero limit must fail")
	}
	// 两个 runtime key（基础 + authorized 前缀），验证匹配与跳过分支。
	w14eSuspect(t, store, accountScope("w14e-rk-a"), clock)
	w14eSuspect(t, store, accountScope("w14e-rk-a:authorized:sys:g:az"), clock)
	changed, err := store.ReplaceAccountDispatchRevision(ctx, ReplaceAccountDispatchRevisionInput{AccountRuntimeKey: "w14e-rk-a", DispatchRevision: "9", TransitionID: "close-all", NowMs: clock})
	if err != nil || changed != 2 {
		t.Fatalf("changed = %d, %v", changed, err)
	}
	again, err := store.ReplaceAccountDispatchRevision(ctx, ReplaceAccountDispatchRevisionInput{AccountRuntimeKey: "w14e-rk-a", DispatchRevision: "9", TransitionID: "close-all-2", NowMs: clock})
	if err != nil || again != 0 {
		t.Fatalf("same revision changed = %d, %v", again, err)
	}
	older, err := store.ReplaceAccountDispatchRevision(ctx, ReplaceAccountDispatchRevisionInput{AccountRuntimeKey: "w14e-rk-a", DispatchRevision: "3", TransitionID: "close-old", NowMs: clock})
	if err != nil || older != 0 {
		t.Fatalf("older revision changed = %d, %v", older, err)
	}
	// due 列表在 close 后不再包含已关闭条目。
	due, err := store.ListDue(ctx, *clock, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("due = %v, %v", due, err)
	}
}

func TestW14EMemoryStoreRestoreNormalizationArms(t *testing.T) {
	ctx := context.Background()
	now := int64(10_000)
	scope := accountScope("w14e-restore")
	store := w14eMemoryStore(t, nil)

	// suspect 相位缺 RetryAtMs 且带租约：归一化时以租约截止时间为重试点。
	state := ClosedState(scope, "5", 1, "t1", now)
	state.Phase = PhaseSuspect
	state.RetryAtMs = nil
	state.Lease = &Lease{Kind: LeaseKindConfirmation, LeaseID: "lease", LeaseUntilMs: now + 999}
	result, err := store.Restore(ctx, state, &now)
	if err != nil || result.Status != MutationApplied {
		t.Fatalf("restore = (%s, %v)", result.Status, err)
	}
	if result.State.RetryAtMs == nil || *result.State.RetryAtMs != now+999 {
		t.Fatalf("retryAt = %v", result.State.RetryAtMs)
	}
	// 状态缺 scope key：AssertStateScopeKey 报错。
	if _, err := store.Restore(ctx, State{Phase: PhaseSuspect}, &now); err == nil {
		t.Fatal("state without scope key must fail restore")
	}
	// 旧版本 restore 返回 stale。
	w14eSuspect(t, store, accountScope("w14e-stale"), &now)
	stale := ClosedState(accountScope("w14e-stale"), "3", 9, "old", now)
	stale.Phase = PhaseSuspect
	result, err = store.Restore(ctx, stale, &now)
	if err != nil || result.Status != MutationStaleDispatchRevision {
		t.Fatalf("stale restore = (%s, %v)", result.Status, err)
	}
}

// ---------------------------------------------------------------------------
// store_redis.go：输入校验与 Lua 响应解析。
// ---------------------------------------------------------------------------

func TestW14ERedisStoreValidationArms(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := NewRedisStore(RedisStoreOptions{RedisURL: "redis://" + server.Addr(), Name: "w14e", Capacity: 10, Now: func() int64 { return 1_000 }})
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	ctx := context.Background()
	scope := accountScope("w14e-redis")
	badCFR := int64(0)

	if _, err := store.Suspect(ctx, SuspectInput{Scope: scope, DispatchRevision: "5", TransitionID: "t1", ConfirmationFailuresRequired: &badCFR}); err == nil {
		t.Fatal("invalid confirmationFailuresRequired must fail")
	}
	if _, err := store.Suspect(ctx, SuspectInput{Scope: scope, TransitionID: "t1"}); err == nil {
		t.Fatal("suspect without dispatchRevision must fail")
	}
	if _, err := store.AcquireConfirmationLease(ctx, AcquireConfirmationLeaseInput{Scope: scope, TransitionID: "t", LeaseID: "", LeaseUntilMs: 2_000}); err == nil {
		t.Fatal("acquire confirmation without leaseId must fail")
	}
	if _, err := store.AcquireConfirmationLease(ctx, AcquireConfirmationLeaseInput{Scope: scope, TransitionID: "t", LeaseID: "l", LeaseUntilMs: 500}); err == nil {
		t.Fatal("lease before now must fail")
	}
	if _, err := store.AcquireCanaryLease(ctx, AcquireCanaryLeaseInput{Scope: scope, TransitionID: "t", LeaseID: "l", LeaseUntilMs: 500}); err == nil {
		t.Fatal("canary lease before now must fail")
	}
	// 空白证据键由 fallback seed 归一化，输入校验放行后落入 Lua 的 not_found。
	if result, err := store.CloseSuspectFromObserver(ctx, CloseSuspectFromObserverInput{Scope: scope, TransitionID: "t", ExpectedFailureEvidenceKey: "", ObserverEvidenceKey: ""}); err != nil || result.Status != MutationNotFound {
		t.Fatalf("close observer = (%s, %v)", result.Status, err)
	}
	if result, err := store.CloseSuspectFromKeyRotation(ctx, CloseSuspectFromKeyRotationInput{Scope: scope, TransitionID: "t", ExpectedFailureEvidenceKey: ""}); err != nil || result.Status != MutationNotFound {
		t.Fatalf("close key rotation = (%s, %v)", result.Status, err)
	}
	if _, err := store.CompleteConfirmation(ctx, CompleteConfirmationInput{Scope: scope, TransitionID: "t", LeaseID: "l", Outcome: "bogus"}); err == nil {
		t.Fatal("invalid outcome must fail")
	}
	if result, err := store.CompleteCanary(ctx, CompleteCanaryInput{Scope: scope, TransitionID: "t", LeaseID: "l", Outcome: OutcomeTransportFailure}); err != nil || result.Status != MutationNotFound {
		t.Fatalf("canary transport failure on missing state = (%s, %v)", result.Status, err)
	}
	if _, err := store.CompleteConfirmation(ctx, CompleteConfirmationInput{Scope: scope, TransitionID: "t", LeaseID: "l", Outcome: OutcomeTransportFailure, FailureEvidenceKey: strPtr(strings.Repeat("a", 64)), FramingCompleteDisposition: strPtr("bogus")}); err == nil {
		t.Fatal("invalid framing disposition must fail")
	}
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, ProtocolModelOpenEvidenceInput{Scope: scope, ConfirmedFailureCount: 0, WindowMs: 60_000, MaxProtocolScopes: 4}); err == nil {
		t.Fatal("invalid confirmedFailureCount must fail")
	}
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, ProtocolModelOpenEvidenceInput{Scope: scope, ConfirmedFailureCount: 1, WindowMs: 0, MaxProtocolScopes: 4}); err == nil {
		t.Fatal("invalid windowMs must fail")
	}
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, ProtocolModelOpenEvidenceInput{Scope: scope, ConfirmedFailureCount: 1, WindowMs: 60_000, MaxProtocolScopes: 0}); err == nil {
		t.Fatal("invalid maxProtocolScopes must fail")
	}
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, ProtocolModelOpenEvidenceInput{Scope: scope, ConfirmedFailureCount: 1, WindowMs: 60_000, MaxProtocolScopes: 4, EvidenceID: " ", AccountTransitionID: "t", Reason: "r"}); err == nil {
		t.Fatal("missing evidenceId must fail")
	}
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, ProtocolModelOpenEvidenceInput{Scope: scope, ConfirmedFailureCount: 1, WindowMs: 60_000, MaxProtocolScopes: 4, EvidenceID: "e", AccountTransitionID: " ", Reason: "r"}); err == nil {
		t.Fatal("missing accountTransitionId must fail")
	}
	if _, err := store.RecordProtocolModelOpenEvidence(ctx, ProtocolModelOpenEvidenceInput{Scope: scope, ConfirmedFailureCount: 1, WindowMs: 60_000, MaxProtocolScopes: 4, EvidenceID: "e", AccountTransitionID: "t", Reason: " "}); err == nil {
		t.Fatal("missing reason must fail")
	}
	if _, err := store.ListDue(ctx, 1_000, 0); err == nil {
		t.Fatal("zero limit must fail")
	}
}

func TestW14EValidateOperationPayloadArms(t *testing.T) {
	nowMs := int64(1_000)
	evidence := strings.Repeat("a", 64)

	if err := validateOperationPayload("get", map[string]any{}); err != nil {
		t.Fatalf("get skips transitionId: %v", err)
	}
	if err := validateOperationPayload("suspect", map[string]any{"transitionId": ""}); err == nil {
		t.Fatal("empty transitionId must fail")
	}
	if err := validateOperationPayload("replace_revision", map[string]any{"transitionId": "t"}); err == nil {
		t.Fatal("missing dispatchRevision must fail")
	}
	if err := validateOperationPayload("suspect", map[string]any{"transitionId": "t", "dispatchRevision": "5", "confirmationFailuresRequired": int64(0)}); err == nil {
		t.Fatal("invalid confirmationFailuresRequired must fail")
	}
	if err := validateOperationPayload("suspect", map[string]any{"transitionId": "t", "dispatchRevision": "5"}); err == nil {
		t.Fatal("missing failureEvidenceKey must fail")
	}
	if err := validateOperationPayload("acquire_confirmation", map[string]any{"transitionId": "t", "leaseId": "l", "nowMs": nowMs}); err == nil {
		t.Fatal("missing leaseUntilMs must fail")
	}
	if err := validateOperationPayload("acquire_confirmation", map[string]any{"transitionId": "t", "leaseId": "l", "nowMs": nowMs, "leaseUntilMs": nowMs}); err == nil {
		t.Fatal("lease not after now must fail")
	}
	if err := validateOperationPayload("acquire_confirmation", map[string]any{
		"transitionId": "t", "leaseId": "l", "nowMs": nowMs, "leaseUntilMs": nowMs + 1,
		"expectedFailureEvidenceKey": "short", "confirmationEvidenceKey": evidence,
	}); err == nil {
		t.Fatal("invalid expected evidence key must fail")
	}
	if err := validateOperationPayload("acquire_canary", map[string]any{"transitionId": "t", "leaseId": "l", "nowMs": nowMs, "leaseUntilMs": nowMs + 1}); err != nil {
		t.Fatalf("valid acquire canary: %v", err)
	}
	if err := validateOperationPayload("close_suspect_key_rotation", map[string]any{"transitionId": "t", "expectedFailureEvidenceKey": "short"}); err == nil {
		t.Fatal("invalid close evidence must fail")
	}
	if err := validateOperationPayload("close_suspect_observer", map[string]any{"transitionId": "t", "expectedFailureEvidenceKey": evidence}); err == nil {
		t.Fatal("missing observerEvidenceKey must fail")
	}
	if err := validateOperationPayload("complete_confirmation", map[string]any{"transitionId": "t", "leaseId": "l", "outcome": OutcomeTransportFailure}); err == nil {
		t.Fatal("transport failure without evidence must fail")
	}
	if err := validateOperationPayload("complete_confirmation", map[string]any{
		"transitionId": "t", "leaseId": "l", "outcome": OutcomeFramingComplete,
		"framingCompleteDisposition": "bogus",
	}); err == nil {
		t.Fatal("invalid framing disposition must fail")
	}
	if err := validateOperationPayload("complete_canary", map[string]any{"transitionId": "t", "leaseId": "l", "outcome": OutcomeUnknown}); err != nil {
		t.Fatalf("valid complete canary: %v", err)
	}
}

func TestW14EParseListDuePageArms(t *testing.T) {
	if _, err := parseListDuePage(""); err == nil {
		t.Fatal("empty payload must fail")
	}
	if _, err := parseListDuePage("{bad"); err == nil {
		t.Fatal("invalid json must fail")
	}
	if _, err := parseListDuePage(`{"scanned":1,"nextOffset":2}`); err == nil {
		t.Fatal("missing scopeKeys must fail")
	}
	if _, err := parseListDuePage(`{"scopeKeys":[],"scanned":-1,"nextOffset":2}`); err == nil {
		t.Fatal("negative scanned must fail")
	}
	page, err := parseListDuePage(`{"scopeKeys":["a",2,null],"scanned":3,"nextOffset":4,"exhausted":true}`)
	if err != nil {
		t.Fatalf("valid page: %v", err)
	}
	if !page.exhausted || page.scanned != 3 || page.nextOffset != 4 || len(page.scopeKeys) != 3 {
		t.Fatalf("page = %+v", page)
	}
}

// ---------------------------------------------------------------------------
// types.go：UnmarshalJSON 变体与 ScopeKey 无效 kind。
// ---------------------------------------------------------------------------

func TestW14ETypesUnmarshalAndScopeArms(t *testing.T) {
	var list stringList
	if err := list.UnmarshalJSON([]byte("null")); err != nil || list != nil {
		t.Fatalf("null list = %v, %v", list, err)
	}
	if err := list.UnmarshalJSON([]byte("{}")); err != nil || len(list) != 0 {
		t.Fatalf("empty object list = %v, %v", list, err)
	}
	if err := list.UnmarshalJSON([]byte(`["a","b"]`)); err != nil || len(list) != 2 {
		t.Fatalf("array list = %v, %v", list, err)
	}
	if err := list.UnmarshalJSON([]byte("[1]")); err == nil {
		t.Fatal("non-string entries must fail")
	}

	var states stateList
	if err := states.UnmarshalJSON([]byte("null")); err != nil || states != nil {
		t.Fatalf("null states = %v, %v", states, err)
	}
	if err := states.UnmarshalJSON([]byte("[]")); err != nil || states != nil {
		t.Fatalf("empty states = %v, %v", states, err)
	}
	if err := states.UnmarshalJSON([]byte(`[{}]`)); err != nil || len(states) != 1 {
		t.Fatalf("state entries = %v, %v", states, err)
	}
	if err := states.UnmarshalJSON([]byte("[1]")); err == nil {
		t.Fatal("non-state entries must fail")
	}

	if _, err := ScopeKey(Scope{Kind: "bogus", AccountRuntimeKey: "a"}); err == nil || !strings.Contains(err.Error(), "kind 无效") {
		t.Fatalf("invalid kind err = %v", err)
	}
	// MustScopeKey 对已验证作用域不 panic。
	if MustScopeKey(accountScope("w14e-must")) == "" {
		t.Fatal("must scope key empty")
	}
	// ConfirmationFailureCountOf 缺省与无效分支。
	if count, err := ConfirmationFailureCountOf(State{}); err != nil || count != 0 {
		t.Fatalf("count default = %d, %v", count, err)
	}
	negative := int64(-2)
	if _, err := ConfirmationFailureCountOf(State{ConfirmationFailureCount: &negative}); err == nil {
		t.Fatal("negative count must fail")
	}
	// FailureEvidenceKeysOf / LastFailureEvidenceKey 错误分支。
	if _, err := FailureEvidenceKeysOf(State{ConfirmationFailuresRequired: &negative}); err == nil {
		t.Fatal("invalid required must fail evidence keys")
	}
	if _, _, err := LastFailureEvidenceKey(State{ConfirmationFailuresRequired: &negative}); err == nil {
		t.Fatal("invalid required must fail last evidence key")
	}
	sha := strings.Repeat("b", 64)
	keys, err := FailureEvidenceKeysOf(State{ConfirmationFailuresRequired: int64Ptr(2), FailureEvidenceKeys: stringList{sha, strings.ToUpper(sha), "nope", sha}})
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys = %v, %v", keys, err)
	}
}
