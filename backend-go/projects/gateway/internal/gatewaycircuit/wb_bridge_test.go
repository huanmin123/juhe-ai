package gatewaycircuit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// wbControlPlaneDB 是 ControlPlaneDB 的可编排替身：记录调用并允许逐方法注入错误。
type wbControlPlaneDB struct {
	mu sync.Mutex

	incidents     map[string]IncidentRecord
	rebuildPages  []RebuildPage
	byRuntimeKeys []IncidentRecord

	errCompareAndSet    error
	errListForRebuild   error
	errListByKeys       error
	errGetByScope       error
	errClaimOutbox      error
	errAckOutbox        error
	errReleaseOutbox    error
	ackAcknowledged     bool
	getByScopeMissing   bool
	compareStatus       string
	compareIncident     *IncidentRecord
	compareDispatch     int64
	claimEvents         []OutboxEvent
	releaseErrorClasses []string

	compareCalls int
	loadCalls    int
	loadKeyCalls int
}

func wbNewFakeDB() *wbControlPlaneDB {
	return &wbControlPlaneDB{incidents: map[string]IncidentRecord{}}
}

func (db *wbControlPlaneDB) CompareAndSetIncident(context.Context, CompareAndSetIncidentInput) (CompareAndSetIncidentResult, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.compareCalls++
	if db.errCompareAndSet != nil {
		return CompareAndSetIncidentResult{}, db.errCompareAndSet
	}
	return CompareAndSetIncidentResult{Status: db.compareStatus, Incident: db.compareIncident, CurrentDispatchRevision: db.compareDispatch}, nil
}

func (db *wbControlPlaneDB) ListIncidentsForRebuild(_ context.Context, input RebuildPageInput) (RebuildPage, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.loadCalls++
	if db.errListForRebuild != nil {
		return RebuildPage{}, db.errListForRebuild
	}
	if len(db.rebuildPages) == 0 {
		return RebuildPage{}, nil
	}
	index := db.loadCalls - 1
	if index >= len(db.rebuildPages) {
		index = len(db.rebuildPages) - 1
	}
	return db.rebuildPages[index], nil
}

func (db *wbControlPlaneDB) ListIncidentsByRuntimeKeys(_ context.Context, input ListIncidentsByRuntimeKeysInput) ([]IncidentRecord, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.loadKeyCalls++
	if db.errListByKeys != nil {
		return nil, db.errListByKeys
	}
	return append([]IncidentRecord{}, db.byRuntimeKeys...), nil
}

func (db *wbControlPlaneDB) GetIncidentByScopeKey(_ context.Context, circuitScopeKey string) (*IncidentRecord, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.errGetByScope != nil {
		return nil, db.errGetByScope
	}
	if db.getByScopeMissing {
		return nil, nil
	}
	incident, ok := db.incidents[circuitScopeKey]
	if !ok {
		return nil, nil
	}
	return &incident, nil
}

func (db *wbControlPlaneDB) ClaimOutbox(context.Context, ClaimOutboxInput) ([]OutboxEvent, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.errClaimOutbox != nil {
		return nil, db.errClaimOutbox
	}
	return db.claimEvents, nil
}

func (db *wbControlPlaneDB) AckOutbox(context.Context, AckOutboxInput) (AckOutboxResult, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.errAckOutbox != nil {
		return AckOutboxResult{}, db.errAckOutbox
	}
	return AckOutboxResult{Acknowledged: db.ackAcknowledged}, nil
}

func (db *wbControlPlaneDB) ReleaseOutboxForReplay(_ context.Context, input ReleaseOutboxInput) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.releaseErrorClasses = append(db.releaseErrorClasses, input.ErrorClass)
	return db.errReleaseOutbox
}

func wbAccountIncident(key string) IncidentRecord {
	scope := Scope{Kind: ScopeKindAccount, AccountRuntimeKey: key}
	return IncidentRecord{
		CircuitScopeKey:              MustScopeKey(scope),
		AccountID:                    "acc",
		AccountRuntimeKey:            key,
		ScopeKind:                    IncidentScopeKindAccount,
		IncidentID:                   "inc-1",
		State:                        IncidentStateSuspect,
		Generation:                   1,
		DispatchRevision:             1,
		LedgerRevision:               1,
		TransitionID:                 "t1",
		UpdatedAtMs:                  1_000,
		CreatedAtMs:                  1_000,
		ConfirmationFailuresRequired: 2,
		NextTransitionAtMs:           int64Ptr(9_000),
		LastFailureClass:             strPtr(FailureClassConnectFailed),
	}
}

func wbNewBridgeForTest(t *testing.T, db *wbControlPlaneDB, store Store, mutates ...func(*BridgeOptions)) *Bridge {
	t.Helper()
	options := BridgeOptions{
		Store: store, DB: db, OwnerID: "wb-bridge",
		Now:   func() int64 { return 1_000 },
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	if len(mutates) > 0 && mutates[0] != nil {
		mutates[0](&options)
	}
	bridge, err := NewBridge(options)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	t.Cleanup(bridge.Close)
	return bridge
}

func wbWaitFor(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.After(timeout)
	for !condition() {
		select {
		case <-deadline:
			t.Fatal(message)
		default:
		}
	}
}

// 构造契约：store/DB 与数值门禁必须齐全。
func TestWBBridgeConstructionValidation(t *testing.T) {
	if _, err := NewBridge(BridgeOptions{DB: wbNewFakeDB()}); err == nil {
		t.Fatal("缺 store 必须报错")
	}
	if _, err := NewBridge(BridgeOptions{Store: newNonExpiringMemoryStore(t)}); err == nil {
		t.Fatal("缺 DB 必须报错")
	}
	store := newNonExpiringMemoryStore(t)
	for name, mutate := range map[string]func(*BridgeOptions){
		"negative retry":      func(o *BridgeOptions) { o.RetryDelayMs = -1 },
		"negative attempts":   func(o *BridgeOptions) { o.MaxPersistAttempts = -1 },
		"negative retention":  func(o *BridgeOptions) { o.ClosedRetentionMs = -1 },
		"negative page size":  func(o *BridgeOptions) { o.RebuildPageSize = -1 },
		"negative max pages":  func(o *BridgeOptions) { o.RebuildMaxPages = -1 },
		"negative page wait":  func(o *BridgeOptions) { o.RebuildPageTimeoutMs = -1 },
		"negative total wait": func(o *BridgeOptions) { o.RebuildTotalTimeoutMs = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			options := BridgeOptions{Store: store, DB: wbNewFakeDB()}
			mutate(&options)
			if _, err := NewBridge(options); err == nil {
				t.Fatalf("%s 必须报错", name)
			}
		})
	}
}

// 就绪门禁契约：冷启动未完成前不就绪；Rebuild 成功后全局就绪，
// 账户级持久化失败必须阻塞该账户。
func TestWBBridgeReadyGateAndAccountLoad(t *testing.T) {
	db := wbNewFakeDB()
	incident := wbAccountIncident("acc")
	db.rebuildPages = []RebuildPage{{Items: []IncidentRecord{incident}, NextCursor: nil}}
	db.byRuntimeKeys = []IncidentRecord{incident}
	store := newNonExpiringMemoryStore(t)
	bridge := wbNewBridgeForTest(t, db, store)
	if bridge.IsReady() {
		t.Fatal("冷启动前不得就绪")
	}
	if ready, err := bridge.IsAccountReady("acc"); err != nil || ready {
		t.Fatalf("冷启动前账户就绪 = (%v, %v)", ready, err)
	}
	if _, err := bridge.EnsureAccountReady(context.Background(), " "); err == nil {
		t.Fatal("空运行键必须报错")
	}
	result, err := bridge.Rebuild(context.Background())
	if err != nil || result.Loaded != 1 || result.Blocked {
		t.Fatalf("rebuild = %+v err=%v", result, err)
	}
	if !bridge.IsReady() {
		t.Fatal("重建成功后必须全局就绪")
	}
	ready, err := bridge.EnsureAccountReady(context.Background(), "acc")
	if err != nil || !ready {
		t.Fatalf("账户加载后必须就绪 = (%v, %v)", ready, err)
	}
	// 账户加载单飞：第二次调用走已就绪短路，不再触发加载。
	readyCalls := db.loadKeyCalls
	if ready, err := bridge.EnsureAccountReady(context.Background(), "acc"); err != nil || !ready {
		t.Fatalf("二次就绪 = (%v, %v)", ready, err)
	}
	if db.loadKeyCalls != readyCalls {
		t.Fatalf("就绪短路不得再次加载: %d -> %d", readyCalls, db.loadKeyCalls)
	}
}

// 账户加载失败路径：列表错误或容量不足必须保持未就绪。
func TestWBBridgeAccountLoadFailures(t *testing.T) {
	db := wbNewFakeDB()
	db.errListByKeys = errors.New("列表失败")
	bridge := wbNewBridgeForTest(t, db, newNonExpiringMemoryStore(t))
	// 全局重建未就绪时账户级加载才会真正执行。
	if ready, err := bridge.EnsureAccountReady(context.Background(), "acc"); err != nil || ready {
		t.Fatalf("列表失败必须保持未就绪 = (%v, %v)", ready, err)
	}
	// 运行键不匹配的账本行必须失败关闭。
	db.errListByKeys = nil
	foreign := wbAccountIncident("other")
	db.byRuntimeKeys = []IncidentRecord{foreign}
	if ready, err := bridge.EnsureAccountReady(context.Background(), "acc"); err != nil || ready {
		t.Fatalf("外键账本必须失败关闭 = (%v, %v)", ready, err)
	}
}

// 账户加载失败必须进入有界退避，避免每个请求都重复打 DB；退避到期后
// 仍会自动重试，成功后清除失败状态。
func TestWBBridgeAccountLoadFailureBackoffAndRecovery(t *testing.T) {
	now := int64(1_000)
	db := wbNewFakeDB()
	db.errListByKeys = errors.New("列表失败")
	var failures []ReadinessFailure
	bridge, err := NewBridge(BridgeOptions{
		Store: newNonExpiringMemoryStore(t), DB: db,
		Now: func() int64 { return now }, RetryDelayMs: 1_000,
		OnReadinessFailure: func(failure ReadinessFailure) { failures = append(failures, failure) },
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	t.Cleanup(bridge.Close)

	if ready, err := bridge.EnsureAccountReady(context.Background(), "acc"); err != nil || ready {
		t.Fatalf("首次列表失败 = (%v, %v)", ready, err)
	}
	if ready, err := bridge.EnsureAccountReady(context.Background(), "acc"); err != nil || ready {
		t.Fatalf("退避期间必须保持未就绪 = (%v, %v)", ready, err)
	}
	if db.loadKeyCalls != 1 {
		t.Fatalf("退避期间不得重复查询 DB: %d", db.loadKeyCalls)
	}
	if len(failures) != 1 || failures[0].Reason != "account_load_failed" || failures[0].RetryAtMs != 2_000 {
		t.Fatalf("失败诊断 = %+v", failures)
	}

	// 模拟依赖恢复；到达 retryAt 后下一次请求应重新加载并成功。
	db.mu.Lock()
	db.errListByKeys = nil
	db.byRuntimeKeys = nil
	db.mu.Unlock()
	now = 2_000
	if ready, err := bridge.EnsureAccountReady(context.Background(), "acc"); err != nil || !ready {
		t.Fatalf("退避到期后必须恢复 = (%v, %v)", ready, err)
	}
	if db.loadKeyCalls != 2 {
		t.Fatalf("恢复后应恰好重试一次: %d", db.loadKeyCalls)
	}
}

// 重建失败矩阵：分页失败、游标回退、容量耗尽都必须 Blocked 且带原因。
func TestWBBridgeRebuildFailureReasons(t *testing.T) {
	t.Run("page error", func(t *testing.T) {
		db := wbNewFakeDB()
		db.errListForRebuild = errors.New("分页失败")
		var failures []ReadinessFailure
		bridge := wbNewBridgeForTest(t, db, newNonExpiringMemoryStore(t), func(options *BridgeOptions) {
			options.OnReadinessFailure = func(failure ReadinessFailure) { failures = append(failures, failure) }
		})
		result, err := bridge.Rebuild(context.Background())
		if err != nil || !result.Blocked || result.Reason != RebuildReasonRebuildFailed {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if len(failures) != 1 || failures[0].Operation != "rebuild" || failures[0].Reason != RebuildReasonRebuildFailed {
			t.Fatalf("重建失败诊断 = %+v", failures)
		}
	})
	t.Run("invalid cursor", func(t *testing.T) {
		db := wbNewFakeDB()
		cursor := &RebuildCursor{UpdatedAtMs: 1, CircuitScopeKey: "a"}
		db.rebuildPages = []RebuildPage{{
			Items:      []IncidentRecord{wbAccountIncident("acc")},
			NextCursor: cursor,
		}, {
			Items:      nil,
			NextCursor: &RebuildCursor{UpdatedAtMs: 1, CircuitScopeKey: "a"},
		}}
		bridge := wbNewBridgeForTest(t, db, newNonExpiringMemoryStore(t))
		result, err := bridge.Rebuild(context.Background())
		if err != nil || !result.Blocked || result.Reason != RebuildReasonInvalidCursor {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("capacity exhausted", func(t *testing.T) {
		db := wbNewFakeDB()
		items := make([]IncidentRecord, 0, 3)
		for index := 0; index < 3; index++ {
			items = append(items, wbAccountIncident(fmt.Sprintf("acc-%d", index)))
		}
		db.rebuildPages = []RebuildPage{{Items: items}}
		store := newTestMemoryStore(t, 1, func() int64 { return 1_000 })
		bridge := wbNewBridgeForTest(t, db, store)
		result, err := bridge.Rebuild(context.Background())
		if err != nil || !result.Blocked || result.Reason != RebuildReasonCapacityExhausted {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}

// 对账契约：全局就绪后按页回放账本；未就绪或重建中为 no-op；limit 非法报错。
func TestWBBridgeReconcileActive(t *testing.T) {
	db := wbNewFakeDB()
	incident := wbAccountIncident("acc")
	db.rebuildPages = []RebuildPage{{Items: []IncidentRecord{incident}}}
	db.byRuntimeKeys = []IncidentRecord{incident}
	bridge := wbNewBridgeForTest(t, db, newNonExpiringMemoryStore(t))
	repaired, err := bridge.ReconcileActive(context.Background(), 10)
	if err != nil || repaired != 0 {
		t.Fatalf("未就绪对账 = (%d, %v)", repaired, err)
	}
	if _, err := bridge.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	db.rebuildPages = []RebuildPage{{Items: []IncidentRecord{wbAccountIncident("acc"), wbAccountIncident("acc-b")}, NextCursor: &RebuildCursor{UpdatedAtMs: 2, CircuitScopeKey: "next"}}}
	repaired, err = bridge.ReconcileActive(context.Background(), 10)
	if err != nil || repaired != 2 {
		t.Fatalf("对账修复数 = (%d, %v)", repaired, err)
	}
	if _, err := bridge.ReconcileActive(context.Background(), 0); err == nil {
		t.Fatal("非法 limit 必须报错")
	}
	db.errListForRebuild = errors.New("分页失败")
	if _, err := bridge.ReconcileActive(context.Background(), 10); err == nil {
		t.Fatal("分页失败必须上抛")
	}
}

// 观察持久化契约：Observe 合并状态并经 persistWithRetry 落账本；
// 持久化失败会标记账户并安排重试。
func TestWBBridgeObservePersistsThroughRetry(t *testing.T) {
	db := wbNewFakeDB()
	db.compareStatus = CASApplied
	db.compareDispatch = 1
	bridge := wbNewBridgeForTest(t, db, newNonExpiringMemoryStore(t))
	scope := accountScope("acc")
	state := ClosedState(scope, "1", 0, "t1", 1_000)
	bridge.Observe(scope, state)
	wbWaitFor(t, 2*time.Second, func() bool {
		db.mu.Lock()
		defer db.mu.Unlock()
		return db.compareCalls > 0
	}, "观察状态未触发账本写入")
	if _, err := bridge.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ready, err := bridge.IsAccountReady("acc"); err != nil || !ready {
		t.Fatalf("成功持久化后账户必须就绪 = (%v, %v)", ready, err)
	}
	// 持久化失败：CompareAndSet 持续报错 → 账户标记失败并安排重试。
	db.mu.Lock()
	db.errCompareAndSet = errors.New("账本写入失败")
	db.compareCalls = 0
	db.mu.Unlock()
	bridge.Observe(scope, ClosedState(scope, "1", 1, "t2", 2_000))
	wbWaitFor(t, 2*time.Second, func() bool {
		db.mu.Lock()
		defer db.mu.Unlock()
		return db.compareCalls > 0
	}, "失败后必须仍然尝试写入")
	// 失败标记由后台 worker 写入，必须等待其完成而不是立即断言。
	wbWaitFor(t, 2*time.Second, func() bool {
		ready, err := bridge.IsAccountReady("acc")
		return err == nil && !ready
	}, "持久化失败必须阻塞账户")
	bridge.Close()
}

// ProjectPending 契约：outbox 事件投影后确认；投影失败按错误类别释放重放。
func TestWBBridgeProjectPending(t *testing.T) {
	incident := wbAccountIncident("acc")
	t.Run("acknowledged incident projection", func(t *testing.T) {
		db := wbNewFakeDB()
		db.claimEvents = []OutboxEvent{{
			EventID: "evt-1", ProjectionKey: "p1", EventType: OutboxEventTypeIncidentChanged,
			AccountID: "acc", AccountRuntimeKey: "acc", CircuitScopeKey: strPtr(incident.CircuitScopeKey),
			TransitionID: "t1", DispatchRevision: 1,
		}}
		db.incidents[incident.CircuitScopeKey] = incident
		db.ackAcknowledged = true
		bridge := wbNewBridgeForTest(t, db, newNonExpiringMemoryStore(t))
		acknowledged, err := bridge.ProjectPending(context.Background(), 1)
		if err != nil || acknowledged != 1 {
			t.Fatalf("acknowledged = (%d, %v)", acknowledged, err)
		}
	})
	t.Run("dispatch revision event", func(t *testing.T) {
		db := wbNewFakeDB()
		db.claimEvents = []OutboxEvent{{
			EventID: "evt-2", ProjectionKey: "p2", EventType: OutboxEventTypeDispatchRevisionChanged,
			AccountRuntimeKey: "acc", TransitionID: "t2", DispatchRevision: 7,
		}}
		bridge := wbNewBridgeForTest(t, db, newNonExpiringMemoryStore(t))
		if _, err := bridge.ProjectPending(context.Background(), 1); err != nil {
			t.Fatalf("dispatch 事件投影失败: %v", err)
		}
	})
	t.Run("missing ledger incident", func(t *testing.T) {
		db := wbNewFakeDB()
		db.getByScopeMissing = true
		db.claimEvents = []OutboxEvent{{
			EventID: "evt-3", ProjectionKey: "p3", EventType: OutboxEventTypeIncidentChanged,
			AccountRuntimeKey: "acc", CircuitScopeKey: strPtr("missing"), TransitionID: "t3",
		}}
		bridge := wbNewBridgeForTest(t, db, newNonExpiringMemoryStore(t))
		if _, err := bridge.ProjectPending(context.Background(), 1); err != nil {
			t.Fatalf("缺账本事件必须被释放而不是中断: %v", err)
		}
		if len(db.releaseErrorClasses) != 1 {
			t.Fatalf("release calls = %v", db.releaseErrorClasses)
		}
	})
	t.Run("invalid limit and claim error", func(t *testing.T) {
		db := wbNewFakeDB()
		bridge := wbNewBridgeForTest(t, db, newNonExpiringMemoryStore(t))
		if _, err := bridge.ProjectPending(context.Background(), 0); err == nil {
			t.Fatal("非法 limit 必须报错")
		}
		db.errClaimOutbox = errors.New("认领失败")
		if _, err := bridge.ProjectPending(context.Background(), 1); err == nil {
			t.Fatal("认领失败必须上抛")
		}
	})
}

// 纯函数契约：错误分类、游标比较、摘要优先级与运行键集合比较。
func TestWBBridgePureHelpers(t *testing.T) {
	if classifyError(nil) != "projector_error" {
		t.Fatal("nil 错误必须回退 projector_error")
	}
	if got := classifyError(errors.New("x")); got != "errorString" {
		t.Fatalf("errors.New 分类 = %q", got)
	}

	if got := classifyError(fmt.Errorf("wrapped")); got != "errorString" {
		t.Fatalf("wrap 分类 = %q", got)
	}
	if compareCursor(&RebuildCursor{UpdatedAtMs: 1}, &RebuildCursor{UpdatedAtMs: 2}) >= 0 {
		t.Fatal("时间更早的游标必须小于后者")
	}
	if compareCursor(&RebuildCursor{UpdatedAtMs: 1, CircuitScopeKey: "a"}, &RebuildCursor{UpdatedAtMs: 1, CircuitScopeKey: "b"}) >= 0 {
		t.Fatal("同刻游标必须按键序比较")
	}
	if !sameStringSet([]string{"a", "b"}, []string{"b", "a"}) || sameStringSet([]string{"a"}, []string{"a", "a"}) || sameStringSet([]string{"a"}, []string{"b"}) {
		t.Fatal("sameStringSet 语义不正确")
	}
	if !incidentIsNewerThanRuntimeState(&IncidentRecord{DispatchRevision: 2}, State{DispatchRevision: "1"}) {
		t.Fatal("更大账本修订必须更新")
	}
	if incidentIsNewerThanRuntimeState(&IncidentRecord{DispatchRevision: 1, Generation: 1}, State{DispatchRevision: "1", Generation: 2}) {
		t.Fatal("更小代际不得视为更新")
	}
	incidentKeys := incidentScopeKeyMapFromRuntimeState(State{
		Scope:            accountScope("acc"),
		ChildIncidentIDs: stringList{"inc-1", "inc-2"},
		ChildScopeKeys:   stringList{"sk-1", ""},
	})
	if len(incidentKeys) != 1 {
		t.Fatalf("运行态子作用域映射 = %#v", incidentKeys)
	}
	if accountID, err := accountIDFromRuntimeKey("acc:authorized"); err != nil || accountID != "acc" {
		t.Fatalf("账户前缀 = %q err=%v", accountID, err)
	}
	if _, err := accountIDFromRuntimeKey(" "); err == nil {
		t.Fatal("空运行键必须报错")
	}
	if classifyFailure("Connect timeout") != FailureClassTimeoutBeforeComplete || classifyFailure("read failed") != FailureClassReadInterrupted || classifyFailure("policy breach") != FailureClassExplicitPolicy || classifyFailure("other") != FailureClassIncompleteResponse {
		t.Fatal("失败分类不正确")
	}
	if got := runtimePhase(IncidentStatePersisting); got != PhaseOpen {
		t.Fatalf("persisting 相位 = %s", got)
	}
	if got := PublicSummaryOf(nil); got.Status != PublicSummaryStatusNormal {
		t.Fatalf("空摘要 = %+v", got)
	}
}

// 公共摘要契约：OPEN 优先级最高，其次 HALF_OPEN/SUSPECT，再次 RECOVERING；
// nextCheckAt 取最小的下次迁移时间。
func TestWBPublicSummariesFromIncidents(t *testing.T) {
	incidents := []IncidentRecord{
		{AccountRuntimeKey: "acc", State: IncidentStateSuspect, UpdatedAtMs: 2_000},
		{AccountRuntimeKey: "acc", State: IncidentStateOpen, UpdatedAtMs: 3_000, LastFailureClass: strPtr(FailureClassConnectFailed), NextTransitionAtMs: int64Ptr(8_000)},
		{AccountRuntimeKey: "acc", State: IncidentStateRecovering, UpdatedAtMs: 1_000, NextTransitionAtMs: int64Ptr(5_000)},
	}
	summaries := PublicSummariesFromIncidents([]string{"acc", "acc", " ", "other"}, incidents)
	if len(summaries) != 2 {
		t.Fatalf("keys = %#v", summaries)
	}
	head := summaries["acc"]
	if head.Status != PublicSummaryStatusAvoided || head.Reason != FailureClassConnectFailed || head.NextCheckAt == "" {
		t.Fatalf("acc 摘要 = %+v", head)
	}
	if summaries["other"].Status != PublicSummaryStatusNormal {
		t.Fatalf("other 摘要 = %+v", summaries["other"])
	}
	if err := errors.New("列表失败"); err != nil {
		db := wbNewFakeDB()
		db.errListByKeys = err
		if _, err := LoadPublicAccountCircuitSummaries(context.Background(), db, []string{"acc"}); err == nil {
			t.Fatal("列表失败必须上抛")
		}
	}
}
