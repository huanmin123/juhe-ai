package gatewaygemini

import (
	"bytes"
	"encoding/json"
	"regexp"
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
	streamInspectorMaxLineBytes     = 256 * 1024
	streamInspectorMaxEventBytes    = 256 * 1024
	streamInspectorOversizedPrefix  = 4 * 1024
	streamInspectorRecentEventTypes = 8
	geminiStreamSkipReasonTooLarge  = "gemini_stream_event_too_large"
)

// StreamInspector 对齐 GeminiStreamInspector。
type StreamInspector struct {
	inspection StreamInspection
	eventName  string
	dataLines  []string
	dataBytes  int
	// pendingLine 是当前未完结行的字节缓冲（容量跨 chunk 复用），替代
	// strings.Builder：每个 chunk 快照的 PendingEvent 判定可走字节前缀比较，
	// 不再为读一行而复制。
	pendingLine []byte
	// oversized 状态只属于当前 SSE event：data:/event: 行或事件超过解析上限
	// 时丢弃当前事件的完整解析，但继续扫描后续事件（BUG-0354）。前缀只保留
	// 有界窗口，用于轻量识别 finishReason/candidates/顶层 error。
	oversizedEvent        bool
	oversizedEventType    string
	oversizedEventFailed  bool
	oversizedEventOutput  bool
	oversizedEventPrefix  []byte
	oversizedLineSkip     bool
	pendingEventSummaries []StreamEventSummary
}

// NewStreamInspector 创建流检查器。
func NewStreamInspector() *StreamInspector {
	return &StreamInspector{inspection: StreamInspection{EventTypeCounts: map[string]int{}, Usage: EmptyUsage()}}
}

// PushChunk 对齐 pushChunk：字节入参直达行扫描，避免整块 string 拷贝。
func (s *StreamInspector) PushChunk(chunk []byte) StreamInspection {
	return s.pushBytes(chunk)
}

// PushText 对齐 pushText。
func (s *StreamInspector) PushText(text string) StreamInspection {
	return s.pushBytes([]byte(text))
}

func (s *StreamInspector) pushBytes(chunk []byte) StreamInspection {
	if s.inspection.Skipped {
		return s.Snapshot()
	}
	offset := 0
	for offset < len(chunk) {
		// 超大行剩余字节只丢弃到换行，不再缓冲；后续行继续扫描。
		if s.oversizedLineSkip {
			newlineIndex := bytes.IndexByte(chunk[offset:], '\n')
			if newlineIndex < 0 {
				return s.Snapshot()
			}
			offset += newlineIndex + 1
			s.oversizedLineSkip = false
			continue
		}
		newlineIndex := bytes.IndexByte(chunk[offset:], '\n')
		var segmentEnd int
		if newlineIndex < 0 {
			segmentEnd = len(chunk)
		} else {
			segmentEnd = offset + newlineIndex
		}
		// 对齐原 TrimSuffix(segment, "\r")：每个分段（含未完结尾段）剥一个行尾 \r。
		segment := chunk[offset:segmentEnd]
		if len(segment) > 0 && segment[len(segment)-1] == '\r' {
			segment = segment[:len(segment)-1]
		}
		// 行级上限（对齐 openai 侧 streamInspectorMaxLineBytes）：未完结行缓冲
		// 不得随无换行输入无上限累积。data:/event: 行只丢弃当前事件并进入
		// oversized 状态，后续事件继续解析；其他前缀仍按整流 Skipped 处理。
		if len(s.pendingLine)+len(segment) > streamInspectorMaxLineBytes {
			linePrefix := boundedStreamLinePrefix(s.pendingLine, segment, streamInspectorOversizedPrefix)
			if isStreamDataOrEventLinePrefix(linePrefix) {
				s.markOversizedLine(linePrefix, len(s.pendingLine)+len(segment))
				s.pendingLine = nil
				if newlineIndex < 0 {
					s.oversizedLineSkip = true
					return s.Snapshot()
				}
				offset = segmentEnd + 1
				continue
			}
			s.inspection.Skipped = true
			s.inspection.SkipReason = geminiStreamSkipReasonTooLarge
			s.pendingLine = nil
			s.resetEvent()
			return s.Snapshot()
		}
		s.pendingLine = append(s.pendingLine, segment...)
		if newlineIndex < 0 {
			break
		}
		s.processLine(string(s.pendingLine))
		s.pendingLine = s.pendingLine[:0]
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
	if len(s.pendingLine) > 0 {
		s.processLine(string(s.pendingLine))
		s.pendingLine = s.pendingLine[:0]
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
		s.pendingLine,
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
		if s.oversizedEvent {
			s.flushOversizedEvent()
			return
		}
		s.flushEvent()
		return
	}
	if strings.HasPrefix(line, "event:") {
		s.eventName = strings.TrimSpace(line[len("event:"):])
		if s.oversizedEvent && s.oversizedEventType == "" {
			s.oversizedEventType = s.eventName
		}
		return
	}
	if !strings.HasPrefix(line, "data:") {
		return
	}
	dataLine := trimStart(line[len("data:"):])
	s.dataBytes += len(dataLine)
	if s.oversizedEvent {
		// 当前事件已超限：后续 data 行只计数，不再保存完整内容。
		s.rememberOversizedEventPrefix(dataLine)
		return
	}
	if s.dataBytes > streamInspectorMaxEventBytes {
		s.enterOversizedEvent(dataLine)
		return
	}
	s.dataLines = append(s.dataLines, dataLine)
}

func (s *StreamInspector) flushEvent() {
	if s.inspection.Skipped {
		return
	}
	if s.oversizedEvent {
		s.flushOversizedEvent()
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

// markOversizedLine 处理超过行上限的 data:/event: 行：只保留有界前缀用于
// 轻量分类，当前行剩余字节由 pushBytes 丢弃；非 data/event 前缀不走本路径。
func (s *StreamInspector) markOversizedLine(linePrefix []byte, lineBytes int) {
	if bytes.HasPrefix(linePrefix, []byte("event:")) {
		s.eventName = strings.TrimSpace(string(linePrefix[len("event:"):]))
		s.enterOversizedEvent("")
		return
	}
	dataPrefix := trimStart(string(linePrefix[len("data:"):]))
	s.dataBytes += lineBytes
	s.enterOversizedEvent(dataPrefix)
}

// enterOversizedEvent 把当前事件切换为 oversized 模式：已有 data 行与本次
// 触发片段只进入有界前缀，完整内容不再保存。
func (s *StreamInspector) enterOversizedEvent(extraPrefix string) {
	if !s.oversizedEvent {
		s.oversizedEvent = true
		if s.oversizedEventType == "" {
			s.oversizedEventType = s.eventName
		}
	}
	for _, line := range s.dataLines {
		s.rememberOversizedEventPrefix(line)
		if len(s.oversizedEventPrefix) >= streamInspectorOversizedPrefix {
			break
		}
	}
	s.rememberOversizedEventPrefix(extraPrefix)
	s.dataLines = nil
}

func (s *StreamInspector) rememberOversizedEventPrefix(fragment string) {
	if fragment == "" || len(s.oversizedEventPrefix) >= streamInspectorOversizedPrefix {
		return
	}
	remaining := streamInspectorOversizedPrefix - len(s.oversizedEventPrefix)
	if len(fragment) > remaining {
		fragment = fragment[:remaining]
	}
	s.oversizedEventPrefix = append(s.oversizedEventPrefix, fragment...)
	prefix := string(s.oversizedEventPrefix)
	if s.eventName == "" && s.oversizedEventType == "" {
		s.oversizedEventType = extractOversizedEventType(prefix)
	}
	if !s.oversizedEventFailed && topLevelStreamErrorObjectHint(prefix) {
		s.oversizedEventFailed = true
	}
	if !s.oversizedEventOutput && geminiOversizedOutputHint(prefix) {
		s.oversizedEventOutput = true
	}
}

func (s *StreamInspector) flushOversizedEvent() {
	eventType := s.eventName
	if eventType == "" {
		eventType = s.oversizedEventType
	}
	if eventType == "" {
		eventType = "message"
	}
	prefix := string(s.oversizedEventPrefix)
	failed := s.oversizedEventFailed ||
		eventType == "error" ||
		eventType == "interaction.failed"
	terminal := failed ||
		isGeminiTerminalEventType(eventType) ||
		eventType == "interaction.completed" ||
		geminiOversizedFinishReasonHint(prefix)
	output := s.oversizedEventOutput ||
		eventType == "step.delta" ||
		strings.Contains(prefix, "candidates")

	if failed {
		s.inspection.FailedReceived = true
		if s.inspection.ErrorMessage == "" {
			s.inspection.ErrorMessage = "Gemini 流式响应失败"
		}
	}
	if terminal {
		s.inspection.TerminalReceived = true
	}
	if output {
		s.inspection.OutputReceived = true
		s.inspection.OutputEventCount++
	}
	s.inspection.EventCount++
	s.inspection.LastEventType = eventType
	s.inspection.EventTypeCounts[eventType]++
	s.inspection.RecentEventTypes = append(s.inspection.RecentEventTypes, eventType)
	if len(s.inspection.RecentEventTypes) > streamInspectorRecentEventTypes {
		s.inspection.RecentEventTypes = s.inspection.RecentEventTypes[1:]
	}
	s.pendingEventSummaries = append(s.pendingEventSummaries, StreamEventSummary{
		Type:         eventType,
		DataBytes:    s.dataBytes,
		Terminal:     terminal,
		CanEndStream: terminal && !failed,
		Failed:       failed,
		Output:       output,
		ParseError:   true,
	})
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
	if errorObject == nil && data != nil {
		// 顶层 error 对象形态（无 event 名、无 type 字段）正是 Gemini
		// streamGenerate 的标准流式错误形态；共享 ExtractStreamEventError
		// 不放宽，检查器判定独立补齐（docs/functions/上游流式失败帧拦截设计.md §3.3）。
		if topLevelError, ok := data["error"].(map[string]any); ok {
			errorObject = topLevelError
		}
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
	s.oversizedEvent = false
	s.oversizedEventType = ""
	s.oversizedEventFailed = false
	s.oversizedEventOutput = false
	s.oversizedEventPrefix = nil
	s.oversizedLineSkip = false
}

// boundedStreamLinePrefix 只拼出当前行开头的有界窗口，用于判断行类型；
// 不随超大行剩余字节增长。
func boundedStreamLinePrefix(pendingLine, segment []byte, limit int) []byte {
	prefix := make([]byte, 0, limit)
	if len(pendingLine) >= limit {
		return append(prefix, pendingLine[:limit]...)
	}
	prefix = append(prefix, pendingLine...)
	remaining := limit - len(prefix)
	if remaining > 0 && len(segment) > 0 {
		if len(segment) > remaining {
			segment = segment[:remaining]
		}
		prefix = append(prefix, segment...)
	}
	return prefix
}

func isStreamDataOrEventLinePrefix(prefix []byte) bool {
	return bytes.HasPrefix(prefix, []byte("data:")) || bytes.HasPrefix(prefix, []byte("event:"))
}

// topLevelStreamErrorObjectHint 识别数据前缀中的顶层 {"error":{...}} 形态；
// 只做有界文本判断，不做 JSON 解析。
func topLevelStreamErrorObjectHint(prefix string) bool {
	text := strings.TrimSpace(prefix)
	if !strings.HasPrefix(text, "{") {
		return false
	}
	text = strings.TrimSpace(text[1:])
	const errorKey = `"error"`
	if !strings.HasPrefix(text, errorKey) {
		return false
	}
	text = strings.TrimSpace(text[len(errorKey):])
	if !strings.HasPrefix(text, ":") {
		return false
	}
	text = strings.TrimSpace(text[1:])
	return strings.HasPrefix(text, "{")
}

func geminiOversizedOutputHint(prefix string) bool {
	return strings.Contains(prefix, "candidates")
}

// oversizedEventTypePattern 只认顶层首键 type，避免 functionCall.args 等
// 嵌套 type 被误当作事件类型；匹配范围受 4KB 有界前缀限制。
var oversizedEventTypePattern = regexp.MustCompile(`^\s*\{\s*"type"\s*:\s*"([^"]{1,160})"`)

func extractOversizedEventType(prefix string) string {
	match := oversizedEventTypePattern.FindStringSubmatch(prefix)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

// geminiOversizedFinishReasonPattern 只认 JSON 键值形态，避免正文中裸
// finishReason 字面量触发误终止；值必须是非空字符串（与正常分类器
// stringField 的取值口径一致）。
var geminiOversizedFinishReasonPattern = regexp.MustCompile(`"finishReason"\s*:\s*"[^"]+"`)

func geminiOversizedFinishReasonHint(prefix string) bool {
	return geminiOversizedFinishReasonPattern.MatchString(prefix)
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
