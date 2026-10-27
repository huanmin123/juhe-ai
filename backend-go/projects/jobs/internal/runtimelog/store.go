package runtimelog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlpool"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	facetBucketKey                 = "current"
	runtimeLogOwnerLeaseKey        = "runtime-log-index-retention"
	postgresInsertRowsPerStatement = 5000
	postgresCleanupRowsPerBatch    = 5000
	// BUG-0254：lock_timeout 2s→5s、statement_timeout 5s→30s，对齐
	// maintenance j3a/j3b 族先例（30s/5s）。每日 04:10 pg_dump 的整库读
	// IO 压力会把持锁事务拖长（生产实测 F1 连续 55P03 峰值与备份窗口
	// 吻合），2s 锁等待在备份窗口内批量超时；正常时段事务为毫秒级，
	// 上限放宽不改变常态行为。idle 事务超时保持 5s 防挂死语义不变。
	postgresStatementTimeout = "30s"
	postgresLockTimeout      = "5s"
	postgresIdleTxTimeout    = "5s"
	// 租约获取/续租的 Go 侧 ctx 预算须大于 PG 侧 lock_timeout+执行时间
	//（BUG-0254 同批上调，否则 ctx 先于锁超时取消，放宽失效）。
	postgresOwnerLeaseTimeout = 15 * time.Second
)

var errPostgresRuntimeLogSchemaMissing = errors.New("PostgreSQL 运行日志 schema 不完整")

type facetRow struct {
	Time  string
	Level string
	Event string
}

type postgresStore struct {
	pool *pgxpool.Pool
}

// rollbackPostgresTx must not reuse the caller's context.  F1 database
// operations are bounded by a short cycle context; when that context expires,
// pgx will otherwise reject Rollback as already canceled and return the pooled
// connection with an open transaction.  The leaked transaction can retain
// relation locks on the F1 tables and block concurrent maintenance
// indefinitely.
func rollbackPostgresTx(tx pgx.Tx) {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = tx.Rollback(rollbackCtx)
}

// beginPostgresTx installs a bounded server-side guard before the owner-row
// fence is acquired. A canceled caller context alone is insufficient here:
// an interrupted client must not leave the F1 lease row locked indefinitely.
func beginPostgresTx(ctx context.Context, pool *pgxpool.Pool) (pgx.Tx, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	for _, statement := range postgresTransactionGuardStatements() {
		if _, err := tx.Exec(ctx, statement); err != nil {
			rollbackPostgresTx(tx)
			return nil, fmt.Errorf("设置 F1 PostgreSQL 事务时限失败: %w", err)
		}
	}
	return tx, nil
}

func postgresTransactionGuardStatements() []string {
	return []string{
		"SET LOCAL statement_timeout = '" + postgresStatementTimeout + "'",
		"SET LOCAL lock_timeout = '" + postgresLockTimeout + "'",
		"SET LOCAL idle_in_transaction_session_timeout = '" + postgresIdleTxTimeout + "'",
	}
}

// postgresRebuildStatementTimeout 是 facet 重建事务的语句时限：一次性全量
// 聚合的语句时长超出常驻 30s 守卫（W33 生产 1.39M 行实测 57014）。重建持
// 独占 owner lease 且幂等可重入，statement_timeout 单独放宽为有界 15 分钟；
// lock_timeout 沿用常驻守卫，避免长聚合持锁阻塞业务读写。
const postgresRebuildStatementTimeout = "15min"

func postgresRebuildTransactionGuardStatements() []string {
	return []string{
		"SET LOCAL statement_timeout = '" + postgresRebuildStatementTimeout + "'",
		"SET LOCAL lock_timeout = '" + postgresLockTimeout + "'",
	}
}

func ownerLeaseContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, postgresOwnerLeaseTimeout)
}

func OpenStore(ctx context.Context, config Config) (Store, error) {
	switch config.Mode {
	case ModePostgres:
		poolConfig, err := pgxpool.ParseConfig(config.PostgresURL)
		if err != nil {
			return nil, err
		}
		maxConns := config.PostgresMaxConns
		if maxConns == 0 {
			maxConns = 1000
		}
		poolConfig.MaxConns = int32(maxConns)
		poolConfig.MinConns = int32(config.PostgresMinConns)
		poolConfig.MinIdleConns = int32(config.PostgresMinConns)
		poolConfig.MaxConnIdleTime = sqlpool.MaxConnIdleTime
		if poolConfig.MinConns > poolConfig.MaxConns {
			return nil, fmt.Errorf("运行日志 PostgreSQL min connections 不得大于 max connections: %d > %d", poolConfig.MinConns, poolConfig.MaxConns)
		}
		pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
		if err != nil {
			return nil, err
		}
		if err := pool.Ping(ctx); err != nil {
			pool.Close()
			return nil, err
		}
		return &postgresStore{pool: pool}, nil
	default:
		return nil, fmt.Errorf("不支持的运行日志 Store 模式 %q", config.Mode)
	}
}

func EnsureSchema(ctx context.Context, store Store) error {
	switch value := store.(type) {
	case *postgresStore:
		bootstrap, err := postgresRuntimeLogSchemaBootstrapRequired(value.CheckSchema(ctx))
		if err != nil {
			return err
		}
		if !bootstrap {
			return nil
		}
		for _, statement := range strings.Split(postgresSchema, ";") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if _, err := value.pool.Exec(ctx, statement); err != nil {
				return err
			}
		}
		if err := value.CheckSchema(ctx); err != nil {
			return fmt.Errorf("初始化 PostgreSQL 运行日志 schema 后校验失败: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("未知运行日志 Store")
	}
}

func (store *postgresStore) FindCursor(ctx context.Context, logFile string) (*Cursor, error) {
	return scanPostgresCursor(store.pool.QueryRow(ctx, postgresCursorSelect+" WHERE log_file = $1", logFile))
}

func (store *postgresStore) FindCursorByIdentity(ctx context.Context, identity string) (*Cursor, error) {
	return scanPostgresCursor(store.pool.QueryRow(ctx, postgresCursorSelect+" WHERE file_identity = $1 ORDER BY updated_at DESC LIMIT 1", identity))
}

func (store *postgresStore) ReplaceCursor(ctx context.Context, lease OwnerLease, displaced *Cursor, replacement Cursor) error {
	tx, err := beginPostgresTx(ctx, store.pool)
	if err != nil {
		return err
	}
	defer rollbackPostgresTx(tx)
	if err := verifyPostgresOwnerLease(ctx, tx, lease); err != nil {
		return err
	}
	if displaced != nil {
		if err := upsertPostgresCursor(ctx, tx, *displaced); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, "DELETE FROM juhe_dataset.runtime_log_file_cursors WHERE log_file = $1", replacement.LogFile); err != nil {
		return err
	}
	if err := upsertPostgresCursor(ctx, tx, replacement); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (store *postgresStore) CopyCursor(ctx context.Context, lease OwnerLease, cursor Cursor) error {
	tx, err := beginPostgresTx(ctx, store.pool)
	if err != nil {
		return err
	}
	defer rollbackPostgresTx(tx)
	if err := verifyPostgresOwnerLease(ctx, tx, lease); err != nil {
		return err
	}
	if err := upsertPostgresCursor(ctx, tx, cursor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (store *postgresStore) Commit(ctx context.Context, lease OwnerLease, records []Record, cursor Cursor, retentionCutoff time.Time) error {
	tx, err := beginPostgresTx(ctx, store.pool)
	if err != nil {
		return err
	}
	defer rollbackPostgresTx(tx)
	if err := verifyPostgresOwnerLease(ctx, tx, lease); err != nil {
		return err
	}
	inserted, err := insertPostgresRecords(ctx, tx, records)
	if err != nil {
		return err
	}
	if err := incrementPostgresFacets(ctx, tx, inserted, retentionCutoff); err != nil {
		return err
	}
	if err := upsertPostgresCursor(ctx, tx, cursor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (store *postgresStore) Cleanup(ctx context.Context, lease OwnerLease, cutoff time.Time, batchSize int, maxBatches int) (CleanupResult, error) {
	return cleanupPostgres(ctx, store.pool, lease, cutoff, batchSize, maxBatches)
}

func (store *postgresStore) VerifyOwnerLease(ctx context.Context, lease OwnerLease) error {
	tx, err := beginPostgresTx(ctx, store.pool)
	if err != nil {
		return err
	}
	defer rollbackPostgresTx(tx)
	if err := verifyPostgresOwnerLease(ctx, tx, lease); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (store *postgresStore) WithOwnerLeaseFence(ctx context.Context, lease OwnerLease, callback func() error) error {
	tx, err := beginPostgresTx(ctx, store.pool)
	if err != nil {
		return err
	}
	defer rollbackPostgresTx(tx)
	if err := verifyPostgresOwnerLease(ctx, tx, lease); err != nil {
		return err
	}
	if err := callback(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (store *postgresStore) RuntimeRetentionDays(ctx context.Context, fallback int) (int, error) {
	var valueJSON string
	err := store.pool.QueryRow(ctx, "SELECT value_json FROM juhe_business.system_settings WHERE system_account_id = $1 AND key = $2", "sys_admin", "runtimeLogIndexRetentionDays").Scan(&valueJSON)
	if err == pgx.ErrNoRows {
		return fallback, nil
	}
	if err != nil {
		return 0, fmt.Errorf("读取 PostgreSQL runtimeLogIndexRetentionDays 失败: %w", err)
	}
	return parseRuntimeRetentionDays(valueJSON)
}

func (store *postgresStore) AcquireOwnerLease(ctx context.Context, ownerID string, duration time.Duration) (OwnerLease, bool, error) {
	return postgresAcquireOwnerLease(ctx, store.pool, ownerID, duration)
}

func (store *postgresStore) RenewOwnerLease(ctx context.Context, lease OwnerLease, duration time.Duration) (bool, error) {
	return postgresRenewOwnerLease(ctx, store.pool, lease, duration)
}

func (store *postgresStore) ReleaseOwnerLease(ctx context.Context, lease OwnerLease) error {
	now := time.Now().UTC()
	leaseCtx, cancel := ownerLeaseContext(ctx)
	defer cancel()
	tx, err := beginPostgresTx(leaseCtx, store.pool)
	if err != nil {
		return err
	}
	defer rollbackPostgresTx(tx)
	result, err := tx.Exec(leaseCtx, "UPDATE juhe_dataset.runtime_log_index_owner_leases SET owner_id = '', lease_until = $1, updated_at = $1 WHERE lease_key = $2 AND owner_id = $3 AND fence_token = $4", now, runtimeLogOwnerLeaseKey, lease.OwnerID, lease.FenceToken)
	if err != nil {
		return err
	}
	if err := requireOwnerLeaseMutation(result.RowsAffected()); err != nil {
		return err
	}
	return tx.Commit(leaseCtx)
}

func (store *postgresStore) CheckSchema(ctx context.Context) error {
	for _, table := range runtimeLogTables {
		var found string
		err := store.pool.QueryRow(ctx, "SELECT table_name FROM information_schema.tables WHERE table_schema = 'juhe_dataset' AND table_name = $1", table).Scan(&found)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: 缺少 PostgreSQL 表 juhe_dataset.%s", errPostgresRuntimeLogSchemaMissing, table)
			}
			return fmt.Errorf("缺少 PostgreSQL 表 juhe_dataset.%s: %w", table, err)
		}
		if err := checkPostgresColumns(ctx, store.pool, table); err != nil {
			return err
		}
	}
	for _, index := range runtimeLogPostgresIndexes {
		var found string
		err := store.pool.QueryRow(ctx, "SELECT indexname FROM pg_indexes WHERE schemaname = 'juhe_dataset' AND indexname = $1", index).Scan(&found)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: 缺少 PostgreSQL 运行日志索引 juhe_dataset.%s", errPostgresRuntimeLogSchemaMissing, index)
			}
			return fmt.Errorf("缺少 PostgreSQL 运行日志索引 juhe_dataset.%s: %w", index, err)
		}
	}
	return nil
}

func (store *postgresStore) Close() error {
	store.pool.Close()
	return nil
}

func parseRuntimeRetentionDays(valueJSON string) (int, error) {
	var value any
	if err := json.Unmarshal([]byte(valueJSON), &value); err != nil {
		return 0, fmt.Errorf("runtimeLogIndexRetentionDays 不是 JSON: %w", err)
	}
	parsed, ok := value.(float64)
	if !ok || parsed != float64(int(parsed)) || parsed < 1 || parsed > 90 {
		return 0, fmt.Errorf("runtimeLogIndexRetentionDays 必须是 1..90 的整数")
	}
	return int(parsed), nil
}

func postgresAcquireOwnerLease(ctx context.Context, pool *pgxpool.Pool, ownerID string, duration time.Duration) (OwnerLease, bool, error) {
	now := time.Now().UTC()
	leaseUntil := now.Add(duration)
	var token int64
	leaseCtx, cancel := ownerLeaseContext(ctx)
	defer cancel()
	tx, err := beginPostgresTx(leaseCtx, pool)
	if err != nil {
		return OwnerLease{}, false, err
	}
	defer rollbackPostgresTx(tx)
	err = tx.QueryRow(leaseCtx, `
    INSERT INTO juhe_dataset.runtime_log_index_owner_leases (lease_key, owner_id, fence_token, lease_until, updated_at)
    VALUES ($1, $2, 1, $3, $4)
    ON CONFLICT(lease_key) DO UPDATE SET
      owner_id = excluded.owner_id,
      fence_token = juhe_dataset.runtime_log_index_owner_leases.fence_token + 1,
      lease_until = excluded.lease_until,
      updated_at = excluded.updated_at
    WHERE juhe_dataset.runtime_log_index_owner_leases.lease_until <= $5
    RETURNING fence_token
	`, runtimeLogOwnerLeaseKey, ownerID, leaseUntil, now, now).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		return OwnerLease{}, false, nil
	}
	if err != nil {
		return OwnerLease{}, false, err
	}
	if err := tx.Commit(leaseCtx); err != nil {
		return OwnerLease{}, false, err
	}
	return OwnerLease{OwnerID: ownerID, FenceToken: token}, true, nil
}

func postgresRenewOwnerLease(ctx context.Context, pool *pgxpool.Pool, lease OwnerLease, duration time.Duration) (bool, error) {
	now := time.Now().UTC()
	leaseUntil := now.Add(duration)
	leaseCtx, cancel := ownerLeaseContext(ctx)
	defer cancel()
	tx, err := beginPostgresTx(leaseCtx, pool)
	if err != nil {
		return false, err
	}
	defer rollbackPostgresTx(tx)
	result, err := tx.Exec(leaseCtx, `
    UPDATE juhe_dataset.runtime_log_index_owner_leases
    SET lease_until = $1, updated_at = $2
    WHERE lease_key = $3 AND owner_id = $4 AND fence_token = $5 AND lease_until > $6
  `, leaseUntil, now, runtimeLogOwnerLeaseKey, lease.OwnerID, lease.FenceToken, now)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(leaseCtx); err != nil {
		return false, err
	}
	return result.RowsAffected() == 1, nil
}

func verifyPostgresOwnerLease(ctx context.Context, tx pgx.Tx, lease OwnerLease) error {
	now := time.Now().UTC()
	var token int64
	err := tx.QueryRow(ctx, `
    SELECT fence_token
    FROM juhe_dataset.runtime_log_index_owner_leases
    WHERE lease_key = $1 AND owner_id = $2 AND fence_token = $3 AND lease_until > $4
    FOR UPDATE
  `, runtimeLogOwnerLeaseKey, lease.OwnerID, lease.FenceToken, now).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrOwnerLeaseLost
	}
	if err != nil {
		return err
	}
	return nil
}

func requireOwnerLeaseMutation(affected int64) error {
	if affected != 1 {
		return ErrOwnerLeaseLost
	}
	return nil
}

var runtimeLogTables = []string{
	"runtime_logs",
	"runtime_log_file_cursors",
	"runtime_log_index_owner_leases",
	"runtime_log_facet_summary",
	"runtime_log_level_facets",
	"runtime_log_event_facets",
}

var runtimeLogColumns = map[string][]string{
	"runtime_logs": {
		"id", "log_file", "log_offset", "line_number", "time", "level", "trace_id", "event", "message", "error_message", "raw_json", "created_at",
	},
	"runtime_log_file_cursors": {
		"log_file", "file_identity", "cursor_offset", "line_number", "file_size", "truncation_generation", "file_mtime_ms", "last_read_at", "last_error_message", "created_at", "updated_at",
	},
	"runtime_log_index_owner_leases": {
		"lease_key", "owner_id", "fence_token", "lease_until", "updated_at",
	},
	"runtime_log_facet_summary": {
		"bucket_key", "total_count", "earliest_time", "latest_time", "updated_at",
	},
	"runtime_log_level_facets": {
		"bucket_key", "level", "count", "updated_at",
	},
	"runtime_log_event_facets": {
		"bucket_key", "event", "count", "latest_time", "updated_at",
	},
}

var runtimeLogPostgresIndexes = []string{
	"idx_runtime_logs_time",
	"idx_runtime_logs_trace_id_time",
	"idx_runtime_log_file_cursors_updated",
	"idx_runtime_log_facet_summary_latest",
	"idx_runtime_log_event_facets_latest",
	"idx_runtime_logs_trace_c_time",
}

var runtimeLogPostgresInstantColumns = map[string]map[string]bool{
	"runtime_logs":                   {"time": true, "created_at": true},
	"runtime_log_file_cursors":       {"last_read_at": true, "created_at": true, "updated_at": true},
	"runtime_log_index_owner_leases": {"lease_until": true, "updated_at": true},
	"runtime_log_facet_summary":      {"earliest_time": true, "latest_time": true, "updated_at": true},
	"runtime_log_level_facets":       {"updated_at": true},
	"runtime_log_event_facets":       {"latest_time": true, "updated_at": true},
}

func checkPostgresColumns(ctx context.Context, pool *pgxpool.Pool, table string) error {
	rows, err := pool.Query(ctx, "SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = 'juhe_dataset' AND table_name = $1", table)
	if err != nil {
		return fmt.Errorf("读取 PostgreSQL 表 juhe_dataset.%s 的字段失败: %w", table, err)
	}
	defer rows.Close()
	found := map[string]string{}
	for rows.Next() {
		var name, dataType string
		if err := rows.Scan(&name, &dataType); err != nil {
			return fmt.Errorf("读取 PostgreSQL 表 juhe_dataset.%s 的字段失败: %w", table, err)
		}
		found[name] = dataType
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("读取 PostgreSQL 表 juhe_dataset.%s 的字段失败: %w", table, err)
	}
	for _, column := range runtimeLogColumns[table] {
		if _, ok := found[column]; !ok {
			return fmt.Errorf("%w: PostgreSQL 表 juhe_dataset.%s 缺少运行日志字段 %s", errPostgresRuntimeLogSchemaMissing, table, column)
		}
	}
	for column := range runtimeLogPostgresInstantColumns[table] {
		if found[column] != "timestamp with time zone" {
			return fmt.Errorf("PostgreSQL 表 juhe_dataset.%s.%s 必须是 timestamptz，实际为 %s；请先离线迁移", table, column, found[column])
		}
	}
	return nil
}

func shouldBootstrapPostgresRuntimeLogSchema(err error) bool {
	return errors.Is(err, errPostgresRuntimeLogSchemaMissing)
}

func postgresRuntimeLogSchemaBootstrapRequired(checkErr error) (bool, error) {
	if checkErr == nil {
		// 正常启动只做元数据校验。对已有的大表重复执行
		// CREATE INDEX IF NOT EXISTS 仍可能等待 DDL 锁，拖慢 worker 启动。
		return false, nil
	}
	if shouldBootstrapPostgresRuntimeLogSchema(checkErr) {
		return true, nil
	}
	return false, fmt.Errorf("检查 PostgreSQL 运行日志 schema 失败，拒绝执行初始化 DDL: %w", checkErr)
}

const postgresCursorSelect = `SELECT log_file, COALESCE(file_identity, ''), cursor_offset, line_number, file_size, truncation_generation, COALESCE(file_mtime_ms, 0), COALESCE(last_read_at::text, ''), COALESCE(last_error_message, ''), created_at::text, updated_at::text FROM juhe_dataset.runtime_log_file_cursors`

func scanPostgresCursor(row pgx.Row) (*Cursor, error) {
	var cursor Cursor
	err := row.Scan(&cursor.LogFile, &cursor.FileIdentity, &cursor.CursorOffset, &cursor.LineNumber, &cursor.FileSize, &cursor.TruncationGeneration, &cursor.FileMtimeMs, &cursor.LastReadAt, &cursor.LastErrorMessage, &cursor.CreatedAt, &cursor.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &cursor, err
}

type eventFacet struct {
	count      int
	latestTime string
}

func retainedFacetRows(rows []facetRow, cutoff time.Time) []facetRow {
	cutoffText := nodeISO(cutoff)
	retained := make([]facetRow, 0, len(rows))
	for _, row := range rows {
		if row.Time >= cutoffText {
			retained = append(retained, row)
		}
	}
	return retained
}

func rangeTimes(rows []facetRow) (string, string) {
	earliest, latest := rows[0].Time, rows[0].Time
	for _, row := range rows[1:] {
		if row.Time < earliest {
			earliest = row.Time
		}
		if row.Time > latest {
			latest = row.Time
		}
	}
	return earliest, latest
}

func facetLevelCounts(rows []facetRow) map[string]int {
	counts := map[string]int{}
	for _, row := range rows {
		counts[row.Level]++
	}
	return counts
}

func facetEventCounts(rows []facetRow) map[string]eventFacet {
	counts := map[string]eventFacet{}
	for _, row := range rows {
		if strings.TrimSpace(row.Event) == "" {
			continue
		}
		current := counts[row.Event]
		current.count++
		if row.Time > current.latestTime {
			current.latestTime = row.Time
		}
		counts[row.Event] = current
	}
	return counts
}

func facetRowsFrom(rows []facetRow, earliestCountedTime string) []facetRow {
	if strings.TrimSpace(earliestCountedTime) == "" {
		return rows
	}
	counted := make([]facetRow, 0, len(rows))
	for _, row := range rows {
		if row.Time >= earliestCountedTime {
			counted = append(counted, row)
		}
	}
	return counted
}

func normalizeCursor(cursor Cursor) (Cursor, error) {
	now := nowISO()
	if cursor.CreatedAt == "" {
		cursor.CreatedAt = now
	} else {
		var err error
		cursor.CreatedAt, err = normalizeNodeTimestamp(cursor.CreatedAt)
		if err != nil {
			return Cursor{}, fmt.Errorf("runtime log cursor createdAt invalid: %w", err)
		}
	}
	if cursor.LastReadAt == "" {
		cursor.LastReadAt = now
	} else {
		var err error
		cursor.LastReadAt, err = normalizeNodeTimestamp(cursor.LastReadAt)
		if err != nil {
			return Cursor{}, fmt.Errorf("runtime log cursor lastReadAt invalid: %w", err)
		}
	}
	cursor.UpdatedAt = now
	return cursor, nil
}

func normalizeRecord(record Record) (Record, error) {
	var err error
	record.Time, err = normalizeNodeTimestamp(record.Time)
	if err != nil {
		return Record{}, fmt.Errorf("runtime log time invalid: %w", err)
	}
	record.CreatedAt, err = normalizeNodeTimestamp(record.CreatedAt)
	if err != nil {
		return Record{}, fmt.Errorf("runtime log createdAt invalid: %w", err)
	}
	record.Level = strings.ToLower(strings.TrimSpace(record.Level))
	if record.Level == "" {
		record.Level = "info"
	}
	return record, nil
}

func normalizeNodeTimestamp(value string) (string, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return "", fmt.Errorf("时间不能为空")
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		// 旧 Node 日志使用空格分隔日期时间，并将整点时区写成 +HH。
		// 仅对这个已知格式做显式规范化，其他非法值仍然拒绝。
		legacy := normalizeLegacyNodeTimestamp(text)
		if legacy != text {
			parsed, err = time.Parse(time.RFC3339Nano, legacy)
		}
	}
	if err != nil {
		return "", fmt.Errorf("必须是 RFC3339 瞬时值: %w", err)
	}
	return nodeISO(parsed), nil
}

func normalizeLegacyNodeTimestamp(text string) string {
	if len(text) < len("2006-01-02 15:04:05+00") || text[10] != ' ' {
		return text
	}
	legacy := text[:10] + "T" + text[11:]
	if len(legacy) >= 3 {
		suffix := legacy[len(legacy)-3:]
		if (suffix[0] == '+' || suffix[0] == '-') && isASCIIDigit(suffix[1]) && isASCIIDigit(suffix[2]) {
			legacy += ":00"
		}
	}
	return legacy
}

func isASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func nullable(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nowISO() string {
	return nodeISO(time.Now().UTC())
}

func nodeISO(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

// postgresNodeInstantText 把 timestamptz 列渲染为与 nodeISO 完全一致的毫秒
// 文本（"2006-01-02T15:04:05.000Z"）。facet 链所有行时间与汇总边界的读出
// 统一经该表达式归一：PG 默认 timestamptz::text 是空格分隔 + "+00" 形态
// （"2026-10-24 04:00:00+00"），与 nodeISO 做字典序比较时空格(0x20) < 'T'，
// 会把与 cutoff 同一自然日的记录系统性判早、facet 计数少计（W23）。归一后
// 同格式定长字符串的字典序恒等于时间序。
func postgresNodeInstantText(column string) string {
	return fmt.Sprintf(`to_char(%s AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')`, column)
}

func insertPostgresRecords(ctx context.Context, tx pgx.Tx, records []Record) ([]facetRow, error) {
	inserted := make([]facetRow, 0, len(records))
	for start := 0; start < len(records); start += postgresInsertRowsPerStatement {
		end := minInt(start+postgresInsertRowsPerStatement, len(records))
		values := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*12)
		for _, source := range records[start:end] {
			record, err := normalizeRecord(source)
			if err != nil {
				return nil, err
			}
			values = append(values, "("+dollarMarks(len(args)+1, 12)+")")
			args = append(args,
				record.ID,
				nullable(record.LogFile),
				record.LogOffset,
				record.LineNumber,
				record.Time,
				record.Level,
				nullable(record.TraceID),
				nullable(record.Event),
				nullable(record.Message),
				nullable(record.ErrorMessage),
				record.RawJSON,
				record.CreatedAt,
			)
		}
		rows, err := tx.Query(ctx, `INSERT INTO juhe_dataset.runtime_logs (id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at) VALUES `+strings.Join(values, ",")+` ON CONFLICT(id) DO NOTHING RETURNING `+postgresNodeInstantText("time")+`, level, COALESCE(event, '')`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var row facetRow
			if err := rows.Scan(&row.Time, &row.Level, &row.Event); err != nil {
				rows.Close()
				return nil, err
			}
			inserted = append(inserted, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return inserted, nil
}

func upsertPostgresCursor(ctx context.Context, tx pgx.Tx, cursor Cursor) error {
	var err error
	cursor, err = normalizeCursor(cursor)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO juhe_dataset.runtime_log_file_cursors (log_file, file_identity, cursor_offset, line_number, file_size, truncation_generation, file_mtime_ms, last_read_at, last_error_message, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT(log_file) DO UPDATE SET file_identity = excluded.file_identity, cursor_offset = excluded.cursor_offset, line_number = excluded.line_number, file_size = excluded.file_size, truncation_generation = excluded.truncation_generation, file_mtime_ms = excluded.file_mtime_ms, last_read_at = excluded.last_read_at, last_error_message = excluded.last_error_message, updated_at = excluded.updated_at`, cursor.LogFile, nullable(cursor.FileIdentity), cursor.CursorOffset, cursor.LineNumber, cursor.FileSize, cursor.TruncationGeneration, cursor.FileMtimeMs, nullable(cursor.LastReadAt), nullable(cursor.LastErrorMessage), cursor.CreatedAt, cursor.UpdatedAt)
	return err
}

func incrementPostgresFacets(ctx context.Context, tx pgx.Tx, rows []facetRow, cutoff time.Time) error {
	retained := retainedFacetRows(rows, cutoff)
	if len(retained) == 0 {
		return nil
	}
	now := nowISO()
	earliest, latest := rangeTimes(retained)
	if _, err := tx.Exec(ctx, `INSERT INTO juhe_dataset.runtime_log_facet_summary (bucket_key, total_count, earliest_time, latest_time, updated_at) VALUES ($1, $2, $3, $4, $5) ON CONFLICT(bucket_key) DO UPDATE SET total_count = juhe_dataset.runtime_log_facet_summary.total_count + excluded.total_count, earliest_time = CASE WHEN juhe_dataset.runtime_log_facet_summary.earliest_time IS NULL OR excluded.earliest_time < juhe_dataset.runtime_log_facet_summary.earliest_time THEN excluded.earliest_time ELSE juhe_dataset.runtime_log_facet_summary.earliest_time END, latest_time = CASE WHEN juhe_dataset.runtime_log_facet_summary.latest_time IS NULL OR excluded.latest_time > juhe_dataset.runtime_log_facet_summary.latest_time THEN excluded.latest_time ELSE juhe_dataset.runtime_log_facet_summary.latest_time END, updated_at = excluded.updated_at`, facetBucketKey, len(retained), earliest, latest, now); err != nil {
		return err
	}
	for level, count := range facetLevelCounts(retained) {
		if _, err := tx.Exec(ctx, `INSERT INTO juhe_dataset.runtime_log_level_facets (bucket_key, level, count, updated_at) VALUES ($1, $2, $3, $4) ON CONFLICT(bucket_key, level) DO UPDATE SET count = juhe_dataset.runtime_log_level_facets.count + excluded.count, updated_at = excluded.updated_at`, facetBucketKey, level, count, now); err != nil {
			return err
		}
	}
	for event, summary := range facetEventCounts(retained) {
		if _, err := tx.Exec(ctx, `INSERT INTO juhe_dataset.runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at) VALUES ($1, $2, $3, $4, $5) ON CONFLICT(bucket_key, event) DO UPDATE SET count = juhe_dataset.runtime_log_event_facets.count + excluded.count, latest_time = CASE WHEN juhe_dataset.runtime_log_event_facets.latest_time IS NULL OR excluded.latest_time > juhe_dataset.runtime_log_event_facets.latest_time THEN excluded.latest_time ELSE juhe_dataset.runtime_log_event_facets.latest_time END, updated_at = excluded.updated_at`, facetBucketKey, event, summary.count, summary.latestTime, now); err != nil {
			return err
		}
	}
	return nil
}

func cleanupPostgres(ctx context.Context, pool *pgxpool.Pool, lease OwnerLease, cutoff time.Time, batchSize int, maxBatches int) (CleanupResult, error) {
	result := CleanupResult{}
	batchLimit := minInt(maxInt(batchSize, 1), postgresCleanupRowsPerBatch)
	for batch := 0; batch < maxBatches; batch++ {
		tx, err := beginPostgresTx(ctx, pool)
		if err != nil {
			return result, err
		}
		if err := verifyPostgresOwnerLease(ctx, tx, lease); err != nil {
			rollbackPostgresTx(tx)
			return result, err
		}
		rows, err := tx.Query(ctx, "SELECT id, "+postgresNodeInstantText("time")+", level, COALESCE(event, '') FROM juhe_dataset.runtime_logs WHERE time < $1 ORDER BY time ASC, id ASC LIMIT $2", cutoff, batchLimit)
		if err != nil {
			rollbackPostgresTx(tx)
			return result, err
		}
		ids, deleted, err := scanPostgresFacetRows(rows)
		rows.Close()
		if err != nil {
			rollbackPostgresTx(tx)
			return result, err
		}
		if len(ids) == 0 {
			rollbackPostgresTx(tx)
			break
		}
		args := stringArgs(ids)
		if _, err := tx.Exec(ctx, "DELETE FROM juhe_dataset.runtime_logs WHERE id IN ("+dollarMarks(1, len(ids))+")", args...); err != nil {
			rollbackPostgresTx(tx)
			return result, err
		}
		if err := decrementPostgresFacets(ctx, tx, deleted, cutoff); err != nil {
			rollbackPostgresTx(tx)
			return result, err
		}
		if err := tx.Commit(ctx); err != nil {
			return result, err
		}
		result.RuntimeLogs += int64(len(ids))
		if len(ids) < batchLimit {
			break
		}
	}
	for batch := 0; batch < maxBatches; batch++ {
		tx, err := beginPostgresTx(ctx, pool)
		if err != nil {
			return result, err
		}
		if err := verifyPostgresOwnerLease(ctx, tx, lease); err != nil {
			rollbackPostgresTx(tx)
			return result, err
		}
		query := `DELETE FROM juhe_dataset.runtime_log_file_cursors WHERE ctid IN (SELECT ctid FROM juhe_dataset.runtime_log_file_cursors WHERE updated_at < $1 AND cursor_offset >= file_size AND last_error_message IS NULL ORDER BY updated_at ASC, ctid ASC LIMIT $2)`
		deleted, err := tx.Exec(ctx, query, cutoff, batchLimit)
		if err != nil {
			rollbackPostgresTx(tx)
			return result, err
		}
		count := deleted.RowsAffected()
		if err := tx.Commit(ctx); err != nil {
			return result, err
		}
		result.RuntimeLogCursors += count
		if count < int64(batchLimit) {
			break
		}
	}
	return result, nil
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func scanPostgresFacetRows(rows pgx.Rows) ([]string, []facetRow, error) {
	ids := []string{}
	facets := []facetRow{}
	for rows.Next() {
		var id string
		var row facetRow
		if err := rows.Scan(&id, &row.Time, &row.Level, &row.Event); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		facets = append(facets, row)
	}
	return ids, facets, rows.Err()
}

func decrementPostgresFacets(ctx context.Context, tx pgx.Tx, rows []facetRow, cutoff time.Time) error {
	if len(rows) == 0 {
		return nil
	}
	now := nowISO()
	var countedFrom, earliest, latest string
	if err := tx.QueryRow(ctx, "SELECT COALESCE((SELECT "+postgresNodeInstantText("earliest_time")+" FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1), '')", facetBucketKey).Scan(&countedFrom); err != nil {
		return err
	}
	counted := facetRowsFrom(rows, countedFrom)
	if len(counted) == 0 {
		return nil
	}
	if err := tx.QueryRow(ctx, "SELECT COALESCE((SELECT "+postgresNodeInstantText("time")+" FROM juhe_dataset.runtime_logs WHERE time >= $1 ORDER BY time ASC, id ASC LIMIT 1), '')", cutoff).Scan(&earliest); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, "SELECT COALESCE((SELECT "+postgresNodeInstantText("time")+" FROM juhe_dataset.runtime_logs WHERE time >= $1 ORDER BY time DESC, id DESC LIMIT 1), '')", cutoff).Scan(&latest); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE juhe_dataset.runtime_log_facet_summary SET total_count = GREATEST(0, total_count - $1), earliest_time = $2, latest_time = $3, updated_at = $4 WHERE bucket_key = $5", len(counted), nullable(earliest), nullable(latest), now, facetBucketKey); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1 AND total_count <= 0", facetBucketKey); err != nil {
		return err
	}
	for level, count := range facetLevelCounts(counted) {
		if _, err := tx.Exec(ctx, "UPDATE juhe_dataset.runtime_log_level_facets SET count = GREATEST(0, count - $1), updated_at = $2 WHERE bucket_key = $3 AND level = $4", count, now, facetBucketKey, level); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, "DELETE FROM juhe_dataset.runtime_log_level_facets WHERE bucket_key = $1 AND count <= 0", facetBucketKey); err != nil {
		return err
	}
	for event, summary := range facetEventCounts(counted) {
		if _, err := tx.Exec(ctx, "UPDATE juhe_dataset.runtime_log_event_facets SET count = GREATEST(0, count - $1), updated_at = $2 WHERE bucket_key = $3 AND event = $4", summary.count, now, facetBucketKey, event); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, "DELETE FROM juhe_dataset.runtime_log_event_facets WHERE bucket_key = $1 AND count <= 0", facetBucketKey)
	return err
}

func questionMarks(count int) string {
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

func dollarMarks(start int, count int) string {
	values := make([]string, 0, count)
	for index := 0; index < count; index++ {
		values = append(values, fmt.Sprintf("$%d", start+index))
	}
	return strings.Join(values, ",")
}

func stringArgs(values []string) []any {
	args := make([]any, 0, len(values))
	for _, value := range values {
		args = append(args, value)
	}
	return args
}
