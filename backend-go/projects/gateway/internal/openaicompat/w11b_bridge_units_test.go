package openaicompat

// w11b 波次：桥接转换纯函数（usage 映射、Gemini JSON 转换、工具选择、
// responses 输入追加与 codex 估算）。

import (
	"strings"
	"testing"
)

func TestW11BChatUsageToGeminiUsage(t *testing.T) {
	if chatUsageToGeminiUsage(nil) != nil {
		t.Fatal("nil usage")
	}
	// 标准 chat usage + cached/reasoning 细节。
	usage := map[string]any{
		"prompt_tokens":             float64(10),
		"completion_tokens":         float64(20),
		"prompt_tokens_details":     map[string]any{"cached_tokens": float64(4)},
		"completion_tokens_details": map[string]any{"reasoning_tokens": float64(6)},
	}
	out := chatUsageToGeminiUsage(usage)
	if out["promptTokenCount"] != float64(10) || out["candidatesTokenCount"] != float64(20) || out["totalTokenCount"] != float64(30) {
		t.Fatalf("usage = %v", out)
	}
	if out["cachedContentTokenCount"] != float64(4) || out["thoughtsTokenCount"] != float64(6) {
		t.Fatalf("details = %v", out)
	}
	// input/output 命名回退 + details 回退。
	fallback := chatUsageToGeminiUsage(map[string]any{
		"input_tokens":          float64(5),
		"output_tokens":         float64(7),
		"total_tokens":          float64(11),
		"input_tokens_details":  map[string]any{"cached_tokens": float64(2)},
		"output_tokens_details": map[string]any{"reasoning_tokens": float64(3)},
	})
	if fallback["promptTokenCount"] != float64(5) || fallback["candidatesTokenCount"] != float64(7) {
		t.Fatalf("fallback = %v", fallback)
	}
	if fallback["cachedContentTokenCount"] != float64(2) || fallback["thoughtsTokenCount"] != float64(3) {
		t.Fatalf("fallback details = %v", fallback)
	}
	// 显式 total 优先；字符串 token 不被解析。
	explicit := chatUsageToGeminiUsage(map[string]any{"prompt_tokens": float64(1), "completion_tokens": float64(2), "total_tokens": float64(9)})
	if explicit["totalTokenCount"] != float64(9) {
		t.Fatalf("explicit total = %v", explicit)
	}
	stringTokens := chatUsageToGeminiUsage(map[string]any{"prompt_tokens": "1"})
	if stringTokens["promptTokenCount"] != float64(0) {
		t.Fatalf("string tokens = %v", stringTokens)
	}
	empty := chatUsageToGeminiUsage(map[string]any{})
	if empty["totalTokenCount"] != float64(0) {
		t.Fatalf("empty = %v", empty)
	}
	if _, ok := empty["cachedContentTokenCount"]; ok {
		t.Fatal("无 cached 不写字段")
	}
}

func TestW11BChatCompletionJSONToGemini(t *testing.T) {
	// 缺 model → fallback；无 choices → 空占位。
	converted := ChatCompletionJSONToGeminiGenerateContent(map[string]any{}, "gemini-fallback")
	if converted["modelVersion"] != "gemini-fallback" {
		t.Fatalf("model = %v", converted["modelVersion"])
	}
	candidates := converted["candidates"].([]any)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %+v", candidates)
	}
	if !strings.Contains(candidates[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string), "空 assistant 内容") {
		t.Fatal("空内容占位")
	}
	// 完整 choices + usage。
	full := ChatCompletionJSONToGeminiGenerateContent(map[string]any{
		"model": "m",
		"choices": []any{
			map[string]any{"message": map[string]any{"role": "assistant", "content": "回答"}, "finish_reason": "stop"},
		},
		"usage": map[string]any{"prompt_tokens": float64(1), "completion_tokens": float64(2)},
	}, "fallback")
	candidates = full["candidates"].([]any)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %+v", candidates)
	}
	if full["usageMetadata"] == nil {
		t.Fatal("usageMetadata")
	}
	// choice 为空对象 → parts 占位。
	degenerate := ChatCompletionJSONToGeminiGenerateContent(map[string]any{
		"choices": []any{map[string]any{}},
	}, "fallback")
	if len(degenerate["candidates"].([]any)) != 1 {
		t.Fatal("degenerate choice")
	}
}

func TestW11BAnthropicToolChoiceFromOpenAI(t *testing.T) {
	// 无 tool_choice → nil。
	if anthropicToolChoiceFromOpenAI(nil, false) != nil {
		t.Fatal("缺省 nil")
	}
	// "none"/"auto"。
	if choice := anthropicToolChoiceFromOpenAI("none", true); choice == nil {
		t.Fatal("none 映射")
	}
	if choice := anthropicToolChoiceFromOpenAI("auto", true); choice == nil {
		t.Fatal("auto 映射")
	}
	// "required"。
	if choice := anthropicToolChoiceFromOpenAI("required", true); choice == nil {
		t.Fatal("required 映射")
	}
	// 函数对象。
	if choice := anthropicToolChoiceFromOpenAI(map[string]any{"type": "function", "function": map[string]any{"name": "f"}}, true); choice == nil {
		t.Fatal("function 映射")
	}
	// 未知类型。
	if choice := anthropicToolChoiceFromOpenAI("bogus", true); choice != nil {
		t.Fatalf("未知类型 nil，got %v", choice)
	}
}

func TestW11BAppendResponsesInput(t *testing.T) {
	options := &BridgeRequestBodyOptions{}
	var messages []any
	var systemParts []string
	history := newOpenAIToolResultHistory()

	// 字符串输入 → user 消息。
	appendResponsesInput(&messages, &systemParts, "hello", options, history)
	if len(messages) != 1 {
		t.Fatalf("messages = %+v", messages)
	}
	// 数组输入（message 元素）：连续 user 合并为同一消息的多块。
	appendResponsesInput(&messages, &systemParts, []any{
		map[string]any{"type": "message", "role": "user", "content": "second"},
	}, options, history)
	if len(messages) != 1 || len(messages[0].(map[string]any)["content"].([]any)) != 2 {
		t.Fatalf("messages = %+v", messages)
	}
	// 对象输入（assistant 消息）。
	appendResponsesInput(&messages, &systemParts, map[string]any{"type": "message", "role": "assistant", "content": "third"}, options, history)
	if len(messages) != 2 {
		t.Fatalf("messages = %+v", messages)
	}
	// system 消息 → systemParts。
	appendResponsesInput(&messages, &systemParts, map[string]any{"type": "message", "role": "system", "content": "sys"}, options, history)
	if len(messages) != 2 || len(systemParts) != 1 {
		t.Fatalf("system = %v parts = %v", messages, systemParts)
	}
	// function_call + 输出（工具历史校验）。
	appendResponsesInput(&messages, &systemParts, map[string]any{
		"type": "function_call", "call_id": "call-1", "name": "f", "arguments": "{}",
	}, options, history)
	if len(messages) != 2 {
		t.Fatalf("function_call = %+v", messages)
	}
	appendResponsesInput(&messages, &systemParts, map[string]any{
		"type": "function_call_output", "call_id": "call-1", "output": "ok",
	}, options, history)
	if len(messages) != 3 {
		t.Fatalf("function_call_output = %+v", messages)
	}
	// 未知 call_id 的输出被忽略。
	appendResponsesInput(&messages, &systemParts, map[string]any{
		"type": "function_call_output", "call_id": "missing", "output": "x",
	}, options, history)
	if len(messages) != 3 {
		t.Fatal("未知 call_id 忽略")
	}
	// 缺 name/call_id 的 function_call 忽略。
	appendResponsesInput(&messages, &systemParts, map[string]any{"type": "function_call"}, options, history)
	if len(messages) != 3 {
		t.Fatal("缺字段忽略")
	}
	// reasoning 与 compaction 项。
	appendResponsesInput(&messages, &systemParts, map[string]any{
		"type": "reasoning", "summary": "think",
	}, options, history)
	appendResponsesInput(&messages, &systemParts, map[string]any{
		"type": "compaction", "summary": []any{map[string]any{"type": "summary_text", "text": "compact"}},
	}, options, history)
	if len(systemParts) < 3 {
		t.Fatalf("system parts = %v", systemParts)
	}
	// 未知类型与数值忽略。
	appendResponsesInput(&messages, &systemParts, map[string]any{"type": "unknown-w11b"}, options, history)
	appendResponsesInput(&messages, &systemParts, float64(7), options, history)
	if len(messages) != 3 {
		t.Fatal("未知类型忽略")
	}
}

func TestW11BChatToolChoiceToGeminiToolConfig(t *testing.T) {
	if config := chatToolChoiceToGeminiToolConfig("auto", true); config == nil {
		t.Fatal("auto 配置")
	}
	if config := chatToolChoiceToGeminiToolConfig("none", true); config == nil {
		t.Fatal("none 配置")
	}
	if config := chatToolChoiceToGeminiToolConfig("required", true); config == nil {
		t.Fatal("required 配置")
	}
	if config := chatToolChoiceToGeminiToolConfig(map[string]any{"type": "function", "function": map[string]any{"name": "f"}}, true); config == nil {
		t.Fatal("function 配置")
	}
	if config := chatToolChoiceToGeminiToolConfig("bogus", true); config == nil {
		t.Fatal("未知类型回退 AUTO 配置")
	}
	if chatToolChoiceToGeminiToolConfig("auto", false) != nil {
		t.Fatal("无 tool_choice nil")
	}
}

func TestW11BResponsesToolChoiceToChatToolChoice(t *testing.T) {
	if choice := responsesToolChoiceToChatToolChoice(map[string]any{"type": "function", "name": "f"}, nil); choice == nil {
		t.Fatal("function 映射")
	}
	if choice := responsesToolChoiceToChatToolChoice("auto", nil); choice == nil {
		t.Fatal("auto 映射")
	}
	if choice := responsesToolChoiceToChatToolChoice("bogus", nil); choice != nil {
		t.Fatalf("未知 nil，got %v", choice)
	}
}

func TestW11BGeminiToolsToAnthropicTools(t *testing.T) {
	if geminiToolsToAnthropicTools(nil) != nil {
		t.Fatal("nil nil")
	}
	if tools := geminiToolsToAnthropicTools("not-array"); tools != nil {
		t.Fatalf("非数组 nil，got %v", tools)
	}
	// functionDeclarations 形态。
	tools := geminiToolsToAnthropicTools([]any{map[string]any{
		"functionDeclarations": []any{map[string]any{"name": "f", "description": "d"}},
	}})
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", tools)
	}
	// 其它形态跳过。
	if tools := geminiToolsToAnthropicTools([]any{map[string]any{"googleSearch": map[string]any{}}}); len(tools) != 0 {
		t.Fatalf("unknown tools = %+v", tools)
	}
}

func TestW11BEstimatedBridgeOutputTokens(t *testing.T) {
	state := &CodexChatToResponsesState{OutputText: "abcdefgh", ReasoningText: "你好"}
	// join 后 ascii 9/4 -> 3，cjk 2，合计 5。
	if got := state.EstimatedBridgeOutputTokens(); got != 5 {
		t.Fatalf("estimated = %d", got)
	}
	empty := &CodexChatToResponsesState{}
	if got := empty.EstimatedBridgeOutputTokens(); got < 0 {
		t.Fatalf("empty = %d", got)
	}
}
