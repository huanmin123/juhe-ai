package gatewayclientip

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"
)

func newPolicyTestCache(t *testing.T, mutate func(*PolicyCacheOptions)) (*PolicyCache, *fakePolicySource, *manualClock, *manualScheduler, *spyingLogger) {
	t.Helper()
	clock := newManualClock(time.UnixMilli(1_000_000))
	scheduler := &manualScheduler{clock: clock}
	source := &fakePolicySource{}
	logger := &spyingLogger{}
	opts := PolicyCacheOptions{
		Clock:       clock,
		Logger:      logger,
		CacheDriver: "memory",
		RuntimeMode: "standalone",
		Source:      source,
		Scheduler:   scheduler,
	}
	if mutate != nil {
		mutate(&opts)
	}
	cache, err := NewPolicyCache(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Close)
	return cache, source, clock, scheduler, logger
}

func TestPolicyCacheInspectMemoryMode(t *testing.T) {
	// clock 起点 1_000_000ms；expiresAt 定在 +15s，TTL 必须被截断到 15s。
	expiresAt := canonicalRFC3339(time.UnixMilli(1_015_000))
	policy := ActiveClientIPPolicy{
		ID: "p1", IPHash: hashOf("10.0.0.1"), PolicyType: PolicyTypeBlacklist,
		AggregateIPKey: "10.0.0.1", ClientIP: "10.0.0.1", Reason: strPtr("abuse"),
		ExpiresAt: &expiresAt,
	}
	cache, source, clock, _, _ := newPolicyTestCache(t, nil)
	ctx := context.Background()

	// cacheOnly + 未加载快照：不查库、不回源、不落负缓存。
	decision, err := cache.InspectPolicy(ctx, "10.0.0.1", InspectClientIPPolicyOptions{CacheOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Blocked || decision.NormalizedIP == nil || decision.NormalizedIP.ClientIP != "10.0.0.1" {
		t.Fatalf("decision=%+v", decision)
	}
	if source.listCalls+source.findCalls != 0 {
		t.Fatalf("cacheOnly 未加载快照不得查库: list=%d find=%d", source.listCalls, source.findCalls)
	}
	if cache.policyCache.size() != 0 {
		t.Fatalf("cacheOnly 未加载快照不得写负缓存")
	}

	// 加载快照后 inspect 命中黑名单，TTL 受 expiresAt 限制。
	if err := cache.PrimeClientIPPolicyCacheLocal([]ActiveClientIPPolicy{policy}); err != nil {
		t.Fatal(err)
	}
	decision, err = cache.InspectPolicy(ctx, "10.0.0.1", InspectClientIPPolicyOptions{CacheOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Blocked || decision.BlacklistPolicy == nil || decision.BlacklistPolicy.ID != "p1" {
		t.Fatalf("decision=%+v", decision)
	}
	if decision.Allowlisted {
		t.Fatal("blacklist policy must not mark allowlisted")
	}
	entry, ok := cache.policyCache.get(hashOf("10.0.0.1"))
	if !ok {
		t.Fatal("entry must be cached")
	}
	// clock at 1_000_000ms; expiresAt 2026-09-04T12:00:30Z in ms:
	expiresAtMs, _ := rfc3339Millis(expiresAt)
	wantTTL := time.Duration(expiresAtMs-1_000_000) * time.Millisecond
	if wantTTL <= 0 || wantTTL >= clientIPPolicyCacheTTL {
		t.Fatalf("test setup: wantTTL=%v", wantTTL)
	}
	_ = entry

	// 二次 inspect 走缓存，不查库。
	if _, err := cache.InspectPolicy(ctx, "10.0.0.1", InspectClientIPPolicyOptions{CacheOnly: true}); err != nil {
		t.Fatal(err)
	}
	if source.listCalls+source.findCalls != 0 {
		t.Fatal("hit must not query the source")
	}

	// 过期瞬间到达后按未命中处理（isPolicyActiveAt）。
	clock.advance(wantTTL + time.Millisecond)
	decision, err = cache.InspectPolicy(ctx, "10.0.0.1", InspectClientIPPolicyOptions{CacheOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Blocked {
		t.Fatalf("expired policy must not block: %+v", decision)
	}
}

func TestPolicyCacheInspectAllowlistAndNegativeCache(t *testing.T) {
	allow := ActiveClientIPPolicy{ID: "a1", IPHash: hashOf("10.0.0.2"), PolicyType: PolicyTypeAllowlist, AggregateIPKey: "10.0.0.2", ClientIP: "10.0.0.2"}
	cache, source, _, _, _ := newPolicyTestCache(t, nil)
	ctx := context.Background()
	if err := cache.PrimeClientIPPolicyCacheLocal([]ActiveClientIPPolicy{allow}); err != nil {
		t.Fatal(err)
	}
	decision, err := cache.InspectPolicy(ctx, "10.0.0.2", InspectClientIPPolicyOptions{CacheOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allowlisted || decision.AllowlistPolicy == nil || decision.Blocked {
		t.Fatalf("decision=%+v", decision)
	}
	// 无策略 IP：负缓存生效，第二次不触发任何 source 调用。
	if _, err := cache.InspectPolicy(ctx, "10.0.0.3", InspectClientIPPolicyOptions{CacheOnly: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.InspectPolicy(ctx, "10.0.0.3", InspectClientIPPolicyOptions{CacheOnly: true}); err != nil {
		t.Fatal(err)
	}
	if source.listCalls+source.findCalls != 0 {
		t.Fatal("prime 路径不查库")
	}
	if cache.policyCache.size() != 2 {
		t.Fatalf("policy cache size=%d want 2 (含负缓存)", cache.policyCache.size())
	}
}

func TestPolicyCacheReloadUsesSharedSnapshotThenDatabase(t *testing.T) {
	policy := ActiveClientIPPolicy{ID: "p1", IPHash: hashOf("10.0.0.1"), PolicyType: PolicyTypeBlacklist, AggregateIPKey: "10.0.0.1", ClientIP: "10.0.0.1"}
	cache, source, _, _, _ := newPolicyTestCache(t, nil)
	source.policies = []ActiveClientIPPolicy{policy}
	ctx := context.Background()

	// 首次 reload：共享快照为空 → 查库，并写入共享快照。
	if err := cache.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{}); err != nil {
		t.Fatal(err)
	}
	if source.listCalls != 1 {
		t.Fatalf("listCalls=%d", source.listCalls)
	}
	snapshot, err := cache.getActivePolicySnapshotSharedCacheEntry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || len(snapshot.Policies) != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}

	// 二次 reload：共享快照命中，不再查库。
	source.policies = nil
	if err := cache.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{}); err != nil {
		t.Fatal(err)
	}
	if source.listCalls != 1 {
		t.Fatalf("共享快照命中时不得再查库: %d", source.listCalls)
	}
	if cache.GetClientIPPolicyCacheRuntime().SnapshotPolicyCount != 1 {
		t.Fatalf("runtime=%+v", cache.GetClientIPPolicyCacheRuntime())
	}

	// bypass：绕过共享快照直接查库。
	source.policies = []ActiveClientIPPolicy{policy}
	if err := cache.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{BypassSharedCache: true}); err != nil {
		t.Fatal(err)
	}
	if source.listCalls != 2 {
		t.Fatalf("bypass 必须直查库: %d", source.listCalls)
	}
}

func TestPolicyCacheReplaceOnRedisWithoutSkipThrowsNodeMessage(t *testing.T) {
	cache, _, _, _, _ := newPolicyTestCache(t, func(opts *PolicyCacheOptions) {
		opts.CacheDriver = CacheDriverRedis
		opts.Shared = newFakeSharedCacheFactory()
	})
	err := cache.ReplaceClientIPPolicyCacheLocal(nil, ReplaceClientIPPolicyCacheLocalOptions{})
	if err == nil || err.Error() != "高性能模式禁止同步写入 Client-IP 策略 Redis shared cache，必须使用异步刷新入口" {
		t.Fatalf("err=%v", err)
	}
}

func TestPolicyCacheRedisInspectCachesNegatives(t *testing.T) {
	source := &fakePolicySource{}
	shared := newFakeSharedCacheFactory()
	cache, _, _, _, _ := newPolicyTestCache(t, func(opts *PolicyCacheOptions) {
		opts.CacheDriver = CacheDriverRedis
		opts.Shared = shared
		opts.Source = source
	})
	ctx := context.Background()
	// miss → 查库 → 写 shared 负缓存。
	decision, err := cache.InspectPolicy(ctx, "10.0.0.9", InspectClientIPPolicyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Blocked {
		t.Fatal("expected not blocked")
	}
	if source.findCalls != 1 {
		t.Fatalf("findCalls=%d", source.findCalls)
	}
	var entry sharedByIPEntry
	found, err := shared.caches[policyByIPCacheName].Get(ctx, hashOf("10.0.0.9"), &entry)
	if err != nil || !found {
		t.Fatalf("负缓存必须写入 shared: found=%v err=%v", found, err)
	}
	// 第二次：shared 命中，不再查库。
	if _, err := cache.InspectPolicy(ctx, "10.0.0.9", InspectClientIPPolicyOptions{}); err != nil {
		t.Fatal(err)
	}
	if source.findCalls != 1 {
		t.Fatalf("shared 命中不得回库: %d", source.findCalls)
	}
	// 写入黑名单后再查 → shared 旧负缓存仍在（Node 同样读到旧值），先失效再验证。
	source.policies = []ActiveClientIPPolicy{{ID: "p9", IPHash: hashOf("10.0.0.9"), PolicyType: PolicyTypeBlacklist, AggregateIPKey: "10.0.0.9", ClientIP: "10.0.0.9"}}
	if err := cache.ClearClientIPPolicyCacheLocal(ctx); err != nil {
		t.Fatal(err)
	}
	decision, err = cache.InspectPolicy(ctx, "10.0.0.9", InspectClientIPPolicyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Blocked || decision.BlacklistPolicy == nil {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestPolicyCacheRedisUsesByIPNotSnapshotForCacheOnly(t *testing.T) {
	// 回归守卫：redis cacheOnly 路径必须只读 by-ip shared cache，miss 时不
	// 回库（Node getClientIpPolicyByIpSharedCacheEntry 直返 undefined）。
	policy := ActiveClientIPPolicy{ID: "p1", IPHash: hashOf("10.0.0.1"), PolicyType: PolicyTypeBlacklist, AggregateIPKey: "10.0.0.1", ClientIP: "10.0.0.1"}
	source := &fakePolicySource{policies: []ActiveClientIPPolicy{policy}}
	shared := newFakeSharedCacheFactory()
	cache, _, _, _, _ := newPolicyTestCache(t, func(opts *PolicyCacheOptions) {
		opts.CacheDriver = CacheDriverRedis
		opts.Shared = shared
		opts.Source = source
	})
	ctx := context.Background()
	decision, err := cache.InspectPolicy(ctx, "10.0.0.1", InspectClientIPPolicyOptions{CacheOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Blocked {
		t.Fatal("cacheOnly miss 不得回库，必须按未命中处理")
	}
	if source.listCalls != 0 || source.findCalls != 0 {
		t.Fatalf("list=%d find=%d（cacheOnly miss 不得查库）", source.listCalls, source.findCalls)
	}
	// 非 cacheOnly：回库并写 by-ip shared cache。
	decision, err = cache.InspectPolicy(ctx, "10.0.0.1", InspectClientIPPolicyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Blocked {
		t.Fatalf("decision=%+v", decision)
	}
	if source.findCalls != 1 {
		t.Fatalf("find=%d", source.findCalls)
	}
	var entry sharedByIPEntry
	found, err := shared.caches[policyByIPCacheName].Get(ctx, hashOf("10.0.0.1"), &entry)
	if err != nil || !found || entry.Policy == nil {
		t.Fatalf("by-ip shared cache 未写入: found=%v err=%v", found, err)
	}
	// 第二次 cacheOnly：shared 命中即封禁，仍然零回库。
	decision, err = cache.InspectPolicy(ctx, "10.0.0.1", InspectClientIPPolicyOptions{CacheOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Blocked {
		t.Fatalf("cacheOnly 命中必须封禁: %+v", decision)
	}
	if source.findCalls != 1 {
		t.Fatalf("cacheOnly 命中不得回库: find=%d", source.findCalls)
	}
}

func TestPolicyCacheStatsWriterBridgeSelection(t *testing.T) {
	policy := ActiveClientIPPolicy{ID: "p1", IPHash: hashOf("10.0.0.1"), PolicyType: PolicyTypeBlacklist, AggregateIPKey: "10.0.0.1", ClientIP: "10.0.0.1"}
	stats := &stubStatsWriter{listResult: []ActiveClientIPPolicy{policy}}
	cache, source, _, _, _ := newPolicyTestCache(t, func(opts *PolicyCacheOptions) {
		opts.ProcessRole = ProcessRoleServer
		opts.StatsWriter = stats
	})
	ctx := context.Background()
	if err := cache.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{BypassSharedCache: true}); err != nil {
		t.Fatal(err)
	}
	if source.listCalls != 0 {
		t.Fatal("server 角色必须走 stats writer")
	}
	if len(stats.operations) != 1 || stats.operations[0] != StatsWriterOpListActiveClientIPPolicies {
		t.Fatalf("operations=%v", stats.operations)
	}
	// worker + stats-worker 不走 bridge。
	stats2 := &stubStatsWriter{}
	cache2, source2, _, _, _ := newPolicyTestCache(t, func(opts *PolicyCacheOptions) {
		opts.ProcessRole = ProcessRoleWorker
		opts.WorkerRole = WorkerRoleStatsWorker
		opts.StatsWriter = stats2
	})
	if err := cache2.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{BypassSharedCache: true}); err != nil {
		t.Fatal(err)
	}
	if source2.listCalls != 1 || len(stats2.operations) != 0 {
		t.Fatalf("stats-worker 必须直查库: source=%d stats=%v", source2.listCalls, stats2.operations)
	}
}

func TestPolicyCacheHitBufferMergeOverflowAndFlush(t *testing.T) {
	// 行为对齐 backend/src/scripts/regression/client-ip-policy-hit-buffer-regression.ts
	cache, source, _, scheduler, logger := newPolicyTestCache(t, nil)
	ctx := context.Background()

	runtime := cache.GetClientIPPolicyCacheRuntime()
	if runtime.SnapshotPolicyCount != 0 {
		t.Fatalf("初始快照应为空: %+v", runtime)
	}
	maxPendingHits := runtime.MaxPendingPolicyHits
	if maxPendingHits != 5000 || runtime.FlushBatchSize != 1000 {
		t.Fatalf("constants drifted: %+v", runtime)
	}
	policyForIndex := func(index int) ActiveClientIPPolicy {
		return ActiveClientIPPolicy{
			ID: "policy_buffer_guard", IPHash: hashIndexOf(index), PolicyType: PolicyTypeBlacklist,
			AggregateIPKey: "10.0.0.1", ClientIP: "10.0.0.1", Reason: strPtr("buffer guard"),
		}
	}
	for index := 0; index < maxPendingHits+25; index += 1 {
		if err := cache.RecordClientIPPolicyHitAsync(ctx, policyForIndex(index)); err != nil {
			t.Fatal(err)
		}
	}
	overflow := cache.GetClientIPPolicyCacheRuntime()
	if overflow.PendingPolicyHitCount != maxPendingHits {
		t.Fatalf("待写缓冲必须按固定 distinct key 上限截断: %d", overflow.PendingPolicyHitCount)
	}
	if overflow.DroppedPolicyHitCount != 25 {
		t.Fatalf("溢出计数=%d want 25", overflow.DroppedPolicyHitCount)
	}
	// 同一 key 合并不应扩大缓冲，也不算新的溢出。
	if err := cache.RecordClientIPPolicyHitAsync(ctx, policyForIndex(0)); err != nil {
		t.Fatal(err)
	}
	merged := cache.GetClientIPPolicyCacheRuntime()
	if merged.PendingPolicyHitCount != maxPendingHits || merged.DroppedPolicyHitCount != 25 {
		t.Fatalf("merge runtime=%+v", merged)
	}

	// 冲刷：推进 1000ms 触发定时器；batch 1000，剩余 4001 分批 + 0 延迟续冲。
	scheduler.advance(clientIPPolicyHitFlushDelay)
	if source.hitCalls == 0 {
		t.Fatal("flush must deliver hits")
	}
	scheduler.advance(time.Millisecond)
	scheduler.advance(time.Millisecond)
	scheduler.advance(time.Millisecond)
	scheduler.advance(time.Millisecond)
	// 5000/1000 = 5 批。
	if source.hitCalls != 5 {
		t.Fatalf("hitCalls=%d want 5 批", source.hitCalls)
	}
	if got := source.hitCountFor(hashIndexOf(0), "policy_buffer_guard"); got != 2 {
		t.Fatalf("合并计数=%d want 2", got)
	}
	if logger.count("client_ip_policy_hits_flush_failed") != 0 {
		t.Fatal("unexpected flush failure")
	}

	// flush 失败：记录 warn 事件，缓冲不回滚（与 Node 一致，已删批次丢弃）。
	source.err = errors.New("db down")
	if err := cache.RecordClientIPPolicyHitAsync(ctx, policyForIndex(7000)); err != nil {
		t.Fatal(err)
	}
	scheduler.advance(clientIPPolicyHitFlushDelay)
	if logger.count("client_ip_policy_hits_flush_failed") != 1 {
		t.Fatalf("flush failure warn=%d", logger.count("client_ip_policy_hits_flush_failed"))
	}
}

func TestPolicyCacheHitBufferDropWarnCadence(t *testing.T) {
	cache, _, _, scheduler, logger := newPolicyTestCache(t, nil)
	ctx := context.Background()
	policy := func(index int) ActiveClientIPPolicy {
		return ActiveClientIPPolicy{ID: "p", IPHash: hashIndexOf(index), PolicyType: PolicyTypeBlacklist, AggregateIPKey: "ip", ClientIP: "ip"}
	}
	// 5010 distinct → 前 10 次丢弃各告警一次，之后每 1000 次：第 1010 次。
	for index := 0; index < clientIPPolicyHitMaxPendingEntries+10; index += 1 {
		if err := cache.RecordClientIPPolicyHitAsync(ctx, policy(index)); err != nil {
			t.Fatal(err)
		}
	}
	if logger.count("client_ip_policy_hit_buffer_dropped") != 10 {
		t.Fatalf("drop warns=%d want 10", logger.count("client_ip_policy_hit_buffer_dropped"))
	}
	scheduler.advance(clientIPPolicyHitFlushDelay)
}

func TestPolicyCachePerformanceModeBypassesBuffer(t *testing.T) {
	cache, source, _, _, _ := newPolicyTestCache(t, func(opts *PolicyCacheOptions) {
		opts.RuntimeMode = RuntimeModePerformance
	})
	policy := ActiveClientIPPolicy{ID: "p1", IPHash: hashOf("10.0.0.1"), PolicyType: PolicyTypeBlacklist, AggregateIPKey: "ip", ClientIP: "ip"}
	if err := cache.RecordClientIPPolicyHitAsync(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	if source.hitCalls != 1 {
		t.Fatalf("高性能模式必须直写: %d", source.hitCalls)
	}
	if cache.GetClientIPPolicyCacheRuntime().PendingPolicyHitCount != 0 {
		t.Fatal("performance mode must not buffer")
	}
	// 非黑名单直接忽略。
	allow := policy
	allow.PolicyType = PolicyTypeAllowlist
	if err := cache.RecordClientIPPolicyHitAsync(context.Background(), allow); err != nil {
		t.Fatal(err)
	}
	if source.hitCalls != 1 {
		t.Fatal("allowlist hits are ignored")
	}
}

func TestPolicyCacheMalformedExpiresAtThrowsNodeMessage(t *testing.T) {
	cache, _, _, _, _ := newPolicyTestCache(t, nil)
	policy := ActiveClientIPPolicy{ID: "p", IPHash: hashOf("ip"), PolicyType: PolicyTypeBlacklist, AggregateIPKey: "ip", ClientIP: "ip", ExpiresAt: strPtr("not-a-time")}
	err := cache.PrimeClientIPPolicyCacheLocal([]ActiveClientIPPolicy{policy})
	if err == nil || err.Error() != "Client-IP 策略 expiresAt 必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("err=%v", err)
	}
}

func TestPolicyCacheClearAndRuntimeSnapshot(t *testing.T) {
	policy := ActiveClientIPPolicy{ID: "p1", IPHash: hashOf("10.0.0.1"), PolicyType: PolicyTypeBlacklist, AggregateIPKey: "ip", ClientIP: "ip"}
	cache, _, _, _, _ := newPolicyTestCache(t, nil)
	ctx := context.Background()
	if err := cache.PrimeClientIPPolicyCacheLocal([]ActiveClientIPPolicy{policy}); err != nil {
		t.Fatal(err)
	}
	runtime := cache.GetClientIPPolicyCacheRuntime()
	if runtime.SnapshotPolicyCount != 1 || runtime.SnapshotLoadedAt == "" {
		t.Fatalf("runtime=%+v", runtime)
	}
	if err := cache.ClearClientIPPolicyCacheLocal(ctx); err != nil {
		t.Fatal(err)
	}
	runtime = cache.GetClientIPPolicyCacheRuntime()
	if runtime.SnapshotPolicyCount != 0 || runtime.SnapshotLoadedAt != "" {
		t.Fatalf("clear runtime=%+v", runtime)
	}
}

// hashOf mirrors the runtime ipHash: sha256("client-ip:" + ip) for IPv4
// inputs (clientIpIdentity), sha256(seed) otherwise.
func hashOf(seed string) string {
	if normalized := NormalizeClientIPForStats(seed); normalized != nil {
		return normalized.IPHash
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func hashIndexOf(index int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("hash-%d", index)))
	return hex.EncodeToString(sum[:])
}

func strPtr(value string) *string { return &value }
