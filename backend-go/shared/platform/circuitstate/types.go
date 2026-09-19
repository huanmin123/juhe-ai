package circuitstate

import (
	"encoding/json"
	"strings"
)

// Scope mirrors AccountCircuitScope. Kind selects the active fields exactly
// like the Node discriminated union.
//
// REFACTOR-0008（跨模块成对收敛）：gateway/internal/gatewaycircuit 与
// jobs/internal/circuitstore 对同一份共享账户熔断运行态（Redis）各有一份
// 逐字节相同的实现；词汇与脚本下潜到本包作为单一事实，两侧保留类型别名。
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

// StringList decodes Lua round-tripped JSON arrays: an empty Lua array is
// encoded as `{}`, which must behave like an empty list (Node tolerates the
// same shapes through cloneStringArray).
type StringList []string

func (l StringList) clone() StringList {
	if l == nil {
		return nil
	}
	out := make(StringList, len(l))
	copy(out, l)
	return out
}

func (l *StringList) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		*l = nil
		return nil
	}
	if trimmed == "{}" || trimmed == "[]" {
		*l = StringList{}
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
	FailureEvidenceKeys          StringList `json:"failureEvidenceKeys,omitempty"`
	OpenedAtMs                   *int64     `json:"openedAtMs,omitempty"`
	RetryAtMs                    *int64     `json:"retryAtMs,omitempty"`
	FailureReason                *string    `json:"failureReason,omitempty"`
	Lease                        *Lease     `json:"lease,omitempty"`
	HalfOpenOrigin               *string    `json:"halfOpenOrigin,omitempty"`
	IncidentID                   *string    `json:"incidentId,omitempty"`
	ShadowedByIncidentID         *string    `json:"shadowedByIncidentId,omitempty"`
	ChildIncidentIDs             StringList `json:"childIncidentIds,omitempty"`
	ChildScopeKeys               StringList `json:"childScopeKeys,omitempty"`
	RequiredRecoveryScopeKeys    StringList `json:"requiredRecoveryScopeKeys,omitempty"`
	RecoveryEvidenceScopeKeys    StringList `json:"recoveryEvidenceScopeKeys,omitempty"`
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

// StateList decodes Lua-encoded `relatedStates`: an empty Lua array is
// encoded as `{}`, which must decode as an empty list.
type StateList []State

func (l *StateList) UnmarshalJSON(raw []byte) error {
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

// Slice returns the states as a plain slice (nil stays nil, non-nil copies).
func (l StateList) Slice() []State {
	if l == nil {
		return nil
	}
	return append([]State{}, l...)
}

// MutationResult mirrors AccountCircuitMutationResult.
type MutationResult struct {
	Status        string    `json:"status"`
	State         State     `json:"state"`
	RelatedStates StateList `json:"relatedStates,omitempty"`
}

// RelatedStatesSlice returns the related states as a plain slice.
func (r MutationResult) RelatedStatesSlice() []State { return r.RelatedStates.Slice() }

// ScopeKey and MustScopeKey stay in the owning modules on purpose: the two
// module-local ScopeKey implementations have already textually drifted
// (kind/request-lane constants vs inline literals) while behaving the same,
// so per the REFACTOR-0008 reconciliation they are not sunk here and this
// package does not grow a third key-encoding implementation.
