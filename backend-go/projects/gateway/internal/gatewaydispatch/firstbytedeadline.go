package gatewaydispatch

import (
	"context"
	"sync"
	"time"
)

// First-byte deadline decision machinery, migrated from
// upstream/first-byte-deadline.ts.

// FirstByteDeadlineAction mirrors the action union.
type FirstByteDeadlineAction string

const (
	FirstByteDeadlineActionContinue FirstByteDeadlineAction = "continue"
	FirstByteDeadlineActionAbort    FirstByteDeadlineAction = "abort"
)

// FirstByteDeadlineDecisionInput mirrors FirstByteDeadlineDecisionInput.
type FirstByteDeadlineDecisionInput struct {
	ElapsedMs int64
	TimeoutMs int64
	// Transport is 'stream' | 'non_stream'.
	Transport string
}

// FirstByteDeadlineHandler mirrors FirstByteDeadlineHandler. Node allows an
// async decision; in Go the hook is invoked synchronously inside the
// deadline goroutine and the transport aborts on its return value. The
// Node late-decision release path (decision.then(notify)) therefore has no
// pending tail here: the decision is complete before the abort applies.
type FirstByteDeadlineHandler func(input FirstByteDeadlineDecisionInput) FirstByteDeadlineAction

// NowMs is the wall-clock source for deadline decisions; tests override it.
var NowMs = func() int64 { return time.Now().UnixMilli() }

// ObservedFirstBytePendingRead mirrors observeFirstBytePendingRead: it keeps
// the settlement time of a pending read so a read that settled before the
// deadline can supersede the routing decision.
type ObservedFirstBytePendingRead[T any] struct {
	outcome chan readOutcome[T]

	mu          sync.Mutex
	settled     bool
	settledAtMs int64
}

type readOutcome[T any] struct {
	result T
	err    error
}

// ObserveFirstBytePendingRead mirrors observeFirstBytePendingRead.
func ObserveFirstBytePendingRead[T any](pendingRead func() (T, error)) *ObservedFirstBytePendingRead[T] {
	observed := &ObservedFirstBytePendingRead[T]{outcome: make(chan readOutcome[T], 1)}
	go func() {
		result, err := pendingRead()
		now := NowMs()
		observed.mu.Lock()
		observed.settled = true
		observed.settledAtMs = now
		observed.mu.Unlock()
		observed.outcome <- readOutcome[T]{result: result, err: err}
	}()
	return observed
}

// IsSettled mirrors isSettled().
func (o *ObservedFirstBytePendingRead[T]) IsSettled() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.settled
}

// SettledAtMs mirrors settledAtMs().
func (o *ObservedFirstBytePendingRead[T]) SettledAtMs() (int64, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.settledAtMs, o.settled
}

// Await resolves the pending read (Node: await pendingRead.promise).
func (o *ObservedFirstBytePendingRead[T]) Await() (T, error) {
	outcome := <-o.outcome
	// Keep the outcome available for repeated Await calls.
	o.outcome = make(chan readOutcome[T], 1)
	o.outcome <- outcome
	return outcome.result, outcome.err
}

// FirstByteDeadlineDecisionResult mirrors the decision result union.
type FirstByteDeadlineDecisionResult[T any] struct {
	// Type is 'read' | 'action' | 'response_precommit_deadline'.
	Type string

	// read variant
	Result        T
	Action        FirstByteDeadlineAction
	DecisionError error
	SettledAtMs   int64

	// every variant: the read error when the read itself rejected
	Error error
}

// Decision result types.
const (
	DeadlineDecisionRead              = "read"
	DeadlineDecisionAction            = "action"
	DeadlineDecisionResponsePrecommit = "response_precommit_deadline"
)

// FirstByteDeadlineDecisionWaitOptions mirrors
// FirstByteDeadlineDecisionWaitOptions.
type FirstByteDeadlineDecisionWaitOptions struct {
	ResponsePrecommitDeadlineAtMs *int64
	OnResponsePrecommitDeadline   func()
}

// DecideFirstByteDeadlineAfterPendingRead mirrors
// decideFirstByteDeadlineAfterPendingRead: wait for the routing decision
// because it may own shared reservations or observations; the caller decides
// whether a read that settled in parallel is semantic enough to supersede
// that decision (raw activity alone is not).
func DecideFirstByteDeadlineAfterPendingRead[T any](
	pendingRead *ObservedFirstBytePendingRead[T],
	handler FirstByteDeadlineHandler,
	input FirstByteDeadlineDecisionInput,
	options FirstByteDeadlineDecisionWaitOptions,
) FirstByteDeadlineDecisionResult[T] {
	action, handlerErr := runDeadlineHandler(handler, input)
	if handlerErr != nil {
		if !pendingRead.IsSettled() {
			return FirstByteDeadlineDecisionResult[T]{Type: DeadlineDecisionAction, Error: handlerErr}
		}
		result, readErr := pendingRead.Await()
		settledAt, _ := pendingRead.SettledAtMs()
		return FirstByteDeadlineDecisionResult[T]{
			Type: DeadlineDecisionRead, Result: result, DecisionError: handlerErr,
			SettledAtMs: settledAt, Error: readErr,
		}
	}

	if options.ResponsePrecommitDeadlineAtMs == nil {
		return finishDeadlineDecision(pendingRead, action, nil)
	}

	deadlineAtMs := *options.ResponsePrecommitDeadlineAtMs
	// The Go decision settles synchronously, so the response-precommit timer
	// can only win when the wall deadline already passed (Node:
	// outcome.settledAtMs > responsePrecommitDeadlineAtMs).
	if deadlineAtMs < NowMs() {
		// Decision settled after the wall deadline: the wall wins.
		deadlineError := &GatewayResponsePrecommitDeadlineError{DeadlineAtMs: deadlineAtMs}
		notifyResponsePrecommitDeadline(options.OnResponsePrecommitDeadline)
		settledAt, settled := pendingRead.SettledAtMs()
		if settled && settledAt <= deadlineAtMs {
			result, readErr := pendingRead.Await()
			return FirstByteDeadlineDecisionResult[T]{
				Type: DeadlineDecisionRead, Result: result,
				DecisionError: deadlineError, SettledAtMs: settledAt, Error: readErr,
			}
		}
		return FirstByteDeadlineDecisionResult[T]{
			Type: DeadlineDecisionResponsePrecommit, Error: deadlineError,
		}
	}
	return finishDeadlineDecision(pendingRead, action, nil)
}

// runDeadlineHandler mirrors the try/catch around the Node handler call.
func runDeadlineHandler(handler FirstByteDeadlineHandler, input FirstByteDeadlineDecisionInput) (action FirstByteDeadlineAction, err error) {
	if handler == nil {
		return FirstByteDeadlineActionAbort, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			action = ""
			if panicErr, ok := recovered.(error); ok {
				err = panicErr
				return
			}
			err = &deadlineHandlerPanic{value: recovered}
		}
	}()
	return handler(input), nil
}

type deadlineHandlerPanic struct{ value any }

func (e *deadlineHandlerPanic) Error() string { return "网关首字截止决策失败" }

func finishDeadlineDecision[T any](
	pendingRead *ObservedFirstBytePendingRead[T],
	action FirstByteDeadlineAction,
	decisionErr error,
) FirstByteDeadlineDecisionResult[T] {
	if decisionErr != nil && !pendingRead.IsSettled() {
		return FirstByteDeadlineDecisionResult[T]{Type: DeadlineDecisionAction, Error: decisionErr}
	}
	if !pendingRead.IsSettled() {
		return FirstByteDeadlineDecisionResult[T]{Type: DeadlineDecisionAction, Action: action}
	}
	result, readErr := pendingRead.Await()
	settledAt, _ := pendingRead.SettledAtMs()
	return FirstByteDeadlineDecisionResult[T]{
		Type: DeadlineDecisionRead, Result: result, Action: action,
		DecisionError: decisionErr, SettledAtMs: settledAt, Error: readErr,
	}
}

func notifyResponsePrecommitDeadline(callback func()) {
	if callback == nil {
		return
	}
	defer func() { _ = recover() }()
	callback()
}

// waitForDelayMs mirrors shared/retry-policy.ts waitForRetryDelayMs with the
// abort signal translated to context cancellation.
func waitForDelayMs(ctx context.Context, delayMs int64) error {
	if delayMs <= 0 {
		return nil
	}
	timer := time.NewTimer(time.Duration(delayMs) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
