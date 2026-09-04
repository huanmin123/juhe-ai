package gatewaycircuit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

const (
	gatewayAccountCircuitKnownModelLimit      = 256
	gatewayAccountCircuitUnknownModelBucket   = "unknown"
	gatewayAccountCircuitFailureEvidenceMarker = "|request_evidence_sha256="
)

// Transport failure kinds mirror GatewayAccountCircuitTransportFailureKind.
const (
	TransportFailureKindTransport     = "transport"
	TransportFailureKindTimeout       = "timeout"
	TransportFailureKindReadIncomplete = "read_incomplete"
)

// TransportFailure mirrors GatewayAccountCircuitTransportFailure.
type TransportFailure struct {
	Kind   string
	Reason string
}

// Confirmation mirrors GatewayAccountCircuitConfirmation.
type Confirmation struct {
	Scope            Scope
	ScopeKey         string
	AccountRuntimeKey string
	Generation       int64
	DispatchRevision string
	LeaseID          string
}

// FailureDecision mirrors GatewayAccountCircuitFailureDecision.
type FailureDecision struct {
	// Outcome is 'confirmation_acquired' | 'suspected' | 'observer_neutral' | 'blocked'.
	Outcome      string
	Confirmation *Confirmation
	State        State
}

// Failure decision outcomes.
const (
	DecisionConfirmationAcquired = "confirmation_acquired"
	DecisionSuspected            = "suspected"
	DecisionObserverNeutral      = "observer_neutral"
	DecisionBlocked              = "blocked"
)

// PrepareResult mirrors GatewayAccountCircuitPrepareResult.
type PrepareResult struct {
	// Outcome is 'dispatchable' | 'blocked'.
	Outcome string
	Attempt *Attempt
	State   *State
}

// Prepare outcomes.
const (
	PrepareDispatchable = "dispatchable"
	PrepareBlocked      = "blocked"
)

// CircuitOperation mirrors GatewayRoutingCircuitOperation (the operations the
// observability stream consumes).
type CircuitOperation string

const (
	OperationSuspect             CircuitOperation = "suspect"
	OperationAcquireConfirmation CircuitOperation = "acquire_confirmation"
	OperationCompleteConfirmation CircuitOperation = "complete_confirmation"
	OperationReplaceRevision     CircuitOperation = "replace_revision"
	OperationRecordParentEvidence CircuitOperation = "record_parent_evidence"
)

// MutationEvent mirrors the onMutation callback input.
type MutationEvent struct {
	Scope         Scope
	State         State
	Status        string
	Operation     CircuitOperation
	PreviousPhase string
}

// RoutingObservabilityEvent carries the observeGatewayRouting payloads the
// Node service emits; production wiring forwards them to the metrics sink.
type RoutingObservabilityEvent struct {
	Kind      string // 'circuit_dispatch' | 'circuit_mutation' | 'circuit_transition'
	Outcome   string
	Phase     string
	Operation string
	Status    string
	LeaseKind string
	From      string
	To        string
	Source    string
}

// ServiceOptions mirrors GatewayAccountCircuitServiceOptions.
type ServiceOptions struct {
	Now                        func() int64
	CreateID                   func() string
	OnMutation                 func(ctx context.Context, input MutationEvent) error
	IsRuntimeStateReady        func(accountRuntimeKey string) bool
	EnsureRuntimeStateReady    func(ctx context.Context, accountRuntimeKey string) (bool, error)
	EscalationDistinctScopeThreshold *int64
	EscalationWindowMs               *int64
	Settings                   Settings
	Random                     func() float64
}

// PrepareAttemptInput mirrors PrepareGatewayAccountCircuitAttemptInput.
type PrepareAttemptInput struct {
	Account                       gatewayruntimecache.OpenAIAccountSecret
	RequestLane                   string
	Model                         *string
	ConfirmationLeaseDurationMs   int64
	ConfirmationEligible          *bool
	ConfirmationFailuresRequired  *int64
	Confirmation                  *Confirmation
	FailureEvidenceKey            *string
}

type observerState struct {
	generation                 int64
	dispatchRevision           string
	expectedFailureEvidenceKey string
	observerEvidenceKey        string
	state                      State
}

type confirmationSettlementIntent struct {
	outcome                    string
	reason                     *string
	failureEvidenceKey         *string
	framingCompleteDisposition *string
}

type confirmationSettlement struct {
	outcome string
	result  MutationResult
}

// settlementSignal carries the in-flight settlement result or the error to
// every concurrent settler (Node: all await the same promise).
type settlementSignal struct {
	settlement confirmationSettlement
	err        error
}

// Attempt mirrors GatewayAccountCircuitAttempt.
type Attempt struct {
	service                    *CircuitService
	Scope                      Scope
	DispatchRevision           string
	ConfirmationLeaseDurationMs int64
	ConfirmationFailuresRequired int64
	IsObserver                 bool

	mu                                 sync.Mutex
	requestRecoveryGeneration          *int64
	requestRecoveryEvidenceKey         *string
	confirmationKeyRotationFailureObserved bool
	confirmation                       *Confirmation
	confirmationSettlementIntent       *confirmationSettlementIntent
	confirmationSettled                *confirmationSettlement
	settlementSignal                   chan settlementSignal
	failureEvidenceKey                 *string
	observer                           *observerState
}

// IsConfirmation mirrors the isConfirmation getter.
func (a *Attempt) IsConfirmation() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.confirmation != nil
}

// ReportFramingComplete mirrors reportFramingComplete. A nil result mirrors
// the Node undefined return.
func (a *Attempt) ReportFramingComplete(ctx context.Context) (*MutationResult, error) {
	a.mu.Lock()
	keyRotationFailureObserved := a.confirmationKeyRotationFailureObserved
	settled := a.confirmationSettled
	hasConfirmation := a.confirmation != nil
	scope := a.Scope
	a.mu.Unlock()

	if hasConfirmation || settled != nil {
		var disposition *string
		if keyRotationFailureObserved {
			closed := "closed"
			disposition = &closed
		}
		settlement, err := a.settleConfirmation(ctx, confirmationSettlementIntent{
			outcome:                    OutcomeFramingComplete,
			framingCompleteDisposition: disposition,
		})
		if err != nil {
			return nil, err
		}
		result := settlement.result
		return &result, nil
	}

	a.mu.Lock()
	observer := a.observer
	requestRecoveryGeneration := a.requestRecoveryGeneration
	requestRecoveryEvidenceKey := a.requestRecoveryEvidenceKey
	a.mu.Unlock()

	if observer != nil {
		result, err := a.service.completeObserverFraming(ctx, completeObserverFramingInput{
			scope:                      scope,
			generation:                 observer.generation,
			dispatchRevision:           observer.dispatchRevision,
			expectedFailureEvidenceKey: observer.expectedFailureEvidenceKey,
			observerEvidenceKey:        observer.observerEvidenceKey,
		})
		if err != nil {
			return nil, err
		}
		return &result, nil
	}
	if requestRecoveryGeneration != nil {
		result, err := a.service.completeRequestFramingAfterKeyRotation(ctx, keyRotationFramingInput{
			scope:              scope,
			generation:         *requestRecoveryGeneration,
			dispatchRevision:   a.DispatchRevision,
			failureEvidenceKey: requestRecoveryEvidenceKey,
		})
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		a.requestRecoveryGeneration = nil
		a.requestRecoveryEvidenceKey = nil
		a.mu.Unlock()
		return &result, nil
	}
	if a.IsConfirmation() {
		return nil, nil
	}
	if err := a.service.clearAccountEscalationEvidenceAfterFramingComplete(ctx, scope, a.DispatchRevision); err != nil {
		return nil, err
	}
	return nil, nil
}

// ReportTransportFailure mirrors reportTransportFailure.
func (a *Attempt) ReportTransportFailure(ctx context.Context, failure TransportFailure) (FailureDecision, error) {
	failureReason, err := requiredText(failure.Reason, "failure.reason")
	if err != nil {
		return FailureDecision{}, err
	}
	a.mu.Lock()
	hasConfirmation := a.confirmation != nil
	settled := a.confirmationSettled
	failureEvidenceKey := a.failureEvidenceKey
	confirmationFailuresRequired := a.ConfirmationFailuresRequired
	scope := a.Scope
	dispatchRevision := a.DispatchRevision
	observer := a.observer
	a.mu.Unlock()

	if hasConfirmation || settled != nil {
		settlement, err := a.settleConfirmation(ctx, confirmationSettlementIntent{
			outcome:            OutcomeTransportFailure,
			reason:             strPtr(failureReason),
			failureEvidenceKey: failureEvidenceKey,
		})
		if err != nil {
			return FailureDecision{}, err
		}
		if settlement.outcome == OutcomeTransportFailure && settlement.result.State.Phase == PhaseSuspect {
			generation := settlement.result.State.Generation
			a.mu.Lock()
			a.requestRecoveryGeneration = &generation
			if len(settlement.result.State.FailureEvidenceKeys) > 0 {
				last := settlement.result.State.FailureEvidenceKeys[len(settlement.result.State.FailureEvidenceKeys)-1]
				a.requestRecoveryEvidenceKey = &last
			} else {
				a.requestRecoveryEvidenceKey = nil
			}
			a.mu.Unlock()
		}
		outcome := DecisionObserverNeutral
		if settlement.outcome == OutcomeTransportFailure {
			outcome = DecisionBlocked
		}
		return FailureDecision{Outcome: outcome, State: settlement.result.State}, nil
	}
	if observer != nil {
		return FailureDecision{Outcome: DecisionObserverNeutral, State: observer.state}, nil
	}
	decision, err := a.service.SuspectForegroundFailure(ctx, suspectForegroundInput{
		scope:                        scope,
		dispatchRevision:             dispatchRevision,
		confirmationFailuresRequired: &confirmationFailuresRequired,
		reason:                       fmt.Sprintf("%s:%s", failure.Kind, failureReason),
		failureEvidenceKey:           failureEvidenceKey,
	})
	if err != nil {
		return FailureDecision{}, err
	}
	if decision.Outcome == DecisionSuspected {
		a.mu.Lock()
		generation := decision.State.Generation
		a.requestRecoveryGeneration = &generation
		if last, ok, err := LastFailureEvidenceKey(decision.State); err == nil && ok {
			key := last
			a.requestRecoveryEvidenceKey = &key
		} else if err != nil {
			a.mu.Unlock()
			return FailureDecision{}, err
		} else {
			a.requestRecoveryEvidenceKey = nil
		}
		a.mu.Unlock()
	}
	return decision, nil
}

// DeferConfirmationTransportFailureForKeyRotation mirrors
// deferConfirmationTransportFailureForKeyRotation.
func (a *Attempt) DeferConfirmationTransportFailureForKeyRotation() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.confirmation == nil || a.confirmationSettlementIntent != nil {
		return false
	}
	a.confirmationKeyRotationFailureObserved = true
	return true
}

// ReportUnknown mirrors reportUnknown.
func (a *Attempt) ReportUnknown(ctx context.Context) (*MutationResult, error) {
	a.mu.Lock()
	hasConfirmation := a.confirmation != nil
	settled := a.confirmationSettled
	a.mu.Unlock()
	if hasConfirmation || settled != nil {
		settlement, err := a.settleConfirmation(ctx, confirmationSettlementIntent{outcome: OutcomeUnknown})
		if err != nil {
			return nil, err
		}
		result := settlement.result
		return &result, nil
	}
	return nil, nil
}

// settleConfirmation mirrors the private settleConfirmation: the first
// observed terminal outcome owns the lease.
func (a *Attempt) settleConfirmation(ctx context.Context, requested confirmationSettlementIntent) (confirmationSettlement, error) {
	a.mu.Lock()
	if a.confirmationSettled != nil {
		settled := *a.confirmationSettled
		a.mu.Unlock()
		return settled, nil
	}
	confirmation := a.confirmation
	if confirmation == nil {
		a.mu.Unlock()
		return confirmationSettlement{}, errors.New("settleConfirmation called without a confirmation")
	}
	if a.confirmationSettlementIntent == nil {
		intentCopy := requested
		a.confirmationSettlementIntent = &intentCopy
	}
	intent := *a.confirmationSettlementIntent
	if a.settlementSignal != nil {
		signal := a.settlementSignal
		a.mu.Unlock()
		received := <-signal
		return received.settlement, received.err
	}
	signal := make(chan settlementSignal, 1)
	a.settlementSignal = signal
	a.mu.Unlock()

	result, err := a.service.CompleteConfirmation(ctx, *confirmation, intent.outcome, intent.reason, intent.failureEvidenceKey, intent.framingCompleteDisposition)
	if err != nil {
		a.mu.Lock()
		a.settlementSignal = nil
		a.mu.Unlock()
		signal <- settlementSignal{err: err}
		return confirmationSettlement{}, err
	}
	completed := confirmationSettlement{outcome: intent.outcome, result: result}
	a.mu.Lock()
	a.confirmationSettled = &completed
	a.confirmation = nil
	a.confirmationKeyRotationFailureObserved = false
	a.mu.Unlock()
	signal <- settlementSignal{settlement: completed}
	return completed, nil
}

// CircuitService mirrors GatewayAccountCircuitService.
type CircuitService struct {
	store                        Store
	now                          func() int64
	createID                     func() string
	onMutation                   func(ctx context.Context, input MutationEvent) error
	isRuntimeStateReady          func(accountRuntimeKey string) bool
	ensureRuntimeStateReady      func(ctx context.Context, accountRuntimeKey string) (bool, error)
	escalationDistinctScopeThreshold int64
	escalationWindowMs               int64
	settings                     Settings
	random                       func() float64
	observability                func(event RoutingObservabilityEvent)
}

// NewCircuitService mirrors new GatewayAccountCircuitService.
func NewCircuitService(store Store, options ServiceOptions) (*CircuitService, error) {
	settings := options.Settings
	if len(settings.AccountCircuitBackoffMs) == 0 {
		settings = DefaultSettings()
	}
	threshold, err := NormalizeEscalationDistinctScopeThreshold(options.EscalationDistinctScopeThreshold, settings.AccountCircuitEscalationDistinctScopeThreshold)
	if err != nil {
		return nil, err
	}
	windowMs, err := NormalizeEscalationWindowMs(options.EscalationWindowMs, settings.AccountCircuitEscalationWindowMs)
	if err != nil {
		return nil, err
	}
	now := options.Now
	if now == nil {
		now = defaultNowMs
	}
	createID := options.CreateID
	if createID == nil {
		createID = defaultCreateID
	}
	isReady := options.IsRuntimeStateReady
	if isReady == nil {
		isReady = func(string) bool { return true }
	}
	random := options.Random
	if random == nil {
		random = defaultRandom
	}
	return &CircuitService{
		store:                        store,
		now:                          now,
		createID:                     createID,
		onMutation:                   options.OnMutation,
		isRuntimeStateReady:          isReady,
		ensureRuntimeStateReady:      options.EnsureRuntimeStateReady,
		escalationDistinctScopeThreshold: threshold,
		escalationWindowMs:               windowMs,
		settings:                         settings,
		random:                           random,
		observability:                    nil,
	}, nil
}

// SetObservabilitySink wires the routing observability sink (Node observes
// through the singleton service; Go takes an explicit hook).
func (s *CircuitService) SetObservabilitySink(sink func(event RoutingObservabilityEvent)) {
	s.observability = sink
}

func (s *CircuitService) observe(event RoutingObservabilityEvent) {
	if s.observability != nil {
		s.observability(event)
	}
}

func (s *CircuitService) observeBlockedDispatch(state State) {
	if state.Phase == PhaseClosed {
		return
	}
	s.observe(RoutingObservabilityEvent{Kind: "circuit_dispatch", Outcome: "blocked", Phase: state.Phase})
}

func (s *CircuitService) notifyMutation(
	ctx context.Context,
	operation CircuitOperation,
	scope Scope,
	result MutationResult,
	previousPhase string,
) error {
	if s.onMutation == nil || result.Status == MutationNotFound {
		return nil
	}
	for _, relatedState := range result.RelatedStates.slice() {
		if err := s.onMutation(ctx, MutationEvent{
			Scope:     relatedState.Scope,
			State:     relatedState,
			Status:    MutationApplied,
			Operation: operation,
		}); err != nil {
			return err
		}
	}
	return s.onMutation(ctx, MutationEvent{
		Scope:         scope,
		State:         result.State,
		Status:        result.Status,
		Operation:     operation,
		PreviousPhase: previousPhase,
	})
}

// nowPtr returns a fresh clock sample as a pointer (Node passes this.now()
// per call).
func (s *CircuitService) nowPtr() *int64 {
	now := s.now()
	return &now
}

// PrepareAttempt mirrors prepareAttempt.
func (s *CircuitService) PrepareAttempt(ctx context.Context, input PrepareAttemptInput) (PrepareResult, error) {
	scope := GatewayAccountProtocolModelScope(input.Account, input.RequestLane, input.Model)
	dispatchRevision, err := AccountCircuitDispatchRevision(input.Account)
	if err != nil {
		return PrepareResult{}, err
	}
	leaseDurationMs, err := positiveDuration(input.ConfirmationLeaseDurationMs)
	if err != nil {
		return PrepareResult{}, err
	}
	confirmationFailuresRequiredValue := DefaultConfirmationFailuresRequired
	if input.ConfirmationFailuresRequired != nil {
		confirmationFailuresRequiredValue = *input.ConfirmationFailuresRequired
	}
	confirmationFailuresRequired, err := boundedConfirmationFailuresRequired(confirmationFailuresRequiredValue)
	if err != nil {
		return PrepareResult{}, err
	}
	expectedScopeKey, err := ScopeKey(scope)
	if err != nil {
		return PrepareResult{}, err
	}

	runtimeStateReady := s.isRuntimeStateReady(scope.AccountRuntimeKey)
	if !runtimeStateReady && s.ensureRuntimeStateReady != nil {
		ready, err := s.ensureRuntimeStateReady(ctx, scope.AccountRuntimeKey)
		if err != nil {
			return PrepareResult{}, err
		}
		runtimeStateReady = ready
	}
	if !runtimeStateReady {
		s.observe(RoutingObservabilityEvent{Kind: "circuit_dispatch", Outcome: "rebuild_blocked", Phase: PhaseSuspect})
		blocked := ClosedState(scope, dispatchRevision, 0, "runtime-state-rebuilding", s.now())
		blocked.Phase = PhaseSuspect
		blocked.FailureReason = strPtr("runtime_state_rebuilding")
		return PrepareResult{Outcome: PrepareBlocked, State: &blocked}, nil
	}

	accountScope := Scope{Kind: ScopeKindAccount, AccountRuntimeKey: scope.AccountRuntimeKey}
	accountState, err := s.store.Get(ctx, accountScope, s.nowPtr())
	if err != nil {
		return PrepareResult{}, err
	}
	if accountState.DispatchRevision != "" && accountState.DispatchRevision != dispatchRevision {
		replaced, err := s.store.ReplaceDispatchRevision(ctx, ReplaceDispatchRevisionInput{
			Scope:            accountScope,
			DispatchRevision: dispatchRevision,
			TransitionID:     s.createID(),
			NowMs:            s.nowPtr(),
		})
		if err != nil {
			return PrepareResult{}, err
		}
		if err := s.notifyMutation(ctx, OperationReplaceRevision, accountScope, replaced, accountState.Phase); err != nil {
			return PrepareResult{}, err
		}
		accountState = replaced.State
		if replaced.Status == MutationStaleDispatchRevision {
			s.observeBlockedDispatch(accountState)
			state := accountState
			return PrepareResult{Outcome: PrepareBlocked, State: &state}, nil
		}
	}
	if accountState.Phase != PhaseClosed {
		s.observeBlockedDispatch(accountState)
		state := accountState
		return PrepareResult{Outcome: PrepareBlocked, State: &state}, nil
	}

	if input.Confirmation != nil {
		if input.ConfirmationEligible != nil && !*input.ConfirmationEligible {
			if _, err := s.CompleteConfirmation(ctx, *input.Confirmation, OutcomeUnknown, nil, nil, nil); err != nil {
				return PrepareResult{}, err
			}
			state, err := s.store.Get(ctx, scope, s.nowPtr())
			if err != nil {
				return PrepareResult{}, err
			}
			s.observeBlockedDispatch(state)
			return PrepareResult{Outcome: PrepareBlocked, State: &state}, nil
		}
		if input.Confirmation.ScopeKey != expectedScopeKey ||
			input.Confirmation.AccountRuntimeKey != scope.AccountRuntimeKey ||
			input.Confirmation.DispatchRevision != dispatchRevision {
			state, err := s.store.Get(ctx, scope, s.nowPtr())
			if err != nil {
				return PrepareResult{}, err
			}
			s.observeBlockedDispatch(state)
			return PrepareResult{Outcome: PrepareBlocked, State: &state}, nil
		}
		state, err := s.store.Get(ctx, scope, s.nowPtr())
		if err != nil {
			return PrepareResult{}, err
		}
		if sameRequestFailureEvidence(state, input.FailureEvidenceKey) {
			s.observeBlockedDispatch(state)
			return PrepareResult{Outcome: PrepareBlocked, State: &state}, nil
		}
		leaseMatches := state.Lease != nil && state.Lease.Kind == LeaseKindConfirmation &&
			state.Lease.LeaseID == input.Confirmation.LeaseID
		if state.Phase != PhaseSuspect ||
			state.Generation != input.Confirmation.Generation ||
			state.DispatchRevision != input.Confirmation.DispatchRevision ||
			!leaseMatches {
			s.observeBlockedDispatch(state)
			return PrepareResult{Outcome: PrepareBlocked, State: &state}, nil
		}
		return PrepareResult{
			Outcome: PrepareDispatchable,
			Attempt: newAttempt(s, scope, dispatchRevision, leaseDurationMs, confirmationFailuresRequired, cloneConfirmation(input.Confirmation), normalizedFailureEvidenceKey(input.FailureEvidenceKey), nil),
		}, nil
	}

	state, err := s.store.Get(ctx, scope, s.nowPtr())
	if err != nil {
		return PrepareResult{}, err
	}
	if state.DispatchRevision != "" && state.DispatchRevision != dispatchRevision {
		replaced, err := s.store.ReplaceDispatchRevision(ctx, ReplaceDispatchRevisionInput{
			Scope:            scope,
			DispatchRevision: dispatchRevision,
			TransitionID:     s.createID(),
			NowMs:            s.nowPtr(),
		})
		if err != nil {
			return PrepareResult{}, err
		}
		if err := s.notifyMutation(ctx, OperationReplaceRevision, scope, replaced, state.Phase); err != nil {
			return PrepareResult{}, err
		}
		state = replaced.State
		if replaced.Status == MutationStaleDispatchRevision {
			s.observeBlockedDispatch(state)
			return PrepareResult{Outcome: PrepareBlocked, State: &state}, nil
		}
	}
	if state.Phase == PhaseClosed {
		return PrepareResult{
			Outcome: PrepareDispatchable,
			Attempt: newAttempt(s, scope, dispatchRevision, leaseDurationMs, confirmationFailuresRequired, nil, normalizedFailureEvidenceKey(input.FailureEvidenceKey), nil),
		}, nil
	}
	if state.Phase == PhaseSuspect && state.DispatchRevision == dispatchRevision {
		if input.ConfirmationEligible != nil && !*input.ConfirmationEligible {
			s.observeBlockedDispatch(state)
			return PrepareResult{Outcome: PrepareBlocked, State: &state}, nil
		}
		confirmationEvidenceKey := normalizedFailureEvidenceKey(input.FailureEvidenceKey)
		if confirmationEvidenceKey == nil || sameRequestFailureEvidence(state, confirmationEvidenceKey) {
			s.observeBlockedDispatch(state)
			return PrepareResult{Outcome: PrepareBlocked, State: &state}, nil
		}
		decision, err := s.acquireConfirmation(ctx, scope, state, leaseDurationMs, *confirmationEvidenceKey)
		if err != nil {
			return PrepareResult{}, err
		}
		if decision.Outcome == DecisionConfirmationAcquired {
			// Mirror releaseAcquiredConfirmation: best-effort release of the
			// lease with one replayed attempt when the release fails.
			currentParent, err := s.store.Get(ctx, accountScope, s.nowPtr())
			if err != nil {
				s.releaseAcquiredConfirmation(ctx, decision.Confirmation)
				return PrepareResult{}, err
			}
			parentMismatch := currentParent.Phase != PhaseClosed ||
				(currentParent.DispatchRevision != "" && currentParent.DispatchRevision != dispatchRevision)
			if parentMismatch {
				s.releaseAcquiredConfirmation(ctx, decision.Confirmation)
				s.observeBlockedDispatch(currentParent)
				return PrepareResult{Outcome: PrepareBlocked, State: &currentParent}, nil
			}
			return PrepareResult{
				Outcome: PrepareDispatchable,
				Attempt: newAttempt(s, scope, dispatchRevision, leaseDurationMs, confirmationFailuresRequired, decision.Confirmation, normalizedFailureEvidenceKey(input.FailureEvidenceKey), nil),
			}, nil
		}
		candidateState := decision.State
		expectedFailureEvidenceKey, ok, err := LastFailureEvidenceKey(candidateState)
		if err != nil {
			return PrepareResult{}, err
		}
		observerKeys, err := FailureEvidenceKeysOf(candidateState)
		if err != nil {
			return PrepareResult{}, err
		}
		if candidateState.Phase == PhaseSuspect &&
			candidateState.DispatchRevision == dispatchRevision &&
			candidateState.ShadowedByIncidentID == nil &&
			ok && expectedFailureEvidenceKey != "" &&
			!containsString(observerKeys, *confirmationEvidenceKey) {
			return PrepareResult{
				Outcome: PrepareDispatchable,
				Attempt: newAttempt(s, scope, dispatchRevision, leaseDurationMs, confirmationFailuresRequired, nil, confirmationEvidenceKey, &observerState{
					generation:                 candidateState.Generation,
					dispatchRevision:           candidateState.DispatchRevision,
					expectedFailureEvidenceKey: expectedFailureEvidenceKey,
					observerEvidenceKey:        *confirmationEvidenceKey,
					state:                      candidateState,
				}),
			}, nil
		}
		s.observeBlockedDispatch(candidateState)
		return PrepareResult{Outcome: PrepareBlocked, State: &candidateState}, nil
	}
	s.observeBlockedDispatch(state)
	return PrepareResult{Outcome: PrepareBlocked, State: &state}, nil
}

// releaseAcquiredConfirmation mirrors releaseAcquiredConfirmation: complete
// as unknown; on failure replay the exact same completion once.
func (s *CircuitService) releaseAcquiredConfirmation(ctx context.Context, confirmation *Confirmation) {
	if confirmation == nil {
		return
	}
	_, err := s.CompleteConfirmation(ctx, *confirmation, OutcomeUnknown, nil, nil, nil)
	if err != nil {
		_, _ = s.CompleteConfirmation(ctx, *confirmation, OutcomeUnknown, nil, nil, nil)
	}
}

type suspectForegroundInput struct {
	scope                        Scope
	dispatchRevision             string
	confirmationFailuresRequired *int64
	reason                       string
	failureEvidenceKey           *string
}

// SuspectForegroundFailure mirrors suspectForegroundFailure.
func (s *CircuitService) SuspectForegroundFailure(ctx context.Context, input suspectForegroundInput) (FailureDecision, error) {
	nowMs := s.now()
	suspectTransitionID := s.createID()
	dispatchRevision, err := requiredText(input.dispatchRevision, "dispatchRevision")
	if err != nil {
		return FailureDecision{}, err
	}
	confirmationFailuresRequiredValue := DefaultConfirmationFailuresRequired
	if input.confirmationFailuresRequired != nil {
		confirmationFailuresRequiredValue = *input.confirmationFailuresRequired
	}
	confirmationFailuresRequired, err := boundedConfirmationFailuresRequired(confirmationFailuresRequiredValue)
	if err != nil {
		return FailureDecision{}, err
	}
	failureEvidenceKey, err := NormalizeFailureEvidenceKey(input.failureEvidenceKey, "suspect:"+suspectTransitionID)
	if err != nil {
		return FailureDecision{}, err
	}
	suspect, err := s.store.Suspect(ctx, SuspectInput{
		Scope:                        input.scope,
		DispatchRevision:             dispatchRevision,
		TransitionID:                 suspectTransitionID,
		Reason:                       failureReasonWithEvidence(input.reason, input.failureEvidenceKey),
		ConfirmationFailuresRequired: &confirmationFailuresRequired,
		FailureEvidenceKey:           &failureEvidenceKey,
		NowMs:                        &nowMs,
	})
	if err != nil {
		return FailureDecision{}, err
	}
	if err := s.notifyMutation(ctx, OperationSuspect, input.scope, suspect, PhaseClosed); err != nil {
		return FailureDecision{}, err
	}
	if suspect.Status == MutationCapacityExhausted ||
		suspect.State.Phase != PhaseSuspect ||
		suspect.State.DispatchRevision != input.dispatchRevision {
		return FailureDecision{Outcome: DecisionBlocked, State: suspect.State}, nil
	}
	if suspect.Status == MutationApplied {
		return FailureDecision{Outcome: DecisionSuspected, State: suspect.State}, nil
	}
	return FailureDecision{Outcome: DecisionBlocked, State: suspect.State}, nil
}

// CompleteConfirmation mirrors completeConfirmation.
func (s *CircuitService) CompleteConfirmation(
	ctx context.Context,
	confirmation Confirmation,
	outcome string,
	reason *string,
	failureEvidenceKey *string,
	framingCompleteDisposition *string,
) (MutationResult, error) {
	completionIdentity := sha256Hex(stableSerialize(map[string]any{
		"scopeKey":         confirmation.ScopeKey,
		"generation":       confirmation.Generation,
		"dispatchRevision": confirmation.DispatchRevision,
		"leaseId":          confirmation.LeaseID,
		"outcome":          outcome,
	}))
	now := s.now()
	input := CompleteConfirmationInput{
		Scope:                      confirmation.Scope,
		Generation:                 confirmation.Generation,
		DispatchRevision:           confirmation.DispatchRevision,
		TransitionID:               fmt.Sprintf("confirmation:%s", completionIdentity),
		LeaseID:                    confirmation.LeaseID,
		Outcome:                    outcome,
		Reason:                     reason,
		FramingCompleteDisposition: framingCompleteDisposition,
		NowMs:                      &now,
	}
	if outcome == OutcomeTransportFailure {
		normalized, err := NormalizeFailureEvidenceKey(failureEvidenceKey, "confirmation:"+confirmation.LeaseID)
		if err != nil {
			return MutationResult{}, err
		}
		input.FailureEvidenceKey = &normalized
	}
	result, err := s.completeAndNotify(ctx, OperationCompleteConfirmation, confirmation.Scope, PhaseSuspect, func() (MutationResult, error) {
		return s.store.CompleteConfirmation(ctx, input)
	})
	if err != nil {
		return MutationResult{}, err
	}
	appliedOrReplayed := result.Status == MutationApplied || result.Status == MutationIdempotent
	if outcome == OutcomeFramingComplete && appliedOrReplayed && result.State.Phase == PhaseClosed {
		if err := s.clearAccountEscalationEvidenceAfterFramingComplete(ctx, confirmation.Scope, confirmation.DispatchRevision); err != nil {
			return MutationResult{}, err
		}
	}
	if outcome == OutcomeTransportFailure && appliedOrReplayed && result.State.Phase == PhaseOpen {
		reasonValue := "protocol_model_transport_failure"
		if reason != nil {
			reasonValue = *reason
		}
		reasonValue, err = requiredText(reasonValue, "reason")
		if err != nil {
			return MutationResult{}, err
		}
		maxProtocolScopes := s.escalationDistinctScopeThreshold
		if maxProtocolScopes < 8 {
			maxProtocolScopes = 8
		}
		escalation, err := s.store.RecordProtocolModelOpenEvidence(ctx, ProtocolModelOpenEvidenceInput{
			Scope:                   confirmation.Scope,
			Generation:              result.State.Generation,
			DispatchRevision:        confirmation.DispatchRevision,
			EvidenceID:              fmt.Sprintf("%s:%d:%s", confirmation.ScopeKey, confirmation.Generation, confirmation.LeaseID),
			AccountTransitionID:     fmt.Sprintf("confirmation-parent:%s", completionIdentity),
			Reason:                  reasonValue,
			ConfirmedFailureCount:   1,
			DistinctScopeThreshold:  s.escalationDistinctScopeThreshold,
			WindowMs:                s.escalationWindowMs,
			MaxProtocolScopes:       maxProtocolScopes,
			NowMs:                   &now,
		})
		if err != nil {
			return MutationResult{}, err
		}
		for _, relatedState := range escalation.RelatedStates.slice() {
			if s.onMutation != nil {
				if err := s.onMutation(ctx, MutationEvent{
					Scope:     relatedState.Scope,
					State:     relatedState,
					Status:    MutationApplied,
					Operation: OperationRecordParentEvidence,
				}); err != nil {
					return MutationResult{}, err
				}
			}
		}
		if s.onMutation != nil {
			event := MutationEvent{
				Scope:     Scope{Kind: ScopeKindAccount, AccountRuntimeKey: confirmation.AccountRuntimeKey},
				State:     escalation.AccountState,
				Status:    escalationMutationStatus(escalation.Status),
				Operation: OperationRecordParentEvidence,
			}
			if escalation.Status == EscalationEscalated {
				event.PreviousPhase = PhaseClosed
			}
			if err := s.onMutation(ctx, event); err != nil {
				return MutationResult{}, err
			}
		}
	}
	return result, nil
}

// ClearAccountEscalationEvidenceAfterFramingComplete mirrors
// clearAccountEscalationEvidenceAfterFramingComplete.
func (s *CircuitService) ClearAccountEscalationEvidenceAfterFramingComplete(ctx context.Context, scope Scope, dispatchRevision string) error {
	return s.clearAccountEscalationEvidenceAfterFramingComplete(ctx, scope, dispatchRevision)
}

func (s *CircuitService) clearAccountEscalationEvidenceAfterFramingComplete(ctx context.Context, scope Scope, dispatchRevision string) error {
	normalizedDispatchRevision, err := requiredText(dispatchRevision, "dispatchRevision")
	if err != nil {
		return err
	}
	_, err = s.store.ClearAccountEscalationEvidence(ctx, ClearAccountEscalationEvidenceInput{
		AccountRuntimeKey: scope.AccountRuntimeKey,
		DispatchRevision:  normalizedDispatchRevision,
		EvidenceID:        s.createID(),
		NowMs:             int64Ptr(s.now()),
	})
	return err
}

type completeObserverFramingInput struct {
	scope                      Scope
	generation                 int64
	dispatchRevision           string
	expectedFailureEvidenceKey string
	observerEvidenceKey        string
}

func (s *CircuitService) completeObserverFraming(ctx context.Context, input completeObserverFramingInput) (MutationResult, error) {
	dispatchRevision, err := requiredText(input.dispatchRevision, "dispatchRevision")
	if err != nil {
		return MutationResult{}, err
	}
	now := s.now()
	completed, err := s.completeAndNotify(ctx, OperationCompleteConfirmation, input.scope, PhaseSuspect, func() (MutationResult, error) {
		return s.store.CloseSuspectFromObserver(ctx, CloseSuspectFromObserverInput{
			Scope:                      input.scope,
			Generation:                 input.generation,
			DispatchRevision:           dispatchRevision,
			TransitionID:               s.createID(),
			ExpectedFailureEvidenceKey: input.expectedFailureEvidenceKey,
			ObserverEvidenceKey:        input.observerEvidenceKey,
			NowMs:                      &now,
		})
	})
	if err != nil {
		return MutationResult{}, err
	}
	if completed.Status == MutationApplied && completed.State.Phase == PhaseClosed {
		if err := s.clearAccountEscalationEvidenceAfterFramingComplete(ctx, input.scope, input.dispatchRevision); err != nil {
			return MutationResult{}, err
		}
	}
	return completed, nil
}

type keyRotationFramingInput struct {
	scope             Scope
	generation        int64
	dispatchRevision  string
	failureEvidenceKey *string
}

// CompleteRequestFramingAfterKeyRotation mirrors
// completeRequestFramingAfterKeyRotation.
func (s *CircuitService) CompleteRequestFramingAfterKeyRotation(ctx context.Context, input keyRotationFramingInput) (MutationResult, error) {
	return s.completeRequestFramingAfterKeyRotation(ctx, input)
}

func (s *CircuitService) completeRequestFramingAfterKeyRotation(ctx context.Context, input keyRotationFramingInput) (MutationResult, error) {
	var expectedEvidence *string
	if input.failureEvidenceKey != nil {
		normalized, err := NormalizeFailureEvidenceKey(input.failureEvidenceKey, "request-key-rotation")
		if err != nil {
			return MutationResult{}, err
		}
		expectedEvidence = &normalized
	}
	if expectedEvidence == nil {
		now := s.now()
		state, err := s.store.Get(ctx, input.scope, &now)
		if err != nil {
			return MutationResult{}, err
		}
		return MutationResult{Status: MutationStateMismatch, State: state}, nil
	}
	dispatchRevision, err := requiredText(input.dispatchRevision, "dispatchRevision")
	if err != nil {
		return MutationResult{}, err
	}
	now := s.now()
	completed, err := s.completeAndNotify(ctx, OperationCompleteConfirmation, input.scope, PhaseSuspect, func() (MutationResult, error) {
		return s.store.CloseSuspectFromKeyRotation(ctx, CloseSuspectFromKeyRotationInput{
			Scope:                      input.scope,
			Generation:                 input.generation,
			DispatchRevision:           dispatchRevision,
			TransitionID:               s.createID(),
			ExpectedFailureEvidenceKey: *expectedEvidence,
			NowMs:                      &now,
		})
	})
	if err != nil {
		return MutationResult{}, err
	}
	if completed.Status == MutationApplied && completed.State.Phase == PhaseClosed {
		if err := s.clearAccountEscalationEvidenceAfterFramingComplete(ctx, input.scope, input.dispatchRevision); err != nil {
			return MutationResult{}, err
		}
	}
	return completed, nil
}

func (s *CircuitService) completeAndNotify(
	ctx context.Context,
	operation CircuitOperation,
	scope Scope,
	previousPhase string,
	mutation func() (MutationResult, error),
) (MutationResult, error) {
	result, err := mutation()
	if err != nil {
		return MutationResult{}, err
	}
	if err := s.notifyMutation(ctx, operation, scope, result, previousPhase); err != nil {
		return MutationResult{}, err
	}
	return result, nil
}

func (s *CircuitService) acquireConfirmation(
	ctx context.Context,
	scope Scope,
	state State,
	leaseDurationMs int64,
	confirmationEvidenceKey string,
) (FailureDecision, error) {
	leaseID := s.createID()
	nowMs := s.now()
	expectedFailureEvidenceKey, ok, err := LastFailureEvidenceKey(state)
	if err != nil {
		return FailureDecision{}, err
	}
	acquireInput := AcquireConfirmationLeaseInput{
		Scope:            scope,
		Generation:       state.Generation,
		DispatchRevision: state.DispatchRevision,
		TransitionID:     s.createID(),
		LeaseID:          leaseID,
		LeaseUntilMs:     nowMs + leaseDurationMs,
		NowMs:            &nowMs,
	}
	if ok {
		acquireInput.ExpectedFailureEvidenceKey = &expectedFailureEvidenceKey
	}
	acquireInput.ConfirmationEvidenceKey = &confirmationEvidenceKey

	var result MutationResult
	result, err = s.store.AcquireConfirmationLease(ctx, acquireInput)
	if err != nil {
		// A Redis reply can be lost after EVAL committed. Replaying the exact
		// transition is safe and lets the caller recover ownership of its lease.
		result, err = s.store.AcquireConfirmationLease(ctx, acquireInput)
		if err != nil {
			now := s.now()
			observed, getErr := s.store.Get(ctx, scope, &now)
			if getErr != nil {
				return FailureDecision{}, err
			}
			replayCommitted := observed.Phase == PhaseSuspect &&
				observed.Generation == state.Generation &&
				observed.DispatchRevision == state.DispatchRevision &&
				observed.Lease != nil && observed.Lease.Kind == LeaseKindConfirmation &&
				observed.Lease.LeaseID == leaseID
			if !replayCommitted {
				return FailureDecision{}, err
			}
			result = MutationResult{Status: MutationIdempotent, State: observed}
		}
	}
	if err := s.notifyMutation(ctx, OperationAcquireConfirmation, scope, result, PhaseSuspect); err != nil {
		return FailureDecision{}, err
	}
	ownsLease := result.State.Phase == PhaseSuspect &&
		result.State.Generation == state.Generation &&
		result.State.DispatchRevision == state.DispatchRevision &&
		result.State.Lease != nil && result.State.Lease.Kind == LeaseKindConfirmation &&
		result.State.Lease.LeaseID == leaseID
	if (result.Status != MutationApplied && result.Status != MutationIdempotent) || !ownsLease {
		return FailureDecision{Outcome: DecisionBlocked, State: result.State}, nil
	}
	scopeKey, err := ScopeKey(scope)
	if err != nil {
		return FailureDecision{}, err
	}
	confirmation := &Confirmation{
		Scope:             scope,
		ScopeKey:          scopeKey,
		AccountRuntimeKey: scope.AccountRuntimeKey,
		Generation:        result.State.Generation,
		DispatchRevision:  result.State.DispatchRevision,
		LeaseID:           leaseID,
	}
	return FailureDecision{Outcome: DecisionConfirmationAcquired, Confirmation: confirmation, State: result.State}, nil
}

// GatewayAccountProtocolModelScope mirrors gatewayAccountProtocolModelScope.
func GatewayAccountProtocolModelScope(
	account gatewayruntimecache.OpenAIAccountSecret,
	requestLane string,
	model *string,
) Scope {
	protocolProfile := account.ProviderProtocolProfileID
	if strings.TrimSpace(protocolProfile) == "" {
		protocolProfile = fmt.Sprintf("%s:%s", account.ProtocolCode, account.ProtocolVersion)
	}
	return Scope{
		Kind:              ScopeKindProtocolModel,
		AccountRuntimeKey: account.ID,
		ProtocolProfile:   protocolProfile,
		RequestLane:       requestLane,
		ModelBucket:       gatewayAccountCircuitModelBucket(account, model),
	}
}

// AccountCircuitDispatchRevision mirrors accountCircuitDispatchRevision.
func AccountCircuitDispatchRevision(account gatewayruntimecache.OpenAIAccountSecret) (string, error) {
	if account.DispatchRevision != nil && *account.DispatchRevision > 0 {
		return fmt.Sprintf("%d", *account.DispatchRevision), nil
	}
	credentialMaterialDigest := sha256Hex(stableSerialize(map[string]any{
		"apiKey":       account.APIKey,
		"apiKeys":      anySlice(account.APIKeys),
		"refreshToken": nilOrString(account.RefreshToken),
		"clientId":     nilOrString(account.ClientID),
		"credentials":  accountCircuitCredentialOwnerIdentity(account.Credentials),
	}))
	revisionPayload := map[string]any{
		"accountRuntimeKey":          account.ID,
		"credentialSourceAccountId":  nilOrString(account.CredentialSourceAccountID),
		"providerCode":               account.ProviderCode,
		"providerProtocolProfileId":  account.ProviderProtocolProfileID,
		"protocolCode":               account.ProtocolCode,
		"protocolVersion":            account.ProtocolVersion,
		"accountType":                account.Type,
		"baseUrl":                    account.BaseURL,
		"proxyProfileId":             nilOrString(account.ProxyProfileID),
		"proxyUrl":                   nilOrString(account.ProxyURL),
		"clientCompatibility":        account.ClientCompatibility,
		"supportedEndpointModes":     anySlice(account.SupportedEndpointModes),
		"credentialMaterialDigest":   credentialMaterialDigest,
	}
	return fmt.Sprintf("v1:%s", sha256Hex(stableSerialize(revisionPayload))), nil
}

func gatewayAccountCircuitModelBucket(account gatewayruntimecache.OpenAIAccountSecret, model *string) string {
	candidate := normalizeModelBucket(model)
	if candidate == nil {
		return gatewayAccountCircuitUnknownModelBucket
	}
	known := map[string]struct{}{}
	for _, configured := range account.SupportedModels {
		normalized := normalizeModelBucket(&configured)
		if normalized != nil {
			known[*normalized] = struct{}{}
		}
	}
	for _, mapping := range account.ModelMappings {
		if !mapping.Enabled {
			continue
		}
		source := normalizeModelBucket(&mapping.SourceModel)
		upstream := normalizeModelBucket(&mapping.UpstreamModel)
		if source != nil {
			known[*source] = struct{}{}
		}
		if upstream != nil {
			known[*upstream] = struct{}{}
		}
	}
	boundedKnown := make([]string, 0, len(known))
	for value := range known {
		boundedKnown = append(boundedKnown, value)
	}
	sort.Strings(boundedKnown)
	if len(boundedKnown) > gatewayAccountCircuitKnownModelLimit {
		boundedKnown = boundedKnown[:gatewayAccountCircuitKnownModelLimit]
	}
	for _, value := range boundedKnown {
		if value == *candidate {
			return *candidate
		}
	}
	return gatewayAccountCircuitUnknownModelBucket
}

var modelBucketControlChars = regexp.MustCompile(`[\x{0000}-\x{001f}\x{007f}]`)

func normalizeModelBucket(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.ToLower(strings.TrimSpace(*value))
	if normalized == "" || len(normalized) > 128 || modelBucketControlChars.MatchString(normalized) {
		return nil
	}
	return &normalized
}

func cloneConfirmation(value *Confirmation) *Confirmation {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func newAttempt(
	service *CircuitService,
	scope Scope,
	dispatchRevision string,
	confirmationLeaseDurationMs int64,
	confirmationFailuresRequired int64,
	confirmation *Confirmation,
	failureEvidenceKey *string,
	observer *observerState,
) *Attempt {
	return &Attempt{
		service:                      service,
		Scope:                        scope,
		DispatchRevision:             dispatchRevision,
		ConfirmationLeaseDurationMs:  confirmationLeaseDurationMs,
		ConfirmationFailuresRequired: confirmationFailuresRequired,
		IsObserver:                   observer != nil,
		confirmation:                 confirmation,
		failureEvidenceKey:           failureEvidenceKey,
		observer:                     observer,
	}
}

func positiveDuration(value int64) (int64, error) {
	if value <= 0 {
		return 0, errors.New("confirmationLeaseDurationMs 必须是正有限数值")
	}
	return value, nil
}

func escalationMutationStatus(status string) string {
	if status == EscalationEscalated || status == EscalationAlreadyActive {
		return MutationApplied
	}
	if status == EscalationRecorded {
		return MutationIdempotent
	}
	return status
}

func requiredText(value, name string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", fmt.Errorf("账户电路缺少 %s", name)
	}
	return normalized, nil
}

func failureReasonWithEvidence(reason string, evidenceKey *string) string {
	normalizedReason := strings.TrimSpace(reason)
	normalizedEvidence := normalizedFailureEvidenceKey(evidenceKey)
	if normalizedEvidence != nil {
		return fmt.Sprintf("%s%s%s", normalizedReason, gatewayAccountCircuitFailureEvidenceMarker, *normalizedEvidence)
	}
	return normalizedReason
}

func sameRequestFailureEvidence(state State, evidenceKey *string) bool {
	normalizedEvidence := normalizedFailureEvidenceKey(evidenceKey)
	if normalizedEvidence == nil {
		return false
	}
	keys, err := FailureEvidenceKeysOf(state)
	if err == nil && containsString(keys, *normalizedEvidence) {
		return true
	}
	failureReason := state.FailureReason
	if failureReason == nil {
		return false
	}
	markerIndex := strings.LastIndex(*failureReason, gatewayAccountCircuitFailureEvidenceMarker)
	if markerIndex < 0 {
		return false
	}
	return (*failureReason)[markerIndex+len(gatewayAccountCircuitFailureEvidenceMarker):] == *normalizedEvidence
}

func boundedConfirmationFailuresRequired(value int64) (int64, error) {
	return NormalizeConfirmationFailuresRequired(&value, LegacyConfirmationFailuresRequired)
}

func normalizedFailureEvidenceKey(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.ToLower(strings.TrimSpace(*value))
	if normalized == "" || !isSHA256Hex(normalized) {
		return nil
	}
	return &normalized
}

// stableSerialize mirrors the Node stableSerialize: deterministic JSON with
// sorted object keys, Node-compatible string escaping and '[Circular]'
// cycle markers. Object identity for the cycle guard uses the reflect
// pointer of map/slice values (Node uses a WeakSet).
func stableSerialize(value any, seen ...map[string]struct{}) string {
	var visited map[string]struct{}
	if len(seen) == 1 {
		visited = seen[0]
	} else {
		visited = map[string]struct{}{}
	}
	if value == nil {
		return "null"
	}
	identity, hasIdentity := cycleIdentity(value)
	if hasIdentity {
		if _, ok := visited[identity]; ok {
			return jsonString("[Circular]")
		}
		visited[identity] = struct{}{}
		defer delete(visited, identity)
	}
	switch typed := value.(type) {
	case string:
		return jsonString(typed)
	case bool:
		return jsonString(typed)
	case int:
		return jsonString(typed)
	case int64:
		return jsonString(typed)
	case float64:
		return jsonString(typed)
	case []any:
		parts := make([]string, len(typed))
		for index, item := range typed {
			parts[index] = stableSerialize(item, visited)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case []string:
		return stableSerialize(anySlice(typed), visited)
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, jsonString(key)+":"+stableSerialize(typed[key], visited))
		}
		return "{" + strings.Join(parts, ",") + "}"
	default:
		return jsonString(fmt.Sprintf("%v", value))
	}
}

func cycleIdentity(value any) (string, bool) {
	switch reflect.ValueOf(value).Kind() {
	case reflect.Map:
		return fmt.Sprintf("map:%d", reflect.ValueOf(value).Pointer()), true
	case reflect.Slice:
		return fmt.Sprintf("slice:%d", reflect.ValueOf(value).Pointer()), true
	}
	return "", false
}

// jsonString mirrors JSON.stringify for a scalar (Go encoding/json with HTML
// escaping disabled matches Node's escaping).
func jsonString(value any) string {
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "null"
	}
	return strings.TrimRight(out.String(), "\n")
}

func nilOrString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func anySlice(values []string) any {
	if values == nil {
		return nil
	}
	out := make([]any, len(values))
	for index, value := range values {
		out[index] = value
	}
	return out
}

// accountCircuitCredentialOwnerIdentity mirrors
// domain/account-circuit-owner.ts: only upstream connection identity keys.
func accountCircuitCredentialOwnerIdentity(credentials map[string]any) map[string]any {
	identityKeys := []string{
		"api_key",
		"api_keys",
		"access_token",
		"refresh_token",
		"client_id",
		"client_secret",
		"id_token",
		"account_id",
		"chatgpt_user_id",
		"quota_project_id",
		"base_url",
		"supported_endpoint_modes",
	}
	if credentials == nil {
		return map[string]any{}
	}
	identity := map[string]any{}
	for _, key := range identityKeys {
		if value, ok := credentials[key]; ok {
			identity[key] = value
		}
	}
	return identity
}
