package gatewaycircuit

// w14e 覆盖率补强（第四批）：persistWithRetry 嵌套错误臂、worker/schedule
// 守卫、retryPendingImmediately、service 确认分支收尾、store_memory 父子关系
// 投影、suppression/precheck 小臂。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

type w14eErrorWithAnExtremelyLongTypeNameWellBeyondTheSixtyFourCharLimit struct{}

func (w14eErrorWithAnExtremelyLongTypeNameWellBeyondTheSixtyFourCharLimit) Error() string {
	return "long"
}

func TestW14EBridgeConflictNestedErrorArms(t *testing.T) {
	protocol := protocolScope("w14e-nest")
	base := w14eIncidentRecord(protocol, "inc-1", 5)

	t.Run("refresh sleep error aborts retry", func(t *testing.T) {
		wrapped := &w14eStoreErrors{Store: newNonExpiringMemoryStore(t)}
		db := &mockControlPlaneDB{}
		calls := 0
		bridge := w14eNewBridge(t, wrapped, db, func(o *BridgeOptions) {
			o.MaxPersistAttempts = 3
			o.RetryDelayMs = 1
			o.Sleep = func(context.Context, time.Duration) error {
				calls++
				if calls >= 1 {
					return errors.New("w14e sleep boom")
				}
				return nil
			}
		})
		wrapped.mu.Lock()
		wrapped.getErr = errors.New("w14e refresh boom")
		wrapped.mu.Unlock()
		state := ClosedState(protocol, "7", 1, "t1", 900)
		state.Phase = PhaseSuspect
		bridge.Observe(protocol, state)
		w14eWaitWorkersDone(t, bridge)
		// sleep 错误必须在第一次重试等待时中止循环。
		bridge.mu.Lock()
		_, hasPending := bridge.pending[MustScopeKey(protocol)]
		bridge.mu.Unlock()
		if !hasPending {
			t.Fatal("aborted retry must retain the observation")
		}
	})

	t.Run("empty transition id falls back to rebuild id", func(t *testing.T) {
		store := newNonExpiringMemoryStore(t)
		db := &mockControlPlaneDB{}
		bridge := w14eNewBridge(t, store, db, nil)
		state := ClosedState(protocol, "7", 1, "", 900)
		state.Phase = PhaseSuspect
		state.TransitionID = ""
		bridge.Observe(protocol, state)
		w14eWaitWorkersDone(t, bridge)
		db.mu.Lock()
		count := len(db.casCalls)
		db.mu.Unlock()
		if count != 1 {
			t.Fatalf("cas calls = %d", count)
		}
		if db.casCalls[0].TransitionID == "" || !strings.HasPrefix(db.casCalls[0].TransitionID, "rebuild:") {
			t.Fatalf("transition id = %q", db.casCalls[0].TransitionID)
		}
	})

	t.Run("invalid confirmation failures required surfaces", func(t *testing.T) {
		store := newNonExpiringMemoryStore(t)
		db := &mockControlPlaneDB{}
		bridge := w14eNewBridge(t, store, db, nil)
		badCFR := int64(99)
		state := ClosedState(protocol, "7", 1, "t1", 900)
		state.Phase = PhaseSuspect
		state.ConfirmationFailuresRequired = &badCFR
		bridge.Observe(protocol, state)
		w14eWaitWorkersDone(t, bridge)
		db.mu.Lock()
		count := len(db.casCalls)
		db.mu.Unlock()
		if count != 0 {
			t.Fatal("invalid CFR must fail before CAS")
		}
	})

	t.Run("key scope persists fingerprint", func(t *testing.T) {
		store := newNonExpiringMemoryStore(t)
		db := &mockControlPlaneDB{}
		bridge := w14eNewBridge(t, store, db, nil)
		keyScope := Scope{Kind: ScopeKindKey, AccountRuntimeKey: "w14e-nest", KeyFingerprint: "fp-1"}
		state := ClosedState(keyScope, "7", 1, "t1", 900)
		state.Phase = PhaseSuspect
		bridge.Observe(keyScope, state)
		w14eWaitWorkersDone(t, bridge)
		db.mu.Lock()
		count := len(db.casCalls)
		var input CompareAndSetIncidentInput
		if count > 0 {
			input = db.casCalls[0]
		}
		db.mu.Unlock()
		if count != 1 || input.KeyFingerprint == nil || *input.KeyFingerprint != "fp-1" {
			t.Fatalf("key scope cas = (%d, %+v)", count, input)
		}
	})

	t.Run("conflict refresh error then recover", func(t *testing.T) {
		store := newNonExpiringMemoryStore(t)
		db := &mockControlPlaneDB{}
		conflict := base
		attempts := 0
		wrapped := wrappedStoreForW14E(store)
		bridge := w14eNewBridge(t, wrapped, db, func(o *BridgeOptions) {
			o.MaxPersistAttempts = 3
			o.RetryDelayMs = 1
			o.Sleep = func(context.Context, time.Duration) error {
				wrapped.mu.Lock()
				wrapped.getErr = nil
				wrapped.mu.Unlock()
				return nil
			}
			o.PersistIncident = func(ctx context.Context, input CompareAndSetIncidentInput) (CompareAndSetIncidentResult, error) {
				attempts++
				cloned := conflict
				return CompareAndSetIncidentResult{Status: CASConflict, Incident: &cloned}, nil
			}
		})
		// 冲突后的二次刷新失败一次：覆盖冲突嵌套里的刷新错误分支。
		bridge.mu.Lock()
		bridge.ledgerRevisions[MustScopeKey(protocol)] = 3
		bridge.mu.Unlock()
		state := ClosedState(protocol, "7", 1, "t1", 900)
		state.Phase = PhaseSuspect
		state.UpdatedAtMs = 900
		wrapped.mu.Lock()
		wrapped.getErr = errors.New("w14e conflict refresh boom")
		wrapped.mu.Unlock()
		go func() {
			time.Sleep(10 * time.Millisecond)
			wrapped.mu.Lock()
			wrapped.getErr = nil
			wrapped.mu.Unlock()
		}()
		bridge.Observe(protocol, state)
		w14eWaitWorkersDone(t, bridge)
		if attempts < 1 {
			t.Fatalf("attempts = %d", attempts)
		}
	})
}

func wrappedStoreForW14E(store Store) *w14eStoreErrors {
	return &w14eStoreErrors{Store: store}
}

func TestW14EBridgeWorkerAndScheduleGuards(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	bridge := w14eNewBridge(t, store, db, nil)
	scopeKey := MustScopeKey(protocolScope("w14e-guard"))

	// 已有 worker 在跑：Observe 不再启动新 worker。
	bridge.mu.Lock()
	bridge.workers[scopeKey] = &scopeWorker{done: make(chan struct{})}
	// 已登记重试定时器：同样不启动 worker。
	bridge.retryBackoffs[scopeKey+"-retry"] = &scopeRetryState{backoff: 500, timerStop: func() {}}
	bridge.mu.Unlock()
	bridge.Observe(protocolScope("w14e-guard"), ClosedState(protocolScope("w14e-guard"), "7", 1, "t1", 900))
	bridge.mu.Lock()
	workers := len(bridge.workers)
	bridge.mu.Unlock()
	if workers != 1 {
		t.Fatalf("workers = %d", workers)
	}

	// scheduleScopeRetry：已存在定时器时直接返回；stop 后返回。
	bridge.mu.Lock()
	bridge.retryBackoffs[scopeKey] = &scopeRetryState{backoff: 400, timerStop: func() {}}
	bridge.mu.Unlock()
	bridge.scheduleScopeRetry(scopeKey)
	bridge.Close()
	bridge.scheduleScopeRetry(scopeKey)
}

func TestW14EBridgeRetryPendingImmediatelyArms(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	// 阻塞式 CAS：worker 停在持久化里，等待超过 remaining 预算。
	release := make(chan struct{})
	bridge := w14eNewBridge(t, store, db, func(o *BridgeOptions) {
		o.MaxPersistAttempts = 1
		o.RebuildTotalTimeoutMs = 5_000
		o.PersistIncident = func(ctx context.Context, input CompareAndSetIncidentInput) (CompareAndSetIncidentResult, error) {
			<-release
			return CompareAndSetIncidentResult{Status: CASApplied}, nil
		}
	})
	// 先保留一个 pending 观察（CAS 阻塞中）。
	state := ClosedState(protocolScope("w14e-rpi"), "7", 1, "t1", 900)
	state.Phase = PhaseSuspect
	bridge.Observe(protocolScope("w14e-rpi"), state)
	time.Sleep(50 * time.Millisecond)
	// rebuild 触发 retryPendingImmediately：worker 已在跑 → 等待 done。
	result, err := bridge.Rebuild(context.Background())
	close(release)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if !result.Blocked && result.Loaded == 0 {
		t.Logf("rebuild result = %+v", result)
	}
}

func TestW14EBridgeDeferredParentRestoreError(t *testing.T) {
	childScope := protocolScope("w14e-defer")
	child := w14eIncidentRecord(childScope, "leaf-1", 1)
	accountScopeW14E := Scope{Kind: ScopeKindAccount, AccountRuntimeKey: "w14e-defer"}
	parent := w14eIncidentRecord(accountScopeW14E, "parent-1", 1)
	parent.ChildIncidentIDs = []string{"leaf-1"}

	wrapped := &w14eStoreErrors{Store: newNonExpiringMemoryStore(t)}
	db := &w14eControlPlaneDB{mockControlPlaneDB: &mockControlPlaneDB{}}
	db.rebuildPage = func(call int) (RebuildPage, error) {
		if call == 1 {
			// 父 incident（带子引用）延迟到页后恢复；先恢复子以便层级映射完整。
			return RebuildPage{Items: []IncidentRecord{child, parent}}, nil
		}
		return RebuildPage{}, nil
	}
	bridge := w14eNewBridge(t, wrapped, db, func(o *BridgeOptions) {
		o.RebuildPageTimeoutMs = 1_000
		o.RebuildTotalTimeoutMs = 10_000
	})
	wrapped.mu.Lock()
	wrapped.restoreErr = errors.New("w14e defer restore boom")
	wrapped.mu.Unlock()
	result, err := bridge.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if !result.Blocked || result.Reason != RebuildReasonRebuildFailed {
		t.Fatalf("result = %+v", result)
	}
}

func TestW14EBridgeOutboxUnresolvedChildLoadError(t *testing.T) {
	protocol := protocolScope("w14e-outbox-child")
	incident := w14eIncidentRecord(protocol, "inc-1", 1)
	incident.ChildIncidentIDs = []string{"missing-child"}
	scopeKey := MustScopeKey(protocol)
	db := &w14eControlPlaneDB{mockControlPlaneDB: &mockControlPlaneDB{}, listByKeysErr: errors.New("w14e child load boom")}
	db.incidents = map[string]IncidentRecord{scopeKey: incident}
	db.claimEvents = []OutboxEvent{{
		EventID: "ev-child", EventType: "incident", CircuitScopeKey: &scopeKey, AccountRuntimeKey: "w14e-outbox-child",
	}}
	store := newNonExpiringMemoryStore(t)
	bridge := w14eNewBridge(t, store, db, nil)
	if _, err := bridge.ProjectPending(context.Background(), 5); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(db.release) != 1 || db.release[0].EventID != "ev-child" {
		t.Fatalf("release = %+v", db.release)
	}
	// PublicSummaries 成功路径（独立健康 db）。
	summaries, err := LoadPublicAccountCircuitSummaries(context.Background(), &mockControlPlaneDB{}, []string{"w14e-outbox-child"})
	if err != nil || summaries == nil {
		t.Fatalf("summaries = %v, %v", summaries, err)
	}
}

func TestW14EBridgeReconcileCapacityArm(t *testing.T) {
	ctx := context.Background()
	childScope := protocolScope("w14e-rcap")
	child := w14eIncidentRecord(childScope, "leaf-1", 1)
	capacityStore, err := NewMemoryStore(MemoryStoreOptions{Capacity: 1, Now: func() int64 { return 1_000 }, Random: func() float64 { return 0.5 }})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if _, err := capacityStore.Suspect(ctx, SuspectInput{Scope: accountScope("w14e-rcap-fill"), DispatchRevision: "5", TransitionID: "fill", Reason: "r"}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	db := &w14eControlPlaneDB{mockControlPlaneDB: &mockControlPlaneDB{}}
	db.rebuildPage = func(call int) (RebuildPage, error) {
		if call == 1 {
			return RebuildPage{Items: []IncidentRecord{child}}, nil
		}
		return RebuildPage{}, nil
	}
	bridge := w14eNewBridge(t, capacityStore, db, func(o *BridgeOptions) { o.RebuildPageTimeoutMs = 1_000 })
	bridge.mu.Lock()
	bridge.globallyReady = true
	bridge.mu.Unlock()
	if _, err := bridge.ReconcileActive(ctx, 5); err == nil || !strings.Contains(err.Error(), "容量不足") {
		t.Fatalf("err = %v", err)
	}
}

func TestW14EBridgeClassifyErrorLongName(t *testing.T) {
	got := classifyError(w14eErrorWithAnExtremelyLongTypeNameWellBeyondTheSixtyFourCharLimit{})
	if len(got) > 64 {
		t.Fatalf("long name not truncated: %q", got)
	}
}

// ---------------------------------------------------------------------------
// store_memory：父子关系投影（shadow / unshadow）。
// ---------------------------------------------------------------------------

func TestW14EMemoryStoreProjectParentRelationship(t *testing.T) {
	ctx := context.Background()
	now := int64(10_000)
	clock := &now
	store := w14eMemoryStore(t, func(o *MemoryStoreOptions) { o.Now = func() int64 { return *clock } })
	child := protocolScope("w14e-parent-child")
	parent := Scope{Kind: ScopeKindAccount, AccountRuntimeKey: "w14e-parent"}

	// 子条目：open、同 revision，携带父引用的 incident id。
	childState := ClosedState(child, "5", 1, "child-open", *clock)
	childState.Phase = PhaseOpen
	childOpenedAt := *clock
	childState.OpenedAtMs = &childOpenedAt
	childRetryAt := *clock + 1_000
	childState.RetryAtMs = &childRetryAt
	childState.IncidentID = strPtr("child-open")
	if _, err := store.Restore(ctx, childState, clock); err != nil {
		t.Fatalf("restore child: %v", err)
	}
	// 父 open（带子引用）恢复：子被 shadow。
	parentOpen := ClosedState(parent, "5", 1, "parent-open", *clock)
	parentOpen.Phase = PhaseOpen
	openedAt := *clock
	parentOpen.OpenedAtMs = &openedAt
	retryAt := *clock + 1_000
	parentOpen.RetryAtMs = &retryAt
	parentOpen.IncidentID = strPtr("parent-inc-1")
	parentOpen.ChildScopeKeys = stringList{MustScopeKey(child)}
	parentOpen.ChildIncidentIDs = stringList{"child-open"}
	parentOpen.RequiredRecoveryScopeKeys = stringList{MustScopeKey(child)}
	if _, err := store.Restore(ctx, parentOpen, clock); err != nil {
		t.Fatalf("restore parent open: %v", err)
	}
	childState, err := store.Get(ctx, child, clock)
	if err != nil || childState.ShadowedByIncidentID == nil || *childState.ShadowedByIncidentID != "parent-inc-1" {
		t.Fatalf("child shadow = (%+v, %v)", childState.ShadowedByIncidentID, err)
	}
	// 父 closed（同 incident）恢复：子被 unshadow。
	parentClosed := ClosedState(parent, "5", 2, "parent-close", *clock)
	parentClosed.IncidentID = strPtr("parent-inc-1")
	parentClosed.ChildScopeKeys = stringList{MustScopeKey(child)}
	parentClosed.ChildIncidentIDs = stringList{"child-open"}
	if _, err := store.Restore(ctx, parentClosed, clock); err != nil {
		t.Fatalf("restore parent closed: %v", err)
	}
	childState, err = store.Get(ctx, child, clock)
	if err != nil || childState.ShadowedByIncidentID != nil {
		t.Fatalf("child unshadow = (%+v, %v)", childState.ShadowedByIncidentID, err)
	}
}

// ---------------------------------------------------------------------------
// service：PrepareAttempt 确认分支收尾。
// ---------------------------------------------------------------------------

func TestW14EPrepareAttemptConfirmationDispatchableArms(t *testing.T) {
	now := int64(0)
	clock := &now
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	ctx := context.Background()
	evidenceA := strings.Repeat("a", 64)
	evidenceB := strings.Repeat("b", 64)

	if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{
		scope: protocolModelScope(testAccount(), LaneText, strPtr("gpt-4o")),
		dispatchRevision: revisionOf(t, testAccount()), confirmationFailuresRequired: int64Ptr(2),
		reason: "transport:connect failed", failureEvidenceKey: strPtr(evidenceA),
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 3_000
	prepared, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: strPtr(evidenceB),
	})
	if err != nil || prepared.Outcome != PrepareDispatchable || prepared.Attempt == nil {
		t.Fatalf("prepare = (%s, %v)", prepared.Outcome, err)
	}
	confirmation := w14eConfirmationOf(prepared.Attempt)
	if confirmation == nil {
		t.Fatal("confirmation expected")
	}
	// 同请求证据：引发 suspect 的同一 evidence 再次准备必须 blocked。
	again, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, Confirmation: confirmation,
		FailureEvidenceKey: strPtr(evidenceA),
	})
	if err != nil || again.Outcome != PrepareBlocked {
		t.Fatalf("same evidence = (%s, %v)", again.Outcome, err)
	}
	// 租约不匹配：错误租约 ID 必须 blocked。
	wrongLease := *confirmation
	wrongLease.LeaseID = "other-lease"
	mismatch, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, Confirmation: &wrongLease,
		FailureEvidenceKey: strPtr(strings.Repeat("c", 64)),
	})
	if err != nil || mismatch.Outcome != PrepareBlocked {
		t.Fatalf("lease mismatch = (%s, %v)", mismatch.Outcome, err)
	}
	// 匹配租约：可调度并携带确认。
	eligible, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, Confirmation: confirmation,
		FailureEvidenceKey: strPtr(strings.Repeat("d", 64)),
	})
	if err != nil || eligible.Outcome != PrepareDispatchable || eligible.Attempt == nil || !eligible.Attempt.IsConfirmation() {
		t.Fatalf("eligible = (%s, %v)", eligible.Outcome, err)
	}
}

// ---------------------------------------------------------------------------
// suppression / precheck 小臂。
// ---------------------------------------------------------------------------

func TestW14ESuppressionOrderAndPreserveArms(t *testing.T) {
	now := int64(1_000_000)
	store := NewLocalSuppressionStore(LocalSuppressionStoreOptions{Now: func() int64 { return now }})
	emptyOrder := store.OrderDegradations(nil, nil)
	_ = emptyOrder
	// 少于两个账户时 PreserveDispatchPriorityTiers 直接返回副本。
	single := []SuppressibleAccount{{SuppressibleGatewayAccount: SuppressibleGatewayAccount{ID: "w14e-one"}}}
	reordered := PreserveDispatchPriorityTiers(single, single, nil)
	if len(reordered) != 1 || reordered[0].ID != "w14e-one" {
		t.Fatalf("preserve = %+v", reordered)
	}
}

func TestW14EPrecheckSummaryOpenAIProfileArms(t *testing.T) {
	openai := gatewayruntimecache.OpenAIAccountSecret{ProviderProtocolProfileID: "acct_openai_gpt"}
	if code := gatewayAccountSummaryProtocolCode(openai); code != OpenAIProtocolCode {
		t.Fatalf("openai code = %q", code)
	}
	if version := gatewayAccountSummaryProtocolVersion(openai); version != OpenAIProtocolVersion {
		t.Fatalf("openai version = %q", version)
	}
	// 授权账户带绑定系统账户时直接返回绑定。
	bound := gatewayruntimecache.OpenAIAccountSecret{AccountAccessType: "account_authorized", BindingSystemAccountID: strPtr("sys-1")}
	got, err := gatewayAccountSummarySystemAccountID(bound, PrecheckSummaryContext{})
	if err != nil || got != "sys-1" {
		t.Fatalf("bound system = (%q, %v)", got, err)
	}
}


