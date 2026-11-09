// BUG-0248 契约 5：公开 Drain 与后台 flushCycle 并发时不得双删 pending
// （removeBatch 按队头快照切片，单执行者不变量）。运行时防护为 flushMu
// 串行：本文件用一个门控 store 证明 Drain 在在役冲刷执行者持有批快照期间
// 无法进入 WriteBatch（最大并发 1），且最终语义不回归（全部落库、队列清空）。
package usagewriter

import (
	"context"
	"sync"
	"testing"
	"time"
)

// bug0248GatedStore 统计 WriteBatch 并发并在放行前阻塞。
type bug0248GatedStore struct {
	mu        sync.Mutex
	batches   []WritePlan
	active    int
	maxActive int
	entered   chan struct{}
	release   chan struct{}
}

func (g *bug0248GatedStore) WriteBatch(ctx Ctx, plan WritePlan) (int, error) {
	g.mu.Lock()
	g.active++
	if g.active > g.maxActive {
		g.maxActive = g.active
	}
	g.mu.Unlock()
	select {
	case g.entered <- struct{}{}:
	default:
	}
	<-g.release
	g.mu.Lock()
	g.active--
	g.batches = append(g.batches, plan)
	count := len(plan.ShardEntries)
	g.mu.Unlock()
	return count, nil
}

func (g *bug0248GatedStore) totalRows() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	total := 0
	for _, batch := range g.batches {
		total += len(batch.ShardEntries)
	}
	return total
}

func TestBug0248DrainSerializedAgainstFlushCycle(t *testing.T) {
	store := &bug0248GatedStore{entered: make(chan struct{}, 16), release: make(chan struct{})}
	// BatchSize=1：3 条记录切成 3 个批次，逐批观察队头快照串行。
	writer := NewWriter(Config{BatchSize: 1}, store, fixedClock("2026-01-02T03:04:05.000Z"))
	closedCtx, cancel := context.WithCancel(context.Background())
	cancel()
	t.Cleanup(func() { writer.Close(closedCtx) })
	for i := 0; i < 3; i++ {
		if err := writer.Enqueue(context.Background(), gatewayInput("bug0248-drain")); err != nil {
			t.Fatalf("入队失败: %v", err)
		}
	}

	cycleDone := make(chan struct{})
	go func() {
		defer close(cycleDone)
		writer.flushCycle()
	}()
	// 等待 flushCycle 真正进入 WriteBatch（持有队头批快照）。
	select {
	case <-store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("flushCycle 未进入 WriteBatch")
	}

	// 并发 Drain：守卫生效时它必须阻塞在 flushMu 上，无法进入 WriteBatch
	//（旧实现会立刻以同一批快照进入第二次 WriteBatch，maxActive 变 2）。
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		writer.Drain()
	}()
	time.Sleep(150 * time.Millisecond)
	if active := store.maxActiveNow(); active != 1 {
		t.Fatalf("冲刷执行者必须串行（maxActive=%d want 1）", active)
	}

	// 放行后两边都完成，语义不回归：3 行全部落库、队列清空、批次边界保持。
	close(store.release)
	select {
	case <-drainDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Drain 未在放行后完成")
	}
	select {
	case <-cycleDone:
	case <-time.After(5 * time.Second):
		t.Fatal("flushCycle 未在放行后完成")
	}
	if total := store.totalRows(); total != 3 {
		t.Fatalf("应写 3 行，实际: %d", total)
	}
	if pending := writer.PendingCount(); pending != 0 {
		t.Fatalf("队列应清空，实际: %d", pending)
	}
	if written := writer.Runtime().WrittenRecords; written != 3 {
		t.Fatalf("writtenRecords=%d want 3", written)
	}
	if max := store.maxActiveNow(); max != 1 {
		t.Fatalf("全程只允许一个冲刷执行者（maxActive=%d）", max)
	}
}

func (g *bug0248GatedStore) maxActiveNow() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.maxActive
}
