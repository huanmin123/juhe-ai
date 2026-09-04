package gatewaygemini

import (
	"encoding/json"
	"strings"
)

// StreamInspection 对齐 GeminiStreamInspection。
type StreamInspection struct {
	TerminalReceived      bool
	FailedReceived        bool
	OutputReceived        bool
	ImageOutputReceived   bool
	OutputEventCount      int
	EstimatedOutputTokens int
	HasEstimatedOutput    bool
	EventCount            int
	EventTypeCounts       map[string]int
	LastEventType         string
	RecentEventTypes      []string
	PendingEvent          bool
	Skipped               bool
	SkipReason            string
	ErrorCode             string
	ErrorMessage          string
	ResponseResourceID    string
	Usage                 ParsedUsage
}

// StreamEventSummary 对齐 GeminiStreamEventSummary。
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

// StreamUsageFallbackInput 对齐 GatewayStreamUsageFallbackInput 中 Gemini
// driver 实际使用的字段。
type StreamUsageFallbackInput struct {
	OutputReceived        bool
	EstimatedOutputTokens int
	HasEstimatedOutput    bool
}

// StreamUsageFallbackResult 对齐 GeminiStreamUsageFallbackResult。
type StreamUsageFallbackResult struct {
	Usage                 ParsedUsage
	Estimated             bool
	EstimatedInputTokens  int
	HasEstimatedInput     bool
	EstimatedOutputTokens int
	HasEstimatedOutput    bool
}

const (
	streamInspectorMaxEventBytes    = 256 * 1024
	streamInspectorRecentEventTypes = 8
	geminiStreamSkipReasonTooLarge  = "gemini_stream_event_too_large"
)

// StreamInspector 对齐 GeminiStreamInspector。
type StreamInspector struct {
	inspection            StreamInspection
	eventName             string
	dataLines             []string
	dataBytes             int
	pendingLine           strings.Builder
	pendingEventSummaries []StreamEventSummary
}

// NewStreamInspector 创建流检查器。
func NewStreamInspector() *StreamInspector {
	return &StreamInspector{inspection: StreamInspection{EventTypeCounts: map[string]int{}, Usage: EmptyUsage()}}
}

// PushChunk 对齐 pushChunk。
func (s *StreamInspector) PushChunk(chunk []byte) StreamInspection {
	return s.PushText(string(chunk))
}

// PushText 对齐 pushText。
func (s *StreamInspector) PushText(text string) StreamInspection {
	if s.inspection.Skipped {
		return s.Snapshot()
	}
	offset := 0
	for offset < len(text) {
		newlineIndex := strings.IndexByte(text[offset:], '\n')
		var segmentEnd int
		if newlineIndex < 0 {
			segmentEnd = len(text)
		} else {
			segmentEnd = offset + newlineIndex
		}
		s.pendingLine.WriteString(strings.TrimSuffix(text[offset:segmentEnd], "\r"))
		if newlineIndex < 0 {
			break
		}
		s.processLine(s.pendingLine.String())
		s.pendingLine.Reset()
		offset = segmentEnd + 1
	}
	return s.Snapshot()
}

// PushParsedEvent 对齐 pushParsedEvent。
func (s *StreamInspector) PushParsedEvent(event StreamEvent, dataBytes int) StreamInspection {
	if s.inspection.Skipped {
		return s.Snapshot()
	}
	if dataBytes == 0 {
		dataBytes = event.DataBytes
		if dataBytes == 0 {
			dataBytes = len(event.DataText)
		}
	}
	s.inspectParsedEvent(event, dataBytes, "")
	return s.Snapshot()
}

// Finish 对齐 finish：冲刷未完结内容；有输出且无失败时补记终止
// （Gemini 流可能在没有显式 finish 事件的情况下直接结束）。
func (s *StreamInspector) Finish() StreamInspection {
	if s.inspection.Skipped {
		return s.Snapshot()
	}
	if s.pendingLine.Len() > 0 {
		s.processLine(s.pendingLine.String())
		s.pendingLine.Reset()
	}
	s.flushEvent()
	if s.inspection.OutputReceived && !s.inspection.FailedReceived {
		s.inspection.TerminalReceived = true
	}
	return s.Snapshot()
}

// Snapshot 对齐 snapshot。
func (s *StreamInspector) Snapshot() StreamInspection {
	snapshot := s.inspection
	snapshot.EventTypeCounts = make(map[string]int, len(s.inspection.EventTypeCounts))
	for key, value := range s.inspection.EventTypeCounts {
		snapshot.EventTypeCounts[key] = value
	}
	snapshot.RecentEventTypes = append([]string(nil), s.inspection.RecentEventTypes...)
	snapshot.Usage = MergeUsage(EmptyUsage(), s.inspection.Usage)
	snapshot.PendingEvent = hasPendingSSEProtocolEvent(
		s.inspection.Skipped,
		s.eventName,
		len(s.dataLines),
		s.dataBytes,
		s.pendingLine.String(),
	)
	return snapshot
}

// DrainEventSummaries 对齐 drainEventSummaries。
func (s *StreamInspector) DrainEventSummaries() []StreamEventSummary {
	summaries := s.pendingEventSummaries
	s.pendingEventSummaries = nil
	return summaries
}

// DrainEventSummariesCanEndStream 对齐 drainEventSummariesCanEndStream。
func (s *StreamInspector) DrainEventSummariesCanEndStream() bool {
	summaries := s.pendingEventSummaries
	s.pendingEventSummaries = nil
	for _, summary := range summaries {
		if summary.CanEndStream {
			return true
		}
	}
	return false
}

func (s *StreamInspector) processLine(line string) {
	if line == "" {
		s.flushEvent()
		return
	}
	if strings.HasPrefix(line, "event:") {
		s.eventName = strings.TrimSpace(line[len("event:"):])
		return
	}
	if !strings.HasPrefix(line, "data:") {
		return
	}
	dataLine := trimStart(line[len("data:"):])
	s.dataBytes += len(dataLine)
	if s.dataBytes > streamInspectorMaxEventBytes {
		s.inspection.Skipped = true
		s.inspection.SkipReason = geminiStreamSkipReasonTooLarge
		s.resetEvent()
		return
	}
	s.dataLines = append(s.dataLines, dataLine)
}

func (s *StreamInspector) flushEvent() {
	if s.inspection.Skipped {
		return
	}
	if len(s.dataLines) == 0 {
		s.resetEvent()
		return
	}
	eventName := s.eventName
	dataText := strings.TrimSpace(strings.Join(s.dataLines, "\n"))
	var rawText strings.Builder
	if eventName != "" {
		rawText.WriteString("event: ")
		rawText.WriteString(eventName)
		rawText.WriteString("\n")
	}
	for _, line := range s.dataLines {
		rawText.WriteString("data: ")
		rawText.WriteString(line)
		rawText.WriteString("\n")
	}
	rawText.WriteString("\n")
	event := ParseSSEEventData(dataText, eventName, rawText.String(), s.dataBytes)
	s.inspectParsedEvent(event, s.dataBytes, eventName)
	s.resetEvent()
}

func (s *StreamInspector) inspectParsedEvent(event StreamEvent, dataBytes int, fallbackEventName string) {
	eventType := orString(event.EventType, event.EventName, fallbackEventName, "message")
	summary := s.classifyEvent(eventType, orString(event.EventName, fallbackEventName), event.Data, event.DataParseError, dataBytes)
	s.pendingEventSummaries = append(s.pendingEventSummaries, summary)
}

func (s *StreamInspector) classifyEvent(eventType, eventName string, data map[string]any, parseError bool, dataBytes int) StreamEventSummary {
	s.inspection.EventCount++
	s.inspection.LastEventType = eventType
	s.inspection.EventTypeCounts[eventType]++
	s.inspection.RecentEventTypes = append(s.inspection.RecentEventTypes, eventType)
	if len(s.inspection.RecentEventTypes) > streamInspectorRecentEventTypes {
		s.inspection.RecentEventTypes = s.inspection.RecentEventTypes[1:]
	}

	var errorObject map[string]any
	if data != nil {
		errorObject = ExtractStreamEventError(data, eventType, eventName)
	}
	failed := errorObject != nil
	outputText := ""
	output := false
	if data != nil {
		outputText = outputTextFromStreamEvent(data)
		output = outputText != ""
	}
	finishReason := ""
	if data != nil {
		finishReason = firstGeminiFinishReason(data, eventType)
	}
	terminal := failed || finishReason != "" || isGeminiTerminalEventType(eventType)
	usage := EmptyUsage()
	if data != nil {
		usage = ExtractUsage(data)
	}
	responseResourceID := ""
	if data != nil {
		responseResourceID = interactionResourceID(data, eventType)
	}

	if failed {
		s.inspection.FailedReceived = true
		s.inspection.ErrorCode = orString(stringField(errorObject["status"]), stringField(errorObject["code"]))
		s.inspection.ErrorMessage = orString(stringField(errorObject["message"]), "Gemini 流式响应失败")
	}
	if terminal {
		s.inspection.TerminalReceived = true
	}
	if output {
		s.inspection.OutputReceived = true
		s.inspection.OutputEventCount++
		s.inspection.EstimatedOutputTokens += EstimateTokenCountFromText(outputText)
		s.inspection.HasEstimatedOutput = true
	}
	if HasAnyUsageValue(usage) {
		s.inspection.Usage = MergeUsage(s.inspection.Usage, usage)
	}
	if s.inspection.ResponseResourceID == "" && responseResourceID != "" {
		s.inspection.ResponseResourceID = responseResourceID
	}

	return StreamEventSummary{
		Type:         eventType,
		DataBytes:    dataBytes,
		Terminal:     terminal,
		CanEndStream: terminal && !failed,
		Failed:       failed,
		Output:       output,
		Usage:        HasAnyUsageValue(usage),
		ParseError:   parseError,
	}
}

func (s *StreamInspector) resetEvent() {
	s.eventName = ""
	s.dataLines = nil
	s.dataBytes = 0
}

// interactionResourceID 对齐 interactionResourceId：仅 interaction.* 事件提取。
func interactionResourceID(data map[string]any, eventType string) string {
	if !strings.HasPrefix(eventType, "interaction.") {
		return ""
	}
	interaction, _ := data["interaction"].(map[string]any)
	return orString(stringField(interaction["id"]), stringField(data["interaction_id"]))
}

// outputTextFromStreamEvent 对齐 outputTextFromGeminiStreamEvent：
// 拼接所有 candidates[].content.parts[].text，缺省回退 delta.text。
func outputTextFromStreamEvent(data map[string]any) string {
	var textParts []string
	for _, part := range candidateParts(data) {
		partObject, _ := part.(map[string]any)
		if text := stringField(partObject["text"]); text != "" {
			textParts = append(textParts, text)
		}
	}
	if len(textParts) > 0 {
		return strings.Join(textParts, "")
	}
	delta, _ := data["delta"].(map[string]any)
	return orString(stringField(delta["text"]))
}

// firstGeminiFinishReason 对齐 firstGeminiFinishReason。
func firstGeminiFinishReason(data map[string]any, eventType string) string {
	if eventType == "interaction.completed" {
		interaction, _ := data["interaction"].(map[string]any)
		return orString(stringField(interaction["status"]), "completed")
	}
	if eventType == "interaction.failed" {
		interaction, _ := data["interaction"].(map[string]any)
		return orString(stringField(interaction["status"]), "failed")
	}
	candidates, _ := data["candidates"].([]any)
	for _, candidate := range candidates {
		row, _ := candidate.(map[string]any)
		if reason := stringField(row["finishReason"]); reason != "" {
			return reason
		}
	}
	return ""
}

func isGeminiTerminalEventType(eventType string) bool {
	return eventType == "finish" || eventType == "done" || eventType == "[DONE]"
}

func candidateParts(data map[string]any) []any {
	candidates, _ := data["candidates"].([]any)
	parts := []any{}
	for _, candidate := range candidates {
		row, _ := candidate.(map[string]any)
		content, _ := row["content"].(map[string]any)
		candidateParts, _ := content["parts"].([]any)
		parts = append(parts, candidateParts...)
	}
	return parts
}

// InspectStreamText 对齐 inspectGeminiStreamText。
func InspectStreamText(text string) StreamInspection {
	inspector := NewStreamInspector()
	inspector.PushText(text)
	return inspector.Finish()
}

// RequestFacts 提供流式 usage 兜底所需的请求体事实。
type RequestFacts struct {
	JSONBody   any
	JSONParsed bool
	RawBody    []byte
}

// EstimateRequestInputTokens 对齐 estimateGeminiRequestInputTokens。
func EstimateRequestInputTokens(facts RequestFacts) (int, bool) {
	if facts.JSONBody != nil {
		encoded, err := json.Marshal(facts.JSONBody)
		if err == nil {
			if tokenCount := EstimateTokenCountFromText(string(encoded)); tokenCount > 0 {
				return tokenCount, true
			}
		}
	}
	if len(facts.RawBody) == 0 {
		return 0, false
	}
	if facts.JSONParsed {
		return 0, false
	}
	return maxInt(1, ceilDiv(len(facts.RawBody), 4)), true
}

// ApplyStreamUsageFallback 对齐 applyGeminiStreamUsageFallback。
func ApplyStreamUsageFallback(facts RequestFacts, usage ParsedUsage, input StreamUsageFallbackInput) StreamUsageFallbackResult {
	if !input.OutputReceived || (positiveTokenCount(usage.InputTokens) && positiveTokenCount(usage.OutputTokens)) {
		return StreamUsageFallbackResult{Usage: usage}
	}
	nextUsage := usage
	estimated := false
	result := StreamUsageFallbackResult{Usage: nextUsage}

	if !positiveTokenCount(nextUsage.InputTokens) {
		if inputTokens, ok := EstimateRequestInputTokens(facts); ok {
			nextUsage.InputTokens = &inputTokens
			result.EstimatedInputTokens = inputTokens
			result.HasEstimatedInput = true
			estimated = true
		}
	}
	if !positiveTokenCount(nextUsage.OutputTokens) {
		outputTokens := 0
		if input.HasEstimatedOutput && input.EstimatedOutputTokens > 0 {
			outputTokens = input.EstimatedOutputTokens
		}
		if outputTokens < 1 {
			outputTokens = 1
		}
		nextUsage.OutputTokens = &outputTokens
		result.EstimatedOutputTokens = outputTokens
		result.HasEstimatedOutput = true
		estimated = true
	}

	result.Usage = nextUsage
	result.Estimated = estimated
	return result
}

func positiveTokenCount(value *int) bool {
	return value != nil && *value > 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
