// Package gatewayresponse owns one already-dispatched upstream response. It
// combines bounded body ownership with protocol inspection, but leaves HTTP
// routing, account policy and candidate retry orchestration to its caller.
package gatewayresponse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"juhe-ai/backend-go/internal/gatewayaudit"
	"juhe-ai/backend-go/internal/modules/gatewaycodexresponses"
	"juhe-ai/backend-go/internal/modules/gatewaydeadline"
	"juhe-ai/backend-go/internal/modules/gatewaydispatch"
	"juhe-ai/backend-go/internal/modules/gatewaydownstream"
	"juhe-ai/backend-go/internal/modules/gatewayretry"
	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	"juhe-ai/backend-go/internal/modules/gatewayusage"
	"juhe-ai/backend-go/internal/protocols/codexresponses"
	"juhe-ai/backend-go/internal/protocols/openai"
)

const (
	TransportJSON   Transport = "json"
	TransportStream Transport = "stream"
)

type Transport string

var (
	ErrSinkRequired             = errors.New("网关响应 handler 缺少下游 sink")
	ErrUnsupportedTransport     = errors.New("网关响应 transport 不支持")
	ErrUpstreamStatus           = errors.New("上游响应状态码非成功")
	ErrDestinationWrite         = errors.New("写入下游响应失败")
	ErrCodexProtocolIntercepted = errors.New("Codex Responses 协议严格模式已拦截")
	ErrCodexProtocolBlocked     = errors.New("Codex Responses 协议安全模式已阻断")
	ErrInvalidCodexCheckpoint   = errors.New("Codex Responses checkpoint 必须显式声明")
	ErrInspectorAlreadySet      = errors.New("响应 handler 不允许外部覆盖 protocol inspector")
	ErrInvalidInitialCommit     = errors.New("响应 handler initial commit 无效")
	ErrInvalidResponsePolicy    = errors.New("响应 handler response disposition 无效")
	ErrJSONAlreadyCommitted     = errors.New("JSON 响应已提交，禁止重复写入")
)

type Checkpoint struct{ provenance codexresponses.Provenance }

func RawUpstreamCheckpoint() Checkpoint {
	return Checkpoint{provenance: codexresponses.ProvenanceRawUpstream}
}

func GatewayBridgeCheckpoint() Checkpoint {
	return Checkpoint{provenance: codexresponses.ProvenanceGatewayBridge}
}

func (c Checkpoint) valid() bool {
	return c.provenance == codexresponses.ProvenanceRawUpstream || c.provenance == codexresponses.ProvenanceGatewayBridge
}

type CodexGuard struct {
	Mode         codexresponses.Mode
	Checkpoint   Checkpoint
	EnvelopeKind gatewaycodexresponses.JSONEnvelopeKind
	SSELimits    openai.SSELimits
	CreateItemID func(prefix, itemType string, sequence, outputIndex int) string
}

type Handler struct {
	Dispatcher gatewaydispatch.Dispatcher
	Now        func() time.Time
}

type ResponseDispositionResolver func(statusCode int, body []byte) (gatewayretry.ResponseDisposition, error)

type Input struct {
	Context                context.Context
	Dispatch               gatewaydispatch.Result
	Transport              Transport
	Sink                   gatewaystreamrelay.Sink
	StartedAt              time.Time
	HadFailedAttempt       bool
	Codex                  *CodexGuard
	InitialCommit          codexresponses.CommitState
	RelayOptions           gatewaystreamrelay.Options
	OnFirstByte            func()
	OnTransportCommit      func()
	OnFirstSemanticOutput  func()
	CommittedFailureSignal gatewaystreamrelay.CommittedFailureSignal
	ResponseDisposition    gatewayretry.ResponseDisposition
	DispositionResolver    ResponseDispositionResolver
	ResponsePolicy         ResponsePolicyFacts
}

// ResponsePolicyFacts carries caller-owned routing facts that cannot be
// inferred from transport alone. Body-derived fields are bounded and merged
// only when explicit response policy is enabled.
type ResponsePolicyFacts struct {
	ErrorCode             string
	ErrorType             string
	ErrorMessage          string
	HasAlternativeAPIKeys bool
}

type State string

const (
	StateSucceeded                State = "succeeded"
	StateUpstreamFailureForwarded State = "upstream_failure_forwarded"
	StateFailedBeforeCommit       State = "failed_before_commit"
	StateFailedAfterCommit        State = "failed_after_commit"
)

type GuardSummary struct {
	Revision          string
	Mode              codexresponses.Mode
	Provenance        codexresponses.Provenance
	Outcome           codexresponses.Outcome
	DiagnosticCodes   []string
	OmittedIssueCount int
	RepairRuleIDs     []string
}

type RetryHandoff struct {
	Allowed        bool
	Failure        gatewayretry.Failure
	Classification gatewayretry.FailureClassification
}

type Handoff struct {
	Retry  RetryHandoff
	Usage  gatewayusage.TerminalFacts
	Audit  gatewayaudit.TerminalInput
	Commit codexresponses.CommitState
}

type Result struct {
	State               State
	StatusCode          int
	BytesRead           int64
	BytesWritten        int64
	TransportCommitted  bool
	SemanticCommitted   bool
	RetryAllowed        bool
	BufferedBody        []byte
	Guard               *GuardSummary
	Stream              *gatewaystreamrelay.Result
	TerminalDisposition *gatewaystreamrelay.TerminalDisposition
	Handoff             Handoff
}

// Handle is retained as a compact compatibility entry point for callers that
// already classify the transport. JSON and stream paths still have distinct
// internal ownership and inspector rules.
func (h Handler) Handle(input Input) (Result, error) {
	if err := validateCommit(input.InitialCommit); err != nil {
		return h.failAndClose(input, StateFailedBeforeCommit, err, gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "initial_commit_invalid", "响应 initial commit 无效", false)
	}
	if err := validateResponseDisposition(input.ResponseDisposition); err != nil {
		return h.failAndClose(input, StateFailedBeforeCommit, err, gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "response_disposition_invalid", "响应 disposition 无效", false)
	}
	if input.Transport == TransportJSON && (input.InitialCommit.SemanticCommitted || input.InitialCommit.DownstreamBytes > 0) {
		return h.failAndClose(input, StateFailedAfterCommit, ErrJSONAlreadyCommitted, gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "json_response_already_committed", "JSON 响应已经产生语义提交，禁止重复写入", false)
	}
	if input.Transport == TransportJSON {
		return h.handleJSON(input)
	}
	if input.Transport == TransportStream {
		return h.handleStream(input)
	}
	return h.failAndClose(input, StateFailedBeforeCommit, ErrUnsupportedTransport, gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "response_transport_unsupported", "不支持的响应 transport", false)
}

func (h Handler) handleJSON(input Input) (Result, error) {
	if input.Dispatch.Response == nil {
		return Result{}, gatewaydispatch.ErrResponseMissing
	}
	if input.Dispatch.Response.Body == nil {
		return Result{}, gatewaydispatch.ErrResponseBodyMissing
	}
	if input.Codex != nil {
		if err := validateCodexGuard(input.Codex, TransportJSON); err != nil {
			return h.failAndClose(input, StateFailedBeforeCommit, err, gatewayretry.ResponseSignalProtocolContract, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "codex_guard_config_invalid", "Codex Responses guard 配置无效", false)
		}
	}
	status := input.Dispatch.Response.StatusCode
	raw, err := h.Dispatcher.ReadBody(input.Dispatch)
	if err != nil {
		failure := classifyBufferedReadFailure(err)
		return h.failure(input, StateFailedBeforeCommit, status, 0, nil, err, failure.signal, failure.attribution, failure.auditOutcome, failure.code, failure.message, failure.retryAllowed)
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		if input.DispositionResolver != nil {
			input.ResponseDisposition, err = input.DispositionResolver(status, raw)
			if err != nil {
				return h.failure(input, StateFailedBeforeCommit, status, int64(len(raw)), raw, err, gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "response_disposition_resolution_failed", "响应 disposition 解析失败", false)
			}
			if err := validateResponseDisposition(input.ResponseDisposition); err != nil {
				return h.failure(input, StateFailedBeforeCommit, status, int64(len(raw)), raw, err, gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "response_disposition_invalid", "响应 disposition 无效", false)
			}
		}
		if input.ResponseDisposition != gatewayretry.ResponseDispositionExplicitPolicy {
			return h.forwardUpstreamFailure(input, status, raw)
		}
		input.ResponsePolicy = mergeResponsePolicyFacts(input.ResponsePolicy, parseResponsePolicyFacts(raw))
		return h.failure(input, StateFailedBeforeCommit, status, int64(len(raw)), raw, fmt.Errorf("%w: %d", ErrUpstreamStatus, status), gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionAccountUpstream, gatewayaudit.OutcomeUpstreamFailed, "upstream_http_status", "上游响应状态码表示失败", true)
	}

	usage := gatewayusage.UsageFacts{}
	if parsedUsage, usageErr := openai.ParseJSONUsage(raw, h.Dispatcher.MaxResponseBodyBytes); usageErr == nil {
		usage = usageFacts(parsedUsage)
	}
	body := raw
	var guardSummary *GuardSummary
	if input.Codex != nil {
		guard := input.Codex
		guardMode := guard.Mode
		if guardMode == "" {
			guardMode = codexresponses.ModeShadow
		}
		guardResult, guardErr := gatewaycodexresponses.InspectJSON(raw, gatewaycodexresponses.JSONOptions{
			Mode: guardMode, Provenance: guard.Checkpoint.provenance, EnvelopeKind: guard.EnvelopeKind,
			Commit: input.InitialCommit, MaxBytes: h.Dispatcher.MaxResponseBodyBytes, CreateItemID: guard.CreateItemID,
		})
		if guardErr != nil {
			guardSummary = &GuardSummary{Revision: codexresponses.Revision, Mode: guardMode, Provenance: guard.Checkpoint.provenance, Outcome: codexresponses.OutcomeBlocked, DiagnosticCodes: []string{"json_guard_error"}}
			if guardMode != codexresponses.ModeShadow {
				_, _, sentinel := guardFailure(guardMode)
				result, returnedErr := h.failureWithGuard(input, StateFailedBeforeCommit, status, int64(len(raw)), guardSummary, errors.Join(sentinel, guardErr), gatewayretry.ResponseSignalProtocolContract, gatewayusage.FailureAttributionAccountUpstream, gatewayaudit.OutcomeUpstreamFailed, guardCode(guardMode), "Codex Responses JSON guard 无法解析响应", true)
				result.Handoff.Usage.Usage = usage
				return result, returnedErr
			}
		} else {
			guardSummary = summarizeJSONGuard(guardResult)
			if shouldBlockJSON(guardMode, guardResult) {
				code, message, sentinel := guardFailure(guardMode)
				result, returnedErr := h.failureWithGuard(input, StateFailedBeforeCommit, status, int64(len(raw)), guardSummary, sentinel, gatewayretry.ResponseSignalProtocolContract, gatewayusage.FailureAttributionAccountUpstream, gatewayaudit.OutcomeUpstreamFailed, code, message, true)
				result.Handoff.Usage.Usage = usage
				return result, returnedErr
			}
			if guardResult.Changed {
				body = guardResult.Body
			}
		}
	}
	if input.Sink == nil {
		result, returnedErr := h.failureWithGuard(input, StateFailedBeforeCommit, status, int64(len(body)), guardSummary, ErrSinkRequired, gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "downstream_sink_missing", "响应下游 sink 缺失", false)
		result.Handoff.Usage.Usage = usage
		return result, returnedErr
	}
	bodyLength := int64(len(body))
	if err := stageResponse(input.Sink, status, input.Dispatch.Response.Header, gatewaydownstream.ModeJSON, &bodyLength); err != nil {
		return h.failureWithGuard(input, StateFailedBeforeCommit, status, int64(len(body)), guardSummary, err, gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "downstream_response_stage_failed", "准备下游响应头失败", false)
	}
	if _, err := commitResponseSink(input.Context, input.Sink, input.OnTransportCommit); err != nil {
		return h.failureWithGuard(input, StateFailedBeforeCommit, status, int64(len(body)), guardSummary, fmt.Errorf("%w: %w", ErrDestinationWrite, err), gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionClientLifecycle, gatewayaudit.OutcomeClientAborted, "downstream_header_commit_failed", "提交下游响应头失败", false)
	}
	written, writeErr := writeAll(input.Context, input.Sink, body, firstOutputCallback(input.OnFirstByte, input.OnFirstSemanticOutput))
	if writeErr != nil {
		result, returnedErr := h.failureWithGuard(input, stateForBytes(written), status, int64(len(body)), guardSummary, fmt.Errorf("%w: %v", ErrDestinationWrite, writeErr), gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionClientLifecycle, gatewayaudit.OutcomeClientAborted, "downstream_write_failed", "写入下游响应失败", false)
		result.BytesWritten = written
		commit := combineCommit(input.InitialCommit, commitStateFromSink(input.Sink, written, written > 0))
		result.TransportCommitted = commit.TransportCommitted
		result.SemanticCommitted = commit.SemanticCommitted
		result.Handoff = h.failureHandoff(input, status, result, gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionClientLifecycle, gatewayaudit.OutcomeClientAborted, "downstream_write_failed", "写入下游响应失败")
		result.Handoff.Commit = commit
		result.Handoff.Usage.Usage = usage
		result.Handoff.Usage.ResponseSnapshot = guardSummary
		return result, returnedErr
	}
	markSinkSemantic(input.Sink)
	commit := combineCommit(input.InitialCommit, commitStateFromSink(input.Sink, written, true))
	handoff := h.successHandoff(input, status, usage, commit)
	handoff.Usage.ResponseSnapshot = guardSummary
	return Result{State: StateSucceeded, StatusCode: status, BytesRead: int64(len(raw)), BytesWritten: written, TransportCommitted: commit.TransportCommitted, SemanticCommitted: commit.SemanticCommitted, Guard: guardSummary, Handoff: handoff, RetryAllowed: false}, nil
}

func (h Handler) handleStream(input Input) (Result, error) {
	if input.Dispatch.Response == nil {
		return Result{}, gatewaydispatch.ErrResponseMissing
	}
	if input.Dispatch.Response.Body == nil {
		return Result{}, gatewaydispatch.ErrResponseBodyMissing
	}
	if input.Sink == nil {
		return h.failAndClose(input, StateFailedBeforeCommit, ErrSinkRequired, gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "downstream_sink_missing", "响应下游 sink 缺失", false)
	}
	if input.Codex != nil {
		if err := validateCodexGuard(input.Codex, TransportStream); err != nil {
			return h.failAndClose(input, StateFailedBeforeCommit, err, gatewayretry.ResponseSignalProtocolContract, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "codex_guard_config_invalid", "Codex Responses guard 配置无效", false)
		}
	}
	status := input.Dispatch.Response.StatusCode
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		raw, err := h.Dispatcher.ReadBody(input.Dispatch)
		if err != nil {
			failure := classifyBufferedReadFailure(err)
			return h.failure(input, StateFailedBeforeCommit, status, 0, nil, err, failure.signal, failure.attribution, failure.auditOutcome, failure.code, failure.message, failure.retryAllowed)
		}
		if input.DispositionResolver != nil {
			input.ResponseDisposition, err = input.DispositionResolver(status, raw)
			if err != nil {
				return h.failure(input, StateFailedBeforeCommit, status, int64(len(raw)), raw, err, gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "response_disposition_resolution_failed", "响应 disposition 解析失败", false)
			}
			if err := validateResponseDisposition(input.ResponseDisposition); err != nil {
				return h.failure(input, StateFailedBeforeCommit, status, int64(len(raw)), raw, err, gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "response_disposition_invalid", "响应 disposition 无效", false)
			}
		}
		if input.ResponseDisposition != gatewayretry.ResponseDispositionExplicitPolicy {
			return h.forwardUpstreamFailure(input, status, raw)
		}
		input.ResponsePolicy = mergeResponsePolicyFacts(input.ResponsePolicy, parseResponsePolicyFacts(raw))
		return h.failure(input, StateFailedBeforeCommit, status, int64(len(raw)), raw, fmt.Errorf("%w: %d", ErrUpstreamStatus, status), gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionAccountUpstream, gatewayaudit.OutcomeUpstreamFailed, "upstream_http_status", "上游响应状态码表示失败", true)
	}
	options := input.RelayOptions
	options.OnFirstByte = input.OnFirstByte
	options.OnTransportCommit = input.OnTransportCommit
	options.OnFirstSemanticOutput = input.OnFirstSemanticOutput
	options.StatusCode = status
	options.StartedAt = input.StartedAt
	options.HadFailedAttempt = input.HadFailedAttempt
	if h.Now != nil {
		options.Now = h.Now
	}
	if options.Inspector != nil {
		return h.failAndClose(input, StateFailedBeforeCommit, ErrInspectorAlreadySet, gatewayretry.ResponseSignalProtocolContract, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "inspector_already_set", "响应 handler 不允许外部覆盖 inspector", false)
	}
	var inspector *gatewaycodexresponses.Inspector
	if input.Codex != nil {
		created, err := gatewaycodexresponses.NewInspector(gatewaycodexresponses.Options{
			Mode: input.Codex.Mode, Provenance: input.Codex.Checkpoint.provenance, SSELimits: input.Codex.SSELimits, CreateItemID: input.Codex.CreateItemID,
		})
		if err != nil {
			return h.failAndClose(input, StateFailedBeforeCommit, err, gatewayretry.ResponseSignalProtocolContract, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "codex_inspector_invalid", "Codex Responses inspector 配置无效", false)
		}
		inspector = created
		inspector.ObserveCommit(input.InitialCommit.TransportCommitted, input.InitialCommit.SemanticCommitted, input.InitialCommit.DownstreamBytes)
		options.Inspector = inspector
	} else {
		options.Inspector = gatewaystreamrelay.NewSSEPreCommitInspector()
	}
	if err := stageResponse(input.Sink, status, input.Dispatch.Response.Header, gatewaydownstream.ModeSSE, nil); err != nil {
		return h.failAndClose(input, StateFailedBeforeCommit, err, gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "downstream_response_stage_failed", "准备下游响应头失败", false)
	}
	relayResult, err := h.Dispatcher.Relay(input.Context, input.Dispatch, input.Sink, options)
	var guard *GuardSummary
	if inspector != nil {
		guard = guardFromStreamSnapshot(relayResult.Inspection.ResponseSnapshot)
	}
	result := resultFromRelay(input, status, relayResult, guard)
	if err != nil {
		return h.finishStreamFailure(input, result, err)
	}
	return result, nil
}

func (h Handler) finishStreamFailure(input Input, result Result, err error) (Result, error) {
	signal := gatewayretry.ResponseSignalNone
	phase := gatewayretry.PhaseUpstreamResponse
	attribution := gatewayusage.FailureAttributionAccountUpstream
	auditOutcome := gatewayaudit.OutcomeStreamFailed
	code := result.Handoff.Usage.ErrorCode
	message := result.Handoff.Usage.ErrorMessage
	if errors.Is(err, gatewaystreamrelay.ErrProtocolTerminalFailed) || errors.Is(err, gatewaystreamrelay.ErrInspector) {
		signal = gatewayretry.ResponseSignalProtocolContract
		code = firstText(code, "upstream_protocol_contract")
		message = firstText(message, "上游流式协议检查失败")
	} else if errors.Is(err, gatewaystreamrelay.ErrClientCanceled) || errors.Is(err, gatewaystreamrelay.ErrDestinationWrite) {
		phase = gatewayretry.PhaseClientLifecycle
		attribution = gatewayusage.FailureAttributionClientLifecycle
		auditOutcome = gatewayaudit.OutcomeClientAborted
		code = firstText(code, "client_stream_interrupted")
		message = firstText(message, "客户端流式连接已中断")
	} else if errors.Is(err, gatewaystreamrelay.ErrPreCommitBufferExceeded) {
		phase = gatewayretry.PhaseGatewayPolicy
		attribution = gatewayusage.FailureAttributionGatewayCapacity
		auditOutcome = gatewayaudit.OutcomeGatewayFailed
		code = "stream_precommit_buffer_exceeded"
		message = "流式响应预提交缓冲超过上限"
	} else if errors.Is(err, gatewaydeadline.ErrFirstByteDeadline) || errors.Is(err, gatewaystreamrelay.ErrPreCommitEvidenceMissing) || errors.Is(err, gatewaystreamrelay.ErrMissingTerminal) || errors.Is(err, gatewaystreamrelay.ErrSourceRead) || errors.Is(err, gatewaystreamrelay.ErrIdleDeadline) || errors.Is(err, gatewaystreamrelay.ErrTotalDeadline) || errors.Is(err, gatewaydispatch.ErrResponseBodyClose) {
		closeOnlyCompleted := errors.Is(err, gatewaydispatch.ErrResponseBodyClose) && result.Stream != nil && result.Stream.State == gatewaystreamrelay.StateCompleted
		if closeOnlyCompleted {
			phase = gatewayretry.PhaseGatewayPolicy
			attribution = gatewayusage.FailureAttributionGatewayPolicy
			auditOutcome = gatewayaudit.OutcomeGatewayFailed
			code = "upstream_response_close_failed"
			message = "流式响应已发送完成但关闭上游响应失败"
		} else {
			signal = gatewayretry.ResponseSignalStreamInterrupted
			code = firstText(code, "upstream_stream_interrupted")
			message = firstText(message, "上游流式响应未完整结束")
		}
	} else if errors.Is(err, gatewaystreamrelay.ErrInvalidLimits) {
		phase = gatewayretry.PhaseGatewayPolicy
		attribution = gatewayusage.FailureAttributionGatewayPolicy
		auditOutcome = gatewayaudit.OutcomeGatewayFailed
		code = "stream_relay_config_invalid"
		message = "流式 relay 配置无效"
	} else {
		phase = gatewayretry.PhaseGatewayPolicy
		attribution = gatewayusage.FailureAttributionGatewayPolicy
		auditOutcome = gatewayaudit.OutcomeGatewayFailed
		code = firstText(code, "stream_response_handler_failed")
		message = firstText(message, "流式响应处理失败")
	}
	if result.State == StateSucceeded {
		result.State = StateFailedBeforeCommit
		if result.TransportCommitted || result.SemanticCommitted || result.Handoff.Commit.DownstreamBytes > 0 {
			result.State = StateFailedAfterCommit
		}
	}
	if result.Handoff.Usage.Outcome != gatewayusage.OutcomeFailed {
		observed := result.Handoff.Usage
		observedAudit := result.Handoff.Audit
		observedCommit := result.Handoff.Commit
		result.Handoff = h.failureHandoff(input, result.StatusCode, result, signal, attribution, auditOutcome, code, message)
		result.Handoff.Commit = observedCommit
		result.Handoff.Usage.Usage = observed.Usage
		result.Handoff.Usage.FirstToken = observed.FirstToken
		result.Handoff.Usage.ResponseSnapshot = observed.ResponseSnapshot
		result.Handoff.Audit.TerminalRequired = observedAudit.TerminalRequired
		result.Handoff.Audit.TerminalReceived = observedAudit.TerminalReceived
	}
	failure := gatewayretry.Failure{Phase: phase, StatusCode: result.StatusCode, ErrorCode: code, Err: err, FirstByteForwarded: result.BytesWritten > 0, DownstreamCommitted: result.TransportCommitted, ResponseSignal: signal}
	classification := gatewayretry.ClassifyFailure(failure)
	result.Handoff.Retry = RetryHandoff{Allowed: result.RetryAllowed && classification.Retryable, Failure: failure, Classification: classification}
	result.RetryAllowed = result.Handoff.Retry.Allowed
	if signal != gatewayretry.ResponseSignalNone {
		result.TerminalDisposition = streamTerminalDisposition(input, result, err)
		return result, errors.Join(responseSignalError{signal: signal}, err)
	}
	result.TerminalDisposition = streamTerminalDisposition(input, result, err)
	return result, err
}

func streamTerminalDisposition(input Input, result Result, err error) *gatewaystreamrelay.TerminalDisposition {
	if result.Stream == nil {
		return nil
	}
	commit := gatewaystreamrelay.SinkState{TransportCommitted: result.TransportCommitted, SemanticCommitted: result.SemanticCommitted, DownstreamBytes: result.Handoff.Commit.DownstreamBytes}
	successTerminalSent := result.Stream.Inspection.TerminalReceived && !result.Stream.Inspection.Failed
	disposition := gatewaystreamrelay.DecideTerminalDisposition(gatewaystreamrelay.TerminalDispositionInput{
		Commit: commit, TerminalKind: streamTerminalKind(err), Capability: input.CommittedFailureSignal, SuccessTerminalSent: successTerminalSent,
	})
	return &disposition
}

func streamTerminalKind(err error) gatewaystreamrelay.TerminalKind {
	switch {
	case errors.Is(err, gatewaystreamrelay.ErrProtocolTerminalFailed), errors.Is(err, gatewaystreamrelay.ErrInspector):
		return gatewaystreamrelay.TerminalKindUpstreamProtocolFailure
	case errors.Is(err, gatewaystreamrelay.ErrMissingTerminal):
		return gatewaystreamrelay.TerminalKindMissingTerminal
	case errors.Is(err, gatewaystreamrelay.ErrClientCanceled), errors.Is(err, gatewaystreamrelay.ErrDestinationWrite):
		return gatewaystreamrelay.TerminalKindClientCanceled
	case errors.Is(err, gatewaystreamrelay.ErrSourceRead), errors.Is(err, gatewaystreamrelay.ErrIdleDeadline), errors.Is(err, gatewaystreamrelay.ErrTotalDeadline), errors.Is(err, gatewaydeadline.ErrFirstByteDeadline), errors.Is(err, gatewaystreamrelay.ErrPreCommitEvidenceMissing):
		return gatewaystreamrelay.TerminalKindReadFailure
	default:
		return gatewaystreamrelay.TerminalKindGatewayLocal
	}
}

type responseSignalError struct{ signal gatewayretry.ResponseSignal }

func (e responseSignalError) Error() string { return string(e.signal) }

func (e responseSignalError) Signal() gatewayretry.ResponseSignal { return e.signal }

func firstText(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func (h Handler) successHandoff(input Input, status int, usage gatewayusage.UsageFacts, commit codexresponses.CommitState) Handoff {
	return Handoff{Commit: commit, Usage: gatewayusage.TerminalFacts{Outcome: gatewayusage.OutcomeSucceeded, CompletedAt: h.now(), StatusCode: intPtr(status), Usage: usage}, Audit: gatewayaudit.TerminalInput{Success: true, Stream: false, HadFailedAttempt: input.HadFailedAttempt}}
}

func (h Handler) forwardUpstreamFailure(input Input, status int, body []byte) (Result, error) {
	if input.Sink == nil {
		return h.failure(input, StateFailedBeforeCommit, status, int64(len(body)), body, ErrSinkRequired, gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "downstream_sink_missing", "响应下游 sink 缺失", false)
	}
	bodyLength := int64(len(body))
	if err := stageResponse(input.Sink, status, input.Dispatch.Response.Header, gatewaydownstream.ModeOpaque, &bodyLength); err != nil {
		return h.failure(input, StateFailedBeforeCommit, status, int64(len(body)), body, err, gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionGatewayPolicy, gatewayaudit.OutcomeGatewayFailed, "downstream_response_stage_failed", "准备下游响应头失败", false)
	}
	if _, err := commitResponseSink(input.Context, input.Sink, input.OnTransportCommit); err != nil {
		return h.failure(input, StateFailedBeforeCommit, status, int64(len(body)), body, fmt.Errorf("%w: %w", ErrDestinationWrite, err), gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionClientLifecycle, gatewayaudit.OutcomeClientAborted, "downstream_header_commit_failed", "提交下游响应头失败", false)
	}
	written, err := writeAll(input.Context, input.Sink, body, input.OnFirstByte)
	if err != nil {
		result, returnedErr := h.failure(input, stateForBytes(written), status, int64(len(body)), nil, fmt.Errorf("%w: %v", ErrDestinationWrite, err), gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionClientLifecycle, gatewayaudit.OutcomeClientAborted, "downstream_write_failed", "写入下游响应失败", false)
		result.BytesWritten = written
		commit := combineCommit(input.InitialCommit, commitStateFromSink(input.Sink, written, written > 0))
		result.TransportCommitted = commit.TransportCommitted
		result.SemanticCommitted = commit.SemanticCommitted
		result.Handoff = h.failureHandoff(input, status, result, gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionClientLifecycle, gatewayaudit.OutcomeClientAborted, "downstream_write_failed", "写入下游响应失败")
		result.Handoff.Commit = commit
		return result, returnedErr
	}
	markSinkSemantic(input.Sink)
	commit := combineCommit(input.InitialCommit, commitStateFromSink(input.Sink, written, true))
	result := Result{State: StateUpstreamFailureForwarded, StatusCode: status, BytesRead: int64(len(body)), BytesWritten: written, TransportCommitted: commit.TransportCommitted, SemanticCommitted: commit.SemanticCommitted, Handoff: h.failureHandoff(input, status, Result{TransportCommitted: commit.TransportCommitted, SemanticCommitted: commit.SemanticCommitted, BytesWritten: written}, gatewayretry.ResponseSignalNone, gatewayusage.FailureAttributionAccountUpstream, gatewayaudit.OutcomeUpstreamFailed, "upstream_http_status", "上游完整失败响应已透明转发")}
	result.Handoff.Commit = commit
	result.RetryAllowed = false
	result.Handoff.Retry.Allowed = false
	return result, nil
}

func (h Handler) failure(input Input, state State, status int, bytesRead int64, buffered []byte, err error, signal gatewayretry.ResponseSignal, attribution gatewayusage.FailureAttribution, auditOutcome gatewayaudit.Outcome, code, message string, retryAllowed bool) (Result, error) {
	commit := combineCommit(input.InitialCommit, commitStateFromSink(input.Sink, 0, false))
	if commit.TransportCommitted || commit.SemanticCommitted || commit.DownstreamBytes > 0 {
		state = StateFailedAfterCommit
	}
	result := Result{State: state, StatusCode: status, BytesRead: bytesRead, BufferedBody: append([]byte(nil), buffered...), RetryAllowed: retryAllowed, TransportCommitted: commit.TransportCommitted, SemanticCommitted: commit.SemanticCommitted}
	result.Handoff = h.failureHandoff(input, status, result, signal, attribution, auditOutcome, code, message)
	result.Handoff.Commit = commit
	result.RetryAllowed = retryAllowed && result.Handoff.Retry.Allowed
	result.Handoff.Retry.Allowed = result.RetryAllowed
	return result, err
}

func (h Handler) failureWithGuard(input Input, state State, status int, bytesRead int64, guard *GuardSummary, err error, signal gatewayretry.ResponseSignal, attribution gatewayusage.FailureAttribution, auditOutcome gatewayaudit.Outcome, code, message string, retryAllowed bool) (Result, error) {
	result, returnedErr := h.failure(input, state, status, bytesRead, nil, err, signal, attribution, auditOutcome, code, message, retryAllowed)
	result.Guard = guard
	result.Handoff.Usage.ResponseSnapshot = guard
	return result, returnedErr
}

func (h Handler) failAndClose(input Input, state State, err error, signal gatewayretry.ResponseSignal, attribution gatewayusage.FailureAttribution, auditOutcome gatewayaudit.Outcome, code, message string, retryAllowed bool) (Result, error) {
	var closeErr error
	if input.Dispatch.Response != nil && input.Dispatch.Response.Body != nil {
		closeErr = input.Dispatch.Response.Body.Close()
	}
	result, returnedErr := h.failure(input, state, responseStatusCode(input.Dispatch), 0, nil, err, signal, attribution, auditOutcome, code, message, retryAllowed)
	if closeErr != nil {
		return result, errors.Join(returnedErr, closeErr)
	}
	return result, returnedErr
}

func (h Handler) failureHandoff(input Input, status int, result Result, signal gatewayretry.ResponseSignal, attribution gatewayusage.FailureAttribution, auditOutcome gatewayaudit.Outcome, code, message string) Handoff {
	recordedCode, recordedMessage := code, message
	policy := ResponsePolicyFacts{}
	if code == "upstream_http_status" && input.ResponseDisposition == gatewayretry.ResponseDispositionExplicitPolicy {
		policy = input.ResponsePolicy
		if policy.ErrorCode != "" {
			recordedCode = policy.ErrorCode
		}
		if policy.ErrorMessage != "" {
			recordedMessage = policy.ErrorMessage
		}
	}
	failure := gatewayretry.Failure{Phase: gatewayretry.PhaseUpstreamResponse, StatusCode: status, ErrorCode: recordedCode, ErrorType: policy.ErrorType, Err: errors.New(recordedMessage), FirstByteForwarded: result.BytesWritten > 0, DownstreamCommitted: result.TransportCommitted, HasAlternativeAPIKeys: policy.HasAlternativeAPIKeys, ResponseSignal: signal}
	if code == "upstream_http_status" {
		failure.ResponseDisposition = input.ResponseDisposition
		if failure.ResponseDisposition == gatewayretry.ResponseDispositionUnspecified {
			failure.ResponseDisposition = gatewayretry.ResponseDispositionCompleteTransparent
		}
	}
	if attribution == gatewayusage.FailureAttributionClientLifecycle {
		failure.Phase = gatewayretry.PhaseClientLifecycle
	} else if attribution == gatewayusage.FailureAttributionGatewayPolicy || attribution == gatewayusage.FailureAttributionGatewayCapacity {
		failure.Phase = gatewayretry.PhaseGatewayPolicy
	}
	classification := gatewayretry.ClassifyFailure(failure)
	allowed := classification.Retryable && input.InitialCommit.CanRetryUpstream() && !result.TransportCommitted
	return Handoff{Retry: RetryHandoff{Allowed: allowed, Failure: failure, Classification: classification}, Commit: input.InitialCommit, Usage: gatewayusage.TerminalFacts{Outcome: gatewayusage.OutcomeFailed, CompletedAt: h.now(), StatusCode: intPtrIfValid(status), FailureAttribution: attribution, ErrorCode: recordedCode, ErrorMessage: recordedMessage}, Audit: gatewayaudit.TerminalInput{RequestedOutcome: auditOutcome, Stream: input.Transport == TransportStream, HadFailedAttempt: input.HadFailedAttempt, ClientAborted: attribution == gatewayusage.FailureAttributionClientLifecycle, ErrorPhase: failurePhase(attribution), ErrorCode: recordedCode, ErrorMessage: recordedMessage}}
}

func failurePhase(attribution gatewayusage.FailureAttribution) string {
	switch attribution {
	case gatewayusage.FailureAttributionClientLifecycle:
		return "client"
	case gatewayusage.FailureAttributionGatewayPolicy, gatewayusage.FailureAttributionGatewayCapacity:
		return "gateway"
	default:
		return "upstream_response"
	}
}

type bufferedReadFailure struct {
	signal       gatewayretry.ResponseSignal
	attribution  gatewayusage.FailureAttribution
	auditOutcome gatewayaudit.Outcome
	code         string
	message      string
	retryAllowed bool
}

func classifyBufferedReadFailure(err error) bufferedReadFailure {
	if errors.Is(err, gatewaydispatch.ErrResponseBodyTooLarge) {
		return bufferedReadFailure{attribution: gatewayusage.FailureAttributionGatewayCapacity, auditOutcome: gatewayaudit.OutcomeGatewayFailed, code: "upstream_response_too_large", message: "上游响应超过网关限制"}
	}
	if errors.Is(err, gatewaydispatch.ErrResponseBodyRead) {
		return bufferedReadFailure{signal: gatewayretry.ResponseSignalStreamInterrupted, attribution: gatewayusage.FailureAttributionAccountUpstream, auditOutcome: gatewayaudit.OutcomeUpstreamFailed, code: "upstream_response_read_failed", message: "读取上游响应失败", retryAllowed: true}
	}
	if errors.Is(err, gatewaydispatch.ErrResponseBodyClose) {
		return bufferedReadFailure{attribution: gatewayusage.FailureAttributionGatewayPolicy, auditOutcome: gatewayaudit.OutcomeGatewayFailed, code: "upstream_response_close_failed", message: "关闭上游响应失败"}
	}
	return bufferedReadFailure{signal: gatewayretry.ResponseSignalStreamInterrupted, attribution: gatewayusage.FailureAttributionAccountUpstream, auditOutcome: gatewayaudit.OutcomeUpstreamFailed, code: "upstream_response_read_failed", message: "读取上游响应失败", retryAllowed: true}
}

func parseResponsePolicyFacts(raw []byte) ResponsePolicyFacts {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ResponsePolicyFacts{}
	}
	facts := ResponsePolicyFacts{
		ErrorCode:    rawPolicyString(envelope["code"]),
		ErrorType:    rawPolicyString(envelope["type"]),
		ErrorMessage: rawPolicyString(envelope["message"]),
	}
	if detail, ok := rawPolicyObject(envelope["error"]); ok {
		facts.ErrorCode = firstPolicyString(rawPolicyString(detail["code"]), facts.ErrorCode)
		facts.ErrorType = firstPolicyString(rawPolicyString(detail["type"]), facts.ErrorType)
		facts.ErrorMessage = firstPolicyString(rawPolicyString(detail["message"]), facts.ErrorMessage)
	} else if errorText := rawPolicyString(envelope["error"]); errorText != "" {
		facts.ErrorMessage = firstPolicyString(errorText, facts.ErrorMessage)
	}
	facts.ErrorCode = boundedPolicyText(facts.ErrorCode)
	facts.ErrorType = boundedPolicyText(facts.ErrorType)
	facts.ErrorMessage = boundedPolicyText(facts.ErrorMessage)
	return facts
}

func rawPolicyObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	return object, true
}

func rawPolicyString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}

func mergeResponsePolicyFacts(caller, body ResponsePolicyFacts) ResponsePolicyFacts {
	return ResponsePolicyFacts{ErrorCode: boundedPolicyText(firstPolicyString(caller.ErrorCode, body.ErrorCode)), ErrorType: boundedPolicyText(firstPolicyString(caller.ErrorType, body.ErrorType)), ErrorMessage: boundedPolicyText(firstPolicyString(caller.ErrorMessage, body.ErrorMessage)), HasAlternativeAPIKeys: caller.HasAlternativeAPIKeys || body.HasAlternativeAPIKeys}
}

func firstPolicyString(first, fallback string) string {
	if strings.TrimSpace(first) != "" {
		return first
	}
	return fallback
}

func boundedPolicyText(value string) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	if len(value) > 256 {
		value = value[:256]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func validateCodexGuard(guard *CodexGuard, transport Transport) error {
	if guard == nil || !guard.Checkpoint.valid() {
		return ErrInvalidCodexCheckpoint
	}
	mode := guard.Mode
	if mode != "" && mode != codexresponses.ModeShadow && mode != codexresponses.ModeSafeRepair && mode != codexresponses.ModeStrictIntercept {
		return gatewaycodexresponses.ErrJSONUnsupportedMode
	}
	if transport == TransportJSON {
		envelope := guard.EnvelopeKind
		if envelope != "" && envelope != gatewaycodexresponses.JSONEnvelopeResponse && envelope != gatewaycodexresponses.JSONEnvelopeCompact {
			return gatewaycodexresponses.ErrJSONUnsupportedEnvelope
		}
	}
	return nil
}

func validateCommit(commit codexresponses.CommitState) error {
	if commit.DownstreamBytes < 0 {
		return ErrInvalidInitialCommit
	}
	if (commit.SemanticCommitted || commit.DownstreamBytes > 0) && !commit.TransportCommitted {
		return ErrInvalidInitialCommit
	}
	return nil
}

func validateResponseDisposition(disposition gatewayretry.ResponseDisposition) error {
	switch disposition {
	case gatewayretry.ResponseDispositionUnspecified, gatewayretry.ResponseDispositionCompleteTransparent, gatewayretry.ResponseDispositionExplicitPolicy:
		return nil
	default:
		return ErrInvalidResponsePolicy
	}
}

func summarizeJSONGuard(value gatewaycodexresponses.JSONResult) *GuardSummary {
	codes := make([]string, 0, len(value.Issues))
	for _, issue := range value.Issues {
		codes = append(codes, issue.Code)
	}
	return &GuardSummary{Revision: value.Revision, Mode: value.Mode, Provenance: value.Provenance, Outcome: value.Outcome, DiagnosticCodes: codes, OmittedIssueCount: value.OmittedIssueCount, RepairRuleIDs: append([]string(nil), value.RepairRuleIDs...)}
}

func guardFromStreamSnapshot(snapshot any) *GuardSummary {
	guard, ok := snapshot.(gatewaycodexresponses.GuardSnapshot)
	if !ok {
		return nil
	}
	codes := make([]string, 0, len(guard.Diagnostics))
	for _, issue := range guard.Diagnostics {
		codes = append(codes, issue.Code)
	}
	return &GuardSummary{Revision: guard.Revision, Mode: guard.Mode, Provenance: guard.Provenance, Outcome: guard.Outcome, DiagnosticCodes: codes, OmittedIssueCount: guard.OmittedIssueCount, RepairRuleIDs: append([]string(nil), guard.RepairRuleIDs...)}
}

func shouldBlockJSON(mode codexresponses.Mode, result gatewaycodexresponses.JSONResult) bool {
	if mode == codexresponses.ModeStrictIntercept {
		return result.Outcome == codexresponses.OutcomeRepairable || result.Outcome == codexresponses.OutcomeBlocked || result.Outcome == codexresponses.OutcomeLateViolation
	}
	return mode == codexresponses.ModeSafeRepair && (result.Outcome == codexresponses.OutcomeBlocked || result.Outcome == codexresponses.OutcomeLateViolation)
}

func guardCode(mode codexresponses.Mode) string {
	if mode == codexresponses.ModeStrictIntercept {
		return "codex_responses_protocol_intercepted"
	}
	return "codex_responses_protocol_blocked"
}

func guardFailure(mode codexresponses.Mode) (string, string, error) {
	if mode == codexresponses.ModeStrictIntercept {
		return "codex_responses_protocol_intercepted", "Codex Responses JSON 严格模式已拦截", ErrCodexProtocolIntercepted
	}
	return "codex_responses_protocol_blocked", "Codex Responses JSON 安全模式已阻断", ErrCodexProtocolBlocked
}

func stageResponse(sink gatewaystreamrelay.Sink, status int, header http.Header, mode gatewaydownstream.Mode, bodyBytes *int64) error {
	staged, ok := sink.(gatewaydownstream.StagedSink)
	if !ok {
		return nil
	}
	plan, err := gatewaydownstream.NewPlan(status, header, mode, bodyBytes)
	if err != nil {
		return err
	}
	return staged.Stage(plan)
}

func commitResponseSink(ctx context.Context, sink gatewaystreamrelay.Sink, onCommit func()) (gatewaystreamrelay.SinkState, error) {
	stateful, ok := sink.(gatewaystreamrelay.StatefulSink)
	if !ok {
		return gatewaystreamrelay.SinkState{}, nil
	}
	before := stateful.Snapshot()
	err := stateful.Commit(ctx)
	after := stateful.Snapshot()
	if !before.TransportCommitted && after.TransportCommitted && onCommit != nil {
		onCommit()
	}
	return after, err
}

func markSinkSemantic(sink gatewaystreamrelay.Sink) {
	if stateful, ok := sink.(gatewaystreamrelay.StatefulSink); ok {
		stateful.MarkSemantic()
	}
}

func sinkState(sink gatewaystreamrelay.Sink) gatewaystreamrelay.SinkState {
	if stateful, ok := sink.(gatewaystreamrelay.StatefulSink); ok {
		return stateful.Snapshot()
	}
	return gatewaystreamrelay.SinkState{}
}

func commitStateFromSink(sink gatewaystreamrelay.Sink, written int64, semantic bool) codexresponses.CommitState {
	state := sinkState(sink)
	return codexresponses.CommitState{
		TransportCommitted: state.TransportCommitted || written > 0,
		SemanticCommitted:  state.SemanticCommitted || semantic,
		DownstreamBytes:    max(state.DownstreamBytes, written),
	}
}

func writeAll(ctx context.Context, sink gatewaystreamrelay.Sink, body []byte, onFirstByte func()) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var written int64
	for len(body) > 0 {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, err := sink.Write(ctx, body)
		if n < 0 || n > len(body) {
			return written, fmt.Errorf("invalid sink write count %d", n)
		}
		if n > 0 && written == 0 && onFirstByte != nil {
			onFirstByte()
		}
		if n > 0 {
			markSinkSemantic(sink)
		}
		written += int64(n)
		body = body[n:]
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrNoProgress
		}
	}
	return written, nil
}

func firstOutputCallback(onFirstByte, onFirstSemanticOutput func()) func() {
	return func() {
		if onFirstByte != nil {
			onFirstByte()
		}
		if onFirstSemanticOutput != nil {
			onFirstSemanticOutput()
		}
	}
}

func stateForBytes(bytesWritten int64) State {
	if bytesWritten > 0 {
		return StateFailedAfterCommit
	}
	return StateFailedBeforeCommit
}

func streamState(result gatewaystreamrelay.Result) State {
	if result.State == gatewaystreamrelay.StateCompleted {
		return StateSucceeded
	}
	if result.BytesWritten > 0 {
		return StateFailedAfterCommit
	}
	return StateFailedBeforeCommit
}

func resultFromRelay(input Input, status int, relay gatewaystreamrelay.Result, guard *GuardSummary) Result {
	commit := combineCommit(input.InitialCommit, codexresponses.CommitState{
		TransportCommitted: relay.TransportCommitted,
		SemanticCommitted:  relay.SemanticCommitted,
		DownstreamBytes:    relay.BytesWritten,
	})
	state := streamState(relay)
	if state != StateSucceeded && (commit.TransportCommitted || commit.SemanticCommitted || commit.DownstreamBytes > 0) {
		state = StateFailedAfterCommit
	}
	usage := relay.Handoff.Usage
	if guard != nil {
		usage.ResponseSnapshot = guard
	}
	return Result{
		State: state, StatusCode: status, BytesRead: relay.BytesRead, BytesWritten: relay.BytesWritten,
		TransportCommitted: commit.TransportCommitted, SemanticCommitted: commit.SemanticCommitted,
		RetryAllowed: relay.RetryAllowed && input.InitialCommit.CanRetryUpstream(), Guard: guard, Stream: &relay,
		Handoff: Handoff{Usage: usage, Audit: relay.Handoff.Audit, Commit: commit},
	}
}

func combineCommit(left, right codexresponses.CommitState) codexresponses.CommitState {
	return codexresponses.CommitState{TransportCommitted: left.TransportCommitted || right.TransportCommitted, SemanticCommitted: left.SemanticCommitted || right.SemanticCommitted, DownstreamBytes: boundedAdd(left.DownstreamBytes, right.DownstreamBytes)}
}

func boundedAdd(left, right int64) int64 {
	if left < 0 || right < 0 || right > int64(^uint64(0)>>1)-left {
		return int64(^uint64(0) >> 1)
	}
	return left + right
}

func usageFacts(value openai.SSEUsage) gatewayusage.UsageFacts {
	result := gatewayusage.UsageFacts{InputTokens: cloneInt64(value.InputTokens), OutputTokens: cloneInt64(value.OutputTokens), CacheReadTokens: cloneInt64(value.CacheReadTokens), CacheWriteTokens: cloneInt64(value.CacheWriteTokens), CacheWrite1hTokens: cloneInt64(value.CacheWrite1hTokens), ThinkingTokens: cloneInt64(value.ThinkingTokens), InputImageTokens: cloneInt64(value.InputImageTokens), OutputImageTokens: cloneInt64(value.OutputImageTokens), InputAudioTokens: cloneInt64(value.InputAudioTokens), OutputAudioTokens: cloneInt64(value.OutputAudioTokens), OutputImageCount: cloneInt64(value.OutputImageCount)}
	if value.ServiceTier != nil {
		result.ReportedServiceTier = *value.ServiceTier
	}
	return result
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func (h Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func intPtr(value int) *int { return &value }

func intPtrIfValid(value int) *int {
	if value < 100 || value > 599 {
		return nil
	}
	return &value
}

func responseStatusCode(result gatewaydispatch.Result) int {
	if result.Response == nil {
		return 0
	}
	return result.Response.StatusCode
}
