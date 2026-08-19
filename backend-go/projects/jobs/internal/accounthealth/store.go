package accounthealth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type StoreMode string

const (
	StoreSQLite   StoreMode = "sqlite"
	StorePostgres StoreMode = "postgres"
)

var ErrOwnerLeaseLost = errors.New("account-health owner lease 已丢失")

type StoreConfig struct {
	Mode         StoreMode
	DatabasePath string
	PostgresURL  string
}

type OwnerLease struct {
	OwnerID    string
	FenceToken int64
}

type Store struct {
	db      *sql.DB
	mode    StoreMode
	writeMu sync.Mutex
}

func OpenStore(config StoreConfig) (*Store, error) {
	switch config.Mode {
	case StoreSQLite:
		path := strings.TrimSpace(config.DatabasePath)
		if path == "" {
			return nil, errors.New("account-health sqlite 缺少数据库路径")
		}
		dsn, err := sqliteDSN(path)
		if err != nil {
			return nil, err
		}
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		if _, err := db.Exec("PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;"); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("配置 account-health sqlite 单 writer 失败: %w", err)
		}
		return &Store{db: db, mode: config.Mode}, nil
	case StorePostgres:
		if strings.TrimSpace(config.PostgresURL) == "" {
			return nil, errors.New("account-health postgres 缺少连接 URL")
		}
		db, err := sql.Open("pgx", config.PostgresURL)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(4)
		return &Store{db: db, mode: config.Mode}, nil
	default:
		return nil, errors.New("account-health store mode 必须为 sqlite 或 postgres")
	}
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) EnsureSchema(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.mode == StorePostgres {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(918271446)"); err != nil {
			return err
		}
		if err := ensurePostgresJobsSchema(ctx, tx); err != nil {
			return err
		}
		for _, statement := range strings.Split(postgresSchema, ";") {
			if statement = strings.TrimSpace(statement); statement != "" {
				if _, err := tx.ExecContext(ctx, statement); err != nil {
					return fmt.Errorf("初始化 account-health postgres schema 失败: %w", err)
				}
			}
		}
		return tx.Commit()
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("获取 account-health sqlite schema 锁失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if _, err := conn.ExecContext(ctx, sqliteSchema); err != nil {
		return fmt.Errorf("初始化 account-health sqlite schema 失败: %w", err)
	}
	if err := ensureSQLiteCurrentStateColumns(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

// ensurePostgresJobsSchema keeps the jobs store constrained to its
// externally bootstrapped schema. In particular, the jobs role must not need
// CREATE privilege on the whole business database merely to start.
func ensurePostgresJobsSchema(ctx context.Context, tx *sql.Tx) error {
	var owner, currentUser string
	err := tx.QueryRowContext(ctx, `SELECT pg_get_userbyid(nspowner), current_user
FROM pg_namespace
WHERE nspname = 'juhe_jobs'`).Scan(&owner, &currentUser)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("缺少外部 bootstrap 创建的 juhe_jobs schema")
	}
	if err != nil {
		return fmt.Errorf("读取 account-health postgres schema owner 失败: %w", err)
	}
	if owner != currentUser {
		return fmt.Errorf("juhe_jobs schema owner 必须是当前 jobs role: owner=%s current=%s", owner, currentUser)
	}
	return nil
}

func ensureSQLiteCurrentStateColumns(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, "PRAGMA table_info(account_health_current_state)")
	if err != nil {
		return fmt.Errorf("读取 account-health sqlite state schema 失败: %w", err)
	}
	defer rows.Close()
	existing := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for name, definition := range map[string]string{
		"next_due_at":                     "TEXT",
		"failure_count":                   "INTEGER NOT NULL DEFAULT 0",
		"failure_started_at":              "TEXT",
		"account_status":                  "TEXT NOT NULL DEFAULT ''",
		"cooldown_observation_started_at": "TEXT",
		"cooldown_generation":             "TEXT",
		"cooldown_source_config_revision": "INTEGER",
	} {
		if existing[name] {
			continue
		}
		if _, err := conn.ExecContext(ctx, "ALTER TABLE account_health_current_state ADD COLUMN "+name+" "+definition); err != nil {
			return fmt.Errorf("扩展 account-health sqlite state 列 %s 失败: %w", name, err)
		}
	}
	return nil
}

func (s *Store) AcquireOwnerLease(ctx context.Context, ownerID string, duration time.Duration) (OwnerLease, bool, error) {
	if strings.TrimSpace(ownerID) == "" || duration <= 0 {
		return OwnerLease{}, false, errors.New("owner lease 参数无效")
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return OwnerLease{}, false, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	if s.mode == StorePostgres {
		var token int64
		err := s.db.QueryRowContext(ctx, `INSERT INTO juhe_jobs.account_health_owner_leases (lease_key, owner_id, fence_token, lease_until, updated_at)
VALUES ('account-health-owner', $1, 1, $2, $3)
ON CONFLICT (lease_key) DO UPDATE SET owner_id = EXCLUDED.owner_id, fence_token = juhe_jobs.account_health_owner_leases.fence_token + 1, lease_until = EXCLUDED.lease_until, updated_at = EXCLUDED.updated_at
WHERE juhe_jobs.account_health_owner_leases.lease_until <= $3
RETURNING fence_token`, ownerID, now.Add(duration), now).Scan(&token)
		if errors.Is(err, sql.ErrNoRows) {
			return OwnerLease{}, false, nil
		}
		if err != nil {
			return OwnerLease{}, false, err
		}
		return OwnerLease{OwnerID: ownerID, FenceToken: token}, true, nil
	}
	nowText := now.Format(time.RFC3339Nano)
	var token int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO account_health_owner_leases (lease_key, owner_id, fence_token, lease_until, updated_at)
VALUES ('account-health-owner', ?, 1, ?, ?)
ON CONFLICT (lease_key) DO UPDATE SET owner_id = excluded.owner_id, fence_token = account_health_owner_leases.fence_token + 1, lease_until = excluded.lease_until, updated_at = excluded.updated_at
WHERE account_health_owner_leases.lease_until <= ?
RETURNING fence_token`, ownerID, now.Add(duration).Format(time.RFC3339Nano), nowText, nowText).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerLease{}, false, nil
	}
	if err != nil {
		return OwnerLease{}, false, err
	}
	return OwnerLease{OwnerID: ownerID, FenceToken: token}, true, nil
}

func (s *Store) RenewOwnerLease(ctx context.Context, lease OwnerLease, duration time.Duration) (bool, error) {
	if duration <= 0 {
		return false, errors.New("owner lease 续约 duration 无效")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	var result sql.Result
	var err error
	if s.mode == StorePostgres {
		result, err = s.db.ExecContext(ctx, `UPDATE juhe_jobs.account_health_owner_leases SET lease_until=$1, updated_at=$2
WHERE lease_key='account-health-owner' AND owner_id=$3 AND fence_token=$4 AND lease_until > $2`, now.Add(duration), now, lease.OwnerID, lease.FenceToken)
	} else {
		nowText := now.Format(time.RFC3339Nano)
		result, err = s.db.ExecContext(ctx, `UPDATE account_health_owner_leases SET lease_until=?, updated_at=?
WHERE lease_key='account-health-owner' AND owner_id=? AND fence_token=? AND lease_until > ?`, now.Add(duration).Format(time.RFC3339Nano), nowText, lease.OwnerID, lease.FenceToken, nowText)
	}
	if err != nil {
		return false, err
	}
	updated, err := result.RowsAffected()
	return updated == 1, err
}

// ReleaseOwnerLease relinquishes only the exact owner/fence generation. A
// stale process cannot delete a lease acquired by its replacement.
func (s *Store) ReleaseOwnerLease(ctx context.Context, lease OwnerLease) error {
	if strings.TrimSpace(lease.OwnerID) == "" || lease.FenceToken < 1 {
		return errors.New("owner lease 释放参数无效")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var err error
	if s.mode == StorePostgres {
		_, err = s.db.ExecContext(ctx, `DELETE FROM juhe_jobs.account_health_owner_leases
WHERE lease_key='account-health-owner' AND owner_id=$1 AND fence_token=$2`, lease.OwnerID, lease.FenceToken)
	} else {
		_, err = s.db.ExecContext(ctx, `DELETE FROM account_health_owner_leases
WHERE lease_key='account-health-owner' AND owner_id=? AND fence_token=?`, lease.OwnerID, lease.FenceToken)
	}
	return err
}

// AppendOutcome makes the request ID idempotent and persists the current
// account state in the same owner-fenced transaction.
func (s *Store) AppendOutcome(ctx context.Context, lease OwnerLease, outcome Outcome) (bool, error) {
	if outcome.OutcomeID == "" || outcome.RequestID == "" || outcome.AccountID == "" || outcome.ObservedAt.IsZero() || outcome.InputVersion < 1 || outcome.ConfigRevision < 1 || outcome.DispatchRevision < 1 {
		return false, errors.New("outcome 缺少幂等或账户字段")
	}
	if err := validateOutcomeStateContract(outcome); err != nil {
		return false, err
	}
	// A projection is a conditional business-state command, not immutable
	// probe evidence. Persist it only after the jobs current-state CAS accepts
	// the same outcome; otherwise the durable row is audit/source-settlement
	// only and Node must not project a stale decision.
	storedOutcome := outcome
	if outcome.Projection != nil {
		storedOutcome.Projection = nil
	}
	payload, err := json.Marshal(storedOutcome)
	if err != nil {
		return false, fmt.Errorf("编码 account-health outcome 失败: %w", err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := s.verifyLease(ctx, tx, lease); err != nil {
		return false, err
	}
	var inserted bool
	if s.mode == StorePostgres {
		var id string
		err = tx.QueryRowContext(ctx, `INSERT INTO juhe_jobs.account_health_outcomes (outcome_id, request_id, account_id, outcome, observed_at, input_version, config_revision, dispatch_revision, status_code, error_code, error_message, payload)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
ON CONFLICT (request_id) DO NOTHING RETURNING outcome_id`, outcome.OutcomeID, outcome.RequestID, outcome.AccountID, outcome.Outcome, outcome.ObservedAt.UTC(), outcome.InputVersion, outcome.ConfigRevision, outcome.DispatchRevision, nullableStatus(outcome.StatusCode), nullableText(outcome.ErrorCode), nullableText(outcome.ErrorMessage), string(payload)).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return false, tx.Commit()
		}
		if err != nil {
			return false, err
		}
		inserted = true
		stateApplied, stateErr := s.writeCurrentStateTx(ctx, tx, outcome)
		err = stateErr
		if err == nil && stateApplied && outcome.Projection != nil {
			err = s.writeOutcomePayloadTx(ctx, tx, outcome)
		}
	} else {
		result, execErr := tx.ExecContext(ctx, `INSERT INTO account_health_outcomes (outcome_id, request_id, account_id, outcome, observed_at, input_version, config_revision, dispatch_revision, status_code, error_code, error_message, payload)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT (request_id) DO NOTHING`, outcome.OutcomeID, outcome.RequestID, outcome.AccountID, outcome.Outcome, outcome.ObservedAt.UTC().Format(time.RFC3339Nano), outcome.InputVersion, outcome.ConfigRevision, outcome.DispatchRevision, nullableStatus(outcome.StatusCode), nullableText(outcome.ErrorCode), nullableText(outcome.ErrorMessage), string(payload))
		if execErr != nil {
			return false, execErr
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return false, tx.Commit()
		}
		inserted = true
		stateApplied, stateErr := s.writeCurrentStateTx(ctx, tx, outcome)
		err = stateErr
		if err == nil && stateApplied && outcome.Projection != nil {
			err = s.writeOutcomePayloadTx(ctx, tx, outcome)
		}
	}
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return inserted, nil
}

func (s *Store) writeOutcomePayloadTx(ctx context.Context, tx *sql.Tx, outcome Outcome) error {
	payload, err := json.Marshal(outcome)
	if err != nil {
		return fmt.Errorf("编码可投影 account-health outcome 失败: %w", err)
	}
	if s.mode == StorePostgres {
		_, err = tx.ExecContext(ctx, `UPDATE juhe_jobs.account_health_outcomes SET payload=$1 WHERE outcome_id=$2`, string(payload), outcome.OutcomeID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE account_health_outcomes SET payload=? WHERE outcome_id=?`, string(payload), outcome.OutcomeID)
	}
	return err
}

// writeCurrentStateTx advances the jobs-owned scheduling state only when the
// immutable input generation still matches. A rejected state CAS deliberately
// leaves the immutable outcome row committed so operators can audit the late
// result without letting it change the current scheduling decision.
func (s *Store) writeCurrentStateTx(ctx context.Context, tx *sql.Tx, outcome Outcome) (bool, error) {
	if outcome.Outcome == OutcomeStale {
		// A stale explicit/source request is auditable but has no authority to
		// create a scheduling baseline. Only a current input task may establish
		// current state.
		return false, nil
	}
	if outcome.Projection != nil && outcome.Projection.ExpectedCooldownFence != nil {
		return s.updateCooldownCurrentStateTx(ctx, tx, outcome)
	}
	return s.upsertCurrentStateTx(ctx, tx, outcome)
}

func (s *Store) upsertCurrentStateTx(ctx context.Context, tx *sql.Tx, outcome Outcome) (bool, error) {
	args := currentStateArgs(outcome, s.mode)
	var query string
	if s.mode == StorePostgres {
		query = `INSERT INTO juhe_jobs.account_health_current_state (account_id, outcome_id, outcome, observed_at, input_version, config_revision, dispatch_revision, status_code, error_code, error_message, next_due_at, failure_count, failure_started_at, account_status, cooldown_observation_started_at, cooldown_generation, cooldown_source_config_revision, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$4)
ON CONFLICT (account_id) DO UPDATE SET outcome_id=EXCLUDED.outcome_id, outcome=EXCLUDED.outcome, observed_at=EXCLUDED.observed_at, input_version=EXCLUDED.input_version, config_revision=EXCLUDED.config_revision, dispatch_revision=EXCLUDED.dispatch_revision, status_code=EXCLUDED.status_code, error_code=EXCLUDED.error_code, error_message=EXCLUDED.error_message, next_due_at=EXCLUDED.next_due_at, failure_count=EXCLUDED.failure_count, failure_started_at=EXCLUDED.failure_started_at, account_status=EXCLUDED.account_status, cooldown_observation_started_at=EXCLUDED.cooldown_observation_started_at, cooldown_generation=EXCLUDED.cooldown_generation, cooldown_source_config_revision=EXCLUDED.cooldown_source_config_revision, updated_at=EXCLUDED.updated_at
WHERE (juhe_jobs.account_health_current_state.input_version < EXCLUDED.input_version
	   OR (juhe_jobs.account_health_current_state.input_version = EXCLUDED.input_version
       AND juhe_jobs.account_health_current_state.config_revision = EXCLUDED.config_revision
       AND juhe_jobs.account_health_current_state.dispatch_revision = EXCLUDED.dispatch_revision
	   AND juhe_jobs.account_health_current_state.observed_at <= EXCLUDED.observed_at)`
		query += `)`
		if outcome.Projection != nil {
			query += ` AND juhe_jobs.account_health_current_state.account_status = $18`
			args = append(args, outcome.Projection.ExpectedAccountStatus)
		}
	} else {
		query = `INSERT INTO account_health_current_state (account_id, outcome_id, outcome, observed_at, input_version, config_revision, dispatch_revision, status_code, error_code, error_message, next_due_at, failure_count, failure_started_at, account_status, cooldown_observation_started_at, cooldown_generation, cooldown_source_config_revision, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT (account_id) DO UPDATE SET outcome_id=excluded.outcome_id, outcome=excluded.outcome, observed_at=excluded.observed_at, input_version=excluded.input_version, config_revision=excluded.config_revision, dispatch_revision=excluded.dispatch_revision, status_code=excluded.status_code, error_code=excluded.error_code, error_message=excluded.error_message, next_due_at=excluded.next_due_at, failure_count=excluded.failure_count, failure_started_at=excluded.failure_started_at, account_status=excluded.account_status, cooldown_observation_started_at=excluded.cooldown_observation_started_at, cooldown_generation=excluded.cooldown_generation, cooldown_source_config_revision=excluded.cooldown_source_config_revision, updated_at=excluded.updated_at
WHERE (account_health_current_state.input_version < excluded.input_version
	   OR (account_health_current_state.input_version = excluded.input_version
       AND account_health_current_state.config_revision = excluded.config_revision
       AND account_health_current_state.dispatch_revision = excluded.dispatch_revision
	   AND account_health_current_state.observed_at <= excluded.observed_at)`
		query += `)`
		if outcome.Projection != nil {
			query += ` AND account_health_current_state.account_status = ?`
			args = append(args, outcome.Projection.ExpectedAccountStatus)
		}
	}
	return execCurrentStateCAS(ctx, tx, query, args...)
}

// Cooldown transitions require an already-recorded fence. They intentionally
// use UPDATE instead of UPSERT: a missing state cannot prove that the retest
// still owns the cooldown generation and is therefore outcome-only stale.
func (s *Store) updateCooldownCurrentStateTx(ctx context.Context, tx *sql.Tx, outcome Outcome) (bool, error) {
	fence := outcome.Projection.ExpectedCooldownFence
	if s.mode == StorePostgres {
		args := currentStateArgs(outcome, s.mode)
		query := `UPDATE juhe_jobs.account_health_current_state SET outcome_id=$2, outcome=$3, observed_at=$4, input_version=$5, config_revision=$6, dispatch_revision=$7, status_code=$8, error_code=$9, error_message=$10, next_due_at=$11, failure_count=$12, failure_started_at=$13, account_status=$14, cooldown_observation_started_at=$15, cooldown_generation=$16, cooldown_source_config_revision=$17, updated_at=$4
WHERE account_id=$1
  AND input_version=$5
  AND config_revision=$6
  AND dispatch_revision=$7
  AND observed_at <= $4
  AND account_status=$18
  AND cooldown_observation_started_at=$19
  AND cooldown_generation=$20
  AND cooldown_source_config_revision IS NOT DISTINCT FROM $21`
		args = append(args, outcome.Projection.ExpectedAccountStatus, fence.ObservationStartedAt.UTC(), fence.Generation, nullableInt64(fence.SourceConfigRevision))
		return execCurrentStateCAS(ctx, tx, query, args...)
	}
	query := `UPDATE account_health_current_state SET outcome_id=?, outcome=?, observed_at=?, input_version=?, config_revision=?, dispatch_revision=?, status_code=?, error_code=?, error_message=?, next_due_at=?, failure_count=?, failure_started_at=?, account_status=?, cooldown_observation_started_at=?, cooldown_generation=?, cooldown_source_config_revision=?, updated_at=?
WHERE account_id=?
  AND input_version=?
  AND config_revision=?
  AND dispatch_revision=?
  AND observed_at <= ?
  AND account_status=?
  AND cooldown_observation_started_at=?
	  AND cooldown_generation=?
	  AND cooldown_source_config_revision IS ?`
	args := append(currentStateUpdateArgs(outcome),
		outcome.ObservedAt.UTC().Format(time.RFC3339Nano),
		outcome.AccountID, outcome.InputVersion, outcome.ConfigRevision, outcome.DispatchRevision, outcome.ObservedAt.UTC().Format(time.RFC3339Nano),
		outcome.Projection.ExpectedAccountStatus, fence.ObservationStartedAt.UTC().Format(time.RFC3339Nano), fence.Generation, nullableInt64(fence.SourceConfigRevision),
	)
	return execCurrentStateCAS(ctx, tx, query, args...)
}

func execCurrentStateCAS(ctx context.Context, tx *sql.Tx, query string, args ...any) (bool, error) {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return count == 1, nil
}

func currentStateArgs(outcome Outcome, mode StoreMode) []any {
	observed := outcome.ObservedAt.UTC()
	if mode == StorePostgres {
		return []any{outcome.AccountID, outcome.OutcomeID, outcome.Outcome, observed, outcome.InputVersion, outcome.ConfigRevision, outcome.DispatchRevision, nullableStatus(outcome.StatusCode), nullableText(outcome.ErrorCode), nullableText(outcome.ErrorMessage), nullableTime(outcome.NextDueAt), outcome.FailureCount, nullableTime(outcome.FailureStartedAt), outcome.AccountStatus, cooldownObservation(outcome), cooldownGeneration(outcome), cooldownSourceRevision(outcome)}
	}
	return []any{outcome.AccountID, outcome.OutcomeID, outcome.Outcome, observed.Format(time.RFC3339Nano), outcome.InputVersion, outcome.ConfigRevision, outcome.DispatchRevision, nullableStatus(outcome.StatusCode), nullableText(outcome.ErrorCode), nullableText(outcome.ErrorMessage), nullableTimeText(outcome.NextDueAt), outcome.FailureCount, nullableTimeText(outcome.FailureStartedAt), outcome.AccountStatus, cooldownObservationText(outcome), cooldownGeneration(outcome), cooldownSourceRevision(outcome), observed.Format(time.RFC3339Nano)}
}

func currentStateUpdateArgs(outcome Outcome) []any {
	return []any{outcome.OutcomeID, outcome.Outcome, outcome.ObservedAt.UTC().Format(time.RFC3339Nano), outcome.InputVersion, outcome.ConfigRevision, outcome.DispatchRevision, nullableStatus(outcome.StatusCode), nullableText(outcome.ErrorCode), nullableText(outcome.ErrorMessage), nullableTimeText(outcome.NextDueAt), outcome.FailureCount, nullableTimeText(outcome.FailureStartedAt), outcome.AccountStatus, cooldownObservationText(outcome), cooldownGeneration(outcome), cooldownSourceRevision(outcome)}
}

func validateOutcomeStateContract(outcome Outcome) error {
	projection := outcome.Projection
	if projection == nil {
		return nil
	}
	if projection.TargetAccountID != outcome.AccountID || projection.InputVersion != outcome.InputVersion || projection.ConfigRevision != outcome.ConfigRevision || projection.DispatchRevision != outcome.DispatchRevision {
		return errors.New("outcome projection 与 current-state fence 不一致")
	}
	if strings.TrimSpace(projection.ExpectedAccountStatus) == "" {
		return errors.New("outcome projection 缺少 expected account status")
	}
	if projection.ExpectedCooldownFence != nil && !validStoredCooldownFence(projection.ExpectedCooldownFence) {
		return errors.New("outcome projection 的 expected cooldown fence 无效")
	}
	if projection.ExpectedCooldownFence != nil && !sameOptionalInt64(projection.ExpectedCooldownFence.SourceConfigRevision, projection.SourceRevision) {
		return errors.New("outcome projection 的 cooldown source revision 不一致")
	}
	if outcome.CooldownFence != nil && projection.CooldownFence != nil && !sameCooldownFence(outcome.CooldownFence, projection.CooldownFence) {
		return errors.New("outcome cooldown fence 与 projection 不一致")
	}
	if projection.TransitionKind == "cooldown_error" && projection.CooldownFence != nil && !sameCooldownFence(projection.ExpectedCooldownFence, projection.CooldownFence) {
		return errors.New("cooldown terminal 的输出 fence 与 expected fence 不一致")
	}
	return nil
}

func validStoredCooldownFence(fence *CooldownFence) bool {
	return fence != nil && !fence.ObservationStartedAt.IsZero() && strings.TrimSpace(fence.Generation) != ""
}

func sameCooldownFence(left, right *CooldownFence) bool {
	if left == nil || right == nil {
		return left == right
	}
	if !left.ObservationStartedAt.Equal(right.ObservationStartedAt) || left.Generation != right.Generation {
		return false
	}
	if left.SourceConfigRevision == nil || right.SourceConfigRevision == nil {
		return left.SourceConfigRevision == nil && right.SourceConfigRevision == nil
	}
	return *left.SourceConfigRevision == *right.SourceConfigRevision
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func sameOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *Store) LoadCurrentState(ctx context.Context, accountID string) (CurrentState, bool, error) {
	if strings.TrimSpace(accountID) == "" {
		return CurrentState{}, false, errors.New("current state 查询缺少 account ID")
	}
	var state CurrentState
	var statusCode sql.NullInt64
	var errorCode, errorMessage, accountStatus, cooldownGeneration sql.NullString
	var nextDue, failureStarted nullableDBTime
	var cooldownObserved nullableDBTime
	var cooldownSourceRevision sql.NullInt64
	var observed nullableDBTime
	var err error
	if s.mode == StorePostgres {
		err = s.db.QueryRowContext(ctx, `SELECT account_id,outcome_id,outcome,observed_at,input_version,config_revision,dispatch_revision,status_code,error_code,error_message,next_due_at,failure_count,failure_started_at,account_status,cooldown_observation_started_at,cooldown_generation,cooldown_source_config_revision FROM juhe_jobs.account_health_current_state WHERE account_id=$1`, accountID).Scan(&state.AccountID, &state.OutcomeID, &state.Outcome, &observed, &state.InputVersion, &state.ConfigRevision, &state.DispatchRevision, &statusCode, &errorCode, &errorMessage, &nextDue, &state.FailureCount, &failureStarted, &accountStatus, &cooldownObserved, &cooldownGeneration, &cooldownSourceRevision)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT account_id,outcome_id,outcome,observed_at,input_version,config_revision,dispatch_revision,status_code,error_code,error_message,next_due_at,failure_count,failure_started_at,account_status,cooldown_observation_started_at,cooldown_generation,cooldown_source_config_revision FROM account_health_current_state WHERE account_id=?`, accountID).Scan(&state.AccountID, &state.OutcomeID, &state.Outcome, &observed, &state.InputVersion, &state.ConfigRevision, &state.DispatchRevision, &statusCode, &errorCode, &errorMessage, &nextDue, &state.FailureCount, &failureStarted, &accountStatus, &cooldownObserved, &cooldownGeneration, &cooldownSourceRevision)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return CurrentState{}, false, nil
	}
	if err != nil {
		return CurrentState{}, false, err
	}
	state.ObservedAt = observed.Time
	if statusCode.Valid {
		state.StatusCode = int(statusCode.Int64)
	}
	state.ErrorCode = errorCode.String
	state.ErrorMessage = errorMessage.String
	state.AccountStatus = accountStatus.String
	if nextDue.Valid {
		value := nextDue.Time
		state.NextDueAt = &value
	}
	if failureStarted.Valid {
		value := failureStarted.Time
		state.FailureStartedAt = &value
	}
	if cooldownObserved.Valid && cooldownGeneration.Valid {
		fence := &CooldownFence{ObservationStartedAt: cooldownObserved.Time, Generation: cooldownGeneration.String}
		if cooldownSourceRevision.Valid {
			value := cooldownSourceRevision.Int64
			fence.SourceConfigRevision = &value
		}
		state.CooldownFence = fence
	}
	return state, true, nil
}

func (s *Store) HasRequest(ctx context.Context, requestID string) (bool, error) {
	if strings.TrimSpace(requestID) == "" {
		return false, errors.New("request ID 缺失")
	}
	var marker int
	var err error
	if s.mode == StorePostgres {
		err = s.db.QueryRowContext(ctx, `SELECT 1 FROM juhe_jobs.account_health_outcomes WHERE request_id=$1`, requestID).Scan(&marker)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT 1 FROM account_health_outcomes WHERE request_id=?`, requestID).Scan(&marker)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

type nullableDBTime struct {
	Time  time.Time
	Valid bool
}

func (value *nullableDBTime) Scan(source any) error {
	if source == nil {
		value.Time, value.Valid = time.Time{}, false
		return nil
	}
	switch text := source.(type) {
	case time.Time:
		value.Time, value.Valid = text.UTC(), true
		return nil
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return err
		}
		value.Time, value.Valid = parsed.UTC(), true
		return nil
	case []byte:
		return value.Scan(string(text))
	default:
		return fmt.Errorf("不支持的时间列类型 %T", source)
	}
}

func (s *Store) LoadKeyCursor(ctx context.Context, accountID, purpose, fingerprint string) (int, bool, error) {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(purpose) == "" || strings.TrimSpace(fingerprint) == "" {
		return 0, false, errors.New("key cursor 查询参数无效")
	}
	var index int
	var err error
	if s.mode == StorePostgres {
		err = s.db.QueryRowContext(ctx, `SELECT next_index FROM juhe_jobs.account_health_key_cursors WHERE account_id=$1 AND purpose=$2 AND key_set_fingerprint=$3`, accountID, purpose, fingerprint).Scan(&index)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT next_index FROM account_health_key_cursors WHERE account_id=? AND purpose=? AND key_set_fingerprint=?`, accountID, purpose, fingerprint).Scan(&index)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return index, err == nil, err
}

func (s *Store) SaveKeyCursor(ctx context.Context, lease OwnerLease, accountID, purpose, fingerprint string, nextIndex int) error {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(purpose) == "" || strings.TrimSpace(fingerprint) == "" || nextIndex < 0 {
		return errors.New("key cursor 写入参数无效")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.verifyLease(ctx, tx, lease); err != nil {
		return err
	}
	now := time.Now().UTC()
	if s.mode == StorePostgres {
		_, err = tx.ExecContext(ctx, `INSERT INTO juhe_jobs.account_health_key_cursors (account_id, purpose, key_set_fingerprint, next_index, updated_at) VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (account_id,purpose,key_set_fingerprint) DO UPDATE SET next_index=EXCLUDED.next_index, updated_at=EXCLUDED.updated_at`, accountID, purpose, fingerprint, nextIndex, now)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO account_health_key_cursors (account_id, purpose, key_set_fingerprint, next_index, updated_at) VALUES (?,?,?,?,?)
ON CONFLICT (account_id,purpose,key_set_fingerprint) DO UPDATE SET next_index=excluded.next_index, updated_at=excluded.updated_at`, accountID, purpose, fingerprint, nextIndex, now.Format(time.RFC3339Nano))
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) verifyLease(ctx context.Context, tx *sql.Tx, lease OwnerLease) error {
	if strings.TrimSpace(lease.OwnerID) == "" || lease.FenceToken <= 0 {
		return ErrOwnerLeaseLost
	}
	if s.mode == StorePostgres {
		var token int64
		err := tx.QueryRowContext(ctx, `SELECT fence_token FROM juhe_jobs.account_health_owner_leases WHERE lease_key='account-health-owner' AND owner_id=$1 AND fence_token=$2 AND lease_until > $3 FOR UPDATE`, lease.OwnerID, lease.FenceToken, time.Now().UTC()).Scan(&token)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOwnerLeaseLost
		}
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE account_health_owner_leases SET updated_at=updated_at WHERE lease_key='account-health-owner' AND owner_id=? AND fence_token=? AND lease_until > ?`, lease.OwnerID, lease.FenceToken, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrOwnerLeaseLost
	}
	return nil
}

func sqliteDSN(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	return (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "_pragma=busy_timeout(5000)"}).String(), nil
}

func nullableStatus(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullableTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC()
}

func nullableTimeText(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func outcomeCooldownFence(outcome Outcome) *CooldownFence {
	if outcome.CooldownFence != nil {
		return outcome.CooldownFence
	}
	if outcome.Projection != nil {
		return outcome.Projection.CooldownFence
	}
	return nil
}

func cooldownObservation(outcome Outcome) any {
	fence := outcomeCooldownFence(outcome)
	if fence == nil || fence.ObservationStartedAt.IsZero() || strings.TrimSpace(fence.Generation) == "" {
		return nil
	}
	return fence.ObservationStartedAt.UTC()
}

func cooldownObservationText(outcome Outcome) any {
	fence := outcomeCooldownFence(outcome)
	if fence == nil || fence.ObservationStartedAt.IsZero() || strings.TrimSpace(fence.Generation) == "" {
		return nil
	}
	return fence.ObservationStartedAt.UTC().Format(time.RFC3339Nano)
}

func cooldownGeneration(outcome Outcome) any {
	fence := outcomeCooldownFence(outcome)
	if fence == nil || fence.ObservationStartedAt.IsZero() || strings.TrimSpace(fence.Generation) == "" {
		return nil
	}
	return fence.Generation
}

func cooldownSourceRevision(outcome Outcome) any {
	fence := outcomeCooldownFence(outcome)
	if fence == nil || fence.ObservationStartedAt.IsZero() || strings.TrimSpace(fence.Generation) == "" || fence.SourceConfigRevision == nil {
		return nil
	}
	return *fence.SourceConfigRevision
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS account_health_owner_leases (lease_key TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token INTEGER NOT NULL, lease_until TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS account_health_outcomes (outcome_id TEXT PRIMARY KEY, request_id TEXT NOT NULL UNIQUE, account_id TEXT NOT NULL, outcome TEXT NOT NULL, observed_at TEXT NOT NULL, input_version INTEGER NOT NULL, config_revision INTEGER NOT NULL, dispatch_revision INTEGER NOT NULL, status_code INTEGER, error_code TEXT, error_message TEXT, payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS account_health_current_state (account_id TEXT PRIMARY KEY, outcome_id TEXT NOT NULL, outcome TEXT NOT NULL, observed_at TEXT NOT NULL, input_version INTEGER NOT NULL, config_revision INTEGER NOT NULL, dispatch_revision INTEGER NOT NULL, status_code INTEGER, error_code TEXT, error_message TEXT, next_due_at TEXT, failure_count INTEGER NOT NULL DEFAULT 0, failure_started_at TEXT, account_status TEXT NOT NULL DEFAULT '', cooldown_observation_started_at TEXT, cooldown_generation TEXT, cooldown_source_config_revision INTEGER, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS account_health_key_cursors (account_id TEXT NOT NULL, purpose TEXT NOT NULL, key_set_fingerprint TEXT NOT NULL, next_index INTEGER NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (account_id, purpose, key_set_fingerprint));
CREATE INDEX IF NOT EXISTS idx_account_health_outcomes_account_observed ON account_health_outcomes(account_id, observed_at DESC, outcome_id DESC);
CREATE INDEX IF NOT EXISTS idx_account_health_outcomes_observed ON account_health_outcomes(observed_at DESC, outcome_id DESC);
`

const postgresSchema = `
CREATE TABLE IF NOT EXISTS juhe_jobs.account_health_owner_leases (lease_key TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL, lease_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL);
CREATE TABLE IF NOT EXISTS juhe_jobs.account_health_outcomes (outcome_id TEXT PRIMARY KEY, request_id TEXT NOT NULL UNIQUE, account_id TEXT NOT NULL, outcome TEXT NOT NULL, observed_at TIMESTAMPTZ NOT NULL, input_version BIGINT NOT NULL, config_revision BIGINT NOT NULL, dispatch_revision BIGINT NOT NULL, status_code INTEGER, error_code TEXT, error_message TEXT, payload JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS juhe_jobs.account_health_current_state (account_id TEXT PRIMARY KEY, outcome_id TEXT NOT NULL, outcome TEXT NOT NULL, observed_at TIMESTAMPTZ NOT NULL, input_version BIGINT NOT NULL, config_revision BIGINT NOT NULL, dispatch_revision BIGINT NOT NULL, status_code INTEGER, error_code TEXT, error_message TEXT, next_due_at TIMESTAMPTZ, failure_count INTEGER NOT NULL DEFAULT 0, failure_started_at TIMESTAMPTZ, account_status TEXT NOT NULL DEFAULT '', cooldown_observation_started_at TIMESTAMPTZ, cooldown_generation TEXT, cooldown_source_config_revision BIGINT, updated_at TIMESTAMPTZ NOT NULL);
ALTER TABLE juhe_jobs.account_health_current_state ADD COLUMN IF NOT EXISTS next_due_at TIMESTAMPTZ;
ALTER TABLE juhe_jobs.account_health_current_state ADD COLUMN IF NOT EXISTS failure_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE juhe_jobs.account_health_current_state ADD COLUMN IF NOT EXISTS failure_started_at TIMESTAMPTZ;
ALTER TABLE juhe_jobs.account_health_current_state ADD COLUMN IF NOT EXISTS account_status TEXT NOT NULL DEFAULT '';
ALTER TABLE juhe_jobs.account_health_current_state ADD COLUMN IF NOT EXISTS cooldown_observation_started_at TIMESTAMPTZ;
ALTER TABLE juhe_jobs.account_health_current_state ADD COLUMN IF NOT EXISTS cooldown_generation TEXT;
ALTER TABLE juhe_jobs.account_health_current_state ADD COLUMN IF NOT EXISTS cooldown_source_config_revision BIGINT;
CREATE TABLE IF NOT EXISTS juhe_jobs.account_health_key_cursors (account_id TEXT NOT NULL, purpose TEXT NOT NULL, key_set_fingerprint TEXT NOT NULL, next_index INTEGER NOT NULL, updated_at TIMESTAMPTZ NOT NULL, PRIMARY KEY (account_id, purpose, key_set_fingerprint));
CREATE INDEX IF NOT EXISTS idx_account_health_outcomes_account_observed ON juhe_jobs.account_health_outcomes(account_id, observed_at DESC, outcome_id DESC);
CREATE INDEX IF NOT EXISTS idx_account_health_outcomes_observed ON juhe_jobs.account_health_outcomes(observed_at DESC, outcome_id DESC);
`
