package retention

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeAuditShellReaper 记录 W13 兜底清扫调用参数并回放脚本化删除数。
type fakeAuditShellReaper struct {
	calls      int
	cutoffs    []time.Time
	limits     []int
	script     []int64
	err        error
	totalLimit int
}

func (r *fakeAuditShellReaper) ReapStaleInProgress(_ context.Context, cutoff time.Time, limit int) (int64, error) {
	r.calls++
	r.cutoffs = append(r.cutoffs, cutoff)
	r.limits = append(r.limits, limit)
	if r.err != nil {
		return 0, r.err
	}
	if len(r.script) > 0 {
		value := r.script[0]
		r.script = r.script[1:]
		return value, nil
	}
	return 0, nil
}

// W13 兜底（2026-10-29）：data-retention-cleanup 每轮必须以 now-2h 为 cutoff、
// CleanupBatchSize 为批大小调用空壳清扫；未注入端口时整阶段跳过（测试与
// 降级装配零依赖）。
func TestW13AuditShellStageCutsOffAtTwoHours(t *testing.T) {
	ctx := context.Background()
	reaper := &fakeAuditShellReaper{script: []int64{5}}
	job, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
		j.AuditShells = reaper
	})
	if _, err := job.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if reaper.calls != 1 {
		t.Fatalf("calls = %d, want 1", reaper.calls)
	}
	wantCutoff := fixedNow().Add(-2 * time.Hour)
	if !reaper.cutoffs[0].Equal(wantCutoff) {
		t.Fatalf("cutoff = %v, want %v", reaper.cutoffs[0], wantCutoff)
	}
	if reaper.limits[0] != CleanupBatchSize {
		t.Fatalf("limit = %d, want %d", reaper.limits[0], CleanupBatchSize)
	}
}

func TestW13AuditShellStageRunsFullBatchQuota(t *testing.T) {
	ctx := context.Background()
	reaper := &fakeAuditShellReaper{script: make([]int64, CleanupMaxBatchesPerRun)}
	for index := range reaper.script {
		reaper.script[index] = CleanupBatchSize
	}
	job, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
		j.AuditShells = reaper
	})
	if _, err := job.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if reaper.calls != CleanupMaxBatchesPerRun {
		t.Fatalf("满批应耗尽配额 %d，实际 %d", CleanupMaxBatchesPerRun, reaper.calls)
	}
}

func TestW13AuditShellStageSkippedWhenUnwired(t *testing.T) {
	ctx := context.Background()
	job, _, _, _, _, _ := w9gBaseJob(t, nil)
	if _, err := job.Run(ctx); err != nil {
		t.Fatalf("未注入端口时阶段必须跳过，err = %v", err)
	}
}

func TestW13AuditShellStagePropagatesError(t *testing.T) {
	ctx := context.Background()
	reaper := &fakeAuditShellReaper{err: errors.New("dataset down")}
	job, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
		j.AuditShells = reaper
	})
	if _, err := job.Run(ctx); err == nil || err.Error() != "dataset down" {
		t.Fatalf("err = %v", err)
	}
}

func TestW13AuditShellStageSkippedForNonWorker(t *testing.T) {
	ctx := context.Background()
	reaper := &fakeAuditShellReaper{}
	job, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
		j.ProcessRole = "server"
		j.AuditShells = reaper
	})
	if err := job.EnqueuePostgresMaintenanceJobs(ctx); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if reaper.calls != 0 {
		t.Fatalf("非 worker 角色不得触达清扫，calls = %d", reaper.calls)
	}
}
