package gatewayresponse

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

var gatewayAccountFixture = gatewayAccountFixtureValue()

func gatewayAccountFixtureValue() gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID:                "acc-1",
		Name:              "测试账户",
		ProviderCode:      "openai",
		ProtocolCode:      "openai",
		ProtocolVersion:   "v1",
		Type:              "api_key",
		ClientCompatibility: "",
	}
}

func newInputFixture(body UpstreamBody, status int, header map[string]string) (HandleUpstreamResponseInput, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	tracking := gatewaypreauth.NewTrackingWriter(recorder)
	upstreamHeader := httpHeaderOf(header)
	input := HandleUpstreamResponseInput{
		Req: gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil)),
		Downstream: StreamDownstream{Res: tracking},
		Account:        accountFixture(),
		UpstreamResponse: &GatewayUpstreamResponse{
			Status: status,
			Header: upstreamHeader,
			Body:   body,
		},
		UpstreamURL:          "https://upstream.example/v1/chat/completions",
		AuditAttemptID:       "attempt-1",
		AuditCapture:         newMockAuditCapture(),
		Settings:             gatewayruntimecache.GatewaySettings{},
		TimeoutProfile:       TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 30_000, UncommittedAttemptMaxLifetimeMs: 300_000},
		UsageContext:         usageContextFixture(),
		StartedAtMs:          1000,
		Signal:               staticSignal(),
		DownstreamCommitState: &DownstreamCommitState{},
		Driver:               NewOpenAIResponseDriver(),
	}
	return input, recorder
}

func httpHeaderOf(values map[string]string) http.Header {
	header := http.Header{}
	for key, value := range values {
		header.Set(key, value)
	}
	return header
}

func gatewayprotoParsedUsage(input, output int) gatewayproto.ParsedUsage {
	return gatewayproto.ParsedUsage{
		InputTokens:  &input,
		OutputTokens: &output,
	}
}

func gatewayprotoErrorPayload(code, message string) gatewayproto.ErrorPayload {
	return gatewayproto.ErrorPayload{Code: code, Message: message}
}

func TestHandleStreamUpstreamResponseSuccess(t *testing.T) {
	chunks := [][]byte{
		[]byte(chatDeltaChunk),
		[]byte(chatFinishChunk),
		[]byte(chatDoneChunk),
	}
	input, _ := newInputFixture(NewSliceUpstreamBody(chunks...), 200, nil)
	audit := input.AuditCapture.(*mockAuditCapture)
	usage := &mockUsageRecords{}
	effects := &mockAccountEffects{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: effects, NowMs: func() int64 { return 1000 }}

	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.AlreadyFinalized || result.RetryUpstream {
		t.Fatalf("result = %+v", result)
	}
	if result.Usage.OutputTokens == nil || *result.Usage.OutputTokens != 7 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if !result.ProtocolValidatedSuccess {
		t.Fatal("protocol validated success expected")
	}
	if len(audit.completed) != 1 {
		t.Fatalf("completed attempts = %d", len(audit.completed))
	}
	if !audit.completed[0].Success {
		t.Fatal("attempt should be success")
	}
	if len(usage.completed) != 0 {
		t.Fatalf("completed success must not record usage here (G17 finalize path), got %d", len(usage.completed))
	}
}

func TestHandleStreamUpstreamResponseMissingTerminalFinalizes(t *testing.T) {
	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(chatDeltaChunk)), 200, nil)
	audit := input.AuditCapture.(*mockAuditCapture)
	usage := &mockUsageRecords{}
	effects := &mockAccountEffects{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: effects, NowMs: func() int64 { return 1000 }}

	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.AlreadyFinalized {
		t.Fatalf("result = %+v", result)
	}
	// usage 记录触发点：失败完成尝试（G17）。
	if len(usage.completed) != 1 || usage.completed[0].Success {
		t.Fatalf("completed records = %+v", usage.completed)
	}
	// 审计触发点：completeAttempt + finalize(stream_failed)。
	if len(audit.completed) != 1 || len(audit.finalized) != 1 {
		t.Fatalf("audit calls: completed=%d finalized=%d", len(audit.completed), len(audit.finalized))
	}
	if audit.finalized[0].Outcome != "stream_failed" || audit.finalized[0].Success {
		t.Fatalf("finalize = %+v", audit.finalized[0])
	}
	// 已转发的 delta 分片保持原样；语义已提交（delta 内容已写出）时不补发
	// 失败事件（Node writePreCommitStreamFailureToClient 的 semanticCommitted 短路）。
	body := recorder.Body.String()
	if body != chatDeltaChunk {
		t.Fatalf("client body = %q", body)
	}
	if len(audit.completed) != 1 || audit.completed[0].Success {
		t.Fatalf("attempt audit = %+v", audit.completed)
	}
	if len(usage.completed) != 1 || usage.completed[0].Stream != true {
		t.Fatalf("usage records = %+v", usage.completed)
	}
	if len(effects.affinity) != 1 {
		t.Fatalf("affinity forgets = %+v", effects.affinity)
	}
}

func TestHandleStreamUpstreamResponseRetryOnPreCommitFailure(t *testing.T) {
	// 上游失败终态发生在下游提交前：不写客户端，直接换号重试。
	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"provider_error\",\"message\":\"失败\"}}}\n\n")), 200, nil)
	usage := &mockUsageRecords{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}

	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.RetryUpstream || result.RetryReason != StreamServerRetryPreCommitStreamFailure {
		t.Fatalf("result = %+v", result)
	}
	if !result.ExcludeCurrentAccount {
		t.Fatal("pre-commit failure excludes current account")
	}
	if recorder.Body.String() != "" {
		t.Fatalf("no downstream bytes expected, got %q", recorder.Body.String())
	}
}

func TestHandleNonStreamUpstreamResponseChatJSONSuccess(t *testing.T) {
	payload := `{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2}}`
	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(payload)), 200, map[string]string{"Content-Type": "application/json"})
	input.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	audit := input.AuditCapture.(*mockAuditCapture)

	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.AlreadyFinalized {
		t.Fatalf("result = %+v", result)
	}
	if result.Usage.InputTokens == nil || *result.Usage.InputTokens != 8 ||
		result.Usage.OutputTokens == nil || *result.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if !result.ProtocolValidatedSuccess {
		t.Fatal("protocol validated success expected")
	}
	if !audit.completed[0].Success {
		t.Fatal("attempt should be success")
	}
	if recorder.Body.String() != payload {
		t.Fatalf("passthrough body = %q", recorder.Body.String())
	}
}

func TestHandleNonStreamUpstreamResponseProtocolFailureRenders502(t *testing.T) {
	// 2xx + 违反 chat 协议的 JSON → 502 + upstream_failed 收尾。
	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(`{"unexpected":"shape"}`)), 200, map[string]string{"Content-Type": "application/json"})
	audit := input.AuditCapture.(*mockAuditCapture)
	usage := &mockUsageRecords{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}

	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.AlreadyFinalized || result.ErrorCode != "upstream_protocol_error" {
		t.Fatalf("result = %+v", result)
	}
	if recorder.Code != 502 {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "上游 Chat JSON 响应结构无效") {
		t.Fatalf("client body = %q", recorder.Body.String())
	}
	if len(audit.finalized) != 1 || audit.finalized[0].Outcome != "upstream_failed" || audit.finalized[0].StatusCode != 502 {
		t.Fatalf("finalize = %+v", audit.finalized)
	}
	if len(usage.completed) != 1 || usage.completed[0].ErrorCode != "upstream_protocol_error" {
		t.Fatalf("usage records = %+v", usage.completed)
	}
}

func TestFinalizeHandledUpstreamResponseRecordsUsageAndAudit(t *testing.T) {
	input, _ := newInputFixture(nil, 200, nil)
	input.UpstreamResponse.Body = nil
	audit := input.AuditCapture.(*mockAuditCapture)
	usage := &mockUsageRecords{}
	input.Deps = &FinalizationDeps{UsageRecords: usage}

	result := UpstreamResponseHandlingResult{
		Usage:                    gatewayprotoParsedUsage(3, 4),
		ProtocolValidatedSuccess: true,
	}
	FinalizeHandledUpstreamResponse(input, result)
	if len(usage.completed) != 1 {
		t.Fatalf("usage records = %d", len(usage.completed))
	}
	record := usage.completed[0]
	if !record.Success || !record.ProtocolValidatedSuccess {
		t.Fatalf("record = %+v", record)
	}
	if record.Usage.InputTokens == nil || *record.Usage.InputTokens != 3 || record.Usage.OutputTokens == nil || *record.Usage.OutputTokens != 4 {
		t.Fatalf("usage = %+v", record.Usage)
	}
	if len(audit.finalized) != 1 {
		t.Fatalf("finalize calls = %d", len(audit.finalized))
	}
	final := audit.finalized[0]
	if final.Outcome != gatewaypreauth.AuditOutcomeSuccess || !final.Success || final.ResponsePartType != "gateway_response" {
		t.Fatalf("finalize = %+v", final)
	}
}

func TestFinalizeHandledUpstreamResponseUpstreamProtocolFailure502(t *testing.T) {
	input, recorder := newInputFixture(nil, 200, nil)
	input.UpstreamResponse.Body = nil
	audit := input.AuditCapture.(*mockAuditCapture)
	usage := &mockUsageRecords{}
	input.Deps = &FinalizationDeps{UsageRecords: usage}

	result := UpstreamResponseHandlingResult{
		ErrorPayload: gatewayprotoErrorPayload("upstream_protocol_failure", "上游响应违反请求协议终态"),
	}
	FinalizeHandledUpstreamResponse(input, result)
	if recorder.Code != 502 {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "上游响应违反请求协议终态") {
		t.Fatalf("client body = %q", recorder.Body.String())
	}
	if audit.finalized[0].Outcome != "upstream_failed" || audit.finalized[0].ResponsePartType != "gateway_error" {
		t.Fatalf("finalize = %+v", audit.finalized[0])
	}
	if usage.completed[0].FailureAttribution != "opaque_upstream" {
		t.Fatalf("attribution = %q", usage.completed[0].FailureAttribution)
	}
}

func TestFinalizeNonStreamResponseAfterSSEHeartbeat(t *testing.T) {
	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(`{"choices":[]}`)), 200, nil)
	input.ClientStrategy = &ClientStrategyView{DownstreamProtocol: "responses_sse"}
	commit := &DownstreamCommitState{}
	commit.MarkTransportCommitted(0)
	input.DownstreamCommitState = commit
	audit := input.AuditCapture.(*mockAuditCapture)
	usage := &mockUsageRecords{}
	input.Deps = &FinalizationDeps{UsageRecords: usage}

	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.AlreadyFinalized {
		t.Fatalf("result = %+v", result)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "response.failed") || !strings.Contains(body, "请重试") {
		t.Fatalf("client body = %q", body)
	}
	if len(audit.finalized) != 1 || audit.finalized[0].ErrorCode != "downstream_transport_conflict" {
		t.Fatalf("finalize = %+v", audit.finalized)
	}
}

func TestEmptyUpstreamStreamBodyProtocolFailure(t *testing.T) {
	// chat 请求的上游 204 空响应：缺少协议终态 → upstream_protocol_failure。
	input, _ := newInputFixture(nil, 204, nil)
	audit := input.AuditCapture.(*mockAuditCapture)

	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ErrorPayload.Code != "upstream_protocol_failure" {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.ErrorPayload.Message, "上游返回空响应") {
		t.Fatalf("message = %q", result.ErrorPayload.Message)
	}
	if audit.completed[0].ErrorCode != "upstream_protocol_failure" {
		t.Fatalf("audit = %+v", audit.completed[0])
	}
}

func TestEmptyUpstreamBodyDeleteInteractionsSucceeds(t *testing.T) {
	input, recorder := newInputFixture(nil, 204, nil)
	input.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("DELETE", "/v1beta/interactions/act_1", nil))
	input.Driver = NewGeminiResponseDriver()
	commit := &DownstreamCommitState{}

	input.DownstreamCommitState = commit
	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ErrorPayload.Code != "" {
		t.Fatalf("result = %+v", result)
	}
	if !commit.TransportCommitted || !commit.SemanticCommitted {
		t.Fatalf("commit = %+v", commit)
	}
	if recorder.Body.String() != "" {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}
