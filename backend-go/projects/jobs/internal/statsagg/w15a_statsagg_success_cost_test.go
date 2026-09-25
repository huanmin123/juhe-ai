package statsagg

// 成功口径成本（success_cost_usd）聚合断言：业务口径拍板为「客户端配额与
// 账单只计成功交付的尝试」。切号重试的部分流失败尝试（Success=false 但携带
// token/费用）保留 total_cost_usd 全量观测，但只进 total_cost_usd；成功口径
// 列只累加 success=1 的成本，供配额读侧 gateway gatewayquota/costs.go 消费。

import (
	"context"
	"testing"
)

// TestAccumulatorSuccessCostOnlyFromSuccessRecords 固定 accumulator 层口径：
// 成功记录成本进 TotalCostUsd 与 SuccessCostUsd；失败记录成本只进 TotalCostUsd。
func TestAccumulatorSuccessCostOnlyFromSuccessRecords(t *testing.T) {
	success := UsageStatsAccumulatorFromRecord(UsageStatsRecordRow{
		ID: "rec-ok", SystemAccountID: "alice", TrafficSource: "gateway",
		Success: 1, CostUsd: f64Ptr(0.02), CreatedAt: "2026-04-18T10:15:00.000Z",
	})
	if success.TotalCostUsd != 0.02 || success.SuccessCostUsd != 0.02 {
		t.Fatalf("成功记录 accumulator = total %v / success %v, want 0.02 / 0.02", success.TotalCostUsd, success.SuccessCostUsd)
	}
	if success.SuccessCount != 1 || success.ErrorCount != 0 {
		t.Fatalf("成功记录计数 = success %v / error %v", success.SuccessCount, success.ErrorCount)
	}

	failed := UsageStatsAccumulatorFromRecord(UsageStatsRecordRow{
		ID: "rec-fail", SystemAccountID: "alice", TrafficSource: "gateway",
		Success: 0, StatusCode: f64Ptr(502), CostUsd: f64Ptr(0.05), CreatedAt: "2026-04-18T10:16:00.000Z",
	})
	if failed.TotalCostUsd != 0.05 {
		t.Fatalf("失败记录 total_cost = %v, want 0.05（账号成本观测不可丢）", failed.TotalCostUsd)
	}
	if failed.SuccessCostUsd != 0 {
		t.Fatalf("失败记录 success_cost = %v, want 0（失败尝试不计配额口径）", failed.SuccessCostUsd)
	}
	if failed.ErrorCount != 1 {
		t.Fatalf("失败记录 error_count = %v, want 1（错误统计不受影响）", failed.ErrorCount)
	}

	merged := UsageStatsAccumulator{TotalCostUsd: 1, SuccessCostUsd: 0.5}
	if err := MergeAccumulator(&merged, success); err != nil {
		t.Fatal(err)
	}
	if err := MergeAccumulator(&merged, failed); err != nil {
		t.Fatal(err)
	}
	if merged.TotalCostUsd != 1.07 || merged.SuccessCostUsd != 0.52 {
		t.Fatalf("merge 后 = total %v / success %v, want 1.07 / 0.52", merged.TotalCostUsd, merged.SuccessCostUsd)
	}
}

// TestAggregateSuccessCostUsdSplitMixedAttempts 走真实聚合管线（SQLite 承载
// 测试库，双模 SQL 由 Dialect 单一来源生成）：同一 api_key scope 上一条成功
// 交付 + 一条带费用的失败尝试，聚合后 total_cost_usd 为全量、success_cost_usd
// 只含成功交付。
func TestAggregateSuccessCostUsdSplitMixedAttempts(t *testing.T) {
	env := newTestEnv(t)
	// 成功交付：cost 0.02（时间键 date=2026-04-18 / hour=2026-04-18T10）。
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-ok", SystemAccountID: "alice", TraceID: "tr-ok", TrafficSource: "gateway",
		APIKeyID: strPtr("key-1"), Endpoint: strPtr("/v1/chat/completions"),
		ProviderCode: strPtr("openai"), Model: strPtr("gpt-5"),
		Success:     1,
		InputTokens: f64Ptr(100), OutputTokens: f64Ptr(50),
		CostUsd: f64Ptr(0.02), CreatedAt: "2026-04-18T10:15:00.000Z",
	})
	// 切号重试的部分流失败尝试：Success=false 但携带 token 与费用 0.05。
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-partial-fail", SystemAccountID: "alice", TraceID: "tr-fail", TrafficSource: "gateway",
		APIKeyID: strPtr("key-1"), Endpoint: strPtr("/v1/chat/completions"),
		ProviderCode: strPtr("openai"), Model: strPtr("gpt-5"),
		Success: 0, StatusCode: f64Ptr(502), ErrorCode: strPtr("upstream_stream_failed"),
		InputTokens: f64Ptr(80), OutputTokens: f64Ptr(10),
		CostUsd: f64Ptr(0.05), CreatedAt: "2026-04-18T10:16:00.000Z",
	})

	processed, err := env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if processed != 2 {
		t.Fatalf("processed = %d want 2", processed)
	}

	// api_key scope（配额消费维度）：request 2 / success 1 / error 1；
	// total 0.07 全量观测；success_cost 0.02 配额口径。
	env.assertFloats("api_key totals mixed attempts",
		env.queryRowFloats(`SELECT request_count, success_count, error_count, total_cost_usd, success_cost_usd
			FROM usage_stats_totals WHERE system_account_id='alice' AND scope_type='api_key' AND scope_id='key-1'`),
		2, 1, 1, 0.07, 0.02)
	env.assertFloats("api_key daily mixed attempts",
		env.queryRowFloats(`SELECT total_cost_usd, success_cost_usd
			FROM usage_stats_daily WHERE system_account_id='alice' AND scope_type='api_key' AND scope_id='key-1' AND stat_date='2026-04-18'`),
		0.07, 0.02)
	env.assertFloats("api_key hourly mixed attempts",
		env.queryRowFloats(`SELECT total_cost_usd, success_cost_usd
			FROM usage_stats_hourly WHERE system_account_id='alice' AND scope_type='api_key' AND scope_id='key-1' AND stat_hour='2026-04-18T10'`),
		0.07, 0.02)

	// UPSERT 增量合并后的口径：重放同一批聚合（幂等场景由游标保证，这里
	// 直接验证第二批记录合并不破坏成功口径）。
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-ok-2", SystemAccountID: "alice", TraceID: "tr-ok-2", TrafficSource: "gateway",
		APIKeyID: strPtr("key-1"), Endpoint: strPtr("/v1/chat/completions"),
		ProviderCode: strPtr("openai"), Model: strPtr("gpt-5"),
		Success: 1,
		CostUsd: f64Ptr(0.03), CreatedAt: "2026-04-18T10:17:00.000Z",
	})
	processed, err = env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 {
		t.Fatalf("second processed = %d want 1", processed)
	}
	env.assertFloats("api_key totals after second batch",
		env.queryRowFloats(`SELECT total_cost_usd, success_cost_usd
			FROM usage_stats_totals WHERE system_account_id='alice' AND scope_type='api_key' AND scope_id='key-1'`),
		0.10, 0.05)
}
