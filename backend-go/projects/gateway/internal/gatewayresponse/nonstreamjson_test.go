package gatewayresponse

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestParseGatewayNonStreamJsonBody(t *testing.T) {
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	tests := []struct {
		name      string
		bodyText  string
		hasBody   bool
		useHeader bool
		wantState string
	}{
		{"空正文", "", false, false, NonStreamJSONStatusEmpty},
		{"空白正文", "   ", true, false, NonStreamJSONStatusEmpty},
		{"非 JSON 无内容类型", "hello", true, false, NonStreamJSONStatusNotJSON},
		{"JSON 内容类型 + 非法文本按 invalid", "hello", true, true, NonStreamJSONStatusInvalid},
		{"JSON 内容类型 + 大括号", "{bad", true, true, NonStreamJSONStatusInvalid},
		{"有效对象", `{"a":1}`, true, false, NonStreamJSONStatusValid},
		{"无内容类型但以 { 开头", `{"a":1}`, true, false, NonStreamJSONStatusValid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useHeader := header
			if !tt.useHeader {
				useHeader = nil
			}
			body := ParseGatewayNonStreamJsonBody(tt.bodyText, tt.hasBody, useHeader)
			if body.Status != tt.wantState {
				t.Fatalf("status = %q, want %q", body.Status, tt.wantState)
			}
		})
	}
}

func TestValidateBufferedJsonProtocolResponse(t *testing.T) {
	mustJSON := func(t *testing.T, text string) GatewayNonStreamJsonBody {
		t.Helper()
		body := ParseGatewayNonStreamJsonBody(text, true, nil)
		if body.Status != NonStreamJSONStatusValid {
			t.Fatalf("fixture invalid: %q", text)
		}
		return body
	}
	tests := []struct {
		name           string
		body           string
		endpointFamily string
		requestPath    string
		upstreamNotOK  bool
		wantFailure    bool
		wantCode       string
		wantMessageHas string
	}{
		{
			name: "chat 缺 choices", body: `{}`, endpointFamily: "chat_completions",
			wantFailure: true, wantCode: "upstream_protocol_error", wantMessageHas: "choices",
		},
		{
			name: "chat 有效", body: `{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`,
			endpointFamily: "chat_completions",
		},
		{
			name: "chat choice 带错误对象", body: `{"choices":[{"error":{"message":"x"}}]}`,
			endpointFamily: "chat_completions", wantFailure: true, wantCode: "upstream_protocol_error",
		},
		{
			name: "responses 失败终态带上游消息", body: `{"object":"response","id":"resp_1","output":[],"status":"failed","error":{"message":"配额不足"}}`,
			endpointFamily: "responses", wantFailure: true, wantCode: "upstream_protocol_failure", wantMessageHas: "配额不足",
		},
		{
			name: "responses 缺 output", body: `{"object":"response","id":"resp_1"}`,
			endpointFamily: "responses", wantFailure: true, wantCode: "upstream_protocol_error",
		},
		{
			name: "messages 空 content", body: `{"type":"message","content":[]}`,
			endpointFamily: "messages", wantFailure: true,
		},
		{
			name: "models 有效 name", body: `{"name":"gemini-1"}`, endpointFamily: "models",
		},
		{
			name: "generate_content 缺 candidates", body: `{}`, endpointFamily: "generate_content",
			wantFailure: true, wantMessageHas: "candidates",
		},
		{
			name: "count_tokens 缺 totalTokens", body: `{}`, endpointFamily: "count_tokens",
			wantFailure: true, wantMessageHas: "totalTokens",
		},
		{
			name: "embed_content 缺 embedding", body: `{}`, endpointFamily: "embed_content",
			wantFailure: true,
		},
		{
			name: "interactions 缺 id/name", body: `{}`, endpointFamily: "interactions",
			wantFailure: true,
		},
		{
			name: "unknown embeddings data 非数组", body: `{"data":{}}`, endpointFamily: "unknown",
			requestPath: "/v1/embeddings", wantFailure: true, wantMessageHas: "数组",
		},
		{
			name: "unknown moderations 有效", body: `{"results":[]}`, endpointFamily: "unknown", requestPath: "/v1/moderations",
		},
		{
			name: "audio 缺 text", body: `{}`, endpointFamily: "unknown", requestPath: "/v1/audio/transcriptions",
			wantFailure: true, wantMessageHas: "text",
		},
		{
			name: "管理接口 batches 缺 id/data", body: `{}`, endpointFamily: "unknown", requestPath: "/v1/batches",
			wantFailure: true, wantMessageHas: "id 或 data",
		},
		{
			name: "根 error 即失败", body: `{"error":{"message":"boom"}}`, endpointFamily: "chat_completions",
			wantFailure: true, wantMessageHas: "boom",
		},
		{
			name: "上游非 2xx 不校验", body: `{}`, endpointFamily: "chat_completions", wantFailure: false, upstreamNotOK: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := mustJSON(t, tt.body)
			failure := ValidateBufferedJsonProtocolResponse(body, !tt.upstreamNotOK, false, tt.endpointFamily, tt.requestPath)
			if tt.wantFailure != (failure != nil) {
				t.Fatalf("failure = %+v, wantFailure %v", failure, tt.wantFailure)
			}
			if failure == nil {
				return
			}
			if tt.wantCode != "" && failure.ErrorCode != tt.wantCode {
				t.Fatalf("code = %q, want %q", failure.ErrorCode, tt.wantCode)
			}
			if tt.wantMessageHas != "" && !containsString(failure.Message, tt.wantMessageHas) {
				t.Fatalf("message = %q, want contains %q", failure.Message, tt.wantMessageHas)
			}
		})
	}

	limitFailure := ValidateBufferedJsonProtocolResponse(mustJSON(t, `{}`), true, true, "chat_completions", "")
	if limitFailure == nil || limitFailure.ErrorCode != "upstream_protocol_error" || !containsString(limitFailure.Message, "上限") {
		t.Fatalf("limit exceeded failure = %+v", limitFailure)
	}
}

func containsString(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestProtocolValidatedNonStreamResponse(t *testing.T) {
	parse := func(text string) GatewayNonStreamJsonBody {
		var value any
		if err := json.Unmarshal([]byte(text), &value); err != nil {
			t.Fatal(err)
		}
		return GatewayNonStreamJsonBody{Status: NonStreamJSONStatusValid, Value: value}
	}
	if !ProtocolValidatedNonStreamResponse(parse(`{"choices":[{"message":{"content":"ok"}}]}`), 200, "chat_completions", "") {
		t.Fatal("valid chat should pass")
	}
	if ProtocolValidatedNonStreamResponse(parse(`{"choices":[{"message":{"content":"ok"}}]}`), 199, "chat_completions", "") {
		t.Fatal("non-2xx cannot validate")
	}
	if !ProtocolValidatedNonStreamResponse(parse(`{"object":"response","id":"r","output":[],"status":"completed"}`), 200, "responses", "") {
		t.Fatal("valid responses should pass")
	}
	if ProtocolValidatedNonStreamResponse(parse(`{"object":"response","id":"r","output":[],"status":"failed"}`), 200, "responses", "") {
		t.Fatal("failed responses cannot validate")
	}
	if !ProtocolValidatedNonStreamResponse(parse(`{"data":[]}`), 200, "unknown", "/v1/models") {
		t.Fatal("models path accepts data array")
	}
}

func TestGatewayGeneratedResponsesFailure(t *testing.T) {
	parse := func(text string) any {
		var value any
		if err := json.Unmarshal([]byte(text), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if !IsGatewayGeneratedResponsesFailure(parse(`{"status":"failed","metadata":{"gateway_generated_failure":true}}`), "responses") {
		t.Fatal("gateway generated failure detected")
	}
	if IsGatewayGeneratedResponsesFailure(parse(`{"status":"failed"}`), "responses") {
		t.Fatal("plain failure is not gateway generated")
	}
	if IsGatewayGeneratedResponsesFailure(parse(`{"status":"failed","metadata":{"gateway_generated_failure":true}}`), "chat_completions") {
		t.Fatal("non-responses family ignored")
	}
}

func TestIsCodexResponsesCyberPolicyFailedJSON(t *testing.T) {
	parse := func(text string) any {
		var value any
		if err := json.Unmarshal([]byte(text), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if !IsCodexResponsesCyberPolicyFailedJSON(403, "responses", "codex", parse(`{"status":"failed","error":{"code":"cyber_policy"}}`)) {
		t.Fatal("cyber policy detected")
	}
	if IsCodexResponsesCyberPolicyFailedJSON(200, "responses", "codex", parse(`{"error":{"code":"cyber_policy"}}`)) {
		t.Fatal("2xx never passthrough")
	}
	if IsCodexResponsesCyberPolicyFailedJSON(403, "responses", "generic", parse(`{"error":{"code":"cyber_policy"}}`)) {
		t.Fatal("non-codex profile ignored")
	}
	if IsCodexResponsesCyberPolicyFailedJSON(403, "chat_completions", "codex", parse(`{"error":{"code":"cyber_policy"}}`)) {
		t.Fatal("non-responses family ignored")
	}
}

func TestShouldHandleOpenAIUpstreamResponseAsStream(t *testing.T) {
	tests := []struct {
		contentType   string
		streamRequest bool
		want          bool
	}{
		{"text/event-stream; charset=utf-8", false, true},
		{"application/json", true, false},
		{"application/octet-stream", true, false},
		{"image/png", true, false},
		{"", true, true},
		{"", false, false},
	}
	for _, tt := range tests {
		if got := ShouldHandleOpenAIUpstreamResponseAsStream(tt.contentType, tt.streamRequest); got != tt.want {
			t.Fatalf("contentType %q stream=%v: got %v want %v", tt.contentType, tt.streamRequest, got, tt.want)
		}
	}
}

func TestNormalizeV1PrefixPath(t *testing.T) {
	tests := map[string]string{
		"/v1/models":   "/models",
		"/v1":          "/",
		"/models":      "/models",
		"/v1beta/models": "/v1beta/models",
		"":             "/",
	}
	for input, want := range tests {
		if got := normalizeV1PrefixPath(input); got != want {
			t.Fatalf("normalizeV1PrefixPath(%q) = %q, want %q", input, got, want)
		}
	}
	if got := normalizeV1BetaPrefixPath("/v1beta/interactions/abc"); got != "/interactions/abc" {
		t.Fatalf("normalizeV1BetaPrefixPath = %q", got)
	}
}
