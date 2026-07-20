package postgres

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
)

//go:embed queries/w6_account_health_check.sql
var accountHealthCheckCandidatesSQL string

const accountHealthCheckCurrentSQL = `
SELECT
  a.id, a.config_revision, a.status, a.schedulable, COALESCE(binding.group_id, ''),
  a.account_expires_at, a.next_health_check_at
FROM juhe_business.accounts AS a
LEFT JOIN LATERAL (
  SELECT ga.group_id
  FROM juhe_business.group_accounts AS ga
  WHERE ga.account_id = a.id AND ga.system_account_id = a.system_account_id AND ga.enabled = true
  ORDER BY ga.updated_at DESC, ga.group_id LIMIT 1
) AS binding ON true
WHERE a.id = $1 AND a.deleted_at IS NULL
  AND a.authorization_instance_source_account_id IS NULL
  AND a.authorization_instance_authorization_id IS NULL
  AND a.authorization_instance_owner_system_account_id IS NULL
  AND a.status IN ('active', 'pending_test')
  AND (a.status = 'pending_test' OR a.schedulable = true)
  AND (a.account_expires_at IS NULL OR a.account_expires_at > $2)
  AND (a.next_health_check_at IS NULL OR a.next_health_check_at <= $2)
  AND binding.group_id IS NOT NULL
LIMIT 1`

func (s *Store) ListAccountHealthCheckCandidates(ctx context.Context, afterID string, limit int, now time.Time) (port.AccountHealthCheckCandidatePage, error) {
	if limit <= 0 || limit > port.AccountHealthCheckMaxPageSize {
		limit = port.AccountHealthCheckMaxPageSize
	}
	now = now.UTC()
	rows, err := s.pool.Query(ctx, accountHealthCheckCandidatesSQL, strings.TrimSpace(afterID), limit, now)
	if err != nil {
		return port.AccountHealthCheckCandidatePage{}, fmt.Errorf("list account health check candidates: %w", err)
	}
	defer rows.Close()
	page := port.AccountHealthCheckCandidatePage{Items: make([]port.AccountHealthCheckCandidate, 0, limit)}
	for rows.Next() {
		var item port.AccountHealthCheckCandidate
		if err := rows.Scan(&item.ID, &item.ConfigRevision, &item.Status, &item.Schedulable, &item.BoundGroupID, &item.ExpiresAt, &item.NextCheckAt); err != nil {
			return port.AccountHealthCheckCandidatePage{}, fmt.Errorf("scan account health check candidate: %w", err)
		}
		page.Items = append(page.Items, item)
		page.NextCursor = item.ID
	}
	if err := rows.Err(); err != nil {
		return port.AccountHealthCheckCandidatePage{}, fmt.Errorf("read account health check candidates: %w", err)
	}
	page.HasMore = len(page.Items) == limit
	return page, nil
}

func (s *Store) GetAccountHealthCheckCandidate(ctx context.Context, accountID string, now time.Time) (port.AccountHealthCheckCandidate, bool, error) {
	var item port.AccountHealthCheckCandidate
	err := s.pool.QueryRow(ctx, accountHealthCheckCurrentSQL, strings.TrimSpace(accountID), now.UTC()).Scan(
		&item.ID, &item.ConfigRevision, &item.Status, &item.Schedulable, &item.BoundGroupID, &item.ExpiresAt, &item.NextCheckAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return port.AccountHealthCheckCandidate{}, false, nil
		}
		return port.AccountHealthCheckCandidate{}, false, fmt.Errorf("get account health check candidate: %w", err)
	}
	return item, true, nil
}

var _ port.AccountHealthCheckCandidateReader = (*Store)(nil)
var _ port.AccountHealthCheckCurrentReader = (*Store)(nil)
