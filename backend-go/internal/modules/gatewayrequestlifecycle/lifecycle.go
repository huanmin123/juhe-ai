// Package gatewayrequestlifecycle owns request-local monotonic state between
// an authenticated execution plan and a future gateway listener.  It has no
// I/O and deliberately does not select, retain, expose, or advance candidates:
// gatewayattemptloop remains the sole owner of candidate/API-key control.
package gatewayrequestlifecycle

import (
	"errors"
	"sync"

	"juhe-ai/backend-go/internal/modules/gatewayrequestexecution"
	"juhe-ai/backend-go/internal/modules/gatewaystreamrelay"
)

var (
	ErrExecutionEmpty      = errors.New("gateway request lifecycle execution is empty")
	ErrInitialCommit       = errors.New("gateway request lifecycle execution is already committed")
	ErrNoActiveAttempt     = errors.New("gateway request lifecycle has no active attempt")
	ErrStaleAttempt        = errors.New("gateway request lifecycle attempt is stale")
	ErrTerminal            = errors.New("gateway request lifecycle is terminal")
	ErrRetryAfterCommit    = errors.New("gateway request lifecycle cannot retry after downstream commit")
	ErrInvalidSinkState    = errors.New("gateway request lifecycle sink state is invalid")
	ErrSinkStateRegression = errors.New("gateway request lifecycle sink state regressed")
	ErrSuccessBeforeCommit = errors.New("gateway request lifecycle cannot succeed before downstream commit")
	ErrInvalidFailure      = errors.New("gateway request lifecycle failure kind is invalid")
	ErrExecutionMismatch   = errors.New("gateway request lifecycle execution lineage does not match")
	ErrContinuationState   = errors.New("gateway request lifecycle is not ready for continuation")
)

// State is the request-local lifecycle. Only Ready and Attempting permit an
// attempt loop to continue. Transport and semantic commits are hard monotonic
// fences; terminal states cannot be reopened.
type State string

const (
	StateReady              State = "ready"
	StateAttempting         State = "attempting"
	StateTransportCommitted State = "transport_committed"
	StateSemanticCommitted  State = "semantic_committed"
	StateSucceeded          State = "succeeded"
	StateFailed             State = "failed"
)

// FailureKind is intentionally typed. This package does not parse errors or
// make retry/policy decisions; the executor classifies exceptional ends.
type FailureKind string

const (
	FailureUpstream       FailureKind = "upstream"
	FailureGateway        FailureKind = "gateway"
	FailureClientCanceled FailureKind = "client_canceled"
)

// Attempt is an opaque request-local generation. It names no account, batch,
// key, or credential. The unexported token makes a late callback from a
// pre-commit retry unable to affect the next attempt.
type Attempt struct {
	number uint64
	token  uint64
}

func (a Attempt) Number() uint64 { return a.number }

// Snapshot is a copy of request-local state safe to pass between an attempt
// executor and a sink/response adapter. It intentionally contains no plan or
// candidate facts.
type Snapshot struct {
	State      State
	Sink       gatewaystreamrelay.SinkState
	Attempts   uint64
	Failure    FailureKind
	IsTerminal bool
}

// Lifecycle is safe for concurrent callbacks within one request. It must not
// be shared by requests: the attempt generation and first terminal are local.
type Lifecycle struct {
	mu       sync.Mutex
	state    State
	sink     gatewaystreamrelay.SinkState
	attempts uint64
	active   uint64
	failure  FailureKind
	lineage  gatewayrequestexecution.RequestLineage
}

// New accepts only an authenticated execution with at least one candidate
// batch. It checks that its initial sink is clean, then intentionally discards
// the batches; candidate traversal belongs exclusively to gatewayattemptloop.
func New(execution gatewayrequestexecution.Execution) (*Lifecycle, error) {
	initial := execution.InitialCommit()
	if !validSink(initial) || initial.TransportCommitted || initial.SemanticCommitted || initial.DownstreamBytes != 0 {
		return nil, ErrInitialCommit
	}
	batches := execution.Batches()
	if len(batches) == 0 {
		return nil, ErrExecutionEmpty
	}
	for _, batch := range batches {
		if batch.BindingID() == "" || batch.GroupID() == "" || len(batch.Candidates()) == 0 {
			return nil, ErrExecutionEmpty
		}
	}
	return &Lifecycle{state: StateReady, lineage: execution.RequestLineage()}, nil
}

// ValidateContinuation proves that a later target belongs to this lifecycle's
// original finalized request and that no active, committed, or terminal state
// can be reused. It is the only cross-group handoff check; adapters remain
// opaque and are still created separately for each target attempt loop.
func (l *Lifecycle) ValidateContinuation(execution gatewayrequestexecution.Execution) error {
	if l == nil {
		return ErrExecutionEmpty
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.lineage.Matches(execution) {
		return ErrExecutionMismatch
	}
	if l.state != StateReady || l.active != 0 || l.sink.TransportCommitted || l.sink.SemanticCommitted || l.sink.DownstreamBytes != 0 {
		return ErrContinuationState
	}
	return nil
}

// Start creates an opaque generation for one attempt-loop execution. It does
// not claim a candidate or resource. A pre-commit retry must call Start again
// after the attempt loop decides which verified candidate/key is next.
func (l *Lifecycle) Start() (Attempt, error) {
	if l == nil {
		return Attempt{}, ErrExecutionEmpty
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if isTerminal(l.state) {
		return Attempt{}, ErrTerminal
	}
	if l.state != StateReady {
		return Attempt{}, ErrNoActiveAttempt
	}
	l.attempts++
	l.active = l.attempts
	l.state = StateAttempting
	return Attempt{number: l.attempts, token: l.active}, nil
}

// RetryPreCommit marks one active attempt as safely replaceable. It neither
// releases resources nor advances candidate order. A stale result loses to a
// newer attempt generation; a committed sink blocks replacement entirely.
func (l *Lifecycle) RetryPreCommit(attempt Attempt) (Snapshot, error) {
	if l == nil {
		return Snapshot{}, ErrExecutionEmpty
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.requireActiveLocked(attempt); err != nil {
		return l.snapshotLocked(), err
	}
	if l.sink.TransportCommitted || l.sink.SemanticCommitted || l.sink.DownstreamBytes != 0 {
		return l.snapshotLocked(), ErrRetryAfterCommit
	}
	l.active = 0
	l.state = StateReady
	return l.snapshotLocked(), nil
}

// ObserveSink records a monotonic snapshot from the actual downstream owner.
// It does not write bytes, encode a terminal disposition, or decide retry.
func (l *Lifecycle) ObserveSink(attempt Attempt, value gatewaystreamrelay.SinkState) (Snapshot, error) {
	if l == nil {
		return Snapshot{}, ErrExecutionEmpty
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.requireActiveLocked(attempt); err != nil {
		return l.snapshotLocked(), err
	}
	if !validSink(value) {
		return l.snapshotLocked(), ErrInvalidSinkState
	}
	if value.DownstreamBytes < l.sink.DownstreamBytes || (l.sink.TransportCommitted && !value.TransportCommitted) || (l.sink.SemanticCommitted && !value.SemanticCommitted) {
		return l.snapshotLocked(), ErrSinkStateRegression
	}
	l.sink = value
	switch {
	case value.SemanticCommitted:
		l.state = StateSemanticCommitted
	case value.TransportCommitted:
		l.state = StateTransportCommitted
	default:
		l.state = StateAttempting
	}
	return l.snapshotLocked(), nil
}

// FinishSuccess seals the request after its transport fence. The first
// terminal wins: a competing completion/failure observes ErrTerminal and the
// already-recorded terminal snapshot.
func (l *Lifecycle) FinishSuccess(attempt Attempt) (Snapshot, error) {
	if l == nil {
		return Snapshot{}, ErrExecutionEmpty
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.requireActiveLocked(attempt); err != nil {
		return l.snapshotLocked(), err
	}
	if l.state != StateTransportCommitted && l.state != StateSemanticCommitted {
		return l.snapshotLocked(), ErrSuccessBeforeCommit
	}
	l.active = 0
	l.state = StateSucceeded
	return l.snapshotLocked(), nil
}

// FinishFailure makes a typed abnormal terminal win once. It cannot overwrite
// an earlier success/failure or a later attempt that replaced this generation.
func (l *Lifecycle) FinishFailure(attempt Attempt, kind FailureKind) (Snapshot, error) {
	if l == nil {
		return Snapshot{}, ErrExecutionEmpty
	}
	if !validFailure(kind) {
		return Snapshot{}, ErrInvalidFailure
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.requireActiveLocked(attempt); err != nil {
		return l.snapshotLocked(), err
	}
	l.active = 0
	l.state = StateFailed
	l.failure = kind
	return l.snapshotLocked(), nil
}

// CancelClient records a client-lifecycle terminal even when the listener
// observes cancellation before Start. Unlike an upstream/gateway failure, it
// has no attempt-local cause and therefore needs no attempt generation. It
// still obeys first-terminal wins and never writes a downstream disposition.
func (l *Lifecycle) CancelClient() (Snapshot, error) {
	if l == nil {
		return Snapshot{}, ErrExecutionEmpty
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if isTerminal(l.state) {
		return l.snapshotLocked(), ErrTerminal
	}
	l.active = 0
	l.state = StateFailed
	l.failure = FailureClientCanceled
	return l.snapshotLocked(), nil
}

// FinishClientCanceled is the explicit terminal-name alias for callers that
// report a completed client-abort observation rather than issuing a cancel.
func (l *Lifecycle) FinishClientCanceled() (Snapshot, error) { return l.CancelClient() }

func (l *Lifecycle) Snapshot() Snapshot {
	if l == nil {
		return Snapshot{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.snapshotLocked()
}

func (l *Lifecycle) requireActiveLocked(attempt Attempt) error {
	if isTerminal(l.state) {
		return ErrTerminal
	}
	if l.active == 0 {
		return ErrNoActiveAttempt
	}
	if attempt.token == 0 || attempt.token != l.active || attempt.number != l.attempts {
		return ErrStaleAttempt
	}
	return nil
}

func (l *Lifecycle) snapshotLocked() Snapshot {
	return Snapshot{State: l.state, Sink: l.sink, Attempts: l.attempts, Failure: l.failure, IsTerminal: isTerminal(l.state)}
}

func validSink(value gatewaystreamrelay.SinkState) bool {
	return value.DownstreamBytes >= 0 && (value.TransportCommitted || (!value.SemanticCommitted && value.DownstreamBytes == 0))
}

func validFailure(value FailureKind) bool {
	switch value {
	case FailureUpstream, FailureGateway, FailureClientCanceled:
		return true
	default:
		return false
	}
}

func isTerminal(value State) bool { return value == StateSucceeded || value == StateFailed }
