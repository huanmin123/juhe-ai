package jobsched

import (
	"context"
	"math"
	"testing"
	"time"
)

// ---- 时钟与定时器 ----

func TestSystemClockImmediateTimer(t *testing.T) {
	clock := SystemClock{}
	if clock.Now().IsZero() {
		t.Fatalf("系统时钟应返回当前时间")
	}
	timer := clock.NewTimer(0)
	defer timer.Stop()
	select {
	case <-timer.C():
	default:
		t.Fatalf("零延迟定时器应立即触发")
	}
}

// ---- 纯函数 ----

func TestEarlierTimeTable(t *testing.T) {
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	if got := earlierTime(nil, nil); got != nil {
		t.Fatalf("双空应返回 nil: %v", got)
	}
	if got := earlierTime(&late, nil); got != &late {
		t.Fatalf("右侧为空应返回左侧")
	}
	if got := earlierTime(nil, &late); got != &late {
		t.Fatalf("左侧为空应返回右侧")
	}
	if got := earlierTime(&late, &early); got != &early {
		t.Fatalf("应返回更早者")
	}
	if got := earlierTime(&early, &late); got != &early {
		t.Fatalf("应返回更早者")
	}
}

func TestClampDelayVariants(t *testing.T) {
	if got := clampDelay(-time.Second); got != 0 {
		t.Fatalf("负延迟应钳制为 0: %v", got)
	}
	if got := clampDelay(time.Second); got != time.Second {
		t.Fatalf("正延迟应原样: %v", got)
	}
}

func TestClamp01Variants(t *testing.T) {
	cases := []struct {
		value float64
		want  float64
	}{
		{-1, 0}, {math.NaN(), 0}, {0, 0}, {0.5, 0.5}, {1, 1}, {2, 1},
	}
	for _, tc := range cases {
		if got := clamp01(tc.value); got != tc.want {
			t.Fatalf("clamp01(%v)=%v，期望 %v", tc.value, got, tc.want)
		}
	}
}

func TestPassiveIntervalDelayVariants(t *testing.T) {
	interval := time.Second
	if got := passiveIntervalDelay(interval, false, func() float64 { return 0.5 }); got != interval {
		t.Fatalf("非 passive 应原样返回间隔: %v", got)
	}
	// random=0.5 → 偏移归一为 +1ms。
	got := passiveIntervalDelay(interval, true, func() float64 { return 0.5 })
	if got != interval+time.Millisecond {
		t.Fatalf("零偏移应归一为 1ms: %v", got)
	}
}

func TestPassiveScheduleOffsetMSVariants(t *testing.T) {
	if got := passiveScheduleOffsetMS(0, func() float64 { return 0.5 }); got != 0 {
		t.Fatalf("窗口为 0 应返回 0: %d", got)
	}
	// platformjitter.Window(1s) 为 500ms：random=0 → 下界 -500；
	// random=1 → 上界 +1；random=0.5 → 零偏移归一为 1。
	if got := passiveScheduleOffsetMS(time.Second, func() float64 { return 0 }); got != -500 {
		t.Fatalf("下界应为 -window(-500): %d", got)
	}
	if got := passiveScheduleOffsetMS(time.Second, func() float64 { return 1 }); got != 501 {
		t.Fatalf("上界应为 window+1(+501): %d", got)
	}
	if got := passiveScheduleOffsetMS(time.Second, func() float64 { return 0.5 }); got != 1 {
		t.Fatalf("零偏移应归一为 1: %d", got)
	}
}

func TestNextFixedRateTargetVariants(t *testing.T) {
	previous := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	interval := 10 * time.Second
	// 时钟倒退：以 previous 为锚立即重排。
	if got := nextFixedRateTarget(previous, interval, previous.Add(-time.Second)); !got.Equal(previous.Add(interval)) {
		t.Fatalf("倒退时钟应取下一整间隔: %v", got)
	}
	if got := nextFixedRateTarget(previous, interval, previous.Add(15*time.Second)); !got.Equal(previous.Add(20 * time.Second)) {
		t.Fatalf("跨间隔应对齐下一锚点: %v", got)
	}
	if got := nextFixedRateTarget(previous, interval, previous); !got.Equal(previous.Add(interval)) {
		t.Fatalf("当前时刻应取下一锚点: %v", got)
	}
}

func TestStableOffsetMSDeterministic(t *testing.T) {
	if got := stableOffsetMS("seed", 0); got != 0 {
		t.Fatalf("窗口为 0 应返回 0: %d", got)
	}
	first := stableOffsetMS("seed:job-a", time.Minute)
	second := stableOffsetMS("seed:job-a", time.Minute)
	if first != second {
		t.Fatalf("同种子应稳定: %d vs %d", first, second)
	}
	if first < 0 || first >= int64(time.Minute/time.Millisecond) {
		t.Fatalf("偏移应落在窗口内: %d", first)
	}
}

func TestScheduledAtForKinds(t *testing.T) {
	target := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := target.Add(time.Minute)
	if got := scheduledAtFor(fireLaneWake, target, now); !got.Equal(now) {
		t.Fatalf("lane 唤醒应以当下为调度时刻: %v", got)
	}
	if got := scheduledAtFor(fireRegular, target, now); !got.Equal(target) {
		t.Fatalf("常规触发应以目标时刻为调度时刻: %v", got)
	}
}

// ---- 调度器内部状态分支 ----

func TestNextTargetBranches(t *testing.T) {
	scheduler := NewScheduler(Options{})
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	job := &jobState{}
	if target, _ := scheduler.nextTarget(job); target != nil {
		t.Fatalf("无目标应返回 nil")
	}
	onlyFixed := base.Add(time.Second)
	job.fixedRateNext = &onlyFixed
	if target, kind := scheduler.nextTarget(job); target != &onlyFixed || kind != fireRegular {
		t.Fatalf("仅 fixedRate 应返回常规目标: %v %v", target, kind)
	}
	onlyDeferred := base.Add(2 * time.Second)
	job.deferredAt = &onlyDeferred
	job.fixedRateNext = nil
	if target, kind := scheduler.nextTarget(job); target != &onlyDeferred || kind != fireDeferred {
		t.Fatalf("仅 deferred 应返回延迟目标: %v %v", target, kind)
	}
	job.fixedRateNext = &onlyFixed
	if target, kind := scheduler.nextTarget(job); target != &onlyFixed || kind != fireRegular {
		t.Fatalf("更早者应胜出: %v %v", target, kind)
	}
	job.deferredAt = &base
	if target, kind := scheduler.nextTarget(job); target != &base || kind != fireDeferred {
		t.Fatalf("更早的 deferred 应胜出: %v %v", target, kind)
	}
}

func TestRecordSkipAndCoalesced(t *testing.T) {
	scheduler := NewScheduler(Options{})
	job := &jobState{spec: Spec{Name: "job-a"}}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scheduler.recordSkip(job, now, "running")
	if job.skipped != 1 || job.lastOutcome != OutcomeSkipped || job.lastSkipReason != "running" {
		t.Fatalf("跳过记账不符: %#v", job)
	}
	scheduler.markCoalesced(job, now)
	if job.coalesced != 1 || job.lastSkipReason != "running:coalesced" {
		t.Fatalf("合并记账不符: %#v", job)
	}
}

func TestAdvanceFixedRateVariants(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scheduler := NewScheduler(Options{Clock: newFakeClock(now), Random: func() float64 { return 0.5 }})
	job := &jobState{spec: Spec{Name: "job-a", Interval: time.Second}}
	past := now.Add(-time.Second)
	scheduler.advanceFixedRate(job, past)
	if job.fixedRateNext == nil || !job.fixedRateNext.After(now) {
		t.Fatalf("锚点应推进到未来: %v", job.fixedRateNext)
	}
	// passive 抖动：候选不晚于当下时回退为 now+1ms。
	jittered := &jobState{spec: Spec{Name: "job-b", Interval: time.Second, PassiveJitter: true}}
	scheduler.advanceFixedRate(jittered, now.Add(-time.Hour))
	if jittered.fixedRateNext == nil || !jittered.fixedRateNext.After(now) {
		t.Fatalf("抖动后锚点仍应在未来: %v", jittered.fixedRateNext)
	}
}

func TestReleaseLaneHandsOffQueuedJob(t *testing.T) {
	scheduler := NewScheduler(Options{Clock: newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))})
	first := &jobState{spec: Spec{Name: "job-a", Lane: "lane-1"}, wake: make(chan struct{}, 1)}
	second := &jobState{spec: Spec{Name: "job-b", Lane: "lane-1"}, wake: make(chan struct{}, 1)}
	scheduler.jobs[first.spec.Name] = first
	scheduler.jobs[second.spec.Name] = second
	lane := &laneState{runningJob: "job-a", queue: []string{"job-b"}}
	scheduler.lanes["lane-1"] = lane
	// 未知 lane：直接返回不 panic。
	missing := &jobState{spec: Spec{Name: "job-c", Lane: "nope"}}
	scheduler.releaseLane(missing)
	// 交接：队首任务被唤醒并标记占用。
	scheduler.releaseLane(first)
	select {
	case <-second.wake:
	default:
		t.Fatalf("队首任务应被唤醒")
	}
	scheduler.mu.Lock()
	running := lane.runningJob
	queued := second.laneQueued
	scheduler.mu.Unlock()
	if running != "job-b" || queued {
		t.Fatalf("交接后 running=job-b 且应清除排队标记: %s %v", running, queued)
	}
}

func TestScheduleValidationAndStop(t *testing.T) {
	scheduler := NewScheduler(Options{Clock: newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), Random: func() float64 { return 0.5 }})
	// 非法 spec 全部忽略。
	scheduler.Schedule(Spec{})
	scheduler.Schedule(Spec{Name: "a", Interval: time.Second})
	scheduler.Schedule(Spec{Name: "b", Task: func(context.Context, TaskContext) (TaskResult, error) { return TaskResult{}, nil }})
	scheduler.Schedule(Spec{Name: "dup", Interval: time.Second, Task: func(context.Context, TaskContext) (TaskResult, error) { return TaskResult{}, nil }})
	scheduler.Schedule(Spec{Name: "dup", Interval: time.Second, Task: func(context.Context, TaskContext) (TaskResult, error) { return TaskResult{}, nil }})
	scheduler.mu.Lock()
	count := len(scheduler.jobs)
	scheduler.mu.Unlock()
	if count != 1 {
		t.Fatalf("仅合法 spec 应被登记: %d", count)
	}
	scheduler.Stop()
	if !scheduler.isStopped() {
		t.Fatalf("Stop 后应处于停止状态")
	}
	// 停止后任务表被清空且不再接受注册。
	scheduler.Schedule(Spec{Name: "after-stop", Interval: time.Second, Task: func(context.Context, TaskContext) (TaskResult, error) { return TaskResult{}, nil }})
	scheduler.mu.Lock()
	count = len(scheduler.jobs)
	scheduler.mu.Unlock()
	if count != 0 {
		t.Fatalf("停止后任务应清空且不再登记: %d", count)
	}
	// NewScheduler 默认值。
	defaults := NewScheduler(Options{})
	if defaults.clock == nil || defaults.random == nil {
		t.Fatalf("默认时钟与随机源应被填充")
	}
}
