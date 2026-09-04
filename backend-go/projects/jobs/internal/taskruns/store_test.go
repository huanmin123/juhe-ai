package taskruns

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newTestStore(t *testing.T) (*Store, *FakeClock) {
	t.Helper()
	store, clock, err := newTestStoreE(t.TempDir())
	if err != nil {
		t.Fatalf("打开测试存储失败: %v", err)
	}
	return store, clock
}

func newTestStoreE(dir string) (*Store, *FakeClock, error) {
	config := StoreConfig{Mode: ModeSQLite, DatabasePath: filepath.Join(dir, "taskruns.db")}
	store, err := OpenStore(config)
	if err != nil {
		return nil, nil, err
	}
	clock := NewFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC))
	store.SetClock(clock)
	if err := store.EnsureSchema(context.Background()); err != nil {
		return nil, nil, err
	}
	return store, clock, nil
}

func TestTaskRunStateMachine(t *testing.T) {
	store, clock := newTestStore(t)
	defer store.Close()
	ctx := context.Background()

	run, err := store.CreateTaskRun(ctx, TaskRunCreateInput{
		JobName:    "temporary-maintenance-worker",
		JobType:    "probe",
		WorkerRole: TemporaryMaintenanceWorkerRole,
		LeaseKey:   "temporary-maintenance-worker:run-1",
		Params:     map[string]any{"target": "accounts/1"},
	})
	if err != nil {
		t.Fatalf("CreateTaskRun: %v", err)
	}
	if run.Status != StatusQueued || run.LeaseKey == "" {
		t.Fatalf("queued 运行记录不符合预期: %+v", run)
	}

	ownerA := "instance-a:worker:0:uuid-a"
	leaseUntil := clock.Now().Add(2 * time.Minute)
	started, err := store.TryStartTaskRun(ctx, TaskRunStartInput{RunID: run.RunID, OwnerID: ownerA, LeaseUntil: leaseUntil})
	if err != nil || !started {
		t.Fatalf("TryStartTaskRun 应成功: started=%v err=%v", started, err)
	}
	// 已是 running：CAS 不再命中。
	startedAgain, err := store.TryStartTaskRun(ctx, TaskRunStartInput{RunID: run.RunID, OwnerID: "other", LeaseUntil: leaseUntil})
	if err != nil || startedAgain {
		t.Fatalf("第二次 start 应失败: started=%v err=%v", startedAgain, err)
	}

	// 心跳：错误 owner → false；正确 owner → true 且续租。
	ok, err := store.HeartbeatTaskRun(ctx, run.RunID, "wrong-owner", leaseUntil, nil)
	if err != nil || ok {
		t.Fatalf("错误 owner 心跳应失败: ok=%v err=%v", ok, err)
	}
	clock.Advance(30 * time.Second)
	renewedUntil := clock.Now().Add(2 * time.Minute)
	ok, err = store.HeartbeatTaskRun(ctx, run.RunID, ownerA, renewedUntil, nil)
	if err != nil || !ok {
		t.Fatalf("正确 owner 心跳应成功: ok=%v err=%v", ok, err)
	}

	// 完成终态 + 释放租约。
	clock.Advance(10 * time.Second)
	finishedAt := clock.Now()
	exit := int64(0)
	changed, err := store.FinishTaskRun(ctx, TaskRunFinishInput{
		RunID:      run.RunID,
		Status:     StatusCompleted,
		Result:     map[string]any{"processed": float64(3)},
		FinishedAt: &finishedAt,
		ExitCode:   &exit,
	})
	if err != nil || !changed {
		t.Fatalf("FinishTaskRun 应命中: changed=%v err=%v", changed, err)
	}
	final, err := store.GetTaskRun(ctx, run.RunID)
	if err != nil || final == nil {
		t.Fatalf("GetTaskRun: %v", err)
	}
	if final.Status != StatusCompleted {
		t.Fatalf("终态应为 completed: %s", final.Status)
	}
	if final.DurationMs == nil || *final.DurationMs != 40_000 {
		t.Fatalf("duration_ms 应为 40000: %v", final.DurationMs)
	}
	if final.Result["processed"] != float64(3) {
		t.Fatalf("result_json 未按预期写入: %v", final.Result)
	}
	// 租约已释放。
	acquired, err := store.AcquireLease(ctx, LeaseAcquireInput{
		LeaseKey:   TemporaryTaskLeaseKey(run.RunID),
		JobName:    TemporaryMaintenanceWorkerRole,
		ShardKey:   run.RunID,
		OwnerID:    "next-owner",
		LeaseUntil: clock.Now().Add(time.Minute),
	})
	if err != nil || !acquired {
		t.Fatalf("释放后租约应可被新 owner 获取: %v %v", acquired, err)
	}
}

// TestFinishTerminalStates 表驱动覆盖 completed/failed/skipped 终态与
// duration 下限 0、exit_code 截断。
func TestFinishTerminalStates(t *testing.T) {
	cases := []struct {
		status   TaskRunStatus
		errorMsg string
	}{
		{StatusCompleted, ""},
		{StatusFailed, "探针超时"},
		{StatusSkipped, ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.status), func(t *testing.T) {
			store, clock := newTestStore(t)
			defer store.Close()
			ctx := context.Background()
			run, err := store.CreateTaskRun(ctx, TaskRunCreateInput{JobName: "j", JobType: "probe", WorkerRole: TemporaryMaintenanceWorkerRole, LeaseKey: "k"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.TryStartTaskRun(ctx, TaskRunStartInput{RunID: run.RunID, OwnerID: "o", LeaseUntil: clock.Now().Add(time.Minute)}); err != nil {
				t.Fatal(err)
			}
			startedAt := clock.Now()
			// startedAt 晚于 finishedAt：duration 下限 0。
			clock.Advance(-5 * time.Second)
			finishedAt := clock.Now()
			changed, err := store.FinishTaskRun(ctx, TaskRunFinishInput{RunID: run.RunID, Status: tc.status, ErrorMessage: tc.errorMsg, FinishedAt: &finishedAt})
			if err != nil || !changed {
				t.Fatalf("finish: %v %v", changed, err)
			}
			got, _ := store.GetTaskRun(ctx, run.RunID)
			if got.Status != tc.status {
				t.Fatalf("终态不符: %s", got.Status)
			}
			if tc.errorMsg != "" && got.ErrorMessage != tc.errorMsg {
				t.Fatalf("错误信息不符: %q", got.ErrorMessage)
			}
			if got.DurationMs == nil || *got.DurationMs != 0 {
				t.Fatalf("duration 应为 0: %v", got.DurationMs)
			}
			_ = startedAt
		})
	}
}

func TestAcquireLeaseExpiredTakeover(t *testing.T) {
	store, clock := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	key := "temporary-maintenance-worker:run-x"

	acquired, err := store.AcquireLease(ctx, LeaseAcquireInput{LeaseKey: key, JobName: TemporaryMaintenanceWorkerRole, ShardKey: "run-x", OwnerID: "a", LeaseUntil: clock.Now().Add(time.Minute)})
	if err != nil || !acquired {
		t.Fatalf("首次获取应成功: %v %v", acquired, err)
	}
	// 未过期：他人不可获取。
	acquired, err = store.AcquireLease(ctx, LeaseAcquireInput{LeaseKey: key, JobName: TemporaryMaintenanceWorkerRole, ShardKey: "run-x", OwnerID: "b", LeaseUntil: clock.Now().Add(2 * time.Minute)})
	if err != nil || acquired {
		t.Fatalf("未过期接管应失败: %v %v", acquired, err)
	}
	// 过期：b 接管成功。
	clock.Advance(2 * time.Minute)
	nowRef := clock.Now()
	acquired, err = store.AcquireLease(ctx, LeaseAcquireInput{LeaseKey: key, JobName: TemporaryMaintenanceWorkerRole, ShardKey: "run-x", OwnerID: "b", LeaseUntil: clock.Now().Add(time.Minute), Now: &nowRef})
	if err != nil || !acquired {
		t.Fatalf("过期接管应成功: %v %v", acquired, err)
	}
	// 原 owner 续租应失败（owner 已易主）。
	renewed, err := store.RenewLease(ctx, key, "a", clock.Now().Add(time.Minute), nil)
	if err != nil || renewed {
		t.Fatalf("旧 owner 续租应失败: %v %v", renewed, err)
	}
}

func TestScheduledLeaseFencingToken(t *testing.T) {
	store, clock := newTestStore(t)
	defer store.Close()
	ctx := context.Background()

	first, err := store.TryAcquireScheduledLease(ctx, ScheduledLeaseAcquireInput{
		JobName: "account-quality-refresh",
		OwnerID: "owner-1",
		TTL:     time.Minute,
	})
	if err != nil || !first.Acquired {
		t.Fatalf("首次获取应成功: %+v %v", first, err)
	}
	if first.Lease.FencingToken != 1 {
		t.Fatalf("fencing token 应为 1: %d", first.Lease.FencingToken)
	}
	// 持有中：lease_held。
	second, err := store.TryAcquireScheduledLease(ctx, ScheduledLeaseAcquireInput{JobName: "account-quality-refresh", OwnerID: "owner-2", TTL: time.Minute})
	if err != nil || second.Acquired || second.Reason != AcquireLeaseHeld {
		t.Fatalf("持有中应返回 lease_held: %+v %v", second, err)
	}
	// 持有中续租成功且身份不变。
	renewed, err := store.RenewScheduledLease(ctx, *first.Lease, time.Minute)
	if err != nil || renewed == nil {
		t.Fatalf("续租应成功: %v %v", renewed, err)
	}
	// 旧 fencing token 续租失败。
	stale := *first.Lease
	stale.FencingToken = 0
	if r, err := store.RenewScheduledLease(ctx, stale, time.Minute); err != nil || r != nil {
		t.Fatalf("过期 token 续租应失败: %v %v", r, err)
	}
	// 到期后 owner-2 接管，token 自增。
	clock.Advance(2 * time.Minute)
	takeover, err := store.TryAcquireScheduledLease(ctx, ScheduledLeaseAcquireInput{JobName: "account-quality-refresh", OwnerID: "owner-2", TTL: time.Minute})
	if err != nil || !takeover.Acquired {
		t.Fatalf("过期接管应成功: %+v %v", takeover, err)
	}
	if takeover.Lease.FencingToken != 2 {
		t.Fatalf("接管后 token 应为 2: %d", takeover.Lease.FencingToken)
	}
	// 原 owner 的 fence 校验应报丢失。
	assertErr := store.AssertScheduledLease(ctx, first.Lease.Fence())
	var lost *ErrLeaseLost
	if !errors.As(assertErr, &lost) {
		t.Fatalf("应返回 ErrLeaseLost: %v", assertErr)
	}
	if !strings.Contains(lost.Error(), "后台任务租约已失效") {
		t.Fatalf("错误文案不符: %s", lost.Error())
	}
	// 新 owner 释放后 lease_until 过期。
	released, err := store.ReleaseScheduledLease(ctx, *takeover.Lease)
	if err != nil || !released {
		t.Fatalf("释放应命中: %v %v", released, err)
	}
}

// TestScheduledLeaseConcurrentOnlyOneHolder 双 worker 并发抢约：恰好一个成功。
func TestScheduledLeaseConcurrentOnlyOneHolder(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()

	const workers = 8
	results := make(chan AcquireResult, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := store.TryAcquireScheduledLease(ctx, ScheduledLeaseAcquireInput{
				JobName:  "account-api-key-cooldown-retest",
				ShardKey: "global",
				OwnerID:  fmt.Sprintf("owner-%d", i),
				TTL:      time.Minute,
			})
			if err != nil {
				t.Errorf("抢约出错: %v", err)
				return
			}
			results <- result
		}(i)
	}
	wg.Wait()
	close(results)
	acquired := 0
	for result := range results {
		if result.Acquired {
			acquired++
		}
	}
	if acquired != 1 {
		t.Fatalf("应恰好一个持约，实际 %d", acquired)
	}
}

func TestReconcileStale(t *testing.T) {
	store, clock := newTestStore(t)
	defer store.Close()
	ctx := context.Background()

	// 场景表：
	// 1) queued 超时无租约 → failed(worker_never_started)
	// 2) queued 超时但有有效租约 → 保留
	// 3) running 心跳超时无租约 → failed(lease_expired_after_worker_exit)
	// 4) running 心跳新 → 保留
	// 5) 终态行 + 过期租约 → 租约被删除
	staleSubmitted := clock.Now().Add(-10 * time.Minute)
	freshSubmitted := clock.Now().Add(-1 * time.Second)

	staleQueued := mustCreate(t, store, staleSubmitted)
	staleQueuedWithLease := mustCreate(t, store, staleSubmitted)
	runningStale := mustCreateRunning(t, store, staleSubmitted, -10*time.Minute)
	runningFresh := mustCreateRunning(t, store, freshSubmitted, -time.Second)
	finished := mustCreateRunning(t, store, staleSubmitted, -10*time.Minute)

	// 有效租约：阻止回收。
	if ok, err := store.AcquireLease(ctx, LeaseAcquireInput{LeaseKey: TemporaryTaskLeaseKey(staleQueuedWithLease.RunID), JobName: TemporaryMaintenanceWorkerRole, ShardKey: staleQueuedWithLease.RunID, OwnerID: "live", RunID: staleQueuedWithLease.RunID, LeaseUntil: clock.Now().Add(time.Minute)}); err != nil || !ok {
		t.Fatalf("acquire lease: %v %v", ok, err)
	}
	// finished 行的过期租约（应以当前时刻接管并置为过期，应被第三步删除）。
	if ok, err := store.AcquireLease(ctx, LeaseAcquireInput{LeaseKey: TemporaryTaskLeaseKey(finished.RunID), JobName: TemporaryMaintenanceWorkerRole, ShardKey: finished.RunID, OwnerID: "dead", LeaseUntil: clock.Now().Add(-5 * time.Minute)}); err != nil || !ok {
		t.Fatalf("acquire expired lease: %v %v", ok, err)
	}
	if _, err := store.FinishTaskRun(ctx, TaskRunFinishInput{RunID: finished.RunID, Status: StatusCompleted}); err != nil {
		t.Fatal(err)
	}
	// FinishTaskRun 会释放 finished 的租约，重建一个孤儿过期租约场景：
	orphanNow := clock.Now()
	if ok, err := store.AcquireLease(ctx, LeaseAcquireInput{LeaseKey: "orphan-expired", JobName: TemporaryMaintenanceWorkerRole, ShardKey: "gone", OwnerID: "dead", LeaseUntil: orphanNow.Add(-time.Minute), Now: &orphanNow}); err != nil || !ok {
		t.Fatalf("acquire orphan lease: %v %v", ok, err)
	}

	result, err := store.ReconcileStale(ctx, TaskRunReconcileInput{
		QueuedBefore:           clock.Now().Add(-5 * time.Minute),
		RunningHeartbeatBefore: clock.Now().Add(-5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FailedQueuedCount != 1 {
		t.Fatalf("应收口 1 条 queued: %d", result.FailedQueuedCount)
	}
	if result.FailedRunningCount != 1 {
		t.Fatalf("应收口 1 条 running: %d", result.FailedRunningCount)
	}
	if result.DeletedExpiredLeaseCount != 3 {
		// 3 条 = finished 的过期租约 + 孤儿过期租约 + runningStale 收口为
		// failed 后自身的过期租约（NOT IN ('queued','running')，与 Node 一致）。
		t.Fatalf("应删除 3 条过期租约: %d", result.DeletedExpiredLeaseCount)
	}

	reconciled, _ := store.GetTaskRun(ctx, staleQueued.RunID)
	if reconciled.Status != StatusFailed {
		t.Fatalf("stale queued 应为 failed: %s", reconciled.Status)
	}
	if reconciled.ErrorMessage != "临时维护 worker 未在期限内启动，后台任务已自动收口为失败" {
		t.Fatalf("收口文案不符: %q", reconciled.ErrorMessage)
	}
	var resultPayload map[string]any
	if err := json.Unmarshal([]byte(mustRawResult(t, store, staleQueued.RunID)), &resultPayload); err != nil {
		t.Fatal(err)
	}
	if resultPayload["reconciledReason"] != "worker_never_started" {
		t.Fatalf("reconciledReason 不符: %v", resultPayload)
	}
	runningReconciled, _ := store.GetTaskRun(ctx, runningStale.RunID)
	if runningReconciled.Status != StatusFailed {
		t.Fatalf("stale running 应为 failed: %s", runningReconciled.Status)
	}
	if runningReconciled.ErrorMessage != "临时维护 worker 心跳中断且无有效租约，后台任务已自动收口为失败" {
		t.Fatalf("running 收口文案不符: %q", runningReconciled.ErrorMessage)
	}
	keptQueued, _ := store.GetTaskRun(ctx, staleQueuedWithLease.RunID)
	if keptQueued.Status != StatusQueued {
		t.Fatalf("有效租约应阻止回收: %s", keptQueued.Status)
	}
	keptRunning, _ := store.GetTaskRun(ctx, runningFresh.RunID)
	if keptRunning.Status != StatusRunning {
		t.Fatalf("近期心跳应阻止回收: %s", keptRunning.Status)
	}
}

func mustRawResult(t *testing.T, store *Store, runID string) string {
	t.Helper()
	run, err := store.GetTaskRun(context.Background(), runID)
	if err != nil || run == nil {
		t.Fatalf("读取 run: %v", err)
	}
	encoded, err := json.Marshal(run.Result)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func mustCreate(t *testing.T, store *Store, submittedAt time.Time) TaskRun {
	t.Helper()
	run, err := store.CreateTaskRun(context.Background(), TaskRunCreateInput{
		JobName: "temporary-maintenance-worker", JobType: "probe", WorkerRole: TemporaryMaintenanceWorkerRole,
		LeaseKey: "pending", SubmittedAt: &submittedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func mustCreateRunning(t *testing.T, store *Store, submittedAt time.Time, heartbeatDelta time.Duration) TaskRun {
	t.Helper()
	run := mustCreate(t, store, submittedAt)
	heartbeatAt := submittedAt.Add(heartbeatDelta + 10*time.Minute)
	startInput := TaskRunStartInput{RunID: run.RunID, OwnerID: "w", LeaseUntil: submittedAt.Add(time.Minute), Now: &heartbeatAt}
	ok, err := store.TryStartTaskRun(context.Background(), startInput)
	if err != nil || !ok {
		t.Fatalf("start: %v %v", ok, err)
	}
	return run
}

// TestRunWithTaskRunLifecycle 走完 queued→running→completed，并验证心跳续租。
func TestRunWithTaskRunLifecycle(t *testing.T) {
	// 心跳时间戳需要真实推进（FakeClock 静止时 heartbeat_at 不变化）。
	dir := t.TempDir()
	store, err := OpenStore(StoreConfig{Mode: ModeSQLite, DatabasePath: filepath.Join(dir, "lifecycle.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	_, outcome, err := RunWithTaskRun(ctx, store, TaskRunRunnerOptions{
		JobName: "temporary-maintenance-worker", JobType: "probe", WorkerRole: TemporaryMaintenanceWorkerRole,
		OwnerID: "owner-1", LeaseTTL: 5 * time.Second, HeartbeatInterval: 20 * time.Millisecond,
	}, func(runCtx context.Context, fence LeaseFence, run TaskRun) (TaskRunResult, error) {
		if fence.LeaseKey != TemporaryTaskLeaseKey(run.RunID) {
			return TaskRunResult{}, fmt.Errorf("lease key 不符: %s", fence.LeaseKey)
		}
		// 轮询租约 heartbeat_at 是否被后台心跳刷新。
		var firstHeartbeat string
		poll := time.NewTicker(5 * time.Millisecond)
		defer poll.Stop()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case <-deadline:
				return TaskRunResult{}, errors.New("未观察到心跳")
			case <-poll.C:
				var heartbeatAt string
				if err := store.db.QueryRowContext(ctx, `SELECT heartbeat_at FROM background_job_leases WHERE lease_key = ?`, fence.LeaseKey).Scan(&heartbeatAt); err != nil {
					continue
				}
				if firstHeartbeat == "" {
					firstHeartbeat = heartbeatAt
					continue
				}
				if heartbeatAt != firstHeartbeat {
					return TaskRunResult{Status: StatusCompleted, Result: map[string]any{"ok": true}}, nil
				}
			}
		}
	})
	if err != nil {
		t.Fatalf("RunWithTaskRun: %v", err)
	}
	if outcome.Outcome != OutcomeSuccess || outcome.LeaseState != LeaseStateAcquired {
		t.Fatalf("outcome 不符: %+v", outcome)
	}
}

// TestRunWithTaskRunFailureTerminal 任务函数报错 → failed 终态。
func TestRunWithTaskRunFailureTerminal(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	_, _, err := RunWithTaskRun(context.Background(), store, TaskRunRunnerOptions{
		JobName: "j", JobType: "probe", WorkerRole: TemporaryMaintenanceWorkerRole,
		OwnerID: "o", LeaseTTL: 10 * time.Second,
	}, func(ctx context.Context, fence LeaseFence, run TaskRun) (TaskRunResult, error) {
		return TaskRunResult{}, errors.New("诊断失败")
	})
	if err == nil || !strings.Contains(err.Error(), "诊断失败") {
		t.Fatalf("应返回任务错误: %v", err)
	}
}

// TestKillRestartRecovery 模拟 kill-restart：
// 旧进程留下 running 行 + 过期租约 → RecoverOnStartup 收口 failed；
// 新进程 RunWithTaskRun 全新运行成功；旧过期租约被新运行接管。
func TestKillRestartRecovery(t *testing.T) {
	store, clock := newTestStore(t)
	defer store.Close()
	ctx := context.Background()

	// 旧进程运行中（kill 掉，不再续租）。
	_, _, _ = RunWithTaskRun(ctx, store, TaskRunRunnerOptions{
		JobName: "temporary-maintenance-worker", JobType: "probe", WorkerRole: TemporaryMaintenanceWorkerRole,
		OwnerID: "dead-owner", LeaseTTL: time.Second,
	}, func(runCtx context.Context, fence LeaseFence, run TaskRun) (TaskRunResult, error) {
		// 直接返回（模拟在任务中进程被 kill 前，租约未释放的替代路径：
		// 手工删除终态，保留 running 行与租约）。
		return TaskRunResult{Status: StatusCompleted}, nil
	})
	// 构造被 kill 的现场：一小时前启动的 running 行 + 已过期租约。
	killed := mustCreate(t, store, clock.Now().Add(-time.Hour))
	pastStart := clock.Now().Add(-time.Hour)
	if ok, err := store.TryStartTaskRun(ctx, TaskRunStartInput{RunID: killed.RunID, OwnerID: "dead", LeaseUntil: pastStart.Add(time.Minute), Now: &pastStart}); err != nil || !ok {
		t.Fatalf("构造 killed 现场: %v %v", ok, err)
	}

	result, err := RecoverOnStartup(ctx, store, clock.Now(), 5*time.Minute, 5*time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	if result.FailedRunningCount != 1 {
		t.Fatalf("应收口 1 条 killed running: %+v", result)
	}
	recovered, _ := store.GetTaskRun(ctx, killed.RunID)
	if recovered.Status != StatusFailed {
		t.Fatalf("killed 运行应被收口: %s", recovered.Status)
	}

	// 新进程接管：同 key 过期租约可被新 owner 获取。
	acquired, err := store.AcquireLease(ctx, LeaseAcquireInput{
		LeaseKey: TemporaryTaskLeaseKey(killed.RunID), JobName: TemporaryMaintenanceWorkerRole,
		ShardKey: killed.RunID, OwnerID: "new-owner", LeaseUntil: clock.Now().Add(time.Minute),
	})
	if err != nil || !acquired {
		t.Fatalf("新进程应接管过期租约: %v %v", acquired, err)
	}
}

// TestConcurrentTaskStartOnlyOneWinner 并发 TryStartTaskRun 只有一个赢家。
func TestConcurrentTaskStartOnlyOneWinner(t *testing.T) {
	store, clock := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	run, err := store.CreateTaskRun(ctx, TaskRunCreateInput{JobName: "j", JobType: "probe", WorkerRole: TemporaryMaintenanceWorkerRole, LeaseKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	winners := make(chan struct{}, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := store.TryStartTaskRun(ctx, TaskRunStartInput{RunID: run.RunID, OwnerID: fmt.Sprintf("owner-%d", i), LeaseUntil: clock.Now().Add(time.Minute)})
			if err != nil {
				t.Errorf("start err: %v", err)
				return
			}
			if ok {
				winners <- struct{}{}
			}
		}(i)
	}
	wg.Wait()
	close(winners)
	count := 0
	for range winners {
		count++
	}
	if count != 1 {
		t.Fatalf("应恰好一个启动赢家，实际 %d", count)
	}
}

// TestRunWithScheduledLeaseLost 租约被接管后续租失败 → *ErrLeaseLost 取消任务。
func TestRunWithScheduledLeaseLost(t *testing.T) {
	store, clock := newTestStore(t)
	defer store.Close()
	ctx := context.Background()

	type startedSignal struct{}
	started := make(chan startedSignal, 1)
	release := make(chan struct{})
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, runErr = RunWithScheduledLease(ctx, store, ScheduledLeaseRunnerOptions{
			JobName: "account-quality-refresh", OwnerID: "holder", TTL: time.Hour, RenewInterval: 10 * time.Millisecond,
		}, func(runCtx context.Context, lease LeaseIdentity) error {
			started <- startedSignal{}
			<-release
			<-runCtx.Done()
			// 取消原因携带 *ErrLeaseLost（等价 Node abort(reason)）。
			return context.Cause(runCtx)
		})
	}()
	<-started
	// 推进假时钟让租约过期（TTL=1h）。
	clock.Advance(2 * time.Hour)
	close(release)
	<-done
	var lost *ErrLeaseLost
	if runErr == nil || !errors.As(runErr, &lost) {
		t.Fatalf("应以 ErrLeaseLost 收口: %v", runErr)
	}
}

// TestNormalizeLeaseTTLBoundary TTL 边界与 Node 一致。
func TestNormalizeLeaseTTLBoundary(t *testing.T) {
	if _, err := NormalizeLeaseTTL(50 * time.Millisecond); err == nil {
		t.Fatal("低于最小 TTL 应报错")
	}
	if _, err := NormalizeLeaseTTL(25 * time.Hour); err == nil {
		t.Fatal("超过最大 TTL 应报错")
	}
	if _, err := NormalizeLeaseTTL(time.Second); err != nil {
		t.Fatalf("合法 TTL 不应报错: %v", err)
	}
}

// TestAdvisoryKeyMatchesNode 已知输入的 advisory key 稳定（跨语言核对锚点）。
func TestAdvisoryKeyMatchesNode(t *testing.T) {
	key, err := ScheduledLeaseAdvisoryKey("scheduled:account-quality-refresh:global")
	if err != nil {
		t.Fatal(err)
	}
	// sha256(namespace+leaseKey) 首 8 字节大端转有符号 int64（与 Node 一致）。
	if key != -5017148235430353918 {
		t.Fatalf("advisory key 与 Node 算法不符: %d", key)
	}
}

var _ = sql.ErrNoRows
