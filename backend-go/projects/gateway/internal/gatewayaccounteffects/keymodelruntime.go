package gatewayaccounteffects

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// KeyModelPhase mirrors KeyModelPhase: a deliberately narrow circuit for one
// physical credential and one resolved route; it does not participate in
// account-circuit escalation.
type KeyModelPhase string

// Key model phases.
const (
	KeyModelPhaseClosed     KeyModelPhase = "CLOSED"
	KeyModelPhaseOpen       KeyModelPhase = "OPEN"
	KeyModelPhaseHalfOpen   KeyModelPhase = "HALF_OPEN"
	KeyModelPhaseRecovering KeyModelPhase = "RECOVERING"
)

// KeyModelOutcome mirrors KeyModelOutcome.
type KeyModelOutcome string

// Key model outcomes.
const (
	KeyModelOutcomeCompleteSuccess     KeyModelOutcome = "complete_success"
	KeyModelOutcomeUpstreamNotComplete KeyModelOutcome = "upstream_not_complete"
	KeyModelOutcomeUnknown             KeyModelOutcome = "unknown"
)

// Key-model runtime constants (key-model-runtime.ts).
var keyModelBackoffMs = []int64{5_000, 15_000, 60_000, 5 * 60_000}

const (
	KeyModelRecoverySuccessThreshold    = 3
	KeyModelRecoverySuccessMaxGapMs     = int64(2 * 60_000)
	KeyModelRecoveryIntervalMs          = int64(10_000)
	KeyModelProbeTimeoutMs              = int64(30_000)
	KeyModelProbeLeaseMs                = int64(45_000)
	KeyModelProbeLeaseRenewMs           = int64(10_000)
	KeyModelForegroundLimit             = 2
	KeyModelForegroundPrecommitLeaseMs  = int64(90_000)
	KeyModelForegroundLeaseRenewMs      = int64(30_000)
	KeyModelForegroundRedisOperationTimeoutMs = int64(100)
	KeyModelMainProbeUnknownRetryMs     = int64(10_000)
	keyModelStateCapacity               = 50_000
	keyModelClosedRetentionMs           = int64(5 * 60_000)
	keyModelReceiptRetentionMs          = int64(5 * 60_000)
)

// CapabilityKey mirrors CapabilityKey; the JSON tags are the canonical hash
// contract shared with Node and jobs/keymodelrecovery.
type CapabilityKey struct {
	CredentialSourceAccountID string `json:"credentialSourceAccountId"`
	KeyFingerprint            string `json:"keyFingerprint"`
	ClientModel               string `json:"clientModel"`
	ClientEndpointFamily      string `json:"clientEndpointFamily"`
	FinalUpstreamModel        string `json:"finalUpstreamModel"`
	UpstreamEndpointMode      string `json:"upstreamEndpointMode"`
	DispatchRevision          int64  `json:"dispatchRevision"`
}

// KeyModelProbeLease mirrors the probeLease field.
type KeyModelProbeLease struct {
	LeaseID          string `json:"leaseId"`
	LeaseUntilMs     int64  `json:"leaseUntilMs"`
	PriorSuccessCount int   `json:"priorSuccessCount"`
}

// KeyModelState mirrors KeyModelState; the JSON shape is the Redis wire
// contract (Lua cjson on write, JSON parse on read).
type KeyModelState struct {
	CapabilityKey
	CapabilityHash         string             `json:"capabilityHash"`
	Generation             int64              `json:"generation"`
	Phase                  KeyModelPhase      `json:"phase"`
	BackoffAttempt         int                `json:"backoffAttempt"`
	RetryAtMs              *int64             `json:"retryAtMs,omitempty"`
	RecoverySuccessCount   int                `json:"recoverySuccessCount"`
	LastRecoverySuccessAtMs *int64            `json:"lastRecoverySuccessAtMs,omitempty"`
	LastObservedAtMs       int64              `json:"lastObservedAtMs"`
	LastOutcome            KeyModelOutcome    `json:"lastOutcome,omitempty"`
	ProbeLease             *KeyModelProbeLease `json:"probeLease,omitempty"`
}

// Clone returns a deep copy of the state (cloneState).
func (s KeyModelState) Clone() KeyModelState {
	out := s
	if s.RetryAtMs != nil {
		retry := *s.RetryAtMs
		out.RetryAtMs = &retry
	}
	if s.LastRecoverySuccessAtMs != nil {
		value := *s.LastRecoverySuccessAtMs
		out.LastRecoverySuccessAtMs = &value
	}
	if s.ProbeLease != nil {
		lease := *s.ProbeLease
		out.ProbeLease = &lease
	}
	return out
}

// KeyModelMutationStatus mirrors KeyModelMutationStatus.
type KeyModelMutationStatus string

// Mutation statuses.
const (
	KeyModelMutationApplied       KeyModelMutationStatus = "applied"
	KeyModelMutationIdempotent    KeyModelMutationStatus = "idempotent"
	KeyModelMutationStale         KeyModelMutationStatus = "stale"
	KeyModelMutationNotDue        KeyModelMutationStatus = "not_due"
	KeyModelMutationLeaseMismatch KeyModelMutationStatus = "lease_mismatch"
)

// KeyModelBackoffDelayMs mirrors keyModelBackoffDelayMs.
func KeyModelBackoffDelayMs(attempt int) int64 {
	index := attempt - 1
	if index < 0 {
		index = 0
	}
	if index >= len(keyModelBackoffMs) {
		index = len(keyModelBackoffMs) - 1
	}
	return keyModelBackoffMs[index]
}

// CapabilityHash mirrors capabilityHash: sha256 of the canonical JSON with
// lexicographic field order. This must stay byte identical with Node and
// jobs/keymodelrecovery.
func CapabilityHash(key CapabilityKey) (string, error) {
	normalized, err := NormalizeCapabilityKey(key)
	if err != nil {
		return "", err
	}
	payload := struct {
		ClientEndpointFamily      string `json:"clientEndpointFamily"`
		ClientModel               string `json:"clientModel"`
		CredentialSourceAccountID string `json:"credentialSourceAccountId"`
		DispatchRevision          int64  `json:"dispatchRevision"`
		FinalUpstreamModel        string `json:"finalUpstreamModel"`
		KeyFingerprint            string `json:"keyFingerprint"`
		UpstreamEndpointMode      string `json:"upstreamEndpointMode"`
	}{
		normalized.ClientEndpointFamily,
		normalized.ClientModel,
		normalized.CredentialSourceAccountID,
		normalized.DispatchRevision,
		normalized.FinalUpstreamModel,
		normalized.KeyFingerprint,
		normalized.UpstreamEndpointMode,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// CanonicalCapabilityJSON mirrors canonicalCapabilityJson.
func CanonicalCapabilityJSON(key CapabilityKey) (string, error) {
	normalized, err := NormalizeCapabilityKey(key)
	if err != nil {
		return "", err
	}
	payload := struct {
		ClientEndpointFamily      string `json:"clientEndpointFamily"`
		ClientModel               string `json:"clientModel"`
		CredentialSourceAccountID string `json:"credentialSourceAccountId"`
		DispatchRevision          int64  `json:"dispatchRevision"`
		FinalUpstreamModel        string `json:"finalUpstreamModel"`
		KeyFingerprint            string `json:"keyFingerprint"`
		UpstreamEndpointMode      string `json:"upstreamEndpointMode"`
	}{
		normalized.ClientEndpointFamily,
		normalized.ClientModel,
		normalized.CredentialSourceAccountID,
		normalized.DispatchRevision,
		normalized.FinalUpstreamModel,
		normalized.KeyFingerprint,
		normalized.UpstreamEndpointMode,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// NormalizeCapabilityKey mirrors normalizeCapabilityKey: trimmed required
// text fields and a positive dispatchRevision.
func NormalizeCapabilityKey(key CapabilityKey) (CapabilityKey, error) {
	normalized := CapabilityKey{}
	fields := []struct {
		name  string
		value string
		dest  *string
	}{
		{"credentialSourceAccountId", key.CredentialSourceAccountID, &normalized.CredentialSourceAccountID},
		{"keyFingerprint", key.KeyFingerprint, &normalized.KeyFingerprint},
		{"clientModel", key.ClientModel, &normalized.ClientModel},
		{"clientEndpointFamily", key.ClientEndpointFamily, &normalized.ClientEndpointFamily},
		{"finalUpstreamModel", key.FinalUpstreamModel, &normalized.FinalUpstreamModel},
		{"upstreamEndpointMode", key.UpstreamEndpointMode, &normalized.UpstreamEndpointMode},
	}
	for _, field := range fields {
		value := trimSpace(field.value)
		if value == "" {
			return CapabilityKey{}, fmt.Errorf("CapabilityKey 缺少 %s", field.name)
		}
		*field.dest = value
	}
	if !isSafeInteger(key.DispatchRevision) || key.DispatchRevision < 1 {
		return CapabilityKey{}, errors.New("CapabilityKey dispatchRevision 必须是正整数")
	}
	normalized.DispatchRevision = key.DispatchRevision
	return normalized, nil
}

// CreateKeyModelOpenState mirrors createKeyModelOpenState.
func CreateKeyModelOpenState(key CapabilityKey, nowMs int64) (KeyModelState, error) {
	normalized, err := NormalizeCapabilityKey(key)
	if err != nil {
		return KeyModelState{}, err
	}
	hash, err := CapabilityHash(normalized)
	if err != nil {
		return KeyModelState{}, err
	}
	retryAt := nowMs + KeyModelBackoffDelayMs(1)
	return KeyModelState{
		CapabilityKey:       normalized,
		CapabilityHash:      hash,
		Generation:          1,
		Phase:               KeyModelPhaseOpen,
		BackoffAttempt:      1,
		RetryAtMs:           &retryAt,
		RecoverySuccessCount: 0,
		LastObservedAtMs:    nowMs,
		LastOutcome:         KeyModelOutcomeUpstreamNotComplete,
	}, nil
}

// SettleKeyModelRecoveryInput mirrors the settle input.
type SettleKeyModelRecoveryInput struct {
	Generation       int64
	DispatchRevision int64
	LeaseID          string
	Outcome          KeyModelOutcome
	NowMs            int64
}

// SettleKeyModelRecovery mirrors settleKeyModelRecovery.
func SettleKeyModelRecovery(state KeyModelState, input SettleKeyModelRecoveryInput) (KeyModelMutationStatus, KeyModelState) {
	current := state.Clone()
	if current.Generation != input.Generation || current.DispatchRevision != input.DispatchRevision {
		return KeyModelMutationStale, current
	}
	if current.Phase != KeyModelPhaseHalfOpen || current.ProbeLease == nil || current.ProbeLease.LeaseID != input.LeaseID {
		return KeyModelMutationLeaseMismatch, current
	}
	if current.ProbeLease.LeaseUntilMs < input.NowMs {
		return KeyModelMutationStale, current
	}

	current.ProbeLease = nil
	current.LastObservedAtMs = input.NowMs
	current.LastOutcome = input.Outcome
	if input.Outcome == KeyModelOutcomeUnknown {
		if current.RecoverySuccessCount > 0 {
			current.Phase = KeyModelPhaseRecovering
		} else {
			current.Phase = KeyModelPhaseOpen
		}
		retryAt := input.NowMs + KeyModelRecoveryIntervalMs
		current.RetryAtMs = &retryAt
		return KeyModelMutationApplied, current
	}
	if input.Outcome == KeyModelOutcomeUpstreamNotComplete {
		current.Phase = KeyModelPhaseOpen
		current.BackoffAttempt = minInt(4, current.BackoffAttempt+1)
		current.RecoverySuccessCount = 0
		current.LastRecoverySuccessAtMs = nil
		retryAt := input.NowMs + KeyModelBackoffDelayMs(current.BackoffAttempt)
		current.RetryAtMs = &retryAt
		return KeyModelMutationApplied, current
	}

	withinGap := current.LastRecoverySuccessAtMs == nil ||
		input.NowMs-*current.LastRecoverySuccessAtMs <= KeyModelRecoverySuccessMaxGapMs
	nextSuccessCount := 1
	if withinGap {
		nextSuccessCount = current.RecoverySuccessCount + 1
	}
	if nextSuccessCount >= KeyModelRecoverySuccessThreshold {
		closed := current
		closed.Phase = KeyModelPhaseClosed
		closed.BackoffAttempt = 0
		closed.RetryAtMs = nil
		closed.RecoverySuccessCount = 0
		closed.LastRecoverySuccessAtMs = nil
		return KeyModelMutationApplied, closed
	}
	current.Phase = KeyModelPhaseRecovering
	current.RecoverySuccessCount = nextSuccessCount
	lastSuccess := input.NowMs
	current.LastRecoverySuccessAtMs = &lastSuccess
	retryAt := input.NowMs + KeyModelRecoveryIntervalMs
	current.RetryAtMs = &retryAt
	return KeyModelMutationApplied, current
}

// AcquireKeyModelRecoveryLeaseInput mirrors the acquire input.
type AcquireKeyModelRecoveryLeaseInput struct {
	Generation       int64
	DispatchRevision int64
	LeaseID          string
	NowMs            int64
}

// AcquireKeyModelRecoveryLease mirrors acquireKeyModelRecoveryLease. The
// requiredText throw on an empty leaseId becomes the error return.
func AcquireKeyModelRecoveryLease(state KeyModelState, input AcquireKeyModelRecoveryLeaseInput) (KeyModelMutationStatus, KeyModelState, error) {
	current := state.Clone()
	if current.Generation != input.Generation || current.DispatchRevision != input.DispatchRevision {
		return KeyModelMutationStale, current, nil
	}
	due := current.RetryAtMs == nil || *current.RetryAtMs <= input.NowMs
	if (current.Phase != KeyModelPhaseOpen && current.Phase != KeyModelPhaseRecovering) || !due {
		return KeyModelMutationNotDue, current, nil
	}
	if current.ProbeLease != nil && current.ProbeLease.LeaseUntilMs >= input.NowMs {
		return KeyModelMutationLeaseMismatch, current, nil
	}
	leaseID := trimSpace(input.LeaseID)
	if leaseID == "" {
		return KeyModelMutationLeaseMismatch, current, errors.New("CapabilityKey 缺少 leaseId")
	}
	current.Phase = KeyModelPhaseHalfOpen
	current.ProbeLease = &KeyModelProbeLease{
		LeaseID:          leaseID,
		LeaseUntilMs:     input.NowMs + KeyModelProbeLeaseMs,
		PriorSuccessCount: current.RecoverySuccessCount,
	}
	return KeyModelMutationApplied, current, nil
}

// IsKeyModelBlocked mirrors isKeyModelBlocked.
func IsKeyModelBlocked(state KeyModelState) bool {
	return state.Phase != KeyModelPhaseClosed
}

// KeyModelForegroundDecision mirrors KeyModelForegroundDecision.
type KeyModelForegroundDecision string

// Foreground admission decisions.
const (
	ForegroundAdmitted KeyModelForegroundDecision = "admitted"
	ForegroundBusy     KeyModelForegroundDecision = "busy"
	ForegroundBlocked  KeyModelForegroundDecision = "blocked"
)

// DecideKeyModelForegroundAdmission mirrors decideKeyModelForegroundAdmission.
// The Redis adapter owns the count; this pure decision keeps the account
// concurrency release rule independent from any status/error-body heuristic.
func DecideKeyModelForegroundAdmission(phase KeyModelPhase, activeUncommitted int) (KeyModelForegroundDecision, error) {
	if phase != KeyModelPhaseClosed {
		return ForegroundBlocked, nil
	}
	if activeUncommitted < 0 || int64(activeUncommitted) > safeIntegerMax {
		return "", fmt.Errorf("foreground activeUncommitted 无效")
	}
	if activeUncommitted >= KeyModelForegroundLimit {
		return ForegroundBusy, nil
	}
	return ForegroundAdmitted, nil
}

// MainProbeRoute mirrors MainProbeRoute.
type MainProbeRoute struct {
	ClientModel          string
	ClientEndpointFamily string
	FinalUpstreamModel   string
	UpstreamEndpointMode string
}

// MatchesMainProbeRoute mirrors matchesMainProbeRoute.
func MatchesMainProbeRoute(key CapabilityKey, main MainProbeRoute) bool {
	return trimSpace(key.ClientModel) == trimSpace(main.ClientModel) &&
		trimSpace(key.ClientEndpointFamily) == trimSpace(main.ClientEndpointFamily) &&
		trimSpace(key.FinalUpstreamModel) == trimSpace(main.FinalUpstreamModel) &&
		trimSpace(key.UpstreamEndpointMode) == trimSpace(main.UpstreamEndpointMode)
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
