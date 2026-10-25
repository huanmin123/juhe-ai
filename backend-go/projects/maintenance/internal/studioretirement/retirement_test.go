// studioretirement 测试（PG opt-in live smoke 门禁
// TestPostgresStudioRetirementLiveSmoke，环境变量
// JUHE_AI_STUDIO_RETIREMENT_PG_SMOKE_URL 指向一次性隔离库时才运行）：
//   - 全量 legacy 工件：4 张真实 studio 表（冒烟造 1 张）+ 遗留索引 +
//     purpose='studio' 行 + studio_domains_json 列 → Run 全部清理，
//     general Key 保真，Ready；重跑 AlreadyRetired 零写入；
//   - 拒绝路径：OpenAI-compatible 资源仍关联 studio Key 时写入前拒绝。
// SQLite 真库臂（全量工件/保行重建/形状钉死）随 SQLite 存储形态退役删除
// （PLAN-20261024T051925945Z B4）。

package studioretirement

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	_ "github.com/jackc/pgx/v5/stdlib"
)

var fixtureClock = time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)

// TestPostgresStudioRetirementLiveSmoke 是 opt-in 真库测试：设
// JUHE_AI_STUDIO_RETIREMENT_PG_SMOKE_URL 指向一次性隔离库时运行真实 PG 方言
// 全流程（EnsurePostgres 建表 → 造 legacy studio 工件 → Run → 重跑），
// plain `go test` 跳过（口径同 schedprefmigration live smoke）。
func TestPostgresStudioRetirementLiveSmoke(t *testing.T) {
	databaseURL := os.Getenv("JUHE_AI_STUDIO_RETIREMENT_PG_SMOKE_URL")
	if databaseURL == "" {
		t.Skip("JUHE_AI_STUDIO_RETIREMENT_PG_SMOKE_URL not set; PostgreSQL studio retirement smoke test skipped")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open pgx: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}
	if _, err := schema.EnsurePostgres(ctx, db); err != nil {
		t.Fatalf("ensure postgres: %v", err)
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	seed(`INSERT INTO "juhe_business"."system_accounts"
		(id, username, display_name, description, role, status, password_hash, created_at, updated_at)
		VALUES ($1, $2, $3, NULL, 'user', 'active', 'hash', '2026-10-14T08:00:00Z', '2026-10-14T08:00:00Z')`,
		"sys-smoke", "studio-smoke", "studio 冒烟")
	seed(`INSERT INTO "juhe_business"."route_strategies"
		(id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at)
		VALUES ($1, $2, $3, NULL, 'normal', 'active', 0, NULL, '2026-10-14T08:00:00Z', '2026-10-14T08:00:00Z')`,
		"st-smoke", "sys-smoke", "策略冒烟")
	seed(`INSERT INTO "juhe_business"."api_keys"
		(id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
		 key_secret_encrypted, status, is_default, purpose, scheduling_preference_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NULL, $5, 'sk-prefix', 'suf', 'enc', 'active', 0, 'general', NULL, '2026-10-14T08:00:00Z', '2026-10-14T08:00:00Z')`,
		"k-smoke", "sys-smoke", "st-smoke", "k-smoke", "hash-smoke")
	seed(`INSERT INTO "juhe_business"."api_keys"
		(id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
		 key_secret_encrypted, status, is_default, purpose, scheduling_preference_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NULL, $5, 'sk-prefix', 'suf', 'enc', 'active', 0, 'studio', NULL, '2026-10-14T08:00:00Z', '2026-10-14T08:00:00Z')`,
		"k-smoke-studio", "sys-smoke", "st-smoke", "k-smoke-studio", "hash-smoke-studio")
	seed(`CREATE TABLE "juhe_business"."studio_assets" (id text PRIMARY KEY, created_at text NOT NULL)`)
	seed(`INSERT INTO "juhe_business"."studio_assets" (id, created_at) VALUES ('asset-smoke', '2026-10-14T08:00:00Z')`)
	seed(`CREATE UNIQUE INDEX "juhe_business".idx_api_keys_studio_purpose_unique ON "juhe_business".api_keys(system_account_id) WHERE purpose = 'studio'`)
	seed(`ALTER TABLE "juhe_business".api_keys ADD COLUMN studio_domains_json text`)

	report, err := Run(ctx, db, fixtureClock)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("PG report not ready: %+v", report)
	}
	if report.StudioAPIKeyRows != 1 || report.DeletedAPIKeyRows != 1 {
		t.Fatalf("PG studio key rows: preflight=%d deleted=%d, want 1/1", report.StudioAPIKeyRows, report.DeletedAPIKeyRows)
	}
	if len(report.DroppedTables) != 1 || !report.DroppedIndex || !report.DroppedColumn || report.RebuiltTable {
		t.Fatalf("PG actions = %+v", report)
	}
	var generalKeys int
	if err := db.QueryRow(`SELECT count(*) FROM "juhe_business".api_keys WHERE purpose = 'general'`).Scan(&generalKeys); err != nil || generalKeys != 1 {
		t.Fatalf("PG general key must survive: %v %d", err, generalKeys)
	}
	rerun, err := Run(ctx, db, fixtureClock)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !rerun.Ready() || !rerun.AlreadyRetired {
		t.Fatalf("PG rerun = %+v, want ready/alreadyRetired", rerun)
	}
}


// TestPostgresStudioRetirementRefusesWhenOpenAICompatibleResourcesRemain 是
// opt-in 真库测试（同上 URL 门禁）：关联 OpenAI-compatible 资源的 studio Key
// 在写入前被拒绝（返回错误、零 DELETE/DROP）。
func TestPostgresStudioRetirementRefusesWhenOpenAICompatibleResourcesRemain(t *testing.T) {
	databaseURL := os.Getenv("JUHE_AI_STUDIO_RETIREMENT_PG_SMOKE_URL")
	if databaseURL == "" {
		t.Skip("JUHE_AI_STUDIO_RETIREMENT_PG_SMOKE_URL not set; PostgreSQL studio retirement smoke test skipped")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open pgx: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}
	if _, err := schema.EnsurePostgres(ctx, db); err != nil {
		t.Fatalf("ensure postgres: %v", err)
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	seed(`INSERT INTO "juhe_business"."system_accounts"
		(id, username, display_name, description, role, status, password_hash, created_at, updated_at)
		VALUES ($1, $2, $3, NULL, 'user', 'active', 'hash', '2026-10-14T08:00:00Z', '2026-10-14T08:00:00Z')`,
		"sys-refuse", "studio-refuse", "studio 拒绝冒烟")
	seed(`INSERT INTO "juhe_business"."route_strategies"
		(id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at)
		VALUES ($1, $2, $3, NULL, 'normal', 'active', 0, NULL, '2026-10-14T08:00:00Z', '2026-10-14T08:00:00Z')`,
		"st-refuse", "sys-refuse", "策略冒烟")
	seed(`INSERT INTO "juhe_business"."api_keys"
		(id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
		 key_secret_encrypted, status, is_default, purpose, scheduling_preference_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NULL, $5, 'sk-prefix', 'suf', 'enc', 'active', 0, 'studio', NULL, '2026-10-14T08:00:00Z', '2026-10-14T08:00:00Z')`,
		"k-refuse-studio", "sys-refuse", "st-refuse", "k-refuse-studio", "hash-refuse")
	seed(`INSERT INTO "juhe_business"."openai_compatible_files"
		(id, system_account_id, api_key_id, purpose, filename, bytes, storage_key, sha256, created_at, updated_at)
		VALUES ('file-refuse', 'sys-refuse', 'k-refuse-studio', 'user', 'a.txt', 1, 'studio-refuse/a.txt', 'deadbeef', '2026-10-14T08:00:00Z', '2026-10-14T08:00:00Z')`)

	if _, err := Run(ctx, db, fixtureClock); err == nil {
		t.Fatal("retirement must be refused while OpenAI-compatible resources remain")
	}
	var deleted int
	if err := db.QueryRow(`SELECT count(*) FROM "juhe_business".api_keys WHERE purpose = 'studio'`).Scan(&deleted); err != nil || deleted != 1 {
		t.Fatalf("refused run must not delete studio keys: %v %d", err, deleted)
	}
}
