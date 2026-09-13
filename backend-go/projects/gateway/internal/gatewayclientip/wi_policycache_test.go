package gatewayclientip

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// newWITestPolicyCache 构造 memory 模式 PolicyCache，返回实例与手动时钟/调度器，
// 供 hit buffer 的确定性冲刷断言复用。
func newWITestPolicyCache(t *testing.T, mutate func(*PolicyCacheOptions)) (*PolicyCache, *fakePolicySource, *manualClock, *manualScheduler, *spyingLogger) {
	t.Helper()
	clock := newManualClock(time.UnixMilli(1_000_000))
	scheduler := &manualScheduler{clock: clock}
	source := &fakePolicySource{}
	logger := &spyingLogger{}
	opts := PolicyCacheOptions{
		Clock:       clock,
		Logger:      logger,
		Source:      source,
		Scheduler:   scheduler,
		ProcessRole: ProcessRoleWorker,
		WorkerRole:  WorkerRoleStatsWorker,
	}
	if mutate != nil {
		mutate(&opts)
	}
	cache, err := NewPolicyCache(opts)
	if err != nil {
		t.Fatalf("NewPolicyCache 失败: %v", err)
	}
	t.Cleanup(cache.Close)
	return cache, source, clock, scheduler, logger
}

// wiBlacklistPolicy 构造一个未过期黑名单策略。
func wiBlacklistPolicy(id, ipHash string) ActiveClientIPPolicy {
	reason := "滥用"
	return ActiveClientIPPolicy{
		ID: id, IPHash: ipHash, PolicyType: PolicyTypeBlacklist,
		AggregateIPKey: "10.0.0.1", ClientIP: "10.0.0.1", Reason: &reason,
	}
}

func TestWIPolicyCacheConstructionErrors(t *testing.T) {
	if _, err := NewPolicyCache(PolicyCacheOptions{}); err == nil {
		t.Fatal("缺 PolicySource 必须报错")
	}
	if _, err := NewPolicyCache(PolicyCacheOptions{
		Source:      &fakePolicySource{},
		CacheDriver: CacheDriverRedis,
	}); err == nil {
		t.Fatal("redis driver 缺 shared factory 必须报错")
	}
}

func TestWIPolicyCacheInspectPreAuthProjection(t *testing.T) {
	ipHash := NormalizeClientIPForStats("10.0.0.1").IPHash
	policy := wiBlacklistPolicy("p1", ipHash)

	t.Run("黑名单投影", func(t *testing.T) {
		cache, source, _, _, _ := newWITestPolicyCache(t, nil)
		if err := cache.PrimeClientIPPolicyCacheLocal([]ActiveClientIPPolicy{policy}); err != nil {
			t.Fatalf("预置快照失败: %v", err)
		}
		decision, err := cache.InspectClientIPPolicy(context.Background(), "10.0.0.1", false)
		if err != nil {
			t.Fatalf("InspectClientIPPolicy 失败: %v", err)
		}
		if !decision.Blocked || decision.Allowlisted {
			t.Fatalf("decision=%+v", decision)
		}
		if decision.BlacklistPolicy == nil || decision.BlacklistPolicy.ID != "p1" || decision.BlacklistPolicy.Reason != "滥用" {
			t.Fatalf("blacklist 投影错误: %+v", decision.BlacklistPolicy)
		}
		if decision.NormalizedIP == nil || decision.NormalizedIP.AggregateIPKey != "10.0.0.1" {
			t.Fatalf("normalizedIP 错误: %+v", decision.NormalizedIP)
		}
		if source.findCalls != 0 {
			t.Fatal("memory 模式检查不得触发 source.find")
		}
	})

	t.Run("非黑即白的 allowlist 与无策略", func(t *testing.T) {
		cache, _, _, _, _ := newWITestPolicyCache(t, nil)
		allow := ActiveClientIPPolicy{ID: "p2", IPHash: ipHash, PolicyType: PolicyTypeAllowlist}
		if err := cache.PrimeClientIPPolicyCacheLocal([]ActiveClientIPPolicy{allow}); err != nil {
			t.Fatalf("预置快照失败: %v", err)
		}
		decision, err := cache.InspectClientIPPolicy(context.Background(), "10.0.0.1", false)
		if err != nil || !decision.Allowlisted || decision.Blocked {
			t.Fatalf("allowlist decision=%+v err=%v", decision, err)
		}
		decision, err = cache.InspectClientIPPolicy(context.Background(), "192.168.1.1", false)
		if err != nil || decision.Blocked || decision.Allowlisted {
			t.Fatalf("无策略 decision=%+v err=%v", decision, err)
		}
	})

	t.Run("cacheOnly 未加载时只返回归一化 IP", func(t *testing.T) {
		cache, source, _, _, _ := newWITestPolicyCache(t, nil)
		decision, err := cache.InspectClientIPPolicy(context.Background(), "10.0.0.1", true)
		if err != nil {
			t.Fatalf("InspectClientIPPolicy 失败: %v", err)
		}
		if decision.NormalizedIP == nil || decision.NormalizedIP.ClientIP != "10.0.0.1" {
			t.Fatalf("normalizedIP 错误: %+v", decision.NormalizedIP)
		}
		if decision.Blocked || source.listCalls != 0 {
			t.Fatalf("cacheOnly 不得加载快照: calls=%d", source.listCalls)
		}
	})

	t.Run("EnsureSnapshotLoaded 从 source 加载", func(t *testing.T) {
		cache, source, _, _, _ := newWITestPolicyCache(t, nil)
		source.policies = []ActiveClientIPPolicy{policy}
		decision, err := cache.InspectPolicy(context.Background(), "10.0.0.1", InspectClientIPPolicyOptions{EnsureSnapshotLoaded: true})
		if err != nil {
			t.Fatalf("InspectPolicy 失败: %v", err)
		}
		if !decision.Blocked || source.listCalls != 1 {
			t.Fatalf("decision=%+v listCalls=%d", decision, source.listCalls)
		}
		// 第二次走 per-IP 缓存，不再触发加载。
		if _, err := cache.InspectPolicy(context.Background(), "10.0.0.1", InspectClientIPPolicyOptions{EnsureSnapshotLoaded: true}); err != nil {
			t.Fatalf("第二次 InspectPolicy 失败: %v", err)
		}
		if source.listCalls != 1 {
			t.Fatalf("命中 per-IP 缓存后 listCalls=%d", source.listCalls)
		}
	})

	t.Run("非法 IP 直接返回空决策", func(t *testing.T) {
		cache, _, _, _, _ := newWITestPolicyCache(t, nil)
		decision, err := cache.InspectPolicy(context.Background(), "not-an-ip", InspectClientIPPolicyOptions{})
		if err != nil || decision.NormalizedIP != nil {
			t.Fatalf("decision=%+v err=%v", decision, err)
		}
	})
}

func TestWIPolicyCacheInspectRedisDriver(t *testing.T) {
	factory := newFakeSharedCacheFactory()
	cache, source, clock, _, _ := newWITestPolicyCache(t, func(opts *PolicyCacheOptions) {
		opts.CacheDriver = CacheDriverRedis
		opts.Shared = factory
	})
	ipHash := NormalizeClientIPForStats("10.0.0.1").IPHash
	policy := wiBlacklistPolicy("p1", ipHash)
	source.policies = []ActiveClientIPPolicy{policy}
	ctx := context.Background()

	// Redis 模式：cacheOnly 只读共享缓存（未命中且无 DB 回源）。
	decision, err := cache.InspectPolicy(ctx, "10.0.0.1", InspectClientIPPolicyOptions{CacheOnly: true})
	if err != nil {
		t.Fatalf("redis cacheOnly 失败: %v", err)
	}
	if decision.Blocked || source.findCalls != 0 {
		t.Fatalf("cacheOnly 不得回源: %+v findCalls=%d", decision, source.findCalls)
	}

	// 非 cacheOnly 从 DB 加载并写入共享缓存。
	decision, err = cache.InspectPolicy(ctx, "10.0.0.1", InspectClientIPPolicyOptions{})
	if err != nil || !decision.Blocked {
		t.Fatalf("redis 加载失败: %+v err=%v", decision, err)
	}
	if source.findCalls != 1 {
		t.Fatalf("findCalls=%d want 1", source.findCalls)
	}
	// 第二次直接命中共享缓存。
	if _, err := cache.InspectPolicy(ctx, "10.0.0.1", InspectClientIPPolicyOptions{}); err != nil {
		t.Fatalf("第二次检查失败: %v", err)
	}
	if source.findCalls != 1 {
		t.Fatalf("共享缓存命中后 findCalls=%d", source.findCalls)
	}
	// redis 模式的 runtime 快照计数恒 0。
	runtimeInfo := cache.GetClientIPPolicyCacheRuntime()
	if runtimeInfo.SnapshotPolicyCount != 0 {
		t.Fatalf("redis 模式 snapshot count 必须 0: %+v", runtimeInfo)
	}
	if runtimeInfo.MaxPendingPolicyHits != clientIPPolicyHitMaxPendingEntries || runtimeInfo.FlushBatchSize != clientIPPolicyHitFlushBatchSize {
		t.Fatalf("runtime 常量错误: %+v", runtimeInfo)
	}
	_ = clock
}

func TestWIPolicyCacheTTLBoundaries(t *testing.T) {
	cache, _, clock, _, _ := newWITestPolicyCache(t, nil)
	base := time.UnixMilli(1_000_000)

	// 无 expiresAt → 固定 30s。
	ttl, err := cache.clientIPPolicyTTL(nil)
	if err != nil || ttl != clientIPPolicyCacheTTL {
		t.Fatalf("nil policy ttl=%v err=%v", ttl, err)
	}
	noExpiry := wiBlacklistPolicy("p", "x")
	ttl, err = cache.clientIPPolicyTTL(&noExpiry)
	if err != nil || ttl != clientIPPolicyCacheTTL {
		t.Fatalf("无 expiresAt ttl=%v err=%v", ttl, err)
	}
	// 即将过期 → 剩余时间。
	soon := base.Add(5 * time.Second).UTC().Format(time.RFC3339)
	expiring := wiBlacklistPolicy("p", "x")
	expiring.ExpiresAt = &soon
	ttl, err = cache.clientIPPolicyTTL(&expiring)
	if err != nil || ttl != 5*time.Second {
		t.Fatalf("临期 ttl=%v err=%v", ttl, err)
	}
	// 已过期 → 下限 1ms。
	clock.advance(10 * time.Second)
	ttl, err = cache.clientIPPolicyTTL(&expiring)
	if err != nil || ttl != time.Millisecond {
		t.Fatalf("已过期 ttl=%v err=%v", ttl, err)
	}
	// 非 RFC3339 → 报错。
	bad := "not-a-time"
	expiring.ExpiresAt = &bad
	if _, err := cache.clientIPPolicyTTL(&expiring); err == nil {
		t.Fatal("坏 expiresAt 必须报错")
	}
}

func TestWIPolicyCacheHitBufferFlushAndDrop(t *testing.T) {
	t.Run("缓冲冲刷按插入序写 source", func(t *testing.T) {
		cache, source, _, scheduler, _ := newWITestPolicyCache(t, nil)
		ipHash := NormalizeClientIPForStats("10.0.0.1").IPHash
		policy := wiBlacklistPolicy("p1", ipHash)
		// 同一 key 命中 3 次 → 合并为一个计数 3 的缓冲项。
		for i := 0; i < 3; i++ {
			if err := cache.RecordClientIPPolicyHitAsync(context.Background(), policy); err != nil {
				t.Fatalf("记录命中失败: %v", err)
			}
		}
		runtimeInfo := cache.GetClientIPPolicyCacheRuntime()
		if runtimeInfo.PendingPolicyHitCount != 1 {
			t.Fatalf("pending=%d want 1", runtimeInfo.PendingPolicyHitCount)
		}
		if source.hitCalls != 0 {
			t.Fatal("冲刷前不得写 source")
		}
		scheduler.advance(clientIPPolicyHitFlushDelay)
		if source.hitCalls != 1 {
			t.Fatalf("冲刷后 hitCalls=%d", source.hitCalls)
		}
		if got := source.hitCountFor(ipHash, "p1"); got != 3 {
			t.Fatalf("累计命中=%d want 3", got)
		}
	})

	t.Run("非 blacklist 类型直接忽略", func(t *testing.T) {
		cache, source, _, _, _ := newWITestPolicyCache(t, nil)
		if err := cache.RecordClientIPPolicyHitAsync(context.Background(), ActiveClientIPPolicy{PolicyType: PolicyTypeAllowlist}); err != nil {
			t.Fatalf("allowlist 记录必须无错忽略: %v", err)
		}
		if source.hitCalls != 0 {
			t.Fatal("allowlist 不得写入")
		}
	})

	t.Run("缓冲写满后丢弃并告警", func(t *testing.T) {
		cache, source, _, scheduler, logger := newWITestPolicyCache(t, nil)
		ctx := context.Background()
		for i := 0; i < clientIPPolicyHitMaxPendingEntries; i++ {
			policy := wiBlacklistPolicy("p", "x")
			// 前 4 位十进制序号保证 64 位 ipHash 互不相同（distinct key 才计入上限）。
			policy.IPHash = padLeft(i) + strings.Repeat("a", 60)
			if err := cache.RecordClientIPPolicyHitAsync(ctx, policy); err != nil {
				t.Fatalf("记录命中失败: %v", err)
			}
		}
		scheduler.advance(clientIPPolicyHitFlushDelay)
		// 5000 条超过单批 1000：冲刷链会连续 5 批写完。
		if source.hitCalls != 5 {
			t.Fatalf("满缓冲冲刷 hitCalls=%d want 5", source.hitCalls)
		}
		if len(source.hits) != clientIPPolicyHitMaxPendingEntries {
			t.Fatalf("冲刷总量=%d", len(source.hits))
		}
		// 缓冲已清空，继续填满并触发溢出丢弃。
		for i := 0; i < clientIPPolicyHitMaxPendingEntries+1; i++ {
			policy := wiBlacklistPolicy("p", "x")
			policy.IPHash = padLeft(i) + strings.Repeat("c", 60)
			_ = cache.RecordClientIPPolicyHitAsync(ctx, policy)
		}
		if got := cache.GetClientIPPolicyCacheRuntime().DroppedPolicyHitCount; got != 1 {
			t.Fatalf("dropped=%d want 1", got)
		}
		if logger.count("client_ip_policy_hit_buffer_dropped") == 0 {
			t.Fatal("溢出丢弃必须告警")
		}
	})
}

// padLeft 生成固定 4 位十进制后缀，保证每个 ipHash 64 位且互不相同。
func padLeft(value int) string {
	text := "0000"
	suffix := []byte(text)
	suffix[3] = byte('0' + value%10)
	suffix[2] = byte('0' + (value/10)%10)
	suffix[1] = byte('0' + (value/100)%10)
	suffix[0] = byte('0' + (value/1000)%10)
	return string(suffix)
}

func TestWIPolicyCacheRecordHitFailureWarns(t *testing.T) {
	// 契约：fire-and-forget 记录失败时只告警不 panic（Node
	// gateway_client_ip_blacklist_hit_record_failed）。performance 模式直写
	// source，失败路径才会传播到告警。
	cache, source, _, _, logger := newWITestPolicyCache(t, func(opts *PolicyCacheOptions) {
		opts.RuntimeMode = RuntimeModePerformance
	})
	source.err = errors.New("数据库不可用")
	cache.RecordClientIPPolicyHit(gatewaypreauth.BlacklistPolicy{
		ID: "p1", IPHash: "abc", Reason: "滥用",
	})
	if logger.count("gateway_client_ip_blacklist_hit_record_failed") != 1 {
		t.Fatal("记录失败必须告警")
	}
	// 无 logger 时静默吞错，且无 Reason 时转换结果不携带 reason。
	cacheNoLog, sourceNoLog, _, _, _ := newWITestPolicyCache(t, func(opts *PolicyCacheOptions) {
		opts.Logger = nil
		opts.RuntimeMode = RuntimeModePerformance
	})
	sourceNoLog.err = nil
	cacheNoLog.RecordClientIPPolicyHit(gatewaypreauth.BlacklistPolicy{ID: "p1", IPHash: "abc"})
	if sourceNoLog.hitCalls != 1 {
		t.Fatal("performance 模式必须直写 source")
	}
	if got := sourceNoLog.hits[0]; got.IPHash != "abc" || got.HitCount != 1 {
		t.Fatalf("转换后的命中=%+v", got)
	}
}

func TestWIPolicyCacheStatsWriterBridge(t *testing.T) {
	t.Run("server 角色走 stats writer", func(t *testing.T) {
		stats := &stubStatsWriter{}
		cache, source, _, scheduler, _ := newWITestPolicyCache(t, func(opts *PolicyCacheOptions) {
			opts.ProcessRole = ProcessRoleServer
			opts.StatsWriter = stats
		})
		ipHash := NormalizeClientIPForStats("10.0.0.1").IPHash
		policy := wiBlacklistPolicy("p1", ipHash)
		if err := cache.RecordClientIPPolicyHitAsync(context.Background(), policy); err != nil {
			t.Fatalf("记录命中失败: %v", err)
		}
		scheduler.advance(clientIPPolicyHitFlushDelay)
		// 冲刷走 requestStatsWriter → RecordClientIPPolicyHits 操作。
		if len(stats.operations) == 0 || stats.operations[len(stats.operations)-1] != StatsWriterOpRecordClientIPPolicyHits {
			t.Fatalf("operations=%v", stats.operations)
		}
		if source.hitCalls != 0 {
			t.Fatal("stats bridge 模式不得直写 source")
		}
	})

	t.Run("stats 不可用时直写与报错", func(t *testing.T) {
		stats := &stubStatsWriter{}
		cache, _, _, _, _ := newWITestPolicyCache(t, func(opts *PolicyCacheOptions) {
			opts.ProcessRole = ProcessRoleServer
			opts.StatsWriter = nil
		})
		// requestStatsWriter 在 stats 为 nil 时报错（flushClientIPPolicyHits 吞掉并告警）。
		err := cache.requestStatsWriter(context.Background(), StatsWriterOpRecordClientIPPolicyHits, StatsWriterPayload{})
		if err == nil || !strings.Contains(err.Error(), "stats-writer 不可用") {
			t.Fatalf("err=%v", err)
		}
		// EnsureSnapshotLoaded 走 stats 失败 → 错误透传。
		_ = stats
		cacheBroken, _, _, _, _ := newWITestPolicyCache(t, func(opts *PolicyCacheOptions) {
			opts.ProcessRole = ProcessRoleServer
			opts.StatsWriter = &stubStatsWriter{err: errors.New("桥接失败")}
		})
		if _, err := cacheBroken.InspectPolicy(context.Background(), "10.0.0.1", InspectClientIPPolicyOptions{EnsureSnapshotLoaded: true}); err == nil {
			t.Fatal("stats 桥接失败必须透传")
		}
	})
}

func TestWIPolicyCacheReplaceAndReloadSnapshot(t *testing.T) {
	ipHash := NormalizeClientIPForStats("10.0.0.1").IPHash
	policy := wiBlacklistPolicy("p1", ipHash)

	t.Run("memory 模式异步替换退化为本地替换", func(t *testing.T) {
		cache, _, _, _, _ := newWITestPolicyCache(t, nil)
		if err := cache.ReplaceClientIPPolicySharedSnapshotAsync(context.Background(), []ActiveClientIPPolicy{policy}); err != nil {
			t.Fatalf("替换失败: %v", err)
		}
		decision, err := cache.InspectPolicy(context.Background(), "10.0.0.1", InspectClientIPPolicyOptions{})
		if err != nil || !decision.Blocked {
			t.Fatalf("decision=%+v err=%v", decision, err)
		}
	})

	t.Run("redis 模式异步替换写共享缓存", func(t *testing.T) {
		factory := newFakeSharedCacheFactory()
		cache, _, clock, _, _ := newWITestPolicyCache(t, func(opts *PolicyCacheOptions) {
			opts.CacheDriver = CacheDriverRedis
			opts.Shared = factory
		})
		if err := cache.ReplaceClientIPPolicySharedSnapshotAsync(context.Background(), []ActiveClientIPPolicy{policy}); err != nil {
			t.Fatalf("替换失败: %v", err)
		}
		decision, err := cache.InspectPolicy(context.Background(), "10.0.0.1", InspectClientIPPolicyOptions{CacheOnly: true})
		if err != nil || !decision.Blocked {
			t.Fatalf("cacheOnly 读共享缓存失败: %+v err=%v", decision, err)
		}
		// 清空后 cacheOnly 读不到。
		if err := cache.ClearClientIPPolicyCacheLocal(context.Background()); err != nil {
			t.Fatalf("清空失败: %v", err)
		}
		decision, err = cache.InspectPolicy(context.Background(), "10.0.0.1", InspectClientIPPolicyOptions{CacheOnly: true})
		if err != nil || decision.Blocked {
			t.Fatalf("清空后必须不阻塞: %+v err=%v", decision, err)
		}
		_ = clock
	})

	t.Run("memory reload 优先读共享快照", func(t *testing.T) {
		cache, source, _, _, _ := newWITestPolicyCache(t, nil)
		source.policies = []ActiveClientIPPolicy{policy}
		ctx := context.Background()
		// 首次 reload：从 DB 加载并写共享快照。
		if err := cache.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{}); err != nil {
			t.Fatalf("reload 失败: %v", err)
		}
		firstLoadedAt := cache.GetClientIPPolicyCacheRuntime().SnapshotLoadedAt
		if firstLoadedAt == "" {
			t.Fatal("reload 后必须记录 loadedAt")
		}
		if source.listCalls != 1 {
			t.Fatalf("listCalls=%d", source.listCalls)
		}
		// 共享快照存在后，再次 reload 不再访问 DB（共享快照命中）。
		source.policies = nil
		if err := cache.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{}); err != nil {
			t.Fatalf("第二次 reload 失败: %v", err)
		}
		if source.listCalls != 1 {
			t.Fatalf("共享命中后 listCalls=%d", source.listCalls)
		}
		// bypass 时清空共享缓存并从 DB（空）加载。
		if err := cache.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{BypassSharedCache: true}); err != nil {
			t.Fatalf("bypass reload 失败: %v", err)
		}
		if source.listCalls != 2 {
			t.Fatalf("bypass 后 listCalls=%d", source.listCalls)
		}
		if got := cache.GetClientIPPolicyCacheRuntime().SnapshotPolicyCount; got != 0 {
			t.Fatalf("空 source reload 后快照应为空: %d", got)
		}
	})

	t.Run("source 报错时 reload 失败", func(t *testing.T) {
		cache, source, _, _, _ := newWITestPolicyCache(t, nil)
		source.err = errors.New("数据库不可用")
		if err := cache.ReloadClientIPPolicyCacheLocal(context.Background(), ReloadClientIPPolicyCacheLocalOptions{BypassSharedCache: true}); err == nil {
			t.Fatal("source 失败必须透传")
		}
	})

	t.Run("坏 expiresAt 阻止本地替换", func(t *testing.T) {
		cache, _, _, _, _ := newWITestPolicyCache(t, nil)
		bad := wiBlacklistPolicy("p1", ipHash)
		badTime := "not-a-time"
		bad.ExpiresAt = &badTime
		if err := cache.ReplaceClientIPPolicyCacheLocal([]ActiveClientIPPolicy{bad}, ReplaceClientIPPolicyCacheLocalOptions{}); err == nil {
			t.Fatal("坏 expiresAt 必须报错")
		}
	})

	t.Run("redis 模式同步替换要求 skip 标记", func(t *testing.T) {
		factory := newFakeSharedCacheFactory()
		cache, _, _, _, _ := newWITestPolicyCache(t, func(opts *PolicyCacheOptions) {
			opts.CacheDriver = CacheDriverRedis
			opts.Shared = factory
		})
		if err := cache.ReplaceClientIPPolicyCacheLocal([]ActiveClientIPPolicy{policy}, ReplaceClientIPPolicyCacheLocalOptions{}); err == nil {
			t.Fatal("高性能模式禁止同步写共享缓存，必须报错")
		}
		if err := cache.ReplaceClientIPPolicyCacheLocal(nil, ReplaceClientIPPolicyCacheLocalOptions{SkipSharedCache: true}); err != nil {
			t.Fatalf("skip 标记替换失败: %v", err)
		}
		// bypass reload 清空共享缓存。
		if err := cache.ReloadClientIPPolicyCacheLocal(context.Background(), ReloadClientIPPolicyCacheLocalOptions{BypassSharedCache: true}); err != nil {
			t.Fatalf("redis bypass reload 失败: %v", err)
		}
	})
}

func TestWIPolicyCacheCloseCancelsFlush(t *testing.T) {
	cache, source, _, scheduler, _ := newWITestPolicyCache(t, nil)
	ipHash := NormalizeClientIPForStats("10.0.0.1").IPHash
	policy := wiBlacklistPolicy("p1", ipHash)
	if err := cache.RecordClientIPPolicyHitAsync(context.Background(), policy); err != nil {
		t.Fatalf("记录命中失败: %v", err)
	}
	// Close 取消已排定的冲刷；推进时钟后 source 不得收到写入。
	cache.Close()
	scheduler.advance(clientIPPolicyHitFlushDelay)
	if source.hitCalls != 0 {
		t.Fatalf("Close 后不得冲刷: hitCalls=%d", source.hitCalls)
	}
	// Close 后再次调度也被拒绝（closed 状态）。
	if err := cache.RecordClientIPPolicyHitAsync(context.Background(), policy); err != nil {
		t.Fatalf("Close 后记录失败: %v", err)
	}
	scheduler.advance(clientIPPolicyHitFlushDelay)
	if source.hitCalls != 0 {
		t.Fatalf("closed 后不得冲刷: hitCalls=%d", source.hitCalls)
	}
}

func TestWITimerFlushSchedulerAfterFunc(t *testing.T) {
	// 契约：默认调度器用 time.AfterFunc；取消句柄可停掉未触发的回调。
	fired := make(chan struct{}, 1)
	cancel := timerFlushScheduler{}.AfterFunc(time.Millisecond, func() { fired <- struct{}{} })
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("回调未触发")
	}
	cancel() // 触发后的取消必须是安全的 no-op。
	cancelLate := timerFlushScheduler{}.AfterFunc(10*time.Second, func() {})
	cancelLate()
}

// pendingHitsKeyExists 测试探针：确认 cache 处于直写模式（无缓冲 key）。
func (c *PolicyCache) pendingHitsKeyExists() bool {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	return len(c.pendingHits) > 0
}

// 编译期确认 fakeSharedCacheFactory 满足共享缓存工厂 seam。
var _ gatewayruntimecache.SharedCacheFactory = (*fakeSharedCacheFactory)(nil)
