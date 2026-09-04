package taskruns

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

// LeaseState 与 Node WorkerScheduledJobTaskResult.leaseState 一致。
type LeaseState string

const (
	LeaseStateBusy     LeaseState = "busy"
	LeaseStateAcquired LeaseState = "acquired"
	LeaseStateLost     LeaseState = "lost"
)

// Outcome 与 Node WorkerScheduledJobTaskResult.outcome 一致。
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeSkipped Outcome = "skipped"
	OutcomePartial Outcome = "partial"
)

// ScheduledLeaseOutcome 是一次带租约周期任务执行的收口结果。
type ScheduledLeaseOutcome struct {
	Outcome    Outcome
	Warning    string
	LeaseState LeaseState
	Lease      *LeaseIdentity
}

// ScheduledLeaseRunnerOptions 控制 TryAcquireScheduledLease 与心跳节奏。
type ScheduledLeaseRunnerOptions struct {
	JobName  string
	ShardKey string
	LeaseKey string
	OwnerID  string
	RunID    string
	TTL      time.Duration
	// RenewInterval 为空时取 max(time.Second, TTL/3)，与 Node 一致。
	RenewInterval time.Duration
}

// RunWithScheduledLease 是 runWithPostgresScheduledLease 的 Go 等价：
// 获取失败返回 skipped（leaseState=busy）；持约期间按 TTL/3 续租，续租失败
// 取消任务并抛 *ErrLeaseLost；完成后释放租约，释放未命中返回 partial。
// fence 校验贯穿整个执行：任务函数必须通过 ctx 感知租约丢失。
func RunWithScheduledLease(
	ctx context.Context,
	store *Store,
	opts ScheduledLeaseRunnerOptions,
	fn func(runCtx context.Context, lease LeaseIdentity) error,
) (ScheduledLeaseOutcome, error) {
	acquired, err := store.TryAcquireScheduledLease(ctx, ScheduledLeaseAcquireInput{
		JobName:  opts.JobName,
		ShardKey: opts.ShardKey,
		LeaseKey: opts.LeaseKey,
		OwnerID:  opts.OwnerID,
		RunID:    opts.RunID,
		TTL:      opts.TTL,
	})
	if err != nil {
		return ScheduledLeaseOutcome{}, err
	}
	if !acquired.Acquired {
		return ScheduledLeaseOutcome{
			Outcome:    OutcomeSkipped,
			Warning:    fmt.Sprintf("lease_busy:%s", acquired.Reason),
			LeaseState: LeaseStateBusy,
		}, nil
	}
	lease := *acquired.Lease

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	renewInterval := opts.RenewInterval
	if renewInterval <= 0 {
		renewInterval = opts.TTL / 3
		if renewInterval < time.Second {
			renewInterval = time.Second
		}
	}

	var (
		leaseMu     sync.Mutex
		current     = lease
		leaseLost   error
		stopRenewal = make(chan struct{})
		renewDone   = make(chan struct{})
	)
	go func() {
		defer close(renewDone)
		ticker := time.NewTicker(renewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopRenewal:
				return
			case <-runCtx.Done():
				return
			case <-ticker.C:
				renewed, renewErr := store.RenewScheduledLease(ctx, current, opts.TTL)
				if renewErr == nil && renewed != nil {
					leaseMu.Lock()
					current = *renewed
					leaseMu.Unlock()
					continue
				}
				lost := &ErrLeaseLost{Lease: current.Fence()}
				if renewErr != nil && !errors.As(renewErr, new(*ErrLeaseLost)) {
					lost = &ErrLeaseLost{Lease: current.Fence()}
					log.Printf("background_job_lease_renew_failed jobName=%s leaseKey=%s error=%v", opts.JobName, current.LeaseKey, renewErr)
				}
				leaseMu.Lock()
				leaseLost = lost
				leaseMu.Unlock()
				cancel(lost)
				return
			}
		}
	}()

	var (
		fnErr     error
		fenceLost error
	)
	fnErr = fn(runCtx, lease)
	cancel(nil)
	close(stopRenewal)
	<-renewDone
	leaseMu.Lock()
	fenceLost = leaseLost
	leaseMu.Unlock()

	var released bool
	if fenceLost == nil {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		releasedRelease, releaseErr := store.ReleaseScheduledLease(releaseCtx, lease)
		releaseCancel()
		released = releasedRelease
		if releaseErr != nil {
			log.Printf("background_job_lease_release_failed jobName=%s leaseKey=%s error=%v", opts.JobName, lease.LeaseKey, releaseErr)
		}
	}
	if fnErr != nil {
		return ScheduledLeaseOutcome{Lease: &lease}, fnErr
	}
	if fenceLost != nil {
		return ScheduledLeaseOutcome{Lease: &lease}, fenceLost
	}
	if !released {
		return ScheduledLeaseOutcome{
			Outcome:    OutcomePartial,
			Warning:    "任务完成但租约释放未命中",
			LeaseState: LeaseStateLost,
			Lease:      &lease,
		}, nil
	}
	return ScheduledLeaseOutcome{
		Outcome:    OutcomeSuccess,
		LeaseState: LeaseStateAcquired,
		Lease:      &lease,
	}, nil
}

// TaskRunRunnerOptions 描述一次临时维护任务的运行记录与租约参数。
type TaskRunRunnerOptions struct {
	JobName    string
	JobType    string
	WorkerRole string // 对账只认 temporary-maintenance-worker
	LeaseKey   string
	OwnerID    string
	LeaseTTL   time.Duration
	Params     map[string]any
	// HeartbeatInterval 为空时取 TTL/3（最小 100ms，便于测试）。
	HeartbeatInterval time.Duration
}

// TaskRunResult 是任务函数写回运行记录的终态载荷。
type TaskRunResult struct {
	Status       TaskRunStatus // completed | failed | skipped
	Result       map[string]any
	ErrorMessage string
	ExitCode     *int64
}

// RunWithTaskRun 串起 J-INF 运行记录状态机：
// queued（CreateTaskRun）→ running（TryStartTaskRun CAS + 租约获取）→
// completed/failed/skipped（FinishTaskRun + 释放租约）。
// 心跳协程周期性调用 HeartbeatTaskRun（行 CAS + 租约续期）；任一步失联
// 即以 *ErrLeaseLost 取消任务 ctx（fence 硬门禁）。kill-restart 后：
// 旧进程留下的 queued/running 行由 ReconcileStale 收口为 failed，
// 新进程用新的 runID 重新走 queued→running；过期租约由 AcquireLease 的
// `lease_until <= now` CAS 被新 owner 接管。
func RunWithTaskRun(
	ctx context.Context,
	store *Store,
	opts TaskRunRunnerOptions,
	fn func(runCtx context.Context, fence LeaseFence, run TaskRun) (TaskRunResult, error),
) (TaskRun, ScheduledLeaseOutcome, error) {
	run, err := store.CreateTaskRun(ctx, TaskRunCreateInput{
		JobName:    opts.JobName,
		JobType:    opts.JobType,
		WorkerRole: opts.WorkerRole,
		LeaseKey:   opts.LeaseKey,
		Params:     opts.Params,
	})
	if err != nil {
		return TaskRun{}, ScheduledLeaseOutcome{}, err
	}
	leaseUntil := store.now().Add(opts.LeaseTTL)
	started, err := store.TryStartTaskRun(ctx, TaskRunStartInput{
		RunID:      run.RunID,
		OwnerID:    opts.OwnerID,
		LeaseUntil: leaseUntil,
	})
	if err != nil {
		return run, ScheduledLeaseOutcome{}, err
	}
	if !started {
		finished := store.now()
		_, _ = store.FinishTaskRun(ctx, TaskRunFinishInput{
			RunID:        run.RunID,
			Status:       StatusSkipped,
			ErrorMessage: "任务启动 CAS 未命中，运行记录已非 queued",
			FinishedAt:   &finished,
		})
		updated, _ := store.GetTaskRun(ctx, run.RunID)
		return derefRun(updated), ScheduledLeaseOutcome{
			Outcome:    OutcomeSkipped,
			Warning:    "lease_busy:run_not_queued",
			LeaseState: LeaseStateBusy,
		}, nil
	}

	fence := LeaseFence{
		LeaseKey:     TemporaryTaskLeaseKey(run.RunID),
		OwnerID:      opts.OwnerID,
		FencingToken: 0, // temporary worker 租约走 upsert CAS，不使用 fencing token
	}

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	interval := opts.HeartbeatInterval
	if interval <= 0 {
		interval = opts.LeaseTTL / 3
		if interval < 100*time.Millisecond {
			interval = 100 * time.Millisecond
		}
	}

	var (
		mu        sync.Mutex
		leaseLost error
		stop      = make(chan struct{})
		done      = make(chan struct{})
	)
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-runCtx.Done():
				return
			case <-ticker.C:
				nextUntil := store.now().Add(opts.LeaseTTL)
				ok, hbErr := store.HeartbeatTaskRun(ctx, run.RunID, opts.OwnerID, nextUntil, nil)
				if hbErr == nil && ok {
					continue
				}
				lost := &ErrLeaseLost{Lease: fence}
				if hbErr != nil {
					log.Printf("task_run_heartbeat_failed runID=%s leaseKey=%s error=%v", run.RunID, fence.LeaseKey, hbErr)
				}
				mu.Lock()
				leaseLost = lost
				mu.Unlock()
				cancel(lost)
				return
			}
		}
	}()

	runOutcome, fnErr := fn(runCtx, fence, run)
	cancel(nil)
	close(stop)
	<-done
	mu.Lock()
	lost := leaseLost
	mu.Unlock()

	result := TaskRunResult{Status: StatusCompleted}
	switch {
	case lost != nil:
		result = TaskRunResult{Status: StatusFailed, ErrorMessage: lost.Error()}
	case fnErr != nil:
		result = TaskRunResult{Status: StatusFailed, ErrorMessage: fnErr.Error()}
	default:
		result = runOutcome
	}
	if result.Status != StatusCompleted && result.Status != StatusFailed && result.Status != StatusSkipped {
		result.Status = StatusCompleted
	}
	finished := store.now()
	_, finishErr := store.FinishTaskRun(ctx, TaskRunFinishInput{
		RunID:        run.RunID,
		Status:       result.Status,
		Result:       result.Result,
		ErrorMessage: result.ErrorMessage,
		ExitCode:     result.ExitCode,
		FinishedAt:   &finished,
	})
	updated, getErr := store.GetTaskRun(ctx, run.RunID)
	finalRun := derefRun(updated)
	if getErr != nil {
		return finalRun, ScheduledLeaseOutcome{LeaseState: LeaseStateAcquired}, getErr
	}
	if finishErr != nil {
		return finalRun, ScheduledLeaseOutcome{LeaseState: LeaseStateAcquired}, finishErr
	}
	if fnErr != nil {
		return finalRun, ScheduledLeaseOutcome{LeaseState: LeaseStateAcquired}, fnErr
	}
	if lost != nil {
		return finalRun, ScheduledLeaseOutcome{Outcome: OutcomePartial, Warning: "任务完成但租约释放未命中", LeaseState: LeaseStateLost}, lost
	}
	return finalRun, ScheduledLeaseOutcome{Outcome: OutcomeSuccess, LeaseState: LeaseStateAcquired}, nil
}

func derefRun(run *TaskRun) TaskRun {
	if run == nil {
		return TaskRun{}
	}
	return *run
}

// RecoverOnStartup 是 kill-restart 恢复入口：进程启动时把上一进程留下的
// 临时维护任务收口（等价 background-task-run-reconcile 的启动回收语义）。
// 近期提交/心跳未超阈值的行不会被回收，有效租约同样阻止回收。
func RecoverOnStartup(ctx context.Context, store *Store, now time.Time, queuedGrace, runningGrace time.Duration, limit int) (TaskRunReconcileResult, error) {
	return store.ReconcileStale(ctx, TaskRunReconcileInput{
		QueuedBefore:           now.Add(-queuedGrace),
		RunningHeartbeatBefore: now.Add(-runningGrace),
		Now:                    &now,
		Limit:                  limit,
	})
}
