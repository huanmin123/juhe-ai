package gatewayproxyhealth

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// whRedisForTest 启动独立 miniredis 并挂接 go-redis 客户端，
// 用于驱动 Redis runtime-state / penalty window Lua 路径。
func whRedisForTest(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, client
}

func whRaw(value string) json.RawMessage { return json.RawMessage(value) }

// RedisRuntimeStateStore 是与 Node 共享状态空间的驱动：键前缀、JSON 载荷、
// PX TTL 与 Lua CAS 必须逐字节对齐，这里用真实 miniredis 验证语义。
func TestWhRedisRuntimeStateStoreAgainstMiniredis(t *testing.T) {
	server, client := whRedisForTest(t)
	store, err := NewRedisRuntimeStateStore(client, "wh-ns", "wh-state")
	if err != nil {
		t.Fatal(err)
	}
	ctx := contextBackground()

	if _, err := NewRedisRuntimeStateStore(nil, "ns", "name"); err == nil {
		t.Fatal("nil client 必须报错")
	}
	if _, err := NewRedisRuntimeStateStore(client, "  ", "name"); err == nil {
		t.Fatal("空 namespace 必须报错")
	}

	// GetJSON 缺失 → nil,nil。
	raw, err := store.GetJSON(ctx, "missing")
	if err != nil || raw != nil {
		t.Fatalf("缺失键 = %v err=%v", raw, err)
	}
	// SetJSON + GetJSON 往返。
	type whPayload struct {
		Value int `json:"value"`
	}
	if err := store.SetJSON(ctx, "k1", whPayload{Value: 7}, 60_000); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetJSON(ctx, "k1")
	if err != nil || got == nil || !strings.Contains(string(got), `"value":7`) {
		t.Fatalf("往返载荷 = %s err=%v", got, err)
	}
	// 非法 JSON 读取即清理（Node catch 同款）。
	if err := client.Set(ctx, store.key("k-bad"), "{nope", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	raw, err = store.GetJSON(ctx, "k-bad")
	if err != nil || raw != nil {
		t.Fatalf("非法 JSON 必须按缺失处理: %v err=%v", raw, err)
	}
	if server.Exists(store.key("k-bad")) {
		t.Fatal("非法 JSON 必须被删除")
	}

	// CompareSetJSON：expected=nil 仅在键缺失时生效。
	applied, err := store.CompareSetJSON(ctx, "k-cas", nil, whPayload{Value: 1}, 60_000)
	if err != nil || !applied {
		t.Fatalf("缺失键 CAS = %v err=%v", applied, err)
	}
	applied, err = store.CompareSetJSON(ctx, "k-cas", nil, whPayload{Value: 2}, 60_000)
	if err != nil || applied {
		t.Fatalf("已存在键的 absent CAS 必须失败: %v err=%v", applied, err)
	}
	current, _ := store.GetJSON(ctx, "k-cas")
	applied, err = store.CompareSetJSON(ctx, "k-cas", current, whPayload{Value: 3}, 60_000)
	if err != nil || !applied {
		t.Fatalf("按当前字节 CAS = %v err=%v", applied, err)
	}
	applied, err = store.CompareSetJSON(ctx, "k-cas", whRaw(`{"value":999}`), whPayload{Value: 4}, 60_000)
	if err != nil || applied {
		t.Fatalf("错误期望值 CAS 必须失败: %v err=%v", applied, err)
	}

	// CompareDeleteJSON。
	applied, err = store.CompareDeleteJSON(ctx, "k-cas", whRaw(`{"value":3}`))
	if err != nil || !applied {
		t.Fatalf("按值删除 = %v err=%v", applied, err)
	}
	applied, err = store.CompareDeleteJSON(ctx, "k-cas", whRaw(`{"value":3}`))
	if err != nil || applied {
		t.Fatalf("重复删除必须失败: %v err=%v", applied, err)
	}

	// 锁三件套。
	acquired, err := store.AcquireLock(ctx, "lock1", 60_000, "token-a")
	if err != nil || !acquired {
		t.Fatalf("加锁 = %v err=%v", acquired, err)
	}
	if acquired, _ := store.AcquireLock(ctx, "lock1", 60_000, "token-b"); acquired {
		t.Fatal("互斥加锁必须失败")
	}
	if renewed, err := store.RenewLock(ctx, "lock1", 60_000, "token-wrong"); err != nil || renewed {
		t.Fatalf("错 token 续约 = %v err=%v", renewed, err)
	}
	if renewed, err := store.RenewLock(ctx, "lock1", 60_000, "token-a"); err != nil || !renewed {
		t.Fatalf("对 token 续约 = %v err=%v", renewed, err)
	}
	if err := store.ReleaseLock(ctx, "lock1", "token-wrong"); err != nil {
		t.Fatalf("错 token 释放不应报错: %v", err)
	}
	if err := store.ReleaseLock(ctx, "lock1", "token-a"); err != nil {
		t.Fatal(err)
	}
	if acquired, _ := store.AcquireLock(ctx, "lock1", 60_000, "token-b"); !acquired {
		t.Fatal("释放后必须可重新加锁")
	}

	// GetJSONMany：缺失留空、非法值清理。
	if err := store.SetJSON(ctx, "m1", whPayload{Value: 1}, 60_000); err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, store.key("m2"), "{bad", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	many, err := store.GetJSONMany(ctx, []string{"m1", "m2", "m3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(many) != 3 || many[0] == nil || many[1] != nil || many[2] != nil {
		t.Fatalf("MGET 结果 = %v", many)
	}
	if server.Exists(store.key("m2")) {
		t.Fatal("MGET 必须清理非法载荷")
	}
	if empty, err := store.GetJSONMany(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("空 MGET = %v err=%v", empty, err)
	}
	if err := store.Delete(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	if server.Exists(store.key("m1")) {
		t.Fatal("Delete 必须移除键")
	}
}

// PenaltyWindow 的 Redis 驱动消费路径：Lua 脚本在 miniredis 上执行，
// 覆盖放行、定窗拦截与分组拦截语义。
func TestWhPenaltyWindowRedisConsume(t *testing.T) {
	_, client := whRedisForTest(t)
	clock := newFakeClock(1_000_000)
	limiter := NewPenaltyWindowRateLimiter(clock.Now, true, client, "juhe-ai:wh")
	store := NewPenaltyWindowRateLimitStore(clock.Now, PenaltyWindowStoreOptions{
		Name: "wh-redis-store", PenaltyMode: PenaltyModeFixedWindow, RedisDriver: true,
	})
	ctx := contextBackground()
	rules := []PenaltyWindowRateLimitRule{{WindowSeconds: 60, MaxRequests: 2}}

	// 缺失 client 的 limiter：消费必须报状态 URL 错误。
	brokenLimiter := NewPenaltyWindowRateLimiter(clock.Now, true, nil, "juhe-ai:wh")
	if _, err := brokenLimiter.ConsumeAsync(ctx, store, "scope-a", rules, nil); err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_STATE_URL") {
		t.Fatalf("缺 Redis client 必须报错, err=%v", err)
	}

	// 前两次放行，第三次在窗口内被拦截且 RetryAfter 指向窗口结束。
	for i := 0; i < 2; i++ {
		decision, err := limiter.ConsumeAsync(ctx, store, "scope-a", rules, nil)
		if err != nil || !decision.Allowed {
			t.Fatalf("第 %d 次消费 = %+v err=%v", i, decision, err)
		}
	}
	blocked, err := limiter.ConsumeAsync(ctx, store, "scope-a", rules, nil)
	if err != nil || blocked.Allowed || blocked.Rule == nil || blocked.Limit == nil || *blocked.Limit != 2 {
		t.Fatalf("窗口内拦截 = %+v err=%v", blocked, err)
	}
	if blocked.RetryAfterSeconds == nil || *blocked.RetryAfterSeconds < 1 || *blocked.RetryAfterSeconds > 60 {
		t.Fatalf("RetryAfter 越界: %v", blocked.RetryAfterSeconds)
	}
	if blocked.StoreName != "wh-redis-store" {
		t.Fatalf("StoreName = %q", blocked.StoreName)
	}

	// 空 rules 的 Redis 路径直接放行。
	if decision, err := limiter.ConsumeAsync(ctx, store, "scope-b", nil, nil); err != nil || !decision.Allowed {
		t.Fatalf("空规则 = %+v err=%v", decision, err)
	}
	// 无效规则被过滤后等价空规则。
	invalid := []PenaltyWindowRateLimitRule{{WindowSeconds: 0, MaxRequests: 5}}
	if decision, err := limiter.ConsumeAsync(ctx, store, "scope-b", invalid, nil); err != nil || !decision.Allowed {
		t.Fatalf("无效规则 = %+v err=%v", decision, err)
	}
	// nowMs 覆盖：直接指定未来时间跨窗放行。
	future := clock.NowMs() + 61_000
	if decision, err := limiter.ConsumeAsync(ctx, store, "scope-a", rules, &future); err != nil || !decision.Allowed {
		t.Fatalf("跨窗口放行 = %+v err=%v", decision, err)
	}
}

func TestWhPenaltyWindowRedisGroupsConsume(t *testing.T) {
	_, client := whRedisForTest(t)
	clock := newFakeClock(1_000_000)
	limiter := NewPenaltyWindowRateLimiter(clock.Now, true, client, "juhe-ai:wh")
	apiKeyStore := NewPenaltyWindowRateLimitStore(clock.Now, PenaltyWindowStoreOptions{
		Name: "wh-group-key", PenaltyMode: PenaltyModeFixedWindow, RedisDriver: true,
	})
	ipStore := NewPenaltyWindowRateLimitStore(clock.Now, PenaltyWindowStoreOptions{
		Name: "wh-group-ip", PenaltyMode: PenaltyModeFixedWindow, RedisDriver: true,
	})
	ctx := contextBackground()
	groups := []PenaltyWindowGroup{
		{Scope: "api_key", Store: apiKeyStore, ScopeKey: "key-1", Rules: []PenaltyWindowRateLimitRule{{WindowSeconds: 60, MaxRequests: 1}}},
		{Scope: "api_key_ip", Store: ipStore, ScopeKey: "key-1:ip:1.2.3.4", Rules: []PenaltyWindowRateLimitRule{{WindowSeconds: 60, MaxRequests: 5}}},
	}

	// 第一次放行（api_key 组 1/1），第二次 api_key 组拦截且 Scope 指明组。
	if decision, err := limiter.ConsumeGroupsAsync(ctx, groups, nil); err != nil || !decision.Allowed {
		t.Fatalf("首次分组消费 = %+v err=%v", decision, err)
	}
	blocked, err := limiter.ConsumeGroupsAsync(ctx, groups, nil)
	if err != nil || blocked.Allowed || blocked.Scope != "api_key" {
		t.Fatalf("分组拦截 = %+v err=%v", blocked, err)
	}
	if blocked.Rule == nil || blocked.Rule.MaxRequests != 1 {
		t.Fatalf("分组拦截规则 = %+v", blocked.Rule)
	}
	// 空组直接放行。
	if decision, err := limiter.ConsumeGroupsAsync(ctx, nil, nil); err != nil || !decision.Allowed {
		t.Fatalf("空分组 = %+v err=%v", decision, err)
	}
}

// memory 驱动下的清理/裁剪/固定窗惩罚语义（内部 entry 结构可观察）。
func TestWhPenaltyWindowMemoryLifecycle(t *testing.T) {
	clock := newFakeClock(1_000_000)
	store := NewPenaltyWindowRateLimitStore(clock.Now, PenaltyWindowStoreOptions{
		Name: "wh-mem", MaxEntries: 3, MaxIdleMs: 1_000, PenaltyMode: PenaltyModeFixedWindow,
	})
	rule := PenaltyWindowRateLimitRule{WindowSeconds: 60, MaxRequests: 1}

	// 固定窗：窗口内拦截，RetryAfter 到窗口边界；跨窗后重置。
	first, err := store.ConsumeMemory("k1", []PenaltyWindowRateLimitRule{rule}, nil)
	if err != nil || !first.Allowed {
		t.Fatalf("首次消费 = %+v err=%v", first, err)
	}
	blocked, err := store.ConsumeMemory("k1", []PenaltyWindowRateLimitRule{rule}, nil)
	if err != nil || blocked.Allowed || blocked.RetryAfterSeconds == nil || *blocked.RetryAfterSeconds > 60 {
		t.Fatalf("固定窗拦截 = %+v err=%v", blocked, err)
	}
	// 固定窗惩罚不翻倍：跨窗后立即放行。
	clock.Advance(61_000)
	again, err := store.ConsumeMemory("k1", []PenaltyWindowRateLimitRule{rule}, nil)
	if err != nil || !again.Allowed {
		t.Fatalf("跨窗放行 = %+v err=%v", again, err)
	}

	// 指数惩罚：blocked 期间再消费翻倍并封顶 maxPenalty。
	expStore := NewPenaltyWindowRateLimitStore(clock.Now, PenaltyWindowStoreOptions{
		Name: "wh-exp", MaxPenaltyMs: 1_500, PenaltyMode: PenaltyModeExponential,
	})
	expRule := PenaltyWindowRateLimitRule{WindowSeconds: 1, MaxRequests: 1}
	if _, err := expStore.ConsumeMemory("x", []PenaltyWindowRateLimitRule{expRule}, nil); err != nil {
		t.Fatal(err)
	}
	// 第一次拦截：惩罚 = 窗口 1000ms。
	if decision, err := expStore.ConsumeMemory("x", []PenaltyWindowRateLimitRule{expRule}, nil); err != nil || decision.Allowed {
		t.Fatalf("首次拦截 = %+v err=%v", decision, err)
	}
	// 惩罚期内再消费：翻倍到 2000 但被 maxPenalty 1500 封顶。
	if decision, err := expStore.ConsumeMemory("x", []PenaltyWindowRateLimitRule{expRule}, nil); err != nil || decision.Allowed {
		t.Fatalf("惩罚期拦截 = %+v err=%v", decision, err)
	}
	entry := expStore.entries["x:1:1"]
	if entry == nil || entry.penaltyMs != 1_500 {
		t.Fatalf("惩罚封顶 = %+v", entry)
	}

	// maxIdle 清理：空闲超时的 entry 在下次消费时被清除。
	clock.Advance(2_000)
	if _, err := store.ConsumeMemory("k2", []PenaltyWindowRateLimitRule{rule}, nil); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	_, k1Alive := store.entries["k1:60000:1"]
	store.mu.Unlock()
	if k1Alive {
		t.Fatal("空闲超限的 entry 必须被清理")
	}

	// 容量裁剪：超过 maxEntries 时淘汰到目标规模。
	for i := 0; i < 5; i++ {
		if _, err := store.ConsumeMemory("trim-"+itoaForTest(int64(i)), []PenaltyWindowRateLimitRule{rule}, nil); err != nil {
			t.Fatal(err)
		}
	}
	store.mu.Lock()
	size := len(store.entries)
	store.mu.Unlock()
	if size > 3 {
		t.Fatalf("裁剪后 size = %d, want <= 3", size)
	}

	// Clear 复位全部状态。
	store.Clear()
	store.mu.Lock()
	size = len(store.entries)
	store.mu.Unlock()
	if size != 0 {
		t.Fatalf("Clear 后 size = %d", size)
	}

	// redisDriver 标记禁止本机消费（Node assert 同款文案）。
	redisFlagged := NewPenaltyWindowRateLimitStore(clock.Now, PenaltyWindowStoreOptions{Name: "r", RedisDriver: true})
	if _, err := redisFlagged.ConsumeMemory("k", nil, nil); err == nil || !strings.Contains(err.Error(), "高性能模式禁止使用本机 penalty window") {
		t.Fatalf("redisDriver 本机消费必须报错, err=%v", err)
	}
	// Redis 键布局：短 namespace 会被补上 juhe-ai: 根前缀，后接两个 b64 摘要与规则维度。
	key := redisPenaltyWindowRateLimitKey("wh", "store", "scope", rule)
	parts := strings.Split(key, ":")
	if len(parts) != 8 || parts[0] != "juhe-ai" || parts[2] != "rate-limit" || parts[3] != "penalty" {
		t.Fatalf("Redis 键布局 = %q", key)
	}
	if len(parts[4]) != 43 || len(parts[5]) != 43 {
		t.Fatalf("b64 摘要长度异常: %q", key)
	}
}

// 记录面的补充入口：别名/回退分支与证据比较纯函数。
func TestWhProxyHealthRecordExtras(t *testing.T) {
	ctx := contextBackground()
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryProxyHealth(clock)
	a := accountFixture{id: "a", systemAccountID: "sys", providerCode: "p", proxyURL: stringPtrValue("http://proxy:9"), baseURL: "https://x.example.com"}.build()

	// RecordGatewayProxyFailure 默认收敛到 proxy scope：桶键只有 proxy:*。
	decision := service.RecordGatewayProxyFailure(a, "代理失败", FailureRecordOptions{})
	if !decision.Recorded || decision.ProxyKey == nil || len(decision.BucketKeys) != 1 || !strings.HasPrefix(decision.BucketKeys[0], "proxy:url:") {
		t.Fatalf("proxy scope 记录 = %+v", decision)
	}
	// Async 内存回退与 proxy success 清理。
	if _, err := service.RecordGatewayProxyFailureAsync(ctx, a, "代理失败", FailureRecordOptions{}); err != nil {
		t.Fatal(err)
	}
	cleared, err := service.RecordGatewayProxySuccessAsync(ctx, a)
	if err != nil || !cleared {
		t.Fatalf("proxy success = %v err=%v", cleared, err)
	}
	if cleared, err := service.RecordGatewayUpstreamBucketSuccessAsync(ctx, a, FailureRecordOptions{}); err != nil || cleared {
		t.Fatalf("空状态 success = %v err=%v", cleared, err)
	}
	// 异步抑制（Redis 驱动路径落到内存状态存储）与 0 秒 TTL 归一化。
	if _, err := service.SuppressGatewayUpstreamBucketForSecondsAsync(ctx, a, 0, "短路避让", FailureRecordOptions{}); err != nil {
		t.Fatal(err)
	}
	if decision := service.SuppressGatewayUpstreamBucketLocallyForSeconds(a, 0, "短路避让", FailureRecordOptions{}); !decision.Suspected {
		t.Fatalf("0 秒抑制 = %+v", decision)
	}
	// 异步排序的内存回退别名。
	if result, err := service.OrderOpenAIAccountsByGatewayProxyHealthAsync(ctx, []gatewayruntimecache.OpenAIAccountSecret{a}, nil); err != nil || len(result.Accounts) != 1 {
		t.Fatalf("异步别名排序 = %+v err=%v", result, err)
	}
	if result := service.OrderOpenAIAccountsByGatewayProxyHealth([]gatewayruntimecache.OpenAIAccountSecret{a}, nil); len(result.Accounts) != 1 {
		t.Fatalf("同步别名排序 = %+v", result)
	}
	service.ClearForTest()

	// 证据比较纯函数：失败证据全字段相等才可清理。
	left := upstreamBucketFailureEntry{Key: "k", Reason: "r", FailureCount: 1, FirstFailedAtMs: 1, LastFailedAtMs: 2}
	right := left
	if !sameGatewayUpstreamBucketFailureEvidence(left, right) {
		t.Fatal("相同证据必须相等")
	}
	right.FailureCount = 2
	if sameGatewayUpstreamBucketFailureEvidence(left, right) {
		t.Fatal("失败计数不同必须不相等")
	}
	if !avoidUntilEqual(nil, nil) || avoidUntilEqual(int64Ptr(1), nil) || avoidUntilEqual(nil, int64Ptr(1)) || !avoidUntilEqual(int64Ptr(1), int64Ptr(1)) {
		t.Fatal("avoidUntilEqual 四分支语义错误")
	}
	// 观察比较：后代写入代次高于观察 → 视为观察后失败（保守保留）。
	observation := upstreamBucketMutationObservation{observedAtMs: 100, generation: &upstreamBucketMutationGeneration{InstanceID: "i", Sequence: 1}}
	stale := upstreamBucketFailureEntry{LastFailedAtMs: 90}
	fresh := upstreamBucketFailureEntry{LastFailedAtMs: 101}
	sameWithOlderGen := upstreamBucketFailureEntry{LastFailedAtMs: 100, LastFailureGeneration: &upstreamBucketMutationGeneration{InstanceID: "i", Sequence: 0}}
	if !gatewayUpstreamBucketFailureOccurredAfterObservation(fresh, observation) {
		t.Fatal("更晚失败必须视为观察后发生")
	}
	if gatewayUpstreamBucketFailureOccurredAfterObservation(stale, observation) {
		t.Fatal("更早失败不得视为观察后发生")
	}
	if !gatewayUpstreamBucketFailureOccurredAfterObservation(sameWithOlderGen, observation) {
		t.Fatal("同刻更旧代次必须视为观察后发生（保守保留）")
	}
	// 最新观察选择：当前更新、无当前、同刻代次三分支。
	current := &upstreamBucketFailureEntry{LastFailedAtMs: 200, LastFailureGeneration: &upstreamBucketMutationGeneration{InstanceID: "i", Sequence: 5}}
	if got := latestGatewayUpstreamBucketFailureObservation(current, upstreamBucketMutationObservation{observedAtMs: 100}); got.observedAtMs != 200 {
		t.Fatalf("当前更新必须保留当前观察: %+v", got)
	}
	if got := latestGatewayUpstreamBucketFailureObservation(nil, upstreamBucketMutationObservation{observedAtMs: 100}); got.observedAtMs != 100 {
		t.Fatalf("无当前必须采用来者: %+v", got)
	}
	incoming := upstreamBucketMutationObservation{observedAtMs: 200, generation: &upstreamBucketMutationGeneration{InstanceID: "i", Sequence: 3}}
	if got := latestGatewayUpstreamBucketFailureObservation(current, incoming); got.generation.Sequence != 5 {
		t.Fatalf("同刻代次更高必须保留当前: %+v", got)
	}
	incomingNewer := upstreamBucketMutationObservation{observedAtMs: 200, generation: &upstreamBucketMutationGeneration{InstanceID: "i", Sequence: 6}}
	if got := latestGatewayUpstreamBucketFailureObservation(current, incomingNewer); got.generation.Sequence != 6 {
		t.Fatalf("同刻来者代次更高必须采用来者: %+v", got)
	}
}

// 上游桶条目 TTL 契约：至少存活到避让/半开截止再加一个失败窗口。
func TestWhRedisBucketEntryTTL(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryProxyHealth(clock)
	base := int64(10_000)
	// 无任何截止时间：取最小 TTL。
	if got := service.redisBucketFailureEntryTTLMs(&upstreamBucketFailureEntry{}, 1_000_000, base); got != base {
		t.Fatalf("基础 TTL = %d", got)
	}
	// 避让截止远超基础 TTL：TTL = 剩余 + 失败窗口。
	entry := upstreamBucketFailureEntry{AvoidUntilMs: int64Ptr(1_000_000 + 500_000)}
	if got := service.redisBucketFailureEntryTTLMs(&entry, 1_000_000, base); got != 500_000+60_000 {
		t.Fatalf("避让 TTL = %d", got)
	}
	// 半开截止同样参与取最大。
	entry.HalfOpenUntilMs = int64Ptr(1_000_000 + 900_000)
	if got := service.redisBucketFailureEntryTTLMs(&entry, 1_000_000, base); got != 900_000+60_000 {
		t.Fatalf("半开 TTL = %d", got)
	}
}

// bucketEntryMap 的删除语义与原始字节透传。
func TestWhBucketEntryMapAndRaw(t *testing.T) {
	entries := newBucketEntryMap()
	if _, ok := entries.Load("missing"); ok {
		t.Fatal("空 map 不得命中")
	}
	entries.Delete("missing")
	entries.Store("k", &loadedBucketEntry{raw: whRaw(`{"key":"k"}`), entry: upstreamBucketFailureEntry{Key: "k"}})
	entries.Store("k", &loadedBucketEntry{raw: whRaw(`{"key":"k"}`), entry: upstreamBucketFailureEntry{Key: "k"}})
	if len(entries.keys) != 1 {
		t.Fatalf("重复 Store 不得重复登记: %v", entries.keys)
	}
	entries.Delete("k")
	if len(entries.keys) != 0 {
		t.Fatalf("Delete 后必须移除: %v", entries.keys)
	}
	entries.Delete("k")
	raw, err := rawOrMarshal(nil, map[string]string{"a": "b"})
	if err != nil || string(raw) != `{"a":"b"}` {
		t.Fatalf("rawOrMarshal 缺省编码 = %s err=%v", raw, err)
	}
	pass := whRaw(`already`)
	if got, err := rawOrMarshal(pass, nil); err != nil || string(got) != "already" {
		t.Fatalf("rawOrMarshal 透传 = %s err=%v", got, err)
	}
	if upstreamBucketType("proxy:url:x") != "proxy" || upstreamBucketType("nocolon") != "unknown" {
		t.Fatal("桶类型推导语义错误")
	}
	if bucketKeyForLog("proxy:url: ") != "proxy:url:[configured]" || bucketKeyForLog("provider:p") != "provider:p" {
		t.Fatal("日志脱敏语义错误")
	}
	if got := bucketKeysForLog([]string{"proxy:url: ", "provider:p"}); len(got) != 2 || got[0] != "proxy:url:[configured]" {
		t.Fatalf("批量日志脱敏 = %v", got)
	}
	// 账户样本 ["<id>", <ms>] 二元组互操作格式。
	sample := AccountSample{AccountID: "a", FailedAtMs: 5}
	encoded, err := json.Marshal(sample)
	if err != nil || string(encoded) != `["a",5]` {
		t.Fatalf("样本编码 = %s err=%v", encoded, err)
	}
	var decoded AccountSample
	if err := json.Unmarshal([]byte(`["b",7]`), &decoded); err != nil || decoded.AccountID != "b" || decoded.FailedAtMs != 7 {
		t.Fatalf("样本解码 = %+v err=%v", decoded, err)
	}
	if err := json.Unmarshal([]byte(`["b"]`), &decoded); err == nil {
		t.Fatal("非二元组样本必须解码失败")
	}
}

// whVanishingStore 在首次 bucket CAS 失败后立即删除底层条目，
// 模拟并发成功清理：半开探测必须放弃并从本轮 ordering 中移除该条目。
type whVanishingStore struct {
	*MemoryRuntimeStateStore
	vanish bool
}

func (s *whVanishingStore) CompareSetJSON(ctx context.Context, key string, expected json.RawMessage, next any, ttlMs int64) (bool, error) {
	applied, err := s.MemoryRuntimeStateStore.CompareSetJSON(ctx, key, expected, next, ttlMs)
	if s.vanish && err == nil && !applied && strings.HasPrefix(key, "bucket:") {
		_ = s.MemoryRuntimeStateStore.Delete(ctx, key)
	}
	return applied, err
}

// 半开探测的 CAS 竞争恢复：CAS 失败且条目被并发删除后放弃探测并移除条目。
func TestWhAsyncOrderDeletesVanishedBucketEntry(t *testing.T) {
	clock := newFakeClock(1_000_000)
	store := &whVanishingStore{MemoryRuntimeStateStore: NewMemoryRuntimeStateStore(clock.Now)}
	service := NewProxyHealthService(clock.Now, store, ProxyHealthOptions{CASMaxAttempts: 3}, nil)
	a := accountFixture{id: "a", systemAccountID: "sys", providerCode: "p", baseURL: "https://x.example.com"}.build()
	b := accountFixture{id: "b", systemAccountID: "sys", providerCode: "p", baseURL: "https://x.example.com"}.build()
	if _, err := service.RecordGatewayUpstreamBucketFailureAsync(contextBackground(), a, "err", FailureRecordOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordGatewayUpstreamBucketFailureAsync(contextBackground(), b, "err", FailureRecordOptions{}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(61_000)
	store.vanish = true
	result, err := service.OrderGatewayAccountsByUpstreamBucketHealthAsync(contextBackground(), []gatewayruntimecache.OpenAIAccountSecret{a}, nil)
	if err != nil {
		t.Fatalf("异步排序 = %+v err=%v", result, err)
	}
	if result.Applied || len(result.HalfOpenAccountIDs) != 0 {
		t.Fatalf("条目消失后不得半开: %+v", result)
	}
}

// 全部桶被避让且无新桶可轮换：结果必须原样透传并标记 bypassed。
func TestWhOrderBypassedWhenAllAvoided(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryProxyHealth(clock)
	shared := accountFixture{systemAccountID: "sys", providerCode: "p", baseURL: "https://x.example.com"}
	a := shared.build()
	a.ID = "a"
	b := shared.build()
	b.ID = "b"
	service.RecordGatewayUpstreamBucketFailure(a, "err", FailureRecordOptions{})
	service.RecordGatewayUpstreamBucketFailure(b, "err", FailureRecordOptions{})
	// 两个账号同 baseUrl + 同 provider：specific 与 provider scope 都全员避让。
	result := service.OrderGatewayAccountsByUpstreamBucketHealth([]gatewayruntimecache.OpenAIAccountSecret{a, b}, nil)
	if result.Applied || !result.BypassedAllAvoided {
		t.Fatalf("全员避让必须透传 bypassed: %+v", result)
	}
	if len(result.AvoidedAccountIDs) != 2 || len(result.AvoidedBucketKeys) == 0 || len(result.AvoidedProxyKeys) != 0 {
		t.Fatalf("bypassed 汇总 = %+v", result)
	}
}
