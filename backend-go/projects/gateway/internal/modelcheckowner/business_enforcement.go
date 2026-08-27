package modelcheckowner

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// BusinessEnforcementApplier is the narrow Business-owner mutation port used
// by J3b quality projection. It must be opened only after the complete
// Business SQLite writer handoff (or an equivalent PostgreSQL owner grant).
// The adapter performs account status and enforcement persistence in one
// transaction and never talks to Node or another process.
type BusinessEnforcementApplier struct {
	db       *sql.DB
	postgres bool
}

func NewBusinessEnforcementApplier(db *sql.DB, postgres bool) (*BusinessEnforcementApplier, error) {
	if db == nil {
		return nil, errors.New("J3b Business enforcement database is required")
	}
	return &BusinessEnforcementApplier{db: db, postgres: postgres}, nil
}

func (a *BusinessEnforcementApplier) Apply(ctx context.Context, input QualityEnforcement) error {
	if a == nil || a.db == nil {
		return errors.New("J3b Business enforcement owner is not initialized")
	}
	if strings.TrimSpace(input.AccountID) == "" || strings.TrimSpace(input.SystemAccountID) == "" || strings.TrimSpace(input.RunID) == "" || strings.TrimSpace(input.Action) == "" || input.Threshold < 40 || input.Threshold > 100 || input.Score >= input.Threshold || input.RecoveryIntervalMinutes < 10 || input.RecoveryIntervalMinutes > 10080 {
		return errors.New("J3b Business enforcement input is invalid")
	}
	if input.Action != "disable" && input.Action != "fallback" && input.Action != "quality_isolate" {
		return errors.New("J3b Business enforcement action is invalid")
	}
	policyRevision, err := nonNegativeInt(input.PolicyRevision)
	if err != nil {
		return fmt.Errorf("J3b Business enforcement policy revision: %w", err)
	}
	accountRevision, err := positiveInt(input.AccountConfigRevision)
	if err != nil {
		return fmt.Errorf("J3b Business enforcement account revision: %w", err)
	}
	now := input.OccurredAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin J3b Business enforcement: %w", err)
	}
	defer tx.Rollback()
	matched, err := a.configurationMatches(ctx, tx, input, policyRevision)
	if err != nil {
		return fmt.Errorf("read J3b Business enforcement configuration: %w", err)
	}
	if !matched {
		return errors.New("J3b Business enforcement configuration is stale")
	}
	accountTable := a.table("accounts")
	var status string
	var currentRevision, fallbackEnabled, superPriority int
	var deletedAt sql.NullString
	query := `SELECT status,config_revision,fallback_enabled,super_priority_enabled,deleted_at FROM ` + accountTable + ` WHERE id=` + a.placeholder(1) + ` AND system_account_id=` + a.placeholder(2)
	if err := tx.QueryRowContext(ctx, query, input.AccountID, input.SystemAccountID).Scan(&status, &currentRevision, &fallbackEnabled, &superPriority, &deletedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("J3b Business enforcement account not found")
		}
		return fmt.Errorf("read J3b Business enforcement account: %w", err)
	}
	if deletedAt.Valid {
		return errors.New("J3b Business enforcement account revision is stale")
	}
	// Health publication is stored separately and may fail after this
	// transaction commits. A retry for the same run must therefore succeed
	// after the account revision has already advanced, while a different run
	// still remains fenced by the original revision.
	var priorID, priorState, priorRun string
	var priorGeneration int
	priorErr := tx.QueryRowContext(ctx, `SELECT enforcement_id,generation,state,trigger_run_id FROM `+a.table("account_quality_enforcements")+` WHERE account_id=`+a.placeholder(1), input.AccountID).Scan(&priorID, &priorGeneration, &priorState, &priorRun)
	if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
		return fmt.Errorf("read J3b existing enforcement: %w", priorErr)
	}
	if priorErr == nil && priorRun == input.RunID && priorState == "active" {
		return tx.Commit()
	}
	if currentRevision != accountRevision {
		return errors.New("J3b Business enforcement account revision is stale")
	}
	if status != "active" && status != "temporary_unavailable" && status != "rate_limited" {
		return errors.New("J3b Business enforcement account status is not enforceable")
	}
	newStatus, schedulable, nextFallback, nextPriority := status, 1, fallbackEnabled, superPriority
	if input.Action == "disable" {
		newStatus, schedulable = "disabled", 0
	} else if input.Action == "quality_isolate" {
		newStatus, schedulable = "quality_isolated", 0
	} else {
		nextFallback, nextPriority = 1, 0
	}
	message := input.Message
	if len(message) > 1000 {
		message = message[:1000]
	}
	update := `UPDATE ` + accountTable + ` SET status=` + a.placeholder(1) + `,schedulable=` + a.placeholder(2) + `,fallback_enabled=` + a.placeholder(3) + `,super_priority_enabled=` + a.placeholder(4) + `,last_error_code=` + a.placeholder(5) + `,last_error_message=` + a.placeholder(6) + `,config_revision=config_revision+1,updated_at=` + a.placeholder(7) + ` WHERE id=` + a.placeholder(8) + ` AND system_account_id=` + a.placeholder(9) + ` AND config_revision=` + a.placeholder(10)
	result, err := tx.ExecContext(ctx, update, newStatus, schedulable, nextFallback, nextPriority, "model_quality_failed", message, now.Format(time.RFC3339Nano), input.AccountID, input.SystemAccountID, accountRevision)
	if err != nil {
		return fmt.Errorf("update J3b Business enforcement account: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("J3b Business enforcement account changed before commit")
	}
	enforcementID := newEnforcementID()
	interval := input.RecoveryIntervalMinutes
	source, sourceID := "manual", any(nil)
	if strings.TrimSpace(input.ScheduleID) != "" {
		source, sourceID = "schedule", input.ScheduleID
	}
	enforcement := `INSERT INTO ` + a.table("account_quality_enforcements") + ` (account_id,system_account_id,enforcement_id,generation,state,action,trigger_run_id,config_source,config_source_id,policy_revision,profile,penalty_threshold,recovery_interval_minutes,account_config_revision,before_status,after_status,fallback_was_enabled,super_priority_was_enabled,started_at,recovery_due_at,created_at,updated_at) VALUES (` + a.placeholders(22) + `) ON CONFLICT(account_id) DO UPDATE SET system_account_id=excluded.system_account_id,enforcement_id=excluded.enforcement_id,generation=generation+1,state='active',action=excluded.action,trigger_run_id=excluded.trigger_run_id,config_source=excluded.config_source,config_source_id=excluded.config_source_id,policy_revision=excluded.policy_revision,profile=excluded.profile,penalty_threshold=excluded.penalty_threshold,recovery_interval_minutes=excluded.recovery_interval_minutes,account_config_revision=excluded.account_config_revision,before_status=excluded.before_status,after_status=excluded.after_status,fallback_was_enabled=excluded.fallback_was_enabled,super_priority_was_enabled=excluded.super_priority_was_enabled,started_at=excluded.started_at,recovery_due_at=excluded.recovery_due_at,cleared_at=NULL,updated_at=excluded.updated_at`
	args := []any{input.AccountID, input.SystemAccountID, enforcementID, 1, "active", input.Action, input.RunID, source, sourceID, policyRevision, defaultProfile(input.Profile), input.Threshold, interval, accountRevision, status, newStatus, fallbackEnabled, superPriority, now.Format(time.RFC3339Nano), nullableTime(now.Add(time.Duration(interval)*time.Minute), input.Action == "quality_isolate"), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)}
	if _, err := tx.ExecContext(ctx, enforcement, args...); err != nil {
		return fmt.Errorf("persist J3b Business enforcement: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit J3b Business enforcement: %w", err)
	}
	return nil
}

func (a *BusinessEnforcementApplier) configurationMatches(ctx context.Context, tx *sql.Tx, input QualityEnforcement, policyRevision int) (bool, error) {
	if strings.TrimSpace(input.ScheduleID) != "" {
		var revision, threshold, recovery int
		var profile, action, model string
		err := tx.QueryRowContext(ctx, `SELECT revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes,model FROM `+a.table("model_quality_schedules")+` WHERE id=`+a.placeholder(1)+` AND system_account_id=`+a.placeholder(2)+` AND account_id=`+a.placeholder(3), input.ScheduleID, input.SystemAccountID, input.AccountID).Scan(&revision, &profile, &threshold, &action, &recovery, &model)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return revision == policyRevision && profile == defaultProfile(input.Profile) && threshold == input.Threshold && action == input.Action && recovery == input.RecoveryIntervalMinutes && (input.Model == "" || model == input.Model), nil
	}
	var revision, threshold, recovery int
	var profile, action string
	err := tx.QueryRowContext(ctx, `SELECT revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes FROM `+a.table("model_quality_policies")+` WHERE system_account_id=`+a.placeholder(1), input.SystemAccountID).Scan(&revision, &profile, &threshold, &action, &recovery)
	if errors.Is(err, sql.ErrNoRows) {
		return policyRevision == 0 && defaultProfile(input.Profile) == "quick" && input.Threshold == 70 && input.Action == "fallback" && input.RecoveryIntervalMinutes == 10, nil
	}
	if err != nil {
		return false, err
	}
	return revision == policyRevision && profile == defaultProfile(input.Profile) && threshold == input.Threshold && action == input.Action && recovery == input.RecoveryIntervalMinutes, nil
}

func positiveInt(value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 1 {
		return 0, errors.New("revision must be a positive integer")
	}
	return parsed, nil
}

func nonNegativeInt(value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return 0, errors.New("revision must be a non-negative integer")
	}
	return parsed, nil
}

func defaultProfile(profile string) string {
	if strings.TrimSpace(profile) == "" {
		return "quick"
	}
	return strings.TrimSpace(profile)
}

func nullableTime(value time.Time, enabled bool) any {
	if !enabled {
		return nil
	}
	return value.Format(time.RFC3339Nano)
}

func (a *BusinessEnforcementApplier) table(name string) string {
	if a.postgres {
		return "juhe_business." + name
	}
	return name
}

func (a *BusinessEnforcementApplier) placeholder(index int) string {
	if a.postgres {
		return fmt.Sprintf("$%d", index)
	}
	return "?"
}

func (a *BusinessEnforcementApplier) placeholders(count int) string {
	values := make([]string, count)
	for i := range values {
		values[i] = a.placeholder(i + 1)
	}
	return strings.Join(values, ",")
}

func newEnforcementID() string {
	var value [10]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "mqe-random-fallback"
	}
	return "mqe-" + fmt.Sprintf("%x", value[:])
}

var _ EnforcementApplier = (*BusinessEnforcementApplier)(nil)
