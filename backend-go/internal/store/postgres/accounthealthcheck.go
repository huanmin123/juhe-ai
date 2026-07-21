package postgres

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
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
LEFT JOIN juhe_business.resource_authorizations AS ra
  ON ra.id = a.authorization_instance_authorization_id
LEFT JOIN juhe_business.accounts AS source
  ON source.id = a.authorization_instance_source_account_id
  AND source.deleted_at IS NULL
LEFT JOIN LATERAL (
  SELECT ga.group_id
  FROM juhe_business.group_accounts AS ga
  WHERE ga.account_id = a.id AND ga.system_account_id = a.system_account_id AND ga.enabled = true
    AND (a.authorization_instance_authorization_id IS NULL OR ga.account_authorization_id = a.authorization_instance_authorization_id)
  ORDER BY ga.updated_at DESC, ga.group_id LIMIT 1
) AS binding ON true
WHERE a.id = $1 AND a.deleted_at IS NULL
  AND a.status IN ('active', 'pending_test')
  AND (a.status = 'pending_test' OR a.schedulable = true)
  AND (a.account_expires_at IS NULL OR a.account_expires_at > $2)
  AND (
    a.authorization_instance_authorization_id IS NULL
    OR (
      ra.id IS NOT NULL
      AND ra.status = 'active'
      AND (ra.expires_at IS NULL OR ra.expires_at > $2)
      AND source.id IS NOT NULL
      AND source.status = 'active'
      AND source.schedulable = true
      AND (source.last_error_code IS NULL OR source.last_error_code <> 'account_expired')
      AND (source.account_expires_at IS NULL OR source.account_expires_at > $2)
      AND (source.cooldown_until IS NULL OR source.cooldown_until <= $2)
    )
  )
  AND (a.next_health_check_at IS NULL OR a.next_health_check_at <= $2)
  AND binding.group_id IS NOT NULL
LIMIT 1`

func (s *Store) ListAccountHealthCheckCandidates(ctx context.Context, cursor string, limit int, now time.Time) (port.AccountHealthCheckCandidatePage, error) {
	if limit <= 0 || limit > port.AccountHealthCheckMaxPageSize {
		limit = port.AccountHealthCheckMaxPageSize
	}
	cursorPriority := -1
	cursorID := ""
	if cursor = strings.TrimSpace(cursor); cursor != "" {
		priorityText, accountID, ok := strings.Cut(cursor, ":")
		if !ok || strings.TrimSpace(accountID) == "" {
			return port.AccountHealthCheckCandidatePage{}, fmt.Errorf("invalid account health check cursor")
		}
		priority, err := strconv.Atoi(priorityText)
		if err != nil || priority < 0 || priority > 1 {
			return port.AccountHealthCheckCandidatePage{}, fmt.Errorf("invalid account health check cursor")
		}
		cursorPriority = priority
		cursorID = accountID
	}
	now = now.UTC()
	rows, err := s.pool.Query(ctx, accountHealthCheckCandidatesSQL, cursorPriority, cursorID, limit, now)
	if err != nil {
		return port.AccountHealthCheckCandidatePage{}, fmt.Errorf("list account health check candidates: %w", err)
	}
	defer rows.Close()
	page := port.AccountHealthCheckCandidatePage{Items: make([]port.AccountHealthCheckCandidate, 0, limit)}
	for rows.Next() {
		var item port.AccountHealthCheckCandidate
		var statusPriority int
		if err := rows.Scan(&item.ID, &item.ConfigRevision, &item.Status, &item.Schedulable, &item.BoundGroupID, &item.ExpiresAt, &item.NextCheckAt, &statusPriority); err != nil {
			return port.AccountHealthCheckCandidatePage{}, fmt.Errorf("scan account health check candidate: %w", err)
		}
		page.Items = append(page.Items, item)
		page.NextCursor = fmt.Sprintf("%d:%s", statusPriority, item.ID)
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
