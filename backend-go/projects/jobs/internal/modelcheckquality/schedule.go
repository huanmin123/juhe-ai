package modelcheckquality

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ScheduledCandidate is the immutable schedule snapshot fenced by Revision.
// The future scheduler must create exactly one scheduled model check from it.
type ScheduledCandidate struct {
	ScheduleID, SystemAccountID, AccountID, Model, Profile, Action       string
	Revision, IntervalMinutes, PenaltyThreshold, RecoveryIntervalMinutes int
}

type ScheduledClaimInput struct {
	OwnerID string
	Now     time.Time
	Limit   int
	Lease   time.Duration
}

type ScheduledCompletionInput struct {
	OwnerID, ScheduleID, RunID, Status string
	Revision, IntervalMinutes          int
	CompletedAt                        time.Time
}

// ClaimDueSchedules leases only an enabled schedule whose own account remains
// a direct, active account. It does not execute a probe or mutate quality.
func ClaimDueSchedules(ctx context.Context, db *sql.DB, postgres bool, input ScheduledClaimInput) ([]ScheduledCandidate, error) {
	if db == nil || strings.TrimSpace(input.OwnerID) == "" || input.Now.IsZero() || input.Limit < 1 || input.Limit > 1000 || input.Lease <= 0 {
		return nil, errors.New("invalid scheduled claim input")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin scheduled claim: %w", err)
	}
	defer tx.Rollback()
	q := func(value string) string { return businessSQL(value, postgres) }
	now := input.Now.UTC().Format(time.RFC3339Nano)
	until := input.Now.UTC().Add(input.Lease).Format(time.RFC3339Nano)
	query := `SELECT mqs.id,mqs.revision,mqs.system_account_id,mqs.account_id,mqs.model,mqs.interval_minutes,mqs.profile,mqs.penalty_threshold,mqs.penalty_action,mqs.recovery_interval_minutes FROM model_quality_schedules mqs JOIN accounts a ON a.id=mqs.account_id WHERE mqs.enabled=1 AND mqs.next_run_at<=? AND (mqs.lease_until IS NULL OR mqs.lease_until<=?) AND a.deleted_at IS NULL AND a.authorization_instance_authorization_id IS NULL AND a.status='active' ORDER BY mqs.next_run_at ASC,mqs.id ASC LIMIT ?`
	if postgres {
		query += " FOR UPDATE OF mqs SKIP LOCKED"
	}
	rows, err := tx.QueryContext(ctx, q(query), now, now, input.Limit)
	if err != nil {
		return nil, fmt.Errorf("query due model quality schedules: %w", err)
	}
	defer rows.Close()
	var candidates []ScheduledCandidate
	for rows.Next() {
		var candidate ScheduledCandidate
		if err := rows.Scan(&candidate.ScheduleID, &candidate.Revision, &candidate.SystemAccountID, &candidate.AccountID, &candidate.Model, &candidate.IntervalMinutes, &candidate.Profile, &candidate.PenaltyThreshold, &candidate.Action, &candidate.RecoveryIntervalMinutes); err != nil {
			return nil, err
		}
		result, err := tx.ExecContext(ctx, q(`UPDATE model_quality_schedules SET lease_owner=?,lease_until=?,updated_at=? WHERE id=? AND revision=? AND enabled=1 AND (lease_until IS NULL OR lease_until<=?)`), input.OwnerID, until, now, candidate.ScheduleID, candidate.Revision, now)
		if err != nil {
			return nil, err
		}
		if changed, _ := result.RowsAffected(); changed == 1 {
			candidates = append(candidates, candidate)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return candidates, nil
}

// CompleteScheduledRun advances a schedule only when the original owner and
// revision still hold the lease. A changed schedule cannot be overwritten.
func CompleteScheduledRun(ctx context.Context, db *sql.DB, postgres bool, input ScheduledCompletionInput) (bool, error) {
	if db == nil || strings.TrimSpace(input.OwnerID) == "" || strings.TrimSpace(input.ScheduleID) == "" || input.Revision < 1 || input.IntervalMinutes < 10 || input.IntervalMinutes > 10080 || input.CompletedAt.IsZero() || (input.Status != "completed" && input.Status != "failed" && input.Status != "canceled") {
		return false, errors.New("invalid scheduled completion input")
	}
	completed := input.CompletedAt.UTC()
	q := businessSQL(`UPDATE model_quality_schedules SET last_run_id=?,last_run_at=?,last_run_status=?,next_run_at=?,lease_owner=NULL,lease_until=NULL,updated_at=? WHERE id=? AND revision=? AND lease_owner=?`, postgres)
	result, err := db.ExecContext(ctx, q, nullable(input.RunID), completed.Format(time.RFC3339Nano), input.Status, completed.Add(time.Duration(input.IntervalMinutes)*time.Minute).Format(time.RFC3339Nano), completed.Format(time.RFC3339Nano), input.ScheduleID, input.Revision, input.OwnerID)
	if err != nil {
		return false, fmt.Errorf("complete model quality schedule: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return changed == 1, nil
}
