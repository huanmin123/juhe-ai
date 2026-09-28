package gatewaycircuit

// BUG-0222 回归锚点：Bridge 默认 newTimer 的 stop 契约。retryPendingImmediately
// （rebuild 尾部抢跑结算）会遍历调用 retryBackoffs 中 timer 的 stop 并清理；
// 若 stop 只调 timer.Stop 而不令 done 可读，scheduleScopeRetry 布防的 goroutine
// 会永久阻塞在 select <-done 上（goroutine 泄漏，直到 Bridge.Close）。契约出处：
// wait.go WaitCoordinatorOptions.NewTimer 注释与 WaitCoordinator 默认实现。

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// bug0222DB 最小 CAS 计数桩：worker goroutine 写计数、测试 goroutine 读，
// 其余 ControlPlaneDB 方法返回零值（本测试不触达）。
type bug0222DB struct {
	callsGot atomic.Int64
}

func (m *bug0222DB) CompareAndSetIncident(_ context.Context, input CompareAndSetIncidentInput) (CompareAndSetIncidentResult, error) {
	m.callsGot.Add(1)
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

func (m *bug0222DB) ListIncidentsForRebuild(context.Context, RebuildPageInput) (RebuildPage, error) {
	return RebuildPage{}, nil
}

func (m *bug0222DB) ListIncidentsByRuntimeKeys(context.Context, ListIncidentsByRuntimeKeysInput) ([]IncidentRecord, error) {
	return nil, nil
}

func (m *bug0222DB) GetIncidentByScopeKey(context.Context, string) (*IncidentRecord, error) {
	return nil, nil
}

func (m *bug0222DB) ClaimOutbox(context.Context, ClaimOutboxInput) ([]OutboxEvent, error) {
	return nil, nil
}

func (m *bug0222DB) AckOutbox(context.Context, AckOutboxInput) (AckOutboxResult, error) {
	return AckOutboxResult{Acknowledged: true}, nil
}

func (m *bug0222DB) ReleaseOutboxForReplay(context.Context, ReleaseOutboxInput) error {
	return nil
}

func bug0222NewBridge(t *testing.T, retryDelayMs int64) (*Bridge, *bug0222DB) {
	t.Helper()
	store, err := NewMemoryStore(MemoryStoreOptions{Capacity: 128})
	if err != nil {
		t.Fatalf("create memory store: %v", err)
	}
	db := &bug0222DB{}
	bridge, err := NewBridge(BridgeOptions{
		Store:        store,
		DB:           db,
		RetryDelayMs: retryDelayMs,
	})
	if err != nil {
		t.Fatalf("create bridge: %v", err)
	}
	return bridge, db
}

// TestBug0222DefaultNewTimerStopMakesDoneReadable：默认 newTimer（生产
// NewBridge 未注入 NewTimer 时生效，即 BUG-0222 的违约现场）布防远超测试
// 时长的 timer，抢跑调用 stop 后 done 必须变为可读——被抢跑废弃的 retry
// goroutine 从 select <-done 退出唯一依赖该信号。
func TestBug0222DefaultNewTimerStopMakesDoneReadable(t *testing.T) {
	bridge, _ := bug0222NewBridge(t, 60_000)
	defer bridge.Close()
	done, stop := bridge.newTimer(time.Hour)
	stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("BUG-0222 回退：stop 后 done 不可读，被抢跑废弃的 retry goroutine 将永久泄漏")
	}
}

// TestBug0222DefaultNewTimerStopAfterFireKeepsDoneReadable：timer 已自然
// fire 后再调用 stop 不得 panic（done 的 close 必须幂等），done 保持可读。
func TestBug0222DefaultNewTimerStopAfterFireKeepsDoneReadable(t *testing.T) {
	bridge, _ := bug0222NewBridge(t, 60_000)
	defer bridge.Close()
	done, stop := bridge.newTimer(5 * time.Millisecond)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timer 未按 delay 关闭 done")
	}
	stop()
	select {
	case <-done:
	default:
		t.Fatal("fire 后 done 必须保持可读")
	}
}

// TestBug0222PreemptedRetryGoroutineWakesFromDone：经真实布防路径
// scheduleScopeRetry 布防 retry timer（未注入 NewTimer，默认实现生效），随后
// 逐字复刻 retryPendingImmediately 的 timer 清理段做抢跑结算（不调用该函数
// 本体，避免其自带的 pending worker 完成排干、混淆信号来源）。被废弃的
// retry goroutine 必须从 <-done 醒来并继续 startScopeWorker → drainScope
// 处理 pending；CAS 调用计数是确定性证据。旧实现（stop 只调 timer.Stop 而
// 不关 done）下该 goroutine 永不醒来，计数恒 0。
func TestBug0222PreemptedRetryGoroutineWakesFromDone(t *testing.T) {
	bridge, db := bug0222NewBridge(t, 60_000)
	defer bridge.Close()
	scope := Scope{Kind: ScopeKindAccount, AccountRuntimeKey: "bug0222-account"}
	nowMs := int64(1_000)
	suspect, err := bridge.store.Suspect(context.Background(), SuspectInput{
		Scope:            scope,
		DispatchRevision: "1",
		TransitionID:     "bug0222-suspect",
		Reason:           "transport:connect refused",
		NowMs:            &nowMs,
	})
	if err != nil {
		t.Fatalf("suspect: %v", err)
	}
	scopeKey := suspect.State.ScopeKey
	bridge.mu.Lock()
	bridge.pending[scopeKey] = observeInput{scope: scope, state: suspect.State}
	bridge.mu.Unlock()

	bridge.scheduleScopeRetry(scopeKey)
	bridge.mu.Lock()
	retry, scheduled := bridge.retryBackoffs[scopeKey]
	bridge.mu.Unlock()
	if !scheduled || retry.timerStop == nil {
		t.Fatal("retry timer 未布防")
	}

	// 抢跑结算：复刻 retryPendingImmediately 对 retryBackoffs 的清理段。
	bridge.mu.Lock()
	for key, item := range bridge.retryBackoffs {
		if item.timerStop != nil {
			item.timerStop()
		}
		delete(bridge.retryBackoffs, key)
	}
	bridge.mu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	for db.callsGot.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if db.callsGot.Load() == 0 {
		t.Fatal("BUG-0222 回退：抢跑 stop 后 retry goroutine 未从 done 醒来（永久泄漏），pending 未被处理")
	}
}
