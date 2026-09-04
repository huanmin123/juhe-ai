package gatewaypreauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// error-response.ts 的表驱动测试：已知错误的分支顺序、payload 与审计 finalize。

func TestHandleKnownError_ServerDiagnosticTimeout(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	MarkGatewayRequestAbortSource(req, AbortSourceServerDiagnosticTimeout)
	audit := &fakeAuditCapture{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	handled := service.HandleGatewayRequestKnownErrorResponse(KnownErrorResponseInput{
		Req: req, Res: writer, AuditCapture: audit, Err: errors.New("boom"), Signal: ctx,
	})
	if !handled {
		t.Fatal("应处理")
	}
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d", recorder.Code)
	}
	errObject := errorBody(t, recorder)
	if errObject["message"] != "服务端账户诊断超时" || errObject["type"] != "gateway_timeout" || errObject["code"] != "server_diagnostic_timeout" {
		t.Fatalf("payload = %v", errObject)
	}
	final := audit.finals[0]
	if final.Outcome != AuditOutcomeGatewayFailed || final.ErrorPhase != "server_diagnostic" || final.ErrorCode != "server_diagnostic_timeout" {
		t.Fatalf("final = %+v", final)
	}
	if !strings.Contains(final.ResponseBody, "服务端账户诊断超时") {
		t.Fatalf("responseBody = %q", final.ResponseBody)
	}
}

func TestHandleKnownError_ServerDiagnosticCancel(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	MarkGatewayRequestAbortSource(req, AbortSourceServerDiagnosticCancel)
	audit := &fakeAuditCapture{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if !service.HandleGatewayRequestKnownErrorResponse(KnownErrorResponseInput{
		Req: req, Res: writer, AuditCapture: audit, Signal: ctx,
	}) {
		t.Fatal("应处理")
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}
	errObject := errorBody(t, recorder)
	if errObject["message"] != "服务端账户诊断已取消" || errObject["code"] != "server_diagnostic_cancelled" {
		t.Fatalf("payload = %v", errObject)
	}
}

func TestHandleKnownError_DownstreamClosed(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	audit := &fakeAuditCapture{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	handled := service.HandleGatewayRequestKnownErrorResponse(KnownErrorResponseInput{
		Req: req, Res: writer, AuditCapture: audit, Err: errors.New("请求已取消"), Signal: ctx,
	})
	if !handled {
		t.Fatal("应处理")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	final := audit.finals[0]
	if final.Outcome != "downstream_closed" || final.ErrorCode != "downstream_connection_closed" || final.ErrorMessage != downstreamConnectionClosedMessage {
		t.Fatalf("final = %+v", final)
	}
	if !writer.WritableEnded() {
		t.Fatal("应结束响应")
	}
}

func TestHandleKnownError_ValidationAndLocalProtocol(t *testing.T) {
	t.Run("GatewayRequestValidationError", func(t *testing.T) {
		service, _, _ := newTestService(t, nil)
		req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
		audit := &fakeAuditCapture{}
		validationErr := NewGatewayRequestValidationError("请求体字段无效",
			WithValidationErrorCode("invalid_gateway_request"),
			WithValidationErrorStatusCode(422),
		)
		handled := service.HandleGatewayRequestKnownErrorResponse(KnownErrorResponseInput{
			Req: req, Res: writer, AuditCapture: audit, Err: validationErr,
		})
		if !handled {
			t.Fatal("应处理")
		}
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d", recorder.Code)
		}
		errObject := errorBody(t, recorder)
		if errObject["message"] != "请求体字段无效" || errObject["type"] != "invalid_request_error" {
			t.Fatalf("payload = %v", errObject)
		}
		if audit.finals[0].ErrorCode != "invalid_gateway_request" || audit.finals[0].Outcome != AuditOutcomeGatewayFailed {
			t.Fatalf("final = %+v", audit.finals[0])
		}
	})
	t.Run("GatewayLocalProtocolResponse", func(t *testing.T) {
		service, _, _ := newTestService(t, nil)
		req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
		audit := &fakeAuditCapture{}
		localErr := NewGatewayLocalProtocolResponse(GatewayLocalProtocolResponse{
			Message: "本地响应", Code: "local_stop",
			Body:        "{\"ok\":true}",
			ContentType: "application/json",
			StatusCode:  200,
		})
		handled := service.HandleGatewayRequestKnownErrorResponse(KnownErrorResponseInput{
			Req: req, Res: writer, AuditCapture: audit, Err: localErr,
		})
		if !handled {
			t.Fatal("应处理")
		}
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
		assertContains(t, recorder.Body.String(), `"ok":true`)
		if audit.finals[0].Outcome != AuditOutcomeSuccess || audit.finals[0].ResponsePartType != AuditPartGatewayResponse {
			t.Fatalf("final = %+v", audit.finals[0])
		}
	})
}

func TestHandleKnownError_AgentGuidanceChatJSON(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	audit := &fakeAuditCapture{}
	accountScoped := false
	guidance := NewGatewayAgentGuidanceResponse(GatewayAgentGuidanceResponse{
		Message: "请改用受支持的模型", Code: "model_not_supported",
		Protocol: AgentGuidanceProtocolChatCompletions, Model: "gpt-4o",
		AccountScoped: &accountScoped,
	})
	handled := service.HandleGatewayRequestKnownErrorResponse(KnownErrorResponseInput{
		Req: req, Res: writer, AuditCapture: audit, Err: guidance,
	})
	if !handled {
		t.Fatal("应处理")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("guidance 不是 JSON: %v", err)
	}
	if body["object"] != "chat.completion" {
		t.Fatalf("object = %v", body["object"])
	}
	choices := body["choices"].([]any)
	choice := choices[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if message["content"] != "请改用受支持的模型" || choice["finish_reason"] != "stop" {
		t.Fatalf("choice = %v", choice)
	}
	final := audit.finals[0]
	if final.Outcome != AuditOutcomeSuccess || final.StatusCode != http.StatusOK || final.ErrorCode != "model_not_supported" {
		t.Fatalf("final = %+v", final)
	}
}

func TestHandleKnownError_AgentGuidanceChatSSE(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	req, recorder, writer := newTestRequest("POST", "/v1/chat/completions")
	audit := &fakeAuditCapture{}
	accountScoped := false
	guidance := NewGatewayAgentGuidanceResponse(GatewayAgentGuidanceResponse{
		Message: "流式提示", Code: "model_not_supported",
		Protocol: AgentGuidanceProtocolChatCompletions, Stream: true, Model: "gpt-4o",
		AccountScoped: &accountScoped,
	})
	if !service.HandleGatewayRequestKnownErrorResponse(KnownErrorResponseInput{
		Req: req, Res: writer, AuditCapture: audit, Err: guidance,
	}) {
		t.Fatal("应处理")
	}
	body := recorder.Body.String()
	assertContains(t, body, "data: ", `"content":"流式提示"`, "data: [DONE]\n\n")
	if audit.finals[0].ResponseBody != body {
		t.Fatal("审计响应体应与实际一致")
	}
}

func TestHandleKnownError_AgentGuidanceAccountScopedSkipped(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	req, _, writer := newTestRequest("POST", "/v1/chat/completions")
	audit := &fakeAuditCapture{}
	// 默认 accountScoped = true（Node accountScoped !== false）：不在此渲染。
	guidance := NewGatewayAgentGuidanceResponse(GatewayAgentGuidanceResponse{
		Message: "账号级提示", Code: "account_scoped", Protocol: AgentGuidanceProtocolChatCompletions,
		Model: "gpt-4o",
	})
	if service.HandleGatewayRequestKnownErrorResponse(KnownErrorResponseInput{
		Req: req, Res: writer, AuditCapture: audit, Err: guidance,
	}) {
		t.Fatal("accountScoped=true 的 guidance 不在此处理")
	}
}

func TestHandleKnownError_UnknownErrorPasses(t *testing.T) {
	service, _, _ := newTestService(t, nil)
	req, _, writer := newTestRequest("POST", "/v1/chat/completions")
	audit := &fakeAuditCapture{}
	if service.HandleGatewayRequestKnownErrorResponse(KnownErrorResponseInput{
		Req: req, Res: writer, AuditCapture: audit, Err: errors.New("未知错误"),
	}) {
		t.Fatal("未知错误不应处理")
	}
	if len(audit.finals) != 0 {
		t.Fatal("不应写审计")
	}
}

func TestGatewayDiagnosticAbortSourceFromSignal(t *testing.T) {
	if GatewayDiagnosticAbortSourceFromSignal("deadline exceeded", nil) != AbortSourceServerDiagnosticTimeout {
		t.Fatal("timeout 文案应归类为 timeout")
	}
	if GatewayDiagnosticAbortSourceFromSignal("user cancel", nil) != AbortSourceServerDiagnosticCancel {
		t.Fatal("普通文案应归类为 cancel")
	}
}
