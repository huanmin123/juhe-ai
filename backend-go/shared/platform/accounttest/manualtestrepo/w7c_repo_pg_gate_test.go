//go:build windows

package manualtestrepo

// w7c PostgreSQL gated end-to-end for the manual test task repository: the
// bindSQL $n rewriting, juhe_business-qualified tables, native boolean
// literals and time.Time instants all run against the real temp cover
// database (juhe_ai_sub2api_dev_w1cover, same isolation rules as the
// accountbalance w7c gate: env-only credentials, skip when unreachable,
// w7c- prefixed rows cleaned up afterwards).

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func w7cOpenCoverDB(t *testing.T) *sql.DB {
	t.Helper()
	env := w7cSharedEnv(t)
	appURL := env["JUHE_AI_POSTGRES_URL"]
	if env["DEV_POSTGRES_HOST"] == "" || appURL == "" {
		t.Skipf("dev env 缺少 PG 键（跳过）")
	}
	admin, err := sql.Open("pgx", "postgres://"+env["DEV_POSTGRES_ADMIN_USERNAME"]+":"+env["DEV_POSTGRES_ADMIN_PASSWORD"]+"@"+env["DEV_POSTGRES_HOST"]+":"+env["DEV_POSTGRES_DIRECT_PORT"]+"/postgres?sslmode=disable")
	if err != nil {
		t.Fatalf("打开 dev PG 管理连接失败: %v", err)
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Skipf("dev PG 管理口不可达（跳过）: %v", err)
	}
	var exists bool
	if err := admin.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, w7cCoverDBName).Scan(&exists); err != nil {
		t.Fatalf("查询临时子库失败: %v", err)
	}
	if !exists {
		owner := env["DEV_POSTGRES_APP_USERNAME"]
		if owner == "" {
			owner = env["DEV_POSTGRES_ADMIN_USERNAME"]
		}
		if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+w7cCoverDBName+` OWNER `+owner); err != nil {
			t.Fatalf("创建临时子库失败: %v", err)
		}
	}
	sep := strings.LastIndex(appURL, "/")
	if sep < 0 {
		t.Skipf("dev env JUHE_AI_POSTGRES_URL 形态异常（跳过）")
	}
	db, err := sql.Open("pgx", appURL[:sep+1]+w7cCoverDBName)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("临时子库不可达: %v", err)
	}
	// 临时子库可能从未引导过业务表（其他波次只建了自己需要的表）；这里
	// 按 maintenance 权威 DDL 幂等补建本包契约校验的三张表（加法、可重入）。
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_business`); err != nil {
		t.Fatalf("补建 juhe_business schema 失败: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS juhe_business.account_test_tasks (
      id text PRIMARY KEY,
      account_id text NOT NULL,
      account_name text NOT NULL,
      provider_code text NOT NULL,
      provider_protocol_profile_id text NOT NULL,
      protocol_code text NOT NULL,
      protocol_version text NOT NULL,
      account_type text NOT NULL,
      request_system_account_id text NOT NULL,
      request_role text NOT NULL,
      request_system_account_filter_id text,
      diagnostics text NOT NULL DEFAULT 'full',
      model text,
      test_endpoint_mode text,
      draft_account_encrypted text,
      status text NOT NULL DEFAULT 'queued',
      status_message text,
      result_json text,
      error_message text,
      cancel_requested boolean NOT NULL DEFAULT false,
      queued_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00',
      queued_deadline_at timestamptz,
      started_at timestamptz,
      finished_at timestamptz,
      created_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00',
      updated_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00'
    )`,
		`CREATE TABLE IF NOT EXISTS juhe_business.account_test_sessions (
      id text PRIMARY KEY,
      request_system_account_id text NOT NULL,
      request_role text NOT NULL,
      request_system_account_filter_id text,
      status text NOT NULL DEFAULT 'running',
      cancel_reason text,
      last_heartbeat_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00',
      cancel_requested_at timestamptz,
      finished_at timestamptz,
      created_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00',
      updated_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00',
      CHECK (status IN ('running', 'canceled', 'expired', 'completed'))
    )`,
		`CREATE TABLE IF NOT EXISTS juhe_business.account_test_session_tasks (
      session_id text NOT NULL,
      task_id text NOT NULL,
      created_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00',
      PRIMARY KEY (session_id, task_id)
    )`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("补建手动测试契约表失败: %v", err)
		}
	}
	return db
}

const w7cCoverDBName = "juhe_ai_sub2api_dev_w1cover"

func w7cSharedEnv(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("../../../../../.local/project-resources/dev/env/shared.env")
	if err != nil {
		t.Skipf("dev env 不可达（跳过 PG 门禁测试）: %v", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return values
}

func w7cSeedPGTask(t *testing.T, db *sql.DB, id, status string, cancelRequested bool, queuedAt time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.account_test_tasks (
  id, account_id, account_name, provider_code, provider_protocol_profile_id, protocol_code,
  protocol_version, account_type, request_system_account_id, request_role, diagnostics,
  status, status_message, cancel_requested, queued_at, created_at, updated_at
) VALUES ($1,'w7c-acc','w7c','openai','','chat_completions','v1','api_key','w7c-viewer','user','full',
  $2,'等待后台测试',$3::boolean,$4::timestamptz,$5::timestamptz,$5::timestamptz)
ON CONFLICT (id) DO UPDATE SET status = excluded.status, cancel_requested = excluded.cancel_requested,
  updated_at = excluded.updated_at`,
		id, status, cancelRequested, queuedAt.UTC(), now); err != nil {
		t.Fatalf("seed task %s: %v", id, err)
	}
}

func TestW7CManualTestRepoPostgresLifecycle(t *testing.T) {
	db := w7cOpenCoverDB(t)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, statement := range []string{
			`DELETE FROM juhe_business.account_test_session_tasks WHERE session_id LIKE 'w7c-%' OR task_id LIKE 'w7c-%'`,
			`DELETE FROM juhe_business.account_test_tasks WHERE id LIKE 'w7c-%'`,
			`DELETE FROM juhe_business.account_test_sessions WHERE id LIKE 'w7c-%'`,
		} {
			_, _ = db.ExecContext(ctx, statement)
		}
	})

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
