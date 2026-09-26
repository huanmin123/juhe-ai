package recordmaintenance

// anthropic_claude 快照行的 drain 落库对照（AI账户Grok用量快照设计 §8.2）：
// gateway 失败面/成功面采集的 claude_* payload 经 drain 消费后按
// account_id/kind/source/snapshot/updatedAt 逐字段送达执行器（真实 runner +
// Mock StatsWriter），与 openai_codex 行混排时两 kind 同批送达。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

func TestDrainDeliversAnthropicSnapshotRowToExecutor(t *testing.T) {
	store, db := openStoreSQLite(t)
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	seedAnthropicSnapshotRow(t, db, "recmaint_claude_1", "acc-claude-1",
		`{"claude_usage_updated_at":"2026-09-27T00:00:00.000Z","claude_5h_used_percent":14,`+
			`"claude_5h_reset_at":"2026-09-27T04:00:00.000Z","claude_7d_used_percent":7,`+
			`"claude_7d_reset_at":"2026-09-28T02:34:03.000Z","claude_unified_status":"allowed",`+
			`"source":"anthropic_unified_headers"}`,
		"2026-09-27T00:00:00.000Z")
	// 混排一行 openai_codex：连续段内两 kind 同一次批量往返（执行器按行透传）。
	seedGatewaySnapshotRow(t, db, "recmaint_claude_2", "acc-codex-1",
		`{"codex_usage_updated_at":"2026-09-27T01:00:00.000Z"}`,
		"2026-09-27T01:00:00.000Z")

	recorder := &recordingStatsWriter{}
	runner := &retention.RecordMaintenanceRunner{
		Mode:   retention.ModeSQLite,
		Clock:  func() time.Time { return time.Date(2026, 9, 27, 2, 0, 0, 0, time.UTC) },
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
	if len(recorder.batches) != 1 || len(recorder.batches[0]) != 2 {
		t.Fatalf("batches = %#v want one 2-input batch", recorder.batches)
	}
	claude := recorder.batches[0][0]
	if claude.AccountID != "acc-claude-1" || claude.Kind != "anthropic_claude" ||
		claude.Source != "anthropic_unified_headers" || claude.UpdatedAt != "2026-09-27T00:00:00.000Z" {
		t.Fatalf("claude envelope = %#v", claude)
	}
	if claude.Snapshot["claude_5h_used_percent"] != float64(14) ||
		claude.Snapshot["claude_5h_reset_at"] != "2026-09-27T04:00:00.000Z" ||
		claude.Snapshot["claude_7d_used_percent"] != float64(7) ||
		claude.Snapshot["claude_7d_reset_at"] != "2026-09-28T02:34:03.000Z" ||
		claude.Snapshot["claude_unified_status"] != "allowed" {
		t.Fatalf("claude snapshot = %#v", claude.Snapshot)
	}
}

// seedAnthropicSnapshotRow 按 gateway 通道的落行形状写入 anthropic_claude 行
// （seedGatewaySnapshotRow 的 kind/source 变体，列集与哨兵值一致）。
func seedAnthropicSnapshotRow(t *testing.T, db *sql.DB, id, accountID, snapshotJSON, updatedAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO record_maintenance_jobs
		(id, type, cutoff_at, batch_size, max_batches, created_at, account_id, kind, source, snapshot_json, updated_at)
		VALUES (?, 'account_usage_snapshot_upsert', '', 0, 0, ?, ?, 'anthropic_claude', 'anthropic_unified_headers', ?, ?)`,
		id, updatedAt, accountID, snapshotJSON, updatedAt); err != nil {
		t.Fatalf("seed anthropic snapshot row: %v", err)
	}
}
