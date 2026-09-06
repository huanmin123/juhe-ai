package accounts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Lock family mirrors storage/account-lock.repository.ts +
// modules/accounts/account-lock.routes.ts: the account_lock_states row is the
// authoritative lock lifecycle (UNLOCKED / LOCKED_IDLE / ENGAGED /
// DEAD_CONFIRMED) and every mutation CAS-advances both the lock generation and
// the account config_revision.

const (
	defaultLockDeathTimeoutSeconds  = 300
	defaultLockRetryIntervalSeconds = 5
)

// AccountLockState mirrors AccountLockState.
type AccountLockState struct {
	AccountID                string `json:"accountId"`
	Enabled                  bool   `json:"enabled"`
	LockState                string `json:"lockState"`
	LockDeathTimeoutSeconds  int    `json:"lockDeathTimeoutSeconds"`
	LockRetryIntervalSeconds int    `json:"lockRetryIntervalSeconds"`
	Generation               int64  `json:"generation"`
	UpdatedAt                string `json:"updatedAt"`
}

// NormalizeLockDeathTimeoutSeconds mirrors normalizeAccountLockDeathTimeoutSeconds.
func NormalizeLockDeathTimeoutSeconds(value int) (int, error) {
	return normalizeLockInteger(value, defaultLockDeathTimeoutSeconds, 30, 3600, "锁死死亡窗口")
}

// NormalizeLockRetryIntervalSeconds mirrors normalizeAccountLockRetryIntervalSeconds.
func NormalizeLockRetryIntervalSeconds(value int) (int, error) {
	return normalizeLockInteger(value, defaultLockRetryIntervalSeconds, 5, 30, "锁死重试间隔")
}

func normalizeLockInteger(value, fallback, min, max int, label string) (int, error) {
	if value < min || value > max {
		return fallback, &ValidationError{Message: label + "必须是 " + itoa(min) + ".." + itoa(max) + " 的整数"}
	}
	return value, nil
}

type accountLockRow struct {
	accountID      string
	enabled        int
	lockState      string
	deathTimeout   int
	retryInterval  int
	incidentID     sql.NullString
	incidentStart  sql.NullString
	deadlineAt     sql.NullString
	originalStatus sql.NullString
	provenance     sql.NullString
	nextRetryAtMs  sql.NullInt64
	leaseID        sql.NullString
	leaseUntilMs   sql.NullInt64
	generation     int64
	updatedAt      string
}

func (s *Store) findAccountLockState(ctx context.Context, q queryer, accountID string) (*accountLockRow, error) {
	var row accountLockRow
	err := q.QueryRowContext(ctx, s.bind(`SELECT account_id, enabled, lock_state,
			lock_death_timeout_seconds, lock_retry_interval_seconds, incident_id,
			incident_started_at, deadline_at, original_status, provenance,
			next_retry_at_ms, lease_id, lease_until_ms, generation, updated_at
		FROM `+s.table("account_lock_states")+` WHERE account_id = ?`), accountID).
		Scan(&row.accountID, &row.enabled, &row.lockState, &row.deathTimeout,
			&row.retryInterval, &row.incidentID, &row.incidentStart, &row.deadlineAt,
			&row.originalStatus, &row.provenance, &row.nextRetryAtMs, &row.leaseID,
			&row.leaseUntilMs, &row.generation, &row.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// SetLockInput mirrors the setAccountLockAsync input.
type SetLockInput struct {
	AccountID                string
	Enabled                  bool
	ExpectedConfigRevision   int64
	LockDeathTimeoutSeconds  *int
	LockRetryIntervalSeconds *int
	ExpectedLockGeneration   *int64
}

// SetLock mirrors setAccountLockAsync: scope-checked account row, config
// revision CAS, lock generation CAS upsert and the config_revision bump in one
// transaction. Returns (nil, nil) when the account is missing or outside the
// access scope.
func (s *Store) SetLock(ctx context.Context, input SetLockInput, access AccessScope) (*AccountLockState, error) {
	ctx = ensureCtx(ctx)
	id := strings.TrimSpace(input.AccountID)
	if id == "" {
		return nil, &ValidationError{Message: lockNotFoundMessage}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	scoped := access.manageableID()
	scopeClause := ""
	args := []any{id}
	if scoped != "" {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	var rowID, systemAccountID string
	var configRevision int64
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id, system_account_id, config_revision
		FROM `+s.table("accounts")+`
		WHERE id = ? AND deleted_at IS NULL`+scopeClause), args...).
		Scan(&rowID, &systemAccountID, &configRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !access.canAccessAll() && systemAccountID != access.ViewerID {
		return nil, nil
	}
	if input.ExpectedConfigRevision > 0 && configRevision != input.ExpectedConfigRevision {
		return nil, &RevisionConflictError{Message: lockConfigConflictMessage}
	}
	previous, err := s.findAccountLockState(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if input.ExpectedLockGeneration != nil {
		var previousGeneration int64
		if previous != nil {
			previousGeneration = previous.generation
		}
		if previousGeneration != *input.ExpectedLockGeneration {
			return nil, &RevisionConflictError{Message: lockStateConflictMessage}
		}
	}
	timeout := defaultLockDeathTimeoutSeconds
	if previous != nil && previous.deathTimeout >= 30 && previous.deathTimeout <= 3600 {
		timeout = previous.deathTimeout
	}
	if input.LockDeathTimeoutSeconds != nil {
		if timeout, err = NormalizeLockDeathTimeoutSeconds(*input.LockDeathTimeoutSeconds); err != nil {
			return nil, err
		}
	}
	interval := defaultLockRetryIntervalSeconds
	if previous != nil && previous.retryInterval >= 5 && previous.retryInterval <= 30 {
		interval = previous.retryInterval
	}
	if input.LockRetryIntervalSeconds != nil {
		if interval, err = NormalizeLockRetryIntervalSeconds(*input.LockRetryIntervalSeconds); err != nil {
			return nil, err
		}
	}

	// preserveActiveIncident: re-locking an enabled account whose incident is
	// still ENGAGED keeps the incident bookkeeping and stays ENGAGED.
	preserveIncident := input.Enabled && previous != nil &&
		previous.enabled == 1 && previous.lockState == "ENGAGED"
	// Node account-lock.repository.ts setAccountLockAsync: the retry lease
	// only survives when the incident is preserved AND neither lock config
	// field actually changed; any config change releases the lease.
	retryConfigChanged := previous != nil && previous.retryInterval != interval
	deathConfigChanged := previous != nil && previous.deathTimeout != timeout
	preserveLease := preserveIncident && !retryConfigChanged && !deathConfigChanged
	nextState := "LOCKED_IDLE"
	if !input.Enabled {
		nextState = "UNLOCKED"
	} else if preserveIncident {
		nextState = "ENGAGED"
	}
	var previousGeneration int64
	if previous != nil {
		previousGeneration = previous.generation
	}
	generation := previousGeneration + 1
	if generation < 1 {
		generation = 1
	}
	now := isoMillis(s.now())
	var incidentID, incidentStart, deadlineAt, originalStatus, provenance any
	var nextRetryAtMs, leaseID, leaseUntilMs any
	if preserveIncident {
		incidentID = previous.incidentID
		incidentStart = previous.incidentStart
		deadlineAt = previous.deadlineAt
		originalStatus = previous.originalStatus
		provenance = previous.provenance
		// A changed death timeout recomputes the deadline from the original
		// incident start (Node: incidentStartedAt + timeout, ISO output).
		if deathConfigChanged && previous.incidentStart.Valid && strings.TrimSpace(previous.incidentStart.String) != "" {
			parsedStart, err := time.Parse(time.RFC3339Nano, previous.incidentStart.String)
			if err != nil {
				return nil, &ValidationError{Message: "时间必须是有效时间字符串"}
			}
			deadlineAt = isoMillis(parsedStart.Add(time.Duration(timeout) * time.Second))
		}
	}
	if preserveLease {
		nextRetryAtMs = previous.nextRetryAtMs
		leaseID = previous.leaseID
		leaseUntilMs = previous.leaseUntilMs
	}
	upsert, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_lock_states")+`
		(account_id, enabled, lock_state, lock_death_timeout_seconds, lock_retry_interval_seconds,
		 incident_id, incident_started_at, deadline_at, original_status, provenance,
		 next_retry_at_ms, lease_id, lease_until_ms, generation, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET
			enabled = excluded.enabled,
			lock_state = excluded.lock_state,
			lock_death_timeout_seconds = excluded.lock_death_timeout_seconds,
			lock_retry_interval_seconds = excluded.lock_retry_interval_seconds,
			incident_id = excluded.incident_id,
			incident_started_at = excluded.incident_started_at,
			deadline_at = excluded.deadline_at,
			original_status = excluded.original_status,
			provenance = excluded.provenance,
			next_retry_at_ms = excluded.next_retry_at_ms,
			lease_id = excluded.lease_id,
			lease_until_ms = excluded.lease_until_ms,
			generation = excluded.generation,
			updated_at = excluded.updated_at
		WHERE `+lockStateTableRef(s.pg)+`.generation = ?`),
		id, boolInt(input.Enabled), nextState, timeout, interval,
		incidentID, incidentStart, deadlineAt, originalStatus, provenance,
		nextRetryAtMs, leaseID, leaseUntilMs,
		generation, now, previousGeneration)
	if err != nil {
		return nil, err
	}
	if affected, _ := upsert.RowsAffected(); affected != 1 {
		return nil, &RevisionConflictError{Message: lockStateConflictMessage}
	}
	configBump, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
		SET config_revision = config_revision + 1, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL AND config_revision = ?`),
		now, id, configRevision)
	if err != nil {
		return nil, err
	}
	if affected, _ := configBump.RowsAffected(); affected != 1 {
		return nil, &RevisionConflictError{Message: lockConfigConflictMessage}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &AccountLockState{
		AccountID:                id,
		Enabled:                  input.Enabled,
		LockState:                nextState,
		LockDeathTimeoutSeconds:  timeout,
		LockRetryIntervalSeconds: interval,
		Generation:               generation,
		UpdatedAt:                now,
	}, nil
}

// lockStateTableRef renders the target-table reference the upsert WHERE needs
// (PostgreSQL qualifies the schema, SQLite does not).
func lockStateTableRef(pg bool) string {
	if pg {
		return "juhe_business.account_lock_states"
	}
	return "account_lock_states"
}

// LockConfig mirrors updateAccountLockConfigAsync: keep the current enabled
// flag, require at least one config field and fence on the current generation.
func (s *Store) LockConfig(ctx context.Context, input SetLockInput, access AccessScope) (*AccountLockState, error) {
	if input.LockDeathTimeoutSeconds == nil && input.LockRetryIntervalSeconds == nil {
		return nil, &ValidationError{Message: "请至少提交一项锁死配置"}
	}
	current, err := s.findAccountLockState(ctx, s.db, strings.TrimSpace(input.AccountID))
	if err != nil {
		return nil, err
	}
	enabled := false
	var generation int64
	if current != nil {
		enabled = current.enabled == 1
		generation = current.generation
	}
	input.Enabled = enabled
	input.ExpectedLockGeneration = &generation
	return s.SetLock(ctx, input, access)
}
