package gatewayresponse

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// D-108 修复验证：非流式响应层对齐 finalization.ts:944-949/1061-1076 的
// 发送契约——上游 4xx/5xx 错误体必须透传、协议校验关闭的小 2xx 必须发送、
// 协议校验开启时完整缓冲后发送、超过验证窗口时拒绝透传未验证正文。

// ---- 检查门判定单元 ----

func TestIsOpenAIJSONResponseContentType(t *testing.T) {
	cases := map[string]bool{
		"application/json":                true,
		"application/json; charset=utf-8": true,
		"text/event-stream":               false,
		"audio/mpeg":                      false,
		"":                                false,
	}
	for contentType, expected := range cases {
		if got := isOpenAIJSONResponseContentType(contentType); got != expected {
			t.Fatalf("contentType = %q, got %v want %v", contentType, got, expected)
		}
	}
}

func TestShouldBufferNonStreamJSONResponse(t *testing.T) {
	// 无策略、无语义解释：不缓冲。
	plain := HandleUpstreamResponseInput{Account: accountFixture()}
	if shouldBufferNonStreamJSONResponse(plain) {
		t.Fatal("plain input must not buffer")
	}
	// codex compaction 语义解释：缓冲。
	strategyInput := HandleUpstreamResponseInput{
		Account:        accountFixture(),
		ClientStrategy: &ClientStrategyView{InterpretSemantics: true, CodexCompactionExpected: true},
	}
	if !shouldBufferNonStreamJSONResponse(strategyInput) {
		t.Fatal("codex compaction semantics must buffer")
	}
	// 语义解释但无 compaction 期望：不缓冲。
	noCompaction := HandleUpstreamResponseInput{
		Account:        accountFixture(),
		ClientStrategy: &ClientStrategyView{InterpretSemantics: true},
	}
	if shouldBufferNonStreamJSONResponse(noCompaction) {
		t.Fatal("interpretation without compaction expectation must not buffer")
	}
}

// ---- 上游 4xx/5xx 错误体透传（chat 路径）----

func TestHandleNonStreamUpstreamResponseUpstream429BodyForwarded(t *testing.T) {
	payload := `{"error":{"code":"rate_limit_exceeded","message":"请求过于频繁"}}`
	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(payload)), 429, map[string]string{"Content-Type": "application/json"})
	input.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	audit := input.AuditCapture.(*mockAuditCapture)

	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.AlreadyFinalized {
		t.Fatalf("result = %+v", result)
	}
	if recorder.Code != 429 {
		t.Fatalf("status = %d", recorder.Code)
	}
	if recorder.Body.String() != payload {
		t.Fatalf("client body = %q", recorder.Body.String())
	}
	if result.ErrorPayload.Code != "rate_limit_exceeded" {
		t.Fatalf("errorPayload = %+v", result.ErrorPayload)
	}
	if len(audit.completed) != 1 || audit.completed[0].Success || audit.completed[0].ErrorPhase != "upstream_response" {
		t.Fatalf("attempt audit = %+v", audit.completed)
	}
}

func TestHandleNonStreamUpstreamResponseUpstream503TextBodyForwarded(t *testing.T) {
	payload := "upstream unavailable, please retry"
	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(payload)), 503, map[string]string{"Content-Type": "text/plain; charset=utf-8"})
	audit := input.AuditCapture.(*mockAuditCapture)

	_, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if recorder.Code != 503 {
		t.Fatalf("status = %d", recorder.Code)
	}
	if recorder.Body.String() != payload {
		t.Fatalf("client body = %q", recorder.Body.String())
	}
	if len(audit.completed) != 1 || audit.completed[0].Success {
		t.Fatalf("attempt audit = %+v", audit.completed)
	}
}

// ---- 协议校验关闭的小 2xx（/v1/audio/speech 形状）----

func TestHandleNonStreamUpstreamResponseNonValidatedSmall2xxForwarded(t *testing.T) {
	// /v1/audio/speech 不在非流式 JSON 协议校验路径上；无检查策略时走纯透传
	// 管道，二进制正文逐字节到达客户端（修复前为空 200）。
	payload := "ID3\x04audio-bytes-payload\x00"
	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(payload)), 200, map[string]string{"Content-Type": "audio/mpeg"})
	input.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/audio/speech", nil))
	audit := input.AuditCapture.(*mockAuditCapture)

	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.AlreadyFinalized {
		t.Fatalf("result = %+v", result)
	}
	if recorder.Code != 200 {
		t.Fatalf("status = %d", recorder.Code)
	}
	if recorder.Body.String() != payload {
		t.Fatalf("client body = %q want %q", recorder.Body.String(), payload)
	}
	if !audit.completed[0].Success {
		t.Fatalf("attempt audit = %+v", audit.completed)
	}
}

// ---- 协议校验开启：完整缓冲后发送 / 超过验证窗口拒绝透传 ----

func TestHandleNonStreamUpstreamResponseValidationLimitExceededRejectsBody(t *testing.T) {
	// chat 2xx 超过 1MB 检查窗口：requireFullyBuffered 下不边转发，按
	// “拒绝透传未验证正文”以 502 协议诊断收尾。
	padding := strings.Repeat("a", 1<<20+64)
	payload := `{"choices":[{"message":{"role":"assistant","content":"` + padding + `"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(payload)), 200, map[string]string{"Content-Type": "application/json"})
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
	if !strings.Contains(recorder.Body.String(), "超过网关协议验证上限") {
		t.Fatalf("client body = %q", recorder.Body.String())
	}
	if len(audit.finalized) != 1 || audit.finalized[0].Outcome != "upstream_failed" {
		t.Fatalf("finalize = %+v", audit.finalized)
	}
	if len(usage.completed) != 1 || usage.completed[0].ErrorCode != "upstream_protocol_error" {
		t.Fatalf("usage records = %+v", usage.completed)
	}
}

// ---- 非必需缓冲超限：管道边转发完整正文 ----

func TestHandleNonStreamUpstreamResponseOversizedErrorBodyStreamsThrough(t *testing.T) {
	// 4xx 错误体超过检查窗口且协议校验关闭（非 ok 本就不校验）：冲刷缓冲后
	// 边转发，客户端收到完整错误体。
	payload := strings.Repeat("x", 1<<20+128)
	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(payload)), 429, map[string]string{"Content-Type": "text/plain"})

	_, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if recorder.Code != 429 {
		t.Fatalf("status = %d", recorder.Code)
	}
	if len(recorder.Body.String()) != len(payload) {
		t.Fatalf("client body length = %d want %d", len(recorder.Body.String()), len(payload))
	}
}
