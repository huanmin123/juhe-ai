// Tests for the juhe-ai-maintenance --ensure-schema / --seed commands:
// flag parsing/validation exit codes and the real SQLite end-to-end run.

package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestParseSQLiteStoragePaths(t *testing.T) {
	parsed, err := parseSQLiteStoragePaths("business=b.sqlite,chat=c.sqlite,dataset=d.sqlite,usage-catalog=u.sqlite,stats=s.sqlite,codex-context-shard-root=shards,codex-context-shard-count=2")
	if err != nil {
		t.Fatalf("parse valid paths: %v", err)
	}
	if parsed.Business != "b.sqlite" || parsed.Chat != "c.sqlite" || parsed.Dataset != "d.sqlite" || parsed.UsageCatalog != "u.sqlite" || parsed.Stats != "s.sqlite" || parsed.CodexContextShardRoot != "shards" || parsed.CodexContextShardCount != 2 {
		t.Fatalf("parsed paths = %+v", parsed)
	}
	if _, err := parseSQLiteStoragePaths("business=b.sqlite"); err == nil || !strings.Contains(err.Error(), "缺少必填 key") {
		t.Fatalf("missing keys error = %v", err)
	}
	if _, err := parseSQLiteStoragePaths("business=b.sqlite,chat=c.sqlite,dataset=d.sqlite,usage-catalog=u.sqlite,stats=s.sqlite,codex-context-shard-root=shards,unknown=x"); err == nil || !strings.Contains(err.Error(), "未知 key") {
		t.Fatalf("unknown key error = %v", err)
	}
	if _, err := parseSQLiteStoragePaths("business=b.sqlite,business=b2.sqlite,chat=c.sqlite,dataset=d.sqlite,usage-catalog=u.sqlite,stats=s.sqlite,codex-context-shard-root=shards"); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("duplicate key error = %v", err)
	}
	if _, err := parseSQLiteStoragePaths("business=b.sqlite,chat=c.sqlite,dataset=d.sqlite,usage-catalog=u.sqlite,stats=s.sqlite,codex-context-shard-root=shards,codex-context-shard-count=0"); err == nil || !strings.Contains(err.Error(), "1 到 256") {
		t.Fatalf("shard count bound error = %v", err)
	}
}

func TestRunStorageBootstrapUsageErrors(t *testing.T) {
	if code := runStorageBootstrap(true, false, "mysql", "", "", ""); code != 2 {
		t.Fatalf("unknown driver exit = %d, want 2", code)
	}
	if code := runStorageBootstrap(true, false, "postgres", "", "", ""); code != 2 {
		t.Fatalf("postgres without dsn exit = %d, want 2", code)
	}
	if code := runStorageBootstrap(true, false, "postgres", "business=x", "", ""); code != 2 {
		t.Fatalf("postgres with paths exit = %d, want 2", code)
	}
	if code := runStorageBootstrap(true, false, "sqlite", "", "postgres://db", ""); code != 2 {
		t.Fatalf("sqlite with dsn exit = %d, want 2", code)
	}
}

func TestRunStorageBootstrapSQLiteEndToEnd(t *testing.T) {
	root := t.TempDir()
	business := filepath.Join(root, "business.sqlite3")
	paths := strings.Join([]string{
		"business=" + business,
		"chat=" + filepath.Join(root, "chat.sqlite3"),
		"dataset=" + filepath.Join(root, "dataset.sqlite3"),
		"usage-catalog=" + filepath.Join(root, "usage-catalog.sqlite3"),
		"stats=" + filepath.Join(root, "stats.sqlite3"),
		"codex-context-shard-root=" + filepath.Join(root, "shards"),
		"codex-context-shard-count=2",
	}, ",")

	if code := runStorageBootstrap(true, true, "sqlite", paths, "", "juhe-ai-seed-test-secret"); code != 0 {
		t.Fatalf("first ensure+seed exit = %d, want 0", code)
	}
	if code := runStorageBootstrap(true, true, "sqlite", paths, "", "juhe-ai-seed-test-secret"); code != 0 {
		t.Fatalf("second ensure+seed exit = %d, want 0", code)
	}

	db, err := sql.Open("sqlite", "file:"+business+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var adminRows int
	if err := db.QueryRow("SELECT count(*) FROM system_accounts WHERE id = 'sys_admin' AND username = 'admin'").Scan(&adminRows); err != nil {
		t.Fatalf("query seeded admin: %v", err)
	}
	if adminRows != 1 {
		t.Fatalf("admin rows = %d, want 1", adminRows)
	}
	var catalogRows int
	if err := db.QueryRow("SELECT count(*) FROM provider_model_catalog").Scan(&catalogRows); err != nil {
		t.Fatalf("query catalog: %v", err)
	}
	if catalogRows != 106 {
		t.Fatalf("catalog rows = %d, want 106", catalogRows)
	}
	var apiKeys int
	if err := db.QueryRow("SELECT count(*) FROM api_keys").Scan(&apiKeys); err != nil {
		t.Fatalf("query api keys: %v", err)
	}
	if apiKeys != 8 {
		t.Fatalf("api keys = %d, want 8", apiKeys)
	}
	for _, name := range []string{"chat.sqlite3", "dataset.sqlite3", "usage-catalog.sqlite3", "stats.sqlite3", filepath.Join("shards", "state-000.sqlite3"), filepath.Join("shards", "state-001.sqlite3")} {
		db, err := sql.Open("sqlite", "file:"+filepath.Join(root, filepath.FromSlash(name))+"?mode=ro")
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		var tables int
		if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table'").Scan(&tables); err != nil {
			t.Fatalf("query %s tables: %v", name, err)
		}
		_ = db.Close()
		if tables == 0 {
			t.Fatalf("%s has no tables after ensure", name)
		}
	}
}
