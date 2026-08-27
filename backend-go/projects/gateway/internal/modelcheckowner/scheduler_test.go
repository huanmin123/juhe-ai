package modelcheckowner

import (
	"context"
	"sync"
	"testing"
	"time"
)

type schedulerSource struct{}

func (schedulerSource) Claim(_ context.Context, kind SchedulerKind, _ time.Time, _ int) ([]ScheduleTask, error) {
	return []ScheduleTask{{ID: string(kind), Kind: kind}}, nil
}

type schedulerExecutor struct {
	mu    sync.Mutex
	kinds []SchedulerKind
	done  chan struct{}
}

func (e *schedulerExecutor) Execute(_ context.Context, task ScheduleTask) error {
	e.mu.Lock()
	e.kinds = append(e.kinds, task.Kind)
	complete := len(e.kinds) == 3
	e.mu.Unlock()
	if complete {
		close(e.done)
	}
	return nil
}

func TestSchedulerRunsAllOwnerKindsAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	executor := &schedulerExecutor{done: make(chan struct{})}
	scheduler := &Scheduler{Source: schedulerSource{}, Executor: executor, Interval: time.Hour, Now: func() time.Time { return time.Unix(0, 0) }}
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	select {
	case <-executor.done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not complete initial cycle")
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("err=%v", err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.kinds) != 3 || executor.kinds[0] != SchedulerScheduled || executor.kinds[1] != SchedulerQualityRecovery || executor.kinds[2] != SchedulerHealthRetry {
		t.Fatalf("kinds=%v", executor.kinds)
	}
}
