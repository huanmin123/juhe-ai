package gatewayhotquality

import (
	"sync"
	"time"
)

// Lifecycle is request-local. It holds the first observable downstream byte
// until a single terminal record can atomically project it with the outcome.
// It intentionally contains no goroutine, retry policy, or shared-store
// fallback; callers decide whether its best-effort observation is enabled.
type Lifecycle struct {
	store     *Store
	attemptID string
	scope     Scope
	mu        sync.Mutex
	firstByte *time.Duration
	terminal  *lifecycleTerminal
}

type lifecycleTerminal struct {
	mutation TerminalMutation
	err      error
}

type LifecycleTerminalInput struct {
	OutcomeID string
	Outcome   OutcomeClass
	Failure   FailureScope
	Source    TerminalSource
	Now       time.Time
}

func StartLifecycle(store *Store, attemptID string, scope Scope, now time.Time) (*Lifecycle, AttemptMutation, error) {
	if store == nil {
		return nil, AttemptMutation{}, ErrNilStore
	}
	mutation, err := store.RecordAttempt(RecordAttemptInput{AttemptID: attemptID, Scope: scope, Now: now})
	if err != nil {
		return nil, AttemptMutation{}, err
	}
	return &Lifecycle{store: store, attemptID: attemptID, scope: mutation.RequestedScope}, mutation, nil
}

// MarkFirstByte records only the first non-negative transport first-byte
// duration. Header commit and semantic-only events must not call this method.
func (l *Lifecycle) MarkFirstByte(value time.Duration) {
	if l == nil || value < 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.firstByte != nil {
		return
	}
	copyValue := value
	l.firstByte = &copyValue
}

// RecordTerminal is first-terminal-wins for a lifecycle instance. A rejected
// validation/store write is not sealed, so a caller can correct an internal
// programming error without making the receipt permanently unusable.
func (l *Lifecycle) RecordTerminal(input LifecycleTerminalInput) (TerminalMutation, error) {
	if l == nil {
		return TerminalMutation{}, ErrNilLifecycle
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.terminal != nil {
		return cloneTerminalMutation(l.terminal.mutation), l.terminal.err
	}
	var firstByte *time.Duration
	if l.firstByte != nil {
		copyValue := *l.firstByte
		firstByte = &copyValue
	}
	mutation, err := l.store.RecordTerminal(RecordTerminalInput{AttemptID: l.attemptID, Scope: l.scope, OutcomeID: input.OutcomeID, Outcome: input.Outcome, Failure: input.Failure, Source: input.Source, FirstByte: firstByte, Now: input.Now})
	if err != nil {
		return TerminalMutation{}, err
	}
	if mutation.Status == TerminalApplied || mutation.Status == TerminalIdempotent {
		l.terminal = &lifecycleTerminal{mutation: cloneTerminalMutation(mutation)}
	}
	return mutation, nil
}

var ErrNilLifecycle = errNilLifecycle{}

var ErrNilStore = errNilStore{}

type errNilLifecycle struct{}

func (errNilLifecycle) Error() string { return "hot quality lifecycle is nil" }

type errNilStore struct{}

func (errNilStore) Error() string { return "hot quality store is nil" }

func cloneTerminalMutation(value TerminalMutation) TerminalMutation {
	if value.Terminal != nil {
		terminal := *value.Terminal
		value.Terminal = &terminal
	}
	if value.EffectiveScope != nil {
		scope := *value.EffectiveScope
		value.EffectiveScope = &scope
	}
	return value
}
