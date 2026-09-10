package openaicompat

// Gemini 方向响应转换的补充覆盖测试：
//
//	Anthropic Messages SSE -> Gemini SSE（gemini-anthropic-messages-bridge.ts）
//	Chat Completions -> Gemini GenerateContent（JSON + SSE）
//	Gemini 上游 -> chat / responses / messages 客户端（gemini-native 桥）
//	Gemini Code Assist {response:...} 解包

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCovAnthropicJSONToGeminiGenerateContent(t *testing.T) {
	t.Run("文本与 tool_use", func(t *testing.T) {
		got := AnthropicMessageJSONToGeminiGenerateContent(map[string]any{
			"model":       "claude-x",
			"stop_reason": "max_tokens",
			"content": []any{
				map[string]any{"type": "text", "text": "答案"},
				map[string]any{"type": "tool_use", "name": "lookup", "input": map[string]any{"q": 1}},
				map[string]any{"type": "tool_use", "input": map[string]any{}},
				nil,
			},
			"usage": map[string]any{
				"input_tokens": 7, "cache_creation_input_tokens": 2, "cache_read_input_tokens": 3,
				"output_tokens": 9, "output_tokens_details": map[string]any{"thinking_tokens": 4},
			},
		}, "fallback")
		candidates := covSlice(t, got["candidates"])
		candidate := covMap(t, candidates[0])
		if covText(t, candidate["finishReason"]) != "MAX_TOKENS" {
			t.Errorf("max_tokens -> MAX_TOKENS，实际 %v", candidate["finishReason"])
		}
		content := covMap(t, candidate["content"])
		parts := covSlice(t, content["parts"])
		if len(parts) != 2 {
			t.Fatalf("期望 text + functionCall 两个 part（缺 name 的 tool_use 跳过）：%v", parts)
		}
		call := covMap(t, covMap(t, parts[1])["functionCall"])
		if covText(t, call["name"]) != "lookup" {
			t.Errorf("functionCall = %v", call)
		}
		usage := covMap(t, got["usageMetadata"])
		// input 总额 = input + cache_creation + cache_read。
		if usage["promptTokenCount"] != float64(12) {
			t.Errorf("promptTokenCount = %v，期望 12", usage["promptTokenCount"])
		}
		if usage["thoughtsTokenCount"] != float64(4) {
			t.Errorf("thoughtsTokenCount = %v", usage["thoughtsTokenCount"])
		}
		if usage["cachedContentTokenCount"] != float64(3) {
			t.Errorf("cachedContentTokenCount = %v", usage["cachedContentTokenCount"])
		}
	})
	t.Run("空内容与空 usage", func(t *testing.T) {
		got := AnthropicMessageJSONToGeminiGenerateContent(nil, "fallback")
		candidate := covMap(t, covSlice(t, got["candidates"])[0])
		parts := covSlice(t, covMap(t, candidate["content"])["parts"])
		if len(parts) != 1 || covText(t, covMap(t, parts[0])["text"]) != "" {
			t.Errorf("空内容应兜底空 text part：%v", parts)
		}
		if _, exists := got["usageMetadata"]; exists {
			t.Errorf("无 usage 不应产出 usageMetadata：%v", got)
		}
		if covText(t, got["modelVersion"]) != "fallback" {
			t.Errorf("modelVersion = %v", got["modelVersion"])
		}
	})
	t.Run("usage 快照合并", func(t *testing.T) {
		previous := map[string]any{"promptTokenCount": float64(5), "candidatesTokenCount": float64(6)}
		merged := anthropicUsageToGeminiUsage(nil, previous)
		if merged == nil {
			t.Fatal("previous 非空时应合并")
		}
		if merged["promptTokenCount"] != float64(5) || merged["candidatesTokenCount"] != float64(6) {
			t.Errorf("合并 usage = %v", merged)
		}
		if got := anthropicUsageToGeminiUsage(nil, nil); got != nil {
			t.Errorf("双空应返回 nil：%v", got)
		}
	})
}

func TestCovAnthropicSseAsGemini(t *testing.T) {
	state := NewAnthropicGeminiStreamState("gemini-pro")
	events := []string{
		"event: message_start\ndata: " + `{"type":"message_start","message":{"model":"gemini-2","usage":{"input_tokens":4}}}` + "\n\n",
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n",
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"片段"}}` + "\n\n",
		// 空 text_delta 不产出事件。
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}` + "\n\n",
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"lookup"}}` + "\n\n",
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":1}"}}` + "\n\n",
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":9,"delta":{"type":"input_json_delta","partial_json":"孤儿"}}` + "\n\n",
		"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":1}` + "\n\n",
		// 未记录的 block stop 不产出。
		"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":77}` + "\n\n",
		"event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":8,"cache_read_input_tokens":2}}` + "\n\n",
		"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n",
	}
	var collected []string
	for _, eventText := range events {
		collected = append(collected, state.ProcessAnthropicSseEvent(eventText)...)
	}
	if len(collected) != 3 {
		t.Fatalf("期望 3 个 gemini 事件（text + functionCall + 收尾），实际 %d：%v", len(collected), collected)
	}
	textEvent := covSseData(t, collected[0])
	candidates := covSlice(t, textEvent["candidates"])
	textParts := covSlice(t, covMap(t, covMap(t, candidates[0])["content"])["parts"])
	if covText(t, covMap(t, textParts[0])["text"]) != "片段" {
		t.Errorf("text part = %v", textParts)
	}
	callEvent := covSseData(t, collected[1])
	callParts := covSlice(t, covMap(t, covMap(t, covSlice(t, callEvent["candidates"])[0])["content"])["parts"])
	call := covMap(t, covMap(t, callParts[0])["functionCall"])
	if covText(t, call["name"]) != "lookup" || covMap(t, call["args"])["q"] != float64(1) {
		t.Errorf("functionCall = %v", call)
	}
	final := covSseData(t, collected[2])
	if covText(t, covMap(t, covSlice(t, final["candidates"])[0])["finishReason"]) != "STOP" {
		t.Errorf("finishReason = %v", final)
	}
	if covMap(t, final["usageMetadata"])["candidatesTokenCount"] != float64(8) {
		t.Errorf("usageMetadata = %v", final)
	}
	if state.Model != "gemini-2" {
		t.Errorf("message_start 应刷新 model，实际 %q", state.Model)
	}
	t.Run("错误事件带 status", func(t *testing.T) {
		fresh := NewAnthropicGeminiStreamState("m")
		out := fresh.ProcessAnthropicSseEvent("event: error\ndata: " + `{"error":{"message":"坏","code":"boom","type":"overloaded"}}` + "\n\n")
		if covSseName(t, out[0]) != "error" {
			t.Fatalf("应输出 error 事件：%v", out)
		}
		payload := covSseData(t, out[0])
		errObject := covMap(t, payload["error"])
		if covText(t, errObject["status"]) != "overloaded" || covText(t, errObject["code"]) != "boom" {
			t.Errorf("error payload = %v", errObject)
		}
		if again := fresh.ProcessAnthropicSseEvent(events[0]); again != nil {
			t.Errorf("失败后幂等：%v", again)
		}
	})
	t.Run("data.type=error 与解析失败", func(t *testing.T) {
		fresh := NewAnthropicGeminiStreamState("m")
		out := fresh.ProcessAnthropicSseEvent("data: " + `{"type":"error","message":"负载"}` + "\n\n")
		payload := covSseData(t, out[0])
		if covMap(t, payload["error"])["message"] != "负载" {
			t.Errorf("data.type=error payload = %v", payload)
		}
		fresh2 := NewAnthropicGeminiStreamState("m")
		out2 := fresh2.ProcessAnthropicSseEvent("data: {坏\n\n")
		if covMap(t, covSseData(t, out2[0])["error"])["status"] != "INTERNAL" {
			t.Errorf("解析失败默认 INTERNAL：%v", out2)
		}
	})
	t.Run("CompleteGeminiStream 幂等", func(t *testing.T) {
		fresh := NewAnthropicGeminiStreamState("m")
		_ = fresh.ProcessAnthropicSseEvent("data: " + `{"type":"message_stop"}` + "\n\n")
		if again := fresh.CompleteGeminiStream(); again != nil {
			t.Errorf("完成后应幂等：%v", again)
		}
	})
}

func TestCovSummarizeAndRenderGemini(t *testing.T) {
	summary := SummarizeGeminiResponse(map[string]any{
		"candidates": []any{
			map[string]any{
				"finishReason": "MAX_TOKENS",
				"content": map[string]any{"parts": []any{
					map[string]any{"text": "甲"},
					map[string]any{"text": "乙"},
					map[string]any{"functionCall": map[string]any{"name": "f1", "args": map[string]any{"a": 1}}},
					map[string]any{"functionCall": map[string]any{"name": ""}},
					map[string]any{"functionCall": map[string]any{"name": "f2"}},
					nil,
				}},
			},
			nil,
		},
		"usageMetadata": map[string]any{"promptTokenCount": 3, "candidatesTokenCount": 5, "totalTokenCount": 8},
	})
	if summary.Text != "甲乙" {
		t.Errorf("Text = %q", summary.Text)
	}
	if summary.FinishReason != "MAX_TOKENS" {
		t.Errorf("FinishReason = %q", summary.FinishReason)
	}
	if len(summary.FunctionCalls) != 2 {
		t.Fatalf("期望 2 个 function call：%v", summary.FunctionCalls)
	}
	if summary.FunctionCalls[0]["id"] != "call_0" || summary.FunctionCalls[1]["id"] != "call_1" {
		t.Errorf("call id 应按序号生成：%v", summary.FunctionCalls)
	}
	if summary.Usage == nil {
		t.Fatal("usage 应透传")
	}

	t.Run("chat 协议渲染", func(t *testing.T) {
		got := RenderGeminiResponseJSON(GeminiNativeProtocolChatCompletions, "g-model", summary)
		if covText(t, got["model"]) != "g-model" {
			t.Errorf("model = %v", got["model"])
		}
		choice := covMap(t, covSlice(t, got["choices"])[0])
		// 有 function calls 时 finish_reason 固定 tool_calls（Node 同名函数）。
		if covText(t, choice["finish_reason"]) != "tool_calls" {
			t.Errorf("带 tool_calls 的 finish_reason = %v，期望 tool_calls", choice["finish_reason"])
		}
		message := covMap(t, choice["message"])
		if message["content"] != nil {
			t.Errorf("有 tool_calls 时 content 应为 nil：%v", message)
		}
		calls := covSlice(t, message["tool_calls"])
		first := covMap(t, calls[0])
		if covText(t, covMap(t, first["function"])["name"]) != "f1" || covNumber(t, first["index"]) != 0 {
			t.Errorf("tool_calls[0] = %v", first)
		}
		usage := covMap(t, got["usage"])
		if usage["total_tokens"] != float64(8) {
			t.Errorf("usage = %v", usage)
		}
	})
	t.Run("chat 协议纯文本与 SAFETY", func(t *testing.T) {
		got := RenderGeminiResponseJSON(GeminiNativeProtocolChatCompletions, "m", GeminiResponseSummary{
			Text: "仅文本", FinishReason: "SAFETY",
			Usage: map[string]any{},
		})
		choice := covMap(t, covSlice(t, got["choices"])[0])
		if covText(t, choice["finish_reason"]) != "content_filter" {
			t.Errorf("SAFETY -> content_filter，实际 %v", choice["finish_reason"])
		}
		if covText(t, covMap(t, choice["message"])["content"]) != "仅文本" {
			t.Errorf("message.content = %v", choice["message"])
		}
	})
	t.Run("responses 协议渲染", func(t *testing.T) {
		got := RenderGeminiResponseJSON(GeminiNativeProtocolResponses, "m", summary)
		if covText(t, got["object"]) != "response" {
			t.Errorf("object = %v", got["object"])
		}
		output := covSlice(t, got["output"])
		if len(output) != 3 {
			t.Fatalf("期望 message + 2 function_call，实际 %v", output)
		}
		usage := covMap(t, got["usage"])
		if usage["input_tokens"] != float64(3) || usage["output_tokens"] != float64(5) {
			t.Errorf("responses usage = %v", usage)
		}
		empty := RenderGeminiResponseJSON(GeminiNativeProtocolResponses, "m", GeminiResponseSummary{})
		if len(covSlice(t, empty["output"])) != 0 {
			t.Errorf("空 summary 应输出空 output：%v", empty)
		}
	})
	t.Run("messages 协议渲染", func(t *testing.T) {
		got := RenderGeminiResponseJSON(GeminiNativeProtocolMessages, "m", summary)
		if covText(t, got["stop_reason"]) != "tool_use" {
			t.Errorf("有 tool_use 时 stop_reason = %v", got["stop_reason"])
		}
		blocks := covSlice(t, got["content"])
		if len(blocks) != 3 {
			t.Fatalf("期望 text + 2 tool_use，实际 %v", blocks)
		}
		textOnly := RenderGeminiResponseJSON(GeminiNativeProtocolMessages, "m", GeminiResponseSummary{
			Text: "纯文", FinishReason: "RECITATION", Usage: map[string]any{},
		})
		if covText(t, textOnly["stop_reason"]) != "stop_sequence" {
			t.Errorf("RECITATION -> stop_sequence，实际 %v", textOnly["stop_reason"])
		}
		if covMap(t, textOnly["usage"])["input_tokens"] != float64(0) {
			t.Errorf("usage 缺省为 0：%v", textOnly["usage"])
		}
	})
	t.Run("协议选择", func(t *testing.T) {
		if got := GeminiNativeDownstreamProtocolForMapping("responses"); got != GeminiNativeProtocolResponses {
			t.Errorf("responses -> %v", got)
		}
		if got := GeminiNativeDownstreamProtocolForMapping("messages"); got != GeminiNativeProtocolMessages {
			t.Errorf("messages -> %v", got)
		}
		if got := GeminiNativeDownstreamProtocolForMapping("messages_alias_unknown"); got != GeminiNativeProtocolChatCompletions {
			t.Errorf("缺省 -> chat_completions，实际 %v", got)
		}
		if got := GeminiNativeDownstreamProtocolForMapping("anthropic_messages"); got != GeminiNativeProtocolMessages {
			t.Errorf("anthropic_messages -> messages，实际 %v", got)
		}
	})
	t.Run("buffer 变体", func(t *testing.T) {
		got := TransformGeminiJSONBufferToDownstreamJSON([]byte("{broken"), GeminiNativeProtocolChatCompletions, "m")
		if !strings.Contains(string(got), `"finish_reason":"stop"`) {
			t.Errorf("坏 JSON 应按空 summary 渲染：%s", got)
		}
	})
}

func TestCovGeminiNativeSseRenderState(t *testing.T) {
	summary := GeminiResponseSummary{
		Text:          "文本",
		FunctionCalls: []map[string]any{{"id": "call_0", "name": "f", "args": map[string]any{"x": 1}}},
		FinishReason:  "STOP",
	}
	t.Run("chat 协议事件序列", func(t *testing.T) {
		state := NewGeminiNativeSseRenderState(GeminiNativeProtocolChatCompletions, "g")
		out := state.RenderSseSummary(summary)
		if len(out) != 4 {
			t.Fatalf("期望 role+text+tool+finish 四事件，实际 %d", len(out))
		}
		first := covSseData(t, out[0])
		delta := covMap(t, covMap(t, covSlice(t, first["choices"])[0])["delta"])
		if delta["role"] != "assistant" {
			t.Errorf("首事件 delta = %v", delta)
		}
		finish := covChatChunk(t, out[3])
		if finish["finish_reason"] != "tool_calls" {
			t.Errorf("带 tool_calls 的 finish chunk = %v", out[3])
		}
		done := state.RenderSseDone()
		if done != "data: [DONE]\n\n" {
			t.Errorf("finish 已发后 Done 只补 [DONE]，实际 %q", done)
		}
	})
	t.Run("chat 协议 Done 补 finish", func(t *testing.T) {
		state := NewGeminiNativeSseRenderState(GeminiNativeProtocolChatCompletions, "g")
		done := state.RenderSseDone()
		if !strings.Contains(done, `"finish_reason":"stop"`) || !strings.HasSuffix(done, "data: [DONE]\n\n") {
			t.Errorf("未发 finish 时 Done 应补齐：%q", done)
		}
	})
	t.Run("responses 协议事件序列", func(t *testing.T) {
		state := NewGeminiNativeSseRenderState(GeminiNativeProtocolResponses, "g")
		out := state.RenderSseSummary(summary)
		names := []string{}
		for _, event := range out {
			names = append(names, covSseName(t, event))
		}
		want := []string{"response.created", "response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.output_item.added"}
		if strings.Join(names, ",") != strings.Join(want, ",") {
			t.Fatalf("事件序列 = %v", names)
		}
		done := state.RenderSseDone()
		for _, want := range []string{"response.content_part.done", "response.output_item.done", "response.completed"} {
			if !strings.Contains(done, want) {
				t.Errorf("Done 缺少 %s：%q", want, done)
			}
		}
	})
	t.Run("messages 协议事件序列", func(t *testing.T) {
		state := NewGeminiNativeSseRenderState(GeminiNativeProtocolMessages, "g")
		out := state.RenderSseSummary(summary)
		names := []string{}
		for _, event := range out {
			names = append(names, covSseName(t, event))
		}
		want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_start", "content_block_delta", "content_block_stop"}
		if strings.Join(names, ",") != strings.Join(want, ",") {
			t.Fatalf("事件序列 = %v", names)
		}
		done := state.RenderSseDone()
		for _, want := range []string{"content_block_stop", "message_delta", "message_stop"} {
			if !strings.Contains(done, want) {
				t.Errorf("Done 缺少 %s：%q", want, done)
			}
		}
	})
	t.Run("messages 纯文本序列", func(t *testing.T) {
		state := NewGeminiNativeSseRenderState(GeminiNativeProtocolMessages, "g")
		out := state.RenderSseSummary(GeminiResponseSummary{Text: "只有文本"})
		if len(out) != 3 {
			t.Fatalf("期望 start + block_start + delta，实际 %d", len(out))
		}
	})
	t.Run("SSE pump 与 buffer 一致", func(t *testing.T) {
		geminiSse := "data: " + `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"你好"}]}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}` + "\n\n"
		buffered := TransformGeminiSseBufferToDownstreamSse([]byte(geminiSse), GeminiNativeProtocolChatCompletions, "m")
		if !strings.Contains(string(buffered), `"content":"你好"`) {
			t.Fatalf("SSE 转换应含文本：%s", buffered)
		}
		if !strings.HasSuffix(string(buffered), "data: [DONE]\n\n") {
			t.Errorf("SSE 应以 [DONE] 收尾：%s", buffered)
		}
	})
}

func TestCovGeminiSseAsChatNative(t *testing.T) {
	state := NewGeminiNativeChatStreamState("g-model")
	events := []string{
		"data: " + `{"candidates":[{"content":{"parts":[{"text":"你"}]}}]}` + "\n\n",
		"data: " + `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"q":1}}},{"functionCall":{}}]}}]}` + "\n\n",
		"data: " + `{"candidates":[{"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"cachedContentTokenCount":1,"thoughtsTokenCount":4}}` + "\n\n",
	}
	var collected []string
	for _, eventText := range events {
		collected = append(collected, ProcessGeminiSseEventAsChat(state, eventText)...)
	}
	// functionCall 一出现就设置 StopReason=tool_calls，并立即触发完整收尾
	// （TerminalReceived），其后携带 usage 的事件被丢弃（当前实际行为）。
	if len(collected) != 5 {
		t.Fatalf("期望 role+text+tool+finish+[DONE] 五段，实际 %d：%v", len(collected), collected)
	}
	toolChunk := covChatChunk(t, collected[2])
	toolDelta := covMap(t, toolChunk["delta"])
	calls := covSlice(t, toolDelta["tool_calls"])
	first := covMap(t, calls[0])
	if covText(t, covMap(t, first["function"])["name"]) != "lookup" {
		t.Errorf("tool chunk = %v", first)
	}
	if covNumber(t, first["index"]) != 0 {
		t.Errorf("tool index = %v", first["index"])
	}
	finishChunk := covChatChunk(t, collected[3])
	if finishChunk["finish_reason"] != "tool_calls" {
		t.Errorf("finish_reason = %v，期望 tool_calls", finishChunk["finish_reason"])
	}
	if collected[4] != "data: [DONE]\n\n" {
		t.Errorf("最后应为 [DONE]：%q", collected[4])
	}
	if state.Usage != nil {
		t.Errorf("完成后的 usage 事件不应再写入：%v", state.Usage)
	}
	t.Run("usage 细节映射", func(t *testing.T) {
		fresh := NewGeminiNativeChatStreamState("m")
		ProcessGeminiSseEventAsChat(fresh, "data: "+`{"candidates":[{"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"cachedContentTokenCount":1,"thoughtsTokenCount":4}}`+"\n\n")
		usage := covMap(t, fresh.Usage)
		if usage["prompt_tokens"] != float64(2) || usage["completion_tokens"] != float64(3) {
			t.Errorf("usage = %v", usage)
		}
		details := covMap(t, usage["prompt_tokens_details"])
		if details["cached_tokens"] != float64(1) || details["reasoning_tokens"] != float64(4) {
			t.Errorf("usage details = %v", details)
		}
	})
	t.Run("停止后幂等与 [DONE]", func(t *testing.T) {
		if again := ProcessGeminiSseEventAsChat(state, "data: "+`{"candidates":[]}`+"\n\n"); again != nil {
			t.Errorf("完成后幂等：%v", again)
		}
		fresh := NewGeminiNativeChatStreamState("m")
		out := ProcessGeminiSseEventAsChat(fresh, "data: [DONE]\n\n")
		if len(out) == 0 || out[len(out)-1] != "data: [DONE]\n\n" {
			t.Fatalf("[DONE] 应收尾：%v", out)
		}
	})
	t.Run("错误事件", func(t *testing.T) {
		fresh := NewGeminiNativeChatStreamState("m")
		out := ProcessGeminiSseEventAsChat(fresh, "data: "+`{"error":{"message":"炸","status":"UNAVAILABLE"}}`+"\n\n")
		payload := covSseData(t, out[0])
		errObject := covMap(t, payload["error"])
		if covText(t, errObject["code"]) != "UNAVAILABLE" {
			t.Errorf("error payload = %v", errObject)
		}
		fresh2 := NewGeminiNativeChatStreamState("m")
		out2 := ProcessGeminiSseEventAsChat(fresh2, "data: {坏\n\n")
		if covMap(t, covSseData(t, out2[0])["error"])["code"] != "upstream_stream_parse_error" {
			t.Errorf("解析失败 code = %v", out2)
		}
	})
	t.Run("functionCall 缺 name 跳过", func(t *testing.T) {
		fresh := NewGeminiNativeChatStreamState("m")
		out := ProcessGeminiSseEventAsChat(fresh, "data: "+`{"candidates":[{"content":{"parts":[{"functionCall":{}}]}}]}`+"\n\n")
		if len(out) != 0 {
			t.Errorf("缺 name 的 functionCall 应跳过：%v", out)
		}
	})
	t.Run("空 finishReason 缺省 stop", func(t *testing.T) {
		fresh := NewGeminiNativeChatStreamState("m")
		out := ProcessGeminiSseEventAsChat(fresh, "data: [DONE]\n\n")
		finish := covChatChunk(t, out[1])
		if finish["finish_reason"] != "stop" {
			t.Errorf("缺省 finish = %v", finish["finish_reason"])
		}
	})
	t.Run("TransformGeminiGenerateContentToOpenAIChatResponse 边界", func(t *testing.T) {
		response := map[string]any{
			"responseId":   "resp-9",
			"modelVersion": "",
			"candidates": []any{
				map[string]any{
					"finishReason": "SAFETY",
					"content":      map[string]any{"parts": []any{map[string]any{"functionCall": map[string]any{"name": "f"}}}},
				},
				nil,
			},
			"usageMetadata": map[string]any{"promptTokenCount": 4, "candidatesTokenCount": 0},
		}
		got := TransformGeminiGenerateContentToOpenAIChatResponse(response, BridgeTransformResponseOptions{Enabled: true, Model: "m"})
		if got["id"] != "resp-9" {
			t.Errorf("id = %v", got["id"])
		}
		if covText(t, got["model"]) != "m" {
			t.Errorf("model 应回退 options.Model：%v", got["model"])
		}
		choices := covSlice(t, got["choices"])
		if len(choices) != 1 {
			t.Fatalf("nil candidate 跳过后期望 1 个 choice：%v", choices)
		}
		choice := covMap(t, choices[0])
		if choice["finish_reason"] != "content_filter" {
			t.Errorf("SAFETY -> content_filter：%v", choice)
		}
		message := covMap(t, choice["message"])
		if message["content"] != nil {
			t.Errorf("纯 functionCall 时 content 应为 nil：%v", message)
		}
		usage := covMap(t, got["usage"])
		if usage["total_tokens"] != float64(4) {
			t.Errorf("total 回退 prompt+completion = %v", usage["total_tokens"])
		}
		// numberAsFloat64 / firstNonEmptyFloat 直连。
		if got := numberAsFloat64(int64(5)); got != 5 {
			t.Errorf("int64 转换 = %v", got)
		}
		if got := firstNonEmptyFloat(0, 9); got != 9 {
			t.Errorf("回退 = %v", got)
		}
		if got := geminiFinishReasonToChatFinishReason("BLOCKED"); got != "content_filter" {
			t.Errorf("BLOCKED -> %q", got)
		}
	})
}

func TestCovGeminiCodeAssistUnwrap(t *testing.T) {
	t.Run("payload 解包形态", func(t *testing.T) {
		cases := []struct {
			name  string
			in    string
			equal bool // 输出是否与输入相同
		}{
			{"对象包装", `{"response":{"candidates":[]}}`, false},
			{"数组包装", `{"response":[1,2]}`, false},
			{"非对象包装", `{"response":"text"}`, true},
			{"无 response 键", `{"foo":1}`, true},
			{"非法 JSON", `{坏`, true},
			{"JSON 数组顶层", `[1]`, true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got := UnwrapGeminiCodeAssistPayload(tc.in)
				if tc.equal && got != tc.in {
					t.Errorf("应原样返回：got %q", got)
				}
				if !tc.equal && got == tc.in {
					t.Errorf("应解包：got %q", got)
				}
			})
		}
	})
	// 行为存疑：PumpUnwrapGeminiCodeAssistSse 重写事件时只保留 data 行，
	// 携带 "event:" 行的事件会丢失事件名（与注释「事件框架保留」不完全一致）；
	// 按当前实际行为断言。
	t.Run("SSE 解包重写 data 行", func(t *testing.T) {
		body := []byte("event: x\ndata: {\"response\":{\"candidates\":[]}}\n\ndata: [DONE]\n\n")
		got := UnwrapGeminiCodeAssistSseBuffer(body)
		text := string(got)
		if strings.Contains(text, "event: x\n") {
			t.Errorf("当前实现不保留 event 行：%q", text)
		}
		if !strings.Contains(text, `"candidates":[]`) {
			t.Errorf("内层 response 应提升：%q", text)
		}
		if !strings.Contains(text, "data: [DONE]") {
			t.Errorf("DONE 应保留：%q", text)
		}
	})
	t.Run("非流式收集与文本合并", func(t *testing.T) {
		body := []byte(strings.Join([]string{
			"data: " + `{"response":{"candidates":[{"content":{"parts":[{"text":"一"},{"functionCall":{"name":"f"}}]}}]}}`,
			"",
			"data: " + `{"response":{"candidates":[{"content":{"parts":[{"text":"二"}]}}]}}`,
			"",
			"data: " + `{"response":{"candidates":[{"content":{"parts":[]}}]}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n"))
		got := CollectGeminiCodeAssistSseBuffer(body)
		var parsed map[string]any
		if err := json.Unmarshal(got, &parsed); err != nil {
			t.Fatalf("输出应为 JSON：%s", got)
		}
		// 带 parts 的最后事件是「二」，合并收集文本「一二」。
		candidates := covSlice(t, parsed["candidates"])
		parts := covSlice(t, covMap(t, covMap(t, candidates[0])["content"])["parts"])
		if covText(t, covMap(t, parts[0])["text"]) != "一二" {
			t.Errorf("合并文本 = %v", parts[0])
		}
	})
	t.Run("空流兜底与无文本 part", func(t *testing.T) {
		empty := CollectGeminiCodeAssistSseBuffer(nil)
		if string(empty) != "{}" {
			t.Errorf("空流应输出 {}：%q", empty)
		}
		noText := CollectGeminiCodeAssistSseBuffer([]byte("data: {\"response\":{\"candidates\":[]}}\n\n"))
		if !strings.Contains(string(noText), "candidates") {
			t.Errorf("无文本应保留最后响应：%q", noText)
		}
	})
	t.Run("合并缺 parts 的响应", func(t *testing.T) {
		merged := geminiCodeAssistMergeCollectedTextParts(map[string]any{"candidates": []any{}}, []string{"文"})
		candidates := covSlice(t, merged["candidates"])
		content := covMap(t, covMap(t, candidates[0])["content"])
		if covText(t, content["role"]) != "model" {
			t.Errorf("content role = %v", content)
		}
		parts := covSlice(t, content["parts"])
		if covText(t, covMap(t, parts[0])["text"]) != "文" {
			t.Errorf("前置 text part = %v", parts)
		}
	})
}
