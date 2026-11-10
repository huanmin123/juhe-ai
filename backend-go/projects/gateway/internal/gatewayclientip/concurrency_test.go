package gatewayclientip

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayscheduling"
)

func newTestConcurrency(t *testing.T, mutate func(*ClientIPConcurrencyOptions)) (*ClientIPConcurrency, *manualClock) {
	t.Helper()
	clock := newManualClock(time.UnixMilli(1_000_000))
	opts := ClientIPConcurrencyOptions{
		Clock:              clock,
		RuntimeStateDriver: RuntimeStateDriverMemory,
	}
	if mutate != nil {
		mutate(&opts)
	}
	slots, err := NewClientIPConcurrency(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(slots.Close)
	return slots, clock
}

func baseAcquireInput(ip string) ClientIPConcurrencyAcquireInput {
	return ClientIPConcurrencyAcquireInput{
		SystemAccountID: "sys", GroupID: "grp", APIKeyID: "key", ClientIP: ip,
	}
}

func ipGatePolicy(limit int) *gatewayscheduling.Policy {
	return &gatewayscheduling.Policy{DefaultSoftConcurrency: -1, ClientIPConcurrencyLimit: limit}
}

// 普通分组 IP 闸口关闭臂：nil 策略、显式 0 与空白 IP 都是 disabled 快速通道。
func TestAcquireClientIPSlotDisabledTable(t *testing.T) {
	tests := []struct {
		name   string
		policy *gatewayscheduling.Policy
		ip     string
	}{
		{name: "blank ip", policy: ipGatePolicy(3), ip: "  "},
		{name: "nil policy", policy: nil, ip: "10.0.0.1"},
		{name: "limit 0 default", policy: &gatewayscheduling.Policy{}, ip: "10.0.0.1"},
		{name: "explicit limit 0", policy: ipGatePolicy(0), ip: "10.0.0.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			slots, _ := newTestConcurrency(t, nil)
			input := baseAcquireInput(tc.ip)
			input.Policy = tc.policy
			decision, err := slots.AcquireClientIPSlot(context.Background(), input)
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

// 本地闸口生命周期：0→>0 启用、满额 reject、释放恢复、空闲态清理。
func TestClientIPConcurrencyLocalSlotLifecycle(t *testing.T) {
	slots, _ := newTestConcurrency(t, nil)
	ctx := context.Background()
	// 0 上限（默认策略）关闭；换非零策略后同键立即生效（禁用→启用边界）。
	closed := baseAcquireInput("10.0.0.1")
	closed.Policy = &gatewayscheduling.Policy{}
	if decision, err := slots.AcquireClientIPSlot(ctx, closed); err != nil || decision.Enabled {
		t.Fatalf("closed=%+v err=%v", decision, err)
	}
	input := baseAcquireInput("10.0.0.1")
	input.Policy = ipGatePolicy(2)

	first, err := slots.AcquireClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Acquired || first.Current != 1 || first.Limit != 2 || first.WaitedMs != 0 || first.Queued {
		t.Fatalf("first=%+v", first)
	}
	second, err := slots.AcquireClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Acquired || second.Current != 2 {
		t.Fatalf("second=%+v", second)
	}
	// 高水位：limit_reached 即时拒绝（不排队、不 429 文案差异，由派发面统一渲染）。
	third, err := slots.AcquireClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if third.Acquired || third.Reason != RejectLimitReached || third.Current != 2 || third.Limit != 2 {
		t.Fatalf("third=%+v", third)
	}
	if third.Queued || third.QueueSize != 0 || third.QueueSizeBeforeAcquire != 0 {
		t.Fatalf("queue fields must stay zero: %+v", third)
	}
	// 释放 → 可再获取，计数回落。
	second.Release()
	second.Release() // 幂等
	fourth, err := slots.AcquireClientIPSlot(ctx, input)
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

// 隔离：不同 IP / API Key / 分组各自独立计数；同键共享同一槽位。
func TestClientIPConcurrencyScopeIsolation(t *testing.T) {
	slots, _ := newTestConcurrency(t, nil)
	ctx := context.Background()

	ipA := baseAcquireInput("10.0.0.1")
	ipA.Policy = ipGatePolicy(1)
	ipB := baseAcquireInput("10.0.0.2")
	ipB.Policy = ipGatePolicy(1)
	keyB := baseAcquireInput("10.0.0.1")
	keyB.Policy = ipGatePolicy(1)
	keyB.APIKeyID = "key-b"
	groupB := baseAcquireInput("10.0.0.1")
	groupB.Policy = ipGatePolicy(1)
	groupB.GroupID = "grp-b"

	for _, input := range []ClientIPConcurrencyAcquireInput{ipA, ipB, keyB, groupB} {
		decision, err := slots.AcquireClientIPSlot(ctx, input)
		if err != nil || !decision.Acquired {
			t.Fatalf("acquire=%+v err=%v", decision, err)
		}
	}
	// 四个作用域互不影响：每个键再取一个都满额。
	for _, input := range []ClientIPConcurrencyAcquireInput{ipA, ipB, keyB, groupB} {
		decision, err := slots.AcquireClientIPSlot(ctx, input)
		if err != nil || decision.Acquired || decision.Reason != RejectLimitReached {
			t.Fatalf("scope %s must be isolated: %+v err=%v", input.APIKeyID+"/"+input.GroupID, decision, err)
		}
	}
	if rows := slots.Snapshot(); len(rows) != 4 {
		t.Fatalf("snapshot rows = %d, want 4: %+v", len(rows), rows)
	}
}

// 并发获取不得超发：内存锁与 Redis Lua 两条实现都必须严格守住 limit，
// 释放后计数归零。并发数远大于 limit，覆盖竞争窗口。
func TestClientIPConcurrencyConcurrentAcquireNeverExceedsLimit(t *testing.T) {
	const limit = 4
	const workers = 32
	run := func(t *testing.T, slots *ClientIPConcurrency) {
		input := baseAcquireInput("10.9.0.1")
		input.Policy = ipGatePolicy(limit)
		var wg sync.WaitGroup
		var acquired atomic.Int64
		decisions := make(chan ClientIPConcurrencyDecision, workers)
		for index := 0; index < workers; index++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				decision, err := slots.AcquireClientIPSlot(context.Background(), input)
				if err != nil {
					t.Errorf("acquire: %v", err)
					return
				}
				if decision.Acquired {
					acquired.Add(1)
					decisions <- decision
				}
			}()
		}
		wg.Wait()
		close(decisions)
		if got := acquired.Load(); got != limit {
			t.Fatalf("concurrent acquired = %d, want %d", got, limit)
		}
		for decision := range decisions {
			decision.Release()
		}
	}

	t.Run("memory", func(t *testing.T) {
		slots, _ := newTestConcurrency(t, nil)
		run(t, slots)
		if rows := slots.Snapshot(); len(rows) != 0 {
			t.Fatalf("memory state must be empty after release: %+v", rows)
		}
	})

	t.Run("redis", func(t *testing.T) {
		server := miniredis.RunT(t)
		slots, err := NewClientIPConcurrency(ClientIPConcurrencyOptions{
			Clock:              newManualClock(time.UnixMilli(2_000_000)),
			RuntimeStateDriver: RuntimeStateDriverRedis,
			StateRedisURL:      "redis://" + server.Addr(),
		})
		if err != nil {
			t.Fatalf("construct: %v", err)
		}
		t.Cleanup(slots.Close)
		run(t, slots)
		key := clientIPConcurrencyKey("sys", "grp", "key", "10.9.0.1")
		if got := wiZCard(t, server, redisClientIPConcurrencyKeyNS("", key)); got != 0 {
			t.Fatalf("redis slots = %d, want 0 after release", got)
		}
	})
}

// 已取消的 signal 直接快速失败（aborted），不改变任何计数。
func TestClientIPConcurrencyCanceledSignalFastReject(t *testing.T) {
	slots, _ := newTestConcurrency(t, nil)
	ctx := context.Background()
	input := baseAcquireInput("10.0.0.3")
	input.Policy = ipGatePolicy(1)
	first, err := slots.AcquireClientIPSlot(ctx, input)
	if err != nil || !first.Acquired {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	signalCtx, cancel := context.WithCancel(ctx)
	cancel()
	fastInput := input
	fastInput.Signal = signalCtx
	fast, err := slots.AcquireClientIPSlot(ctx, fastInput)
	if err != nil {
		t.Fatal(err)
	}
	if fast.Acquired || fast.Reason != RejectAborted || fast.Current != 0 || fast.QueueSize != 0 {
		t.Fatalf("fast=%+v", fast)
	}
	first.Release()
	if rows := slots.Snapshot(); len(rows) != 0 {
		t.Fatalf("aborted fast path must not create state: %+v", rows)
	}
}

// Redis 闸口：获取/满额拒绝/释放删键 + key 布局。
func TestClientIPConcurrencyRedisMode(t *testing.T) {
	server := miniredis.RunT(t)
	slots, err := NewClientIPConcurrency(ClientIPConcurrencyOptions{
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "redis://" + server.Addr(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(slots.Close)
	ctx := context.Background()
	input := baseAcquireInput("10.0.0.4")
	input.Policy = ipGatePolicy(1)
	first, err := slots.AcquireClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Acquired || first.Current != 1 {
		t.Fatalf("first=%+v", first)
	}
	rejected, err := slots.AcquireClientIPSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Acquired || rejected.Reason != RejectLimitReached {
		t.Fatalf("rejected=%+v", rejected)
	}
	// key 布局：<namespace>:client-ip-concurrency:<base64url>（空 namespace 时
	// 前导冒号；Node 旧 raw 前缀已随实现收敛移除）。
	key := redisClientIPConcurrencyKeyNS("", clientIPConcurrencyKey("sys", "grp", "key", "10.0.0.4"))
	if !server.Exists(key) {
		t.Fatalf("redis key missing: %s", key)
	}
	first.Release()
	if server.Exists(key) {
		t.Fatal("release must delete the empty zset")
	}
}

func TestClientIPConcurrencyRedisMissingURL(t *testing.T) {
	// URL 缺失：构造期报 Node 错误文案。
	if _, err := NewClientIPConcurrency(ClientIPConcurrencyOptions{
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      " ",
	}); err == nil || err.Error() != "JUHE_AI_REDIS_URL 在 Redis runtime state driver 下必须配置" {
		t.Fatalf("err=%v", err)
	}
}
