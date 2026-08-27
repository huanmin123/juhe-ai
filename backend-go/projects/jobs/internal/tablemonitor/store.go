package tablemonitor

import (
	"context"
	"crypto/rand"
	"database/sql"
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

type Store struct {
	db          *sql.DB
	mode        Mode
	writeMu     sync.Mutex
	schemaMu    sync.Mutex
	schemaReady bool
	pool        *pgpool.Handle
}

const sqliteBusyTimeoutMs = 5000

func OpenStore(cfg Config) (*Store, error) {
	if cfg.Mode == ModeSQLite {
		dsn, err := sqliteOutputDSN(cfg.OutputPath)
		if err != nil {
			return nil, fmt.Errorf("解析表监控 SQLite 输出路径失败: %w", err)
		}
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, fmt.Errorf("打开表监控 SQLite 失败: %w", err)
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		if err := configureSQLiteWriter(db); err != nil {
			db.Close()
			return nil, err
		}
		return &Store{db: db, mode: cfg.Mode}, nil
	}
	maxOpen := cfg.PostgresMaxOpenConns
	if maxOpen == 0 {
		maxOpen = 1000
	}
	maxIdle := cfg.PostgresMaxIdleConns
	if maxIdle == 0 {
		maxIdle = 1000
	}
	var err error
	pool := cfg.PostgresPool
	if pool == nil {
		registry := pgpool.NewRegistry()
		pool, err = registry.Acquire("pgx", cfg.PostgresURL, "table-monitor-store", maxOpen, maxIdle)
		if err != nil {
			return nil, fmt.Errorf("打开表监控 PostgreSQL 连接池失败: %w", err)
		}
	}
	return &Store{db: pool.DB(), mode: cfg.Mode, pool: pool}, nil
}

func configureSQLiteWriter(db *sql.DB) error {
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", sqliteBusyTimeoutMs)); err != nil {
		return fmt.Errorf("设置表监控 SQLite busy_timeout 失败: %w", err)
	}
	var busyTimeout int
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		return fmt.Errorf("读取表监控 SQLite busy_timeout 失败: %w", err)
	}
	if busyTimeout != sqliteBusyTimeoutMs {
		return fmt.Errorf("表监控 SQLite busy_timeout 未生效，实际为 %d", busyTimeout)
	}
	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return fmt.Errorf("启用表监控 SQLite WAL journal_mode 失败: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(journalMode), "wal") {
		return fmt.Errorf("表监控 SQLite WAL journal_mode 未生效，实际为 %q", journalMode)
	}
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("检查表监控 SQLite 连接失败: %w", err)
	}
	return nil
}

func sqliteOutputDSN(path string) (string, error) {
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

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	if s.pool != nil {
		return s.pool.Close()
	}
	return s.db.Close()
}

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// ensureSchema only creates tables while holding a cross-process bootstrap
// lock. A contender rechecks after obtaining that lock, so it never runs
// competing DDL after another process has completed the bootstrap.
func (s *Store) ensureSchema(ctx context.Context) error {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	if s.schemaReady {
		return nil
	}
	var err error
	if s.mode == ModePostgres {
		err = s.ensurePostgresSchema(ctx)
	} else {
		err = s.ensureSQLiteSchema(ctx)
	}
	if err != nil {
		return err
	}
	s.schemaReady = true
	return nil
}

type schemaQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func sqliteSchemaExists(ctx context.Context, queryer schemaQueryer) (bool, error) {
	var count int
	err := queryer.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
WHERE (type = 'table' AND name IN ('database_storage_snapshots', 'table_storage_snapshots', 'table_monitor_owner_leases'))
   OR (type = 'index' AND name IN ('idx_database_storage_snapshots_role_time_id', 'idx_table_storage_snapshots_latest_id', 'idx_table_storage_snapshots_time'))`).Scan(&count)
	return count == 6, err
}

func postgresSchemaExists(ctx context.Context, queryer schemaQueryer) (bool, error) {
	var count int
	err := queryer.QueryRowContext(ctx, `SELECT COUNT(to_regclass(name))
FROM (VALUES ('juhe_stats.database_storage_snapshots'), ('juhe_stats.table_storage_snapshots'), ('juhe_stats.table_monitor_owner_leases')) AS required(name)`).Scan(&count)
	return count == 3, err
}

func (s *Store) ensureSQLiteSchema(ctx context.Context) error {
	exists, err := sqliteSchemaExists(ctx, s.db)
	if err == nil && exists {
		return nil
	}
	if err != nil && !isMissingSQLiteSchemaError(err) {
		return fmt.Errorf("检查表监控 SQLite schema 失败: %w", err)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("打开表监控 SQLite bootstrap 连接失败: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("获取表监控 SQLite bootstrap 锁失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	exists, err = sqliteSchemaExists(ctx, conn)
	if err != nil && !isMissingSQLiteSchemaError(err) {
		return fmt.Errorf("重检表监控 SQLite schema 失败: %w", err)
	}
	if !exists {
		if _, err := conn.ExecContext(ctx, sqliteSchema); err != nil {
			return fmt.Errorf("初始化表监控 SQLite schema 失败: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("提交表监控 SQLite schema bootstrap 失败: %w", err)
	}
	committed = true
	return nil
}

func isMissingSQLiteSchemaError(err error) bool {
	return err != nil && (errors.Is(err, sql.ErrNoRows) || containsSQLiteMissingTable(err))
}

func containsSQLiteMissingTable(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such table")
}

func (s *Store) ensurePostgresSchema(ctx context.Context) error {
	exists, err := postgresSchemaExists(ctx, s.db)
	if err == nil && exists {
		return nil
	}
	if err != nil {
		return fmt.Errorf("检查表监控 PostgreSQL schema 失败: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(763847291)"); err != nil {
		return fmt.Errorf("获取表监控 PostgreSQL bootstrap 锁失败: %w", err)
	}
	exists, err = postgresSchemaExists(ctx, tx)
	if err != nil {
		return fmt.Errorf("重检表监控 PostgreSQL schema 失败: %w", err)
	}
	if !exists {
		if err := executePostgresSchema(ctx, tx); err != nil {
			return fmt.Errorf("初始化表监控 PostgreSQL schema 失败: %w", err)
		}
	}
	return tx.Commit()
}

func executePostgresSchema(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range strings.Split(postgresSchema, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	return s.ensureSchema(ctx)
}

func (s *Store) AcquireOwnerLease(ctx context.Context, ownerID string, duration time.Duration) (OwnerLease, bool, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return OwnerLease{}, false, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	leaseUntil := now.Add(duration)
	var token int64
	if s.mode == ModePostgres {
		err := s.db.QueryRowContext(ctx, `
INSERT INTO juhe_stats.table_monitor_owner_leases (lease_key, owner_id, fence_token, lease_until, updated_at)
VALUES ('table-monitor-sampling-retention', $1, 1, $2, $3)
ON CONFLICT (lease_key) DO UPDATE SET
  owner_id = EXCLUDED.owner_id,
  fence_token = juhe_stats.table_monitor_owner_leases.fence_token + 1,
  lease_until = EXCLUDED.lease_until,
  updated_at = EXCLUDED.updated_at
WHERE juhe_stats.table_monitor_owner_leases.lease_until <= $3
RETURNING fence_token`, ownerID, leaseUntil, now).Scan(&token)
		if errors.Is(err, sql.ErrNoRows) {
			return OwnerLease{}, false, nil
		}
		if err != nil {
			return OwnerLease{}, false, err
		}
		return OwnerLease{OwnerID: ownerID, FenceToken: token}, true, nil
	}
	nowText := sqliteTimestamp(now)
	err := s.db.QueryRowContext(ctx, `
INSERT INTO table_monitor_owner_leases (lease_key, owner_id, fence_token, lease_until, updated_at)
VALUES ('table-monitor-sampling-retention', ?, 1, ?, ?)
ON CONFLICT (lease_key) DO UPDATE SET
  owner_id = excluded.owner_id,
  fence_token = table_monitor_owner_leases.fence_token + 1,
  lease_until = excluded.lease_until,
  updated_at = excluded.updated_at
WHERE table_monitor_owner_leases.lease_until <= ?
RETURNING fence_token`, ownerID, sqliteTimestamp(leaseUntil), nowText, nowText).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerLease{}, false, nil
	}
	if err != nil {
		return OwnerLease{}, false, err
	}
	return OwnerLease{OwnerID: ownerID, FenceToken: token}, true, nil
}

func (s *Store) RenewOwnerLease(ctx context.Context, lease OwnerLease, duration time.Duration) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	if s.mode == ModePostgres {
		result, err := s.db.ExecContext(ctx, `UPDATE juhe_stats.table_monitor_owner_leases
SET lease_until = $1, updated_at = $2
WHERE lease_key = 'table-monitor-sampling-retention' AND owner_id = $3 AND fence_token = $4 AND lease_until > $2`, now.Add(duration), now, lease.OwnerID, lease.FenceToken)
		if err != nil {
			return false, err
		}
		affected, err := result.RowsAffected()
		return affected == 1, err
	}
	nowText := sqliteTimestamp(now)
	result, err := s.db.ExecContext(ctx, `UPDATE table_monitor_owner_leases
SET lease_until = ?, updated_at = ?
WHERE lease_key = 'table-monitor-sampling-retention' AND owner_id = ? AND fence_token = ? AND lease_until > ?`, sqliteTimestamp(now.Add(duration)), nowText, lease.OwnerID, lease.FenceToken, nowText)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (s *Store) ReleaseOwnerLease(ctx context.Context, lease OwnerLease) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	var result sql.Result
	var err error
	if s.mode == ModePostgres {
		result, err = s.db.ExecContext(ctx, `UPDATE juhe_stats.table_monitor_owner_leases
SET owner_id = '', lease_until = $1, updated_at = $1
WHERE lease_key = 'table-monitor-sampling-retention' AND owner_id = $2 AND fence_token = $3`, now, lease.OwnerID, lease.FenceToken)
	} else {
		nowText := sqliteTimestamp(now)
		result, err = s.db.ExecContext(ctx, `UPDATE table_monitor_owner_leases
SET owner_id = '', lease_until = ?, updated_at = ?
WHERE lease_key = 'table-monitor-sampling-retention' AND owner_id = ? AND fence_token = ?`, nowText, nowText, lease.OwnerID, lease.FenceToken)
	}
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrOwnerLeaseLost
	}
	return nil
}

func (s *Store) verifyOwnerLease(ctx context.Context, tx *sql.Tx, lease OwnerLease) error {
	if lease.OwnerID == "" || lease.FenceToken <= 0 {
		return ErrOwnerLeaseLost
	}
	if s.mode == ModePostgres {
		var token int64
		err := tx.QueryRowContext(ctx, `SELECT fence_token
FROM juhe_stats.table_monitor_owner_leases
WHERE lease_key = 'table-monitor-sampling-retention' AND owner_id = $1 AND fence_token = $2 AND lease_until > $3
FOR UPDATE`, lease.OwnerID, lease.FenceToken, time.Now().UTC()).Scan(&token)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOwnerLeaseLost
		}
		return err
	}
	nowText := sqliteTimestamp(time.Now().UTC())
	result, err := tx.ExecContext(ctx, `UPDATE table_monitor_owner_leases SET updated_at = updated_at
WHERE lease_key = 'table-monitor-sampling-retention' AND owner_id = ? AND fence_token = ? AND lease_until > ?`, lease.OwnerID, lease.FenceToken, nowText)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrOwnerLeaseLost
	}
	return nil
}

func (s *Store) WriteSample(ctx context.Context, lease OwnerLease, result collectedSample) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.verifyOwnerLease(ctx, tx, lease); err != nil {
		return err
	}
	if s.mode == ModePostgres {
		for _, snapshot := range result.databases {
			if err := insertPostgresDatabase(ctx, tx, snapshot); err != nil {
				return err
			}
		}
		for _, snapshot := range result.tables {
			if err := insertPostgresTable(ctx, tx, snapshot); err != nil {
				return err
			}
		}
	} else {
		for _, snapshot := range result.databases {
			if err := insertSQLiteDatabase(ctx, tx, snapshot); err != nil {
				return err
			}
		}
		for _, snapshot := range result.tables {
			if err := insertSQLiteTable(ctx, tx, snapshot); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) Cleanup(ctx context.Context, lease OwnerLease, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	if s.mode == ModePostgres {
		return s.cleanupPostgres(ctx, lease, cutoff, limit)
	}
	return s.cleanupSQLite(ctx, lease, cutoff, limit)
}

func (s *Store) CleanupUntilComplete(ctx context.Context, lease OwnerLease, cutoff time.Time, batchSize, maxBatches int) (int64, error) {
	if batchSize <= 0 || maxBatches <= 0 {
		return 0, fmt.Errorf("表监控 retention batch 参数必须大于零")
	}
	var total int64
	for batch := 0; batch < maxBatches; batch++ {
		deleted, err := s.Cleanup(ctx, lease, cutoff, batchSize)
		if err != nil {
			return total, err
		}
		total += deleted
		if deleted == 0 {
			return total, nil
		}
	}
	pending, err := s.hasExpiredSnapshots(ctx, lease, cutoff)
	if err != nil {
		return total, err
	}
	if !pending {
		return total, nil
	}
	return total, fmt.Errorf("表监控 retention 在 %d 批后仍未清空；已删除 %d 行，拒绝静默遗漏", maxBatches, total)
}

func (s *Store) hasExpiredSnapshots(ctx context.Context, lease OwnerLease, cutoff time.Time) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := s.verifyOwnerLease(ctx, tx, lease); err != nil {
		return false, err
	}
	var pending bool
	if s.mode == ModePostgres {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM juhe_stats.table_storage_snapshots WHERE sampled_at < $1
  UNION ALL
  SELECT 1 FROM juhe_stats.database_storage_snapshots WHERE sampled_at < $1
)`, cutoff.UTC()).Scan(&pending)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM table_storage_snapshots WHERE sampled_at < ?
  UNION ALL
  SELECT 1 FROM database_storage_snapshots WHERE sampled_at < ?
)`, sqliteTimestamp(cutoff.UTC()), sqliteTimestamp(cutoff.UTC())).Scan(&pending)
	}
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return pending, nil
}

func (s *Store) populateGrowth(ctx context.Context, sample *collectedSample) error {
	if len(sample.tables) == 0 {
		return nil
	}
	oneHour, err := s.previousTableSnapshots(ctx, sample.tables, time.Hour)
	if err != nil {
		return err
	}
	oneDay, err := s.previousTableSnapshots(ctx, sample.tables, 24*time.Hour)
	if err != nil {
		return err
	}
	for index := range sample.tables {
		snapshot := &sample.tables[index]
		snapshot.GrowthBytes1h = growthDelta(snapshot.TotalBytes, oneHour[index].totalBytes)
		snapshot.GrowthRows1h = growthDelta(snapshot.RowCount, oneHour[index].rowCount)
		snapshot.GrowthBytes24h = growthDelta(snapshot.TotalBytes, oneDay[index].totalBytes)
		snapshot.GrowthRows24h = growthDelta(snapshot.RowCount, oneDay[index].rowCount)
	}
	return nil
}

type tableSnapshotBaseline struct {
	totalBytes *int64
	rowCount   *int64
}

// previousTableSnapshots resolves every table's latest baseline in one query
// for each lookback window.
// The old per-table lookup made a 189-table sample issue 378 sequential
// round trips, which can exhaust a healthy run's wall-clock budget.
func (s *Store) previousTableSnapshots(ctx context.Context, snapshots []TableSnapshot, lookback time.Duration) ([]tableSnapshotBaseline, error) {
	baselines := make([]tableSnapshotBaseline, len(snapshots))
	if len(snapshots) == 0 {
		return baselines, nil
	}

	var values strings.Builder
	args := make([]any, 0, len(snapshots)*4)
	for index, snapshot := range snapshots {
		if index > 0 {
			values.WriteString(", ")
		}
		if s.mode == ModePostgres {
			base := index*4 + 1
			fmt.Fprintf(&values, "($%d::int, $%d::text, $%d::text, $%d::timestamptz)", base, base+1, base+2, base+3)
			args = append(args, index, snapshot.Role, snapshot.TableName, snapshot.SampledAt.Add(-lookback).UTC())
			continue
		}
		values.WriteString("(?, ?, ?, ?)")
		args = append(args, index, snapshot.Role, snapshot.TableName, sqliteTimestamp(snapshot.SampledAt.Add(-lookback).UTC()))
	}

	table := "table_storage_snapshots"
	beforeAt := "target.before_at"
	if s.mode == ModePostgres {
		table = "juhe_stats.table_storage_snapshots"
		beforeAt = "target.before_at::timestamptz"
	}
	query := fmt.Sprintf(`WITH target(target_index, database_role, table_name, before_at) AS (
  VALUES %s
), ranked AS (
  SELECT target.target_index, snapshot.total_bytes, snapshot.row_count,
    ROW_NUMBER() OVER (
      PARTITION BY target.target_index
      ORDER BY snapshot.sampled_at DESC, snapshot.id DESC
    ) AS baseline_rank
  FROM target
  JOIN %s AS snapshot
    ON snapshot.database_role = target.database_role
   AND snapshot.table_name = target.table_name
   AND snapshot.sampled_at <= %s
)
SELECT target_index, total_bytes, row_count
FROM ranked
WHERE baseline_rank = 1`, values.String(), table, beforeAt)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var index int
		var totalBytes, rowCount sql.NullInt64
		if err := rows.Scan(&index, &totalBytes, &rowCount); err != nil {
			return nil, err
		}
		if index < 0 || index >= len(baselines) {
			return nil, fmt.Errorf("表监控历史基线返回了无效目标序号 %d", index)
		}
		if totalBytes.Valid {
			value := totalBytes.Int64
			baselines[index].totalBytes = &value
		}
		if rowCount.Valid {
			value := rowCount.Int64
			baselines[index].rowCount = &value
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return baselines, nil
}

func growthDelta(current, previous *int64) *int64 {
	if current == nil || previous == nil {
		return nil
	}
	delta := *current - *previous
	return &delta
}

func (s *Store) cleanupSQLite(ctx context.Context, lease OwnerLease, cutoff time.Time, limit int) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := s.verifyOwnerLease(ctx, tx, lease); err != nil {
		return 0, err
	}
	var total int64
	for _, table := range []string{"table_storage_snapshots", "database_storage_snapshots"} {
		res, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE sampled_at < ? ORDER BY sampled_at ASC, id ASC LIMIT ?)`, table, table), sqliteTimestamp(cutoff.UTC()), limit)
		if err != nil {
			return 0, err
		}
		count, _ := res.RowsAffected()
		total += count
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return total, nil
}

func (s *Store) cleanupPostgres(ctx context.Context, lease OwnerLease, cutoff time.Time, limit int) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := s.verifyOwnerLease(ctx, tx, lease); err != nil {
		return 0, err
	}
	var total int64
	for _, table := range []string{"table_storage_snapshots", "database_storage_snapshots"} {
		res, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM juhe_stats.%s WHERE id IN (SELECT id FROM juhe_stats.%s WHERE sampled_at < $1 ORDER BY sampled_at ASC, id ASC LIMIT $2)`, table, table), cutoff.UTC(), limit)
		if err != nil {
			return 0, err
		}
		count, _ := res.RowsAffected()
		total += count
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return total, nil
}

func insertSQLiteDatabase(ctx context.Context, tx *sql.Tx, snapshot DatabaseSnapshot) error {
	id, err := newID("dbsnap")
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO database_storage_snapshots (id, database_role, database_path, sampled_at, file_bytes, wal_bytes, shm_bytes, page_size, page_count, freelist_count, used_bytes, free_bytes, table_count, index_count, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, snapshot.Role, snapshot.Path, sqliteTimestamp(snapshot.SampledAt.UTC()), snapshot.FileBytes, snapshot.WALBytes, snapshot.SHMBytes, snapshot.PageSize, snapshot.PageCount, snapshot.FreelistCount, snapshot.UsedBytes, snapshot.FreeBytes, snapshot.TableCount, snapshot.IndexCount, sqliteTimestamp(snapshot.SampledAt.UTC()))
	return err
}

func insertSQLiteTable(ctx context.Context, tx *sql.Tx, snapshot TableSnapshot) error {
	id, err := newID("tblsnap")
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO table_storage_snapshots (id, database_role, table_name, sampled_at, table_kind, parent_table_name, is_partition, is_archive, row_count, table_bytes, index_bytes, total_bytes, page_count, index_count, growth_bytes_1h, growth_rows_1h, growth_bytes_24h, growth_rows_24h, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(database_role, table_name, sampled_at) DO UPDATE SET
  table_kind = excluded.table_kind, parent_table_name = excluded.parent_table_name, is_partition = excluded.is_partition,
  is_archive = excluded.is_archive, row_count = excluded.row_count, table_bytes = excluded.table_bytes,
  index_bytes = excluded.index_bytes, total_bytes = excluded.total_bytes, page_count = excluded.page_count,
  index_count = excluded.index_count, growth_bytes_1h = excluded.growth_bytes_1h, growth_rows_1h = excluded.growth_rows_1h,
  growth_bytes_24h = excluded.growth_bytes_24h, growth_rows_24h = excluded.growth_rows_24h, created_at = excluded.created_at`, id, snapshot.Role, snapshot.TableName, sqliteTimestamp(snapshot.SampledAt.UTC()), snapshot.TableKind, snapshot.ParentTableName, boolInt(snapshot.IsPartition), boolInt(snapshot.IsArchive), snapshot.RowCount, snapshot.TableBytes, snapshot.IndexBytes, snapshot.TotalBytes, snapshot.PageCount, snapshot.IndexCount, snapshot.GrowthBytes1h, snapshot.GrowthRows1h, snapshot.GrowthBytes24h, snapshot.GrowthRows24h, sqliteTimestamp(snapshot.SampledAt.UTC()))
	return err
}

func insertPostgresDatabase(ctx context.Context, tx *sql.Tx, snapshot DatabaseSnapshot) error {
	id, err := newID("dbsnap")
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO juhe_stats.database_storage_snapshots (id, database_role, database_path, sampled_at, file_bytes, wal_bytes, shm_bytes, page_size, page_count, freelist_count, used_bytes, free_bytes, table_count, index_count, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $4)`, id, snapshot.Role, snapshot.Path, snapshot.SampledAt.UTC(), snapshot.FileBytes, snapshot.WALBytes, snapshot.SHMBytes, snapshot.PageSize, snapshot.PageCount, snapshot.FreelistCount, snapshot.UsedBytes, snapshot.FreeBytes, snapshot.TableCount, snapshot.IndexCount)
	return err
}

func insertPostgresTable(ctx context.Context, tx *sql.Tx, snapshot TableSnapshot) error {
	id, err := newID("tblsnap")
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO juhe_stats.table_storage_snapshots (id, database_role, table_name, sampled_at, table_kind, parent_table_name, is_partition, is_archive, row_count, table_bytes, index_bytes, total_bytes, page_count, index_count, growth_bytes_1h, growth_rows_1h, growth_bytes_24h, growth_rows_24h, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $4)
ON CONFLICT(database_role, table_name, sampled_at) DO UPDATE SET
  table_kind = EXCLUDED.table_kind, parent_table_name = EXCLUDED.parent_table_name, is_partition = EXCLUDED.is_partition,
  is_archive = EXCLUDED.is_archive, row_count = EXCLUDED.row_count, table_bytes = EXCLUDED.table_bytes,
  index_bytes = EXCLUDED.index_bytes, total_bytes = EXCLUDED.total_bytes, page_count = EXCLUDED.page_count,
  index_count = EXCLUDED.index_count, growth_bytes_1h = EXCLUDED.growth_bytes_1h, growth_rows_1h = EXCLUDED.growth_rows_1h,
  growth_bytes_24h = EXCLUDED.growth_bytes_24h, growth_rows_24h = EXCLUDED.growth_rows_24h, created_at = EXCLUDED.created_at`, id, snapshot.Role, snapshot.TableName, snapshot.SampledAt.UTC(), snapshot.TableKind, snapshot.ParentTableName, snapshot.IsPartition, snapshot.IsArchive, snapshot.RowCount, snapshot.TableBytes, snapshot.IndexBytes, snapshot.TotalBytes, snapshot.PageCount, snapshot.IndexCount, snapshot.GrowthBytes1h, snapshot.GrowthRows1h, snapshot.GrowthBytes24h, snapshot.GrowthRows24h)
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func sqliteTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func newID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("生成表监控快照 ID 失败: %w", err)
	}
	return fmt.Sprintf("%s-%x", prefix, raw[:]), nil
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS database_storage_snapshots (
  id TEXT PRIMARY KEY, database_role TEXT NOT NULL, database_path TEXT NOT NULL, sampled_at TEXT NOT NULL,
  file_bytes INTEGER, wal_bytes INTEGER, shm_bytes INTEGER, page_size INTEGER, page_count INTEGER,
  freelist_count INTEGER, used_bytes INTEGER, free_bytes INTEGER, table_count INTEGER, index_count INTEGER, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS table_storage_snapshots (
  id TEXT PRIMARY KEY, database_role TEXT NOT NULL, table_name TEXT NOT NULL, sampled_at TEXT NOT NULL,
  table_kind TEXT NOT NULL DEFAULT 'table', parent_table_name TEXT, is_partition INTEGER NOT NULL DEFAULT 0,
  is_archive INTEGER NOT NULL DEFAULT 0, row_count INTEGER, table_bytes INTEGER, index_bytes INTEGER, total_bytes INTEGER,
  page_count INTEGER, index_count INTEGER NOT NULL DEFAULT 0, growth_bytes_1h INTEGER, growth_rows_1h INTEGER,
  growth_bytes_24h INTEGER, growth_rows_24h INTEGER, created_at TEXT NOT NULL,
  UNIQUE(database_role, table_name, sampled_at)
);
CREATE TABLE IF NOT EXISTS table_monitor_owner_leases (
  lease_key TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token INTEGER NOT NULL,
  lease_until TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_database_storage_snapshots_role_time_id ON database_storage_snapshots(database_role, sampled_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_table_storage_snapshots_latest_id ON table_storage_snapshots(database_role, table_name, sampled_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_table_storage_snapshots_time ON table_storage_snapshots(sampled_at DESC);
`

const postgresSchema = `
CREATE SCHEMA IF NOT EXISTS juhe_stats;
CREATE TABLE IF NOT EXISTS juhe_stats.database_storage_snapshots (
  id TEXT PRIMARY KEY, database_role TEXT NOT NULL, database_path TEXT NOT NULL, sampled_at TIMESTAMPTZ NOT NULL,
  file_bytes BIGINT, wal_bytes BIGINT, shm_bytes BIGINT, page_size INTEGER, page_count INTEGER,
  freelist_count INTEGER, used_bytes BIGINT, free_bytes BIGINT, table_count INTEGER, index_count INTEGER, created_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_stats.table_storage_snapshots (
  id TEXT PRIMARY KEY, database_role TEXT NOT NULL, table_name TEXT NOT NULL, sampled_at TIMESTAMPTZ NOT NULL,
  table_kind TEXT NOT NULL DEFAULT 'table', parent_table_name TEXT, is_partition BOOLEAN NOT NULL DEFAULT FALSE,
  is_archive BOOLEAN NOT NULL DEFAULT FALSE, row_count BIGINT, table_bytes BIGINT, index_bytes BIGINT, total_bytes BIGINT,
  page_count BIGINT, index_count INTEGER NOT NULL DEFAULT 0, growth_bytes_1h BIGINT, growth_rows_1h BIGINT,
  growth_bytes_24h BIGINT, growth_rows_24h BIGINT, created_at TIMESTAMPTZ NOT NULL,
  UNIQUE(database_role, table_name, sampled_at)
);
CREATE TABLE IF NOT EXISTS juhe_stats.table_monitor_owner_leases (
  lease_key TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL,
  lease_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_database_storage_snapshots_role_time_id ON juhe_stats.database_storage_snapshots(database_role, sampled_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_table_storage_snapshots_latest_id ON juhe_stats.table_storage_snapshots(database_role, table_name, sampled_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_table_storage_snapshots_time ON juhe_stats.table_storage_snapshots(sampled_at DESC);
`
