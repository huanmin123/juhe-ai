package gatewayattemptloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/gatewayaudit"
	"juhe-ai/backend-go/internal/modules/gatewaydeadline"
	"juhe-ai/backend-go/internal/modules/gatewaydispatch"
	"juhe-ai/backend-go/internal/modules/gatewayresponse"
	"juhe-ai/backend-go/internal/modules/gatewayresponseinspection"
	"juhe-ai/backend-go/internal/modules/gatewayretry"
	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	"juhe-ai/backend-go/internal/modules/gatewayupstream"
	"juhe-ai/backend-go/internal/modules/gatewayusage"
)

type PrepareHTTPAttempt func(context.Context, Attempt) (gatewayupstream.Input, gatewayresponse.Input, error)

// HTTPExecutor is the concrete adapter between the attempt loop and the
// already-reviewed request/dispatch/response seams. It owns one upstream
// response only; the Service still owns retry actions and mutations. The
// adapter only probes the typed account policy after reading a bounded failure
// body so unmatched upstream failures remain transparent.
type HTTPExecutor struct {
	Dispatcher gatewaydispatch.Dispatcher
	Handler    gatewayresponse.Handler
	Prepare    PrepareHTTPAttempt
	Now        func() time.Time
}

func (e HTTPExecutor) Execute(ctx context.Context, attempt Attempt) (AttemptResult, error) {
	if e.Prepare == nil {
		return AttemptResult{}, fmt.Errorf("gateway http attempt prepare function is required")
	}
	deadline, err := gatewaydeadline.New(ctx, attempt.Budget.FirstByteDeadline)
	if err != nil {
		return AttemptResult{}, err
	}
	defer deadline.Close()
	attemptCtx := deadline.Context()
	upstreamInput, responseInput, err := e.Prepare(attemptCtx, attempt)
	if err != nil {
		return AttemptResult{RetryAllowed: attempt.AvailabilityFailoverAllowed, Failure: FailureFacts{Message: err.Error()}}, err
	}
	upstreamInput.Context = attemptCtx
	dispatchResult, dispatchErr := e.Dispatcher.Dispatch(upstreamInput)
	if dispatchErr != nil {
		if causedByFirstByteDeadline(attemptCtx, dispatchErr) {
			return firstByteDeadlineResult(attempt, AttemptResult{}), gatewaydeadline.ErrFirstByteDeadline
		}
		return AttemptResult{RetryAllowed: attempt.AvailabilityFailoverAllowed, KeyScopedFailure: automaticAPIKeyFailover(attempt), Failure: FailureFacts{Message: boundedText(dispatchErr.Error(), 1000)}}, dispatchErr
	}
	responseInput.Context = attemptCtx
	responseInput.Dispatch = dispatchResult
	preparedOnFirstByte := responseInput.OnFirstByte
	responseInput.OnFirstByte = func() {
		if preparedOnFirstByte != nil {
			preparedOnFirstByte()
		}
	}
	preparedOnFirstSemanticOutput := responseInput.OnFirstSemanticOutput
	responseInput.OnFirstSemanticOutput = func() {
		observedAt := e.now()
		deadline.MarkVisible()
		if preparedOnFirstSemanticOutput != nil {
			preparedOnFirstSemanticOutput()
		}
		if attempt.OnFirstByte != nil {
			attempt.OnFirstByte(observedAt)
		}
	}
	preparedOnTransportCommit := responseInput.OnTransportCommit
	responseInput.OnTransportCommit = func() {
		if preparedOnTransportCommit != nil {
			preparedOnTransportCommit()
		}
	}
	automaticKeyFailover := automaticAPIKeyFailover(attempt)
	// Preserve caller-provided alternative-key facts for explicit account policy;
	// automatic key failover remains separately gated below.
	responseInput.ResponsePolicy.HasAlternativeAPIKeys = responseInput.ResponsePolicy.HasAlternativeAPIKeys || attempt.HasAlternativeKeys
	// This authorization comes only from the selected candidate. A prepared
	// response template must not broaden it.
	responseInput.ResponsePolicy.AutomaticAPIKeyFailover = automaticKeyFailover
	preparedResolver := responseInput.DispositionResolver
	var policyDecision *PolicyDecision
	responseInput.DispositionResolver = func(statusCode int, body []byte) (gatewayretry.ResponseDisposition, error) {
		errorCode, errorType, message := extractErrorFacts(string(body))
		rawRules, _ := attempt.Candidate.Credentials.Value("error_handling_rules")
		now := attempt.PolicyNow
		if now.IsZero() {
			now = time.Now()
		}
		decision, err := DecidePolicy(rawRules, FailureFacts{
			StatusCode: statusCode,
			ErrorCode:  errorCode,
			ErrorType:  errorType,
			BodyText:   boundedText(string(body), 64<<10),
			Message:    message,
		}, attempt.PolicySettings, now)
		if err != nil {
			return gatewayretry.ResponseDispositionUnspecified, err
		}
		if decision.Action != PolicyActionNone {
			copy := decision
			policyDecision = &copy
			return gatewayretry.ResponseDispositionExplicitPolicy, nil
		}
		if automaticKeyFailover {
			return gatewayretry.ResponseDispositionExplicitPolicy, nil
		}
		if preparedResolver != nil {
			return preparedResolver(statusCode, body)
		}
		if responseInput.ResponseDisposition == gatewayretry.ResponseDispositionExplicitPolicy {
			return gatewayretry.ResponseDispositionExplicitPolicy, nil
		}
		return gatewayretry.ResponseDispositionCompleteTransparent, nil
	}
	handled, handleErr := e.Handler.Handle(responseInput)
	if causedByFirstByteDeadline(attemptCtx, handleErr) &&
		handled.Handoff.Usage.FailureAttribution != gatewayusage.FailureAttributionDownstreamClosed &&
		!handled.Handoff.Audit.DownstreamClosed &&
		!handled.TransportCommitted && !handled.SemanticCommitted && handled.BytesWritten == 0 {
		return firstByteDeadlineResult(attempt, AttemptResult{Usage: handled.Handoff.Usage, Audit: handled.Handoff.Audit}), gatewaydeadline.ErrFirstByteDeadline
	}
	if handled.State == gatewayresponse.StateSucceeded {
		return AttemptResult{Success: true, Committed: true, Sink: attemptSinkState(responseInput.Sink, handled), Response: &handled, Usage: handled.Handoff.Usage, Audit: handled.Handoff.Audit, ResponseInspection: gatewayresponseinspection.CloneHandoff(handled.ResponseInspection)}, handleErr
	}
	failure := handled.Handoff.Retry.Failure
	bodyText := string(handled.BufferedBody)
	responseInspectionMatched := handled.ResponseInspection != nil && handled.ResponseInspection.Decision != nil
	errorCode, errorType, message := failure.ErrorCode, failure.ErrorType, ""
	if responseInspectionMatched {
		// The inspection decision owns these facts. Re-parsing its successful
		// upstream JSON as a normal error can replace its frozen error code.
		if failure.Err != nil {
			message = failure.Err.Error()
		}
	} else {
		errorCode, errorType, message = extractErrorFacts(bodyText)
		if errorCode == "" {
			errorCode = failure.ErrorCode
		}
		if errorType == "" {
			errorType = failure.ErrorType
		}
		if message == "" && failure.Err != nil {
			message = failure.Err.Error()
		}
	}
	keyScopedFailure := handled.Handoff.Retry.Classification.WouldAvoidAPIKey
	if automaticKeyFailover && failure.ResponseSignal == gatewayretry.ResponseSignalNone {
		keyScopedFailure = true
	}
	committed := handled.TransportCommitted || handled.SemanticCommitted || handled.BytesWritten > 0
	return AttemptResult{
		Committed:           committed,
		Sink:                attemptSinkState(responseInput.Sink, handled),
		Response:            &handled,
		RetryAllowed:        handled.RetryAllowed && attempt.AvailabilityFailoverAllowed,
		KeyScopedFailure:    keyScopedFailure,
		FallbackDisposition: FallbackAccountUnknown,
		Failure:             FailureFacts{StatusCode: failure.StatusCode, ErrorCode: boundedText(errorCode, 256), ErrorType: boundedText(errorType, 256), BodyText: boundedText(bodyText, 64<<10), Message: boundedText(message, 1000)},
		Usage:               handled.Handoff.Usage,
		Audit:               handled.Handoff.Audit,
		PolicyDecision:      policyDecision,
		ResponseInspection:  gatewayresponseinspection.CloneHandoff(handled.ResponseInspection),
	}, handleErr
}

func attemptSinkState(sink gatewaystreamrelay.Sink, handled gatewayresponse.Result) *gatewaystreamrelay.SinkState {
	state := gatewaystreamrelay.SinkState{
		TransportCommitted: handled.TransportCommitted,
		SemanticCommitted:  handled.SemanticCommitted,
		DownstreamBytes:    handled.Handoff.Commit.DownstreamBytes,
	}
	if handled.BytesWritten > state.DownstreamBytes {
		state.DownstreamBytes = handled.BytesWritten
	}
	if stateful, ok := sink.(gatewaystreamrelay.StatefulSink); ok {
		snapshot := stateful.Snapshot()
		state.TransportCommitted = state.TransportCommitted || snapshot.TransportCommitted
		state.SemanticCommitted = state.SemanticCommitted || snapshot.SemanticCommitted
		if snapshot.DownstreamBytes > state.DownstreamBytes {
			state.DownstreamBytes = snapshot.DownstreamBytes
		}
	}
	if state.SemanticCommitted || state.DownstreamBytes > 0 {
		state.TransportCommitted = true
	}
	return &state
}

func automaticAPIKeyFailover(attempt Attempt) bool {
	strategy, ok := attempt.Candidate.Credentials.StringValue("api_key_strategy")
	return ok && strings.EqualFold(strings.TrimSpace(strategy), "failover") && attempt.HasAlternativeKeys
}

func (e HTTPExecutor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func firstByteDeadlineResult(attempt Attempt, base AttemptResult) AttemptResult {
	message := gatewaydeadline.ErrFirstByteDeadline.Error()
	base.RetryAllowed = attempt.AvailabilityFailoverAllowed
	base.FallbackDisposition = FallbackAccountUnknown
	base.Failure.ErrorCode = "first_byte_timeout"
	base.Failure.Message = message
	base.Usage = gatewayusage.TerminalFacts{
		Outcome: gatewayusage.OutcomeFailed, CompletedAt: time.Now().UTC(),
		FailureAttribution: gatewayusage.FailureAttributionAccountUpstream,
		ErrorCode:          "first_byte_timeout", ErrorMessage: message,
	}
	base.Audit = gatewayaudit.TerminalInput{
		RequestedOutcome: gatewayaudit.OutcomeUpstreamFailed, Stream: base.Audit.Stream,
		HadFailedAttempt: base.Audit.HadFailedAttempt, ErrorPhase: "upstream_response",
		ErrorCode: "first_byte_timeout", ErrorMessage: message,
	}
	return base
}

func causedByFirstByteDeadline(ctx context.Context, err error) bool {
	if err == nil || !errors.Is(context.Cause(ctx), gatewaydeadline.ErrFirstByteDeadline) {
		return false
	}
	return errors.Is(err, gatewaydeadline.ErrFirstByteDeadline) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func extractErrorFacts(body string) (string, string, string) {
	body = boundedText(body, 64<<10)
	var value map[string]any
	if json.Unmarshal([]byte(body), &value) != nil {
		return "", "", ""
	}
	find := func(key string) string {
		if nested, ok := value["error"].(map[string]any); ok {
			if item := scalarText(nested[key]); item != "" {
				return item
			}
		}
		return scalarText(value[key])
	}
	code := find("code")
	if code == "" {
		code = find("status")
	}
	if code == "" {
		code = find("type")
	}
	return code, find("type"), find("message")
}

func scalarText(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return fmt.Sprintf("%g", typed)
	default:
		return ""
	}
}

var _ AttemptExecutor = HTTPExecutor{}
