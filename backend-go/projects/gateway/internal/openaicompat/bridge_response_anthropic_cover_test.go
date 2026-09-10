package openaicompat

// Anthropic Messages <-> OpenAI Chat 双向（JSON + SSE）转换的补充覆盖测试。
// 语义对照 Node 归档 openai-anthropic-bridge.ts / anthropic-openai-chat-bridge.ts。

import (
	"encoding/json"
	"strings"
	"testing"
)

// covSseData 取一条 SSE 事件文本的 data JSON（末段）。
func covSseData(t *testing.T, eventText string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(eventText, "\n") {
		if strings.HasPrefix(line, "data: ") {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &parsed); err != nil {
				t.Fatalf("data 不是 JSON：%q（%v）", line, err)
			}
			return parsed
		}
	}
	t.Fatalf("事件缺少 data 行：%q", eventText)
	return nil
}

// covSseName 取一条 SSE 事件文本的 event: 行。
func covSseName(t *testing.T, eventText string) string {
	t.Helper()
	for _, line := range strings.Split(eventText, "\n") {
		if strings.HasPrefix(line, "event: ") {
			return strings.TrimPrefix(line, "event: ")
		}
	}
	return ""
}

// covChatChunk 取 chat chunk 的 choices[0]。
func covChatChunk(t *testing.T, eventText string) map[string]any {
	t.Helper()
	data := covSseData(t, eventText)
	choices := covSlice(t, data["choices"])
	if len(choices) != 1 {
		t.Fatalf("chunk 应含 1 个 choice：%v", data)
	}
	return covMap(t, choices[0])
}

func TestCovAnthropicMessageToChatCompletion(t *testing.T) {
	message := map[string]any{
		"id":          "msg_1",
		"model":       "claude-9",
		"stop_reason": "max_tokens",
		"content": []any{
			map[string]any{"type": "text", "text": "前"},
			map[string]any{"type": "text", "text": "后"},
			map[string]any{"type": "tool_use", "id": "t1", "name": "lookup", "input": map[string]any{"q": 1}},
			map[string]any{"type": "tool_use", "name": "noid", "input": "not-object"},
			nil,
		},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "cache_read_input_tokens": 4},
	}
	result := AnthropicMessageToChatCompletion(message, "fallback")
	if !strings.HasPrefix(covText(t, result["id"]), "chatcmpl_msg_1") {
		t.Errorf("id = %v，期望 chatcmpl_msg_1 前缀", result["id"])
	}
	if covText(t, result["model"]) != "claude-9" {
		t.Errorf("model = %v", result["model"])
	}
	choice := covMap(t, covSlice(t, result["choices"])[0])
	if covText(t, choice["finish_reason"]) != "length" {
		t.Errorf("max_tokens 应映射 length，实际 %v", choice["finish_reason"])
	}
	assistant := covMap(t, choice["message"])
	if covText(t, assistant["content"]) != "前后" {
		t.Errorf("content = %v，期望文本拼接", assistant["content"])
	}
	calls := covSlice(t, assistant["tool_calls"])
	if len(calls) != 2 {
		t.Fatalf("期望 2 个 tool_call，实际 %v", calls)
	}
	second := covMap(t, calls[1])
	if covText(t, second["id"]) != "call_1" {
		t.Errorf("缺 id 的 tool_use 应生成 call_<index>，实际 %v", second["id"])
	}
	if covText(t, covMap(t, second["function"])["arguments"]) != "{}" {
		t.Errorf("非对象 input 应折叠 {}，实际 %v", second["function"])
	}
	usage := covMap(t, result["usage"])
	if usage["prompt_tokens"] != float64(10) || usage["completion_tokens"] != float64(5) {
		t.Errorf("usage = %v", usage)
	}
	details := covMap(t, usage["prompt_tokens_details"])
	if details["cached_tokens"] != float64(4) {
		t.Errorf("cached_tokens = %v", details["cached_tokens"])
	}

	t.Run("空 model 与空 content", func(t *testing.T) {
		result := AnthropicMessageToChatCompletion(map[string]any{}, "fallback-model")
		if covText(t, result["model"]) != "fallback-model" {
			t.Errorf("model = %v，期望 fallback", result["model"])
		}
		choice := covMap(t, covSlice(t, result["choices"])[0])
		assistant := covMap(t, choice["message"])
		if covText(t, assistant["content"]) != "" {
			t.Errorf("空 content 应为空串，实际 %v", assistant["content"])
		}
		if choice["finish_reason"] != "stop" {
			t.Errorf("缺 stop_reason 应映射 stop，实际 %v", choice["finish_reason"])
		}
	})
	t.Run("tool_use 优先 content null", func(t *testing.T) {
		result := AnthropicMessageToChatCompletion(map[string]any{
			"content": []any{map[string]any{"type": "tool_use", "id": "t", "name": "f", "input": map[string]any{}}},
		}, "m")
		choice := covMap(t, covSlice(t, result["choices"])[0])
		assistant := covMap(t, choice["message"])
		if assistant["content"] != nil {
			t.Errorf("纯 tool_call 时 content 应为 nil，实际 %v", assistant["content"])
		}
	})
}

func TestCovFinishReasonAndUsageMappers(t *testing.T) {
	t.Run("openAIChatFinishReasonFromAnthropic", func(t *testing.T) {
		cases := map[string]string{
			"max_tokens": "length", "tool_use": "tool_calls", "stop_sequence": "stop",
			"end_turn": "stop", "": "stop", "pause_turn": "pause_turn",
		}
		for in, want := range cases {
			if got := openAIChatFinishReasonFromAnthropic(in); got != want {
				t.Errorf("finish(%q) = %q，期望 %q", in, got, want)
			}
		}
	})
	t.Run("chatFinishReasonToAnthropicStopReason", func(t *testing.T) {
		if got := chatFinishReasonToAnthropicStopReason("tool_calls", nil); got != "tool_use" {
			t.Errorf("tool_calls -> tool_use，实际 %q", got)
		}
		if got := chatFinishReasonToAnthropicStopReason("length", nil); got != "max_tokens" {
			t.Errorf("length -> max_tokens，实际 %q", got)
		}
		if got := chatFinishReasonToAnthropicStopReason("content_filter", nil); got != "refusal" {
			t.Errorf("content_filter -> refusal，实际 %q", got)
		}
		if got := chatFinishReasonToAnthropicStopReason("stop", nil); got != "end_turn" {
			t.Errorf("stop -> end_turn，实际 %q", got)
		}
		blocks := []any{map[string]any{"type": "tool_use", "id": "t"}}
		if got := chatFinishReasonToAnthropicStopReason("", blocks); got != "tool_use" {
			t.Errorf("tool_use block 兜底，实际 %q", got)
		}
	})
	t.Run("anthropicUsageToChatUsage 回退链", func(t *testing.T) {
		previous := map[string]any{
			"prompt_tokens": float64(3), "completion_tokens": float64(4),
			"prompt_tokens_details": map[string]any{"cached_tokens": float64(2)},
		}
		usage := anthropicUsageToChatUsage(nil, previous)
		if usage["prompt_tokens"] != float64(3) || usage["completion_tokens"] != float64(4) {
			t.Errorf("previous 回退 = %v", usage)
		}
		if covMap(t, usage["prompt_tokens_details"])["cached_tokens"] != float64(2) {
			t.Errorf("cached 回退 = %v", usage)
		}
		if got := anthropicInputTokens(nil); got != 0 {
			t.Errorf("nil usage input = %d", got)
		}
		if got := anthropicInputTokens(map[string]any{"prompt_tokens": float64(7)}); got != 7 {
			t.Errorf("prompt_tokens 回退 = %d", got)
		}
	})
	t.Run("chatUsageToAnthropicUsage 变体", func(t *testing.T) {
		if got := chatUsageToAnthropicUsage(nil); got != nil {
			t.Errorf("nil usage 应返回 nil，实际 %v", got)
		}
		usage := chatUsageToAnthropicUsage(map[string]any{
			"input_tokens": float64(1), "output_tokens": float64(2),
			"input_tokens_details": map[string]any{"cached_tokens": float64(5)},
		})
		if usage["input_tokens"] != float64(1) || usage["output_tokens"] != float64(2) {
			t.Errorf("usage = %v", usage)
		}
		if usage["cache_read_input_tokens"] != float64(5) {
			t.Errorf("input_tokens_details cached = %v", usage)
		}
		plain := chatUsageToAnthropicUsage(map[string]any{"prompt_tokens": float64(1), "completion_tokens": float64(2)})
		if _, exists := plain["cache_read_input_tokens"]; exists {
			t.Errorf("无缓存不应输出 cache_read_input_tokens：%v", plain)
		}
	})
	t.Run("transformAnthropicUsageToOpenAI", func(t *testing.T) {
		usage := transformAnthropicUsageToOpenAI(map[string]any{"input_tokens": 9, "output_tokens": 1})
		if usage["total_tokens"] != float64(10) {
			t.Errorf("total = %v", usage["total_tokens"])
		}
	})
}

func TestCovProcessAnthropicEventAsChat(t *testing.T) {
	newState := func() *AnthropicChatStreamState { return NewAnthropicChatStreamState("claude-3") }
	events := []string{
		"event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_x","model":"claude-9","usage":{"input_tokens":11,"output_tokens":1}}}` + "\n\n",
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n",
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}` + "\n\n",
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"lookup","input":{"q":1}}}` + "\n\n",
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}` + "\n\n",
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"沉思"}}` + "\n\n",
		"event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}` + "\n\n",
		"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n",
	}
	state := newState()
	var collected []string
	for _, eventText := range events {
		collected = append(collected, ProcessAnthropicEventAsChat(state, eventText)...)
	}
	joined := strings.Join(collected, "")
	// 逐事件断言关键形态。
	if !strings.Contains(collected[0], `"role":"assistant"`) {
		t.Errorf("首事件应是 role chunk：%q", collected[0])
	}
	toolStart := collected[2]
	choice := covChatChunk(t, toolStart)
	delta := covMap(t, choice["delta"])
	toolCalls := covSlice(t, delta["tool_calls"])
	first := covMap(t, toolCalls[0])
	if covText(t, first["id"]) != "t1" || covNumber(t, first["index"]) != 0 {
		t.Errorf("tool_call 起始 chunk = %v", first)
	}
	if !strings.Contains(joined, `{"content":"你好"}`) {
		t.Errorf("应包含文本 delta：%q", joined)
	}
	// input_json_delta 以 chat chunk 的 function.arguments 字段透传。
	if !strings.Contains(joined, `"arguments":"{\"a\":"`) {
		t.Errorf("应包含参数增量：%q", joined)
	}
	// message_stop 触发完整收尾：finish_reason=tool_calls、usage、[DONE]。
	tail := collected[len(collected)-1]
	if tail != "data: [DONE]\n\n" {
		t.Errorf("最后应是 [DONE]，实际 %q", tail)
	}
	final := collected[len(collected)-3]
	choice = covChatChunk(t, final)
	if covText(t, choice["finish_reason"]) != "tool_calls" {
		t.Errorf("finish_reason = %v，期望 tool_calls", choice["finish_reason"])
	}
	usageChunk := covSseData(t, collected[len(collected)-2])
	if covMap(t, usageChunk["usage"])["completion_tokens"] != float64(9) {
		t.Errorf("usage chunk = %v", usageChunk)
	}
	if state.ChatID != "chatcmpl_msg_x" {
		t.Errorf("message_start 应刷新 ChatID，实际 %q", state.ChatID)
	}
	t.Run("错误事件", func(t *testing.T) {
		state := newState()
		out := ProcessAnthropicEventAsChat(state, "event: error\ndata: "+`{"error":{"message":"炸了","type":"overloaded"}}`+"\n\n")
		if len(out) != 2 || out[1] != "data: [DONE]\n\n" {
			t.Fatalf("错误事件应输出 error + DONE：%v", out)
		}
		payload := covSseData(t, out[0])
		errObject := covMap(t, payload["error"])
		if covText(t, errObject["message"]) != "炸了" || covText(t, errObject["code"]) != "overloaded" {
			t.Errorf("error payload = %v", errObject)
		}
		if again := ProcessAnthropicEventAsChat(state, events[0]); again != nil {
			t.Errorf("失败后应不再处理事件：%v", again)
		}
	})
	t.Run("解析失败事件", func(t *testing.T) {
		state := newState()
		out := ProcessAnthropicEventAsChat(state, "data: {broken\n\n")
		if len(out) != 2 {
			t.Fatalf("解析失败应终止流：%v", out)
		}
		payload := covSseData(t, out[0])
		if covMap(t, payload["error"])["code"] != "upstream_stream_parse_error" {
			t.Errorf("error payload = %v", payload)
		}
	})
	t.Run("data 缺省与未知类型", func(t *testing.T) {
		state := newState()
		if out := ProcessAnthropicEventAsChat(state, "event: ping\n\n"); out != nil {
			t.Errorf("无 data 事件应返回 nil：%v", out)
		}
		if out := ProcessAnthropicEventAsChat(state, "data: "+`{"type":"ping"}`+"\n\n"); len(out) != 0 {
			t.Errorf("未知类型应无输出：%v", out)
		}
	})
	t.Run("content_block_start 缺 content_block", func(t *testing.T) {
		state := newState()
		out := ProcessAnthropicEventAsChat(state, "data: "+`{"type":"content_block_start","index":0}`+"\n\n")
		if len(out) != 1 {
			t.Fatalf("仍应发 role chunk：%v", out)
		}
	})
	t.Run("空 delta 与空文本", func(t *testing.T) {
		state := newState()
		state.blocks = map[int64]*anthropicStreamBlockState{}
		if out := ProcessAnthropicEventAsChat(state, "data: "+`{"type":"content_block_delta","index":5}`+"\n\n"); len(out) != 1 {
			t.Fatalf("应仅发 role chunk：%v", out)
		}
	})
	t.Run("CompleteAnthropicChatStream 缺省 finish 与已结束幂等", func(t *testing.T) {
		state := newState()
		out := CompleteAnthropicChatStream(state, "")
		if len(out) != 4 {
			t.Fatalf("期望 role+finish+usage+DONE 四段，实际 %d", len(out))
		}
		if covChatChunk(t, out[1])["finish_reason"] != "stop" {
			t.Errorf("缺省 finish 应为 stop：%v", out[1])
		}
		if usage := covSseData(t, out[2])["usage"]; usage == nil {
			t.Errorf("无 usage 时应输出零 usage chunk")
		}
		if again := CompleteAnthropicChatStream(state, "stop"); again != nil {
			t.Errorf("完成后应幂等返回 nil：%v", again)
		}
	})
	t.Run("FailAnthropicChatStream 缺省 code 与幂等", func(t *testing.T) {
		state := newState()
		out := FailAnthropicChatStream(state, "故障", "")
		payload := covSseData(t, out[0])
		if covMap(t, payload["error"])["code"] != "upstream_error" {
			t.Errorf("缺省 code = %v", payload)
		}
		if again := FailAnthropicChatStream(state, "再炸", "x"); again != nil {
			t.Errorf("失败后幂等：%v", again)
		}
	})
}

func TestCovChatCompletionToAnthropicJSON(t *testing.T) {
	t.Run("完整映射", func(t *testing.T) {
		value := map[string]any{
			"id":    "chatcmpl-9",
			"model": "gpt-x",
			"choices": []any{map[string]any{
				"message": map[string]any{
					"content": "文本",
					"tool_calls": []any{
						map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "lookup", "arguments": `{"q":1}`}},
						// 缺 name 的 tool_call 被跳过。
						map[string]any{"id": "c2", "type": "function", "function": map[string]any{"arguments": "not-json"}},
						map[string]any{"type": "function", "function": map[string]any{"name": "noid", "arguments": "not-json"}},
					},
				},
				"finish_reason": "length",
			}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 4},
		}
		result := ChatCompletionJSONToAnthropicMessage(value, "fallback")
		if covText(t, result["id"]) != "msg_chatcmpl-9" {
			t.Errorf("id 应补 msg_ 前缀，实际 %v", result["id"])
		}
		if covText(t, result["stop_reason"]) != "max_tokens" {
			t.Errorf("length -> max_tokens，实际 %v", result["stop_reason"])
		}
		usage := covMap(t, result["usage"])
		if usage["input_tokens"] != float64(3) {
			t.Errorf("usage = %v", usage)
		}
		blocks := covSlice(t, result["content"])
		toolUses := covToolUseBlocks(t, blocks)
		if len(toolUses) != 2 {
			t.Fatalf("期望 2 个 tool_use block：%v", blocks)
		}
		if covText(t, toolUses[1]["id"]) != "toolu_2" {
			t.Errorf("缺 id 应生成 toolu_<len>，实际 %v", toolUses[1]["id"])
		}
		if covText(t, covMap(t, toolUses[1]["input"])["_raw"].(string)) != "not-json" {
			t.Errorf("非法 JSON 参数应包 _raw：%v", toolUses[1])
		}
	})
	t.Run("choices 缺失与 reasoning_content 回退", func(t *testing.T) {
		result := ChatCompletionJSONToAnthropicMessage(map[string]any{}, "fallback")
		if covText(t, result["model"]) != "fallback" {
			t.Errorf("model = %v", result["model"])
		}
		if covSlice(t, result["content"]) == nil {
			t.Errorf("content 应至少是空数组：%v", result["content"])
		}
		blocks := ChatCompletionJSONToAnthropicMessage(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"reasoning_content": "推理中"}}},
		}, "m")
		content := covSlice(t, blocks["content"])
		if covBlockText(t, content, 0) != "推理中" {
			t.Errorf("reasoning_content 应回退为 text block：%v", content)
		}
	})
	t.Run("chatMessageTextContent 数组内容", func(t *testing.T) {
		got := chatMessageTextContent(map[string]any{"content": []any{
			map[string]any{"type": "output_text", "text": "a"},
			map[string]any{"type": "image_url"},
			nil,
		}})
		if got != "a" {
			t.Errorf("数组 content 提取 = %q，期望 a", got)
		}
		if got := chatMessageTextContent(nil); got != "" {
			t.Errorf("nil message = %q", got)
		}
	})
}

func TestCovChatSseAsAnthropic(t *testing.T) {
	chatEvent := func(payload string) string {
		return "data: " + payload + "\n\n"
	}
	events := []string{
		chatEvent(`{"id":"chatcmpl-1","model":"gpt-x","choices":[{"index":0,"delta":{"role":"assistant","content":"早"},"finish_reason":null}]}`),
		chatEvent(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"t1","function":{"name":"lookup"}}]}}]}`),
		// 先到参数增量、后到名字：buffered 参数应在 block start 后补发。
		chatEvent(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"late\":"}}]}}]}`),
		chatEvent(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"t2","function":{"name":"second","arguments":"1}"}}]}}]}`),
		chatEvent(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":6,"prompt_tokens_details":{"cached_tokens":1}}}`),
		"data: [DONE]\n\n",
	}
	state := NewAnthropicFromChatStreamState("claude-3")
	var collected []string
	for _, eventText := range events {
		collected = append(collected, ProcessChatCompletionsSseEventAsAnthropic(state, eventText)...)
	}
	joined := strings.Join(collected, "")
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		`"text":"早"`,
		`"name":"lookup"`,
		`"name":"second"`,
		`"partial_json":"{\"late\":"`,
		"event: message_delta",
		`"stop_reason":"tool_use"`,
		"event: message_stop",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("输出缺少 %s；实际流：\n%s", want, joined)
		}
	}
	// t2 的 buffered 参数（{"late":）应在其 content_block_start 之后补发。
	// 注意 SSE data 是 JSON 串，内部引号带反斜杠转义。
	lateStart := strings.Index(joined, `"name":"second"`)
	lateArgs := strings.Index(joined, `{\"late\":`)
	if lateStart < 0 || lateArgs < lateStart {
		t.Errorf("buffered 参数应晚于 block start 出现：\n%s", joined)
	}
	usage := covMap(t, state.Usage)
	if usage["input_tokens"] != float64(5) || usage["cache_read_input_tokens"] != float64(1) {
		t.Errorf("state.Usage = %v", state.Usage)
	}
	if state.StopReason != "tool_use" {
		t.Errorf("StopReason = %q", state.StopReason)
	}
	t.Run("错误事件形态", func(t *testing.T) {
		state := NewAnthropicFromChatStreamState("m")
		out := ProcessChatCompletionsSseEventAsAnthropic(state, "event: error\ndata: "+`{"error":{"message":"崩","code":"boom"}}`+"\n\n")
		if covSseName(t, out[0]) != "error" {
			t.Fatalf("应输出 error 事件：%v", out)
		}
		payload := covSseData(t, out[0])
		errObject := covMap(t, payload["error"])
		if covText(t, errObject["code"]) != "boom" || covText(t, errObject["type"]) != "api_error" {
			t.Errorf("error payload = %v", errObject)
		}
	})
	t.Run("response.failed 包一层错误提取", func(t *testing.T) {
		state := NewAnthropicFromChatStreamState("m")
		// 注意：错误分支按 EventName=="error" 或 data.type 匹配；这里给 data.type。
		out := ProcessChatCompletionsSseEventAsAnthropic(state, "event: response.failed\ndata: "+`{"type":"response.failed","response":{"error":{"message":"失败","type":"server_error"}}}`+"\n\n")
		if len(out) == 0 {
			t.Fatal("response.failed 应输出 error 事件")
		}
		payload := covSseData(t, out[0])
		errObject := covMap(t, payload["error"])
		if covText(t, errObject["code"]) != "server_error" {
			t.Errorf("response.error 提取 = %v", errObject)
		}
	})
	t.Run("data 缺省", func(t *testing.T) {
		state := NewAnthropicFromChatStreamState("m")
		if out := ProcessChatCompletionsSseEventAsAnthropic(state, "event: ping\n\n"); out != nil {
			t.Errorf("无 data 应返回 nil：%v", out)
		}
	})
	t.Run("上游 [DONE] 收尾", func(t *testing.T) {
		state := NewAnthropicFromChatStreamState("m")
		out := ProcessChatCompletionsSseEventAsAnthropic(state, "data: [DONE]\n\n")
		// 空内容：应插入兜底提示文本。
		joined := strings.Join(out, "")
		if !strings.Contains(joined, "上游 Chat Completions 返回了空 assistant 内容") {
			t.Errorf("空内容应转提示：%s", joined)
		}
		if !strings.Contains(joined, `"stop_reason":"end_turn"`) {
			t.Errorf("缺省 stop_reason 应为 end_turn：%s", joined)
		}
		if again := ProcessChatCompletionsSseEventAsAnthropic(state, "data: x\n\n"); again != nil {
			t.Errorf("完成后幂等：%v", again)
		}
	})
	t.Run("tool 未知名在收尾补 start", func(t *testing.T) {
		state := NewAnthropicFromChatStreamState("m")
		out := ProcessChatCompletionsSseEventAsAnthropic(state, "data: "+`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`+"\n\n")
		// 名字未到：仅触发 message_start，不产出 content_block。
		if len(out) != 1 || !strings.Contains(out[0], "message_start") {
			t.Fatalf("未声明名字前只应有 message_start：%v", out)
		}
		out = ProcessChatCompletionsSseEventAsAnthropic(state, "data: [DONE]\n\n")
		joined := strings.Join(out, "")
		if !strings.Contains(joined, `"name":"unknown_tool"`) {
			t.Errorf("收尾应补 unknown_tool：\n%s", joined)
		}
	})
	t.Run("FailAnthropicFromChatStream", func(t *testing.T) {
		state := NewAnthropicFromChatStreamState("m")
		out := FailAnthropicFromChatStream(state, "坏了", "")
		payload := covSseData(t, out[0])
		if covMap(t, payload["error"])["code"] != "upstream_error" {
			t.Errorf("缺省 code = %v", payload)
		}
		if again := FailAnthropicFromChatStream(state, "x", "y"); again != nil {
			t.Errorf("失败后幂等：%v", again)
		}
	})
}

func TestCovLegacyResponseEntries(t *testing.T) {
	t.Run("TransformOpenAIAnthropicBridgeUpstreamResponse", func(t *testing.T) {
		response := map[string]any{"content": []any{}}
		if got := TransformOpenAIAnthropicBridgeUpstreamResponse(response, BridgeTransformResponseOptions{}); got == nil {
			t.Fatal("禁用时应原样返回")
		}
		got := TransformOpenAIAnthropicBridgeUpstreamResponse(map[string]any{
			"content": "hi",
		}, BridgeTransformResponseOptions{Enabled: true, Model: "m"})
		if covText(t, got["object"]) != "chat.completion" {
			t.Errorf("启用后应转 chat.completion：%v", got)
		}
	})
	t.Run("TransformOpenAIChatToAnthropicStream 与禁用", func(t *testing.T) {
		event := "data: " + `{"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"
		if got := TransformOpenAIChatToAnthropicStream(event, BridgeTransformResponseOptions{}); got != event {
			t.Errorf("禁用应原样返回")
		}
		got := TransformOpenAIChatToAnthropicStream(event, BridgeTransformResponseOptions{Enabled: true, Model: "m"})
		if !strings.Contains(got, "content_block_delta") {
			t.Errorf("启用后应输出 anthropic SSE：%s", got)
		}
	})
	t.Run("BuildGatewayUpstreamResponse", func(t *testing.T) {
		got := BuildGatewayUpstreamResponse(201, map[string]string{"X": "1"}, []byte("body"))
		if got["status"] != 201 || got["body"] != "body" {
			t.Errorf("响应包装 = %v", got)
		}
	})
	t.Run("ExtractJSONObject", func(t *testing.T) {
		object, err := ExtractJSONObject(`{"a":1}`)
		if err != nil || object["a"] != float64(1) {
			t.Fatalf("对象解析 = %v（%v）", object, err)
		}
		if _, err := ExtractJSONObject("not-json"); err == nil {
			t.Fatal("非法 JSON 应报错")
		}
	})
	t.Run("PrepareBridgeHeaders", func(t *testing.T) {
		got := PrepareBridgeHeaders(map[string]string{"X-A": "1", "Accept": "text/plain"}, "/v1/x")
		if got["Accept"] != "application/json" {
			t.Errorf("Accept 应强制 application/json，实际 %v", got)
		}
		if got["X-A"] != "1" {
			t.Errorf("原头应保留：%v", got)
		}
	})
	t.Run("ID 工具函数", func(t *testing.T) {
		if got := normalizeAnthropicMessageID(""); got != "" {
			t.Errorf("空 id 应返回空：%q", got)
		}
		if got := normalizeAnthropicMessageID("abc"); got != "msg_abc" {
			t.Errorf("补前缀 = %q", got)
		}
		if got := normalizeAnthropicMessageID("msg_x"); got != "msg_x" {
			t.Errorf("已有前缀不变 = %q", got)
		}
		id := chatCompletionIDFromAnthropicID("")
		if !strings.HasPrefix(id, "chatcmpl_") || len(id) <= len("chatcmpl_") {
			t.Errorf("空 id 应生成随机后缀 = %q", id)
		}
		weird := chatCompletionIDFromAnthropicID("a b/c")
		if weird != "chatcmpl_a_b_c" {
			t.Errorf("分段清洗 = %q", weird)
		}
	})
}
