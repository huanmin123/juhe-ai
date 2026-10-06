package statsagg

import (
	"context"
	"fmt"
	"testing"
)

// overview 阶段多行 VALUES 批量路径的行集语义回归：
// 多 scope（含全空 scope）× 全部 FixedUsageStatsRanges（496 个区间）下，
// summary 每 (scope, window_key) 恰一行、trend 桶键集合与值、model/error
// rank 每范围 top10 与 rank 序，与逐行单行 INSERT 语义一致；另覆盖多行
// 占位符双方言（SQLite `?` 原样、PG `$1..$n` 跨行连续编号）。
// 推导依据在断言处标注；summary 每 scope 496 行 > overviewWindowInsertMaxRows
// (200)，天然覆盖 200/200/96 分片边界。
func TestOverviewWindowBatchMultiScopeGolden(t *testing.T) {
	env := newTestEnv(t)
	const updatedAt = "2026-04-18T09:00:00.000Z"
	for _, scope := range []string{"batch-a", "batch-b", "batch-empty"} {
		env.exec(`INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, updated_at)
			VALUES (?, 'system_account', ?, 1, ?)`, scope, scope, updatedAt)
	}
	// summary 种子：duration sum=100×request、count=1；first_token sum=50、
	// count=1（同 TestWindowStagesGolden.insertDaily 规则）。
	insertDaily := func(system, statDate string, request, input, cost float64) {
		env.exec(`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date,
			request_count, input_tokens, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
			first_token_ms_sum, first_token_ms_count, first_token_ms_max, last_used_at, updated_at)
			VALUES (?, 'system_account', ?, ?, ?, ?, ?, ?, 1, 100, 50, 1, 50, ?, ?)`,
			system, system, statDate, request, input, cost, 100*request, updatedAt, updatedAt)
	}
	// batch-a 四个日期 → today/last7d/两天窗/31 天全窗值互不相同（≥3 ranges）。
	insertDaily("batch-a", "2026-04-10", 6, 60, 0.6)
	insertDaily("batch-a", "2026-04-16", 2, 20, 0.2)
	insertDaily("batch-a", "2026-04-17", 3, 30, 0.3)
	insertDaily("batch-a", "2026-04-18", 4, 40, 0.4)
	insertDaily("batch-b", "2026-04-18", 7, 70, 0.7)
	// batch-empty 无任何 daily/hourly/model/error 数据 → summary 全零行 +
	// last_used_at NULL，trend/rank 零行。
	insertHourly := func(statHour string, request, errorCount float64) {
		env.exec(`INSERT INTO usage_stats_hourly (system_account_id, scope_type, scope_id, stat_hour,
			request_count, error_count, input_tokens, output_tokens, total_cost_usd, duration_ms_sum, duration_ms_count, updated_at)
			VALUES ('batch-a', 'system_account', 'batch-a', ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
			statHour, request, errorCount, request*10, request, request, request*100, updatedAt)
	}
	insertHourly("2026-04-10T12", 5, 0)
	insertHourly("2026-04-17T08", 1, 0)
	insertHourly("2026-04-17T09", 2, 0)
	insertHourly("2026-04-18T10", 3, 1)
	insertHourly("2026-04-18T11", 4, 0)
	// model rank 种子：04-18 同日 14 个模型（含 request 并列对，验证
	// CompareText provider/model 升序决胜），04-17 单模型 100 次使 last7d
	// 与 today 的 top10 首位不同并触发 top-10 截断。
	insertModel := func(system, statDate, model string, request float64) {
		env.exec(`INSERT INTO usage_model_daily (system_account_id, stat_date, provider_code, model, request_count, total_cost_usd, updated_at)
			VALUES (?, ?, 'openai', ?, ?, 0.1, ?)`, system, statDate, model, request, updatedAt)
	}
	insertModel("batch-a", "2026-04-17", "m-old", 100)
	insertModel("batch-a", "2026-04-18", "m-tie-a", 15)
	insertModel("batch-a", "2026-04-18", "m-tie-b", 15)
	for index := 1; index <= 12; index++ {
		insertModel("batch-a", "2026-04-18", fmt.Sprintf("m-%02d", index), float64(index))
	}
	insertModel("batch-b", "2026-04-18", "m-b1", 1)
	// error rank 种子：error_count 9/3/3/1，其中 3 并列且 error_message 为空
	//（NULL 传递）。
	insertError := func(errorCode string, statusCode float64, errorMessage string, count float64) {
		env.exec(`INSERT INTO usage_error_daily (system_account_id, stat_date, error_group, provider_code, error_code, status_code, error_message, error_count, updated_at)
			VALUES ('batch-a', '2026-04-18', 'openai', 'openai', ?, ?, ?, ?, ?)`, errorCode, statusCode, errorMessage, count, updatedAt)
	}
	insertError("e-high", 504, "upstream timeout", 9)
	insertError("e-tie-a", 429, "", 3)
	insertError("e-tie-b", 429, "", 3)
	insertError("e-low", 500, "boom", 1)

	refresher := env.refresher()
	if _, err := refresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}

	// ---- summary：4 scopes（3 种子 + global 兜底）× 496 ranges 恰好一行一对 ----
	env.assertFloats("summary total rows",
		env.queryRowFloats(`SELECT COUNT(*) FROM usage_overview_summary_windows`), 4*496)
	env.assertFloats("summary distinct window keys",
		env.queryRowFloats(`SELECT COUNT(DISTINCT window_key) FROM usage_overview_summary_windows`), 496)
	env.assertFloats("summary no duplicate scope-window",
		env.queryRowFloats(`SELECT COUNT(*) FROM (
			SELECT system_account_id, window_key FROM usage_overview_summary_windows
			GROUP BY system_account_id, window_key HAVING COUNT(*) > 1)`), 0)
	// batch-a today（04-18 一行）。
	env.assertFloats("summary batch-a today",
		env.queryRowFloats(`SELECT request_count, input_tokens, total_cost_usd, duration_ms_sum, duration_ms_count
			FROM usage_overview_summary_windows WHERE system_account_id='batch-a' AND window_key='2026-04-18:2026-04-18'`),
		4, 40, 0.4, 400, 1)
	// batch-a last7d：04-16/17/18 → 9、90、0.9、900、3。
	env.assertFloats("summary batch-a last7d",
		env.queryRowFloats(`SELECT request_count, input_tokens, total_cost_usd, duration_ms_sum, duration_ms_count
			FROM usage_overview_summary_windows WHERE system_account_id='batch-a' AND window_key='2026-04-12:2026-04-18'`),
		9, 90, 0.9, 900, 3)
	// batch-a 两天窗（非热窗 range）：04-17/18 → 7、70、0.7、700、2。
	env.assertFloats("summary batch-a two-day window",
		env.queryRowFloats(`SELECT request_count, input_tokens, total_cost_usd, duration_ms_sum, duration_ms_count
			FROM usage_overview_summary_windows WHERE system_account_id='batch-a' AND window_key='2026-04-17:2026-04-18'`),
		7, 70, 0.7, 700, 2)
	// batch-a 31 天全窗：四天合计 15、150、1.5、1500、4。
	env.assertFloats("summary batch-a full window",
		env.queryRowFloats(`SELECT request_count, input_tokens, total_cost_usd, duration_ms_sum, duration_ms_count
			FROM usage_overview_summary_windows WHERE system_account_id='batch-a' AND window_key='2026-03-19:2026-04-18'`),
		15, 150, 1.5, 1500, 4)
	// batch-a last7d 的 last_used_at 经多行 VALUES 非空路径写入。
	if got := env.queryString(`SELECT last_used_at FROM usage_overview_summary_windows
		WHERE system_account_id='batch-a' AND window_key='2026-04-12:2026-04-18'`); len(got) != 1 || got[0] != updatedAt {
		t.Fatalf("summary batch-a last_used_at = %v want %s", got, updatedAt)
	}
	// batch-b last7d。
	env.assertFloats("summary batch-b last7d",
		env.queryRowFloats(`SELECT request_count, input_tokens, total_cost_usd, duration_ms_sum, duration_ms_count
			FROM usage_overview_summary_windows WHERE system_account_id='batch-b' AND window_key='2026-04-12:2026-04-18'`),
		7, 70, 0.7, 700, 1)
	// batch-empty：全零值 + last_used_at 全 NULL。
	env.assertFloats("summary empty scope zeros",
		env.queryRowFloats(`SELECT request_count, input_tokens, total_cost_usd
			FROM usage_overview_summary_windows WHERE system_account_id='batch-empty' AND window_key='2026-04-18:2026-04-18'`),
		0, 0, 0)
	env.assertFloats("summary empty scope row count",
		env.queryRowFloats(`SELECT COUNT(*) FROM usage_overview_summary_windows WHERE system_account_id='batch-empty'`), 496)
	if got := env.queryString(`SELECT system_account_id FROM usage_overview_summary_windows
		WHERE system_account_id='batch-empty' AND last_used_at IS NOT NULL`); len(got) != 0 {
		t.Fatalf("summary empty scope last_used_at must stay NULL, got %v", got)
	}

	// ---- trend：桶键集合与值 ----
	// today（days=1 → 1h 桶）。
	assertStringList(t, "trend batch-a today keys",
		env.queryString(`SELECT bucket_key FROM usage_overview_trend_windows
			WHERE system_account_id='batch-a' AND window_key='2026-04-18:2026-04-18' ORDER BY bucket_key ASC`),
		[]string{"2026-04-18T10", "2026-04-18T11"})
	env.assertFloats("trend batch-a today 10h",
		env.queryRowFloats(`SELECT request_count, error_count FROM usage_overview_trend_windows
			WHERE system_account_id='batch-a' AND window_key='2026-04-18:2026-04-18' AND bucket_key='2026-04-18T10'`),
		3, 1)
	env.assertFloats("trend batch-a today 11h",
		env.queryRowFloats(`SELECT request_count, error_count FROM usage_overview_trend_windows
			WHERE system_account_id='batch-a' AND window_key='2026-04-18:2026-04-18' AND bucket_key='2026-04-18T11'`),
		4, 0)
	// last7d（days=7 → 24h 桶）。
	assertStringList(t, "trend batch-a last7d keys",
		env.queryString(`SELECT bucket_key FROM usage_overview_trend_windows
			WHERE system_account_id='batch-a' AND window_key='2026-04-12:2026-04-18' ORDER BY bucket_key ASC`),
		[]string{"2026-04-17", "2026-04-18"})
	env.assertFloats("trend batch-a last7d 04-17",
		env.queryRowFloats(`SELECT request_count FROM usage_overview_trend_windows
			WHERE system_account_id='batch-a' AND window_key='2026-04-12:2026-04-18' AND bucket_key='2026-04-17'`),
		3)
	// 两天窗（days=2 → 6h 桶）。
	assertStringList(t, "trend batch-a two-day keys",
		env.queryString(`SELECT bucket_key FROM usage_overview_trend_windows
			WHERE system_account_id='batch-a' AND window_key='2026-04-17:2026-04-18' ORDER BY bucket_key ASC`),
		[]string{"2026-04-17T06", "2026-04-18T06"})
	env.assertFloats("trend batch-a two-day 17T06",
		env.queryRowFloats(`SELECT request_count FROM usage_overview_trend_windows
			WHERE system_account_id='batch-a' AND window_key='2026-04-17:2026-04-18' AND bucket_key='2026-04-17T06'`),
		3)
	// 31 天全窗（days=31 → 24h 桶，含 04-10 行）。
	assertStringList(t, "trend batch-a full keys",
		env.queryString(`SELECT bucket_key FROM usage_overview_trend_windows
			WHERE system_account_id='batch-a' AND window_key='2026-03-19:2026-04-18' ORDER BY bucket_key ASC`),
		[]string{"2026-04-10", "2026-04-17", "2026-04-18"})
	env.assertFloats("trend batch-a full 04-10",
		env.queryRowFloats(`SELECT request_count FROM usage_overview_trend_windows
			WHERE system_account_id='batch-a' AND window_key='2026-03-19:2026-04-18' AND bucket_key='2026-04-10'`),
		5)
	// 无 hourly 数据的 scope 与 global 不产生 trend 行。
	env.assertFloats("trend empty scopes",
		env.queryRowFloats(`SELECT COUNT(*) FROM usage_overview_trend_windows
			WHERE system_account_id IN ('batch-b', 'batch-empty', 'global')`), 0)

	// ---- model rank：每范围 top10 与 rank 序 ----
	assertStringList(t, "model rank batch-a today",
		env.queryString(`SELECT model FROM usage_model_rank_windows
			WHERE system_account_id='batch-a' AND window_key='2026-04-18:2026-04-18' ORDER BY rank ASC`),
		[]string{"m-tie-a", "m-tie-b", "m-12", "m-11", "m-10", "m-09", "m-08", "m-07", "m-06", "m-05"})
	assertStringList(t, "model rank batch-a last7d",
		env.queryString(`SELECT model FROM usage_model_rank_windows
			WHERE system_account_id='batch-a' AND window_key='2026-04-12:2026-04-18' ORDER BY rank ASC`),
		[]string{"m-old", "m-tie-a", "m-tie-b", "m-12", "m-11", "m-10", "m-09", "m-08", "m-07", "m-06"})
	env.assertFloats("model rank batch-a today rank1 request",
		env.queryRowFloats(`SELECT request_count FROM usage_model_rank_windows
			WHERE system_account_id='batch-a' AND window_key='2026-04-18:2026-04-18' AND rank=1`), 15)
	env.assertFloats("model rank rank bounds",
		env.queryRowFloats(`SELECT COUNT(*) FROM usage_model_rank_windows
			WHERE system_account_id='batch-a' AND (rank < 1 OR rank > 10)`), 0)
	env.assertFloats("model rank no duplicate window-rank",
		env.queryRowFloats(`SELECT COUNT(*) FROM (
			SELECT window_key, rank FROM usage_model_rank_windows WHERE system_account_id='batch-a'
			GROUP BY window_key, rank HAVING COUNT(*) > 1)`), 0)
	assertStringList(t, "model rank batch-b today",
		env.queryString(`SELECT model FROM usage_model_rank_windows
			WHERE system_account_id='batch-b' AND window_key='2026-04-18:2026-04-18' ORDER BY rank ASC`),
		[]string{"m-b1"})
	env.assertFloats("model rank empty scopes",
		env.queryRowFloats(`SELECT COUNT(*) FROM usage_model_rank_windows
			WHERE system_account_id IN ('batch-empty', 'global')`), 0)

	// ---- error rank：每范围 top10 与 rank 序（含 NULL error_message 传递）----
	assertStringList(t, "error rank batch-a today",
		env.queryString(`SELECT error_code FROM usage_error_rank_windows
			WHERE system_account_id='batch-a' AND window_key='2026-04-18:2026-04-18' ORDER BY rank ASC`),
		[]string{"e-high", "e-tie-a", "e-tie-b", "e-low"})
	env.assertFloats("error rank batch-a today rank1",
		env.queryRowFloats(`SELECT status_code, error_count FROM usage_error_rank_windows
			WHERE system_account_id='batch-a' AND window_key='2026-04-18:2026-04-18' AND rank=1`),
		504, 9)
	assertStringList(t, "error rank null messages",
		env.queryString(`SELECT error_code FROM usage_error_rank_windows
			WHERE system_account_id='batch-a' AND window_key='2026-04-18:2026-04-18' AND error_message IS NULL ORDER BY rank ASC`),
		[]string{"e-tie-a", "e-tie-b"})
	assertStringList(t, "error rank batch-a last7d",
		env.queryString(`SELECT error_code FROM usage_error_rank_windows
			WHERE system_account_id='batch-a' AND window_key='2026-04-12:2026-04-18' ORDER BY rank ASC`),
		[]string{"e-high", "e-tie-a", "e-tie-b", "e-low"})
	env.assertFloats("error rank rank bounds",
		env.queryRowFloats(`SELECT COUNT(*) FROM usage_error_rank_windows
			WHERE system_account_id='batch-a' AND (rank < 1 OR rank > 10)`), 0)
	env.assertFloats("error rank no duplicate window-rank",
		env.queryRowFloats(`SELECT COUNT(*) FROM (
			SELECT window_key, rank FROM usage_error_rank_windows WHERE system_account_id='batch-a'
			GROUP BY window_key, rank HAVING COUNT(*) > 1)`), 0)
	env.assertFloats("error rank empty scopes",
		env.queryRowFloats(`SELECT COUNT(*) FROM usage_error_rank_windows
			WHERE system_account_id IN ('batch-b', 'batch-empty', 'global')`), 0)

	// ---- 多行占位符双方言：SQLite `?` 原样，PG `$1..$n` 跨行连续编号 ----
	if got := (Dialect{Postgres: false}).bind(`INSERT INTO t (a, b) VALUES ` + placeholderRows(2, 3)); got != `INSERT INTO t (a, b) VALUES (?, ?, ?), (?, ?, ?)` {
		t.Fatalf("sqlite multi-row placeholders = %s", got)
	}
	if got := (Dialect{Postgres: true}).bind(`INSERT INTO t (a, b) VALUES ` + placeholderRows(3, 2)); got != `INSERT INTO t (a, b) VALUES ($1, $2), ($3, $4), ($5, $6)` {
		t.Fatalf("pg multi-row placeholders = %s", got)
	}
	if placeholderRows(1, 24) != "("+placeholders(24)+")" {
		t.Fatalf("placeholderRows single row must equal placeholders(24) row: %s", placeholderRows(1, 24))
	}
}

func assertStringList(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v want %v", label, got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("%s: got %v want %v", label, got, want)
		}
	}
}
