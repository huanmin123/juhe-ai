package gatewayruntimecache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

func whIntPtr(v int) *int { return &v }

// whWarnLogger 捕获 Warn 事件（Options.Logger 注入点）。后台刷新 goroutine
// 会并发写入，读取方必须经 snapshot()（-race 下裸切片是数据竞争）。
type whWarnLogger struct {
	mu     sync.Mutex
	events []string
}

func (l *whWarnLogger) Warn(event string, _ map[string]any, _ string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

// snapshot 返回事件的加锁拷贝，供测试断言读取。
func (l *whWarnLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// whFailingShared 是全部写入失败的 SharedCache（驱动共享缓存写失败告警）。
type whFailingShared struct{ failSet bool }

func (c *whFailingShared) Get(context.Context, string, any) (bool, error) { return false, nil }

func (c *whFailingShared) Set(context.Context, string, any, time.Duration) error {
	if c.failSet {
		return errors.New("wh 注入共享写入失败")
	}
	return nil
}

func (c *whFailingShared) Clear(context.Context) error { return nil }

type whFailingFactory struct{ inner SharedCacheFactory }

func (f *whFailingFactory) Cache(name string) SharedCache {
	return &whFailingShared{failSet: true}
}

// RedisSharedCache 工厂：URL 校验、命名空间前缀与 JSON 往返（miniredis）。
func TestWhRedisSharedCacheFactory(t *testing.T) {
	if _, _, err := NewRedisSharedCacheFactory("  ", "ns"); err == nil {
		t.Fatal("空 URL 必须报错")
	}
	if _, _, err := NewRedisSharedCacheFactory("://bad", "ns"); err == nil {
		t.Fatal("坏 URL 必须报错")
	}
	server := miniredis.RunT(t)
	factory, closeFn, err := NewRedisSharedCacheFactory("redis://"+server.Addr()+"/0", "wh-ns")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	ctx := context.Background()
	cache := factory.Cache("models")
	missOK, missErr := cache.Get(ctx, "missing", &map[string]any{})
	if missOK || missErr != nil {
		t.Fatalf("缺失键 = ok=%v err=%v", missOK, missErr)
	}
	if err := cache.Set(ctx, "k1", map[string]any{"a": 1}, time.Minute); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	ok, err := cache.Get(ctx, "k1", &out)
	if err != nil || !ok || out["a"].(float64) != 1 {
		t.Fatalf("往返 = %v ok=%v err=%v", out, ok, err)
	}
	// 非 JSON 载荷：读取按未命中处理（Node catch 同款）。
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	if err := client.Set(ctx, "juhe-ai:wh-ns:models:raw", "{nope", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	readOK, readErr := cache.Get(ctx, "raw", &out)
	if readOK || readErr != nil {
		t.Fatalf("非 JSON 载荷 = ok=%v err=%v", readOK, readErr)
	}
	// Clear 扫描删除全部前缀键。
	if err := cache.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if stillOK, _ := cache.Get(ctx, "k1", &out); stillOK {
		t.Fatal("Clear 后必须为空")
	}
}

// 响应检查策略缓存的共享模式：跨进程命中、陈旧后台刷新与写失败告警。
func TestWhInspectionSharedModeAndWarn(t *testing.T) {
	models := newFakeModels()
	models.policies["openai:gpt"] = []ResponseInspectionPolicySummary{{
		ID: "p1", Name: "n", Enabled: true, Priority: 1, ScopeType: "provider",
		ProtocolCode: "openai", ProviderCode: whStringPtr2("gpt"), Match: ResponseInspectionPolicyMatch{}, Action: "observe",
	}}
	clock := newManualClock()
	shared := newFakeSharedFactory()
	logger := &whWarnLogger{}
	svc := newTestService(t, models, clock, func(o *Options) {
		o.Shared = shared
		o.Logger = logger
	})
	ctx := context.Background()

	policies, err := svc.ListCachedActiveResponseInspectionPoliciesAsync(ctx, "openai", "gpt")
	if err != nil || len(policies) != 1 || policies[0].ID != "p1" {
		t.Fatalf("首读 = %v err=%v", policies, err)
	}
	if calls := models.policyCalls["openai:gpt"]; calls != 1 {
		t.Fatalf("首读 loader = %d", calls)
	}
	// 冷进程命中共享缓存（getSharedInspection/toEntry 路径）。
	cold, err := New(models, Options{Clock: clock, Shared: shared})
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	policies, err = cold.ListCachedActiveResponseInspectionPoliciesAsync(ctx, "openai", "gpt")
	if err != nil || len(policies) != 1 {
		t.Fatalf("冷进程 = %v err=%v", policies, err)
	}
	if calls := models.policyCalls["openai:gpt"]; calls != 1 {
		t.Fatalf("冷进程不得触发 loader, 调用 = %d", calls)
	}
	// 超过 retain TTL：共享命中已过期 → 后台刷新（去重 + 完成后写回）。
	clock.Advance(11 * time.Minute)
	if _, err := svc.ListCachedActiveResponseInspectionPoliciesAsync(ctx, "openai", "gpt"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AwaitBackgroundWork(ctx); err != nil {
		t.Fatal(err)
	}
	if calls := models.policyCalls["openai:gpt"]; calls != 2 {
		t.Fatalf("后台刷新 loader = %d", calls)
	}
	// 写失败告警路径：共享写失败时记录 logSharedFailure 事件。
	failing := &whFailingFactory{}
	logger2 := &whWarnLogger{}
	svc2 := newTestService(t, models, clock, func(o *Options) {
		o.Shared = failing
		o.Logger = logger2
	})
	if _, err := svc2.ListCachedActiveResponseInspectionPoliciesAsync(ctx, "openai", "gpt"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range logger2.snapshot() {
		if event == "gateway_response_inspection_policy_shared_cache_write_failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("共享写失败告警缺失: %v", logger2.snapshot())
	}
	// 共享读取失败同样告警。
	svc3 := newTestService(t, models, clock, func(o *Options) { o.Logger = logger2 })
	svc3.sharedInspection = &whReadErrShared{}
	if _, err := svc3.ListCachedActiveResponseInspectionPoliciesAsync(ctx, "openai", "gpt"); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, event := range logger2.snapshot() {
		if event == "gateway_response_inspection_policy_shared_cache_read_failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("共享读失败告警缺失")
	}
}

// whReadErrShared 是读取失败的 SharedCache。
type whReadErrShared struct{}

func (c *whReadErrShared) Get(context.Context, string, any) (bool, error) {
	return false, errors.New("wh 注入共享读取失败")
}

func (c *whReadErrShared) Set(context.Context, string, any, time.Duration) error { return nil }

func (c *whReadErrShared) Clear(context.Context) error { return nil }

// 共享模式账户读取契约（Node redis 驱动同款）：app 进程缓存禁用（凭据不进
// 共享层），每次 async 读都落 loader；loader 失败直接上抛，无 stale 回退。
func TestWhAccountsSharedModeAlwaysLoadsAndSurfacesLoaderFailure(t *testing.T) {
	models := newFakeModels()
	models.accounts["g1"] = OpenAIAccountsForGroupResult{Accounts: []OpenAIAccountSecret{testAccount("a1", "sys")}}
	counter := &whFailingAfterModels{fakeModels: models, failAfter: 1}
	clock := newManualClock()
	shared := newFakeSharedFactory()
	svc := newTestService(t, counter, clock, func(o *Options) {
		o.Shared = shared
	})
	ctx := context.Background()
	if _, err := svc.ListCachedOpenAIAccountsForGroupAsync(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err != nil {
		t.Fatal(err)
	}
	if calls := counter.calls; calls != 1 {
		t.Fatalf("首读 loader = %d", calls)
	}
	// retain 窗口内再次读取仍触发 loader（共享模式进程缓存为 no-op），
	// 且 loader 失败直接上抛，不用任何缓存兜底。
	clock.Advance(2 * time.Minute)
	if _, err := svc.ListCachedOpenAIAccountsForGroupAsync(ctx, "g1", "sys", CachedOpenAIAccountsForGroupOptions{}); err == nil {
		t.Fatal("共享模式二次读取必须重新加载并上抛 loader 失败")
	}
	if calls := counter.calls; calls != 2 {
		t.Fatalf("共享模式二次读取 loader = %d", calls)
	}
}

// whFailingAfterModels 在第 N 次调用后让账户 loader 失败。
type whFailingAfterModels struct {
	*fakeModels
	calls     int
	failAfter int
}

func (m *whFailingAfterModels) ListOpenAIAccountsForGroupResult(ctx context.Context, groupID, systemAccountID string, opts OpenAIAccountsForGroupOptions) (OpenAIAccountsForGroupResult, error) {
	m.calls++
	if m.calls > m.failAfter {
		return OpenAIAccountsForGroupResult{}, errors.New("wh 注入刷新失败")
	}
	return m.fakeModels.ListOpenAIAccountsForGroupResult(ctx, groupID, systemAccountID, opts)
}

// 运行态身份索引迁移：同一 key 的 apiKeyID 变更时旧索引被回收。
func TestWhRuntimeIdentityIndexMigration(t *testing.T) {
	models := newFakeModels()
	row1 := testAPIKeyRow("key1", RouteStrategyModeNormal, "g1")
	runtime1 := staticRuntime(t, models, row1, nil, nil)
	row2 := testAPIKeyRow("key2", RouteStrategyModeNormal, "g1")
	runtime2 := staticRuntime(t, models, row2, nil, nil)
	models.runtimes["sk-same"] = runtime1
	clock := newManualClock()
	svc := newTestService(t, models, clock, nil)
	ctx := context.Background()

	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-same"); err != nil {
		t.Fatal(err)
	}
	if n := svc.runtimeCacheSize(); n != 1 {
		t.Fatalf("runtime 缓存 = %d", n)
	}
	// 载荷里的 apiKeyID 变化：身份索引迁移（旧映射被移除）。
	models.runtimes["sk-same"] = runtime2
	svc.ClearGatewayRuntimeCache("test_rewrite")
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-same"); err != nil {
		t.Fatal(err)
	}
	svc.InvalidateGatewayRuntimeCacheByAPIKeyID("key2", nil)
	// 新身份的键已被定向失效：再次读取重新装载。
	if _, err := svc.ReadCachedGatewayRuntimeAsync(ctx, "sk-same"); err != nil {
		t.Fatal(err)
	}
	// 纯助手：克隆绑定行。
	cloned := GatewayAPIKeyGroupBindingRow{ID: "b"}.Clone()
	if cloned.ID != "b" {
		t.Fatal("绑定克隆失败")
	}
}

func whStringPtr2(v string) *string { return &v }
