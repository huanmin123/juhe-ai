package gatewayanthropic

import (
	"bytes"
	"errors"
	"strings"

	"juhe-ai/backend-go/internal/modules/gatewayusage"
)

const (
	DefaultSSELineMaxBytes  = 256 << 10
	DefaultSSEEventMaxBytes = 512 << 10
	DefaultSSETotalMaxBytes = 64 << 20
	MaxSSELineBytes         = 1 << 20
	MaxSSEEventBytes        = 2 << 20
	MaxSSETotalBytes        = 256 << 20
)

var (
	ErrLineTooLarge   = errors.New("anthropic: SSE line too large")
	ErrEventTooLarge  = errors.New("anthropic: SSE event too large")
	ErrStreamTooLarge = errors.New("anthropic: SSE stream too large")
	ErrParserFinished = errors.New("anthropic: SSE parser already finished")
)

type SSEOptions struct {
	MaxLineBytes  int
	MaxEventBytes int
	MaxTotalBytes int
}

// SSEParser incrementally inspects Anthropic SSE. The first explicit terminal
// event is authoritative, so bytes after message_stop or error cannot rewrite
// usage or turn a failure into success.
type SSEParser struct {
	maxLineBytes  int
	maxEventBytes int
	maxTotalBytes int
	totalBytes    int
	line          []byte
	afterCR       bool
	eventName     string
	data          []byte
	eventBytes    int
	hasData       bool
	result        Result
	fatal         error
	finished      bool
}

func NewSSEParser(options SSEOptions) (*SSEParser, error) {
	line, err := normalizedLimit(options.MaxLineBytes, DefaultSSELineMaxBytes, MaxSSELineBytes)
	if err != nil {
		return nil, err
	}
	event, err := normalizedLimit(options.MaxEventBytes, DefaultSSEEventMaxBytes, MaxSSEEventBytes)
	if err != nil {
		return nil, err
	}
	total, err := normalizedLimit(options.MaxTotalBytes, DefaultSSETotalMaxBytes, MaxSSETotalBytes)
	if err != nil {
		return nil, err
	}
	return &SSEParser{maxLineBytes: line, maxEventBytes: event, maxTotalBytes: total}, nil
}

func (parser *SSEParser) Push(chunk []byte) error {
	if parser.finished {
		return ErrParserFinished
	}
	if parser.fatal != nil {
		return parser.fatal
	}
	if len(chunk) > parser.maxTotalBytes-parser.totalBytes {
		return parser.fail(ErrStreamTooLarge)
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
			if err := parser.consumeLine(); err != nil {
				return parser.fail(err)
			}
			parser.afterCR = value == '\r'
			continue
		}
		if len(parser.line)+1 > parser.maxLineBytes {
			return parser.fail(ErrLineTooLarge)
		}
		if parser.eventBytes+len(parser.line)+1 > parser.maxEventBytes {
			return parser.fail(ErrEventTooLarge)
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
		return parser.snapshot(), nil
	}
	parser.finished = true
	parser.afterCR = false
	if len(parser.line) > 0 {
		if err := parser.consumeLine(); err != nil {
			return parser.snapshot(), parser.fail(err)
		}
	}
	parser.flushEvent()
	return parser.snapshot(), nil
}

func (parser *SSEParser) Snapshot() Result {
	return parser.snapshot()
}

func (parser *SSEParser) consumeLine() error {
	line := parser.line
	parser.line = nil
	if len(line) == 0 {
		parser.flushEvent()
		return nil
	}
	separator := 0
	if parser.eventBytes > 0 {
		separator = 1
	}
	if parser.eventBytes+separator+len(line) > parser.maxEventBytes {
		return ErrEventTooLarge
	}
	parser.eventBytes += separator + len(line)
	if line[0] == ':' {
		return nil
	}
	fieldName, value, hasColon := bytes.Cut(line, []byte{':'})
	if !hasColon {
		value = nil
	}
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	switch string(fieldName) {
	case "event":
		parser.eventName = strings.TrimSpace(string(value))
	case "data":
		if parser.hasData {
			parser.data = append(parser.data, '\n')
		}
		parser.data = append(parser.data, value...)
		parser.hasData = true
	}
	return nil
}

func (parser *SSEParser) flushEvent() {
	if !parser.hasData {
		parser.resetEvent()
		return
	}
	if parser.result.Terminal {
		parser.resetEvent()
		return
	}
	data := strings.TrimSpace(string(parser.data))
	eventName := parser.eventName
	parser.resetEvent()
	if data == "" {
		return
	}
	parser.result.Events++
	if data == "[DONE]" {
		parser.markCompleted("message_stop")
		return
	}
	value, err := decodeJSONObject([]byte(data))
	if err != nil {
		parser.result.MalformedEvents++
		if equalFold(eventName, "error") {
			parser.markFailed("invalid_sse_error_payload", "Anthropic SSE error event contained invalid JSON")
		}
		return
	}
	eventType := stringValue(value["type"])
	if eventType == "" {
		eventType = eventName
	}
	parser.result.Usage = parser.result.Usage.Merge(usageFromEnvelope(value))
	if delta := objectValue(value["delta"]); delta != nil {
		if reason := stringValue(delta["stop_reason"]); reason != "" {
			parser.result.Status = reason
		}
	}
	observed := resultFromObject(value)
	if observed.Failed || equalFold(eventType, "error") || equalFold(eventName, "error") {
		code := observed.ErrorCode
		message := observed.ErrorMessage
		if code == "" {
			code = "anthropic_stream_error"
		}
		if message == "" {
			message = "Anthropic streaming response failed"
		}
		parser.markFailed(code, message)
		return
	}
	if equalFold(eventType, "message_stop") {
		status := parser.result.Status
		if status == "" {
			status = "message_stop"
		}
		parser.markCompleted(status)
	}
}

func (parser *SSEParser) markCompleted(status string) {
	if parser.result.Terminal {
		return
	}
	parser.result.Terminal = true
	parser.result.Status = status
}

func (parser *SSEParser) markFailed(code, message string) {
	if parser.result.Terminal {
		return
	}
	parser.result.Terminal = true
	parser.result.Failed = true
	parser.result.Status = "failed"
	parser.result.ErrorCode = code
	parser.result.ErrorMessage = message
}

func (parser *SSEParser) resetEvent() {
	parser.eventName = ""
	parser.data = nil
	parser.eventBytes = 0
	parser.hasData = false
}

func (parser *SSEParser) fail(err error) error {
	parser.line = nil
	parser.afterCR = false
	parser.resetEvent()
	parser.fatal = err
	return err
}

func (parser *SSEParser) snapshot() Result {
	result := parser.result
	result.Usage = cloneUsageFacts(result.Usage)
	result.Pending = !parser.finished && parser.fatal == nil && (len(parser.line) > 0 || parser.eventName != "" || parser.hasData)
	return result
}

func cloneUsageFacts(value gatewayusage.UsageFacts) gatewayusage.UsageFacts {
	value.InputTokens = cloneInt64(value.InputTokens)
	value.OutputTokens = cloneInt64(value.OutputTokens)
	value.CacheReadTokens = cloneInt64(value.CacheReadTokens)
	value.CacheWriteTokens = cloneInt64(value.CacheWriteTokens)
	value.CacheWrite1hTokens = cloneInt64(value.CacheWrite1hTokens)
	value.ThinkingTokens = cloneInt64(value.ThinkingTokens)
	return value
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
