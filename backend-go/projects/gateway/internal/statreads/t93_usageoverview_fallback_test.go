package statreads

// T93-5 / C1-C2：usage-overview 窗口行缺失时回退事实表实时聚合。
// 全部为真 PG 契约测试（pgtestkit，JUHE_AI_TEST_EXTERNAL=1）。

import (
	"net/http"
	"testing"
)

func t93SeedOverviewDaily(t *testing.T, fixture *testFixture, statement string) {
	t.Helper()
	if _, err := fixture.db.Exec(statement); err != nil {
		t.Fatalf("seed usage_stats_daily: %v", err)
	}
}

// TestT93UsageOverviewSummaryFallsBackToRealtime 覆盖：多日区间窗口行缺失
// → 按 system_account scope 实时聚合 usage_stats_daily，非 0 且标记 realtime；
// 其他租户与非 system_account scope 行不串入。
func TestT93UsageOverviewSummaryFallsBackToRealtime(t *testing.T) {
	fixture := newFixture(t)
	t93SeedOverviewDaily(t, fixture, `
		INSERT INTO juhe_stats.usage_stats_daily (system_account_id, scope_type, scope_id, stat_date,
			request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
			total_cost_usd, duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count)
		VALUES
			('sys-user-1', 'system_account', 'sys-user-1', '2026-09-02', 3, 2, 1, 100, 50, 10, 0.5, 900, 3, 240, 2),
			('sys-user-1', 'system_account', 'sys-user-1', '2026-09-03', 7, 7, 0, 200, 100, 20, 1.5, 2100, 7, 560, 5),
			('sys-other', 'system_account', 'sys-other', '2026-09-02', 999, 999, 0, 999, 999, 0, 9.0, 999, 1, 999, 1),
			('sys-user-1', 'api_key', 'key-x', '2026-09-02', 555, 555, 0, 555, 555, 0, 5.0, 555, 1, 555, 1)`)
	handler := fixture.deps.overviewSectionHandler(false)
	recorder := invoke(t, handler, http.MethodGet,
		"/__aisys__/api/stats/usage-overview/summary?startDate=2026-09-02&endDate=2026-09-03", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("summary 应 200：%d %s", recorder.Code, recorder.Body.String())
	}
	data := dataMap(t, decodeBody(t, recorder))
	if data["dataSource"] != usageOverviewDataSourceRealtime {
		t.Fatalf("窗口缺行应标记 realtime：%#v", data["dataSource"])
	}
	summary := data["summary"].(map[string]any)
	checks := map[string]float64{
		"requestCount":        10,
		"successCount":        9,
		"errorCount":          1,
		"errorRate":           0.1,
		"inputTokens":         300,
		"outputTokens":        150,
		"cacheReadTokens":     30,
		"totalTokens":         450,
		"totalCost":           2,
		"averageDurationMs":   300,
		"averageFirstTokenMs": 114,
	}
	for key, want := range checks {
		if got := summary[key]; got != want {
			t.Fatalf("summary.%s 应为 %v，实际 %#v（全量 %#v）", key, want, got, summary)
		}
	}
}

// TestT93UsageOverviewSummaryPrefersWindowSnapshot 覆盖：窗口行存在时仍读
// 快照（与事实表数值不同），并标记 window。
func TestT93UsageOverviewSummaryPrefersWindowSnapshot(t *testing.T) {
	fixture := newFixture(t)
	t93SeedOverviewDaily(t, fixture, `
		INSERT INTO juhe_stats.usage_stats_daily (system_account_id, scope_type, scope_id, stat_date,
			request_count, success_count, error_count, input_tokens, output_tokens, total_cost_usd,
			duration_ms_sum, duration_ms_count)
		VALUES ('global', 'system_account', 'global', '2026-09-02', 10, 10, 0, 100, 100, 1.0, 1000, 10)`)
	if _, err := fixture.db.Exec(`
		INSERT INTO juhe_stats.usage_overview_summary_windows (system_account_id, window_key, start_date, end_date,
			request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens, total_cost_usd,
			duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count)
		VALUES ('global', '2026-09-02:2026-09-03', '2026-09-02', '2026-09-03',
			999, 900, 99, 5000, 5000, 0, 9.99, 1000, 2, 100, 1)`); err != nil {
		t.Fatalf("seed summary window: %v", err)
	}
	handler := fixture.deps.overviewSectionHandler(false)
	recorder := invoke(t, handler, http.MethodGet,
		"/__aisys__/api/stats/usage-overview/summary?startDate=2026-09-02&endDate=2026-09-03", adminAuth("super_admin"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("summary 应 200：%d %s", recorder.Code, recorder.Body.String())
	}
	data := dataMap(t, decodeBody(t, recorder))
	if data["dataSource"] != usageOverviewDataSourceWindow {
		t.Fatalf("窗口命中应标记 window：%#v", data["dataSource"])
	}
	summary := data["summary"].(map[string]any)
	if summary["requestCount"] != float64(999) || summary["totalCost"] != 9.99 || summary["averageDurationMs"] != float64(500) {
		t.Fatalf("窗口命中应取快照数值：%#v", summary)
	}
}

// TestT93UsageOverviewWindowSectionsPreferSnapshots 覆盖其余三个窗口分节：
// 窗口行存在时 dataSource=window 且数值取窗口，不回退事实表。
func TestT93UsageOverviewWindowSectionsPreferSnapshots(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.db.Exec(`
		INSERT INTO juhe_stats.usage_stats_hourly (system_account_id, scope_type, scope_id, stat_hour,
			request_count, error_count, duration_ms_sum, duration_ms_count)
		VALUES ('global', 'system_account', 'global', '2026-09-04T00', 1, 0, 100, 1);
		INSERT INTO juhe_stats.usage_model_daily (system_account_id, stat_date, provider_code, model,
			request_count, input_tokens, output_tokens, total_cost_usd)
		VALUES ('global', '2026-09-04', 'openai', 'fact-model', 1, 1, 1, 0.1);
		INSERT INTO juhe_stats.usage_error_daily (system_account_id, stat_date, error_group, provider_code,
			error_code, status_code, error_message, error_count)
		VALUES ('global', '2026-09-04', 'openai', 'openai', 'fact_error', 500, 'fact message', 1)`); err != nil {
		t.Fatalf("seed facts: %v", err)
	}
	if _, err := fixture.db.Exec(`
		INSERT INTO juhe_stats.usage_overview_trend_windows (system_account_id, window_key, start_date, end_date,
			bucket_key, request_count, error_count, duration_ms_sum, duration_ms_count)
		VALUES ('global', '2026-09-04:2026-09-04', '2026-09-04', '2026-09-04', '2026-09-04T00', 77, 7, 7000, 7);
		INSERT INTO juhe_stats.usage_model_rank_windows (system_account_id, window_key, start_date, end_date,
			rank, provider_code, model, request_count, input_tokens, output_tokens, total_cost_usd)
		VALUES ('global', '2026-09-04:2026-09-04', '2026-09-04', '2026-09-04', 1, 'window-provider', 'window-model', 88, 800, 400, 8.8);
		INSERT INTO juhe_stats.usage_error_rank_windows (system_account_id, window_key, start_date, end_date,
			rank, provider_code, error_code, status_code, error_message, error_count)
		VALUES ('global', '2026-09-04:2026-09-04', '2026-09-04', '2026-09-04', 1, 'window-provider', 'window_error', 503, 'window message', 9)`); err != nil {
		t.Fatalf("seed windows: %v", err)
	}
	handler := fixture.deps.overviewSectionHandler(false)
	query := "?startDate=2026-09-04&endDate=2026-09-04"

	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/usage-overview/hourly-trend"+query, adminAuth("super_admin"))
	data := dataMap(t, decodeBody(t, recorder))
	if data["dataSource"] != usageOverviewDataSourceWindow {
		t.Fatalf("hourly-trend 窗口命中应标记 window：%#v", data["dataSource"])
	}
	hourly := data["hourlyTrend"].([]any)
	if len(hourly) != 1 || hourly[0].(map[string]any)["requestCount"] != float64(77) {
		t.Fatalf("hourly-trend 应取窗口数值：%#v", hourly)
	}

	recorder = invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/usage-overview/model-distribution"+query, adminAuth("super_admin"))
	data = dataMap(t, decodeBody(t, recorder))
	if data["dataSource"] != usageOverviewDataSourceWindow {
		t.Fatalf("model-distribution 窗口命中应标记 window：%#v", data["dataSource"])
	}
	distribution := data["modelDistribution"].([]any)
	if len(distribution) != 1 || distribution[0].(map[string]any)["model"] != "window-model" {
		t.Fatalf("model-distribution 应取窗口数值：%#v", distribution)
	}

	recorder = invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/usage-overview/errors"+query, adminAuth("super_admin"))
	data = dataMap(t, decodeBody(t, recorder))
	if data["dataSource"] != usageOverviewDataSourceWindow {
		t.Fatalf("errors 窗口命中应标记 window：%#v", data["dataSource"])
	}
	errorsList := data["errors"].([]any)
	if len(errorsList) != 1 || errorsList[0].(map[string]any)["errorCode"] != "window_error" {
		t.Fatalf("errors 应取窗口数值：%#v", errorsList)
	}
}

// TestT93UsageOverviewHourlyTrendFallsBackToRealtime 覆盖小时趋势回退：// 窗口行缺失时按 usage_stats_hourly 实时聚合，并按窗口天数映射 1h / 6h /
// 24h 趋势桶（与 statsagg.TrendBucketKey 一致）。
func TestT93UsageOverviewHourlyTrendFallsBackToRealtime(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.db.Exec(`
		INSERT INTO juhe_stats.usage_stats_hourly (system_account_id, scope_type, scope_id, stat_hour,
			request_count, error_count, duration_ms_sum, duration_ms_count)
		VALUES
			('global', 'system_account', 'global', '2026-09-02T01', 2, 1, 1000, 2),
			('global', 'system_account', 'global', '2026-09-02T07', 3, 0, 3000, 3),
			('global', 'system_account', 'global', '2026-09-03T23', 5, 2, 5000, 5),
			('sys-other', 'system_account', 'sys-other', '2026-09-02T01', 99, 99, 9900, 99)`); err != nil {
		t.Fatalf("seed hourly: %v", err)
	}
	handler := fixture.deps.overviewSectionHandler(false)

	recorder := invoke(t, handler, http.MethodGet,
		"/__aisys__/api/stats/usage-overview/hourly-trend?startDate=2026-09-02&endDate=2026-09-03", adminAuth("super_admin"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("hourly-trend 应 200：%d %s", recorder.Code, recorder.Body.String())
	}
	data := dataMap(t, decodeBody(t, recorder))
	if data["dataSource"] != usageOverviewDataSourceRealtime {
		t.Fatalf("窗口缺行应标记 realtime：%#v", data["dataSource"])
	}
	hourly := data["hourlyTrend"].([]any)
	if len(hourly) != 3 {
		t.Fatalf("2 天区间应按 6 小时桶返回 3 点：%#v", hourly)
	}
	first := hourly[0].(map[string]any)
	second := hourly[1].(map[string]any)
	third := hourly[2].(map[string]any)
	if first["statHour"] != "2026-09-02T00" || first["requestCount"] != float64(2) ||
		first["errorCount"] != float64(1) || first["averageDurationMs"] != float64(500) {
		t.Fatalf("首个 6h 桶错误：%#v", first)
	}
	if second["statHour"] != "2026-09-02T06" || second["requestCount"] != float64(3) || second["averageDurationMs"] != float64(1000) {
		t.Fatalf("第二个 6h 桶错误：%#v", second)
	}
	if third["statHour"] != "2026-09-03T18" || third["requestCount"] != float64(5) || third["errorCount"] != float64(2) {
		t.Fatalf("第三个 6h 桶错误：%#v", third)
	}

	// >3 天区间按自然日聚合：09-02 的 2 个 6h 桶合并为一天。
	recorder = invoke(t, handler, http.MethodGet,
		"/__aisys__/api/stats/usage-overview/hourly-trend?startDate=2026-09-01&endDate=2026-09-04", adminAuth("super_admin"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("4 天 hourly-trend 应 200：%d %s", recorder.Code, recorder.Body.String())
	}
	hourly = dataMap(t, decodeBody(t, recorder))["hourlyTrend"].([]any)
	if len(hourly) != 2 {
		t.Fatalf("4 天区间应按自然日返回 2 点：%#v", hourly)
	}
	dayOne := hourly[0].(map[string]any)
	if dayOne["statHour"] != "2026-09-02" || dayOne["requestCount"] != float64(5) ||
		dayOne["errorCount"] != float64(1) || dayOne["averageDurationMs"] != float64(800) {
		t.Fatalf("自然日桶错误：%#v", dayOne)
	}
}

// TestT93UsageOverviewModelAndErrorRankFallBackToRealtime 覆盖模型分布与错误
// Top 的回退：Top 10 排序、Token/成本求和、错误计数求和与 max(status/message)。
func TestT93UsageOverviewModelAndErrorRankFallBackToRealtime(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.db.Exec(`
		INSERT INTO juhe_stats.usage_model_daily (system_account_id, stat_date, provider_code, model,
			request_count, input_tokens, output_tokens, total_cost_usd)
		VALUES
			('global', '2026-09-02', 'openai', 'gpt-5', 3, 30, 15, 0.3),
			('global', '2026-09-03', 'openai', 'gpt-5', 7, 70, 35, 0.7),
			('global', '2026-09-03', 'anthropic', 'claude', 5, 50, 25, 1.0),
			('sys-other', '2026-09-02', 'openai', 'gpt-5', 100, 100, 100, 10.0)`); err != nil {
		t.Fatalf("seed model daily: %v", err)
	}
	if _, err := fixture.db.Exec(`
		INSERT INTO juhe_stats.usage_error_daily (system_account_id, stat_date, error_group, provider_code,
			error_code, status_code, error_message, error_count)
		VALUES
			('global', '2026-09-02', 'openai', 'openai', 'rate_limit', 429, 'slow down', 2),
			('global', '2026-09-03', 'openai', 'openai', 'rate_limit', 429, 'retry later', 3),
			('global', '2026-09-02', 'openai', 'openai', 'server_error', 500, NULL, 1),
			('sys-other', '2026-09-02', 'openai', 'openai', 'rate_limit', 429, 'other tenant', 99)`); err != nil {
		t.Fatalf("seed error daily: %v", err)
	}
	handler := fixture.deps.overviewSectionHandler(false)

	recorder := invoke(t, handler, http.MethodGet,
		"/__aisys__/api/stats/usage-overview/model-distribution?startDate=2026-09-02&endDate=2026-09-03", adminAuth("super_admin"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("model-distribution 应 200：%d %s", recorder.Code, recorder.Body.String())
	}
	data := dataMap(t, decodeBody(t, recorder))
	if data["dataSource"] != usageOverviewDataSourceRealtime {
		t.Fatalf("窗口缺行应标记 realtime：%#v", data["dataSource"])
	}
	distribution := data["modelDistribution"].([]any)
	if len(distribution) != 2 {
		t.Fatalf("模型分布应 2 行：%#v", distribution)
	}
	top := distribution[0].(map[string]any)
	second := distribution[1].(map[string]any)
	if top["providerCode"] != "openai" || top["model"] != "gpt-5" || top["requestCount"] != float64(10) ||
		top["totalTokens"] != float64(150) || top["totalCost"] != float64(1.0) {
		t.Fatalf("模型 Top1 错误：%#v", top)
	}
	if second["model"] != "claude" || second["requestCount"] != float64(5) || second["totalTokens"] != float64(75) {
		t.Fatalf("模型 Top2 错误：%#v", second)
	}

	recorder = invoke(t, handler, http.MethodGet,
		"/__aisys__/api/stats/usage-overview/errors?startDate=2026-09-02&endDate=2026-09-03", adminAuth("super_admin"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("errors 应 200：%d %s", recorder.Code, recorder.Body.String())
	}
	data = dataMap(t, decodeBody(t, recorder))
	if data["dataSource"] != usageOverviewDataSourceRealtime {
		t.Fatalf("窗口缺行应标记 realtime：%#v", data["dataSource"])
	}
	errorsList := data["errors"].([]any)
	if len(errorsList) != 2 {
		t.Fatalf("错误 Top 应 2 行：%#v", errorsList)
	}
	first := errorsList[0].(map[string]any)
	if first["errorCode"] != "rate_limit" || first["statusCode"] != float64(429) ||
		first["errorMessage"] != "slow down" || first["errorCount"] != float64(5) {
		t.Fatalf("错误 Top1 错误：%#v", first)
	}
	secondError := errorsList[1].(map[string]any)
	if secondError["errorCode"] != "server_error" || secondError["statusCode"] != float64(500) ||
		secondError["errorMessage"] != nil || secondError["errorCount"] != float64(1) {
		t.Fatalf("错误 Top2 错误：%#v", secondError)
	}
}

// TestT93UsageOverviewRealtimeRangeClampedTo31Days 覆盖 31 天边界：
// 超长请求被 normalizeRange 钳制到最近 31 天，回退只聚合钳制后的区间。
func TestT93UsageOverviewRealtimeRangeClampedTo31Days(t *testing.T) {
	fixture := newFixture(t)
	t93SeedOverviewDaily(t, fixture, `
		INSERT INTO juhe_stats.usage_stats_daily (system_account_id, scope_type, scope_id, stat_date,
			request_count, success_count, error_count, total_cost_usd)
		VALUES
			('global', 'system_account', 'global', '2026-08-04', 1000, 1000, 0, 10.0),
			('global', 'system_account', 'global', '2026-08-05', 2, 2, 0, 0.2),
			('global', 'system_account', 'global', '2026-09-04', 3, 3, 0, 0.3)`)
	handler := fixture.deps.overviewSectionHandler(false)
	recorder := invoke(t, handler, http.MethodGet,
		"/__aisys__/api/stats/usage-overview/summary?startDate=2026-07-01&endDate=2026-09-04", adminAuth("super_admin"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("summary 应 200：%d %s", recorder.Code, recorder.Body.String())
	}
	data := dataMap(t, decodeBody(t, recorder))
	rangeData := data["range"].(map[string]any)
	if rangeData["startDate"] != "2026-08-05" || rangeData["endDate"] != "2026-09-04" ||
		rangeData["days"] != float64(31) || rangeData["maxDays"] != float64(31) {
		t.Fatalf("31 天钳制范围错误：%#v", rangeData)
	}
	summary := data["summary"].(map[string]any)
	if summary["requestCount"] != float64(5) || summary["totalCost"] != 0.5 {
		t.Fatalf("回退应只聚合钳制区间：%#v", summary)
	}
	if data["dataSource"] != usageOverviewDataSourceRealtime {
		t.Fatalf("窗口缺行应标记 realtime：%#v", data["dataSource"])
	}
}

// TestT93UsageOverviewRealtimeEmptyDataMarksRealtime 覆盖空数据：窗口行与
// 事实行都不存在时，四个分节返回 0 / 空数组，并保留明确 dataSource。
func TestT93UsageOverviewRealtimeEmptyDataMarksRealtime(t *testing.T) {
	fixture := newFixture(t)
	handler := fixture.deps.overviewSectionHandler(false)
	query := "?startDate=2026-09-01&endDate=2026-09-03"

	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/usage-overview/summary"+query, adminAuth("super_admin"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("summary 应 200：%d %s", recorder.Code, recorder.Body.String())
	}
	data := dataMap(t, decodeBody(t, recorder))
	if data["dataSource"] != usageOverviewDataSourceRealtime {
		t.Fatalf("空数据也应标记来源：%#v", data["dataSource"])
	}
	summary := data["summary"].(map[string]any)
	if summary["requestCount"] != float64(0) || summary["totalTokens"] != float64(0) || summary["averageDurationMs"] != nil {
		t.Fatalf("空数据摘要应全 0：%#v", summary)
	}

	cases := []struct {
		section string
		key     string
	}{
		{"hourly-trend", "hourlyTrend"},
		{"model-distribution", "modelDistribution"},
		{"errors", "errors"},
	}
	for _, tc := range cases {
		recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/usage-overview/"+tc.section+query, adminAuth("super_admin"))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s 应 200：%d %s", tc.section, recorder.Code, recorder.Body.String())
		}
		data := dataMap(t, decodeBody(t, recorder))
		if data["dataSource"] != usageOverviewDataSourceRealtime {
			t.Fatalf("%s 空数据也应标记来源：%#v", tc.section, data["dataSource"])
		}
		if rows, ok := data[tc.key].([]any); !ok || len(rows) != 0 {
			t.Fatalf("%s 空数据应为空数组：%#v", tc.section, data[tc.key])
		}
	}
}

// TestUsageOverviewTrendBucketKeyMirrorsStatsAgg 钉住纯函数分桶边界。
func TestUsageOverviewTrendBucketKeyMirrorsStatsAgg(t *testing.T) {
	cases := []struct {
		statHour string
		days     int
		want     string
	}{
		{"2026-09-02T05", 1, "2026-09-02T05"},
		{"2026-09-02T05", 2, "2026-09-02T00"},
		{"2026-09-02T07", 3, "2026-09-02T06"},
		{"2026-09-02T23", 3, "2026-09-02T18"},
		{"2026-09-02T23", 4, "2026-09-02"},
		{"2026-09-02T23", 31, "2026-09-02"},
	}
	for _, tc := range cases {
		if got := usageOverviewTrendBucketKey(tc.statHour, tc.days); got != tc.want {
			t.Fatalf("usageOverviewTrendBucketKey(%q,%d) = %q，期望 %q", tc.statHour, tc.days, got, tc.want)
		}
	}
}
