// Package gatewaycodexresponses connects the protocol-only Codex Responses
// state machine to the gateway's bounded stream relay.
package gatewaycodexresponses

import (
	"errors"
	"fmt"

	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	"juhe-ai/backend-go/internal/modules/gatewayusage"
	"juhe-ai/backend-go/internal/protocols/codexresponses"
	"juhe-ai/backend-go/internal/protocols/openai"
)

var (
	ErrUnsupportedMode   = errors.New("Codex Responses stream inspector 不支持该模式")
	ErrInvalidProvenance = errors.New("Codex Responses stream inspector provenance 无效")
)

type Options struct {
	Mode       codexresponses.Mode
	Provenance codexresponses.Provenance
	SSELimits  openai.SSELimits
}

type GuardSnapshot struct {
	Revision          string
	Mode              codexresponses.Mode
	Provenance        codexresponses.Provenance
	Outcome           codexresponses.Outcome
	Diagnostics       []codexresponses.Issue
	OmittedIssueCount int
	Stream            codexresponses.StreamSnapshot
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
}

var _ gatewaystreamrelay.TerminalInspector = (*Inspector)(nil)

func NewInspector(options Options) (*Inspector, error) {
	mode := options.Mode
	if mode == "" {
		mode = codexresponses.ModeShadow
	}
	if mode != codexresponses.ModeShadow && mode != codexresponses.ModeStrictIntercept {
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
		mode:       mode,
		provenance: options.Provenance,
		contract:   codexresponses.NewStreamState(options.Provenance, false, nil),
		outcome:    codexresponses.OutcomeClean,
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
	written, err := i.sse.Write(p)
	if err != nil {
		return err
	}
	if written != len(p) {
		return fmt.Errorf("Codex Responses stream inspector short write: %d/%d", written, len(p))
	}
	return nil
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
	}
	result := gatewaystreamrelay.Inspection{
		TerminalRequired: true,
		TerminalReceived: sse.TerminalReceived,
		SemanticOutput:   i.semanticOutput,
		Failed:           sse.FailedReceived || i.strictBlocked,
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
	result := i.contract.Consume(codexresponses.StreamInput{ResponseResourceID: i.responseResourceID, Event: event.Data}, false)
	i.outcome = higherOutcome(i.outcome, result.Outcome)
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
		return 5
	case codexresponses.OutcomeBlocked:
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
