package gemini

import (
	"bytes"
	"errors"
	"strings"
)

const (
	DefaultSSEEventMaxBytes = 256 << 10
	MaxSSEEventBytes        = 1 << 20
)

var (
	ErrEventTooLarge  = errors.New("gemini: SSE event too large")
	ErrParserFinished = errors.New("gemini: SSE parser already finished")
)

type SSEOptions struct {
	MaxEventBytes int
}

// SSEParser incrementally consumes Gemini SSE without retaining the full
// stream. A stream is terminal only after an explicit protocol signal.
type SSEParser struct {
	maxEventBytes int
	line          []byte
	eventName     string
	data          []byte
	result        Result
	fatal         error
	finished      bool
}

func NewSSEParser(options SSEOptions) *SSEParser {
	maxBytes, err := normalizedLimit(options.MaxEventBytes, DefaultSSEEventMaxBytes, MaxSSEEventBytes)
	return &SSEParser{maxEventBytes: maxBytes, fatal: err}
}

func (parser *SSEParser) Push(chunk []byte) error {
	if parser.finished {
		return ErrParserFinished
	}
	if parser.fatal != nil {
		return parser.fatal
	}
	for len(chunk) > 0 {
		newline := bytes.IndexByte(chunk, '\n')
		if newline < 0 {
			if parser.currentEventBytes()+len(chunk) > parser.maxEventBytes {
				parser.fatal = ErrEventTooLarge
				return parser.fatal
			}
			parser.line = append(parser.line, chunk...)
			return nil
		}
		if parser.currentEventBytes()+newline > parser.maxEventBytes {
			parser.fatal = ErrEventTooLarge
			return parser.fatal
		}
		parser.line = append(parser.line, chunk[:newline]...)
		line := parser.line
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		parser.line = nil
		if err := parser.processLine(line); err != nil {
			parser.fatal = err
			return err
		}
		chunk = chunk[newline+1:]
	}
	return nil
}

func (parser *SSEParser) Finish() (Result, error) {
	if parser.fatal != nil {
		return parser.snapshot(), parser.fatal
	}
	if parser.finished {
		return parser.snapshot(), ErrParserFinished
	}
	parser.finished = true
	if len(parser.line) > 0 {
		line := parser.line
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		parser.line = nil
		if err := parser.processLine(line); err != nil {
			parser.fatal = err
			return parser.snapshot(), err
		}
	}
	parser.flushEvent()
	return parser.snapshot(), nil
}

func (parser *SSEParser) Snapshot() Result {
	return parser.snapshot()
}

func (parser *SSEParser) processLine(line []byte) error {
	if len(line) == 0 {
		parser.flushEvent()
		return nil
	}
	if line[0] == ':' {
		return nil
	}
	field, value, hasColon := bytes.Cut(line, []byte{':'})
	if !hasColon {
		value = nil
	}
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	switch string(field) {
	case "event":
		parser.eventName = string(value)
	case "data":
		if len(parser.data) > 0 {
			parser.data = append(parser.data, '\n')
		}
		parser.data = append(parser.data, value...)
	}
	if parser.currentEventBytes() > parser.maxEventBytes {
		return ErrEventTooLarge
	}
	return nil
}

func (parser *SSEParser) flushEvent() {
	if len(parser.data) == 0 {
		parser.resetEvent()
		return
	}
	parser.result.Events++
	data := strings.TrimSpace(string(parser.data))
	if data == "[DONE]" {
		parser.markCompleted("completed")
		parser.resetEvent()
		return
	}
	value, err := decodeJSONObject([]byte(data))
	if err != nil {
		parser.result.MalformedEvents++
		parser.resetEvent()
		return
	}
	parser.result.Usage = mergeUsage(parser.result.Usage, extractUsage(value))
	observed := resultFromObject(value)
	if parser.result.InteractionID == "" {
		parser.result.InteractionID = observed.InteractionID
	}
	if observed.Failed {
		parser.result.Terminal = true
		parser.result.Failed = true
		parser.result.Status = "failed"
		parser.result.ErrorCode = observed.ErrorCode
		parser.result.ErrorMessage = observed.ErrorMessage
	}

	eventType := firstString(value, "type", "event_type")
	if eventType == "" {
		eventType = parser.eventName
	}
	switch strings.ToLower(eventType) {
	case "interaction.completed":
		status := observed.Status
		if status == "" {
			status = "completed"
		}
		parser.markCompleted(status)
	case "interaction.failed":
		parser.result.Terminal = true
		parser.result.Failed = true
		parser.result.Status = "failed"
	case "finish", "done", "[done]":
		parser.markCompleted("completed")
	}
	if reason := firstFinishReason(value); reason != "" {
		parser.markCompleted(reason)
	}
	if equalFold(observed.Status, "completed") {
		parser.markCompleted(observed.Status)
	} else if equalFold(observed.Status, "failed") {
		parser.result.Terminal = true
		parser.result.Failed = true
		parser.result.Status = "failed"
	}
	parser.resetEvent()
}

func (parser *SSEParser) markCompleted(status string) {
	if parser.result.Failed {
		return
	}
	parser.result.Terminal = true
	parser.result.Status = status
}

func (parser *SSEParser) resetEvent() {
	parser.eventName = ""
	parser.data = nil
}

func (parser *SSEParser) currentEventBytes() int {
	return len(parser.line) + len(parser.eventName) + len(parser.data)
}

func (parser *SSEParser) snapshot() Result {
	result := parser.result
	result.Usage = cloneUsage(result.Usage)
	result.Pending = !parser.finished && (len(parser.line) > 0 || parser.eventName != "" || len(parser.data) > 0)
	return result
}

func cloneUsage(value Usage) Usage {
	return Usage{
		ReportedServiceTier: value.ReportedServiceTier,
		InputTokens:         cloneInt64(value.InputTokens),
		OutputTokens:        cloneInt64(value.OutputTokens),
		CacheReadTokens:     cloneInt64(value.CacheReadTokens),
		ThinkingTokens:      cloneInt64(value.ThinkingTokens),
	}
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func firstFinishReason(value map[string]any) string {
	candidates, _ := value["candidates"].([]any)
	for _, candidate := range candidates {
		if reason := stringValue(objectValue(candidate)["finishReason"]); reason != "" {
			return reason
		}
	}
	return ""
}
