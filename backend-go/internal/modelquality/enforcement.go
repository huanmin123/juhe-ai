package modelquality

import (
	"fmt"
	"math"
	"strings"
)

// The distinct fence types prevent callers from accidentally comparing a
// policy revision with an account or schedule revision.
type PolicyFence struct {
	Expected PolicyRevision
	Current  PolicyRevision
}

func (f PolicyFence) Matches() bool { return f.Expected == f.Current }

type AccountFence struct {
	Expected AccountRevision
	Current  AccountRevision
}

func (f AccountFence) Matches() bool { return f.Expected == f.Current }

type ScheduleFence struct {
	ScheduleID string
	Expected   ScheduleRevision
	Current    ScheduleRevision
}

func (f ScheduleFence) Validate() error {
	if strings.TrimSpace(f.ScheduleID) == "" {
		return fmt.Errorf("model quality schedule ID is required")
	}
	return nil
}

func (f ScheduleFence) Matches() bool {
	return f.Validate() == nil && f.Expected == f.Current
}

type EnforcementToken struct {
	ID         string
	Generation EnforcementGeneration
}

func (t EnforcementToken) Validate() error {
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("model quality enforcement ID is required")
	}
	if t.Generation == 0 {
		return fmt.Errorf("model quality enforcement generation must be positive")
	}
	return nil
}

type EnforcementFence struct {
	Expected EnforcementToken
	Current  EnforcementToken
}

func (f EnforcementFence) Matches() bool {
	return f.Expected.Validate() == nil && f.Current.Validate() == nil && f.Expected == f.Current
}

// NextGeneration is intentionally the only generation increment helper so an
// adapter cannot silently wrap a durable enforcement generation.
func NextGeneration(previous EnforcementGeneration) (EnforcementGeneration, error) {
	if previous == EnforcementGeneration(math.MaxUint64) {
		return 0, fmt.Errorf("model quality enforcement generation exhausted")
	}
	return previous + 1, nil
}

type Account struct {
	ID               string
	SystemAccountID  string
	Status           AccountStatus
	ConfigRevision   AccountRevision
	OwnPhysical      bool
	FallbackEnabled  bool
	SuperPrioritySet bool
}

func (a Account) Validate() error {
	if strings.TrimSpace(a.ID) == "" || strings.TrimSpace(a.SystemAccountID) == "" {
		return fmt.Errorf("model quality account identity is required")
	}
	if !validAccountStatus(a.Status) {
		return fmt.Errorf("unsupported model quality account status %q", a.Status)
	}
	return nil
}

// AllowedPenaltySourceStatus is deliberately a narrow matrix. A status not
// listed here must be changed by an explicit operator workflow, never by a
// quality check.
func AllowedPenaltySourceStatus(status AccountStatus, action Action) bool {
	switch action {
	case ActionFallback:
		return status == AccountStatusActive
	case ActionDisable:
		return status == AccountStatusActive || status == AccountStatusQualityIsolated
	case ActionQualityIsolate:
		return status == AccountStatusActive
	default:
		return false
	}
}

func ManualEnforcementAllowed(policy Policy, account Account) bool {
	return policy.ManualEnforcementEnabled && account.OwnPhysical
}

type EnforcementRequest struct {
	Trigger         Trigger
	RunID           string
	Action          Action
	PolicyRevision  PolicyRevision
	AccountRevision AccountRevision
}

func (r EnforcementRequest) Validate() error {
	if r.Trigger != TriggerManual && r.Trigger != TriggerScheduled && r.Trigger != TriggerQualityRecovery {
		return fmt.Errorf("unsupported model quality enforcement trigger %q", r.Trigger)
	}
	if r.Trigger == TriggerQualityRecovery {
		return fmt.Errorf("model quality recovery trigger must use recovery planning")
	}
	if strings.TrimSpace(r.RunID) == "" {
		return fmt.Errorf("model quality enforcement run ID is required")
	}
	if !validAction(r.Action) {
		return fmt.Errorf("unsupported model quality penalty action %q", r.Action)
	}
	if r.AccountRevision == 0 {
		return fmt.Errorf("model quality enforcement requires a non-zero account revision")
	}
	return nil
}

type EnforcementResult string

const (
	EnforcementApply            EnforcementResult = "apply"
	EnforcementAlreadyEffective EnforcementResult = "already_effective"
	EnforcementSkipped          EnforcementResult = "skipped"
	EnforcementStale            EnforcementResult = "stale"
)

type PenaltyPlan struct {
	Result       EnforcementResult
	TargetStatus AccountStatus
	// TargetSchedulable is nil when this penalty leaves scheduling unchanged.
	// A non-nil value is the desired durable scheduling state and must be
	// applied atomically with TargetStatus by the eventual control-plane adapter.
	TargetSchedulable     *bool
	SetFallbackEnabled    bool
	ClearSuperPrioritySet bool
}

// PlanEnforcement applies fencing and the pure account-state transition. The
// adapter must still atomically compare its durable row revision before it
// writes this plan.
func PlanEnforcement(request EnforcementRequest, policy Policy, account Account) (PenaltyPlan, error) {
	if err := request.Validate(); err != nil {
		return PenaltyPlan{}, err
	}
	if err := policy.Validate(); err != nil {
		return PenaltyPlan{}, err
	}
	if err := account.Validate(); err != nil {
		return PenaltyPlan{}, err
	}
	if account.SystemAccountID != policy.SystemAccountID {
		return PenaltyPlan{Result: EnforcementSkipped, TargetStatus: account.Status}, nil
	}
	if !(PolicyFence{Expected: request.PolicyRevision, Current: policy.Revision}.Matches()) || request.Action != policy.PenaltyAction {
		return PenaltyPlan{Result: EnforcementStale, TargetStatus: account.Status}, nil
	}
	if !account.OwnPhysical {
		return PenaltyPlan{Result: EnforcementSkipped, TargetStatus: account.Status}, nil
	}
	if policy.SystemAccountID != account.SystemAccountID {
		return PenaltyPlan{Result: EnforcementSkipped, TargetStatus: account.Status}, nil
	}
	if request.Trigger == TriggerManual && !ManualEnforcementAllowed(policy, account) {
		return PenaltyPlan{Result: EnforcementSkipped, TargetStatus: account.Status}, nil
	}
	if !(AccountFence{Expected: request.AccountRevision, Current: account.ConfigRevision}.Matches()) {
		return PenaltyPlan{Result: EnforcementStale, TargetStatus: account.Status}, nil
	}
	if !AllowedPenaltySourceStatus(account.Status, request.Action) {
		return PenaltyPlan{Result: EnforcementSkipped, TargetStatus: account.Status}, nil
	}
	switch request.Action {
	case ActionFallback:
		if account.FallbackEnabled {
			return PenaltyPlan{Result: EnforcementAlreadyEffective, TargetStatus: account.Status}, nil
		}
		return PenaltyPlan{Result: EnforcementApply, TargetStatus: account.Status, SetFallbackEnabled: true, ClearSuperPrioritySet: true}, nil
	case ActionDisable:
		return PenaltyPlan{Result: EnforcementApply, TargetStatus: AccountStatusDisabled, TargetSchedulable: schedulableTarget(false)}, nil
	case ActionQualityIsolate:
		return PenaltyPlan{Result: EnforcementApply, TargetStatus: AccountStatusQualityIsolated, TargetSchedulable: schedulableTarget(false)}, nil
	default:
		return PenaltyPlan{}, fmt.Errorf("unsupported model quality penalty action %q", request.Action)
	}
}

type EnforcementState struct {
	SystemAccountID string
	Token           EnforcementToken
	// AccountRevision is the account revision captured when a recovery is
	// claimed. It is deliberately refreshed for every claim: edits made after
	// isolation but before the next recovery check become part of that check's
	// baseline, while edits after the claim still invalidate its result.
	AccountRevision AccountRevision
	Active          bool
	Action          Action
}

func (s EnforcementState) Validate() error {
	if strings.TrimSpace(s.SystemAccountID) == "" {
		return fmt.Errorf("model quality enforcement system account ID is required")
	}
	if err := s.Token.Validate(); err != nil {
		return err
	}
	if s.AccountRevision == 0 {
		return fmt.Errorf("model quality enforcement requires a non-zero recovery claim account revision")
	}
	if !validAction(s.Action) {
		return fmt.Errorf("unsupported model quality penalty action %q", s.Action)
	}
	return nil
}

// RecoveryClaimRequest identifies the policy and enforcement generation a
// scheduler intends to claim. The durable adapter must atomically compare and
// persist the returned EnforcementState.AccountRevision with its lease write.
type RecoveryClaimRequest struct {
	PolicyRevision PolicyRevision
	Enforcement    EnforcementToken
}

func (r RecoveryClaimRequest) Validate() error {
	return r.Enforcement.Validate()
}

type RecoveryClaimResult string

const (
	RecoveryClaimed    RecoveryClaimResult = "claimed"
	RecoveryClaimStale RecoveryClaimResult = "stale"
)

type RecoveryClaimPlan struct {
	Result RecoveryClaimResult
	// State is the generation state that must be persisted with the claim. It
	// carries the refreshed account revision only when Result is
	// RecoveryClaimed.
	State EnforcementState
}

// ClaimRecovery mirrors Node's due-recovery claim update: it captures the
// account's *current* config revision, rather than retaining the revision from
// when the account first entered isolation. This is a pure transition; lease
// ownership, due-time selection, and atomic storage writes remain adapter
// responsibilities.
func ClaimRecovery(request RecoveryClaimRequest, currentPolicyRevision PolicyRevision, current EnforcementState, account Account) (RecoveryClaimPlan, error) {
	if err := request.Validate(); err != nil {
		return RecoveryClaimPlan{}, err
	}
	if err := current.Validate(); err != nil {
		return RecoveryClaimPlan{}, err
	}
	if err := account.Validate(); err != nil {
		return RecoveryClaimPlan{}, err
	}
	if account.ConfigRevision == 0 {
		return RecoveryClaimPlan{}, fmt.Errorf("model quality recovery claim requires a non-zero account revision")
	}
	if !current.Active || current.Action != ActionQualityIsolate || account.Status != AccountStatusQualityIsolated ||
		current.SystemAccountID != account.SystemAccountID ||
		!(EnforcementFence{Expected: request.Enforcement, Current: current.Token}.Matches()) ||
		!(PolicyFence{Expected: request.PolicyRevision, Current: currentPolicyRevision}.Matches()) {
		return RecoveryClaimPlan{Result: RecoveryClaimStale, State: current}, nil
	}
	claimed := current
	claimed.AccountRevision = account.ConfigRevision
	return RecoveryClaimPlan{Result: RecoveryClaimed, State: claimed}, nil
}

type RecoveryRequest struct {
	PolicyRevision  PolicyRevision
	AccountRevision AccountRevision
	Enforcement     EnforcementToken
	Passed          bool
}

func (r RecoveryRequest) Validate() error {
	if err := r.Enforcement.Validate(); err != nil {
		return err
	}
	if r.AccountRevision == 0 {
		return fmt.Errorf("model quality recovery requires a non-zero account revision")
	}
	return nil
}

type RecoveryResult string

const (
	RecoveryRecovered    RecoveryResult = "recovered"
	RecoveryKeptIsolated RecoveryResult = "kept_isolated"
	RecoveryStale        RecoveryResult = "stale"
)

type RecoveryPlan struct {
	Result       RecoveryResult
	TargetStatus AccountStatus
	// TargetSchedulable is nil when this recovery result must not change
	// scheduling. A recovered active account becomes schedulable; a recovered
	// disabled account remains unschedulable.
	TargetSchedulable *bool
	NeedsReschedule   bool
}

// PlanRecovery retains Node's safety rule: only the same active
// quality-isolation generation may recover an account. Time arithmetic and
// lease ownership belong to the scheduling adapter.
func PlanRecovery(request RecoveryRequest, currentPolicyRevision PolicyRevision, current EnforcementState, account Account, availableNow bool) (RecoveryPlan, error) {
	if err := request.Validate(); err != nil {
		return RecoveryPlan{}, err
	}
	if err := current.Validate(); err != nil {
		return RecoveryPlan{}, err
	}
	if err := account.Validate(); err != nil {
		return RecoveryPlan{}, err
	}
	if !current.Active || current.Action != ActionQualityIsolate || !(EnforcementFence{Expected: request.Enforcement, Current: current.Token}.Matches()) {
		return RecoveryPlan{Result: RecoveryStale, TargetStatus: account.Status}, nil
	}
	if current.SystemAccountID != account.SystemAccountID {
		return RecoveryPlan{Result: RecoveryStale, TargetStatus: account.Status}, nil
	}
	if !(PolicyFence{Expected: request.PolicyRevision, Current: currentPolicyRevision}.Matches()) {
		return RecoveryPlan{Result: RecoveryStale, TargetStatus: account.Status, NeedsReschedule: true}, nil
	}
	// The request is based on the revision claimed for this recovery, the
	// enforcement records that same recovery-claim snapshot, and the account
	// exposes the current revision. All three must agree before either
	// a successful or failed recovery is acted on. If an account edit raced the
	// check, retain its current isolation and ask the scheduler to claim a new
	// recovery instead of applying an obsolete result.
	if request.AccountRevision != current.AccountRevision || current.AccountRevision != account.ConfigRevision {
		return RecoveryPlan{Result: RecoveryStale, TargetStatus: account.Status, NeedsReschedule: true}, nil
	}
	if !request.Passed {
		return RecoveryPlan{Result: RecoveryKeptIsolated, TargetStatus: AccountStatusQualityIsolated, NeedsReschedule: true}, nil
	}
	if account.Status != AccountStatusQualityIsolated {
		return RecoveryPlan{Result: RecoveryStale, TargetStatus: account.Status}, nil
	}
	if availableNow {
		return RecoveryPlan{Result: RecoveryRecovered, TargetStatus: AccountStatusActive, TargetSchedulable: schedulableTarget(true)}, nil
	}
	return RecoveryPlan{Result: RecoveryRecovered, TargetStatus: AccountStatusDisabled, TargetSchedulable: schedulableTarget(false)}, nil
}

func schedulableTarget(value bool) *bool {
	return &value
}

func validAccountStatus(status AccountStatus) bool {
	return status == AccountStatusActive || status == AccountStatusPendingTest || status == AccountStatusDisabled || status == AccountStatusError || status == AccountStatusRateLimited || status == AccountStatusTemporaryUnavailable || status == AccountStatusQualityIsolated
}
