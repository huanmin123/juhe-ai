package gatewayproto

// StreamInspection mirrors the Node GatewayStreamInspection
// (protocols/_shared/types.ts). It is the value snapshot a StreamInspector
// returns after every chunk and at finish.
type StreamInspection struct {
	TerminalReceived      bool
	FailedReceived        bool
	OutputReceived        bool
	ImageOutputReceived   bool
	OutputEventCount      int
	EstimatedOutputTokens int
	EventCount            int
	// ParseErrorEventCount 是数据载荷无法 JSON 解析的事件计数（DataParseError，
	// 含粘连载荷残片；oversized 事件不计数）。流式提交边界受限重放与异常终局
	// 处置设计 §3.2：EOF 终局判定按本计数收紧 generic 收敛，字节级隔离与
	// WARN 观测由管道与拦截面消费。
	ParseErrorEventCount int
	// InvalidProtocolEventCount 是解析成功但协议形状异常的事件计数（顶层存在
	// `choices` 键且 JSON 值不是数组；流式提交边界受限重放与异常终局处置设计
	// §3.2.0 L2，2026-10-26 增量）：与 ParseErrorEventCount 并列的观测计数，
	// 字节级隔离与 WARN 摘要由同一隔离基建处置；L2 纯观测+隔离，不参与终局
	// 判定。仅 gatewayopenai inspector 域计数（谓词只对 openai 形状有意义）。
	InvalidProtocolEventCount int
	EventTypeCounts           map[string]int
	LastEventType             string
	RecentEventTypes          []string
	PendingEvent              bool
	Skipped                   bool
	SkipReason                string
	ErrorCode                 string
	ErrorMessage              string
	// ResponseResourceID 是 Gemini interactions 创建流的资源 ID（inspector 从
	// interaction.* 事件提取的协议内部元数据）；其余协议恒空。亲和写入
	// （提交前 Remember）与后续资源路由依赖本字段的全链回带。
	ResponseResourceID string
	Usage              ParsedUsage
}

// ProtocolComplete reports whether a stream terminal (e.g. [DONE] or a
// completed response event) was observed.
func (s StreamInspection) ProtocolComplete() bool { return s.TerminalReceived }

// SemanticSuccess reports whether the stream completed without failure
// evidence. It is the streaming counterpart of the buffered response
// semantic-success decision.
func (s StreamInspection) SemanticSuccess() bool {
	return s.TerminalReceived && !s.FailedReceived
}

// StreamEventSummary mirrors OpenAIStreamEventSummary: the per-event record
// kept aside from the aggregate snapshot so the stream pipe can decide
// whether the stream may end.
type StreamEventSummary struct {
	Type         string
	DataBytes    int
	Terminal     bool
	CanEndStream bool
	Failed       bool
	Output       bool
	Usage        bool
	ParseError   bool
}

// StreamInspector is the driver-provided incremental stream inspection
// surface (GatewayStreamInspector in Node, narrowed to the chunk/text/snapshot
// core that the gateway stream pipe needs).
type StreamInspector interface {
	// PushChunk feeds one raw transport chunk (bytes).
	PushChunk(chunk []byte)
	// PushText feeds one decoded text segment.
	PushText(text string)
	// Finish flushes any pending line/event at EOF and returns the final
	// snapshot.
	Finish() StreamInspection
	// Snapshot returns the current inspection state.
	Snapshot() StreamInspection
	// DrainEventSummariesCanEndStream drains the pending per-event summaries
	// and reports whether any of them may end the stream.
	DrainEventSummariesCanEndStream() bool
}

// SsePendingEventState is the protocol-agnostic SSE parser state consumed by
// HasPendingSseProtocolEvent (mirrors _shared/sse-pending-event.ts).
type SsePendingEventState struct {
	Skipped        bool
	OversizedEvent bool
	EventName      string
	DataLineCount  int
	DataBytes      int
	PendingLine    string
}

// HasPendingSseProtocolEvent mirrors hasPendingSseProtocolEvent: true when
// buffered state suggests an SSE event is still being assembled.
func HasPendingSseProtocolEvent(state SsePendingEventState) bool {
	if state.Skipped {
		return false
	}
	if state.OversizedEvent ||
		state.EventName != "" ||
		state.DataLineCount > 0 ||
		state.DataBytes > 0 {
		return true
	}
	pendingLine := stripTrailingCarriageReturn(state.PendingLine)
	return startsWithField(pendingLine, "event:") || startsWithField(pendingLine, "data:")
}

// HasPendingSseProtocolEventBytes 与 HasPendingSseProtocolEvent 同语义，但允许
// 调用方直接传入 pending 行字节视图：流式 inspector 每个 chunk 都要出快照，
// 字节入参避免只为前缀判定而复制整行。两处判定逻辑必须同步修改。
func HasPendingSseProtocolEventBytes(state SsePendingEventState, pendingLine []byte) bool {
	if state.Skipped {
		return false
	}
	if state.OversizedEvent ||
		state.EventName != "" ||
		state.DataLineCount > 0 ||
		state.DataBytes > 0 {
		return true
	}
	if len(pendingLine) > 0 && pendingLine[len(pendingLine)-1] == '\r' {
		pendingLine = pendingLine[:len(pendingLine)-1]
	}
	return startsWithFieldBytes(pendingLine, "event:") || startsWithFieldBytes(pendingLine, "data:")
}

func startsWithFieldBytes(value []byte, field string) bool {
	return len(value) >= len(field) && string(value[:len(field)]) == field
}

func stripTrailingCarriageReturn(value string) string {
	if len(value) > 0 && value[len(value)-1] == '\r' {
		return value[:len(value)-1]
	}
	return value
}

func startsWithField(value, field string) bool {
	return len(value) >= len(field) && value[:len(field)] == field
}
