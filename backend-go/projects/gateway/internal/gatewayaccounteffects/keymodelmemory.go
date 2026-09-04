package gatewayaccounteffects

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// KeyModelFailureIntent mirrors KeyModelFailureIntent.
type KeyModelFailureIntent struct {
	IntentID        string
	RequestID       string
	AttemptID       string
	Capability      CapabilityKey
	ObservedAtMs    int64
	Outcome         KeyModelOutcome
	SourceFence     string
	Permit          *KeyModelForegroundPermit
	RecoveryTarget  *KeyModelRecoveryTarget
}

// KeyModelRecoveryTarget mirrors KeyModelRecoveryTarget.
type KeyModelRecoveryTarget struct {
	AccountID       string
	GroupID         string
	SystemAccountID string
}

// KeyModelFenceReference mirrors KeyModelFenceReference.
type KeyModelFenceReference struct {
	CapabilityHash   string
	KeyFingerprint   string
	DispatchRevision int64
	OwnerID          string
}

// KeyModelFailureResult mirrors KeyModelFailureResult.
type KeyModelFailureResult struct {
	// Status is applied | idempotent | capacity_exhausted | stale.
	Status  KeyModelMutationStatus
	State   *KeyModelState
	Applied bool
}

// KeyModelAdmissionResult mirrors KeyModelAdmissionResult.
type KeyModelAdmissionResult struct {
	// Status is admitted | busy | blocked.
	Status      KeyModelForegroundDecision
	WakeSequence int64
	Permit      *KeyModelForegroundPermit
}

// KeyModelForegroundPermit mirrors KeyModelForegroundPermit.
type KeyModelForegroundPermit struct {
	CapabilityHash string
	AttemptID      string
	LeaseUntilMs   int64
}

// KeyModelRuntimeStore mirrors KeyModelRuntimeStore.
type KeyModelRuntimeStore interface {
	Get(ctx context.Context, capability CapabilityKey) (*KeyModelState, error)
	RecordFailure(ctx context.Context, intent KeyModelFailureIntent) (KeyModelFailureResult, error)
	AdmitForeground(ctx context.Context, capability CapabilityKey, attemptID string) (KeyModelAdmissionResult, error)
	ReleaseForeground(ctx context.Context, permit KeyModelForegroundPermit) (bool, error)
	RenewForeground(ctx context.Context, permit KeyModelForegroundPermit) (*KeyModelForegroundPermit, error)
	RecordMainProbeFailure(ctx context.Context, capability CapabilityKey, permit KeyModelForegroundPermit) error
	ClearMainProbeFence(ctx context.Context, fence KeyModelFenceReference, winnerKeyFingerprint string) (bool, error)
	DeferMainProbeFence(ctx context.Context, fence KeyModelFenceReference) (bool, error)
	ClaimJ1Confirmation(ctx context.Context, sourceAccountID string, dispatchRevision int64) (bool, error)
}

// InMemoryKeyModelRecoveryStore extends the runtime store with the recovery
// sweep surface (InMemoryKeyModelRecoveryStore).
type InMemoryKeyModelRecoveryStore interface {
	KeyModelRuntimeStore
	ListDue(nowMs int64, limit int) ([]KeyModelState, error)
	GetRecoveryTarget(capability CapabilityKey) *KeyModelRecoveryTarget
	AcquireRecoveryLease(input MemoryRecoveryLeaseInput) (KeyModelMutationStatus, KeyModelState)
	RenewRecoveryLease(input MemoryRecoveryRenewInput) bool
	SettleRecovery(input MemoryRecoverySettleInput) (KeyModelMutationStatus, KeyModelState)
}

// MemoryRecoveryLeaseInput mirrors acquireRecoveryLease's input.
type MemoryRecoveryLeaseInput struct {
	Capability       CapabilityKey
	Generation       int64
	DispatchRevision int64
	LeaseID          string
	NowMs            int64
}

// MemoryRecoveryRenewInput mirrors renewRecoveryLease's input.
type MemoryRecoveryRenewInput struct {
	CapabilityHash   string
	Generation       int64
	DispatchRevision int64
	LeaseID          string
	NowMs            int64
}

// MemoryRecoverySettleInput mirrors settleRecovery's input.
type MemoryRecoverySettleInput struct {
	Capability       CapabilityKey
	Generation       int64
	DispatchRevision int64
	LeaseID          string
	Outcome          KeyModelOutcome
	NowMs            int64
}

type memoryReceipt struct {
	state       KeyModelState
	expiresAtMs int64
}

// InMemoryKeyModelRuntimeStore is the single-process equivalent of the Redis
// adapter (InMemoryKeyModelRuntimeStore). It intentionally shares the exact
// pure state transitions and limits, but has no cross-process durability.
type InMemoryKeyModelRuntimeStore struct {
	clock Clock

	mu              sync.Mutex
	states          map[string]KeyModelState
	receipts        map[string]*memoryReceipt
	closedUntil     map[string]int64
	permits         map[string]map[string]int64
	wakes           map[string]int64
	mainFences      map[string]*memoryMainFence
	j1Claims        map[string]int64
	recoveryTargets map[string]KeyModelRecoveryTarget
}

type memoryMainFence struct {
	ownerID     string
	expiresAtMs int64
}

// NewInMemoryKeyModelRuntimeStore builds the process-local store.
func NewInMemoryKeyModelRuntimeStore(clock Clock) *InMemoryKeyModelRuntimeStore {
	if clock == nil {
		clock = SystemClock{}
	}
	return &InMemoryKeyModelRuntimeStore{
		clock:           clock,
		states:          map[string]KeyModelState{},
		receipts:        map[string]*memoryReceipt{},
		closedUntil:     map[string]int64{},
		permits:         map[string]map[string]int64{},
		wakes:           map[string]int64{},
		mainFences:      map[string]*memoryMainFence{},
		j1Claims:        map[string]int64{},
		recoveryTargets: map[string]KeyModelRecoveryTarget{},
	}
}

// Get implements KeyModelRuntimeStore.
func (s *InMemoryKeyModelRuntimeStore) Get(ctx context.Context, capability CapabilityKey) (*KeyModelState, error) {
	hash, err := CapabilityHash(capability)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(NowMs(s.clock))
	if state, ok := s.states[hash]; ok {
		clone := state.Clone()
		return &clone, nil
	}
	return nil, nil
}

// GetRecoveryTarget implements InMemoryKeyModelRecoveryStore.
func (s *InMemoryKeyModelRuntimeStore) GetRecoveryTarget(capability CapabilityKey) *KeyModelRecoveryTarget {
	hash, err := CapabilityHash(capability)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if target, ok := s.recoveryTargets[hash]; ok {
		clone := target
		return &clone
	}
	return nil
}

// ListDue implements InMemoryKeyModelRecoveryStore.
func (s *InMemoryKeyModelRuntimeStore) ListDue(nowMs int64, limit int) ([]KeyModelState, error) {
	if nowMs < 1 || nowMs > safeIntegerMax {
		return nil, errors.New("Key-model memory listDue nowMs 无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(nowMs)
	due := make([]KeyModelState, 0, len(s.states))
	for _, state := range s.states {
		if (state.Phase == KeyModelPhaseOpen || state.Phase == KeyModelPhaseRecovering) && (state.RetryAtMs == nil || *state.RetryAtMs <= nowMs) {
			due = append(due, state.Clone())
		}
	}
	sort.SliceStable(due, func(left, right int) bool {
		leftContinuation := due[left].Phase == KeyModelPhaseRecovering
		rightContinuation := due[right].Phase == KeyModelPhaseRecovering
		if leftContinuation != rightContinuation {
			return leftContinuation
		}
		return retryAtOrInfinity(due[left]) < retryAtOrInfinity(due[right])
	})
	boundedLimit := limit
	if boundedLimit < 0 {
		boundedLimit = 0
	}
	if len(due) > boundedLimit {
		due = due[:boundedLimit]
	}
	return due, nil
}

func retryAtOrInfinity(state KeyModelState) int64 {
	if state.RetryAtMs == nil {
		return int64(1 << 62)
	}
	return *state.RetryAtMs
}

// AcquireRecoveryLease implements InMemoryKeyModelRecoveryStore.
func (s *InMemoryKeyModelRuntimeStore) AcquireRecoveryLease(input MemoryRecoveryLeaseInput) (KeyModelMutationStatus, KeyModelState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(input.NowMs)
	hash, err := CapabilityHash(input.Capability)
	if err != nil {
		open, openErr := CreateKeyModelOpenState(input.Capability, input.NowMs)
		if openErr != nil {
			return KeyModelMutationStale, open
		}
		return KeyModelMutationStale, open
	}
	current, ok := s.states[hash]
	if !ok {
		open, openErr := CreateKeyModelOpenState(input.Capability, input.NowMs)
		if openErr != nil {
			return KeyModelMutationStale, KeyModelState{}
		}
		return KeyModelMutationStale, open
	}
	status, next, leaseErr := AcquireKeyModelRecoveryLease(current, AcquireKeyModelRecoveryLeaseInput{
		Generation:       input.Generation,
		DispatchRevision: input.DispatchRevision,
		LeaseID:          input.LeaseID,
		NowMs:            input.NowMs,
	})
	if leaseErr != nil {
		return KeyModelMutationLeaseMismatch, current.Clone()
	}
	if status == KeyModelMutationApplied {
		s.states[hash] = next
	}
	return status, next.Clone()
}

// RenewRecoveryLease implements InMemoryKeyModelRecoveryStore.
func (s *InMemoryKeyModelRuntimeStore) RenewRecoveryLease(input MemoryRecoveryRenewInput) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(input.NowMs)
	state, ok := s.states[trimSpace(input.CapabilityHash)]
	if !ok || state.Generation != input.Generation || state.DispatchRevision != input.DispatchRevision || state.Phase != KeyModelPhaseHalfOpen {
		return false
	}
	if state.ProbeLease == nil || state.ProbeLease.LeaseID != input.LeaseID || state.ProbeLease.LeaseUntilMs < input.NowMs {
		return false
	}
	renewed := *state.ProbeLease
	renewed.LeaseUntilMs = input.NowMs + KeyModelProbeLeaseMs
	state.ProbeLease = &renewed
	s.states[state.CapabilityHash] = state
	return true
}

// SettleRecovery implements InMemoryKeyModelRecoveryStore.
func (s *InMemoryKeyModelRuntimeStore) SettleRecovery(input MemoryRecoverySettleInput) (KeyModelMutationStatus, KeyModelState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(input.NowMs)
	hash, err := CapabilityHash(input.Capability)
	if err != nil {
		open, openErr := CreateKeyModelOpenState(input.Capability, input.NowMs)
		if openErr != nil {
			return KeyModelMutationStale, KeyModelState{}
		}
		return KeyModelMutationStale, open
	}
	current, ok := s.states[hash]
	if !ok {
		open, openErr := CreateKeyModelOpenState(input.Capability, input.NowMs)
		if openErr != nil {
			return KeyModelMutationStale, KeyModelState{}
		}
		return KeyModelMutationStale, open
	}
	status, next := SettleKeyModelRecovery(current, SettleKeyModelRecoveryInput{
		Generation:       input.Generation,
		DispatchRevision: input.DispatchRevision,
		LeaseID:          input.LeaseID,
		Outcome:          input.Outcome,
		NowMs:            input.NowMs,
	})
	if status == KeyModelMutationApplied {
		s.states[hash] = next
		if next.Phase == KeyModelPhaseClosed {
			s.closedUntil[hash] = input.NowMs + keyModelClosedRetentionMs
		} else {
			delete(s.closedUntil, hash)
		}
	}
	return status, next.Clone()
}

// RecordFailure implements KeyModelRuntimeStore.
func (s *InMemoryKeyModelRuntimeStore) RecordFailure(ctx context.Context, intent KeyModelFailureIntent) (KeyModelFailureResult, error) {
	if err := validateFailureIntent(intent); err != nil {
		return KeyModelFailureResult{}, err
	}
	hash, err := CapabilityHash(intent.Capability)
	if err != nil {
		return KeyModelFailureResult{}, err
	}
	if intent.Permit != nil && intent.Permit.CapabilityHash != hash {
		return KeyModelFailureResult{}, errors.New("Key-model 失败意图 permit 与 CapabilityKey 不匹配")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prior, ok := s.receipts[intent.IntentID]; ok {
		s.releaseForegroundIfPresentLocked(intent.Permit, hash)
		receiptState := prior.state.Clone()
		return KeyModelFailureResult{Status: KeyModelMutationIdempotent, State: &receiptState, Applied: false}, nil
	}
	current, hasCurrent := s.states[hash]
	if hasCurrent && current.DispatchRevision > intent.Capability.DispatchRevision {
		s.releaseForegroundIfPresentLocked(intent.Permit, hash)
		return KeyModelFailureResult{Status: KeyModelMutationStale}, nil
	}
	// Standalone has no shared Redis clock; use the captured failure fact so
	// memory transitions follow the same deterministic contract.
	now := intent.ObservedAtMs
	if !hasCurrent && !s.ensureStateCapacityLocked() {
		s.releaseForegroundIfPresentLocked(intent.Permit, hash)
		return KeyModelFailureResult{Status: "capacity_exhausted"}, nil
	}
	idempotentPrior := hasCurrent && current.DispatchRevision == intent.Capability.DispatchRevision && current.Phase != KeyModelPhaseClosed
	var state KeyModelState
	if idempotentPrior {
		state = current.Clone()
		state.LastObservedAtMs = now
		state.LastOutcome = KeyModelOutcomeUpstreamNotComplete
	} else {
		open, openErr := CreateKeyModelOpenState(intent.Capability, now)
		if openErr != nil {
			return KeyModelFailureResult{}, openErr
		}
		if hasCurrent {
			open.Generation = current.Generation + 1
		}
		state = open
	}
	s.states[hash] = state
	delete(s.closedUntil, hash)
	if intent.RecoveryTarget != nil {
		normalized, err := normalizeRecoveryTarget(*intent.RecoveryTarget)
		if err != nil {
			return KeyModelFailureResult{}, err
		}
		s.recoveryTargets[hash] = normalized
	}
	cloned := state.Clone()
	s.receipts[intent.IntentID] = &memoryReceipt{state: cloned, expiresAtMs: now + keyModelReceiptRetentionMs}
	s.releaseForegroundIfPresentLocked(intent.Permit, hash)
	status := KeyModelMutationApplied
	if idempotentPrior {
		status = KeyModelMutationIdempotent
	}
	return KeyModelFailureResult{Status: status, State: &cloned, Applied: status == KeyModelMutationApplied}, nil
}

// AdmitForeground implements KeyModelRuntimeStore.
func (s *InMemoryKeyModelRuntimeStore) AdmitForeground(ctx context.Context, capability CapabilityKey, attemptID string) (KeyModelAdmissionResult, error) {
	hash, err := CapabilityHash(capability)
	if err != nil {
		return KeyModelAdmissionResult{}, err
	}
	normalizedAttemptID := trimSpace(attemptID)
	if normalizedAttemptID == "" {
		return KeyModelAdmissionResult{}, errors.New("Key-model 缺少 attemptId")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := NowMs(s.clock)
	if existing, ok := s.permits[hash][normalizedAttemptID]; ok {
		return KeyModelAdmissionResult{
			Status: ForegroundAdmitted,
			Permit: &KeyModelForegroundPermit{CapabilityHash: hash, AttemptID: normalizedAttemptID, LeaseUntilMs: existing},
		}, nil
	}
	s.cleanupLocked(now)
	state, hasState := s.states[hash]
	if fence, hasFence := s.mainFences[hash]; hasFence && fence.expiresAtMs <= now {
		delete(s.mainFences, hash)
	}
	_, fenceActive := s.mainFences[hash]
	if (hasState && state.DispatchRevision == capability.DispatchRevision && state.Phase != KeyModelPhaseClosed) || fenceActive {
		return KeyModelAdmissionResult{Status: ForegroundBlocked, WakeSequence: s.wakes[hash]}, nil
	}
	active := s.permits[hash]
	if active == nil {
		active = map[string]int64{}
	}
	for attempt, lease := range active {
		if lease <= now {
			delete(active, attempt)
		}
	}
	if len(active) >= KeyModelForegroundLimit {
		return KeyModelAdmissionResult{Status: ForegroundBusy, WakeSequence: s.wakes[hash]}, nil
	}
	leaseUntilMs := now + KeyModelForegroundPrecommitLeaseMs
	active[normalizedAttemptID] = leaseUntilMs
	s.permits[hash] = active
	return KeyModelAdmissionResult{
		Status: ForegroundAdmitted,
		Permit: &KeyModelForegroundPermit{CapabilityHash: hash, AttemptID: normalizedAttemptID, LeaseUntilMs: leaseUntilMs},
	}, nil
}

// ReleaseForeground implements KeyModelRuntimeStore.
func (s *InMemoryKeyModelRuntimeStore) ReleaseForeground(ctx context.Context, permit KeyModelForegroundPermit) (bool, error) {
	hash, err := requiredCapabilityHash(permit.CapabilityHash)
	if err != nil {
		return false, err
	}
	attemptID, err := requireKeyModelText(permit.AttemptID, "attemptId")
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.permits[hash]
	if active == nil {
		return false, nil
	}
	if _, ok := active[attemptID]; !ok {
		return false, nil
	}
	delete(active, attemptID)
	s.wakes[permit.CapabilityHash] = s.wakes[permit.CapabilityHash] + 1
	return true, nil
}

// RenewForeground implements KeyModelRuntimeStore.
func (s *InMemoryKeyModelRuntimeStore) RenewForeground(ctx context.Context, permit KeyModelForegroundPermit) (*KeyModelForegroundPermit, error) {
	hash, err := requiredCapabilityHash(permit.CapabilityHash)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.permits[hash]
	if active == nil {
		return nil, nil
	}
	if _, ok := active[permit.AttemptID]; !ok {
		return nil, nil
	}
	leaseUntilMs := NowMs(s.clock) + KeyModelForegroundPrecommitLeaseMs
	active[permit.AttemptID] = leaseUntilMs
	return &KeyModelForegroundPermit{CapabilityHash: hash, AttemptID: permit.AttemptID, LeaseUntilMs: leaseUntilMs}, nil
}

// RecordMainProbeFailure implements KeyModelRuntimeStore.
func (s *InMemoryKeyModelRuntimeStore) RecordMainProbeFailure(ctx context.Context, capability CapabilityKey, permit KeyModelForegroundPermit) error {
	hash, err := CapabilityHash(capability)
	if err != nil {
		return err
	}
	if permit.CapabilityHash != hash {
		return errors.New("MainProbe fence permit 与 CapabilityKey 不匹配")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if active := s.permits[hash]; active != nil {
		if _, ok := active[permit.AttemptID]; ok {
			delete(active, permit.AttemptID)
			s.wakes[permit.CapabilityHash] = s.wakes[permit.CapabilityHash] + 1
		}
	}
	s.mainFences[hash] = &memoryMainFence{ownerID: permit.AttemptID, expiresAtMs: NowMs(s.clock) + KeyModelForegroundPrecommitLeaseMs}
	return nil
}

// ClearMainProbeFence implements KeyModelRuntimeStore.
func (s *InMemoryKeyModelRuntimeStore) ClearMainProbeFence(ctx context.Context, fence KeyModelFenceReference, winnerKeyFingerprint string) (bool, error) {
	if fence.KeyFingerprint != winnerKeyFingerprint {
		return false, nil
	}
	hash, err := requiredCapabilityHash(fence.CapabilityHash)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.mainFences[hash]
	if current == nil || current.ownerID != fence.OwnerID {
		return false, nil
	}
	delete(s.mainFences, hash)
	return true, nil
}

// DeferMainProbeFence implements KeyModelRuntimeStore.
func (s *InMemoryKeyModelRuntimeStore) DeferMainProbeFence(ctx context.Context, fence KeyModelFenceReference) (bool, error) {
	hash, err := requiredCapabilityHash(fence.CapabilityHash)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.mainFences[hash]
	if current == nil || current.ownerID != fence.OwnerID {
		return false, nil
	}
	current.expiresAtMs = NowMs(s.clock) + KeyModelMainProbeUnknownRetryMs
	return true, nil
}

// ClaimJ1Confirmation implements KeyModelRuntimeStore.
func (s *InMemoryKeyModelRuntimeStore) ClaimJ1Confirmation(ctx context.Context, sourceAccountID string, dispatchRevision int64) (bool, error) {
	if _, err := requireKeyModelText(sourceAccountID, "credentialSourceAccountId"); err != nil {
		return false, err
	}
	key := sourceAccountID + ":" + strconv.FormatInt(dispatchRevision, 10)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := NowMs(s.clock)
	if s.j1Claims[key] > now {
		return false, nil
	}
	s.j1Claims[key] = now + 2*60_000
	return true, nil
}

func (s *InMemoryKeyModelRuntimeStore) releaseForegroundIfPresentLocked(permit *KeyModelForegroundPermit, hash string) {
	if permit == nil {
		return
	}
	if active := s.permits[hash]; active != nil {
		if _, ok := active[permit.AttemptID]; ok {
			delete(active, permit.AttemptID)
			s.wakes[permit.CapabilityHash] = s.wakes[permit.CapabilityHash] + 1
		}
	}
}

func (s *InMemoryKeyModelRuntimeStore) cleanupLocked(nowMs int64) {
	for intentID, receipt := range s.receipts {
		if receipt.expiresAtMs <= nowMs {
			delete(s.receipts, intentID)
		}
	}
	for hash, retainedUntilMs := range s.closedUntil {
		if retainedUntilMs > nowMs {
			continue
		}
		if state, ok := s.states[hash]; ok && state.Phase == KeyModelPhaseClosed {
			delete(s.states, hash)
			delete(s.recoveryTargets, hash)
		}
		delete(s.closedUntil, hash)
	}
}

func (s *InMemoryKeyModelRuntimeStore) ensureStateCapacityLocked() bool {
	if len(s.states) < keyModelStateCapacity {
		return true
	}
	hashes := make([]string, 0, len(s.closedUntil))
	for hash := range s.closedUntil {
		hashes = append(hashes, hash)
	}
	sort.Slice(hashes, func(left, right int) bool {
		return s.closedUntil[hashes[left]] < s.closedUntil[hashes[right]]
	})
	for _, hash := range hashes {
		if state, ok := s.states[hash]; ok && state.Phase == KeyModelPhaseClosed {
			delete(s.states, hash)
			delete(s.recoveryTargets, hash)
		}
		delete(s.closedUntil, hash)
		if len(s.states) < keyModelStateCapacity {
			return true
		}
	}
	return false
}

func validateFailureIntent(intent KeyModelFailureIntent) error {
	if _, err := requireKeyModelText(intent.IntentID, "intentId"); err != nil {
		return err
	}
	if _, err := requireKeyModelText(intent.RequestID, "requestId"); err != nil {
		return err
	}
	if _, err := requireKeyModelText(intent.AttemptID, "attemptId"); err != nil {
		return err
	}
	if _, err := requireKeyModelText(intent.SourceFence, "sourceFence"); err != nil {
		return err
	}
	if intent.Outcome != KeyModelOutcomeUpstreamNotComplete {
		return errors.New("Key-model 失败意图 outcome 只能为 upstream_not_complete")
	}
	if intent.ObservedAtMs < 1 || intent.ObservedAtMs > safeIntegerMax {
		return errors.New("Key-model observedAtMs 无效")
	}
	if _, err := CapabilityHash(intent.Capability); err != nil {
		return err
	}
	return nil
}

func normalizeRecoveryTarget(target KeyModelRecoveryTarget) (KeyModelRecoveryTarget, error) {
	accountID, err := requireKeyModelText(target.AccountID, "recoveryTarget.accountId")
	if err != nil {
		return KeyModelRecoveryTarget{}, err
	}
	groupID, err := requireKeyModelText(target.GroupID, "recoveryTarget.groupId")
	if err != nil {
		return KeyModelRecoveryTarget{}, err
	}
	systemAccountID, err := requireKeyModelText(target.SystemAccountID, "recoveryTarget.systemAccountId")
	if err != nil {
		return KeyModelRecoveryTarget{}, err
	}
	return KeyModelRecoveryTarget{AccountID: accountID, GroupID: groupID, SystemAccountID: systemAccountID}, nil
}

func requireKeyModelText(value string, name string) (string, error) {
	normalized := trimSpace(value)
	if normalized == "" {
		return "", errors.New("Key-model 缺少 " + name)
	}
	return normalized, nil
}

var capabilityHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// requiredCapabilityHash mirrors requiredHash.
func requiredCapabilityHash(value string) (string, error) {
	normalized := trimSpace(value)
	normalized = strings.ToLower(normalized)
	if !capabilityHashPattern.MatchString(normalized) {
		return "", errors.New("Key-model capabilityHash 无效")
	}
	return normalized, nil
}
