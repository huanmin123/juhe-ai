package gatewayquota

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestWJSnapshotCacheMemoryAsyncFallback 固定内存驱动下各 async 读法的
// 同步回退语义：行为与同步版本完全一致。
func TestWJSnapshotCacheMemoryAsyncFallback(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	cache, err := NewSnapshotCache(Modes{}, nil, clock.Now, nil)
	if err != nil {
		t.Fatalf("NewSnapshotCache: %v", err)
	}
	ctx := context.Background()

	// 空缓存：Has/Complete/Incomplete 各 async 入口。
	if ok, err := cache.HasGatewayQuotaSnapshotAsync(ctx); err != nil || ok {
		t.Fatalf("空缓存 Has = (%v, %v)", ok, err)
	}
	if ok, err := cache.IsCostSnapshotCompleteAsync(ctx); err != nil || ok {
		t.Fatalf("空缓存 IsCostSnapshotCompleteAsync = (%v, %v)", ok, err)
	}
	if ok, err := cache.IsAuthorizationSnapshotCompleteAsync(ctx); err != nil || ok {
		t.Fatalf("空缓存 IsAuthorizationSnapshotCompleteAsync = (%v, %v)", ok, err)
	}
	if ok, err := cache.IsCostSnapshotIncompleteAsync(ctx); err != nil || ok {
		t.Fatalf("空缓存 IsCostSnapshotIncompleteAsync = (%v, %v)", ok, err)
	}
	if ok, err := cache.IsAuthorizationSnapshotIncompleteAsync(ctx); err != nil || ok {
		t.Fatalf("空缓存 IsAuthorizationSnapshotIncompleteAsync = (%v, %v)", ok, err)
	}
	if _, ok, err := cache.ReadAuthorizationSnapshotAsync(ctx, ScopeTypeGroupAuthorization, "ga"); err != nil || ok {
		t.Fatalf("空缓存 ReadAuthorizationSnapshotAsync = (%v, %v)", ok, err)
	}
	info := cache.SnapshotRuntime()
	if info.GeneratedAt != "" || info.CostEntryCount != 0 {
		t.Fatalf("空缓存 SnapshotRuntime = %+v", info)
	}

	// 装载快照后回退读必须命中；快照不完整通过 async 可见。
	doc := snapshotFixture("2026-09-04T00:00:00.000Z", false, true)
	doc.CostEntries = []QuotaCostSnapshotEntry{{
		SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak",
		Costs: RequestQuotaCosts{Total: 7},
	}}
	if err := cache.ReplaceGatewayQuotaSnapshot(doc); err != nil {
		t.Fatalf("replace: %v", err)
	}
	costs, ok, err := cache.ReadCostsSnapshotAsync(ctx, QuotaCostSnapshotEntry{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak"})
	if err != nil || !ok || costs.Total != 7 {
		t.Fatalf("memory ReadCostsSnapshotAsync = (%+v, %v, %v)", costs, ok, err)
	}
	decision, ok, err := cache.ReadAuthorizationSnapshotAsync(ctx, ScopeTypeGroupAuthorization, "ga")
	if err != nil || !ok || decision.Allowed {
		t.Fatalf("memory ReadAuthorizationSnapshotAsync = (%+v, %v, %v)", decision, ok, err)
	}
	if ok, err := cache.HasGatewayQuotaSnapshotAsync(ctx); err != nil || !ok {
		t.Fatalf("Has = (%v, %v)", ok, err)
	}
	if ok, err := cache.IsCostSnapshotIncompleteAsync(ctx); err != nil || !ok {
		t.Fatalf("IsCostSnapshotIncompleteAsync = (%v, %v)", ok, err)
	}
	info = cache.SnapshotRuntime()
	if info.GeneratedAt != "2026-09-04T00:00:00.000Z" || info.CostEntryCount != 1 || info.CostEntriesComplete {
		t.Fatalf("SnapshotRuntime = %+v", info)
	}
}

// TestWJSnapshotCacheRedisMissingDocAndErrors 固定 Redis 驱动在文档缺失与
// 传输故障下的行为。
func TestWJSnapshotCacheRedisMissingDocAndErrors(t *testing.T) {
	server, client := wjNewMiniredis(t)
	runtimeState, err := NewRedisRuntimeStateStore(client, "dev", GatewayQuotaSnapshotRuntimeStateStoreName)
	if err != nil {
		t.Fatalf("NewRedisRuntimeStateStore: %v", err)
	}
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	logs := &logRecorder{}
	cache, err := NewSnapshotCache(Modes{RedisCache: true, RedisRuntimeState: true}, runtimeState, clock.Now, logs.hook)
	if err != nil {
		t.Fatalf("NewSnapshotCache: %v", err)
	}
	ctx := context.Background()

	// 文档缺失：授权快照不完整必须回落到本进程失效水印（authzInvalidated）。
	if ok, err := cache.IsAuthorizationSnapshotIncompleteAsync(ctx); err != nil || ok {
		t.Fatalf("无文档且未失效 = (%v, %v)", ok, err)
	}
	if err := cache.InvalidateAuthorizationQuotaSnapshot(nil); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if ok, err := cache.IsAuthorizationSnapshotIncompleteAsync(ctx); err != nil || !ok {
		t.Fatalf("无文档且已失效 = (%v, %v)", ok, err)
	}
	if _, ok, err := cache.ReadAuthorizationSnapshotAsync(ctx, ScopeTypeGroupAuthorization, "ga"); err != nil || ok {
		t.Fatalf("无文档读取 = (%v, %v)", ok, err)
	}

	// 传输故障按契约降级为“无快照”（不传播错误，由 DB service 补判兜底）。
	clock.Advance(2 * time.Second)
	server.Close()
	if ok, err := cache.HasGatewayQuotaSnapshotAsync(ctx); err != nil || ok {
		t.Fatalf("传输故障后 Has 必须降级为无快照 = (%v, %v)", ok, err)
	}
	if _, _, err := cache.ReadAuthorizationSnapshotAsync(ctx, ScopeTypeGroupAuthorization, "ga"); err != nil {
		t.Fatalf("传输故障后 ReadAuthz 必须降级: %v", err)
	}
	if _, err := cache.IsCostSnapshotIncompleteAsync(ctx); err != nil {
		t.Fatalf("传输故障后 IsCostIncomplete 必须降级: %v", err)
	}
}

// TestWJSnapshotCacheRedisCorruptDoc 固定共享快照文档校验失败的传播：
// generatedAt 非法时读取必须报错（区别于传输故障的降级）。
func TestWJSnapshotCacheRedisCorruptDoc(t *testing.T) {
	_, client := wjNewMiniredis(t)
	runtimeState, err := NewRedisRuntimeStateStore(client, "dev", GatewayQuotaSnapshotRuntimeStateStoreName)
	if err != nil {
		t.Fatalf("NewRedisRuntimeStateStore: %v", err)
	}
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	cache, err := NewSnapshotCache(Modes{RedisCache: true, RedisRuntimeState: true}, runtimeState, clock.Now, nil)
	if err != nil {
		t.Fatalf("NewSnapshotCache: %v", err)
	}
	ctx := context.Background()
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey,
		snapshotFixture("corrupt", true, true), time.Hour); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := cache.HasGatewayQuotaSnapshotAsync(ctx); err == nil {
		t.Fatal("损坏文档 Has 必须报错")
	}
	if _, _, err := cache.ReadAuthorizationSnapshotAsync(ctx, ScopeTypeGroupAuthorization, "ga"); err == nil {
		t.Fatal("损坏文档 ReadAuthz 必须报错")
	}
	if _, err := cache.IsAuthorizationSnapshotIncompleteAsync(ctx); err == nil {
		t.Fatal("损坏文档 IsAuthzIncomplete 必须报错")
	}
	if _, ok, err := cache.ReadCostsSnapshotAsync(ctx, QuotaCostSnapshotEntry{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak"}); err == nil {
		t.Fatal("损坏文档 ReadCosts 必须报错")
	} else if ok {
		t.Fatal("损坏文档 ReadCosts 不得命中")
	}
}

// TestWJSnapshotCacheRedisConcurrentLoad 受控验证共享快照的在途装载去重：
// 已有装载在途时，后续读取必须等待同一装载结果而不是并发二次装载。
func TestWJSnapshotCacheRedisConcurrentLoad(t *testing.T) {
	_, client := wjNewMiniredis(t)
	runtimeState, err := NewRedisRuntimeStateStore(client, "dev", GatewayQuotaSnapshotRuntimeStateStoreName)
	if err != nil {
		t.Fatalf("NewRedisRuntimeStateStore: %v", err)
	}
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	cache, err := NewSnapshotCache(Modes{RedisCache: true, RedisRuntimeState: true}, runtimeState, clock.Now, nil)
	if err != nil {
		t.Fatalf("NewSnapshotCache: %v", err)
	}
	ctx := context.Background()
	doc := snapshotFixture("2026-09-04T00:00:00.000Z", true, true)
	doc.CostEntries = []QuotaCostSnapshotEntry{{
		SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak",
		Costs: RequestQuotaCosts{Total: 9},
	}}

	// 模拟在途装载：预置 loading 通道；读者 goroutine 将进入等待分支，
	// 主 goroutine 作为装载者完成 memo 填充并投递结果。
	loading := make(chan sharedLoadResult, 1)
	cache.sharedMu.Lock()
	cache.loading = loading
	cache.sharedMu.Unlock()

	readerDone := make(chan sharedLoadResult, 1)
	go func() {
		snapshot, err := cache.readSharedGatewayQuotaSnapshot(ctx)
		readerDone <- sharedLoadResult{snapshot: snapshot, err: err}
	}()

	result, err := cache.replaceSharedGatewayQuotaSnapshotMemo(&doc, true)
	if err != nil {
		t.Fatalf("装载 memo: %v", err)
	}
	cache.sharedMu.Lock()
	cache.loading = nil
	cache.sharedMu.Unlock()
	loading <- sharedLoadResult{snapshot: result.snapshot}

	got := <-readerDone
	if got.err != nil || got.snapshot == nil || got.snapshot.GeneratedAt != doc.GeneratedAt {
		t.Fatalf("在途装载等待必须返回装载结果 = (%+v, %v)", got.snapshot, got.err)
	}

	costs, ok, err := cache.ReadCostsSnapshotAsync(ctx, QuotaCostSnapshotEntry{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak"})
	if err != nil || !ok || costs.Total != 9 {
		t.Fatalf("装载完成后读取必须命中 = (%+v, %v, %v)", costs, ok, err)
	}

	// 多个读取者并发读取（此时 memo 已就绪）必须全部一致命中。
	const readers = 6
	var wg sync.WaitGroup
	errs := make([]error, readers)
	for i := range readers {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			costs, ok, err := cache.ReadCostsSnapshotAsync(ctx, QuotaCostSnapshotEntry{SystemAccountID: "sys", ScopeType: ScopeTypeAPIKey, ScopeID: "ak"})
			if err != nil {
				errs[slot] = err
				return
			}
			if !ok || costs.Total != 9 {
				errs[slot] = errors.New("并发读取必须一致命中 9")
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("reader %d: %v", i, err)
		}
	}
}

// TestWJSnapshotSharedReadGuards 覆盖共享读取的入口守卫：非 redis-runtime
// 组合直接返回无快照；Redis 模式下空授权 ID 直接 miss。
func TestWJSnapshotSharedReadGuards(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	// RedisRuntimeState 关闭：readShared 直接短路返回 nil（无错误）。
	runtimeOnly, err := NewSnapshotCache(Modes{RedisCache: true}, nil, clock.Now, nil)
	if err != nil {
		t.Fatalf("NewSnapshotCache: %v", err)
	}
	if snapshot, err := runtimeOnly.readSharedGatewayQuotaSnapshot(context.Background()); snapshot != nil || err != nil {
		t.Fatalf("非 runtime 驱动 readShared = (%v, %v)", snapshot, err)
	}
	// Redis 模式空授权 ID：直接 miss，不读共享文档。
	_, client := wjNewMiniredis(t)
	runtimeState, err := NewRedisRuntimeStateStore(client, "dev", GatewayQuotaSnapshotRuntimeStateStoreName)
	if err != nil {
		t.Fatalf("NewRedisRuntimeStateStore: %v", err)
	}
	redisCache, err := NewSnapshotCache(Modes{RedisCache: true, RedisRuntimeState: true}, runtimeState, clock.Now, nil)
	if err != nil {
		t.Fatalf("NewSnapshotCache: %v", err)
	}
	if _, ok, err := redisCache.ReadAuthorizationSnapshotAsync(context.Background(), ScopeTypeGroupAuthorization, ""); err != nil || ok {
		t.Fatalf("空授权 ID = (%v, %v)", ok, err)
	}
}

// TestWJAuthzBatchServerRoleSnapshotSecondLoop 固定 server 角色（内存快照）
// 批量判定的兜底循环：两个账户共享同一授权 ID、快照完整可判时，重复
// cacheKey 的账户仍必须得到快照决策并被写入运行时缓存。
func TestWJAuthzBatchServerRoleSnapshotSecondLoop(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	service, _, _ := wjNewAuthzService(t, Modes{ServerRole: true}, clock, "loop2")
	ctx := context.Background()
	groupAccess := GroupAccessMetadata{GroupAuthorizationID: "ga1", GroupAuthorizationQuotaLimited: true}
	accounts := []AccountAuthorizationSummary{
		{ID: "u1", AccountAuthorizationID: "aa1", AccountAuthorizationQuotaLimited: true},
		{ID: "u3", AccountAuthorizationID: "aa1", AccountAuthorizationQuotaLimited: true},
	}
	// 完整快照且无任何条目：两个账户都按快照允许（受限但快照完整 → 不保护拒绝）。
	doc := snapshotFixture("2026-09-04T00:00:00.000Z", true, true)
	if err := service.snapshot.ReplaceGatewayQuotaSnapshot(doc); err != nil {
		t.Fatalf("replace: %v", err)
	}
	output, err := service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if !output["u1"].Allowed || !output["u3"].Allowed {
		t.Fatalf("完整快照下共享授权账户必须都允许: %+v", output)
	}
}

// TestWJAuthzBatchAsyncAllCached 固定批量入口的全缓存命中短路：所有账户
// 的运行时缓存都命中时必须直接返回，不进入缺失装载。
func TestWJAuthzBatchAsyncAllCached(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	service, business, statsDB := wjNewAuthzService(t, Modes{}, clock, "allcached")
	ctx := context.Background()
	seedAuthzRow(t, business, "ga1", "sysA", "sysB", "group", "g1", "", `{"daily":{"enabled":true,"limit":10}}`, "active")
	seedCost(t, statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "total_cost_usd"},
		[]any{"sysA", "group_authorization", "ga1", "2026-09-04", 1})
	groupAccess := GroupAccessMetadata{GroupAuthorizationID: "ga1"}
	accounts := []AccountAuthorizationSummary{{ID: "u1"}, {ID: "u2"}}

	first, err := service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts)
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}
	// 第二次调用全部命中内存缓存；超库后仍保持放行以证明未重新装载。
	if _, err := statsDB.Exec(`UPDATE usage_stats_daily SET total_cost_usd = 99 WHERE scope_id = 'ga1'`); err != nil {
		t.Fatalf("update: %v", err)
	}
	second, err := service.CheckAuthorizationQuotaBatchAsync(ctx, groupAccess, accounts)
	if err != nil {
		t.Fatalf("second batch: %v", err)
	}
	for _, accountID := range []string{"u1", "u2"} {
		if !first[accountID].Allowed || !second[accountID].Allowed {
			t.Fatalf("%s 决策不符: first=%+v second=%+v", accountID, first[accountID], second[accountID])
		}
	}
}

// TestWJAPIKeyExactAsyncSharedCacheHit 固定精确异步入口的共享缓存命中：
// 命中后直接返回缓存决策，不再批量装载成本。
func TestWJAPIKeyExactAsyncSharedCacheHit(t *testing.T) {
	db := newTestDB(t, "wj-apikey-sharedhit")
	statsSchema(t, db)
	stats, err := NewStatsStore(db, false)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	clock := newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	denied := newCachedDecision(DeniedDecision(APIKeyQuotaExceededMessage), clock.Now().UnixMilli())
	shared := &wjMockShared{getFound: true, getValue: denied}
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes:    Modes{PostgresDatabase: true, RedisCache: true},
		Stats:    stats,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{}, clock),
		Shared:   shared,
		Now:      clock.Now,
	})
	if err != nil {
		t.Fatalf("NewAPIKeyQuotaService: %v", err)
	}
	ctx := context.Background()
	apiKey := APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: testQuotaLimits}
	decision, err := service.CheckAPIKeyQuotaExactAsync(ctx, apiKey, clock.Now())
	if err != nil || decision.Allowed || decision.Message != APIKeyQuotaExceededMessage {
		t.Fatalf("shared 命中必须返回缓存拒绝: (%+v, %v)", decision, err)
	}
	if shared.getCalls != 1 {
		t.Fatalf("必须读一次 shared cache, calls=%d", shared.getCalls)
	}
}

// TestWJRedisSharedCacheInjectedErrors 用 miniredis 故障注入覆盖共享缓存
// 各写路径的错误传播（ZADD/PEXPIRE/ZCard/Del 等）。
func TestWJRedisSharedCacheInjectedErrors(t *testing.T) {
	server, client := wjNewMiniredis(t)
	cache, err := NewRedisSharedCache(client, "dev", "wj-injected")
	if err != nil {
		t.Fatalf("NewRedisSharedCache: %v", err)
	}
	ctx := context.Background()

	// 正常写入一次建立 version，再注入命令错误。
	if err := cache.Set(ctx, "k", CachedDecision{Allowed: true}, time.Minute); err != nil {
		t.Fatalf("seed Set: %v", err)
	}
	server.SetError("injected failure")
	if err := cache.Set(ctx, "k2", CachedDecision{Allowed: true}, time.Minute); err == nil {
		t.Fatal("注入错误后 Set 必须报错")
	}
	if err := cache.Clear(ctx); err == nil {
		t.Fatal("注入错误后 Clear 必须报错")
	}
	if _, err := cache.Get(ctx, "k", &CachedDecision{}); err == nil {
		t.Fatal("注入错误后 Get 必须报错")
	}
	server.SetError("")
	// 恢复后 Clear 成功并轮换 version，旧值不可达。
	if err := cache.Clear(ctx); err != nil {
		t.Fatalf("恢复后 Clear: %v", err)
	}
	if found, err := cache.Get(ctx, "k", &CachedDecision{}); err != nil || found {
		t.Fatalf("Clear 后读取 = (%v, %v)", found, err)
	}
}

// TestWJCostsMissingProjectionTables 固定各投影缺失时 LoadCosts 的逐窗口
// 错误传播：任何一路读取失败都不得静默当作零成本。
func TestWJCostsMissingProjectionTables(t *testing.T) {
	tests := []struct {
		name      string
		drop      string
		hasHourly bool
	}{
		{name: "totals 缺失", drop: "usage_stats_totals"},
		{name: "daily 缺失", drop: "usage_stats_daily"},
		{name: "weekly 缺失", drop: "usage_stats_weekly"},
		{name: "monthly 缺失", drop: "usage_stats_monthly"},
		{name: "hourly 缺失", drop: "usage_quota_hourly_windows", hasHourly: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newTestDB(t, "wj-drop")
			statsSchema(t, db)
			stats, err := NewStatsStore(db, false)
			if err != nil {
				t.Fatalf("NewStatsStore: %v", err)
			}
			if _, err := db.Exec("DROP TABLE " + tt.drop); err != nil {
				t.Fatalf("drop: %v", err)
			}
			now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
			_, err = stats.LoadCosts(context.Background(), CostInput{
				SystemAccountID: "s", ScopeType: "api_key", ScopeID: "k", Now: now, HasHourlyWindow: tt.hasHourly, HourlyWindowHours: 3,
			}, time.UTC)
			if err == nil {
				t.Fatalf("%s 缺失必须报错", tt.drop)
			}
		})
	}
}

// TestWJCostsBatchNilContext 固定 nil ctx 的 Background 回退。
func TestWJCostsBatchNilContext(t *testing.T) {
	db := newTestDB(t, "wj-nilctx")
	statsSchema(t, db)
	stats, err := NewStatsStore(db, false)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	// LoadCosts / LoadCostsBatch 的 nil ctx 回退由内部 ensure 分支处理。
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	if _, err := stats.LoadCosts(nil, CostInput{SystemAccountID: "s", ScopeType: "api_key", ScopeID: "k", Now: now}, time.UTC); err != nil {
		t.Fatalf("LoadCosts nil ctx: %v", err)
	}
	if _, err := stats.LoadCostsBatch(nil, []CostInput{{SystemAccountID: "s", ScopeType: "api_key", ScopeID: "k", Now: now}}, time.UTC); err != nil {
		t.Fatalf("LoadCostsBatch nil ctx: %v", err)
	}
}

// TestWJInflightEdgeInputs 固定在途额度的输入边界：nil 预约、负释放延迟、
// 无估值器放行与非法 limits 传播。
func TestWJInflightEdgeInputs(t *testing.T) {
	// nil receiver 的 Complete 必须安全。
	var reservation *InflightReservation
	reservation.Complete()

	// estimator 为 nil：估值不可得 → 直接放行（不预约）。
	service, err := NewInflightQuotaService(InflightQuotaConfig{})
	if err != nil {
		t.Fatalf("NewInflightQuotaService: %v", err)
	}
	decision, err := service.ReserveGatewayCost(context.Background(), GatewayReserveInput{
		APIKey:   APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: testQuotaLimits},
		Estimate: EstimateRequestInput{RawBodyBytes: 400},
	})
	if err != nil || !decision.Allowed || decision.HasEstimatedCostUsd {
		t.Fatalf("无估值器必须放行: (%+v, %v)", decision, err)
	}
	// 估值 <= 0 同样放行。
	estimator := &wjMockEstimator{cost: 0, ok: true}
	service2, err := NewInflightQuotaService(InflightQuotaConfig{Estimator: estimator})
	if err != nil {
		t.Fatalf("NewInflightQuotaService: %v", err)
	}
	decision, err = service2.ReserveGatewayCost(context.Background(), GatewayReserveInput{
		APIKey:   APIKeyRow{ID: "ak", SystemAccountID: "sys", QuotaLimitsJSON: testQuotaLimits},
		Estimate: EstimateRequestInput{RawBodyBytes: 400},
	})
	if err != nil || !decision.Allowed {
		t.Fatalf("零估值必须放行: (%+v, %v)", decision, err)
	}
	// 非法 limits JSON → 错误传播。
	if _, err := service2.ReserveGatewayCost(context.Background(), GatewayReserveInput{
		APIKey:   APIKeyRow{ID: "ak", QuotaLimitsJSON: `{invalid`},
		Estimate: EstimateRequestInput{RawBodyBytes: 400},
	}); err == nil {
		t.Fatal("非法 limits JSON 必须报错")
	}
	// 无启用限制 → 放行。
	decision, err = service2.ReserveGatewayCost(context.Background(), GatewayReserveInput{
		APIKey:   APIKeyRow{ID: "free"},
		Estimate: EstimateRequestInput{RawBodyBytes: 400},
	})
	if err != nil || !decision.Allowed {
		t.Fatalf("无限制必须放行: (%+v, %v)", decision, err)
	}

	// 负释放延迟必须被钳制为立即释放（覆盖 delay<0 分支）。
	timers := &manualTimerQueue{}
	negative := -5
	service3, err := NewInflightQuotaService(InflightQuotaConfig{Timer: timers.schedule})
	if err != nil {
		t.Fatalf("NewInflightQuotaService: %v", err)
	}
	reserve := service3.Reserve(ReserveInput{
		APIKeyID:         "ak",
		Limits:           mustLimits(t, testQuotaLimits),
		CurrentCosts:     RequestQuotaCosts{},
		EstimatedCostUsd: 1,
		ReleaseDelayMs:   &negative,
	})
	if !reserve.Allowed || reserve.Reservation == nil {
		t.Fatalf("预约必须成功: %+v", reserve)
	}
	// 泄漏计时器调度 1 次（30min）；Complete 后 stop 并调度立即释放。
	if timers.len() != 1 {
		t.Fatalf("泄漏计时器数量 = %d", timers.len())
	}
	reserve.Reservation.Complete()
	// Complete 停掉泄漏计时器并调度立即释放：队列共 2 个计时器，其中只有
	// 立即释放的那个会真正触发（泄漏计时器已被 stop）。
	if timers.len() != 2 {
		t.Fatalf("Complete 后计时器数量 = %d, 期望 2", timers.len())
	}
	if fired := timers.fireAll(); fired != 1 {
		t.Fatalf("只有释放计时器应触发, fired=%d", fired)
	}
	if states := service3.Snapshot(); len(states) != 0 {
		t.Fatalf("释放后必须为空: %+v", states)
	}
}

func mustLimits(t *testing.T, raw string) RequestQuotaLimits {
	t.Helper()
	limits, err := ParseRequestQuotaLimitsJSON(raw)
	if err != nil {
		t.Fatalf("parse limits: %v", err)
	}
	return limits
}
