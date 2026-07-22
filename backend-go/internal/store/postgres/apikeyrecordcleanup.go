package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
)

const runAPIKeyRecordCleanupOnceSQL = `
WITH candidates AS (
  SELECT api_key_id, system_account_id
  FROM juhe_dataset.api_key_record_cleanup_targets
  ORDER BY COALESCE(last_attempt_at, created_at) ASC, created_at ASC, api_key_id ASC
  LIMIT $1
  FOR UPDATE SKIP LOCKED
)
UPDATE juhe_dataset.api_key_record_cleanup_targets AS target
SET attempt_count = target.attempt_count + 1,
    last_attempt_at = $2,
    last_blocked_reason = $3,
    last_error_message = NULL,
    updated_at = $2
FROM candidates
WHERE target.api_key_id = candidates.api_key_id
  AND target.system_account_id = candidates.system_account_id
`

type apiKeyRecordCleanupRun func(
	context.Context,
	pgx.Tx,
	port.APIKeyRecordCleanupRunInput,
) (int64, error)

func (s *Store) RunAPIKeyRecordCleanupOnce(
	ctx context.Context,
	input port.APIKeyRecordCleanupRunInput,
) (port.APIKeyRecordCleanupRunResult, error) {
	return runAPIKeyRecordCleanupOnceInTx(ctx, s.pool.BeginTx, deferAPIKeyRecordCleanupTargets, input)
}

func runAPIKeyRecordCleanupOnceInTx(
	ctx context.Context,
	beginTx func(context.Context, pgx.TxOptions) (pgx.Tx, error),
	run apiKeyRecordCleanupRun,
	input port.APIKeyRecordCleanupRunInput,
) (port.APIKeyRecordCleanupRunResult, error) {
	if err := validateAPIKeyRecordCleanupRunInput(input); err != nil {
		return port.APIKeyRecordCleanupRunResult{}, err
	}
	tx, err := beginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.APIKeyRecordCleanupRunResult{}, fmt.Errorf("begin API Key record cleanup tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	attempted, err := run(ctx, tx, input)
	if err != nil {
		return port.APIKeyRecordCleanupRunResult{}, err
	}
	if attempted < 0 || attempted > int64(input.Limit) {
		return port.APIKeyRecordCleanupRunResult{}, fmt.Errorf(
			"API Key record cleanup affected %d targets, limit %d",
			attempted,
			input.Limit,
		)
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return port.APIKeyRecordCleanupRunResult{}, fmt.Errorf("commit API Key record cleanup tx rolled back: %w", err)
		}
		return port.APIKeyRecordCleanupRunResult{}, fmt.Errorf("commit API Key record cleanup tx: %w", err)
	}
	committed = true
	return port.APIKeyRecordCleanupRunResult{Attempted: attempted, Deferred: attempted}, nil
}

func deferAPIKeyRecordCleanupTargets(
	ctx context.Context,
	tx pgx.Tx,
	input port.APIKeyRecordCleanupRunInput,
) (int64, error) {
	commandTag, err := tx.Exec(
		ctx,
		runAPIKeyRecordCleanupOnceSQL,
		input.Limit,
		input.AttemptedAt.UTC(),
		strings.TrimSpace(input.BlockedReason),
	)
	if err != nil {
		return 0, fmt.Errorf("claim and defer API Key record cleanup targets: %w", err)
	}
	return commandTag.RowsAffected(), nil
}

func validateAPIKeyRecordCleanupRunInput(input port.APIKeyRecordCleanupRunInput) error {
	if input.Limit <= 0 || input.Limit > port.MaxAPIKeyRecordCleanupTargetLimit {
		return fmt.Errorf(
			"API Key record cleanup limit must be between 1 and %d",
			port.MaxAPIKeyRecordCleanupTargetLimit,
		)
	}
	if input.AttemptedAt.IsZero() {
		return fmt.Errorf("API Key record cleanup attempted_at is required")
	}
	if strings.TrimSpace(input.BlockedReason) == "" {
		return fmt.Errorf("API Key record cleanup blocked reason is required")
	}
	return nil
}

var _ port.APIKeyRecordCleanupRunner = (*Store)(nil)
