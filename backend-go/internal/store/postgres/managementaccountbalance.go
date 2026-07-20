package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
)

func (s *Store) GetManagementAccountBalanceSnapshot(ctx context.Context, input port.ManagementAccountBalanceInput) (port.ManagementAccountBalanceSnapshot, bool, error) {
	var row port.ManagementAccountBalanceSnapshot
	err := s.pool.QueryRow(ctx, managementAccountBalanceSnapshotSQL, strings.TrimSpace(input.AccountID), strings.TrimSpace(input.SystemAccountID)).Scan(
		&row.AccountID, &row.SystemAccountID, &row.Status, &row.SnapshotJSON, &row.NextRefreshAt, &row.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAccountBalanceSnapshot{}, false, nil
	}
	if err != nil {
		return port.ManagementAccountBalanceSnapshot{}, false, fmt.Errorf("get management account balance snapshot: %w", err)
	}
	return row, true, nil
}

func (s *Store) GetManagementAccountBalanceCandidate(ctx context.Context, input port.ManagementAccountBalanceInput) (port.ManagementAccountBalanceCandidate, bool, error) {
	var row port.ManagementAccountBalanceCandidate
	err := s.pool.QueryRow(ctx, managementAccountBalanceCandidateSQL, strings.TrimSpace(input.AccountID), strings.TrimSpace(input.SystemAccountID)).Scan(
		&row.AccountID, &row.SystemAccountID, &row.ProviderCode, &row.ProtocolCode, &row.ProtocolVersion, &row.Type, &row.CredentialsEncrypted,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAccountBalanceCandidate{}, false, nil
	}
	if err != nil {
		return port.ManagementAccountBalanceCandidate{}, false, fmt.Errorf("get management account balance candidate: %w", err)
	}
	return row, true, nil
}

func (s *Store) UpsertManagementAccountBalanceSnapshot(ctx context.Context, snapshot port.ManagementAccountBalanceSnapshot) error {
	_, err := s.pool.Exec(ctx, managementAccountBalanceUpsertSQL,
		snapshot.SystemAccountID, snapshot.AccountID, snapshot.Status, snapshot.SnapshotJSON,
	)
	if err != nil {
		return fmt.Errorf("upsert management account balance snapshot: %w", err)
	}
	return nil
}

const managementAccountBalanceSnapshotSQL = `
SELECT account_id, system_account_id, refresh_status, snapshot_json,
       COALESCE(next_refresh_after::text, ''), updated_at::text
FROM juhe_stats.account_usage_snapshots
WHERE account_id = $1
  AND kind = 'relay_balance'
  AND ($2 = '' OR system_account_id = $2)
LIMIT 1`

const managementAccountBalanceCandidateSQL = `
SELECT id, system_account_id, provider_code, protocol_code, protocol_version,
       type, credentials_encrypted
FROM juhe_business.accounts
WHERE id = $1
  AND ($2 = '' OR system_account_id = $2)
  AND deleted_at IS NULL
  AND type = 'api_key'
LIMIT 1`

const managementAccountBalanceUpsertSQL = `
INSERT INTO juhe_stats.account_usage_snapshots (
  system_account_id, account_id, kind, source, snapshot_json, refresh_status,
  last_attempt_at, updated_at, created_at
) VALUES ($1, $2, 'relay_balance', 'upstream_api', $4, $3,
          now(), now(), now())
ON CONFLICT (system_account_id, account_id, kind) DO UPDATE SET
  source = EXCLUDED.source,
  snapshot_json = EXCLUDED.snapshot_json,
  refresh_status = EXCLUDED.refresh_status,
  last_attempt_at = EXCLUDED.last_attempt_at,
  updated_at = EXCLUDED.updated_at`

var _ port.ManagementAccountBalanceReader = (*Store)(nil)
var _ port.ManagementAccountBalanceWriter = (*Store)(nil)
