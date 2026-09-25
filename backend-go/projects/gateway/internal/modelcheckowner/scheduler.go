package modelcheckowner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

type SchedulerKind string

const (
	SchedulerScheduled       SchedulerKind = "scheduled"
	SchedulerQualityRecovery SchedulerKind = "quality_recovery"
	SchedulerHealthRetry     SchedulerKind = "health_sync_retry"
)

type ScheduleTask struct {
	ID         string
	Kind       SchedulerKind
	OwnerID    string
	FenceToken int64
	Payload    []byte
}

type SchedulerSource interface {
	Claim(context.Context, SchedulerKind, time.Time, int) ([]ScheduleTask, error)
}

type SchedulerExecutor interface {
	Execute(context.Context, ScheduleTask) error
}

// SchedulerLifecycle makes execution durable. Implementations must fence the
// task by owner and fence token; stale workers therefore cannot acknowledge a
// task after lease takeover.
type SchedulerLifecycle interface {
	Complete(context.Context, ScheduleTask) error
	Fail(context.Context, ScheduleTask, error) error
}

// SchedulerErrorOperation identifies the durable scheduler step that failed.
// It is intentionally stable so a Gateway host can turn the events into
// structured logs and alerts without parsing error strings.
type SchedulerErrorOperation string

const (
	SchedulerErrorClaim    SchedulerErrorOperation = "claim"
	SchedulerErrorExecute  SchedulerErrorOperation = "execute"
	SchedulerErrorComplete SchedulerErrorOperation = "complete"
	SchedulerErrorFail     SchedulerErrorOperation = "fail"
)

// SchedulerError preserves the scheduler kind and, when one has been leased,
// the exact task whose durable lifecycle operation failed. A claim failure has
// no task because no lease was acquired.
type SchedulerError struct {
	Operation SchedulerErrorOperation
	Kind      SchedulerKind
	Task      ScheduleTask
	Err       error
}

type Scheduler struct {
	Source   SchedulerSource
	Executor SchedulerExecutor
	Interval time.Duration
	Batch    int
	// MaxConcurrency bounds how many claimed tasks execute at the same time
	// inside one claimed batch (default 4). The claim implementations own
	// lease and concurrency policy at the store level; this cap only stops
	// one oversized batch from spawning unbounded probe goroutines. Claimed
	// tasks beyond the cap wait for a slot instead of being re-claimed later;
	// a queued task may therefore start after its lease has already expired,
	// and the store's stale-lease owner/fence CAS fail-safes its final
	// lifecycle write in that case.
	MaxConcurrency int
	Kinds          []SchedulerKind
	Now            func() time.Time
	ErrorSink      func(SchedulerError)
}

// schedulerMaxConcurrencyDefault is the process-wide default bound for one
// owner's concurrent probe executions. Four concurrent full-profile probes
// stay well inside a personal deployment's upstream and connection budget
// while still letting unrelated accounts progress in parallel.
const schedulerMaxConcurrencyDefault = 4

// Run executes all configured scheduler kinds in one Gateway owner process.
// Concurrent task execution inside a claimed batch is bounded by
// MaxConcurrency (default 4); claim implementations own the store-level lease
// and concurrency policy. A failed task remains claimable for the next retry
// scan.
func (s *Scheduler) Run(ctx context.Context) error {
	if s == nil || s.Source == nil || s.Executor == nil {
		return errors.New("J3b scheduler is not initialized")
	}
	interval := s.Interval
	if interval <= 0 {
		interval = time.Second
	}
	batch := s.Batch
	if batch <= 0 {
		batch = 1000
	}
	maxConcurrency := s.MaxConcurrency
	if maxConcurrency <= 0 {
		maxConcurrency = schedulerMaxConcurrencyDefault
	}
	kinds := s.Kinds
	if len(kinds) == 0 {
		kinds = []SchedulerKind{SchedulerScheduled, SchedulerQualityRecovery, SchedulerHealthRetry}
	}
	if err := validateSchedulerKinds(kinds); err != nil {
		return err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	report := func(event SchedulerError) {
		slog.Error("J3b scheduler task operation failed",
			"operation", event.Operation,
			"kind", event.Kind,
			"task_id", event.Task.ID,
			"owner_id", event.Task.OwnerID,
			"fence_token", event.Task.FenceToken,
			"err", event.Err,
		)
		if s.ErrorSink != nil {
			s.ErrorSink(event)
		}
	}
	runCycle := func() {
		for _, kind := range kinds {
			tasks, err := s.Source.Claim(ctx, kind, now().UTC(), batch)
			if err != nil {
				report(SchedulerError{Operation: SchedulerErrorClaim, Kind: kind, Err: err})
				continue
			}
			executeSchedulerBatch(ctx, s.Executor, s.Source, kind, tasks, report, maxConcurrency)
		}
	}
	runCycle()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			runCycle()
		}
	}
}

// executeSchedulerBatch runs independently leased tasks concurrently, bounded
// by maxConcurrency. Each task owns its own completion/failure fence; one slow
// or failed probe does not serialize unrelated work until the cap is reached,
// and tasks beyond the cap wait for a slot inside the lease they were claimed
// under (scheduled runs are additionally bounded by RunBudget, health retries
// are pure durable projections). Execution errors are persisted through
// lifecycle.Fail for retry. Every error is emitted to report, but no single
// leased task is allowed to terminate the owner loop. report may be called
// concurrently because sibling tasks run concurrently.
func executeSchedulerBatch(ctx context.Context, executor SchedulerExecutor, source SchedulerSource, kind SchedulerKind, tasks []ScheduleTask, report func(SchedulerError), maxConcurrency int) {
	if len(tasks) == 0 {
		return
	}
	if maxConcurrency <= 0 {
		maxConcurrency = schedulerMaxConcurrencyDefault
	}
	lifecycle, hasLifecycle := source.(SchedulerLifecycle)
	var wg sync.WaitGroup
	slots := make(chan struct{}, maxConcurrency)
	recordErr := func(operation SchedulerErrorOperation, task ScheduleTask, err error) {
		if err == nil {
			return
		}
		if report != nil {
			report(SchedulerError{Operation: operation, Kind: kind, Task: task, Err: err})
		}
	}
	for _, original := range tasks {
		task := original
		if task.Kind == "" {
			task.Kind = kind
		}
		wg.Add(1)
		go func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			defer wg.Done()
			// Registered last so LIFO unwinding recovers (and writes the
			// durable failure fence below) before the batch WaitGroup and
			// slot are released: callers of wg.Wait never observe a task
			// whose terminal state is still pending.
			defer safego.Handle("modelcheckowner.executeSchedulerBatch.task", func(recovered any) {
				panicErr := fmt.Errorf("任务执行异常终止: %v", recovered)
				recordErr(SchedulerErrorExecute, task, panicErr)
				// The panic unwound before the normal settlement below could
				// run, so the durable failure fence is written here.
				// lifecycle.Fail is fenced by owner and fence token and the
				// normal path never runs after a panic, so the terminal state
				// cannot be written twice.
				if hasLifecycle {
					if releaseErr := lifecycle.Fail(ctx, task, panicErr); releaseErr != nil {
						recordErr(SchedulerErrorFail, task, errors.Join(panicErr, releaseErr))
					}
				}
			})
			execErr := executor.Execute(ctx, task)
			if hasLifecycle {
				if execErr != nil {
					recordErr(SchedulerErrorExecute, task, execErr)
					if releaseErr := lifecycle.Fail(ctx, task, execErr); releaseErr != nil {
						recordErr(SchedulerErrorFail, task, errors.Join(execErr, releaseErr))
					}
					return
				}
				if completeErr := lifecycle.Complete(ctx, task); completeErr != nil {
					recordErr(SchedulerErrorComplete, task, completeErr)
				}
				return
			}
			recordErr(SchedulerErrorExecute, task, execErr)
		}()
	}
	wg.Wait()
}

// validateSchedulerKinds prevents a partially wired Gateway owner from
// silently running only one scheduler family. Production J3b ownership is
// complete only when scheduled, quality recovery, and health-sync retry are
// all present exactly once; unknown/duplicate kinds fail closed.
func validateSchedulerKinds(kinds []SchedulerKind) error {
	if len(kinds) != 3 {
		return errors.New("J3b scheduler must configure all owner kinds")
	}
	seen := make(map[SchedulerKind]struct{}, len(kinds))
	for _, kind := range kinds {
		switch kind {
		case SchedulerScheduled, SchedulerQualityRecovery, SchedulerHealthRetry:
			if _, exists := seen[kind]; exists {
				return fmt.Errorf("J3b scheduler kind %q is configured more than once", kind)
			}
			seen[kind] = struct{}{}
		default:
			return fmt.Errorf("unsupported J3b scheduler kind %q", kind)
		}
	}
	if len(seen) != 3 {
		return errors.New("J3b scheduler must configure scheduled, quality recovery, and health retry")
	}
	return nil
}

type memorySchedulerSource struct {
	mu    sync.Mutex
	tasks []ScheduleTask
}

func (s *memorySchedulerSource) Claim(_ context.Context, kind SchedulerKind, _ time.Time, limit int) ([]ScheduleTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]ScheduleTask, 0, limit)
	remaining := s.tasks[:0]
	for _, task := range s.tasks {
		if task.Kind == kind && len(result) < limit {
			result = append(result, task)
		} else {
			remaining = append(remaining, task)
		}
	}
	s.tasks = remaining
	return result, nil
}
