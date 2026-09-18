package main

// w14a（单元层）：chain_bridge_response.go bridgeStreamPump 各白名单方向的
// 增量 pump 闭包与其 EOF finish 回调臂（正常完成 / 带 stop_reason 提前收尾 /
// 上游中断失败）。responses→chat 方向不在跨协议白名单，对应 pump 臂不可达
// （见 w14a_bridge_buffered_test.go 文件头清单）。

import (
	"io"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

// w14aPumpStream 驱动一次流式桥转换并排空管道，返回下游收到的完整输出。
func w14aPumpStream(t *testing.T, target, body string, account gatewaydispatch.AccountCandidate, upstreamSSE string) string {
	t.Helper()
	transformer := newChainBridgeResponseTransformer()
	response := newBridgeUpstreamResponse("text/event-stream", io.NopCloser(strings.NewReader(upstreamSSE)))
	input := w14aBridgeInput(t, target, body, account, response)
	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if ct := got.Header.Get("Content-Type"); !strings.Contains(strings.ToLower(ct), "event-stream") {
		t.Fatalf("流式桥必须回答 SSE, got %q", ct)
	}
	return w14aReadAll(t, got.Body)
}

const w14aAnthropicMessagesRequestBody = `{"model":"client-model","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`

// chat 上游 -> anthropic 客户端（transformAnthropicMessagesChatBridge 流式）。
func TestW14aBridgePumpChatToAnthropic(t *testing.T) {
	account := w14aBridgeMappedAccount("client-model", "messages", "upstream-chat-model", "chat_completions")
	delta := "data: " + `{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"stream"},"finish_reason":null}]}` + "\n\n"

	// 完整流：delta + finish_reason stop + [DONE] → message_stop 收尾（finish
	// 回调 Completed 臂）。
	full := w14aPumpStream(t, "/v1/messages", w14aAnthropicMessagesRequestBody, account,
		delta+
			"data: "+`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n"+
			"data: [DONE]\n\n")
	if !strings.Contains(full, `"type":"message_start"`) || !strings.Contains(full, "message_stop") || !strings.Contains(full, "stream") {
		t.Fatalf("完整 chat 流必须产出 anthropic messages SSE, got %s", full)
	}

	// 中断流：仅 content delta、无 stop_reason → EOF finish 回调失败臂
	// （upstream_stream_interrupted）。
	interrupted := w14aPumpStream(t, "/v1/messages", w14aAnthropicMessagesRequestBody, account, delta)
	if !strings.Contains(interrupted, "upstream_stream_interrupted") {
		t.Fatalf("无 stop_reason 中断必须产出失败事件, got %s", interrupted)
	}

	// 提前收尾流：finish_reason stop 已到但 [DONE]/message_stop 未到 → EOF
	// finish 回调 Complete 臂。
	earlyStop := w14aPumpStream(t, "/v1/messages", w14aAnthropicMessagesRequestBody, account,
		delta+"data: "+`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
	if !strings.Contains(earlyStop, "message_stop") || strings.Contains(earlyStop, "upstream_stream_interrupted") {
		t.Fatalf("带 stop_reason 的中断必须按已完成收尾, got %s", earlyStop)
	}
}

// chat 上游 -> gemini 客户端（transformGeminiGenerateContentChatBridge 流式）。
// :streamGenerateContent 路径的请求侧源族是 stream_generate_content。
func TestW14aBridgePumpChatToGemini(t *testing.T) {
	account := w14aBridgeMappedAccount("client-model", "stream_generate_content", "upstream-chat-model", "chat_completions")
	target := "/v1beta/models/client-model:streamGenerateContent?alt=sse"
	body := `{"contents":[{"parts":[{"text":"hi"}]}],"stream":true}`
	delta := "data: " + `{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"gem"},"finish_reason":null}]}` + "\n\n"

	full := w14aPumpStream(t, target, body, account,
		delta+"data: "+`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n"+"data: [DONE]\n\n")
	if !strings.Contains(full, `"candidates"`) || !strings.Contains(full, "gem") {
		t.Fatalf("完整 chat 流必须产出 gemini SSE, got %s", full)
	}

	// 中断流 → EOF finish 回调 CompleteGeminiChatStream 臂。
	interrupted := w14aPumpStream(t, target, body, account, delta)
	if !strings.Contains(interrupted, `"candidates"`) {
		t.Fatalf("中断 chat 流必须仍按 gemini 收尾, got %s", interrupted)
	}
}

// anthropic 上游 -> gemini 客户端（transformGeminiGenerateContentAnthropic
// MessagesBridge 流式）。
func TestW14aBridgePumpAnthropicToGemini(t *testing.T) {
	account := w14aBridgeMappedAccount("client-model", "stream_generate_content", "upstream-claude-model", "messages")
	target := "/v1beta/models/client-model:streamGenerateContent?alt=sse"
	body := `{"contents":[],"stream":true}`
	start := "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_1","model":"claude-9","usage":{"input_tokens":2,"output_tokens":1}}}` + "\n\n"
	text := "event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"claude text"}}` + "\n\n"

	full := w14aPumpStream(t, target, body, account,
		start+text+
			"event: message_delta\ndata: "+`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`+"\n\n"+
			"event: message_stop\ndata: "+`{"type":"message_stop"}`+"\n\n")
	if !strings.Contains(full, `"candidates"`) || !strings.Contains(full, "claude text") {
		t.Fatalf("完整 anthropic 流必须产出 gemini SSE, got %s", full)
	}

	// 中断流 → EOF finish 回调 CompleteGeminiStream 臂。
	interrupted := w14aPumpStream(t, target, body, account, start+text)
	if !strings.Contains(interrupted, "claude text") {
		t.Fatalf("中断 anthropic 流必须仍按 gemini 收尾, got %s", interrupted)
	}
}

// anthropic 上游 -> chat 客户端（transformOpenAIToAnthropicBridge 流式，chat
// 目标）；含 previous_response_id 解析臂（对 chat 目标无害，仅覆盖解析路径）。
func TestW14aBridgePumpAnthropicToChat(t *testing.T) {
	account := w14aBridgeMappedAccount("client-model", "chat_completions", "upstream-claude-model", "messages")
	body := `{"model":"client-model","stream":true,"previous_response_id":"resp_prev_w14a","messages":[]}`
	start := "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_1","model":"claude-9","usage":{"input_tokens":2,"output_tokens":1}}}` + "\n\n"
	text := "event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"to chat"}}` + "\n\n"

	full := w14aPumpStream(t, "/v1/chat/completions", body, account,
		start+text+
			"event: message_delta\ndata: "+`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`+"\n\n"+
			"event: message_stop\ndata: "+`{"type":"message_stop"}`+"\n\n")
	if !strings.Contains(full, `"choices"`) || !strings.Contains(full, "to chat") {
		t.Fatalf("完整 anthropic 流必须产出 chat completions SSE, got %s", full)
	}

	// 中断流 → EOF finish 回调失败臂（upstream_stream_interrupted）。
	interrupted := w14aPumpStream(t, "/v1/chat/completions", body, account, start+text)
	if !strings.Contains(interrupted, "upstream_stream_interrupted") {
		t.Fatalf("无终止事件的 anthropic 中断必须产出失败事件, got %s", interrupted)
	}
}

// anthropic 上游 -> responses 客户端（transformOpenAIToAnthropicBridge 流式，
// responses 目标走 PumpAnthropicMessagesSseToResponsesSse）。
func TestW14aBridgePumpAnthropicToResponses(t *testing.T) {
	account := w14aBridgeMappedAccount("client-model", "responses", "upstream-claude-model", "messages")
	body := `{"model":"client-model","stream":true,"input":"hi","previous_response_id":"resp_prev_w14a"}`
	sse := "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_1","model":"claude-9","usage":{"input_tokens":2,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n" +
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"to responses"}}` + "\n\n" +
		"event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\n" +
		"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"

	full := w14aPumpStream(t, "/v1/responses", body, account, sse)
	if !strings.Contains(full, `"type":"message"`) || !strings.Contains(full, "to responses") {
		t.Fatalf("anthropic 流必须回转 Responses SSE, got %s", full)
	}
}

// gemini 原生上游 -> chat 客户端（transformGeminiNativeTargetBridge 流式，chat
// 协议）：含 EOF finish 回调 CompleteGeminiNativeChatStream 臂与已收到终止
// 事件的 nil 臂。
func TestW14aBridgePumpGeminiNativeToChat(t *testing.T) {
	account := w14aBridgeMappedAccount("client-model", "chat_completions", "upstream-gemini-model", "generate_content")
	target := "/v1/chat/completions"
	body := `{"model":"client-model","stream":true,"messages":[]}`
	event := "data: " + `{"candidates":[{"content":{"parts":[{"text":"native"}]}}],"modelVersion":"upstream-gemini-model"}` + "\n\n"

	// 完整流（finishReason STOP → TerminalReceived → finish 回调 nil 臂）。
	full := w14aPumpStream(t, target, body, account,
		event+"data: "+`{"candidates":[{"finishReason":"STOP"}],"modelVersion":"upstream-gemini-model"}`+"\n\n")
	if !strings.Contains(full, `"choices"`) || !strings.Contains(full, "native") {
		t.Fatalf("gemini 原生流必须产出 chat completions SSE, got %s", full)
	}

	// 中断流（无终止事件）→ EOF finish 回调 CompleteGeminiNativeChatStream 臂。
	interrupted := w14aPumpStream(t, target, body, account, event)
	if !strings.Contains(interrupted, `"choices"`) {
		t.Fatalf("中断 gemini 原生流必须按 chat 收尾, got %s", interrupted)
	}
}

// gemini 原生上游 -> messages 客户端（同 pump 的 messages 协议出口已在既有
// 流式对照中覆盖 responses 协议；此处补 messages 协议）。
func TestW14aBridgePumpGeminiNativeToMessages(t *testing.T) {
	account := w14aBridgeMappedAccount("client-model", "messages", "upstream-gemini-model", "generate_content")
	event := "data: " + `{"candidates":[{"content":{"parts":[{"text":"for messages"}]}}],"modelVersion":"upstream-gemini-model"}` + "\n\n"

	full := w14aPumpStream(t, "/v1/messages", w14aAnthropicMessagesRequestBody, account, event+"data: [DONE]\n\n")
	if !strings.Contains(full, `"content"`) || !strings.Contains(full, "for messages") {
		t.Fatalf("gemini 原生流必须产出 anthropic messages SSE, got %s", full)
	}
}
