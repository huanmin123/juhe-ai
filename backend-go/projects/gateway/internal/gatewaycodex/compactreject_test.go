package gatewaycodex

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// B-4 rejectUnsupportedCodexResponsesChatBridgeCompactRequest:
// unsupported_codex_bridge_compact 的判定与错误形状
// （codex-responses-chat-bridge.ts:168-193）。

func TestIsCodexResponsesChatBridgeUnsupportedCompactRequest(t *testing.T) {
	req := newTestRequest(t, "POST", "/v1/responses/compact", nil, nil)
	if !IsCodexResponsesChatBridgeUnsupportedCompactRequest(req, "codex_responses", true) {
		t.Fatal("codex_responses client + bridge enabled + compact POST should reject")
	}
	if IsCodexResponsesChatBridgeUnsupportedCompactRequest(req, "openai_standard", true) {
		t.Fatal("non-codex client should not reject")
	}
	if IsCodexResponsesChatBridgeUnsupportedCompactRequest(req, "codex_responses", false) {
		t.Fatal("bridge disabled should not reject")
	}
	responsesReq := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	if IsCodexResponsesChatBridgeUnsupportedCompactRequest(responsesReq, "codex_responses", true) {
		t.Fatal("non-compact POST should not reject")
	}
}

func TestRejectUnsupportedCodexResponsesChatBridgeCompactRequest(t *testing.T) {
	err := RejectUnsupportedCodexResponsesChatBridgeCompactRequest()
	if err == nil {
		t.Fatal("expected error")
	}
	validation, ok := err.(*gatewaypreauth.GatewayRequestValidationError)
	if !ok {
		t.Fatalf("error type = %T", err)
	}
	if validation.Code != "unsupported_codex_bridge_compact" {
		t.Fatalf("code = %q", validation.Code)
	}
	if !strings.Contains(validation.Message, "/responses/compact") {
		t.Fatalf("message = %q", validation.Message)
	}
}

func TestCodexBridgeUnsupportedCompactFailureShape(t *testing.T) {
	failure := codexBridgeUnsupportedCompactFailure()
	if failure.statusCode != 400 || failure._type != "invalid_request_error" {
		t.Fatalf("failure = %+v", failure)
	}
	if failure.code != "unsupported_codex_bridge_compact" {
		t.Fatalf("code = %q", failure.code)
	}
	// 响应负载带同码（sendCompactFailure 渲染面）。
	payload := gatewaypreauth.GatewayErrorPayloadOf(failure.message, failure._type, failure.code)
	if payload.Error.Code != "unsupported_codex_bridge_compact" {
		t.Fatalf("payload code = %q", payload.Error.Code)
	}
}
