package accountprobe

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"juhe-ai/backend-go/internal/accounthealth"
	"juhe-ai/backend-go/internal/platform/upstreamtransport"
)

type AttemptTransport interface {
	ExecuteWithFence(context.Context, *http.Request, func(context.Context) error) (upstreamtransport.Result, error)
}

type HTTPAttempt interface {
	Method() string
	URL() string
	Header() http.Header
	Body() []byte
}

type Executor struct {
	Transport AttemptTransport
	Fence     func(context.Context) error
}

func (e Executor) Execute(ctx context.Context, mode EndpointMode, attempt APIKeyAttempt) (accounthealth.ProbeOutcome, upstreamtransport.Result, error) {
	return e.ExecuteAttempt(ctx, mode, attempt)
}

func (e Executor) ExecuteAttempt(ctx context.Context, mode EndpointMode, attempt HTTPAttempt) (accounthealth.ProbeOutcome, upstreamtransport.Result, error) {
	if e.Transport == nil {
		return accounthealth.ProbeOutcomeTaskFailure, upstreamtransport.Result{}, fmt.Errorf("account probe transport is required")
	}
	if attempt == nil {
		return accounthealth.ProbeOutcomeTaskFailure, upstreamtransport.Result{}, fmt.Errorf("account probe HTTP attempt is required")
	}
	request, err := http.NewRequestWithContext(ctx, attempt.Method(), attempt.URL(), bytes.NewReader(attempt.Body()))
	if err != nil {
		return accounthealth.ProbeOutcomeTaskFailure, upstreamtransport.Result{}, fmt.Errorf("build account probe HTTP request: %w", err)
	}
	request.Header = attempt.Header()
	result, executeErr := e.Transport.ExecuteWithFence(ctx, request, e.Fence)
	return ClassifyExecution(mode, result, executeErr), result, executeErr
}

// ClassifyExecution converts bounded transport and protocol facts into the
// automatic account-health attribution contract. A fully framed malformed or
// truncated upstream response is neutral: it lacks success evidence, but is
// not a local executor failure.
func ClassifyExecution(mode EndpointMode, result upstreamtransport.Result, executeErr error) accounthealth.ProbeOutcome {
	attempt := accounthealth.ProbeUpstreamAttempt{}
	if result.Attempted {
		attempt = accounthealth.NewProbeUpstreamAttempt(result.AttemptURL)
	}
	if result.FramingComplete {
		success := false
		if result.StatusCode >= http.StatusOK && result.StatusCode < http.StatusMultipleChoices && !result.BodyTruncated {
			if evidence, err := InspectEvidence(mode, result.Body, false); err == nil {
				success = evidence.Complete && !evidence.Failed
			}
		}
		return accounthealth.ClassifyAutomaticProbeOutcome(accounthealth.ProbeEvidence{
			Success: success, UpstreamAttempt: attempt,
			Transport: accounthealth.FramingCompleteTransport(result.StatusCode),
		})
	}
	if kind, ok := upstreamtransport.FailureKindOf(executeErr); ok && result.Attempted {
		var failure accounthealth.ProbeTransportFailureKind
		switch kind {
		case upstreamtransport.FailureTimeout:
			failure = accounthealth.ProbeTransportFailureTimeout
		case upstreamtransport.FailureConnection:
			failure = accounthealth.ProbeTransportFailureConnection
		case upstreamtransport.FailureRead:
			failure = accounthealth.ProbeTransportFailureRead
		}
		if failure != 0 {
			return accounthealth.ClassifyAutomaticProbeOutcome(accounthealth.ProbeEvidence{
				UpstreamAttempt: attempt, Transport: accounthealth.IncompleteTransport(failure),
			})
		}
	}
	return accounthealth.ClassifyAutomaticProbeOutcome(accounthealth.ProbeEvidence{
		UpstreamAttempt: attempt,
		Transport:       accounthealth.UnknownTransport(accounthealth.ProbeTaskFailureExecutor),
	})
}
