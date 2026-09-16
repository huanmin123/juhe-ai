package statsagg

import (
	"context"
	"testing"
)

// w10c_statsagg_units2_test.go 覆盖统计聚合域第二轮剩余臂：
// 错误/模型窗口聚合纯函数、聚合游标 lag、派生窗口脏标记冲突更新臂、
// 授权查找 JOIN、系统指标采样批次与趋势窗口 stage 全链、配额重建句柄分派。

// ---- 错误/模型窗口聚合纯函数 ----

func TestW10CAggregateErrorAndModelRowsPure(t *testing.T) {
	rangeValue := StatsRange{StartDate: "2026-04-18", EndDate: "2026-04-18", Days: 1, MaxDays: FixedRangeWindowDays}

	// aggregateUsageErrorRows：unknown 归一、status code 取大、error message
	// 字典序取大、排序与窗口外过滤。
	rowsByDate := map[string][]usageErrorWindowRow{
		"2026-04-18": {
			{StatDate: "2026-04-18", ErrorGroup: "", ProviderCode: "", ErrorCode: "", StatusCode: 429, ErrorMessage: "b", ErrorCount: 2},
			{StatDate: "2026-04-18", ErrorGroup: "openai", ProviderCode: "openai", ErrorCode: "e1", StatusCode: 500, ErrorMessage: "a", ErrorCount: 1},
			{StatDate: "2026-04-18", ErrorGroup: "openai", ProviderCode: "openai", ErrorCode: "e1", StatusCode: 400, ErrorMessage: "a", ErrorCount: 1},
		},
		"2026-04-01": {
			{StatDate: "2026-04-01", ErrorGroup: "outside", ProviderCode: "x", ErrorCode: "y", StatusCode: 500, ErrorCount: 99},
		},
	}
	ranked := aggregateUsageErrorRows(rowsByDate, rangeValue)
	if len(ranked) != 2 {
		t.Fatalf("窗口外行应过滤: %v", ranked)
	}
	// 排序按 error_count DESC：openai/e1（count 2）在前，unknown（count 2）……
	// unknown 也是 2 → 按 provider_code 字典序 openai 在前。
	if ranked[0].ProviderCode != "openai" || ranked[0].ErrorCode != "e1" || ranked[0].ErrorCount != 2 {
		t.Fatalf("排序错误: %+v", ranked)
	}
	if ranked[0].StatusCode != 500 || ranked[0].ErrorMessage != "a" {
		t.Fatalf("status code / message 取大错误: %+v", ranked[0])
	}
	if ranked[1].ErrorGroup != "unknown" || ranked[1].ProviderCode != "unknown" || ranked[1].ErrorCode != "unknown" {
		t.Fatalf("unknown 归一错误: %+v", ranked[1])
	}

	// aggregateUsageModelRows：unknown 归一、累加、排序、top-10 截断。
	modelRows := map[string][]usageModelWindowRow{
		"2026-04-18": {
			{StatDate: "2026-04-18", ProviderCode: "", Model: "m1", RequestCount: 3, InputTokens: 10},
			{StatDate: "2026-04-18", ProviderCode: "openai", Model: "m2", RequestCount: 5},
		},
	}
	aggregates := aggregateUsageModelRows(modelRows, rangeValue)
	if len(aggregates) != 2 || aggregates[0].ProviderCode != "openai" || aggregates[0].RequestCount != 5 {
		t.Fatalf("模型聚合排序错误: %+v", aggregates)
	}
	if aggregates[1].ProviderCode != "unknown" || aggregates[1].Model != "m1" || aggregates[1].InputTokens != 10 {
		t.Fatalf("unknown 模型归一错误: %+v", aggregates[1])
	}
	// 超 10 行：纯函数返回全部行（top-10 截断在 refreshUsageModelRankWindows
	// 调用方执行，由 stage 全链覆盖）。
	many := map[string][]usageModelWindowRow{}
	for index := 0; index < 15; index++ {
		many["2026-04-18"] = append(many["2026-04-18"], usageModelWindowRow{
			StatDate: "2026-04-18", ProviderCode: "p", Model: "m" + string(rune('a'+index)), RequestCount: float64(index + 1),
		})
	}
	if got := aggregateUsageModelRows(many, rangeValue); len(got) != 15 || got[0].RequestCount != 15 {
		t.Fatalf("模型聚合行数错误: %d", len(got))
	}
}

// ---- 聚合游标 lag（空批次 + 最新记录）----

func TestW10CLatestUsageRecordLag(t *testing.T) {
	env := newTestEnv(t)
	env.seedGoldenPair()
	aggregator := env.aggregator()

	// 第一批：处理全部记录，游标推进到 rec-2。
	processed, err := aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 10})
	if err != nil || processed != 2 {
		t.Fatalf("第一批 processed=%d err=%v", processed, err)
	}
	// 第二批：游标之后无记录 → 空批次路径，lag 记 0（latestUsageRecordLagSeconds
	// 与主查询谓词完全一致，空批次时最新行查询必空）。
	processed, err = aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 10})
	if err != nil || processed != 0 {
		t.Fatalf("空批次 processed=%d err=%v", processed, err)
	}
	lag := env.queryRowFloats(`SELECT lag_seconds FROM stats_job_state WHERE job_name='usage_stats_aggregation'`)
	if len(lag) != 1 || lag[0] != 0 {
		t.Fatalf("空批次 lag = %v want 0", lag)
	}
	// Aggregator.Now 为 nil → time.Now 兜底臂。
	bare := &Aggregator{DB: env.db, Dialect: env.dialect, Clock: StaticTimezoneSource{env.zone}}
	if bare.now().IsZero() {
		t.Fatalf("time.Now 兜底不应为零值")
	}
}

// ---- 派生窗口脏标记冲突更新臂 ----

func TestW10CDerivedDirtyScopesConflict(t *testing.T) {
	env := newTestEnv(t)
	aggregator := env.aggregator()

	// 第一批：04-17 单日记录（system_account + account scope）。
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-d1", SystemAccountID: "carol", TraceID: "tr", TrafficSource: "gateway",
		APIKeyID: strPtr("key-1"), Success: 1, CreatedAt: "2026-04-17T10:00:00.000Z",
		AccountID: strPtr("acc-1"), AccountOwnerSystemAccountID: strPtr("carol"), AccountAccessType: strPtr("owner"),
	})
	if _, err := aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 10}); err != nil {
		t.Fatal(err)
	}
	// 第二批：04-18 记录同账户 → ON CONFLICT DO UPDATE：overview min_changed_date
	// 取 MIN（leastExpr 冲突臂）、ai max_stat_date 取 MAX（greatestExpr 冲突臂）、
	// generation +1。
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-d2", SystemAccountID: "carol", TraceID: "tr", TrafficSource: "gateway",
		APIKeyID: strPtr("key-1"), Success: 1, CreatedAt: "2026-04-18T10:00:00.000Z",
		AccountID: strPtr("acc-1"), AccountOwnerSystemAccountID: strPtr("carol"), AccountAccessType: strPtr("owner"),
	})
	if _, err := aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 10}); err != nil {
		t.Fatal(err)
	}
	overview := env.queryString(`SELECT min_changed_date || '|' || scope_id FROM usage_overview_dirty_scopes WHERE system_account_id='carol'`)
	if len(overview) != 1 || overview[0] != "2026-04-17|carol" {
		t.Fatalf("overview 冲突更新错误: %v", overview)
	}
	if got := env.queryRowFloats(`SELECT generation FROM usage_overview_dirty_scopes WHERE system_account_id='carol'`); got[0] != 2 {
		t.Fatalf("overview generation = %v want 2", got)
	}
	ai := env.queryString(`SELECT min_stat_date || '|' || max_stat_date FROM ai_performance_summary_dirty_system_accounts WHERE system_account_id='carol'`)
	if len(ai) != 1 || ai[0] != "2026-04-17|2026-04-18" {
		t.Fatalf("ai 冲突更新错误: %v", ai)
	}
}

// ---- 授权查找 JOIN ----

func TestW10CAuthorizationLookupJoin(t *testing.T) {
	env := newTestEnv(t)
	// 授权 + 实例账户 fixture（SQLite 最小 schema：resource_authorizations
	// 只有 id/resource_type/resource_id/grantee_system_account_id 四列）。
	env.exec(`INSERT INTO resource_authorizations (id, resource_type, resource_id, grantee_system_account_id)
		VALUES ('auth-1', 'account', 'acc-instance', 'bob')`)
	env.exec(`INSERT INTO accounts (id, system_account_id, authorization_instance_authorization_id)
		VALUES ('acc-instance', 'bob', 'auth-1')`)
	// 授权调用记录 + 未知授权 id 记录（ErrNoRows continue 臂）。
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-a1", SystemAccountID: "bob", TraceID: "tr", TrafficSource: "gateway",
		APIKeyID: strPtr("key-1"), Success: 1, CreatedAt: "2026-04-18T10:15:00.000Z",
		AccountID: strPtr("acc-instance"), AccountOwnerSystemAccountID: strPtr("alice"), AccountAccessType: strPtr("account_authorized"),
		AccountAuthorizationID: strPtr("auth-1"),
	})
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-a2", SystemAccountID: "bob", TraceID: "tr", TrafficSource: "gateway",
		Success: 1, CreatedAt: "2026-04-18T10:16:00.000Z",
		AccountID: strPtr("acc-x"), AccountOwnerSystemAccountID: strPtr("alice"), AccountAccessType: strPtr("account_authorized"),
		AccountAuthorizationID: strPtr("auth-missing"),
	})
	if _, err := env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 10}); err != nil {
		t.Fatal(err)
	}
	// 未知授权 id 不中断聚合（continue 臂）。
	if got := env.queryRowFloats(`SELECT COUNT(*) FROM usage_stats_totals`); got[0] == 0 {
		t.Fatalf("聚合应完成")
	}
}

// ---- 系统指标采样批次 + 趋势窗口 stage 全链 ----

func TestW10CSystemMetricsSampleBatchAndTrendStage(t *testing.T) {
	env := newTestEnv(t)
	refresher := env.refresher()
	ctx := context.Background()

	// 非法 sampledAt 错误臂。
	badInput := SystemMetricsSampleInput{SampledAt: "garbage"}
	if err := refresher.InsertSystemMetricsSampleBatch(ctx, badInput, nil); err == nil {
		t.Fatalf("非法 sampledAt 应报错")
	}

	// 全量 nil 指标字段（nullableParam nil 臂）+ 显式 sampledAt + 事件循环样本。
	// 采样 ID 由 sampledAt 派生（process-metric-<ts>-<UnixNano()%1e6>），同
	// sampledAt 会撞主键；同小时冲突臂用秒级不同的 sampledAt 覆盖。
	input := SystemMetricsSampleInput{SampledAt: "2026-04-18T10:00:00.000Z"}
	samples := []ProcessEventLoopSampleInput{
		{ProcessRole: "server", SampledAt: "2026-04-18T10:00:00.000Z", EventLoopLagMs: f64Ptr(12.5)},
		{ProcessRole: "stats-worker:2", SampledAt: "2026-04-18T10:00:01.000Z", EventLoopLagMs: f64Ptr(3.25)},
		// 全空指标样本被 normalizedProcessEventLoopSample 跳过（skip 臂）。
		{ProcessRole: "usage-worker:1", SampledAt: "2026-04-18T10:00:00.000Z"},
	}
	if err := refresher.InsertSystemMetricsSampleBatch(ctx, input, samples); err != nil {
		t.Fatal(err)
	}
	if got := env.queryRowFloats(`SELECT COUNT(*) FROM system_metrics_hourly WHERE stat_hour='2026-04-18T10'`); got[0] != 1 {
		t.Fatalf("system_metrics_hourly 未落行: %v", got)
	}
	if got := env.queryRowFloats(`SELECT COUNT(*) FROM process_event_loop_hourly`); got[0] != 2 {
		t.Fatalf("process_event_loop_hourly 未落行: %v", got)
	}
	// 同小时二次采样：sum 累加 + sample_count +1（upsert 冲突臂）。
	secondInput := SystemMetricsSampleInput{SampledAt: "2026-04-18T10:30:00.000Z", CPUPercent: f64Ptr(50), EventLoopLagMs: f64Ptr(20)}
	secondSamples := []ProcessEventLoopSampleInput{
		{ProcessRole: "server", SampledAt: "2026-04-18T10:30:00.000Z", EventLoopLagMs: f64Ptr(30)},
	}
	if err := refresher.InsertSystemMetricsSampleBatch(ctx, secondInput, secondSamples); err != nil {
		t.Fatal(err)
	}
	if got := env.queryRowFloats(`SELECT sample_count FROM system_metrics_hourly WHERE stat_hour='2026-04-18T10'`); got[0] != 2 {
		t.Fatalf("sample_count = %v want 2", got)
	}

	// systemMetricsTrendRowsAtWatermark 纯函数臂：nil updated_at、非字符串、
	// 非法时间、非目标水位均过滤。
	rawRows := []rawRow{
		{columns: []string{"updated_at"}, values: map[string]any{"updated_at": nil}},
		{columns: []string{"updated_at"}, values: map[string]any{"updated_at": 42}},
		{columns: []string{"updated_at"}, values: map[string]any{"updated_at": "garbage"}},
		{columns: []string{"updated_at"}, values: map[string]any{"updated_at": "2026-04-18T09:00:00.000Z"}},
		{columns: []string{"updated_at"}, values: map[string]any{"updated_at": "2026-04-18T10:00:00.000Z"}},
	}
	if got := systemMetricsTrendRowsAtWatermark(rawRows, "2026-04-18T10:00:00.000Z"); len(got) != 1 {
		t.Fatalf("watermark 行过滤错误: %d", len(got))
	}
	if got := systemMetricsTrendRowsAtWatermark(rawRows, "2026-04-18T11:00:00.000Z"); len(got) != 0 {
		t.Fatalf("无匹配水位应为空: %d", len(got))
	}

	// 趋势窗口 stage 全链：单阶段 + SkipIfUnchanged → sourceVersion 状态行。
	result, err := refresher.RunStages(ctx, []WindowStageName{StageSystemMetricsTrendWindows}, RefreshOptions{SkipIfUnchanged: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stages) != 1 || result.SourceWatermark == "" {
		t.Fatalf("趋势窗口 stage 结果错误: %+v", result)
	}
	if got := env.queryRowFloats(`SELECT COUNT(*) FROM system_metrics_trend_windows`); got[0] == 0 {
		t.Fatalf("system_metrics_trend_windows 应重建")
	}
	// sourceVersion 状态行落库。
	if got := env.queryRowFloats(`SELECT COUNT(*) FROM stats_job_state WHERE scope_type='usage_rank_snapshot_source_version' AND job_name=?`,
		DefaultJobName([]WindowStageName{StageSystemMetricsTrendWindows})); got[0] != 1 {
		t.Fatalf("sourceVersion 状态行未落库: %v", got)
	}
	// 源未变 → 次轮跳过。
	second, err := refresher.RunStages(ctx, []WindowStageName{StageSystemMetricsTrendWindows}, RefreshOptions{SkipIfUnchanged: true})
	if err != nil || !second.Skipped {
		t.Fatalf("次轮应跳过: %+v err=%v", second, err)
	}
}

// ---- 配额重建句柄分派与绑定过滤 ----

func TestW10CQuotaRebuildHandlesAndBindingFilter(t *testing.T) {
	env := newTestEnv(t)
	businessDB := quotaTestBusinessDB(t)
	// 有效 + 无效 window_hours 绑定（validQuotaHourlyWindowHours 过滤臂）。
	// 绑定表在独立业务库（quotaTestBusinessDB 已建表），插入走 businessDB。
	for _, binding := range []struct {
		scopeID string
		hours   int
	}{
		{"key-1", 24}, {"key-bad", 0}, {"key-bad2", 1000},
	} {
		if _, err := businessDB.Exec("INSERT INTO request_quota_hourly_window_scope_bindings (system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at) VALUES ('alice','api_key',?,'manual','src',?,'2026-04-18T10:00:00.000Z','2026-04-18T10:00:00.000Z')", binding.scopeID, binding.hours); err != nil {
			t.Fatal(err)
		}
	}

	// BusinessDB 句柄分派：独立业务库连接承载绑定表（businessDB() 非 nil 臂）。
	refresher := env.refresher()
	refresher.BusinessDB = businessDB
	if got := env.queryRowFloats("SELECT COUNT(*) FROM usage_quota_hourly_windows"); got[0] != 0 {
		t.Fatalf("前置应为空")
	}
	result, err := refresher.RunQuotaHourlyWindows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.HasMore {
		t.Fatalf("SQLite 全量重建固定 changed=true/hasMore=false: %+v", result)
	}
	// 有效绑定落窗，无效 hours 过滤；零成本 scope 不落行。
	if got := env.queryRowFloats(`SELECT COUNT(*) FROM usage_quota_hourly_windows WHERE scope_id='key-1'`); got[0] != 0 {
		t.Fatalf("零成本 scope 不应落行: %v", got)
	}
	// scopeLimit()：ScopeLimit>0 覆盖默认。
	refresher.ScopeLimit = 7
	if refresher.scopeLimit() != 7 {
		t.Fatalf("ScopeLimit 覆盖失败")
	}
	// businessDB nil 兜底臂 → 回退 w.DB。
	fallback := env.refresher()
	if fallback.businessDB() != env.db {
		t.Fatalf("nil BusinessDB 应回退 w.DB")
	}
}

// ---- RunStages 错误臂 ----

func TestW10CRunStagesErrorArms(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Clock 失败 → RunStages 错误透传。
	failing := &WindowRefresher{DB: env.db, Dialect: env.dialect, Clock: w10cErrorClock{}}
	if _, err := failing.RunStages(ctx, nil, RefreshOptions{}); err == nil || err.Error() != "clock 失败" {
		t.Fatalf("RunStages clock 失败应透传, got %v", err)
	}
	// runQuotaHourlyWindows clock 失败臂。
	if _, err := failing.RunQuotaHourlyWindows(ctx); err == nil || err.Error() != "clock 失败" {
		t.Fatalf("RunQuotaHourlyWindows clock 失败应透传, got %v", err)
	}

	// 源表非法 updated_at → sourceState 错误臂。
	env.exec(`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, updated_at)
		VALUES ('alice', 'system_account', 'alice', '2026-04-18', 1, 'garbage')`)
	if _, err := env.refresher().RunStages(ctx, []WindowStageName{StageUsageOverviewWindows}, RefreshOptions{}); err == nil {
		t.Fatalf("非法 updated_at 应报错")
	}
}
