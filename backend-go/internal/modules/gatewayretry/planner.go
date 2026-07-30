// Package gatewayretry provides the side-effect-free candidate retry state
// machine used between route selection and upstream dispatch.
package gatewayretry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"juhe-ai/backend-go/internal/modules/gatewayerrors"
	"juhe-ai/backend-go/internal/modules/gatewayrouting"
	"juhe-ai/backend-go/internal/store/port"
)

type FailurePhase string

const (
	PhaseDownstreamClosed FailurePhase = "downstream_closed"
	PhaseGatewayPolicy    FailurePhase = "gateway_policy"
	PhaseUpstreamRequest  FailurePhase = "upstream_request"
	PhaseUpstreamResponse FailurePhase = "upstream_response"
)

type FailureClass string

const (
	FailureClassDownstreamClosed FailureClass = "downstream_closed"
	FailureClassGatewayPolicy    FailureClass = "gateway_policy"
	FailureClassRequestSemantic  FailureClass = "request_semantic"
	FailureClassCredential       FailureClass = "credential"
	FailureClassRateLimit        FailureClass = "rate_limit"
	FailureClassUpstreamService  FailureClass = "upstream_service"
	FailureClassTransport        FailureClass = "transport"
	FailureClassUpstreamProtocol FailureClass = "upstream_protocol"
	FailureClassUpstreamStream   FailureClass = "upstream_stream"
	FailureClassUnknown          FailureClass = "unknown"
)

// ResponseSignal carries an explicit, protocol-independent reason from a
// response adapter. It prevents a 2xx protocol failure from being disguised as
// a synthetic HTTP status or an upstream request transport error.
type ResponseSignal string

const (
	ResponseSignalNone              ResponseSignal = ""
	ResponseSignalProtocolContract  ResponseSignal = "protocol_contract"
	ResponseSignalStreamInterrupted ResponseSignal = "stream_interrupted"
)

type ResponseDisposition string

const (
	ResponseDispositionUnspecified         ResponseDisposition = ""
	ResponseDispositionCompleteTransparent ResponseDisposition = "complete_transparent"
	// ResponseDispositionExplicitPolicy opts into status/error classification;
	// it is not the Node account-policy action (retry_next/skip_account).
	ResponseDispositionExplicitPolicy ResponseDisposition = "explicit_policy"
)

type Failure struct {
	Phase                 FailurePhase
	StatusCode            int
	ErrorCode             string
	ErrorType             string
	Err                   error
	DownstreamClosed      bool
	FirstByteForwarded    bool
	DownstreamCommitted   bool
	HasAlternativeAPIKeys bool
	ResponseSignal        ResponseSignal
	ResponseDisposition   ResponseDisposition
}

type FailureClassification struct {
	Class              FailureClass
	Reason             string
	Retryable          bool
	WouldAvoidAPIKey   bool
	WouldAvoidAccount  bool
	WouldAvoidUpstream bool
}

type Action string

const (
	ActionAttempt   Action = "attempt"
	ActionSucceeded Action = "succeeded"
	ActionStopped   Action = "stopped"
	ActionRejected  Action = "rejected"
)

type Reason string

const (
	ReasonInitialAttempt      Reason = "initial_attempt"
	ReasonRetryableFailure    Reason = "retryable_failure"
	ReasonSucceeded           Reason = "succeeded"
	ReasonNoCandidates        Reason = "no_candidates"
	ReasonCandidatesExhausted Reason = "candidates_exhausted"
	ReasonMaxAttempts         Reason = "max_attempts"
	ReasonDownstreamClosed    Reason = "downstream_closed"
	ReasonContextDeadline     Reason = "context_deadline"
	ReasonDownstreamCommitted Reason = "downstream_committed"
	ReasonNonRetryableFailure Reason = "non_retryable_failure"
	ReasonInvalidTransition   Reason = "invalid_transition"
)

// PlanInput deliberately consumes the result of gatewayrouting.OrderBindings
// and the bounded account projection. It does not load, hydrate, or lease data.
type PlanInput struct {
	Route       gatewayrouting.OrderResult
	Candidates  []port.GatewayAccountCandidate
	Protocol    gatewayerrors.Protocol
	MaxAttempts int
}

type Attempt struct {
	Sequence int
	Binding  gatewayrouting.Binding
	Account  port.GatewayAccountCandidate
}

type Decision struct {
	Action            Action
	Reason            Reason
	Attempt           *Attempt
	Retry             bool
	Classification    FailureClassification
	Protocol          gatewayerrors.Protocol
	AttemptCount      int
	MaxAttempts       int
	AttemptsRemaining int
}

type queuedCandidate struct {
	binding gatewayrouting.Binding
	account port.GatewayAccountCandidate
}

// Planner is safe for concurrent callers. Repeated starts are idempotent and
// stale resolutions are rejected without changing the active attempt.
type Planner struct {
	mu           sync.Mutex
	protocol     gatewayerrors.Protocol
	maxAttempts  int
	queue        []queuedCandidate
	next         int
	attemptedIDs []string
	awaiting     *Decision
	terminal     *Decision
}

func NewPlanner(input PlanInput) (*Planner, error) {
	if input.MaxAttempts <= 0 {
		return nil, fmt.Errorf("max attempts must be greater than zero")
	}
	protocol := input.Protocol
	if protocol == "" {
		protocol = gatewayerrors.ProtocolOpenAI
	}
	for index, binding := range input.Route.Bindings {
		if strings.TrimSpace(binding.ID) == "" {
			return nil, fmt.Errorf("route binding %d has blank id", index)
		}
		if strings.TrimSpace(binding.GroupID) == "" {
			return nil, fmt.Errorf("route binding %q has blank group id", binding.ID)
		}
	}
	for index, candidate := range input.Candidates {
		if strings.TrimSpace(candidate.AccountID) == "" {
			return nil, fmt.Errorf("candidate %d has blank account id", index)
		}
		if strings.TrimSpace(candidate.GroupID) == "" {
			return nil, fmt.Errorf("candidate %q has blank group id", candidate.AccountID)
		}
	}

	queue := make([]queuedCandidate, 0, len(input.Candidates))
	seen := make(map[string]struct{}, len(input.Candidates))
	for _, binding := range input.Route.Bindings {
		for _, candidate := range input.Candidates {
			if candidate.GroupID != binding.GroupID {
				continue
			}
			if _, duplicate := seen[candidate.AccountID]; duplicate {
				continue
			}
			seen[candidate.AccountID] = struct{}{}
			queue = append(queue, queuedCandidate{binding: binding, account: candidate})
		}
	}

	return &Planner{
		protocol:    protocol,
		maxAttempts: input.MaxAttempts,
		queue:       queue,
	}, nil
}

// Start returns the first attempt or a terminal decision when the request is
// already canceled, expired, or has no eligible candidates.
func (p *Planner) Start(ctx context.Context) Decision {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.terminal != nil {
		return cloneDecision(*p.terminal)
	}
	if p.awaiting != nil {
		return cloneDecision(*p.awaiting)
	}
	if classification, reason, stopped := contextStop(ctx); stopped {
		return p.stopLocked(reason, classification)
	}
	if len(p.queue) == 0 {
		return p.stopLocked(ReasonNoCandidates, FailureClassification{})
	}
	return p.issueLocked(false, ReasonInitialAttempt, FailureClassification{})
}

// Fail resolves the current attempt. An intrinsically retryable failure only
// advances to a fresh candidate before the response is committed downstream.
func (p *Planner) Fail(ctx context.Context, attempt Attempt, failure Failure) Decision {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.terminal != nil {
		return cloneDecision(*p.terminal)
	}
	if !sameAttempt(p.awaiting, attempt) {
		return p.rejectLocked()
	}
	p.awaiting = nil

	if classification, reason, stopped := contextStop(ctx); stopped {
		return p.stopLocked(reason, classification)
	}
	classification := ClassifyFailure(failure)
	if classification.Reason == "context_deadline" {
		return p.stopLocked(ReasonContextDeadline, classification)
	}
	if classification.Reason == "downstream_closed" {
		return p.stopLocked(ReasonDownstreamClosed, classification)
	}
	if failure.FirstByteForwarded || failure.DownstreamCommitted {
		return p.stopLocked(ReasonDownstreamCommitted, classification)
	}
	if !classification.Retryable {
		return p.stopLocked(ReasonNonRetryableFailure, classification)
	}
	if len(p.attemptedIDs) >= p.maxAttempts {
		return p.stopLocked(ReasonMaxAttempts, classification)
	}
	if p.next >= len(p.queue) {
		return p.stopLocked(ReasonCandidatesExhausted, classification)
	}
	return p.issueLocked(true, ReasonRetryableFailure, classification)
}

func (p *Planner) Succeed(attempt Attempt) Decision {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.terminal != nil {
		return cloneDecision(*p.terminal)
	}
	if !sameAttempt(p.awaiting, attempt) {
		return p.rejectLocked()
	}
	p.awaiting = nil
	return p.finishLocked(ActionSucceeded, ReasonSucceeded, FailureClassification{})
}

func (p *Planner) AttemptedAccountIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.attemptedIDs...)
}

func (p *Planner) issueLocked(retry bool, reason Reason, classification FailureClassification) Decision {
	item := p.queue[p.next]
	p.next++
	attempt := Attempt{
		Sequence: len(p.attemptedIDs) + 1,
		Binding:  item.binding,
		Account:  item.account,
	}
	p.attemptedIDs = append(p.attemptedIDs, item.account.AccountID)
	decision := p.decisionLocked(ActionAttempt, reason, &attempt, retry, classification)
	p.awaiting = &decision
	return cloneDecision(decision)
}

func (p *Planner) stopLocked(reason Reason, classification FailureClassification) Decision {
	return p.finishLocked(ActionStopped, reason, classification)
}

func (p *Planner) rejectLocked() Decision {
	return p.decisionLocked(ActionRejected, ReasonInvalidTransition, nil, false, FailureClassification{})
}

func (p *Planner) finishLocked(action Action, reason Reason, classification FailureClassification) Decision {
	decision := p.decisionLocked(action, reason, nil, false, classification)
	p.terminal = &decision
	return cloneDecision(decision)
}

func (p *Planner) decisionLocked(action Action, reason Reason, attempt *Attempt, retry bool, classification FailureClassification) Decision {
	remaining := p.maxAttempts - len(p.attemptedIDs)
	if candidateRemaining := len(p.queue) - p.next; remaining > candidateRemaining {
		remaining = candidateRemaining
	}
	if remaining < 0 || action != ActionAttempt {
		remaining = 0
	}
	return Decision{
		Action:            action,
		Reason:            reason,
		Attempt:           cloneAttempt(attempt),
		Retry:             retry,
		Classification:    classification,
		Protocol:          p.protocol,
		AttemptCount:      len(p.attemptedIDs),
		MaxAttempts:       p.maxAttempts,
		AttemptsRemaining: remaining,
	}
}

func sameAttempt(current *Decision, provided Attempt) bool {
	return current != nil && current.Attempt != nil &&
		current.Attempt.Sequence == provided.Sequence &&
		current.Attempt.Binding.ID == provided.Binding.ID &&
		current.Attempt.Account.AccountID == provided.Account.AccountID
}

func cloneAttempt(attempt *Attempt) *Attempt {
	if attempt == nil {
		return nil
	}
	cloned := *attempt
	return &cloned
}

func cloneDecision(decision Decision) Decision {
	decision.Attempt = cloneAttempt(decision.Attempt)
	return decision
}

func contextStop(ctx context.Context) (FailureClassification, Reason, bool) {
	if ctx == nil {
		return FailureClassification{}, "", false
	}
	cause := context.Cause(ctx)
	if errors.Is(cause, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return downstreamClosedClassification(), ReasonContextDeadline, true
	}
	if ctx.Err() != nil {
		return downstreamClosedClassification(), ReasonDownstreamClosed, true
	}
	return FailureClassification{}, "", false
}

func ClassifyFailure(failure Failure) FailureClassification {
	if failure.DownstreamClosed || errors.Is(failure.Err, context.Canceled) {
		return downstreamClosedClassification()
	}
	if errors.Is(failure.Err, context.DeadlineExceeded) {
		return downstreamClosedClassification()
	}
	if failure.Phase == PhaseDownstreamClosed {
		return downstreamClosedClassification()
	}
	if failure.Phase == PhaseGatewayPolicy {
		return FailureClassification{Class: FailureClassGatewayPolicy, Reason: "gateway_policy_failure"}
	}
	if failure.Phase == PhaseUpstreamRequest {
		return FailureClassification{
			Class:              FailureClassTransport,
			Reason:             "upstream_transport_failure",
			Retryable:          true,
			WouldAvoidAccount:  true,
			WouldAvoidUpstream: true,
		}
	}
	if failure.Phase != PhaseUpstreamResponse {
		return FailureClassification{Class: FailureClassUnknown, Reason: "invalid_failure_phase"}
	}
	if failure.ResponseSignal != ResponseSignalNone {
		switch failure.ResponseSignal {
		case ResponseSignalProtocolContract:
			return FailureClassification{
				Class: FailureClassUpstreamProtocol, Reason: "upstream_protocol_contract",
				Retryable: true, WouldAvoidAccount: true,
			}
		case ResponseSignalStreamInterrupted:
			return FailureClassification{
				Class: FailureClassUpstreamStream, Reason: "upstream_stream_interrupted",
				Retryable: true, WouldAvoidAccount: true, WouldAvoidUpstream: true,
			}
		default:
			return FailureClassification{Class: FailureClassUnknown, Reason: "invalid_response_signal"}
		}
	}
	switch failure.ResponseDisposition {
	case ResponseDispositionUnspecified, ResponseDispositionCompleteTransparent:
		return FailureClassification{Class: FailureClassUpstreamService, Reason: "complete_response_transparent"}
	case ResponseDispositionExplicitPolicy:
		// Continue into explicit status/error policy classification below.
	default:
		return FailureClassification{Class: FailureClassUnknown, Reason: "invalid_response_disposition"}
	}

	errorCode := normalizeIdentifier(failure.ErrorCode)
	errorType := normalizeIdentifier(failure.ErrorType)
	if requestSemanticIdentifiers[errorCode] || requestSemanticIdentifiers[errorType] {
		return FailureClassification{Class: FailureClassRequestSemantic, Reason: "explicit_request_error"}
	}
	if credentialIdentifiers[errorCode] || credentialIdentifiers[errorType] || failure.StatusCode == 401 || failure.StatusCode == 403 {
		classification := FailureClassification{
			Class:     FailureClassCredential,
			Retryable: true,
		}
		if failure.HasAlternativeAPIKeys {
			classification.Reason = "credential_error_with_alternative_key"
			classification.WouldAvoidAPIKey = true
		} else {
			classification.Reason = "credential_error_without_alternative_key"
			classification.WouldAvoidAccount = true
		}
		return classification
	}
	if rateLimitIdentifiers[errorCode] || rateLimitIdentifiers[errorType] || failure.StatusCode == 429 {
		return FailureClassification{
			Class:             FailureClassRateLimit,
			Reason:            "upstream_rate_limit",
			Retryable:         true,
			WouldAvoidAccount: true,
		}
	}
	if failure.StatusCode >= 500 {
		return FailureClassification{
			Class:              FailureClassUpstreamService,
			Reason:             "upstream_server_error",
			Retryable:          true,
			WouldAvoidAccount:  true,
			WouldAvoidUpstream: true,
		}
	}
	return FailureClassification{Class: FailureClassUnknown, Reason: "unclassified_upstream_response"}
}

func downstreamClosedClassification() FailureClassification {
	return FailureClassification{Class: FailureClassDownstreamClosed, Reason: "downstream_closed"}
}

func normalizeIdentifier(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

var requestSemanticIdentifiers = map[string]bool{
	"content_policy_violation": true,
	"context_length_exceeded":  true,
	"invalid_prompt":           true,
	"invalid_request_error":    true,
	"model_not_found":          true,
	"unsupported_value":        true,
}

var credentialIdentifiers = map[string]bool{
	"authentication_error":   true,
	"invalid_api_key":        true,
	"invalid_authentication": true,
	"permission_denied":      true,
	"unauthorized":           true,
}

var rateLimitIdentifiers = map[string]bool{
	"insufficient_quota":  true,
	"rate_limit_error":    true,
	"rate_limit_exceeded": true,
}
