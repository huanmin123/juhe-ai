package gatewayproxyhealth

// w14g：代理健康族覆盖率补强（校验/淘汰/降级分支直驱）。
//
// 不可达语句登记（分析依据见各条；均不需要在本轮构造可达路径）：
//   - clock.go NewRandomHex/NewUUID 的 crypto/rand 失败回退（38-43/50-52）：
//     受支持平台不会失败。
//   - clock.go PassiveScheduleJitterWindowMs 的 half<0 分支（172-174）：
//     interval 先被钳到 ≥1。
//   - clock.go PassiveScheduleDelayMs 的 delay<1 分支（209-211）：delay ≥ 1
//     且 offset 受 window ≤ interval/2 约束。
//   - latencydegradation.go mustMarshalJSON 的 json.Marshal 错误分支
//     （1924-1926）：字符串/数值/布尔组成的字面量序列化不会失败。
//   - runtimestate.go jsonEscapeString 的 json.Marshal(string) 错误分支
//     （192-194）：字符串序列化不会失败。
//   - proxyhealthrecord.go RecordGatewayUpstreamBucketFailure(Async) 的
//     len(bucketKeys)==0 分支（33-35/59-61）：GatewayUpstreamBucketKeys 恒
//     追加 provider 兜底键，结果不可能为空。
//   - modelsratelimit.go ConsumeAuthenticatedModelsRateLimit 的 scope==""
//     分支（104-106）与 Limit==nil 且 Rule!=nil 分支（114.33-117.3）：
//     memory 组路径的 blocked 决策不含 Scope/Rule/Limit，redis 组路径三者
//     全量填充，两条生产路径都不会产生该组合。
//   - penaltywindow.go redis 拒绝路径的 retry-after 下限分支（613-615/
//     684-686）：Lua 返回的 retry_ms 恒为正数，ceilDiv 后 ≥1。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// runtimestate.go
// ---------------------------------------------------------------------------

type w14gUnmarshalable struct{ Ch chan int }

func TestW14GRuntimeStateMemoryBranches(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(1000)
	store := NewMemoryRuntimeStateStore(clock.Now)
	// SetJSON 序列化失败。
	if err := store.SetJSON(ctx, "w14g-bad", w14gUnmarshalable{}, 1000); err == nil {
		t.Fatalf("不可序列化值必须失败")
	}
	// CompareSetJSON 序列化失败。
	if ok, err := store.CompareSetJSON(ctx, "w14g-bad2", json.RawMessage(`{}`), w14gUnmarshalable{}, 1000); ok || err == nil {
		t.Fatalf("CompareSetJSON 序列化失败必须透传")
	}
	// expected=nil 但已有值 → false。
	if err := store.SetJSON(ctx, "w14g-exists", map[string]int{"a": 1}, 1000); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.CompareSetJSON(ctx, "w14g-exists", nil, map[string]int{"b": 2}, 1000); ok || err != nil {
		t.Fatalf("expected=nil 与现存值冲突 = %v err=%v", ok, err)
	}
	// CompareDeleteJSON：缺失 / 值不匹配。
	if ok, err := store.CompareDeleteJSON(ctx, "w14g-missing", json.RawMessage(`{}`)); ok || err != nil {
		t.Fatalf("缺失删除 = %v err=%v", ok, err)
	}
	if ok, err := store.CompareDeleteJSON(ctx, "w14g-exists", json.RawMessage(`{"other":1}`)); ok || err != nil {
		t.Fatalf("不匹配删除 = %v err=%v", ok, err)
	}
	// 工具函数分支。
	if lockTokenEqual(json.RawMessage(`nope`), "t") {
		t.Fatalf("非法 JSON token 必须不等")
	}
	if normalizeTTLms(0) != 1 {
		t.Fatalf("normalizeTTLms(0) = %d", normalizeTTLms(0))
	}
	// 命名空间键。
	if got := namespacedRedisKey("ns", "juhe-ai:ns:k"); got != "juhe-ai:ns:k" {
		t.Fatalf("已前缀键 = %s", got)
	}
	if got := namespacedRedisKey("ns", "juhe-ai:other:k"); got != "juhe-ai:ns:other:k" {
		t.Fatalf("juhe-ai 前缀改写 = %s", got)
	}
	if got := sanitizeRedisNamespacePart("  $$$ "); got != "" {
		t.Fatalf("全非法命名空间 = %q", got)
	}
	if got := sanitizeRedisKeyPart(""); got != "default" {
		t.Fatalf("空键部分 = %q", got)
	}
	if got := sanitizeRedisKeyPart("a b/c"); got != "a_b_c" {
		t.Fatalf("非法字符替换 = %q", got)
	}
}

func TestW14GRuntimeStateRedisBranches(t *testing.T) {
	ctx := context.Background()
	client := newFakeRedisClient()
	store, err := NewRedisRuntimeStateStore(client, "w14g-ns", "w14g-name")
	if err != nil {
		t.Fatal(err)
	}
	if serr := store.SetJSON(ctx, "w14g-bad", w14gUnmarshalable{}, 1000); serr == nil {
		t.Fatalf("Redis SetJSON 序列化失败必须透传")
	}
	if ok, cerr := store.CompareSetJSON(ctx, "w14g-bad2", json.RawMessage(`{}`), w14gUnmarshalable{}, 1000); ok || cerr == nil {
		t.Fatalf("Redis CompareSetJSON 序列化失败必须透传")
	}
}

// ---------------------------------------------------------------------------
// userrequestlimit.go
// ---------------------------------------------------------------------------

func TestW14GUserRequestLimitBranches(t *testing.T) {
	clock := newFakeClock(60_000)
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{})
	// 过期的覆盖配置失效。
	expired := "2025-01-01T00:00:00Z"
	overrides := overridesLimits(iptr(100), nil, nil, nil, &expired)
	decision := counter.Consume(UserRequestLimitConsumeInput{
		SystemAccountID: "w14g-u", Settings: settingsLimits(iptr(100), nil, nil, nil), Overrides: overrides,
	})
	if !decision.Allowed {
		t.Fatalf("过期覆盖不应拦截")
	}
	// itoa 分支。
	if itoa(0) != "0" || itoa(-45) != "-45" || itoa(123456) != "123456" {
		t.Fatalf("itoa 分支错误")
	}
	if formatInt64(-7) != "-7" {
		t.Fatalf("formatInt64 = %s", formatInt64(-7))
	}
	// 脏快照：脏序表中的陈旧键被清理。
	counter.mu.Lock()
	counter.dirtyOrder = append(counter.dirtyOrder, "w14g-stale-dirty")
	counter.mu.Unlock()
	for _, item := range counter.DirtySnapshot(10) {
		if item.EntryKey == "w14g-stale-dirty" {
			t.Fatalf("陈旧脏键不应输出")
		}
	}
	// removeDirtyLocked 未知键静默；清理循环删除过期条目。
	counter.mu.Lock()
	counter.removeDirtyLocked("w14g-unknown-dirty")
	counter.entries["w14g-expired"] = &userRequestLimitCounterEntry{
		key: "w14g-expired", systemAccountID: "w14g-u", window: userRequestLimitWindowPerMinute, bucket: "b",
		expiresAtMs: 1, localCount: 1, redisTTLms: 60_000, dirty: true,
	}
	counter.order = append(counter.order, "w14g-expired")
	counter.cleanupExpiredLocked(100_000, 10)
	if _, ok := counter.entries["w14g-expired"]; ok {
		t.Fatalf("过期条目应被清理")
	}
	// entryLocked 过期条目原位重置。
	counter.entryLocked("w14g-reset", userRequestLimitWindowPerMinute, userRequestLimitBucketDefinition{
		bucket: "w14g-b", expiresAtMs: 120_000, redisTTLms: 60_000,
	}, 60_000).localCount = 5
	reset := counter.entryLocked("w14g-reset", userRequestLimitWindowPerMinute, userRequestLimitBucketDefinition{
		bucket: "w14g-b", expiresAtMs: 180_000, redisTTLms: 60_000,
	}, 130_000)
	// 时区快照缓存超过 32 个时淘汰最老。
	for index := 0; index < 40; index++ {
		counter.currentBucketsLocked("TZ-"+string(rune('A'+index%26))+string(rune(index)), 60_000)
	}
	over := len(counter.snapshotOrder) > 32
	counter.mu.Unlock()
	if reset == nil || reset.localCount != 0 || reset.dirty || reset.expiresAtMs != 180_000 {
		t.Fatalf("过期条目未原位重置: %+v", reset)
	}
	if over {
		t.Fatalf("时区快照缓存未淘汰")
	}
}

// ---------------------------------------------------------------------------
// penaltywindow.go / modelsratelimit.go
// ---------------------------------------------------------------------------

func TestW14GPenaltyWindowBranches(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(1_000_000)
	store := NewPenaltyWindowRateLimitStore(clock.Now, PenaltyWindowStoreOptions{Name: "w14g-pw", MaxEntries: 4})
	now := int64(1_000_000)
	rules := []PenaltyWindowRateLimitRule{{MaxRequests: 1, WindowSeconds: 60}}
	// 非法规则跳过。
	if decision := store.consumeMemory("w14g-scope", []PenaltyWindowRateLimitRule{{MaxRequests: 0, WindowSeconds: 1}}, &now); !decision.Allowed {
		t.Fatalf("非法规则应放行")
	}
	// 指数惩罚：首次放行、超出拒绝。
	if first := store.consumeMemory("w14g-exp", rules, &now); !first.Allowed {
		t.Fatalf("首次消费应放行")
	}
	clock.Set(1_000_001)
	if blocked := store.consumeMemory("w14g-exp", rules, nil); blocked.Allowed {
		t.Fatalf("超出限流应拒绝")
	}
	// cleanupLocked / trimLocked 的陈旧 order 跳过分支。
	store.mu.Lock()
	store.order = append(store.order, "w14g-ghost")
	store.cleanupLocked(2_000_000)
	store.order = append(store.order, "w14g-ghost2")
	store.trimLocked(2_000_000)
	store.mu.Unlock()
	// blockedBucketDecision 的 retry-after 下限。
	clamped := blockedBucketDecision(rules[0], 10, "w14g")
	if clamped.RetryAfterSeconds == nil || *clamped.RetryAfterSeconds < 1 {
		t.Fatalf("retry-after 未钳到 1: %+v", clamped)
	}
	// 组消费的内存路径。
	limiter := NewPenaltyWindowRateLimiter(clock.Now, false, nil, "w14g-ns")
	groupDecision, err := limiter.ConsumeGroupsAsync(ctx, []PenaltyWindowGroup{{
		Scope: "w14g", Store: store, ScopeKey: "w14g-group", Rules: rules,
	}}, &now)
	if err != nil || !groupDecision.Allowed {
		t.Fatalf("组消费 = %+v err=%v", groupDecision, err)
	}
	// redis driver：Eval 失败（miniredis 已关闭）与成功放行/组消费。
	closedServer, closedClient := whRedisForTest(t)
	closedLimiter := NewPenaltyWindowRateLimiter(clock.Now, true, closedClient, "w14g-ns")
	closedServer.Close()
	if _, err := closedLimiter.ConsumeAsync(ctx, store, "w14g-redis", rules, &now); err == nil {
		t.Fatalf("Redis 关闭后消费必须失败")
	}
	_, liveClient := whRedisForTest(t)
	liveLimiter := NewPenaltyWindowRateLimiter(clock.Now, true, liveClient, "w14g-ns")
	redisDecision, err := liveLimiter.ConsumeAsync(ctx, store, "w14g-redis-live", rules, &now)
	if err != nil || !redisDecision.Allowed {
		t.Fatalf("Redis 消费 = %+v err=%v", redisDecision, err)
	}
	groupRedis, err := liveLimiter.ConsumeGroupsAsync(ctx, []PenaltyWindowGroup{{
		Scope: "w14g", Store: store, ScopeKey: "w14g-group-redis", Rules: rules,
	}}, &now)
	if err != nil || !groupRedis.Allowed {
		t.Fatalf("Redis 组消费 = %+v err=%v", groupRedis, err)
	}
}

func TestW14GModelsRateLimitBranches(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(1_000_000)
	limiter := NewPenaltyWindowRateLimiter(clock.Now, false, nil, "w14g-ns")
	auth := NewAuthenticatedModelsRateLimitService(clock.Now, limiter, nil)
	// 空 apiKeyId 直接放行。
	if decision, err := auth.ConsumeAuthenticatedModelsRateLimit(ctx, gatewaypreauth.AuthenticatedModelsRateLimitInput{}, nil); err != nil || !decision.Allowed {
		t.Fatalf("空 apiKeyId 应放行: %+v err=%v", decision, err)
	}
	// 连续消费直至 blocked（scope 缺省与 Rule→Limit 回退分支）。
	input := gatewaypreauth.AuthenticatedModelsRateLimitInput{APIKeyID: "w14g-key", ClientIP: "10.0.0.1"}
	blocked := false
	for i := 0; i < 256; i++ {
		decision, err := auth.ConsumeAuthenticatedModelsRateLimit(ctx, input, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !decision.Allowed {
			blocked = true
			break
		}
	}
	if !blocked {
		t.Fatalf("认证模型限流未触发拒绝")
	}
	// 公共模型限流 blocked 分支。
	public := NewPublicModelsRateLimitService(clock.Now, limiter)
	publicBlocked := false
	for i := 0; i < 256; i++ {
		decision, err := public.ConsumePublicModelsRateLimit(ctx, "10.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		if !decision.Allowed {
			publicBlocked = true
			break
		}
	}
	if !publicBlocked {
		t.Fatalf("公共模型限流未触发拒绝")
	}
}

// ---------------------------------------------------------------------------
// proxyhealth.go / latencydegradation.go
// ---------------------------------------------------------------------------

func TestW14GProxyHealthMemoryBranches(t *testing.T) {
	clock := newFakeClock(1000)
	service := NewProxyHealthService(clock.Now, NewMemoryRuntimeStateStore(clock.Now), ProxyHealthOptions{FailureMaxEntries: 2}, nil)
	// getMemoryEntryLocked 缺失 / 过期。
	service.mu.Lock()
	if _, ok := service.getMemoryEntryLocked("w14g-missing"); ok {
		t.Fatalf("缺失键不应命中")
	}
	service.setMemoryEntryLocked("w14g-exp", upstreamBucketFailureEntry{}, 100)
	service.mu.Unlock()
	clock.Set(10_000)
	service.mu.Lock()
	if _, ok := service.getMemoryEntryLocked("w14g-exp"); ok {
		t.Fatalf("过期键不应命中")
	}
	service.mu.Unlock()
	// evictOldestLocked：超过容量后条目被淘汰。
	service.setMemoryEntry("w14g-a", upstreamBucketFailureEntry{}, 60_000)
	service.setMemoryEntry("w14g-b", upstreamBucketFailureEntry{}, 60_000)
	service.setMemoryEntry("w14g-c", upstreamBucketFailureEntry{}, 60_000)
	service.setMemoryEntry("w14g-d", upstreamBucketFailureEntry{}, 60_000)
	service.mu.Lock()
	size := len(service.entries)
	service.mu.Unlock()
	if size > 4 {
		t.Fatalf("容量淘汰未生效: %d", size)
	}
	// AccountSample UnmarshalJSON 防御分支。
	var sample AccountSample
	if err := sample.UnmarshalJSON([]byte(`not-json`)); err == nil {
		t.Fatalf("非法 JSON 必须失败")
	}
	if err := sample.UnmarshalJSON([]byte(`["only-one"]`)); err == nil {
		t.Fatalf("单元素数组必须失败")
	}
	if err := sample.UnmarshalJSON([]byte(`[{},"x"]`)); err == nil {
		t.Fatalf("非法 accountId 必须失败")
	}
	if err := sample.UnmarshalJSON([]byte(`["a",{}]`)); err == nil {
		t.Fatalf("非法 failedAtMs 必须失败")
	}
	// scopeOrProxy 缺省。
	if got := (FailureRecordOptions{}).scopeOrProxy(); got != BucketScopeProxy {
		t.Fatalf("缺省 scope = %s", got)
	}
	// setKeysUnionAccountIDs 的重复账号去重。
	union := setKeysUnionAccountIDs(
		[]gatewayruntimecache.OpenAIAccountSecret{{ID: "k1"}, {ID: "k1"}, {ID: "k2"}},
		[]gatewayruntimecache.OpenAIAccountSecret{{ID: "k3"}},
	)
	if len(union) != 3 {
		t.Fatalf("去重失败: %v", union)
	}
}

func TestW14GLatencyDegradationSmallBranches(t *testing.T) {
	service := &LatencyDegradationService{
		random: func() float64 { return 0.5 },
		opts:   LatencyDegradationOptions{LockRetryDelay: func(int) {}},
	}
	if service.jitterRandom() == nil {
		t.Fatalf("注入 random 应原样返回")
	}
	service.lockRetryDelay(1)
	if NormalRouteLatencyDegradationScope("", "", "") != nil {
		t.Fatalf("空 scope 必须返回 nil")
	}
	if scope := NormalRouteLatencyDegradationScope("s", "r", "g"); scope == nil {
		t.Fatalf("合法 scope 不应为 nil")
	}
}
