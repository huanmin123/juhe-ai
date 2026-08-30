package modelcheckowner

import (
	"context"
	"errors"
	"strings"
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

type isolatedFailureSource struct {
	mu sync.Mutex
}

func (s *isolatedFailureSource) Claim(_ context.Context, kind SchedulerKind, _ time.Time, _ int) ([]ScheduleTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch kind {
	case SchedulerScheduled:
		return nil, errors.New("scheduled claim unavailable")
	case SchedulerQualityRecovery:
		return []ScheduleTask{{ID: "execute-fails", Kind: kind}, {ID: "complete-stale", Kind: kind}}, nil
	case SchedulerHealthRetry:
		return []ScheduleTask{{ID: "healthy-task", Kind: kind}}, nil
	default:
		return nil, nil
	}
}

func (s *isolatedFailureSource) Complete(_ context.Context, task ScheduleTask) error {
	if task.ID == "complete-stale" {
		return errors.New("complete rejected by owner/fence")
	}
	return nil
}

func (s *isolatedFailureSource) Fail(_ context.Context, task ScheduleTask, _ error) error {
	if task.ID == "execute-fails" {
		return errors.New("fail rejected by owner/fence")
	}
	return nil
}

type isolatedFailureExecutor struct {
	mu       sync.Mutex
	executed map[string]struct{}
	done     chan struct{}
}

func (e *isolatedFailureExecutor) Execute(_ context.Context, task ScheduleTask) error {
	e.mu.Lock()
	e.executed[task.ID] = struct{}{}
	completed := len(e.executed) == 3
	e.mu.Unlock()
	if completed {
		close(e.done)
	}
	if task.ID == "execute-fails" {
		return errors.New("probe failed")
	}
	return nil
}

func TestSchedulerIsolatesClaimAndTaskLifecycleFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executor := &isolatedFailureExecutor{executed: make(map[string]struct{}), done: make(chan struct{})}
	errorsSeen := make(chan SchedulerError, 4)
	scheduler := &Scheduler{
		Source:    &isolatedFailureSource{},
		Executor:  executor,
		Interval:  time.Hour,
		ErrorSink: func(event SchedulerError) { errorsSeen <- event },
	}
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	select {
	case <-executor.done:
	case <-time.After(time.Second):
		t.Fatal("scheduler stopped before unaffected tasks completed")
	}
	seen := make(map[SchedulerErrorOperation]map[string]SchedulerError)
	for want := 4; want > 0; want-- {
		select {
		case event := <-errorsSeen:
			if seen[event.Operation] == nil {
				seen[event.Operation] = make(map[string]SchedulerError)
			}
			seen[event.Operation][event.Task.ID] = event
		case <-time.After(time.Second):
			t.Fatal("scheduler did not report all isolated failures")
		}
	}
	if event, ok := seen[SchedulerErrorClaim][""]; !ok || event.Kind != SchedulerScheduled || event.Err == nil || event.Err.Error() != "scheduled claim unavailable" {
		t.Fatalf("claim error=%+v, want scheduled claim failure", event)
	}
	if event, ok := seen[SchedulerErrorExecute]["execute-fails"]; !ok || event.Kind != SchedulerQualityRecovery || event.Err == nil || event.Err.Error() != "probe failed" {
		t.Fatalf("execute error=%+v, want recovery probe failure", event)
	}
	if event, ok := seen[SchedulerErrorFail]["execute-fails"]; !ok || event.Kind != SchedulerQualityRecovery || event.Err == nil || !strings.Contains(event.Err.Error(), "fail rejected by owner/fence") {
		t.Fatalf("fail error=%+v, want recovery failure CAS error", event)
	}
	if event, ok := seen[SchedulerErrorComplete]["complete-stale"]; !ok || event.Kind != SchedulerQualityRecovery || event.Err == nil || event.Err.Error() != "complete rejected by owner/fence" {
		t.Fatalf("complete error=%+v, want recovery completion CAS error", event)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("scheduler err=%v, want context canceled", err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	for _, id := range []string{"execute-fails", "complete-stale", "healthy-task"} {
		if _, ok := executor.executed[id]; !ok {
			t.Fatalf("unaffected task %q was not executed", id)
		}
	}
}
