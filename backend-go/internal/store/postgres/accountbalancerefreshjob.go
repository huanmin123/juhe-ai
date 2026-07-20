package postgres

import (
	"context"
	"fmt"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const maxAccountBalanceRefreshCandidateLimit = 256

func (s *Store) ListAccountBalanceRefreshRecoveryCandidates(ctx context.Context, limit int) ([]port.AccountBalanceRefreshCandidate, error) {
	return s.listAccountBalanceRefreshCandidates(ctx, accountBalanceRefreshRecoveryCandidatesSQL, normalizedAccountBalanceRefreshLimit(limit))
}

func (s *Store) ListAccountBalanceRefreshDueCandidates(ctx context.Context, now time.Time, limit int) ([]port.AccountBalanceRefreshCandidate, error) {
	boundedLimit := normalizedAccountBalanceRefreshLimit(limit)
	if boundedLimit == 0 {
		return []port.AccountBalanceRefreshCandidate{}, nil
	}
	rows, err := s.pool.Query(ctx, accountBalanceRefreshDueCandidatesSQL, now.UTC(), boundedLimit)
	if err != nil {
		return nil, fmt.Errorf("list account balance refresh due candidates: %w", err)
	}
	return scanAccountBalanceRefreshCandidates(rows)
}

func (s *Store) listAccountBalanceRefreshCandidates(ctx context.Context, query string, limit int) ([]port.AccountBalanceRefreshCandidate, error) {
	if limit == 0 {
		return []port.AccountBalanceRefreshCandidate{}, nil
	}
	rows, err := s.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("list account balance refresh recovery candidates: %w", err)
	}
	return scanAccountBalanceRefreshCandidates(rows)
}

type accountBalanceRefreshRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

func scanAccountBalanceRefreshCandidates(rows accountBalanceRefreshRows) ([]port.AccountBalanceRefreshCandidate, error) {
	defer rows.Close()
	candidates := make([]port.AccountBalanceRefreshCandidate, 0)
	for rows.Next() {
		var candidate port.AccountBalanceRefreshCandidate
		if err := rows.Scan(
			&candidate.ID,
			&candidate.SystemAccountID,
			&candidate.ConfigRevision,
			&candidate.CredentialsEncrypted,
			&candidate.BalanceQueryConfigJSON,
			&candidate.NextRefreshAt,
			&candidate.StateUpdatedAt,
			&candidate.ProxyProfileID,
		); err != nil {
			return nil, fmt.Errorf("scan account balance refresh candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate account balance refresh candidates: %w", err)
	}
	return candidates, nil
}

func normalizedAccountBalanceRefreshLimit(limit int) int {
	if limit <= 0 {
		return 0
	}
	return min(limit, maxAccountBalanceRefreshCandidateLimit)
}

const accountBalanceRefreshRecoveryCandidatesSQL = `
SELECT id, system_account_id, config_revision, credentials_encrypted,
       balance_query_config_json::text, balance_query_next_refresh_at,
       updated_at, proxy_profile_id
FROM juhe_business.accounts
WHERE status = 'active'
  AND schedulable = 1
  AND type = 'api_key'
  AND balance_query_enabled = 1
  AND balance_query_next_refresh_at IS NULL
  AND deleted_at IS NULL
  AND authorization_instance_authorization_id IS NULL
ORDER BY id ASC
LIMIT $1`

const accountBalanceRefreshDueCandidatesSQL = `
SELECT id, system_account_id, config_revision, credentials_encrypted,
       balance_query_config_json::text, balance_query_next_refresh_at,
       updated_at, proxy_profile_id
FROM juhe_business.accounts
WHERE status = 'active'
  AND schedulable = 1
  AND type = 'api_key'
  AND balance_query_enabled = 1
  AND balance_query_next_refresh_at IS NOT NULL
  AND balance_query_next_refresh_at <= $1
  AND deleted_at IS NULL
  AND authorization_instance_authorization_id IS NULL
ORDER BY balance_query_next_refresh_at ASC, id ASC
LIMIT $2`

var _ port.AccountBalanceRefreshJobReader = (*Store)(nil)
