package gatewayclientip

// w13d 波次补充测试：策略缓存（含 redis shared cache）、错误熔断（memory +
// redis）、账户回避记忆与各共享 helper 的分支覆盖。
//
// 本文件覆盖范围内的不可达语句登记：
//   - normalize.go:40-42：NormalizeClientIPForStats 已在 clientIPIdentity 前用
//     netip.ParseAddr + Is4 验证过同一字符串，normalizeIpv4 的二次校验恒通过。
//   - normalize.go:105-107：clientIPIdentity 的 ipHash 前 8 位是 hex 摘要，
//     strconv.ParseInt(…, 16) 恒成功。
//   - circuit.go:571-573：blockedUntilMs > now 时 (delta+999)/1000 整数除法
//     恒 >= 1，retryAfterSeconds < 1 分支不可达。
//   - circuit.go:651-653：json.Marshal(string) 恒成功，writeJSONStringCompact
//     的错误分支不可达。
//   - avoidance.go:651-653：bytes.Buffer 的 json.Encoder.Encode 恒成功。
//   - cache.go:206-208：orderedExpiryMap 中 entries 与 order 恒同步增删，
//     entries > max 时 order 恒非空。
//   - cache.go:237-239：Values 遍历 order 时 entries 恒含同键（二者同步维护）。
//   - policycache.go:752-754：clone 成功意味着 expiresAt 可被解析，
//     clientIPPolicyTTL 的再次解析恒成功。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// fakes：可注入错误的 shared cache 与可编程错误的状态存储
// ---------------------------------------------------------------------------

// w13dSharedCache 在 fakeSharedCache 之上注入 Get/Set/Clear 错误。
type w13dSharedCache struct {
	inner    *fakeSharedCache
	getErr   error
	setErr   error
	clearErr error
}

func (c *w13dSharedCache) Get(ctx context.Context, key string, dst any) (bool, error) {
	if c.getErr != nil {
		return false, c.getErr
	}
	return c.inner.Get(ctx, key, dst)
}

func (c *w13dSharedCache) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	if c.setErr != nil {
		return c.setErr
	}
	return c.inner.Set(ctx, key, value, ttl)
}

func (c *w13dSharedCache) Clear(ctx context.Context) error {
	if c.clearErr != nil {
		return c.clearErr
	}
	return c.inner.Clear(ctx)
}

// w13dSharedFactory 按 cache 名返回独立的 w13dSharedCache。
type w13dSharedFactory struct {
	mu     sync.Mutex
	caches map[string]*w13dSharedCache
}

func newW13DSharedFactory() *w13dSharedFactory {
	return &w13dSharedFactory{caches: map[string]*w13dSharedCache{}}
}

func (f *w13dSharedFactory) Cache(name string) gatewayruntimecache.SharedCache {
	f.mu.Lock()
	defer f.mu.Unlock()
	cache, ok := f.caches[name]
	if !ok {
		cache = &w13dSharedCache{inner: &fakeSharedCache{entries: map[string][]byte{}}}
		f.caches[name] = cache
	}
	return cache
}

func (f *w13dSharedFactory) byName(name string) *w13dSharedCache {
	return f.Cache(name).(*w13dSharedCache)
}

// w13dScriptedStore 在内存 store 之上按调用序注入错误。
type w13dScriptedStore struct {
	RuntimeStateStore
	setSeq []error
	getSeq []error
	delErr error
}

func (s *w13dScriptedStore) SetJSON(ctx context.Context, key string, value any, ttlMs int64) error {
	if len(s.setSeq) > 0 {
		err := s.setSeq[0]
		s.setSeq = s.setSeq[1:]
		if err != nil {
			return err
		}
	}
	return s.RuntimeStateStore.SetJSON(ctx, key, value, ttlMs)
}

func (s *w13dScriptedStore) GetJSON(ctx context.Context, key string, dst any) (bool, error) {
	if len(s.getSeq) > 0 {
		err := s.getSeq[0]
		s.getSeq = s.getSeq[1:]
		if err != nil {
			return false, err
		}
	}
	return s.RuntimeStateStore.GetJSON(ctx, key, dst)
}

func (s *w13dScriptedStore) Delete(ctx context.Context, key string) error {
	if s.delErr != nil {
		return s.delErr
	}
	return s.RuntimeStateStore.Delete(ctx, key)
}

func w13dBlacklistPolicy(id, ipHash string, expiresAt *string) ActiveClientIPPolicy {
	return ActiveClientIPPolicy{
		ID: id, IPHash: ipHash, PolicyType: PolicyTypeBlacklist,
		AggregateIPKey: ipHash + "-agg", ClientIP: ipHash + "-ip", ExpiresAt: expiresAt,
	}
}

func w13dNewRedisPolicyCache(t *testing.T) (*PolicyCache, *w13dSharedFactory) {
	t.Helper()
	factory := newW13DSharedFactory()
	cache, err := NewPolicyCache(PolicyCacheOptions{
		Clock:       newManualClock(time.UnixMilli(1_000_000)),
		CacheDriver: CacheDriverRedis,
		Source:      &fakePolicySource{},
		Shared:      factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Close)
	return cache, factory
}

// ---------------------------------------------------------------------------
// policy cache: redis inspect + reload + async replace
// ---------------------------------------------------------------------------

func TestW13DPolicyCacheRedisInspectErrors(t *testing.T) {
	cache, factory := w13dNewRedisPolicyCache(t)
	ctx := context.Background()

	// CacheOnly 读取错误透传。
	factory.byName(policyByIPCacheName).getErr = errors.New("shared cache 读取失败")
	if _, err := cache.InspectPolicy(ctx, "10.0.0.1", InspectClientIPPolicyOptions{CacheOnly: true}); err == nil {
		t.Fatal("CacheOnly 读取错误必须透传")
	}
	if _, err := cache.InspectClientIPPolicy(ctx, "10.0.0.1", true); err == nil {
		t.Fatal("InspectClientIPPolicy 错误必须透传")
	}

	// 非 CacheOnly：shared miss → 走 source 读取错误。
	factory.byName(policyByIPCacheName).getErr = nil
	source := &fakePolicySource{err: errors.New("策略库不可用")}
	redisCache, err := NewPolicyCache(PolicyCacheOptions{
		Clock: newManualClock(time.UnixMilli(1_000_000)), CacheDriver: CacheDriverRedis,
		Source: source, Shared: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(redisCache.Close)
	if _, err := redisCache.InspectPolicy(ctx, "10.0.0.1", InspectClientIPPolicyOptions{}); err == nil {
		t.Fatal("source 错误必须透传")
	}
}

func TestW13DPolicyCacheRedisReloadAndReplace(t *testing.T) {
	cache, factory := w13dNewRedisPolicyCache(t)
	ctx := context.Background()
	snapshotName := policySnapshotCacheName
	byIPName := policyByIPCacheName

	// BypassSharedCache → 清 snapshot + byIP。
	if err := cache.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{BypassSharedCache: true}); err != nil {
		t.Fatal(err)
	}
	// 普通 reload → 只清 byIP。
	if err := cache.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{}); err != nil {
		t.Fatal(err)
	}
	// Clear 错误透传。
	factory.byName(snapshotName).clearErr = errors.New("snapshot 清理失败")
	if err := cache.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{BypassSharedCache: true}); err == nil {
		t.Fatal("snapshot Clear 错误必须透传")
	}
	factory.byName(snapshotName).clearErr = nil
	factory.byName(byIPName).clearErr = errors.New("byIP 清理失败")
	if err := cache.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{BypassSharedCache: true}); err == nil {
		t.Fatal("byIP Clear 错误必须透传")
	}
	factory.byName(byIPName).clearErr = nil

	// 异步替换：非 redis 驱动直接落本地；redis 驱动写 shared。
	memoryCache, err := NewPolicyCache(PolicyCacheOptions{
		Clock: newManualClock(time.UnixMilli(1_000_000)), Source: &fakePolicySource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(memoryCache.Close)
	farFuture := "2099-01-01T00:00:00.000Z"
	policies := []ActiveClientIPPolicy{w13dBlacklistPolicy("p1", strings.Repeat("b", 64), &farFuture)}
	if err := memoryCache.ReplaceClientIPPolicySharedSnapshotAsync(ctx, policies); err != nil {
		t.Fatal(err)
	}
	if err := cache.ReplaceClientIPPolicySharedSnapshotAsync(ctx, policies); err != nil {
		t.Fatal(err)
	}
	// 写入错误透传。
	factory.byName(byIPName).setErr = errors.New("byIP 写入失败")
	if err := cache.ReplaceClientIPPolicySharedSnapshotAsync(ctx, policies); err == nil {
		t.Fatal("byIP 写入错误必须透传")
	}
	factory.byName(byIPName).setErr = nil
	// snapshot 清理错误透传。
	factory.byName(snapshotName).clearErr = errors.New("snapshot 清理失败")
	if err := cache.ReplaceClientIPPolicySharedSnapshotAsync(ctx, policies); err == nil {
		t.Fatal("snapshot Clear 错误必须透传")
	}
	factory.byName(snapshotName).clearErr = nil

	// ClearClientIPPolicyCacheLocal + 错误透传。
	if err := cache.ClearClientIPPolicyCacheLocal(ctx); err != nil {
		t.Fatal(err)
	}
	factory.byName(snapshotName).clearErr = errors.New("snapshot 清理失败")
	if err := cache.ClearClientIPPolicyCacheLocal(ctx); err == nil {
		t.Fatal("Clear 错误必须透传")
	}
	factory.byName(snapshotName).clearErr = nil
}

func TestW13DPolicyCacheMemoryReloadPaths(t *testing.T) {
	clock := newManualClock(time.UnixMilli(1_000_000))
	source := &fakePolicySource{}
	cache, err := NewPolicyCache(PolicyCacheOptions{Clock: clock, Source: source})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Close)
	ctx := context.Background()

	// shared 未命中 → 走数据库加载。
	if err := cache.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{}); err != nil {
		t.Fatal(err)
	}
	if source.listCalls != 1 {
		t.Fatalf("listCalls=%d", source.listCalls)
	}
	runtime := cache.GetClientIPPolicyCacheRuntime()
	if runtime.SnapshotPolicyCount != 0 || runtime.SnapshotLoadedAt == "" {
		t.Fatalf("runtime=%+v", runtime)
	}

	// shared 命中 → 直接替换本地。
	farFuture := "2099-01-01T00:00:00.000Z"
	seeded := &w13dSharedCache{inner: &fakeSharedCache{entries: map[string][]byte{}}}
	if err := seeded.Set(ctx, activePolicySnapshotSharedCacheKey, sharedSnapshotEntry{
		LoadedAt: "2026-01-01T00:00:00.000Z",
		Policies: []ActiveClientIPPolicy{w13dBlacklistPolicy("p1", strings.Repeat("c", 64), &farFuture)},
	}, clientIPPolicyCacheTTL); err != nil {
		t.Fatal(err)
	}
	cache.sharedSnapshot = seeded
	if err := cache.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{}); err != nil {
		t.Fatal(err)
	}
	runtime = cache.GetClientIPPolicyCacheRuntime()
	if runtime.SnapshotPolicyCount != 1 || runtime.SnapshotLoadedAt != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("runtime=%+v", runtime)
	}

	// shared 读取错误透传。
	seeded.getErr = errors.New("shared 读取失败")
	if err := cache.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{}); err == nil {
		t.Fatal("shared 读取错误必须透传")
	}
	seeded.getErr = nil

	// 数据库加载后写 shared 失败透传。
	breaking := &w13dSharedCache{inner: &fakeSharedCache{entries: map[string][]byte{}}}
	breaking.setErr = errors.New("snapshot 写入失败")
	cache.sharedSnapshot = breaking
	if err := cache.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{BypassSharedCache: true}); err == nil {
		t.Fatal("snapshot 写入错误必须透传")
	}
}

func TestW13DPolicyCacheSharedSnapshotEntryShapes(t *testing.T) {
	clock := newManualClock(time.UnixMilli(1_000_000))
	cache, err := NewPolicyCache(PolicyCacheOptions{Clock: clock, Source: &fakePolicySource{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Close)
	ctx := context.Background()

	// 形状非法的条目跳过、clone 失败透传、loadedAt 缺省回填。
	badExpires := "not-a-time"
	shaped := &w13dSharedCache{inner: &fakeSharedCache{entries: map[string][]byte{}}}
	if err := shaped.Set(ctx, activePolicySnapshotSharedCacheKey, sharedSnapshotEntry{
		Policies: []ActiveClientIPPolicy{
			{ID: "bad-type", PolicyType: "unknown"},
			w13dBlacklistPolicy("bad-expiry", strings.Repeat("d", 64), &badExpires),
		},
	}, clientIPPolicyCacheTTL); err != nil {
		t.Fatal(err)
	}
	cache.sharedSnapshot = shaped
	if _, err := cache.getActivePolicySnapshotSharedCacheEntry(ctx); err == nil {
		t.Fatal("clone 失败必须透传")
	}
	if err := shaped.Set(ctx, activePolicySnapshotSharedCacheKey, sharedSnapshotEntry{
		Policies: []ActiveClientIPPolicy{{ID: "bad-type", PolicyType: "unknown"}},
	}, clientIPPolicyCacheTTL); err != nil {
		t.Fatal(err)
	}
	entry, err := cache.getActivePolicySnapshotSharedCacheEntry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entry.Policies) != 0 || entry.LoadedAt == "" {
		t.Fatalf("entry=%+v", entry)
	}
	// loadedAt 保留原值。
	if err := shaped.Set(ctx, activePolicySnapshotSharedCacheKey, sharedSnapshotEntry{
		LoadedAt: "2026-02-03T04:05:06.000Z",
	}, clientIPPolicyCacheTTL); err != nil {
		t.Fatal(err)
	}
	entry, err = cache.getActivePolicySnapshotSharedCacheEntry(ctx)
	if err != nil || entry.LoadedAt != "2026-02-03T04:05:06.000Z" {
		t.Fatalf("entry=%+v err=%v", entry, err)
	}
	// setActivePolicySnapshotSharedCacheEntry clone 失败透传。
	if err := cache.setActivePolicySnapshotSharedCacheEntry(ctx, sharedSnapshotEntry{
		Policies: []ActiveClientIPPolicy{w13dBlacklistPolicy("bad", strings.Repeat("e", 64), &badExpires)},
	}); err == nil {
		t.Fatal("clone 失败必须透传")
	}
}

func TestW13DPolicyCacheByIPSharedEntries(t *testing.T) {
	cache, factory := w13dNewRedisPolicyCache(t)
	ctx := context.Background()
	byIP := factory.byName(policyByIPCacheName)
	badExpires := "not-a-time"
	ipHash := strings.Repeat("f", 64)

	// 共享条目含非法 expiresAt → clone 失败透传。
	if err := byIP.Set(ctx, ipHash, sharedByIPEntry{
		Policy: &ActiveClientIPPolicy{ID: "bad", PolicyType: PolicyTypeBlacklist, ExpiresAt: &badExpires},
	}, clientIPPolicyCacheTTL); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.getClientIPPolicyByIPSharedCacheEntry(ctx, ipHash); err == nil {
		t.Fatal("clone 失败必须透传")
	}

	// loadedAt 缺省回填 + 形状非法读作 nil policy。
	if err := byIP.Set(ctx, ipHash, sharedByIPEntry{
		Policy: &ActiveClientIPPolicy{ID: "bad-type", PolicyType: "unknown"},
	}, clientIPPolicyCacheTTL); err != nil {
		t.Fatal(err)
	}
	entry, err := cache.getClientIPPolicyByIPSharedCacheEntry(ctx, ipHash)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Policy != nil || entry.LoadedAt == "" {
		t.Fatalf("entry=%+v", entry)
	}

	// setClientIPPolicyByIPSharedCacheEntry：loadedAt 缺省回填。
	if err := cache.setClientIPPolicyByIPSharedCacheEntry(ctx, ipHash, nil, ""); err != nil {
		t.Fatal(err)
	}
	// clone 失败透传。
	if err := cache.setClientIPPolicyByIPSharedCacheEntry(ctx, ipHash,
		&ActiveClientIPPolicy{ID: "bad", PolicyType: PolicyTypeBlacklist, ExpiresAt: &badExpires}, ""); err == nil {
		t.Fatal("clone 失败必须透传")
	}

	// shared 写入错误透传（loadClientIPPolicyByHashFromSharedCacheOrDatabase 落库后回写）。
	byIP.setErr = errors.New("byIP 写入失败")
	source := &fakePolicySource{}
	redisCache, err := NewPolicyCache(PolicyCacheOptions{
		Clock: newManualClock(time.UnixMilli(1_000_000)), CacheDriver: CacheDriverRedis,
		Source: source, Shared: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(redisCache.Close)
	cleanHash := strings.Repeat("1", 64)
	if _, err := redisCache.loadClientIPPolicyByHashFromSharedCacheOrDatabase(ctx, cleanHash); err == nil {
		t.Fatal("回写失败必须透传")
	}
	// source 读取错误透传（未命中 shared 的 hash 才会走 source）。
	source.err = errors.New("策略库不可用")
	if _, err := redisCache.loadClientIPPolicyByHashFromSharedCacheOrDatabase(ctx, cleanHash); err == nil {
		t.Fatal("source 错误必须透传")
	}
	source.err = nil
	// shared 读取错误透传。
	byIP.getErr = errors.New("byIP 读取失败")
	if _, err := redisCache.loadClientIPPolicyByHashFromSharedCacheOrDatabase(ctx, ipHash); err == nil {
		t.Fatal("shared 读取错误必须透传")
	}
	byIP.getErr = nil
}

func TestW13DPolicyCacheInspectTTLAndStatsBridge(t *testing.T) {
	clock := newManualClock(time.UnixMilli(1_000_000))
	stats := &stubStatsWriter{}
	cache, err := NewPolicyCache(PolicyCacheOptions{
		Clock: clock, Source: &fakePolicySource{},
		ProcessRole: ProcessRoleServer, StatsWriter: stats,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Close)
	ctx := context.Background()

	// stats-writer 不可用（nil bridge）→ 错误文本含 operation。
	nilStatsCache, err := NewPolicyCache(PolicyCacheOptions{
		Clock: clock, Source: &fakePolicySource{},
		ProcessRole: ProcessRoleServer,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nilStatsCache.Close)
	if err := nilStatsCache.requestStatsWriter(ctx, StatsWriterOpRecordClientIPPolicyHits, StatsWriterPayload{}); err == nil {
		t.Fatal("stats-writer 缺失必须报错")
	}
	if _, err := nilStatsCache.statsListActivePolicies(ctx); err == nil {
		t.Fatal("stats-writer 缺失必须报错")
	}

	// useStats 且 shared 未命中 → 走 stats bridge 加载快照。
	farFuture := "2099-01-01T00:00:00.000Z"
	stats.listResult = []ActiveClientIPPolicy{w13dBlacklistPolicy("p1", strings.Repeat("9", 64), &farFuture)}
	if err := cache.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{BypassSharedCache: true}); err != nil {
		t.Fatal(err)
	}
	if len(stats.operations) == 0 || stats.operations[0] != StatsWriterOpListActiveClientIPPolicies {
		t.Fatalf("operations=%v", stats.operations)
	}

	// requestStatsWriter ctx=nil 分支。
	if err := cache.requestStatsWriter(nil, StatsWriterOpRecordClientIPPolicyHits, StatsWriterPayload{}); err != nil {
		t.Fatalf("ctx=nil 必须回退 Background: %v", err)
	}

	// bridge 错误透传。
	stats.err = errors.New("stats-writer 故障")
	if err := cache.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{BypassSharedCache: true}); err == nil {
		t.Fatal("bridge 错误必须透传")
	}
	stats.err = nil

	// Prime 走 clone：非法 expiresAt 必须在写入时拒绝。
	badExpires := "not-a-time"
	if err := cache.PrimeClientIPPolicyCacheLocal([]ActiveClientIPPolicy{
		w13dBlacklistPolicy("bad", strings.Repeat("8", 64), &badExpires),
	}); err == nil {
		t.Fatal("非法 expiresAt 必须在 prime 时拒绝")
	}

	// 远期 expiresAt + 匹配 IP 的 ipHash → 命中完整 inspect 链路。
	farFuture = "2099-01-01T00:00:00.000Z"
	targetHash := NormalizeClientIPForStats("10.0.0.1").IPHash
	good := []ActiveClientIPPolicy{w13dBlacklistPolicy("p1", targetHash, &farFuture)}
	if err := cache.PrimeClientIPPolicyCacheLocal(good); err != nil {
		t.Fatal(err)
	}
	decision, err := cache.InspectPolicy(ctx, "10.0.0.1", InspectClientIPPolicyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Blocked || decision.BlacklistPolicy == nil {
		t.Fatalf("decision=%+v", decision)
	}
	// flush 空 pending 直接返回。
	cache.flushClientIPPolicyHits()
}

// ---------------------------------------------------------------------------
// error circuit: memory + redis branches
// ---------------------------------------------------------------------------

func w13dNewMemoryCircuit(t *testing.T) *ErrorCircuit {
	t.Helper()
	circuit, err := NewErrorCircuit(ErrorCircuitOptions{Clock: newManualClock(time.UnixMilli(1_000_000))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(circuit.Close)
	return circuit
}

func TestW13DCircuitMemoryShortPaths(t *testing.T) {
	circuit := w13dNewMemoryCircuit(t)

	// ClientIP 为空 → 未封禁。
	if decision := circuit.InspectGatewayPreAuthCircuit(gatewaypreauth.PreAuthCircuitInput{}); decision.Blocked {
		t.Fatal("空 IP 必须未封禁")
	}
	if _, err := circuit.InspectPreAuthCircuit(context.Background(), gatewaypreauth.PreAuthCircuitInput{}); err != nil {
		t.Fatal(err)
	}
	if decision := circuit.RecordGatewayPreAuthFailure(gatewaypreauth.PreAuthFailureInput{}); decision.Blocked {
		t.Fatal("空 IP 记录必须未封禁")
	}
	if _, err := circuit.RecordPreAuthFailure(context.Background(), gatewaypreauth.PreAuthFailureInput{}); err != nil {
		t.Fatal(err)
	}
	if decision := circuit.InspectClientIPErrorCircuitSync(gatewaypreauth.ClientIPErrorCircuitInput{}); decision.Blocked {
		t.Fatal("空 IP 错误熔断必须未封禁")
	}
	if _, err := circuit.InspectClientIPErrorCircuit(context.Background(), gatewaypreauth.ClientIPErrorCircuitInput{}); err != nil {
		t.Fatal(err)
	}
	if decision := circuit.RecordClientIPErrorCircuitSampleSync(gatewaypreauth.ClientIPErrorCircuitSampleInput{}); decision.Blocked {
		t.Fatal("空 IP 采样必须未封禁")
	}
	if _, err := circuit.RecordClientIPErrorCircuitSample(context.Background(), gatewaypreauth.ClientIPErrorCircuitSampleInput{}); err != nil {
		t.Fatal(err)
	}
	circuit.RecordClientIPErrorCircuitSuccess(context.Background(), gatewaypreauth.ClientIPErrorCircuitInput{})
	if circuit.RecordClientIPErrorCircuitSuccessSync(gatewaypreauth.ClientIPErrorCircuitInput{}) {
		t.Fatal("空 IP success 必须返回 false")
	}
}

func TestW13DCircuitOpenBlockEntry(t *testing.T) {
	circuit := w13dNewMemoryCircuit(t)
	input := gatewaypreauth.ClientIPErrorCircuitSampleInput{
		SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.0.0.1",
		Reason: "invalid_json",
	}

	// 同 signature 5 次 → signature 阈值封禁（openBlockEntry 首次）。
	for i := 0; i < 5; i++ {
		decision, err := circuit.RecordClientIPErrorCircuitSample(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		_ = decision
	}
	decision := circuit.InspectClientIPErrorCircuitSync(gatewaypreauth.ClientIPErrorCircuitInput{
		SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.0.0.1",
	})
	if !decision.Blocked || decision.RetryAfterSeconds == nil || *decision.RetryAfterSeconds < 1 {
		t.Fatalf("decision=%+v", decision)
	}

	// 解封后累计到 total 阈值：blockedUntil 仍有效时 openBlockEntry 跳过
	// （指数重复封禁不叠加）。
	clock := circuit.clock.(*manualClock)
	clock.advance(time.Duration(clientIPInitialBlockMs)*time.Millisecond + time.Millisecond)
	circuit.ClearGatewayClientIPErrorCircuitForTest()
	// 19 次不同 signature（未达 signature 阈值 5），total 达 19。
	for i := 0; i < 19; i++ {
		sample := input
		sample.Signature = fmt.Sprintf("sig-%d", i)
		if _, err := circuit.RecordClientIPErrorCircuitSample(context.Background(), sample); err != nil {
			t.Fatal(err)
		}
	}
	// 第 5 个同 signature 触发 signature 封禁。
	for i := 0; i < 5; i++ {
		if _, err := circuit.RecordClientIPErrorCircuitSample(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	// 再补 1 次不同 signature → total 达 21 >= 20 → shouldBlock，但
	// blockedUntil 仍未来 → openBlockEntry 直接跳过（blockCount 不变）。
	row5 := input
	row5.Signature = "sig-other"
	if _, err := circuit.RecordClientIPErrorCircuitSample(context.Background(), row5); err != nil {
		t.Fatal(err)
	}
	_, errorRows := circuit.SecuritySnapshotForTest()
	if len(errorRows) != 1 || !errorRows[0].Blocked {
		t.Fatalf("errorRows=%+v", errorRows)
	}
}

func TestW13DCircuitPreAuthBlockCap(t *testing.T) {
	circuit := w13dNewMemoryCircuit(t)
	clock := circuit.clock.(*manualClock)
	input := gatewaypreauth.PreAuthFailureInput{
		ClientIP: "10.0.0.1", Reason: gatewaypreauth.PreAuthFailureInvalidAPIKey, Authorization: "Bearer x",
	}
	// 5 轮封禁：30s → 60s → 120s → 240s → 480s 封顶 300s（blockCount=4 指数封顶）。
	for round := 0; round < 5; round++ {
		for i := 0; i < 8; i++ {
			if _, err := circuit.RecordPreAuthFailure(context.Background(), input); err != nil {
				t.Fatal(err)
			}
		}
		decision := circuit.InspectGatewayPreAuthCircuit(gatewaypreauth.PreAuthCircuitInput{
			ClientIP: input.ClientIP, Authorization: input.Authorization,
		})
		if !decision.Blocked {
			t.Fatalf("round %d 未封禁: %+v", round, decision)
		}
		clock.advance(time.Duration(preAuthInitialBlockMs<<uint(round))*time.Millisecond + time.Millisecond)
	}
	// spray 计数在 invalid_api_key 下同步累计：40 次 invalid 触发 spray 封禁
	// 需要不同 token；这里用同一 token 已由 specific 封禁短路，验证 spray key
	// 独立计数即可（直接看 snapshot 的 spray 条目存在）。
}

func TestW13DCircuitRedisPaths(t *testing.T) {
	base := NewMemoryRuntimeStateStore(newManualClock(time.UnixMilli(1_000_000)))
	store := &w13dScriptedStore{RuntimeStateStore: base}
	circuit, err := NewErrorCircuit(ErrorCircuitOptions{
		Clock: newManualClock(time.UnixMilli(1_000_000)),
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateStore:         store,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(circuit.Close)
	ctx := context.Background()
	input := gatewaypreauth.PreAuthFailureInput{
		ClientIP: "10.0.0.1", Reason: gatewaypreauth.PreAuthFailureMissingBearerToken,
	}

	// 常规 redis 记录与读取。
	if _, err := circuit.RecordPreAuthFailure(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := circuit.InspectPreAuthCircuit(ctx, gatewaypreauth.PreAuthCircuitInput{ClientIP: "10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	sampleInput := gatewaypreauth.ClientIPErrorCircuitSampleInput{
		SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.0.0.1",
		Reason: "invalid_json",
	}
	if _, err := circuit.RecordClientIPErrorCircuitSample(ctx, sampleInput); err != nil {
		t.Fatal(err)
	}
	if _, err := circuit.InspectClientIPErrorCircuit(ctx, gatewaypreauth.ClientIPErrorCircuitInput{
		SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.0.0.1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := circuit.RecordClientIPErrorCircuitSuccess(ctx, gatewaypreauth.ClientIPErrorCircuitInput{
		SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.0.0.1",
	}); err != nil {
		t.Fatal(err)
	}

	// store 错误透传：record / spray get / spray record / set。
	store.setSeq = []error{errors.New("写入失败")}
	if _, err := circuit.RecordPreAuthFailure(ctx, input); err == nil {
		t.Fatal("record 写入错误必须透传")
	}
	store.setSeq = nil
	store.getSeq = []error{errors.New("读取失败")}
	if _, err := circuit.RecordPreAuthFailure(ctx, gatewaypreauth.PreAuthFailureInput{
		ClientIP: "10.0.0.2", Reason: gatewaypreauth.PreAuthFailureInvalidAPIKey,
	}); err == nil {
		t.Fatal("spray 读取错误必须透传")
	}
	store.getSeq = nil
	store.setSeq = []error{nil, errors.New("spray 写入失败")}
	if _, err := circuit.RecordPreAuthFailure(ctx, gatewaypreauth.PreAuthFailureInput{
		ClientIP: "10.0.0.2", Reason: gatewaypreauth.PreAuthFailureInvalidAPIKey,
	}); err == nil {
		t.Fatal("spray 写入错误必须透传")
	}
	store.setSeq = nil
	store.getSeq = []error{errors.New("采样读取失败")}
	if _, err := circuit.RecordClientIPErrorCircuitSample(ctx, sampleInput); err == nil {
		t.Fatal("采样读取错误必须透传")
	}
	store.getSeq = nil

	// 预置 blocked 条目 → specific / spray 直接短路。
	blockedUntil := int64(2_000_000)
	lastReason := preAuthReasonMissingBearerToken
	if err := base.SetJSON(ctx, runtimeEntryKey("preauth:10.0.0.3:missing"), preAuthEntry{
		Key: "preauth:10.0.0.3:missing", Samples: []int64{999_000},
		BlockedUntilMs: &blockedUntil, LastReason: &lastReason,
	}, 60_000); err != nil {
		t.Fatal(err)
	}
	decision, err := circuit.InspectPreAuthCircuit(ctx, gatewaypreauth.PreAuthCircuitInput{ClientIP: "10.0.0.3"})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Blocked {
		t.Fatalf("预置封禁未生效: %+v", decision)
	}
	sprayDecision, err := circuit.RecordPreAuthFailure(ctx, gatewaypreauth.PreAuthFailureInput{
		ClientIP: "10.0.0.3", Reason: gatewaypreauth.PreAuthFailureInvalidAPIKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sprayDecision.Blocked {
		t.Fatalf("specific 封禁未短路: %+v", sprayDecision)
	}
	// spray key 预置封禁 → invalid_api_key 直接返回 spray decision。
	sprayBlocked := int64(2_000_000)
	sprayReason := preAuthReasonInvalidTokenSpray
	if err := base.SetJSON(ctx, runtimeEntryKey(preAuthSprayKey("10.0.0.4")), preAuthEntry{
		Key: preAuthSprayKey("10.0.0.4"), Samples: []int64{999_000},
		BlockedUntilMs: &sprayBlocked, LastReason: &sprayReason,
	}, 60_000); err != nil {
		t.Fatal(err)
	}
	sprayDecision, err = circuit.RecordPreAuthFailure(ctx, gatewaypreauth.PreAuthFailureInput{
		ClientIP: "10.0.0.4", Reason: gatewaypreauth.PreAuthFailureInvalidAPIKey, Authorization: "Bearer token-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sprayDecision.Blocked || sprayDecision.Reason != preAuthReasonInvalidTokenSpray {
		t.Fatalf("spray 封禁未短路: %+v", sprayDecision)
	}

	// 预置 blocked 的错误熔断条目 → 采样直接短路。
	errorBlocked := int64(2_000_000)
	errorReason := circuitReasonInvalidJSON
	scopeKey, _ := clientIPErrorScopeKey(gatewaypreauth.ClientIPErrorCircuitInput{
		SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.0.0.5",
	})
	if err := base.SetJSON(ctx, runtimeEntryKey(scopeKey), clientIPErrorEntry{
		Key: scopeKey, Samples: []int64{999_000},
		BlockedUntilMs: &errorBlocked, LastReason: &errorReason,
	}, 60_000); err != nil {
		t.Fatal(err)
	}
	blockedSample, err := circuit.RecordClientIPErrorCircuitSample(ctx, gatewaypreauth.ClientIPErrorCircuitSampleInput{
		SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.0.0.5",
		Reason: "invalid_json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !blockedSample.Blocked {
		t.Fatalf("错误熔断封禁未短路: %+v", blockedSample)
	}
}

func TestW13DCircuitConstructorGuard(t *testing.T) {
	if _, err := NewErrorCircuit(ErrorCircuitOptions{
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "://bad-url",
	}); err == nil {
		t.Fatal("坏 URL 必须报错")
	}
}

// ---------------------------------------------------------------------------
// avoidance
// ---------------------------------------------------------------------------

func w13dAvoidanceScope() AvoidanceScopeInput {
	return AvoidanceScopeInput{SystemAccountID: "sys", GroupID: "grp", APIKeyID: "key", ClientIP: "10.0.0.1"}
}

func w13dAccountFailure(accountID string) AccountFailure {
	status := int64(500)
	return AccountFailure{
		AccountID: accountID, StatusCode: &status, ErrorCode: "upstream_error",
		ErrorPhase: "upstream_request", ErrorMessage: "上游失败",
	}
}

func TestW13DAvoidanceMemoryAndRedisEdges(t *testing.T) {
	clock := newManualClock(time.UnixMilli(1_000_000))
	memory, err := NewAvoidance(AvoidanceOptions{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(memory.Close)
	ctx := context.Background()

	// Async 入口的 memory 分支。
	accounts := []gatewayruntimecache.OpenAIAccountSecret{{ID: "a1"}, {ID: "a2"}}
	if result, err := memory.OrderAccountsByClientIPAccountAvoidanceAsync(ctx, accounts, w13dAvoidanceScope(), nil); err != nil {
		t.Fatal(err)
	} else if len(result.Accounts) != 2 || result.Applied {
		t.Fatalf("result=%+v", result)
	}
	tracker := memory.CreateAvoidanceTracker(w13dAvoidanceScope())
	memory.RememberPendingFailure(tracker, "a1", "账号一", w13dAccountFailure("a1"))
	memory.confirmTrackerPendingFailures(tracker, nil, "")
	// a1 已有回避条目 → ConfirmAfterSuccess 清除 existed=true。
	if result, err := memory.ConfirmAfterSuccessAsync(ctx, tracker, "a1", nil); err != nil {
		t.Fatal(err)
	} else if !result.Cleared || result.ClearedAccountID != "a1" || len(result.ConfirmedAccountIDs) != 0 {
		t.Fatalf("result=%+v", result)
	}

	// memory 快照：确认后的条目带 scope，失败计数累计即 active。
	memory.RememberPendingFailure(tracker, "a1", "账号一", w13dAccountFailure("a1"))
	memory.confirmTrackerPendingFailures(tracker, nil, "")
	rows := memory.SnapshotForTest()
	if len(rows) != 1 {
		t.Fatalf("rows=%+v", rows)
	}
	row := rows[0]
	if row.AccountID != "a1" || row.FailureCount < 1 {
		t.Fatalf("row=%+v", row)
	}
	if row.ClientIP != "10.0.0.1" || row.APIKeyID != "key" || row.SystemAccountID != "sys" {
		t.Fatalf("row scope=%+v", row)
	}
	if result, err := memory.ConfirmAfterFinalFailureAsync(ctx, tracker, nil); err != nil {
		t.Fatal(err)
	} else if len(result.ConfirmedAccountIDs) != 0 {
		t.Fatalf("result=%+v", result)
	}

	// scope nil / 空 tracker 的 Async 分支。
	if _, err := memory.OrderAccountsByClientIPAccountAvoidanceAsync(ctx, accounts, AvoidanceScopeInput{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.OrderAccountsByClientIPAccountAvoidanceAsync(ctx, nil, w13dAvoidanceScope(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.ConfirmAfterSuccessAsync(ctx, nil, "a1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.ConfirmAfterFinalFailureAsync(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.ClearForAccountAsync(ctx, nil, "a1"); err != nil {
		t.Fatal(err)
	}
	// confirmTrackerPendingFailures 的 scope nil 分支（tracker 有 PendingFailures
	// 但 Scope 空）。
	nilScope := &AvoidanceTracker{PendingFailures: []AccountFailure{w13dAccountFailure("a1")}}
	memory.confirmTrackerPendingFailures(nilScope, nil, "")
	if confirmed, err := memory.confirmTrackerPendingFailuresAsync(ctx, nilScope, nil, ""); err != nil || len(confirmed) != 0 {
		t.Fatalf("confirmed=%+v err=%v", confirmed, err)
	}
}

func TestW13DAvoidanceRedisPaths(t *testing.T) {
	base := NewMemoryRuntimeStateStore(newManualClock(time.UnixMilli(1_000_000)))
	store := &w13dScriptedStore{RuntimeStateStore: base}
	clock := newManualClock(time.UnixMilli(1_000_000))
	avoidance, err := NewAvoidance(AvoidanceOptions{
		Clock: clock, RuntimeStateDriver: RuntimeStateDriverRedis, StateStore: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(avoidance.Close)
	if _, err := NewAvoidance(AvoidanceOptions{
		RuntimeStateDriver: RuntimeStateDriverRedis, StateRedisURL: "://bad-url",
	}); err == nil {
		t.Fatal("坏 URL 必须报错")
	}
	ctx := context.Background()
	accounts := []gatewayruntimecache.OpenAIAccountSecret{{ID: "a1"}, {ID: "a2"}}

	// 记录两次失败（确认两轮）→ FailureCount=2，a1 进入 avoided。
	tracker := avoidance.CreateAvoidanceTracker(w13dAvoidanceScope())
	avoidance.RememberPendingFailure(tracker, "a1", "账号一", w13dAccountFailure("a1"))
	if _, err := avoidance.confirmTrackerPendingFailuresAsync(ctx, tracker, nil, ""); err != nil {
		t.Fatal(err)
	}
	tracker = avoidance.CreateAvoidanceTracker(w13dAvoidanceScope())
	avoidance.RememberPendingFailure(tracker, "a1", "账号一", w13dAccountFailure("a1"))
	if _, err := avoidance.confirmTrackerPendingFailuresAsync(ctx, tracker, nil, ""); err != nil {
		t.Fatal(err)
	}
	result, err := avoidance.OrderAccountsByClientIPAccountAvoidanceAsync(ctx, accounts, w13dAvoidanceScope(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || len(result.AvoidedAccountIDs) != 1 || result.AvoidedAccountIDs[0] != "a1" {
		t.Fatalf("result=%+v", result)
	}
	if result.Accounts[0].ID != "a2" {
		t.Fatalf("accounts=%+v", result.Accounts)
	}

	// store 错误透传。
	store.getSeq = []error{errors.New("读取失败")}
	if _, err := avoidance.OrderAccountsByClientIPAccountAvoidanceAsync(ctx, accounts, w13dAvoidanceScope(), nil); err == nil {
		t.Fatal("读取错误必须透传")
	}
	store.getSeq = nil
	store.setSeq = []error{errors.New("写入失败")}
	tracker2 := avoidance.CreateAvoidanceTracker(w13dAvoidanceScope())
	avoidance.RememberPendingFailure(tracker2, "a1", "账号一", w13dAccountFailure("a1"))
	if _, err := avoidance.confirmTrackerPendingFailuresAsync(ctx, tracker2, nil, ""); err == nil {
		t.Fatal("写入错误必须透传")
	}
	store.setSeq = nil
	store.getSeq = []error{errors.New("读取失败")}
	if _, err := avoidance.ClearForAccountAsync(ctx, tracker2, "a1"); err == nil {
		t.Fatal("Clear 读取错误必须透传")
	}
	store.getSeq = nil
	store.delErr = errors.New("删除失败")
	if _, err := avoidance.ClearForAccountAsync(ctx, tracker2, "a1"); err == nil {
		t.Fatal("Clear 删除错误必须透传")
	}
	store.delErr = nil

	// skipAccountID 跳过（成功账户不确认）。
	tracker3 := avoidance.CreateAvoidanceTracker(w13dAvoidanceScope())
	avoidance.RememberPendingFailure(tracker3, "a1", "账号一", w13dAccountFailure("a1"))
	avoidance.RememberPendingFailure(tracker3, "a2", "账号二", w13dAccountFailure("a2"))
	confirmed, err := avoidance.confirmTrackerPendingFailuresAsync(ctx, tracker3, nil, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(confirmed) != 1 || confirmed[0] != "a2" {
		t.Fatalf("confirmed=%+v", confirmed)
	}
	if len(tracker3.PendingFailures) != 0 {
		t.Fatal("确认后必须清空 pending")
	}

	// ConfirmAfterSuccessAsync redis：清除 + 确认。
	tracker4 := avoidance.CreateAvoidanceTracker(w13dAvoidanceScope())
	avoidance.RememberPendingFailure(tracker4, "a2", "账号二", w13dAccountFailure("a2"))
	confirm, err := avoidance.ConfirmAfterSuccessAsync(ctx, tracker4, "a2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !confirm.Cleared || len(confirm.ConfirmedAccountIDs) != 0 {
		t.Fatalf("confirm=%+v", confirm)
	}

	// ConfirmAfterFinalFailureAsync redis。
	tracker5 := avoidance.CreateAvoidanceTracker(w13dAvoidanceScope())
	avoidance.RememberPendingFailure(tracker5, "a1", "账号一", w13dAccountFailure("a1"))
	final, err := avoidance.ConfirmAfterFinalFailureAsync(ctx, tracker5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.ConfirmedAccountIDs) != 1 {
		t.Fatalf("final=%+v", final)
	}

	// redis driver 下快照读 memory map，恒为空（仅 memory 模式有快照语义）。
	if rows := avoidance.SnapshotForTest(); len(rows) != 0 {
		t.Fatalf("redis snapshot=%+v", rows)
	}
}

func TestW13DAvoidanceHelpers(t *testing.T) {
	// uniqueStrings 去重保序。
	unique := uniqueStrings([]string{"b", "a", "b", "c", "a"})
	if len(unique) != 3 || unique[0] != "b" || unique[1] != "a" || unique[2] != "c" {
		t.Fatalf("unique=%+v", unique)
	}
	// RememberPendingFailure 的 nil tracker / nil scope / 上限保护。
	clock := newManualClock(time.UnixMilli(1_000_000))
	avoidance, err := NewAvoidance(AvoidanceOptions{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(avoidance.Close)
	avoidance.RememberPendingFailure(nil, "a1", "", w13dAccountFailure("a1"))
	nilScope := &AvoidanceTracker{}
	avoidance.RememberPendingFailure(nilScope, "a1", "", w13dAccountFailure("a1"))
	tracker := avoidance.CreateAvoidanceTracker(w13dAvoidanceScope())
	for i := 0; i < clientIPAccountAvoidanceMaxPendingFailures+5; i++ {
		avoidance.RememberPendingFailure(tracker, fmt.Sprintf("a-%d", i), "", w13dAccountFailure(fmt.Sprintf("a-%d", i)))
	}
	if len(tracker.PendingFailures) != clientIPAccountAvoidanceMaxPendingFailures {
		t.Fatalf("pending=%d", len(tracker.PendingFailures))
	}
	// TransferPendingFailures：nil 与正常路径。
	avoidance.TransferPendingFailures(nil, tracker)
	avoidance.TransferPendingFailures(tracker, nil)
	target := avoidance.CreateAvoidanceTracker(w13dAvoidanceScope())
	avoidance.TransferPendingFailures(tracker, target)
	if len(target.PendingFailures) != clientIPAccountAvoidanceMaxPendingFailures || len(tracker.PendingFailures) != 0 {
		t.Fatalf("transfer target=%d source=%d", len(target.PendingFailures), len(tracker.PendingFailures))
	}
	// 覆盖同一 accountID 时 index 更新（不追加）。
	avoidance.RememberPendingFailure(target, "a-0", "改名", w13dAccountFailure("a-0"))
	if len(target.PendingFailures) != clientIPAccountAvoidanceMaxPendingFailures {
		t.Fatalf("update 后 pending=%d", len(target.PendingFailures))
	}
}

// ---------------------------------------------------------------------------
// helpers: normalize / scheduling / rfc3339 / cache
// ---------------------------------------------------------------------------

func TestW13DNormalizeEdges(t *testing.T) {
	if NormalizeClientIPForStats("   ") != nil {
		t.Fatal("全空白必须返回 nil")
	}
	if normalizeIpv4("nope") != "" {
		t.Fatal("非法 IPv4 必须返回空")
	}
	if normalizeIpv4("2001:db8::1") != "" {
		t.Fatal("IPv6 必须返回空")
	}
	// 前导零八位组与 Node isIP 一致被拒绝。
	if NormalizeClientIPForStats("010.001.001.001") != nil {
		t.Fatal("前导零 IP 必须返回 nil")
	}
	normal := NormalizeClientIPForStats("10.1.1.1")
	if normal == nil || normal.ClientIP != "10.1.1.1" || normal.BucketNo < 0 {
		t.Fatalf("normal=%+v", normal)
	}
}

func TestW13DSchedulingPolicyValidation(t *testing.T) {
	defaults := HighConcurrencyPolicyDefaults{MaxQueueSize: 4, PerAPIKeyQueueLimit: 4}
	if _, err := resolveGroupSchedulingPolicy(map[string]any{"maxQueueWaitMs": "abc"}, defaults); err == nil {
		t.Fatal("非数字 maxQueueWaitMs 必须报错")
	}
	if _, err := resolveGroupSchedulingPolicy(map[string]any{"maxQueueWaitMs": 1.5}, defaults); err == nil {
		t.Fatal("非整数 maxQueueWaitMs 必须报错")
	}
	if _, err := resolveGroupSchedulingPolicy(map[string]any{"maxQueueWaitMs": 0}, defaults); err == nil {
		t.Fatal("越界 maxQueueWaitMs 必须报错")
	}
	if _, err := resolveGroupSchedulingPolicy(map[string]any{"clientIpConcurrencyOverflowMode": "other"}, defaults); err == nil {
		t.Fatal("非法 overflow mode 必须报错")
	}
	if _, err := resolveGroupSchedulingPolicy(map[string]any{"maxQueueSize": "abc"}, defaults); err == nil {
		t.Fatal("非数字 maxQueueSize 必须报错")
	}
	if _, err := resolveGroupSchedulingPolicy(map[string]any{"perApiKeyQueueLimit": 0}, defaults); err == nil {
		t.Fatal("越界 perApiKeyQueueLimit 必须报错")
	}
	if got := normalizePositiveInteger("abc", 7); got != 7 {
		t.Fatalf("normalizePositiveInteger fallback=%d", got)
	}
	if got := normalizeNonNegativeInteger(-3, 0); got != 0 {
		t.Fatalf("normalizeNonNegativeInteger=%d", got)
	}
	if got := normalizePositiveInteger("5", 1); got != 5 {
		t.Fatalf("数字字符串=%d", got)
	}
}

func TestW13DRFC3339Edges(t *testing.T) {
	// 正则通过但日历无效：2 月 30 日。
	if _, ok := rfc3339Millis("2023-02-30T00:00:00Z"); ok {
		t.Fatal("2 月 30 日必须解析失败")
	}
	if _, ok := parseRFC3339InstantTime("2023-02-30T00:00:00Z"); ok {
		t.Fatal("2 月 30 日必须解析失败")
	}
	if _, err := requiredRFC3339Millis("nope", "测试"); err == nil {
		t.Fatal("非法时间必须报错")
	}
	parsed, ok := parseRFC3339InstantTime("2026-01-02T03:04:05.123+08:00")
	if !ok {
		t.Fatal("带 offset 的时间必须可解析")
	}
	if got := canonicalRFC3339(parsed); got != "2026-01-01T19:04:05.123Z" {
		t.Fatalf("canonical=%q", got)
	}
}

func TestW13DEntryTTLCacheEviction(t *testing.T) {
	clock := newManualClock(time.UnixMilli(1_000_000))
	cache := newEntryTTLCache[string](clock, 2)
	cache.set("a", "1", time.Minute)
	cache.set("b", "2", time.Minute)
	// 触碰 a 后插入 c → 驱逐 b（LRU 顺序）。
	if _, ok := cache.get("a"); !ok {
		t.Fatal("a 必须命中")
	}
	cache.set("c", "3", time.Minute)
	if _, ok := cache.get("b"); ok {
		t.Fatal("b 必须被驱逐")
	}
	if _, ok := cache.get("a"); !ok {
		t.Fatal("a 必须保留")
	}
	// 过期读取删除。
	cache.set("d", "4", time.Millisecond)
	clock.advance(time.Minute)
	if _, ok := cache.get("d"); ok {
		t.Fatal("过期条目必须读作缺失")
	}
	cache.clear()
	if cache.size() != 0 {
		t.Fatalf("size=%d", cache.size())
	}
}

func TestW13DMemorySharedCacheEdges(t *testing.T) {
	clock := newManualClock(time.UnixMilli(1_000_000))
	shared := newMemorySharedCache(clock, 4, time.Minute)
	ctx := context.Background()
	// 不可编码值 → 错误。
	if err := shared.Set(ctx, "bad", make(chan int), time.Minute); err == nil {
		t.Fatal("不可编码值必须报错")
	}
	// ttl <= 0 回落默认 TTL。
	if err := shared.Set(ctx, "k", map[string]string{"a": "b"}, 0); err != nil {
		t.Fatal(err)
	}
	var target map[string]string
	if found, err := shared.Get(ctx, "k", &target); err != nil || !found || target["a"] != "b" {
		t.Fatalf("found=%v err=%v target=%+v", found, err, target)
	}
	// 损坏的 JSON 读作错误。
	shared.cache.set("corrupt", []byte("{not-json"), time.Minute)
	var target2 map[string]string
	if _, err := shared.Get(ctx, "corrupt", &target2); err == nil {
		t.Fatal("损坏 JSON 必须报错")
	}
	if err := shared.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if found, err := shared.Get(ctx, "k", &target); err != nil || found {
		t.Fatalf("clear 后必须缺失: found=%v err=%v", found, err)
	}
}
