package openaicompat

// 响应侧剩余小分支的直连覆盖：bridge_response / bridge_gemini_response /
// bridge_codex_response / bridgedispatch 的缺省折叠与 nil 跳过路径。

import (
	"strings"
	"testing"
)

func TestCovResponseEdgeHelpers(t *testing.T) {
	t.Run("safeBridgeIDSegment 边界", func(t *testing.T) {
		if got := safeBridgeIDSegment(stringsRepeatForTest("x", 120)); len(got) != 96 {
			t.Errorf("超长应截断 96：%d", len(got))
		}
		if got := safeBridgeIDSegment(""); got != "anthropic" {
			t.Errorf("空串回退 anthropic：%q", got)
		}
		if got := safeBridgeIDSegment("!!!"); got != "___" {
			t.Errorf("全非法字符替换下划线：%q", got)
		}
	})
	t.Run("anthropicUsageToChatUsage 双空", func(t *testing.T) {
		usage := anthropicUsageToChatUsage(nil, nil)
		if usage["prompt_tokens"] != float64(0) || usage["completion_tokens"] != float64(0) {
			t.Errorf("双空 usage = %v", usage)
		}
		outputOnly := anthropicUsageToChatUsage(map[string]any{"output_tokens": float64(2)}, nil)
		if outputOnly["completion_tokens"] != float64(2) {
			t.Errorf("仅 output usage = %v", outputOnly)
		}
	})
	t.Run("openAIErrorFromAnthropicPayload 回退链", func(t *testing.T) {
		payload := openAIErrorFromAnthropicPayload(nil)
		errObject := covMap(t, payload["error"])
		if errObject["message"] != "上游 Anthropic Messages 请求失败" || errObject["type"] != "upstream_error" {
			t.Errorf("nil payload 回退 = %v", errObject)
		}
		plain := openAIErrorFromAnthropicPayload(map[string]any{"message": "直接消息", "type": "t1"})
		plainObject := covMap(t, plain["error"])
		if plainObject["message"] != "直接消息" || plainObject["code"] != "t1" {
			t.Errorf("无 error 包裹回退 = %v", plainObject)
		}
	})
	t.Run("TransformAnthropicMessagesJSONToChatJSONBody", func(t *testing.T) {
		body := string(TransformAnthropicMessagesJSONToChatJSONBody(map[string]any{"id": "m1"}, "fb"))
		if !strings.Contains(body, `"id":"chatcmpl_m1"`) {
			t.Errorf("JSON 体 = %s", body)
		}
	})
	t.Run("ChatCompletionJSONToAnthropicMessage 首选 nil", func(t *testing.T) {
		result := ChatCompletionJSONToAnthropicMessage(map[string]any{
			"choices": []any{nil},
		}, "m")
		if covSlice(t, result["content"]) == nil {
			t.Errorf("nil choice 应产出空 content 数组：%v", result["content"])
		}
	})
	t.Run("AsAnthropic 解析失败", func(t *testing.T) {
		state := NewAnthropicFromChatStreamState("m")
		out := ProcessChatCompletionsSseEventAsAnthropic(state, "data: {坏\n\n")
		if covMap(t, covSseData(t, out[0])["error"])["code"] != "upstream_stream_parse_error" {
			t.Errorf("解析失败应终止：%v", out)
		}
		if again := ProcessChatCompletionsSseEventAsAnthropic(state, "data: [DONE]\n\n"); again != nil {
			t.Errorf("失败后幂等：%v", again)
		}
	})
	t.Run("appendToolCallDelta nil 与缺 index", func(t *testing.T) {
		state := NewAnthropicFromChatStreamState("m")
		var out []string
		state.appendToolCallDelta(&out, nil)
		if len(out) != 0 {
			t.Errorf("nil toolCall 应忽略：%v", out)
		}
		state.appendToolCallDelta(&out, map[string]any{"id": "i0", "function": map[string]any{"name": "f"}})
		if len(out) == 0 {
			t.Fatal("缺 index 默认 0 应产出 block start")
		}
		if !strings.Contains(strings.Join(out, ""), `"id":"i0"`) {
			t.Errorf("block start = %v", out)
		}
	})
	t.Run("CompleteAnthropicFromChatStream 幂等", func(t *testing.T) {
		state := NewAnthropicFromChatStreamState("m")
		state.Completed = true
		if out := CompleteAnthropicFromChatStream(state); out != nil {
			t.Errorf("completed 应 nil：%v", out)
		}
		failed := NewAnthropicFromChatStreamState("m")
		failed.Failed = true
		if out := FailAnthropicFromChatStream(failed, "x", "y"); out != nil {
			t.Errorf("failed 应 nil：%v", out)
		}
	})
	t.Run("AsGemini 幂等与空数据", func(t *testing.T) {
		state := NewGeminiChatStreamState("m")
		state.Completed = true
		if out := ProcessChatCompletionsSseEventAsGemini(state, "data: x\n\n"); out != nil {
			t.Errorf("completed 应 nil：%v", out)
		}
		fresh := NewGeminiChatStreamState("m")
		if out := ProcessChatCompletionsSseEventAsGemini(fresh, "event: ping\n\n"); out != nil {
			t.Errorf("data nil 应 nil：%v", out)
		}
		fresh2 := NewGeminiChatStreamState("m")
		out := ProcessChatCompletionsSseEventAsGemini(fresh2, "data: "+`{"choices":[{"delta":{"tool_calls":[{"index":3,"id":"later"}]}}]}`+"\n\n")
		// 只有 id 没有名字：不产出事件，但状态已记录。
		if len(out) != 0 {
			t.Errorf("无名工具不应有输出：%v", out)
		}
	})
	t.Run("gemini 状态完成与失败幂等", func(t *testing.T) {
		state := NewGeminiChatStreamState("m")
		state.Completed = true
		if out := CompleteGeminiChatStream(state); out != nil {
			t.Errorf("completed 应 nil：%v", out)
		}
		failed := NewGeminiChatStreamState("m")
		failed.Failed = true
		if out := FailGeminiChatStream(failed, "x", "y"); out != nil {
			t.Errorf("failed 应 nil：%v", out)
		}
	})
	t.Run("gemini 工具完成收集", func(t *testing.T) {
		state := NewGeminiChatStreamState("m")
		var out []string
		state.emitCompletedToolCalls(&out)
		if len(out) != 0 {
			t.Errorf("无工具应为空：%v", out)
		}
		// 名字为空的工具被过滤，全部过滤后不产出。
		state.toolCalls[0] = &geminiStreamToolCallState{id: "a"}
		state.emitCompletedToolCalls(&out)
		if len(out) != 0 {
			t.Errorf("无名工具应过滤：%v", out)
		}
		// 两个有名字工具按 id 排序产出。
		state.toolCalls[1] = &geminiStreamToolCallState{id: "b", name: "second", arguments: `{"b":1}`}
		state.toolCalls[0] = &geminiStreamToolCallState{id: "a", name: "first", arguments: ""}
		state.emitCompletedToolCalls(&out)
		if len(out) != 1 {
			t.Fatalf("应产出一个合并事件：%v", out)
		}
		event := covSseData(t, out[0])
		candidates := covSlice(t, event["candidates"])
		callParts := covSlice(t, covMap(t, covMap(t, candidates[0])["content"])["parts"])
		if covText(t, covMap(t, callParts[0])["functionCall"].(map[string]any)["name"]) != "first" {
			t.Errorf("排序第一个应为 first：%v", callParts)
		}
		if len(state.toolCalls) != 0 {
			t.Errorf("产出后应清空工具状态：%v", state.toolCalls)
		}
		if !state.EmittedContent {
			t.Error("工具产出应置 EmittedContent")
		}
	})
	t.Run("ProcessGeminiSseEventAsChat 边界", func(t *testing.T) {
		completed := NewGeminiNativeChatStreamState("m")
		completed.Completed = true
		if out := ProcessGeminiSseEventAsChat(completed, "data: x\n\n"); out != nil {
			t.Errorf("completed 应 nil：%v", out)
		}
		fresh := NewGeminiNativeChatStreamState("m")
		if out := ProcessGeminiSseEventAsChat(fresh, "event: ping\n\n"); out != nil {
			t.Errorf("data nil 应 nil：%v", out)
		}
		// 候选/内容/part 逐级 nil 或空跳过：空 text 与无名 functionCall 均不产出。
		fresh2 := NewGeminiNativeChatStreamState("m")
		out := ProcessGeminiSseEventAsChat(fresh2, "data: "+`{"candidates":[null,{"content":null},{"content":{"parts":[null,{"text":""},{"functionCall":{"name":""}}]}}]}`+"\n\n")
		if len(out) != 0 {
			t.Errorf("空候选不应有输出：%v", out)
		}
		// functionCall args 非对象折叠 {}。
		fresh3 := NewGeminiNativeChatStreamState("m")
		out3 := ProcessGeminiSseEventAsChat(fresh3, "data: "+`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"f","args":"x"}}]}}]}`+"\n\n")
		if !strings.Contains(strings.Join(out3, ""), `"arguments":"{}"`) {
			t.Errorf("非对象 args 应回退 {}：%v", out3)
		}
	})
	t.Run("gemini native 完成幂等", func(t *testing.T) {
		state := NewGeminiNativeChatStreamState("m")
		state.Failed = true
		if out := CompleteGeminiNativeChatStream(state); out != nil {
			t.Errorf("failed 应 nil：%v", out)
		}
	})
	t.Run("gemini 提取与用量助手", func(t *testing.T) {
		if parts := extractGeminiParts(nil); len(parts) != 0 {
			t.Errorf("nil content = %v", parts)
		}
		if parts := extractGeminiParts(map[string]any{"text": "直出"}); len(parts) != 1 || parts[0] != "直出" {
			t.Errorf("text 直出 = %v", parts)
		}
		if calls := geminiFunctionCallsToChatToolCalls(nil); calls != nil {
			t.Errorf("nil content = %v", calls)
		}
		if calls := geminiFunctionCallsToChatToolCalls(map[string]any{"parts": []any{nil, map[string]any{"functionCall": nil}, map[string]any{"functionCall": map[string]any{"name": "f", "args": "bad"}}}}); len(calls) != 1 {
			t.Errorf("应只剩有效 call：%v", calls)
		} else if args := covMap(t, covMap(t, calls[0])["function"])["arguments"]; args != "{}" {
			t.Errorf("bad args 应折叠 {}：%v", args)
		}
		if got := chatUsageToGeminiUsage(nil); got != nil {
			t.Errorf("nil usage = %v", got)
		}
		usage := geminiUsageToChatUsage(map[string]any{"promptTokenCount": 2, "candidatesTokenCount": 3})
		if usage["total_tokens"] != float64(5) {
			t.Errorf("total 回退相加 = %v", usage["total_tokens"])
		}
		if got := finishReasonFromGeminiCandidate(map[string]any{"finishReason": "OTHER"}); got != "stop" {
			t.Errorf("未知 finish = %q", got)
		}
	})
	t.Run("TransformSSEStreamToGeminiSSE", func(t *testing.T) {
		event := "data: " + `{"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"
		if got := TransformSSEStreamToGeminiSSE(event, BridgeTransformResponseOptions{}); got != event {
			t.Error("禁用应原样返回")
		}
		got := TransformSSEStreamToGeminiSSE(event, BridgeTransformResponseOptions{Enabled: true, Model: "m"})
		if !strings.Contains(got, `"text":"hi"`) {
			t.Errorf("启用应转 gemini SSE：%s", got)
		}
	})
}

func TestCovGeminiResponseEdgeBranches(t *testing.T) {
	t.Run("anthropic content parts 形态", func(t *testing.T) {
		parts := anthropicContentBlocksToGeminiParts([]any{
			nil,
			map[string]any{"type": "text"},
			map[string]any{"type": "tool_use", "name": "f", "input": "bad"},
		})
		if len(parts) != 1 {
			t.Fatalf("应只剩 functionCall：%v", parts)
		}
		args := covMap(t, covMap(t, parts[0])["functionCall"])["args"]
		if len(covMapStatic(args)) != 0 {
			t.Errorf("非对象 input 应回退 {}：%v", args)
		}
	})
	t.Run("finish reason 兜底分支", func(t *testing.T) {
		// parts 携带 functionCall 时 tool_use 之外的 reason 也映射 STOP。
		parts := []any{map[string]any{"functionCall": map[string]any{}}}
		if got := anthropicStopReasonToGeminiFinishReason("end_turn", parts); got != "STOP" {
			t.Errorf("functionCall parts -> STOP，实际 %q", got)
		}
		if got := geminiChatFinishReason(GeminiResponseSummary{FinishReason: "RECITATION"}); got != "content_filter" {
			t.Errorf("RECITATION -> content_filter，实际 %q", got)
		}
		if got := geminiAnthropicStopReason("recitation"); got != "stop_sequence" {
			t.Errorf("recitation -> stop_sequence，实际 %q", got)
		}
	})
	t.Run("usage 回退分支", func(t *testing.T) {
		previous := map[string]any{"cachedContentTokenCount": float64(4)}
		usage := anthropicUsageToGeminiUsage(map[string]any{"output_tokens": float64(2)}, previous)
		if usage["cachedContentTokenCount"] != float64(4) {
			t.Errorf("previous cached 回退 = %v", usage)
		}
		thinking := anthropicUsageToGeminiUsage(map[string]any{
			"thinking_tokens": float64(7),
		}, nil)
		if thinking["thoughtsTokenCount"] != float64(7) {
			t.Errorf("thinking_tokens 直读 = %v", thinking)
		}
	})
	t.Run("AsGemini 边角", func(t *testing.T) {
		state := NewAnthropicGeminiStreamState("m")
		// 解析失败直接 fail。
		out := state.ProcessAnthropicSseEvent("data: {坏\n\n")
		if len(out) != 1 {
			t.Fatalf("解析失败应 fail：%v", out)
		}
		// completed 后幂等。
		if again := state.ProcessAnthropicSseEvent("data: x\n\n"); again != nil {
			t.Errorf("失败后幂等：%v", again)
		}
		fresh := NewAnthropicGeminiStreamState("m")
		// 无名 block / 未记录 block stop / 未知类型不产出；孤儿 text_delta 照常转发。
		for _, payload := range []string{
			`{"type":"content_block_start","index":0,"content_block":null}`,
			`{"type":"content_block_stop","index":9}`,
			`{"type":"unknown_type"}`,
		} {
			if out := fresh.ProcessAnthropicSseEvent("data: " + payload + "\n\n"); len(out) != 0 {
				t.Errorf("%s 不应产出：%v", payload, out)
			}
		}
		if out := fresh.ProcessAnthropicSseEvent("data: " + `{"type":"content_block_delta","index":9,"delta":{"type":"text_delta","text":"孤儿"}}` + "\n\n"); len(out) != 1 {
			t.Errorf("孤儿 text_delta 应照常转发：%v", out)
		}
		if again := fresh.CompleteGeminiStream(); again == nil {
			t.Error("未完成的流 Complete 应产出收尾")
		}
	})
	t.Run("render 与 code assist 边角", func(t *testing.T) {
		// responses 协议 summary 已测；这里补 buffer 非 object root。
		got := TransformGeminiJSONBufferToDownstreamJSON([]byte("[1,2]"), GeminiNativeProtocolMessages, "m")
		if !strings.Contains(string(got), `"type":"message"`) {
			t.Errorf("非对象 root 应回退空 summary：%s", got)
		}
		// 无 data 行的事件透传。
		out := CollectGeminiCodeAssistSseBuffer([]byte("event: ping\n\n"))
		if string(out) != "{}" {
			t.Errorf("无数据事件应得空对象：%q", out)
		}
		// 合并：已有 parts 但首个带 text 的部分改写。
		merged := geminiCodeAssistMergeCollectedTextParts(map[string]any{
			"candidates": []any{"not-map", map[string]any{"content": map[string]any{"parts": []any{
				map[string]any{"text": "旧", "extra": 1},
				map[string]any{"inlineData": map[string]any{}},
			}}}},
		}, []string{"新"})
		candidates := covSlice(t, merged["candidates"])
		parts := covSlice(t, covMap(t, covMap(t, candidates[0])["content"])["parts"])
		if covText(t, covMap(t, parts[0])["text"]) != "新" {
			t.Errorf("新候选首个 text part = %v", parts)
		}
	})
}

func TestCovCodexStateEdgeBranches(t *testing.T) {
	t.Run("id 工具", func(t *testing.T) {
		if got := int64ToBase36(0); got != "0" {
			t.Errorf("base36(0) = %q", got)
		}
		suffix := codexBridgeIDSuffix()
		if !strings.Contains(suffix, "_") || len(suffix) < 8 {
			t.Errorf("id 后缀形状 = %q", suffix)
		}
	})
	t.Run("appendOutput 空事件与续 delta", func(t *testing.T) {
		state := NewCodexChatToResponsesState(CodexResponsesChatBridgeTransformOptions{})
		// 工具已存在时仅追加参数并重复 Emit（added 幂等）。
		state.ToolCalls[0] = &codexChatToolCallState{id: "fc1", added: true}
		if out := state.AppendResponsesToolCallDelta(map[string]any{
			"index": float64(0), "function": map[string]any{"arguments": "X"},
		}); out != nil {
			t.Errorf("added 后应幂等：%v", out)
		}
		if state.ToolCalls[0].arguments != "X" {
			t.Errorf("参数应累计：%q", state.ToolCalls[0].arguments)
		}
	})
	t.Run("textIndexOrZero 缺省", func(t *testing.T) {
		state := NewCodexChatToResponsesState(CodexResponsesChatBridgeTransformOptions{})
		if got := state.textIndexOrZero(); got != 0 {
			t.Errorf("缺省 text index = %d", got)
		}
	})
	t.Run("CompleteToolCallOutputItem 未 added 先补", func(t *testing.T) {
		var functionCallCollected []string
		state := NewCodexChatToResponsesState(CodexResponsesChatBridgeTransformOptions{})
		toolCall := &codexChatToolCallState{id: "fc_x", itemType: "function_call", callID: "call_x", name: "f"}
		state.CompleteToolCallOutputItem(&functionCallCollected, toolCall)
		if len(functionCallCollected) != 2 {
			t.Fatalf("未 added 应先补 start 再 done：%v", functionCallCollected)
		}
		if !strings.Contains(functionCallCollected[0], "response.output_item.added") {
			t.Errorf("首事件应为 added：%s", functionCallCollected[0])
		}
		var collected []string
		state2 := NewCodexChatToResponsesState(CodexResponsesChatBridgeTransformOptions{})
		toolCall2 := &codexChatToolCallState{id: "fc_y", itemType: "custom_tool_call", callID: "call_y", name: "shell", arguments: "raw", adapter: CodexBridgeToolAdapter{Namespace: "ns"}}
		state2.CompleteToolCallOutputItem(&collected, toolCall2)
		joined := strings.Join(collected, "")
		if !strings.Contains(joined, `"type":"custom_tool_call"`) || !strings.Contains(joined, `"namespace":"ns"`) {
			t.Errorf("custom 完成项 = %s", joined)
		}
	})
	t.Run("完成与失败幂等", func(t *testing.T) {
		state := NewCodexChatToResponsesState(CodexResponsesChatBridgeTransformOptions{})
		state.Completed = true
		if out := state.CompleteResponsesStream(); out != nil {
			t.Errorf("completed 应 nil：%v", out)
		}
		if out := state.FailResponsesStream("x", "y"); out != nil {
			t.Errorf("completed 后 fail 应 nil：%v", out)
		}
	})
	t.Run("上游错误负载回退", func(t *testing.T) {
		failure := codexUpstreamChatSseErrorFailure(map[string]any{"error": map[string]any{"type": "server_error"}})
		if failure.Code != "server_error" || failure.Message != "上游 Chat SSE 返回错误事件" {
			t.Errorf("failure = %+v", failure)
		}
		direct := codexUpstreamChatSseErrorFailure(map[string]any{"message": "直出", "code": "c1"})
		if direct.Code != "c1" || direct.Message != "直出" {
			t.Errorf("无 error 包裹回退 = %+v", direct)
		}
	})
	t.Run("usage 无 total 回退相加", func(t *testing.T) {
		usage := codexChatUsageToResponsesUsage(map[string]any{"prompt_tokens": float64(3), "completion_tokens": float64(4)})
		if usage["total_tokens"] != float64(7) {
			t.Errorf("total = %v", usage["total_tokens"])
		}
	})
	t.Run("dispatch 剩余分支", func(t *testing.T) {
		// role 非字符串按 user 处理；blocks 为空的内容跳过；
		// functionCall args 非对象折叠 {}；functionResponse response 缺省 {}。
		output, err := BuildGeminiToAnthropicMessagesBody(map[string]any{
			"contents": []any{
				map[string]any{"role": 3, "parts": []any{map[string]any{"text": "默认角色"}}},
				map[string]any{"role": "model", "parts": []any{}},
				map[string]any{"role": "model", "parts": []any{map[string]any{"functionCall": map[string]any{"name": "f", "args": "bad"}}}},
				map[string]any{"role": "user", "parts": []any{map[string]any{"functionResponse": map[string]any{"name": "f"}}}},
			},
			"tools": []any{nil},
		}, BridgeRequestBodyOptions{DefaultModel: "m"})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		messages := covSlice(t, output["messages"])
		// 默认角色 user 文本、model functionCall、user tool_result。
		if len(messages) != 3 {
			t.Fatalf("期望 3 条消息，实际 %d：%v", len(messages), messages)
		}
		callBlock := covToolUseBlocks(t, covSlice(t, covMap(t, messages[1])["content"]))
		if input := covMap(t, callBlock[0])["input"]; len(covMapStatic(input)) != 0 {
			t.Errorf("bad args 应回退 {}：%v", input)
		}
		if geminiSystemInstructionText(nil) != "" {
			t.Error("nil systemInstruction 应为空")
		}
	})
}
