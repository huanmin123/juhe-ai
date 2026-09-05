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
