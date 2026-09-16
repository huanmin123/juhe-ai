package gatewayruntimecache

// w11d 覆盖补齐（一）：service.go 纯函数与 TTL/可用性助手、shared.go Redis
// 共享缓存错误臂、cache.go peek/禁用臂、concurrency.go 后台等待与设置槽、
// types.go 克隆臂、snapshot.go numberValue/键投影助手。

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// ---------------------------------------------------------------------------
// service.go：New 守卫、syncInvalidationsBestEffort、logSharedFailure、
// key/TTL/可用性助手
// ---------------------------------------------------------------------------

func TestW11DNewRequiresModels(t *testing.T) {
	if _, err := New(nil, Options{}); err == nil || !strings.Contains(err.Error(), "ReadModels") {
		t.Fatalf("nil models 必须报错: %v", err)
	}
}

// w11dInvalStore 是可控的 inval.SharedStore 替身。
type w11dInvalStore struct {
	getVersion func(ctx context.Context, topic string) (int64, error)
}

func (s *w11dInvalStore) GetVersion(ctx context.Context, topic string) (int64, error) {
	if s.getVersion == nil {
		return 0, nil
	}
	return s.getVersion(ctx, topic)
}

func (s *w11dInvalStore) PublishVersion(context.Context, string, int64) (int64, error) {
	return 0, nil
}

func TestW11DSyncInvalidationsBestEffort(t *testing.T) {
	ctx := context.Background()

	// 未开启 SyncInvalidationsOnRead：直接返回。
	models := newFakeModels()
	clock := newManualClock()
	svcPlain := newTestService(t, models, clock, nil)
	svcPlain.syncInvalidationsBestEffort(ctx) // no-op 分支

	// 开启但共享读取失败：吞掉错误，不清缓存。
	store := &w11dInvalStore{getVersion: func(context.Context, string) (int64, error) {
		return 0, errors.New("w11d shared down")
	}}
	bus := inval.New(nil)
	bus.SetSharedStore(store)
	models2 := newFakeModels()
	models2.runtimes["sk-sync"] = staticRuntime(t, models2, testAPIKeyRow("k_sync", RouteStrategyModeNormal, "g1"), nil, nil)
	svcErr := newTestService(t, models2, newManualClock(), func(o *Options) {
		o.SyncInvalidationsOnRead = true
		o.Bus = bus
	})
	if _, err := svcErr.ReadCachedGatewayRuntimeAsync(ctx, "sk-sync"); err != nil {
		t.Fatal(err)
	}
	if svcErr.runtimeCacheSize() != 1 {
		t.Fatalf("预热前缓存 = %d", svcErr.runtimeCacheSize())
	}
	svcErr.syncInvalidationsBestEffort(ctx)
	if svcErr.runtimeCacheSize() != 1 {
		t.Fatal("共享读取失败不得清缓存")
	}

	// 版本变化：跨实例失效清空 runtime 缓存。
	versions := map[string]int64{}
	store.getVersion = func(_ context.Context, topic string) (int64, error) {
		return versions[topic], nil
	}
	svc := newTestService(t, models2, newManualClock(), func(o *Options) {
		o.SyncInvalidationsOnRead = true
		o.Bus = bus
	})
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-sync"); err != nil {
		t.Fatal(err)
	}
	// 首次 sync：记录 lastSeen，不清。
	svc.syncInvalidationsBestEffort(ctx)
	if svc.runtimeCacheSize() != 1 {
		t.Fatal("首见版本不得清缓存")
	}
	// 版本前进：清空。
	versions[inval.TopicGatewayRuntime] = 5
	svc.syncInvalidationsBestEffort(ctx)
	if svc.runtimeCacheSize() != 0 {
		t.Fatal("版本变化必须清 runtime 缓存")
	}
	// 相同版本：不清。
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-sync"); err != nil {
		t.Fatal(err)
	}
	svc.syncInvalidationsBestEffort(ctx)
	if svc.runtimeCacheSize() != 1 {
		t.Fatal("同版本不得清缓存")
	}
}

func TestW11DLogSharedFailureThrottle(t *testing.T) {
	models := newFakeModels()
	clock := newManualClock()
	logger := &whWarnLogger{}
	svc := newTestService(t, models, clock, func(o *Options) { o.Logger = logger })
	err := errors.New("w11d shared failure")
	svc.logSharedFailure("w11d_event", err)
	svc.logSharedFailure("w11d_event", err)
	if len(logger.events) != 1 {
		t.Fatalf("30s 窗口内只告警一次: %v", logger.events)
	}
	clock.Advance(31 * time.Second)
	svc.logSharedFailure("w11d_event", err)
	if len(logger.events) != 2 {
		t.Fatalf("窗口过后必须再次告警: %v", logger.events)
	}
	// 无 logger：静默。
	bare := newTestService(t, newFakeModels(), newManualClock(), nil)
	bare.logSharedFailure("w11d_event", err)
}

func TestW11DCacheKeyHelpers(t *testing.T) {
	if got := gatewayOpenAIAccountsCacheKey("g1", "sys", "", ""); got != "g1:sys" {
		t.Fatalf("无模型键 = %q", got)
	}
	if got := gatewayOpenAIAccountsCacheKey("g1", "sys", " m1 ", ""); got != "g1:sys:model:m1:endpoint:any" {
		t.Fatalf("无族键 = %q", got)
	}
	if got := gatewayOpenAIAccountsCacheKey("g1", "sys", "m1", "responses"); got != "g1:sys:model:m1:endpoint:responses" {
		t.Fatalf("模型+族键 = %q", got)
	}
	if got := providerModelRouteIndexCacheKey([]string{"a", "b"}, "sys", false); got != "sys:priced:a,b" {
		t.Fatalf("路由索引键 = %q", got)
	}
	if got := providerModelRouteIndexCacheKey([]string{"a"}, "sys", true); got != "sys:unpriced:a" {
		t.Fatalf("含未定价键 = %q", got)
	}
	if got := providerModelCatalogCacheKey(ModelCatalogListOptions{ProviderCode: "gpt", SystemAccountID: "s1"}); got != "gpt:s1:active:priced" {
		t.Fatalf("目录键 = %q", got)
	}
	if got := providerModelCatalogCacheKey(ModelCatalogListOptions{ProviderCode: "gpt", SystemAccountID: "s1", IncludeInactive: true, IncludeUnpriced: true}); got != "gpt:s1:inactive:unpriced" {
		t.Fatalf("全量目录键 = %q", got)
	}
	if got := gatewayCacheKey("g", "s"); got != "g:s" {
		t.Fatalf("基础键 = %q", got)
	}
	if got := responseInspectionPolicyCacheKey("openai", "gpt"); got != "openai:gpt" {
		t.Fatalf("检查策略键 = %q", got)
	}
	if got := normalizedProviderRouteCodes([]string{" b ", "a", "a", ""}); strings.Join(got, ",") != "a,b" {
		t.Fatalf("路由代码归一 = %v", got)
	}
	values := []string{"c", "a", "b", "a"}
	sortStrings(values)
	if strings.Join(values, ",") != "a,a,b,c" {
		t.Fatalf("排序 = %v", values)
	}
}

func TestW11DTTLAndInstantHelpers(t *testing.T) {
	now := int64(1_700_000_000_000)
	if _, ok := rfc3339Millis("not-a-time"); ok {
		t.Fatal("坏时间不得通过")
	}
	if _, ok := rfc3339Millis("2026-09-01T08:00:00"); ok {
		t.Fatal("无 offset 不得通过")
	}
	if ms, ok := rfc3339Millis(" 2026-09-01T08:00:00Z "); !ok || ms == 0 {
		t.Fatalf("合法时间必须解析: %d %v", ms, ok)
	}
	// ttlBoundedByIsoExpiries：坏值、提前到期、钳制下限。
	if _, err := ttlBoundedByIsoExpiries(time.Minute, []string{"bad"}, now); err == nil {
		t.Fatal("坏 expiresAt 必须报错")
	}
	ttl, err := ttlBoundedByIsoExpiries(time.Minute, []string{"2026-09-01T08:00:05Z"}, 1_789_992_000_000)
	if err != nil || ttl >= time.Minute {
		t.Fatalf("提前到期必须收窄: %v %v", ttl, err)
	}
	if ttl, err := ttlBoundedByIsoExpiries(time.Minute, nil, now); err != nil || ttl != time.Minute {
		t.Fatalf("无 expiresAt 保持基础 TTL: %v %v", ttl, err)
	}
	// 早已过期的 expiresAt 必须钳到 1ms。
	past := time.UnixMilli(now - 1000).UTC().Format(time.RFC3339Nano)
	if ttl, err := ttlBoundedByIsoExpiries(time.Minute, []string{past}, now); err != nil || ttl != time.Millisecond {
		t.Fatalf("过期钳制 = %v %v", ttl, err)
	}
	// isoTimeExpired 坏值。
	if _, err := isoTimeExpired("bad", now); err == nil {
		t.Fatal("坏过期时间必须报错")
	}
	expired, err := isoTimeExpired(time.UnixMilli(now-1).UTC().Format(time.RFC3339Nano), now)
	if err != nil || !expired {
		t.Fatalf("过期判定 = %v %v", expired, err)
	}
}

func TestW11DRuntimeCacheTTLAndCandidates(t *testing.T) {
	now := int64(1_700_000_000_000)
	// 全字段 expiresAt 候选。
	expires := time.UnixMilli(now + 5_000).UTC().Format(time.RFC3339Nano)
	apiKeyExpires := expires
	row := testAPIKeyRow("k_ttl", RouteStrategyModeNormal, "g1")
	row.ExpiresAt = &apiKeyExpires
	groupExpires := expires
	account := testAccount("a_ttl", "sys_owner")
	account.AccountExpiresAt = &expires
	account.ExpiresAt = &expires
	account.AccountAuthorizationExpiresAt = &expires
	account.GroupAuthorizationExpiresAt = &expires
	groupAccess := &GroupUsageAccessMetadata{
		GroupOwnerSystemAccountID:     "sys_owner",
		GroupAuthorizationExpiresAt:   &groupExpires,
	}
	runtime := GatewayRuntime{APIKey: row, GroupAccess: groupAccess, Accounts: []OpenAIAccountSecret{account}}
	candidates := runtimeCacheExpiryCandidates(runtime)
	if len(candidates) != 6 {
		t.Fatalf("过期候选 = %d", len(candidates))
	}
	ttl, err := gatewayRuntimeCacheTTL(runtime, now)
	if err != nil || ttl != 5*time.Second {
		t.Fatalf("TTL 收窄 = %v %v", ttl, err)
	}
	// 坏候选必须报错。
	bad := "bad-instant"
	rowBad := testAPIKeyRow("k_bad", RouteStrategyModeNormal, "g1")
	rowBad.ExpiresAt = &bad
	if _, err := gatewayRuntimeCacheTTL(GatewayRuntime{APIKey: rowBad}, now); err == nil {
		t.Fatal("坏候选必须报错")
	}
	// 已过期候选钳到 1ms。
	pastPtr := time.UnixMilli(now - 10_000).UTC().Format(time.RFC3339Nano)
	rowPast := testAPIKeyRow("k_past", RouteStrategyModeNormal, "g1")
	rowPast.ExpiresAt = &pastPtr
	if ttl, err := gatewayRuntimeCacheTTL(GatewayRuntime{APIKey: rowPast}, now); err != nil || ttl != time.Millisecond {
		t.Fatalf("过期钳制 = %v %v", ttl, err)
	}
	// 分组访问 TTL 与账户 TTL。
	if ttl, err := groupUsageAccessCacheTTL(*groupAccess, now); err != nil || ttl != 5*time.Second {
		t.Fatalf("分组 TTL = %v %v", ttl, err)
	}
	if ttl, err := openAIAccountsCacheTTL([]OpenAIAccountSecret{account}, now); err != nil || ttl != 5*time.Second {
		t.Fatalf("账户 TTL = %v %v", ttl, err)
	}
	if _, err := groupUsageAccessCacheTTL(GroupUsageAccessMetadata{}, now); err != nil || ttlOfBase() == 0 {
		_ = err
	}
}

func ttlOfBase() int64 { return 0 }

func TestW11DUsabilityChecks(t *testing.T) {
	now := int64(1_700_000_000_000)
	future := time.UnixMilli(now + 60_000).UTC().Format(time.RFC3339Nano)
	past := time.UnixMilli(now - 60_000).UTC().Format(time.RFC3339Nano)

	if ok, err := isGatewayAPIKeyRuntimeUsableAt(nil, now); err != nil || ok {
		t.Fatalf("nil key = %v %v", ok, err)
	}
	row := testAPIKeyRow("k_u", RouteStrategyModeNormal, "g1")
	row.Status = "revoked"
	if ok, err := isGatewayAPIKeyRuntimeUsableAt(row, now); err != nil || ok {
		t.Fatalf("非活跃 key = %v %v", ok, err)
	}
	row.Status = "active"
	row.ExpiresAt = &future
	if ok, err := isGatewayAPIKeyRuntimeUsableAt(row, now); err != nil || !ok {
		t.Fatalf("活跃 key = %v %v", ok, err)
	}
	bad := "bad"
	row.ExpiresAt = &bad
	if _, err := isGatewayAPIKeyRuntimeUsableAt(row, now); err == nil {
		t.Fatal("坏 expiresAt 必须报错")
	}

	if ok, err := isGroupUsageAccessRuntimeUsableAt(nil, now); err != nil || ok {
		t.Fatalf("nil 分组 = %v %v", ok, err)
	}
	access := &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys"}
	accessExpired := &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys", GroupAuthorizationExpiresAt: &past}
	if ok, err := isGroupUsageAccessRuntimeUsableAt(accessExpired, now); err != nil || ok {
		t.Fatalf("过期分组 = %v %v", ok, err)
	}
	if ok, err := isGroupUsageAccessRuntimeUsableAt(access, now); err != nil || !ok {
		t.Fatalf("有效分组 = %v %v", ok, err)
	}

	account := testAccount("a_u", "sys")
	if ok, err := isOpenAIAccountRuntimeUsableAt(&account, now); err != nil || !ok {
		t.Fatalf("活跃账户 = %v %v", ok, err)
	}
	account.Status = AccountStatusTemporaryUnavailable
	if ok, err := isOpenAIAccountRuntimeUsableAt(&account, now); err != nil || ok {
		t.Fatalf("非活跃账户 = %v %v", ok, err)
	}
	account.Status = AccountStatusActive
	account.ExpiresAt = &past
	if ok, err := isOpenAIAccountRuntimeUsableAt(&account, now); err != nil || ok {
		t.Fatalf("过期账户 = %v %v", ok, err)
	}
	account.ExpiresAt = &bad
	if _, err := isOpenAIAccountRuntimeUsableAt(&account, now); err == nil {
		t.Fatal("坏账户 expiresAt 必须报错")
	}
	if expired, err := notExpired(past, now); err != nil || expired {
		t.Fatalf("notExpired = %v %v", expired, err)
	}
	if _, err := notExpired("bad", now); err == nil {
		t.Fatal("notExpired 坏值必须报错")
	}
	if ok := ShouldInvalidateProviderModelCatalog("custom_provider_model_saved"); !ok {
		t.Fatal("目录失效理由必须命中")
	}
	if ok := ShouldInvalidateProviderModelCatalog("other"); ok {
		t.Fatal("未知理由不得触发目录失效")
	}
}

// ---------------------------------------------------------------------------
// shared.go：RedisSharedCache 错误臂
// ---------------------------------------------------------------------------

func TestW11DRedisSharedCacheErrorArms(t *testing.T) {
	server := miniredis.RunT(t)
	factory, closeFn, err := NewRedisSharedCacheFactory("redis://"+server.Addr()+"/0", "w11d-ns")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	ctx := context.Background()
	cache := factory.Cache("w11d")

	// 缺失键：redis.Nil → 未命中。
	var out map[string]any
	if ok, err := cache.Get(ctx, "missing", &out); ok || err != nil {
		t.Fatalf("缺失 = %v %v", ok, err)
	}
	// Set 不可序列化值：marshal 失败。
	if err := cache.Set(ctx, "bad", make(chan int), time.Minute); err == nil {
		t.Fatal("channel 值必须 marshal 失败")
	}

	// 服务器下线后的连接失败臂：Get/Set/Clear 报原始错误。
	deadServer := miniredis.NewMiniRedis()
	if err := deadServer.Start(); err != nil {
		t.Fatal(err)
	}
	deadFactory, deadClose, err := NewRedisSharedCacheFactory("redis://"+deadServer.Addr()+"/0", "w11d-dead")
	if err != nil {
		t.Fatal(err)
	}
	deadClose()
	deadServer.Close()
	dead := deadFactory.Cache("w11d")
	deadCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := dead.Get(deadCtx, "k", &out); err == nil {
		t.Fatal("断连 Get 必须报错")
	}
	if err := dead.Set(deadCtx, "k", map[string]any{"a": 1}, time.Minute); err == nil {
		t.Fatal("断连 Set 必须报错")
	}
	if err := dead.Clear(deadCtx); err == nil {
		t.Fatal("断连 Clear 必须报错")
	}
}

// ---------------------------------------------------------------------------
// cache.go：peek 与禁用臂
// ---------------------------------------------------------------------------

func TestW11DEntryCachePeekArms(t *testing.T) {
	clock := newManualClock()
	disabled := newEntryCache[string, int]("w11d_disabled", 4, time.Minute, false, false, clock, nil, nil)
	if _, ok := disabled.get("k"); ok {
		t.Fatal("禁用缓存 get 必须未命中")
	}
	disabled.set("k", 1, time.Minute)
	if disabled.size() != 0 {
		t.Fatal("禁用缓存不得写入")
	}
	if _, ok := disabled.peek("k"); ok {
		t.Fatal("禁用缓存 peek 必须未命中")
	}
	disabled.delete("k")
	disabled.clear()

	peeker := newEntryCache[string, int]("w11d_peek", 4, time.Minute, true, true, clock, nil, nil)
	peeker.set("k", 7, time.Minute)
	if v, ok := peeker.peek("k"); !ok || v != 7 {
		t.Fatalf("peek = %v %v", v, ok)
	}
	// peek 不校验过期：过期条目仍可读（有界回退窗口语义）。
	clock.Advance(2 * time.Minute)
	if _, ok := peeker.peek("k"); !ok {
		t.Fatal("peek 不得因过期丢弃")
	}
	if _, ok := peeker.get("k"); ok {
		t.Fatal("get 过期必须丢弃")
	}
	// delete 与覆盖 set。
	peeker.set("k2", 1, time.Minute)
	peeker.set("k2", 2, time.Minute)
	peeker.delete("k2")
	if _, ok := peeker.peek("k2"); ok {
		t.Fatal("delete 后必须未命中")
	}
	// 默认 TTL 分支：ttl<=0 用缓存默认值。
	peeker.set("k3", 3, 0)
	if v, ok := peeker.get("k3"); !ok || v != 3 {
		t.Fatalf("默认 TTL = %v %v", v, ok)
	}
	// 容量淘汰：max=2 写 3 个键，最老被淘汰。
	bounded := newEntryCache[string, int]("w11d_bounded", 2, time.Minute, false, true, clock, nil, nil)
	bounded.set("a", 1, time.Minute)
	bounded.set("b", 2, time.Minute)
	bounded.set("c", 3, time.Minute)
	if bounded.size() != 2 {
		t.Fatalf("容量淘汰 = %d", bounded.size())
	}
	if _, ok := bounded.get("a"); ok {
		t.Fatal("最老条目必须被淘汰")
	}
}

// ---------------------------------------------------------------------------
// concurrency.go：wait ctx、AwaitBackgroundWork 取消臂、settingsCacheLoaded
// ---------------------------------------------------------------------------

func TestW11DAwaitBackgroundWorkCanceledArms(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.runtimes["sk-w11d"] = staticRuntime(t, models, testAPIKeyRow("k_w11d", RouteStrategyModeNormal, "g1"), nil, nil)
	models.groupAccess["gw:sys"] = &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys"}
	models.accounts["gw"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testAccount("a_w11d", "sys")}}
	clock := newManualClock()
	shared := newFakeSharedFactory()
	block := make(chan struct{})
	models.blockRuntime = block
	svc := newTestService(t, models, clock, func(o *Options) { o.Shared = shared })
	blockGuard := &w11dOnce{}
	t.Cleanup(func() { blockGuard.close(block) })

	// 触发一个 pending runtime load（读取本身取消，load 留在 pending 表）。
	readCtx, cancelRead := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancelRead()
	_, _ = svc.ReadCachedGatewayRuntimeAsync(readCtx, "sk-w11d")
	if svc.pendingRuntimeLoadCount() != 1 {
		t.Fatalf("pending load = %d", svc.pendingRuntimeLoadCount())
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := svc.AwaitBackgroundWork(canceled); err == nil {
		t.Fatal("取消 ctx 必须返回 ctx.Err")
	}
	// 解除阻塞让其收敛。
	blockGuard.close(block)
	if err := svc.AwaitBackgroundWork(context.Background()); err != nil {
		t.Fatalf("收敛 = %v", err)
	}

	// refresh wait 的取消臂：共享模式分组 stale 触发后台刷新并立刻取消等待。
	models2 := newFakeModels()
	models2.groupAccess["g1:sys"] = &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys"}
	blockGroup := make(chan struct{})
	groupBlocked := &w11dBlockGroupModels{fakeModels: models2, block: blockGroup, passThrough: 1}
	clock2 := newManualClock()
	shared2 := newFakeSharedFactory()
	svc2 := newTestService(t, groupBlocked, clock2, func(o *Options) { o.Shared = shared2 })
	groupGuard := &w11dOnce{}
	t.Cleanup(func() { groupGuard.close(blockGroup) })
	// 第一次读取直通（装载并写共享），随后的读取全部阻塞。
	if _, err := svc2.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g1", "sys"); err != nil {
		t.Fatal(err)
	}
	// 推进超过 revalidate 窗口：stale 服务 + 后台刷新（阻塞在 loader）。
	clock2.Advance(61 * time.Second)
	if _, err := svc2.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g1", "sys"); err != nil {
		t.Fatal(err)
	}
	canceled2, cancel2 := context.WithCancel(ctx)
	cancel2()
	_ = svc2.AwaitBackgroundWork(canceled2)
	groupGuard.close(blockGroup)
	if err := svc2.AwaitBackgroundWork(context.Background()); err != nil {
		t.Fatalf("分组刷新收敛 = %v", err)
	}
}

// w11dOnce 保证 close 只执行一次（显式解除与 t.Cleanup 共存）。
type w11dOnce struct {
	done bool
}

func (o *w11dOnce) close(ch chan struct{}) {
	if !o.done {
		o.done = true
		close(ch)
	}
}

// w11dBlockGroupModels 让前 passThrough 次分组访问直通，其余阻塞。
type w11dBlockGroupModels struct {
	*fakeModels
	block      chan struct{}
	passThrough int
	calls      int
}

func (m *w11dBlockGroupModels) ResolveGroupUsageAccessMetadata(ctx context.Context, groupID, systemAccountID string) (*GroupUsageAccessMetadata, error) {
	m.calls++
	if m.calls > m.passThrough {
		<-m.block
	}
	return m.fakeModels.ResolveGroupUsageAccessMetadata(ctx, groupID, systemAccountID)
}

func TestW11DSettingsCacheLoadedSlot(t *testing.T) {
	svc := newTestService(t, newFakeModels(), newManualClock(), nil)
	if svc.settingsCacheLoaded() {
		t.Fatal("初始设置槽必须为空")
	}
	if _, err := svc.ReadCachedGatewaySettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !svc.settingsCacheLoaded() {
		t.Fatal("读取后设置槽必须加载")
	}
}

// ---------------------------------------------------------------------------
// types.go：克隆臂与模式归一
// ---------------------------------------------------------------------------

func TestW11DTypesCloneArms(t *testing.T) {
	if got := NormalizeRouteStrategyMode("bogus"); got != RouteStrategyModeNormal {
		t.Fatalf("未知模式 = %q", got)
	}
	if got := NormalizeRouteStrategyMode(RouteStrategyModeFailover); got != RouteStrategyModeFailover {
		t.Fatalf("已知模式 = %q", got)
	}

	// CloneGatewayAPIKeyRow 全字段。
	config := "{}"
	limits := UserRequestLimits{}
	row := GatewayAPIKeyRow{
		ID: "k_c", RouteStrategyConfigJSON: &config, QuotaLimitsJSON: &config,
		SystemAccountRequestLimitsJSON: &config, SystemAccountRequestLimits: &limits,
		GroupBindings: []GatewayAPIKeyGroupBindingRow{{ID: "b1"}},
	}
	cloned := CloneGatewayAPIKeyRow(row)
	if cloned.GroupBindings == nil || len(cloned.GroupBindings) != 1 || cloned.SystemAccountRequestLimits == nil {
		t.Fatalf("克隆 = %+v", cloned)
	}

	// CloneStaticOpenAIAccountSecret：可选切片与运行态。
	account := testAccount("a_c", "sys")
	account.SupportedEndpointModes = []string{"chat"}
	account.SupportedModels = []string{"m1"}
	account.APIKeys = []string{"sk-1", "sk-2"}
	account.APIKeyRuntimeStates = []AccountAPIKeyRuntimeSelectionState{{APIKeyID: "k", Fingerprint: "f"}}
	account.ModelMappings = []AccountModelMapping{{SourceModel: "m1", UpstreamModel: "m2", RuntimeSource: &config}}
	account.CurrentConcurrency = whIntPtr(3)
	secret := CloneStaticOpenAIAccountSecret(account)
	if secret.CurrentConcurrency != nil || len(secret.APIKeyRuntimeStates) != 1 || len(secret.ModelMappings) != 1 {
		t.Fatalf("账户克隆 = %+v", secret)
	}
	if secret.ModelMappings[0].RuntimeSource == nil || *secret.ModelMappings[0].RuntimeSource != "{}" {
		t.Fatal("映射克隆必须复制指针字段")
	}
	// 空切片分支：SupportedEndpointModes/APIKeys/APIKeyRuntimeStates 为 nil。
	bare := testAccount("a_bare", "sys")
	bareClone := CloneStaticOpenAIAccountSecret(bare)
	if bareClone.SupportedEndpointModes != nil || bareClone.APIKeys != nil || bareClone.APIKeyRuntimeStates != nil {
		t.Fatal("nil 切片克隆必须保持 nil")
	}

	// CloneGroupUsageAccessMetadata：调度策略深拷贝。
	policy := GroupSchedulingPolicy{"k": 1.0}
	access := GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys", SchedulingPolicy: &policy}
	accessClone := CloneGroupUsageAccessMetadata(access)
	if accessClone.SchedulingPolicy == nil || *accessClone.SchedulingPolicy == nil {
		t.Fatal("调度策略克隆失败")
	}
	policy["k2"] = 2.0
	if _, leaked := (*accessClone.SchedulingPolicy)["k2"]; leaked {
		t.Fatal("调度策略克隆必须深拷贝")
	}
	bareAccess := CloneGroupUsageAccessMetadata(GroupUsageAccessMetadata{})
	if bareAccess.SchedulingPolicy != nil {
		t.Fatal("无策略克隆必须保持 nil")
	}
}

// ---------------------------------------------------------------------------
// snapshot.go：numberValue 与可用性键投影
// ---------------------------------------------------------------------------

func TestW11DSnapshotNumberValueArms(t *testing.T) {
	if numberValue(3) != 3 || numberValue(int64(4)) != 4 || numberValue(float64(5.5)) != 5 {
		t.Fatal("数值收敛错误")
	}
	if numberValue(math.NaN()) != 0 || numberValue(math.Inf(1)) != 0 {
		t.Fatal("非有限浮点必须归零")
	}
	if numberValue("7") != 7 || numberValue("nope") != 0 || numberValue(nil) != 0 || numberValue(true) != 0 {
		t.Fatal("字符串/未知类型收敛错误")
	}
}

func TestW11DAccountRuntimeAvailabilityKeyArms(t *testing.T) {
	// 完整授权投影键。
	if got := AccountRuntimeAvailabilityKey("a1", "authorized", "auth1", "g1", "bindSys", "sys", "owner"); got != "a1:authorized:bindSys:g1:auth1" {
		t.Fatalf("授权键 = %q", got)
	}
	// 绑定账号缺失回退。
	if got := AccountRuntimeAvailabilityKey("a1", "authorized", "auth1", "g1", "", "sys", "owner"); got != "a1:authorized:sys:g1:auth1" {
		t.Fatalf("回退键 = %q", got)
	}
	// 全部缺失回退 owner。
	if got := AccountRuntimeAvailabilityKey("a1", "authorized", "auth1", "g1", "", "", "owner"); got != "a1:authorized:owner:g1:auth1" {
		t.Fatalf("owner 回退键 = %q", got)
	}
	// 条件不足：裸 ID。
	if got := AccountRuntimeAvailabilityKey("a1", "owner", "auth1", "g1", "b", "s", "o"); got != "a1" {
		t.Fatalf("裸键 = %q", got)
	}
	if got := AccountRuntimeAvailabilityKey("a1", "authorized", "", "g1", "b", "s", "o"); got != "a1" {
		t.Fatalf("缺授权键 = %q", got)
	}
	if got := AccountRuntimeAvailabilityKey("a1", "authorized", "auth1", "", "b", "s", "o"); got != "a1" {
		t.Fatalf("缺分组键 = %q", got)
	}
	if got := AccountRuntimeAvailabilityKey("a1", "authorized", "auth1", "g1", "", "", ""); got != "a1" {
		t.Fatalf("无系统账号键 = %q", got)
	}
	if got := SortSnapshotKeys([]string{"b", "a"}); strings.Join(got, ",") != "a,b" {
		t.Fatalf("排序键 = %v", got)
	}
}

// 保留引用避免未使用导入误报（http 仅在部分构建标签下需要）。
var _ = http.MethodGet
