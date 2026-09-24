package gatewaydispatch

// circuitaudit_http400_skip_test.go — 复现发现 R5（确定性 400 也触发切号，
// 且失败响应会结算熔断 confirmation）。
// 状态：设计决策项（非 2xx 一律切号，error policy 可配置短路），本轮不修复，
// 留存为行为基线。
//
// 复现的缺陷：网关对上游失败响应不做确定性/瞬时性区分——生产装配点
// cmd/juhe-ai-gateway/chain_ports.go:579-580 对 gateway 流量无条件返回
// `Action: gatewaydispatch.FailedResponseActionSkipAccount`；引擎收到
// SkipAccount 后立即放弃当前账号切到下一候选。一个语义上确定性的 400
//（请求构造错误、模型不存在等，重试/切号都不可能成功）与瞬时 500 走完全
// 相同的切号路径，把候选池里剩下的账号也逐个烧掉。
//
// 该 SkipAccount 分支在 attemptoutcomes.go 原无条件调用
// accountCircuitAttempt.ReportFramingComplete(ctx)——与发现 R2（SUSPECT 被
// 失败 HTTP 响应"治愈"）是同一生产触发点；R2 修复后该分支已改调
// ReportUnknown，失败响应不再推进/关闭熔断状态（本测试不依赖该结算行为，
// 切号断言不受 R2 修复影响）。
//
// 层级选择说明：选择引擎级测试（FetchFirstAvailableUpstream + 现有
// newTestEngine fake fixture + httptest 上游）而非直接单测
// chain_ports.go 的 HandleFailedUpstreamResponse——后者在 cmd/
// juhe-ai-gateway 主包，构造其 dispatcher 依赖（audit/usage/error policy/
// codex recovery/probe health check）远超本发现所需；而引擎级测试能同时
// 证明"失败响应交给 failure dispatcher + 立即切到下一候选 + 400 不做
// 同账号重试"三个事实，是能覆盖本契约的最稳层级。
//
// 对照基线：同样的双账号场景下瞬时 500 会对同一账号重试到
// maxSameAccountRetries（dispatch_test.go:157-162，handleFailedCalls==3）；
// 400 是确定性错误，isTransientSameAccountHttpStatus(400)==false
// （dispatch_test.go:389-399 已覆盖分类），因此 handleFailedCalls==1。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// circuitAuditStatusRecordingDispatcher 包装 fakeFailureDispatcher，记录每次
// HandleFailedUpstreamResponse 收到的账号与上游状态码。
type circuitAuditStatusRecordingDispatcher struct {
	*fakeFailureDispatcher

	mu       sync.Mutex
	accounts []string
	statuses []int
}

func (d *circuitAuditStatusRecordingDispatcher) HandleFailedUpstreamResponse(ctx context.Context, input FailedUpstreamResponseInput) (FailedUpstreamResponseResult, error) {
	d.mu.Lock()
	d.accounts = append(d.accounts, input.Account.ID)
	status := 0
	if input.Response != nil {
		status = input.Response.Status()
	}
	d.statuses = append(d.statuses, status)
	d.mu.Unlock()
	return d.fakeFailureDispatcher.HandleFailedUpstreamResponse(ctx, input)
}

func TestCircuitAuditR5DeterministicHTTP400SkipsAccountImmediately(t *testing.T) {
	// a-1 的上游恒返回 400（确定性客户端错误），a-2 返回 200。
	badRequestServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"model not found","type":"invalid_request_error","code":"model_not_found"}}`))
	}))
	t.Cleanup(badRequestServer.Close)
	var okHits int
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		okHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-ok"}`))
	}))
	defer okServer.Close()

	engine, driver, fakeDispatcher := newTestEngine(t)
	recorder := &circuitAuditStatusRecordingDispatcher{
		fakeFailureDispatcher: fakeDispatcher,
	}
	engine.FailureDispatcher = recorder
	driver.urlByAccount = map[string][]string{
		"a-1": {badRequestServer.URL + "/v1/chat/completions"},
		"a-2": {okServer.URL + "/v1/chat/completions"},
	}
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), dispatchArgs(t, req, testAccounts("a-1", "a-2")))
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}

	// 引擎最终成功：切号后 a-2 返回 200。
	if result.Account.ID != "a-2" {
		t.Fatalf("最终账号 = %s, want a-2（400 后应立即切号到下一候选）", result.Account.ID)
	}
	if !result.Response.OK() {
		t.Fatalf("响应状态 = %d, want 2xx", result.Response.Status())
	}

	// 400 一次即切号：失败响应被交给 failure dispatcher（生产映射点
	// chain_ports.go:579-580 无条件 SkipAccount），且不做同账号重试
	// （对照：500 场景 handleFailedCalls==3，dispatch_test.go:160-162）。
	if calls := recorder.handleFailedCalls.Load(); calls != 1 {
		t.Fatalf("失败响应处理调用 = %d, want 1（确定性 400 不做同账号重试、直接切号）", calls)
	}
	recorder.mu.Lock()
	recordedAccounts := append([]string(nil), recorder.accounts...)
	recordedStatuses := append([]int(nil), recorder.statuses...)
	recorder.mu.Unlock()
	if len(recordedAccounts) != 1 || recordedAccounts[0] != "a-1" || recordedStatuses[0] != http.StatusBadRequest {
		t.Fatalf("失败记录 = (accounts=%#v, statuses=%#v), want ([a-1], [400])", recordedAccounts, recordedStatuses)
	}

	// a-2 确实被尝试过：第二个候选上游收到恰好一次请求并返回 200。
	if okHits != 1 {
		t.Fatalf("第二个候选上游命中 = %d, want 1（切号后必须实际尝试 a-2）", okHits)
	}
}
