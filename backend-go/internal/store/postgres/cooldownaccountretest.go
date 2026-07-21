package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"juhe-ai/backend-go/internal/store/port"
)

func (s *Store) ListDueCooldownAccountRetests(ctx context.Context, input port.CooldownAccountRetestListInput) (port.CooldownAccountRetestPage, error) {
	limit := input.Limit
	if limit < 1 {
		limit = 1
	}
	if limit > port.CooldownAccountRetestMaxPageSize {
		limit = port.CooldownAccountRetestMaxPageSize
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	var cursorTime, cursorPriority, cursorCreatedAt, cursorID any
	if input.Cursor != nil {
		cursorTime, cursorPriority, cursorCreatedAt, cursorID = input.Cursor.CooldownUntil, input.Cursor.Priority, input.Cursor.CreatedAt, input.Cursor.ID
	}
	rows, err := s.pool.Query(ctx, listCooldownAccountRetestCandidatesSQL, now, cursorTime, cursorPriority, cursorCreatedAt, cursorID, limit)
	if err != nil {
		return port.CooldownAccountRetestPage{}, fmt.Errorf("query cooldown account retest candidates: %w", err)
	}
	defer rows.Close()
	candidates := make([]port.CooldownAccountRetestCandidate, 0, limit)
	for rows.Next() {
		candidate, scanErr := scanCooldownAccountRetestCandidate(rows.Scan)
		if scanErr != nil {
			return port.CooldownAccountRetestPage{}, scanErr
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return port.CooldownAccountRetestPage{}, fmt.Errorf("iterate cooldown account retest candidates: %w", err)
	}
	page := port.CooldownAccountRetestPage{Candidates: candidates}
	if len(candidates) == limit {
		last := candidates[len(candidates)-1]
		page.NextCursor = &port.CooldownAccountRetestCursor{CooldownUntil: last.CooldownUntil, Priority: last.Priority, CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}

func (s *Store) FindDueCooldownAccountRetest(ctx context.Context, accountID string, now time.Time) (port.CooldownAccountRetestCandidate, bool, error) {
	if now.IsZero() {
		now = time.Now()
	}
	candidate, err := scanCooldownAccountRetestCandidate(func(dest ...any) error {
		return s.pool.QueryRow(ctx, findCooldownAccountRetestCandidateSQL, accountID, now).Scan(dest...)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.CooldownAccountRetestCandidate{}, false, nil
	}
	if err != nil {
		return port.CooldownAccountRetestCandidate{}, false, err
	}
	return candidate, true, nil
}

type cooldownAccountRetestScan func(...any) error

func scanCooldownAccountRetestCandidate(scan cooldownAccountRetestScan) (port.CooldownAccountRetestCandidate, error) {
	var candidate port.CooldownAccountRetestCandidate
	if err := scan(&candidate.ID, &candidate.Name, &candidate.ConfigRevision, &candidate.CooldownUntil,
		&candidate.Priority, &candidate.CreatedAt, &candidate.ObservationStartedAt, &candidate.SystemAccountID,
		&candidate.GroupID, &candidate.HealthCheckModel, &candidate.HealthCheckEndpointMode); err != nil {
		return port.CooldownAccountRetestCandidate{}, fmt.Errorf("scan cooldown account retest candidate: %w", err)
	}
	return candidate, nil
}
