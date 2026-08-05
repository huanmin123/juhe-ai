package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaybodyadmission"
	"juhe-ai/backend-go/internal/modules/gatewaydispatch"
	"juhe-ai/backend-go/internal/modules/gatewaydownstream"
	"juhe-ai/backend-go/internal/modules/gatewayhttpcompletion"
	"juhe-ai/backend-go/internal/modules/gatewayprebodyadmission"
	"juhe-ai/backend-go/internal/modules/gatewayresponse"
	"juhe-ai/backend-go/internal/modules/gatewayresponseterminal"
	"juhe-ai/backend-go/internal/modules/gatewayroutecoordination"
	"juhe-ai/backend-go/internal/modules/gatewayrouteplan"
	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
	"juhe-ai/backend-go/internal/modules/gatewayupstream"
	"juhe-ai/backend-go/internal/protocols/codexresponses"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
	"juhe-ai/backend-go/internal/store/port"
)

func TestGatewayResponsesHTTPOwnerRecordsResponseTerminalWithoutCompletingIt(t *testing.T) {
	preflight := newGatewayResponsesTestPreflight(t)
	orchestrator := &gatewayResponsesOrchestratorStub{}
	staged := newGatewayResponsesStagedIngress(t, preflight, orchestrator)
	startedAt := time.Now().Add(-time.Second)
	client := &gatewayResponsesOwnerDoer{response: &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"response_1"}`)),
	}}
	attempt := &gatewayResponsesOwnerAttempt{}
	executorCalls := 0
	owner, err := NewGatewayResponsesHTTPOwner(GatewayResponsesHTTPOwnerOptions{
		Staged: staged,
		Execute: func(ctx context.Context, input GatewayResponsesOwnerExecutionInput) (GatewayResponsesOwnerExecution, error) {
			executorCalls++
			dispatcher := gatewaydispatch.Dispatcher{Client: client, Builder: gatewayupstream.Builder{}}
			credential, err := gatewayupstream.NewCredential("upstream-secret", gatewayupstream.CredentialOptions{})
			if err != nil {
				return GatewayResponsesOwnerExecution{}, err
			}
			dispatchResult, err := dispatcher.Dispatch(gatewayupstream.Input{
				Context: ctx, Request: protocolgateway.RequestShape{Method: http.MethodPost, Path: "/v1/responses"},
				Candidate: port.GatewayAccountCandidate{ProtocolCode: "openai", ProtocolVersion: "v1", Type: "api_key"},
				BaseURL:   "https://upstream.example.test", Credential: credential, Body: input.Staged.Inbound.RawBody(),
			})
			if err != nil {
				return GatewayResponsesOwnerExecution{}, err
			}
			sink, err := gatewaydownstream.NewHTTPWriterSink(input.Writer)
			if err != nil {
				return GatewayResponsesOwnerExecution{}, err
			}
			response, responseErr := (gatewayresponse.Handler{Dispatcher: dispatcher}).Handle(gatewayresponse.Input{
				Context: ctx, Dispatch: dispatchResult, Transport: gatewayresponse.TransportJSON,
				Sink: sink, StartedAt: startedAt,
			})
			if responseErr != nil {
				return GatewayResponsesOwnerExecution{}, responseErr
			}
			terminalHandoff, err := gatewayresponseterminal.NewHandoff(attempt, response, sink.Snapshot())
			if err != nil {
				return GatewayResponsesOwnerExecution{}, err
			}
			return GatewayResponsesOwnerExecution{
				TerminalHandoff: terminalHandoff, Disposition: gatewayresponseterminal.DispositionProtocolValidatedSuccess,
				WriterAction: gatewayresponseterminal.WriterActionProtocolSuccess, ProtocolValidatedSuccess: true,
			}, nil
		},
		OnError: func(_ http.ResponseWriter, _ *http.Request, _ GatewayResponsesStagedIngressResult, err error, _ *gatewayhttpcompletion.Observer) {
			t.Fatalf("owner error: %v", err)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := owner.Handler()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6"}`))
	request.Header.Set("Authorization", "Bearer sk-owner")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if executorCalls != 1 || client.calls != 1 || response.Code != http.StatusOK || response.Body.String() != `{"id":"response_1"}` {
		t.Fatalf("calls=%d client=%d status=%d body=%q", executorCalls, client.calls, response.Code, response.Body.String())
	}
	if attempt.finishSuccess != 0 || attempt.observeSink != 1 || attempt.cancelClient != 0 {
		t.Fatalf("attempt=%#v", attempt)
	}
}

func TestGatewayResponsesHTTPOwnerPreflightRejectionDoesNotReadBodyOrExecute(t *testing.T) {
	preflight := &gatewayResponsesPreflightStub{}
	orchestrator := &gatewayResponsesOrchestratorStub{}
	staged := newGatewayResponsesStagedIngress(t, preflight, orchestrator)
	body := &gatewayResponsesCountingBody{Reader: strings.NewReader(`{"model":"gpt"}`)}
	var observed error
	executorCalls := 0
	owner, err := NewGatewayResponsesHTTPOwner(GatewayResponsesHTTPOwnerOptions{
		Staged: staged,
		Execute: func(context.Context, GatewayResponsesOwnerExecutionInput) (GatewayResponsesOwnerExecution, error) {
			executorCalls++
			return GatewayResponsesOwnerExecution{}, nil
		},
		OnError: func(_ http.ResponseWriter, _ *http.Request, _ GatewayResponsesStagedIngressResult, err error, _ *gatewayhttpcompletion.Observer) {
			observed = err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := owner.Handler()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Body = body
	request.Header.Set("X-API-Key", "sk-denied")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if !errors.Is(observed, ErrGatewayResponsesOwnerPreflightRejected) || executorCalls != 0 || body.reads != 0 || body.closes != 0 {
		t.Fatalf("observed=%v executor=%d reads=%d closes=%d", observed, executorCalls, body.reads, body.closes)
	}
}

func TestGatewayResponsesHTTPOwnerClientCancellationWinsBeforeResponseCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	preflight := newGatewayResponsesTestPreflight(t)
	staged := newGatewayResponsesStagedIngress(t, preflight, &gatewayResponsesOrchestratorStub{})
	attempt := &gatewayResponsesOwnerAttempt{}
	var observed error
	owner, err := NewGatewayResponsesHTTPOwner(GatewayResponsesHTTPOwnerOptions{
		Staged: staged,
		Execute: func(_ context.Context, input GatewayResponsesOwnerExecutionInput) (GatewayResponsesOwnerExecution, error) {
			cancel()
			return GatewayResponsesOwnerExecution{
				TerminalHandoff: mustGatewayResponsesOwnerHandoff(t, attempt),
				Disposition:     gatewayresponseterminal.DispositionProtocolValidatedSuccess, WriterAction: gatewayresponseterminal.WriterActionProtocolSuccess, ProtocolValidatedSuccess: true,
			}, nil
		},
		OnError: func(_ http.ResponseWriter, _ *http.Request, _ GatewayResponsesStagedIngressResult, err error, _ *gatewayhttpcompletion.Observer) {
			observed = err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := owner.Handler()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt"}`)).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer sk-cancel")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if observed == nil || attempt.cancelClient != 1 || !errors.Is(observed, gatewayresponseterminal.ErrClientCanceled) {
		t.Fatalf("observed=%v attempt=%#v", observed, attempt)
	}
}

func TestGatewayResponsesHTTPOwnerKeepsPreBodyLeaseAfterHandlerReturns(t *testing.T) {
	preflightService := newGatewayResponsesTestPreflight(t)
	preflight := mustGatewayResponsesPreflight(t, preflightService)
	controller := gatewaybodyadmission.NewController()
	admissionInput := gatewayResponsesAdmissionInput()
	admissionInput.MaxQueueWait = 0
	decision, err := controller.Acquire(context.Background(), admissionInput)
	if err != nil || !decision.Acquired() {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
	defer decision.Lease.Release()
	preBody := &gatewayResponsesPreBodyStub{result: gatewayprebodyadmission.Result{
		Preflight: preflight,
		Route:     &gatewayrouteplan.RouteOnlyResult{Preflight: preflight, Plan: &gatewayroutecoordination.Plan{}},
		Admission: &decision,
	}}
	orchestrator := &gatewayResponsesOrchestratorStub{}
	staged := newGatewayResponsesStagedIngressWithOptions(t, GatewayResponsesStagedIngressOptions{
		Preflight: preflightService, Orchestrate: orchestrator, PreBody: preBody,
	})
	attempt := &gatewayResponsesOwnerAttempt{}
	var blocked gatewaybodyadmission.Decision
	owner, err := NewGatewayResponsesHTTPOwner(GatewayResponsesHTTPOwnerOptions{
		Staged: staged,
		Execute: func(_ context.Context, input GatewayResponsesOwnerExecutionInput) (GatewayResponsesOwnerExecution, error) {
			blocked, err = controller.Acquire(context.Background(), admissionInput)
			if err != nil {
				return GatewayResponsesOwnerExecution{}, err
			}
			if err := writeOwnerSuccessResponse(input.Writer); err != nil {
				return GatewayResponsesOwnerExecution{}, err
			}
			return ownerSuccessExecution(t, attempt), nil
		},
		OnError: func(_ http.ResponseWriter, _ *http.Request, _ GatewayResponsesStagedIngressResult, err error, _ *gatewayhttpcompletion.Observer) {
			t.Fatalf("owner error: %v", err)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := owner.Handler()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt"}`))
	request.Header.Set("Authorization", "Bearer sk-owner-lease")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if blocked.Reason != gatewaybodyadmission.RejectQueueDisabled {
		t.Fatalf("lease was released before terminal: blocked=%#v", blocked)
	}
	acquired, err := controller.Acquire(context.Background(), admissionInput)
	if err != nil || acquired.Acquired() || acquired.Reason != gatewaybodyadmission.RejectQueueDisabled {
		t.Fatalf("handler return released pre-body lease: acquired=%#v err=%v", acquired, err)
	}
}

type gatewayResponsesOwnerDoer struct {
	response *http.Response
	calls    int
}

func (d *gatewayResponsesOwnerDoer) Do(request *http.Request) (*http.Response, error) {
	d.calls++
	return d.response, nil
}

type gatewayResponsesOwnerAttempt struct {
	observeSink   int
	finishSuccess int
	cancelClient  int
}

func (a *gatewayResponsesOwnerAttempt) ObserveSink(gatewaystreamrelay.SinkState) error {
	a.observeSink++
	return nil
}
func (a *gatewayResponsesOwnerAttempt) RetryPreCommit() error      { return nil }
func (a *gatewayResponsesOwnerAttempt) FinishSuccess() error       { a.finishSuccess++; return nil }
func (a *gatewayResponsesOwnerAttempt) FinishFailure(string) error { return nil }
func (a *gatewayResponsesOwnerAttempt) CancelClient() error        { a.cancelClient++; return nil }

func mustGatewayResponsesOwnerHandoff(t *testing.T, attempt *gatewayResponsesOwnerAttempt) *gatewayresponseterminal.Handoff {
	t.Helper()
	handoff, err := gatewayresponseterminal.NewHandoff(attempt, gatewayresponse.Result{
		State: gatewayresponse.StateSucceeded, TransportCommitted: true, SemanticCommitted: true,
		BytesWritten: 1, Handoff: gatewayresponse.Handoff{Commit: codexresponses.CommitState{TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 1}},
	}, gatewaystreamrelay.SinkState{TransportCommitted: true, SemanticCommitted: true, DownstreamBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	return handoff
}

func ownerSuccessExecution(t *testing.T, attempt *gatewayResponsesOwnerAttempt) GatewayResponsesOwnerExecution {
	t.Helper()
	status := http.StatusNoContent
	response := gatewayresponse.Result{
		State: gatewayresponse.StateSucceeded, StatusCode: status, TransportCommitted: true, SemanticCommitted: true,
		Handoff: gatewayresponse.Handoff{Commit: codexresponses.CommitState{TransportCommitted: true, SemanticCommitted: true}},
	}
	handoff, err := gatewayresponseterminal.NewHandoff(attempt, response, gatewaystreamrelay.SinkState{TransportCommitted: true, SemanticCommitted: true})
	if err != nil {
		t.Fatal(err)
	}
	return GatewayResponsesOwnerExecution{
		TerminalHandoff: handoff, Disposition: gatewayresponseterminal.DispositionProtocolValidatedSuccess,
		WriterAction: gatewayresponseterminal.WriterActionProtocolSuccess, ProtocolValidatedSuccess: true,
	}
}

func writeOwnerSuccessResponse(writer http.ResponseWriter) error {
	if writer == nil {
		return errors.New("owner response writer is required")
	}
	writer.WriteHeader(http.StatusNoContent)
	return nil
}
