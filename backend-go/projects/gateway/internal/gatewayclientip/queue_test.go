package gatewayclientip

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func newTestGroupQueue(t *testing.T, mutate func(*HighConcurrencyQueueOptions)) (*HighConcurrencyGroupQueue, *recordingConcurrency, *manualClock) {
	t.Helper()
	clock := newManualClock(time.UnixMilli(1_000_000))
	concurrency := newRecordingConcurrency()
	opts := HighConcurrencyQueueOptions{
		Clock:              clock,
		RuntimeStateDriver: RuntimeStateDriverMemory,
		Concurrency:        concurrency,
		Scheduler:          &manualScheduler{clock: clock},
		PolicyDefaults:     HighConcurrencyPolicyDefaults{MaxQueueSize: 8, PerAPIKeyQueueLimit: 8},
	}
	if mutate != nil {
		mutate(&opts)
	}
	queue, err := NewHighConcurrencyGroupQueue(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(queue.Close)
	return queue, concurrency, clock
}

func groupWaitInput(accountIDs ...string) HighConcurrencyQueueWaitInput {
	return HighConcurrencyQueueWaitInput{
		SystemAccountID: "sys", GroupID: "grp", APIKeyID: "key",
		AccountIDs: accountIDs,
		Policy:     map[string]any{"maxQueueWaitMs": float64(5_000), "maxQueueSize": float64(4), "perApiKeyQueueLimit": float64(4)},
	}
}

func TestHighConcurrencyGroupQueueImmediateCapacity(t *testing.T) {
	queue, concurrency, _ := newTestGroupQueue(t, nil)
	ctx := context.Background()
	// 无容量配置（capacities 对每个 account 都有默认 hardLimit=1，Node
	// buildAccountCapacities 同款）：current=0 < 1 → 放行。
	result, err := queue.WaitForHighConcurrencyGroupCapacity(ctx, groupWaitInput("a1"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || result.WaitedMs != 0 || result.QueueSizeBeforeWake != 0 {
		t.Fatalf("result=%+v", result)
	}
	// 有余量 → 放行：hardLimit 默认 1，current=0 < 1。
	result, err = queue.WaitForHighConcurrencyGroupCapacity(ctx, groupWaitInput("a1"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready {
		t.Fatalf("result=%+v", result)
	}
	// 显式 limits 下的余量。
	limited := groupWaitInput("a1")
	limited.AccountConcurrencyLimits = map[string]int{"a1": 2}
	concurrency.setTotal("a1", 1)
	result, err = queue.WaitForHighConcurrencyGroupCapacity(ctx, limited)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready {
		t.Fatalf("limited result=%+v", result)
	}
	// 打满 hard limit 且无队列空间语义由 Reject/HardLimit 用例覆盖。
}

func TestHighConcurrencyGroupQueueHardLimitBlocksAndWakes(t *testing.T) {
	queue, concurrency, _ := newTestGroupQueue(t, nil)
	ctx := context.Background()
	input := groupWaitInput("a1")
	input.AccountConcurrencyLimits = map[string]int{"a1": 2}
	policy := map[string]any{"maxQueueWaitMs": float64(5_000), "maxQueueSize": float64(4), "perApiKeyQueueLimit": float64(4)}
	input.Policy = policy
	concurrency.setTotal("a1", 2) // 打满 hard limit → 排队。

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
	time.Sleep(20 * time.Millisecond)
	rows := queue.Snapshot()
	if len(rows) != 1 || rows[0].QueueSize != 1 || rows[0].Lane != AccountConcurrencyLaneText {
		t.Fatalf("snapshot=%+v", rows)
	}
	if rows[0].PerAPIKeyQueueSize["key"] != 1 {
		t.Fatalf("per api key=%+v", rows[0].PerAPIKeyQueueSize)
	}
	// 释放账户 → 唤醒。
	concurrency.emit("a1", AccountConcurrencyLaneText)
	select {
	case result := <-waiter:
		if !result.Ready || result.QueueSizeBeforeWake != 1 {
			t.Fatalf("woken=%+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not woken")
	}
	if rows := queue.Snapshot(); len(rows) != 0 {
		t.Fatalf("queue must drain: %+v", rows)
	}
}

func TestHighConcurrencyGroupQueueRejectTable(t *testing.T) {
	// Node 顺序：先判 immediate（队列空 + 有余量 → ready），再判
	// queue_disabled / queue_full / api_key_queue_full —— 拒绝路径都要求
	// 无 immediate 容量或队列已存在。
	t.Run("queue_disabled via maxWaitMs 0 without capacity", func(t *testing.T) {
		queue, concurrency, _ := newTestGroupQueue(t, nil)
		input := groupWaitInput("a1")
		input.AccountConcurrencyLimits = map[string]int{"a1": 1}
		zero := int64(0)
		input.MaxWaitMs = &zero
		concurrency.setTotal("a1", 10)
		result, err := queue.WaitForHighConcurrencyGroupCapacity(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if result.Ready || result.Reason != QueueRejectQueueDisabled {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("queue_full after occupied slot", func(t *testing.T) {
		queue, concurrency, _ := newTestGroupQueue(t, nil)
		input := groupWaitInput("a1")
		input.AccountConcurrencyLimits = map[string]int{"a1": 1}
		// perApiKeyQueueLimit 上限是 maxQueueSize（Node resolvePerApiKeyQueueLimit）。
		input.Policy = map[string]any{"maxQueueWaitMs": float64(30_000), "maxQueueSize": float64(1), "perApiKeyQueueLimit": float64(1)}
		// current == hardLimit → 无 immediate；释放后 0 < 1 可唤醒。
		concurrency.setTotal("a1", 1)
		occupied := make(chan HighConcurrencyQueueWaitResult, 1)
		go func() {
			result, err := queue.WaitForHighConcurrencyGroupCapacity(context.Background(), input)
			if err != nil {
				t.Error(err)
				occupied <- HighConcurrencyQueueWaitResult{}
				return
			}
			occupied <- result
		}()
		time.Sleep(20 * time.Millisecond)
		result, err := queue.WaitForHighConcurrencyGroupCapacity(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if result.Ready || result.Reason != QueueRejectQueueFull || result.QueueSize != 1 {
			t.Fatalf("result=%+v", result)
		}
		concurrency.emit("a1", AccountConcurrencyLaneText)
		woken := <-occupied
		if !woken.Ready {
			t.Fatalf("woken=%+v", woken)
		}
	})

	t.Run("api_key_queue_full after occupied slot", func(t *testing.T) {
		queue, concurrency, _ := newTestGroupQueue(t, nil)
		input := groupWaitInput("a1")
		input.AccountConcurrencyLimits = map[string]int{"a1": 1}
		input.Policy = map[string]any{"maxQueueWaitMs": float64(30_000), "maxQueueSize": float64(4), "perApiKeyQueueLimit": float64(1)}
		concurrency.setTotal("a1", 1)
		occupied := make(chan HighConcurrencyQueueWaitResult, 1)
		go func() {
			result, err := queue.WaitForHighConcurrencyGroupCapacity(context.Background(), input)
			if err != nil {
				t.Error(err)
				occupied <- HighConcurrencyQueueWaitResult{}
				return
			}
			occupied <- result
		}()
		time.Sleep(20 * time.Millisecond)
		result, err := queue.WaitForHighConcurrencyGroupCapacity(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if result.Ready || result.Reason != QueueRejectAPIKeyQueueFull || result.PerAPIKeyQueueSize != 1 {
			t.Fatalf("result=%+v", result)
		}
		concurrency.emit("a1", AccountConcurrencyLaneText)
		woken := <-occupied
		if !woken.Ready {
			t.Fatalf("woken=%+v", woken)
		}
	})
}

func TestHighConcurrencyGroupQueuePerAPIKeyAccounting(t *testing.T) {
	queue, concurrency, _ := newTestGroupQueue(t, nil)
	ctx := context.Background()
	input := groupWaitInput("a1")
	input.AccountConcurrencyLimits = map[string]int{"a1": 1}
	input.Policy = map[string]any{"maxQueueWaitMs": float64(30_000), "maxQueueSize": float64(4), "perApiKeyQueueLimit": float64(1)}
	concurrency.setTotal("a1", 1)
	// 两个 key 各排一队，验证 per-api-key 计数。
	other := input
	other.APIKeyID = "other"
	results := make(chan HighConcurrencyQueueWaitResult, 2)
	go func() {
		result, err := queue.WaitForHighConcurrencyGroupCapacity(ctx, input)
		if err != nil {
			t.Error(err)
			results <- HighConcurrencyQueueWaitResult{}
			return
		}
		results <- result
	}()
	go func() {
		result, err := queue.WaitForHighConcurrencyGroupCapacity(ctx, other)
		if err != nil {
			t.Error(err)
			results <- HighConcurrencyQueueWaitResult{}
			return
		}
		results <- result
	}()
	time.Sleep(20 * time.Millisecond)
	rows := queue.Snapshot()
	if len(rows) != 1 || rows[0].QueueSize != 2 || rows[0].PerAPIKeyQueueSize["key"] != 1 || rows[0].PerAPIKeyQueueSize["other"] != 1 {
		t.Fatalf("snapshot=%+v", rows)
	}
	// 第三个同 key 超过 per-api-key 限制 → api_key_queue_full。
	third, err := queue.WaitForHighConcurrencyGroupCapacity(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if third.Ready || third.Reason != QueueRejectAPIKeyQueueFull || third.PerAPIKeyQueueSize != 1 {
		t.Fatalf("third=%+v", third)
	}
	// 两次释放各唤醒一个排队者（Node 每次释放只唤醒一个候选）。
	concurrency.emit("a1", AccountConcurrencyLaneText)
	concurrency.emit("a1", AccountConcurrencyLaneText)
	first := <-results
	second := <-results
	if !first.Ready || !second.Ready {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func TestHighConcurrencyGroupQueueTimeoutAndAbort(t *testing.T) {
	queue, concurrency, clock := newTestGroupQueue(t, nil)
	scheduler := &manualScheduler{clock: clock}
	queue.sched = scheduler
	ctx := context.Background()
	input := groupWaitInput("a1")
	input.AccountConcurrencyLimits = map[string]int{"a1": 1}
	input.Policy = map[string]any{"maxQueueWaitMs": float64(2_000), "maxQueueSize": float64(4), "perApiKeyQueueLimit": float64(4)}
	concurrency.setTotal("a1", 1)

	resultCh := make(chan HighConcurrencyQueueWaitResult, 1)
	go func() {
		result, err := queue.WaitForHighConcurrencyGroupCapacity(ctx, input)
		if err != nil {
			t.Error(err)
			resultCh <- HighConcurrencyQueueWaitResult{}
			return
		}
		resultCh <- result
	}()
	time.Sleep(20 * time.Millisecond)
	scheduler.advance(2 * time.Second)
	result := <-resultCh
	if result.Ready || result.Reason != QueueRejectTimeout {
		t.Fatalf("timeout=%+v", result)
	}

	// abort。
	signalCtx, cancel := context.WithCancel(ctx)
	input.Signal = signalCtx
	abortCh := make(chan HighConcurrencyQueueWaitResult, 1)
	go func() {
		result, err := queue.WaitForHighConcurrencyGroupCapacity(ctx, input)
		if err != nil {
			t.Error(err)
			abortCh <- HighConcurrencyQueueWaitResult{}
			return
		}
		abortCh <- result
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	aborted := <-abortCh
	if aborted.Ready || aborted.Reason != QueueRejectAborted {
		t.Fatalf("aborted=%+v", aborted)
	}
	// 清理：直接清空队列。
	queue.Clear()
}

func TestHighConcurrencyGroupQueueImageLane(t *testing.T) {
	queue, concurrency, _ := newTestGroupQueue(t, nil)
	ctx := context.Background()
	input := groupWaitInput("a1")
	input.Lane = AccountConcurrencyLaneImage
	input.AccountConcurrencyLimits = map[string]int{"a1": 3}
	input.Policy = map[string]any{"maxQueueWaitMs": float64(5_000), "imageLaneMaxConcurrency": float64(2)}
	concurrency.lane["a1:image"] = 2 // image lane 打满（hard 3 未满）→ 排队。

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
	time.Sleep(20 * time.Millisecond)
	if rows := queue.Snapshot(); len(rows) != 1 || rows[0].Lane != AccountConcurrencyLaneImage {
		t.Fatalf("snapshot=%+v", rows)
	}
	// image lane 释放 → 唤醒。
	concurrency.emit("a1", AccountConcurrencyLaneImage)
	select {
	case result := <-waiter:
		if !result.Ready {
			t.Fatalf("woken=%+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not woken")
	}
}

func TestHighConcurrencyGroupQueueRedisMode(t *testing.T) {
	server := miniredis.RunT(t)
	concurrency := newRecordingConcurrency()
	concurrency.setTotal("a1", 5)
	queue, err := NewHighConcurrencyGroupQueue(HighConcurrencyQueueOptions{
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "redis://" + server.Addr(),
		RedisNamespace:     "dev",
		Concurrency:        concurrency,
		Sleep:              func(time.Duration) {},
		PolicyDefaults:     HighConcurrencyPolicyDefaults{MaxQueueSize: 4, PerAPIKeyQueueLimit: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(queue.Close)
	concurrency.setTotal("a1", 1)
	ctx := context.Background()
	input := groupWaitInput("a1")
	input.AccountConcurrencyLimits = map[string]int{"a1": 1}

	// queue_disabled（maxWaitMs=0 覆盖）。
	disabled := input
	zero := int64(0)
	disabled.MaxWaitMs = &zero
	result, err := queue.WaitForHighConcurrencyGroupCapacity(ctx, disabled)
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready || result.Reason != QueueRejectQueueDisabled {
		t.Fatalf("disabled=%+v", result)
	}

	// 排队 → 释放容量 → 轮询循环内拿到 rank0 + immediate → ready。
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
	time.Sleep(50 * time.Millisecond)
	wantKey := namespacedStateKey("dev", highConcurrencyQueueKeyFamily+"sys:grp:text")
	if !server.Exists(wantKey) {
		t.Fatalf("redis queue key missing: %s", wantKey)
	}
	concurrency.setTotal("a1", 0)
	select {
	case result := <-waiter:
		if !result.Ready || result.QueueSizeBeforeWake < 1 {
			t.Fatalf("woken=%+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not woken")
	}
	if server.Exists(wantKey) {
		t.Fatal("queue zset must be removed on wake")
	}
}

func TestGatewaySettingsAvoidanceTTLFieldWiring(t *testing.T) {
	// 与 gatewayruntimecache.GatewaySettings 的字段接线保持可编译核对。
	settings := gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 3}
	if avoidanceTTL(&settings) != 180_000 {
		t.Fatalf("ttl=%d", avoidanceTTL(&settings))
	}
}

func int64Ptr(value int64) *int64 { return &value }
