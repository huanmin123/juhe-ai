package gatewayopenai

import (
	"bytes"
	"encoding/json"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// maxBufferedSseEventBytes mirrors maxBufferedSseEventBytes: beyond this the
// parser stops inspecting and passes the stream through untouched.
const maxBufferedSseEventBytes = 256 * 1024

// InspectionDecision mirrors ResponseInspectionDecision at the boundary this
// slice consumes: a policy verdict over one parsed event.
type InspectionDecision struct {
	Action    string // "intercept" | "discard_event" | "dry_run"
	ErrorCode string
	Message   string
	Retryable bool
}

// Decision actions.
const (
	DecisionIntercept    = "intercept"
	DecisionDiscardEvent = "discard_event"
	DecisionDryRun       = "dry_run"
)

// InspectionPolicy inspects one parsed SSE event with its semantic frames
// and may return a decision.
type InspectionPolicy func(event ParsedStreamEvent, frames []gatewayproto.SemanticFrame) *InspectionDecision

// InspectionSseResult mirrors ResponseInspectionSseResult.
type InspectionSseResult struct {
	Chunks        [][]byte
	Intercepted   *InspectionDecision
	PendingEvent  bool
	ParserSkipped bool
}

// ResponseInspectionBufferOptions mirrors OpenAIResponseInspectionBufferOptions.
type ResponseInspectionBufferOptions struct {
	ClientRetryEnabled    bool
	Policies              []InspectionPolicy
	EndpointFamily        gatewayproto.ResponseEndpointFamily
	ExtractSemanticFrames func(ParsedStreamEvent) []gatewayproto.SemanticFrame
	BuildFailureEvent     func(decision InspectionDecision, clientRetryEnabled bool) []byte
}

// ResponseInspectionBuffer mirrors OpenAIResponseInspectionBuffer: buffers
// an SSE stream into event-framed chunks, extracts semantic frames per event
// and gives policies a chance to intercept before bytes reach the client.
// The Codex compaction contract checks belong to a later slice and are not
// engaged here.
type ResponseInspectionBuffer struct {
	pendingBuffer      pendingSseEventBuffer
	clientRetryEnabled bool
	policies           []InspectionPolicy
	// inspectVisibleOutputTextEvents is the conservative Go stand-in for the
	// Node policy-descriptor fast path: with any policy registered every
	// event is inspected (no visible-output pass-through).
	inspectVisibleOutputTextEvents bool
	endpointFamily                 gatewayproto.ResponseEndpointFamily
	extractSemanticFrames          func(ParsedStreamEvent) []gatewayproto.SemanticFrame
	buildFailureEvent              func(InspectionDecision, bool) []byte
	parserSkipped                  bool
	downstreamWritten              bool
	deferredLeadingNoopChunks      [][]byte
}

// NewResponseInspectionBuffer builds the buffer.
func NewResponseInspectionBuffer(options ResponseInspectionBufferOptions) *ResponseInspectionBuffer {
	buffer := &ResponseInspectionBuffer{
		pendingBuffer:                  newPendingSseEventBuffer(),
		clientRetryEnabled:             options.ClientRetryEnabled,
		policies:                       options.Policies,
		inspectVisibleOutputTextEvents: len(options.Policies) > 0,
		endpointFamily:                 options.EndpointFamily,
	}
	if options.ExtractSemanticFrames != nil {
		buffer.extractSemanticFrames = options.ExtractSemanticFrames
	} else {
		buffer.extractSemanticFrames = func(event ParsedStreamEvent) []gatewayproto.SemanticFrame {
			return ExtractSseSemanticFrames(event, openAIEndpointFamilyOrUnknown(options.EndpointFamily))
		}
	}
	if options.BuildFailureEvent != nil {
		buffer.buildFailureEvent = options.BuildFailureEvent
	} else {
		buffer.buildFailureEvent = failureEventForDecision
	}
	return buffer
}

func openAIEndpointFamilyOrUnknown(family gatewayproto.ResponseEndpointFamily) gatewayproto.ResponseEndpointFamily {
	if family == gatewayproto.EndpointFamilyChatCompletions || family == gatewayproto.EndpointFamilyResponses {
		return family
	}
	return gatewayproto.EndpointFamilyUnknown
}

// MarkDownstreamWrite mirrors markDownstreamWrite.
func (b *ResponseInspectionBuffer) MarkDownstreamWrite() {
	if !b.clientRetryEnabled && len(b.policies) == 0 {
		return
	}
	b.downstreamWritten = true
}

// Engaged reports whether the buffer inspects at all.
func (b *ResponseInspectionBuffer) Engaged() bool {
	return b.clientRetryEnabled || len(b.policies) > 0
}

// PushChunk mirrors pushChunk.
func (b *ResponseInspectionBuffer) PushChunk(chunk []byte) InspectionSseResult {
	if !b.Engaged() {
		return InspectionSseResult{Chunks: [][]byte{chunk}}
	}
	if b.parserSkipped {
		return InspectionSseResult{
			Chunks:        append(b.drainDeferredLeadingNoopChunks(), chunk),
			ParserSkipped: true,
		}
	}

	b.pendingBuffer.push(chunk)
	if b.pendingBuffer.length() > maxBufferedSseEventBytes {
		buffered := b.pendingBuffer.drain()
		b.parserSkipped = true
		return InspectionSseResult{
			Chunks:        append(b.drainDeferredLeadingNoopChunks(), buffered),
			ParserSkipped: true,
		}
	}

	var chunks [][]byte
	semanticOutputBuffered := false
	for {
		rawBuffer := b.pendingBuffer.shiftEvent()
		if rawBuffer == nil {
			break
		}
		if b.canPassThroughCommonResponsesTextDeltaBuffer(rawBuffer) {
			semanticOutputBuffered = true
			chunks = append(chunks, b.drainDeferredLeadingNoopChunks()...)
			chunks = append(chunks, rawBuffer)
			continue
		}
		event := ParseSseEventText(string(rawBuffer))
		if !b.downstreamWritten && isDeferrableLeadingChatCompletionNoopEvent(event) {
			b.deferredLeadingNoopChunks = append(b.deferredLeadingNoopChunks, rawBuffer)
			continue
		}
		if b.canPassThroughUninspectableVisibleOutputTextEvent(event) {
			semanticOutputBuffered = true
			chunks = append(chunks, b.drainDeferredLeadingNoopChunks()...)
			chunks = append(chunks, rawBuffer)
			continue
		}
		frames := b.extractSemanticFrames(event)
		decision := b.runPolicies(event, frames)
		if decision != nil {
			if decision.Action == DecisionDiscardEvent {
				continue
			}
			b.clearDeferredLeadingNoopChunks()
			if !b.downstreamWritten && !semanticOutputBuffered {
				chunks = nil
			}
			if failureEvent := b.buildFailureEvent(*decision, b.clientRetryEnabled); failureEvent != nil {
				chunks = append(chunks, failureEvent)
			}
			return InspectionSseResult{
				Chunks:        chunks,
				Intercepted:   decision,
				PendingEvent:  b.pendingBuffer.length() > 0,
				ParserSkipped: b.parserSkipped,
			}
		}
		semanticOutputBuffered = semanticOutputBuffered || framesHaveVisibleOutput(frames)
		chunks = append(chunks, b.drainDeferredLeadingNoopChunks()...)
		chunks = append(chunks, rawBuffer)
	}

	return InspectionSseResult{
		Chunks:        chunks,
		PendingEvent:  b.pendingBuffer.length() > 0,
		ParserSkipped: b.parserSkipped,
	}
}

// FlushPendingOnEOF mirrors flushPendingOnEof.
func (b *ResponseInspectionBuffer) FlushPendingOnEOF() InspectionSseResult {
	if !b.Engaged() {
		return InspectionSseResult{}
	}
	if b.parserSkipped || b.pendingBuffer.length() == 0 {
		var chunks [][]byte
		if b.parserSkipped {
			chunks = b.drainDeferredLeadingNoopChunks()
		}
		return InspectionSseResult{
			Chunks:        chunks,
			PendingEvent:  b.pendingBuffer.length() > 0,
			ParserSkipped: b.parserSkipped,
		}
	}
	rawBuffer := b.pendingBuffer.drainEnsuringBoundary()
	return b.inspectRawEventBuffer(rawBuffer)
}

func (b *ResponseInspectionBuffer) inspectRawEventBuffer(rawBuffer []byte) InspectionSseResult {
	if b.canPassThroughCommonResponsesTextDeltaBuffer(rawBuffer) {
		return InspectionSseResult{
			Chunks:        append(b.drainDeferredLeadingNoopChunks(), rawBuffer),
			PendingEvent:  b.pendingBuffer.length() > 0,
			ParserSkipped: b.parserSkipped,
		}
	}
	event := ParseSseEventText(string(rawBuffer))
	if !b.downstreamWritten && isDeferrableLeadingChatCompletionNoopEvent(event) {
		b.deferredLeadingNoopChunks = append(b.deferredLeadingNoopChunks, rawBuffer)
		b.clearDeferredLeadingNoopChunks()
		return InspectionSseResult{
			PendingEvent:  b.pendingBuffer.length() > 0,
			ParserSkipped: b.parserSkipped,
		}
	}
	if b.canPassThroughUninspectableVisibleOutputTextEvent(event) {
		return InspectionSseResult{
			Chunks:        append(b.drainDeferredLeadingNoopChunks(), rawBuffer),
			PendingEvent:  b.pendingBuffer.length() > 0,
			ParserSkipped: b.parserSkipped,
		}
	}
	frames := b.extractSemanticFrames(event)
	decision := b.runPolicies(event, frames)
	if decision == nil {
		return InspectionSseResult{
			Chunks:        append(b.drainDeferredLeadingNoopChunks(), rawBuffer),
			PendingEvent:  b.pendingBuffer.length() > 0,
			ParserSkipped: b.parserSkipped,
		}
	}
	if decision.Action == DecisionDiscardEvent {
		return InspectionSseResult{
			PendingEvent:  b.pendingBuffer.length() > 0,
			ParserSkipped: b.parserSkipped,
		}
	}
	b.clearDeferredLeadingNoopChunks()
	var chunks [][]byte
	if failureEvent := b.buildFailureEvent(*decision, b.clientRetryEnabled); failureEvent != nil {
		chunks = append(chunks, failureEvent)
	}
	return InspectionSseResult{
		Chunks:        chunks,
		Intercepted:   decision,
		PendingEvent:  b.pendingBuffer.length() > 0,
		ParserSkipped: b.parserSkipped,
	}
}

func (b *ResponseInspectionBuffer) runPolicies(event ParsedStreamEvent, frames []gatewayproto.SemanticFrame) *InspectionDecision {
	for _, policy := range b.policies {
		if decision := policy(event, frames); decision != nil {
			return decision
		}
	}
	return nil
}

func framesHaveVisibleOutput(frames []gatewayproto.SemanticFrame) bool {
	for _, frame := range frames {
		if frame.VisibleOutput {
			return true
		}
	}
	return false
}

func (b *ResponseInspectionBuffer) drainDeferredLeadingNoopChunks() [][]byte {
	if len(b.deferredLeadingNoopChunks) == 0 {
		return nil
	}
	chunks := b.deferredLeadingNoopChunks
	b.deferredLeadingNoopChunks = nil
	return chunks
}

func (b *ResponseInspectionBuffer) clearDeferredLeadingNoopChunks() {
	b.deferredLeadingNoopChunks = nil
}

func (b *ResponseInspectionBuffer) canPassThroughUninspectableVisibleOutputTextEvent(event ParsedStreamEvent) bool {
	if b.inspectVisibleOutputTextEvents {
		return false
	}
	if event.Data == nil || event.DataParseError {
		return false
	}
	if !isSafeVisibleOutputOnlyRoot(event.Data) {
		return false
	}
	eventType := orDefault(event.EventType, event.EventName)
	if eventType == "response.output_text.delta" || eventType == "response.output_text.done" {
		return true
	}
	if eventType != "message" && event.EventName != "" {
		return false
	}
	choices, _ := event.Data["choices"].([]any)
	if len(choices) == 0 {
		return false
	}
	for _, entry := range choices {
		row, ok := entry.(map[string]any)
		if !ok || !isVisibleOutputOnlyChatCompletionChoice(row) {
			return false
		}
	}
	return true
}

func (b *ResponseInspectionBuffer) canPassThroughCommonResponsesTextDeltaBuffer(rawBuffer []byte) bool {
	if b.inspectVisibleOutputTextEvents {
		return false
	}
	return isCommonResponsesTextDeltaSseBuffer(rawBuffer)
}

// failureEventForDecision mirrors failureEventForDecision +
// buildGatewayStreamFailureEvent.
func failureEventForDecision(decision InspectionDecision, clientRetryEnabled bool) []byte {
	if decision.Action == DecisionDiscardEvent || decision.Action == DecisionDryRun {
		return nil
	}
	code := decision.ErrorCode
	if code == "" {
		code = "upstream_stream_interrupted"
	}
	payload := `{"type":"response.failed","response":{"status":"failed","error":{"code":` +
		jsonString(code) + `,"message":` + jsonString(decision.Message) + `}}}`
	_ = clientRetryEnabled
	return []byte("event: response.failed\ndata: " + payload + "\n\n")
}

func isDeferrableLeadingChatCompletionNoopEvent(event ParsedStreamEvent) bool {
	data := event.Data
	if data == nil {
		return false
	}
	if object, _ := data["object"].(string); object != "chat.completion.chunk" {
		return false
	}
	if _, hasError := data["error"]; hasError {
		return false
	}
	if _, hasUsage := data["usage"]; hasUsage {
		return false
	}
	choices, _ := data["choices"].([]any)
	if len(choices) == 0 {
		return false
	}
	for _, entry := range choices {
		row, ok := entry.(map[string]any)
		if !ok || !isNoopChatCompletionChoice(row) {
			return false
		}
	}
	return true
}

func isNoopChatCompletionChoice(choice map[string]any) bool {
	if choice == nil {
		return false
	}
	if value, has := choice["finish_reason"]; has && value != nil {
		return false
	}
	if text, ok := choice["text"].(string); ok && len(text) > 0 {
		return false
	}
	if _, has := choice["message"]; has {
		return false
	}
	delta, ok := choice["delta"].(map[string]any)
	if !ok {
		return false
	}
	for key, value := range delta {
		if key == "role" {
			if _, ok := value.(string); ok {
				continue
			}
			return false
		}
		if key == "content" {
			if value == nil {
				continue
			}
			if text, ok := value.(string); ok && text == "" {
				continue
			}
			return false
		}
		return false
	}
	return true
}

func isSafeVisibleOutputOnlyRoot(data map[string]any) bool {
	for _, key := range []string{"error", "response", "usage", "status", "finish_reason", "code", "message"} {
		if _, has := data[key]; has {
			return false
		}
	}
	return true
}

func isVisibleOutputOnlyChatCompletionChoice(choice map[string]any) bool {
	if choice == nil {
		return false
	}
	if value, has := choice["finish_reason"]; has && value != nil {
		return false
	}
	if _, has := choice["error"]; has {
		return false
	}
	if _, has := choice["message"]; has {
		return false
	}
	if text, has := choice["text"]; has {
		_, isString := text.(string)
		return isString
	}
	delta, ok := choice["delta"].(map[string]any)
	if !ok {
		return false
	}
	hasContent := false
	hasRefusal := false
	for key, value := range delta {
		switch key {
		case "content":
			if _, ok := value.(string); !ok {
				return false
			}
			hasContent = true
		case "refusal":
			if _, ok := value.(string); !ok {
				return false
			}
			hasRefusal = true
		case "role":
			if _, ok := value.(string); !ok {
				return false
			}
		default:
			return false
		}
	}
	return hasContent || hasRefusal
}

// ---- common responses text delta buffer fast path ----

var (
	commonResponsesTextDeltaSsePrefix     = []byte("event: response.output_text.delta\ndata: " + `{"type":"response.output_text.delta","delta":"`)
	commonResponsesTextDeltaSseSuffix     = []byte("\"}\n\n")
	commonResponsesTextDeltaSseCrLfSuffix = []byte("\"}\r\n\r\n")
	commonResponsesTextDeltaSseCrSuffix   = []byte("\"}\r\r")
)

func isCommonResponsesTextDeltaSseBuffer(buffer []byte) bool {
	if !bytes.HasPrefix(buffer, commonResponsesTextDeltaSsePrefix) {
		return false
	}
	suffixLength := commonResponsesTextDeltaSuffixLength(buffer)
	if suffixLength == 0 {
		return false
	}
	for index := len(commonResponsesTextDeltaSsePrefix); index < len(buffer)-suffixLength; index++ {
		code := buffer[index]
		if code == jsonQuoteByte || code == jsonBackslashByte || code < jsonSpaceByte {
			return false
		}
	}
	return true
}

func commonResponsesTextDeltaSuffixLength(buffer []byte) int {
	if bytes.HasSuffix(buffer, commonResponsesTextDeltaSseSuffix) {
		return len(commonResponsesTextDeltaSseSuffix)
	}
	if bytes.HasSuffix(buffer, commonResponsesTextDeltaSseCrLfSuffix) {
		return len(commonResponsesTextDeltaSseCrLfSuffix)
	}
	if bytes.HasSuffix(buffer, commonResponsesTextDeltaSseCrSuffix) {
		return len(commonResponsesTextDeltaSseCrSuffix)
	}
	return 0
}

const (
	jsonQuoteByte     = 34
	jsonBackslashByte = 92
	jsonSpaceByte     = 32
)

// jsonString renders a Go string as a JSON string literal.
func jsonString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

// ---- pendingSseEventBuffer (PendingSseEventBuffer port) ----

type pendingSseEventBuffer struct {
	chunks               [][]byte
	headIndex            int
	size                 int
	nextBoundaryEndIndex int // -1 = undefined
}

var (
	sseCrLfBoundary        = []byte("\r\n\r\n")
	sseLfBoundary          = []byte("\n\n")
	sseCrBoundary          = []byte("\r\r")
	sseEventBoundarySuffix = []byte("\n\n")
)

func newPendingSseEventBuffer() pendingSseEventBuffer {
	return pendingSseEventBuffer{nextBoundaryEndIndex: -1}
}

func (p *pendingSseEventBuffer) length() int { return p.size }

func (p *pendingSseEventBuffer) push(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	previousSize := p.size
	previousTail := p.tail(3)
	p.chunks = append(p.chunks, chunk)
	p.size += len(chunk)
	if p.nextBoundaryEndIndex < 0 {
		p.nextBoundaryEndIndex = findBoundaryEndAfterAppend(previousSize, previousTail, chunk)
	}
}

func (p *pendingSseEventBuffer) shiftEvent() []byte {
	if p.nextBoundaryEndIndex < 0 {
		return nil
	}
	event := p.consumePrefix(p.nextBoundaryEndIndex)
	p.nextBoundaryEndIndex = p.findBoundaryEndFromStart()
	return event
}

func (p *pendingSseEventBuffer) drain() []byte {
	buffered := p.consumePrefix(p.size)
	p.nextBoundaryEndIndex = -1
	return buffered
}

func (p *pendingSseEventBuffer) drainEnsuringBoundary() []byte {
	if p.size == 0 {
		return nil
	}
	hasBoundary := p.endsWithBoundary()
	drained := p.drain()
	if hasBoundary {
		return drained
	}
	return append(append([]byte(nil), drained...), sseEventBoundarySuffix...)
}

func (p *pendingSseEventBuffer) consumePrefix(length int) []byte {
	if length <= 0 || p.size == 0 {
		return nil
	}
	boundedLength := length
	if boundedLength > p.size {
		boundedLength = p.size
	}
	first := p.chunks[p.headIndex]
	if first != nil && boundedLength < len(first) {
		output := first[:boundedLength]
		p.chunks[p.headIndex] = first[boundedLength:]
		p.size -= boundedLength
		return output
	}
	if first != nil && boundedLength == len(first) {
		p.headIndex++
		p.size -= boundedLength
		p.compactConsumedChunks()
		return first
	}

	var parts [][]byte
	remaining := boundedLength
	for remaining > 0 {
		current := p.chunks[p.headIndex]
		if current == nil {
			break
		}
		if len(current) <= remaining {
			parts = append(parts, current)
			remaining -= len(current)
			p.headIndex++
		} else {
			parts = append(parts, current[:remaining])
			p.chunks[p.headIndex] = current[remaining:]
			remaining = 0
		}
	}
	p.size -= boundedLength - remaining
	p.compactConsumedChunks()
	if len(parts) == 1 {
		return parts[0]
	}
	return bytes.Join(parts, nil)
}

func (p *pendingSseEventBuffer) findBoundaryEndFromStart() int {
	offset := 0
	var tail []byte
	for index := p.headIndex; index < len(p.chunks); index++ {
		chunk := p.chunks[index]
		boundary := findBoundaryEndInChunk(offset, tail, chunk)
		if boundary >= 0 {
			return boundary
		}
		offset += len(chunk)
		tail = trailingBytes(tail, chunk, 3)
	}
	return -1
}

func (p *pendingSseEventBuffer) tail(length int) []byte {
	if length <= 0 || p.size == 0 {
		return nil
	}
	targetLength := length
	if targetLength > p.size {
		targetLength = p.size
	}
	var parts [][]byte
	remaining := targetLength
	for index := len(p.chunks) - 1; index >= p.headIndex && remaining > 0; index-- {
		chunk := p.chunks[index]
		partLength := len(chunk)
		if partLength > remaining {
			partLength = remaining
		}
		parts = append([][]byte{chunk[len(chunk)-partLength:]}, parts...)
		remaining -= partLength
	}
	return bytes.Join(parts, nil)
}

func (p *pendingSseEventBuffer) endsWithBoundary() bool {
	suffix := p.tail(4)
	return bufferEndsWith(suffix, sseCrLfBoundary) ||
		bufferEndsWith(suffix, sseLfBoundary) ||
		bufferEndsWith(suffix, sseCrBoundary)
}

func (p *pendingSseEventBuffer) compactConsumedChunks() {
	if p.headIndex == 0 {
		return
	}
	if p.headIndex >= len(p.chunks) {
		p.chunks = nil
		p.headIndex = 0
		return
	}
	if p.headIndex > 64 && p.headIndex*2 > len(p.chunks) {
		p.chunks = append([][]byte(nil), p.chunks[p.headIndex:]...)
		p.headIndex = 0
	}
}

func findBoundaryEndAfterAppend(previousSize int, previousTail []byte, chunk []byte) int {
	boundary := findBoundaryEndInChunk(previousSize, previousTail, chunk)
	if boundary < 0 {
		return -1
	}
	return boundary
}

// findBoundaryEndInChunk returns the absolute end index of the earliest SSE
// event boundary, or -1.
func findBoundaryEndInChunk(chunkOffset int, previousTail []byte, chunk []byte) int {
	crossBoundary := findCrossChunkBoundaryEnd(chunkOffset, previousTail, chunk)
	inChunkBoundary := findSseEventBoundary(chunk)
	inChunkBoundaryEnd := -1
	if inChunkBoundary >= 0 {
		inChunkBoundaryEnd = chunkOffset + inChunkBoundary
	}
	if crossBoundary < 0 {
		return inChunkBoundaryEnd
	}
	if inChunkBoundaryEnd < 0 {
		return crossBoundary
	}
	if crossBoundary < inChunkBoundaryEnd {
		return crossBoundary
	}
	return inChunkBoundaryEnd
}

func findCrossChunkBoundaryEnd(chunkOffset int, previousTail []byte, chunk []byte) int {
	if len(previousTail) == 0 || len(chunk) == 0 {
		return -1
	}
	prefixLength := len(chunk)
	if prefixLength > 3 {
		prefixLength = 3
	}
	combined := append(append([]byte(nil), previousTail...), chunk[:prefixLength]...)
	index, endIndex := findSseEventBoundaryWithIndex(combined)
	if index < 0 || index >= len(previousTail) || endIndex <= len(previousTail) {
		return -1
	}
	return chunkOffset - len(previousTail) + endIndex
}

// findSseEventBoundary returns the end index of the earliest boundary.
func findSseEventBoundary(buffer []byte) int {
	index, endIndex := findSseEventBoundaryWithIndex(buffer)
	if index < 0 {
		return -1
	}
	return endIndex
}

func findSseEventBoundaryWithIndex(buffer []byte) (int, int) {
	bestIndex := -1
	bestLength := 0
	for _, token := range [][]byte{sseCrLfBoundary, sseLfBoundary, sseCrBoundary} {
		index := bytes.Index(buffer, token)
		if index < 0 {
			continue
		}
		if bestIndex < 0 || index < bestIndex || (index == bestIndex && len(token) < bestLength) {
			bestIndex = index
			bestLength = len(token)
		}
	}
	if bestIndex < 0 {
		return -1, -1
	}
	return bestIndex, bestIndex + bestLength
}

func trailingBytes(previousTail []byte, chunk []byte, length int) []byte {
	if len(chunk) >= length {
		return chunk[len(chunk)-length:]
	}
	combined := append(append([]byte(nil), previousTail...), chunk...)
	combinedLength := len(combined)
	if combinedLength > length {
		combinedLength = length
	}
	return combined[len(combined)-combinedLength:]
}

func bufferEndsWith(buffer []byte, suffix []byte) bool {
	return len(buffer) >= len(suffix) && bytes.Equal(buffer[len(buffer)-len(suffix):], suffix)
}
