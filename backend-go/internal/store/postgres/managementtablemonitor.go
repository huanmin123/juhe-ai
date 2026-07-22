package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	tableMonitorDefaultOverviewLimit = 200
	tableMonitorMaxOverviewLimit     = 1000
	tableMonitorDefaultHistoryLimit  = 720
	tableMonitorMaxHistoryLimit      = 10000
)

const tableMonitorOverviewDatabasesSQL = `
WITH monitored_roles(database_role, sort_order) AS (
  VALUES
    ('business'::text, 1),
    ('dataset'::text, 2),
    ('usage-catalog'::text, 3),
    ('stats'::text, 4),
    ('codex-context-state'::text, 5)
)
SELECT
  snapshot.id,
  snapshot.database_role,
  snapshot.database_path,
  snapshot.sampled_at,
  snapshot.file_bytes,
  snapshot.wal_bytes,
  snapshot.shm_bytes,
  snapshot.free_bytes,
  snapshot.table_count
FROM monitored_roles AS role
CROSS JOIN LATERAL (
  SELECT
    storage.id,
    storage.database_role,
    storage.database_path,
    storage.sampled_at,
    storage.file_bytes,
    storage.wal_bytes,
    storage.shm_bytes,
    storage.free_bytes,
    storage.table_count
  FROM juhe_stats.database_storage_snapshots AS storage
  WHERE storage.database_role = role.database_role
  ORDER BY storage.sampled_at DESC, storage.id DESC
  LIMIT 1
) AS snapshot
ORDER BY role.sort_order ASC`

const tableMonitorOverviewTablesSQL = `
WITH monitored_roles(database_role) AS (
  VALUES
    ('business'::text),
    ('dataset'::text),
    ('usage-catalog'::text),
    ('stats'::text),
    ('codex-context-state'::text)
), latest_samples AS (
  SELECT role.database_role, latest.sampled_at
  FROM monitored_roles AS role
  CROSS JOIN LATERAL (
    SELECT storage.sampled_at
    FROM juhe_stats.table_storage_snapshots AS storage
    WHERE storage.database_role = role.database_role
    ORDER BY storage.sampled_at DESC, storage.id DESC
    LIMIT 1
  ) AS latest
), bounded_tables AS (
  SELECT storage.*
  FROM latest_samples AS latest
  CROSS JOIN LATERAL (
    SELECT
      snapshot.id,
      snapshot.database_role,
      snapshot.table_name,
      snapshot.sampled_at,
      snapshot.table_kind,
      snapshot.parent_table_name,
      snapshot.is_partition,
      snapshot.is_archive,
      snapshot.row_count,
      snapshot.table_bytes,
      snapshot.index_bytes,
      snapshot.total_bytes,
      snapshot.growth_bytes_1h,
      snapshot.growth_rows_1h,
      snapshot.growth_bytes_24h,
      snapshot.growth_rows_24h
    FROM juhe_stats.table_storage_snapshots AS snapshot
    WHERE snapshot.database_role = latest.database_role
      AND snapshot.sampled_at = latest.sampled_at
    ORDER BY
      snapshot.total_bytes DESC NULLS LAST,
      snapshot.row_count DESC NULLS LAST,
      snapshot.table_name COLLATE "C" ASC,
      snapshot.id ASC
    LIMIT $1::int
  ) AS storage
)
SELECT
  storage.id,
  storage.database_role,
  storage.table_name,
  storage.sampled_at,
  storage.table_kind,
  storage.parent_table_name,
  (storage.is_partition <> 0) AS is_partition,
  (storage.is_archive <> 0) AS is_archive,
  storage.row_count,
  storage.table_bytes,
  storage.index_bytes,
  storage.total_bytes,
  storage.growth_bytes_1h,
  storage.growth_rows_1h,
  storage.growth_bytes_24h,
  storage.growth_rows_24h
FROM bounded_tables AS storage
ORDER BY
  storage.total_bytes DESC NULLS LAST,
  storage.row_count DESC NULLS LAST,
  storage.table_name COLLATE "C" ASC,
  storage.database_role COLLATE "C" ASC,
  storage.id ASC
LIMIT $1::int`

const tableMonitorTableHistorySQL = `
SELECT
  history.id,
  history.database_role,
  history.table_name,
  history.sampled_at,
  history.table_kind,
  history.parent_table_name,
  (history.is_partition <> 0) AS is_partition,
  (history.is_archive <> 0) AS is_archive,
  history.row_count,
  history.table_bytes,
  history.index_bytes,
  history.total_bytes,
  history.page_count,
  history.index_count,
  history.growth_bytes_1h,
  history.growth_rows_1h,
  history.growth_bytes_24h,
  history.growth_rows_24h
FROM (
  SELECT
    storage.id,
    storage.database_role,
    storage.table_name,
    storage.sampled_at,
    storage.table_kind,
    storage.parent_table_name,
    storage.is_partition,
    storage.is_archive,
    storage.row_count,
    storage.table_bytes,
    storage.index_bytes,
    storage.total_bytes,
    storage.page_count,
    storage.index_count,
    storage.growth_bytes_1h,
    storage.growth_rows_1h,
    storage.growth_bytes_24h,
    storage.growth_rows_24h
  FROM juhe_stats.table_storage_snapshots AS storage
  WHERE storage.database_role = $1::text
    AND storage.table_name = $2::text
    AND storage.sampled_at >= $3::text
    AND storage.sampled_at <= $4::text
  ORDER BY storage.sampled_at DESC, storage.id DESC
  LIMIT $5::int
) AS history
ORDER BY history.sampled_at ASC, history.id ASC`

const tableMonitorDatabaseHistorySQL = `
WITH monitored_roles(database_role, sort_order) AS (
  VALUES
    ('business'::text, 1),
    ('dataset'::text, 2),
    ('usage-catalog'::text, 3),
    ('stats'::text, 4),
    ('codex-context-state'::text, 5)
), bounded_history AS (
  SELECT snapshot.*, role.sort_order
  FROM monitored_roles AS role
  CROSS JOIN LATERAL (
    SELECT
      storage.id,
      storage.database_role,
      storage.database_path,
      storage.sampled_at,
      storage.file_bytes,
      storage.wal_bytes,
      storage.shm_bytes,
      storage.page_size,
      storage.page_count,
      storage.freelist_count,
      storage.used_bytes,
      storage.free_bytes,
      storage.table_count,
      storage.index_count
    FROM juhe_stats.database_storage_snapshots AS storage
    WHERE storage.database_role = role.database_role
      AND storage.sampled_at >= $1::text
      AND storage.sampled_at <= $2::text
    ORDER BY storage.sampled_at DESC, storage.id DESC
    LIMIT $3::int
  ) AS snapshot
)
SELECT
  history.id,
  history.database_role,
  history.database_path,
  history.sampled_at,
  history.file_bytes,
  history.wal_bytes,
  history.shm_bytes,
  history.page_size,
  history.page_count,
  history.freelist_count,
  history.used_bytes,
  history.free_bytes,
  history.table_count,
  history.index_count
FROM bounded_history AS history
ORDER BY history.sampled_at ASC, history.sort_order ASC, history.id DESC
LIMIT 50000`

type managementDatabaseStorageSnapshotRow struct {
	ID            string
	DatabaseRole  string
	DatabasePath  string
	SampledAt     string
	FileBytes     *int64
	WALBytes      *int64
	SHMBytes      *int64
	PageSize      *int64
	PageCount     *int64
	FreelistCount *int64
	UsedBytes     *int64
	FreeBytes     *int64
	TableCount    *int64
	IndexCount    *int64
}

type managementTableStorageSnapshotRow struct {
	ID              string
	DatabaseRole    string
	TableName       string
	SampledAt       string
	TableKind       *string
	ParentTableName *string
	IsPartition     *bool
	IsArchive       *bool
	RowCount        *int64
	TableBytes      *int64
	IndexBytes      *int64
	TotalBytes      *int64
	PageCount       *int64
	IndexCount      int64
	GrowthBytes1H   *int64
	GrowthRows1H    *int64
	GrowthBytes24H  *int64
	GrowthRows24H   *int64
}

type managementTableMonitorExecutor interface {
	QueryLatestManagementDatabaseStorageSnapshots(ctx context.Context) ([]managementDatabaseStorageSnapshotRow, error)
	QueryLatestManagementTableStorageSnapshots(ctx context.Context, limit int) ([]managementTableStorageSnapshotRow, error)
	QueryManagementTableStorageHistory(ctx context.Context, input port.ManagementTableStorageHistoryInput) ([]managementTableStorageSnapshotRow, error)
	QueryManagementDatabaseStorageHistory(ctx context.Context, input port.ManagementDatabaseStorageHistoryInput) ([]managementDatabaseStorageSnapshotRow, error)
}

type postgresManagementTableMonitorExecutor struct{ store *Store }

func (e postgresManagementTableMonitorExecutor) QueryLatestManagementDatabaseStorageSnapshots(ctx context.Context) ([]managementDatabaseStorageSnapshotRow, error) {
	rows, err := e.store.pool.Query(ctx, tableMonitorOverviewDatabasesSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanManagementOverviewDatabaseRows(rows)
}

func (e postgresManagementTableMonitorExecutor) QueryLatestManagementTableStorageSnapshots(ctx context.Context, limit int) ([]managementTableStorageSnapshotRow, error) {
	rows, err := e.store.pool.Query(ctx, tableMonitorOverviewTablesSQL, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanManagementOverviewTableRows(rows)
}

func (e postgresManagementTableMonitorExecutor) QueryManagementTableStorageHistory(ctx context.Context, input port.ManagementTableStorageHistoryInput) ([]managementTableStorageSnapshotRow, error) {
	rows, err := e.store.pool.Query(ctx, tableMonitorTableHistorySQL, input.DatabaseRole, input.TableName, input.StartAt, input.EndAt, input.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanManagementHistoryTableRows(rows)
}

func (e postgresManagementTableMonitorExecutor) QueryManagementDatabaseStorageHistory(ctx context.Context, input port.ManagementDatabaseStorageHistoryInput) ([]managementDatabaseStorageSnapshotRow, error) {
	rows, err := e.store.pool.Query(ctx, tableMonitorDatabaseHistorySQL, input.StartAt, input.EndAt, input.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanManagementHistoryDatabaseRows(rows)
}

func (s *Store) GetManagementTableStorageOverview(ctx context.Context, limit int) (port.ManagementTableStorageOverview, error) {
	return getManagementTableStorageOverview(ctx, postgresManagementTableMonitorExecutor{store: s}, normalizeTableMonitorLimit(limit, tableMonitorDefaultOverviewLimit, tableMonitorMaxOverviewLimit))
}

func getManagementTableStorageOverview(ctx context.Context, executor managementTableMonitorExecutor, limit int) (port.ManagementTableStorageOverview, error) {
	databaseRows, err := executor.QueryLatestManagementDatabaseStorageSnapshots(ctx)
	if err != nil {
		return port.ManagementTableStorageOverview{}, fmt.Errorf("list table monitor database overview: %w", err)
	}
	tableRows, err := executor.QueryLatestManagementTableStorageSnapshots(ctx, limit)
	if err != nil {
		return port.ManagementTableStorageOverview{}, fmt.Errorf("list table monitor table overview: %w", err)
	}
	databases := make([]port.ManagementDatabaseStorageSnapshot, 0, len(databaseRows))
	sampledAt := ""
	for _, row := range databaseRows {
		databases = append(databases, managementDatabaseStorageSnapshot(row))
		if row.SampledAt > sampledAt {
			sampledAt = row.SampledAt
		}
	}
	tables := make([]port.ManagementTableStorageSnapshot, 0, len(tableRows))
	for _, row := range tableRows {
		tables = append(tables, managementTableStorageSnapshot(row))
	}
	return port.ManagementTableStorageOverview{SampledAt: sampledAt, Databases: databases, Tables: tables}, nil
}

func (s *Store) ListManagementTableStorageHistory(ctx context.Context, input port.ManagementTableStorageHistoryInput) ([]port.ManagementTableStorageSnapshot, error) {
	input.Limit = normalizeTableMonitorLimit(input.Limit, tableMonitorDefaultHistoryLimit, tableMonitorMaxHistoryLimit)
	return listManagementTableStorageHistory(ctx, postgresManagementTableMonitorExecutor{store: s}, input)
}

func listManagementTableStorageHistory(ctx context.Context, executor managementTableMonitorExecutor, input port.ManagementTableStorageHistoryInput) ([]port.ManagementTableStorageSnapshot, error) {
	rows, err := executor.QueryManagementTableStorageHistory(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("list table monitor table history: %w", err)
	}
	result := make([]port.ManagementTableStorageSnapshot, 0, len(rows))
	for _, row := range rows {
		result = append(result, managementTableStorageSnapshot(row))
	}
	return result, nil
}

func (s *Store) ListManagementDatabaseStorageHistory(ctx context.Context, input port.ManagementDatabaseStorageHistoryInput) ([]port.ManagementDatabaseStorageSnapshot, error) {
	input.Limit = normalizeTableMonitorLimit(input.Limit, tableMonitorDefaultHistoryLimit, tableMonitorMaxHistoryLimit)
	return listManagementDatabaseStorageHistory(ctx, postgresManagementTableMonitorExecutor{store: s}, input)
}

func listManagementDatabaseStorageHistory(ctx context.Context, executor managementTableMonitorExecutor, input port.ManagementDatabaseStorageHistoryInput) ([]port.ManagementDatabaseStorageSnapshot, error) {
	rows, err := executor.QueryManagementDatabaseStorageHistory(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("list table monitor database history: %w", err)
	}
	result := make([]port.ManagementDatabaseStorageSnapshot, 0, len(rows))
	for _, row := range rows {
		result = append(result, managementDatabaseStorageSnapshot(row))
	}
	return result, nil
}

func scanManagementOverviewDatabaseRows(rows pgx.Rows) ([]managementDatabaseStorageSnapshotRow, error) {
	result := make([]managementDatabaseStorageSnapshotRow, 0)
	for rows.Next() {
		var row managementDatabaseStorageSnapshotRow
		if err := rows.Scan(&row.ID, &row.DatabaseRole, &row.DatabasePath, &row.SampledAt, &row.FileBytes, &row.WALBytes, &row.SHMBytes, &row.FreeBytes, &row.TableCount); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func scanManagementOverviewTableRows(rows pgx.Rows) ([]managementTableStorageSnapshotRow, error) {
	result := make([]managementTableStorageSnapshotRow, 0)
	for rows.Next() {
		var row managementTableStorageSnapshotRow
		if err := rows.Scan(
			&row.ID, &row.DatabaseRole, &row.TableName, &row.SampledAt,
			&row.TableKind, &row.ParentTableName, &row.IsPartition, &row.IsArchive,
			&row.RowCount, &row.TableBytes, &row.IndexBytes, &row.TotalBytes,
			&row.GrowthBytes1H, &row.GrowthRows1H, &row.GrowthBytes24H, &row.GrowthRows24H,
		); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func scanManagementHistoryTableRows(rows pgx.Rows) ([]managementTableStorageSnapshotRow, error) {
	result := make([]managementTableStorageSnapshotRow, 0)
	for rows.Next() {
		var row managementTableStorageSnapshotRow
		if err := rows.Scan(
			&row.ID, &row.DatabaseRole, &row.TableName, &row.SampledAt,
			&row.TableKind, &row.ParentTableName, &row.IsPartition, &row.IsArchive,
			&row.RowCount, &row.TableBytes, &row.IndexBytes, &row.TotalBytes,
			&row.PageCount, &row.IndexCount,
			&row.GrowthBytes1H, &row.GrowthRows1H, &row.GrowthBytes24H, &row.GrowthRows24H,
		); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func scanManagementHistoryDatabaseRows(rows pgx.Rows) ([]managementDatabaseStorageSnapshotRow, error) {
	result := make([]managementDatabaseStorageSnapshotRow, 0)
	for rows.Next() {
		var row managementDatabaseStorageSnapshotRow
		if err := rows.Scan(
			&row.ID, &row.DatabaseRole, &row.DatabasePath, &row.SampledAt,
			&row.FileBytes, &row.WALBytes, &row.SHMBytes, &row.PageSize, &row.PageCount,
			&row.FreelistCount, &row.UsedBytes, &row.FreeBytes, &row.TableCount, &row.IndexCount,
		); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func managementDatabaseStorageSnapshot(row managementDatabaseStorageSnapshotRow) port.ManagementDatabaseStorageSnapshot {
	return port.ManagementDatabaseStorageSnapshot{
		DatabaseRole: port.MonitoredDatabaseRole(row.DatabaseRole), DatabasePath: tableMonitorBaseName(row.DatabasePath), SampledAt: row.SampledAt,
		FileBytes: row.FileBytes, WALBytes: row.WALBytes, SHMBytes: row.SHMBytes,
		PageSize: row.PageSize, PageCount: row.PageCount, FreelistCount: row.FreelistCount,
		UsedBytes: row.UsedBytes, FreeBytes: row.FreeBytes, TableCount: row.TableCount, IndexCount: row.IndexCount,
	}
}

func managementTableStorageSnapshot(row managementTableStorageSnapshotRow) port.ManagementTableStorageSnapshot {
	return port.ManagementTableStorageSnapshot{
		DatabaseRole: port.MonitoredDatabaseRole(row.DatabaseRole), TableName: row.TableName, SampledAt: row.SampledAt,
		TableKind: row.TableKind, ParentTableName: row.ParentTableName,
		IsPartition: optionalBool(row.IsPartition), IsArchive: optionalBool(row.IsArchive),
		RowCount: row.RowCount, TableBytes: row.TableBytes, IndexBytes: row.IndexBytes, TotalBytes: row.TotalBytes,
		PageCount: row.PageCount, IndexCount: row.IndexCount,
		GrowthBytes1H: row.GrowthBytes1H, GrowthRows1H: row.GrowthRows1H,
		GrowthBytes24H: row.GrowthBytes24H, GrowthRows24H: row.GrowthRows24H,
	}
}

func tableMonitorBaseName(value string) string {
	if index := strings.LastIndexAny(value, `/\`); index >= 0 {
		return value[index+1:]
	}
	return value
}

func optionalBool(value *bool) bool {
	return value != nil && *value
}

func normalizeTableMonitorLimit(value int, fallback int, maximum int) int {
	if value <= 0 {
		return fallback
	}
	return min(value, maximum)
}

var _ port.ManagementTableMonitorReader = (*Store)(nil)
