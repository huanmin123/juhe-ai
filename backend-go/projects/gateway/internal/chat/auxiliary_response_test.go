package chat

// 辅助请求统一（PLAN-20261012T121500000Z D4「辅助请求统一」）表驱动单测：
// 构造侧 responses（instructions+input）与 chat（system+user）两形态；消费侧
// 按 Content-Type 分流——OAuth 归一化强制流式账户的 SSE 有界聚合 vs 普通
// JSON 提取；非 2xx 转 AuxiliaryHTTPStatusError。

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBuildAuxiliaryModelRequestForms(t *testing.T) {
	t.Run("responses 形态：instructions + input 内容块", func(t *testing.T) {
		path, body := BuildAuxiliaryModelRequest(ProtocolResponses, "gpt-5", "压缩指令", "页面正文")
		if path != "/v1/responses" {
			t.Fatalf("path = %s", path)
		}
		if body["model"] != "gpt-5" || body["instructions"] != "压缩指令" {
			t.Fatalf("body = %v", body)
		}
		if stream, ok := body["stream"].(bool); !ok || stream {
			t.Fatalf("stream = %v, want false", body["stream"])
		}
		input, _ := body["input"].([]any)
		if len(input) != 1 {
			t.Fatalf("input = %v", input)
		}
		item, _ := input[0].(map[string]any)
		if item["role"] != "user" {
			t.Fatalf("role = %v", item["role"])
		}
		content, _ := item["content"].([]any)
		block, _ := content[0].(map[string]any)
		if block["type"] != "input_text" || block["text"] != "页面正文" {
			t.Fatalf("content block = %v", block)
		}
	})
	t.Run("chat 形态：system + user 消息", func(t *testing.T) {
		path, body := BuildAuxiliaryModelRequest(ProtocolChatCompletions, "deepseek-chat", "压缩指令", "页面正文")
		if path != "/v1/chat/completions" {
			t.Fatalf("path = %s", path)
		}
		if body["model"] != "deepseek-chat" {
			t.Fatalf("model = %v", body["model"])
		}
		if stream, ok := body["stream"].(bool); !ok || stream {
			t.Fatalf("stream = %v, want false", body["stream"])
		}
		messages, _ := body["messages"].([]any)
		if len(messages) != 2 {
			t.Fatalf("messages = %v", messages)
		}
		system, _ := messages[0].(map[string]any)
		user, _ := messages[1].(map[string]any)
		if system["role"] != "system" || system["content"] != "压缩指令" {
			t.Fatalf("system = %v", system)
		}
		if user["role"] != "user" || user["content"] != "页面正文" {
			t.Fatalf("user = %v", user)
		}
	})
}

func TestConsumeAuxiliaryModelTextForms(t *testing.T) {
	t.Run("nil 响应报错", func(t *testing.T) {
		_, err := ConsumeAuxiliaryModelText(nil, ProtocolResponses, AuxiliaryResponseMaxBytes)
		if err == nil || !strings.Contains(err.Error(), "chat_gateway_dispatch_missing") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("非 2xx → AuxiliaryHTTPStatusError", func(t *testing.T) {
		response := &GenerationDispatchResponse{Status: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader("{}"))}
		_, err := ConsumeAuxiliaryModelText(response, ProtocolChatCompletions, AuxiliaryResponseMaxBytes)
		statusErr, ok := err.(*AuxiliaryHTTPStatusError)
		if !ok || statusErr.Status != http.StatusBadGateway {
			t.Fatalf("err = %v, want AuxiliaryHTTPStatusError(502)", err)
		}
	})
	t.Run("SSE 响应（OAuth 强制流式）→ 有界聚合", func(t *testing.T) {
		stream := responsesSseEvent(`{"type":"response.output_text.delta","delta":"摘要"}`) +
			responsesSseEvent(`{"type":"response.completed","response":{}}`)
		header := http.Header{}
		header.Set("Content-Type", "text/event-stream; charset=utf-8")
		response := &GenerationDispatchResponse{Status: 200, Header: header, Body: io.NopCloser(strings.NewReader(stream))}
		text, err := ConsumeAuxiliaryModelText(response, ProtocolResponses, AuxiliaryResponseMaxBytes)
		if err != nil || text != "摘要" {
			t.Fatalf("text=%q err=%v", text, err)
		}
	})
	t.Run("SSE 响应 EOF 无终态报错", func(t *testing.T) {
		header := http.Header{}
		header.Set("Content-Type", "text/event-stream")
		response := &GenerationDispatchResponse{Status: 200, Header: header,
			Body: io.NopCloser(strings.NewReader(responsesSseEvent(`{"type":"response.output_text.delta","delta":"半截"}`)))}
		if _, err := ConsumeAuxiliaryModelText(response, ProtocolResponses, AuxiliaryResponseMaxBytes); err == nil {
			t.Fatal("EOF 无 completed 应报错")
		}
	})
	t.Run("JSON chat 形态提取 choices", func(t *testing.T) {
		body := `{"choices":[{"message":{"role":"assistant","content":"压缩摘要"}}]}`
		response := &GenerationDispatchResponse{Status: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(body))}
		text, err := ConsumeAuxiliaryModelText(response, ProtocolChatCompletions, AuxiliaryResponseMaxBytes)
		if err != nil || text != "压缩摘要" {
			t.Fatalf("text=%q err=%v", text, err)
		}
	})
	t.Run("JSON responses 形态提取 output_text", func(t *testing.T) {
		body := `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"响应正文"}]}]}`
		response := &GenerationDispatchResponse{Status: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(body))}
		text, err := ConsumeAuxiliaryModelText(response, ProtocolResponses, AuxiliaryResponseMaxBytes)
		if err != nil || text != "响应正文" {
			t.Fatalf("text=%q err=%v", text, err)
		}
	})
	t.Run("非法 JSON → chat_auxiliary_model_missing_response", func(t *testing.T) {
		response := &GenerationDispatchResponse{Status: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader("not-json"))}
		_, err := ConsumeAuxiliaryModelText(response, ProtocolChatCompletions, AuxiliaryResponseMaxBytes)
		if err == nil || !strings.Contains(err.Error(), "chat_auxiliary_model_missing_response") {
			t.Fatalf("err = %v", err)
		}
	})
}
