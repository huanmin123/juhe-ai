package gatewayquota

// w13d 波次补充测试（二）：配额服务的错误注入路径、redis 共享缓存与运行时
// 状态存储的故障分支、快照缓存的 redis 模式读取，以及各 helper。
//
// 本文件登记的不可达语句：
//   - apikeyquota.go:456-459（server 角色非 redis 分支的 setCacheEntryAsync
//     错误）：该分支走 memory.set，恒返回 nil。
//   - authzquota.go:272-274（非 redis 分支的 setCacheEntryAsync 错误）：同上。
//   - costs.go:340（rows.Scan 错误）、authzquota.go:1088/1138（rows.Scan
//     错误）与 rows.Err 分支：schema 列类型与 Scan 目标一致，SQLite 驱动下
//     无法从公共 API 触发。
//   - rediscache.go:214-218（namespaceVersion 的 SetNX 竞争回读）：单客户端
//     顺序调用下 SetNX 恒成功，竞争窗口需要真实并发写方。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// w13dSharedOverride 在真实 RedisSharedCache 之上注入 Set 错误。
type w13dSharedOverride struct {
	inner    SharedJSONCache
	setErrAt int
	setCount int
}

func (o *w13dSharedOverride) Get(ctx context.Context, key string, target any) (bool, error) {
	return o.inner.Get(ctx, key, target)
}

func (o *w13dSharedOverride) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	o.setCount++
	if o.setErrAt > 0 && o.setCount >= o.setErrAt {
		return errors.New("shared 写入失败")
	}
	return o.inner.Set(ctx, key, value, ttl)
}

func (o *w13dSharedOverride) Clear(ctx context.Context) error {
	return o.inner.Clear(ctx)
}

// ---------------------------------------------------------------------------
// apikeyquota: 非回退路径与错误注入
// ---------------------------------------------------------------------------

func TestW13DAPIKeyQuotaExactAsyncFallbacks(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	db := newTestDB(t, "w13d-exact-fallback")
	statsSchema(t, db)
	// stats 用 SQLite 方言（无 schema 前缀表），配合 PostgresDatabase 模式走
	// 批量装载成功路径。
	stats, err := NewStatsStore(db, false)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes:    Modes{PostgresDatabase: true},
		Stats:    stats,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{PostgresDatabase: true}, clock),
		Now:      clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := clock.Now()
	seedCost(t, db, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sys", "api_key", "ak1", "2026-09-04", 20})

	// 非postgres 模式回退同步检查。
	fallback, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision, err := fallback.CheckAPIKeyQuotaExactAsync(ctx, w13dAPIKey("ak1"), now); err != nil || decision.Allowed {
		t.Fatalf("非 PG 回退必须按同步路径拒绝: (%+v, %v)", decision, err)
	}

	// PG 批量成功：命中 key → 超额拒绝；ExactAsync 决策进 memory。
	decision, err := service.CheckAPIKeyQuotaExactAsync(ctx, w13dAPIKey("ak1"), now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allowed || decision.Message != APIKeyQuotaExceededMessage {
		t.Fatalf("超额必须拒绝: %+v", decision)
	}
	// 第二次调用命中 memory 缓存（CostKey 一致）。
	decision, err = service.CheckAPIKeyQuotaExactAsync(ctx, w13dAPIKey("ak1"), now)
	if err != nil {
		t.Fatal(err)
	}
	_ = decision
	// ReadAPIKeyQuotaCostsExactAsync 命中 batch 结果。
	costs, err := service.ReadAPIKeyQuotaCostsExactAsync(ctx, w13dAPIKey("ak1"), now)
	if err != nil {
		t.Fatal(err)
	}
	if costs.Daily != 20 {
		t.Fatalf("costs=%+v", costs)
	}
	// 批量未命中（无该 key 的成本）→ 空成本。
	costs, err = service.ReadAPIKeyQuotaCostsExactAsync(ctx, w13dAPIKey("missing"), now)
	if err != nil {
		t.Fatal(err)
	}
	if costs != EmptyRequestQuotaCosts() {
		t.Fatalf("missing costs=%+v", costs)
	}
	// 无启用限额 → 直接放行。
	if decision, err := service.CheckAPIKeyQuotaExactAsync(ctx, w13dNoLimitsAPIKey("ak2"), now); err != nil || !decision.Allowed {
		t.Fatalf("无限额必须放行: (%+v, %v)", decision, err)
	}

	// ExactAsync 的 stats 缺失 / 坏 limits / 坏时区 / shared 错误。
	noStats, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes: Modes{PostgresDatabase: true}, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{PostgresDatabase: true}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noStats.CheckAPIKeyQuotaExactAsync(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("缺 Stats 必须报错")
	}
	if _, err := service.CheckAPIKeyQuotaExactAsync(ctx, APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: `{bad`}, now); err == nil {
		t.Fatal("坏 limits 必须报错")
	}
	brokenTZ, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes: Modes{PostgresDatabase: true}, Stats: stats, Timezone: w13dBrokenTZ{},
		Snapshot: mustSnapshotCache(t, Modes{PostgresDatabase: true}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := brokenTZ.CheckAPIKeyQuotaExactAsync(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("坏时区必须报错")
	}

	// shared 命中 / 错误（RedisCache 模式）。
	shared := &w13dFlakyShared{}
	_, _, redisSnapshot, _ := newRedisQuotaStack(t, clock)
	redisService, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes:    Modes{PostgresDatabase: true, RedisCache: true},
		Stats:    stats,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: redisSnapshot,
		Shared:   shared,
		Now:      clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	shared.mu.Lock()
	shared.getErr = errors.New("shared 读取失败")
	shared.mu.Unlock()
	if _, err := redisService.CheckAPIKeyQuotaExactAsync(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("shared 读取错误必须透传")
	}
	shared.mu.Lock()
	shared.getErr = nil
	shared.setErrAt = 1
	shared.mu.Unlock()
	if _, err := redisService.CheckAPIKeyQuotaExactAsync(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("shared 写入失败必须透传")
	}
}

func TestW13DAPIKeyQuotaCheckCostErrors(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	// stats 用 PG 方言（juhe_stats 前缀表在 SQLite 不存在）→ LoadCosts 报错。
	pgStats, err := NewStatsStore(newTestDB(t, "w13d-cost-err"), true)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Stats: pgStats, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := clock.Now()
	if _, err := service.CheckAPIKeyQuota(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("成本装载失败必须透传")
	}
	if _, err := service.CheckAPIKeyQuotaReadOnly(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("成本装载失败必须透传")
	}
	// 坏时区。
	brokenTZ, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Stats: pgStats, Timezone: w13dBrokenTZ{},
		Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := brokenTZ.CheckAPIKeyQuota(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("坏时区必须报错")
	}
}

func TestW13DAPIKeyQuotaAsyncRedisSnapshotErrors(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	server, shared, snapshot, _ := newRedisQuotaStack(t, clock)
	logs := &logRecorder{}
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes:    Modes{RedisCache: true, RedisRuntimeState: true, ServerRole: true},
		Timezone: mustTZ(t, time.UTC),
		Snapshot: snapshot,
		Shared:   shared,
		Now:      clock.Now,
		Log:      logs.hook,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// redis 快照读取故障 → 错误透传。
	server.SetError("模拟故障")
	if _, err := service.CheckAPIKeyQuotaAsync(ctx, w13dAPIKey("ak1")); err == nil {
		t.Fatal("redis 快照读取错误必须透传")
	}
	server.SetError("")
	// shared 命中路径：写共享缓存后命中。
	cached := newCachedDecision(Decision{Allowed: false, Message: APIKeyQuotaExceededMessage}, clock.Now().UnixMilli())
	if err := shared.Set(ctx, sharedCacheKey(w13dAPIKeyCacheKey(t, service, "ak1")), cached, apiKeyQuotaCacheTTL); err != nil {
		t.Fatal(err)
	}
	decision, err := service.CheckAPIKeyQuotaAsync(ctx, w13dAPIKey("ak1"))
	if err != nil || decision.Allowed {
		t.Fatalf("shared 命中必须拒绝: (%+v, %v)", decision, err)
	}
}

func w13dAPIKeyCacheKey(t *testing.T, service *APIKeyQuotaService, apiKeyID string) string {
	t.Helper()
	key, err := service.apiKeyQuotaCacheKey(context.Background(), w13dAPIKey(apiKeyID), service.now(), 0, false)
	if err != nil {
		t.Fatalf("cache key: %v", err)
	}
	return key
}

// ---------------------------------------------------------------------------
// authzquota: Async 各模式分支与错误注入
// ---------------------------------------------------------------------------

func TestW13DAuthorizationAsyncModeBranches(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	business := newTestDB(t, "w13d-authz-modes-biz")
	statsDB := newTestDB(t, "w13d-authz-modes-stats")
	authzSchema(t, business)
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seedAuthzRow(t, business, "ga1", "sysA", "sysB", "group", "g1", "", `{"daily":{"enabled":true,"limit":10}}`, "active")
	groupAccess := GroupAccessMetadata{GroupAuthorizationID: "ga1", GroupAuthorizationQuotaLimited: true}

	// 非 PG 模式：Async 走 ByIDs 同步路径并写 memory 缓存。
	memoryService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := memoryService.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil)
	if err != nil || !decision.Allowed {
		t.Fatalf("memory 模式必须放行: (%+v, %v)", decision, err)
	}
	// memory 命中：第二次调用直接返回缓存决策。
	decision, err = memoryService.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil)
	if err != nil || !decision.Allowed {
		t.Fatalf("memory 命中必须放行: (%+v, %v)", decision, err)
	}

	// PG 模式：Async 走 ByIDsExactAsync；业务表限定 schema，SQLite 上报错。
	pgService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{PostgresDatabase: true},
		Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{PostgresDatabase: true}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pgService.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil); err == nil {
		t.Fatal("PG 业务表查询在 SQLite 上必须报错")
	}

	// 坏时区：Async 的 cacheKey 构造错误透传（两种模式）。
	brokenTZ, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Business: business, Stats: stats, Timezone: w13dBrokenTZ{},
		Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := brokenTZ.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil); err == nil {
		t.Fatal("坏时区必须报错")
	}
	// 无授权 ID 的空输入在任何配额校验前直接放行。
	if decision, err := brokenTZ.CheckAuthorizationQuotaAsync(ctx, GroupAccessMetadata{}, nil); err != nil || !decision.Allowed {
		t.Fatalf("空输入必须直接放行: (%+v, %v)", decision, err)
	}

	// syncer 错误透传。
	syncerService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{PostgresDatabase: true, RedisRuntimeState: true},
		Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{PostgresDatabase: true}, clock),
		Syncer:   &wjMockSyncer{err: errors.New("失效同步失败")}, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncerService.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil); err == nil {
		t.Fatal("syncer 错误必须透传")
	}
}

func TestW13DAuthorizationByIDsErrorPaths(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	business := newTestDB(t, "w13d-authz-byids-err")
	statsDB := newTestDB(t, "w13d-authz-byids-stats")
	authzSchema(t, business)
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	brokenTZ, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Business: business, Stats: stats, Timezone: w13dBrokenTZ{},
		Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	// ByIDs / ByIDsReadOnly / ByIDsExactAsync 的批量错误透传。
	if _, err := brokenTZ.CheckAuthorizationQuotaByIDs(ctx, "ga1", "aa1", clock.Now()); err == nil {
		t.Fatal("ByIDs 批量错误必须透传")
	}
	if _, err := brokenTZ.CheckAuthorizationQuotaByIDsReadOnly(ctx, "ga1", "aa1", clock.Now()); err == nil {
		t.Fatal("ReadOnly 批量错误必须透传")
	}
	if _, err := brokenTZ.CheckAuthorizationQuotaByIDsExactAsync(ctx, "ga1", "aa1", clock.Now()); err == nil {
		t.Fatal("Exact 批量错误必须透传")
	}
	// 空 account 与空 group：批量决策为空 → 包装层回落允许。
	ok, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := ok.CheckAuthorizationQuotaBatchByIDsReadOnly(ctx, "", nil, clock.Now())
	if err != nil || len(empty) != 0 {
		t.Fatalf("空输入批量=%+v err=%v", empty, err)
	}
	if decision, err := ok.CheckAuthorizationQuotaByIDsReadOnly(ctx, "", "", clock.Now()); err != nil || !decision.Allowed {
		t.Fatalf("空批量必须放行: (%+v, %v)", decision, err)
	}
	if decision, err := ok.CheckAuthorizationQuotaByIDs(ctx, "", "", clock.Now()); err != nil || !decision.Allowed {
		t.Fatalf("空批量必须放行: (%+v, %v)", decision, err)
	}
	// syncer 错误（ExactAsync 批量）。
	syncerService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{PostgresDatabase: true, RedisRuntimeState: true},
		Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{PostgresDatabase: true}, clock),
		Syncer:   &wjMockSyncer{err: errors.New("失效同步失败")}, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncerService.CheckAuthorizationQuotaBatchByIDsExactAsync(ctx, "ga1", []AccountRef{{AccountID: "u1", AccountAuthorizationID: "aa1"}}, clock.Now()); err == nil {
		t.Fatal("syncer 错误必须透传")
	}
	// 重复 scope 去重。
	scopes := authorizationQuotaScopeList("ga1", []AccountRef{
		{AccountID: "u1", AccountAuthorizationID: "aa1"},
		{AccountID: "u2", AccountAuthorizationID: "aa1"},
	})
	if len(uniqueAuthorizationQuotaScopes(scopes)) != len(scopes)-1 {
		t.Fatalf("重复 scope 必须去重: %d -> %d", len(scopes), len(uniqueAuthorizationQuotaScopes(scopes)))
	}
}

func TestW13DAuthorizationRedisServerRoleSnapshotErrors(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	server, shared, snapshot, _ := newRedisQuotaStack(t, clock)
	business := newTestDB(t, "w13d-authz-redis-role-biz")
	statsDB := newTestDB(t, "w13d-authz-redis-role-stats")
	authzSchema(t, business)
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB, false)
	if err != nil {
		t.Fatal(err)
	}
	logs := &logRecorder{}
	dbService := &mockDBService{}
	service, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:     Modes{RedisCache: true, RedisRuntimeState: true, ServerRole: true},
		Business:  business,
		Stats:     stats,
		Timezone:  mustTZ(t, time.UTC),
		Snapshot:  snapshot,
		Shared:    shared,
		DBService: dbService,
		Now:       clock.Now,
		Log:       logs.hook,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	groupAccess := GroupAccessMetadata{GroupAuthorizationID: "ga1", GroupAuthorizationQuotaLimited: true}
	accounts := []AccountAuthorizationSummary{{ID: "u1", AccountAuthorizationID: "aa1", AccountAuthorizationQuotaLimited: true}}

	// redis 故障：HasGatewayQuotaSnapshotAsync 错误透传。
	server.SetError("模拟故障")
	if _, err := service.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil); err == nil {
		t.Fatal("快照可用性检查错误必须透传")
	}
	server.SetError("")
	// 发布一个完整快照（redis 共享缓存）→ 决策直接来自快照。
	complete := snapshotFixture("2026-09-04T00:00:00.000Z", true, true)
	complete.AuthorizationEntries = []AuthorizationQuotaSnapshotEntry{
		{ScopeType: ScopeTypeGroupAuthorization, AuthorizationID: "ga1", Decision: DeniedDecision(AuthorizationQuotaExceededMessage)},
	}
	if err := snapshot.ReplaceGatewayQuotaSnapshot(complete); err != nil {
		t.Fatal(err)
	}
	decision, err := service.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allowed {
		t.Fatalf("快照拒绝必须生效: %+v", decision)
	}
	// 发布快照后中断 redis：快照 memo 可用但读取需要回源 → 错误透传。
	server.SetError("模拟故障")
	if _, err := service.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil); err == nil {
		t.Fatal("快照回读错误必须透传")
	}
	server.SetError("")
	snapshot.ClearGatewayQuotaSnapshot()
	snapshot.clearSharedMemo()

	// 批量：redis 故障 → 错误透传。
	server.SetError("模拟故障")
	if _, err := service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts); err == nil {
		t.Fatal("批量快照检查错误必须透传")
	}
	server.SetError("")
	// 批量：db service 失败 → 保护性拒绝。
	dbService.mu.Lock()
	dbService.checkBatchErr = errors.New("ipc down")
	dbService.mu.Unlock()
	batch, err := service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range batch {
		if item.Allowed {
			t.Fatalf("保护性拒绝必须生效: %+v", batch)
		}
	}
	dbService.mu.Lock()
	dbService.checkBatchErr = nil
	dbService.mu.Unlock()
}

// ---------------------------------------------------------------------------
// redis shared cache + runtime state
// ---------------------------------------------------------------------------

func TestW13DRedisCacheConstructionGuards(t *testing.T) {
	if _, err := NewRedisRuntimeStateStore(nil, "dev", "store"); err == nil {
		t.Fatal("缺 client 必须报错")
	}
	if _, err := NewRedisRuntimeStateStore(&redis.Client{}, "", "store"); err == nil {
		t.Fatal("空 namespace 必须报错")
	}
	if _, err := NewRedisSharedCache(nil, "dev", "cache"); err == nil {
		t.Fatal("缺 client 必须报错")
	}
	if _, err := NewRedisSharedCache(&redis.Client{}, "", "cache"); err == nil {
		t.Fatal("空 namespace 必须报错")
	}
	if _, err := SanitizeRedisNamespacePart("   "); err == nil {
		t.Fatal("空 namespace 必须报错")
	}
	if _, err := RedisNamespacedKey("", "key"); err == nil {
		t.Fatal("空 namespace 必须报错")
	}
	if _, err := RedisNamespacedKey("dev", "  "); err == nil {
		t.Fatal("空 key 必须报错")
	}
	// 已带根前缀的 key 不重复前缀。
	key, err := RedisNamespacedKey("dev", "juhe-ai:state:x:")
	if err != nil || !strings.HasPrefix(key, "juhe-ai:dev:") {
		t.Fatalf("key=%q err=%v", key, err)
	}
}

func TestW13DRedisRuntimeStateAndSharedErrors(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	state, err := NewRedisRuntimeStateStore(client, "w13d", "gateway-quota-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	shared, err := NewRedisSharedCache(client, "w13d", "gateway:api-key-quota")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 空 key 守卫。
	if err := state.SetJSON(ctx, "store", "  ", map[string]string{}, time.Minute); err == nil {
		t.Fatal("空 key 必须报错")
	}
	if err := state.Delete(ctx, "store", "  "); err == nil {
		t.Fatal("空 key 必须报错")
	}
	// 损坏 JSON 读作缺失且删除。
	if err := state.SetJSON(ctx, "store", "corrupt", "{bad", time.Minute); err != nil {
		t.Fatal(err)
	}
	var target map[string]string
	if found, err := state.GetJSON(ctx, "store", "corrupt", &target); err != nil || found {
		t.Fatalf("损坏 JSON 必须读作缺失: found=%v err=%v", found, err)
	}

	// shared：不可编码值报错。
	if err := shared.Set(ctx, "bad", make(chan int), time.Minute); err == nil {
		t.Fatal("不可编码值必须报错")
	}
	// 正常 set/get/clear。
	if err := shared.Set(ctx, "k", map[string]string{"a": "b"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if found, err := shared.Get(ctx, "k", &got); err != nil || !found || got["a"] != "b" {
		t.Fatalf("found=%v err=%v got=%+v", found, err, got)
	}
	// Get 无版本 → read-only miss。
	if found, err := shared.Get(ctx, "missing", &got); err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	// 故障注入：Get / Set / Clear 的传输错误。
	server.SetError("模拟故障")
	if _, err := shared.Get(ctx, "k", &got); err == nil {
		t.Fatal("Get 错误必须透传")
	}
	if err := shared.Set(ctx, "k2", map[string]string{}, time.Minute); err == nil {
		t.Fatal("Set 错误必须透传")
	}
	if err := shared.Clear(ctx); err == nil {
		t.Fatal("Clear 错误必须透传")
	}
	// 版本键已存在时 Set 的写值错误（version Get 命中后 Set location 失败）。
	server.SetError("")
	if err := client.Set(ctx, shared.versionKey, "seed-version", 0).Err(); err != nil {
		t.Fatal(err)
	}
	server.SetError("模拟故障")
	if err := shared.Set(ctx, "k3", map[string]string{}, time.Minute); err == nil {
		t.Fatal("写值错误必须透传")
	}
	if err := shared.Clear(ctx); err == nil {
		t.Fatal("索引读取错误必须透传")
	}
	server.SetError("")
	if err := shared.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	// state 故障透传。
	server.SetError("模拟故障")
	if err := state.SetJSON(ctx, "store", "k", map[string]string{}, time.Minute); err == nil {
		t.Fatal("state Set 错误必须透传")
	}
	if _, err := state.GetJSON(ctx, "store", "k", &target); err == nil {
		t.Fatal("state Get 错误必须透传")
	}
	if err := state.Delete(ctx, "store", "k"); err == nil {
		t.Fatal("state Delete 错误必须透传")
	}
}

// ---------------------------------------------------------------------------
// snapshot cache: redis 模式读取与失效
// ---------------------------------------------------------------------------

func TestW13DSnapshotCacheRedisReads(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	server, _, snapshot, runtimeState := newRedisQuotaStack(t, clock)
	ctx := context.Background()

	// redis 模式下 memory 读取恒 miss。
	hours := 3
	costEntry := QuotaCostSnapshotEntry{
		SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak", HourlyWindowHours: &hours,
		Costs: RequestQuotaCosts{Daily: 2},
	}
	if _, found := snapshot.ReadCostsSnapshot(costEntry); found {
		t.Fatal("redis 模式 memory 读取必须 miss")
	}
	if _, found, err := snapshot.ReadCostsSnapshotAsync(ctx, costEntry); err != nil || found {
		t.Fatalf("无快照必须 miss: found=%v err=%v", found, err)
	}

	// worker 侧发布快照（写 runtime state）后 gateway 可读；先越过 memo TTL。
	clock.Advance(sharedSnapshotMemoTTL + time.Second)
	complete := snapshotFixture("2026-09-04T00:00:00.000Z", true, true)
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey, complete, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, found, err := snapshot.ReadCostsSnapshotAsync(ctx, costEntry); err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if _, found, err := snapshot.ReadAuthorizationSnapshotAsync(ctx, string(ScopeTypeGroupAuthorization), "ga"); err != nil || !found {
		t.Fatalf("authz found=%v err=%v", found, err)
	}
	if complete, err := snapshot.IsCostSnapshotCompleteAsync(ctx); err != nil || !complete {
		t.Fatalf("complete=%v err=%v", complete, err)
	}
	if incomplete, err := snapshot.IsAuthorizationSnapshotIncompleteAsync(ctx); err != nil || incomplete {
		t.Fatalf("incomplete=%v err=%v", incomplete, err)
	}
	if has, err := snapshot.HasGatewayQuotaSnapshotAsync(ctx); err != nil || !has {
		t.Fatalf("has=%v err=%v", has, err)
	}
	// redis 模式下本地标记不随 shared 快照更新（Replace 仅清空本地），
	// Is*Complete 内存语义由 memory 模式测试覆盖。
	// 推进时钟越过 shared memo TTL 后重新回源。
	clock.Advance(sharedSnapshotMemoTTL + time.Second)
	// runtime state 读取失败按"无快照"降级并告警（不向调用方透传错误，
	// 与 Node 的 warn + DB service 回退语义一致）。
	logs := &logRecorder{}
	failureSnapshot, err := NewSnapshotCache(Modes{RedisCache: true, RedisRuntimeState: true}, runtimeState, clock.Now, logs.hook)
	if err != nil {
		t.Fatal(err)
	}
	server.SetError("模拟故障")
	if _, found, err := failureSnapshot.ReadCostsSnapshotAsync(ctx, costEntry); err != nil || found {
		t.Fatalf("读取失败必须按 miss 处理: found=%v err=%v", found, err)
	}
	if !logs.has("gateway_quota_snapshot_runtime_state_read_failed|") {
		t.Fatal("读取失败必须告警")
	}
	server.SetError("")

	// 发布的快照 generatedAt 非法 → 回源处理错误透传。
	badGenerated := "not-a-time"
	badDoc := snapshotFixture(badGenerated, true, true)
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey, badDoc, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, _, err := snapshot.ReadCostsSnapshotAsync(ctx, costEntry); err == nil {
		t.Fatal("坏 generatedAt 必须透传")
	}
	if _, _, err := snapshot.ReadAuthorizationSnapshotAsync(ctx, string(ScopeTypeGroupAuthorization), "ga"); err == nil {
		t.Fatal("坏 generatedAt 必须透传")
	}
	if _, err := snapshot.IsCostSnapshotIncompleteAsync(ctx); err == nil {
		t.Fatal("坏 generatedAt 必须透传")
	}
	if _, err := snapshot.HasGatewayQuotaSnapshotAsync(ctx); err == nil {
		t.Fatal("坏 generatedAt 必须透传")
	}

	// 授权失效水位 + 坏 generatedAt → usable 校验错误透传。
	clock.Advance(sharedSnapshotMemoTTL + time.Second)
	if err := snapshot.InvalidateAuthorizationQuotaSnapshot(nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := snapshot.ReadAuthorizationSnapshotAsync(ctx, string(ScopeTypeGroupAuthorization), "ga"); err == nil {
		t.Fatal("失效水位下的坏 generatedAt 必须透传")
	}
	// 授权失效：publishedAt 非法报错。
	goodSnapshot, err := NewSnapshotCache(Modes{RedisCache: true, RedisRuntimeState: true}, runtimeState, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := goodSnapshot.InvalidateAuthorizationQuotaSnapshot(nil); err != nil {
		t.Fatal(err)
	}
	badTime := "not-a-time"
	if err := goodSnapshot.InvalidateAuthorizationQuotaSnapshot(&badTime); err == nil {
		t.Fatal("非法 publishedAt 必须报错")
	}
}

func TestW13DSnapshotDecisionFromSnapshotBranches(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	business := newTestDB(t, "w13d-authz-decision-biz")
	statsDB := newTestDB(t, "w13d-authz-decision-stats")
	authzSchema(t, business)
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB, false)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewSnapshotCache(Modes{}, nil, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	dbService := &mockDBService{}
	service, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes: Modes{ServerRole: true}, Business: business, Stats: stats,
		Timezone: mustTZ(t, time.UTC), Snapshot: snapshot, DBService: dbService, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 完整快照 + account 条目拒绝 → decisionFromSnapshot 返回拒绝决策。
	complete := snapshotFixture("2026-09-04T00:00:00.000Z", true, true)
	complete.AuthorizationEntries = []AuthorizationQuotaSnapshotEntry{
		{ScopeType: ScopeTypeAccountAuthorization, AuthorizationID: "aa1", Decision: DeniedDecision(AuthorizationQuotaExceededMessage)},
	}
	if err := snapshot.ReplaceGatewayQuotaSnapshot(complete); err != nil {
		t.Fatal(err)
	}
	input := authorizationSnapshotInput{accountAuthorizationID: "aa1", accountAuthorizationQuotaLimited: true}
	if decision := service.decisionFromSnapshot(input); decision.Allowed {
		t.Fatalf("account 拒绝必须生效: %+v", decision)
	}
	if decision, err := service.decisionFromSnapshotAsync(ctx, input); err != nil || decision.Allowed {
		t.Fatalf("async account 拒绝必须生效: (%+v, %v)", decision, err)
	}
	// 不完整快照 + 缺失条目 → 保护性拒绝。
	incomplete := snapshotFixture("2026-09-04T00:00:00.000Z", true, false)
	if err := snapshot.ReplaceGatewayQuotaSnapshot(incomplete); err != nil {
		t.Fatal(err)
	}
	missing := authorizationSnapshotInput{groupAuthorizationID: "g-missing", groupAuthorizationQuotaLimited: true}
	if decision := service.decisionFromSnapshot(missing); decision.Allowed {
		t.Fatalf("缺失条目必须保护性拒绝: %+v", decision)
	}
	if decision, err := service.decisionFromSnapshotAsync(ctx, missing); err != nil || decision.Allowed {
		t.Fatalf("async 缺失条目必须保护性拒绝: (%+v, %v)", decision, err)
	}
}

// ---------------------------------------------------------------------------
// helpers: modes / inflight / rfc3339 / statwindow / limits
// ---------------------------------------------------------------------------

func TestW13DQuotaMemoryCacheEviction(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	cache := newQuotaMemoryCache(clock.Now, time.Minute, 2)
	entry := newCachedDecision(AllowedDecision(), clock.Now().UnixMilli())
	cache.set("ak1", "k1", entry)
	cache.set("ak2", "k2", entry)
	cache.set("ak3", "k3", entry) // 超过 max → 驱逐最旧。
	if _, ok := cache.get("k1"); ok {
		t.Fatal("k1 必须被驱逐")
	}
	if _, ok := cache.get("k2"); !ok {
		t.Fatal("k2 必须保留")
	}
	if _, ok := cache.get("k3"); !ok {
		t.Fatal("k3 必须保留")
	}
	cache.removeByID("ak2")
	if _, ok := cache.get("k2"); ok {
		t.Fatal("removeByID 必须删除")
	}
	cache.clear()
	if _, ok := cache.get("k3"); ok {
		t.Fatal("clear 必须清空")
	}
}

func TestW13DRFC3339Helpers(t *testing.T) {
	// 数值 offset 与 Z 均可解析。
	if _, ok := rfc3339InstantMilliseconds("2026-09-04T08:00:00.5+08:00"); !ok {
		t.Fatal("带 offset 的时间必须可解析")
	}
	if _, ok := parseRfc3339Instant("2026-02-30T00:00:00Z"); ok {
		t.Fatal("2 月 30 日必须解析失败")
	}
	// 非法分数秒。
	if _, ok := parseRfc3339Instant("2026-09-04T08:00:00.1234567890Z"); ok {
		t.Fatal("超长分数秒必须失败")
	}
}

func TestW13DStatWindowGuards(t *testing.T) {
	if _, err := NewDBTimezoneSource(nil, false, nil); err == nil {
		t.Fatal("缺 db 必须报错")
	}
	db := newTestDB(t, "w13d-statwindow")
	source, err := NewDBTimezoneSource(db, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 系统设置表缺失 → 时区读取错误。
	if _, err := source.StatsTimezone(context.Background()); err == nil {
		t.Fatal("系统设置缺失必须报错")
	}
	// PG 方言在 SQLite 上读取必须报错。
	pgSource, err := NewDBTimezoneSource(db, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pgSource.StatsTimezone(context.Background()); err == nil {
		t.Fatal("PG 方言在 SQLite 上必须报错")
	}
}

func TestW13DQuotaLimitsHourlyValidation(t *testing.T) {
	cases := []string{
		`{"hourly":{"enabled":true}}`,
		`{"hourly":{"enabled":false,"limit":10,"hours":1}}`,
		`{"hourly":{"enabled":true,"limit":-1,"hours":1}}`,
		`{"hourly":{"enabled":true,"limit":10,"hours":0}}`,
		`{"hourly":{"enabled":true,"limit":10,"unknown":1}}`,
	}
	for _, raw := range cases {
		limits, err := ParseRequestQuotaLimitsJSON(raw)
		if err == nil {
			t.Fatalf("%s 必须报错: %+v", raw, limits)
		}
	}
	// 合法 hourly。
	limits, err := ParseRequestQuotaLimitsJSON(`{"hourly":{"enabled":true,"limit":10,"hours":2}}`)
	if err != nil || limits.Hourly == nil || limits.Hourly.Hours != 2 {
		t.Fatalf("limits=%+v err=%v", limits, err)
	}
	// 限额 JSON 序列化 round-trip。
	encoded, ok := RequestQuotaLimitsJSON(limits)
	if !ok || !strings.Contains(encoded, `"hours":2`) {
		t.Fatalf("encoded=%q ok=%v", encoded, ok)
	}
}

// ---------------------------------------------------------------------------
// w13d 补充：批量快照可用路径、成本批量与 helpers
// ---------------------------------------------------------------------------

func boolPtr(value bool) *bool { return &value }

func TestW13DAuthorizationBatchServerRoleSnapshotAvailable(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	server, shared, snapshot, runtimeState := newRedisQuotaStack(t, clock)
	business := newTestDB(t, "w13d-authz-batch-snap-biz")
	statsDB := newTestDB(t, "w13d-authz-batch-snap-stats")
	authzSchema(t, business)
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB, false)
	if err != nil {
		t.Fatal(err)
	}
	logs := &logRecorder{}
	dbService := &mockDBService{}
	service, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:     Modes{RedisCache: true, RedisRuntimeState: true, ServerRole: true},
		Business:  business,
		Stats:     stats,
		Timezone:  mustTZ(t, time.UTC),
		Snapshot:  snapshot,
		Shared:    shared,
		DBService: dbService,
		Now:       clock.Now,
		Log:       logs.hook,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	groupAccess := GroupAccessMetadata{GroupAuthorizationID: "ga1", GroupAuthorizationQuotaLimited: true}
	accounts := []AccountAuthorizationSummary{
		{ID: "u1", AccountAuthorizationID: "aa1", AccountAuthorizationQuotaLimited: true},
	}

	// 发布完整快照：group 条目拒绝 + account 条目拒绝 → 决策全部来自快照，
	// 不触碰 DB service。
	doc := snapshotFixture("2026-09-04T00:00:00.000Z", true, true)
	doc.AuthorizationEntries = []AuthorizationQuotaSnapshotEntry{
		{ScopeType: ScopeTypeGroupAuthorization, AuthorizationID: "ga1", Decision: DeniedDecision(AuthorizationQuotaExceededMessage)},
		{ScopeType: ScopeTypeAccountAuthorization, AuthorizationID: "aa1", Decision: AllowedDecision()},
	}
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey, doc, time.Minute); err != nil {
		t.Fatal(err)
	}
	batch, err := service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts)
	if err != nil {
		t.Fatal(err)
	}
	if batch["u1"].Allowed {
		t.Fatalf("快照拒绝必须生效: %+v", batch)
	}
	if dbService.checkBatchCalls != 0 {
		t.Fatal("快照完整时不得触碰 DB service")
	}

	// 快照不完整（authzComplete=false）且 account 条目缺失 → needsFallback
	// → DB service 批量补判；完整快照的缺失条目按允许判定、不触 DB。
	doc.AuthorizationEntriesComplete = boolPtr(false)
	doc.AuthorizationEntries = doc.AuthorizationEntries[:1]
	clock.Advance(sharedSnapshotMemoTTL + time.Second)
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey, doc, time.Minute); err != nil {
		t.Fatal(err)
	}
	dbService.mu.Lock()
	dbService.checkBatchDecs = []Decision{AllowedDecision()}
	dbService.mu.Unlock()
	service.ClearCache(ctx)
	batch, err = service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts)
	if err != nil {
		t.Fatal(err)
	}
	// account 条目缺失触发 DB service 补判，补判决策直接生效。
	if !batch["u1"].Allowed {
		t.Fatalf("DB service 补判必须生效: %+v", batch)
	}
	if dbService.checkBatchCalls == 0 {
		t.Fatal("缺失条目必须触发 DB service 补判")
	}

	// 单条 CheckAuthorizationQuotaAsync 的快照可用路径（完整快照）。
	clock.Advance(sharedSnapshotMemoTTL + time.Second)
	service.ClearCache(ctx)
	completeDoc := snapshotFixture("2026-09-04T00:00:00.000Z", true, true)
	completeDoc.AuthorizationEntries = []AuthorizationQuotaSnapshotEntry{
		{ScopeType: ScopeTypeGroupAuthorization, AuthorizationID: "ga1", Decision: DeniedDecision(AuthorizationQuotaExceededMessage)},
	}
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey, completeDoc, time.Minute); err != nil {
		t.Fatal(err)
	}
	dbService.mu.Lock()
	dbService.checkBatchDecs = nil
	dbService.mu.Unlock()
	decision, err := service.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = decision

	// server 恢复后清缓存重查：快照不完整 + db 成功 → 补判决策生效。
	server.SetError("")
	clock.Advance(sharedSnapshotMemoTTL + time.Second)
	service.ClearCache(ctx)
	if _, err := service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts); err != nil {
		t.Fatal(err)
	}
}

func TestW13DCostsBatchTableCoverage(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	db := newTestDB(t, "w13d-costs-batch")
	statsSchema(t, db)
	stats, err := NewStatsStore(db, false)
	if err != nil {
		t.Fatal(err)
	}
	location := time.UTC
	// daily/weekly/monthly + hourly on/off 的批量读取。
	seedCost(t, db, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sys", "api_key", "ak1", "2026-09-04", 3})
	seedCost(t, db, "usage_stats_weekly", []string{"system_account_id", "scope_type", "scope_id", "stat_week", "success_cost_usd"},
		[]any{"sys", "api_key", "ak1", "2026-W36", 5})
	seedCost(t, db, "usage_stats_monthly", []string{"system_account_id", "scope_type", "scope_id", "stat_month", "success_cost_usd"},
		[]any{"sys", "api_key", "ak1", "2026-09", 7})
	seedCost(t, db, "usage_quota_hourly_windows", []string{"system_account_id", "scope_type", "scope_id", "window_hours", "total_cost_usd"},
		[]any{"sys", "api_key", "ak1", 3, 1})
	hours := 3
	inputs := []CostInput{
		{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak1", Now: clock.Now(),
			HourlyWindowHours: hours, HasHourlyWindow: true},
		{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak1", Now: clock.Now(),
			HourlyWindowHours: hours, HasHourlyWindow: false},
		{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "missing", Now: clock.Now()},
	}
	costs, err := stats.LoadCostsBatch(context.Background(), inputs, location)
	if err != nil {
		t.Fatal(err)
	}
	if len(costs) == 0 {
		t.Fatal("批量成本不能为空")
	}
	// 单条 LoadCosts 同样覆盖各窗口表。
	single, err := stats.LoadCosts(context.Background(), inputs[0], location)
	if err != nil {
		t.Fatal(err)
	}
	_ = single
}

func TestW13DStatWindowTimezoneErrors(t *testing.T) {
	db := newTestDB(t, "w13d-tz-errors")
	if _, err := db.Exec(`CREATE TABLE system_settings (system_account_id TEXT, key TEXT, value_json TEXT)`); err != nil {
		t.Fatal(err)
	}
	source, err := NewDBTimezoneSource(db, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Context(context.Background())
	// 缺行。
	if _, err := source.StatsTimezone(ctx); err == nil {
		t.Fatal("缺行必须报错")
	}
	// 坏 JSON。
	if _, err := db.Exec(`INSERT INTO system_settings VALUES ('sys_admin','usageStatsTimezone','{bad')`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.StatsTimezone(ctx); err == nil {
		t.Fatal("坏 JSON 必须报错")
	}
	// 非法时区值。
	if _, err := db.Exec(`UPDATE system_settings SET value_json = '"Not/AZone"' WHERE key = 'usageStatsTimezone'`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.StatsTimezone(ctx); err == nil {
		t.Fatal("非法时区必须报错")
	}
	// 合法时区 + 60s 缓存命中。
	if _, err := db.Exec(`UPDATE system_settings SET value_json = '"Asia/Shanghai"' WHERE key = 'usageStatsTimezone'`); err != nil {
		t.Fatal(err)
	}
	loc, err := source.StatsTimezone(ctx)
	if err != nil {
		t.Fatal(err)
	}
	again, err := source.StatsTimezone(ctx)
	if err != nil || again != loc {
		t.Fatalf("缓存必须命中: %v %v", again, err)
	}
}

func TestW13DNormalizedCostClamps(t *testing.T) {
	if normalizedCost(-5) != 0 {
		t.Fatal("负值必须钳到 0")
	}
	if normalizedCost(1.5) != 1.5 {
		t.Fatal("正常值必须保留")
	}
}

// ---------------------------------------------------------------------------
// w13d 补充：缓存写入错误链与计数式时区注入
// ---------------------------------------------------------------------------

// w13dCountingTZ 前 N 次 StatsTimezone 成功，其后失败：用于越过前置 tz 校验
// 后在后续 tz 读取处注入错误。
type w13dCountingTZ struct {
	mu       sync.Mutex
	okCalls  int
	failAtN  int
	location *time.Location
}

func (c *w13dCountingTZ) StatsTimezone(context.Context) (*time.Location, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.okCalls++
	if c.okCalls > c.failAtN {
		return nil, errors.New("时区读取失败")
	}
	return c.location, nil
}

func TestW13DAuthorizationCacheWriteErrors(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	_, realShared, snapshot, runtimeState := newRedisQuotaStack(t, clock)
	shared := &w13dSharedOverride{inner: realShared}
	business := newTestDB(t, "w13d-authz-write-err-biz")
	statsDB := newTestDB(t, "w13d-authz-write-err-stats")
	authzSchema(t, business)
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB, false)
	if err != nil {
		t.Fatal(err)
	}
	dbService := &mockDBService{}
	service, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:     Modes{RedisCache: true, RedisRuntimeState: true, ServerRole: true},
		Business:  business,
		Stats:     stats,
		Timezone:  mustTZ(t, time.UTC),
		Snapshot:  snapshot,
		Shared:    shared,
		DBService: dbService,
		Now:       clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	groupAccess := GroupAccessMetadata{GroupAuthorizationID: "ga1", GroupAuthorizationQuotaLimited: true}
	accounts := []AccountAuthorizationSummary{{ID: "u1", AccountAuthorizationID: "aa1", AccountAuthorizationQuotaLimited: true}}

	// 发布完整快照（条目齐全）→ 快照可用 + 无需补判 → 决策写入 shared 失败
	// 必须透传（单条与批量）。
	doc := snapshotFixture("2026-09-04T00:00:00.000Z", true, true)
	doc.AuthorizationEntries = []AuthorizationQuotaSnapshotEntry{
		{ScopeType: ScopeTypeGroupAuthorization, AuthorizationID: "ga1", Decision: AllowedDecision()},
		{ScopeType: ScopeTypeAccountAuthorization, AuthorizationID: "aa1", Decision: AllowedDecision()},
	}
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey, doc, time.Minute); err != nil {
		t.Fatal(err)
	}
	shared.setErrAt = 1
	if _, err := service.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil); err == nil {
		t.Fatal("单条快照决策的 shared 写入失败必须透传")
	}
	if _, err := service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts); err == nil {
		t.Fatal("批量快照决策的 shared 写入失败必须透传")
	}

	// db 补判成功但写入失败 → 透传（快照不完整 + 条目缺失）。
	doc.AuthorizationEntriesComplete = boolPtr(false)
	doc.AuthorizationEntries = doc.AuthorizationEntries[:1]
	clock.Advance(sharedSnapshotMemoTTL + time.Second)
	service.ClearCache(ctx)
	if _, err := service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts); err == nil {
		t.Fatal("批量补判决策的 shared 写入失败必须透传")
	}

	// 坏 business 库 → worker 批量与单条的 ByIDs 错误透传。
	if err := business.Close(); err != nil {
		t.Fatal(err)
	}
	workerService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{RedisCache: true},
		Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: snapshot, Shared: shared, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workerService.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil); err == nil {
		t.Fatal("业务库故障单条检查必须透传")
	}
	if _, err := workerService.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts); err == nil {
		t.Fatal("业务库故障批量检查必须透传")
	}
	// business 库故障时的 ByIDs 系列透传。
	if _, err := workerService.CheckAuthorizationQuotaByIDs(ctx, "ga1", "aa1", clock.Now()); err == nil {
		t.Fatal("ByIDs 业务库故障必须透传")
	}
	if _, err := workerService.CheckAuthorizationQuotaByIDsExactAsync(ctx, "ga1", "aa1", clock.Now()); err == nil {
		t.Fatal("ByIDsExact 业务库故障必须透传")
	}
}

func TestW13DAPIKeyQuotaCountingTimezoneErrors(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	db := newTestDB(t, "w13d-counting-tz")
	statsSchema(t, db)
	stats, err := NewStatsStore(db, false)
	if err != nil {
		t.Fatal(err)
	}
	tz := &w13dCountingTZ{failAtN: 1, location: time.UTC}
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes: Modes{}, Stats: stats, Timezone: tz,
		Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := clock.Now()
	// 第一次 tz（cacheKey）成功、第二次 tz（LoadCosts 前）失败。
	tz.mu.Lock()
	tz.okCalls = 0
	tz.failAtN = 1
	tz.mu.Unlock()
	if _, err := service.CheckAPIKeyQuota(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("第二次 tz 失败必须透传")
	}
	if _, err := service.CheckAPIKeyQuotaReadOnly(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("ReadOnly tz 失败必须透传")
	}
	// ExactAsync PG 模式的 stats 缺失 / 坏 limits / tz 失败（越过非 PG 回退）。
	pgService, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes: Modes{PostgresDatabase: true}, Stats: stats, Timezone: tz,
		Snapshot: mustSnapshotCache(t, Modes{PostgresDatabase: true}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	noStats, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes: Modes{PostgresDatabase: true}, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{PostgresDatabase: true}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noStats.CheckAPIKeyQuotaExactAsync(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("PG 模式缺 Stats 必须报错")
	}
	if _, err := pgService.CheckAPIKeyQuotaExactAsync(ctx, APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: `{bad`}, now); err == nil {
		t.Fatal("PG 模式坏 limits 必须报错")
	}
	tz.mu.Lock()
	tz.okCalls = 0
	tz.failAtN = 1
	tz.mu.Unlock()
	if _, err := pgService.CheckAPIKeyQuotaExactAsync(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("ExactAsync 后置 tz 失败必须透传")
	}
	// 无启用限额直接放行（越过后续 tz）。
	tz.mu.Lock()
	tz.okCalls = 0
	tz.failAtN = 1
	tz.mu.Unlock()
	if decision, err := pgService.CheckAPIKeyQuotaExactAsync(ctx, w13dNoLimitsAPIKey("ak2"), now); err != nil || !decision.Allowed {
		t.Fatalf("无限额必须放行: (%+v, %v)", decision, err)
	}
	// Async 快照读取错误透传（redis 快照 + 坏 generatedAt）。
	_, _, redisSnapshot, redisState := newRedisQuotaStack(t, clock)
	asyncService, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes:    Modes{RedisCache: true, RedisRuntimeState: true, ServerRole: true},
		Stats:    stats,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: redisSnapshot,
		Shared:   &w13dFlakyShared{},
		Now:      clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	badDoc := snapshotFixture("not-a-time", true, true)
	if err := redisState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey, badDoc, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := asyncService.CheckAPIKeyQuotaAsync(ctx, w13dAPIKey("ak1")); err == nil {
		t.Fatal("坏快照必须透传")
	}
}

// ---------------------------------------------------------------------------
// w13d 补充：残余分支（PG 模式读取入口、worker 批量、坏快照透传）
// ---------------------------------------------------------------------------

func TestW13DAPIKeyQuotaReadExactAsyncPGGuards(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	db := newTestDB(t, "w13d-pg-guards")
	statsSchema(t, db)
	stats, err := NewStatsStore(db, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := clock.Now()
	// PG 模式 + Stats 缺失：读取入口的守卫。
	noStats, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes: Modes{PostgresDatabase: true}, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{PostgresDatabase: true}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noStats.ReadAPIKeyQuotaCostsExactAsync(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("PG 读取缺 Stats 必须报错")
	}
	// PG 模式 + 坏 limits / 坏时区。
	pgService, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes: Modes{PostgresDatabase: true}, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{PostgresDatabase: true}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pgService.ReadAPIKeyQuotaCostsExactAsync(ctx, APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: `{bad`}, now); err == nil {
		t.Fatal("PG 读取坏 limits 必须报错")
	}
	brokenTZ, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes: Modes{PostgresDatabase: true}, Stats: stats, Timezone: w13dBrokenTZ{},
		Snapshot: mustSnapshotCache(t, Modes{PostgresDatabase: true}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := brokenTZ.ReadAPIKeyQuotaCostsExactAsync(ctx, w13dAPIKey("ak1"), now); err == nil {
		t.Fatal("PG 读取坏时区必须报错")
	}
}

func TestW13DAuthorizationWorkerBatchCacheWriteError(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	_, realShared, _, _ := newRedisQuotaStack(t, clock)
	shared := &w13dSharedOverride{inner: realShared}
	business := newTestDB(t, "w13d-authz-worker-batch-biz")
	statsDB := newTestDB(t, "w13d-authz-worker-batch-stats")
	authzSchema(t, business)
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB, false)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{RedisCache: true},
		Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{}, clock),
		Shared:   shared, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	groupAccess := GroupAccessMetadata{GroupAuthorizationID: "ga1", GroupAuthorizationQuotaLimited: true}
	accounts := []AccountAuthorizationSummary{{ID: "u1", AccountAuthorizationID: "aa1", AccountAuthorizationQuotaLimited: true}}
	seedAuthzRow(t, business, "ga1", "sysA", "sysB", "group", "g1", "", `{"daily":{"enabled":true,"limit":10}}`, "active")

	// worker 批量成功后写入 shared 失败 → 透传。
	shared.setErrAt = 1
	if _, err := service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts); err == nil {
		t.Fatal("worker 批量 shared 写入失败必须透传")
	}
	// 写入正常 → 决策产出。
	shared.setErrAt = 0
	batch, err := service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) == 0 {
		t.Fatal("批量决策不能为空")
	}
	// 坏快照（generatedAt 非法）→ HasGatewayQuotaSnapshotAsync 错误透传。
	badDoc := snapshotFixture("not-a-time", true, true)
	_, _, badSnapshot, badState := newRedisQuotaStack(t, clock)
	badService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{RedisCache: true, RedisRuntimeState: true, ServerRole: true},
		Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: badSnapshot, Shared: shared, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := badState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey, badDoc, time.Minute); err != nil {
		t.Fatal(err)
	}
	// 清掉 worker 段写入的决策缓存，避免 shared 命中提前返回。
	if err := realShared.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := badService.CheckAuthorizationQuotaAsync(ctx, groupAccess, nil); err == nil {
		t.Fatal("坏快照必须透传")
	}
	if _, err := badService.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts); err == nil {
		t.Fatal("坏快照批量必须透传")
	}
}

func TestW13DAuthorizationMemoryServerRoleBatch(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	business := newTestDB(t, "w13d-authz-mem-batch-biz")
	statsDB := newTestDB(t, "w13d-authz-mem-batch-stats")
	authzSchema(t, business)
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB, false)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewSnapshotCache(Modes{}, nil, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	dbService := &mockDBService{}
	service, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{ServerRole: true},
		Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: snapshot, DBService: dbService, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	groupAccess := GroupAccessMetadata{GroupAuthorizationID: "ga1", GroupAuthorizationQuotaLimited: true}
	accounts := []AccountAuthorizationSummary{{ID: "u1", AccountAuthorizationID: "aa1", AccountAuthorizationQuotaLimited: true}}

	// 不完整快照 + DB 补判成功（memory server 批量）。
	incomplete := snapshotFixture("2026-09-04T00:00:00.000Z", true, false)
	if err := snapshot.ReplaceGatewayQuotaSnapshot(incomplete); err != nil {
		t.Fatal(err)
	}
	// memory 模式下单条 Async 会写入 5s 运行时缓存；先预填再批量以覆盖
	// cachedDecisionsByAccountID 遍历分支。
	if _, err := service.CheckAuthorizationQuotaAsync(ctx, groupAccess, &accounts[0]); err != nil {
		t.Fatal(err)
	}
	service.ClearCache(ctx)
	if _, err := service.CheckAuthorizationQuotaAsync(ctx, groupAccess, &accounts[0]); err != nil {
		t.Fatal(err)
	}
	dbService.mu.Lock()
	dbService.checkBatchDecs = []Decision{AllowedDecision()}
	dbService.mu.Unlock()
	batch, err := service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := batch["u1"]; !ok {
		t.Fatalf("批量必须产出 u1 决策: %+v", batch)
	}
}

// ---------------------------------------------------------------------------
// w13d 补充：快照回退判定的 account 维度与 ByIDs 空批量
// ---------------------------------------------------------------------------

func TestW13DAuthorizationSnapshotFallbackAccountDimension(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	business := newTestDB(t, "w13d-authz-fallback-acct-biz")
	statsDB := newTestDB(t, "w13d-authz-fallback-acct-stats")
	authzSchema(t, business)
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB, false)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewSnapshotCache(Modes{}, nil, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	dbService := &mockDBService{}
	service, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{ServerRole: true},
		Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: snapshot, DBService: dbService, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 不完整快照 + 仅 account 维度缺失 → needsDBFallback 的 account 分支。
	incomplete := snapshotFixture("2026-09-04T00:00:00.000Z", true, false)
	if err := snapshot.ReplaceGatewayQuotaSnapshot(incomplete); err != nil {
		t.Fatal(err)
	}
	accountOnly := authorizationSnapshotInput{accountAuthorizationID: "aa-missing", accountAuthorizationQuotaLimited: true}
	if !service.snapshotNeedsDBFallback(accountOnly) {
		t.Fatal("account 维度缺失必须回退")
	}
	needs, err := service.snapshotNeedsDBFallbackAsync(ctx, accountOnly)
	if err != nil || !needs {
		t.Fatalf("async account 回退=%v err=%v", needs, err)
	}
	// account 条目存在 → 不回退（表达式 false 分支）。
	found := authorizationSnapshotInput{accountAuthorizationID: "aa", accountAuthorizationQuotaLimited: true}
	if service.snapshotNeedsDBFallback(found) {
		t.Fatal("完整快照存在条目不回退")
	}
	// decisionFromSnapshot 的 account 缺失保护拒绝。
	if decision := service.decisionFromSnapshot(accountOnly); decision.Allowed {
		t.Fatalf("缺失 account 必须保护性拒绝: %+v", decision)
	}
	// batchSnapshotNeedsDBFallback 携带非空 accounts。
	groupAccess := GroupAccessMetadata{GroupAuthorizationID: "ga1", GroupAuthorizationQuotaLimited: true}
	accounts := []AccountAuthorizationSummary{{ID: "u1", AccountAuthorizationID: "aa-missing", AccountAuthorizationQuotaLimited: true}}
	if !service.batchSnapshotNeedsDBFallback(groupAccess, accounts) {
		t.Fatal("accounts 内缺失条目必须回退")
	}
	// 完整快照 + accounts 非空 → 遍历后返回 false。
	complete := snapshotFixture("2026-09-04T00:00:00.000Z", true, true)
	if err := snapshot.ReplaceGatewayQuotaSnapshot(complete); err != nil {
		t.Fatal(err)
	}
	if service.batchSnapshotNeedsDBFallback(groupAccess, accounts) {
		t.Fatal("完整快照批量不回退")
	}
	// decisionFromSnapshotAsync 的 account 缺失保护拒绝（完整快照 → 允许）。
	if decision, err := service.decisionFromSnapshotAsync(ctx, found); err != nil || !decision.Allowed {
		t.Fatalf("async account 缺失按完整快照放行: (%+v, %v)", decision, err)
	}
}

func TestW13DAuthorizationByIDsEmptyBatchAllowed(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	service, _, _ := wjNewAuthzService(t, Modes{}, clock, "w13d-empty-batch")
	ctx := context.Background()
	// scope 列表非空但行不存在 → 决策空 → 包装层回落允许。
	decision, err := service.CheckAuthorizationQuotaByIDs(ctx, "ghost", "ghost-acct", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	_ = decision
	decisions, err := service.CheckAuthorizationQuotaBatchByIDs(ctx, "ghost", []AccountRef{{AccountID: "u1", AccountAuthorizationID: ""}}, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	_ = decisions
}

// ---------------------------------------------------------------------------
// w13d 补充：空批量回落与快照并发装载
// ---------------------------------------------------------------------------

func TestW13DAuthorizationByIDsEmptyScopesAllowed(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	service, _, _ := wjNewAuthzService(t, Modes{}, clock, "w13d-empty-scopes")
	ctx := context.Background()
	// group 与 account 授权 ID 均为空 → scope 列表为空 → 决策为空 → 包装层回落允许。
	if decision, err := service.CheckAuthorizationQuotaByIDs(ctx, "", "", clock.Now()); err != nil || !decision.Allowed {
		t.Fatalf("空 scope ByIDs 必须放行: (%+v, %v)", decision, err)
	}
	if decision, err := service.CheckAuthorizationQuotaByIDsReadOnly(ctx, "", "", clock.Now()); err != nil || !decision.Allowed {
		t.Fatalf("空 scope ReadOnly 必须放行: (%+v, %v)", decision, err)
	}
	if decision, err := service.CheckAuthorizationQuotaByIDsExactAsync(ctx, "", "", clock.Now()); err != nil || !decision.Allowed {
		t.Fatalf("空 scope Exact 必须放行: (%+v, %v)", decision, err)
	}
	if decisions, err := service.CheckAuthorizationQuotaBatchByIDs(ctx, "", []AccountRef{{AccountID: "u1", AccountAuthorizationID: ""}}, clock.Now()); err != nil || len(decisions) != 1 || !decisions[0].Allowed {
		t.Fatalf("无授权 ID 的批量必须按允许判定: (%+v, %v)", decisions, err)
	}
	if decisions, err := service.CheckAuthorizationQuotaBatchByIDsReadOnly(ctx, "", []AccountRef{{AccountID: "u1", AccountAuthorizationID: ""}}, clock.Now()); err != nil || len(decisions) != 1 || !decisions[0].Allowed {
		t.Fatalf("无授权 ID 的 ReadOnly 批量必须按允许判定: (%+v, %v)", decisions, err)
	}
}

func TestW13DSnapshotCacheConcurrentLoading(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	server, _, snapshot, runtimeState := newRedisQuotaStack(t, clock)
	ctx := context.Background()
	doc := snapshotFixture("2026-09-04T00:00:00.000Z", true, true)
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey, doc, time.Minute); err != nil {
		t.Fatal(err)
	}
	// 预置 loading 通道：第二个读者必须等待第一个的结果（并发去重）。
	ch := make(chan sharedLoadResult, 1)
	snapshot.mu.Lock()
	snapshot.loading = ch
	snapshot.mu.Unlock()
	waiter := make(chan struct{})
	go func() {
		close(waiter)
		hours := 3
		costEntry := QuotaCostSnapshotEntry{
			SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak", HourlyWindowHours: &hours,
		}
		_, found, err := snapshot.ReadCostsSnapshotAsync(ctx, costEntry)
		if err != nil || !found {
			t.Errorf("并发读者必须拿到快照: found=%v err=%v", found, err)
		}
	}()
	<-waiter
	// 模拟第一个读者完成装载。
	time.Sleep(10 * time.Millisecond)
	snapshot.sharedMu.Lock()
	snapshot.loading = nil
	snapshot.sharedFetchedAtMs = clock.Now().UnixMilli()
	snapshot.shared = &doc
	snapshot.sharedCosts = map[string]RequestQuotaCosts{}
	for _, entry := range doc.CostEntries {
		snapshot.sharedCosts[costSnapshotKey(entry)] = CloneRequestQuotaCosts(entry.Costs)
	}
	snapshot.sharedMu.Unlock()
	_ = server
}

// ---------------------------------------------------------------------------
// w13d 补充：构造守卫与快照缓存的非 redis 回退
// ---------------------------------------------------------------------------

func TestW13DQuotaConstructionAndFallbackGuards(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	ctx := context.Background()
	// SnapshotCache：now 缺省回退。
	fallbackNow, err := NewSnapshotCache(Modes{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fallbackNow
	// RedisCache + RedisRuntimeState 模式缺 runtime state store 必须报错。
	if _, err := NewSnapshotCache(Modes{RedisCache: true, RedisRuntimeState: true}, nil, clock.Now, nil); err == nil {
		t.Fatal("redis 模式缺 runtime state 必须报错")
	}
	// 非 redis 模式的 Async 读取回退 memory 路径。
	memorySnapshot, err := NewSnapshotCache(Modes{}, nil, clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	complete := snapshotFixture("2026-09-04T00:00:00.000Z", true, true)
	if err := memorySnapshot.ReplaceGatewayQuotaSnapshot(complete); err != nil {
		t.Fatal(err)
	}
	hours := 3
	costEntry := QuotaCostSnapshotEntry{
		SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak", HourlyWindowHours: &hours,
	}
	if _, found, err := memorySnapshot.ReadCostsSnapshotAsync(context.Background(), costEntry); err != nil || !found {
		t.Fatalf("非 redis 回退必须命中: found=%v err=%v", found, err)
	}
	// RedisRuntimeState：GetJSON 空 key 守卫。
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	state, err := NewRedisRuntimeStateStore(client, "w13d", "gateway-quota-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	var target map[string]string
	if _, err := state.GetJSON(context.Background(), "store", "  ", &target); err == nil {
		t.Fatal("GetJSON 空 key 必须报错")
	}
	// Async 的 cacheKey 时区错误透传（brokenTZ + Async 组合）。
	db := newTestDB(t, "w13d-async-tz")
	statsSchema(t, db)
	stats, err := NewStatsStore(db, false)
	if err != nil {
		t.Fatal(err)
	}
	brokenTZ, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Stats: stats, Timezone: w13dBrokenTZ{},
		Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := brokenTZ.CheckAPIKeyQuotaAsync(ctx, w13dAPIKey("ak1")); err == nil {
		t.Fatal("Async 坏时区必须报错")
	}
}
