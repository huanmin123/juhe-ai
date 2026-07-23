package gatewayattemptloop

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewaydispatch"
	"juhe-ai/backend-go/internal/modules/gatewayresponse"
	"juhe-ai/backend-go/internal/modules/gatewayretry"
	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	"juhe-ai/backend-go/internal/modules/gatewayupstream"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
	"juhe-ai/backend-go/internal/store/port"
)

func TestHTTPExecutorComposesDispatchAndJSONResponse(t *testing.T) {
	client := doerStub{response: &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"response_1"}`))}}
	dispatcher := gatewaydispatch.Dispatcher{Client: client}
	credential, err := gatewayupstream.NewCredential("sk-test", gatewayupstream.CredentialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	written := ""
	executor := HTTPExecutor{
		Dispatcher: dispatcher,
		Handler:    gatewayresponse.Handler{Dispatcher: dispatcher},
		Prepare: func(_ context.Context, attempt Attempt) (gatewayupstream.Input, gatewayresponse.Input, error) {
			return gatewayupstream.Input{
					Request:   protocolgateway.RequestShape{Method: http.MethodGet, Path: "/v1/models"},
					Candidate: attempt.Candidate.Projection, BaseURL: "https://upstream.example.com", Credential: credential,
				}, gatewayresponse.Input{
					Transport: gatewayresponse.TransportJSON, StartedAt: time.Now(),
					Sink: gatewaystreamrelay.SinkFunc(func(_ context.Context, body []byte) (int, error) {
						written += string(body)
						return len(body), nil
					}),
					ResponseDisposition: gatewayretry.ResponseDispositionCompleteTransparent,
				}, nil
		},
	}
	result, err := executor.Execute(context.Background(), Attempt{Candidate: gatewaycandidate("a")})
	if err != nil || !result.Success || !result.Committed || written != `{"id":"response_1"}` {
		t.Fatalf("result = %+v err=%v written=%q", result, err, written)
	}
}

func TestHTTPExecutorReturnsRetryableTransportFailure(t *testing.T) {
	executor := HTTPExecutor{Prepare: func(context.Context, Attempt) (gatewayupstream.Input, gatewayresponse.Input, error) {
		return gatewayupstream.Input{}, gatewayresponse.Input{}, nil
	}}
	result, err := executor.Execute(context.Background(), Attempt{})
	if err == nil || !result.RetryAllowed || result.Committed || result.Failure.Message == "" {
		t.Fatalf("result = %+v err=%v", result, err)
	}
}

func TestHTTPExecutorTransparentForUnmatchedUpstreamFailure(t *testing.T) {
	client := doerStub{response: &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"maintenance"}`))}}
	dispatcher := gatewaydispatch.Dispatcher{Client: client}
	credential, _ := gatewayupstream.NewCredential("sk-test", gatewayupstream.CredentialOptions{})
	written := ""
	executor := HTTPExecutor{
		Dispatcher: dispatcher, Handler: gatewayresponse.Handler{Dispatcher: dispatcher},
		Prepare: func(_ context.Context, attempt Attempt) (gatewayupstream.Input, gatewayresponse.Input, error) {
			return gatewayupstream.Input{Request: protocolgateway.RequestShape{Method: http.MethodGet, Path: "/v1/models"}, Candidate: attempt.Candidate.Projection, BaseURL: "https://upstream.example.com", Credential: credential}, gatewayresponse.Input{
				Transport: gatewayresponse.TransportJSON, Sink: gatewaystreamrelay.SinkFunc(func(_ context.Context, body []byte) (int, error) { written = string(body); return len(body), nil }),
			}, nil
		},
	}
	result, err := executor.Execute(context.Background(), Attempt{Candidate: gatewaycandidate("a")})
	if err != nil || result.RetryAllowed || !result.Committed || written != `{"error":"maintenance"}` {
		t.Fatalf("result = %+v err=%v written=%q", result, err, written)
	}
}

func TestHTTPExecutorUsesExplicitPolicyOnlyWhenRuleMatches(t *testing.T) {
	client := doerStub{response: &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"code":"rate_limit","message":"busy"}}`))}}
	dispatcher := gatewaydispatch.Dispatcher{Client: client}
	credential, _ := gatewayupstream.NewCredential("sk-test", gatewayupstream.CredentialOptions{})
	candidate := gatewaycandidate("a")
	candidate.Credentials = gatewaycandidatewindow.NewCredentialSet(map[string]any{"error_handling_rules": []any{rule(map[string]any{"action": "retry_next"})}})
	executor := HTTPExecutor{
		Dispatcher: dispatcher, Handler: gatewayresponse.Handler{Dispatcher: dispatcher},
		Prepare: func(_ context.Context, attempt Attempt) (gatewayupstream.Input, gatewayresponse.Input, error) {
			return gatewayupstream.Input{Request: protocolgateway.RequestShape{Method: http.MethodGet, Path: "/v1/models"}, Candidate: attempt.Candidate.Projection, BaseURL: "https://upstream.example.com", Credential: credential}, gatewayresponse.Input{Transport: gatewayresponse.TransportJSON, Sink: gatewaystreamrelay.SinkFunc(func(_ context.Context, body []byte) (int, error) { return len(body), nil }), ResponseDisposition: gatewayretry.ResponseDispositionCompleteTransparent}, nil
		},
	}
	result, err := executor.Execute(context.Background(), Attempt{Candidate: candidate})
	if err == nil || !result.RetryAllowed || result.Committed || result.Failure.ErrorCode != "rate_limit" {
		t.Fatalf("result = %+v err=%v", result, err)
	}
}

func TestHTTPExecutorMarksCredentialFailureAsKeyScopedWhenAlternativeExists(t *testing.T) {
	client := doerStub{response: &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_api_key","message":"bad key"}}`))}}
	dispatcher := gatewaydispatch.Dispatcher{Client: client}
	credential, _ := gatewayupstream.NewCredential("sk-test", gatewayupstream.CredentialOptions{})
	candidate := gatewaycandidate("a")
	candidate.Credentials = gatewaycandidatewindow.NewCredentialSet(map[string]any{"error_handling_rules": []any{rule(map[string]any{"status_codes": []any{float64(401)}, "error_codes": []any{"invalid_api_key"}})}})
	executor := HTTPExecutor{
		Dispatcher: dispatcher, Handler: gatewayresponse.Handler{Dispatcher: dispatcher},
		Prepare: func(_ context.Context, attempt Attempt) (gatewayupstream.Input, gatewayresponse.Input, error) {
			return gatewayupstream.Input{Request: protocolgateway.RequestShape{Method: http.MethodGet, Path: "/v1/models"}, Candidate: attempt.Candidate.Projection, BaseURL: "https://upstream.example.com", Credential: credential}, gatewayresponse.Input{Transport: gatewayresponse.TransportJSON, Sink: gatewaystreamrelay.SinkFunc(func(_ context.Context, body []byte) (int, error) { return len(body), nil })}, nil
		},
	}
	result, err := executor.Execute(context.Background(), Attempt{Candidate: candidate, HasAlternativeKeys: true})
	if err == nil || !result.RetryAllowed || !result.KeyScopedFailure || result.Committed {
		t.Fatalf("result = %+v err=%v", result, err)
	}
}

func TestExtractErrorFactsSupportsNestedProtocolPayload(t *testing.T) {
	code, errorType, message := extractErrorFacts(`{"type":"error","error":{"type":"overloaded_error","code":"rate_limit","message":"busy"}}`)
	if code != "rate_limit" || errorType != "overloaded_error" || message != "busy" {
		t.Fatalf("facts = %q/%q/%q", code, errorType, message)
	}
	code, errorType, message = extractErrorFacts(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)
	if code != "overloaded_error" || errorType != "overloaded_error" || message != "busy" {
		t.Fatalf("type fallback facts = %q/%q/%q", code, errorType, message)
	}
	code, errorType, message = extractErrorFacts(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota"}}`)
	if code != "429" || errorType != "" || message != "quota" {
		t.Fatalf("gemini facts = %q/%q/%q", code, errorType, message)
	}
}

type doerStub struct{ response *http.Response }

func (s doerStub) Do(*http.Request) (*http.Response, error) { return s.response, nil }

func gatewaycandidate(id string) gatewaycandidatewindow.Candidate {
	return gatewaycandidatewindow.Candidate{Projection: port.GatewayAccountCandidate{AccountID: id, ProtocolCode: "openai", ProtocolVersion: "v1", Type: "api_key"}}
}
