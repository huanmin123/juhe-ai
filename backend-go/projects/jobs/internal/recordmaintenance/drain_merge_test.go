package recordmaintenance

// D5 修复（常驻审查第五轮）的回归测试：drain 循环对照 Node flush 循环的
// collect/process 合并语义（record-maintenance-queue.service.ts:846-869）——
// 连续 account_usage_snapshot_upsert 任务段合并为一次执行面往返，成功后整段
// 删行；失败保留段首行与后续行。

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// mockBatchRunner 在 mockRunner 之上记录批量调用（Mock 边界：合并/非合并、
// 正常/失败路径均可回放）。
type mockBatchRunner struct {
	mockRunner
	mu        sync.Mutex
	batchRuns [][]retention.RecordMaintenanceJob
	failRunAt int // 第 run 段（0 基）失败；-1 表示不失败
	runCount  int
}

func (m *mockBatchRunner) RunAccountUsageSnapshotUpserts(ctx context.Context, jobs []retention.RecordMaintenanceJob) (map[string]any, error) {
	m.mu.Lock()
	m.batchRuns = append(m.batchRuns, append([]retention.RecordMaintenanceJob(nil), jobs...))
	fail := m.failRunAt == m.runCount
	m.runCount++
	m.mu.Unlock()
	if fail {
		return nil, errors.New("快照批量执行失败")
	}
	return map[string]any{"upsertedCount": len(jobs)}, nil
}

func (m *mockBatchRunner) batchRunIDs() [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]string, 0, len(m.batchRuns))
	for _, run := range m.batchRuns {
		ids := make([]string, 0, len(run))
		for _, job := range run {
			ids = append(ids, job.ID)
		}
		out = append(out, ids)
	}
	return out
}

func TestDrainOnceMergesConsecutiveSnapshotUpserts(t *testing.T) {
	runner := &mockBatchRunner{failRunAt: -1}
	drainer, store := newTestDrainer(t, runner)
	ctx := context.Background()
	// 连续快照段 + 尾部普通任务（Node index += snapshotJobs.length 语义）。
	seedRow(t, store.db, "snap_1", "account_usage_snapshot_upsert", "", 0, 0, "2026-09-04T01:00:00.000Z")
	seedRow(t, store.db, "snap_2", "account_usage_snapshot_upsert", "", 0, 0, "2026-09-04T02:00:00.000Z")
	seedRow(t, store.db, "snap_3", "account_usage_snapshot_upsert", "", 0, 0, "2026-09-04T03:00:00.000Z")
	seedRow(t, store.db, "cleanup_1", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T04:00:00.000Z")

	processed, err := drainer.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("drain once: %v", err)
	}
	if processed != 4 {
		t.Fatalf("processed = %d want 4", processed)
	}
	runs := runner.batchRunIDs()
	if len(runs) != 1 {
		t.Fatalf("batch runs = %d want 1 (consecutive run merged): %v", len(runs), runs)
	}
	if len(runs[0]) != 3 || runs[0][0] != "snap_1" || runs[0][1] != "snap_2" || runs[0][2] != "snap_3" {
		t.Fatalf("merged run ids = %v", runs[0])
	}
	if pending := pendingCount(t, store); pending != 0 {
		t.Fatalf("merged run rows not deleted: %d", pending)
	}
}

func TestDrainOnceSplitsNonConsecutiveSnapshotRuns(t *testing.T) {
	runner := &mockBatchRunner{failRunAt: -1}
	drainer, store := newTestDrainer(t, runner)
	ctx := context.Background()
	seedRow(t, store.db, "snap_1", "account_usage_snapshot_upsert", "", 0, 0, "2026-09-04T01:00:00.000Z")
	seedRow(t, store.db, "cleanup_1", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T02:00:00.000Z")
	seedRow(t, store.db, "snap_2", "account_usage_snapshot_upsert", "", 0, 0, "2026-09-04T03:00:00.000Z")

	if _, err := drainer.DrainOnce(ctx); err != nil {
		t.Fatalf("drain once: %v", err)
	}
	runs := runner.batchRunIDs()
	if len(runs) != 2 {
		t.Fatalf("batch runs = %v want two single-job runs", runs)
	}
	if len(runs[0]) != 1 || runs[0][0] != "snap_1" {
		t.Fatalf("first run = %v", runs[0])
	}
	if len(runs[1]) != 1 || runs[1][0] != "snap_2" {
		t.Fatalf("second run = %v", runs[1])
	}
}

func TestDrainOnceRetainsWholeRunOnBatchFailure(t *testing.T) {
	runner := &mockBatchRunner{failRunAt: 0}
	drainer, store := newTestDrainer(t, runner)
	ctx := context.Background()
	seedRow(t, store.db, "snap_1", "account_usage_snapshot_upsert", "", 0, 0, "2026-09-04T01:00:00.000Z")
	seedRow(t, store.db, "snap_2", "account_usage_snapshot_upsert", "", 0, 0, "2026-09-04T02:00:00.000Z")

	processed, err := drainer.DrainOnce(ctx)
	if err == nil || err.Error() != "快照批量执行失败" {
		t.Fatalf("err = %v", err)
	}
	if processed != 0 {
		t.Fatalf("processed = %d", processed)
	}
	// Node 失败 return：整段行保留（head-of-line），后续行不被消费。
	if pending := pendingCount(t, store); pending != 2 {
		t.Fatalf("failed run rows must be retained: %d", pending)
	}
}

func TestDrainShutdownMergesSnapshotUpserts(t *testing.T) {
	runner := &mockBatchRunner{failRunAt: -1}
	drainer, store := newTestDrainer(t, runner)
	seedRow(t, store.db, "snap_1", "account_usage_snapshot_upsert", "", 0, 0, "2026-09-04T01:00:00.000Z")
	seedRow(t, store.db, "snap_2", "account_usage_snapshot_upsert", "", 0, 0, "2026-09-04T02:00:00.000Z")

	if processed := drainer.DrainShutdown(1); processed != 2 {
		t.Fatalf("shutdown processed = %d want 2", processed)
	}
	runs := runner.batchRunIDs()
	if len(runs) != 1 || len(runs[0]) != 2 {
		t.Fatalf("shutdown runs = %v want one merged run", runs)
	}
	if pending := pendingCount(t, store); pending != 0 {
		t.Fatalf("shutdown rows not deleted: %d", pending)
	}
}
