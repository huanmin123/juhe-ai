package recordmaintenance

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// mockRunner 记录 RunOnce 调用并按 job.ID 注入失败（Mock 边界：drain 循环
// 的正常/异常路径均可回放、结果稳定）。
type mockRunner struct {
	mu      sync.Mutex
	ran     []retention.RecordMaintenanceJob
	failFor map[string]error
}

func (m *mockRunner) RunOnce(ctx context.Context, job retention.RecordMaintenanceJob) (map[string]any, error) {
	m.mu.Lock()
	m.ran = append(m.ran, job)
	_, failed := m.failFor[job.ID]
	m.mu.Unlock()
	if failed {
		return nil, m.failFor[job.ID]
	}
	return map[string]any{"deletedRows": int64(1)}, nil
}

func (m *mockRunner) ranIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.ran))
	for _, job := range m.ran {
		out = append(out, job.ID)
	}
	return out
}

func (m *mockRunner) setFailure(id string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failFor == nil {
		m.failFor = map[string]error{}
	}
	if err == nil {
		delete(m.failFor, id)
		return
	}
	m.failFor[id] = err
}

func newTestDrainer(t *testing.T, runner Runner) (*Drainer, *Store) {
	t.Helper()
	store, _ := openStoreSQLite(t)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	return &Drainer{Store: store, Runner: runner, Logger: discardLogger()}, store
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func pendingCount(t *testing.T, store *Store) int {
	t.Helper()
	jobs, err := store.Dequeue(context.Background(), 1000)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	return len(jobs)
}

func TestDrainOnceExecutesAndDeletesInOrder(t *testing.T) {
	runner := &mockRunner{}
	drainer, store := newTestDrainer(t, runner)
	ctx := context.Background()
	seedRow(t, store.db, "recmaint_late", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T02:00:00.000Z")
	seedRow(t, store.db, "recmaint_early", "usage_records_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T01:00:00.000Z")

	processed, err := drainer.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("drain once: %v", err)
	}
	if processed != 2 {
		t.Fatalf("processed = %d", processed)
	}
	ids := runner.ranIDs()
	if len(ids) != 2 || ids[0] != "recmaint_early" || ids[1] != "recmaint_late" {
		t.Fatalf("ran order = %v", ids)
	}
	if pending := pendingCount(t, store); pending != 0 {
		t.Fatalf("rows not deleted: %d", pending)
	}
	// 空表再排空是 no-op。
	processed, err = drainer.DrainOnce(ctx)
	if err != nil || processed != 0 {
		t.Fatalf("idle drain = %d %v", processed, err)
	}
}

func TestDrainOnceRetainsFailedHeadAndBlocksLaterRows(t *testing.T) {
	runner := &mockRunner{}
	drainer, store := newTestDrainer(t, runner)
	ctx := context.Background()
	seedRow(t, store.db, "recmaint_head", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T01:00:00.000Z")
	seedRow(t, store.db, "recmaint_tail", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T02:00:00.000Z")
	runner.setFailure("recmaint_head", errors.New("执行器故障"))

	processed, err := drainer.DrainOnce(ctx)
	if err == nil || err.Error() != "执行器故障" {
		t.Fatalf("err = %v", err)
	}
	if processed != 0 {
		t.Fatalf("processed = %d", processed)
	}
	// 失败行保留，后续行不被消费（Node flush 失败 return 语义）。
	ids := runner.ranIDs()
	if len(ids) != 1 || ids[0] != "recmaint_head" {
		t.Fatalf("ran = %v", ids)
	}
	if pending := pendingCount(t, store); pending != 2 {
		t.Fatalf("failed row must be retained: %d", pending)
	}

	// 执行器恢复后同一行重试成功，队列排空。
	runner.setFailure("recmaint_head", nil)
	processed, err = drainer.DrainOnce(ctx)
	if err != nil || processed != 2 {
		t.Fatalf("retry drain = %d %v", processed, err)
	}
	if pending := pendingCount(t, store); pending != 0 {
		t.Fatalf("rows not deleted after retry: %d", pending)
	}
}

func TestDrainShutdownBoundedBatchesWithoutRetry(t *testing.T) {
	runner := &mockRunner{}
	drainer, store := newTestDrainer(t, runner)
	drainer.BatchSize = 2
	seedRow(t, store.db, "r1", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T01:00:00.000Z")
	seedRow(t, store.db, "r2", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T02:00:00.000Z")
	seedRow(t, store.db, "r3", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T03:00:00.000Z")

	// Node shutdown flush：maxBatches=1 → 只跑一个批次（2 行），第 3 行留下。
	if processed := drainer.DrainShutdown(1); processed != 2 {
		t.Fatalf("shutdown processed = %d", processed)
	}
	if pending := pendingCount(t, store); pending != 1 {
		t.Fatalf("shutdown must leave one row: %d", pending)
	}
	// 失败时停且不再重试：失败行保留。
	runner.setFailure("r3", errors.New("停机前故障"))
	if processed := drainer.DrainShutdown(1); processed != 0 {
		t.Fatalf("failing shutdown processed = %d", processed)
	}
	if pending := pendingCount(t, store); pending != 1 {
		t.Fatalf("failed shutdown row must be retained: %d", pending)
	}
	// maxBatches<1 回落 Node 默认 1。
	runner.setFailure("r3", nil)
	if processed := drainer.DrainShutdown(0); processed != 1 {
		t.Fatalf("default shutdown processed = %d", processed)
	}
}

func TestRunDrainsAndStops(t *testing.T) {
	runner := &mockRunner{}
	drainer, store := newTestDrainer(t, runner)
	drainer.FlushInterval = 5 * time.Millisecond
	drainer.RetryDelay = 50 * time.Millisecond
	seedRow(t, store.db, "recmaint_loop", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T01:00:00.000Z")
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		drainer.Run(stop)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for pendingCount(t, store) != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if pending := pendingCount(t, store); pending != 0 {
		t.Fatalf("loop did not drain: %d", pending)
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run loop did not stop")
	}
	// 失败退避期间 stop 立即生效（不等 RetryDelay）。
	failing := &mockRunner{failFor: map[string]error{"x": errors.New("down")}}
	drainer2, store2 := newTestDrainer(t, failing)
	drainer2.FlushInterval = 5 * time.Millisecond
	drainer2.RetryDelay = time.Hour
	seedRow(t, store2.db, "x", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T01:00:00.000Z")
	stop2 := make(chan struct{})
	done2 := make(chan struct{})
	go func() {
		drainer2.Run(stop2)
		close(done2)
	}()
	deadline = time.Now().Add(2 * time.Second)
	for len(failing.ranIDs()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(stop2)
	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("run loop did not stop during retry backoff")
	}
}
