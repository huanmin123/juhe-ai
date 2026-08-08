package tablemonitor

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type Store struct {
	db      *sql.DB
	mode    Mode
	writeMu sync.Mutex
}

func OpenStore(cfg Config) (*Store, error) {
	if cfg.Mode == ModeSQLite {
		if err := os.MkdirAll(filepath.Dir(cfg.OutputPath), 0o755); err != nil {
			return nil, fmt.Errorf("创建表监控 SQLite 目录失败: %w", err)
		}
		db, err := sql.Open("sqlite", cfg.OutputPath)
		if err != nil {
			return nil, fmt.Errorf("打开表监控 SQLite 失败: %w", err)
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		return &Store{db: db, mode: cfg.Mode}, nil
	}
	db, err := sql.Open("pgx", cfg.PostgresURL)
	if err != nil {
		return nil, fmt.Errorf("打开表监控 PostgreSQL 失败: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	return &Store{db: db, mode: cfg.Mode}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) EnsureSchema(ctx context.Context) error {
	if s.mode == ModePostgres {
		_, err := s.db.ExecContext(ctx, postgresSchema)
		return err
	}
	_, err := s.db.ExecContext(ctx, sqliteSchema)
	return err
}

func (s *Store) AcquireOwnerLease(ctx context.Context, ownerID string, duration time.Duration) (OwnerLease, bool, error) {
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
	nowText := now.Format(time.RFC3339Nano)
	err := s.db.QueryRowContext(ctx, `
INSERT INTO table_monitor_owner_leases (lease_key, owner_id, fence_token, lease_until, updated_at)
VALUES ('table-monitor-sampling-retention', ?, 1, ?, ?)
ON CONFLICT (lease_key) DO UPDATE SET
  owner_id = excluded.owner_id,
  fence_token = table_monitor_owner_leases.fence_token + 1,
  lease_until = excluded.lease_until,
  updated_at = excluded.updated_at
WHERE table_monitor_owner_leases.lease_until <= ?
RETURNING fence_token`, ownerID, leaseUntil.Format(time.RFC3339Nano), nowText, nowText).Scan(&token)
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
	nowText := now.Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE table_monitor_owner_leases
SET lease_until = ?, updated_at = ?
WHERE lease_key = 'table-monitor-sampling-retention' AND owner_id = ? AND fence_token = ? AND lease_until > ?`, now.Add(duration).Format(time.RFC3339Nano), nowText, lease.OwnerID, lease.FenceToken, nowText)
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
		nowText := now.Format(time.RFC3339Nano)
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
	nowText := time.Now().UTC().Format(time.RFC3339Nano)
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
		res, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE sampled_at < ? ORDER BY sampled_at ASC, id ASC LIMIT ?)`, table, table), cutoff.UTC().Format(time.RFC3339Nano), limit)
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
	_, err := tx.ExecContext(ctx, `INSERT INTO database_storage_snapshots (id, database_role, database_path, sampled_at, file_bytes, wal_bytes, shm_bytes, page_size, page_count, freelist_count, used_bytes, free_bytes, table_count, index_count, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, newID("dbsnap"), snapshot.Role, snapshot.Path, snapshot.SampledAt.UTC().Format(time.RFC3339Nano), snapshot.FileBytes, nil, nil, snapshot.PageSize, snapshot.PageCount, snapshot.FreelistCount, snapshot.UsedBytes, snapshot.FreeBytes, snapshot.TableCount, snapshot.IndexCount, snapshot.SampledAt.UTC().Format(time.RFC3339Nano))
	return err
}

func insertSQLiteTable(ctx context.Context, tx *sql.Tx, snapshot TableSnapshot) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO table_storage_snapshots (id, database_role, table_name, sampled_at, table_kind, parent_table_name, is_partition, is_archive, row_count, table_bytes, index_bytes, total_bytes, page_count, index_count, growth_bytes_1h, growth_rows_1h, growth_bytes_24h, growth_rows_24h, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, newID("tblsnap"), snapshot.Role, snapshot.TableName, snapshot.SampledAt.UTC().Format(time.RFC3339Nano), snapshot.TableKind, snapshot.ParentTableName, boolInt(snapshot.IsPartition), boolInt(snapshot.IsArchive), snapshot.RowCount, snapshot.TableBytes, snapshot.IndexBytes, snapshot.TotalBytes, snapshot.PageCount, snapshot.IndexCount, nil, nil, nil, nil, snapshot.SampledAt.UTC().Format(time.RFC3339Nano))
	return err
}

func insertPostgresDatabase(ctx context.Context, tx *sql.Tx, snapshot DatabaseSnapshot) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO juhe_stats.database_storage_snapshots (id, database_role, database_path, sampled_at, file_bytes, wal_bytes, shm_bytes, page_size, page_count, freelist_count, used_bytes, free_bytes, table_count, index_count, created_at) VALUES ($1, $2, $3, $4, $5, NULL, NULL, $6, $7, $8, $9, $10, $11, $12, $4)`, newID("dbsnap"), snapshot.Role, snapshot.Path, snapshot.SampledAt.UTC(), snapshot.FileBytes, snapshot.PageSize, snapshot.PageCount, snapshot.FreelistCount, snapshot.UsedBytes, snapshot.FreeBytes, snapshot.TableCount, snapshot.IndexCount)
	return err
}

func insertPostgresTable(ctx context.Context, tx *sql.Tx, snapshot TableSnapshot) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO juhe_stats.table_storage_snapshots (id, database_role, table_name, sampled_at, table_kind, parent_table_name, is_partition, is_archive, row_count, table_bytes, index_bytes, total_bytes, page_count, index_count, growth_bytes_1h, growth_rows_1h, growth_bytes_24h, growth_rows_24h, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,  NULL, NULL, NULL, NULL, $4) ON CONFLICT (database_role, table_name, sampled_at) DO NOTHING`, newID("tblsnap"), snapshot.Role, snapshot.TableName, snapshot.SampledAt.UTC(), snapshot.TableKind, snapshot.ParentTableName, snapshot.IsPartition, snapshot.IsArchive, snapshot.RowCount, snapshot.TableBytes, snapshot.IndexBytes, snapshot.TotalBytes, snapshot.PageCount, snapshot.IndexCount)
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func newID(prefix string) string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%x", prefix, raw[:])
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
