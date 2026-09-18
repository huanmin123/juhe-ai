package gatewaycircuit

// w14e 覆盖率补强：bridge 与 service 的错误臂、重试嵌套与映射分支。
// DB 行为使用包内 mock 注入；Store 错误通过包装 Store 注入（Mock 优先）。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// w14eConfirmationOf 读取 attempt 持有的确认租约快照（仅测试使用）。
func w14eConfirmationOf(attempt *Attempt) *Confirmation {
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	if attempt.confirmation == nil {
		return nil
	}
	cloned := *attempt.confirmation
	return &cloned
}

// ---------------------------------------------------------------------------
// 可注入错误的 Store 包装。
// ---------------------------------------------------------------------------

type w14eStoreErrors struct {
	Store
	mu               sync.Mutex
	restoreErr       error
	restoreScopeKind string
	getErr           error
	suspectErr       error
}

func (w *w14eStoreErrors) Restore(ctx context.Context, state State, nowMs *int64) (MutationResult, error) {
	w.mu.Lock()
	restoreErr := w.restoreErr
	scopeKind := w.restoreScopeKind
	w.mu.Unlock()
	if restoreErr != nil && (scopeKind == "" || scopeKind == state.Scope.Kind) {
		return MutationResult{}, restoreErr
	}
	return w.Store.Restore(ctx, state, nowMs)
}

func (w *w14eStoreErrors) Get(ctx context.Context, scope Scope, nowMs *int64) (State, error) {
	w.mu.Lock()
	getErr := w.getErr
	w.mu.Unlock()
	if getErr != nil {
		return State{}, getErr
	}
	return w.Store.Get(ctx, scope, nowMs)
}

func (w *w14eStoreErrors) Suspect(ctx context.Context, input SuspectInput) (MutationResult, error) {
	w.mu.Lock()
	suspectErr := w.suspectErr
	w.mu.Unlock()
	if suspectErr != nil {
		return MutationResult{}, suspectErr
	}
	return w.Store.Suspect(ctx, input)
}

// w14eControlPlaneDB 在既有 mockControlPlaneDB 之上补充可注入行为。
type w14eControlPlaneDB struct {
	*mockControlPlaneDB

	listByKeysErr    error
	getByScopeErr    error
	rebuildPage      func(call int) (RebuildPage, error)
	claimEvents      []OutboxEvent
	claimErr         error
	blockUntilCtx    bool
	pageTimeoutSleep time.Duration
}

func (m *w14eControlPlaneDB) ListIncidentsByRuntimeKeys(ctx context.Context, input ListIncidentsByRuntimeKeysInput) ([]IncidentRecord, error) {
	if m.pageTimeoutSleep > 0 {
		select {
		case <-time.After(m.pageTimeoutSleep):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.listByKeysErr != nil {
		return nil, m.listByKeysErr
	}
	return m.mockControlPlaneDB.ListIncidentsByRuntimeKeys(ctx, input)
}

func (m *w14eControlPlaneDB) GetIncidentByScopeKey(ctx context.Context, scopeKey string) (*IncidentRecord, error) {
	if m.getByScopeErr != nil {
		return nil, m.getByScopeErr
	}
	return m.mockControlPlaneDB.GetIncidentByScopeKey(ctx, scopeKey)
}

func (m *w14eControlPlaneDB) ListIncidentsForRebuild(ctx context.Context, input RebuildPageInput) (RebuildPage, error) {
	if m.rebuildPage != nil {
		m.mu.Lock()
		m.rebuildCalls++
		call := m.rebuildCalls
		m.mu.Unlock()
		return m.rebuildPage(call)
	}
	return m.mockControlPlaneDB.ListIncidentsForRebuild(ctx, input)
}

func (m *w14eControlPlaneDB) ClaimOutbox(ctx context.Context, input ClaimOutboxInput) ([]OutboxEvent, error) {
	if m.claimErr != nil {
		return nil, m.claimErr
	}
	return m.claimEvents, nil
}

func w14eWaitWorkersDone(t *testing.T, bridge *Bridge) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		bridge.mu.Lock()
		idle := len(bridge.workers) == 0
		bridge.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("bridge workers never stopped")
}

func w14eNewBridge(t *testing.T, store Store, db ControlPlaneDB, mutate func(*BridgeOptions)) *Bridge {
	t.Helper()
	options := BridgeOptions{
		Store: store,
		DB:    db,
		Now:   func() int64 { return 1000 },
		Sleep: func(context.Context, time.Duration) error { return nil },
		NewTimer: func(delay time.Duration) (<-chan struct{}, func()) {
			done := make(chan struct{})
			return done, func() {}
		},
	}
	if mutate != nil {
		mutate(&options)
	}
	bridge, err := NewBridge(options)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	t.Cleanup(bridge.Close)
	return bridge
}

func w14eIncidentRecord(scope Scope, incidentID string, generation int64) IncidentRecord {
	record := IncidentRecord{
		IncidentID:                   incidentID,
		AccountID:                    "acc",
		AccountRuntimeKey:            scope.AccountRuntimeKey,
		CircuitScopeKey:              MustScopeKey(scope),
		ScopeKind:                    incidentScopeKind(scope),
		State:                        IncidentStateSuspect,
		Generation:                   generation,
		DispatchRevision:             7,
		TransitionID:                 incidentID,
		UpdatedAtMs:                  1_000,
		LedgerRevision:               3,
		ConfirmationFailuresRequired: 1,
	}
	if scope.Kind == ScopeKindKey {
		fingerprint := scope.KeyFingerprint
		record.KeyFingerprint = &fingerprint
	}
	if scope.Kind == ScopeKindProtocolModel {
		profile := scope.ProtocolProfile
		lane := scope.RequestLane
		bucket := scope.ModelBucket
		record.ProtocolCode = &profile
		record.RequestLane = &lane
		record.ModelFamily = &bucket
	}
	return record
}

func incidentScopeKind(scope Scope) string {
	switch scope.Kind {
	case ScopeKindKey:
		return IncidentScopeKindKey
	case ScopeKindProtocolModel:
		return IncidentScopeKindProtocolModel
	default:
		return IncidentScopeKindAccount
	}
}

// ---------------------------------------------------------------------------
// bridge：构造缺省 wiring、rebuild、账户加载、outbox 投影、persistWithRetry。
// ---------------------------------------------------------------------------

func TestW14EBridgeDefaultWiringAndRetryTimers(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	// 不注入 Sleep / NewTimer：覆盖真实缺省 wiring（真实 1ms 重试等待）。
	bridge, err := NewBridge(BridgeOptions{
		Store: store, DB: db,
		Now:                func() int64 { return 1_000 },
		RetryDelayMs:       1,
		MaxPersistAttempts: 2,
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	t.Cleanup(bridge.Close)

	state := ClosedState(protocolScope("w14e-wire"), "7", 1, "t1", 900)
	state.Phase = PhaseSuspect
	bridge.Observe(protocolScope("w14e-wire"), state)
	waitForBridgeIdle(t, bridge)
	if len(db.casCalls) == 0 {
		t.Fatal("default wiring must persist observations")
	}
}

func TestW14EBridgeRebuildMaxPagesExceeded(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &w14eControlPlaneDB{mockControlPlaneDB: &mockControlPlaneDB{}}
	// 每页都返回下一页游标：超过 maxPages 必须失败。
	db.rebuildPage = func(call int) (RebuildPage, error) {
		return RebuildPage{
			Items:      []IncidentRecord{},
			NextCursor: &RebuildCursor{UpdatedAtMs: int64(call), CircuitScopeKey: fmt.Sprintf("k%d", call)},
		}, nil
	}
	bridge := w14eNewBridge(t, store, db, func(o *BridgeOptions) {
		o.RebuildMaxPages = 2
		o.RebuildPageSize = 10
		o.RebuildPageTimeoutMs = 1_000
		o.RebuildTotalTimeoutMs = 10_000
	})
	result, err := bridge.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if !result.Blocked || result.Reason != RebuildReasonInvalidCursor {
		t.Fatalf("result = %+v", result)
	}
}

func TestW14EBridgeEnsureAccountReadySingleFlightAndBackoff(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &w14eControlPlaneDB{mockControlPlaneDB: &mockControlPlaneDB{}}
	release := make(chan struct{})
	// 第一次加载阻塞在 DB 调用上：第二个调用者必须等待同一个 load。
	db.pageTimeoutSleep = 0
	bridge := newTestBridge(t, store, db.mockControlPlaneDB, func(o *BridgeOptions) {
		o.LoadAccountIncidents = func(ctx context.Context, key string) ([]IncidentRecord, error) {
			<-release
			return nil, nil
		}
	})
	var wg sync.WaitGroup
	results := make([]bool, 2)
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ready, err := bridge.EnsureAccountReady(context.Background(), "w14e-single")
			if err != nil {
				t.Errorf("ensure: %v", err)
			}
			results[index] = ready
		}(index)
	}
	time.Sleep(50 * time.Millisecond)
	bridge.mu.Lock()
	loads := len(bridge.accountLoads)
	bridge.mu.Unlock()
	if loads != 1 {
		t.Fatalf("loads in flight = %d", loads)
	}
	close(release)
	wg.Wait()
	for _, ready := range results {
		if !ready {
			t.Fatal("load must become ready")
		}
	}

	// 连续失败：backoff 倍增（consecutive > 1 的循环分支）。
	failures := 0
	clock := int64(1_000)
	bridge2 := newTestBridge(t, store, db.mockControlPlaneDB, func(o *BridgeOptions) {
		o.LoadAccountIncidents = func(ctx context.Context, key string) ([]IncidentRecord, error) {
			failures++
			return nil, errors.New("w14e load boom")
		}
		o.Now = func() int64 { clock += 65_000; return clock }
	})
	for i := 0; i < 3; i++ {
		if _, err := bridge2.EnsureAccountReady(context.Background(), "w14e-backoff"); err != nil {
			t.Fatalf("ensure %d: %v", i, err)
		}
	}
	bridge2.mu.Lock()
	failure := bridge2.readinessFailures["w14e-backoff"]
	bridge2.mu.Unlock()
	if failure.consecutiveFailures != 3 {
		t.Fatalf("failure state = %+v", failure)
	}
	// backoff 期间直接返回 false。
	if ready, err := bridge2.EnsureAccountReady(context.Background(), "w14e-backoff"); err != nil || ready {
		t.Fatalf("backoff gate = (%t, %v)", ready, err)
	}
}

func TestW14EBridgePerformAccountLoadArms(t *testing.T) {
	parentScope := Scope{Kind: ScopeKindAccount, AccountRuntimeKey: "w14e-load"}
	childScope := protocolScope("w14e-load")
	leaf := w14eIncidentRecord(childScope, "leaf-1", 1)
	parent := w14eIncidentRecord(parentScope, "parent-1", 1)
	parent.ChildIncidentIDs = []string{"leaf-1"}

	t.Run("persistence failure flag", func(t *testing.T) {
		store := newNonExpiringMemoryStore(t)
		db := &mockControlPlaneDB{}
		bridge := newTestBridge(t, store, db, nil)
		bridge.mu.Lock()
		bridge.persistenceFailures[MustScopeKey(parentScope)] = parentScope.AccountRuntimeKey
		bridge.mu.Unlock()
		ready, err := bridge.EnsureAccountReady(context.Background(), "w14e-load")
		if err != nil || ready {
			t.Fatalf("persistence failure gate = (%t, %v)", ready, err)
		}
	})

	t.Run("restore error surfaces via readiness failure", func(t *testing.T) {
		wrapped := &w14eStoreErrors{Store: newNonExpiringMemoryStore(t)}
		db := &mockControlPlaneDB{}
		db.incidents = map[string]IncidentRecord{"w14e-load": leaf}
		failures := make(chan ReadinessFailure, 1)
		bridge := w14eNewBridge(t, wrapped, db, func(o *BridgeOptions) {
			o.OnReadinessFailure = func(failure ReadinessFailure) { failures <- failure }
		})
		wrapped.mu.Lock()
		wrapped.restoreErr = errors.New("w14e restore boom")
		wrapped.mu.Unlock()
		if _, err := bridge.EnsureAccountReady(context.Background(), "w14e-load"); err != nil {
			t.Fatalf("ensure must swallow the load error: %v", err)
		}
		select {
		case failure := <-failures:
			if failure.Reason != "account_load_failed" {
				t.Fatalf("failure = %+v", failure)
			}
		case <-time.After(time.Second):
			t.Fatal("readiness failure must be reported")
		}
	})

	t.Run("capacity exhausted surfaces", func(t *testing.T) {
		capacityStore, err := NewMemoryStore(MemoryStoreOptions{Capacity: 1, Now: func() int64 { return 1_000 }, Random: func() float64 { return 0.5 }})
		if err != nil {
			t.Fatalf("store: %v", err)
		}
		// 先占满容量：open 条目占住唯一槽位。
		if _, err := capacityStore.Suspect(context.Background(), SuspectInput{Scope: accountScope("w14e-cap-fill"), DispatchRevision: "5", TransitionID: "fill", Reason: "r"}); err != nil {
			t.Fatalf("suspect: %v", err)
		}
		db := &mockControlPlaneDB{}
		db.incidents = map[string]IncidentRecord{"w14e-load": leaf}
		failures := make(chan ReadinessFailure, 1)
		bridge := newTestBridge(t, capacityStore, db, func(o *BridgeOptions) {
			o.OnReadinessFailure = func(failure ReadinessFailure) { failures <- failure }
		})
		if _, err := bridge.EnsureAccountReady(context.Background(), "w14e-load"); err != nil {
			t.Fatalf("ensure must swallow the load error: %v", err)
		}
		select {
		case failure := <-failures:
			if failure.Reason != "account_load_capacity_exhausted" {
				t.Fatalf("failure = %+v", failure)
			}
		case <-time.After(time.Second):
			t.Fatal("readiness failure must be reported")
		}
	})

	t.Run("leaf then parent ordering restores both", func(t *testing.T) {
		store := newNonExpiringMemoryStore(t)
		db := &mockControlPlaneDB{}
		db.incidents = map[string]IncidentRecord{"w14e-load": leaf, "w14e-load-parent": parent}
		bridge := newTestBridge(t, store, db, nil)
		ready, err := bridge.EnsureAccountReady(context.Background(), "w14e-load")
		if err != nil || !ready {
			t.Fatalf("ready = (%t, %v)", ready, err)
		}
		bridge.mu.Lock()
		ledger := bridge.ledgerRevisions[MustScopeKey(childScope)]
		bridge.mu.Unlock()
		if ledger != 3 {
			t.Fatalf("ledger revision = %d", ledger)
		}
	})
}

func TestW14EBridgeProjectOutboxEventArms(t *testing.T) {
	protocol := protocolScope("w14e-outbox")
	incident := w14eIncidentRecord(protocol, "inc-1", 1)
	scopeKey := MustScopeKey(protocol)

	t.Run("dispatch revision event", func(t *testing.T) {
		store := newNonExpiringMemoryStore(t)
		db := &w14eControlPlaneDB{mockControlPlaneDB: &mockControlPlaneDB{}, claimEvents: []OutboxEvent{{
			EventID: "ev-1", EventType: OutboxEventTypeDispatchRevisionChanged,
			AccountRuntimeKey: "w14e-outbox", DispatchRevision: 9, TransitionID: "t9",
		}}}
		bridge := w14eNewBridge(t, store, db, nil)
		acknowledged, err := bridge.ProjectPending(context.Background(), 5)
		if err != nil || acknowledged != 1 {
			t.Fatalf("project = (%d, %v)", acknowledged, err)
		}
		if len(db.acked) != 1 {
			t.Fatalf("acked = %d", len(db.acked))
		}
	})

	t.Run("missing scope key", func(t *testing.T) {
		store := newNonExpiringMemoryStore(t)
		db := &w14eControlPlaneDB{mockControlPlaneDB: &mockControlPlaneDB{}, claimEvents: []OutboxEvent{{
			EventID: "ev-2", EventType: "incident", AccountRuntimeKey: "w14e-outbox",
		}}}
		bridge := w14eNewBridge(t, store, db, nil)
		// 投影失败不传播：事件被释放回放，ProjectPending 正常返回。
		if _, err := bridge.ProjectPending(context.Background(), 5); err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(db.release) != 1 || db.release[0].EventID != "ev-2" {
			t.Fatalf("release = %+v", db.release)
		}
	})

	t.Run("get incident error", func(t *testing.T) {
		store := newNonExpiringMemoryStore(t)
		db := &w14eControlPlaneDB{mockControlPlaneDB: &mockControlPlaneDB{}, getByScopeErr: errors.New("w14e ledger boom"), claimEvents: []OutboxEvent{{
			EventID: "ev-3", EventType: "incident", CircuitScopeKey: &scopeKey, AccountRuntimeKey: "w14e-outbox",
		}}}
		bridge := w14eNewBridge(t, store, db, nil)
		if _, err := bridge.ProjectPending(context.Background(), 5); err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(db.release) != 1 || db.release[0].EventID != "ev-3" {
			t.Fatalf("release = %+v", db.release)
		}
	})

	t.Run("incident missing in ledger", func(t *testing.T) {
		store := newNonExpiringMemoryStore(t)
		db := &w14eControlPlaneDB{mockControlPlaneDB: &mockControlPlaneDB{}, claimEvents: []OutboxEvent{{
			EventID: "ev-4", EventType: "incident", CircuitScopeKey: &scopeKey, AccountRuntimeKey: "w14e-outbox",
		}}}
		bridge := w14eNewBridge(t, store, db, nil)
		if _, err := bridge.ProjectPending(context.Background(), 5); err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(db.release) != 1 || db.release[0].EventID != "ev-4" {
			t.Fatalf("release = %+v", db.release)
		}
	})

	t.Run("restore error and capacity", func(t *testing.T) {
		// Restore 错误。
		wrapped := &w14eStoreErrors{Store: newNonExpiringMemoryStore(t)}
		db := &w14eControlPlaneDB{mockControlPlaneDB: &mockControlPlaneDB{}, claimEvents: []OutboxEvent{{
			EventID: "ev-5", EventType: "incident", CircuitScopeKey: &scopeKey, AccountRuntimeKey: "w14e-outbox",
		}}}
		db.incidents = map[string]IncidentRecord{scopeKey: incident}
		bridge := w14eNewBridge(t, wrapped, db, nil)
		wrapped.mu.Lock()
		wrapped.restoreErr = errors.New("w14e project restore boom")
		wrapped.mu.Unlock()
		if _, err := bridge.ProjectPending(context.Background(), 5); err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(db.release) != 1 || db.release[0].EventID != "ev-5" {
			t.Fatalf("release = %+v", db.release)
		}

		// 容量耗尽。
		capacityStore, err := NewMemoryStore(MemoryStoreOptions{Capacity: 1, Now: func() int64 { return 1_000 }, Random: func() float64 { return 0.5 }})
		if err != nil {
			t.Fatalf("store: %v", err)
		}
		if _, err := capacityStore.Suspect(context.Background(), SuspectInput{Scope: accountScope("w14e-cap-fill"), DispatchRevision: "5", TransitionID: "fill", Reason: "r"}); err != nil {
			t.Fatalf("suspect: %v", err)
		}
		db2 := &w14eControlPlaneDB{mockControlPlaneDB: &mockControlPlaneDB{}, claimEvents: []OutboxEvent{{
			EventID: "ev-6", EventType: "incident", CircuitScopeKey: &scopeKey, AccountRuntimeKey: "w14e-outbox",
		}}}
		db2.incidents = map[string]IncidentRecord{scopeKey: incident}
		bridge2 := w14eNewBridge(t, capacityStore, db2, nil)
		if _, err := bridge2.ProjectPending(context.Background(), 5); err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(db2.release) != 1 || db2.release[0].EventID != "ev-6" {
			t.Fatalf("release = %+v", db2.release)
		}
	})
}

func TestW14EBridgePersistWithRetryArms(t *testing.T) {
	protocol := protocolScope("w14e-persist")

	t.Run("refresh error consumes attempts", func(t *testing.T) {
		wrapped := &w14eStoreErrors{Store: newNonExpiringMemoryStore(t)}
		db := &mockControlPlaneDB{}
		bridge := newTestBridge(t, wrapped, db, func(o *BridgeOptions) {
			o.MaxPersistAttempts = 2
			o.RetryDelayMs = 1
		})
		wrapped.mu.Lock()
		wrapped.getErr = errors.New("w14e refresh boom")
		wrapped.mu.Unlock()
		state := ClosedState(protocol, "7", 1, "t1", 900)
		state.Phase = PhaseSuspect
		bridge.Observe(protocol, state)
		w14eWaitWorkersDone(t, bridge)
		if len(db.casCalls) != 0 {
			t.Fatal("refresh failure must prevent CAS")
		}
		bridge.mu.Lock()
		failed := bridge.persistenceFailures[MustScopeKey(protocol)]
		bridge.mu.Unlock()
		if failed == "" {
			t.Fatal("persistence failure must be recorded")
		}
	})

	t.Run("non numeric dispatch revision falls back to cached", func(t *testing.T) {
		store := newNonExpiringMemoryStore(t)
		db := &mockControlPlaneDB{}
		bridge := newTestBridge(t, store, db, nil)
		bridge.mu.Lock()
		bridge.dispatchRevisions["w14e-persist"] = 42
		bridge.mu.Unlock()
		state := ClosedState(protocol, "not-a-number", 1, "t1", 900)
		state.Phase = PhaseSuspect
		bridge.Observe(protocol, state)
		waitForBridgeIdle(t, bridge)
		if len(db.casCalls) != 1 || db.casCalls[0].DispatchRevision != 42 {
			t.Fatalf("cas calls = %+v", db.casCalls)
		}
	})

	t.Run("conflict refresh error then exhausted", func(t *testing.T) {
		store := newNonExpiringMemoryStore(t)
		db := &mockControlPlaneDB{}
		attempts := 0
		bridge := newTestBridge(t, store, db, func(o *BridgeOptions) {
			o.MaxPersistAttempts = 1
			o.PersistIncident = func(ctx context.Context, input CompareAndSetIncidentInput) (CompareAndSetIncidentResult, error) {
				attempts++
				conflict := w14eIncidentRecord(protocol, "inc-1", 5)
				conflict.UpdatedAtMs = 2_000
				return CompareAndSetIncidentResult{Status: CASConflict, Incident: &conflict}, nil
			}
			o.MaxPersistAttempts = 1
		})
		state := ClosedState(protocol, "7", 1, "t1", 900)
		state.Phase = PhaseSuspect
		bridge.Observe(protocol, state)
		w14eWaitWorkersDone(t, bridge)
		if attempts != 1 {
			t.Fatalf("attempts = %d", attempts)
		}
	})
}

func TestW14EBridgeRefreshDesiredStateArms(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &mockControlPlaneDB{}
	bridge := newTestBridge(t, store, db, nil)
	ctx := context.Background()
	scope := protocolScope("w14e-refresh")
	observed := ClosedState(scope, "7", 1, "t1", 900)
	observed.Phase = PhaseSuspect

	// 运行态缺 dispatchRevision：观察态获胜。
	refreshed, err := bridge.refreshDesiredState(ctx, scope, observed)
	if err != nil || refreshed == nil || refreshed.Generation != 1 {
		t.Fatalf("fresh scope refresh = %+v, %v", refreshed, err)
	}
	// 运行态代数更小：观察态获胜。
	older := observed
	older.Generation = 0
	if _, err := store.Restore(ctx, older, int64Ptr(1_000)); err != nil {
		t.Fatalf("restore older: %v", err)
	}
	refreshed, err = bridge.refreshDesiredState(ctx, scope, observed)
	if err != nil || refreshed == nil || refreshed.Generation != 1 {
		t.Fatalf("older generation refresh = %+v, %v", refreshed, err)
	}
	// 运行态 revision 更大：放弃观察态。
	newer := observed
	newer.DispatchRevision = "9"
	newer.Generation = 5
	if _, err := store.Restore(ctx, newer, int64Ptr(1_000)); err != nil {
		t.Fatalf("restore newer: %v", err)
	}
	refreshed, err = bridge.refreshDesiredState(ctx, scope, observed)
	if err != nil || refreshed != nil {
		t.Fatalf("newer runtime must discard observed: %+v, %v", refreshed, err)
	}
	// 运行态 revision 更小但可解析：观察态保留（独立作用域避免互相污染）。
	lowerScope := protocolScope("w14e-refresh-lower")
	lowerObserved := ClosedState(lowerScope, "7", 1, "t1", 900)
	lowerObserved.Phase = PhaseSuspect
	lower := lowerObserved
	lower.DispatchRevision = "3"
	lower.Generation = 9
	if _, err := store.Restore(ctx, lower, int64Ptr(1_000)); err != nil {
		t.Fatalf("restore lower: %v", err)
	}
	refreshed, err = bridge.refreshDesiredState(ctx, lowerScope, lowerObserved)
	if err != nil || refreshed == nil || refreshed.Generation != 1 {
		t.Fatalf("lower revision runtime must keep observed: %+v, %v", refreshed, err)
	}
	// 同 revision 同 generation、更新时间更新：运行态获胜。
	sameScope := protocolScope("w14e-refresh-same")
	sameObserved := ClosedState(sameScope, "7", 1, "t1", 900)
	sameObserved.Phase = PhaseSuspect
	sameRev := sameObserved
	sameRev.UpdatedAtMs = 5_000
	if _, err := store.Restore(ctx, sameRev, int64Ptr(1_000)); err != nil {
		t.Fatalf("restore sameRev: %v", err)
	}
	refreshed, err = bridge.refreshDesiredState(ctx, sameScope, sameObserved)
	if err != nil || refreshed == nil || refreshed.UpdatedAtMs != 5_000 {
		t.Fatalf("newer updatedAt runtime must win: %+v, %v", refreshed, err)
	}
	// 运行态 generation 相同、transition 不同：运行态获胜。
	transScope := protocolScope("w14e-refresh-trans")
	transObserved := ClosedState(transScope, "7", 1, "t1", 900)
	transObserved.Phase = PhaseSuspect
	otherTransition := transObserved
	otherTransition.TransitionID = "other-transition"
	if _, err := store.Restore(ctx, otherTransition, int64Ptr(1_000)); err != nil {
		t.Fatalf("restore otherTransition: %v", err)
	}
	refreshed, err = bridge.refreshDesiredState(ctx, transScope, transObserved)
	if err != nil || refreshed == nil || refreshed.TransitionID != "other-transition" {
		t.Fatalf("other transition runtime must win: %+v, %v", refreshed, err)
	}
}

func TestW14EBridgePureHelpers(t *testing.T) {
	// rebuildError.Error。
	err := &rebuildError{reason: RebuildReasonRebuildTimeout}
	if err.Error() != RebuildReasonRebuildTimeout {
		t.Fatalf("error = %s", err.Error())
	}
	// msToDuration 负值钳制。
	if got := msToDuration(-5); got != 0 {
		t.Fatalf("negative duration = %v", got)
	}
	// classifyError 的截断与空名。
	long := &w14eVeryLongNamedError{}
	if got := classifyError(long); len(got) > 64 {
		t.Fatalf("long name = %s", got)
	}
	if got := classifyError(nil); got != "projector_error" {
		t.Fatalf("nil classify = %s", got)
	}
	// incidentStatePriority 未知状态。
	if got := incidentStatePriority("bogus"); got != 0 {
		t.Fatalf("priority = %d", got)
	}
	// sameStringSet 重复检测。
	if sameStringSet([]string{"a", "a"}, []string{"a", "b"}) {
		t.Fatal("duplicates must not match")
	}
	if sameStringSet([]string{"a"}, []string{"b"}) {
		t.Fatal("different sets must not match")
	}
	// boundedRuntimeKeys 的 100 上限。
	many := make([]string, 0, 150)
	for index := 0; index < 150; index++ {
		many = append(many, fmt.Sprintf("key-%d", index))
	}
	if got := boundedRuntimeKeys(many); len(got) != 100 {
		t.Fatalf("bounded keys = %d", len(got))
	}
	// PublicSummaryOf 平局按最早更新时间。
	summary := PublicSummaryOf([]IncidentRecord{
		{State: IncidentStateSuspect, UpdatedAtMs: 2_000},
		{State: IncidentStateSuspect, UpdatedAtMs: 1_000},
	})
	if summary.Since != msToRFC3339(1_000) {
		t.Fatalf("summary = %+v", summary)
	}
	// LoadPublicAccountCircuitSummaries 的 DB 错误。
	if _, err := LoadPublicAccountCircuitSummaries(context.Background(), &w14eControlPlaneDB{mockControlPlaneDB: &mockControlPlaneDB{}, listByKeysErr: errors.New("w14e list boom")}, []string{"a"}); err == nil {
		t.Fatal("db error must surface")
	}
}

type w14eVeryLongNamedError struct{}

func (e *w14eVeryLongNamedError) Error() string { return "boom" }

func TestW14EIncidentToRuntimeStateMapping(t *testing.T) {
	protocol := protocolScope("w14e-map")
	childScope := protocolScope("w14e-map-child")
	leasePurpose := LeaseKindHalfOpen
	protocolCode := protocol.ProtocolProfile
	requestLane := protocol.RequestLane
	modelFamily := protocol.ModelBucket
	openUntil := int64(5_000)
	nextTransition := int64(6_000)
	failureClass := "transport:timeout"
	leaseID := "lease-1"
	leaseUntil := int64(7_000)
	parentID := "parent-1"
	incident := IncidentRecord{
		IncidentID:         "inc-map",
		AccountID:          "acc",
		AccountRuntimeKey:  protocol.AccountRuntimeKey,
		CircuitScopeKey:    MustScopeKey(protocol),
		ScopeKind:          IncidentScopeKindProtocolModel,
		ProtocolCode:       &protocolCode,
		RequestLane:        &requestLane,
		ModelFamily:        &modelFamily,
		State:              IncidentStateHalfOpen,
		Generation:         2,
		DispatchRevision:   7,
		TransitionID:       "inc-map",
		UpdatedAtMs:        1_000,
		LeasePurpose:       &leasePurpose,
		LeaseID:            &leaseID,
		LeaseUntilMs:       &leaseUntil,
		OpenUntilMs:        &openUntil,
		NextTransitionAtMs: &nextTransition,
		LastFailureClass:   &failureClass,
		ParentIncidentID:   &parentID,
		ChildIncidentIDs:   []string{"child-1"},
	}
	hierarchy := map[string]string{
		incidentHierarchyKey(protocol.AccountRuntimeKey, "child-1"): MustScopeKey(childScope),
	}
	state := IncidentToRuntimeState(incident, hierarchy)
	if state.ShadowedByIncidentID == nil || *state.ShadowedByIncidentID != "parent-1" {
		t.Fatalf("shadow = %v", state.ShadowedByIncidentID)
	}
	if len(state.ChildScopeKeys) != 1 || state.ChildScopeKeys[0] != MustScopeKey(childScope) {
		t.Fatalf("child scope keys = %v", state.ChildScopeKeys)
	}
	if state.OpenedAtMs == nil || *state.OpenedAtMs != 1_000 {
		t.Fatalf("openedAt = %v", state.OpenedAtMs)
	}
	if state.RetryAtMs == nil || *state.RetryAtMs != 6_000 {
		t.Fatalf("retryAt = %v", state.RetryAtMs)
	}
	if state.FailureReason == nil || *state.FailureReason != failureClass {
		t.Fatalf("failureReason = %v", state.FailureReason)
	}
	if state.Lease == nil || state.Lease.LeaseID != "lease-1" || state.Lease.Kind != LeaseKindHalfOpen {
		t.Fatalf("lease = %+v", state.Lease)
	}
	if state.HalfOpenOrigin == nil || *state.HalfOpenOrigin != PhaseOpen {
		t.Fatalf("halfOpenOrigin = %v", state.HalfOpenOrigin)
	}
	// key 作用域映射。
	keyScope := Scope{Kind: ScopeKindKey, AccountRuntimeKey: "w14e-map", KeyFingerprint: "fp"}
	keyRecord := w14eIncidentRecord(keyScope, "inc-key", 1)
	keyState := IncidentToRuntimeState(keyRecord, map[string]string{})
	if keyState.Scope.KeyFingerprint != "fp" {
		t.Fatalf("key scope = %+v", keyState.Scope)
	}
	// 恢复相位映射。
	recovering := leasePurpose
	recovering = LeaseKindRecovery
	incident.LeasePurpose = &recovering
	incident.State = IncidentStateHalfOpen
	recoveringState := IncidentToRuntimeState(incident, hierarchy)
	if recoveringState.HalfOpenOrigin == nil || *recoveringState.HalfOpenOrigin != PhaseRecovering {
		t.Fatalf("recovering origin = %v", recoveringState.HalfOpenOrigin)
	}
}

func TestW14EBridgeWithinTimeoutListDeadline(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	db := &w14eControlPlaneDB{mockControlPlaneDB: &mockControlPlaneDB{}, pageTimeoutSleep: 50 * time.Millisecond}
	failures := make(chan ReadinessFailure, 1)
	bridge := w14eNewBridge(t, store, db, func(o *BridgeOptions) {
		o.RebuildPageTimeoutMs = 1
		o.OnReadinessFailure = func(failure ReadinessFailure) { failures <- failure }
	})
	if _, err := bridge.EnsureAccountReady(context.Background(), "w14e-slow"); err != nil {
		t.Fatalf("ensure must swallow the timeout: %v", err)
	}
	select {
	case failure := <-failures:
		if failure.Reason != "account_load_timeout" {
			t.Fatalf("failure = %+v", failure)
		}
	case <-time.After(time.Second):
		t.Fatal("readiness failure must be reported")
	}
}

// ---------------------------------------------------------------------------
// service：Attempt 生命周期与 PrepareAttempt 确认分支。
// ---------------------------------------------------------------------------

func TestW14EPrepareAttemptValidationArms(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{})
	// 授权账户缺绑定上下文：运行态键构造失败。
	brokenAccount := gatewayruntimecache.OpenAIAccountSecret{ID: "w14e-broken", AccountAccessType: "account_authorized"}

	if _, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: brokenAccount, RequestLane: LaneText, ConfirmationLeaseDurationMs: 1_000,
	}); err == nil {
		t.Fatal("authorized account without binding must fail scope construction")
	}
	if _, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, ConfirmationLeaseDurationMs: 0,
	}); err == nil {
		t.Fatal("zero lease duration must fail")
	}
	badCFR := int64(99)
	if _, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, ConfirmationLeaseDurationMs: 1_000,
		ConfirmationFailuresRequired: &badCFR,
	}); err == nil {
		t.Fatal("out of range confirmationFailuresRequired must fail")
	}
}

func TestW14EPrepareAttemptConfirmationBranches(t *testing.T) {
	now := int64(0)
	clock := &now
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	ctx := context.Background()
	evidenceA := strings.Repeat("a", 64)
	evidenceB := strings.Repeat("b", 64)

	// 建立 suspect 并拿到确认租约。
	if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{
		scope:            protocolModelScope(testAccount(), LaneText, strPtr("gpt-4o")),
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
		t.Fatal("confirmation attempt expected")
	}

	// 确认作用域不匹配：blocked。
	mismatched := *confirmation
	mismatched.ScopeKey = "bogus"
	blocked, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, Confirmation: &mismatched,
		FailureEvidenceKey: strPtr(evidenceB),
	})
	if err != nil || blocked.Outcome != PrepareBlocked {
		t.Fatalf("mismatch = (%s, %v)", blocked.Outcome, err)
	}

	// ConfirmationEligible=false：先以 unknown 结算确认再 blocked。
	ineligible := false
	released, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, Confirmation: confirmation,
		ConfirmationEligible: &ineligible,
	})
	if err != nil || released.Outcome != PrepareBlocked {
		t.Fatalf("ineligible = (%s, %v)", released.Outcome, err)
	}
}

func TestW14EAttemptFramingAndKeyRotation(t *testing.T) {
	now := int64(0)
	clock := &now
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	ctx := context.Background()

	// 普通尝试的 framing 完成：走 clearAccountEscalationEvidence 分支。
	prepared, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil || prepared.Attempt == nil {
		t.Fatalf("prepare = %v", err)
	}
	if result, err := prepared.Attempt.ReportFramingComplete(ctx); err != nil || result != nil {
		t.Fatalf("plain framing = (%+v, %v)", result, err)
	}

	// 确认尝试：延迟的 key rotation 失败把 framing 完成映射为 closed。
	*clock = 0
	if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{
		scope:            protocolModelScope(testAccount(), LaneText, strPtr("gpt-4o")),
		dispatchRevision: revisionOf(t, testAccount()), confirmationFailuresRequired: int64Ptr(2),
		reason: "transport:connect failed",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 3_000
	confirmPrepared, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: strPtr(strings.Repeat("d", 64)),
	})
	if err != nil || confirmPrepared.Attempt == nil || !confirmPrepared.Attempt.IsConfirmation() {
		t.Fatalf("confirm prepare = (%s, %v)", confirmPrepared.Outcome, err)
	}
	if !confirmPrepared.Attempt.DeferConfirmationTransportFailureForKeyRotation() {
		t.Fatal("defer must be accepted while confirmation pending")
	}
	result, err := confirmPrepared.Attempt.ReportFramingComplete(ctx)
	if err != nil || result == nil || result.State.Phase != PhaseClosed {
		t.Fatalf("key rotation framing = (%+v, %v)", result, err)
	}
}

func TestW14EAttemptConcurrentSettlementSharesResult(t *testing.T) {
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
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: strPtr(strings.Repeat("e", 64)),
	})
	if err != nil || prepared.Attempt == nil {
		t.Fatalf("prepare = %v", err)
	}
	attempt := prepared.Attempt
	const racers = 4
	results := make([]*MutationResult, racers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index := 0; index < racers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			result, err := attempt.ReportUnknown(ctx)
			if err != nil {
				t.Errorf("report unknown: %v", err)
				return
			}
			results[index] = result
		}(index)
	}
	close(start)
	wg.Wait()
	for _, result := range results {
		if result == nil || result.Status != results[0].Status {
			t.Fatalf("settlement results diverged: %+v vs %+v", result, results[0])
		}
	}
}

func TestW14EServiceNotifyAndReleaseArms(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	var mu sync.Mutex
	callCount := 0
	service, _ := newTestService(t, store, ServiceOptions{
		OnMutation: func(context.Context, MutationEvent) error {
			mu.Lock()
			defer mu.Unlock()
			callCount++
			if callCount > 1 {
				return errors.New("w14e notify boom")
			}
			return nil
		},
	})
	// observeBlockedDispatch 对 closed 相位是 no-op。
	service.observeBlockedDispatch(ClosedState(protocolScope("w14e-notify"), "7", 1, "t", 0))
	// RelatedStates 通知失败必须向上传播。
	result := MutationResult{
		Status:        MutationApplied,
		State:         ClosedState(protocolScope("w14e-notify"), "7", 1, "t", 0),
		RelatedStates: stateList{ClosedState(protocolScope("w14e-notify"), "7", 1, "t", 0)},
	}
	if err := service.notifyMutation(context.Background(), OperationSuspect, protocolScope("w14e-notify"), result, PhaseClosed); err == nil {
		t.Fatal("related notification failure must propagate")
	}
	// releaseAcquiredConfirmation(nil) 是 no-op。
	service.releaseAcquiredConfirmation(context.Background(), nil)
}

func TestW14ECompleteRequestFramingWithoutEvidence(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{})
	result, err := service.CompleteRequestFramingAfterKeyRotation(context.Background(), keyRotationFramingInput{
		scope:      protocolModelScope(testAccount(), LaneText, strPtr("gpt-4o")),
		generation: 1, dispatchRevision: "7",
	})
	if err != nil || result.Status != MutationStateMismatch {
		t.Fatalf("result = (%s, %v)", result.Status, err)
	}
}
