// Package gatewayrequestfinalization composes the unregistered W10 response
// terminal with detached usage/audit facts. It has no HTTP writer, route,
// persistence, queue worker, or production-owner registration.
package gatewayrequestfinalization

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"juhe-ai/backend-go/internal/gatewayaudit"
	"juhe-ai/backend-go/internal/modules/gatewayhttpcompletion"
	"juhe-ai/backend-go/internal/modules/gatewayresponse"
	"juhe-ai/backend-go/internal/modules/gatewayusage"
)

var (
	ErrObserverRequired        = errors.New("gateway request finalization terminal observer is required")
	ErrCaptureRequired         = errors.New("gateway request finalization audit capture is required")
	ErrTerminalRequired        = errors.New("gateway request finalization requires an observed terminal")
	ErrResponseCompleterNeeded = errors.New("gateway request finalization response completer is required")
	ErrFinalizationInProgress  = errors.New("gateway request finalization is already in progress")
	ErrSideEffectQueueNeeded   = errors.New("gateway request finalization side-effect queue is required")
	ErrContextRequired         = errors.New("gateway request finalization side-effect context is required")
	ErrSideEffectsEnqueued     = errors.New("gateway request finalization side effects are already enqueued")
	ErrSideEffectEnqueueBusy   = errors.New("gateway request finalization side-effect enqueue is already in progress")
)

// Input contains only request facts and response facts already proved by
// other owners. The response handoff never carries body bytes or credentials.
type Input struct {
	Observer  *gatewayhttpcompletion.Observer
	Request   gatewayusage.RequestFacts
	Response  gatewayresponse.Handoff
	Capture   *gatewayaudit.Capture
	Completer ResponseCompleter

	// Release is optional for normal requests. A cross-group owner supplies it
	// for a retained target client-IP lease; terminal completion, including
	// client cancellation, invokes it exactly once.
	Release func()
}

// ResponseCompleter is implemented by the caller-owned response-terminal
// adapter. Its method must only return after authoritative downstream
// completion evidence; a handler return is not equivalent.
type ResponseCompleter interface {
	CompleteResponse() error
}

// Handoff is detached from the request and can be passed to a future queue or
// persistence owner after the HTTP response has completed. Enqueueing this
// value is deliberately outside this package's responsibility.
type Handoff struct {
	Terminal gatewayhttpcompletion.Terminal
	Usage    gatewayusage.FinalRecord
	Audit    gatewayaudit.Snapshot
}

// SideEffectQueue is the narrow future owner boundary. Implementations must
// enqueue usage/audit work without doing synchronous database or network I/O;
// this package only reports enqueue errors and never drops them.
type SideEffectQueue interface {
	Enqueue(context.Context, Handoff) error
}

// Finalizer is request-local and safe for terminal-release callbacks racing
// with the response owner. Finalize itself is intentionally explicit: a
// handler return cannot be mistaken for response_finished.
type Finalizer struct {
	input Input

	mu        sync.Mutex
	busy      bool
	finalized bool
	enqueued  bool
	enqueuing bool
	handoff   Handoff
	release   sync.Once
}

func New(input Input) (*Finalizer, error) {
	if input.Observer == nil {
		return nil, ErrObserverRequired
	}
	if input.Capture == nil {
		return nil, ErrCaptureRequired
	}
	finalizer := &Finalizer{input: input}
	input.Observer.OnTerminal(func(gatewayhttpcompletion.Terminal) {
		finalizer.releaseOnce()
	})
	return finalizer, nil
}

// Finalize requires the observer's first terminal and then creates exactly
// one detached usage/audit handoff. Client cancellation overrides any
// response success facts, matching Node's downstream-close attribution.
func (f *Finalizer) Finalize() (Handoff, error) {
	if f == nil {
		return Handoff{}, ErrObserverRequired
	}
	f.mu.Lock()
	if f.finalized {
		handoff := f.handoff
		f.mu.Unlock()
		return handoff, nil
	}
	if f.busy {
		f.mu.Unlock()
		return Handoff{}, ErrFinalizationInProgress
	}
	f.busy = true
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.busy = false
		f.mu.Unlock()
	}()

	terminal, completed := f.input.Observer.Terminal()
	if !completed {
		return Handoff{}, ErrTerminalRequired
	}
	usageFacts, auditFacts := terminalFacts(f.input.Response, terminal)
	usage, err := gatewayusage.Finalize(f.input.Request, usageFacts)
	if err != nil {
		return Handoff{}, fmt.Errorf("finalize gateway usage facts: %w", err)
	}
	if err := f.input.Capture.SetTerminal(auditFacts); err != nil {
		return Handoff{}, fmt.Errorf("finalize gateway audit capture: %w", err)
	}
	audit, err := f.input.Capture.TakeSnapshot()
	if err != nil {
		return Handoff{}, fmt.Errorf("take gateway audit snapshot: %w", err)
	}

	handoff := Handoff{Terminal: terminal, Usage: usage, Audit: audit}
	f.mu.Lock()
	f.handoff = handoff
	f.finalized = true
	f.mu.Unlock()
	return handoff, nil
}

// CompleteResponse asks the caller-owned response terminal to publish the
// real response_finished event, then finalizes detached usage/audit facts. It
// is intentionally unavailable when no completer was supplied, so this seam
// cannot infer completion from a normal function return.
func (f *Finalizer) CompleteResponse() (Handoff, error) {
	if f == nil || f.input.Completer == nil {
		return Handoff{}, ErrResponseCompleterNeeded
	}
	if err := f.input.Completer.CompleteResponse(); err != nil {
		return Handoff{}, fmt.Errorf("complete gateway response terminal: %w", err)
	}
	return f.Finalize()
}

// Enqueue hands detached facts to the future side-effect owner. It is safe to
// retry after an enqueue error; a successful enqueue is once-only.
func (f *Finalizer) Enqueue(ctx context.Context, queue SideEffectQueue) error {
	if queue == nil {
		return ErrSideEffectQueueNeeded
	}
	if ctx == nil {
		return ErrContextRequired
	}
	handoff, err := f.Finalize()
	if err != nil {
		return err
	}
	f.mu.Lock()
	if f.enqueued {
		f.mu.Unlock()
		return ErrSideEffectsEnqueued
	}
	if f.enqueuing {
		f.mu.Unlock()
		return ErrSideEffectEnqueueBusy
	}
	f.enqueuing = true
	f.mu.Unlock()
	if err := queue.Enqueue(ctx, handoff); err != nil {
		f.mu.Lock()
		f.enqueuing = false
		f.mu.Unlock()
		return fmt.Errorf("enqueue gateway side effects: %w", err)
	}
	f.mu.Lock()
	f.enqueuing = false
	f.enqueued = true
	f.mu.Unlock()
	return nil
}

// Handoff returns the already-finalized detached facts. It returns false
// before Finalize succeeds, so callers cannot enqueue an incomplete record.
func (f *Finalizer) Handoff() (Handoff, bool) {
	if f == nil {
		return Handoff{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.handoff, f.finalized
}

func (f *Finalizer) releaseOnce() {
	f.release.Do(func() {
		if f.input.Release != nil {
			f.input.Release()
		}
	})
}

func terminalFacts(response gatewayresponse.Handoff, terminal gatewayhttpcompletion.Terminal) (gatewayusage.TerminalFacts, gatewayaudit.TerminalInput) {
	usage := response.Usage
	audit := response.Audit
	usage.CompletedAt = terminal.CompletedAt
	if terminal.Reason == gatewayhttpcompletion.TerminalClientCanceled {
		usage.Outcome = gatewayusage.OutcomeFailed
		usage.FailureAttribution = gatewayusage.FailureAttributionDownstreamClosed
		usage.ErrorCode = "downstream_connection_closed"
		usage.ErrorMessage = "下游连接关闭"
		audit.Success = false
		audit.DownstreamClosed = true
		audit.RequestedOutcome = gatewayaudit.OutcomeDownstreamClosed
		audit.ErrorPhase = "downstream"
		audit.ErrorCode = "downstream_connection_closed"
		audit.ErrorMessage = "下游连接关闭"
	}
	return usage, audit
}
