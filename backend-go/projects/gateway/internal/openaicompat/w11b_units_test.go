package openaicompat

// w11b 波次：bridge 纯函数（角色/内容映射、token 估算、阈值、媒体类型）、
// image generation 解析、finish reason 往返与杂项辅助。

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

func TestW11BBridgeExecutorsHelpers(t *testing.T) {
	threshold := 0.5
	options := map[string]any{"score_threshold": threshold}
	if value := scoreThresholdFromRankingOptions(options); value == nil || *value != 0.5 {
		t.Fatalf("threshold = %v", value)
	}
	if scoreThresholdFromRankingOptions(nil) != nil {
		t.Fatal("nil 选项 nil")
	}
	if scoreThresholdFromRankingOptions(map[string]any{}) != nil {
		t.Fatal("缺阈值 nil")
	}
	if !isTextBridgeMediaType("text/plain") || !isTextBridgeMediaType("text/plain; charset=utf-8") {
		t.Fatal("文本媒体类型")
	}
	if isTextBridgeMediaType("") || isTextBridgeMediaType("application/json") {
		t.Fatal("非文本媒体类型")
	}
}

func TestW11BImageGenerationParseHelpers(t *testing.T) {
	// safeParseJSON：空串/坏 JSON 折叠空对象。
	if object := safeParseJSON("bad").(map[string]any); len(object) != 0 {
		t.Fatal("坏 JSON 空对象")
	}
	if object := safeParseJSON("").(map[string]any); len(object) != 0 {
		t.Fatal("空串空对象")
	}
	if object := safeParseJSON(`{"a":1}`).(map[string]any); object["a"] != float64(1) {
		t.Fatal("对象解析")
	}
	// imageGenerationOutputItemFrom。
	if imageGenerationOutputItemFrom(nil) != nil {
		t.Fatal("nil 输出 nil")
	}
	if imageGenerationOutputItemFrom(map[string]any{}) != nil {
		t.Fatal("缺 output nil")
	}
	if imageGenerationOutputItemFrom(map[string]any{"output": []any{map[string]any{"type": "other"}}}) != nil {
		t.Fatal("无匹配项 nil")
	}
	item := imageGenerationOutputItemFrom(map[string]any{"output": []any{map[string]any{"type": "image_generation_call", "id": "ig"}}})
	if item == nil || item["id"] != "ig" {
		t.Fatalf("item = %v", item)
	}
	// imageGenerationResultFromJSON：nil 缺 b64 → 错误。
	if _, err := imageGenerationResultFromJSON(nil, "png"); err == nil {
		t.Fatal("nil JSON 缺 b64 应报错")
	}
	result, err := imageGenerationResultFromJSON(map[string]any{"data": []any{map[string]any{"b64_json": "Zm9v"}}}, "png")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ImageBase64 == "" {
		t.Fatalf("result = %+v", result)
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
	if mediaTypePointer(&uploadedFile{}) != nil {
		t.Fatal("无媒体类型 nil")
	}
	if value := mediaTypePointer(&uploadedFile{HasMedia: true, MediaType: "text/plain"}); value == nil || *value != "text/plain" {
		t.Fatal("媒体类型透传")
	}
	// newFileObject 非法时间戳。
	badTime := "not-a-time"
	if _, err := newFileObject(FileRecord{ID: "f", CreatedAt: badTime}); err == nil {
		t.Fatal("非法时间戳报错")
	}
	expires := "not-a-time"
	if _, err := newFileObject(FileRecord{ID: "f", CreatedAt: "2026-01-02T03:04:05Z", ExpiresAt: &expires}); err == nil {
		t.Fatal("非法过期时间报错")
	}
	if _, err := newContainerFileObject(FileRecord{CreatedAt: badTime}); err == nil {
		t.Fatal("容器文件非法时间戳报错")
	}
	object, err := newFileObject(FileRecord{ID: "f", CreatedAt: "2026-01-02T03:04:05Z", Bytes: 3, Filename: "a.txt", Purpose: "assistants", Status: "processed"})
	if err != nil || object.ID != "f" || object.Object != "file" {
		t.Fatalf("object = %+v err = %v", object, err)
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
