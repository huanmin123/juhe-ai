package gatewayproxyhealth

import (
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 用户请求限制计数器的容量、清理与重置语义：
// 固定内存上限通过“先精确清理再淘汰最旧”维持，淘汰数可观测。
func TestWhUserRequestLimitCapacityAndReset(t *testing.T) {
	clock := newFakeClock(1_000_000)
	maxEntries := 2
	stride := 1 << 30 // 不触发周期清理，专注容量淘汰路径
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{
		MaxEntries: &maxEntries, CleanupStride: &stride,
	})
	limits := settingsLimits(int64Ptr(5), nil, nil, nil)
	// 3 个系统账户 × perMinute：超出容量 2，最旧桶被淘汰。
	for _, id := range []string{"u1", "u2", "u3"} {
		decision := counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: id, Settings: limits, NowMs: int64Ptr(1_000_000)})
		if !decision.Allowed {
			t.Fatalf("容量内消费被拒绝: %+v", decision)
		}
	}
	stats := counter.Stats()
	if stats.Entries != 2 || stats.CapacityEvictions != 1 {
		t.Fatalf("容量淘汰 = %+v, want entries=2 evictions=1", stats)
	}
	if stats.DirtyEntries != 2 {
		t.Fatalf("脏桶数 = %d", stats.DirtyEntries)
	}

	// Reset 清空全部状态与计数。
	counter.Reset()
	if stats := counter.Stats(); stats.Entries != 0 || stats.DirtyEntries != 0 || stats.CapacityEvictions != 0 {
		t.Fatalf("Reset 后 = %+v", stats)
	}
	if counter.Size() != 0 {
		t.Fatalf("Reset 后 Size = %d", counter.Size())
	}
	// 无任何限制配置：直接放行且不产生条目。
	if decision := counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u9", Settings: gatewayruntimecache.GatewaySettings{}, NowMs: int64Ptr(1_000_000)}); !decision.Allowed {
		t.Fatalf("无限制必须放行: %+v", decision)
	}
	if counter.Size() != 0 {
		t.Fatalf("无限制不得创建条目: size=%d", counter.Size())
	}
}

// 周期清理与过期重建：条目到期后原位重建（JS Map 插入序语义）。
func TestWhUserRequestLimitCleanupAndBucketRotation(t *testing.T) {
	clock := newFakeClock(1_000_000)
	stride := 1
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{CleanupStride: &stride})
	limits := settingsLimits(int64Ptr(2), nil, nil, nil)
	now := int64(1_000_000)
	// 同一分钟桶内消耗 2 次触达上限。
	for i := 0; i < 2; i++ {
		if decision := counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: limits, NowMs: &now}); !decision.Allowed {
			t.Fatalf("第 %d 次消费被拒: %+v", i, decision)
		}
	}
	blocked := counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: limits, NowMs: &now})
	if blocked.Allowed || blocked.Window != userRequestLimitWindowPerMinute || blocked.Limit == nil || *blocked.Limit != 2 {
		t.Fatalf("分钟窗拦截 = %+v", blocked)
	}
	// now=1_000_000 落在第 17 分钟桶内 40s 处：窗口剩余 20s。
	if blocked.RetryAfterSeconds == nil || *blocked.RetryAfterSeconds != 20 {
		t.Fatalf("分钟窗 RetryAfter = %v", blocked.RetryAfterSeconds)
	}
	// 3 次消费触发 stride=1 的周期清理；推进 2 分钟后旧桶到期，清理后重建计数从 0 开始。
	now += 120_000
	if decision := counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: limits, NowMs: &now}); !decision.Allowed {
		t.Fatalf("新窗口必须放行: %+v", decision)
	}
	if counter.Size() != 1 {
		t.Fatalf("过期桶必须被清理重建: size=%d", counter.Size())
	}

	// 非法时区回退 UTC：消费不受影响（Node Intl RangeError 的文档化回退）。
	badTZ := settingsLimits(int64Ptr(5), nil, nil, nil)
	badTZ.UsageStatsTimezone = "Mars/Olympus"
	if decision := counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u2", Settings: badTZ, NowMs: &now}); !decision.Allowed {
		t.Fatalf("非法时区必须回退 UTC: %+v", decision)
	}

	// CleanupExpired 显式入口：推进 10 分钟后两个桶均过期，limit 足够时全部清掉。
	now += 10 * 60_000
	removed := counter.CleanupExpired(&now, intPtrValue(10))
	if removed != 2 {
		t.Fatalf("过期清理 = %d, want 2", removed)
	}
	// 同步结果应用于未知条目必须被忽略。
	counter.ApplySyncResults([]UserRequestLimitSyncResult{{EntryKey: "missing", RemoteTotal: 9, SentLocalCount: 9}})
	// DirtySnapshot limit<=0 回退 512。
	snapshots := counter.DirtySnapshot(0)
	for _, snapshot := range snapshots {
		if snapshot.EntryKey == "" {
			t.Fatalf("脏快照 = %+v", snapshot)
		}
	}
}

func intPtrValue(v int) *int { return &v }

// 协调器后台 tick、容量日志与 sleep 注入点（直接驱动，不起真实循环）。
func TestWhCoordinatorScheduledTickAndCapacityLog(t *testing.T) {
	clock := newFakeClock(1_000_000)
	maxEntries := 1
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{MaxEntries: &maxEntries})
	log := &recordingLog{}
	coordinator := NewUserRequestLimitCoordinator(counter, clock.Now, UserRequestLimitCoordinatorOptions{
		Log: log.record,
	})

	// 空计数器：tick 无日志。
	coordinator.runScheduledTick()
	if log.count() != 0 {
		t.Fatalf("无容量压力不得记日志: %v", log.events())
	}

	// 制造容量淘汰 → 首次 tick 记容量日志，间隔内第二次不记。
	limits := settingsLimits(int64Ptr(5), nil, nil, nil)
	counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "a", Settings: limits, NowMs: int64Ptr(1_000_000)})
	counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "b", Settings: limits, NowMs: int64Ptr(1_000_001)})
	if stats := counter.Stats(); stats.CapacityEvictions != 1 {
		t.Fatalf("容量淘汰 = %+v", stats)
	}
	coordinator.runScheduledTick()
	if log.count() != 1 || log.events()[0] != "gateway_user_request_limit_capacity_exhausted" {
		t.Fatalf("容量日志 = %v", log.events())
	}
	coordinator.runScheduledTick()
	if log.count() != 1 {
		t.Fatalf("日志间隔内不得重复记录: %d", log.count())
	}
	// 间隔（30s）过后再次淘汰才允许记录。
	clock.Advance(30_001)
	counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "c", Settings: limits, NowMs: int64Ptr(1_030_002)})
	coordinator.runScheduledTick()
	if log.count() != 2 {
		t.Fatalf("间隔后必须记录: %d", log.count())
	}

	// Sleep 注入点：注入 hook 记录时长，不走真实 time.Sleep。
	var slept []time.Duration
	coordinator.opts.Sleep = func(d time.Duration) { slept = append(slept, d) }
	coordinator.coordinatorSleep(20 * time.Millisecond)
	if len(slept) != 1 || slept[0] != 20*time.Millisecond {
		t.Fatalf("sleep 注入 = %v", slept)
	}
}

// 同步失败退避：错误日志限频、退避上限与 Invalidate 调用。
func TestWhCoordinatorSyncBackoffDetails(t *testing.T) {
	clock := newFakeClock(1_000_000)
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{})
	log := &recordingLog{}
	provider := &fakeClientProvider{client: &fakeEvalClient{failAll: true}}
	provider.clientErrAt.Store(1) // Client 建连即失败
	coordinator := NewUserRequestLimitCoordinator(counter, clock.Now, UserRequestLimitCoordinatorOptions{
		RedisEnabled:     true,
		Namespace:        "juhe-ai:wh",
		ClientProvider:   provider,
		Log:              log.record,
		ServerInstanceID: "wh-instance",
	})
	limits := settingsLimits(int64Ptr(5), nil, nil, nil)
	now := int64(1_000_000)
	counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: limits, NowMs: &now})

	// 第一次失败：记录日志，退避 2s。
	if err := coordinator.SynchronizeDirtyCounters(true); err == nil {
		t.Fatal("同步失败必须报错")
	}
	if log.count() != 1 {
		t.Fatalf("首次失败必须记录日志: %d", log.count())
	}
	// 立即重试（非强制）：退避期内静默跳过。
	if err := coordinator.SynchronizeDirtyCounters(false); err != nil {
		t.Fatalf("退避期内非强制同步必须静默: %v", err)
	}
	// 退避结束后再次失败：不记日志（30s 限频），退避翻倍。
	clock.Advance(2_001)
	if err := coordinator.SynchronizeDirtyCounters(false); err == nil {
		t.Fatal("退避结束后应再次尝试并失败")
	}
	if log.count() != 1 {
		t.Fatalf("限频期内不得重复记录: %d", log.count())
	}
	// 连续失败推进到退避封顶 30s；每次间隔超过 30s 日志窗口都放行一条。
	for i := 0; i < 6; i++ {
		clock.Advance(30_001)
		if err := coordinator.SynchronizeDirtyCounters(true); err == nil {
			t.Fatal("持续失败必须报错")
		}
	}
	// 最后一次：退避必须已封顶（不再随失败次数翻倍）。
	clock.Advance(30_001)
	if err := coordinator.SynchronizeDirtyCounters(true); err == nil {
		t.Fatal("强制同步失败必须报错")
	}
	if log.count() != 8 {
		t.Fatalf("限频窗口放行次数 = %d, want 8", log.count())
	}
	// Redis 禁用时强制同步静默返回。
	coordinator.opts.RedisEnabled = false
	if err := coordinator.SynchronizeDirtyCounters(true); err != nil {
		t.Fatalf("Redis 禁用必须静默: %v", err)
	}
}

// 数字同步值解析：int/float/string/负值/未知类型。
func TestWhNumericSyncValue(t *testing.T) {
	cases := []struct {
		input any
		want  int64
	}{
		{int64(7), 7},
		{7, 7},
		{7.9, 7},
		{"8", 8},
		{"bad", 0},
		{-3, 0},
		{nil, 0},
		{struct{}{}, 0},
	}
	for _, c := range cases {
		if got := numericSyncValue(c.input); got != c.want {
			t.Fatalf("numericSyncValue(%#v) = %d, want %d", c.input, got, c.want)
		}
	}
	if formatSyncInt64(-42) != "-42" || formatSyncInt64(0) != "0" || formatSyncInt64(123) != "123" {
		t.Fatal("同步整数格式化语义错误")
	}
}

// modelsratelimit G05 端口包装与测试助手。
func TestWhModelsRateLimitPortWrappers(t *testing.T) {
	clock := newFakeClock(1_000_000)
	limiter := NewPenaltyWindowRateLimiter(clock.Now, false, nil, "juhe-ai:wh")
	authService := NewAuthenticatedModelsRateLimitService(clock.Now, limiter, nil)
	publicService := NewPublicModelsRateLimitService(clock.Now, limiter)

	// Consume（无 nowMs 的 G05 端口）与 nowMs 注入入口行为一致。
	input := gatewayModelsInput("key-1", "10.0.0.1")
	if decision, err := authService.Consume(contextBackground(), input); err != nil || !decision.Allowed {
		t.Fatalf("端口 Consume = %+v err=%v", decision, err)
	}
	if decision, err := authService.ConsumeAuthenticatedModelsRateLimit(contextBackground(), input, nil); err != nil || !decision.Allowed {
		t.Fatalf("nowMs 注入 Consume = %+v err=%v", decision, err)
	}
	authService.ClearForTest()
	publicService.ClearForTest()

	// UserRequestLimits G05 端口包装：决策字段逐一映射。
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{})
	coordinator := NewUserRequestLimitCoordinator(counter, clock.Now, UserRequestLimitCoordinatorOptions{})
	service := NewUserRequestLimitsService(counter, coordinator)
	if service.Counter() != counter || service.Coordinator() != coordinator {
		t.Fatal("Counter/Coordinator 访问器必须返回底层实例")
	}
	limits := settingsLimits(int64Ptr(1), nil, nil, nil)
	if decision := service.Consume(gatewayConsumeInput("u1", limits, nil, nil)); !decision.Allowed {
		t.Fatalf("首次消费必须放行: %+v", decision)
	}
	blocked := service.Consume(gatewayConsumeInput("u1", limits, nil, nil))
	if blocked.Allowed || blocked.Window != gatewaypreauth.UserRequestLimitWindow(userRequestLimitWindowPerMinute) {
		t.Fatalf("端口拦截决策 = %+v", blocked)
	}
	if blocked.Limit == nil || *blocked.Limit != 1 {
		t.Fatalf("端口拦截上限 = %+v", blocked.Limit)
	}
	if blocked.RetryAfterSeconds == nil || *blocked.RetryAfterSeconds != 20 {
		t.Fatalf("端口拦截重试 = %+v", blocked.RetryAfterSeconds)
	}

	// SettingsForTest 只装配限额字段的测试助手。
	filled := SettingsForTest(int64Ptr(1), int64Ptr(2), int64Ptr(3), int64Ptr(4), "Asia/Shanghai")
	if filled.GatewayUserRequestLimitPerWeek == nil || *filled.GatewayUserRequestLimitPerWeek != 3 || filled.UsageStatsTimezone != "Asia/Shanghai" {
		t.Fatalf("SettingsForTest = %+v", filled)
	}
}

// 并发下的 dirty 索引一致性：并发消费 + 快照轮转不得死锁或丢计数。
func TestWhUserRequestLimitConcurrentConsume(t *testing.T) {
	clock := newFakeClock(1_000_000)
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{})
	limits := settingsLimits(int64Ptr(1_000_000), nil, nil, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := "u" + itoaForTest(int64(n))
			for j := 0; j < 20; j++ {
				counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: id, Settings: limits, NowMs: int64Ptr(1_000_000)})
				_ = counter.DirtySnapshot(4)
			}
		}(i)
	}
	wg.Wait()
	if counter.Size() != 8 {
		t.Fatalf("并发消费 size = %d", counter.Size())
	}
	total := 0
	for _, snapshot := range counter.DirtySnapshot(1024) {
		total += int(snapshot.LocalCount)
	}
	if total != 160 {
		t.Fatalf("并发消费总数 = %d, want 160", total)
	}
}

// 编译期守卫 whLatencyStore 满足 RuntimeStateStore（防漂移）。
var _ RuntimeStateStore = (*whLatencyStore)(nil)
