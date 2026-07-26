// Package modelquality contains deterministic policy decisions for model
// quality checks. Persistence, leases, scheduling, probing, and account
// mutation deliberately remain outside this package.
package modelquality

import (
	"fmt"
	"math"
	"strings"
)

type PolicyRevision uint64
type AccountRevision uint64
type ScheduleRevision uint64
type EnforcementGeneration uint64

type Profile string

const (
	ProfileQuick Profile = "quick"
	ProfileFull  Profile = "full"
)

type Action string

const (
	ActionDisable        Action = "disable"
	ActionFallback       Action = "fallback"
	ActionQualityIsolate Action = "quality_isolate"
)

type Trigger string

const (
	TriggerManual          Trigger = "manual"
	TriggerScheduled       Trigger = "scheduled"
	TriggerQualityRecovery Trigger = "quality_recovery"
)

type AccountStatus string

const (
	AccountStatusActive               AccountStatus = "active"
	AccountStatusPendingTest          AccountStatus = "pending_test"
	AccountStatusDisabled             AccountStatus = "disabled"
	AccountStatusError                AccountStatus = "error"
	AccountStatusRateLimited          AccountStatus = "rate_limited"
	AccountStatusTemporaryUnavailable AccountStatus = "temporary_unavailable"
	AccountStatusQualityIsolated      AccountStatus = "quality_isolated"
)

type RunStatus string

const (
	RunStatusRunning   RunStatus = "running"
	RunStatusCompleted RunStatus = "completed"
	RunStatusFailed    RunStatus = "failed"
	RunStatusCanceled  RunStatus = "canceled"
)

type Level string

const (
	LevelHighConfidence Level = "high_confidence"
	LevelLikely         Level = "likely"
	LevelUncertain      Level = "uncertain"
	LevelSuspicious     Level = "suspicious"
	LevelUnavailable    Level = "unavailable"
)

type MappingStatus string

const (
	MappingStatusDirect             MappingStatus = "direct"
	MappingStatusConfigured         MappingStatus = "configured_mapping"
	MappingStatusUndeclaredMismatch MappingStatus = "undeclared_mismatch"
	MappingStatusUnknown            MappingStatus = "unknown"
)

type ProtocolStatus string

const (
	ProtocolStatusConsistent           ProtocolStatus = "consistent"
	ProtocolStatusWarning              ProtocolStatus = "warning"
	ProtocolStatusFailed               ProtocolStatus = "failed"
	ProtocolStatusInsufficientEvidence ProtocolStatus = "insufficient_evidence"
)

// Policy is the current policy state. Revision zero is the Node-compatible
// implicit default policy and remains a valid revision.
type Policy struct {
	SystemAccountID          string
	Revision                 PolicyRevision
	Profile                  Profile
	ManualEnforcementEnabled bool
	PenaltyThreshold         int
	PenaltyAction            Action
	RecoveryIntervalMinutes  int
}

func DefaultPolicy(systemAccountID string) Policy {
	return Policy{
		SystemAccountID:          strings.TrimSpace(systemAccountID),
		Revision:                 0,
		Profile:                  ProfileQuick,
		ManualEnforcementEnabled: true,
		PenaltyThreshold:         70,
		PenaltyAction:            ActionFallback,
		RecoveryIntervalMinutes:  10,
	}
}

func (p Policy) Validate() error {
	if strings.TrimSpace(p.SystemAccountID) == "" {
		return fmt.Errorf("model quality policy system account ID is required")
	}
	if p.Profile != ProfileQuick && p.Profile != ProfileFull {
		return fmt.Errorf("unsupported model quality profile %q", p.Profile)
	}
	if p.PenaltyThreshold < 40 || p.PenaltyThreshold > 100 {
		return fmt.Errorf("model quality penalty threshold must be an integer from 40 to 100")
	}
	if !validAction(p.PenaltyAction) {
		return fmt.Errorf("unsupported model quality penalty action %q", p.PenaltyAction)
	}
	if p.RecoveryIntervalMinutes < 10 || p.RecoveryIntervalMinutes > 10080 {
		return fmt.Errorf("model quality recovery interval must be an integer from 10 to 10080 minutes")
	}
	return nil
}

type PolicyUpdate struct {
	ExpectedRevision         PolicyRevision
	Profile                  Profile
	ManualEnforcementEnabled bool
	PenaltyThreshold         int
	PenaltyAction            Action
	RecoveryIntervalMinutes  int
}

func (u PolicyUpdate) Validate() error {
	return Policy{
		SystemAccountID:          "policy-update",
		Profile:                  u.Profile,
		ManualEnforcementEnabled: u.ManualEnforcementEnabled,
		PenaltyThreshold:         u.PenaltyThreshold,
		PenaltyAction:            u.PenaltyAction,
		RecoveryIntervalMinutes:  u.RecoveryIntervalMinutes,
	}.Validate()
}

// PolicySnapshot is captured with a run. ScheduleRevision is intentionally
// explicit in Go: Node currently snapshots schedule ID but its completion
// fencing is separate from the run snapshot.
type PolicySnapshot struct {
	PolicyRevision           PolicyRevision
	Profile                  Profile
	ManualEnforcementEnabled bool
	Threshold                int
	Action                   Action
	RecoveryIntervalMinutes  int
	ScheduleID               string
	ScheduleRevision         ScheduleRevision
	AccountRevision          AccountRevision
}

func Snapshot(policy Policy, accountRevision AccountRevision, scheduleID string, scheduleRevision ScheduleRevision) (PolicySnapshot, error) {
	if err := policy.Validate(); err != nil {
		return PolicySnapshot{}, err
	}
	if strings.TrimSpace(scheduleID) == "" && scheduleRevision != 0 {
		return PolicySnapshot{}, fmt.Errorf("model quality schedule revision requires schedule ID")
	}
	return PolicySnapshot{
		PolicyRevision: policy.Revision, Profile: policy.Profile,
		ManualEnforcementEnabled: policy.ManualEnforcementEnabled,
		Threshold:                policy.PenaltyThreshold, Action: policy.PenaltyAction,
		RecoveryIntervalMinutes: policy.RecoveryIntervalMinutes,
		ScheduleID:              strings.TrimSpace(scheduleID), ScheduleRevision: scheduleRevision,
		AccountRevision: accountRevision,
	}, nil
}

type RuntimeFacts struct {
	RunStatus      RunStatus
	Level          Level
	Score          float64
	MappingStatus  MappingStatus
	ProtocolStatus ProtocolStatus
	ReasonCodes    []string
}

func (f RuntimeFacts) Validate() error {
	if f.RunStatus != RunStatusRunning && f.RunStatus != RunStatusCompleted && f.RunStatus != RunStatusFailed && f.RunStatus != RunStatusCanceled {
		return fmt.Errorf("unsupported model quality run status %q", f.RunStatus)
	}
	if f.Level != LevelHighConfidence && f.Level != LevelLikely && f.Level != LevelUncertain && f.Level != LevelSuspicious && f.Level != LevelUnavailable {
		return fmt.Errorf("unsupported model quality level %q", f.Level)
	}
	if math.IsNaN(f.Score) || math.IsInf(f.Score, 0) || f.Score < 0 || f.Score > 100 {
		return fmt.Errorf("model quality score must be finite and between 0 and 100")
	}
	if f.MappingStatus != "" && f.MappingStatus != MappingStatusDirect && f.MappingStatus != MappingStatusConfigured && f.MappingStatus != MappingStatusUndeclaredMismatch && f.MappingStatus != MappingStatusUnknown {
		return fmt.Errorf("unsupported model mapping status %q", f.MappingStatus)
	}
	if f.ProtocolStatus != "" && f.ProtocolStatus != ProtocolStatusConsistent && f.ProtocolStatus != ProtocolStatusWarning && f.ProtocolStatus != ProtocolStatusFailed && f.ProtocolStatus != ProtocolStatusInsufficientEvidence {
		return fmt.Errorf("unsupported model protocol status %q", f.ProtocolStatus)
	}
	return nil
}

type Decision struct {
	Triggered           bool
	HardFailure         bool
	EvidenceUnavailable bool
	ReasonCodes         []string
}

// DecideQuality applies the Node quality rule without persistence side
// effects. Hard evidence is recorded even when a run did not complete, but
// only a completed run can trigger a penalty.
func DecideQuality(policy Policy, facts RuntimeFacts) (Decision, error) {
	if err := policy.Validate(); err != nil {
		return Decision{}, err
	}
	if err := facts.Validate(); err != nil {
		return Decision{}, err
	}
	hardFailure := facts.Level == LevelSuspicious || facts.MappingStatus == MappingStatusUndeclaredMismatch || facts.ProtocolStatus == ProtocolStatusFailed
	evidenceUnavailable := facts.RunStatus == RunStatusCompleted && facts.Level == LevelUnavailable
	triggered := facts.RunStatus == RunStatusCompleted && !evidenceUnavailable && (hardFailure || facts.Score < float64(policy.PenaltyThreshold))
	reasons := uniqueReasonCodes(facts.ReasonCodes)
	if hardFailure {
		reasons = appendIfAbsent(reasons, "hard_quality_conflict")
	}
	if !hardFailure && triggered {
		reasons = appendIfAbsent(reasons, "score_below_threshold")
	}
	if evidenceUnavailable {
		reasons = appendIfAbsent(reasons, "quality_evidence_unavailable")
	}
	return Decision{Triggered: triggered, HardFailure: hardFailure, EvidenceUnavailable: evidenceUnavailable, ReasonCodes: reasons}, nil
}

func validAction(action Action) bool {
	return action == ActionDisable || action == ActionFallback || action == ActionQualityIsolate
}

func uniqueReasonCodes(values []string) []string {
	result := make([]string, 0, len(values)+1)
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = appendIfAbsent(result, value)
		}
	}
	return result
}

func appendIfAbsent(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
