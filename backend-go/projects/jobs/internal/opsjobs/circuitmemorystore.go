package opsjobs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// MemoryCircuitStore 是 Node MemoryAccountCircuitStore 的 Go 对齐实现，覆盖
// 恢复扫描与控制面对账所需的操作子集：租约语义、乐观围栏、transitionId 幂等
// 重放、CLOSED 保留期、过期租约归一化与到期索引。仅用于测试与单机模式；
// 生产部署使用 Redis/DB store 实现同一 CircuitStore port。

const memoryCircuitDefaultClosedRetentionMS = 5 * 60_000

// CircuitRecoverySuccessThreshold 与 CircuitRecoveryCanaryIntervalMS 对齐 Node
// 默认配置（JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_RECOVERY_SUCCESS_THRESHOLD /
// _CANARY_INTERVAL_MS）。确认间隔共用 3s 默认值。
const (
	CircuitRecoverySuccessThreshold      = 3
	CircuitRecoveryCanaryIntervalMS      = 3_000
	circuitSuspectConfirmationIntervalMS = 3_000
)

type memoryCircuitEntry struct {
	state             CircuitState
	closedExpiresAtMS int64
	closedExpiresSet  bool
	replayOrder       []string
	replayIDs         map[string]struct{}
}

type MemoryCircuitStore struct {
	mu                  sync.Mutex
	capacity            int
	closedRetentionMS   int64
	replayLimitPerScope int
	now                 func() int64
	entries             map[string]*memoryCircuitEntry
	capacitySaturated   bool
}

func NewMemoryCircuitStore(capacity int, now func() int64) (*MemoryCircuitStore, error) {
	if capacity < 1 {
		return nil, fmt.Errorf("账户电路 capacity 必须是正整数")
	}
	if now == nil {
		now = func() int64 { return 0 }
	}
	return &MemoryCircuitStore{
		capacity:            capacity,
		closedRetentionMS:   memoryCircuitDefaultClosedRetentionMS,
		replayLimitPerScope: 64,
		now:                 now,
		entries:             map[string]*memoryCircuitEntry{},
	}, nil
}

func (s *MemoryCircuitStore) Get(_ context.Context, scope CircuitScope, nowMS int64) (CircuitState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.freshEntry(scope, nowMS); entry != nil {
		return cloneCircuitState(entry.state), nil
	}
	return closedAccountCircuitState(scope, "", 0, "", 0), nil
}

func (s *MemoryCircuitStore) AcquireConfirmationLease(_ context.Context, identity CircuitTransitionIdentity, lease CircuitLeaseSpec) (CircuitMutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquireLease(identity, lease, CircuitPhaseSuspect, CircuitLeaseConfirmation)
}

func (s *MemoryCircuitStore) AcquireCanaryLease(_ context.Context, identity CircuitTransitionIdentity, lease CircuitLeaseSpec) (CircuitMutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := normalizeClockMS(identity.NowMS)
	entry := s.freshEntry(identity.Scope, now)
	if invalid := s.validateIdentity(entry, identity); invalid != nil {
		return *invalid, nil
	}
	if entry == nil {
		return notFoundResult(identity.Scope), nil
	}
	if replay := s.idempotentResult(entry, identity.TransitionID); replay != nil {
		return *replay, nil
	}
	if entry.state.Phase != CircuitPhaseOpen && entry.state.Phase != CircuitPhaseRecovering {
		return stateMismatchResult(entry.state), nil
	}
	if entry.state.Lease != nil {
		return stateMismatchResult(entry.state), nil
	}
	if retryAtMS(entry.state) > now {
		return circuitMutationResult(CircuitMutationNotDue, entry.state, nil), nil
	}
	origin := entry.state.Phase
	kind := CircuitLeaseHalfOpen
	if origin == CircuitPhaseRecovering {
		kind = CircuitLeaseRecovery
	}
	leaseState, err := s.withLease(entry.state, kind, lease, now)
	if err != nil {
		return CircuitMutationResult{}, err
	}
	leaseState.Phase = CircuitPhaseHalfOpen
	leaseState.TransitionID = identity.TransitionID
	leaseState.HalfOpenOrigin = string(origin)
	return s.apply(entry, leaseState, identity.TransitionID, 0, false), nil
}

func (s *MemoryCircuitStore) CompleteConfirmation(_ context.Context, identity CircuitTransitionIdentity, leaseID string, completion CircuitCompletion) (CircuitMutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, now, failure := s.checkedEntry(identity, CircuitPhaseSuspect, leaseID)
	if failure != nil {
		return *failure, nil
	}
	if completion.Outcome == CircuitVerdictFramingComplete {
		if completion.FramingCompleteDisposition == "closed" {
			return s.close(entry, identity.TransitionID, now), nil
		}
		return s.enterRecovering(entry, identity.TransitionID, now), nil
	}
	if completion.Outcome == CircuitVerdictUnknown {
		backoffAttempt := entry.state.BackoffAttempt + 1
		next := cloneCircuitState(entry.state)
		next.TransitionID = identity.TransitionID
		next.BackoffAttempt = backoffAttempt
		next.Lease = nil
		retryAt := now + AccountCircuitBackoffDelayMS(int64(backoffAttempt),
			fmt.Sprintf("%s:%d:%d:confirmation-unknown", entry.state.ScopeKey, entry.state.Generation, backoffAttempt), nil)
		next.RetryAtMS = &retryAt
		next.UpdatedAtMS = now
		return s.apply(entry, next, identity.TransitionID, 0, false), nil
	}

	required, err := NormalizeAccountCircuitConfirmationFailuresRequired(entry.state.ConfirmationFailuresRequired)
	if err != nil {
		return CircuitMutationResult{}, err
	}
	previousEvidenceKeys, err := AccountCircuitFailureEvidenceKeys(entry.state)
	if err != nil {
		return CircuitMutationResult{}, err
	}
	failureEvidenceKey, err := NormalizeAccountCircuitFailureEvidenceKey(completion.FailureEvidenceKey, fmt.Sprintf("confirmation:%s", leaseID))
	if err != nil {
		return CircuitMutationResult{}, err
	}
	isIndependentEvidence := !containsString(previousEvidenceKeys, failureEvidenceKey)
	failureEvidenceKeys := previousEvidenceKeys
	if isIndependentEvidence {
		failureEvidenceKeys = append(cloneStringSlice(previousEvidenceKeys), failureEvidenceKey)
		keep := required + 1
		if len(failureEvidenceKeys) > keep {
			failureEvidenceKeys = failureEvidenceKeys[len(failureEvidenceKeys)-keep:]
		}
	}
	confirmationFailureCount, err := AccountCircuitConfirmationFailureCount(entry.state)
	if err != nil {
		return CircuitMutationResult{}, err
	}
	if isIndependentEvidence {
		confirmationFailureCount += 1
	}

	confirmationState := cloneCircuitState(entry.state)
	confirmationState.BackoffAttempt = 0
	confirmationState.ConfirmationFailuresRequired = &required
	confirmationState.ConfirmationFailureCount = &confirmationFailureCount
	confirmationState.FailureEvidenceKeys = failureEvidenceKeys
	confirmationState.TransitionID = identity.TransitionID
	if completion.Reason != "" {
		confirmationState.FailureReason = completion.Reason
	}
	confirmationState.Lease = nil
	retryAt := now + circuitSuspectConfirmationIntervalMS
	confirmationState.RetryAtMS = &retryAt
	confirmationState.UpdatedAtMS = now
	if confirmationFailureCount < required {
		return s.apply(entry, confirmationState, identity.TransitionID, 0, false), nil
	}
	entry.state = confirmationState
	return s.open(entry, identity.TransitionID, now, completion.Reason), nil
}

func (s *MemoryCircuitStore) CompleteCanary(_ context.Context, identity CircuitTransitionIdentity, leaseID string, completion CircuitCompletion) (CircuitMutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Node completeCanary 只校验 phase=HALF_OPEN 与 leaseId 匹配，不校验租约 kind。
	entry, now, failure := s.checkedEntry(identity, CircuitPhaseHalfOpen, leaseID)
	if failure != nil {
		return *failure, nil
	}
	if completion.Outcome == CircuitVerdictTransportFailure {
		return s.open(entry, identity.TransitionID, now, completion.Reason), nil
	}
	if completion.Outcome == CircuitVerdictUnknown {
		return s.restoreCanaryOrigin(entry, identity.TransitionID, now), nil
	}
	if entry.state.HalfOpenOrigin == string(CircuitPhaseOpen) {
		return s.enterRecovering(entry, identity.TransitionID, now), nil
	}
	recoverySuccessCount := entry.state.RecoverySuccessCount + 1
	if recoverySuccessCount >= CircuitRecoverySuccessThreshold {
		return s.close(entry, identity.TransitionID, now), nil
	}
	next := cloneCircuitState(entry.state)
	next.Phase = CircuitPhaseRecovering
	next.TransitionID = identity.TransitionID
	next.RecoverySuccessCount = recoverySuccessCount
	next.RecoveryEvidenceScopeKeys = s.nextRecoveryEvidenceScopeKeys(entry.state, completion.EvidenceScopeKey)
	next.Lease = nil
	next.HalfOpenOrigin = ""
	retryAt := now
	next.RetryAtMS = &retryAt
	next.UpdatedAtMS = now
	return s.apply(entry, next, identity.TransitionID, 0, false), nil
}

func (s *MemoryCircuitStore) ClearAccountEscalationEvidence(_ context.Context, accountRuntimeKey, dispatchRevision, evidenceID string, _ int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, err := requiredScopePart(accountRuntimeKey, "accountRuntimeKey")
	if err != nil {
		return false, err
	}
	if _, err := requiredScopePart(dispatchRevision, "dispatchRevision"); err != nil {
		return false, err
	}
	if _, err := requiredScopePart(evidenceID, "evidenceId"); err != nil {
		return false, err
	}
	// 内存实现未启用 escalation evidence 记录（恢复引擎不写入），与 Node
	// MemoryAccountCircuitStore 未记录时返回 false 的行为一致。
	_ = key
	return false, nil
}

func (s *MemoryCircuitStore) ReplaceDispatchRevision(_ context.Context, scope CircuitScope, dispatchRevision, transitionID string, nowMS int64) (CircuitMutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := normalizeClockMS(nowMS)
	revision, err := requiredScopePart(dispatchRevision, "dispatchRevision")
	if err != nil {
		return CircuitMutationResult{}, err
	}
	transition, err := requiredScopePart(transitionID, "transitionId")
	if err != nil {
		return CircuitMutationResult{}, err
	}
	entry := s.freshEntry(scope, now)
	if replay := s.idempotentResult(entry, transition); replay != nil {
		return *replay, nil
	}
	if entry != nil && entry.state.DispatchRevision == revision {
		return circuitMutationResult(CircuitMutationIdempotent, entry.state, nil), nil
	}
	if entry != nil && isOlderNumericDispatchRevision(revision, entry.state.DispatchRevision) {
		return circuitMutationResult(CircuitMutationStaleDispatchRevision, entry.state, nil), nil
	}
	if entry == nil && !s.reserveCapacity(now) {
		return circuitMutationResult(CircuitMutationCapacityExhausted,
			capacityExhaustedAccountCircuitState(scope, revision, now), nil), nil
	}
	target := entry
	if target == nil {
		target = s.newEntry(closedAccountCircuitState(scope, "", 0, "", 0))
	}
	next := closedAccountCircuitState(scope, revision, target.state.Generation+1, transition, now)
	return s.apply(target, next, transition, now+s.closedRetentionMS, true), nil
}

func (s *MemoryCircuitStore) ReplaceAccountDispatchRevision(_ context.Context, accountRuntimeKey, dispatchRevision, transitionID string, nowMS int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := normalizeClockMS(nowMS)
	target, err := requiredScopePart(accountRuntimeKey, "accountRuntimeKey")
	if err != nil {
		return 0, err
	}
	revision, err := requiredScopePart(dispatchRevision, "dispatchRevision")
	if err != nil {
		return 0, err
	}
	transition, err := requiredScopePart(transitionID, "transitionId")
	if err != nil {
		return 0, err
	}
	var changed int64
	for _, scopeKey := range sortedMapKeys(s.entries) {
		entry := s.entries[scopeKey]
		if !runtimeKeyMatchesDispatchRevisionTarget(entry.state.Scope.AccountRuntimeKey, target) {
			continue
		}
		if entry.state.DispatchRevision == revision {
			continue
		}
		if isOlderNumericDispatchRevision(revision, entry.state.DispatchRevision) {
			continue
		}
		entry.state = closedAccountCircuitState(entry.state.Scope, revision, entry.state.Generation+1, transition, now)
		entry.closedExpiresAtMS = now + s.closedRetentionMS
		entry.closedExpiresSet = true
		s.rememberReplay(entry, transition)
		changed++
	}
	return changed, nil
}

func (s *MemoryCircuitStore) Restore(_ context.Context, rawState CircuitState, nowMS int64) (CircuitMutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := normalizeClockMS(nowMS)
	state, err := s.normalizeConfirmationState(cloneCircuitState(rawState))
	if err != nil {
		return CircuitMutationResult{}, err
	}
	expectedScopeKey, err := AccountCircuitScopeKey(state.Scope)
	if err != nil {
		return CircuitMutationResult{}, err
	}
	if state.ScopeKey != expectedScopeKey {
		return CircuitMutationResult{}, fmt.Errorf("账户电路 scopeKey 与作用域字段不一致")
	}
	existing := s.freshEntry(state.Scope, now)
	if existing != nil && isOlderNumericDispatchRevision(state.DispatchRevision, existing.state.DispatchRevision) {
		return circuitMutationResult(CircuitMutationStaleDispatchRevision, existing.state, nil), nil
	}
	if existing != nil &&
		(existing.state.Generation > state.Generation ||
			(existing.state.Generation == state.Generation && existing.state.UpdatedAtMS >= state.UpdatedAtMS)) {
		related := s.projectParentRelationship(existing.state)
		return circuitMutationResult(CircuitMutationIdempotent, existing.state, related), nil
	}
	if existing == nil && !s.reserveCapacity(now) {
		return circuitMutationResult(CircuitMutationCapacityExhausted,
			capacityExhaustedAccountCircuitState(state.Scope, state.DispatchRevision, now), nil), nil
	}
	entry := existing
	if entry == nil {
		entry = s.newEntry(state)
	}
	entry.state = state
	entry.closedExpiresSet = state.Phase == CircuitPhaseClosed
	entry.closedExpiresAtMS = now + s.closedRetentionMS
	s.rememberReplay(entry, state.TransitionID)
	related := s.projectParentRelationship(state)
	return circuitMutationResult(CircuitMutationApplied, state, related), nil
}

func (s *MemoryCircuitStore) ListDue(_ context.Context, nowMS int64, limit int) ([]CircuitState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := normalizeClockMS(nowMS)
	if limit < 1 {
		return nil, fmt.Errorf("账户电路 limit 必须是正整数")
	}
	s.cleanup(now)
	due := make([]CircuitState, 0, len(s.entries))
	for _, entry := range s.entries {
		if circuitDueAtMS(entry.state) <= now {
			due = append(due, cloneCircuitState(entry.state))
		}
	}
	sort.SliceStable(due, func(left, right int) bool {
		return circuitDueAtMS(due[left]) < circuitDueAtMS(due[right])
	})
	if len(due) > limit {
		due = due[:limit]
	}
	return due, nil
}

// Size 返回当前条目数（测试/运维辅助）。
func (s *MemoryCircuitStore) Size(nowMS int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup(normalizeClockMS(nowMS))
	return len(s.entries)
}

func (s *MemoryCircuitStore) acquireLease(identity CircuitTransitionIdentity, lease CircuitLeaseSpec, expectedPhase CircuitPhase, kind CircuitLeaseKind) (CircuitMutationResult, error) {
	now := normalizeClockMS(identity.NowMS)
	entry := s.freshEntry(identity.Scope, now)
	if invalid := s.validateIdentity(entry, identity); invalid != nil {
		return *invalid, nil
	}
	if entry == nil {
		return notFoundResult(identity.Scope), nil
	}
	if replay := s.idempotentResult(entry, identity.TransitionID); replay != nil {
		return *replay, nil
	}
	if entry.state.Phase != expectedPhase || entry.state.ShadowedByIncidentID != "" {
		return stateMismatchResult(entry.state), nil
	}
	if entry.state.Lease != nil {
		return stateMismatchResult(entry.state), nil
	}
	if retryAtMS(entry.state) > now {
		return circuitMutationResult(CircuitMutationNotDue, entry.state, nil), nil
	}
	leaseState, err := s.withLease(entry.state, kind, lease, now)
	if err != nil {
		return CircuitMutationResult{}, err
	}
	leaseState.TransitionID = identity.TransitionID
	return s.apply(entry, leaseState, identity.TransitionID, 0, false), nil
}

func (s *MemoryCircuitStore) withLease(state CircuitState, kind CircuitLeaseKind, lease CircuitLeaseSpec, now int64) (CircuitState, error) {
	leaseID, err := requiredScopePart(lease.LeaseID, "leaseId")
	if err != nil {
		return CircuitState{}, err
	}
	until := normalizeClockMS(lease.LeaseUntilMS)
	if until <= now {
		return CircuitState{}, fmt.Errorf("账户电路租约截止时间必须晚于当前时间")
	}
	next := cloneCircuitState(state)
	next.Lease = &CircuitLease{Kind: kind, LeaseID: leaseID, LeaseUntilMS: until}
	next.UpdatedAtMS = now
	return next, nil
}

func (s *MemoryCircuitStore) checkedEntry(identity CircuitTransitionIdentity, expectedPhase CircuitPhase, leaseID string) (*memoryCircuitEntry, int64, *CircuitMutationResult) {
	now := normalizeClockMS(identity.NowMS)
	entry := s.freshEntry(identity.Scope, now)
	if invalid := s.validateIdentity(entry, identity); invalid != nil {
		return nil, 0, invalid
	}
	if entry == nil {
		result := notFoundResult(identity.Scope)
		return nil, 0, &result
	}
	if replay := s.idempotentResult(entry, identity.TransitionID); replay != nil {
		return nil, 0, replay
	}
	if entry.state.Phase != expectedPhase {
		result := stateMismatchResult(entry.state)
		return nil, 0, &result
	}
	if entry.state.Lease == nil || entry.state.Lease.LeaseID != leaseID {
		result := circuitMutationResult(CircuitMutationLeaseMismatch, entry.state, nil)
		return nil, 0, &result
	}
	return entry, now, nil
}

func (s *MemoryCircuitStore) validateIdentity(entry *memoryCircuitEntry, identity CircuitTransitionIdentity) *CircuitMutationResult {
	if entry == nil {
		result := notFoundResult(identity.Scope)
		return &result
	}
	if entry.state.Generation != identity.Generation {
		result := circuitMutationResult(CircuitMutationStaleGeneration, entry.state, nil)
		return &result
	}
	if entry.state.DispatchRevision != identity.DispatchRevision {
		result := circuitMutationResult(CircuitMutationStaleDispatchRevision, entry.state, nil)
		return &result
	}
	return nil
}

func (s *MemoryCircuitStore) open(entry *memoryCircuitEntry, transitionID string, now int64, reason string) CircuitMutationResult {
	backoffAttempt := entry.state.BackoffAttempt + 1
	next := cloneCircuitState(entry.state)
	next.Phase = CircuitPhaseOpen
	next.TransitionID = transitionID
	next.BackoffAttempt = backoffAttempt
	next.RecoverySuccessCount = 0
	next.RecoveryEvidenceScopeKeys = nil
	next.IncidentID = firstNonEmpty(entry.state.IncidentID, transitionID)
	openedAt := now
	next.OpenedAtMS = &openedAt
	retryAt := now + AccountCircuitBackoffDelayMS(int64(backoffAttempt),
		fmt.Sprintf("%s:%d:%d", entry.state.ScopeKey, entry.state.Generation, backoffAttempt), nil)
	next.RetryAtMS = &retryAt
	if reason != "" {
		next.FailureReason = reason
	}
	next.Lease = nil
	next.HalfOpenOrigin = ""
	next.UpdatedAtMS = now
	return s.apply(entry, next, transitionID, 0, false)
}

func (s *MemoryCircuitStore) enterRecovering(entry *memoryCircuitEntry, transitionID string, now int64) CircuitMutationResult {
	backoffAttempt := entry.state.BackoffAttempt
	if entry.state.Phase == CircuitPhaseSuspect {
		backoffAttempt = 0
	}
	next := cloneCircuitState(entry.state)
	next.Phase = CircuitPhaseRecovering
	next.TransitionID = transitionID
	next.BackoffAttempt = backoffAttempt
	next.RecoverySuccessCount = 0
	next.RecoveryEvidenceScopeKeys = nil
	zeroCount := 0
	next.ConfirmationFailureCount = &zeroCount
	next.FailureEvidenceKeys = nil
	next.FailureReason = ""
	next.Lease = nil
	next.HalfOpenOrigin = ""
	retryAt := now + CircuitRecoveryCanaryIntervalMS
	next.RetryAtMS = &retryAt
	next.UpdatedAtMS = now
	return s.apply(entry, next, transitionID, 0, false)
}

func (s *MemoryCircuitStore) close(entry *memoryCircuitEntry, transitionID string, now int64) CircuitMutationResult {
	childScopeKeys := entry.state.ChildScopeKeys
	childIncidentIDs := entry.state.ChildIncidentIDs
	incidentID := entry.state.IncidentID
	isAccountScope := entry.state.Scope.Kind == CircuitScopeAccount
	closed := closedAccountCircuitState(entry.state.Scope, entry.state.DispatchRevision, entry.state.Generation, transitionID, now)
	closed.IncidentID = incidentID
	if isAccountScope && incidentID != "" && len(childScopeKeys) > 0 {
		closed.ChildScopeKeys = cloneStringSlice(childScopeKeys)
		closed.ChildIncidentIDs = cloneStringSlice(childIncidentIDs)
	}
	applied := s.apply(entry, closed, transitionID, now+s.closedRetentionMS, true)
	var related []CircuitState
	if incidentID != "" {
		related = s.unshadowChildren(childScopeKeys, childIncidentIDs, incidentID, transitionID, now)
	}
	return circuitMutationResult(applied.Status, applied.State, related)
}

func (s *MemoryCircuitStore) restoreCanaryOrigin(entry *memoryCircuitEntry, transitionID string, now int64) CircuitMutationResult {
	origin := entry.state.HalfOpenOrigin
	if origin == "" {
		origin = string(CircuitPhaseOpen)
	}
	backoffAttempt := entry.state.BackoffAttempt + 1
	next := cloneCircuitState(entry.state)
	next.Phase = CircuitPhase(origin)
	next.TransitionID = transitionID
	next.BackoffAttempt = backoffAttempt
	next.Lease = nil
	next.HalfOpenOrigin = ""
	retryAt := now + AccountCircuitBackoffDelayMS(int64(backoffAttempt),
		fmt.Sprintf("%s:%d:%d:unknown", entry.state.ScopeKey, entry.state.Generation, backoffAttempt), nil)
	next.RetryAtMS = &retryAt
	next.UpdatedAtMS = now
	return s.apply(entry, next, transitionID, 0, false)
}

func (s *MemoryCircuitStore) nextRecoveryEvidenceScopeKeys(state CircuitState, evidenceScopeKey string) []string {
	if state.Scope.Kind != CircuitScopeAccount {
		return cloneStringSlice(state.RecoveryEvidenceScopeKeys)
	}
	if len(state.RequiredRecoveryScopeKeys) == 0 {
		return cloneStringSlice(state.RecoveryEvidenceScopeKeys)
	}
	normalized := strings.TrimSpace(evidenceScopeKey)
	if normalized == "" || !containsString(state.RequiredRecoveryScopeKeys, normalized) {
		return cloneStringSlice(state.RecoveryEvidenceScopeKeys)
	}
	merged := cloneStringSlice(state.RecoveryEvidenceScopeKeys)
	if !containsString(merged, normalized) {
		merged = append(merged, normalized)
	}
	return merged
}

func (s *MemoryCircuitStore) projectParentRelationship(parentState CircuitState) []CircuitState {
	if parentState.Scope.Kind != CircuitScopeAccount || parentState.IncidentID == "" {
		return nil
	}
	childScopeKeys := parentState.ChildScopeKeys
	childIncidentIDs := parentState.ChildIncidentIDs
	var related []CircuitState
	for index, scopeKey := range childScopeKeys {
		child, found := s.entries[scopeKey]
		if !found || child.state.DispatchRevision != parentState.DispatchRevision {
			continue
		}
		if index >= len(childIncidentIDs) {
			continue
		}
		childIncidentID := childIncidentIDs[index]
		currentIncidentID := firstNonEmpty(child.state.IncidentID, fmt.Sprintf("%s@%d", scopeKey, child.state.Generation))
		if childIncidentID == "" || currentIncidentID != childIncidentID || child.state.UpdatedAtMS > parentState.UpdatedAtMS {
			continue
		}
		if parentState.Phase == CircuitPhaseClosed {
			if child.state.ShadowedByIncidentID == parentState.IncidentID {
				transitionID := hierarchyTransitionID("unshadow", parentState.TransitionID, parentState.IncidentID, scopeKey, child.state.Generation)
				child.state.TransitionID = transitionID
				child.state.ShadowedByIncidentID = ""
				child.state.UpdatedAtMS = parentState.UpdatedAtMS
				s.rememberReplay(child, transitionID)
				related = append(related, cloneCircuitState(child.state))
			}
			continue
		}
		if child.state.Phase != CircuitPhaseClosed && child.state.ShadowedByIncidentID == "" {
			transitionID := hierarchyTransitionID("shadow", parentState.TransitionID, parentState.IncidentID, scopeKey, child.state.Generation)
			child.state.TransitionID = transitionID
			child.state.ShadowedByIncidentID = parentState.IncidentID
			child.state.UpdatedAtMS = parentState.UpdatedAtMS
			s.rememberReplay(child, transitionID)
			related = append(related, cloneCircuitState(child.state))
		}
	}
	return related
}

func (s *MemoryCircuitStore) unshadowChildren(scopeKeys, childIncidentIDs []string, parentIncidentID, parentTransitionID string, now int64) []CircuitState {
	var related []CircuitState
	for index, scopeKey := range scopeKeys {
		child, found := s.entries[scopeKey]
		if !found || child.state.ShadowedByIncidentID != parentIncidentID {
			continue
		}
		if index >= len(childIncidentIDs) {
			continue
		}
		childIncidentID := childIncidentIDs[index]
		currentIncidentID := firstNonEmpty(child.state.IncidentID, fmt.Sprintf("%s@%d", scopeKey, child.state.Generation))
		if childIncidentID == "" || currentIncidentID != childIncidentID {
			continue
		}
		transitionID := hierarchyTransitionID("unshadow", parentTransitionID, parentIncidentID, scopeKey, child.state.Generation)
		child.state.TransitionID = transitionID
		child.state.ShadowedByIncidentID = ""
		child.state.UpdatedAtMS = now
		s.rememberReplay(child, transitionID)
		related = append(related, cloneCircuitState(child.state))
	}
	return related
}

func (s *MemoryCircuitStore) freshEntry(scope CircuitScope, now int64) *memoryCircuitEntry {
	scopeKey, err := AccountCircuitScopeKey(scope)
	if err != nil {
		return nil
	}
	entry, found := s.entries[scopeKey]
	if !found {
		return nil
	}
	if entry.state.Phase == CircuitPhaseClosed && entry.closedExpiresSet && entry.closedExpiresAtMS <= now {
		delete(s.entries, entry.state.ScopeKey)
		return nil
	}
	s.normalizeExpiredLease(entry, now)
	entry.state = s.safeNormalizeConfirmationState(entry.state)
	return entry
}

func (s *MemoryCircuitStore) normalizeExpiredLease(entry *memoryCircuitEntry, now int64) {
	lease := entry.state.Lease
	if lease == nil || lease.LeaseUntilMS > now {
		return
	}
	if lease.Kind == CircuitLeaseConfirmation {
		entry.state.Lease = nil
		retryAt := now
		entry.state.RetryAtMS = &retryAt
		entry.state.UpdatedAtMS = now
		return
	}
	origin := entry.state.HalfOpenOrigin
	if origin == "" {
		origin = string(CircuitPhaseOpen)
	}
	entry.state.Phase = CircuitPhase(origin)
	entry.state.Lease = nil
	entry.state.HalfOpenOrigin = ""
	retryAt := now
	entry.state.RetryAtMS = &retryAt
	entry.state.UpdatedAtMS = now
}

func (s *MemoryCircuitStore) reserveCapacity(now int64) bool {
	s.cleanup(now)
	if len(s.entries) < s.capacity {
		s.capacitySaturated = false
		return true
	}
	var oldestClosed *memoryCircuitEntry
	for _, entry := range s.entries {
		if entry.state.Phase != CircuitPhaseClosed {
			continue
		}
		if oldestClosed == nil || entry.state.UpdatedAtMS < oldestClosed.state.UpdatedAtMS {
			oldestClosed = entry
		}
	}
	if oldestClosed == nil {
		s.capacitySaturated = true
		return false
	}
	delete(s.entries, oldestClosed.state.ScopeKey)
	s.capacitySaturated = false
	return true
}

func (s *MemoryCircuitStore) cleanup(now int64) {
	for scopeKey, entry := range s.entries {
		if entry.state.Phase == CircuitPhaseClosed && entry.closedExpiresSet && entry.closedExpiresAtMS <= now {
			delete(s.entries, scopeKey)
		} else {
			s.normalizeExpiredLease(entry, now)
		}
	}
	if len(s.entries) < s.capacity {
		s.capacitySaturated = false
	}
}

func (s *MemoryCircuitStore) newEntry(state CircuitState) *memoryCircuitEntry {
	entry := &memoryCircuitEntry{state: state, replayIDs: map[string]struct{}{}}
	s.entries[state.ScopeKey] = entry
	return entry
}

func (s *MemoryCircuitStore) apply(entry *memoryCircuitEntry, state CircuitState, transitionID string, closedExpiresAtMS int64, closedExpiresSet bool) CircuitMutationResult {
	entry.state = state
	entry.closedExpiresAtMS = closedExpiresAtMS
	entry.closedExpiresSet = closedExpiresSet
	s.rememberReplay(entry, transitionID)
	return circuitMutationResult(CircuitMutationApplied, state, nil)
}

func (s *MemoryCircuitStore) idempotentResult(entry *memoryCircuitEntry, transitionID string) *CircuitMutationResult {
	if _, err := requiredScopePart(transitionID, "transitionId"); err != nil {
		return nil
	}
	if _, exists := entry.replayIDs[transitionID]; exists {
		result := circuitMutationResult(CircuitMutationIdempotent, entry.state, nil)
		return &result
	}
	return nil
}

func (s *MemoryCircuitStore) rememberReplay(entry *memoryCircuitEntry, transitionID string) {
	if _, exists := entry.replayIDs[transitionID]; exists {
		return
	}
	entry.replayIDs[transitionID] = struct{}{}
	entry.replayOrder = append(entry.replayOrder, transitionID)
	for len(entry.replayOrder) > s.replayLimitPerScope {
		oldest := entry.replayOrder[0]
		entry.replayOrder = entry.replayOrder[1:]
		delete(entry.replayIDs, oldest)
	}
}

func (s *MemoryCircuitStore) normalizeConfirmationState(state CircuitState) (CircuitState, error) {
	if state.Phase == CircuitPhaseClosed {
		return state, nil
	}
	required, err := NormalizeAccountCircuitConfirmationFailuresRequired(state.ConfirmationFailuresRequired)
	if err != nil {
		return CircuitState{}, err
	}
	state.ConfirmationFailuresRequired = &required
	count, err := AccountCircuitConfirmationFailureCount(state)
	if err != nil {
		return CircuitState{}, err
	}
	state.ConfirmationFailureCount = &count
	evidenceKeys, err := AccountCircuitFailureEvidenceKeys(state)
	if err != nil {
		return CircuitState{}, err
	}
	state.FailureEvidenceKeys = evidenceKeys
	if state.Phase == CircuitPhaseSuspect && state.RetryAtMS == nil {
		retryAt := state.UpdatedAtMS
		if state.Lease != nil {
			retryAt = state.Lease.LeaseUntilMS
		}
		state.RetryAtMS = &retryAt
	}
	return state, nil
}

func (s *MemoryCircuitStore) safeNormalizeConfirmationState(state CircuitState) CircuitState {
	normalized, err := s.normalizeConfirmationState(state)
	if err != nil {
		return state
	}
	return normalized
}

func notFoundResult(scope CircuitScope) CircuitMutationResult {
	return circuitMutationResult(CircuitMutationNotFound, closedAccountCircuitState(scope, "", 0, "", 0), nil)
}

func stateMismatchResult(state CircuitState) CircuitMutationResult {
	return circuitMutationResult(CircuitMutationStateMismatch, state, nil)
}

func normalizeClockMS(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func retryAtMS(state CircuitState) int64 {
	if state.RetryAtMS != nil {
		return *state.RetryAtMS
	}
	return int64(^uint64(0) >> 1)
}

func runtimeKeyMatchesDispatchRevisionTarget(runtimeKey, target string) bool {
	return runtimeKey == target ||
		(!strings.Contains(target, ":authorized:") && strings.HasPrefix(runtimeKey, target+":authorized:"))
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sortedMapKeys[V any](source map[string]V) []string {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func capacityExhaustedAccountCircuitState(scope CircuitScope, dispatchRevision string, nowMS int64) CircuitState {
	state := closedAccountCircuitState(scope, dispatchRevision, 0, "runtime-capacity-exhausted", nowMS)
	state.Phase = CircuitPhaseSuspect
	state.FailureReason = "runtime_state_capacity_exhausted"
	retryAt := nowMS + 1_000
	state.RetryAtMS = &retryAt
	return state
}
