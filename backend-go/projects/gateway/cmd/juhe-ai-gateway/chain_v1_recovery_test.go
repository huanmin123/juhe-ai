package main

// chain_v1 编排修复（常驻审查第二轮）的回归测试：
//
//   - V1：stream 请求遇非 2xx + text/event-stream 上游必须走 non-stream 处理
//     （routes.ts:1550 shouldHandleAsStream = upstreamResponse.ok && ...），
//     上游错误体不被替换成 gateway 事件。
//   - V2：多分组 key 首分组 dispatch 耗尽后 switchToFallbackGroup 接线
//     （routes.ts:570-662 / 1469-1478），fallback 分组成功返回。
//   - V3：dispatch 耗尽出口渲染 Node 固定文案与 audit 结果（2551-2638）。
//   - V4：wall 预算 coordination 类错误走 client handoff 出口（1346-1370）。
//   - V5：dispatch 段意外错误保持 503 上游契约（顶层 catch，非 500）。
//   - V10：request.accepted 时序在 preauth+body 之后（401 拒绝无 accepted 日志）。
//   - V7：audit capture 生命周期——Cancel 暴露、幂等、活跃计数回收。

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// chainV1ChatRequest posts a chat completion against the chain server.
func chainV1ChatRequest(t *testing.T, serverURL, apiKey, body string) (int, string) {
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
	return response.StatusCode, string(raw)
}

// shortenChainWaitBudgets keeps the engine from waiting on recoverable
// (cooldown) accounts during the failure drills.
func shortenChainWaitBudgets(t *testing.T, fixture *chainFixture) {
	t.Helper()
	for _, key := range []string{"noAvailableAccountWaitTimeoutSeconds", "temporaryUnschedulableRetryIntervalSeconds"} {
		// 10 是 noAvailableAccountWaitTimeoutSeconds 的校验下限。
		if _, err := fixture.db.Exec(`UPDATE system_settings SET value_json = '10' WHERE key = ?`, key); err != nil {
			t.Fatalf("shorten %s: %v", key, err)
		}
	}
}

// newDeadUpstreamAddr returns a listen-and-close address (connection refused).
func newDeadUpstreamAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("open dead listener: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return "http://" + address
}

// ---------------------------------------------------------------------------
// V1: the stream ok gate
// ---------------------------------------------------------------------------

// TestV1StreamGateRequiresUpstreamOK：逐分支固定 routes.ts:1550 的
// shouldHandleAsStream = upstreamResponse.ok && shouldHandle... 决策——非 2xx
// + text/event-stream 的上游响应（provider 限流/过载错误体）绝不进入流式处理。
//
// 端到端注记：当前 composition 的 chainFailureDispatcher 占位实现把所有失败
// 上游响应 SkipAccount（不 ReturnResponse），非 2xx 响应在 G16 failure-dispatch
// 切片接入前不会到达 chain 响应层；因此 ok 门以决策向量单元覆盖。
func TestV1StreamGateRequiresUpstreamOK(t *testing.T) {
	cases := []struct {
		name          string
		status        int
		contentType   string
		streamRequest bool
		want          bool
	}{
		{"2xx-sse-stream-request", http.StatusOK, "text/event-stream; charset=utf-8", true, true},
		{"2xx-json-stream-request", http.StatusOK, "application/json", true, false},
		// SSE content-type 本身决定按流处理（Node shouldHandle... 第一分支），
		// streamRequest 只影响非 SSE content-type 的推断。
		{"2xx-sse-non-stream-request", http.StatusOK, "text/event-stream", false, true},
		{"429-sse-stream-request", http.StatusTooManyRequests, "text/event-stream", true, false},
		{"503-sse-stream-request", http.StatusServiceUnavailable, "text/event-stream", true, false},
		{"500-json-stream-request", http.StatusInternalServerError, "application/json", true, false},
		{"201-sse-stream-request", http.StatusCreated, "text/event-stream", true, true},
		{"3xx-sse-stream-request", http.StatusMultipleChoices, "text/event-stream", true, false},
	}
	for _, testCase := range cases {
		if got := shouldHandleOpenAIUpstreamResponseAsStreamWithStatus(testCase.status, testCase.contentType, testCase.streamRequest); got != testCase.want {
			t.Fatalf("%s: got=%v want=%v", testCase.name, got, testCase.want)
		}
	}
}

// ---------------------------------------------------------------------------
// V2: the api-key group fallback wiring
// ---------------------------------------------------------------------------

// TestGatewayChainGroupFallbackAfterFirstGroupExhausted：绑定两个分组的 key
// 首分组 dispatch 耗尽（连接拒绝）→ switchToFallbackGroup 切到 fallback 分组
// → 成功返回 fallback 账户内容。
func TestGatewayChainGroupFallbackAfterFirstGroupExhausted(t *testing.T) {
	fixture := newChainFixture(t)
	shortenChainWaitBudgets(t, fixture)
	now := "2026-09-04T00:00:00.000Z"
	dead := newDeadUpstreamAddr(t)

	fallbackHits := 0
	fallbackUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-fallback","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"fallback 分组内容"},"finish_reason":"stop"}]}`))
	}))
	defer fallbackUpstream.Close()

	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v: %v", query, err)
		}
	}
	// fallback 分组 + 账户 + 绑定 + 模型 + 策略第二跳。
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES ('group_fb', ?, 'openai', 1, 'personal')`, fixture.systemAccount)
	fallbackCredentials := mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": fallbackUpstream.URL})
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, credentials_encrypted, deleted_at, health_check_model
		) VALUES ('acc_fb', ?, 'openai', 'prof_1', 'openai', 'v1', 'fallback 账户', 'api_key', 'active', 1, ?, NULL, 'gpt-test')`,
		fixture.systemAccount, fallbackCredentials)
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at) VALUES ('group_fb', ?, 'acc_fb', 1, ?)`,
		fixture.systemAccount, now)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at) VALUES ('acc_fb', 'openai', 'gpt-test', ?)`, now)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_2', 'rs_1', ?, 'group_fb', 1, 1, 'active', ?)`, fixture.systemAccount, now)
	// 主组账户改指 dead upstream（连接拒绝 → transport failure → 耗尽）。
	seed(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": dead}), fixture.accountID)

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
		t.Fatalf("status=%d want 200 via fallback group: %s", status, raw)
	}
	if !strings.Contains(raw, "fallback 分组内容") {
		t.Fatalf("fallback content missing: %s", raw)
	}
	if fallbackHits != 1 {
		t.Fatalf("fallback upstream hits=%d want 1", fallbackHits)
	}
}

// TestGatewayChainDispatchExhaustedRendersFixedCopy：单分组 key 全部耗尽时
// 客户端收到 Node 固定文案 503，而不是逐账户诊断串。两条等价耗尽路径都可接
// 受：候选排空走 preflight RouteAction failure（没有可用的上游账户 /
// no_available_upstream_account），fetch 层耗尽走 attempt 出口（上游暂时不可
// 用，请重试 / upstream_retryable_error）——Node 顶层 catch 对两者渲染同样的
// 固定文案对。
func TestGatewayChainDispatchExhaustedRendersFixedCopy(t *testing.T) {
	fixture := newChainFixture(t)
	shortenChainWaitBudgets(t, fixture)
	dead := newDeadUpstreamAddr(t)
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": dead}), fixture.accountID); err != nil {
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
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503: %s", status, raw)
	}
	fixedCopy := strings.Contains(raw, "没有可用的上游账户") || strings.Contains(raw, "上游暂时不可用，请重试")
	if !fixedCopy {
		t.Fatalf("exhaustion copy wrong: %s", raw)
	}
	if strings.Contains(raw, "最后一次尝试") || strings.Contains(raw, "127.0.0.1:") {
		t.Fatalf("detailed diagnostics leaked to client: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// V3/V4/V5: error-exit rendering over a stubbed response sink
// ---------------------------------------------------------------------------

// recordingFailureSink captures SendGatewayFailureResponse inputs.
type recordingFailureSink struct {
	inputs []gatewaypreauth.FailureResponseInput
}

func (s *recordingFailureSink) SendGatewayFailureResponse(input gatewaypreauth.FailureResponseInput) {
	s.inputs = append(s.inputs, input)
}

func (s *recordingFailureSink) FinalizeGatewayAuthFailureAudit(*gatewaypreauth.GatewayRequest, gatewaypreauth.GatewayResponseWriter, gatewaypreauth.AuditCaptureContext) {
}

func (s *recordingFailureSink) SendAuthenticatedModelsGatewayResponse(gatewaypreauth.ModelsResponseInput) {
}
func (s *recordingFailureSink) SendOpenAIModelsGatewayResponse(gatewaypreauth.ModelsResponseInput) {}
func (s *recordingFailureSink) SendAnthropicModelsGatewayResponse(gatewaypreauth.ModelsResponseInput) {
}
func (s *recordingFailureSink) SendGeminiModelsGatewayResponse(gatewaypreauth.ModelsResponseInput) {}

// newV1TestLoop builds a dispatch loop over a stub service + sink.
func newV1TestLoop(t *testing.T, sink *recordingFailureSink) *v1DispatchLoop {
	t.Helper()
	observability := newSlogObservability(slog.New(slog.NewTextHandler(io.Discard, nil)), gatewaypreauth.SystemClock{})
	service := &gatewaypreauth.Service{
		Responses:     sink,
		Observability: observability,
		Clock:         gatewaypreauth.SystemClock{},
	}
	chain := &gatewayChain{preauth: service, observability: observability}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req := gatewaypreauth.NewGatewayRequest(request)
	res := gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())
	return &v1DispatchLoop{
		c:                   chain,
		req:                 req,
		res:                 res,
		auditCapture:        chainStubCapture{},
		requestSnapshot:     gatewaypreauth.UsageRequestSnapshot{},
		serverRetryBudget:   gatewaypreauth.NewServerRetryBudget(0, gatewaypreauth.SystemClock{}),
		startedAt:           1728000000000,
		endpoint:            "chat_completions",
		traceID:             "trace_v1",
		current:             &gatewaypreauth.DispatchContext{UsageContext: gatewaypreauth.GatewayFailureUsageContext{GroupID: "group_main", APIKeyID: "key_1", TrafficSource: "gateway"}},
		actionVisitedGroups: map[string]bool{},
		enteredGroups:       map[string]bool{},
	}
}

// chainStubCapture is the minimal AuditCaptureContext stub.
type chainStubCapture struct{}

func (chainStubCapture) BindContext(gatewaypreauth.AuditGatewayContext) {}
func (chainStubCapture) AddGatewayMetadata(string, map[string]any)      {}
func (chainStubCapture) Finalize(gatewaypreauth.AuditFinalizeInput)     {}

// TestV1DispatchLoopExhaustedCopyBranches covers the two fixed-copy branches
// of renderDispatchExhausted plus the unexpected-error contract.
func TestV1DispatchLoopExhaustedCopyBranches(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)

	// With a last attempt: retryable copy, detailed message only in audit.
	loop.renderDispatchExhausted(&gatewaydispatch.UpstreamAttemptError{
		Message:          "所有上游账户均失败；最后一次尝试 账户一 https://upstream 返回 500",
		LastAttempt:      &gatewaydispatch.UpstreamAttempt{AccountID: "acc_1", UpstreamURL: "https://upstream", HasStatus: true, Status: 500},
		FailedAccountIDs: []string{"acc_1"},
	})
	if len(sink.inputs) != 1 {
		t.Fatalf("inputs=%d", len(sink.inputs))
	}
	first := sink.inputs[0]
	if first.ResponsePayload.Error.Message != "上游暂时不可用，请重试" || first.ResponsePayload.Error.Code != "upstream_retryable_error" {
		t.Fatalf("payload wrong: %+v", first.ResponsePayload)
	}
	if first.Audit.Outcome != gatewaypreauth.AuditOutcomeUpstreamFailed || first.Audit.ErrorMessage == "" {
		t.Fatalf("audit wrong: %+v", first.Audit)
	}
	if first.RecordUsage == nil || *first.RecordUsage {
		t.Fatalf("recordUsage must be false with a last attempt: %+v", first.RecordUsage)
	}

	// Without a last attempt: the no-candidate copy.
	sink.inputs = nil
	loop.renderDispatchExhausted(&gatewaydispatch.UpstreamAttemptError{Message: "上游账户请求失败"})
	if len(sink.inputs) != 1 {
		t.Fatalf("inputs=%d", len(sink.inputs))
	}
	second := sink.inputs[0]
	if second.ResponsePayload.Error.Message != "没有可用的上游账户" || second.ResponsePayload.Error.Code != "no_available_upstream_account" {
		t.Fatalf("no-candidate payload wrong: %+v", second.ResponsePayload)
	}
	if second.RecordUsage == nil || !*second.RecordUsage {
		t.Fatalf("recordUsage must be true without a last attempt")
	}

	// Unexpected dispatch error: the 503 upstream contract, never a 500.
	sink.inputs = nil
	loop.renderUnexpectedDispatchFailure(errors.New("意外的调度故障"))
	if len(sink.inputs) != 1 {
		t.Fatalf("inputs=%d", len(sink.inputs))
	}
	third := sink.inputs[0]
	if third.StatusCode != http.StatusServiceUnavailable || third.ResponsePayload.Error.Message != "上游暂时不可用，请重试" {
		t.Fatalf("unexpected-error contract wrong: %+v", third)
	}
	if third.Audit.Outcome != gatewaypreauth.AuditOutcomeUpstreamFailed {
		t.Fatalf("audit outcome wrong: %+v", third.Audit)
	}
}

// TestV1DispatchLoopWallBudgetKinds covers the V4 split: coordination →
// client handoff, wall → the fixed 503.
func TestV1DispatchLoopWallBudgetKinds(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)

	settled := loop.settleDispatchError(context.Background(), &gatewaydispatch.GatewayRequestWallBudgetExhaustedError{
		BudgetKind:      gatewaydispatch.WallBudgetKindCoordination,
		WallRemainingMs: 12,
	})
	if !settled {
		t.Fatal("coordination wall error must settle the request")
	}
	if len(sink.inputs) != 1 {
		t.Fatalf("inputs=%d", len(sink.inputs))
	}
	handoff := sink.inputs[0]
	if handoff.ResponsePayload.Error.Message != "网关请求协调预算已到，请客户端重试并重新选择可用账户" {
		t.Fatalf("handoff copy wrong: %+v", handoff.ResponsePayload)
	}
	if handoff.Audit.Outcome != gatewaypreauth.AuditOutcomeStreamFailed {
		t.Fatalf("handoff audit wrong: %+v", handoff.Audit)
	}

	sink.inputs = nil
	settled = loop.settleDispatchError(context.Background(), &gatewaydispatch.GatewayRequestWallBudgetExhaustedError{
		BudgetKind:      gatewaydispatch.WallBudgetKindWall,
		WallRemainingMs: 0,
	})
	if !settled {
		t.Fatal("wall error must settle the request")
	}
	if len(sink.inputs) != 1 {
		t.Fatalf("inputs=%d", len(sink.inputs))
	}
	wall := sink.inputs[0]
	if wall.ResponsePayload.Error.Code != "gateway_request_wall_budget_exhausted" {
		t.Fatalf("wall contract wrong: %+v", wall.ResponsePayload)
	}
}

// TestV1RouteActionClientHandoffExit covers the V2 client_handoff rendering of
// finalizeRouteAction.
func TestV1RouteActionClientHandoffExit(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	loop.finalizeRouteAction(&gatewaypreauth.RouteAction{
		Coordination: gatewaypreauth.RouteActionCoordination{Outcome: gatewaypreauth.RouteOutcomeClientHandoff, Reason: "route_coordination_budget_exhausted"},
		UsageContext: loop.current.UsageContext,
	})
	if len(sink.inputs) != 1 {
		t.Fatalf("inputs=%d", len(sink.inputs))
	}
	handoff := sink.inputs[0]
	if handoff.ResponsePayload.Error.Message != "当前路由暂时无法继续派发，请客户端重试并重新选择可用账户" {
		t.Fatalf("client-handoff copy wrong: %+v", handoff.ResponsePayload)
	}
	if handoff.Audit.Outcome != gatewaypreauth.AuditOutcomeStreamFailed {
		t.Fatalf("client-handoff audit wrong: %+v", handoff.Audit)
	}
}

// ---------------------------------------------------------------------------
// V7: audit capture lifecycle
// ---------------------------------------------------------------------------

// TestAuditCaptureCancelReleasesActiveSlot：未完成 capture 的 Cancel 回收活跃
// 计数，且幂等。
func TestAuditCaptureCancelReleasesActiveSlot(t *testing.T) {
	t.Cleanup(gatewayusage.ResetActiveAuditCaptureCountForTest)
	gatewayusage.ResetActiveAuditCaptureCountForTest()

	capture := gatewayusage.NewAuditCaptureContext(gatewayusage.AuditCaptureInput{
		TraceID:       "trace_cancel",
		StartedAtMs:   1728000000000,
		TrafficSource: "gateway",
		Method:        http.MethodPost,
		Path:          "/v1/chat/completions",
		Settings: gatewayusage.FixedAuditLogSettingsSource{Settings: gatewayusage.AuditLogSettings{
			Enabled: true,
		}},
	})
	if got := gatewayusage.GetActiveAuditCaptureCount(); got != 1 {
		t.Fatalf("active count after create=%d want 1", got)
	}
	// The chain adapter exposes the canceller; Cancel twice (failure path +
	// finally) keeps one decrement.
	var canceller gatewaypreauth.AuditCaptureCanceller = preauthAuditCapture{inner: capture}
	canceller.Cancel()
	canceller.Cancel()
	if got := gatewayusage.GetActiveAuditCaptureCount(); got != 0 {
		t.Fatalf("active count after cancel=%d want 0", got)
	}
}

// TestAuditCaptureLifecycleClosesOverChainRequest：启用 audit 的完整请求后，
// 活跃 capture 计数回到基线（finalized/canceled 回收闭合）。
func TestAuditCaptureLifecycleClosesOverChainRequest(t *testing.T) {
	fixture := newChainFixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-audit","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"审计"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": upstream.URL}), fixture.accountID); err != nil {
		t.Fatalf("update account credentials: %v", err)
	}

	deps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool"))
	deps.AuditLogEnabled = func() bool { return true }
	chain, shutdown, err := composeGatewayChain(deps)
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()

	baseline := gatewayusage.GetActiveAuditCaptureCount()
	status, raw := chainV1ChatRequest(t, server.URL, fixture.apiKeySecret,
		`{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status=%d: %s", status, raw)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if gatewayusage.GetActiveAuditCaptureCount() == baseline {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("active capture count=%d did not return to baseline %d", gatewayusage.GetActiveAuditCaptureCount(), baseline)
}

// ---------------------------------------------------------------------------
// V10: request.accepted ordering
// ---------------------------------------------------------------------------

// TestGatewayChainPreauthRejectLogsNoAcceptedStage：preauth 段 401 拒绝发生
// 在 request.accepted 之前（Node server-level middleware order），因此日志中
// 没有 accepted 阶段。
func TestGatewayChainPreauthRejectLogsNoAcceptedStage(t *testing.T) {
	fixture := newChainFixture(t)
	logBuffer := &bytes.Buffer{}
	deps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool"))
	deps.Logger = slog.New(slog.NewTextHandler(logBuffer, nil))
	chain, shutdown, err := composeGatewayChain(deps)
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()

	status, raw := chainV1ChatRequest(t, server.URL, "sk-not-a-real-key",
		`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401: %s", status, raw)
	}
	if strings.Contains(logBuffer.String(), "request.accepted") {
		t.Fatalf("request.accepted logged before the preauth rejection:\n%s", logBuffer.String())
	}
}
