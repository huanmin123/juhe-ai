package openaicompatbridge

// w11b 波次 bridge 域半边（自 w11b_units_test.go 拆出）：角色/内容映射、token
// 估算、computer adapter 截断、hosted tool 详情、finish reason 往返与杂项辅助。

import (
	"strings"
	"testing"
)

func TestW11BBridgeRoleAndContentHelpers(t *testing.T) {
	if chatRoleForResponsesRole("user") != "user" || chatRoleForResponsesRole("assistant") != "assistant" {
		t.Fatal("基础角色")
	}
	if chatRoleForResponsesRole("system") != "system" || chatRoleForResponsesRole("developer") != "system" {
		t.Fatal("system 映射")
	}
	if chatRoleForResponsesRole("tool") != "" || chatRoleForResponsesRole(float64(1)) != "" {
		t.Fatal("未知角色空")
	}
	if responsesMessageItemAsChatMessage(map[string]any{"role": "tool"}) != nil {
		t.Fatal("未知角色消息 nil")
	}
	message := responsesMessageItemAsChatMessage(map[string]any{"role": "user", "content": "hi"})
	if message["role"] != "user" || message["content"] != "hi" {
		t.Fatalf("message = %v", message)
	}
	if responsesContentFromValue("text") != "text" {
		t.Fatal("字符串内容")
	}
	if responsesContentFromValue(map[string]any{}) != "" {
		t.Fatal("空对象内容空串")
	}
	if responsesTextFromValue("s") != "s" || responsesTextFromValue(map[string]any{"text": "t"}) != "t" {
		t.Fatal("responsesTextFromValue")
	}
}

func TestW11BBridgeTokenEstimate(t *testing.T) {
	if bridgeEstimateTokenCountFromText("") != 0 || bridgeEstimateTokenCountFromText("   ") != 0 {
		t.Fatal("空文本 0")
	}
	if got := bridgeEstimateTokenCountFromText("abcdefgh"); got != 2 {
		t.Fatalf("ascii = %d", got)
	}
	if got := bridgeEstimateTokenCountFromText("你好吗"); got != 3 {
		t.Fatalf("cjk = %d", got)
	}
	if got := bridgeEstimateTokenCountFromText("±±"); got != 1 {
		t.Fatalf("other = %d", got)
	}
	if bridgeCeilDiv(0, 0) != 0 || bridgeCeilDiv(5, 0) != 0 {
		t.Fatal("除数 0 → 0")
	}
	if bridgeCeilDiv(9, 4) != 3 || bridgeCeilDiv(8, 4) != 2 {
		t.Fatal("向上取整")
	}
	if !bridgeIsCjkCodePoint(0x4e2d) || bridgeIsCjkCodePoint('a') {
		t.Fatal("CJK 判定")
	}
}
func TestW11BComputerAdapterAndMiscHelpers(t *testing.T) {
	if got := truncateString(strings.Repeat("x", 300), 20); len([]rune(got)) != 20 {
		t.Fatalf("truncate = %d", len(got))
	}
	if truncateString("short", 20) != "short" {
		t.Fatal("短串原样")
	}
	if firstAdapterString(nil) != nil {
		t.Fatal("nil 入参 nil")
	}
	if value := firstAdapterString("a"); value == nil || *value != "a" {
		t.Fatal("字符串入参")
	}
	if value := firstAdapterString(nil, "", "b"); value == nil || *value != "b" {
		t.Fatal("跳过空值")
	}
	if number, ok := parseJSNumber("42.5"); !ok || number != 42.5 {
		t.Fatal("parseJSNumber 数值")
	}
	if _, ok := parseJSNumber("bad"); ok {
		t.Fatal("非法数值 false")
	}
	if _, ok := parseJSNumber("  "); ok {
		t.Fatal("空串 false")
	}
}
func TestW11BHostedToolCompatibilityDetail(t *testing.T) {
	if detail := hostedToolCompatibilityDetail(OpenAIHostedToolCodeInterpreter); detail == "" {
		t.Fatal("code interpreter 详情非空")
	}
	if detail := hostedToolCompatibilityDetail(OpenAIHostedToolComputer); detail == "" {
		t.Fatal("computer 详情非空")
	}
	if detail := hostedToolCompatibilityDetail(OpenAIHostedToolMCP); detail == "" {
		t.Fatal("mcp 详情非空")
	}
	if detail := hostedToolCompatibilityDetail(OpenAIHostedToolRuntimeType("unknown-w11b")); detail == "" {
		t.Fatal("未知工具给出通用提示")
	}
}

func TestW11BResponsesCompactionSummaryText(t *testing.T) {
	if text := responsesCompactionSummaryTextFromItem(map[string]any{}); text != "" {
		t.Fatalf("空对象摘要 = %q", text)
	}
	if text := responsesCompactionSummaryTextFromItem(map[string]any{"summary": "summary-text"}); text != "summary-text" {
		t.Fatalf("字符串 summary = %q", text)
	}
	if text := responsesCompactionSummaryTextFromItem(map[string]any{"content": "content-text"}); text != "content-text" {
		t.Fatalf("content 回退 = %q", text)
	}
}

func TestW11BCodexMergeChatToolName(t *testing.T) {
	if name := codexMergeChatToolName("", "fn"); name != "fn" {
		t.Fatalf("空当前名 = %q", name)
	}
	if name := codexMergeChatToolName("base", "base"); name != "base" {
		t.Fatalf("同名 = %q", name)
	}
	if name := codexMergeChatToolName("base.fn", "fn"); name != "base.fn" {
		t.Fatalf("已有后缀 = %q", name)
	}
}

func TestW11BGeminiFinishReasonRoundTrip(t *testing.T) {
	if got := chatFinishReasonToGeminiFinishReason("stop", nil); got != "STOP" {
		t.Fatalf("stop = %q", got)
	}
	if got := chatFinishReasonToGeminiFinishReason("length", nil); got != "MAX_TOKENS" {
		t.Fatalf("length = %q", got)
	}
	if got := chatFinishReasonToGeminiFinishReason("content_filter", nil); got != "SAFETY" {
		t.Fatalf("filter = %q", got)
	}
	if got := chatFinishReasonToGeminiFinishReason("bogus", nil); got != "STOP" {
		t.Fatalf("默认 = %q", got)
	}
	if got := geminiFinishReasonToChatFinishReason("STOP"); got != "stop" {
		t.Fatalf("STOP = %q", got)
	}
	if got := geminiFinishReasonToChatFinishReason("MAX_TOKENS"); got != "length" {
		t.Fatalf("MAX_TOKENS = %q", got)
	}
	if got := geminiFinishReasonToChatFinishReason("SAFETY"); got != "content_filter" {
		t.Fatalf("SAFETY = %q", got)
	}
	if got := geminiFinishReasonToChatFinishReason("OTHER"); got != "stop" {
		t.Fatalf("OTHER = %q", got)
	}
	if got := finishReasonFromGeminiCandidate(nil); got != "stop" {
		t.Fatalf("nil candidate = %q", got)
	}
	if got := finishReasonFromGeminiCandidate(map[string]any{"finishReason": "STOP"}); got != "stop" {
		t.Fatalf("candidate = %q", got)
	}
	if got := geminiChatFinishReason(GeminiResponseSummary{FinishReason: "STOP"}); got != "stop" {
		t.Fatalf("chat finish = %q", got)
	}
	if got := geminiChatFinishReason(GeminiResponseSummary{FinishReason: "MAX_TOKENS"}); got != "length" {
		t.Fatalf("chat length = %q", got)
	}
	if got := geminiChatFinishReason(GeminiResponseSummary{FinishReason: "SAFETY"}); got != "content_filter" {
		t.Fatalf("chat safety = %q", got)
	}
	if got := geminiChatFinishReason(GeminiResponseSummary{FunctionCalls: []map[string]any{{"name": "f"}}}); got != "tool_calls" {
		t.Fatalf("chat tools = %q", got)
	}
	if got := geminiAnthropicStopReason("end_turn"); got != "end_turn" {
		t.Fatalf("anthropic stop = %q", got)
	}
	if got := geminiAnthropicStopReason("MAX_TOKENS"); got != "max_tokens" {
		t.Fatalf("anthropic max_tokens = %q", got)
	}
	if got := geminiAnthropicStopReason("SAFETY"); got != "stop_sequence" {
		t.Fatalf("anthropic safety = %q", got)
	}
}

func TestW11BAnthropicResponseIDFromAnthropicID(t *testing.T) {
	if got := anthropicResponseIDFromAnthropicID("msg_x"); got != "resp_msg_x" {
		t.Fatalf("passthrough = %q", got)
	}
	if got := anthropicResponseIDFromAnthropicID(""); got == "" {
		t.Fatal("缺省生成")
	}
}
