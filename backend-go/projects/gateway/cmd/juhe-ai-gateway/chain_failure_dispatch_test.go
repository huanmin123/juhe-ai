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
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accountkeystates"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// unit fakes
// ---------------------------------------------------------------------------

// failureDispatchMetadata captures one addGatewayMetadata call.
type failureDispatchMetadata struct {
	label    string
	metadata map[string]any
}

// failureDispatchAuditSink captures the attempt-level audit calls. It
// implements both the attempt sink and the frozen capture context, so tests
// may mount it either as Context or Sink.
type failureDispatchAuditSink struct {
	completions []gatewaydispatch.CompleteAttemptInput
	records     []gatewaydispatch.FailedDispatchAttemptInput
	metadata    []failureDispatchMetadata
}

func (s *failureDispatchAuditSink) StartAttempt(gatewaydispatch.StartAttemptInput) string {
	return "attempt_1"
}

func (s *failureDispatchAuditSink) CompleteAttempt(_ string, input gatewaydispatch.CompleteAttemptInput) {
	s.completions = append(s.completions, input)
}

func (s *failureDispatchAuditSink) RecordFailedDispatchAttempt(input gatewaydispatch.FailedDispatchAttemptInput) {
	s.records = append(s.records, input)
}

func (s *failureDispatchAuditSink) BindContext(gatewaypreauth.AuditGatewayContext) {}

func (s *failureDispatchAuditSink) AddGatewayMetadata(label string, metadata map[string]any) {
	s.metadata = append(s.metadata, failureDispatchMetadata{label: label, metadata: metadata})
}

func (s *failureDispatchAuditSink) Finalize(gatewaypreauth.AuditFinalizeInput) {}

func (s *failureDispatchAuditSink) metadataByLabel(label string) *failureDispatchMetadata {
	for index := range s.metadata {
		if s.metadata[index].label == label {
			return &s.metadata[index]
		}
	}
	return nil
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
		UsageContext:       gatewaypreauth.GatewayFailureUsageContext{TrafficSource: trafficSource},
		AuditCapture:       gatewaydispatch.AuditCapture{Sink: sink},
		AuditAttemptID:     "attempt_1",
		Account:            gatewaydispatch.AccountCandidate{ID: "acc_1", Name: "账户一"},
		UpstreamURL:        "https://upstream.example/chat",
		Response:           response,
		AttemptStartedAt:   1728000000000,
		AttemptIndex:       1,
		AuditAttemptIndex:  1,
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
// codex encrypted-content compatibility recovery (G18 接线①)
// ---------------------------------------------------------------------------

// codexRecoveryResponsesInput 构造 /v1/responses 的 gateway 失败响应输入：
// codex 协议账户 + 携带加密上下文的请求体 + 上游拒绝错误体。
func codexRecoveryResponsesInput(t *testing.T, response *gatewaydispatch.GatewayUpstreamResponse, sink *failureDispatchAuditSink, requestBody string) gatewaydispatch.FailedUpstreamResponseInput {
	t.Helper()
	raw := httptest.NewRequest(http.MethodPost, "http://gateway.local/v1/responses", nil)
	input := gatewayFailedResponseInput(response, sink, "gateway")
	// addGatewayMetadata 只在 Context 通道分发；同时挂载以便断言恢复元数据。
	input.AuditCapture.Context = sink
	input.Req = gatewaypreauth.NewGatewayRequest(raw)
	input.Account = gatewaydispatch.AccountCandidate{
		ID: "acc_1", Name: "账户一",
		ProviderCode: "openai", ProtocolCode: "openai", ProtocolVersion: "v1",
	}
	input.RequestBody = []byte(requestBody)
	return input
}

// TestChainFailureDispatcherCodexRecoveryReplaysSanitizedBody：上游明确拒绝
// 加密上下文且请求体含可移除的 encrypted_content 时，派发器必须返回
// retry_with_compatibility_recovery——恢复体去除加密状态、保留其余输入与
// previous_response_id，语义重试 ID 带 cleanup 信号，audit 记录 retry 元数据，
// 且不进入 skip 决策（failure-dispatch.ts:292-315）。
func TestChainFailureDispatcherCodexRecoveryReplaysSanitizedBody(t *testing.T) {
	const errorBody = `{"error":{"message":"Encrypted content could not be decoded","code":"invalid_encrypted_content"}}`
	response := failureDispatchUpstreamResponse(t, http.StatusBadRequest, "application/json", errorBody)
	sink := &failureDispatchAuditSink{}
	dispatcher := newFailureDispatcherForTest(nil)
	input := codexRecoveryResponsesInput(t, response, sink,
		`{"model":"gpt-5","input":[{"type":"reasoning","summary":[],"encrypted_content":"rejected-payload"},{"type":"message","role":"user","content":[{"type":"output_text","text":"hi"}]}],"previous_response_id":"resp_prev","store":false}`)

	result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input)
	if err != nil {
		t.Fatalf("handle failed upstream response: %v", err)
	}
	if result.Action != gatewaydispatch.FailedResponseActionRetryWithCompatibilityRecovery {
		t.Fatalf("action=%s want retry_with_compatibility_recovery", result.Action)
	}
	if result.FailureKind != chainFailureKindCompatibilityRecovery {
		t.Fatalf("failureKind=%s want compatibility_recovery", result.FailureKind)
	}
	if result.Response != nil {
		t.Fatal("the recovery decision must not hand the rejected response to the response layer")
	}
	var sanitized map[string]any
	if err := json.Unmarshal(result.Recovery.Body, &sanitized); err != nil {
		t.Fatalf("recovery body must stay JSON: %v", err)
	}
	rawRecovery := string(result.Recovery.Body)
	if strings.Contains(rawRecovery, "encrypted_content") {
		t.Fatalf("recovery body still carries encrypted content: %s", rawRecovery)
	}
	inputItems, _ := sanitized["input"].([]any)
	if len(inputItems) != 1 {
		t.Fatalf("sanitized input items = %v want the single message item", sanitized["input"])
	}
	if sanitized["previous_response_id"] != "resp_prev" {
		t.Fatalf("previous_response_id must be preserved: %v", sanitized["previous_response_id"])
	}
	if result.Recovery.SemanticRetryID != "codex_encrypted_content_cleanup:invalid_encrypted_content" {
		t.Fatalf("semantic retry id = %q", result.Recovery.SemanticRetryID)
	}
	if result.LastAttempt == nil || result.LastAttempt.UpstreamURL != input.UpstreamURL || !result.LastAttempt.HasStatus {
		t.Fatalf("last attempt must stay enriched: %+v", result.LastAttempt)
	}
	retryMetadata := sink.metadataByLabel("codex_encrypted_content_recovery_retry")
	if retryMetadata == nil {
		t.Fatalf("retry audit metadata missing: %+v", sink.metadata)
	}
	if retryMetadata.metadata["accountId"] != "acc_1" || retryMetadata.metadata["transport"] != "http" {
		t.Fatalf("retry metadata identity wrong: %v", retryMetadata.metadata)
	}
	if retryMetadata.metadata["signal"] != gatewaycodex.SignalInvalidEncryptedContent ||
		retryMetadata.metadata["strategy"] != "codex_encrypted_content_cleanup" {
		t.Fatalf("retry metadata signal wrong: %v", retryMetadata.metadata)
	}
	if retryMetadata.metadata["preservedPreviousResponseID"] != true {
		t.Fatalf("retry metadata must report the preserved previous_response_id: %v", retryMetadata.metadata)
	}
	if len(sink.completions) != 1 {
		t.Fatalf("the audit attempt still closes before the recovery: %+v", sink.completions)
	}
}

// TestChainFailureDispatcherCodexRecoverySkippedFallsToSkipFlow：信号命中但
// 请求体无可移除加密内容（not_recoverable）→ 记录 skipped 元数据后继续既有
// skip 流；完全无信号的失败不产生任何 recovery 元数据（failure-dispatch.ts:316-327）。
func TestChainFailureDispatcherCodexRecoverySkippedFallsToSkipFlow(t *testing.T) {
	const errorBody = `{"error":{"message":"Encrypted content could not be decoded","code":"invalid_encrypted_content"}}`
	response := failureDispatchUpstreamResponse(t, http.StatusBadRequest, "application/json", errorBody)
	sink := &failureDispatchAuditSink{}
	dispatcher := newFailureDispatcherForTest(nil)
	input := codexRecoveryResponsesInput(t, response, sink,
		`{"model":"gpt-5","input":[{"type":"message","role":"user","content":[{"type":"output_text","text":"hi"}]}]}`)

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
	skipped := sink.metadataByLabel("codex_encrypted_content_recovery_skipped")
	if skipped == nil {
		t.Fatalf("skipped audit metadata missing: %+v", sink.metadata)
	}
	if skipped.metadata["signal"] != gatewaycodex.SignalInvalidEncryptedContent ||
		skipped.metadata["reason"] != gatewaycodex.RecoveryReasonNoRemovableEncryptedContent {
		t.Fatalf("skipped metadata wrong: %v", skipped.metadata)
	}

	// 无信号：不产生 recovery 元数据，skip 流保持原样。
	plainSink := &failureDispatchAuditSink{}
	plainResponse := failureDispatchUpstreamResponse(t, http.StatusInternalServerError, "application/json", `{"error":{"message":"boom"}}`)
	plainInput := codexRecoveryResponsesInput(t, plainResponse, plainSink,
		`{"model":"gpt-5","input":[{"type":"reasoning","summary":[],"encrypted_content":"kept-payload"}]}`)
	plainResult, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), plainInput)
	if err != nil {
		t.Fatalf("handle plain failure: %v", err)
	}
	if plainResult.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("plain action=%s want skip_account", plainResult.Action)
	}
	if plainSink.metadataByLabel("codex_encrypted_content_recovery_retry") != nil ||
		plainSink.metadataByLabel("codex_encrypted_content_recovery_skipped") != nil {
		t.Fatalf("signal-less failure must not emit recovery metadata: %+v", plainSink.metadata)
	}
}

// ---------------------------------------------------------------------------
// client-source avoidance 记录接线（G18 接线②）
// ---------------------------------------------------------------------------

// avoidanceRecordRequestInput 构造 gateway 传输失败的请求错误输入：完整派发
// 身份（system/api-key/group/endpoint/client-ip）+ openai 协议账户。
func avoidanceRecordRequestInput(t *testing.T, sink *failureDispatchAuditSink, trafficSource string) gatewaydispatch.UpstreamRequestErrorInput {
	t.Helper()
	raw := httptest.NewRequest(http.MethodPost, "http://gateway.local/v1/chat/completions", nil)
	input := upstreamRequestErrorInput(errors.New("连接被重置"), sink, nil)
	input.Req = gatewaypreauth.NewGatewayRequest(raw)
	input.UsageContext.TrafficSource = trafficSource
	input.UsageContext.SystemAccountID = "sys_1"
	input.UsageContext.APIKeyID = "key_1"
	input.UsageContext.GroupID = "grp_1"
	input.UsageContext.Endpoint = "/v1/chat/completions"
	input.UsageContext.ClientIP = "203.0.113.9"
	input.Account = gatewaydispatch.AccountCandidate{
		ID: "acc_1", Name: "账户一",
		ProviderCode: "openai", ProtocolCode: "openai", ProtocolVersion: "v1",
	}
	return input
}

// newAvoidanceDispatcherForTest 组装带 G18 避让协作器的派发器（进程内记忆
// 驱动，源身份 HMAC secret 固定）。
func newAvoidanceDispatcherForTest() (*chainFailureDispatcher, *gatewaycodex.TurnRetryService, *chainClientSourceAvoidance) {
	strategyDeps := &gatewaycodex.ClientStrategyDeps{
		Source: &gatewaycodex.SourceIdentityResolver{Secret: "unit-secret"},
	}
	turnRetry := &gatewaycodex.TurnRetryService{Secret: "unit-secret"}
	adapter := &chainClientSourceAvoidance{turnRetry: turnRetry}
	dispatcher := &chainFailureDispatcher{
		clientStrategy: strategyDeps,
		turnRetry:      turnRetry,
	}
	return dispatcher, turnRetry, adapter
}

// TestChainFailureDispatcherClientSourceAvoidanceRecordsTransportFailure：
// gateway 传输失败按失败时身份重新解析客户端策略并记录来源级失败——两次不同
// observation 达到阈值后激活避让，消费适配器把失败账户排到新鲜账户之后；
// 相同 observationId 去重不重复计数。
func TestChainFailureDispatcherClientSourceAvoidanceRecordsTransportFailure(t *testing.T) {
	sink := &failureDispatchAuditSink{}
	dispatcher, _, adapter := newAvoidanceDispatcherForTest()
	input := avoidanceRecordRequestInput(t, sink, "gateway")
	input.AuditAttemptID = "attempt_1"
	if _, err := dispatcher.HandleUpstreamRequestError(context.Background(), input); err != nil {
		t.Fatalf("first transport failure: %v", err)
	}
	input.AuditAttemptID = "attempt_2"
	if _, err := dispatcher.HandleUpstreamRequestError(context.Background(), input); err != nil {
		t.Fatalf("second transport failure: %v", err)
	}

	strategy := dispatcher.clientStrategy.ResolveOpenAIGatewayClientStrategy(input.Req, gatewaycodex.ClientStrategyIdentity{
		SystemAccountID: "sys_1", APIKeyID: "key_1", GroupID: "grp_1",
		Endpoint: "/v1/chat/completions", ClientIP: "203.0.113.9",
		ProviderCode: "openai", ProtocolCode: "openai", ProtocolVersion: "v1",
	})
	if !strategy.AllowClientSourceAccountAvoidance {
		t.Fatal("the resolved strategy must allow client-source avoidance")
	}
	accounts := []gatewaydispatch.AccountCandidate{{ID: "acc_1"}, {ID: "acc_2"}}
	order, err := adapter.OrderAsync(context.Background(), accounts, gatewaypreauth.ClientStrategyContext{Opaque: strategy}, nil)
	if err != nil {
		t.Fatalf("avoidance order: %v", err)
	}
	if !order.Applied || !order.ThresholdReached {
		t.Fatalf("avoidance must activate after two failures: %+v", order)
	}
	if order.FailureCount != 2 {
		t.Fatalf("failure count = %d want 2", order.FailureCount)
	}
	if len(order.AvoidedAccountIDs) != 1 || order.AvoidedAccountIDs[0] != "acc_1" {
		t.Fatalf("avoided accounts = %v", order.AvoidedAccountIDs)
	}
	if order.Accounts[0].ID != "acc_2" || order.Accounts[1].ID != "acc_1" {
		t.Fatalf("fresh accounts must dispatch first: %v/%v", order.Accounts[0].ID, order.Accounts[1].ID)
	}

	// 相同 observationId（audit attempt id）重复投递不重复计数。
	input.AuditAttemptID = "attempt_1"
	if _, err := dispatcher.HandleUpstreamRequestError(context.Background(), input); err != nil {
		t.Fatalf("duplicate transport failure: %v", err)
	}
	deduped, err := adapter.OrderAsync(context.Background(), accounts, gatewaypreauth.ClientStrategyContext{Opaque: strategy}, nil)
	if err != nil {
		t.Fatalf("deduped avoidance order: %v", err)
	}
	if deduped.FailureCount != 2 {
		t.Fatalf("duplicate observation must not count: failure count = %d", deduped.FailureCount)
	}
}

// TestChainFailureDispatcherClientSourceAvoidanceGuards：非 gateway 流量、缺
// 源身份（无 client-ip）、未装配协作器三种情况都不记录（Node: 缺失 source
// key 时避让天然关闭）。
func TestChainFailureDispatcherClientSourceAvoidanceGuards(t *testing.T) {
	// 非 gateway 流量。
	hybridSink := &failureDispatchAuditSink{}
	hybridDispatcher, _, hybridAdapter := newAvoidanceDispatcherForTest()
	hybridInput := avoidanceRecordRequestInput(t, hybridSink, "hybrid_scoring")
	if _, err := hybridDispatcher.HandleUpstreamRequestError(context.Background(), hybridInput); err != nil {
		t.Fatalf("hybrid transport failure: %v", err)
	}
	hybridOrder, err := hybridAdapter.OrderAsync(context.Background(),
		[]gatewaydispatch.AccountCandidate{{ID: "acc_1"}},
		gatewaypreauth.ClientStrategyContext{}, nil)
	if err != nil {
		t.Fatalf("hybrid avoidance order: %v", err)
	}
	if hybridOrder.Applied || hybridOrder.FailureCount != 0 {
		t.Fatalf("non-gateway traffic must not record: %+v", hybridOrder)
	}

	// 无 client-ip：源身份缺失 → 策略不允许避让。
	noIPSink := &failureDispatchAuditSink{}
	noIPDispatcher, _, noIPAdapter := newAvoidanceDispatcherForTest()
	noIPInput := avoidanceRecordRequestInput(t, noIPSink, "gateway")
	noIPInput.UsageContext.ClientIP = ""
	if _, err := noIPDispatcher.HandleUpstreamRequestError(context.Background(), noIPInput); err != nil {
		t.Fatalf("no-ip transport failure: %v", err)
	}
	noIPOrder, err := noIPAdapter.OrderAsync(context.Background(),
		[]gatewaydispatch.AccountCandidate{{ID: "acc_1"}},
		gatewaypreauth.ClientStrategyContext{}, nil)
	if err != nil {
		t.Fatalf("no-ip avoidance order: %v", err)
	}
	if noIPOrder.Applied || noIPOrder.FailureCount != 0 {
		t.Fatalf("missing source identity must not record: %+v", noIPOrder)
	}

	// 未装配协作器：不 panic，skip 决策不变。
	bareSink := &failureDispatchAuditSink{}
	bareDispatcher := newFailureDispatcherForTest(nil)
	bareInput := avoidanceRecordRequestInput(t, bareSink, "gateway")
	bareResult, err := bareDispatcher.HandleUpstreamRequestError(context.Background(), bareInput)
	if err != nil {
		t.Fatalf("bare transport failure: %v", err)
	}
	if bareResult.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("bare action=%s want skip_account", bareResult.Action)
	}
}

// TestChainClientSourceAvoidanceAdapterPassthrough：无 G18 策略上下文
// （Opaque 为空）时消费适配器保持直通——装配降级与 Node 无避让状态语义一致。
func TestChainClientSourceAvoidanceAdapterPassthrough(t *testing.T) {
	_, turnRetry, adapter := newAvoidanceDispatcherForTest()
	_ = turnRetry
	accounts := []gatewaydispatch.AccountCandidate{{ID: "acc_1"}, {ID: "acc_2"}}
	order, err := adapter.OrderAsync(context.Background(), accounts, gatewaypreauth.ClientStrategyContext{}, nil)
	if err != nil {
		t.Fatalf("passthrough order: %v", err)
	}
	if order.Applied || order.ThresholdReached || order.FailureCount != 0 {
		t.Fatalf("passthrough must keep the scheduling order: %+v", order)
	}
	if len(order.Accounts) != 2 || order.Accounts[0].ID != "acc_1" || order.Accounts[1].ID != "acc_2" {
		t.Fatalf("passthrough accounts wrong: %+v", order.Accounts)
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

// TestGatewayChainResponsesRecoversRejectedEncryptedContent：端到端恢复桥
// ——/v1/responses 请求携带加密上下文，上游首次明确拒绝
// （invalid_encrypted_content），派发器返回兼容性恢复决策后引擎以同一账户重放
// 清理后的请求体（语义重试），第二次命中成功；客户端拿到 200 响应。
func TestGatewayChainResponsesRecoversRejectedEncryptedContent(t *testing.T) {
	fixture := newChainFixture(t)
	var hits int32
	bodies := make(chan string, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		select {
		case bodies <- string(raw):
		default:
		}
		if atomic.AddInt32(&hits, 1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Encrypted content could not be decoded","code":"invalid_encrypted_content"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-recovered","object":"response","status":"completed","model":"gpt-test","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"恢复后内容"}]}]}`))
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

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(
		`{"model":"gpt-test","stream":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"你好"}]},{"type":"reasoning","summary":[],"encrypted_content":"rejected-payload"}],"previous_response_id":"resp_prev"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixture.apiKeySecret)
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST /v1/responses: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200 after the compatibility recovery: %s", response.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "resp-recovered") {
		t.Fatalf("recovered upstream response missing: %s", raw)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("upstream hits=%d want 2 (rejected attempt + sanitized semantic retry)", got)
	}
	// 两次命中按序捕获：第一次原始体带加密内容，第二次语义重试必须已清理。
	firstBody := <-bodies
	secondBody := <-bodies
	if !strings.Contains(firstBody, "encrypted_content") {
		t.Fatalf("first attempt body must be the original request: %s", firstBody)
	}
	if strings.Contains(secondBody, "encrypted_content") {
		t.Fatalf("semantic retry must replay the sanitized body: %s", secondBody)
	}
	if !strings.Contains(secondBody, "resp_prev") {
		t.Fatalf("sanitized body must preserve previous_response_id: %s", secondBody)
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

// ---------------------------------------------------------------------------
// 显式账户错误策略：决策矩阵（account-error-policy.service.ts
// decideAccountErrorPolicy）与状态写侧（chain_error_policy_effects.go）
// ---------------------------------------------------------------------------

// fixedErrorPolicyClock 提供固定时钟（2026-09-01T10:00:00Z）。
var fixedErrorPolicyClock = func() time.Time {
	return time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
}

func newFixedErrorPolicyService(pool func(gatewaydispatch.AccountCandidate) bool) *chainErrorPolicyService {
	return newChainErrorPolicyService(chainErrorPolicyDeps{Now: fixedErrorPolicyClock, PoolIsolationEnabled: pool})
}

// errorPolicyAccount 构造决策输入的候选账户。
func errorPolicyAccount(credentials map[string]any) gatewaydispatch.AccountCandidate {
	if credentials == nil {
		credentials = map[string]any{}
	}
	return gatewaydispatch.AccountCandidate{ID: "acc_1", Name: "账户一", Type: "api_key", ProviderCode: "openai", Credentials: credentials}
}

// errorPolicyDecide 便捷入口：status + 失败体 → 决策。失败体事实与生产面
// parseFailureBodyFacts 同构（JSON 体带载荷投影，非 JSON 体载荷为空）。
func errorPolicyDecide(t *testing.T, service *chainErrorPolicyService, account gatewaydispatch.AccountCandidate, status int, body string) *accountErrorPolicyDecision {
	t.Helper()
	var parsedBody map[string]any
	if parsed := gatewayresponse.ParseGatewayNonStreamJsonBody(body, true, nil); parsed.Status == gatewayresponse.NonStreamJSONStatusValid {
		if mapped, ok := parsed.Value.(map[string]any); ok {
			parsedBody = mapped
		}
	}
	decision, err := service.Decide(account, status, nil, body, parsedBody, gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 30})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	return decision
}

// TestChainErrorPolicySystemRuleMatrix：系统额度不足规则的命中/排除矩阵与
// 系统决策形状（rate_limited 冷却 + system 来源 + api_key generic 模式）。
func TestChainErrorPolicySystemRuleMatrix(t *testing.T) {
	service := newFixedErrorPolicyService(nil)
	cases := []struct {
		name    string
		status  int
		body    string
		matches bool
	}{
		{"402 无码命中", http.StatusPaymentRequired, `{"error":{"message":"请求失败"}}`, true},
		{"402 余额不足文本", http.StatusPaymentRequired, `{"error":{"message":"账户余额不足，请充值"}}`, true},
		{"403 稳定错误码", http.StatusForbidden, `{"error":{"code":"insufficient_quota","message":"You exceeded your quota"}}`, true},
		{"403 非额度标识排除", http.StatusForbidden, `{"error":{"code":"content_policy_violation","message":"blocked"}}`, false},
		{"429 不属于系统规则", http.StatusTooManyRequests, `{"error":{"code":"rate_limit_exceeded"}}`, false},
		{"200 无决策", http.StatusOK, `{"error":{"code":"insufficient_quota"}}`, false},
	}
	for _, testCase := range cases {
		decision := errorPolicyDecide(t, service, errorPolicyAccount(nil), testCase.status, testCase.body)
		if !testCase.matches {
			if decision != nil {
				t.Fatalf("%s: decision = %+v want nil", testCase.name, decision)
			}
			continue
		}
		if decision == nil {
			t.Fatalf("%s: decision = nil want the system quota decision", testCase.name)
		}
		if decision.Action != decisionActionCooldown || decision.RuleSource != "system" ||
			decision.CooldownStatus != cooldownStatusRateLimited || decision.RuleID != systemInsufficientQuotaRuleID {
			t.Fatalf("%s: decision = %+v", testCase.name, decision)
		}
		if decision.CooldownUntil == "" {
			t.Fatalf("%s: cooldownUntil must be derived (api_key generic recovery)", testCase.name)
		}
		if decision.QuotaRecoveryMode != "generic" {
			t.Fatalf("%s: quotaRecoveryMode = %q want generic", testCase.name, decision.QuotaRecoveryMode)
		}
	}
}

// TestChainErrorPolicyQuotaRecoveryHint：显式恢复 hint 优先于策略边界
// （reset_at 族字段 + retry-after 响应头）。
func TestChainErrorPolicyQuotaRecoveryHint(t *testing.T) {
	service := newFixedErrorPolicyService(nil)
	account := errorPolicyAccount(nil)

	body := `{"error":{"code":"insufficient_quota","message":"quota exceeded","reset_at":1790000000}}`
	decision := errorPolicyDecide(t, service, account, http.StatusPaymentRequired, body)
	// 1790000000 是纪元秒（< 1e10 按秒转毫秒）。
	if decision == nil || decision.QuotaRecoveryMode != "explicit_reset" ||
		decision.QuotaRecoveryHintSource != "reset_at" || decision.CooldownUntil != "2026-09-21T14:13:20.000Z" {
		t.Fatalf("reset_at hint decision = %+v", decision)
	}

	decisionWithHeader, err := service.Decide(account, http.StatusPaymentRequired,
		http.Header{"Retry-After": []string{"120"}}, `{"error":{"code":"insufficient_quota"}}`, nil,
		gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 30})
	if err != nil {
		t.Fatalf("decide with retry-after: %v", err)
	}
	if decisionWithHeader == nil || decisionWithHeader.QuotaRecoveryHintSource != "retry_after" ||
		decisionWithHeader.CooldownUntil != "2026-09-01T10:02:00.000Z" {
		t.Fatalf("retry-after hint decision = %+v", decisionWithHeader)
	}
}

// TestChainErrorPolicyOverridesSuppressSystemRule：账户覆盖 delete/replace
// 抑制系统规则 —— delete 后无决策；replace 后账户规则接管匹配。
func TestChainErrorPolicyOverridesSuppressSystemRule(t *testing.T) {
	service := newFixedErrorPolicyService(nil)
	body := `{"error":{"code":"insufficient_quota","message":"insufficient quota"}}`

	deleted := errorPolicyAccount(map[string]any{
		"error_handling_rule_overrides": []any{map[string]any{"system_rule_id": systemInsufficientQuotaRuleID, "action": "delete"}},
	})
	if decision := errorPolicyDecide(t, service, deleted, http.StatusPaymentRequired, body); decision != nil {
		t.Fatalf("delete override must suppress the system rule: %+v", decision)
	}

	replaced := errorPolicyAccount(map[string]any{
		"error_handling_rule_overrides": []any{map[string]any{"system_rule_id": systemInsufficientQuotaRuleID, "action": "replace", "rule_index": float64(0)}},
		"error_handling_rules": []any{map[string]any{
			"enabled": true, "name": "自定义额度", "priority": float64(1), "action": "temp_unschedulable",
			"status_codes": []any{float64(402)},
		}},
	})
	decision := errorPolicyDecide(t, service, replaced, http.StatusPaymentRequired, body)
	if decision == nil || decision.RuleSource != "account" || decision.RuleName != "自定义额度" ||
		decision.Action != decisionActionCooldown || decision.CooldownStatus != cooldownStatusTemporaryUnavailable {
		t.Fatalf("replace override decision = %+v", decision)
	}
	// temp_unschedulable 冷却来自系统设置（30 分钟）。
	if decision.CooldownUntil != "2026-09-01T10:30:00.000Z" {
		t.Fatalf("temp_unschedulable cooldownUntil = %q", decision.CooldownUntil)
	}
}

// TestChainErrorPolicyAccountRulePriorityAndActions：账户规则 priority 升序
// 先命中先赢；retry_next / error_disabled / rate_limited(duration) 动作映射；
// 关键字与状态码维度过滤；无规则默认无决策。
func TestChainErrorPolicyAccountRulePriorityAndActions(t *testing.T) {
	service := newFixedErrorPolicyService(nil)
	account := errorPolicyAccount(map[string]any{
		"error_handling_rules": []any{
			map[string]any{
				"enabled": true, "name": "低优先级禁用", "priority": float64(5), "action": "error_disabled",
				"status_codes": []any{float64(500)},
			},
			map[string]any{
				"enabled": true, "name": "高优先级换号", "priority": float64(2), "action": "retry_next",
				"status_codes": []any{float64(500)},
			},
			map[string]any{
				"enabled": false, "name": "停用规则", "priority": float64(1), "action": "retry_next",
				"status_codes": []any{float64(500)},
			},
		},
	})
	decision := errorPolicyDecide(t, service, account, http.StatusInternalServerError, `{"error":{"message":"boom"}}`)
	if decision == nil || decision.Action != decisionActionRetryNext || decision.RuleName != "高优先级换号" || decision.RuleSource != "account" {
		t.Fatalf("priority decision = %+v", decision)
	}

	disabled := errorPolicyAccount(map[string]any{
		"error_handling_rules": []any{map[string]any{
			"enabled": true, "name": "上游崩溃禁用", "priority": float64(1), "action": "error_disabled",
			"status_codes": []any{float64(500)},
		}},
	})
	decision = errorPolicyDecide(t, service, disabled, http.StatusInternalServerError, `{"error":{"message":"boom"}}`)
	if decision == nil || decision.Action != decisionActionDisable || decision.RuleName != "上游崩溃禁用" {
		t.Fatalf("disable decision = %+v", decision)
	}

	rateLimited := errorPolicyAccount(map[string]any{
		"error_handling_rules": []any{map[string]any{
			"enabled": true, "name": "两小时限流", "priority": float64(1), "action": "rate_limited",
			"error_codes":    []any{"rate_limit_exceeded"},
			"reset_strategy": "duration", "duration_hours": float64(2),
		}},
	})
	decision = errorPolicyDecide(t, service, rateLimited, http.StatusTooManyRequests, `{"error":{"code":"rate_limit_exceeded"}}`)
	if decision == nil || decision.Action != decisionActionCooldown || decision.CooldownStatus != cooldownStatusRateLimited {
		t.Fatalf("rate_limited decision = %+v", decision)
	}
	// 2 小时 duration + 被动确定性抖动（窗口 ±30 分钟）；固定时钟下稳定。
	if decision.CooldownUntil == "2026-09-01T12:00:00.000Z" {
		t.Fatalf("deterministic jitter must move the boundary off the exact timestamp: %q", decision.CooldownUntil)
	}
	until, err := time.Parse(time.RFC3339, decision.CooldownUntil)
	if err != nil {
		t.Fatalf("parse cooldownUntil: %v", err)
	}
	if delta := until.Sub(fixedErrorPolicyClock().Add(2 * time.Hour)); delta > 30*time.Minute || delta < -30*time.Minute || delta == 0 {
		t.Fatalf("cooldownUntil delta = %v want within ±30m and non-zero", delta)
	}

	// 关键字维度：状态码不限时按消息文本命中。
	keyword := errorPolicyAccount(map[string]any{
		"error_handling_rules": []any{map[string]any{
			"enabled": true, "name": "过载关键字", "priority": float64(1), "action": "retry_next",
			"keywords": []any{"系统过载"},
		}},
	})
	if decision := errorPolicyDecide(t, service, keyword, http.StatusInternalServerError, `{"error":{"message":"上游系统过载，请稍后重试"}}`); decision == nil {
		t.Fatal("keyword rule must match the message text")
	}
	// 关键字不匹配 → 无决策（opaque）。
	if decision := errorPolicyDecide(t, service, keyword, http.StatusInternalServerError, `{"error":{"message":"boom"}}`); decision != nil {
		t.Fatalf("non-matching keyword rule must not fire: %+v", decision)
	}

	// 无规则默认：无决策。
	if decision := errorPolicyDecide(t, service, errorPolicyAccount(nil), http.StatusInternalServerError, `{"error":{"message":"boom"}}`); decision != nil {
		t.Fatalf("no rules must produce no decision: %+v", decision)
	}
}

// TestChainErrorPolicyKeyScopedPoolIsolation：池隔离开启时系统额度决策带
// keyScoped 事实（单 Key 账户恒 false）。
func TestChainErrorPolicyKeyScopedPoolIsolation(t *testing.T) {
	fingerprint := "fp_1"
	poolAccount := errorPolicyAccount(nil)
	poolAccount.SelectedAPIKeyFingerprint = &fingerprint
	service := newFixedErrorPolicyService(func(account gatewaydispatch.AccountCandidate) bool {
		return account.SelectedAPIKeyFingerprint != nil
	})
	decision := errorPolicyDecide(t, service, poolAccount, http.StatusPaymentRequired, `{"error":{"message":"insufficient quota"}}`)
	if decision == nil || !decision.KeyScoped {
		t.Fatalf("pooled account decision = %+v want keyScoped", decision)
	}
	single := errorPolicyDecide(t, service, errorPolicyAccount(nil), http.StatusPaymentRequired, `{"error":{"message":"insufficient quota"}}`)
	if single == nil || single.KeyScoped {
		t.Fatalf("single-key account decision = %+v want not keyScoped", single)
	}
}

// ---------------------------------------------------------------------------
// 状态写侧桥：SQL 副作用
// ---------------------------------------------------------------------------

// errorPolicyEffectsFixture 提供桥测试的最小业务表 + 固定时钟桥。
type errorPolicyEffectsFixture struct {
	db        *sql.DB
	bridge    *chainErrorPolicyEffectsBridge
	keyStates *accountkeystates.Store
	now       time.Time
}

func newErrorPolicyEffectsFixture(t *testing.T) *errorPolicyEffectsFixture {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "policy.sqlite3"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	statements := []string{
		`CREATE TABLE accounts (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL DEFAULT 'sys_owner',
			provider_code TEXT NOT NULL DEFAULT 'openai', protocol_code TEXT, protocol_version TEXT,
			name TEXT NOT NULL DEFAULT '账户', type TEXT NOT NULL DEFAULT 'api_key',
			status TEXT NOT NULL DEFAULT 'active', schedulable INTEGER NOT NULL DEFAULT 1,
			config_revision INTEGER NOT NULL DEFAULT 1,
			dispatch_revision INTEGER NOT NULL DEFAULT 1, last_health_success_at TEXT,
			credentials_encrypted TEXT, cooldown_until TEXT,
			last_error_code TEXT, last_error_message TEXT, last_error_trace_id TEXT,
			cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0,
			cooldown_retest_observation_started_at TEXT, cooldown_retest_generation TEXT,
			cooldown_retest_last_at TEXT, cooldown_retest_last_status_code INTEGER,
			stream_failure_count INTEGER NOT NULL DEFAULT 0, stream_failure_window_started_at TEXT,
			account_expires_at TEXT, deleted_at TEXT, updated_at TEXT NOT NULL DEFAULT '',
			authorization_instance_source_account_id TEXT,
			authorization_instance_authorization_id TEXT)`,
		`CREATE TABLE group_accounts (
			group_id TEXT NOT NULL, system_account_id TEXT NOT NULL, account_id TEXT NOT NULL,
			enabled INTEGER NOT NULL, account_authorization_id TEXT)`,
		`CREATE TABLE group_account_stats_dirty (
			group_id TEXT PRIMARY KEY, reason TEXT, updated_at TEXT)`,
		`CREATE TABLE account_api_key_runtime_states (
			id TEXT PRIMARY KEY, system_account_id TEXT, account_id TEXT NOT NULL,
			key_fingerprint TEXT NOT NULL, key_index INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'active', failure_count INTEGER NOT NULL DEFAULT 0,
			consecutive_failures INTEGER NOT NULL DEFAULT 0, success_count INTEGER NOT NULL DEFAULT 0,
			cooldown_until TEXT, next_probe_at TEXT, probe_backoff_seconds INTEGER NOT NULL DEFAULT 0,
			recovery_started_at TEXT, last_attempt_at TEXT, last_failure_at TEXT,
			last_error_code TEXT, last_error_message TEXT, last_trace_id TEXT,
			probe_claim_token TEXT, probe_claimed_until TEXT,
			created_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '',
			UNIQUE(account_id, key_fingerprint))`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed schema: %v: %v", statement, err)
		}
	}
	now := fixedErrorPolicyClock()
	keyStates, err := accountkeystates.NewStore(accountkeystates.Config{
		DB: db, Postgres: false, Secret: "chain-error-policy-secret",
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create key states store: %v", err)
	}
	bridge := &chainErrorPolicyEffectsBridge{
		db: db, pg: false, bus: nil, keyStates: keyStates,
		now: func() time.Time { return now },
	}
	return &errorPolicyEffectsFixture{db: db, bridge: bridge, keyStates: keyStates, now: now}
}

func (f *errorPolicyEffectsFixture) seedAccount(t *testing.T, id, status string, configRevision int64) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO accounts (id, status, config_revision, stream_failure_count, stream_failure_window_started_at) VALUES (?, ?, ?, 3, '2026-08-31T00:00:00.000Z')`, id, status, configRevision); err != nil {
		t.Fatalf("seed account: %v", err)
	}
}

func (f *errorPolicyEffectsFixture) accountRow(t *testing.T, id string) map[string]any {
	t.Helper()
	row := f.db.QueryRow(`SELECT status, schedulable, cooldown_until, last_error_code, last_error_message, stream_failure_count, config_revision FROM accounts WHERE id = ?`, id)
	var status string
	var schedulable int
	var cooldownUntil, errorCode, errorMessage sql.NullString
	var streamFailures int
	var configRevision int64
	if err := row.Scan(&status, &schedulable, &cooldownUntil, &errorCode, &errorMessage, &streamFailures, &configRevision); err != nil {
		t.Fatalf("read account row: %v", err)
	}
	return map[string]any{
		"status": status, "schedulable": schedulable, "cooldown_until": cooldownUntil.String,
		"last_error_code": errorCode.String, "last_error_message": errorMessage.String,
		"stream_failure_count": streamFailures, "config_revision": configRevision,
	}
}

// systemQuotaDecisionOf 构造系统额度冷却决策。
func systemQuotaDecisionOf(mode string) accountErrorPolicyDecision {
	return accountErrorPolicyDecision{
		Action: decisionActionCooldown, RuleID: systemInsufficientQuotaRuleID,
		RuleName: systemQuotaRuleName, RuleSource: "system",
		CooldownStatus:    cooldownStatusRateLimited,
		CooldownUntil:     "2026-09-01T11:00:00.000Z",
		QuotaRecoveryMode: mode,
	}
}

// TestChainErrorPolicyEffectsCooldownWrites：cooldown 写侧副作用 —— 状态、
// schedulable、冷却时间、provenance 错误码、流失败计数复位、归因文案。
func TestChainErrorPolicyEffectsCooldownWrites(t *testing.T) {
	fixture := newErrorPolicyEffectsFixture(t)
	fixture.seedAccount(t, "acc_1", "active", 2)

	changed, status, err := fixture.bridge.ApplyAccountErrorPolicyDecision(context.Background(),
		errorPolicyAccount(nil), systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{
			HasStatusCode: true, StatusCode: http.StatusPaymentRequired,
			BodyText: `{"error":{"code":"insufficient_quota"}}`,
		})
	if err != nil {
		t.Fatalf("apply cooldown: %v", err)
	}
	if !changed || status != cooldownStatusRateLimited {
		t.Fatalf("changed=%v status=%s", changed, status)
	}
	row := fixture.accountRow(t, "acc_1")
	if row["status"] != "rate_limited" || row["schedulable"] != 1 {
		t.Fatalf("account row = %+v", row)
	}
	if row["cooldown_until"] != "2026-09-01T11:00:00.000Z" {
		t.Fatalf("cooldown_until = %v", row["cooldown_until"])
	}
	if row["last_error_code"] != systemQuotaGenericCooldownCode {
		t.Fatalf("last_error_code = %v", row["last_error_code"])
	}
	if row["stream_failure_count"] != 0 {
		t.Fatalf("stream failure counter must reset: %+v", row)
	}
	if !strings.Contains(fmt.Sprint(row["last_error_message"]), "系统继承错误策略") {
		t.Fatalf("reason must carry the system policy label: %+v", row)
	}

	// 账户规则决策写 explicit provenance 码（候选 ID 对准目标行）。
	fixture.seedAccount(t, "acc_2", "active", 1)
	accountDecision := accountErrorPolicyDecision{
		Action: decisionActionCooldown, RuleName: "五分钟限流", RuleSource: "account",
		CooldownStatus: cooldownStatusRateLimited, CooldownUntil: "2026-09-01T12:00:00.000Z",
	}
	secondCandidate := errorPolicyAccount(nil)
	secondCandidate.ID = "acc_2"
	if _, _, err := fixture.bridge.ApplyAccountErrorPolicyDecision(context.Background(),
		secondCandidate, accountDecision, chainErrorPolicyFailureInput{
			HasStatusCode: true, StatusCode: http.StatusTooManyRequests,
			BodyText: `{"error":{"code":"rate_limit_exceeded"}}`,
		}); err != nil {
		t.Fatalf("apply account cooldown: %v", err)
	}
	row = fixture.accountRow(t, "acc_2")
	if row["last_error_code"] != explicitAccountErrorPolicyCooldownCode || row["cooldown_until"] != "2026-09-01T12:00:00.000Z" {
		t.Fatalf("account-rule cooldown row = %+v", row)
	}

	// temporary_unavailable 决策：初始退避 3 秒（Node temporaryUnavailableRuntimeState）。
	fixture.seedAccount(t, "acc_3", "active", 1)
	tempDecision := accountErrorPolicyDecision{
		Action: decisionActionCooldown, RuleName: "临时不可用", RuleSource: "account",
		CooldownStatus: cooldownStatusTemporaryUnavailable,
	}
	thirdCandidate := errorPolicyAccount(nil)
	thirdCandidate.ID = "acc_3"
	if _, _, err := fixture.bridge.ApplyAccountErrorPolicyDecision(context.Background(),
		thirdCandidate, tempDecision, chainErrorPolicyFailureInput{}); err != nil {
		t.Fatalf("apply temp cooldown: %v", err)
	}
	row = fixture.accountRow(t, "acc_3")
	if row["status"] != "temporary_unavailable" || row["cooldown_until"] != "2026-09-01T10:00:03.000Z" {
		t.Fatalf("temp cooldown row = %+v", row)
	}
}

// TestChainErrorPolicyEffectsCooldownGuards：硬不可用账户不写；config_revision
// 竞争不写；系统 quota 通用码不得覆盖更高优先级的显式重置冷却。
func TestChainErrorPolicyEffectsCooldownGuards(t *testing.T) {
	fixture := newErrorPolicyEffectsFixture(t)
	fixture.seedAccount(t, "acc_disabled", "disabled", 1)
	fixture.seedAccount(t, "acc_race", "active", 2)
	fixture.seedAccount(t, "acc_explicit", "rate_limited", 1)
	if _, err := fixture.db.Exec(`UPDATE accounts SET last_error_code = ?, cooldown_until = '2026-09-01T11:30:00.000Z' WHERE id = 'acc_explicit'`,
		systemQuotaExplicitResetCooldownCode); err != nil {
		t.Fatalf("seed explicit cooldown: %v", err)
	}

	changed, _, err := fixture.bridge.ApplyAccountErrorPolicyDecision(context.Background(),
		errorPolicyAccount(nil), systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{})
	if err != nil {
		t.Fatalf("apply disabled cooldown: %v", err)
	}
	if changed {
		t.Fatal("hard-unavailable account must not be cooled down")
	}
	if row := fixture.accountRow(t, "acc_disabled"); row["status"] != "disabled" {
		t.Fatalf("disabled row changed: %+v", row)
	}

	// config_revision 竞争：桥读取 current 前手动抬高版本。
	if _, err := fixture.db.Exec(`UPDATE accounts SET config_revision = 3 WHERE id = 'acc_race'`); err != nil {
		t.Fatalf("bump revision: %v", err)
	}
	changed, _, err = fixture.bridge.ApplyAccountErrorPolicyDecision(context.Background(),
		errorPolicyAccount(nil), systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{})
	if err != nil {
		t.Fatalf("apply raced cooldown: %v", err)
	}
	if changed {
		t.Fatal("stale config_revision write must be fenced off")
	}

	// generic 不覆盖 explicit（系统配额优先级围栏）。
	changed, _, err = fixture.bridge.ApplyAccountErrorPolicyDecision(context.Background(),
		errorPolicyAccount(nil), systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{})
	if err != nil {
		t.Fatalf("apply fenced cooldown: %v", err)
	}
	if changed {
		t.Fatal("generic system quota must not override an explicit reset cooldown")
	}
	if row := fixture.accountRow(t, "acc_explicit"); row["cooldown_until"] != "2026-09-01T11:30:00.000Z" {
		t.Fatalf("explicit cooldown must stay: %+v", row)
	}
}

// TestChainErrorPolicyEffectsDisableWrites：disable 写 status='error' +
// schedulable=0 + upstream_failure 码；error 账户短路。
func TestChainErrorPolicyEffectsDisableWrites(t *testing.T) {
	fixture := newErrorPolicyEffectsFixture(t)
	fixture.seedAccount(t, "acc_1", "active", 1)
	fixture.seedAccount(t, "acc_error", "error", 1)

	disable := accountErrorPolicyDecision{Action: decisionActionDisable, RuleName: "上游崩溃禁用", RuleSource: "account"}
	changed, status, err := fixture.bridge.ApplyAccountErrorPolicyDecision(context.Background(),
		errorPolicyAccount(nil), disable, chainErrorPolicyFailureInput{
			HasStatusCode: true, StatusCode: http.StatusInternalServerError,
			BodyText: `{"error":{"message":"upstream exploded"}}`,
		})
	if err != nil {
		t.Fatalf("apply disable: %v", err)
	}
	if !changed || status != "error" {
		t.Fatalf("changed=%v status=%s", changed, status)
	}
	row := fixture.accountRow(t, "acc_1")
	if row["status"] != "error" || row["schedulable"] != 0 || row["cooldown_until"] != "" ||
		row["last_error_code"] != "upstream_failure" {
		t.Fatalf("disabled row = %+v", row)
	}

	changed, _, err = fixture.bridge.ApplyAccountErrorPolicyDecision(context.Background(),
		errorPolicyAccount(nil), disable, chainErrorPolicyFailureInput{})
	if err != nil {
		t.Fatalf("apply error-account disable: %v", err)
	}
	if changed {
		t.Fatal("error account must short-circuit the disable write")
	}
}

// TestChainErrorPolicyEffectsKeyScopedQuotaRecord：keyScoped 系统 quota 决策
// 写 Key 级运行态（rate_limited + quota 恢复码 + 决策冷却时间）。
func TestChainErrorPolicyEffectsKeyScopedQuotaRecord(t *testing.T) {
	fixture := newErrorPolicyEffectsFixture(t)
	if _, err := fixture.db.Exec(`INSERT INTO account_api_key_runtime_states
		(id, system_account_id, account_id, key_fingerprint, status, created_at, updated_at)
		VALUES ('state_1', 'sys_owner', 'acc_1', ?, 'active', '2026-09-01T09:00:00.000Z', '2026-09-01T09:00:00.000Z')`,
		fixture.keyStates.FingerprintAPIKey("key-b")); err != nil {
		t.Fatalf("seed key state: %v", err)
	}

	fingerprint := fixture.keyStates.FingerprintAPIKey("key-b")
	account := errorPolicyAccount(map[string]any{"api_keys": []any{"key-a", "key-b"}})
	account.SystemAccountID = "sys_owner"
	account.SelectedAPIKeyFingerprint = &fingerprint

	if err := fixture.bridge.RecordKeyScopedQuotaFailure(context.Background(), account,
		systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{
			HasStatusCode:        true,
			StatusCode:           http.StatusPaymentRequired,
			UpstreamErrorSummary: "insufficient quota",
		}); err != nil {
		t.Fatalf("record key scoped failure: %v", err)
	}
	var status, errorCode, cooldownUntil string
	if err := fixture.db.QueryRow(`SELECT status, last_error_code, COALESCE(cooldown_until, '') FROM account_api_key_runtime_states WHERE account_id = 'acc_1' AND key_fingerprint = ?`,
		fingerprint).Scan(&status, &errorCode, &cooldownUntil); err != nil {
		t.Fatalf("read key state: %v", err)
	}
	if status != "rate_limited" || errorCode != "api_key_quota_insufficient" {
		t.Fatalf("key state = %s/%s", status, errorCode)
	}
	if cooldownUntil != "2026-09-01T11:00:00.000Z" {
		t.Fatalf("key cooldown_until = %q", cooldownUntil)
	}
}

// TestChainErrorPolicyEffectsRuntimeFailureObservationFence：决策时刻陈旧观察
// 围栏（Node account-runtime-mutation.repository.ts:2296-2325，
// account-error-policy.service.ts:607-615）—— SQL 追加
// dispatch_revision = 快照 AND last_health_success_at < observedAt AND
// updated_at <= observedAt，updated_at 以 CASE 保留较新值；候选快照缺
// dispatchRevision 时不设围栏（Node guard undefined → 普通赋值）。
func TestChainErrorPolicyEffectsRuntimeFailureObservationFence(t *testing.T) {
	fixture := newErrorPolicyEffectsFixture(t)
	observedAt := fixedErrorPolicyClock().UTC().Format(rfc3339MillisUTC)
	candidateWithRevision := func(id string, revision int64) gatewaydispatch.AccountCandidate {
		account := errorPolicyAccount(nil)
		account.ID = id
		account.DispatchRevision = &revision
		return account
	}
	assertActive := func(t *testing.T, id string) {
		t.Helper()
		if row := fixture.accountRow(t, id); row["status"] != "active" {
			t.Fatalf("%s must stay active, row = %+v", id, row)
		}
	}

	// 1) 围栏放行：行 revision 与快照一致、无健康成功、updated_at 较旧 →
	//    冷却写入且 updated_at 归位到观察时刻。
	fixture.seedAccount(t, "acc_ok", "active", 1)
	if _, err := fixture.db.Exec(`UPDATE accounts SET updated_at = '2026-08-31T00:00:00.000Z' WHERE id = 'acc_ok'`); err != nil {
		t.Fatalf("seed updated_at: %v", err)
	}
	changed, _, err := fixture.bridge.ApplyAccountErrorPolicyDecision(context.Background(),
		candidateWithRevision("acc_ok", 1), systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{})
	if err != nil || !changed {
		t.Fatalf("fenced cooldown must apply: changed=%v err=%v", changed, err)
	}
	row := fixture.accountRow(t, "acc_ok")
	if row["status"] != "rate_limited" {
		t.Fatalf("cooled row = %+v", row)
	}
	var updatedAt string
	if err := fixture.db.QueryRow(`SELECT updated_at FROM accounts WHERE id = 'acc_ok'`).Scan(&updatedAt); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if updatedAt != observedAt {
		t.Fatalf("updated_at = %q want the observation instant %q", updatedAt, observedAt)
	}

	// 2) 决策后账户被重新派发（dispatch_revision 前进）→ 拒绝写入。
	fixture.seedAccount(t, "acc_redis", "active", 1)
	if _, err := fixture.db.Exec(`UPDATE accounts SET dispatch_revision = 2 WHERE id = 'acc_redis'`); err != nil {
		t.Fatalf("seed dispatch revision: %v", err)
	}
	changed, _, err = fixture.bridge.ApplyAccountErrorPolicyDecision(context.Background(),
		candidateWithRevision("acc_redis", 1), systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{})
	if err != nil || changed {
		t.Fatalf("stale dispatch_revision write must be fenced: changed=%v err=%v", changed, err)
	}
	assertActive(t, "acc_redis")

	// 3) 决策之后有健康成功（last_health_success_at == observedAt，严格 <）
	//    → 拒绝写入。
	fixture.seedAccount(t, "acc_health", "active", 1)
	if _, err := fixture.db.Exec(`UPDATE accounts SET last_health_success_at = ? WHERE id = 'acc_health'`, observedAt); err != nil {
		t.Fatalf("seed health success: %v", err)
	}
	changed, _, err = fixture.bridge.ApplyAccountErrorPolicyDecision(context.Background(),
		candidateWithRevision("acc_health", 1), systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{})
	if err != nil || changed {
		t.Fatalf("health success at observedAt must fence the failure write: changed=%v err=%v", changed, err)
	}
	assertActive(t, "acc_health")

	// 4) 决策之后其他写者已更新行（updated_at > observedAt）→ 拒绝写入。
	fixture.seedAccount(t, "acc_writer", "active", 1)
	if _, err := fixture.db.Exec(`UPDATE accounts SET updated_at = ? WHERE id = 'acc_writer'`,
		fixedErrorPolicyClock().Add(time.Millisecond).UTC().Format(rfc3339MillisUTC)); err != nil {
		t.Fatalf("seed newer updated_at: %v", err)
	}
	changed, _, err = fixture.bridge.ApplyAccountErrorPolicyDecision(context.Background(),
		candidateWithRevision("acc_writer", 1), systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{})
	if err != nil || changed {
		t.Fatalf("newer updated_at must fence the failure write: changed=%v err=%v", changed, err)
	}
	assertActive(t, "acc_writer")

	// 5) disable 写线同围栏：健康成功不晚于决策时不短路，晚于决策时拒绝。
	fixture.seedAccount(t, "acc_dis_ok", "active", 1)
	disable := accountErrorPolicyDecision{Action: decisionActionDisable, RuleName: "上游崩溃禁用", RuleSource: "account"}
	fifth := candidateWithRevision("acc_dis_ok", 1)
	if changed, _, err := fixture.bridge.ApplyAccountErrorPolicyDecision(context.Background(),
		fifth, disable, chainErrorPolicyFailureInput{}); err != nil || !changed {
		t.Fatalf("fenced disable must apply without health success: changed=%v err=%v", changed, err)
	}
	fixture.seedAccount(t, "acc_dis_fenced", "active", 1)
	if _, err := fixture.db.Exec(`UPDATE accounts SET last_health_success_at = ? WHERE id = 'acc_dis_fenced'`, observedAt); err != nil {
		t.Fatalf("seed fenced health success: %v", err)
	}
	fenced := candidateWithRevision("acc_dis_fenced", 1)
	if changed, _, err := fixture.bridge.ApplyAccountErrorPolicyDecision(context.Background(),
		fenced, disable, chainErrorPolicyFailureInput{}); err != nil || changed {
		t.Fatalf("health success must fence the disable write: changed=%v err=%v", changed, err)
	}
	assertActive(t, "acc_dis_fenced")

	// 6) 候选无 dispatchRevision 快照（Node guard undefined）→ 不设围栏，
	//    健康成功列不拦截，updated_at 普通赋值。
	fixture.seedAccount(t, "acc_noguard", "active", 1)
	if _, err := fixture.db.Exec(`UPDATE accounts SET last_health_success_at = ? WHERE id = 'acc_noguard'`, observedAt); err != nil {
		t.Fatalf("seed health success: %v", err)
	}
	noGuard := errorPolicyAccount(nil)
	noGuard.ID = "acc_noguard"
	if changed, _, err := fixture.bridge.ApplyAccountErrorPolicyDecision(context.Background(),
		noGuard, systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{}); err != nil || !changed {
		t.Fatalf("unguarded cooldown must apply: changed=%v err=%v", changed, err)
	}

	// 7) 过期分支同样带围栏：健康成功晚于决策时不自动停用。
	fixture.seedAccount(t, "acc_exp", "active", 1)
	if _, err := fixture.db.Exec(`UPDATE accounts SET account_expires_at = '2026-08-01T00:00:00.000Z', last_health_success_at = ? WHERE id = 'acc_exp'`, observedAt); err != nil {
		t.Fatalf("seed expired account: %v", err)
	}
	expired := candidateWithRevision("acc_exp", 1)
	if changed, _, err := fixture.bridge.ApplyAccountErrorPolicyDecision(context.Background(),
		expired, systemQuotaDecisionOf("generic"), chainErrorPolicyFailureInput{}); err != nil || changed {
		t.Fatalf("expired-branch write must honor the fence: changed=%v err=%v", changed, err)
	}
	assertActive(t, "acc_exp")
}

// failingKeyScopedEffects 注入 Key 级失败写入错误（#2 catch-warn 契约验证）。
type failingKeyScopedEffects struct {
	recordErr error
	recorded  int
}

func (f *failingKeyScopedEffects) ApplyAccountErrorPolicyDecision(context.Context, gatewaydispatch.AccountCandidate, accountErrorPolicyDecision, chainErrorPolicyFailureInput) (bool, string, error) {
	return false, "", nil
}

func (f *failingKeyScopedEffects) RecordKeyScopedQuotaFailure(context.Context, gatewaydispatch.AccountCandidate, accountErrorPolicyDecision, chainErrorPolicyFailureInput) error {
	f.recorded++
	return f.recordErr
}

// TestChainFailureDispatcherKeyScopedWriteFailureContinues：Key 级失败写入
// 失败按 Node account-api-key-effects.service.ts:141-171 的 catch-warn 语义
// 处理 —— 不中止当前尝试，请求继续 skip_account 候选故障转移。
func TestChainFailureDispatcherKeyScopedWriteFailureContinues(t *testing.T) {
	response := failureDispatchUpstreamResponse(t, http.StatusPaymentRequired, "application/json",
		`{"error":{"code":"insufficient_quota","message":"insufficient quota"}}`)
	fingerprint := "fp_keyscoped"
	account := errorPolicyAccount(nil)
	account.APIKeys = []string{"key-a", "key-b"}
	account.SelectedAPIKeyFingerprint = &fingerprint

	effects := &failingKeyScopedEffects{recordErr: errors.New("key-state write down")}
	dispatcher := &chainFailureDispatcher{
		policy: newFixedErrorPolicyService(func(candidate gatewaydispatch.AccountCandidate) bool {
			return candidate.SelectedAPIKeyFingerprint != nil
		}),
		effects: effects,
	}
	input := gatewayFailedResponseInput(response, &failureDispatchAuditSink{}, "gateway")
	input.Account = account
	input.AccountStateMutationEnabled = true
	result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input)
	if err != nil {
		t.Fatalf("key-scoped write failure must not abort the attempt: %v", err)
	}
	if effects.recorded != 1 {
		t.Fatalf("key-scoped record attempts = %d want 1", effects.recorded)
	}
	if result.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("action=%s want skip_account", result.Action)
	}
	if result.FailureKind != gatewaydispatch.FailureKindExplicitPolicy {
		t.Fatalf("failureKind=%s want explicit_policy", result.FailureKind)
	}
	if !result.KeyScopedFailure {
		t.Fatal("keyScoped decision must still authorize the same-account key rotation")
	}
}

// recordingErrorPolicyEffects 记录账户级状态写与 Key 级失败记录两条写入口的
// 调用次数（keyScoped 账户级守卫的回归验证）。
type recordingErrorPolicyEffects struct {
	applyCalls  int
	recordCalls int
}

func (r *recordingErrorPolicyEffects) ApplyAccountErrorPolicyDecision(context.Context, gatewaydispatch.AccountCandidate, accountErrorPolicyDecision, chainErrorPolicyFailureInput) (bool, string, error) {
	r.applyCalls++
	return true, cooldownStatusRateLimited, nil
}

func (r *recordingErrorPolicyEffects) RecordKeyScopedQuotaFailure(context.Context, gatewaydispatch.AccountCandidate, accountErrorPolicyDecision, chainErrorPolicyFailureInput) error {
	r.recordCalls++
	return nil
}

// TestChainFailureDispatcherKeyScopedSkipsAccountStateWrite：池隔离开启的
// keyScoped 系统 quota 决策只做 Key 级失败记录 —— 归档
// account-error-policy.service.ts:261-268 对 keyScoped 提前返回
// （changed:false，不进入 applyExplicitAccountErrorPolicyDecision），账户级
// cooldown/disable 状态写不得执行；audit 归因（failure-dispatch.ts:369-383
// 对 keyScoped 同样写入）、Key 级记录与 explicit_policy failureKind 保持。
// 对照面：非 keyScoped 的系统 quota 决策仍走账户级状态写。
func TestChainFailureDispatcherKeyScopedSkipsAccountStateWrite(t *testing.T) {
	response := failureDispatchUpstreamResponse(t, http.StatusPaymentRequired, "application/json",
		`{"error":{"code":"insufficient_quota","message":"insufficient quota"}}`)
	newDispatcher := func(poolOn bool) (*chainFailureDispatcher, *recordingErrorPolicyEffects) {
		effects := &recordingErrorPolicyEffects{}
		return &chainFailureDispatcher{
			policy: newFixedErrorPolicyService(func(candidate gatewaydispatch.AccountCandidate) bool {
				return poolOn && candidate.SelectedAPIKeyFingerprint != nil
			}),
			effects: effects,
		}, effects
	}

	// 池隔离开启 → keyScoped 决策：Key 级记录在，账户级写不落。
	fingerprint := "fp_keyscoped_guard"
	pooledAccount := errorPolicyAccount(nil)
	pooledAccount.APIKeys = []string{"key-a", "key-b"}
	pooledAccount.SelectedAPIKeyFingerprint = &fingerprint
	dispatcher, effects := newDispatcher(true)
	sink := &failureDispatchAuditSink{}
	input := gatewayFailedResponseInput(response, sink, "gateway")
	input.AuditCapture = gatewaydispatch.AuditCapture{Context: sink, Sink: sink}
	input.Account = pooledAccount
	input.AccountStateMutationEnabled = true
	input.Settings = gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 30}
	result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input)
	if err != nil {
		t.Fatalf("handle keyScoped failure: %v", err)
	}
	if effects.applyCalls != 0 {
		t.Fatalf("keyScoped decision must not write account-level state: ApplyAccountErrorPolicyDecision calls = %d", effects.applyCalls)
	}
	if effects.recordCalls != 1 {
		t.Fatalf("key-scoped record calls = %d want 1", effects.recordCalls)
	}
	if result.FailureKind != gatewaydispatch.FailureKindExplicitPolicy {
		t.Fatalf("failureKind=%s want explicit_policy", result.FailureKind)
	}
	if !result.KeyScopedFailure {
		t.Fatal("keyScoped decision must still authorize the same-account key rotation")
	}
	if result.PendingApiKeyFailure != nil {
		t.Fatal("keyScoped failure is recorded directly; no pending key failure may be captured")
	}
	policyMetadata := sink.metadataByLabel("account_error_policy_matched")
	if policyMetadata == nil {
		t.Fatalf("account_error_policy_matched metadata missing: %+v", sink.metadata)
	}
	if keyScoped, ok := policyMetadata.metadata["keyScoped"].(bool); !ok || !keyScoped {
		t.Fatalf("policy metadata keyScoped = %+v want true", policyMetadata.metadata["keyScoped"])
	}

	// 池隔离关闭 → 同一上游失败产生非 keyScoped 决策：账户级状态写保持。
	singleDispatcher, singleEffects := newDispatcher(false)
	singleInput := gatewayFailedResponseInput(response, &failureDispatchAuditSink{}, "gateway")
	singleInput.Account = errorPolicyAccount(nil)
	singleInput.AccountStateMutationEnabled = true
	singleInput.Settings = gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 30}
	if _, err := singleDispatcher.HandleFailedUpstreamResponse(context.Background(), singleInput); err != nil {
		t.Fatalf("handle non-keyScoped failure: %v", err)
	}
	if singleEffects.applyCalls != 1 {
		t.Fatalf("non-keyScoped decision must still apply the account-level state: calls = %d", singleEffects.applyCalls)
	}
	if singleEffects.recordCalls != 0 {
		t.Fatalf("non-keyScoped decision must not record the key-scoped failure: calls = %d", singleEffects.recordCalls)
	}
}

// TestChainErrorPolicyNonJSONBodyKeepsEmptyPayload：非 JSON 失败体的
// errorPayload 恒空（Node failure-dispatch.ts:439-447）—— 决策与 usage 都
// 不再从捕获文本重解析结构化 code/type；error_codes 维度不命中，keywords /
// status_codes 维度仍按 bodyText 与状态码生效。
func TestChainErrorPolicyNonJSONBodyKeepsEmptyPayload(t *testing.T) {
	service := newFixedErrorPolicyService(nil)
	const body = `upstream said: {"code":"quota_exceeded_custom"}`

	codeRule := errorPolicyAccount(map[string]any{
		"error_handling_rules": []any{map[string]any{
			"enabled": true, "name": "额度码", "priority": float64(1), "action": "retry_next",
			"error_codes": []any{"quota_exceeded_custom"},
		}},
	})
	if decision := errorPolicyDecide(t, service, codeRule, http.StatusTooManyRequests, body); decision != nil {
		t.Fatalf("non-JSON body must not feed error_codes matching: %+v", decision)
	}

	keywordRule := errorPolicyAccount(map[string]any{
		"error_handling_rules": []any{map[string]any{
			"enabled": true, "name": "文本关键字", "priority": float64(1), "action": "retry_next",
			"keywords": []any{"quota_exceeded_custom"},
		}},
	})
	if decision := errorPolicyDecide(t, service, keywordRule, http.StatusTooManyRequests, body); decision == nil {
		t.Fatal("keyword dimension must still match the raw body text")
	}

	statusRule := errorPolicyAccount(map[string]any{
		"error_handling_rules": []any{map[string]any{
			"enabled": true, "name": "状态码", "priority": float64(1), "action": "error_disabled",
			"status_codes": []any{float64(429)},
		}},
	})
	if decision := errorPolicyDecide(t, service, statusRule, http.StatusTooManyRequests, body); decision == nil || decision.Action != decisionActionDisable {
		t.Fatalf("status-code dimension must keep matching: %+v", decision)
	}

	if payload := failureProtocolPayloadOf(nil); payload.HasEvidence() {
		t.Fatalf("non-JSON payload must stay empty: %+v", payload)
	}
	parsed := map[string]any{"code": "rate_limit_exceeded"}
	if payload := failureProtocolPayloadOf(parsed); !payload.HasEvidence() || payload.Code != "rate_limit_exceeded" {
		t.Fatalf("json payload projection = %+v", payload)
	}
}

// ---------------------------------------------------------------------------
// 派发器决策驱动（failureKind / 换 Key 事实 / audit 归因）
// ---------------------------------------------------------------------------

// errorPolicyRuleAccount 构造带账户规则的候选账户。
func errorPolicyRuleAccount(rules ...map[string]any) gatewaydispatch.AccountCandidate {
	list := make([]any, 0, len(rules))
	for _, rule := range rules {
		list = append(list, rule)
	}
	return gatewaydispatch.AccountCandidate{
		ID: "acc_1", Name: "账户一", Type: "api_key", ProviderCode: "openai",
		Credentials: map[string]any{"error_handling_rules": list},
	}
}

// TestChainFailureDispatcherExplicitPolicyFailureKind：显式决策驱动
// failureKind=explicit_policy、审计归因 metadata；无决策保持 opaque_http。
func TestChainFailureDispatcherExplicitPolicyFailureKind(t *testing.T) {
	body := `{"error":{"code":"insufficient_quota","message":"insufficient quota"}}`
	response := failureDispatchUpstreamResponse(t, http.StatusPaymentRequired, "application/json", body)

	sink := &failureDispatchAuditSink{}
	dispatcher := &chainFailureDispatcher{policy: newFixedErrorPolicyService(nil)}
	input := gatewayFailedResponseInput(response, sink, "gateway")
	// metadata 走冻结捕获上下文；sink 同时实现两侧，双挂保证 attempt 记录。
	input.AuditCapture = gatewaydispatch.AuditCapture{Context: sink, Sink: sink}
	input.AccountStateMutationEnabled = true
	input.Settings = gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 30}
	result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input)
	if err != nil {
		t.Fatalf("handle failed response: %v", err)
	}
	if result.FailureKind != gatewaydispatch.FailureKindExplicitPolicy {
		t.Fatalf("failureKind=%s want explicit_policy", result.FailureKind)
	}
	if result.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("action=%s", result.Action)
	}
	policyMetadata := sink.metadataByLabel("account_error_policy_matched")
	if policyMetadata == nil {
		t.Fatalf("account_error_policy_matched metadata missing: %+v", sink.metadata)
	}
	if policyMetadata.metadata["ruleSource"] != "system" || policyMetadata.metadata["action"] != decisionActionCooldown {
		t.Fatalf("policy metadata = %+v", policyMetadata.metadata)
	}

	// 无规则 → opaque_http（现状契约保持）。
	opaqueResponse := failureDispatchUpstreamResponse(t, http.StatusInternalServerError, "application/json", `{"error":{"message":"boom"}}`)
	opaqueSink := &failureDispatchAuditSink{}
	opaqueInput := gatewayFailedResponseInput(opaqueResponse, opaqueSink, "gateway")
	opaqueInput.Settings = gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 30}
	opaque, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), opaqueInput)
	if err != nil {
		t.Fatalf("handle opaque failure: %v", err)
	}
	if opaque.FailureKind != chainFailureKindOpaqueHTTP {
		t.Fatalf("opaque failureKind=%s", opaque.FailureKind)
	}
	if len(opaqueSink.metadata) != 0 {
		t.Fatalf("opaque failure must not add policy metadata: %+v", opaqueSink.metadata)
	}
}

// TestChainFailureDispatcherRetryNextAuthorizesRotation：显式 retry_next
// 规则不受预提交推迟约束 —— DeferAutomatic 同账户换 Key 事实仍激活。
func TestChainFailureDispatcherRetryNextAuthorizesRotation(t *testing.T) {
	response := failureDispatchUpstreamResponse(t, http.StatusInternalServerError, "application/json", `{"error":{"message":"upstream exploded"}}`)
	fingerprint := "fp_1"
	account := errorPolicyRuleAccount(map[string]any{
		"enabled": true, "name": "换号重试", "priority": float64(1), "action": "retry_next",
		"status_codes": []any{float64(500)},
	})
	account.APIKeys = []string{"key-a", "key-b"}
	account.SelectedAPIKeyFingerprint = &fingerprint

	dispatcher := &chainFailureDispatcher{policy: newFixedErrorPolicyService(nil)}
	input := gatewayFailedResponseInput(response, &failureDispatchAuditSink{}, "gateway")
	input.Account = account
	input.AccountStateMutationEnabled = true
	input.DeferAutomaticSameAccountKeyRotation = true
	input.Settings = gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 30}
	result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input)
	if err != nil {
		t.Fatalf("handle retry_next failure: %v", err)
	}
	if result.FailureKind != gatewaydispatch.FailureKindExplicitPolicy {
		t.Fatalf("failureKind=%s want explicit_policy", result.FailureKind)
	}
	if !result.KeyScopedFailure {
		t.Fatal("explicit retry_next must authorize the same-account key rotation despite the deferral")
	}
	if result.PendingApiKeyFailure == nil {
		t.Fatal("retry_next rotation must capture the pending key failure")
	}
	if result.PendingApiKeyFailure.ErrorMessage == "" {
		t.Fatal("pending failure should carry the upstream summary for confirmation")
	}
}

// TestGatewayChain402InsufficientQuotaCooldownsAccount：端到端 —— 上游 402
// 额度不足 → 系统继承策略冷却落库（status=rate_limited + system quota
// provenance 码），客户端拿到耗尽契约。
func TestGatewayChain402InsufficientQuotaCooldownsAccount(t *testing.T) {
	fixture := newChainFixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"message":"insufficient quota","code":"insufficient_quota"}}`))
	}))
	defer upstream.Close()
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": upstream.URL}), fixture.accountID); err != nil {
		t.Fatalf("update account credentials: %v", err)
	}
	// 桥写侧需要测试 schema 未包含的运行态列（生产 schema 具备）。
	for _, alter := range []string{
		`ALTER TABLE accounts ADD COLUMN last_error_trace_id TEXT`,
		`ALTER TABLE accounts ADD COLUMN cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE accounts ADD COLUMN cooldown_retest_observation_started_at TEXT`,
		`ALTER TABLE accounts ADD COLUMN cooldown_retest_generation TEXT`,
		`ALTER TABLE accounts ADD COLUMN cooldown_retest_last_at TEXT`,
		`ALTER TABLE accounts ADD COLUMN cooldown_retest_last_status_code INTEGER`,
		`ALTER TABLE accounts ADD COLUMN updated_at TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE IF NOT EXISTS group_account_stats_dirty (group_id TEXT PRIMARY KEY, reason TEXT, updated_at TEXT)`,
	} {
		if _, err := fixture.db.Exec(alter); err != nil {
			t.Fatalf("extend fixture schema: %v: %v", alter, err)
		}
	}

	deps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool"))
	errorPolicyBridge, errorPolicyService, err := newChainErrorPolicyEffectsBridge(&composition{db: fixture.db, pgDialect: false}, "chain-test-secret")
	if err != nil {
		t.Fatalf("compose error policy bridge: %v", err)
	}
	deps.AccountErrorPolicy = errorPolicyService
	deps.AccountErrorPolicyEffects = errorPolicyBridge

	chain, shutdown, err := composeGatewayChain(deps)
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
	row := fixture.db.QueryRow(`SELECT status, schedulable, last_error_code, COALESCE(cooldown_until, '') FROM accounts WHERE id = ?`, fixture.accountID)
	var accountStatus string
	var schedulable int
	var errorCode, cooldownUntil string
	if err := row.Scan(&accountStatus, &schedulable, &errorCode, &cooldownUntil); err != nil {
		t.Fatalf("read cooled account: %v", err)
	}
	if accountStatus != "rate_limited" || schedulable != 1 {
		t.Fatalf("cooled account = %s/%d", accountStatus, schedulable)
	}
	if errorCode != systemQuotaGenericCooldownCode {
		t.Fatalf("provenance code = %s want system_quota_generic_cooldown", errorCode)
	}
	if cooldownUntil == "" {
		t.Fatal("cooldown_until must be persisted")
	}
}

// ---------------------------------------------------------------------------
// 第七轮审查修复：失败观察代际 / codex usage headers 失败面 / 结构化失败警告
// ---------------------------------------------------------------------------

// fakeChainAPIKeyObservation implements chainAPIKeyObservationPort with a
// deterministic epoch sequence.
type fakeChainAPIKeyObservation struct {
	accounts []string
	epoch    int64
}

func (f *fakeChainAPIKeyObservation) CaptureFailureObservation(account gatewayruntimecache.OpenAIAccountSecret) *int64 {
	f.accounts = append(f.accounts, account.ID)
	f.epoch++
	epoch := f.epoch
	return &epoch
}

// unavailableChainAPIKeyObservation mirrors the process-local state being
// unusable (the guard renders nil).
type unavailableChainAPIKeyObservation struct{}

func (unavailableChainAPIKeyObservation) CaptureFailureObservation(gatewayruntimecache.OpenAIAccountSecret) *int64 {
	return nil
}

// fakeChainCodexHeadersDispatcher captures the codex usage-header dispatch.
type fakeChainCodexHeadersDispatcher struct {
	mu         sync.Mutex
	accountIDs []string
	sources    []string
}

func (d *fakeChainCodexHeadersDispatcher) PersistOpenAICodexUsageHeaders(_ context.Context, accountID string, _ http.Header, source string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.accountIDs = append(d.accountIDs, accountID)
	d.sources = append(d.sources, source)
}

// failureDispatchUpstreamResponseWithHeaders performs the in-process upstream
// request with extra response headers (the codex usage headers ride here).
func failureDispatchUpstreamResponseWithHeaders(t *testing.T, status int, contentType, body string, extra http.Header) *gatewaydispatch.GatewayUpstreamResponse {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, values := range extra {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
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

// TestChainFailureDispatcherPendingFailureObservationEpoch（缺口 B，归档
// failure-dispatch.ts:421-434）：确认换 Key 的挂起失败必须携带失败时刻捕获的
// 进程内观察代际；端口缺席或代际不可用时留空（记录侧 guard 按 stale 拒绝，
// 不误占 fence）。
func TestChainFailureDispatcherPendingFailureObservationEpoch(t *testing.T) {
	fingerprint := "fp_epoch"
	account := gatewaydispatch.AccountCandidate{
		ID: "acc_epoch", Name: "账户一",
		APIKeys:                   []string{"key-a", "key-b"},
		SelectedAPIKeyFingerprint: &fingerprint,
	}
	response := failureDispatchUpstreamResponse(t, http.StatusTooManyRequests, "application/json", `{"error":{"message":"rate limited"}}`)
	sink := &failureDispatchAuditSink{}

	observation := &fakeChainAPIKeyObservation{}
	dispatcher := newFailureDispatcherForTest(nil)
	dispatcher.apiKeyObservation = observation

	input := gatewayFailedResponseInput(response, sink, "gateway")
	input.Account = account
	input.AccountStateMutationEnabled = true
	result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input)
	if err != nil {
		t.Fatalf("handle failed upstream response: %v", err)
	}
	if result.PendingApiKeyFailure == nil {
		t.Fatal("pending api key failure must be captured for confirmation")
	}
	if result.PendingApiKeyFailure.ObservationEpoch != "1" {
		t.Fatalf("observation epoch = %q want %q", result.PendingApiKeyFailure.ObservationEpoch, "1")
	}
	if len(observation.accounts) != 1 || observation.accounts[0] != "acc_epoch" {
		t.Fatalf("observation capture accounts = %v", observation.accounts)
	}

	// 端口缺席：挂起失败保留，代际留空。
	plain := newFailureDispatcherForTest(nil)
	plainResult, err := plain.HandleFailedUpstreamResponse(context.Background(), input)
	if err != nil {
		t.Fatalf("handle nil-port failure: %v", err)
	}
	if plainResult.PendingApiKeyFailure == nil || plainResult.PendingApiKeyFailure.ObservationEpoch != "" {
		t.Fatalf("nil-port pending failure wrong: %+v", plainResult.PendingApiKeyFailure)
	}

	// 代际不可用（进程本地状态不可用）：同样留空。
	unavailable := newFailureDispatcherForTest(nil)
	unavailable.apiKeyObservation = unavailableChainAPIKeyObservation{}
	unavailableResult, err := unavailable.HandleFailedUpstreamResponse(context.Background(), input)
	if err != nil {
		t.Fatalf("handle unavailable-epoch failure: %v", err)
	}
	if unavailableResult.PendingApiKeyFailure == nil || unavailableResult.PendingApiKeyFailure.ObservationEpoch != "" {
		t.Fatalf("unavailable-epoch pending failure wrong: %+v", unavailableResult.PendingApiKeyFailure)
	}
}

// TestChainFailureDispatcherCodexUsageHeadersFailureFace（缺口 C，归档
// failure-dispatch.ts:340-344）：失败面在 mutationEnabled ≠ false 时对
// OAuth codex 账户派发用量响应头持久化；非 codex 账户与状态变更关闭的
// 请求保持静默。
func TestChainFailureDispatcherCodexUsageHeadersFailureFace(t *testing.T) {
	codexHeaders := http.Header{
		"X-Codex-Primary-Used-Percent": []string{"37"},
	}
	response := failureDispatchUpstreamResponseWithHeaders(t, http.StatusTooManyRequests, "application/json",
		`{"error":{"message":"rate limited"}}`, codexHeaders)
	sink := &failureDispatchAuditSink{}
	codexDispatcher := &fakeChainCodexHeadersDispatcher{}
	dispatcher := newFailureDispatcherForTest(nil)
	dispatcher.codexUsageHeaders = codexDispatcher

	input := gatewayFailedResponseInput(response, sink, "gateway")
	input.Account = gatewaydispatch.AccountCandidate{
		ID: "acc_codex", Name: "codex 账户",
		Type: "oauth", ProtocolCode: "openai", ProtocolVersion: "v1",
	}
	input.AccountStateMutationEnabled = true
	if _, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input); err != nil {
		t.Fatalf("handle failed upstream response: %v", err)
	}
	codexDispatcher.mu.Lock()
	calls := len(codexDispatcher.accountIDs)
	sources := append([]string{}, codexDispatcher.sources...)
	codexDispatcher.mu.Unlock()
	if calls != 1 {
		t.Fatalf("codex header dispatch calls = %d want 1", calls)
	}
	if sources[0] != "gateway_error" {
		t.Fatalf("codex header source = %q want gateway_error", sources[0])
	}

	// 状态变更关闭：失败面不派发。
	codexDispatcher2 := &fakeChainCodexHeadersDispatcher{}
	dispatcher2 := newFailureDispatcherForTest(nil)
	dispatcher2.codexUsageHeaders = codexDispatcher2
	input2 := gatewayFailedResponseInput(response, sink, "gateway")
	input2.Account = input.Account
	input2.AccountStateMutationEnabled = false
	if _, err := dispatcher2.HandleFailedUpstreamResponse(context.Background(), input2); err != nil {
		t.Fatalf("handle mutation-disabled failure: %v", err)
	}
	codexDispatcher2.mu.Lock()
	disabledCalls := len(codexDispatcher2.accountIDs)
	codexDispatcher2.mu.Unlock()
	if disabledCalls != 0 {
		t.Fatalf("mutation-disabled failure must not dispatch codex headers, got %d", disabledCalls)
	}

	// 非 OAuth 账户：不派发（gatewaycodex 账户资格门）。
	codexDispatcher3 := &fakeChainCodexHeadersDispatcher{}
	dispatcher3 := newFailureDispatcherForTest(nil)
	dispatcher3.codexUsageHeaders = codexDispatcher3
	input3 := gatewayFailedResponseInput(response, sink, "gateway")
	input3.Account = gatewaydispatch.AccountCandidate{
		ID: "acc_apikey", Name: "api key 账户",
		Type: "api_key", ProtocolCode: "openai", ProtocolVersion: "v1",
	}
	input3.AccountStateMutationEnabled = true
	if _, err := dispatcher3.HandleFailedUpstreamResponse(context.Background(), input3); err != nil {
		t.Fatalf("handle api-key failure: %v", err)
	}
	codexDispatcher3.mu.Lock()
	apiKeyCalls := len(codexDispatcher3.accountIDs)
	codexDispatcher3.mu.Unlock()
	if apiKeyCalls != 0 {
		t.Fatalf("api-key account must not dispatch codex headers, got %d", apiKeyCalls)
	}
}

// captureSlogWarnings redirects the default slog logger into a buffer for the
// duration of the test.
func captureSlogWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buffer, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buffer
}

// TestChainFailureDispatcherStructuredFailureWarnings（缺口 D，归档
// failure-dispatch.ts:240-254 / :524-542）：失败响应与传输失败分支各记录一条
// 结构化 warn，字段集对照归档（phase-only 分类保留 Node 的 metric reason
// 缺省）。
func TestChainFailureDispatcherStructuredFailureWarnings(t *testing.T) {
	logs := captureSlogWarnings(t)

	// 失败响应分支。
	response := failureDispatchUpstreamResponse(t, http.StatusTooManyRequests, "application/json", `{"error":{"message":"rate limited"}}`)
	sink := &failureDispatchAuditSink{}
	dispatcher := newFailureDispatcherForTest(nil)
	input := gatewayFailedResponseInput(response, sink, "gateway")
	if _, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input); err != nil {
		t.Fatalf("handle failed upstream response: %v", err)
	}

	// 传输失败分支。
	errorSink := &failureDispatchAuditSink{}
	if _, err := dispatcher.HandleUpstreamRequestError(context.Background(),
		upstreamRequestErrorInput(errors.New("连接被重置"), errorSink, nil)); err != nil {
		t.Fatalf("handle upstream request error: %v", err)
	}

	captured := logs.String()
	for _, fragment := range []string{
		"event=gateway_upstream_response_failed",
		"accountId=acc_1",
		"statusCode=429",
		"contentType=application/json",
		"responseBodyTruncated=false",
		"failureClass=opaque_upstream_response",
		"metricReasonClass=unknown",
		"classificationReason=opaque_upstream_response_failure",
		"trafficSource=gateway",
		"event=gateway_upstream_request_failed",
		"failureClass=transport",
		"metricReasonClass=transport",
		"classificationReason=upstream_transport_failure",
		"stream=false",
		"errorMessage=连接被重置",
	} {
		if !strings.Contains(captured, fragment) {
			t.Fatalf("structured failure warnings missing %q, got:\n%s", fragment, captured)
		}
	}
}

// 负例（第八轮审查建议）：keyScoped 决策 + AccountStateMutationEnabled=false
// 双条件（Node failure-dispatch.ts:348 的 mutationEnabled!==false 门）——
// Key 级记录跳过、账户级状态写跳过、audit 归因保留。
func TestChainFailureDispatcherKeyScopedMutationDisabledSkipsBothWrites(t *testing.T) {
	fingerprint := "fp_neg"
	response := failureDispatchUpstreamResponse(t, http.StatusPaymentRequired, "application/json", `{"error":{"code":"insufficient_quota","message":"insufficient quota"}}`)
	sink := &failureDispatchAuditSink{}
	effects := &recordingErrorPolicyEffects{}
	dispatcher := &chainFailureDispatcher{
		policy: newFixedErrorPolicyService(func(candidate gatewaydispatch.AccountCandidate) bool {
			return candidate.SelectedAPIKeyFingerprint != nil
		}),
		effects: effects,
	}

	input := gatewayFailedResponseInput(response, sink, "gateway")
	input.Account = gatewaydispatch.AccountCandidate{
		ID: "acc_neg", Name: "账户负例", Type: "api_key", ProviderCode: "openai",
		APIKeys:                   []string{"key-a", "key-b"},
		SelectedAPIKeyFingerprint: &fingerprint,
	}
	input.AccountStateMutationEnabled = false
	input.Settings = gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 30}
	result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), input)
	if err != nil {
		t.Fatalf("handle keyScoped mutation-disabled failure: %v", err)
	}
	if result.FailureKind != gatewaydispatch.FailureKindExplicitPolicy {
		t.Fatalf("failureKind=%s want explicit_policy (决策仍归因)", result.FailureKind)
	}
	if effects.applyCalls != 0 {
		t.Fatalf("mutation disabled must skip account-level state: applyCalls = %d", effects.applyCalls)
	}
	if effects.recordCalls != 0 {
		t.Fatalf("mutation disabled must skip key-scoped record: recordCalls = %d", effects.recordCalls)
	}
	// 归档 :382 的 audit 归因块也在 mutation 门内：禁用时 metadata 同样省略。
	if policyMetadata := sink.metadataByLabel("account_error_policy_matched"); policyMetadata != nil {
		t.Fatalf("mutation disabled must skip audit attribution: %+v", policyMetadata.metadata)
	}
}
