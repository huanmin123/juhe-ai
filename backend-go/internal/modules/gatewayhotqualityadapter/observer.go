// Package gatewayhotqualityadapter bridges the generic attempt observer to the
// bounded, process-local hot-quality lifecycle. It is intentionally not wired
// into the application or a Redis owner.
package gatewayhotqualityadapter

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewayattemptloop"
	"juhe-ai/backend-go/internal/modules/gatewayhotquality"
	"juhe-ai/backend-go/internal/modules/gatewayusage"
)

const (
	DefaultMaxActive = 512
	// Keep the default active window aligned with the store's attempt identity.
	// A longer observer window cannot make an expired store identity terminal-safe.
	DefaultActiveTTL = gatewayhotquality.DefaultTerminalTTL
)

type Options struct {
	MaxActive int
	ActiveTTL time.Duration
	Now       func() time.Time
}

// Observer is safe for concurrent observer calls. It deliberately keeps no
// candidate, credential, request body, raw model, or failure message.
type Observer struct {
	store     *gatewayhotquality.Store
	maxActive int
	activeTTL time.Duration
	now       func() time.Time
	mu        sync.Mutex
	active    map[string]activeLifecycle
}

type activeLifecycle struct {
	lifecycle *gatewayhotquality.Lifecycle
	startedAt time.Time
	expiresAt time.Time
}

func New(store *gatewayhotquality.Store, options Options) (*Observer, error) {
	if store == nil {
		return nil, fmt.Errorf("gateway hot quality store is required")
	}
	if options.MaxActive == 0 {
		options.MaxActive = DefaultMaxActive
	}
	if options.ActiveTTL == 0 {
		options.ActiveTTL = DefaultActiveTTL
	}
	if options.MaxActive < 1 || options.MaxActive > gatewayattemptloop.MaxAttempts || options.ActiveTTL <= 0 {
		return nil, fmt.Errorf("invalid gateway hot quality adapter bounds")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Observer{store: store, maxActive: options.MaxActive, activeTTL: options.ActiveTTL, now: options.Now, active: make(map[string]activeLifecycle)}, nil
}

func (o *Observer) Start(_ context.Context, observation gatewayattemptloop.AttemptObservation) {
	if o == nil {
		return
	}
	scope, ok := scopeFor(observation)
	if !ok {
		return
	}
	now := o.at(observation.StartedAt)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cleanup(now)
	if _, exists := o.active[observation.ID]; exists || len(o.active) >= o.maxActive {
		return
	}
	lifecycle, mutation, err := gatewayhotquality.StartLifecycle(o.store, observation.ID, scope, now)
	if err != nil || !acceptedAttempt(mutation.Status) {
		return
	}
	o.active[observation.ID] = activeLifecycle{lifecycle: lifecycle, startedAt: now, expiresAt: now.Add(o.activeTTL)}
}

func (o *Observer) FirstByte(_ context.Context, observation gatewayattemptloop.AttemptObservation, observedAt time.Time) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	active, ok := o.active[observation.ID]
	if !ok {
		return
	}
	if observedAt.IsZero() || observedAt.Before(active.startedAt) {
		return
	}
	active.lifecycle.MarkFirstByte(observedAt.Sub(active.startedAt))
}

func (o *Observer) Terminal(_ context.Context, observation gatewayattemptloop.AttemptObservation, terminal gatewayattemptloop.AttemptTerminalObservation) {
	if o == nil {
		return
	}
	now := o.at(terminal.CompletedAt)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cleanup(now)
	active, ok := o.active[observation.ID]
	if !ok {
		return
	}
	input := terminalInput(observation.ID, terminal, now)
	mutation, err := active.lifecycle.RecordTerminal(input)
	if err != nil {
		return
	}
	if !terminalAccepted(mutation.Status) && mutation.Status != gatewayhotquality.TerminalAttemptMissing && now.Before(active.expiresAt) {
		return
	}
	delete(o.active, observation.ID)
}

func (o *Observer) ActiveCount(now time.Time) int {
	if o == nil {
		return 0
	}
	now = o.at(now)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cleanup(now)
	return len(o.active)
}

func (o *Observer) at(value time.Time) time.Time {
	if value.IsZero() {
		value = o.now()
	}
	return value.UTC()
}

func (o *Observer) cleanup(now time.Time) {
	for id, active := range o.active {
		if !active.expiresAt.After(now) {
			delete(o.active, id)
		}
	}
}

func scopeFor(observation gatewayattemptloop.AttemptObservation) (gatewayhotquality.Scope, bool) {
	lane := gatewayhotquality.RequestLane(observation.RequestLane)
	if lane != gatewayhotquality.RequestLaneText && lane != gatewayhotquality.RequestLaneImage {
		return gatewayhotquality.Scope{}, false
	}
	scope := gatewayhotquality.Scope{AccountRuntimeKey: strings.TrimSpace(observation.AccountRuntime), ProtocolProfile: strings.TrimSpace(observation.ProtocolProfile), RequestLane: lane, ModelFamily: strings.TrimSpace(observation.ModelBucket)}
	if scope.AccountRuntimeKey == "" || scope.ProtocolProfile == "" || scope.ModelFamily == "" {
		return gatewayhotquality.Scope{}, false
	}
	return scope, true
}

func acceptedAttempt(status gatewayhotquality.AttemptMutationStatus) bool {
	return status == gatewayhotquality.AttemptApplied || status == gatewayhotquality.AttemptIdempotent || status == gatewayhotquality.AttemptDegraded
}

func terminalAccepted(status gatewayhotquality.TerminalMutationStatus) bool {
	return status == gatewayhotquality.TerminalApplied || status == gatewayhotquality.TerminalIdempotent
}

func terminalInput(attemptID string, terminal gatewayattemptloop.AttemptTerminalObservation, now time.Time) gatewayhotquality.LifecycleTerminalInput {
	result := gatewayhotquality.LifecycleTerminalInput{OutcomeID: attemptID + ":terminal", Outcome: gatewayhotquality.OutcomeUnknown, Failure: gatewayhotquality.FailureScopeNone, Source: gatewayhotquality.TerminalSourceRequestLife, Now: now}
	if !terminal.Valid {
		return result
	}
	if terminal.Success {
		result.Outcome = gatewayhotquality.OutcomeCompletedResponse
		result.Source = gatewayhotquality.TerminalSourceTransport
		return result
	}
	if terminal.FailureAttribution == gatewayusage.FailureAttributionDownstreamClosed {
		return result
	}
	// The generic observer intentionally has no typed protocol disposition or
	// transport evidence. Do not infer timeout/read/incomplete/explicit-policy
	// facts from status or diagnostic strings: Node treats many of them as
	// neutral unless a more specific lifecycle source confirms them.
	return result
}

var _ gatewayattemptloop.AttemptObserver = (*Observer)(nil)
