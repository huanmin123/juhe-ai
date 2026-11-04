package statsagg

// BUG-0345 根治回归：晚到写入的用量记录必须仍被聚合消费。
//
// 旧口径按 (created_at, id) 顺序游标消费：只要游标越过了某条记录的逻辑时间
// （created_at 是请求终态时刻），该记录之后才落库就永久漏计。新口径消费
// usage_stats_aggregated_at IS NULL 的行，与 created_at 顺序无关。
//
// 本用例直接构造「游标已越过、记录后到」的形态：先聚合 10:15/10:20 两条，
// 再插入 created_at=10:10 的晚到记录，断言它仍被消费且只消费一次。

import (
	"context"
	"testing"
	"time"
)

func TestAggregateUsageStatsBatchConsumesLateArrivingRecord(t *testing.T) {
	env := newPGTestEnv(t)
	env.seedGoldenPair()
	aggregator := env.aggregator()
	ctx := context.Background()

	if processed, err := aggregator.AggregateUsageStatsBatch(ctx, AggregateOptions{BatchSize: 100}); err != nil {
		t.Fatal(err)
	} else if processed != 2 {
		t.Fatalf("首批 processed = %d want 2", processed)
	}
	cursor := env.queryString(`SELECT cursor_created_at FROM juhe_stats.stats_job_state
		WHERE scope_type='global' AND scope_id='' AND job_name='usage_stats_aggregation'`)
	if len(cursor) != 1 || cursor[0] != "2026-04-18T10:20:00.000Z" {
		t.Fatalf("聚合游标 = %v want 2026-04-18T10:20:00.000Z", cursor)
	}
	if marked := env.queryFloats(`SELECT count(*) FROM juhe_usage.usage_records WHERE usage_stats_aggregated_at IS NOT NULL`); len(marked) != 1 || marked[0] != 2 {
		t.Fatalf("已消费行数 = %v want 2", marked)
	}

	// 晚到记录：created_at 早于已推进的游标（10:10 < 10:20），物理写入在聚合之后。
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-late", SystemAccountID: "bob", TraceID: "tr-late", TrafficSource: "gateway",
		ProviderCode: strPtr("openai"), Model: strPtr("gpt-5"), Success: 1,
		InputTokens: f64Ptr(7), CostUsd: f64Ptr(0.01), CreatedAt: "2026-04-18T10:10:00.000Z",
	})
	if marked := env.queryFloats(`SELECT count(*) FROM juhe_usage.usage_records WHERE usage_stats_aggregated_at IS NULL`); len(marked) != 1 || marked[0] != 1 {
		t.Fatalf("待消费行数 = %v want 1", marked)
	}

	processed, err := aggregator.AggregateUsageStatsBatch(ctx, AggregateOptions{BatchSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 {
		t.Fatalf("晚到记录 processed = %d want 1（旧游标口径会永久跳过这条）", processed)
	}
	env.assertFloats("global totals 含晚到记录",
		env.queryRowFloats(`SELECT request_count, input_tokens, total_cost_usd
			FROM juhe_stats.usage_stats_totals WHERE system_account_id='global' AND scope_type='system_account' AND scope_id='global'`),
		3, 112, 0.03)
	env.assertFloats("晚到记录的日桶",
		env.queryRowFloats(`SELECT request_count FROM juhe_stats.usage_stats_daily
			WHERE system_account_id='global' AND scope_type='system_account' AND scope_id='global' AND stat_date='2026-04-18'`),
		3)

	// 重复执行不重复计入：无待消费行时不再消费任何记录。
	processed, err = aggregator.AggregateUsageStatsBatch(ctx, AggregateOptions{BatchSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if processed != 0 {
		t.Fatalf("重复执行 processed = %d want 0", processed)
	}
	env.assertFloats("重复执行后 global totals 不变",
		env.queryRowFloats(`SELECT request_count, input_tokens FROM juhe_stats.usage_stats_totals
			WHERE system_account_id='global' AND scope_type='system_account' AND scope_id='global'`),
		3, 112)
	if lag := env.queryFloats(`SELECT lag_seconds FROM juhe_stats.stats_job_state
		WHERE scope_type='global' AND scope_id='' AND job_name='usage_stats_aggregation'`); len(lag) != 1 || lag[0] != 0 {
		t.Fatalf("无积压时 lag_seconds = %v want 0", lag)
	}
}

// TestAggregateUsageStatsBatchLagReportsOldestPending 锁定 lag 口径：积压年龄取
// 最旧未消费记录，而不是「最后消费行」——旧口径会把晚到行造成的积压读成 0。
func TestAggregateUsageStatsBatchLagReportsOldestPending(t *testing.T) {
	env := newPGTestEnv(t)
	env.seedGoldenPair()
	aggregator := env.aggregator()
	ctx := context.Background()
	if _, err := aggregator.AggregateUsageStatsBatch(ctx, AggregateOptions{BatchSize: 100}); err != nil {
		t.Fatal(err)
	}
	// 晚到且晚于安全截止：created_at 落在 now-15s 之后，本轮不可消费，形成积压。
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-pending", SystemAccountID: "bob", TraceID: "tr-pending", TrafficSource: "gateway",
		Success: 1, InputTokens: f64Ptr(1), CreatedAt: "2026-04-18T11:59:50.000Z",
	})
	if processed, err := aggregator.AggregateUsageStatsBatch(ctx, AggregateOptions{BatchSize: 100}); err != nil {
		t.Fatal(err)
	} else if processed != 0 {
		t.Fatalf("安全截止内 processed = %d want 0", processed)
	}
	// now=12:00:00，最旧待消费 11:59:50 → lag 10s。
	if lag := env.queryFloats(`SELECT lag_seconds FROM juhe_stats.stats_job_state
		WHERE scope_type='global' AND scope_id='' AND job_name='usage_stats_aggregation'`); len(lag) != 1 || lag[0] != 10 {
		t.Fatalf("lag_seconds = %v want 10", lag)
	}
	// 时钟推进后可消费，lag 归零。
	env.now = env.now.Add(20 * time.Second)
	if processed, err := aggregator.AggregateUsageStatsBatch(ctx, AggregateOptions{BatchSize: 100}); err != nil {
		t.Fatal(err)
	} else if processed != 1 {
		t.Fatalf("时钟推进后 processed = %d want 1", processed)
	}
	if lag := env.queryFloats(`SELECT lag_seconds FROM juhe_stats.stats_job_state
		WHERE scope_type='global' AND scope_id='' AND job_name='usage_stats_aggregation'`); len(lag) != 1 || lag[0] != 0 {
		t.Fatalf("排干后 lag_seconds = %v want 0", lag)
	}
}

// TestAggregateUsageStatsBatchBootstrapsExistingCursor：水位列首次启用（存量行
// 全无标记、但旧游标已推进）时按旧游标结算存量，绝不把历史行重算一遍。
// 统计写入是增量累加，重算一遍等于整体翻倍，这是上线时最危险的失败形态。
func TestAggregateUsageStatsBatchBootstrapsExistingCursor(t *testing.T) {
	env := newPGTestEnv(t)
	env.seedGoldenPair()
	// 模拟改造前的库：游标已推进到 rec-2，但没有任何消费标记。
	env.exec(`INSERT INTO juhe_stats.stats_job_state
		(scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, lag_seconds, updated_at)
		VALUES ('global', '', 'usage_stats_aggregation', '2026-04-18T10:20:00.000Z', 'rec-2', '2026-04-18T11:00:00.000Z', 0, '2026-04-18T11:00:00.000Z')`)

	aggregator := env.aggregator()
	processed, err := aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if processed != 0 {
		t.Fatalf("存量结算轮 processed = %d want 0", processed)
	}
	if marked := env.queryFloats(`SELECT count(*) FROM juhe_usage.usage_records WHERE usage_stats_aggregated_at IS NOT NULL`); len(marked) != 1 || marked[0] != 2 {
		t.Fatalf("存量结算行数 = %v want 2", marked)
	}
	// 结算不产生任何统计写入（否则就是重复计数）。
	if totals := env.queryFloats(`SELECT count(*) FROM juhe_stats.usage_stats_totals`); len(totals) != 1 || totals[0] != 0 {
		t.Fatalf("存量结算不应写入统计行, count = %v want 0", totals)
	}
	// 结算之后的新记录正常消费一次。
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-c", SystemAccountID: "alice", TraceID: "tr-c", TrafficSource: "gateway",
		ProviderCode: strPtr("openai"), Success: 1, InputTokens: f64Ptr(3), CostUsd: f64Ptr(0.01),
		CreatedAt: "2026-04-18T10:30:00.000Z",
	})
	if processed, err = aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 100}); err != nil {
		t.Fatal(err)
	} else if processed != 1 {
		t.Fatalf("结算后新记录 processed = %d want 1", processed)
	}
	env.assertFloats("结算后 global totals",
		env.queryRowFloats(`SELECT request_count, input_tokens FROM juhe_stats.usage_stats_totals
			WHERE system_account_id='global' AND scope_type='system_account' AND scope_id='global'`),
		1, 3)
}
