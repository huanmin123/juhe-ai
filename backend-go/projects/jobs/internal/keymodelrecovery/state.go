// Package keymodelrecovery implements the deterministic key_model transition
// contract. Redis persistence and credential input are adapters around this
// core; they must not change its state decisions.
package keymodelrecovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

const (
	ScanInterval              = time.Second
	ProbeTimeout              = 30 * time.Second
	ProbeLease                = 45 * time.Second
	ProbeLeaseRenew           = 10 * time.Second
	RecoverySuccessMaxGap     = 2 * time.Minute
	RecoveryProbeInterval     = 10 * time.Second
	RecoverySuccessThreshold  = 3
	ForegroundLimit           = 2
	ContinuationGlobalReserve = 8
	ContinuationSourceReserve = 1
	ContinuationStartSLO      = 45 * time.Second
	RecoveryGlobalLimit       = 32
	RecoverySourceLimit       = 2
)

type Phase string

const (
	Closed     Phase = "CLOSED"
	Open       Phase = "OPEN"
	HalfOpen   Phase = "HALF_OPEN"
	Recovering Phase = "RECOVERING"
)

type Outcome string

const (
	CompleteSuccess     Outcome = "complete_success"
	UpstreamNotComplete Outcome = "upstream_not_complete"
	Unknown             Outcome = "unknown"
)

type CapabilityKey struct {
	CredentialSourceAccountID string `json:"credentialSourceAccountId"`
	KeyFingerprint            string `json:"keyFingerprint"`
	ClientModel               string `json:"clientModel"`
	ClientEndpointFamily      string `json:"clientEndpointFamily"`
	FinalUpstreamModel        string `json:"finalUpstreamModel"`
	UpstreamEndpointMode      string `json:"upstreamEndpointMode"`
	DispatchRevision          int64  `json:"dispatchRevision"`
}

type State struct {
	CapabilityKey
	CapabilityHash        string    `json:"capabilityHash"`
	Generation            int64     `json:"generation"`
	Phase                 Phase     `json:"phase"`
	BackoffAttempt        int       `json:"backoffAttempt"`
	RetryAt               time.Time `json:"-"`
	RecoverySuccessCount  int       `json:"recoverySuccessCount"`
	LastRecoverySuccessAt time.Time `json:"-"`
	LastObservedAt        time.Time `json:"-"`
	LastOutcome           Outcome   `json:"lastOutcome"`
	Lease                 *Lease    `json:"probeLease,omitempty"`
}

type Lease struct {
	ID         string    `json:"leaseId"`
	Until      time.Time `json:"-"`
	PriorCount int       `json:"priorSuccessCount"`
}

type stateJSON struct {
	CapabilityKey
	CapabilityHash          string     `json:"capabilityHash"`
	Generation              int64      `json:"generation"`
	Phase                   Phase      `json:"phase"`
	BackoffAttempt          int        `json:"backoffAttempt"`
	RetryAtMS               *int64     `json:"retryAtMs,omitempty"`
	RecoverySuccessCount    int        `json:"recoverySuccessCount"`
	LastRecoverySuccessAtMS *int64     `json:"lastRecoverySuccessAtMs,omitempty"`
	LastObservedAtMS        int64      `json:"lastObservedAtMs"`
	LastOutcome             Outcome    `json:"lastOutcome,omitempty"`
	ProbeLease              *leaseJSON `json:"probeLease,omitempty"`
}

type leaseJSON struct {
	ID         string `json:"leaseId"`
	UntilMS    int64  `json:"leaseUntilMs"`
	PriorCount int    `json:"priorSuccessCount"`
}

func (state State) MarshalJSON() ([]byte, error) {
	view := stateJSON{CapabilityKey: state.CapabilityKey, CapabilityHash: state.CapabilityHash, Generation: state.Generation, Phase: state.Phase, BackoffAttempt: state.BackoffAttempt, RecoverySuccessCount: state.RecoverySuccessCount, LastObservedAtMS: state.LastObservedAt.UnixMilli(), LastOutcome: state.LastOutcome}
	if !state.RetryAt.IsZero() {
		value := state.RetryAt.UnixMilli()
		view.RetryAtMS = &value
	}
	if !state.LastRecoverySuccessAt.IsZero() {
		value := state.LastRecoverySuccessAt.UnixMilli()
		view.LastRecoverySuccessAtMS = &value
	}
	if state.Lease != nil {
		view.ProbeLease = &leaseJSON{ID: state.Lease.ID, UntilMS: state.Lease.Until.UnixMilli(), PriorCount: state.Lease.PriorCount}
	}
	return json.Marshal(view)
}

func (state *State) UnmarshalJSON(data []byte) error {
	var view stateJSON
	if err := json.Unmarshal(data, &view); err != nil {
		return err
	}
	*state = State{CapabilityKey: view.CapabilityKey, CapabilityHash: view.CapabilityHash, Generation: view.Generation, Phase: view.Phase, BackoffAttempt: view.BackoffAttempt, RecoverySuccessCount: view.RecoverySuccessCount, LastObservedAt: time.UnixMilli(view.LastObservedAtMS).UTC(), LastOutcome: view.LastOutcome}
	if view.RetryAtMS != nil {
		state.RetryAt = time.UnixMilli(*view.RetryAtMS).UTC()
	}
	if view.LastRecoverySuccessAtMS != nil {
		state.LastRecoverySuccessAt = time.UnixMilli(*view.LastRecoverySuccessAtMS).UTC()
	}
	if view.ProbeLease != nil {
		state.Lease = &Lease{ID: view.ProbeLease.ID, Until: time.UnixMilli(view.ProbeLease.UntilMS).UTC(), PriorCount: view.ProbeLease.PriorCount}
	}
	return nil
}

type MutationStatus string

const (
	Applied       MutationStatus = "applied"
	Stale         MutationStatus = "stale"
	NotDue        MutationStatus = "not_due"
	LeaseMismatch MutationStatus = "lease_mismatch"
)

type RecoveryResult struct {
	Generation       int64
	DispatchRevision int64
	LeaseID          string
	Outcome          Outcome
	ObservedAt       time.Time
}

func Backoff(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return 5 * time.Second
	case attempt == 2:
		return 15 * time.Second
	case attempt == 3:
		return time.Minute
	default:
		return 5 * time.Minute
	}
}

func Hash(key CapabilityKey) (string, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}
	// Struct field order is deliberately lexicographic to match the canonical
	// JSON contract used by Node.
	payload := struct {
		ClientEndpointFamily      string `json:"clientEndpointFamily"`
		ClientModel               string `json:"clientModel"`
		CredentialSourceAccountID string `json:"credentialSourceAccountId"`
		DispatchRevision          int64  `json:"dispatchRevision"`
		FinalUpstreamModel        string `json:"finalUpstreamModel"`
		KeyFingerprint            string `json:"keyFingerprint"`
		UpstreamEndpointMode      string `json:"upstreamEndpointMode"`
	}{key.ClientEndpointFamily, key.ClientModel, key.CredentialSourceAccountID, key.DispatchRevision, key.FinalUpstreamModel, key.KeyFingerprint, key.UpstreamEndpointMode}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (key CapabilityKey) Validate() error {
	for name, value := range map[string]string{
		"credentialSourceAccountId": key.CredentialSourceAccountID, "keyFingerprint": key.KeyFingerprint,
		"clientModel": key.ClientModel, "clientEndpointFamily": key.ClientEndpointFamily,
		"finalUpstreamModel": key.FinalUpstreamModel, "upstreamEndpointMode": key.UpstreamEndpointMode,
	} {
		if strings.TrimSpace(value) == "" {
			return errors.New("CapabilityKey 缺少 " + name)
		}
	}
	if key.DispatchRevision < 1 {
		return errors.New("CapabilityKey dispatchRevision 必须为正整数")
	}
	return nil
}

func NewOpen(key CapabilityKey, observedAt time.Time) (State, error) {
	hash, err := Hash(key)
	if err != nil {
		return State{}, err
	}
	return State{CapabilityKey: key, CapabilityHash: hash, Generation: 1, Phase: Open, BackoffAttempt: 1, RetryAt: observedAt.Add(Backoff(1)), LastObservedAt: observedAt, LastOutcome: UpstreamNotComplete}, nil
}

func Acquire(state State, generation, revision int64, leaseID string, now time.Time) (State, MutationStatus) {
	if state.Generation != generation || state.DispatchRevision != revision {
		return state, Stale
	}
	if (state.Phase != Open && state.Phase != Recovering) || now.Before(state.RetryAt) {
		return state, NotDue
	}
	if state.Lease != nil && !now.After(state.Lease.Until) {
		return state, LeaseMismatch
	}
	if strings.TrimSpace(leaseID) == "" {
		return state, LeaseMismatch
	}
	state.Phase = HalfOpen
	state.Lease = &Lease{ID: leaseID, Until: now.Add(ProbeLease), PriorCount: state.RecoverySuccessCount}
	return state, Applied
}

func Settle(state State, result RecoveryResult) (State, MutationStatus) {
	if state.Generation != result.Generation || state.DispatchRevision != result.DispatchRevision {
		return state, Stale
	}
	if state.Phase != HalfOpen || state.Lease == nil || state.Lease.ID != result.LeaseID {
		return state, LeaseMismatch
	}
	if result.ObservedAt.After(state.Lease.Until) {
		return state, Stale
	}
	state.Lease = nil
	state.LastObservedAt, state.LastOutcome = result.ObservedAt, result.Outcome
	switch result.Outcome {
	case Unknown:
		if state.RecoverySuccessCount > 0 {
			state.Phase = Recovering
		} else {
			state.Phase = Open
		}
		state.RetryAt = result.ObservedAt.Add(RecoveryProbeInterval)
	case UpstreamNotComplete:
		state.Phase, state.RecoverySuccessCount, state.LastRecoverySuccessAt = Open, 0, time.Time{}
		if state.BackoffAttempt < 4 {
			state.BackoffAttempt++
		}
		state.RetryAt = result.ObservedAt.Add(Backoff(state.BackoffAttempt))
	case CompleteSuccess:
		count := state.RecoverySuccessCount + 1
		if !state.LastRecoverySuccessAt.IsZero() && result.ObservedAt.Sub(state.LastRecoverySuccessAt) > RecoverySuccessMaxGap {
			count = 1
		}
		if count >= RecoverySuccessThreshold {
			state.Phase, state.BackoffAttempt, state.RecoverySuccessCount, state.LastRecoverySuccessAt, state.RetryAt = Closed, 0, 0, time.Time{}, time.Time{}
		} else {
			state.Phase, state.RecoverySuccessCount, state.LastRecoverySuccessAt, state.RetryAt = Recovering, count, result.ObservedAt, result.ObservedAt.Add(RecoveryProbeInterval)
		}
	default:
		return state, LeaseMismatch
	}
	return state, Applied
}

type Due struct {
	State    State
	SourceID string
}

type Running struct {
	SourceID     string
	Continuation bool
}

// PrioritizeDue enforces continuation-first selection. OPEN work can borrow a
// reserve only when no due RECOVERING continuation exists.
func PrioritizeDue(items []Due, now time.Time) []Due {
	ready := make([]Due, 0, len(items))
	for _, item := range items {
		if !now.Before(item.State.RetryAt) && (item.State.Phase == Open || item.State.Phase == Recovering) {
			ready = append(ready, item)
		}
	}
	sort.SliceStable(ready, func(i, j int) bool {
		leftContinuation, rightContinuation := ready[i].State.Phase == Recovering, ready[j].State.Phase == Recovering
		if leftContinuation != rightContinuation {
			return leftContinuation
		}
		return ready[i].State.RetryAt.Before(ready[j].State.RetryAt)
	})
	return ready
}

// SelectDue applies the fixed 32/2 limits and their 8/1 continuation reserve.
// An OPEN probe may borrow only while no due continuation is waiting.
func SelectDue(items []Due, running []Running, now time.Time) []Due {
	ordered := PrioritizeDue(items, now)
	continuationWaiting := false
	for _, item := range ordered {
		if item.State.Phase == Recovering {
			continuationWaiting = true
			break
		}
	}
	globalRunning := len(running)
	bySource := map[string]int{}
	for _, item := range running {
		bySource[item.SourceID]++
	}
	selected := make([]Due, 0, len(ordered))
	for _, item := range ordered {
		if globalRunning >= RecoveryGlobalLimit || bySource[item.SourceID] >= RecoverySourceLimit {
			continue
		}
		if item.State.Phase == Open && continuationWaiting {
			// Reserve capacity is unavailable to new OPEN work whenever a due
			// continuation exists. Already-running OPEN probes are never killed.
			if globalRunning >= RecoveryGlobalLimit-ContinuationGlobalReserve || bySource[item.SourceID] >= RecoverySourceLimit-ContinuationSourceReserve {
				continue
			}
		}
		selected = append(selected, item)
		globalRunning++
		bySource[item.SourceID]++
	}
	return selected
}
