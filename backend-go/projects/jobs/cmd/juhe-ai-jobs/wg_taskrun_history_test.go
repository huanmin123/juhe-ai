package main

// withLease 运行历史落库契约测试：PG 组装（存储用 SQLite 实现，租约与
// 运行记录状态机同 driver 无关）下每次 job 执行写入/更新
// background_task_runs；SQLite 模式直跑不落库。

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/taskruns"
	_ "modernc.org/sqlite"
)

func newTaskRunHistoryAssembly(t *testing.T, driver string) (*workerAssembly, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "task-run-history.sqlite3")
	store, err := taskruns.OpenStore(taskruns.StoreConfig{Mode: taskruns.ModeSQLite, DatabasePath: path})
	if err != nil {
		t.Fatalf("open taskruns store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	history, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open history reader: %v", err)
	}
	t.Cleanup(func() { _ = history.Close() })
	assembly := &workerAssembly{
		config:        workerConfig{Driver: driver, InstanceID: "wg-history", WorkerRole: "worker", WorkerReplicaIdx: 1},
		logger:        slog.Default(),
		taskRunsStore: store,
	}
	return assembly, history
}

// TestWithLeaseRecordsCompletedTaskRunHistory 锁定成功执行的运行记录：
// status=completed、started_at/finished_at/duration_ms 落列、job 三元组与
// owner 归属正确。
func TestWithLeaseRecordsCompletedTaskRunHistory(t *testing.T) {
	assembly, history := newTaskRunHistoryAssembly(t, "postgres")
	wrapped := assembly.withLease("usage-stats-aggregation", time.Minute, func(_ context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		return jobsched.TaskResult{}, nil
	})
	result, err := wrapped(context.Background(), jobsched.TaskContext{})
	if err != nil {
		t.Fatalf("成功任务不得报错: %v", err)
	}
	if result.Outcome != jobsched.OutcomeSuccess {
		t.Fatalf("成功任务必须返回 success: %+v", result)
	}
	row := history.QueryRow(`SELECT job_name, job_type, worker_role, status, owner_id, started_at, finished_at, duration_ms, error_message FROM background_task_runs`)
	var jobName, jobType, workerRole, status, ownerID string
	var startedAt, finishedAt sql.NullString
	var durationMs sql.NullInt64
	var errorMessage sql.NullString
	if err := row.Scan(&jobName, &jobType, &workerRole, &status, &ownerID, &startedAt, &finishedAt, &durationMs, &errorMessage); err != nil {
		t.Fatalf("读取运行记录失败: %v", err)
	}
	if jobName != "usage-stats-aggregation" || jobType != "usage-stats-aggregation" {
		t.Fatalf("job 名称错误: jobName=%q jobType=%q", jobName, jobType)
	}
	if workerRole != "worker" {
		t.Fatalf("worker_role 必须为 worker: %q", workerRole)
	}
	if status != "completed" {
		t.Fatalf("成功执行必须落 completed: %q", status)
	}
	if !startedAt.Valid || !finishedAt.Valid {
		t.Fatalf("成功执行必须落 started_at/finished_at: %v %v", startedAt, finishedAt)
	}
	if !durationMs.Valid || durationMs.Int64 < 0 {
		t.Fatalf("成功执行必须落非负 duration_ms: %v", durationMs)
	}
	if errorMessage.Valid && errorMessage.String != "" {
		t.Fatalf("成功执行不得写 error_message: %q", errorMessage.String)
	}
	if ownerID == "" {
		t.Fatal("owner_id 必须落列")
	}
}

// TestWithLeaseRecordsFailedTaskRunHistory 锁定失败执行的运行记录与错误
// 透传：表内 failed + error_message 留痕，调度器拿到原样错误。
func TestWithLeaseRecordsFailedTaskRunHistory(t *testing.T) {
	assembly, history := newTaskRunHistoryAssembly(t, "postgres")
	taskErr := errors.New("聚合批次失败")
	wrapped := assembly.withLease("group-account-stats-refresh", time.Minute, func(_ context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		return jobsched.TaskResult{}, taskErr
	})
	_, err := wrapped(context.Background(), jobsched.TaskContext{})
	if !errors.Is(err, taskErr) {
		t.Fatalf("任务错误必须原样透传给调度器: %v", err)
	}
	row := history.QueryRow(`SELECT status, error_message, finished_at FROM background_task_runs`)
	var status string
	var errorMessage sql.NullString
	var finishedAt sql.NullString
	if err := row.Scan(&status, &errorMessage, &finishedAt); err != nil {
		t.Fatalf("读取运行记录失败: %v", err)
	}
	if status != "failed" {
		t.Fatalf("失败执行必须落 failed: %q", status)
	}
	if !errorMessage.Valid || errorMessage.String != taskErr.Error() {
		t.Fatalf("error_message 必须留痕原错误: %#v", errorMessage)
	}
	if !finishedAt.Valid {
		t.Fatalf("失败执行必须落 finished_at: %v", finishedAt)
	}
}

// TestWithLeaseSQLiteSkipsTaskRunHistory 锁定 SQLite 模式直跑且不写运行
// 历史（与 Node driver 分支一致）。
func TestWithLeaseSQLiteSkipsTaskRunHistory(t *testing.T) {
	assembly, history := newTaskRunHistoryAssembly(t, "sqlite")
	called := false
	wrapped := assembly.withLease("usage-stats-aggregation", time.Minute, func(_ context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		called = true
		return jobsched.TaskResult{}, nil
	})
	if _, err := wrapped(context.Background(), jobsched.TaskContext{}); err != nil || !called {
		t.Fatalf("SQLite 必须直跑任务: %v %v", called, err)
	}
	rows, err := history.Query(`SELECT 1 FROM background_task_runs`)
	if err != nil {
		t.Fatalf("查询运行记录失败: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("SQLite 模式不得写运行历史")
	}
}
