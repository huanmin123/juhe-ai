// Package gatewayresponseterminal provides the unregistered W10 bridge from
// an already-observed response result to the request lifecycle terminal. It
// does not write HTTP bytes, infer a net/http finish event, or own audit/usage.
package gatewayresponseterminal

import (
	"errors"
	"fmt"
	"sync"

	"juhe-ai/backend-go/internal/modules/gatewayhttpcompletion"
	"juhe-ai/backend-go/internal/modules/gatewayresponse"
	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
)

var (
	ErrTerminalRequired                   = errors.New("gateway response terminal requires HTTP terminal observer")
	ErrAttemptLifecycleRequired           = errors.New("gateway response terminal requires active attempt lifecycle")
	ErrTerminalAlreadyObserved            = errors.New("gateway response terminal already observed downstream terminal")
	ErrClientCanceled                     = errors.New("gateway response terminal observed client cancellation")
	ErrSettlementAlreadyRecorded          = errors.New("gateway response terminal settlement already recorded")
	ErrResponseFinishedWithoutSettlement  = errors.New("gateway response terminal response finished without settlement")
	ErrResponseFinishedBeforeSinkObserved = errors.New("gateway response terminal response finished before sink observation")
	ErrInvalidDisposition                 = errors.New("gateway response terminal disposition is invalid")
	ErrInvalidWriterAction                = errors.New("gateway response terminal writer action is invalid")
	ErrInvalidDownstreamCommit            = errors.New("gateway response terminal downstream commit is invalid")
	ErrResponseCommitAheadOfDownstream    = errors.New("gateway response terminal response commit exceeds downstream observation")
	ErrProtocolValidationRequired         = errors.New("gateway response terminal success requires explicit protocol validation")
	ErrResponseStateDispositionMismatch   = errors.New("gateway response terminal response state does not match disposition")
	ErrRetryDispositionCommitted          = errors.New("gateway response terminal pre-commit retry has downstream commit")
	ErrTerminalLifecycleConflict          = errors.New("gateway response terminal lifecycle rejected observed terminal")
	ErrHandoffRequired                    = errors.New("gateway response terminal handoff is required")
)

// Disposition is supplied by the response owner. It is intentionally not
// inferred from status codes, handler return, or HTTP transport completion.
type Disposition string

const (
	DispositionProtocolValidatedSuccess Disposition = "protocol_validated_success"
	DispositionRetryPreCommit           Disposition = "retry_pre_commit"
	DispositionUpstreamFailure          Disposition = "upstream_failure"
	DispositionGatewayFailure           Disposition = "gateway_failure"
)

// WriterAction records the explicit action already taken by the real response
// owner. This adapter never writes a body or manufactures a protocol error.
type WriterAction string

const (
	WriterActionNone                     WriterAction = "none"
	WriterActionProtocolSuccess          WriterAction = "protocol_success"
	WriterActionForwardedUpstreamFailure WriterAction = "forwarded_upstream_failure"
	WriterActionControlledError          WriterAction = "controlled_error"
)

// AttemptLifecycle is the narrow terminal portion of the opaque attempt
// bridge. gatewayrequestlifecycle.AttemptLoopAdapter satisfies it without
// exposing a lifecycle generation, candidate, API key, or credential.
// Keeping this interface here lets gatewayattemptloop return a Handoff without
// creating an import cycle back to this package.
type AttemptLifecycle interface {
	ObserveSink(gatewaystreamrelay.SinkState) error
	RetryPreCommit() error
	FinishSuccess() error
	FinishFailure(string) error
	CancelClient() error
}

// Handoff is the immutable, bounded response fact set an opt-in attempt loop
// can return after a handler has produced a committed result. It is not proof
// of HTTP response completion: the listener owner still records disposition
// facts and calls CompleteResponse only after a real finish-equivalent event.
type Handoff struct {
	attempt    AttemptLifecycle
	response   gatewayresponse.Result
	downstream gatewaystreamrelay.SinkState
}

// NewHandoff accepts only the response facts relevant to lifecycle settlement.
// The copied result intentionally omits buffered bodies, stream payloads and
// audit/usage data so this owner seam cannot become an accidental payload or
// persistence channel.
func NewHandoff(attempt AttemptLifecycle, response gatewayresponse.Result, downstream gatewaystreamrelay.SinkState) (*Handoff, error) {
	if attempt == nil {
		return nil, ErrAttemptLifecycleRequired
	}
	return &Handoff{
		attempt: attempt,
		response: gatewayresponse.Result{
			State:              response.State,
			BytesWritten:       response.BytesWritten,
			TransportCommitted: response.TransportCommitted,
			SemanticCommitted:  response.SemanticCommitted,
			RetryAllowed:       response.RetryAllowed,
			Handoff: gatewayresponse.Handoff{
				Commit: response.Handoff.Commit,
			},
		},
		downstream: downstream,
	}, nil
}

// Input is one already-started request attempt. The terminal observer must be
// owned by the concrete HTTP listener. Attempt is the opaque bridge already
// supplied to gatewayattemptloop; this package never exposes a lifecycle
// generation, candidate, API key, or credential.
type Input struct {
	Terminal *gatewayhttpcompletion.Observer
	Attempt  AttemptLifecycle
}

// Observation combines the existing response-core result with the exact sink
// state seen by the response writer. ProtocolValidatedSuccess is required even
// when gatewayresponse returned StateSucceeded so a future listener cannot
// turn a 2xx transport outcome into a semantic protocol success by accident.
type Observation struct {
	Response                 gatewayresponse.Result
	Downstream               gatewaystreamrelay.SinkState
	Disposition              Disposition
	WriterAction             WriterAction
	ProtocolValidatedSuccess bool
}

// Adapter waits for the observer's first terminal. Record stores verified
// response facts, then the response owner calls CompleteResponse only after
// authoritative response-completion evidence. A handler return is insufficient.
type Adapter struct {
	terminal *gatewayhttpcompletion.Observer
	attempt  AttemptLifecycle
	handoff  *Handoff

	mu                 sync.Mutex
	observation        *Observation
	observationApplied bool
	closed             bool
	terminalErr        error
	unsubscribe        func()
}

func New(input Input) (*Adapter, error) {
	if input.Terminal == nil {
		return nil, ErrTerminalRequired
	}
	if input.Attempt == nil {
		return nil, ErrAttemptLifecycleRequired
	}
	if terminal, completed := input.Terminal.Terminal(); completed {
		if terminal.Reason == gatewayhttpcompletion.TerminalClientCanceled {
			if err := input.Attempt.CancelClient(); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrTerminalLifecycleConflict, err)
			}
			return nil, ErrClientCanceled
		}
		return nil, ErrTerminalAlreadyObserved
	}

	adapter := &Adapter{terminal: input.Terminal, attempt: input.Attempt}
	adapter.unsubscribe = input.Terminal.OnTerminal(adapter.onTerminal)
	return adapter, nil
}

// NewFromHandoff creates the response terminal adapter for an opt-in deferred
// attempt-loop result. The returned adapter remains inert until RecordHandoff
// verifies caller-owned response disposition and writer facts.
func NewFromHandoff(terminal *gatewayhttpcompletion.Observer, handoff *Handoff) (*Adapter, error) {
	if handoff == nil {
		return nil, ErrHandoffRequired
	}
	adapter, err := New(Input{Terminal: terminal, Attempt: handoff.attempt})
	if err != nil {
		return nil, err
	}
	adapter.handoff = handoff
	return adapter, nil
}

// Record validates and records an already-observed response result, but does
// not complete the HTTP terminal. The caller must retain the Adapter and call
// CompleteResponse after it has actual finish-equivalent evidence. A
// pre-commit retry remains inside the same request and never reaches terminal.
func (a *Adapter) Record(observation Observation) error {
	if a == nil || a.terminal == nil || a.attempt == nil {
		return ErrAttemptLifecycleRequired
	}
	if err := validateObservation(observation); err != nil {
		return err
	}
	if err := a.observeExistingTerminal(); err != nil {
		return err
	}

	copy := observation
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return a.closedError()
	}
	if a.observation != nil {
		a.mu.Unlock()
		return ErrSettlementAlreadyRecorded
	}
	a.observation = &copy
	a.mu.Unlock()

	if err := a.attempt.ObserveSink(observation.Downstream); err != nil {
		a.recordTerminalError(fmt.Errorf("observe response sink: %w", err))
		return a.Err()
	}

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return a.closedError()
	}
	a.observationApplied = true
	a.mu.Unlock()

	if observation.Disposition == DispositionRetryPreCommit {
		if err := a.attempt.RetryPreCommit(); err != nil {
			a.recordTerminalError(fmt.Errorf("retry response before commit: %w", err))
			return a.Err()
		}
		a.closeAfterRetry()
		return nil
	}
	return nil
}

// RecordHandoff records the immutable response/sink facts from the deferred
// attempt-loop handoff. Disposition, writer action and protocol validation
// remain explicit caller-owned facts and are never inferred by this package.
func (a *Adapter) RecordHandoff(disposition Disposition, writerAction WriterAction, protocolValidatedSuccess bool) error {
	if a == nil || a.handoff == nil {
		return ErrHandoffRequired
	}
	return a.Record(Observation{
		Response:                 a.handoff.response,
		Downstream:               a.handoff.downstream,
		Disposition:              disposition,
		WriterAction:             writerAction,
		ProtocolValidatedSuccess: protocolValidatedSuccess,
	})
}

// CompleteResponse records response_finished only after the caller has actual
// downstream completion evidence. The observer still makes a concurrent client
// cancellation win over a late response completion.
func (a *Adapter) CompleteResponse() error {
	if a == nil || a.terminal == nil || a.attempt == nil {
		return ErrAttemptLifecycleRequired
	}
	if err := a.observeExistingTerminal(); err != nil {
		return err
	}
	a.mu.Lock()
	recorded := a.observation != nil
	applied := a.observationApplied
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return a.closedError()
	}
	if !recorded {
		return ErrResponseFinishedWithoutSettlement
	}
	if !applied {
		return ErrResponseFinishedBeforeSinkObserved
	}
	a.terminal.Complete()
	if terminal, completed := a.terminal.Terminal(); completed && terminal.Reason == gatewayhttpcompletion.TerminalClientCanceled {
		return ErrClientCanceled
	}
	return a.Err()
}

// Err exposes a terminal callback failure that cannot be returned through the
// observer callback. A non-nil value means the caller supplied contradictory
// facts or another owner had already terminalized the lifecycle.
func (a *Adapter) Err() error {
	if a == nil {
		return ErrAttemptLifecycleRequired
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.terminalErr
}

func (a *Adapter) observeExistingTerminal() error {
	a.terminal.CompleteClientCanceledIfContextDone()
	terminal, completed := a.terminal.Terminal()
	if !completed {
		return nil
	}
	if terminal.Reason == gatewayhttpcompletion.TerminalClientCanceled {
		return ErrClientCanceled
	}
	return ErrTerminalAlreadyObserved
}

func (a *Adapter) onTerminal(terminal gatewayhttpcompletion.Terminal) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	observation := a.observation
	applied := a.observationApplied
	a.mu.Unlock()

	if terminal.Reason == gatewayhttpcompletion.TerminalClientCanceled {
		if err := a.attempt.CancelClient(); err != nil {
			a.recordTerminalError(fmt.Errorf("%w: cancel client lifecycle: %v", ErrTerminalLifecycleConflict, err))
		}
		return
	}
	if observation == nil {
		a.recordTerminalError(ErrResponseFinishedWithoutSettlement)
		return
	}
	if !applied {
		a.recordTerminalError(ErrResponseFinishedBeforeSinkObserved)
		return
	}

	var err error
	switch observation.Disposition {
	case DispositionProtocolValidatedSuccess:
		err = a.attempt.FinishSuccess()
	case DispositionUpstreamFailure:
		err = a.attempt.FinishFailure("upstream")
	case DispositionGatewayFailure:
		err = a.attempt.FinishFailure("gateway")
	default:
		err = ErrInvalidDisposition
	}
	if err != nil {
		a.recordTerminalError(fmt.Errorf("%w: %v", ErrTerminalLifecycleConflict, err))
	}
}

func (a *Adapter) closeAfterRetry() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	unsubscribe := a.unsubscribe
	a.unsubscribe = nil
	a.mu.Unlock()
	if unsubscribe != nil {
		unsubscribe()
	}
}

func (a *Adapter) closedError() error {
	if err := a.Err(); err != nil {
		return err
	}
	terminal, completed := a.terminal.Terminal()
	if completed && terminal.Reason == gatewayhttpcompletion.TerminalClientCanceled {
		return ErrClientCanceled
	}
	return ErrTerminalAlreadyObserved
}

func (a *Adapter) recordTerminalError(err error) {
	if err == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.terminalErr == nil {
		a.terminalErr = err
	}
}

func validateObservation(observation Observation) error {
	if observation.Downstream.DownstreamBytes < 0 ||
		(observation.Downstream.SemanticCommitted && !observation.Downstream.TransportCommitted) ||
		(observation.Downstream.DownstreamBytes > 0 && !observation.Downstream.TransportCommitted) {
		return ErrInvalidDownstreamCommit
	}
	if observation.Response.BytesWritten < 0 ||
		(observation.Response.TransportCommitted && !observation.Downstream.TransportCommitted) ||
		(observation.Response.SemanticCommitted && !observation.Downstream.SemanticCommitted) ||
		observation.Response.BytesWritten > observation.Downstream.DownstreamBytes ||
		observation.Response.Handoff.Commit.DownstreamBytes < 0 ||
		observation.Response.Handoff.Commit.DownstreamBytes > observation.Downstream.DownstreamBytes ||
		(observation.Response.Handoff.Commit.TransportCommitted && !observation.Downstream.TransportCommitted) ||
		(observation.Response.Handoff.Commit.SemanticCommitted && !observation.Downstream.SemanticCommitted) {
		return ErrResponseCommitAheadOfDownstream
	}

	switch observation.Disposition {
	case DispositionProtocolValidatedSuccess:
		if observation.Response.State != gatewayresponse.StateSucceeded || observation.Response.RetryAllowed || !observation.ProtocolValidatedSuccess {
			if !observation.ProtocolValidatedSuccess {
				return ErrProtocolValidationRequired
			}
			return ErrResponseStateDispositionMismatch
		}
		if observation.WriterAction != WriterActionProtocolSuccess {
			return ErrInvalidWriterAction
		}
		if !observation.Downstream.TransportCommitted || !observation.Downstream.SemanticCommitted {
			return ErrInvalidDownstreamCommit
		}
	case DispositionRetryPreCommit:
		if observation.Response.State != gatewayresponse.StateFailedBeforeCommit || !observation.Response.RetryAllowed || observation.ProtocolValidatedSuccess {
			return ErrResponseStateDispositionMismatch
		}
		if observation.WriterAction != WriterActionNone {
			return ErrInvalidWriterAction
		}
		if observation.Downstream.TransportCommitted || observation.Downstream.SemanticCommitted || observation.Downstream.DownstreamBytes != 0 {
			return ErrRetryDispositionCommitted
		}
	case DispositionUpstreamFailure:
		if observation.Response.State != gatewayresponse.StateUpstreamFailureForwarded || observation.Response.RetryAllowed || observation.ProtocolValidatedSuccess {
			return ErrResponseStateDispositionMismatch
		}
		if observation.WriterAction != WriterActionForwardedUpstreamFailure {
			return ErrInvalidWriterAction
		}
		if !observation.Downstream.TransportCommitted || !observation.Downstream.SemanticCommitted {
			return ErrInvalidDownstreamCommit
		}
	case DispositionGatewayFailure:
		if (observation.Response.State != gatewayresponse.StateFailedBeforeCommit && observation.Response.State != gatewayresponse.StateFailedAfterCommit) || observation.Response.RetryAllowed || observation.ProtocolValidatedSuccess {
			return ErrResponseStateDispositionMismatch
		}
		if observation.WriterAction != WriterActionControlledError {
			return ErrInvalidWriterAction
		}
		if !observation.Downstream.TransportCommitted {
			return ErrInvalidDownstreamCommit
		}
	default:
		return ErrInvalidDisposition
	}
	return nil
}
