package openaicompatbridge

// w13c 覆盖率补充测试（bridge 域半边，自 w13c_streams_gaps_test.go 拆出）：Gemini
// 流状态机与 Gemini native source 解析边界。

import (
	"strings"
	"testing"
)

func TestW13CGeminiChatStreamArms(t *testing.T) {
	// 已完成/已失败状态短路。
	state := &GeminiChatStreamState{}
	state.Completed = true
	if got := FailGeminiChatStream(state, "m", ""); got != nil {
		t.Fatal("completed state must not fail again")
	}
	state.Failed = true
	state.Completed = false
	if got := ProcessChatCompletionsSseEventAsGemini(state, "data: {}"); got != nil {
		t.Fatal("failed state must ignore events")
	}

	// [DONE] 与解析失败。
	fresh := &GeminiChatStreamState{Model: "m"}
	if got := ProcessChatCompletionsSseEventAsGemini(fresh, "data: [DONE]"); len(got) == 0 {
		t.Fatal("[DONE] must complete the stream")
	}
	parseFail := &GeminiChatStreamState{}
	if got := ProcessChatCompletionsSseEventAsGemini(parseFail, "data: {bad"); len(got) != 1 {
		t.Fatalf("parse error must fail the stream: %v", got)
	}

	// 错误事件的三种形态。
	errorEvent := &GeminiChatStreamState{}
	got := ProcessChatCompletionsSseEventAsGemini(errorEvent,
		"event: error\ndata: {\"error\":{\"message\":\"失败\",\"code\":\"bad\"}}")
	if len(got) != 1 || !strings.Contains(got[0], "失败") {
		t.Fatalf("error event = %v", got)
	}
	responseFailed := &GeminiChatStreamState{}
	got = ProcessChatCompletionsSseEventAsGemini(responseFailed,
		"data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"type\":\"upstream\"}}}")
	if len(got) != 1 || !strings.Contains(got[0], "upstream") {
		t.Fatalf("response.failed = %v", got)
	}
	bareError := &GeminiChatStreamState{}
	got = ProcessChatCompletionsSseEventAsGemini(bareError, "event: error\ndata: {\"oops\":1}")
	if len(got) != 1 || !strings.Contains(got[0], "上游 Chat Completions SSE 返回错误事件") {
		t.Fatalf("bare error data = %v", got)
	}

	// 非 choice 事件（无 delta）保持静默。
	quiet := &GeminiChatStreamState{}
	if got := ProcessChatCompletionsSseEventAsGemini(quiet, "data: {\"model\":\"m2\"}"); len(got) != 0 {
		t.Fatalf("model-only event = %v", got)
	}

	// 完成事件带 usage。
	withUsage := &GeminiChatStreamState{}
	withUsage.Usage = map[string]any{"totalTokenCount": 3}
	completed := CompleteGeminiChatStream(withUsage)
	if len(completed) == 0 || !strings.Contains(completed[len(completed)-1], "usageMetadata") {
		t.Fatalf("usage completion = %v", completed)
	}
}

func TestW13CGeminiNativeSourceArms(t *testing.T) {
	// responseMimeType / responseSchema 的非对象与非法形态。
	if got := responseMimeTypeFromSourceForGeminiNative(map[string]any{}); got != "" {
		t.Fatalf("empty body mime = %q", got)
	}
	if got := responseMimeTypeFromSourceForGeminiNative(map[string]any{
		"response_format": map[string]any{"type": "json_object"},
	}); got != "application/json" {
		t.Fatalf("json_object mime = %q", got)
	}
	if got := responseMimeTypeFromSourceForGeminiNative(map[string]any{
		"text": map[string]any{"format": map[string]any{"type": "json_schema"}},
	}); got != "application/json" {
		t.Fatalf("text.format mime = %q", got)
	}
	if got := responseMimeTypeFromSourceForGeminiNative(map[string]any{
		"response_format": "not-an-object",
	}); got != "" {
		t.Fatalf("non-object format = %q", got)
	}
	if got := responseSchemaFromSourceForGeminiNative(map[string]any{}); got != nil {
		t.Fatalf("empty body schema = %v", got)
	}
	if got := responseSchemaFromSourceForGeminiNative(map[string]any{
		"response_format": map[string]any{"json_schema": map[string]any{"schema": "not-object"}},
	}); got != nil {
		t.Fatalf("non-object schema = %v", got)
	}
	schema := map[string]any{"type": "object"}
	if got := responseSchemaFromSourceForGeminiNative(map[string]any{
		"response_format": map[string]any{"json_schema": map[string]any{"schema": schema}},
	}); got == nil {
		t.Fatal("valid snake schema must resolve")
	}
	if got := responseSchemaFromSourceForGeminiNative(map[string]any{
		"text": map[string]any{"format": map[string]any{"json_schema": map[string]any{"schema": schema}}},
	}); got == nil {
		t.Fatal("valid text.format schema must resolve")
	}
	// source 内容到 Gemini parts 的非映射形态。
	parts, err := openAIContentToGeminiPartsForGeminiNative("plain text", BridgeRequestBodyOptions{})
	if err != nil || len(parts) == 0 {
		t.Fatalf("string content parts = %v err=%v", parts, err)
	}
	parts, err = openAIContentToGeminiPartsForGeminiNative([]any{nil, 5}, BridgeRequestBodyOptions{})
	if err != nil || len(parts) != 0 {
		t.Fatalf("invalid array content = %v err=%v", parts, err)
	}
	if _, err := openAIContentToGeminiPartsForGeminiNative(5, BridgeRequestBodyOptions{}); err == nil {
		t.Fatal("numeric content must fail")
	}
}
