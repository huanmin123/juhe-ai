package gatewaydispatch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 定向补齐引擎循环剩余分支：容量排队策略分支、可恢复等待恢复路径、
// precheck 半开等待、Key-model 准备错误、请求级 Key 安全上限、
// 响应失败处理的显式策略/换 Key/失败桥接分支与管道写失败。

// ---------------------------------------------------------------------------
// 容量排队：策略分支 + 就绪队列恢复
// ---------------------------------------------------------------------------

// freeAfterNStore 前 N 次 TryAcquire 失败，之后成功（模拟并发槽释放）。
type freeAfterNStore struct {
	failures atomic.Int64
	limit    int64
}

func (f *freeAfterNStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (f *freeAfterNStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (f *freeAfterNStore) TryAcquireAsync(context.Context, string, int, AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	if f.failures.Add(1) <= f.limit {
		return ConcurrencySlot{Acquired: false, Current: 4, Limit: 4, Lane: "text"}, nil
	}
	return ConcurrencySlot{Acquired: true, Current: 1, Limit: 4, Lane: "text"}, nil
}

func TestDispatchCapacityPolicyQueueReadyRecovers(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	store := &freeAfterNStore{limit: 1} // 第一次获取失败，队列等待后成功
	engine.Concurrency = store
	engine.Config.AccountConcurrencyRetryBudgetMs = 0 // 短重试不消耗服务器预算
	engine.HighConcurrencyQueue = &configurableQueue{ready: true}
	policy := gatewayruntimecache.GroupSchedulingPolicy{}
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.GroupSchedulingPolicy = &policy
	args.RequestCoordination.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{})
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("队列就绪后必须恢复调度: %v", err)
	}
	_ = result
	if result.Account.ID != "a-1" {
		t.Fatalf("account = %s", result.Account.ID)
	}
}

// nonTimeoutQueue 恒未就绪且原因非 timeout（触发重试延迟分支）。
type nonTimeoutQueue struct{}

func (nonTimeoutQueue) WaitForCapacity(context.Context, HighConcurrencyWaitInput) (QueueWaitResult, error) {
	return QueueWaitResult{Ready: false, Reason: "drained"}, nil
}

func TestDispatchCapacityPolicyQueueDrainedRetries(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	store := &freeAfterNStore{limit: 1}
	engine.Concurrency = store
	engine.Config.AccountConcurrencyRetryBudgetMs = 0
	engine.HighConcurrencyQueue = nonTimeoutQueue{}
	policy := gatewayruntimecache.GroupSchedulingPolicy{}
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.GroupSchedulingPolicy = &policy
	args.RequestCoordination.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(120, gatewaypreauth.SystemClock{})
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("排队后必须恢复调度: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("account = %s", result.Account.ID)
	}
}

func TestDispatchCapacityNoPolicyRetriesThenRecords(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	store := &freeAfterNStore{limit: 1}
	engine.Concurrency = store
	engine.Config.AccountConcurrencyRetryBudgetMs = 0
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.RequestCoordination.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(300, gatewaypreauth.SystemClock{})
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("无策略容量重试后必须恢复调度: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("account = %s", result.Account.ID)
	}
}

// ---------------------------------------------------------------------------
// 可恢复等待：等待中恢复 + precheck 半开范围
// ---------------------------------------------------------------------------

// hookWaiter 在等待刷新前执行钩子（模拟抑制解除）。
type hookWaiter struct {
	hook func()
}

func (h hookWaiter) WaitForState(_ context.Context, input SuppressionWaitInput) (SuppressionFilterResult, error) {
	if h.hook != nil {
		h.hook()
	}
	return input.Refresh(context.Background())
}

func TestDispatchRecoverableWaitContinuesAfterRecovery(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	suppression := &switchableSuppression{suppress: map[string]bool{"a-1": true}}
	engine.Suppression = suppression
	// 等待开始时解除抑制：Refresh 返回不再全部抑制 → 继续调度循环。
	engine.RecoverableWait = hookWaiter{hook: func() { suppression.set("a-1", false) }}
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.WaitForRecoverableFailures = true
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("等待恢复后必须完成调度: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("account = %s", result.Account.ID)
	}
}

// precheckScopeSuppression 全部抑制并携带 precheck 运行态范围。
type precheckScopeSuppression struct{}

func (precheckScopeSuppression) FilterAsync(_ context.Context, accounts []AccountCandidate, _ SuppressionFilterOptions) (SuppressionFilterResult, error) {
	return SuppressionFilterResult{
		Accounts:                        []AccountCandidate{},
		AllSuppressed:                   true,
		SuppressedCount:                 len(accounts),
		SuppressedAccountIDs:            accountIDs(accounts),
		PrecheckSuppressedAccountIDs:    accountIDs(accounts),
		PrecheckSuppressedRuntimeScopes: []PrecheckSuppressedRuntimeScope{{RuntimeKey: "a-1", Generation: 3}},
		AcquiredHalfOpenLeases:          []HalfOpenLease{},
	}, nil
}

func (precheckScopeSuppression) ResolveLocalSuppressionFilter(ctx context.Context, input LocalSuppressionPreflightInput) (*SuppressionFilterResult, bool, error) {
	result := localSuppressionBypassResult(input.Accounts)
	return &result, false, nil
}

func TestDispatchRecoverableWaitPrecheckScopesExhausted(t *testing.T) {
	engine, driver, _ := newTestEngine(t)
	engine.Suppression = precheckScopeSuppression{}
	engine.RecoverableWait = exhaustedWaiter{}
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.WaitForRecoverableFailures = true
	args.RequestCoordination.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(60, gatewaypreauth.SystemClock{})
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if attemptErr.LastAttempt == nil || !strings.Contains(attemptErr.LastAttempt.Message, "本地短期屏蔽") {
		t.Fatalf("lastAttempt = %#v", attemptErr.LastAttempt)
	}
}

// ---------------------------------------------------------------------------
// 同账户重试登记失败 → 请求内去重
// ---------------------------------------------------------------------------

func TestDispatchUnregisteredSameAccountRetryDeduplicates(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.RequestCoordination.SameAccountRetry = &SameAccountRetry{
		RetryID: "unregistered-retry",
		Account: testAccounts("a-1")[0],
	}
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.UpstreamURL != "account:request_deduplicated" {
		t.Fatalf("未登记的同账户重试必须去重, got %#v", attemptErr.LastAttempt)
	}
}

// ---------------------------------------------------------------------------
// 首字截止配置装配 + Key-model 准备错误 + 请求级安全上限
// ---------------------------------------------------------------------------

func TestDispatchWithNormalRouteFirstByteConfig(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	// speed-first 首字截止只作用于流式请求（非流式已豁免）。
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.RequestCoordination.NormalRouteFirstByteConfig = &gatewayrouting.NormalRouteFirstByteRuntimeConfig{
		SchedulingPreference: "speed_first",
		FirstByteDeadlineMs:  30_000,
	}
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.NormalRouteFirstByteDeadline == nil {
		t.Fatal("成功结果必须携带首字截止")
	}
	if result.OnFirstByteDeadline == nil {
		t.Fatal("成功结果必须携带首字截止处理器")
	}
	if result.FirstByteDeadlineCoordinator == nil {
		t.Fatal("成功结果必须携带首字协调器")
	}
}

func TestDispatchNonStreamExemptFromNormalRouteFirstByteDeadline(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	// 非流式即使配置了 speed-first 首字截止也豁免：上游生成完才返回首响应。
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.RequestCoordination.NormalRouteFirstByteConfig = &gatewayrouting.NormalRouteFirstByteRuntimeConfig{
		SchedulingPreference: "speed_first",
		FirstByteDeadlineMs:  30_000,
	}
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.NormalRouteFirstByteDeadline != nil {
		t.Fatalf("非流式必须豁免首字截止, got %+v", result.NormalRouteFirstByteDeadline)
	}
	if result.OnFirstByteDeadline != nil {
		t.Fatal("非流式不得携带首字截止处理器")
	}
	if result.FirstByteDeadlineCoordinator != nil {
		t.Fatal("非流式不得携带首字协调器")
	}
}

// errorKeyModelAdmission 返回准备错误（状态不可用语义）。
type errorKeyModelAdmission struct{}

func (errorKeyModelAdmission) Prepare(context.Context, gatewayaccounteffects.KeyModelRuntimeStore, gatewayaccounteffects.PrepareGatewayKeyModelAttemptInput) (gatewayaccounteffects.GatewayKeyModelAttemptPreparation, error) {
	return gatewayaccounteffects.GatewayKeyModelAttemptPreparation{}, errors.New("state store unavailable")
}

func TestDispatchKeyModelPrepareErrorRotatesKey(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	engine.KeyModel = errorKeyModelAdmission{}
	engine.Config.KeyModelForegroundQueuePollMs = 1
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	accounts := []AccountCandidate{multiKeyTestAccount("a-1", "key-a", "key-b")}
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, accounts))
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	// 两次准备错误轮换后 Key 池穷尽，最后尝试为池不可用。
	if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.UpstreamURL != "account:api_key_pool_unavailable" {
		t.Fatalf("准备错误轮换后应为池不可用, got %#v", attemptErr.LastAttempt)
	}
}

func TestDispatchRequestApiKeySafetyLimitTripsImmediately(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	engine.Config.AccountApiKeyRequestAttemptSafetyLimit = 0
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	accounts := []AccountCandidate{multiKeyTestAccount("a-1", "key-a", "key-b")}
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, accounts))
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if attemptErr.LastAttempt == nil || attemptErr.LastAttempt.UpstreamURL != "account:api_key_request_retry_budget_exhausted" {
		t.Fatalf("安全上限必须立即生效, got %#v", attemptErr.LastAttempt)
	}
}

// ---------------------------------------------------------------------------
// 准备与 URL 构建错误
// ---------------------------------------------------------------------------

type failingPrepareDriver struct {
	fakeDriver
	prepareErr error
	urlsErr    error
}

func (f *failingPrepareDriver) PrepareGatewayUpstreamAccount(ctx context.Context, account AccountCandidate) (AccountCandidate, error) {
	if f.prepareErr != nil {
		return AccountCandidate{}, f.prepareErr
	}
	return account, nil
}

func (f *failingPrepareDriver) BuildGatewayUpstreamURLsForAccount(ctx context.Context, account AccountCandidate, req *gatewaypreauth.GatewayRequest) ([]string, error) {
	if f.urlsErr != nil {
		return nil, f.urlsErr
	}
	return f.fakeDriver.BuildGatewayUpstreamURLsForAccount(ctx, account, req)
}

func TestDispatchPrepareAndURLErrorsPropagate(t *testing.T) {
	for index, testCase := range []struct {
		name      string
		driver    *failingPrepareDriver
		wantError string
	}{
		{"prepare", &failingPrepareDriver{prepareErr: errors.New("准备爆炸")}, "准备爆炸"},
		{"urls", &failingPrepareDriver{urlsErr: errors.New("URL 爆炸")}, "URL 爆炸"},
	} {
		engine, _, _ := newTestEngine(t)
		engine.Driver = testCase.driver
		okServer := sequentialServer(t, 0, 500)
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, testAccounts("a-1")))
		if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
			t.Fatalf("case %d: expected %q, got %v", index, testCase.wantError, err)
		}
		okServer.Close()
	}
}

// ---------------------------------------------------------------------------
// 响应失败处理分支
// ---------------------------------------------------------------------------

// failingFailureDispatcher 的失败响应处理返回错误。
type failingFailureDispatcher struct {
	fakeFailureDispatcher
	err error
}

func (f *failingFailureDispatcher) HandleFailedUpstreamResponse(ctx context.Context, input FailedUpstreamResponseInput) (FailedUpstreamResponseResult, error) {
	return FailedUpstreamResponseResult{}, f.err
}

func TestDispatchFailedResponseHandlerErrorCompletesAttempt(t *testing.T) {
	failingServer := sequentialServer(t, 1, http.StatusInternalServerError)
	defer failingServer.Close()
	engine, driver, _ := newTestEngine(t)
	handlerErr := errors.New("失败处理器爆炸")
	engine.FailureDispatcher = &failingFailureDispatcher{err: handlerErr}
	sink := &fakeAuditSink{}
	audit := &frozenAudit{sink: sink}
	driver.urlByAccount = map[string][]string{
		"a-1": {failingServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.AuditCapture = AuditCapture{Context: audit, Sink: sink}
	if _, err := engine.FetchFirstAvailableUpstream(context.Background(), args); !errors.Is(err, handlerErr) {
		t.Fatalf("失败处理错误必须透传, got %v", err)
	}
	if sink.completed != 1 {
		t.Fatalf("失败完成审计 = %d", sink.completed)
	}
}

// explicitPolicyDispatcher 报告显式策略失败。
type explicitPolicyDispatcher struct {
	fakeFailureDispatcher
}

func (*explicitPolicyDispatcher) HandleFailedUpstreamResponse(ctx context.Context, input FailedUpstreamResponseInput) (FailedUpstreamResponseResult, error) {
	return FailedUpstreamResponseResult{
		Action:      FailedResponseActionSkipAccount,
		FailureKind: FailureKindExplicitPolicy,
		LastAttempt: input.LastAttempt,
	}, nil
}

func TestDispatchExplicitPolicyFailureRecordsTerminal(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	engine, driver, _ := newTestEngine(t)
	engine.FailureDispatcher = &explicitPolicyDispatcher{}
	hotQualityCalls := 0
	engine.HotQualityAttemptFactory = func(input HotQualityLifecycleInput) HotQualityAttemptLifecycle {
		hotQualityCalls++
		return &recordingHotQualityLifecycle{}
	}
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"policy","type":"invalid_request_error","code":"policy_violation"}}`))
	}))
	defer failServer.Close()
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, testAccounts("a-1")))
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	if hotQualityCalls != 1 {
		t.Fatalf("热质量生命周期未创建, calls = %d", hotQualityCalls)
	}
}

// tryNextKeyDispatcher 请求级换 Key。
type tryNextKeyDispatcher struct {
	fakeFailureDispatcher
	calls atomic.Int64
}

func (t *tryNextKeyDispatcher) HandleFailedUpstreamResponse(ctx context.Context, input FailedUpstreamResponseInput) (FailedUpstreamResponseResult, error) {
	t.calls.Add(1)
	return FailedUpstreamResponseResult{
		Action:                  FailedResponseActionSkipAccount,
		LastAttempt:             input.LastAttempt,
		TryNextApiKeyForRequest: true,
	}, nil
}

func TestDispatchTryNextApiKeyForRequestRotates(t *testing.T) {
	failingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream down","type":"server_error","code":"upstream_error"}}`))
	}))
	defer failingServer.Close()
	engine, driver, _ := newTestEngine(t)
	engine.FailureDispatcher = &tryNextKeyDispatcher{}
	driver.urlByAccount = map[string][]string{
		"a-1": {failingServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	accounts := []AccountCandidate{multiKeyTestAccount("a-1", "key-a", "key-b")}
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, accounts))
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
	// 请求级换 Key 让第二把 Key 也经历一次失败响应。
	dispatcher := engine.FailureDispatcher.(*tryNextKeyDispatcher)
	if dispatcher.calls.Load() < 2 {
		t.Fatalf("请求级换 Key 必须尝试第二把 Key, calls = %d", dispatcher.calls.Load())
	}
	if attemptErr.LastAttempt == nil {
		t.Fatal("最后尝试必须保留")
	}
}

// TestDispatchFailedResponseWithCircuitReportsFraming: 短电路装配下失败响应
// 归帧完成。
func TestDispatchFailedResponseWithCircuitReportsFraming(t *testing.T) {
	okServer := sequentialServer(t, 0, 500)
	defer okServer.Close()
	memoryStore, err := gatewaycircuit.NewMemoryStore(gatewaycircuit.MemoryStoreOptions{Capacity: 1024})
	if err != nil {
		t.Fatalf("circuit store: %v", err)
	}
	circuits, err := gatewaycircuit.NewCircuitService(memoryStore, gatewaycircuit.ServiceOptions{})
	if err != nil {
		t.Fatalf("circuit service: %v", err)
	}
	engine, driver, _ := newTestEngine(t)
	engine.Circuits = circuits
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`bad gateway`))
	}))
	defer failServer.Close()
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	_, err = engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, testAccounts("a-1")))
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 管道写失败与检查截断
// ---------------------------------------------------------------------------

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("下游写失败") }

func TestPipeNonStreamUpstreamResponseWriteError(t *testing.T) {
	_, err := PipeNonStreamUpstreamResponse(context.Background(), strings.NewReader("payload"), failingWriter{}, NonStreamPipeInput{
		StartedAt: gatewayupstream.NowMs(),
		Signal:    context.Background(),
	})
	var pipeErr *NonStreamUpstreamBodyPipeError
	if !errorsAs(err, &pipeErr) {
		t.Fatalf("expected pipe error, got %v", err)
	}
	if !strings.Contains(pipeErr.Message, "下游写失败") {
		t.Fatalf("message = %q", pipeErr.Message)
	}
	if pipeErr.PartialResult.TransferredBytes != 7 {
		t.Fatalf("partial = %#v", pipeErr.PartialResult)
	}
}

func TestPipeInspectionLimitExceededRequireFullyBuffered(t *testing.T) {
	result, err := PipeNonStreamUpstreamResponseForInspection(context.Background(), strings.NewReader("body-too-large"), &strings.Builder{}, InspectableNonStreamPipeInput{
		NonStreamPipeInput:   NonStreamPipeInput{StartedAt: gatewayupstream.NowMs(), Signal: context.Background()},
		InspectBytes:         4,
		RequireFullyBuffered: true,
	})
	if err != nil {
		t.Fatalf("inspection: %v", err)
	}
	if !result.InspectionLimitExceeded || result.FullyBuffered {
		t.Fatalf("result = %#v", result)
	}
	if string(result.CompleteBody) != "body" {
		t.Fatalf("complete = %q", result.CompleteBody)
	}
}

func TestPipeInspectionBeforeDownstreamCommitError(t *testing.T) {
	commitErr := errors.New("提交校验失败")
	_, err := PipeNonStreamUpstreamResponseForInspection(context.Background(), strings.NewReader("payload-exceeds-limit"), &strings.Builder{}, InspectableNonStreamPipeInput{
		NonStreamPipeInput:     NonStreamPipeInput{StartedAt: gatewayupstream.NowMs(), Signal: context.Background()},
		InspectBytes:           4,
		BeforeDownstreamCommit: func([]byte) error { return commitErr },
	})
	if !errors.Is(err, commitErr) {
		t.Fatalf("expected commit error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// waitForAccountLockDelay：重复 token 幂等重放 → coordination
// ---------------------------------------------------------------------------

func TestWaitForAccountLockDelayReplayedToken(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	coordination := newTestCoordination(t)
	// 首次等待消费 token。
	if outcome, err := engine.waitForAccountLockDelay(context.Background(), "a-1", "lease-replay", 1, coordination.GatewayRequestWallBudget, coordination.RouteCoordinationBudget); err != nil || outcome != accountLockWaitCompleted {
		t.Fatalf("首次等待: %v %v", outcome, err)
	}
	// 同 token 重放 → coordination（BeginWait 幂等重放 != Applied）。
	outcome, err := engine.waitForAccountLockDelay(context.Background(), "a-1", "lease-replay", 1, coordination.GatewayRequestWallBudget, coordination.RouteCoordinationBudget)
	if err != nil || outcome != accountLockWaitCoordination {
		t.Fatalf("重放应返回 coordination: %v %v", outcome, err)
	}
}
