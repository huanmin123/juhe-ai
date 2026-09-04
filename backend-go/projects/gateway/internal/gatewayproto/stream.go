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
	EventTypeCounts       map[string]int
	LastEventType         string
	RecentEventTypes      []string
	PendingEvent          bool
	Skipped               bool
	SkipReason            string
	ErrorCode             string
	ErrorMessage          string
	Usage                 ParsedUsage
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

func stripTrailingCarriageReturn(value string) string {
	if len(value) > 0 && value[len(value)-1] == '\r' {
		return value[:len(value)-1]
	}
	return value
}

func startsWithField(value, field string) bool {
	return len(value) >= len(field) && value[:len(field)] == field
}
