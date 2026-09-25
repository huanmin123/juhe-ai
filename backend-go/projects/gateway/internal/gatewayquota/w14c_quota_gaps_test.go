package gatewayquota

// w14c 覆盖率补齐：针对 w13d/wj 之后仍缺失的分支。
// 已确认的不可达（防御性）守卫，本文件登记、不强行构造：
//   - rediscache.go NewRedisSharedCache 第 2/3 个 RedisNamespacedKey 错误分支
//     （三个调用使用同一 namespace，首个失败即返回）；
//   - rfc3339.go fractionNanos 的 Atoi 错误分支（正则限制 1-9 位小数，填充后
//     必然可解析）、parseRfc3339Instant 的 fractionNanos !ok 与归一化分支
//     （分量均已在正则/比较中限界，time.Date 不会进位）；
//   - snapshot.go InvalidateAuthorizationQuotaSnapshot 规范化后毫秒转换失败、
//     sharedSnapshotAuthorizationUsableLocked 的 generatedAt 失败（入参先经
//     requiredRfc3339Instant 规范化，规范串必可复解析）；
//   - apikeyquota.go/authzquota.go ServerRole 非 Redis 分支内的
//     setCacheEntryAsync 错误返回（RedisCache=false 时恒返回 nil）；
//   - inflight.go normalizedCost 的 ParseFloat 失败（FormatFloat 输出必可回解）；
//   - modes.go recencyList.back 的空链表分支（调用点保证非空）。

import (
	"context"
	"database/sql"
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
	if snapshot.IsAuthorizationSnapshotComplete() {
		t.Fatal("redis 模式 IsAuthorizationSnapshotComplete 必须为 false")
	}
	if !snapshot.IsAuthorizationSnapshotIncomplete() {
		t.Fatal("redis 模式 IsAuthorizationSnapshotIncomplete 必须为 true")
	}

	// 空快照：异步完整/不完整读取按缺失处理。
	if complete, err := snapshot.IsCostSnapshotCompleteAsync(ctx); err != nil || complete {
		t.Fatalf("空快照必须不完整: (%v, %v)", complete, err)
	}
	if complete, err := snapshot.IsAuthorizationSnapshotCompleteAsync(ctx); err != nil || complete {
		t.Fatalf("空快照必须不完整: (%v, %v)", complete, err)
	}
	if incomplete, err := snapshot.IsAuthorizationSnapshotIncompleteAsync(ctx); err != nil || incomplete {
		t.Fatalf("空快照未失效必须不完整=false: (%v, %v)", incomplete, err)
	}
	if sharedGeneratedAt(nil) != "" {
		t.Fatal("nil 快照 generatedAt 必须为空")
	}
	fixture := snapshotFixture("2026-09-17T08:00:00.000Z", true, true)
	if sharedGeneratedAt(&fixture) != fixture.GeneratedAt {
		t.Fatal("快照 generatedAt 必须透出")
	}

	// 坏 generatedAt 文档：Is*Async 与 Has 透传 replaceShared 错误。
	clock.Advance(sharedSnapshotMemoTTL + time.Second)
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey,
		snapshotFixture("not-a-time", true, true), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.IsCostSnapshotCompleteAsync(ctx); err == nil {
		t.Fatal("坏 generatedAt 必须透传")
	}
	if _, err := snapshot.IsAuthorizationSnapshotCompleteAsync(ctx); err == nil {
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
	source, err := NewDBTimezoneSource(db, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.StatsTimezone(context.Background()); err == nil {
		t.Fatal("已关闭数据库必须报错")
	}

	// 非法时区值 → LoadLocation 失败。
	db2 := newTestDB(t, "w14c-tz-bad")
	if _, err := db2.Exec(`CREATE TABLE system_settings (
		system_account_id TEXT, key TEXT, value_json TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db2.Exec(`INSERT INTO system_settings VALUES ('sys_admin', 'usageStatsTimezone', '"No/Such/Zone"')`); err != nil {
		t.Fatal(err)
	}
	source2, err := NewDBTimezoneSource(db2, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source2.StatsTimezone(context.Background()); err == nil || !strings.Contains(err.Error(), "无效") {
		t.Fatalf("非法时区必须报无效: %v", err)
	}
}

// TestW14CCostsWeeklyAndScanErrors 覆盖 Weekly apply 闭包、Scan 错误与
// ReadAPIKeyQuotaCostsExactAsync 的 LoadCostsBatch 错误透传。
func TestW14CCostsWeeklyAndScanErrors(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC))
	location := time.UTC
	db := newTestDB(t, "w14c-costs-weekly")
	statsSchema(t, db)
	stats, err := NewStatsStore(db, false)
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

	// success_cost_usd 为不可解析文本 → Scan 错误。
	dbBad := newTestDB(t, "w14c-costs-bad")
	statsSchema(t, dbBad)
	statsBad, err := NewStatsStore(dbBad, false)
	if err != nil {
		t.Fatal(err)
	}
	seedCost(t, dbBad, "usage_stats_totals", []string{"system_account_id", "scope_type", "scope_id", "success_cost_usd"},
		[]any{"w14c-sys", ScopeTypeAPIKey, "w14c-ak", "not-a-number"})
	if _, err := statsBad.LoadCostsBatch(context.Background(), []CostInput{input}, location); err == nil {
		t.Fatal("非数值成本必须报 Scan 错误")
	}

	// PG 模式 ReadAPIKeyQuotaCostsExactAsync：stats 缺表 → LoadCostsBatch 错误。
	dbNoTables := newTestDB(t, "w14c-costs-notable")
	statsNoTables, err := NewStatsStore(dbNoTables, false)
	if err != nil {
		t.Fatal(err)
	}
	clock2 := newFakeClock(time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC))
	service, err := NewAPIKeyQuotaService(APIKeyQuotaConfig{
		Modes:    Modes{PostgresDatabase: true},
		Stats:    statsNoTables,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{PostgresDatabase: true}, clock2),
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

// TestW14CAuthzWrapperErrorPaths 覆盖 ByIDs / ReadOnly / ExactAsync 包装层错误。
func TestW14CAuthzWrapperErrorPaths(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC))
	business := newTestDB(t, "w14c-authz-wrap-biz")
	statsDB := newTestDB(t, "w14c-authz-wrap-stats")
	authzSchema(t, business)
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := clock.Now()

	// ServerRole 直接同步 SQLite 守卫（BatchByIDs）。
	guardService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes: Modes{ServerRole: true}, Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{ServerRole: true}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guardService.CheckAuthorizationQuotaBatchByIDs(ctx, "w14c-ga", nil, now); err == nil {
		t.Fatal("ServerRole 同步 SQLite 必须拒绝")
	}

	// ReadOnly：坏时区错误透传（单账号与批量）。
	brokenService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Business: business, Stats: stats, Timezone: w13dBrokenTZ{},
		Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := brokenService.CheckAuthorizationQuotaByIDsReadOnly(ctx, "w14c-ga", "w14c-aa", now); err == nil {
		t.Fatal("坏时区必须报错")
	}
	if _, err := brokenService.CheckAuthorizationQuotaBatchByIDsReadOnly(ctx, "w14c-ga", nil, now); err == nil {
		t.Fatal("坏时区必须报错")
	}

	// ReadOnly：统计库缺表 → loadCostChecksByScope 错误（需要真实授权行走
	// 到成本物化，才能触发统计库查询）。
	seedAuthzRow(t, business, "w14c-ga", "w14c-owner", "w14c-grantee", "group", "w14c-g1", "",
		`{"daily":{"enabled":true,"limit":10}}`, "active")
	emptyStatsDB := newTestDB(t, "w14c-authz-wrap-empty")
	emptyStats, err := NewStatsStore(emptyStatsDB, false)
	if err != nil {
		t.Fatal(err)
	}
	emptyStatsService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Business: business, Stats: emptyStats, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emptyStatsService.CheckAuthorizationQuotaBatchByIDsReadOnly(ctx, "w14c-ga", nil, now); err == nil {
		t.Fatal("统计库缺表必须报错")
	}

	// ExactAsync：syncer 错误、坏时区、缺表统计库。
	pgBusiness := newTestDB(t, "w14c-authz-wrap-pg")
	syncerService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{PostgresDatabase: true, RedisRuntimeState: true},
		Business: pgBusiness, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{PostgresDatabase: true}, clock),
		Syncer:   &wjMockSyncer{err: errors.New("w14c 失效同步失败")}, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncerService.CheckAuthorizationQuotaByIDsExactAsync(ctx, "w14c-ga", "w14c-aa", now); err == nil {
		t.Fatal("syncer 错误必须透传")
	}
	brokenPGService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes: Modes{PostgresDatabase: true}, Business: pgBusiness, Stats: stats, Timezone: w13dBrokenTZ{},
		Snapshot: mustSnapshotCache(t, Modes{PostgresDatabase: true}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := brokenPGService.CheckAuthorizationQuotaByIDsExactAsync(ctx, "w14c-ga", "w14c-aa", now); err == nil {
		t.Fatal("坏时区必须报错")
	}
	if _, err := brokenPGService.CheckAuthorizationQuotaBatchByIDsExactAsync(ctx, "w14c-ga", nil, now); err == nil {
		t.Fatal("坏时区必须报错")
	}
	noTablePGService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes: Modes{PostgresDatabase: true}, Business: pgBusiness, Stats: emptyStats, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{PostgresDatabase: true}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noTablePGService.CheckAuthorizationQuotaBatchByIDsExactAsync(ctx, "w14c-ga", nil, now); err == nil {
		t.Fatal("统计库缺表必须报错")
	}

	// 已关闭业务库：行加载与团队行加载错误。
	closedDB := newTestDB(t, "w14c-authz-wrap-closed")
	if err := closedDB.Close(); err != nil {
		t.Fatal(err)
	}
	closedService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Business: closedDB, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{}, clock), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := closedService.queryAuthorizationRows(ctx, []string{"w14c-x"}); err == nil {
		t.Fatal("已关闭业务库必须报错")
	}
	teamRow := AuthorizationQuotaRow{ID: "w14c-x", EffectiveSourceTeamID: sql.NullString{Valid: true, String: "w14c-team"}}
	if _, err := closedService.loadTeamRowsByAuthorizationID(ctx, []AuthorizationQuotaRow{teamRow}); err == nil {
		t.Fatal("已关闭业务库必须报错")
	}
	if _, err := closedService.loadCostChecksByScope(ctx,
		[]authorizationQuotaScopeRequest{{authorizationID: "w14c-x", scopeType: scopeGroupAuthorization}}, now, time.UTC); err == nil {
		t.Fatal("已关闭业务库必须报错")
	}
}

// TestW14CAuthzSnapshotFallbackSemantics 覆盖异步快照补判与决策分支。
func TestW14CAuthzSnapshotFallbackSemantics(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC))
	business := newTestDB(t, "w14c-authz-snap-biz")
	statsDB := newTestDB(t, "w14c-authz-snap-stats")
	authzSchema(t, business)
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 内存模式：失效水位下的批量补判语义（batchSnapshotNeedsDBFallback）。
	memSnapshot := mustSnapshotCache(t, Modes{}, clock)
	if err := memSnapshot.InvalidateAuthorizationQuotaSnapshot(nil); err != nil {
		t.Fatal(err)
	}
	memService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: memSnapshot, Now: clock.Now,
		DBService: &mockDBService{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !memService.batchSnapshotNeedsDBFallback(
		GroupAccessMetadata{GroupAuthorizationID: "w14c-ga", GroupAuthorizationQuotaLimited: true},
		[]AccountAuthorizationSummary{{ID: "w14c-a1", AccountAuthorizationID: "w14c-aa", AccountAuthorizationQuotaLimited: true}}) {
		t.Fatal("失效且组缺失必须补判")
	}
	if !memService.batchSnapshotNeedsDBFallback(
		GroupAccessMetadata{},
		[]AccountAuthorizationSummary{{ID: "w14c-a2", AccountAuthorizationID: "w14c-aa", AccountAuthorizationQuotaLimited: true}}) {
		t.Fatal("账号受限缺失必须补判")
	}
	if memService.batchSnapshotNeedsDBFallback(GroupAccessMetadata{}, nil) {
		t.Fatal("无受限 scope 不需要补判")
	}

	// Redis 模式：snapshotNeedsDBFallbackAsync / decisionFromSnapshotAsync。
	_, shared, snapshot, runtimeState := newRedisQuotaStack(t, clock)
	redisService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{RedisCache: true, RedisRuntimeState: true},
		Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: snapshot, Shared: shared, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	seedSharedSnapshot := func(doc GatewayQuotaSnapshot) {
		t.Helper()
		clock.Advance(sharedSnapshotMemoTTL + time.Second)
		if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey, doc, time.Minute); err != nil {
			t.Fatal(err)
		}
	}

	// 不完整快照：受限 scope 缺失 → 必须补判；决策回退为拒绝。
	incomplete := snapshotFixture("2026-09-17T08:00:00.000Z", true, false)
	incomplete.AuthorizationEntries = nil
	seedSharedSnapshot(incomplete)
	needs, err := redisService.snapshotNeedsDBFallbackAsync(ctx, authorizationSnapshotInput{
		groupAuthorizationID:           "w14c-ga",
		groupAuthorizationQuotaLimited: true,
	})
	if err != nil || !needs {
		t.Fatalf("组缺失必须补判: (%v, %v)", needs, err)
	}
	needs, err = redisService.snapshotNeedsDBFallbackAsync(ctx, authorizationSnapshotInput{
		accountAuthorizationID:           "w14c-aa",
		accountAuthorizationQuotaLimited: true,
	})
	if err != nil || !needs {
		t.Fatalf("账号缺失必须补判: (%v, %v)", needs, err)
	}
	decision, err := redisService.decisionFromSnapshotAsync(ctx, authorizationSnapshotInput{
		groupAuthorizationID:           "w14c-ga",
		groupAuthorizationQuotaLimited: true,
	})
	if err != nil || decision.Allowed {
		t.Fatalf("不完整快照受限缺失必须拒绝: (%+v, %v)", decision, err)
	}
	decision, err = redisService.decisionFromSnapshotAsync(ctx, authorizationSnapshotInput{
		accountAuthorizationID:           "w14c-aa",
		accountAuthorizationQuotaLimited: true,
	})
	if err != nil || decision.Allowed {
		t.Fatalf("不完整快照账号受限缺失必须拒绝: (%+v, %v)", decision, err)
	}

	// 坏 generatedAt：异步补判与决策的错误透传。
	seedSharedSnapshot(snapshotFixture("not-a-time", true, true))
	if _, err := redisService.snapshotNeedsDBFallbackAsync(ctx, authorizationSnapshotInput{}); err == nil {
		t.Fatal("坏 generatedAt 必须透传")
	}
	if _, err := redisService.decisionFromSnapshotAsync(ctx, authorizationSnapshotInput{}); err == nil {
		t.Fatal("坏 generatedAt 必须透传")
	}
}

// TestW14CAuthzRedisWorkerWriteErrors 覆盖 RedisCache worker/批量路径的
// sharedGet 与 setCacheEntryAsync 错误。
func TestW14CAuthzRedisWorkerWriteErrors(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC))
	business := newTestDB(t, "w14c-authz-worker-biz")
	statsDB := newTestDB(t, "w14c-authz-worker-stats")
	authzSchema(t, business)
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	groupLimited := GroupAccessMetadata{GroupAuthorizationID: "w14c-ga", GroupAuthorizationQuotaLimited: true}

	_, shared, snapshot, _ := newRedisQuotaStack(t, clock)
	// 共享缓存 Get 失败。
	getFail := &w14cSharedFail{inner: shared, getErr: true}
	workerService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{RedisCache: true, RedisRuntimeState: true},
		Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: snapshot, Shared: getFail, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workerService.CheckAuthorizationQuotaAsync(ctx, groupLimited, nil); err == nil {
		t.Fatal("sharedGet 错误必须透传")
	}

	// 批量：runtimeCacheKey 错误。
	brokenBatch, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{RedisCache: true, RedisRuntimeState: true},
		Business: business, Stats: stats, Timezone: w13dBrokenTZ{},
		Snapshot: snapshot, Shared: shared, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := brokenBatch.CheckAuthorizationQuotaBatchAsync(ctx, groupLimited,
		[]AccountAuthorizationSummary{{ID: "w14c-a1"}}); err == nil {
		t.Fatal("坏时区批量必须报错")
	}

	// 批量 worker：missing 决策写缓存失败。
	setFail := &w14cSharedFail{inner: shared, setErr: true}
	setFailService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{RedisCache: true, RedisRuntimeState: true},
		Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: snapshot, Shared: setFail, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setFailService.CheckAuthorizationQuotaAsync(ctx, groupLimited, nil); err == nil {
		t.Fatal("worker 写缓存失败必须透传")
	}
	if _, err := setFailService.CheckAuthorizationQuotaBatchAsync(ctx, groupLimited,
		[]AccountAuthorizationSummary{{ID: "w14c-a1", AccountAuthorizationID: "w14c-aa"}}); err == nil {
		t.Fatal("批量 worker 写缓存失败必须透传")
	}
}

// w14cSharedFail 在 w13dSharedOverride 基础上支持 Get 失败注入。
type w14cSharedFail struct {
	inner  SharedJSONCache
	getErr bool
	setErr bool
}

func (o *w14cSharedFail) Get(ctx context.Context, key string, target any) (bool, error) {
	if o.getErr {
		return false, errors.New("w14c shared 读取失败")
	}
	return o.inner.Get(ctx, key, target)
}

func (o *w14cSharedFail) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	if o.setErr {
		return errors.New("w14c shared 写入失败")
	}
	return o.inner.Set(ctx, key, value, ttl)
}

func (o *w14cSharedFail) Clear(ctx context.Context) error { return o.inner.Clear(ctx) }

// TestW14CAuthzBatchServerRoleSnapshotErrors 覆盖批量 server 角色的快照错误与写缓存失败。
func TestW14CAuthzBatchServerRoleSnapshotErrors(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC))
	business := newTestDB(t, "w14c-authz-bsr-biz")
	statsDB := newTestDB(t, "w14c-authz-bsr-stats")
	authzSchema(t, business)
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	groupLimited := GroupAccessMetadata{GroupAuthorizationID: "w14c-ga", GroupAuthorizationQuotaLimited: true}
	accounts := []AccountAuthorizationSummary{{ID: "w14c-a1", AccountAuthorizationID: "w14c-aa", AccountAuthorizationQuotaLimited: true}}

	// server 角色 + 坏快照 → HasGatewayQuotaSnapshotAsync 错误透传。
	_, shared, snapshot, runtimeState := newRedisQuotaStack(t, clock)
	badService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{RedisCache: true, RedisRuntimeState: true, ServerRole: true},
		Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: snapshot, Shared: shared, Now: clock.Now,
		DBService: &mockDBService{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey,
		snapshotFixture("not-a-time", true, true), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := badService.CheckAuthorizationQuotaAsync(ctx, groupLimited, &accounts[0]); err == nil {
		t.Fatal("坏快照必须透传")
	}
	if _, err := badService.CheckAuthorizationQuotaBatchAsync(ctx, groupLimited, accounts); err == nil {
		t.Fatal("坏快照批量必须透传")
	}

	// server 角色 + 完整快照 + 写缓存失败（checkBatchServerRole 内 setCacheEntryAsync）。
	setFail := &w14cSharedFail{inner: shared, setErr: true}
	setFailService, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    Modes{RedisCache: true, RedisRuntimeState: true, ServerRole: true},
		Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: snapshot, Shared: setFail, Now: clock.Now,
		DBService: &mockDBService{},
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(sharedSnapshotMemoTTL + time.Second)
	if err := runtimeState.SetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey,
		snapshotFixture("2026-09-17T08:00:00.000Z", true, true), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := setFailService.CheckAuthorizationQuotaBatchAsync(ctx, groupLimited, accounts); err == nil {
		t.Fatal("批量快照决策写缓存失败必须透传")
	}
}

// TestW14CQuotaNoopLogAndNamespaceGuard 直接触发 noopLog 与 namespace 守卫。
func TestW14CQuotaNoopLogAndNamespaceGuard(t *testing.T) {
	noopLog("w14c-event", map[string]any{"k": "v"}, "w14c 消息")
	if _, err := NewRedisSharedCache(&redis.Client{}, "###", "w14c-cache"); err == nil {
		t.Fatal("非法 namespace 必须报错")
	}
}

// TestW14CQuotaDefaultNowAndNilCtx 覆盖 Now=nil 回退与 StatsTimezone(nil ctx)。
func TestW14CQuotaDefaultNowAndNilCtx(t *testing.T) {
	business := newTestDB(t, "w14c-default-now-biz")
	statsDB := newTestDB(t, "w14c-default-now-stats")
	authzSchema(t, business)
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB, false)
	if err != nil {
		t.Fatal(err)
	}
	// Now=nil → time.Now 回退；Log=nil → noopLog 回退。
	wallClock := newFakeClock(time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC))
	service, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Business: business, Stats: stats, Timezone: mustTZ(t, time.UTC),
		Snapshot: mustSnapshotCache(t, Modes{}, wallClock),
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := service.CheckAuthorizationQuotaAsync(context.Background(),
		GroupAccessMetadata{GroupAuthorizationID: "w14c-ga", GroupAuthorizationQuotaLimited: true}, nil)
	if err != nil {
		t.Fatalf("默认 Now 必须可用: %v", err)
	}
	_ = decision

	// nil ctx → context.Background() 回退（缓存命中路径，无数据库访问）。
	tzSource, err := NewDBTimezoneSource(statsDB, false, time.Now)
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
		snapshotFixture("not-a-time", true, true), time.Minute); err != nil {
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
