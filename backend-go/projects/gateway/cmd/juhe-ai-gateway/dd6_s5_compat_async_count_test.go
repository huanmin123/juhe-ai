package main

// DD6-S5 波次测试（2026-11-01，R6 confirmed 中低）：compat 401 计数此前在响
// 应写出后同步占请求协程（WithoutCancel + 3s 超时的 RecordPreAuthFailure）——
// Redis 黑洞窗内每个无效凭据请求在 401 已写出（无背压）后仍同步占用协程至超
// 时，goroutine ≈ RPS×3s。修复：计数改有界 fire-and-forget 后台面（单 worker
// + 有界 channel，超容量丢弃计数并告警），401 响应不等待计数。钉住契约：
// ① 慢计数（模拟 Redis 黑洞）窗内 401 响应立即返回，计数异步完成；
// ② 计数队列有界，超容量直接丢弃并计数告警，已入队计数不丢。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// dd6S5SlowCountCircuits 以可编程延迟模拟慢 Redis（黑洞窗内 evalLua 长时间
// 不返回的形态；生产 3s 由 compatPreAuthFailureCountTimeout 兜底，测试用短延
// 迟保持等价时序、快速收敛）。
type dd6S5SlowCountCircuits struct {
	ddC1RecordingCircuits
	delay time.Duration
}

func (c *dd6S5SlowCountCircuits) RecordPreAuthFailure(ctx context.Context, input gatewaypreauth.PreAuthFailureInput) (gatewaypreauth.CircuitDecision, error) {
	select {
	case <-time.After(c.delay):
	case <-ctx.Done():
		return gatewaypreauth.CircuitDecision{}, ctx.Err()
	}
	return c.ddC1RecordingCircuits.RecordPreAuthFailure(ctx, input)
}

// TestDD6S5Compat401ResponseNotBlockedBySlowCount 钉住 ①：计数面慢（模拟
// Redis 黑洞）时 401 响应必须立即返回（不等待计数），计数随后异步完成入账
// ——修复前响应协程被同步计数拉长至计数时延。
func TestDD6S5Compat401ResponseNotBlockedBySlowCount(t *testing.T) {
	circuits := &dd6S5SlowCountCircuits{delay: 400 * time.Millisecond}
	chain := ddC1Chain(t, circuits)

	startedAt := time.Now()
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, withBearer("bad-key"))
	elapsed := time.Since(startedAt)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d, want 401（计数不得阻断/改写已写出的 401）", recorder.Code)
	}
	if elapsed >= circuits.delay/2 {
		t.Fatalf("401 响应耗时 %v ≥ 慢计数时延一半 %v：响应不得等待后台计数（DD6-S5）", elapsed, circuits.delay/2)
	}
	records := waitForCompatRecords(t, &circuits.ddC1RecordingCircuits, 1)
	if records[0].Reason != gatewaypreauth.PreAuthFailureInvalidAPIKey {
		t.Fatalf("reason = %q, want invalid_api_key（慢计数最终异步完成）", records[0].Reason)
	}
}

// dd6S5GateCircuits 把 RecordPreAuthFailure 挂在 gate 上（首个计数停住后台
// worker，制造确定性队列堆积现场），并记录每次进入计数面的调用。
type dd6S5GateCircuits struct {
	ddC1RecordingCircuits
	gate    chan struct{}
	entered atomic.Int32
	once    sync.Once
}

func (c *dd6S5GateCircuits) RecordPreAuthFailure(ctx context.Context, input gatewaypreauth.PreAuthFailureInput) (gatewaypreauth.CircuitDecision, error) {
	c.entered.Add(1)
	<-c.gate
	return c.ddC1RecordingCircuits.RecordPreAuthFailure(ctx, input)
}

func (c *dd6S5GateCircuits) release() { c.once.Do(func() { close(c.gate) }) }

// dd6S5FireCompat401 直调计数入口（与 mux 写出 401 后的调用现场同构），避免
// 逐请求走完整 HTTP 面。
func dd6S5FireCompat401(chain *gatewayChain) {
	recorder := httptest.NewRecorder()
	res := gatewaypreauth.NewTrackingWriter(recorder)
	res.WriteHeader(http.StatusUnauthorized) // 模拟 compat mux 已写出 401
	req := gatewaypreauth.NewGatewayRequest(withBearer("bad-key"))
	chain.recordCompatPreAuthFailure(req, res)
}

// TestDD6S5CompatCountQueueBoundedDropsOverCapacity 钉住 ②：首个计数停住后
// 台 worker，队列可精确容纳 compatPreAuthFailureQueueCapacity 条，再入队即
// 丢弃并计数告警面（dropped 计数）；放行后已入队计数全部补账、不丢。
func TestDD6S5CompatCountQueueBoundedDropsOverCapacity(t *testing.T) {
	circuits := &dd6S5GateCircuits{gate: make(chan struct{})}
	t.Cleanup(circuits.release)
	chain := ddC1Chain(t, circuits)

	// 首个计数被后台 worker 取走并停在 gate 内（worker 不再接收）。
	dd6S5FireCompat401(chain)
	deadline := time.Now().Add(2 * time.Second)
	for circuits.entered.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if circuits.entered.Load() != 1 {
		t.Fatalf("后台 worker 未进入计数面: entered=%d", circuits.entered.Load())
	}

	// 队列恰好再容纳 capacity 条，超出的第一条即丢弃。
	for i := 0; i < compatPreAuthFailureQueueCapacity; i++ {
		dd6S5FireCompat401(chain)
	}
	if circuits.entered.Load() != 1 {
		t.Fatalf("worker 停在 gate 内时不得继续消费: entered=%d", circuits.entered.Load())
	}
	dd6S5FireCompat401(chain) // capacity+1：超出容量，直接丢弃
	if dropped := chain.compatPreAuthFailureQueue.dropped.Load(); dropped != 1 {
		t.Fatalf("超容量计数必须丢弃并计数，dropped = %d, want 1", dropped)
	}

	// 放行：首个计数 + capacity 条队列计数全部补账，丢弃不波及已入队计数。
	circuits.release()
	records := waitForCompatRecords(t, &circuits.ddC1RecordingCircuits, compatPreAuthFailureQueueCapacity+1)
	if got := len(records); got != compatPreAuthFailureQueueCapacity+1 {
		t.Fatalf("补账计数 = %d, want %d（丢弃只作用于超容量条目）", got, compatPreAuthFailureQueueCapacity+1)
	}
	if dropped := chain.compatPreAuthFailureQueue.dropped.Load(); dropped != 1 {
		t.Fatalf("放行后 dropped 必须保持 1，实际 %d", dropped)
	}
}
