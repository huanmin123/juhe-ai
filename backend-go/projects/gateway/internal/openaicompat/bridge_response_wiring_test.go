package openaicompat

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// B-4 bridge response face tests: each cross-protocol pair replays a fake
// upstream (httptest for the streaming pump; buffered strings for the
// transforms) and asserts the normal / error / boundary paths (tool-call
// round-trip, truncation, usage carry, SSE event ordering).

// ---------------------------------------------------------------------------
// codex responses -> chat SSE 回转 (bridge_codex_response.go)
// ---------------------------------------------------------------------------

func TestCreateCodexBridgeToolIdentity(t *testing.T) {
	function := CreateCodexBridgeToolIdentity(CodexBridgeToolIdentityInput{
		AdapterKind: "function", IDPrefix: "openai_bridge", Index: 2, UpstreamCallId: "call_up_1", Suffix: "abc",
	})
	if function.ItemID != "fc_openai_bridge_2_abc" {
		t.Fatalf("function itemId = %q", function.ItemID)
	}
	if function.CallID != "call_up_1" {
		t.Fatalf("function callId should keep the upstream id, got %q", function.CallID)
	}
	if function.ItemType != "function_call" {
		t.Fatalf("function itemType = %q", function.ItemType)
	}
	custom := CreateCodexBridgeToolIdentity(CodexBridgeToolIdentityInput{
		AdapterKind: "custom", IDPrefix: "chat_bridge", Index: 0, Suffix: "xyz",
	})
	if custom.ItemID != "ctc_chat_bridge_0_xyz" || custom.CallID != "call_chat_bridge_0_xyz" {
		t.Fatalf("custom identity = %+v", custom)
	}
	if custom.ItemType != "custom_tool_call" {
		t.Fatalf("custom itemType = %q", custom.ItemType)
	}
}

func codexChatSseFixture() string {
	return strings.Join([]string{
		`data: {"id":"chatcmpl-1","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"你好"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-1","model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]}}]}`,
		"",
		`data: {"id":"chatcmpl-1","model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"北京\"}"}}]}}]}`,
		"",
		`data: {"id":"chatcmpl-1","model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
}

func codexBridgeToolAdapters() map[string]CodexBridgeToolAdapter {
	return map[string]CodexBridgeToolAdapter{
		"get_weather": {Kind: "function", ChatName: "get_weather", ResponsesName: "get_weather"},
	}
}

func TestTransformChatSseToResponsesSseNormalAndToolRoundTrip(t *testing.T) {
	rendered := string(TransformChatCompletionsSseBufferToResponsesSse([]byte(codexChatSseFixture()), CodexResponsesChatBridgeTransformOptions{
		Enabled:                true,
		DefaultModel:           "gpt-upstream",
		IDPrefix:               "openai_bridge",
		ToolAdaptersByChatName: codexBridgeToolAdapters(),
	}))
	for _, want := range []string{
		"event: response.created",
		"event: response.output_item.added",
		"event: response.output_text.delta",
		"\"text\":\"你好\"",
		"event: response.output_item.done",
		"\"id\":\"fc_openai_bridge_0_",
		"\"call_id\":\"call_9\"",
		"\"name\":\"get_weather\"",
		"\"arguments\":\"{\\\"city\\\":\\\"北京\\\"}\"",
		"event: response.completed",
		"\"input_tokens\":11",
		"\"output_tokens\":7",
		"\"total_tokens\":18",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered SSE missing %q:\n%s", want, rendered)
		}
	}
	// 终态顺序：completed 收尾且没有 failed。
	if strings.Contains(rendered, "response.failed") {
		t.Fatalf("unexpected response.failed:\n%s", rendered)
	}
	completedIndex := strings.Index(rendered, "event: response.completed")
	toolDoneIndex := strings.Index(rendered, "response.output_item.done")
	if completedIndex < 0 || toolDoneIndex < 0 || toolDoneIndex > completedIndex {
		t.Fatalf("completion ordering wrong (done=%d completed=%d)", toolDoneIndex, completedIndex)
	}
}

func TestTransformChatSseToResponsesSseTruncatedUpstream(t *testing.T) {
	truncated := `data: {"id":"chatcmpl-1","model":"gpt-test","choices":[{"index":0,"delta":{"content":"片段"},"finish_reason":null}]}` + "\n\n"
	rendered := string(TransformChatCompletionsSseBufferToResponsesSse([]byte(truncated), CodexResponsesChatBridgeTransformOptions{
		Enabled: true, DefaultModel: "m", IDPrefix: "chat_bridge",
	}))
	if !strings.Contains(rendered, "event: response.failed") {
		t.Fatalf("expected response.failed for truncated upstream:\n%s", rendered)
	}
	if !strings.Contains(rendered, "upstream_stream_interrupted") {
		t.Fatalf("expected upstream_stream_interrupted code:\n%s", rendered)
	}
}

func TestTransformChatSseToResponsesSseUnknownToolCallFails(t *testing.T) {
	fixture := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","function":{"name":"undeclared_tool","arguments":"{}"}}]}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	rendered := string(TransformChatCompletionsSseBufferToResponsesSse([]byte(fixture), CodexResponsesChatBridgeTransformOptions{
		Enabled: true, DefaultModel: "m", IDPrefix: "chat_bridge",
	}))
	if !strings.Contains(rendered, "codex_bridge_unknown_tool_call") {
		t.Fatalf("expected codex_bridge_unknown_tool_call:\n%s", rendered)
	}
}

func TestTransformChatSseToResponsesSseUpstreamErrorEvent(t *testing.T) {
	fixture := "event: error\ndata: {\"error\":{\"message\":\"上游炸了\",\"code\":\"upstream_boom\"}}\n\n"
	rendered := string(TransformChatCompletionsSseBufferToResponsesSse([]byte(fixture), CodexResponsesChatBridgeTransformOptions{
		Enabled: true, DefaultModel: "m", IDPrefix: "chat_bridge",
	}))
	if !strings.Contains(rendered, "event: response.failed") || !strings.Contains(rendered, "upstream_boom") {
		t.Fatalf("error event not carried into response.failed:\n%s", rendered)
	}
}

func TestTransformChatSseToResponsesJSONBuffer(t *testing.T) {
	payload := TransformChatCompletionsSseBufferToResponsesJSON([]byte(codexChatSseFixture()), CodexResponsesChatBridgeTransformOptions{
		Enabled:                true,
		DefaultModel:           "gpt-upstream",
		IDPrefix:               "openai_bridge",
		ToolAdaptersByChatName: codexBridgeToolAdapters(),
	})
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("json: %v", err)
	}
	if parsed["status"] != "completed" {
		t.Fatalf("status = %v", parsed["status"])
	}
	output, _ := parsed["output"].([]any)
	if len(output) == 0 {
		t.Fatalf("empty output: %s", payload)
	}
	usage, _ := parsed["usage"].(map[string]any)
	if usage == nil || usage["total_tokens"] != float64(18) {
		t.Fatalf("usage = %v", parsed["usage"])
	}
}

func TestCodexBridgeCompletionHandlerNotifiedOnce(t *testing.T) {
	calls := 0
	options := CodexResponsesChatBridgeTransformOptions{
		Enabled:                true,
		DefaultModel:           "m",
		IDPrefix:               "chat_bridge",
		ToolAdaptersByChatName: codexBridgeToolAdapters(),
		OnCompleted:            func(CodexBridgeCompletionPayload) { calls++ },
	}
	_ = TransformChatCompletionsSseBufferToResponsesSse([]byte(codexChatSseFixture()), options)
	if calls != 1 {
		t.Fatalf("completion handler calls = %d, want 1", calls)
	}
	// 未声明工具的回转在终态判失败（codex_bridge_unknown_tool_call）且不触发
	// 完成回调。
	failureCalls := 0
	failureOptions := CodexResponsesChatBridgeTransformOptions{
		Enabled:     true,
		DefaultModel: "m",
		IDPrefix:    "chat_bridge",
		OnCompleted: func(CodexBridgeCompletionPayload) { failureCalls++ },
	}
	rendered := string(TransformChatCompletionsSseBufferToResponsesSse([]byte(codexChatSseFixture()), failureOptions))
	if failureCalls != 0 {
		t.Fatalf("failure path should not notify completion, calls = %d", failureCalls)
	}
	_ = rendered
}

// ---------------------------------------------------------------------------
// chat -> anthropic messages (already-ported core, wiring sanity + truncation)
// ---------------------------------------------------------------------------

func TestChatSseToAnthropicMessagesStreamWiring(t *testing.T) {
	fixture := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	state := NewAnthropicFromChatStreamState("claude-test")
	out := &strings.Builder{}
	if err := PumpBridgeSseTransform(strings.NewReader(fixture), out, func(event string) []string {
		return ProcessChatCompletionsSseEventAsAnthropic(state, event)
	}, nil); err != nil {
		t.Fatalf("pump: %v", err)
	}
	rendered := out.String()
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		"\"text_delta\"",
		"\"stop_reason\":\"end_turn\"",
		"event: message_stop",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("anthropic SSE missing %q:\n%s", want, rendered)
		}
	}
}

func TestChatSseToAnthropicMessagesTruncated(t *testing.T) {
	state := NewAnthropicFromChatStreamState("claude-test")
	// 合法事件（非终态）后流被截断：finish 路径输出中断错误事件。
	rendered := strings.Join(ProcessChatCompletionsSseEventAsAnthropic(state, `data: {"choices":[{"index":0,"delta":{"content":"片段"},"finish_reason":null}]}`), "")
	rendered += strings.Join(FailAnthropicFromChatStream(state, "上游 Chat Completions SSE 在 message_stop 前中断", "upstream_stream_interrupted"), "")
	if !strings.Contains(rendered, "event: error") || !strings.Contains(rendered, "upstream_stream_interrupted") {
		t.Fatalf("truncation error missing:\n%s", rendered)
	}
	// 已 failed 的流对重复 fail 与后续事件保持幂等。
	if again := FailAnthropicFromChatStream(state, "again", "again"); len(again) != 0 {
		t.Fatalf("fail not idempotent: %v", again)
	}
}

// ---------------------------------------------------------------------------
// chat -> gemini generateContent
// ---------------------------------------------------------------------------

func TestChatJSONToGeminiGenerateContent(t *testing.T) {
	parsed := map[string]any{
		"model": "gpt-x",
		"choices": []any{map[string]any{
			"message": map[string]any{
				"content":    "答案",
				"tool_calls": []any{map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "f", "arguments": "{\"a\":1}"}}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{"prompt_tokens": 9, "completion_tokens": 4, "total_tokens": 13},
	}
	rendered := ChatCompletionJSONToGeminiGenerateContent(parsed, "fallback")
	if rendered["modelVersion"] != "gpt-x" {
		t.Fatalf("modelVersion = %v", rendered["modelVersion"])
	}
	usage := rendered["usageMetadata"].(map[string]any)
	if usage["promptTokenCount"] != float64(9) {
		t.Fatalf("usage = %v", usage)
	}
	encoded, _ := json.Marshal(rendered)
	if !strings.Contains(string(encoded), "\"functionCall\"") {
		t.Fatalf("functionCall missing: %s", encoded)
	}
}

func TestChatSseToGeminiStreamToolCallFlush(t *testing.T) {
	fixture := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"部分"}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"f","arguments":"{\"x\":1}"}}]}}]}`,
		"",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":3}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	state := NewGeminiChatStreamState("m")
	out := &strings.Builder{}
	if err := PumpBridgeSseTransform(strings.NewReader(fixture), out, func(event string) []string {
		return ProcessChatCompletionsSseEventAsGemini(state, event)
	}, nil); err != nil {
		t.Fatalf("pump: %v", err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "\"text\":\"部分\"") {
		t.Fatalf("text delta missing:\n%s", rendered)
	}
	if !strings.Contains(rendered, "\"finishReason\":\"STOP\"") {
		t.Fatalf("final finishReason missing:\n%s", rendered)
	}
	if !strings.Contains(rendered, "\"promptTokenCount\":3") {
		t.Fatalf("usage metadata missing:\n%s", rendered)
	}
}

// ---------------------------------------------------------------------------
// anthropic messages -> gemini (new core)
// ---------------------------------------------------------------------------

func TestAnthropicMessageJSONToGemini(t *testing.T) {
	message := map[string]any{
		"id":    "msg_1",
		"model": "claude-x",
		"content": []any{
			map[string]any{"type": "text", "text": "文本"},
			map[string]any{"type": "tool_use", "id": "toolu_1", "name": "f", "input": map[string]any{"a": 1}},
		},
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": 4, "output_tokens": 2},
	}
	rendered := AnthropicMessageJSONToGeminiGenerateContent(message, "fallback")
	if rendered["modelVersion"] != "claude-x" {
		t.Fatalf("modelVersion = %v", rendered["modelVersion"])
	}
	encoded, _ := json.Marshal(rendered)
	for _, want := range []string{"\"text\":\"文本\"", "\"functionCall\"", "\"name\":\"f\"", "\"finishReason\":\"STOP\""} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("missing %s in %s", want, encoded)
		}
	}
}

func TestAnthropicSseToGeminiStream(t *testing.T) {
	fixture := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"model":"claude-x","usage":{"input_tokens":7}}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"片段"}}`,
		"",
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":4}}`,
		"",
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
	state := NewAnthropicGeminiStreamState("m")
	out := &strings.Builder{}
	if err := PumpBridgeSseTransform(strings.NewReader(fixture), out, func(event string) []string {
		return state.ProcessAnthropicSseEvent(event)
	}, nil); err != nil {
		t.Fatalf("pump: %v", err)
	}
	rendered := out.String()
	for _, want := range []string{
		"\"text\":\"片段\"",
		"\"finishReason\":\"MAX_TOKENS\"",
		"\"promptTokenCount\":7",
		"\"candidatesTokenCount\":4",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q:\n%s", want, rendered)
		}
	}
}

// ---------------------------------------------------------------------------
// gemini native -> chat / responses / messages
// ---------------------------------------------------------------------------

func geminiSseFixture() string {
	return strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"你好"}]}}],"modelVersion":"gemini-x"}`,
		"",
		`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"f","args":{"a":1}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":6,"candidatesTokenCount":2,"totalTokenCount":8}}`,
		"",
	}, "\n")
}

func TestGeminiSseToChatStream(t *testing.T) {
	state := NewGeminiNativeChatStreamState("gemini-x")
	out := &strings.Builder{}
	if err := PumpBridgeSseTransform(strings.NewReader(geminiSseFixture()), out, func(event string) []string {
		return ProcessGeminiSseEventAsChat(state, event)
	}, nil); err != nil {
		t.Fatalf("pump: %v", err)
	}
	rendered := out.String()
	for _, want := range []string{
		"\"content\":\"你好\"",
		"\"tool_calls\":[",
		"\"finish_reason\":\"stop\"",
		"\"prompt_tokens\":6",
		"data: [DONE]",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q:\n%s", want, rendered)
		}
	}
}

func TestGeminiJSONToResponsesAndMessages(t *testing.T) {
	geminiBody := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"回复"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1,"totalTokenCount":6}}`)
	responsesPayload := TransformGeminiJSONBufferToDownstreamJSON(geminiBody, GeminiNativeProtocolResponses, "m")
	if !strings.Contains(string(responsesPayload), "\"object\":\"response\"") || !strings.Contains(string(responsesPayload), "\"output_text\"") {
		t.Fatalf("responses payload wrong: %s", responsesPayload)
	}
	messagesPayload := TransformGeminiJSONBufferToDownstreamJSON(geminiBody, GeminiNativeProtocolMessages, "m")
	for _, want := range []string{"\"type\":\"message\"", "\"stop_reason\":\"end_turn\"", "\"input_tokens\":5"} {
		if !strings.Contains(string(messagesPayload), want) {
			t.Fatalf("messages payload missing %s: %s", want, messagesPayload)
		}
	}
}

func TestGeminiSseToResponsesStreamDoneSequence(t *testing.T) {
	rendered := string(TransformGeminiSseBufferToDownstreamSse([]byte(geminiSseFixture()), GeminiNativeProtocolResponses, "m"))
	for _, want := range []string{
		"event: response.created",
		"event: response.output_text.delta",
		"event: response.completed",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q:\n%s", want, rendered)
		}
	}
}

// ---------------------------------------------------------------------------
// anthropic messages -> responses (new core)
// ---------------------------------------------------------------------------

func TestAnthropicJSONToResponsesJSON(t *testing.T) {
	body := []byte(`{"id":"msg_9","model":"claude-x","content":[{"type":"text","text":"回答"},{"type":"tool_use","id":"toolu_1","name":"f","input":{"k":1}}],"stop_reason":"tool_use","usage":{"input_tokens":8,"output_tokens":3}}`)
	payload := TransformAnthropicMessagesJSONBufferToResponsesJSON(body, "fallback-model", "resp_prev_1")
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("json: %v", err)
	}
	if parsed["id"] != "resp_msg_9" {
		t.Fatalf("id = %v", parsed["id"])
	}
	if parsed["previous_response_id"] != "resp_prev_1" {
		t.Fatalf("previous_response_id = %v", parsed["previous_response_id"])
	}
	if parsed["status"] != "completed" {
		t.Fatalf("status = %v", parsed["status"])
	}
	encoded := string(payload)
	for _, want := range []string{"\"type\":\"function_call\"", "\"call_id\":\"toolu_1\"", "\"input_tokens\":8", "\"total_tokens\":11"} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("missing %s in %s", want, encoded)
		}
	}
}

func TestAnthropicSseToResponsesStream(t *testing.T) {
	fixture := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-x","usage":{"input_tokens":6}}}`,
		"",
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"流式"}}`,
		"",
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		"",
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		"",
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
	rendered := string(TransformAnthropicMessagesSseBufferToResponsesSse([]byte(fixture), "m", ""))
	for _, want := range []string{
		"event: response.created",
		"event: response.output_text.delta",
		"\"delta\":\"流式\"",
		"event: response.completed",
		"\"output_tokens\":2",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q:\n%s", want, rendered)
		}
	}
}

func TestAnthropicSseToResponsesTruncated(t *testing.T) {
	fixture := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-x\"}}\n\n"
	rendered := string(TransformAnthropicMessagesSseBufferToResponsesSse([]byte(fixture), "m", ""))
	if !strings.Contains(rendered, "event: response.failed") || !strings.Contains(rendered, "upstream_stream_interrupted") {
		t.Fatalf("truncation missing:\n%s", rendered)
	}
}

// ---------------------------------------------------------------------------
// Gemini Code Assist unwrap
// ---------------------------------------------------------------------------

func TestUnwrapGeminiCodeAssistSse(t *testing.T) {
	wrapped := "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"A\"}]}}]}}\n\n" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"B\"}]}}]}}\n\n"
	unwrapped := string(UnwrapGeminiCodeAssistSseBuffer([]byte(wrapped)))
	if strings.Contains(unwrapped, "\"response\"") {
		t.Fatalf("wrapper still present: %s", unwrapped)
	}
	if strings.Count(unwrapped, "\"text\"") != 2 {
		t.Fatalf("unwrapped events wrong: %s", unwrapped)
	}
}

func TestCollectGeminiCodeAssistSse(t *testing.T) {
	wrapped := "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"A\"}]}}]}}\n\n" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"B\"}]}}]}}\n\n"
	collected := string(CollectGeminiCodeAssistSseBuffer([]byte(wrapped)))
	if !strings.Contains(collected, "\"text\":\"AB\"") {
		t.Fatalf("collected text merge wrong: %s", collected)
	}
}

func TestUnwrapGeminiCodeAssistPayloadPassthrough(t *testing.T) {
	raw := `{"candidates":[]}`
	if UnwrapGeminiCodeAssistPayload(raw) != raw {
		t.Fatalf("plain payload changed: %s", UnwrapGeminiCodeAssistPayload(raw))
	}
	broken := "{not json"
	if UnwrapGeminiCodeAssistPayload(broken) != broken {
		t.Fatalf("broken payload changed")
	}
}

// ---------------------------------------------------------------------------
// hosted tool runtime registry
// ---------------------------------------------------------------------------

func TestNormalizeOpenAIHostedToolRuntimeType(t *testing.T) {
	if got, ok := NormalizeOpenAIHostedToolRuntimeType("container"); !ok || got != OpenAIHostedToolCodeInterpreter {
		t.Fatalf("container -> %v %v", got, ok)
	}
	for _, known := range []string{"code_interpreter", "computer", "mcp", "shell", "skills", "tool_search"} {
		if _, ok := NormalizeOpenAIHostedToolRuntimeType(known); !ok {
			t.Fatalf("%s should be hosted", known)
		}
	}
	if _, ok := NormalizeOpenAIHostedToolRuntimeType("web_search"); ok {
		t.Fatal("web_search should not be hosted")
	}
}

func TestResolveOpenAIHostedToolRuntimeDecisionModes(t *testing.T) {
	modes := OpenAIHostedToolRuntimeModes{CodeInterpreter: "local_runtime", Computer: "mock", Shell: "reject"}
	decision, ok := ResolveOpenAIHostedToolRuntimeDecision("container", "responses", modes)
	if !ok || decision.ToolType != OpenAIHostedToolCodeInterpreter || decision.Mode != OpenAIHostedToolModeLocalRuntim {
		t.Fatalf("code_interpreter decision = %+v ok=%v", decision, ok)
	}
	if decision.CompatibilityDetail == "" {
		t.Fatal("compatibility detail empty")
	}
	computer, _ := ResolveOpenAIHostedToolRuntimeDecision("computer", "", modes)
	if computer.Mode != OpenAIHostedToolModeMock {
		t.Fatalf("computer mode = %v", computer.Mode)
	}
	shell, _ := ResolveOpenAIHostedToolRuntimeDecision("shell", "", modes)
	if shell.Mode != OpenAIHostedToolModeReject {
		t.Fatalf("shell mode = %v", shell.Mode)
	}
	mcp, _ := ResolveOpenAIHostedToolRuntimeDecision("mcp", "", modes)
	if mcp.Mode != OpenAIHostedToolModeGuidance {
		t.Fatalf("mcp is fixed guidance, got %v", mcp.Mode)
	}
	if _, ok := ResolveOpenAIHostedToolRuntimeDecision("web_search", "", modes); ok {
		t.Fatal("web_search must not resolve")
	}
	// 默认 guidance。
	defaultDecision, _ := ResolveOpenAIHostedToolRuntimeDecision("tool_search", "", OpenAIHostedToolRuntimeModes{})
	if defaultDecision.Mode != OpenAIHostedToolModeGuidance {
		t.Fatalf("default mode = %v", defaultDecision.Mode)
	}
}

func TestResponsesToolsToAnthropicToolsHostedDegradation(t *testing.T) {
	tools := []any{
		map[string]any{"type": "function", "name": "f", "parameters": map[string]any{"type": "object"}},
		map[string]any{"type": "web_search"},
	}
	result, degraded, err := responsesToolsToAnthropicTools(tools, OpenAIHostedToolRuntimeModes{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("function tool missing: %v", result)
	}
	if len(degraded) != 1 || degraded[0] != "web_search" {
		t.Fatalf("degraded = %v", degraded)
	}
	constraint := AppendUnsupportedHostedToolConstraintText(degraded)
	if !strings.Contains(constraint, "web_search") {
		t.Fatalf("constraint text = %q", constraint)
	}
}

func TestResponsesToolsToAnthropicToolsRejectMode(t *testing.T) {
	tools := []any{map[string]any{"type": "shell", "name": "shell"}}
	_, _, err := responsesToolsToAnthropicTools(tools, OpenAIHostedToolRuntimeModes{Shell: "reject"})
	if err == nil {
		t.Fatal("reject mode should error")
	}
	bridgeErr, ok := err.(*BridgeRequestError)
	if !ok || bridgeErr.Code != "openai_anthropic_bridge_unsupported_tool" {
		t.Fatalf("err = %v", err)
	}
	// registry 外类型（web_search）在 reject bag 下也保持降级（Node 决策表
	// 外的 fallback 是原始 type label）。
	_, degraded, err := responsesToolsToAnthropicTools([]any{map[string]any{"type": "web_search"}}, OpenAIHostedToolRuntimeModes{Shell: "reject"})
	if err != nil || len(degraded) != 1 || degraded[0] != "web_search" {
		t.Fatalf("web_search degraded = %v err=%v", degraded, err)
	}
}

// ---------------------------------------------------------------------------
// streaming pump through httptest (fake upstream SSE replay)
// ---------------------------------------------------------------------------

func TestPumpBridgeSseThroughHTTPBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, event := range []string{
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"分\"}}]}\n\n",
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"片\"}}]}\n\n",
			"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
			"data: [DONE]\n\n",
		} {
			_, _ = io.WriteString(w, event)
			flusher.Flush()
		}
	}))
	defer server.Close()
	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	state := NewGeminiChatStreamState("m")
	out := &strings.Builder{}
	if err := PumpBridgeSseTransform(resp.Body, out, func(event string) []string {
		return ProcessChatCompletionsSseEventAsGemini(state, event)
	}, nil); err != nil {
		t.Fatalf("pump: %v", err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "\"text\":\"分\"") || !strings.Contains(rendered, "\"text\":\"片\"") {
		t.Fatalf("streamed deltas missing:\n%s", rendered)
	}
	if !strings.Contains(rendered, "\"finishReason\":\"STOP\"") {
		t.Fatalf("final event missing:\n%s", rendered)
	}
}
