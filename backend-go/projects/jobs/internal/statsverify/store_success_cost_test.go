package statsverify

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// success_cost_usd 列的 SQLite schema 语义测试：新库直建含列、旧窄形状库
// ensure 后补列并按 maintenance 同款守卫回填（error_count = 0 → success =
// total；混合行保持 0），回填幂等，且聚合写入形状（带 success_cost_usd 的
// INSERT，statsagg upserts.go usageStatsMetricColumns 同列序）可执行。

// createLegacyStatsTable 按补列前的窄形状建 usage_stats 表（无
// success_cost_usd），复现旧库文件。
func createLegacyStatsTable(t *testing.T, db *sql.DB, table, timeColumn string) {
	t.Helper()
	mustExec(t, context.Background(), db, `CREATE TABLE `+table+` (
		system_account_id TEXT NOT NULL,
		scope_type TEXT NOT NULL,
		scope_id TEXT NOT NULL,
		`+timeColumn+` TEXT NOT NULL,
		request_count INTEGER NOT NULL DEFAULT 0,
		success_count INTEGER NOT NULL DEFAULT 0,
		error_count INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read_cost_usd REAL NOT NULL DEFAULT 0,
		cache_write_tokens INTEGER NOT NULL DEFAULT 0,
		cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
		cache_write_cost_usd REAL NOT NULL DEFAULT 0,
		thinking_tokens INTEGER NOT NULL DEFAULT 0,
		input_image_tokens INTEGER NOT NULL DEFAULT 0,
		output_image_tokens INTEGER NOT NULL DEFAULT 0,
		total_cost_usd REAL NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (system_account_id, scope_type, scope_id, `+timeColumn+`)
	)`)
}

func tableColumns(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, declaredType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info(%s): %v", table, err)
	}
	return columns
}

func queryFloat(t *testing.T, db *sql.DB, query string, args ...any) float64 {
	t.Helper()
	var value float64
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return value
}

// TestEnsureSchemaSQLiteFreshDeclaresSuccessCost：新库直建含列，聚合形状
// INSERT（显式 success_cost_usd）与默认 0 均可执行。
func TestEnsureSchemaSQLiteFreshDeclaresSuccessCost(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	for _, table := range []string{"usage_stats_daily", "usage_stats_hourly"} {
		if !tableColumns(t, store.db, table)["success_cost_usd"] {
			t.Fatalf("%s 新库缺 success_cost_usd 列", table)
		}
	}
	// 聚合 upsert 形状：显式写 success_cost_usd（缺列时报 no such column）。
	mustExec(t, ctx, store.db, `INSERT INTO usage_stats_daily (
		system_account_id, scope_type, scope_id, stat_date, total_cost_usd, success_cost_usd, updated_at)
		VALUES ('sys-1', 'account', 'acc-1', '2026-09-25', 3.5, 2.5, '2026-09-25T00:00:00.000Z')`)
	// 不写该列的 plain INSERT 落默认 0。
	mustExec(t, ctx, store.db, `INSERT INTO usage_stats_hourly (
		system_account_id, scope_type, scope_id, stat_hour, total_cost_usd, updated_at)
		VALUES ('sys-1', 'account', 'acc-1', '2026-09-25T10', 1.25, '2026-09-25T00:00:00.000Z')`)
	if got := queryFloat(t, store.db, `SELECT success_cost_usd FROM usage_stats_daily
		WHERE system_account_id = 'sys-1' AND scope_id = 'acc-1'`); got != 2.5 {
		t.Fatalf("daily success_cost_usd = %v, want 2.5", got)
	}
	if got := queryFloat(t, store.db, `SELECT success_cost_usd FROM usage_stats_hourly
		WHERE system_account_id = 'sys-1' AND scope_id = 'acc-1'`); got != 0 {
		t.Fatalf("hourly success_cost_usd 默认 = %v, want 0", got)
	}
}

// TestEnsureSchemaSQLiteLegacySuccessCostBackfill：旧窄形状库 ensure 后补列、
// 按 error_count = 0 守卫回填，且重复 ensure 幂等。
func TestEnsureSchemaSQLiteLegacySuccessCostBackfill(t *testing.T) {
	dir := t.TempDir()
	statsPath := filepath.Join(dir, "stats.db")
	legacy, err := sql.Open("sqlite", statsPath)
	if err != nil {
		t.Fatalf("open legacy stats db: %v", err)
	}
	createLegacyStatsTable(t, legacy, "usage_stats_daily", "stat_date")
	createLegacyStatsTable(t, legacy, "usage_stats_hourly", "stat_hour")
	ctx := context.Background()
	// 无失败行：success_cost_usd 应回填为 total_cost_usd；混合行保持 0。
	mustExec(t, ctx, legacy, `INSERT INTO usage_stats_daily (
		system_account_id, scope_type, scope_id, stat_date, request_count, error_count,
		total_cost_usd, updated_at)
		VALUES ('sys-1', 'account', 'acc-clean', '2026-09-25', 2, 0, 2.5, '2026-09-25T00:00:00.000Z')`)
	mustExec(t, ctx, legacy, `INSERT INTO usage_stats_daily (
		system_account_id, scope_type, scope_id, stat_date, request_count, error_count,
		total_cost_usd, updated_at)
		VALUES ('sys-1', 'account', 'acc-mixed', '2026-09-25', 3, 1, 3.75, '2026-09-25T00:00:00.000Z')`)
	mustExec(t, ctx, legacy, `INSERT INTO usage_stats_hourly (
		system_account_id, scope_type, scope_id, stat_hour, request_count, error_count,
		total_cost_usd, updated_at)
		VALUES ('sys-1', 'account', 'acc-clean', '2026-09-25T10', 1, 0, 1.25, '2026-09-25T00:00:00.000Z')`)
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy stats db: %v", err)
	}

	store, err := OpenStore(StoreConfig{
		Mode:               StoreSQLite,
		SQLiteStatsPath:    statsPath,
		SQLiteBusinessPath: filepath.Join(dir, "business.db"),
	})
	if err != nil {
		t.Fatalf("open store on legacy db: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, table := range []string{"usage_stats_daily", "usage_stats_hourly"} {
		if !tableColumns(t, store.db, table)["success_cost_usd"] {
			t.Fatalf("%s 旧库 ensure 后缺 success_cost_usd 列", table)
		}
	}
	cleanCost := queryFloat(t, store.db, `SELECT success_cost_usd FROM usage_stats_daily
		WHERE scope_id = 'acc-clean'`)
	if cleanCost != 2.5 {
		t.Fatalf("无失败行 success_cost_usd = %v, want 2.5（回填 total）", cleanCost)
	}
	mixedCost := queryFloat(t, store.db, `SELECT success_cost_usd FROM usage_stats_daily
		WHERE scope_id = 'acc-mixed'`)
	if mixedCost != 0 {
		t.Fatalf("混合行 success_cost_usd = %v, want 0（不可分解保持业务口径 0）", mixedCost)
	}
	hourlyCost := queryFloat(t, store.db, `SELECT success_cost_usd FROM usage_stats_hourly
		WHERE scope_id = 'acc-clean'`)
	if hourlyCost != 1.25 {
		t.Fatalf("hourly 无失败行 success_cost_usd = %v, want 1.25", hourlyCost)
	}

	// 重复 ensure 幂等：回填不重复叠加、列不重复报错。
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("re-ensure schema: %v", err)
	}
	if got := queryFloat(t, store.db, `SELECT success_cost_usd FROM usage_stats_daily
		WHERE scope_id = 'acc-clean'`); got != 2.5 {
		t.Fatalf("幂等 ensure 后 success_cost_usd = %v, want 2.5", got)
	}
}
