package statsagg

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

)

// 配额小时窗刷新测试（BUG-0175 D-48/D-75/D-86/D-229）。双载体：
//   - 组A 增量族（PG 生产分支）：pgtestkit 隔离 PG 测试库 + 生产方言
//     Dialect{}，真实覆盖 FOR UPDATE SKIP LOCKED 认领编排；
//     绑定表经 juhe_business. 前缀落在同池（生产拓扑），种子 SQL 按 PG 方言
//     书写（juhe_ 前缀 schema 限定 + $n）；
//   - 组B 全量重建族（SQLite 生产分支）：SQLite 承载测试库 + BusinessDB
//     独立业务库句柄（refreshQuotaHourlyWindowsRebuild 读绑定表必需），
//     随生产 sqlite 臂删除批次同批退役（D17）。
//
// 双模 SQL 由 Dialect 单一来源生成，语义锚定 usage-stats.repository.ts:1783-1951
// 与 usage-stats-snapshot-helpers.ts:8-42。共享 helper（seedQuotaHourly /
// quotaWindowRows / quotaDirtyCount）经生产同款 Dialect.bind / StatsTable
// 生成 SQL，双载体透明；SQLite 专用 helper（createBindingTable / seedBinding）
// 保持原状（组B 与 w13g5 scripted 用例消费）。
//
// 归档行为基线：
//   - 全量重建（SQLite 生产分支）：读业务库绑定 → DELETE 全表 → 按 200 一批
//     重算，零成本 scope 不落行，残留行随全表 DELETE 消失；
//   - 增量消费（PG 生产分支）：脏 scope 按 first_dirty_at 认领 → 删旧窗口行 →
//     按活跃绑定重算 → generation 匹配删除脏行；空脏范围不写不改；
//   - expiry 游标：小时翻转后给滑动截止区间内仍有时数据的 scope 打脏，
//     同一小时不重复打脏；非法游标 fail closed；
//   - 刷新失败写 stats_job_state(global,'',usage-quota-hourly-windows-refresh)
//     的 last_error_message，成功清空并记 last_success_at（D-229）。

func createBindingTable(t *testing.T, db *sql.DB) {
	t.Helper()
	// 列集对齐 maintenance sqlite_schema.go:1221 的业务库绑定表（SQLite 载体
	// 专用：组B 全量重建与 w13g5 scripted 用例）。
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS request_quota_hourly_window_scope_bindings (
		system_account_id TEXT NOT NULL,
		scope_type TEXT NOT NULL,
		scope_id TEXT NOT NULL DEFAULT '',
		source_type TEXT NOT NULL,
		source_id TEXT NOT NULL,
		window_hours INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (system_account_id, scope_type, scope_id)
	)`)
	if err != nil {
		t.Fatal(err)
	}
}

func seedBinding(db *sql.DB, systemAccountID, scopeType, scopeID string, windowHours int) error {
	_, err := db.Exec(`INSERT INTO request_quota_hourly_window_scope_bindings (
		system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
	) VALUES (?, ?, ?, 'api_key', ?, ?, '2026-04-18T00:00:00.000Z', '2026-04-18T00:00:00.000Z')`,
		systemAccountID, scopeType, scopeID, scopeID, windowHours)
	return err
}

// pgSeedQuotaBinding 把绑定写入 PG 同池的 juhe_business 绑定表（增量编排经
// BusinessTable 前缀消费，生产拓扑；source_type 与 seedBinding 同值 api_key）。
func pgSeedQuotaBinding(t *testing.T, e *testEnv, systemAccountID, scopeType, scopeID string, windowHours int) {
	t.Helper()
	e.exec(`INSERT INTO juhe_business.request_quota_hourly_window_scope_bindings (
		system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
	) VALUES ($1, $2, $3, 'api_key', $4, $5, '2026-04-18T00:00:00.000Z', '2026-04-18T00:00:00.000Z')`,
		systemAccountID, scopeType, scopeID, scopeID, windowHours)
}

func (e *testEnv) quotaRefresher(business *sql.DB) *WindowRefresher {
	e.t.Helper()
	return &WindowRefresher{
		DB:         e.db,
		BusinessDB: business,
		Dialect:    e.dialect,
		Clock:      StaticTimezoneSource{e.zone},
		Now:        func() time.Time { return e.now },
	}
}

func (e *testEnv) seedQuotaHourly(systemAccountID, scopeType, scopeID, statHour string, totalCostUsd float64) {
	e.t.Helper()
	e.seedQuotaHourlySplit(systemAccountID, scopeType, scopeID, statHour, totalCostUsd, totalCostUsd)
}

// seedQuotaHourlySplit 分别种入全量成本与成功口径成本（成功口径切换后窗口
// 刷新按 usage_stats_hourly.success_cost_usd 汇总）。双载体 helper：SQL 经
// 生产 Dialect 装配习语生成。
func (e *testEnv) seedQuotaHourlySplit(systemAccountID, scopeType, scopeID, statHour string, totalCostUsd, successCostUsd float64) {
	e.t.Helper()
	e.exec(e.dialect.bind(`INSERT INTO `+e.dialect.StatsTable("usage_stats_hourly")+` (
		system_account_id, scope_type, scope_id, stat_hour, total_cost_usd, success_cost_usd, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, '2026-04-18T00:00:00.000Z')`),
		systemAccountID, scopeType, scopeID, statHour, totalCostUsd, successCostUsd)
}

type quotaWindowRow struct {
	systemAccountID string
	scopeType       string
	scopeID         string
	windowHours     int
	totalCostUsd    float64
}

func (e *testEnv) quotaWindowRows(t *testing.T) []quotaWindowRow {
	t.Helper()
	rows, err := e.db.Query(`SELECT system_account_id, scope_type, scope_id, window_hours, total_cost_usd
		FROM ` + e.dialect.StatsTable("usage_quota_hourly_windows") + `
		ORDER BY system_account_id, scope_type, scope_id, window_hours`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := []quotaWindowRow{}
	for rows.Next() {
		var row quotaWindowRow
		if err := rows.Scan(&row.systemAccountID, &row.scopeType, &row.scopeID, &row.windowHours, &row.totalCostUsd); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func (e *testEnv) quotaDirtyCount(t *testing.T) int {
	t.Helper()
	counts := e.queryFloats(`SELECT COUNT(*) FROM ` + e.dialect.StatsTable("usage_quota_hourly_window_dirty_scopes"))
	if len(counts) != 1 {
		t.Fatalf("dirty count query returned %d rows", len(counts))
	}
	return int(counts[0])
}

func assertQuotaWindowRows(t *testing.T, got []quotaWindowRow, want ...quotaWindowRow) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("usage_quota_hourly_windows rows: got %v want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("usage_quota_hourly_windows rows: got %v want %v", got, want)
		}
	}
}

func TestQuotaHourlyWindowsIncrementalConsumesDirtyScopes(t *testing.T) {
	e := newPGTestEnv(t)
	refresher := e.quotaRefresher(nil)

	pgSeedQuotaBinding(t, e, "sa1", "api_key", "keyA", 1)
	pgSeedQuotaBinding(t, e, "sa1", "api_key", "keyB", 1)
	e.seedQuotaHourly("sa1", "api_key", "keyA", "2026-04-18T11", 3)
	// keyB：有绑定无 hourly，但残留旧窗口行 → 重算后归零消失（:1894-1901 删旧行）。
	e.exec(`INSERT INTO juhe_stats.usage_quota_hourly_windows (system_account_id, scope_type, scope_id, window_hours, total_cost_usd, updated_at)
		VALUES ('sa1', 'api_key', 'keyB', 1, 5, '2026-04-17T00:00:00.000Z')`)
	// keyC：脏标记存在但绑定已删（LEFT JOIN window_hours NULL）→ 只删不插。
	e.exec(`INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at)
		VALUES ('sa1', 'api_key', 'keyA', 2, '2026-04-18T10:00:00.000Z', '2026-04-18T10:00:00.000Z'),
		       ('sa1', 'api_key', 'keyB', 1, '2026-04-18T10:00:00.000Z', '2026-04-18T10:00:00.000Z'),
		       ('sa1', 'api_key', 'keyC', 1, '2026-04-18T10:00:00.000Z', '2026-04-18T10:00:00.000Z')`)

	result := QuotaHourlyWindowRefreshResult{}
	if err := refresher.refreshQuotaHourlyWindowsIncremental(context.Background(), e.zone, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.HasMore {
		t.Fatalf("result = %+v, want changed=true hasMore=false", result)
	}
	assertQuotaWindowRows(t, e.quotaWindowRows(t),
		quotaWindowRow{"sa1", "api_key", "keyA", 1, 3},
	)
	if got := e.quotaDirtyCount(t); got != 0 {
		t.Fatalf("dirty scopes left = %d, want 0", got)
	}
}

// TestQuotaHourlyWindowsIncrementalEmptyDirty mirrors :1890 空脏范围早退：
// 不写窗口、不改状态。
func TestQuotaHourlyWindowsIncrementalEmptyDirty(t *testing.T) {
	e := newPGTestEnv(t)
	refresher := e.quotaRefresher(nil)

	e.exec(`INSERT INTO juhe_stats.usage_quota_hourly_windows (system_account_id, scope_type, scope_id, window_hours, total_cost_usd, updated_at)
		VALUES ('sa1', 'api_key', 'keyA', 1, 9, '2026-04-17T00:00:00.000Z')`)

	result := QuotaHourlyWindowRefreshResult{}
	if err := refresher.refreshQuotaHourlyWindowsIncremental(context.Background(), e.zone, &result); err != nil {
		t.Fatal(err)
	}
	if result.Changed || result.HasMore {
		t.Fatalf("result = %+v, want changed=false hasMore=false", result)
	}
	assertQuotaWindowRows(t, e.quotaWindowRows(t),
		quotaWindowRow{"sa1", "api_key", "keyA", 1, 9},
	)
}

// TestQuotaHourlyWindowsIncrementalPagination 用 ScopeLimit=2 覆盖 :1871/1948
// 的分页续跑：消费满上限返回 hasMore=true，循环排空后为 false。
func TestQuotaHourlyWindowsIncrementalPagination(t *testing.T) {
	e := newPGTestEnv(t)
	refresher := e.quotaRefresher(nil)
	refresher.ScopeLimit = 2

	for _, keyID := range []string{"keyA", "keyB", "keyC"} {
		pgSeedQuotaBinding(t, e, "sa1", "api_key", keyID, 1)
		e.seedQuotaHourly("sa1", "api_key", keyID, "2026-04-18T11", 1)
		e.exec(`INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at)
			VALUES ('sa1', 'api_key', $1, 1, '2026-04-18T10:00:00.000Z', '2026-04-18T10:00:00.000Z')`, keyID)
	}

	first := QuotaHourlyWindowRefreshResult{}
	if err := refresher.refreshQuotaHourlyWindowsIncremental(context.Background(), e.zone, &first); err != nil {
		t.Fatal(err)
	}
	if !first.Changed || !first.HasMore {
		t.Fatalf("first pass result = %+v, want changed=true hasMore=true", first)
	}
	if got := len(e.quotaWindowRows(t)); got != 2 {
		t.Fatalf("first pass window rows = %d, want 2", got)
	}
	if got := e.quotaDirtyCount(t); got != 1 {
		t.Fatalf("first pass dirty scopes = %d, want 1", got)
	}

	second := QuotaHourlyWindowRefreshResult{}
	if err := refresher.refreshQuotaHourlyWindowsIncremental(context.Background(), e.zone, &second); err != nil {
		t.Fatal(err)
	}
	if !second.Changed || second.HasMore {
		t.Fatalf("second pass result = %+v, want changed=true hasMore=false", second)
	}
	if got := len(e.quotaWindowRows(t)); got != 3 {
		t.Fatalf("second pass window rows = %d, want 3", got)
	}
	if got := e.quotaDirtyCount(t); got != 0 {
		t.Fatalf("second pass dirty scopes = %d, want 0", got)
	}
}

// TestQuotaHourlyWindowsExpiryCursorAndSkipSameHour mirrors :1807-1869：
// 小时翻转打脏 + 游标推进；同一小时重复运行不再打脏。
func TestQuotaHourlyWindowsExpiryCursorAndSkipSameHour(t *testing.T) {
	e := newPGTestEnv(t)
	refresher := e.quotaRefresher(nil)
	pgSeedQuotaBinding(t, e, "sa1", "api_key", "keyA", 1)
	// previousBoundary = now-1h = 11:00 → previousCutoff=10:00、currentCutoff=11:00；
	// hourly@10:00 落在 [10:00, 11:00) → 打脏。
	e.seedQuotaHourly("sa1", "api_key", "keyA", "2026-04-18T10", 2)

	result := QuotaHourlyWindowRefreshResult{}
	if err := refresher.refreshQuotaHourlyWindowsIncremental(context.Background(), e.zone, &result); err != nil {
		t.Fatal(err)
	}
	cursorIDs := e.queryString(`SELECT cursor_id FROM juhe_stats.stats_job_state
		WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_quota_hourly_windows_expiry'`)
	if len(cursorIDs) != 1 || cursorIDs[0] != "2026-04-18T12" {
		t.Fatalf("expiry cursor_id = %v, want [2026-04-18T12]", cursorIDs)
	}
	// 消费段已清掉 expiry 打的脏 → dirty 空、窗口零成本不落行。
	if got := e.quotaDirtyCount(t); got != 0 {
		t.Fatalf("dirty scopes after first pass = %d, want 0", got)
	}

	// 同一小时第二次运行：expiry 段不重复打脏（否则下方断言会出现脏行）。
	if err := refresher.refreshQuotaHourlyWindowsIncremental(context.Background(), e.zone, &result); err != nil {
		t.Fatal(err)
	}
	if got := e.quotaDirtyCount(t); got != 0 {
		t.Fatalf("dirty scopes after same-hour rerun = %d, want 0", got)
	}
}

// TestQuotaHourlyWindowsExpiryCursorInvalidTimestamp mirrors :1816-1818：
// 非法 cursor_created_at fail closed。
func TestQuotaHourlyWindowsExpiryCursorInvalidTimestamp(t *testing.T) {
	e := newPGTestEnv(t)
	refresher := e.quotaRefresher(nil)
	pgSeedQuotaBinding(t, e, "sa1", "api_key", "keyA", 1)
	e.exec(`INSERT INTO juhe_stats.stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
		VALUES ('global', '', 'usage_quota_hourly_windows_expiry', 'not-a-time', '2026-04-18T11', '2026-04-18T11:00:00.000Z')`)

	err := refresher.refreshQuotaHourlyWindowsIncremental(context.Background(), e.zone, &QuotaHourlyWindowRefreshResult{})
	if err == nil {
		t.Fatal("expected error for invalid expiry cursor_created_at")
	}
	if !strings.Contains(err.Error(), "用量统计 expiry cursor_created_at 必须是带 Z 或数值 offset 的 RFC3339 时间") {
		t.Fatalf("error = %v", err)
	}
}

// TestQuotaHourlyWindowsFailureWritesJobState 覆盖 D-229 的失败可见性：
// 刷新失败写 stats_job_state last_error_message；成功后清空并记
// last_success_at。
//
// PG 生产分支：先以缺 juhe_business 绑定表的隔离库触发增量消费失败（真实
// 场景：业务 schema 未迁移），补建绑定表后重跑成功；断言与原 SQLite 载体
// 逐字节一致。
func TestQuotaHourlyWindowsFailureWritesJobState(t *testing.T) {
	e := newPGTestEnvWithDB(t, openKitStatsOnlyPostgresDB(t))
	refresher := e.quotaRefresher(nil)

	if _, err := refresher.RunQuotaHourlyWindows(context.Background()); err == nil {
		t.Fatal("expected refresh failure against business db without bindings table")
	}
	messages := e.queryString(`SELECT last_error_message FROM juhe_stats.stats_job_state
		WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage-quota-hourly-windows-refresh'`)
	if len(messages) != 1 || messages[0] == "" {
		t.Fatalf("last_error_message after failure = %v, want non-empty", messages)
	}

	// 补建绑定表（kitBusinessBindingDDL，生产对齐形状）后重跑成功 →
	// last_error_message 清空、last_success_at 记录。PG 增量分支按脏范围
	// 消费驱动重算（SQLite 重建分支由绑定全量驱动），成功段种入一条脏范围
	// 触发窗口重建，断言与原 SQLite 载体逐字节一致。
	e.exec(kitBusinessBindingDDL)
	e.exec(`INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at)
		VALUES ('sa1', 'api_key', 'keyA', 1, '2026-04-18T10:00:00.000Z', '2026-04-18T10:00:00.000Z')`)
	pgSeedQuotaBinding(t, e, "sa1", "api_key", "keyA", 1)
	e.seedQuotaHourly("sa1", "api_key", "keyA", "2026-04-18T11", 3)
	if _, err := refresher.RunQuotaHourlyWindows(context.Background()); err != nil {
		t.Fatal(err)
	}
	errorColumns := e.queryString(`SELECT COALESCE(last_error_message, '') FROM juhe_stats.stats_job_state
		WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage-quota-hourly-windows-refresh'`)
	if len(errorColumns) != 1 || errorColumns[0] != "" {
		t.Fatalf("last_error_message after success = %v, want empty", errorColumns)
	}
	successAt := e.queryString(`SELECT last_success_at FROM juhe_stats.stats_job_state
		WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage-quota-hourly-windows-refresh'`)
	if len(successAt) != 1 || successAt[0] == "" {
		t.Fatalf("last_success_at after success = %v, want non-empty", successAt)
	}
	assertQuotaWindowRows(t, e.quotaWindowRows(t),
		quotaWindowRow{"sa1", "api_key", "keyA", 1, 3},
	)
}
