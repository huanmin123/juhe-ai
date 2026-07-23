// Package gatewaycodexresponses connects the protocol-only Codex Responses
// state machine to the gateway's bounded stream relay.
package gatewaycodexresponses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	"juhe-ai/backend-go/internal/modules/gatewayusage"
	"juhe-ai/backend-go/internal/protocols/codexresponses"
	"juhe-ai/backend-go/internal/protocols/openai"
)

var (
	ErrUnsupportedMode      = errors.New("Codex Responses stream inspector 不支持该模式")
	ErrInvalidProvenance    = errors.New("Codex Responses stream inspector provenance 无效")
	ErrTransformRequired    = errors.New("Codex Responses strict/safe 模式必须通过 transform seam 调用")
	ErrRewriteFrameTooLarge = errors.New("Codex Responses safe repair SSE frame 超过限制")
	ErrRewriteFailed        = errors.New("Codex Responses safe repair SSE frame 重写失败")
)

type Options struct {
	Mode         codexresponses.Mode
	Provenance   codexresponses.Provenance
	SSELimits    openai.SSELimits
	CreateItemID func(prefix, itemType string, sequence, outputIndex int) string
}

type GuardSnapshot struct {
	Revision          string
	Mode              codexresponses.Mode
	Provenance        codexresponses.Provenance
	Outcome           codexresponses.Outcome
	Diagnostics       []codexresponses.Issue
	OmittedIssueCount int
	Stream            codexresponses.StreamSnapshot
	RepairRuleIDs     []string
}

type Inspector struct {
	mode               codexresponses.Mode
	provenance         codexresponses.Provenance
	sse                *openai.SSEInspector
	contract           *codexresponses.StreamState
	responseResourceID string
	outcome            codexresponses.Outcome
	semanticOutput     bool
	strictBlocked      bool
	coverageGaps       int
	commitState        codexresponses.CommitState
	limits             openai.SSELimits
	frameBuffer        []byte
	currentFrame       []byte
	pendingRewrite     []byte
	transformFinished  bool
	safeBlocked        bool
	repairRuleIDs      map[string]struct{}
}

var _ gatewaystreamrelay.TerminalInspector = (*Inspector)(nil)
var _ gatewaystreamrelay.TransformingInspector = (*Inspector)(nil)

func NewInspector(options Options) (*Inspector, error) {
	mode := options.Mode
	if mode == "" {
		mode = codexresponses.ModeShadow
	}
	if mode != codexresponses.ModeShadow && mode != codexresponses.ModeStrictIntercept && mode != codexresponses.ModeSafeRepair {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedMode, mode)
	}
	if options.Provenance != codexresponses.ProvenanceRawUpstream && options.Provenance != codexresponses.ProvenanceGatewayBridge {
		return nil, fmt.Errorf("%w: %s", ErrInvalidProvenance, options.Provenance)
	}
	limits := options.SSELimits
	if limits == (openai.SSELimits{}) {
		limits = openai.DefaultSSELimits()
	}
	inspector := &Inspector{
		mode:          mode,
		provenance:    options.Provenance,
		contract:      codexresponses.NewStreamState(options.Provenance, mode == codexresponses.ModeSafeRepair, options.CreateItemID),
		outcome:       codexresponses.OutcomeClean,
		limits:        limits,
		repairRuleIDs: make(map[string]struct{}),
	}
	sse, err := openai.NewSSEInspectorWithObserver(limits, inspector.observeEvent)
	if err != nil {
		return nil, err
	}
	inspector.sse = sse
	return inspector, nil
}

func (i *Inspector) Observe(p []byte) error {
	if i == nil || i.sse == nil {
		return errors.New("Codex Responses stream inspector 未初始化")
	}
	if i.requiresTransform() {
		return ErrTransformRequired
	}
	return i.observeRaw(p)
}

func (i *Inspector) observeRaw(p []byte) error {
	written, err := i.sse.Write(p)
	if err != nil {
		return err
	}
	if written != len(p) {
		return fmt.Errorf("Codex Responses stream inspector short write: %d/%d", written, len(p))
	}
	return nil
}

func (i *Inspector) Transform(p []byte) ([]byte, error) {
	if i == nil || i.sse == nil {
		return nil, errors.New("Codex Responses stream inspector 未初始化")
	}
	if !i.requiresTransform() {
		if err := i.observeRaw(p); err != nil {
			return nil, err
		}
		return append([]byte(nil), p...), nil
	}
	if i.transformFinished {
		return nil, ErrTransformRequired
	}
	i.frameBuffer = append(i.frameBuffer, p...)
	var output bytes.Buffer
	for {
		frame, rest, ok := splitSSEFrame(i.frameBuffer)
		if !ok {
			break
		}
		i.frameBuffer = rest
		rewritten, err := i.processFrame(frame)
		if err != nil {
			return nil, err
		}
		_, _ = output.Write(rewritten)
	}
	if pendingSSEFrameTooLarge(i.frameBuffer, i.limits) {
		return nil, ErrRewriteFrameTooLarge
	}
	return output.Bytes(), nil
}

func (i *Inspector) FinishTransform() ([]byte, error) {
	if i == nil || i.sse == nil {
		return nil, errors.New("Codex Responses stream inspector 未初始化")
	}
	if !i.requiresTransform() {
		return nil, nil
	}
	if i.transformFinished {
		return nil, nil
	}
	i.transformFinished = true
	partial := append([]byte(nil), i.frameBuffer...)
	i.frameBuffer = nil
	if len(partial) == 0 {
		return nil, i.sse.Finish()
	}
	if i.sse.Snapshot().TerminalReceived && len(bytes.TrimSpace(partial)) > 0 {
		if i.mode == codexresponses.ModeSafeRepair {
			i.safeBlocked = true
		} else {
			i.strictBlocked = true
		}
	}
	i.currentFrame = partial
	if _, err := i.sse.Write(partial); err != nil {
		i.currentFrame = nil
		return nil, err
	}
	if err := i.sse.Finish(); err != nil {
		i.currentFrame = nil
		return nil, err
	}
	i.currentFrame = nil
	if len(i.pendingRewrite) > 0 {
		result := append([]byte(nil), i.pendingRewrite...)
		i.pendingRewrite = nil
		return result, nil
	}
	return ensureSSEBoundary(partial), nil
}

func (i *Inspector) Finish() error {
	if i == nil || i.sse == nil {
		return errors.New("Codex Responses stream inspector 未初始化")
	}
	return i.sse.Finish()
}

func (i *Inspector) ObserveCommit(transportCommitted, semanticCommitted bool, downstreamBytes int64) {
	if i == nil {
		return
	}
	i.commitState = codexresponses.CommitState{TransportCommitted: transportCommitted, SemanticCommitted: semanticCommitted, DownstreamBytes: downstreamBytes}
}

func (i *Inspector) Snapshot() gatewaystreamrelay.Inspection {
	if i == nil || i.sse == nil || i.contract == nil {
		return gatewaystreamrelay.Inspection{TerminalRequired: true, Failed: true, ErrorCode: "codex_responses_inspector_uninitialized", ErrorMessage: "Codex Responses stream inspector 未初始化"}
	}
	sse := i.sse.Snapshot()
	stream := i.contract.Snapshot()
	diagnostics := append([]codexresponses.Issue(nil), stream.Diagnostics...)
	omitted := stream.OmittedIssueCount
	if i.coverageGaps > 0 {
		issue := codexresponses.Issue{Code: "protocol_guard_coverage_degraded", Message: "Codex Responses SSE parser 无法解析协议事件", Provenance: codexresponses.ProvenanceUnknown, OutputIndex: -1}
		if len(diagnostics) < codexresponses.DiagnosticLimit {
			diagnostics = append(diagnostics, issue)
		} else {
			omitted++
		}
		if i.coverageGaps > 1 {
			omitted += i.coverageGaps - 1
		}
	}
	committedOutcome := codexresponses.OutcomeAtCommit(i.outcome, i.commitState)
	snapshot := GuardSnapshot{
		Revision: codexresponses.Revision, Mode: i.mode, Provenance: i.provenance,
		Outcome: committedOutcome, Diagnostics: diagnostics, OmittedIssueCount: omitted, Stream: stream,
		RepairRuleIDs: sortedRepairRules(i.repairRuleIDs),
	}
	result := gatewaystreamrelay.Inspection{
		TerminalRequired: true,
		TerminalReceived: sse.TerminalReceived,
		SemanticOutput:   i.semanticOutput,
		Failed:           sse.FailedReceived || i.strictBlocked || i.safeBlocked,
		Usage:            usageFacts(sse.Usage),
		ResponseSnapshot: snapshot,
	}
	if sse.Error != nil {
		result.ErrorCode = sse.Error.Code
		result.ErrorMessage = sse.Error.Message
	}
	if i.strictBlocked {
		result.ErrorCode = "codex_responses_protocol_intercepted"
		result.ErrorMessage = "Codex Responses 流式响应违反协议契约，严格模式已拦截"
	}
	if i.safeBlocked {
		result.ErrorCode = "codex_responses_protocol_blocked"
		result.ErrorMessage = "Codex Responses 流式响应无法安全修复，已阻断"
	}
	return result
}

func (i *Inspector) observeEvent(event openai.SSEEvent) error {
	if event.Malformed {
		i.coverageGaps++
		i.outcome = higherOutcome(i.outcome, codexresponses.OutcomeObservedUnknown)
		return nil
	}
	if event.Done || event.Data == nil {
		return nil
	}
	if response, ok := event.Data["response"].(map[string]any); ok {
		if responseID, ok := response["id"].(string); ok && responseID != "" && i.responseResourceID == "" {
			i.responseResourceID = responseID
		}
	}
	result := i.contract.Consume(codexresponses.StreamInput{ResponseResourceID: i.responseResourceID, Event: event.Data}, i.mode == codexresponses.ModeSafeRepair)
	eventOutcome := result.Outcome
	if i.mode == codexresponses.ModeSafeRepair {
		switch result.Outcome {
		case codexresponses.OutcomeBlocked:
			i.safeBlocked = true
		case codexresponses.OutcomeRepairable:
			if len(result.Repairs) == 0 {
				i.safeBlocked = true
			} else {
				rewritten, err := rewriteEvent(event, result.Repairs, i.currentFrame, i.limits)
				if err != nil {
					return err
				} else {
					i.pendingRewrite = rewritten
					i.repairRuleIDs["codex.r0.response.replace_stream_item_id"] = struct{}{}
					if i.provenance == codexresponses.ProvenanceGatewayBridge {
						eventOutcome = codexresponses.OutcomeRepairedBridge
					} else {
						eventOutcome = codexresponses.OutcomeRepairedSafe
					}
				}
			}
		}
	}
	i.outcome = higherOutcome(i.outcome, eventOutcome)
	if isSemanticEvent(event.EventType) {
		i.semanticOutput = true
	}
	if i.mode == codexresponses.ModeStrictIntercept && (result.Outcome == codexresponses.OutcomeRepairable || result.Outcome == codexresponses.OutcomeBlocked) {
		i.strictBlocked = true
	}
	return nil
}

func higherOutcome(current, next codexresponses.Outcome) codexresponses.Outcome {
	if outcomePriority(next) > outcomePriority(current) {
		return next
	}
	return current
}

func outcomePriority(outcome codexresponses.Outcome) int {
	switch outcome {
	case codexresponses.OutcomeLateViolation:
		return 6
	case codexresponses.OutcomeBlocked:
		return 5
	case codexresponses.OutcomeRepairedSafe, codexresponses.OutcomeRepairedBridge:
		return 4
	case codexresponses.OutcomeRepairable:
		return 3
	case codexresponses.OutcomeObservedUnknown:
		return 2
	case codexresponses.OutcomeClean:
		return 1
	default:
		return 0
	}
}

func isSemanticEvent(eventType string) bool {
	if eventType == "response.completed" || eventType == "response.output_item.added" || eventType == "response.output_item.done" {
		return true
	}
	return len(eventType) > len("response.") && len(eventType) > len(".delta") && eventType[:len("response.")] == "response." && eventType[len(eventType)-len(".delta"):] == ".delta"
}

func usageFacts(value openai.SSEUsage) gatewayusage.UsageFacts {
	result := gatewayusage.UsageFacts{
		InputTokens: cloneInt64(value.InputTokens), OutputTokens: cloneInt64(value.OutputTokens),
		CacheReadTokens: cloneInt64(value.CacheReadTokens), CacheWriteTokens: cloneInt64(value.CacheWriteTokens),
		CacheWrite1hTokens: cloneInt64(value.CacheWrite1hTokens), ThinkingTokens: cloneInt64(value.ThinkingTokens),
		InputImageTokens: cloneInt64(value.InputImageTokens), OutputImageTokens: cloneInt64(value.OutputImageTokens),
		InputAudioTokens: cloneInt64(value.InputAudioTokens), OutputAudioTokens: cloneInt64(value.OutputAudioTokens),
		OutputImageCount: cloneInt64(value.OutputImageCount),
	}
	if value.ServiceTier != nil {
		result.ReportedServiceTier = *value.ServiceTier
	}
	return result
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func (i *Inspector) processFrame(frame []byte) ([]byte, error) {
	i.pendingRewrite = nil
	i.currentFrame = frame
	if _, err := i.sse.Write(frame); err != nil {
		i.currentFrame = nil
		return nil, err
	}
	i.currentFrame = nil
	if len(i.pendingRewrite) > 0 {
		result := append([]byte(nil), i.pendingRewrite...)
		i.pendingRewrite = nil
		return result, nil
	}
	return append([]byte(nil), frame...), nil
}

func (i *Inspector) requiresTransform() bool {
	return i != nil && (i.mode == codexresponses.ModeStrictIntercept || i.mode == codexresponses.ModeSafeRepair)
}

func rewriteEvent(event openai.SSEEvent, repairs []codexresponses.StreamRepair, rawFrame []byte, limits openai.SSELimits) ([]byte, error) {
	if event.Data == nil || len(repairs) == 0 || len(rawFrame) == 0 {
		return nil, fmt.Errorf("%w: event data or repairs missing", ErrRewriteFailed)
	}
	data := cloneJSONObject(event.Data)
	for _, repair := range repairs {
		switch repair.Field {
		case "item_id":
			data["item_id"] = repair.ClientItemID
		case "item.id":
			item, ok := data["item"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%w: item missing", ErrRewriteFailed)
			}
			item["id"] = repair.ClientItemID
		case "response.output.id":
			response, ok := data["response"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%w: response missing", ErrRewriteFailed)
			}
			output, ok := response["output"].([]any)
			if !ok || repair.OutputIndex < 0 || repair.OutputIndex >= len(output) {
				return nil, fmt.Errorf("%w: output index invalid", ErrRewriteFailed)
			}
			item, ok := output[repair.OutputIndex].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%w: output item missing", ErrRewriteFailed)
			}
			item["id"] = repair.ClientItemID
		default:
			return nil, fmt.Errorf("%w: unknown repair field %s", ErrRewriteFailed, repair.Field)
		}
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRewriteFailed, err)
	}
	output, err := replaceSSEDataLines(rawFrame, payload)
	if err != nil {
		return nil, err
	}
	output = ensureSSEBoundary(output)
	if pendingSSEFrameTooLarge(output, limits) {
		return nil, ErrRewriteFrameTooLarge
	}
	return output, nil
}

func splitSSEFrame(buffer []byte) (frame, rest []byte, ok bool) {
	lineStart := 0
	for index := 0; index < len(buffer); {
		lineEnd := index
		lineBreak := 1
		switch buffer[index] {
		case '\r':
			if index+1 < len(buffer) && buffer[index+1] == '\n' {
				lineBreak = 2
			}
		case '\n':
		default:
			index++
			continue
		}
		if lineEnd == lineStart {
			boundary := index + lineBreak
			return append([]byte(nil), buffer[:boundary]...), append([]byte(nil), buffer[boundary:]...), true
		}
		index += lineBreak
		lineStart = index
	}
	return nil, buffer, false
}

func ensureSSEBoundary(value []byte) []byte {
	if hasSSEBoundary(value) {
		return append([]byte(nil), value...)
	}
	result := make([]byte, 0, len(value)+2)
	result = append(result, value...)
	result = append(result, '\n', '\n')
	return result
}

func hasSSEBoundary(value []byte) bool {
	return bytes.HasSuffix(value, []byte("\n\n")) || bytes.HasSuffix(value, []byte("\r\r")) || bytes.HasSuffix(value, []byte("\r\n\r\n"))
}

type sseRawLine struct {
	content []byte
	ending  []byte
}

func replaceSSEDataLines(rawFrame, payload []byte) ([]byte, error) {
	lines := splitSSERawLines(rawFrame)
	var output bytes.Buffer
	replaced := false
	for _, line := range lines {
		field := line.content
		if separator := bytes.IndexByte(field, ':'); separator >= 0 {
			field = field[:separator]
		}
		if bytes.Equal(field, []byte("data")) {
			if replaced {
				continue
			}
			output.WriteString("data: ")
			output.Write(payload)
			output.Write(line.ending)
			replaced = true
			continue
		}
		output.Write(line.content)
		output.Write(line.ending)
	}
	if !replaced {
		return nil, fmt.Errorf("%w: data line missing", ErrRewriteFailed)
	}
	return output.Bytes(), nil
}

func splitSSERawLines(raw []byte) []sseRawLine {
	lines := make([]sseRawLine, 0, 4)
	start := 0
	for index := 0; index < len(raw); {
		if raw[index] != '\r' && raw[index] != '\n' {
			index++
			continue
		}
		endingLength := 1
		if raw[index] == '\r' && index+1 < len(raw) && raw[index+1] == '\n' {
			endingLength = 2
		}
		lines = append(lines, sseRawLine{
			content: append([]byte(nil), raw[start:index]...),
			ending:  append([]byte(nil), raw[index:index+endingLength]...),
		})
		index += endingLength
		start = index
	}
	if start < len(raw) {
		lines = append(lines, sseRawLine{content: append([]byte(nil), raw[start:]...)})
	}
	return lines
}

func sseEventSize(frame []byte) int64 {
	var size int64
	for _, line := range splitSSERawLines(frame) {
		if len(line.content) == 0 {
			break
		}
		if size > 0 {
			size++
		}
		size += int64(len(line.content))
	}
	return size
}

func pendingSSEFrameTooLarge(frame []byte, limits openai.SSELimits) bool {
	if sseEventSize(frame) > limits.MaxEventBytes {
		return true
	}
	for _, line := range splitSSERawLines(frame) {
		if int64(len(line.content)) > limits.MaxLineBytes {
			return true
		}
	}
	return false
}

func cloneJSONObject(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, entry := range value {
		result[key] = cloneJSON(entry)
	}
	return result
}

func cloneJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneJSONObject(typed)
	case []any:
		result := make([]any, len(typed))
		for index, entry := range typed {
			result[index] = cloneJSON(entry)
		}
		return result
	default:
		return value
	}
}

func sortedRepairRules(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
