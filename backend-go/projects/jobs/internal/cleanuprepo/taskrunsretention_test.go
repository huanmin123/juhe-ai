package cleanuprepo

import (
	"context"
	"testing"
)

// TaskRunsRetentionStore 测试（缺陷1修复）：background_task_runs /
// background_job_leases 此前全仓无删除路径，本节锁定滚动保留清理的
// 删除边界（严格早于 cutoff、最旧优先、受 limit 约束）与双模方言形状。
// DDL 只建清理谓词涉及的最小列（created_at / lease_until），与
// taskruns.Store 冻结 schema 的时间列同名同 TEXT 语义。

func createKitTaskRunsTables(t *testing.T, db *DB) {
	t.Helper()
	mustExecKit(t, db, `CREATE TABLE background_task_runs (
		run_id TEXT PRIMARY KEY,
		job_name TEXT NOT NULL,
		status TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`)
	mustExecKit(t, db, `CREATE TABLE background_job_leases (
		lease_key TEXT PRIMARY KEY,
		job_name TEXT NOT NULL,
		lease_until TEXT NOT NULL
	)`)
}

func seedKitTaskRuns(t *testing.T, db *DB) {
	t.Helper()
	mustExecKit(t, db, `INSERT INTO background_task_runs (run_id, job_name, status, created_at) VALUES
		('run-old-2', 'projection', 'completed', '2020-01-01T00:00:00.000Z'),
		('run-old-1', 'projection', 'failed',    '2020-06-01T00:00:00.000Z'),
		('run-edge',  'projection', 'completed', '2025-01-01T00:00:00.000Z'),
		('run-new',   'projection', 'running',   '2100-01-01T00:00:00.000Z')`)
	mustExecKit(t, db, `INSERT INTO background_job_leases (lease_key, job_name, lease_until) VALUES
		('lease-expired-old', 'worker', '2020-01-01T00:00:00.000Z'),
		('lease-expired-edge', 'worker', '2025-01-01T00:00:00.000Z'),
		('lease-held', 'worker', '2100-01-01T00:00:00.000Z')`)
}

// TestTaskRunsRetentionCleanupBeforeCutoff：清理前后行数断言——严格早于
// cutoff 的行删除（最旧优先、limit 截断），边界行与新鲜行保留；无候选时
// 零删除幂等。
func TestTaskRunsRetentionCleanupBeforeCutoff(t *testing.T) {
	db := openKitSQLite(t, "taskruns_retention")
	createKitTaskRunsTables(t, db)
	seedKitTaskRuns(t, db)
	store := &TaskRunsRetentionStore{DB: db}
	ctx := context.Background()
	const cutoff = "2025-01-01T00:00:00.000Z"

	// limit=1：只删最旧的 run-old-2。
	deleted, err := store.CleanupRunsBefore(ctx, cutoff, 1)
	if err != nil || deleted != 1 {
		t.Fatalf("limited runs deleted = %d, %v", deleted, err)
	}
	if got := mustQueryCountKit(t, db, `SELECT COUNT(*) FROM background_task_runs WHERE run_id = 'run-old-2'`); got != 0 {
		t.Fatalf("最旧运行历史行应先被删除")
	}
	// 放开 limit：run-old-1 删除；边界行 run-edge（等于 cutoff）与新鲜行保留。
	deleted, err = store.CleanupRunsBefore(ctx, cutoff, 1000)
	if err != nil || deleted != 1 {
		t.Fatalf("runs deleted = %d, %v", deleted, err)
	}
	if got := mustQueryCountKit(t, db, `SELECT COUNT(*) FROM background_task_runs WHERE run_id IN ('run-edge','run-new')`); got != 2 {
		t.Fatalf("边界行与新鲜运行历史不应被删除，残余 = %d", got)
	}
	// 再跑一轮：无严格早于 cutoff 的候选 → 零删除（幂等）。
	deleted, err = store.CleanupRunsBefore(ctx, cutoff, 1000)
	if err != nil || deleted != 0 {
		t.Fatalf("幂等轮 runs deleted = %d, %v", deleted, err)
	}

	// 租约：严格早于 cutoff 的过期行删除（含 scheduled 释放置过期的审计行），
	// 边界行与有效租约保留。
	deleted, err = store.CleanupExpiredLeasesBefore(ctx, cutoff, 1000)
	if err != nil || deleted != 1 {
		t.Fatalf("leases deleted = %d, %v", deleted, err)
	}
	if got := mustQueryCountKit(t, db, `SELECT COUNT(*) FROM background_job_leases WHERE lease_key IN ('lease-expired-edge','lease-held')`); got != 2 {
		t.Fatalf("边界租约与有效租约不应被删除，残余 = %d", got)
	}
	deleted, err = store.CleanupExpiredLeasesBefore(ctx, cutoff, 1000)
	if err != nil || deleted != 0 {
		t.Fatalf("幂等轮 leases deleted = %d, %v", deleted, err)
	}
}

// TestTaskRunsRetentionPGStatementShape：PG 方言必须产出 juhe_stats schema
// 限定的 ctid 批删语句（录制驱动断言语句形状与占位符改写）。
func TestTaskRunsRetentionPGStatementShape(t *testing.T) {
	rec := newPGRecorder()
	store := &TaskRunsRetentionStore{DB: openRecorderPG(rec)}
	ctx := context.Background()
	if _, err := store.CleanupRunsBefore(ctx, "2025-01-01T00:00:00.000Z", 7); err != nil {
		t.Fatalf("pg cleanup runs: %v", err)
	}
	if _, err := store.CleanupExpiredLeasesBefore(ctx, "2025-01-01T00:00:00.000Z", 7); err != nil {
		t.Fatalf("pg cleanup leases: %v", err)
	}
	statements := rec.all()
	if len(statements) != 2 {
		t.Fatalf("应恰好录制两条 DELETE，实际 %d 条", len(statements))
	}
	wantRuns := normalizeKitSQL(bindTestPG(`
      DELETE FROM juhe_stats.background_task_runs
      WHERE ctid IN (
        SELECT ctid FROM juhe_stats.background_task_runs
        WHERE "created_at" < ?
        ORDER BY "created_at" ASC, ctid ASC
        LIMIT ?
      )
	`))
	if got := normalizeKitSQL(statements[0].query); got != wantRuns {
		t.Fatalf("PG background_task_runs 语句不匹配：\n%s", statements[0].query)
	}
	wantLeases := normalizeKitSQL(bindTestPG(`
      DELETE FROM juhe_stats.background_job_leases
      WHERE ctid IN (
        SELECT ctid FROM juhe_stats.background_job_leases
        WHERE "lease_until" < ?
        ORDER BY "lease_until" ASC, ctid ASC
        LIMIT ?
      )
	`))
	if got := normalizeKitSQL(statements[1].query); got != wantLeases {
		t.Fatalf("PG background_job_leases 语句不匹配：\n%s", statements[1].query)
	}
}
