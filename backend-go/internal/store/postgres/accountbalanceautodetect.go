package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
)

func (s *Store) LoadAccountBalanceAutoDetectCandidate(ctx context.Context, input port.AccountBalanceAutoDetectLookup) (port.AccountBalanceAutoDetectCandidate, bool, error) {
	var candidate port.AccountBalanceAutoDetectCandidate
	err := s.pool.QueryRow(ctx, accountBalanceAutoDetectCandidateSQL, strings.TrimSpace(input.AccountID), input.ConfigRevision).Scan(
		&candidate.AccountID, &candidate.SystemAccountID, &candidate.ConfigRevision,
		&candidate.CredentialsEncrypted, &candidate.ProxyProfileID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.AccountBalanceAutoDetectCandidate{}, false, nil
	}
	if err != nil {
		return port.AccountBalanceAutoDetectCandidate{}, false, fmt.Errorf("load account balance auto detect candidate: %w", err)
	}
	return candidate, true, nil
}

func (s *Store) CommitAccountBalanceAutoDetect(ctx context.Context, input port.AccountBalanceAutoDetectCommit) (bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin account balance auto detect tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	command, err := tx.Exec(ctx, accountBalanceAutoDetectEnableSQL,
		input.ConfigJSON, input.NextRefreshAt, input.CompletedAt, input.AccountID, input.ExpectedConfigRevision,
	)
	if err != nil {
		return false, fmt.Errorf("enable detected account balance config: %w", err)
	}
	if command.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := tx.Exec(ctx, accountBalanceAutoDetectSnapshotSQL,
		input.SystemAccountID, input.AccountID, input.SnapshotJSON, input.SnapshotStatus,
		input.CompletedAt, input.NextRefreshAt,
	); err != nil {
		return false, fmt.Errorf("write detected account balance snapshot: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit account balance auto detect tx: %w", err)
	}
	committed = true
	return true, nil
}

const accountBalanceAutoDetectCandidateSQL = `
SELECT id, system_account_id, config_revision, credentials_encrypted,
       COALESCE(proxy_profile_id, '')
FROM juhe_business.accounts
WHERE id = $1
  AND config_revision = $2
  AND status = 'active'
  AND schedulable = true
  AND type = 'api_key'
  AND balance_query_enabled = false
  AND balance_query_config_json = '{}'
  AND deleted_at IS NULL
  AND authorization_instance_authorization_id IS NULL
LIMIT 1`

const accountBalanceAutoDetectEnableSQL = `
UPDATE juhe_business.accounts
SET balance_query_enabled = true,
    balance_query_config_json = $1,
    balance_query_next_refresh_at = $2,
    updated_at = $3
WHERE id = $4
  AND config_revision = $5
  AND status = 'active'
  AND schedulable = true
  AND type = 'api_key'
  AND balance_query_enabled = false
  AND balance_query_config_json = '{}'
  AND deleted_at IS NULL
  AND authorization_instance_authorization_id IS NULL`

const accountBalanceAutoDetectSnapshotSQL = `
INSERT INTO juhe_stats.account_usage_snapshots (
  system_account_id, account_id, kind, source, snapshot_json, refresh_status,
  last_attempt_at, last_success_at, next_refresh_after, last_error_message,
  updated_at, created_at
) VALUES ($1, $2, 'relay_balance', 'upstream_api', $3, $4,
          $5, $5, $6, NULL, $5, $5)
ON CONFLICT (system_account_id, account_id, kind) DO UPDATE SET
  source = EXCLUDED.source,
  snapshot_json = EXCLUDED.snapshot_json,
  refresh_status = EXCLUDED.refresh_status,
  last_attempt_at = EXCLUDED.last_attempt_at,
  last_success_at = EXCLUDED.last_success_at,
  next_refresh_after = EXCLUDED.next_refresh_after,
  last_error_message = EXCLUDED.last_error_message,
  updated_at = EXCLUDED.updated_at`

var _ port.AccountBalanceAutoDetectStore = (*Store)(nil)
