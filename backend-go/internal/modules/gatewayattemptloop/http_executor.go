package gatewayattemptloop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaydispatch"
	"juhe-ai/backend-go/internal/modules/gatewayresponse"
	"juhe-ai/backend-go/internal/modules/gatewayretry"
	"juhe-ai/backend-go/internal/modules/gatewayupstream"
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
}

func (e HTTPExecutor) Execute(ctx context.Context, attempt Attempt) (AttemptResult, error) {
	if e.Prepare == nil {
		return AttemptResult{}, fmt.Errorf("gateway http attempt prepare function is required")
	}
	upstreamInput, responseInput, err := e.Prepare(ctx, attempt)
	if err != nil {
		return AttemptResult{RetryAllowed: attempt.ReplayAllowed, Failure: FailureFacts{Message: err.Error()}}, err
	}
	upstreamInput.Context = ctx
	dispatchResult, dispatchErr := e.Dispatcher.Dispatch(upstreamInput)
	if dispatchErr != nil {
		return AttemptResult{RetryAllowed: attempt.ReplayAllowed, Failure: FailureFacts{Message: boundedText(dispatchErr.Error(), 1000)}}, dispatchErr
	}
	responseInput.Context = ctx
	responseInput.Dispatch = dispatchResult
	responseInput.ResponsePolicy.HasAlternativeAPIKeys = responseInput.ResponsePolicy.HasAlternativeAPIKeys || attempt.HasAlternativeKeys
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
		if preparedResolver != nil {
			return preparedResolver(statusCode, body)
		}
		if responseInput.ResponseDisposition == gatewayretry.ResponseDispositionExplicitPolicy {
			return gatewayretry.ResponseDispositionExplicitPolicy, nil
		}
		return gatewayretry.ResponseDispositionCompleteTransparent, nil
	}
	handled, handleErr := e.Handler.Handle(responseInput)
	if handled.State == gatewayresponse.StateSucceeded {
		return AttemptResult{Success: true, Committed: true, Usage: handled.Handoff.Usage, Audit: handled.Handoff.Audit}, handleErr
	}
	failure := handled.Handoff.Retry.Failure
	bodyText := string(handled.BufferedBody)
	errorCode, errorType, message := extractErrorFacts(bodyText)
	if errorCode == "" {
		errorCode = failure.ErrorCode
	}
	if errorType == "" {
		errorType = failure.ErrorType
	}
	if message == "" && failure.Err != nil {
		message = failure.Err.Error()
	}
	return AttemptResult{
		Committed:        handled.TransportCommitted || handled.SemanticCommitted || handled.BytesWritten > 0,
		RetryAllowed:     handled.RetryAllowed && attempt.ReplayAllowed,
		KeyScopedFailure: handled.Handoff.Retry.Classification.WouldAvoidAPIKey,
		Failure:          FailureFacts{StatusCode: failure.StatusCode, ErrorCode: boundedText(errorCode, 256), ErrorType: boundedText(errorType, 256), BodyText: boundedText(bodyText, 64<<10), Message: boundedText(message, 1000)},
		Usage:            handled.Handoff.Usage,
		Audit:            handled.Handoff.Audit,
		PolicyDecision:   policyDecision,
	}, handleErr
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
