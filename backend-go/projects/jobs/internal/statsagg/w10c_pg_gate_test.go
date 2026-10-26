package statsagg

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// w10c_pg_gate_test.go 覆盖 SQLite 无法执行的 PG 专有 SQL 行：增量配额刷新
// 中的 FOR UPDATE ... SKIP LOCKED 认领臂与 juhe_ 前缀方言执行。建库走
// statsagg_pgkit_test.go 的 pgtestkit 隔离 PG 测试库 + 生产对齐 DDL（原
// shared.env 门禁共享覆盖库装配已退役）：每测试独立 fresh 库，PG 不可达由
// pgtestkit 显式 Fatal 而非 t.Skip；隔离库随测试结束删除，不再需要行级
// 清理与 account_list_availability_* 函数族自愈 seed（刷新 SQL 不消费该
// 函数族，触发器依赖只存在于共享库形态）。连接串永不进入日志或断言。

func w10cPgExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("PG fixture exec 失败: %v", err)
	}
}

// TestW10CPGQuotaIncrementalSkipLocked 在真实 PG 上跑增量配额刷新，覆盖
// SKIP LOCKED 认领臂；同语义编排已在 SQLite 覆盖，此门禁只补 PG 方言行。
func TestW10CPGQuotaIncrementalSkipLocked(t *testing.T) {
	db := openKitPostgresDB(t)
	ctx := context.Background()

	// 一条绑定 + 一条小时成本 + 一个脏 scope，触发认领（含 SKIP LOCKED）。
	// 绑定表为 kitBusinessBindingDDL 生产形状（check 约束限定 source_type 枚举）。
	w10cPgExec(t, db, `INSERT INTO juhe_business.request_quota_hourly_window_scope_bindings
		(system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at)
		VALUES ('w10cpg-1', 'api_key', 'key-1', 'resource_authorization_grant', 'src-1', 24, $1, $2)`,
		"2026-09-06T12:00:00.000Z", "2026-09-06T12:00:00.000Z")
	// 成功口径切换后窗口刷新按 success_cost_usd 汇总（HAVING > 0，零成本不落
	// 行）：夹具同步给成功口径成本，原「小时成本落窗口行」断言语义不变。
	w10cPgExec(t, db, `INSERT INTO juhe_stats.usage_stats_hourly
		(system_account_id, scope_type, scope_id, stat_hour, request_count, success_count, error_count,
		 input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
		 thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd, success_cost_usd, updated_at)
		VALUES ('w10cpg-1', 'api_key', 'key-1', $1, 1, 1, 0, 10, 5, 0, 0, 0, 0, 0, 0, 0, 0, 0.5, 0.5, $2)`,
		"2026-09-06T11", "2026-09-06T12:00:00.000Z")
	w10cPgExec(t, db, `INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes
		(system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at)
		VALUES ('w10cpg-1', 'api_key', 'key-1', 1, $1, $2)`,
		"2026-09-06T12:00:00.000Z", "2026-09-06T12:00:00.000Z")

	refresher := &WindowRefresher{
		DB:      db,
		Dialect: Dialect{},
		Clock:   StaticTimezoneSource{Location: time.UTC},
		Now:     func() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) },
	}
	result, err := refresher.RunQuotaHourlyWindows(ctx)
	if err != nil {
		t.Fatalf("PG 增量配额刷新失败: %v", err)
	}
	if !result.Changed {
		t.Fatalf("PG 增量应标记 changed")
	}
	// 脏 scope 已消费删除。
	var dirtyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_stats.usage_quota_hourly_window_dirty_scopes WHERE system_account_id='w10cpg-1'`).Scan(&dirtyCount); err != nil {
		t.Fatal(err)
	}
	if dirtyCount != 0 {
		t.Fatalf("脏 scope 应被消费删除, count=%d", dirtyCount)
	}
	// 窗口成本落库（零成本不落行）。
	var windowCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_stats.usage_quota_hourly_windows WHERE system_account_id='w10cpg-1'`).Scan(&windowCount); err != nil {
		t.Fatal(err)
	}
	if windowCount != 1 {
		t.Fatalf("窗口成本应落库, count=%d", windowCount)
	}
}
