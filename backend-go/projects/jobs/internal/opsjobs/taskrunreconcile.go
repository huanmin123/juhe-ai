package opsjobs

import (
	"context"
	"fmt"
	"time"
)

// 后台临时任务状态对账，逐语义对齐 Node
// modules/background/background-task-run-reconcile.job.ts：
//   - 常量：initialDelay=2s、interval=5min、staleAfter=10min、
//     batchSize=JUHE_AI_BACKGROUND_TASK_RUN_RECONCILE_BATCH_SIZE(默认500)。
//   - 输入：queuedBefore = runningHeartbeatBefore = now-10min（RFC3339 UTC）。
//   - 结果计数：failedQueuedCount/failedRunningCount/deletedExpiredLeaseCount。

const (
	TaskRunReconcileInitialDelayMS = 2_000
	TaskRunReconcileIntervalMS     = 5 * 60_000
	TaskRunStaleAfterMS            = 10 * 60_000
	TaskRunReconcileBatchSize      = 500
)

// TaskRunReconcileInput 是仓储层对账输入（字段名对齐 Node）。
type TaskRunReconcileInput struct {
	QueuedBefore           string `json:"queuedBefore"`
	RunningHeartbeatBefore string `json:"runningHeartbeatBefore"`
	Now                    string `json:"now"`
	Limit                  int    `json:"limit"`
}

// TaskRunReconcileResult 是对账结果计数。
type TaskRunReconcileResult struct {
	FailedQueuedCount        int64 `json:"failedQueuedCount"`
	FailedRunningCount       int64 `json:"failedRunningCount"`
	DeletedExpiredLeaseCount int64 `json:"deletedExpiredLeaseCount"`
}

// ReconciledCount 返回修复的陈旧任务总数。
func (r TaskRunReconcileResult) ReconciledCount() int64 {
	return r.FailedQueuedCount + r.FailedRunningCount
}

// TaskRunReconcileRepo 是后台任务 run/lease 持久化 port。
// 生产实现为 PostgreSQL/SQLite 仓储（DB 双模），测试用内存 mock。
type TaskRunReconcileRepo interface {
	ReconcileStale(ctx context.Context, input TaskRunReconcileInput) (TaskRunReconcileResult, error)
}

// BuildTaskRunReconcileInput 对齐 Node 的输入构造：时间戳统一 RFC3339 UTC。
func BuildTaskRunReconcileInput(nowMS int64, batchSize int) TaskRunReconcileInput {
	now := time.UnixMilli(nowMS).UTC().Format(time.RFC3339Nano)
	staleBefore := time.UnixMilli(nowMS - TaskRunStaleAfterMS).UTC().Format(time.RFC3339Nano)
	return TaskRunReconcileInput{
		QueuedBefore:           staleBefore,
		RunningHeartbeatBefore: staleBefore,
		Now:                    now,
		Limit:                  batchSize,
	}
}

// RunTaskRunReconcile 执行一轮对账。
func RunTaskRunReconcile(ctx context.Context, repo TaskRunReconcileRepo, nowMS int64, batchSize int) (TaskRunReconcileResult, error) {
	if repo == nil {
		return TaskRunReconcileResult{}, fmt.Errorf("后台任务对账仓储未初始化")
	}
	if batchSize < 1 {
		batchSize = TaskRunReconcileBatchSize
	}
	return repo.ReconcileStale(ctx, BuildTaskRunReconcileInput(nowMS, batchSize))
}

// TaskRunReconcileScheduler 是固定间隔调度器（initialDelay/interval 对齐
// Node scheduler 配置）。kill-restart 后重启即恢复：对账本身幂等，
// stale 判定基于持久化时间戳，与进程生命周期无关。
type TaskRunReconcileScheduler struct {
	repo      TaskRunReconcileRepo
	batchSize int
	nowMS     func() int64
	onResult  func(TaskRunReconcileResult)
}

func NewTaskRunReconcileScheduler(repo TaskRunReconcileRepo, batchSize int, nowMS func() int64, onResult func(TaskRunReconcileResult)) (*TaskRunReconcileScheduler, error) {
	if repo == nil {
		return nil, fmt.Errorf("后台任务对账仓储未初始化")
	}
	if nowMS == nil {
		return nil, fmt.Errorf("后台任务对账必须注入 NowMS 时钟")
	}
	if batchSize < 1 {
		batchSize = TaskRunReconcileBatchSize
	}
	return &TaskRunReconcileScheduler{repo: repo, batchSize: batchSize, nowMS: nowMS, onResult: onResult}, nil
}

// RunOnce 执行单轮（供外部调度器/测试驱动）。
func (s *TaskRunReconcileScheduler) RunOnce(ctx context.Context) (TaskRunReconcileResult, error) {
	result, err := RunTaskRunReconcile(ctx, s.repo, s.nowMS(), s.batchSize)
	if err == nil && s.onResult != nil {
		s.onResult(result)
	}
	return result, err
}
