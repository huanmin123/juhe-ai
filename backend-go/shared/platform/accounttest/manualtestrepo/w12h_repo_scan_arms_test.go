package manualtestrepo

// w12h 补充 arms：cancel_requested 非法布尔值、取消会话、缺表/缺列与
// 触发器中止的精确落点（区别于 w12h_repo_error_arms_test.go 的粗粒度矩阵）。
// 本文件补充登记的不可达语句（modernc.org/sqlite 驱动层无注入点）：
//   - repo.go:286 SweepOrRefill 中 listRunnable 错误返回：failExpiredQueued
//     与 listRunnable 读取同一表集合，无法只让后者失败；
//   - repo.go:343/409/455/507/713 RowsAffected 错误分支；
//   - repo.go:363/853 tx.Commit 错误分支；
//   - repo.go:543/547 finalizeIfCanceledTx 的 QueryRow 错误分支（getTaskRecord
//     已先行吸收 ErrNoRows，剩余错误与表结构错误在 getTaskRecord 处先触发）；
//   - repo.go:682/689/743/785/792 rows.Scan 错误与 rows.Err 兜底。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest"
)

func TestW12HRepoMarkRunningSessionCancelArms(t *testing.T) {
	ctx := context.Background()
	seedTaskRow := func(t *testing.T, repo *Repo, id, status string, cancel int) {
		t.Helper()
		if _, err := repo.db.Exec(`INSERT INTO account_test_tasks (
			id, account_id, request_system_account_id, status, cancel_requested, queued_at, created_at, updated_at
		) VALUES (?, 'acc', 'sys', ?, ?, ?, ?, ?)`,
			id, status, cancel, time.Now().UTC(), time.Now().UTC(), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	seedCanceledSession := func(t *testing.T, repo *Repo, sessionID, taskID, reason string) {
		t.Helper()
		now := time.Now().UTC()
		if _, err := repo.db.Exec(`INSERT INTO account_test_sessions (
			id, request_system_account_id, status, cancel_reason, last_heartbeat_at, created_at, updated_at
		) VALUES (?, 'sys', 'canceled', ?, ?, ?, ?)`, sessionID, reason, now, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.db.Exec(`INSERT INTO account_test_session_tasks (session_id, task_id) VALUES (?, ?)`,
			sessionID, taskID); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("会话取消加写中止命中markCanceledTx错误", func(t *testing.T) {
		repo, _, _ := openTestRepo(t)
		seedTaskRow(t, repo, "w12h-t", "queued", 0)
		seedCanceledSession(t, repo, "w12h-s", "w12h-t", "用户停止")
		if _, err := repo.db.Exec(`CREATE TRIGGER w12h_abort_upd2 BEFORE UPDATE ON account_test_tasks BEGIN SELECT RAISE(ABORT, 'w12h cancel abort'); END`); err != nil {
			t.Fatal(err)
		}
		record, err := repo.MarkRunning(ctx, "w12h-t")
		if err == nil || !strings.Contains(err.Error(), "w12h cancel abort") {
			t.Fatalf("取消会话+写中止必须失败: %+v %v", record, err)
		}
	})

	t.Run("queued已请求取消加写中止命中queued取消分支错误", func(t *testing.T) {
		repo, _, _ := openTestRepo(t)
		seedTaskRow(t, repo, "w12h-t", "queued", 1)
		if _, err := repo.db.Exec(`CREATE TRIGGER w12h_abort_upd3 BEFORE UPDATE ON account_test_tasks BEGIN SELECT RAISE(ABORT, 'w12h queued cancel abort'); END`); err != nil {
			t.Fatal(err)
		}
		record, err := repo.MarkRunning(ctx, "w12h-t")
		if err == nil || !strings.Contains(err.Error(), "w12h queued cancel abort") {
			t.Fatalf("queued 取消+写中止必须失败: %+v %v", record, err)
		}
	})

	t.Run("queued非法cancel布尔命中claim后查询错误", func(t *testing.T) {
		repo, _, _ := openTestRepo(t)
		seedTaskRow(t, repo, "w12h-t", "queued", 2)
		record, err := repo.MarkRunning(ctx, "w12h-t")
		if err == nil || record != nil {
			t.Fatalf("非法布尔值必须失败: %+v %v", record, err)
		}
	})

	t.Run("sessions缺表命中sessionCancelReason错误", func(t *testing.T) {
		repo, _, _ := openTestRepo(t)
		seedTaskRow(t, repo, "w12h-t", "queued", 0)
		if _, err := repo.db.Exec(`DROP TABLE account_test_sessions`); err != nil {
			t.Fatal(err)
		}
		record, err := repo.MarkRunning(ctx, "w12h-t")
		if err == nil || record != nil {
			t.Fatalf("sessions 缺表必须失败: %+v %v", record, err)
		}
	})
}

func TestW12HRepoFinalizeScanErrorArms(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T, repo *Repo, id, status string) {
		t.Helper()
		if _, err := repo.db.Exec(`INSERT INTO account_test_tasks (
			id, account_id, request_system_account_id, status, cancel_requested, queued_at, created_at, updated_at
		) VALUES (?, 'acc', 'sys', ?, 2, ?, ?, ?)`,
			id, status, time.Now().UTC(), time.Now().UTC(), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("Complete", func(t *testing.T) {
		repo, _, _ := openTestRepo(t)
		seed(t, repo, "w12h-t", "running")
		if err := repo.Complete(ctx, "w12h-t", accounttest.ManualTestTaskExecutorResult{Success: true}, nil); err == nil {
			t.Fatal("非法布尔 finalize 必须失败")
		}
	})
	t.Run("Fail", func(t *testing.T) {
		repo, _, _ := openTestRepo(t)
		seed(t, repo, "w12h-t", "running")
		if err := repo.Fail(ctx, "w12h-t", "m", "", nil); err == nil {
			t.Fatal("非法布尔 finalize 必须失败")
		}
	})
}

func TestW12HRepoMaintenancePreciseArms(t *testing.T) {
	ctx := context.Background()

	t.Run("过期会话删除中止", func(t *testing.T) {
		repo, db, _ := openTestRepo(t)
		if _, err := db.Exec(`CREATE TRIGGER w12h_abort_sess_del BEFORE DELETE ON account_test_sessions BEGIN SELECT RAISE(ABORT, 'w12h session delete aborted'); END`); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)
		if _, err := db.Exec(`INSERT INTO account_test_sessions (
			id, request_system_account_id, status, last_heartbeat_at, created_at, updated_at
		) VALUES ('w12h-s', 'sys', 'finished', ?, ?, ?)`, old, old, old); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "sweep"}); err == nil {
			t.Fatal("会话删除中止必须失败")
		}
	})

	t.Run("sessions缺last_heartbeat_at命中completeIdleSessions查询错误", func(t *testing.T) {
		repo, db, _ := openTestRepo(t)
		if _, err := db.Exec(`ALTER TABLE account_test_sessions DROP COLUMN last_heartbeat_at`); err != nil {
			t.Skipf("modernc 不支持 DROP COLUMN: %v", err)
		}
		if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "start", StaleRunningMS: int64Ptr(60_000)}); err == nil {
			t.Fatal("缺列必须失败")
		}
	})

	t.Run("running已请求取消加写中止命中requeue首语句错误", func(t *testing.T) {
		repo, db, _ := openTestRepo(t)
		if _, err := db.Exec(`CREATE TRIGGER w12h_abort_upd4 BEFORE UPDATE ON account_test_tasks BEGIN SELECT RAISE(ABORT, 'w12h requeue abort'); END`); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if _, err := db.Exec(`INSERT INTO account_test_tasks (
			id, account_id, request_system_account_id, status, cancel_requested, queued_at, created_at, updated_at
		) VALUES ('w12h-t', 'acc', 'sys', 'running', 1, ?, ?, ?)`, now, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "start", StaleRunningMS: int64Ptr(60_000)}); err == nil {
			t.Fatal("回收写中止必须失败")
		}
	})
}
