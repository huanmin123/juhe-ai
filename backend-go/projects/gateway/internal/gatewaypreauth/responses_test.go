package gatewaypreauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// responses.go 的表驱动测试：payload 构造、协议转换、本地化与流失败事件。

func TestGatewayErrorPayloadOf(t *testing.T) {
	payload := GatewayErrorPayloadOf("缺少访问令牌", "invalid_request_error")
	encoded, _ := json.Marshal(payload)
	assertContains(t, string(encoded), `"message":"缺少访问令牌"`, `"type":"invalid_request_error"`)
	if jsonBytes, _ := json.Marshal(payload); jsonContains(jsonBytes, `"code"`) {
		t.Fatal("未传 code 时不应输出 code 字段")
	}
	withCode := GatewayErrorPayloadOf("额度已用完，请联系管理员提升额度", "rate_limit_exceeded", "user_request_limit_exceeded")
	encodedCode, _ := json.Marshal(withCode)
	assertContains(t, string(encodedCode), `"code":"user_request_limit_exceeded"`)
}

func jsonContains(body []byte, needle string) bool {
	return len(needle) > 0 && len(body) > 0 && stringContains(string(body), needle)
}

func stringContains(haystack, needle string) bool {
	return indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestGatewayErrorPayloadForProtocol(t *testing.T) {
	cases := []struct {
		name        string
		payload     GatewayErrorPayload
		protocol    GatewayErrorProtocol
		wantType    string
		wantStatus  string
		wantErrType string
	}{
		{"anthropic 限流", GatewayErrorPayloadOf("过于频繁", "rate_limit_exceeded", "x"), GatewayErrorProtocolAnthropic, "error", "", "rate_limit_error"},
		{"anthropic 过载", GatewayErrorPayloadOf("过载", "service_unavailable", ""), GatewayErrorProtocolAnthropic, "error", "", "overloaded_error"},
		{"anthropic 默认", GatewayErrorPayloadOf("其他", "weird", ""), GatewayErrorProtocolAnthropic, "error", "", "api_error"},
		{"gemini 限流", GatewayErrorPayloadOf("过于频繁", "rate_limit_exceeded", ""), GatewayErrorProtocolGemini, "", "RESOURCE_EXHAUSTED", ""},
		{"gemini 未认证", GatewayErrorPayloadOf("令牌无效", "invalid_request_error", ""), GatewayErrorProtocolGemini, "", "UNAUTHENTICATED", ""},
		{"gemini 参数", GatewayErrorPayloadOf("参数无效", "invalid_request_error", ""), GatewayErrorProtocolGemini, "", "INVALID_ARGUMENT", ""},
		{"gemini 超时", GatewayErrorPayloadOf("超时", "other", "upstream_timeout_deadline"), GatewayErrorProtocolGemini, "", "DEADLINE_EXCEEDED", ""},
		{"gemini internal", GatewayErrorPayloadOf("其他", "other", ""), GatewayErrorProtocolGemini, "", "INTERNAL", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			converted := GatewayErrorPayloadForProtocol(tc.payload, tc.protocol)
			encoded, _ := json.Marshal(converted)
			var decoded map[string]any
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("转换结果不是 JSON: %v", err)
			}
			if tc.wantType != "" {
				if decoded["type"] != tc.wantType {
					t.Fatalf("type = %v, want %v", decoded["type"], tc.wantType)
				}
			}
			errObject := decoded["error"].(map[string]any)
			if tc.wantStatus != "" && errObject["status"] != tc.wantStatus {
				t.Fatalf("status = %v, want %v", errObject["status"], tc.wantStatus)
			}
			if tc.wantErrType != "" && errObject["type"] != tc.wantErrType {
				t.Fatalf("error.type = %v, want %v", errObject["type"], tc.wantErrType)
			}
		})
	}
}

func TestSendGatewayJSONErrorPreservesChineseCopy(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := NewTrackingWriter(recorder)
	req := openAIRequest()
	SendGatewayJSONError(writer, http.StatusUnauthorized, GatewayErrorPayloadOf("缺少访问令牌", "invalid_request_error"), SendGatewayErrorOptions{})
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", recorder.Code)
	}
	assertContains(t, recorder.Body.String(), "缺少访问令牌", "invalid_request_error")
	contentType := recorder.Header().Get("Content-Type")
	if contentType == "" {
		t.Fatal("缺少 Content-Type")
	}
	_ = req
}

func TestSendGatewayJSONErrorAnthropicProtocol(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := NewTrackingWriter(recorder)
	req := anthropicNativeRequest()
	protocol, err := GatewayProtocolClientErrorProtocolForRequest(req)
	if err != nil {
		t.Fatalf("协议识别失败: %v", err)
	}
	if protocol != GatewayErrorProtocolAnthropic {
		t.Fatalf("protocol = %v", protocol)
	}
	SendGatewayJSONError(writer, http.StatusUnauthorized, GatewayErrorPayloadOf("API Key 无效", "invalid_request_error"), SendGatewayErrorOptions{Protocol: protocol})
	assertContains(t, recorder.Body.String(), `"type":"error"`, "API Key 无效", "invalid_request_error")
}

func TestSendClientIPBlacklistResponseCopy(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	service.sendClientIPBlacklistResponse(req, writer, blacklistResponseInput{
		reason:         "滥用",
		clientIP:       "203.0.113.9",
		aggregateIPKey: "203.0.113.0/24",
	})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d", recorder.Code)
	}
	errObject := errorBody(t, recorder)
	message := errObject["message"].(string)
	want := "当前来源 IP 203.0.113.9（封禁范围：203.0.113.0/24）已被管理员封禁：滥用"
	if message != want {
		t.Fatalf("message = %q, want %q", message, want)
	}
	if errObject["client_ip"] != "203.0.113.9" {
		t.Fatalf("client_ip = %v", errObject["client_ip"])
	}
	if errObject["aggregate_ip_key"] != "203.0.113.0/24" {
		t.Fatalf("aggregate_ip_key = %v", errObject["aggregate_ip_key"])
	}
	if req.AuthFailureErrorCode != "client_ip_blacklisted" {
		t.Fatalf("audit errorCode = %q", req.AuthFailureErrorCode)
	}
}

func TestBlacklistIPMessage(t *testing.T) {
	cases := []struct {
		name     string
		clientIP string
		rangeKey string
		want     string
	}{
		{"双值不同", "1.2.3.4", "1.2.3.0/24", " IP 1.2.3.4（封禁范围：1.2.3.0/24）"},
		{"仅 IP", "1.2.3.4", "", " IP 1.2.3.4"},
		{"仅范围", "", "1.2.3.0/24", " IP 1.2.3.0/24"},
		{"同值", "1.2.3.4", "1.2.3.4", " IP 1.2.3.4"},
		{"空", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := blacklistIPMessage(tc.clientIP, tc.rangeKey); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildGatewayStreamFailureEvent(t *testing.T) {
	event := BuildGatewayStreamFailureEvent("boom")
	assertContains(t, string(event), "event: response.failed", `"code":"upstream_stream_interrupted"`, `"message":"boom"`)
	anthropic := BuildAnthropicGatewayStreamFailureEvent(GatewayErrorPayloadOf("boom", "service_unavailable", "x"))
	assertContains(t, string(anthropic), "event: error", `"type":"overloaded_error"`)
	gemini := BuildGeminiGatewayStreamFailureEvent(GatewayErrorPayloadOf("boom", "service_unavailable", "x"))
	assertContains(t, string(gemini), "event: error", `"status":"UNAVAILABLE"`)
	if downstream := BuildGatewayStreamFailureEventForProtocol("boom", "", GatewayErrorProtocolOpenAI, ""); downstream != nil {
		t.Fatal("responses_sse 之外的 openai 流不应产生失败事件")
	}
	withResponsesSSE := BuildGatewayStreamFailureEventForProtocol("boom", "", GatewayErrorProtocolOpenAI, DownstreamProtocolResponsesSSE)
	if withResponsesSSE == nil {
		t.Fatal("responses_sse 流应产生失败事件")
	}
}

func TestIsOpenAIStreamContentType(t *testing.T) {
	if !IsOpenAIStreamContentType("text/event-stream; charset=utf-8") {
		t.Fatal("SSE 应命中")
	}
	if IsOpenAIStreamContentType("application/json") {
		t.Fatal("JSON 不应命中")
	}
}

func TestTrackingWriterFlags(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := NewTrackingWriter(recorder)
	if writer.HeadersSent() {
		t.Fatal("初始不应已发送 header")
	}
	writer.WriteHeader(http.StatusTooManyRequests)
	writer.Write([]byte("{}"))
	if !writer.HeadersSent() || writer.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("flags = %v %d", writer.HeadersSent(), writer.StatusCode())
	}
	writer.End()
	if !writer.WritableEnded() {
		t.Fatal("End 后 writableEnded 应为 true")
	}
	writer.SetDestroyed()
}
