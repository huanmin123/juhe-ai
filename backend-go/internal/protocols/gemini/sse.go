package gemini

import (
	"bytes"
	"errors"
	"strings"
)

const (
	DefaultSSEEventMaxBytes = 256 << 10
	MaxSSEEventBytes        = 1 << 20
	DefaultSSETotalMaxBytes = 64 << 20
	MaxSSETotalBytes        = 256 << 20
)

var (
	ErrEventTooLarge  = errors.New("gemini: SSE event too large")
	ErrStreamTooLarge = errors.New("gemini: SSE stream too large")
	ErrParserFinished = errors.New("gemini: SSE parser already finished")
)

type SSEOptions struct {
	MaxEventBytes int
	MaxTotalBytes int
}

// SSEParser incrementally consumes Gemini SSE without retaining the full
// stream. A stream is terminal only after an explicit protocol signal.
type SSEParser struct {
	maxEventBytes int
	maxTotalBytes int
	totalBytes    int
	line          []byte
	afterCR       bool
	eventName     string
	data          []byte
	result        Result
	fatal         error
	finished      bool
}

func NewSSEParser(options SSEOptions) *SSEParser {
	maxEventBytes, eventErr := normalizedLimit(options.MaxEventBytes, DefaultSSEEventMaxBytes, MaxSSEEventBytes)
	maxTotalBytes, totalErr := normalizedLimit(options.MaxTotalBytes, DefaultSSETotalMaxBytes, MaxSSETotalBytes)
	if eventErr == nil {
		eventErr = totalErr
	}
	return &SSEParser{maxEventBytes: maxEventBytes, maxTotalBytes: maxTotalBytes, fatal: eventErr}
}

func (parser *SSEParser) Push(chunk []byte) error {
	if parser.finished {
		return ErrParserFinished
	}
	if parser.fatal != nil {
		return parser.fatal
	}
	if len(chunk) > parser.maxTotalBytes-parser.totalBytes {
		parser.fatal = ErrStreamTooLarge
		return parser.fatal
	}
	parser.totalBytes += len(chunk)
	for _, value := range chunk {
		if parser.afterCR {
			parser.afterCR = false
			if value == '\n' {
				continue
			}
		}
		if value == '\r' || value == '\n' {
			if err := parser.processBufferedLine(); err != nil {
				parser.fatal = err
				return err
			}
			parser.afterCR = value == '\r'
			continue
		}
		if parser.currentEventBytes()+1 > parser.maxEventBytes {
			parser.fatal = ErrEventTooLarge
			return parser.fatal
		}
		parser.line = append(parser.line, value)
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
	parser.afterCR = false
	if len(parser.line) > 0 {
		if err := parser.processBufferedLine(); err != nil {
			parser.fatal = err
			return parser.snapshot(), err
		}
	}
	parser.flushEvent()
	return parser.snapshot(), nil
}

func (parser *SSEParser) processBufferedLine() error {
	line := parser.line
	parser.line = nil
	return parser.processLine(line)
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
	if parser.result.Terminal {
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
		parser.markFailed(observed)
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
		parser.markFailed(observed)
	case "finish", "done", "[done]":
		parser.markCompleted("completed")
	}
	if reason := firstFinishReason(value); reason != "" {
		parser.markCompleted(reason)
	}
	if equalFold(observed.Status, "completed") {
		parser.markCompleted(observed.Status)
	} else if equalFold(observed.Status, "failed") {
		parser.markFailed(observed)
	}
	parser.resetEvent()
}

func (parser *SSEParser) markCompleted(status string) {
	if parser.result.Terminal {
		return
	}
	parser.result.Terminal = true
	parser.result.Status = status
}

func (parser *SSEParser) markFailed(observed Result) {
	if parser.result.Terminal {
		return
	}
	parser.result.Terminal = true
	parser.result.Failed = true
	parser.result.Status = "failed"
	parser.result.ErrorCode = observed.ErrorCode
	parser.result.ErrorMessage = observed.ErrorMessage
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
