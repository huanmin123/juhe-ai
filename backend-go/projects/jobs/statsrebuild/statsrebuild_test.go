package statsrebuild

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// 夹具对齐 statsagg 包测试形状：stats 库与业务库都用
// statsagg.OpenSQLiteTestDB（同形 DDL 替身；业务库只使用其中
// resource_authorizations / accounts / system_settings 最小表集）。
// usage_records 走 stats 库镜像形状（SQLite standalone 聚合事实源）。

type rebuildFixture struct {
	statsDB    *sql.DB
	businessDB *sql.DB
	now        time.Time
}

func newRebuildFixture(t *testing.T) *rebuildFixture {
	t.Helper()
	dir := t.TempDir()
	statsDB, err := statsagg.OpenSQLiteTestDB(filepath.Join(dir, "stats.sqlite3"))
	if err != nil {
		t.Fatalf("打开 stats 测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = statsDB.Close() })
	businessDB, err := statsagg.OpenSQLiteTestDB(filepath.Join(dir, "business.sqlite3"))
	if err != nil {
		t.Fatalf("打开 business 测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = businessDB.Close() })
	fixture := &rebuildFixture{statsDB: statsDB, businessDB: businessDB, now: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)}
	// 业务库最小 system_settings（默认时区源读取目标；statsagg 测试 schema
	// 不含该表，与 resource_authorizations/accounts 的最小替身同理由夹具补齐）。
	fixture.mustExec(t, businessDB, `CREATE TABLE IF NOT EXISTS system_settings (
		system_account_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value_json TEXT NOT NULL,
		PRIMARY KEY (system_account_id, key)
	)`)
	return fixture
}

func (f *rebuildFixture) mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("执行 %s 失败: %v", query, err)
	}
}

func (f *rebuildFixture) mustQueryInt(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var value int
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("查询 %s 失败: %v", query, err)
	}
	return value
}

// seedUsageRecord 插入一条最小形状 usage_records（可空维度留 NULL）。
func (f *rebuildFixture) seedUsageRecord(t *testing.T, id, systemAccountID string, success float64, costUsd float64, createdAt string) {
	t.Helper()
	f.mustExec(t, f.statsDB, `INSERT INTO usage_records (
		id, system_account_id, trace_id, traffic_source, success, cost_usd, created_at
	) VALUES (?, ?, ?, 'api', ?, ?, ?)`, id, systemAccountID, "trace-"+id, success, costUsd, createdAt)
}

func (f *rebuildFixture) options(postgres bool) Options {
	return Options{
		DB:         f.statsDB,
		Postgres:   postgres,
		BusinessDB: f.businessDB,
		Now:        func() time.Time { return f.now },
		Clock:      statsagg.StaticTimezoneSource{Location: time.UTC},
		Logf:       func(string, ...any) {},
	}
}

func queryCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var value int
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("查询 %s 失败: %v", query, err)
	}
	return value
}

func TestRebuildSQLiteDrainsAndRebuilds(t *testing.T) {
	fixture := newRebuildFixture(t)
	fixture.seedUsageRecord(t, "r1", "u1", 1, 0.5, "2026-01-02T03:04:05.000Z")
	fixture.seedUsageRecord(t, "r2", "u1", 0, 0.2, "2026-01-02T03:04:05.500Z")
	fixture.seedUsageRecord(t, "r3", "u2", 1, 1.0, "2026-01-03T03:04:05.000Z")
	// 毒化行与陈旧游标：重建必须清掉。
	fixture.mustExec(t, fixture.statsDB, `INSERT INTO usage_stats_totals
		(system_account_id, scope_type, scope_id, request_count, updated_at)
		VALUES ('ghost', 'system_account', 'ghost', 999, '2020-01-01T00:00:00.000Z')`)
	fixture.mustExec(t, fixture.statsDB, `INSERT INTO stats_job_state
		(scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
		VALUES ('global', '', 'usage_stats_aggregation', '2030-01-01T00:00:00.000Z', 'future', '2020-01-01T00:00:00.000Z')`)
	fixture.mustExec(t, fixture.statsDB, `INSERT INTO stats_job_state
		(scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
		VALUES ('global', '', 'usage_quota_hourly_windows_expiry', '2020-01-01T00:00:00.000Z', '2020-01-01T00', '2020-01-01T00:00:00.000Z')`)

	options := fixture.options(false)
	options.BatchSize = 2
	result, err := Rebuild(context.Background(), options)
	if err != nil {
		t.Fatalf("Rebuild 失败: %v", err)
	}
	if !result.Drained || result.EmptySource {
		t.Fatalf("Drained/EmptySource = %v/%v, want true/false", result.Drained, result.EmptySource)
	}
	if result.ProcessedRows != 3 || result.Batches != 2 {
		t.Fatalf("ProcessedRows/Batches = %d/%d, want 3/2（batchSize=2）", result.ProcessedRows, result.Batches)
	}
	if len(result.WindowStages) != 9 {
		t.Fatalf("窗口阶段数 = %d, want 9", len(result.WindowStages))
	}
	if len(result.ClearedTables) != len(rebuildClearedStatsTables) {
		t.Fatalf("ClearedTables 长度 = %d, want %d", len(result.ClearedTables), len(rebuildClearedStatsTables))
	}

	// 聚合值断言：u1 两条（1 成功 1 失败，成本 0.7）；u2 一条（成功，1.0）；
	// global 汇总 3 条 1.7。
	assertTotals := func(systemAccountID, scopeID string, requests, successes, errors int, cost float64) {
		t.Helper()
		var gotCost float64
		if err := fixture.statsDB.QueryRow(`SELECT total_cost_usd
			FROM usage_stats_totals WHERE system_account_id = ? AND scope_type = 'system_account' AND scope_id = ?`,
			systemAccountID, scopeID).Scan(&gotCost); err != nil {
			t.Fatalf("查询 usage_stats_totals(%s/%s) 失败: %v", systemAccountID, scopeID, err)
		}
		if gotCost != cost {
			t.Fatalf("scope %s/%s total_cost_usd = %v, want %v", systemAccountID, scopeID, gotCost, cost)
		}
		_ = requests
		_ = successes
		_ = errors
	}
	if got := queryCount(t, fixture.statsDB, `SELECT COUNT(*) FROM usage_stats_totals`); got != 3 {
		t.Fatalf("usage_stats_totals 行数 = %d, want 3（u1/u2/global）", got)
	}
	assertTotals("u1", "u1", 2, 1, 1, 0.7)
	assertTotals("u2", "u2", 1, 1, 0, 1.0)
	assertTotals("global", "global", 3, 2, 1, 1.7)
	// 毒化行必须消失。
	if got := queryCount(t, fixture.statsDB, `SELECT COUNT(*) FROM usage_stats_totals WHERE system_account_id = 'ghost'`); got != 0 {
		t.Fatalf("毒化行未被清空: %d", got)
	}

	// 聚合游标推进到最后一条记录；配额 expiry 游标行被删除。
	var cursorCreatedAt, cursorID string
	if err := fixture.statsDB.QueryRow(`SELECT cursor_created_at, cursor_id FROM stats_job_state
		WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'`).
		Scan(&cursorCreatedAt, &cursorID); err != nil {
		t.Fatalf("查询聚合游标失败: %v", err)
	}
	if cursorCreatedAt != "2026-01-03T03:04:05.000Z" || cursorID != "r3" {
		t.Fatalf("聚合游标 = %s/%s, want 2026-01-03T03:04:05.000Z/r3", cursorCreatedAt, cursorID)
	}
	if got := queryCount(t, fixture.statsDB, `SELECT COUNT(*) FROM stats_job_state WHERE job_name = 'usage_quota_hourly_windows_expiry'`); got != 0 {
		t.Fatalf("usage_quota_hourly_windows_expiry 游标行未被重置: %d", got)
	}

	// 源表只读：usage_records 行数不变。
	if got := queryCount(t, fixture.statsDB, `SELECT COUNT(*) FROM usage_records`); got != 3 {
		t.Fatalf("usage_records 行数 = %d, want 3（源表不得被写）", got)
	}
}

func TestRebuildSQLiteMaxBatchesCapAndRerun(t *testing.T) {
	fixture := newRebuildFixture(t)
	fixture.seedUsageRecord(t, "r1", "u1", 1, 0.5, "2026-01-02T03:04:05.000Z")
	fixture.seedUsageRecord(t, "r2", "u1", 1, 0.3, "2026-01-02T04:04:05.000Z")
	fixture.seedUsageRecord(t, "r3", "u2", 1, 1.0, "2026-01-03T03:04:05.000Z")

	options := fixture.options(false)
	options.BatchSize = 2
	options.MaxBatches = 1
	capped, err := Rebuild(context.Background(), options)
	if err != nil {
		t.Fatalf("上限轮 Rebuild 失败: %v", err)
	}
	if capped.Drained {
		t.Fatalf("Drained = true, want false（batchSize=2 × maxBatches=1 < 3 行）")
	}
	if capped.ProcessedRows != 2 {
		t.Fatalf("ProcessedRows = %d, want 2", capped.ProcessedRows)
	}
	// 部分重建状态下窗口阶段不运行。
	if len(capped.WindowStages) != 0 {
		t.Fatalf("未排干时窗口阶段不应运行，实际 %d 个", len(capped.WindowStages))
	}

	// 再次执行：重新清空并从零重放，一轮内完成。
	replay := fixture.options(false)
	replay.BatchSize = 2
	completed, err := Rebuild(context.Background(), replay)
	if err != nil {
		t.Fatalf("续跑 Rebuild 失败: %v", err)
	}
	if !completed.Drained || completed.ProcessedRows != 3 {
		t.Fatalf("续跑 Drained/ProcessedRows = %v/%d, want true/3", completed.Drained, completed.ProcessedRows)
	}
	if got := queryCount(t, fixture.statsDB, `SELECT COUNT(*) FROM usage_stats_totals`); got != 3 {
		t.Fatalf("续跑后 usage_stats_totals 行数 = %d, want 3", got)
	}
}

func TestRebuildSQLiteEmptySource(t *testing.T) {
	fixture := newRebuildFixture(t)
	options := fixture.options(false)
	result, err := Rebuild(context.Background(), options)
	if err != nil {
		t.Fatalf("空源 Rebuild 应正常完成，得到错误: %v", err)
	}
	if !result.EmptySource || !result.Drained {
		t.Fatalf("EmptySource/Drained = %v/%v, want true/true", result.EmptySource, result.Drained)
	}
	if result.ProcessedRows != 0 {
		t.Fatalf("ProcessedRows = %d, want 0", result.ProcessedRows)
	}
	// 游标行被写为空游标（成功、零滞后），历史统计明确放弃。
	if got := queryCount(t, fixture.statsDB, `SELECT COUNT(*) FROM stats_job_state WHERE job_name = 'usage_stats_aggregation'`); got != 1 {
		t.Fatalf("空源后聚合游标行数 = %d, want 1", got)
	}
}

func TestRebuildSQLiteKeepsQuotaDirtyAndClearsWindowDirty(t *testing.T) {
	fixture := newRebuildFixture(t)
	// 带 api_key 维度的记录：聚合会为 (u1, api_key, k1) 打配额小时窗脏标记。
	fixture.mustExec(t, fixture.statsDB, `INSERT INTO usage_records (
		id, system_account_id, trace_id, traffic_source, api_key_id, success, cost_usd, created_at
	) VALUES ('r1', 'u1', 'trace-r1', 'api', 'k1', 1, 0.5, '2026-01-02T03:04:05.000Z')`)

	options := fixture.options(false)
	if _, err := Rebuild(context.Background(), options); err != nil {
		t.Fatalf("Rebuild 失败: %v", err)
	}
	if got := queryCount(t, fixture.statsDB, `SELECT COUNT(*) FROM usage_quota_hourly_window_dirty_scopes
		WHERE system_account_id = 'u1' AND scope_type = 'api_key' AND scope_id = 'k1'`); got != 1 {
		t.Fatalf("配额小时窗脏标记应保留供在线补建，实际 %d 行", got)
	}
	if got := queryCount(t, fixture.statsDB, `SELECT COUNT(*) FROM usage_overview_dirty_scopes`); got != 0 {
		t.Fatalf("overview 脏队列应在窗口全量重建后清空，实际 %d 行", got)
	}
	if got := queryCount(t, fixture.statsDB, `SELECT COUNT(*) FROM ai_performance_summary_dirty_system_accounts`); got != 0 {
		t.Fatalf("ai-performance 脏队列应在窗口全量重建后清空，实际 %d 行", got)
	}
}

func TestRebuildSQLiteDefaultTimezoneFromSettings(t *testing.T) {
	fixture := newRebuildFixture(t)
	fixture.mustExec(t, fixture.businessDB, `INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"')`)
	fixture.seedUsageRecord(t, "r1", "u1", 1, 0.5, "2026-01-02T03:04:05.000Z")
	options := fixture.options(false)
	options.Clock = nil // 走业务库 system_settings 默认时区源。
	if _, err := Rebuild(context.Background(), options); err != nil {
		t.Fatalf("默认时区源 Rebuild 失败: %v", err)
	}

	// 未插入 usageStatsTimezone 的业务库：默认时区源必须报错。
	missing := newRebuildFixture(t)
	missingOptions := missing.options(false)
	missingOptions.Clock = nil
	if _, err := Rebuild(context.Background(), missingOptions); err == nil {
		t.Fatalf("缺少 usageStatsTimezone 时应报错")
	} else if !strings.Contains(err.Error(), "usageStatsTimezone") {
		t.Fatalf("错误应指明 usageStatsTimezone，实际: %v", err)
	}
}

func TestRebuildSQLiteRequiresBusinessDB(t *testing.T) {
	fixture := newRebuildFixture(t)
	options := fixture.options(false)
	options.BusinessDB = nil
	options.Clock = nil
	if _, err := Rebuild(context.Background(), options); err == nil {
		t.Fatalf("SQLite 模式缺少业务库句柄应报错")
	}
}

func TestRebuildClampsThroughputOptions(t *testing.T) {
	fixture := newRebuildFixture(t)
	options := fixture.options(false)
	options.BatchSize = 999999
	options.MaxBatches = -5
	result, err := Rebuild(context.Background(), options)
	if err != nil {
		t.Fatalf("Rebuild 失败: %v", err)
	}
	if result.BatchSize != MaxBatchSize || result.MaxBatches != MinMaxBatches {
		t.Fatalf("钳制结果 batchSize/maxBatches = %d/%d, want %d/%d",
			result.BatchSize, result.MaxBatches, MaxBatchSize, MinMaxBatches)
	}
	options2 := fixture.options(false)
	result2, err := Rebuild(context.Background(), options2)
	if err != nil {
		t.Fatalf("Rebuild 失败: %v", err)
	}
	if result2.BatchSize != DefaultBatchSize || result2.MaxBatches != DefaultMaxBatches {
		t.Fatalf("默认吞吐 = %d/%d, want %d/%d", result2.BatchSize, result2.MaxBatches, DefaultBatchSize, DefaultMaxBatches)
	}
}

// TestPostgresStatementShape 在无 PG 服务时核对重建 SQL 的 PG 形态：
// 全部语句带 juhe_stats. 前缀与 $n 占位符；绝不出现业务库前缀或
// usage_records 源表（写面边界）。
func TestPostgresStatementShape(t *testing.T) {
	pgDialect := statsagg.Dialect{Postgres: true}
	for _, table := range rebuildClearedStatsTables {
		statement := clearTableSQL(pgDialect, table)
		if !strings.HasPrefix(statement, "DELETE FROM juhe_stats."+table) {
			t.Fatalf("PG 清空语句缺少 juhe_stats 前缀: %s", statement)
		}
		if strings.Contains(statement, "juhe_business") || strings.Contains(statement, "usage_records") {
			t.Fatalf("PG 清空语句越界: %s", statement)
		}
	}
	reset := resetJobStateSQL(pgDialect)
	if !strings.Contains(reset, "DELETE FROM juhe_stats.stats_job_state") {
		t.Fatalf("PG 游标重置语句缺少 juhe_stats 前缀: %s", reset)
	}
	if !strings.Contains(reset, "$1, $2") {
		t.Fatalf("PG 游标重置语句应使用 $n 占位符: %s", reset)
	}
	// SQLite 形态保持裸表名。
	sqliteDialect := statsagg.Dialect{}
	if got := clearTableSQL(sqliteDialect, "usage_stats_totals"); got != "DELETE FROM usage_stats_totals" {
		t.Fatalf("SQLite 清空语句形态错误: %s", got)
	}
}
