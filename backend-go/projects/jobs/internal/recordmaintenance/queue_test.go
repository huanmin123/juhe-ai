package recordmaintenance

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func openStoreSQLite(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	// DSN 与组合根 openSQLite 同款（busy_timeout + WAL）：drain 循环与查询
	// 并发访问同一文件，生产与测试都依赖 busy_timeout 消化锁竞争。
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "business.sqlite3")+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL;"); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := OpenStore(db, false)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return store, db
}

func seedRow(t *testing.T, db *sql.DB, id, jobType, cutoffAt string, batchSize, maxBatches int, createdAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO record_maintenance_jobs (id, type, cutoff_at, batch_size, max_batches, created_at)
VALUES (?, ?, ?, ?, ?, ?)`, id, jobType, cutoffAt, batchSize, maxBatches, createdAt); err != nil {
		t.Fatalf("seed row: %v", err)
	}
}

func TestEnsureSchemaIdempotent(t *testing.T) {
	store, db := openStoreSQLite(t)
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("re-ensure schema: %v", err)
	}
	jobs, err := store.Dequeue(ctx, 10)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("empty dequeue = %v %v", jobs, err)
	}
	// 表名与方言限定（PG 落 juhe_dataset schema 的放置契约）。
	if store.Table() != "record_maintenance_jobs" {
		t.Fatalf("sqlite table = %q", store.Table())
	}
	pgStore, err := OpenStore(db, true)
	if err != nil {
		t.Fatalf("open pg store: %v", err)
	}
	if pgStore.Table() != "juhe_dataset.record_maintenance_jobs" {
		t.Fatalf("pg table = %q", pgStore.Table())
	}
	if bound := pgStore.bind("SELECT * FROM t WHERE a = ? AND b = ?"); bound != "SELECT * FROM t WHERE a = $1 AND b = $2" {
		t.Fatalf("pg bind = %q", bound)
	}
}

func TestDequeueOrdersByCreatedAtWithIDTiebreaker(t *testing.T) {
	store, db := openStoreSQLite(t)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	// 乱序写入：created_at 决定消费顺序；同 created_at 时 id 升序稳定。
	seedRow(t, db, "recmaint_3_b", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T03:00:00.000Z")
	seedRow(t, db, "recmaint_1_a", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T01:00:00.000Z")
	seedRow(t, db, "recmaint_2_a", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T01:00:00.000Z")

	jobs, err := store.Dequeue(context.Background(), 2)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("limit respected: %d", len(jobs))
	}
	if jobs[0].ID != "recmaint_1_a" || jobs[1].ID != "recmaint_2_a" {
		t.Fatalf("order = %s, %s", jobs[0].ID, jobs[1].ID)
	}
	if jobs[0].Type != "non_business_data_cleanup" || jobs[0].CutoffAt != "2026-09-01T00:00:00.000Z" ||
		jobs[0].BatchSize != 10 || jobs[0].MaxBatches != 1 || jobs[0].CreatedAt != "2026-09-04T01:00:00.000Z" {
		t.Fatalf("row mapping = %+v", jobs[0])
	}
}

func TestDeleteRemovesRowByID(t *testing.T) {
	store, db := openStoreSQLite(t)
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	seedRow(t, db, "recmaint_1_a", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T01:00:00.000Z")
	seedRow(t, db, "recmaint_2_b", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T02:00:00.000Z")
	if err := store.Delete(ctx, "recmaint_1_a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// 重复删除（并发消费方已删）不报错。
	if err := store.Delete(ctx, "recmaint_1_a"); err != nil {
		t.Fatalf("re-delete: %v", err)
	}
	jobs, err := store.Dequeue(ctx, 10)
	if err != nil || len(jobs) != 1 || jobs[0].ID != "recmaint_2_b" {
		t.Fatalf("after delete = %v %v", jobs, err)
	}
}

// createLegacyRecordMaintenanceTable 按契约 v1（6 列）建表，模拟升级前的
// gateway/jobs 旧表（gateway 旧版 DurableDispatch 或升级前 jobs 组合根所建）。
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

// seedGatewaySnapshotRow 按 gateway internal/tablemonitor
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

// 旧表升级 + 幂等双跑：v1 表与既有行保留；EnsureSchema 两次运行（重启语义）
// 列集收敛到 v2；升级后 gateway 形状的快照行与旧行都能按契约消费。
func TestEnsureSchemaUpgradesLegacyTableAndIsIdempotent(t *testing.T) {
	store, db := openStoreSQLite(t)
	ctx := context.Background()
	createLegacyRecordMaintenanceTable(t, db)
	seedRow(t, db, "recmaint_legacy", "non_business_data_cleanup", "2026-09-01T00:00:00.000Z", 10, 1, "2026-09-04T01:00:00.000Z")

	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("re-ensure schema: %v", err)
	}
	columns, err := store.existingColumns(ctx)
	if err != nil {
		t.Fatalf("existing columns: %v", err)
	}
	for _, column := range snapshotColumns {
		if !columns[column] {
			t.Fatalf("column %q not upgraded", column)
		}
	}

	// 升级后写入 gateway 形状的快照行，Dequeue 逐列映射执行器输入。
	seedGatewaySnapshotRow(t, db, "recmaint_snap_1", "acc-1",
		`{"codex_usage_updated_at":"2026-09-06T00:00:00.000Z","codex_5h_used_percent":12.5}`,
		"2026-09-06T00:00:00.000Z")
	jobs, err := store.Dequeue(ctx, 10)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("jobs = %d want 2", len(jobs))
	}
	legacy := jobs[0]
	if legacy.ID != "recmaint_legacy" || legacy.Type != "non_business_data_cleanup" ||
		legacy.AccountID != "" || legacy.Kind != "" || legacy.Source != "" || legacy.Snapshot != nil || legacy.UpdatedAt != "" {
		t.Fatalf("legacy row mapping = %+v", legacy)
	}
	snap := jobs[1]
	if snap.ID != "recmaint_snap_1" || snap.Type != "account_usage_snapshot_upsert" ||
		snap.AccountID != "acc-1" || snap.Kind != "openai_codex" || snap.Source != "gateway_error" ||
		snap.UpdatedAt != "2026-09-06T00:00:00.000Z" {
		t.Fatalf("snapshot row mapping = %+v", snap)
	}
	if snap.Snapshot["codex_5h_used_percent"] != float64(12.5) {
		t.Fatalf("snapshot payload = %#v", snap.Snapshot)
	}
}

// snapshot_json 损坏（非 JSON 对象文本）按失败返回，行保留等待重试（不静默丢弃）。
func TestDequeueSurfacesCorruptSnapshotJSON(t *testing.T) {
	store, db := openStoreSQLite(t)
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO record_maintenance_jobs
		(id, type, cutoff_at, batch_size, max_batches, created_at, account_id, kind, source, snapshot_json, updated_at)
		VALUES ('recmaint_broken', 'account_usage_snapshot_upsert', '', 0, 0, '2026-09-06T00:00:00.000Z',
			'acc-1', 'openai_codex', 'gateway_error', '{broken', '2026-09-06T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed broken row: %v", err)
	}
	if _, err := store.Dequeue(ctx, 10); err == nil {
		t.Fatal("corrupt snapshot_json must surface an error")
	}
}
