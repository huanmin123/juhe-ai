package gatewayruntimecache

// w11d 覆盖补齐（二）：accounts / catalog / groupaccess / inspection /
// invalidate / prewarm / runtime 的缓存流程错误臂、后台刷新与共享模式分支，
// 以及 registry（发布/枚举/签名/命名空间归一）。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// accounts.go
// ---------------------------------------------------------------------------

// w11dFailingAccounts 让账户 loader 第 failAfter 次后失败。
type w11dFailingAccounts struct {
	*fakeModels
	calls     int
	failAfter int
}

func (m *w11dFailingAccounts) ListOpenAIAccountsForGroupResult(ctx context.Context, groupID, systemAccountID string, opts OpenAIAccountsForGroupOptions) (OpenAIAccountsForGroupResult, error) {
	m.calls++
	if m.calls > m.failAfter {
		return OpenAIAccountsForGroupResult{}, errors.New("w11d accounts loader down")
	}
	return m.fakeModels.ListOpenAIAccountsForGroupResult(ctx, groupID, systemAccountID, opts)
}

// w11dFailingConcurrency 让并发挥取失败。
type w11dFailingConcurrency struct {
	*fakeModels
}

func (m *w11dFailingConcurrency) LoadAccountCurrentConcurrencyByID(context.Context, []string) (map[string]int, error) {
	return nil, errors.New("w11d concurrency down")
}

func TestW11DAccountsCacheErrorArms(t *testing.T) {
	ctx := context.Background()

	// 同步读：loader 失败上抛。
	failing := &w11dFailingAccounts{fakeModels: newFakeModels(), failAfter: 0}
	svc := newTestService(t, failing, newManualClock(), nil)
	if _, err := svc.ListCachedOpenAIAccountsForGroup(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err == nil {
		t.Fatal("loader 失败必须上抛")
	}
	// Fresh 读：loader 失败上抛。
	if _, err := svc.ListFreshOpenAIAccountsForGroupAsync(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err == nil {
		t.Fatal("fresh 读 loader 失败必须上抛")
	}
	// 可恢复不可用读：loader 失败上抛。
	if _, err := svc.ListRecoverableUnavailableOpenAIAccountsForGroupAsync(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}, nil); err == nil {
		t.Fatal("recoverable 读 loader 失败必须上抛")
	}

	// 坏 cooldownUntil：recoverable 过滤抛 RFC3339 错误。
	models := newFakeModels()
	w11dClock := newManualClock()
	bad := "bad-cooldown"
	account := testAccount("a_bad_cd", "sys")
	account.Status = AccountStatusTemporaryUnavailable
	account.CooldownUntil = &bad
	models.accounts["gcd"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{account}}
	svc2 := newTestService(t, models, w11dClock, nil)
	if _, err := svc2.ListRecoverableUnavailableOpenAIAccountsForGroupAsync(ctx, "gcd", "sys", CachedOpenAIAccountsForGroupOptions{}, nil); err == nil {
		t.Fatal("坏 cooldownUntil 必须报错")
	}

	// 并发挥取失败：同步读上抛。
	conv := &w11dFailingConcurrency{fakeModels: models}
	svc3 := newTestService(t, conv, newManualClock(), nil)
	if _, err := svc3.ListCachedOpenAIAccountsForGroup(ctx, "gcd", "sys", CachedOpenAIAccountsForGroupOptions{}); err == nil {
		t.Fatal("并发源失败必须上抛")
	}
	// 坏账户过期时间：缓存条目构建报错。
	expiredBad := "bad-expiry"
	account2 := testAccount("a_bad_exp", "sys")
	account2.ExpiresAt = &expiredBad
	models.accounts["gexp"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{account2}}
	if _, err := svc2.ListCachedOpenAIAccountsForGroup(ctx, "gexp", "sys", CachedOpenAIAccountsForGroupOptions{}); err == nil {
		t.Fatal("坏 expiresAt 必须拒绝缓存")
	}
	// 覆盖层：不可用账户被剔除。
	past := w11dClock.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	account3 := testAccount("a_past", "sys")
	account3.ExpiresAt = &past
	models.accounts["gdrop"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{account3, testAccount("a_ok", "sys")}}
	got, err := svc2.ListCachedOpenAIAccountsForGroup(ctx, "gdrop", "sys", CachedOpenAIAccountsForGroupOptions{})
	if err != nil || len(got) != 1 || got[0].ID != "a_ok" {
		t.Fatalf("剔除过期账户 = %v err=%v", got, err)
	}
	if got[0].CurrentConcurrency == nil || *got[0].CurrentConcurrency != 0 {
		t.Fatalf("并发缺省为 0: %+v", got[0].CurrentConcurrency)
	}
}

func TestW11DAccountsSharedModeAlwaysLoads(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testAccount("a1", "sys")}}
	counter := &w11dAccountsCallRecorder{fakeModels: models}
	clock := newManualClock()
	shared := newFakeSharedFactory()
	svc := newTestService(t, counter, clock, func(o *Options) { o.Shared = shared })
	// 共享模式进程缓存禁用：每次读取都落 loader；loader 失败直接上抛。
	if _, err := svc.ListCachedOpenAIAccountsForGroupAsync(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	counter.fail = true
	if _, err := svc.ListCachedOpenAIAccountsForGroupAsync(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err == nil {
		t.Fatal("共享模式二次读取必须重新加载并上抛 loader 失败")
	}
}

// w11dAccountsCallRecorder 记录并可选失败。
type w11dAccountsCallRecorder struct {
	*fakeModels
	fail bool
}

func (m *w11dAccountsCallRecorder) ListOpenAIAccountsForGroupResult(ctx context.Context, groupID, systemAccountID string, opts OpenAIAccountsForGroupOptions) (OpenAIAccountsForGroupResult, error) {
	if m.fail {
		return OpenAIAccountsForGroupResult{}, errors.New("w11d refresh fail")
	}
	return m.fakeModels.ListOpenAIAccountsForGroupResult(ctx, groupID, systemAccountID, opts)
}

// ---------------------------------------------------------------------------
// catalog.go
// ---------------------------------------------------------------------------

func TestW11DCatalogSharedAndErrorArms(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.catalog["gpt"] = []ProviderModelCatalogItem{{ProviderCode: "gpt", Model: "m1"}}
	clock := newManualClock()
	shared := newFakeSharedFactory()
	logger := &whWarnLogger{}
	svc := newTestService(t, models, clock, func(o *Options) {
		o.Shared = shared
		o.Logger = logger
	})
	input := ModelCatalogListOptions{ProviderCode: "gpt", SystemAccountID: "sys"}
	items, err := svc.ListCachedProviderModelCatalogAsync(ctx, input)
	if err != nil || len(items) != 1 {
		t.Fatalf("首读 = %v err=%v", items, err)
	}
	// 取消 ctx 的 pending 路径：awaitCatalogLoad ctx 臂。
	blockCatalog := newW11dBlockCatalog(newFakeModels())
	svc2 := newTestService(t, blockCatalog, clock, func(o *Options) { o.Shared = newFakeSharedFactory() })
	input2 := ModelCatalogListOptions{ProviderCode: "other", SystemAccountID: "sys"}
	watched := make(chan struct{})
	blockCatalog.onStart = func() { close(watched) }
	readCtx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel()
	if _, err := svc2.ListCachedProviderModelCatalogAsync(readCtx, input2); err == nil {
		t.Fatal("取消 ctx 必须返回错误")
	}
	<-watched
	blockCatalog.release()
	if err := svc2.AwaitBackgroundWork(ctx); err != nil {
		t.Fatal(err)
	}
	// 路由解析：空模型与空代码集合 → missing。
	resolution, err := svc.ResolveCachedProviderModelRouteAsync(ctx, "  ", []string{"gpt"}, "sys", false)
	if err != nil || resolution.Outcome != ProviderModelRouteMissing {
		t.Fatalf("空模型 = %+v err=%v", resolution, err)
	}
	resolution, err = svc.ResolveCachedProviderModelRouteAsync(ctx, "m1", nil, "sys", false)
	if err != nil || resolution.Outcome != ProviderModelRouteMissing {
		t.Fatalf("空代码 = %+v err=%v", resolution, err)
	}
	// 单命中 / 多命中。
	resolution, err = svc.ResolveCachedProviderModelRouteAsync(ctx, "m1", []string{"gpt"}, "sys", false)
	if err != nil || resolution.Outcome != ProviderModelRouteMatched || resolution.ProviderCode != "gpt" {
		t.Fatalf("单命中 = %+v err=%v", resolution, err)
	}
	models.catalog["anthropic"] = []ProviderModelCatalogItem{{ProviderCode: "anthropic", Model: " m1 "}}
	svc.ClearGatewayRuntimeCacheLocal(ClearOptions{ClearModelCatalog: true})
	resolution, err = svc.ResolveCachedProviderModelRouteAsync(ctx, "m1", []string{"gpt", "anthropic"}, "sys", false)
	if err != nil || resolution.Outcome != ProviderModelRouteAmbiguous || len(resolution.MatchedProviderCodes) != 2 {
		t.Fatalf("多命中 = %+v err=%v", resolution, err)
	}
	// 本地缓存命中路径（第二次解析同键）。
	if _, err := svc.ResolveCachedProviderModelRouteAsync(ctx, "m1", []string{"gpt"}, "sys", false); err != nil {
		t.Fatal(err)
	}
	// 目录加载失败传播到路由索引构建。
	failingCatalog := &w11dFailingCatalogModels{fakeModels: newFakeModels()}
	svc3 := newTestService(t, failingCatalog, clock, nil)
	if _, err := svc3.ResolveCachedProviderModelRouteAsync(ctx, "m1", []string{"gpt"}, "sys", false); err == nil {
		t.Fatal("目录失败必须传播")
	}
	// 共享路由索引读取失败告警。
	svc4 := newTestService(t, models, clock, func(o *Options) { o.Logger = logger })
	svc4.sharedRouteIdx = &whReadErrShared{}
	if _, err := svc4.ResolveCachedProviderModelRouteAsync(ctx, "m1", []string{"gpt"}, "sys", false); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range logger.snapshot() {
		if event == "gateway_provider_model_route_index_shared_cache_read_failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("路由索引共享读失败告警缺失")
	}
	// 共享写失败告警（setRouteIndexSharedCacheEntry）。
	svc5 := newTestService(t, models, clock, func(o *Options) { o.Logger = logger })
	svc5.sharedRouteIdx = &whFailingShared{failSet: true}
	if _, err := svc5.ResolveCachedProviderModelRouteAsync(ctx, "m1", []string{"gpt"}, "sys", false); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, event := range logger.snapshot() {
		if event == "gateway_provider_model_route_index_shared_cache_write_failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("路由索引共享写失败告警缺失")
	}
	// 目录共享写失败告警。
	svc6 := newTestService(t, models, clock, func(o *Options) { o.Logger = logger })
	svc6.sharedCatalog = &whFailingShared{failSet: true}
	if _, err := svc6.ListCachedProviderModelCatalogAsync(ctx, input); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, event := range logger.snapshot() {
		if event == "gateway_provider_model_catalog_shared_cache_write_failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("目录共享写失败告警缺失")
	}
}

// w11dBlockCatalogModels 可阻塞目录 loader。
type w11dBlockCatalogModels struct {
	*fakeModels
	block   chan struct{}
	onStart func()
}

func newW11dBlockCatalog(models *fakeModels) *w11dBlockCatalogModels {
	return &w11dBlockCatalogModels{fakeModels: models, block: make(chan struct{})}
}

func (m *w11dBlockCatalogModels) ListProviderModelCatalog(ctx context.Context, input ModelCatalogListOptions) ([]ProviderModelCatalogItem, error) {
	if m.onStart != nil {
		m.onStart()
		m.onStart = nil
	}
	<-m.block
	return m.fakeModels.ListProviderModelCatalog(ctx, input)
}

func (m *w11dBlockCatalogModels) release() { close(m.block) }

// w11dFailingCatalogModels 目录源恒失败。
type w11dFailingCatalogModels struct {
	*fakeModels
}

func (m *w11dFailingCatalogModels) ListProviderModelCatalog(context.Context, ModelCatalogListOptions) ([]ProviderModelCatalogItem, error) {
	return nil, errors.New("w11d catalog down")
}

// ---------------------------------------------------------------------------
// groupaccess.go / inspection.go / invalidate.go / prewarm.go
// ---------------------------------------------------------------------------

func TestW11DGroupAccessAndInspectionArms(t *testing.T) {
	ctx := context.Background()

	// 同步读 loader 失败 / 坏 expiresAt。
	failing := &w11dGroupFailingModels{fakeModels: newFakeModels()}
	svc := newTestService(t, failing, newManualClock(), nil)
	if _, err := svc.ResolveCachedGroupUsageAccessMetadata(ctx, "g1", "sys"); err == nil {
		t.Fatal("分组 loader 失败必须上抛")
	}
	bad := "bad-instant"
	models := newFakeModels()
	models.groupAccess["gbad:sys"] = &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys", GroupAuthorizationExpiresAt: &bad}
	svc2 := newTestService(t, models, newManualClock(), nil)
	if _, err := svc2.ResolveCachedGroupUsageAccessMetadata(ctx, "gbad", "sys"); err == nil {
		t.Fatal("坏分组过期时间必须报错")
	}
	// 缓存条目过期后读取：缓存命中路径 groupUsageAccessFromCacheEntry 判定不可用 → nil。
	w11dClock := newManualClock()
	past := w11dClock.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	models.groupAccess["gold:sys"] = &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys", GroupAuthorizationExpiresAt: &past}
	loaded, err := svc2.ResolveCachedGroupUsageAccessMetadata(ctx, "gold", "sys")
	if err != nil || loaded == nil {
		t.Fatalf("首读返回 loader 值 = %v err=%v", loaded, err)
	}
	cachedAgain, err := svc2.ResolveCachedGroupUsageAccessMetadata(ctx, "gold", "sys")
	if err != nil || cachedAgain != nil {
		t.Fatalf("缓存命中过期授权必须读作 nil = %v err=%v", cachedAgain, err)
	}

	// 检查策略 loader 失败上抛。
	failingPolicy := &w11dPolicyFailingModels{fakeModels: newFakeModels()}
	svc3 := newTestService(t, failingPolicy, newManualClock(), nil)
	if _, err := svc3.ListCachedActiveResponseInspectionPoliciesAsync(ctx, "openai", "gpt"); err == nil {
		t.Fatal("检查策略 loader 失败必须上抛")
	}
	// 账户扇出：重复 scope 去重、空协议跳过。
	models2 := newFakeModels()
	models2.policies["openai:gpt"] = []ResponseInspectionPolicySummary{{
		ID: "p1", Name: "n", Enabled: true, Priority: 1, ScopeType: "provider",
		ProtocolCode: "openai", Match: ResponseInspectionPolicyMatch{}, Action: "observe",
	}}
	svc4 := newTestService(t, models2, newManualClock(), nil)
	accounts := []OpenAIAccountSecret{
		{ID: "a1", ProtocolCode: " openai ", ProviderCode: " gpt "},
		{ID: "a2", ProtocolCode: "openai", ProviderCode: "gpt"},
		{ID: "a3", ProtocolCode: " "},
	}
	policies, err := svc4.ListCachedActiveResponseInspectionPoliciesForAccountsAsync(ctx, accounts)
	if err != nil || len(policies) != 1 {
		t.Fatalf("扇出合并 = %v err=%v", policies, err)
	}
	if calls := models2.policyCalls["openai:gpt"]; calls != 1 {
		t.Fatalf("scope 去重 = %d", calls)
	}

	// 定向失效：空 hash 跳过 + 空 apiKeyID 全清。
	models3 := newFakeModels()
	models3.runtimes["sk-inv"] = staticRuntime(t, models3, testAPIKeyRow("k_inv", RouteStrategyModeNormal, "g1"), nil, nil)
	svc5 := newTestService(t, models3, newManualClock(), nil)
	if _, err := svc5.ReadCachedGatewayRuntimeAsync(ctx, "sk-inv"); err != nil {
		t.Fatal(err)
	}
	svc5.InvalidateGatewayRuntimeCacheByAPIKeyID("", []string{"", HashSecret("sk-inv")})
	if svc5.runtimeCacheSize() != 0 {
		t.Fatal("定向失效必须删除目标键")
	}
	svc5.InvalidateGatewayRuntimeCacheByAPIKeyID("", []string{""})
	// 共享清理失败告警臂。
	logger := &whWarnLogger{}
	svc6 := newTestService(t, models3, newManualClock(), func(o *Options) { o.Logger = logger })
	svc6.sharedGroup = &w11dClearErrShared{}
	svc6.sharedInspection = &w11dClearErrShared{}
	svc6.sharedCatalog = &w11dClearErrShared{}
	svc6.sharedRouteIdx = &w11dClearErrShared{}
	svc6.sharedSettings = &w11dClearErrShared{}
	svc6.ClearGatewayRuntimeCacheLocal(ClearOptions{ClearModelCatalog: true})
	events := strings.Join(logger.snapshot(), ",")
	for _, want := range []string{
		"gateway_group_usage_access_shared_cache_clear_failed",
		"gateway_response_inspection_policy_shared_cache_clear_failed",
		"gateway_provider_model_catalog_shared_cache_clear_failed",
		"gateway_provider_model_route_index_shared_cache_clear_failed",
		"gateway_settings_shared_cache_clear_failed",
	} {
		if !strings.Contains(events, want) {
			t.Fatalf("共享清理失败告警缺失 %s: %s", want, events)
		}
	}

	// 预热：hash 枚举失败 / 无 prewarmer 退化。
	if warmed, err := svc5.PrewarmGatewayAPIKeyValidationCache(ctx); err != nil || warmed != 0 {
		t.Fatalf("无 prewarmer = %d %v", warmed, err)
	}
}

// w11dGroupFailingModels 分组访问恒失败。
type w11dGroupFailingModels struct{ *fakeModels }

func (m *w11dGroupFailingModels) ResolveGroupUsageAccessMetadata(context.Context, string, string) (*GroupUsageAccessMetadata, error) {
	return nil, errors.New("w11d group down")
}

// w11dPolicyFailingModels 检查策略恒失败。
type w11dPolicyFailingModels struct{ *fakeModels }

func (m *w11dPolicyFailingModels) ListActiveResponseInspectionPolicies(context.Context, string, string) ([]ResponseInspectionPolicySummary, error) {
	return nil, errors.New("w11d policy down")
}

// w11dClearErrShared 清理失败的共享缓存。
type w11dClearErrShared struct{}

func (c *w11dClearErrShared) Get(context.Context, string, any) (bool, error) { return false, nil }
func (c *w11dClearErrShared) Set(context.Context, string, any, time.Duration) error {
	return nil
}
func (c *w11dClearErrShared) Clear(context.Context) error { return errors.New("w11d clear fail") }

// w11dPrewarmModels 覆盖预热的世代与装载错误臂。
type w11dPrewarmModels struct {
	*fakeModels
	hashes     []string
	listErr    error
	runtimeErr error
	onListed   func()
}

func (m *w11dPrewarmModels) ListActiveGatewayAPIKeyHashes(context.Context) ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	if m.onListed != nil {
		m.onListed()
	}
	return m.hashes, nil
}

func (m *w11dPrewarmModels) ReadGatewayRuntimeByKeyHash(ctx context.Context, keyHash string) (GatewayRuntime, error) {
	if m.runtimeErr != nil {
		return GatewayRuntime{}, m.runtimeErr
	}
	for _, hash := range m.hashes {
		if hash == keyHash {
			return m.fakeModels.runtimes["sk-w11d"], nil
		}
	}
	return GatewayRuntime{}, nil
}

func TestW11DPrewarmErrorArms(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.runtimes["sk-w11d"] = staticRuntime(t, models, testAPIKeyRow("k_pw", RouteStrategyModeNormal, "g1"), nil, nil)

	// 枚举失败。
	prewarm := &w11dPrewarmModels{fakeModels: models, listErr: errors.New("w11d list down")}
	svc := newTestService(t, prewarm, newManualClock(), nil)
	if _, err := svc.PrewarmGatewayAPIKeyValidationCache(ctx); err == nil {
		t.Fatal("枚举失败必须上抛")
	}
	// 单键装载失败。
	prewarm2 := &w11dPrewarmModels{fakeModels: models, hashes: []string{HashSecret("sk-w11d")}, runtimeErr: errors.New("w11d load down")}
	svc2 := newTestService(t, prewarm2, newManualClock(), nil)
	if _, err := svc2.PrewarmGatewayAPIKeyValidationCache(ctx); err == nil {
		t.Fatal("装载失败必须上抛")
	}
	// 世代中途失效：枚举返回后推进世代 → 首键检查提前返回。
	prewarm3 := &w11dPrewarmModels{fakeModels: models, hashes: []string{HashSecret("sk-w11d")}}
	svc3 := newTestService(t, prewarm3, newManualClock(), nil)
	prewarm3.onListed = func() { svc3.InvalidateGatewayRuntimeCacheByAPIKeyID("bump", nil) }
	warmed, err := svc3.PrewarmGatewayAPIKeyValidationCache(ctx)
	if err != nil || warmed != 0 {
		t.Fatalf("世代失效提前返回 = %d %v", warmed, err)
	}
	// 坏 expiresAt 的 populate 错误臂。
	bad := "bad-instant"
	row := testAPIKeyRow("k_bad_pw", RouteStrategyModeNormal, "g1")
	row.ExpiresAt = &bad
	badModels := newFakeModels()
	badModels.runtimes["sk-w11d"] = staticRuntime(t, badModels, row, nil, nil)
	prewarm4 := &w11dPrewarmModels{fakeModels: badModels, hashes: []string{HashSecret("sk-w11d")}}
	prewarm4.fakeModels = badModels
	svc4 := newTestService(t, prewarm4, newManualClock(), nil)
	if _, err := svc4.PrewarmGatewayAPIKeyValidationCache(ctx); err == nil {
		t.Fatal("populate 坏 expiresAt 必须报错")
	}
}

// ---------------------------------------------------------------------------
// runtime.go：净化/派发错误臂、后台刷新、动态路由重选
// ---------------------------------------------------------------------------

func TestW11DRuntimeSanitizeAndDispatchArms(t *testing.T) {
	ctx := context.Background()
	now := newManualClock().Now().UnixMilli()
	future := time.UnixMilli(now + 3600_000).UTC().Format(time.RFC3339Nano)

	// APIKey nil → 空快照。
	models := newFakeModels()
	svc := newTestService(t, models, newManualClock(), nil)
	empty, err := svc.dispatchGatewayRuntimeForSend(ctx, GatewayRuntime{Settings: models.settings})
	if err != nil || empty.APIKey != nil {
		t.Fatalf("nil key 派发 = %+v err=%v", empty.APIKey, err)
	}

	// key 不可用 → 空快照（状态非活跃）。
	row := testAPIKeyRow("k_dead", RouteStrategyModeNormal, "g1")
	row.Status = "revoked"
	revoked, err := svc.dispatchGatewayRuntimeForSend(ctx, GatewayRuntime{APIKey: row, Settings: models.settings})
	if err != nil || revoked.APIKey != nil {
		t.Fatalf("非活跃 key = %+v err=%v", revoked.APIKey, err)
	}
	// 坏 key expiresAt → 错误上抛。
	bad := "bad"
	rowBad := testAPIKeyRow("k_badexp", RouteStrategyModeNormal, "g1")
	rowBad.ExpiresAt = &bad
	if _, err := svc.dispatchGatewayRuntimeForSend(ctx, GatewayRuntime{APIKey: rowBad}); err == nil {
		t.Fatal("坏 key expiresAt 必须报错")
	}
	// 分组过期 → 空快照；坏分组 expiresAt → 报错。
	rowOK := testAPIKeyRow("k_ok", RouteStrategyModeNormal, "g1")
	past := time.UnixMilli(now - 1000).UTC().Format(time.RFC3339Nano)
	expiredGroup := &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys", GroupAuthorizationExpiresAt: &past}
	collapsed, err := svc.dispatchGatewayRuntimeForSend(ctx, GatewayRuntime{APIKey: rowOK, GroupAccess: expiredGroup, Settings: models.settings})
	if err != nil || collapsed.APIKey != nil {
		t.Fatalf("过期分组 = %+v err=%v", collapsed.APIKey, err)
	}
	badGroup := &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys", GroupAuthorizationExpiresAt: &bad}
	if _, err := svc.dispatchGatewayRuntimeForSend(ctx, GatewayRuntime{APIKey: rowOK, GroupAccess: badGroup}); err == nil {
		t.Fatal("坏分组 expiresAt 必须报错")
	}
	// 账户坏 expiresAt → 报错；过期账户剔除 + 诊断/策略克隆。
	accountBad := testAccount("a_bad", "sys")
	accountBad.ExpiresAt = &bad
	if _, err := svc.dispatchGatewayRuntimeForSend(ctx, GatewayRuntime{APIKey: rowOK, Accounts: []OpenAIAccountSecret{accountBad}, Settings: models.settings}); err == nil {
		t.Fatal("坏账户 expiresAt 必须报错")
	}
	accountPast := testAccount("a_past", "sys")
	accountPast.ExpiresAt = &past
	diagnostics := &OpenAIAccountsForGroupDiagnostics{ScanLimit: 5}
	policies := []ResponseInspectionPolicySummary{{
		ID: "p1", Name: "n", Enabled: true, Priority: 1, ScopeType: "protocol",
		ProtocolCode: "openai", Match: ResponseInspectionPolicyMatch{}, Action: "observe",
	}}
	served, err := svc.dispatchGatewayRuntimeForSend(ctx, GatewayRuntime{
		APIKey: rowOK, Settings: models.settings,
		Accounts:                   []OpenAIAccountSecret{accountPast, testAccount("a_keep", "sys")},
		AccountDispatchDiagnostics: diagnostics,
		ResponseInspectionPolicies: policies,
	})
	if err != nil || len(served.Accounts) != 1 || served.Accounts[0].ID != "a_keep" {
		t.Fatalf("账户剔除 = %+v err=%v", served.Accounts, err)
	}
	if served.AccountDispatchDiagnostics == nil || served.AccountDispatchDiagnostics.ScanLimit != 5 {
		t.Fatal("诊断克隆缺失")
	}
	if len(served.ResponseInspectionPolicies) != 1 {
		t.Fatal("策略克隆缺失")
	}

	// 动态路由派发：nil APIKey 静态克隆；Orderer 失败无 last-good → 上抛；
	// 有 last-good → 回退 + 告警；成功路径选组。
	dynamicRow := testAPIKeyRow("k_dyn", RouteStrategyModeHybridSmart, "g1", "g2")
	dynamicRow.ExpiresAt = &future
	staticDyn, err := svc.routeCachedDynamicGatewayRuntimeForDispatch(ctx, GatewayRuntime{Settings: models.settings})
	if err != nil || staticDyn.APIKey != nil {
		t.Fatalf("nil key 动态 = %+v err=%v", staticDyn.APIKey, err)
	}
	ordererFail := &w11dOrderer{err: errors.New("w11d orderer down")}
	modelsDyn := newFakeModels()
	modelsDyn.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testAccount("a1", "sys")}}
	modelsDyn.accounts["g2"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testAccount("a2", "sys")}}
	logger := &whWarnLogger{}
	svcDyn := newTestService(t, modelsDyn, newManualClock(), func(o *Options) {
		o.Orderer = ordererFail
		o.Logger = logger
	})
	// 无 last-good（SelectedGroupID 空）→ 上抛。
	bare := GatewayRuntime{APIKey: dynamicRow, Settings: modelsDyn.settings}
	if _, err := svcDyn.routeCachedDynamicGatewayRuntimeForDispatch(ctx, bare); err == nil {
		t.Fatal("Orderer 失败且无 last-good 必须上抛")
	}
	// 有 last-good → 回退并告警。
	lastGood := GatewayRuntime{
		APIKey: dynamicRow, Settings: modelsDyn.settings,
		GroupAccess: &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys"},
		Accounts:    []OpenAIAccountSecret{testAccount("a1", "sys")},
	}
	fallback, err := svcDyn.routeCachedDynamicGatewayRuntimeForDispatch(ctx, lastGood)
	if err != nil || fallback.SelectedGroupIDInAPIKey() != "g1" {
		t.Fatalf("last-good 回退 = %+v err=%v", fallback.APIKey, err)
	}
	found := false
	for _, event := range logger.snapshot() {
		if event == "gateway_dynamic_route_last_good_fallback" {
			found = true
		}
	}
	if !found {
		t.Fatalf("last-good 回退告警缺失: %v", logger.snapshot())
	}
	// 成功重选：Orderer 返回倒序，两个分组都有访问与账户，选第一个。
	modelsDyn.groupAccess["g1:sys_owner"] = &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys_owner"}
	modelsDyn.groupAccess["g2:sys_owner"] = &GroupUsageAccessMetadata{GroupOwnerSystemAccountID: "sys_owner"}
	reorder := &w11dOrderer{ordered: []GatewayAPIKeyGroupBindingRow{
		{GroupID: "g2", Status: "active", GroupEnabled: 1, Priority: 1},
		{GroupID: "g1", Status: "active", GroupEnabled: 1, Priority: 2},
	}}
	svcDyn2 := newTestService(t, modelsDyn, newManualClock(), func(o *Options) { o.Orderer = reorder })
	rerouted, err := svcDyn2.routeCachedDynamicGatewayRuntimeWithFreshSelection(ctx, GatewayRuntime{APIKey: dynamicRow, Settings: modelsDyn.settings})
	if err != nil || rerouted.APIKey == nil || rerouted.APIKey.SelectedGroupID != "g2" {
		t.Fatalf("重选 = %+v err=%v", rerouted.APIKey, err)
	}
	// 第一个分组无账户且多候选 → 跳到下一个。
	emptyAccounts := &w11dEmptyAccountsFor{fakeModels: modelsDyn, group: "g2"}
	svcDyn3 := newTestService(t, emptyAccounts, newManualClock(), func(o *Options) { o.Orderer = reorder })
	rerouted2, err := svcDyn3.routeCachedDynamicGatewayRuntimeWithFreshSelection(ctx, GatewayRuntime{APIKey: dynamicRow, Settings: modelsDyn.settings})
	if err != nil || rerouted2.APIKey == nil || rerouted2.APIKey.SelectedGroupID != "g1" {
		t.Fatalf("跳过空分组 = %+v err=%v", rerouted2.APIKey, err)
	}
	// 全部无访问 → 空账户结果。
	modelsNone := newFakeModels()
	svcDyn4 := newTestService(t, modelsNone, newManualClock(), nil)
	none, err := svcDyn4.routeCachedDynamicGatewayRuntimeWithFreshSelection(ctx, GatewayRuntime{APIKey: dynamicRow, Settings: modelsNone.settings})
	if err != nil || none.APIKey == nil || len(none.Accounts) != 0 {
		t.Fatalf("无访问结果 = %+v err=%v", none.APIKey, err)
	}
	// 分组访问解析失败传播。
	groupErr := &w11dGroupFailingModels{fakeModels: modelsDyn}
	svcDyn5 := newTestService(t, groupErr, newManualClock(), nil)
	if _, err := svcDyn5.routeCachedDynamicGatewayRuntimeWithFreshSelection(ctx, GatewayRuntime{APIKey: dynamicRow, Settings: modelsDyn.settings}); err == nil {
		t.Fatal("分组访问失败必须传播")
	}
	// 并发覆盖失败传播（cloneGatewayRuntimeForDispatchAsync）。
	convFail := &w11dFailingConcurrency{fakeModels: modelsDyn}
	svcConv := newTestService(t, convFail, newManualClock(), nil)
	if _, err := svcConv.dispatchGatewayRuntimeForSend(ctx, lastGood); err == nil {
		t.Fatal("并发覆盖失败必须传播")
	}
}

// w11dOrderer 是 GroupBindingOrderer 替身。
type w11dOrderer struct {
	ordered []GatewayAPIKeyGroupBindingRow
	err     error
}

func (o *w11dOrderer) OrderAPIKeyGroupBindings(context.Context, GatewayAPIKeyRow) ([]GatewayAPIKeyGroupBindingRow, error) {
	if o.err != nil {
		return nil, o.err
	}
	return o.ordered, nil
}

// w11dEmptyAccountsFor 对指定分组返回空账户集。
type w11dEmptyAccountsFor struct {
	*fakeModels
	group string
}

func (m *w11dEmptyAccountsFor) ListOpenAIAccountsForGroupResult(ctx context.Context, groupID, systemAccountID string, opts OpenAIAccountsForGroupOptions) (OpenAIAccountsForGroupResult, error) {
	if groupID == m.group {
		return OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{}}, nil
	}
	return m.fakeModels.ListOpenAIAccountsForGroupResult(ctx, groupID, systemAccountID, opts)
}

// SelectedGroupIDInAPIKey 读取运行态选组（测试助手）。
func (r GatewayRuntime) SelectedGroupIDInAPIKey() string {
	if r.APIKey == nil {
		return ""
	}
	return r.APIKey.SelectedGroupID
}

func TestW11DRuntimeStaleRefreshWarns(t *testing.T) {
	ctx := context.Background()
	models := newFakeModels()
	models.runtimes["sk-stale"] = staticRuntime(t, models, testAPIKeyRow("k_stale", RouteStrategyModeNormal, "g1"), nil, nil)
	logger := &whWarnLogger{}
	clock := newManualClock()
	svc := newTestService(t, models, clock, func(o *Options) { o.Logger = logger })
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-stale"); err != nil {
		t.Fatal(err)
	}
	// 推进 61s：stale 服务 + 后台刷新；loader 改为失败 → 告警。
	models.runtimeErr = errors.New("w11d stale refresh down")
	clock.Advance(61 * time.Second)
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-stale"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AwaitBackgroundWork(ctx); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range logger.snapshot() {
		if event == "gateway_runtime_stale_refresh_failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("运行态后台刷新失败告警缺失: %v", logger.snapshot())
	}
}

// ---------------------------------------------------------------------------
// registry.go
// ---------------------------------------------------------------------------

func TestW11DRegistryConfigArms(t *testing.T) {
	if _, err := NewRegistry(RegistryConfig{}); err == nil {
		t.Fatal("空 Redis URL 必须报错")
	}
	if _, err := NewRegistry(RegistryConfig{RedisURL: "redis://127.0.0.1:6379/0"}); err == nil {
		t.Fatal("空 secret 必须报错")
	}
	if _, err := NewRegistry(RegistryConfig{RedisURL: "://bad", Secret: "s"}); err == nil {
		t.Fatal("坏 URL 必须报错")
	}
	server := miniredis.RunT(t)
	registry, err := NewRegistry(RegistryConfig{
		RedisURL:  "redis://" + server.Addr() + "/0",
		Namespace: "w11d:ns:",
		Secret:    "w11d-secret",
		InstanceID: "w11d-inst",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	if !strings.HasPrefix(registry.prefix, "juhe-ai:") || !strings.HasSuffix(registry.prefix, ":runtime:internal-gateway:v1:") {
		t.Fatalf("命名空间前缀 = %q", registry.prefix)
	}
	// 命名空间归一：裸 juhe-ai。
	bare, err := NewRegistry(RegistryConfig{RedisURL: "redis://" + server.Addr() + "/0", Namespace: "juhe-ai:", Secret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bare.Close() })
	if bare.prefix != "juhe-ai:runtime:internal-gateway:v1:" {
		t.Fatalf("裸命名空间 = %q", bare.prefix)
	}
	if got := registry.EntryKey("Inst/1 例子"); !strings.HasSuffix(got, "Inst_1") || strings.Contains(got, "/") {
		t.Fatalf("实例键净化 = %q", got)
	}
	if got := sanitizeRegistryNamespacePart("   "); got != "_" {
		t.Fatalf("空白净化 = %q", got)
	}
	if got := sanitizeRegistryNamespacePart("ok_-:.X"); got != "ok_-:.X" {
		t.Fatalf("合法字符 = %q", got)
	}
}

func TestW11DRegistryLifecycleAndList(t *testing.T) {
	server := miniredis.RunT(t)
	url := "redis://" + server.Addr() + "/0"
	publisher, err := NewRegistry(RegistryConfig{
		RedisURL: url, Namespace: "w11dreg", Secret: "w11d-secret",
		InstanceID: "w11d-live", Port: 8123, PublisherEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = publisher.Close() })
	// Publisher 禁用时 Start 只登记意图。
	idle, err := NewRegistry(RegistryConfig{RedisURL: url, Namespace: "w11dreg", Secret: "w11d-secret", InstanceID: "w11d-idle"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idle.Close() })
	idle.Start()
	if err := idle.Stop(context.Background()); err != nil {
		t.Fatalf("无会话 Stop = %v", err)
	}

	publisher.Start()
	ctx := context.Background()
	waitFor(t, 3*time.Second, func() bool {
		client := redis.NewClient(&redis.Options{Addr: server.Addr()})
		defer client.Close()
		value, err := client.Get(ctx, publisher.EntryKey("w11d-live")).Result()
		return err == nil && strings.Contains(value, "http://127.0.0.1:8123")
	})
	if err := publisher.Stop(ctx); err != nil {
		t.Fatalf("Stop = %v", err)
	}

	// 枚举：写入两条已签名条目 + 一条前缀不符 + 一条签名错误。
	reader, err := NewRegistry(RegistryConfig{
		RedisURL: url, Namespace: "w11dreg", Secret: "w11d-secret", ReaderEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	seedEntry := func(instanceID, origin, secret string) {
		entry := registryEntry{Version: registryEntryVersion, InstanceID: instanceID, Origin: origin, BootID: "boot-" + instanceID}
		entry.Signature = registrySignature(entry.Version, entry.InstanceID, entry.Origin, entry.BootID, secret)
		encoded := w11dMustMarshal(t, entry)
		if err := client.Set(ctx, reader.EntryKey(instanceID), encoded, time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
		if err := client.ZAdd(ctx, reader.indexKey, redis.Z{Score: float64(time.Now().UnixMilli()), Member: reader.EntryKey(instanceID)}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	seedEntry("w11d-a", "http://127.0.0.1:9001", "w11d-secret")
	seedEntry("w11d-b", "http://127.0.0.1:9002", "w11d-secret")
	seedEntry("w11d-bad", "http://127.0.0.1:9003", "wrong-secret")
	// 前缀不符成员。
	if err := client.ZAdd(ctx, reader.indexKey, redis.Z{Score: float64(time.Now().UnixMilli()), Member: "juhe-ai:w11dreg:other:v1:foreign"}).Err(); err != nil {
		t.Fatal(err)
	}
	endpoints, err := reader.ListEndpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 2 || endpoints[0].InstanceID != "w11d-a" || endpoints[1].Origin != "http://127.0.0.1:9002" {
		t.Fatalf("枚举 = %+v", endpoints)
	}
	// Reader 禁用 → 空表。
	off, err := NewRegistry(RegistryConfig{RedisURL: url, Namespace: "w11dreg", Secret: "w11d-secret"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = off.Close() })
	if endpoints, err := off.ListEndpoints(ctx); err != nil || len(endpoints) != 0 {
		t.Fatalf("禁用枚举 = %+v err=%v", endpoints, err)
	}
	// 断连枚举报错。
	deadServer := miniredis.NewMiniRedis()
	if err := deadServer.Start(); err != nil {
		t.Fatal(err)
	}
	dead, err := NewRegistry(RegistryConfig{
		RedisURL: "redis://" + deadServer.Addr() + "/0", Namespace: "w11dreg",
		Secret: "w11d-secret", ReaderEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = dead.Close()
	deadServer.Close()
	if _, err := dead.ListEndpoints(ctx); err == nil {
		t.Fatal("断连枚举必须报错")
	}
}

func TestW11DRegistryEntryParsingArms(t *testing.T) {
	secret := "w11d-secret"
	valid := registryEntry{Version: registryEntryVersion, InstanceID: "i1", Origin: "http://127.0.0.1:9000", BootID: "b1"}
	valid.Signature = registrySignature(valid.Version, valid.InstanceID, valid.Origin, valid.BootID, secret)
	encoded := w11dMustMarshal(t, valid)
	if parsed := parseRegistryEntry(encoded, secret); parsed == nil || parsed.InstanceID != "i1" {
		t.Fatal("合法条目必须解析")
	}
	if parseRegistryEntry("{nope", secret) != nil {
		t.Fatal("坏 JSON 必须拒绝")
	}
	// 版本不符 / 空 instance / 空 boot。
	wrongVersion := valid
	wrongVersion.Version = 99
	if parseRegistryEntry(w11dMustMarshal(t, wrongVersion), secret) != nil {
		t.Fatal("版本不符必须拒绝")
	}
	noInstance := valid
	noInstance.InstanceID = " "
	if parseRegistryEntry(w11dMustMarshal(t, noInstance), secret) != nil {
		t.Fatal("空实例必须拒绝")
	}
	noBoot := valid
	noBoot.BootID = ""
	if parseRegistryEntry(w11dMustMarshal(t, noBoot), secret) != nil {
		t.Fatal("空 boot 必须拒绝")
	}
	// 签名不符。
	badSig := valid
	badSig.Signature = "00"
	if parseRegistryEntry(w11dMustMarshal(t, badSig), secret) != nil {
		t.Fatal("签名不符必须拒绝")
	}
	// 非 loopback origin。
	remote := valid
	remote.Origin = "http://10.0.0.1:9000"
	remote.Signature = registrySignature(remote.Version, remote.InstanceID, remote.Origin, remote.BootID, secret)
	if parseRegistryEntry(w11dMustMarshal(t, remote), secret) != nil {
		t.Fatal("非 loopback 必须拒绝")
	}

	// isLoopbackHTTPOrigin 各臂。
	cases := map[string]bool{
		"http://127.0.0.1:9000":     true,
		"http://127.0.0.1:9000/":    true,
		"http://127.0.0.1":          false, // 无端口
		"https://127.0.0.1:9000":    false, // 非 http
		"http://localhost:9000":     false, // 非 127.0.0.1
		"http://user@127.0.0.1:9000": false,
		"http://127.0.0.1:9000/p":   false, // 带路径
		"http://127.0.0.1:9000?q=1": false,
		"http://127.0.0.1:9000#f":   false,
		"://bad":                    false,
	}
	for origin, want := range cases {
		if got := isLoopbackHTTPOrigin(origin); got != want {
			t.Fatalf("origin %q = %v want %v", origin, got, want)
		}
	}
	// 排序臂。
	endpoints := []InternalGatewayEndpoint{{InstanceID: "b"}, {InstanceID: "a"}, {InstanceID: "c"}}
	sortRegistryEndpoints(endpoints)
	if endpoints[0].InstanceID != "a" || endpoints[2].InstanceID != "c" {
		t.Fatalf("排序 = %+v", endpoints)
	}
}

func w11dMustMarshal(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
