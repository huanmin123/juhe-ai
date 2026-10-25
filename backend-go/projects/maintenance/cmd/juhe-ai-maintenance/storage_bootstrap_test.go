// Tests for the juhe-ai-maintenance --ensure-schema / --seed commands:
// usage-validation exit codes and the real PostgreSQL end-to-end run.
// SQLite 形态测试随 SQLite 存储形态退役删除
// （PLAN-20261024T051925945Z B4）。

package main

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	capdomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestRunStorageBootstrapUsageErrors(t *testing.T) {
	if code := runStorageBootstrap(true, false, "", ""); code != 2 {
		t.Fatalf("missing dsn exit = %d, want 2", code)
	}
	if code := runStorageBootstrap(true, false, "mysql://x", ""); code != 2 {
		t.Fatalf("non-postgres dsn exit = %d, want 2", code)
	}
}

// TestRunStorageBootstrapEnsureArmSeedsBuiltinSchedulingPreference 覆盖
// --ensure-schema 补种臂（调度偏好独立实体 PLAN-20261012T033000000Z）：仅
// ensure（不 seed）也必须落内置调度偏好行——存量库升级 = 跑 ensure-schema 即
// 得内置行；重复 ensure 幂等（updated_at 不漂移），权威值与契约一致。
func TestRunStorageBootstrapEnsureArmSeedsBuiltinSchedulingPreference(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		return nil
	}).DSN
	readRow := func(t *testing.T, db *sql.DB) (name, chatMode string, builtIn int, updatedAt string) {
		t.Helper()
		err := db.QueryRow(`SELECT name, chat_mode, built_in, updated_at FROM "juhe_business"."scheduling_preferences" WHERE id = $1`,
			schema.BuiltinSchedulingPreferenceID).Scan(&name, &chatMode, &builtIn, &updatedAt)
		if err != nil {
			t.Fatalf("query builtin scheduling preference after ensure-only run: %v", err)
		}
		return
	}

	if code := runStorageBootstrap(true, false, dsn, ""); code != 0 {
		t.Fatalf("ensure-only exit = %d, want 0", code)
	}
	ensureDB := func(t *testing.T) *sql.DB {
		t.Helper()
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db := ensureDB(t)
	name, chatMode, builtIn, updatedAt := readRow(t, db)
	if name != schema.BuiltinSchedulingPreferenceName || chatMode != "cost_first" || builtIn != 1 {
		t.Fatalf("builtin row = %q %q built_in=%d", name, chatMode, builtIn)
	}
	if code := runStorageBootstrap(true, false, dsn, ""); code != 0 {
		t.Fatalf("second ensure-only exit = %d, want 0", code)
	}
	_, _, _, updatedAtAfterRerun := readRow(t, db)
	if updatedAtAfterRerun != updatedAt {
		t.Fatalf("updated_at drifted on ensure rerun: %s -> %s", updatedAt, updatedAtAfterRerun)
	}
}

// TestRunStorageBootstrapPostgresEndToEnd 覆盖真实 PG ensure+seed：admin 账户、
// 目录行数与被测侧同源对称计算（同出口 EnsurePostgres+SeedPostgresDefaults 后
// 用同一条活跃行数查询）、API Key 收口契约、空库封存与幂等重跑。
func TestRunStorageBootstrapPostgresEndToEnd(t *testing.T) {
	dsn := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		return nil
	}).DSN

	if code := runStorageBootstrap(true, true, dsn, "juhe-ai-seed-test-secret"); code != 0 {
		t.Fatalf("first ensure+seed exit = %d, want 0", code)
	}
	if code := runStorageBootstrap(true, true, dsn, "juhe-ai-seed-test-secret"); code != 0 {
		t.Fatalf("second ensure+seed exit = %d, want 0", code)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var adminRows int
	if err := db.QueryRow(`SELECT count(*) FROM "juhe_business"."system_accounts" WHERE id = 'sys_admin' AND username = 'admin'`).Scan(&adminRows); err != nil {
		t.Fatalf("query seeded admin: %v", err)
	}
	if adminRows != 1 {
		t.Fatalf("admin rows = %d, want 1", adminRows)
	}
	var catalogRows int
	if err := db.QueryRow(`SELECT count(*) FROM "juhe_business"."models"`).Scan(&catalogRows); err != nil {
		t.Fatalf("query catalog: %v", err)
	}
	// 行数期望与被测侧同源对称计算：期望库重放同一 ensure+seed 后用同一条
	// 查询取活跃行数（按当前 UTC 日期过滤 shutdown 到期行），替换原硬编码——
	// 目录快照演进或 shutdown 到期时断言不再失真（BUG-0221 教训）。
	wantCatalogRows := seedActiveModelCatalogRowCount(t, "juhe-ai-seed-test-secret")
	if catalogRows != wantCatalogRows {
		t.Fatalf("catalog rows = %d, want %d (同源活跃种子行数)", catalogRows, wantCatalogRows)
	}
	var apiKeys int
	if err := db.QueryRow(`SELECT count(*) FROM "juhe_business"."api_keys"`).Scan(&apiKeys); err != nil {
		t.Fatalf("query api keys: %v", err)
	}
	if apiKeys != 1 {
		t.Fatalf("api keys = %d, want 1", apiKeys)
	}
	// 全新空库自动封存（AI 账户能力域拆分 M2）：ensure 臂对空业务库写入
	// m2_finalized 阶段记录；第二次 ensure+seed（记录已存在）幂等跳过且退出码
	// 仍为 0（上方两次 runStorageBootstrap 已覆盖）。
	record, err := capabilityDomainBootstrapStageRecord(t, db)
	if err != nil {
		t.Fatalf("read stage record: %v", err)
	}
	parsed, err := capdomain.ParseRecord(record)
	if err != nil {
		t.Fatalf("parse stage record: %v", err)
	}
	if parsed.Stage != capdomain.StageM2Finalized || parsed.RuleVersion != capdomain.RuleVersion {
		t.Fatalf("ensure 后阶段记录应已封存 m2_finalized，got %+v", parsed)
	}
}

// seedActiveModelCatalogRowCount 在期望库上重放与 runStorageBootstrap 完全
// 相同的 ensure+seed 出口（bootstrap 同名入口 → schema.EnsurePostgres +
// schema.SeedPostgresDefaults，同一默认墙钟），再执行与被测断言完全同一条
// 活跃行数查询返回计数：seed 只 upsert ShutdownDate 仍在未来的活跃行，两侧
// 同出口、同查询、同日期口径，任何 shutdown 日期到达都不再分叉。
func seedActiveModelCatalogRowCount(t *testing.T, secret string) int {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := schema.EnsurePostgres(ctx, db); err != nil {
			return err
		}
		_, err := schema.SeedPostgresDefaults(ctx, db, schema.SeedOptions{Secret: secret})
		return err
	})
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM "juhe_business"."models"`).Scan(&count); err != nil {
		t.Fatalf("query catalog count source: %v", err)
	}
	return count
}

// capabilityDomainBootstrapStageRecord 读业务库阶段记录原始 JSON（无记录报错）。
func capabilityDomainBootstrapStageRecord(t *testing.T, db *sql.DB) (string, error) {
	t.Helper()
	var valueJSON string
	err := db.QueryRow(`SELECT value_json FROM "juhe_business"."global_settings" WHERE key = $1`, capdomain.MigrationSettingKey).Scan(&valueJSON)
	return valueJSON, err
}
