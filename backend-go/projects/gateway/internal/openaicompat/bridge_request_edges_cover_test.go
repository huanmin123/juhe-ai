package openaicompat

// bridge_request.go 剩余小分支的直连覆盖：nil item 跳过、缺省折叠、
// 枚举分支与纯助手函数。

import (
	"testing"
)

func TestCovBridgeRequestEdgeBranches(t *testing.T) {
	t.Run("anthropicThinking 预算不抬高", func(t *testing.T) {
		output := map[string]any{"max_tokens": int64(8000)}
		applyAnthropicThinkingFromOpenAIReasoning(output, map[string]any{"reasoning_effort": "low"})
		if output["max_tokens"] != int64(8000) {
			t.Errorf("预算小于 max_tokens 时不应抬高：%v", output["max_tokens"])
		}
	})
	t.Run("legacy 校验仅限 chat 家族", func(t *testing.T) {
		if err := validateOpenAIToAnthropicLegacyChatFunctionFields(FamilyResponses, map[string]any{"functions": []any{}}); err != nil {
			t.Errorf("非 chat 家族不应校验 legacy 字段：%v", err)
		}
		if err := validateOpenAIToAnthropicLegacyChatFunctionFields(FamilyChatCompletions, map[string]any{}); err != nil {
			t.Errorf("无 legacy 字段应通过：%v", err)
		}
	})
	t.Run("chat content 边界", func(t *testing.T) {
		// 空消息内容折叠空 text block。
		blocks, err := openAIChatContentToAnthropicBlocks(nil, nil)
		if err != nil || blocks != nil {
			t.Errorf("非数组内容应返回 nil blocks：%v/%v", blocks, err)
		}
		if _, err := openAIChatContentToAnthropicBlocks([]any{map[string]any{"text": "无类型"}}, nil); err == nil {
			t.Error("无类型 part 应报 unsupported")
		}
		if got := imageURLFromOpenAIImagePart(42); got != "" {
			t.Errorf("非字符串非对象 url = %q", got)
		}
	})
	t.Run("解析器错误透传", func(t *testing.T) {
		resolver := &covFakeResolver{err: errTestBoom}
		_, err := anthropicDocumentBlockFromOpenAIFileID("file-9", &BridgeRequestBodyOptions{FileResolver: resolver})
		if err != errTestBoom {
			t.Errorf("解析器错误应透传：%v", err)
		}
	})
	t.Run("tool 相关纯函数", func(t *testing.T) {
		if got := chatToolCallsToAnthropicToolUseBlocks("not-array"); got != nil {
			t.Errorf("非数组 tool_calls = %v", got)
		}
		blocks := chatToolCallsToAnthropicToolUseBlocks([]any{nil, map[string]any{"id": "x"}})
		if len(blocks) != 0 {
			t.Errorf("缺 name/id 的 tool_call 应跳过：%v", blocks)
		}
		applyTools(map[string]any{}, nil, nil, false)
		// 空 tools 不写任何键（不 panic 即通过）。
		if got := anthropicToolChoiceFromOpenAI(map[string]any{"type": "function"}, true); got != nil {
			t.Errorf("function 缺 name 应 nil：%v", got)
		}
		if got := anthropicToolChoiceFromOpenAI(map[string]any{"type": "function", "name": "n"}, true); got == nil {
			t.Error("typed.name 应回退")
		}
		if got := anthropicToolChoiceFromOpenAI(map[string]any{"type": "allowed_tools", "tools": []any{}}, true); got == nil {
			t.Error("allowed_tools 默认应 auto")
		}
		if got := anthropicToolChoiceFromOpenAI(map[string]any{"type": "mystery"}, true); got != nil {
			t.Errorf("未知类型应 nil：%v", got)
		}
		if got := anthropicToolChoiceFromOpenAI(3.14, true); got != nil {
			t.Errorf("数字类型应 nil：%v", got)
		}
		allowed := allowedOpenAIFunctionTools(map[string]any{
			"type":  "allowed_tools",
			"tools": []any{"a", nil, map[string]any{"type": "web"}, map[string]any{"name": "b"}, map[string]any{"type": "function", "function": map[string]any{"name": "c"}}},
		})
		for name, want := range map[string]bool{"a": true, "b": false, "c": true, "d": false} {
			if got := openAIFunctionToolAllowed(allowed, name); got != want {
				t.Errorf("allowed[%s] = %v，期望 %v", name, got, want)
			}
		}
		if !openAIFunctionToolAllowed(nil, "any") {
			t.Error("nil allowed 应放行所有")
		}
		if err := unsupportedOpenAIToolError("not-map"); err == nil {
			t.Error("非 map 工具应报 unknown")
		}
	})
	t.Run("responses 文本提取", func(t *testing.T) {
		if got := responsesReasoningTextFromItem(map[string]any{"content": "内容优先"}); got != "内容优先" {
			t.Errorf("content 回退 = %q", got)
		}
		if got := responsesReasoningTextFromItem(map[string]any{"text": "  纯文本  "}); got != "纯文本" {
			t.Errorf("text 提取 = %q", got)
		}
		if got := responsesCompactionSummaryTextFromItem(map[string]any{"content": "摘要正文"}); got != "摘要正文" {
			t.Errorf("compaction content 回退 = %q", got)
		}
	})
	t.Run("工具历史与系统文本", func(t *testing.T) {
		history := newOpenAIToolResultHistory()
		rememberAnthropicToolUseBlocks(history, []any{
			nil,
			map[string]any{"type": "text"},
			map[string]any{"type": "tool_use", "id": ""},
			map[string]any{"type": "tool_use", "id": "t1"},
		})
		if _, err := validateOpenAIToolResultHistory(history, "t1"); err != nil {
			t.Errorf("tool_use 应被记住：%v", err)
		}
		if hasOwnKey(nil, "x") {
			t.Error("nil 对象 hasOwnKey 应 false")
		}
		parts := []string{}
		appendSystemText(&parts, "  ")
		if len(parts) != 0 {
			t.Errorf("空白系统文本不应追加：%v", parts)
		}
		// 名字前缀：首块 text 为空时只写前缀。
		blocks := []any{map[string]any{"type": "text", "text": ""}}
		withOpenAIChatMessageNamePrefix(blocks, "甲")
		if got := blocks[0].(map[string]any)["text"]; got != "参与者: 甲" {
			t.Errorf("空 text 应替换为前缀：%v", got)
		}
	})
	t.Run("anthropic->chat 空项与枚举", func(t *testing.T) {
		output, err := BuildAnthropicMessagesToChatCompletionsBody(map[string]any{
			"messages": []any{nil, map[string]any{"role": "user", "content": []any{
				nil,
				map[string]any{"type": "text", "text": "文"},
			}}},
			"system":         []any{nil, map[string]any{"type": "text", "text": "S"}},
			"tools":          []any{nil, map[string]any{"name": "f"}},
			"tool_choice":    map[string]any{"type": "any"},
			"stop_sequences": []any{},
		}, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		messages := covSlice(t, output["messages"])
		if len(messages) != 2 {
			t.Fatalf("期望 system + user 两条，实际 %v", messages)
		}
		if _, exists := output["stop"]; exists {
			t.Errorf("空 stop_sequences 不应输出 stop：%v", output["stop"])
		}
		// base64 图缺 data 时错误从 user 消息循环向上抛。
		if _, err := BuildAnthropicMessagesToChatCompletionsBody(map[string]any{
			"messages": []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "image", "source": map[string]any{"type": "base64"}},
			}}},
		}, BridgeRequestBodyOptions{}); err == nil {
			t.Error("base64 图缺 data 应报错")
		}
		// assistant 空项与 redacted_thinking。
		if _, err := BuildAnthropicMessagesToChatCompletionsBody(map[string]any{
			"messages": []any{nil, map[string]any{"role": "assistant", "content": []any{
				nil,
				map[string]any{"type": "redacted_thinking", "data": "x"},
			}}},
		}, BridgeRequestBodyOptions{}); err == nil {
			t.Error("redacted_thinking 应拒绝")
		}
		// 非 text block 的 tool_result content 被过滤。
		result := anthropicContentText([]any{nil, map[string]any{"type": "image"}, map[string]any{"type": "text", "text": "t"}})
		if result != "t" {
			t.Errorf("anthropicContentText = %q", result)
		}
	})
	t.Run("gemini 助手函数", func(t *testing.T) {
		if got := sanitizeGeminiToolCallIDName("get weather!?"); got != "get_weather__" {
			t.Errorf("非法字符清洗 = %q", got)
		}
		long := sanitizeGeminiToolCallIDName(stringsRepeatForTest("x", 60))
		if len(long) != 48 {
			t.Errorf("超长应截断 48：%d", len(long))
		}
		if got := sanitizeGeminiToolCallIDName("!?"); got != "__" {
			t.Errorf("全非法字符替换为下划线：%q", got)
		}
		if got := int64ToText(0); got != "0" {
			t.Errorf("0 = %q", got)
		}
		if got := int64ToText(-42); got != "-42" {
			t.Errorf("负数 = %q", got)
		}
	})
	t.Run("gemini 输入 nil 项跳过", func(t *testing.T) {
		output, err := BuildGeminiGenerateContentToChatCompletionsBody(map[string]any{
			"systemInstruction": map[string]any{"parts": []any{nil, map[string]any{"text": "S"}}},
			"contents": []any{
				nil,
				map[string]any{"role": "model", "parts": []any{
					nil,
					map[string]any{"functionCall": map[string]any{"name": "f", "args": nil}},
				}},
				map[string]any{"role": "user", "parts": []any{
					nil,
					map[string]any{"functionResponse": map[string]any{"name": "f", "response": nil}},
				}},
			},
			"tools": []any{nil, map[string]any{"functionDeclarations": []any{nil, map[string]any{"name": "d"}}, "ignored": nil}},
		}, BridgeRequestBodyOptions{DefaultModel: "m"})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		messages := covSlice(t, output["messages"])
		if len(messages) != 3 {
			t.Fatalf("期望 3 条消息，实际 %d：%v", len(messages), messages)
		}
		assistant := covMap(t, messages[1])
		calls := covSlice(t, assistant["tool_calls"])
		if covText(t, covMap(t, calls[0])["id"]) != "call_f_0" {
			t.Errorf("nil args 应回退且 id 稳定：%v", calls[0])
		}
		tool := covMap(t, messages[2])
		if covText(t, tool["content"]) != "{}" {
			t.Errorf("nil response 折叠 {}：%v", tool["content"])
		}
		// ignored 键值为 nil 不算 unsupported 原生工具。
		if _, exists := output["tools"]; !exists {
			t.Error("声明工具应输出")
		}
	})
	t.Run("chat->gemini 空项跳过", func(t *testing.T) {
		output, err := BuildOpenAIChatToGeminiBody(map[string]any{
			"messages": []any{nil, map[string]any{"role": "assistant", "tool_calls": []any{
				nil,
				map[string]any{"id": "x", "function": map[string]any{"arguments": "{}"}},
			}}},
			"tools":       []any{nil, "not-map", map[string]any{"type": "not-function"}},
			"tool_choice": "bogus",
		}, BridgeRequestBodyOptions{})
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		contents := covSlice(t, output["contents"])
		if len(contents) != 1 {
			t.Fatalf("期望 1 个 content（无名 tool_call 跳过，空 parts 兜底），实际 %v", contents)
		}
		if _, exists := output["tools"]; exists {
			t.Error("全部非法工具不应输出 tools")
		}
	})
	t.Run("codex 请求边角", func(t *testing.T) {
		// 工具重名超过一次的后缀递增。
		plan := responsesToolsToChatToolPlan([]any{
			nil,
			map[string]any{"type": "function"},
			map[string]any{"type": "function", "name": "f"},
			map[string]any{"type": "function", "name": "f"},
			map[string]any{"type": "function", "name": "f"},
		})
		names := []string{}
		for _, tool := range plan.chatTools {
			names = append(names, covText(t, covMap(t, tool)["function"].(map[string]any)["name"]))
		}
		if stringsJoinForTest(names) != "f,f_2,f_3" {
			t.Errorf("重名后缀 = %v", names)
		}
		// 空 name 的 function_call_output 无对应项。
		message := unsupportedToolsSystemMessage(nil)
		if message != nil {
			t.Errorf("空列表应为 nil：%v", message)
		}
		if got := CodexResponsesChatBridgeToolAdaptersFromClientBody(map[string]any{}); len(got) != 0 {
			t.Errorf("无 tools 应为空映射：%v", got)
		}
	})
	t.Run("coalesce 与 stop 值", func(t *testing.T) {
		merged := coalesceAdjacentSystemMessages([]any{
			nil,
			map[string]any{"role": "system", "content": "a"},
			"not-map",
			map[string]any{"role": "system", "content": ""},
			map[string]any{"role": "user", "content": "u"},
		})
		if len(merged) != 2 {
			t.Fatalf("期望 system + user 两条，实际 %v", merged)
		}
		if got, ok := merged[0].(map[string]any)["content"].(string); !ok || got != "a" {
			t.Errorf("空 next 不应加分隔符：%v", merged[0])
		}
	})
}

// errTestBoom 测试专用哨兵错误。
var errTestBoom = errBoomer{}

type errBoomer struct{}

func (errBoomer) Error() string { return "boom" }

func stringsRepeatForTest(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func stringsJoinForTest(items []string) string {
	out := ""
	for i, item := range items {
		if i > 0 {
			out += ","
		}
		out += item
	}
	return out
}
