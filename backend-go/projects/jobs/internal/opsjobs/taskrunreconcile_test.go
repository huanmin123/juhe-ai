package opsjobs

import (
	"context"
	"testing"
	"time"
)

type fakeTaskRunRepo struct {
	inputs []TaskRunReconcileInput
	result TaskRunReconcileResult
	err    error
}

func (f *fakeTaskRunRepo) ReconcileStale(_ context.Context, input TaskRunReconcileInput) (TaskRunReconcileResult, error) {
	f.inputs = append(f.inputs, input)
	if f.err != nil {
		return TaskRunReconcileResult{}, f.err
	}
	return f.result, nil
}

// 输入构造逐字段对齐 Node：staleBefore = now-10min（RFC3339）。
func TestBuildTaskRunReconcileInput(t *testing.T) {
	now := time.Date(2030, 5, 6, 12, 0, 0, 0, time.UTC).UnixMilli()
	input := BuildTaskRunReconcileInput(now, 500)
	wantStale := time.UnixMilli(now - TaskRunStaleAfterMS).UTC().Format(time.RFC3339Nano)
	if input.QueuedBefore != wantStale || input.RunningHeartbeatBefore != wantStale {
		t.Fatalf("staleBefore = %q / %q, want %q", input.QueuedBefore, input.RunningHeartbeatBefore, wantStale)
	}
	if input.Now != time.UnixMilli(now).UTC().Format(time.RFC3339Nano) {
		t.Fatalf("now = %q", input.Now)
	}
	if input.Limit != 500 {
		t.Fatalf("limit = %d", input.Limit)
	}
	// 10 分钟常量契约。
	if TaskRunStaleAfterMS != 10*60_000 {
		t.Fatalf("TaskRunStaleAfterMS = %d", TaskRunStaleAfterMS)
	}
	if TaskRunReconcileIntervalMS != 5*60_000 || TaskRunReconcileInitialDelayMS != 2_000 {
		t.Fatal("调度常量与 Node 不一致")
	}
}

// 对账修复分支：queued/running 失败计数与过期租约删除计数透传。
func TestRunTaskRunReconcilePassesThroughCounts(t *testing.T) {
	repo := &fakeTaskRunRepo{result: TaskRunReconcileResult{
		FailedQueuedCount:        2,
		FailedRunningCount:       3,
		DeletedExpiredLeaseCount: 5,
	}}
	scheduler, err := NewTaskRunReconcileScheduler(repo, 500, func() int64 {
		return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := scheduler.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.ReconciledCount() != 5 || result.DeletedExpiredLeaseCount != 5 {
		t.Fatalf("计数不符: %+v", result)
	}
	if len(repo.inputs) != 1 || repo.inputs[0].Limit != 500 {
		t.Fatalf("inputs = %+v", repo.inputs)
	}
}

// 空转：无需回收时全部计数为 0。
func TestRunTaskRunReconcileIdle(t *testing.T) {
	repo := &fakeTaskRunRepo{}
	scheduler, _ := NewTaskRunReconcileScheduler(repo, 0, func() int64 { return 1_000 }, nil)
	result, err := scheduler.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result != (TaskRunReconcileResult{}) {
		t.Fatalf("空转应返回零计数: %+v", result)
	}
	if repo.inputs[0].Limit != TaskRunReconcileBatchSize {
		t.Fatalf("默认批次 = %d", repo.inputs[0].Limit)
	}
}
