package gatewaygemini

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 流检查器端到端：GenerateContent SSE 文本、usageMetadata、finishReason 终止。
func TestWAStreamInspectorGenerateContent(t *testing.T) {
	stream := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"你好 world\"}]}}]}\n" +
		"\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"!\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":2}}\n" +
		"\n"
	inspector := NewStreamInspector()
	snapshot := inspector.PushText(stream)
	if snapshot.PendingEvent {
		t.Fatalf("完整流不应有 pending: %+v", snapshot)
	}
	snapshot = inspector.Finish()
	// Gemini 流有输出且无失败时 Finish 补记终止。
	if !snapshot.TerminalReceived {
		t.Fatalf("有输出的流应补记终止: %+v", snapshot)
	}
	if snapshot.OutputEventCount != 2 || !snapshot.OutputReceived {
		t.Fatalf("输出 = %d/%v", snapshot.OutputEventCount, snapshot.OutputReceived)
	}
	// “你好 world” = 2 CJK + ceil(6/4)=2 → 4；“!” = 1 → 5。
	if snapshot.EstimatedOutputTokens != 5 {
		t.Fatalf("估算输出 token = %d，期望 5", snapshot.EstimatedOutputTokens)
	}
	waAssertGeminiToken(t, snapshot.Usage.InputTokens, 3, "input")
	waAssertGeminiToken(t, snapshot.Usage.OutputTokens, 2, "output")
	if snapshot.LastEventType != "message" {
		t.Fatalf("LastEventType = %q", snapshot.LastEventType)
	}
}

// interaction 流：step.delta + interaction.completed + ResponseResourceID 提取。
func TestWAStreamInspectorInteractionStream(t *testing.T) {
	stream := "event: step.delta\n" +
		"data: {\"type\":\"step.delta\",\"delta\":{\"type\":\"text\",\"text\":\"答\"}}\n" +
		"\n" +
		"event: interaction.created\n" +
		"data: {\"type\":\"interaction.created\",\"interaction\":{\"id\":\" ix_42 \"}}\n" +
		"\n" +
		"event: interaction.completed\n" +
		"data: {\"type\":\"interaction.completed\",\"interaction\":{\"id\":\" ix_42 \",\"status\":\"completed\"}}\n" +
		"\n"
	inspector := NewStreamInspector()
	inspector.PushText(stream)
	snapshot := inspector.Finish()
	if !snapshot.TerminalReceived {
		t.Fatalf("interaction.completed 应终止: %+v", snapshot)
	}
	// 流检查器的 ResourceID 保留原始文本（空格等在 affinity 边界再归一化）。
	if snapshot.ResponseResourceID != " ix_42 " {
		t.Fatalf("ResponseResourceID = %q，期望保留原文", snapshot.ResponseResourceID)
	}
	if snapshot.LastEventType != "interaction.completed" {
		t.Fatalf("LastEventType = %q", snapshot.LastEventType)
	}
	// step.delta 文本可见（type=text）。
	if snapshot.OutputEventCount != 1 {
		t.Fatalf("输出事件数 = %d", snapshot.OutputEventCount)
	}
}

// 失败流：interaction.failed 设置 FailedReceived 与错误信息。
func TestWAStreamInspectorFailedStream(t *testing.T) {
	inspection := InspectStreamText("data: {\"type\":\"interaction.failed\",\"interaction\":{\"status\":\"failed\",\"error\":{\"status\":\"UNAVAILABLE\",\"message\":\"x\"}}}\n\n")
	if !inspection.FailedReceived {
		t.Fatalf("failed 流应标记失败: %+v", inspection)
	}
	if inspection.ErrorCode != "UNAVAILABLE" || inspection.ErrorMessage != "x" {
		t.Fatalf("错误信息 = %q/%q", inspection.ErrorCode, inspection.ErrorMessage)
	}
	// 失败时 Finish 不再补记终止（失败本身就是终止）。
	if !inspection.TerminalReceived {
		t.Fatal("failed 事件本身应终止")
	}
}

// 显式 finish/done/[DONE] 事件类型是终止事件。
func TestWAStreamInspectorTerminalEventTypes(t *testing.T) {
	for _, eventType := range []string{"finish", "done", "[DONE]"} {
		inspector := NewStreamInspector()
		inspector.PushParsedEvent(ParseSSEEventData(`{"type":"`+eventType+`"}`, "", "", 0), 0)
		snapshot := inspector.Finish()
		if !snapshot.TerminalReceived {
			t.Fatalf("事件 %q 应判定终止: %+v", eventType, snapshot)
		}
	}
}

// 超大事件跳过契约。
func TestWAStreamInspectorOversizeSkip(t *testing.T) {
	oversize := strings.Repeat("a", 256*1024+1)
	inspector := NewStreamInspector()
	inspector.PushText("data: {\"pad\":\"" + oversize + "\"}\n\n")
	snapshot := inspector.Finish()
	if !snapshot.Skipped || snapshot.SkipReason != "gemini_stream_event_too_large" {
		t.Fatalf("跳过标记 = %v/%q", snapshot.Skipped, snapshot.SkipReason)
	}
	if inspector.PushText("data: {}\n\n").EventCount != 0 {
		t.Fatal("跳过后不应再分类事件")
	}
}

// 持续无换行输入契约：未完结行缓冲受 256KB 行上限约束，不随输入无上限增长；
// 超过行上限即按事件超限语义整流跳过并报告原因（资源边界回归）。
func TestWAStreamInspectorNoNewlineLineCap(t *testing.T) {
	const lineCap = 256 * 1024
	inspector := NewStreamInspector()
	inspector.PushChunk([]byte("data: "))
	chunk := []byte(strings.Repeat("x", 4096))
	for total := len("data: "); total <= 2*lineCap; total += len(chunk) {
		inspector.PushChunk(chunk)
		if len(inspector.pendingLine) > lineCap {
			t.Fatalf("pendingLine 越界累积: %d", len(inspector.pendingLine))
		}
	}
	snapshot := inspector.Snapshot()
	if !snapshot.Skipped || snapshot.SkipReason != "gemini_stream_event_too_large" {
		t.Fatalf("持续无换行输入应超限跳过: %+v", snapshot)
	}
	if snapshot.PendingEvent {
		t.Fatal("跳过状态不应报告 pending")
	}
}

// 行上限边界：恰为上限的未完结行保留，超出 1 字节即跳过。
func TestWAStreamInspectorLineCapBoundary(t *testing.T) {
	const lineCap = 256 * 1024
	atLimit := NewStreamInspector()
	atLimit.PushChunk([]byte("data: " + strings.Repeat("x", lineCap-len("data: "))))
	if snapshot := atLimit.Snapshot(); snapshot.Skipped {
		t.Fatalf("上限内未完结行不应跳过: %+v", snapshot)
	}
	overLimit := NewStreamInspector()
	overLimit.PushChunk([]byte("data: " + strings.Repeat("x", lineCap-len("data: ")+1)))
	if snapshot := overLimit.Snapshot(); !snapshot.Skipped || snapshot.SkipReason != "gemini_stream_event_too_large" {
		t.Fatalf("超出上限 1 字节应跳过: %+v", snapshot)
	}
}

// 行上限跨 chunk：多段推入累计恰达上限不跳过，再来 1 字节即跳过且缓冲释放。
func TestWAStreamInspectorLineCapCrossChunk(t *testing.T) {
	const lineCap = 256 * 1024
	inspector := NewStreamInspector()
	inspector.PushChunk([]byte("data: "))
	total := len("data: ")
	fill := []byte(strings.Repeat("x", 4096))
	for total < lineCap {
		size := len(fill)
		if remaining := lineCap - total; remaining < size {
			size = remaining
		}
		inspector.PushChunk(fill[:size])
		total += size
	}
	if len(inspector.pendingLine) != lineCap {
		t.Fatalf("跨 chunk 累计应恰达上限: %d", len(inspector.pendingLine))
	}
	if snapshot := inspector.Snapshot(); snapshot.Skipped {
		t.Fatalf("恰达上限不应跳过: %+v", snapshot)
	}
	inspector.PushChunk([]byte("x"))
	snapshot := inspector.Snapshot()
	if !snapshot.Skipped || snapshot.SkipReason != "gemini_stream_event_too_large" {
		t.Fatalf("跨 chunk 超出上限 1 字节应跳过: %+v", snapshot)
	}
	if len(inspector.pendingLine) != 0 {
		t.Fatalf("跳过后未完结行缓冲应释放: %d", len(inspector.pendingLine))
	}
}

// 超限行在同 chunk 内带换行完结：预追加检查先于行处理触发，整流跳过且不分类事件。
func TestWAStreamInspectorOversizedLineWithNewline(t *testing.T) {
	const lineCap = 256 * 1024
	inspector := NewStreamInspector()
	inspector.PushChunk([]byte("data: " + strings.Repeat("x", lineCap) + "\n\n"))
	snapshot := inspector.Snapshot()
	if !snapshot.Skipped || snapshot.SkipReason != "gemini_stream_event_too_large" || snapshot.EventCount != 0 {
		t.Fatalf("同 chunk 超限行应整流跳过且不分类: %+v", snapshot)
	}
	if len(inspector.pendingLine) != 0 {
		t.Fatalf("跳过后未完结行缓冲应释放: %d", len(inspector.pendingLine))
	}
}

// event 前缀行与其他前缀同判（上限检查在预追加阶段，与行前缀无关）；跳过后短路。
func TestWAStreamInspectorOversizedEventPrefixLine(t *testing.T) {
	const lineCap = 256 * 1024
	inspector := NewStreamInspector()
	inspector.PushChunk([]byte("event: " + strings.Repeat("x", lineCap)))
	snapshot := inspector.Snapshot()
	if !snapshot.Skipped || snapshot.SkipReason != "gemini_stream_event_too_large" {
		t.Fatalf("event 前缀超限行应跳过: %+v", snapshot)
	}
	if len(inspector.pendingLine) != 0 {
		t.Fatalf("跳过后未完结行缓冲应释放: %d", len(inspector.pendingLine))
	}
	inspector.PushChunk([]byte("data: {\"type\":\"finish\"}\n\n"))
	if snapshot := inspector.Snapshot(); snapshot.EventCount != 0 || !snapshot.Skipped {
		t.Fatalf("跳过后应短路不再分类: %+v", snapshot)
	}
}

// PushChunk / PushParsedEvent / 解析失败 summary。
func TestWAStreamInspectorPushVariants(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushChunk([]byte("data: {\"type\":\"finish\"}\n\n"))
	snapshot := inspector.Finish()
	if !snapshot.TerminalReceived {
		t.Fatalf("PushChunk 路径 = %+v", snapshot)
	}

	parsed := NewStreamInspector()
	parsed.PushParsedEvent(ParseSSEEventData(`{"candidates":[{"finishReason":"STOP"}]}`, "", "", 0), 0)
	snapshot = parsed.Finish()
	if !snapshot.TerminalReceived {
		t.Fatalf("finishReason 事件应终止: %+v", snapshot)
	}

	broken := NewStreamInspector()
	broken.PushParsedEvent(ParseSSEEventData("{bad", "evt", "", 5), 0)
	snapshot = broken.Finish()
	if snapshot.EventCount != 1 || snapshot.LastEventType != "evt" {
		t.Fatalf("解析失败 summary = %+v", snapshot)
	}
}

// httptest 上游模拟：确定性 Anthropic/Gemini 风格 SSE 响应流经检查器，
// 验证事件分类与 chunk 边界无关。
func TestWAStreamInspectorWithUpstreamSSE(t *testing.T) {
	const upstreamBody = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ab\"}]}}]}\n\n" +
		"data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":4,\"candidatesTokenCount\":1}}\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	t.Cleanup(server.Close)

	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("请求上游失败: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if contentType := response.Header.Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("Content-Type = %q", contentType)
	}
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读取上游失败: %v", err)
	}

	// 一次整读 + 逐字节喂入两种方式结果一致（SSE 解析与 chunk 边界无关）。
	oneShot := InspectStreamText(string(raw))
	byteWise := NewStreamInspector()
	for index := 0; index < len(raw); index++ {
		byteWise.PushChunk(raw[index : index+1])
	}
	finished := byteWise.Finish()
	if finished.EventCount != oneShot.EventCount || finished.EstimatedOutputTokens != oneShot.EstimatedOutputTokens {
		t.Fatalf("chunk 边界影响结果: 一次 %+v vs 逐字节 %+v", oneShot, finished)
	}
	if !finished.TerminalReceived || !finished.OutputReceived {
		t.Fatalf("上游流分类 = %+v", finished)
	}
	waAssertGeminiToken(t, finished.Usage.InputTokens, 4, "input")
	waAssertGeminiToken(t, finished.Usage.OutputTokens, 1, "output")
}

// SSE 文本解析契约（与 anthropic 切片同源）。
func TestWAParseSSEEventText(t *testing.T) {
	t.Run("event 与 data", func(t *testing.T) {
		event := ParseSSEEventText("event: step.delta\ndata: {\"type\":\"step.delta\",\"delta\":{\"text\":\"x\"}}\n\n")
		if event.EventName != "step.delta" || event.EventType != "step.delta" {
			t.Fatalf("事件名/类型 = %q/%q", event.EventName, event.EventType)
		}
		if event.Data == nil {
			t.Fatal("Data 应解析")
		}
	})
	t.Run("DONE 与空 data", func(t *testing.T) {
		if event := ParseSSEEventText("data: [DONE]\n\n"); event.EventType != "[DONE]" {
			t.Fatalf("DONE = %+v", event)
		}
		if event := ParseSSEEventText("event: ping\n\n"); event.EventType != "ping" || event.Data != nil {
			t.Fatalf("空 data = %+v", event)
		}
	})
	t.Run("非法 JSON", func(t *testing.T) {
		event := ParseSSEEventText("data: {oops\n\n")
		if !event.DataParseError || event.EventType != "" {
			t.Fatalf("非法 JSON = %+v", event)
		}
	})
	t.Run("错误字段提取", func(t *testing.T) {
		event := ParseSSEEventData(`{"error":{"code":"e","message":"m"}}`, "", "", 0)
		if event.ErrorCode != "e" || event.ErrorMessage != "m" {
			t.Fatalf("错误字段 = %q/%q", event.ErrorCode, event.ErrorMessage)
		}
		event = ParseSSEEventData(`{"type":"response.mcp_call.failed","error":{"code":"e"}}`, "", "", 0)
		if event.ErrorCode != "" {
			t.Fatalf("mcp 失败不应提取错误: %+v", event)
		}
	})
	t.Run("换行风格", func(t *testing.T) {
		event := ParseSSEEventText("data: {\"a\"\r\n data2")
		// CRLF 行拆分：第二段无 data: 前缀被忽略。
		_ = event
		if got := splitSSELines("a\r\nb\rc\nd"); !waSliceEqual(got, []string{"a", "b", "c", "d"}) {
			t.Fatalf("splitSSELines = %v", got)
		}
	})
}

func TestWAEstimateTokenCountFromTextGemini(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"  ", 0},
		{"abcd", 1},
		{"你好", 2},
		{"ééé", 2},
	}
	for _, tc := range cases {
		if got := EstimateTokenCountFromText(tc.text); got != tc.want {
			t.Fatalf("EstimateTokenCountFromText(%q) = %d，期望 %d", tc.text, got, tc.want)
		}
	}
}

func TestWAHasPendingSSEProtocolEventGemini(t *testing.T) {
	if hasPendingSSEProtocolEvent(true, "", 0, 0, []byte("data: x")) {
		t.Fatal("skipped 应为 false")
	}
	if !hasPendingSSEProtocolEvent(false, "evt", 0, 0, nil) {
		t.Fatal("事件名应 pending")
	}
	if !hasPendingSSEProtocolEvent(false, "", 0, 0, []byte("data: {")) {
		t.Fatal("pending data 行应 pending")
	}
	if hasPendingSSEProtocolEvent(false, "", 0, 0, nil) {
		t.Fatal("空状态不 pending")
	}
}

// usage 兜底与请求输入估算。
func TestWAGeminiUsageFallbackAndEstimate(t *testing.T) {
	t.Run("usage 完整不估算", func(t *testing.T) {
		usage := ParsedUsage{InputTokens: intPtrGemini(1), OutputTokens: intPtrGemini(2)}
		result := ApplyStreamUsageFallback(RequestFacts{}, usage, StreamUsageFallbackInput{OutputReceived: true})
		if result.Estimated {
			t.Fatalf("完整 usage 不应估算: %+v", result)
		}
	})
	t.Run("无输出不估算", func(t *testing.T) {
		result := ApplyStreamUsageFallback(RequestFacts{}, EmptyUsage(), StreamUsageFallbackInput{OutputReceived: false})
		if result.Estimated {
			t.Fatalf("无输出不应估算: %+v", result)
		}
	})
	t.Run("缺 input/output 估算", func(t *testing.T) {
		facts := RequestFacts{JSONBody: map[string]any{"contents": "abcdef"}, JSONParsed: true}
		result := ApplyStreamUsageFallback(facts, EmptyUsage(), StreamUsageFallbackInput{OutputReceived: true, EstimatedOutputTokens: 4, HasEstimatedOutput: true})
		if !result.Estimated || !result.HasEstimatedInput || result.EstimatedInputTokens <= 0 {
			t.Fatalf("input 估算 = %+v", result)
		}
		if result.EstimatedOutputTokens != 4 {
			t.Fatalf("output 估算 = %d，期望 4", result.EstimatedOutputTokens)
		}
	})
	t.Run("输出估算下限 1", func(t *testing.T) {
		result := ApplyStreamUsageFallback(RequestFacts{}, EmptyUsage(), StreamUsageFallbackInput{OutputReceived: true})
		if result.EstimatedOutputTokens != 1 {
			t.Fatalf("输出下限 = %d", result.EstimatedOutputTokens)
		}
	})
	t.Run("请求输入估算", func(t *testing.T) {
		tokens, ok := EstimateRequestInputTokens(RequestFacts{JSONBody: map[string]any{"contents": "abcd"}, JSONParsed: true, RawBody: []byte("x")})
		if !ok || tokens <= 0 {
			t.Fatalf("JSON 估算 = %d/%v", tokens, ok)
		}
		raw, ok := EstimateRequestInputTokens(RequestFacts{RawBody: []byte("12345678")})
		if !ok || raw != 2 {
			t.Fatalf("字节估算 = %d/%v，期望 2/true", raw, ok)
		}
		if _, ok := EstimateRequestInputTokens(RequestFacts{JSONParsed: true, RawBody: []byte("12345678")}); ok {
			t.Fatal("JSON 已解析不应回退字节估算")
		}
		if _, ok := EstimateRequestInputTokens(RequestFacts{}); ok {
			t.Fatal("空事实不可估算")
		}
	})
}

func intPtrGemini(value int) *int { return &value }

// usage 底层：numberValue、合并与 service tier 校验。
func TestWAGeminiUsagePrimitives(t *testing.T) {
	if numberValue(float64(-1)) != nil || numberValue("abc") != nil || numberValue(struct{}{}) != nil {
		t.Fatal("numberValue 非法输入应为 nil")
	}
	waAssertGeminiToken(t, numberValue("12"), 12, "字符串数字")
	base := ParsedUsage{InputTokens: intPtrGemini(1)}
	merged := MergeUsage(base, ParsedUsage{ServiceTier: "flex"})
	waAssertGeminiToken(t, merged.InputTokens, 1, "合并保留")
	if merged.ServiceTier != "flex" {
		t.Fatalf("合并 tier = %q", merged.ServiceTier)
	}
	if HasAnyUsageValue(EmptyUsage()) {
		t.Fatal("空 usage 无值")
	}
	cases := []struct {
		in   any
		want string
	}{
		{"priority", "priority"},
		{"PRIORITY", ""}, // gemini 端正则无 i 标志
		{" auto ", ""},
		{7, ""},
	}
	for _, tc := range cases {
		if got := NormalizeOptionalUsageServiceTier(tc.in); got != tc.want {
			t.Fatalf("NormalizeOptionalUsageServiceTier(%v) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}
