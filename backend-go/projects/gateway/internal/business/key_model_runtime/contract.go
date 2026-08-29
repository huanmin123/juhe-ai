package keymodelruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Phase string

const (
	PhaseClosed     Phase = "CLOSED"
	PhaseOpen       Phase = "OPEN"
	PhaseHalfOpen   Phase = "HALF_OPEN"
	PhaseRecovering Phase = "RECOVERING"
)

type Outcome string

const (
	OutcomeCompleteSuccess    Outcome = "complete_success"
	OutcomeUpstreamIncomplete Outcome = "upstream_not_complete"
	OutcomeUnknown            Outcome = "unknown"
)

type Capability struct {
	CredentialSourceAccountID string `json:"credentialSourceAccountId"`
	KeyFingerprint            string `json:"keyFingerprint"`
	ClientModel               string `json:"clientModel"`
	ClientEndpointFamily      string `json:"clientEndpointFamily"`
	FinalUpstreamModel        string `json:"finalUpstreamModel"`
	UpstreamEndpointMode      string `json:"upstreamEndpointMode"`
	DispatchRevision          int64  `json:"dispatchRevision"`
}

type State struct {
	Capability
	CapabilityHash        string    `json:"capabilityHash"`
	Generation            int       `json:"generation"`
	Phase                 Phase     `json:"phase"`
	BackoffAttempt        int       `json:"backoffAttempt"`
	RetryAt               time.Time `json:"retryAt,omitempty"`
	RecoverySuccessCount  int       `json:"recoverySuccessCount"`
	LastRecoverySuccessAt time.Time `json:"lastRecoverySuccessAt,omitempty"`
	LastObservedAt        time.Time `json:"lastObservedAt"`
	LastOutcome           Outcome   `json:"lastOutcome,omitempty"`
	ProbeLease            *Lease    `json:"probeLease,omitempty"`
}

type Lease struct {
	ID             string    `json:"leaseId"`
	Until          time.Time `json:"leaseUntil"`
	PriorSuccesses int       `json:"priorSuccesses"`
}

type stateJSON struct {
	Capability
	CapabilityHash          string  `json:"capabilityHash"`
	Generation              int     `json:"generation"`
	Phase                   Phase   `json:"phase"`
	BackoffAttempt          int     `json:"backoffAttempt"`
	RetryAtMS               *int64  `json:"retryAtMs,omitempty"`
	RecoverySuccessCount    int     `json:"recoverySuccessCount"`
	LastRecoverySuccessAtMS *int64  `json:"lastRecoverySuccessAtMs,omitempty"`
	LastObservedAtMS        int64   `json:"lastObservedAtMs"`
	LastOutcome             Outcome `json:"lastOutcome,omitempty"`
	ProbeLease              *struct {
		ID             string `json:"leaseId"`
		UntilMS        int64  `json:"leaseUntilMs"`
		PriorSuccesses int    `json:"priorSuccessCount"`
	} `json:"probeLease,omitempty"`
}

func (state State) MarshalJSON() ([]byte, error) {
	view := stateJSON{Capability: state.Capability, CapabilityHash: state.CapabilityHash, Generation: state.Generation, Phase: state.Phase, BackoffAttempt: state.BackoffAttempt, RecoverySuccessCount: state.RecoverySuccessCount, LastObservedAtMS: state.LastObservedAt.UnixMilli(), LastOutcome: state.LastOutcome}
	if !state.RetryAt.IsZero() {
		value := state.RetryAt.UnixMilli()
		view.RetryAtMS = &value
	}
	if !state.LastRecoverySuccessAt.IsZero() {
		value := state.LastRecoverySuccessAt.UnixMilli()
		view.LastRecoverySuccessAtMS = &value
	}
	if state.ProbeLease != nil {
		view.ProbeLease = &struct {
			ID             string `json:"leaseId"`
			UntilMS        int64  `json:"leaseUntilMs"`
			PriorSuccesses int    `json:"priorSuccessCount"`
		}{state.ProbeLease.ID, state.ProbeLease.Until.UnixMilli(), state.ProbeLease.PriorSuccesses}
	}
	return json.Marshal(view)
}

func (state *State) UnmarshalJSON(data []byte) error {
	var view stateJSON
	if err := json.Unmarshal(data, &view); err != nil {
		return err
	}
	*state = State{Capability: view.Capability, CapabilityHash: view.CapabilityHash, Generation: view.Generation, Phase: view.Phase, BackoffAttempt: view.BackoffAttempt, RecoverySuccessCount: view.RecoverySuccessCount, LastObservedAt: time.UnixMilli(view.LastObservedAtMS).UTC(), LastOutcome: view.LastOutcome}
	if view.RetryAtMS != nil {
		state.RetryAt = time.UnixMilli(*view.RetryAtMS).UTC()
	}
	if view.LastRecoverySuccessAtMS != nil {
		state.LastRecoverySuccessAt = time.UnixMilli(*view.LastRecoverySuccessAtMS).UTC()
	}
	if view.ProbeLease != nil {
		state.ProbeLease = &Lease{ID: view.ProbeLease.ID, Until: time.UnixMilli(view.ProbeLease.UntilMS).UTC(), PriorSuccesses: view.ProbeLease.PriorSuccesses}
	}
	return nil
}

type MutationStatus string

const (
	StatusApplied       MutationStatus = "applied"
	StatusIdempotent    MutationStatus = "idempotent"
	StatusStale         MutationStatus = "stale"
	StatusNotDue        MutationStatus = "not_due"
	StatusLeaseMismatch MutationStatus = "lease_mismatch"
)

const (
	RecoverySuccessThreshold = 3
	RecoverySuccessMaxGap    = 2 * time.Minute
	RecoveryInterval         = 10 * time.Second
	ProbeLeaseDuration       = 45 * time.Second
	// Go owns the runtime and must not inherit Node's event-loop concurrency
	// cap. Redis leases still provide per-attempt fencing; this is only a
	// defensive upper bound for malformed callers.
	ForegroundLimit = 100000
)

var backoff = [...]time.Duration{5 * time.Second, 15 * time.Second, time.Minute, 5 * time.Minute}

func BackoffDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(backoff) {
		attempt = len(backoff)
	}
	return backoff[attempt-1]
}

func NormalizeCapability(in Capability) (Capability, error) {
	fields := map[string]*string{
		"credentialSourceAccountId": &in.CredentialSourceAccountID,
		"keyFingerprint":            &in.KeyFingerprint,
		"clientModel":               &in.ClientModel,
		"clientEndpointFamily":      &in.ClientEndpointFamily,
		"finalUpstreamModel":        &in.FinalUpstreamModel,
		"upstreamEndpointMode":      &in.UpstreamEndpointMode,
	}
	for name, value := range fields {
		*value = strings.TrimSpace(*value)
		if *value == "" {
			return Capability{}, fmt.Errorf("key-model %s is required", name)
		}
	}
	if in.DispatchRevision < 1 {
		return Capability{}, errors.New("key-model dispatch revision must be positive")
	}
	return in, nil
}

func HashCapability(in Capability) (string, error) {
	normalized, err := NormalizeCapability(in)
	if err != nil {
		return "", err
	}
	// Keep the exact lexicographic property order used by Node's
	// canonicalCapabilityJson; changing struct declaration order must not
	// change the capability hash or break Node/Go Redis interoperability.
	raw, err := json.Marshal(struct {
		ClientEndpointFamily      string `json:"clientEndpointFamily"`
		ClientModel               string `json:"clientModel"`
		CredentialSourceAccountID string `json:"credentialSourceAccountId"`
		DispatchRevision          int64  `json:"dispatchRevision"`
		FinalUpstreamModel        string `json:"finalUpstreamModel"`
		KeyFingerprint            string `json:"keyFingerprint"`
		UpstreamEndpointMode      string `json:"upstreamEndpointMode"`
	}{normalized.ClientEndpointFamily, normalized.ClientModel, normalized.CredentialSourceAccountID, normalized.DispatchRevision, normalized.FinalUpstreamModel, normalized.KeyFingerprint, normalized.UpstreamEndpointMode})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func Open(capability Capability, now time.Time) (State, error) {
	normalized, err := NormalizeCapability(capability)
	if err != nil {
		return State{}, err
	}
	if now.IsZero() {
		return State{}, errors.New("key-model observed time is required")
	}
	hash, err := HashCapability(normalized)
	if err != nil {
		return State{}, err
	}
	return State{Capability: normalized, CapabilityHash: hash, Generation: 1, Phase: PhaseOpen, BackoffAttempt: 1, RetryAt: now.Add(BackoffDelay(1)), RecoverySuccessCount: 0, LastObservedAt: now, LastOutcome: OutcomeUpstreamIncomplete}, nil
}

func IsBlocked(state State) bool { return state.Phase != PhaseClosed }

func AcquireRecoveryLease(state State, generation int, revision int64, leaseID string, now time.Time) (MutationStatus, State) {
	state = clone(state)
	if state.Generation != generation || state.DispatchRevision != revision {
		return StatusStale, state
	}
	if state.Phase != PhaseOpen && state.Phase != PhaseRecovering || state.RetryAt.IsZero() || state.RetryAt.After(now) {
		return StatusNotDue, state
	}
	if state.ProbeLease != nil && !state.ProbeLease.Until.Before(now) {
		return StatusLeaseMismatch, state
	}
	if strings.TrimSpace(leaseID) == "" {
		return StatusLeaseMismatch, state
	}
	state.Phase = PhaseHalfOpen
	state.ProbeLease = &Lease{ID: leaseID, Until: now.Add(ProbeLeaseDuration), PriorSuccesses: state.RecoverySuccessCount}
	return StatusApplied, state
}

func SettleRecovery(state State, generation int, revision int64, leaseID string, outcome Outcome, now time.Time) (MutationStatus, State) {
	state = clone(state)
	if state.Generation != generation || state.DispatchRevision != revision {
		return StatusStale, state
	}
	if state.Phase != PhaseHalfOpen || state.ProbeLease == nil || state.ProbeLease.ID != leaseID || state.ProbeLease.Until.Before(now) {
		return StatusLeaseMismatch, state
	}
	state.ProbeLease = nil
	state.LastObservedAt, state.LastOutcome = now, outcome
	switch outcome {
	case OutcomeUnknown:
		state.Phase = PhaseOpen
		if state.RecoverySuccessCount > 0 {
			state.Phase = PhaseRecovering
		}
		state.RetryAt = now.Add(RecoveryInterval)
	case OutcomeUpstreamIncomplete:
		state.Phase = PhaseOpen
		state.BackoffAttempt++
		if state.BackoffAttempt > len(backoff) {
			state.BackoffAttempt = len(backoff)
		}
		state.RecoverySuccessCount = 0
		state.LastRecoverySuccessAt = time.Time{}
		state.RetryAt = now.Add(BackoffDelay(state.BackoffAttempt))
	case OutcomeCompleteSuccess:
		if state.LastRecoverySuccessAt.IsZero() || now.Sub(state.LastRecoverySuccessAt) <= RecoverySuccessMaxGap {
			state.RecoverySuccessCount++
		} else {
			state.RecoverySuccessCount = 1
		}
		if state.RecoverySuccessCount >= RecoverySuccessThreshold {
			state.Phase, state.BackoffAttempt, state.RetryAt, state.RecoverySuccessCount, state.LastRecoverySuccessAt = PhaseClosed, 0, time.Time{}, 0, time.Time{}
		} else {
			state.Phase, state.LastRecoverySuccessAt, state.RetryAt = PhaseRecovering, now, now.Add(RecoveryInterval)
		}
	default:
		return StatusLeaseMismatch, state
	}
	return StatusApplied, state
}

func clone(state State) State {
	if state.ProbeLease != nil {
		lease := *state.ProbeLease
		state.ProbeLease = &lease
	}
	return state
}
