package modelcheckowner

import (
	"context"
	"errors"
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
			for _, task := range tasks {
				if task.Kind == "" {
					task.Kind = kind
				}
				err := s.Executor.Execute(ctx, task)
				if lifecycle, ok := s.Source.(SchedulerLifecycle); ok {
					if err != nil {
						if releaseErr := lifecycle.Fail(ctx, task, err); releaseErr != nil {
							return errors.Join(err, releaseErr)
						}
						continue
					}
					if completeErr := lifecycle.Complete(ctx, task); completeErr != nil {
						return completeErr
					}
				} else if err != nil {
					return err
				}
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
