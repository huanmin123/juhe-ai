package taskruns

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// 缺陷3修复回归：RunWithTaskRun 的终态收口此前直接复用调用方 ctx——调度器
// 停机取消 ctx 后 FinishTaskRun 以 context.Canceled 失败，background_task_runs
// 行永久停留 running。修复后终态写/前置读走 WithoutCancel 的有限 ctx，
// 停机窗口内取消父 ctx 终态仍写入。

func wfixOpenStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(StoreConfig{Mode: ModeSQLite, DatabasePath: filepath.Join(t.TempDir(), "wfix-finish.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

func wfxWaitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("条件在超时前未满足")
}

// TestRunWithTaskRunFinishSurvivesCancelledParentContext：父 ctx 在任务执行中
// 被取消（等价停机），fn 以 ctx 错误返回后终态仍必须落 failed 且
// finished_at 非空，不得停留 running。
func TestRunWithTaskRunFinishSurvivesCancelledParentContext(t *testing.T) {
	store := wfixOpenStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type started struct{}
	startedCh := make(chan started, 1)
	done := make(chan struct{})
	// runID 由 fn 记录（RunWithTaskRun 的返回值在收口后才可用）。
	var runIDOnce sync.Once
	runID := ""
	go func() {
		defer close(done)
		_, _, _ = RunWithTaskRun(ctx, store, TaskRunRunnerOptions{
			JobName:           "wfix-cancel-job",
			JobType:           "wfix-cancel-job",
			WorkerRole:        "worker",
			OwnerID:           "wfix-owner",
			LeaseTTL:          time.Hour,
			HeartbeatInterval: 20 * time.Millisecond,
		}, func(runCtx context.Context, _ LeaseFence, run TaskRun) (TaskRunResult, error) {
			runIDOnce.Do(func() { runID = run.RunID })
			startedCh <- started{}
			<-runCtx.Done()
			return TaskRunResult{}, runCtx.Err()
		})
	}()
	<-startedCh

	wfxWaitFor(t, 2*time.Second, func() bool {
		row, err := store.GetTaskRun(context.Background(), runID)
		return err == nil && row != nil && row.Status == StatusRunning
	})
	cancel()
	<-done

	row, err := store.GetTaskRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("读取终态失败: %v", err)
	}
	if row == nil {
		t.Fatal("运行记录必须存在")
	}
	if row.Status != StatusFailed {
		t.Fatalf("停机取消后终态必须落 failed，实际 %q（停留 running 即为缺陷复现）", row.Status)
	}
	if row.FinishedAt == nil {
		t.Fatal("终态必须落 finished_at")
	}
	if row.ErrorMessage == "" {
		t.Fatal("终态失败原因必须留痕 error_message")
	}
}
