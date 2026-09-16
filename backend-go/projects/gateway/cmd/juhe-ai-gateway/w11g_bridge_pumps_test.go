package main

// w11g 覆盖补充：bridgeStreamPump 各流式方向（Anthropic/Gemini/Responses 客户端
// × Chat/Anthropic/Gemini 上游）的转换管道与收尾语义。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func w11gMappingAccount(source, upstream string) gatewaydispatch.AccountCandidate {
	enabled := true
	return gatewaydispatch.AccountCandidate{
		ID:           "acc_w11g_bridge",
		ProviderCode: "hybrid",
		ProtocolCode: "openai",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "client-model",
			SourceEndpointFamily:   source,
			UpstreamModel:          "w11g-upstream-model",
			UpstreamEndpointFamily: upstream,
			Enabled:                enabled,
		}},
	}
}

func w11gBridgeInput(t *testing.T, source string, response *gatewaydispatch.GatewayUpstreamResponse) gatewaydispatch.UpstreamResponseTransformInput {
	t.Helper()
	body := `{"model":"client-model","stream":true,"input":"hi","messages":[{"role":"user","content":"hi"}]}`
	path := "/v1/responses"
	if source == "messages" {
		path = "/v1/messages"
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: mustJSONMap(t, body)}
	return gatewaydispatch.UpstreamResponseTransformInput{
		Req:      req,
		Account:  w11gMappingAccount(source, "chat_completions"),
		Response: response,
	}
}

const w11gChatUpstreamSSE = "data: {\"id\":\"cmpl-w11g\",\"model\":\"m\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"He\"}}]}\n\n" +
	"data: {\"choices\":[{\"delta\":{\"content\":\"y\"}}]}\n\n" +
	"data: [DONE]\n\n"

func w11gTransform(t *testing.T, input gatewaydispatch.UpstreamResponseTransformInput) *gatewaydispatch.GatewayUpstreamResponse {
	t.Helper()
	transformer := newChainBridgeResponseTransformer()
	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	return got
}

func TestW11GBridgePumpChatUpstreamToAnthropicClient(t *testing.T) {
	input := w11gBridgeInput(t, "messages", newBridgeUpstreamResponse("text/event-stream", io.NopCloser(strings.NewReader(w11gChatUpstreamSSE))))
	got := w11gTransform(t, input)
	if ct := got.Header.Get("Content-Type"); !strings.Contains(strings.ToLower(ct), "event-stream") {
		t.Fatalf("content type = %q", ct)
	}
	payload, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "data:") || !strings.Contains(string(payload), "message") {
		t.Fatalf("anthropic sse = %q", payload)
	}
}

func TestW11GBridgePumpChatUpstreamToGeminiClient(t *testing.T) {
	input := w11gBridgeInput(t, "generate_content", newBridgeUpstreamResponse("text/event-stream", io.NopCloser(strings.NewReader(w11gChatUpstreamSSE))))
	got := w11gTransform(t, input)
	payload, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "data:") {
		t.Fatalf("gemini sse = %q", payload)
	}
}

func TestW11GBridgePumpChatUpstreamToResponsesClient(t *testing.T) {
	input := w11gBridgeInput(t, "responses", newBridgeUpstreamResponse("text/event-stream", io.NopCloser(strings.NewReader(w11gChatUpstreamSSE))))
	got := w11gTransform(t, input)
	payload, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "data:") {
		t.Fatalf("responses sse = %q", payload)
	}
}

const w11gAnthropicUpstreamSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_w11g\",\"model\":\"m\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"He\"}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func w11gAnthropicInput(t *testing.T, source string) gatewaydispatch.UpstreamResponseTransformInput {
	t.Helper()
	input := w11gBridgeInput(t, source, newBridgeUpstreamResponse("text/event-stream", io.NopCloser(strings.NewReader(w11gAnthropicUpstreamSSE))))
	input.Account = w11gMappingAccount(source, "messages")
	return input
}

func TestW11GBridgePumpAnthropicUpstreamToGeminiClient(t *testing.T) {
	got := w11gTransform(t, w11gAnthropicInput(t, "generate_content"))
	payload, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "data:") {
		t.Fatalf("gemini sse = %q", payload)
	}
}

func TestW11GBridgePumpAnthropicUpstreamToResponsesClient(t *testing.T) {
	got := w11gTransform(t, w11gAnthropicInput(t, "responses"))
	payload, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "data:") {
		t.Fatalf("responses sse = %q", payload)
	}
}

func TestW11GBridgePumpAnthropicUpstreamToChatClient(t *testing.T) {
	got := w11gTransform(t, w11gAnthropicInput(t, "chat_completions"))
	payload, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "data:") {
		t.Fatalf("chat sse = %q", payload)
	}
}

const w11gGeminiUpstreamSSE = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"He\"}]}}],\"modelVersion\":\"m\"}\n\n" +
	"data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"modelVersion\":\"m\"}\n\n" +
	"data: [DONE]\n\n"

func w11gGeminiInput(t *testing.T, source, upstream string) gatewaydispatch.UpstreamResponseTransformInput {
	t.Helper()
	input := w11gBridgeInput(t, source, newBridgeUpstreamResponse("text/event-stream", io.NopCloser(strings.NewReader(w11gGeminiUpstreamSSE))))
	input.Account = w11gMappingAccount(source, upstream)
	return input
}

func TestW11GBridgePumpGeminiUpstreamToChatClient(t *testing.T) {
	got := w11gTransform(t, w11gGeminiInput(t, "chat_completions", "generate_content"))
	payload, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "data:") {
		t.Fatalf("chat sse = %q", payload)
	}
}

func TestW11GBridgePumpGeminiUpstreamToAnthropicClient(t *testing.T) {
	got := w11gTransform(t, w11gGeminiInput(t, "messages", "stream_generate_content"))
	payload, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "data:") {
		t.Fatalf("anthropic sse = %q", payload)
	}
}

func TestW11GBridgePumpGeminiUpstreamToResponsesClient(t *testing.T) {
	got := w11gTransform(t, w11gGeminiInput(t, "responses", "generate_content"))
	payload, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "data:") {
		t.Fatalf("responses sse = %q", payload)
	}
}

func TestW11GBridgeNonStreamDirectionsWithEmptyBody(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	// 空 JSON 上游体：各非流式方向走 buffer 转换的空输入分支。
	for _, tc := range []struct{ source, upstream string }{
		{"messages", "chat_completions"},
		{"generate_content", "chat_completions"},
		{"responses", "chat_completions"},
		{"generate_content", "messages"},
		{"responses", "messages"},
		{"chat_completions", "messages"},
		{"chat_completions", "generate_content"},
		{"messages", "generate_content"},
		{"responses", "generate_content"},
	} {
		input := w11gBridgeInput(t, tc.source, newBridgeUpstreamResponse("application/json", io.NopCloser(strings.NewReader("{}"))))
		input.Account = w11gMappingAccount(tc.source, tc.upstream)
		// 非流式请求。
		body := `{"model":"client-model","stream":false,"input":"hi"}`
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req := gatewaypreauth.NewGatewayRequest(request)
		req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: mustJSONMap(t, body)}
		input.Req = req
		got, err := transformer.TransformUpstreamResponseForAccount(input)
		if err != nil {
			t.Fatalf("%s->%s: %v", tc.source, tc.upstream, err)
		}
		if got == nil {
			t.Fatalf("%s->%s: nil response", tc.source, tc.upstream)
		}
		payload, readErr := io.ReadAll(got.Body)
		if readErr != nil {
			t.Fatalf("%s->%s read: %v", tc.source, tc.upstream, readErr)
		}
		if len(payload) == 0 {
			t.Fatalf("%s->%s: empty transformed body", tc.source, tc.upstream)
		}
	}
}
