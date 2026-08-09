package tablemonitor

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type collectedSample struct {
	databases []DatabaseSnapshot
	tables    []TableSnapshot
}

type sqliteTarget struct {
	role     string
	path     string
	shardKey string
}

type postgresTarget struct {
	role   string
	schema string
	path   string
}

func RunOnce(ctx context.Context, cfg Config, store *Store, now time.Time) (SampleResult, error) {
	lease, err := ownerLeaseFromContext(ctx)
	if err != nil {
		return SampleResult{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var collected collectedSample
	if cfg.Mode == ModeSQLite {
		collected, err = collectSQLite(ctx, cfg, now)
	} else {
		collected, err = collectPostgres(ctx, cfg, now)
	}
	if err != nil {
		return SampleResult{}, err
	}
	if err := store.populateGrowth(ctx, &collected); err != nil {
		return SampleResult{}, fmt.Errorf("读取表监控历史增长基线失败: %w", err)
	}
	if err := store.WriteSample(ctx, lease, collected); err != nil {
		return SampleResult{}, fmt.Errorf("写入表监控快照失败: %w", err)
	}
	cutoff := now.UTC().Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)
	deleted, err := store.CleanupUntilComplete(ctx, lease, cutoff, cfg.RetentionBatchSize, cfg.RetentionMaxBatches)
	if err != nil {
		return SampleResult{}, fmt.Errorf("清理表监控快照失败: %w", err)
	}
	return SampleResult{SampledAt: now.UTC(), DatabaseSnapshots: len(collected.databases), TableSnapshots: len(collected.tables), DeletedSnapshots: deleted}, nil
}

func collectSQLite(ctx context.Context, cfg Config, sampledAt time.Time) (collectedSample, error) {
	targets := []sqliteTarget{
		{role: "business", path: cfg.BusinessPath},
		{role: "dataset", path: cfg.DatasetPath},
		{role: "usage-catalog", path: cfg.UsageCatalogPath},
		{role: "stats", path: cfg.StatsPath},
	}
	entries, err := filepath.Glob(filepath.Join(cfg.CodexShardRoot, "*.sqlite3"))
	if err != nil {
		return collectedSample{}, fmt.Errorf("枚举 Codex context shard 失败: %w", err)
	}
	sort.Strings(entries)
	for _, path := range entries {
		targets = append(targets, sqliteTarget{role: "codex-context-state", path: path, shardKey: filepath.Base(path)})
	}
	parts, err := collectBounded(ctx, cfg.MaxConcurrentSources, targets, func(target sqliteTarget) (collectedSample, error) {
		return collectSQLiteTarget(ctx, target, sampledAt, cfg.MaxTables)
	})
	if err != nil {
		return collectedSample{}, err
	}
	var collected collectedSample
	shards := make([]collectedSample, 0, len(entries))
	for index, part := range parts {
		if targets[index].role == "codex-context-state" {
			shards = append(shards, part)
			continue
		}
		collected.databases = append(collected.databases, part.databases...)
		collected.tables = append(collected.tables, part.tables...)
	}
	if len(shards) > 0 {
		database, tables, err := aggregateCodexShards(cfg.CodexShardRoot, sampledAt, shards)
		if err != nil {
			return collectedSample{}, err
		}
		collected.databases = append(collected.databases, database)
		collected.tables = append(collected.tables, tables...)
	}
	sort.Slice(collected.databases, func(i, j int) bool { return collected.databases[i].Role < collected.databases[j].Role })
	sort.Slice(collected.tables, func(i, j int) bool {
		if collected.tables[i].Role != collected.tables[j].Role {
			return collected.tables[i].Role < collected.tables[j].Role
		}
		return collected.tables[i].TableName < collected.tables[j].TableName
	})
	return collected, nil
}

func collectSQLiteTarget(ctx context.Context, target sqliteTarget, sampledAt time.Time, maxTables int) (collectedSample, error) {
	db, info, err := openSQLiteReadOnly(target.path)
	if err != nil {
		return collectedSample{}, fmt.Errorf("打开表监控源库 %s 失败: %w", target.role, err)
	}
	defer db.Close()
	pageSize, err := pragmaInt(ctx, db, "page_size")
	if err != nil {
		return collectedSample{}, fmt.Errorf("读取表监控源库 %s page_size 失败: %w", target.role, err)
	}
	pageCount, err := pragmaInt(ctx, db, "page_count")
	if err != nil {
		return collectedSample{}, fmt.Errorf("读取表监控源库 %s page_count 失败: %w", target.role, err)
	}
	freelist, err := pragmaInt(ctx, db, "freelist_count")
	if err != nil {
		return collectedSample{}, fmt.Errorf("读取表监控源库 %s freelist_count 失败: %w", target.role, err)
	}
	tables, tableCount, indexCount, err := listSQLiteTables(ctx, db, maxTables)
	if err != nil {
		return collectedSample{}, fmt.Errorf("读取表监控源库 %s 目录失败: %w", target.role, err)
	}
	fileBytes := pageSize * pageCount
	used := pageSize * (pageCount - freelist)
	free := pageSize * freelist
	walBytes, err := optionalFileBytes(target.path + "-wal")
	if err != nil {
		return collectedSample{}, fmt.Errorf("读取表监控源库 %s WAL 大小失败: %w", target.role, err)
	}
	shmBytes, err := optionalFileBytes(target.path + "-shm")
	if err != nil {
		return collectedSample{}, fmt.Errorf("读取表监控源库 %s SHM 大小失败: %w", target.role, err)
	}
	database := DatabaseSnapshot{Role: target.role, Path: target.path, SampledAt: sampledAt, FileBytes: &fileBytes, WALBytes: walBytes, SHMBytes: shmBytes, PageSize: &pageSize, PageCount: &pageCount, FreelistCount: &freelist, UsedBytes: &used, FreeBytes: &free, TableCount: tableCount, IndexCount: indexCount}
	rows := make([]TableSnapshot, 0, len(tables))
	for _, table := range tables {
		count, err := sqliteRowCount(ctx, db, table.name)
		if err != nil {
			return collectedSample{}, fmt.Errorf("读取表监控源库 %s.%s 行数失败: %w", target.role, table.name, err)
		}
		indexes, err := sqliteIndexNames(ctx, db, table.name)
		if err != nil {
			return collectedSample{}, err
		}
		objectSizes, available, err := loadSQLiteObjectSizes(ctx, db, append([]string{table.name}, indexes...))
		if err != nil {
			return collectedSample{}, fmt.Errorf("读取表监控源库 %s.%s dbstat 失败: %w", target.role, table.name, err)
		}
		var tableBytes, indexBytes, totalBytes, pageTotal *int64
		if available {
			tableBytes, pageTotal = objectSizes.bytes(table.name), objectSizes.pages(table.name)
			indexBytes = sumObjectBytes(objectSizes, indexes)
			indexPages := sumObjectPages(objectSizes, indexes)
			totalBytes = addOptionalInt64(tableBytes, indexBytes)
			pageTotal = addOptionalInt64(pageTotal, indexPages)
		}
		tableName := table.name
		kind := "table"
		var parent *string
		isPartition := false
		if target.shardKey != "" {
			tableName = target.shardKey + ":" + table.name
			kind = "shard_table"
			logicalName := table.name
			parent = &logicalName
			isPartition = true
		}
		rows = append(rows, TableSnapshot{Role: target.role, TableName: tableName, SampledAt: sampledAt, TableKind: kind, ParentTableName: parent, IsPartition: isPartition, RowCount: &count, TableBytes: tableBytes, IndexBytes: indexBytes, TotalBytes: totalBytes, PageCount: pageTotal, IndexCount: len(indexes)})
	}
	if info.Size() < 0 {
		return collectedSample{}, fmt.Errorf("表监控源库 %s 文件大小无效", target.role)
	}
	return collectedSample{databases: []DatabaseSnapshot{database}, tables: rows}, nil
}

func openSQLiteReadOnly(path string) (*sql.DB, os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("不是常规 SQLite 文件")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, info, nil
}

func optionalFileBytes(path string) (*int64, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	bytes := info.Size()
	return &bytes, nil
}

type sqliteTable struct{ name string }

func listSQLiteTables(ctx context.Context, db *sql.DB, maxTables int) ([]sqliteTable, int, int, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name ASC LIMIT ?", maxTables)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	var tables []sqliteTable
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, 0, 0, err
		}
		tables = append(tables, sqliteTable{name: name})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	var tableCount, indexCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'").Scan(&tableCount); err != nil {
		return nil, 0, 0, err
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE type = 'index' AND name NOT LIKE 'sqlite_%'").Scan(&indexCount); err != nil {
		return nil, 0, 0, err
	}
	return tables, tableCount, indexCount, nil
}

func sqliteRowCount(ctx context.Context, db *sql.DB, name string) (int64, error) {
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", quoteSQLiteIdentifier(name))
	var count int64
	err := db.QueryRowContext(ctx, query).Scan(&count)
	return count, err
}

func sqliteIndexNames(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type = 'index' AND tbl_name = ? AND name NOT LIKE 'sqlite_%' ORDER BY name ASC", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

type sqliteObjectSize struct {
	bytes int64
	pages int64
}

type sqliteObjectSizes map[string]sqliteObjectSize

func (sizes sqliteObjectSizes) bytes(name string) *int64 {
	value, ok := sizes[name]
	if !ok {
		return nil
	}
	return &value.bytes
}

func (sizes sqliteObjectSizes) pages(name string) *int64 {
	value, ok := sizes[name]
	if !ok {
		return nil
	}
	return &value.pages
}

func loadSQLiteObjectSizes(ctx context.Context, db *sql.DB, names []string) (sqliteObjectSizes, bool, error) {
	sizes := make(sqliteObjectSizes, len(names))
	for _, name := range names {
		var bytes, pages sql.NullInt64
		err := db.QueryRowContext(ctx, "SELECT SUM(pgsize), COUNT(*) FROM dbstat WHERE name = ?", name).Scan(&bytes, &pages)
		if err != nil {
			if isDBStatUnavailable(err) {
				return nil, false, nil
			}
			return nil, false, err
		}
		if bytes.Valid && pages.Valid {
			sizes[name] = sqliteObjectSize{bytes: bytes.Int64, pages: pages.Int64}
		}
	}
	return sizes, true, nil
}

func isDBStatUnavailable(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such table: dbstat") || strings.Contains(message, "no such module: dbstat")
}

func sumObjectBytes(sizes sqliteObjectSizes, names []string) *int64 {
	var total int64
	for _, name := range names {
		value, ok := sizes[name]
		if !ok {
			return nil
		}
		total += value.bytes
	}
	return &total
}

func sumObjectPages(sizes sqliteObjectSizes, names []string) *int64 {
	var total int64
	for _, name := range names {
		value, ok := sizes[name]
		if !ok {
			return nil
		}
		total += value.pages
	}
	return &total
}

func addOptionalInt64(left, right *int64) *int64 {
	if left == nil || right == nil {
		return nil
	}
	total := *left + *right
	return &total
}

func pragmaInt(ctx context.Context, db *sql.DB, name string) (int64, error) {
	var value int64
	err := db.QueryRowContext(ctx, "PRAGMA "+name).Scan(&value)
	return value, err
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func aggregateCodexShards(root string, sampledAt time.Time, shards []collectedSample) (DatabaseSnapshot, []TableSnapshot, error) {
	var tableCount, indexCount int
	var fileBytes, walBytes, shmBytes, pageCount, freelist, usedBytes, freeBytes int64
	var pageSize *int64
	pageSizeConsistent := true
	var tables []TableSnapshot
	for _, shard := range shards {
		if len(shard.databases) != 1 {
			return DatabaseSnapshot{}, nil, fmt.Errorf("Codex shard 数据库快照数量异常: %d", len(shard.databases))
		}
		database := shard.databases[0]
		if database.PageSize == nil || database.PageCount == nil || database.FreelistCount == nil || database.FileBytes == nil || database.UsedBytes == nil || database.FreeBytes == nil {
			return DatabaseSnapshot{}, nil, fmt.Errorf("Codex shard %s 缺少完整数据库统计", database.Path)
		}
		if pageSize == nil && pageSizeConsistent {
			value := *database.PageSize
			pageSize = &value
		} else if pageSizeConsistent && *pageSize != *database.PageSize {
			pageSize = nil
			pageSizeConsistent = false
		}
		fileBytes += *database.FileBytes
		pageCount += *database.PageCount
		freelist += *database.FreelistCount
		usedBytes += *database.UsedBytes
		freeBytes += *database.FreeBytes
		tableCount += database.TableCount
		indexCount += database.IndexCount
		if database.WALBytes == nil {
			walBytes = -1
		} else if walBytes >= 0 {
			walBytes += *database.WALBytes
		}
		if database.SHMBytes == nil {
			shmBytes = -1
		} else if shmBytes >= 0 {
			shmBytes += *database.SHMBytes
		}
		tables = append(tables, shard.tables...)
	}
	var wal, shm *int64
	if walBytes >= 0 {
		wal = &walBytes
	}
	if shmBytes >= 0 {
		shm = &shmBytes
	}
	return DatabaseSnapshot{Role: "codex-context-state", Path: root, SampledAt: sampledAt, FileBytes: &fileBytes, WALBytes: wal, SHMBytes: shm, PageSize: pageSize, PageCount: &pageCount, FreelistCount: &freelist, UsedBytes: &usedBytes, FreeBytes: &freeBytes, TableCount: tableCount, IndexCount: indexCount}, tables, nil
}

func collectPostgres(ctx context.Context, cfg Config, sampledAt time.Time) (collectedSample, error) {
	db, err := sql.Open("pgx", cfg.PostgresURL)
	if err != nil {
		return collectedSample{}, fmt.Errorf("打开表监控 PostgreSQL 源库失败: %w", err)
	}
	defer db.Close()
	connections := min(cfg.MaxConcurrentSources, 5)
	db.SetMaxOpenConns(connections)
	db.SetMaxIdleConns(connections)
	targets := []postgresTarget{
		{role: "business", schema: "juhe_business", path: "postgres:juhe_business"},
		{role: "dataset", schema: "juhe_dataset", path: "postgres:juhe_dataset"},
		{role: "usage-catalog", schema: "juhe_usage", path: "postgres:juhe_usage"},
		{role: "stats", schema: "juhe_stats", path: "postgres:juhe_stats"},
		{role: "codex-context-state", schema: "juhe_codex_context", path: "postgres:juhe_codex_context"},
	}
	parts, err := collectBounded(ctx, cfg.MaxConcurrentSources, targets, func(target postgresTarget) (collectedSample, error) {
		return collectPostgresTarget(ctx, db, target, sampledAt, cfg.MaxTables)
	})
	if err != nil {
		return collectedSample{}, err
	}
	var collected collectedSample
	for _, part := range parts {
		collected.databases = append(collected.databases, part.databases...)
		collected.tables = append(collected.tables, part.tables...)
	}
	return collected, nil
}

func collectPostgresTarget(ctx context.Context, db *sql.DB, target postgresTarget, sampledAt time.Time, maxTables int) (collectedSample, error) {
	var blockSize int64
	if err := db.QueryRowContext(ctx, "SELECT current_setting('block_size')::bigint").Scan(&blockSize); err != nil {
		return collectedSample{}, fmt.Errorf("读取 PostgreSQL block_size 失败: %w", err)
	}
	rows, err := db.QueryContext(ctx, `WITH index_summary AS (
  SELECT i.indrelid, COALESCE(SUM(index_class.relpages), 0)::bigint AS index_pages, COUNT(*)::integer AS index_count
  FROM pg_index i
  JOIN pg_class index_class ON index_class.oid = i.indexrelid
  GROUP BY i.indrelid
)
SELECT c.relname,
  CASE c.relkind WHEN 'p' THEN 'partitioned_table' WHEN 'm' THEN 'materialized_view' ELSE 'table' END,
  parent.relname,
  (parent.oid IS NOT NULL),
  GREATEST(COALESCE(s.n_live_tup::double precision, c.reltuples, 0), 0)::bigint,
  GREATEST(c.relpages, 0)::bigint,
  COALESCE(i.index_pages, 0)::bigint,
  COALESCE(i.index_count, 0)::integer,
  pg_relation_size(c.oid)::bigint,
  pg_indexes_size(c.oid)::bigint,
  pg_total_relation_size(c.oid)::bigint
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_inherits inheritance ON inheritance.inhrelid = c.oid
LEFT JOIN pg_class parent ON parent.oid = inheritance.inhparent
LEFT JOIN pg_stat_all_tables s ON s.relid = c.oid
LEFT JOIN index_summary i ON i.indrelid = c.oid
WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'm')
ORDER BY c.relname ASC`, target.schema)
	if err != nil {
		return collectedSample{}, fmt.Errorf("读取 PostgreSQL %s 表目录失败: %w", target.schema, err)
	}
	defer rows.Close()
	type postgresCatalogRow struct {
		name, kind                                                           string
		parent                                                               sql.NullString
		partition                                                            bool
		rowCount, tablePages, indexPages, tableBytes, indexBytes, totalBytes int64
		indexCount                                                           int
	}
	var catalog []postgresCatalogRow
	for rows.Next() {
		var row postgresCatalogRow
		if err := rows.Scan(&row.name, &row.kind, &row.parent, &row.partition, &row.rowCount, &row.tablePages, &row.indexPages, &row.indexCount, &row.tableBytes, &row.indexBytes, &row.totalBytes); err != nil {
			return collectedSample{}, err
		}
		catalog = append(catalog, row)
	}
	if err := rows.Err(); err != nil {
		return collectedSample{}, err
	}
	var pageCount int64
	var indexCount int
	for _, row := range catalog {
		pageCount += row.tablePages + row.indexPages
		indexCount += row.indexCount
	}
	fileBytes := pageCount * blockSize
	database := DatabaseSnapshot{Role: target.role, Path: target.path, SampledAt: sampledAt, FileBytes: &fileBytes, PageSize: &blockSize, PageCount: &pageCount, UsedBytes: &fileBytes, TableCount: len(catalog), IndexCount: indexCount}
	limit := min(maxTables, len(catalog))
	tables := make([]TableSnapshot, 0, limit)
	for _, row := range catalog[:limit] {
		rowCount, tableBytes, indexBytes, totalBytes := row.rowCount, row.tableBytes, row.indexBytes, row.totalBytes
		pageTotal := int64(0)
		if blockSize > 0 {
			pageTotal = (totalBytes + blockSize - 1) / blockSize
		}
		var parent *string
		if row.parent.Valid {
			parentName := row.parent.String
			parent = &parentName
		}
		tables = append(tables, TableSnapshot{Role: target.role, TableName: row.name, SampledAt: sampledAt, TableKind: row.kind, ParentTableName: parent, IsPartition: row.partition, RowCount: &rowCount, TableBytes: &tableBytes, IndexBytes: &indexBytes, TotalBytes: &totalBytes, PageCount: &pageTotal, IndexCount: row.indexCount})
	}
	return collectedSample{databases: []DatabaseSnapshot{database}, tables: tables}, nil
}

func collectBounded[T any, R any](ctx context.Context, limit int, targets []T, collect func(T) (R, error)) ([]R, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("采样并发上限必须大于零")
	}
	if len(targets) == 0 {
		return []R{}, nil
	}
	workers := min(limit, len(targets))
	results := make([]R, len(targets))
	jobs := make(chan int)
	errs := make(chan error, len(targets))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if err := ctx.Err(); err != nil {
					errs <- err
					continue
				}
				result, err := collect(targets[index])
				if err != nil {
					errs <- err
					continue
				}
				results[index] = result
			}
		}()
	}
	for index := range targets {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}
