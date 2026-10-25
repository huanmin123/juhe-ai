package statsverify

import (
	"context"
	"database/sql"
	"testing"
)

// success_cost_usd 列的 SQLite schema 语义测试：新库直建含列、旧窄形状库
// ensure 后补列（结构守卫保留）但不再执行数据回填——历史错口径行的回填
// 唯一入口是 maintenance --apply-success-cost-backfill 一次性子命令（计划
// PLAN-20261011T225418），ensure 保持行原值；聚合写入形状（带
// success_cost_usd 的 INSERT，statsagg upserts.go usageStatsMetricColumns
// 同列序）可执行。

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

