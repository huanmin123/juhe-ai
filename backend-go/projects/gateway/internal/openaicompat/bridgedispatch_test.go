package openaicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

// B-4 bridge conversion tests (D-149/D-157). Expectations follow the archived
// Node bridges function by function.

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}

func TestBuildOpenAIChatToAnthropicMessagesBodySystemAndSampling(t *testing.T) {
	body := map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{"role": "system", "content": "be terse"},
			map[string]any{"role": "developer", "content": "extra rule"},
			map[string]any{"role": "user", "content": "hello"},
		},
		"max_tokens":  float64(256),
		"temperature": 0.3,
		"top_p":       0.9,
		"stop":        "END",
		"user":        "u-1",
	}
	result, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{ModelOverride: "claude-3-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["model"] != "claude-3-5" {
		t.Errorf("model = %v", result["model"])
	}
	if result["max_tokens"] != int64(256) {
		t.Errorf("max_tokens = %v, want 256", result["max_tokens"])
	}
	if result["system"] != "be terse\n\nextra rule" {
		t.Errorf("system = %v", result["system"])
	}
	if result["stream"] != false {
		t.Errorf("stream = %v", result["stream"])
	}
	stop, ok := result["stop_sequences"].([]string)
	if !ok || len(stop) != 1 || stop[0] != "END" {
		t.Errorf("stop_sequences = %#v", result["stop_sequences"])
	}
	metadata, _ := result["metadata"].(map[string]any)
	if metadata == nil || metadata["user_id"] != "u-1" {
		t.Errorf("metadata = %#v", result["metadata"])
	}
	messages, _ := result["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %s", mustJSON(t, messages))
	}
	first := messages[0].(map[string]any)
	if first["role"] != "user" {
		t.Errorf("first role = %v (system must fold into system field)", first["role"])
	}
}

func TestBuildOpenAIChatToAnthropicMessagesBodyToolHistory(t *testing.T) {
	body := map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{"role": "user", "content": "weather?"},
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "get_weather",
							"arguments": `{"city":"SF"}`,
						},
					},
				},
			},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "sunny"},
		},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "get_weather",
					"description": "weather lookup",
					"parameters":  map[string]any{"type": "object"},
				},
			},
		},
		"tool_choice": "auto",
	}
	result, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	messages, _ := result["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %s", mustJSON(t, messages))
	}
	assistant := messages[1].(map[string]any)
	blocks, _ := assistant["content"].([]any)
	var toolUse map[string]any
	for _, block := range blocks {
		blockMap := block.(map[string]any)
		if blockMap["type"] == "tool_use" {
			toolUse = blockMap
		}
	}
	if toolUse == nil || toolUse["id"] != "call_1" || toolUse["name"] != "get_weather" {
		t.Fatalf("assistant blocks = %s", mustJSON(t, blocks))
	}
	input := toolUse["input"].(map[string]any)
	if input["city"] != "SF" {
		t.Errorf("tool input = %s", mustJSON(t, input))
	}
	toolMsg := messages[2].(map[string]any)
	toolBlocks := toolMsg["content"].([]any)
	toolResult := toolBlocks[0].(map[string]any)
	if toolResult["tool_use_id"] != "call_1" || toolResult["content"] != "sunny" {
		t.Errorf("tool_result = %s", mustJSON(t, toolResult))
	}
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %s", mustJSON(t, tools))
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "get_weather" || tool["input_schema"] == nil {
		t.Errorf("anthropic tool = %s", mustJSON(t, tool))
	}
	if choice, ok := result["tool_choice"].(map[string]any); !ok || choice["type"] != "auto" {
		t.Errorf("tool_choice = %#v", result["tool_choice"])
	}
}

func TestBuildOpenAIChatToAnthropicMessagesBodyOrphanToolResult(t *testing.T) {
	body := map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{"role": "tool", "tool_call_id": "call_missing", "content": "x"},
		},
	}
	_, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
	if err == nil || !strings.Contains(err.Error(), "call_missing") {
		t.Fatalf("expected orphan tool result error, got %v", err)
	}
	if _, ok := err.(*BridgeRequestError); !ok {
		t.Fatalf("error type = %T", err)
	}
}

func TestBuildOpenAIChatToAnthropicMessagesBodyReasoningThinking(t *testing.T) {
	body := map[string]any{
		"model":            "gpt-5",
		"messages":         []any{map[string]any{"role": "user", "content": "hi"}},
		"reasoning_effort": "high",
	}
	result, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	thinking := result["thinking"].(map[string]any)
	if thinking["type"] != "adaptive" {
		t.Errorf("thinking = %s", mustJSON(t, thinking))
	}
	outputConfig := result["output_config"].(map[string]any)
	if outputConfig["effort"] != "high" {
		t.Errorf("output_config = %s", mustJSON(t, outputConfig))
	}

	body["reasoning_effort"] = "bogus"
	if _, err := BuildOpenAIChatToAnthropicMessagesBody(body, BridgeRequestBodyOptions{}); err == nil {
		t.Fatal("expected reasoning effort error")
	}
}

func TestBuildOpenAIResponsesToAnthropicMessagesBody(t *testing.T) {
	body := map[string]any{
		"model":        "gpt-5",
		"instructions": "be terse",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
			map[string]any{"type": "function_call", "name": "lookup", "call_id": "c1", "arguments": `{"q":"x"}`},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": "done"},
		},
	}
	result, err := BuildOpenAIResponsesToAnthropicMessagesBody(body, BridgeRequestBodyOptions{ModelOverride: "claude-x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["system"] != "be terse" {
		t.Errorf("system = %v", result["system"])
	}
	messages, _ := result["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %s", mustJSON(t, messages))
	}
}

func TestBuildOpenAIResponsesToAnthropicMessagesBodyPreviousResponseRejected(t *testing.T) {
	body := map[string]any{
		"model":                "gpt-5",
		"previous_response_id": "resp_1",
		"input":                "hi",
	}
	if _, err := BuildOpenAIResponsesToAnthropicMessagesBody(body, BridgeRequestBodyOptions{}); err == nil {
		t.Fatal("expected previous_response_id rejection")
	}
}

func TestAnthropicMessagesToChatCompletionsBody(t *testing.T) {
	body := map[string]any{
		"model":          "claude-3-5",
		"system":         []any{map[string]any{"type": "text", "text": "sys-a"}, map[string]any{"type": "text", "text": "sys-b"}},
		"max_tokens":     512.0,
		"temperature":    0.5,
		"stop_sequences": []any{"A", "B"},
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "text", "text": "let me check"},
					map[string]any{"type": "tool_use", "id": "toolu_1", "name": "lookup", "input": map[string]any{"q": "x"}},
				},
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "found"},
				},
			},
		},
		"tools": []any{
			map[string]any{"name": "lookup", "description": "d", "input_schema": map[string]any{"type": "object"}},
		},
		"tool_choice": map[string]any{"type": "auto", "disable_parallel_tool_use": true},
	}
	result, err := BuildAnthropicMessagesToChatCompletionsBody(body, BridgeRequestBodyOptions{ModelOverride: "glm-4"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["model"] != "glm-4" || result["stream"] != false {
		t.Errorf("output = %s", mustJSON(t, result))
	}
	messages, _ := result["messages"].([]any)
	if len(messages) != 4 {
		t.Fatalf("messages = %s", mustJSON(t, result))
	}
	if messages[0].(map[string]any)["role"] != "system" || messages[0].(map[string]any)["content"] != "sys-a\nsys-b" {
		t.Errorf("system message = %s", mustJSON(t, messages[0]))
	}
	toolMessage := messages[3].(map[string]any)
	if toolMessage["role"] != "tool" || toolMessage["tool_call_id"] != "toolu_1" || toolMessage["content"] != "found" {
		t.Errorf("tool message = %s", mustJSON(t, toolMessage))
	}
	assistant := messages[2].(map[string]any)
	if assistant["content"] != "let me check" {
		t.Errorf("assistant content = %v", assistant["content"])
	}
	toolCalls, _ := assistant["tool_calls"].([]any)
	call := toolCalls[0].(map[string]any)
	if call["id"] != "toolu_1" {
		t.Errorf("tool call = %s", mustJSON(t, call))
	}
	if stop, ok := result["stop"].([]string); !ok || len(stop) != 2 || stop[0] != "A" || stop[1] != "B" {
		t.Errorf("stop = %#v (multi stop stays array)", result["stop"])
	}
	if result["parallel_tool_calls"] != false {
		t.Errorf("parallel_tool_calls = %v", result["parallel_tool_calls"])
	}
}

func TestAnthropicMessagesToChatCompletionsBodyRejectsCacheControl(t *testing.T) {
	body := map[string]any{
		"model": "claude-3-5",
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "hi", "cache_control": map[string]any{"type": "ephemeral"}},
			}},
		},
	}
	_, err := BuildAnthropicMessagesToChatCompletionsBody(body, BridgeRequestBodyOptions{})
	if err == nil || !strings.Contains(err.Error(), "cache_control") {
		t.Fatalf("expected cache_control guidance, got %v", err)
	}
}

func TestGeminiGenerateContentToChatCompletionsBody(t *testing.T) {
	body := map[string]any{
		"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": "be nice"}}},
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "draw?"}}},
			map[string]any{"role": "model", "parts": []any{
				map[string]any{"text": "calling"},
				map[string]any{"functionCall": map[string]any{"name": "draw", "args": map[string]any{"x": 1.0}}},
			}},
			map[string]any{"role": "user", "parts": []any{
				map[string]any{"functionResponse": map[string]any{"name": "draw", "response": map[string]any{"ok": true}}},
			}},
		},
		"generationConfig": map[string]any{"temperature": 0.2, "maxOutputTokens": 128.0, "responseMimeType": "application/json"},
		"tools": []any{
			map[string]any{"functionDeclarations": []any{
				map[string]any{"name": "draw", "description": "d", "parameters": map[string]any{"type": "object"}},
			}},
		},
		"toolConfig": map[string]any{"functionCallingConfig": map[string]any{"mode": "ANY", "allowedFunctionNames": []any{"draw"}}},
	}
	result, err := BuildGeminiGenerateContentToChatCompletionsBody(body, BridgeRequestBodyOptions{ModelOverride: "glm-4.6"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["model"] != "glm-4.6" || result["stream"] != false {
		t.Errorf("output = %s", mustJSON(t, result))
	}
	messages, _ := result["messages"].([]any)
	if len(messages) != 4 {
		t.Fatalf("messages = %s", mustJSON(t, result))
	}
	if messages[0].(map[string]any)["role"] != "system" {
		t.Errorf("first message = %s", mustJSON(t, messages[0]))
	}
	tool := messages[3].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] == "" {
		t.Errorf("functionResponse message = %s", mustJSON(t, tool))
	}
	if result["response_format"] == nil || result["max_tokens"] != int64(128) {
		t.Errorf("generationConfig mapping = %s", mustJSON(t, result))
	}
	if result["tool_choice"] != "draw" && result["tool_choice"] == nil {
		t.Errorf("tool_choice = %#v", result["tool_choice"])
	}
}

func TestGeminiToChatCompletionsRejectsThinkingConfig(t *testing.T) {
	body := map[string]any{
		"contents":         []any{},
		"generationConfig": map[string]any{"thinkingConfig": map[string]any{}},
	}
	_, err := BuildGeminiGenerateContentToChatCompletionsBody(body, BridgeRequestBodyOptions{})
	if err == nil {
		t.Fatal("expected thinkingConfig guidance")
	}
}

func TestCodexResponsesToChatCompletionsBody(t *testing.T) {
	body := map[string]any{
		"model": "gpt-5.3-codex",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hello"},
			map[string]any{"type": "function_call", "name": "run", "call_id": "c9", "arguments": `{"a":1}`},
			map[string]any{"type": "function_call_output", "call_id": "c9", "output": "ok"},
		},
		"tools": []any{
			map[string]any{"type": "function", "name": "run", "description": "d", "parameters": map[string]any{"type": "object"}},
		},
		"reasoning":        map[string]any{"effort": "high"},
		"prompt_cache_key": "cache-1",
		"service_tier":     " priority ",
	}
	result, err := BuildCodexResponsesToChatCompletionsBody(body, BridgeRequestBodyOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["stream"] != true {
		t.Errorf("stream = %v (bridge pins SSE)", result["stream"])
	}
	if result["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v", result["reasoning_effort"])
	}
	if result["service_tier"] != "priority" {
		t.Errorf("service_tier = %v", result["service_tier"])
	}
	if result["prompt_cache_key"] != "cache-1" {
		t.Errorf("prompt_cache_key = %v", result["prompt_cache_key"])
	}
	messages, _ := result["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %s", mustJSON(t, messages))
	}
	toolMessage := messages[2].(map[string]any)
	if toolMessage["role"] != "tool" || toolMessage["tool_call_id"] != "c9" {
		t.Errorf("tool message = %s", mustJSON(t, toolMessage))
	}
}

func TestBuildBridgeRequestBodyFamilyDispatch(t *testing.T) {
	chatBody := map[string]any{"model": "m", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	if _, err := BuildBridgeRequestBody(FamilyChatCompletions, FamilyAnthropicMessages, chatBody, BridgeRequestBodyOptions{}); err != nil {
		t.Fatalf("chat->anthropic: %v", err)
	}
	if _, err := BuildBridgeRequestBody(FamilyAnthropicMessages, FamilyChatCompletions, chatBody, BridgeRequestBodyOptions{}); err != nil {
		t.Fatalf("anthropic->chat: %v", err)
	}
	if _, err := BuildBridgeRequestBody(FamilyChatCompletions, FamilyGeminiGenerateContent, chatBody, BridgeRequestBodyOptions{}); err != nil {
		t.Fatalf("chat->gemini: %v", err)
	}
	geminiBody := map[string]any{"model": "m", "contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}}}
	if _, err := BuildBridgeRequestBody(FamilyGeminiStreamGenerate, FamilyAnthropicMessages, geminiBody, BridgeRequestBodyOptions{DefaultModel: "m"}); err != nil {
		t.Fatalf("gemini->anthropic: %v", err)
	}
	responsesBody := map[string]any{"model": "m", "input": "hi"}
	if _, err := BuildBridgeRequestBody(FamilyResponses, FamilyGeminiStreamGenerate, responsesBody, BridgeRequestBodyOptions{ModelOverride: "gemini-m"}); err != nil {
		t.Fatalf("responses->gemini: %v", err)
	}
	anthropicBody := map[string]any{"model": "m", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	if _, err := BuildBridgeRequestBody(FamilyAnthropicMessages, FamilyGeminiGenerateContent, anthropicBody, BridgeRequestBodyOptions{ModelOverride: "gemini-m"}); err != nil {
		t.Fatalf("anthropic->gemini: %v", err)
	}
	if _, err := BuildBridgeRequestBody("gemini_generate_content", FamilyGeminiGenerateContent, geminiBody, BridgeRequestBodyOptions{}); err == nil {
		t.Fatal("gemini->gemini same-family source must surface the fallback source error")
	}
}

func TestBuildOpenAIChatToGeminiBody(t *testing.T) {
	body := map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{"role": "system", "content": "sys"},
			map[string]any{"role": "user", "content": "hi"},
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "f", "arguments": `{"a":1}`}},
				},
			},
			map[string]any{"role": "tool", "tool_call_id": "f", "content": "res"},
		},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "f", "description": "d", "parameters": map[string]any{"type": "object"}}},
		},
		"tool_choice": "auto",
	}
	result, err := BuildOpenAIChatToGeminiBody(body, BridgeRequestBodyOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	contents, _ := result["contents"].([]any)
	if len(contents) != 3 {
		t.Fatalf("contents = %s", mustJSON(t, result))
	}
	if result["systemInstruction"] == nil {
		t.Error("systemInstruction missing")
	}
	if result["tools"] == nil || result["toolConfig"] == nil {
		t.Errorf("tools/toolConfig = %s", mustJSON(t, result))
	}
}

// ---- SSE stream transforms ----

func TestProcessAnthropicEventAsChatTextAndToolUse(t *testing.T) {
	state := NewAnthropicChatStreamState("claude-3-5")
	var output []string
	emit := func(events []string) {
		output = append(output, events...)
	}
	emit(ProcessAnthropicEventAsChat(state, "event: message_start\ndata: "+`{"type":"message_start","message":{"id":"msg_1","model":"claude-3-5","usage":{"input_tokens":10,"output_tokens":1}}}`))
	emit(ProcessAnthropicEventAsChat(state, "event: content_block_start\ndata: "+`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	emit(ProcessAnthropicEventAsChat(state, "event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`))
	emit(ProcessAnthropicEventAsChat(state, "event: content_block_start\ndata: "+`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"lookup","input":{}}}`))
	emit(ProcessAnthropicEventAsChat(state, "event: content_block_delta\ndata: "+`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":1}"}}`))
	emit(ProcessAnthropicEventAsChat(state, "event: message_delta\ndata: "+`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`))
	emit(ProcessAnthropicEventAsChat(state, "event: message_stop\ndata: "+`{"type":"message_stop"}`))

	joined := strings.Join(output, "")
	if !strings.Contains(joined, `"role":"assistant"`) {
		t.Errorf("missing role chunk: %s", joined)
	}
	if !strings.Contains(joined, `"content":"Hi"`) {
		t.Errorf("missing text delta: %s", joined)
	}
	if !strings.Contains(joined, `"name":"lookup"`) || !strings.Contains(joined, `"{\"q\":1}"`) {
		t.Errorf("missing tool call chunks: %s", joined)
	}
	if !strings.Contains(joined, `"finish_reason":"tool_calls"`) {
		t.Errorf("missing finish reason: %s", joined)
	}
	if !strings.Contains(joined, "[DONE]") {
		t.Errorf("missing [DONE]: %s", joined)
	}
	if !state.Completed {
		t.Error("state not completed")
	}
}

func TestProcessChatCompletionsSseEventAsAnthropic(t *testing.T) {
	state := NewAnthropicFromChatStreamState("glm-4")
	var output []string
	output = append(output, ProcessChatCompletionsSseEventAsAnthropic(state, `data: {"id":"cmpl-1","model":"glm-4","choices":[{"delta":{"role":"assistant","content":"He"}}]}`)...)
	output = append(output, ProcessChatCompletionsSseEventAsAnthropic(state, `data: {"choices":[{"delta":{"content":"y"}}]}`)...)
	output = append(output, ProcessChatCompletionsSseEventAsAnthropic(state, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":"{\"a\":"}}]}}]}`)...)
	output = append(output, ProcessChatCompletionsSseEventAsAnthropic(state, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":4}}`)...)
	output = append(output, ProcessChatCompletionsSseEventAsAnthropic(state, "data: [DONE]")...)

	joined := strings.Join(output, "")
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		`"text_delta"`,
		`"input_json_delta"`,
		`"stop_reason":"tool_use"`,
		"event: message_stop",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %s", want, joined)
		}
	}
	if !state.Completed {
		t.Error("stream not completed")
	}
}

func TestProcessChatCompletionsSseEventAsGemini(t *testing.T) {
	state := NewGeminiChatStreamState("glm-4")
	var output []string
	output = append(output, ProcessChatCompletionsSseEventAsGemini(state, `data: {"model":"glm-4","choices":[{"delta":{"content":"Hi"}}]}`)...)
	output = append(output, ProcessChatCompletionsSseEventAsGemini(state, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)...)
	output = append(output, ProcessChatCompletionsSseEventAsGemini(state, "data: [DONE]")...)

	joined := strings.Join(output, "")
	if !strings.Contains(joined, `"text":"Hi"`) {
		t.Errorf("missing text part: %s", joined)
	}
	if !strings.Contains(joined, `"functionCall"`) || !strings.Contains(joined, `"name":"f"`) {
		t.Errorf("missing functionCall part: %s", joined)
	}
	if !strings.Contains(joined, `"finishReason":"STOP"`) {
		t.Errorf("missing final finishReason: %s", joined)
	}
	if !state.Completed {
		t.Error("stream not completed")
	}
}

func TestProcessGeminiSseEventAsChat(t *testing.T) {
	state := NewGeminiNativeChatStreamState("gemini-2.5-pro")
	var output []string
	output = append(output, ProcessGeminiSseEventAsChat(state, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}],"modelVersion":"gemini-2.5-pro"}`)...)
	output = append(output, ProcessGeminiSseEventAsChat(state, `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"f","args":{"a":1}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}}`)...)

	joined := strings.Join(output, "")
	if !strings.Contains(joined, `"content":"Hello"`) {
		t.Errorf("missing chat delta: %s", joined)
	}
	if !strings.Contains(joined, `"finish_reason":"stop"`) {
		t.Errorf("missing finish reason: %s", joined)
	}
	if !strings.Contains(joined, `"prompt_tokens":7`) {
		t.Errorf("missing usage: %s", joined)
	}
	if !state.Completed {
		t.Error("stream not completed")
	}
}

func TestChatCompletionJSONToAnthropicMessage(t *testing.T) {
	value := map[string]any{
		"id":    "cmpl-1",
		"model": "glm-4",
		"choices": []any{
			map[string]any{
				"finish_reason": "tool_calls",
				"message": map[string]any{
					"role":    "assistant",
					"content": "checking",
					"tool_calls": []any{
						map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "f", "arguments": `{"a":1}`}},
					},
				},
			},
		},
		"usage": map[string]any{"prompt_tokens": 1.0, "completion_tokens": 2.0},
	}
	message := ChatCompletionJSONToAnthropicMessage(value, "fallback")
	if message["type"] != "message" || message["role"] != "assistant" {
		t.Errorf("message envelope = %s", mustJSON(t, message))
	}
	if message["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v", message["stop_reason"])
	}
	blocks := message["content"].([]any)
	foundTool := false
	for _, block := range blocks {
		blockMap := block.(map[string]any)
		if blockMap["type"] == "tool_use" && blockMap["name"] == "f" {
			foundTool = true
		}
	}
	if !foundTool {
		t.Errorf("tool_use block missing: %s", mustJSON(t, blocks))
	}
	usage := message["usage"].(map[string]any)
	if usage["input_tokens"] != float64(1) || usage["output_tokens"] != float64(2) {
		t.Errorf("usage = %s", mustJSON(t, usage))
	}
}

func TestAnthropicMessageToChatCompletion(t *testing.T) {
	message := map[string]any{
		"id":    "msg_1",
		"model": "claude-x",
		"content": []any{
			map[string]any{"type": "text", "text": "answer"},
			map[string]any{"type": "tool_use", "id": "toolu_1", "name": "f", "input": map[string]any{"a": 1.0}},
		},
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": 4.0, "output_tokens": 6.0},
	}
	completion := AnthropicMessageToChatCompletion(message, "fallback-model")
	if completion["model"] != "claude-x" {
		t.Errorf("model = %v", completion["model"])
	}
	choice := completion["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Errorf("finish_reason = %v", choice["finish_reason"])
	}
	chatMessage := choice["message"].(map[string]any)
	if chatMessage["content"] != "answer" {
		t.Errorf("content = %v", chatMessage["content"])
	}
	toolCalls := chatMessage["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Errorf("tool_calls = %s", mustJSON(t, toolCalls))
	}
	usage := completion["usage"].(map[string]any)
	if usage["total_tokens"] != float64(10) {
		t.Errorf("usage = %s", mustJSON(t, usage))
	}
}

func TestChatCompletionJSONToGeminiGenerateContent(t *testing.T) {
	value := map[string]any{
		"model": "glm-4",
		"choices": []any{
			map[string]any{"finish_reason": "length", "message": map[string]any{"role": "assistant", "content": "hi"}},
		},
		"usage": map[string]any{"prompt_tokens": 2.0, "completion_tokens": 1.0, "total_tokens": 3.0},
	}
	result := ChatCompletionJSONToGeminiGenerateContent(value, "fallback")
	candidates := result["candidates"].([]any)
	candidate := candidates[0].(map[string]any)
	if candidate["finishReason"] != "MAX_TOKENS" {
		t.Errorf("finishReason = %v", candidate["finishReason"])
	}
	usage := result["usageMetadata"].(map[string]any)
	if usage["promptTokenCount"] != float64(2) || usage["totalTokenCount"] != float64(3) {
		t.Errorf("usageMetadata = %s", mustJSON(t, usage))
	}
}
