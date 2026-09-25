package gatewaycircuit

// 缺陷 E（熔断观测断链）：Bridge.OnPersistFailure 钩子的行为锚点。持久化在
// worker goroutine 内失败（重试耗尽）时只上报诊断并保留 pending 重试，绝不
// 影响 Observe 调用方（请求热路径）与其他 scope；成功路径不得误报。

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// w17eCapturingDB 按 mode 返回结果/错误并记录 CAS 调用（计数与标记均为
// atomic：bridge worker goroutine 写、测试 goroutine 读）。
type w17eCapturingDB struct {
	mode      string // "applied" | "error"
	calls     chan CompareAndSetIncidentInput
	callsGot  atomic.Int64
	claimOut  []OutboxEvent
	claimErr  error
	acked     bool
	ackedGot  atomic.Int64
	released  atomic.Int64
	releaseOk bool
}

func (m *w17eCapturingDB) CompareAndSetIncident(_ context.Context, input CompareAndSetIncidentInput) (CompareAndSetIncidentResult, error) {
	m.callsGot.Add(1)
	if m.calls != nil {
		select {
		case m.calls <- input:
		default:
		}
	}
	if m.mode == "error" {
		return CompareAndSetIncidentResult{}, errors.New("w17e persist failure")
	}
	return CompareAndSetIncidentResult{
		Status: CASApplied,
		Incident: &IncidentRecord{
			CircuitScopeKey:   input.CircuitScopeKey,
			AccountID:         input.AccountID,
			AccountRuntimeKey: input.AccountRuntimeKey,
			ScopeKind:         input.ScopeKind,
			IncidentID:        input.IncidentID,
			State:             input.State,
			Generation:        input.Generation,
			DispatchRevision:  input.DispatchRevision,
			LedgerRevision:    1,
			TransitionID:      input.TransitionID,
			CreatedAtMs:       1,
			UpdatedAtMs:       2,
		},
		CurrentDispatchRevision: input.DispatchRevision,
	}, nil
}

func (m *w17eCapturingDB) ListIncidentsForRebuild(context.Context, RebuildPageInput) (RebuildPage, error) {
	return RebuildPage{}, nil
}

func (m *w17eCapturingDB) ListIncidentsByRuntimeKeys(_ context.Context, _ ListIncidentsByRuntimeKeysInput) ([]IncidentRecord, error) {
	return nil, nil
}

func (m *w17eCapturingDB) GetIncidentByScopeKey(context.Context, string) (*IncidentRecord, error) {
	return nil, nil
}

func (m *w17eCapturingDB) ClaimOutbox(context.Context, ClaimOutboxInput) ([]OutboxEvent, error) {
	if m.claimErr != nil {
		return nil, m.claimErr
	}
	return m.claimOut, nil
}

func (m *w17eCapturingDB) AckOutbox(context.Context, AckOutboxInput) (AckOutboxResult, error) {
	m.ackedGot.Add(1)
	return AckOutboxResult{Acknowledged: m.acked}, nil
}

func (m *w17eCapturingDB) ReleaseOutboxForReplay(context.Context, ReleaseOutboxInput) error {
	m.released.Add(1)
	return nil
}

func mustW17eStore(t *testing.T) Store {
	t.Helper()
	store, err := NewMemoryStore(MemoryStoreOptions{Capacity: 128})
	if err != nil {
		t.Fatalf("create memory store: %v", err)
	}
	return store
}

func w17eWaitFor(t *testing.T, description string, probe func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if probe() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

// w17eSuspectObservation 在给定 store 上走一次真实 SUSPECT 转换，返回待
// 观测的 scope/state（与生产 Observe 输入同源）。
func w17eSuspectObservation(t *testing.T, store Store, accountRuntimeKey string) (Scope, State) {
	t.Helper()
	scope := Scope{Kind: ScopeKindAccount, AccountRuntimeKey: accountRuntimeKey}
	nowMs := int64(1_000)
	suspect, err := store.Suspect(context.Background(), SuspectInput{
		Scope:            scope,
		DispatchRevision: "1",
		TransitionID:     "w17e-suspect-" + accountRuntimeKey,
		Reason:           "transport:connect refused",
		NowMs:            &nowMs,
	})
	if err != nil {
		t.Fatalf("suspect: %v", err)
	}
	if suspect.Status != MutationApplied {
		t.Fatalf("suspect status = %s, want applied", suspect.Status)
	}
	return scope, suspect.State
}

// OnPersistFailureReportsExhaustedPersist：持久化重试耗尽时钩子收到
// scope 维度诊断（scopeKey / accountRuntimeKey / 非 nil 错误），Observe
// 调用方不受影响。
func TestOnPersistFailureReportsExhaustedPersist(t *testing.T) {
	store := mustW17eStore(t)
	scope, state := w17eSuspectObservation(t, store, "w17e-failing-account")
	db := &w17eCapturingDB{mode: "error"}
	failures := make(chan PersistFailure, 16)
	bridge, err := NewBridge(BridgeOptions{
		Store:            store,
		DB:               db,
		RetryDelayMs:     1,
		OnPersistFailure: func(failure PersistFailure) { failures <- failure },
	})
	if err != nil {
		t.Fatalf("create bridge: %v", err)
	}
	defer bridge.Close()
	bridge.Observe(scope, state)
	w17eWaitFor(t, "persist failure report", func() bool {
		select {
		case failure := <-failures:
			if failure.ScopeKey != state.ScopeKey {
				t.Fatalf("failure.ScopeKey = %q, want %q", failure.ScopeKey, state.ScopeKey)
			}
			if failure.AccountRuntimeKey != scope.AccountRuntimeKey {
				t.Fatalf("failure.AccountRuntimeKey = %q, want %q", failure.AccountRuntimeKey, scope.AccountRuntimeKey)
			}
			if failure.Err == nil {
				t.Fatal("failure.Err must carry the persistence error")
			}
			return true
		default:
			return false
		}
	})
}

// OnPersistFailureNotInvokedOnApplied：持久化成功时钩子不得误报。
func TestOnPersistFailureNotInvokedOnApplied(t *testing.T) {
	store := mustW17eStore(t)
	scope, state := w17eSuspectObservation(t, store, "w17e-applied-account")
	db := &w17eCapturingDB{mode: "applied"}
	invoked := &atomic.Bool{}
	bridge, err := NewBridge(BridgeOptions{
		Store:            store,
		DB:               db,
		RetryDelayMs:     1,
		OnPersistFailure: func(PersistFailure) { invoked.Store(true) },
	})
	if err != nil {
		t.Fatalf("create bridge: %v", err)
	}
	defer bridge.Close()
	bridge.Observe(scope, state)
	w17eWaitFor(t, "CAS call", func() bool { return db.callsGot.Load() > 0 })
	// 给失败误报留一个宽限窗口（worker 已完成本轮 persist）。
	time.Sleep(50 * time.Millisecond)
	if invoked.Load() {
		t.Fatal("OnPersistFailure must not fire when persistence applies")
	}
}

// OnPersistFailureNilHookKeepsSilentRetry：未装配钩子时失败路径保持既有
// 静默重试行为（无 panic；失败 scope 留在 pending 由 backoff 重试）。
func TestOnPersistFailureNilHookKeepsSilentRetry(t *testing.T) {
	store := mustW17eStore(t)
	scope, state := w17eSuspectObservation(t, store, "w17e-silent-account")
	db := &w17eCapturingDB{mode: "error"}
	bridge, err := NewBridge(BridgeOptions{Store: store, DB: db, RetryDelayMs: 1})
	if err != nil {
		t.Fatalf("create bridge: %v", err)
	}
	bridge.Observe(scope, state)
	w17eWaitFor(t, "first CAS attempt", func() bool { return db.callsGot.Load() > 0 })
	bridge.Close()
}

// FirstInsertPersistsNilExpectedLedgerRevision：首次插入（无既有 incident，
// bridge 侧 ledgerRevisions 未命中）时 buildPersistIncidentInput 产出的
// ExpectedLedgerRevision 必须为 nil（store 契约的"新行插入围栏"，
// bridge.go buildPersistIncidentInput 的 Node 对齐注释同键）。恒非 nil 会让
// 首插永远 cas_conflict → 重试耗尽（缺陷 E 接线实测发现的 ledger 围栏缺陷，
// 本断言防止其回退——回退时表现为落行超时，间接信号不够直接）。
func TestFirstInsertPersistsNilExpectedLedgerRevision(t *testing.T) {
	store := mustW17eStore(t)
	scope, state := w17eSuspectObservation(t, store, "w17e-first-insert-account")
	db := &w17eCapturingDB{mode: "applied", calls: make(chan CompareAndSetIncidentInput, 16)}
	bridge, err := NewBridge(BridgeOptions{Store: store, DB: db, RetryDelayMs: 1})
	if err != nil {
		t.Fatalf("create bridge: %v", err)
	}
	defer bridge.Close()
	bridge.Observe(scope, state)
	w17eWaitFor(t, "first CAS call", func() bool { return db.callsGot.Load() > 0 })
	select {
	case input := <-db.calls:
		if input.CircuitScopeKey != state.ScopeKey {
			t.Fatalf("captured scope = %q, want %q", input.CircuitScopeKey, state.ScopeKey)
		}
		if input.ExpectedLedgerRevision != nil {
			t.Fatalf("首次插入 ExpectedLedgerRevision = %d, 必须为 nil（新行插入围栏）",
				*input.ExpectedLedgerRevision)
		}
	default:
		t.Fatal("CAS input 未被捕获")
	}
}
