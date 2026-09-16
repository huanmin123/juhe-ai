package taskruns

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// w12b_runner_test.go 覆盖 RunWithScheduledLease / RunWithTaskRun 的分支：
// 获取失败（错误与 busy）、默认续租/心跳间隔、续租成功与失败、fence 丢失、
// 释放未命中（partial）、启动 CAS 未命中（skipped）、心跳失联收口 failed、
// 非法终态归一 completed、以及 finish/get 读取错误路径。
// 依赖 w12b_failinject_test.go 的注入驱动设施。

// TestW12bScheduledLeaseAcquireError 获取阶段注入错误 → 原样返回错误。
func TestW12bScheduledLeaseAcquireError(t *testing.T) {
	store, spec := w12bOpenFailStore(t)
	spec.arm("RETURNING")
	_, err := RunWithScheduledLease(context.Background(), store, ScheduledLeaseRunnerOptions{
		JobName: "w12b-j", OwnerID: "o", TTL: time.Minute,
	}, func(context.Context, LeaseIdentity) error { return nil })
	if err == nil {
		t.Fatal("获取注入失败应返回错误")
	}
}

// TestW12bScheduledLeaseBusyAndDefaultRenewInterval 持有中 → skipped busy；
// RenewInterval 缺省且 TTL/3 < 1s 时取 1s。
func TestW12bScheduledLeaseBusyAndDefaultRenewInterval(t *testing.T) {
	store, _ := w12bOpenFailStore(t)
	ctx := context.Background()
	held, err := store.TryAcquireScheduledLease(ctx, ScheduledLeaseAcquireInput{
		JobName: "w12b-busy-job", OwnerID: "w12b-holder", TTL: time.Hour,
	})
	if err != nil || !held.Acquired {
		t.Fatalf("预占租约失败: %+v %v", held, err)
	}
	outcome, err := RunWithScheduledLease(ctx, store, ScheduledLeaseRunnerOptions{
		JobName: "w12b-busy-job", OwnerID: "w12b-other", TTL: 300 * time.Millisecond,
	}, func(context.Context, LeaseIdentity) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Outcome != OutcomeSkipped || outcome.LeaseState != LeaseStateBusy {
		t.Fatalf("应 skipped/busy: %+v", outcome)
	}
	if !strings.Contains(outcome.Warning, "lease_busy:lease_held") {
		t.Fatalf("warning 应含 lease_held: %q", outcome.Warning)
	}
}

// TestW12bScheduledLeaseRenewalExtendsLease 真实时钟下续租协程按
// RenewInterval 续约，lease_until 应被推迟；随后正常释放 → success。
func TestW12bScheduledLeaseRenewalExtendsLease(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(StoreConfig{Mode: ModeSQLite, DatabasePath: dir + "/w12b-renew.db"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	leaseUntil := func() (string, error) {
		var until string
		err := store.db.QueryRowContext(ctx,
			`SELECT lease_until FROM background_job_leases WHERE lease_key = ?`,
			"w12b-renew-key").Scan(&until)
		return until, err
	}

	outcome, runErr := RunWithScheduledLease(ctx, store, ScheduledLeaseRunnerOptions{
		JobName: "w12b-renew-job", LeaseKey: "w12b-renew-key", OwnerID: "w12b-renew-owner",
		TTL: 1500 * time.Millisecond, // RenewInterval 缺省：TTL/3=500ms < 1s → 钳为 1s
	}, func(context.Context, LeaseIdentity) error {
		first, err := leaseUntil()
		if err != nil {
			return err
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			current, err := leaseUntil()
			if err == nil && current != first {
				return nil // 续租已推迟 lease_until
			}
			time.Sleep(10 * time.Millisecond)
		}
		return errors.New("未观察到续租推迟 lease_until")
	})
	if runErr != nil {
		t.Fatalf("RunWithScheduledLease: %v", runErr)
	}
	if outcome.Outcome != OutcomeSuccess || outcome.LeaseState != LeaseStateAcquired || outcome.Lease == nil {
		t.Fatalf("应 success/acquired: %+v", outcome)
	}
}

// TestW12bScheduledLeaseRenewalFailuresCancelsTask 租约表被删后续租失败：
// 续租错误记日志并以 *ErrLeaseLost 取消任务；任务成功返回时错误来自
// fenceLost 分支（fn 返回 nil）。
func TestW12bScheduledLeaseRenewalFailuresCancelsTask(t *testing.T) {
	store, _ := w12bOpenFailStore(t)
	ctx := context.Background()
	type started struct{}
	startedCh := make(chan started, 1)
	release := make(chan struct{})
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, runErr = RunWithScheduledLease(ctx, store, ScheduledLeaseRunnerOptions{
			JobName: "w12b-lost-job", OwnerID: "w12b-lost-owner", TTL: time.Hour, RenewInterval: 10 * time.Millisecond,
		}, func(runCtx context.Context, lease LeaseIdentity) error {
			startedCh <- started{}
			// 删除租约表：续租 SQL 直接失败（非 ErrLeaseLost 错误分支 + 日志）。
			if _, err := store.db.ExecContext(ctx, `DROP TABLE background_job_leases`); err != nil {
				return err
			}
			<-release
			<-runCtx.Done()
			return nil // fn 成功：错误应来自 fenceLost 分支
		})
	}()
	<-startedCh
	close(release)
	<-done
	var lost *ErrLeaseLost
	if runErr == nil || !errors.As(runErr, &lost) {
		t.Fatalf("应以 ErrLeaseLost 收口: %v", runErr)
	}
}

// TestW12bScheduledLeaseReleaseMiss 任务中删除租约表 → 释放未命中 → partial。
func TestW12bScheduledLeaseReleaseMiss(t *testing.T) {
	store, _ := w12bOpenFailStore(t)
	ctx := context.Background()
	outcome, err := RunWithScheduledLease(ctx, store, ScheduledLeaseRunnerOptions{
		JobName: "w12b-release-job", OwnerID: "w12b-release-owner",
		TTL: time.Hour, RenewInterval: time.Hour,
	}, func(context.Context, LeaseIdentity) error {
		if _, err := store.db.ExecContext(ctx, `DROP TABLE background_job_leases`); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("释放未命中不应返回错误: %v", err)
	}
	if outcome.Outcome != OutcomePartial || outcome.LeaseState != LeaseStateLost {
		t.Fatalf("应 partial/lost: %+v", outcome)
	}
	if !strings.Contains(outcome.Warning, "租约释放未命中") {
		t.Fatalf("warning 不符: %q", outcome.Warning)
	}
}

// TestW12bTaskRunCreateAndStartErrors 启动链路错误路径。
func TestW12bTaskRunCreateAndStartErrors(t *testing.T) {
	ctx := context.Background()

	store, spec := w12bOpenFailStore(t)
	spec.arm("INSERT INTO background_task_runs")
	if _, _, err := RunWithTaskRun(ctx, store, TaskRunRunnerOptions{JobName: "j", WorkerRole: TemporaryMaintenanceWorkerRole}, nil); err == nil {
		t.Fatal("创建注入失败应返回错误")
	}

	store2, spec2 := w12bOpenFailStore(t)
	spec2.arm("SET status = 'running'")
	run2, _, err := RunWithTaskRun(ctx, store2, TaskRunRunnerOptions{
		JobName: "w12b-j", WorkerRole: TemporaryMaintenanceWorkerRole, HeartbeatInterval: time.Hour,
	}, func(context.Context, LeaseFence, TaskRun) (TaskRunResult, error) { return TaskRunResult{}, nil })
	if err == nil {
		t.Fatal("start 注入失败应返回错误")
	}
	if run2.RunID == "" {
		t.Fatal("start 失败应返回已创建的 run")
	}
}

// TestW12bTaskRunStartMissSkipped 启动 CAS 命中 0 行（另一进程已抢跑）→ skipped。
func TestW12bTaskRunStartMissSkipped(t *testing.T) {
	store, spec := w12bOpenFailStore(t)
	spec.armZero("SET status = 'running'")
	run, outcome, err := RunWithTaskRun(context.Background(), store, TaskRunRunnerOptions{
		JobName: "w12b-j", WorkerRole: TemporaryMaintenanceWorkerRole, HeartbeatInterval: time.Hour,
	}, func(context.Context, LeaseFence, TaskRun) (TaskRunResult, error) {
		t.Fatal("CAS 未命中时任务函数不应执行")
		return TaskRunResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Outcome != OutcomeSkipped || outcome.LeaseState != LeaseStateBusy {
		t.Fatalf("应 skipped/busy: %+v", outcome)
	}
	if run.Status != StatusSkipped {
		t.Fatalf("运行记录应落 skipped 终态: %s", run.Status)
	}
}

// TestW12bTaskRunHeartbeatLost 心跳 CAS 未命中 → fence 失联 → failed 终态。
func TestW12bTaskRunHeartbeatLost(t *testing.T) {
	store, spec := w12bOpenFailStore(t)
	spec.armZero("SET heartbeat_at")
	run, outcome, err := RunWithTaskRun(context.Background(), store, TaskRunRunnerOptions{
		JobName: "w12b-j", WorkerRole: TemporaryMaintenanceWorkerRole, HeartbeatInterval: 20 * time.Millisecond,
	}, func(runCtx context.Context, fence LeaseFence, run TaskRun) (TaskRunResult, error) {
		<-runCtx.Done()
		return TaskRunResult{Status: StatusCompleted, Result: map[string]any{"ok": true}}, nil
	})
	var lost *ErrLeaseLost
	if err == nil || !errors.As(err, &lost) {
		t.Fatalf("应以 ErrLeaseLost 收口: %v", err)
	}
	if run.Status != StatusFailed || !strings.Contains(run.ErrorMessage, "后台任务租约已失效") {
		t.Fatalf("终态应 failed 且带失联文案: %+v", run)
	}
	if outcome.Outcome != OutcomePartial || outcome.LeaseState != LeaseStateLost {
		t.Fatalf("outcome 应 partial/lost: %+v", outcome)
	}
}

// TestW12bTaskRunHeartbeatError 心跳执行错误（非未命中）→ 记日志并收口 failed。
func TestW12bTaskRunHeartbeatError(t *testing.T) {
	store, spec := w12bOpenFailStore(t)
	spec.arm("SET heartbeat_at")
	run, _, err := RunWithTaskRun(context.Background(), store, TaskRunRunnerOptions{
		JobName: "w12b-j", WorkerRole: TemporaryMaintenanceWorkerRole, HeartbeatInterval: 20 * time.Millisecond,
	}, func(runCtx context.Context, fence LeaseFence, run TaskRun) (TaskRunResult, error) {
		<-runCtx.Done()
		return TaskRunResult{Status: StatusCompleted}, nil
	})
	var lost *ErrLeaseLost
	if err == nil || !errors.As(err, &lost) {
		t.Fatalf("心跳错误应以 ErrLeaseLost 收口: %v", err)
	}
	if run.Status != StatusFailed {
		t.Fatalf("终态应 failed: %s", run.Status)
	}
}

// TestW12bTaskRunFinishAndUpdateErrors finish 写入/读回错误路径。
func TestW12bTaskRunFinishAndUpdateErrors(t *testing.T) {
	ctx := context.Background()
	store, spec := w12bOpenFailStore(t)
	_, _, err := RunWithTaskRun(ctx, store, TaskRunRunnerOptions{
		JobName: "w12b-j", WorkerRole: TemporaryMaintenanceWorkerRole, HeartbeatInterval: time.Hour,
	}, func(context.Context, LeaseFence, TaskRun) (TaskRunResult, error) {
		spec.arm("FROM background_task_runs") // 持续命中：FinishTaskRun 读回与终读均失败
		return TaskRunResult{Status: StatusCompleted}, nil
	})
	if err == nil {
		t.Fatal("读回注入失败应返回错误")
	}

	store2, spec2 := w12bOpenFailStore(t)
	_, _, err = RunWithTaskRun(ctx, store2, TaskRunRunnerOptions{
		JobName: "w12b-j", WorkerRole: TemporaryMaintenanceWorkerRole, HeartbeatInterval: time.Hour,
	}, func(context.Context, LeaseFence, TaskRun) (TaskRunResult, error) {
		spec2.arm("UPDATE background_task_runs")
		return TaskRunResult{Status: StatusCompleted}, nil
	})
	if err == nil {
		t.Fatal("finish UPDATE 注入失败应返回错误")
	}
}

// TestW12bTaskRunInvalidStatusNormalized 非法终态归一为 completed。
func TestW12bTaskRunInvalidStatusNormalized(t *testing.T) {
	store, _ := w12bOpenFailStore(t)
	run, outcome, err := RunWithTaskRun(context.Background(), store, TaskRunRunnerOptions{
		JobName: "w12b-j", WorkerRole: TemporaryMaintenanceWorkerRole, HeartbeatInterval: time.Hour,
	}, func(context.Context, LeaseFence, TaskRun) (TaskRunResult, error) {
		return TaskRunResult{Status: TaskRunStatus("w12b-bogus")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusCompleted {
		t.Fatalf("非法终态应归一 completed: %s", run.Status)
	}
	if outcome.Outcome != OutcomeSuccess {
		t.Fatalf("应为 success: %+v", outcome)
	}
}

// TestW12bTaskRunDefaultHeartbeatInterval 心跳缺省间隔取 TTL/3 且下限 100ms。
func TestW12bTaskRunDefaultHeartbeatInterval(t *testing.T) {
	store, _ := w12bOpenFailStore(t)
	// TTL=150ms → 缺省间隔 50ms < 100ms 下限 → 取 100ms：任务瞬时返回，不触发心跳。
	run, outcome, err := RunWithTaskRun(context.Background(), store, TaskRunRunnerOptions{
		JobName: "w12b-j", WorkerRole: TemporaryMaintenanceWorkerRole, LeaseTTL: 150 * time.Millisecond,
	}, func(context.Context, LeaseFence, TaskRun) (TaskRunResult, error) {
		return TaskRunResult{Status: StatusCompleted}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusCompleted || outcome.Outcome != OutcomeSuccess {
		t.Fatalf("缺省心跳间隔路径应正常收口: %+v %+v", run, outcome)
	}
}
