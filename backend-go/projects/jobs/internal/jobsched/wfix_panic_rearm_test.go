package jobsched

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// jobs 框架域排查缺陷2/缺陷5 的回归测试：
//   - 缺陷2：任务 panic 此前无任务级 recover，外溢到 jobLoop 屏障后
//     job.running 卡 true、终态不写、任务静默死亡直至重启；修复后 panic 转
//     为失败终态并按退避语义重排。
//   - 缺陷5：fixedDelay 触发即清锚点，错过间隔的 skip-continue 路径跳过
//     armPostRun，nextTarget 双空会让 jobLoop return；修复后 skip 路径对
//     fixedDelay 重建下一轮锚点。

// TestSchedulerTaskPanicFailsRunAndReschedules：注入 panic 任务（测试钩子），
// 断言 panic 后 job 不再卡 Running:true、失败终态（LastError 含 panic 值）
// 落账、下一轮按退避重排可再调度。
func TestSchedulerTaskPanicFailsRunAndReschedules(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	var runs atomic.Int64
	scheduler.Schedule(Spec{
		Name:         "panic-job",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		Backoff:      &Backoff{Base: 5 * time.Millisecond, Max: 10 * time.Millisecond},
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			if runs.Add(1) == 1 {
				panic("boom-inject")
			}
			return TaskResult{}, nil
		},
	})
	settle()
	clock.Advance(time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshot := scheduler.Snapshots()[0]
		return snapshot.RunCount == 1 && snapshot.FailureCount == 1 && !snapshot.Running
	})
	snapshot := scheduler.Snapshots()[0]
	if !strings.Contains(snapshot.LastError, "boom-inject") {
		t.Fatalf("失败终态必须留痕 panic 值: %q", snapshot.LastError)
	}
	if snapshot.LastErrorAt == nil || snapshot.LastFinishedAt == nil {
		t.Fatalf("panic 轮必须落 error/finished 时间戳: %+v", snapshot)
	}
	// 下一轮按退避重排：deferred 重试成功，任务存活。
	clock.Advance(20 * time.Millisecond)
	waitFor(t, time.Second, func() bool {
		after := scheduler.Snapshots()[0]
		return after.SuccessCount >= 1 && after.ConsecutiveFails == 0
	})
	if runs.Load() < 2 {
		t.Fatalf("panic 后任务必须可再调度，实际执行轮数 = %d", runs.Load())
	}
	if drained, _ := scheduler.StopAndDrain(time.Second); !drained {
		t.Fatal("panic 任务不应阻碍停机排空")
	}
}

// TestFixedDelayRegularSkipRearmsNextTarget：skip 路径的 fixedDelay 锚点重建
// ——双空态（fire 已清锚点 + skip 跳过 armPostRun）必须被补齐；fixedRate 与
// 仍有目标的状态不受影响。
func TestFixedDelayRegularSkipRearmsNextTarget(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	now := clock.Now()

	// fixedDelay 双空态：重建下一轮锚点，nextTarget 不再为 nil（任务存活）。
	fixedDelayJob := &jobState{
		spec: Spec{Name: "fd", Interval: 10 * time.Millisecond, ScheduleMode: ScheduleModeFixedDelay},
		wake: make(chan struct{}, 1),
	}
	scheduler.rearmAfterRegularSkip(fixedDelayJob, now)
	if fixedDelayJob.fixedRateNext == nil || !fixedDelayJob.fixedRateNext.After(now) {
		t.Fatalf("fixedDelay skip 后必须重建未来锚点: %v", fixedDelayJob.fixedRateNext)
	}
	if target, _ := scheduler.nextTarget(fixedDelayJob); target == nil {
		t.Fatal("重建后 nextTarget 不得为空（否则 jobLoop return 任务死亡）")
	}

	// fixedRate：锚点在 fire 时已推进，重建反而多挪一个间隔——必须不动。
	fixedRateJob := &jobState{
		spec: Spec{Name: "fr", Interval: 10 * time.Millisecond, ScheduleMode: ScheduleModeFixedRate},
		wake: make(chan struct{}, 1),
	}
	existing := now.Add(time.Hour)
	fixedRateJob.fixedRateNext = &existing
	scheduler.rearmAfterRegularSkip(fixedRateJob, now)
	if fixedRateJob.fixedRateNext != &existing {
		t.Fatalf("fixedRate 锚点不得被 skip 重排改动: %v", fixedRateJob.fixedRateNext)
	}

	// fixedDelay 但目标/退避重试仍在：不覆盖既有目标。
	deferred := now.Add(time.Minute)
	queuedJob := &jobState{
		spec:       Spec{Name: "fd-queued", Interval: 10 * time.Millisecond, ScheduleMode: ScheduleModeFixedDelay},
		wake:       make(chan struct{}, 1),
		deferredAt: &deferred,
	}
	scheduler.rearmAfterRegularSkip(queuedJob, now)
	if queuedJob.fixedRateNext != nil {
		t.Fatalf("既有 deferred 目标存在时不得重建锚点: %v", queuedJob.fixedRateNext)
	}
}

// TestFixedRateMissedIntervalSkipKeepsJobAlive：错过间隔的 skip-continue
// 路径（修复点所在分支）行为回归——首轮横跨后续锚点时按 skip 记账且任务
// 存活可再调度（skip 后仍有触发目标，不再需要收尾重排）。
func TestFixedRateMissedIntervalSkipKeepsJobAlive(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	release := make(chan struct{})
	var runs atomic.Int64
	scheduler.Schedule(Spec{
		Name:          "overrun-job",
		Interval:      10 * time.Millisecond,
		InitialDelay:  time.Millisecond,
		OverlapPolicy: OverlapSkip,
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			if runs.Add(1) == 1 {
				<-release // 第一轮横跨后续锚点，制造错过间隔
			}
			return TaskResult{}, nil
		},
	})
	settle()
	clock.Advance(time.Millisecond)
	waitFor(t, time.Second, func() bool { return runs.Load() == 1 })
	clock.Advance(50 * time.Millisecond)
	close(release)
	// 首轮结束后下一锚点已错过：skip 记账，且任务不卡运行态。
	waitFor(t, time.Second, func() bool {
		snapshot := scheduler.Snapshots()[0]
		return snapshot.SkippedCount >= 1 && !snapshot.Running
	})
	// skip 后下一目标仍存在：推进时钟即可再调度（skip 路径不产生双空）。
	clock.Advance(20 * time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshot := scheduler.Snapshots()[0]
		return snapshot.RunCount >= 2 && snapshot.SuccessCount >= 2 && snapshot.NextRunAt != nil
	})
	if drained, _ := scheduler.StopAndDrain(time.Second); !drained {
		t.Fatal("skip 路径不应阻碍停机排空")
	}
}
