package manualtestrepo

// w12h 补充 arms：取消会话、缺表/缺列与触发器中止的精确落点（区别于
// w12h_repo_error_arms_test.go 的粗粒度矩阵）。
// 组A 臂走 pgkit_test.go 夹具（pgtestkit 真库 + 生产 PG 方言），触发器注入
// 经 pgAbortTrigger 转写为 PG 原生触发器函数（文案逐字保留）。
// 原 newTestRepoSQLite（SQLite 夹具）与其两个弱类型注入臂
// TestW12HRepoSQLiteIllegalCancelBoolArm（cancel_requested 存非法整数 2）与
// TestW12HRepoFinalizeScanErrorArms（finalize 扫描错误）为组B——生产 PG
// boolean 列无法注入该脏值、无行为等价断言，2026-10-25 随生产 sqlite 方言
// 分支删除批次退役。
// 本文件补充登记的不可达语句（驱动层无注入点）：
//   - repo.go SweepOrRefill 中 listRunnable 错误返回：failExpiredQueued
//     与 listRunnable 读取同一表集合，无法只让后者失败；
//   - repo.go RowsAffected 错误分支；
//   - repo.go tx.Commit 错误分支；
//   - repo.go finalizeIfCanceledTx 的 QueryRow 错误分支（getTaskRecord
//     已先行吸收 ErrNoRows，剩余错误与表结构错误在 getTaskRecord 处先触发）；
//   - repo.go rows.Scan 错误与 rows.Err 兜底。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest"
)

func TestW12HRepoMarkRunningSessionCancelArms(t *testing.T) {
	ctx := context.Background()
	seedTaskRow := func(t *testing.T, repo *Repo, id, status string, cancel bool) {
		t.Helper()
		if _, err := repo.db.Exec(`INSERT INTO juhe_business.account_test_tasks (
			id, account_id, account_name, protocol_code, protocol_version, account_type,
			request_system_account_id, request_role, status, cancel_requested, queued_at, created_at, updated_at
		) VALUES ($1, 'acc', 'w12h-seed', 'chat_completions', 'v1', 'api_key', 'sys', 'user', $2, $3, $4, $4, $4)`,
			id, status, cancel, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	seedCanceledSession := func(t *testing.T, repo *Repo, sessionID, taskID, reason string) {
		t.Helper()
		now := time.Now().UTC()
		if _, err := repo.db.Exec(`INSERT INTO juhe_business.account_test_sessions (
			id, request_system_account_id, request_role, status, cancel_reason, last_heartbeat_at, created_at, updated_at
		) VALUES ($1, 'sys', 'user', 'canceled', $2, $3, $3, $3)`, sessionID, reason, now); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.db.Exec(`INSERT INTO juhe_business.account_test_session_tasks (session_id, task_id, created_at) VALUES ($1, $2, $3)`,
			sessionID, taskID, now); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("会话取消加写中止命中markCanceledTx错误", func(t *testing.T) {
		repo, _, _ := openTestRepo(t)
		seedTaskRow(t, repo, "w12h-t", "queued", false)
		seedCanceledSession(t, repo, "w12h-s", "w12h-t", "用户停止")
		pgAbortTrigger(t, repo.db, "w12h_abort_upd2", "UPDATE", "account_test_tasks", "w12h cancel abort")
		record, err := repo.MarkRunning(ctx, "w12h-t")
		if err == nil || !strings.Contains(err.Error(), "w12h cancel abort") {
			t.Fatalf("取消会话+写中止必须失败: %+v %v", record, err)
		}
	})

	t.Run("queued已请求取消加写中止命中queued取消分支错误", func(t *testing.T) {
		repo, _, _ := openTestRepo(t)
		seedTaskRow(t, repo, "w12h-t", "queued", true)
		pgAbortTrigger(t, repo.db, "w12h_abort_upd3", "UPDATE", "account_test_tasks", "w12h queued cancel abort")
		record, err := repo.MarkRunning(ctx, "w12h-t")
		if err == nil || !strings.Contains(err.Error(), "w12h queued cancel abort") {
			t.Fatalf("queued 取消+写中止必须失败: %+v %v", record, err)
		}
	})

	t.Run("sessions缺表命中sessionCancelReason错误", func(t *testing.T) {
		repo, _, _ := openTestRepo(t)
		seedTaskRow(t, repo, "w12h-t", "queued", false)
		if _, err := repo.db.Exec(`DROP TABLE juhe_business.account_test_sessions`); err != nil {
			t.Fatal(err)
		}
		record, err := repo.MarkRunning(ctx, "w12h-t")
		if err == nil || record != nil {
			t.Fatalf("sessions 缺表必须失败: %+v %v", record, err)
		}
	})
}

func TestW12HRepoMaintenancePreciseArms(t *testing.T) {
	ctx := context.Background()

	t.Run("过期会话删除中止", func(t *testing.T) {
		repo, db, _ := openTestRepo(t)
		pgAbortTrigger(t, db, "w12h_abort_sess_del", "DELETE", "account_test_sessions", "w12h session delete aborted")
		old := time.Now().Add(-48 * time.Hour).UTC()
		// 生产 sessions.status 带 CHECK（running/canceled/expired/completed）；
		// SQLite 夹具的任意值 'finished' 在生产约束下改为 'completed'（行龄
		// 与无活跃任务前提不变，中止断言不变）。
		if _, err := db.Exec(`INSERT INTO juhe_business.account_test_sessions (
			id, request_system_account_id, request_role, status, last_heartbeat_at, created_at, updated_at
		) VALUES ('w12h-s', 'sys', 'user', 'completed', $1, $1, $1)`, old); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "sweep"}); err == nil {
			t.Fatal("会话删除中止必须失败")
		}
	})

	t.Run("sessions缺last_heartbeat_at命中completeIdleSessions查询错误", func(t *testing.T) {
		repo, db, _ := openTestRepo(t)
		if _, err := db.Exec(`ALTER TABLE juhe_business.account_test_sessions DROP COLUMN last_heartbeat_at`); err != nil {
			t.Skipf("当前 PG 不支持 DROP COLUMN: %v", err)
		}
		if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "start", StaleRunningMS: int64Ptr(60_000)}); err == nil {
			t.Fatal("缺列必须失败")
		}
	})

	t.Run("running已请求取消加写中止命中requeue首语句错误", func(t *testing.T) {
		repo, db, _ := openTestRepo(t)
		pgAbortTrigger(t, db, "w12h_abort_upd4", "UPDATE", "account_test_tasks", "w12h requeue abort")
		now := time.Now().UTC()
		if _, err := db.Exec(`INSERT INTO juhe_business.account_test_tasks (
			id, account_id, account_name, protocol_code, protocol_version, account_type,
			request_system_account_id, request_role, status, cancel_requested, queued_at, created_at, updated_at
		) VALUES ('w12h-t', 'acc', 'w12h-seed', 'chat_completions', 'v1', 'api_key', 'sys', 'user', 'running', true, $1, $1, $1)`, now); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{Action: "start", StaleRunningMS: int64Ptr(60_000)}); err == nil {
			t.Fatal("回收写中止必须失败")
		}
	})
}
