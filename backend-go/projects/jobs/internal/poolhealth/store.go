package poolhealth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Store is the PG fact-source adapter. The probe task is the only writer of
// proxy_profiles.pool_health_*; it must not touch test_status/latency fields or
// proxy_profiles.updated_at (the management CAS revision).
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("pool health store requires a database")
	}
	return &Store{db: db}, nil
}

// ListDue returns enabled proxies referenced by at least one enabled pool
// member, ordered by pool_health_checked_at (never-checked first). Errors,
// cancelled runs, and projection failures leave checked_at unchanged so the
// same row is retried next round.
func (s *Store) ListDue(ctx context.Context, now time.Time, limit int) ([]Target, error) {
	if limit <= 0 {
		limit = DefaultBatchSize
	}
	rows, err := s.db.QueryContext(ctx, `SELECT profile.id, profile.name, profile.type, profile.host, profile.port,
       COALESCE(profile.username, ''), COALESCE(profile.password_encrypted, '')
FROM juhe_business.proxy_profiles profile
WHERE profile.enabled = TRUE
  AND (profile.pool_health_checked_at IS NULL OR profile.pool_health_checked_at <= $2)
  AND EXISTS (
      SELECT 1
      FROM juhe_business.proxy_pool_members member
      JOIN juhe_business.proxy_pools pool ON pool.id = member.pool_id
      WHERE member.proxy_profile_id = profile.id
        AND member.enabled = TRUE
        AND pool.enabled = TRUE
  )
ORDER BY profile.pool_health_checked_at NULLS FIRST, profile.id ASC
LIMIT $1`, limit, now.UTC().Add(-DueInterval))
	if err != nil {
		return nil, fmt.Errorf("list due proxy pool members: %w", err)
	}
	defer rows.Close()
	out := make([]Target, 0, limit)
	for rows.Next() {
		var target Target
		if err := rows.Scan(&target.ProfileID, &target.Name, &target.Type, &target.Host,
			&target.Port, &target.Username, &target.PasswordEncrypted); err != nil {
			return nil, fmt.Errorf("scan due proxy pool member: %w", err)
		}
		out = append(out, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due proxy pool members: %w", err)
	}
	return out, nil
}

// Record projects one completed probe. A success clears the streak and marks
// healthy; the 1st/2nd consecutive failure marks degraded; the 3rd enters dead.
// checked_at is only updated here, i.e. after a successful PG projection.
//
// The target snapshot taken by ListDue is compared under the row lock before
// writing: a connection edit (host/port/credentials) or disable that happened
// while the probe was in flight wins, and the stale result is discarded.
func (s *Store) Record(ctx context.Context, target Target, success bool, checkedAt time.Time) (RecordResult, error) {
	profileID := strings.TrimSpace(target.ProfileID)
	if profileID == "" {
		return RecordResult{}, errors.New("pool health profile id is empty")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RecordResult{}, fmt.Errorf("begin pool health projection: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var currentStatus sql.NullString
	var currentStreak int
	var host string
	var port int
	var username, password sql.NullString
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(pool_health_status, ''), pool_health_failure_streak,
       host, port, username, password_encrypted, enabled
FROM juhe_business.proxy_profiles
WHERE id = $1
FOR UPDATE`, profileID).Scan(&currentStatus, &currentStreak, &host, &port, &username, &password, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return RecordResult{}, fmt.Errorf("pool health profile %s not found", profileID)
	}
	if err != nil {
		return RecordResult{}, fmt.Errorf("lock pool health profile: %w", err)
	}
	if !enabled || host != target.Host || port != target.Port ||
		strings.TrimSpace(username.String) != strings.TrimSpace(target.Username) ||
		password.String != target.PasswordEncrypted {
		return RecordResult{ProfileID: profileID, Skipped: true}, nil
	}
	nextStatus, nextStreak := nextHealthState(success, currentStreak)
	if _, err := tx.ExecContext(ctx, `UPDATE juhe_business.proxy_profiles
SET pool_health_status = $2,
    pool_health_failure_streak = $3,
    pool_health_checked_at = $4
WHERE id = $1`, profileID, string(nextStatus), nextStreak, checkedAt.UTC()); err != nil {
		return RecordResult{}, fmt.Errorf("project pool health: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RecordResult{}, fmt.Errorf("commit pool health projection: %w", err)
	}
	previous := HealthState(strings.TrimSpace(currentStatus.String))
	if previous == "" {
		previous = HealthUnknown
	}
	return RecordResult{
		ProfileID:    profileID,
		Status:       nextStatus,
		Streak:       nextStreak,
		Transitioned: previous != nextStatus,
	}, nil
}

// nextHealthState is the §7.2 projection: success → healthy/0; failures 1-2 →
// degraded; failure 3+ → dead (stays dead until a success).
func nextHealthState(success bool, currentStreak int) (HealthState, int) {
	if success {
		return HealthHealthy, 0
	}
	nextStreak := currentStreak + 1
	if nextStreak >= FailureThreshold {
		return HealthDead, nextStreak
	}
	return HealthDegraded, nextStreak
}
