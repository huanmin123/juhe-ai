package gatewayclientip

// AI 账户每分钟请求上限设计 §4/§5.2 的行为契约测试：RPM 与硬并发在同一原子
// 临界区判定（并发检查在前、RPM 在后），任何拒绝都不自增；通过才同时 +1；
// Release 只退并发不退 RPM；窗口按自然分钟固定滚动，读取与判定共用同一惰性
// 重置路径；高并发分组队列的 ready 判定在并发余量之外叠加 RPM 余量。

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

func TestMemoryAccountConcurrencyRPMAtomicJudgement(t *testing.T) {
	clock := newManualClock(time.UnixMilli(1_800_000)) // bucket = 30
	tracker := NewMemoryAccountConcurrency(clock)
	ctx := context.Background()

	// 契约：并发满 / RPM 有余——拒绝占位且不消耗 RPM。
	for index := 0; index < 2; index++ {
		outcome := tracker.TryAcquire("acc-a", 2, AccountConcurrencyLaneText, 2, 10)
		if !outcome.Acquired {
			t.Fatalf("第 %d 次并发占位应成功: %+v", index+1, outcome)
		}
	}
	full := tracker.TryAcquire("acc-a", 2, AccountConcurrencyLaneText, 2, 10)
	if full.Acquired || full.RPMCurrent != 2 || full.RPMLimit != 10 || full.RPMResetAtMs != 1_860_000 {
		t.Fatalf("并发满拒绝 = %+v, want 拒绝且回带 RPM 观测（未扣减）", full)
	}
	if state := tracker.CurrentAccountRPM("acc-a"); state.Used != 2 || state.ResetAtMs != 1_860_000 {
		t.Fatalf("并发满拒绝后 RPM 计数 = %+v, want 保持 2（不扣 RPM）", state)
	}
	tracker.Release("acc-a", AccountConcurrencyLaneText)

	// 契约：RPM 满 / 并发有余——拒绝占位且不占并发。
	rpmFull := tracker.TryAcquire("acc-b", 5, AccountConcurrencyLaneText, 5, 1)
	if !rpmFull.Acquired || rpmFull.RPMCurrent != 1 {
		t.Fatalf("首占应成功且计数 1: %+v", rpmFull)
	}
	blocked := tracker.TryAcquire("acc-b", 5, AccountConcurrencyLaneText, 5, 1)
	if blocked.Acquired || blocked.RPMCurrent != 1 || blocked.RPMLimit != 1 {
		t.Fatalf("RPM 满拒绝 = %+v, want 拒绝", blocked)
	}
	if got := tracker.CurrentAccountConcurrency("acc-b", ""); got != 1 {
		t.Fatalf("RPM 满拒绝后并发计数 = %d, want 1（不占并发）", got)
	}

	// 契约：两者都满——并发半边先生效，同样不自增任何计数。
	both := tracker.TryAcquire("acc-b", 1, AccountConcurrencyLaneText, 1, 1)
	if both.Acquired || both.Total != 1 || both.RPMCurrent != 1 {
		t.Fatalf("双满拒绝 = %+v, want 拒绝且计数不变", both)
	}

	// 契约：Release 只退并发，不退 RPM。
	tracker.Release("acc-b", AccountConcurrencyLaneText)
	if got := tracker.CurrentAccountConcurrency("acc-b", ""); got != 0 {
		t.Fatalf("Release 后并发 = %d, want 0", got)
	}
	if state := tracker.CurrentAccountRPM("acc-b"); state.Used != 1 {
		t.Fatalf("Release 后 RPM = %d, want 1（不回退）", state.Used)
	}

	// 契约：窗口滚动重置——跨过分钟边界后计数归 0，可继续占位。
	clock.advance(2 * time.Minute)
	afterRoll := tracker.TryAcquire("acc-b", 5, AccountConcurrencyLaneText, 5, 1)
	if !afterRoll.Acquired || afterRoll.RPMCurrent != 1 {
		t.Fatalf("滚动后首占 = %+v, want 成功且计数从 0 重新累加", afterRoll)
	}
	if state := tracker.CurrentAccountRPM("acc-b"); state.Used != 1 || state.ResetAtMs != 1_980_000 {
		t.Fatalf("滚动后读取 = %+v, want 新窗口计数 1 与新边界", state)
	}
	if err := ctx.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryAccountConcurrencyConsumeRPM(t *testing.T) {
	clock := newManualClock(time.UnixMilli(1_800_000)) // bucket = 30
	tracker := NewMemoryAccountConcurrency(clock)
	ctx := context.Background()

	// TryAcquire 已为首次出站计 1 次；同一槽内的后续消费继续累加。
	acquired := tracker.TryAcquire("acc-consume", 5, AccountConcurrencyLaneText, 5, 2)
	if !acquired.Acquired || acquired.RPMCurrent != 1 {
		t.Fatalf("首次占位 = %+v, want RPM 1", acquired)
	}
	allowed, current, resetAtMs, err := tracker.ConsumeRPM(ctx, "acc-consume", 2)
	if err != nil || !allowed || current != 2 || resetAtMs != 1_860_000 {
		t.Fatalf("第二次真实出站消费 = allowed=%v current=%d reset=%d err=%v, want 2", allowed, current, resetAtMs, err)
	}
	allowed, current, resetAtMs, err = tracker.ConsumeRPM(ctx, "acc-consume", 2)
	if err != nil || allowed || current != 2 || resetAtMs != 1_860_000 {
		t.Fatalf("RPM 满消费 = allowed=%v current=%d reset=%d err=%v, want 拒绝且计数不变", allowed, current, resetAtMs, err)
	}
	// 消费不动并发计数；RPM 满时 TryAcquire 仍按原语义拒绝且不再自增。
	if got := tracker.CurrentAccountConcurrency("acc-consume", ""); got != 1 {
		t.Fatalf("ConsumeRPM 后并发 = %d, want 1", got)
	}
	rejected := tracker.TryAcquire("acc-consume", 5, AccountConcurrencyLaneText, 5, 2)
	if rejected.Acquired || rejected.RPMCurrent != 2 || rejected.Total != 1 {
		t.Fatalf("RPM 满占位 = %+v, want 拒绝且计数不变", rejected)
	}

	// 窗口滚动后消费从 0 重新累加。
	clock.advance(2 * time.Minute)
	allowed, current, resetAtMs, err = tracker.ConsumeRPM(ctx, "acc-consume", 2)
	if err != nil || !allowed || current != 1 || resetAtMs != 1_980_000 {
		t.Fatalf("滚动后消费 = allowed=%v current=%d reset=%d err=%v, want 新窗口 1", allowed, current, resetAtMs, err)
	}
}

func TestRedisAccountConcurrencyConsumeRPM(t *testing.T) {
	server := miniredis.RunT(t)
	nowMs := int64(1_800_000)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	instance, err := NewRedisAccountConcurrency(RedisAccountConcurrencyOptions{
		Client:    client,
		Namespace: "dev",
		Clock:     func() int64 { return nowMs },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	outcome, err := instance.AcquireAccountConcurrency(ctx, "acc-consume", AccountConcurrencyLaneText, (6 * time.Hour).Milliseconds(), 5, 5, 2)
	if err != nil || !outcome.Acquired || outcome.RPMCurrent != 1 {
		t.Fatalf("首次占位 = %+v err %v, want RPM 1", outcome, err)
	}
	allowed, current, resetAtMs, err := instance.ConsumeRPM(ctx, "acc-consume", 2)
	if err != nil || !allowed || current != 2 || resetAtMs != 1_860_000 {
		t.Fatalf("第二次消费 = allowed=%v current=%d reset=%d err=%v, want 2", allowed, current, resetAtMs, err)
	}
	// ConsumeRPM 只延长不足 120s 的 TTL，不能把并发槽的 6h 泄漏兜底缩短。
	ttl, err := client.PTTL(ctx, instance.accountConcurrencyKey("acc-consume")).Result()
	if err != nil || ttl < 5*time.Hour {
		t.Fatalf("consume 后 key TTL = %v err %v, want 仍接近 6h", ttl, err)
	}
	allowed, current, resetAtMs, err = instance.ConsumeRPM(ctx, "acc-consume", 2)
	if err != nil || allowed || current != 2 || resetAtMs != 1_860_000 {
		t.Fatalf("RPM 满消费 = allowed=%v current=%d reset=%d err=%v, want 拒绝", allowed, current, resetAtMs, err)
	}
	states, err := instance.LoadAccountRPMByID(ctx, []string{"acc-consume"})
	if err != nil || states["acc-consume"].Used != 2 {
		t.Fatalf("消费后读取 = %v err %v, want 2", states, err)
	}

	nowMs = 2_400_000
	allowed, current, resetAtMs, err = instance.ConsumeRPM(ctx, "acc-consume", 2)
	if err != nil || !allowed || current != 1 || resetAtMs != 2_460_000 {
		t.Fatalf("滚动后消费 = allowed=%v current=%d reset=%d err=%v, want 新窗口 1", allowed, current, resetAtMs, err)
	}
}

func TestMemoryAccountConcurrencyRPMLoadByIDLazyReset(t *testing.T) {
	clock := newManualClock(time.UnixMilli(1_800_000))
	tracker := NewMemoryAccountConcurrency(clock)
	tracker.TryAcquire("acc-a", 0, "", 0, 5)
	states, err := tracker.LoadAccountRPMByID(context.Background(), []string{"acc-a", "acc-a", "", "acc-b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("去重与空 ID 过滤后 = %v", states)
	}
	if states["acc-a"].Used != 1 || states["acc-b"].Used != 0 {
		t.Fatalf("states = %v, want acc-a=1 acc-b=0", states)
	}
	// 读取面：窗口过期按 0 返回并就地重置（设计 §4.2）。
	clock.advance(time.Minute)
	states, err = tracker.LoadAccountRPMByID(context.Background(), []string{"acc-a"})
	if err != nil {
		t.Fatal(err)
	}
	if states["acc-a"].Used != 0 || states["acc-a"].ResetAtMs != 1_920_000 {
		t.Fatalf("过期窗口读取 = %v, want 0 与新边界", states)
	}
}

func TestRedisAccountConcurrencyRPMWindow(t *testing.T) {
	server := miniredis.RunT(t)
	nowMs := int64(1_800_000)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	instance, err := NewRedisAccountConcurrency(RedisAccountConcurrencyOptions{
		Client:    client,
		Namespace: "dev",
		Clock:     func() int64 { return nowMs },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 契约：RPM 上限 2——前两次成功，第三次拒绝且三计数都不自增。
	for index := 0; index < 2; index++ {
		outcome, acquireErr := instance.AcquireAccountConcurrency(ctx, "acc-1", AccountConcurrencyLaneText, 120_000, 5, 5, 2)
		if acquireErr != nil || !outcome.Acquired || outcome.Total != index+1 || outcome.RPMCurrent != index+1 {
			t.Fatalf("第 %d 次 acquire = %+v err %v, want 成功", index+1, outcome, acquireErr)
		}
	}
	blocked, acquireErr := instance.AcquireAccountConcurrency(ctx, "acc-1", AccountConcurrencyLaneText, 120_000, 5, 5, 2)
	if acquireErr != nil || blocked.Acquired || blocked.RPMCurrent != 2 || blocked.RPMResetAtMs != 1_860_000 {
		t.Fatalf("RPM 满拒绝 = %+v err %v, want 拒绝且回带窗口观测", blocked, acquireErr)
	}
	// 契约：并发满 / RPM 有余——拒绝且 RPM 计数不扣。
	byID, err := instance.LoadAccountRPMByID(ctx, []string{"acc-1"})
	if err != nil || byID["acc-1"].Used != 2 {
		t.Fatalf("RPM 读取 = %v err %v, want 2", byID, err)
	}
	// 契约：窗口滚动后惰性重置（acquire 落新 bucket 并重置计数）。
	nowMs = 2_400_000
	rolled, acquireErr := instance.AcquireAccountConcurrency(ctx, "acc-1", AccountConcurrencyLaneText, 120_000, 5, 5, 2)
	if acquireErr != nil || !rolled.Acquired || rolled.RPMCurrent != 1 {
		t.Fatalf("滚动后 acquire = %+v err %v, want 成功且新窗口计数 1", rolled, acquireErr)
	}
	states, err := instance.LoadAccountRPMByID(ctx, []string{"acc-1"})
	if err != nil || states["acc-1"].Used != 1 {
		t.Fatalf("滚动后读取 = %v err %v, want 1", states, err)
	}
	// 契约：读取面 bucket 过期按 0 返回。
	nowMs = 3_000_000
	states, err = instance.LoadAccountRPMByID(ctx, []string{"acc-1"})
	if err != nil || states["acc-1"].Used != 0 {
		t.Fatalf("过期窗口读取 = %v err %v, want 0", states, err)
	}
}

func TestRedisAccountConcurrencyReleasePreservesRPMWindow(t *testing.T) {
	server := miniredis.RunT(t)
	nowMs := int64(1_800_000) // bucket = 30
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	instance, err := NewRedisAccountConcurrency(RedisAccountConcurrencyOptions{
		Client:    client,
		Namespace: "dev",
		Clock:     func() int64 { return nowMs },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 契约：并发排空（total 归 0）不得清掉同一 HASH 的 RPM 窗口计数——release
	// 只删并发字段，残留字段由 acquire 的 PEXPIRE TTL 自然过期。
	for index := 0; index < 2; index++ {
		if _, acquireErr := instance.AcquireAccountConcurrency(ctx, "acc-1", AccountConcurrencyLaneText, 120_000, 5, 5, 5); acquireErr != nil {
			t.Fatalf("第 %d 次 acquire 失败: %v", index+1, acquireErr)
		}
	}
	for index := 0; index < 2; index++ {
		if releaseErr := instance.ReleaseAccountConcurrency(ctx, "acc-1", AccountConcurrencyLaneText); releaseErr != nil {
			t.Fatalf("第 %d 次 release 失败: %v", index+1, releaseErr)
		}
	}
	if got := instance.CurrentAccountConcurrency("acc-1", ""); got != 0 {
		t.Fatalf("release 到 0 后并发读取 = %d, want 0", got)
	}
	states, err := instance.LoadAccountRPMByID(ctx, []string{"acc-1"})
	if err != nil {
		t.Fatal(err)
	}
	if states["acc-1"].Used != 2 || states["acc-1"].ResetAtMs != 1_860_000 {
		t.Fatalf("并发排空后 RPM 读取 = %+v, want 本分钟已用 2 与原窗口边界", states["acc-1"])
	}

	// 契约：下一分钟滚动后同一读取面按 0 返回（bucket 过期）。
	nowMs = 1_860_000
	states, err = instance.LoadAccountRPMByID(ctx, []string{"acc-1"})
	if err != nil {
		t.Fatal(err)
	}
	if states["acc-1"].Used != 0 {
		t.Fatalf("滚动后 RPM 读取 = %+v, want 0", states["acc-1"])
	}
}

func TestHighConcurrencyGroupQueueRPMLimitReadyGate(t *testing.T) {
	clock := newManualClock(time.UnixMilli(1_000_000))
	concurrency := newRecordingConcurrency()
	scheduler := &manualScheduler{clock: clock}
	queue, err := NewHighConcurrencyGroupQueue(HighConcurrencyQueueOptions{
		Clock:              clock,
		RuntimeStateDriver: RuntimeStateDriverMemory,
		Concurrency:        concurrency,
		Scheduler:          scheduler,
		PolicyDefaults:     HighConcurrencyPolicyDefaults{MaxQueueSize: 8, PerAPIKeyQueueLimit: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(queue.Close)
	ctx := context.Background()
	input := groupWaitInput("a1")
	input.AccountConcurrencyLimits = map[string]int{"a1": 4}
	input.AccountRPMLimits = map[string]int{"a1": 2}

	// 契约：并发有余、RPM 满 → 不走 immediate ready，进入排队域。
	concurrency.setRPM("a1", 2)
	live, stop := context.WithCancel(context.Background())
	defer stop()
	input.Signal = live
	waiter := make(chan HighConcurrencyQueueWaitResult, 1)
	go func() {
		result, waitErr := queue.WaitForHighConcurrencyGroupCapacity(ctx, input)
		if waitErr != nil {
			t.Error(waitErr)
			waiter <- HighConcurrencyQueueWaitResult{}
			return
		}
		waiter <- result
	}()
	groupKey := highConcurrencyGroupQueueKey("sys", "grp", AccountConcurrencyLaneText)
	enqueued := false
	for i := 0; i < 200; i++ {
		if queue.queueSizeOf(groupKey) == 1 {
			enqueued = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !enqueued {
		t.Fatal("RPM 满时请求应入队等待")
	}
	scheduler.advance(6 * time.Second)
	select {
	case result := <-waiter:
		if result.Ready || result.Reason != QueueRejectTimeout {
			t.Fatalf("RPM 满超时 result=%+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not resolved")
	}

	// 契约：窗口滚动重置（RPM 余量恢复）后同一请求形态立即 ready——RPM 重置
	// 没有槽释放事件，等待封顶后由外层轮次重新判定（设计 §5.2）。
	clock.advance(time.Minute)
	concurrency.setRPM("a1", 0)
	immediate, err := queue.WaitForHighConcurrencyGroupCapacity(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !immediate.Ready {
		t.Fatalf("RPM 重置后应立即 ready: %+v", immediate)
	}
}

// TestMemoryAccountConcurrencyUnlimitedRPMZeroMapFootprint：rpmLimit <= 0（默
// 认不限制）的热路径零开销契约——占位不读不写 RPM map（观测字段零值回带），
// 读取未配置账户不新增条目，Release 不触碰 RPM map。
func TestMemoryAccountConcurrencyUnlimitedRPMZeroMapFootprint(t *testing.T) {
	tracker := NewMemoryAccountConcurrency(nil)
	ctx := context.Background()
	for index := 0; index < 3; index++ {
		outcome := tracker.TryAcquire("acc-u", 0, AccountConcurrencyLaneText, 0, 0)
		if !outcome.Acquired || outcome.Total != index+1 || outcome.RPMCurrent != 0 || outcome.RPMLimit != 0 || outcome.RPMResetAtMs != 0 {
			t.Fatalf("未配置 RPM 的第 %d 次占位 = %+v, want 成功且 RPM 观测零值", index+1, outcome)
		}
	}
	if len(tracker.rpmBucket) != 0 || len(tracker.rpmCount) != 0 {
		t.Fatalf("rpmLimit=0 占位后 RPM map = %d/%d, want 均为 0", len(tracker.rpmBucket), len(tracker.rpmCount))
	}
	// 读取面：未配置账户按 0 返回且不新增条目。
	states, err := tracker.LoadAccountRPMByID(ctx, []string{"acc-u", "acc-none"})
	if err != nil {
		t.Fatal(err)
	}
	if states["acc-u"].Used != 0 || states["acc-none"].Used != 0 {
		t.Fatalf("未配置账户读取 = %v, want 双 0", states)
	}
	if len(tracker.rpmBucket) != 0 || len(tracker.rpmCount) != 0 {
		t.Fatalf("读取不应新增条目: %d/%d", len(tracker.rpmBucket), len(tracker.rpmCount))
	}
	tracker.Release("acc-u", AccountConcurrencyLaneText)
	if len(tracker.rpmBucket) != 0 || len(tracker.rpmCount) != 0 {
		t.Fatalf("Release 不应触碰 RPM map: %d/%d", len(tracker.rpmBucket), len(tracker.rpmCount))
	}
}

// TestMemoryAccountConcurrencyReadCleansExpiredWindow：读取面的惰性清理契约——
// 同窗口读取保留条目，跨分钟后按 0 返回且两个 RPM 条目被删除（map 不无限留存）。
func TestMemoryAccountConcurrencyReadCleansExpiredWindow(t *testing.T) {
	clock := newManualClock(time.UnixMilli(1_800_000))
	tracker := NewMemoryAccountConcurrency(clock)
	tracker.TryAcquire("acc-c", 0, "", 0, 5)
	if len(tracker.rpmBucket) != 1 || len(tracker.rpmCount) != 1 {
		t.Fatalf("已配置占位后 RPM map = %d/%d, want 各 1 条", len(tracker.rpmBucket), len(tracker.rpmCount))
	}
	// 同窗口读取保留条目与计数。
	states, err := tracker.LoadAccountRPMByID(context.Background(), []string{"acc-c"})
	if err != nil || states["acc-c"].Used != 1 {
		t.Fatalf("同窗口读取 = %v err %v, want 1", states, err)
	}
	if len(tracker.rpmBucket) != 1 || len(tracker.rpmCount) != 1 {
		t.Fatalf("同窗口读取不应清理条目: %d/%d", len(tracker.rpmBucket), len(tracker.rpmCount))
	}
	// 跨分钟读取：按 0 返回且两个条目被惰性清理。
	clock.advance(time.Minute)
	states, err = tracker.LoadAccountRPMByID(context.Background(), []string{"acc-c"})
	if err != nil || states["acc-c"].Used != 0 {
		t.Fatalf("跨分钟读取 = %v err %v, want 0", states, err)
	}
	if len(tracker.rpmBucket) != 0 || len(tracker.rpmCount) != 0 {
		t.Fatalf("跨分钟读取后条目应被清理: %d/%d", len(tracker.rpmBucket), len(tracker.rpmCount))
	}
}
