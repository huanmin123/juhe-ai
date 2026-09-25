package jobsched

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// W2 逐轮 outcome 日志测试：注入 slog.TextHandler + 假时钟，断言各级别
// 事件与字段；nil logger 路径断言零行为变化。

// newW2LoggedScheduler 构建带 TextHandler 日志的调度器，返回调度器与日志
// buffer；级别由调用方指定。
func newW2LoggedScheduler(clock *fakeClock, level slog.Level) (*Scheduler, *bytes.Buffer) {
	buffer := &bytes.Buffer{}
	scheduler := NewScheduler(Options{
		StableSeed: "instance:stats-worker:0",
		Clock:      clock,
		Random:     func() float64 { return 0.5 },
		Logger:     slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: level})),
	})
	return scheduler, buffer
}

// waitForLog 等待日志出现（记账与锁外日志之间存在微小窗口）。
func waitForLog(t *testing.T, buffer *bytes.Buffer, substr string) {
	t.Helper()
	waitFor(t, time.Second, func() bool {
		return strings.Contains(buffer.String(), substr)
	})
}

func assertLogContains(t *testing.T, logs string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(logs, want) {
			t.Fatalf("日志缺少 %q:\n%s", want, logs)
		}
	}
}

func TestW2RunFailedEmitsWarnWithFields(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler, buffer := newW2LoggedScheduler(clock, slog.LevelDebug)
	scheduler.Schedule(Spec{
		Name:         "background-task-run-reconcile",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		ScheduleMode: ScheduleModeFixedDelay,
		Backoff:      &Backoff{Base: 5 * time.Millisecond, Max: 10 * time.Millisecond},
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			return TaskResult{}, errors.New("boom")
		},
	})
	settle()
	clock.Advance(time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshot := scheduler.Snapshots()[0]
		return snapshot.RunCount == 1 && snapshot.FailureCount == 1 && snapshot.ConsecutiveFails == 1
	})
	waitForLog(t, buffer, "level=WARN msg=jobsched_run_failed")
	assertLogContains(t, buffer.String(),
		"job=background-task-run-reconcile",
		"error=boom",
		"durationMs=",
		"consecFail=1",
		"backoffMs=")
	scheduler.Stop()
}

func TestW2RunSuccessEmitsDebugOnly(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler, buffer := newW2LoggedScheduler(clock, slog.LevelDebug)
	scheduler.Schedule(Spec{
		Name:          "usage-stats-consistency-check",
		Interval:      time.Hour,
		InitialDelay:  time.Millisecond,
		ScheduleMode:  ScheduleModeFixedDelay,
		OverlapPolicy: OverlapSkip,
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			return TaskResult{}, nil
		},
	})
	settle()
	clock.Advance(time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshot := scheduler.Snapshots()[0]
		return snapshot.RunCount == 1 && snapshot.SuccessCount == 1
	})
	waitForLog(t, buffer, "level=DEBUG msg=jobsched_run_success")
	logs := buffer.String()
	assertLogContains(t, logs, "job=usage-stats-consistency-check", "durationMs=")
	if strings.Contains(logs, "level=WARN") || strings.Contains(logs, "level=INFO") {
		t.Fatalf("成功轮不得产生 WARN/INFO 日志:\n%s", logs)
	}
	scheduler.Stop()
}

func TestW2NilLoggerStaysNoop(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	// 成功路径：nil logger 不 panic、记账不变。
	scheduler := newTestScheduler(clock)
	scheduler.Schedule(Spec{
		Name:         "usage-stats-consistency-check",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		ScheduleMode: ScheduleModeFixedDelay,
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			return TaskResult{}, nil
		},
	})
	settle()
	clock.Advance(time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshot := scheduler.Snapshots()[0]
		return snapshot.RunCount == 1 && snapshot.SuccessCount == 1
	})
	scheduler.Stop()

	// 失败路径：覆盖 runErr/backoffUntil 快照采集分支的 no-op 提前返回。
	failureClock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	failureScheduler := newTestScheduler(failureClock)
	failureScheduler.Schedule(Spec{
		Name:         "background-task-run-reconcile",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		ScheduleMode: ScheduleModeFixedDelay,
		Backoff:      &Backoff{Base: 5 * time.Millisecond, Max: 10 * time.Millisecond},
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			return TaskResult{}, errors.New("boom")
		},
	})
	settle()
	failureClock.Advance(time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshot := failureScheduler.Snapshots()[0]
		return snapshot.FailureCount == 1 && snapshot.ConsecutiveFails == 1
	})
	failureScheduler.Stop()
}

func TestW2RunTimeoutEmitsWarnWithNextRetryAt(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler, buffer := newW2LoggedScheduler(clock, slog.LevelDebug)
	scheduler.Schedule(Spec{
		Name:         "slow-job",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		Timeout:      5 * time.Millisecond,
		Backoff:      &Backoff{Base: 10 * time.Millisecond, Max: 20 * time.Millisecond},
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			<-ctx.Done()
			return TaskResult{}, ctx.Err()
		},
	})
	settle()
	clock.Advance(time.Millisecond)
	// 超时由 context.WithTimeout 的真实时间触发。
	waitFor(t, time.Second, func() bool {
		return scheduler.Snapshots()[0].TimedOutCount == 1
	})
	waitForLog(t, buffer, "level=WARN msg=jobsched_run_timeout")
	logs := buffer.String()
	assertLogContains(t, logs, "job=slow-job", "durationMs=", "consecFail=1", "nextRetryAt=")
	// timeout 分支优先于 runErr 分支：不得降级为 jobsched_run_failed。
	if strings.Contains(logs, "jobsched_run_failed") {
		t.Fatalf("timeout 轮不得输出 jobsched_run_failed:\n%s", logs)
	}
	scheduler.Stop()
}

func TestW2RunPartialWarnAndSkippedDebug(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler, buffer := newW2LoggedScheduler(clock, slog.LevelDebug)
	scheduler.Schedule(Spec{
		Name:         "partial-job",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		ScheduleMode: ScheduleModeFixedDelay,
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			return TaskResult{Outcome: OutcomePartial, Warning: "部分数据待补"}, nil
		},
	})
	settle()
	clock.Advance(time.Millisecond)
	waitFor(t, time.Second, func() bool {
		return scheduler.Snapshots()[0].PartialCount == 1
	})
	waitForLog(t, buffer, "level=WARN msg=jobsched_run_partial")
	logs := buffer.String()
	assertLogContains(t, logs, "job=partial-job", "warning=部分数据待补")
	if strings.Contains(logs, "jobsched_run_success") {
		t.Fatalf("partial 轮不得输出 jobsched_run_success:\n%s", logs)
	}
	scheduler.Stop()

	// OutcomeSkipped → Debug jobsched_run_skipped（task_skipped）。
	skippedClock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	skippedScheduler, skippedBuffer := newW2LoggedScheduler(skippedClock, slog.LevelDebug)
	skippedScheduler.Schedule(Spec{
		Name:         "skipped-job",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		ScheduleMode: ScheduleModeFixedDelay,
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			return TaskResult{Outcome: OutcomeSkipped}, nil
		},
	})
	settle()
	skippedClock.Advance(time.Millisecond)
	waitFor(t, time.Second, func() bool {
		return skippedScheduler.Snapshots()[0].TaskSkippedCount == 1
	})
	waitForLog(t, skippedBuffer, "level=DEBUG msg=jobsched_run_skipped")
	skippedLogs := skippedBuffer.String()
	assertLogContains(t, skippedLogs, "job=skipped-job", "skipReason=task_skipped")
	if strings.Contains(skippedLogs, "level=WARN") {
		t.Fatalf("skipped 轮不得产生 WARN:\n%s", skippedLogs)
	}
	skippedScheduler.Stop()
}

func TestW2BackoffSkipEmitsDebug(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler, buffer := newW2LoggedScheduler(clock, slog.LevelDebug)
	scheduler.Schedule(Spec{
		Name:         "background-task-run-reconcile",
		Interval:     10 * time.Millisecond,
		InitialDelay: time.Millisecond,
		ScheduleMode: ScheduleModeFixedRate,
		// Random=0.5 → 首败退避 = 0.5 * (50ms+1) ≈ 25ms，覆盖 11ms/21ms 锚点。
		Backoff: &Backoff{Base: 50 * time.Millisecond, Max: 100 * time.Millisecond},
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			return TaskResult{}, errors.New("boom")
		},
	})
	settle()
	clock.Advance(time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshot := scheduler.Snapshots()[0]
		return snapshot.FailureCount == 1 && snapshot.ConsecutiveFails == 1
	})
	clock.Advance(10 * time.Millisecond)
	waitFor(t, time.Second, func() bool {
		return scheduler.Snapshots()[0].SkippedCount >= 1
	})
	waitForLog(t, buffer, "level=DEBUG msg=jobsched_run_backoff_skip")
	assertLogContains(t, buffer.String(), "job=background-task-run-reconcile")
	scheduler.Stop()
}

func TestW2LaneBusyEmitsDebug(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler, buffer := newW2LoggedScheduler(clock, slog.LevelDebug)
	release := make(chan struct{})
	firstStarted := make(chan struct{})
	scheduler.Schedule(Spec{
		Name:         "first",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		Lane:         "stats-heavy",
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			close(firstStarted)
			<-release
			return TaskResult{}, nil
		},
	})
	scheduler.Schedule(Spec{
		Name:          "second",
		Interval:      time.Hour,
		InitialDelay:  2 * time.Millisecond,
		OverlapPolicy: OverlapSkip,
		Lane:          "stats-heavy",
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			return TaskResult{}, nil
		},
	})
	settle()
	clock.Advance(time.Millisecond)
	<-firstStarted
	clock.Advance(time.Millisecond)
	waitForLog(t, buffer, "level=DEBUG msg=jobsched_lane_busy")
	assertLogContains(t, buffer.String(), "job=second", "lane=stats-heavy")
	close(release)
	drained, _ := scheduler.StopAndDrain(time.Second)
	if !drained {
		t.Fatal("expected clean drain")
	}
}

func TestW2SchedulerStoppedEmitsDebug(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler, buffer := newW2LoggedScheduler(clock, slog.LevelDebug)
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
	waitForLog(t, buffer, "level=DEBUG msg=jobsched_run_stopped")
	logs := buffer.String()
	assertLogContains(t, logs, "job=long-job", "skipReason=scheduler_stopped")
	// 停机分支优先于成功分支：不得输出 jobsched_run_success。
	if strings.Contains(logs, "jobsched_run_success") {
		t.Fatalf("停机轮不得输出 jobsched_run_success:\n%s", logs)
	}
}

// TestW2StopWindowPanicCountsAsFailure：停机窗口内 panic 的轮次按失败记账，
// 并保留 panicked Error 日志。此前 panic 落入 stoppedRun 分支被记成
// skipped、日志只有 jobsched_run_stopped，panic 缺陷被停机掩盖。
// consecFail 只在失败会计分支赋值（stoppedRun 分支恒 0），作为分类的
// 日志侧证据（Stop 清空 jobs map 后 Snapshots 不可用）。
func TestW2StopWindowPanicCountsAsFailure(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler, buffer := newW2LoggedScheduler(clock, slog.LevelDebug)
	started := make(chan struct{})
	release := make(chan struct{})
	scheduler.Schedule(Spec{
		Name:         "stop-panic-job",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			close(started)
			<-release
			panic("boom-on-stop")
		},
	})
	settle()
	clock.Advance(time.Millisecond)
	<-started
	// Stop 同步置停机态后任务仍阻塞在 release 上，构成停机窗口内的 panic。
	scheduler.Stop()
	close(release)
	if drained, active := scheduler.StopAndDrain(time.Second); !drained || active != 0 {
		t.Fatalf("expected drained shutdown, got drained=%v active=%d", drained, active)
	}
	waitForLog(t, buffer, "msg=jobsched_run_panicked")
	logs := buffer.String()
	assertLogContains(t, logs, "job=stop-panic-job", "panic=boom-on-stop", "consecFail=1")
	if strings.Contains(logs, "jobsched_run_stopped") {
		t.Fatalf("停机窗口 panic 不得再输出 jobsched_run_stopped:\n%s", logs)
	}
}
