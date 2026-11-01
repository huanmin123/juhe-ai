package main

// T86（深挖复审遗留项）钉住测试：其余流式桥方向 + 非流式桥接臂的 usage 快照
// 供给，复用 DD-C6 已建的 chainBridgeUsageSlot/carrier 通道与 DD-RC-C1 语义：
//   - 每方向桥接流 usage 快照 = 桥内计算值（input/output/cache 三族）；
//   - 上游未发 usage（桥状态 / 观测值为 nil）时不写快照，结算回落既有估算
//     兜底（非 0 计费）；发了 usage 但全 0 是上游事实，照常快照计 0；
//   - 非流式缓冲臂在缓冲变换前从上游原始 JSON 按账户协议形态提取 usage 写入
//     本次 attempt 槽（nonstream 结算面消费入口见 gatewayresponse 侧测试）。

import (
	"bytes"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/openaicompat/openaicompatbridge"
)

// ---- 流式：gemini native 上游 → chat 客户端 ----

// ddT86GeminiNativeSSE 是 gemini native 上游流（usageMetadata 携带四族）。
const ddT86GeminiNativeSSE = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}],\"role\":\"model\"}}]}\n\n" +
	"data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":20,\"thoughtsTokenCount\":7,\"cachedContentTokenCount\":5,\"totalTokenCount\":37}}\n\n"

// ddT86GeminiNativeSSENoUsage 是不带 usageMetadata 的 gemini native 上游流。
const ddT86GeminiNativeSSENoUsage = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}],\"role\":\"model\"}}]}\n\n" +
	"data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n"

// TestDDT86GeminiNativeToChatPumpUsageSnapshot 钉住 gemini native 上游 → chat
// 客户端方向：快照 = 桥内计算值（state.Usage 为 geminiUsageToChatUsage 渲染的
// chat 形，input/output/cache 三族与客户端 usage chunk 同值）。
func TestDDT86GeminiNativeToChatPumpUsageSnapshot(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	slot := &chainBridgeUsageSlot{}
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "chat_completions"},
		openaicompatbridge.FamilyChatCompletions, openaicompatbridge.FamilyGeminiGenerateContent, "m", slot)
	if pump == nil {
		t.Fatal("gemini→chat pump 缺失")
	}
	if err := pump(strings.NewReader(ddT86GeminiNativeSSE), &bytes.Buffer{}); err != nil {
		t.Fatalf("pump: %v", err)
	}
	usage, ok := slot.loadUsage()
	if !ok {
		t.Fatal("桥泵完成后快照缺失")
	}
	if gatewayproto.Token(usage.InputTokens) != 10 {
		t.Fatalf("InputTokens = %d, want 10（promptTokenCount 经桥转 chat 形 prompt_tokens）", gatewayproto.Token(usage.InputTokens))
	}
	if gatewayproto.Token(usage.OutputTokens) != 20 {
		t.Fatalf("OutputTokens = %d, want 20（candidatesTokenCount → completion_tokens）", gatewayproto.Token(usage.OutputTokens))
	}
	if gatewayproto.Token(usage.CacheReadTokens) != 5 {
		t.Fatalf("CacheReadTokens = %d, want 5（cachedContentTokenCount → prompt_tokens_details.cached_tokens）", gatewayproto.Token(usage.CacheReadTokens))
	}
}

// TestDDT86GeminiNativeToChatPumpNilUsageNoSnapshot 同方向上游未发 usage：
// state.Usage 保持 nil，不写快照（回落估算兜底）。
func TestDDT86GeminiNativeToChatPumpNilUsageNoSnapshot(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	slot := &chainBridgeUsageSlot{}
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "chat_completions"},
		openaicompatbridge.FamilyChatCompletions, openaicompatbridge.FamilyGeminiGenerateContent, "m", slot)
	if err := pump(strings.NewReader(ddT86GeminiNativeSSENoUsage), &bytes.Buffer{}); err != nil {
		t.Fatalf("pump: %v", err)
	}
	if _, ok := slot.loadUsage(); ok {
		t.Fatal("上游未发 usage 时槽上不应有快照")
	}
}

// ---- 流式：anthropic messages 上游 → gemini 客户端 ----

// ddT86AnthropicSSEWithThinking 在 message_delta usage 携带 thinking_tokens。
const ddT86AnthropicSSEWithThinking = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100,\"cache_read_input_tokens\":30}}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":50,\"thinking_tokens\":7}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// TestDDT86AnthropicToGeminiPumpUsageSnapshot 钉住 anthropic messages 上游 →
// gemini 客户端方向：快照为 anthropicUsageToGeminiUsage 渲染的 usageMetadata
// 形终值，口径同 gatewaygemini 提取（Input=promptTokenCount 含 cache 求和、
// Output=candidates+thoughts、CacheRead=cachedContentTokenCount、Thinking 单列）。
func TestDDT86AnthropicToGeminiPumpUsageSnapshot(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	slot := &chainBridgeUsageSlot{}
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "gemini_generate_content"},
		openaicompatbridge.FamilyGeminiGenerateContent, openaicompatbridge.FamilyAnthropicMessages, "m", slot)
	if pump == nil {
		t.Fatal("anthropic→gemini pump 缺失")
	}
	if err := pump(strings.NewReader(ddT86AnthropicSSEWithThinking), &bytes.Buffer{}); err != nil {
		t.Fatalf("pump: %v", err)
	}
	usage, ok := slot.loadUsage()
	if !ok {
		t.Fatal("桥泵完成后快照缺失")
	}
	if gatewayproto.Token(usage.InputTokens) != 130 {
		t.Fatalf("InputTokens = %d, want 130（promptTokenCount = input 100 + cache_read 30）", gatewayproto.Token(usage.InputTokens))
	}
	if gatewayproto.Token(usage.OutputTokens) != 57 {
		t.Fatalf("OutputTokens = %d, want 57（candidates 50 + thoughts 7，gemini 口径）", gatewayproto.Token(usage.OutputTokens))
	}
	if gatewayproto.Token(usage.CacheReadTokens) != 30 {
		t.Fatalf("CacheReadTokens = %d, want 30（cachedContentTokenCount）", gatewayproto.Token(usage.CacheReadTokens))
	}
	if gatewayproto.Token(usage.ThinkingTokens) != 7 {
		t.Fatalf("ThinkingTokens = %d, want 7（thoughtsTokenCount）", gatewayproto.Token(usage.ThinkingTokens))
	}
}

// TestDDT86AnthropicToGeminiPumpNilUsageNoSnapshot 同方向上游未发 usage：
// anthropicUsageToGeminiUsage 双 nil 返回 nil，槽上无快照。
func TestDDT86AnthropicToGeminiPumpNilUsageNoSnapshot(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	slot := &chainBridgeUsageSlot{}
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "gemini_generate_content"},
		openaicompatbridge.FamilyGeminiGenerateContent, openaicompatbridge.FamilyAnthropicMessages, "m", slot)
	if err := pump(strings.NewReader(ddRC1AnthropicSSENoUsage), &bytes.Buffer{}); err != nil {
		t.Fatalf("pump: %v", err)
	}
	if _, ok := slot.loadUsage(); ok {
		t.Fatal("上游未发 usage 时槽上不应有快照")
	}
}

// ---- 流式：gemini native 上游 → messages 客户端（桥渲染面无 usage，观测变体）----

// TestDDT86GeminiToMessagesPumpUsageSnapshot 钉住 gemini native 上游 → messages
// 客户端回转方向：桥渲染面（renderAnthropicSummary）不向下游输出 usage，快照经
// 观测回调取上游原始 usageMetadata，口径同 gemini 原生提取。
func TestDDT86GeminiToMessagesPumpUsageSnapshot(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	slot := &chainBridgeUsageSlot{}
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "anthropic_messages"},
		openaicompatbridge.FamilyAnthropicMessages, openaicompatbridge.FamilyGeminiGenerateContent, "m", slot)
	if pump == nil {
		t.Fatal("gemini→messages pump 缺失")
	}
	if err := pump(strings.NewReader(ddT86GeminiNativeSSE), &bytes.Buffer{}); err != nil {
		t.Fatalf("pump: %v", err)
	}
	usage, ok := slot.loadUsage()
	if !ok {
		t.Fatal("桥泵完成后快照缺失")
	}
	if gatewayproto.Token(usage.InputTokens) != 10 {
		t.Fatalf("InputTokens = %d, want 10（promptTokenCount）", gatewayproto.Token(usage.InputTokens))
	}
	if gatewayproto.Token(usage.OutputTokens) != 27 {
		t.Fatalf("OutputTokens = %d, want 27（candidates 20 + thoughts 7）", gatewayproto.Token(usage.OutputTokens))
	}
	if gatewayproto.Token(usage.CacheReadTokens) != 5 {
		t.Fatalf("CacheReadTokens = %d, want 5（cachedContentTokenCount）", gatewayproto.Token(usage.CacheReadTokens))
	}
	if gatewayproto.Token(usage.ThinkingTokens) != 7 {
		t.Fatalf("ThinkingTokens = %d, want 7", gatewayproto.Token(usage.ThinkingTokens))
	}
}

// TestDDT86GeminiToMessagesPumpNilUsageNoSnapshot 同方向上游未发 usageMetadata：
// 观测值保持 nil，不写快照。
func TestDDT86GeminiToMessagesPumpNilUsageNoSnapshot(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	slot := &chainBridgeUsageSlot{}
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "anthropic_messages"},
		openaicompatbridge.FamilyAnthropicMessages, openaicompatbridge.FamilyGeminiGenerateContent, "m", slot)
	if err := pump(strings.NewReader(ddT86GeminiNativeSSENoUsage), &bytes.Buffer{}); err != nil {
		t.Fatalf("pump: %v", err)
	}
	if _, ok := slot.loadUsage(); ok {
		t.Fatal("上游未发 usage 时槽上不应有快照")
	}
}

// ---- 流式：chat completions 上游 → responses 客户端（codex responses 回转）----

// TestDDT86ChatToResponsesPumpUsageSnapshot 钉住 codex responses 回转方向：
// 快照 = codexChatUsageToResponsesUsage 渲染的 responses 形终值（input_tokens /
// output_tokens / input_tokens_details.cached_tokens / output_tokens_details.
// reasoning_tokens），解析与直连 responses 流的结算同源。
func TestDDT86ChatToResponsesPumpUsageSnapshot(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	slot := &chainBridgeUsageSlot{}
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "responses"},
		openaicompatbridge.FamilyResponses, openaicompatbridge.FamilyChatCompletions, "m", slot)
	if pump == nil {
		t.Fatal("chat→responses pump 缺失")
	}
	if err := pump(strings.NewReader(ddC6ChatSSE), &bytes.Buffer{}); err != nil {
		t.Fatalf("pump: %v", err)
	}
	usage, ok := slot.loadUsage()
	if !ok {
		t.Fatal("桥泵完成后快照缺失")
	}
	if gatewayproto.Token(usage.InputTokens) != 10 {
		t.Fatalf("InputTokens = %d, want 10（prompt_tokens → input_tokens）", gatewayproto.Token(usage.InputTokens))
	}
	if gatewayproto.Token(usage.OutputTokens) != 20 {
		t.Fatalf("OutputTokens = %d, want 20（completion_tokens → output_tokens）", gatewayproto.Token(usage.OutputTokens))
	}
	if gatewayproto.Token(usage.CacheReadTokens) != 5 {
		t.Fatalf("CacheReadTokens = %d, want 5（cached_tokens → input_tokens_details.cached_tokens）", gatewayproto.Token(usage.CacheReadTokens))
	}
	if gatewayproto.Token(usage.ThinkingTokens) != 7 {
		t.Fatalf("ThinkingTokens = %d, want 7（reasoning_tokens）", gatewayproto.Token(usage.ThinkingTokens))
	}
}

// TestDDT86ChatToResponsesPumpNilUsageNoSnapshot 回转方向上游未发 usage chunk：
// 桥状态 Usage 保持 nil（桥向客户端渲染的 CompletedResponsesUsage 估算回落形
// 不进快照），不写快照回落结算估算兜底。
func TestDDT86ChatToResponsesPumpNilUsageNoSnapshot(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	slot := &chainBridgeUsageSlot{}
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "responses"},
		openaicompatbridge.FamilyResponses, openaicompatbridge.FamilyChatCompletions, "m", slot)
	if err := pump(strings.NewReader(ddRC1ChatSSENoUsage), &bytes.Buffer{}); err != nil {
		t.Fatalf("pump: %v", err)
	}
	if _, ok := slot.loadUsage(); ok {
		t.Fatal("上游未发 usage 时槽上不应有快照")
	}
}

// ---- 流式：anthropic messages 上游 → responses 客户端 ----

// TestDDT86AnthropicToResponsesPumpUsageSnapshot 钉住 anthropic messages 上游 →
// responses 客户端方向：快照 = anthropicUsageToResponsesUsage 渲染的 responses
// 形终值（input_tokens 含 cache_read 求和、details.cached_tokens、reasoning）。
func TestDDT86AnthropicToResponsesPumpUsageSnapshot(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	slot := &chainBridgeUsageSlot{}
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "responses"},
		openaicompatbridge.FamilyResponses, openaicompatbridge.FamilyAnthropicMessages, "m", slot)
	if pump == nil {
		t.Fatal("anthropic→responses pump 缺失")
	}
	if err := pump(strings.NewReader(ddT86AnthropicSSEWithThinking), &bytes.Buffer{}); err != nil {
		t.Fatalf("pump: %v", err)
	}
	usage, ok := slot.loadUsage()
	if !ok {
		t.Fatal("桥泵完成后快照缺失")
	}
	if gatewayproto.Token(usage.InputTokens) != 130 {
		t.Fatalf("InputTokens = %d, want 130（input_tokens = 100 + cache_read 30）", gatewayproto.Token(usage.InputTokens))
	}
	if gatewayproto.Token(usage.OutputTokens) != 50 {
		t.Fatalf("OutputTokens = %d, want 50", gatewayproto.Token(usage.OutputTokens))
	}
	if gatewayproto.Token(usage.CacheReadTokens) != 30 {
		t.Fatalf("CacheReadTokens = %d, want 30（input_tokens_details.cached_tokens）", gatewayproto.Token(usage.CacheReadTokens))
	}
	if gatewayproto.Token(usage.ThinkingTokens) != 7 {
		t.Fatalf("ThinkingTokens = %d, want 7（output_tokens_details.reasoning_tokens）", gatewayproto.Token(usage.ThinkingTokens))
	}
}

// TestDDT86AnthropicToResponsesPumpNilUsageNoSnapshot 同方向上游未发 usage：
// message_start 赋值点的 usage != nil 守卫（T86 补）使桥状态 Usage 保持 nil，
// 不写快照（修复前 message_start 无守卫会物化全 0 快照否决估算兜底）。
func TestDDT86AnthropicToResponsesPumpNilUsageNoSnapshot(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	slot := &chainBridgeUsageSlot{}
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "responses"},
		openaicompatbridge.FamilyResponses, openaicompatbridge.FamilyAnthropicMessages, "m", slot)
	if err := pump(strings.NewReader(ddRC1AnthropicSSENoUsage), &bytes.Buffer{}); err != nil {
		t.Fatalf("pump: %v", err)
	}
	if _, ok := slot.loadUsage(); ok {
		t.Fatal("上游未发 usage 时槽上不应有快照")
	}
}

// TestDDT86AnthropicToResponsesPumpZeroUsageStillSnapshotted 同方向「发了 usage
// 但全 0」的合法上游行为：快照生效（ok=true，值全 0），计 0 是上游事实。
func TestDDT86AnthropicToResponsesPumpZeroUsageStillSnapshotted(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	slot := &chainBridgeUsageSlot{}
	pump := transformer.bridgeStreamPump(ddC6BridgeInput(t),
		&gatewayproto.ResolvedModelMapping{SourceEndpointFamily: "responses"},
		openaicompatbridge.FamilyResponses, openaicompatbridge.FamilyAnthropicMessages, "m", slot)
	if err := pump(strings.NewReader(ddRC1AnthropicSSEZeroUsage), &bytes.Buffer{}); err != nil {
		t.Fatalf("pump: %v", err)
	}
	usage, ok := slot.loadUsage()
	if !ok {
		t.Fatal("上游发了 usage（全 0）时快照应生效（ok=true 计 0）")
	}
	if gatewayproto.Token(usage.InputTokens) != 0 || gatewayproto.Token(usage.OutputTokens) != 0 {
		t.Fatalf("全 0 usage 快照 = %+v, want 0/0", usage)
	}
}

// ---- 非流式桥接缓冲臂：上游原始 JSON → attempt 槽 ----

// TestDDT86NonStreamChatUpstreamSnapshot 钉住非流式缓冲臂 chat completions 上
// 游：变换前从上游原始 JSON 的 usage 对象按 chat 形解析写入槽位。
func TestDDT86NonStreamChatUpstreamSnapshot(t *testing.T) {
	slot := &chainBridgeUsageSlot{}
	body := []byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":10,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":5}}}`)
	storeChainBridgeNonStreamUsageSnapshot(slot, openaicompatbridge.FamilyChatCompletions, body)
	usage, ok := slot.loadUsage()
	if !ok {
		t.Fatal("上游 usage 对象在场时快照缺失")
	}
	if gatewayproto.Token(usage.InputTokens) != 10 || gatewayproto.Token(usage.OutputTokens) != 20 || gatewayproto.Token(usage.CacheReadTokens) != 5 {
		t.Fatalf("chat 上游快照 = %+v, want 10/20/5", usage)
	}
}

// TestDDT86NonStreamAnthropicUpstreamSnapshot 钉住非流式缓冲臂 anthropic
// messages 上游：usage 对象按 anthropic 形解析（input/output/cache read/
// cache write 四族），与直连 anthropic 流的结算同源。
func TestDDT86NonStreamAnthropicUpstreamSnapshot(t *testing.T) {
	slot := &chainBridgeUsageSlot{}
	body := []byte(`{"id":"msg_1","type":"message","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":30,"cache_creation_input_tokens":12}}`)
	storeChainBridgeNonStreamUsageSnapshot(slot, openaicompatbridge.FamilyAnthropicMessages, body)
	usage, ok := slot.loadUsage()
	if !ok {
		t.Fatal("上游 usage 对象在场时快照缺失")
	}
	if gatewayproto.Token(usage.InputTokens) != 100 || gatewayproto.Token(usage.OutputTokens) != 50 {
		t.Fatalf("anthropic 上游快照 input/output = %d/%d, want 100/50", gatewayproto.Token(usage.InputTokens), gatewayproto.Token(usage.OutputTokens))
	}
	if gatewayproto.Token(usage.CacheReadTokens) != 30 || gatewayproto.Token(usage.CacheWriteTokens) != 12 {
		t.Fatalf("anthropic 上游快照 cache read/write = %d/%d, want 30/12", gatewayproto.Token(usage.CacheReadTokens), gatewayproto.Token(usage.CacheWriteTokens))
	}
}

// TestDDT86NonStreamGeminiUpstreamSnapshot 钉住非流式缓冲臂 gemini native 上
// 游：usageMetadata 按 gemini 原生口径解析（Output=candidates+thoughts）。
func TestDDT86NonStreamGeminiUpstreamSnapshot(t *testing.T) {
	slot := &chainBridgeUsageSlot{}
	body := []byte(`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":20,"thoughtsTokenCount":7,"cachedContentTokenCount":5}}`)
	storeChainBridgeNonStreamUsageSnapshot(slot, openaicompatbridge.FamilyGeminiGenerateContent, body)
	usage, ok := slot.loadUsage()
	if !ok {
		t.Fatal("上游 usageMetadata 在场时快照缺失")
	}
	if gatewayproto.Token(usage.InputTokens) != 10 || gatewayproto.Token(usage.OutputTokens) != 27 ||
		gatewayproto.Token(usage.CacheReadTokens) != 5 || gatewayproto.Token(usage.ThinkingTokens) != 7 {
		t.Fatalf("gemini 上游快照 = %+v, want 10/27/5/7", usage)
	}
}

// TestDDT86NonStreamUpstreamWithoutUsageNoSnapshot 钉住上游 JSON 缺 usage 对象
// 与不可解析正文（SSE 缓冲回退形态）都不写快照（ok=false 回落估算兜底）。
func TestDDT86NonStreamUpstreamWithoutUsageNoSnapshot(t *testing.T) {
	slot := &chainBridgeUsageSlot{}
	storeChainBridgeNonStreamUsageSnapshot(slot, openaicompatbridge.FamilyChatCompletions, []byte(`{"id":"chatcmpl-1","choices":[]}`))
	if _, ok := slot.loadUsage(); ok {
		t.Fatal("上游无 usage 对象时不应写快照")
	}
	storeChainBridgeNonStreamUsageSnapshot(slot, openaicompatbridge.FamilyAnthropicMessages, []byte("data: {\"type\":\"message_stop\"}\n\n"))
	if _, ok := slot.loadUsage(); ok {
		t.Fatal("不可解析正文不应写快照")
	}
	storeChainBridgeNonStreamUsageSnapshot(slot, openaicompatbridge.FamilyGeminiGenerateContent, nil)
	if _, ok := slot.loadUsage(); ok {
		t.Fatal("空正文不应写快照")
	}
}

// ---- 转换函数口径 ----

// TestDDT86ResponsesUsageMapConversion 钉住 responses 形 map → ParsedUsage 的
// 解析口径与 gatewayopenai 双口径链同源（Responses 键优先）。
func TestDDT86ResponsesUsageMapConversion(t *testing.T) {
	usage := map[string]any{
		"input_tokens":          float64(130),
		"input_tokens_details":  map[string]any{"cached_tokens": float64(30)},
		"output_tokens":         float64(50),
		"output_tokens_details": map[string]any{"reasoning_tokens": float64(7)},
	}
	parsed := chainBridgeResponsesUsageMapToParsedUsage(usage)
	if gatewayproto.Token(parsed.InputTokens) != 130 || gatewayproto.Token(parsed.OutputTokens) != 50 ||
		gatewayproto.Token(parsed.CacheReadTokens) != 30 || gatewayproto.Token(parsed.ThinkingTokens) != 7 {
		t.Fatalf("responses 形解析 = %+v", parsed)
	}
	if nilUsage := chainBridgeResponsesUsageMapToParsedUsage(nil); nilUsage.InputTokens != nil {
		t.Fatalf("nil usage 应为无证据: %+v", nilUsage)
	}
}

// TestDDT86AnthropicUsageMapConversion 钉住 anthropic 形 map → ParsedUsage 的
// 解析口径与 gatewayanthropic 提取器同源。
func TestDDT86AnthropicUsageMapConversion(t *testing.T) {
	usage := map[string]any{
		"input_tokens":                float64(100),
		"output_tokens":               float64(50),
		"cache_read_input_tokens":     float64(30),
		"cache_creation_input_tokens": float64(12),
		"output_tokens_details":       map[string]any{"thinking_tokens": float64(7)},
	}
	parsed := chainBridgeAnthropicUsageMapToParsedUsage(usage)
	if gatewayproto.Token(parsed.InputTokens) != 100 || gatewayproto.Token(parsed.OutputTokens) != 50 ||
		gatewayproto.Token(parsed.CacheReadTokens) != 30 || gatewayproto.Token(parsed.CacheWriteTokens) != 12 ||
		gatewayproto.Token(parsed.ThinkingTokens) != 7 {
		t.Fatalf("anthropic 形解析 = %+v", parsed)
	}
	if nilUsage := chainBridgeAnthropicUsageMapToParsedUsage(nil); nilUsage.InputTokens != nil {
		t.Fatalf("nil usage 应为无证据: %+v", nilUsage)
	}
}
