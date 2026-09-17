package gatewayclientip

// w13d 波次补充测试：高并发分组队列、Client-IP 并发槽（local + redis driver）、
// Redis 账户并发源与派发优先层级的分支覆盖。
//
// 本文件覆盖范围内的不可达语句登记（其余见 w13d_policy_circuit_misc_test.go）：
//   - concurrency.go:228-231（local queue_disabled）：resolveGroupSchedulingPolicy
//     对 maxQueueWaitMs 的下界是 1，policy.MaxQueueWaitMs 恒 >= 1。
//   - concurrency.go:382-384（release 队首 completed 短路）：completeQueuedItem /
//     acquired / abort / Clear 各路径都在锁内同步把 completed 条目移出 items。
//   - concurrency.go:500-502（redis queue_disabled）：同 228-231，恒 >= 1。
//   - concurrency.go:624-635（槽续租 ticker 循环体）：time.NewTicker 固定 30s，
//     无注入点，等待 30s 真实时间不可接受；续租 Lua 语义由
//     renewRedisClientIPSlot 的调用路径覆盖。
//   - concurrency.go:685-687（enqueue ttl<1 钳制）：ttl = deadline-now+60000，
//     同一次调用内 deadline > now，恒 >= 60000。
//   - concurrency.go:750-751（remove 空结果）：remove 脚本恒返回 {ZCARD} 单元素。
//   - queue.go:591-592（redis queue_disabled）：同 concurrency.go:228-231。
//   - queue.go:696-698（enqueue ttl<1 钳制）：同 concurrency.go:685-687。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

// w13dFailingConcurrency 在指定读取入口注入错误，其余委托给内嵌实现。
type w13dFailingConcurrency struct {
	*recordingConcurrency
	loadByIDErr error
	loadLaneErr error
}

func (f *w13dFailingConcurrency) LoadAccountCurrentConcurrencyByID(ctx context.Context, accountIDs []string) (map[string]int, error) {
	if f.loadByIDErr != nil {
		return nil, f.loadByIDErr
	}
	return f.recordingConcurrency.LoadAccountCurrentConcurrencyByID(ctx, accountIDs)
}

func (f *w13dFailingConcurrency) LoadAccountCurrentConcurrencyByLane(ctx context.Context, accountIDs []string, lane string) (map[string]int, error) {
	if f.loadLaneErr != nil {
		return nil, f.loadLaneErr
	}
	return f.recordingConcurrency.LoadAccountCurrentConcurrencyByLane(ctx, accountIDs, lane)
}

// ---------------------------------------------------------------------------
// constructor guards
// ---------------------------------------------------------------------------

func TestW13DQueueConstructorGuards(t *testing.T) {
	if _, err := NewHighConcurrencyGroupQueue(HighConcurrencyQueueOptions{}); err == nil {
		t.Fatal("缺 Concurrency 必须报错")
	}
	concurrency := newRecordingConcurrency()
	if _, err := NewHighConcurrencyGroupQueue(HighConcurrencyQueueOptions{
		Concurrency:        concurrency,
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "   ",
	}); err == nil {
		t.Fatal("redis driver 空 URL 必须报错")
	}
	if _, err := NewHighConcurrencyGroupQueue(HighConcurrencyQueueOptions{
		Concurrency:        concurrency,
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "://bad-url",
	}); err == nil {
		t.Fatal("redis driver 坏 URL 必须报错")
	}
	if _, err := NewClientIPConcurrency(ClientIPConcurrencyOptions{
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "://bad-url",
	}); err == nil {
		t.Fatal("ClientIPConcurrency 坏 URL 必须报错")
	}
}

// ---------------------------------------------------------------------------
// group queue: local driver branches
// ---------------------------------------------------------------------------

func TestW13DQueuePolicyValidationError(t *testing.T) {
	queue, _, _ := newTestGroupQueue(t, nil)
	input := groupWaitInput("a1")
	input.Policy = map[string]any{"maxQueueWaitMs": "abc"}
	if _, err := queue.WaitForHighConcurrencyGroupCapacity(context.Background(), input); err == nil {
		t.Fatal("非法 maxQueueWaitMs 必须报错")
	}
}

func TestW13DQueueLocalAbortedAndDoubleComplete(t *testing.T) {
	clock := newManualClock(time.UnixMilli(1_000_000))
	scheduler := &manualScheduler{clock: clock}
	concurrency := newRecordingConcurrency()
	concurrency.setTotal("a1", 1)
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

	// 入队前 signal 已取消 → aborted。
	signaled, cancel := context.WithCancel(context.Background())
	cancel()
	input := groupWaitInput("a1")
	input.AccountConcurrencyLimits = map[string]int{"a1": 1}
	input.Signal = signaled
	result, err := queue.WaitForHighConcurrencyGroupCapacity(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready || result.Reason != QueueRejectAborted {
		t.Fatalf("result=%+v", result)
	}

	// 排队（signal 存活）→ timer 超时完成；signal watcher 在 item.done 上退出。
	live, stop := context.WithCancel(context.Background())
	defer stop()
	input.Signal = live
	waiter := make(chan HighConcurrencyQueueWaitResult, 1)
	go func() {
		result, err := queue.WaitForHighConcurrencyGroupCapacity(context.Background(), input)
		if err != nil {
			t.Error(err)
			waiter <- HighConcurrencyQueueWaitResult{}
			return
		}
		waiter <- result
	}()
	groupKey := highConcurrencyGroupQueueKey("sys", "grp", AccountConcurrencyLaneText)
	for i := 0; i < 200; i++ {
		if queue.queueSizeOf(groupKey) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	scheduler.advance(6 * time.Second)
	select {
	case result := <-waiter:
		if result.Ready || result.Reason != QueueRejectTimeout {
			t.Fatalf("timeout result=%+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not resolved")
	}
	// timer 已消费后重放 → completeGroupQueueItem 幂等短路。
	scheduler.advance(6 * time.Second)
	// 超时完成后 state 已清空：size 查询走 missing-key 分支。
	if size := queue.queueSizeOf("missing"); size != 0 {
		t.Fatalf("missing queue size=%d", size)
	}
	if size := queue.perAPIKeyQueueSizeOf("missing", "key"); size != 0 {
		t.Fatalf("missing per api key size=%d", size)
	}
}

func TestW13DQueueLocalPerAPIKeyCountDecrement(t *testing.T) {
	queue, concurrency, _ := newTestGroupQueue(t, nil)
	input := groupWaitInput("a1")
	input.AccountConcurrencyLimits = map[string]int{"a1": 1}
	concurrency.setTotal("a1", 1)
	waiter := make(chan HighConcurrencyQueueWaitResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			result, err := queue.WaitForHighConcurrencyGroupCapacity(context.Background(), input)
			if err != nil {
				t.Error(err)
				waiter <- HighConcurrencyQueueWaitResult{}
				return
			}
			waiter <- result
		}()
	}
	groupKey := highConcurrencyGroupQueueKey("sys", "grp", AccountConcurrencyLaneText)
	for i := 0; i < 400; i++ {
		if queue.queueSizeOf(groupKey) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	concurrency.emit("a1", AccountConcurrencyLaneText)
	<-waiter
	// 第二个仍在排队：per-api-key 计数从 2 递减到 1（>0 保留路径）。
	state := queue.queues[groupKey]
	if state == nil || state.perAPIKeyCount["key"] != 1 {
		t.Fatalf("per api key count=%+v", state)
	}
	concurrency.emit("a1", AccountConcurrencyLaneText)
	second := <-waiter
	if !second.Ready {
		t.Fatalf("second=%+v", second)
	}
}

func TestW13DQueueWakeCandidateSearch(t *testing.T) {
	queue, concurrency, _ := newTestGroupQueue(t, nil)
	ctx := context.Background()

	// 无任何队列时发布 release → findQueueWakeCandidate index 缺失 + 无候选返回。
	concurrency.emit("ghost", AccountConcurrencyLaneText)

	// image lane 队列 + text lane 队列都含 a1：text release 时遍历跳过
	// image lane 的 state（lane 不匹配），命中 text 队列。
	imageInput := groupWaitInput("a1")
	imageInput.Lane = AccountConcurrencyLaneImage
	imageInput.AccountConcurrencyLimits = map[string]int{"a1": 1}
	textInput := groupWaitInput("a1")
	textInput.AccountConcurrencyLimits = map[string]int{"a1": 1}
	concurrency.setTotal("a1", 1)
	concurrency.lane["a1:"+AccountConcurrencyLaneImage] = 1

	startWaiter := func(input HighConcurrencyQueueWaitInput) chan HighConcurrencyQueueWaitResult {
		waiter := make(chan HighConcurrencyQueueWaitResult, 1)
		go func() {
			result, err := queue.WaitForHighConcurrencyGroupCapacity(ctx, input)
			if err != nil {
				t.Error(err)
				waiter <- HighConcurrencyQueueWaitResult{}
				return
			}
			waiter <- result
		}()
		return waiter
	}
	imageWaiter := startWaiter(imageInput)
	textWaiter := startWaiter(textInput)
	groupKeyText := highConcurrencyGroupQueueKey("sys", "grp", AccountConcurrencyLaneText)
	groupKeyImage := highConcurrencyGroupQueueKey("sys", "grp", AccountConcurrencyLaneImage)
	for i := 0; i < 400; i++ {
		if queue.queueSizeOf(groupKeyText) == 1 && queue.queueSizeOf(groupKeyImage) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if queue.queueSizeOf(groupKeyText) != 1 || queue.queueSizeOf(groupKeyImage) != 1 {
		t.Fatalf("queues not ready: text=%d image=%d", queue.queueSizeOf(groupKeyText), queue.queueSizeOf(groupKeyImage))
	}
	// text 释放唤醒 text 队列；遍历会经过 image state（lane continue）。
	concurrency.emit("a1", AccountConcurrencyLaneText)
	result := <-textWaiter
	if !result.Ready {
		t.Fatalf("text wake=%+v", result)
	}
	// image 释放：fallback lane 切到 text（releasedLane==image 分支），命中
	// image 队列。
	concurrency.lane["a1:"+AccountConcurrencyLaneImage] = 0
	concurrency.emit("a1", AccountConcurrencyLaneImage)
	result = <-imageWaiter
	if !result.Ready {
		t.Fatalf("image wake=%+v", result)
	}
}

func TestW13DQueueWakeCandidateSkips(t *testing.T) {
	queue, concurrency, _ := newTestGroupQueue(t, nil)
	ctx := context.Background()
	// 同 group 排两个 item：第一个只含 a3（对 a1 不在 candidates → 跳过），
	// 第二个含 a1（可唤醒）。
	first := groupWaitInput("a3")
	first.AccountConcurrencyLimits = map[string]int{"a3": 1, "a1": 1}
	second := groupWaitInput("a1")
	second.AccountConcurrencyLimits = map[string]int{"a3": 1, "a1": 1}
	concurrency.setTotal("a1", 1)
	concurrency.setTotal("a3", 1)
	firstWaiter := make(chan HighConcurrencyQueueWaitResult, 1)
	go func() {
		result, err := queue.WaitForHighConcurrencyGroupCapacity(ctx, first)
		if err != nil {
			t.Error(err)
			firstWaiter <- HighConcurrencyQueueWaitResult{}
			return
		}
		firstWaiter <- result
	}()
	secondWaiter := make(chan HighConcurrencyQueueWaitResult, 1)
	go func() {
		result, err := queue.WaitForHighConcurrencyGroupCapacity(ctx, second)
		if err != nil {
			t.Error(err)
			secondWaiter <- HighConcurrencyQueueWaitResult{}
			return
		}
		secondWaiter <- result
	}()
	groupKey := highConcurrencyGroupQueueKey("sys", "grp", AccountConcurrencyLaneText)
	for i := 0; i < 400; i++ {
		if queue.queueSizeOf(groupKey) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if queue.queueSizeOf(groupKey) != 2 {
		t.Fatalf("queue size=%d", queue.queueSizeOf(groupKey))
	}
	// 释放 a1：第一 item 不含 a1（candidates 跳过），第二 item 命中。
	concurrency.emit("a1", AccountConcurrencyLaneText)
	result := <-secondWaiter
	if !result.Ready {
		t.Fatalf("wake=%+v", result)
	}
	// 释放 a3：第一 item 命中。
	concurrency.emit("a3", AccountConcurrencyLaneText)
	result = <-firstWaiter
	if !result.Ready {
		t.Fatalf("a3 wake=%+v", result)
	}
}

func TestW13DQueueWakeCandidateCapacityDenied(t *testing.T) {
	// hardLimit 仍满 → queueItemCanAcquireAfterRelease false → 无候选返回。
	queue, concurrency, _ := newTestGroupQueue(t, nil)
	input := groupWaitInput("a1")
	input.AccountConcurrencyLimits = map[string]int{"a1": 1}
	concurrency.setTotal("a1", 2)
	waiter := make(chan HighConcurrencyQueueWaitResult, 1)
	go func() {
		result, err := queue.WaitForHighConcurrencyGroupCapacity(context.Background(), input)
		if err != nil {
			t.Error(err)
			waiter <- HighConcurrencyQueueWaitResult{}
			return
		}
		waiter <- result
	}()
	groupKey := highConcurrencyGroupQueueKey("sys", "grp", AccountConcurrencyLaneText)
	for i := 0; i < 400; i++ {
		if queue.queueSizeOf(groupKey) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// 发布 release 但同步计数仍 >= hardLimit → 不可唤醒。
	concurrency.emit("a1", AccountConcurrencyLaneText)
	select {
	case result := <-waiter:
		t.Fatalf("不该被唤醒: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}
	// 真实释放后可唤醒。
	concurrency.setTotal("a1", 0)
	concurrency.emit("a1", AccountConcurrencyLaneText)
	result := <-waiter
	if !result.Ready {
		t.Fatalf("wake=%+v", result)
	}
}

func TestW13DQueueHasImmediateCapacitySkips(t *testing.T) {
	queue, concurrency, _ := newTestGroupQueue(t, nil)
	// 空 accountID 与重复 accountID 在 immediate 容量判断中跳过，a1 可用。
	input := groupWaitInput("", "a1", "a1")
	input.AccountConcurrencyLimits = map[string]int{"a1": 1}
	concurrency.setTotal("a1", 0)
	result, err := queue.WaitForHighConcurrencyGroupCapacity(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready {
		t.Fatalf("result=%+v", result)
	}
	// 容量空缺（limits 无该账号）→ immediate true。
	unknown := groupWaitInput("ghost")
	result, err = queue.WaitForHighConcurrencyGroupCapacity(context.Background(), unknown)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready {
		t.Fatalf("unknown account result=%+v", result)
	}
}

func TestW13DQueueRedisSnapshotAndClear(t *testing.T) {
	server := miniredis.RunT(t)
	queue, _, _ := newTestGroupQueue(t, func(opts *HighConcurrencyQueueOptions) {
		opts.RuntimeStateDriver = RuntimeStateDriverRedis
		opts.StateRedisURL = "redis://" + server.Addr()
	})
	// redis driver 下 Snapshot 恒 nil。
	if rows := queue.Snapshot(); rows != nil {
		t.Fatalf("redis snapshot=%+v", rows)
	}
	// redis driver 的 Clear 走本地 map 清空分支。
	queue.Clear()
	if len(queue.queues) != 0 || len(queue.index) != 0 {
		t.Fatal("clear 后必须为空")
	}
	// queueSizeLocked / perAPIKeyQueueSizeLocked 的 missing-key 分支。
	if size := queue.queueSizeLocked("missing"); size != 0 {
		t.Fatalf("size=%d", size)
	}
	if size := queue.perAPIKeyQueueSizeLocked("missing", "key"); size != 0 {
		t.Fatalf("per api key=%d", size)
	}
}

// ---------------------------------------------------------------------------
// group queue: redis driver wait loop
// ---------------------------------------------------------------------------

func newW13DRedisGroupQueue(
	t *testing.T,
	server *miniredis.Miniredis,
	clock *manualClock,
	concurrency AccountConcurrencySource,
	sleep func(time.Duration),
) *HighConcurrencyGroupQueue {
	t.Helper()
	queue, err := NewHighConcurrencyGroupQueue(HighConcurrencyQueueOptions{
		Clock:              clock,
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "redis://" + server.Addr(),
		RedisNamespace:     "w13d",
		Concurrency:        concurrency,
		Sleep:              sleep,
		PolicyDefaults:     HighConcurrencyPolicyDefaults{MaxQueueSize: 8, PerAPIKeyQueueLimit: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(queue.Close)
	return queue
}

func (q *HighConcurrencyGroupQueue) w13dRedisQueueSize(groupKey string) int {
	if q.redis == nil {
		return 0
	}
	size, err := q.redis.ZCard(context.Background(), redisHighConcurrencyGroupQueueKey(q.redisNamespace(), groupKey)).Result()
	if err != nil {
		return 0
	}
	return int(size)
}

func TestW13DQueueRedisWaitLoopImmediateReady(t *testing.T) {
	server := miniredis.RunT(t)
	clock := newManualClock(time.UnixMilli(1_000_000))
	concurrency := newRecordingConcurrency()
	concurrency.setTotal("a1", 1)
	queue := newW13DRedisGroupQueue(t, server, clock, concurrency, func(time.Duration) {})
	ctx := context.Background()

	// signal 已取消 → 直接 aborted。
	signaled, cancel := context.WithCancel(context.Background())
	cancel()
	input := groupWaitInput("a1")
	input.AccountConcurrencyLimits = map[string]int{"a1": 1}
	input.MaxWaitMs = int64ptr(5_000)
	input.Signal = signaled
	result, err := queue.WaitForHighConcurrencyGroupCapacity(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready || result.Reason != QueueRejectAborted {
		t.Fatalf("aborted=%+v", result)
	}

	// 排队后放开容量 → 首轮轮询 rank==0 + immediate → ready。
	input.Signal = nil
	readyWaiter := make(chan HighConcurrencyQueueWaitResult, 1)
	go func() {
		result, err := queue.WaitForHighConcurrencyGroupCapacity(ctx, input)
		if err != nil {
			t.Error(err)
			readyWaiter <- HighConcurrencyQueueWaitResult{}
			return
		}
		readyWaiter <- result
	}()
	groupKey := highConcurrencyGroupQueueKey("sys", "grp", AccountConcurrencyLaneText)
	for i := 0; i < 400; i++ {
		if queue.w13dRedisQueueSize(groupKey) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	concurrency.setTotal("a1", 0)
	select {
	case result := <-readyWaiter:
		if !result.Ready || result.QueueSizeBeforeWake != 1 {
			t.Fatalf("ready=%+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ready waiter not resolved")
	}
}

func TestW13DQueueRedisWaitLoopDeadlineTimeout(t *testing.T) {
	server := miniredis.RunT(t)
	clock := newManualClock(time.UnixMilli(1_000_000))
	concurrency := newRecordingConcurrency()
	concurrency.setTotal("a1", 1)
	queue := newW13DRedisGroupQueue(t, server, clock, concurrency, func(d time.Duration) { clock.advance(d) })
	input := groupWaitInput("a1")
	input.AccountConcurrencyLimits = map[string]int{"a1": 1}
	input.MaxWaitMs = int64ptr(1_000)
	result, err := queue.WaitForHighConcurrencyGroupCapacity(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready || result.Reason != QueueRejectTimeout {
		t.Fatalf("timeout=%+v", result)
	}
}

func TestW13DQueueRedisWaitLoopAbortedAndRemoved(t *testing.T) {
	server := miniredis.RunT(t)
	clock := newManualClock(time.UnixMilli(1_000_000))
	concurrency := newRecordingConcurrency()
	concurrency.setTotal("a1", 1)
	// Sleep 空转且不推进时钟：循环等待 signal，行为确定。
	queue := newW13DRedisGroupQueue(t, server, clock, concurrency, func(time.Duration) {})
	ctx := context.Background()
	input := groupWaitInput("a1")
	input.AccountConcurrencyLimits = map[string]int{"a1": 1}
	input.MaxWaitMs = int64ptr(60_000)

	// 中途 signal 取消 → remove + aborted。
	signaled, stop := context.WithCancel(context.Background())
	input.Signal = signaled
	waiter := make(chan HighConcurrencyQueueWaitResult, 1)
	go func() {
		result, err := queue.WaitForHighConcurrencyGroupCapacity(ctx, input)
		if err != nil {
			t.Error(err)
			waiter <- HighConcurrencyQueueWaitResult{}
			return
		}
		waiter <- result
	}()
	groupKey := highConcurrencyGroupQueueKey("sys", "grp", AccountConcurrencyLaneText)
	for i := 0; i < 400; i++ {
		if queue.w13dRedisQueueSize(groupKey) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	select {
	case result := <-waiter:
		if result.Ready || result.Reason != QueueRejectAborted {
			t.Fatalf("aborted=%+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("aborted waiter not resolved")
	}

	// 排队后手动移除 group 队列（他人消费）→ position 不在 → timeout。
	input.Signal = nil
	timeoutWaiter := make(chan HighConcurrencyQueueWaitResult, 1)
	go func() {
		result, err := queue.WaitForHighConcurrencyGroupCapacity(ctx, input)
		if err != nil {
			t.Error(err)
			timeoutWaiter <- HighConcurrencyQueueWaitResult{}
			return
		}
		timeoutWaiter <- result
	}()
	for i := 0; i < 400; i++ {
		if queue.w13dRedisQueueSize(groupKey) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	server.Del(redisHighConcurrencyGroupQueueKey("w13d", groupKey))
	select {
	case result := <-timeoutWaiter:
		if result.Ready || result.Reason != QueueRejectTimeout {
			t.Fatalf("timeout=%+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiter not resolved")
	}
}

func TestW13DQueueRedisWaitLoopLoadErrors(t *testing.T) {
	server := miniredis.RunT(t)
	clock := newManualClock(time.UnixMilli(1_000_000))
	concurrency := &w13dFailingConcurrency{
		recordingConcurrency: newRecordingConcurrency(),
		loadByIDErr:          errors.New("读取账户并发失败"),
	}
	queue := newW13DRedisGroupQueue(t, server, clock, concurrency, func(time.Duration) {})
	input := groupWaitInput("a1")
	input.AccountConcurrencyLimits = map[string]int{"a1": 1}
	if _, err := queue.WaitForHighConcurrencyGroupCapacity(context.Background(), input); err == nil {
		t.Fatal("LoadByID 错误必须透传")
	}
	// LoadByID 错误在 lane 判定前透传。
	imageInput := groupWaitInput("a1")
	imageInput.Lane = AccountConcurrencyLaneImage
	imageInput.AccountConcurrencyLimits = map[string]int{"a1": 1}
	if _, err := queue.WaitForHighConcurrencyGroupCapacity(context.Background(), imageInput); err == nil {
		t.Fatal("LoadByID 错误必须先透传")
	}
	concurrency.loadByIDErr = nil
	concurrency.loadLaneErr = errors.New("读取 lane 并发失败")
	// image lane 时 immediate 判定追加 lane 计数加载 → lane 错误透传。
	if _, err := queue.WaitForHighConcurrencyGroupCapacity(context.Background(), imageInput); err == nil {
		t.Fatal("LoadByLane 错误必须透传")
	}
}

func TestW13DQueueRedisWaitLoopRedisError(t *testing.T) {
	server := miniredis.RunT(t)
	clock := newManualClock(time.UnixMilli(1_000_000))
	concurrency := newRecordingConcurrency()
	concurrency.setTotal("a1", 1)
	queue := newW13DRedisGroupQueue(t, server, clock, concurrency, func(time.Duration) {})
	input := groupWaitInput("a1")
	input.AccountConcurrencyLimits = map[string]int{"a1": 1}
	server.SetError("模拟故障")
	if _, err := queue.WaitForHighConcurrencyGroupCapacity(context.Background(), input); err == nil {
		t.Fatal("redis 故障必须透传")
	}
}

func int64ptr(value int64) *int64 { return &value }

// ---------------------------------------------------------------------------
// client-ip concurrency: local driver branches
// ---------------------------------------------------------------------------

func w13dClientIPQueuePolicy() map[string]any {
	return map[string]any{
		"clientIpConcurrencyLimit":        float64(1),
		"clientIpConcurrencyOverflowMode": "queue",
		"maxQueueWaitMs":                  float64(2_000),
	}
}

func TestW13DClientIPConcurrencyLocalEdges(t *testing.T) {
	clock := newManualClock(time.UnixMilli(1_000_000))
	scheduler := &manualScheduler{clock: clock}
	concurrency, err := NewClientIPConcurrency(ClientIPConcurrencyOptions{
		Clock:     clock,
		Scheduler: scheduler,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(concurrency.Close)
	ctx := context.Background()
	input := ClientIPConcurrencyAcquireInput{
		SystemAccountID: "sys", GroupID: "grp", APIKeyID: "key", ClientIP: "10.0.0.1",
		Policy: w13dClientIPQueuePolicy(),
	}

	// 直接获取。
	decision, err := concurrency.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Acquired {
		t.Fatalf("decision=%+v", decision)
	}
	// 释放不存在的 key（内部路径）。
	concurrency.releaseClientIPSlot("missing")
	// apiKeyID 空 → key 的 api-key 段落 internal。
	key := clientIPConcurrencyKey("sys", "grp", "", "10.0.0.2")
	if !strings.HasSuffix(key, ":internal:10.0.0.2") {
		t.Fatalf("key=%q", key)
	}
	decision.Release()
	decision.Release() // release 幂等。
}

func TestW13DClientIPConcurrencyLocalQueueTimeout(t *testing.T) {
	clock := newManualClock(time.UnixMilli(1_000_000))
	scheduler := &manualScheduler{clock: clock}
	concurrency, err := NewClientIPConcurrency(ClientIPConcurrencyOptions{
		Clock:     clock,
		Scheduler: scheduler,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(concurrency.Close)
	ctx := context.Background()
	input := ClientIPConcurrencyAcquireInput{
		SystemAccountID: "sys", GroupID: "grp", APIKeyID: "key", ClientIP: "10.0.0.1",
		Policy: w13dClientIPQueuePolicy(),
	}
	first, err := concurrency.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Acquired {
		t.Fatalf("first=%+v", first)
	}
	// signal 存活排队 → timer 超时完成（watcher 在 done 上退出）。
	live, stop := context.WithCancel(context.Background())
	defer stop()
	input.Signal = live
	waiter := make(chan ClientIPConcurrencyDecision, 1)
	go func() {
		decision, err := concurrency.AcquireHighConcurrencyClientIPSlot(ctx, input)
		if err != nil {
			t.Error(err)
			waiter <- ClientIPConcurrencyDecision{}
			return
		}
		waiter <- decision
	}()
	time.Sleep(50 * time.Millisecond)
	scheduler.advance(3 * time.Second)
	select {
	case decision := <-waiter:
		if decision.Acquired || decision.Reason != RejectTimeout {
			t.Fatalf("timeout=%+v", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not resolved")
	}
	// 重放 timer → completeQueuedItem 幂等短路。
	scheduler.advance(3 * time.Second)
	first.Release()
}

func TestW13DClientIPConcurrencyLocalQueueWakes(t *testing.T) {
	clock := newManualClock(time.UnixMilli(1_000_000))
	scheduler := &manualScheduler{clock: clock}
	concurrency, err := NewClientIPConcurrency(ClientIPConcurrencyOptions{
		Clock:     clock,
		Scheduler: scheduler,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(concurrency.Close)
	ctx := context.Background()
	input := ClientIPConcurrencyAcquireInput{
		SystemAccountID: "sys", GroupID: "grp", APIKeyID: "key", ClientIP: "10.0.0.1",
		Policy: w13dClientIPQueuePolicy(),
	}
	first, err := concurrency.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Acquired {
		t.Fatalf("first=%+v", first)
	}
	waiter := make(chan ClientIPConcurrencyDecision, 1)
	go func() {
		decision, err := concurrency.AcquireHighConcurrencyClientIPSlot(ctx, input)
		if err != nil {
			t.Error(err)
			waiter <- ClientIPConcurrencyDecision{}
			return
		}
		waiter <- decision
	}()
	time.Sleep(50 * time.Millisecond)
	first.Release()
	select {
	case decision := <-waiter:
		if !decision.Acquired || !decision.Queued {
			t.Fatalf("woken=%+v", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not woken")
	}
}

func TestW13DClientIPConcurrencySnapshotAndClearRedis(t *testing.T) {
	server := miniredis.RunT(t)
	concurrency, err := NewClientIPConcurrency(ClientIPConcurrencyOptions{
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "redis://" + server.Addr(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(concurrency.Close)
	if rows := concurrency.Snapshot(); rows != nil {
		t.Fatalf("redis snapshot=%+v", rows)
	}
	concurrency.Clear()
}

// ---------------------------------------------------------------------------
// client-ip concurrency: redis driver acquire loop
// ---------------------------------------------------------------------------

type w13dRedisAcquireFixture struct {
	server      *miniredis.Miniredis
	clock       *manualClock
	concurrency *ClientIPConcurrency
}

func newW13DRedisAcquireFixture(t *testing.T, onSleep func()) *w13dRedisAcquireFixture {
	t.Helper()
	server := miniredis.RunT(t)
	clock := newManualClock(time.UnixMilli(1_000_000))
	concurrency, err := NewClientIPConcurrency(ClientIPConcurrencyOptions{
		Clock:              clock,
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "redis://" + server.Addr(),
		Sleep: func(d time.Duration) {
			clock.advance(d)
			if onSleep != nil {
				onSleep()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(concurrency.Close)
	return &w13dRedisAcquireFixture{server: server, clock: clock, concurrency: concurrency}
}

func w13dRedisAcquirePolicy(overflow string) map[string]any {
	return map[string]any{
		"clientIpConcurrencyLimit":        float64(1),
		"clientIpConcurrencyOverflowMode": overflow,
		"maxQueueWaitMs":                  float64(1_000),
	}
}

func TestW13DClientIPConcurrencyRedisAcquirePaths(t *testing.T) {
	ctx := context.Background()
	input := ClientIPConcurrencyAcquireInput{
		SystemAccountID: "sys", GroupID: "grp", APIKeyID: "key", ClientIP: "10.0.0.1",
	}
	concurrencyKey := redisClientIPConcurrencyKey(clientIPConcurrencyKey("sys", "grp", "key", "10.0.0.1"))
	queueKey := redisClientIPQueueKey(clientIPConcurrencyKey("sys", "grp", "key", "10.0.0.1"))

	// 直接获取成功 → release 清理。
	direct := newW13DRedisAcquireFixture(t, nil)
	input.Policy = w13dRedisAcquirePolicy("reject")
	decision, err := direct.concurrency.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Acquired {
		t.Fatalf("direct=%+v", decision)
	}
	decision.Release()
	input.Policy = w13dRedisAcquirePolicy("queue")

	// 已占用 + reject → limit_reached。
	input.Policy = w13dRedisAcquirePolicy("reject")
	full := newW13DRedisAcquireFixture(t, nil)
	if err := full.concurrency.redis.ZAdd(ctx, concurrencyKey, redis.Z{
		Score: float64(full.clock.Now().UnixMilli() + 100_000), Member: "occupied",
	}).Err(); err != nil {
		t.Fatal(err)
	}
	decision, err = full.concurrency.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Acquired || decision.Reason != RejectLimitReached {
		t.Fatalf("limit_reached=%+v", decision)
	}

	// 排队但队列已满 → queue_full。
	input.Policy = w13dRedisAcquirePolicy("queue")
	queueFull := newW13DRedisAcquireFixture(t, nil)
	if err := queueFull.concurrency.redis.ZAdd(ctx, queueKey, redis.Z{
		Score: float64(queueFull.clock.Now().UnixMilli() + 100_000), Member: "queued-1",
	}).Err(); err != nil {
		t.Fatal(err)
	}
	decision, err = queueFull.concurrency.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Acquired || decision.Reason != RejectQueueFull {
		t.Fatalf("queue_full=%+v", decision)
	}

	// 排队后 signal 取消 → remove + aborted（Sleep 空转，循环等 signal）。
	aborted := newW13DRedisAcquireFixture(t, nil)
	if err := aborted.concurrency.redis.ZAdd(ctx, concurrencyKey, redis.Z{
		Score: float64(aborted.clock.Now().UnixMilli() + 100_000), Member: "occupied",
	}).Err(); err != nil {
		t.Fatal(err)
	}
	signaled, stop := context.WithCancel(context.Background())
	input.Signal = signaled
	abortedWaiter := make(chan ClientIPConcurrencyDecision, 1)
	go func() {
		decision, err := aborted.concurrency.AcquireHighConcurrencyClientIPSlot(ctx, input)
		if err != nil {
			t.Error(err)
			abortedWaiter <- ClientIPConcurrencyDecision{}
			return
		}
		abortedWaiter <- decision
	}()
	for i := 0; i < 400; i++ {
		size, _ := aborted.concurrency.redis.ZCard(ctx, queueKey).Result()
		if size > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	select {
	case decision := <-abortedWaiter:
		if decision.Acquired || decision.Reason != RejectAborted {
			t.Fatalf("aborted=%+v", decision)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("aborted waiter not resolved")
	}
	input.Signal = nil

	// 排队条目在轮询后消失 → position 不在 → timeout。
	removed := newW13DRedisAcquireFixture(t, nil)
	removed.concurrency.redis.ZAdd(ctx, concurrencyKey, redis.Z{
		Score: float64(removed.clock.Now().UnixMilli() + 100_000), Member: "occupied",
	})
	removedOnSleepRan := false
	removed.concurrency.sleep = func(d time.Duration) {
		removed.clock.advance(d)
		if !removedOnSleepRan {
			removedOnSleepRan = true
			return
		}
		// 第一轮轮询后移除整个队列，模拟条目被其他实例消费。
		_ = removed.concurrency.redis.Del(ctx, queueKey).Err()
	}
	decision, err = removed.concurrency.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Acquired || decision.Reason != RejectTimeout {
		t.Fatalf("timeout=%+v", decision)
	}

	// rank==0 时尝试获取成功 → queued ready。
	queuedReady := newW13DRedisAcquireFixture(t, nil)
	if err := queuedReady.concurrency.redis.ZAdd(ctx, concurrencyKey, redis.Z{
		Score: float64(queuedReady.clock.Now().UnixMilli() + 100_000), Member: "occupied",
	}).Err(); err != nil {
		t.Fatal(err)
	}
	readyOnSleep := func() {
		// 排队后清空占用槽，下一轮轮询 acquisition 成功。
		_ = queuedReady.concurrency.redis.Del(ctx, concurrencyKey).Err()
	}
	queuedReady.concurrency.sleep = func(d time.Duration) {
		queuedReady.clock.advance(d)
		readyOnSleep()
	}
	decision, err = queuedReady.concurrency.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Acquired || !decision.Queued {
		t.Fatalf("queued ready=%+v", decision)
	}
	decision.Release()

	// 容量始终不足 → deadline 耗尽 → timeout。
	deadline := newW13DRedisAcquireFixture(t, nil)
	if err := deadline.concurrency.redis.ZAdd(ctx, concurrencyKey, redis.Z{
		Score: float64(1 << 40), Member: "occupied",
	}).Err(); err != nil {
		t.Fatal(err)
	}
	decision, err = deadline.concurrency.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Acquired || decision.Reason != RejectTimeout {
		t.Fatalf("deadline timeout=%+v", decision)
	}
}

func TestW13DClientIPConcurrencyRedisErrors(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	clock := newManualClock(time.UnixMilli(1_000_000))
	concurrency, err := NewClientIPConcurrency(ClientIPConcurrencyOptions{
		Clock:              clock,
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "redis://" + server.Addr(),
		Sleep:              func(time.Duration) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(concurrency.Close)
	input := ClientIPConcurrencyAcquireInput{
		SystemAccountID: "sys", GroupID: "grp", APIKeyID: "key", ClientIP: "10.0.0.1",
		Policy: w13dRedisAcquirePolicy("queue"),
	}
	server.SetError("模拟故障")
	if _, err := concurrency.AcquireHighConcurrencyClientIPSlot(ctx, input); err == nil {
		t.Fatal("redis 故障必须透传")
	}
	// 清除故障后同请求成功（直接获取路径）。
	server.SetError("")
	decision, err := concurrency.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatalf("恢复后必须成功: %v", err)
	}
	decision.Release()
}

// ---------------------------------------------------------------------------
// redis account concurrency store
// ---------------------------------------------------------------------------

func TestW13DRedisAccountConcurrencyStore(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	source, err := NewRedisAccountConcurrency(RedisAccountConcurrencyOptions{
		Client: client, Namespace: "w13d", Name: "gateway-account-concurrency",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(source.Close)
	ctx := context.Background()

	// 空 ID 列表早退。
	if values, err := source.LoadAccountCurrentConcurrencyByID(ctx, nil); err != nil || len(values) != 0 {
		t.Fatalf("empty=%+v err=%v", values, err)
	}
	if values, err := source.LoadAccountCurrentConcurrencyByLane(ctx, nil, AccountConcurrencyLaneImage); err != nil || len(values) != 0 {
		t.Fatalf("empty lane=%+v err=%v", values, err)
	}

	// 写入计数后读取。
	if err := source.AcquireAccountConcurrency(ctx, "a1", "", 60_000); err != nil {
		t.Fatal(err)
	}
	if err := source.AcquireAccountConcurrency(ctx, "a1", AccountConcurrencyLaneImage, 60_000); err != nil {
		t.Fatal(err)
	}
	if got := source.CurrentAccountConcurrency("a1", ""); got != 1 {
		t.Fatalf("current=%d", got)
	}
	if got := source.CurrentAccountConcurrency("a1", AccountConcurrencyLaneImage); got != 1 {
		t.Fatalf("lane current=%d", got)
	}
	if got := source.CurrentAccountConcurrency("", ""); got != 0 {
		t.Fatalf("empty account=%d", got)
	}
	byIDs, err := source.LoadAccountCurrentConcurrencyByID(ctx, []string{"a1", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if byIDs["a1"] != 2 || byIDs["missing"] != 0 {
		t.Fatalf("byIDs=%+v", byIDs)
	}
	byLanes, err := source.LoadAccountCurrentConcurrencyByLane(ctx, []string{"a1"}, AccountConcurrencyLaneImage)
	if err != nil {
		t.Fatal(err)
	}
	if byLanes["a1"] != 1 {
		t.Fatalf("byLanes=%+v", byLanes)
	}

	// 释放到 0 删除 key（total 归零后整 key 删除）。
	if err := source.ReleaseAccountConcurrency(ctx, "a1", ""); err != nil {
		t.Fatal(err)
	}
	// total 仍剩 image 计数：total=1，text 归零后 Current 读 text=0。
	if got := source.CurrentAccountConcurrency("a1", ""); got != 0 {
		t.Fatalf("after text release=%d", got)
	}
	byIDsAfter, err := source.LoadAccountCurrentConcurrencyByID(ctx, []string{"a1"})
	if err != nil {
		t.Fatal(err)
	}
	if byIDsAfter["a1"] != 1 {
		t.Fatalf("total after text release=%d", byIDsAfter["a1"])
	}
	if err := source.ReleaseAccountConcurrency(ctx, "a1", AccountConcurrencyLaneImage); err != nil {
		t.Fatal(err)
	}
	if got := source.CurrentAccountConcurrency("a1", AccountConcurrencyLaneImage); got != 0 {
		t.Fatalf("after image release=%d", got)
	}

	// redis 故障：Load 透传错误、Current 归零。
	server.SetError("模拟故障")
	if _, err := source.LoadAccountCurrentConcurrencyByID(ctx, []string{"a1"}); err == nil {
		t.Fatal("LoadByID 故障必须透传")
	}
	if _, err := source.LoadAccountCurrentConcurrencyByLane(ctx, []string{"a1"}, AccountConcurrencyLaneImage); err == nil {
		t.Fatal("LoadByLane 故障必须透传")
	}
	if got := source.CurrentAccountConcurrency("a1", ""); got != 0 {
		t.Fatalf("故障时 current=%d", got)
	}
	// 订阅 stub 直接可用。
	unsubscribe := source.SubscribeAccountConcurrencyRelease(func(AccountConcurrencyReleaseEvent) {})
	unsubscribe()
}

func TestW13DRedisAccountConcurrencyURLGuards(t *testing.T) {
	if _, err := NewRedisAccountConcurrency(RedisAccountConcurrencyOptions{RedisURL: "  "}); err == nil {
		t.Fatal("空 URL 必须报错")
	}
	if _, err := NewRedisAccountConcurrency(RedisAccountConcurrencyOptions{RedisURL: "://bad"}); err == nil {
		t.Fatal("坏 URL 必须报错")
	}
}

// ---------------------------------------------------------------------------
// priority order tiers
// ---------------------------------------------------------------------------

func TestW13DDispatchPriorityTierOrdering(t *testing.T) {
	account := func(id string, priority int, fallback, super bool) gatewayruntimecache.OpenAIAccountSecret {
		return gatewayruntimecache.OpenAIAccountSecret{
			ID: id, Priority: priority, FallbackEnabled: fallback, SuperPriorityEnabled: super,
		}
	}
	base := []gatewayruntimecache.OpenAIAccountSecret{
		account("a1", 10, false, true),
		account("a2", 20, true, false),
	}
	reordered := []gatewayruntimecache.OpenAIAccountSecret{base[1], base[0]}
	options := DispatchPriorityTierInput{ModelRankByAccountID: map[string]int{"a1": -3, "a2": 2, "ghost": 9}}
	merged := PreserveGatewayAccountDispatchPriorityTiers(base, reordered, options)
	if len(merged) != 2 {
		t.Fatalf("merged=%+v", merged)
	}
	// tier 字符串由 rank/fallback/super/priority 组成。
	tier := GatewayAccountDispatchPriorityTier(base[0], DispatchPriorityTierInput{})
	if tier == "" {
		t.Fatal("tier 不能为空")
	}
	// unknown tier：reordered 含 base 不存在的 tier 组合。
	unknown := []gatewayruntimecache.OpenAIAccountSecret{account("a3", 30, true, true)}
	merged = PreserveGatewayAccountDispatchPriorityTiers(base, unknown, options)
	if len(merged) != 1 || merged[0].ID != "a3" {
		t.Fatalf("unknown tier merged=%+v", merged)
	}
	// 少于 2 个账号直接返回副本。
	single := PreserveGatewayAccountDispatchPriorityTiers(base[:1], base[:1], options)
	if len(single) != 1 || single[0].ID != "a1" {
		t.Fatalf("single=%+v", single)
	}
	// nil model rank → 0。
	if got := gatewayAccountDispatchModelRank("a1", DispatchPriorityTierInput{}); got != 0 {
		t.Fatalf("nil rank=%d", got)
	}
	// 未知账号 → 3。
	if got := gatewayAccountDispatchModelRank("nobody", options); got != unknownGatewayDispatchModelRank {
		t.Fatalf("unknown rank=%d", got)
	}
	// 负值 rank → 0。
	if got := gatewayAccountDispatchModelRank("a1", options); got != 0 {
		t.Fatalf("negative rank=%d", got)
	}
}

// ---------------------------------------------------------------------------
// runtime state store
// ---------------------------------------------------------------------------

func TestW13DMemoryStateStoreDecodeError(t *testing.T) {
	store := NewMemoryRuntimeStateStore(newManualClock(time.UnixMilli(1_000_000)))
	if err := store.SetJSON(context.Background(), "w13d-key", map[string]int{"n": 1}, 60_000); err != nil {
		t.Fatal(err)
	}
	var target []string
	if _, err := store.GetJSON(context.Background(), "w13d-key", &target); err == nil {
		t.Fatal("类型不匹配必须报错")
	}
}

func TestW13DRedisStateStoreNilContext(t *testing.T) {
	server := miniredis.RunT(t)
	store, closeFn, err := NewRedisRuntimeStateStore("redis://"+server.Addr(), "w13d", "gateway-client-ip-error-circuit")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	// ctx=nil 走 run 的 nil 分支。
	if err := store.SetJSON(nil, "w13d-key", map[string]string{"a": "b"}, 60_000); err != nil {
		t.Fatal(err)
	}
	var target map[string]string
	found, err := store.GetJSON(nil, "w13d-key", &target)
	if err != nil || !found || target["a"] != "b" {
		t.Fatalf("found=%v err=%v target=%+v", found, err, target)
	}
	if err := store.Delete(nil, "w13d-key"); err != nil {
		t.Fatal(err)
	}
}
