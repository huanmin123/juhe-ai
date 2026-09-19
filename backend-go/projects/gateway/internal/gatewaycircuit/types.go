package gatewaycircuit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/circuitstate"
)

// Circuit phase names mirror AccountCircuitPhase exactly.
const (
	PhaseClosed     = "CLOSED"
	PhaseSuspect    = "SUSPECT"
	PhaseOpen       = "OPEN"
	PhaseHalfOpen   = "HALF_OPEN"
	PhaseRecovering = "RECOVERING"
)

// Lease kinds mirror AccountCircuitLeaseKind.
const (
	LeaseKindConfirmation = "confirmation"
	LeaseKindHalfOpen     = "half_open"
	LeaseKindRecovery     = "recovery"
)

// Scope kinds.
const (
	ScopeKindAccount       = "account"
	ScopeKindKey           = "key"
	ScopeKindProtocolModel = "protocol_model"
)

// Request lanes mirror OpenAIGatewayRequestLane.
const (
	LaneText  = "text"
	LaneImage = "image"
)

// Mutation statuses mirror AccountCircuitMutationStatus.
const (
	MutationApplied               = "applied"
	MutationIdempotent            = "idempotent"
	MutationNotFound              = "not_found"
	MutationStateMismatch         = "state_mismatch"
	MutationStaleGeneration       = "stale_generation"
	MutationStaleDispatchRevision = "stale_dispatch_revision"
	MutationLeaseMismatch         = "lease_mismatch"
	MutationNotDue                = "not_due"
	MutationCapacityExhausted     = "capacity_exhausted"
)

// Escalation statuses mirror AccountCircuitEscalationStatus.
const (
	EscalationRecorded         = "recorded"
	EscalationEscalated        = "escalated"
	EscalationAlreadyActive    = "already_active"
	EscalationIdempotent       = "idempotent"
	EscalationNotFound         = "not_found"
	EscalationStateMismatch    = "state_mismatch"
	EscalationStaleGeneration  = "stale_generation"
	EscalationStaleRevision    = "stale_dispatch_revision"
	EscalationCapacityExceeded = "capacity_exhausted"
)

// Confirmation outcomes mirror the shared outcome union.
const (
	OutcomeFramingComplete  = "framing_complete"
	OutcomeTransportFailure = "transport_failure"
	OutcomeUnknown          = "unknown"
)

// Store defaults (account-circuit-store.ts / memory + redis store options).
const (
	DefaultConfirmationFailuresRequired     = int64(2)
	LegacyConfirmationFailuresRequired      = int64(1)
	ConfirmationFailuresRequiredMin         = int64(1)
	ConfirmationFailuresRequiredMax         = int64(5)
	EscalationDistinctScopeThresholdDefault = int64(3)
	EscalationDistinctScopeThresholdMin     = int64(3)
	EscalationDistinctScopeThresholdMax     = int64(64)
	EscalationWindowMsDefault               = int64(10 * 60_000)
	EscalationWindowMsMin                   = int64(60_000)
	EscalationWindowMsMax                   = int64(24 * 60 * 60_000)
	DefaultClosedRetentionMs                = int64(5 * 60_000)
	DefaultReplayLimitPerScope              = int64(64)
)

// Settings carries the runtime-configurable circuit numbers (Node reads them
// from config/runtime.ts). Defaults mirror the Node fallbacks exactly.
type Settings struct {
	// AccountCircuitBackoffMs mirrors runtimeConfig.gateway.accountCircuitBackoffMs.
	AccountCircuitBackoffMs []int64
	// AccountCircuitRecoverySuccessThreshold mirrors
	// runtimeConfig.gateway.accountCircuitRecoverySuccessThreshold (default 3).
	AccountCircuitRecoverySuccessThreshold int64
	// AccountCircuitRecoveryCanaryIntervalMs (default 3000).
	AccountCircuitRecoveryCanaryIntervalMs int64
	// AccountCircuitSuspectConfirmationIntervalMs (default 3000).
	AccountCircuitSuspectConfirmationIntervalMs int64
	// AccountCircuitEscalationDistinctScopeThreshold (default 3).
	AccountCircuitEscalationDistinctScopeThreshold int64
	// AccountCircuitEscalationWindowMs (default 10 minutes).
	AccountCircuitEscalationWindowMs int64
	// RecoverableUnavailable* mirror runtimeConfig.gateway.recoverableUnavailable*.
	RecoverableUnavailableMaxWaitMs          int64
	RecoverableUnavailableCheckIntervalMs    int64
	RecoverableUnavailableDueRetryDelayMs    int64
	RecoverableUnavailableMaxWaitersPerScope int
	RecoverableUnavailableMaxWaitersGlobal   int
}

// DefaultSettings mirrors the Node config fallbacks.
func DefaultSettings() Settings {
	return Settings{
		AccountCircuitBackoffMs:                        []int64{3_000, 5_000, 10_000, 30_000, 60_000, 120_000, 300_000, 600_000, 900_000},
		AccountCircuitRecoverySuccessThreshold:         3,
		AccountCircuitRecoveryCanaryIntervalMs:         3_000,
		AccountCircuitSuspectConfirmationIntervalMs:    3_000,
		AccountCircuitEscalationDistinctScopeThreshold: 3,
		AccountCircuitEscalationWindowMs:               10 * 60_000,
		RecoverableUnavailableMaxWaitMs:                30_000,
		RecoverableUnavailableCheckIntervalMs:          5_000,
		RecoverableUnavailableDueRetryDelayMs:          250,
		RecoverableUnavailableMaxWaitersPerScope:       5_000,
		RecoverableUnavailableMaxWaitersGlobal:         5_000,
	}
}

// REFACTOR-0008 跨模块成对收敛：与 jobs/internal/circuitstore 逐字节相同的
// 共享运行态词汇（Scope/Lease/State/MutationResult 及其列表类型、CloneState）
// 下潜到 shared/platform/circuitstate 作为单一事实；本包保留类型别名，
// 调用点零改动。
type (
	Scope          = circuitstate.Scope
	Lease          = circuitstate.Lease
	State          = circuitstate.State
	MutationResult = circuitstate.MutationResult
	stringList     = circuitstate.StringList
	stateList      = circuitstate.StateList
)

// CloneState mirrors cloneAccountCircuitState（下潜委托壳）。
func CloneState(state State) State { return circuitstate.CloneState(state) }

// stringListEqual 保留原 stringList.equal 的比较语义（REFACTOR-0008：equal
// 方法仅 gateway 侧存在、仅测试消费，别名类型无法挂方法，收敛为包内函数）。
func stringListEqual(l, other stringList) bool {
	if len(l) != len(other) {
		return false
	}
	for i := range l {
		if l[i] != other[i] {
			return false
		}
	}
	return true
}

// ScopeKey 与 MustScopeKey 留守：两侧实现已文本漂移（本包用作用域 kind
// 常量，circuitstore 内联字面量），行为等价，按对账结论不强行统一。

// TransitionIdentity mirrors AccountCircuitTransitionIdentity.
type TransitionIdentity struct {
	Scope            Scope
	Generation       int64
	DispatchRevision string
	TransitionID     string
	NowMs            *int64
}

// SuspectInput mirrors the store.suspect input.
type SuspectInput struct {
	Scope                        Scope
	DispatchRevision             string
	TransitionID                 string
	Reason                       string
	ConfirmationFailuresRequired *int64
	FailureEvidenceKey           *string
	NowMs                        *int64
}

// AcquireConfirmationLeaseInput mirrors the store.acquireConfirmationLease input.
type AcquireConfirmationLeaseInput struct {
	Scope                      Scope
	Generation                 int64
	DispatchRevision           string
	TransitionID               string
	LeaseID                    string
	LeaseUntilMs               int64
	ExpectedFailureEvidenceKey *string
	ConfirmationEvidenceKey    *string
	NowMs                      *int64
}

// CloseSuspectFromObserverInput mirrors the store.closeSuspectFromObserver input.
type CloseSuspectFromObserverInput struct {
	Scope                      Scope
	Generation                 int64
	DispatchRevision           string
	TransitionID               string
	ExpectedFailureEvidenceKey string
	ObserverEvidenceKey        string
	NowMs                      *int64
}

// CloseSuspectFromKeyRotationInput mirrors the store.closeSuspectFromKeyRotation input.
type CloseSuspectFromKeyRotationInput struct {
	Scope                      Scope
	Generation                 int64
	DispatchRevision           string
	TransitionID               string
	ExpectedFailureEvidenceKey string
	NowMs                      *int64
}

// CompleteConfirmationInput mirrors the store.completeConfirmation input.
type CompleteConfirmationInput struct {
	Scope                      Scope
	Generation                 int64
	DispatchRevision           string
	TransitionID               string
	LeaseID                    string
	Outcome                    string
	Reason                     *string
	FailureEvidenceKey         *string
	FramingCompleteDisposition *string
	NowMs                      *int64
}

// AcquireCanaryLeaseInput mirrors the store.acquireCanaryLease input.
type AcquireCanaryLeaseInput struct {
	Scope            Scope
	Generation       int64
	DispatchRevision string
	TransitionID     string
	LeaseID          string
	LeaseUntilMs     int64
	NowMs            *int64
}

// CompleteCanaryInput mirrors the store.completeCanary input.
type CompleteCanaryInput struct {
	Scope            Scope
	Generation       int64
	DispatchRevision string
	TransitionID     string
	LeaseID          string
	Outcome          string
	Reason           *string
	EvidenceScopeKey *string
	NowMs            *int64
}

// ReplaceDispatchRevisionInput mirrors the store.replaceDispatchRevision input.
type ReplaceDispatchRevisionInput struct {
	Scope            Scope
	DispatchRevision string
	TransitionID     string
	NowMs            *int64
}

// ReplaceAccountDispatchRevisionInput mirrors
// store.replaceAccountDispatchRevision input.
type ReplaceAccountDispatchRevisionInput struct {
	AccountRuntimeKey string
	DispatchRevision  string
	TransitionID      string
	NowMs             *int64
}

// ProtocolModelOpenEvidenceInput mirrors AccountCircuitProtocolModelOpenEvidenceInput.
type ProtocolModelOpenEvidenceInput struct {
	Scope                  Scope
	Generation             int64
	DispatchRevision       string
	EvidenceID             string
	AccountTransitionID    string
	Reason                 string
	ConfirmedFailureCount  int64
	DistinctScopeThreshold int64
	WindowMs               int64
	MaxProtocolScopes      int64
	NowMs                  *int64
}

// EscalationResult mirrors AccountCircuitEscalationResult.
type EscalationResult struct {
	Status                string    `json:"status"`
	AccountState          State     `json:"accountState"`
	ProtocolScopeCount    int64     `json:"protocolScopeCount"`
	ConfirmedFailureCount int64     `json:"confirmedFailureCount"`
	RelatedStates         stateList `json:"relatedStates,omitempty"`
}

// RelatedStatesSlice returns the related states as a plain slice.
func (r EscalationResult) RelatedStatesSlice() []State { return r.RelatedStates.Slice() }

// Store mirrors AccountCircuitStore. nowMs nil means "use the store clock".
type Store interface {
	Get(ctx context.Context, scope Scope, nowMs *int64) (State, error)
	Suspect(ctx context.Context, input SuspectInput) (MutationResult, error)
	AcquireConfirmationLease(ctx context.Context, input AcquireConfirmationLeaseInput) (MutationResult, error)
	CloseSuspectFromObserver(ctx context.Context, input CloseSuspectFromObserverInput) (MutationResult, error)
	CloseSuspectFromKeyRotation(ctx context.Context, input CloseSuspectFromKeyRotationInput) (MutationResult, error)
	CompleteConfirmation(ctx context.Context, input CompleteConfirmationInput) (MutationResult, error)
	AcquireCanaryLease(ctx context.Context, input AcquireCanaryLeaseInput) (MutationResult, error)
	CompleteCanary(ctx context.Context, input CompleteCanaryInput) (MutationResult, error)
	RecordProtocolModelOpenEvidence(ctx context.Context, input ProtocolModelOpenEvidenceInput) (EscalationResult, error)
	ClearAccountEscalationEvidence(ctx context.Context, input ClearAccountEscalationEvidenceInput) (bool, error)
	ReplaceDispatchRevision(ctx context.Context, input ReplaceDispatchRevisionInput) (MutationResult, error)
	Restore(ctx context.Context, state State, nowMs *int64) (MutationResult, error)
	ReplaceAccountDispatchRevision(ctx context.Context, input ReplaceAccountDispatchRevisionInput) (int64, error)
	ListDue(ctx context.Context, nowMs int64, limit int) ([]State, error)
	Size(ctx context.Context) (int64, error)
}

// ClearAccountEscalationEvidenceInput mirrors the clear input.
type ClearAccountEscalationEvidenceInput struct {
	AccountRuntimeKey string
	DispatchRevision  string
	EvidenceID        string
	NowMs             *int64
}

// ScopeKey mirrors accountCircuitScopeKey.
func ScopeKey(scope Scope) (string, error) {
	accountRuntimeKey, err := requiredScopePart(scope.AccountRuntimeKey, "accountRuntimeKey")
	if err != nil {
		return "", err
	}
	switch scope.Kind {
	case ScopeKindAccount:
		return encodedScopeKey("account", accountRuntimeKey), nil
	case ScopeKindKey:
		keyFingerprint, err := requiredScopePart(scope.KeyFingerprint, "keyFingerprint")
		if err != nil {
			return "", err
		}
		return encodedScopeKey("key", accountRuntimeKey, keyFingerprint), nil
	case ScopeKindProtocolModel:
		protocolProfile, err := requiredScopePart(scope.ProtocolProfile, "protocolProfile")
		if err != nil {
			return "", err
		}
		requestLane, err := requiredRequestLane(scope.RequestLane)
		if err != nil {
			return "", err
		}
		modelBucket, err := requiredScopePart(scope.ModelBucket, "modelBucket")
		if err != nil {
			return "", err
		}
		return encodedScopeKey("protocol_model", accountRuntimeKey, protocolProfile, requestLane, modelBucket), nil
	default:
		// Node would fall through to protocol_model handling only for the
		// three declared kinds; an unknown kind never reaches the stores.
		return "", fmt.Errorf("账户电路作用域 kind 无效: %s", scope.Kind)
	}
}

// MustScopeKey is the panic-free helper for already validated scopes.
func MustScopeKey(scope Scope) string {
	key, err := ScopeKey(scope)
	if err != nil {
		panic(err)
	}
	return key
}

// HierarchyTransitionID mirrors accountCircuitHierarchyTransitionId.
func HierarchyTransitionID(action, parentTransitionID, parentIncidentID, childScopeKey string, childGeneration int64) (string, error) {
	parentTransitionID, err := requiredScopePart(parentTransitionID, "parentTransitionId")
	if err != nil {
		return "", err
	}
	parentIncidentID, err = requiredScopePart(parentIncidentID, "parentIncidentId")
	if err != nil {
		return "", err
	}
	childScopeKey, err = requiredScopePart(childScopeKey, "childScopeKey")
	if err != nil {
		return "", err
	}
	if childGeneration < 0 {
		return "", errors.New("账户电路 hierarchy childGeneration 无效")
	}
	digest := sha1Hex(action + "\x00" + parentTransitionID + "\x00" + parentIncidentID + "\x00" + childScopeKey + "\x00" + fmt.Sprintf("%d", childGeneration))
	return "hierarchy:" + action + ":" + digest, nil
}

// AssertStateScopeKey mirrors assertAccountCircuitStateScopeKey.
func AssertStateScopeKey(state State) error {
	expected, err := ScopeKey(state.Scope)
	if err != nil {
		return err
	}
	if state.ScopeKey != expected {
		return errors.New("账户电路 scopeKey 与作用域字段不一致")
	}
	return nil
}

// ClosedState mirrors closedAccountCircuitState.
func ClosedState(scope Scope, dispatchRevision string, generation int64, transitionID string, updatedAtMs int64) State {
	key := MustScopeKey(scope)
	return State{
		ScopeKey:             key,
		Scope:                scope,
		Phase:                PhaseClosed,
		Generation:           generation,
		DispatchRevision:     dispatchRevision,
		TransitionID:         transitionID,
		BackoffAttempt:       0,
		RecoverySuccessCount: 0,
		UpdatedAtMs:          updatedAtMs,
	}
}

// CapacityExhaustedState mirrors capacityExhaustedAccountCircuitState.
func CapacityExhaustedState(scope Scope, dispatchRevision string, nowMs int64) State {
	state := ClosedState(scope, dispatchRevision, 0, "runtime-capacity-exhausted", nowMs)
	state.Phase = PhaseSuspect
	reason := "runtime_state_capacity_exhausted"
	state.FailureReason = &reason
	retryAt := nowMs + 1_000
	state.RetryAtMs = &retryAt
	return state
}

// NormalizeConfirmationFailuresRequired mirrors
// normalizeAccountCircuitConfirmationFailuresRequired. Callers pass the same
// fallback the Node call site uses (Default when normalizing an explicit
// input, Legacy when re-normalizing stored state).
func NormalizeConfirmationFailuresRequired(value *int64, fallback int64) (int64, error) {
	normalized := fallback
	if value != nil {
		normalized = *value
	}
	if normalized < ConfirmationFailuresRequiredMin || normalized > ConfirmationFailuresRequiredMax {
		return 0, fmt.Errorf("账户电路 confirmationFailuresRequired 必须是 %d..%d 的整数",
			ConfirmationFailuresRequiredMin, ConfirmationFailuresRequiredMax)
	}
	return normalized, nil
}

// NormalizeEscalationDistinctScopeThreshold mirrors
// normalizeAccountCircuitEscalationDistinctScopeThreshold.
func NormalizeEscalationDistinctScopeThreshold(value *int64, fallback int64) (int64, error) {
	normalized := fallback
	if value != nil {
		normalized = *value
	}
	if normalized < EscalationDistinctScopeThresholdMin || normalized > EscalationDistinctScopeThresholdMax {
		return 0, fmt.Errorf("账户电路 distinctScopeThreshold 必须是 %d..%d 的整数",
			EscalationDistinctScopeThresholdMin, EscalationDistinctScopeThresholdMax)
	}
	return normalized, nil
}

// NormalizeEscalationWindowMs mirrors normalizeAccountCircuitEscalationWindowMs.
func NormalizeEscalationWindowMs(value *int64, fallback int64) (int64, error) {
	normalized := fallback
	if value != nil {
		normalized = *value
	}
	if normalized < EscalationWindowMsMin || normalized > EscalationWindowMsMax {
		return 0, fmt.Errorf("账户电路 escalationWindowMs 必须是 %d..%d 的整数毫秒值",
			EscalationWindowMsMin, EscalationWindowMsMax)
	}
	return normalized, nil
}

// NormalizeFailureEvidenceKey mirrors normalizeAccountCircuitFailureEvidenceKey.
func NormalizeFailureEvidenceKey(value *string, fallbackSeed string) (string, error) {
	normalized := ""
	if value != nil {
		normalized = strings.ToLower(strings.TrimSpace(*value))
	}
	if isSHA256Hex(normalized) {
		return normalized, nil
	}
	seed := strings.TrimSpace(fallbackSeed)
	if seed == "" {
		return "", errors.New("账户电路 failure evidence 缺少 fallbackSeed")
	}
	return sha256Hex(seed), nil
}

// ConfirmationFailureCountOf mirrors accountCircuitConfirmationFailureCount.
func ConfirmationFailureCountOf(state State) (int64, error) {
	if state.ConfirmationFailureCount == nil {
		return 0, nil
	}
	value := *state.ConfirmationFailureCount
	if value < 0 || value > ConfirmationFailuresRequiredMax {
		return 0, errors.New("账户电路 confirmationFailureCount 无效")
	}
	return value, nil
}

// FailureEvidenceKeysOf mirrors accountCircuitFailureEvidenceKeys.
func FailureEvidenceKeysOf(state State) ([]string, error) {
	required, err := NormalizeConfirmationFailuresRequired(state.ConfirmationFailuresRequired, LegacyConfirmationFailuresRequired)
	if err != nil {
		return nil, err
	}
	normalized := make([]string, 0, len(state.FailureEvidenceKeys))
	seen := map[string]struct{}{}
	for _, value := range state.FailureEvidenceKeys {
		candidate := strings.ToLower(strings.TrimSpace(value))
		if !isSHA256Hex(candidate) {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		normalized = append(normalized, candidate)
	}
	keep := int(required) + 1
	if keep < 0 {
		keep = 0
	}
	if len(normalized) > keep {
		normalized = normalized[len(normalized)-keep:]
	}
	return normalized, nil
}

// LastFailureEvidenceKey mirrors accountCircuitFailureEvidenceKeys(...).at(-1).
func LastFailureEvidenceKey(state State) (string, bool, error) {
	keys, err := FailureEvidenceKeysOf(state)
	if err != nil {
		return "", false, err
	}
	if len(keys) == 0 {
		return "", false, nil
	}
	return keys[len(keys)-1], true, nil
}

// REFACTOR-0008 下潜委托壳：两侧逐字节相同的解析/校验原语收敛到
// shared/platform/circuitstate，包内调用点零改动。
func isSHA256Hex(value string) bool { return circuitstate.IsSHA256Hex(value) }

func requiredScopePart(value, name string) (string, error) {
	return circuitstate.RequiredScopePart(value, name)
}

func requiredRequestLane(value string) (string, error) {
	if value != LaneText && value != LaneImage {
		return "", errors.New("账户电路作用域 requestLane 必须是 text 或 image")
	}
	return value, nil
}

func encodedScopeKey(parts ...string) string { return circuitstate.EncodedScopeKey(parts...) }

// olderNumericDispatchRevision mirrors isOlderNumericDispatchRevision: true
// when candidate is a safe positive integer and current is a strictly larger
// safe integer.
func olderNumericDispatchRevision(candidate, current string) bool {
	candidateNumber, candidateOK := parseSafeInteger(candidate)
	if !candidateOK || candidateNumber <= 0 {
		return false
	}
	currentNumber, currentOK := parseSafeInteger(current)
	if !currentOK {
		return false
	}
	return currentNumber > candidateNumber
}

// parseSafeInteger mirrors Number(value) + Number.isSafeInteger checks for
// dispatch revision strings. Revision values are decimal numbers ("3") or
// opaque digests ("v1:<sha256>"); anything Number() would reject stays false.
func parseSafeInteger(value string) (float64, bool) { return circuitstate.ParseSafeInteger(value) }

// sortedCopy returns a sorted copy of values (Node [...values].sort()).
func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
