package cleanuprepo

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// ModelCheckRetentionStore 语句形状与真实 PG 边界测试（2026-11-09 新增）。
// 覆盖：runs 时间窗 + items/observations 级联、receipts、claims、outcomes、
// inputs、质量健康小时表、scheduler 终态任务、每账户 1000 条 run 上限。

func TestModelCheckRetentionPGStatementShape(t *testing.T) {
	rec := newPGRecorder()
	store := &ModelCheckRetentionStore{DB: openRecorderPG(rec)}
	ctx := context.Background()
	cutoffText := "2026-10-10T00:00:00Z"
	cutoffTime := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	if _, err := store.CleanupRunsBefore(ctx, cutoffText, 7); err != nil {
		t.Fatalf("cleanup runs: %v", err)
	}
	if _, err := store.CleanupObservationReceiptsBefore(ctx, cutoffText, 7); err != nil {
		t.Fatalf("cleanup receipts: %v", err)
	}
	if _, err := store.CleanupExecutionClaimsBefore(ctx, cutoffTime, 7); err != nil {
		t.Fatalf("cleanup claims: %v", err)
	}
	if _, err := store.CleanupOutcomesBefore(ctx, cutoffTime, 7); err != nil {
		t.Fatalf("cleanup outcomes: %v", err)
	}
	if _, err := store.CleanupInputsBefore(ctx, cutoffTime, 7); err != nil {
		t.Fatalf("cleanup inputs: %v", err)
	}
	if _, err := store.CleanupQualityHealthBefore(ctx, "2026-10-10T00", 7); err != nil {
		t.Fatalf("cleanup quality health: %v", err)
	}
	if _, err := store.CleanupSchedulerTasksBefore(ctx, cutoffTime, cutoffTime, 7); err != nil {
		t.Fatalf("cleanup scheduler tasks: %v", err)
	}
	if _, err := store.CleanupRunsBeyondAccountCap(ctx, 1000, 7); err != nil {
		t.Fatalf("cleanup account cap: %v", err)
	}

	statements := rec.all()
	if len(statements) != 9 {
		t.Fatalf("应恰好录制九条 DELETE，实际 %d 条", len(statements))
	}
	want := []string{
		normalizeKitSQL(bindTestPG(`
      DELETE FROM juhe_j3b.model_check_runs
      WHERE ctid IN (
        SELECT ctid FROM juhe_j3b.model_check_runs
        WHERE "created_at" < ?
        ORDER BY "created_at" ASC, ctid ASC
        LIMIT ?
      )
	`)),
		normalizeKitSQL(bindTestPG(`
      DELETE FROM juhe_j3b.model_trust_observation_receipts
      WHERE ctid IN (
        SELECT ctid FROM juhe_j3b.model_trust_observation_receipts
        WHERE "observation_created_at" < ?
        ORDER BY "observation_created_at" ASC, ctid ASC
        LIMIT ?
      )
	`)),
		normalizeKitSQL(bindTestPG(`
      DELETE FROM juhe_j3b.model_check_execution_claims
      WHERE ctid IN (
        SELECT ctid FROM juhe_j3b.model_check_execution_claims
        WHERE "updated_at" < ?
        ORDER BY "updated_at" ASC, ctid ASC
        LIMIT ?
      )
	`)),
		normalizeKitSQL(bindTestPG(`
      DELETE FROM juhe_j3b.model_check_outcomes
      WHERE ctid IN (
        SELECT ctid FROM juhe_j3b.model_check_outcomes
        WHERE "observed_at" < ?
        ORDER BY "observed_at" ASC, ctid ASC
        LIMIT ?
      )
	`)),
		normalizeKitSQL(bindTestPG(`
      DELETE FROM juhe_j3b.model_check_inputs
      WHERE ctid IN (
        SELECT ctid FROM juhe_j3b.model_check_inputs
        WHERE "issued_at" < ?
        ORDER BY "issued_at" ASC, ctid ASC
        LIMIT ?
      )
	`)),
		normalizeKitSQL(bindTestPG(`
      DELETE FROM juhe_j3b.account_quality_health_hourly
      WHERE ctid IN (
        SELECT ctid FROM juhe_j3b.account_quality_health_hourly
        WHERE "stat_hour" < ?
        ORDER BY "stat_hour" ASC, ctid ASC
        LIMIT ?
      )
	`)),
		normalizeKitSQL(bindTestPG(`
      DELETE FROM juhe_j3b.model_check_scheduler_tasks
      WHERE ctid IN (
        SELECT ctid FROM juhe_j3b.model_check_scheduler_tasks
        WHERE state = 'completed' AND completed_at < ?
        ORDER BY completed_at ASC, ctid ASC
        LIMIT ?
      )
	`)),
		normalizeKitSQL(bindTestPG(`
      DELETE FROM juhe_j3b.model_check_scheduler_tasks
      WHERE ctid IN (
        SELECT ctid FROM juhe_j3b.model_check_scheduler_tasks
        WHERE state = 'failed' AND due_at < ?
        ORDER BY due_at ASC, ctid ASC
        LIMIT ?
      )
	`)),
		normalizeKitSQL(bindTestPG(`
      DELETE FROM juhe_j3b.model_check_runs
      WHERE id IN (
        SELECT id FROM (
          SELECT id, row_number() OVER (
            PARTITION BY account_id
            ORDER BY created_at DESC, id DESC
          ) AS rn
          FROM juhe_j3b.model_check_runs
          WHERE account_id IS NOT NULL AND status <> 'running'
        ) ranked
        WHERE ranked.rn > ?
        ORDER BY ranked.rn DESC
        LIMIT ?
      )
	`)),
	}
	for index, expected := range want {
		if got := normalizeKitSQL(statements[index].query); got != expected {
			t.Fatalf("第 %d 条语句不匹配：\n%s", index+1, statements[index].query)
		}
	}
}

func TestModelCheckRetentionPGRollingWindow(t *testing.T) {
	ctx := context.Background()
	db := openKitPostgres(t, func(ctx context.Context, db *sql.DB) error {
		execKitSchema(t, db,
			`CREATE SCHEMA IF NOT EXISTS juhe_j3b`,
			`CREATE TABLE juhe_j3b.model_check_runs (id text PRIMARY KEY, account_id text, status text NOT NULL DEFAULT 'completed', created_at text NOT NULL)`,
			`CREATE TABLE juhe_j3b.model_check_items (id text PRIMARY KEY, run_id text NOT NULL REFERENCES juhe_j3b.model_check_runs(id) ON DELETE CASCADE)`,
			`CREATE TABLE juhe_j3b.model_check_observations (id text PRIMARY KEY, run_id text NOT NULL REFERENCES juhe_j3b.model_check_runs(id) ON DELETE CASCADE)`,
			`CREATE TABLE juhe_j3b.model_trust_observation_receipts (observation_id text PRIMARY KEY, observation_created_at text NOT NULL)`,
			`CREATE TABLE juhe_j3b.model_check_execution_claims (input_id text PRIMARY KEY, updated_at timestamptz NOT NULL)`,
			`CREATE TABLE juhe_j3b.model_check_outcomes (outcome_id text PRIMARY KEY, observed_at timestamptz NOT NULL)`,
			`CREATE TABLE juhe_j3b.model_check_inputs (input_id text PRIMARY KEY, issued_at timestamptz NOT NULL)`,
			`CREATE TABLE juhe_j3b.account_quality_health_hourly (account_id text NOT NULL, stat_hour text NOT NULL, PRIMARY KEY (account_id, stat_hour))`,
			`CREATE TABLE juhe_j3b.model_check_scheduler_tasks (id text PRIMARY KEY, state text NOT NULL, due_at timestamptz NOT NULL, completed_at timestamptz)`,
		)
		return nil
	})
	store := &ModelCheckRetentionStore{DB: db}
	cutoffText := "2026-10-10T00:00:00Z"
	cutoff := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	seedRun := func(id, account, createdAt string) {
		if _, err := db.ExecContext(ctx, `INSERT INTO juhe_j3b.model_check_runs (id, account_id, created_at) VALUES ($1, $2, $3)`, id, account, createdAt); err != nil {
			t.Fatalf("seed run %s: %v", id, err)
		}
	}
	seedRun("run-old", "acct-1", "2026-10-09T23:59:59Z")
	seedRun("run-boundary", "acct-1", cutoffText)
	seedRun("run-new", "acct-1", "2026-10-10T00:00:01Z")
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_j3b.model_check_items (id, run_id) VALUES ('item-old', 'run-old')`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_j3b.model_check_observations (id, run_id) VALUES ('obs-old', 'run-old')`); err != nil {
		t.Fatalf("seed observation: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_j3b.model_trust_observation_receipts (observation_id, observation_created_at) VALUES ('obs-old', $1)`, "2026-10-09T23:00:00Z"); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_j3b.model_check_execution_claims (input_id, updated_at) VALUES ('input-old', $1)`, cutoff.Add(-time.Hour)); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_j3b.model_check_outcomes (outcome_id, observed_at) VALUES ('outcome-old', $1)`, cutoff.Add(-time.Hour)); err != nil {
		t.Fatalf("seed outcome: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_j3b.model_check_inputs (input_id, issued_at) VALUES ('input-old', $1)`, cutoff.Add(-time.Hour)); err != nil {
		t.Fatalf("seed input: %v", err)
	}
	// stat_hour 生产写入格式是业务时区 YYYY-MM-DDTHH（gateway
	// NewHealthStatHourFunc），不是 RFC3339；边界行必须保留。
	for _, row := range []struct{ account, statHour string }{
		{"acct-old", "2026-10-09T23"},
		{"acct-boundary", "2026-10-10T00"},
		{"acct-new", "2026-10-10T01"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO juhe_j3b.account_quality_health_hourly (account_id, stat_hour) VALUES ($1, $2)`, row.account, row.statHour); err != nil {
			t.Fatalf("seed quality %s: %v", row.statHour, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_j3b.model_check_scheduler_tasks (id, state, due_at, completed_at) VALUES ('task-completed-old', 'completed', $1, $1)`, cutoff.Add(-time.Hour)); err != nil {
		t.Fatalf("seed completed task: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_j3b.model_check_scheduler_tasks (id, state, due_at, completed_at) VALUES ('task-failed-old', 'failed', $1, NULL)`, cutoff.Add(-time.Hour)); err != nil {
		t.Fatalf("seed failed task: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_j3b.model_check_scheduler_tasks (id, state, due_at, completed_at) VALUES ('task-pending-future', 'pending', $1, NULL)`, cutoff.Add(time.Hour)); err != nil {
		t.Fatalf("seed pending task: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_j3b.model_check_scheduler_tasks (id, state, due_at, completed_at) VALUES ('task-pending-old', 'pending', $1, NULL)`, cutoff.Add(-time.Hour)); err != nil {
		t.Fatalf("seed pending old task: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_j3b.model_check_scheduler_tasks (id, state, due_at, completed_at) VALUES ('task-failed-future', 'failed', $1, NULL)`, cutoff.Add(time.Hour)); err != nil {
		t.Fatalf("seed failed future task: %v", err)
	}

	if deleted, err := store.CleanupRunsBefore(ctx, cutoffText, 10); err != nil || deleted != 1 {
		t.Fatalf("runs cleanup deleted=%d err=%v, want 1", deleted, err)
	}
	var items, observations int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_j3b.model_check_items`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_j3b.model_check_observations`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if items != 0 || observations != 0 {
		t.Fatalf("items/observations 必须随 run 级联删除：items=%d observations=%d", items, observations)
	}
	if deleted, err := store.CleanupObservationReceiptsBefore(ctx, cutoffText, 10); err != nil || deleted != 1 {
		t.Fatalf("receipts cleanup deleted=%d err=%v, want 1", deleted, err)
	}
	if deleted, err := store.CleanupExecutionClaimsBefore(ctx, cutoff, 10); err != nil || deleted != 1 {
		t.Fatalf("claims cleanup deleted=%d err=%v, want 1", deleted, err)
	}
	if deleted, err := store.CleanupOutcomesBefore(ctx, cutoff, 10); err != nil || deleted != 1 {
		t.Fatalf("outcomes cleanup deleted=%d err=%v, want 1", deleted, err)
	}
	if deleted, err := store.CleanupInputsBefore(ctx, cutoff, 10); err != nil || deleted != 1 {
		t.Fatalf("inputs cleanup deleted=%d err=%v, want 1", deleted, err)
	}
	if deleted, err := store.CleanupQualityHealthBefore(ctx, "2026-10-10T00", 10); err != nil || deleted != 1 {
		t.Fatalf("quality health cleanup deleted=%d err=%v, want 1", deleted, err)
	}
	var qualityRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_j3b.account_quality_health_hourly`).Scan(&qualityRows); err != nil {
		t.Fatal(err)
	}
	if qualityRows != 2 {
		t.Fatalf("质量健康小时表必须保留 boundary/new 两行：%d", qualityRows)
	}
	if deleted, err := store.CleanupSchedulerTasksBefore(ctx, cutoff, cutoff, 10); err != nil || deleted != 2 {
		t.Fatalf("scheduler tasks cleanup deleted=%d err=%v, want 2", deleted, err)
	}
	var pending int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_j3b.model_check_scheduler_tasks WHERE state='pending'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 2 {
		t.Fatalf("pending 任务不得删除（含 due_at 已过期的 pending）：%d", pending)
	}
	var failedFuture int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_j3b.model_check_scheduler_tasks WHERE id='task-failed-future'`).Scan(&failedFuture); err != nil {
		t.Fatal(err)
	}
	if failedFuture != 1 {
		t.Fatalf("due_at 在未来的 failed 任务不得删除：%d", failedFuture)
	}
}

func TestModelCheckRetentionPGAccountRunCap(t *testing.T) {
	ctx := context.Background()
	db := openKitPostgres(t, func(ctx context.Context, db *sql.DB) error {
		execKitSchema(t, db,
			`CREATE SCHEMA IF NOT EXISTS juhe_j3b`,
			`CREATE TABLE juhe_j3b.model_check_runs (id text PRIMARY KEY, account_id text, status text NOT NULL DEFAULT 'completed', created_at text NOT NULL)`,
			`CREATE TABLE juhe_j3b.model_check_items (id text PRIMARY KEY, run_id text NOT NULL REFERENCES juhe_j3b.model_check_runs(id) ON DELETE CASCADE)`,
		)
		return nil
	})
	store := &ModelCheckRetentionStore{DB: db}
	for index, id := range []string{"run-1", "run-2", "run-3"} {
		createdAt := time.Date(2026, 11, 1, 0, index, 0, 0, time.UTC).Format(time.RFC3339)
		if _, err := db.ExecContext(ctx, `INSERT INTO juhe_j3b.model_check_runs (id, account_id, created_at) VALUES ($1, 'acct-1', $2)`, id, createdAt); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO juhe_j3b.model_check_items (id, run_id) VALUES ($1, $2)`, "item-"+id, id); err != nil {
			t.Fatalf("seed item %s: %v", id, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_j3b.model_check_runs (id, account_id, status, created_at) VALUES ('run-other', 'acct-2', 'completed', $1)`, "2026-11-01T00:00:00Z"); err != nil {
		t.Fatalf("seed other account: %v", err)
	}
	// 运行中的 run 不计入容量候选：即使它是最旧行也必须保留。
	if _, err := db.ExecContext(ctx, `INSERT INTO juhe_j3b.model_check_runs (id, account_id, status, created_at) VALUES ('run-running', 'acct-1', 'running', '2026-10-31T23:00:00Z')`); err != nil {
		t.Fatalf("seed running run: %v", err)
	}
	deleted, err := store.CleanupRunsBeyondAccountCap(ctx, 2, 10)
	if err != nil {
		t.Fatalf("account cap cleanup: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("account cap deleted = %d, want 1", deleted)
	}
	var remaining int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_j3b.model_check_runs WHERE account_id='acct-1'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 3 {
		t.Fatalf("acct-1 remaining = %d, want 3 (2 completed + 1 running)", remaining)
	}
	var oldest int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_j3b.model_check_runs WHERE id='run-1'`).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if oldest != 0 {
		t.Fatal("必须删除最旧 completed run")
	}
	var running int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_j3b.model_check_runs WHERE id='run-running'`).Scan(&running); err != nil {
		t.Fatal(err)
	}
	if running != 1 {
		t.Fatal("running run 不得被容量清理删除")
	}
	var other int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_j3b.model_check_runs WHERE account_id='acct-2'`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if other != 1 {
		t.Fatalf("acct-2 不得被清理：%d", other)
	}
}
