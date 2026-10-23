package gatewaydispatch

// 调度慢失败直切与墙钟收尾修复设计 §7 的引擎级差分用例：
//  1. A 慢失败直切——响应失败与传输失败两臂，耗时 ≥ 阈值不预留同账户重试并
//     输出 same_account_retry_skipped_slow_failure 审计；快失败保留既有预算。
//  2. C1 启动门槛——剩余预算扣渲染保留后不足最小尝试窗口时拒启（既有
//     wall 终态错误），充足时放行。
//  3. C2 尝试截止裁剪——普通路径首字超时按墙钟预提交线收窄，Unbounded 豁免。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// slowFailServer 返回先按 delayMs 慢速失败（failStatus 置位时回 HTTP 状态码，
// 否则中断连接制造传输失败）的假上游；release 在测试收尾时放行阻塞连接。
func slowFailServer(t *testing.T, delayMs int, failStatus int) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(time.Duration(delayMs) * time.Millisecond):
		case <-release:
			return
		case <-r.Context().Done():
			return
		}
		if failStatus > 0 {
			w.WriteHeader(failStatus)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream down","type":"server_error","code":"upstream_error"}}`))
			return
		}
		// 中断连接：已建连后无响应断开 = started transport failure。
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(func() {
		once.Do(func() { close(release) })
		server.Close()
	})
	return server
}

// slowOKServer 延迟 delayMs 后返回成功补全响应（Unbounded 豁免臂的慢上游）。
func slowOKServer(delayMs int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(time.Duration(delayMs) * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-ok"}`))
	}))
}

func withWallBudgetMs(t *testing.T, args *FetchFirstAvailableUpstreamArgs, acceptedAt, budgetMs int64) {
	t.Helper()
	budget, err := gatewayrouting.NewGatewayRequestWallBudget(gatewayrouting.GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: acceptedAt,
		BudgetMs:            &budgetMs,
	}, nil)
	if err != nil {
		t.Fatalf("NewGatewayRequestWallBudget: %v", err)
	}
	args.RequestCoordination.GatewayRequestWallBudget = budget
}

// TestDispatchSlowResponseFailureSkipsSameAccountRetry：A 差分（慢响应失败）。
// 500 慢失败（≥ 阈值）不再原地重试，直接轮换 a-2，审计携带
// same_account_retry_skipped_slow_failure。
func TestDispatchSlowResponseFailureSkipsSameAccountRetry(t *testing.T) {
	slowServer := slowFailServer(t, 120, http.StatusBadGateway)
	okServer := sequentialServer(t, 0, http.StatusBadGateway)
	engine, driver, dispatcher := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {slowServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1", "a-2"))
	args.Settings.SameAccountRetryFastFailMaxMs = 50
	audit := newRetryBackoffMetadataAudit()
	args.AuditCapture = AuditCapture{Context: audit, Sink: &fakeAuditSink{}}
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("慢失败应直切 a-2, got %s", result.Account.ID)
	}
	if dispatcher.handleFailedCalls.Load() != 1 {
		t.Fatalf("慢失败不得预留同账户重试, failed calls = %d", dispatcher.handleFailedCalls.Load())
	}
	skips := audit.entries["same_account_retry_skipped_slow_failure"]
	if len(skips) != 1 {
		t.Fatalf("缺少慢失败直切审计, entries = %v", audit.entries)
	}
	if skips[0]["retryReason"] != "upstream_http_response" {
		t.Fatalf("retryReason = %v", skips[0]["retryReason"])
	}
	elapsedMs, _ := skips[0]["elapsedMs"].(int64)
	thresholdMs, _ := skips[0]["thresholdMs"].(int64)
	if elapsedMs < 50 || thresholdMs != 50 {
		t.Fatalf("elapsedMs = %v, thresholdMs = %v", skips[0]["elapsedMs"], skips[0]["thresholdMs"])
	}
}

// TestDispatchSlowTransportFailureSkipsSameAccountRetry：A 差分（慢传输失败）。
// 建连后悬挂再断开（≥ 阈值）的传输失败同样直切下一候选。
func TestDispatchSlowTransportFailureSkipsSameAccountRetry(t *testing.T) {
	dropServer := slowFailServer(t, 120, 0)
	okServer := sequentialServer(t, 0, http.StatusBadGateway)
	engine, driver, dispatcher := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {dropServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1", "a-2"))
	args.Settings.SameAccountRetryFastFailMaxMs = 50
	audit := newRetryBackoffMetadataAudit()
	args.AuditCapture = AuditCapture{Context: audit, Sink: &fakeAuditSink{}}
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("慢传输失败应直切 a-2, got %s", result.Account.ID)
	}
	if dispatcher.handleFailedCalls.Load() != 0 {
		t.Fatalf("慢传输失败不得预留同账户重试, failed calls = %d", dispatcher.handleFailedCalls.Load())
	}
	skips := audit.entries["same_account_retry_skipped_slow_failure"]
	if len(skips) != 1 || skips[0]["retryReason"] != "upstream_transport_failure" {
		t.Fatalf("缺少慢传输失败直切审计, entries = %v", audit.entries)
	}
}

// TestDispatchFastFailureKeepsSameAccountRetry：A 差分（快失败保留）。
// 失败耗时 < 阈值时同账户重试预算语义不变（1 次初始 + 2 次原地重试）。
func TestDispatchFastFailureKeepsSameAccountRetry(t *testing.T) {
	failServer := slowFailServer(t, 0, http.StatusInternalServerError)
	okServer := sequentialServer(t, 0, http.StatusBadGateway)
	engine, driver, dispatcher := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {failServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1", "a-2"))
	args.Settings.SameAccountRetryFastFailMaxMs = 60_000
	audit := newRetryBackoffMetadataAudit()
	args.AuditCapture = AuditCapture{Context: audit, Sink: &fakeAuditSink{}}
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("快失败仍应轮换, got %s", result.Account.ID)
	}
	if dispatcher.handleFailedCalls.Load() != 3 {
		t.Fatalf("快失败应保留同账户重试预算, failed calls = %d", dispatcher.handleFailedCalls.Load())
	}
	if skips := audit.entries["same_account_retry_skipped_slow_failure"]; len(skips) != 0 {
		t.Fatalf("快失败不得输出慢失败审计, entries = %v", skips)
	}
}

// TestDispatchAttemptStartGateRejectsAndAllows：C1 引擎级差分。剩余 20s 扣
// 2s 渲染保留后不足 30s 最小窗口 → 拒启（既有 wall 终态错误）；剩余 40s 放行。
func TestDispatchAttemptStartGateRejectsAndAllows(t *testing.T) {
	okServer := sequentialServer(t, 0, http.StatusBadGateway)
	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)

	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.Settings.GatewayAttemptStartMinWindowMs = 30_000
	withWallBudgetMs(t, &args, gatewayupstream.NowMs(), 20_000)
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	var budgetErr *GatewayRequestWallBudgetExhaustedError
	if !errorsAs(err, &budgetErr) {
		t.Fatalf("剩余 20s 应拒启, got %v", err)
	}
	if budgetErr.MinimumMeaningfulAttemptMs != 30_000 {
		t.Fatalf("MinimumMeaningfulAttemptMs = %d", budgetErr.MinimumMeaningfulAttemptMs)
	}

	argsAllow := fastDispatchArgs(t, req, testAccounts("a-1"))
	argsAllow.Settings.GatewayAttemptStartMinWindowMs = 30_000
	withWallBudgetMs(t, &argsAllow, gatewayupstream.NowMs(), 40_000)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), argsAllow)
	if err != nil {
		t.Fatalf("剩余 40s 应放行: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("account = %s", result.Account.ID)
	}
}

// TestDispatchNormalAttemptDeadlineClippedByWallBudget：C2。尝试启动于剩
// 约 3s（首字超时 30s）时截止被裁到墙钟预提交线（约 1s），到点按既有超时
// 路径失败结算，总时长不越过墙钟。
func TestDispatchNormalAttemptDeadlineClippedByWallBudget(t *testing.T) {
	hangServer := slowFailServer(t, 6_000, http.StatusBadGateway)
	engine, driver, _ := newTestEngine(t)
	engine.Config.UpstreamRetryBackoffDelaysMs = []int64{5, 5, 5, 5}
	driver.urlByAccount = map[string][]string{
		"a-1": {hangServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	// 隔离 A/C1：本用例只验证 C2 裁剪，关同账户直切与启动门槛。
	args.Settings.SameAccountRetryFastFailMaxMs = 0
	args.Settings.GatewayAttemptStartMinWindowMs = 0
	withWallBudgetMs(t, &args, gatewayupstream.NowMs()-267_000, 270_000)
	startedMs := gatewayupstream.NowMs()
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	elapsedMs := gatewayupstream.NowMs() - startedMs
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("裁剪后应按超时失败结算, got %v", err)
	}
	if elapsedMs >= 5_000 {
		t.Fatalf("尝试截止未被墙钟裁剪: elapsed = %dms", elapsedMs)
	}
}

// TestDispatchUnboundedBudgetKeepsLaneFirstByteTimeout：C2 豁免臂。Unbounded
// （压缩）预算下首字超时保持 lane 配置值，慢上游 2s 正常交付。
func TestDispatchUnboundedBudgetKeepsLaneFirstByteTimeout(t *testing.T) {
	slowServer := slowOKServer(2_000)
	defer slowServer.Close()
	engine, driver, _ := newTestEngine(t)
	engine.Config.UpstreamRetryBackoffDelaysMs = []int64{5, 5, 5, 5}
	driver.urlByAccount = map[string][]string{
		"a-1": {slowServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	args := fastDispatchArgs(t, req, testAccounts("a-1"))
	args.Settings.SameAccountRetryFastFailMaxMs = 0
	args.Settings.GatewayAttemptStartMinWindowMs = 0
	withWallBudgetMs(t, &args, gatewayupstream.NowMs()-267_000, 270_000)
	args.RequestCoordination.GatewayRequestWallBudget = args.RequestCoordination.GatewayRequestWallBudget.WithoutLimit()
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("Unbounded 预算不得裁剪: %v", err)
	}
	if !result.Response.OK() {
		t.Fatalf("status = %d", result.Response.Status())
	}
}
