package gatewaystreamrelay

import "bytes"

// SSEPreCommitBufferBytes bounds opaque SSE framing retained before a client
// visible data payload is observed. It intentionally mirrors the Node gateway
// boundary without parsing provider event names or JSON payloads.
const SSEPreCommitBufferBytes = 256 * 1024

// NewSSEPreCommitInspector returns a request-local inspector for generic SSE
// relays. Until a non-empty standard SSE data field is observed it retains only
// framing that could belong to that data event; comments and completed empty
// metadata events stay private. The inspector is not safe for concurrent use
// because Relay invokes it synchronously for one request.
func NewSSEPreCommitInspector() TransformingInspector {
	return &ssePreCommitInspector{evidence: sseEvidence{onlyNonSemanticFramingObserved: true}}
}

type ssePreCommitInspector struct {
	evidence sseEvidence
	pending  [][]byte
	bytes    int
	semantic bool
}

func (s *ssePreCommitInspector) Observe([]byte) error { return nil }

func (s *ssePreCommitInspector) Transform(chunk []byte) ([]byte, error) {
	if s.semantic {
		return append([]byte(nil), chunk...), nil
	}
	s.evidence.push(chunk)
	if s.evidence.onlyNonSemanticFramingObserved {
		s.pending = nil
		s.bytes = 0
		return nil, nil
	}
	if s.evidence.dataPayloadStarted || s.evidence.dataEventObserved {
		s.semantic = true
		output := make([]byte, 0, s.bytes+len(chunk))
		for _, pending := range s.pending {
			output = append(output, pending...)
		}
		output = append(output, chunk...)
		s.pending = nil
		s.bytes = 0
		return output, nil
	}
	if len(chunk) > SSEPreCommitBufferBytes-s.bytes {
		return nil, ErrPreCommitBufferExceeded
	}
	s.pending = append(s.pending, append([]byte(nil), chunk...))
	s.bytes += len(chunk)
	return nil, nil
}

func (s *ssePreCommitInspector) FinishTransform() ([]byte, error) {
	if !s.semantic {
		s.evidence.finish()
		if !s.evidence.dataEventObserved {
			s.pending = nil
			s.bytes = 0
			return nil, ErrPreCommitEvidenceMissing
		}
		// dataPayloadStarted is set on the first non-space data byte, so this
		// branch is defensive only. It keeps a complete EOF data event visible.
		s.semantic = true
	}
	output := joinChunks(s.pending)
	s.pending = nil
	s.bytes = 0
	return output, nil
}

func (s *ssePreCommitInspector) Finish() error { return nil }

func (s *ssePreCommitInspector) Snapshot() Inspection {
	return Inspection{SemanticOutput: s.semantic}
}

func joinChunks(chunks [][]byte) []byte {
	if len(chunks) == 0 {
		return nil
	}
	return bytes.Join(chunks, nil)
}

type sseLineKind uint8

const (
	sseLineEmpty sseLineKind = iota
	sseLineComment
	sseLineFieldCandidate
	sseLineData
	sseLineOther
)

// sseEvidence is a byte-oriented standard SSE lexer. It deliberately accepts
// opaque payloads and only decides whether a non-empty data field exists.
type sseEvidence struct {
	dataEventObserved              bool
	dataPayloadStarted             bool
	onlyNonSemanticFramingObserved bool
	lineKind                       sseLineKind
	fieldCandidate                 []byte
	dataCanSkipLeadingSpace        bool
	currentDataLineHasValue        bool
	currentEventHasData            bool
	carriageReturnPending          bool
}

func (s *sseEvidence) push(chunk []byte) {
	for _, value := range chunk {
		switch value {
		case '\n':
			s.finishLine()
			s.carriageReturnPending = false
		case '\r':
			if s.carriageReturnPending {
				s.finishLine()
			}
			s.carriageReturnPending = true
		default:
			if s.carriageReturnPending {
				s.finishLine()
				s.carriageReturnPending = false
			}
			s.pushLineByte(value)
		}
	}
}

func (s *sseEvidence) finish() {
	if s.carriageReturnPending || s.lineKind != sseLineEmpty {
		s.finishLine()
		s.carriageReturnPending = false
	}
	s.finishEvent()
}

func (s *sseEvidence) pushLineByte(value byte) {
	switch s.lineKind {
	case sseLineEmpty:
		if value == ':' {
			s.lineKind = sseLineComment
			return
		}
		s.onlyNonSemanticFramingObserved = false
		s.lineKind = sseLineFieldCandidate
		s.fieldCandidate = append(s.fieldCandidate[:0], value)
	case sseLineComment, sseLineOther:
		return
	case sseLineData:
		if s.dataCanSkipLeadingSpace {
			s.dataCanSkipLeadingSpace = false
			if value == ' ' {
				return
			}
		}
		s.currentDataLineHasValue = true
		s.dataPayloadStarted = true
	case sseLineFieldCandidate:
		if value == ':' {
			if string(s.fieldCandidate) == "data" {
				s.lineKind = sseLineData
				s.dataCanSkipLeadingSpace = true
			} else {
				s.lineKind = sseLineOther
			}
			return
		}
		if len(s.fieldCandidate) >= len("data") {
			s.lineKind = sseLineOther
			return
		}
		s.fieldCandidate = append(s.fieldCandidate, value)
	}
}

func (s *sseEvidence) finishLine() {
	if s.lineKind == sseLineEmpty {
		s.finishEvent()
		return
	}
	if s.lineKind == sseLineData && s.currentDataLineHasValue {
		s.currentEventHasData = true
	}
	s.lineKind = sseLineEmpty
	s.fieldCandidate = s.fieldCandidate[:0]
	s.dataCanSkipLeadingSpace = false
	s.currentDataLineHasValue = false
}

func (s *sseEvidence) finishEvent() {
	if s.currentEventHasData {
		s.dataEventObserved = true
		s.onlyNonSemanticFramingObserved = false
	} else if !s.dataEventObserved {
		s.onlyNonSemanticFramingObserved = true
	}
	s.currentEventHasData = false
}
