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

func TestSchedulerRejectsPartialOrUnknownKindConfiguration(t *testing.T) {
	for name, kinds := range map[string][]SchedulerKind{
		"partial":   {SchedulerScheduled},
		"duplicate": {SchedulerScheduled, SchedulerScheduled, SchedulerHealthRetry},
		"unknown":   {SchedulerScheduled, SchedulerQualityRecovery, "other"},
	} {
		t.Run(name, func(t *testing.T) {
			scheduler := &Scheduler{Source: schedulerSource{}, Executor: &schedulerExecutor{done: make(chan struct{})}, Kinds: kinds}
			if err := scheduler.Run(context.Background()); err == nil {
				t.Fatal("partial/unknown scheduler configuration must fail closed")
			}
		})
	}
}

type concurrentBatchSource struct {
	mu sync.Mutex
}

func (s *concurrentBatchSource) Claim(_ context.Context, kind SchedulerKind, _ time.Time, _ int) ([]ScheduleTask, error) {
	// Two tasks per kind make it possible to observe concurrent execution while
	// Scheduler still visits the three owner kinds in deterministic order.
	return []ScheduleTask{{ID: string(kind) + "-1", Kind: kind}, {ID: string(kind) + "-2", Kind: kind}}, nil
}

type concurrentBatchExecutor struct {
	mu      sync.Mutex
	active  int
	maxSeen int
	done    chan struct{}
}

func (e *concurrentBatchExecutor) Execute(_ context.Context, _ ScheduleTask) error {
	e.mu.Lock()
	e.active++
	if e.active > e.maxSeen {
		e.maxSeen = e.active
	}
	e.mu.Unlock()
	// Keep each task inside Execute briefly so sibling tasks overlap.
	time.Sleep(10 * time.Millisecond)
	e.mu.Lock()
	e.active--
	if e.active == 0 && e.maxSeen >= 2 {
		select {
		case <-e.done:
		default:
			close(e.done)
		}
	}
	e.mu.Unlock()
	return nil
}

func TestSchedulerExecutesClaimedBatchConcurrently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executor := &concurrentBatchExecutor{done: make(chan struct{})}
	scheduler := &Scheduler{Source: &concurrentBatchSource{}, Executor: executor, Interval: time.Hour}
	completed := make(chan error, 1)
	go func() { completed <- scheduler.Run(ctx) }()
	select {
	case <-executor.done:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("scheduler batch did not execute concurrently")
	}
	if err := <-completed; err != context.Canceled {
		t.Fatalf("scheduler err=%v", err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.maxSeen < 2 {
		t.Fatalf("max concurrent executions=%d, want at least 2", executor.maxSeen)
	}
}
