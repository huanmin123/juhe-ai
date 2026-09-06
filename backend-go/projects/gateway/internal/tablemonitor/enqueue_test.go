package tablemonitor

// record_maintenance_jobs 交接契约 v2（codex usage headers 扩展载荷）定向测试：
//   - EnqueueAccountUsageSnapshotUpsert 的落行形状（Node
//     account_usage_snapshot_upsert payload 逐字段 → 表列，与 jobs
//     internal/recordmaintenance/queue.go 的 Dequeue 消费列集成对）；
//   - 对既有 6 列旧表的加法式升级（ensureSchema 幂等双跑；双 DurableDispatch
//     实例各自 ensure，模拟 gateway/jobs 两侧先到先建、各自升级）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

func newDispatchSQLite(t *testing.T) (*DurableDispatch, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	dispatch, err := NewDurableRecordMaintenanceDispatch(db, false, nil)
	if err != nil {
		t.Fatalf("new dispatch: %v", err)
	}
	return dispatch, db
}

// createLegacyRecordMaintenanceTable 按契约 v1（6 列）建表，模拟升级前的
// gateway/jobs 旧表。
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

func sqliteTableColumns(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, columnType string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &columnType, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan pragma row: %v", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("pragma rows: %v", err)
	}
	return columns
}

func TestDurableDispatchSnapshotUpsertRowShape(t *testing.T) {
	dispatch, db := newDispatchSQLite(t)
	result := dispatch.EnqueueAccountUsageSnapshotUpsert(context.Background(), RecordMaintenanceSnapshotJob{
		AccountID: "acc-1",
		Kind:      "openai_codex",
		Source:    "gateway_error",
		Snapshot: map[string]any{
			"codex_usage_updated_at":  "2026-09-06T00:00:00Z",
			"codex_5h_used_percent":   float64(12.5),
			"codex_7d_reset_seconds2": "ignored",
		},
		UpdatedAt: "2026-09-06T00:00:00Z",
	})
	if !result.Queued {
		t.Fatalf("enqueue failed: %#v", result)
	}
	var (
		id, jobType, cutoffAt, createdAt                 string
		batchSize, maxBatches                            int
		accountID, kind, source, snapshotJSON, updatedAt string
	)
	err := db.QueryRow(`SELECT id, type, cutoff_at, batch_size, max_batches, created_at,
		account_id, kind, source, snapshot_json, updated_at
		FROM record_maintenance_jobs`).Scan(&id, &jobType, &cutoffAt, &batchSize, &maxBatches, &createdAt,
		&accountID, &kind, &source, &snapshotJSON, &updatedAt)
	if err != nil {
		t.Fatalf("scan row: %v", err)
	}
	// 公共列：id/createdAt 由 enqueue 按 normalizeRecordMaintenanceJob 语义
	// 填充；快照行的清理列写 '' / 0 哨兵。
	if !strings.HasPrefix(id, "recmaint_") {
		t.Fatalf("id = %q", id)
	}
	if jobType != RecordMaintenanceJobTypeAccountUsageSnapshotUpsert {
		t.Fatalf("type = %q", jobType)
	}
	if cutoffAt != "" || batchSize != 0 || maxBatches != 0 {
		t.Fatalf("cleanup sentinel columns = %q %d %d", cutoffAt, batchSize, maxBatches)
	}
	if createdAt == "" {
		t.Fatal("createdAt not filled")
	}
	// 快照载荷列逐字段（Node payload：accountId/kind/source/snapshot/updatedAt）。
	if accountID != "acc-1" || kind != "openai_codex" || source != "gateway_error" {
		t.Fatalf("payload columns = %q %q %q", accountID, kind, source)
	}
	if updatedAt != "2026-09-06T00:00:00.000Z" {
		t.Fatalf("updatedAt not canonicalized: %q", updatedAt)
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
		t.Fatalf("snapshot_json invalid: %v", err)
	}
	if snapshot["codex_5h_used_percent"] != float64(12.5) {
		t.Fatalf("snapshot payload = %#v", snapshot)
	}
}

func TestDurableDispatchSnapshotUpsertRejectsInvalidUpdatedAt(t *testing.T) {
	dispatch, db := newDispatchSQLite(t)
	// 先走一次有效入队建表，断言被拒任务不落行。
	if queued := dispatch.EnqueueNonBusinessDataCleanup(context.Background(), RecordMaintenanceJob{
		ID: "recmaint_1757000000000_abcdef01", CutoffAt: "2026-09-01T00:00:00.000Z",
		BatchSize: 1, MaxBatches: 1, CreatedAt: "2026-09-04T00:00:00.000Z",
	}); !queued.Queued {
		t.Fatalf("cleanup enqueue failed: %#v", queued)
	}
	result := dispatch.EnqueueAccountUsageSnapshotUpsert(context.Background(), RecordMaintenanceSnapshotJob{
		AccountID: "acc-1",
		Kind:      "openai_codex",
		Snapshot:  map[string]any{"codex_usage_updated_at": "2026-09-06T00:00:00Z"},
		UpdatedAt: "not-a-time",
	})
	if result.Queued || result.DroppedReason != "worker_dispatch_failed" {
		t.Fatalf("receipt = %#v", result)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM record_maintenance_jobs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rejected job must not persist rows: %d %v", count, err)
	}
}

// 旧表升级 + 幂等双跑：v1 表与既有行保留；两个 dispatch 实例各自
// ensureSchema（第二跑等价 jobs 侧/重启后的再次 ensure），列集收敛到 v2。
func TestDurableDispatchUpgradesLegacyTableAndIsIdempotent(t *testing.T) {
	_, db := newDispatchSQLite(t)
	createLegacyRecordMaintenanceTable(t, db)
	legacyJob := RecordMaintenanceJob{
		ID:        "recmaint_1757000000000_abcdef01",
		CutoffAt:  "2026-09-01T00:00:00.000Z",
		BatchSize: defaultCleanupBatchSize, MaxBatches: defaultCleanupMaxBatches,
		CreatedAt: "2026-09-04T00:00:00.000Z",
	}
	if _, err := db.Exec(`INSERT INTO record_maintenance_jobs (id, type, cutoff_at, batch_size, max_batches, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, legacyJob.ID, RecordMaintenanceJobTypeNonBusinessDataCleanup,
		legacyJob.CutoffAt, legacyJob.BatchSize, legacyJob.MaxBatches, legacyJob.CreatedAt); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	first, err := NewDurableRecordMaintenanceDispatch(db, false, nil)
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	// 入队的是新任务（新 id）；与既有行同 id 会因主键冲突被拒，与升级无关。
	newCleanup := legacyJob
	newCleanup.ID = "recmaint_1757000001000_11111111"
	if queued := first.EnqueueNonBusinessDataCleanup(context.Background(), newCleanup); !queued.Queued {
		t.Fatalf("legacy-table cleanup enqueue failed: %#v", queued)
	}
	second, err := NewDurableRecordMaintenanceDispatch(db, false, nil)
	if err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if queued := second.EnqueueAccountUsageSnapshotUpsert(context.Background(), RecordMaintenanceSnapshotJob{
		AccountID: "acc-1",
		Kind:      "openai_codex",
		Snapshot:  map[string]any{"codex_usage_updated_at": "2026-09-06T00:00:00Z"},
		UpdatedAt: "2026-09-06T00:00:00Z",
	}); !queued.Queued {
		t.Fatalf("legacy-table snapshot enqueue failed: %#v", queued)
	}

	columns := sqliteTableColumns(t, db, recordMaintenanceTableName)
	for _, column := range recordMaintenanceSnapshotColumns {
		if !columns[column] {
			t.Fatalf("column %q not upgraded", column)
		}
	}
	// 既有行不受升级影响，新列按默认值补齐。
	var accountID string
	if err := db.QueryRow(`SELECT account_id FROM record_maintenance_jobs WHERE id = ?`,
		legacyJob.ID).Scan(&accountID); err != nil || accountID != "" {
		t.Fatalf("legacy row default = %q %v", accountID, err)
	}
	// 再次双跑 ensure（幂等：重复 ADD COLUMN 被 duplicate column 容错）。
	reCleanup := legacyJob
	reCleanup.ID = "recmaint_1757000002000_22222222"
	if queued := second.EnqueueNonBusinessDataCleanup(context.Background(), reCleanup); !queued.Queued {
		t.Fatalf("re-ensure cleanup enqueue failed: %#v", queued)
	}
}
