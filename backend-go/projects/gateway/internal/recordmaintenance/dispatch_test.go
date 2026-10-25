package recordmaintenance

// record_maintenance_jobs 交接契约 v2（codex usage headers 扩展载荷）定向测试：
//   - EnqueueAccountUsageSnapshotUpsert 的落行形状（Node
//     account_usage_snapshot_upsert payload 逐字段 → 表列，与 jobs
//     internal/recordmaintenance/queue.go 的 Dequeue 消费列集成对）；
//   - 对既有 6 列旧表的加法式升级（ensureSchema 幂等双跑；双 DurableDispatch
//     实例各自 ensure，模拟 gateway/jobs 两侧先到先建、各自升级）。
//
// 夹具走 pgtestkit 隔离 PG 库（SQLite 与内存模式退役后唯一形态）：表名/
// 占位符/升级路径即生产 juhe_dataset 形态。

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// newDispatchPG 创建 pgtestkit 隔离 PG 测试库（预建 juhe_dataset schema，
// 表由生产 ensureSchema 路径幂等创建）并返回 PG 单形态 dispatch。
func newDispatchPG(t *testing.T) (*DurableDispatch, *sql.DB) {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_dataset`)
		return err
	})
	dispatch, err := NewDurableRecordMaintenanceDispatch(db, nil)
	if err != nil {
		t.Fatalf("new dispatch: %v", err)
	}
	return dispatch, db
}

// createLegacyRecordMaintenanceTable 按契约 v1（6 列）建表，模拟升级前的
// gateway/jobs 旧表。
func createLegacyRecordMaintenanceTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE juhe_dataset.record_maintenance_jobs (
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

// datasetTableColumns 经 information_schema 列出既有列名（与生产
// existingColumns 同一查询形态）。
func datasetTableColumns(t *testing.T, db *sql.DB, schema, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT column_name FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2`, schema, table)
	if err != nil {
		t.Fatalf("information_schema.columns: %v", err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column row: %v", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("column rows: %v", err)
	}
	return columns
}

func TestDurableDispatchSnapshotUpsertRowShape(t *testing.T) {
	dispatch, db := newDispatchPG(t)
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
		FROM juhe_dataset.record_maintenance_jobs`).Scan(&id, &jobType, &cutoffAt, &batchSize, &maxBatches, &createdAt,
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
	dispatch, db := newDispatchPG(t)
	// 先走一次有效入队建表，断言被拒任务不落行。
	if queued := dispatch.EnqueueAccountUsageSnapshotUpsert(context.Background(), RecordMaintenanceSnapshotJob{
		AccountID: "acc-0",
		Kind:      "openai_codex",
		Snapshot:  map[string]any{"codex_usage_updated_at": "2026-09-05T00:00:00Z"},
		UpdatedAt: "2026-09-05T00:00:00Z",
	}); !queued.Queued {
		t.Fatalf("seed enqueue failed: %#v", queued)
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
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.record_maintenance_jobs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rejected job must not persist rows: %d %v", count, err)
	}
}

// 旧表升级 + 幂等双跑：v1 表与既有行保留（清理行家族由 jobs drain 消费）；
// 两个 dispatch 实例各自 ensureSchema（第二跑等价 jobs 侧/重启后的再次
// ensure），列集收敛到 v2。
func TestDurableDispatchUpgradesLegacyTableAndIsIdempotent(t *testing.T) {
	_, db := newDispatchPG(t)
	createLegacyRecordMaintenanceTable(t, db)
	legacyRowID := "recmaint_1757000000000_abcdef01"
	if _, err := db.Exec(`INSERT INTO juhe_dataset.record_maintenance_jobs (id, type, cutoff_at, batch_size, max_batches, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, legacyRowID, RecordMaintenanceJobTypeNonBusinessDataCleanup,
		"2026-09-01T00:00:00.000Z", 1, 1, "2026-09-04T00:00:00.000Z"); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	first, err := NewDurableRecordMaintenanceDispatch(db, nil)
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	// 入队的是新任务（新 id）；与既有行同 id 会因主键冲突被拒，与升级无关。
	if queued := first.EnqueueAccountUsageSnapshotUpsert(context.Background(), RecordMaintenanceSnapshotJob{
		AccountID: "acc-1",
		Kind:      "openai_codex",
		Snapshot:  map[string]any{"codex_usage_updated_at": "2026-09-06T00:00:00Z"},
		UpdatedAt: "2026-09-06T00:00:00Z",
	}); !queued.Queued {
		t.Fatalf("legacy-table snapshot enqueue failed: %#v", queued)
	}
	second, err := NewDurableRecordMaintenanceDispatch(db, nil)
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

	columns := datasetTableColumns(t, db, recordMaintenancePGSchema, recordMaintenanceTableName)
	for _, column := range recordMaintenanceSnapshotColumns {
		if !columns[column] {
			t.Fatalf("column %q not upgraded", column)
		}
	}
	// 既有行不受升级影响，新列按默认值补齐。
	var accountID string
	if err := db.QueryRow(`SELECT account_id FROM juhe_dataset.record_maintenance_jobs WHERE id = $1`,
		legacyRowID).Scan(&accountID); err != nil || accountID != "" {
		t.Fatalf("legacy row default = %q %v", accountID, err)
	}
}
