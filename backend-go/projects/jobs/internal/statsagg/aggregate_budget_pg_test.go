// aggregate_budget_pg_test.go —— 单批事务硬预算：预算在事务中途耗尽时
// 必须整批回滚，消费水位不推进、统计结果不落库，下一轮可重试同一批。
package statsagg

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAggregateUsageStatsBatchHonorsTimeBudget(t *testing.T) {
	env := newPGTestEnv(t)
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-budget", SystemAccountID: "alice", TraceID: "tr-budget", TrafficSource: "gateway",
		ProviderCode: strPtr("openai"), Model: strPtr("gpt-5"),
		Success: 1, InputTokens: f64Ptr(100), CreatedAt: "2026-04-18T10:15:00.000Z",
	})

	// 用另一连接对第一张统计结果表持 ACCESS EXCLUSIVE 锁：聚合事务会先
	// 完成 BeginTx、选行和内存聚合，再阻塞在 upsertUsageStatsTotals——这样
	// 预算一定在事务中途耗尽，而不是只卡在 BeginTx 前（后者无法证明回滚）。
	lockTx, err := env.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockTx.Exec(`LOCK TABLE juhe_stats.usage_stats_totals IN ACCESS EXCLUSIVE MODE`); err != nil {
		_ = lockTx.Rollback()
		t.Fatal(err)
	}
	startedAt := time.Now()
	_, err = env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{
		BatchSize:         10,
		SafeCreatedBefore: "2026-04-18T11:00:00.000Z",
		TimeBudget:        time.Second,
	})
	elapsed := time.Since(startedAt)
	if err == nil {
		_ = lockTx.Rollback()
		t.Fatal("预算耗尽必须返回错误，不能继续提交事务")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		_ = lockTx.Rollback()
		t.Fatalf("预算耗尽错误 = %v，want context.DeadlineExceeded", err)
	}
	if elapsed < 800*time.Millisecond {
		_ = lockTx.Rollback()
		t.Fatalf("聚合在 %s 返回，未真正等待到预算耗尽（可能只在 BeginTx 前失败）", elapsed)
	}
	if err := lockTx.Rollback(); err != nil {
		t.Fatalf("释放统计表锁失败: %v", err)
	}
	if got := env.queryString(`SELECT usage_stats_aggregated_at FROM juhe_usage.usage_records WHERE id='rec-budget'`); len(got) != 1 || got[0] != "" {
		t.Fatalf("消费水位 = %v，want NULL（整批回滚）", got)
	}
	if got := env.queryString(`SELECT request_count FROM juhe_stats.usage_stats_totals`); len(got) != 0 {
		t.Fatalf("预算回滚后仍写入 %d 条统计行: %v", len(got), got)
	}
}
