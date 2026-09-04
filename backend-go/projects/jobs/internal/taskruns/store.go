package taskruns

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/pgpool"

	_ "modernc.org/sqlite"
)

// StoreMode 支持双模：SQLite 测试闭环 / PostgreSQL 生产。
type StoreMode string

const (
	ModeSQLite   StoreMode = "sqlite"
	ModePostgres StoreMode = "postgres"
)

// StoreConfig 等价 accounthealth StoreConfig 的双模配置。
type StoreConfig struct {
	Mode                 StoreMode
	DatabasePath         string
	PostgresURL          string
	PostgresMaxOpenConns int
	PostgresMaxIdleConns int
	PostgresPool         *pgpool.Handle
}

// Store 是 background_task_runs + background_job_leases 的双模存储。
type Store struct {
	db    *sql.DB
	mode  StoreMode
	pool  *pgpool.Handle
	clock Clock
}

// NewStore 使用已有 *sql.DB（测试或自定义池）。
func NewStore(db *sql.DB, mode StoreMode) *Store {
	return &Store{db: db, mode: mode, clock: SystemClock{}}
}

// OpenStore 按配置打开 SQLite（MaxOpenConns(1) 单 writer，事务 IMMEDIATE）
// 或 PostgreSQL（pgpool）。
func OpenStore(config StoreConfig) (*Store, error) {
	switch config.Mode {
	case ModeSQLite:
		path := strings.TrimSpace(config.DatabasePath)
		if path == "" {
			return nil, errors.New("taskruns sqlite 缺少数据库路径")
		}
		dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_txlock=immediate"
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		if _, err := db.Exec("PRAGMA journal_mode = WAL;"); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("配置 taskruns sqlite 单 writer 失败: %w", err)
		}
		return &Store{db: db, mode: ModeSQLite, clock: SystemClock{}}, nil
	case ModePostgres:
		if strings.TrimSpace(config.PostgresURL) == "" {
			return nil, errors.New("taskruns postgres 缺少连接 URL")
		}
		maxOpen := config.PostgresMaxOpenConns
		if maxOpen == 0 {
			maxOpen = 4
		}
		maxIdle := config.PostgresMaxIdleConns
		if maxIdle == 0 {
			maxIdle = maxOpen
		}
		pool := config.PostgresPool
		if pool == nil {
			registry := pgpool.NewRegistry()
			var err error
			pool, err = registry.Acquire("pgx", config.PostgresURL, "taskruns-store", maxOpen, maxIdle)
			if err != nil {
				return nil, err
			}
		}
		return &Store{db: pool.DB(), mode: ModePostgres, pool: pool, clock: SystemClock{}}, nil
	default:
		return nil, errors.New("taskruns store mode 必须为 sqlite 或 postgres")
	}
}

// Close 释放底层连接（pgpool 句柄交还引用计数）。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if s.pool != nil {
		return s.pool.Close()
	}
	return s.db.Close()
}

// Mode 返回当前存储模式。
func (s *Store) Mode() StoreMode { return s.mode }

// SetClock 注入时间源（测试用）。
func (s *Store) SetClock(clock Clock) { s.clock = clock }

func (s *Store) now() time.Time { return s.clock.Now().UTC() }

// ---------------------------------------------------------------------------
// schema

// EnsureSchema 创建 SQLite/PG 表与索引。PG 的 DDL 与
// backend-go/projects/maintenance/internal/schema/pg_schema.go 中
// juhe_stats.background_task_runs / background_job_leases 冻结文本一致。
func (s *Store) EnsureSchema(ctx context.Context) error {
	if s.mode == ModeSQLite {
		if _, err := s.db.ExecContext(ctx, sqliteSchema); err != nil {
			return fmt.Errorf("初始化 taskruns sqlite schema 失败: %w", err)
		}
		return nil
	}
	if _, err := s.db.ExecContext(ctx, postgresSchema); err != nil {
		return fmt.Errorf("初始化 taskruns postgres schema 失败: %w", err)
	}
	return nil
}

func (s *Store) runsTable() string {
	if s.mode == ModePostgres {
		return "juhe_stats.background_task_runs"
	}
	return "background_task_runs"
}

func (s *Store) leasesTable() string {
	if s.mode == ModePostgres {
		return "juhe_stats.background_job_leases"
	}
	return "background_job_leases"
}

// pgNowText 与 Node postgresUtcNowTextSql 一致：数据库毫秒 UTC 时钟。
const pgNowText = `to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')`

// ---------------------------------------------------------------------------
// task run 生命周期

// CreateTaskRun 写入 queued 运行记录（等价 createBackgroundTaskRun）。
func (s *Store) CreateTaskRun(ctx context.Context, input TaskRunCreateInput) (TaskRun, error) {
	now := s.now()
	submittedAt := now
	if input.SubmittedAt != nil {
		submittedAt = input.SubmittedAt.UTC()
	}
	runID := newID("bgtask")
	params := "{}"
	if len(input.Params) > 0 {
		encoded, err := json.Marshal(input.Params)
		if err != nil {
			encoded = []byte("{}")
		}
		params = string(encoded)
	}
	nowText := FormatInstant(now)
	_, err := s.db.ExecContext(ctx, `
	INSERT INTO `+s.runsTable()+` (
	  run_id, job_name, job_type, worker_role, status, lease_key, params_json, result_json,
	  submitted_at, created_at, updated_at
	) VALUES (?, ?, ?, ?, 'queued', ?, ?, '{}', ?, ?, ?)
	`, runID, input.JobName, input.JobType, input.WorkerRole, input.LeaseKey, params,
		FormatInstant(submittedAt), nowText, nowText)
	if err != nil {
		return TaskRun{}, fmt.Errorf("写入后台任务运行记录失败: %w", err)
	}
	run, err := s.GetTaskRun(ctx, runID)
	if err != nil {
		return TaskRun{}, err
	}
	if run == nil {
		return TaskRun{}, errors.New("后台任务运行记录写入后不可读")
	}
	return *run, nil
}

// TryStartTaskRun 将 queued 行 CAS 为 running 并获取
// temporary-maintenance-worker 租约（等价 tryStartBackgroundTaskRun）。
func (s *Store) TryStartTaskRun(ctx context.Context, input TaskRunStartInput) (bool, error) {
	now := s.now()
	if input.Now != nil {
		now = input.Now.UTC()
	}
	nowText := FormatInstant(now)
	changed, err := s.execChanges(ctx, `
	UPDATE `+s.runsTable()+`
	SET status = 'running',
	  owner_id = ?,
	  started_at = COALESCE(started_at, ?),
	  heartbeat_at = ?,
	  updated_at = ?
	WHERE run_id = ?
	  AND status = 'queued'
	`, input.OwnerID, nowText, nowText, nowText, input.RunID)
	if err != nil {
		return false, fmt.Errorf("启动后台任务运行记录失败: %w", err)
	}
	if changed <= 0 {
		return false, nil
	}
	return s.AcquireLease(ctx, LeaseAcquireInput{
		LeaseKey:   TemporaryTaskLeaseKey(input.RunID),
		JobName:    TemporaryMaintenanceWorkerRole,
		ShardKey:   input.RunID,
		OwnerID:    input.OwnerID,
		RunID:      input.RunID,
		LeaseUntil: input.LeaseUntil,
		Now:        &now,
	})
}

// HeartbeatTaskRun 刷新 running 行心跳并续租（等价 heartbeatBackgroundTaskRun）。
func (s *Store) HeartbeatTaskRun(ctx context.Context, runID, ownerID string, leaseUntil time.Time, now *time.Time) (bool, error) {
	nowTs := s.now()
	if now != nil {
		nowTs = now.UTC()
	}
	nowText := FormatInstant(nowTs)
	changed, err := s.execChanges(ctx, `
	UPDATE `+s.runsTable()+`
	SET heartbeat_at = ?, updated_at = ?
	WHERE run_id = ?
	  AND owner_id = ?
	  AND status = 'running'
	`, nowText, nowText, runID, ownerID)
	if err != nil {
		return false, fmt.Errorf("刷新后台任务心跳失败: %w", err)
	}
	if changed <= 0 {
		return false, nil
	}
	return s.RenewLease(ctx, TemporaryTaskLeaseKey(runID), ownerID, leaseUntil, &nowTs)
}

// FinishTaskRun 写终态并释放租约（等价 finishBackgroundTaskRun）。
func (s *Store) FinishTaskRun(ctx context.Context, input TaskRunFinishInput) (bool, error) {
	finishedAt := s.now()
	if input.FinishedAt != nil {
		finishedAt = input.FinishedAt.UTC()
	}
	run, err := s.GetTaskRun(ctx, input.RunID)
	if err != nil {
		return false, err
	}
	if run == nil {
		return false, nil
	}
	var durationMs *int64
	if run.StartedAt != nil {
		delta := finishedAt.Sub(*run.StartedAt).Milliseconds()
		if delta < 0 {
			delta = 0
		}
		durationMs = &delta
	}
	resultJSON := "{}"
	if len(input.Result) > 0 {
		encoded, err := json.Marshal(input.Result)
		if err != nil {
			encoded = []byte("{}")
		}
		resultJSON = string(encoded)
	}
	var errorMessage any
	if input.ErrorMessage != "" {
		errorMessage = input.ErrorMessage
	}
	var exitCode any
	if input.ExitCode != nil {
		exitCode = *input.ExitCode
	}
	finishedText := FormatInstant(finishedAt)
	changed, err := s.execChanges(ctx, `
	UPDATE `+s.runsTable()+`
	SET status = ?,
	  result_json = ?,
	  error_message = ?,
	  finished_at = ?,
	  duration_ms = ?,
	  exit_code = ?,
	  updated_at = ?
	WHERE run_id = ?
	`, string(input.Status), resultJSON, errorMessage, finishedText, durationMs, exitCode, finishedText, input.RunID)
	if err != nil {
		return false, fmt.Errorf("写入后台任务终态失败: %w", err)
	}
	if run.OwnerID != "" {
		_ = s.ReleaseLease(ctx, TemporaryTaskLeaseKey(input.RunID), run.OwnerID)
	}
	return changed > 0, nil
}

// GetTaskRun 读取运行记录；不存在返回 nil。
func (s *Store) GetTaskRun(ctx context.Context, runID string) (*TaskRun, error) {
	row := s.db.QueryRowContext(ctx, `
	SELECT run_id, job_name, job_type, worker_role, status, lease_key, owner_id,
	  params_json, result_json, error_message, submitted_at, started_at, heartbeat_at,
	  finished_at, duration_ms, exit_code, created_at, updated_at
	FROM `+s.runsTable()+`
	WHERE run_id = ?
	LIMIT 1
	`, runID)
	run, err := scanTaskRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return run, err
}

func scanTaskRun(row *sql.Row) (*TaskRun, error) {
	var (
		run                                TaskRun
		ownerID, errorMessage              sql.NullString
		paramsJSON, resultJSON             string
		startedAt, heartbeatAt, finishedAt sql.NullString
		durationMs, exitCode               sql.NullInt64
		submittedAt, createdAt, updatedAt  string
		status                             string
	)
	if err := row.Scan(&run.RunID, &run.JobName, &run.JobType, &run.WorkerRole, &status,
		&run.LeaseKey, &ownerID, &paramsJSON, &resultJSON, &errorMessage,
		&submittedAt, &startedAt, &heartbeatAt, &finishedAt, &durationMs, &exitCode,
		&createdAt, &updatedAt); err != nil {
		return nil, err
	}
	run.Status = NormalizeStatus(status)
	run.OwnerID = ownerID.String
	run.ErrorMessage = errorMessage.String
	run.Params = parseJSONObject(paramsJSON)
	run.Result = parseJSONObject(resultJSON)
	var err error
	if run.SubmittedAt, err = ParseInstant(submittedAt, "background_task_runs.submitted_at"); err != nil {
		return nil, err
	}
	if run.StartedAt, err = optionalInstant(startedAt, "background_task_runs.started_at"); err != nil {
		return nil, err
	}
	if run.HeartbeatAt, err = optionalInstant(heartbeatAt, "background_task_runs.heartbeat_at"); err != nil {
		return nil, err
	}
	if run.FinishedAt, err = optionalInstant(finishedAt, "background_task_runs.finished_at"); err != nil {
		return nil, err
	}
	if durationMs.Valid {
		v := durationMs.Int64
		run.DurationMs = &v
	}
	if exitCode.Valid {
		v := exitCode.Int64
		run.ExitCode = &v
	}
	if run.CreatedAt, err = ParseInstant(createdAt, "background_task_runs.created_at"); err != nil {
		return nil, err
	}
	if run.UpdatedAt, err = ParseInstant(updatedAt, "background_task_runs.updated_at"); err != nil {
		return nil, err
	}
	return &run, nil
}

func optionalInstant(value sql.NullString, field string) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	t, err := ParseInstant(value.String, field)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func parseJSONObject(text string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(text) == "" {
		return out
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return out
	}
	if obj, ok := parsed.(map[string]any); ok {
		return obj
	}
	return out
}

// ---------------------------------------------------------------------------
// 共享租约（background_job_leases）

// LeaseAcquireInput 等价 Node acquireBackgroundJobLease 输入。
type LeaseAcquireInput struct {
	LeaseKey   string
	JobName    string
	ShardKey   string
	OwnerID    string
	RunID      string
	LeaseUntil time.Time
	Now        *time.Time
}

// AcquireLease 以“过期即可接管”的 upsert CAS 获取租约
// （等价 Node acquireBackgroundJobLease：INSERT .. ON CONFLICT DO UPDATE
// WHERE lease_until <= now）。kill-restart 后新 owner 由此接管。
func (s *Store) AcquireLease(ctx context.Context, input LeaseAcquireInput) (bool, error) {
	now := s.now()
	if input.Now != nil {
		now = input.Now.UTC()
	}
	nowText := FormatInstant(now)
	untilText := FormatInstant(input.LeaseUntil)
	shardKey := input.ShardKey
	var runID any
	if input.RunID != "" {
		runID = input.RunID
	}
	changes, err := s.execChanges(ctx, `
	INSERT INTO `+s.leasesTable()+` (
	  lease_key, job_name, shard_key, owner_id, run_id, lease_until, heartbeat_at, started_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(lease_key) DO UPDATE SET
	  job_name = excluded.job_name,
	  shard_key = excluded.shard_key,
	  owner_id = excluded.owner_id,
	  run_id = excluded.run_id,
	  lease_until = excluded.lease_until,
	  heartbeat_at = excluded.heartbeat_at,
	  started_at = excluded.started_at,
	  updated_at = excluded.updated_at
	WHERE `+s.leasesTable()+`.lease_until <= ?
	`, input.LeaseKey, input.JobName, shardKey, input.OwnerID, runID, untilText, nowText, nowText, nowText, nowText)
	if err != nil {
		return false, fmt.Errorf("获取后台任务租约失败: %w", err)
	}
	return changes > 0, nil
}

// RenewLease 仅允许当前 owner 续租（等价 Node renewBackgroundJobLease）。
func (s *Store) RenewLease(ctx context.Context, leaseKey, ownerID string, leaseUntil time.Time, now *time.Time) (bool, error) {
	nowTs := s.now()
	if now != nil {
		nowTs = now.UTC()
	}
	nowText := FormatInstant(nowTs)
	changed, err := s.execChanges(ctx, `
	UPDATE `+s.leasesTable()+`
	SET lease_until = ?, heartbeat_at = ?, updated_at = ?
	WHERE lease_key = ?
	  AND owner_id = ?
	`, FormatInstant(leaseUntil), nowText, nowText, leaseKey, ownerID)
	if err != nil {
		return false, fmt.Errorf("续租后台任务租约失败: %w", err)
	}
	return changed > 0, nil
}

// ReleaseLease 按 key+owner 删除租约；owner 为空时是 no-op
// （等价 Node releaseBackgroundJobLease）。
func (s *Store) ReleaseLease(ctx context.Context, leaseKey, ownerID string) error {
	if ownerID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
	DELETE FROM `+s.leasesTable()+`
	WHERE lease_key = ?
	  AND owner_id = ?
	`, leaseKey, ownerID)
	if err != nil {
		return fmt.Errorf("释放后台任务租约失败: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 带 fencing token 的周期任务共享租约（对齐 Node tryAcquireScheduledJobLease）

const scheduledJobLeaseAdvisoryNamespace = "juhe-ai:scheduled-job-lease:v1:"

// ScheduledLeaseAdvisoryKey 与 Node scheduledJobLeaseAdvisoryKey 一致：
// sha256(namespace+leaseKey) 首 8 字节按大端转有符号 int64。
func ScheduledLeaseAdvisoryKey(leaseKey string) (int64, error) {
	trimmed, err := requiredText(leaseKey, "leaseKey")
	if err != nil {
		return 0, err
	}
	digest := sha256Sum([]byte(scheduledJobLeaseAdvisoryNamespace + trimmed))
	unsigned := uint64(digest[0])<<56 | uint64(digest[1])<<48 | uint64(digest[2])<<40 | uint64(digest[3])<<32 |
		uint64(digest[4])<<24 | uint64(digest[5])<<16 | uint64(digest[6])<<8 | uint64(digest[7])
	return int64(unsigned), nil
}

// TryAcquireScheduledLease 获取带 fencing token 的共享租约：
// PG 在事务内先取 pg_try_advisory_xact_lock 再做过期 CAS + token 自增；
// SQLite 依赖单 writer 连接 + IMMEDIATE 事务做同样的过期 CAS + token 自增，
// 无 advisory（advisory_busy 仅是 PG 并发快路径，不影响语义）。
func (s *Store) TryAcquireScheduledLease(ctx context.Context, input ScheduledLeaseAcquireInput) (AcquireResult, error) {
	jobName, err := requiredText(input.JobName, "jobName")
	if err != nil {
		return AcquireResult{}, err
	}
	shardKey := input.ShardKey
	if shardKey == "" {
		shardKey = "global"
	}
	if shardKey, err = requiredText(shardKey, "shardKey"); err != nil {
		return AcquireResult{}, err
	}
	leaseKey := input.LeaseKey
	if leaseKey == "" {
		leaseKey = ScheduledLeaseKey(jobName, shardKey)
	}
	if leaseKey, err = requiredText(leaseKey, "leaseKey"); err != nil {
		return AcquireResult{}, err
	}
	ownerID, err := requiredText(input.OwnerID, "ownerId")
	if err != nil {
		return AcquireResult{}, err
	}
	ttl, err := NormalizeLeaseTTL(input.TTL)
	if err != nil {
		return AcquireResult{}, err
	}
	var runID any
	if input.RunID != "" {
		if runID, err = requiredText(input.RunID, "runId"); err != nil {
			return AcquireResult{}, err
		}
	}

	var result AcquireResult
	txErr := s.withTx(ctx, func(tx *sql.Tx) error {
		if s.mode == ModePostgres {
			advisory, advErr := ScheduledLeaseAdvisoryKey(leaseKey)
			if advErr != nil {
				return advErr
			}
			var acquiredRaw any
			if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1::bigint) AS acquired`, advisory).Scan(&acquiredRaw); err != nil {
				return err
			}
			if !postgresBool(acquiredRaw) {
				result = AcquireResult{Acquired: false, Reason: AcquireAdvisoryBusy, LeaseKey: leaseKey}
				return nil
			}
		}
		identity, opErr := s.acquireScheduledLeaseRow(ctx, tx, leaseKey, jobName, shardKey, ownerID, runID, ttl)
		if opErr != nil {
			return opErr
		}
		if identity == nil {
			result = AcquireResult{Acquired: false, Reason: AcquireLeaseHeld, LeaseKey: leaseKey}
			return nil
		}
		result = AcquireResult{Acquired: true, LeaseKey: leaseKey, Lease: identity}
		return nil
	})
	if txErr != nil {
		return AcquireResult{}, txErr
	}
	return result, nil
}

func (s *Store) acquireScheduledLeaseRow(ctx context.Context, tx *sql.Tx, leaseKey, jobName, shardKey, ownerID string, runID any, ttl time.Duration) (*LeaseIdentity, error) {
	var query string
	var args []any
	if s.mode == ModePostgres {
		query = `
	INSERT INTO ` + s.leasesTable() + ` AS current (
	  lease_key, job_name, shard_key, owner_id, run_id, lease_until,
	  heartbeat_at, started_at, updated_at, fencing_token
	) VALUES ($1, $2, $3, $4, $5, to_char((clock_timestamp() + ($6 * INTERVAL '1 millisecond')) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'), ` + pgNowText + `, ` + pgNowText + `, ` + pgNowText + `, 1)
	ON CONFLICT(lease_key) DO UPDATE SET
	  job_name = excluded.job_name,
	  shard_key = excluded.shard_key,
	  owner_id = excluded.owner_id,
	  run_id = excluded.run_id,
	  lease_until = excluded.lease_until,
	  heartbeat_at = excluded.heartbeat_at,
	  started_at = excluded.started_at,
	  updated_at = excluded.updated_at,
	  fencing_token = current.fencing_token + 1
	WHERE current.lease_until <= ` + pgNowText + `
	RETURNING lease_key, owner_id, fencing_token, lease_until
	`
		args = []any{leaseKey, jobName, shardKey, ownerID, runID, ttl.Milliseconds()}
	} else {
		nowText := FormatInstant(s.now())
		untilText := FormatInstant(s.now().Add(ttl))
		query = `
	INSERT INTO ` + s.leasesTable() + ` (
	  lease_key, job_name, shard_key, owner_id, run_id, lease_until,
	  heartbeat_at, started_at, updated_at, fencing_token
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
	ON CONFLICT(lease_key) DO UPDATE SET
	  job_name = excluded.job_name,
	  shard_key = excluded.shard_key,
	  owner_id = excluded.owner_id,
	  run_id = excluded.run_id,
	  lease_until = excluded.lease_until,
	  heartbeat_at = excluded.heartbeat_at,
	  started_at = excluded.started_at,
	  updated_at = excluded.updated_at,
	  fencing_token = fencing_token + 1
	WHERE background_job_leases.lease_until <= ?
	RETURNING lease_key, owner_id, fencing_token, lease_until
	`
		args = []any{leaseKey, jobName, shardKey, ownerID, runID, untilText, nowText, nowText, nowText, nowText}
	}
	// 事务内必须绑定同一连接：MaxOpenConns(1) 下经由 s.db 再取连接会死锁。
	identity, err := scanLeaseIdentity(tx.QueryRowContext(ctx, query, args...))
	if err != nil {
		return nil, err
	}
	return identity, nil
}

// RenewScheduledLease 校验 key+owner+token+未过期 后续租
// （等价 Node renewScheduledJobLease）。
func (s *Store) RenewScheduledLease(ctx context.Context, lease LeaseIdentity, ttl time.Duration) (*LeaseIdentity, error) {
	normalizedTTL, err := NormalizeLeaseTTL(ttl)
	if err != nil {
		return nil, err
	}
	key, err := requiredText(lease.LeaseKey, "leaseKey")
	if err != nil {
		return nil, err
	}
	owner, err := requiredText(lease.OwnerID, "ownerId")
	if err != nil {
		return nil, err
	}
	var query string
	var args []any
	if s.mode == ModePostgres {
		query = `
	UPDATE ` + s.leasesTable() + `
	SET lease_until = to_char((clock_timestamp() + ($1 * INTERVAL '1 millisecond')) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'),
	  heartbeat_at = ` + pgNowText + `,
	  updated_at = ` + pgNowText + `
	WHERE lease_key = $2
	  AND owner_id = $3
	  AND fencing_token = $4
	  AND lease_until > ` + pgNowText + `
	RETURNING lease_key, owner_id, fencing_token, lease_until
	`
		args = []any{normalizedTTL.Milliseconds(), key, owner, lease.FencingToken}
	} else {
		nowText := FormatInstant(s.now())
		query = `
	UPDATE ` + s.leasesTable() + `
	SET lease_until = ?, heartbeat_at = ?, updated_at = ?
	WHERE lease_key = ?
	  AND owner_id = ?
	  AND fencing_token = ?
	  AND lease_until > ?
	RETURNING lease_key, owner_id, fencing_token, lease_until
	`
		args = []any{FormatInstant(s.now().Add(normalizedTTL)), nowText, nowText, key, owner, lease.FencingToken, nowText}
	}
	identity, err := s.queryLeaseIdentity(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return identity, nil
}

// ReleaseScheduledLease 将仍有效的租约立即置为过期（等价 Node
// releaseScheduledJobLease：lease_until := now，保留行用于 fencing 审计）。
func (s *Store) ReleaseScheduledLease(ctx context.Context, lease LeaseIdentity) (bool, error) {
	var query string
	var args []any
	if s.mode == ModePostgres {
		query = `
	UPDATE ` + s.leasesTable() + `
	SET lease_until = ` + pgNowText + `,
	  heartbeat_at = ` + pgNowText + `,
	  updated_at = ` + pgNowText + `
	WHERE lease_key = $1
	  AND owner_id = $2
	  AND fencing_token = $3
	  AND lease_until > ` + pgNowText + `
	`
		args = []any{lease.LeaseKey, lease.OwnerID, lease.FencingToken}
	} else {
		nowText := FormatInstant(s.now())
		query = `
	UPDATE ` + s.leasesTable() + `
	SET lease_until = ?, heartbeat_at = ?, updated_at = ?
	WHERE lease_key = ?
	  AND owner_id = ?
	  AND fencing_token = ?
	  AND lease_until > ?
	`
		args = []any{nowText, nowText, nowText, lease.LeaseKey, lease.OwnerID, lease.FencingToken, nowText}
	}
	changed, err := s.execChanges(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("释放周期任务租约失败: %w", err)
	}
	return changed > 0, nil
}

// AssertScheduledLease 校验 fence 仍持有有效租约，否则返回 *ErrLeaseLost
// （等价 Node assertScheduledJobLease / pinScheduledJobLeaseInTransaction 的
// 失败路径）。
func (s *Store) AssertScheduledLease(ctx context.Context, lease LeaseFence) error {
	var query string
	var args []any
	if s.mode == ModePostgres {
		query = `
	SELECT lease_key
	FROM ` + s.leasesTable() + `
	WHERE lease_key = $1
	  AND owner_id = $2
	  AND fencing_token = $3
	  AND lease_until > ` + pgNowText + `
	LIMIT 1
	`
		args = []any{lease.LeaseKey, lease.OwnerID, lease.FencingToken}
	} else {
		query = `
	SELECT lease_key
	FROM ` + s.leasesTable() + `
	WHERE lease_key = ?
	  AND owner_id = ?
	  AND fencing_token = ?
	  AND lease_until > ?
	LIMIT 1
	`
		args = []any{lease.LeaseKey, lease.OwnerID, lease.FencingToken, FormatInstant(s.now())}
	}
	var key string
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return &ErrLeaseLost{Lease: lease}
	}
	if err != nil {
		return err
	}
	return nil
}

func (s *Store) queryLeaseIdentity(ctx context.Context, query string, args ...any) (*LeaseIdentity, error) {
	return scanLeaseIdentity(s.db.QueryRowContext(ctx, query, args...))
}

func scanLeaseIdentity(row *sql.Row) (*LeaseIdentity, error) {
	var (
		key, owner, until string
		token             int64
	)
	err := row.Scan(&key, &owner, &token, &until)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parsedUntil, err := ParseInstant(until, "background_job_leases.lease_until")
	if err != nil {
		return nil, err
	}
	return &LeaseIdentity{LeaseKey: key, OwnerID: owner, FencingToken: token, LeaseUntil: parsedUntil}, nil
}

// ---------------------------------------------------------------------------
// 对账（background-task-run-reconcile 的对账对象）

// ReconcileStale 对齐 Node reconcileStaleBackgroundTaskRuns：单事务内
// 1) 超时未启动的 queued 临时任务收口为 failed（worker_never_started）
// 2) 心跳中断且无有效租约的 running 收口为 failed（lease_expired_after_worker_exit）
// 3) 删除已终态/孤儿临时任务中已过期的 temporary-maintenance-worker 租约。
func (s *Store) ReconcileStale(ctx context.Context, input TaskRunReconcileInput) (TaskRunReconcileResult, error) {
	now := s.now()
	if input.Now != nil {
		now = input.Now.UTC()
	}
	limit := NormalizeReconcileLimit(input.Limit)
	queuedBefore := FormatInstant(input.QueuedBefore)
	heartbeatBefore := FormatInstant(input.RunningHeartbeatBefore)
	nowText := FormatInstant(now)

	var result TaskRunReconcileResult
	txErr := s.withTx(ctx, func(tx *sql.Tx) error {
		// Node 的 PG SQL 含外层 NOT EXISTS（9 个占位符）；SQLite 分支去掉
		// 该外层限定后为 8 个占位符，参数顺序与谓词一一对应。
		queuedArgs := []any{
			`{"reconciled":true,"reconciledReason":"worker_never_started"}`,
			"临时维护 worker 未在期限内启动，后台任务已自动收口为失败",
			nowText, nowText, queuedBefore, nowText, queuedBefore, nowText, limit,
		}
		runningArgs := []any{
			`{"reconciled":true,"reconciledReason":"lease_expired_after_worker_exit"}`,
			"临时维护 worker 心跳中断且无有效租约，后台任务已自动收口为失败",
			nowText, nowText, heartbeatBefore, nowText, heartbeatBefore, nowText, limit,
		}
		if s.mode != ModePostgres {
			queuedArgs = []any{
				`{"reconciled":true,"reconciledReason":"worker_never_started"}`,
				"临时维护 worker 未在期限内启动，后台任务已自动收口为失败",
				nowText, nowText, queuedBefore, nowText, limit,
			}
			runningArgs = []any{
				`{"reconciled":true,"reconciledReason":"lease_expired_after_worker_exit"}`,
				"临时维护 worker 心跳中断且无有效租约，后台任务已自动收口为失败",
				nowText, nowText, heartbeatBefore, nowText, limit,
			}
		}
		changes, err := execTxChanges(tx, s.reconcileQueuedSQL(), queuedArgs...)
		if err != nil {
			return err
		}
		result.FailedQueuedCount = changes

		changes, err = execTxChanges(tx, s.reconcileRunningSQL(), runningArgs...)
		if err != nil {
			return err
		}
		result.FailedRunningCount = changes

		changes, err = execTxChanges(tx, s.deleteExpiredLeasesSQL(), nowText, limit)
		if err != nil {
			return err
		}
		result.DeletedExpiredLeaseCount = changes
		return nil
	})
	if txErr != nil {
		return TaskRunReconcileResult{}, txErr
	}
	return result, nil
}

// Node 的 SQLite 对账 UPDATE 使用 IN (SELECT ..) 子查询而非 PG 的
// NOT EXISTS 目标行限定（SQLite 不支持 UPDATE 目标行上直接引用同表
// CTE/子查询的写法），谓词与排序完全一致；PG 分支与 Node SQL 文本一致。
func (s *Store) reconcileQueuedSQL() string {
	runs, leases := s.runsTable(), s.leasesTable()
	leaseValidSub := "leases.run_id = runs.run_id AND leases.job_name = '" + TemporaryMaintenanceWorkerRole + "' AND leases.lease_until > ?"
	head := `
	SET status = 'failed',
	  result_json = ?,
	  error_message = ?,
	  finished_at = ?,
	  updated_at = ?
	WHERE run_id IN (
	  SELECT runs.run_id
	  FROM ` + runs + ` runs
	  WHERE runs.worker_role = '` + TemporaryMaintenanceWorkerRole + `'
	    AND runs.status = 'queued'
	    AND runs.submitted_at <= ?
	    AND NOT EXISTS (
	      SELECT 1
	      FROM ` + leases + ` leases
	      WHERE ` + leaseValidSub + `
	    )
	  ORDER BY runs.updated_at ASC, runs.run_id ASC
	  LIMIT ?
	)
	`
	if s.mode == ModePostgres {
		return `
	UPDATE ` + runs + ` AS target
	SET status = 'failed',
	  result_json = ?,
	  error_message = ?,
	  finished_at = ?,
	  updated_at = ?
	WHERE target.worker_role = '` + TemporaryMaintenanceWorkerRole + `'
	  AND target.status = 'queued'
	  AND target.submitted_at <= ?
	  AND NOT EXISTS (
	    SELECT 1
	    FROM ` + leases + ` current_lease
	    WHERE current_lease.run_id = target.run_id
	      AND current_lease.job_name = '` + TemporaryMaintenanceWorkerRole + `'
	      AND current_lease.lease_until > ?
	  )
	  AND target.run_id IN (
	    SELECT runs.run_id
	    FROM ` + runs + ` runs
	    WHERE runs.worker_role = '` + TemporaryMaintenanceWorkerRole + `'
	      AND runs.status = 'queued'
	      AND runs.submitted_at <= ?
	      AND NOT EXISTS (
	        SELECT 1
	        FROM ` + leases + ` leases
	        WHERE ` + leaseValidSub + `
	      )
	    ORDER BY runs.updated_at ASC, runs.run_id ASC
	    LIMIT ?
	  )
	`
	}
	return `UPDATE ` + runs + head
}

func (s *Store) reconcileRunningSQL() string {
	runs, leases := s.runsTable(), s.leasesTable()
	leaseValidSub := "leases.run_id = runs.run_id AND leases.job_name = '" + TemporaryMaintenanceWorkerRole + "' AND leases.lease_until > ?"
	if s.mode == ModePostgres {
		return `
	UPDATE ` + runs + ` AS target
	SET status = 'failed',
	  result_json = ?,
	  error_message = ?,
	  finished_at = ?,
	  updated_at = ?
	WHERE target.worker_role = '` + TemporaryMaintenanceWorkerRole + `'
	  AND target.status = 'running'
	  AND COALESCE(target.heartbeat_at, target.started_at, target.updated_at) <= ?
	  AND NOT EXISTS (
	    SELECT 1
	    FROM ` + leases + ` current_lease
	    WHERE current_lease.run_id = target.run_id
	      AND current_lease.job_name = '` + TemporaryMaintenanceWorkerRole + `'
	      AND current_lease.lease_until > ?
	  )
	  AND target.run_id IN (
	    SELECT runs.run_id
	    FROM ` + runs + ` runs
	    WHERE runs.worker_role = '` + TemporaryMaintenanceWorkerRole + `'
	      AND runs.status = 'running'
	      AND COALESCE(runs.heartbeat_at, runs.started_at, runs.updated_at) <= ?
	      AND NOT EXISTS (
	        SELECT 1
	        FROM ` + leases + ` leases
	        WHERE ` + leaseValidSub + `
	      )
	    ORDER BY runs.updated_at ASC, runs.run_id ASC
	    LIMIT ?
	  )
	`
	}
	return `
	UPDATE ` + runs + ` AS target
	SET status = 'failed',
	  result_json = ?,
	  error_message = ?,
	  finished_at = ?,
	  updated_at = ?
	WHERE target.run_id IN (
	  SELECT runs.run_id
	  FROM ` + runs + ` runs
	  WHERE runs.worker_role = '` + TemporaryMaintenanceWorkerRole + `'
	    AND runs.status = 'running'
	    AND COALESCE(runs.heartbeat_at, runs.started_at, runs.updated_at) <= ?
	    AND NOT EXISTS (
	      SELECT 1
	      FROM ` + leases + ` leases
	      WHERE ` + leaseValidSub + `
	    )
	  ORDER BY runs.updated_at ASC, runs.run_id ASC
	  LIMIT ?
	)
	`
}

func (s *Store) deleteExpiredLeasesSQL() string {
	runs, leases := s.runsTable(), s.leasesTable()
	return `
	DELETE FROM ` + leases + `
	WHERE lease_key IN (
	  SELECT leases.lease_key
	  FROM ` + leases + ` leases
	  LEFT JOIN ` + runs + ` runs ON runs.run_id = leases.run_id
	  WHERE leases.job_name = '` + TemporaryMaintenanceWorkerRole + `'
	    AND leases.lease_until <= ?
	    AND (runs.run_id IS NULL OR runs.status NOT IN ('queued', 'running'))
	  ORDER BY leases.lease_until ASC, leases.lease_key ASC
	  LIMIT ?
	)
	`
}

// ---------------------------------------------------------------------------
// 底层执行辅助

func (s *Store) execChanges(ctx context.Context, query string, args ...any) (int64, error) {
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func execTxChanges(tx *sql.Tx, query string, args ...any) (int64, error) {
	result, err := tx.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// withTx 在单个事务内执行 fn。SQLite DSN 已启用 _txlock=immediate，
// 与 MaxOpenConns(1) 一起保证读-改-写竞争串行化；PG 走默认事务，
// 租约行的过期 CAS 由行级锁 + WHERE 谓词保证。
func (s *Store) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func postgresBool(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return v == "t" || v == "true" || v == "1"
	case int64:
		return v != 0
	case []byte:
		return string(v) == "t" || string(v) == "true" || string(v) == "1"
	default:
		return false
	}
}
