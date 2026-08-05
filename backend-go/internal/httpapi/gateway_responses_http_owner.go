package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"juhe-ai/backend-go/internal/modules/gatewayhttpcompletion"
	"juhe-ai/backend-go/internal/modules/gatewayresponseterminal"
)

var (
	ErrGatewayResponsesOwnerStagedIngressRequired    = errors.New("gateway responses HTTP owner staged ingress is required")
	ErrGatewayResponsesOwnerExecutorRequired         = errors.New("gateway responses HTTP owner executor is required")
	ErrGatewayResponsesOwnerErrorHandlerRequired     = errors.New("gateway responses HTTP owner error handler is required")
	ErrGatewayResponsesOwnerPreflightRejected        = errors.New("gateway responses HTTP owner preflight rejected before body ownership")
	ErrGatewayResponsesOwnerAdmissionRejected        = errors.New("gateway responses HTTP owner pre-body admission rejected before body ownership")
	ErrGatewayResponsesOwnerResultIncomplete         = errors.New("gateway responses HTTP owner staged result is incomplete")
	ErrGatewayResponsesOwnerExecutionHandoffRequired = errors.New("gateway responses HTTP owner execution terminal handoff is required")
)

// GatewayResponsesOwnerExecutionInput is the request-local input supplied to
// the injected dispatch/response owner. The callback may use gatewaydispatch,
// gatewayresponse and a real HTTPWriterSink, but this composition layer never
// chooses an upstream, writes an error payload, or registers a route.
type GatewayResponsesOwnerExecutionInput struct {
	Request  *http.Request
	Writer   http.ResponseWriter
	Staged   GatewayResponsesStagedIngressResult
	Terminal *gatewayhttpcompletion.Observer
}

// GatewayResponsesOwnerExecution is the already-dispatched response handoff.
// TerminalHandoff carries the opaque attempt lifecycle and downstream commit
// facts required by the response terminal adapter. This seam records those
// facts but cannot claim that an HTTP response has actually finished.
type GatewayResponsesOwnerExecution struct {
	TerminalHandoff          *gatewayresponseterminal.Handoff
	Disposition              gatewayresponseterminal.Disposition
	WriterAction             gatewayresponseterminal.WriterAction
	ProtocolValidatedSuccess bool
}

// GatewayResponsesOwnerExecutor owns dispatch and response writing after the
// staged ingress has completed. It must return explicit response disposition
// and writer facts; they are never inferred from HTTP status or callback return.
type GatewayResponsesOwnerExecutor func(context.Context, GatewayResponsesOwnerExecutionInput) (GatewayResponsesOwnerExecution, error)

// GatewayResponsesHTTPOwnerOptions configures an unregistered composition
// seam. OnError owns preparation, execution, and response-terminal recording
// errors. A real response owner must separately own finish evidence, detached
// finalization, side effects, and retained-lease release.
type GatewayResponsesHTTPOwnerOptions struct {
	Staged  *GatewayResponsesStagedIngress
	Execute GatewayResponsesOwnerExecutor
	OnError GatewayResponsesStagedHandler
}

// GatewayResponsesHTTPOwner is deliberately not an http.Handler. Handler
// returns a per-process unregistered handler only when a caller explicitly
// asks for it; listener and route ownership stay outside this package.
type GatewayResponsesHTTPOwner struct {
	staged  *GatewayResponsesStagedIngress
	execute GatewayResponsesOwnerExecutor
	onError GatewayResponsesStagedHandler
}

func NewGatewayResponsesHTTPOwner(options GatewayResponsesHTTPOwnerOptions) (*GatewayResponsesHTTPOwner, error) {
	if options.Staged == nil {
		return nil, ErrGatewayResponsesOwnerStagedIngressRequired
	}
	if options.Execute == nil {
		return nil, ErrGatewayResponsesOwnerExecutorRequired
	}
	if options.OnError == nil {
		return nil, ErrGatewayResponsesOwnerErrorHandlerRequired
	}
	return &GatewayResponsesHTTPOwner{
		staged: options.Staged, execute: options.Execute, onError: options.OnError,
	}, nil
}

// Handler composes staged ingress with the injected owner without registering
// a listener or route. A handler return is not treated as response_finished;
// the caller must retain the returned terminal/adapter facts and only complete
// them from a real response owner with finish evidence.
func (o *GatewayResponsesHTTPOwner) Handler() (http.Handler, error) {
	if o == nil || o.staged == nil {
		return nil, ErrGatewayResponsesOwnerStagedIngressRequired
	}
	return o.staged.NewGatewayResponsesStagedHTTPHandler(o.handle)
}

func (o *GatewayResponsesHTTPOwner) handle(
	writer http.ResponseWriter,
	request *http.Request,
	staged GatewayResponsesStagedIngressResult,
	prepareErr error,
	terminal *gatewayhttpcompletion.Observer,
) {
	if prepareErr != nil {
		o.report(writer, request, staged, prepareErr, terminal)
		return
	}
	if err := validateGatewayResponsesOwnerStagedResult(staged); err != nil {
		o.report(writer, request, staged, err, terminal)
		return
	}
	ctx := context.Background()
	if request != nil {
		ctx = request.Context()
	}
	execution, err := o.execute(ctx, GatewayResponsesOwnerExecutionInput{
		Request: request, Writer: writer, Staged: staged, Terminal: terminal,
	})
	if err != nil {
		terminal.CompleteClientCanceledIfContextDone()
		o.report(writer, request, staged, fmt.Errorf("execute gateway responses owner: %w", err), terminal)
		return
	}
	if execution.TerminalHandoff == nil {
		o.report(writer, request, staged, ErrGatewayResponsesOwnerExecutionHandoffRequired, terminal)
		return
	}
	adapter, err := gatewayresponseterminal.NewFromHandoff(terminal, execution.TerminalHandoff)
	if err != nil {
		o.report(writer, request, staged, fmt.Errorf("create gateway response terminal adapter: %w", err), terminal)
		return
	}
	if err := adapter.RecordHandoff(execution.Disposition, execution.WriterAction, execution.ProtocolValidatedSuccess); err != nil {
		o.report(writer, request, staged, fmt.Errorf("record gateway response terminal handoff: %w", err), terminal)
		return
	}
}

func (o *GatewayResponsesHTTPOwner) report(
	writer http.ResponseWriter,
	request *http.Request,
	staged GatewayResponsesStagedIngressResult,
	err error,
	terminal *gatewayhttpcompletion.Observer,
) {
	if o != nil && o.onError != nil {
		o.onError(writer, request, staged, err, terminal)
	}
}

func validateGatewayResponsesOwnerStagedResult(result GatewayResponsesStagedIngressResult) error {
	if !result.Preflight.Decision().Allowed() {
		return ErrGatewayResponsesOwnerPreflightRejected
	}
	if result.PreBody != nil && result.PreBody.Admission != nil && !result.PreBody.Admission.Acquired() {
		return ErrGatewayResponsesOwnerAdmissionRejected
	}
	if result.Orchestration == nil || result.Orchestration.Route == nil || result.Orchestration.Ingress == nil ||
		result.Orchestration.Ingress.Finalization == nil || result.Orchestration.Ingress.Admission == nil {
		return ErrGatewayResponsesOwnerResultIncomplete
	}
	return nil
}
