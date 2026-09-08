package statsagg

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// 配额小时窗刷新测试（BUG-0175 D-48/D-75/D-86/D-229）。SQLite 承载测试库，
// 双模 SQL 由 Dialect 单一来源生成；PG 分支的增量编排（除 FOR UPDATE SKIP
// LOCKED 方言行）在 SQLite 上直接驱动，语义锚定
// usage-stats.repository.ts:1783-1951 与 usage-stats-snapshot-helpers.ts:8-42。
//
// 绑定表连接说明：生产 PG 的增量消费经 juhe_business. 前缀在同一池内 JOIN
// request_quota_hourly_window_scope_bindings；SQLite 驱动同一增量编排时该表
// 以裸表名落在 stats 测试库（seedBindingBoth 写入镜像）。全量重建（SQLite
// 生产分支）则经 BusinessDB 句柄读独立业务库，与生产拓扑一致。
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

func quotaTestBusinessDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "business.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createBindingTable(t, db)
	return db
}

func createBindingTable(t *testing.T, db *sql.DB) {
	t.Helper()
	// 列集对齐 maintenance sqlite_schema.go:1221 的业务库绑定表。
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

// seedBindingBoth 把绑定同时写入业务库与 stats 镜像库（增量编排测试用，
// 见文件头连接说明）。
func (e *testEnv) seedBindingBoth(t *testing.T, business *sql.DB, systemAccountID, scopeType, scopeID string, windowHours int) {
	t.Helper()
	if err := seedBinding(business, systemAccountID, scopeType, scopeID, windowHours); err != nil {
		t.Fatal(err)
	}
	if err := seedBinding(e.db, systemAccountID, scopeType, scopeID, windowHours); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) seedQuotaHourly(systemAccountID, scopeType, scopeID, statHour string, totalCostUsd float64) {
	e.t.Helper()
	e.exec(`INSERT INTO usage_stats_hourly (
		system_account_id, scope_type, scope_id, stat_hour, total_cost_usd, updated_at
	) VALUES (?, ?, ?, ?, ?, '2026-04-18T00:00:00.000Z')`,
		systemAccountID, scopeType, scopeID, statHour, totalCostUsd)
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
		FROM usage_quota_hourly_windows
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
	counts := e.queryFloats(`SELECT COUNT(*) FROM usage_quota_hourly_window_dirty_scopes`)
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

// TestQuotaHourlyWindowsRebuildSQLite mirrors refreshUsageQuotaHourlyWindowsCache
// （usage-stats.repository.ts:1783-1793）+ refreshUsageQuotaHourlyWindowSnapshots
// （usage-stats-snapshot-helpers.ts:8-42）。
func TestQuotaHourlyWindowsRebuildSQLite(t *testing.T) {
	e := newTestEnv(t)
	business := quotaTestBusinessDB(t)
	refresher := e.quotaRefresher(business)

	if err := seedBinding(business, "sa1", "api_key", "keyA", 1); err != nil {
		t.Fatal(err)
	}
	if err := seedBinding(business, "sa1", "api_key", "keyA2", 12); err != nil {
		t.Fatal(err)
	}
	// 无 hourly → 零成本不落行。
	if err := seedBinding(business, "sa1", "account_authorization", "authB", 2); err != nil {
		t.Fatal(err)
	}
	// 无效窗口小时 → 读侧过滤（helpers :160）。
	if err := seedBinding(business, "sa2", "api_key", "keyC", 0); err != nil {
		t.Fatal(err)
	}
	// now=2026-04-18T12:00Z；1h 窗 cutoff="2026-04-18T11"，12h 窗 cutoff="2026-04-18T00"。
	e.seedQuotaHourly("sa1", "api_key", "keyA", "2026-04-18T11", 3)
	e.seedQuotaHourly("sa1", "api_key", "keyA", "2026-04-18T08", 5) // 1h 窗外
	e.seedQuotaHourly("sa1", "api_key", "keyA2", "2026-04-18T04", 7)
	// 无绑定残留行：全表 DELETE 后消失。
	e.exec(`INSERT INTO usage_quota_hourly_windows (system_account_id, scope_type, scope_id, window_hours, total_cost_usd, updated_at)
		VALUES ('sa9', 'api_key', 'keyOld', 5, 9.9, '2026-04-17T00:00:00.000Z')`)

	result, err := refresher.RunQuotaHourlyWindows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// :1796-1799 非 PG 分支固定 {changed: true, hasMore: false}。
	if !result.Changed || result.HasMore {
		t.Fatalf("result = %+v, want changed=true hasMore=false", result)
	}
	assertQuotaWindowRows(t, e.quotaWindowRows(t),
		quotaWindowRow{"sa1", "api_key", "keyA", 1, 3},
		quotaWindowRow{"sa1", "api_key", "keyA2", 12, 7},
	)
}

// TestQuotaHourlyWindowsIncrementalConsumesDirtyScopes mirrors
// refreshUsageQuotaHourlyWindowsCacheAsync 的消费段（:1871-1949）：
// 脏范围输入 → 窗口重算 → 脏行按 generation 消费，消费方可读。
func TestQuotaHourlyWindowsIncrementalConsumesDirtyScopes(t *testing.T) {
	e := newTestEnv(t)
	business := quotaTestBusinessDB(t)
	refresher := e.quotaRefresher(business)
	createBindingTable(t, e.db) // 增量编排 JOIN 的绑定表镜像（文件头连接说明）

	e.seedBindingBoth(t, business, "sa1", "api_key", "keyA", 1)
	e.seedBindingBoth(t, business, "sa1", "api_key", "keyB", 1)
	e.seedQuotaHourly("sa1", "api_key", "keyA", "2026-04-18T11", 3)
	// keyB：有绑定无 hourly，但残留旧窗口行 → 重算后归零消失（:1894-1901 删旧行）。
	e.exec(`INSERT INTO usage_quota_hourly_windows (system_account_id, scope_type, scope_id, window_hours, total_cost_usd, updated_at)
		VALUES ('sa1', 'api_key', 'keyB', 1, 5, '2026-04-17T00:00:00.000Z')`)
	// keyC：脏标记存在但绑定已删（LEFT JOIN window_hours NULL）→ 只删不插。
	e.exec(`INSERT INTO usage_quota_hourly_window_dirty_scopes (system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at)
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
	e := newTestEnv(t)
	refresher := e.quotaRefresher(quotaTestBusinessDB(t))
	createBindingTable(t, e.db) // 增量编排 JOIN 的绑定表镜像（文件头连接说明）

	e.exec(`INSERT INTO usage_quota_hourly_windows (system_account_id, scope_type, scope_id, window_hours, total_cost_usd, updated_at)
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
	e := newTestEnv(t)
	business := quotaTestBusinessDB(t)
	refresher := e.quotaRefresher(business)
	createBindingTable(t, e.db) // 增量编排 JOIN 的绑定表镜像（文件头连接说明）
	refresher.ScopeLimit = 2

	for _, keyID := range []string{"keyA", "keyB", "keyC"} {
		e.seedBindingBoth(t, business, "sa1", "api_key", keyID, 1)
		e.seedQuotaHourly("sa1", "api_key", keyID, "2026-04-18T11", 1)
		e.exec(`INSERT INTO usage_quota_hourly_window_dirty_scopes (system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at)
			VALUES ('sa1', 'api_key', ?, 1, '2026-04-18T10:00:00.000Z', '2026-04-18T10:00:00.000Z')`, keyID)
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
	e := newTestEnv(t)
	business := quotaTestBusinessDB(t)
	refresher := e.quotaRefresher(business)
	createBindingTable(t, e.db) // 增量编排 JOIN 的绑定表镜像（文件头连接说明）
	e.seedBindingBoth(t, business, "sa1", "api_key", "keyA", 1)
	// previousBoundary = now-1h = 11:00 → previousCutoff=10:00、currentCutoff=11:00；
	// hourly@10:00 落在 [10:00, 11:00) → 打脏。
	e.seedQuotaHourly("sa1", "api_key", "keyA", "2026-04-18T10", 2)

	result := QuotaHourlyWindowRefreshResult{}
	if err := refresher.refreshQuotaHourlyWindowsIncremental(context.Background(), e.zone, &result); err != nil {
		t.Fatal(err)
	}
	cursorIDs := e.queryString(`SELECT cursor_id FROM stats_job_state
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
	e := newTestEnv(t)
	business := quotaTestBusinessDB(t)
	refresher := e.quotaRefresher(business)
	createBindingTable(t, e.db) // 增量编排 JOIN 的绑定表镜像（文件头连接说明）
	e.seedBindingBoth(t, business, "sa1", "api_key", "keyA", 1)
	e.exec(`INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
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
func TestQuotaHourlyWindowsFailureWritesJobState(t *testing.T) {
	e := newTestEnv(t)
	// 业务库无绑定表 → 全量重建失败（真实场景：业务库 schema 未迁移）。
	emptyBusiness, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "empty-business.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = emptyBusiness.Close() })
	refresher := e.quotaRefresher(emptyBusiness)

	if _, err := refresher.RunQuotaHourlyWindows(context.Background()); err == nil {
		t.Fatal("expected refresh failure against business db without bindings table")
	}
	messages := e.queryString(`SELECT last_error_message FROM stats_job_state
		WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage-quota-hourly-windows-refresh'`)
	if len(messages) != 1 || messages[0] == "" {
		t.Fatalf("last_error_message after failure = %v, want non-empty", messages)
	}

	// 修复业务库后重跑成功 → last_error_message 清空、last_success_at 记录。
	createBindingTable(t, emptyBusiness)
	if err := seedBinding(emptyBusiness, "sa1", "api_key", "keyA", 1); err != nil {
		t.Fatal(err)
	}
	e.seedQuotaHourly("sa1", "api_key", "keyA", "2026-04-18T11", 3)
	if _, err := refresher.RunQuotaHourlyWindows(context.Background()); err != nil {
		t.Fatal(err)
	}
	errorColumns := e.queryString(`SELECT COALESCE(last_error_message, '') FROM stats_job_state
		WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage-quota-hourly-windows-refresh'`)
	if len(errorColumns) != 1 || errorColumns[0] != "" {
		t.Fatalf("last_error_message after success = %v, want empty", errorColumns)
	}
	successAt := e.queryString(`SELECT last_success_at FROM stats_job_state
		WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage-quota-hourly-windows-refresh'`)
	if len(successAt) != 1 || successAt[0] == "" {
		t.Fatalf("last_success_at after success = %v, want non-empty", successAt)
	}
	assertQuotaWindowRows(t, e.quotaWindowRows(t),
		quotaWindowRow{"sa1", "api_key", "keyA", 1, 3},
	)
}
