package gatewaydispatch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
)

// 账户锁死窗口的跨账户 hold 测试（AI 账户锁死调度机制设计与实现前复审
// §3.3/§3.4）：锁死账号真实失败后本请求不得切换到同组其他账号；只有
// complete_success 能清窗，死期结算为 DEAD_CONFIRMED 后才恢复切换；未开启
// 状态变更或探针流量不触发锁死 hold。

type lockHoldFailureCall struct {
	AccountID string
	Reason    string
}

type lockHoldSettleCall struct {
	AccountID string
	NowMs     int64
}

// lockHoldLocks 是自包含的 AccountLocks fake：按账号返回 AccountLockStateView，
// 并记录 RecordFailureAsync / SettleDeadlineAsync / 租约调用。settleClears
// 模拟 SettleDeadlineAsync 把死亡窗口结算为 DEAD_CONFIRMED（之后不再阻断）。
// lockedOnRecord 模拟 LOCKED_IDLE → ENGAGED 推进：账号被 RecordFailureAsync
// 记录过真实失败后，FindStateAsync 返回 BlocksCrossAccount=true。
type lockHoldLocks struct {
	mu             sync.Mutex
	states         map[string]AccountLockStateView
	settleClears   bool
	lockedOnRecord bool
	recorded       map[string]bool
	settled        map[string]bool
	findErr        error
	listErr        error
	failures       []lockHoldFailureCall
	settles        []lockHoldSettleCall
	acquireCalls   int
	consumeCalls   int
	releaseCalls   int
}

func newLockHoldLocks(states map[string]AccountLockStateView) *lockHoldLocks {
	return &lockHoldLocks{states: states, recorded: map[string]bool{}, settled: map[string]bool{}}
}

func (l *lockHoldLocks) FindStateAsync(_ context.Context, accountID string) (*AccountLockStateView, error) {
	if l.findErr != nil {
		return nil, l.findErr
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state, ok := l.states[accountID]
	if !ok && !(l.lockedOnRecord && l.recorded[accountID]) {
		return nil, nil
	}
	if l.lockedOnRecord && l.recorded[accountID] {
		state.BlocksCrossAccount = true
	}
	if l.settleClears && l.settled[accountID] {
		state.BlocksCrossAccount = false
	}
	return &state, nil
}

func (l *lockHoldLocks) AcquireRetryLeaseAsync(context.Context, string, int64) (LockLeaseAcquire, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.acquireCalls++
	return LockLeaseAcquire{Allowed: true}, nil
}

func (l *lockHoldLocks) ConsumeRetryLeaseAsync(context.Context, string, string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.consumeCalls++
	return true, nil
}

func (l *lockHoldLocks) ReleaseRetryLeaseAsync(context.Context, ReleaseRetryLeaseInput) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseCalls++
	return true, nil
}

func (l *lockHoldLocks) AbandonRetryReservationAsync(context.Context, AccountLockRetryLease) error {
	return nil
}

func (l *lockHoldLocks) RecordFailureAsync(_ context.Context, accountID, reason string, _ *AccountLockObservation) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.recorded == nil {
		l.recorded = map[string]bool{}
	}
	l.recorded[accountID] = true
	l.failures = append(l.failures, lockHoldFailureCall{AccountID: accountID, Reason: reason})
	return nil
}

func (l *lockHoldLocks) SettleDeadlineAsync(_ context.Context, accountID string, nowMs int64, _ *AccountLockObservation) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.settles = append(l.settles, lockHoldSettleCall{AccountID: accountID, NowMs: nowMs})
	if l.settleClears {
		l.settled[accountID] = true
	}
	return nil
}

func (l *lockHoldLocks) ListStatesAsync(_ context.Context, accountIDs []string) (map[string]AccountLockStateView, error) {
	if l.listErr != nil {
		return nil, l.listErr
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	states := make(map[string]AccountLockStateView, len(accountIDs))
	for _, accountID := range accountIDs {
		if state, ok := l.states[accountID]; ok {
			states[accountID] = state
		}
	}
	return states, nil
}

func (l *lockHoldLocks) CompleteSuccessAsync(context.Context, string, string, *AccountLockObservation) error {
	return nil
}

func (l *lockHoldLocks) failureCalls(accountID, reason string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := 0
	for _, call := range l.failures {
		if call.AccountID == accountID && call.Reason == reason {
			count++
		}
	}
	return count
}

func (l *lockHoldLocks) settleCalls(accountID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := 0
	for _, call := range l.settles {
		if call.AccountID == accountID {
			count++
		}
	}
	return count
}

func (l *lockHoldLocks) totalFailureCalls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.failures)
}

func (l *lockHoldLocks) totalSettleCalls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.settles)
}

// newLockHoldEngine 组装双账户引擎：a-1 返回 a1Status，a-2 返回 200，并用
// per-account 计数器暴露命中次数。
func newLockHoldEngine(t *testing.T, a1Status int) (*Engine, *fakeDriver, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	engine, driver, _ := newTestEngine(t)
	a1Hits := &atomic.Int64{}
	a2Hits := &atomic.Int64{}
	a1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a1Hits.Add(1)
		w.WriteHeader(a1Status)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream down","type":"server_error","code":"upstream_error"}}`))
	}))
	a2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a2Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-ok"}`))
	}))
	t.Cleanup(a1.Close)
	t.Cleanup(a2.Close)
	driver.urlByAccount = map[string][]string{
		"a-1": {a1.URL + "/v1/chat/completions"},
		"a-2": {a2.URL + "/v1/chat/completions"},
	}
	return engine, driver, a1Hits, a2Hits
}

func lockHoldAccounts() []AccountCandidate {
	return testAccounts("a-1", "a-2")
}

// TestAccountLockHoldBlocksCrossAccountAfterHTTPFailure：a-1 锁死 + 恒 500，
// a-2 健康 → 返回 AccountLockHoldError，a-2 命中 0，且非 2xx 已写入锁死窗口。
func TestAccountLockHoldBlocksCrossAccountAfterHTTPFailure(t *testing.T) {
	locks := newLockHoldLocks(map[string]AccountLockStateView{
		"a-1": {BlocksCrossAccount: true},
	})
	engine, _, a1Hits, a2Hits := newLockHoldEngine(t, http.StatusInternalServerError)
	engine.Locks = locks
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)

	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, lockHoldAccounts()))

	var lockHold *AccountLockHoldError
	if !errorsAs(err, &lockHold) {
		t.Fatalf("expected AccountLockHoldError, got %v", err)
	}
	if lockHold.AccountID != "a-1" {
		t.Fatalf("accountId = %q", lockHold.AccountID)
	}
	if a1Hits.Load() == 0 {
		t.Fatal("锁死账号必须被真实尝试")
	}
	if hits := a2Hits.Load(); hits != 0 {
		t.Fatalf("a-2 命中 = %d，锁死窗口内不得切换", hits)
	}
	if calls := locks.failureCalls("a-1", "upstream_http_response"); calls == 0 {
		t.Fatal("完整非 2xx 必须写入锁死窗口")
	}
	if calls := locks.settleCalls("a-1"); calls == 0 {
		t.Fatal("完整非 2xx 必须触发死期结算")
	}
}

// TestAccountLockHoldBlocksCrossAccountAfterTransportFailure：锁死 + 传输失败
// 同样不得切换其他账号。
func TestAccountLockHoldBlocksCrossAccountAfterTransportFailure(t *testing.T) {
	locks := newLockHoldLocks(map[string]AccountLockStateView{
		"a-1": {BlocksCrossAccount: true},
	})
	engine, driver, _, a2Hits := newLockHoldEngine(t, http.StatusInternalServerError)
	engine.Locks = locks
	driver.urlByAccount["a-1"] = []string{"https://127.0.0.1:9/v1/chat/completions"}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)

	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, lockHoldAccounts()))

	var lockHold *AccountLockHoldError
	if !errorsAs(err, &lockHold) {
		t.Fatalf("expected AccountLockHoldError, got %v", err)
	}
	if lockHold.AccountID != "a-1" {
		t.Fatalf("accountId = %q", lockHold.AccountID)
	}
	if hits := a2Hits.Load(); hits != 0 {
		t.Fatalf("a-2 命中 = %d，锁死窗口内不得切换", hits)
	}
	if calls := locks.failureCalls("a-1", "upstream_transport_failure"); calls == 0 {
		t.Fatal("传输失败必须写入锁死窗口")
	}
}

// TestAccountLockHoldUnlockedStillSwitches：未锁账号 + 500 保持既有换号基线。
func TestAccountLockHoldUnlockedStillSwitches(t *testing.T) {
	locks := newLockHoldLocks(nil)
	engine, _, a1Hits, a2Hits := newLockHoldEngine(t, http.StatusInternalServerError)
	engine.Locks = locks
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)

	result, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, lockHoldAccounts()))
	if err != nil {
		t.Fatalf("未锁账号必须允许换号: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("account = %q，期望 a-2", result.Account.ID)
	}
	if a1Hits.Load() == 0 || a2Hits.Load() == 0 {
		t.Fatalf("a-1 命中 = %d, a-2 命中 = %d", a1Hits.Load(), a2Hits.Load())
	}
}

// TestAccountLockHoldReleasedAfterDeadlineSettlement：SettleDeadlineAsync 把
// 死亡窗口结算为 DEAD_CONFIRMED 后不再阻断，允许切换 a-2。
func TestAccountLockHoldReleasedAfterDeadlineSettlement(t *testing.T) {
	locks := newLockHoldLocks(map[string]AccountLockStateView{
		"a-1": {BlocksCrossAccount: true},
	})
	locks.settleClears = true
	engine, _, _, a2Hits := newLockHoldEngine(t, http.StatusInternalServerError)
	engine.Locks = locks
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)

	result, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, lockHoldAccounts()))
	if err != nil {
		t.Fatalf("死期结算后必须允许切换: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("account = %q，期望 a-2", result.Account.ID)
	}
	if hits := a2Hits.Load(); hits != 1 {
		t.Fatalf("a-2 命中 = %d，期望 1", hits)
	}
	if calls := locks.settleCalls("a-1"); calls == 0 {
		t.Fatal("锁死账号必须触发死期结算")
	}
}

// TestAccountLockHoldSkippedForNonGateTraffic：AccountStateMutationEnabled=false
// 与探针流量都不进入锁死门（锁死运行态不写、不 hold）。
func TestAccountLockHoldSkippedForNonGateTraffic(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*FetchFirstAvailableUpstreamArgs)
	}{
		{
			name: "mutation_disabled",
			mutate: func(args *FetchFirstAvailableUpstreamArgs) {
				args.AccountStateMutationEnabled = false
			},
		},
		{
			name: "probe_traffic",
			mutate: func(args *FetchFirstAvailableUpstreamArgs) {
				args.UsageContext.TrafficSource = "probe"
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			locks := newLockHoldLocks(map[string]AccountLockStateView{
				"a-1": {BlocksCrossAccount: true},
			})
			engine, _, _, a2Hits := newLockHoldEngine(t, http.StatusInternalServerError)
			engine.Locks = locks
			req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
			args := fastDispatchArgs(t, req, lockHoldAccounts())
			testCase.mutate(&args)

			result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
			if err != nil {
				t.Fatalf("非锁死流量必须保持既有换号: %v", err)
			}
			if result.Account.ID != "a-2" {
				t.Fatalf("account = %q，期望 a-2", result.Account.ID)
			}
			if hits := a2Hits.Load(); hits != 1 {
				t.Fatalf("a-2 命中 = %d，期望 1", hits)
			}
			if calls := locks.totalFailureCalls(); calls != 0 {
				t.Fatalf("锁死失败写入 = %d，期望 0", calls)
			}
			if calls := locks.totalSettleCalls(); calls != 0 {
				t.Fatalf("锁死死期结算 = %d，期望 0", calls)
			}
		})
	}
}

// TestAccountLockHoldFirstFailureTransitionsAndBlocks：a-1 初始有锁行但未阻断
// （LOCKED_IDLE），首次真实 500 由 RecordFailureAsync 推进为阻断（ENGAGED），
// 同账号重试耗尽后必须 hold 在 a-1，a-2 命中 0。
func TestAccountLockHoldFirstFailureTransitionsAndBlocks(t *testing.T) {
	locks := newLockHoldLocks(map[string]AccountLockStateView{
		"a-1": {BlocksCrossAccount: false},
	})
	locks.lockedOnRecord = true
	engine, _, a1Hits, a2Hits := newLockHoldEngine(t, http.StatusInternalServerError)
	engine.Locks = locks
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)

	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, lockHoldAccounts()))

	var lockHold *AccountLockHoldError
	if !errorsAs(err, &lockHold) {
		t.Fatalf("expected AccountLockHoldError, got %v", err)
	}
	if lockHold.AccountID != "a-1" {
		t.Fatalf("accountId = %q", lockHold.AccountID)
	}
	if a1Hits.Load() == 0 {
		t.Fatal("锁死账号必须被真实尝试")
	}
	if hits := a2Hits.Load(); hits != 0 {
		t.Fatalf("a-2 命中 = %d，首次失败进入阻断后不得切换", hits)
	}
	if calls := locks.failureCalls("a-1", "upstream_http_response"); calls == 0 {
		t.Fatal("完整非 2xx 必须写入锁死窗口")
	}
}

// TestAccountLockHoldExplicitPolicyTransientStillHolds：D12——显式策略的可恢复
// 失败（此处 500，无确定性处置标记）在锁死窗口内继续 hold：不得切到 a-2，
// 并写锁死运行态推进窗口。
func TestAccountLockHoldExplicitPolicyTransientStillHolds(t *testing.T) {
	locks := newLockHoldLocks(map[string]AccountLockStateView{
		"a-1": {BlocksCrossAccount: true},
	})
	engine, _, a1Hits, a2Hits := newLockHoldEngine(t, http.StatusInternalServerError)
	engine.Locks = locks
	dispatcher, ok := engine.FailureDispatcher.(*fakeFailureDispatcher)
	if !ok {
		t.Fatalf("FailureDispatcher = %T", engine.FailureDispatcher)
	}
	dispatcher.failedResult = FailedUpstreamResponseResult{
		Action:             FailedResponseActionSkipAccount,
		FailureKind:        FailureKindExplicitPolicy,
		FailureDisposition: FailureDispositionTransientHold,
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)

	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, lockHoldAccounts()))
	var lockHold *AccountLockHoldError
	if !errorsAs(err, &lockHold) {
		t.Fatalf("expected AccountLockHoldError, got %v", err)
	}
	if a1Hits.Load() == 0 {
		t.Fatal("锁死账号必须被真实尝试")
	}
	if hits := a2Hits.Load(); hits != 0 {
		t.Fatalf("a-2 命中 = %d，D12 可恢复显式策略失败不得切换", hits)
	}
	if calls := locks.failureCalls("a-1", "upstream_http_response"); calls == 0 {
		t.Fatal("D12 可恢复失败必须写入锁死窗口")
	}
	if calls := locks.totalSettleCalls(); calls == 0 {
		t.Fatal("D12 可恢复失败必须推进死期结算")
	}
}

// TestAccountLockHoldExplicitPolicyDeterministicReleases：确定性硬门（如凭据
// 无效/额度耗尽）在锁死窗口内仍释放换号，且不写锁死运行态。
func TestAccountLockHoldExplicitPolicyDeterministicReleases(t *testing.T) {
	locks := newLockHoldLocks(map[string]AccountLockStateView{
		"a-1": {BlocksCrossAccount: true},
	})
	engine, _, a1Hits, a2Hits := newLockHoldEngine(t, http.StatusUnauthorized)
	engine.Locks = locks
	dispatcher, ok := engine.FailureDispatcher.(*fakeFailureDispatcher)
	if !ok {
		t.Fatalf("FailureDispatcher = %T", engine.FailureDispatcher)
	}
	dispatcher.failedResult = FailedUpstreamResponseResult{
		Action:             FailedResponseActionSkipAccount,
		FailureKind:        FailureKindExplicitPolicy,
		FailureDisposition: FailureDispositionDeterministicRelease,
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)

	result, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, lockHoldAccounts()))
	if err != nil {
		t.Fatalf("确定性硬门必须允许换号: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("account = %q，期望 a-2", result.Account.ID)
	}
	if a1Hits.Load() == 0 || a2Hits.Load() != 1 {
		t.Fatalf("命中次数 a1=%d a2=%d，期望 a1>0、a2=1", a1Hits.Load(), a2Hits.Load())
	}
	if calls := locks.totalFailureCalls(); calls != 0 {
		t.Fatalf("确定性硬门不得写锁死运行态: calls = %d", calls)
	}
	if calls := locks.totalSettleCalls(); calls != 0 {
		t.Fatalf("确定性硬门不得触发死期结算: calls = %d", calls)
	}
}

// TestAccountLockHoldBlocksCrossAccountOnPreAttemptSkip：M3——尝试前的候选内
// 跳过出口（此处为上游 URL 构造为空）同样不得切换到其他候选；锁死账号的
// skip 出口必须由最终锁死门转为 hold。
func TestAccountLockHoldBlocksCrossAccountOnPreAttemptSkip(t *testing.T) {
	locks := newLockHoldLocks(map[string]AccountLockStateView{
		"a-1": {BlocksCrossAccount: true},
	})
	engine, driver, _, a2Hits := newLockHoldEngine(t, http.StatusInternalServerError)
	engine.Locks = locks
	driver.urlByAccount["a-1"] = []string{}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)

	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, lockHoldAccounts()))

	var lockHold *AccountLockHoldError
	if !errorsAs(err, &lockHold) {
		t.Fatalf("expected AccountLockHoldError, got %v", err)
	}
	if lockHold.AccountID != "a-1" {
		t.Fatalf("accountId = %q", lockHold.AccountID)
	}
	if hits := a2Hits.Load(); hits != 0 {
		t.Fatalf("a-2 命中 = %d，尝试前跳过出口同样不得切换", hits)
	}
}

// TestAccountLockHoldKeyModelAttemptSettled：B1——传输失败锁死出口也必须结算
// key-model attempt（否则前台许可与续租泄漏）。直驱 handleUpstreamAttemptError
// 断言锁死停止与 settle-once 终态信号同时成立。
func TestAccountLockHoldKeyModelAttemptSettled(t *testing.T) {
	harness := newAttemptErrorHarness(t)
	harness.input.accountLockTrafficEnabled = true
	harness.engine.Locks = &blockingLocks{}

	kstore := gatewayaccounteffects.NewInMemoryKeyModelRuntimeStore(gatewayaccounteffects.SystemClock{})
	route := gatewayaccounteffects.GatewayKeyModelCapability{
		AccountID: "a-1",
		Capability: gatewayaccounteffects.CapabilityKey{
			CredentialSourceAccountID: "km-hold-source",
			KeyFingerprint:            "km-hold-fp",
			ClientModel:               "gpt-test",
			ClientEndpointFamily:      "chat_completions",
			FinalUpstreamModel:        "gpt-upstream",
			UpstreamEndpointMode:      "chat_json",
			DispatchRevision:          1,
		},
	}
	preparation, err := gatewayaccounteffects.PrepareGatewayKeyModelAttempt(context.Background(), kstore,
		gatewayaccounteffects.PrepareGatewayKeyModelAttemptInput{
			Route:         route,
			RequestID:     "km-hold-req",
			AttemptID:     "km-hold-1",
			FailureBudget: gatewayaccounteffects.NewGatewayKeyModelFailureBudget(),
			Scheduler:     gatewayaccounteffects.NewManualScheduler(),
			Logger:        gatewayaccounteffects.NopLogger{},
		})
	if err != nil {
		t.Fatalf("PrepareGatewayKeyModelAttempt: %v", err)
	}
	if preparation.Status != gatewayaccounteffects.AttemptPreparationAdmitted || preparation.Attempt == nil {
		t.Fatalf("preparation = %s, want admitted+attempt", preparation.Status)
	}

	started := &PrimaryStartedGatewayTransportError{Err: &StartedTransportError{Err: errors.New("连接被重置")}}
	_, kind, stop, callErr := harness.errorContext(testAccounts("a-1")[0], started, func(errorCtx *upstreamAttemptErrorContext) {
		errorCtx.keyModelAttempt = preparation.Attempt
	})
	if callErr != nil {
		t.Fatalf("handleUpstreamAttemptError: %v", callErr)
	}
	if kind != errorKindHandled || stop.kind != errorStopAccountLocked {
		t.Fatalf("kind=%v stop=%#v", kind, stop)
	}
	select {
	case <-preparation.Attempt.WaitTerminal():
	default:
		t.Fatal("锁死出口必须结算 key-model attempt（WaitTerminal 未关闭，许可续租泄漏）")
	}
}
