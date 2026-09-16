package gatewayproto

// w11b 波次：ErrorPayload.HasEvidence、StreamInspection 语义面、Token 投影、
// 响应协议驱动解析与未知请求 shape 的端点模式分支。

import (
	"strings"
	"testing"
)

func TestW11BErrorPayloadHasEvidence(t *testing.T) {
	if (ErrorPayload{}).HasEvidence() {
		t.Fatal("空载荷无证据")
	}
	if !(ErrorPayload{Code: "x"}).HasEvidence() {
		t.Fatal("code 即证据")
	}
	if !(ErrorPayload{Type: "x"}).HasEvidence() {
		t.Fatal("type 即证据")
	}
	if !(ErrorPayload{Message: "x"}).HasEvidence() {
		t.Fatal("message 即证据")
	}
}

func TestW11BStreamInspectionSemantics(t *testing.T) {
	inspection := StreamInspection{}
	if inspection.ProtocolComplete() {
		t.Fatal("无终止事件不完整")
	}
	if inspection.SemanticSuccess() {
		t.Fatal("无终止事件非成功")
	}
	inspection.TerminalReceived = true
	if !inspection.ProtocolComplete() || !inspection.SemanticSuccess() {
		t.Fatal("终止且无失败即成功")
	}
	inspection.FailedReceived = true
	if inspection.SemanticSuccess() {
		t.Fatal("失败终态非成功")
	}
}

func TestW11BTokenProjection(t *testing.T) {
	if Token(nil) != 0 {
		t.Fatal("nil → 0")
	}
	five := 5
	if Token(&five) != 5 {
		t.Fatal("值透传")
	}
	if *IntToken(7) != 7 {
		t.Fatal("装箱")
	}
}

func TestW11BRegistryResponseProtocolAndEndpointMode(t *testing.T) {
	registry := newTestRegistry()
	if _, err := registry.DriverForResponseProtocol(""); err == nil || !strings.Contains(err.Error(), "missing_response_protocol") {
		t.Fatalf("空协议错误 = %v", err)
	}
	// 未知请求 shape：无驱动命中。
	if _, ok := registry.EndpointModeForRequest(RequestShape{Method: "POST", Path: "/v1/messages", OriginalPathAndQuery: "/v1/messages"}); ok {
		t.Fatal("未知路径无端点模式")
	}
	// 已知路径返回驱动端点模式。
	if mode, ok := registry.EndpointModeForRequest(RequestShape{Method: "POST", Path: "/v1/chat/completions", OriginalPathAndQuery: "/v1/chat/completions"}); !ok || mode == "" {
		t.Fatalf("mode = %q ok = %v", mode, ok)
	}
}
