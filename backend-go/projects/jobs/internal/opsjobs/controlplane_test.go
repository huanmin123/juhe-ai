package opsjobs

import (
	"context"
	"testing"
)

// ---- 内存 ledger / outbox / cursor mock ----

// fakeLedger 模拟真实分页 ledger：按 (updatedAtMS, circuitScopeKey) 稳定排序
// 与游标过滤，可选 forcePages 注入异常页序列。
type fakeLedger struct {
	items         []CircuitIncidentRecord
	forcePages    []RebuildPage
	byScopeKey    map[string]CircuitIncidentRecord
	byRuntimeKeys map[string][]CircuitIncidentRecord
	pageQueries   []RebuildPageQuery
}

func (f *fakeLedger) ListForRebuild(_ context.Context, query RebuildPageQuery) (RebuildPage, error) {
	f.pageQueries = append(f.pageQueries, query)
	if f.forcePages != nil {
		page := f.forcePages[0]
		f.forcePages = f.forcePages[1:]
		return page, nil
	}
	var filtered []CircuitIncidentRecord
	for _, item := range f.items {
		if query.AfterUpdatedAtMS != nil {
			if item.UpdatedAtMS < *query.AfterUpdatedAtMS {
				continue
			}
			if item.UpdatedAtMS == *query.AfterUpdatedAtMS && query.AfterCircuitScopeKey != nil && item.CircuitScopeKey <= *query.AfterCircuitScopeKey {
				continue
			}
		}
		filtered = append(filtered, item)
	}
	page := RebuildPage{}
	if len(filtered) > query.Limit {
		page.Items = filtered[:query.Limit]
		last := filtered[query.Limit-1]
		page.NextCursor = &IncidentCursor{UpdatedAtMS: last.UpdatedAtMS, CircuitScopeKey: last.CircuitScopeKey}
	} else {
		page.Items = filtered
	}
	return page, nil
}

func (f *fakeLedger) ListByRuntimeKeys(_ context.Context, keys []string, _ bool, _ int64) ([]CircuitIncidentRecord, error) {
	var result []CircuitIncidentRecord
	for _, key := range keys {
		result = append(result, f.byRuntimeKeys[key]...)
	}
	return result, nil
}

func (f *fakeLedger) GetByScopeKey(_ context.Context, scopeKey string) (*CircuitIncidentRecord, error) {
	if incident, found := f.byScopeKey[scopeKey]; found {
		return &incident, nil
	}
	return nil, nil
}

type ackedOutboxEvent struct {
	event   OutboxEvent
	ackedAt int64
}

type fakeOutbox struct {
	claims   [][]OutboxEvent
	acked    []ackedOutboxEvent
	released []OutboxEvent
}

func (f *fakeOutbox) Claim(_ context.Context, _ string, _ int64, _ int64, _ int) ([]OutboxEvent, error) {
	if len(f.claims) == 0 {
		return nil, nil
	}
	batch := f.claims[0]
	f.claims = f.claims[1:]
	return batch, nil
}

func (f *fakeOutbox) Ack(_ context.Context, event OutboxEvent, at int64) (bool, error) {
	f.acked = append(f.acked, ackedOutboxEvent{event: event, ackedAt: at})
	return true, nil
}

func (f *fakeOutbox) ReleaseForReplay(_ context.Context, event OutboxEvent, _ string, _ int64, _ int64) error {
	f.released = append(f.released, event)
	return nil
}

type fakeCursorStore struct {
	loaded *IncidentCursor
	saved  []IncidentCursor
}

func (f *fakeCursorStore) Load(context.Context) (*IncidentCursor, error) { return f.loaded, nil }

func (f *fakeCursorStore) Save(_ context.Context, cursor IncidentCursor) error {
	f.saved = append(f.saved, cursor)
	f.loaded = &cursor
	return nil
}

func newTestControlPlane(t *testing.T, store CircuitStore, ledger ControlPlaneLedger, outbox ControlPlaneOutbox, cursor ReconcileCursorStore) (*ControlPlaneMaintenance, ReconcileCursorStore) {
	t.Helper()
	cursorStore := cursor
	if cursorStore == nil {
		cursorStore = &fakeCursorStore{}
	}
	maintenance, err := NewControlPlaneMaintenance(store, ledger, outbox, ControlPlaneOptions{
		OwnerID:     "test-owner",
		NowMS:       func() int64 { return 1_000 },
		CursorStore: cursorStore,
	})
	if err != nil {
		t.Fatalf("构造控制面失败: %v", err)
	}
	return maintenance, cursorStore
}

func incidentRow(scopeKey string, scope CircuitScope, state CircuitIncidentState, updatedAtMS int64) CircuitIncidentRecord {
	return CircuitIncidentRecord{
		AccountID:                    scope.AccountRuntimeKey,
		AccountRuntimeKey:            scope.AccountRuntimeKey,
		IncidentID:                   scopeKey + "-incident",
		CircuitScopeKey:              scopeKey,
		ScopeKind:                    string(scope.Kind),
		KeyFingerprint:               scope.KeyFingerprint,
		ProtocolCode:                 scope.ProtocolProfile,
		RequestLane:                  scope.RequestLane,
		ModelFamily:                  scope.ModelBucket,
		State:                        state,
		Generation:                   2,
		DispatchRevision:             7,
		LedgerRevision:               1,
		TransitionID:                 "tx-" + scopeKey,
		ConfirmationFailuresRequired: 2,
		UpdatedAtMS:                  updatedAtMS,
	}
}

// ---- IncidentToRuntimeState 契约矩阵 ----

func TestIncidentToRuntimeStateMatrix(t *testing.T) {
	scope := accountScope("acc-1")
	scopeKey, _ := AccountCircuitScopeKey(scope)
	cases := []struct {
		name          string
		mutate        func(*CircuitIncidentRecord)
		wantPhase     CircuitPhase
		wantOrigin    string
		wantLeaseKind CircuitLeaseKind
	}{
		{"OPEN 原样", func(r *CircuitIncidentRecord) {}, CircuitPhaseOpen, "", ""},
		{"PERSISTING → OPEN", func(r *CircuitIncidentRecord) { r.State = CircuitIncidentPersisting }, CircuitPhaseOpen, "", ""},
		{"SHADOWED → OPEN", func(r *CircuitIncidentRecord) { r.State = CircuitIncidentShadowedByPersistent }, CircuitPhaseOpen, "", ""},
		{"HALF_OPEN+half_open", func(r *CircuitIncidentRecord) {
			r.State = CircuitIncidentHalfOpen
			r.LeasePurpose = "half_open"
			r.LeaseID = "lease-1"
			until := int64(5_000)
			r.LeaseUntilMS = &until
		}, CircuitPhaseHalfOpen, "OPEN", CircuitLeaseHalfOpen},
		{"HALF_OPEN+recovery", func(r *CircuitIncidentRecord) {
			r.State = CircuitIncidentHalfOpen
			r.LeasePurpose = "recovery"
			r.LeaseID = "lease-2"
			until := int64(5_000)
			r.LeaseUntilMS = &until
		}, CircuitPhaseHalfOpen, "RECOVERING", CircuitLeaseRecovery},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := incidentRow(scopeKey, scope, CircuitIncidentOpen, 100)
			tc.mutate(&record)
			state, err := IncidentToRuntimeState(record, map[string]string{})
			if err != nil {
				t.Fatalf("转换失败: %v", err)
			}
			if state.Phase != tc.wantPhase {
				t.Fatalf("phase = %s, want %s", state.Phase, tc.wantPhase)
			}
			if state.HalfOpenOrigin != tc.wantOrigin {
				t.Fatalf("halfOpenOrigin = %q, want %q", state.HalfOpenOrigin, tc.wantOrigin)
			}
			if tc.wantLeaseKind == "" {
				if state.Lease != nil {
					t.Fatalf("不应有租约: %+v", state.Lease)
				}
			} else if state.Lease == nil || state.Lease.Kind != tc.wantLeaseKind {
				t.Fatalf("lease = %+v, want kind %s", state.Lease, tc.wantLeaseKind)
			}
			if state.ScopeKey != scopeKey || state.DispatchRevision != "7" {
				t.Fatalf("基础字段不符: %+v", state)
			}
			if state.ConfirmationFailuresRequired == nil || *state.ConfirmationFailuresRequired != 2 {
				t.Fatal("confirmationFailuresRequired 应为 2")
			}
		})
	}

	t.Run("scopeKey 不一致报错", func(t *testing.T) {
		record := incidentRow(scopeKey, scope, CircuitIncidentOpen, 100)
		record.CircuitScopeKey = "tampered"
		if _, err := IncidentToRuntimeState(record, map[string]string{}); err == nil {
			t.Fatal("应报 scopeKey 不一致")
		}
	})
	t.Run("子 incident 解析 scope keys", func(t *testing.T) {
		childScope := protocolModelScope("acc-1", "prof", "text", "gpt")
		childKey, _ := AccountCircuitScopeKey(childScope)
		parent := incidentRow(scopeKey, scope, CircuitIncidentOpen, 100)
		parent.ChildIncidentIDs = []string{"child-incident"}
		hierarchy := map[string]string{incidentHierarchyKey("acc-1", "child-incident"): childKey}
		state, err := IncidentToRuntimeState(parent, hierarchy)
		if err != nil {
			t.Fatal(err)
		}
		if len(state.ChildScopeKeys) != 1 || state.ChildScopeKeys[0] != childKey {
			t.Fatalf("childScopeKeys = %v", state.ChildScopeKeys)
		}
		if len(state.RequiredRecoveryScopeKeys) != 1 {
			t.Fatalf("requiredRecoveryScopeKeys = %v", state.RequiredRecoveryScopeKeys)
		}
	})
}

// ---- projectPending：ack / release-for-replay ----

func TestControlPlaneProjectPendingAckAndReplay(t *testing.T) {
	nowMS, _ := testClock(t)
	store, err := NewMemoryCircuitStore(10, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	scope := accountScope("acc-1")
	scopeKey, _ := AccountCircuitScopeKey(scope)
	incident := incidentRow(scopeKey, scope, CircuitIncidentOpen, 100)
	ledger := &fakeLedger{byScopeKey: map[string]CircuitIncidentRecord{scopeKey: incident}}
	outbox := &fakeOutbox{claims: [][]OutboxEvent{{
		{EventID: "e1", EventType: "dispatch_revision_changed", AccountRuntimeKey: "acc-1", DispatchRevision: 9, TransitionID: "tx-rev"},
		{EventID: "e2", EventType: "incident_projected", AccountRuntimeKey: "acc-1", CircuitScopeKey: scopeKey},
		{EventID: "e3", EventType: "incident_projected", AccountRuntimeKey: "acc-1", CircuitScopeKey: "missing"},
	}}}
	maintenance, _ := newTestControlPlane(t, store, ledger, outbox, nil)

	acknowledged, err := maintenance.ProjectPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("ProjectPending 失败: %v", err)
	}
	if acknowledged != 2 {
		t.Fatalf("ack 数 = %d, want 2", acknowledged)
	}
	if len(outbox.released) != 1 || outbox.released[0].EventID != "e3" {
		t.Fatalf("ledger 缺失事件应释放重放: %+v", outbox.released)
	}
	state, _ := store.Get(context.Background(), scope, 1_000)
	if state.DispatchRevision == "9" {
		// dispatch_revision_changed 关闭整个账户作用域（CLOSED + 新 revision）。
		t.Fatalf("账户 revision 变更不应保持旧 revision: %+v", state)
	}
}

// ---- reconcileActive：游标推进 + kill-restart 从持久游标续跑 ----

func TestControlPlaneReconcileActiveCursorAdvanceAndRestart(t *testing.T) {
	nowMS, _ := testClock(t)
	store, err := NewMemoryCircuitStore(10, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	scopeA := accountScope("acc-a")
	scopeB := accountScope("acc-b")
	keyA, _ := AccountCircuitScopeKey(scopeA)
	keyB, _ := AccountCircuitScopeKey(scopeB)
	incidentA := incidentRow(keyA, scopeA, CircuitIncidentOpen, 100)
	incidentB := incidentRow(keyB, scopeB, CircuitIncidentSuspect, 200)
	ledger := &fakeLedger{items: []CircuitIncidentRecord{incidentA, incidentB}}
	outbox := &fakeOutbox{}
	cursorStore := &fakeCursorStore{}
	maintenance, _ := newTestControlPlane(t, store, ledger, outbox, cursorStore)

	// rebuild 使全局就绪。
	if _, err := maintenance.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild 失败: %v", err)
	}
	repaired, err := maintenance.ReconcileActive(context.Background(), 1)
	if err != nil {
		t.Fatalf("ReconcileActive 失败: %v", err)
	}
	if repaired != 1 {
		t.Fatalf("第一页 repaired = %d", repaired)
	}
	if len(cursorStore.saved) != 1 || cursorStore.saved[0].CircuitScopeKey != keyA {
		t.Fatalf("游标应持久化到第一页末尾: %+v", cursorStore.saved)
	}

	// 模拟 kill-restart：全新 maintenance 实例 + 持久游标 store 已保存 A 页游标；
	// 同一 ledger 从头开始提供页——已游标化的页必须被跳过。
	ledger2 := &fakeLedger{items: []CircuitIncidentRecord{incidentA, incidentB}}
	cursorStoreRestarted := &fakeCursorStore{loaded: &IncidentCursor{UpdatedAtMS: 100, CircuitScopeKey: keyA}}
	restarted, err := NewControlPlaneMaintenance(store, ledger2, outbox, ControlPlaneOptions{
		OwnerID:     "restarted-owner",
		NowMS:       func() int64 { return 1_000 },
		CursorStore: cursorStoreRestarted,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 重启后 rebuild 幂等回放（持久事实源），然后 reconcile 从持久游标续跑。
	if _, err := restarted.Rebuild(context.Background()); err != nil {
		t.Fatalf("重启 Rebuild 失败: %v", err)
	}
	queriesBefore := len(ledger2.pageQueries)
	repaired, err = restarted.ReconcileActive(context.Background(), 1)
	if err != nil {
		t.Fatalf("重启后 ReconcileActive 失败: %v", err)
	}
	if repaired != 1 {
		t.Fatalf("重启后续跑 repaired = %d", repaired)
	}
	firstQuery := ledger2.pageQueries[queriesBefore]
	if firstQuery.AfterUpdatedAtMS == nil || *firstQuery.AfterUpdatedAtMS != 100 {
		t.Fatalf("重启后应从持久游标 100 续跑: %+v", firstQuery)
	}
}

// rebuild：deferred parents 最后回放；无效游标返回契约 reason。
func TestControlPlaneRebuildDeferredParentsAndInvalidCursor(t *testing.T) {
	nowMS, _ := testClock(t)
	store, err := NewMemoryCircuitStore(10, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	scope := accountScope("acc-1")
	key, _ := AccountCircuitScopeKey(scope)
	parent := incidentRow(key, scope, CircuitIncidentOpen, 100)
	parent.ChildIncidentIDs = []string{"child-1"}
	ledger := &fakeLedger{items: []CircuitIncidentRecord{parent}}
	outbox := &fakeOutbox{}
	maintenance, _ := newTestControlPlane(t, store, ledger, outbox, nil)

	result, err := maintenance.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("Rebuild 失败: %v", err)
	}
	if result.Loaded != 1 || result.Blocked {
		t.Fatalf("deferred parent 应最终回放: %+v", result)
	}
	if !maintenance.IsReady() {
		t.Fatal("成功 rebuild 后应就绪")
	}

	// 游标回退（异常数据源）→ invalid_cursor。
	badLedger := &fakeLedger{forcePages: []RebuildPage{
		{Items: nil, NextCursor: &IncidentCursor{UpdatedAtMS: 100, CircuitScopeKey: key}},
		{Items: nil, NextCursor: &IncidentCursor{UpdatedAtMS: 50, CircuitScopeKey: key}},
	}}
	maintenance2, _ := newTestControlPlane(t, store, badLedger, outbox, nil)
	result, err = maintenance2.Rebuild(context.Background())
	if err == nil || result.Reason != RebuildReasonInvalidCursor {
		t.Fatalf("应返回 invalid_cursor: %+v err=%v", result, err)
	}
}

// RunMaintenance：未就绪时 rebuild 后继续 project+reconcile。
func TestControlPlaneRunMaintenanceSumsCounts(t *testing.T) {
	nowMS, _ := testClock(t)
	store, err := NewMemoryCircuitStore(10, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	scope := accountScope("acc-1")
	key, _ := AccountCircuitScopeKey(scope)
	incident := incidentRow(key, scope, CircuitIncidentOpen, 100)
	ledger := &fakeLedger{
		items:      []CircuitIncidentRecord{incident},
		byScopeKey: map[string]CircuitIncidentRecord{key: incident},
	}
	outbox := &fakeOutbox{claims: [][]OutboxEvent{
		{{EventID: "e1", EventType: "dispatch_revision_changed", AccountRuntimeKey: "acc-1", DispatchRevision: 9, TransitionID: "tx"}},
	}}
	maintenance, _ := newTestControlPlane(t, store, ledger, outbox, nil)
	total, err := maintenance.RunMaintenance(context.Background(), 10)
	if err != nil {
		t.Fatalf("RunMaintenance 失败: %v", err)
	}
	if total != 2 { // 1 ack + 1 reconcile repaired
		t.Fatalf("总数 = %d, want 2", total)
	}
}

func TestControlPlaneValidateDefaults(t *testing.T) {
	if _, err := NewControlPlaneMaintenance(nil, nil, nil, ControlPlaneOptions{}); err == nil {
		t.Fatal("缺依赖应报错")
	}
	if _, err := NewControlPlaneMaintenance(nil, nil, nil, ControlPlaneOptions{NowMS: func() int64 { return 0 }}); err == nil {
		t.Fatal("store 缺失应报错")
	}
}
