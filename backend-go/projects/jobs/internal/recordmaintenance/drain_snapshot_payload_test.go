package recordmaintenance

// 交接通道 → 执行器定向对照：gateway 形状落行的
// account_usage_snapshot_upsert 任务经 drain 消费后，必须把
// account_id/kind/source/snapshot/updatedAt 逐字段送达
// retention.RecordMaintenanceRunner.RunAccountUsageSnapshotUpserts（执行器为
// 真实实现，仅 StatsWriter 打 Mock——Mock 边界可回放、结果稳定），并保持
// 连续段一次批量往返（Node record-maintenance-queue.service.ts:846-869）。

import (
	"context"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// recordingStatsWriter 记录 UpsertAccountUsageSnapshots 的批量输入；其余
// stats-writer 操作在本测试路径上不应被触达。
type recordingStatsWriter struct {
	mu      sync.Mutex
	batches [][]retention.AccountUsageSnapshotUpsertInput
}

func (w *recordingStatsWriter) CleanupUsageStatsRetention(context.Context, retention.UsageStatsRetentionInput) (retention.UsageStatsRetentionCounts, error) {
	return retention.UsageStatsRetentionCounts{}, nil
}

func (w *recordingStatsWriter) CleanupNonBusinessStatsData(context.Context, string, int) (retention.NonBusinessDataCleanupCounts, error) {
	return retention.NonBusinessDataCleanupCounts{}, nil
}

func (w *recordingStatsWriter) CleanupDeletedApiKeyRecordStats(context.Context, retention.DeletedApiKeyRecordStatsCleanupInput) error {
	return nil
}

func (w *recordingStatsWriter) CleanupDeletedAccountRecordStats(context.Context, retention.DeletedAccountRecordStatsCleanupInput) error {
	return nil
}

func (w *recordingStatsWriter) UpsertAccountUsageSnapshots(_ context.Context, inputs []retention.AccountUsageSnapshotUpsertInput) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.batches = append(w.batches, append([]retention.AccountUsageSnapshotUpsertInput(nil), inputs...))
	return nil
}

