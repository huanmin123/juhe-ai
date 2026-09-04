package gatewayclientip

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
)

func newTestConcurrency(t *testing.T, mutate func(*ClientIPConcurrencyOptions)) (*ClientIPConcurrency, *manualClock, *manualScheduler) {
	t.Helper()
	clock := newManualClock(time.UnixMilli(1_000_000))
	scheduler := &manualScheduler{clock: clock}
	opts := ClientIPConcurrencyOptions{
		Clock:              clock,
		RuntimeStateDriver: RuntimeStateDriverMemory,
		Scheduler:          scheduler,
		PolicyDefaults: HighConcurrencyPolicyDefaults{
			MaxQueueSize:        16,
			PerAPIKeyQueueLimit: 16,
		},
	}
	if mutate != nil {
		mutate(&opts)
	}
	slots, err := NewClientIPConcurrency(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(slots.Close)
	return slots, clock, scheduler
}

func baseAcquireInput(ip string) ClientIPConcurrencyAcquireInput {
	return ClientIPConcurrencyAcquireInput{
		SystemAccountID: "sys", GroupID: "grp", APIKeyID: "key", ClientIP: ip,
	}
}

func TestAcquireHighConcurrencyClientIPSlotDisabledTable(t *testing.T) {
	tests := []struct {
		name   string
		policy map[string]any
		ip     string
	}{
		{name: "blank ip", policy: map[string]any{"clientIpConcurrencyLimit": float64(3)}, ip: "  "},
		{name: "limit 0 default", policy: nil, ip: "10.0.0.1"},
		{name: "explicit limit 0", policy: map[string]any{"clientIpConcurrencyLimit": float64(0)}, ip: "10.0.0.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			slots, _, _ := newTestConcurrency(t, nil)
			input := baseAcquireInput(tc.ip)
			input.Policy = tc.policy
			decision, err := slots.AcquireHighConcurrencyClientIPSlot(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Enabled || !decision.Acquired {
				t.Fatalf("disabled path must return {enabled:false, acquired:true}: %+v", decision)
			}
			decision.Release() // noop must not panic
		})
	}
}

func TestClientIPConcurrencyLocalSlotLifecycle(t *testing.T) {
	slots, _, _ := newTestConcurrency(t, nil)
	policy := map[string]any{
		"clientIpConcurrencyLimit":      float64(2),
		"clientIpConcurrencyOverflowMode": "reject",
	}
	ctx := context.Background()
	input := baseAcquireInput("10.0.0.1")
	input.Policy = policy

	first, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Acquired || first.Current != 1 || first.Limit != 2 || first.WaitedMs != 0 || first.Queued {
		t.Fatalf("first=%+v", first)
	}
	second, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Acquired || second.Current != 2 {
		t.Fatalf("second=%+v", second)
	}
	// 高水位：limit_reached。
	third, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if third.Acquired || third.Reason != RejectLimitReached || third.Current != 2 || third.Limit != 2 {
		t.Fatalf("third=%+v", third)
	}
	// 释放 → 可再获取，计数回落。
	second.Release()
	second.Release() // 幂等
	fourth, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !fourth.Acquired || fourth.Current != 2 {
		t.Fatalf("fourth=%+v", fourth)
	}
	first.Release()
	fourth.Release()
	if rows := slots.Snapshot(); len(rows) != 0 {
		t.Fatalf("idle states must be cleaned: %+v", rows)
	}
}

func TestClientIPConcurrencyLocalQueueWakesFIFO(t *testing.T) {
	slots, _, _ := newTestConcurrency(t, nil)
	policy := map[string]any{
		"clientIpConcurrencyLimit":        float64(1),
		"clientIpConcurrencyOverflowMode": "queue",
		"maxQueueWaitMs":                  float64(5_000),
		"perApiKeyQueueLimit":             float64(4),
	}
	ctx := context.Background()
	input := baseAcquireInput("10.0.0.2")
	input.Policy = policy

	first, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Acquired {
		t.Fatalf("first=%+v", first)
	}
	// 两个排队者：a 先 b 后，释放后必须按 FIFO 唤醒。
	type wait struct {
		decision ClientIPConcurrencyDecision
		err      error
	}
	a := make(chan wait, 1)
	b := make(chan wait, 1)
	go func() {
		inputA := input
		decision, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, inputA)
		a <- wait{decision, err}
	}()
	time.Sleep(20 * time.Millisecond)
	go func() {
		inputB := input
		decision, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, inputB)
		b <- wait{decision, err}
	}()
	time.Sleep(20 * time.Millisecond)
	rows := slots.Snapshot()
	if len(rows) != 1 || rows[0].QueueSize != 2 || rows[0].Current != 1 {
		t.Fatalf("snapshot=%+v", rows)
	}
	first.Release()
	resA := <-a
	if resA.err != nil {
		t.Fatal(resA.err)
	}
	if !resA.decision.Acquired || !resA.decision.Queued {
		t.Fatalf("a=%+v", resA.decision)
	}
	if resA.decision.QueueSizeBeforeAcquire != 2 {
		t.Fatalf("queueSizeBeforeAcquire=%d want 2（Node 取移除前长度）", resA.decision.QueueSizeBeforeAcquire)
	}
	select {
	case res := <-b:
		t.Fatalf("b 不得被提前唤醒: %+v", res.decision)
	default:
	}
	resA.decision.Release()
	resB := <-b
	if !resB.decision.Acquired || !resB.decision.Queued {
		t.Fatalf("b=%+v", resB.decision)
	}
	resB.decision.Release()
}

func TestClientIPConcurrencyLocalQueueFullAndDisabled(t *testing.T) {
	slots, _, _ := newTestConcurrency(t, nil)
	policy := map[string]any{
		"clientIpConcurrencyLimit":        float64(1),
		"clientIpConcurrencyOverflowMode": "queue",
		"maxQueueWaitMs":                  float64(5_000),
		"perApiKeyQueueLimit":             float64(1),
	}
	ctx := context.Background()
	input := baseAcquireInput("10.0.0.3")
	input.Policy = policy
	first, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Acquired {
		t.Fatal("first must acquire")
	}
	// 占满唯一排队位（异步等待）。
	done := make(chan ClientIPConcurrencyDecision, 1)
	go func() {
		decision, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
		if err != nil {
			t.Error(err)
			done <- ClientIPConcurrencyDecision{}
			return
		}
		done <- decision
	}()
	time.Sleep(20 * time.Millisecond)
	queued, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Acquired || queued.Reason != RejectQueueFull || queued.QueueSize != 1 {
		t.Fatalf("queued=%+v", queued)
	}
	// queue_disabled 分支在 Node 中依赖 resolve 后的 <=0 值；本服务的
	// resolveGroupSchedulingPolicy 对 maxQueueWaitMs 强制 1..3600000，
	// 传入 0 与 Node 一致地抛错（queue_disabled 只出现在组队列的
	// maxWaitMs 覆盖路径）。
	disabledInput := baseAcquireInput("10.0.0.4")
	disabledInput.Policy = map[string]any{
		"clientIpConcurrencyLimit":        float64(1),
		"clientIpConcurrencyOverflowMode": "queue",
		"maxQueueWaitMs":                  float64(0),
	}
	if _, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, disabledInput); err == nil ||
		err.Error() != "分组调度策略 maxQueueWaitMs 必须在 1-3600000 之间" {
		t.Fatalf("err=%v", err)
	}
	// overflowMode reject（默认）：limit_reached 而非排队。
	rejectInput := baseAcquireInput("10.0.0.5")
	rejectInput.Policy = map[string]any{"clientIpConcurrencyLimit": float64(1)}
	one, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, rejectInput)
	if err != nil {
		t.Fatal(err)
	}
	two, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, rejectInput)
	if err != nil {
		t.Fatal(err)
	}
	if two.Acquired || two.Reason != RejectLimitReached {
		t.Fatalf("two=%+v", two)
	}
	one.Release()
	two.Release()
	// 释放 10.0.0.3 的占位 → 唤醒排队 waiter。
	first.Release()
	woken := <-done
	if !woken.Acquired || !woken.Queued {
		t.Fatalf("woken=%+v", woken)
	}
	woken.Release()
}

func TestClientIPConcurrencyLocalTimeoutAndAbort(t *testing.T) {
	slots, clock, scheduler := newTestConcurrency(t, nil)
	policy := map[string]any{
		"clientIpConcurrencyLimit":        float64(1),
		"clientIpConcurrencyOverflowMode": "queue",
		"maxQueueWaitMs":                  float64(2_000),
	}
	ctx := context.Background()
	input := baseAcquireInput("10.0.0.6")
	input.Policy = policy
	first, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Acquired {
		t.Fatal("first must acquire")
	}
	result := make(chan ClientIPConcurrencyDecision, 1)
	go func() {
		decision, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
		if err != nil {
			t.Error(err)
			result <- ClientIPConcurrencyDecision{}
			return
		}
		result <- decision
	}()
	time.Sleep(20 * time.Millisecond)
	// 超时：调度器推进 maxQueueWaitMs。
	scheduler.advance(2 * time.Second)
	decision := <-result
	if decision.Acquired || decision.Reason != RejectTimeout {
		t.Fatalf("timeout=%+v", decision)
	}
	if decision.WaitedMs < 1_000 {
		t.Fatalf("waitedMs=%d", decision.WaitedMs)
	}

	// abort：带 signal 的排队请求在取消时被拒绝（first 保持占位）。
	signalCtx, cancel := context.WithCancel(ctx)
	abortInput := input
	abortInput.Signal = signalCtx
	abortResult := make(chan ClientIPConcurrencyDecision, 1)
	go func() {
		decision, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, abortInput)
		if err != nil {
			t.Error(err)
			abortResult <- ClientIPConcurrencyDecision{}
			return
		}
		abortResult <- decision
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	aborted := <-abortResult
	if aborted.Acquired || aborted.Reason != RejectAborted {
		t.Fatalf("aborted=%+v", aborted)
	}
	if rows := slots.Snapshot(); len(rows) != 0 && (rows[0].Current != 1 || rows[0].QueueSize != 0) {
		t.Fatalf("snapshot=%+v", rows)
	}
	first.Release()
	// 已取消的 signal 直接快速失败。
	fastInput := input
	fastInput.Signal = signalCtx
	fast, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, fastInput)
	if err != nil {
		t.Fatal(err)
	}
	if fast.Acquired || fast.Reason != RejectAborted || fast.Current != 0 || fast.QueueSize != 0 {
		t.Fatalf("fast=%+v", fast)
	}
	clock.advance(time.Millisecond)
}

func TestClientIPConcurrencyClearCompletesWaiters(t *testing.T) {
	slots, _, _ := newTestConcurrency(t, nil)
	policy := map[string]any{
		"clientIpConcurrencyLimit":        float64(1),
		"clientIpConcurrencyOverflowMode": "queue",
		"maxQueueWaitMs":                  float64(60_000),
	}
	ctx := context.Background()
	input := baseAcquireInput("10.0.0.7")
	input.Policy = policy
	first, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Acquired {
		t.Fatal("first must acquire")
	}
	result := make(chan ClientIPConcurrencyDecision, 1)
	go func() {
		decision, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
		if err != nil {
			t.Error(err)
			result <- ClientIPConcurrencyDecision{}
			return
		}
		result <- decision
	}()
	time.Sleep(20 * time.Millisecond)
	slots.Clear()
	decision := <-result
	if decision.Acquired || decision.Reason != RejectAborted || decision.Limit != 1 {
		t.Fatalf("clear=%+v", decision)
	}
}

func TestClientIPConcurrencyRedisMode(t *testing.T) {
	server := miniredis.RunT(t)
	slots, err := NewClientIPConcurrency(ClientIPConcurrencyOptions{
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "redis://" + server.Addr(),
		Sleep:              func(time.Duration) {},
		PolicyDefaults:     HighConcurrencyPolicyDefaults{MaxQueueSize: 4, PerAPIKeyQueueLimit: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(slots.Close)
	ctx := context.Background()
	input := baseAcquireInput("10.0.0.8")
	input.Policy = map[string]any{
		"clientIpConcurrencyLimit":      float64(1),
		"clientIpConcurrencyOverflowMode": "reject",
	}
	first, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Acquired || first.Current != 1 {
		t.Fatalf("first=%+v", first)
	}
	rejected, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Acquired || rejected.Reason != RejectLimitReached {
		t.Fatalf("rejected=%+v", rejected)
	}
	// key 布局：juhe-ai:client-ip-concurrency:<base64url>（Node 原样前缀，无 namespace 段）。
	key := redisClientIPConcurrencyKey(clientIPConcurrencyKey("sys", "grp", "key", "10.0.0.8"))
	if !server.Exists(key) {
		t.Fatalf("redis key missing: %s", key)
	}
	first.Release()
	if server.Exists(key) {
		t.Fatal("release must delete the empty zset")
	}
	// 排队：占位后第二个在 poll 循环中被唤醒。
	input.Policy = map[string]any{
		"clientIpConcurrencyLimit":        float64(1),
		"clientIpConcurrencyOverflowMode": "queue",
		"maxQueueWaitMs":                  float64(5_000),
	}
	holder, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !holder.Acquired {
		t.Fatalf("holder=%+v", holder)
	}
	waiterDone := make(chan ClientIPConcurrencyDecision, 1)
	go func() {
		decision, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
		if err != nil {
			t.Error(err)
			waiterDone <- ClientIPConcurrencyDecision{}
			return
		}
		waiterDone <- decision
	}()
	time.Sleep(50 * time.Millisecond)
	queueKey := redisClientIPQueueKey(clientIPConcurrencyKey("sys", "grp", "key", "10.0.0.8"))
	if !server.Exists(queueKey) {
		t.Fatal("queue zset missing")
	}
	holder.Release()
	select {
	case decision := <-waiterDone:
		if !decision.Acquired || !decision.Queued {
			t.Fatalf("waiter=%+v", decision)
		}
		decision.Release()
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not woken")
	}
}

func TestClientIPConcurrencyRedisQueueFullAndMissingURL(t *testing.T) {
	server := miniredis.RunT(t)
	slots, err := NewClientIPConcurrency(ClientIPConcurrencyOptions{
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "redis://" + server.Addr(),
		Sleep:              func(time.Duration) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(slots.Close)
	ctx := context.Background()
	input := baseAcquireInput("10.0.0.9")
	input.Policy = map[string]any{
		"clientIpConcurrencyLimit":        float64(1),
		"clientIpConcurrencyOverflowMode": "queue",
		"maxQueueWaitMs":                  float64(5_000),
		"perApiKeyQueueLimit":             float64(1),
	}
	holder, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !holder.Acquired {
		t.Fatal("holder must acquire")
	}
	waiter := make(chan ClientIPConcurrencyDecision, 1)
	go func() {
		decision, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
		if err != nil {
			t.Error(err)
			waiter <- ClientIPConcurrencyDecision{}
			return
		}
		waiter <- decision
	}()
	time.Sleep(50 * time.Millisecond)
	full, err := slots.AcquireHighConcurrencyClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if full.Acquired || full.Reason != RejectQueueFull {
		t.Fatalf("full=%+v", full)
	}
	holder.Release()
	<-waiter

	// URL 缺失：构造期报 Node 错误文案。
	if _, err := NewClientIPConcurrency(ClientIPConcurrencyOptions{
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      " ",
	}); err == nil || err.Error() != "JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置" {
		t.Fatalf("err=%v", err)
	}
}
