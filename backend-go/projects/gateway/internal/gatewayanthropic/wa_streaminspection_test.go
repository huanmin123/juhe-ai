package gatewayanthropic

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
)

// SSE 事件文本解析契约（对齐 Node parseOpenAISseEventText / parseOpenAIStreamEventData）。
func TestWAParseSSEEventText(t *testing.T) {
	t.Run("event 与 data 行", func(t *testing.T) {
		event := ParseSSEEventText("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		if event.EventName != "message_stop" || event.EventType != "message_stop" {
			t.Fatalf("事件名/类型 = %q/%q", event.EventName, event.EventType)
		}
		if event.Data == nil || event.Data["type"] != "message_stop" {
			t.Fatalf("Data = %+v", event.Data)
		}
		if event.DataParseError {
			t.Fatal("合法 JSON 不应报解析错误")
		}
	})
	t.Run("多行 data 以换行拼接", func(t *testing.T) {
		event := ParseSSEEventText("data: {\"a\":\ndata: 1}\n\n")
		if event.Data == nil || event.Data["a"] != float64(1) {
			t.Fatalf("多行 data 应拼接后解析: %+v", event.Data)
		}
	})
	t.Run("DONE 标记", func(t *testing.T) {
		event := ParseSSEEventText("data: [DONE]\n\n")
		if event.EventType != "[DONE]" || event.Data != nil {
			t.Fatalf("DONE 事件 = %+v", event)
		}
	})
	t.Run("空 data", func(t *testing.T) {
		event := ParseSSEEventText("event: ping\n\n")
		if event.EventName != "ping" || event.EventType != "ping" || event.Data != nil {
			t.Fatalf("空 data 事件 = %+v", event)
		}
	})
	t.Run("非法 JSON", func(t *testing.T) {
		event := ParseSSEEventText("data: {broken\n\n")
		if !event.DataParseError {
			t.Fatal("非法 JSON 应标记 DataParseError")
		}
		if event.EventType != "" {
			t.Fatalf("解析失败时 EventType 应回退事件名，得到 %q", event.EventType)
		}
	})
	t.Run("event_type 回退字段", func(t *testing.T) {
		event := ParseSSEEventData(`{"event_type":"step.delta"}`, "", "", 0)
		if event.EventType != "step.delta" {
			t.Fatalf("event_type 回退 = %q", event.EventType)
		}
	})
	t.Run("JSON 标量解析成功但无对象", func(t *testing.T) {
		// Node 语义：JSON.parse 成功但不是对象仍视为解析成功，只是对象字段落空。
		event := ParseSSEEventData(`[1,2]`, "evt", "", 0)
		if event.DataParseError || event.Data != nil {
			t.Fatalf("数组负载应视为解析成功但无对象: %+v", event)
		}
	})
	t.Run("错误字段提取", func(t *testing.T) {
		cases := []struct {
			name, data, code, message string
		}{
			{"error 子对象", `{"error":{"code":"e1","message":"m1"}}`, "e1", "m1"},
			{"response.error 嵌套", `{"response":{"error":{"code":"e2","message":"m2"}}}`, "e2", "m2"},
			{"type=error 平铺", `{"type":"error","code":"e3","message":"m3"}`, "e3", "m3"},
			{"mcp 失败跳过", `{"type":"response.mcp_call.failed","error":{"code":"x","message":"y"}}`, "", ""},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				event := ParseSSEEventData(tc.data, "", "", 0)
				if event.ErrorCode != tc.code || event.ErrorMessage != tc.message {
					t.Fatalf("错误字段 = %q/%q，期望 %q/%q", event.ErrorCode, event.ErrorMessage, tc.code, tc.message)
				}
			})
		}
	})
}

func TestWAEstimateTokenCountFromText(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"  \t\n", 0},
		{"abcd", 1},  // 4 ASCII = 1 token
		{"abcde", 2}, // 5 ASCII = ceil(5/4) = 2
		{"你好", 2},    // CJK 每个 1 token
		{"你a", 2},    // 1 CJK + ceil(1/4)=1
		{"é", 1},     // 非 CJK 非 ASCII 按 ceil(1/2)=1
		{"ééé", 2},   // ceil(3/2)=2
	}
	for _, tc := range cases {
		if got := EstimateTokenCountFromText(tc.text); got != tc.want {
			t.Fatalf("EstimateTokenCountFromText(%q) = %d，期望 %d", tc.text, got, tc.want)
		}
	}
}

func TestWAHasPendingSSEProtocolEvent(t *testing.T) {
	cases := []struct {
		name                     string
		skipped                  bool
		eventName                string
		dataLineCount, dataBytes int
		pendingLine              string
		want                     bool
	}{
		{"skipped 优先", true, "", 0, 0, "data: x", false},
		{"有事件名", false, "message", 0, 0, "", true},
		{"有 data 行", false, "", 2, 10, "", true},
		{"有 data 字节", false, "", 0, 4, "", true},
		{"pending data 行", false, "", 0, 0, "data: {\"a", true},
		{"pending event 行", false, "", 0, 0, "event: error", true},
		{"pending 行 CRLF 尾", false, "", 0, 0, "data: 1\r", true},
		{"无任何 pending", false, "", 0, 0, "id: 1", false},
		{"全部为空", false, "", 0, 0, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hasPendingSSEProtocolEvent(tc.skipped, tc.eventName, tc.dataLineCount, tc.dataBytes, tc.pendingLine)
			if got != tc.want {
				t.Fatalf("hasPendingSSEProtocolEvent = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// 流检查器端到端：完整 Anthropic message 流的分类、usage 合并与终止判定。
func TestWAStreamInspectorFullStream(t *testing.T) {
	stream := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n" +
		"\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"你好 world\"}}\n" +
		"\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"!\"}}\n" +
		"\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n" +
		"\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n" +
		"\n"
	inspector := NewStreamInspector()
	snapshot := inspector.PushText(stream)
	if snapshot.PendingEvent {
		t.Fatalf("完整流不应有 pending 事件: %+v", snapshot)
	}
	snapshot = inspector.Finish()
	if !snapshot.TerminalReceived {
		t.Fatal("message_stop 应判定终止")
	}
	if snapshot.FailedReceived {
		t.Fatal("正常流不应失败")
	}
	if !snapshot.OutputReceived {
		t.Fatal("应有可见输出")
	}
	if snapshot.OutputEventCount != 2 {
		t.Fatalf("输出事件数 = %d，期望 2", snapshot.OutputEventCount)
	}
	// “你好 world” = 2 CJK + ceil(6/4)=2 → 4；“!” = 1。
	if snapshot.EstimatedOutputTokens != 5 || !snapshot.HasEstimatedOutput {
		t.Fatalf("估算输出 token = %d/%v，期望 5/true", snapshot.EstimatedOutputTokens, snapshot.HasEstimatedOutput)
	}
	if snapshot.EventCount != 5 {
		t.Fatalf("事件总数 = %d，期望 5", snapshot.EventCount)
	}
	if snapshot.LastEventType != "message_stop" {
		t.Fatalf("LastEventType = %q", snapshot.LastEventType)
	}
	if snapshot.EventTypeCounts["content_block_delta"] != 2 {
		t.Fatalf("content_block_delta 计数 = %d", snapshot.EventTypeCounts["content_block_delta"])
	}
	waAssertToken(t, snapshot.Usage.InputTokens, 10, "input（message_start）")
	waAssertToken(t, snapshot.Usage.OutputTokens, 5, "output（message_delta）")
	if len(snapshot.RecentEventTypes) != 5 {
		t.Fatalf("RecentEventTypes = %v", snapshot.RecentEventTypes)
	}
	// 终止事件 summary：message_stop 可结束流（drain 一次即清空）。
	if !inspector.DrainEventSummariesCanEndStream() {
		t.Fatal("message_stop summary 应可结束流")
	}
	// CanEndStream 检查已经 drain；再次 drain 应为空。
	if drained := inspector.DrainEventSummaries(); len(drained) != 0 {
		t.Fatalf("第二次 drain 应为空，得到 %d", len(drained))
	}
	// 用新实例验证 summary 内容与 CanEndStream 标记。
	replay := NewStreamInspector()
	replay.PushText(stream)
	replay.Finish()
	drained := replay.DrainEventSummaries()
	if len(drained) != 5 {
		t.Fatalf("drain summary 数 = %d，期望 5", len(drained))
	}
	if !drained[len(drained)-1].CanEndStream || drained[len(drained)-1].Type != "message_stop" {
		t.Fatalf("末个 summary = %+v", drained[len(drained)-1])
	}
	// Snapshot 返回拷贝：修改不影响内部状态。
	snapshot.EventTypeCounts["message_stop"] = 99
	if inspector.Snapshot().EventTypeCounts["message_stop"] != 1 {
		t.Fatal("Snapshot 必须返回计数拷贝")
	}
}

// 错误事件流：失败判定与错误码/消息提取。
func TestWAStreamInspectorErrorStream(t *testing.T) {
	inspection := InspectStreamText("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n")
	if !inspection.FailedReceived || !inspection.TerminalReceived {
		t.Fatalf("error 事件应失败且终止: %+v", inspection)
	}
	if inspection.ErrorCode != "overloaded_error" {
		t.Fatalf("ErrorCode = %q", inspection.ErrorCode)
	}
	if inspection.ErrorMessage != "Overloaded" {
		t.Fatalf("ErrorMessage = %q", inspection.ErrorMessage)
	}
}

// 未完结行与 Keep-Alive 注释行：Finish 冲刷 pending 行；非 event/data 行忽略。
func TestWAStreamInspectorPendingAndComments(t *testing.T) {
	inspector := NewStreamInspector()
	snapshot := inspector.PushText("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"a\"}}")
	if !snapshot.PendingEvent {
		t.Fatal("未完结事件应报告 PendingEvent")
	}
	// 忽略注释/未知行。
	snapshot = inspector.PushText("\n: keep-alive\n\n")
	if snapshot.EventCount != 1 {
		t.Fatalf("注释行不应计入事件: %d", snapshot.EventCount)
	}
	snapshot = inspector.Finish()
	if snapshot.PendingEvent {
		t.Fatal("Finish 后不应残留 pending")
	}
	if snapshot.OutputEventCount != 1 {
		t.Fatalf("冲刷后应有 1 个输出事件: %d", snapshot.OutputEventCount)
	}
}

// 超大事件跳过契约：data 行超过 256KB 后整体跳过并报告原因。
func TestWAStreamInspectorOversizeSkip(t *testing.T) {
	oversize := strings.Repeat("a", 256*1024+1)
	inspector := NewStreamInspector()
	inspector.PushText("event: content_block_delta\ndata: {\"pad\":\"" + oversize + "\"}\n\n")
	snapshot := inspector.Finish()
	if !snapshot.Skipped {
		t.Fatal("超限后应标记 Skipped")
	}
	if snapshot.SkipReason != "anthropic_stream_event_too_large" {
		t.Fatalf("SkipReason = %q", snapshot.SkipReason)
	}
	if snapshot.PendingEvent {
		t.Fatal("跳过状态不应报告 pending")
	}
	// 跳过后继续推送不再变化。
	before := inspector.Snapshot()
	if after := inspector.PushText("data: {}\n\n"); after.EventCount != before.EventCount {
		t.Fatal("跳过后不应再分类事件")
	}
}

func TestWAStreamInspectorPushChunkAndParsedEvent(t *testing.T) {
	inspector := NewStreamInspector()
	inspector.PushChunk([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	snapshot := inspector.Finish()
	if !snapshot.TerminalReceived {
		t.Fatalf("PushChunk 路径应分类事件: %+v", snapshot)
	}

	parsed := NewStreamInspector()
	parsed.PushParsedEvent(ParseSSEEventData(`{"type":"message_stop"}`, "", "", 0), 0)
	snapshot = parsed.Finish()
	if !snapshot.TerminalReceived {
		t.Fatalf("PushParsedEvent 路径应分类事件: %+v", snapshot)
	}
	// 解析失败事件也进 summary（ParseError 标记）。
	withError := NewStreamInspector()
	withError.PushParsedEvent(ParseSSEEventData("{broken", "", "", 7), 0)
	snapshot = withError.Finish()
	if snapshot.EventCount != 1 || snapshot.LastEventType != "message" {
		t.Fatalf("解析失败事件 summary = %+v", snapshot)
	}
}

// EstimateRequestInputTokens：优先 JSON 请求体，其次原始字节长度；已解析但
// 估算为 0 时不回退字节。
func TestWAEstimateRequestInputTokens(t *testing.T) {
	facts := RequestFacts{JSONBody: map[string]any{"messages": []any{"abcd"}}, JSONParsed: true, RawBody: []byte(`{"messages":["abcd"]}`)}
	tokens, ok := EstimateRequestInputTokens(facts)
	if !ok || tokens <= 0 {
		t.Fatalf("JSON 请求体应可估算: %d/%v", tokens, ok)
	}
	// 原始字节回退：bytes/4。
	raw, ok := EstimateRequestInputTokens(RequestFacts{RawBody: []byte("12345678")})
	if !ok || raw != 2 {
		t.Fatalf("原始字节估算 = %d/%v，期望 2/true", raw, ok)
	}
	// JSON 已解析（无解析值）且带原始字节时不再回退字节估算（对齐 Node
	// bodyState.isJson 分支）。
	if _, ok := EstimateRequestInputTokens(RequestFacts{JSONParsed: true, RawBody: []byte("abcdefgh")}); ok {
		t.Fatal("JSON 已解析且无有效文本时不应回退字节估算")
	}
	if _, ok := EstimateRequestInputTokens(RequestFacts{}); ok {
		t.Fatal("空请求事实不可估算")
	}
}

func TestWAApplyStreamUsageFallback(t *testing.T) {
	t.Run("usage 完整时不估算", func(t *testing.T) {
		usage := ParsedUsage{InputTokens: intPtr(3), OutputTokens: intPtr(4)}
		result := ApplyStreamUsageFallback(RequestFacts{}, usage, StreamUsageFallbackInput{OutputReceived: true})
		if result.Estimated || result.Usage.InputTokens != usage.InputTokens {
			t.Fatalf("完整 usage 不应估算: %+v", result)
		}
	})
	t.Run("无输出时不估算", func(t *testing.T) {
		result := ApplyStreamUsageFallback(RequestFacts{}, EmptyUsage(), StreamUsageFallbackInput{OutputReceived: false})
		if result.Estimated {
			t.Fatalf("无输出不应估算: %+v", result)
		}
	})
	t.Run("有输出缺 input/output 时兜底", func(t *testing.T) {
		facts := RequestFacts{JSONBody: map[string]any{"messages": "abcdef"}, JSONParsed: true}
		result := ApplyStreamUsageFallback(facts, EmptyUsage(), StreamUsageFallbackInput{OutputReceived: true, EstimatedOutputTokens: 7, HasEstimatedOutput: true})
		if !result.Estimated || !result.HasEstimatedInput || result.EstimatedInputTokens <= 0 {
			t.Fatalf("input 估算缺失: %+v", result)
		}
		if !result.HasEstimatedOutput || result.EstimatedOutputTokens != 7 {
			t.Fatalf("output 估算应采用输入估计值 7: %+v", result)
		}
		if result.Usage.InputTokens == nil || result.Usage.OutputTokens == nil {
			t.Fatalf("兜底 usage 必须回填: %+v", result.Usage)
		}
	})
	t.Run("输出估算缺省至少 1", func(t *testing.T) {
		result := ApplyStreamUsageFallback(RequestFacts{}, EmptyUsage(), StreamUsageFallbackInput{OutputReceived: true})
		if result.EstimatedOutputTokens != 1 || !result.HasEstimatedOutput {
			t.Fatalf("输出兜底应为 1: %+v", result)
		}
	})
}

// 失败归因四分类（对齐 Node classifyFailure 关键字规则）。
func TestWAClassifyFailureReason(t *testing.T) {
	cases := []struct {
		reason string
		want   FailureClass
	}{
		{"Context deadline exceeded (Timeout)", FailureTimeoutBeforeComplete},
		{"DIAL TCP CONNECT refused", FailureConnectFailed},
		// 关键字按 timeout > connect > read 顺序匹配："read: reset by peer"
		// 不含 connect 才落到 read。
		{"READ: reset by peer", FailureReadInterrupted},
		// 含 "connection" 的读取失败按关键字顺序归为 connect_failed（现行行为）。
		{"read: connection reset", FailureConnectFailed},
		{"explicit_policy", ""},
		{"other", FailureIncompleteResponse},
		{"", FailureIncompleteResponse},
	}
	for _, tc := range cases {
		if got := ClassifyFailureReason(tc.reason); got != tc.want {
			t.Fatalf("ClassifyFailureReason(%q) = %q，期望 %q", tc.reason, got, tc.want)
		}
	}
}

// timeoutNetErr 实现 net.Error 且 Timeout() 为 true（确定性测试桩）。
type timeoutNetErr struct{}

var _ net.Error = timeoutNetErr{}

func (timeoutNetErr) Error() string   { return "i/o timeout while reading" }
func (timeoutNetErr) Timeout() bool   { return true }
func (timeoutNetErr) Temporary() bool { return true }

func TestWAClassifyTransportError(t *testing.T) {
	if _, ok := ClassifyTransportError(nil); ok {
		t.Fatal("nil 错误不归因")
	}
	if _, ok := ClassifyTransportError(context.Canceled); ok {
		t.Fatal("客户端取消不归因")
	}
	if class, ok := ClassifyTransportError(context.DeadlineExceeded); !ok || class != FailureTimeoutBeforeComplete {
		t.Fatalf("DeadlineExceeded 应为 timeout: %q/%v", class, ok)
	}
	if class, ok := ClassifyTransportError(timeoutNetErr{}); !ok || class != FailureTimeoutBeforeComplete {
		t.Fatalf("net.Error Timeout 应为 timeout: %q/%v", class, ok)
	}
	if class, ok := ClassifyTransportError(io.ErrUnexpectedEOF); !ok || class != FailureReadInterrupted {
		t.Fatalf("ErrUnexpectedEOF 应为 read_interrupted: %q/%v", class, ok)
	}
	cases := []struct {
		message string
		want    FailureClass
	}{
		{"dial tcp: connection refused", FailureConnectFailed},
		{"lookup no such host", FailureConnectFailed},
		{"tls: handshake failure", FailureConnectFailed},
		{"read: reset by peer", FailureReadInterrupted},
		{"broken pipe", FailureReadInterrupted},
		// 含 "connection" 的消息先命中 connect 关键字（现行关键字顺序行为）。
		{"read: connection reset by peer", FailureConnectFailed},
		{"request timeout while waiting", FailureTimeoutBeforeComplete},
		{"totally unknown", ""},
	}
	for _, tc := range cases {
		class, ok := ClassifyTransportError(errors.New(tc.message))
		if ok != (tc.want != "") || (tc.want != "" && class != tc.want) {
			t.Fatalf("ClassifyTransportError(%q) = %q/%v，期望 %q", tc.message, class, ok, tc.want)
		}
	}
}

func TestWAAttributeStreamOutcome(t *testing.T) {
	t.Run("客户端取消", func(t *testing.T) {
		result := AttributeStreamOutcome(StreamInspection{}, errors.New("boom"), true)
		if !result.ClientCancelled || result.Class != "" {
			t.Fatalf("客户端取消归因 = %+v", result)
		}
	})
	t.Run("已终止视为完成", func(t *testing.T) {
		result := AttributeStreamOutcome(StreamInspection{TerminalReceived: true}, errors.New("late read error"), false)
		if !result.Completed || result.Class != "" {
			t.Fatalf("终止后的尾部错误不应改归因: %+v", result)
		}
	})
	t.Run("干净结束但无终止事件", func(t *testing.T) {
		result := AttributeStreamOutcome(StreamInspection{EventCount: 2}, nil, false)
		if result.Class != FailureIncompleteResponse {
			t.Fatalf("无终止事件应 incomplete_response: %+v", result)
		}
	})
	t.Run("分类错误直接采用", func(t *testing.T) {
		result := AttributeStreamOutcome(StreamInspection{}, context.DeadlineExceeded, false)
		if result.Class != FailureTimeoutBeforeComplete || result.Message == "" {
			t.Fatalf("超时归因 = %+v", result)
		}
	})
	t.Run("未知错误按事件进度区分", func(t *testing.T) {
		before := AttributeStreamOutcome(StreamInspection{}, errors.New("weird"), false)
		if before.Class != FailureConnectFailed {
			t.Fatalf("事件前未知错误应 connect_failed: %+v", before)
		}
		mid := AttributeStreamOutcome(StreamInspection{EventCount: 3}, errors.New("weird"), false)
		if mid.Class != FailureReadInterrupted {
			t.Fatalf("事件后未知错误应 read_interrupted: %+v", mid)
		}
	})
}

func TestWAParseErrorPayload(t *testing.T) {
	headerJSON := map[string][]string{"Content-Type": {"application/json"}}
	t.Run("标准 error 子对象", func(t *testing.T) {
		payload := ParseErrorPayload(`{"type":"error","error":{"type":"invalid_request_error","code":"bad","message":"格式错误"}}`, headerJSON)
		if payload.Code != "bad" || payload.Type != "invalid_request_error" || payload.Message != "格式错误" {
			t.Fatalf("错误负载 = %+v", payload)
		}
		if payload.IsZero() {
			t.Fatal("非空负载 IsZero 应为 false")
		}
	})
	t.Run("无 error 子对象时根对象兜底", func(t *testing.T) {
		payload := ParseErrorPayload(`{"code":429,"type":"rate_limit_error","message":"慢一点"}`, headerJSON)
		if payload.Code != "429" || payload.Type != "rate_limit_error" || payload.Message != "慢一点" {
			t.Fatalf("根对象负载 = %+v", payload)
		}
	})
	t.Run("message 别名字段回退", func(t *testing.T) {
		payload := ParseErrorPayload(`{"error":{"detail":"明细"}}`, headerJSON)
		if payload.Message != "明细" {
			t.Fatalf("detail 回退 = %+v", payload)
		}
		payload = ParseErrorPayload(`{"error":{"error_description":"desc"}}`, headerJSON)
		if payload.Message != "desc" {
			t.Fatalf("error_description 回退 = %+v", payload)
		}
	})
	t.Run("非 JSON 文本且无 json Content-Type", func(t *testing.T) {
		payload := ParseErrorPayload("plain upstream error", map[string][]string{"Content-Type": {"text/plain"}})
		if !payload.IsZero() {
			t.Fatalf("纯文本负载应为空: %+v", payload)
		}
	})
	t.Run("JSON Content-Type 但解析失败", func(t *testing.T) {
		payload := ParseErrorPayload("{broken", headerJSON)
		if !payload.IsZero() {
			t.Fatalf("解析失败应为空: %+v", payload)
		}
	})
	t.Run("文本以 { 开头可无需 Content-Type", func(t *testing.T) {
		payload := ParseErrorPayload(`{"error":{"message":"m"}}`, nil)
		if payload.Message != "m" {
			t.Fatalf("无 header 时应按 JSON 解析: %+v", payload)
		}
	})
	t.Run("JSON 数组负载", func(t *testing.T) {
		payload := ParseErrorPayload(`[]`, headerJSON)
		if !payload.IsZero() {
			t.Fatalf("数组负载应为空: %+v", payload)
		}
	})
}

func TestWAParseErrorPayloadFromJSONValue(t *testing.T) {
	if payload := ParseErrorPayloadFromJSONValue("str"); !payload.IsZero() {
		t.Fatalf("非对象输入应为空: %+v", payload)
	}
	value := waParseJSONObject(t, `{"error":{"code":"c","type":"t","message":"m"}}`)
	payload := ParseErrorPayloadFromJSONValue(value)
	if payload.Code != "c" || payload.Type != "t" || payload.Message != "m" {
		t.Fatalf("JSONValue 负载 = %+v", payload)
	}
	var empty ErrorPayload
	if !empty.IsZero() {
		t.Fatal("零值负载 IsZero 应为 true")
	}
}

// randomUUID 契约：RFC 4122 v4 形态（版本位 4、variant 位 8/9/a/b）。
func TestWARandomUUIDShape(t *testing.T) {
	value := randomUUID()
	if len(value) != 36 || strings.Count(value, "-") != 4 {
		t.Fatalf("UUID 形态错误: %q", value)
	}
	if value[14] != '4' {
		t.Fatalf("应为 v4 UUID: %q", value)
	}
	variant := value[19]
	if variant != '8' && variant != '9' && variant != 'a' && variant != 'b' {
		t.Fatalf("variant 位错误: %q", value)
	}
}
