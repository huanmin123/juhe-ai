package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
)

const (
	defaultSSEMaxLineBytes  int64 = 256 * 1024
	defaultSSEMaxEventBytes int64 = 512 * 1024
	defaultSSEMaxTotalBytes int64 = 64 * 1024 * 1024
)

var (
	ErrSSELineTooLarge   = errors.New("openai SSE line exceeds limit")
	ErrSSEEventTooLarge  = errors.New("openai SSE event exceeds limit")
	ErrSSEStreamTooLarge = errors.New("openai SSE stream exceeds limit")
	ErrSSEInspectorDone  = errors.New("openai SSE inspector is finished")
)

// SSELimits bounds both retained state and total inspection work.
type SSELimits struct {
	MaxLineBytes  int64
	MaxEventBytes int64
	MaxTotalBytes int64
}

func DefaultSSELimits() SSELimits {
	return SSELimits{
		MaxLineBytes:  defaultSSEMaxLineBytes,
		MaxEventBytes: defaultSSEMaxEventBytes,
		MaxTotalBytes: defaultSSEMaxTotalBytes,
	}
}

type SSELimitScope string

const (
	SSELimitLine   SSELimitScope = "line"
	SSELimitEvent  SSELimitScope = "event"
	SSELimitStream SSELimitScope = "stream"
)

type SSELimitError struct {
	Scope    SSELimitScope
	Limit    int64
	Observed int64
}

func (e *SSELimitError) Error() string {
	return fmt.Sprintf("openai SSE %s exceeds limit: observed=%d limit=%d", e.Scope, e.Observed, e.Limit)
}

func (e *SSELimitError) Is(target error) bool {
	switch e.Scope {
	case SSELimitLine:
		return target == ErrSSELineTooLarge
	case SSELimitEvent:
		return target == ErrSSEEventTooLarge
	case SSELimitStream:
		return target == ErrSSEStreamTooLarge
	default:
		return false
	}
}

type SSEStreamError struct {
	EventType string
	Code      string
	Message   string
}

// SSEUsage keeps presence separate from zero because upstream usage counters may
// legitimately report zero and later events replace only fields they contain.
type SSEUsage struct {
	ServiceTier        *string
	InputTokens        *int64
	OutputTokens       *int64
	CacheReadTokens    *int64
	CacheWriteTokens   *int64
	CacheWrite1hTokens *int64
	ThinkingTokens     *int64
	InputImageTokens   *int64
	OutputImageTokens  *int64
	InputAudioTokens   *int64
	OutputAudioTokens  *int64
	OutputImageCount   *int64
}

type SSEInspection struct {
	TotalBytes          int64
	EventCount          int64
	MalformedEventCount int64
	LastEventType       string
	TerminalReceived    bool
	TerminalEventType   string
	FailedReceived      bool
	FinishReason        string
	Usage               SSEUsage
	Error               *SSEStreamError
	PendingEvent        bool
	Finished            bool
}

// SSEInspector incrementally inspects an OpenAI-compatible SSE stream. It has no
// goroutines or context dependency; one owner feeds it bytes and reads snapshots.
type SSEInspector struct {
	limits SSELimits

	inspection SSEInspection
	line       []byte
	afterCR    bool
	eventName  string
	eventData  []byte
	eventBytes int64
	hasData    bool
	stickyErr  error
}

func NewSSEInspector(limits SSELimits) (*SSEInspector, error) {
	if limits.MaxLineBytes <= 0 || limits.MaxEventBytes <= 0 || limits.MaxTotalBytes <= 0 {
		return nil, fmt.Errorf("openai SSE limits must all be positive: %#v", limits)
	}
	return &SSEInspector{limits: limits}, nil
}

// Write accepts arbitrary byte boundaries, including boundaries inside CRLF and
// UTF-8 sequences. Limit errors are sticky because continuing would make the
// inspection result incomplete and therefore unsafe to trust.
func (i *SSEInspector) Write(p []byte) (int, error) {
	if i.stickyErr != nil {
		return 0, i.stickyErr
	}
	if i.inspection.Finished {
		return 0, ErrSSEInspectorDone
	}
	for offset, value := range p {
		if i.inspection.TotalBytes >= i.limits.MaxTotalBytes {
			return offset, i.failLimit(SSELimitStream, i.limits.MaxTotalBytes, i.inspection.TotalBytes+1)
		}
		if i.afterCR {
			i.afterCR = false
			if value == '\n' {
				i.inspection.TotalBytes++
				continue
			}
		}
		if value == '\r' {
			i.inspection.TotalBytes++
			if err := i.consumeLine(); err != nil {
				return offset + 1, err
			}
			i.afterCR = true
			continue
		}
		if value == '\n' {
			i.inspection.TotalBytes++
			if err := i.consumeLine(); err != nil {
				return offset + 1, err
			}
			continue
		}
		observed := int64(len(i.line)) + 1
		if observed > i.limits.MaxLineBytes {
			return offset, i.failLimit(SSELimitLine, i.limits.MaxLineBytes, observed)
		}
		observedEventBytes := observed
		if i.eventBytes > 0 {
			observedEventBytes += i.eventBytes + 1
		}
		if observedEventBytes > i.limits.MaxEventBytes {
			return offset, i.failLimit(SSELimitEvent, i.limits.MaxEventBytes, observedEventBytes)
		}
		i.line = append(i.line, p[offset])
		i.inspection.TotalBytes++
	}
	return len(p), nil
}

// Finish dispatches a final unterminated line and event. It is idempotent.
func (i *SSEInspector) Finish() error {
	if i.stickyErr != nil {
		return i.stickyErr
	}
	if i.inspection.Finished {
		return nil
	}
	i.afterCR = false
	if len(i.line) > 0 {
		if err := i.consumeLine(); err != nil {
			return err
		}
	}
	if i.pendingEvent() {
		i.dispatchEvent()
	}
	i.inspection.Finished = true
	return nil
}

func (i *SSEInspector) Snapshot() SSEInspection {
	result := i.inspection
	result.PendingEvent = i.pendingEvent()
	result.Usage = cloneSSEUsage(i.inspection.Usage)
	if i.inspection.Error != nil {
		streamErr := *i.inspection.Error
		result.Error = &streamErr
	}
	return result
}

func (i *SSEInspector) consumeLine() error {
	line := i.line
	i.line = nil
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	if len(line) == 0 {
		i.dispatchEvent()
		return nil
	}

	separatorBytes := int64(0)
	if i.eventBytes > 0 {
		separatorBytes = 1
	}
	observed := i.eventBytes + separatorBytes + int64(len(line))
	if observed > i.limits.MaxEventBytes {
		return i.failLimit(SSELimitEvent, i.limits.MaxEventBytes, observed)
	}
	i.eventBytes = observed

	if line[0] == ':' {
		return nil
	}
	field, value, _ := bytes.Cut(line, []byte{':'})
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	switch string(field) {
	case "event":
		i.eventName = strings.TrimSpace(string(value))
	case "data":
		if i.hasData {
			i.eventData = append(i.eventData, '\n')
		}
		i.eventData = append(i.eventData, value...)
		i.hasData = true
	}
	return nil
}

func (i *SSEInspector) dispatchEvent() {
	if !i.hasData {
		i.resetEvent()
		return
	}
	dataText := strings.TrimSpace(string(i.eventData))
	eventName := i.eventName
	i.resetEvent()
	if dataText == "" {
		return
	}

	i.inspection.EventCount++
	if dataText == "[DONE]" {
		i.recordEvent("[DONE]", eventName, nil)
		return
	}

	payload, ok := decodeSSEPayload(dataText)
	if !ok {
		i.inspection.MalformedEventCount++
		i.inspection.LastEventType = firstNonEmpty(eventName, "message")
		return
	}
	eventType := firstNonEmpty(stringField(payload, "type"), stringField(payload, "event_type"), eventName, "message")
	i.recordEvent(eventType, eventName, payload)
}

func (i *SSEInspector) recordEvent(eventType, eventName string, payload map[string]any) {
	i.inspection.LastEventType = eventType
	if isSSETerminalEvent(eventType) && !i.inspection.TerminalReceived {
		i.inspection.TerminalReceived = true
		i.inspection.TerminalEventType = eventType
	}

	if payload == nil {
		return
	}
	i.inspection.Usage = mergeSSEUsage(i.inspection.Usage, extractSSEUsage(payload))
	if finishReason := extractFinishReason(payload); finishReason != "" {
		i.inspection.FinishReason = finishReason
	}

	streamErr := extractSSEStreamError(payload, eventType)
	failed := isSSEFailureEvent(eventType) || isSSEFailureEvent(eventName) || streamErr != nil
	if failed {
		i.inspection.FailedReceived = true
		if i.inspection.Error == nil && streamErr != nil {
			streamErr.EventType = eventType
			i.inspection.Error = streamErr
		}
	}
}

func (i *SSEInspector) pendingEvent() bool {
	return len(i.line) > 0 || i.eventBytes > 0 || i.eventName != "" || i.hasData
}

func (i *SSEInspector) resetEvent() {
	i.eventName = ""
	i.eventData = nil
	i.eventBytes = 0
	i.hasData = false
}

func (i *SSEInspector) failLimit(scope SSELimitScope, limit, observed int64) error {
	i.line = nil
	i.afterCR = false
	i.resetEvent()
	i.stickyErr = &SSELimitError{Scope: scope, Limit: limit, Observed: observed}
	return i.stickyErr
}

func decodeSSEPayload(text string) (map[string]any, bool) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		return nil, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, false
	}
	return payload, true
}

func isSSETerminalEvent(eventType string) bool {
	switch eventType {
	case "[DONE]", "response.completed", "response.done", "response.incomplete", "response.failed", "image_generation.completed", "image_generation.failed":
		return true
	default:
		return false
	}
}

func isSSEFailureEvent(eventType string) bool {
	switch eventType {
	case "response.failed", "image_generation.failed", "error":
		return true
	default:
		return false
	}
}

func extractSSEStreamError(payload map[string]any, eventType string) *SSEStreamError {
	if eventType == "response.mcp_call.failed" {
		return nil
	}
	response := objectField(payload, "response")
	if responseError := objectField(response, "error"); responseError != nil {
		return streamErrorFromObject(responseError)
	}
	if rootError := objectField(payload, "error"); rootError != nil {
		return streamErrorFromObject(rootError)
	}
	if eventType == "error" {
		return streamErrorFromObject(payload)
	}
	return nil
}

func streamErrorFromObject(value map[string]any) *SSEStreamError {
	code := stringField(value, "code")
	message := stringField(value, "message")
	if code == "" && message == "" {
		return nil
	}
	return &SSEStreamError{Code: code, Message: message}
}

func extractFinishReason(payload map[string]any) string {
	choices, _ := payload["choices"].([]any)
	for _, value := range choices {
		choice, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if reason := stringField(choice, "finish_reason"); reason != "" {
			return reason
		}
	}
	return ""
}

func extractSSEUsage(payload map[string]any) SSEUsage {
	response := objectField(payload, "response")
	usage := objectField(response, "usage")
	if usage == nil {
		usage = objectField(payload, "usage")
	}
	if usage == nil {
		return SSEUsage{}
	}

	responsesInputDetails := objectField(usage, "input_tokens_details")
	chatInputDetails := objectField(usage, "prompt_tokens_details")
	responsesCacheCreation := objectField(responsesInputDetails, "cache_creation")
	chatCacheCreation := objectField(chatInputDetails, "cache_creation")
	rootCacheCreation := objectField(usage, "cache_creation")
	outputDetails := objectField(usage, "output_tokens_details")
	if outputDetails == nil {
		outputDetails = objectField(usage, "completion_tokens_details")
	}
	cacheWrite5m := firstNonnegativeInt64(
		valueField(responsesInputDetails, "cache_write_5m_tokens"),
		valueField(responsesInputDetails, "cache_write_5m_input_tokens"),
		valueField(responsesInputDetails, "cache_creation_5m_tokens"),
		valueField(responsesInputDetails, "cache_creation_5m_input_tokens"),
		valueField(responsesCacheCreation, "ephemeral_5m_input_tokens"),
		valueField(chatInputDetails, "cache_write_5m_tokens"),
		valueField(chatInputDetails, "cache_write_5m_input_tokens"),
		valueField(chatInputDetails, "cache_creation_5m_tokens"),
		valueField(chatInputDetails, "cache_creation_5m_input_tokens"),
		valueField(chatCacheCreation, "ephemeral_5m_input_tokens"),
		usage["cache_write_5m_tokens"],
		usage["cache_write_5m_input_tokens"],
		usage["cache_creation_5m_tokens"],
		usage["cache_creation_5m_input_tokens"],
		usage["cache_creation_5_m_tokens"],
		usage["claude_cache_creation_5m_tokens"],
		usage["claude_cache_creation_5_m_tokens"],
		valueField(rootCacheCreation, "ephemeral_5m_input_tokens"),
	)
	cacheWrite1h := firstNonnegativeInt64(
		valueField(responsesInputDetails, "cache_write_1h_tokens"),
		valueField(responsesInputDetails, "cache_write_1h_input_tokens"),
		valueField(responsesInputDetails, "cache_creation_1h_tokens"),
		valueField(responsesInputDetails, "cache_creation_1h_input_tokens"),
		valueField(responsesCacheCreation, "ephemeral_1h_input_tokens"),
		valueField(chatInputDetails, "cache_write_1h_tokens"),
		valueField(chatInputDetails, "cache_write_1h_input_tokens"),
		valueField(chatInputDetails, "cache_creation_1h_tokens"),
		valueField(chatInputDetails, "cache_creation_1h_input_tokens"),
		valueField(chatCacheCreation, "ephemeral_1h_input_tokens"),
		usage["cache_write_1h_tokens"],
		usage["cache_write_1h_input_tokens"],
		usage["cache_creation_1h_tokens"],
		usage["cache_creation_1h_input_tokens"],
		usage["cache_creation_1_h_tokens"],
		usage["claude_cache_creation_1h_tokens"],
		usage["claude_cache_creation_1_h_tokens"],
		valueField(rootCacheCreation, "ephemeral_1h_input_tokens"),
	)
	cacheWrite := firstNonnegativeInt64(
		valueField(responsesInputDetails, "cache_write_tokens"),
		valueField(responsesInputDetails, "cache_write_input_tokens"),
		valueField(responsesInputDetails, "cache_creation_tokens"),
		valueField(responsesInputDetails, "cache_creation_input_tokens"),
		valueField(chatInputDetails, "cache_write_tokens"),
		valueField(chatInputDetails, "cache_write_input_tokens"),
		valueField(chatInputDetails, "cache_creation_tokens"),
		valueField(chatInputDetails, "cache_creation_input_tokens"),
		usage["cache_write_tokens"],
		usage["cache_write_input_tokens"],
		usage["cache_creation_tokens"],
		usage["cache_creation_input_tokens"],
	)
	if cacheWrite == nil {
		cacheWrite = sumPresentInt64(cacheWrite5m, cacheWrite1h)
	}
	serviceTier := firstValidCapabilityToken(
		valueField(response, "service_tier"),
		payload["service_tier"],
		usage["service_tier"],
	)
	outputImageCount := firstNonnegativeInt64(usage["output_image_count"], usage["output_images"], usage["image_count"])
	if outputImageCount != nil && *outputImageCount <= 0 {
		outputImageCount = nil
	}
	return SSEUsage{
		ServiceTier:        serviceTier,
		InputTokens:        firstNonnegativeInt64(usage["input_tokens"], usage["prompt_tokens"]),
		OutputTokens:       firstNonnegativeInt64(usage["output_tokens"], usage["completion_tokens"]),
		CacheReadTokens:    firstNonnegativeInt64(valueField(responsesInputDetails, "cached_tokens"), valueField(chatInputDetails, "cached_tokens"), usage["prompt_cache_hit_tokens"]),
		CacheWriteTokens:   cacheWrite,
		CacheWrite1hTokens: cacheWrite1h,
		ThinkingTokens:     firstNonnegativeInt64(valueField(outputDetails, "reasoning_tokens")),
		InputImageTokens:   firstNonnegativeInt64(valueField(responsesInputDetails, "image_tokens"), valueField(chatInputDetails, "image_tokens")),
		OutputImageTokens:  firstNonnegativeInt64(valueField(outputDetails, "image_tokens")),
		InputAudioTokens:   firstNonnegativeInt64(valueField(responsesInputDetails, "audio_tokens"), valueField(chatInputDetails, "audio_tokens")),
		OutputAudioTokens:  firstNonnegativeInt64(valueField(outputDetails, "audio_tokens")),
		OutputImageCount:   outputImageCount,
	}
}

func mergeSSEUsage(current, next SSEUsage) SSEUsage {
	if next.ServiceTier != nil {
		current.ServiceTier = cloneString(next.ServiceTier)
	}
	mergeInt64 := func(currentValue **int64, nextValue *int64) {
		if nextValue != nil {
			*currentValue = cloneInt64(nextValue)
		}
	}
	mergeInt64(&current.InputTokens, next.InputTokens)
	mergeInt64(&current.OutputTokens, next.OutputTokens)
	mergeInt64(&current.CacheReadTokens, next.CacheReadTokens)
	mergeInt64(&current.CacheWriteTokens, next.CacheWriteTokens)
	mergeInt64(&current.CacheWrite1hTokens, next.CacheWrite1hTokens)
	mergeInt64(&current.ThinkingTokens, next.ThinkingTokens)
	mergeInt64(&current.InputImageTokens, next.InputImageTokens)
	mergeInt64(&current.OutputImageTokens, next.OutputImageTokens)
	mergeInt64(&current.InputAudioTokens, next.InputAudioTokens)
	mergeInt64(&current.OutputAudioTokens, next.OutputAudioTokens)
	mergeInt64(&current.OutputImageCount, next.OutputImageCount)
	return current
}

func cloneSSEUsage(value SSEUsage) SSEUsage {
	value.ServiceTier = cloneString(value.ServiceTier)
	value.InputTokens = cloneInt64(value.InputTokens)
	value.OutputTokens = cloneInt64(value.OutputTokens)
	value.CacheReadTokens = cloneInt64(value.CacheReadTokens)
	value.CacheWriteTokens = cloneInt64(value.CacheWriteTokens)
	value.CacheWrite1hTokens = cloneInt64(value.CacheWrite1hTokens)
	value.ThinkingTokens = cloneInt64(value.ThinkingTokens)
	value.InputImageTokens = cloneInt64(value.InputImageTokens)
	value.OutputImageTokens = cloneInt64(value.OutputImageTokens)
	value.InputAudioTokens = cloneInt64(value.InputAudioTokens)
	value.OutputAudioTokens = cloneInt64(value.OutputAudioTokens)
	value.OutputImageCount = cloneInt64(value.OutputImageCount)
	return value
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func firstNonnegativeInt64(values ...any) *int64 {
	for _, value := range values {
		var text string
		switch typed := value.(type) {
		case json.Number:
			text = typed.String()
		case string:
			text = strings.TrimSpace(typed)
			if text == "" {
				continue
			}
		default:
			continue
		}
		parsed, _, err := big.ParseFloat(text, 10, 256, big.ToZero)
		if err != nil || parsed.Sign() < 0 || parsed.IsInf() {
			continue
		}
		integer, _ := parsed.Int(nil)
		if integer.IsInt64() {
			count := integer.Int64()
			return &count
		}
	}
	return nil
}

func sumPresentInt64(values ...*int64) *int64 {
	var total int64
	found := false
	for _, value := range values {
		if value == nil {
			continue
		}
		found = true
		if *value > 0 && total > int64(^uint64(0)>>1)-*value {
			return nil
		}
		total += *value
	}
	if !found {
		return nil
	}
	return &total
}

func firstValidCapabilityToken(values ...any) *string {
	for _, value := range values {
		text, ok := value.(string)
		if !ok || text == "" || text != strings.TrimSpace(text) || len(text) > 64 {
			continue
		}
		valid := true
		for index, char := range text {
			if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || (index > 0 && (char == '.' || char == '_' || char == '-')) {
				continue
			}
			valid = false
			break
		}
		if valid {
			return &text
		}
	}
	return nil
}

func objectField(value map[string]any, key string) map[string]any {
	if value == nil {
		return nil
	}
	result, _ := value[key].(map[string]any)
	return result
}

func valueField(value map[string]any, key string) any {
	if value == nil {
		return nil
	}
	return value[key]
}

func stringField(value map[string]any, key string) string {
	if value == nil {
		return ""
	}
	result, _ := value[key].(string)
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
