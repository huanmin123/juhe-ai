package recordmaintenance

// anthropic_claude 快照行的 drain 落库对照（AI账户Grok用量快照设计 §8.2）：
// gateway 失败面/成功面采集的 claude_* payload 经 drain 消费后按
// account_id/kind/source/snapshot/updatedAt 逐字段送达执行器（真实 runner +
// Mock StatsWriter），与 openai_codex 行混排时两 kind 同批送达。

import (
	"database/sql"
	"testing"

)

func seedAnthropicSnapshotRow(t *testing.T, db *sql.DB, id, accountID, snapshotJSON, updatedAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO record_maintenance_jobs
		(id, type, cutoff_at, batch_size, max_batches, created_at, account_id, kind, source, snapshot_json, updated_at)
		VALUES (?, 'account_usage_snapshot_upsert', '', 0, 0, ?, ?, 'anthropic_claude', 'anthropic_unified_headers', ?, ?)`,
		id, updatedAt, accountID, snapshotJSON, updatedAt); err != nil {
		t.Fatalf("seed anthropic snapshot row: %v", err)
	}
}
