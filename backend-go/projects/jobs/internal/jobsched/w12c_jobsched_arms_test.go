// 波次 w12c：补齐 jobsched 剩余分支覆盖（目标包覆盖率 ≥95%）。
//
// 不可达语句登记（无自然触发路径，不做无语义强注入）：
//   - scheduler.go nextTarget 的 target==nil return：fixedRate 锚点一旦排定
//     永不清空，fixedDelay 模式 fire 后 armPostRun 必重排下一轮，两者同时为空
//     不可达（防御守卫）。
//   - scheduler.go armPostRun 的 pending 补跑分支：jobState.pending 在生产代码
//     中无任何 true 赋值点（仅 armPostRun 内置 false），分支不可达。
//   - scheduler.go passiveScheduleInitialDelayMS 的 result<1 分支：windowMS 被
//     half=delayMS/2 上限约束，数学上 result>=delayMS/2>=1，分支不可达。
package jobsched

import (
	"context"
	"testing"
	"time"
)

// waitForInitialSchedule 等待全部任务完成首轮排程（NextRunAt 就绪），
// 消除 fakeClock.Advance 与 jobLoop 注册 timer 的交错竞争。
func waitForInitialSchedule(t *testing.T, scheduler *Scheduler, count int) {
	t.Helper()
	waitFor(t, time.Second, func() bool {
		snapshots := scheduler.Snapshots()
		if len(snapshots) != count {
			return false
		}
		for _, snapshot := range snapshots {
			if snapshot.NextRunAt == nil {
				return false
			}
		}
		return true
	})
}

func TestW12CBackoffTargetLockedBranches(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	scheduler := NewScheduler(Options{Clock: newFakeClock(now), Random: func() float64 { return 0.5 }})
	// 未配置 Backoff：直接返回 nil。
	if got := scheduler.backoffTargetLocked(&jobState{spec: Spec{Name: "a"}}, 1, now); got != nil {
		t.Fatalf("无 Backoff 时应返回 nil: %v", got)
	}
	// Base<=0：视为未配置退避。
	job := &jobState{spec: Spec{Name: "b", Backoff: &Backoff{Base: 0, Max: time.Second}}}
	if got := scheduler.backoffTargetLocked(job, 1, now); got != nil {
		t.Fatalf("Base<=0 时应返回 nil: %v", got)
	}
	// Max<Base：封顶回落到 Base。
	job = &jobState{spec: Spec{Name: "c", Backoff: &Backoff{Base: 10 * time.Millisecond, Max: time.Millisecond}}}
	got := scheduler.backoffTargetLocked(job, 1, now)
	if got == nil || !got.After(now) || got.After(now.Add(11*time.Millisecond)) {
		t.Fatalf("Max<Base 应按 Base 封顶: %v", got)
	}
	// consecFail=0：负指数归零。
	job = &jobState{spec: Spec{Name: "d", Backoff: &Backoff{Base: 10 * time.Millisecond, Max: time.Second}}}
	got = scheduler.backoffTargetLocked(job, 0, now)
	if got == nil || !got.After(now) {
		t.Fatalf("consecFail=0 应按 0 次幂计算: %v", got)
	}
	// exponent>30 且随机比例 >=1：封顶值全量延迟。
	capped := &jobState{spec: Spec{Name: "e", Backoff: &Backoff{Base: time.Millisecond, Max: 200 * time.Millisecond}}}
	scheduler.random = func() float64 { return 1.5 }
	got = scheduler.backoffTargetLocked(capped, 40, now)
	if got == nil || !got.Equal(now.Add(200*time.Millisecond)) {
		t.Fatalf("fraction>=1 应取封顶全量: %v", got)
	}
}

func TestW12CPassiveDelayEdgeCases(t *testing.T) {
	rnd := func() float64 { return 0.5 }
	// 窗口为 0 时延迟不足 1ms 应归一为 1ms。
	if got := passiveIntervalDelay(time.Microsecond, true, rnd); got != time.Millisecond {
		t.Fatalf("亚毫秒延迟应归一为 1ms: %v", got)
	}
	// 初始延迟不足 1ms 先归一，再因窗口为 0 原样返回。
	if got := passiveScheduleInitialDelayMS(time.Microsecond, time.Second, rnd); got != time.Millisecond {
		t.Fatalf("亚毫秒初始延迟应归一为 1ms: %v", got)
	}
}

func TestW12CAdvanceFixedRateJitterFallsBackToNow(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scheduler := NewScheduler(Options{Clock: newFakeClock(now), Random: func() float64 { return 0 }})
	job := &jobState{spec: Spec{Name: "j", Interval: 2 * time.Second, PassiveJitter: true}}
	// 负偏移把候选拉回过去：锚点回退为 now+1ms。
	scheduler.advanceFixedRate(job, now.Add(-1500*time.Millisecond))
	if job.fixedRateNext == nil || !job.fixedRateNext.Equal(now.Add(time.Millisecond)) {
		t.Fatalf("候选过期时应回退为 now+1ms: %v", job.fixedRateNext)
	}
}

func TestW12CScheduleStablePhaseWindow(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	name := "w12c-stable-job"
	scheduler.Schedule(Spec{
		Name:              name,
		Interval:          time.Hour,
		InitialDelay:      time.Millisecond,
		StablePhaseWindow: 2 * time.Second,
		Task: func(context.Context, TaskContext) (TaskResult, error) {
			return TaskResult{}, nil
		},
	})
	want := stableOffsetMS(scheduler.stableSeed+":"+name, 2*time.Second)
	waitFor(t, time.Second, func() bool {
		snapshots := scheduler.Snapshots()
		return len(snapshots) == 1 && snapshots[0].StablePhaseMS == want && snapshots[0].NextRunAt != nil
	})
	settle()
	// 首轮延迟 = InitialDelay + stable 偏移；推进后应立即执行。
	clock.Advance(time.Duration(want)*time.Millisecond + time.Millisecond)
	waitFor(t, time.Second, func() bool {
		return scheduler.Snapshots()[0].RunCount == 1
	})
	scheduler.Stop()
}

func TestW12CDeferFirstRunDelaysFirstFire(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	scheduler.Schedule(Spec{
		Name:          "w12c-deferred-start",
		Interval:      50 * time.Millisecond,
		DeferFirstRun: true,
		Task: func(context.Context, TaskContext) (TaskResult, error) {
			return TaskResult{}, nil
		},
	})
	waitForInitialSchedule(t, scheduler, 1)
	if snapshots := scheduler.Snapshots(); snapshots[0].RunCount != 0 {
		t.Fatalf("DeferFirstRun 首轮不应立即执行: %+v", snapshots)
	}
	clock.Advance(50 * time.Millisecond)
	waitFor(t, time.Second, func() bool { return scheduler.Snapshots()[0].RunCount == 1 })
	scheduler.Stop()
}

func TestW12CPassiveJitterFirstDelay(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	scheduler.Schedule(Spec{
		Name:          "w12c-passive-first",
		Interval:      20 * time.Millisecond,
		InitialDelay:  10 * time.Millisecond,
		PassiveJitter: true,
		Task: func(context.Context, TaskContext) (TaskResult, error) {
			return TaskResult{}, nil
		},
	})
	waitForInitialSchedule(t, scheduler, 1)
	if snapshots := scheduler.Snapshots(); snapshots[0].RunCount != 0 {
		t.Fatalf("抖动后的首轮不应立即执行: %+v", snapshots)
	}
	// random=0.5 时初始抖动偏移归零，首轮仍按 InitialDelay 触发。
	clock.Advance(10 * time.Millisecond)
	waitFor(t, time.Second, func() bool { return scheduler.Snapshots()[0].RunCount == 1 })
	scheduler.Stop()
}

func TestW12CStopAndDrainImmediateTimeout(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	started := make(chan struct{})
	release := make(chan struct{})
	scheduler.Schedule(Spec{
		Name:         "w12c-drain-blocker",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			close(started)
			<-release
			return TaskResult{}, nil
		},
	})
	waitForInitialSchedule(t, scheduler, 1)
	clock.Advance(time.Millisecond)
	<-started
	// timeout<=0 视为立即超时：活跃任务未结束时报 drained=false 与活跃数。
	drained, active := scheduler.StopAndDrain(0)
	if drained || active != 1 {
		t.Fatalf("未排空应返回 drained=false active=1: %v %d", drained, active)
	}
	close(release)
	drained, active = scheduler.StopAndDrain(time.Second)
	if !drained || active != 0 {
		t.Fatalf("任务结束后应可排空: %v %d", drained, active)
	}
}

func TestW12CStopWakesWaitTargetLoop(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	scheduler.Schedule(Spec{
		Name:         "w12c-idle-job",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		Task: func(context.Context, TaskContext) (TaskResult, error) {
			return TaskResult{}, nil
		},
	})
	waitForInitialSchedule(t, scheduler, 1)
	clock.Advance(time.Millisecond)
	waitFor(t, time.Second, func() bool { return scheduler.Snapshots()[0].RunCount == 1 })
	// 任务空档期停机：jobLoop 在 waitTarget 阻塞中被 stopCh 唤醒并退出。
	scheduler.Stop()
	drained, _ := scheduler.StopAndDrain(time.Second)
	if !drained {
		t.Fatal("空档停机应可排空")
	}
}

func TestW12CFireOnLaneWake(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	name := "w12c-lane-wake"
	scheduler.Schedule(Spec{
		Name:         name,
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		Task: func(context.Context, TaskContext) (TaskResult, error) {
			return TaskResult{}, nil
		},
	})
	waitForInitialSchedule(t, scheduler, 1)
	clock.Advance(time.Millisecond)
	waitFor(t, time.Second, func() bool { return scheduler.Snapshots()[0].RunCount == 1 })
	// 白盒注入 lane 唤醒信号：jobLoop 应以 fireLaneWake 立即执行一轮。
	scheduler.mu.Lock()
	job := scheduler.jobs[name]
	scheduler.mu.Unlock()
	if job == nil {
		t.Fatal("任务应已登记")
	}
	select {
	case job.wake <- struct{}{}:
	default:
		t.Fatal("wake 通道应可写入")
	}
	waitFor(t, time.Second, func() bool { return scheduler.Snapshots()[0].RunCount == 2 })
	scheduler.Stop()
}

func TestW12CLaneBusyCoalesceOneQueuesJob(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	started := make(chan struct{})
	release := make(chan struct{})
	scheduler.Schedule(Spec{
		Name:         "w12c-lane-holder",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		Lane:         "w12c-lane",
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			close(started)
			<-release
			return TaskResult{}, nil
		},
	})
	scheduler.Schedule(Spec{
		Name:          "w12c-lane-waiter",
		Interval:      time.Hour,
		InitialDelay:  2 * time.Millisecond,
		Lane:          "w12c-lane",
		OverlapPolicy: OverlapCoalesceOne,
		Task: func(context.Context, TaskContext) (TaskResult, error) {
			return TaskResult{}, nil
		},
	})
	waitForInitialSchedule(t, scheduler, 2)
	clock.Advance(time.Millisecond)
	<-started
	clock.Advance(time.Millisecond)
	// coalesceOne 在 lane 忙时进入排队并标记 QueuedForLane。
	waitFor(t, time.Second, func() bool {
		for _, snapshot := range scheduler.Snapshots() {
			if snapshot.Name == "w12c-lane-waiter" && snapshot.QueuedForLane {
				return true
			}
		}
		return false
	})
	scheduler.Stop()
	close(release)
}

func TestW12CMissedIntervalCoalesceOne(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	firstFire := make(chan struct{})
	releaseCh := make(chan struct{})
	calls := 0
	scheduler.Schedule(Spec{
		Name:          "w12c-coalesce",
		Interval:      10 * time.Millisecond,
		InitialDelay:  time.Millisecond,
		OverlapPolicy: OverlapCoalesceOne,
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			calls++
			if calls == 1 {
				close(firstFire)
				<-releaseCh
			}
			return TaskResult{}, nil
		},
	})
	waitForInitialSchedule(t, scheduler, 1)
	clock.Advance(time.Millisecond)
	<-firstFire
	// 首轮运行跨过 11ms、21ms 两个锚点；结束后应合并补跑一次。
	clock.Advance(30 * time.Millisecond)
	close(releaseCh)
	waitFor(t, time.Second, func() bool {
		return scheduler.Snapshots()[0].CoalescedCount >= 1
	})
	scheduler.Stop()
}

func TestW12CMissedIntervalSkipsAndRecovers(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	firstFire := make(chan struct{})
	releaseCh := make(chan struct{})
	calls := 0
	scheduler.Schedule(Spec{
		Name:          "w12c-missed-skip",
		Interval:      10 * time.Millisecond,
		InitialDelay:  time.Millisecond,
		OverlapPolicy: OverlapSkip,
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			calls++
			if calls == 1 {
				close(firstFire)
				<-releaseCh
			}
			return TaskResult{}, nil
		},
	})
	waitForInitialSchedule(t, scheduler, 1)
	clock.Advance(time.Millisecond)
	<-firstFire
	clock.Advance(30 * time.Millisecond)
	close(releaseCh)
	waitFor(t, time.Second, func() bool {
		snapshot := scheduler.Snapshots()[0]
		return snapshot.SkippedCount >= 1 && snapshot.LastSkipReason == "running"
	})
	// 跳过过期锚点后应恢复常规调度并成功执行。
	waitFor(t, time.Second, func() bool {
		snapshot := scheduler.Snapshots()[0]
		return snapshot.SuccessCount >= 1
	})
	scheduler.Stop()
}

func TestW12CBackoffSkipsDueAnchorsThenRetries(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	attempts := 0
	scheduler.Schedule(Spec{
		Name:         "w12c-backoff-skip",
		Interval:     5 * time.Millisecond,
		InitialDelay: time.Millisecond,
		Backoff:      &Backoff{Base: 100 * time.Millisecond, Max: 200 * time.Millisecond},
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			attempts++
			if attempts == 1 {
				return TaskResult{}, context.DeadlineExceeded
			}
			return TaskResult{}, nil
		},
	})
	waitForInitialSchedule(t, scheduler, 1)
	clock.Advance(time.Millisecond)
	waitFor(t, time.Second, func() bool {
		return scheduler.Snapshots()[0].FailureCount == 1
	})
	// 退避窗口内到期的 fixedRate 锚点按 failure_backoff 跳过：
	// 31ms 处跳过 6ms 锚点；与 jobLoop 同步后再推进，46ms 处跳过 36ms 锚点
	// （退避至 51ms）。
	clock.Advance(30 * time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshot := scheduler.Snapshots()[0]
		return snapshot.SkippedCount >= 1 && snapshot.LastSkipReason == "failure_backoff"
	})
	clock.Advance(15 * time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshot := scheduler.Snapshots()[0]
		return snapshot.SkippedCount >= 2 && snapshot.LastSkipReason == "failure_backoff"
	})
	// 退避到期后 deferred 重试应成功。
	clock.Advance(80 * time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshot := scheduler.Snapshots()[0]
		return snapshot.SuccessCount >= 1 && snapshot.ConsecutiveFails == 0
	})
	scheduler.Stop()
}

func TestW12CReleaseLaneSkipsMissingAndRunningEntries(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scheduler := NewScheduler(Options{Clock: newFakeClock(now), Random: func() float64 { return 0.5 }})
	holder := &jobState{spec: Spec{Name: "w12c-holder", Lane: "w12c-rl"}, wake: make(chan struct{}, 1)}
	ghost := &jobState{spec: Spec{Name: "w12c-ghost", Lane: "w12c-rl"}, wake: make(chan struct{}, 1)}
	busy := &jobState{spec: Spec{Name: "w12c-busy", Lane: "w12c-rl"}, wake: make(chan struct{}, 1)}
	busy.running = true
	scheduler.jobs[busy.spec.Name] = busy
	scheduler.lanes["w12c-rl"] = &laneState{runningJob: holder.spec.Name, queue: []string{ghost.spec.Name, busy.spec.Name}}
	// 队首不在任务表（continue），次位仍在运行（continue）：队列清空后仅释放。
	scheduler.releaseLane(holder)
	scheduler.mu.Lock()
	running := scheduler.lanes["w12c-rl"].runningJob
	queued := len(scheduler.lanes["w12c-rl"].queue)
	scheduler.mu.Unlock()
	if running != "" || queued != 0 {
		t.Fatalf("无效与运行中的队首都应被跳过: running=%s queue=%d", running, queued)
	}
}

func TestW12CNegativeDurationClampToZero(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	calls := 0
	scheduler.Schedule(Spec{
		Name:         "w12c-clock-backwards",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		Task: func(ctx context.Context, taskCtx TaskContext) (TaskResult, error) {
			calls++
			if calls == 1 {
				// 运行中时钟回拨：执行时长按 0 记账。
				clock.Advance(-5 * time.Millisecond)
			}
			return TaskResult{}, nil
		},
	})
	waitForInitialSchedule(t, scheduler, 1)
	clock.Advance(time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshot := scheduler.Snapshots()[0]
		return snapshot.RunCount == 1 && snapshot.LastDurationMS == 0
	})
	scheduler.Stop()
}

func TestW12COutcomePartialAndSkippedBookkeeping(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	cases := []struct {
		name          string
		result        TaskResult
		wantOutcome   string
		wantDetail    string
		detailFromRes bool
	}{
		{name: "w12c-partial-plain", result: TaskResult{Outcome: OutcomePartial}, wantOutcome: string(OutcomePartial), wantDetail: "后台任务部分完成"},
		{name: "w12c-partial-warn", result: TaskResult{Outcome: OutcomePartial, Warning: "w12c 部分警告"}, wantOutcome: string(OutcomePartial), wantDetail: "w12c 部分警告", detailFromRes: true},
		{name: "w12c-skip-plain", result: TaskResult{Outcome: OutcomeSkipped}, wantOutcome: string(OutcomeSkipped), wantDetail: "task_skipped"},
		{name: "w12c-skip-reason", result: TaskResult{Outcome: OutcomeSkipped, Warning: "w12c 跳过原因"}, wantOutcome: string(OutcomeSkipped), wantDetail: "w12c 跳过原因", detailFromRes: true},
	}
	for _, tc := range cases {
		tc := tc
		scheduler.Schedule(Spec{
			Name:         tc.name,
			Interval:     time.Hour,
			InitialDelay: time.Millisecond,
			Task: func(context.Context, TaskContext) (TaskResult, error) {
				return tc.result, nil
			},
		})
	}
	waitForInitialSchedule(t, scheduler, len(cases))
	clock.Advance(time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshots := scheduler.Snapshots()
		if len(snapshots) != len(cases) {
			return false
		}
		for _, snapshot := range snapshots {
			if snapshot.RunCount != 1 {
				return false
			}
		}
		return true
	})
	for _, snapshot := range scheduler.Snapshots() {
		var tc *struct {
			name          string
			wantOutcome   string
			wantDetail    string
			detailFromRes bool
		}
		for i := range cases {
			if cases[i].name == snapshot.Name {
				tc = &struct {
					name          string
					wantOutcome   string
					wantDetail    string
					detailFromRes bool
				}{cases[i].name, cases[i].wantOutcome, cases[i].wantDetail, cases[i].detailFromRes}
			}
		}
		if tc == nil {
			t.Fatalf("未知任务快照: %s", snapshot.Name)
		}
		if tc.wantOutcome == string(OutcomePartial) {
			if snapshot.PartialCount != 1 || snapshot.LastWarning != tc.wantDetail {
				t.Fatalf("%s: partial 记账不符: %+v", snapshot.Name, snapshot)
			}
			continue
		}
		if snapshot.TaskSkippedCount != 1 || snapshot.SkippedCount != 0 || snapshot.LastSkipReason != tc.wantDetail {
			t.Fatalf("%s: skipped 记账不符: %+v", snapshot.Name, snapshot)
		}
	}
	scheduler.Stop()
}
