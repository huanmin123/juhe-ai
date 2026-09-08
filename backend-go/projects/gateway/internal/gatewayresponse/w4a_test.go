package gatewayresponse

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// W4-A（BUG-0175 波4 响应/流式层缺陷族）Mock 回归：
// D-116 响应头转发 + http metric 失败域标注、D-122 失败响应审计/usage 快照、
// D-125 models 审计首 token + usageSemantic、D-112 非流式 JSON 检查主链。

// ---- D-116 / D-122：http metric 失败域标注口 ----

type mockFailureScopeMarker struct {
	mu     sync.Mutex
	scopes []string
}

func (m *mockFailureScopeMarker) MarkFailureScope(scope string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scopes = append(m.scopes, scope)
}

func (m *mockFailureScopeMarker) recorded() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.scopes...)
}

func withFailureScopeMarker(t *testing.T) *mockFailureScopeMarker {
	t.Helper()
	marker := &mockFailureScopeMarker{}
	SetHTTPMetricFailureScopeMarker(marker)
	t.Cleanup(func() { SetHTTPMetricFailureScopeMarker(nil) })
	return marker
}

// TestPrepareUpstreamResponseForDownstreamForwardsUpstreamHeaders 对齐
// downstream-headers.ts copyResponseHeaders：上游响应头转发到客户端，逐跳头
// /网关头/连接令牌头剔除（D-116）。
func TestPrepareUpstreamResponseForDownstreamForwardsUpstreamHeaders(t *testing.T) {
	upstream := &GatewayUpstreamResponse{
		Status: 200,
		Header: http.Header{
			"X-Request-Id":   {"req-1"},
			"Content-Type":   {"application/json"},
			"Content-Length": {"42"},
			"Connection":     {"close, x-close-token"},
			"X-Close-Token":  {"drop-me"},
			"Cf-Aig-Trace":   {"drop-by-prefix"},
			"Set-Cookie":     {"session=1"},
		},
	}
	recorder := httptest.NewRecorder()
	downstream := StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(recorder)}
	prepareUpstreamResponseForDownstream(downstream, upstream, false)
	if recorder.Code != 200 {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("X-Request-Id"); got != "req-1" {
		t.Fatalf("x-request-id = %q", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q", got)
	}
	for _, name := range []string{"Content-Length", "Connection", "X-Close-Token", "Cf-Aig-Trace", "Set-Cookie"} {
		if got := recorder.Header().Get(name); got != "" {
			t.Fatalf("%s 不应转发，实际 = %q", name, got)
		}
	}
}

// TestPrepareUpstreamResponseForDownstreamStreamHeaders 流式模式补齐 SSE 头且
// 全部头先于 WriteHeader（Go 响应头立即提交语义）。
func TestPrepareUpstreamResponseForDownstreamStreamHeaders(t *testing.T) {
	upstream := &GatewayUpstreamResponse{Status: 200, Header: http.Header{}}
	recorder := httptest.NewRecorder()
	downstream := StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(recorder)}
	prepareUpstreamResponseForDownstream(downstream, upstream, true)
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("content-type = %q", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-cache, no-transform" {
		t.Fatalf("cache-control = %q", got)
	}
	if got := recorder.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("x-accel-buffering = %q", got)
	}
}

// TestMarkHTTPMetricFailureScopeOnUpstreamFailure D-116：上游失败进入下游
// 准备时标注 upstream 失败域（downstream-headers.ts:17-19）。
func TestMarkHTTPMetricFailureScopeOnUpstreamFailure(t *testing.T) {
	marker := withFailureScopeMarker(t)
	upstream := &GatewayUpstreamResponse{Status: 502, Header: http.Header{}}
	downstream := StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())}
	prepareUpstreamResponseForDownstream(downstream, upstream, false)
	scopes := marker.recorded()
	if len(scopes) != 1 || scopes[0] != "upstream" {
		t.Fatalf("scopes = %v", scopes)
	}
	// 成功响应不标注。
	prepareUpstreamResponseForDownstream(StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())}, &GatewayUpstreamResponse{Status: 200, Header: http.Header{}}, false)
	if scopes := marker.recorded(); len(scopes) != 1 {
		t.Fatalf("成功响应不应标注，scopes = %v", scopes)
	}
}

// ---- D-122：失败响应审计 ResponseHeaders + usage 快照 errorMessage ----

func TestSinkFailureResponseAuditHeadersAndUsageSnapshot(t *testing.T) {
	marker := withFailureScopeMarker(t)
	sink, tracking, _, audit, usage, observer := newSinkFixture()
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	recordUsage := true
	tracking.Header().Set("X-Gateway-Trace", "trace-9")
	sink.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:             req,
		Res:             tracking,
		AuditCapture:    audit,
		UsageContext:    usageContextFixture(),
		StartedAt:       1000,
		StatusCode:      502,
		ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf("上游返回失败终态", "upstream_response_error", "upstream_protocol_failure"),
		Audit: gatewaypreauth.FailureAudit{
			Outcome:      "upstream_failed",
			ErrorPhase:   "upstream_response",
			ErrorCode:    "upstream_protocol_failure",
			ErrorMessage: "上游返回失败终态",
		},
		RecordUsage: &recordUsage,
	})
	// failureScope 缺省推断为 upstream（D-122 弃用面恢复）。
	if scopes := marker.recorded(); len(scopes) != 1 || scopes[0] != "upstream" {
		t.Fatalf("scopes = %v", scopes)
	}
	if len(audit.finalized) != 1 {
		t.Fatalf("finalize = %+v", audit.finalized)
	}
	final := audit.finalized[0]
	// 审计收尾携带响应头快照（Node responseHeadersToObject(res)）。
	if got, ok := final.ResponseHeaders["X-Gateway-Trace"]; !ok || got != "trace-9" {
		t.Fatalf("responseHeaders = %+v", final.ResponseHeaders)
	}
	observer.ch <- 9000
	waitFor(t, func() bool { return usage.failureCount() > 0 })
	record := usage.lastFailure()
	if record.ResponseSnapshot == nil {
		t.Fatalf("responseSnapshot 缺失")
	}
	if record.ResponseSnapshot.ErrorMessage != "上游返回失败终态" {
		t.Fatalf("snapshot errorMessage = %q", record.ResponseSnapshot.ErrorMessage)
	}
	if record.ResponseSnapshot.GeneratedBy != "gateway" {
		t.Fatalf("snapshot generatedBy = %q", record.ResponseSnapshot.GeneratedBy)
	}
}

// ---- D-125：models 审计 firstTokenMs + usageSemantic ----

type extenderAuditCapture struct {
	*mockAuditCapture
	mu             sync.Mutex
	extended       []AuditFinalizeExtras
	extendedBodies []gatewaypreauth.AuditFinalizeInput
}

func (e *extenderAuditCapture) FinalizeExtended(input gatewaypreauth.AuditFinalizeInput, extras AuditFinalizeExtras) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.extended = append(e.extended, extras)
	e.extendedBodies = append(e.extendedBodies, input)
}

func TestUsageSemanticForProviderCode(t *testing.T) {
	cases := map[string]string{
		"anthropic":        "anthropic",
		"Anthropic":        "anthropic",
		"gemini":           "gemini",
		"openai":           "openai",
		"gpt":              "openai",
		"deepseek":         "openai",
		"unknown-provider": "openai",
		"":                 "openai",
	}
	for providerCode, want := range cases {
		if got := usageSemanticForProviderCode(providerCode); got != want {
			t.Fatalf("usageSemanticForProviderCode(%q) = %q, want %q", providerCode, got, want)
		}
	}
}

func TestSinkModelsAuditFirstTokenMsAndSemantic(t *testing.T) {
	sink, tracking, _, _, usage, _ := newSinkFixture()
	capture := &extenderAuditCapture{mockAuditCapture: newMockAuditCapture()}
	input := gatewaypreauth.ModelsResponseInput{
		Req:          gatewaypreauth.NewGatewayRequest(httptest.NewRequest("GET", "/v1/models", nil)),
		Res:          tracking,
		AuditCapture: capture,
		UsageContext: usageContextFixture(),
		StartedAt:    1000,
	}
	sink.SendOpenAIModelsGatewayResponse(input)
	if len(capture.extended) != 1 {
		t.Fatalf("extended finalize = %+v", capture.extended)
	}
	extras := capture.extended[0]
	if extras.FirstTokenMs == nil || *extras.FirstTokenMs != 4000 {
		t.Fatalf("firstTokenMs = %+v", extras.FirstTokenMs)
	}
	if len(usage.dispatch) != 1 {
		t.Fatalf("dispatch = %+v", usage.dispatch)
	}
	// usageSemantic 按 providerCode 驱动回退解析（D-125 空占位修复）。
	if got := usage.dispatch[0].UsageSemantic; got != "openai" {
		t.Fatalf("usageSemantic = %q", got)
	}
	// anthropic 供应商的 models 调用语义应为 anthropic。
	sink2, tracking2, _, _, usage2, _ := newSinkFixture()
	sink2.SendAnthropicModelsGatewayResponse(gatewaypreauth.ModelsResponseInput{
		Req:           gatewaypreauth.NewGatewayRequest(httptest.NewRequest("GET", "/v1/models", nil)),
		Res:           tracking2,
		AuditCapture:  newMockAuditCapture(),
		UsageContext:  usageContextFixture(),
		ProviderCodes: []string{"anthropic"},
		StartedAt:     1000,
	})
	if len(usage2.dispatch) != 1 || usage2.dispatch[0].UsageSemantic != "anthropic" {
		t.Fatalf("anthropic semantic = %+v", usage2.dispatch)
	}
}

// ---- D-112：非流式 JSON 检查主链 ----

func inspectionPolicyFixture(id string, action string, match gatewayruntimecache.ResponseInspectionPolicyMatch) gatewayruntimecache.ResponseInspectionPolicySummary {
	providerCode := "openai"
	return gatewayruntimecache.ResponseInspectionPolicySummary{
		ID:           id,
		Name:         id + "-name",
		Enabled:      true,
		Priority:     10,
		ScopeType:    "provider",
		ProtocolCode: "openai",
		ProviderCode: &providerCode,
		Match:        match,
		Action:       action,
	}
}

func TestInspectBufferedGatewayJSONResponseReplacesWithFailure(t *testing.T) {
	marker := withFailureScopeMarker(t)
	input, recorder := newInputFixture(nil, 200, map[string]string{"Content-Type": "application/json"})
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}}
	input.ClientStrategy = &ClientStrategyView{ClientProfile: "generic", InterpretSemantics: true}
	input.ResponseInspectionPolicies = []gatewayruntimecache.ResponseInspectionPolicySummary{
		inspectionPolicyFixture("policy-1", "replace_with_failure", gatewayruntimecache.ResponseInspectionPolicyMatch{
			OutputTextIncludes: []string{"forbidden-phrase"},
		}),
	}
	body := `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"包含 forbidden-phrase 的回答"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":5}}`
	handled := input.inspectBufferedGatewayJSONResponse(InspectBufferedGatewayJSONArgs{
		ResponseBody:     []byte(body),
		ResponseBodyText: body,
	})
	if handled == nil {
		t.Fatalf("检查命中时必须接管响应")
	}
	if handled.RetryUpstream || !handled.AlreadyFinalized {
		t.Fatalf("handled = %+v", handled)
	}
	if recorder.Code != 502 {
		t.Fatalf("status = %d", recorder.Code)
	}
	completed := input.AuditCapture.(*mockAuditCapture)
	if len(completed.completed) != 1 {
		t.Fatalf("completeAttempt = %+v", completed.completed)
	}
	attempt := completed.completed[0]
	if attempt.ErrorPhase != "response_inspection" || attempt.Success {
		t.Fatalf("attempt = %+v", attempt)
	}
	if attempt.ErrorCode != "response_inspection_matched" || !strings.Contains(attempt.ErrorMessage, "policy-1-name") {
		t.Fatalf("attempt 错误事实 = %+v", attempt)
	}
	if len(completed.finalized) != 1 || completed.finalized[0].Outcome != "upstream_failed" || completed.finalized[0].StatusCode != 502 {
		t.Fatalf("finalize = %+v", completed.finalized)
	}
	if scopes := marker.recorded(); len(scopes) != 1 || scopes[0] != "upstream" {
		t.Fatalf("scopes = %v", scopes)
	}
}

func TestInspectBufferedGatewayJSONResponseServerRetry(t *testing.T) {
	input, _ := newInputFixture(nil, 200, map[string]string{"Content-Type": "application/json"})
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}}
	input.ClientStrategy = &ClientStrategyView{ClientProfile: "generic", InterpretSemantics: true}
	input.ResponseInspectionPolicies = []gatewayruntimecache.ResponseInspectionPolicySummary{
		inspectionPolicyFixture("policy-retry", "retry_next_account", gatewayruntimecache.ResponseInspectionPolicyMatch{
			OutputTextIncludes: []string{"forbidden-phrase"},
		}),
	}
	body := `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"forbidden-phrase"},"finish_reason":"stop"}]}`
	handled := input.inspectBufferedGatewayJSONResponse(InspectBufferedGatewayJSONArgs{
		ResponseBody:     []byte(body),
		ResponseBodyText: body,
	})
	if handled == nil || !handled.RetryUpstream {
		t.Fatalf("handled = %+v", handled)
	}
	if handled.RetryReason != StreamServerRetryResponseInspection || handled.ResponseInspection == nil {
		t.Fatalf("verdict = %+v", handled)
	}
	// 已提交下游后不再重试。
	committedInput, _ := newInputFixture(nil, 200, map[string]string{"Content-Type": "application/json"})
	committedInput.Deps = input.Deps
	committedInput.ClientStrategy = input.ClientStrategy
	committedInput.ResponseInspectionPolicies = input.ResponseInspectionPolicies
	committedInput.DownstreamCommitState.MarkSemanticCommitted(128)
	handled = committedInput.inspectBufferedGatewayJSONResponse(InspectBufferedGatewayJSONArgs{
		ResponseBody:     []byte(body),
		ResponseBodyText: body,
	})
	if handled == nil || handled.RetryUpstream {
		t.Fatalf("已提交下游应改写失败而非重试，handled = %+v", handled)
	}
}

func TestInspectBufferedGatewayJSONResponseNoPolicyFallsThrough(t *testing.T) {
	input, _ := newInputFixture(nil, 200, map[string]string{"Content-Type": "application/json"})
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}}
	input.ClientStrategy = &ClientStrategyView{ClientProfile: "generic", InterpretSemantics: true}
	body := `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"正常回答"},"finish_reason":"stop"}]}`
	if handled := input.inspectBufferedGatewayJSONResponse(InspectBufferedGatewayJSONArgs{
		ResponseBody:     []byte(body),
		ResponseBodyText: body,
	}); handled != nil {
		t.Fatalf("未命中策略应回退转发，handled = %+v", handled)
	}
}

func TestInspectBufferedGatewayJSONResponseCodexCyberPolicySkips(t *testing.T) {
	input, recorder := newInputFixture(nil, 502, map[string]string{"Content-Type": "application/json"})
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}}
	input.ClientStrategy = &ClientStrategyView{ClientProfile: "codex", InterpretSemantics: true}
	body := `{"error":{"code":"cyber_policy","message":"blocked"},"status":"failed"}`
	if handled := input.inspectBufferedGatewayJSONResponse(InspectBufferedGatewayJSONArgs{
		ResponseBody:     []byte(body),
		ResponseBodyText: body,
	}); handled != nil {
		t.Fatalf("codex cyber_policy 失败体应原样透传，handled = %+v", handled)
	}
	if recorder.Code != 200 {
		t.Fatalf("不应改写状态码，status = %d", recorder.Code)
	}
}

// waitFor 轮询条件直到成立或超时。
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

// ---- D-120：SSE 等待心跳 ----

// TestHeartbeatProtocolGate 非 SSE 协议不创建心跳；构造本身不写出。
func TestHeartbeatProtocolGate(t *testing.T) {
	if heartbeat := CreateGatewaySseWaitHeartbeat(HeartbeatDeps{
		Res:                gatewaypreauth.NewTrackingWriter(httptest.NewRecorder()),
		DownstreamProtocol: "chat_completions_json",
	}); heartbeat != nil {
		t.Fatalf("非 SSE 协议应返回 nil")
	}
	recorder := httptest.NewRecorder()
	heartbeat := CreateGatewaySseWaitHeartbeat(HeartbeatDeps{
		Res:                gatewaypreauth.NewTrackingWriter(recorder),
		DownstreamProtocol: "chat_completions_sse",
		DownstreamCommit:   &DownstreamCommitState{},
	})
	if heartbeat == nil {
		t.Fatalf("SSE 协议应返回心跳实例")
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("构造不应写出，body = %q", recorder.Body.String())
	}
}

// TestHeartbeatObserverWritesAndStops 观察者等待开始时立即写出首个保活块并
// 标记 transport committed；暂停后停止写出（D-120 装配面）。
func TestHeartbeatObserverWritesAndStops(t *testing.T) {
	commit := &DownstreamCommitState{}
	recorder := httptest.NewRecorder()
	heartbeat := CreateGatewaySseWaitHeartbeat(HeartbeatDeps{
		Res:                gatewaypreauth.NewTrackingWriter(recorder),
		DownstreamProtocol: "messages_sse",
		DownstreamCommit:   commit,
	})
	if heartbeat == nil {
		t.Fatalf("心跳创建失败")
	}
	observer := &gatewaypreauth.ServerRetryBudgetWaitObserver{
		OnWaitStarted: heartbeat.Start,
		OnWaitPaused:  heartbeat.Stop,
	}
	observer.OnWaitStarted()
	waitFor(t, func() bool { return strings.Contains(recorder.Body.String(), "juhe-ai waiting for upstream capacity") })
	if !commit.TransportCommitted {
		t.Fatalf("心跳应标记 transport committed")
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("content-type = %q", got)
	}
	writes := recorder.Body.Len()
	observer.OnWaitPaused()
	time.Sleep(30 * time.Millisecond)
	if recorder.Body.Len() != writes {
		t.Fatalf("暂停后不应继续写出")
	}
	// 语义提交后心跳不再写出。
	observer.OnWaitStarted()
	commit.MarkSemanticCommitted(1)
	time.Sleep(30 * time.Millisecond)
	if recorder.Body.Len() != writes {
		t.Fatalf("语义提交后不应写出")
	}
	heartbeat.Stop()
}
