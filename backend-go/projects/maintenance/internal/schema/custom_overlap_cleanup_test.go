// 契约 2026-09-30（docs/functions/自定义模型与模型映射设计.md 第 4 节）：自定义
// 模型不得与内置模型同名（内置权威优先）。本文件锁定 model catalog seed 在目录
// upsert 后的 custom_provider_models 存量清理：SQLite 走真实库行为断言（同名
// 可见行删除、shutdown 过滤行与不同名行保留、计数与幂等），PostgreSQL 走语句
// 契约 + 录制驱动的计数接线（真实 PG 行为由 JUHE_AI_PG_SCHEMA_SMOKE_URL 门控
// 的 smoke 测试路径覆盖）。

package schema

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestSeedSQLiteCustomModelOverlapCleanup 预置三条 custom 行后跑 seed：与本次
// seed 运行时可见内置行同名的行被删除，与 shutdown 过滤行同名及不同名的行
// 保留；CustomModelOverlapCleaned 计数正确且重复 seed 归零。
func TestSeedSQLiteCustomModelOverlapCleanup(t *testing.T) {
	db := openSeedTestDatabase(t)
	ctx := context.Background()
	// 时钟 2026-11-01：快照中 shutdown 2026-10-23 的行（如 gpt/o4-mini）被
	// activeModelCatalogSeedRows 过滤出可见集合，gpt-6.1-sol（无 shutdown）
	// 保持可见。
	clock := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	options := SeedOptions{Now: func() time.Time { return clock }, Secret: sqliteSeedTestSecret}
	// 基线 seed 建好 providers 与目录（真实存量库上发布 seed 的形态）。
	if _, err := SeedSQLiteDefaults(ctx, db, options); err != nil {
		t.Fatalf("baseline seed: %v", err)
	}
	const stamp = "2026-10-31T00:00:00.000Z"
	insertCustom := func(id, model string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO custom_provider_models
			(id, provider_code, model, scope, system_account_id, status, created_by, created_at, updated_at)
			VALUES (?, 'gpt', ?, 'global', NULL, 'active', 'sys_admin', ?, ?)`, id, model, stamp, stamp); err != nil {
			t.Fatalf("insert custom %s: %v", id, err)
		}
	}
	insertCustom("cu-overlap-visible", "gpt-6.1-sol")
	insertCustom("cu-overlap-shutdown", "o4-mini")
	insertCustom("cu-no-overlap", "juhe-custom-own-model")

	result, err := SeedSQLiteDefaults(ctx, db, options)
	if err != nil {
		t.Fatalf("cleanup seed: %v", err)
	}
	if result.CustomModelOverlapCleaned != 1 {
		t.Fatalf("CustomModelOverlapCleaned = %d, want 1", result.CustomModelOverlapCleaned)
	}
	if got := countSeedTestRows(t, db, `SELECT count(*) FROM custom_provider_models WHERE id = 'cu-overlap-visible'`); got != 0 {
		t.Fatalf("可见内置同名 custom 行必须被删除: %d", got)
	}
	if got := countSeedTestRows(t, db, `SELECT count(*) FROM custom_provider_models WHERE id = 'cu-overlap-shutdown'`); got != 1 {
		t.Fatalf("shutdown 过滤的 seed 行对应的 custom 行不删: %d", got)
	}
	if got := countSeedTestRows(t, db, `SELECT count(*) FROM custom_provider_models WHERE id = 'cu-no-overlap'`); got != 1 {
		t.Fatalf("不同名 custom 行必须保留: %d", got)
	}

	// 幂等：再次 seed 无可清理行，计数归零。
	again, err := SeedSQLiteDefaults(ctx, db, options)
	if err != nil {
		t.Fatalf("idempotent seed: %v", err)
	}
	if again.CustomModelOverlapCleaned != 0 {
		t.Fatalf("重复 seed CustomModelOverlapCleaned = %d, want 0", again.CustomModelOverlapCleaned)
	}
}

// TestSeedPostgresCustomModelOverlapCleanupContract 锁定 PG 清理步的语句契约
// 与计数接线：DELETE 以 jsonb_to_recordset 匹配本次 seed 可见行键集合，
// RowsAffected 写入 PGSeedResult.CustomModelOverlapCleaned。
func TestSeedPostgresCustomModelOverlapCleanupContract(t *testing.T) {
	for _, fragment := range []string{
		`DELETE FROM "juhe_business"."custom_provider_models"`,
		"jsonb_to_recordset($1::jsonb) AS built_in(provider_code text, model text)",
		`built_in.provider_code = "juhe_business"."custom_provider_models".provider_code`,
		`built_in.model = "juhe_business"."custom_provider_models".model`,
	} {
		if !strings.Contains(pgSeedCustomModelOverlapCleanup, fragment) {
			t.Fatalf("清理语句缺少片段 %q", fragment)
		}
	}

	rec := &wmSchemaRecorder{}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	result := PGSeedResult{}
	if err := seedPostgresModelCatalog(context.Background(), db,
		func(string, ...any) error { return nil },
		SeedOptions{Now: func() time.Time { return pgSeedTestClock }},
		seedTimestamp(pgSeedTestClock), &result); err != nil {
		t.Fatalf("seedPostgresModelCatalog: %v", err)
	}
	rec.mu.Lock()
	var cleanupArgs []any
	for _, statement := range rec.execs {
		if strings.Contains(statement.query, `DELETE FROM "juhe_business"."custom_provider_models"`) {
			cleanupArgs = statement.args
			break
		}
	}
	rec.mu.Unlock()
	if cleanupArgs == nil {
		t.Fatal("清理 DELETE 未被执行")
	}
	if len(cleanupArgs) != 1 {
		t.Fatalf("清理 DELETE 参数个数 = %d, want 1", len(cleanupArgs))
	}
	payload, _ := cleanupArgs[0].(string)
	var keys []pgSeedBuiltInModelKey
	if err := json.Unmarshal([]byte(payload), &keys); err != nil {
		t.Fatalf("清理参数必须是 JSON 键数组: %v", err)
	}
	// 2026-09-04 时钟下可见行 = 全量 120（与 stale disable 共用同一键集合）。
	if got := len(keys); got != ActiveModelCatalogSeedRowCount("2026-09-04") || got == 0 {
		t.Fatalf("清理键集合长度 = %d, want ActiveModelCatalogSeedRowCount = %d", got, ActiveModelCatalogSeedRowCount("2026-09-04"))
	}
	// 录制驱动 Exec 恒报 3 行受影响：RowsAffected 必须透传进结果字段。
	if result.CustomModelOverlapCleaned != 3 {
		t.Fatalf("CustomModelOverlapCleaned = %d, want 3（录制驱动 RowsAffected）", result.CustomModelOverlapCleaned)
	}
}
