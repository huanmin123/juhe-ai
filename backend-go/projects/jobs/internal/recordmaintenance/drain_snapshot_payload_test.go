package recordmaintenance

// 交接通道 → 执行器定向对照：gateway 形状落行的
// account_usage_snapshot_upsert 任务经 drain 消费后，必须把
// account_id/kind/source/snapshot/updatedAt 逐字段送达
// retention.RecordMaintenanceRunner.RunAccountUsageSnapshotUpserts（执行器为
// 真实实现，仅 StatsWriter 打 Mock——Mock 边界可回放、结果稳定），并保持
// 连续段一次批量往返（Node record-maintenance-queue.service.ts:846-869）。

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

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

func (w *recordingStatsWriter) CleanupSystemMetricsRetention(context.Context, retention.SystemMetricsRetentionInput) (retention.SystemMetricsRetentionCounts, error) {
	return retention.SystemMetricsRetentionCounts{}, nil
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

func TestDrainDeliversGatewaySnapshotRowToExecutor(t *testing.T) {
	store, db := openStoreSQLite(t)
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	// 两个连续 gateway 形状快照行（连续段边界由 drain_merge_test.go 以 Mock
	// Runner 覆盖；本测试只对照载荷送达，执行器为真实实现）。
	seedGatewaySnapshotRow(t, db, "recmaint_snap_1", "acc-1",
		`{"codex_usage_updated_at":"2026-09-06T00:00:00.000Z","codex_5h_used_percent":12.5}`,
		"2026-09-06T00:00:00.000Z")
	seedGatewaySnapshotRow(t, db, "recmaint_snap_2", "acc-2",
		`{"codex_usage_updated_at":"2026-09-06T01:00:00.000Z","codex_7d_used_percent":80}`,
		"2026-09-06T01:00:00.000Z")

	recorder := &recordingStatsWriter{}
	runner := &retention.RecordMaintenanceRunner{
		Mode:   retention.ModeSQLite,
		Clock:  func() time.Time { return time.Date(2026, 9, 6, 2, 0, 0, 0, time.UTC) },
		Logger: discardLogger(),
		Executor: retention.RecordMaintenanceExecutor{
			StatsWriter: recorder,
		},
	}
	drainer := &Drainer{Store: store, Runner: runner, Logger: discardLogger()}

	processed, err := drainer.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("drain once: %v", err)
	}
	if processed != 2 {
		t.Fatalf("processed = %d want 2", processed)
	}
	if pending := pendingCount(t, store); pending != 0 {
		t.Fatalf("rows not deleted: %d", pending)
	}
	// 单次批量往返承载整段输入（Node 合并语义）。
	if len(recorder.batches) != 1 || len(recorder.batches[0]) != 2 {
		t.Fatalf("batches = %#v want one 2-input batch", recorder.batches)
	}
	want := []retention.AccountUsageSnapshotUpsertInput{
		{
			AccountID: "acc-1",
			Kind:      "openai_codex",
			Source:    "gateway_error",
			Snapshot: map[string]any{
				"codex_usage_updated_at": "2026-09-06T00:00:00.000Z",
				"codex_5h_used_percent":  float64(12.5),
			},
			UpdatedAt: "2026-09-06T00:00:00.000Z",
		},
		{
			AccountID: "acc-2",
			Kind:      "openai_codex",
			Source:    "gateway_error",
			Snapshot: map[string]any{
				"codex_usage_updated_at": "2026-09-06T01:00:00.000Z",
				"codex_7d_used_percent":  float64(80),
			},
			UpdatedAt: "2026-09-06T01:00:00.000Z",
		},
	}
	if !reflect.DeepEqual(recorder.batches[0], want) {
		t.Fatalf("batch inputs = %#v want %#v", recorder.batches[0], want)
	}
}
