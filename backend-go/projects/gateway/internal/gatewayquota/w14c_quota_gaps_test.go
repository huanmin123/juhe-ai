package gatewayquota

// w14c 覆盖率补齐：针对 w13d/wj 之后仍缺失的分支。
// 已确认的不可达（防御性）守卫，本文件登记、不强行构造：
//   - rediscache.go NewRedisSharedCache 第 2/3 个 RedisNamespacedKey 错误分支
//     （三个调用使用同一 namespace，首个失败即返回）；
//   - rfc3339.go fractionNanos 的 Atoi 错误分支（正则限制 1-9 位小数，填充后
//     必然可解析）、parseRfc3339Instant 的 fractionNanos !ok 与归一化分支
//     requiredRfc3339Instant 规范化，规范串必可复解析）；
//   - apikeyquota.go ServerRole 非 Redis 分支内的
//     setCacheEntryAsync 错误返回（RedisCache=false 时恒返回 nil）；
//   - inflight.go normalizedCost 的 ParseFloat 失败（FormatFloat 输出必可回解）；
//   - modes.go recencyList.back 的空链表分支（调用点保证非空）。

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// w14cHookedClient 建一个带按命令失败注入 hook 的 redis client。
// fail 中 key 为命令名（如 "get"），value 为第几次该命令调用时失败（1 起）。
func w14cHookedClient(t *testing.T, server *miniredis.Miniredis, fail map[string]int, onCmd func(cmd redis.Cmder)) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	counts := map[string]int{}
	client.AddHook(w14cCmdHook{fail: fail, counts: counts, onCmd: onCmd})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

type w14cCmdHook struct {
	fail   map[string]int
	counts map[string]int
	onCmd  func(cmd redis.Cmder)
}

func (h w14cCmdHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h w14cCmdHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		name := cmd.Name()
		h.counts[name]++
		if h.onCmd != nil {
			h.onCmd(cmd)
		}
		if nth, ok := h.fail[name]; ok && h.counts[name] == nth {
			return errors.New("w14c 注入故障")
		}
		return next(ctx, cmd)
	}
}

func (h w14cCmdHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// w14cIsSetNX 识别 SetNX 命令（底层 argv 为 set ... nx）。
func w14cIsSetNX(cmd redis.Cmder) bool {
	args := cmd.Args()
	if len(args) == 0 || args[0] != "set" {
		return false
	}
	last, _ := args[len(args)-1].(string)
	return last == "nx"
}

// TestW14CRedisSharedCacheCommandFaults 按命令注入传输故障，覆盖 Set/Get/Clear
// 的中间命令错误分支与版本竞态回读路径。
func TestW14CRedisSharedCacheCommandFaults(t *testing.T) {
	server := miniredis.RunT(t)
	ctx := context.Background()

	newCache := func(fail map[string]int, onCmd func(cmd redis.Cmder)) (*redis.Client, *RedisSharedCache) {
		client := w14cHookedClient(t, server, fail, onCmd)
		shared, err := NewRedisSharedCache(client, "w14c", "gap-cache")
		if err != nil {
			t.Fatal(err)
		}
		return client, shared
	}
	// rawClient 不挂钩子，专供种子写入，避免被失败注入拦截。
	rawClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rawClient.Close() })
	seedVersion := func(shared *RedisSharedCache) {
		if err := rawClient.Set(ctx, shared.versionKey, "w14c-version", 0).Err(); err != nil {
			t.Fatal(err)
		}
	}
	overflowIndex := func(shared *RedisSharedCache) {
		t.Helper()
		members := make([]redis.Z, 0, apiKeyQuotaCacheMax+2)
		for i := 0; i < apiKeyQuotaCacheMax+2; i++ {
			members = append(members, redis.Z{Score: float64(i), Member: shared.keyPrefix + "w14c-overflow-" + strconv.Itoa(i)})
		}
		if err := rawClient.ZAdd(ctx, shared.indexKeyPrefix+"w14c-version", members...).Err(); err != nil {
			t.Fatal(err)
		}
	}

	// Set 中间命令逐一失败：version Get 命中后依次走 SET/ZADD/PEXPIRE/ZCARD。
	for _, fail := range []map[string]int{
		{"set": 1}, {"zadd": 1}, {"pexpire": 1}, {"zcard": 1},
	} {
		_, shared := newCache(fail, nil)
		seedVersion(shared)
		if err := shared.Set(ctx, "k", map[string]string{"a": "b"}, time.Minute); err == nil {
			t.Fatalf("fail=%v 必须透传错误", fail)
		}
	}

	// Get：版本读命中后读值命令失败（Get 内第 2 个、客户端第 3 个 get）。
	_, shared := newCache(map[string]int{"get": 3}, nil)
	seedVersion(shared)
	if err := shared.Set(ctx, "k", map[string]string{"a": "b"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := shared.Get(ctx, "k", &map[string]string{}); err == nil {
		t.Fatal("Get 传输错误必须透传")
	}

	// namespaceVersion：SetNX 竞态失败后回读当前版本（inserted=false 分支）。
	// 前序子测试已在服务端创建版本键，先清理让版本缺失、SetNX 真正执行。
	if err := rawClient.Del(ctx, shared.versionKey).Err(); err != nil {
		t.Fatal(err)
	}
	_, shared = newCache(nil, func(cmd redis.Cmder) { // 共享外层声明
		if w14cIsSetNX(cmd) {
			if err := rawClient.Set(ctx, shared.versionKey, "w14c-raced", 0).Err(); err != nil {
				t.Fatal(err)
			}
		}
	})
	if err := shared.Set(ctx, "k", map[string]string{"a": "b"}, time.Minute); err != nil {
		t.Fatalf("SetNX 竞态回读必须成功: %v", err)
	}

	// namespaceVersion：SetNX 自身失败（注入后置错误）。
	// 竞态子测试残留了版本键，先清理让版本缺失、SetNX 真正执行。
	if err := rawClient.Del(ctx, shared.versionKey).Err(); err != nil {
		t.Fatal(err)
	}
	_, shared = newCache(nil, func(cmd redis.Cmder) { // 共享外层声明
		if w14cIsSetNX(cmd) {
			server.SetError("w14c SetNX 故障")
		}
	})
	if err := shared.Set(ctx, "k", map[string]string{}, time.Minute); err == nil {
		t.Fatal("SetNX 失败必须透传")
	}
	server.SetError("")

	// Clear：索引读取失败、索引成员删除失败（第 1 个 del）与索引键删除失败
	// （第 2 个 del）。
	for _, fail := range []map[string]int{{"zrange": 1}, {"del": 1}, {"del": 2}} {
		_, shared := newCache(fail, nil)
		if err := shared.Set(ctx, "k", map[string]string{"a": "b"}, time.Minute); err != nil {
			t.Fatal(err)
		}
		if err := shared.Clear(ctx); err == nil {
			t.Fatalf("fail=%v 必须透传", fail)
		}
	}

	// Set 溢出裁剪：索引成员超过 apiKeyQuotaCacheMax 后触发 ZRange/Del/ZRem。
	_, shared = newCache(nil, nil)
	seedVersion(shared)
	overflowIndex(shared)
	if err := shared.Set(ctx, "w14c-fresh", map[string]string{"a": "b"}, time.Minute); err != nil {
		t.Fatalf("溢出裁剪必须成功: %v", err)
	}
	// 溢出路径中的 ZRange / Del / ZRem 错误分支。
	for _, fail := range []map[string]int{{"zrange": 1}, {"del": 1}, {"zrem": 1}} {
		_, shared := newCache(fail, nil)
		seedVersion(shared)
		overflowIndex(shared)
		if err := shared.Set(ctx, "w14c-fresh", map[string]string{}, time.Minute); err == nil {
			t.Fatalf("fail=%v 必须透传错误", fail)
		}
	}
}

// TestW14CRedisRuntimeStateSetJSONMarshal 覆盖 SetJSON 的编码错误分支。
func TestW14CRedisRuntimeStateSetJSONMarshal(t *testing.T) {
	server := miniredis.RunT(t)
	client := w14cHookedClient(t, server, nil, nil)
	state, err := NewRedisRuntimeStateStore(client, "w14c", "gap-state")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetJSON(context.Background(), "store", "k", make(chan int), time.Minute); err == nil {
		t.Fatal("不可编码值必须报错")
	}
}

// TestW14CSnapshotCacheNilAndAsyncBranches 覆盖 SnapshotCache 的 Redis 模式
// 内存恒定值、空快照异步读取与坏文档错误透传。
func TestW14CSnapshotCacheNilAndAsyncBranches(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC))
	_, _, snapshot, runtimeState := newRedisQuotaStack(t, clock)
	ctx := context.Background()

	// Redis 模式下内存语义恒定。
	if snapshot.IsCostSnapshotComplete() {
		t.Fatal("redis 模式 IsCostSnapshotComplete 必须为 false")
	}
	if !snapshot.IsCostSnapshotIncomplete() {
		t.Fatal("redis 模式 IsCostSnapshotIncomplete 必须为 true")
	}
	// 空快照：异步完整/不完整读取按缺失处理。
	if complete, err := snapshot.IsCostSnapshotCompleteAsync(ctx); err != nil || complete {
		t.Fatalf("空快照必须不完整: (%v, %v)", complete, err)
	}
	if sharedGeneratedAt(nil) != "" {
		t.Fatal("nil 快照 generatedAt 必须为空")
	}
	fixture := snapshotFixture("2026-09-17T08:00:00.000Z", true)
	if sharedGeneratedAt(&fixture) != fixture.GeneratedAt {
		t.Fatal("快照 generatedAt 必须透出")
	}

	// 坏 generatedAt 文档：Is*Async 与 Has 透传 replaceShared 错误。
	clock.Advance(sharedSnapshotMemoTTL + time.Second)
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey,
		snapshotFixture("not-a-time", true), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.IsCostSnapshotCompleteAsync(ctx); err == nil {
		t.Fatal("坏 generatedAt 必须透传")
	}
	if _, err := snapshot.HasGatewayQuotaSnapshotAsync(ctx); err == nil {
		t.Fatal("坏 generatedAt 必须透传")
	}
}

// TestW14CStatWindowTimezoneGaps 覆盖 StatsTimezone 的查询错误与 LoadLocation 失败。
func TestW14CStatWindowTimezoneGaps(t *testing.T) {
	// 已关闭数据库 → 查询错误透传。
	db := newTestDB(t, "w14c-tz-closed")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	source, err := NewDBTimezoneSource(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.StatsTimezone(context.Background()); err == nil {
		t.Fatal("已关闭数据库必须报错")
	}

	// 非法时区值 → LoadLocation 失败。
	db2 := newTestDB(t, "w14c-tz-bad")
	// 生产 PG 臂方言：juhe_business 限定，列照抄业务库 system_settings DDL。
	if _, err := db2.Exec(`CREATE TABLE juhe_business.system_settings (
		system_account_id text NOT NULL, key text NOT NULL, value_json text NOT NULL, updated_at text NOT NULL,
		PRIMARY KEY (system_account_id, key))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db2.Exec(`INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', 'usageStatsTimezone', '"No/Such/Zone"', '2026-09-17T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	source2, err := NewDBTimezoneSource(db2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source2.StatsTimezone(context.Background()); err == nil || !strings.Contains(err.Error(), "无效") {
		t.Fatalf("非法时区必须报无效: %v", err)
	}
}

// TestW14CCostsWeeklyAndScanErrors 覆盖 Weekly apply 闭包、数值列写入期拒收
// 与 ReadAPIKeyQuotaCostsExactAsync 的 LoadCostsBatch 错误透传。
func TestW14CCostsWeeklyAndScanErrors(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC))
	location := time.UTC
	db := newTestDB(t, "w14c-costs-weekly")
	statsSchema(t, db)
	stats, err := NewStatsStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := clock.Now()
	input := CostInput{SystemAccountID: "w14c-sys", ScopeType: ScopeTypeAPIKey, ScopeID: "w14c-ak", Now: now}
	seedCost(t, db, "usage_stats_weekly", []string{"system_account_id", "scope_type", "scope_id", "stat_week", "success_cost_usd"},
		[]any{"w14c-sys", ScopeTypeAPIKey, "w14c-ak", weekKey(now, location), 7.5})
	output, err := stats.LoadCostsBatch(context.Background(), []CostInput{input}, location)
	if err != nil {
		t.Fatal(err)
	}
	costs, ok := output[CostKey(input, location)]
	if !ok || costs.Weekly != 7.5 {
		t.Fatalf("weekly 行必须生效: (%+v, %v)", costs, err)
	}

	// PG 数值列（double precision）在写入期拒绝不可解析文本（SQLite 时代以
	// TEXT-in-REAL 注入 rows.Scan 错误，迁真实 PG 后重钉为写入期拒收断言；
	// rows.Scan 错误分支登记为 pgx 驱动下不可达）。
	dbBad := newTestDB(t, "w14c-costs-bad")
	statsSchema(t, dbBad)
	if _, err := dbBad.Exec(`INSERT INTO juhe_stats.usage_stats_totals (system_account_id, scope_type, scope_id, success_cost_usd)
		VALUES ('w14c-sys', 'api_key', 'w14c-ak', 'not-a-number')`); err == nil {
		t.Fatal("非数值成本必须被数值列拒收")
	}

	// PG 模式 ReadAPIKeyQuotaCostsExactAsync：stats 缺表 → LoadCostsBatch 错误。
	dbNoTables := newTestDB(t, "w14c-costs-notable")
	statsNoTables, err := NewStatsStore(dbNoTables)
	if err != nil {
		t.Fatal(err)
	}
	clock2 := newFakeClock(time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC))
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes:    Modes{},
		Stats:    statsNoTables,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{}, clock2),
		Now:      clock2.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadAPIKeyQuotaCostsExactAsync(context.Background(),
		APIKeyRow{ID: "w14c-ak", SystemAccountID: "w14c-sys", QuotaLimitsJSON: testQuotaLimits}, clock2.Now()); err == nil {
		t.Fatal("缺表必须报错")
	}
}

// TestW14CQuotaDefaultNowAndNilCtx 覆盖 Now=nil 回退（APIKey 配额服务，授权
// 配额服务构造臂随授权额度域退役删除）与 StatsTimezone(nil ctx)。
func TestW14CQuotaDefaultNowAndNilCtx(t *testing.T) {
	statsDB := newTestDB(t, "w14c-default-now-stats")
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB)
	if err != nil {
		t.Fatal(err)
	}
	wallClock := newFakeClock(time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC))
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes: Modes{}, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{}, wallClock),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CheckAPIKeyQuotaAsync(context.Background(),
		APIKeyRow{ID: "w14c-ak", SystemAccountID: "w14c-sys", QuotaLimitsJSON: testQuotaLimits}); err != nil {
		t.Fatalf("默认 Now 必须可用: %v", err)
	}

	// nil ctx → context.Background() 回退（缓存命中路径，无数据库访问）。
	tzSource, err := NewDBTimezoneSource(statsDB, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tzSource.StatsTimezone(nil); err == nil {
		t.Fatal("缺表必须报错")
	}

	// hourly 限额带未知键 → normalizeHourlyQuotaLimit 报错。
	if _, err := ParseRequestQuotaLimitsJSON(`{"hourly":{"enabled":true,"limit":1,"hours":2,"w14c-extra":1}}`); err == nil {
		t.Fatal("hourly 未知键必须报错")
	}
}

// TestW14CInflightSnapshotCostError 覆盖 ReserveGatewayCost 的快照成本读取错误。
func TestW14CInflightSnapshotCostError(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC))
	_, _, snapshot, runtimeState := newRedisQuotaStack(t, clock)
	ctx := context.Background()
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey,
		snapshotFixture("not-a-time", true), time.Minute); err != nil {
		t.Fatal(err)
	}
	apiKeys, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes:    Modes{RedisCache: true, RedisRuntimeState: true},
		Timezone: mustTZ(t, time.UTC),
		Snapshot: snapshot,
		Shared:   mustSharedForInflight(t, clock),
		Now:      clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	inflight, err := NewInflightQuotaService(InflightQuotaConfig{
		APIKeys:   apiKeys,
		Estimator: &wjMockEstimator{cost: 0.5, ok: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inflight.ReserveGatewayCost(ctx, GatewayReserveInput{
		APIKey:       APIKeyRow{ID: "w14c-ak", SystemAccountID: "w14c-sys", QuotaLimitsJSON: testQuotaLimits},
		ProviderCode: "openai",
	}); err == nil {
		t.Fatal("快照成本读取错误必须透传")
	}
}

// mustSharedForInflight 构造 API Key 服务使用的 shared cache。
func mustSharedForInflight(t *testing.T, clock *fakeClock) SharedJSONCache {
	t.Helper()
	_ = clock
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	shared, err := NewRedisSharedCache(client, "w14c", "gateway:api-key-quota")
	if err != nil {
		t.Fatal(err)
	}
	return shared
}
