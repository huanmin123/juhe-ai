package gatewaycircuit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
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
	OutcomeFramingComplete = "framing_complete"
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

// Scope mirrors AccountCircuitScope. Kind selects the active fields exactly
// like the Node discriminated union.
type Scope struct {
	Kind              string `json:"kind"`
	AccountRuntimeKey string `json:"accountRuntimeKey,omitempty"`
	KeyFingerprint    string `json:"keyFingerprint,omitempty"`
	ProtocolProfile   string `json:"protocolProfile,omitempty"`
	RequestLane       string `json:"requestLane,omitempty"`
	ModelBucket       string `json:"modelBucket,omitempty"`
}

// Lease mirrors AccountCircuitLease.
type Lease struct {
	Kind         string `json:"kind"`
	LeaseID      string `json:"leaseId"`
	LeaseUntilMs int64  `json:"leaseUntilMs"`
}

// stringList decodes Lua round-tripped JSON arrays: an empty Lua array is
// encoded as `{}`, which must behave like an empty list (Node tolerates the
// same shapes through cloneStringArray).
type stringList []string

func (l stringList) clone() stringList {
	if l == nil {
		return nil
	}
	out := make(stringList, len(l))
	copy(out, l)
	return out
}

func (l stringList) equal(other stringList) bool {
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

func (l *stringList) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		*l = nil
		return nil
	}
	if trimmed == "{}" || trimmed == "[]" {
		*l = stringList{}
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	*l = values
	return nil
}

// State mirrors AccountCircuitState. Optional fields are pointers so the JSON
// encoding keeps Node's undefined-presence semantics (a present 0 stays).
type State struct {
	ScopeKey                     string     `json:"scopeKey"`
	Scope                        Scope      `json:"scope"`
	Phase                        string     `json:"phase"`
	Generation                   int64      `json:"generation"`
	DispatchRevision             string     `json:"dispatchRevision"`
	TransitionID                 string     `json:"transitionId"`
	BackoffAttempt               int64      `json:"backoffAttempt"`
	RecoverySuccessCount         int64      `json:"recoverySuccessCount"`
	ConfirmationFailuresRequired *int64     `json:"confirmationFailuresRequired,omitempty"`
	ConfirmationFailureCount     *int64     `json:"confirmationFailureCount,omitempty"`
	FailureEvidenceKeys          stringList `json:"failureEvidenceKeys,omitempty"`
	OpenedAtMs                   *int64     `json:"openedAtMs,omitempty"`
	RetryAtMs                    *int64     `json:"retryAtMs,omitempty"`
	FailureReason                *string    `json:"failureReason,omitempty"`
	Lease                        *Lease     `json:"lease,omitempty"`
	HalfOpenOrigin               *string    `json:"halfOpenOrigin,omitempty"`
	IncidentID                   *string    `json:"incidentId,omitempty"`
	ShadowedByIncidentID         *string    `json:"shadowedByIncidentId,omitempty"`
	ChildIncidentIDs             stringList `json:"childIncidentIds,omitempty"`
	ChildScopeKeys               stringList `json:"childScopeKeys,omitempty"`
	RequiredRecoveryScopeKeys    stringList `json:"requiredRecoveryScopeKeys,omitempty"`
	RecoveryEvidenceScopeKeys    stringList `json:"recoveryEvidenceScopeKeys,omitempty"`
	UpdatedAtMs                  int64      `json:"updatedAtMs"`
}

// CloneState mirrors cloneAccountCircuitState.
func CloneState(state State) State {
	out := state
	out.Scope = Scope{
		Kind:              state.Scope.Kind,
		AccountRuntimeKey: state.Scope.AccountRuntimeKey,
		KeyFingerprint:    state.Scope.KeyFingerprint,
		ProtocolProfile:   state.Scope.ProtocolProfile,
		RequestLane:       state.Scope.RequestLane,
		ModelBucket:       state.Scope.ModelBucket,
	}
	if state.Lease != nil {
		lease := *state.Lease
		out.Lease = &lease
	}
	out.FailureEvidenceKeys = state.FailureEvidenceKeys.clone()
	out.ChildIncidentIDs = state.ChildIncidentIDs.clone()
	out.ChildScopeKeys = state.ChildScopeKeys.clone()
	out.RequiredRecoveryScopeKeys = state.RequiredRecoveryScopeKeys.clone()
	out.RecoveryEvidenceScopeKeys = state.RecoveryEvidenceScopeKeys.clone()
	return out
}

// stateList decodes Lua-encoded `relatedStates`: an empty Lua array is
// encoded as `{}`, which must decode as an empty list.
type stateList []State

func (l *stateList) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "{}" || trimmed == "[]" {
		*l = nil
		return nil
	}
	var values []State
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	*l = values
	return nil
}

func (l stateList) slice() []State {
	if l == nil {
		return nil
	}
	return append([]State{}, l...)
}

// MutationResult mirrors AccountCircuitMutationResult.
type MutationResult struct {
	Status        string    `json:"status"`
	State         State     `json:"state"`
	RelatedStates stateList `json:"relatedStates,omitempty"`
}

// RelatedStatesSlice returns the related states as a plain slice.
func (r MutationResult) RelatedStatesSlice() []State { return r.RelatedStates.slice() }

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
	Scope                     Scope
	Generation                int64
	DispatchRevision          string
	TransitionID              string
	LeaseID                   string
	LeaseUntilMs              int64
	ExpectedFailureEvidenceKey *string
	ConfirmationEvidenceKey    *string
	NowMs                     *int64
}

// CloseSuspectFromObserverInput mirrors the store.closeSuspectFromObserver input.
type CloseSuspectFromObserverInput struct {
	Scope                     Scope
	Generation                int64
	DispatchRevision          string
	TransitionID              string
	ExpectedFailureEvidenceKey string
	ObserverEvidenceKey        string
	NowMs                     *int64
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
	Scope                       Scope
	Generation                  int64
	DispatchRevision            string
	TransitionID                string
	LeaseID                     string
	Outcome                     string
	Reason                      *string
	FailureEvidenceKey          *string
	FramingCompleteDisposition  *string
	NowMs                       *int64
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
	Scope                   Scope
	Generation              int64
	DispatchRevision        string
	EvidenceID              string
	AccountTransitionID     string
	Reason                  string
	ConfirmedFailureCount   int64
	DistinctScopeThreshold  int64
	WindowMs                int64
	MaxProtocolScopes       int64
	NowMs                   *int64
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
func (r EscalationResult) RelatedStatesSlice() []State { return r.RelatedStates.slice() }

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

func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func requiredScopePart(value, name string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", fmt.Errorf("账户电路作用域缺少 %s", name)
	}
	return normalized, nil
}

func requiredRequestLane(value string) (string, error) {
	if value != LaneText && value != LaneImage {
		return "", errors.New("账户电路作用域 requestLane 必须是 text 或 image")
	}
	return value, nil
}

func encodedScopeKey(parts ...string) string {
	encoded := make([]string, len(parts))
	for i, part := range parts {
		encoded[i] = fmt.Sprintf("%d:%s", len(part), part)
	}
	return strings.Join(encoded, "|")
}

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
func parseSafeInteger(value string) (float64, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, false
	}
	number, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	if number != math.Trunc(number) || math.Abs(number) > 9007199254740991 {
		return 0, false
	}
	return number, true
}

// sortedCopy returns a sorted copy of values (Node [...values].sort()).
func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
