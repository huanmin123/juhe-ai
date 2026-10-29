package cleanuprepo

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// W13 兜底清扫（2026-10-29）：AuditShellStore 的删除边界必须精确——
// 只删「cutoff 之前 + in_progress + metadata_only」三条件同时命中的死行；
// 终态行、有载荷行、新鲜信封一概不动；受 limit 约束且最旧优先。
func TestAuditShellStoreReapStaleInProgressPG(t *testing.T) {
	store := &AuditShellStore{DB: openKitPostgres(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `CREATE SCHEMA juhe_dataset`); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `
			CREATE TABLE juhe_dataset.audit_logs (
				id text PRIMARY KEY,
				lifecycle_status text NOT NULL,
				capture_status text NOT NULL,
				created_at text NOT NULL
			)`)
		return err
	})}
	ctx := context.Background()
	cutoff := time.Date(2026, 10, 29, 6, 0, 0, 0, time.UTC)
	old := cutoff.Add(-time.Hour).Format(time.RFC3339Nano)
	fresh := cutoff.Add(time.Hour).Format(time.RFC3339Nano)

	seed := func(id, lifecycle, capture, createdAt string) {
		t.Helper()
		if _, err := store.DB.ExecContext(ctx,
			store.DB.Bind(`INSERT INTO juhe_dataset.audit_logs (id, lifecycle_status, capture_status, created_at) VALUES (?,?,?,?)`),
			id, lifecycle, capture, createdAt); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	// 三个旧空壳（应删，最旧优先，limit=2 时只删前两条）+ 三类不得误删的行。
	seed("shell-oldest", "in_progress", "metadata_only", cutoff.Add(-3*time.Hour).Format(time.RFC3339Nano))
	seed("shell-second", "in_progress", "metadata_only", cutoff.Add(-2*time.Hour).Format(time.RFC3339Nano))
	seed("shell-third", "in_progress", "metadata_only", old)
	seed("fresh-shell", "in_progress", "metadata_only", fresh)
	seed("finalized-old", "finalized", "complete", old)
	seed("payload-old", "in_progress", "complete", old)

	reaped, err := store.ReapStaleInProgress(ctx, cutoff, 2)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if reaped != 2 {
		t.Fatalf("limit=2 应删 2 行，实际 %d", reaped)
	}
	remaining := map[string]bool{}
	rows, err := store.DB.QueryContext(ctx, `SELECT id FROM juhe_dataset.audit_logs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		remaining[id] = true
	}
	for _, id := range []string{"shell-oldest", "shell-second"} {
		if remaining[id] {
			t.Fatalf("最旧空壳 %s 应被删除", id)
		}
	}
	for _, id := range []string{"shell-third", "fresh-shell", "finalized-old", "payload-old"} {
		if !remaining[id] {
			t.Fatalf("不得误删 %s", id)
		}
	}
}

// 非 PG 句柄必须显式报错（存储形态已收敛为 PostgreSQL，静默跳过会隐藏接线错误）。
func TestAuditShellStoreRejectsNonPostgres(t *testing.T) {
	store := &AuditShellStore{DB: &DB{}}
	if _, err := store.ReapStaleInProgress(context.Background(), time.Now(), 10); err == nil {
		t.Fatal("非 PostgreSQL 句柄必须报错")
	}
}
