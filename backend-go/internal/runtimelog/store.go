package runtimelog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "modernc.org/sqlite"
)

const (
	facetBucketKey                 = "current"
	runtimeLogOwnerLeaseKey        = "runtime-log-index-retention"
	sqliteBusyTimeoutMs            = 5000
	postgresInsertRowsPerStatement = 5000
	postgresCleanupRowsPerBatch    = 5000
)

type facetRow struct {
	Time  string
	Level string
	Event string
}

type sqliteStore struct {
	db         *sql.DB
	businessDB *sql.DB
	writeMu    sync.Mutex
}

type postgresStore struct {
	pool *pgxpool.Pool
}

func OpenStore(ctx context.Context, config Config) (Store, error) {
	switch config.Mode {
	case ModeSQLite:
		db, err := sql.Open("sqlite", config.DatasetPath)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", sqliteBusyTimeoutMs)); err != nil {
			db.Close()
			return nil, fmt.Errorf("设置 SQLite busy_timeout 失败: %w", err)
		}
		if err := db.PingContext(ctx); err != nil {
			db.Close()
			return nil, err
		}
		businessDB, err := openSQLiteReadOnly(ctx, config.BusinessPath)
		if err != nil {
			db.Close()
			return nil, err
		}
		return &sqliteStore{db: db, businessDB: businessDB}, nil
	case ModePostgres:
		pool, err := pgxpool.New(ctx, config.PostgresURL)
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
	case *sqliteStore:
		value.writeMu.Lock()
		defer value.writeMu.Unlock()
		if _, err := value.db.ExecContext(ctx, sqliteSchema); err != nil {
			return err
		}
		return ensureSQLiteFenceTokenColumn(ctx, value.db)
	case *postgresStore:
		for _, statement := range strings.Split(postgresSchema, ";") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if _, err := value.pool.Exec(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("未知运行日志 Store")
	}
}

func (store *sqliteStore) FindCursor(ctx context.Context, logFile string) (*Cursor, error) {
	return scanSQLiteCursor(store.db.QueryRowContext(ctx, sqliteCursorSelect+" WHERE log_file = ?", logFile))
}

func (store *sqliteStore) FindCursorByIdentity(ctx context.Context, identity string) (*Cursor, error) {
	return scanSQLiteCursor(store.db.QueryRowContext(ctx, sqliteCursorSelect+" WHERE file_identity = ? ORDER BY updated_at DESC LIMIT 1", identity))
}

func (store *sqliteStore) ReplaceCursor(ctx context.Context, lease OwnerLease, displaced *Cursor, replacement Cursor) error {
	store.writeMu.Lock()
	defer store.writeMu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := verifySQLiteOwnerLease(ctx, tx, lease); err != nil {
		return err
	}
	if displaced != nil {
		if err := upsertSQLiteCursor(ctx, tx, *displaced); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM runtime_log_file_cursors WHERE log_file = ?", replacement.LogFile); err != nil {
		return err
	}
	if err := upsertSQLiteCursor(ctx, tx, replacement); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *sqliteStore) CopyCursor(ctx context.Context, lease OwnerLease, cursor Cursor) error {
	store.writeMu.Lock()
	defer store.writeMu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := verifySQLiteOwnerLease(ctx, tx, lease); err != nil {
		return err
	}
	if err := upsertSQLiteCursor(ctx, tx, cursor); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *sqliteStore) Commit(ctx context.Context, lease OwnerLease, records []Record, cursor Cursor, retentionCutoff time.Time) error {
	store.writeMu.Lock()
	defer store.writeMu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := verifySQLiteOwnerLease(ctx, tx, lease); err != nil {
		return err
	}
	inserted, err := insertSQLiteRecords(ctx, tx, records)
	if err != nil {
		return err
	}
	if err := incrementSQLiteFacets(ctx, tx, inserted, retentionCutoff); err != nil {
		return err
	}
	if err := upsertSQLiteCursor(ctx, tx, cursor); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *sqliteStore) Cleanup(ctx context.Context, lease OwnerLease, cutoff time.Time, batchSize int, maxBatches int) (CleanupResult, error) {
	store.writeMu.Lock()
	defer store.writeMu.Unlock()
	return cleanupSQLite(ctx, store.db, lease, cutoff, batchSize, maxBatches)
}

func (store *sqliteStore) VerifyOwnerLease(ctx context.Context, lease OwnerLease) error {
	store.writeMu.Lock()
	defer store.writeMu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := verifySQLiteOwnerLease(ctx, tx, lease); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *sqliteStore) RuntimeRetentionDays(ctx context.Context, fallback int) (int, error) {
	if store.businessDB == nil {
		return fallback, nil
	}
	var valueJSON string
	err := store.businessDB.QueryRowContext(ctx, "SELECT value_json FROM system_settings WHERE system_account_id = ? AND key = ?", "sys_admin", "runtimeLogIndexRetentionDays").Scan(&valueJSON)
	if err == sql.ErrNoRows {
		return fallback, nil
	}
	if err != nil {
		return 0, fmt.Errorf("读取 SQLite runtimeLogIndexRetentionDays 失败: %w", err)
	}
	return parseRuntimeRetentionDays(valueJSON)
}

func (store *sqliteStore) AcquireOwnerLease(ctx context.Context, ownerID string, duration time.Duration) (OwnerLease, bool, error) {
	store.writeMu.Lock()
	defer store.writeMu.Unlock()
	return sqliteAcquireOwnerLease(ctx, store.db, ownerID, duration)
}

func (store *sqliteStore) RenewOwnerLease(ctx context.Context, lease OwnerLease, duration time.Duration) (bool, error) {
	store.writeMu.Lock()
	defer store.writeMu.Unlock()
	return sqliteRenewOwnerLease(ctx, store.db, lease, duration)
}

func (store *sqliteStore) ReleaseOwnerLease(ctx context.Context, lease OwnerLease) error {
	store.writeMu.Lock()
	defer store.writeMu.Unlock()
	result, err := store.db.ExecContext(ctx, "DELETE FROM runtime_log_index_owner_leases WHERE lease_key = ? AND owner_id = ? AND fence_token = ?", runtimeLogOwnerLeaseKey, lease.OwnerID, lease.FenceToken)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	return requireOwnerLeaseMutation(affected)
}

func (store *sqliteStore) CheckSchema(ctx context.Context) error {
	for _, table := range runtimeLogTables {
		var found string
		err := store.db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&found)
		if err != nil {
			return fmt.Errorf("缺少 SQLite 表 %s: %w", table, err)
		}
		if err := checkSQLiteColumns(ctx, store.db, table); err != nil {
			return err
		}
	}
	for _, index := range runtimeLogSQLiteIndexes {
		var found string
		err := store.db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?", index).Scan(&found)
		if err != nil {
			return fmt.Errorf("缺少 SQLite 运行日志索引 %s: %w", index, err)
		}
	}
	return nil
}

func (store *sqliteStore) Close() error {
	err := store.db.Close()
	if store.businessDB != nil {
		if businessErr := store.businessDB.Close(); err == nil {
			err = businessErr
		}
	}
	return err
}

func (store *postgresStore) FindCursor(ctx context.Context, logFile string) (*Cursor, error) {
	return scanPostgresCursor(store.pool.QueryRow(ctx, postgresCursorSelect+" WHERE log_file = $1", logFile))
}

func (store *postgresStore) FindCursorByIdentity(ctx context.Context, identity string) (*Cursor, error) {
	return scanPostgresCursor(store.pool.QueryRow(ctx, postgresCursorSelect+" WHERE file_identity = $1 ORDER BY updated_at DESC LIMIT 1", identity))
}

func (store *postgresStore) ReplaceCursor(ctx context.Context, lease OwnerLease, displaced *Cursor, replacement Cursor) error {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
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
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := verifyPostgresOwnerLease(ctx, tx, lease); err != nil {
		return err
	}
	if err := upsertPostgresCursor(ctx, tx, cursor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (store *postgresStore) Commit(ctx context.Context, lease OwnerLease, records []Record, cursor Cursor, retentionCutoff time.Time) error {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
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
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := verifyPostgresOwnerLease(ctx, tx, lease); err != nil {
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
	result, err := store.pool.Exec(ctx, "DELETE FROM juhe_dataset.runtime_log_index_owner_leases WHERE lease_key = $1 AND owner_id = $2 AND fence_token = $3", runtimeLogOwnerLeaseKey, lease.OwnerID, lease.FenceToken)
	if err != nil {
		return err
	}
	return requireOwnerLeaseMutation(result.RowsAffected())
}

func (store *postgresStore) CheckSchema(ctx context.Context) error {
	for _, table := range runtimeLogTables {
		var found string
		err := store.pool.QueryRow(ctx, "SELECT table_name FROM information_schema.tables WHERE table_schema = 'juhe_dataset' AND table_name = $1", table).Scan(&found)
		if err != nil {
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
			return fmt.Errorf("缺少 PostgreSQL 运行日志索引 juhe_dataset.%s: %w", index, err)
		}
	}
	return nil
}

func (store *postgresStore) Close() error {
	store.pool.Close()
	return nil
}

func openSQLiteReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("读取运行日志保留设置前无法访问 SQLite 业务库: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("设置 SQLite 业务库只读连接失败: %w", err)
	}
	return db, nil
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

func ensureSQLiteFenceTokenColumn(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(runtime_log_index_owner_leases)")
	if err != nil {
		return fmt.Errorf("读取 SQLite owner lease 字段失败: %w", err)
	}
	found := false
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("读取 SQLite owner lease 字段失败: %w", err)
		}
		if name == "fence_token" {
			found = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("读取 SQLite owner lease 字段失败: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("关闭 SQLite owner lease 字段读取失败: %w", err)
	}
	if found {
		return nil
	}
	if _, err := db.ExecContext(ctx, "ALTER TABLE runtime_log_index_owner_leases ADD COLUMN fence_token INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("迁移 SQLite owner lease fence_token 失败: %w", err)
	}
	return nil
}

func sqliteAcquireOwnerLease(ctx context.Context, db *sql.DB, ownerID string, duration time.Duration) (OwnerLease, bool, error) {
	now := time.Now().UTC()
	nowText := nodeISO(now)
	leaseUntil := nodeISO(now.Add(duration))
	var token int64
	err := db.QueryRowContext(ctx, `
    INSERT INTO runtime_log_index_owner_leases (lease_key, owner_id, fence_token, lease_until, updated_at)
    VALUES (?, ?, 1, ?, ?)
    ON CONFLICT(lease_key) DO UPDATE SET
      owner_id = excluded.owner_id,
      fence_token = runtime_log_index_owner_leases.fence_token + 1,
      lease_until = excluded.lease_until,
      updated_at = excluded.updated_at
    WHERE runtime_log_index_owner_leases.lease_until <= ?
    RETURNING fence_token
  `, runtimeLogOwnerLeaseKey, ownerID, leaseUntil, nowText, nowText).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerLease{}, false, nil
	}
	if err != nil {
		return OwnerLease{}, false, err
	}
	return OwnerLease{OwnerID: ownerID, FenceToken: token}, true, nil
}

func sqliteRenewOwnerLease(ctx context.Context, db *sql.DB, lease OwnerLease, duration time.Duration) (bool, error) {
	now := time.Now().UTC()
	nowText := nodeISO(now)
	leaseUntil := nodeISO(now.Add(duration))
	result, err := db.ExecContext(ctx, `
    UPDATE runtime_log_index_owner_leases
    SET lease_until = ?, updated_at = ?
    WHERE lease_key = ? AND owner_id = ? AND fence_token = ? AND lease_until > ?
  `, leaseUntil, nowText, runtimeLogOwnerLeaseKey, lease.OwnerID, lease.FenceToken, nowText)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func postgresAcquireOwnerLease(ctx context.Context, pool *pgxpool.Pool, ownerID string, duration time.Duration) (OwnerLease, bool, error) {
	now := time.Now().UTC()
	nowText := nodeISO(now)
	leaseUntil := nodeISO(now.Add(duration))
	var token int64
	err := pool.QueryRow(ctx, `
    INSERT INTO juhe_dataset.runtime_log_index_owner_leases (lease_key, owner_id, fence_token, lease_until, updated_at)
    VALUES ($1, $2, 1, $3, $4)
    ON CONFLICT(lease_key) DO UPDATE SET
      owner_id = excluded.owner_id,
      fence_token = juhe_dataset.runtime_log_index_owner_leases.fence_token + 1,
      lease_until = excluded.lease_until,
      updated_at = excluded.updated_at
    WHERE juhe_dataset.runtime_log_index_owner_leases.lease_until <= $5
    RETURNING fence_token
  `, runtimeLogOwnerLeaseKey, ownerID, leaseUntil, nowText, nowText).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		return OwnerLease{}, false, nil
	}
	if err != nil {
		return OwnerLease{}, false, err
	}
	return OwnerLease{OwnerID: ownerID, FenceToken: token}, true, nil
}

func postgresRenewOwnerLease(ctx context.Context, pool *pgxpool.Pool, lease OwnerLease, duration time.Duration) (bool, error) {
	now := time.Now().UTC()
	nowText := nodeISO(now)
	leaseUntil := nodeISO(now.Add(duration))
	result, err := pool.Exec(ctx, `
    UPDATE juhe_dataset.runtime_log_index_owner_leases
    SET lease_until = $1, updated_at = $2
    WHERE lease_key = $3 AND owner_id = $4 AND fence_token = $5 AND lease_until > $6
  `, leaseUntil, nowText, runtimeLogOwnerLeaseKey, lease.OwnerID, lease.FenceToken, nowText)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() == 1, nil
}

func verifySQLiteOwnerLease(ctx context.Context, tx *sql.Tx, lease OwnerLease) error {
	nowText := nodeISO(time.Now().UTC())
	result, err := tx.ExecContext(ctx, `
    UPDATE runtime_log_index_owner_leases
    SET updated_at = updated_at
    WHERE lease_key = ? AND owner_id = ? AND fence_token = ? AND lease_until > ?
  `, runtimeLogOwnerLeaseKey, lease.OwnerID, lease.FenceToken, nowText)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	return requireOwnerLeaseMutation(affected)
}

func verifyPostgresOwnerLease(ctx context.Context, tx pgx.Tx, lease OwnerLease) error {
	nowText := nodeISO(time.Now().UTC())
	var token int64
	err := tx.QueryRow(ctx, `
    SELECT fence_token
    FROM juhe_dataset.runtime_log_index_owner_leases
    WHERE lease_key = $1 AND owner_id = $2 AND fence_token = $3 AND lease_until > $4
    FOR UPDATE
  `, runtimeLogOwnerLeaseKey, lease.OwnerID, lease.FenceToken, nowText).Scan(&token)
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

var runtimeLogSQLiteIndexes = []string{
	"idx_runtime_logs_time",
	"idx_runtime_logs_trace_id_time",
	"idx_runtime_log_file_cursors_updated",
	"idx_runtime_log_facet_summary_latest",
	"idx_runtime_log_event_facets_latest",
}

var runtimeLogPostgresIndexes = append(append([]string{}, runtimeLogSQLiteIndexes...), "idx_runtime_logs_trace_c_time")

func checkSQLiteColumns(ctx context.Context, db *sql.DB, table string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return fmt.Errorf("读取 SQLite 表 %s 的字段失败: %w", table, err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("读取 SQLite 表 %s 的字段失败: %w", table, err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("读取 SQLite 表 %s 的字段失败: %w", table, err)
	}
	for _, column := range runtimeLogColumns[table] {
		if !found[column] {
			return fmt.Errorf("SQLite 表 %s 缺少运行日志字段 %s", table, column)
		}
	}
	return nil
}

func checkPostgresColumns(ctx context.Context, pool *pgxpool.Pool, table string) error {
	rows, err := pool.Query(ctx, "SELECT column_name FROM information_schema.columns WHERE table_schema = 'juhe_dataset' AND table_name = $1", table)
	if err != nil {
		return fmt.Errorf("读取 PostgreSQL 表 juhe_dataset.%s 的字段失败: %w", table, err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("读取 PostgreSQL 表 juhe_dataset.%s 的字段失败: %w", table, err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("读取 PostgreSQL 表 juhe_dataset.%s 的字段失败: %w", table, err)
	}
	for _, column := range runtimeLogColumns[table] {
		if !found[column] {
			return fmt.Errorf("PostgreSQL 表 juhe_dataset.%s 缺少运行日志字段 %s", table, column)
		}
	}
	return nil
}

const sqliteCursorSelect = `SELECT log_file, COALESCE(file_identity, ''), cursor_offset, line_number, file_size, truncation_generation, COALESCE(file_mtime_ms, 0), COALESCE(last_read_at, ''), COALESCE(last_error_message, ''), created_at, updated_at FROM runtime_log_file_cursors`
const postgresCursorSelect = `SELECT log_file, COALESCE(file_identity, ''), cursor_offset, line_number, file_size, truncation_generation, COALESCE(file_mtime_ms, 0), COALESCE(last_read_at::text, ''), COALESCE(last_error_message, ''), created_at::text, updated_at::text FROM juhe_dataset.runtime_log_file_cursors`

func scanSQLiteCursor(row *sql.Row) (*Cursor, error) {
	var cursor Cursor
	err := row.Scan(&cursor.LogFile, &cursor.FileIdentity, &cursor.CursorOffset, &cursor.LineNumber, &cursor.FileSize, &cursor.TruncationGeneration, &cursor.FileMtimeMs, &cursor.LastReadAt, &cursor.LastErrorMessage, &cursor.CreatedAt, &cursor.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &cursor, err
}

func scanPostgresCursor(row pgx.Row) (*Cursor, error) {
	var cursor Cursor
	err := row.Scan(&cursor.LogFile, &cursor.FileIdentity, &cursor.CursorOffset, &cursor.LineNumber, &cursor.FileSize, &cursor.TruncationGeneration, &cursor.FileMtimeMs, &cursor.LastReadAt, &cursor.LastErrorMessage, &cursor.CreatedAt, &cursor.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &cursor, err
}

func insertSQLiteRecords(ctx context.Context, tx *sql.Tx, records []Record) ([]facetRow, error) {
	inserted := make([]facetRow, 0, len(records))
	insertLog, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO runtime_logs (id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	defer insertLog.Close()
	for _, record := range records {
		record = normalizeRecord(record)
		result, err := insertLog.ExecContext(ctx, record.ID, nullable(record.LogFile), record.LogOffset, record.LineNumber, record.Time, record.Level, nullable(record.TraceID), nullable(record.Event), nullable(record.Message), nullable(record.ErrorMessage), record.RawJSON, record.CreatedAt)
		if err != nil {
			return nil, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if changed > 0 {
			inserted = append(inserted, facetRow{Time: record.Time, Level: record.Level, Event: record.Event})
		}
	}
	return inserted, nil
}

func upsertSQLiteCursor(ctx context.Context, tx *sql.Tx, cursor Cursor) error {
	cursor = normalizeCursor(cursor)
	_, err := tx.ExecContext(ctx, `INSERT INTO runtime_log_file_cursors (log_file, file_identity, cursor_offset, line_number, file_size, truncation_generation, file_mtime_ms, last_read_at, last_error_message, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(log_file) DO UPDATE SET file_identity = excluded.file_identity, cursor_offset = excluded.cursor_offset, line_number = excluded.line_number, file_size = excluded.file_size, truncation_generation = excluded.truncation_generation, file_mtime_ms = excluded.file_mtime_ms, last_read_at = excluded.last_read_at, last_error_message = excluded.last_error_message, updated_at = excluded.updated_at`, cursor.LogFile, nullable(cursor.FileIdentity), cursor.CursorOffset, cursor.LineNumber, cursor.FileSize, cursor.TruncationGeneration, cursor.FileMtimeMs, nullable(cursor.LastReadAt), nullable(cursor.LastErrorMessage), cursor.CreatedAt, cursor.UpdatedAt)
	return err
}

func incrementSQLiteFacets(ctx context.Context, tx *sql.Tx, rows []facetRow, cutoff time.Time) error {
	retained := retainedFacetRows(rows, cutoff)
	if len(retained) == 0 {
		return nil
	}
	now := nowISO()
	earliest, latest := rangeTimes(retained)
	_, err := tx.ExecContext(ctx, `INSERT INTO runtime_log_facet_summary (bucket_key, total_count, earliest_time, latest_time, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(bucket_key) DO UPDATE SET total_count = total_count + excluded.total_count, earliest_time = CASE WHEN runtime_log_facet_summary.earliest_time IS NULL OR excluded.earliest_time < runtime_log_facet_summary.earliest_time THEN excluded.earliest_time ELSE runtime_log_facet_summary.earliest_time END, latest_time = CASE WHEN runtime_log_facet_summary.latest_time IS NULL OR excluded.latest_time > runtime_log_facet_summary.latest_time THEN excluded.latest_time ELSE runtime_log_facet_summary.latest_time END, updated_at = excluded.updated_at`, facetBucketKey, len(retained), earliest, latest, now)
	if err != nil {
		return err
	}
	for level, count := range facetLevelCounts(retained) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_log_level_facets (bucket_key, level, count, updated_at) VALUES (?, ?, ?, ?) ON CONFLICT(bucket_key, level) DO UPDATE SET count = count + excluded.count, updated_at = excluded.updated_at`, facetBucketKey, level, count, now); err != nil {
			return err
		}
	}
	for event, summary := range facetEventCounts(retained) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(bucket_key, event) DO UPDATE SET count = count + excluded.count, latest_time = CASE WHEN runtime_log_event_facets.latest_time IS NULL OR excluded.latest_time > runtime_log_event_facets.latest_time THEN excluded.latest_time ELSE runtime_log_event_facets.latest_time END, updated_at = excluded.updated_at`, facetBucketKey, event, summary.count, summary.latestTime, now); err != nil {
			return err
		}
	}
	return nil
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

func normalizeCursor(cursor Cursor) Cursor {
	now := nowISO()
	if cursor.CreatedAt == "" {
		cursor.CreatedAt = now
	}
	if cursor.LastReadAt == "" {
		cursor.LastReadAt = now
	}
	cursor.UpdatedAt = now
	return cursor
}

func normalizeRecord(record Record) Record {
	fallback := nowISO()
	record.Time = normalizeNodeTimestamp(record.Time, fallback)
	record.CreatedAt = normalizeNodeTimestamp(record.CreatedAt, fallback)
	record.Level = strings.ToLower(strings.TrimSpace(record.Level))
	if record.Level == "" {
		record.Level = "info"
	}
	return record
}

func normalizeNodeTimestamp(value string, fallback string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return fallback
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC1123, time.RFC1123Z} {
		parsed, err := time.Parse(layout, text)
		if err == nil {
			return nodeISO(parsed)
		}
	}
	return fallback
}

func nullable(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nowISO() string {
	return nodeISO(time.Now())
}

func nodeISO(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

func cleanupSQLite(ctx context.Context, db *sql.DB, lease OwnerLease, cutoff time.Time, batchSize int, maxBatches int) (CleanupResult, error) {
	result := CleanupResult{}
	cutoffText := nodeISO(cutoff)
	for batch := 0; batch < maxBatches; batch++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return result, err
		}
		if err := verifySQLiteOwnerLease(ctx, tx, lease); err != nil {
			tx.Rollback()
			return result, err
		}
		rows, err := tx.QueryContext(ctx, "SELECT id, time, level, COALESCE(event, '') FROM runtime_logs WHERE time < ? ORDER BY time ASC, id ASC LIMIT ?", cutoffText, batchSize)
		if err != nil {
			tx.Rollback()
			return result, err
		}
		ids, deleted, err := scanFacetRows(rows)
		rows.Close()
		if err != nil {
			tx.Rollback()
			return result, err
		}
		if len(ids) == 0 {
			tx.Rollback()
			break
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM runtime_logs WHERE id IN ("+questionMarks(len(ids))+")", stringArgs(ids)...); err != nil {
			tx.Rollback()
			return result, err
		}
		if err := decrementSQLiteFacets(ctx, tx, deleted, cutoffText); err != nil {
			tx.Rollback()
			return result, err
		}
		if err := tx.Commit(); err != nil {
			return result, err
		}
		result.RuntimeLogs += int64(len(ids))
		if len(ids) < batchSize {
			break
		}
	}
	for batch := 0; batch < maxBatches; batch++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return result, err
		}
		if err := verifySQLiteOwnerLease(ctx, tx, lease); err != nil {
			tx.Rollback()
			return result, err
		}
		query := `DELETE FROM runtime_log_file_cursors WHERE log_file IN (SELECT log_file FROM runtime_log_file_cursors WHERE updated_at < ? AND cursor_offset >= file_size AND last_error_message IS NULL ORDER BY updated_at ASC, log_file ASC LIMIT ?)`
		deleted, err := tx.ExecContext(ctx, query, cutoffText, batchSize)
		if err != nil {
			tx.Rollback()
			return result, err
		}
		count, err := deleted.RowsAffected()
		if err != nil {
			tx.Rollback()
			return result, err
		}
		if err := tx.Commit(); err != nil {
			return result, err
		}
		result.RuntimeLogCursors += count
		if count < int64(batchSize) {
			break
		}
	}
	return result, nil
}

func scanFacetRows(rows *sql.Rows) ([]string, []facetRow, error) {
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

func decrementSQLiteFacets(ctx context.Context, tx *sql.Tx, rows []facetRow, cutoffText string) error {
	if len(rows) == 0 {
		return nil
	}
	now := nowISO()
	var countedFrom, earliest, latest sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT earliest_time FROM runtime_log_facet_summary WHERE bucket_key = ?", facetBucketKey).Scan(&countedFrom); err != nil && err != sql.ErrNoRows {
		return err
	}
	counted := facetRowsFrom(rows, countedFrom.String)
	if len(counted) == 0 {
		return nil
	}
	if err := tx.QueryRowContext(ctx, "SELECT time FROM runtime_logs WHERE time >= ? ORDER BY time ASC, id ASC LIMIT 1", cutoffText).Scan(&earliest); err != nil && err != sql.ErrNoRows {
		return err
	}
	if err := tx.QueryRowContext(ctx, "SELECT time FROM runtime_logs WHERE time >= ? ORDER BY time DESC, id DESC LIMIT 1", cutoffText).Scan(&latest); err != nil && err != sql.ErrNoRows {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE runtime_log_facet_summary SET total_count = MAX(0, total_count - ?), earliest_time = ?, latest_time = ?, updated_at = ? WHERE bucket_key = ?", len(counted), nullable(earliest.String), nullable(latest.String), now, facetBucketKey); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM runtime_log_facet_summary WHERE bucket_key = ? AND total_count <= 0", facetBucketKey); err != nil {
		return err
	}
	for level, count := range facetLevelCounts(counted) {
		if _, err := tx.ExecContext(ctx, "UPDATE runtime_log_level_facets SET count = MAX(0, count - ?), updated_at = ? WHERE bucket_key = ? AND level = ?", count, now, facetBucketKey, level); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM runtime_log_level_facets WHERE bucket_key = ? AND count <= 0", facetBucketKey); err != nil {
		return err
	}
	for event, summary := range facetEventCounts(counted) {
		if _, err := tx.ExecContext(ctx, "UPDATE runtime_log_event_facets SET count = MAX(0, count - ?), updated_at = ? WHERE bucket_key = ? AND event = ?", summary.count, now, facetBucketKey, event); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM runtime_log_event_facets WHERE bucket_key = ? AND count <= 0", facetBucketKey)
	return err
}

func insertPostgresRecords(ctx context.Context, tx pgx.Tx, records []Record) ([]facetRow, error) {
	inserted := make([]facetRow, 0, len(records))
	for start := 0; start < len(records); start += postgresInsertRowsPerStatement {
		end := minInt(start+postgresInsertRowsPerStatement, len(records))
		values := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*12)
		for _, source := range records[start:end] {
			record := normalizeRecord(source)
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
		rows, err := tx.Query(ctx, `INSERT INTO juhe_dataset.runtime_logs (id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at) VALUES `+strings.Join(values, ",")+` ON CONFLICT(id) DO NOTHING RETURNING time, level, COALESCE(event, '')`, args...)
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
	cursor = normalizeCursor(cursor)
	_, err := tx.Exec(ctx, `INSERT INTO juhe_dataset.runtime_log_file_cursors (log_file, file_identity, cursor_offset, line_number, file_size, truncation_generation, file_mtime_ms, last_read_at, last_error_message, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT(log_file) DO UPDATE SET file_identity = excluded.file_identity, cursor_offset = excluded.cursor_offset, line_number = excluded.line_number, file_size = excluded.file_size, truncation_generation = excluded.truncation_generation, file_mtime_ms = excluded.file_mtime_ms, last_read_at = excluded.last_read_at, last_error_message = excluded.last_error_message, updated_at = excluded.updated_at`, cursor.LogFile, nullable(cursor.FileIdentity), cursor.CursorOffset, cursor.LineNumber, cursor.FileSize, cursor.TruncationGeneration, cursor.FileMtimeMs, nullable(cursor.LastReadAt), nullable(cursor.LastErrorMessage), cursor.CreatedAt, cursor.UpdatedAt)
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
	cutoffText := nodeISO(cutoff)
	batchLimit := minInt(maxInt(batchSize, 1), postgresCleanupRowsPerBatch)
	for batch := 0; batch < maxBatches; batch++ {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return result, err
		}
		if err := verifyPostgresOwnerLease(ctx, tx, lease); err != nil {
			tx.Rollback(ctx)
			return result, err
		}
		rows, err := tx.Query(ctx, "SELECT id, time::text, level, COALESCE(event, '') FROM juhe_dataset.runtime_logs WHERE time < $1 ORDER BY time ASC, id ASC LIMIT $2", cutoffText, batchLimit)
		if err != nil {
			tx.Rollback(ctx)
			return result, err
		}
		ids, deleted, err := scanPostgresFacetRows(rows)
		rows.Close()
		if err != nil {
			tx.Rollback(ctx)
			return result, err
		}
		if len(ids) == 0 {
			tx.Rollback(ctx)
			break
		}
		args := stringArgs(ids)
		if _, err := tx.Exec(ctx, "DELETE FROM juhe_dataset.runtime_logs WHERE id IN ("+dollarMarks(1, len(ids))+")", args...); err != nil {
			tx.Rollback(ctx)
			return result, err
		}
		if err := decrementPostgresFacets(ctx, tx, deleted, cutoffText); err != nil {
			tx.Rollback(ctx)
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
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return result, err
		}
		if err := verifyPostgresOwnerLease(ctx, tx, lease); err != nil {
			tx.Rollback(ctx)
			return result, err
		}
		query := `DELETE FROM juhe_dataset.runtime_log_file_cursors WHERE ctid IN (SELECT ctid FROM juhe_dataset.runtime_log_file_cursors WHERE updated_at < $1 AND cursor_offset >= file_size AND last_error_message IS NULL ORDER BY updated_at ASC, ctid ASC LIMIT $2)`
		deleted, err := tx.Exec(ctx, query, cutoffText, batchLimit)
		if err != nil {
			tx.Rollback(ctx)
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

func decrementPostgresFacets(ctx context.Context, tx pgx.Tx, rows []facetRow, cutoffText string) error {
	if len(rows) == 0 {
		return nil
	}
	now := nowISO()
	var countedFrom, earliest, latest string
	if err := tx.QueryRow(ctx, "SELECT COALESCE((SELECT earliest_time FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1), '')", facetBucketKey).Scan(&countedFrom); err != nil {
		return err
	}
	counted := facetRowsFrom(rows, countedFrom)
	if len(counted) == 0 {
		return nil
	}
	if err := tx.QueryRow(ctx, "SELECT COALESCE((SELECT time::text FROM juhe_dataset.runtime_logs WHERE time >= $1 ORDER BY time ASC, id ASC LIMIT 1), '')", cutoffText).Scan(&earliest); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, "SELECT COALESCE((SELECT time::text FROM juhe_dataset.runtime_logs WHERE time >= $1 ORDER BY time DESC, id DESC LIMIT 1), '')", cutoffText).Scan(&latest); err != nil {
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
