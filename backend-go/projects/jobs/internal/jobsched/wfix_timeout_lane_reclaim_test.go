package jobsched

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 超时强制回收（lane 卡死缺陷）回归：Timeout 此前只是 taskCtx 取消，handler
// 忽略 ctx 卡死时 jobLoop 卡在 fire、lane 永久占用，同 lane 任务全部只留下
// Debug 级 jobsched_lane_busy（生产 2026-09-27 external-account-maintenance
// 停摆 9 小时）。修复后：超时先到即按 timeout 记账、释放 lane、复位 running；
// 泄漏 handler 迟到结果静默丢弃（不二次记账/二次日志），仅留一条
// jobsched_run_leaked_finished Info；超 Timeout + stuckGrace 仍在跑的泄漏 run
// 由调度器周期打 Error jobsched_run_stuck，按指数间隔重复。

// snapshotByName 按任务名取运行快照（Snapshots 按名字排序，不能按 [0] 取）。
func snapshotByName(scheduler *Scheduler, name string) (Snapshot, bool) {
	for _, snapshot := range scheduler.Snapshots() {
		if snapshot.Name == name {
			return snapshot, true
		}
	}
	return Snapshot{}, false
}

// TestSchedulerTimeoutReclaimsLaneAndSchedulesNext：handler 阻塞且忽略 ctx
// → 超时后 lane 被释放（同 lane 任务可执行）、job.running 复位、
// jobsched_run_timeout 恰好一条；下一轮任务可正常调度；泄漏 handler 迟到
// 返回不二次记账（TimedOutCount 不再增长，仅新增 leaked_finished Info）。
func TestSchedulerTimeoutReclaimsLaneAndSchedulesNext(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 27, 8, 45, 0, 0, time.UTC))
	scheduler, buffer := newW2LoggedScheduler(clock, slog.LevelDebug)
	var firstRuns atomic.Int64
	release := make(chan struct{})
	firstStarted := make(chan struct{}, 1)
	scheduler.Schedule(Spec{
		Name:         "stuck-maintenance",
		Interval:     30 * time.Millisecond,
		InitialDelay: time.Millisecond,
		Timeout:      20 * time.Millisecond,
		Lane:         "external-account-maintenance",
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			if firstRuns.Add(1) == 1 {
				// 第一轮模拟卡死：忽略 ctx，只等测试放行。
				select {
				case firstStarted <- struct{}{}:
				default:
				}
				<-release
			}
			return TaskResult{}, nil
		},
	})
	scheduler.Schedule(Spec{
		Name:          "same-lane-follower",
		Interval:      time.Hour,
		InitialDelay:  5 * time.Millisecond,
		OverlapPolicy: OverlapSkip,
		Lane:          "external-account-maintenance",
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			return TaskResult{}, nil
		},
	})
	settle()
	clock.Advance(time.Millisecond)
	<-firstStarted
	// 超时由 context.WithTimeout 的真实时间触发；回收后 running 复位。
	waitFor(t, time.Second, func() bool {
		snapshot, _ := snapshotByName(scheduler, "stuck-maintenance")
		return snapshot.TimedOutCount == 1 && !snapshot.Running && snapshot.RunningSince == nil
	})
	waitForLog(t, buffer, "level=WARN msg=jobsched_run_timeout")
	stuckSnapshot, _ := snapshotByName(scheduler, "stuck-maintenance")
	if stuckSnapshot.LastError != "后台任务执行超时" || stuckSnapshot.LastSkipReason != "timeout" {
		t.Fatalf("超时终态必须落账: %+v", stuckSnapshot)
	}
	// lane 必须已释放：同 lane 的 follower 在 5ms 到期后可直接执行。
	clock.Advance(4 * time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshot, ok := snapshotByName(scheduler, "same-lane-follower")
		return ok && snapshot.SuccessCount == 1
	})
	// 下一轮任务可正常调度：泄漏 handler 仍在跑，first 的下一轮照常成功。
	clock.Advance(30 * time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshot, _ := snapshotByName(scheduler, "stuck-maintenance")
		return snapshot.SuccessCount == 1 && snapshot.RunCount == 2
	})
	// 泄漏 handler 迟到返回：结果丢弃，只有一条 leaked_finished Info，
	// timeout 记账与日志不重复。
	close(release)
	waitForLog(t, buffer, "level=INFO msg=jobsched_run_leaked_finished")
	waitFor(t, time.Second, func() bool {
		snapshot, _ := snapshotByName(scheduler, "stuck-maintenance")
		return snapshot.TimedOutCount == 1 && snapshot.RunCount == 2 && snapshot.SuccessCount == 1
	})
	if timeouts := strings.Count(buffer.String(), "msg=jobsched_run_timeout"); timeouts != 1 {
		t.Fatalf("jobsched_run_timeout 必须恰好一条，得到 %d:\n%s", timeouts, buffer.String())
	}
	if drained, active := scheduler.StopAndDrain(time.Second); !drained || active != 0 {
		t.Fatalf("expected clean drain, got drained=%v active=%d", drained, active)
	}
}

// TestSchedulerShutdownDoesNotMisreportTimeout：带 Timeout 但停机先到——
// 停机分支保持 jobsched_run_stopped 行为，不强记 timeout。
func TestSchedulerShutdownDoesNotMisreportTimeout(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 27, 8, 45, 0, 0, time.UTC))
	scheduler, buffer := newW2LoggedScheduler(clock, slog.LevelDebug)
	started := make(chan struct{})
	release := make(chan struct{})
	scheduler.Schedule(Spec{
		Name:         "slow-on-shutdown",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		Timeout:      30 * time.Second,
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			close(started)
			<-release // 忽略 ctx 的卡死 handler：停机后由测试放行
			return TaskResult{}, nil
		},
	})
	settle()
	clock.Advance(time.Millisecond)
	<-started
	scheduler.Stop()
	close(release)
	// 停机分支等待 handler 返回后按 scheduler_stopped 记账。
	drained, active := scheduler.StopAndDrain(2 * time.Second)
	if !drained || active != 0 {
		t.Fatalf("expected drained shutdown, got drained=%v active=%d", drained, active)
	}
	waitForLog(t, buffer, "level=DEBUG msg=jobsched_run_stopped")
	logs := buffer.String()
	if strings.Contains(logs, "jobsched_run_timeout") {
		t.Fatalf("停机轮不得误报 timeout:\n%s", logs)
	}
	if strings.Contains(logs, "jobsched_run_success") {
		t.Fatalf("停机轮不得记成功:\n%s", logs)
	}
}

// raceBuffer 是互斥保护的日志缓冲：stuck watcher goroutine 写、测试主
// goroutine 轮询读（waitFor/stuckCount），-race 下必须同步。
type raceBuffer struct {
	mu   sync.Mutex
	text strings.Builder
}

func (b *raceBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.Write(p)
}

func (b *raceBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String()
}

// pumpStuckWatch 以 watcher 的扫描步长（stuckWatchInterval）小步推进
// fakeClock，直至 done 成立或真实时间超时，返回达成时的累计推进量。
// fakeClock.Advance 只触发当前已到期的 timer（见 scheduler_test.go），单次
// 大步推进与 watcher「处理完本轮 → 挂下一个 timer」之间存在错过窗口：错过
// 一次 tick 后没有后续 Advance，条件永不再满足、等待必然超时（复审 M2 同族
// 竞态）。小步推进并在步间让出，保证 watcher 每轮处理完都能赶上下一次推进。
func pumpStuckWatch(t *testing.T, clock *fakeClock, done func() bool, maxElapsed time.Duration) time.Duration {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var elapsed time.Duration
	for {
		if done() {
			return elapsed
		}
		if time.Now().After(deadline) {
			t.Fatalf("stuck watcher 在推进 %v 内未达成条件（elapsed=%v）", maxElapsed, elapsed)
		}
		if elapsed < maxElapsed {
			clock.Advance(stuckWatchInterval)
			elapsed += stuckWatchInterval
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSchedulerStuckLogEmitsWithExponentialRepeat：以生产默认节奏
// （grace=5min、重复 5min→10min→20min 封顶、扫描 1min）验证 jobsched_run_stuck
// 的首次触发、×2 指数重复与封顶，以及泄漏 handler 迟到返回后停止重复。
// fakeClock 是虚拟时钟，对 5min 级 duration 的推进同样廉价，因此不覆写包级
// var——覆写的恢复（t.Cleanup 写）与常驻 stuckWatchLoop goroutine 的读之间
// 无法建立 happens-before，-race 必报（复审 M2 修复顺带消除）。
func TestSchedulerStuckLogEmitsWithExponentialRepeat(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 27, 8, 45, 0, 0, time.UTC))
	logBuffer := &raceBuffer{}
	scheduler := NewScheduler(Options{
		StableSeed: "instance:stats-worker:0",
		Clock:      clock,
		Random:     func() float64 { return 0.5 },
		Logger:     slog.New(slog.NewTextHandler(logBuffer, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	scheduler.Schedule(Spec{
		Name:         "leaked-run",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		// Timeout 用真实 context 超时触发回收；它在 stuck 阈值里只是
		// 20ms 的 duration 值（fake 时间戳 + duration 纯算术），不影响
		// 下面的分钟级推进边界。
		Timeout: 20 * time.Millisecond,
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			return TaskResult{}, nil
		},
	})
	settle()
	clock.Advance(time.Millisecond)
	<-started
	stuckCount := func() int { return strings.Count(logBuffer.String(), "msg=jobsched_run_stuck") }
	waitForLogText := func(substr string) {
		waitFor(t, time.Second, func() bool { return strings.Contains(logBuffer.String(), substr) })
	}
	// 超时回收落账（TimedOutCount==1）与泄漏登记（leakStartedAt 非 nil）必须
	// 都就绪才允许推进时钟：两者分属 finishRun 与 trackLeakedHandler 两步、
	// 中间无同步点，登记未完成时 watcher 的扫描会因 leakStartedAt==nil 跳过，
	// 而 fakeClock 的 Advance 只触发当前已到期的 timer，错过一次 tick 后没有
	// 后续 Advance、等待必然超时（复审 M2）。
	waitFor(t, time.Second, func() bool {
		snapshot, _ := snapshotByName(scheduler, "leaked-run")
		scheduler.mu.Lock()
		job := scheduler.jobs["leaked-run"]
		leaked := job != nil && job.leakStartedAt != nil
		scheduler.mu.Unlock()
		return snapshot.TimedOutCount == 1 && leaked
	})
	// 未越过 startedAt + Timeout(20ms) + grace(5min) 不得打 stuck 日志：
	// 推进 4min（距阈值差 1min+）并给 watcher 处理窗口后仍须为 0 条。
	clock.Advance(4 * time.Minute)
	time.Sleep(50 * time.Millisecond)
	if stuckCount() != 0 {
		t.Fatalf("未越过 Timeout+grace 不得打 stuck 日志，得到 %d:\n%s", stuckCount(), logBuffer.String())
	}
	// 阈值 = startedAt + 20ms + 5min：再推进经过 1min+20ms 的边界，首条在
	// 第 2 个扫描步出现。
	if elapsed := pumpStuckWatch(t, clock, func() bool { return stuckCount() >= 1 }, 2*time.Minute); elapsed != 2*time.Minute {
		t.Fatalf("首条 stuck 应在 Timeout+grace 后的首个扫描步出现，实际累计推进 %v", elapsed)
	}
	waitForLogText("level=ERROR msg=jobsched_run_stuck")
	logs := logBuffer.String()
	if !strings.Contains(logs, "job=leaked-run") || !strings.Contains(logs, "runningSince=") || !strings.Contains(logs, "overdueMs=") {
		t.Fatalf("stuck 日志必须携带 job/runningSince/overdueMs:\n%s", logs)
	}
	// 重复间隔 = stuckLogRepeat(5min)：恰在 5min 步进处出现第二条；若间隔
	// 逻辑缺失（立即重复）会在更小步进达成、断言失败。
	if elapsed := pumpStuckWatch(t, clock, func() bool { return stuckCount() >= 2 }, 5*time.Minute); elapsed != 5*time.Minute {
		t.Fatalf("第二条应在 5min 重复间隔边界出现，实际累计推进 %v", elapsed)
	}
	// 间隔 ×2 → 10min：第三条恰在 10min 步进处出现；若未放大（仍 5min）会在
	// 5min 达成、断言失败。
	if elapsed := pumpStuckWatch(t, clock, func() bool { return stuckCount() >= 3 }, 10*time.Minute); elapsed != 10*time.Minute {
		t.Fatalf("第三条应在放大后的 10min 间隔边界出现，实际累计推进 %v", elapsed)
	}
	// 间隔再 ×2 = 20min 恰为封顶：第四条恰在 20min 步进处出现；未封顶则
	// 20min 内无法达成、pump 超时失败。
	if elapsed := pumpStuckWatch(t, clock, func() bool { return stuckCount() >= 4 }, 20*time.Minute); elapsed != 20*time.Minute {
		t.Fatalf("第四条应在封顶后的 20min 间隔边界出现，实际累计推进 %v", elapsed)
	}
	// 泄漏 handler 迟到返回：登记清理，stuck 日志停止重复。
	close(release)
	waitForLogText("level=INFO msg=jobsched_run_leaked_finished")
	clock.Advance(200 * time.Minute)
	time.Sleep(50 * time.Millisecond) // 给 watcher 处理到期 tick 的窗口
	if stuckCount() != 4 {
		t.Fatalf("handler 迟到返回后不得再打 stuck 日志，得到 %d:\n%s", stuckCount(), logBuffer.String())
	}
	if drained, _ := scheduler.StopAndDrain(time.Second); !drained {
		t.Fatal("expected clean drain")
	}
}
