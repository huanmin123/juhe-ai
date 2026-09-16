package statsagg

// w12f_statsagg_arms_test.go 覆盖统计聚合域的关句柄错误臂、RFC3339 解析
// 边界、latency 排序出口与 SQLite 打开失败路径。
//
// w12f 收尾时未覆盖清单（距 95% 尚差约 80 条，均为以下两类，未在本轮登记
// 为不可达，后续 wave 可继续）：
//  1. stages_window_snapshots / stages_metrics / upserts / stages_quota 中
//     各 upsert 与查询的深层 err 传播臂——需要在事务中段精确注入 SQL 失败，
//     SQLite/PG driver 均不提供按语句失败注入；
//  2. aggregate.go / record.go / authorization.go / dirty_scopes.go 的数据
//     驱动分支（授权 owner/group 扇出、account quality/health 的字段组合、
//     NaN 归一）——需要构造大量特定字段组合的 usage 行 fixture。

import (
	"context"
	"math"
	"testing"
)

func TestW12fStatsAggregatorClosedDBArms(t *testing.T) {
	env := newTestEnv(t)
	aggregator := env.aggregator()
	if err := env.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{}); err == nil {
		t.Fatal("句柄关闭后聚合必须报错")
	}
}

func TestW12fStatsRefresherClosedDBArms(t *testing.T) {
	env := newTestEnv(t)
	refresher := env.refresher()
	if err := env.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := refresher.RunHotWindows(context.Background(), RefreshOptions{}); err == nil {
		t.Fatal("句柄关闭后 RunHotWindows 必须报错")
	}
	if _, err := refresher.RunStages(context.Background(), nil, RefreshOptions{}); err == nil {
		t.Fatal("句柄关闭后 RunStages 必须报错")
	}
	if _, err := refresher.RunQuotaHourlyWindows(context.Background()); err == nil {
		t.Fatal("句柄关闭后 RunQuotaHourlyWindows 必须报错")
	}
}

func TestW12fStatsUnknownStageRejected(t *testing.T) {
	env := newTestEnv(t)
	refresher := env.refresher()
	if _, err := refresher.RunStages(context.Background(), []WindowStageName{"w12f-not-a-stage"}, RefreshOptions{}); err == nil {
		t.Fatal("未知 stage 必须报错")
	}
}

func TestW12fStatsOpenSQLiteFailureArms(t *testing.T) {
	// 目录路径与非法路径打开失败。
	if db, err := OpenSQLiteTestDB("w12f-missing-dir/w12f/x.sqlite3"); err == nil {
		db.Close()
		t.Fatal("非法路径打开必须报错")
	}
}

func TestW12fStatsRFC3339Rejects(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
	}{
		{"小时越界", "2026-09-14T24:00:00Z"},
		{"分钟越界", "2026-09-14T10:60:00Z"},
		{"秒越界", "2026-09-14T10:00:60Z"},
		{"坏 offset", "2026-09-14T10:00:00+99:00"},
	}
	for _, tc := range cases {
		if _, ok := ParseRFC3339Instant(tc.raw); ok {
			t.Fatalf("%s 必须解析失败: %s", tc.name, tc.raw)
		}
	}
	if _, ok := ParseRFC3339Instant("2026-09-14T10:00:00.123456789Z"); !ok {
		t.Fatal("超长小数秒必须截断后解析成功")
	}
	if _, ok := ParseRFC3339Instant("2026-13-40T99:00:00Z"); ok {
		t.Fatal("整体非法日期必须失败")
	}
}

func TestW12fStatsLatencyBucketBounds(t *testing.T) {
	first := LatencyBucketUpperBoundsMs[0]
	if LatencyBucketUpperBound(-1) != first {
		t.Fatalf("负值必须落在第一桶: %d", LatencyBucketUpperBound(-1))
	}
	if got := LatencyBucketUpperBound(float64(first)); got != first {
		t.Fatalf("边界值必须命中本桶: %d", got)
	}
	if got := LatencyBucketUpperBound(math.MaxFloat64); got != -1 {
		t.Fatalf("超大值必须命中末桶哨兵: %d", got)
	}
}

func TestW12fStatsNaNGuard(t *testing.T) {
	// nullableNumber 对 NaN/Inf 归一为 nil。
	value := math.NaN()
	if got := nullableNumber(&value); got != nil {
		t.Fatal("NaN 必须归一 nil")
	}
	inf := math.Inf(1)
	if got := nullableNumber(&inf); got != nil {
		t.Fatal("Inf 必须归一 nil")
	}
	finite := 1.5
	if got := nullableNumber(&finite); got == nil || *got != 1.5 {
		t.Fatal("有限值必须保留")
	}
}

// TestW12fStatsRunStagesEachClosedDB 逐个合法 stage 在关句柄库上的第一错误
// 传播（每个 stage 的 fail-closed 入口臂）。
func TestW12fStatsRunStagesEachClosedDB(t *testing.T) {
	env := newTestEnv(t)
	refresher := env.refresher()
	if err := env.db.Close(); err != nil {
		t.Fatal(err)
	}
	stages := []WindowStageName{
		StageAccountLast7dRequestRank,
		StageCallerAccountLast7dRequestRank,
		StageApiKeyCurrentMonthCostRank,
		StageAccountAuthorizationCurrentMonthRank,
		StageGroupAuthorizationCurrentMonthRank,
		StageUsageOverviewWindows,
		StageAiPerformanceSummaryWindows,
		StageSystemMetricsTrendWindows,
	}
	for _, stage := range stages {
		if _, err := refresher.RunStages(context.Background(), []WindowStageName{stage}, RefreshOptions{}); err == nil {
			t.Fatalf("stage %s 在关句柄库上必须报错", stage)
		}
	}
}

// TestW12fStatsAggregateRowsEmpty 直接覆盖空记录早退臂。
func TestW12fStatsAggregateRowsEmpty(t *testing.T) {
	env := newTestEnv(t)
	aggregator := env.aggregator()
	tx, err := env.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := aggregator.aggregateRows(context.Background(), tx, nil, FormatRFC3339Millis(env.now), env.zone); err != nil {
		t.Fatalf("空记录必须早退成功: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}
