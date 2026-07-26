package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/modelquality"
	"juhe-ai/backend-go/internal/store/port"
)

type modelQualityScheduleBeginTx func(context.Context, pgx.TxOptions) (pgx.Tx, error)
type modelQualityScheduleIDGenerator func(string) (string, error)

type modelQualityScheduleExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type modelQualityScheduleScanner interface {
	Scan(...any) error
}

func (s *Store) UpsertModelQualitySchedule(ctx context.Context, input port.ModelQualityScheduleUpsertInput) (port.ModelQualityScheduleWriteResult, error) {
	return upsertModelQualitySchedule(ctx, s.pool.BeginTx, input, func(prefix string) (string, error) {
		value, err := uuid.NewRandom()
		if err != nil {
			return "", err
		}
		return prefix + "_" + strings.ReplaceAll(value.String(), "-", ""), nil
	})
}

func upsertModelQualitySchedule(
	ctx context.Context,
	beginTx modelQualityScheduleBeginTx,
	input port.ModelQualityScheduleUpsertInput,
	newID modelQualityScheduleIDGenerator,
) (port.ModelQualityScheduleWriteResult, error) {
	if err := validateModelQualityScheduleUpsertInput(input); err != nil {
		return port.ModelQualityScheduleWriteResult{}, err
	}
	if newID == nil {
		return port.ModelQualityScheduleWriteResult{}, fmt.Errorf("model quality schedule ID generator is required")
	}
	tx, err := beginModelQualityScheduleTx(ctx, beginTx, "upsert")
	if err != nil {
		return port.ModelQualityScheduleWriteResult{}, err
	}
	committed := false
	defer rollbackModelQualityScheduleTx(tx, &committed)()

	var accountID string
	err = tx.QueryRow(ctx, lockModelQualityScheduleAccountSQL, input.AccountID, input.SystemAccountID).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ModelQualityScheduleWriteResult{Status: port.ModelQualityScheduleMissing}, nil
	}
	if err != nil {
		return port.ModelQualityScheduleWriteResult{}, fmt.Errorf("lock model quality schedule account: %w", err)
	}

	existing, found, err := findLockedModelQualitySchedule(ctx, tx, lockModelQualityScheduleByScopeSQL, input.SystemAccountID, input.AccountID)
	if err != nil {
		return port.ModelQualityScheduleWriteResult{}, err
	}
	if !found {
		if input.ExpectedRevision != nil && *input.ExpectedRevision != 0 {
			return commitModelQualityScheduleWriteResult(ctx, tx, &committed, port.ModelQualityScheduleWriteResult{Status: port.ModelQualityScheduleConflict})
		}
		id, err := newID("mqs")
		if err != nil {
			return port.ModelQualityScheduleWriteResult{}, fmt.Errorf("generate model quality schedule ID: %w", err)
		}
		if !validModelQualityScheduleText(id, 256) {
			return port.ModelQualityScheduleWriteResult{}, fmt.Errorf("generated model quality schedule ID is invalid")
		}
		nextRunAt, err := addModelQualityScheduleInterval(input.UpdatedAt, input.Interval)
		if err != nil {
			return port.ModelQualityScheduleWriteResult{}, err
		}
		created, err := scanModelQualitySchedule(tx.QueryRow(ctx, insertModelQualityScheduleSQL,
			id,
			input.SystemAccountID,
			input.AccountID,
			input.Model,
			int(input.Interval/time.Minute),
			string(input.Profile),
			input.PenaltyThreshold,
			string(input.PenaltyAction),
			int(input.RecoveryInterval/time.Minute),
			modelQualityPolicyBoolInt(input.Enabled),
			modelQualityPolicyTimeText(nextRunAt),
			modelQualityPolicyTimeText(input.UpdatedAt),
		))
		if err != nil {
			return port.ModelQualityScheduleWriteResult{}, fmt.Errorf("insert model quality schedule: %w", err)
		}
		return commitModelQualityScheduleWriteResult(ctx, tx, &committed, port.ModelQualityScheduleWriteResult{Status: port.ModelQualityScheduleWritten, Schedule: &created})
	}

	if input.ExpectedRevision != nil && *input.ExpectedRevision != existing.Revision {
		return commitModelQualityScheduleWriteResult(ctx, tx, &committed, port.ModelQualityScheduleWriteResult{Status: port.ModelQualityScheduleConflict, Schedule: &existing})
	}
	if existing.Revision >= modelquality.ScheduleRevision(math.MaxInt32) {
		return port.ModelQualityScheduleWriteResult{}, fmt.Errorf("model quality schedule revision is exhausted")
	}
	nextRunAt, err := addModelQualityScheduleInterval(input.UpdatedAt, input.Interval)
	if err != nil {
		return port.ModelQualityScheduleWriteResult{}, err
	}
	updated, err := scanModelQualitySchedule(tx.QueryRow(ctx, updateModelQualityScheduleSQL,
		input.Model,
		int(input.Interval/time.Minute),
		string(input.Profile),
		input.PenaltyThreshold,
		string(input.PenaltyAction),
		int(input.RecoveryInterval/time.Minute),
		modelQualityPolicyBoolInt(input.Enabled),
		modelQualityPolicyTimeText(nextRunAt),
		modelQualityPolicyTimeText(input.UpdatedAt),
		existing.ID,
		int64(existing.Revision),
	))
	if err != nil {
		return port.ModelQualityScheduleWriteResult{}, fmt.Errorf("update model quality schedule: %w", err)
	}
	return commitModelQualityScheduleWriteResult(ctx, tx, &committed, port.ModelQualityScheduleWriteResult{Status: port.ModelQualityScheduleWritten, Schedule: &updated})
}

func (s *Store) DeleteModelQualitySchedule(ctx context.Context, input port.ModelQualityScheduleDeleteInput) (port.ModelQualityScheduleWriteResult, error) {
	return deleteModelQualitySchedule(ctx, s.pool.BeginTx, input)
}

func deleteModelQualitySchedule(ctx context.Context, beginTx modelQualityScheduleBeginTx, input port.ModelQualityScheduleDeleteInput) (port.ModelQualityScheduleWriteResult, error) {
	if err := validateModelQualityScheduleDeleteInput(input); err != nil {
		return port.ModelQualityScheduleWriteResult{}, err
	}
	tx, err := beginModelQualityScheduleTx(ctx, beginTx, "delete")
	if err != nil {
		return port.ModelQualityScheduleWriteResult{}, err
	}
	committed := false
	defer rollbackModelQualityScheduleTx(tx, &committed)()

	existing, found, err := findLockedModelQualitySchedule(ctx, tx, lockModelQualityScheduleDeleteSQL, input.ScheduleID, input.SystemAccountID)
	if err != nil {
		return port.ModelQualityScheduleWriteResult{}, err
	}
	if !found {
		return commitModelQualityScheduleWriteResult(ctx, tx, &committed, port.ModelQualityScheduleWriteResult{Status: port.ModelQualityScheduleMissing})
	}
	if input.ExpectedRevision != nil && *input.ExpectedRevision != existing.Revision {
		return commitModelQualityScheduleWriteResult(ctx, tx, &committed, port.ModelQualityScheduleWriteResult{Status: port.ModelQualityScheduleConflict, Schedule: &existing})
	}
	command, err := tx.Exec(ctx, deleteModelQualityScheduleSQL, input.ScheduleID, input.SystemAccountID, int64(existing.Revision))
	if err != nil {
		return port.ModelQualityScheduleWriteResult{}, fmt.Errorf("delete model quality schedule: %w", err)
	}
	if command.RowsAffected() != 1 {
		return port.ModelQualityScheduleWriteResult{}, fmt.Errorf("locked model quality schedule was not deleted")
	}
	return commitModelQualityScheduleWriteResult(ctx, tx, &committed, port.ModelQualityScheduleWriteResult{Status: port.ModelQualityScheduleWritten, Schedule: &existing})
}

func (s *Store) ClaimDueModelQualitySchedules(ctx context.Context, input port.ModelQualityScheduleClaimInput) ([]port.ModelQualityScheduleClaim, error) {
	return claimDueModelQualitySchedules(ctx, s.pool.BeginTx, input, func(string) (string, error) {
		value, err := uuid.NewRandom()
		if err != nil {
			return "", err
		}
		return "mqs_claim_" + strings.ReplaceAll(value.String(), "-", ""), nil
	})
}

func claimDueModelQualitySchedules(
	ctx context.Context,
	beginTx modelQualityScheduleBeginTx,
	input port.ModelQualityScheduleClaimInput,
	newToken modelQualityScheduleIDGenerator,
) ([]port.ModelQualityScheduleClaim, error) {
	input = normalizeModelQualityScheduleClaimInput(input)
	if err := validateModelQualityScheduleClaimInput(input); err != nil {
		return nil, err
	}
	if newToken == nil {
		return nil, fmt.Errorf("model quality schedule claim token generator is required")
	}
	tx, err := beginModelQualityScheduleTx(ctx, beginTx, "claim")
	if err != nil {
		return nil, err
	}
	committed := false
	defer rollbackModelQualityScheduleTx(tx, &committed)()

	rows, err := tx.Query(ctx, claimDueModelQualityScheduleCandidatesSQL, input.Limit)
	if err != nil {
		return nil, fmt.Errorf("select due model quality schedules: %w", err)
	}
	type candidate struct {
		schedule        port.ModelQualitySchedule
		accountRevision modelquality.AccountRevision
	}
	candidates := make([]candidate, 0, input.Limit)
	for rows.Next() {
		schedule, accountRevision, scanErr := scanModelQualityScheduleClaimCandidate(rows)
		if scanErr != nil {
			rows.Close()
			return nil, fmt.Errorf("scan due model quality schedule: %w", scanErr)
		}
		candidates = append(candidates, candidate{schedule: schedule, accountRevision: accountRevision})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("read due model quality schedules: %w", err)
	}
	rows.Close()

	claims := make([]port.ModelQualityScheduleClaim, 0, len(candidates))
	for _, candidate := range candidates {
		tokenValue, err := newToken("mqs_claim")
		if err != nil {
			return nil, fmt.Errorf("generate model quality schedule claim token: %w", err)
		}
		token := port.ModelQualityScheduleClaimToken(tokenValue)
		if !validModelQualityScheduleText(string(token), 256) {
			return nil, fmt.Errorf("generated model quality schedule claim token is invalid")
		}
		var leaseUntilRaw, claimedAtRaw string
		err = tx.QueryRow(ctx, claimModelQualityScheduleSQL,
			string(input.OwnerID),
			string(token),
			int64(input.LeaseDuration/time.Millisecond),
			candidate.schedule.ID,
			int64(candidate.schedule.Revision),
			int64(candidate.accountRevision),
		).Scan(&leaseUntilRaw, &claimedAtRaw)
		if errors.Is(err, pgx.ErrNoRows) {
			// The schedule row remains locked, so no row means the account
			// changed after candidate selection. This is a stale-CAS skip.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("claim model quality schedule %q: %w", candidate.schedule.ID, err)
		}
		leaseUntil, err := modelQualityPolicyParseTime(leaseUntilRaw)
		if err != nil {
			return nil, fmt.Errorf("parse claimed model quality schedule lease_until: %w", err)
		}
		claimedAt, err := modelQualityPolicyParseTime(claimedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse claimed model quality schedule updated_at: %w", err)
		}
		policy := modelQualitySchedulePolicy(candidate.schedule)
		lease := port.ModelQualityScheduleLease{OwnerID: input.OwnerID, ClaimToken: token, Until: leaseUntil}
		candidate.schedule.Lease = &lease
		candidate.schedule.UpdatedAt = claimedAt
		claims = append(claims, port.ModelQualityScheduleClaim{
			Schedule:              candidate.schedule,
			Policy:                policy,
			AccountConfigRevision: candidate.accountRevision,
			Lease:                 lease,
		})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit model quality schedule claim: %w", err)
	}
	committed = true
	return claims, nil
}

func (s *Store) CompleteModelQualitySchedule(ctx context.Context, input port.ModelQualityScheduleCompleteInput) (bool, error) {
	return completeModelQualitySchedule(ctx, s.pool, input)
}

func completeModelQualitySchedule(ctx context.Context, q modelQualityScheduleExecer, input port.ModelQualityScheduleCompleteInput) (bool, error) {
	if err := validateModelQualityScheduleCompleteInput(input); err != nil {
		return false, err
	}
	command, err := q.Exec(ctx, completeModelQualityScheduleSQL,
		input.RunID,
		string(input.Status),
		int(input.Interval/time.Minute),
		input.ScheduleID,
		int64(input.ExpectedRevision),
		string(input.Lease.OwnerID),
		string(input.Lease.ClaimToken),
		modelQualityPolicyTimeText(input.Lease.Until),
	)
	if err != nil {
		return false, fmt.Errorf("complete model quality schedule: %w", err)
	}
	return command.RowsAffected() == 1, nil
}

func findLockedModelQualitySchedule(ctx context.Context, tx pgx.Tx, query string, args ...any) (port.ModelQualitySchedule, bool, error) {
	schedule, err := scanModelQualitySchedule(tx.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ModelQualitySchedule{}, false, nil
	}
	if err != nil {
		return port.ModelQualitySchedule{}, false, fmt.Errorf("lock model quality schedule: %w", err)
	}
	return schedule, true, nil
}

func scanModelQualityScheduleClaimCandidate(row modelQualityScheduleScanner) (port.ModelQualitySchedule, modelquality.AccountRevision, error) {
	var accountRevision int64
	schedule, err := scanModelQualityScheduleWithTail(row, &accountRevision)
	if err != nil {
		return port.ModelQualitySchedule{}, 0, err
	}
	if accountRevision < 1 || accountRevision > math.MaxInt32 {
		return port.ModelQualitySchedule{}, 0, fmt.Errorf("invalid model quality schedule account revision %d", accountRevision)
	}
	return schedule, modelquality.AccountRevision(accountRevision), nil
}

func scanModelQualitySchedule(row modelQualityScheduleScanner) (port.ModelQualitySchedule, error) {
	return scanModelQualityScheduleWithTail(row)
}

func scanModelQualityScheduleWithTail(row modelQualityScheduleScanner, tail ...any) (port.ModelQualitySchedule, error) {
	var (
		id, systemAccountID, accountID, model string
		profile, penaltyAction                string
		intervalMinutes, penaltyThreshold     int64
		recoveryMinutes, enabled, revision    int64
		nextRunRaw, createdRaw, updatedRaw    string
		lastRunID, lastRunAt, lastRunStatus   pgtype.Text
		leaseOwner, leaseToken, leaseUntil    pgtype.Text
	)
	destinations := []any{
		&id, &systemAccountID, &accountID, &model,
		&intervalMinutes, &profile, &penaltyThreshold, &penaltyAction, &recoveryMinutes,
		&enabled, &revision, &nextRunRaw,
		&lastRunID, &lastRunAt, &lastRunStatus,
		&leaseOwner, &leaseToken, &leaseUntil,
		&createdRaw, &updatedRaw,
	}
	destinations = append(destinations, tail...)
	if err := row.Scan(destinations...); err != nil {
		return port.ModelQualitySchedule{}, err
	}
	if !validModelQualityScheduleText(id, 256) || !validModelQualityScheduleText(systemAccountID, 256) || !validModelQualityScheduleText(accountID, 256) || !validModelQualityScheduleText(model, 4096) {
		return port.ModelQualitySchedule{}, fmt.Errorf("invalid persisted model quality schedule identity")
	}
	if intervalMinutes < 10 || intervalMinutes > 10080 || penaltyThreshold < 40 || penaltyThreshold > 100 ||
		recoveryMinutes < 10 || recoveryMinutes > 10080 || enabled < 0 || enabled > 1 || revision < 1 || revision > math.MaxInt32 {
		return port.ModelQualitySchedule{}, fmt.Errorf("invalid persisted model quality schedule numeric range")
	}
	nextRunAt, err := modelQualityPolicyParseTime(nextRunRaw)
	if err != nil {
		return port.ModelQualitySchedule{}, fmt.Errorf("parse model quality schedule next_run_at: %w", err)
	}
	createdAt, err := modelQualityPolicyParseTime(createdRaw)
	if err != nil {
		return port.ModelQualitySchedule{}, fmt.Errorf("parse model quality schedule created_at: %w", err)
	}
	updatedAt, err := modelQualityPolicyParseTime(updatedRaw)
	if err != nil {
		return port.ModelQualitySchedule{}, fmt.Errorf("parse model quality schedule updated_at: %w", err)
	}
	schedule := port.ModelQualitySchedule{
		ID: id, SystemAccountID: systemAccountID, AccountID: accountID, Model: model,
		Interval: time.Duration(intervalMinutes) * time.Minute, Profile: modelquality.Profile(profile),
		PenaltyThreshold: int(penaltyThreshold), PenaltyAction: modelquality.Action(penaltyAction),
		RecoveryInterval: time.Duration(recoveryMinutes) * time.Minute, Enabled: enabled == 1,
		Revision: modelquality.ScheduleRevision(revision), NextRunAt: nextRunAt,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
	if err := modelQualitySchedulePolicy(schedule).Policy.Validate(); err != nil {
		return port.ModelQualitySchedule{}, fmt.Errorf("invalid persisted model quality schedule policy: %w", err)
	}
	if lastRunID.Valid {
		if !validModelQualityScheduleText(lastRunID.String, 256) {
			return port.ModelQualitySchedule{}, fmt.Errorf("invalid persisted model quality schedule last run ID")
		}
		schedule.LastRunID = lastRunID.String
	}
	if lastRunAt.Valid {
		value, err := modelQualityPolicyParseTime(lastRunAt.String)
		if err != nil {
			return port.ModelQualitySchedule{}, fmt.Errorf("parse model quality schedule last_run_at: %w", err)
		}
		schedule.LastRunAt = &value
	}
	if lastRunStatus.Valid {
		status := modelquality.RunStatus(lastRunStatus.String)
		if status != modelquality.RunStatusCompleted && status != modelquality.RunStatusFailed && status != modelquality.RunStatusCanceled {
			return port.ModelQualitySchedule{}, fmt.Errorf("invalid persisted model quality schedule last run status %q", status)
		}
		schedule.LastRunStatus = &status
	}
	if lastRunAt.Valid != lastRunStatus.Valid {
		return port.ModelQualitySchedule{}, fmt.Errorf("invalid persisted model quality schedule last run facts")
	}
	if leaseToken.Valid {
		if !leaseOwner.Valid || !leaseUntil.Valid || !validModelQualityScheduleText(leaseOwner.String, 128) || !validModelQualityScheduleText(leaseToken.String, 256) {
			return port.ModelQualitySchedule{}, fmt.Errorf("invalid persisted model quality schedule token lease")
		}
		until, err := modelQualityPolicyParseTime(leaseUntil.String)
		if err != nil {
			return port.ModelQualitySchedule{}, fmt.Errorf("parse model quality schedule lease_until: %w", err)
		}
		schedule.Lease = &port.ModelQualityScheduleLease{
			OwnerID:    port.ModelQualityClaimOwnerID(leaseOwner.String),
			ClaimToken: port.ModelQualityScheduleClaimToken(leaseToken.String),
			Until:      until,
		}
	} else if leaseOwner.Valid != leaseUntil.Valid {
		return port.ModelQualitySchedule{}, fmt.Errorf("invalid persisted legacy model quality schedule lease")
	} else if leaseOwner.Valid && (!validModelQualityScheduleText(leaseOwner.String, 128) || modelQualityScheduleTimeInvalid(leaseUntil.String)) {
		return port.ModelQualitySchedule{}, fmt.Errorf("invalid persisted legacy model quality schedule lease")
	}
	return schedule, nil
}

func validateModelQualityScheduleUpsertInput(input port.ModelQualityScheduleUpsertInput) error {
	if !validModelQualityScheduleText(input.SystemAccountID, 256) || !validModelQualityScheduleText(input.AccountID, 256) || !validModelQualityScheduleText(input.Model, 4096) {
		return fmt.Errorf("model quality schedule upsert identity is invalid")
	}
	if input.UpdatedAt.IsZero() || !validModelQualityScheduleInterval(input.Interval) || !validModelQualityScheduleInterval(input.RecoveryInterval) {
		return fmt.Errorf("model quality schedule upsert time or interval is invalid")
	}
	policy := modelquality.Policy{
		SystemAccountID: input.SystemAccountID, Profile: input.Profile, ManualEnforcementEnabled: true,
		PenaltyThreshold: input.PenaltyThreshold, PenaltyAction: input.PenaltyAction,
		RecoveryIntervalMinutes: int(input.RecoveryInterval / time.Minute),
	}
	if err := policy.Validate(); err != nil {
		return fmt.Errorf("model quality schedule upsert policy is invalid: %w", err)
	}
	if input.ExpectedRevision != nil && *input.ExpectedRevision > modelquality.ScheduleRevision(math.MaxInt32) {
		return fmt.Errorf("model quality schedule expected revision is outside PostgreSQL INTEGER range")
	}
	return nil
}

func modelQualitySchedulePolicy(schedule port.ModelQualitySchedule) port.ModelQualityPolicyRecord {
	createdAt, updatedAt := schedule.CreatedAt, schedule.UpdatedAt
	return port.ModelQualityPolicyRecord{
		Policy: modelquality.Policy{
			SystemAccountID:          schedule.SystemAccountID,
			Revision:                 modelquality.PolicyRevision(schedule.Revision),
			Profile:                  schedule.Profile,
			ManualEnforcementEnabled: true,
			PenaltyThreshold:         schedule.PenaltyThreshold,
			PenaltyAction:            schedule.PenaltyAction,
			RecoveryIntervalMinutes:  int(schedule.RecoveryInterval / time.Minute),
		},
		Persisted: true,
		CreatedAt: &createdAt,
		UpdatedAt: &updatedAt,
	}
}

func validateModelQualityScheduleDeleteInput(input port.ModelQualityScheduleDeleteInput) error {
	if !validModelQualityScheduleText(input.SystemAccountID, 256) || !validModelQualityScheduleText(input.ScheduleID, 256) {
		return fmt.Errorf("model quality schedule delete identity is invalid")
	}
	if input.ExpectedRevision != nil && *input.ExpectedRevision > modelquality.ScheduleRevision(math.MaxInt32) {
		return fmt.Errorf("model quality schedule expected revision is outside PostgreSQL INTEGER range")
	}
	return nil
}

func normalizeModelQualityScheduleClaimInput(input port.ModelQualityScheduleClaimInput) port.ModelQualityScheduleClaimInput {
	if input.LeaseDuration == 0 {
		input.LeaseDuration = port.ModelQualityScheduleClaimDefaultLease
	}
	if input.Limit == 0 {
		input.Limit = port.ModelQualityScheduleClaimDefaultLimit
	}
	return input
}

func validateModelQualityScheduleClaimInput(input port.ModelQualityScheduleClaimInput) error {
	if !validModelQualityScheduleText(string(input.OwnerID), 128) ||
		input.LeaseDuration < port.ModelQualityClaimMinimumLease || input.LeaseDuration > port.ModelQualityClaimMaximumLease ||
		input.LeaseDuration%time.Millisecond != 0 ||
		input.Limit < 1 || input.Limit > port.ModelQualityScheduleClaimMaximumLimit {
		return fmt.Errorf("model quality schedule claim is invalid")
	}
	return nil
}

func validateModelQualityScheduleCompleteInput(input port.ModelQualityScheduleCompleteInput) error {
	if !validModelQualityScheduleText(input.ScheduleID, 256) || input.ExpectedRevision == 0 || input.ExpectedRevision > modelquality.ScheduleRevision(math.MaxInt32) ||
		!validModelQualityScheduleText(string(input.Lease.OwnerID), 128) || !validModelQualityScheduleText(string(input.Lease.ClaimToken), 256) ||
		input.Lease.Until.IsZero() || !validModelQualityScheduleInterval(input.Interval) {
		return fmt.Errorf("model quality schedule completion is invalid")
	}
	if input.RunID != "" && !validModelQualityScheduleText(input.RunID, 256) {
		return fmt.Errorf("model quality schedule completion run ID is invalid")
	}
	if input.Status != port.ModelQualityScheduleRunCompleted && input.Status != port.ModelQualityScheduleRunFailed && input.Status != port.ModelQualityScheduleRunCanceled {
		return fmt.Errorf("model quality schedule completion status is invalid")
	}
	return nil
}

func validModelQualityScheduleInterval(value time.Duration) bool {
	return value >= port.ModelQualityMinimumInterval && value <= port.ModelQualityMaximumInterval && value%time.Minute == 0
}

func addModelQualityScheduleInterval(value time.Time, interval time.Duration) (time.Time, error) {
	if value.IsZero() || !validModelQualityScheduleInterval(interval) {
		return time.Time{}, fmt.Errorf("model quality schedule time or interval is invalid")
	}
	next := value.UTC().Add(interval)
	if !next.After(value.UTC()) {
		return time.Time{}, fmt.Errorf("model quality schedule next run time overflowed")
	}
	return next, nil
}

func validModelQualityScheduleText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func modelQualityScheduleTimeInvalid(value string) bool {
	_, err := modelQualityPolicyParseTime(value)
	return err != nil
}

func beginModelQualityScheduleTx(ctx context.Context, beginTx modelQualityScheduleBeginTx, operation string) (pgx.Tx, error) {
	if beginTx == nil {
		return nil, fmt.Errorf("model quality schedule %s transaction starter is required", operation)
	}
	tx, err := beginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin model quality schedule %s transaction: %w", operation, err)
	}
	return tx, nil
}

func commitModelQualityScheduleWriteResult(ctx context.Context, tx pgx.Tx, committed *bool, result port.ModelQualityScheduleWriteResult) (port.ModelQualityScheduleWriteResult, error) {
	if err := tx.Commit(ctx); err != nil {
		return port.ModelQualityScheduleWriteResult{}, fmt.Errorf("commit model quality schedule write: %w", err)
	}
	*committed = true
	return result, nil
}

func rollbackModelQualityScheduleTx(tx pgx.Tx, committed *bool) func() {
	return func() {
		if *committed {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}
}

var _ port.ModelQualityScheduleWriter = (*Store)(nil)
var _ port.ModelQualityScheduleClaimer = (*Store)(nil)
var _ port.ModelQualityScheduleCompleter = (*Store)(nil)
