package modelcheckowner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
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
	Source    SchedulerSource
	Executor  SchedulerExecutor
	Interval  time.Duration
	Batch     int
	Kinds     []SchedulerKind
	Now       func() time.Time
	ErrorSink func(SchedulerError)
}

// Run executes all configured scheduler kinds in one Gateway owner process.
// It does not impose a low worker limit; claim implementations own lease and
// concurrency policy. A failed task remains claimable for the next retry scan.
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
			executeSchedulerBatch(ctx, s.Executor, s.Source, kind, tasks, report)
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

// executeSchedulerBatch runs independently leased tasks concurrently. Each
// task owns its own completion/failure fence; one slow or failed probe does
// not serialize unrelated work in the same claimed batch. Execution errors
// are persisted through lifecycle.Fail for retry. Every error is emitted to
// report, but no single leased task is allowed to terminate the owner loop.
// report may be called concurrently because sibling tasks run concurrently.
func executeSchedulerBatch(ctx context.Context, executor SchedulerExecutor, source SchedulerSource, kind SchedulerKind, tasks []ScheduleTask, report func(SchedulerError)) {
	if len(tasks) == 0 {
		return
	}
	lifecycle, hasLifecycle := source.(SchedulerLifecycle)
	var wg sync.WaitGroup
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
			defer wg.Done()
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
