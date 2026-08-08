package tablemonitor

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestLoadConfigRequiresDedicatedSQLiteOutput(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	env := sqliteTestEnv(root)
	delete(env, "JUHE_AI_TABLE_MONITOR_DATABASE_PATH")
	if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil {
		t.Fatal("expected missing dedicated output path to fail")
	}
	env["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"] = env["JUHE_AI_STATS_DATABASE_PATH"]
	if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil {
		t.Fatal("expected output/source path collision to fail")
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
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	sampledAt := time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC)
	result, err := RunOnce(ctx, cfg, store, sampledAt)
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

func sqliteTestEnv(root string) map[string]string {
	return map[string]string{
		"JUHE_AI_TABLE_MONITOR_INSTANCE_ID":      "test-instance",
		"JUHE_AI_TABLE_MONITOR_STORE":            "sqlite",
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH":    filepath.Join(root, "table-monitor.sqlite3"),
		"JUHE_AI_DATABASE_PATH":                  filepath.Join(root, "business.sqlite3"),
		"JUHE_AI_DATASET_DATABASE_PATH":          filepath.Join(root, "dataset.sqlite3"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH":    filepath.Join(root, "usage.sqlite3"),
		"JUHE_AI_STATS_DATABASE_PATH":            filepath.Join(root, "stats.sqlite3"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT": filepath.Join(root, "codex"),
	}
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
