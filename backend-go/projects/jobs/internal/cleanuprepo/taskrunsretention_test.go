package cleanuprepo

import (
	"context"
	"testing"
)

// TaskRunsRetentionStore 测试（缺陷1修复）：background_task_runs /
// background_job_leases 此前全仓无删除路径，本节锁定滚动保留清理的
// 删除边界（严格早于 cutoff、最旧优先、受 limit 约束）与双模方言形状。
// DDL 只建清理谓词涉及的最小列（created_at / lease_until），与
// taskruns.Store 冻结 schema 的时间列同名同 TEXT 语义。真实建库测试迁
// pgtestkit 隔离 PG 库（PLAN-20261024T051925945Z B1 POC#2），表建在
// 生产 PG schema juhe_stats 下，与 deleteRowsBefore 的 ctid 臂精确对齐。

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
