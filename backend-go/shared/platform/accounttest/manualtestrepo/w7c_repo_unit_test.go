package manualtestrepo

// w7c contract completion for the manual test task repository: the postgres
// bindSQL placeholder rewriting, the dialect boolean/instant params, the Fail
// lifecycle, session-cancel reason precedence, idle-session completion,
// expired-row cleanup, and the small formatting helpers. The gated PG
// end-to-end lives in w7c_repo_pg_gate_test.go.

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest"
)

func w7cPostgresRepo() *Repo {
	return &Repo{postgres: true, now: func() time.Time { return time.Now().UTC() }}
}

func TestW7CBindSQLPlaceholderRewrite(t *testing.T) {
	repo := w7cPostgresRepo()
	sqlite := &Repo{postgres: false}

	// Plain ? placeholders become $1..$n in order.
	got := repo.bindSQL("SELECT * FROM t WHERE a = ? AND b = ? LIMIT ?")
	if got != "SELECT * FROM t WHERE a = $1 AND b = $2 LIMIT $3" {
		t.Fatalf("rewrite: %q", got)
	}
	// String literals keep their ? untouched and do not consume numbering.
	got = repo.bindSQL("SELECT 'a?b' FROM t WHERE c = ? AND d = 'x ? y' AND e = ?")
	if got != "SELECT 'a?b' FROM t WHERE c = $1 AND d = 'x ? y' AND e = $2" {
		t.Fatalf("literal guard: %q", got)
	}
	// SQLite dialect passes through untouched.
	const query = "UPDATE t SET a = ? WHERE b = ? OR c = 'w?z'"
	if sqlite.bindSQL(query) != query {
		t.Fatalf("sqlite passthrough: %q", sqlite.bindSQL(query))
	}
}

func TestW7CDialectHelpers(t *testing.T) {
	pg := w7cPostgresRepo()
	if pg.boolTrue() != "TRUE" || pg.boolFalse() != "FALSE" {
		t.Fatalf("postgres booleans: %q %q", pg.boolTrue(), pg.boolFalse())
	}
	sqlite := &Repo{}
	if sqlite.boolTrue() != "1" || sqlite.boolFalse() != "0" {
		t.Fatalf("sqlite booleans: %q %q", sqlite.boolTrue(), sqlite.boolFalse())
	}
	if pg.table("account_test_tasks") != "juhe_business.account_test_tasks" {
		t.Fatalf("postgres table: %q", pg.table("account_test_tasks"))
	}
	if sqlite.table("account_test_tasks") != "account_test_tasks" {
		t.Fatalf("sqlite table: %q", sqlite.table("account_test_tasks"))
	}

	parsed := time.Date(2026, 9, 14, 8, 0, 0, 123456789, time.UTC)
	text := parsed.Format(time.RFC3339Nano)
	if got := instantParam(true, text, time.Now); got != parsed {
		t.Fatalf("postgres instant: %#v", got)
	}
	if got := instantParam(true, "not-a-time", time.Now); got != "not-a-time" {
		t.Fatalf("postgres fallback: %#v", got)
	}
	if got := instantParam(false, text, time.Now); got != text {
		t.Fatalf("sqlite passthrough: %#v", got)
	}

	if got := sqlString(true, "envelope"); got != nil {
		t.Fatalf("success clears the envelope: %#v", got)
	}
	if got := sqlString(false, "envelope"); got != "envelope" {
		t.Fatalf("failure keeps the envelope: %#v", got)
	}
	if got := nullIfEmpty("   "); got != nil {
		t.Fatalf("blank envelope: %#v", got)
	}
	if got := nullIfEmpty(" x "); got != " x " {
		t.Fatalf("kept envelope: %#v", got)
	}
}

func TestW7CFormatQueuedWaitAndMaxInt(t *testing.T) {
	cases := map[int64]string{
		0:            "1 秒",
		999:          "1 秒",
		45_000:       "45 秒",
		61_000:       "2 分钟",
		90 * 60_000:  "2 小时",
		120 * 60_000: "2 小时",
	}
	for input, want := range cases {
		if got := formatQueuedWait(input); got != want {
			t.Fatalf("formatQueuedWait(%d) = %q, want %q", input, got, want)
		}
	}
	if !strings.Contains(queuedWaitExpiredMessage(10*60_000), "10 分钟") {
		t.Fatalf("message wording: %q", queuedWaitExpiredMessage(10*60_000))
	}
	if maxInt(3, 1) != 3 || maxInt(1, 3) != 3 {
		t.Fatal("maxInt contract")
	}
	if placeholdersFor(1) != "?" || placeholdersFor(3) != "?, ?, ?" {
		t.Fatalf("placeholders: %q %q", placeholdersFor(1), placeholdersFor(3))
	}
}

func TestW7CNewAndValidateCoreTables(t *testing.T) {
	if _, err := New(Config{}); err == nil || !strings.Contains(err.Error(), "缺少业务库句柄") {
		t.Fatalf("nil db: %v", err)
	}
	repo, db, _ := openTestRepo(t)
	ctx := context.Background()
	if err := repo.ValidateCoreTables(ctx); err != nil {
		t.Fatalf("existing tables: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE account_test_session_tasks`); err != nil {
		t.Fatal(err)
	}
	if err := repo.ValidateCoreTables(ctx); err == nil || !strings.Contains(err.Error(), "account_test_session_tasks") {
		t.Fatalf("missing table: %v", err)
	}
	// Now defaults to the wall clock.
	if _, err := New(Config{DB: db}); err != nil {
		t.Fatalf("default now: %v", err)
	}
}

func TestW7CMarkRunningArms(t *testing.T) {
	repo, db, _ := openTestRepo(t)
	ctx := context.Background()

	// Missing task: nil record, nil error.
	record, err := repo.MarkRunning(ctx, "w7c-missing")
	if err != nil || record != nil {
		t.Fatalf("missing task: %#v %v", record, err)
	}
	// Invalid id: nil record, nil error.
	if record, err = repo.MarkRunning(ctx, "   "); err != nil || record != nil {
		t.Fatalf("invalid id: %#v %v", record, err)
	}
	// Terminal task: claim misses, status stays.
	seedTask(t, db, "w7c-done", "success", 0, time.Now().UTC())
	if record, err = repo.MarkRunning(ctx, "w7c-done"); err != nil || record != nil {
		t.Fatalf("terminal task: %#v %v", record, err)
	}
	// queued with cancel_requested: claim misses and the task is finalized.
	seedTask(t, db, "w7c-cx", "queued", 1, time.Now().UTC())
	if record, err = repo.MarkRunning(ctx, "w7c-cx"); err != nil || record != nil {
		t.Fatalf("canceled queued: %#v %v", record, err)
	}
	var status, message string
	if err := db.QueryRow(`SELECT status, status_message FROM account_test_tasks WHERE id = 'w7c-cx'`).Scan(&status, &message); err != nil {
		t.Fatal(err)
	}
	// The seeded queued row already carries a status message, and
	// markCanceledTx preserves an existing message over the default.
	if status != "canceled" || message != "等待后台测试" {
		t.Fatalf("canceled queued finalization: %s %s", status, message)
	}
}

func TestW7CFailLifecycle(t *testing.T) {
	repo, db, _ := openTestRepo(t)
	ctx := context.Background()

	// queued → failed with result envelope.
	seedTask(t, db, "w7c-fail-q", "queued", 0, time.Now().UTC())
	if err := repo.Fail(ctx, "w7c-fail-q", "上游 5xx", `{"error":"upstream"}`, nil); err != nil {
		t.Fatal(err)
	}
	var status, message, resultJSON, errorMessage sql.NullString
	if err := db.QueryRow(`SELECT status, status_message, result_json, error_message FROM account_test_tasks WHERE id = 'w7c-fail-q'`).Scan(&status, &message, &resultJSON, &errorMessage); err != nil {
		t.Fatal(err)
	}
	if status.String != "failed" || message.String != "上游 5xx" || resultJSON.String != `{"error":"upstream"}` || errorMessage.String != "上游 5xx" {
		t.Fatalf("failed queued: %s/%s/%s/%s", status.String, message.String, resultJSON.String, errorMessage.String)
	}

	// Blank message falls back to the Node default wording.
	seedTask(t, db, "w7c-fail-blank", "queued", 0, time.Now().UTC())
	if err := repo.Fail(ctx, "w7c-fail-blank", "   ", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status_message FROM account_test_tasks WHERE id = 'w7c-fail-blank'`).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if message.String != "账号测试任务执行失败" {
		t.Fatalf("default failure message: %q", message.String)
	}

	// Invalid ids are ignored.
	if err := repo.Fail(ctx, "  ", "x", "", nil); err != nil {
		t.Fatalf("invalid id: %v", err)
	}

	// Fenced fail with a wrong started_at misses and leaves the row running.
	seedTask(t, db, "w7c-fail-running", "running", 0, time.Now().UTC())
	wrongFence := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if err := repo.Fail(ctx, "w7c-fail-running", "late", "", &wrongFence); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM account_test_tasks WHERE id = 'w7c-fail-running'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status.String != "running" {
		t.Fatalf("wrong fence must not fail the task: %s", status.String)
	}

	// cancel_requested=1 + running: the missed write finalizes the task as
	// canceled and reuses its progress message.
	seedTask(t, db, "w7c-fail-cx", "running", 1, time.Now().UTC())
	if _, err := db.Exec(`UPDATE account_test_tasks SET status_message = '进行中' WHERE id = 'w7c-fail-cx'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.Fail(ctx, "w7c-fail-cx", "late", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status, status_message FROM account_test_tasks WHERE id = 'w7c-fail-cx'`).Scan(&status, &message); err != nil {
		t.Fatal(err)
	}
	if status.String != "canceled" || message.String != "进行中" {
		t.Fatalf("cancel finalize keeps the message: %s %s", status.String, message.String)
	}

	// Same path with a blank record message uses the default wording.
	seedTask(t, db, "w7c-fail-cx2", "running", 1, time.Now().UTC())
	if _, err := db.Exec(`UPDATE account_test_tasks SET status_message = NULL WHERE id = 'w7c-fail-cx2'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.Fail(ctx, "w7c-fail-cx2", "late", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status_message FROM account_test_tasks WHERE id = 'w7c-fail-cx2'`).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if message.String != "已停止测试" {
		t.Fatalf("blank finalize message: %q", message.String)
	}
}

func TestW7CCompleteFenceAndCancelFinalize(t *testing.T) {
	repo, db, _ := openTestRepo(t)
	ctx := context.Background()

	seedTask(t, db, "w7c-complete", "queued", 0, time.Now().UTC())
	record, err := repo.MarkRunning(ctx, "w7c-complete")
	if err != nil || record == nil || record.StartedAt == nil {
		t.Fatalf("mark running: %#v %v", record, err)
	}
	startedAt := *record.StartedAt

	// Success with the matching fence writes the envelope and clears the
	// error message.
	if err := repo.Complete(ctx, "w7c-complete", accounttest.ManualTestTaskExecutorResult{Success: true, Message: "通过", ResultJSON: `{"latency_ms":12}`}, &startedAt); err != nil {
		t.Fatal(err)
	}
	var status, resultJSON, errorMessage sql.NullString
	if err := db.QueryRow(`SELECT status, result_json, error_message FROM account_test_tasks WHERE id = 'w7c-complete'`).Scan(&status, &resultJSON, &errorMessage); err != nil {
		t.Fatal(err)
	}
	if status.String != "success" || resultJSON.String != `{"latency_ms":12}` || errorMessage.Valid {
		t.Fatalf("complete: %s/%s/%v", status.String, resultJSON.String, errorMessage)
	}

	// Completing a canceled running task finalizes it as canceled.
	seedTask(t, db, "w7c-complete-cx", "queued", 0, time.Now().UTC())
	if record, err = repo.MarkRunning(ctx, "w7c-complete-cx"); err != nil || record == nil {
		t.Fatalf("mark running cx: %#v %v", record, err)
	}
	if _, err := db.Exec(`UPDATE account_test_tasks SET cancel_requested = 1 WHERE id = 'w7c-complete-cx'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.Complete(ctx, "w7c-complete-cx", accounttest.ManualTestTaskExecutorResult{Success: true, Message: "late"}, record.StartedAt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM account_test_tasks WHERE id = 'w7c-complete-cx'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status.String != "canceled" {
		t.Fatalf("canceled finalize: %s", status.String)
	}

	// Complete on a missing task is a no-op.
	if err := repo.Complete(ctx, "w7c-complete-missing", accounttest.ManualTestTaskExecutorResult{Success: true}, nil); err != nil {
		t.Fatalf("missing complete: %v", err)
	}
	// UpdateMessage with an invalid message is ignored.
	if err := repo.UpdateMessage(ctx, "w7c-complete", "   ", nil); err != nil {
		t.Fatalf("blank message: %v", err)
	}
	// Cancel with a blank message uses the default wording.
	seedTask(t, db, "w7c-cancel-default", "queued", 0, time.Now().UTC())
	if err := repo.Cancel(ctx, "w7c-cancel-default", "  ", nil); err != nil {
		t.Fatal(err)
	}
	var cancelMessage string
	if err := db.QueryRow(`SELECT status_message FROM account_test_tasks WHERE id = 'w7c-cancel-default'`).Scan(&cancelMessage); err != nil {
		t.Fatal(err)
	}
	if cancelMessage != "已停止测试" {
		t.Fatalf("cancel default message: %q", cancelMessage)
	}
}

func w7cSeedSession(t *testing.T, db *sql.DB, id, status string, cancelReason *string, heartbeat time.Time) {
	t.Helper()
	reason := any(nil)
	if cancelReason != nil {
		reason = *cancelReason
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO account_test_sessions (
			id, request_system_account_id, request_role, status, cancel_reason,
			last_heartbeat_at, created_at, updated_at
		) VALUES (?, 'viewer-1', 'user', ?, ?, ?, ?, ?)`,
		id, status, reason, heartbeat.UTC().Format(time.RFC3339Nano), now, now); err != nil {
		t.Fatal(err)
	}
}

func TestW7CSessionCancelReasonMatrix(t *testing.T) {
	repo, db, _ := openTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	reason := "运维取消"
	seedTask(t, db, "t-none", "queued", 0, now)
	seedTask(t, db, "t-cx", "queued", 0, now)
	seedTask(t, db, "t-cx-bare", "queued", 0, now)
	seedTask(t, db, "t-exp", "queued", 0, now)
	seedTask(t, db, "t-exp-bare", "queued", 0, now)
	seedTask(t, db, "t-done", "queued", 0, now)
	seedTask(t, db, "t-done-bare", "queued", 0, now)
	seedTask(t, db, "t-running", "queued", 0, now)

	w7cSeedSession(t, db, "s-cx", "canceled", &reason, now)
	w7cSeedSession(t, db, "s-cx-bare", "canceled", nil, now)
	w7cSeedSession(t, db, "s-exp", "expired", &reason, now)
	w7cSeedSession(t, db, "s-exp-bare", "expired", nil, now)
	w7cSeedSession(t, db, "s-done", "completed", &reason, now)
	w7cSeedSession(t, db, "s-done-bare", "completed", nil, now)
	w7cSeedSession(t, db, "s-running", "running", nil, now)
	for _, pair := range [][2]string{
		{"s-cx", "t-cx"}, {"s-cx-bare", "t-cx-bare"}, {"s-exp", "t-exp"},
		{"s-exp-bare", "t-exp-bare"}, {"s-done", "t-done"}, {"s-done-bare", "t-done-bare"},
		{"s-running", "t-running"},
	} {
		if _, err := db.Exec(`INSERT INTO account_test_session_tasks (session_id, task_id) VALUES (?, ?)`, pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}

	expect := map[string]string{
		"t-none":      "",
		"t-cx":        reason,
		"t-cx-bare":   "已停止测试",
		"t-exp":       reason,
		"t-exp-bare":  "账户测试会话已过期",
		"t-done":      reason,
		"t-done-bare": "账户测试会话已结束",
		"t-running":   "",
	}
	for taskID, want := range expect {
		got, err := repo.sessionCancelReason(ctx, repo.db, taskID)
		if err != nil {
			t.Fatalf("%s: %v", taskID, err)
		}
		if got != want {
			t.Fatalf("%s reason = %q, want %q", taskID, got, want)
		}
	}
}

func TestW7CMaintenanceCleanupAndIdleSessions(t *testing.T) {
	repo, db, _ := openTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)

	// Finished task older than retention: cleaned; fresh: kept.
	seedTask(t, db, "w7c-old-done", "success", 0, old)
	if _, err := db.Exec(`UPDATE account_test_tasks SET finished_at = ? WHERE id = 'w7c-old-done'`, old.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	seedTask(t, db, "w7c-new-done", "success", 0, now)

	// Idle running session without active tasks: completed.
	w7cSeedSession(t, db, "w7c-idle", "running", nil, old)
	// Running session with a queued task: kept even when idle.
	w7cSeedSession(t, db, "w7c-busy", "running", nil, old)
	seedTask(t, db, "w7c-busy-task", "queued", 0, now)
	if _, err := db.Exec(`INSERT INTO account_test_session_tasks (session_id, task_id) VALUES ('w7c-busy', 'w7c-busy-task')`); err != nil {
		t.Fatal(err)
	}
	// Heartbeating session: kept.
	w7cSeedSession(t, db, "w7c-alive", "running", nil, now)

	result, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "sweep"})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_test_tasks WHERE id = 'w7c-old-done'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("expired finished task must be cleaned")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_test_tasks WHERE id = 'w7c-new-done'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("fresh finished task must be kept")
	}
	for id, want := range map[string]string{
		"w7c-idle":  "completed",
		"w7c-busy":  "running",
		"w7c-alive": "running",
	} {
		var status string
		if err := db.QueryRow(`SELECT status FROM account_test_sessions WHERE id = ?`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != want {
			t.Fatalf("session %s = %s, want %s", id, status, want)
		}
	}
	_ = result
}

func TestW7CMaintenanceValidationArms(t *testing.T) {
	repo, _, _ := openTestRepo(t)
	ctx := context.Background()
	if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "purge"}); err == nil || !strings.Contains(err.Error(), "action 无效") {
		t.Fatalf("invalid action: %v", err)
	}
	// Zero/absent tuning inputs fall back to the documented defaults and the
	// stale-running clamp never goes below the 60s minimum.
	for _, action := range []string{"start", "sweep"} {
		if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: action}); err != nil {
			t.Fatalf("%s defaults: %v", action, err)
		}
	}
	zero := int64(0)
	if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "start", StaleRunningMS: &zero, MaxQueuedMS: 0, SweepLimit: 0, RefillLimit: 0}); err != nil {
		t.Fatalf("zero inputs: %v", err)
	}
}
