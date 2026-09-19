package gatewayruntimecache

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// entryCache 原语：TTL/LRU 淘汰/禁用态/peek（stale 读取）契约
// ---------------------------------------------------------------------------

func TestWhEntryCachePrimitive(t *testing.T) {
	clock := newManualClock()
	disposed := []string{}
	cache := newEntryCache[string, int]("wh", 2, time.Minute, true, true, clock,
		func(key string, _ int) { disposed = append(disposed, key) }, nil)

	// max<1 钳位为 1，nil clock 回退系统时钟。
	if got := newEntryCache[string, int]("x", 0, time.Minute, false, true, nil, nil, nil); got.max != 1 || got.clock == nil {
		t.Fatal("newEntryCache 钳位/默认时钟契约失败")
	}
	if SystemClock().Now().IsZero() {
		t.Fatal("SystemClock 必须返回当前时间")
	}

	cache.set("a", 1, 0) // 0 → 默认 TTL
	cache.set("b", 2, 2*time.Minute)
	if v, ok := cache.get("a"); !ok || v != 1 {
		t.Fatalf("get(a) = %v %v", v, ok)
	}
	if cache.size() != 2 {
		t.Fatalf("size = %d", cache.size())
	}
	// 淘汰最旧（updateAgeOnGet 使 a 保持最新 → b 被淘汰）。
	cache.set("c", 3, 0)
	if _, ok := cache.get("b"); ok {
		t.Fatal("b 应被 LRU 淘汰")
	}
	if len(disposed) == 0 || disposed[0] != "b" {
		t.Fatalf("dispose 回调 = %v", disposed)
	}
	// 过期读取即清除。
	clock.Advance(2 * time.Minute)
	if _, ok := cache.get("a"); ok {
		t.Fatal("过期条目必须被清除")
	}
	// peek 允许读取陈旧条目（stale 窗口契约）。
	staleCache := newEntryCache[string, string]("wh2", 2, time.Minute, false, true, clock, nil, nil)
	staleCache.set("k", "v", time.Minute)
	clock.Advance(90 * time.Second)
	// peek 不校验过期：允许 stale 窗口读取。
	if v, ok := staleCache.peek("k"); !ok || v != "v" {
		t.Fatalf("peek 陈旧条目 = %v %v", v, ok)
	}
	if _, ok := staleCache.get("k"); ok {
		t.Fatal("get 必须拒绝过期条目")
	}
	// delete 与 clear 回调。
	cleared := false
	clearable := newEntryCache[string, int]("wh3", 2, time.Minute, false, true, clock, nil, func() { cleared = true })
	clearable.set("k", 9, 0)
	clearable.delete("k")
	if _, ok := clearable.get("k"); ok {
		t.Fatal("delete 后必须不可读")
	}
	clearable.set("k2", 8, 0)
	clearable.clear()
	if !cleared || clearable.size() != 0 {
		t.Fatalf("clear = %v size=%d", cleared, clearable.size())
	}
	// 禁用态全部为 no-op（redis 模式下的本地缓存语义）。
	disabled := newEntryCache[string, int]("wh4", 2, time.Minute, false, false, clock, nil, nil)
	disabled.set("k", 1, 0)
	if _, ok := disabled.get("k"); ok {
		t.Fatal("禁用缓存不得命中")
	}
	disabled.delete("k")
	if _, ok := disabled.peek("k"); ok || disabled.size() != 0 {
		t.Fatal("禁用缓存 peek/size 恒空")
	}
	// updateAgeOnGet=false 的命中不改变淘汰序。
	noUpdate := newEntryCache[string, int]("wh5", 2, time.Minute, false, true, clock, nil, nil)
	noUpdate.set("first", 1, 5*time.Minute)
	noUpdate.set("second", 2, 5*time.Minute)
	if _, ok := noUpdate.get("first"); !ok {
		t.Fatal("命中失败")
	}
	noUpdate.set("third", 3, 5*time.Minute)
	if _, ok := noUpdate.get("first"); ok {
		t.Fatal("无 updateAgeOnGet 时命中不续位，first 应被淘汰")
	}
}

// 候选账户缓存：未命中装载并写缓存、命中不再触发 loader、fresh 永远直读。
func TestWhAccountsCacheLifecycle(t *testing.T) {
	models := newFakeModels()
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testAccount("a1", "sys")}}
	counter := &whCountingModels{fakeModels: models}
	clock := newManualClock()
	svc := newTestService(t, counter, clock, nil)
	ctx := context.Background()

	// 未命中 → loader 装载。
	first, err := svc.ListCachedOpenAIAccountsForGroupAsync(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{})
	if err != nil || len(first) != 1 || first[0].ID != "a1" {
		t.Fatalf("首次读取 = %v err=%v", first, err)
	}
	if counter.accountCalls != 1 {
		t.Fatalf("loader 调用 = %d", counter.accountCalls)
	}
	// 命中 → 不再触发 loader。
	second, err := svc.ListCachedOpenAIAccountsForGroupAsync(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{})
	if err != nil || len(second) != 1 {
		t.Fatalf("缓存读取 = %v err=%v", second, err)
	}
	if counter.accountCalls != 1 {
		t.Fatalf("命中后 loader 调用 = %d", counter.accountCalls)
	}
	// fresh 读取永远直读 loader。
	if _, err := svc.ListFreshOpenAIAccountsForGroupAsync(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if counter.accountCalls != 2 {
		t.Fatalf("fresh loader 调用 = %d", counter.accountCalls)
	}
	// 并发覆盖：CurrentConcurrency 被注入（loader 未设置 → 0）。
	if first[0].CurrentConcurrency == nil || *first[0].CurrentConcurrency != 0 {
		t.Fatalf("并发覆盖 = %+v", first[0].CurrentConcurrency)
	}
}

// 可恢复候选的窗口与状态过滤契约。
func TestWhRecoverableUnavailableAccounts(t *testing.T) {
	now := int64(1_700_000_000_000)
	iso := func(offsetMs int64) *string {
		value := time.UnixMilli(now + offsetMs).UTC().Format("2006-01-02T15:04:05.000Z07:00")
		return &value
	}
	activeSoon := testAccount("active_soon", "sys")
	activeSoon.Status = AccountStatusActive
	activeSoon.CooldownUntil = iso(10_000)
	activeExpired := testAccount("active_expired", "sys")
	activeExpired.Status = AccountStatusActive
	activeExpired.CooldownUntil = iso(-1_000)
	temp := testAccount("temp", "sys")
	temp.Status = AccountStatusTemporaryUnavailable
	temp.CooldownUntil = iso(20_000)
	broken := testAccount("broken", "sys")
	broken.Status = AccountStatusTemporaryUnavailable
	broken.CooldownUntil = whStringPtr("bad-instant")
	none := testAccount("none", "sys")
	none.Status = AccountStatusTemporaryUnavailable

	accounts := []OpenAIAccountSecret{activeSoon, activeExpired, temp, none}
	// 窗口 15s：只放行 10s 内到期的 active 账户（到期账户不回收）。
	out, err := recoverableUnavailableOpenAIAccounts(accounts, whInt64Ptr(15_000), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != "active_soon" {
		t.Fatalf("过滤结果 = %v", idsWh(out))
	}
	// 坏时间戳必须报错。
	if _, err := recoverableUnavailableOpenAIAccounts(
		[]OpenAIAccountSecret{activeSoon, broken}, nil, now); err == nil {
		t.Fatal("坏 cooldownUntil 必须报错")
	}
	// nil 窗口 → 30s 默认：20s 内的 temp 也入选。
	out, err = recoverableUnavailableOpenAIAccounts(accounts, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("默认窗口结果 = %v", idsWh(out))
	}
	// 负窗口 → 0：仅已到期。
	out, err = recoverableUnavailableOpenAIAccounts(accounts, whInt64Ptr(-1), now)
	if err != nil || len(out) != 0 {
		t.Fatalf("负窗口结果 = %v err=%v", idsWh(out), err)
	}
	// 时间戳解析辅助。
	if _, ok := rfc3339Millis("2026-01-02T03:04:05Z"); !ok {
		t.Fatal("Z 时间必须可解析")
	}
	if _, ok := rfc3339Millis("nope"); ok {
		t.Fatal("垃圾输入必须失败")
	}
	if normalizeRecoverableUnavailableWindowMs(nil) != 30_000 || normalizeRecoverableUnavailableWindowMs(whInt64Ptr(-5)) != 0 {
		t.Fatal("窗口归一化契约失败")
	}
	// 并发键：凭据来源账户优先，空白 id 跳过。
	source := "root"
	withSource := testAccount("child", "sys")
	withSource.CredentialSourceAccountID = &source
	if got := gatewayAccountConcurrencyAccountID(&withSource); got != "root" {
		t.Fatalf("凭据来源键 = %q", got)
	}
	blank := testAccount("child2", "sys")
	blank.CredentialSourceAccountID = whStringPtr("  ")
	if got := gatewayAccountConcurrencyAccountID(&blank); got != "child2" {
		t.Fatalf("空白来源回退 = %q", got)
	}
	// 空白凭据来源回退物理 id；重复 root 去重。
	if got := gatewayAccountConcurrencyAccountIDs([]OpenAIAccountSecret{withSource, withSource, blank}); len(got) != 2 || got[0] != "root" || got[1] != "child2" {
		t.Fatalf("键去重 = %v", got)
	}
	if trimSpace(" \t a b\n") != "a b" {
		t.Fatalf("trimSpace = %q", trimSpace(" \t a b\n"))
	}
}

// 后台刷新：过期后异步刷新由 singleflight 去重且写回缓存。
func TestWhAccountsSharedModeBackgroundRefresh(t *testing.T) {
	models := newFakeModels()
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testAccount("a1", "sys")}}
	counter := &whCountingModels{fakeModels: models}
	clock := newManualClock()
	shared := newFakeSharedFactory()
	svc := newTestService(t, counter, clock, func(o *Options) { o.Shared = shared })
	ctx := context.Background()

	if _, err := svc.ListCachedOpenAIAccountsForGroupAsync(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if calls := counter.accountCalls; calls != 1 {
		t.Fatalf("共享模式首载 = %d", calls)
	}
	// 推进超过 revalidate 窗口：陈旧值立即返回 + 后台刷新。
	clock.Advance(120 * time.Second)
	stale, err := svc.ListCachedOpenAIAccountsForGroupAsync(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{})
	if err != nil || len(stale) != 1 {
		t.Fatalf("陈旧返回 = %v err=%v", stale, err)
	}
	if err := svc.AwaitBackgroundWork(ctx); err != nil {
		t.Fatal(err)
	}
	if calls := counter.accountCalls; calls != 2 {
		t.Fatalf("后台刷新 loader 调用 = %d", calls)
	}
	// pendingRuntimeLoadCount 测试视图。
	if n := svc.pendingRuntimeLoadCount(); n != 0 {
		t.Fatalf("pending = %d", n)
	}
}

// 设置缓存 async 入口：standalone 直落同步路径；共享模式跨进程命中。
func TestWhSettingsAsyncAndShared(t *testing.T) {
	models := newFakeModels()
	clock := newManualClock()
	svc := newTestService(t, models, clock, nil)
	ctx := context.Background()

	if _, err := svc.ReadCachedGatewaySettingsAsync(ctx); err != nil {
		t.Fatal(err)
	}
	if models.settingsCalls != 1 {
		t.Fatalf("async 首载 = %d", models.settingsCalls)
	}
	// 共享模式：第二个进程从共享缓存命中，不触发 loader。
	shared := newFakeSharedFactory()
	seed := newTestService(t, models, clock, func(o *Options) { o.Shared = shared })
	if _, err := seed.ReadCachedGatewaySettingsAsync(ctx); err != nil {
		t.Fatal(err)
	}
	if models.settingsCalls != 2 {
		t.Fatalf("共享 seed 首载 = %d", models.settingsCalls)
	}
	cold, err := New(models, Options{Clock: clock, Shared: shared})
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	if _, err := cold.ReadCachedGatewaySettingsAsync(ctx); err != nil {
		t.Fatal(err)
	}
	if models.settingsCalls != 2 {
		t.Fatalf("冷进程必须命中共享缓存, loader 调用 = %d", models.settingsCalls)
	}
}

// 目录缓存的共享路径与模型路由索引。
func TestWhCatalogSharedPaths(t *testing.T) {
	models := newFakeModels()
	models.catalog["gpt"] = []ProviderModelCatalogItem{{
		ProviderCode: "gpt", Model: "m1", SupportedAPIProtocols: []string{"chat"},
	}}
	clock := newManualClock()
	shared := newFakeSharedFactory()
	svc := newTestService(t, models, clock, func(o *Options) { o.Shared = shared })
	ctx := context.Background()

	items, err := svc.ListCachedProviderModelCatalogAsync(ctx, ModelCatalogListOptions{ProviderCode: "gpt"})
	if err != nil || len(items) != 1 {
		t.Fatalf("目录读取 = %v err=%v", items, err)
	}
	// 第二个进程：共享目录命中。
	cold, err := New(models, Options{Clock: clock, Shared: shared})
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	before := models.catalogCalls["gpt"]
	items, err = cold.ListCachedProviderModelCatalogAsync(ctx, ModelCatalogListOptions{ProviderCode: "gpt"})
	if err != nil || len(items) != 1 {
		t.Fatalf("冷进程目录 = %v err=%v", items, err)
	}
	if models.catalogCalls["gpt"] != before {
		t.Fatal("冷进程必须命中共享目录")
	}
	// 路由索引共享 entry 的序列化往返（toEntry 容错分支）。
	roundTrip := sharedProviderModelRouteIndexEntry{Entries: [][2]any{
		{"m1", []any{"gpt", "gpt2"}}, {"bad", "not-array"}, {42, []any{"x"}},
	}}.toEntry()
	if len(roundTrip.index) != 1 || len(roundTrip.index["m1"]) != 2 {
		t.Fatalf("索引往返 = %+v", roundTrip.index)
	}
	sharedOut := providerModelRouteIndexToShared(providerModelRouteIndexCacheEntry{index: map[string][]string{"m1": {"gpt"}}})
	if len(sharedOut.Entries) != 1 {
		t.Fatalf("共享索引 = %+v", sharedOut.Entries)
	}
}

func idsWh(accounts []OpenAIAccountSecret) []string {
	out := make([]string, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, account.ID)
	}
	return out
}

func whInt64Ptr(v int64) *int64    { return &v }
func whStringPtr(v string) *string { return &v }

// 类型 Clone 的深拷贝契约（指针字段不复用原指针）。
func TestWhTypeClones(t *testing.T) {
	limit := int64(5)
	expires := "2026-01-01"
	limits := UserRequestLimits{PerMinute: &limit, ExpiresOn: &expires}
	cloned := limits.Clone()
	if cloned.PerMinute == limits.PerMinute || *cloned.PerMinute != limit {
		t.Fatal("UserRequestLimits.Clone 必须复制指针")
	}
	cloned.PerMinute = whInt64Ptr(9)
	if *limits.PerMinute != 5 {
		t.Fatal("克隆必须与原值隔离")
	}
	config := RouteStrategyNormalRoutingConfig{SchedulingPreference: "speed_first", Raw: json.RawMessage(`{"a":1}`)}
	if got := config.Clone(); string(got.Raw) != `{"a":1}` || got.SchedulingPreference != "speed_first" {
		t.Fatalf("speed_first 克隆 = %+v", got)
	}
	if got := (RouteStrategyNormalRoutingConfig{SchedulingPreference: "cost_first", Raw: json.RawMessage(`{"a":1}`)}).Clone(); got.SchedulingPreference != "cost_first" || got.Raw != nil {
		t.Fatalf("cost_first 折叠 = %+v", got)
	}
	hybrid := ApiKeyHybridRoutingConfig{Raw: json.RawMessage(`{"levelRoutes":[]}`)}
	if got := hybrid.Clone(); string(got.Raw) != `{"levelRoutes":[]}` {
		t.Fatalf("hybrid 克隆 = %+v", got)
	}
	source := "explicit_hybrid_route"
	mapping := AccountModelMapping{RuntimeSource: &source}
	if got := mapping.Clone(); got.RuntimeSource == mapping.RuntimeSource || *got.RuntimeSource != source {
		t.Fatal("映射克隆指针契约失败")
	}
	state := AccountAPIKeyRuntimeSelectionState{CooldownUntil: whStringPtr("c"), LastErrorCode: whStringPtr("e"), Generation: whStringPtr("g")}
	clonedState := state.Clone()
	if clonedState.CooldownUntil == state.CooldownUntil || *clonedState.CooldownUntil != "c" {
		t.Fatal("运行态克隆指针契约失败")
	}
	if got := cloneInt64(nil); got != nil {
		t.Fatal("nil cloneInt64 必须返回 nil")
	}
	if got := cloneInt64(&limit); got == &limit || *got != 5 {
		t.Fatal("cloneInt64 值契约失败")
	}
}

// whCountingModels 统计账户 loader 调用次数。
type whCountingModels struct {
	*fakeModels
	accountCalls int
}

func (m *whCountingModels) ListOpenAIAccountsForGroupResult(ctx context.Context, groupID, systemAccountID string, opts OpenAIAccountsForGroupOptions) (OpenAIAccountsForGroupResult, error) {
	m.accountCalls++
	return m.fakeModels.ListOpenAIAccountsForGroupResult(ctx, groupID, systemAccountID, opts)
}
