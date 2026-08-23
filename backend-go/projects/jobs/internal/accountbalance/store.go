package accountbalance

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

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type StoreMode string

const (
	StoreSQLite   StoreMode = "sqlite"
	StorePostgres StoreMode = "postgres"
)

var (
	ErrOwnerLeaseLost   = errors.New("account-balance owner lease 已丢失")
	ErrAccountLeaseLost = errors.New("account-balance account lease 已丢失")
	ErrAccountLeaseHeld = errors.New("account-balance account lease 被其他活动 owner 持有")
	ErrOutcomeStale     = errors.New("account-balance outcome CAS 已过期")
	ErrSnapshotStale    = errors.New("account-balance snapshot CAS 已过期")
)

type StoreConfig struct {
	Mode                 StoreMode
	DatabasePath         string
	PostgresURL          string
	PostgresMaxOpenConns int
	PostgresMaxIdleConns int
	PostgresPool         *pgpool.Handle
}

type OwnerLease struct {
	OwnerID    string
	FenceToken int64
}

type AccountLease struct {
	AccountID  string
	OwnerID    string
	FenceToken int64
}

type Store struct {
	db          *sql.DB
	mode        StoreMode
	writeMu     sync.Mutex
	schemaMu    sync.Mutex
	schemaReady bool
	pool        *pgpool.Handle
}

func OpenStore(config StoreConfig) (*Store, error) {
	switch config.Mode {
	case StoreSQLite:
		path := strings.TrimSpace(config.DatabasePath)
		if path == "" {
			return nil, errors.New("account-balance sqlite 缺少数据库路径")
		}
		dsn, err := balanceSQLiteDSN(path)
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
			return nil, fmt.Errorf("配置 account-balance sqlite writer 失败: %w", err)
		}
		return &Store{db: db, mode: config.Mode}, nil
	case StorePostgres:
		if strings.TrimSpace(config.PostgresURL) == "" {
			return nil, errors.New("account-balance postgres 缺少连接 URL")
		}
		maxOpen := config.PostgresMaxOpenConns
		if maxOpen == 0 {
			maxOpen = 1000
		}
		maxIdle := config.PostgresMaxIdleConns
		if maxIdle == 0 {
			maxIdle = 1000
		}
		if maxOpen < 1 || maxIdle < 1 || maxIdle > maxOpen {
			return nil, fmt.Errorf("account-balance postgres max open/idle 必须满足 1 <= idle <= open，实际为 %d/%d", maxOpen, maxIdle)
		}
		var err error
		pool := config.PostgresPool
		if pool == nil {
			registry := pgpool.NewRegistry()
			pool, err = registry.Acquire("pgx", config.PostgresURL, "account-balance-store", maxOpen, maxIdle)
			if err != nil {
				return nil, err
			}
		}
		return &Store{db: pool.DB(), mode: config.Mode, pool: pool}, nil
	default:
		return nil, errors.New("account-balance store mode 必须为 sqlite 或 postgres")
	}
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if s.pool != nil {
		return s.pool.Close()
	}
	return s.db.Close()
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("account-balance store 未初始化")
	}
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	if s.schemaReady {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.mode == StorePostgres {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(918271447)"); err != nil {
			return err
		}
		if err := ensureBalancePGSchema(ctx, tx); err != nil {
			return err
		}
		for _, statement := range strings.Split(balancePostgresSchema, ";") {
			if statement = strings.TrimSpace(statement); statement != "" {
				if _, err := tx.ExecContext(ctx, statement); err != nil {
					return fmt.Errorf("初始化 account-balance postgres schema 失败: %w", err)
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE juhe_jobs.account_balance_outcomes ADD COLUMN IF NOT EXISTS committed BOOLEAN NOT NULL DEFAULT FALSE`); err != nil {
			return fmt.Errorf("迁移 account-balance postgres outcome committed 字段失败: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		s.schemaReady = true
		return nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("获取 account-balance sqlite schema 锁失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if _, err := conn.ExecContext(ctx, balanceSQLiteSchema); err != nil {
		return fmt.Errorf("初始化 account-balance sqlite schema 失败: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `SELECT committed FROM account_balance_outcomes LIMIT 0`); err != nil {
		if _, alterErr := conn.ExecContext(ctx, `ALTER TABLE account_balance_outcomes ADD COLUMN committed INTEGER NOT NULL DEFAULT 0`); alterErr != nil {
			return fmt.Errorf("迁移 account-balance sqlite outcome committed 字段失败: %w", alterErr)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	s.schemaReady = true
	return nil
}

func ensureBalancePGSchema(ctx context.Context, tx *sql.Tx) error {
	var owner, current string
	err := tx.QueryRowContext(ctx, `SELECT pg_get_userbyid(nspowner), current_user FROM pg_namespace WHERE nspname='juhe_jobs'`).Scan(&owner, &current)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("缺少外部 bootstrap 创建的 juhe_jobs schema")
	}
	if err != nil {
		return fmt.Errorf("读取 account-balance postgres schema owner 失败: %w", err)
	}
	if owner != current {
		return fmt.Errorf("juhe_jobs schema owner 必须是当前 jobs role: owner=%s current=%s", owner, current)
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
		err := s.db.QueryRowContext(ctx, `INSERT INTO juhe_jobs.account_balance_owner_leases (lease_key, owner_id, fence_token, lease_until, updated_at)
VALUES ('account-balance-owner',$1,1,$2,$3)
ON CONFLICT (lease_key) DO UPDATE SET owner_id=EXCLUDED.owner_id, fence_token=juhe_jobs.account_balance_owner_leases.fence_token+1, lease_until=EXCLUDED.lease_until, updated_at=EXCLUDED.updated_at
WHERE juhe_jobs.account_balance_owner_leases.lease_until <= $3
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
	err := s.db.QueryRowContext(ctx, `INSERT INTO account_balance_owner_leases (lease_key,owner_id,fence_token,lease_until,updated_at)
VALUES ('account-balance-owner',?,?,?,?)
ON CONFLICT (lease_key) DO UPDATE SET owner_id=excluded.owner_id, fence_token=account_balance_owner_leases.fence_token+1, lease_until=excluded.lease_until, updated_at=excluded.updated_at
WHERE account_balance_owner_leases.lease_until <= ?
RETURNING fence_token`, ownerID, 1, now.Add(duration).Format(time.RFC3339Nano), nowText, nowText).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerLease{}, false, nil
	}
	if err != nil {
		return OwnerLease{}, false, err
	}
	return OwnerLease{OwnerID: ownerID, FenceToken: token}, true, nil
}

func (s *Store) RenewOwnerLease(ctx context.Context, lease OwnerLease, duration time.Duration) (bool, error) {
	if lease.FenceToken < 1 || strings.TrimSpace(lease.OwnerID) == "" || duration <= 0 {
		return false, errors.New("owner lease 续约参数无效")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	var result sql.Result
	var err error
	if s.mode == StorePostgres {
		result, err = s.db.ExecContext(ctx, `UPDATE juhe_jobs.account_balance_owner_leases SET lease_until=$1,updated_at=$2 WHERE lease_key='account-balance-owner' AND owner_id=$3 AND fence_token=$4 AND lease_until>$2`, now.Add(duration), now, lease.OwnerID, lease.FenceToken)
	} else {
		text := now.Format(time.RFC3339Nano)
		result, err = s.db.ExecContext(ctx, `UPDATE account_balance_owner_leases SET lease_until=?,updated_at=? WHERE lease_key='account-balance-owner' AND owner_id=? AND fence_token=? AND lease_until>?`, now.Add(duration).Format(time.RFC3339Nano), text, lease.OwnerID, lease.FenceToken, text)
	}
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) ReleaseOwnerLease(ctx context.Context, lease OwnerLease) error {
	if lease.FenceToken < 1 || strings.TrimSpace(lease.OwnerID) == "" {
		return errors.New("owner lease 释放参数无效")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	if s.mode == StorePostgres {
		_, err := s.db.ExecContext(ctx, `UPDATE juhe_jobs.account_balance_owner_leases SET lease_until=$1,updated_at=$1 WHERE lease_key='account-balance-owner' AND owner_id=$2 AND fence_token=$3`, now, lease.OwnerID, lease.FenceToken)
		return err
	}
	text := now.Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `UPDATE account_balance_owner_leases SET lease_until=?,updated_at=? WHERE lease_key='account-balance-owner' AND owner_id=? AND fence_token=?`, text, text, lease.OwnerID, lease.FenceToken)
	return err
}

func (s *Store) AcquireAccountLease(ctx context.Context, owner OwnerLease, accountID string, duration time.Duration) (AccountLease, bool, error) {
	if owner.FenceToken < 1 || strings.TrimSpace(owner.OwnerID) == "" || strings.TrimSpace(accountID) == "" || duration <= 0 {
		return AccountLease{}, false, errors.New("account lease 参数无效")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AccountLease{}, false, err
	}
	defer tx.Rollback()
	if err := verifyOwnerTx(ctx, tx, s.mode, owner, time.Now().UTC()); err != nil {
		return AccountLease{}, false, err
	}
	now := time.Now().UTC()
	var token int64
	if s.mode == StorePostgres {
		err = tx.QueryRowContext(ctx, `INSERT INTO juhe_jobs.account_balance_account_leases(account_id,owner_id,fence_token,lease_until,updated_at)
VALUES($1,$2,1,$3,$4)
ON CONFLICT(account_id) DO UPDATE SET owner_id=EXCLUDED.owner_id,fence_token=juhe_jobs.account_balance_account_leases.fence_token+1,lease_until=EXCLUDED.lease_until,updated_at=EXCLUDED.updated_at
WHERE juhe_jobs.account_balance_account_leases.lease_until <= $4 OR NOT EXISTS (
  SELECT 1 FROM juhe_jobs.account_balance_owner_leases owner_lease
  WHERE owner_lease.lease_key='account-balance-owner'
    AND owner_lease.owner_id=juhe_jobs.account_balance_account_leases.owner_id
    AND owner_lease.lease_until>$4
)
RETURNING fence_token`, accountID, owner.OwnerID, now.Add(duration), now).Scan(&token)
	} else {
		text := now.Format(time.RFC3339Nano)
		err = tx.QueryRowContext(ctx, `INSERT INTO account_balance_account_leases(account_id,owner_id,fence_token,lease_until,updated_at)
VALUES(?,?,1,?,?)
ON CONFLICT(account_id) DO UPDATE SET owner_id=excluded.owner_id,fence_token=account_balance_account_leases.fence_token+1,lease_until=excluded.lease_until,updated_at=excluded.updated_at
WHERE account_balance_account_leases.lease_until <= ? OR NOT EXISTS (
  SELECT 1 FROM account_balance_owner_leases owner_lease
  WHERE owner_lease.lease_key='account-balance-owner'
    AND owner_lease.owner_id=account_balance_account_leases.owner_id
    AND owner_lease.lease_until>?
)
RETURNING fence_token`, accountID, owner.OwnerID, now.Add(duration).Format(time.RFC3339Nano), text, text, text).Scan(&token)
	}
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Commit()
		return AccountLease{}, false, ErrAccountLeaseHeld
	}
	if err != nil {
		return AccountLease{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return AccountLease{}, false, err
	}
	return AccountLease{AccountID: accountID, OwnerID: owner.OwnerID, FenceToken: token}, true, nil
}

func (s *Store) RenewAccountLease(ctx context.Context, owner OwnerLease, lease AccountLease, duration time.Duration) (bool, error) {
	if lease.FenceToken < 1 || lease.OwnerID != owner.OwnerID || lease.AccountID == "" || duration <= 0 {
		return false, errors.New("account lease 续约参数无效")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	if s.mode == StorePostgres {
		if err := verifyOwner(ctx, s.db, s.mode, owner, now); err != nil {
			return false, err
		}
		result, err := s.db.ExecContext(ctx, `UPDATE juhe_jobs.account_balance_account_leases SET lease_until=$1,updated_at=$2 WHERE account_id=$3 AND owner_id=$4 AND fence_token=$5 AND lease_until>$2`, now.Add(duration), now, lease.AccountID, lease.OwnerID, lease.FenceToken)
		if err != nil {
			return false, err
		}
		count, err := result.RowsAffected()
		return count == 1, err
	}
	text := now.Format(time.RFC3339Nano)
	if err := verifyOwner(ctx, s.db, s.mode, owner, now); err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE account_balance_account_leases SET lease_until=?,updated_at=? WHERE account_id=? AND owner_id=? AND fence_token=? AND lease_until>?`, now.Add(duration).Format(time.RFC3339Nano), text, lease.AccountID, lease.OwnerID, lease.FenceToken, text)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) ReleaseAccountLease(ctx context.Context, owner OwnerLease, lease AccountLease) error {
	if lease.FenceToken < 1 || lease.OwnerID != owner.OwnerID || lease.AccountID == "" {
		return errors.New("account lease 释放参数无效")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	if s.mode == StorePostgres {
		_, err := s.db.ExecContext(ctx, `UPDATE juhe_jobs.account_balance_account_leases SET lease_until=$1,updated_at=$1 WHERE account_id=$2 AND owner_id=$3 AND fence_token=$4`, now, lease.AccountID, lease.OwnerID, lease.FenceToken)
		return err
	}
	text := now.Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `UPDATE account_balance_account_leases SET lease_until=?,updated_at=? WHERE account_id=? AND owner_id=? AND fence_token=?`, text, text, lease.AccountID, lease.OwnerID, lease.FenceToken)
	return err
}

func (s *Store) LoadSnapshot(ctx context.Context, accountID string) (SnapshotRecord, bool, error) {
	if strings.TrimSpace(accountID) == "" {
		return SnapshotRecord{}, false, errors.New("account-balance snapshot account ID 不能为空")
	}
	var inputVersion, configRevision int64
	var trigger string
	var snapshotRaw, nextRefreshRaw, updatedRaw any
	query := `SELECT account_id,input_version,config_revision,trigger,snapshot_json,next_refresh_at,updated_at FROM account_balance_snapshots WHERE account_id=?`
	args := []any{accountID}
	if s.mode == StorePostgres {
		query = `SELECT account_id,input_version,config_revision,trigger,snapshot_json,next_refresh_at,updated_at FROM juhe_jobs.account_balance_snapshots WHERE account_id=$1`
	}
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&accountID, &inputVersion, &configRevision, &trigger, &snapshotRaw, &nextRefreshRaw, &updatedRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return SnapshotRecord{}, false, nil
	}
	if err != nil {
		return SnapshotRecord{}, false, err
	}
	var snapshot Snapshot
	snapshotJSON, err := balanceStringValue(snapshotRaw)
	if err != nil {
		return SnapshotRecord{}, false, err
	}
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
		return SnapshotRecord{}, false, fmt.Errorf("解析 account-balance snapshot 失败: %w", err)
	}
	updatedAt, err := balanceSQLTime(updatedRaw)
	if err != nil {
		return SnapshotRecord{}, false, errors.New("account-balance snapshot updated_at 无效")
	}
	return SnapshotRecord{AccountID: accountID, InputVersion: inputVersion, ConfigRevision: configRevision, Trigger: Trigger(trigger), Snapshot: snapshot, NextRefreshAt: parseBalanceNullableTime(nextRefreshRaw), UpdatedAt: updatedAt}, true, nil
}

// LoadOutcome reads one immutable jobs outcome by identity. It is used by the
// synchronous manual bridge so a concurrent later refresh cannot be returned
// as the result of the earlier request.
func (s *Store) LoadOutcome(ctx context.Context, outcomeID string) (Outcome, bool, error) {
	if strings.TrimSpace(outcomeID) == "" {
		return Outcome{}, false, errors.New("account-balance outcome ID 不能为空")
	}
	query := `SELECT payload FROM account_balance_outcomes WHERE outcome_id=?`
	if s.mode == StorePostgres {
		query = `SELECT payload FROM juhe_jobs.account_balance_outcomes WHERE outcome_id=$1`
	}
	var raw any
	if err := s.db.QueryRowContext(ctx, query, outcomeID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Outcome{}, false, nil
		}
		return Outcome{}, false, err
	}
	var payload []byte
	switch value := raw.(type) {
	case []byte:
		payload = value
	case string:
		payload = []byte(value)
	default:
		var marshalErr error
		payload, marshalErr = json.Marshal(value)
		if marshalErr != nil {
			return Outcome{}, false, errors.New("account-balance outcome payload 类型无效")
		}
	}
	var outcome Outcome
	if err := json.Unmarshal(payload, &outcome); err != nil {
		return Outcome{}, false, err
	}
	if outcome.OutcomeID != outcomeID {
		return Outcome{}, false, errors.New("account-balance outcome identity 不一致")
	}
	return outcome, true, nil
}

func (s *Store) WriteSnapshotCAS(ctx context.Context, owner OwnerLease, account AccountLease, mutation SnapshotMutation) (bool, error) {
	if err := mutation.Input.Validate(time.Now().UTC()); err != nil {
		return false, err
	}
	if account.AccountID != mutation.Input.AccountID || account.OwnerID != owner.OwnerID {
		return false, errors.New("account-balance snapshot lease 与 input 不匹配")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := verifyLeasesTx(ctx, tx, s.mode, owner, account, time.Now().UTC()); err != nil {
		return false, err
	}
	accepted, err := writeSnapshotTx(ctx, tx, s.mode, mutation.Input.AccountID, mutation.Input.InputVersion, mutation.Input.ConfigRevision, mutation.Input.Trigger, mutation.Snapshot, mutation.NextRefreshAt, mutation.ExpectedInput, mutation.ExpectedConfig, mutation.Input.NextRefreshAt)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return accepted, nil
}

// CASSnapshot is a concise alias used by callers that model the operation as
// a compare-and-swap rather than a write helper.
func (s *Store) CASSnapshot(ctx context.Context, owner OwnerLease, account AccountLease, mutation SnapshotMutation) (bool, error) {
	return s.WriteSnapshotCAS(ctx, owner, account, mutation)
}

func (s *Store) AppendOutcome(ctx context.Context, owner OwnerLease, account AccountLease, outcome Outcome) (bool, error) {
	if strings.TrimSpace(outcome.OutcomeID) == "" || strings.TrimSpace(outcome.RequestID) == "" || strings.TrimSpace(outcome.AccountID) == "" || outcome.InputVersion < 1 || outcome.ConfigRevision < 1 || outcome.ObservedAt.IsZero() {
		return false, errors.New("account-balance outcome 缺少幂等或 fence 字段")
	}
	if account.AccountID != outcome.AccountID || account.OwnerID != owner.OwnerID {
		return false, errors.New("account-balance outcome lease 与账户不匹配")
	}
	payload, err := json.Marshal(outcome)
	if err != nil {
		return false, fmt.Errorf("编码 account-balance outcome 失败: %w", err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := verifyLeasesTx(ctx, tx, s.mode, owner, account, time.Now().UTC()); err != nil {
		return false, err
	}
	inserted, err := insertBalanceOutcome(ctx, tx, s.mode, outcome, string(payload))
	if err != nil {
		return false, err
	}
	if !inserted {
		matches, committed, matchErr := existingOutcomeMatchesTx(ctx, tx, s.mode, outcome)
		if matchErr != nil {
			return false, matchErr
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		if !matches || !committed {
			return false, ErrOutcomeStale
		}
		return false, nil
	}
	accepted, err := writeSnapshotTx(ctx, tx, s.mode, outcome.AccountID, outcome.InputVersion, outcome.ConfigRevision, outcome.Trigger, outcome.Snapshot, outcome.NextRefreshAt, outcome.ExpectedSnapshotInput, outcome.ExpectedSnapshotConfig, outcome.ExpectedSnapshotNextRefreshAt)
	if err != nil {
		return false, err
	}
	if accepted {
		if err := markOutcomeCommittedTx(ctx, tx, s.mode, outcome.OutcomeID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if !accepted {
		return false, ErrOutcomeStale
	}
	return accepted, nil
}

// existingOutcomeMatchesTx verifies idempotency against the immutable outcome
// row, not the mutable current snapshot. A later successful refresh may move
// next_refresh_at, but it must not turn a retry of an already-committed manual
// request into a false stale conflict.
func existingOutcomeMatchesTx(ctx context.Context, tx *sql.Tx, mode StoreMode, outcome Outcome) (bool, bool, error) {
	query := `SELECT outcome_id,account_id,input_version,config_revision,trigger,committed FROM account_balance_outcomes WHERE request_id=?`
	if mode == StorePostgres {
		query = `SELECT outcome_id,account_id,input_version,config_revision,trigger,committed FROM juhe_jobs.account_balance_outcomes WHERE request_id=$1 FOR UPDATE`
	}
	var outcomeID, accountID, trigger string
	var inputVersion, configRevision int64
	var committedRaw any
	if err := tx.QueryRowContext(ctx, query, outcome.RequestID).Scan(&outcomeID, &accountID, &inputVersion, &configRevision, &trigger, &committedRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, false, nil
		}
		return false, false, err
	}
	committed, err := balanceSQLBool(committedRaw)
	if err != nil {
		return false, false, err
	}
	return outcomeID == outcome.OutcomeID && accountID == outcome.AccountID && inputVersion == outcome.InputVersion && configRevision == outcome.ConfigRevision && trigger == string(outcome.Trigger), committed, nil
}

func markOutcomeCommittedTx(ctx context.Context, tx *sql.Tx, mode StoreMode, outcomeID string) error {
	query := `UPDATE account_balance_outcomes SET committed=1 WHERE outcome_id=?`
	if mode == StorePostgres {
		query = `UPDATE juhe_jobs.account_balance_outcomes SET committed=TRUE WHERE outcome_id=$1`
	}
	result, err := tx.ExecContext(ctx, query, outcomeID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return err
		}
		return errors.New("account-balance outcome committed 标记缺失")
	}
	return nil
}

// CASOutcome preserves the explicit CAS terminology for integrations that do
// not use the idempotent AppendOutcome name.
func (s *Store) CASOutcome(ctx context.Context, owner OwnerLease, account AccountLease, outcome Outcome) (bool, error) {
	return s.AppendOutcome(ctx, owner, account, outcome)
}

func writeSnapshotTx(ctx context.Context, tx *sql.Tx, mode StoreMode, accountID string, inputVersion, configRevision int64, trigger Trigger, snapshot Snapshot, nextRefresh *time.Time, expectedInput, expectedConfig int64, expectedNextRefresh *time.Time) (bool, error) {
	var currentInput, currentConfig int64
	var currentNext any
	var exists bool
	selectSQL := `SELECT input_version,config_revision,next_refresh_at FROM account_balance_snapshots WHERE account_id=?`
	if mode == StorePostgres {
		selectSQL = `SELECT input_version,config_revision,next_refresh_at FROM juhe_jobs.account_balance_snapshots WHERE account_id=$1 FOR UPDATE`
	}
	err := tx.QueryRowContext(ctx, selectSQL, accountID).Scan(&currentInput, &currentConfig, &currentNext)
	if errors.Is(err, sql.ErrNoRows) {
		exists = false
	} else if err != nil {
		return false, err
	} else {
		exists = true
	}
	if !exists && (expectedInput > 0 || expectedConfig > 0) {
		return false, nil
	}
	if exists {
		if currentInput > inputVersion || currentConfig > configRevision || expectedInput > 0 && currentInput != expectedInput || expectedConfig > 0 && currentConfig != expectedConfig {
			return false, nil
		}
		if expectedNextRefresh != nil {
			current := parseBalanceNullableTime(currentNext)
			if current == nil || !current.Equal(expectedNextRefresh.UTC()) {
				return false, nil
			}
		}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return false, err
	}
	updated := time.Now().UTC().Format(time.RFC3339Nano)
	next := ""
	if nextRefresh != nil {
		next = nextRefresh.UTC().Format(time.RFC3339Nano)
	}
	if !exists {
		if mode == StorePostgres {
			_, err = tx.ExecContext(ctx, `INSERT INTO juhe_jobs.account_balance_snapshots(account_id,input_version,config_revision,trigger,snapshot_json,next_refresh_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, accountID, inputVersion, configRevision, string(trigger), string(encoded), nilIfEmpty(next), updated)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO account_balance_snapshots(account_id,input_version,config_revision,trigger,snapshot_json,next_refresh_at,updated_at) VALUES(?,?,?,?,?,?,?)`, accountID, inputVersion, configRevision, string(trigger), string(encoded), nilIfEmpty(next), updated)
		}
	} else if mode == StorePostgres {
		_, err = tx.ExecContext(ctx, `UPDATE juhe_jobs.account_balance_snapshots SET input_version=$1,config_revision=$2,trigger=$3,snapshot_json=$4,next_refresh_at=$5,updated_at=$6 WHERE account_id=$7`, inputVersion, configRevision, string(trigger), string(encoded), nilIfEmpty(next), updated, accountID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE account_balance_snapshots SET input_version=?,config_revision=?,trigger=?,snapshot_json=?,next_refresh_at=?,updated_at=? WHERE account_id=?`, inputVersion, configRevision, string(trigger), string(encoded), nilIfEmpty(next), updated, accountID)
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func insertBalanceOutcome(ctx context.Context, tx *sql.Tx, mode StoreMode, outcome Outcome, payload string) (bool, error) {
	if mode == StorePostgres {
		var id string
		err := tx.QueryRowContext(ctx, `INSERT INTO juhe_jobs.account_balance_outcomes(outcome_id,request_id,account_id,input_version,config_revision,trigger,observed_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(request_id) DO NOTHING RETURNING outcome_id`, outcome.OutcomeID, outcome.RequestID, outcome.AccountID, outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), outcome.ObservedAt.UTC(), payload).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil, err
	}
	var id string
	err := tx.QueryRowContext(ctx, `INSERT INTO account_balance_outcomes(outcome_id,request_id,account_id,input_version,config_revision,trigger,observed_at,payload) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(request_id) DO NOTHING RETURNING outcome_id`, outcome.OutcomeID, outcome.RequestID, outcome.AccountID, outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), outcome.ObservedAt.UTC().Format(time.RFC3339Nano), payload).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func verifyOwnerTx(ctx context.Context, tx *sql.Tx, mode StoreMode, lease OwnerLease, now time.Time) error {
	var owner string
	var token int64
	var until any
	query := `SELECT owner_id,fence_token,lease_until FROM account_balance_owner_leases WHERE lease_key=?`
	if mode == StorePostgres {
		query = `SELECT owner_id,fence_token,lease_until FROM juhe_jobs.account_balance_owner_leases WHERE lease_key=$1 FOR UPDATE`
	}
	if err := tx.QueryRowContext(ctx, query, "account-balance-owner").Scan(&owner, &token, &until); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOwnerLeaseLost
		}
		return err
	}
	leaseUntil, err := balanceSQLTime(until)
	if err != nil || owner != lease.OwnerID || token != lease.FenceToken || !leaseUntil.After(now) {
		return ErrOwnerLeaseLost
	}
	return nil
}

func verifyLeasesTx(ctx context.Context, tx *sql.Tx, mode StoreMode, owner OwnerLease, account AccountLease, now time.Time) error {
	if err := verifyOwnerTx(ctx, tx, mode, owner, now); err != nil {
		return err
	}
	var ownerID string
	var token int64
	var until any
	query := `SELECT owner_id,fence_token,lease_until FROM account_balance_account_leases WHERE account_id=?`
	if mode == StorePostgres {
		query = `SELECT owner_id,fence_token,lease_until FROM juhe_jobs.account_balance_account_leases WHERE account_id=$1 FOR UPDATE`
	}
	if err := tx.QueryRowContext(ctx, query, account.AccountID).Scan(&ownerID, &token, &until); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAccountLeaseLost
		}
		return err
	}
	leaseUntil, err := balanceSQLTime(until)
	if err != nil || ownerID != owner.OwnerID || token != account.FenceToken || !leaseUntil.After(now) {
		return ErrAccountLeaseLost
	}
	return nil
}

func verifyOwner(ctx context.Context, db *sql.DB, mode StoreMode, lease OwnerLease, now time.Time) error {
	query := `SELECT owner_id,fence_token,lease_until FROM account_balance_owner_leases WHERE lease_key=?`
	arg := any("account-balance-owner")
	if mode == StorePostgres {
		query = `SELECT owner_id,fence_token,lease_until FROM juhe_jobs.account_balance_owner_leases WHERE lease_key=$1`
	}
	var owner string
	var token int64
	var until any
	if err := db.QueryRowContext(ctx, query, arg).Scan(&owner, &token, &until); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOwnerLeaseLost
		}
		return err
	}
	parsed, err := balanceSQLTime(until)
	if err != nil || owner != lease.OwnerID || token != lease.FenceToken || !parsed.After(now) {
		return ErrOwnerLeaseLost
	}
	return nil
}

func balanceSQLTime(value any) (time.Time, error) {
	switch parsed := value.(type) {
	case time.Time:
		return parsed.UTC(), nil
	case string:
		return time.Parse(time.RFC3339Nano, parsed)
	case []byte:
		return time.Parse(time.RFC3339Nano, string(parsed))
	default:
		return time.Time{}, errors.New("未知 SQL 时间类型")
	}
}

func balanceSQLBool(value any) (bool, error) {
	switch parsed := value.(type) {
	case bool:
		return parsed, nil
	case int64:
		return parsed == 1, nil
	case int:
		return parsed == 1, nil
	case []byte:
		return balanceSQLBool(string(parsed))
	case string:
		if parsed == "1" || strings.EqualFold(parsed, "true") {
			return true, nil
		}
		if parsed == "0" || strings.EqualFold(parsed, "false") {
			return false, nil
		}
	}
	return false, errors.New("未知 SQL 布尔类型")
}

func parseBalanceNullableTime(value any) *time.Time {
	if value == nil {
		return nil
	}
	parsed, err := balanceSQLTime(value)
	if err != nil {
		return nil
	}
	return &parsed
}

func balanceStringValue(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case []byte:
		return string(typed), nil
	default:
		return "", errors.New("未知 SQL 文本类型")
	}
}

func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func balanceSQLiteDSN(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("解析 account-balance sqlite 路径失败: %w", err)
	}
	uriPath := filepath.ToSlash(absolute)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	// Every database/sql transaction starts with SQLite BEGIN IMMEDIATE.  The
	// owner/account fence is therefore serialized across processes before the
	// lease row is verified, not merely protected by this process's writeMu.
	return (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "_txlock=immediate&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"}).String(), nil
}

const balanceSQLiteSchema = `
CREATE TABLE IF NOT EXISTS account_balance_owner_leases (
 lease_key TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token INTEGER NOT NULL, lease_until TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS account_balance_account_leases (
 account_id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token INTEGER NOT NULL, lease_until TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS account_balance_snapshots (
 account_id TEXT PRIMARY KEY, input_version INTEGER NOT NULL, config_revision INTEGER NOT NULL, trigger TEXT NOT NULL, snapshot_json TEXT NOT NULL, next_refresh_at TEXT, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS account_balance_outcomes (
 outcome_id TEXT PRIMARY KEY, request_id TEXT NOT NULL UNIQUE, account_id TEXT NOT NULL, input_version INTEGER NOT NULL, config_revision INTEGER NOT NULL, trigger TEXT NOT NULL, observed_at TEXT NOT NULL, payload TEXT NOT NULL, committed INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_account_balance_outcomes_account ON account_balance_outcomes(account_id, observed_at);
`

const balancePostgresSchema = `
CREATE TABLE IF NOT EXISTS juhe_jobs.account_balance_owner_leases (
 lease_key TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL, lease_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_jobs.account_balance_account_leases (
 account_id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL, lease_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_jobs.account_balance_snapshots (
 account_id TEXT PRIMARY KEY, input_version BIGINT NOT NULL, config_revision BIGINT NOT NULL, trigger TEXT NOT NULL, snapshot_json JSONB NOT NULL, next_refresh_at TIMESTAMPTZ, updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_jobs.account_balance_outcomes (
 outcome_id TEXT PRIMARY KEY, request_id TEXT NOT NULL UNIQUE, account_id TEXT NOT NULL, input_version BIGINT NOT NULL, config_revision BIGINT NOT NULL, trigger TEXT NOT NULL, observed_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, committed BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE INDEX IF NOT EXISTS idx_account_balance_outcomes_account ON juhe_jobs.account_balance_outcomes(account_id, observed_at);
`
