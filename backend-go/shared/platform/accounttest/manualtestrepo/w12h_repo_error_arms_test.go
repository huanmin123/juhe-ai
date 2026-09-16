package manualtestrepo

// w12h 补充 arms：仓储各入口在关闭句柄/缺表下的错误传播、空白任务 ID 的
// 静默跳过、action 校验，以及触发器中止矩阵。
// 不可达语句登记（modernc.org/sqlite 驱动层无注入点，实测无法触发）：
// repo.go 中 getTaskRecord/Exec 后 rows.Scan 与 RowsAffected 的错误分支、
// rows.Err 兜底、tx.Commit 错误分支——驱动对合法 SQL/参数不产生这些错误，
// 触发器只能中止写语句，无法令 Scan/RowsAffected/Commit 返回错误。

import (
	"context"
	"database/sql"
	"strings"
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest"

	_ "modernc.org/sqlite"
)

func TestW12HRepoClosedHandleErrorPropagation(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "w12h-repo.db")))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := New(Config{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := repo.ValidateCoreTables(ctx); err == nil {
		t.Fatal("缺表必须失败")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// 关闭句柄后每个入口都必须失败而非静默。
	if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "sweep"}); err == nil {
		t.Fatal("关闭句柄后 Maintenance 必须失败")
	}
	if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "start"}); err == nil {
		t.Fatal("关闭句柄后 Maintenance(start) 必须失败")
	}
	if _, err := repo.MarkRunning(ctx, "w12h-task"); err == nil {
		t.Fatal("关闭句柄后 MarkRunning 必须失败")
	}
	if err := repo.Complete(ctx, "w12h-task", accounttest.ManualTestTaskExecutorResult{}, nil); err == nil {
		t.Fatal("关闭句柄后 Complete 必须失败")
	}
	if err := repo.Fail(ctx, "w12h-task", "m", "", nil); err == nil {
		t.Fatal("关闭句柄后 Fail 必须失败")
	}
	if err := repo.Cancel(ctx, "w12h-task", "m", nil); err == nil {
		t.Fatal("关闭句柄后 Cancel 必须失败")
	}
	if err := repo.UpdateMessage(ctx, "w12h-task", "m", nil); err == nil {
		t.Fatal("关闭句柄后 UpdateMessage 必须失败")
	}
	if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "bogus"}); err == nil {
		t.Fatal("非法 action 必须失败")
	}
}

func TestW12HRepoBlankTaskIDsAreSilentNoOps(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "w12h-blank.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo, err := New(Config{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// 空白 ID：normalizedText 不过 → (nil, nil) / 无错误静默跳过。
	if record, err := repo.MarkRunning(ctx, "   "); err != nil || record != nil {
		t.Fatalf("空白 ID MarkRunning 必须静默: %+v %v", record, err)
	}
	if err := repo.Complete(ctx, "   ", accounttest.ManualTestTaskExecutorResult{}, nil); err != nil {
		t.Fatalf("空白 ID Complete 必须静默: %v", err)
	}
	if err := repo.Fail(ctx, "   ", "m", "", nil); err != nil {
		t.Fatalf("空白 ID Fail 必须静默: %v", err)
	}
	if err := repo.Cancel(ctx, "   ", "m", nil); err != nil {
		t.Fatalf("空白 ID Cancel 必须静默: %v", err)
	}
	if err := repo.UpdateMessage(ctx, "   ", "m", nil); err != nil {
		t.Fatalf("空白 ID UpdateMessage 必须静默: %v", err)
	}
	// 超长消息被拒绝文案兜底（normalizedText 上限）。
	if err := repo.UpdateMessage(ctx, "w12h-task", stringsRepeat(3000), nil); err != nil {
		t.Fatalf("超长消息必须静默拒绝: %v", err)
	}
}

func stringsRepeat(n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = 'x'
	}
	return string(out)
}

func TestW12HRepoSQLiteBehaviorArms(t *testing.T) {
	repo, db, _ := openTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seed := func(id, status string, cancelRequested int, extraCols []string, extraArgs []any) {
		columns := "id, account_id, request_system_account_id, status, cancel_requested, queued_at, created_at, updated_at"
		placeholders := "?, 'acc', 'sys', ?, ?, ?, ?, ?"
		args := []any{id, status, cancelRequested, now, now, now}
		for _, col := range extraCols {
			columns += ", " + col
			placeholders += ", ?"
		}
		args = append(args, extraArgs...)
		if _, err := db.Exec(`INSERT INTO account_test_tasks (`+columns+`) VALUES (`+placeholders+`)`, args...); err != nil {
			t.Fatal(err)
		}
	}
	// 1) queued 且 cancel_requested=1：MarkRunning 命中取消收口分支。
	seed("w12h-cancelled-queued", "queued", 1, nil, nil)
	if record, err := repo.MarkRunning(ctx, "w12h-cancelled-queued"); err != nil || record != nil {
		t.Fatalf("取消队列任务 MarkRunning 应静默: %+v %v", record, err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM account_test_tasks WHERE id='w12h-cancelled-queued'`).Scan(&status); err != nil || status != "canceled" {
		t.Fatalf("应被标记 canceled: %s %v", status, err)
	}
	// 2) Complete/Fail/Cancel 的 0 行未命中路径（任务不存在或状态不符）。
	seed("w12h-done", "success", 0, nil, nil)
	if err := repo.Complete(ctx, "w12h-done", accounttest.ManualTestTaskExecutorResult{Success: true}, nil); err != nil {
		t.Fatalf("终态任务 Complete 必须静默: %v", err)
	}
	if err := repo.Fail(ctx, "w12h-done", "m", "", nil); err != nil {
		t.Fatalf("终态任务 Fail 必须静默: %v", err)
	}
	if err := repo.Cancel(ctx, "w12h-done", "m", nil); err != nil {
		t.Fatalf("终态任务 Cancel 必须静默: %v", err)
	}
	// 3) Cancel 保留既有取消消息（CASE 分支）。
	seed("w12h-keepmsg", "running", 1, []string{"status_message"}, []any{"用户请求停止"})
	if err := repo.Cancel(ctx, "w12h-keepmsg", "新消息", nil); err != nil {
		t.Fatal(err)
	}
	var keepMsg string
	if err := db.QueryRow(`SELECT status_message FROM account_test_tasks WHERE id='w12h-keepmsg'`).Scan(&keepMsg); err != nil || keepMsg != "用户请求停止" {
		t.Fatalf("应保留既有取消消息: %q %v", keepMsg, err)
	}

	// 4) Maintenance sweep：queued 超限自动失败 + 过期会话清理。
	if _, err := db.Exec(`INSERT INTO account_test_sessions (
		id, request_system_account_id, status, last_heartbeat_at, created_at, updated_at
	) VALUES ('w12h-idle-sess', 'sys', 'running', ?, ?, ?)`,
		nowMillisOff(-60_000), nowMillisOff(-60_000), nowMillisOff(-60_000)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_test_session_tasks (session_id, task_id) VALUES ('w12h-idle-sess', 'w12h-idle-task')`); err != nil {
		t.Fatal(err)
	}
	seed("w12h-idle-task", "success", 0, []string{"finished_at"}, []any{nowMillisOff(-120_000)})
	seed("w12h-expired", "queued", 0, []string{"queued_at"}, []any{nowMillisOff(-120_000)})

	result, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "sweep", MaxQueuedMS: 60_000, SweepLimit: 50, RefillLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range result.ExpiredQueuedTaskIDs {
		if id == "w12h-expired" {
			found = true
		}
	}
	if !found {
		t.Fatalf("过期 queued 任务应被收口: %+v", result)
	}
	var sessStatus string
	if err := db.QueryRow(`SELECT status FROM account_test_sessions WHERE id='w12h-idle-sess'`).Scan(&sessStatus); err != nil || sessStatus != "completed" {
		t.Fatalf("空闲会话应收口 completed: %s %v", sessStatus, err)
	}

	// 5) Maintenance start：running+cancel_requested 回收、stale running 重排队。
	seed("w12h-stale-cancel", "running", 1, []string{"started_at"}, []any{nowMillisOff(-120_000)})
	seed("w12h-stale-run", "running", 0, []string{"started_at", "updated_at"}, []any{nowMillisOff(-120_000), nowMillisOff(-120_000)})
	if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "start", StaleRunningMS: int64Ptr(60_000), RefillLimit: 10}); err != nil {
		t.Fatal(err)
	}
	var requeuedStatus string
	if err := db.QueryRow(`SELECT status FROM account_test_tasks WHERE id='w12h-stale-run'`).Scan(&requeuedStatus); err != nil || requeuedStatus != "queued" {
		t.Fatalf("stale running 应重排队: %s %v", requeuedStatus, err)
	}
	var canceledStatus string
	if err := db.QueryRow(`SELECT status FROM account_test_tasks WHERE id='w12h-stale-cancel'`).Scan(&canceledStatus); err != nil || canceledStatus != "canceled" {
		t.Fatalf("取消的 running 应收口 canceled: %s %v", canceledStatus, err)
	}
}

func int64Ptr(v int64) *int64 { return &v }

func nowMillisOff(ms int64) string {
	return time.Now().UTC().Add(time.Duration(ms) * time.Millisecond).Format(time.RFC3339Nano)
}

func TestW12HFormatQueuedWaitArms(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{1000, "1 秒"},
		{59_000, "59 秒"},
		{90_000, "2 分钟"},
		{3_600_000, "1 小时"},
		{7_200_000, "2 小时"},
	}
	for _, test := range cases {
		if got := queuedWaitExpiredMessage(test.ms); !contains(got, test.want) {
			t.Fatalf("queuedWaitExpiredMessage(%d) = %q, want 含 %q", test.ms, got, test.want)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestW12HRepoWriteAbortMatrix(t *testing.T) {
	// 每个用例独立库：BEFORE UPDATE 中止器让各入口的写路径失败。
	newRepoWithAbort := func(t *testing.T) (*Repo, *sql.DB) {
		repo, db, _ := openTestRepo(t)
		if _, err := db.Exec(`CREATE TRIGGER w12h_abort_update BEFORE UPDATE ON account_test_tasks BEGIN SELECT RAISE(ABORT, 'w12h update aborted'); END`); err != nil {
			t.Fatal(err)
		}
		return repo, db
	}
	seed := func(t *testing.T, db *sql.DB, id, status string, cancel int) {
		if _, err := db.Exec(`INSERT INTO account_test_tasks (
			id, account_id, request_system_account_id, status, cancel_requested, queued_at, created_at, updated_at
		) VALUES (?, 'acc', 'sys', ?, ?, ?, ?, ?)`,
			id, status, cancel, time.Now().UTC(), time.Now().UTC(), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()

	t.Run("MarkRunning", func(t *testing.T) {
		repo, db := newRepoWithAbort(t)
		seed(t, db, "t", "queued", 0)
		if _, err := repo.MarkRunning(ctx, "t"); err == nil || !strings.Contains(err.Error(), "w12h update aborted") {
			t.Fatalf("写中止必须失败: %v", err)
		}
	})
	t.Run("Complete", func(t *testing.T) {
		repo, db := newRepoWithAbort(t)
		seed(t, db, "t", "running", 0)
		if err := repo.Complete(ctx, "t", accounttest.ManualTestTaskExecutorResult{Success: true}, nil); err == nil {
			t.Fatal("写中止必须失败")
		}
	})
	t.Run("Fail", func(t *testing.T) {
		repo, db := newRepoWithAbort(t)
		seed(t, db, "t", "queued", 0)
		if err := repo.Fail(ctx, "t", "m", "", nil); err == nil {
			t.Fatal("写中止必须失败")
		}
	})
	t.Run("Cancel", func(t *testing.T) {
		repo, db := newRepoWithAbort(t)
		seed(t, db, "t", "queued", 0)
		if err := repo.Cancel(ctx, "t", "m", nil); err == nil {
			t.Fatal("写中止必须失败")
		}
	})
	t.Run("UpdateMessage", func(t *testing.T) {
		repo, db := newRepoWithAbort(t)
		seed(t, db, "t", "running", 0)
		if err := repo.UpdateMessage(ctx, "t", "m", nil); err == nil {
			t.Fatal("写中止必须失败")
		}
	})
	t.Run("MaintenanceStart", func(t *testing.T) {
		repo, db := newRepoWithAbort(t)
		seed(t, db, "t", "running", 0)
		if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "start", StaleRunningMS: int64Ptr(60_000)}); err == nil {
			t.Fatal("写中止必须失败")
		}
	})
	t.Run("MaintenanceSweep", func(t *testing.T) {
		repo, db := newRepoWithAbort(t)
		// queued_at 直接写成过期时间（UPDATE 已被触发器拦截）。
		old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
		if _, err := db.Exec(`INSERT INTO account_test_tasks (
			id, account_id, request_system_account_id, status, cancel_requested, queued_at, created_at, updated_at
		) VALUES ('t', 'acc', 'sys', 'queued', 0, ?, ?, ?)`, old, old, old); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "sweep", MaxQueuedMS: 60_000}); err == nil {
			t.Fatal("写中止必须失败")
		}
	})
}

func TestW12HRepoCleanupAbortArms(t *testing.T) {
	ctx := context.Background()
	newRepo := func(t *testing.T, trigger string) (*Repo, *sql.DB) {
		repo, db, _ := openTestRepo(t)
		if _, err := db.Exec(trigger); err != nil {
			t.Fatal(err)
		}
		return repo, db
	}

	t.Run("过期任务删除中止", func(t *testing.T) {
		repo, db := newRepo(t, `CREATE TRIGGER w12h_abort_task_delete BEFORE DELETE ON account_test_tasks BEGIN SELECT RAISE(ABORT, 'w12h task delete aborted'); END`)
		old := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)
		if _, err := db.Exec(`INSERT INTO account_test_tasks (
			id, account_id, request_system_account_id, status, cancel_requested, queued_at, finished_at, created_at, updated_at
		) VALUES ('t', 'acc', 'sys', 'failed', 0, ?, ?, ?, ?)`, old, old, old, old); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "sweep"}); err == nil {
			t.Fatal("删除中止必须失败")
		}
	})

	t.Run("空闲会话收口中止", func(t *testing.T) {
		repo, db := newRepo(t, `CREATE TRIGGER w12h_abort_sess_update BEFORE UPDATE ON account_test_sessions BEGIN SELECT RAISE(ABORT, 'w12h session update aborted'); END`)
		old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
		if _, err := db.Exec(`INSERT INTO account_test_sessions (
			id, request_system_account_id, status, last_heartbeat_at, created_at, updated_at
		) VALUES ('s', 'sys', 'running', ?, ?, ?)`, old, old, old); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "sweep"}); err == nil {
			t.Fatal("会话收口中止必须失败")
		}
	})
}
