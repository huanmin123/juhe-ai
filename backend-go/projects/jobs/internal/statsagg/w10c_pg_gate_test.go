package statsagg

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// w10c_pg_gate_test.go 门禁化开发 PostgreSQL（w1cover 临时覆盖库）覆盖
// SQLite 无法执行的 PG 专有 SQL 行：增量配额刷新中的 FOR UPDATE OF ...
// SKIP LOCKED 认领臂与 juhe_ 前缀方言执行。数据全部 w10cpg- 前缀 ID，
// 测试结束清理行；数据库不可达时 t.Skip。连接串永不进入日志或断言。

func w10cPgSkip(reason string) string { return "w10c statsagg PG gated: " + reason }

func w10cPgDSN(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("JUHE_AI_W10C_PG_URL"); url != "" {
		return url
	}
	raw, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	if err != nil {
		t.Skip(w10cPgSkip("shared.env 不可读"))
	}
	base := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			base = strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL=")
		}
	}
	if base == "" {
		t.Skip(w10cPgSkip("shared.env 缺少 JUHE_AI_POSTGRES_URL"))
	}
	base = strings.Replace(base, ":6432/", ":5432/", 1)
	base = strings.Replace(base, "/juhe_ai_sub2api_dev?", "/juhe_ai_sub2api_dev_w1cover?", 1)
	return base
}

func w10cPgDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", w10cPgDSN(t))
	if err != nil {
		t.Skip(w10cPgSkip("pgx 打开失败"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Skip(w10cPgSkip("PG 不可达"))
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func w10cPgExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("PG fixture exec 失败: %v", err)
	}
}

// TestW10CPGQuotaIncrementalSkipLocked 在真实 PG 上跑增量配额刷新，覆盖
// SKIP LOCKED 认领臂；同语义编排已在 SQLite 覆盖，此门禁只补 PG 方言行。
func TestW10CPGQuotaIncrementalSkipLocked(t *testing.T) {
	db := w10cPgDB(t)
	ctx := context.Background()
	// w14j：共享覆盖库可能被外部重置，幂等补齐配额脏标记依赖的
	// account_list_availability_* 函数族（CREATE OR REPLACE，权威 DDL 来自
	// maintenance/internal/schema/pg_schema.go）。
	w14jEnsureAccountListAvailabilityFunctions(t, db)

	// 幂等 bootstrap 最小 schema（w1cover 覆盖库，加法式变更）。
	for _, statement := range []string{
		`CREATE SCHEMA IF NOT EXISTS juhe_stats`,
		`CREATE SCHEMA IF NOT EXISTS juhe_business`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.stats_job_state (
			scope_type TEXT NOT NULL, scope_id TEXT NOT NULL DEFAULT '', job_name TEXT NOT NULL,
			cursor_created_at TEXT, cursor_id TEXT, last_success_at TEXT, last_error_message TEXT, lag_seconds DOUBLE PRECISION,
			updated_at TEXT NOT NULL, PRIMARY KEY (scope_type, scope_id, job_name))`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_hourly (
			system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL DEFAULT '',
			stat_hour TEXT NOT NULL, total_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0, updated_at TEXT NOT NULL,
			PRIMARY KEY (system_account_id, scope_type, scope_id, stat_hour))`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_quota_hourly_windows (
			system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL DEFAULT '',
			window_hours INTEGER NOT NULL, total_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0, updated_at TEXT NOT NULL,
			PRIMARY KEY (system_account_id, scope_type, scope_id, window_hours))`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_quota_hourly_window_dirty_scopes (
			system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL DEFAULT '',
			generation BIGINT NOT NULL DEFAULT 0, first_dirty_at TEXT, updated_at TEXT NOT NULL,
			PRIMARY KEY (system_account_id, scope_type, scope_id))`,
		`CREATE TABLE IF NOT EXISTS juhe_business.request_quota_hourly_window_scope_bindings (
			system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL DEFAULT '',
			source_type TEXT NOT NULL, source_id TEXT NOT NULL, window_hours INTEGER NOT NULL,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			PRIMARY KEY (system_account_id, scope_type, scope_id))`,
	} {
		w10cPgExec(t, db, statement)
	}
	t.Cleanup(func() {
		// 只清理本测试插入的行，保留 schema 供后续 wave 复用。
		for _, statement := range []string{
			`DELETE FROM juhe_stats.usage_quota_hourly_windows WHERE system_account_id LIKE 'w10cpg-%'`,
			`DELETE FROM juhe_stats.usage_quota_hourly_window_dirty_scopes WHERE system_account_id LIKE 'w10cpg-%'`,
			`DELETE FROM juhe_stats.usage_stats_hourly WHERE system_account_id LIKE 'w10cpg-%'`,
			`DELETE FROM juhe_business.request_quota_hourly_window_scope_bindings WHERE system_account_id LIKE 'w10cpg-%'`,
			`DELETE FROM juhe_stats.stats_job_state WHERE job_name = 'usage_quota_hourly_windows_refresh'`,
		} {
			if _, err := db.Exec(statement); err != nil {
				t.Logf("w10c pg cleanup: %v", err)
			}
		}
	})

	// 一条绑定 + 一条小时成本 + 一个脏 scope，触发认领（含 SKIP LOCKED）。
	// w1cover 表已由生产 schema 建立（check 约束限定 source_type 枚举）。
	w10cPgExec(t, db, `INSERT INTO juhe_business.request_quota_hourly_window_scope_bindings
		(system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at)
		VALUES ('w10cpg-1', 'api_key', 'key-1', 'resource_authorization_grant', 'src-1', 24, $1, $2)`,
		"2026-09-06T12:00:00.000Z", "2026-09-06T12:00:00.000Z")
	w10cPgExec(t, db, `INSERT INTO juhe_stats.usage_stats_hourly
		(system_account_id, scope_type, scope_id, stat_hour, request_count, success_count, error_count,
		 input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
		 thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd, updated_at)
		VALUES ('w10cpg-1', 'api_key', 'key-1', $1, 1, 1, 0, 10, 5, 0, 0, 0, 0, 0, 0, 0, 0, 0.5, $2)`,
		"2026-09-06T11", "2026-09-06T12:00:00.000Z")
	w10cPgExec(t, db, `INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes
		(system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at)
		VALUES ('w10cpg-1', 'api_key', 'key-1', 1, $1, $2)`,
		"2026-09-06T12:00:00.000Z", "2026-09-06T12:00:00.000Z")

	refresher := &WindowRefresher{
		DB:      db,
		Dialect: Dialect{Postgres: true},
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
