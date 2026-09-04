package gatewayresponse

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func newSinkFixture() (Sink, *gatewaypreauth.TrackingWriter, *httptest.ResponseRecorder, *mockAuditCapture, *mockUsageRecords, *mockHTTPObserver) {
	recorder := httptest.NewRecorder()
	tracking := gatewaypreauth.NewTrackingWriter(recorder)
	audit := newMockAuditCapture()
	usage := &mockUsageRecords{}
	completionCh := make(chan int64, 1)
	observer := &mockHTTPObserver{ch: completionCh}
	sink := NewSink(SinkDeps{
		UsageRecords:   usage,
		UsageDispatch:  usage,
		ModelCatalog:   &mockCatalogLoader{},
		HTTPCompletion: observer,
		NowMs:          func() int64 { return 5000 },
	})
	return *sink, tracking, recorder, audit, usage, observer
}

func TestSinkSendGatewayFailureResponse(t *testing.T) {
	sink, tracking, recorder, audit, usage, observer := newSinkFixture()
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	recordUsage := true
	sink.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:          req,
		Res:          tracking,
		AuditCapture: audit,
		UsageContext: usageContextFixture(),
		StartedAt:    1000,
		StatusCode:   429,
		ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf("请求过于频繁", "rate_limit_exceeded", "rate_limit_exceeded"),
		Audit: gatewaypreauth.FailureAudit{
			Outcome:      "gateway_failed",
			ErrorPhase:   "quota",
			ErrorCode:    "rate_limit_exceeded",
			ErrorMessage: "请求过于频繁",
		},
		RecordUsage: &recordUsage,
	})
	if recorder.Code != 429 {
		t.Fatalf("status = %d", recorder.Code)
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body = %q", recorder.Body.String())
	}
	if payload.Error.Message != "请求过于频繁" || payload.Error.Type != "rate_limit_exceeded" {
		t.Fatalf("payload = %+v", payload)
	}
	if len(audit.finalized) != 1 {
		t.Fatalf("finalize = %+v", audit.finalized)
	}
	final := audit.finalized[0]
	if final.Outcome != "gateway_failed" || final.Success || final.ResponsePartType != "gateway_error" || final.ErrorPhase != "quota" {
		t.Fatalf("finalize = %+v", final)
	}
	if !strings.Contains(final.ResponseBody, "请求过于频繁") {
		t.Fatalf("audit body = %q", final.ResponseBody)
	}
	// usage 记录等待 HTTP 完成（G17 消费）。
	observer.ch <- 9000
	deadline := time.Now().Add(2 * time.Second)
	for usage.failureCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if usage.failureCount() != 1 {
		t.Fatalf("failure usage records = %d", usage.failureCount())
	}
	record := usage.lastFailure()
	if record.StatusCode != 429 || record.CompletedAtMs != 9000 || record.StartedAtMs != 1000 {
		t.Fatalf("record = %+v", record)
	}
	if !strings.Contains(record.ResponseSnapshot.BodyText, "请求过于频繁") {
		t.Fatalf("snapshot = %+v", record.ResponseSnapshot)
	}
}

// markerWriter 模拟生产链路中 kernel 包裹层（localizeWriter）的 UpstreamMarker
// 实现：TrackingWriter.MarkUpstreamError 会转发到它。
type markerWriter struct {
	*gatewaypreauth.TrackingWriter
	marked bool
}

func (m *markerWriter) MarkUpstream()        { m.marked = true }
func (m *markerWriter) MarkedUpstream() bool { return m.marked }

func TestSinkSendGatewayFailureResponsePreservesUpstreamMessage(t *testing.T) {
	sink, _, _, audit, _, _ := newSinkFixture()
	recorder2 := httptest.NewRecorder()
	tracking := &markerWriter{TrackingWriter: gatewaypreauth.NewTrackingWriter(recorder2)}
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	preserve := true
	sink.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:          req,
		Res:          tracking,
		AuditCapture: audit,
		UsageContext: usageContextFixture(),
		StartedAt:    1000,
		StatusCode:   502,
		ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf("provider boom", "upstream_error"),
		Audit: gatewaypreauth.FailureAudit{
			Outcome:      "upstream_failed",
			ErrorPhase:   "upstream_response",
			ErrorCode:    "upstream_error",
			ErrorMessage: "provider boom",
		},
		RecordUsage:                  &preserve,
		PreserveUpstreamErrorMessage: true,
	})
	if !strings.Contains(recorder2.Body.String(), "provider boom") {
		t.Fatalf("body = %q", recorder2.Body.String())
	}
	if !strings.Contains(audit.finalized[0].ResponseBody, "provider boom") {
		t.Fatalf("audit body = %q", audit.finalized[0].ResponseBody)
	}
}

func TestSinkFinalizeGatewayAuthFailureAudit(t *testing.T) {
	sink, _, _, _, _, _ := newSinkFixture()
	recorder := httptest.NewRecorder()
	tracking := gatewaypreauth.NewTrackingWriter(recorder)
	tracking.WriteHeader(401)
	audit := newMockAuditCapture()
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	sink.FinalizeGatewayAuthFailureAudit(req, tracking, audit)
	if len(audit.finalized) != 1 {
		t.Fatalf("finalized = %d", len(audit.finalized))
	}
	final := audit.finalized[0]
	if final.ErrorMessage != "缺少访问令牌" || final.ErrorCode != "invalid_request_error" || final.StatusCode != 401 {
		t.Fatalf("finalize = %+v", final)
	}
	// 带 Bearer 的请求 → API Key 无效。
	tracking2 := gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())
	req2 := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	req2.HTTP.Header.Set("Authorization", "Bearer sk-xxx")
	audit2 := newMockAuditCapture()
	sink.FinalizeGatewayAuthFailureAudit(req2, tracking2, audit2)
	if audit2.finalized[0].ErrorMessage != "API Key 无效" {
		t.Fatalf("message = %q", audit2.finalized[0].ErrorMessage)
	}
	// 请求视图已带认证失败文案时优先。
	req3 := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	req3.AuthFailureErrorMessage = "Key 已停用"
	req3.AuthFailureErrorCode = "key_disabled"
	audit3 := newMockAuditCapture()
	sink.FinalizeGatewayAuthFailureAudit(req3, tracking2, audit3)
	if audit3.finalized[0].ErrorMessage != "Key 已停用" || audit3.finalized[0].ErrorCode != "key_disabled" {
		t.Fatalf("finalize = %+v", audit3.finalized[0])
	}
}

func TestSinkSendOpenAIModelsGatewayResponse(t *testing.T) {
	sink, tracking, recorder, audit, usage, _ := newSinkFixture()
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("GET", "/v1/models", nil))
	input := gatewaypreauth.ModelsResponseInput{
		Req:           req,
		Res:           tracking,
		AuditCapture:  audit,
		UsageContext:  usageContextFixture(),
		ProviderCodes: []string{"OpenAI"},
		StartedAt:     1000,
	}
	sink.SendOpenAIModelsGatewayResponse(input)
	if recorder.Code != 200 {
		t.Fatalf("status = %d", recorder.Code)
	}
	var payload struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body = %q", recorder.Body.String())
	}
	if payload.Object != "list" || len(payload.Data) != 2 {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Data[0].ID != "gpt-x" || payload.Data[0].OwnedBy != "openai" {
		t.Fatalf("item0 = %+v", payload.Data[0])
	}
	if payload.Data[0].Created != 1717200000 { // 2024-06-01T00:00:00Z
		t.Fatalf("created = %d", payload.Data[0].Created)
	}
	if payload.Data[1].OwnedBy != "juhe-ai" {
		t.Fatalf("item1 = %+v", payload.Data[1])
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "private, no-cache" {
		t.Fatalf("cache-control = %q", cacheControl)
	}
	if vary := recorder.Header().Get("Vary"); !strings.Contains(vary, "Authorization") || !strings.Contains(vary, "X-Codex-Client") {
		t.Fatalf("vary = %q", vary)
	}
	if len(audit.finalized) != 1 || audit.finalized[0].Outcome != "success" || audit.finalized[0].StatusCode != 200 {
		t.Fatalf("finalize = %+v", audit.finalized)
	}
	if len(usage.dispatch) != 1 || usage.dispatch[0].ProviderCode != "openai" || !usage.dispatch[0].Success {
		t.Fatalf("dispatch = %+v", usage.dispatch)
	}
}

func TestSinkSendAnthropicAndGeminiModelsResponses(t *testing.T) {
	sink, tracking, recorder, audit, _, _ := newSinkFixture()
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("GET", "/v1/models", nil))
	input := gatewaypreauth.ModelsResponseInput{
		Req:           req,
		Res:           tracking,
		AuditCapture:  audit,
		UsageContext:  usageContextFixture(),
		ProviderCodes: []string{"anthropic"},
		StartedAt:     1000,
	}
	sink.SendAnthropicModelsGatewayResponse(input)
	var anthropicPayload struct {
		Data []struct {
			Type        string `json:"type"`
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &anthropicPayload); err != nil {
		t.Fatalf("anthropic body = %q", recorder.Body.String())
	}
	if len(anthropicPayload.Data) != 1 || anthropicPayload.Data[0].Type != "model" || anthropicPayload.Data[0].ID != "claude-x" {
		t.Fatalf("anthropic payload = %+v", anthropicPayload)
	}

	recorder2 := httptest.NewRecorder()
	tracking2 := gatewaypreauth.NewTrackingWriter(recorder2)
	audit2 := newMockAuditCapture()
	input2 := gatewaypreauth.ModelsResponseInput{
		Req:           gatewaypreauth.NewGatewayRequest(httptest.NewRequest("GET", "/v1beta/models", nil)),
		Res:           tracking2,
		AuditCapture:  audit2,
		UsageContext:  usageContextFixture(),
		ProviderCodes: []string{"gemini"},
		StartedAt:     1000,
	}
	sink.SendGeminiModelsGatewayResponse(input2)
	var geminiPayload struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if err := json.Unmarshal(recorder2.Body.Bytes(), &geminiPayload); err != nil {
		t.Fatalf("gemini body = %q", recorder2.Body.String())
	}
	if len(geminiPayload.Models) != 1 || geminiPayload.Models[0].Name != "models/gemini-x" {
		t.Fatalf("gemini payload = %+v", geminiPayload)
	}
}

func TestSinkCodexModelsShape(t *testing.T) {
	sink, tracking, recorder, _, _, _ := newSinkFixture()
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("GET", "/v1/models?client_version=1", nil))
	req.HTTP.Header.Set("Originator", "codex_cli_rs")
	input := gatewaypreauth.ModelsResponseInput{
		Req:          req,
		Res:          tracking,
		AuditCapture: newMockAuditCapture(),
		UsageContext: usageContextFixture(),
		StartedAt:    1000,
	}
	sink.SendOpenAIModelsGatewayResponse(input)
	var payload struct {
		Models []struct {
			Slug             string `json:"slug"`
			DisplayName      string `json:"display_name"`
			ShellType        string `json:"shell_type"`
			ContextWindow    int    `json:"context_window"`
			BaseInstructions string `json:"base_instructions"`
		} `json:"models"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body = %q", recorder.Body.String())
	}
	if len(payload.Models) != 2 {
		t.Fatalf("models = %+v", payload.Models)
	}
	if payload.Models[0].Slug != "gpt-x" || payload.Models[0].ShellType != "shell_command" || payload.Models[0].ContextWindow != 272000 {
		t.Fatalf("model0 = %+v", payload.Models[0])
	}
	if payload.Models[0].BaseInstructions != "You are Codex, a coding agent." {
		t.Fatalf("base instructions = %q", payload.Models[0].BaseInstructions)
	}
}
