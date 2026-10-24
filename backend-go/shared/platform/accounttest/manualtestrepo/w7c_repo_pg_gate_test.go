package manualtestrepo

// w7c PostgreSQL gated end-to-end for the manual test task repository: the
// bindSQL $n rewriting, juhe_business-qualified tables, native boolean
// literals and time.Time instants all run against the pgtestkit isolated test
// database (PLAN-20261024T051925945Z 迁移：原常驻 cover 库
// juhe_ai_sub2api_dev_w1cover 的共享 schema 会过期漂移——2026-10-24 基线即因
// 旧表缺列失败；每测试独立库 + pgkit_test.go 生产 DDL 根治。种子行不再写
// provider_code：供应商解耦后 maintenance 权威 DDL 已删该列。连接串内容不进
// 任何文件、日志或断言）。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func w7cSeedPGTask(t *testing.T, db *sql.DB, id, status string, cancelRequested bool, queuedAt time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.account_test_tasks (
  id, account_id, account_name, provider_protocol_profile_id, protocol_code,
  protocol_version, account_type, request_system_account_id, request_role, diagnostics,
  status, status_message, cancel_requested, queued_at, created_at, updated_at
) VALUES ($1,'w7c-acc','w7c','','chat_completions','v1','api_key','w7c-viewer','user','full',
  $2,'等待后台测试',$3,$4,$5,$5)
ON CONFLICT (id) DO UPDATE SET status = excluded.status, cancel_requested = excluded.cancel_requested,
  updated_at = excluded.updated_at`,
		id, status, cancelRequested, queuedAt.UTC(), now); err != nil {
		t.Fatalf("seed task %s: %v", id, err)
	}
}

func TestW7CManualTestRepoPostgresLifecycle(t *testing.T) {
	db := pgtestkitGateDB(t)

	repo, err := New(Config{DB: db, Postgres: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := repo.ValidateCoreTables(ctx); err != nil {
		t.Fatalf("ValidateCoreTables on bootstrap schema: %v", err)
	}

	now := time.Now().UTC()
	w7cSeedPGTask(t, db, "w7c-pg-task", "queued", false, now)

	record, err := repo.MarkRunning(ctx, "w7c-pg-task")
	if err != nil || record == nil || record.StartedAt == nil {
		t.Fatalf("mark running (pg): %#v %v", record, err)
	}
	startedAt := *record.StartedAt

	// The started_at fence compares native timestamptz values with
	// millisecond precision; the exact fence value must pass.
	if err := repo.UpdateMessage(ctx, "w7c-pg-task", "真实请求测试中", &startedAt); err != nil {
		t.Fatal(err)
	}
	wrongFence := now.Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	if err := repo.UpdateMessage(ctx, "w7c-pg-task", "过期写入", &wrongFence); err != nil {
		t.Fatal(err)
	}
	var message *string
	if err := db.QueryRowContext(ctx, `SELECT status_message FROM juhe_business.account_test_tasks WHERE id = 'w7c-pg-task'`).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if message == nil || *message != "真实请求测试中" {
		t.Fatalf("fenced message: %v", message)
	}

	if err := repo.Complete(ctx, "w7c-pg-task", accounttest.ManualTestTaskExecutorResult{Success: true, Message: "通过", ResultJSON: `{"ok":true}`}, &startedAt); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM juhe_business.account_test_tasks WHERE id = 'w7c-pg-task'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "success" {
		t.Fatalf("complete (pg): %s", status)
	}

	// Fail on a queued task exercises the postgres boolean literal and the
	// native instant binding.
	w7cSeedPGTask(t, db, "w7c-pg-fail", "queued", false, now)
	if err := repo.Fail(ctx, "w7c-pg-fail", "上游失败", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM juhe_business.account_test_tasks WHERE id = 'w7c-pg-fail'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("fail (pg): %s", status)
	}

	// Cancel on a queued task with the postgres dialect.
	w7cSeedPGTask(t, db, "w7c-pg-cancel", "queued", false, now)
	if err := repo.Cancel(ctx, "w7c-pg-cancel", "用户停止", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM juhe_business.account_test_tasks WHERE id = 'w7c-pg-cancel'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "canceled" {
		t.Fatalf("cancel (pg): %s", status)
	}

	// Maintenance start and sweep on the postgres dialect.
	if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "start"}); err != nil {
		t.Fatalf("maintenance start (pg): %v", err)
	}
	w7cSeedPGTask(t, db, "w7c-pg-expired", "queued", false, now.Add(-time.Hour))
	result, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "sweep", MaxQueuedMS: 60_000})
	if err != nil {
		t.Fatalf("maintenance sweep (pg): %v", err)
	}
	expired := false
	for _, taskID := range result.ExpiredQueuedTaskIDs {
		if taskID == "w7c-pg-expired" {
			expired = true
		}
	}
	if !expired {
		t.Fatalf("expired queued task missing: %+v", result.ExpiredQueuedTaskIDs)
	}
}

// pgtestkitGateDB 打开独立测试库（不经 openTestRepo，生命周期门禁自带
// ValidateCoreTables 前置；schema 由同一 prepare 提供）。
func pgtestkitGateDB(t *testing.T) *sql.DB {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, manualTestBusinessSchema)
		return err
	})
	return db
}
