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
	streamInspectorHintWindowBytes  = 4096
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
	oversizedEventError   bool
	oversizedEventOutput  bool
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
	firstDataLine := i.dataBytes == 0
	i.dataBytes += len(dataLine)
	if i.oversizedEvent {
		i.rememberOversizedEventType(dataLine)
		i.rememberOversizedEventUsage(dataLine)
		i.rememberOversizedEventOutputHint(dataLine)
		return
	}
	i.rememberOversizedEventType(dataLine)
	if firstDataLine {
		i.rememberOversizedEventErrorHint(dataLine)
	}
	i.rememberOversizedEventOutputHint(dataLine)
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
	// oversized 临时状态（type/image/usage/error/output）只属于当前 SSE event：
	// 普通事件收尾必须清理，否则后续超大终止事件会复用上一条事件的类型（BUG-0353）。
	if len(i.dataLines) == 0 {
		i.eventName = ""
		i.dataBytes = 0
		i.resetOversizedEventState()
		return
	}
	currentEventName := i.eventName
	currentDataBytes := i.dataBytes
	data := strings.TrimSpace(strings.Join(i.dataLines, "\n"))
	i.eventName = ""
	i.dataLines = nil
	i.dataBytes = 0
	if data == "" {
		i.resetOversizedEventState()
		return
	}
	event := ParseStreamEventData(data, currentEventName, "", currentDataBytes)
	i.inspectParsedEvent(event, currentDataBytes)
	i.resetOversizedEventState()
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
	i.resetOversizedEventState()
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
	firstDataLine := i.dataBytes == 0
	i.dataBytes += maxInt(lineBytes, streamInspectorMaxLineBytes+1)
	i.markOversizedEvent(dataPrefix)
	i.rememberOversizedEventImageOutput(lineTail)
	i.rememberOversizedEventUsage(dataPrefix)
	i.rememberOversizedEventUsage(lineTail)
	if firstDataLine {
		i.rememberOversizedEventErrorHint(dataPrefix)
	}
	i.rememberOversizedEventOutputHint(dataPrefix)
	i.rememberOversizedEventOutputHintTail(lineTail)
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

// rememberOversizedEventErrorHint 只对事件首条 data 行做顶层 error 对象探测：
// 完整 JSON 解析已放弃，但 `{"error":{...}}` 是失败信号的唯一可靠有界提示。
func (i *StreamInspector) rememberOversizedEventErrorHint(dataPrefix string) {
	if i.oversizedEventError {
		return
	}
	searchable := dataPrefix
	if len(searchable) > streamInspectorHintWindowBytes {
		searchable = searchable[:streamInspectorHintWindowBytes]
	}
	if streamTopLevelErrorHintPattern.MatchString(searchable) {
		i.oversizedEventError = true
	}
}

// rememberOversizedEventOutputHint 在 data 前缀前 4096 字节内探测输出类
// JSON 键；只认精确键名（"output_tokens" 不匹配 "output"）。
func (i *StreamInspector) rememberOversizedEventOutputHint(dataPrefix string) {
	if i.oversizedEventOutput || dataPrefix == "" {
		return
	}
	if hasStreamEventOutputKeyHint(hintWindowPrefix(dataPrefix, streamInspectorHintWindowBytes)) {
		i.oversizedEventOutput = true
	}
}

// rememberOversizedEventOutputHintTail 在超大行的滚动尾窗（完整尾窗，
// 上限 streamInspectorUsageTailBytes）内探测输出类 JSON 键。
func (i *StreamInspector) rememberOversizedEventOutputHintTail(lineTail string) {
	if i.oversizedEventOutput || lineTail == "" {
		return
	}
	if hasStreamEventOutputKeyHint(lineTail) {
		i.oversizedEventOutput = true
	}
}

func (i *StreamInspector) flushOversizedEvent() {
	eventType := orDefault(orDefault(i.oversizedEventType, i.eventName), "message")
	classification := classifyOversizedStreamEvent(eventType, i.oversizedEventImage)
	failed := classification.failed || i.oversizedEventError
	terminal := classification.terminal || failed
	visibleOutput := classification.visibleOutput || i.oversizedEventOutput
	if visibleOutput {
		i.inspection.OutputReceived = true
		i.inspection.OutputEventCount++
	}
	if classification.imageOutput {
		i.inspection.ImageOutputReceived = true
	}
	if terminal {
		i.inspection.TerminalReceived = true
	}
	if failed {
		i.inspection.FailedReceived = true
	}
	usageFound := gatewayproto.HasAnyUsageValue(i.oversizedEventUsage)
	if usageFound {
		i.inspection.Usage = gatewayproto.MergeUsage(i.inspection.Usage, i.oversizedEventUsage)
	}
	i.recordEventSummary(gatewayproto.StreamEventSummary{
		Type:         eventType,
		DataBytes:    i.dataBytes,
		Terminal:     terminal,
		CanEndStream: terminal,
		Failed:       failed,
		Output:       visibleOutput,
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
	i.oversizedEventError = false
	i.oversizedEventOutput = false
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

// 顶层 error 对象提示与输出键提示都只在有界窗口内匹配，避免为超大事件
// 恢复完整 JSON 解析带来无界内存/CPU 开销。
var streamTopLevelErrorHintPattern = regexp.MustCompile(`^\s*\{\s*"error"\s*:\s*\{`)
var streamEventOutputKeyHintPattern = regexp.MustCompile(`"(?:choices|delta|text|content|output)"\s*:`)

func hasStreamEventOutputKeyHint(window string) bool {
	return streamEventOutputKeyHintPattern.MatchString(window)
}

func hintWindowPrefix(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit]
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
