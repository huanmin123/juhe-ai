package gatewayquota

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestWJAPIKeyQuotaAsyncRedisSharedWriteFailure 固定 Redis server 角色
// API Key 检查中共享缓存写入失败必须向上传播的契约（setCacheEntryAsync await）。
func TestWJAPIKeyQuotaAsyncRedisSharedWriteFailure(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	_, _, snapshot, runtimeState := newRedisQuotaStack(t, clock)
	failing := &wjMockShared{setErr: errors.New("shared write down")}
	dbService := &mockDBService{}
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes:     Modes{RedisCache: true, RedisRuntimeState: true, ServerRole: true},
		Timezone:  mustTZ(t, time.UTC),
		Snapshot:  snapshot,
		Shared:    failing,
		DBService: dbService,
		Now:       clock.Now,
	})
	if err != nil {
		t.Fatalf("NewAPIKeyQuotaService: %v", err)
	}
	ctx := context.Background()
	apiKey := APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: testQuotaLimits}

	// 快照命中且成本超限 → 被动拒绝；回写 shared 失败 → 错误传播。
	doc := snapshotFixture("2026-09-04T00:00:00.000Z", true, true)
	doc.CostEntries = []QuotaCostSnapshotEntry{{
		SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak",
		Costs: RequestQuotaCosts{Daily: 50, Total: 50},
	}}
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey, doc, time.Hour); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	if _, err := service.CheckAPIKeyQuotaAsync(ctx, apiKey); err == nil || err.Error() != "shared write down" {
		t.Fatalf("被动决策写 shared 失败必须传播: %v", err)
	}

	// 快照缺失 + DB service 决策成功 + 写 shared 失败 → 同样传播。先越过
	// 1s memo 让删除后的“无文档”真正生效。
	clock.Advance(2 * time.Second)
	if err := runtimeState.Delete(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey); err != nil {
		t.Fatalf("delete snapshot: %v", err)
	}
	dbService.mu.Lock()
	dbService.checkAPIKeyDec = AllowedDecision()
	dbService.mu.Unlock()
	if _, err := service.CheckAPIKeyQuotaAsync(ctx, apiKey); err == nil || err.Error() != "shared write down" {
		t.Fatalf("DB service 决策写 shared 失败必须传播: %v", err)
	}
	if dbService.checkAPIKeyCalls != 1 {
		t.Fatalf("缺失快照必须精确补判, calls=%d", dbService.checkAPIKeyCalls)
	}
}

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
	// 统计库以 PostgreSQL 方言构造：批量装载在 SQLite 上无法执行。
	pgStatsDB := newTestDB(t, "wj-materialize-stats")
	statsSchema(t, pgStatsDB)
	pgStats, err := NewStatsStore(pgStatsDB, true)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	service, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
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
	seedCost(t, statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "total_cost_usd"},
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
	stats, err := NewStatsStore(db, false)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	return stats
}
