package tablemonitor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestPostgresIndexSummaryScopesTargetSchema(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位 tablemonitor 测试源码")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(testFile), "sampler.go"))
	if err != nil {
		t.Fatal(err)
	}
	query := string(source)
	indexSummaryStart := strings.Index(query, "WITH index_summary AS")
	if indexSummaryStart < 0 {
		t.Fatal("未找到 PostgreSQL index_summary 查询")
	}
	indexSummary := query[indexSummaryStart:]
	indexSummaryEnd := strings.Index(indexSummary, ")\nSELECT c.relname")
	if indexSummaryEnd < 0 {
		t.Fatal("未找到 PostgreSQL index_summary 查询结尾")
	}
	indexSummary = indexSummary[:indexSummaryEnd]
	for _, requirement := range []string{
		"JOIN pg_class table_class ON table_class.oid = i.indrelid",
		"JOIN pg_namespace table_schema ON table_schema.oid = table_class.relnamespace",
		"WHERE table_schema.nspname = $1",
	} {
		if !strings.Contains(indexSummary, requirement) {
			t.Fatalf("PostgreSQL index_summary 必须按目标 schema 限定，缺少 %q", requirement)
		}
	}
}

func TestLoadConfigRequiresDedicatedSQLiteOutput(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	env := sqliteTestEnv(root)
	delete(env, "JUHE_AI_TABLE_MONITOR_DATABASE_PATH")
	if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil {
		t.Fatal("expected missing dedicated output path to fail")
	}
	env = sqliteTestEnv(root)
	delete(env, "JUHE_AI_RUNTIME_LOG_DATABASE_PATH")
	if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_DATABASE_PATH") {
		t.Fatalf("缺少 F1 专用库路径必须拒绝 F2 SQLite 启动，实际为 %v", err)
	}
	env = sqliteTestEnv(root)
	env["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"] = env["JUHE_AI_STATS_DATABASE_PATH"]
	if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil {
		t.Fatal("expected output/source path collision to fail")
	}
	env = sqliteTestEnv(root)
	env["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"] = filepath.Join(env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"], "table-monitor.sqlite3")
	if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil {
		t.Fatal("expected output inside Codex shard root to fail")
	}
	env = sqliteTestEnv(root)
	env["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"] = env["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"]
	if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_DATABASE_PATH") {
		t.Fatalf("表监控 SQLite 不能与 F1 运行日志文件共用，实际为 %v", err)
	}
	env = sqliteTestEnv(root)
	env["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"] = filepath.Join(root, "runtime-log.sqlite3")
	createSQLiteSource(t, env["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"])
	if err := os.Link(env["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"], env["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"]); err != nil {
		t.Fatalf("create F1/F2 hard-link collision fixture: %v", err)
	}
	if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_DATABASE_PATH") {
		t.Fatalf("表监控 SQLite 不能与 F1 硬链接文件共用，实际为 %v", err)
	}
	env = sqliteTestEnv(root)
	env["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"] = filepath.Join(root, "table-monitor-source-collision.sqlite3")
	createSQLiteSource(t, env["JUHE_AI_DATABASE_PATH"])
	if err := os.Link(env["JUHE_AI_DATABASE_PATH"], env["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"]); err != nil {
		t.Fatalf("create hard-link collision fixture: %v", err)
	}
	if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil {
		t.Fatal("expected hard-link output/source file identity collision to fail")
	}
}

func TestLoadConfigRejectsDanglingSQLiteAliasFailClosed(t *testing.T) {
	root := t.TempDir()
	env := sqliteTestEnv(root)
	dangling := filepath.Join(root, "dangling-runtime-log.sqlite3")
	if err := os.Symlink(filepath.Join(root, "missing-target.sqlite3"), dangling); err != nil {
		t.Skipf("当前环境不能创建悬空 SQLite symlink fixture: %v", err)
	}
	env["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"] = dangling
	if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil || !strings.Contains(err.Error(), "隔离失败") {
		t.Fatalf("悬空 SQLite symlink 必须 fail-closed 拒绝启动，实际为 %v", err)
	}
}

func TestSQLiteStoreForcesWALAndBusyTimeout(t *testing.T) {
	root := t.TempDir()
	env := sqliteTestEnv(root)
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var timeout int
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != sqliteBusyTimeoutMs {
		t.Fatalf("SQLite busy_timeout = %d, want %d", timeout, sqliteBusyTimeoutMs)
	}
	var journalMode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("SQLite journal_mode = %q, want WAL", journalMode)
	}
}

func TestLoadConfigValidatesSamplingAndRetentionBounds(t *testing.T) {
	root := t.TempDir()
	env := sqliteTestEnv(root)
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConcurrentSources != defaultMaxConcurrentSources || cfg.RetentionBatchSize != defaultRetentionBatchSize || cfg.RetentionMaxBatches != defaultRetentionMaxBatches {
		t.Fatalf("unexpected bounded execution defaults: %+v", cfg)
	}
	performance := sqliteTestEnv(root)
	performance["JUHE_AI_RUNTIME_MODE"] = "performance"
	performanceCfg, err := LoadConfig(func(key string) string { return performance[key] })
	if err != nil {
		t.Fatal(err)
	}
	if performanceCfg.MaxConcurrentSources != defaultPerformanceSources {
		t.Fatalf("SQLite performance source concurrency = %d, want %d", performanceCfg.MaxConcurrentSources, defaultPerformanceSources)
	}
	deployed := sqliteTestEnv(root)
	deployed["JUHE_AI_TABLE_MONITOR_MAX_CONCURRENT_SOURCES"] = "512"
	deployedCfg, err := LoadConfig(func(key string) string { return deployed[key] })
	if err != nil || deployedCfg.MaxConcurrentSources != 512 {
		t.Fatalf("deployed table-monitor concurrency must be accepted: cfg=%+v err=%v", deployedCfg, err)
	}
	for key, value := range map[string]string{
		"JUHE_AI_TABLE_MONITOR_MAX_CONCURRENT_SOURCES": "0",
		"JUHE_AI_TABLE_MONITOR_RETENTION_BATCH_SIZE":   "0",
		"JUHE_AI_TABLE_MONITOR_RETENTION_MAX_BATCHES":  "0",
		"JUHE_AI_TABLE_MONITOR_OWNER_LEASE":            "invalid",
	} {
		invalid := sqliteTestEnv(root)
		invalid[key] = value
		if _, err := LoadConfig(func(name string) string { return invalid[name] }); err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("%s=%s must fail fast", key, value)
		}
	}
	tooLarge := sqliteTestEnv(root)
	tooLarge["JUHE_AI_TABLE_MONITOR_MAX_CONCURRENT_SOURCES"] = "513"
	if _, err := LoadConfig(func(key string) string { return tooLarge[key] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_TABLE_MONITOR_MAX_CONCURRENT_SOURCES") {
		t.Fatalf("table-monitor concurrency above the deployed bound must fail, got %v", err)
	}
}

func TestRunOnceSQLiteSamplesSourcesAndCleansRetention(t *testing.T) {
	root := t.TempDir()
	env := sqliteTestEnv(root)
	for _, path := range []string{env["JUHE_AI_DATABASE_PATH"], env["JUHE_AI_DATASET_DATABASE_PATH"], env["JUHE_AI_USAGE_CATALOG_DATABASE_PATH"], env["JUHE_AI_STATS_DATABASE_PATH"]} {
		createSQLiteSource(t, path)
	}
	shardRoot := env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"]
	if err := os.MkdirAll(shardRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	createSQLiteSource(t, filepath.Join(shardRoot, "0.sqlite3"))
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	sampledAt := time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC)
	result, err := runAsOwner(ctx, cfg, store, func(ownerCtx context.Context) (SampleResult, error) {
		return RunOnce(ownerCtx, cfg, store, sampledAt)
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DatabaseSnapshots != 5 || result.TableSnapshots != 5 {
		t.Fatalf("unexpected sample result: %+v", result)
	}
	var databases, tables int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM database_storage_snapshots").Scan(&databases); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("SELECT COUNT(*) FROM table_storage_snapshots").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if databases != 5 || tables != 5 {
		t.Fatalf("unexpected stored counts: databases=%d tables=%d", databases, tables)
	}
	var writeErr error
	for _, path := range []string{env["JUHE_AI_DATABASE_PATH"], env["JUHE_AI_DATASET_DATABASE_PATH"]} {
		db, openErr := sql.Open("sqlite", path)
		if openErr != nil {
			t.Fatal(openErr)
		}
		_, writeErr = db.Exec("INSERT INTO source_rows(value) VALUES ('writer-check')")
		db.Close()
		if writeErr != nil {
			t.Fatalf("source fixture should remain writable outside the sampler: %v", writeErr)
		}
	}
}

func TestRunOnceSQLiteAggregatesSameNamedShardTablesAndPreservesMetrics(t *testing.T) {
	root := t.TempDir()
	env := sqliteTestEnv(root)
	for _, path := range []string{env["JUHE_AI_DATABASE_PATH"], env["JUHE_AI_DATASET_DATABASE_PATH"], env["JUHE_AI_USAGE_CATALOG_DATABASE_PATH"], env["JUHE_AI_STATS_DATABASE_PATH"]} {
		createSQLiteSource(t, path)
	}
	if err := os.MkdirAll(env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"], 0o755); err != nil {
		t.Fatal(err)
	}
	createSQLiteSource(t, filepath.Join(env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"], "0.sqlite3"))
	createSQLiteSource(t, filepath.Join(env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"], "1.sqlite3"))
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sampledAt := time.Date(2026, 8, 9, 2, 3, 4, 0, time.UTC)
	result, err := runAsOwner(context.Background(), cfg, store, func(ownerCtx context.Context) (SampleResult, error) {
		return RunOnce(ownerCtx, cfg, store, sampledAt)
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DatabaseSnapshots != 5 || result.TableSnapshots != 6 {
		t.Fatalf("two shards must produce one aggregate database row and two distinct table rows: %+v", result)
	}
	var databaseRows int
	var databasePath string
	var tableCount, indexCount int
	if err := store.db.QueryRow(`SELECT COUNT(*), MAX(database_path), MAX(table_count), MAX(index_count)
FROM database_storage_snapshots WHERE database_role = 'codex-context-state'`).Scan(&databaseRows, &databasePath, &tableCount, &indexCount); err != nil {
		t.Fatal(err)
	}
	if databaseRows != 1 || databasePath != env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"] || tableCount != 2 || indexCount != 2 {
		t.Fatalf("Codex shard database snapshot must match Node aggregate semantics: rows=%d path=%q tables=%d indexes=%d", databaseRows, databasePath, tableCount, indexCount)
	}
	rows, err := store.db.Query(`SELECT table_name, table_kind, parent_table_name, is_partition, row_count, table_bytes, index_bytes, total_bytes, page_count
FROM table_storage_snapshots WHERE database_role = 'codex-context-state' ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	wantNames := []string{"0.sqlite3:source_rows", "1.sqlite3:source_rows"}
	for index := 0; rows.Next(); index++ {
		var name, kind, parent string
		var partition int
		var rowCount, tableBytes, indexBytes, totalBytes, pageCount sql.NullInt64
		if err := rows.Scan(&name, &kind, &parent, &partition, &rowCount, &tableBytes, &indexBytes, &totalBytes, &pageCount); err != nil {
			t.Fatal(err)
		}
		if index >= len(wantNames) || name != wantNames[index] || kind != "shard_table" || parent != "source_rows" || partition != 1 {
			t.Fatalf("unexpected shard identity row: name=%q kind=%q parent=%q partition=%d", name, kind, parent, partition)
		}
		if !rowCount.Valid || !tableBytes.Valid || !indexBytes.Valid || !totalBytes.Valid || !pageCount.Valid {
			t.Fatalf("SQLite dbstat metrics must remain observable for shard %q: row=%v table=%v index=%v total=%v pages=%v", name, rowCount.Valid, tableBytes.Valid, indexBytes.Valid, totalBytes.Valid, pageCount.Valid)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteSourceUsesReadOnlyDSN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.sqlite3")
	createSQLiteSource(t, path)
	db, _, err := openSQLiteReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("INSERT INTO source_rows(value) VALUES ('must-not-write')"); err == nil {
		t.Fatal("readonly SQLite source DSN unexpectedly allowed a write")
	}
}

func TestRunOnceSQLiteCalculatesGrowthAndRepeatsRetentionBatches(t *testing.T) {
	root := t.TempDir()
	env := sqliteTestEnv(root)
	env["JUHE_AI_TABLE_MONITOR_RETENTION_BATCH_SIZE"] = "1"
	env["JUHE_AI_TABLE_MONITOR_RETENTION_MAX_BATCHES"] = "100"
	for _, path := range []string{env["JUHE_AI_DATABASE_PATH"], env["JUHE_AI_DATASET_DATABASE_PATH"], env["JUHE_AI_USAGE_CATALOG_DATABASE_PATH"], env["JUHE_AI_STATS_DATABASE_PATH"]} {
		createSQLiteSource(t, path)
	}
	if err := os.MkdirAll(env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"], 0o755); err != nil {
		t.Fatal(err)
	}
	createSQLiteSource(t, filepath.Join(env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"], "0.sqlite3"))
	createSQLiteSource(t, filepath.Join(env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"], "1.sqlite3"))
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	staleAt := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := runAsOwner(context.Background(), cfg, store, func(ownerCtx context.Context) (SampleResult, error) {
		return RunOnce(ownerCtx, cfg, store, staleAt)
	}); err != nil {
		t.Fatal(err)
	}
	business, err := sql.Open("sqlite", env["JUHE_AI_DATABASE_PATH"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := business.Exec("INSERT INTO source_rows(value) VALUES ('growth')"); err != nil {
		business.Close()
		t.Fatal(err)
	}
	business.Close()
	secondAt := staleAt.Add(25 * time.Hour)
	if _, err := runAsOwner(context.Background(), cfg, store, func(ownerCtx context.Context) (SampleResult, error) {
		return RunOnce(ownerCtx, cfg, store, secondAt)
	}); err != nil {
		t.Fatal(err)
	}
	var growth1h, growth24h sql.NullInt64
	if err := store.db.QueryRow(`SELECT growth_rows_1h, growth_rows_24h
FROM table_storage_snapshots WHERE database_role = 'business' AND table_name = 'source_rows' AND sampled_at = ?`, sqliteTimestamp(secondAt)).Scan(&growth1h, &growth24h); err != nil {
		t.Fatal(err)
	}
	if !growth1h.Valid || !growth24h.Valid || growth1h.Int64 != 1 || growth24h.Int64 != 1 {
		t.Fatalf("growth fields must use latest <= 1h/24h snapshot: 1h=%v 24h=%v", growth1h, growth24h)
	}
	retainedAt := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	result, err := runAsOwner(context.Background(), cfg, store, func(ownerCtx context.Context) (SampleResult, error) {
		return RunOnce(ownerCtx, cfg, store, retainedAt)
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedSnapshots < 22 {
		t.Fatalf("retention must continue past one batch for both snapshot tables, deleted=%d", result.DeletedSnapshots)
	}
}

func TestCleanupUntilCompleteAcceptsExactFinalBatch(t *testing.T) {
	root := t.TempDir()
	env := sqliteTestEnv(root)
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	staleAt := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	err = RunWithOwnerLease(context.Background(), cfg, store, func(ownerCtx context.Context) error {
		lease, err := ownerLeaseFromContext(ownerCtx)
		if err != nil {
			return err
		}
		if err := store.WriteSample(ownerCtx, lease, collectedSample{databases: []DatabaseSnapshot{{Role: "business", Path: "fixture", SampledAt: staleAt}}}); err != nil {
			return err
		}
		deleted, err := store.CleanupUntilComplete(ownerCtx, lease, staleAt.Add(time.Second), 1, 1)
		if err != nil {
			return err
		}
		if deleted != 1 {
			t.Fatalf("exact final retention batch must delete one row, got %d", deleted)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigRequiresExplicitStoreMode(t *testing.T) {
	_, err := LoadConfig(func(key string) string {
		if key == "JUHE_AI_TABLE_MONITOR_INSTANCE_ID" {
			return "test-instance"
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_TABLE_MONITOR_STORE") {
		t.Fatalf("missing store mode must fail explicitly, got %v", err)
	}
}

func TestCollectBoundedLimitsHighShardCounts(t *testing.T) {
	targets := make([]sqliteTarget, 96)
	for index := range targets {
		targets[index] = sqliteTarget{role: "codex-context-state", path: fmt.Sprintf("%d.sqlite3", index), shardKey: fmt.Sprintf("%d.sqlite3", index)}
	}
	var active, maximum atomic.Int64
	results, err := collectBounded(context.Background(), 3, targets, func(target sqliteTarget) (string, error) {
		current := active.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		active.Add(-1)
		return target.shardKey, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(targets) || maximum.Load() > 3 {
		t.Fatalf("high shard count must preserve all results within the configured bound: results=%d maximum=%d", len(results), maximum.Load())
	}
}

func TestCollectBoundedConvertsCallbackPanicToError(t *testing.T) {
	_, err := collectBounded(context.Background(), 2, []string{"first", "second"}, func(string) (string, error) {
		panic("fixture panic")
	})
	if err == nil || !strings.Contains(err.Error(), "fixture panic") || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("callback panic must become a component error, got %v", err)
	}
}

func TestCollectBoundedKeepsSuccessfulSourcesWhenOneSourceFails(t *testing.T) {
	results, err := collectBounded(context.Background(), 2, []string{"good", "bad"}, func(source string) (string, error) {
		if source == "bad" {
			return "", errors.New("broken SQLite source")
		}
		return source + "-sample", nil
	})
	if len(results) != 1 || results[0] != "good-sample" {
		t.Fatalf("successful source sample must be retained: results=%v", results)
	}
	if err == nil || !strings.Contains(err.Error(), "broken SQLite source") {
		t.Fatalf("source error must remain observable: %v", err)
	}
}

func TestRunOnceSQLiteWritesSuccessfulSourcesAndRunsRetentionAfterPartialFailure(t *testing.T) {
	root := t.TempDir()
	env := sqliteTestEnv(root)
	for _, path := range []string{env["JUHE_AI_DATABASE_PATH"], env["JUHE_AI_USAGE_CATALOG_DATABASE_PATH"], env["JUHE_AI_STATS_DATABASE_PATH"]} {
		createSQLiteSource(t, path)
	}
	if err := os.WriteFile(env["JUHE_AI_DATASET_DATABASE_PATH"], []byte("broken sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 23, 1, 2, 3, 500000000, time.UTC)
	stale := now.Add(-40 * 24 * time.Hour)
	var result SampleResult
	err = RunWithOwnerLease(context.Background(), cfg, store, func(ownerCtx context.Context) error {
		lease, leaseErr := ownerLeaseFromContext(ownerCtx)
		if leaseErr != nil {
			return leaseErr
		}
		if leaseErr := store.WriteSample(ownerCtx, lease, collectedSample{databases: []DatabaseSnapshot{{Role: "stale", Path: "stale", SampledAt: stale}}}); leaseErr != nil {
			return leaseErr
		}
		var runErr error
		result, runErr = RunOnce(ownerCtx, cfg, store, now)
		return runErr
	})
	if err == nil || !strings.Contains(err.Error(), "dataset") {
		t.Fatalf("partial source failure must be reported: result=%+v err=%v", result, err)
	}
	if result.DatabaseSnapshots != 3 || result.TableSnapshots != 3 {
		t.Fatalf("successful sources must still be written: result=%+v", result)
	}
	var stored int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM database_storage_snapshots WHERE database_role = 'stale'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatalf("retention must run after partial source failure: stale rows=%d", stored)
	}
}

func TestSQLiteTimestampsUseFixedWidthAndSortByInstant(t *testing.T) {
	first := sqliteTimestamp(time.Date(2026, 8, 23, 1, 2, 3, 500000000, time.UTC))
	second := sqliteTimestamp(time.Date(2026, 8, 23, 1, 2, 3, 500000001, time.UTC))
	if len(first) != len(second) || len(first) != len("2026-08-23T01:02:03.500000000Z") {
		t.Fatalf("SQLite timestamp must have fixed width: first=%q second=%q", first, second)
	}
	if first >= second {
		t.Fatalf("SQLite timestamp lexical order must match instant order: first=%q second=%q", first, second)
	}
}

func TestRunWithOwnerLeaseReleasesLeaseAfterRunPanic(t *testing.T) {
	root := t.TempDir()
	env := sqliteTestEnv(root)
	for _, path := range []string{env["JUHE_AI_DATABASE_PATH"], env["JUHE_AI_DATASET_DATABASE_PATH"], env["JUHE_AI_USAGE_CATALOG_DATABASE_PATH"], env["JUHE_AI_STATS_DATABASE_PATH"]} {
		createSQLiteSource(t, path)
	}
	if err := os.MkdirAll(env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"], 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	cfg.InstanceID = "panic-owner"
	cfg.OwnerLease = time.Minute
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	err = RunWithOwnerLease(context.Background(), cfg, store, func(context.Context) error {
		panic("table-monitor owner callback fixture panic")
	})
	if err == nil || !strings.Contains(err.Error(), "table-monitor owner callback fixture panic") {
		t.Fatalf("owner callback panic must return with its stack context, got %v", err)
	}
	lease, acquired, acquireErr := store.AcquireOwnerLease(context.Background(), "replacement-owner", time.Minute)
	if acquireErr != nil || !acquired {
		t.Fatalf("panic path must release the old F2 owner lease immediately: acquired=%t err=%v", acquired, acquireErr)
	}
	if err := store.ReleaseOwnerLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
}

func TestSelectShardWindowBoundsAndRotates(t *testing.T) {
	entries := make([]string, 96)
	for index := range entries {
		entries[index] = fmt.Sprintf("%03d.sqlite3", index)
	}
	first := selectShardWindow(entries, 3, time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), time.Minute)
	second := selectShardWindow(entries, 3, time.Date(2026, 8, 9, 0, 1, 0, 0, time.UTC), time.Minute)
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("Codex shard round must enforce the total source budget: first=%d second=%d", len(first), len(second))
	}
	if first[0] == second[0] {
		t.Fatalf("Codex shard window must rotate between sampling slots: first=%v second=%v", first, second)
	}
}

func TestOwnerLeaseRejectsSecondWriterAndFencesFormerOwner(t *testing.T) {
	root := t.TempDir()
	env := sqliteTestEnv(root)
	for _, path := range []string{env["JUHE_AI_DATABASE_PATH"], env["JUHE_AI_DATASET_DATABASE_PATH"], env["JUHE_AI_USAGE_CATALOG_DATABASE_PATH"], env["JUHE_AI_STATS_DATABASE_PATH"]} {
		createSQLiteSource(t, path)
	}
	if err := os.MkdirAll(env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"], 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	first, acquired, err := store.AcquireOwnerLease(ctx, "first", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first owner acquire failed: acquired=%v err=%v", acquired, err)
	}
	if _, acquired, err := store.AcquireOwnerLease(ctx, "second", time.Minute); err != nil || acquired {
		t.Fatalf("second owner must be rejected: acquired=%v err=%v", acquired, err)
	}
	if err := store.ReleaseOwnerLease(ctx, first); err != nil {
		t.Fatal(err)
	}
	second, acquired, err := store.AcquireOwnerLease(ctx, "second", time.Minute)
	if err != nil || !acquired || second.FenceToken <= first.FenceToken {
		t.Fatalf("second owner handoff failed: lease=%+v acquired=%v err=%v", second, acquired, err)
	}
	staleCtx := context.WithValue(ctx, ownerLeaseContextKey{}, first)
	if _, err := RunOnce(staleCtx, cfg, store, time.Now().UTC()); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("stale owner must not write: %v", err)
	}
	var stored int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM database_storage_snapshots").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatalf("stale owner wrote %d snapshots", stored)
	}
}

func TestOwnerLeaseBootstrapSerializesIndependentStores(t *testing.T) {
	root := t.TempDir()
	env := sqliteTestEnv(root)
	for _, path := range []string{env["JUHE_AI_DATABASE_PATH"], env["JUHE_AI_DATASET_DATABASE_PATH"], env["JUHE_AI_USAGE_CATALOG_DATABASE_PATH"], env["JUHE_AI_STATS_DATABASE_PATH"]} {
		createSQLiteSource(t, path)
	}
	if err := os.MkdirAll(env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"], 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	firstStore, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.Close()
	secondStore, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	start := make(chan struct{})
	type acquiredResult struct {
		lease    OwnerLease
		acquired bool
		err      error
	}
	results := make(chan acquiredResult, 2)
	var wait sync.WaitGroup
	for storeIndex, store := range []*Store{firstStore, secondStore} {
		wait.Add(1)
		go func(owner string, candidate *Store) {
			defer wait.Done()
			<-start
			lease, acquired, err := candidate.AcquireOwnerLease(context.Background(), owner, time.Minute)
			results <- acquiredResult{lease: lease, acquired: acquired, err: err}
		}(fmt.Sprintf("owner-%d", storeIndex), store)
	}
	close(start)
	wait.Wait()
	close(results)
	var winner OwnerLease
	winners := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.acquired {
			winners++
			winner = result.lease
		}
	}
	if winners != 1 {
		t.Fatalf("exactly one independent store must bootstrap and acquire the owner lease, winners=%d", winners)
	}
	if err := firstStore.ReleaseOwnerLease(context.Background(), winner); err != nil && !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatal(err)
	}
}

func sqliteTestEnv(root string) map[string]string {
	return map[string]string{
		"JUHE_AI_TABLE_MONITOR_INSTANCE_ID":      "test-instance",
		"JUHE_AI_TABLE_MONITOR_STORE":            "sqlite",
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH":    filepath.Join(root, "table-monitor.sqlite3"),
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH":      filepath.Join(root, "runtime-log.sqlite3"),
		"JUHE_AI_DATABASE_PATH":                  filepath.Join(root, "business.sqlite3"),
		"JUHE_AI_DATASET_DATABASE_PATH":          filepath.Join(root, "dataset.sqlite3"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH":    filepath.Join(root, "usage.sqlite3"),
		"JUHE_AI_STATS_DATABASE_PATH":            filepath.Join(root, "stats.sqlite3"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT": filepath.Join(root, "codex"),
	}
}

func runAsOwner(ctx context.Context, cfg Config, store *Store, run func(context.Context) (SampleResult, error)) (SampleResult, error) {
	var result SampleResult
	err := RunWithOwnerLease(ctx, cfg, store, func(ownerCtx context.Context) error {
		var err error
		result, err = run(ownerCtx)
		return err
	})
	return result, err
}

func createSQLiteSource(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE source_rows (id INTEGER PRIMARY KEY, value TEXT NOT NULL); INSERT INTO source_rows(value) VALUES ('fixture'); CREATE INDEX source_rows_value ON source_rows(value);"); err != nil {
		t.Fatal(err)
	}
}
