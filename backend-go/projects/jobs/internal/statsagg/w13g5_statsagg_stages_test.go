package statsagg

// w13g5_statsagg_stages_test.go 覆盖窗口刷新编排（RunStages/sourceState/
// job state）、overview 热窗与配额小时窗的深层 err 传播臂。注入子串均取
// 自各语句唯一片段，保证命中精确。

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
	// StageAuthorizationUsageRangeWindows 已停用（授权明细直读日摘要表），
	// 不再注册进编排。
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, spec := w13g5StatsOpenFailEnv(t)
			w13g5SeedOverviewData(t, env)
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

func TestW13g5RankRefreshJobStateBadWatermarkArms(t *testing.T) {
	env, _ := w13g5StatsOpenFailEnv(t)
	ctx := context.Background()
	refresher := w13g5Refresher(env)
	env.exec(`INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
		VALUES ('global', '', 'w13g5-job', 'w13g5-bad-time', 'w13g5-refresh', '2026-09-18T00:00:00.000Z')`)
	if _, err := refresher.rankRefreshJobState(ctx, "w13g5-job"); err == nil {
		t.Fatal("坏 watermark 必须报错")
	}
}

func TestW13g5UpdateRankRefreshJobStateValidationArms(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	ctx := context.Background()
	refresher := w13g5Refresher(env)
	if err := refresher.updateRankRefreshJobState(ctx, "w13g5-job", rankRefreshStateInput{}); err == nil {
		t.Fatal("空 watermark 必须报错")
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
