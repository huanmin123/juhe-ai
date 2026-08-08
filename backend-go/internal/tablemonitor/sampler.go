package tablemonitor

import (
	"context"
	"database/sql"
	"fmt"
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
	role string
	path string
}

func RunOnce(ctx context.Context, cfg Config, store *Store, now time.Time) (SampleResult, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var collected collectedSample
	var err error
	if cfg.Mode == ModeSQLite {
		collected, err = collectSQLite(ctx, cfg, now)
	} else {
		collected, err = collectPostgres(ctx, cfg, now)
	}
	if err != nil {
		return SampleResult{}, err
	}
	if err := store.WriteSample(ctx, collected); err != nil {
		return SampleResult{}, fmt.Errorf("写入表监控快照失败: %w", err)
	}
	cutoff := now.UTC().Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)
	deleted, err := store.Cleanup(ctx, cutoff, cfg.MaxTables)
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
	for index, path := range entries {
		targets = append(targets, sqliteTarget{role: "codex-context-state", path: fmt.Sprintf("codex-context-state:%d:%s", index, path)})
	}
	results := make(chan collectedSample, len(targets))
	errs := make(chan error, len(targets))
	var wg sync.WaitGroup
	for _, target := range targets {
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := collectSQLiteTarget(ctx, target, sampledAt, cfg.MaxTables)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	if err := firstError(errs); err != nil {
		return collectedSample{}, err
	}
	var collected collectedSample
	for result := range results {
		collected.databases = append(collected.databases, result.databases...)
		collected.tables = append(collected.tables, result.tables...)
	}
	sort.Slice(collected.databases, func(i, j int) bool { return collected.databases[i].Path < collected.databases[j].Path })
	sort.Slice(collected.tables, func(i, j int) bool {
		if collected.tables[i].Role != collected.tables[j].Role {
			return collected.tables[i].Role < collected.tables[j].Role
		}
		return collected.tables[i].TableName < collected.tables[j].TableName
	})
	return collected, nil
}

func collectSQLiteTarget(ctx context.Context, target sqliteTarget, sampledAt time.Time, maxTables int) (collectedSample, error) {
	path := target.path
	if strings.HasPrefix(path, "codex-context-state:") {
		parts := strings.SplitN(path, ":", 3)
		path = parts[len(parts)-1]
	}
	if _, err := os.Stat(path); err != nil {
		return collectedSample{}, fmt.Errorf("表监控源库 %s 不可用: %w", target.role, err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return collectedSample{}, fmt.Errorf("打开表监控源库 %s 失败: %w", target.role, err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return collectedSample{}, fmt.Errorf("设置表监控源库 %s 只读失败: %w", target.role, err)
	}
	pageSize, err := pragmaInt(ctx, db, "page_size")
	if err != nil {
		return collectedSample{}, err
	}
	pageCount, err := pragmaInt(ctx, db, "page_count")
	if err != nil {
		return collectedSample{}, err
	}
	freelist, err := pragmaInt(ctx, db, "freelist_count")
	if err != nil {
		return collectedSample{}, err
	}
	tables, indexes, err := listSQLiteTables(ctx, db, maxTables)
	if err != nil {
		return collectedSample{}, fmt.Errorf("读取表监控源库 %s 目录失败: %w", target.role, err)
	}
	var fileBytes *int64
	if info, statErr := os.Stat(path); statErr == nil {
		value := info.Size()
		fileBytes = &value
	}
	used := pageSize * (pageCount - freelist)
	free := pageSize * freelist
	database := DatabaseSnapshot{Role: target.role, Path: target.path, SampledAt: sampledAt, FileBytes: fileBytes, PageSize: &pageSize, PageCount: &pageCount, FreelistCount: &freelist, UsedBytes: &used, FreeBytes: &free, TableCount: len(tables), IndexCount: indexes}
	rows := make([]TableSnapshot, 0, len(tables))
	for _, table := range tables {
		count, err := sqliteRowCount(ctx, db, table.name)
		if err != nil {
			return collectedSample{}, fmt.Errorf("读取表监控源库 %s.%s 行数失败: %w", target.role, table.name, err)
		}
		indexCount, err := sqliteIndexCount(ctx, db, table.name)
		if err != nil {
			return collectedSample{}, err
		}
		rows = append(rows, TableSnapshot{Role: target.role, TableName: table.name, SampledAt: sampledAt, TableKind: "table", RowCount: &count, IndexCount: indexCount})
	}
	return collectedSample{databases: []DatabaseSnapshot{database}, tables: rows}, nil
}

type sqliteTable struct{ name string }

func listSQLiteTables(ctx context.Context, db *sql.DB, maxTables int) ([]sqliteTable, int, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name ASC LIMIT ?", maxTables)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var tables []sqliteTable
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, 0, err
		}
		tables = append(tables, sqliteTable{name: name})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var indexes int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE type = 'index' AND name NOT LIKE 'sqlite_%'").Scan(&indexes); err != nil {
		return nil, 0, err
	}
	return tables, indexes, nil
}

func sqliteRowCount(ctx context.Context, db *sql.DB, name string) (int64, error) {
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", quoteSQLiteIdentifier(name))
	var count int64
	err := db.QueryRowContext(ctx, query).Scan(&count)
	return count, err
}

func sqliteIndexCount(ctx context.Context, db *sql.DB, table string) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE type = 'index' AND tbl_name = ? AND name NOT LIKE 'sqlite_%'", table).Scan(&count)
	return count, err
}

func pragmaInt(ctx context.Context, db *sql.DB, name string) (int64, error) {
	var value int64
	err := db.QueryRowContext(ctx, "PRAGMA "+name).Scan(&value)
	return value, err
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func collectPostgres(ctx context.Context, cfg Config, sampledAt time.Time) (collectedSample, error) {
	db, err := sql.Open("pgx", cfg.PostgresURL)
	if err != nil {
		return collectedSample{}, fmt.Errorf("打开表监控 PostgreSQL 源库失败: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	targets := []struct{ role, schema, path string }{
		{"business", "juhe_business", "postgres:juhe_business"},
		{"dataset", "juhe_dataset", "postgres:juhe_dataset"},
		{"usage-catalog", "juhe_usage", "postgres:juhe_usage"},
		{"stats", "juhe_stats", "postgres:juhe_stats"},
		{"codex-context-state", "juhe_codex_context", "postgres:juhe_codex_context"},
	}
	results := make(chan collectedSample, len(targets))
	errs := make(chan error, len(targets))
	var wg sync.WaitGroup
	for _, target := range targets {
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := collectPostgresTarget(ctx, db, target.role, target.schema, target.path, sampledAt, cfg.MaxTables)
			if err != nil {
				errs <- err
			} else {
				results <- result
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	if err := firstError(errs); err != nil {
		return collectedSample{}, err
	}
	var collected collectedSample
	for result := range results {
		collected.databases = append(collected.databases, result.databases...)
		collected.tables = append(collected.tables, result.tables...)
	}
	return collected, nil
}

func collectPostgresTarget(ctx context.Context, db *sql.DB, role, schema, path string, sampledAt time.Time, maxTables int) (collectedSample, error) {
	rows, err := db.QueryContext(ctx, `SELECT c.relname, GREATEST(COALESCE(s.n_live_tup, c.reltuples), 0)::bigint, pg_relation_size(c.oid), pg_total_relation_size(c.oid), (SELECT COUNT(*) FROM pg_index i WHERE i.indrelid = c.oid)::int FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid WHERE n.nspname = $1 AND c.relkind IN ('r','p','m') ORDER BY c.relname LIMIT $2`, schema, maxTables)
	if err != nil {
		return collectedSample{}, fmt.Errorf("读取 PostgreSQL %s 表目录失败: %w", schema, err)
	}
	defer rows.Close()
	tables := make([]TableSnapshot, 0, maxTables)
	for rows.Next() {
		var name string
		var rowCount, tableBytes, totalBytes, indexCount int64
		if err := rows.Scan(&name, &rowCount, &tableBytes, &totalBytes, &indexCount); err != nil {
			return collectedSample{}, err
		}
		indexBytes := totalBytes - tableBytes
		tables = append(tables, TableSnapshot{Role: role, TableName: name, SampledAt: sampledAt, TableKind: "table", RowCount: &rowCount, TableBytes: &tableBytes, IndexBytes: &indexBytes, TotalBytes: &totalBytes, IndexCount: int(indexCount)})
	}
	if err := rows.Err(); err != nil {
		return collectedSample{}, err
	}
	var tableCount, indexCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relkind IN ('r','p','m')", schema).Scan(&tableCount); err != nil {
		return collectedSample{}, err
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relkind = 'i'", schema).Scan(&indexCount); err != nil {
		return collectedSample{}, err
	}
	return collectedSample{databases: []DatabaseSnapshot{{Role: role, Path: path, SampledAt: sampledAt, TableCount: tableCount, IndexCount: indexCount}}, tables: tables}, nil
}

func firstError(errors <-chan error) error {
	for err := range errors {
		if err != nil {
			return err
		}
	}
	return nil
}
