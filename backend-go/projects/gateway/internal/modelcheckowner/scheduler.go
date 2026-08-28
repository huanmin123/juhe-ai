package modelcheckowner

import (
	"context"
	"errors"
	"fmt"
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

type Scheduler struct {
	Source   SchedulerSource
	Executor SchedulerExecutor
	Interval time.Duration
	Batch    int
	Kinds    []SchedulerKind
	Now      func() time.Time
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
	runCycle := func() error {
		for _, kind := range kinds {
			tasks, err := s.Source.Claim(ctx, kind, now().UTC(), batch)
			if err != nil {
				return err
			}
			if err := executeSchedulerBatch(ctx, s.Executor, s.Source, kind, tasks); err != nil {
				return err
			}
		}
		return nil
	}
	if err := runCycle(); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := runCycle(); err != nil {
				return err
			}
		}
	}
}

// executeSchedulerBatch runs independently leased tasks concurrently. Each
// task owns its own completion/failure fence; one slow or failed probe does
// not serialize unrelated work in the same claimed batch. Execution errors
// are persisted through lifecycle.Fail for retry and therefore do not stop a
// healthy scheduler cycle unless that durable failure write itself fails.
func executeSchedulerBatch(ctx context.Context, executor SchedulerExecutor, source SchedulerSource, kind SchedulerKind, tasks []ScheduleTask) error {
	if len(tasks) == 0 {
		return nil
	}
	lifecycle, hasLifecycle := source.(SchedulerLifecycle)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	recordErr := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
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
					if releaseErr := lifecycle.Fail(ctx, task, execErr); releaseErr != nil {
						recordErr(errors.Join(execErr, releaseErr))
					}
					return
				}
				if completeErr := lifecycle.Complete(ctx, task); completeErr != nil {
					recordErr(completeErr)
				}
				return
			}
			recordErr(execErr)
		}()
	}
	wg.Wait()
	return firstErr
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
