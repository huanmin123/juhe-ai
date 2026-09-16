// 波次 w12c：补齐控制面维护与恢复扫描分支覆盖。全部进程内 mock（内存电路
// store、可注错 ledger/outbox/cursor 与 stub CircuitStore），不连接真实依赖。
//
// 不可达/防御守卫登记（无自然触发路径）：
//   - circuitrecovery.go observeMutation 的错误分支：onMutation 回调无返回值，
//     observeMutation 恒返回 nil，四处 observeErr 判断不可达。
//   - circuitrecovery.go NewRandomID 的 crypto/rand 失败回退：rand.Read 不
//     注入失败，回退分支不可达。
package opsjobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 可注错依赖
// ---------------------------------------------------------------------------

type w12cErrOutbox struct {
	inner     *fakeOutbox
	claimErr  error
	ackErr    error
	releaseErr error
	ackResult bool
}

func (f *w12cErrOutbox) Claim(ctx context.Context, owner string, nowMS, leaseMS int64, limit int) ([]OutboxEvent, error) {
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	return f.inner.Claim(ctx, owner, nowMS, leaseMS, limit)
}

func (f *w12cErrOutbox) Ack(ctx context.Context, event OutboxEvent, at int64) (bool, error) {
	if f.ackErr != nil {
		return false, f.ackErr
	}
	if !f.ackResult {
		return f.inner.Ack(ctx, event, at)
	}
	return false, nil
}

func (f *w12cErrOutbox) ReleaseForReplay(ctx context.Context, event OutboxEvent, class string, nowMS, retryMS int64) error {
	if f.releaseErr != nil {
		return f.releaseErr
	}
	return f.inner.ReleaseForReplay(ctx, event, class, nowMS, retryMS)
}

type w12cErrLedger struct {
	inner        *fakeLedger
	listErr      error
	byRuntimeErr error
	byScopeErr   error
	byScopeEmpty bool
}

func (f *w12cErrLedger) ListForRebuild(ctx context.Context, query RebuildPageQuery) (RebuildPage, error) {
	if f.listErr != nil {
		return RebuildPage{}, f.listErr
	}
	return f.inner.ListForRebuild(ctx, query)
}

func (f *w12cErrLedger) ListByRuntimeKeys(ctx context.Context, keys []string, include bool, nowMS int64) ([]CircuitIncidentRecord, error) {
	if f.byRuntimeErr != nil {
		return nil, f.byRuntimeErr
	}
	return f.inner.ListByRuntimeKeys(ctx, keys, include, nowMS)
}

func (f *w12cErrLedger) GetByScopeKey(ctx context.Context, scopeKey string) (*CircuitIncidentRecord, error) {
	if f.byScopeErr != nil {
		return nil, f.byScopeErr
	}
	if f.byScopeEmpty {
		return nil, nil
	}
	return f.inner.GetByScopeKey(ctx, scopeKey)
}

// w12cStubCircuitStore 覆盖恢复扫描用到的 CircuitStore 方法并支持注错。
type w12cStubCircuitStore struct {
	acquireErr      error
	acquireStatus   CircuitMutationStatus
	completeErr     error
	completeStatus  CircuitMutationStatus
	completeApplied bool
	replaceErr      error
	clearErr        error
}

func (s *w12cStubCircuitStore) Get(context.Context, CircuitScope, int64) (CircuitState, error) {
	return CircuitState{}, nil
}

func (s *w12cStubCircuitStore) Restore(context.Context, CircuitState, int64) (CircuitMutationResult, error) {
	return CircuitMutationResult{Status: CircuitMutationApplied}, nil
}

func (s *w12cStubCircuitStore) ListDue(context.Context, int64, int) ([]CircuitState, error) {
	return nil, nil
}

func (s *w12cStubCircuitStore) AcquireConfirmationLease(context.Context, CircuitTransitionIdentity, CircuitLeaseSpec) (CircuitMutationResult, error) {
	if s.acquireErr != nil {
		return CircuitMutationResult{}, s.acquireErr
	}
	return CircuitMutationResult{Status: s.acquireStatus, State: w12cSuspect(accountScope("stub"), 1, "7", 0)}, nil
}

func (s *w12cStubCircuitStore) AcquireCanaryLease(context.Context, CircuitTransitionIdentity, CircuitLeaseSpec) (CircuitMutationResult, error) {
	if s.acquireErr != nil {
		return CircuitMutationResult{}, s.acquireErr
	}
	return CircuitMutationResult{Status: s.acquireStatus, State: w12cSuspect(accountScope("stub"), 1, "7", 0)}, nil
}

func (s *w12cStubCircuitStore) CompleteConfirmation(context.Context, CircuitTransitionIdentity, string, CircuitCompletion) (CircuitMutationResult, error) {
	if s.completeErr != nil {
		return CircuitMutationResult{}, s.completeErr
	}
	status := s.completeStatus
	if s.completeApplied {
		status = CircuitMutationApplied
	}
	return CircuitMutationResult{Status: status, State: w12cSuspect(accountScope("stub"), 1, "7", 0)}, nil
}

func (s *w12cStubCircuitStore) CompleteCanary(context.Context, CircuitTransitionIdentity, string, CircuitCompletion) (CircuitMutationResult, error) {
	if s.completeErr != nil {
		return CircuitMutationResult{}, s.completeErr
	}
	status := s.completeStatus
	if s.completeApplied {
		status = CircuitMutationApplied
	}
	return CircuitMutationResult{Status: status, State: w12cSuspect(accountScope("stub"), 1, "7", 0)}, nil
}

func (s *w12cStubCircuitStore) ClearAccountEscalationEvidence(context.Context, string, string, string, int64) (bool, error) {
	if s.clearErr != nil {
		return false, s.clearErr
	}
	return false, nil
}

func (s *w12cStubCircuitStore) ReplaceDispatchRevision(context.Context, CircuitScope, string, string, int64) (CircuitMutationResult, error) {
	if s.replaceErr != nil {
		return CircuitMutationResult{}, s.replaceErr
	}
	return CircuitMutationResult{Status: CircuitMutationApplied}, nil
}

func (s *w12cStubCircuitStore) ReplaceAccountDispatchRevision(context.Context, string, string, string, int64) (int64, error) {
	return 0, nil
}

// w12cSuspect 构造合法 SUSPECT 状态（不依赖 testing.T）。
func w12cSuspect(scope CircuitScope, generation int64, dispatchRevision string, nowMS int64) CircuitState {
	scopeKey, err := AccountCircuitScopeKey(scope)
	if err != nil {
		panic(err)
	}
	required := 2
	count := 0
	retryAt := nowMS
	return CircuitState{
		ScopeKey:                     scopeKey,
		Scope:                        scope,
		Phase:                        CircuitPhaseSuspect,
		Generation:                   generation,
		DispatchRevision:             dispatchRevision,
		TransitionID:                 "w12c-seed",
		ConfirmationFailuresRequired: &required,
		ConfirmationFailureCount:     &count,
		RetryAtMS:                    &retryAt,
		UpdatedAtMS:                  nowMS,
	}
}

func w12cRecoveryService(t *testing.T, store CircuitStore, resolver CircuitRecoveryTargetResolver, mutate func(*CircuitRecoveryServiceOptions)) *CircuitRecoveryService {
	t.Helper()
	counter := 0
	options := CircuitRecoveryServiceOptions{
		BatchSize:       10,
		Concurrency:     1,
		LeaseDurationMS: 60_000,
		NowMS:           func() int64 { return 1_000 },
		CreateID: func() string {
			counter++
			return fmt.Sprintf("w12c-id-%d", counter)
		},
	}
	if mutate != nil {
		mutate(&options)
	}
	service, err := NewCircuitRecoveryService(store, resolver, options)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

// ---------------------------------------------------------------------------
// ControlPlane
// ---------------------------------------------------------------------------

func TestW12CControlPlaneProjectPendingErrorArms(t *testing.T) {
	store := w7dMemoryStore(t, 8)
	scope := w9hScope("acc-w12c-pp")
	scopeKey, _ := AccountCircuitScopeKey(scope)
	validRow := incidentRow(scopeKey, scope, CircuitIncidentOpen, 900)

	claimErr := &w12cErrOutbox{inner: &fakeOutbox{claims: [][]OutboxEvent{{}}}, claimErr: errors.New("w12c: claim boom")}
	maintenance, _ := newTestControlPlane(t, store, &fakeLedger{}, claimErr, nil)
	if _, err := maintenance.ProjectPending(context.Background(), 5); err == nil {
		t.Fatal("claim 错误必须暴露")
	}

	// limit<1。
	okOutbox := &w12cErrOutbox{inner: &fakeOutbox{}}
	maintenance, _ = newTestControlPlane(t, store, &fakeLedger{}, okOutbox, nil)
	if _, err := maintenance.ProjectPending(context.Background(), 0); err == nil {
		t.Fatal("limit<1 必须拒绝")
	}

	// Ack 错误与 Ack false。
	ackErr := &w12cErrOutbox{inner: &fakeOutbox{claims: [][]OutboxEvent{{{EventID: "e1", EventType: "incident_projected", CircuitScopeKey: scopeKey, TransitionID: "t"}}}}, ackErr: errors.New("w12c: ack boom")}
	ackLedger := &fakeLedger{byScopeKey: map[string]CircuitIncidentRecord{scopeKey: validRow}}
	maintenance, _ = newTestControlPlane(t, store, ackLedger, ackErr, nil)
	if _, err := maintenance.ProjectPending(context.Background(), 5); err == nil {
		t.Fatal("ack 错误必须暴露")
	}
	ackFalse := &w12cErrOutbox{inner: &fakeOutbox{claims: [][]OutboxEvent{{{EventID: "e1", EventType: "incident_projected", CircuitScopeKey: scopeKey, TransitionID: "t"}}}}, ackResult: true}
	maintenance, _ = newTestControlPlane(t, store, ackLedger, ackFalse, nil)
	acknowledged, err := maintenance.ProjectPending(context.Background(), 5)
	if err != nil || acknowledged != 0 {
		t.Fatalf("ack false 不应计数: %d %v", acknowledged, err)
	}

	// release 错误（projection 失败且 release 也失败）。
	releaseErrBox := &w12cErrOutbox{
		inner:      &fakeOutbox{claims: [][]OutboxEvent{{{EventID: "e1", EventType: "incident_projected", CircuitScopeKey: "missing-key", TransitionID: "t"}}}},
		releaseErr: errors.New("w12c: release boom"),
	}
	maintenance, _ = newTestControlPlane(t, store, &fakeLedger{}, releaseErrBox, nil)
	if _, err := maintenance.ProjectPending(context.Background(), 5); err == nil {
		t.Fatal("release 错误必须暴露")
	}

	// GetByScopeKey 错误与 ledger 缺失（释放重放）。
	scopeErrLedger := &w12cErrLedger{byScopeErr: errors.New("w12c: ledger boom")}
	scopeErrBox := &w12cErrOutbox{inner: &fakeOutbox{claims: [][]OutboxEvent{{{EventID: "e1", EventType: "incident_projected", CircuitScopeKey: scopeKey, TransitionID: "t"}}}}}
	maintenance, _ = newTestControlPlane(t, store, scopeErrLedger, scopeErrBox, nil)
	// projection 错误不上抛：事件被释放为重放。
	if _, err := maintenance.ProjectPending(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if len(scopeErrBox.inner.released) != 1 {
		t.Fatalf("ledger 错误事件应释放重放: %#v", scopeErrBox.inner.released)
	}
	missingLedger := &w12cErrLedger{byScopeEmpty: true}
	missingBox := &w12cErrOutbox{inner: &fakeOutbox{claims: [][]OutboxEvent{{{EventID: "e1", EventType: "incident_projected", CircuitScopeKey: scopeKey, TransitionID: "t"}}}}}
	maintenance, _ = newTestControlPlane(t, store, missingLedger, missingBox, nil)
	acknowledged, err = maintenance.ProjectPending(context.Background(), 5)
	if err != nil || acknowledged != 0 {
		t.Fatalf("ledger 缺失应释放重放: %d %v", acknowledged, err)
	}

	// dispatch_revision_changed 事件与 restore 错误事件。
	store2 := w7dMemoryStore(t, 8)
	dispatchBox := &w12cErrOutbox{inner: &fakeOutbox{claims: [][]OutboxEvent{{{EventID: "e2", EventType: "dispatch_revision_changed", AccountRuntimeKey: "acc-w12c-pp", DispatchRevision: 9, TransitionID: "tx-dispatch"}}}}}
	maintenance, _ = newTestControlPlane(t, store2, &fakeLedger{}, dispatchBox, nil)
	acknowledged, err = maintenance.ProjectPending(context.Background(), 5)
	if err != nil || acknowledged != 1 {
		t.Fatalf("dispatch 事件应 ack: %d %v", acknowledged, err)
	}
	badKindBox := &w12cErrOutbox{inner: &fakeOutbox{claims: [][]OutboxEvent{{{EventID: "e3", EventType: "incident_projected", CircuitScopeKey: scopeKey, TransitionID: "t"}}}}}
	badKindLedger := &w12cErrLedger{inner: &fakeLedger{byScopeKey: map[string]CircuitIncidentRecord{scopeKey: func() CircuitIncidentRecord {
		row := incidentRow(scopeKey, scope, CircuitIncidentOpen, 900)
		row.ScopeKind = "w12c-invalid"
		return row
	}()}}}
	maintenance, _ = newTestControlPlane(t, store2, badKindLedger.inner, badKindBox, nil)
	acknowledged, err = maintenance.ProjectPending(context.Background(), 5)
	if err != nil || acknowledged != 0 {
		t.Fatalf("restore 失败应释放: %d %v", acknowledged, err)
	}

	// 容量耗尽 → 投影错误 → 释放。
	tinyStore := w7dMemoryStore(t, 1)
	if _, err := tinyStore.Restore(context.Background(), openState(t, w9hScope("acc-w12c-occupy"), 1, "7", 1_000), 1_000); err != nil {
		t.Fatal(err)
	}
	capBox := &w12cErrOutbox{inner: &fakeOutbox{claims: [][]OutboxEvent{{{EventID: "e4", EventType: "incident_projected", CircuitScopeKey: scopeKey, TransitionID: "t"}}}}}
	capLedger := &fakeLedger{byScopeKey: map[string]CircuitIncidentRecord{scopeKey: validRow}}
	maintenance, _ = newTestControlPlane(t, tinyStore, capLedger, capBox, nil)
	acknowledged, err = maintenance.ProjectPending(context.Background(), 5)
	if err != nil || acknowledged != 0 {
		t.Fatalf("容量耗尽应释放: %d %v", acknowledged, err)
	}

	// 未解析子 incident → ListByRuntimeKeys 补全。
	childScope := w9hScope("acc-w12c-pp-child")
	childKey, _ := AccountCircuitScopeKey(childScope)
	parentRow := incidentRow(scopeKey, scope, CircuitIncidentOpen, 900)
	parentRow.ChildIncidentIDs = []string{"child-incident"}
	childRow := incidentRow(childKey, childScope, CircuitIncidentOpen, 950)
	childRow.IncidentID = "child-incident"
	hierarchyLedger := &fakeLedger{
		byScopeKey:    map[string]CircuitIncidentRecord{scopeKey: parentRow},
		byRuntimeKeys: map[string][]CircuitIncidentRecord{"acc-w12c-pp": {childRow}},
	}
	hierarchyBox := &w12cErrOutbox{inner: &fakeOutbox{claims: [][]OutboxEvent{{{EventID: "e5", EventType: "incident_projected", CircuitScopeKey: scopeKey, TransitionID: "t"}}}}}
	maintenance, _ = newTestControlPlane(t, w7dMemoryStore(t, 8), hierarchyLedger, hierarchyBox, nil)
	acknowledged, err = maintenance.ProjectPending(context.Background(), 5)
	if err != nil || acknowledged != 1 {
		t.Fatalf("层级补全应 ack: %d %v", acknowledged, err)
	}
	hierarchyErrLedger := &w12cErrLedger{
		inner:        &fakeLedger{byScopeKey: map[string]CircuitIncidentRecord{scopeKey: parentRow}},
		byRuntimeErr: errors.New("w12c: runtime keys boom"),
	}
	hierarchyErrBox := &w12cErrOutbox{inner: &fakeOutbox{claims: [][]OutboxEvent{{{EventID: "e6", EventType: "incident_projected", CircuitScopeKey: scopeKey, TransitionID: "t"}}}}}
	maintenance, _ = newTestControlPlane(t, w7dMemoryStore(t, 8), hierarchyErrLedger, hierarchyErrBox, nil)
	if _, err := maintenance.ProjectPending(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if len(hierarchyErrBox.inner.released) != 1 {
		t.Fatalf("ListByRuntimeKeys 错误事件应释放重放: %#v", hierarchyErrBox.inner.released)
	}
}

func w12cControlPlane(store CircuitStore, ledger ControlPlaneLedger, outbox ControlPlaneOutbox, cursor ReconcileCursorStore, mutate func(*ControlPlaneOptions)) (*ControlPlaneMaintenance, error) {
	options := ControlPlaneOptions{
		OwnerID:     "w12c-owner",
		NowMS:       func() int64 { return 1_000 },
		CursorStore: cursor,
	}
	if mutate != nil {
		mutate(&options)
	}
	return NewControlPlaneMaintenance(store, ledger, outbox, options)
}

func TestW12CControlPlaneRebuildArms(t *testing.T) {
	store := w7dMemoryStore(t, 8)
	scope := w9hScope("acc-w12c-rb")
	scopeKey, _ := AccountCircuitScopeKey(scope)

	// rebuild 并发拒绝。
	busy, err := w12cControlPlane(store, &fakeLedger{}, &fakeOutbox{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	busy.mu.Lock()
	busy.rebuilding = true
	busy.mu.Unlock()
	if _, err := busy.Rebuild(context.Background()); err == nil {
		t.Fatal("rebuild 并发必须拒绝")
	}

	// ListForRebuild 错误 → timeout 失败码。
	listErrLedger := &w12cErrLedger{listErr: errors.New("w12c: list boom")}
	failed, err := w12cControlPlane(store, listErrLedger, &fakeOutbox{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := failed.Rebuild(context.Background())
	if err == nil || result.Reason != RebuildReasonTimeout || !result.Blocked {
		t.Fatalf("list 错误应记 timeout: %#v %v", result, err)
	}

	// 总超时：monotonic 第二次起跳变。
	timeoutMaintenance, err := w12cControlPlane(store, &fakeLedger{}, &fakeOutbox{}, nil, func(o *ControlPlaneOptions) {
		calls := 0
		o.MonotonicMS = func() int64 {
			calls++
			if calls >= 2 {
				return 1_000_000
			}
			return 0
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err = timeoutMaintenance.Rebuild(context.Background())
	if err == nil || result.Reason != RebuildReasonTimeout {
		t.Fatalf("超时应记 timeout: %#v %v", result, err)
	}

	// restore 失败：无效 scopeKind。
	badRow := incidentRow(scopeKey, scope, CircuitIncidentOpen, 900)
	badRow.ScopeKind = "w12c-invalid"
	badLedger := &fakeLedger{items: []CircuitIncidentRecord{badRow}}
	badMaintenance, err := w12cControlPlane(store, badLedger, &fakeOutbox{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err = badMaintenance.Rebuild(context.Background())
	if err == nil || result.Reason != RebuildReasonFailed {
		t.Fatalf("restore 失败应记 failed: %#v %v", result, err)
	}

	// 容量耗尽。
	tinyStore := w7dMemoryStore(t, 1)
	if _, err := tinyStore.Restore(context.Background(), openState(t, w9hScope("acc-w12c-occupy"), 1, "7", 1_000), 1_000); err != nil {
		t.Fatal(err)
	}
	capLedger := &fakeLedger{items: []CircuitIncidentRecord{incidentRow(scopeKey, scope, CircuitIncidentOpen, 900)}}
	capMaintenance, err := w12cControlPlane(tinyStore, capLedger, &fakeOutbox{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err = capMaintenance.Rebuild(context.Background())
	if err == nil || result.Reason != RebuildReasonCapacityExhausted {
		t.Fatalf("容量耗尽应记 capacity: %#v %v", result, err)
	}

	// 页数上限：两页且游标推进 → 第二轮 pageNumber>maxPages。
	pagingLedger := &fakeLedger{forcePages: []RebuildPage{
		{Items: []CircuitIncidentRecord{incidentRow(scopeKey, scope, CircuitIncidentOpen, 900)}, NextCursor: &IncidentCursor{UpdatedAtMS: 900, CircuitScopeKey: scopeKey}},
		{Items: []CircuitIncidentRecord{incidentRow(scopeKey, scope, CircuitIncidentOpen, 901)}},
	}}
	pagingMaintenance, err := w12cControlPlane(w7dMemoryStore(t, 8), pagingLedger, &fakeOutbox{}, nil, func(o *ControlPlaneOptions) {
		o.RebuildMaxPages = 1
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err = pagingMaintenance.Rebuild(context.Background())
	if err == nil || result.Reason != RebuildReasonInvalidCursor {
		t.Fatalf("页数超限应记 invalid cursor: %#v %v", result, err)
	}

	// 游标回退：第二页游标不前进。
	regressLedger := &fakeLedger{forcePages: []RebuildPage{
		{Items: []CircuitIncidentRecord{incidentRow(scopeKey, scope, CircuitIncidentOpen, 900)}, NextCursor: &IncidentCursor{UpdatedAtMS: 900, CircuitScopeKey: scopeKey}},
		{Items: []CircuitIncidentRecord{}, NextCursor: &IncidentCursor{UpdatedAtMS: 800, CircuitScopeKey: scopeKey}},
	}}
	regressMaintenance, err := w12cControlPlane(w7dMemoryStore(t, 8), regressLedger, &fakeOutbox{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err = regressMaintenance.Rebuild(context.Background())
	if err == nil || result.Reason != RebuildReasonInvalidCursor {
		t.Fatalf("游标回退应记 invalid cursor: %#v %v", result, err)
	}

	// deferred parents：父带子 incident，子先恢复、父延迟恢复成功。
	childScope := w9hScope("acc-w12c-rb-child")
	childKey, _ := AccountCircuitScopeKey(childScope)
	parentRow := incidentRow(scopeKey, scope, CircuitIncidentOpen, 950)
	parentRow.IncidentID = "parent-incident"
	parentRow.ChildIncidentIDs = []string{"child-incident"}
	childRow := incidentRow(childKey, childScope, CircuitIncidentOpen, 900)
	childRow.IncidentID = "child-incident"
	hierarchyLedger := &fakeLedger{items: []CircuitIncidentRecord{parentRow, childRow}}
	hierarchyMaintenance, err := w12cControlPlane(w7dMemoryStore(t, 8), hierarchyLedger, &fakeOutbox{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err = hierarchyMaintenance.Rebuild(context.Background())
	if err != nil || result.Blocked || result.Loaded != 2 {
		t.Fatalf("层级重建应成功: %#v %v", result, err)
	}
	if !hierarchyMaintenance.IsReady() {
		t.Fatal("成功重建后应就绪")
	}
}

func TestW12CControlPlaneReconcileAndMaintenanceArms(t *testing.T) {
	scope := w9hScope("acc-w12c-rec")
	scopeKey, _ := AccountCircuitScopeKey(scope)
	row := incidentRow(scopeKey, scope, CircuitIncidentOpen, 900)

	// 未就绪 / rebuilding → 0。
	notReady, err := w12cControlPlane(w7dMemoryStore(t, 8), &fakeLedger{}, &fakeOutbox{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	repaired, err := notReady.ReconcileActive(context.Background(), 5)
	if err != nil || repaired != 0 {
		t.Fatalf("未就绪应返回 0: %d %v", repaired, err)
	}
	notReady.mu.Lock()
	notReady.globallyReady = true
	notReady.rebuilding = true
	notReady.mu.Unlock()
	repaired, err = notReady.ReconcileActive(context.Background(), 5)
	if err != nil || repaired != 0 {
		t.Fatalf("rebuilding 应返回 0: %d %v", repaired, err)
	}
	notReady.mu.Lock()
	notReady.rebuilding = false
	notReady.mu.Unlock()
	if _, err := notReady.ReconcileActive(context.Background(), 0); err == nil {
		t.Fatal("limit<1 必须拒绝")
	}

	// Load 错误。
	loadErrCursor := &w12cErrCursor{loadErr: errors.New("w12c: load boom")}
	loadErrMaintenance, err := w12cControlPlane(w7dMemoryStore(t, 8), &fakeLedger{items: []CircuitIncidentRecord{row}}, &fakeOutbox{}, loadErrCursor, nil)
	if err != nil {
		t.Fatal(err)
	}
	loadErrMaintenance.mu.Lock()
	loadErrMaintenance.globallyReady = true
	loadErrMaintenance.mu.Unlock()
	if _, err := loadErrMaintenance.ReconcileActive(context.Background(), 5); err == nil {
		t.Fatal("游标读取错误必须暴露")
	}

	// ListForRebuild 错误 → timeout。
	listErrMaintenance, err := w12cControlPlane(w7dMemoryStore(t, 8), &w12cErrLedger{listErr: errors.New("w12c: list boom")}, &fakeOutbox{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	listErrMaintenance.mu.Lock()
	listErrMaintenance.globallyReady = true
	listErrMaintenance.mu.Unlock()
	if _, err := listErrMaintenance.ReconcileActive(context.Background(), 5); err == nil || !strings.Contains(err.Error(), RebuildReasonTimeout) {
		t.Fatalf("对账读取错误应记 timeout: %v", err)
	}

	// restore / capacity / ListByRuntimeKeys 错误。
	baseStore := w7dMemoryStore(t, 8)
	errMaintenance, err := w12cControlPlane(baseStore, &fakeLedger{items: []CircuitIncidentRecord{func() CircuitIncidentRecord {
		bad := row
		bad.ScopeKind = "w12c-invalid"
		return bad
	}()}}, &fakeOutbox{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	errMaintenance.mu.Lock()
	errMaintenance.globallyReady = true
	errMaintenance.mu.Unlock()
	if _, err := errMaintenance.ReconcileActive(context.Background(), 5); err == nil {
		t.Fatal("restore 错误必须暴露")
	}

	tinyStore := w7dMemoryStore(t, 1)
	if _, err := tinyStore.Restore(context.Background(), openState(t, w9hScope("acc-w12c-occupy"), 1, "7", 1_000), 1_000); err != nil {
		t.Fatal(err)
	}
	capMaintenance, err := w12cControlPlane(tinyStore, &fakeLedger{items: []CircuitIncidentRecord{row}}, &fakeOutbox{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	capMaintenance.mu.Lock()
	capMaintenance.globallyReady = true
	capMaintenance.mu.Unlock()
	if _, err := capMaintenance.ReconcileActive(context.Background(), 5); err == nil {
		t.Fatal("容量不足必须暴露")
	}

	parentRow := row
	parentRow.ChildIncidentIDs = []string{"w12c-missing-child"}
	hierarchyLedger := &w12cErrLedger{
		inner:        &fakeLedger{items: []CircuitIncidentRecord{parentRow}},
		byRuntimeErr: errors.New("w12c: runtime keys boom"),
	}
	hierarchyMaintenance, err := w12cControlPlane(w7dMemoryStore(t, 8), hierarchyLedger, &fakeOutbox{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	hierarchyMaintenance.mu.Lock()
	hierarchyMaintenance.globallyReady = true
	hierarchyMaintenance.mu.Unlock()
	if _, err := hierarchyMaintenance.ReconcileActive(context.Background(), 5); err == nil {
		t.Fatal("ListByRuntimeKeys 错误必须暴露")
	}

	// 成功对账：游标保存；页尾游标为空时从最后行构造。
	savedCursor := &fakeCursorStore{}
	happyLedger := &fakeLedger{forcePages: []RebuildPage{{Items: []CircuitIncidentRecord{row}}}}
	happyMaintenance, err := w12cControlPlane(w7dMemoryStore(t, 8), happyLedger, &fakeOutbox{}, savedCursor, nil)
	if err != nil {
		t.Fatal(err)
	}
	happyMaintenance.mu.Lock()
	happyMaintenance.globallyReady = true
	happyMaintenance.mu.Unlock()
	repaired, err = happyMaintenance.ReconcileActive(context.Background(), 5)
	if err != nil || repaired != 1 {
		t.Fatalf("对账应修复 1 行: %d %v", repaired, err)
	}
	if len(savedCursor.saved) != 1 || savedCursor.saved[0].CircuitScopeKey != scopeKey {
		t.Fatalf("游标应持久化: %#v", savedCursor.saved)
	}

	// 游标保存失败。
	saveErrCursor := &w12cErrCursor{saveErr: errors.New("w12c: save boom")}
	saveErrLedger := &fakeLedger{forcePages: []RebuildPage{{Items: []CircuitIncidentRecord{row}, NextCursor: &IncidentCursor{UpdatedAtMS: 900, CircuitScopeKey: scopeKey}}}}
	saveErrMaintenance, err := w12cControlPlane(w7dMemoryStore(t, 8), saveErrLedger, &fakeOutbox{}, saveErrCursor, nil)
	if err != nil {
		t.Fatal(err)
	}
	saveErrMaintenance.mu.Lock()
	saveErrMaintenance.globallyReady = true
	saveErrMaintenance.mu.Unlock()
	if _, err := saveErrMaintenance.ReconcileActive(context.Background(), 5); err == nil {
		t.Fatal("游标保存错误必须暴露")
	}
}

type w12cErrCursor struct {
	loaded  *IncidentCursor
	loadErr error
	saveErr error
	saved   []IncidentCursor
}

func (c *w12cErrCursor) Load(context.Context) (*IncidentCursor, error) {
	if c.loadErr != nil {
		return nil, c.loadErr
	}
	return c.loaded, nil
}

func (c *w12cErrCursor) Save(_ context.Context, cursor IncidentCursor) error {
	if c.saveErr != nil {
		return c.saveErr
	}
	c.saved = append(c.saved, cursor)
	return nil
}

func TestW12CControlPlaneRunMaintenanceArms(t *testing.T) {
	scope := w9hScope("acc-w12c-run")
	scopeKey, _ := AccountCircuitScopeKey(scope)
	row := incidentRow(scopeKey, scope, CircuitIncidentOpen, 900)

	// EnsureRuntimeStateReady 错误：rebuild 并发拒绝（无 reason）→ 上抛。
	maintenance, err := w12cControlPlane(w7dMemoryStore(t, 8), &fakeLedger{}, &fakeOutbox{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	maintenance.mu.Lock()
	maintenance.rebuilding = true
	maintenance.mu.Unlock()
	if _, err := maintenance.RunMaintenance(context.Background(), 5); err == nil {
		t.Fatal("rebuild 冲突错误必须上抛")
	}
	maintenance.mu.Lock()
	maintenance.rebuilding = false
	maintenance.mu.Unlock()

	// blocked 重建（list 错误）→ 继续投影当前事实。
	blockedMaintenance, err := w12cControlPlane(w7dMemoryStore(t, 8), &w12cErrLedger{listErr: errors.New("w12c: list boom")}, &fakeOutbox{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blockedMaintenance.RunMaintenance(context.Background(), 5); err != nil {
		t.Fatalf("blocked 后应继续: %v", err)
	}

	// ProjectPending 错误上抛。
	claimErrBox := &w12cErrOutbox{inner: &fakeOutbox{claims: [][]OutboxEvent{{}}}, claimErr: errors.New("w12c: claim boom")}
	happyLedger := &fakeLedger{items: []CircuitIncidentRecord{row}}
	errMaintenance, err := w12cControlPlane(w7dMemoryStore(t, 8), happyLedger, claimErrBox, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := errMaintenance.RunMaintenance(context.Background(), 5); err == nil {
		t.Fatal("project 错误必须上抛")
	}

	// 就绪后全链路成功。
	happy, err := w12cControlPlane(w7dMemoryStore(t, 8), happyLedger, &fakeOutbox{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	total, err := happy.RunMaintenance(context.Background(), 5)
	if err != nil || total != 1 {
		t.Fatalf("维护总数应为 1: %d %v", total, err)
	}
	if !happy.EnsureRuntimeStateReadyFast() {
		t.Fatal("重建后应就绪")
	}
}

// EnsureRuntimeStateReadyFast 仅为测试可读性包装 IsReady。
func (m *ControlPlaneMaintenance) EnsureRuntimeStateReadyFast() bool { return m.IsReady() }

func TestW12CControlPlanePureHelpers(t *testing.T) {
	// withinRebuildTimeout：操作超时。
	slow := make(chan struct{})
	defer close(slow)
	if _, err := withinRebuildTimeout(context.Background(), func() (RebuildPage, error) {
		time.Sleep(50 * time.Millisecond)
		return RebuildPage{}, nil
	}, 1); err == nil || !strings.Contains(err.Error(), RebuildReasonTimeout) {
		t.Fatalf("慢操作应超时: %v", err)
	}
	// withinRebuildTimeout：ctx 取消。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := withinRebuildTimeout(ctx, func() (RebuildPage, error) {
		time.Sleep(50 * time.Millisecond)
		return RebuildPage{}, nil
	}, 60_000); err == nil {
		t.Fatal("取消必须暴露")
	}
	// classifyControlPlaneError。
	if got := classifyControlPlaneError(errors.New(strings.Repeat("x", 100))); len(got) != 64 {
		t.Fatalf("超长错误应截断: %d", len(got))
	}
	if got := classifyControlPlaneError(errors.New("   ")); got != "projector_error" {
		t.Fatalf("空白错误应兜底: %q", got)
	}
	// compareCursor。
	if compareCursor(IncidentCursor{UpdatedAtMS: 1}, IncidentCursor{UpdatedAtMS: 2}) >= 0 {
		t.Fatal("时间早应 -1")
	}
	if compareCursor(IncidentCursor{UpdatedAtMS: 1, CircuitScopeKey: "a"}, IncidentCursor{UpdatedAtMS: 1, CircuitScopeKey: "b"}) >= 0 {
		t.Fatal("scopeKey 早应 -1")
	}
	if compareCursor(IncidentCursor{UpdatedAtMS: 1, CircuitScopeKey: "a"}, IncidentCursor{UpdatedAtMS: 1, CircuitScopeKey: "a"}) != 0 {
		t.Fatal("相等应 0")
	}
	// incidentRuntimePhase。
	if incidentRuntimePhase(CircuitIncidentPersisting) != CircuitPhaseOpen || incidentRuntimePhase(CircuitIncidentShadowedByPersistent) != CircuitPhaseOpen {
		t.Fatal("PERSISTING/SHADOWED 应投影 OPEN")
	}
	if incidentRuntimePhase(CircuitIncidentSuspect) != CircuitPhaseSuspect {
		t.Fatal("SUSPECT 原样")
	}
}

func TestW12CIncidentToRuntimeStateArms(t *testing.T) {
	accountScopeValue := w9hScope("acc-w12c-incident")
	accountKey, _ := AccountCircuitScopeKey(accountScopeValue)
	leaseUntil := int64(5_000)
	nextAt := int64(6_000)
	openUntil := int64(7_000)

	cases := []struct {
		name    string
		mutate  func(*CircuitIncidentRecord)
		wantErr string
		check   func(t *testing.T, state CircuitState)
	}{
		{
			name: "key 缺 fingerprint",
			mutate: func(r *CircuitIncidentRecord) {
				r.ScopeKind = "key"
			},
			wantErr: "keyFingerprint",
		},
		{
			name: "protocol_model 缺 protocolCode",
			mutate: func(r *CircuitIncidentRecord) {
				r.ScopeKind = "protocol_model"
			},
			wantErr: "protocolCode",
		},
		{
			name: "protocol_model lane 无效",
			mutate: func(r *CircuitIncidentRecord) {
				r.ScopeKind = "protocol_model"
				r.ProtocolCode = "openai"
				r.RequestLane = "w12c-invalid"
			},
			wantErr: "requestLane",
		},
		{
			name: "protocol_model 缺 modelFamily",
			mutate: func(r *CircuitIncidentRecord) {
				r.ScopeKind = "protocol_model"
				r.ProtocolCode = "openai"
				r.RequestLane = "text"
			},
			wantErr: "modelFamily",
		},
		{
			name: "scopeKind 未知",
			mutate: func(r *CircuitIncidentRecord) {
				r.ScopeKind = "w12c-unknown"
			},
			wantErr: "scopeKind",
		},
		{
			name: "scopeKey 不一致",
			mutate: func(r *CircuitIncidentRecord) {
				r.CircuitScopeKey = "w12c-mismatch"
			},
			wantErr: "scopeKey",
		},
		{
			name: "HALF_OPEN + recovery 租约",
			mutate: func(r *CircuitIncidentRecord) {
				r.State = CircuitIncidentHalfOpen
				r.LeasePurpose = "recovery"
				r.LeaseID = "lease-1"
				r.LeaseUntilMS = &leaseUntil
			},
			check: func(t *testing.T, state CircuitState) {
				t.Helper()
				if state.HalfOpenOrigin != string(CircuitPhaseRecovering) || state.Lease == nil {
					t.Fatalf("recovery 租约投影: %#v", state)
				}
			},
		},
		{
			name: "HALF_OPEN 未知 purpose 无租约",
			mutate: func(r *CircuitIncidentRecord) {
				r.State = CircuitIncidentHalfOpen
				r.LeasePurpose = "w12c-other"
				r.LeaseID = "lease-1"
				r.LeaseUntilMS = &leaseUntil
			},
			check: func(t *testing.T, state CircuitState) {
				t.Helper()
				if state.Lease != nil || state.HalfOpenOrigin != "" {
					t.Fatalf("未知 purpose 不投影租约: %#v", state)
				}
			},
		},
		{
			name: "父 incident shadow",
			mutate: func(r *CircuitIncidentRecord) {
				r.ParentIncidentID = "parent-incident"
			},
			check: func(t *testing.T, state CircuitState) {
				t.Helper()
				if state.ShadowedByIncidentID != "parent-incident" {
					t.Fatalf("shadow 投影: %#v", state)
				}
			},
		},
		{
			name: "openUntil 与 nextTransition 与失败类别",
			mutate: func(r *CircuitIncidentRecord) {
				r.OpenUntilMS = &openUntil
				r.NextTransitionAtMS = &nextAt
				r.LastFailureClass = "w12c-class"
			},
			check: func(t *testing.T, state CircuitState) {
				t.Helper()
				if state.OpenedAtMS == nil || state.RetryAtMS == nil || *state.RetryAtMS != nextAt || state.FailureReason != "w12c-class" {
					t.Fatalf("时间戳投影: %#v", state)
				}
			},
		},
		{
			name: "确认失败证据",
			mutate: func(r *CircuitIncidentRecord) {
				r.ConfirmationFailureEvidenceKeys = []string{"aaaa"}
			},
			check: func(t *testing.T, state CircuitState) {
				t.Helper()
				if len(state.FailureEvidenceKeys) != 1 {
					t.Fatalf("证据投影: %#v", state)
				}
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			row := incidentRow(accountKey, accountScopeValue, CircuitIncidentOpen, 900)
			tc.mutate(&row)
			state, err := IncidentToRuntimeState(row, map[string]string{})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("期望错误 %s: %#v %v", tc.wantErr, state, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.check != nil {
				tc.check(t, state)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CircuitRecovery
// ---------------------------------------------------------------------------

func TestW12CRecoveryRecoverArms(t *testing.T) {
	ctx := context.Background()
	scope := w9hScope("acc-w12c-recover")
	// 相位白名单之外直接 skipped。
	stub := &w12cStubCircuitStore{}
	svc := w12cRecoveryService(t, stub, staticResolver(CircuitRecoveryProbeTarget{}, true), nil)
	outcome, leased, err := svc.recover(ctx, closedAccountCircuitState(scope, "7", 1, "tx", 1_000))
	if err != nil || outcome != circuitRecoverySkipped || leased {
		t.Fatalf("CLOSED 应跳过: %s %v %v", outcome, leased, err)
	}

	// acquire 错误。
	acquireErrStore := &w12cStubCircuitStore{acquireErr: errors.New("w12c: acquire boom")}
	acquireErrSvc := w12cRecoveryService(t, acquireErrStore, staticResolver(CircuitRecoveryProbeTarget{}, true), nil)
	if _, _, err := acquireErrSvc.recover(ctx, w12cSuspect(scope, 1, "7", 1_000)); err == nil {
		t.Fatal("acquire 错误必须暴露")
	}

	// acquire 被 fence / 跳过。
	fencedStore := &w12cStubCircuitStore{acquireStatus: CircuitMutationStaleGeneration}
	fencedSvc := w12cRecoveryService(t, fencedStore, staticResolver(CircuitRecoveryProbeTarget{}, true), nil)
	outcome, _, err = fencedSvc.recover(ctx, w12cSuspect(scope, 1, "7", 1_000))
	if err != nil || outcome != circuitRecoveryFenced {
		t.Fatalf("fenced=%s err=%v", outcome, err)
	}
	skipStore := &w12cStubCircuitStore{acquireStatus: CircuitMutationNotDue}
	skipSvc := w12cRecoveryService(t, skipStore, staticResolver(CircuitRecoveryProbeTarget{}, true), nil)
	outcome, _, err = skipSvc.recover(ctx, w12cSuspect(scope, 1, "7", 1_000))
	if err != nil || outcome != circuitRecoverySkipped {
		t.Fatalf("skipped=%s err=%v", outcome, err)
	}

	// resolver 错误 + release 错误 → join。
	releaseErrStore := &w12cStubCircuitStore{completeApplied: true}
	resolveErrStore := &w12cStubCircuitStore{acquireStatus: CircuitMutationApplied, completeErr: errors.New("w12c: release boom")}
	resolveErrSvc := w12cRecoveryService(t, resolveErrStore, func(context.Context, CircuitState) (CircuitRecoveryProbeTarget, bool, error) {
		return CircuitRecoveryProbeTarget{}, false, errors.New("w12c: resolve boom")
	}, nil)
	if _, _, err := resolveErrSvc.recover(ctx, w12cSuspect(scope, 1, "7", 1_000)); err == nil || !strings.Contains(err.Error(), "resolve boom") {
		t.Fatalf("resolve 错误必须暴露: %v", err)
	}
	_ = releaseErrStore

	// 目标缺失 → unknown（release 成功）。
	missingTargetStore := &w12cStubCircuitStore{acquireStatus: CircuitMutationApplied, completeApplied: true}
	missingSvc := w12cRecoveryService(t, missingTargetStore, staticResolver(CircuitRecoveryProbeTarget{}, false), nil)
	outcome, leased, err = missingSvc.recover(ctx, w12cSuspect(scope, 1, "7", 1_000))
	if err != nil || outcome != circuitRecoveryUnknown || !leased {
		t.Fatalf("missing target=%s %v %v", outcome, leased, err)
	}

	// revision 漂移 → replace → fenced。
	driftStore := &w12cStubCircuitStore{acquireStatus: CircuitMutationApplied, completeApplied: true}
	driftSvc := w12cRecoveryService(t, driftStore, staticResolver(CircuitRecoveryProbeTarget{DispatchRevision: "9"}, true), nil)
	outcome, _, err = driftSvc.recover(ctx, w12cSuspect(scope, 1, "7", 1_000))
	if err != nil || outcome != circuitRecoveryFenced {
		t.Fatalf("drift=%s err=%v", outcome, err)
	}
	// replace 错误。
	replaceErrStore := &w12cStubCircuitStore{acquireStatus: CircuitMutationApplied, replaceErr: errors.New("w12c: replace boom")}
	replaceErrSvc := w12cRecoveryService(t, replaceErrStore, staticResolver(CircuitRecoveryProbeTarget{DispatchRevision: "9"}, true), nil)
	if _, _, err := replaceErrSvc.recover(ctx, w12cSuspect(scope, 1, "7", 1_000)); err == nil {
		t.Fatal("replace 错误必须暴露")
	}
	// replace 非法结果 → skipped。
	replaceSkipSvc := w12cRecoveryService(t, &w12cReplaceStatusStore{inner: &w12cStubCircuitStore{acquireStatus: CircuitMutationApplied, completeApplied: true}, status: CircuitMutationNotDue}, staticResolver(CircuitRecoveryProbeTarget{DispatchRevision: "9"}, true), nil)
	outcome, _, err = replaceSkipSvc.recover(ctx, w12cSuspect(scope, 1, "7", 1_000))
	if err != nil || outcome != circuitRecoverySkipped {
		t.Fatalf("replace skip=%s err=%v", outcome, err)
	}

	// probe 错误 → release → join。
	probeErrStore := &w12cStubCircuitStore{acquireStatus: CircuitMutationApplied, completeApplied: true}
	probeErrSvc := w12cRecoveryService(t, probeErrStore, staticResolver(CircuitRecoveryProbeTarget{
		DispatchRevision: "7",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return TransportProbeOutcome{}, errors.New("w12c: probe boom")
		},
	}, true), nil)
	if _, _, err := probeErrSvc.recover(ctx, w12cSuspect(scope, 1, "7", 1_000)); err == nil || !strings.Contains(err.Error(), "probe boom") {
		t.Fatalf("probe 错误必须暴露: %v", err)
	}

	// complete 错误。
	completeErrStore := &w12cStubCircuitStore{acquireStatus: CircuitMutationApplied, completeErr: errors.New("w12c: complete boom")}
	completeErrSvc := w12cRecoveryService(t, completeErrStore, staticResolver(CircuitRecoveryProbeTarget{
		DispatchRevision: "7",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return framingCompleteOutcome(), nil
		},
	}, true), nil)
	if _, _, err := completeErrSvc.recover(ctx, w12cSuspect(scope, 1, "7", 1_000)); err == nil {
		t.Fatal("complete 错误必须暴露")
	}

	// complete 被 fence / 跳过。
	completeFenceStore := &w12cStubCircuitStore{acquireStatus: CircuitMutationApplied, completeStatus: CircuitMutationLeaseMismatch}
	completeFenceSvc := w12cRecoveryService(t, completeFenceStore, staticResolver(CircuitRecoveryProbeTarget{
		DispatchRevision: "7",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return framingCompleteOutcome(), nil
		},
	}, true), nil)
	outcome, _, err = completeFenceSvc.recover(ctx, w12cSuspect(scope, 1, "7", 1_000))
	if err != nil || outcome != circuitRecoveryFenced {
		t.Fatalf("complete fenced=%s err=%v", outcome, err)
	}
	completeSkipStore := &w12cStubCircuitStore{acquireStatus: CircuitMutationApplied, completeStatus: CircuitMutationCapacityExhausted}
	completeSkipSvc := w12cRecoveryService(t, completeSkipStore, staticResolver(CircuitRecoveryProbeTarget{
		DispatchRevision: "7",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return framingCompleteOutcome(), nil
		},
	}, true), nil)
	outcome, _, err = completeSkipSvc.recover(ctx, w12cSuspect(scope, 1, "7", 1_000))
	if err != nil || outcome != circuitRecoverySkipped {
		t.Fatalf("complete skipped=%s err=%v", outcome, err)
	}

	// canary 路径（OPEN due）：framing complete → recovering；transport incomplete。
	openDue := w12cOpen(scope, 2, "7", 1_000)
	openDue.RetryAtMS = int64Ptr(500)
	canaryStore := w7dMemoryStore(t, 8)
	if _, err := canaryStore.Restore(ctx, openDue, 1_000); err != nil {
		t.Fatal(err)
	}
	canarySvc := w12cRecoveryService(t, canaryStore, staticResolver(CircuitRecoveryProbeTarget{
		DispatchRevision: "7",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return framingCompleteOutcome(), nil
		},
	}, true), nil)
	due, err := canaryStore.ListDue(ctx, 1_000, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("due=%d err=%v", len(due), err)
	}
	outcome, leased, err = canarySvc.recover(ctx, due[0])
	if err != nil || outcome != circuitRecoveryFramingComplete || !leased {
		t.Fatalf("canary framing=%s %v %v", outcome, leased, err)
	}
	state, err := canaryStore.Get(ctx, scope, 2_000)
	if err != nil || state.Phase != CircuitPhaseRecovering {
		t.Fatalf("canary 后应 recovering: %#v err=%v", state, err)
	}

	// canary unknown 结果 → 回退原相位。
	unknownStore := w7dMemoryStore(t, 8)
	if _, err := unknownStore.Restore(ctx, openDue, 1_000); err != nil {
		t.Fatal(err)
	}
	unknownSvc := w12cRecoveryService(t, unknownStore, staticResolver(CircuitRecoveryProbeTarget{
		DispatchRevision: "7",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return TransportProbeOutcome{Kind: ProbeOutcomeUnknown, FailureKind: ProbeFailureTaskFailure}, nil
		},
	}, true), nil)
	due, _ = unknownStore.ListDue(ctx, 1_000, 10)
	outcome, _, err = unknownSvc.recover(ctx, due[0])
	if err != nil || outcome != circuitRecoveryUnknown {
		t.Fatalf("canary unknown=%s err=%v", outcome, err)
	}

	// canary transport incomplete → 恢复计数累计（未达阈值保持 RECOVERING）。
	transportStore := w7dMemoryStore(t, 8)
	if _, err := transportStore.Restore(ctx, openDue, 1_000); err != nil {
		t.Fatal(err)
	}
	transportSvc := w12cRecoveryService(t, transportStore, staticResolver(CircuitRecoveryProbeTarget{
		DispatchRevision: "7",
		Probe: func(context.Context) (TransportProbeOutcome, error) {
			return transportIncompleteOutcome(ProbeFailureTimeout, nil), nil
		},
	}, true), nil)
	due, _ = transportStore.ListDue(ctx, 1_000, 10)
	outcome, _, err = transportSvc.recover(ctx, due[0])
	if err != nil || outcome != circuitRecoveryTransportIncomplete {
		t.Fatalf("canary transport=%s err=%v", outcome, err)
	}

	// releaseUnknown 对 canary（非 suspect）走 CompleteCanary 错误。
	canaryReleaseErrStore := &w12cStubCircuitStore{completeErr: errors.New("w12c: canary release boom")}
	canaryReleaseErrSvc := w12cRecoveryService(t, canaryReleaseErrStore, func(context.Context, CircuitState) (CircuitRecoveryProbeTarget, bool, error) {
		return CircuitRecoveryProbeTarget{}, false, errors.New("w12c: resolve boom")
	}, nil)
	openLeased := w12cOpen(scope, 2, "7", 1_000)
	openLeased.Phase = CircuitPhaseOpen
	if err := canaryReleaseErrSvc.releaseUnknown(ctx, openLeased, "lease-1"); err == nil {
		t.Fatal("canary release 错误必须暴露")
	}
	// releaseUnknown 非 applied 非 fenced → 错误。
	notAppliedStore := &w12cStubCircuitStore{completeStatus: CircuitMutationCapacityExhausted}
	notAppliedSvc := w12cRecoveryService(t, notAppliedStore, staticResolver(CircuitRecoveryProbeTarget{}, true), nil)
	if err := notAppliedSvc.releaseUnknown(ctx, w12cSuspect(scope, 1, "7", 1_000), "lease-1"); err == nil {
		t.Fatal("release 非 applied 必须报错")
	}
}

// w12cReplaceStatusStore 仅覆写 ReplaceDispatchRevision 的返回状态，其余转发。
type w12cReplaceStatusStore struct {
	inner  *w12cStubCircuitStore
	status CircuitMutationStatus
}

func (s *w12cReplaceStatusStore) Get(ctx context.Context, scope CircuitScope, nowMS int64) (CircuitState, error) {
	return s.inner.Get(ctx, scope, nowMS)
}

func (s *w12cReplaceStatusStore) Restore(ctx context.Context, state CircuitState, nowMS int64) (CircuitMutationResult, error) {
	return s.inner.Restore(ctx, state, nowMS)
}

func (s *w12cReplaceStatusStore) ListDue(ctx context.Context, nowMS int64, limit int) ([]CircuitState, error) {
	return s.inner.ListDue(ctx, nowMS, limit)
}

func (s *w12cReplaceStatusStore) AcquireConfirmationLease(ctx context.Context, identity CircuitTransitionIdentity, lease CircuitLeaseSpec) (CircuitMutationResult, error) {
	return s.inner.AcquireConfirmationLease(ctx, identity, lease)
}

func (s *w12cReplaceStatusStore) AcquireCanaryLease(ctx context.Context, identity CircuitTransitionIdentity, lease CircuitLeaseSpec) (CircuitMutationResult, error) {
	return s.inner.AcquireCanaryLease(ctx, identity, lease)
}

func (s *w12cReplaceStatusStore) CompleteConfirmation(ctx context.Context, identity CircuitTransitionIdentity, leaseID string, completion CircuitCompletion) (CircuitMutationResult, error) {
	return s.inner.CompleteConfirmation(ctx, identity, leaseID, completion)
}

func (s *w12cReplaceStatusStore) CompleteCanary(ctx context.Context, identity CircuitTransitionIdentity, leaseID string, completion CircuitCompletion) (CircuitMutationResult, error) {
	return s.inner.CompleteCanary(ctx, identity, leaseID, completion)
}

func (s *w12cReplaceStatusStore) ClearAccountEscalationEvidence(ctx context.Context, accountRuntimeKey, dispatchRevision, evidenceID string, nowMS int64) (bool, error) {
	return s.inner.ClearAccountEscalationEvidence(ctx, accountRuntimeKey, dispatchRevision, evidenceID, nowMS)
}

func (s *w12cReplaceStatusStore) ReplaceDispatchRevision(context.Context, CircuitScope, string, string, int64) (CircuitMutationResult, error) {
	return CircuitMutationResult{Status: s.status}, nil
}

func (s *w12cReplaceStatusStore) ReplaceAccountDispatchRevision(ctx context.Context, key, revision, transition string, nowMS int64) (int64, error) {
	return s.inner.ReplaceAccountDispatchRevision(ctx, key, revision, transition, nowMS)
}

func ctxValue() context.Context { return context.Background() }

// w12cOpen 构造 OPEN 状态（BackoffAttempt=1）。
func w12cOpen(scope CircuitScope, generation int64, dispatchRevision string, nowMS int64) CircuitState {
	state := w12cSuspect(scope, generation, dispatchRevision, nowMS)
	state.Phase = CircuitPhaseOpen
	state.BackoffAttempt = 1
	return state
}

func TestW12CRecoverySweepCancelAndCounters(t *testing.T) {
	// ctx 取消 → Sweep 提前结束并返回取消错误。
	blocking := make(chan struct{})
	defer close(blocking)
	store := &w12cSweepBlockStore{release: blocking}
	svc := w12cRecoveryService(t, store, staticResolver(CircuitRecoveryProbeTarget{}, true), func(o *CircuitRecoveryServiceOptions) {
		o.Concurrency = 1
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := svc.Sweep(ctx)
	if err == nil || result.DueCount != 1 {
		t.Fatalf("取消应中止扫描: %#v %v", result, err)
	}

	// 计数分支。
	counters := CircuitRecoverySweepResult{}
	incrementSweepOutcome(&counters, circuitRecoveryFramingComplete)
	incrementSweepOutcome(&counters, circuitRecoveryTransportIncomplete)
	incrementSweepOutcome(&counters, circuitRecoveryUnknown)
	incrementSweepOutcome(&counters, circuitRecoveryFenced)
	incrementSweepOutcome(&counters, circuitRecoverySkipped)
	if counters.FramingCompleteCount != 1 || counters.TransportIncompleteCount != 1 || counters.UnknownCount != 1 || counters.FencedCount != 1 || counters.SkippedCount != 1 {
		t.Fatalf("计数不符: %+v", counters)
	}
	if NewRandomID() == "" {
		t.Fatal("随机 ID 不应为空")
	}
}

// w12cSweepBlockStore 的 recover 流程阻塞直到测试放行，用于触发 ctx 取消路径。
type w12cSweepBlockStore struct {
	CircuitStore
	release chan struct{}
	once    sync.Once
}

func (s *w12cSweepBlockStore) ListDue(context.Context, int64, int) ([]CircuitState, error) {
	return []CircuitState{w12cSuspect(accountScope("acc-w12c-sweep"), 1, "7", 0)}, nil
}

func (s *w12cSweepBlockStore) AcquireConfirmationLease(ctx context.Context, identity CircuitTransitionIdentity, lease CircuitLeaseSpec) (CircuitMutationResult, error) {
	<-ctx.Done()
	return CircuitMutationResult{}, ctx.Err()
}

func (s *w12cSweepBlockStore) AcquireCanaryLease(ctx context.Context, identity CircuitTransitionIdentity, lease CircuitLeaseSpec) (CircuitMutationResult, error) {
	<-ctx.Done()
	return CircuitMutationResult{}, ctx.Err()
}

func TestW12CRecoveryServiceValidationArms(t *testing.T) {
	if _, err := NewCircuitRecoveryService(nil, staticResolver(CircuitRecoveryProbeTarget{}, true), CircuitRecoveryServiceOptions{NowMS: func() int64 { return 1 }}); err == nil {
		t.Fatal("缺 store 必须拒绝")
	}
	if _, err := NewCircuitRecoveryService(&w12cStubCircuitStore{}, nil, CircuitRecoveryServiceOptions{NowMS: func() int64 { return 1 }}); err == nil {
		t.Fatal("缺 resolver 必须拒绝")
	}
	cases := []CircuitRecoveryServiceOptions{
		{BatchSize: -1, NowMS: func() int64 { return 1 }},
		{LeaseDurationMS: -1, NowMS: func() int64 { return 1 }},
		{Concurrency: -1, NowMS: func() int64 { return 1 }},
		{NowMS: nil},
	}
	for index, options := range cases {
		if _, err := NewCircuitRecoveryService(&w12cStubCircuitStore{}, staticResolver(CircuitRecoveryProbeTarget{}, true), options); err == nil {
			t.Fatalf("case %d 必须拒绝", index)
		}
	}
	// CurrentDispatchRevision。
	if got, ok := CurrentDispatchRevision(0); ok || got != "" {
		t.Fatal("非正数无 revision")
	}
	if got, ok := CurrentDispatchRevision(7); !ok || got != "7" {
		t.Fatal("正数返回字符串")
	}
	// ParseRecoveryRuntimeIdentity。
	owner, ok := ParseRecoveryRuntimeIdentity("acc-1")
	if !ok || owner.Kind != "owner" || owner.AccountID != "acc-1" {
		t.Fatalf("owner 解析: %#v %v", owner, ok)
	}
	authorized, ok := ParseRecoveryRuntimeIdentity("acc-1:authorized:sys:g:a")
	if !ok || authorized.Kind != "authorized" || authorized.AuthorizationID != "a" {
		t.Fatalf("authorized 解析: %#v %v", authorized, ok)
	}
	if _, ok := ParseRecoveryRuntimeIdentity(":authorized:sys:g:a"); ok {
		t.Fatal("空 account 必须拒绝")
	}
	if _, ok := ParseRecoveryRuntimeIdentity("acc:authorized:only-two"); ok {
		t.Fatal("段数不符必须拒绝")
	}
	if _, ok := ParseRecoveryRuntimeIdentity("   "); ok {
		t.Fatal("空键必须拒绝")
	}
	if RuntimeAccountIDFromKey("acc-1:extra") != "acc-1" || RuntimeAccountIDFromKey("acc-1") != "acc-1" {
		t.Fatal("runtime key 截取")
	}
	_ = fmt.Sprint
}
