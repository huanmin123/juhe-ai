package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

// statsaggUpgradedTables 是 statsCleanupTables 简化表中会被 statsagg
// SQLiteTestSchema 重建的统计域表（DerivedWindows 接线后清理链末端的派生窗口
// 重算要求完整列集）。
var statsaggUpgradedTables = []string{
	"usage_stats_totals", "usage_stats_minute", "usage_stats_hourly", "usage_stats_daily", "usage_stats_weekly", "usage_stats_monthly",
	"usage_model_minute", "usage_model_hourly", "usage_model_daily", "usage_model_weekly", "usage_model_monthly",
	"usage_error_minute", "usage_error_hourly", "usage_error_daily", "usage_error_weekly", "usage_error_monthly",
	"usage_latency_minute", "usage_latency_hourly", "usage_latency_daily", "usage_latency_weekly", "usage_latency_monthly",
	"usage_rank_snapshots",
	"usage_overview_summary_windows", "usage_overview_trend_windows", "usage_model_rank_windows", "usage_error_rank_windows",
	"ai_performance_summary_windows", "usage_scope_range_windows",
	"authorization_team_usage_summary_daily", "authorization_user_usage_summary_daily",
	"authorization_team_usage_range_windows", "authorization_user_usage_range_windows",
	"system_metrics_samples", "system_metrics_hourly", "system_metrics_trend_windows",
	"process_event_loop_samples", "process_event_loop_hourly", "process_event_loop_trend_windows",
	"account_quality_minute_stats", "account_quality_dirty_accounts", "account_health_hourly",
	"stats_job_state", "usage_quota_hourly_windows",
}

// statsaggUpgradedDropStatements 返回升级表的 DROP 语句。
func statsaggUpgradedDropStatements() []string {
	statements := make([]string, 0, len(statsaggUpgradedTables))
	for _, table := range statsaggUpgradedTables {
		statements = append(statements, "DROP TABLE IF EXISTS "+table)
	}
	return statements
}

// 配额小时窗生产者组合根接线测试（BUG-0175 D-48）：worker_retention.go 的
// DerivedWindowRefresher 固定 nil → SQLite 模式接入 statsagg.WindowRefresher，
// RefreshQuotaHourlyWindows 从 retention 家族自己的 stats/business 句柄完成
// 全量重建。PG 清理路径保持 DerivedWindows = nil（Node PG cleanup 只写脏范围
// 标记、由调度式窗口刷新收敛），由 worker_retention.go 的 `if !postgres` 门控。
// usage-quota-hourly-windows-refresh 调度任务的函数体语义由 statsagg 包
// quota_windows_test.go 覆盖（组合根无 StatsEnabled=true 装配测试先例，保持
// 一致）。

func TestWorkerRetentionDerivedWindowsWiredSQLite(t *testing.T) {
	dir := t.TempDir()
	seedDataRetention(t, dir)
	seedQuotaHourlyWindowTables(t, dir)

	assembly, err := buildWorkerAssembly(retentionTestConfig(dir), slog.Default())
	if err != nil {
		t.Fatalf("build worker assembly: %v", err)
	}
	defer assembly.closeStores()

	if assembly.retention == nil || assembly.retention.recordCleanup == nil {
		t.Fatal("retention family 未装配")
	}
	derived := assembly.retention.recordCleanup.DerivedWindows
	if derived == nil {
		t.Fatal("SQLite 模式下 DerivedWindows 未接线（D-48：组合根固定 nil 未移除）")
	}

	if err := derived.RefreshQuotaHourlyWindows(context.Background()); err != nil {
		t.Fatalf("RefreshQuotaHourlyWindows: %v", err)
	}

	// 消费方（gateway gatewayquota/costs.go LoadCosts）按四列主键读窗口行。
	stats := openTestSQLite(t, filepath.Join(dir, "stats.sqlite3"))
	var totalCost float64
	if err := stats.QueryRow(`SELECT total_cost_usd FROM usage_quota_hourly_windows
		WHERE system_account_id = 'sys_a' AND scope_type = 'api_key' AND scope_id = 'key_wired' AND window_hours = 1`).Scan(&totalCost); err != nil {
		t.Fatalf("消费方按主键读窗口行: %v", err)
	}
	if totalCost != 3 {
		t.Fatalf("window total_cost_usd = %v, want 3", totalCost)
	}
}

// seedQuotaHourlyWindowTables 在 retention 测试库上补齐配额小时窗链路：
// stats 库的 usage_quota_hourly_windows / usage_stats_hourly 用完整列集重建
// （statsCleanupTables 只建清理探测用的简化列），business 库建绑定表并 seed。
func seedQuotaHourlyWindowTables(t *testing.T, dir string) {
	t.Helper()
	// 组合根 refresher 的时钟是 time.Now（retention family.now）、时区是
	// familyTimezoneClock 的 Asia/Shanghai，hourly 桶键取上海时区当前小时，
	// 保证 1h 窗（cutoff = now 截断到小时）能命中。
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	currentHour := time.Now().In(shanghai).Truncate(time.Hour).Format("2006-01-02T15")
	stats := openTestSQLite(t, filepath.Join(dir, "stats.sqlite3"))
	mustExec(t, stats,
		"DROP TABLE IF EXISTS usage_quota_hourly_windows",
		`CREATE TABLE usage_quota_hourly_windows (
			system_account_id TEXT NOT NULL,
			scope_type TEXT NOT NULL,
			scope_id TEXT NOT NULL DEFAULT '',
			window_hours INTEGER NOT NULL,
			total_cost_usd REAL NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (system_account_id, scope_type, scope_id, window_hours))`,
		"DROP TABLE IF EXISTS usage_stats_hourly",
		`CREATE TABLE usage_stats_hourly (
			system_account_id TEXT NOT NULL,
			scope_type TEXT NOT NULL,
			scope_id TEXT NOT NULL DEFAULT '',
			stat_hour TEXT NOT NULL,
			total_cost_usd REAL NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (system_account_id, scope_type, scope_id, stat_hour))`,
		`INSERT INTO usage_stats_hourly (system_account_id, scope_type, scope_id, stat_hour, total_cost_usd, updated_at)
		 VALUES ('sys_a', 'api_key', 'key_wired', '`+currentHour+`', 3, '2026-04-18T11:00:00.000Z')`)

	business := openTestSQLite(t, filepath.Join(dir, "business.sqlite3"))
	mustExec(t, business,
		`CREATE TABLE IF NOT EXISTS request_quota_hourly_window_scope_bindings (
			system_account_id TEXT NOT NULL,
			scope_type TEXT NOT NULL,
			scope_id TEXT NOT NULL DEFAULT '',
			source_type TEXT NOT NULL,
			source_id TEXT NOT NULL,
			window_hours INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (system_account_id, scope_type, scope_id))`,
		`INSERT INTO request_quota_hourly_window_scope_bindings (
			system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at)
		 VALUES ('sys_a', 'api_key', 'key_wired', 'api_key', 'key_wired', 1, '2026-04-18T00:00:00.000Z', '2026-04-18T00:00:00.000Z')`)
}
