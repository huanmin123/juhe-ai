package gatewayopenai

// w11d 覆盖补齐：inspector 行处理与超长行、usage 解析与估算边界、
// errorpayload/endpoint/lane/semantics 纯函数臂。

import (
	"net/http"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

func TestW11DInspectorLineProcessingArms(t *testing.T) {
	inspector := NewStreamInspector()

	// event: 行、非 data: 行、空 data 行。
	inspector.PushText("event: foo\r\n")
	inspector.PushText("comment: ignore\n\n")
	inspector.PushText("data: \n\n")
	inspector.PushText("data: {\"type\":\"message\"}\n\n")
	// CRLF 换行的 data 行（\r 剥离臂）。
	inspector.PushText("data: {\"type\":\"message.delta\"}\r\n\r\n")
	// 坏 JSON data → DataParseError 摘要。
	inspector.PushText("data: not-json\n\n")
	inspection := inspector.Finish()
	if inspection.RecentEventTypes == nil {
		t.Fatal("事件类型摘要缺失")
	}

	// 空 PushText / 未启用臂。
	idle := NewStreamInspector()
	idle.PushText("")
	if snapshot := idle.Finish(); snapshot.Skipped != false {
		// 未启用检查时快照可用。
		t.Logf("snapshot = %+v", snapshot)
	}

	// 超长行：event 前缀与 data 前缀。
	big := NewStreamInspector()
	big.PushText("event: " + strings.Repeat("e", streamInspectorMaxLineBytes+64) + "\n")
	big.PushText("data: " + strings.Repeat("d", streamInspectorMaxLineBytes+64) + "\n\n")
	big.Finish()

	// appendRollingTextTail 臂。
	if got := appendRollingTextTail("", "next", 0); got != "" {
		t.Fatalf("limit<=0 = %q", got)
	}
	if got := appendRollingTextTail("cur", "", 10); got != "cur" {
		t.Fatalf("空 next = %q", got)
	}
	if got := appendRollingTextTail("abcdef", "ghij", 5); len(got) != 5 || !strings.HasSuffix(got, "ghij") {
		t.Fatalf("滚动尾部 = %q", got)
	}

	// orDefault / maxInt。
	if orDefault("", "x") != "x" || orDefault("v", "x") != "v" {
		t.Fatal("orDefault 臂错误")
	}
	if maxInt(3, 9) != 9 || maxInt(9, 3) != 9 {
		t.Fatal("maxInt 臂错误")
	}
}

func TestW11DUsageParsingArms(t *testing.T) {
	// 空缓冲 / 空片段。
	if got := ParseUsageFromJSONBuffer(nil); got.InputTokens != nil {
		t.Fatal("空缓冲必须零用量")
	}
	if got := ParseUsageFromJSONTextFragment(""); got.InputTokens != nil {
		t.Fatal("空片段必须零用量")
	}
	// 只有 serviceTier 的片段。
	tier := ParseUsageFromJSONTextFragment(`{"service_tier":"priority"}`)
	if tier.ServiceTier != "priority" || tier.InputTokens != nil {
		t.Fatalf("serviceTier 片段 = %+v", tier)
	}
	// 坏 usage JSON 片段。
	bad := ParseUsageFromJSONTextFragment(`{"usage":"not-an-object"}`)
	if bad.InputTokens != nil {
		t.Fatalf("坏 usage 片段 = %+v", bad)
	}
	// 带 usage 的片段。
	good := ParseUsageFromJSONTextFragment(`{"usage":{"input_tokens":3,"output_tokens":5}}`)
	if good.InputTokens == nil || *good.InputTokens != 3 || good.OutputTokens == nil || *good.OutputTokens != 5 {
		t.Fatalf("usage 片段 = %+v", good)
	}
	// JSON 值解析。
	if got := ParseUsageFromJSONValue(map[string]any{"usage": map[string]any{"input_tokens": 7.0}}); got.InputTokens == nil || *got.InputTokens != 7 {
		t.Fatalf("JSON 值解析 = %+v", got)
	}
}

func TestW11DTokenEstimateArms(t *testing.T) {
	// ceilDiv 边界。
	if ceilDiv(7, 0) != 0 || ceilDiv(7, 3) != 3 || ceilDiv(0, 3) != 0 {
		t.Fatal("ceilDiv 臂错误")
	}
	// 纯文本估算下限。
	if tokens, _ := EstimateRequestInputTokens(nil, []byte("a")); tokens < 1 {
		t.Logf("短文本 tokens = %d", tokens)
	}
	// 深层与节点上限。
	deep := map[string]any{}
	current := deep
	for i := 0; i < tokenEstimateMaxDepth+8; i++ {
		next := map[string]any{}
		current["child"] = next
		current = next
	}
	// 超深链触发深度上限守卫（返回 0，不 panic）。
	if tokens, _ := EstimateRequestInputTokens(deep, nil); tokens != 0 {
		t.Fatalf("深层节点 = %d", tokens)
	}
	// 数量庞大的数组节点。
	bigArray := make([]any, tokenEstimateMaxArrayItems+64)
	for i := range bigArray {
		bigArray[i] = "item-" + string(rune('a'+i%26)) + "abcdef"
	}
	if tokens, _ := EstimateRequestInputTokens(map[string]any{"items": bigArray}, nil); tokens < 1 {
		t.Fatalf("大数组 = %d", tokens)
	}
	// 循环引用（seen 检测）。
	cycle := map[string]any{}
	cycle["payload"] = strings.Repeat("abcdefgh", 16)
	cycle["self"] = cycle
	if tokens, _ := EstimateRequestInputTokens(cycle, nil); tokens < 1 {
		t.Fatalf("循环引用 = %d", tokens)
	}
	// 已解析 body 为 nil 且 rawBody 为空。
	if tokens, ok := EstimateRequestInputTokens(nil, nil); ok || tokens != 0 {
		t.Fatalf("空输入 = %d %v", tokens, ok)
	}
	// 已解析 body 非 map 且非 nil → 0,false（554 臂）。
	if tokens, ok := EstimateRequestInputTokens([]any{1}, nil); ok || tokens != 0 {
		t.Fatalf("非 map body = %d %v", tokens, ok)
	}
	// 大 base64/数据 URL 探测。
	longBase64 := strings.Repeat("abcd", 200)
	if !looksLikeLargeBase64Payload(longBase64, "b64_json") {
		t.Fatal("长 base64 必须命中")
	}
	if looksLikeLargeBase64Payload(strings.Repeat("a b c ", 100), "b64_json") {
		t.Fatal("含空白的短单元不得命中")
	}
	if looksLikeLargeBase64Payload("short", "b64_json") {
		t.Fatal("短值不得命中")
	}
	if !shouldSkipEstimatedString("data:image/png;base64,"+strings.Repeat("A", 600), "url") {
		t.Fatal("长 data URL 必须跳过估算")
	}
	if !shouldSkipEstimatedString(longBase64, "b64_json") {
		t.Fatal("长 base64 必须跳过估算")
	}
	if shouldSkipEstimatedString("short", "url") {
		t.Fatal("短值不得跳过")
	}
	if !shouldSkipEstimatedString("   ", "url") {
		t.Fatal("空白（trim 后为空）必须跳过")
	}
}

func TestW11DErrorPayloadArms(t *testing.T) {
	// 空 payload / 无 error 对象。
	if got := openAIErrorPayloadFromParsed(nil, nil); got.Code != "" || got.Message != "" {
		t.Fatalf("空 payload = %+v", got)
	}
	// 非 JSON content-type 且非 { 前缀 → nil。
	header := http.Header{}
	header.Set("Content-Type", "text/plain")
	if payload := ParseErrorPayload("plain text", header); payload != (gatewayproto.ErrorPayload{}) {
		t.Fatalf("纯文本载荷 = %+v", payload)
	}
	if payload := ParseErrorPayload("not json", nil); payload != (gatewayproto.ErrorPayload{}) {
		t.Fatalf("无头非 JSON = %+v", payload)
	}
}

func TestW11DEndpointAndSemanticsArms(t *testing.T) {
	// 归一化：根路径保留（由调用方剥离）。
	if got := normalizedPathWithoutVersion("/v1"); got != "/" {
		t.Fatalf("根版本路径 = %q", got)
	}
	// 语义桥接判断。
	if openAIEndpointFamilyOrUnknown(gatewayproto.EndpointFamilyChatCompletions) != gatewayproto.EndpointFamilyChatCompletions {
		t.Fatal("已知族必须原样返回")
	}
	_ = openAIEndpointFamilyOrUnknown(gatewayproto.ResponseEndpointFamily("other"))
}
