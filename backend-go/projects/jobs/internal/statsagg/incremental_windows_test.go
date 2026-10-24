package statsagg

// incremental_windows_test.go 覆盖 overview / ai-performance 两个窗口 stage
// 的「天滚动全量 + 同日脏范围增量重建」契约（BUG-0327，契约见
// 《统计指标与分层聚合设计》「概览与 AI 性能窗口的脏范围增量重建」）：
//  1. 全量→增量对拍：增量轮窗口行集与同源数据直接全量重建完全一致；
//  2. 脏行 generation CAS 消费与 miss 保留（miss 行下轮被消费）；
//  3. 脏表空 → noop 零写入；
//  4. todayKey 天滚动回全量（行集含新窗口键）；
//  5. 孤儿清理：totals 删账户后其窗口行消失、global 行保留；
//  6. 脏表残留（CAS miss）时 RunStages 不推进 stats_job_state 水位。
//
// 契约 6 需要"消费 DELETE 零行命中"的并发模拟，使用 w13g5 的失败注入
// 驱动（armZero，SQLite scripted 保留），其余用例走生产方言 PG 载体
// （pgtestkit 隔离库；测试体内手写 SQL 按 PG 方言书写：juhe_ 前缀 schema
// 限定 + $n）。

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

// seedIncrementalComparisonData 写入 3 账户对拍种子：system_account daily /
// hourly / model / error 源 + account daily（ai-performance 源）+ totals。
func seedIncrementalComparisonData(t *testing.T, env *testEnv) {
	t.Helper()
	const updatedAt = "2026-04-18T09:00:00.000Z"
	for _, scope := range []string{"sa-a", "sa-b", "sa-c"} {
		env.exec(`INSERT INTO juhe_stats.usage_stats_totals (system_account_id, scope_type, scope_id, request_count, updated_at)
			VALUES ($1, 'system_account', $2, 1, $3)`, scope, scope, updatedAt)
	}
	insertDaily := func(system, scopeType, scopeID, statDate string, request, input, cost float64) {
		env.exec(`INSERT INTO juhe_stats.usage_stats_daily (system_account_id, scope_type, scope_id, stat_date,
			request_count, input_tokens, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
			first_token_ms_sum, first_token_ms_count, first_token_ms_max, last_used_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, 100, 50, 1, 50, $9, $10)`,
			system, scopeType, scopeID, statDate, request, input, cost, 100*request, updatedAt, updatedAt)
	}
	insertDaily("sa-a", "system_account", "sa-a", "2026-04-17", 2, 20, 0.2)
	insertDaily("sa-a", "system_account", "sa-a", "2026-04-18", 3, 30, 0.3)
	insertDaily("sa-b", "system_account", "sa-b", "2026-04-18", 5, 50, 0.5)
	insertDaily("sa-c", "system_account", "sa-c", "2026-04-16", 1, 10, 0.1)
	// ai-performance 源（scope_type='account'）。
	insertDaily("sa-a", "account", "acc-1", "2026-04-18", 4, 40, 0.4)
	// trend 源（hourly）。
	env.exec(`INSERT INTO juhe_stats.usage_stats_hourly (system_account_id, scope_type, scope_id, stat_hour,
		request_count, error_count, input_tokens, output_tokens, total_cost_usd, duration_ms_sum, duration_ms_count, updated_at)
		VALUES ('sa-a', 'system_account', 'sa-a', '2026-04-18T10', 2, 1, 20, 10, 0.2, 200, 1, $1)`, updatedAt)
	// model / error rank 源。
	env.exec(`INSERT INTO juhe_stats.usage_model_daily (system_account_id, stat_date, provider_code, model, request_count, total_cost_usd, updated_at)
		VALUES ('sa-a', '2026-04-18', 'openai', 'm-1', 3, 0.3, $1),
		       ('sa-a', '2026-04-17', 'openai', 'm-2', 2, 0.2, $2)`, updatedAt, updatedAt)
	env.exec(`INSERT INTO juhe_stats.usage_error_daily (system_account_id, stat_date, error_group, provider_code, error_code, status_code, error_count, updated_at)
		VALUES ('sa-a', '2026-04-18', 'openai', 'openai', 'rate_limited', 429, 2, $1)`, updatedAt)
}

// applyIncrementalMutation 追加 sa-a 的 system_account daily（04-16 新日期）
// 与 account daily（04-17 新日期）：增量轮与对拍全量轮共用同一最终源状态。
func applyIncrementalMutation(t *testing.T, env *testEnv) {
	t.Helper()
	const updatedAt = "2026-04-18T09:00:00.000Z"
	env.exec(`INSERT INTO juhe_stats.usage_stats_daily (system_account_id, scope_type, scope_id, stat_date,
		request_count, input_tokens, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
		first_token_ms_sum, first_token_ms_count, first_token_ms_max, last_used_at, updated_at)
		VALUES ('sa-a', 'system_account', 'sa-a', '2026-04-16', 7, 70, 0.7, 700, 1, 700, 350, 1, 350, $1, $2)`,
		updatedAt, updatedAt)
	env.exec(`INSERT INTO juhe_stats.usage_stats_daily (system_account_id, scope_type, scope_id, stat_date,
		request_count, input_tokens, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
		first_token_ms_sum, first_token_ms_count, first_token_ms_max, last_used_at, updated_at)
		VALUES ('sa-a', 'account', 'acc-1', '2026-04-17', 2, 20, 0.2, 200, 1, 200, 100, 1, 100, $1, $2)`,
		updatedAt, updatedAt)
}

// markIncrementalDirty 为 sa-a 写入 overview 与 ai-performance 脏行
// （generation 1，与消费 CAS 匹配）。
func markIncrementalDirty(t *testing.T, env *testEnv) {
	t.Helper()
	markOverviewDirty(t, env)
	const updatedAt = "2026-04-18T09:00:00.000Z"
	env.exec(`INSERT INTO juhe_stats.ai_performance_summary_dirty_system_accounts (system_account_id, min_stat_date, max_stat_date, generation, first_dirty_at, updated_at)
		VALUES ('sa-a', '2026-04-17', '2026-04-18', 1, $1, $2)`, updatedAt, updatedAt)
}

// markOverviewDirty 仅写 overview 脏行（单跑 overview stage 的用例使用）。
func markOverviewDirty(t *testing.T, env *testEnv) {
	t.Helper()
	const updatedAt = "2026-04-18T09:00:00.000Z"
	env.exec(`INSERT INTO juhe_stats.usage_overview_dirty_scopes (system_account_id, scope_id, min_changed_date, generation, first_dirty_at, updated_at)
		VALUES ('sa-a', 'sa-a', '2026-04-16', 1, $1, $2)`, updatedAt, updatedAt)
}

// snapshotWindowTable 以 SELECT * 捕获整表行集（列按存储序拼接为文本行），
// 用于增量/全量行集对拍与 noop 零写入断言。
func snapshotWindowTable(t *testing.T, db *sql.DB, table, orderBy string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY ` + orderBy)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result []string
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(columns))
		for index, value := range values {
			if value == nil {
				parts[index] = "∅"
				continue
			}
			parts[index] = fmt.Sprintf("%v", value)
		}
		result = append(result, strings.Join(parts, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

// windowSnapshotKeys 是 5 张窗口表的 (表名, 排序键) 清单。
var windowSnapshotKeys = []struct {
	table   string
	orderBy string
}{
	{"juhe_stats.usage_overview_summary_windows", "system_account_id, window_key"},
	{"juhe_stats.usage_overview_trend_windows", "system_account_id, window_key, bucket_key"},
	{"juhe_stats.usage_model_rank_windows", "system_account_id, window_key, rank"},
	{"juhe_stats.usage_error_rank_windows", "system_account_id, window_key, rank"},
	{"juhe_stats.ai_performance_summary_windows", "system_account_id, window_key"},
}

func snapshotAllWindows(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	for _, key := range windowSnapshotKeys {
		result[key.table] = snapshotWindowTable(t, db, key.table, key.orderBy)
	}
	return result
}

func assertWindowSnapshotsEqual(t *testing.T, label string, got, want []string) {
	t.Helper()
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s 行数不一致: got %d want %d", label, len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("%s 第 %d 行不一致:\n got=%s\nwant=%s", label, index, got[index], want[index])
		}
	}
}

func assertStageMode(t *testing.T, result UsageRankSnapshotRefreshResult, want ...string) {
	t.Helper()
	if len(result.Stages) != len(want) {
		t.Fatalf("stage 数不一致: %+v", result.Stages)
	}
	for index, stage := range result.Stages {
		if stage.Mode != want[index] {
			t.Fatalf("stage %s mode = %q want %q", stage.Name, stage.Mode, want[index])
		}
	}
}

// 契约 1：全量→增量对拍——增量轮后 5 张窗口表行集与"同源数据直接全量
// 重建"排序后完全一致。
func TestIncrementalWindowsFullVsIncrementalParity(t *testing.T) {
	// env1：首轮全量 → 修改 sa-a 源数据 + 打脏 → 同日增量轮。
	envIncremental := newPGTestEnv(t)
	seedIncrementalComparisonData(t, envIncremental)
	refresher := envIncremental.refresher()
	if _, err := refresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows, StageAiPerformanceSummaryWindows}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	applyIncrementalMutation(t, envIncremental)
	markIncrementalDirty(t, envIncremental)
	incrementalResult, err := refresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows, StageAiPerformanceSummaryWindows}, RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertStageMode(t, incrementalResult, WindowRefreshModeIncremental, WindowRefreshModeIncremental)
	// 消费即删：两张脏表清空。
	envIncremental.assertFloats("overview dirty consumed",
		envIncremental.queryRowFloats(`SELECT COUNT(*) FROM juhe_stats.usage_overview_dirty_scopes`), 0)
	envIncremental.assertFloats("ai dirty consumed",
		envIncremental.queryRowFloats(`SELECT COUNT(*) FROM juhe_stats.ai_performance_summary_dirty_system_accounts`), 0)

	// env2：同一最终源状态直接全量重建。
	envFull := newPGTestEnv(t)
	seedIncrementalComparisonData(t, envFull)
	applyIncrementalMutation(t, envFull)
	if _, err := envFull.refresher().RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows, StageAiPerformanceSummaryWindows}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}

	got := snapshotAllWindows(t, envIncremental.db)
	want := snapshotAllWindows(t, envFull.db)
	for _, key := range windowSnapshotKeys {
		assertWindowSnapshotsEqual(t, "增量 vs 全量 "+key.table, got[key.table], want[key.table])
	}
	// 增量轮只重插了 sa-a 与 global，sa-b/sa-c 行保持全量轮原值（updated_at
	// 相同由快照对拍覆盖，这里点查新日期行证明重算生效）。
	envIncremental.assertFloats("incremental sa-a new date in summary",
		envIncremental.queryRowFloats(`SELECT request_count FROM juhe_stats.usage_overview_summary_windows
			WHERE system_account_id='sa-a' AND window_key='2026-03-19:2026-04-18'`),
		12) // 04-16(7) + 04-17(2) + 04-18(3)
	envIncremental.assertFloats("incremental sa-a ai new date",
		envIncremental.queryRowFloats(`SELECT request_count FROM juhe_stats.ai_performance_summary_windows
			WHERE system_account_id='sa-a' AND window_key='2026-03-19:2026-04-18'`),
		6) // account 04-17(2) + 04-18(4)
}

// 契约 2：脏行消费与 CAS 保留——generation 匹配的行被删除；并发更新推进
// generation 时本轮 CAS 未命中、行保留，下轮被消费。miss 的确定性模拟：
// 注入"消费 DELETE 零行命中"（等价于 claim 读取后并发事务推进了 generation
// 的交错；单线程下预先改大 generation 会被 claim 原样读出、消费必然命中，
// 无法构造真 miss）。
func TestIncrementalWindowsDirtyConsumeAndCASRetain(t *testing.T) {
	// 消费路径走生产方言 PG 载体；miss 轮为并发交错模拟，保留 w13g5 scripted
	// 注入环境（SQLite 载体，纪律：注入机制不动）。
	env := newPGTestEnv(t)
	seedIncrementalComparisonData(t, env)
	refresher := env.refresher()
	if _, err := refresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	// 追加源数据 + 打脏（generation 1）→ 增量轮消费。
	applyIncrementalMutation(t, env)
	markIncrementalDirty(t, env)
	result, err := refresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows}, RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertStageMode(t, result, WindowRefreshModeIncremental)
	env.assertFloats("consumed row deleted",
		env.queryRowFloats(`SELECT COUNT(*) FROM juhe_stats.usage_overview_dirty_scopes`), 0)
	// 打脏并重算生效：31 天全窗含新增 04-16 行（7+2+3=12；增量前为 5）。
	env.assertFloats("rebuilt with mutated source",
		env.queryRowFloats(`SELECT request_count FROM juhe_stats.usage_overview_summary_windows
			WHERE system_account_id='sa-a' AND window_key='2026-03-19:2026-04-18'`), 12)

	// miss 轮：注入消费零行命中 → 脏行保留。
	envMiss, spec := w13g5StatsOpenFailEnv(t)
	w13g5SeedOverviewData(t, envMiss)
	missRefresher := w13g5Refresher(envMiss)
	if _, err := missRefresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	envMiss.exec(`INSERT INTO usage_overview_dirty_scopes (system_account_id, scope_id, min_changed_date, generation, first_dirty_at, updated_at)
		VALUES ('w13g5-sa', 'w13g5-sa', '2026-09-18', 3, '2026-09-18T02:00:00.000Z', '2026-09-18T02:00:00.000Z')`)
	spec.armZero(`DELETE FROM usage_overview_dirty_scopes WHERE system_account_id = ? AND generation = ?`)
	result, err = missRefresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows}, RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec.disarm()
	assertStageMode(t, result, WindowRefreshModeIncremental)
	envMiss.assertFloats("cas miss row retained",
		envMiss.queryRowFloats(`SELECT COUNT(*) FROM usage_overview_dirty_scopes WHERE generation = 3`), 1)
	// 下轮：miss 行被消费。
	result, err = missRefresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows}, RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertStageMode(t, result, WindowRefreshModeIncremental)
	envMiss.assertFloats("miss row consumed next round",
		envMiss.queryRowFloats(`SELECT COUNT(*) FROM usage_overview_dirty_scopes`), 0)
}

// 契约 3：脏表空 → noop 零写入（窗口表逐行不变，含 updated_at）。
func TestIncrementalWindowsNoopZeroWrite(t *testing.T) {
	env := newPGTestEnv(t)
	seedIncrementalComparisonData(t, env)
	refresher := env.refresher()
	if _, err := refresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows, StageAiPerformanceSummaryWindows}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	before := snapshotAllWindows(t, env.db)
	result, err := refresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows, StageAiPerformanceSummaryWindows}, RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertStageMode(t, result, WindowRefreshModeNoop, WindowRefreshModeNoop)
	after := snapshotAllWindows(t, env.db)
	for _, key := range windowSnapshotKeys {
		assertWindowSnapshotsEqual(t, "noop 零写入 "+key.table, after[key.table], before[key.table])
	}
}

// 契约 4：todayKey 前进一天 → 天滚动全量重建（行集含新窗口键）。
func TestIncrementalWindowsDayRolloverFullRebuild(t *testing.T) {
	env := newPGTestEnv(t)
	seedIncrementalComparisonData(t, env)
	refresher := env.refresher()
	result, err := refresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows}, RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertStageMode(t, result, WindowRefreshModeFull)
	env.assertFloats("no next-day window before rollover",
		env.queryRowFloats(`SELECT COUNT(*) FROM juhe_stats.usage_overview_summary_windows WHERE window_key='2026-04-19:2026-04-19'`), 0)
	// 前进一天并补新日期源数据 → 全量重建，行集含新窗口键。
	env.exec(`INSERT INTO juhe_stats.usage_stats_daily (system_account_id, scope_type, scope_id, stat_date,
		request_count, input_tokens, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
		first_token_ms_sum, first_token_ms_count, first_token_ms_max, last_used_at, updated_at)
		VALUES ('sa-a', 'system_account', 'sa-a', '2026-04-19', 9, 90, 0.9, 900, 1, 900, 450, 1, 450,
		'2026-04-19T09:00:00.000Z', '2026-04-19T09:00:00.000Z')`)
	env.now = env.now.Add(24 * time.Hour)
	result, err = refresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows}, RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertStageMode(t, result, WindowRefreshModeFull)
	env.assertFloats("rollover new window key",
		env.queryRowFloats(`SELECT request_count FROM juhe_stats.usage_overview_summary_windows
			WHERE system_account_id='sa-a' AND window_key='2026-04-19:2026-04-19'`), 9)
	env.assertFloats("rollover dirty table untouched by full rebuild",
		env.queryRowFloats(`SELECT COUNT(*) FROM juhe_stats.usage_overview_dirty_scopes`), 0)
}

// 契约 5：孤儿清理——totals 删除账户后，非空增量轮删除其全部窗口行，
// global 行保留。
func TestIncrementalWindowsOrphanCleanupKeepsGlobal(t *testing.T) {
	env := newPGTestEnv(t)
	seedIncrementalComparisonData(t, env)
	refresher := env.refresher()
	if _, err := refresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows, StageAiPerformanceSummaryWindows}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	// 全量轮基线：sa-b 与 global 都有窗口行。
	env.assertFloats("baseline orphan account summary rows",
		env.queryRowFloats(`SELECT COUNT(*) FROM juhe_stats.usage_overview_summary_windows WHERE system_account_id='sa-b'`), 496)
	globalSummaryBefore := env.queryRowFloats(`SELECT COUNT(*) FROM juhe_stats.usage_overview_summary_windows WHERE system_account_id='global'`)[0]
	globalAiBefore := env.queryRowFloats(`SELECT COUNT(*) FROM juhe_stats.ai_performance_summary_windows WHERE system_account_id='global'`)[0]
	// totals 删除 sa-b（孤儿），并打脏 sa-a 使增量轮非空。
	env.exec(`DELETE FROM juhe_stats.usage_stats_totals WHERE system_account_id = 'sa-b'`)
	markIncrementalDirty(t, env)
	result, err := refresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows, StageAiPerformanceSummaryWindows}, RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertStageMode(t, result, WindowRefreshModeIncremental, WindowRefreshModeIncremental)
	for _, table := range overviewWindowTableNames {
		env.assertFloats("orphan rows removed from "+table,
			env.queryRowFloats(`SELECT COUNT(*) FROM juhe_stats.`+table+` WHERE system_account_id='sa-b'`), 0)
	}
	env.assertFloats("orphan rows removed from ai table",
		env.queryRowFloats(`SELECT COUNT(*) FROM juhe_stats.ai_performance_summary_windows WHERE system_account_id='sa-b'`), 0)
	env.assertFloats("global summary rows preserved",
		env.queryRowFloats(`SELECT COUNT(*) FROM juhe_stats.usage_overview_summary_windows WHERE system_account_id='global'`), globalSummaryBefore)
	env.assertFloats("global ai rows preserved",
		env.queryRowFloats(`SELECT COUNT(*) FROM juhe_stats.ai_performance_summary_windows WHERE system_account_id='global'`), globalAiBefore)
	env.assertFloats("rebuilt account rows preserved",
		env.queryRowFloats(`SELECT COUNT(*) FROM juhe_stats.usage_overview_summary_windows WHERE system_account_id='sa-a'`), 496)
}

// 契约 6：脏表残留（CAS miss 场景，注入消费 DELETE 零行命中）→ RunStages
// 不推进 stats_job_state 水位；残留排空后水位恢复推进。
func TestIncrementalWindowsWatermarkRetainedOnDirtyResidue(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	w13g5SeedOverviewData(t, env)
	refresher := w13g5Refresher(env)
	options := RefreshOptions{SkipIfUnchanged: true, JobName: "t-watermark-retain"}
	// 全量轮：水位推进到当前源水位（w13g5 种子 updated_at = 01:00Z）。
	if _, err := refresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows}, options); err != nil {
		t.Fatal(err)
	}
	firstWatermark := env.queryString(`SELECT cursor_created_at || '|' || cursor_id FROM stats_job_state WHERE job_name='t-watermark-retain'`)
	if len(firstWatermark) != 1 || firstWatermark[0] != "2026-09-18T01:00:00.000Z|2026-09-18" {
		t.Fatalf("全量轮水位 = %v", firstWatermark)
	}
	// 源表推进（否则整轮被 watermark-skip）+ 打脏，并注入消费 DELETE 零行
	// 命中模拟并发 generation 推进（全 miss）。
	env.exec(`UPDATE usage_stats_daily SET updated_at='2026-09-18T03:00:00.000Z'`)
	env.exec(`INSERT INTO usage_overview_dirty_scopes (system_account_id, scope_id, min_changed_date, generation, first_dirty_at, updated_at)
		VALUES ('w13g5-sa', 'w13g5-sa', '2026-09-18', 1, '2026-09-18T03:00:00.000Z', '2026-09-18T03:00:00.000Z')`)
	spec.armZero(`DELETE FROM usage_overview_dirty_scopes WHERE system_account_id = ? AND generation = ?`)
	result, err := refresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows}, options)
	if err != nil {
		t.Fatal(err)
	}
	spec.disarm()
	assertStageMode(t, result, WindowRefreshModeIncremental)
	// 脏表残留 → 水位未推进（cursor_created_at 仍是 01:00Z 而非 03:00Z）。
	if got := env.queryString(`SELECT cursor_created_at || '|' || cursor_id FROM stats_job_state WHERE job_name='t-watermark-retain'`); len(got) != 1 || got[0] != firstWatermark[0] {
		t.Fatalf("残留脏行时水位不得推进: got %v want %v", got, firstWatermark)
	}
	env.assertFloats("dirty row retained after all-miss round",
		env.queryRowFloats(`SELECT COUNT(*) FROM usage_overview_dirty_scopes`), 1)
	// 注入解除后再跑一轮：残留被消费，水位恢复推进到新源水位。
	result, err = refresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows}, options)
	if err != nil {
		t.Fatal(err)
	}
	assertStageMode(t, result, WindowRefreshModeIncremental)
	env.assertFloats("residue consumed",
		env.queryRowFloats(`SELECT COUNT(*) FROM usage_overview_dirty_scopes`), 0)
	if got := env.queryString(`SELECT cursor_created_at FROM stats_job_state WHERE job_name='t-watermark-retain'`); len(got) != 1 || got[0] != "2026-09-18T03:00:00.000Z" {
		t.Fatalf("残留排空后水位应推进: got %v", got)
	}
}
