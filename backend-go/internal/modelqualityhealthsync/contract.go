// Package modelqualityhealthsync defines the pure retry-selection and
// completion rules for replaying a failed model-quality health-stat sync.
//
// It deliberately owns neither persistence nor execution. A repository maps
// durable rows into Run values in their database order, a worker writes the
// statistics fact, and a repository persists the resulting transition. This
// package makes the hand-off explicit without copying Node's JSON predicates,
// scheduler, worker IPC, or database implementation.
package modelqualityhealthsync

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	// DefaultBatchCap matches the current Node health-sync retry batch size.
	DefaultBatchCap BatchCap = 20
	// MaxBatchCap bounds one retry selection even if a caller is misconfigured.
	MaxBatchCap BatchCap = 100
)

// BatchCap is intentionally validated instead of silently clamped. A caller
// must make any reduction in retry work explicit.
type BatchCap uint16

func NewBatchCap(value int) (BatchCap, error) {
	if value < 1 || value > int(MaxBatchCap) {
		return 0, fmt.Errorf("model quality health-sync batch cap must be from 1 to %d", MaxBatchCap)
	}
	return BatchCap(value), nil
}

func (cap BatchCap) Validate() error {
	if cap < 1 || cap > MaxBatchCap {
		return fmt.Errorf("model quality health-sync batch cap must be from 1 to %d", MaxBatchCap)
	}
	return nil
}

type RunStatus string

const (
	RunStatusRunning   RunStatus = "running"
	RunStatusCompleted RunStatus = "completed"
	RunStatusFailed    RunStatus = "failed"
	RunStatusCanceled  RunStatus = "canceled"
)

type HealthSyncStatus string

const (
	// HealthSyncStatusNone represents a run with no model-quality health sync
	// decision. It is a valid persisted state but is never retry eligible.
	HealthSyncStatusNone         HealthSyncStatus = ""
	HealthSyncStatusApplied      HealthSyncStatus = "applied"
	HealthSyncStatusPendingRetry HealthSyncStatus = "pending_retry"
	HealthSyncStatusFailed       HealthSyncStatus = "failed"
)

// Run contains only the durable facts required to choose and complete a
// health-sync retry. UpdatedAt and ID form its stable database cursor key.
type Run struct {
	ID               string
	AccountID        string
	Status           RunStatus
	HealthSyncStatus HealthSyncStatus
	UpdatedAt        time.Time
}

func (run Run) Validate() error {
	if !canonicalID(run.ID) {
		return fmt.Errorf("model quality health-sync run ID is invalid")
	}
	if run.AccountID != "" && !canonicalID(run.AccountID) {
		return fmt.Errorf("model quality health-sync account ID is invalid")
	}
	if run.UpdatedAt.IsZero() {
		return fmt.Errorf("model quality health-sync updatedAt is required")
	}
	if !validRunStatus(run.Status) {
		return fmt.Errorf("unsupported model quality health-sync run status %q", run.Status)
	}
	if !validHealthSyncStatus(run.HealthSyncStatus) {
		return fmt.Errorf("unsupported model quality health-sync status %q", run.HealthSyncStatus)
	}
	return nil
}

// Cursor is an exclusive, stable `(updatedAt,id)` key. The next batch begins
// strictly after it, so replay does not repeat the last applied page member.
type Cursor struct {
	UpdatedAt time.Time
	ID        string
}

func (cursor Cursor) Validate() error {
	if cursor.UpdatedAt.IsZero() {
		return fmt.Errorf("model quality health-sync cursor updatedAt is required")
	}
	if !canonicalID(cursor.ID) {
		return fmt.Errorf("model quality health-sync cursor ID is invalid")
	}
	return nil
}

// EligibilityReason gives a fail-closed explanation without requiring a
// scheduler or persistence dependency in this pure contract.
type EligibilityReason string

const (
	EligibilityEligible          EligibilityReason = "eligible"
	EligibilityInvalidInput      EligibilityReason = "invalid_input"
	EligibilityRunNotCompleted   EligibilityReason = "run_not_completed"
	EligibilityAccountMissing    EligibilityReason = "account_missing"
	EligibilityHealthSyncNotFail EligibilityReason = "health_sync_not_failed"
)

type Eligibility struct {
	Eligible bool
	Reason   EligibilityReason
}

// EvaluateEligibility mirrors Node's replay predicate: only completed runs
// with a non-empty account ID and failed health-sync result enter retry.
// Invalid durable facts never become eligible.
func EvaluateEligibility(run Run) Eligibility {
	if err := run.Validate(); err != nil {
		return Eligibility{Reason: EligibilityInvalidInput}
	}
	if run.Status != RunStatusCompleted {
		return Eligibility{Reason: EligibilityRunNotCompleted}
	}
	if run.AccountID == "" {
		return Eligibility{Reason: EligibilityAccountMissing}
	}
	if run.HealthSyncStatus != HealthSyncStatusFailed {
		return Eligibility{Reason: EligibilityHealthSyncNotFail}
	}
	return Eligibility{Eligible: true, Reason: EligibilityEligible}
}

type RetryBatchInput struct {
	// After is exclusive. Nil selects from the beginning of the durable order.
	After *Cursor
	Cap   BatchCap
	Runs  []Run
}

// RetryBatch is a deterministic, bounded retry page. Next is nil when no run
// was selected. HasMore only refers to eligible rows strictly after After.
type RetryBatch struct {
	Runs    []Run
	Next    *Cursor
	HasMore bool
}

// PlanRetryBatch validates every supplied durable fact before choosing any
// work, then orders eligible rows by `(updatedAt,id)`. Refusing the whole page
// for malformed time, ID, or status avoids silently advancing a cursor past an
// invalid row.
func PlanRetryBatch(input RetryBatchInput) (RetryBatch, error) {
	if err := input.Cap.Validate(); err != nil {
		return RetryBatch{}, err
	}
	if input.After != nil {
		if err := input.After.Validate(); err != nil {
			return RetryBatch{}, err
		}
	}

	seenIDs := make(map[string]struct{}, len(input.Runs))
	eligible := make([]Run, 0, len(input.Runs))
	for _, run := range input.Runs {
		if err := run.Validate(); err != nil {
			return RetryBatch{}, err
		}
		if _, exists := seenIDs[run.ID]; exists {
			return RetryBatch{}, fmt.Errorf("duplicate model quality health-sync run ID %q", run.ID)
		}
		seenIDs[run.ID] = struct{}{}
		run.UpdatedAt = canonicalTime(run.UpdatedAt)
		if input.After != nil && !strictlyAfter(run, *input.After) {
			continue
		}
		if EvaluateEligibility(run).Eligible {
			eligible = append(eligible, run)
		}
	}

	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].UpdatedAt.Equal(eligible[j].UpdatedAt) {
			return eligible[i].ID < eligible[j].ID
		}
		return eligible[i].UpdatedAt.Before(eligible[j].UpdatedAt)
	})

	limit := int(input.Cap)
	batch := RetryBatch{HasMore: len(eligible) > limit}
	if len(eligible) > limit {
		eligible = eligible[:limit]
	}
	batch.Runs = append([]Run(nil), eligible...)
	if len(batch.Runs) != 0 {
		last := batch.Runs[len(batch.Runs)-1]
		batch.Next = &Cursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
	return batch, nil
}

type AttemptOutcome struct {
	// StatisticsWritten is true only after the idempotent health-stat fact was
	// successfully committed. It is not merely a request attempt.
	StatisticsWritten bool
	// DecisionMarkedApplied is true only after the run decision was durably
	// updated to healthSyncResult=applied.
	DecisionMarkedApplied bool
}

type TransitionResult string

const (
	TransitionApplied          TransitionResult = "applied"
	TransitionRemainsRetryable TransitionResult = "remains_retryable"
)

type Transition struct {
	Result               TransitionResult
	HealthSyncStatus     HealthSyncStatus
	Retryable            bool
	StatisticsWasWritten bool
}

// PlanTransition preserves the failure state until both durable writes are
// known to have succeeded. In particular, a successful health-stat write does
// not permit `applied` when updating the run decision fails; the next retry may
// safely repeat that idempotent statistic write.
func PlanTransition(run Run, outcome AttemptOutcome) (Transition, error) {
	if eligibility := EvaluateEligibility(run); !eligibility.Eligible {
		return Transition{}, fmt.Errorf("model quality health-sync transition is not retry eligible: %s", eligibility.Reason)
	}
	if outcome.DecisionMarkedApplied && !outcome.StatisticsWritten {
		return Transition{}, fmt.Errorf("model quality health-sync cannot mark applied before statistics are written")
	}
	if outcome.StatisticsWritten && outcome.DecisionMarkedApplied {
		return Transition{
			Result:               TransitionApplied,
			HealthSyncStatus:     HealthSyncStatusApplied,
			StatisticsWasWritten: true,
		}, nil
	}
	return Transition{
		Result:               TransitionRemainsRetryable,
		HealthSyncStatus:     HealthSyncStatusFailed,
		Retryable:            true,
		StatisticsWasWritten: outcome.StatisticsWritten,
	}, nil
}

func strictlyAfter(run Run, cursor Cursor) bool {
	runTime := canonicalTime(run.UpdatedAt)
	cursorTime := canonicalTime(cursor.UpdatedAt)
	if runTime.Equal(cursorTime) {
		return run.ID > cursor.ID
	}
	return runTime.After(cursorTime)
}

func canonicalTime(value time.Time) time.Time {
	return value.UTC().Round(0)
}

func validRunStatus(status RunStatus) bool {
	return status == RunStatusRunning || status == RunStatusCompleted || status == RunStatusFailed || status == RunStatusCanceled
}

func validHealthSyncStatus(status HealthSyncStatus) bool {
	return status == HealthSyncStatusNone || status == HealthSyncStatusApplied || status == HealthSyncStatusPendingRetry || status == HealthSyncStatusFailed
}

func canonicalID(value string) bool {
	if value == "" || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
