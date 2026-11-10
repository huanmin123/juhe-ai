package accountbalance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
	stdlib "github.com/jackc/pgx/v5/stdlib"
)

type StoreMode string

const (
	StorePostgres StoreMode = "postgres"
)

// owner lease 的两个 key 空间（docs/functions/AI账户上游余额查询设计.md §5
// 租约互斥模型）。周期/首探批次共用全局单行：一轮只允许一个执行端持锁扫描，
// fence 世代防止旧批次写回。手动刷新按账户领取独立行（前缀+accountID）：
// 全局行会被周期批次几乎连续持有，手动刷新若争抢同一行会在互斥粒度上被整批
// 拖住（生产 104 账户场景高概率秒拒 409），下沉账户级后不同账户完全并行，
// 同账户互斥由 account_balance_account_leases 承担。
const (
	OwnerLeaseKeyPeriodic     = "account-balance-owner"
	ManualOwnerLeaseKeyPrefix = "account-balance-owner-manual:"
)

// PoolHandle is the minimal pool lifecycle seam shared by this package and
// its hosts. Both the gateway and jobs pgpool handles satisfy it (they alias
// *sqlpool.Handle); a nil pool makes OpenStore open a direct pgx connection
// from PostgresURL instead.
type PoolHandle interface {
	DB() *sql.DB
	Close() error
}

var (
	ErrOwnerLeaseLost   = errors.New("account-balance owner lease 已丢失")
	ErrAccountLeaseLost = errors.New("account-balance account lease 已丢失")
	ErrAccountLeaseHeld = errors.New("account-balance account lease 被其他活动 owner 持有")
	ErrOutcomeStale     = errors.New("account-balance outcome CAS 已过期")
	ErrSnapshotStale    = errors.New("account-balance snapshot CAS 已过期")
)

type StoreConfig struct {
	Mode                 StoreMode
	PostgresURL          string
	PostgresMaxOpenConns int
	PostgresMaxIdleConns int
	PostgresPool         PoolHandle
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
	db            *sql.DB
	writeMu       sync.Mutex
	schemaMu      sync.Mutex
	schemaCheckMu sync.Mutex
	schemaReady   bool
	pool          PoolHandle
}

func OpenStore(config StoreConfig) (*Store, error) {
	if config.Mode != StorePostgres {
		return nil, errors.New("account-balance store mode 必须为 postgres")
	}
	pool := config.PostgresPool
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
	if pool == nil {
		// No injected handle: open a dedicated pgx pool. Hosts that own a
		// shared registry (gateway/jobs pgpool) inject PoolHandle instead.
		if strings.TrimSpace(config.PostgresURL) == "" {
			return nil, errors.New("account-balance postgres 缺少连接 URL")
		}
		// 经 sqldialect 改写 driver 打开（清理批次 C5 统一裸开路径）：
		// 与 sql.Open("pgx", url) 同一 driver 实例、同一惰性语义
		// （openErr 恒为 nil，w12h 授权删除死守卫）；本包 PG 臂 SQL
		// 已是 `$n`，改写层幂等透传，并对未来遗漏改写的 `?` SQL
		// 提供驱动层兜底。
		db, _ := sqldialect.OpenDB(stdlib.GetDefaultDriver(), config.PostgresURL)
		db.SetMaxOpenConns(maxOpen)
		db.SetMaxIdleConns(maxIdle)
		return &Store{db: db}, nil
	}
	return &Store{db: pool.DB(), pool: pool}, nil
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(918271447)"); err != nil {
		return err
	}
	if err := applyBalancePGSchemaDDL(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.schemaReady = true
	return nil
}

// applyBalancePGSchemaDDL applies the J2 account-balance four-table PostgreSQL
// DDL inside an existing transaction: it first re-asserts the externally
// provisioned schema contract via ensureBalancePGSchema (juhe_jobs must exist
// and be owned by the current role; a missing schema fails closed and is never
// created here), then executes every statement of balancePostgresSchema (all
// IF NOT EXISTS, so the sequence is idempotent) and finally migrates the
// outcomes.committed column. The transaction boundary and the advisory lock
// stay with the caller: EnsureSchema (isolated tests) and the maintenance
// one-shot bootstrap (BootstrapPostgres) share this helper under the same
// lock, so concurrent callers serialize instead of racing.
func applyBalancePGSchemaDDL(ctx context.Context, tx *sql.Tx) error {
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
	return nil
}

// CheckSchema verifies the externally provisioned PostgreSQL contract without
// issuing DDL. Production J2 startup must fail closed when migrations are
// missing; EnsureSchema (real DDL) is reserved for isolated tests and the
// maintenance one-shot bootstrap.
func (s *Store) CheckSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("account-balance store 未初始化")
	}
	s.schemaCheckMu.Lock()
	defer s.schemaCheckMu.Unlock()
	return s.checkPostgresSchema(ctx)
}

func (s *Store) checkPostgresSchema(ctx context.Context) error {
	s.schemaMu.Lock()
	s.schemaReady = false
	s.schemaMu.Unlock()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return fmt.Errorf("检查 account-balance postgres schema 事务失败: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL TRANSACTION READ ONLY"); err != nil {
		return err
	}
	if err := ensureBalancePGSchema(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema='juhe_jobs' AND table_name = ANY($1)`, []string{
		"account_balance_owner_leases", "account_balance_account_leases", "account_balance_snapshots", "account_balance_outcomes",
	})
	if err != nil {
		return fmt.Errorf("读取 account-balance postgres tables 失败: %w", err)
	}
	seen := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		seen[name] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	missing := make([]string, 0, 4)
	for _, name := range []string{"account_balance_owner_leases", "account_balance_account_leases", "account_balance_snapshots", "account_balance_outcomes"} {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("J2 PostgreSQL schema 缺少预置表: %s", strings.Join(missing, ", "))
	}
	if err := checkBalancePGColumns(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.schemaMu.Lock()
	s.schemaReady = true
	s.schemaMu.Unlock()
	return nil
}

func checkBalancePGColumns(ctx context.Context, tx *sql.Tx) error {
	expected := map[string]map[string]string{
		"account_balance_owner_leases":   {"lease_key": "text", "owner_id": "text", "fence_token": "int8", "lease_until": "timestamptz", "updated_at": "timestamptz"},
		"account_balance_account_leases": {"account_id": "text", "owner_id": "text", "fence_token": "int8", "lease_until": "timestamptz", "updated_at": "timestamptz"},
		"account_balance_snapshots":      {"account_id": "text", "input_version": "int8", "config_revision": "int8", "trigger": "text", "snapshot_json": "jsonb", "next_refresh_at": "timestamptz", "updated_at": "timestamptz"},
		"account_balance_outcomes":       {"outcome_id": "text", "request_id": "text", "account_id": "text", "input_version": "int8", "config_revision": "int8", "trigger": "text", "observed_at": "timestamptz", "payload": "jsonb", "committed": "bool"},
	}
	rows, err := tx.QueryContext(ctx, `SELECT table_name,column_name,udt_name,is_nullable FROM information_schema.columns WHERE table_schema='juhe_jobs' AND table_name = ANY($1)`, []string{"account_balance_owner_leases", "account_balance_account_leases", "account_balance_snapshots", "account_balance_outcomes"})
	if err != nil {
		return fmt.Errorf("读取 J2 PostgreSQL 列契约失败: %w", err)
	}
	defer rows.Close()
	seen := map[string]map[string]string{}
	for rows.Next() {
		var table, column, udt, nullable string
		if err := rows.Scan(&table, &column, &udt, &nullable); err != nil {
			return err
		}
		if nullable == "YES" && column != "next_refresh_at" {
			return fmt.Errorf("J2 PostgreSQL 列 %s.%s 必须为 NOT NULL", table, column)
		}
		if seen[table] == nil {
			seen[table] = map[string]string{}
		}
		seen[table][column] = udt
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for table, columns := range expected {
		for column, udt := range columns {
			if got, ok := seen[table][column]; !ok {
				return fmt.Errorf("J2 PostgreSQL 缺少列 %s.%s", table, column)
			} else if got != udt {
				return fmt.Errorf("J2 PostgreSQL 列 %s.%s 类型不符: got=%s want=%s", table, column, got, udt)
			}
		}
	}
	return nil
}

// ensureRuntimeSchema is the production entrypoint. PostgreSQL is always
// checked read-only; DDL issuance stays with EnsureSchema (isolated tests and
// the maintenance one-shot bootstrap).
func (s *Store) ensureRuntimeSchema(ctx context.Context) error {
	s.schemaMu.Lock()
	ready := s.schemaReady
	s.schemaMu.Unlock()
	if ready {
		return nil
	}
	s.schemaCheckMu.Lock()
	defer s.schemaCheckMu.Unlock()
	s.schemaMu.Lock()
	ready = s.schemaReady
	s.schemaMu.Unlock()
	if ready {
		return nil
	}
	return s.checkPostgresSchema(ctx)
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

func (s *Store) AcquireOwnerLease(ctx context.Context, leaseKey, ownerID string, duration time.Duration) (OwnerLease, bool, error) {
	if strings.TrimSpace(leaseKey) == "" || strings.TrimSpace(ownerID) == "" || duration <= 0 {
		return OwnerLease{}, false, errors.New("owner lease 参数无效")
	}
	if err := s.ensureRuntimeSchema(ctx); err != nil {
		return OwnerLease{}, false, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	var token int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO juhe_jobs.account_balance_owner_leases (lease_key, owner_id, fence_token, lease_until, updated_at)
VALUES ($1,$2,1,$3,$4)
ON CONFLICT (lease_key) DO UPDATE SET owner_id=EXCLUDED.owner_id, fence_token=juhe_jobs.account_balance_owner_leases.fence_token+1, lease_until=EXCLUDED.lease_until, updated_at=EXCLUDED.updated_at
WHERE juhe_jobs.account_balance_owner_leases.lease_until <= $4
RETURNING fence_token`, leaseKey, ownerID, now.Add(duration), now).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerLease{}, false, nil
	}
	if err != nil {
		return OwnerLease{}, false, err
	}
	return OwnerLease{OwnerID: ownerID, FenceToken: token}, true, nil
}

func (s *Store) RenewOwnerLease(ctx context.Context, leaseKey string, lease OwnerLease, duration time.Duration) (bool, error) {
	if strings.TrimSpace(leaseKey) == "" || lease.FenceToken < 1 || strings.TrimSpace(lease.OwnerID) == "" || duration <= 0 {
		return false, errors.New("owner lease 续约参数无效")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE juhe_jobs.account_balance_owner_leases SET lease_until=$1,updated_at=$2 WHERE lease_key=$3 AND owner_id=$4 AND fence_token=$5 AND lease_until>$2`, now.Add(duration), now, leaseKey, lease.OwnerID, lease.FenceToken)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) ReleaseOwnerLease(ctx context.Context, leaseKey string, lease OwnerLease) error {
	if strings.TrimSpace(leaseKey) == "" || lease.FenceToken < 1 || strings.TrimSpace(lease.OwnerID) == "" {
		return errors.New("owner lease 释放参数无效")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE juhe_jobs.account_balance_owner_leases SET lease_until=$1,updated_at=$1 WHERE lease_key=$2 AND owner_id=$3 AND fence_token=$4`, now, leaseKey, lease.OwnerID, lease.FenceToken)
	return err
}

func (s *Store) AcquireAccountLease(ctx context.Context, leaseKey string, owner OwnerLease, accountID string, duration time.Duration) (AccountLease, bool, error) {
	if strings.TrimSpace(leaseKey) == "" || owner.FenceToken < 1 || strings.TrimSpace(owner.OwnerID) == "" || strings.TrimSpace(accountID) == "" || duration <= 0 {
		return AccountLease{}, false, errors.New("account lease 参数无效")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AccountLease{}, false, err
	}
	defer tx.Rollback()
	// verifyOwnerTx 校验的是当前调用者自己在 leaseKey 空间的 owner lease 有效性，
	// 与下方接管条件无关：跨 key 空间（周期持全局行、manual 按账户行）也能公平
	// 竞争账户租约。
	if err := verifyOwnerTx(ctx, tx, leaseKey, owner, time.Now().UTC()); err != nil {
		return AccountLease{}, false, err
	}
	now := time.Now().UTC()
	var token int64
	// 接管只看 lease_until 过期（fence+1 后旧持有者携旧 fence 的写入在
	// verifyLeasesTx 必被拒——fence 世代是唯一拦截机制，返回
	// ErrAccountLeaseLost；ErrOutcomeStale 是 outcome request_id 冲突语义，
	// 与 fence 无关）。旧「或原持有者 owner lease 失效即接管」分支必须删除：
	// 它按硬编码全局 key 查原持有者的 owner 行，manual 持有者的 owner 行在
	// manual:<accountID> key 空间、在该全局行中恒不存在，分支会放行任何调用
	// 者在其最长 15s 上游探测中途立即抢锁，跨 key 空间互斥失效；删除后接管
	// 时点恒为账户租约自身 lease_until（与旧条件的 OR 左臂一致），也不再抢
	// 「owner 行已过期/不存在但账户租约仍存活」的合法持有者（2026-11-01
	// 手动刷新互斥粒度下沉，设计 docs/functions/AI账户上游余额查询设计.md §5）。
	err = tx.QueryRowContext(ctx, `INSERT INTO juhe_jobs.account_balance_account_leases(account_id,owner_id,fence_token,lease_until,updated_at)
VALUES($1,$2,1,$3,$4)
ON CONFLICT(account_id) DO UPDATE SET owner_id=EXCLUDED.owner_id,fence_token=juhe_jobs.account_balance_account_leases.fence_token+1,lease_until=EXCLUDED.lease_until,updated_at=EXCLUDED.updated_at
WHERE juhe_jobs.account_balance_account_leases.lease_until <= $4
RETURNING fence_token`, accountID, owner.OwnerID, now.Add(duration), now).Scan(&token)
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

func (s *Store) RenewAccountLease(ctx context.Context, leaseKey string, owner OwnerLease, lease AccountLease, duration time.Duration) (bool, error) {
	if strings.TrimSpace(leaseKey) == "" || lease.FenceToken < 1 || lease.OwnerID != owner.OwnerID || lease.AccountID == "" || duration <= 0 {
		return false, errors.New("account lease 续约参数无效")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	if err := verifyOwner(ctx, s.db, leaseKey, owner, now); err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE juhe_jobs.account_balance_account_leases SET lease_until=$1,updated_at=$2 WHERE account_id=$3 AND owner_id=$4 AND fence_token=$5 AND lease_until>$2`, now.Add(duration), now, lease.AccountID, lease.OwnerID, lease.FenceToken)
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
	_, err := s.db.ExecContext(ctx, `UPDATE juhe_jobs.account_balance_account_leases SET lease_until=$1,updated_at=$1 WHERE account_id=$2 AND owner_id=$3 AND fence_token=$4`, now, lease.AccountID, lease.OwnerID, lease.FenceToken)
	return err
}

func (s *Store) LoadSnapshot(ctx context.Context, accountID string) (SnapshotRecord, bool, error) {
	if strings.TrimSpace(accountID) == "" {
		return SnapshotRecord{}, false, errors.New("account-balance snapshot account ID 不能为空")
	}
	var inputVersion, configRevision int64
	var trigger string
	var snapshotRaw, nextRefreshRaw, updatedRaw any
	query := `SELECT account_id,input_version,config_revision,trigger,snapshot_json,next_refresh_at,updated_at FROM juhe_jobs.account_balance_snapshots WHERE account_id=$1`
	err := s.db.QueryRowContext(ctx, query, accountID).Scan(&accountID, &inputVersion, &configRevision, &trigger, &snapshotRaw, &nextRefreshRaw, &updatedRaw)
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
	var raw any
	if err := s.db.QueryRowContext(ctx, `SELECT payload FROM juhe_jobs.account_balance_outcomes WHERE outcome_id=$1`, outcomeID).Scan(&raw); err != nil {
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

func (s *Store) WriteSnapshotCAS(ctx context.Context, leaseKey string, owner OwnerLease, account AccountLease, mutation SnapshotMutation) (bool, error) {
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
	if err := verifyLeasesTx(ctx, tx, leaseKey, owner, account, time.Now().UTC()); err != nil {
		return false, err
	}
	accepted, err := writeSnapshotTx(ctx, tx, mutation.Input.AccountID, mutation.Input.InputVersion, mutation.Input.ConfigRevision, mutation.Input.Trigger, mutation.Snapshot, mutation.NextRefreshAt, mutation.ExpectedInput, mutation.ExpectedConfig, mutation.Input.NextRefreshAt)
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
func (s *Store) CASSnapshot(ctx context.Context, leaseKey string, owner OwnerLease, account AccountLease, mutation SnapshotMutation) (bool, error) {
	return s.WriteSnapshotCAS(ctx, leaseKey, owner, account, mutation)
}

func (s *Store) AppendOutcome(ctx context.Context, leaseKey string, owner OwnerLease, account AccountLease, outcome Outcome) (bool, error) {
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
	if err := verifyLeasesTx(ctx, tx, leaseKey, owner, account, time.Now().UTC()); err != nil {
		return false, err
	}
	inserted, err := insertBalanceOutcome(ctx, tx, outcome, string(payload))
	if err != nil {
		return false, err
	}
	if !inserted {
		matches, committed, matchErr := existingOutcomeMatchesTx(ctx, tx, outcome)
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
	accepted, err := writeSnapshotTx(ctx, tx, outcome.AccountID, outcome.InputVersion, outcome.ConfigRevision, outcome.Trigger, outcome.Snapshot, outcome.NextRefreshAt, outcome.ExpectedSnapshotInput, outcome.ExpectedSnapshotConfig, outcome.ExpectedSnapshotNextRefreshAt)
	if err != nil {
		return false, err
	}
	if accepted {
		if err := markOutcomeCommittedTx(ctx, tx, outcome.OutcomeID); err != nil {
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
func existingOutcomeMatchesTx(ctx context.Context, tx *sql.Tx, outcome Outcome) (bool, bool, error) {
	var outcomeID, accountID, trigger string
	var inputVersion, configRevision int64
	var committedRaw any
	if err := tx.QueryRowContext(ctx, `SELECT outcome_id,account_id,input_version,config_revision,trigger,committed FROM juhe_jobs.account_balance_outcomes WHERE request_id=$1 FOR UPDATE`, outcome.RequestID).Scan(&outcomeID, &accountID, &inputVersion, &configRevision, &trigger, &committedRaw); err != nil {
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

func markOutcomeCommittedTx(ctx context.Context, tx *sql.Tx, outcomeID string) error {
	result, err := tx.ExecContext(ctx, `UPDATE juhe_jobs.account_balance_outcomes SET committed=TRUE WHERE outcome_id=$1`, outcomeID)
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
func (s *Store) CASOutcome(ctx context.Context, leaseKey string, owner OwnerLease, account AccountLease, outcome Outcome) (bool, error) {
	return s.AppendOutcome(ctx, leaseKey, owner, account, outcome)
}

func writeSnapshotTx(ctx context.Context, tx *sql.Tx, accountID string, inputVersion, configRevision int64, trigger Trigger, snapshot Snapshot, nextRefresh *time.Time, expectedInput, expectedConfig int64, expectedNextRefresh *time.Time) (bool, error) {
	var currentInput, currentConfig int64
	var currentNext any
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT input_version,config_revision,next_refresh_at FROM juhe_jobs.account_balance_snapshots WHERE account_id=$1 FOR UPDATE`, accountID).Scan(&currentInput, &currentConfig, &currentNext)
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
		_, err = tx.ExecContext(ctx, `INSERT INTO juhe_jobs.account_balance_snapshots(account_id,input_version,config_revision,trigger,snapshot_json,next_refresh_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, accountID, inputVersion, configRevision, string(trigger), string(encoded), nilIfEmpty(next), updated)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE juhe_jobs.account_balance_snapshots SET input_version=$1,config_revision=$2,trigger=$3,snapshot_json=$4,next_refresh_at=$5,updated_at=$6 WHERE account_id=$7`, inputVersion, configRevision, string(trigger), string(encoded), nilIfEmpty(next), updated, accountID)
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func insertBalanceOutcome(ctx context.Context, tx *sql.Tx, outcome Outcome, payload string) (bool, error) {
	var id string
	err := tx.QueryRowContext(ctx, `INSERT INTO juhe_jobs.account_balance_outcomes(outcome_id,request_id,account_id,input_version,config_revision,trigger,observed_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(request_id) DO NOTHING RETURNING outcome_id`, outcome.OutcomeID, outcome.RequestID, outcome.AccountID, outcome.InputVersion, outcome.ConfigRevision, string(outcome.Trigger), outcome.ObservedAt.UTC(), payload).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func verifyOwnerTx(ctx context.Context, tx *sql.Tx, leaseKey string, lease OwnerLease, now time.Time) error {
	var owner string
	var token int64
	var until any
	if err := tx.QueryRowContext(ctx, `SELECT owner_id,fence_token,lease_until FROM juhe_jobs.account_balance_owner_leases WHERE lease_key=$1 FOR UPDATE`, leaseKey).Scan(&owner, &token, &until); err != nil {
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

func verifyLeasesTx(ctx context.Context, tx *sql.Tx, leaseKey string, owner OwnerLease, account AccountLease, now time.Time) error {
	if err := verifyOwnerTx(ctx, tx, leaseKey, owner, now); err != nil {
		return err
	}
	var ownerID string
	var token int64
	var until any
	if err := tx.QueryRowContext(ctx, `SELECT owner_id,fence_token,lease_until FROM juhe_jobs.account_balance_account_leases WHERE account_id=$1 FOR UPDATE`, account.AccountID).Scan(&ownerID, &token, &until); err != nil {
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

func verifyOwner(ctx context.Context, db *sql.DB, leaseKey string, lease OwnerLease, now time.Time) error {
	var owner string
	var token int64
	var until any
	if err := db.QueryRowContext(ctx, `SELECT owner_id,fence_token,lease_until FROM juhe_jobs.account_balance_owner_leases WHERE lease_key=$1`, leaseKey).Scan(&owner, &token, &until); err != nil {
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
	if parsed, ok := value.(time.Time); ok {
		return parsed.UTC(), nil
	}
	return time.Time{}, errors.New("未知 SQL 时间类型")
}

func balanceSQLBool(value any) (bool, error) {
	if parsed, ok := value.(bool); ok {
		return parsed, nil
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
CREATE INDEX IF NOT EXISTS idx_account_balance_outcomes_observed ON juhe_jobs.account_balance_outcomes(observed_at);
`
