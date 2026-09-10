package gatewaydispatch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 最终冲刺：post-cycle 可恢复等待的继续分支、亲和声明重排失败、客户端 IP
// 并发获取错误、同账户重试预留的范围错误、ReturnResponse 闭包消费。

// TestDispatchPostCycleRecoverableWaitContinuesThenExhausts: post-cycle 过滤
// 发现可恢复账户已解除抑制 → 等待预算内短暂重试直至预算耗尽。
func TestDispatchPostCycleRecoverableWaitContinuesThenExhausts(t *testing.T) {
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`bad gateway`))
	}))
	defer failServer.Close()
	engine, driver, _ := newTestEngine(t)
	engine.Suppression = &releaseOnNthFilter{releaseAfter: 3}
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
		"a-2": {failServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1", "a-2"))
	args.WaitForRecoverableFailures = true
	args.RequestCoordination.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(4_000, gatewaypreauth.SystemClock{})
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected UpstreamAttemptError, got %v", err)
	}
}

// errorAfterClaimAffinity 的 OrderAsync 在声明后的重排阶段报错。
type errorAfterClaimAffinity struct {
	fakeAffinity
	claimed atomic.Bool
}

func (e *errorAfterClaimAffinity) ClaimAsync(_ context.Context, _ string, _ string, _ AffinityScope) (string, bool) {
	e.claimed.Store(true)
	return "a-2", true
}

func (e *errorAfterClaimAffinity) OrderAsync(_ context.Context, accounts []AccountCandidate, _ string, _ AffinityOrderingOptions) ([]AccountCandidate, error) {
	if e.claimed.Load() {
		return nil, errors.New("亲和重排爆炸")
	}
	return accounts, nil
}

func TestPrepareDispatchAccountsClaimReorderError(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	affinity := &errorAfterClaimAffinity{}
	engine.Affinity = affinity
	input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
	input.SessionAffinityKey = "session-1"
	_, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err == nil || !strings.Contains(err.Error(), "亲和重排爆炸") {
		t.Fatalf("声明重排错误必须透传, got %v", err)
	}
}

// errorClientIPConcurrency 的 Acquire 恒报错。
type errorClientIPConcurrency struct{}

func (errorClientIPConcurrency) Acquire(context.Context, ClientIPConcurrencyInput) (ClientIPConcurrencyDecision, error) {
	return ClientIPConcurrencyDecision{}, errors.New("IP 并发爆炸")
}

func TestPrepareDispatchAccountsClientIPAcquireError(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	policy := gatewayruntimecache.GroupSchedulingPolicy{}
	engine.Concurrency = &mapBackedConcurrencyStore{current: map[string]int{}}
	engine.Affinity = &configurableAffinity{busy: false}
	engine.ClientIPConcurrency = errorClientIPConcurrency{}
	input := dispatchPreparationInput(t, testAccounts("a-1"))
	input.GroupAccess = highConcurrencyGroupAccess(&policy)
	if _, err := pipeline.PrepareDispatchAccounts(context.Background(), input); err == nil || !strings.Contains(err.Error(), "IP 并发爆炸") {
		t.Fatalf("获取错误必须透传, got %v", err)
	}
}

// TestDispatchSameAccountRetryReservationRangeError: 重试次数越界 → 预留错误
// 直接作为调度错误返回。
func TestDispatchSameAccountRetryReservationRangeError(t *testing.T) {
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream down","type":"server_error","code":"upstream_error"}}`))
	}))
	defer failServer.Close()
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.Settings.TemporaryUnschedulableRetryAttempts = -1
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "maxRetries") {
		t.Fatalf("预留范围错误必须透传, got %v", err)
	}
}

// TestDispatchReturnResponseClosuresAreSafe: return_response 结果闭包可安全消费。
func TestDispatchReturnResponseClosuresAreSafe(t *testing.T) {
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error","code":"rate_limit_exceeded"}}`))
	}))
	defer failServer.Close()
	engine, driver, _ := newTestEngine(t)
	engine.FailureDispatcher = &returnResponseDispatcher{}
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), fastDispatchArgs(t, req, testAccounts("a-1")))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.ConfirmHalfOpenSuccess() {
		t.Fatal("未启用自动状态变更时 half-open 成功确认返回 false")
	}
	// 无租约时释放返回 false。
	if result.ReleaseHalfOpenLease() {
		t.Fatal("无租约释放应返回 false")
	}
	if result.ReleaseAccountLockRetryLease == nil {
		t.Fatal("锁租约释放闭包必须存在")
	}
	result.ReleaseAccountLockRetryLease(false)
}
