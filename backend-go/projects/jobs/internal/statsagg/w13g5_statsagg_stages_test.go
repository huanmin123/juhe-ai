package statsagg

// w13g5_statsagg_stages_test.go 覆盖窗口刷新编排（RunStages/sourceState/
// job state）与系统指标采样、趋势窗口、overview 热窗、配额小时窗的
// 深层 err 传播臂。注入子串均取自各语句唯一片段，保证命中精确。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func w13g5Refresher(env *testEnv) *WindowRefresher {
	return &WindowRefresher{
		DB:      env.db,
		Dialect: env.dialect,
		Clock:   StaticTimezoneSource{env.zone},
		Now:     func() time.Time { return env.now },
	}
}

func w13g5RunStage(t *testing.T, env *testEnv, stage WindowStageName) error {
	t.Helper()
	_, err := w13g5Refresher(env).RunStages(context.Background(), []WindowStageName{stage}, RefreshOptions{})
	return err
}

// ---- InsertSystemMetricsSampleBatch ----

func w13g5SystemSampleInput() SystemMetricsSampleInput {
	return SystemMetricsSampleInput{
		SampledAt: "2026-09-18T07:00:00.000Z", CPUPercent: w13g5f(11), MemoryUsedPercent: w13g5f(55),
		ProcessRssBytes: w13g5f(1024), EventLoopLagMs: w13g5f(3),
		NetworkRxBytesPerSecond: w13g5f(8), NetworkTxBytesPerSecond: w13g5f(9),
		NetworkRxTotalBytes: w13g5f(800), DBFileBytes: w13g5f(4096), StatsLagSeconds: w13g5f(12),
	}
}

func w13g5ProcessSampleInput() ProcessEventLoopSampleInput {
	return ProcessEventLoopSampleInput{
		ProcessRole: "server", ProcessPid: w13g5f(4242), SampledAt: "2026-09-18T07:00:00.000Z",
		EventLoopLagMs: w13g5f(2), ProcessRssBytes: w13g5f(2048), ProcessHeapUsedBytes: w13g5f(512),
		ProcessHeapTotalBytes: w13g5f(1024), ProcessExternalBytes: w13g5f(64), ProcessArrayBuffersBytes: w13g5f(16),
	}
}

func TestW13g5SystemMetricsBatchHappyPath(t *testing.T) {
	env, _ := w13g5StatsOpenFailEnv(t)
	if err := w13g5Refresher(env).InsertSystemMetricsSampleBatch(context.Background(), w13g5SystemSampleInput(), []ProcessEventLoopSampleInput{w13g5ProcessSampleInput()}); err != nil {
		t.Fatal(err)
	}
}

func TestW13g5SystemMetricsBatchClockAndBeginArms(t *testing.T) {
	env, _ := w13g5StatsOpenFailEnv(t)
	refresher := w13g5Refresher(env)
	refresher.Clock = w13g5StatsErrClock{}
	if err := refresher.InsertSystemMetricsSampleBatch(context.Background(), w13g5SystemSampleInput(), nil); err == nil {
		t.Fatal("时钟失败必须传播")
	}
	env2, spec := w13g5StatsOpenFailEnv(t)
	spec.arm("w13g5-BEGIN")
	defer spec.disarm()
	if err := w13g5Refresher(env2).InsertSystemMetricsSampleBatch(context.Background(), w13g5SystemSampleInput(), nil); err == nil {
		t.Fatal("BeginTx 失败必须传播")
	}
}

func TestW13g5SystemMetricsBatchStatementErrorArms(t *testing.T) {
	cases := []struct {
		name  string
		match string
	}{
		{"sample insert", "INSERT INTO system_metrics_samples"},
		{"hourly upsert", "INSERT INTO system_metrics_hourly"},
		{"process sample insert", "INSERT INTO process_event_loop_samples"},
		{"process hourly upsert", "INSERT INTO process_event_loop_hourly"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, spec := w13g5StatsOpenFailEnv(t)
			spec.armOnce(tc.match)
			defer spec.disarm()
			if err := w13g5Refresher(env).InsertSystemMetricsSampleBatch(context.Background(), w13g5SystemSampleInput(), []ProcessEventLoopSampleInput{w13g5ProcessSampleInput()}); err == nil {
				t.Fatalf("%s 失败必须传播", tc.name)
			}
		})
	}
}

func TestW13g5SystemMetricsSampledAtArms(t *testing.T) {
	env, _ := w13g5StatsOpenFailEnv(t)
	// 空 sampledAt 走 now 分支。
	input := w13g5SystemSampleInput()
	input.SampledAt = ""
	if err := w13g5Refresher(env).InsertSystemMetricsSampleBatch(context.Background(), input, nil); err != nil {
		t.Fatal(err)
	}
	// 非法 sampledAt 报错。
	env2, _ := w13g5StatsOpenFailEnv(t)
	bad := w13g5SystemSampleInput()
	bad.SampledAt = "w13g5-not-time"
	if err := w13g5Refresher(env2).InsertSystemMetricsSampleBatch(context.Background(), bad, nil); err == nil {
		t.Fatal("非法 sampledAt 必须报错")
	}
	// 进程样本空/非法 sampledAt。
	env3, _ := w13g5StatsOpenFailEnv(t)
	emptyProcess := w13g5ProcessSampleInput()
	emptyProcess.SampledAt = ""
	if err := w13g5Refresher(env3).InsertSystemMetricsSampleBatch(context.Background(), w13g5SystemSampleInput(), []ProcessEventLoopSampleInput{emptyProcess}); err != nil {
		t.Fatal(err)
	}
	env4, _ := w13g5StatsOpenFailEnv(t)
	badProcess := w13g5ProcessSampleInput()
	badProcess.SampledAt = "w13g5-not-time"
	if err := w13g5Refresher(env4).InsertSystemMetricsSampleBatch(context.Background(), w13g5SystemSampleInput(), []ProcessEventLoopSampleInput{badProcess}); err == nil {
		t.Fatal("进程样本非法 sampledAt 必须报错")
	}
	// 全空进程样本被丢弃。
	env5, _ := w13g5StatsOpenFailEnv(t)
	dropped := ProcessEventLoopSampleInput{ProcessRole: "server", SampledAt: "2026-09-18T07:00:00.000Z"}
	if err := w13g5Refresher(env5).InsertSystemMetricsSampleBatch(context.Background(), w13g5SystemSampleInput(), []ProcessEventLoopSampleInput{dropped}); err != nil {
		t.Fatal(err)
	}
}

// ---- trend windows stage ----

func w13g5SeedSystemHourly(t *testing.T, env *testEnv, statHour, updatedAt string, cpuSum float64) {
	t.Helper()
	env.exec(`INSERT INTO system_metrics_hourly (
		stat_hour, sample_count, cpu_percent_sum, cpu_percent_max, memory_used_percent_sum,
		network_rx_total_bytes_max, db_file_bytes_max, stats_lag_seconds_max, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		statHour, 2, cpuSum, w13g5f(30), 120, w13g5f(900), w13g5f(4096), w13g5f(15), updatedAt)
}

func w13g5SeedProcessHourly(t *testing.T, env *testEnv, statHour, role, updatedAt string) {
	t.Helper()
	env.exec(`INSERT INTO process_event_loop_hourly (
		stat_hour, process_role, sample_count, event_loop_lag_ms_sum, event_loop_lag_ms_count,
		process_rss_bytes_sum, process_heap_used_bytes_sum, process_heap_total_bytes_sum,
		process_external_bytes_sum, process_array_buffers_bytes_sum, updated_at
	) VALUES (?, ?, 2, 4, 2, 2048, 512, 1024, 64, 16, ?)`,
		statHour, role, updatedAt)
}

func TestW13g5SystemTrendStageHappyPath(t *testing.T) {
	env, _ := w13g5StatsOpenFailEnv(t)
	w13g5SeedSystemHourly(t, env, "2026-09-18T00", "2026-09-18T01:00:00.000Z", 20)
	w13g5SeedProcessHourly(t, env, "2026-09-18T00", "server", "2026-09-18T01:00:00.000Z")
	if err := w13g5RunStage(t, env, StageSystemMetricsTrendWindows); err != nil {
		t.Fatal(err)
	}
}

func TestW13g5SystemTrendStageStatementArms(t *testing.T) {
	cases := []struct {
		name     string
		match    string
		skipFrom int
	}{
		{"delete system trend", "DELETE FROM system_metrics_trend_windows", 0},
		{"delete process trend", "DELETE FROM process_event_loop_trend_windows", 0},
		{"system refresh select", "FROM system_metrics_hourly", 2},
		{"process refresh select", "ORDER BY stat_hour ASC, process_role ASC", 0},
		{"system trend insert", "INSERT INTO system_metrics_trend_windows", 0},
		{"process trend insert", "INSERT INTO process_event_loop_trend_windows", 0},
		{"source version system", "SELECT * FROM system_metrics_hourly", 0},
		{"source version process", "SELECT * FROM process_event_loop_hourly", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, spec := w13g5StatsOpenFailEnv(t)
			w13g5SeedSystemHourly(t, env, "2026-09-18T00", "2026-09-18T01:00:00.000Z", 20)
			w13g5SeedProcessHourly(t, env, "2026-09-18T00", "server", "2026-09-18T01:00:00.000Z")
			if tc.skipFrom > 0 {
				spec.armAfter(tc.match, tc.skipFrom)
			} else {
				spec.armOnce(tc.match)
			}
			defer spec.disarm()
			if err := w13g5RunStage(t, env, StageSystemMetricsTrendWindows); err == nil {
				t.Fatalf("%s 失败必须传播", tc.name)
			}
		})
	}
}

func TestW13g5SystemTrendSourceVersionBadUpdatedAt(t *testing.T) {
	env, _ := w13g5StatsOpenFailEnv(t)
	w13g5SeedSystemHourly(t, env, "2026-09-18T00", "w13g5-not-time", 20)
	if _, err := systemMetricsTrendSourceVersion(context.Background(), env.db, env.dialect); err == nil {
		t.Fatal("非法 updated_at 必须报错")
	}
}

func TestW13g5SystemTrendScanArms(t *testing.T) {
	env, _ := w13g5StatsOpenFailEnv(t)
	// 先播种一行，保证 rows.Next() 迭代到 Scan 列数不匹配错误。
	env.exec(`INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, status_code, success, created_at)
		VALUES ('w13g5-scan-row', 'w13g5-sa', 'w13g5-tr', 'gateway', 200, 1, '2026-09-18T07:00:00.000Z')`)
	rows, err := env.db.Query(`SELECT * FROM usage_records`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if _, err := scanSystemMetricsHourlyRows(rows, nil); err == nil {
		t.Fatal("列数不匹配必须报错")
	}
}

func TestW13g5SystemTrendScanScanErrorArms(t *testing.T) {
	env, _ := w13g5StatsOpenFailEnv(t)
	// cpu_percent_sum 写入非数值文本使 Scan 失败。
	env.exec(`INSERT INTO system_metrics_hourly (stat_hour, sample_count, cpu_percent_sum, updated_at) VALUES ('2026-09-18T00', 1, 'w13g5-bad', '2026-09-18T01:00:00.000Z')`)
	if err := w13g5RunStage(t, env, StageSystemMetricsTrendWindows); err == nil {
		t.Fatal("系统指标扫描失败必须传播")
	}
	env2, _ := w13g5StatsOpenFailEnv(t)
	env2.exec(`INSERT INTO process_event_loop_hourly (stat_hour, process_role, sample_count, updated_at) VALUES ('2026-09-18T00', 'server', 'w13g5-bad', '2026-09-18T01:00:00.000Z')`)
	if err := w13g5RunStage(t, env2, StageSystemMetricsTrendWindows); err == nil {
		t.Fatal("进程指标扫描失败必须传播")
	}
}

// ---- overview / hot windows stage ----

func w13g5SeedOverviewData(t *testing.T, env *testEnv) {
	t.Helper()
	env.exec(`INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, updated_at)
		VALUES ('w13g5-sa', 'system_account', 'w13g5-sa', 3, '2026-09-18T01:00:00.000Z')`)
	env.exec(`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, duration_ms_max, first_token_ms_max, last_used_at, updated_at)
		VALUES ('w13g5-sa', 'system_account', 'w13g5-sa', '2026-09-18', 3, 100, 50, '2026-09-18T01:00:00.000Z', '2026-09-18T01:00:00.000Z')`)
	env.exec(`INSERT INTO usage_stats_hourly (system_account_id, scope_type, scope_id, stat_hour, request_count, updated_at)
		VALUES ('w13g5-sa', 'system_account', 'w13g5-sa', '2026-09-18T00', 2, '2026-09-18T01:00:00.000Z')`)
	env.exec(`INSERT INTO usage_model_daily (system_account_id, stat_date, provider_code, model, request_count, updated_at)
		VALUES ('w13g5-sa', '2026-09-18', 'openai', 'gpt-5', 2, '2026-09-18T01:00:00.000Z')`)
	env.exec(`INSERT INTO usage_error_daily (system_account_id, stat_date, error_group, provider_code, error_code, status_code, error_count, updated_at)
		VALUES ('w13g5-sa', '2026-09-18', 'openai', 'openai', 'rate_limited', 429, 1, '2026-09-18T01:00:00.000Z')`)
}

func TestW13g5OverviewStageHappyPath(t *testing.T) {
	env, _ := w13g5StatsOpenFailEnv(t)
	w13g5SeedOverviewData(t, env)
	if err := w13g5RunStage(t, env, StageUsageOverviewWindows); err != nil {
		t.Fatal(err)
	}
	if err := w13g5RunStage(t, env, StageAiPerformanceSummaryWindows); err != nil {
		t.Fatal(err)
	}
	if err := w13g5RunStage(t, env, StageUsageScopeRangeWindows); err != nil {
		t.Fatal(err)
	}
	if err := w13g5RunStage(t, env, StageAuthorizationUsageRangeWindows); err != nil {
		t.Fatal(err)
	}
}

func TestW13g5OverviewStageStatementArms(t *testing.T) {
	cases := []struct {
		name  string
		match string
		stage WindowStageName
	}{
		{"scopes select", "ORDER BY updated_at DESC, system_account_id ASC, scope_id ASC", StageUsageOverviewWindows},
		{"source state select", "SELECT updated_at FROM usage_stats_totals", StageUsageOverviewWindows},
		{"delete summary", "DELETE FROM usage_overview_summary_windows", StageUsageOverviewWindows},
		{"daily load", "AND stat_date >= ?", StageUsageOverviewWindows},
		{"summary insert", "INSERT INTO usage_overview_summary_windows", StageUsageOverviewWindows},
		{"hourly load", "FROM usage_stats_hourly\n\t\tWHERE system_account_id = ?", StageUsageOverviewWindows},
		{"trend insert", "INSERT INTO usage_overview_trend_windows", StageUsageOverviewWindows},
		{"model load", "FROM usage_model_daily", StageUsageOverviewWindows},
		{"model rank insert", "INSERT INTO usage_model_rank_windows", StageUsageOverviewWindows},
		{"error load", "FROM usage_error_daily", StageUsageOverviewWindows},
		{"error rank insert", "INSERT INTO usage_error_rank_windows", StageUsageOverviewWindows},
		{"ai delete", "DELETE FROM ai_performance_summary_windows", StageAiPerformanceSummaryWindows},
		{"ai load", "scope_type = 'account'", StageAiPerformanceSummaryWindows},
		{"ai insert", "INSERT INTO ai_performance_summary_windows", StageAiPerformanceSummaryWindows},
		{"scope range delete", "DELETE FROM usage_scope_range_windows", StageUsageScopeRangeWindows},
		{"scope range insert", "INSERT INTO usage_scope_range_windows", StageUsageScopeRangeWindows},
		{"auth range delete", "DELETE FROM authorization_team_usage_range_windows", StageAuthorizationUsageRangeWindows},
		{"auth team insert", "INSERT INTO authorization_team_usage_range_windows", StageAuthorizationUsageRangeWindows},
		{"auth user insert", "INSERT INTO authorization_user_usage_range_windows", StageAuthorizationUsageRangeWindows},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, spec := w13g5StatsOpenFailEnv(t)
			w13g5SeedOverviewData(t, env)
			env.exec(`INSERT INTO authorization_team_usage_summary_daily (system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id, request_count, updated_at)
				VALUES ('w13g5-sa', '2026-09-18', 'w13g5-team', 'account', 'w13g5-acc', 2, '2026-09-18T01:00:00.000Z')`)
			env.exec(`INSERT INTO authorization_user_usage_summary_daily (system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id, request_count, updated_at)
				VALUES ('w13g5-sa', '2026-09-18', '', 'w13g5-grantee', 'account', 'w13g5-acc', 2, '2026-09-18T01:00:00.000Z')`)
			spec.armOnce(tc.match)
			defer spec.disarm()
			if err := w13g5RunStage(t, env, tc.stage); err == nil {
				t.Fatalf("%s 失败必须传播", tc.name)
			}
		})
	}
}

// ---- RunStages 编排与 job state ----

func TestW13g5RunStagesClockErrorArm(t *testing.T) {
	env, _ := w13g5StatsOpenFailEnv(t)
	refresher := w13g5Refresher(env)
	refresher.Clock = w13g5StatsErrClock{}
	if _, err := refresher.RunStages(context.Background(), nil, RefreshOptions{}); err == nil {
		t.Fatal("时钟失败必须传播")
	}
}

func TestW13g5RunStagesStageErrorPropagationArm(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	w13g5SeedOverviewData(t, env)
	spec.armOnce("DELETE FROM usage_overview_summary_windows")
	defer spec.disarm()
	if _, err := w13g5Refresher(env).RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows}, RefreshOptions{}); err == nil {
		t.Fatal("stage 失败必须传播")
	}
}

func TestW13g5RunStagesJobStateWriteErrorArm(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	w13g5SeedOverviewData(t, env)
	spec.armOnce("VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?)")
	defer spec.disarm()
	if _, err := w13g5Refresher(env).RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows}, RefreshOptions{SkipIfUnchanged: true, JobName: "w13g5-job"}); err == nil {
		t.Fatal("job state 写入失败必须传播")
	}
}

func TestW13g5RunStagesSourceVersionArms(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	w13g5SeedSystemHourly(t, env, "2026-09-18T00", "2026-09-18T01:00:00.000Z", 20)
	w13g5SeedProcessHourly(t, env, "2026-09-18T00", "server", "2026-09-18T01:00:00.000Z")
	// 主状态 upsert 与 sourceVersion 行 upsert 共用同一语句文本，跳过首命中
	// 让主状态写入成功、sourceVersion 行写入失败（539-541 错误臂）。
	spec.armAfter("cursor_created_at = excluded.cursor_created_at", 1)
	if _, err := w13g5Refresher(env).RunStages(context.Background(), []WindowStageName{StageSystemMetricsTrendWindows}, RefreshOptions{SkipIfUnchanged: true}); err == nil {
		t.Fatal("sourceVersion 行写入失败必须传播")
	}
	spec.disarm()
	// 注入解除后完整写入双行状态。
	if _, err := w13g5Refresher(env).RunStages(context.Background(), []WindowStageName{StageSystemMetricsTrendWindows}, RefreshOptions{SkipIfUnchanged: true}); err != nil {
		t.Fatal(err)
	}
	// 二次运行 watermark+version 均未变化 → skipped。
	result, err := w13g5Refresher(env).RunStages(context.Background(), []WindowStageName{StageSystemMetricsTrendWindows}, RefreshOptions{SkipIfUnchanged: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped {
		t.Fatalf("未变化必须跳过: %+v", result)
	}
}

func TestW13g5RankRefreshJobStateBadWatermarkArms(t *testing.T) {
	env, _ := w13g5StatsOpenFailEnv(t)
	ctx := context.Background()
	refresher := w13g5Refresher(env)
	env.exec(`INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
		VALUES ('global', '', 'w13g5-job', 'w13g5-bad-time', 'w13g5-refresh', '2026-09-18T00:00:00.000Z')`)
	if _, err := refresher.rankRefreshJobState(ctx, "w13g5-job", false); err == nil {
		t.Fatal("非 legacy 模式坏 watermark 必须报错")
	}
	if _, err := refresher.rankRefreshJobState(ctx, "w13g5-job", true); err == nil {
		t.Fatal("legacy 模式坏 watermark 必须报错")
	}
	env.exec(`UPDATE stats_job_state SET cursor_created_at = 'w13g5-bad|v2:zz' WHERE job_name = 'w13g5-job'`)
	if _, err := refresher.rankRefreshJobState(ctx, "w13g5-job", true); err == nil {
		t.Fatal("legacy 管道格式坏 watermark 必须报错")
	}
}

func w13g5SeedVersionState(t *testing.T, env *testEnv, jobName, watermark, version string) {
	t.Helper()
	env.exec(`INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
		VALUES ('usage_rank_snapshot_source_version', '', ?, ?, ?, '2026-09-18T00:00:00.000Z')`, jobName, watermark, version)
}

func TestW13g5PreviousSourceVersionArms(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	ctx := context.Background()
	refresher := w13g5Refresher(env)
	// 查询执行错误臂。
	spec.armOnce("SELECT cursor_created_at, cursor_id FROM stats_job_state")
	if _, err := refresher.previousSourceVersion(ctx, "w13g5-job", nil); err == nil {
		t.Fatal("查询失败必须传播")
	}
	spec.disarm()
	// version 行存在但 watermark 缺失。
	w13g5SeedVersionState(t, env, "w13g5-job", "", "v2:ab")
	if _, err := refresher.previousSourceVersion(ctx, "w13g5-job", &rankRefreshJobState{cursorCreatedAt: w13g5s("2026-09-18T00:00:00.000Z")}); err == nil {
		t.Fatal("缺 watermark 必须报错")
	}
	// version 行存在但 version 缺失。
	env.exec(`DELETE FROM stats_job_state WHERE job_name = 'w13g5-job'`)
	w13g5SeedVersionState(t, env, "w13g5-job", "2026-09-18T00:00:00.000Z", "")
	if _, err := refresher.previousSourceVersion(ctx, "w13g5-job", &rankRefreshJobState{cursorCreatedAt: w13g5s("2026-09-18T00:00:00.000Z")}); err == nil {
		t.Fatal("缺 version 必须报错")
	}
	// version 行存在但主状态缺失。
	if _, err := refresher.previousSourceVersion(ctx, "w13g5-job", nil); err == nil {
		t.Fatal("主状态缺失必须报错")
	}
	// 主状态 cursor 为空。
	if _, err := refresher.previousSourceVersion(ctx, "w13g5-job", &rankRefreshJobState{}); err == nil {
		t.Fatal("主状态缺 watermark 必须报错")
	}
	// legacy sourceVersion：version 行缺失 → 返回 legacy 值；不匹配 → 报错。
	legacy := &rankRefreshJobState{cursorCreatedAt: w13g5s("2026-09-18T00:00:00.000Z"), legacySourceVersion: "v2:legacy"}
	if got, err := refresher.previousSourceVersion(ctx, "w13g5-missing-job", legacy); err != nil || got != "v2:legacy" {
		t.Fatalf("legacy 直返失败: %v %v", got, err)
	}
	env.exec(`DELETE FROM stats_job_state WHERE job_name = 'w13g5-job'`)
	w13g5SeedVersionState(t, env, "w13g5-job", "2026-09-17T00:00:00.000Z", "v2:other")
	if _, err := refresher.previousSourceVersion(ctx, "w13g5-job", legacy); err == nil {
		t.Fatal("legacy 与 version 行不一致必须报错")
	}
	// 非 legacy：version 行缺失 / watermark 不一致 / 一致返回。
	if _, err := refresher.previousSourceVersion(ctx, "w13g5-missing-job", &rankRefreshJobState{cursorCreatedAt: w13g5s("2026-09-18T00:00:00.000Z")}); err == nil {
		t.Fatal("version 行缺失必须报错")
	}
	if _, err := refresher.previousSourceVersion(ctx, "w13g5-job", &rankRefreshJobState{cursorCreatedAt: w13g5s("2026-09-18T00:00:00.000Z")}); err == nil {
		t.Fatal("watermark 不一致必须报错")
	}
	if got, err := refresher.previousSourceVersion(ctx, "w13g5-job", &rankRefreshJobState{cursorCreatedAt: w13g5s("2026-09-17T00:00:00.000Z")}); err != nil || got != "v2:other" {
		t.Fatalf("一致时必须返回 version: %v %v", got, err)
	}
}

func TestW13g5UpdateRankRefreshJobStateValidationArms(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	ctx := context.Background()
	refresher := w13g5Refresher(env)
	if err := refresher.updateRankRefreshJobState(ctx, "w13g5-job", rankRefreshStateInput{SourceVersion: "v2:ZZZ"}); err == nil {
		t.Fatal("空 watermark 必须报错")
	}
	if err := refresher.updateRankRefreshJobState(ctx, "w13g5-job", rankRefreshStateInput{
		SourceWatermark: "2026-09-18T00:00:00.000Z", SourceVersion: "v2:bad--",
	}); err == nil {
		t.Fatal("非法 sourceVersion 必须报错")
	}
	if err := refresher.updateRankRefreshJobState(ctx, "w13g5-job", rankRefreshStateInput{
		SourceWatermark: "2026-09-18T00:00:00.000Z", LastSuccessAt: "w13g5-bad",
	}); err == nil {
		t.Fatal("非法 lastSuccessAt 必须报错")
	}
	spec.arm("w13g5-BEGIN")
	defer spec.disarm()
	if err := refresher.updateRankRefreshJobState(ctx, "w13g5-job", rankRefreshStateInput{
		SourceWatermark: "2026-09-18T00:00:00.000Z", LastSuccessAt: "2026-09-18T00:00:00.000Z",
	}); err == nil {
		t.Fatal("BeginTx 失败必须传播")
	}
}

// ---- quota hourly windows ----

func w13g5QuotaBusinessDB(t *testing.T) *sql.DB {
	t.Helper()
	return w13g5StatsOpenFailDB(t, filepath.Join(t.TempDir(), "w13g5-business.sqlite3"))
}

func TestW13g5QuotaRebuildStatementArms(t *testing.T) {
	cases := []struct {
		name  string
		match string
	}{
		{"bindings read", "ORDER BY window_hours ASC, system_account_id ASC, scope_type ASC, scope_id ASC"},
		{"delete windows", "DELETE FROM usage_quota_hourly_windows"},
		{"rebuild insert", "INSERT INTO usage_quota_hourly_windows"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, spec := w13g5StatsOpenFailEnv(t)
			business := w13g5QuotaBusinessDB(t)
			createBindingTable(t, business)
			if err := seedBinding(business, "w13g5-sa", "api_key", "w13g5-key", 1); err != nil {
				t.Fatal(err)
			}
			refresher := w13g5Refresher(env)
			refresher.BusinessDB = business
			spec.armOnce(tc.match)
			defer spec.disarm()
			if _, err := refresher.RunQuotaHourlyWindows(context.Background()); err == nil {
				t.Fatalf("%s 失败必须传播", tc.name)
			}
		})
	}
}

func TestW13g5QuotaRebuildHappyPathAndCommitError(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	business := w13g5QuotaBusinessDB(t)
	createBindingTable(t, business)
	if err := seedBinding(business, "w13g5-sa", "api_key", "w13g5-key", 1); err != nil {
		t.Fatal(err)
	}
	env.seedQuotaHourly("w13g5-sa", "api_key", "w13g5-key", "2026-09-18T07", 1.5)
	refresher := w13g5Refresher(env)
	refresher.BusinessDB = business
	result, err := refresher.RunQuotaHourlyWindows(context.Background())
	if err != nil || !result.Changed {
		t.Fatalf("重建必须成功: %+v %v", result, err)
	}
	spec.armOnce("w13g5-COMMIT")
	defer spec.disarm()
	if _, err := refresher.RunQuotaHourlyWindows(context.Background()); err == nil {
		t.Fatal("提交失败必须传播")
	}
}

func TestW13g5QuotaRecordJobStateArms(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	business := w13g5QuotaBusinessDB(t)
	refresher := w13g5Refresher(env)
	refresher.BusinessDB = business
	// 失败路径：listBindings 失败（镜像表缺失）→ record 写 last_error_message。
	if _, err := refresher.RunQuotaHourlyWindows(context.Background()); err == nil {
		t.Fatal("缺绑定表必须失败")
	}
	// record 的 exec 失败臂：注入 job state upsert。
	spec.armOnce("VALUES (?, ?, ?, NULL, NULL, ?, ?, NULL, ?)")
	defer spec.disarm()
	_, _ = refresher.RunQuotaHourlyWindows(context.Background())
}

func w13g5SeedQuotaIncrementalFixture(t *testing.T, env *testEnv, spec *w13g5StatsSpec) *WindowRefresher {
	t.Helper()
	createBindingTable(t, env.db)
	if err := seedBinding(env.db, "w13g5-sa", "api_key", "w13g5-key", 1); err != nil {
		t.Fatal(err)
	}
	env.exec(`INSERT INTO usage_quota_hourly_window_dirty_scopes (system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at)
		VALUES ('w13g5-sa', 'api_key', 'w13g5-key', 1, '2026-09-18T07:00:00.000Z', '2026-09-18T07:00:00.000Z')`)
	env.seedQuotaHourly("w13g5-sa", "api_key", "w13g5-key", "2026-09-18T07", 2.5)
	return w13g5Refresher(env)
}

func TestW13g5QuotaIncrementalSQLiteHappyPath(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	refresher := w13g5SeedQuotaIncrementalFixture(t, env, spec)
	result, err := refresher.RunQuotaHourlyWindows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// SQLite 分支固定 rebuild 语义；增量编排直接调用验证。
	result2 := QuotaHourlyWindowRefreshResult{}
	if err := refresher.refreshQuotaHourlyWindowsIncremental(context.Background(), env.zone, &result2); err != nil {
		t.Fatal(err)
	}
	if !result2.Changed {
		t.Fatal("脏 scope 必须产生变更")
	}
	_ = result
}

func TestW13g5QuotaIncrementalStatementArms(t *testing.T) {
	cases := []struct {
		name  string
		match string
	}{
		{"expiry read", "SELECT cursor_created_at, cursor_id FROM stats_job_state"},
		{"claim read", "LEFT JOIN request_quota_hourly_window_scope_bindings bindings"},
		{"delete for scopes", "DELETE FROM usage_quota_hourly_windows"},
		{"rebuild for scopes", "INSERT INTO usage_quota_hourly_windows"},
		{"delete consumed", "DELETE FROM usage_quota_hourly_window_dirty_scopes"},
		{"active hours", "SELECT DISTINCT window_hours"},
		{"expiry dirty insert", "INSERT INTO usage_quota_hourly_window_dirty_scopes"},
		{"expiry cursor upsert", "cursor_created_at = excluded.cursor_created_at"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, spec := w13g5StatsOpenFailEnv(t)
			refresher := w13g5SeedQuotaIncrementalFixture(t, env, spec)
			spec.armOnce(tc.match)
			defer spec.disarm()
			result := QuotaHourlyWindowRefreshResult{}
			if err := refresher.refreshQuotaHourlyWindowsIncremental(context.Background(), env.zone, &result); err == nil {
				t.Fatalf("%s 失败必须传播", tc.name)
			}
		})
	}
}

func TestW13g5QuotaIncrementalBadExpiryCursor(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	refresher := w13g5SeedQuotaIncrementalFixture(t, env, spec)
	env.exec(`INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
		VALUES ('global', '', 'usage_quota_hourly_windows_expiry', 'w13g5-not-time', '2026-09-18T07', '2026-09-18T07:00:00.000Z')`)
	result := QuotaHourlyWindowRefreshResult{}
	if err := refresher.refreshQuotaHourlyWindowsIncremental(context.Background(), env.zone, &result); err == nil {
		t.Fatal("非法 expiry 游标必须报错")
	}
}

func TestW13g5QuotaIncrementalEmptyDirtyScope(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	refresher := w13g5SeedQuotaIncrementalFixture(t, env, spec)
	env.exec(`DELETE FROM usage_quota_hourly_window_dirty_scopes`)
	result := QuotaHourlyWindowRefreshResult{}
	if err := refresher.refreshQuotaHourlyWindowsIncremental(context.Background(), env.zone, &result); err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatal("空脏范围不得产生变更")
	}
	// 无绑定镜像（脏 scope 无绑定）→ rebuild 空活跃集直接返回。
	env2, spec2 := w13g5StatsOpenFailEnv(t)
	createBindingTable(t, env2.db)
	refresher2 := w13g5Refresher(env2)
	env2.exec(`INSERT INTO usage_quota_hourly_window_dirty_scopes (system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at)
		VALUES ('w13g5-sa', 'api_key', 'w13g5-orphan', 1, '2026-09-18T07:00:00.000Z', '2026-09-18T07:00:00.000Z')`)
	_ = spec2
	result3 := QuotaHourlyWindowRefreshResult{}
	if err := refresher2.refreshQuotaHourlyWindowsIncremental(context.Background(), env2.zone, &result3); err != nil {
		t.Fatal(err)
	}
}


