package recordmaintenance

// D5 修复（常驻审查第五轮）的回归测试：drain 循环对照 Node flush 循环的
// collect/process 合并语义（record-maintenance-queue.service.ts:846-869）——
// 连续 account_usage_snapshot_upsert 任务段合并为一次执行面往返，成功后整段
// 删行；失败保留段首行与后续行。

import (
	"context"
	"errors"
	"sync"

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

