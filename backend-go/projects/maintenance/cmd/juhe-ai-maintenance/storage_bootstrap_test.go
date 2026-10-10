// Tests for the juhe-ai-maintenance --ensure-schema / --seed commands:
// flag parsing/validation exit codes and the real SQLite end-to-end run.

package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
)

// seedActiveModelCatalogRowCount 在临时库上重放与 runStorageBootstrap
// sqlite 分支完全相同的 ensure+seed 出口（bootstrap.OpenSQLiteFile +
// schema.EnsureSQLiteBusiness + schema.SeedSQLiteDefaults，同一默认墙钟），
// 再在期望库上执行与被测断言完全同一条活跃行数查询
// （SELECT count(*) FROM provider_model_catalog）返回计数：seed 只 upsert
// ShutdownDate 仍在未来的活跃行（internal/schema activeModelCatalogSeedRows
// 按当前 UTC 日期过滤），两侧同出口、同查询、同日期口径，任何 shutdown
// 日期到达都不再分叉。不直接取 seed 出口自报的 ModelCatalogRows：断言对象
// 是落库后的表行数，期望值必须来自同一条查询才算对称，否则出口计数口径
// 一旦与表内容分叉，断言会把分叉固化而不是发现差异（原硬编码 118 即此类
// 时间炸弹，见 BUG-0221：2026-09-28 gpt-3.5-turbo-1106 到期后两侧恒差 1）。
func seedActiveModelCatalogRowCount(t *testing.T, secret string) int {
	t.Helper()
	db, err := bootstrap.OpenSQLiteFile(filepath.Join(t.TempDir(), "catalog-count-source.sqlite3"))
	if err != nil {
		t.Fatalf("open catalog count source db: %v", err)
	}
	defer db.Close()
	if _, err := schema.EnsureSQLiteBusiness(context.Background(), db); err != nil {
		t.Fatalf("ensure catalog count source schema: %v", err)
	}
	if _, err := schema.SeedSQLiteDefaults(context.Background(), db, schema.SeedOptions{Secret: secret}); err != nil {
		t.Fatalf("seed catalog count source: %v", err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM provider_model_catalog").Scan(&count); err != nil {
		t.Fatalf("query catalog count source: %v", err)
	}
	return count
}

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
	// 行数期望与被测侧同源对称计算：期望库重放同一 ensure+seed 后用同一条
	// 查询取活跃行数（按当前 UTC 日期过滤 shutdown 到期行），替换原硬编码
	// 118——目录快照演进或 shutdown 到期（如 2026-09-28 gpt-3.5-turbo-1106，
	// BUG-0221）时断言不再失真。期望库与被测库两次 seed 间隔若跨 UTC 午夜
	// 存在理论竞态，概率可忽略，不做防御。
	wantCatalogRows := seedActiveModelCatalogRowCount(t, "juhe-ai-seed-test-secret")
	if catalogRows != wantCatalogRows {
		t.Fatalf("catalog rows = %d, want %d (同源活跃种子行数)", catalogRows, wantCatalogRows)
	}
	var apiKeys int
	if err := db.QueryRow("SELECT count(*) FROM api_keys").Scan(&apiKeys); err != nil {
		t.Fatalf("query api keys: %v", err)
	}
	if apiKeys != 1 {
		// 0e83a580b 默认资源收口：默认组收窄 provider_code='gpt'，
		// seedSQLiteAdminChatAPIKey 移除，种子契约收敛为 1 个 API Key
		// （与 maintenance/internal/schema/sqlite_seed_test.go 锚点同源）。
		t.Fatalf("api keys = %d, want 1", apiKeys)
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
