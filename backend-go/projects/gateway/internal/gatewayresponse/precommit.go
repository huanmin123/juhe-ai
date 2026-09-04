package gatewayresponse

// StreamPreCommitBufferMaxBytes 对齐 streamPreCommitBufferMaxBytes。
const StreamPreCommitBufferMaxBytes = 256 * 1024

// PreCommitBufferState 对齐 StreamPreCommitBufferState。
type PreCommitBufferState struct {
	Buffering     bool
	BufferedBytes int
	Chunks        [][]byte
}

// NewPreCommitBufferState 对齐 createStreamPreCommitBufferState。
func NewPreCommitBufferState(enabled bool) *PreCommitBufferState {
	return &PreCommitBufferState{Buffering: enabled}
}

// PreCommitInspectionState 对齐 StreamPreCommitInspectionState。
type PreCommitInspectionState struct {
	OutputReceived   bool
	TerminalReceived bool
	FailedReceived   bool
	Skipped          bool
}

// PreCommitResponseState 对齐 StreamPreCommitResponseState。
type PreCommitResponseState struct {
	HeadersSent   bool
	WritableEnded bool
	Destroyed     bool
}

// CanKeepStreamPreCommitChunk 对齐 canKeepStreamPreCommitChunk。
func CanKeepStreamPreCommitChunk(state *PreCommitBufferState, inspection PreCommitInspectionState, chunk []byte, totalResponseBytes int64, response PreCommitResponseState) bool {
	return state.Buffering &&
		totalResponseBytes == 0 &&
		!inspection.OutputReceived &&
		!inspection.TerminalReceived &&
		!inspection.FailedReceived &&
		!inspection.Skipped &&
		state.BufferedBytes+len(chunk) <= StreamPreCommitBufferMaxBytes &&
		responseCanStillFailBeforeCommit(response)
}

// WouldExceedStreamPreCommitBuffer 对齐 wouldExceedStreamPreCommitBuffer。
func WouldExceedStreamPreCommitBuffer(state *PreCommitBufferState, chunk []byte) bool {
	return state.Buffering && state.BufferedBytes+len(chunk) > StreamPreCommitBufferMaxBytes
}

// AppendStreamPreCommitChunk 对齐 appendStreamPreCommitChunk。
func AppendStreamPreCommitChunk(state *PreCommitBufferState, chunk []byte) {
	state.Chunks = append(state.Chunks, chunk)
	state.BufferedBytes += len(chunk)
}

// ClearStreamPreCommitChunks 对齐 clearStreamPreCommitChunks。
func ClearStreamPreCommitChunks(state *PreCommitBufferState) {
	state.BufferedBytes = 0
	state.Chunks = nil
}

// TakeStreamPreCommitChunks 对齐 takeStreamPreCommitChunks。
func TakeStreamPreCommitChunks(state *PreCommitBufferState) [][]byte {
	state.Buffering = false
	state.BufferedBytes = 0
	chunks := state.Chunks
	state.Chunks = nil
	return chunks
}

// ShouldFailBeforeStreamDownstreamCommit 对齐
// shouldFailBeforeStreamDownstreamCommit。
func ShouldFailBeforeStreamDownstreamCommit(state *PreCommitBufferState, totalResponseBytes int64, response PreCommitResponseState) bool {
	return state.Buffering &&
		totalResponseBytes == 0 &&
		responseCanStillFailBeforeCommit(response)
}

// UncommittedStreamResponseBody 对齐 uncommittedStreamResponseBody。
func UncommittedStreamResponseBody(state *PreCommitBufferState) []byte {
	if len(state.Chunks) == 0 {
		return nil
	}
	size := 0
	for _, chunk := range state.Chunks {
		size += len(chunk)
	}
	out := make([]byte, 0, size)
	for _, chunk := range state.Chunks {
		out = append(out, chunk...)
	}
	return out
}

func responseCanStillFailBeforeCommit(response PreCommitResponseState) bool {
	return !response.WritableEnded && !response.Destroyed
}

// sseLineKind 对齐 SseLineKind。
type sseLineKind int

const (
	sseLineEmpty sseLineKind = iota
	sseLineComment
	sseLineFieldCandidate
	sseLineData
	sseLineOther
)

// StreamPreCommitSseEvidence 对齐 StreamPreCommitSseEvidence：只跟踪标准 SSE
// 帧；不解释事件名、JSON 载荷、供应商代码、状态码或错误信息。
type StreamPreCommitSseEvidence struct {
	DataEventObserved            bool
	DataPayloadStarted           bool
	OnlyNonSemanticFramingObserved bool

	lineKind                  sseLineKind
	fieldCandidate            []byte
	dataValueCanSkipLeadingSpace bool
	currentDataLineHasValue   bool
	currentEventHasData       bool
	carriageReturnPending     bool
}

// NewStreamPreCommitSseEvidence 构造证据机。
func NewStreamPreCommitSseEvidence() *StreamPreCommitSseEvidence {
	return &StreamPreCommitSseEvidence{OnlyNonSemanticFramingObserved: true}
}

// Push 对齐 push。
func (e *StreamPreCommitSseEvidence) Push(chunk []byte) {
	for _, b := range chunk {
		switch {
		case b == 0x0a:
			e.finishLine()
			e.carriageReturnPending = false
		case b == 0x0d:
			if e.carriageReturnPending {
				e.finishLine()
			}
			e.carriageReturnPending = true
		default:
			if e.carriageReturnPending {
				e.finishLine()
				e.carriageReturnPending = false
			}
			e.pushLineByte(b)
		}
	}
}

// Finish 对齐 finish()。
func (e *StreamPreCommitSseEvidence) Finish() {
	if e.carriageReturnPending || e.lineKind != sseLineEmpty {
		e.finishLine()
		e.carriageReturnPending = false
	}
	e.finishEvent()
}

func (e *StreamPreCommitSseEvidence) pushLineByte(b byte) {
	switch e.lineKind {
	case sseLineEmpty:
		if b == 0x3a { // ':'
			e.lineKind = sseLineComment
			return
		}
		e.OnlyNonSemanticFramingObserved = false
		e.lineKind = sseLineFieldCandidate
		e.fieldCandidate = append(e.fieldCandidate[:0], b)
		return
	case sseLineComment, sseLineOther:
		return
	case sseLineData:
		if e.dataValueCanSkipLeadingSpace {
			e.dataValueCanSkipLeadingSpace = false
			if b == 0x20 {
				return
			}
		}
		e.currentDataLineHasValue = true
		e.DataPayloadStarted = true
		return
	case sseLineFieldCandidate:
		if b == 0x3a { // ':'
			if string(e.fieldCandidate) == "data" {
				e.lineKind = sseLineData
				e.dataValueCanSkipLeadingSpace = true
			} else {
				e.lineKind = sseLineOther
			}
			return
		}
		if len(e.fieldCandidate) >= 4 {
			e.lineKind = sseLineOther
			return
		}
		e.fieldCandidate = append(e.fieldCandidate, b)
	}
}

func (e *StreamPreCommitSseEvidence) finishLine() {
	if e.lineKind == sseLineEmpty {
		e.finishEvent()
		return
	}
	if e.lineKind == sseLineData && e.currentDataLineHasValue {
		e.currentEventHasData = true
	}
	e.lineKind = sseLineEmpty
	e.fieldCandidate = e.fieldCandidate[:0]
	e.dataValueCanSkipLeadingSpace = false
	e.currentDataLineHasValue = false
}

func (e *StreamPreCommitSseEvidence) finishEvent() {
	if e.currentEventHasData {
		e.DataEventObserved = true
		e.OnlyNonSemanticFramingObserved = false
	} else if !e.DataEventObserved {
		// A completed event containing only comments, empty data fields, or
		// metadata fields has no client-visible SSE data and may be discarded.
		e.OnlyNonSemanticFramingObserved = true
	}
	e.currentEventHasData = false
}
