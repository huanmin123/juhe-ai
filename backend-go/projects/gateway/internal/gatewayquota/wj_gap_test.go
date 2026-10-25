package gatewayquota

import (
	"context"
	"testing"
	"time"
)

// TestWJAPIKeyQuotaAsyncRedisSharedWriteFailure 固定 Redis server 角色
// API Key 检查中共享缓存写入失败必须向上传播的契约（setCacheEntryAsync await）。

// TestWJAuthzBatchAsyncSharedAllHit 固定批量入口在共享缓存全命中时的短路
// 返回：不进入快照/精确装载。
func TestWJAuthzBatchAsyncSharedAllHit(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	_, _, snapshot, _ := newRedisQuotaStack(t, clock)
	denied := newCachedDecision(DeniedDecision(AuthorizationQuotaExceededMessage), clock.Now().UnixMilli())
	shared := &wjMockShared{getFound: true, getValue: denied}
	service, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{RedisCache: true},
		Business: newTestDB(t, "wj-authz-allhit"),
		Stats:    mustStats(t),
		Timezone: mustTZ(t, time.UTC),
		Snapshot: snapshot,
		Shared:   shared,
		Now:      clock.Now,
	})
	if err != nil {
		t.Fatalf("NewAuthorizationQuotaService: %v", err)
	}
	ctx := context.Background()
	output, err := service.CheckAuthorizationQuotaBatchAsync(ctx, GroupAccessMetadata{GroupAuthorizationID: "ga1"}, []AccountAuthorizationSummary{
		{ID: "u1", AccountAuthorizationID: "aa1"},
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if output["u1"].Allowed || output["u1"].Message != AuthorizationQuotaExceededMessage {
		t.Fatalf("shared 全命中必须返回缓存拒绝: %+v", output["u1"])
	}
}

// TestWJAuthzBatchAsyncNoScopes 固定批量入口的空 scope 契约：group 与账户
// 授权 ID 全空时所有账户直接放行。
func TestWJAuthzBatchAsyncNoScopes(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	service, _, _ := wjNewAuthzService(t, Modes{}, clock, "noscope")
	output, err := service.CheckAuthorizationQuotaBatchAsync(context.Background(), GroupAccessMetadata{}, []AccountAuthorizationSummary{
		{ID: "u1"},
		{ID: "u2", AccountAuthorizationID: ""},
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	for _, accountID := range []string{"u1", "u2"} {
		if !output[accountID].Allowed || output[accountID].Message != "" {
			t.Fatalf("%s 空 scope 必须放行: %+v", accountID, output[accountID])
		}
	}
}

// TestWJAuthzBatchMaterializeError 固定批量成本装载失败的错误传播：
// 业务库读取成功而统计库装载失败时错误不得被吞掉。
func TestWJAuthzBatchMaterializeError(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	business := newTestDB(t, "wj-materialize-biz")
	authzSchema(t, business)
	seedAuthzRow(t, business, "ga1", "sysA", "sysB", "group", "g1", "", `{"daily":{"enabled":true,"limit":10}}`, "active")
	// 统计库以真实 PostgreSQL 构造后关闭连接池注入批量装载失败（SQLite 时代
	// 依赖「PG 方言在 SQLite 上无法执行」，迁真实 PG 后重钉为池关闭失败，
	// 断言保持不变）。
	pgStatsDB := newTestDB(t, "wj-materialize-stats")
	statsSchema(t, pgStatsDB)
	if err := pgStatsDB.Close(); err != nil {
		t.Fatalf("close stats db: %v", err)
	}
	pgStats, err := NewStatsStore(pgStatsDB)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	service, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		// 业务库查询走生产 PG 臂方言（业务库方言由 Modes.PostgresDatabase 决定，
		// 测试业务库已是真实 PG）。
		Modes:    Modes{},
		Business: business,
		Stats:    pgStats,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{}, clock),
		Now:      clock.Now,
	})
	if err != nil {
		t.Fatalf("NewAuthorizationQuotaService: %v", err)
	}
	if _, err := service.CheckAuthorizationQuotaBatchByIDs(context.Background(), "ga1", []AccountRef{{AccountID: "u1"}}, clock.Now()); err == nil {
		t.Fatal("批量成本装载失败必须报错")
	}
}

// TestWJAuthzTeamRowLimitShapes 固定 team grant 行的限制形态分支：无启用
// 限制的 grant 跳过 team 检查；账户维度 grant 在实例账户缺失时同样跳过。
func TestWJAuthzTeamRowLimitShapes(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	service, business, statsDB := wjNewAuthzService(t, Modes{}, clock, "teamshape")
	ctx := context.Background()

	// grant 无启用限制 → team 检查为空 → 允许。
	seedAuthzRow(t, business, "ga1", "sysA", "sysB", "group", "g1", "team1", "", "active")
	seedGrantRow(t, business, "grant-empty", "group", "g1", "team1", "sysA", "")
	decision, err := service.CheckAuthorizationQuotaByIDsReadOnly(ctx, "ga1", "", clock.Now())
	if err != nil || !decision.Allowed {
		t.Fatalf("team grant 无限制必须允许: (%+v, %v)", decision, err)
	}

	// 账户维度授权 + team grant + 实例账户缺失 → resourceID 不可得 → 跳过。
	seedAuthzRow(t, business, "aa2", "sysA", "sysB", "account", "acc2", "team1", "", "active")
	seedGrantRow(t, business, "grant-acc2", "account", "acc2", "team1", "sysA", `{"daily":{"enabled":true,"limit":1}}`)
	seedCost(t, statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sysA", "account_authorization_team", "acc2:team1", "2026-09-04", 5})
	decision, err = service.CheckAuthorizationQuotaByIDsReadOnly(ctx, "", "aa2", clock.Now())
	if err != nil || !decision.Allowed {
		t.Fatalf("实例账户缺失必须跳过 team 检查: (%+v, %v)", decision, err)
	}
}

func mustStats(t *testing.T) *StatsStore {
	t.Helper()
	db := newTestDB(t, "wj-must-stats")
	statsSchema(t, db)
	stats, err := NewStatsStore(db)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	return stats
}
