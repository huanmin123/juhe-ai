package gatewayruntimecache

// 深挖复审 R3 回归修复批钉住测试（DD3-A1）：单飞首载的装载固定使用独立有界
// context（对齐同包 runUnionLoad 先例，union.go「单个等待者取消不传播」）。
// 修复前装载 ctx 捕获首发请求 ctx，首发断开 → pending.err = Canceled → 代未
// 变不重试 → 同 key 全体等待者一次性 500；修复后等待者取消只在
// awaitGatewayRuntimeLoad 的 ctx.Done 臂各自生效，装载结果照常发布并回填缓存。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// a1CancelAwareRuntimeModels 在 fakeModels 之上叠加取消感知的运行时读：装载
// 等待窗口内 ctx 取消即返回 ctx.Err()（模拟真实 SQL 读的取消传播），解锁后
// 无论 ctx 取消与否都按基础 fake 完成读取（装载自身不再受等待者取消影响）。
type a1CancelAwareRuntimeModels struct {
	*fakeModels
	block <-chan struct{}

	mu     sync.Mutex
	starts int
}

func (m *a1CancelAwareRuntimeModels) ReadGatewayRuntime(ctx context.Context, key string) (GatewayRuntime, error) {
	m.mu.Lock()
	m.starts++
	m.mu.Unlock()
	select {
	case <-m.block:
	case <-ctx.Done():
		return GatewayRuntime{}, ctx.Err()
	}
	return m.fakeModels.ReadGatewayRuntime(context.Background(), key)
}

func (m *a1CancelAwareRuntimeModels) loadStarts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.starts
}

type a1ReadResult struct {
	runtime GatewayRuntime
	err     error
}

func a1AwaitResult(t *testing.T, ch <-chan a1ReadResult, timeout time.Duration) a1ReadResult {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(timeout):
		t.Fatalf("等待读取结果超时（%s）", timeout)
		return a1ReadResult{}
	}
}

// TestRuntimeSingleflightLoadSurvivesFirstWaiterCancel 钉住：首发等待者取消
// 不得传染单飞装载——首发断开后，同 key 的后续等待者拿到正常装载结果。
func TestRuntimeSingleflightLoadSurvivesFirstWaiterCancel(t *testing.T) {
	block := make(chan struct{})
	models := &a1CancelAwareRuntimeModels{fakeModels: newFakeModels(), block: block}
	models.runtimes["sk-a1"] = staticRuntime(t, models.fakeModels, testAPIKeyRow("key-a1", RouteStrategyModeNormal, "grp-a1"), nil, nil)
	svc := newTestService(t, models, newManualClock(), nil)

	ctx1, cancel := context.WithCancel(context.Background())
	first := make(chan a1ReadResult, 1)
	go func() {
		runtime, err := svc.ReadCachedGatewayRuntimeAsync(ctx1, "sk-a1")
		first <- a1ReadResult{runtime, err}
	}()
	waitFor(t, 3*time.Second, func() bool { return models.loadStarts() == 1 })

	// 首发断开：该等待者拿到自身的 context.Canceled（等待者取消各自生效）。
	cancel()
	got := a1AwaitResult(t, first, 3*time.Second)
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("首发取消应返回 context.Canceled, got err = %v", got.err)
	}

	// 同 key 后续等待者加入仍在进行的单飞装载；装载解锁后必须拿到正常结果
	// （修复前装载 ctx 已随首发取消 → pending.err = Canceled → 全体等待者失败）。
	second := make(chan a1ReadResult, 1)
	go func() {
		runtime, err := svc.ReadCachedGatewayRuntimeAsync(context.Background(), "sk-a1")
		second <- a1ReadResult{runtime, err}
	}()
	close(block)
	got2 := a1AwaitResult(t, second, 3*time.Second)
	if got2.err != nil {
		t.Fatalf("首发取消不得传染后续等待者, got err = %v", got2.err)
	}
	if got2.runtime.APIKey == nil || got2.runtime.APIKey.ID != "key-a1" {
		t.Fatalf("后续等待者应拿到完整运行时, got APIKey = %+v", got2.runtime.APIKey)
	}
}
