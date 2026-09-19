package gatewayruntimecache

// w11d 覆盖补齐（四）：settings / groupaccess / inspection 共享模式与后台
// 刷新臂、catalog pending 与共享命中、runtime 索引与净化、registry 直构臂、
// SQL 模型剩余查询错误臂与投影错误臂。

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// settings.go
// ---------------------------------------------------------------------------

func TestW11DSettingsCacheArms(t *testing.T) {
	ctx := context.Background()
	failing := &w11dSettingsFailingModels{fakeModels: newFakeModels()}
	logger := &whWarnLogger{}
	clock := newManualClock()

	// 同步读 loader 失败。
	svc := newTestService(t, failing, clock, nil)
	if _, err := svc.ReadCachedGatewaySettings(ctx); err == nil {
		t.Fatal("settings loader 失败必须上抛")
	}
	// 异步无共享 → 同步委托。
	if _, err := svc.ReadCachedGatewaySettingsAsync(ctx); err == nil {
		t.Fatal("异步委托 loader 失败必须上抛")
	}
	// 共享读失败告警 + loader 失败。
	shared := newFakeSharedFactory()
	svc2 := newTestService(t, failing, clock, func(o *Options) {
		o.Shared = shared
		o.Logger = logger
	})
	svc2.sharedSettings = &whReadErrShared{}
	if _, err := svc2.ReadCachedGatewaySettingsAsync(ctx); err == nil {
		t.Fatal("共享读失败后 loader 失败必须上抛")
	}
	found := false
	for _, event := range logger.snapshot() {
		if event == "gateway_settings_shared_cache_read_failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("settings 共享读失败告警缺失")
	}
	// 共享写失败告警。
	okModels := newFakeModels()
	svc3 := newTestService(t, okModels, clock, func(o *Options) {
		o.Shared = shared
		o.Logger = logger
	})
	svc3.sharedSettings = &whFailingShared{failSet: true}
	if _, err := svc3.ReadCachedGatewaySettingsAsync(ctx); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, event := range logger.snapshot() {
		if event == "gateway_settings_shared_cache_write_failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("settings 共享写失败告警缺失")
	}
	// 共享命中（新进程同工厂）：先让一个服务成功写入共享，再验证冷进程不触发 loader。
	populateSvc, err := New(okModels, Options{Clock: clock, Shared: shared})
	if err != nil {
		t.Fatal(err)
	}
	defer populateSvc.Close()
	if _, err := populateSvc.ReadCachedGatewaySettingsAsync(ctx); err != nil {
		t.Fatal(err)
	}
	calls := okModels.settingsCalls
	cold, err := New(okModels, Options{Clock: clock, Shared: shared})
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	if _, err := cold.ReadCachedGatewaySettingsAsync(ctx); err != nil {
		t.Fatal(err)
	}
	if okModels.settingsCalls != calls {
		t.Fatalf("共享命中不得触发 loader: %d -> %d", calls, okModels.settingsCalls)
	}
	// 默认时钟。
	defaultClock, err := New(okModels, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer defaultClock.Close()
}

// w11dSettingsFailingModels 设置 loader 恒失败。
type w11dSettingsFailingModels struct{ *fakeModels }

func (m *w11dSettingsFailingModels) ReadGatewaySettings(context.Context) (GatewaySettings, error) {
	return GatewaySettings{}, errors.New("w11d settings down")
}

// ---------------------------------------------------------------------------
// groupaccess.go：共享模式后台刷新全臂
// ---------------------------------------------------------------------------

func TestW11DGroupAccessSharedRefreshArms(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.groupAccess["g1:sys"] = &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys"}
	recorder := &w11dGroupRecorder{fakeModels: models}
	clock := newManualClock()
	shared := newFakeSharedFactory()
	logger := &whWarnLogger{}
	svc := newTestService(t, recorder, clock, func(o *Options) {
		o.Shared = shared
		o.Logger = logger
	})
	// 首读装载共享。
	if _, err := svc.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g1", "sys"); err != nil {
		t.Fatal(err)
	}
	// 推进过 revalidate：共享命中 stale → 后台刷新（loader 失败告警）。
	clock.Advance(61 * time.Second)
	recorder.fail = true
	if _, err := svc.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g1", "sys"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AwaitBackgroundWork(ctx); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range logger.snapshot() {
		if event == "gateway_group_access_stale_refresh_failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("分组后台刷新失败告警缺失: %v", logger.snapshot())
	}
	// 世代失效：刷新结果不回填。
	recorder.fail = false
	clock.Advance(61 * time.Second)
	svc.ClearGatewayRuntimeCacheLocal(ClearOptions{}) // 推进 runtimeGeneration
	if _, err := svc.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g1", "sys"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AwaitBackgroundWork(ctx); err != nil {
		t.Fatal(err)
	}
	// loader 返回坏 expiresAt：刷新条目构建失败告警。
	recorder.badExpiry = true
	clock.Advance(61 * time.Second)
	if _, err := svc.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g1", "sys"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AwaitBackgroundWork(ctx); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, event := range logger.snapshot() {
		if strings.Contains(event, "gateway_group_access_stale_refresh_failed") {
			found = true
		}
	}
	if !found {
		t.Fatal("坏 expiresAt 刷新告警缺失")
	}
	// 共享写失败告警：装载路径 setGroupUsageAccessCacheEntry 写共享失败。
	svc2 := newTestService(t, models, clock, func(o *Options) {
		o.Shared = shared
		o.Logger = logger
	})
	svc2.sharedGroup = &whFailingShared{failSet: true}
	if _, err := svc2.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g9", "sys"); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, event := range logger.snapshot() {
		if event == "gateway_group_usage_access_shared_cache_write_failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("分组共享写失败告警缺失")
	}
	// 共享读失败告警。
	svc3 := newTestService(t, models, clock, func(o *Options) { o.Logger = logger })
	svc3.sharedGroup = &whReadErrShared{}
	if _, err := svc3.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g2", "sys"); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, event := range logger.snapshot() {
		if event == "gateway_group_usage_access_shared_cache_read_failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("分组共享读失败告警缺失")
	}
	// 共享 miss + loader 失败 / 坏 expiresAt。
	failingSharedMiss := newTestService(t, &w11dGroupFailingModels{fakeModels: models}, clock, func(o *Options) { o.Logger = logger })
	failingSharedMiss.sharedGroup = &whReadErrShared{}
	if _, err := failingSharedMiss.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g3", "sys"); err == nil {
		t.Fatal("共享 miss + loader 失败必须上抛")
	}
	badModels := newFakeModels()
	bad := "bad-instant"
	badModels.groupAccess["g4:sys"] = &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys", GroupAuthorizationExpiresAt: &bad}
	svc4 := newTestService(t, badModels, clock, func(o *Options) { o.Shared = newFakeSharedFactory() })
	if _, err := svc4.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g4", "sys"); err == nil {
		t.Fatal("坏 expiresAt 必须报错")
	}
	// pending 去重臂：首读直通后，stale 读取触发的后台刷新被阻塞观察。
	blocker := make(chan struct{})
	guard := &w11dOnce{}
	t.Cleanup(func() { guard.close(blocker) })
	blocked := &w11dGroupBlockRefreshModels{fakeModels: models, block: blocker, passThrough: 1}
	clock5 := newManualClock()
	svc5 := newTestService(t, blocked, clock5, func(o *Options) { o.Shared = newFakeSharedFactory() })
	if _, err := svc5.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g1", "sys"); err != nil {
		t.Fatal(err)
	}
	// 推进 61s：共享命中 stale → 后台刷新（第 2 次调用阻塞）→ pending 登记。
	clock5.Advance(61 * time.Second)
	if _, err := svc5.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g1", "sys"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		svc5.mu.Lock()
		defer svc5.mu.Unlock()
		return len(svc5.pendingGroupRefreshes) > 0
	})
	// 再次读取（仍 stale）：去重臂直接返回，不新增 pending。
	if _, err := svc5.ResolveCachedGroupUsageAccessMetadataAsync(ctx, "g1", "sys"); err != nil {
		t.Fatal(err)
	}
	svc5.mu.Lock()
	pendingCount := len(svc5.pendingGroupRefreshes)
	svc5.mu.Unlock()
	if pendingCount != 1 {
		t.Fatalf("pending 去重 = %d", pendingCount)
	}
	guard.close(blocker)
	if err := svc5.AwaitBackgroundWork(ctx); err != nil {
		t.Fatalf("刷新收敛 = %v", err)
	}
	// toEntry / toShared nil 臂。
	if entry := (sharedGroupUsageAccessEntry{}).toEntry(); entry.value != nil {
		t.Fatal("null value toEntry 必须是负缓存")
	}
	if shared := (groupUsageAccessCacheEntry{}).toShared(); shared.Value != nil {
		t.Fatal("负缓存 toShared 必须是 null value")
	}
	policy := GroupSchedulingPolicy{"a": 1.0}
	posEntry := groupUsageAccessCacheEntry{value: &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "s", SchedulingPolicy: &policy}}
	if sharedPos := posEntry.toShared(); sharedPos.Value == nil || sharedPos.Value.SchedulingPolicy == nil {
		t.Fatal("正向 toShared 必须携带值")
	}
	if entryPos := (sharedGroupUsageAccessEntry{Value: &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "s"}}).toEntry(); entryPos.value == nil {
		t.Fatal("正向 toEntry 必须携带值")
	}
}

// w11dGroupRecorder 可控分组 loader。
type w11dGroupRecorder struct {
	*fakeModels
	fail      bool
	badExpiry bool
}

func (m *w11dGroupRecorder) ResolveGroupUsageAccessMetadata(ctx context.Context, groupID, systemAccountID string) (*GroupUsageAccessMetadata, error) {
	if m.fail {
		return nil, errors.New("w11d group refresh fail")
	}
	value, err := m.fakeModels.ResolveGroupUsageAccessMetadata(ctx, groupID, systemAccountID)
	if m.badExpiry && value != nil {
		bad := "bad-instant"
		value.GroupAuthorizationExpiresAt = &bad
	}
	return value, err
}

// w11dGroupBlockRefreshModels 让前 passThrough 次分组访问直通，其余阻塞。
type w11dGroupBlockRefreshModels struct {
	*fakeModels
	block      chan struct{}
	passThrough int
	calls      int
}

func (m *w11dGroupBlockRefreshModels) ResolveGroupUsageAccessMetadata(ctx context.Context, groupID, systemAccountID string) (*GroupUsageAccessMetadata, error) {
	m.calls++
	if m.calls > m.passThrough {
		<-m.block
	}
	return m.fakeModels.ResolveGroupUsageAccessMetadata(ctx, groupID, systemAccountID)
}

// ---------------------------------------------------------------------------
// inspection.go：本地 stale 刷新与后台刷新臂
// ---------------------------------------------------------------------------

func TestW11DInspectionLocalStaleRefreshArms(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.policies["openai:gpt"] = []ResponseInspectionPolicySummary{{
		ID: "p1", Name: "n", Enabled: true, Priority: 1, ScopeType: "provider",
		ProtocolCode: "openai", Match: ResponseInspectionPolicyMatch{}, Action: "observe",
	}}
	recorder := &w11dPolicyRecorder{fakeModels: models}
	clock := newManualClock()
	logger := &whWarnLogger{}
	svc := newTestService(t, recorder, clock, func(o *Options) { o.Logger = logger })
	// 首读装载本地缓存。
	if _, err := svc.ListCachedActiveResponseInspectionPoliciesAsync(ctx, "openai", "gpt"); err != nil {
		t.Fatal(err)
	}
	// 推进 61s：本地 stale → 后台刷新（loader 失败告警）。
	clock.Advance(61 * time.Second)
	recorder.fail = true
	if _, err := svc.ListCachedActiveResponseInspectionPoliciesAsync(ctx, "openai", "gpt"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AwaitBackgroundWork(ctx); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range logger.snapshot() {
		if event == "gateway_response_inspection_policy_stale_refresh_failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("检查策略后台刷新失败告警缺失: %v", logger.snapshot())
	}
	// loader 失败直接上抛（缓存空时）。
	svc2 := newTestService(t, recorder, clock, nil)
	if _, err := svc2.ListCachedActiveResponseInspectionPoliciesAsync(ctx, "openai", "other"); err == nil {
		t.Fatal("检查策略 loader 失败必须上抛")
	}
	// 共享命中 + stale：刷新 + 世代失效跳过回填。
	shared := newFakeSharedFactory()
	recorder2 := &w11dPolicyRecorder{fakeModels: models}
	svc3 := newTestService(t, recorder2, newManualClock(), func(o *Options) { o.Shared = shared })
	if _, err := svc3.ListCachedActiveResponseInspectionPoliciesAsync(ctx, "openai", "gpt"); err != nil {
		t.Fatal(err)
	}
	// 冷进程共享命中（无 stale：不触发刷新）。
	cold, err := New(models, Options{Clock: newManualClock(), Shared: shared})
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	if _, err := cold.ListCachedActiveResponseInspectionPoliciesAsync(ctx, "openai", "gpt"); err != nil {
		t.Fatal(err)
	}
}

// w11dPolicyRecorder 可控检查策略 loader。
type w11dPolicyRecorder struct {
	*fakeModels
	fail bool
}

func (m *w11dPolicyRecorder) ListActiveResponseInspectionPolicies(ctx context.Context, protocolCode, providerCode string) ([]ResponseInspectionPolicySummary, error) {
	if m.fail {
		return nil, errors.New("w11d policy refresh fail")
	}
	return m.fakeModels.ListActiveResponseInspectionPolicies(ctx, protocolCode, providerCode)
}

// ---------------------------------------------------------------------------
// catalog.go：pending 路径与共享路由索引命中
// ---------------------------------------------------------------------------

func TestW11DCatalogPendingAndSharedRouteHit(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.catalog["gpt"] = []ProviderModelCatalogItem{{ProviderCode: "gpt", Model: "m1"}, {ProviderCode: "gpt", Model: " "}}
	clock := newManualClock()
	shared := newFakeSharedFactory()
	logger := &whWarnLogger{}
	svc := newTestService(t, models, clock, func(o *Options) {
		o.Shared = shared
		o.Logger = logger
	})
	// 首次解析：构建索引（含空模型跳过）并写共享。
	if _, err := svc.ResolveCachedProviderModelRouteAsync(ctx, "m1", []string{"gpt"}, "sys", false); err != nil {
		t.Fatal(err)
	}
	// 冷进程共享命中路径。
	cold, err := New(models, Options{Clock: clock, Shared: shared})
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	resolution, err := cold.ResolveCachedProviderModelRouteAsync(ctx, "m1", []string{"gpt"}, "sys", false)
	if err != nil || resolution.Outcome != ProviderModelRouteMatched {
		t.Fatalf("共享命中 = %+v err=%v", resolution, err)
	}
	// 共享读失败告警 + 共享目录读失败告警。
	failing := newTestService(t, models, clock, func(o *Options) { o.Logger = logger })
	failing.sharedRouteIdx = &whReadErrShared{}
	failing.sharedCatalog = &whReadErrShared{}
	if _, err := failing.ResolveCachedProviderModelRouteAsync(ctx, "m1", []string{"gpt"}, "sys", false); err != nil {
		t.Fatal(err)
	}
	events := strings.Join(logger.snapshot(), ",")
	if !strings.Contains(events, "gateway_provider_model_route_index_shared_cache_read_failed") {
		t.Fatal("路由索引共享读失败告警缺失")
	}
	if !strings.Contains(events, "gateway_provider_model_catalog_shared_cache_read_failed") {
		t.Fatal("目录共享读失败告警缺失")
	}
	// toEntry 畸形条目臂。
	malformed := sharedProviderModelRouteIndexEntry{Entries: [][2]any{{"only-one"}, {"m1", []any{"gpt"}}, {1, []any{"x"}}}}
	if entry := malformed.toEntry(); len(entry.index) != 1 || len(entry.index["m1"]) != 1 {
		t.Fatalf("畸形条目收敛 = %v", entry.index)
	}
	codesOnly := sharedProviderModelRouteIndexEntry{Entries: [][2]any{{"m1", "not-a-list"}}}
	if entry := codesOnly.toEntry(); len(entry.index) != 0 {
		t.Fatal("非列表 codes 必须跳过")
	}
	// 混合类型 codes。
	mixed := sharedProviderModelRouteIndexEntry{Entries: [][2]any{{"m1", []any{"gpt", 1, "anthropic"}}}}
	if entry := mixed.toEntry(); len(entry.index["m1"]) != 2 {
		t.Fatalf("混合 codes 收敛 = %v", entry.index)
	}

	// pending singleflight：blocked loader + 并发第二次读取走 pending 分支。
	blocker := make(chan struct{})
	guard := &w11dOnce{}
	t.Cleanup(func() { guard.close(blocker) })
	blockedCatalog := &w11dPendingCatalogModels{fakeModels: models, block: blocker}
	svc2 := newTestService(t, blockedCatalog, clock, nil)
	started := make(chan struct{})
	blockedCatalog.onStart = func() { close(started) }
	input := ModelCatalogListOptions{ProviderCode: "pending", SystemAccountID: "sys"}
	done := make(chan error, 1)
	go func() {
		_, err := svc2.ListCachedProviderModelCatalogAsync(ctx, input)
		done <- err
	}()
	<-started
	// 等待 pending 登记。
	waitFor(t, 2*time.Second, func() bool {
		svc2.mu.Lock()
		defer svc2.mu.Unlock()
		return len(svc2.pendingCatalogLoads) > 0
	})
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc2.ListCachedProviderModelCatalogAsync(canceled, input); err == nil {
		t.Fatal("pending 路径取消 ctx 必须返回错误")
	}
	// AwaitBackgroundWork 在 pending 目录存在时的取消臂。
	if err := svc2.AwaitBackgroundWork(canceled); err == nil {
		t.Fatal("目录 pending 取消等待必须返回 ctx.Err")
	}
	guard.close(blocker)
	if err := <-done; err != nil {
		t.Fatalf("首读 = %v", err)
	}
	if err := svc2.AwaitBackgroundWork(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 第二次读取命中 pending 完成后的缓存。
	if _, err := svc2.ListCachedProviderModelCatalogAsync(ctx, input); err != nil {
		t.Fatal(err)
	}
}

// w11dPendingCatalogModels 阻塞目录 loader（singleflight 观察）。
type w11dPendingCatalogModels struct {
	*fakeModels
	block   chan struct{}
	onStart func()
	once    sync.Once
}

func (m *w11dPendingCatalogModels) ListProviderModelCatalog(ctx context.Context, input ModelCatalogListOptions) ([]ProviderModelCatalogItem, error) {
	if m.onStart != nil {
		m.once.Do(m.onStart)
	}
	<-m.block
	return m.fakeModels.ListProviderModelCatalog(ctx, input)
}

// ---------------------------------------------------------------------------
// runtime.go：populate 错误、身份索引、后台刷新去重、fresh selection 臂
// ---------------------------------------------------------------------------

func TestW11DRuntimePopulateAndIndexArms(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	svc := newTestService(t, models, newManualClock(), nil)

	// populate：GroupAccess 坏 expiresAt → 错误。
	bad := "bad-instant"
	row := testAPIKeyRow("k_g", RouteStrategyModeNormal, "g1")
	runtime := GatewayRuntime{APIKey: row, GroupAccess: &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys", GroupAuthorizationExpiresAt: &bad}}
	if err := svc.populateGatewayRuntimeCaches(HashSecret("sk-g"), runtime); err == nil {
		t.Fatal("分组坏 expiresAt 必须报错")
	}
	// populate：账户坏 expiresAt → 错误。
	row2 := testAPIKeyRow("k_a", RouteStrategyModeNormal, "g1")
	runtime2 := GatewayRuntime{
		APIKey:      row2,
		GroupAccess: &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys"},
		Accounts:    []OpenAIAccountSecret{{ID: "a1", Status: AccountStatusActive, ExpiresAt: &bad}},
	}
	if err := svc.populateGatewayRuntimeCaches(HashSecret("sk-a"), runtime2); err == nil {
		t.Fatal("账户坏 expiresAt 必须报错")
	}
	// APIKey nil → 负缓存条目。
	if err := svc.populateGatewayRuntimeCaches(HashSecret("sk-anon"), GatewayRuntime{Settings: models.settings}); err != nil {
		t.Fatal(err)
	}

	// 身份索引：同 cacheKey 换 apiKeyID → 旧索引迁移。
	rowB := testAPIKeyRow("k_b", RouteStrategyModeNormal, "g1")
	good := GatewayRuntime{APIKey: rowB, GroupAccess: &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys"}}
	svc.setGatewayRuntimeCacheEntry(HashSecret("sk-b"), gatewayRuntimeCacheEntry{runtime: good})
	svc.setGatewayRuntimeCacheEntry(HashSecret("sk-b"), gatewayRuntimeCacheEntry{runtime: good})
	svc.keysMu.Lock()
	if len(svc.keysByAPIKeyID["k_b"]) != 1 {
		svc.keysMu.Unlock()
		t.Fatal("身份索引必须登记")
	}
	svc.keysMu.Unlock()
	// removeGatewayRuntimeCacheIndex 各臂。
	svc.removeGatewayRuntimeCacheIndex("", "any")
	svc.removeGatewayRuntimeCacheIndex("unknown", "any")
	svc.keysMu.Lock()
	svc.keysByAPIKeyID["k_multi"] = map[string]struct{}{"h1": {}, "h2": {}}
	svc.keysMu.Unlock()
	svc.removeGatewayRuntimeCacheIndex("k_multi", "h1")
	svc.keysMu.Lock()
	if _, ok := svc.keysByAPIKeyID["k_multi"]["h1"]; ok || len(svc.keysByAPIKeyID["k_multi"]) != 1 {
		svc.keysMu.Unlock()
		t.Fatal("索引删除失败")
	}
	svc.keysMu.Unlock()
	svc.removeGatewayRuntimeCacheIndex("k_multi", "h2")
	svc.keysMu.Lock()
	if _, ok := svc.keysByAPIKeyID["k_multi"]; ok {
		svc.keysMu.Unlock()
		t.Fatal("空索引必须整组删除")
	}
	svc.keysMu.Unlock()
	// identity dispose / onClear 钩子。
	svc.setGatewayRuntimeCacheEntry(HashSecret("sk-dispose"), gatewayRuntimeCacheEntry{runtime: good})
	svc.identityCache.delete(HashSecret("sk-dispose"))
	svc.keysMu.Lock()
	_, indexed := svc.keysByAPIKeyID["k_b"]
	svc.keysMu.Unlock()
	if indexed {
		t.Log("k_b 索引仍在（dispose 只清目标键）")
	}
	svc.identityCache.clear()

	// refreshGatewayRuntimeInBackground 去重臂：手工登记 pending 后调用应直接返回。
	cacheKey := HashSecret("sk-dedupe")
	pending := &runtimeLoad{generation: svc.currentAPIKeyRuntimeGeneration(), done: make(chan struct{})}
	svc.mu.Lock()
	svc.pendingRuntimeLoads[cacheKey] = pending
	svc.mu.Unlock()
	svc.refreshGatewayRuntimeInBackground("sk-dedupe", cacheKey)
	svc.mu.Lock()
	still := svc.pendingRuntimeLoads[cacheKey] == pending
	svc.mu.Unlock()
	if !still {
		t.Fatal("去重臂必须保留原 pending")
	}
	close(pending.done)
	svc.mu.Lock()
	delete(svc.pendingRuntimeLoads, cacheKey)
	svc.mu.Unlock()

	// fresh selection：nil APIKey 直通。
	static, err := svc.routeCachedDynamicGatewayRuntimeWithFreshSelection(ctx, GatewayRuntime{Settings: models.settings})
	if err != nil || static.APIKey != nil {
		t.Fatalf("nil key fresh selection = %+v err=%v", static.APIKey, err)
	}
	// 绑定去重与空 GroupID 跳过。
	dynamicRow := testAPIKeyRow("k_dup", RouteStrategyModeHybridSmart, "g1", "g2")
	modelsDyn := newFakeModels()
	modelsDyn.groupAccess["g1:sys_owner"] = &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys_owner"}
	modelsDyn.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testAccount("a1", "sys_owner")}}
	svcDyn := newTestService(t, modelsDyn, newManualClock(), nil)
	dupOrderer := &w11dOrderer{ordered: []GatewayAPIKeyGroupBindingRow{
		{GroupID: "", Status: "active", GroupEnabled: 1},
		{GroupID: "g1", Status: "active", GroupEnabled: 1},
		{GroupID: "g1", Status: "active", GroupEnabled: 1},
	}}
	svcDyn.opts.Orderer = dupOrderer
	selected, err := svcDyn.routeCachedDynamicGatewayRuntimeWithFreshSelection(ctx, GatewayRuntime{APIKey: dynamicRow, Settings: modelsDyn.settings})
	if err != nil || selected.APIKey == nil || selected.APIKey.SelectedGroupID != "g1" {
		t.Fatalf("绑定去重 = %+v err=%v", selected.APIKey, err)
	}
	// 账户 loader 失败传播。
	accountsFail := &w11dFailingAccounts{fakeModels: modelsDyn, failAfter: 0}
	svcFail := newTestService(t, accountsFail, newManualClock(), nil)
	svcFail.opts.Orderer = dupOrderer
	if _, err := svcFail.routeCachedDynamicGatewayRuntimeWithFreshSelection(ctx, GatewayRuntime{APIKey: dynamicRow, Settings: modelsDyn.settings}); err == nil {
		t.Fatal("账户 loader 失败必须传播")
	}
	// rfc3339 解析失败臂：正则通过但 time.Parse 失败。
	if _, ok := rfc3339Millis("2026-13-45T99:99:99Z"); ok {
		t.Fatal("非法日历时间必须解析失败")
	}
}

// ---------------------------------------------------------------------------
// registry.go：直构臂、心跳再调度、条目上限
// ---------------------------------------------------------------------------

func TestW11DRegistryDirectArms(t *testing.T) {
	// publish !current 臂（直构，publishRequested=false）。
	idle := &Registry{}
	idle.publish(&registrySession{})
	// unregister 空 URL 臂（直构）。
	idle.unregister(context.Background(), "boot")
	// 命名空间空串归一。
	server := miniredis.RunT(t)
	empty, err := NewRegistry(RegistryConfig{RedisURL: "redis://" + server.Addr() + "/0", Namespace: "::", Secret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = empty.Close() })
	if empty.prefix != "juhe-ai:runtime:internal-gateway:v1:" || empty.indexKey != "juhe-ai:runtime:internal-gateway-index:v1" {
		t.Fatalf("空命名空间 = %q", empty.prefix)
	}
}

func TestW11DRegistryHeartbeatRescheduleAndEntryLimit(t *testing.T) {
	server := miniredis.RunT(t)
	url := "redis://" + server.Addr() + "/0"
	publisher, err := NewRegistry(RegistryConfig{
		RedisURL: url, Namespace: "w11dbeat", Secret: "w11d-secret",
		InstanceID: "w11d-beat", Port: 8124, PublisherEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = publisher.Close() })
	ctx := context.Background()
	client := w11dRedisClient(server.Addr())
	defer client.Close()

	publisher.Start()
	entryKey := publisher.EntryKey("w11d-beat")
	waitFor(t, 3*time.Second, func() bool {
		return client.Exists(ctx, entryKey).Val() == 1
	})
	// 手动把 TTL 缩到 2s：若 5s 心跳重调度生效，条目会被重新发布而续命。
	if err := client.Expire(ctx, entryKey, 2*time.Second).Err(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 8*time.Second, func() bool {
		ttl := client.TTL(ctx, entryKey).Val()
		return ttl > 3*time.Second
	})
	if err := publisher.Stop(ctx); err != nil {
		t.Fatalf("Stop = %v", err)
	}

	// 条目上限：种子化 70 个有效条目，枚举最多 64 个。
	reader, err := NewRegistry(RegistryConfig{
		RedisURL: url, Namespace: "w11dbeat", Secret: "w11d-secret", ReaderEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	for i := 0; i < registryEntryLimit+6; i++ {
		instanceID := "w11d-lot"
		if i >= registryEntryLimit {
			instanceID = "w11d-extra"
		}
		entry := registryEntry{Version: registryEntryVersion, InstanceID: instanceID, Origin: "http://127.0.0.1:9000", BootID: "boot"}
		entry.Signature = registrySignature(entry.Version, entry.InstanceID, entry.Origin, entry.BootID, "w11d-secret")
		key := reader.EntryKey(instanceID) + "-" + string(rune('a'+i%26))
		if err := client.Set(ctx, key, w11dMustMarshal(t, entry), time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
		if err := client.ZAdd(ctx, reader.indexKey, w11dZ(float64(time.Now().UnixMilli()), key)).Err(); err != nil {
			t.Fatal(err)
		}
	}
	endpoints, err := reader.ListEndpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) > registryEntryLimit {
		t.Fatalf("条目上限 = %d", len(endpoints))
	}
}

func w11dZ(score float64, member string) redis.Z { return redis.Z{Score: score, Member: member} }

func w11dRedisClient(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{Addr: addr})
}
// accounts.go / sqlmodels.go / sqlruntime.go 剩余臂
// ---------------------------------------------------------------------------

func TestW11DAccountsOverlayDirectArms(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.accounts["gload"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testAccount("a1", "sys")}}
	svc := newTestService(t, models, newManualClock(), nil)
	// loadOpenAIAccountsForGroupAndPopulateCache：loader 失败臂。
	failingAccounts := &w11dFailingAccounts{fakeModels: models, failAfter: 0}
	svcFail := newTestService(t, failingAccounts, newManualClock(), nil)
	cacheKey := gatewayOpenAIAccountsCacheKey("gload", "sys", "", "")
	if _, err := svcFail.loadOpenAIAccountsForGroupAndPopulateCache(ctx, "gload", "sys", cacheKey, "", ""); err == nil {
		t.Fatal("loader 失败必须上抛")
	}
	// applyConcurrencyOverlay 坏 expiresAt 臂。
	bad := "bad-instant"
	badAccount := testAccount("a_bad2", "sys")
	badAccount.ExpiresAt = &bad
	if _, err := svc.applyConcurrencyOverlay([]OpenAIAccountSecret{badAccount}, map[string]int{}); err == nil {
		t.Fatal("覆盖层坏 expiresAt 必须报错")
	}
	// CredentialSourceAccountID 覆盖并发键。
	withSource := testAccount("a_src", "sys")
	source := " cred-src "
	withSource.CredentialSourceAccountID = &source
	ids := gatewayAccountConcurrencyAccountIDs([]OpenAIAccountSecret{withSource, testAccount("a_plain", "sys")})
	if len(ids) != 2 || ids[0] != "cred-src" || ids[1] != "a_plain" {
		t.Fatalf("并发键归一 = %v", ids)
	}
}

func TestW11DSQLProjectionDirectArms(t *testing.T) {
	ctx := context.Background()
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	plain, err := NewSQLReadModels(db, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := plain.settings.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	projectionKeys := []string{
		"gatewayTextRawBodyLimitMegabytes", "accountCircuitConfirmationFailuresRequired",
		"gatewayUserRequestLimitPerMinute", "gatewayUserRequestLimitPerDay",
		"gatewayUserRequestLimitPerWeek", "gatewayUserRequestLimitPerMonth",
		"defaultTemporaryUnschedulableMinutes", "temporaryUnschedulableRetryIntervalSeconds",
		"temporaryUnschedulableRetryAttempts", "textFirstResponseTimeoutSeconds",
		"textStreamIdleTimeoutSeconds", "textUncommittedAttemptMaxLifetimeSeconds",
		"imageFirstResponseTimeoutSeconds", "imageStreamIdleTimeoutSeconds",
		"imageUncommittedAttemptMaxLifetimeSeconds", "imageRequestWallTimeoutSeconds",
		"noAvailableAccountWaitTimeoutSeconds", "streamFailureThresholdCount",
		"streamFailureThresholdWindowMinutes",
	}
	for _, key := range projectionKeys {
		mutated := map[string]any{}
		for k, v := range raw {
			mutated[k] = v
		}
		mutated[key] = "w11d-not-a-number"
		if _, err := projectGatewaySettings(mutated); err == nil {
			t.Fatalf("%s 非数值必须报错", key)
		}
		mutated[key] = -1.0
		if _, err := projectGatewaySettings(mutated); err == nil {
			t.Fatalf("%s 负数必须报错", key)
		}
	}
	// numberSetting NaN/Inf/非整数臂。
	nan := map[string]any{}
	for k, v := range raw {
		nan[k] = v
	}
	nan["streamFailureThresholdCount"] = math.NaN()
	if _, err := projectGatewaySettings(nan); err == nil {
		t.Fatal("NaN 必须报错")
	}
	nan["streamFailureThresholdCount"] = math.Inf(1)
	if _, err := projectGatewaySettings(nan); err == nil {
		t.Fatal("Inf 必须报错")
	}
	nan["streamFailureThresholdCount"] = 2.5
	if _, err := projectGatewaySettings(nan); err == nil {
		t.Fatal("非整数必须报错")
	}
}

func TestW11DSQLAuthorizationFailureArms(t *testing.T) {
	ctx := context.Background()
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	if _, err := db.Exec(`DROP TABLE resource_authorizations`); err != nil {
		t.Fatal(err)
	}
	models, err := NewSQLReadModels(db, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_accounts (id, status) VALUES ('sys_owner', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups (id, system_account_id, provider_code, enabled) VALUES ('g1', 'sys_owner', 'gpt', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := models.ResolveGroupUsageAccessMetadata(ctx, "g1", "sys_other"); err == nil {
		t.Fatal("授权查询失败必须上抛")
	}
	// 本地设置查询失败：drop group_authorization_settings。
	if _, err := db.Exec(`CREATE TABLE resource_authorizations (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, grantee_system_account_id TEXT NOT NULL, status TEXT NOT NULL, effective_source_type TEXT, effective_source_team_id TEXT, expires_at TEXT, limits_json TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE group_authorization_settings`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations (id, resource_type, resource_id, grantee_system_account_id, status) VALUES ('auth1', 'group', 'g1', 'sys_other', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := models.ResolveGroupUsageAccessMetadata(ctx, "g1", "sys_other"); err == nil {
		t.Fatal("本地设置查询失败必须上抛")
	}
}

func TestW11DSQLLocalTypeInvalidArms(t *testing.T) {
	ctx := context.Background()
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	models, err := NewSQLReadModels(db, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := []string{
		`INSERT INTO system_accounts (id, status) VALUES ('sys_owner', 'active')`,
		`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type, scheduling_policy_json) VALUES ('g1', 'sys_owner', 'gpt', 1, 'high_concurrency', '{"maxConcurrency":5}')`,
		`INSERT INTO resource_authorizations (id, resource_type, resource_id, grantee_system_account_id, status) VALUES ('auth1', 'group', 'g1', 'sys_other', 'active')`,
		`INSERT INTO group_authorization_settings (authorization_id, system_account_id, group_id, enabled, group_type) VALUES ('auth1', 'sys_other', 'g1', 1, 'weird')`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	if _, err := models.ResolveGroupUsageAccessMetadata(ctx, "g1", "sys_other"); err == nil {
		t.Fatal("本地非法 group_type 必须报错")
	}
}

func TestW11DSQLRuntimeCompositionTailArms(t *testing.T) {
	ctx := context.Background()
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	models, err := NewSQLReadModels(db, false, nil, seamAccountsSelector{results: map[string]OpenAIAccountsForGroupResult{
		"g1": {Accounts: []OpenAIAccountSecret{testAccount("a1", "sys_owner")}},
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := []string{
		`INSERT INTO system_accounts (id, status) VALUES ('sys_owner', 'active')`,
		`INSERT INTO groups (id, system_account_id, provider_code, enabled) VALUES ('g1', 'sys_owner', 'gpt', 1)`,
		`INSERT INTO groups (id, system_account_id, provider_code, enabled) VALUES ('g2', 'sys_owner', 'gpt', 1)`,
		`INSERT INTO route_strategies (id, system_account_id, mode, status) VALUES ('rs1', 'sys_owner', 'normal', 'active')`,
		`INSERT INTO api_keys (id, system_account_id, route_strategy_id, key_hash, status) VALUES ('key4', 'sys_owner', 'rs1', '` + HashSecret("sk-w11d-sql4") + `', 'active')`,
		// 单绑定：空账户也会走到检查策略扇出（多候选时空账户组会被跳过）。
		`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at) VALUES ('b41', 'rs1', 'sys_owner', 'g1', 1, 1, 'active', '2026-01-01T00:00:00.000Z')`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	// 单候选带账户 → 进入检查策略扇出，返回完整组合。
	runtime, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-w11d-sql4"))
	if err != nil || runtime.APIKey == nil || len(runtime.Accounts) != 1 {
		t.Fatalf("组合读取 = %+v err=%v", runtime.APIKey, err)
	}
	// 检查策略查询失败：drop 表后重读必须上抛。
	if _, err := db.Exec(`DROP TABLE response_inspection_policies`); err != nil {
		t.Fatal(err)
	}
	if _, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-w11d-sql4")); err == nil {
		t.Fatal("检查策略查询失败必须上抛")
	}
	// uniqueInspectionScopes 空协议与重复臂。
	scopes := uniqueInspectionScopes([]OpenAIAccountSecret{
		{ProtocolCode: " "},
		{ProtocolCode: "openai", ProviderCode: "gpt"},
		{ProtocolCode: "openai", ProviderCode: "gpt"},
	})
	if len(scopes) != 1 {
		t.Fatalf("scope 去重 = %v", scopes)
	}
}

func TestW11DSQLBindingFailureArms(t *testing.T) {
	ctx := context.Background()
	db := newSQLTestDB(t)
	seedGatewaySettingsKeys(t, db)
	models, err := NewSQLReadModels(db, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := []string{
		`INSERT INTO system_accounts (id, status) VALUES ('sys_owner', 'active')`,
		`INSERT INTO route_strategies (id, system_account_id, mode, status) VALUES ('rs1', 'sys_owner', 'normal', 'active')`,
		`INSERT INTO api_keys (id, system_account_id, route_strategy_id, key_hash, status) VALUES ('key5', 'sys_owner', 'rs1', '` + HashSecret("sk-w11d-sql5") + `', 'active')`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	if _, err := db.Exec(`DROP TABLE route_strategy_groups`); err != nil {
		t.Fatal(err)
	}
	if _, err := models.ReadGatewayRuntimeByKeyHash(ctx, HashSecret("sk-w11d-sql5")); err == nil {
		t.Fatal("绑定查询失败必须上抛")
	}
	// loadGatewayAPIKeyByKeyHash 的 sk- 分支。
	if _, err := models.loadGatewayAPIKeyByKeyHash(ctx, "sk-w11d-sql5"); err == nil {
		t.Fatal("sk- 分支绑定查询失败必须上抛")
	}
}





