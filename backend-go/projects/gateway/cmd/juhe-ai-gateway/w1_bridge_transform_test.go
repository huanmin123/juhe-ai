package main

// w1: chain_bridge_response.go 转换器直测。buffered 三向（chat/anthropic/
// gemini native）流式与 JSON 双形态 + bridgeStreamPump 全部方向臂。

import (
	"bytes"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/openaicompat/openaicompatbridge"
)

const w1ChatSSE = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
const w1AnthropicSSE = "event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
const w1GeminiJSON = `{"candidates":[{"content":{"parts":[{"text":"你好"}],"role":"model"}}],"modelVersion":"gemini-x"}`
const w1GeminiSSE = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\n"
const w1ChatJSON = `{"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"model":"m"}`
const w1AnthropicJSON = `{"id":"msg_1","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn"}`

// w1BridgeInput 构造带手工挂载 JSON body 的转换输入。
func w1BridgeInput(t *testing.T) gatewaydispatch.UpstreamResponseTransformInput {
	t.Helper()
	body := `{"model":"client-model","stream":true}`
	request := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: mustJSONMap(t, body)}
	return gatewaydispatch.UpstreamResponseTransformInput{Req: req}
}

func TestW1TransformToChatClientArms(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	input := w1BridgeInput(t)
	// chat 上游 → anthropic 客户端（入 = chat 形态）。
	out := transformer.transformToChatClient(input, openaicompatbridge.FamilyAnthropicMessages, []byte(w1ChatSSE), "m", true)
	if len(out) == 0 {
		t.Fatalf("anthropic 流式 = %q", string(out))
	}
	out = transformer.transformToChatClient(input, openaicompatbridge.FamilyAnthropicMessages, []byte(w1ChatJSON), "m", false)
	if len(out) == 0 || !bytes.Contains(out, []byte(`"content"`)) {
		t.Fatalf("anthropic 缓冲 = %q", string(out))
	}
	// chat 上游 → gemini 客户端（入 = chat JSON）。
	out = transformer.transformToChatClient(input, openaicompatbridge.FamilyGeminiGenerateContent, []byte(w1ChatJSON), "m", false)
	if !strings.Contains(string(out), `"candidates"`) || !strings.Contains(string(out), "hi") {
		t.Fatalf("gemini 缓冲 = %q", string(out))
	}
	out = transformer.transformToChatClient(input, openaicompatbridge.FamilyGeminiStreamGenerate, []byte(w1ChatSSE), "m", true)
	if len(out) == 0 {
		t.Fatal("gemini 流式输出为空")
	}
	// responses 上游 → chat SSE 回转。
	out = transformer.transformToChatClient(input, openaicompatbridge.FamilyResponses, []byte(w1ChatSSE), "m", true)
	if len(out) == 0 {
		t.Fatal("responses 回转输出为空")
	}
}

func TestW1TransformToAnthropicClientArms(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	input := w1BridgeInput(t)
	// anthropic 上游 → gemini 客户端（入 = anthropic JSON）。
	out := transformer.transformToAnthropicClient(input, openaicompatbridge.FamilyGeminiGenerateContent, []byte(w1AnthropicJSON), "m", false)
	if !strings.Contains(string(out), `"candidates"`) {
		t.Fatalf("anthropic→gemini 缓冲 = %q", string(out))
	}
	out = transformer.transformToAnthropicClient(input, openaicompatbridge.FamilyGeminiStreamGenerate, []byte(w1AnthropicSSE), "m", true)
	if len(out) == 0 {
		t.Fatal("anthropic→gemini 流式为空")
	}
	// 非法 JSON：错误契约体。
	out = transformer.transformToAnthropicClient(input, openaicompatbridge.FamilyGeminiGenerateContent, []byte("{bad"), "m", false)
	if !strings.Contains(string(out), "upstream_anthropic_messages_invalid_json") {
		t.Fatalf("非法 JSON = %q", string(out))
	}
	// anthropic 上游 → chat 客户端。
	out = transformer.transformToAnthropicClient(input, openaicompatbridge.FamilyChatCompletions, []byte(w1AnthropicSSE), "m", true)
	if len(out) == 0 {
		t.Fatal("anthropic→chat 流式为空")
	}
	out = transformer.transformToAnthropicClient(input, openaicompatbridge.FamilyChatCompletions, []byte(w1AnthropicJSON), "m", false)
	if len(out) == 0 {
		t.Fatal("anthropic→chat 缓冲为空")
	}
	// 非法 JSON：TransformAnthropicJSONToChatErrorBody。
	out = transformer.transformToAnthropicClient(input, openaicompatbridge.FamilyChatCompletions, []byte("{bad"), "m", false)
	if len(out) == 0 {
		t.Fatal("anthropic→chat 非法 JSON 错误体为空")
	}
	// anthropic 上游 → responses 客户端。
	out = transformer.transformToAnthropicClient(input, openaicompatbridge.FamilyResponses, []byte(w1AnthropicSSE), "m", true)
	if len(out) == 0 {
		t.Fatal("anthropic→responses 流式为空")
	}
	out = transformer.transformToAnthropicClient(input, openaicompatbridge.FamilyResponses, []byte(w1AnthropicJSON), "m", false)
	if len(out) == 0 {
		t.Fatal("anthropic→responses 缓冲为空")
	}
	// 未知 source：nil。
	if got := transformer.transformToAnthropicClient(input, "unknown", []byte("{}"), "m", false); got != nil {
		t.Fatal("未知 source 必须 nil")
	}
}

func TestW1TransformToGeminiNativeClientArms(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	input := w1BridgeInput(t)
	chatMapping := &gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "chat_completions"}
	// chat 协议下游：JSON 与 SSE 两形态。
	out := transformer.transformToGeminiNativeClient(input, chatMapping, []byte(w1GeminiJSON), "m", false)
	if !strings.Contains(string(out), "你好") {
		t.Fatalf("gemini→chat JSON = %q", string(out))
	}
	out = transformer.transformToGeminiNativeClient(input, chatMapping, []byte(w1GeminiJSON), "m", true)
	if len(out) == 0 {
		t.Fatal("gemini→chat SSE 为空")
	}
	// 非法 JSON：回退空对象转换。
	out = transformer.transformToGeminiNativeClient(input, chatMapping, []byte("{bad"), "m", false)
	if len(out) == 0 {
		t.Fatal("非法 JSON 转换为空")
	}
	// responses 协议下游。
	responsesMapping := &gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "responses"}
	out = transformer.transformToGeminiNativeClient(input, responsesMapping, []byte(w1GeminiJSON), "m", false)
	if len(out) == 0 {
		t.Fatal("gemini→responses JSON 为空")
	}
	out = transformer.transformToGeminiNativeClient(input, responsesMapping, []byte(w1GeminiJSON), "m", true)
	if len(out) == 0 {
		t.Fatal("gemini→responses SSE 为空")
	}
}

func TestW1BridgeStreamPumpArms(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	input := w1BridgeInput(t)
	pump := func(upstreamFamily, sourceFamily string) func(io.Reader, io.Writer) error {
		var mapping *gatewayproto.ResolvedModelMapping
		if upstreamFamily == openaicompatbridge.FamilyGeminiGenerateContent {
			mapping = &gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "chat_completions"}
		} else {
			mapping = &gatewayproto.ResolvedModelMapping{SourceEndpointFamily: sourceFamily}
		}
		return transformer.bridgeStreamPump(input, mapping, sourceFamily, upstreamFamily, "m")
	}
	// 全部方向臂均返回闭包。
	arms := []struct{ upstream, source string }{
		{openaicompatbridge.FamilyChatCompletions, openaicompatbridge.FamilyAnthropicMessages},
		{openaicompatbridge.FamilyChatCompletions, openaicompatbridge.FamilyGeminiGenerateContent},
		{openaicompatbridge.FamilyChatCompletions, openaicompatbridge.FamilyGeminiStreamGenerate},
		{openaicompatbridge.FamilyChatCompletions, openaicompatbridge.FamilyResponses},
		{openaicompatbridge.FamilyAnthropicMessages, openaicompatbridge.FamilyGeminiGenerateContent},
		{openaicompatbridge.FamilyAnthropicMessages, openaicompatbridge.FamilyGeminiStreamGenerate},
		{openaicompatbridge.FamilyAnthropicMessages, openaicompatbridge.FamilyChatCompletions},
		{openaicompatbridge.FamilyAnthropicMessages, openaicompatbridge.FamilyResponses},
		{openaicompatbridge.FamilyGeminiGenerateContent, openaicompatbridge.FamilyChatCompletions},
	}
	closures := map[string]func(io.Reader, io.Writer) error{}
	for _, arm := range arms {
		fn := pump(arm.upstream, arm.source)
		if fn == nil {
			t.Fatalf("方向 %s→%s 的 pump 缺失", arm.source, arm.upstream)
		}
		closures[arm.upstream+"|"+arm.source] = fn
	}
	// 驱动 chat→anthropic：chat SSE 进，anthropic SSE 出。
	out := &bytes.Buffer{}
	if err := closures[openaicompatbridge.FamilyChatCompletions+"|"+openaicompatbridge.FamilyAnthropicMessages](strings.NewReader(w1ChatSSE), out); err != nil {
		t.Fatalf("chat→anthropic pump: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("chat→anthropic 输出为空")
	}
	// 驱动 gemini→chat（入 = gemini SSE）。
	out.Reset()
	if err := closures[openaicompatbridge.FamilyGeminiGenerateContent+"|"+openaicompatbridge.FamilyChatCompletions](strings.NewReader(w1GeminiSSE), out); err != nil {
		t.Fatalf("gemini→chat pump: %v", err)
	}
	// 未知方向：nil。
	if got := transformer.bridgeStreamPump(input, &gatewayproto.ResolvedModelMapping{}, "unknown", "unknown", "m"); got != nil {
		t.Fatal("未知方向必须 nil")
	}
}
