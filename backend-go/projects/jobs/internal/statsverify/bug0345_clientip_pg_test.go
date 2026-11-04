package statsverify

// BUG-0345 根治回归（client-ip 聚合）：晚到记录照样被消费、被排除的流量来源
// 也被消费（不阻塞待消费队列）、水位列首次启用时结算存量不重复计数。

import (
	"context"
	"testing"
	"time"
)

// bug0345SeedClientIPUsage 写入一条 client-ip 聚合关心的最小使用记录。
func bug0345SeedClientIPUsage(t *testing.T, store *Store, id, trafficSource, clientIP, createdAt string) {
	t.Helper()
	if _, err := store.db.ExecContext(context.Background(), `
		INSERT INTO juhe_usage.usage_records (
			id, traffic_source, client_ip, account_id, success, first_token_ms, duration_ms,
			input_tokens, output_tokens, cost_usd, created_at
		) VALUES ($1, $2, $3, 'acc-1', 1, 100, 200, 10, 5, 0.01, $4)`,
		id, trafficSource, clientIP, createdAt); err != nil {
		t.Fatalf("写入使用记录 %s 失败: %v", id, err)
	}
}

func bug0345ClientIPAggregatedCount(t *testing.T, store *Store) int {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM juhe_usage.usage_records WHERE client_ip_stats_aggregated_at IS NOT NULL`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestClientIPStatsAggregationConsumesLateAndExcludedRecords(t *testing.T) {
	store, _ := w10cSvOpenKitStore(t)
	ctx := context.Background()
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	mustExec(t, ctx, store.db, `INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
		VALUES ('sys_admin', 'usageStatsTimezone', $1, $2) ON CONFLICT (system_account_id, key) DO UPDATE SET value_json = EXCLUDED.value_json`,
		mustJSONString("UTC"), NowIso(now))

	bug0345SeedClientIPUsage(t, store, "rec-a", "gateway", "1.1.1.1", "2026-04-18T10:00:00.000Z")
	// 被排除的流量来源同样要被消费，否则它们永久留在待消费队列里。
	bug0345SeedClientIPUsage(t, store, "rec-probe", "cooldown_retest", "2.2.2.2", "2026-04-18T10:01:00.000Z")

	processed, err := store.AggregateClientIPStatsBatch(ctx, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if processed != 2 {
		t.Fatalf("首批 processed = %d want 2（含被排除来源的消费）", processed)
	}
	if got := bug0345ClientIPAggregatedCount(t, store); got != 2 {
		t.Fatalf("已消费行数 = %d want 2", got)
	}
	// 被排除来源不产生聚合行。
	var probeRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_stats.client_ip_stats_daily
		WHERE ip_hash IN (SELECT ip_hash FROM juhe_stats.client_ip_registry WHERE client_ip = '2.2.2.2')`).Scan(&probeRows); err != nil {
		t.Fatal(err)
	}
	if probeRows != 0 {
		t.Fatalf("被排除来源不应产生聚合行, count=%d", probeRows)
	}

	// 晚到记录：created_at 早于已推进的游标（10:01），物理写入发生在聚合之后。
	bug0345SeedClientIPUsage(t, store, "rec-late", "gateway", "3.3.3.3", "2026-04-18T09:59:00.000Z")
	processed, err = store.AggregateClientIPStatsBatch(ctx, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 {
		t.Fatalf("晚到记录 processed = %d want 1（旧游标口径会永久跳过这条）", processed)
	}
	var lateRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_stats.client_ip_stats_daily
		WHERE ip_hash IN (SELECT ip_hash FROM juhe_stats.client_ip_registry WHERE client_ip = '3.3.3.3')`).Scan(&lateRows); err != nil {
		t.Fatal(err)
	}
	if lateRows != 1 {
		t.Fatalf("晚到记录应进入日聚合, count=%d", lateRows)
	}
	// 重复执行幂等。
	if processed, err = store.AggregateClientIPStatsBatch(ctx, 10, now); err != nil {
		t.Fatal(err)
	} else if processed != 0 {
		t.Fatalf("重复执行 processed = %d want 0", processed)
	}
}

// TestClientIPStatsAggregationBootstrapsExistingCursor：水位列首次启用（存量行
// 无标记）时，按旧游标结算存量，不把已聚合过的历史行再算一遍。
func TestClientIPStatsAggregationBootstrapsExistingCursor(t *testing.T) {
	store, _ := w10cSvOpenKitStore(t)
	ctx := context.Background()
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	mustExec(t, ctx, store.db, `INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
		VALUES ('sys_admin', 'usageStatsTimezone', $1, $2) ON CONFLICT (system_account_id, key) DO UPDATE SET value_json = EXCLUDED.value_json`,
		mustJSONString("UTC"), NowIso(now))

	bug0345SeedClientIPUsage(t, store, "rec-a", "gateway", "1.1.1.1", "2026-04-18T10:00:00.000Z")
	bug0345SeedClientIPUsage(t, store, "rec-b", "gateway", "1.1.1.1", "2026-04-18T10:05:00.000Z")
	// 模拟改造前的库：游标已推进到 10:05，但没有任何消费标记。
	mustExec(t, ctx, store.db, `INSERT INTO juhe_stats.stats_job_state
		(scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, lag_seconds, updated_at)
		VALUES ('global', '', 'client_ip_stats_aggregation', $1, 'rec-b', $2, 0, $2)`,
		"2026-04-18T10:05:00.000Z", NowIso(now))

	processed, err := store.AggregateClientIPStatsBatch(ctx, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if processed != 0 {
		t.Fatalf("存量结算轮 processed = %d want 0", processed)
	}
	if got := bug0345ClientIPAggregatedCount(t, store); got != 2 {
		t.Fatalf("存量结算行数 = %d want 2", got)
	}
	var aggregates int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_stats.client_ip_stats_daily`).Scan(&aggregates); err != nil {
		t.Fatal(err)
	}
	if aggregates != 0 {
		t.Fatalf("存量结算不应产生聚合行（会重复计数）, count=%d", aggregates)
	}
	// 结算后的新记录正常消费。
	bug0345SeedClientIPUsage(t, store, "rec-c", "gateway", "4.4.4.4", "2026-04-18T11:00:00.000Z")
	if processed, err = store.AggregateClientIPStatsBatch(ctx, 10, now); err != nil {
		t.Fatal(err)
	} else if processed != 1 {
		t.Fatalf("结算后新记录 processed = %d want 1", processed)
	}
}
