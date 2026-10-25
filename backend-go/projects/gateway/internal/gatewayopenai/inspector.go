package gatewayopenai

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// Inspector limits (mirrors the Node stream inspector constants).
const (
	streamInspectorMaxLineBytes     = 256 * 1024
	streamInspectorMaxEventBytes    = 512 * 1024
	streamInspectorUsageTailBytes   = 256 * 1024
	streamInspectorRecentEventLimit = 20
)

// Oversize/skip reason strings mirror the Node observability texts.
const (
	reasonLineOverLimit = "SSE 单行超过网关解析上限"
)

// StreamEventSummary mirrors OpenAIStreamEventSummary.
type StreamEventSummary = gatewayproto.StreamEventSummary

// StreamInspector mirrors OpenAIStreamInspector: an incremental SSE parser +
// classifier that produces gatewayproto.StreamInspection snapshots.
type StreamInspector struct {
	inspection gatewayproto.StreamInspection

	eventName string
	dataLines []string
	dataBytes int
	// pendingLine 是当前未完结行的字节缓冲（容量跨 chunk 复用）；容量上限
	// streamInspectorMaxLineBytes+1，越过即 pendingLineExceeded，字节只进
	// pendingLineBytes 计数与 pendingLineTail 滚动尾窗。
	pendingLine           []byte
	pendingLineBytes      int
	pendingLineExceeded   bool
	pendingLineTail       string
	oversizedEvent        bool
	oversizedEventType    string
	oversizedEventImage   bool
	oversizedEventUsage   gatewayproto.ParsedUsage
	pendingEventSummaries []gatewayproto.StreamEventSummary
}

// NewStreamInspector builds an inspector with empty state.
func NewStreamInspector() *StreamInspector {
	return &StreamInspector{
		inspection: gatewayproto.StreamInspection{
			EventTypeCounts: map[string]int{},
			Usage:           gatewayproto.EmptyUsage(),
		},
		oversizedEventUsage: gatewayproto.EmptyUsage(),
	}
}

// PushChunk mirrors pushChunk for byte chunks：字节入参直达行扫描，避免
// 整块 string 拷贝（每 chunk 一次的分配热源）。
func (i *StreamInspector) PushChunk(chunk []byte) {
	i.pushBytes(chunk)
}

// PushText mirrors pushText: split into lines, keeping the tail in the
// pending line buffer.
func (i *StreamInspector) PushText(text string) {
	i.pushBytes([]byte(text))
}

func (i *StreamInspector) pushBytes(chunk []byte) {
	if i.inspection.Skipped {
		return
	}
	offset := 0
	for offset < len(chunk) {
		newlineIndex := bytes.IndexByte(chunk[offset:], '\n')
		var segmentEnd int
		if newlineIndex < 0 {
			segmentEnd = len(chunk)
		} else {
			segmentEnd = offset + newlineIndex
		}
		i.appendPendingLineSegment(chunk[offset:segmentEnd])
		if newlineIndex < 0 {
			break
		}
		i.flushPendingLine()
		offset = segmentEnd + 1
	}
}

// PushParsedEvent mirrors pushParsedEvent: inspect an externally parsed
// event directly (dataBytes <= 0 derives from the data text).
func (i *StreamInspector) PushParsedEvent(event ParsedStreamEvent, dataBytes int) {
	if i.inspection.Skipped {
		return
	}
	if dataBytes <= 0 {
		dataBytes = len(event.DataText)
	}
	i.inspectParsedEvent(event, dataBytes)
}

// Finish mirrors finish: flush pending state at EOF.
func (i *StreamInspector) Finish() gatewayproto.StreamInspection {
	if i.inspection.Skipped {
		return i.Snapshot()
	}
	if len(i.pendingLine) > 0 || i.pendingLineExceeded {
		i.flushPendingLine()
	}
	if i.inspection.Skipped {
		return i.Snapshot()
	}
	i.flushEvent()
	return i.Snapshot()
}

// Snapshot mirrors snapshot.
func (i *StreamInspector) Snapshot() gatewayproto.StreamInspection {
	snapshot := i.inspection
	snapshot.EventTypeCounts = copyCounts(i.inspection.EventTypeCounts)
	snapshot.RecentEventTypes = append([]string(nil), i.inspection.RecentEventTypes...)
	snapshot.PendingEvent = gatewayproto.HasPendingSseProtocolEventBytes(gatewayproto.SsePendingEventState{
		Skipped:        i.inspection.Skipped,
		OversizedEvent: i.oversizedEvent,
		EventName:      i.eventName,
		DataLineCount:  len(i.dataLines),
		DataBytes:      i.dataBytes,
	}, i.pendingLine)
	snapshot.Usage = *usageCopy(&i.inspection.Usage)
	return snapshot
}

// DrainEventSummaries mirrors drainEventSummaries.
func (i *StreamInspector) DrainEventSummaries() []gatewayproto.StreamEventSummary {
	summary := i.pendingEventSummaries
	i.pendingEventSummaries = nil
	return summary
}

// DrainEventSummariesCanEndStream mirrors drainEventSummariesCanEndStream.
func (i *StreamInspector) DrainEventSummariesCanEndStream() bool {
	summary := i.pendingEventSummaries
	i.pendingEventSummaries = nil
	for _, event := range summary {
		if event.CanEndStream {
			return true
		}
	}
	return false
}

func (i *StreamInspector) processLine(line string) {
	if line == "" {
		i.flushEvent()
		return
	}
	if strings.HasPrefix(line, "event:") {
		i.eventName = strings.TrimSpace(line[len("event:"):])
		return
	}
	if !strings.HasPrefix(line, "data:") {
		return
	}
	dataLine := trimStart(line[len("data:"):])
	i.dataBytes += len(dataLine)
	if i.oversizedEvent {
		i.rememberOversizedEventType(dataLine)
		i.rememberOversizedEventUsage(dataLine)
		return
	}
	i.rememberOversizedEventType(dataLine)
	if i.dataBytes > streamInspectorMaxEventBytes {
		i.markOversizedEvent(dataLine)
		return
	}
	i.dataLines = append(i.dataLines, dataLine)
}

func (i *StreamInspector) flushEvent() {
	if i.oversizedEvent {
		i.flushOversizedEvent()
		return
	}
	if len(i.dataLines) == 0 {
		i.eventName = ""
		i.dataBytes = 0
		return
	}
	currentEventName := i.eventName
	currentDataBytes := i.dataBytes
	data := strings.TrimSpace(strings.Join(i.dataLines, "\n"))
	i.eventName = ""
	i.dataLines = nil
	i.dataBytes = 0
	if data == "" {
		return
	}
	event := ParseStreamEventData(data, currentEventName, "", currentDataBytes)
	i.inspectParsedEvent(event, currentDataBytes)
}

func (i *StreamInspector) inspectParsedEvent(event ParsedStreamEvent, dataBytes int) {
	if event.DataParseError {
		// 解析失败事件计数（流式提交边界受限重放与异常终局处置设计 §3.2.0）：
		// 管道按快照增量触发首个 WARN，拦截面按同一分类做字节级隔离；oversized
		// 事件走 flushOversizedEvent 自有 summary，不进本计数。
		i.inspection.ParseErrorEventCount++
		i.recordEventSummary(gatewayproto.StreamEventSummary{
			Type:       orDefault(event.EventName, "message"),
			DataBytes:  dataBytes,
			ParseError: true,
		})
		return
	}

	classification := ClassifyStreamEvent(event, i.inspection.EstimatedOutputTokens)
	if IsProtocolInvalidChunkFrame(event.Data) {
		// L2 协议形状异常计数（流式提交边界受限重放与异常终局处置设计
		// §3.2.0，2026-10-26 增量）：与 ParseErrorEventCount 并列的观测计数；
		// 字节隔离由 gatewayresponse 隔离器按同一谓词处置，不参与终局判定。
		i.inspection.InvalidProtocolEventCount++
	}
	if classification.VisibleOutput {
		i.inspection.OutputReceived = true
		i.inspection.OutputEventCount++
	}
	if classification.ImageOutput {
		i.inspection.ImageOutputReceived = true
	}
	if classification.EstimatedOutputTokens > 0 {
		i.inspection.EstimatedOutputTokens += classification.EstimatedOutputTokens
	}
	if classification.Terminal {
		i.inspection.TerminalReceived = true
	}
	if classification.Failed {
		i.inspection.FailedReceived = true
		if classification.ErrorCode != "" {
			i.inspection.ErrorCode = classification.ErrorCode
		}
		if classification.ErrorMessage != "" {
			i.inspection.ErrorMessage = classification.ErrorMessage
		}
	}
	if classification.UsageFound {
		i.inspection.Usage = gatewayproto.MergeUsage(i.inspection.Usage, classification.Usage)
	}
	i.recordEventSummary(gatewayproto.StreamEventSummary{
		Type:         orDefault(orDefault(classification.EventType, event.EventName), "message"),
		DataBytes:    dataBytes,
		Terminal:     classification.Terminal,
		CanEndStream: classification.Terminal,
		Failed:       classification.Failed,
		Output:       classification.VisibleOutput,
		Usage:        classification.UsageFound,
	})
}

func (i *StreamInspector) skipParsing(reason string) {
	i.pendingLine = nil
	i.eventName = ""
	i.dataLines = nil
	i.dataBytes = 0
	i.pendingLineBytes = 0
	i.pendingLineTail = ""
	i.pendingLineExceeded = false
	i.oversizedEvent = false
	i.oversizedEventType = ""
	i.oversizedEventImage = false
	i.oversizedEventUsage = gatewayproto.EmptyUsage()
	i.inspection.Skipped = true
	i.inspection.SkipReason = reason
}

func (i *StreamInspector) recordEventSummary(summary gatewayproto.StreamEventSummary) {
	i.inspection.EventCount++
	i.inspection.LastEventType = summary.Type
	i.inspection.EventTypeCounts[summary.Type]++
	i.inspection.RecentEventTypes = append(i.inspection.RecentEventTypes, summary.Type)
	if len(i.inspection.RecentEventTypes) > streamInspectorRecentEventLimit {
		i.inspection.RecentEventTypes = i.inspection.RecentEventTypes[len(i.inspection.RecentEventTypes)-streamInspectorRecentEventLimit:]
	}
	i.pendingEventSummaries = append(i.pendingEventSummaries, summary)
}

func (i *StreamInspector) appendPendingLineSegment(segment []byte) {
	if len(segment) == 0 {
		return
	}
	segmentBytes := len(segment)
	if i.pendingLineExceeded {
		i.pendingLineBytes += segmentBytes
		i.pendingLineTail = appendRollingTextTail(i.pendingLineTail, string(segment), streamInspectorUsageTailBytes)
		return
	}
	if i.pendingLineBytes+segmentBytes > streamInspectorMaxLineBytes {
		prefixChars := streamInspectorMaxLineBytes - i.pendingLineBytes + 1
		if prefixChars < 0 {
			prefixChars = 0
		}
		if prefixChars > segmentBytes {
			prefixChars = segmentBytes
		}
		i.pendingLineTail = appendRollingTextTail("", string(i.pendingLine)+string(segment), streamInspectorUsageTailBytes)
		i.pendingLine = append(i.pendingLine, segment[:prefixChars]...)
		i.pendingLineBytes += segmentBytes
		i.pendingLineExceeded = true
		return
	}
	i.pendingLine = append(i.pendingLine, segment...)
	i.pendingLineBytes += segmentBytes
}

func (i *StreamInspector) flushPendingLine() {
	line := string(i.pendingLine)
	if strings.HasSuffix(line, "\r") {
		line = line[:len(line)-1]
	}
	lineBytes := i.pendingLineBytes
	lineTail := i.pendingLineTail
	if strings.HasSuffix(lineTail, "\r") {
		lineTail = lineTail[:len(lineTail)-1]
	}
	exceeded := i.pendingLineExceeded
	i.pendingLine = i.pendingLine[:0]
	i.pendingLineBytes = 0
	i.pendingLineExceeded = false
	i.pendingLineTail = ""
	if exceeded {
		i.processOversizedLine(line, lineBytes, lineTail)
		return
	}
	i.processLine(line)
}

func (i *StreamInspector) processOversizedLine(linePrefix string, lineBytes int, lineTail string) {
	if strings.HasPrefix(linePrefix, "event:") {
		i.eventName = strings.TrimSpace(linePrefix[len("event:"):])
		return
	}
	if !strings.HasPrefix(linePrefix, "data:") {
		i.skipParsing(reasonLineOverLimit)
		return
	}
	dataPrefix := trimStart(linePrefix[len("data:"):])
	i.dataBytes += maxInt(lineBytes, streamInspectorMaxLineBytes+1)
	i.markOversizedEvent(dataPrefix)
	i.rememberOversizedEventImageOutput(lineTail)
	i.rememberOversizedEventUsage(dataPrefix)
	i.rememberOversizedEventUsage(lineTail)
	if i.oversizedEventType == "" && !i.oversizedEventImage && i.eventName == "" {
		i.skipParsing(reasonLineOverLimit)
	}
}

func (i *StreamInspector) markOversizedEvent(dataPrefix string) {
	i.oversizedEvent = true
	i.dataLines = nil
	i.rememberOversizedEventType(dataPrefix)
}

func (i *StreamInspector) rememberOversizedEventType(dataPrefix string) {
	if i.oversizedEventType == "" {
		i.oversizedEventType = extractStreamEventTypeFromJSONPrefix(dataPrefix)
	}
	i.rememberOversizedEventImageOutput(dataPrefix)
}

func (i *StreamInspector) rememberOversizedEventImageOutput(textFragment string) {
	if !i.oversizedEventImage && hasImageStreamPayloadHint(textFragment) {
		i.oversizedEventImage = true
	}
}

func (i *StreamInspector) rememberOversizedEventUsage(textFragment string) {
	usage := ParseUsageFromJSONTextFragment(textFragment)
	if gatewayproto.HasAnyUsageValue(usage) {
		i.oversizedEventUsage = gatewayproto.MergeUsage(i.oversizedEventUsage, usage)
	}
}

func (i *StreamInspector) flushOversizedEvent() {
	eventType := orDefault(orDefault(i.oversizedEventType, i.eventName), "message")
	classification := classifyOversizedStreamEvent(eventType, i.oversizedEventImage)
	if classification.visibleOutput {
		i.inspection.OutputReceived = true
		i.inspection.OutputEventCount++
	}
	if classification.imageOutput {
		i.inspection.ImageOutputReceived = true
	}
	if classification.terminal {
		i.inspection.TerminalReceived = true
	}
	if classification.failed {
		i.inspection.FailedReceived = true
	}
	usageFound := gatewayproto.HasAnyUsageValue(i.oversizedEventUsage)
	if usageFound {
		i.inspection.Usage = gatewayproto.MergeUsage(i.inspection.Usage, i.oversizedEventUsage)
	}
	i.recordEventSummary(gatewayproto.StreamEventSummary{
		Type:         eventType,
		DataBytes:    i.dataBytes,
		Terminal:     classification.terminal,
		CanEndStream: classification.terminal,
		Failed:       classification.failed,
		Output:       classification.visibleOutput,
		Usage:        usageFound,
		ParseError:   true,
	})
	i.eventName = ""
	i.dataLines = nil
	i.dataBytes = 0
	i.resetOversizedEventState()
}

func (i *StreamInspector) resetOversizedEventState() {
	i.oversizedEvent = false
	i.oversizedEventType = ""
	i.oversizedEventImage = false
	i.oversizedEventUsage = gatewayproto.EmptyUsage()
}

// oversizedClassification mirrors classifyOversizedOpenAIStreamEvent.
type oversizedClassification struct {
	terminal      bool
	failed        bool
	visibleOutput bool
	imageOutput   bool
}

func classifyOversizedStreamEvent(eventType string, imageOutputHint bool) oversizedClassification {
	terminal := eventType == "[DONE]" ||
		eventType == "response.completed" ||
		eventType == "response.done" ||
		eventType == "response.incomplete" ||
		eventType == "response.failed" ||
		eventType == "image_generation.completed" ||
		eventType == "image_generation.failed"
	failed := eventType == "response.failed" ||
		eventType == "image_generation.failed" ||
		eventType == "error"
	imageOutput := imageOutputHint || IsImageStreamEventType(eventType)
	visibleOutput := strings.HasSuffix(eventType, ".delta") ||
		eventType == "response.output_item.added" ||
		eventType == "response.output_item.done" ||
		eventType == "response.completed" ||
		eventType == "response.done" ||
		eventType == "response.incomplete" ||
		imageOutput
	return oversizedClassification{terminal: terminal, failed: failed, visibleOutput: visibleOutput, imageOutput: imageOutput}
}

var streamEventTypeFromPrefixPattern = regexp.MustCompile(`"type"\s*:\s*"([^"]{1,160})"`)

func extractStreamEventTypeFromJSONPrefix(dataPrefix string) string {
	searchable := dataPrefix
	if len(searchable) > 4096 {
		searchable = searchable[:4096]
	}
	match := streamEventTypeFromPrefixPattern.FindStringSubmatch(searchable)
	if match == nil {
		return ""
	}
	return match[1]
}

var imageHintTypePattern = regexp.MustCompile(`"type"\s*:\s*"image_generation_call"`)
var imageHintB64Pattern = regexp.MustCompile(`"(partial_image_b64|b64_json)"\s*:`)

func hasImageStreamPayloadHint(textFragment string) bool {
	searchable := textFragment
	if len(searchable) > 65536 {
		searchable = searchable[:65536]
	}
	return imageHintTypePattern.MatchString(searchable) || imageHintB64Pattern.MatchString(searchable)
}

func appendRollingTextTail(current, next string, limitChars int) string {
	if limitChars <= 0 || next == "" {
		return current
	}
	combined := next
	if current != "" {
		combined = current + next
	}
	if len(combined) > limitChars {
		return combined[len(combined)-limitChars:]
	}
	return combined
}

func copyCounts(source map[string]int) map[string]int {
	out := make(map[string]int, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func usageCopy(source *gatewayproto.ParsedUsage) *gatewayproto.ParsedUsage {
	copied := *source
	return &copied
}

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// InspectStreamText mirrors inspectOpenAIStreamText: one-shot text
// inspection.
func InspectStreamText(text string) gatewayproto.StreamInspection {
	inspector := NewStreamInspector()
	inspector.PushText(text)
	return inspector.Finish()
}

// ParseUsageFromSseText mirrors parseOpenAIUsageFromSseText.
func ParseUsageFromSseText(text string) gatewayproto.ParsedUsage {
	return InspectStreamText(text).Usage
}

// EstimateRequestInputTokens mirrors estimateOpenAIRequestInputTokens at the
// driver boundary: prefer the parsed JSON body estimate, then fall back to
// the raw body byte estimate.
func EstimateRequestInputTokens(parsedBody any, rawBody []byte) (int, bool) {
	bodyTokens := EstimateTokensFromRequestValue(parsedBody)
	if bodyTokens > 0 {
		return bodyTokens, true
	}
	if len(rawBody) == 0 {
		return 0, false
	}
	if parsedBody != nil {
		return 0, false
	}
	return EstimateTokenCountFromByteLength(len(rawBody))
}

// StreamUsageFallbackInput mirrors GatewayStreamUsageFallbackInput.
type StreamUsageFallbackInput struct {
	Completed             bool
	CompletedSet          bool
	OutputReceived        bool
	EstimatedOutputTokens int
}

// StreamUsageFallbackResult mirrors OpenAIStreamUsageFallbackResult.
type StreamUsageFallbackResult struct {
	Usage                 gatewayproto.ParsedUsage
	Estimated             bool
	EstimatedInputTokens  *int
	EstimatedOutputTokens *int
}

// ApplyStreamUsageFallback mirrors applyOpenAIStreamUsageFallback: estimate
// missing usage once output was received but the upstream reported nothing.
func ApplyStreamUsageFallback(parsedBody any, rawBody []byte, usage gatewayproto.ParsedUsage, input StreamUsageFallbackInput) StreamUsageFallbackResult {
	completed := input.CompletedSet && input.Completed
	if (!input.OutputReceived && !completed) ||
		(positiveTokenCount(usage.InputTokens) && positiveTokenCount(usage.OutputTokens)) {
		return StreamUsageFallbackResult{Usage: usage}
	}

	nextUsage := usage
	result := StreamUsageFallbackResult{Usage: nextUsage}
	if !positiveTokenCount(nextUsage.InputTokens) {
		if inputTokens, ok := EstimateRequestInputTokens(parsedBody, rawBody); ok {
			nextUsage.InputTokens = gatewayproto.IntToken(inputTokens)
			result.EstimatedInputTokens = gatewayproto.IntToken(inputTokens)
			result.Estimated = true
		}
	}
	if input.OutputReceived && !positiveTokenCount(nextUsage.OutputTokens) {
		outputTokens := maxInt(1, input.EstimatedOutputTokens)
		nextUsage.OutputTokens = gatewayproto.IntToken(outputTokens)
		result.EstimatedOutputTokens = gatewayproto.IntToken(outputTokens)
		result.Estimated = true
	}
	result.Usage = nextUsage
	return result
}

func positiveTokenCount(value *int) bool {
	return value != nil && *value > 0
}
