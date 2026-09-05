package jobsched

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeClock 支持手动推进时间的测试时钟（mock 时间推进）。
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*fakeTimer]struct{}
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start, timers: map[*fakeTimer]struct{}{}}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{clock: c, deadline: c.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		timer.fired = true
		select {
		case timer.ch <- c.now:
		default:
		}
	}
	c.timers[timer] = struct{}{}
	return timer
}

// Advance 推进时间并触发到期 timer。
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	for timer := range c.timers {
		if !timer.fired && !timer.stopped && !timer.deadline.After(c.now) {
			timer.fired = true
			due = append(due, timer)
		}
	}
	c.mu.Unlock()
	for _, timer := range due {
		select {
		case timer.ch <- timer.deadline:
		default:
		}
	}
}

type fakeTimer struct {
	clock    *fakeClock
	deadline time.Time
	ch       chan time.Time
	fired    bool
	stopped  bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	t.stopped = true
	delete(t.clock.timers, t)
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("条件在超时前未满足")
}

// settle 等待任务 goroutine 进入等待状态，避免 Advance 与注册 timer 竞争。
func settle() { time.Sleep(20 * time.Millisecond) }

func newTestScheduler(clock *fakeClock) *Scheduler {
	return NewScheduler(Options{StableSeed: "instance:stats-worker:0", Clock: clock, Random: func() float64 { return 0.5 }})
}

func TestSchedulerRunsFirstCycleAndRecordsSuccess(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	scheduler.Schedule(Spec{
		Name:          "usage-stats-consistency-check",
		Interval:      time.Hour,
		InitialDelay:  10 * time.Millisecond,
		ScheduleMode:  ScheduleModeFixedRate,
		OverlapPolicy: OverlapSkip,
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			return TaskResult{Outcome: OutcomeSuccess, LeaseState: LeaseNotRequired}, nil
		},
	})
	settle()
	clock.Advance(10 * time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshots := scheduler.Snapshots()
		return len(snapshots) == 1 && snapshots[0].RunCount == 1 && snapshots[0].SuccessCount == 1
	})
	snapshot := scheduler.Snapshots()[0]
	if snapshot.LastOutcome != string(OutcomeSuccess) || snapshot.LeaseState != LeaseNotRequired {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	drained, active := scheduler.StopAndDrain(time.Second)
	if !drained || active != 0 {
		t.Fatalf("expected clean drain, got drained=%v active=%d", drained, active)
	}
}

func TestSchedulerFailureBackoffRetries(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	attempts := 0
	scheduler.Schedule(Spec{
		Name:         "background-task-run-reconcile",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		ScheduleMode: ScheduleModeFixedDelay,
		Backoff:      &Backoff{Base: 5 * time.Millisecond, Max: 10 * time.Millisecond},
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			attempts++
			if attempts < 3 {
				return TaskResult{}, context.DeadlineExceeded
			}
			return TaskResult{}, nil
		},
	})
	settle()
	clock.Advance(time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshot := scheduler.Snapshots()[0]
		return snapshot.RunCount == 1 && snapshot.FailureCount == 1
	})
	clock.Advance(20 * time.Millisecond)
	waitFor(t, time.Second, func() bool { return scheduler.Snapshots()[0].FailureCount >= 2 })
	clock.Advance(20 * time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshot := scheduler.Snapshots()[0]
		return snapshot.SuccessCount >= 1 && snapshot.ConsecutiveFails == 0
	})
	if attempts < 3 {
		t.Fatalf("expected retry attempts, got %d", attempts)
	}
	scheduler.Stop()
}

func TestSchedulerRunTimeoutRecordsTimeout(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	scheduler.Schedule(Spec{
		Name:         "slow-job",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		Timeout:      5 * time.Millisecond,
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			if taskCtx.DeadlineAt == nil {
				t.Fatal("deadline must be exposed to the task")
			}
			<-ctx.Done()
			return TaskResult{}, ctx.Err()
		},
	})
	settle()
	clock.Advance(time.Millisecond)
	// 超时由 context.WithTimeout 的真实时间触发。
	waitFor(t, time.Second, func() bool {
		snapshot := scheduler.Snapshots()[0]
		return snapshot.TimedOutCount == 1
	})
	scheduler.Stop()
}

func TestSchedulerStopCancelsRunningTaskAndDrains(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	started := make(chan struct{})
	scheduler.Schedule(Spec{
		Name:         "long-job",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			close(started)
			<-ctx.Done()
			return TaskResult{}, nil
		},
	})
	settle()
	clock.Advance(time.Millisecond)
	<-started
	drained, active := scheduler.StopAndDrain(time.Second)
	if !drained || active != 0 {
		t.Fatalf("expected drained shutdown, got drained=%v active=%d", drained, active)
	}
}

func TestSchedulerResourceLaneSerializesAndSkips(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	release := make(chan struct{})
	firstStarted := make(chan struct{})
	secondSkipped := make(chan struct{}, 1)
	var once sync.Once
	scheduleLong := func(name string, block bool) {
		scheduler.Schedule(Spec{
			Name:         name,
			Interval:     time.Hour,
			InitialDelay: time.Millisecond,
			Lane:         "stats-heavy",
			Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
				if block {
					close(firstStarted)
					<-release
				}
				return TaskResult{}, nil
			},
		})
	}
	scheduleLong("first", true)
	scheduler.Schedule(Spec{
		Name:          "second",
		Interval:      time.Hour,
		InitialDelay:  time.Millisecond,
		OverlapPolicy: OverlapSkip,
		Lane:          "stats-heavy",
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			return TaskResult{}, nil
		},
	})
	// 监听 second 的 lane skip。
	go func() {
		for {
			for _, snapshot := range scheduler.Snapshots() {
				if snapshot.Name == "second" && snapshot.SkippedCount > 0 {
					once.Do(func() { close(secondSkipped) })
					return
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	settle()
	clock.Advance(time.Millisecond)
	<-firstStarted
	<-secondSkipped
	close(release)
	drained, _ := scheduler.StopAndDrain(time.Second)
	if !drained {
		t.Fatal("expected clean drain")
	}
}

func TestStableOffsetIsDeterministic(t *testing.T) {
	first := stableOffsetMS("instance:stats-worker:0:usage-stats-aggregation", 2*time.Second)
	second := stableOffsetMS("instance:stats-worker:0:usage-stats-aggregation", 2*time.Second)
	if first != second || first < 0 || first >= 2000 {
		t.Fatalf("stable offset must be deterministic within window, got %d", first)
	}
}

func TestPassiveInitialDelayStaysBounded(t *testing.T) {
	delay := passiveScheduleInitialDelayMS(10*time.Millisecond, 20*time.Millisecond, func() float64 { return 0.99 })
	if delay <= 0 || delay > 20*time.Millisecond {
		t.Fatalf("initial delay out of bounds: %v", delay)
	}
}
