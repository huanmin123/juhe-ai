package gatewayopenai

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// 流检查器端到端：文本 delta 累计、usage 合并、[DONE] 终止、summary。
func TestWAOpenAIStreamInspectorFullStream(t *testing.T) {
	stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello world\"}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"!\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n" +
		"data: [DONE]\n\n"
	inspector := NewStreamInspector()
	inspector.PushText(stream)
	snapshot := inspector.Finish()
	if !snapshot.TerminalReceived {
		t.Fatalf("[DONE] 应终止: %+v", snapshot)
	}
	if !snapshot.OutputReceived || snapshot.OutputEventCount != 2 {
		t.Fatalf("输出 = %v/%d", snapshot.OutputReceived, snapshot.OutputEventCount)
	}
	// "hello world" = ceil(11/4)=3；"!" = 1 → 4。
	if snapshot.EstimatedOutputTokens != 4 {
		t.Fatalf("估算输出 = %d，期望 4", snapshot.EstimatedOutputTokens)
	}
	waOAssertToken(t, snapshot.Usage.InputTokens, 7, "input")
	waOAssertToken(t, snapshot.Usage.OutputTokens, 3, "output")
	if snapshot.LastEventType != "[DONE]" {
		t.Fatalf("LastEventType = %q", snapshot.LastEventType)
	}
	if snapshot.EventTypeCounts["response.output_text.delta"] != 2 {
		t.Fatalf("delta 计数 = %d", snapshot.EventTypeCounts["response.output_text.delta"])
	}
	summary := inspector.DrainEventSummaries()
	if len(summary) != 4 {
		t.Fatalf("summary 数 = %d", len(summary))
	}
	if !summary[3].Terminal || !summary[3].CanEndStream {
		t.Fatalf("末 summary = %+v", summary[3])
	}
}

// 失败流：response.failed 携带 response.error 字段。
func TestWAOpenAIStreamInspectorFailedStream(t *testing.T) {
	inspection := InspectStreamText("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_error\",\"message\":\"上游失败\"}}}\n\n")
	if !inspection.FailedReceived || !inspection.TerminalReceived {
		t.Fatalf("失败流 = %+v", inspection)
	}
	if inspection.ErrorCode != "server_error" || inspection.ErrorMessage != "上游失败" {
		t.Fatalf("错误字段 = %q/%q", inspection.ErrorCode, inspection.ErrorMessage)
	}
}

// 事故同款流：正常 chat chunk + 顶层 error 对象帧（无 event 名、无 type）+
// 命名 error 帧 + EOF。数据错误帧事件完成即判失败，不给下游透传留窗口。
func TestWAOpenAIStreamInspectorTopLevelErrorObjectStream(t *testing.T) {
	chatChunk := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"
	dataErrorFrame := "data: {\"error\":{\"code\":\"upstream_error\",\"message\":\"upstream sent an error event\"}}\n\n"
	namedErrorFrame := "event: error\ndata: {\"code\":\"upstream_stream_truncated\",\"message\":\"上游流被截断\"}\n\n"

	inspector := NewStreamInspector()
	inspector.PushText(chatChunk + dataErrorFrame)
	// 数据错误帧事件完成（空行）后立即检查快照：失败与终止都已置位。
	snapshot := inspector.Snapshot()
	if !snapshot.FailedReceived || !snapshot.TerminalReceived {
		t.Fatalf("顶层 error 对象帧应失败且终止: %+v", snapshot)
	}
	if snapshot.ErrorCode != "upstream_error" || snapshot.ErrorMessage != "upstream sent an error event" {
		t.Fatalf("错误字段 = %q/%q", snapshot.ErrorCode, snapshot.ErrorMessage)
	}
	// 无事件名的数据错误帧按 message 兜底分类，最近事件类型应含该帧。
	if snapshot.EventTypeCounts["message"] != 2 {
		t.Fatalf("message 计数 = %d", snapshot.EventTypeCounts["message"])
	}
	found := false
	for _, eventType := range snapshot.RecentEventTypes {
		if eventType == "message" {
			found = true
		}
	}
	if !found {
		t.Fatalf("RecentEventTypes 应含数据错误帧 = %v", snapshot.RecentEventTypes)
	}

	inspector.PushText(namedErrorFrame)
	inspector.Finish()
	if !inspector.DrainEventSummariesCanEndStream() {
		t.Fatal("失败帧 summary 应可结束流")
	}
	// CanEndStream 检查已 drain；用新实例验证逐帧 summary。
	replay := NewStreamInspector()
	replay.PushText(chatChunk + dataErrorFrame + namedErrorFrame)
	replay.Finish()
	summaries := replay.DrainEventSummaries()
	if len(summaries) != 3 {
		t.Fatalf("summary 数 = %d", len(summaries))
	}
	if !summaries[1].Failed || !summaries[1].CanEndStream || summaries[1].Type != "message" {
		t.Fatalf("数据错误帧 summary = %+v", summaries[1])
	}
	if !summaries[2].Failed || summaries[2].Type != "error" {
		t.Fatalf("命名 error 帧 summary = %+v", summaries[2])
	}
}

// 图片流：image_generation.partial_image 计入图片输出。
func TestWAOpenAIStreamInspectorImageStream(t *testing.T) {
	inspection := InspectStreamText("data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"aGk=\"}\n\n")
	if !inspection.ImageOutputReceived || !inspection.OutputReceived {
		t.Fatalf("图片流 = %+v", inspection)
	}
}

// 超长单行：非 event/data 前缀触发整体跳过。
func TestWAOpenAIStreamInspectorLineOverLimitSkip(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushText("id: " + strings.Repeat("a", 256*1024+1) + "\n\n")
	snapshot := inspector.Finish()
	if !snapshot.Skipped || snapshot.SkipReason != "SSE 单行超过网关解析上限" {
		t.Fatalf("跳过 = %v/%q", snapshot.Skipped, snapshot.SkipReason)
	}
}

// 超大事件：多行 data 累计超过事件上限（单行仍低于 256KB 行限）时按
// oversized 事件分类，类型取自首行前缀、usage 从尾部片段回捞。
func TestWAOpenAIStreamInspectorOversizedEvent(t *testing.T) {
	inspector := NewStreamInspector()
	pad1 := strings.Repeat("a", 200*1024)
	pad2 := strings.Repeat("b", 205*1024)
	pad3 := strings.Repeat("c", 120*1024)
	// SSE 多行 data：每行都必须带 data: 前缀（否则被解析器忽略）。
	inspector.PushText("data: {\"type\":\"response.completed\",\"pad1\":\"" + pad1 + "\"}\n")
	inspector.PushText("data: ,\"pad2\":\"" + pad2 + "\"\n")
	inspector.PushText("data: ,\"pad3\":\"" + pad3 + "\"}\n")
	// 行为存疑：跨越事件上限的那一行（上一行）上的 usage 片段不会被回捞
	// （event 超限路径仅记录 type/image，usage 记忆只作用于跨限后的行）。
	inspector.PushText("data: ,\"usage\":{\"input_tokens\":9,\"output_tokens\":2}}\n\n")
	snapshot := inspector.Finish()
	if snapshot.Skipped {
		t.Fatalf("可识别的 oversized 事件不应整体跳过: %+v", snapshot)
	}
	if !snapshot.TerminalReceived {
		t.Fatalf("oversized completed 应终止: %+v", snapshot)
	}
	waOAssertToken(t, snapshot.Usage.InputTokens, 9, "input（尾部回捞）")
	waOAssertToken(t, snapshot.Usage.OutputTokens, 2, "output（尾部回捞）")
	if snapshot.EventCount != 1 {
		t.Fatalf("事件数 = %d", snapshot.EventCount)
	}
	summary := inspector.DrainEventSummaries()
	if len(summary) != 1 || !summary[0].ParseError {
		t.Fatalf("oversized summary = %+v", summary)
	}
}

// 单行 data 超行限（256KB）且无类型信息：整流跳过并报告行超限原因。
func TestWAOpenAIStreamInspectorOversizedEventUnknownType(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushText("data: " + strings.Repeat("a", 512*1024+2) + "\n\n")
	snapshot := inspector.Finish()
	if !snapshot.Skipped || snapshot.SkipReason != "SSE 单行超过网关解析上限" {
		t.Fatalf("未知类型行超限应整流跳过 = %v/%q", snapshot.Skipped, snapshot.SkipReason)
	}
}

// PushParsedEvent 与逐字节 chunk 结果一致性；dataBytes 回退链。
func TestWAOpenAIStreamInspectorPushParsedEvent(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushParsedEvent(ParseStreamEventData(`{"type":"response.output_text.delta","delta":"abcd"}`, "", "", 0), 0)
	snapshot := inspector.Finish()
	if snapshot.OutputEventCount != 1 || snapshot.EstimatedOutputTokens != 1 {
		t.Fatalf("PushParsedEvent = %+v", snapshot)
	}
	// Skipped 状态下 PushParsedEvent 短路。
	skipped := NewStreamInspector()
	skipped.PushText("id: " + strings.Repeat("a", 256*1024+1) + "\n\n")
	before := skipped.Snapshot()
	skipped.PushParsedEvent(ParseStreamEventData(`{"type":"response.completed"}`, "", "", 0), 0)
	if after := skipped.Snapshot(); after.EventCount != before.EventCount {
		t.Fatal("跳过后 PushParsedEvent 不应分类")
	}
}

// Usage tail：usage 只出现在超大事件尾部时仍可回捞（与 oversized 用例互补，
// 这里验证 ParseUsageFromSseText 入口）。
func TestWAParseUsageFromSseText(t *testing.T) {
	usage := ParseUsageFromSseText("data: {\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":5}}\n\n")
	waOAssertToken(t, usage.InputTokens, 11, "input")
	waOAssertToken(t, usage.OutputTokens, 5, "output")
}

// 顶层 error 对象帧为流的首个内容（零前置输出）：事件完成（空行）即置位
// FailedReceived/TerminalReceived，不给透传留窗口（设计 §4 判定时序）。
func TestWAOpenAIStreamInspectorErrorFrameFirstContent(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushText("data: {\"error\":{\"code\":\"upstream_error\",\"message\":\"upstream sent an error event\"}}\n\n")
	snapshot := inspector.Snapshot()
	if !snapshot.FailedReceived || !snapshot.TerminalReceived {
		t.Fatalf("首个内容即 error 帧应即时失败且终止: %+v", snapshot)
	}
	if snapshot.OutputReceived {
		t.Fatalf("零前置输出不应有可见输出: %+v", snapshot)
	}
	if snapshot.ErrorCode != "upstream_error" || snapshot.ErrorMessage != "upstream sent an error event" {
		t.Fatalf("错误字段 = %q/%q", snapshot.ErrorCode, snapshot.ErrorMessage)
	}
}

// 失败判定状态单调：error 帧之后继续推送正常 chat chunk，FailedReceived
// 保持 true 不回退（检查器快照只置位不复位）。
func TestWAOpenAIStreamInspectorFailedReceivedMonotonic(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushText("data: {\"error\":{\"code\":\"upstream_error\",\"message\":\"upstream sent an error event\"}}\n\n")
	if snapshot := inspector.Snapshot(); !snapshot.FailedReceived {
		t.Fatalf("error 帧后应已置位失败: %+v", snapshot)
	}
	inspector.PushText("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n")
	inspector.PushText("data: [DONE]\n\n")
	snapshot := inspector.Finish()
	if !snapshot.FailedReceived {
		t.Fatalf("后续正常帧不得复位 FailedReceived: %+v", snapshot)
	}
	if !snapshot.TerminalReceived {
		t.Fatalf("TerminalReceived 应保持置位: %+v", snapshot)
	}
	// 正常 chunk 照常计入输出统计：失败不吞掉后续事件的观察。
	if !snapshot.OutputReceived || snapshot.OutputEventCount != 1 {
		t.Fatalf("error 后的 chat chunk 应计入输出: %v/%d", snapshot.OutputReceived, snapshot.OutputEventCount)
	}
}

// error 帧携带 usage：失败判定与 usage 捕获互不排斥（同一事件双结论）。
func TestWAOpenAIStreamInspectorErrorFrameWithUsage(t *testing.T) {
	inspection := InspectStreamText("data: {\"error\":{\"code\":\"upstream_error\",\"message\":\"upstream sent an error event\"},\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2}}\n\n")
	if !inspection.FailedReceived || !inspection.TerminalReceived {
		t.Fatalf("error+usage 帧应失败且终止: %+v", inspection)
	}
	waOAssertToken(t, inspection.Usage.InputTokens, 1, "input")
	waOAssertToken(t, inspection.Usage.OutputTokens, 2, "output")
	if inspection.ErrorCode != "upstream_error" {
		t.Fatalf("错误码 = %q", inspection.ErrorCode)
	}
}

// 超大 error 帧（单事件单行超过 inspector 行上限 256KB，streamInspectorMaxLineBytes）：
// 整流跳过解析（Skipped），不做逐事件分类，因此不判 Failed——设计 §5.2 已知
// 边界（结构性限制，非本判定缺口）。
func TestWAOpenAIStreamInspectorOversizedErrorFrameSkipped(t *testing.T) {
	inspector := NewStreamInspector()
	// error 对象仅含 message 键：无 "type" 字符串可被前缀探针识别。
	inspector.PushText("data: {\"error\":{\"message\":\"" + strings.Repeat("x", 256*1024) + "\"}}\n\n")
	snapshot := inspector.Finish()
	if !snapshot.Skipped {
		t.Fatalf("超行限 error 帧应整流跳过: %+v", snapshot)
	}
	if snapshot.SkipReason != "SSE 单行超过网关解析上限" {
		t.Fatalf("跳过原因 = %q", snapshot.SkipReason)
	}
	if snapshot.FailedReceived || snapshot.TerminalReceived {
		t.Fatalf("跳过路径不判失败/终止: %+v", snapshot)
	}
}

// 超大 error 帧变体：error 内含 "type" 字段时不触发整流跳过，走 oversized
// 事件分类路径——事件类型取自 "type" 前缀探针（"upstream_error" 不在
// oversized 失败/终止类型集），同样不判 Failed。与上一用例共同锚定 §5.2
// 的两个分支：超大事件一律不解析 JSON、不做负载级失败判定。
func TestWAOpenAIStreamInspectorOversizedErrorFrameWithTypeNotFailed(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushText("data: {\"error\":{\"type\":\"upstream_error\",\"message\":\"" + strings.Repeat("x", 300*1024) + "\"}}\n\n")
	snapshot := inspector.Finish()
	if snapshot.Skipped {
		t.Fatalf("含 type 的超大帧走 oversized 事件分类而非整流跳过: %+v", snapshot)
	}
	if snapshot.FailedReceived {
		t.Fatalf("oversized 路径不解析 JSON，不应判失败: %+v", snapshot)
	}
	if snapshot.LastEventType != "upstream_error" {
		t.Fatalf("事件类型应取自 type 前缀探针: %q", snapshot.LastEventType)
	}
}

// SSE 多 data 行把 JSON 拆成非法负载：DataParseError 记账路径，不判
// Failed（设计 §3.4「非法 JSON 非失败」的行为锚定）。
func TestWAOpenAIStreamInspectorSplitJSONDataParseError(t *testing.T) {
	inspection := InspectStreamText("data: {\"error\":\ndata: \"x\"}}\n\n")
	if inspection.FailedReceived {
		t.Fatalf("拆散 JSON 不应判失败: %+v", inspection)
	}
	if inspection.EventCount != 1 {
		t.Fatalf("事件数 = %d，期望 1", inspection.EventCount)
	}
	replay := NewStreamInspector()
	replay.PushText("data: {\"error\":\ndata: \"x\"}}\n\n")
	replay.Finish()
	summaries := replay.DrainEventSummaries()
	if len(summaries) != 1 || !summaries[0].ParseError {
		t.Fatalf("拆散 JSON summary = %+v", summaries)
	}
}

// 请求输入估算与流式 usage 兜底。
func TestWAOpenAIUsageFallback(t *testing.T) {
	t.Run("EstimateRequestInputTokens", func(t *testing.T) {
		body := mustParseJSON(t, `{"messages":[{"content":"hello world"}]}`)
		tokens, ok := EstimateRequestInputTokens(body, []byte(`raw`))
		if !ok || tokens <= 0 {
			t.Fatalf("JSON 估算 = %d/%v", tokens, ok)
		}
		if _, ok := EstimateRequestInputTokens(body, nil); !ok {
			t.Fatal("有 JSON 值即可估算")
		}
		if _, ok := EstimateRequestInputTokens(nil, nil); ok {
			t.Fatal("无输入不可估算")
		}
		// 纯 JSON 标量（无法估算文本）且带原始字节 → 字节估算。
		tokens, ok = EstimateRequestInputTokens(nil, []byte("12345678"))
		if !ok || tokens != 2 {
			t.Fatalf("字节估算 = %d/%v", tokens, ok)
		}
	})
	t.Run("usage 完整或无输出时不估算", func(t *testing.T) {
		usage := gatewayproto.ParsedUsage{InputTokens: gatewayproto.IntToken(1), OutputTokens: gatewayproto.IntToken(2)}
		result := ApplyStreamUsageFallback(nil, nil, usage, StreamUsageFallbackInput{OutputReceived: true})
		if result.Estimated {
			t.Fatalf("完整 usage 不应估算: %+v", result)
		}
		result = ApplyStreamUsageFallback(nil, nil, gatewayproto.EmptyUsage(), StreamUsageFallbackInput{})
		if result.Estimated {
			t.Fatalf("无输出不估算: %+v", result)
		}
	})
	t.Run("completed 无输出也估算 input", func(t *testing.T) {
		body := mustParseJSON(t, `{"messages":[{"content":"abcdef"}]}`)
		result := ApplyStreamUsageFallback(body, nil, gatewayproto.EmptyUsage(), StreamUsageFallbackInput{CompletedSet: true, Completed: true})
		if !result.Estimated || result.EstimatedInputTokens == nil || *result.EstimatedInputTokens != *result.Usage.InputTokens {
			t.Fatalf("completed 估算 = %+v", result)
		}
		// completed（无 OutputReceived）分支不估算输出 token。
		if result.EstimatedOutputTokens != nil {
			t.Fatalf("无输出不应估算 output: %+v", result)
		}
	})
	t.Run("有输出缺 usage 全量估算", func(t *testing.T) {
		body := mustParseJSON(t, `{"messages":[{"content":"abcdef"}]}`)
		result := ApplyStreamUsageFallback(body, nil, gatewayproto.EmptyUsage(), StreamUsageFallbackInput{OutputReceived: true, EstimatedOutputTokens: 6})
		if !result.Estimated || result.EstimatedInputTokens == nil || result.EstimatedOutputTokens == nil {
			t.Fatalf("全量估算 = %+v", result)
		}
		if *result.EstimatedOutputTokens != 6 {
			t.Fatalf("输出估算 = %d，期望 6", *result.EstimatedOutputTokens)
		}
	})
	t.Run("输出估算下限 1", func(t *testing.T) {
		result := ApplyStreamUsageFallback(nil, nil, gatewayproto.EmptyUsage(), StreamUsageFallbackInput{OutputReceived: true})
		if result.EstimatedOutputTokens == nil || *result.EstimatedOutputTokens != 1 {
			t.Fatalf("输出下限 = %+v", result.EstimatedOutputTokens)
		}
	})
}
