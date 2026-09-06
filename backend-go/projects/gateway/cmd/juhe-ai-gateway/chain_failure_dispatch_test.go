package main

// G16 failure-dispatch 接线（response/failure-dispatch.ts →
// chainFailureDispatcher）的回归测试：
//
//   - 派发决策树：账户诊断流量 → return_response（探针必须看到供应商真实终态
//     响应，响应层 routes.ts:1550 ok 门把非 2xx+SSE 错误体按 non-stream 渲染）；
//     非 gateway 流量 → 遗忘会话亲和后 return_response；gateway 流量 → 有界
//     读取失败体 + audit/usage 记录 + skip_account（候选切换）+ 同账户换 Key
//     决策事实。
//   - 请求错误：下游关闭分支按 downstream_closed 归因记录；传输失败分支按
//     timeout/connection/read_incomplete 归类。
//   - 端到端：mock 上游 429+SSE 错误体（stream 请求）→ 客户端收到 non-stream
//     错误契约；429 JSON → 同账户重试决策后成功；5xx → 下一候选；全部失败 →
//     耗尽出口固定文案。

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// ---------------------------------------------------------------------------
// unit fakes
// ---------------------------------------------------------------------------

// failureDispatchAuditSink captures the attempt-level audit calls.
type failureDispatchAuditSink struct {
	completions []gatewaydispatch.CompleteAttemptInput
	records     []gatewaydispatch.FailedDispatchAttemptInput
}

func (s *failureDispatchAuditSink) StartAttempt(gatewaydispatch.StartAttemptInput) string { return "attempt_1" }

func (s *failureDispatchAuditSink) CompleteAttempt(_ string, input gatewaydispatch.CompleteAttemptInput) {
	s.completions = append(s.completions, input)
}

func (s *failureDispatchAuditSink) RecordFailedDispatchAttempt(input gatewaydispatch.FailedDispatchAttemptInput) {
	s.records = append(s.records, input)
}

// failureDispatchAffinity records ForgetAsync calls (only the consumed method
// is implemented; the embedded nil interface covers the rest).
type failureDispatchAffinity struct {
	forgotten []string
	gatewaydispatch.SessionAffinityPort
}

func (a *failureDispatchAffinity) ForgetAsync(_ context.Context, sessionAffinityKey, accountID string) error {
	a.forgotten = append(a.forgotten, sessionAffinityKey+"/"+accountID)
	return nil
}

// failureDispatchUpstreamResponse performs a real in-process upstream request
// so the failed response carries the true status/content-type/body pipeline.
func failureDispatchUpstreamResponse(t *testing.T, status int, contentType, body string) *gatewaydispatch.GatewayUpstreamResponse {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	response, err := gatewaydispatch.RequestUpstream(context.Background(), server.URL+"/v1/chat/completions",
		gatewaydispatch.UpstreamRequestOptions{
			Method: http.MethodPost,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   []byte(`{}`),
		}, gatewaydispatch.TransportDeps{})
	if err != nil {
		t.Fatalf("request upstream: %v", err)
	}
	return response
}

func newFailureDispatcherForTest(affinity gatewaydispatch.SessionAffinityPort) *chainFailureDispatcher {
	return &chainFailureDispatcher{affinity: affinity}
}

func gatewayFailedResponseInput(response *gatewaydispatch.GatewayUpstreamResponse, sink *failureDispatchAuditSink, trafficSource string) gatewaydispatch.FailedUpstreamResponseInput {
	return gatewaydispatch.FailedUpstreamResponseInput{
		UsageContext:     gatewaypreauth.GatewayFailureUsageContext{TrafficSource: trafficSource},
		AuditCapture:     gatewaydispatch.AuditCapture{Sink: sink},
		AuditAttemptID:   "attempt_1",
		Account:          gatewaydispatch.AccountCandidate{ID: "acc_1", Name: "账户一"},
		UpstreamURL:      "https://upstream.example/chat",
		Response:         response,
		AttemptStartedAt: 1728000000000,
		AttemptIndex:     1,
		AuditAttemptIndex: 1,
		SessionAffinityKey: "aff-key",
		LastAttempt: &gatewaydispatch.UpstreamAttempt{
			AccountID: "acc_1", UpstreamURL: "https://upstream.example/chat",
			Status: response.Status(), HasStatus: true,
		},
	}
}

// ---------------------------------------------------------------------------
// decision tree: failed upstream response
// ---------------------------------------------------------------------------

// TestChainFailureDispatcherDiagnosticTrafficReturnsResponse：账户诊断流量必须
// 拿到 return_response（供应商真实终态响应原样交给响应层），响应体保持未消费。
func TestChainFailureDispatcherDiagnosticTrafficReturnsResponse(t *testing.T) {
	response := failureDispatchUpstreamResponse(t, http.StatusTooManyRequests,
		"text/event-stream", "data: {\"error\":{\"message\":\"rate limited\"}}\n\n")
	sink := &failureDispatchAuditSink{}
	affinity := &failureDispatchAffinity{}
	dispatcher := newFailureDispatcherForTest(affinity)

	result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(),
		gatewayFailedResponseInput(response, sink, "manual_account_test"))
	if err != nil {
		t.Fatalf("handle failed upstream response: %v", err)
	}
	if result.Action != gatewaydispatch.FailedResponseActionReturnResponse {
		t.Fatalf("action=%s want return_response", result.Action)
	}
	if result.Response != response {
		t.Fatal("diagnostic traffic must observe the original response object")
	}
	// 响应体未被派发器消费：诊断侧仍可读到供应商错误体。
	raw, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		t.Fatalf("response body must stay consumable: %v", readErr)
	}
	if !strings.Contains(string(raw), "rate limited") {
		t.Fatalf("response body changed: %s", raw)
	}
	if len(sink.completions) != 0 || len(sink.records) != 0 {
		t.Fatalf("diagnostic branch must not close the audit attempt: %+v", sink)
	}
	if len(affinity.forgotten) != 0 {
		t.Fatalf("diagnostic branch must not forget affinity: %v", affinity.forgotten)
	}
}

// TestChainFailureDispatcherNonGatewayForgetsAffinityThenReturns：非 gateway
// 流量先遗忘会话亲和，再 return_response。
func TestChainFailureDispatcherNonGatewayForgetsAffinityThenReturns(t *testing.T) {
	response := failureDispatchUpstreamResponse(t, http.StatusBadGateway, "application/json", `{"error":"bad gateway"}`)
	sink := &failureDispatchAuditSink{}
	affinity := &failureDispatchAffinity{}
	dispatcher := newFailureDispatcherForTest(affinity)

	result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(),
		gatewayFailedResponseInput(response, sink, "hybrid_scoring"))
	if err != nil {
		t.Fatalf("handle failed upstream response: %v", err)
	}
	if result.Action != gatewaydispatch.FailedResponseActionReturnResponse {
		t.Fatalf("action=%s want return_response", result.Action)
	}
	if result.Response != response {
		t.Fatal("non-gateway traffic must observe the original response object")
	}
	if len(affinity.forgotten) != 1 || affinity.forgotten[0] != "aff-key/acc_1" {
		t.Fatalf("affinity forgets = %v", affinity.forgotten)
	}
}

// TestChainFailureDispatcherGatewaySkipEnrichesAttempt：gateway 流量走候选切换
// ——有界捕获失败体、重建 lastAttempt（响应头/失败体/解析 JSON）、完成 audit
// 尝试、关闭失败响应体、skip_account 带 opaque_http 失败分类。
func TestChainFailureDispatcherGatewaySkipEnrichesAttempt(t *testing.T) {
	const body = `{"error":{"message":"rate limited","code":"rate_limit_exceeded"}}`
	response := failureDispatchUpstreamResponse(t, http.StatusTooManyRequests, "application/json", body)
	sink := &failureDispatchAuditSink{}
	affinity := &failureDispatchAffinity{}
	dispatcher := newFailureDispatcherForTest(affinity)

	input := gatewayFailedResponseInput(response, sink, "gateway")
	input.LastAttempt = &gatewaydispatch.UpstreamAttempt{Message: "先前错误"}
	result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input)
	if err != nil {
		t.Fatalf("handle failed upstream response: %v", err)
	}
	if result.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("action=%s want skip_account", result.Action)
	}
	if result.FailureKind != chainFailureKindOpaqueHTTP {
		t.Fatalf("failureKind=%s want opaque_http", result.FailureKind)
	}
	attempt := result.LastAttempt
	if attempt == nil {
		t.Fatal("lastAttempt must be rebuilt")
	}
	if attempt.Status != http.StatusTooManyRequests || !attempt.HasStatus {
		t.Fatalf("attempt status = %d/%v", attempt.Status, attempt.HasStatus)
	}
	if attempt.Message != "先前错误" {
		t.Fatalf("previous attempt facts must carry over: %q", attempt.Message)
	}
	if attempt.ResponseHeaders["content-type"] != "application/json" {
		t.Fatalf("response headers = %v", attempt.ResponseHeaders)
	}
	if attempt.ResponseBodyText != body {
		t.Fatalf("response body text = %q", attempt.ResponseBodyText)
	}
	errorValue, _ := attempt.ParsedResponseBody["error"].(map[string]any)
	if errorValue == nil || errorValue["code"] != "rate_limit_exceeded" {
		t.Fatalf("parsed response body = %v", attempt.ParsedResponseBody)
	}
	if len(sink.completions) != 1 {
		t.Fatalf("audit completions = %d", len(sink.completions))
	}
	completion := sink.completions[0]
	if completion.Success || completion.ErrorPhase != "upstream_response" || completion.ErrorMessage != body {
		t.Fatalf("audit completion wrong: %+v", completion)
	}
	if _, readErr := response.Body.Read(make([]byte, 1)); readErr == nil {
		t.Fatal("failed response body must be closed on the skip path")
	}
	if result.KeyScopedFailure || result.PendingApiKeyFailure != nil {
		t.Fatalf("single-key account must not rotate keys: %+v", result)
	}
}

// TestChainFailureDispatcherGatewayBodyTruncated：失败体超过捕获上限时截断
// （upstreamErrorBodyCaptureBytes），不吞掉 skip 决策。正文用内存读取器替换：
// 单元边界是派发器的有界捕获，不依赖真实传输通道。
func TestChainFailureDispatcherGatewayBodyTruncated(t *testing.T) {
	response := failureDispatchUpstreamResponse(t, http.StatusInternalServerError, "text/plain", "ignored")
	large := strings.Repeat("x", int(chainFailureErrorBodyCaptureBytes)+100)
	response.Body = io.NopCloser(strings.NewReader(large))
	sink := &failureDispatchAuditSink{}
	dispatcher := newFailureDispatcherForTest(nil)

	result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(),
		gatewayFailedResponseInput(response, sink, "gateway"))
	if err != nil {
		t.Fatalf("handle failed upstream response: %v", err)
	}
	if result.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("action=%s want skip_account", result.Action)
	}
	if got := len(result.LastAttempt.ResponseBodyText); got != int(chainFailureErrorBodyCaptureBytes) {
		t.Fatalf("captured body length = %d want %d", got, chainFailureErrorBodyCaptureBytes)
	}
}

// TestChainFailureDispatcherGatewayKeyRotationFacts：多 Key 账户的自动同账户
// 换 Key 决策——默认激活（keyScopedFailure + pendingApiKeyFailure），预提交瞬态
// 响应推迟，Key 运行态禁用时只保留 keyScoped 事实。
func TestChainFailureDispatcherGatewayKeyRotationFacts(t *testing.T) {
	fingerprint := "fp_1"
	account := gatewaydispatch.AccountCandidate{
		ID: "acc_1", Name: "账户一",
		APIKeys:                   []string{"key-a", "key-b"},
		SelectedAPIKeyFingerprint: &fingerprint,
	}
	response := failureDispatchUpstreamResponse(t, http.StatusTooManyRequests, "application/json", `{"error":{"message":"rate limited"}}`)
	sink := &failureDispatchAuditSink{}
	dispatcher := newFailureDispatcherForTest(nil)

	input := gatewayFailedResponseInput(response, sink, "gateway")
	input.Account = account
	input.AccountStateMutationEnabled = true
	result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input)
	if err != nil {
		t.Fatalf("handle failed upstream response: %v", err)
	}
	if !result.KeyScopedFailure {
		t.Fatal("alternative keys must activate the same-account key rotation")
	}
	if result.PendingApiKeyFailure == nil {
		t.Fatal("pending api key failure must be captured for confirmation")
	}
	if result.PendingApiKeyFailure.Status != "temporary_unavailable" ||
		result.PendingApiKeyFailure.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("pending failure wrong: %+v", result.PendingApiKeyFailure)
	}

	// 预提交瞬态响应推迟自动换 Key（dispatcher 拥有同凭据有界重试）。
	input.DeferAutomaticSameAccountKeyRotation = true
	deferred, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input)
	if err != nil {
		t.Fatalf("handle deferred failure: %v", err)
	}
	if deferred.KeyScopedFailure || deferred.PendingApiKeyFailure != nil {
		t.Fatalf("deferred rotation facts wrong: %+v", deferred)
	}
	input.DeferAutomaticSameAccountKeyRotation = false

	// Key 运行态禁用：keyScoped 事实保留，pending 观测不采集。
	disabled := account
	disabled.APIKeyRuntimeStateDisabled = true
	input.Account = disabled
	stateDisabled, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input)
	if err != nil {
		t.Fatalf("handle state-disabled failure: %v", err)
	}
	if !stateDisabled.KeyScopedFailure {
		t.Fatal("key-scoped fact must survive the runtime-state disablement")
	}
	if stateDisabled.PendingApiKeyFailure != nil {
		t.Fatalf("state-disabled account must not capture a pending failure: %+v", stateDisabled.PendingApiKeyFailure)
	}
}

// ---------------------------------------------------------------------------
// decision tree: upstream request error
// ---------------------------------------------------------------------------

func upstreamRequestErrorInput(err error, sink *failureDispatchAuditSink, lastAttempt *gatewaydispatch.UpstreamAttempt) gatewaydispatch.UpstreamRequestErrorInput {
	return gatewaydispatch.UpstreamRequestErrorInput{
		UsageContext:       gatewaypreauth.GatewayFailureUsageContext{TrafficSource: "gateway"},
		AuditCapture:       gatewaydispatch.AuditCapture{Sink: sink},
		AuditAttemptID:     "attempt_1",
		Account:            gatewaydispatch.AccountCandidate{ID: "acc_1", Name: "账户一"},
		UpstreamURL:        "https://upstream.example/chat",
		AttemptStartedAt:   1728000000000,
		AttemptIndex:       1,
		AuditAttemptIndex:  1,
		SessionAffinityKey: "aff-key",
		LastAttempt:        lastAttempt,
		Error:              err,
	}
}

// TestChainFailureDispatcherRequestErrorDownstreamClosed：下游关闭按
// downstream_closed 归因记录，重建的 lastAttempt 保留匹配尝试的状态。
func TestChainFailureDispatcherRequestErrorDownstreamClosed(t *testing.T) {
	sink := &failureDispatchAuditSink{}
	affinity := &failureDispatchAffinity{}
	dispatcher := newFailureDispatcherForTest(affinity)

	input := upstreamRequestErrorInput(
		&gatewaydispatch.UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true},
		sink,
		&gatewaydispatch.UpstreamAttempt{AccountID: "acc_1", UpstreamURL: "https://upstream.example/chat", Status: 200, HasStatus: true})
	result, err := dispatcher.HandleUpstreamRequestError(context.Background(), input)
	if err != nil {
		t.Fatalf("handle upstream request error: %v", err)
	}
	if result.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("action=%s want skip_account", result.Action)
	}
	if result.LastAttempt == nil || result.LastAttempt.Message != "下游连接关闭" {
		t.Fatalf("last attempt wrong: %+v", result.LastAttempt)
	}
	if !result.LastAttempt.HasStatus || result.LastAttempt.Status != 200 {
		t.Fatalf("matching attempt status must carry over: %+v", result.LastAttempt)
	}
	if len(sink.completions) != 1 || sink.completions[0].ErrorPhase != "downstream" {
		t.Fatalf("audit completions = %+v", sink.completions)
	}
	if len(affinity.forgotten) != 1 {
		t.Fatalf("affinity forgets = %v", affinity.forgotten)
	}
}

// TestChainFailureDispatcherRequestErrorTransportKinds：传输失败分支的消息与
// transport failure kind 分类（timeout / read_incomplete / connection）。
func TestChainFailureDispatcherRequestErrorTransportKinds(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		lastAttempt *gatewaydispatch.UpstreamAttempt
		wantKind    string
	}{
		{"timeout", &gatewaydispatch.UpstreamRequestTimeoutError{Message: "上游请求 30s 后仍未返回首个响应"}, nil, gatewaydispatch.TransportFailureKindTimeout},
		{"read_incomplete", errors.New("连接被重置"), &gatewaydispatch.UpstreamAttempt{Status: 200, HasStatus: true}, gatewaydispatch.TransportFailureKindReadIncomplete},
		{"connection", errors.New("连接被重置"), nil, gatewaydispatch.TransportFailureKindConnection},
	}
	for _, testCase := range cases {
		sink := &failureDispatchAuditSink{}
		dispatcher := newFailureDispatcherForTest(nil)
		result, err := dispatcher.HandleUpstreamRequestError(context.Background(),
			upstreamRequestErrorInput(testCase.err, sink, testCase.lastAttempt))
		if err != nil {
			t.Fatalf("%s: handle upstream request error: %v", testCase.name, err)
		}
		if result.Action != gatewaydispatch.FailedResponseActionSkipAccount {
			t.Fatalf("%s: action=%s", testCase.name, result.Action)
		}
		if result.LastAttempt == nil || result.LastAttempt.TransportFailureKind != testCase.wantKind {
			t.Fatalf("%s: transport kind = %+v want %s", testCase.name, result.LastAttempt, testCase.wantKind)
		}
		if result.LastAttempt == nil || result.LastAttempt.Message != testCase.err.Error() {
			t.Fatalf("%s: message wrong: %+v", testCase.name, result.LastAttempt)
		}
		if len(sink.completions) != 1 || sink.completions[0].ErrorPhase != "upstream_request" {
			t.Fatalf("%s: audit completions = %+v", testCase.name, sink.completions)
		}
	}
}

// TestChainFailureDispatcherOpaqueFailoverDisallowed：opaque HTTP 失败不得换
// 兄弟 Key（failure-dispatch.ts:73-79）。
func TestChainFailureDispatcherOpaqueFailoverDisallowed(t *testing.T) {
	dispatcher := newFailureDispatcherForTest(nil)
	if dispatcher.IsOpaqueUpstreamFailoverAllowed(nil) {
		t.Fatal("opaque upstream failover must stay disallowed")
	}
}

// ---------------------------------------------------------------------------
// end to end: the /v1 chain over the wired dispatcher
// ---------------------------------------------------------------------------

// TestGatewayChain429SSEFailureYieldsNonStreamErrorContract：stream 请求遇
// 上游 429 + text/event-stream 错误体——候选切换（含同账户重试）耗尽后，客户端
// 收到 non-stream 固定文案错误契约，绝不收到 SSE 事件流（V1 端到端）。
func TestGatewayChain429SSEFailureYieldsNonStreamErrorContract(t *testing.T) {
	fixture := newChainFixture(t)
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"rate limited by upstream\"}}\n\n"))
	}))
	defer upstream.Close()
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": upstream.URL}), fixture.accountID); err != nil {
		t.Fatalf("update account credentials: %v", err)
	}

	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool")))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()

	status, contentType, raw := chainV1StreamChatRequest(t, server.URL, fixture.apiKeySecret,
		`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"你好"}]}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503: %s", status, raw)
	}
	if strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("stream request must not receive an SSE error contract: %s", contentType)
	}
	if !strings.Contains(contentType, "application/json") {
		t.Fatalf("non-stream error contract content type wrong: %s", contentType)
	}
	if !strings.Contains(raw, "上游暂时不可用，请重试") {
		t.Fatalf("fixed exhaustion copy missing: %s", raw)
	}
	if strings.Contains(raw, "data:") {
		t.Fatalf("SSE frame leaked to the client: %s", raw)
	}
	if atomic.LoadInt32(&hits) < 2 {
		t.Fatalf("upstream hits=%d want the 429 skip to drive the same-account retry", hits)
	}
}

// TestGatewayChain429JSONRetriesSameAccountThenSucceeds：429 JSON 是重试决策
// ——同账户重试第二次命中上游成功，客户端拿到 200 内容。
func TestGatewayChain429JSONRetriesSameAccountThenSucceeds(t *testing.T) {
	fixture := newChainFixture(t)
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited","code":"rate_limit_exceeded"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-retry","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"重试后内容"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": upstream.URL}), fixture.accountID); err != nil {
		t.Fatalf("update account credentials: %v", err)
	}

	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool")))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()

	status, raw := chainV1ChatRequest(t, server.URL, fixture.apiKeySecret,
		`{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status=%d want 200 after the 429 retry: %s", status, raw)
	}
	if !strings.Contains(raw, "重试后内容") {
		t.Fatalf("retried content missing: %s", raw)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("upstream hits=%d want 2 (initial 429 + same-account retry)", got)
	}
}

// TestGatewayChain5xxFailsOverToNextCandidate：5xx skip_account 后切下一候选
// 账户成功返回；首个候选的重试预算耗尽前绝不提前失败。
func TestGatewayChain5xxFailsOverToNextCandidate(t *testing.T) {
	fixture := newChainFixture(t)
	now := "2026-09-04T00:00:00.000Z"
	var firstHits, secondHits int32
	firstUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&firstHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream exploded"}}`))
	}))
	defer firstUpstream.Close()
	secondUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&secondHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-next","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"候选二内容"},"finish_reason":"stop"}]}`))
	}))
	defer secondUpstream.Close()

	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, credentials_encrypted, deleted_at, health_check_model
		) VALUES ('acc_2', ?, 'openai', 'prof_1', 'openai', 'v1', '账户二', 'api_key', 'active', 1, ?, NULL, 'gpt-test')`,
		fixture.systemAccount, mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key-2", "base_url": secondUpstream.URL}))
	// local_priority 把首候选固定为 acc_1（chainPriorityRank 先读
	// group_accounts.local_priority，低值在前，避免依赖 CJK 名称 collator）。
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, local_priority, created_at) VALUES (?, ?, 'acc_2', 1, 10, ?)`,
		fixture.groupID, fixture.systemAccount, now)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at) VALUES ('acc_2', 'openai', 'gpt-test', ?)`, now)
	seed(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key-1", "base_url": firstUpstream.URL}), fixture.accountID)

	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool")))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()

	status, raw := chainV1ChatRequest(t, server.URL, fixture.apiKeySecret,
		`{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status=%d want 200 via the next candidate: %s", status, raw)
	}
	if !strings.Contains(raw, "候选二内容") {
		t.Fatalf("next-candidate content missing: %s", raw)
	}
	if atomic.LoadInt32(&firstHits) < 1 {
		t.Fatalf("first candidate hits=%d want at least one 500 failure", firstHits)
	}
	if atomic.LoadInt32(&secondHits) != 1 {
		t.Fatalf("second candidate hits=%d want 1", secondHits)
	}
}

// TestGatewayChainAllUpstreamHTTPFailuresRenderExhaustion：全部候选都返回 5xx
// 时进入耗尽出口——客户端拿到固定文案 503，诊断串不外泄。
func TestGatewayChainAllUpstreamHTTPFailuresRenderExhaustion(t *testing.T) {
	fixture := newChainFixture(t)
	now := "2026-09-04T00:00:00.000Z"
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream 502 detail for acc_1"}}`))
	}))
	defer upstream.Close()

	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, credentials_encrypted, deleted_at, health_check_model
		) VALUES ('acc_2', ?, 'openai', 'prof_1', 'openai', 'v1', '账户二', 'api_key', 'active', 1, ?, NULL, 'gpt-test')`,
		fixture.systemAccount, mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key-2", "base_url": upstream.URL}))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at) VALUES (?, ?, 'acc_2', 1, ?)`,
		fixture.groupID, fixture.systemAccount, now)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at) VALUES ('acc_2', 'openai', 'gpt-test', ?)`, now)
	seed(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key-1", "base_url": upstream.URL}), fixture.accountID)

	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool")))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()

	status, raw := chainV1ChatRequest(t, server.URL, fixture.apiKeySecret,
		`{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503: %s", status, raw)
	}
	if !strings.Contains(raw, "上游暂时不可用，请重试") {
		t.Fatalf("exhaustion copy wrong: %s", raw)
	}
	if strings.Contains(raw, "upstream 502 detail") || strings.Contains(raw, "最后一次尝试") {
		t.Fatalf("upstream diagnostics leaked to client: %s", raw)
	}
	if atomic.LoadInt32(&hits) < 2 {
		t.Fatalf("upstream hits=%d want both candidates attempted", hits)
	}
}

// chainV1StreamChatRequest posts a stream chat completion and returns the
// status, content type and body.
func chainV1StreamChatRequest(t *testing.T, serverURL, apiKey, body string) (int, string, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, serverURL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return response.StatusCode, response.Header.Get("Content-Type"), string(raw)
}
