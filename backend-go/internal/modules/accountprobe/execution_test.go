package accountprobe

import (
	"context"
	"net/http"
	"testing"

	"juhe-ai/backend-go/internal/accounthealth"
	"juhe-ai/backend-go/internal/platform/upstreamtransport"
)

func TestClassifyExecutionPreservesAutomaticProbeAttribution(t *testing.T) {
	completeChat := []byte(`{"choices":[{"finish_reason":"stop"}]}`)
	tests := []struct {
		name    string
		mode    EndpointMode
		result  upstreamtransport.Result
		err     error
		outcome accounthealth.ProbeOutcome
	}{
		{name: "complete success", mode: ModeChatJSON, result: completeResult(http.StatusOK, completeChat), outcome: accounthealth.ProbeOutcomeCompleteSuccess},
		{name: "non 2xx neutral", mode: ModeChatJSON, result: completeResult(http.StatusTooManyRequests, completeChat), outcome: accounthealth.ProbeOutcomeFramingCompleteNeutral},
		{name: "missing completion neutral", mode: ModeChatJSON, result: completeResult(http.StatusOK, []byte(`{"choices":[]}`)), outcome: accounthealth.ProbeOutcomeFramingCompleteNeutral},
		{name: "malformed body neutral", mode: ModeChatJSON, result: completeResult(http.StatusOK, []byte(`not json`)), outcome: accounthealth.ProbeOutcomeFramingCompleteNeutral},
		{name: "truncated body neutral", mode: ModeChatJSON, result: func() upstreamtransport.Result {
			value := completeResult(http.StatusOK, completeChat)
			value.BodyTruncated = true
			return value
		}(), outcome: accounthealth.ProbeOutcomeFramingCompleteNeutral},
		{name: "close after complete remains success", mode: ModeChatJSON, result: completeResult(http.StatusOK, completeChat), err: transportFailure(upstreamtransport.FailureClose), outcome: accounthealth.ProbeOutcomeCompleteSuccess},
		{name: "attempt timeout", mode: ModeChatJSON, result: attemptedResult(), err: transportFailure(upstreamtransport.FailureTimeout), outcome: accounthealth.ProbeOutcomeUpstreamFailure},
		{name: "attempt connection", mode: ModeChatJSON, result: attemptedResult(), err: transportFailure(upstreamtransport.FailureConnection), outcome: accounthealth.ProbeOutcomeUpstreamFailure},
		{name: "attempt read", mode: ModeChatJSON, result: attemptedResult(), err: transportFailure(upstreamtransport.FailureRead), outcome: accounthealth.ProbeOutcomeUpstreamFailure},
		{name: "canceled", mode: ModeChatJSON, result: attemptedResult(), err: transportFailure(upstreamtransport.FailureCanceled), outcome: accounthealth.ProbeOutcomeTaskFailure},
		{name: "not attempted", mode: ModeChatJSON, err: transportFailure(upstreamtransport.FailureInvalidRequest), outcome: accounthealth.ProbeOutcomeTaskFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyExecution(test.mode, test.result, test.err); got != test.outcome {
				t.Fatalf("ClassifyExecution() = %q, want %q", got, test.outcome)
			}
		})
	}
}

func TestExecutorBuildsRequestAndUsesFinalFence(t *testing.T) {
	transport := &attemptTransportStub{result: completeResult(http.StatusOK, []byte(`{"choices":[{"finish_reason":"stop"}]}`))}
	fenceCalls := 0
	attempt := APIKeyAttempt{method: http.MethodPost, url: "https://api.example.test/v1/chat/completions", header: http.Header{"Authorization": {"Bearer secret"}}, body: []byte(`{"model":"model"}`)}
	outcome, _, err := (Executor{Transport: transport, Fence: func(context.Context) error { fenceCalls++; return nil }}).Execute(t.Context(), ModeChatJSON, attempt)
	if err != nil || outcome != accounthealth.ProbeOutcomeCompleteSuccess || fenceCalls != 1 {
		t.Fatalf("outcome=%q fenceCalls=%d error=%v", outcome, fenceCalls, err)
	}
	if transport.request == nil || transport.request.Header.Get("Authorization") != "Bearer secret" || transport.request.ContentLength != int64(len(attempt.body)) {
		t.Fatalf("request=%+v", transport.request)
	}
}

func TestExecutorRunsTransportNeutralHTTPAttempt(t *testing.T) {
	transport := &attemptTransportStub{result: completeResult(http.StatusOK, []byte(`{"object":"response","status":"completed","output":[]}`))}
	attempt := genericHTTPAttempt{
		method: http.MethodPost,
		url:    "https://example.test/v1/responses",
		header: http.Header{"Authorization": {"Bearer private"}},
		body:   []byte(`{"model":"gpt-test"}`),
	}
	outcome, _, err := (Executor{Transport: transport}).ExecuteAttempt(t.Context(), ModeResponsesJSON, attempt)
	if err != nil || outcome != accounthealth.ProbeOutcomeCompleteSuccess {
		t.Fatalf("ExecuteAttempt() = (%q, %v), want complete_success", outcome, err)
	}
	if transport.request == nil || transport.request.URL.String() != attempt.url || transport.request.Header.Get("Authorization") != "Bearer private" {
		t.Fatalf("transport request = %+v", transport.request)
	}
}

func completeResult(status int, body []byte) upstreamtransport.Result {
	return upstreamtransport.Result{AttemptURL: "https://api.example.test/v1", Attempted: true, ResponseObserved: true, FramingComplete: true, StatusCode: status, Body: body}
}

func attemptedResult() upstreamtransport.Result {
	return upstreamtransport.Result{AttemptURL: "https://api.example.test/v1", Attempted: true}
}

func transportFailure(kind upstreamtransport.FailureKind) error {
	return &upstreamtransport.Failure{Kind: kind}
}

type attemptTransportStub struct {
	request *http.Request
	result  upstreamtransport.Result
	err     error
}

type genericHTTPAttempt struct {
	method string
	url    string
	header http.Header
	body   []byte
}

func (a genericHTTPAttempt) Method() string      { return a.method }
func (a genericHTTPAttempt) URL() string         { return a.url }
func (a genericHTTPAttempt) Header() http.Header { return a.header.Clone() }
func (a genericHTTPAttempt) Body() []byte        { return append([]byte(nil), a.body...) }

func (s *attemptTransportStub) ExecuteWithFence(ctx context.Context, request *http.Request, fence func(context.Context) error) (upstreamtransport.Result, error) {
	s.request = request
	if fence != nil {
		if err := fence(ctx); err != nil {
			return upstreamtransport.Result{}, err
		}
	}
	return s.result, s.err
}

var _ AttemptTransport = (*attemptTransportStub)(nil)
