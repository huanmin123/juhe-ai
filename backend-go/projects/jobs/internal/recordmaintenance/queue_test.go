package recordmaintenance

import (
	"database/sql"
	"testing"

)

func seedRow(t *testing.T, db *sql.DB, id, jobType, cutoffAt string, batchSize, maxBatches int, createdAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO record_maintenance_jobs (id, type, cutoff_at, batch_size, max_batches, created_at)
VALUES (?, ?, ?, ?, ?, ?)`, id, jobType, cutoffAt, batchSize, maxBatches, createdAt); err != nil {
		t.Fatalf("seed row: %v", err)
	}
}

func createLegacyRecordMaintenanceTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE record_maintenance_jobs (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL,
  cutoff_at TEXT NOT NULL,
  batch_size INTEGER NOT NULL,
  max_batches INTEGER NOT NULL,
  created_at TEXT NOT NULL
)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
}

// seedGatewaySnapshotRow 按 gateway internal/recordmaintenance
// DurableDispatch.EnqueueAccountUsageSnapshotUpsert 的落行形状逐列写入
// （同列集、同哨兵值、同规范化 updatedAt），作为消费侧契约的输入。
func seedGatewaySnapshotRow(t *testing.T, db *sql.DB, id, accountID, snapshotJSON, updatedAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO record_maintenance_jobs
		(id, type, cutoff_at, batch_size, max_batches, created_at, account_id, kind, source, snapshot_json, updated_at)
		VALUES (?, 'account_usage_snapshot_upsert', '', 0, 0, ?, ?, 'openai_codex', 'gateway_error', ?, ?)`,
		id, updatedAt, accountID, snapshotJSON, updatedAt); err != nil {
		t.Fatalf("seed gateway snapshot row: %v", err)
	}
}

