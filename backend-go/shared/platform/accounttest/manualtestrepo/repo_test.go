package manualtestrepo

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest"
)

// openTestRepo / seedTask 由 pgkit_test.go 提供（pgtestkit 真库 + 生产 PG 方言）；
// 测试侧校验 SQL 经 juhe_business 限定 + $n 占位符对齐 PG 臂。

func TestMaintenanceRequeueAndRefill(t *testing.T) {
	repo, db, _ := openTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	stale := now.Add(-10 * time.Minute)
	seedTask(t, db, "task-running", "running", 0, stale)
	seedTask(t, db, "task-cancel", "running", 1, stale)
	seedTask(t, db, "task-queued", "queued", 0, stale)

	staleRunningMS := int64(5 * 60_000)
	result, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{
		Action:         "start",
		MaxQueuedMS:    10 * 60_000,
		StaleRunningMS: &staleRunningMS,
		RefillLimit:    100,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, taskID := range result.TaskIDs {
		found[taskID] = true
	}
	if !found["task-running"] || !found["task-queued"] {
		t.Fatalf("重启恢复应重新排队 running/queued: %+v", result)
	}
	if found["task-cancel"] {
		t.Fatalf("cancel_requested=1 的 running 不得回到可运行清单: %+v", result)
	}
	var status, message string
	if err := db.QueryRow(`SELECT status, status_message FROM juhe_business.account_test_tasks WHERE id = 'task-running'`).Scan(&status, &message); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || message != "后台 worker 重启后重新排队" {
		t.Fatalf("陈旧 running 应重新排队: %s %s", status, message)
	}
	if err := db.QueryRow(`SELECT status, status_message FROM juhe_business.account_test_tasks WHERE id = 'task-cancel'`).Scan(&status, &message); err != nil {
		t.Fatal(err)
	}
	if status != "canceled" || message != "已停止测试" {
		t.Fatalf("cancel_requested running 应收口 canceled: %s %s", status, message)
	}
}

// TestSweepFailsExpiredQueued 覆盖 queued 超限自动失败（deadline 与 fallback
// queued_at 两条路径）与文案。
func TestSweepFailsExpiredQueued(t *testing.T) {
	repo, db, _ := openTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedTask(t, db, "task-deadline", "queued", 0, now.Add(-30*time.Minute))
	if _, err := db.Exec(`UPDATE juhe_business.account_test_tasks SET queued_deadline_at = $1 WHERE id = 'task-deadline'`,
		now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	seedTask(t, db, "task-fallback", "queued", 0, now.Add(-30*time.Minute))
	seedTask(t, db, "task-fresh", "queued", 0, now)

	result, err := repo.Maintenance(ctx, accounttest.ManualTestMaintenanceInput{
		Action:      "sweep",
		MaxQueuedMS: 10 * 60_000,
		SweepLimit:  200,
		RefillLimit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	expired := map[string]bool{}
	for _, taskID := range result.ExpiredQueuedTaskIDs {
		expired[taskID] = true
	}
	if !expired["task-deadline"] || !expired["task-fallback"] {
		t.Fatalf("超限 queued 应被收口: %+v", result.ExpiredQueuedTaskIDs)
	}
	if expired["task-fresh"] {
		t.Fatalf("新鲜 queued 不得被收口: %+v", result.ExpiredQueuedTaskIDs)
	}
	var status, message string
	if err := db.QueryRow(`SELECT status, status_message FROM juhe_business.account_test_tasks WHERE id = 'task-deadline'`).Scan(&status, &message); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("deadline 过期应收口 failed: %s", status)
	}
	if message != queuedWaitExpiredMessage(10*60_000) {
		t.Fatalf("收口文案与 Node 不一致: %s", message)
	}
}

// TestMarkRunningFencesAndCompletion 覆盖 queued→running 原子 claim、
// started_at 围栏消息更新、complete/fail/cancel 生命周期。
func TestMarkRunningFencesAndCompletion(t *testing.T) {
	repo, db, _ := openTestRepo(t)
	ctx := context.Background()
	seedTask(t, db, "task-1", "queued", 0, time.Now().UTC())

	record, err := repo.MarkRunning(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if record == nil || record.StartedAt == nil {
		t.Fatalf("MarkRunning 应返回带 started_at 的记录: %+v", record)
	}
	if message := record.Message; message != "后台测试中" {
		t.Fatalf("running 记录消息应为 后台测试中: %s", message)
	}
	if record.Diagnostics != "limited" || record.AccountID != "acc-1" || record.RequestSystemAccountID != "viewer-1" {
		t.Fatalf("记录投影字段不完整: %+v", record)
	}
	startedAt := *record.StartedAt

	// 围栏内的进度消息。
	if err := repo.UpdateMessage(ctx, "task-1", "真实请求测试中", &startedAt); err != nil {
		t.Fatal(err)
	}
	var message string
	if err := db.QueryRow(`SELECT status_message FROM juhe_business.account_test_tasks WHERE id = 'task-1'`).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if message != "真实请求测试中" {
		t.Fatalf("进度消息未写入: %s", message)
	}
	// 错误围栏（过期 started_at）不得写入。
	wrongFence := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if err := repo.UpdateMessage(ctx, "task-1", "过期写入", &wrongFence); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status_message FROM juhe_business.account_test_tasks WHERE id = 'task-1'`).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if message != "真实请求测试中" {
		t.Fatalf("错误围栏不得写入: %s", message)
	}

	// complete success。
	if err := repo.Complete(ctx, "task-1", accounttest.ManualTestTaskExecutorResult{Success: true, Message: "测试通过"}, &startedAt); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM juhe_business.account_test_tasks WHERE id = 'task-1'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "success" {
		t.Fatalf("complete 应置 success: %s", status)
	}

	// cancel 兜底（终态不再变更）。
	if err := repo.Cancel(ctx, "task-1", "已停止测试", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM juhe_business.account_test_tasks WHERE id = 'task-1'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "success" {
		t.Fatalf("终态任务不得被 cancel 改写: %s", status)
	}
}

// TestMarkRunningHonorsSessionCancel 覆盖会话取消理由优先级（session canceled
// → mark running 时直接取消任务）。
func TestMarkRunningHonorsSessionCancel(t *testing.T) {
	repo, db, _ := openTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedTask(t, db, "task-s", "queued", 0, now)
	if _, err := db.Exec(`
		INSERT INTO juhe_business.account_test_sessions (
			id, request_system_account_id, request_role, status, cancel_reason,
			last_heartbeat_at, created_at, updated_at
		) VALUES ('sess-1', 'viewer-1', 'user', 'canceled', '用户停止', $1, $1, $1)`,
		now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.account_test_session_tasks (session_id, task_id, created_at) VALUES ('sess-1', 'task-s', $1)`, now); err != nil {
		t.Fatal(err)
	}
	record, err := repo.MarkRunning(ctx, "task-s")
	if err != nil {
		t.Fatal(err)
	}
	if record != nil {
		t.Fatalf("会话已取消时 MarkRunning 必须返回 nil: %+v", record)
	}
	var status, message string
	if err := db.QueryRow(`SELECT status, status_message FROM juhe_business.account_test_tasks WHERE id = 'task-s'`).Scan(&status, &message); err != nil {
		t.Fatal(err)
	}
	if status != "canceled" || message != "用户停止" {
		t.Fatalf("会话取消理由应落任务: %s %s", status, message)
	}
}
