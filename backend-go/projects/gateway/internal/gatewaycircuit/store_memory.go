package gatewaycircuit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// MemoryStoreOptions mirrors MemoryAccountCircuitStoreOptions.
type MemoryStoreOptions struct {
	Capacity            int64
	ClosedRetentionMs   int64
	ReplayLimitPerScope int64
	Now                 func() int64
	Random              func() float64
	Settings            Settings
}

type memoryEntry struct {
	state             State
	closedExpiresAtMs *int64
	replayOrder       []string
	replayIDs         map[string]struct{}
}

type memoryEscalationScopeEvidence struct {
	scopeKey              string
	incidentID            string
	evidenceID            string
	confirmedFailureCount int64
	observedAtMs          int64
}

type memoryEscalationEvidence struct {
	dispatchRevision string
	scopes           []memoryEscalationScopeEvidence
}

// MemoryStore mirrors MemoryAccountCircuitStore. All operations hold one
// mutex: the Node store relies on the event loop for serialization, the Go
// store makes that explicit.
type MemoryStore struct {
	mu                  sync.Mutex
	entries             map[string]*memoryEntry
	order               []string // insertion order for Node Map iteration parity
	escalationEvidence  map[string]*memoryEscalationEvidence
	capacity            int64
	closedRetentionMs   int64
	replayLimitPerScope int64
	now                 func() int64
	random              func() float64
	settings            Settings
	capacitySaturated   bool
}

// NewMemoryStore mirrors new MemoryAccountCircuitStore. Zero-valued
// ClosedRetentionMs / ReplayLimitPerScope fall back to the Node defaults.
func NewMemoryStore(options MemoryStoreOptions) (*MemoryStore, error) {
	capacity, err := positiveInteger(options.Capacity, "capacity")
	if err != nil {
		return nil, err
	}
	closedRetentionMs := DefaultClosedRetentionMs
	if options.ClosedRetentionMs != 0 {
		closedRetentionMs = options.ClosedRetentionMs
	}
	closedRetentionMs, err = positiveInteger(closedRetentionMs, "closedRetentionMs")
	if err != nil {
		return nil, err
	}
	replayLimit := DefaultReplayLimitPerScope
	if options.ReplayLimitPerScope != 0 {
		replayLimit = options.ReplayLimitPerScope
	}
	replayLimit, err = positiveInteger(replayLimit, "replayLimitPerScope")
	if err != nil {
		return nil, err
	}
	now := options.Now
	if now == nil {
		now = defaultNowMs
	}
	random := options.Random
	if random == nil {
		random = defaultRandom
	}
	settings := options.Settings
	if len(settings.AccountCircuitBackoffMs) == 0 {
		settings = DefaultSettings()
	}
	return &MemoryStore{
		entries:             map[string]*memoryEntry{},
		escalationEvidence:  map[string]*memoryEscalationEvidence{},
		capacity:            capacity,
		closedRetentionMs:   closedRetentionMs,
		replayLimitPerScope: replayLimit,
		now:                 now,
		random:              random,
		settings:            settings,
	}, nil
}

// Get mirrors store.get.
func (s *MemoryStore) Get(_ context.Context, scope Scope, nowMs *int64) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.resolveNow(nowMs)
	return s.getLocked(scope, now)
}

// Suspect mirrors store.suspect.
func (s *MemoryStore) Suspect(_ context.Context, input SuspectInput) (MutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.resolveNow(input.NowMs)
	existing, err := s.freshEntryLocked(input.Scope, now)
	if err != nil {
		return MutationResult{}, err
	}
	if existing != nil {
		if replay := s.idempotentLocked(existing, input.TransitionID); replay != nil {
			return *replay, nil
		}
	}
	if existing != nil && existing.state.DispatchRevision != "" &&
		existing.state.DispatchRevision != input.DispatchRevision {
		return mutationResult(MutationStaleDispatchRevision, existing.state), nil
	}
	if existing != nil && existing.state.Phase != PhaseClosed {
		return mutationResult(MutationStateMismatch, existing.state), nil
	}
	if existing == nil && !s.reserveCapacityLocked(now) {
		return mutationResult(MutationCapacityExhausted, CapacityExhaustedState(input.Scope, input.DispatchRevision, now)), nil
	}
	generation := int64(1)
	if existing != nil {
		generation = existing.state.Generation + 1
	}
	confirmationFailuresRequired, err := NormalizeConfirmationFailuresRequired(input.ConfirmationFailuresRequired, DefaultConfirmationFailuresRequired)
	if err != nil {
		return MutationResult{}, err
	}
	failureEvidenceKey, err := NormalizeFailureEvidenceKey(input.FailureEvidenceKey, "suspect:"+input.TransitionID)
	if err != nil {
		return MutationResult{}, err
	}
	dispatchRevision, err := requiredValue(input.DispatchRevision, "dispatchRevision")
	if err != nil {
		return MutationResult{}, err
	}
	transitionID, err := requiredValue(input.TransitionID, "transitionId")
	if err != nil {
		return MutationResult{}, err
	}
	incidentID := transitionID
	retryAt := now + s.settings.AccountCircuitSuspectConfirmationIntervalMs
	state := State{
		ScopeKey:                     MustScopeKey(input.Scope),
		Scope:                        input.Scope,
		Phase:                        PhaseSuspect,
		Generation:                   generation,
		DispatchRevision:             dispatchRevision,
		TransitionID:                 transitionID,
		BackoffAttempt:               0,
		RecoverySuccessCount:         0,
		ConfirmationFailuresRequired: &confirmationFailuresRequired,
		ConfirmationFailureCount:     int64Ptr(0),
		FailureEvidenceKeys:          stringList{failureEvidenceKey},
		IncidentID:                   &incidentID,
		FailureReason:                strPtr(input.Reason),
		RetryAtMs:                    &retryAt,
		UpdatedAtMs:                  now,
	}
	entry := existing
	if entry == nil {
		entry = s.newEntryLocked(state)
	}
	return s.applyLocked(entry, state, input.TransitionID, nil)
}

// AcquireConfirmationLease mirrors store.acquireConfirmationLease.
func (s *MemoryStore) AcquireConfirmationLease(_ context.Context, input AcquireConfirmationLeaseInput) (MutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.resolveNow(input.NowMs)
	entry, err := s.freshEntryLocked(input.Scope, now)
	if err != nil {
		return MutationResult{}, err
	}
	if invalid := s.validateIdentityLocked(entry, input.Scope, input.Generation, input.DispatchRevision); invalid != nil {
		return *invalid, nil
	}
	if entry == nil {
		return mutationResult(MutationNotFound, ClosedState(input.Scope, "", 0, "", 0)), nil
	}
	if replay := s.idempotentLocked(entry, input.TransitionID); replay != nil {
		return *replay, nil
	}
	if entry.state.Phase != PhaseSuspect || entry.state.ShadowedByIncidentID != nil {
		return mutationResult(MutationStateMismatch, entry.state), nil
	}
	if input.ExpectedFailureEvidenceKey != nil {
		expected, err := NormalizeFailureEvidenceKey(input.ExpectedFailureEvidenceKey, "confirmation-acquire:"+input.TransitionID)
		if err != nil {
			return MutationResult{}, err
		}
		last, ok, err := LastFailureEvidenceKey(entry.state)
		if err != nil {
			return MutationResult{}, err
		}
		if !ok || last != expected {
			return mutationResult(MutationStateMismatch, entry.state), nil
		}
	}
	if input.ConfirmationEvidenceKey != nil {
		confirmationEvidence, err := NormalizeFailureEvidenceKey(input.ConfirmationEvidenceKey, "confirmation-evidence:"+input.TransitionID)
		if err != nil {
			return MutationResult{}, err
		}
		keys, err := FailureEvidenceKeysOf(entry.state)
		if err != nil {
			return MutationResult{}, err
		}
		for _, key := range keys {
			if key == confirmationEvidence {
				return mutationResult(MutationStateMismatch, entry.state), nil
			}
		}
	}
	if entry.state.Lease != nil {
		return mutationResult(MutationStateMismatch, entry.state), nil
	}
	if entry.state.RetryAtMs != nil && *entry.state.RetryAtMs > now {
		return mutationResult(MutationNotDue, entry.state), nil
	}
	lease, err := memoryLease(LeaseKindConfirmation, input.LeaseID, input.LeaseUntilMs, now)
	if err != nil {
		return MutationResult{}, err
	}
	next := entry.state
	next.TransitionID = input.TransitionID
	next.Lease = lease
	next.UpdatedAtMs = now
	return s.applyLocked(entry, next, input.TransitionID, nil)
}

// CloseSuspectFromObserver mirrors store.closeSuspectFromObserver.
func (s *MemoryStore) CloseSuspectFromObserver(_ context.Context, input CloseSuspectFromObserverInput) (MutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, now, failure, err := s.checkedSuspectClosureLocked(input.Scope, input.Generation, input.DispatchRevision, input.TransitionID, input.ExpectedFailureEvidenceKey, input.NowMs)
	if err != nil {
		return MutationResult{}, err
	}
	if failure != nil {
		return *failure, nil
	}
	observerEvidenceKey, err := NormalizeFailureEvidenceKey(strPtr(input.ObserverEvidenceKey), "observer-close:"+input.TransitionID)
	if err != nil {
		return MutationResult{}, err
	}
	keys, err := FailureEvidenceKeysOf(entry.state)
	if err != nil {
		return MutationResult{}, err
	}
	for _, key := range keys {
		if key == observerEvidenceKey {
			return mutationResult(MutationStateMismatch, entry.state), nil
		}
	}
	return s.closeLocked(entry, input.TransitionID, now)
}

// CloseSuspectFromKeyRotation mirrors store.closeSuspectFromKeyRotation.
func (s *MemoryStore) CloseSuspectFromKeyRotation(_ context.Context, input CloseSuspectFromKeyRotationInput) (MutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, now, failure, err := s.checkedSuspectClosureLocked(input.Scope, input.Generation, input.DispatchRevision, input.TransitionID, input.ExpectedFailureEvidenceKey, input.NowMs)
	if err != nil {
		return MutationResult{}, err
	}
	if failure != nil {
		return *failure, nil
	}
	return s.closeLocked(entry, input.TransitionID, now)
}

// CompleteConfirmation mirrors store.completeConfirmation.
func (s *MemoryStore) CompleteConfirmation(_ context.Context, input CompleteConfirmationInput) (MutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, now, failure, err := s.checkedEntryLocked(input.Scope, input.Generation, input.DispatchRevision, input.TransitionID, input.LeaseID, input.NowMs)
	if err != nil {
		return MutationResult{}, err
	}
	if failure != nil {
		return *failure, nil
	}
	if input.Outcome == OutcomeFramingComplete {
		if input.FramingCompleteDisposition != nil && *input.FramingCompleteDisposition == "closed" {
			return s.closeLocked(entry, input.TransitionID, now)
		}
		return s.enterRecoveringLocked(entry, input.TransitionID, now)
	}
	if input.Outcome == OutcomeUnknown {
		backoffAttempt := entry.state.BackoffAttempt + 1
		next := entry.state
		next.TransitionID = input.TransitionID
		next.BackoffAttempt = backoffAttempt
		next.Lease = nil
		retryAt := now + s.settings.accountCircuitBackoffDelayMs(backoffAttempt,
			fmt.Sprintf("%s:%d:%d:confirmation-unknown", entry.state.ScopeKey, entry.state.Generation, backoffAttempt), s.random)
		next.RetryAtMs = &retryAt
		next.UpdatedAtMs = now
		return s.applyLocked(entry, next, input.TransitionID, nil)
	}
	confirmationFailuresRequired, err := NormalizeConfirmationFailuresRequired(entry.state.ConfirmationFailuresRequired, LegacyConfirmationFailuresRequired)
	if err != nil {
		return MutationResult{}, err
	}
	previousEvidenceKeys, err := FailureEvidenceKeysOf(entry.state)
	if err != nil {
		return MutationResult{}, err
	}
	failureEvidenceKey, err := NormalizeFailureEvidenceKey(input.FailureEvidenceKey, "confirmation:"+input.LeaseID)
	if err != nil {
		return MutationResult{}, err
	}
	isIndependentEvidence := !containsString(previousEvidenceKeys, failureEvidenceKey)
	failureEvidenceKeys := previousEvidenceKeys
	if isIndependentEvidence {
		failureEvidenceKeys = append(append([]string{}, previousEvidenceKeys...), failureEvidenceKey)
		keep := int(confirmationFailuresRequired) + 1
		if len(failureEvidenceKeys) > keep {
			failureEvidenceKeys = failureEvidenceKeys[len(failureEvidenceKeys)-keep:]
		}
	}
	previousCount, err := ConfirmationFailureCountOf(entry.state)
	if err != nil {
		return MutationResult{}, err
	}
	confirmationFailureCount := previousCount
	if isIndependentEvidence {
		confirmationFailureCount++
	}
	confirmationState := entry.state
	confirmationState.BackoffAttempt = 0
	confirmationState.ConfirmationFailuresRequired = &confirmationFailuresRequired
	confirmationState.ConfirmationFailureCount = &confirmationFailureCount
	confirmationState.FailureEvidenceKeys = stringList(failureEvidenceKeys)
	confirmationState.TransitionID = input.TransitionID
	if input.Reason != nil {
		confirmationState.FailureReason = input.Reason
	}
	confirmationState.Lease = nil
	retryAt := now + s.settings.AccountCircuitSuspectConfirmationIntervalMs
	confirmationState.RetryAtMs = &retryAt
	confirmationState.UpdatedAtMs = now
	if confirmationFailureCount < confirmationFailuresRequired {
		return s.applyLocked(entry, confirmationState, input.TransitionID, nil)
	}
	entry.state = confirmationState
	return s.openLocked(entry, input.TransitionID, now, input.Reason)
}

// AcquireCanaryLease mirrors store.acquireCanaryLease.
func (s *MemoryStore) AcquireCanaryLease(_ context.Context, input AcquireCanaryLeaseInput) (MutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.resolveNow(input.NowMs)
	entry, err := s.freshEntryLocked(input.Scope, now)
	if err != nil {
		return MutationResult{}, err
	}
	if invalid := s.validateIdentityLocked(entry, input.Scope, input.Generation, input.DispatchRevision); invalid != nil {
		return *invalid, nil
	}
	if entry == nil {
		return mutationResult(MutationNotFound, ClosedState(input.Scope, "", 0, "", 0)), nil
	}
	if replay := s.idempotentLocked(entry, input.TransitionID); replay != nil {
		return *replay, nil
	}
	if entry.state.Phase != PhaseOpen && entry.state.Phase != PhaseRecovering {
		return mutationResult(MutationStateMismatch, entry.state), nil
	}
	if entry.state.Lease != nil {
		return mutationResult(MutationStateMismatch, entry.state), nil
	}
	if entry.state.RetryAtMs != nil && *entry.state.RetryAtMs > now {
		return mutationResult(MutationNotDue, entry.state), nil
	}
	origin := entry.state.Phase
	kind := LeaseKindRecovery
	if origin == PhaseOpen {
		kind = LeaseKindHalfOpen
	}
	lease, err := memoryLease(kind, input.LeaseID, input.LeaseUntilMs, now)
	if err != nil {
		return MutationResult{}, err
	}
	next := entry.state
	next.Phase = PhaseHalfOpen
	next.TransitionID = input.TransitionID
	next.Lease = lease
	next.HalfOpenOrigin = strPtr(origin)
	next.UpdatedAtMs = now
	return s.applyLocked(entry, next, input.TransitionID, nil)
}

// CompleteCanary mirrors store.completeCanary.
func (s *MemoryStore) CompleteCanary(_ context.Context, input CompleteCanaryInput) (MutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.resolveNow(input.NowMs)
	entry, err := s.freshEntryLocked(input.Scope, now)
	if err != nil {
		return MutationResult{}, err
	}
	if invalid := s.validateIdentityLocked(entry, input.Scope, input.Generation, input.DispatchRevision); invalid != nil {
		return *invalid, nil
	}
	if entry == nil {
		return mutationResult(MutationNotFound, ClosedState(input.Scope, "", 0, "", 0)), nil
	}
	if replay := s.idempotentLocked(entry, input.TransitionID); replay != nil {
		return *replay, nil
	}
	if entry.state.Phase != PhaseHalfOpen {
		return mutationResult(MutationStateMismatch, entry.state), nil
	}
	if entry.state.Lease == nil || entry.state.Lease.LeaseID != input.LeaseID {
		return mutationResult(MutationLeaseMismatch, entry.state), nil
	}
	if input.Outcome == OutcomeTransportFailure {
		return s.openLocked(entry, input.TransitionID, now, input.Reason)
	}
	if input.Outcome == OutcomeUnknown {
		return s.restoreCanaryOriginLocked(entry, input.TransitionID, now)
	}
	if entry.state.HalfOpenOrigin != nil && *entry.state.HalfOpenOrigin == PhaseOpen {
		return s.enterRecoveringLocked(entry, input.TransitionID, now)
	}
	recoveryEvidenceScopeKeys := nextRecoveryEvidenceScopeKeys(entry.state, input.EvidenceScopeKey)
	recoverySuccessCount := entry.state.RecoverySuccessCount + 1
	if recoverySuccessCount >= s.settings.AccountCircuitRecoverySuccessThreshold {
		return s.closeLocked(entry, input.TransitionID, now)
	}
	next := entry.state
	next.Phase = PhaseRecovering
	next.TransitionID = input.TransitionID
	next.RecoverySuccessCount = recoverySuccessCount
	next.RecoveryEvidenceScopeKeys = stringList(recoveryEvidenceScopeKeys)
	next.Lease = nil
	next.HalfOpenOrigin = nil
	retryAt := now
	next.RetryAtMs = &retryAt
	next.UpdatedAtMs = now
	return s.applyLocked(entry, next, input.TransitionID, nil)
}

// RecordProtocolModelOpenEvidence mirrors
// store.recordProtocolModelOpenEvidence.
func (s *MemoryStore) RecordProtocolModelOpenEvidence(_ context.Context, input ProtocolModelOpenEvidenceInput) (EscalationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.resolveNow(input.NowMs)
	scopeKey := MustScopeKey(input.Scope)
	child, err := s.freshEntryLocked(input.Scope, now)
	if err != nil {
		return EscalationResult{}, err
	}
	accountScope := Scope{Kind: ScopeKindAccount, AccountRuntimeKey: input.Scope.AccountRuntimeKey}
	closedAccountState := ClosedState(accountScope, input.DispatchRevision, 0, "", 0)
	if child == nil {
		return escalationResult(EscalationNotFound, closedAccountState, 0, 0), nil
	}
	if child.state.Generation != input.Generation {
		return escalationResult(EscalationStaleGeneration, closedAccountState, 0, 0), nil
	}
	if child.state.DispatchRevision != input.DispatchRevision {
		return escalationResult(EscalationStaleRevision, closedAccountState, 0, 0), nil
	}
	if child.state.Phase != PhaseOpen {
		return escalationResult(EscalationStateMismatch, closedAccountState, 0, 0), nil
	}

	windowMs, err := positiveInteger(input.WindowMs, "windowMs")
	if err != nil {
		return EscalationResult{}, err
	}
	maxProtocolScopes, err := positiveInteger(input.MaxProtocolScopes, "maxProtocolScopes")
	if err != nil {
		return EscalationResult{}, err
	}
	distinctScopeThreshold, err := NormalizeEscalationDistinctScopeThreshold(&input.DistinctScopeThreshold, EscalationDistinctScopeThresholdDefault)
	if err != nil {
		return EscalationResult{}, err
	}
	if distinctScopeThreshold > maxProtocolScopes {
		return EscalationResult{}, errors.New("账户电路 distinctScopeThreshold 不能超过 maxProtocolScopes")
	}
	confirmedFailureCount, err := positiveInteger(input.ConfirmedFailureCount, "confirmedFailureCount")
	if err != nil {
		return EscalationResult{}, err
	}
	cutoff := now - windowMs
	previous := s.escalationEvidence[input.Scope.AccountRuntimeKey]
	var evidence *memoryEscalationEvidence
	if previous != nil && previous.dispatchRevision == input.DispatchRevision {
		scopes := make([]memoryEscalationScopeEvidence, 0, len(previous.scopes))
		for _, item := range previous.scopes {
			if item.observedAtMs >= cutoff {
				scopes = append(scopes, item)
			}
		}
		evidence = &memoryEscalationEvidence{dispatchRevision: previous.dispatchRevision, scopes: scopes}
	} else {
		evidence = &memoryEscalationEvidence{dispatchRevision: input.DispatchRevision, scopes: []memoryEscalationScopeEvidence{}}
	}
	for _, item := range evidence.scopes {
		if item.evidenceID == input.EvidenceID {
			accountState, err := s.getLocked(accountScope, now)
			if err != nil {
				return EscalationResult{}, err
			}
			return escalationResult(EscalationIdempotent, accountState, int64(len(evidence.scopes)), totalConfirmedFailures(evidence.scopes)), nil
		}
	}
	incidentID := scopeKey + "@" + strconv.FormatInt(child.state.Generation, 10)
	if child.state.IncidentID != nil {
		incidentID = *child.state.IncidentID
	}
	nextScopeEvidence := memoryEscalationScopeEvidence{
		scopeKey:              scopeKey,
		incidentID:            incidentID,
		evidenceID:            input.EvidenceID,
		confirmedFailureCount: confirmedFailureCount,
		observedAtMs:          now,
	}
	existingIndex := -1
	for index, item := range evidence.scopes {
		if item.scopeKey == scopeKey {
			existingIndex = index
			break
		}
	}
	if existingIndex >= 0 {
		evidence.scopes[existingIndex] = nextScopeEvidence
	} else {
		evidence.scopes = append(evidence.scopes, nextScopeEvidence)
	}
	sort.SliceStable(evidence.scopes, func(left, right int) bool {
		return evidence.scopes[left].observedAtMs < evidence.scopes[right].observedAtMs
	})
	for len(evidence.scopes) > int(maxProtocolScopes) {
		evidence.scopes = evidence.scopes[1:]
	}
	s.escalationEvidence[input.Scope.AccountRuntimeKey] = evidence

	failureTotal := totalConfirmedFailures(evidence.scopes)
	accountEntry, err := s.freshEntryLocked(accountScope, now)
	if err != nil {
		return EscalationResult{}, err
	}
	if int64(len(evidence.scopes)) < distinctScopeThreshold {
		accountState := closedAccountState
		if accountEntry != nil {
			accountState = accountEntry.state
		}
		return escalationResult(EscalationRecorded, accountState, int64(len(evidence.scopes)), failureTotal), nil
	}

	childScopeKeys := make([]string, 0, len(evidence.scopes))
	childIncidentIDs := make([]string, 0, len(evidence.scopes))
	for _, item := range evidence.scopes {
		childScopeKeys = append(childScopeKeys, item.scopeKey)
		childIncidentIDs = append(childIncidentIDs, item.incidentID)
	}
	if accountEntry != nil && accountEntry.state.Phase != PhaseClosed {
		if accountEntry.state.DispatchRevision != input.DispatchRevision {
			return escalationResult(EscalationStaleRevision, accountEntry.state, int64(len(evidence.scopes)), failureTotal), nil
		}
		relatedStates, err := s.attachAccountShadowLocked(accountEntry, childScopeKeys, childIncidentIDs, input.AccountTransitionID, now)
		if err != nil {
			return EscalationResult{}, err
		}
		return escalationResult(EscalationAlreadyActive, accountEntry.state, int64(len(evidence.scopes)), failureTotal, relatedStates...), nil
	}
	if accountEntry == nil && !s.reserveCapacityLocked(now) {
		return escalationResult(EscalationCapacityExceeded,
			CapacityExhaustedState(accountScope, input.DispatchRevision, now), int64(len(evidence.scopes)), failureTotal), nil
	}
	target := accountEntry
	if target == nil {
		target = s.newEntryLocked(closedAccountState)
	}
	accountIncidentID, err := requiredValue(input.AccountTransitionID, "accountTransitionId")
	if err != nil {
		return EscalationResult{}, err
	}
	reason, err := requiredValue(input.Reason, "reason")
	if err != nil {
		return EscalationResult{}, err
	}
	accountState := closedAccountState
	accountState.Phase = PhaseOpen
	accountState.Generation = target.state.Generation + 1
	accountState.DispatchRevision = input.DispatchRevision
	accountState.TransitionID = accountIncidentID
	accountState.IncidentID = &accountIncidentID
	accountState.BackoffAttempt = 1
	openedAt := now
	accountState.OpenedAtMs = &openedAt
	retryAt := now + s.settings.accountCircuitBackoffDelayMs(1,
		fmt.Sprintf("%s:%d:%d", closedAccountState.ScopeKey, target.state.Generation+1, 1), s.random)
	accountState.RetryAtMs = &retryAt
	accountState.FailureReason = &reason
	accountState.ChildScopeKeys = stringList(childScopeKeys)
	accountState.ChildIncidentIDs = stringList(childIncidentIDs)
	accountState.RequiredRecoveryScopeKeys = stringList(append([]string{}, childScopeKeys...))
	accountState.RecoveryEvidenceScopeKeys = stringList{}
	accountState.UpdatedAtMs = now
	if _, err := s.applyLocked(target, accountState, accountIncidentID, nil); err != nil {
		return EscalationResult{}, err
	}
	relatedStates, err := s.shadowChildrenLocked(childScopeKeys, childIncidentIDs, accountIncidentID, input.DispatchRevision, accountIncidentID, now)
	if err != nil {
		return EscalationResult{}, err
	}
	return escalationResult(EscalationEscalated, accountState, int64(len(evidence.scopes)), failureTotal, relatedStates...), nil
}

// ClearAccountEscalationEvidence mirrors store.clearAccountEscalationEvidence.
func (s *MemoryStore) ClearAccountEscalationEvidence(_ context.Context, input ClearAccountEscalationEvidenceInput) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolveNow(input.NowMs)
	if _, err := requiredValue(input.EvidenceID, "evidenceId"); err != nil {
		return false, err
	}
	accountRuntimeKey, err := requiredValue(input.AccountRuntimeKey, "accountRuntimeKey")
	if err != nil {
		return false, err
	}
	dispatchRevision, err := requiredValue(input.DispatchRevision, "dispatchRevision")
	if err != nil {
		return false, err
	}
	evidence, ok := s.escalationEvidence[accountRuntimeKey]
	if !ok || evidence.dispatchRevision != dispatchRevision {
		return false, nil
	}
	delete(s.escalationEvidence, accountRuntimeKey)
	return true, nil
}

// ReplaceDispatchRevision mirrors store.replaceDispatchRevision.
func (s *MemoryStore) ReplaceDispatchRevision(_ context.Context, input ReplaceDispatchRevisionInput) (MutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.resolveNow(input.NowMs)
	entry, err := s.freshEntryLocked(input.Scope, now)
	if err != nil {
		return MutationResult{}, err
	}
	if entry != nil {
		if replay := s.idempotentLocked(entry, input.TransitionID); replay != nil {
			return *replay, nil
		}
	}
	if entry != nil && entry.state.DispatchRevision == input.DispatchRevision {
		return mutationResult(MutationIdempotent, entry.state), nil
	}
	if entry != nil && olderNumericDispatchRevision(input.DispatchRevision, entry.state.DispatchRevision) {
		return mutationResult(MutationStaleDispatchRevision, entry.state), nil
	}
	delete(s.escalationEvidence, input.Scope.AccountRuntimeKey)
	if entry == nil && !s.reserveCapacityLocked(now) {
		return mutationResult(MutationCapacityExhausted, CapacityExhaustedState(input.Scope, input.DispatchRevision, now)), nil
	}
	target := entry
	if target == nil {
		target = s.newEntryLocked(ClosedState(input.Scope, "", 0, "", 0))
	}
	dispatchRevision, err := requiredValue(input.DispatchRevision, "dispatchRevision")
	if err != nil {
		return MutationResult{}, err
	}
	transitionID, err := requiredValue(input.TransitionID, "transitionId")
	if err != nil {
		return MutationResult{}, err
	}
	closedExpiresAt := now + s.closedRetentionMs
	return s.applyLocked(target, ClosedState(input.Scope, dispatchRevision, target.state.Generation+1, transitionID, now), input.TransitionID, &closedExpiresAt)
}

// Restore mirrors store.restore.
func (s *MemoryStore) Restore(_ context.Context, rawState State, nowMs *int64) (MutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.resolveNow(nowMs)
	state, err := normalizeConfirmationState(CloneState(rawState))
	if err != nil {
		return MutationResult{}, err
	}
	if err := AssertStateScopeKey(state); err != nil {
		return MutationResult{}, err
	}
	existing, err := s.freshEntryLocked(state.Scope, now)
	if err != nil {
		return MutationResult{}, err
	}
	if existing != nil && olderNumericDispatchRevision(state.DispatchRevision, existing.state.DispatchRevision) {
		return mutationResult(MutationStaleDispatchRevision, existing.state), nil
	}
	if existing != nil && (existing.state.Generation > state.Generation ||
		(existing.state.Generation == state.Generation && existing.state.UpdatedAtMs >= state.UpdatedAtMs)) {
		relatedStates := s.projectParentRelationshipLocked(existing.state)
		return mutationResult(MutationIdempotent, existing.state, relatedStates...), nil
	}
	if existing == nil && !s.reserveCapacityLocked(now) {
		return mutationResult(MutationCapacityExhausted, CapacityExhaustedState(state.Scope, state.DispatchRevision, now)), nil
	}
	entry := existing
	if entry == nil {
		entry = s.newEntryLocked(state)
	}
	entry.state = state
	if state.Phase == PhaseClosed {
		expiresAt := now + s.closedRetentionMs
		entry.closedExpiresAtMs = &expiresAt
	} else {
		entry.closedExpiresAtMs = nil
	}
	if _, ok := entry.replayIDs[state.TransitionID]; !ok {
		entry.replayIDs[state.TransitionID] = struct{}{}
		entry.replayOrder = append(entry.replayOrder, state.TransitionID)
	}
	s.setEntryLocked(state.ScopeKey, entry)
	relatedStates := s.projectParentRelationshipLocked(state)
	return mutationResult(MutationApplied, state, relatedStates...), nil
}

// ReplaceAccountDispatchRevision mirrors
// store.replaceAccountDispatchRevision.
func (s *MemoryStore) ReplaceAccountDispatchRevision(_ context.Context, input ReplaceAccountDispatchRevisionInput) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.resolveNow(input.NowMs)
	for runtimeKey, evidence := range s.escalationEvidence {
		if runtimeKeyMatchesDispatchRevisionTarget(runtimeKey, input.AccountRuntimeKey) &&
			evidence.dispatchRevision != input.DispatchRevision &&
			!olderNumericDispatchRevision(input.DispatchRevision, evidence.dispatchRevision) {
			delete(s.escalationEvidence, runtimeKey)
		}
	}
	var changed int64
	for _, scopeKey := range append([]string(nil), s.order...) {
		entry, ok := s.entries[scopeKey]
		if !ok {
			continue
		}
		if !runtimeKeyMatchesDispatchRevisionTarget(entry.state.Scope.AccountRuntimeKey, input.AccountRuntimeKey) {
			continue
		}
		if entry.state.DispatchRevision == input.DispatchRevision {
			continue
		}
		if olderNumericDispatchRevision(input.DispatchRevision, entry.state.DispatchRevision) {
			continue
		}
		entry.state = ClosedState(entry.state.Scope, input.DispatchRevision, entry.state.Generation+1, input.TransitionID, now)
		expiresAt := now + s.closedRetentionMs
		entry.closedExpiresAtMs = &expiresAt
		entry.replayIDs[input.TransitionID] = struct{}{}
		entry.replayOrder = append(entry.replayOrder, input.TransitionID)
		changed++
	}
	return changed, nil
}

// ListDue mirrors store.listDue.
func (s *MemoryStore) ListDue(_ context.Context, nowMs int64, limit int) ([]State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := normalizedNowValue(&nowMs, s.now)
	s.cleanupLocked(now)
	normalizedLimit, err := positiveInteger(int64(limit), "limit")
	if err != nil {
		return nil, err
	}
	type dueEntry struct {
		entry *memoryEntry
		due   int64
	}
	dueEntries := make([]dueEntry, 0, len(s.entries))
	for _, key := range append([]string(nil), s.order...) {
		entry, ok := s.entries[key]
		if !ok {
			continue
		}
		dueAt := accountCircuitDueAtMs(entry.state)
		if dueAt <= now {
			dueEntries = append(dueEntries, dueEntry{entry: entry, due: dueAt})
		}
	}
	sort.SliceStable(dueEntries, func(left, right int) bool {
		return dueEntries[left].due < dueEntries[right].due
	})
	states := make([]State, 0, normalizedLimit)
	for _, item := range dueEntries {
		if int64(len(states)) >= normalizedLimit {
			break
		}
		states = append(states, CloneState(item.entry.state))
	}
	return states, nil
}

// Size mirrors store.size.
func (s *MemoryStore) Size(_ context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(s.now())
	return int64(len(s.entries)), nil
}

func (s *MemoryStore) resolveNow(nowMs *int64) int64 {
	return normalizedNowValue(nowMs, s.now)
}

func (s *MemoryStore) getLocked(scope Scope, now int64) (State, error) {
	entry, err := s.freshEntryLocked(scope, now)
	if err != nil {
		return State{}, err
	}
	if entry != nil {
		return CloneState(entry.state), nil
	}
	if s.capacitySaturated && s.reserveCapacityLocked(now) {
		return ClosedState(scope, "", 0, "", 0), nil
	}
	if s.capacitySaturated {
		return CapacityExhaustedState(scope, "", now), nil
	}
	return ClosedState(scope, "", 0, "", 0), nil
}

func (s *MemoryStore) checkedSuspectClosureLocked(
	scope Scope, generation int64, dispatchRevision, transitionID, expectedFailureEvidenceKey string, nowMs *int64,
) (*memoryEntry, int64, *MutationResult, error) {
	now := s.resolveNow(nowMs)
	entry, err := s.freshEntryLocked(scope, now)
	if err != nil {
		return nil, 0, nil, err
	}
	if invalid := s.validateIdentityLocked(entry, scope, generation, dispatchRevision); invalid != nil {
		return nil, 0, invalid, nil
	}
	if entry == nil {
		result := mutationResult(MutationNotFound, ClosedState(scope, "", 0, "", 0))
		return nil, 0, &result, nil
	}
	if replay := s.idempotentLocked(entry, transitionID); replay != nil {
		return nil, 0, replay, nil
	}
	if entry.state.Phase != PhaseSuspect || entry.state.ShadowedByIncidentID != nil {
		result := mutationResult(MutationStateMismatch, entry.state)
		return nil, 0, &result, nil
	}
	expectedEvidence, err := NormalizeFailureEvidenceKey(strPtr(expectedFailureEvidenceKey), "suspect-close:"+transitionID)
	if err != nil {
		return nil, 0, nil, err
	}
	last, ok, err := LastFailureEvidenceKey(entry.state)
	if err != nil {
		return nil, 0, nil, err
	}
	if !ok || last != expectedEvidence {
		result := mutationResult(MutationStateMismatch, entry.state)
		return nil, 0, &result, nil
	}
	return entry, now, nil, nil
}

func (s *MemoryStore) checkedEntryLocked(
	scope Scope, generation int64, dispatchRevision, transitionID, leaseID string, nowMs *int64,
) (*memoryEntry, int64, *MutationResult, error) {
	now := s.resolveNow(nowMs)
	entry, err := s.freshEntryLocked(scope, now)
	if err != nil {
		return nil, 0, nil, err
	}
	if invalid := s.validateIdentityLocked(entry, scope, generation, dispatchRevision); invalid != nil {
		return nil, 0, invalid, nil
	}
	if entry == nil {
		result := mutationResult(MutationNotFound, ClosedState(scope, "", 0, "", 0))
		return nil, 0, &result, nil
	}
	if replay := s.idempotentLocked(entry, transitionID); replay != nil {
		return nil, 0, replay, nil
	}
	if entry.state.Phase != PhaseSuspect {
		result := mutationResult(MutationStateMismatch, entry.state)
		return nil, 0, &result, nil
	}
	if entry.state.Lease == nil || entry.state.Lease.Kind != LeaseKindConfirmation || entry.state.Lease.LeaseID != leaseID {
		result := mutationResult(MutationLeaseMismatch, entry.state)
		return nil, 0, &result, nil
	}
	return entry, now, nil, nil
}

func (s *MemoryStore) validateIdentityLocked(entry *memoryEntry, scope Scope, generation int64, dispatchRevision string) *MutationResult {
	if entry == nil {
		result := mutationResult(MutationNotFound, ClosedState(scope, "", 0, "", 0))
		return &result
	}
	if entry.state.Generation != generation {
		result := mutationResult(MutationStaleGeneration, entry.state)
		return &result
	}
	if entry.state.DispatchRevision != dispatchRevision {
		result := mutationResult(MutationStaleDispatchRevision, entry.state)
		return &result
	}
	return nil
}

func (s *MemoryStore) openLocked(entry *memoryEntry, transitionID string, now int64, reason *string) (MutationResult, error) {
	backoffAttempt := entry.state.BackoffAttempt + 1
	next := entry.state
	next.Phase = PhaseOpen
	next.TransitionID = transitionID
	next.BackoffAttempt = backoffAttempt
	next.RecoverySuccessCount = 0
	next.RecoveryEvidenceScopeKeys = stringList{}
	if entry.state.IncidentID != nil {
		next.IncidentID = entry.state.IncidentID
	} else {
		next.IncidentID = strPtr(transitionID)
	}
	openedAt := now
	next.OpenedAtMs = &openedAt
	retryAt := now + s.settings.accountCircuitBackoffDelayMs(backoffAttempt,
		fmt.Sprintf("%s:%d:%d", entry.state.ScopeKey, entry.state.Generation, backoffAttempt), s.random)
	next.RetryAtMs = &retryAt
	if reason != nil {
		next.FailureReason = reason
	} else {
		next.FailureReason = entry.state.FailureReason
	}
	next.Lease = nil
	next.HalfOpenOrigin = nil
	next.UpdatedAtMs = now
	return s.applyLocked(entry, next, transitionID, nil)
}

func (s *MemoryStore) enterRecoveringLocked(entry *memoryEntry, transitionID string, now int64) (MutationResult, error) {
	backoffAttempt := entry.state.BackoffAttempt
	if entry.state.Phase == PhaseSuspect {
		backoffAttempt = 0
	}
	next := entry.state
	next.Phase = PhaseRecovering
	next.TransitionID = transitionID
	next.BackoffAttempt = backoffAttempt
	next.RecoverySuccessCount = 0
	next.RecoveryEvidenceScopeKeys = stringList{}
	next.ConfirmationFailureCount = int64Ptr(0)
	next.FailureEvidenceKeys = stringList{}
	next.FailureReason = nil
	next.Lease = nil
	next.HalfOpenOrigin = nil
	retryAt := now + s.settings.AccountCircuitRecoveryCanaryIntervalMs
	next.RetryAtMs = &retryAt
	next.UpdatedAtMs = now
	return s.applyLocked(entry, next, transitionID, nil)
}

func (s *MemoryStore) closeLocked(entry *memoryEntry, transitionID string, now int64) (MutationResult, error) {
	var childScopeKeys []string
	var childIncidentIDs []string
	if entry.state.ChildScopeKeys != nil {
		childScopeKeys = append([]string{}, entry.state.ChildScopeKeys...)
	}
	if entry.state.ChildIncidentIDs != nil {
		childIncidentIDs = append([]string{}, entry.state.ChildIncidentIDs...)
	}
	incidentID := entry.state.IncidentID
	isAccountScope := entry.state.Scope.Kind == ScopeKindAccount
	if isAccountScope {
		delete(s.escalationEvidence, entry.state.Scope.AccountRuntimeKey)
	}
	closed := ClosedState(entry.state.Scope, entry.state.DispatchRevision, entry.state.Generation, transitionID, now)
	closed.IncidentID = incidentID
	if isAccountScope && incidentID != nil && len(childScopeKeys) > 0 {
		closed.ChildScopeKeys = stringList(append([]string{}, childScopeKeys...))
		closed.ChildIncidentIDs = stringList(append([]string{}, childIncidentIDs...))
	}
	expiresAt := now + s.closedRetentionMs
	closedResult, err := s.applyLocked(entry, closed, transitionID, &expiresAt)
	if err != nil {
		return MutationResult{}, err
	}
	var relatedStates []State
	if incidentID != nil {
		relatedStates, err = s.unshadowChildrenLocked(childScopeKeys, childIncidentIDs, *incidentID, transitionID, now)
		if err != nil {
			return MutationResult{}, err
		}
	}
	return mutationResult(closedResult.Status, closedResult.State, relatedStates...), nil
}

// nextRecoveryEvidenceScopeKeys mirrors the memory store helper of the same
// name.
func nextRecoveryEvidenceScopeKeys(state State, evidenceScopeKey *string) []string {
	current := []string(state.RecoveryEvidenceScopeKeys)
	if state.Scope.Kind != ScopeKindAccount {
		return current
	}
	requiredScopeKeys := []string(state.RequiredRecoveryScopeKeys)
	if len(requiredScopeKeys) == 0 {
		return current
	}
	normalized := ""
	if evidenceScopeKey != nil {
		normalized = strings.TrimSpace(*evidenceScopeKey)
	}
	if normalized == "" || !containsString(requiredScopeKeys, normalized) {
		return current
	}
	if !containsString(current, normalized) {
		current = append(append([]string{}, current...), normalized)
	}
	return current
}

func (s *MemoryStore) attachAccountShadowLocked(
	entry *memoryEntry, childScopeKeys, childIncidentIDs []string, transitionID string, now int64,
) ([]State, error) {
	incidentID := entry.state.TransitionID
	if entry.state.IncidentID != nil {
		incidentID = *entry.state.IncidentID
	}
	nextScopeKeys := append([]string{}, entry.state.ChildScopeKeys...)
	nextIncidentIDs := append([]string{}, entry.state.ChildIncidentIDs...)
	relationshipChanged := entry.state.IncidentID == nil || *entry.state.IncidentID != incidentID
	for index, childScopeKey := range childScopeKeys {
		if index >= len(childIncidentIDs) {
			continue
		}
		childIncidentID := childIncidentIDs[index]
		if childIncidentID == "" {
			continue
		}
		existingIndex := -1
		for i, key := range nextScopeKeys {
			if key == childScopeKey {
				existingIndex = i
				break
			}
		}
		if existingIndex < 0 {
			nextScopeKeys = append(nextScopeKeys, childScopeKey)
			nextIncidentIDs = append(nextIncidentIDs, childIncidentID)
			relationshipChanged = true
		} else if nextIncidentIDs[existingIndex] != childIncidentID {
			nextIncidentIDs[existingIndex] = childIncidentID
			relationshipChanged = true
		}
	}
	if relationshipChanged {
		next := entry.state
		next.TransitionID = transitionID
		incident := incidentID
		next.IncidentID = &incident
		next.ChildScopeKeys = stringList(nextScopeKeys)
		next.ChildIncidentIDs = stringList(nextIncidentIDs)
		next.RequiredRecoveryScopeKeys = stringList(mergeUnique(entry.state.RequiredRecoveryScopeKeys, nextScopeKeys))
		next.UpdatedAtMs = now
		entry.state = next
		s.rememberReplayLocked(entry, transitionID)
	}
	return s.shadowChildrenLocked(childScopeKeys, childIncidentIDs, incidentID, entry.state.DispatchRevision, transitionID, now)
}

func (s *MemoryStore) shadowChildrenLocked(
	scopeKeys, childIncidentIDs []string, parentIncidentID, dispatchRevision, parentTransitionID string, now int64,
) ([]State, error) {
	var relatedStates []State
	for index, scopeKey := range scopeKeys {
		child := s.entries[scopeKey]
		if child == nil || child.state.Phase == PhaseClosed || child.state.DispatchRevision != dispatchRevision {
			continue
		}
		childIncidentID := ""
		if index < len(childIncidentIDs) {
			childIncidentID = childIncidentIDs[index]
		}
		currentIncidentID := scopeKey + "@" + strconv.FormatInt(child.state.Generation, 10)
		if child.state.IncidentID != nil {
			currentIncidentID = *child.state.IncidentID
		}
		if childIncidentID == "" || currentIncidentID != childIncidentID || child.state.ShadowedByIncidentID != nil {
			continue
		}
		transitionID, err := HierarchyTransitionID("shadow", parentTransitionID, parentIncidentID, scopeKey, child.state.Generation)
		if err != nil {
			return nil, err
		}
		next := child.state
		next.TransitionID = transitionID
		next.ShadowedByIncidentID = strPtr(parentIncidentID)
		next.UpdatedAtMs = now
		child.state = next
		s.rememberReplayLocked(child, transitionID)
		relatedStates = append(relatedStates, CloneState(child.state))
	}
	return relatedStates, nil
}

func (s *MemoryStore) unshadowChildrenLocked(
	scopeKeys, childIncidentIDs []string, parentIncidentID, parentTransitionID string, now int64,
) ([]State, error) {
	var relatedStates []State
	for index, scopeKey := range scopeKeys {
		child := s.entries[scopeKey]
		if child == nil || child.state.ShadowedByIncidentID == nil || *child.state.ShadowedByIncidentID != parentIncidentID {
			continue
		}
		childIncidentID := ""
		if index < len(childIncidentIDs) {
			childIncidentID = childIncidentIDs[index]
		}
		currentIncidentID := scopeKey + "@" + strconv.FormatInt(child.state.Generation, 10)
		if child.state.IncidentID != nil {
			currentIncidentID = *child.state.IncidentID
		}
		if childIncidentID == "" || currentIncidentID != childIncidentID {
			continue
		}
		transitionID, err := HierarchyTransitionID("unshadow", parentTransitionID, parentIncidentID, scopeKey, child.state.Generation)
		if err != nil {
			return nil, err
		}
		next := child.state
		next.TransitionID = transitionID
		next.ShadowedByIncidentID = nil
		next.UpdatedAtMs = now
		child.state = next
		s.rememberReplayLocked(child, transitionID)
		relatedStates = append(relatedStates, CloneState(child.state))
	}
	return relatedStates, nil
}

func (s *MemoryStore) projectParentRelationshipLocked(parentState State) []State {
	if parentState.Scope.Kind != ScopeKindAccount || parentState.IncidentID == nil {
		return nil
	}
	childScopeKeys := []string(parentState.ChildScopeKeys)
	childIncidentIDs := []string(parentState.ChildIncidentIDs)
	var relatedStates []State
	for index, scopeKey := range childScopeKeys {
		child := s.entries[scopeKey]
		if child == nil || child.state.DispatchRevision != parentState.DispatchRevision {
			continue
		}
		childIncidentID := ""
		if index < len(childIncidentIDs) {
			childIncidentID = childIncidentIDs[index]
		}
		currentIncidentID := scopeKey + "@" + strconv.FormatInt(child.state.Generation, 10)
		if child.state.IncidentID != nil {
			currentIncidentID = *child.state.IncidentID
		}
		if childIncidentID == "" || currentIncidentID != childIncidentID || child.state.UpdatedAtMs > parentState.UpdatedAtMs {
			continue
		}
		if parentState.Phase == PhaseClosed {
			if child.state.ShadowedByIncidentID != nil && *child.state.ShadowedByIncidentID == *parentState.IncidentID {
				transitionID, err := HierarchyTransitionID("unshadow", parentState.TransitionID, *parentState.IncidentID, scopeKey, child.state.Generation)
				if err != nil {
					continue
				}
				next := child.state
				next.TransitionID = transitionID
				next.ShadowedByIncidentID = nil
				next.UpdatedAtMs = parentState.UpdatedAtMs
				child.state = next
				s.rememberReplayLocked(child, transitionID)
				relatedStates = append(relatedStates, CloneState(child.state))
			}
			continue
		}
		if child.state.Phase != PhaseClosed && child.state.ShadowedByIncidentID == nil {
			transitionID, err := HierarchyTransitionID("shadow", parentState.TransitionID, *parentState.IncidentID, scopeKey, child.state.Generation)
			if err != nil {
				continue
			}
			next := child.state
			next.TransitionID = transitionID
			next.ShadowedByIncidentID = strPtr(*parentState.IncidentID)
			next.UpdatedAtMs = parentState.UpdatedAtMs
			child.state = next
			s.rememberReplayLocked(child, transitionID)
			relatedStates = append(relatedStates, CloneState(child.state))
		}
	}
	return relatedStates
}

func (s *MemoryStore) restoreCanaryOriginLocked(entry *memoryEntry, transitionID string, now int64) (MutationResult, error) {
	origin := PhaseOpen
	if entry.state.HalfOpenOrigin != nil {
		origin = *entry.state.HalfOpenOrigin
	}
	backoffAttempt := entry.state.BackoffAttempt + 1
	next := entry.state
	next.Phase = origin
	next.TransitionID = transitionID
	next.BackoffAttempt = backoffAttempt
	next.Lease = nil
	next.HalfOpenOrigin = nil
	retryAt := now + s.settings.accountCircuitBackoffDelayMs(backoffAttempt,
		fmt.Sprintf("%s:%d:%d:unknown", entry.state.ScopeKey, entry.state.Generation, backoffAttempt), s.random)
	next.RetryAtMs = &retryAt
	next.UpdatedAtMs = now
	return s.applyLocked(entry, next, transitionID, nil)
}

func (s *MemoryStore) freshEntryLocked(scope Scope, now int64) (*memoryEntry, error) {
	entry, ok := s.entries[MustScopeKey(scope)]
	if !ok {
		return nil, nil
	}
	if entry.state.Phase == PhaseClosed && (entry.closedExpiresAtMs == nil || *entry.closedExpiresAtMs <= now) {
		s.removeEntryLocked(entry.state.ScopeKey)
		return nil, nil
	}
	s.normalizeExpiredLeaseLocked(entry, now)
	normalized, err := normalizeConfirmationState(entry.state)
	if err != nil {
		return nil, err
	}
	entry.state = normalized
	return entry, nil
}

func (s *MemoryStore) normalizeExpiredLeaseLocked(entry *memoryEntry, now int64) {
	lease := entry.state.Lease
	if lease == nil || lease.LeaseUntilMs > now {
		return
	}
	if lease.Kind == LeaseKindConfirmation {
		next := entry.state
		next.Lease = nil
		retryAt := now
		next.RetryAtMs = &retryAt
		next.UpdatedAtMs = now
		entry.state = next
		return
	}
	origin := PhaseOpen
	if entry.state.HalfOpenOrigin != nil {
		origin = *entry.state.HalfOpenOrigin
	}
	next := entry.state
	next.Phase = origin
	next.Lease = nil
	next.HalfOpenOrigin = nil
	retryAt := now
	next.RetryAtMs = &retryAt
	next.UpdatedAtMs = now
	entry.state = next
}

func (s *MemoryStore) reserveCapacityLocked(now int64) bool {
	s.cleanupLocked(now)
	if int64(len(s.entries)) < s.capacity {
		s.capacitySaturated = false
		return true
	}
	var oldestClosed *memoryEntry
	var oldestClosedKey string
	for _, key := range append([]string(nil), s.order...) {
		entry, ok := s.entries[key]
		if !ok || entry.state.Phase != PhaseClosed {
			continue
		}
		if oldestClosed == nil || entry.state.UpdatedAtMs < oldestClosed.state.UpdatedAtMs {
			oldestClosed = entry
			oldestClosedKey = key
		}
	}
	if oldestClosed == nil {
		s.capacitySaturated = true
		return false
	}
	s.removeEntryLocked(oldestClosedKey)
	s.capacitySaturated = false
	return true
}

func (s *MemoryStore) cleanupLocked(now int64) {
	for _, key := range append([]string(nil), s.order...) {
		entry, ok := s.entries[key]
		if !ok {
			continue
		}
		if entry.state.Phase == PhaseClosed && (entry.closedExpiresAtMs == nil || *entry.closedExpiresAtMs <= now) {
			s.removeEntryLocked(key)
		} else {
			s.normalizeExpiredLeaseLocked(entry, now)
		}
	}
	if int64(len(s.entries)) < s.capacity {
		s.capacitySaturated = false
	}
}

func (s *MemoryStore) setEntryLocked(scopeKey string, entry *memoryEntry) {
	if _, ok := s.entries[scopeKey]; !ok {
		s.order = append(s.order, scopeKey)
	}
	s.entries[scopeKey] = entry
}

func (s *MemoryStore) removeEntryLocked(scopeKey string) {
	delete(s.entries, scopeKey)
	for index, key := range s.order {
		if key == scopeKey {
			s.order = append(s.order[:index], s.order[index+1:]...)
			break
		}
	}
}

func (s *MemoryStore) newEntryLocked(state State) *memoryEntry {
	entry := &memoryEntry{state: state, replayIDs: map[string]struct{}{}}
	s.setEntryLocked(state.ScopeKey, entry)
	return entry
}

func (s *MemoryStore) applyLocked(entry *memoryEntry, state State, transitionID string, closedExpiresAtMs *int64) (MutationResult, error) {
	if _, err := requiredValue(transitionID, "transitionId"); err != nil {
		return MutationResult{}, err
	}
	entry.state = state
	entry.closedExpiresAtMs = closedExpiresAtMs
	s.rememberReplayLocked(entry, transitionID)
	return mutationResult(MutationApplied, state), nil
}

func (s *MemoryStore) idempotentLocked(entry *memoryEntry, transitionID string) *MutationResult {
	if entry == nil {
		return nil
	}
	if _, ok := entry.replayIDs[transitionID]; ok {
		result := mutationResult(MutationIdempotent, entry.state)
		return &result
	}
	return nil
}

func (s *MemoryStore) rememberReplayLocked(entry *memoryEntry, transitionID string) {
	if _, ok := entry.replayIDs[transitionID]; ok {
		return
	}
	entry.replayIDs[transitionID] = struct{}{}
	entry.replayOrder = append(entry.replayOrder, transitionID)
	for int64(len(entry.replayOrder)) > s.replayLimitPerScope {
		oldest := entry.replayOrder[0]
		entry.replayOrder = entry.replayOrder[1:]
		delete(entry.replayIDs, oldest)
	}
}

func runtimeKeyMatchesDispatchRevisionTarget(runtimeKey, target string) bool {
	if runtimeKey == target {
		return true
	}
	return !strings.Contains(target, ":authorized:") && strings.HasPrefix(runtimeKey, target+":authorized:")
}

func accountCircuitDueAtMs(state State) int64 {
	if state.Phase == PhaseClosed {
		return math.MaxInt64
	}
	if state.Lease != nil {
		return state.Lease.LeaseUntilMs
	}
	if state.Phase == PhaseSuspect || state.Phase == PhaseOpen || state.Phase == PhaseRecovering {
		if state.RetryAtMs != nil {
			return *state.RetryAtMs
		}
		return math.MaxInt64
	}
	return math.MaxInt64
}

func normalizeConfirmationState(state State) (State, error) {
	if state.Phase == PhaseClosed {
		return state, nil
	}
	required, err := NormalizeConfirmationFailuresRequired(state.ConfirmationFailuresRequired, LegacyConfirmationFailuresRequired)
	if err != nil {
		return State{}, err
	}
	count, err := ConfirmationFailureCountOf(state)
	if err != nil {
		return State{}, err
	}
	evidenceKeys, err := FailureEvidenceKeysOf(state)
	if err != nil {
		return State{}, err
	}
	next := state
	next.ConfirmationFailuresRequired = &required
	next.ConfirmationFailureCount = &count
	next.FailureEvidenceKeys = stringList(evidenceKeys)
	if state.Phase == PhaseSuspect && state.RetryAtMs == nil {
		retryAt := state.UpdatedAtMs
		if state.Lease != nil {
			retryAt = state.Lease.LeaseUntilMs
		}
		next.RetryAtMs = &retryAt
	}
	return next, nil
}

func memoryLease(kind, leaseID string, leaseUntilMs, now int64) (*Lease, error) {
	normalizedLeaseID, err := requiredValue(leaseID, "leaseId")
	if err != nil {
		return nil, err
	}
	if leaseUntilMs <= now {
		return nil, errors.New("账户电路租约截止时间必须晚于当前时间")
	}
	return &Lease{Kind: kind, LeaseID: normalizedLeaseID, LeaseUntilMs: leaseUntilMs}, nil
}

func mutationResult(status string, state State, relatedStates ...State) MutationResult {
	result := MutationResult{Status: status, State: CloneState(state)}
	if len(relatedStates) > 0 {
		related := make(stateList, 0, len(relatedStates))
		for _, item := range relatedStates {
			related = append(related, CloneState(item))
		}
		result.RelatedStates = related
	}
	return result
}

func escalationResult(status string, accountState State, protocolScopeCount, confirmedFailureCount int64, relatedStates ...State) EscalationResult {
	result := EscalationResult{
		Status:                status,
		AccountState:          CloneState(accountState),
		ProtocolScopeCount:    protocolScopeCount,
		ConfirmedFailureCount: confirmedFailureCount,
	}
	if len(relatedStates) > 0 {
		related := make(stateList, 0, len(relatedStates))
		for _, item := range relatedStates {
			related = append(related, CloneState(item))
		}
		result.RelatedStates = related
	}
	return result
}

func totalConfirmedFailures(scopes []memoryEscalationScopeEvidence) int64 {
	var total int64
	for _, item := range scopes {
		total += item.confirmedFailureCount
	}
	return total
}

func requiredValue(value, name string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", fmt.Errorf("账户电路操作缺少 %s", name)
	}
	return normalized, nil
}

// normalizedNowValue mirrors normalizedNow: clamps negatives to zero (the
// finite-number check from Node is unrepresentable for int64 inputs).
func normalizedNowValue(nowMs *int64, fallback func() int64) int64 {
	value := int64(0)
	if nowMs != nil {
		value = *nowMs
	} else if fallback != nil {
		value = fallback()
	}
	if value < 0 {
		return 0
	}
	return value
}

func positiveInteger(value int64, name string) (int64, error) {
	if value < 1 {
		return 0, fmt.Errorf("账户电路 %s 必须是正整数", name)
	}
	return value, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func mergeUnique(current []string, additions []string) []string {
	out := append([]string{}, current...)
	for _, value := range additions {
		if !containsString(out, value) {
			out = append(out, value)
		}
	}
	return out
}

func strPtr(value string) *string { return &value }

func int64Ptr(value int64) *int64 { return &value }
