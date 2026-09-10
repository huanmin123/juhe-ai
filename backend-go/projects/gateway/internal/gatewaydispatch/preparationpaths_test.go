package gatewaydispatch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// dispatch/preparation.ts 候选准备分支 + reserveSameAccountRetry 账户锁脚本
// 的测试。全部经 fake 端口驱动。

// ---------------------------------------------------------------------------
// 可配置端口假件
// ---------------------------------------------------------------------------

type flagLatencyPort struct {
	applied    bool
	degraded   []string
	bypassAll  bool
}

func (f *flagLatencyPort) OrderAsync(_ context.Context, accounts []AccountCandidate, _ *LatencyScopeInput, _ *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, _ *gatewayrouting.GatewayAccountModelPriority) (LatencyDegradationOrder, error) {
	return LatencyDegradationOrder{
		Accounts:            accounts,
		Applied:             f.applied,
		DegradedAccountIDs:  f.degraded,
		BypassedAllDegraded: f.bypassAll,
	}, nil
}

type flagProxyHealthPort struct {
	applied bool
}

func (f *flagProxyHealthPort) OrderAsync(_ context.Context, accounts []AccountCandidate, _ *gatewayrouting.GatewayAccountModelPriority) (ProxyHealthOrder, error) {
	return ProxyHealthOrder{Accounts: accounts, Applied: f.applied}, nil
}

func (f *flagProxyHealthPort) RecordFailureAsync(context.Context, AccountCandidate, string) error { return nil }

type flagClientIPAvoidancePort struct {
	applied bool
}

func (f *flagClientIPAvoidancePort) OrderAsync(_ context.Context, accounts []AccountCandidate, _ ClientIPAvoidanceScope, _ *gatewayrouting.GatewayAccountModelPriority) (AvoidanceOrder, error) {
	return AvoidanceOrder{Accounts: accounts, Applied: f.applied}, nil
}

type flagClientSourceAvoidancePort struct {
	applied   bool
	threshold bool
	avoided   []string
}

func (f *flagClientSourceAvoidancePort) OrderAsync(_ context.Context, accounts []AccountCandidate, _ gatewaypreauth.ClientStrategyContext, _ *gatewayrouting.GatewayAccountModelPriority) (AvoidanceOrder, error) {
	return AvoidanceOrder{Accounts: accounts, Applied: f.applied, ThresholdReached: f.threshold, AvoidedAccountIDs: f.avoided}, nil
}

type configurableAffinity struct {
	fakeAffinity
	busy bool
}

func (f *configurableAffinity) AreHighConcurrencyAccountsBusyForLaneAsync(context.Context, []AccountCandidate, HighConcurrencyBusyOptions) (bool, error) {
	return f.busy, nil
}

type configurableClientIPConcurrency struct {
	enabled  bool
	acquired bool
	reason   string
	released atomic.Int64
}

func (f *configurableClientIPConcurrency) Acquire(context.Context, ClientIPConcurrencyInput) (ClientIPConcurrencyDecision, error) {
	decision := ClientIPConcurrencyDecision{Enabled: f.enabled, Acquired: f.acquired, Reason: f.reason, Current: 1, Limit: 2}
	if f.acquired {
		decision.Release = func() { f.released.Add(1) }
	}
	return decision, nil
}

type configurableQueue struct {
	ready bool
	calls atomic.Int64
}

func (f *configurableQueue) WaitForCapacity(context.Context, HighConcurrencyWaitInput) (QueueWaitResult, error) {
	f.calls.Add(1)
	return QueueWaitResult{Ready: f.ready, Reason: "drained"}, nil
}

// dispatchPreparationInput 构造准备输入。
func dispatchPreparationInput(t *testing.T, accounts []AccountCandidate) gatewaypreauth.DispatchPreparationInput {
	t.Helper()
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	wallBudget, err := gatewayrouting.NewGatewayRequestWallBudget(gatewayrouting.GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: NowMs(),
		BudgetMs:            ptrInt64(60_000),
	}, nil)
	if err != nil {
		t.Fatalf("wall budget: %v", err)
	}
	return gatewaypreauth.DispatchPreparationInput{
		GatewayRequestWallBudget: wallBudget,
		Req:               req,
		AuditCapture:      &frozenAudit{sink: &fakeAuditSink{}},
		UsageContext:      testUsageContext(),
		StartedAt:         NowMs(),
		CandidateAccounts: accounts,
		ModelPriority:     &gatewayrouting.GatewayAccountModelPriority{RankByAccountID: map[string]int{}},
		GroupAccess:       gatewayruntimecache.GroupUsageAccessMetadata{},
		SystemAccountID:   "system-1",
		APIKeyID:          "apikey-1",
		GroupID:           "group-1",
		ClientStrategy:    gatewaypreauth.ClientStrategyContext{},
		RequestLane:       "text",
		ServerRetryBudget: gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{}),
		RouteCoordinator:  &capturingCoordinator{},
		Signal:            context.Background(),
	}
}

func highConcurrencyGroupAccess(policy *gatewayruntimecache.GroupSchedulingPolicy) gatewayruntimecache.GroupUsageAccessMetadata {
	hc := gatewayruntimecacheGroupTypeHighConcurrency
	return gatewayruntimecache.GroupUsageAccessMetadata{GroupType: &hc, SchedulingPolicy: policy}
}

func TestPrepareDispatchAccountsWithAppliedOrderings(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	engine.Latency = &flagLatencyPort{applied: true, degraded: []string{"a-1"}}
	engine.ProxyHealth = &flagProxyHealthPort{applied: true}
	engine.ClientIPAvoidance = &flagClientIPAvoidancePort{applied: true}
	engine.ClientSourceAvoidance = &flagClientSourceAvoidancePort{applied: true, threshold: true, avoided: []string{"a-2"}}
	input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
	input.NormalRouteSpeedFirstConfig = &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{}
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if !result.NormalRouteLatencyDegradationApplied {
		t.Fatal("延迟降级应用标记必须透传")
	}
	if !result.CodexTurnAccountAvoidanceApplied || len(result.CodexTurnAvoidedAccountIDs) != 1 {
		t.Fatalf("codex turn 规避标记 = %v %v", result.CodexTurnAccountAvoidanceApplied, result.CodexTurnAvoidedAccountIDs)
	}
}

func TestPrepareDispatchAccountsRuntimeDegradedFallback(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	engine.Degradation = &allDegradedPort{}
	coordinator := &capturingCoordinator{nextFallback: &gatewayrouting.GatewayRouteFallbackDecision{Attempted: true, Context: "fb"}}
	input := dispatchPreparationInput(t, testAccounts("a-1"))
	input.RouteCoordinator = coordinator
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeFallback || result.Reason != "runtime_degraded" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPrepareDispatchAccountsPrecheckHalfOpenEligible(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	// 全部被抑制 + fallback 未接管 + precheck 抑制集合覆盖全部候选 → 半开预检资格。
	engine.Suppression = &precheckSuppressedFake{}
	input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if !result.PrecheckHalfOpenEligible {
		t.Fatal("precheck half-open 资格必须置位")
	}
}

// precheckSuppressedFake 首次过滤全部抑制并携带 precheck 集合，后续放行。
type precheckSuppressedFake struct {
	calls int
}

func (f *precheckSuppressedFake) FilterAsync(_ context.Context, accounts []AccountCandidate, _ SuppressionFilterOptions) (SuppressionFilterResult, error) {
	f.calls++
	if f.calls == 1 {
		ids := accountIDs(accounts)
		return SuppressionFilterResult{
			AllSuppressed:                     true,
			SuppressedCount:                   len(accounts),
			SuppressedAccountIDs:              ids,
			PrecheckSuppressedAccountIDs:      ids,
			ConfiguredPolicySuppressedAccountIDs: []string{},
			AcquiredHalfOpenLeases:            []HalfOpenLease{},
		}, nil
	}
	return localSuppressionBypassResult(accounts), nil
}

func (f *precheckSuppressedFake) ResolveLocalSuppressionFilter(ctx context.Context, input LocalSuppressionPreflightInput) (*SuppressionFilterResult, bool, error) {
	result := localSuppressionBypassResult(input.Accounts)
	return &result, false, nil
}

func TestPrepareDispatchAccountsLocalSuppressionCompleted(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	engine.Suppression = &completedSuppressionFake{}
	input := dispatchPreparationInput(t, testAccounts("a-1"))
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeCompleted {
		t.Fatalf("outcome = %s", result.Outcome)
	}
}

// completedSuppressionFake 的本地抑制预检报告已完成（请求已终结）。
type completedSuppressionFake struct{}

func (completedSuppressionFake) FilterAsync(_ context.Context, accounts []AccountCandidate, _ SuppressionFilterOptions) (SuppressionFilterResult, error) {
	return SuppressionFilterResult{Accounts: accounts, AcquiredHalfOpenLeases: []HalfOpenLease{}}, nil
}

func (completedSuppressionFake) ResolveLocalSuppressionFilter(ctx context.Context, input LocalSuppressionPreflightInput) (*SuppressionFilterResult, bool, error) {
	return nil, true, nil
}

func TestPrepareDispatchAccountsHighConcurrencyReady(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer okServer.Close()
	policy := gatewayruntimecache.GroupSchedulingPolicy{}
	engine.Concurrency = &mapBackedConcurrencyStore{current: map[string]int{"a-1": 0}}
	engine.Affinity = &configurableAffinity{busy: false}
	engine.ClientIPConcurrency = &configurableClientIPConcurrency{enabled: true, acquired: true}
	engine.HighConcurrencyQueue = &configurableQueue{ready: true}
	driver := engine.Driver.(*fakeDriver)
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	input := dispatchPreparationInput(t, testAccounts("a-1"))
	input.GroupAccess = highConcurrencyGroupAccess(&policy)
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts || len(result.Accounts) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if result.ReleaseClientIPConcurrency == nil {
		t.Fatal("client IP 并发释放闭包必须存在")
	}
	result.ReleaseClientIPConcurrency()
	result.ReleaseClientIPConcurrency() // 幂等
}

func TestPrepareDispatchAccountsHighConcurrencyBusyFallsBack(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	policy := gatewayruntimecache.GroupSchedulingPolicy{}
	engine.Concurrency = &mapBackedConcurrencyStore{current: map[string]int{}}
	engine.Affinity = &configurableAffinity{busy: true}
	coordinator := &capturingCoordinator{nextFallback: &gatewayrouting.GatewayRouteFallbackDecision{Attempted: true}}
	input := dispatchPreparationInput(t, testAccounts("a-1"))
	input.GroupAccess = highConcurrencyGroupAccess(&policy)
	input.RouteCoordinator = coordinator
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeFallback || result.Reason != "high_concurrency_group_busy" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPrepareDispatchAccountsHighConcurrencyBusyCompletes429(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	policy := gatewayruntimecache.GroupSchedulingPolicy{}
	engine.Concurrency = &mapBackedConcurrencyStore{current: map[string]int{}}
	engine.Affinity = &configurableAffinity{busy: true}
	coordinator := &capturingCoordinator{}
	input := dispatchPreparationInput(t, testAccounts("a-1"))
	input.GroupAccess = highConcurrencyGroupAccess(&policy)
	input.RouteCoordinator = coordinator
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeCompleted {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if coordinator.failure == nil || coordinator.failure.Message != "分组繁忙，请稍后重试" {
		t.Fatalf("failure = %#v", coordinator.failure)
	}
}

func TestPrepareDispatchAccountsClientIPConcurrencyDenied(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	policy := gatewayruntimecache.GroupSchedulingPolicy{}
	engine.Concurrency = &mapBackedConcurrencyStore{current: map[string]int{}}
	engine.Affinity = &configurableAffinity{busy: false}
	engine.ClientIPConcurrency = &configurableClientIPConcurrency{enabled: true, acquired: false, reason: "timeout"}
	input := dispatchPreparationInput(t, testAccounts("a-1"))
	input.GroupAccess = highConcurrencyGroupAccess(&policy)
	coordinator := &capturingCoordinator{}
	input.RouteCoordinator = coordinator
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeCompleted {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if coordinator.failure == nil || coordinator.failure.Message != "当前 IP 并发排队等待超时，请稍后重试" {
		t.Fatalf("failure = %#v", coordinator.failure)
	}
}

func TestPrepareDispatchAccountsCapacityBusyFallsBack(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	// 普通分组：账户满载 → group_capacity_busy fallback。
	engine.Concurrency = &mapBackedConcurrencyStore{current: map[string]int{"a-1": 4}}
	coordinator := &capturingCoordinator{nextFallback: &gatewayrouting.GatewayRouteFallbackDecision{Attempted: true}}
	input := dispatchPreparationInput(t, testAccounts("a-1"))
	input.RouteCoordinator = coordinator
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeFallback || result.Reason != "group_capacity_busy" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPrepareDispatchAccountsQuotaDeniedFallback(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	engine.Quota = &fakeQuota{denied: map[string]struct{}{"a-1": {}}}
	coordinator := &capturingCoordinator{nextFallback: &gatewayrouting.GatewayRouteFallbackDecision{Attempted: true}}
	input := dispatchPreparationInput(t, testAccounts("a-1"))
	input.RouteCoordinator = coordinator
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeFallback || result.Reason != "authorization_quota_exceeded" {
		t.Fatalf("result = %#v", result)
	}
}

func TestRequestRouteFallbackLockGuardBlocks(t *testing.T) {
	_, engine, _, _ := newPipeline(t)
	engine.Locks = &blockingLocks{}
	pipeline2 := NewCandidatePipeline(engine)
	attempted, _, err := pipeline2.requestRouteFallback(context.Background(), dispatchPreparationInput(t, testAccounts("a-1")), testAccounts("a-1"), "upstream_accounts_exhausted")
	if err != nil {
		t.Fatalf("requestRouteFallback: %v", err)
	}
	if attempted {
		t.Fatal("跨账户阻断时不得发起分组回退")
	}
}

func TestReleaseAndCompleteHalfOpenLeaseNotify(t *testing.T) {
	notifyKeys := []string{}
	previous := notifyOneRecoverableUnavailableRuntimeWaiter
	SetRecoverableUnavailableRuntimeWaiterNotifier(func(runtimeKey string) {
		notifyKeys = append(notifyKeys, runtimeKey)
	})
	t.Cleanup(func() {
		SetRecoverableUnavailableRuntimeWaiterNotifier(previous)
	})
	if releaseHalfOpenLease(context.Background(), nil) {
		t.Fatal("nil 租约不释放")
	}
	if completeHalfOpenLeaseSuccess(context.Background(), nil) {
		t.Fatal("nil 租约不完成")
	}
	released := false
	lease := fakeHalfOpenLease{released: &released}
	if !releaseHalfOpenLease(context.Background(), lease) || !released {
		t.Fatal("租约必须释放")
	}
	if !completeHalfOpenLeaseSuccess(context.Background(), lease) {
		t.Fatal("租约必须完成成功")
	}
	// 释放与完成成功各通知一次。
	if len(notifyKeys) != 2 {
		t.Fatalf("通知次数 = %d", len(notifyKeys))
	}
	// nil 通知函数回退为 no-op。
	SetRecoverableUnavailableRuntimeWaiterNotifier(nil)
	releaseHalfOpenLease(context.Background(), lease)
	if len(notifyKeys) != 2 {
		t.Fatalf("nil 通知后不应新增通知, got %d", len(notifyKeys))
	}
}

func TestHotQualityModeForAndGroupTypeOf(t *testing.T) {
	if hotQualityModeFor(nil) != HotQualityModeCostFirst {
		t.Fatal("无速度优先配置 → cost first")
	}
	if hotQualityModeFor(&gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{}) != HotQualityModeSpeedFirst {
		t.Fatal("有速度优先配置 → speed first")
	}
	if groupTypeOf(gatewayruntimecache.GroupUsageAccessMetadata{}) != "" {
		t.Fatal("无分组类型为空")
	}
	groupType := "standard"
	if groupTypeOf(gatewayruntimecache.GroupUsageAccessMetadata{GroupType: &groupType}) != "standard" {
		t.Fatal("分组类型透传")
	}
	if isAccountProbeTrafficSource("probe") != true || isAccountProbeTrafficSource("health_check") != true || isAccountProbeTrafficSource("gateway") != false {
		t.Fatal("探针流量源判定不符")
	}
}

// ---------------------------------------------------------------------------
// reserveSameAccountRetry 账户锁脚本（dead URL 触发 upstream_transport_failure）
// ---------------------------------------------------------------------------

// scriptedLocks 按调用序返回 AcquireRetryLeaseAsync 结果。
type scriptedLocks struct {
	acquireResults []LockLeaseAcquire
	calls          atomic.Int64
	releaseCalls   atomic.Int64
	consumeCalls   atomic.Int64
	consumeAllowed atomic.Bool
}

func (s *scriptedLocks) FindStateAsync(context.Context, string) (*AccountLockStateView, error) {
	return nil, nil
}

func (s *scriptedLocks) AcquireRetryLeaseAsync(context.Context, string, int64) (LockLeaseAcquire, error) {
	call := s.calls.Add(1)
	index := int(call) - 1
	if index >= len(s.acquireResults) {
		return LockLeaseAcquire{Allowed: true}, nil
	}
	return s.acquireResults[index], nil
}

func (s *scriptedLocks) ConsumeRetryLeaseAsync(context.Context, string, string) (bool, error) {
	s.consumeCalls.Add(1)
	return s.consumeAllowed.Load(), nil
}

func (s *scriptedLocks) ReleaseRetryLeaseAsync(context.Context, ReleaseRetryLeaseInput) (bool, error) {
	s.releaseCalls.Add(1)
	return true, nil
}

func (s *scriptedLocks) AbandonRetryReservationAsync(context.Context, AccountLockRetryLease) error {
	return nil
}

func (s *scriptedLocks) RecordFailureAsync(context.Context, string, string, *AccountLockObservation) error {
	return nil
}

func (s *scriptedLocks) SettleDeadlineAsync(context.Context, string, int64, *AccountLockObservation) error {
	return nil
}

func (s *scriptedLocks) ListStatesAsync(context.Context, []string) (map[string]AccountLockStateView, error) {
	return map[string]AccountLockStateView{}, nil
}

func (s *scriptedLocks) CompleteSuccessAsync(context.Context, string, string, *AccountLockObservation) error {
	return nil
}

// dispatchDeadAccount 发起对死端点的单账户调度（触发传输失败 → 同账户重试脚本）。
func dispatchDeadAccount(t *testing.T, engine *Engine, account AccountCandidate) (*UpstreamAttemptError, error) {
	t.Helper()
	driver := engine.Driver.(*fakeDriver)
	driver.urlByAccount = map[string][]string{
		account.ID: {"https://127.0.0.1:9/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, []AccountCandidate{account}))
	var attemptErr *UpstreamAttemptError
	errorsAs(err, &attemptErr)
	return attemptErr, err
}

func TestReserveSameAccountRetryLockLeaseConsumed(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	locks := &scriptedLocks{acquireResults: []LockLeaseAcquire{
		{Allowed: true, LeaseID: "lease-1", WaitMs: 1},
	}}
	locks.consumeAllowed.Store(true)
	engine.Locks = locks
	attemptErr, _ := dispatchDeadAccount(t, engine, testAccounts("a-1")[0])
	if attemptErr == nil {
		t.Fatal("全部尝试失败必须返回 UpstreamAttemptError")
	}
	if locks.calls.Load() < 1 || locks.consumeCalls.Load() < 1 {
		t.Fatalf("acquire=%d consume=%d", locks.calls.Load(), locks.consumeCalls.Load())
	}
	// 重试租约在调度结束时释放。
	if locks.releaseCalls.Load() != 1 {
		t.Fatalf("release calls = %d", locks.releaseCalls.Load())
	}
	_ = attemptErr
}

func TestReserveSameAccountRetryReacquireAfterDelay(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	// 首次拿到无租约但需等待 → 等待后重取（LeaseID=="" 分支）。
	locks := &scriptedLocks{acquireResults: []LockLeaseAcquire{
		{Allowed: false, LeaseID: "", WaitMs: 1},
		{Allowed: true, LeaseID: "lease-2", WaitMs: 0},
	}}
	locks.consumeAllowed.Store(true)
	engine.Locks = locks
	attemptErr, _ := dispatchDeadAccount(t, engine, testAccounts("a-1")[0])
	if attemptErr == nil {
		t.Fatal("全部尝试失败必须返回 UpstreamAttemptError")
	}
	if locks.calls.Load() < 2 {
		t.Fatalf("acquire calls = %d", locks.calls.Load())
	}
	if locks.consumeCalls.Load() < 1 {
		t.Fatalf("consume calls = %d", locks.consumeCalls.Load())
	}
}

func TestReserveSameAccountRetryWaitWallExhausted(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	// 等待时间超出墙钟剩余 → wall 预算错误。
	locks := &scriptedLocks{acquireResults: []LockLeaseAcquire{
		{Allowed: false, LeaseID: "lease-1", WaitMs: 10 * 60_000},
	}}
	engine.Locks = locks
	_, err := dispatchDeadAccount(t, engine, testAccounts("a-1")[0])
	var budgetErr *GatewayRequestWallBudgetExhaustedError
	if !errorsAs(err, &budgetErr) || budgetErr.BudgetKind != WallBudgetKindWall {
		t.Fatalf("expected wall budget error, got %v", err)
	}
}

func TestReserveSameAccountRetryWaitCoordinationExhausted(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	// 等待时间在墙钟内但超出协调预算（3s）→ coordination 预算错误。
	locks := &scriptedLocks{acquireResults: []LockLeaseAcquire{
		{Allowed: false, LeaseID: "lease-1", WaitMs: 5_000},
	}}
	engine.Locks = locks
	_, err := dispatchDeadAccount(t, engine, testAccounts("a-1")[0])
	var budgetErr *GatewayRequestWallBudgetExhaustedError
	if !errorsAs(err, &budgetErr) || budgetErr.BudgetKind != WallBudgetKindCoordination {
		t.Fatalf("expected coordination budget error, got %v", err)
	}
}

func TestReserveSameAccountRetryWaitAborted(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	locks := &scriptedLocks{acquireResults: []LockLeaseAcquire{
		{Allowed: false, LeaseID: "lease-1", WaitMs: 10 * 60_000},
	}}
	engine.Locks = locks
	driver := engine.Driver.(*fakeDriver)
	driver.urlByAccount = map[string][]string{"a-1": {"https://127.0.0.1:9/v1/chat/completions"}}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	ctx, cancel := context.WithCancel(context.Background())
	args.Signal = ctx
	args.RequestCoordination.OnUpstreamAttemptStarted = func(AccountCandidate, string) {
		go func() {
			<-ctx.Done()
		}()
		cancel()
	}
	defer cancel()
	_, err := engine.FetchFirstAvailableUpstream(ctx, args)
	var budgetErr *GatewayRequestWallBudgetExhaustedError
	if errorsAs(err, &budgetErr) {
		t.Fatalf("取消路径不应返回预算错误, got %v", err)
	}
}

func TestReserveSameAccountRetrySecondWaitCoordination(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	// 首次 Allowed 且 LeaseID 非空、WaitMs 在墙钟内但超出协调预算（3s）
	// → 第二等待分支 coordination + 放弃预留。
	locks := &scriptedLocks{acquireResults: []LockLeaseAcquire{
		{Allowed: true, LeaseID: "lease-1", WaitMs: 5_000},
	}}
	engine.Locks = locks
	_, err := dispatchDeadAccount(t, engine, testAccounts("a-1")[0])
	var budgetErr *GatewayRequestWallBudgetExhaustedError
	if !errorsAs(err, &budgetErr) || budgetErr.BudgetKind != WallBudgetKindCoordination {
		t.Fatalf("expected coordination budget error, got %v", err)
	}
}

func TestReserveSameAccountRetrySecondWaitAbandoned(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	// 首次 Allowed 且 WaitMs=5：第二等待完成 → consume 拒绝 → 重试取消。
	locks := &scriptedLocks{acquireResults: []LockLeaseAcquire{
		{Allowed: true, LeaseID: "lease-1", WaitMs: 1},
	}}
	locks.consumeAllowed.Store(false)
	engine.Locks = locks
	attemptErr, _ := dispatchDeadAccount(t, engine, testAccounts("a-1")[0])
	if attemptErr == nil {
		t.Fatal("消耗拒绝后必须以失败结束")
	}
	if locks.consumeCalls.Load() != 1 {
		t.Fatalf("consume calls = %d", locks.consumeCalls.Load())
	}
	_ = attemptErr
}

func TestReserveSameAccountRetryNotReservedEndsRotation(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	// 无锁等待、消耗拒绝：默认 fakeLocks 行为 → 同账户重试耗尽后跳过。
	attemptErr, _ := dispatchDeadAccount(t, engine, testAccounts("a-1")[0])
	if attemptErr == nil {
		t.Fatal("全部尝试失败必须返回 UpstreamAttemptError")
	}
}

func TestReserveSameAccountRetryWallBudgetWindowExhausted(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	// 同账户重试窗口小于配置延迟：直接记录 same_account_retry_exhausted。
	settings := fastDispatchSettings()
	settings.TemporaryUnschedulableRetryIntervalSeconds = 30 // configuredDelayMs=30s
	driver := engine.Driver.(*fakeDriver)
	driver.urlByAccount = map[string][]string{"a-1": {"https://127.0.0.1:9/v1/chat/completions"}}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.Settings = settings
	// 墙钟预算收窄到 5s：剩余 - 预留 < 30s。
	args.RequestCoordination.GatewayRequestWallBudget = mustWallBudget(t, 5_000)
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
}

func mustWallBudget(t *testing.T, budgetMs int64) *gatewayrouting.GatewayRequestWallBudget {
	t.Helper()
	budget, err := gatewayrouting.NewGatewayRequestWallBudget(gatewayrouting.GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: NowMs(),
		BudgetMs:            &budgetMs,
	}, nil)
	if err != nil {
		t.Fatalf("wall budget: %v", err)
	}
	return budget
}

// TestDispatchRequestBodyOverrideReachesUpstream: 请求体覆盖进入上游。
func TestDispatchRequestBodyOverrideReachesUpstream(t *testing.T) {
	var mu chan []byte = make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buffer := make([]byte, 4096)
		n, _ := r.Body.Read(buffer)
		mu <- buffer[:n]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer server.Close()
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {server.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.RequestCoordination.RequestBodyOverride = &RequestBodyOverride{
		AccountID: "a-1",
		Body:      []byte(`{"model":"overridden-model","stream":false}`),
	}
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	select {
	case body := <-mu:
		if string(body) != `{"model":"overridden-model","stream":false}` {
			t.Fatalf("upstream body = %q", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("上游未收到请求")
	}
	if string(result.RequestBody) != `{"model":"overridden-model","stream":false}` {
		t.Fatalf("result body = %q", result.RequestBody)
	}
}

// reserveSameAccountRetryForTest 在 tracker 上登记并预留一次同账户重试，
// 返回引擎侧可消费的 RetryID（镜像 reserveSameAccountRetry 的登记链）。
func reserveSameAccountRetryForTest(t *testing.T, coordination *RequestCoordinationContext, account AccountCandidate, req *gatewaypreauth.GatewayRequest, fingerprint string) string {
	t.Helper()
	runtimeKey, err := gatewayAccountRuntimeKey(account)
	if err != nil {
		t.Fatalf("runtime key: %v", err)
	}
	protocolModelKey, err := gatewayrouting.GatewayAttemptProtocolModelKey(runtimeKey, account.ProtocolCode, account.ProtocolVersion, requestModelOrEmpty(req))
	if err != nil {
		t.Fatalf("protocol model key: %v", err)
	}
	identity := gatewayrouting.GatewayDispatchAttemptIdentity{
		AccountRuntimeKey:     runtimeKey,
		PhysicalCredentialKey: accountPhysicalCredentialKey(account),
		ProtocolModelKey:      protocolModelKey,
		KeyFingerprint:        fingerprint,
	}
	recorded, err := coordination.RequestAttemptTracker.TryRecordDispatchAttempt(gatewayrouting.GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: identity,
	})
	if err != nil || !recorded.Allowed {
		t.Fatalf("预登记失败: %+v %v", recorded, err)
	}
	reservation, err := coordination.RequestAttemptTracker.TryReserveSameAccountRetry(gatewayrouting.GatewaySameAccountRetryReservationInput{
		GatewayDispatchAttemptIdentity: identity,
		MaxRetries:                     2,
	})
	if err != nil || !reservation.Reserved {
		t.Fatalf("预留失败: %+v %v", reservation, err)
	}
	return reservation.RetryID
}

// TestDispatchSameAccountRetryStripsFingerprint: 非活动同账户重试上下文剥离
// 选中 Key 指纹后照常调度。
func TestDispatchSameAccountRetryStripsFingerprint(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, _, dispatcher := newTestEngine(t)
	dispatcher.keyScopedFailure.Store(true)
	partsDriver := &partsErrorDriver{errByAccountID: map[string]error{"a-1": errors.New("准备失败")}}
	partsDriver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	engine.Driver = partsDriver
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, nil)
	carried := multiKeyTestAccount("a-1", "key-a", "key-b")
	fingerprint := apiKeyFingerprint(engine.Config.Secret, "key-a")
	carried.SelectedAPIKeyFingerprint = &fingerprint
	retryID := reserveSameAccountRetryForTest(t, args.RequestCoordination, carried, req, fingerprint)
	args.RequestCoordination.SameAccountRetry = &SameAccountRetry{
		RetryID: retryID,
		Account: carried,
	}
	args.Accounts = []AccountCandidate{carried}
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	// 携带指纹在第二轮 Key 轮换被剥离：key-b 得到一次尝试。
	if dispatcher.handleErrorCalls.Load() < 2 {
		t.Fatalf("第二把 Key 必须在剥离指纹后被尝试, calls = %d", dispatcher.handleErrorCalls.Load())
	}
}

// TestDispatchConcurrencySlotDefaults: 并发槽缺省 MarkFirstOutput/Release 不 panic。
func TestDispatchConcurrencySlotDefaults(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	engine.Concurrency = defaultSlotStore{}
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	if _, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, testAccounts("a-1"))); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

// defaultSlotStore 返回不带 MarkFirstOutput/Release 的槽位。
type defaultSlotStore struct{}

func (defaultSlotStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (defaultSlotStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (defaultSlotStore) TryAcquireAsync(context.Context, string, int, AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	return ConcurrencySlot{Acquired: true, Current: 1, Limit: 4}, nil
}

// TestFetchEntryAuthorizedAccountMissingBindingErrors: 入口运行态键错误直接返回。
func TestFetchEntryAuthorizedAccountMissingBindingErrors(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	broken := testAccounts("a-1")[0]
	broken.AccountAccessType = "account_authorized"
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, []AccountCandidate{broken}))
	if err == nil || !strings.Contains(err.Error(), "授权账户运行态键缺少绑定上下文") {
		t.Fatalf("expected runtime key error, got %v", err)
	}
}

// TestFetchSameAccountRetryCarry: SameAccountRetry 上下文只调度携带账户。
func TestFetchSameAccountRetryCarry(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1", "a-2"))
	carried := testAccounts("a-1")[0]
	retryID := reserveSameAccountRetryForTest(t, args.RequestCoordination, carried, req, "")
	args.RequestCoordination.SameAccountRetry = &SameAccountRetry{
		RetryID: retryID,
		Account: carried,
	}
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// 即使 a-2 在候选中，也只允许携带的 a-1。
	if result.Account.ID != "a-1" {
		t.Fatalf("account = %s", result.Account.ID)
	}
}
