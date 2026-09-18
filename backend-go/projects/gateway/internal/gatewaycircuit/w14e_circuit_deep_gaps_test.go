package gatewaycircuit

// w14e 覆盖率补强（第三批）：persistWithRetry 冲突嵌套、worker 重试定时器、
// ReconcileActive、probe 替换/加入分支、等待协调器真实定时器、store_memory
// 剩余可达臂。

import (
	"context"
	"errors"

	"strings"
	"sync"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
)

// ---------------------------------------------------------------------------
// bridge：CAS 冲突嵌套与重试耗尽。
// ---------------------------------------------------------------------------

func TestW14EPersistWithRetryConflictNesting(t *testing.T) {
	protocol := protocolScope("w14e-conflict")
	conflictIncident := w14eIncidentRecord(protocol, "inc-1", 5)
	conflictIncident.UpdatedAtMs = 2_000

	t.Run("conflict without incident exhausts attempts", func(t *testing.T) {
		store := newNonExpiringMemoryStore(t)
		db := &mockControlPlaneDB{}
		attempts := 0
		bridge := w14eNewBridge(t, store, db, func(o *BridgeOptions) {
			o.MaxPersistAttempts = 3
			o.RetryDelayMs = 1
			o.PersistIncident = func(ctx context.Context, input CompareAndSetIncidentInput) (CompareAndSetIncidentResult, error) {
				attempts++
				return CompareAndSetIncidentResult{Status: CASConflict}, nil
			}
		})
		state := ClosedState(protocol, "7", 1, "t1", 900)
		state.Phase = PhaseSuspect
		bridge.Observe(protocol, state)
		w14eWaitWorkersDone(t, bridge)
		if attempts != 3 {
			t.Fatalf("attempts = %d", attempts)
		}
		bridge.mu.Lock()
		_, hasPending := bridge.pending[MustScopeKey(protocol)]
		bridge.mu.Unlock()
		if !hasPending {
			t.Fatal("exhausted attempts must retain the pending observation")
		}
	})

	t.Run("conflict with matching incident converges", func(t *testing.T) {
		store := newNonExpiringMemoryStore(t)
		db := &mockControlPlaneDB{}
		bridge := w14eNewBridge(t, store, db, func(o *BridgeOptions) {
			o.MaxPersistAttempts = 2
			o.RetryDelayMs = 1
			o.PersistIncident = func(ctx context.Context, input CompareAndSetIncidentInput) (CompareAndSetIncidentResult, error) {
				conflict := conflictIncident
				return CompareAndSetIncidentResult{Status: CASConflict, Incident: &conflict}, nil
			}
		})
		// 运行态先恢复成与冲突 ledger 一致的状态：匹配后立即收敛。
		runtimeState := IncidentToRuntimeState(conflictIncident, map[string]string{})
		if _, err := store.Restore(context.Background(), runtimeState, int64Ptr(2_000)); err != nil {
			t.Fatalf("restore: %v", err)
		}
		state := ClosedState(protocol, "7", 1, "t1", 900)
		state.Phase = PhaseSuspect
		bridge.Observe(protocol, state)
		w14eWaitWorkersDone(t, bridge)
		bridge.mu.Lock()
		_, hasPending := bridge.pending[MustScopeKey(protocol)]
		bridge.mu.Unlock()
		if hasPending {
			t.Fatal("matching conflict must converge without retry exhaustion")
		}
	})

	t.Run("conflict with newer incident restores it", func(t *testing.T) {
		store := newNonExpiringMemoryStore(t)
		db := &mockControlPlaneDB{}
		attempts := 0
		bridge := w14eNewBridge(t, store, db, func(o *BridgeOptions) {
			o.MaxPersistAttempts = 3
			o.RetryDelayMs = 1
			o.PersistIncident = func(ctx context.Context, input CompareAndSetIncidentInput) (CompareAndSetIncidentResult, error) {
				attempts++
				newer := conflictIncident
				newer.DispatchRevision = int64(7 + attempts)
				return CompareAndSetIncidentResult{Status: CASConflict, Incident: &newer}, nil
			}
		})
		state := ClosedState(protocol, "7", 1, "t1", 900)
		state.Phase = PhaseSuspect
		bridge.Observe(protocol, state)
		w14eWaitWorkersDone(t, bridge)
		if attempts < 1 {
			t.Fatalf("attempts = %d", attempts)
		}
		// 冲突中的更新 incident 恢复后立即收敛（匹配即成功）。
		restored, err := store.Get(context.Background(), protocol, int64Ptr(3_000))
		if err != nil || restored.Generation != 5 || restored.Phase != PhaseSuspect {
			t.Fatalf("restored = (%+v, %v)", restored, err)
		}
	})

	t.Run("first refresh error recovers on retry", func(t *testing.T) {
		wrapped := &w14eStoreErrors{Store: newNonExpiringMemoryStore(t)}
		db := &mockControlPlaneDB{}
		bridge := w14eNewBridge(t, wrapped, db, func(o *BridgeOptions) {
			o.MaxPersistAttempts = 2
			o.RetryDelayMs = 1
		})
		wrapped.mu.Lock()
		wrapped.getErr = errors.New("w14e transient get boom")
		wrapped.mu.Unlock()
		state := ClosedState(protocol, "7", 1, "t1", 900)
		state.Phase = PhaseSuspect
		bridge.Observe(protocol, state)
		wrapped.mu.Lock()
		wrapped.getErr = nil
		wrapped.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		wrapped.mu.Lock()
		wrapped.getErr = nil
		wrapped.mu.Unlock()
		// 第一次刷新失败消耗一次尝试；重试等待期间清除错误后收敛。
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			wrapped.mu.Lock()
			blocked := wrapped.getErr != nil
			wrapped.mu.Unlock()
			db.mu.Lock()
			cas := len(db.casCalls)
			db.mu.Unlock()
			if !blocked && cas == 1 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		wrapped.mu.Lock()
		wrapped.getErr = nil
		wrapped.mu.Unlock()
		w14eWaitWorkersDone(t, bridge)
		db.mu.Lock()
		cas := len(db.casCalls)
		db.mu.Unlock()
		if cas != 1 {
			t.Fatalf("cas calls = %d", cas)
		}
	})
}

// ---------------------------------------------------------------------------
// bridge：真实重试定时器与 worker 调度。
// ---------------------------------------------------------------------------

func TestW14EBridgeRetryTimerRestartsWorker(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	// 前三次 CAS 直接失败：失败后由真实定时器重启 worker 再试。
	db.casErrs = []error{errors.New("w14e cas boom 1"), errors.New("w14e cas boom 2"), errors.New("w14e cas boom 3")}
	// 缺省 NewTimer + 1ms 重试延迟。
	bridge, err := NewBridge(BridgeOptions{
		Store: store, DB: db,
		Now:                func() int64 { return 1_000 },
		RetryDelayMs:       1,
		MaxPersistAttempts: 1,
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	t.Cleanup(bridge.Close)
	_ = db.casErrs
	state := ClosedState(protocolScope("w14e-timer"), "7", 1, "t1", 900)
	state.Phase = PhaseSuspect
	bridge.Observe(protocolScope("w14e-timer"), state)
	// 首轮失败后 pending 保留、重试定时器登记。
	deadline := time.Now().Add(2 * time.Second)
	retries := 0
	for time.Now().Before(deadline) {
		db.mu.Lock()
		retries = len(db.casCalls)
		db.mu.Unlock()
		if retries >= 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if retries < 3 {
		t.Fatalf("retry timer must restart the worker, cas calls = %d", retries)
	}
}

func TestW14EBridgeReconcileActiveArms(t *testing.T) {
	ctx := context.Background()
	childScope := protocolScope("w14e-reconcile")
	child := w14eIncidentRecord(childScope, "leaf-1", 1)
	accountScopeW14E := Scope{Kind: ScopeKindAccount, AccountRuntimeKey: "w14e-reconcile"}
	parent := w14eIncidentRecord(accountScopeW14E, "parent-1", 1)
	parent.ChildIncidentIDs = []string{"leaf-1"}

	t.Run("load page timeout surfaces rebuild error", func(t *testing.T) {
		store := newNonExpiringMemoryStore(t)
		db := &w14eControlPlaneDB{mockControlPlaneDB: &mockControlPlaneDB{}}
		db.rebuildPage = func(call int) (RebuildPage, error) {
			time.Sleep(50 * time.Millisecond)
			return RebuildPage{}, errors.New("slow page")
		}
		bridge := w14eNewBridge(t, store, db, func(o *BridgeOptions) {
			o.RebuildPageTimeoutMs = 1
		})
		bridge.mu.Lock()
		bridge.globallyReady = true
		bridge.mu.Unlock()
		_, err := bridge.ReconcileActive(ctx, 5)
		if err == nil || !strings.Contains(err.Error(), RebuildReasonRebuildTimeout) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("unresolved child loads account incidents", func(t *testing.T) {
		store := newNonExpiringMemoryStore(t)
		db := &w14eControlPlaneDB{mockControlPlaneDB: &mockControlPlaneDB{}}
		db.incidents = map[string]IncidentRecord{"w14e-reconcile": child, "w14e-reconcile-parent": parent}
		db.rebuildPage = func(call int) (RebuildPage, error) {
			if call == 1 {
				return RebuildPage{Items: []IncidentRecord{parent}}, nil
			}
			return RebuildPage{}, nil
		}
		bridge := w14eNewBridge(t, store, db, func(o *BridgeOptions) {
			o.RebuildPageTimeoutMs = 1_000
		})
		bridge.mu.Lock()
		bridge.globallyReady = true
		bridge.mu.Unlock()
		repaired, err := bridge.ReconcileActive(ctx, 5)
		if err != nil || repaired != 1 {
			t.Fatalf("repaired = (%d, %v)", repaired, err)
		}
	})

	t.Run("restore error surfaces", func(t *testing.T) {
		wrapped := &w14eStoreErrors{Store: newNonExpiringMemoryStore(t)}
		db := &w14eControlPlaneDB{mockControlPlaneDB: &mockControlPlaneDB{}}
		db.rebuildPage = func(call int) (RebuildPage, error) {
			if call == 1 {
				return RebuildPage{Items: []IncidentRecord{child}}, nil
			}
			return RebuildPage{}, nil
		}
		bridge := w14eNewBridge(t, wrapped, db, func(o *BridgeOptions) {
			o.RebuildPageTimeoutMs = 1_000
		})
		bridge.mu.Lock()
		bridge.globallyReady = true
		bridge.mu.Unlock()
		wrapped.mu.Lock()
		wrapped.restoreErr = errors.New("w14e reconcile restore boom")
		wrapped.mu.Unlock()
		if _, err := bridge.ReconcileActive(ctx, 5); err == nil || !strings.Contains(err.Error(), "reconcile restore") {
			t.Fatalf("err = %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// store_memory：剩余可达臂。
// ---------------------------------------------------------------------------

func TestW14EMemoryStoreEvidenceWindowTrim(t *testing.T) {
	ctx := context.Background()
	now := int64(10_000)
	clock := &now
	scope := accountScope("w14e-trim")
	store := w14eMemoryStore(t, func(o *MemoryStoreOptions) { o.Now = func() int64 { return *clock } })

	// CFR=2 → 保留窗口 keep=3：预置满窗口证据后一次独立完成触发裁剪。
	required := int64(2)
	count := int64(0)
	state := ClosedState(scope, "5", 1, "t1", *clock)
	state.Phase = PhaseSuspect
	state.ConfirmationFailuresRequired = &required
	state.ConfirmationFailureCount = &count
	retryAt := *clock
	state.RetryAtMs = &retryAt
	seeded := stringList{}
	for index := 0; index < 3; index++ {
		seeded = append(seeded, strings.Repeat(string(rune('a'+index)), 64))
	}
	state.FailureEvidenceKeys = seeded
	if result, err := store.Restore(ctx, state, clock); err != nil || result.Status != MutationApplied {
		t.Fatalf("restore = (%s, %v)", result.Status, err)
	}

	lease := "lease-trim"
	if acquired, err := store.AcquireConfirmationLease(ctx, AcquireConfirmationLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "acq-0",
		LeaseID: lease, LeaseUntilMs: *clock + 1_000, NowMs: clock,
	}); err != nil || acquired.Status != MutationApplied {
		t.Fatalf("acquire = (%s, %v)", acquired.Status, err)
	}
	evidence := strings.Repeat("z", 64)
	result, err := store.CompleteConfirmation(ctx, CompleteConfirmationInput{
		Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "cmp-0",
		LeaseID: lease, Outcome: OutcomeTransportFailure, FailureEvidenceKey: &evidence, NowMs: clock,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	keys := []string(result.State.FailureEvidenceKeys)
	if len(keys) != 3 {
		t.Fatalf("evidence window must stay at keep=3: %v", keys)
	}
	if keys[2] != sha256Hex("confirmation:lease-trim") || keys[0] != seeded[1] {
		t.Fatalf("trim must drop the oldest evidence: %v", keys)
	}
	if result.State.ConfirmationFailureCount == nil || *result.State.ConfirmationFailureCount != 1 {
		t.Fatalf("count = %v", result.State.ConfirmationFailureCount)
	}
}

func TestW14EMemoryStoreCanaryOnLeasedOpenState(t *testing.T) {
	ctx := context.Background()
	now := int64(10_000)
	clock := &now
	scope := accountScope("w14e-leased-open")
	store := w14eMemoryStore(t, func(o *MemoryStoreOptions) { o.Now = func() int64 { return *clock } })

	// 恢复一个带确认租约的 open 相位：canary 获取必须命中租约冲突臂。
	state := ClosedState(scope, "5", 1, "t1", *clock)
	state.Phase = PhaseOpen
	state.Lease = &Lease{Kind: LeaseKindConfirmation, LeaseID: "lease", LeaseUntilMs: *clock + 10_000}
	if _, err := store.Restore(ctx, state, clock); err != nil {
		t.Fatalf("restore: %v", err)
	}
	*clock += 1_000
	result, err := store.AcquireCanaryLease(ctx, AcquireCanaryLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "5",
		TransitionID: "c1", LeaseID: "l1", LeaseUntilMs: *clock + 1_000, NowMs: clock,
	})
	if err != nil || result.Status != MutationStateMismatch {
		t.Fatalf("canary on leased open = (%s, %v)", result.Status, err)
	}

	// openLocked 的 IncidentID 缺失回退：无 IncidentID 的 open 状态在
	// transport failure 下重开并继承 transitionID。
	bare := ClosedState(accountScope("w14e-bare-open"), "5", 1, "bare-open", *clock)
	bare.Phase = PhaseOpen
	bare.IncidentID = nil
	if _, err := store.Restore(ctx, bare, clock); err != nil {
		t.Fatalf("restore bare: %v", err)
	}
	*clock += 1_000
	acquired, err := store.AcquireCanaryLease(ctx, AcquireCanaryLeaseInput{
		Scope: accountScope("w14e-bare-open"), Generation: 1, DispatchRevision: "5",
		TransitionID: "c2", LeaseID: "l2", LeaseUntilMs: *clock + 1_000, NowMs: clock,
	})
	if err != nil || acquired.Status != MutationApplied {
		t.Fatalf("acquire = (%s, %v)", acquired.Status, err)
	}
	failure, err := store.CompleteCanary(ctx, CompleteCanaryInput{
		Scope: accountScope("w14e-bare-open"), Generation: 1, DispatchRevision: "5",
		TransitionID: "c3", LeaseID: "l2", Outcome: OutcomeTransportFailure, NowMs: clock,
	})
	if err != nil || failure.State.Phase != PhaseOpen || failure.State.IncidentID == nil || *failure.State.IncidentID != "c3" {
		t.Fatalf("reopen = (%+v, %v)", failure.State, err)
	}
}

func TestW14EMemoryStoreEscalationReasonAndCapacityArms(t *testing.T) {
	ctx := context.Background()
	now := int64(10_000)
	clock := &now

	protocols := []Scope{
		{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "w14e-esc-cap", ProtocolProfile: "profile-1", RequestLane: LaneText, ModelBucket: "gpt"},
		{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "w14e-esc-cap", ProtocolProfile: "profile-2", RequestLane: LaneText, ModelBucket: "gpt"},
		{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "w14e-esc-cap", ProtocolProfile: "profile-3", RequestLane: LaneText, ModelBucket: "gpt"},
	}

	// 容量耗尽臂：3 个协议条目占满容量后，账户条目无法创建。
	capacityStore := w14eMemoryStore(t, func(o *MemoryStoreOptions) { o.Capacity = 3; o.Now = func() int64 { return *clock } })
	for index, protocol := range protocols {
		w14eRestoreOpen(t, capacityStore, protocol, "5", "p-open-"+string(rune('1'+index)), clock)
	}
	mk := func(store *MemoryStore, protocol Scope, evidenceID, transitionID, reason string) ProtocolModelOpenEvidenceInput {
		return ProtocolModelOpenEvidenceInput{
			Scope: protocol, Generation: 1, DispatchRevision: "5", EvidenceID: evidenceID,
			AccountTransitionID: transitionID, Reason: reason,
			ConfirmedFailureCount: 1, DistinctScopeThreshold: 3, WindowMs: 60_000, MaxProtocolScopes: 8, NowMs: clock,
		}
	}
	if _, err := capacityStore.RecordProtocolModelOpenEvidence(ctx, mk(capacityStore, protocols[0], "ev-a", "", "r")); err != nil {
		t.Fatalf("first evidence: %v", err)
	}
	if _, err := capacityStore.RecordProtocolModelOpenEvidence(ctx, mk(capacityStore, protocols[1], "ev-b", "", "r")); err != nil {
		t.Fatalf("second evidence: %v", err)
	}
	capacity := mk(capacityStore, protocols[2], "ev-c", "acc-open", "protocol escalations")
	result, err := capacityStore.RecordProtocolModelOpenEvidence(ctx, capacity)
	if err != nil || result.Status != EscalationCapacityExceeded {
		t.Fatalf("capacity escalation = (%s, %v)", result.Status, err)
	}

	// 缺 reason：阈值满足后的升级路径必须报错。
	reasonStore := w14eMemoryStore(t, func(o *MemoryStoreOptions) { o.Now = func() int64 { return *clock } })
	for index, protocol := range protocols {
		w14eRestoreOpen(t, reasonStore, protocol, "5", "p-open-"+string(rune('1'+index)), clock)
	}
	if _, err := reasonStore.RecordProtocolModelOpenEvidence(ctx, mk(reasonStore, protocols[0], "ev-a", "", "r")); err != nil {
		t.Fatalf("first evidence: %v", err)
	}
	if _, err := reasonStore.RecordProtocolModelOpenEvidence(ctx, mk(reasonStore, protocols[1], "ev-b", "", "r")); err != nil {
		t.Fatalf("second evidence: %v", err)
	}
	noReason := mk(reasonStore, protocols[2], "ev-c", "acc-open", " ")
	if _, err := reasonStore.RecordProtocolModelOpenEvidence(ctx, noReason); err == nil {
		t.Fatal("missing reason must fail escalation")
	}
}

// ---------------------------------------------------------------------------
// store_redis：连接失败错误臂。
// ---------------------------------------------------------------------------

func TestW14ERedisStoreConnectionErrorArms(t *testing.T) {
	server := miniredis.RunT(t)
	store, err := NewRedisStore(RedisStoreOptions{RedisURL: "redis://" + server.Addr(), Name: "w14e-down", Capacity: 10, Now: func() int64 { return 1_000 }})
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	server.Close()
	ctx := context.Background()
	scope := accountScope("w14e-down")
	if _, err := store.Suspect(ctx, SuspectInput{Scope: scope, DispatchRevision: "5", TransitionID: "t1", FailureEvidenceKey: strPtr(strings.Repeat("a", 64))}); err == nil {
		t.Fatal("suspect on closed redis must fail")
	}
	if _, err := store.Get(ctx, scope, nil); err == nil {
		t.Fatal("get on closed redis must fail")
	}
	if _, err := store.ListDue(ctx, 1_000, 5); err == nil {
		t.Fatal("list due on closed redis must fail")
	}
}

// ---------------------------------------------------------------------------
// probe：合并围栏加入与替换已结算代。
// ---------------------------------------------------------------------------

func TestW14EProbeMergeFenceAndReplaceSettled(t *testing.T) {
	ctx := context.Background()
	store := newMockProbeStore()
	coordinator := NewProbeCoordinator(store, func() int64 { return 5_000 }, func() string { return "w14e-owner" })

	first := testFence("w14e-fence-a")
	acquired, err := coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w14e-probe", SourceFence: &first, ExecutionRole: "source_dispatch",
	})
	if err != nil || acquired.Disposition != ProbeDispositionOwner {
		t.Fatalf("acquire = (%s, %v)", acquired.Disposition, err)
	}
	if _, err := coordinator.Settle(ctx, SettleProbeInput{
		RuntimeKey: acquired.RuntimeKey, Generation: acquired.Generation, OwnerToken: acquired.OwnerToken,
		Outcome: ProbeOutcomeSuccess, NowMs: int64Ptr(6_000),
	}); err != nil {
		t.Fatalf("settle: %v", err)
	}
	// 已结算 + 新围栏 + source_dispatch：合并围栏并替换已结算代。
	second := testFence("w14e-fence-b")
	replacement, err := coordinator.Acquire(ctx, ProbeAcquireInput{
		AccountRuntimeScope: "w14e-probe", SourceFence: &second, ExecutionRole: "source_dispatch",
		NowMs: int64Ptr(7_000),
	})
	if err != nil || replacement.Disposition != ProbeDispositionOwner {
		t.Fatalf("replacement = (%s, %v)", replacement.Disposition, err)
	}
	if replacement.ReplacedFenceSettlement == nil || replacement.ReplacedFenceSettlement.Outcome != ProbeOutcomeSuccess {
		t.Fatalf("replaced settlement = %+v", replacement.ReplacedFenceSettlement)
	}
	fences, err := coordinator.SourceFences(ctx, replacement.RuntimeKey, replacement.Generation)
	if err != nil || len(fences) == 0 {
		t.Fatalf("fences = (%v, %v)", fences, err)
	}
}

// ---------------------------------------------------------------------------
// wait：真实定时器下的协调器轮次。
// ---------------------------------------------------------------------------

func TestW14EWaitCoordinatorRealTimerTurns(t *testing.T) {
	coordinator := NewWaitCoordinator(WaitCoordinatorOptions{
		MaxWaitersPerScope: 4,
		MaxWaitersGlobal:   8,
		// 默认真实定时器；DelayMs=5ms 让计时器立即进入触发路径。
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan string, 1)
	go func() {
		done <- coordinator.WaitForTurn(WaitTurnInput{
			ScopeKey: "w14e-wait", Reason: "recoverable_unavailable", DelayMs: 5,
			DeadlineAtMs: time.Now().UnixMilli() + 5_000, Signal: ctx,
		})
	}()
	select {
	case turn := <-done:
		if turn != TurnReady {
			t.Fatalf("turn = %s", turn)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timer turn never resolved")
	}

	// 截止时间已过：直接 TurnDeadlineExceeded。
	deadline := coordinator.WaitForTurn(WaitTurnInput{
		ScopeKey: "w14e-wait-2", Reason: "recoverable_unavailable", DelayMs: 5,
		DeadlineAtMs: time.Now().UnixMilli() - 1, Signal: ctx,
	})
	if deadline != TurnDeadlineExceeded {
		t.Fatalf("deadline turn = %s", deadline)
	}
}

// ---------------------------------------------------------------------------
// service：并发结算等待方分支。
// ---------------------------------------------------------------------------

func TestW14EAttemptSettlementWaiterReceivesResult(t *testing.T) {
	now := int64(0)
	clock := &now
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	ctx := context.Background()

	if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{
		scope:            protocolModelScope(testAccount(), LaneText, strPtr("gpt-4o")),
		dispatchRevision: revisionOf(t, testAccount()), confirmationFailuresRequired: int64Ptr(2),
		reason: "transport:connect failed",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 3_000
	prepared, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: strPtr(strings.Repeat("f", 64)),
	})
	if err != nil || prepared.Attempt == nil {
		t.Fatalf("prepare = %v", err)
	}
	attempt := prepared.Attempt
	start := make(chan struct{})
	creatorResult := make(chan *MutationResult, 1)
	waiterResult := make(chan *MutationResult, 3)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		result, err := attempt.ReportUnknown(ctx)
		if err != nil {
			t.Errorf("creator: %v", err)
			return
		}
		creatorResult <- result
	}()
	wg.Add(3)
	for index := 0; index < 3; index++ {
		go func() {
			defer wg.Done()
			<-start
			result, err := attempt.ReportUnknown(ctx)
			if err != nil {
				t.Errorf("waiter: %v", err)
				return
			}
			waiterResult <- result
		}()
	}
	close(start)
	wg.Wait()
	creator := <-creatorResult
	for index := 0; index < 3; index++ {
		select {
		case waiter := <-waiterResult:
			if waiter.Status != creator.Status {
				t.Fatalf("waiter result diverged: %+v vs %+v", waiter, creator)
			}
		default:
			t.Fatalf("waiter %d missing result", index)
		}
	}
}
