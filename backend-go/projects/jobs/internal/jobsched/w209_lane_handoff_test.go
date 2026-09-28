package jobsched

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// BUG-0209 回归：releaseLane 交接把 runningJob 置为队首任务名并唤醒它，被唤
// 醒任务的 fire→acquireLane 必须认领自己的交接名。缺陷形态：acquireLane 只
// 认空 lane，交接后任务把自己重新排队且永不释放——lane 自死锁，同 lane 全部
// 任务永久 resource_lane_busy（2026-09-28 生产：external-account-maintenance
// 与 stats-online 双双在 jobs 重启后首轮交接即冻结，余额探测补偿/用量统计聚
// 合各只跑一轮）。
func TestSchedulerLaneHandoffRunsQueuedJob(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 28, 8, 30, 0, 0, time.UTC))
	scheduler := newTestScheduler(clock)
	var holderRuns atomic.Int64
	startedHolder := make(chan struct{}, 1)
	releaseHolder := make(chan struct{})
	scheduler.Schedule(Spec{
		Name:         "handoff-holder",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		Lane:         "handoff-lane",
		Task: func(context.Context, TaskContext) (TaskResult, error) {
			if holderRuns.Add(1) == 1 {
				select {
				case startedHolder <- struct{}{}:
				default:
				}
				<-releaseHolder
			}
			return TaskResult{}, nil
		},
	})
	scheduler.Schedule(Spec{
		Name:          "handoff-follower",
		Interval:      time.Hour,
		InitialDelay:  5 * time.Millisecond,
		OverlapPolicy: OverlapCoalesceOne,
		Lane:          "handoff-lane",
		Task: func(context.Context, TaskContext) (TaskResult, error) {
			return TaskResult{}, nil
		},
	})
	settle()
	// holder 先到：占住 lane 并阻塞在任务里。
	clock.Advance(time.Millisecond)
	<-startedHolder
	// follower 到期：lane 忙 → 进入交接队列。
	clock.Advance(4 * time.Millisecond)
	waitFor(t, time.Second, func() bool {
		snapshot, ok := snapshotByName(scheduler, "handoff-follower")
		return ok && snapshot.QueuedForLane
	})
	// holder 完成：releaseLane 交接 → follower 被唤醒并必须真正执行一轮
	// （缺陷形态：唤醒后自认 lane 忙、重新排队、RunCount 恒 0）。
	close(releaseHolder)
	waitFor(t, time.Second, func() bool {
		snapshot, ok := snapshotByName(scheduler, "handoff-follower")
		return ok && snapshot.SuccessCount == 1
	})
	// follower 完成后 lane 必须已释放（交接链不断：follower 不再排队）。
	// LastSkipReason 保留入队时的历史 busy 记录属正常快照语义，不在此断言。
	snapshot, ok := snapshotByName(scheduler, "handoff-follower")
	if !ok || snapshot.QueuedForLane {
		t.Fatalf("交接执行后 follower 不得残留排队状态: %+v", snapshot)
	}
	if drained, active := scheduler.StopAndDrain(time.Second); !drained || active != 0 {
		t.Fatalf("expected clean drain, got drained=%v active=%d", drained, active)
	}
}
