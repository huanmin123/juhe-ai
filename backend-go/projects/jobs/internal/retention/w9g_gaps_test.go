package retention

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// 本文件补齐 retention 域既有测试未覆盖的错误臂、守卫分支与 fail-closed
// stub。命名统一 w9g 前缀，避免与既有用例混淆。

// ---------- 通用小工具 ----------

// w9gCancelSleeper 在首次调用时取消 ctx 但返回 nil，驱动各批次循环
// "上一批 sleep 之后、下一批循环顶部" 的中止检查。
func w9gCancelSleeper(cancel context.CancelFunc) Sleeper {
	var called bool
	return func(context.Context) error {
		if !called {
			called = true
			cancel()
		}
		return nil
	}
}

// w9gCancelAfterPublic 在 CleanupBefore 返回前取消 ctx（返回值不受影响），
// 驱动 yieldToEventLoop 的中止检查。
type w9gCancelAfterPublic struct {
	inner  *fakePublicApiLogs
	cancel context.CancelFunc
}

func (c *w9gCancelAfterPublic) CleanupBefore(ctx context.Context, cutoff string, limit int) (int64, error) {
	deleted, err := c.inner.CleanupBefore(ctx, cutoff, limit)
	c.cancel()
	return deleted, err
}

// w9gAsyncCancelPublic 在首次清理调用时异步（2ms 后）取消 ctx，保证取消
// 落在真实 25ms 批间暂停窗口内而不是同步 yield 检查里。
type w9gAsyncCancelPublic struct {
	inner     *fakePublicApiLogs
	cancel    context.CancelFunc
	scheduled bool
}

func (c *w9gAsyncCancelPublic) CleanupBefore(ctx context.Context, cutoff string, limit int) (int64, error) {
	if !c.scheduled {
		c.scheduled = true
		time.AfterFunc(2*time.Millisecond, c.cancel)
	}
	return c.inner.CleanupBefore(ctx, cutoff, limit)
}

// w9gCancelAfterUsage 在 CleanupProcessedBefore 返回前取消 ctx。
type w9gCancelAfterUsage struct {
	inner  *fakeUsageRecords
	cancel context.CancelFunc
}

func (c *w9gCancelAfterUsage) CleanupProcessedBefore(ctx context.Context, cutoff string, limit int) (UsageRecordsBatch, error) {
	batch, err := c.inner.CleanupProcessedBefore(ctx, cutoff, limit)
	c.cancel()
	return batch, err
}

// w9gCancelAfterStats 在 CleanupUsageStatsRetention 返回前取消 ctx。
type w9gCancelAfterStats struct {
	*fakeStatsWriter
	cancel context.CancelFunc
}

func (c *w9gCancelAfterStats) CleanupUsageStatsRetention(ctx context.Context, input UsageStatsRetentionInput) (UsageStatsRetentionCounts, error) {
	counts, err := c.fakeStatsWriter.CleanupUsageStatsRetention(ctx, input)
	c.cancel()
	return counts, err
}

// w9gCancelAfterMetrics 在 CleanupSystemMetricsRetention 返回前取消 ctx。
type w9gCancelAfterMetrics struct {
	*fakeStatsWriter
	cancel context.CancelFunc
}

func (c *w9gCancelAfterMetrics) CleanupSystemMetricsRetention(ctx context.Context, input SystemMetricsRetentionInput) (SystemMetricsRetentionCounts, error) {
	counts, err := c.fakeStatsWriter.CleanupSystemMetricsRetention(ctx, input)
	c.cancel()
	return counts, err
}

// w9gCancelAfterCodexStorage 在 ProcessBatch 返回前取消 ctx。
type w9gCancelAfterCodexStorage struct {
	inner  *fakeCodexStorage
	cancel context.CancelFunc
}

func (c *w9gCancelAfterCodexStorage) ProcessBatch(ctx context.Context, keys []string) (int64, error) {
	deleted, err := c.inner.ProcessBatch(ctx, keys)
	c.cancel()
	return deleted, err
}

// w9gFailingStats 只让指定方法失败，其余委托 fakeStatsWriter。
type w9gFailingStats struct {
	*fakeStatsWriter
	usageErr   error
	metricsErr error
	upsertErr  error
	nonBizErr  error
}

func (f *w9gFailingStats) CleanupUsageStatsRetention(ctx context.Context, input UsageStatsRetentionInput) (UsageStatsRetentionCounts, error) {
	if f.usageErr != nil {
		return UsageStatsRetentionCounts{}, f.usageErr
	}
	return f.fakeStatsWriter.CleanupUsageStatsRetention(ctx, input)
}

func (f *w9gFailingStats) CleanupSystemMetricsRetention(ctx context.Context, input SystemMetricsRetentionInput) (SystemMetricsRetentionCounts, error) {
	if f.metricsErr != nil {
		return SystemMetricsRetentionCounts{}, f.metricsErr
	}
	return f.fakeStatsWriter.CleanupSystemMetricsRetention(ctx, input)
}

func (f *w9gFailingStats) UpsertAccountUsageSnapshots(ctx context.Context, inputs []AccountUsageSnapshotUpsertInput) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	return f.fakeStatsWriter.UpsertAccountUsageSnapshots(ctx, inputs)
}

func (f *w9gFailingStats) CleanupNonBusinessStatsData(ctx context.Context, cutoffAt string, limit int) (NonBusinessDataCleanupCounts, error) {
	if f.nonBizErr != nil {
		return NonBusinessDataCleanupCounts{}, f.nonBizErr
	}
	return f.fakeStatsWriter.CleanupNonBusinessStatsData(ctx, cutoffAt, limit)
}

// w9gBaseJob 返回一个可直接 mutate 的 ingest-worker 作业（全空脚本）。
func w9gBaseJob(t *testing.T, mutate func(j *DataRetentionJob)) (*DataRetentionJob, *fakePublicApiLogs, *fakeUsageRecords, *fakeStatsWriter, *fakeDbService, *fakeCodexStorage) {
	t.Helper()
	public := &fakePublicApiLogs{script: []int64{0}}
	usage := &fakeUsageRecords{script: []UsageRecordsBatch{{}}}
	stats := &fakeStatsWriter{}
	db := &fakeDbService{}
	codex := &fakeCodexStorage{}
	job := NewDataRetentionJob(ModeSQLite, "worker", "ingest-worker")
	job.Settings = func(context.Context) (map[string]any, error) { return validPolicySettings(), nil }
	job.Timezone = func(context.Context) (string, error) { return "UTC", nil }
	job.Clock = fixedClock(fixedNow())
	job.Sleep = (&countingSleeper{}).sleep
	job.PublicApiLogs = public
	job.UsageRecords = usage
	job.Stats = stats
	job.DB = db
	job.CodexStorage = codex
	job.Enqueuer = &fakeEnqueuer{}
	if mutate != nil {
		mutate(job)
	}
	return job, public, usage, stats, db, codex
}

// ---------- fail-closed stub 与回退访问器（直测） ----------

func TestW9GMissingPortsFailClosed(t *testing.T) {
	ctx := context.Background()
	job := &DataRetentionJob{}

	if _, err := job.stats().CleanupUsageStatsRetention(ctx, UsageStatsRetentionInput{}); err == nil {
		t.Fatal("missingStatsWriter.CleanupUsageStatsRetention 必须 fail-closed")
	}
	if _, err := job.stats().CleanupSystemMetricsRetention(ctx, SystemMetricsRetentionInput{}); err == nil {
		t.Fatal("missingStatsWriter.CleanupSystemMetricsRetention 必须 fail-closed")
	}
	if _, err := job.stats().CleanupNonBusinessStatsData(ctx, "c", 1); err == nil {
		t.Fatal("missingStatsWriter.CleanupNonBusinessStatsData 必须 fail-closed")
	}
	if err := job.stats().CleanupDeletedApiKeyRecordStats(ctx, DeletedApiKeyRecordStatsCleanupInput{}); err == nil {
		t.Fatal("missingStatsWriter.CleanupDeletedApiKeyRecordStats 必须 fail-closed")
	}
	if err := job.stats().CleanupDeletedAccountRecordStats(ctx, DeletedAccountRecordStatsCleanupInput{}); err == nil {
		t.Fatal("missingStatsWriter.CleanupDeletedAccountRecordStats 必须 fail-closed")
	}
	if err := job.stats().UpsertAccountUsageSnapshots(ctx, nil); err == nil {
		t.Fatal("missingStatsWriter.UpsertAccountUsageSnapshots 必须 fail-closed")
	}
	if _, err := job.publicApiLogs().CleanupBefore(ctx, "c", 1); err == nil {
		t.Fatal("missingPublicApiLogs.CleanupBefore 必须 fail-closed")
	}
	if _, err := job.usageRecords().CleanupProcessedBefore(ctx, "c", 1); err == nil {
		t.Fatal("missingUsageRecords.CleanupProcessedBefore 必须 fail-closed")
	}
	if _, err := job.db().CleanupChatRetention(ctx, ChatRetentionInput{}); err == nil {
		t.Fatal("missingDbService.CleanupChatRetention 必须 fail-closed")
	}
	if _, err := job.db().CleanupExpiredSystemSessions(ctx, "c", 1); err == nil {
		t.Fatal("missingDbService.CleanupExpiredSystemSessions 必须 fail-closed")
	}
	if _, err := job.db().CleanupExpiredCodexContextStates(ctx, "c", 1); err == nil {
		t.Fatal("missingDbService.CleanupExpiredCodexContextStates 必须 fail-closed")
	}
	if _, err := job.db().SettleCodexContextStorageCleanup(ctx, CodexContextSettlement{}); err == nil {
		t.Fatal("missingDbService.SettleCodexContextStorageCleanup 必须 fail-closed")
	}
	if _, err := job.db().CleanupExpiredDeletedAccounts(ctx); err == nil {
		t.Fatal("missingDbService.CleanupExpiredDeletedAccounts 必须 fail-closed")
	}
	if _, err := job.codexStorage().ProcessBatch(ctx, nil); err == nil {
		t.Fatal("missingCodexStorage.ProcessBatch 必须 fail-closed")
	}
}

func TestW9GMissingRunnerAndRetryerStubs(t *testing.T) {
	ctx := context.Background()
	runner := &RecordMaintenanceRunner{}
	if _, err := runner.relatedRecords().CleanupApiKeyRelated(ctx, RecordMaintenanceJob{}, nil); err == nil {
		t.Fatal("missingRelatedRecords.CleanupApiKeyRelated 必须 fail-closed")
	}
	if _, err := runner.relatedRecords().CleanupAccountRelated(ctx, RecordMaintenanceJob{}, nil); err == nil {
		t.Fatal("missingRelatedRecords.CleanupAccountRelated 必须 fail-closed")
	}
	if _, err := runner.nonBusinessData().CleanupBefore(ctx, "c", 1); err == nil {
		t.Fatal("missingNonBusinessData.CleanupBefore 必须 fail-closed")
	}

	retry := &RecordCleanupRetryJob{}
	if _, err := retry.apiKey().CleanupPendingTargets(ctx, 1, nil); err == nil {
		t.Fatal("missingAPIKeyRetryer 必须 fail-closed")
	}
	if _, err := retry.account().CleanupPendingTargets(ctx, 1, nil); err == nil {
		t.Fatal("missingAccountRetryer 必须 fail-closed")
	}

	expired := &ExpiredDeletedAccountJob{}
	if result := expired.enqueuer().Enqueue(ctx, RecordMaintenanceJob{}); result.Queued {
		t.Fatal("missingEnqueuer.Enqueue 不得声称已入队")
	}
	if err := expired.enqueuer().EnqueueAsync(ctx, RecordMaintenanceJob{}); err == nil {
		t.Fatal("missingEnqueuer.EnqueueAsync 必须 fail-closed")
	}
}

// ---------- 入口守卫 ----------

func TestW9GDataRetentionEntrypointGuards(t *testing.T) {
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	job, _, _, _, _, _ := w9gBaseJob(t, nil)
	if _, err := job.Run(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run(cancelled) err = %v, want context.Canceled", err)
	}

	job2, _, _, _, _, _ := w9gBaseJob(t, nil)
	if _, err := job2.CleanupExpiredRetainedData(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("CleanupExpiredRetainedData(cancelled) err = %v", err)
	}

	// begin/postgres 互斥：已持有时第二入口直接空返回（并经 defer 复位）。
	job3, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
		j.Mode = ModePostgres
	})
	if !job3.begin("postgres") {
		t.Fatal("首次 begin(postgres) 必须成功")
	}
	if err := job3.EnqueuePostgresMaintenanceJobs(ctx); err != nil {
		t.Fatalf("并发第二入口应空返回 nil，got %v", err)
	}
	job3.end("postgres")

	// 默认 clock/sleep（真实 25ms 节拍）走满批。
	job4, public4, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
		j.Clock = nil
		j.Sleep = nil
	})
	public4.script = []int64{CleanupBatchSize, 0}
	if _, err := job4.CleanupExpiredRetainedData(ctx); err != nil {
		t.Fatalf("默认时钟/睡眠运行失败: %v", err)
	}

	// 已取消 ctx + 默认 sleep：取消在首批清理的 25ms 真实暂停窗口内触发，
	// pauseCleanupBatch 的 select 取消臂被命中。
	job5, public5, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
		j.Sleep = nil
	})
	cancelCtx5, stop := context.WithCancel(ctx)
	defer stop()
	public5.script = []int64{CleanupBatchSize}
	job5.PublicApiLogs = &w9gAsyncCancelPublic{inner: public5, cancel: stop}
	if _, err := job5.CleanupExpiredRetainedData(cancelCtx5); !errors.Is(err, context.Canceled) {
		t.Fatalf("暂停窗口内取消应返回 context.Canceled, got %v", err)
	}
}

// ---------- 设置与时区 fail-closed ----------

func TestW9GSettingsAndTimezoneGuards(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		mutate  func(j *DataRetentionJob)
		wantErr string
	}{
		{
			name:    "settings source 未初始化",
			mutate:  func(j *DataRetentionJob) { j.Settings = nil },
			wantErr: "retention settings source 未初始化",
		},
		{
			name: "settings 返回 nil map",
			mutate: func(j *DataRetentionJob) {
				j.Settings = func(context.Context) (map[string]any, error) { return nil, nil }
			},
			wantErr: "retention settings source 未初始化",
		},
		{
			name:    "timezone source 未初始化",
			mutate:  func(j *DataRetentionJob) { j.Timezone = nil },
			wantErr: "系统设置缺少 usageStatsTimezone",
		},
		{
			name: "timezone source 失败",
			mutate: func(j *DataRetentionJob) {
				j.Timezone = func(context.Context) (string, error) { return "", errors.New("settings down") }
			},
			wantErr: "settings down",
		},
		{
			name: "timezone 为空字符串",
			mutate: func(j *DataRetentionJob) {
				j.Timezone = func(context.Context) (string, error) { return "", nil }
			},
			wantErr: "系统设置缺少 usageStatsTimezone",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			job, public, _, _, _, _ := w9gBaseJob(t, tt.mutate)
			_, err := job.CleanupExpiredRetainedData(ctx)
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
			if public.calls != 0 {
				t.Fatal("fail-closed 前不得触碰存储")
			}
		})
	}

	// 时区非空但不可解析：statsLocation 阶段才失败（dataset/usage 已跑完）。
	job, public, usage, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
		j.Timezone = func(context.Context) (string, error) { return "Mars/Phobos", nil }
	})
	if _, err := job.CleanupExpiredRetainedData(ctx); err == nil || !strings.Contains(err.Error(), "统计时区不存在") {
		t.Fatalf("err = %v, want 统计时区不存在", err)
	}
	if public.calls != 1 || usage.calls != 1 {
		t.Fatalf("dataset 阶段应已完成: public=%d usage=%d", public.calls, usage.calls)
	}
}

// ---------- SQLite 链路阶段错误臂 ----------

func TestW9GSQLiteStageFailures(t *testing.T) {
	ctx := context.Background()
	usageErr := errors.New("usage writer down")
	publicErr := errors.New("logs store down")
	sessionsErr := errors.New("sessions db down")

	t.Run("publicApiLogs 清理失败上抛", func(t *testing.T) {
		job, public, _, _, _, _ := w9gBaseJob(t, nil)
		public.err = publicErr
		_, err := job.CleanupExpiredRetainedData(ctx)
		if err == nil || err.Error() != "logs store down" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("usageRecords 清理失败上抛", func(t *testing.T) {
		job, _, usage, _, _, _ := w9gBaseJob(t, nil)
		usage.err = usageErr
		_, err := job.CleanupExpiredRetainedData(ctx)
		if err == nil || err.Error() != "usage writer down" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("usage stats 清理失败上抛", func(t *testing.T) {
		stats := &fakeStatsWriter{}
		job, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
			j.Stats = &w9gFailingStats{fakeStatsWriter: stats, usageErr: errors.New("stats down")}
		})
		_, err := job.CleanupExpiredRetainedData(ctx)
		if err == nil || err.Error() != "stats down" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("system metrics 清理失败上抛", func(t *testing.T) {
		stats := &fakeStatsWriter{}
		job, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
			j.Stats = &w9gFailingStats{fakeStatsWriter: stats, metricsErr: errors.New("metrics down")}
		})
		_, err := job.CleanupExpiredRetainedData(ctx)
		if err == nil || err.Error() != "metrics down" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("system sessions 清理失败上抛", func(t *testing.T) {
		job, _, _, _, db, _ := w9gBaseJob(t, nil)
		db.sessionsErr = sessionsErr
		_, err := job.CleanupExpiredRetainedData(ctx)
		if err == nil || err.Error() != "sessions db down" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("codex 状态清理失败上抛", func(t *testing.T) {
		job, _, _, _, db, _ := w9gBaseJob(t, nil)
		db.codexErr = errors.New("codex db down")
		_, err := job.CleanupExpiredRetainedData(ctx)
		if err == nil || err.Error() != "codex db down" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("codex 状态 nil 结果提前收束", func(t *testing.T) {
		job, _, _, _, db, codex := w9gBaseJob(t, nil)
		db.codexScript = []*CodexContextExpiredCleanup{nil}
		if _, err := job.CleanupExpiredRetainedData(ctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if codex.calls != 0 {
			t.Fatal("nil 清理结果不得触达存储处理器")
		}
	})

	t.Run("codex 存储处理失败上抛", func(t *testing.T) {
		job, _, _, _, db, codex := w9gBaseJob(t, nil)
		db.codexScript = []*CodexContextExpiredCleanup{{DeletedSessions: 1, StorageKeys: []string{"k"}}}
		codex.processErr = errors.New("storage down")
		_, err := job.CleanupExpiredRetainedData(ctx)
		if err == nil || err.Error() != "storage down" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("未初始化 stats 存储走 missing stub", func(t *testing.T) {
		job, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) { j.Stats = nil })
		_, err := job.CleanupExpiredRetainedData(ctx)
		if err == nil || !strings.Contains(err.Error(), "retention stats writer 未初始化") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("未初始化 DB 走 missing stub", func(t *testing.T) {
		job, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) { j.DB = nil })
		_, err := job.CleanupExpiredRetainedData(ctx)
		if err == nil || !strings.Contains(err.Error(), "retention db service 未初始化") {
			t.Fatalf("err = %v", err)
		}
	})
}

// ---------- SQLite 批次循环的取消/睡眠节拍 ----------

func TestW9GBatchLoopAbortAndRhythm(t *testing.T) {
	ctx := context.Background()

	t.Run("sleep 中取消在循环顶部捕获（publicApiLogs）", func(t *testing.T) {
		job, public, _, _, _, _ := w9gBaseJob(t, nil)
		public.script = []int64{CleanupBatchSize}
		cancelCtx, cancel := context.WithCancel(ctx)
		job.Sleep = w9gCancelSleeper(cancel)
		_, err := job.CleanupExpiredRetainedData(cancelCtx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if public.calls != 1 {
			t.Fatalf("calls = %d, want 1（第二批前中止）", public.calls)
		}
	})

	t.Run("清理调用期间取消在 yield 捕获（publicApiLogs）", func(t *testing.T) {
		job, public, _, _, _, _ := w9gBaseJob(t, nil)
		cancelCtx, cancel := context.WithCancel(ctx)
		job.PublicApiLogs = &w9gCancelAfterPublic{inner: public, cancel: cancel}
		_, err := job.CleanupExpiredRetainedData(cancelCtx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("sleep 中取消在循环顶部捕获（usageRecords）", func(t *testing.T) {
		job, _, usage, _, _, _ := w9gBaseJob(t, nil)
		usage.script = []UsageRecordsBatch{{DeletedRows: CleanupBatchSize, HasMore: true}}
		cancelCtx, cancel := context.WithCancel(ctx)
		job.Sleep = w9gCancelSleeper(cancel)
		_, err := job.CleanupExpiredRetainedData(cancelCtx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if usage.calls != 1 {
			t.Fatalf("usage calls = %d, want 1", usage.calls)
		}
	})

	t.Run("清理调用期间取消在 yield 捕获（usageRecords）", func(t *testing.T) {
		job, _, usage, _, _, _ := w9gBaseJob(t, nil)
		cancelCtx, cancel := context.WithCancel(ctx)
		job.UsageRecords = &w9gCancelAfterUsage{inner: usage, cancel: cancel}
		_, err := job.CleanupExpiredRetainedData(cancelCtx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("usage 记录满批耗尽 maxBatches 配额", func(t *testing.T) {
		job, _, usage, _, _, _ := w9gBaseJob(t, nil)
		usage.script = []UsageRecordsBatch{{DeletedRows: CleanupBatchSize, HasMore: true}}
		result, err := job.CleanupExpiredRetainedData(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if usage.calls != CleanupMaxBatchesPerRun {
			t.Fatalf("usage calls = %d, want %d", usage.calls, CleanupMaxBatchesPerRun)
		}
		if result.UsageRecords != CleanupBatchSize*int64(CleanupMaxBatchesPerRun) {
			t.Fatalf("usageRecords = %d", result.UsageRecords)
		}
	})

	t.Run("stats 满批进入睡眠后失败上抛", func(t *testing.T) {
		sleeper := &countingSleeper{err: errors.New("pause aborted")}
		job, _, _, stats, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) { j.Sleep = sleeper.sleep })
		stats.usageScript = []UsageStatsRetentionCounts{{UsageStatsMinute: CleanupBatchSize}}
		_, err := job.CleanupExpiredRetainedData(ctx)
		if err == nil || err.Error() != "pause aborted" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("metrics 满批进入睡眠后失败上抛", func(t *testing.T) {
		sleeper := &countingSleeper{err: errors.New("pause aborted")}
		job, _, _, stats, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) { j.Sleep = sleeper.sleep })
		stats.metricsScript = []SystemMetricsRetentionCounts{{SystemMetricsSamples: CleanupBatchSize}}
		_, err := job.CleanupExpiredRetainedData(ctx)
		if err == nil || err.Error() != "pause aborted" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("codex 满批续跑进入睡眠后失败上抛", func(t *testing.T) {
		sleeper := &countingSleeper{err: errors.New("pause aborted")}
		job, _, _, _, db, _ := w9gBaseJob(t, func(j *DataRetentionJob) { j.Sleep = sleeper.sleep })
		db.sessionsScript = []int64{0}
		db.codexScript = []*CodexContextExpiredCleanup{{DeletedSessions: CleanupBatchSize, HasMore: true}}
		_, err := job.CleanupExpiredRetainedData(ctx)
		if err == nil || err.Error() != "pause aborted" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("stats 满批耗尽配额", func(t *testing.T) {
		job, _, _, stats, _, _ := w9gBaseJob(t, nil)
		stats.usageScript = make([]UsageStatsRetentionCounts, CleanupMaxBatchesPerRun)
		for index := range stats.usageScript {
			stats.usageScript[index] = UsageStatsRetentionCounts{UsageStatsMinute: 1}
		}
		result, err := job.CleanupExpiredRetainedData(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats.usageCalls != CleanupMaxBatchesPerRun {
			t.Fatalf("stats calls = %d, want %d", stats.usageCalls, CleanupMaxBatchesPerRun)
		}
		if result.UsageStatsMinute != int64(CleanupMaxBatchesPerRun) {
			t.Fatalf("minute counter = %d", result.UsageStatsMinute)
		}
	})

	t.Run("metrics 满批耗尽配额", func(t *testing.T) {
		job, _, _, stats, _, _ := w9gBaseJob(t, nil)
		stats.metricsScript = make([]SystemMetricsRetentionCounts, CleanupMaxBatchesPerRun)
		for index := range stats.metricsScript {
			stats.metricsScript[index] = SystemMetricsRetentionCounts{SystemMetricsSamples: 1}
		}
		result, err := job.CleanupExpiredRetainedData(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats.metricsCalls != CleanupMaxBatchesPerRun {
			t.Fatalf("metrics calls = %d, want %d", stats.metricsCalls, CleanupMaxBatchesPerRun)
		}
		if result.SystemMetricsSamples != int64(CleanupMaxBatchesPerRun) {
			t.Fatalf("samples counter = %d", result.SystemMetricsSamples)
		}
	})

	t.Run("codex 满批耗尽配额", func(t *testing.T) {
		job, _, _, _, db, codex := w9gBaseJob(t, nil)
		db.sessionsScript = []int64{0}
		db.codexScript = make([]*CodexContextExpiredCleanup, CleanupMaxBatchesPerRun)
		for index := range db.codexScript {
			db.codexScript[index] = &CodexContextExpiredCleanup{DeletedSessions: CleanupBatchSize, StorageKeys: []string{"k"}, HasMore: true}
		}
		if _, err := job.CleanupExpiredRetainedData(ctx); err != nil {
			t.Fatal(err)
		}
		if db.codexCalls != CleanupMaxBatchesPerRun || codex.calls != CleanupMaxBatchesPerRun {
			t.Fatalf("codex db=%d storage=%d, want %d", db.codexCalls, codex.calls, CleanupMaxBatchesPerRun)
		}
	})

	t.Run("usage 记录满批后睡眠失败上抛", func(t *testing.T) {
		sleeper := &countingSleeper{err: errors.New("pause aborted")}
		job, _, usage, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) { j.Sleep = sleeper.sleep })
		usage.script = []UsageRecordsBatch{{DeletedRows: CleanupBatchSize, HasMore: true}}
		_, err := job.CleanupExpiredRetainedData(ctx)
		if err == nil || err.Error() != "pause aborted" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("stats 调用期间取消在 yield 捕获", func(t *testing.T) {
		job, _, _, stats, _, _ := w9gBaseJob(t, nil)
		cancelCtx, cancel := context.WithCancel(ctx)
		job.Stats = &w9gCancelAfterStats{fakeStatsWriter: stats, cancel: cancel}
		_, err := job.CleanupExpiredRetainedData(cancelCtx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("stats 满批后取消在循环顶部捕获", func(t *testing.T) {
		job, _, _, stats, _, _ := w9gBaseJob(t, nil)
		cancelCtx, cancel := context.WithCancel(ctx)
		job.Sleep = w9gCancelSleeper(cancel)
		stats.usageScript = []UsageStatsRetentionCounts{{UsageStatsMinute: 1}}
		_, err := job.CleanupExpiredRetainedData(cancelCtx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if stats.usageCalls != 1 {
			t.Fatalf("stats calls = %d, want 1", stats.usageCalls)
		}
	})

	t.Run("metrics 调用期间取消在 yield 捕获", func(t *testing.T) {
		job, _, _, stats, _, _ := w9gBaseJob(t, nil)
		cancelCtx, cancel := context.WithCancel(ctx)
		job.Stats = &w9gCancelAfterMetrics{fakeStatsWriter: stats, cancel: cancel}
		_, err := job.CleanupExpiredRetainedData(cancelCtx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("metrics 满批后取消在循环顶部捕获", func(t *testing.T) {
		job, _, _, stats, _, _ := w9gBaseJob(t, nil)
		cancelCtx, cancel := context.WithCancel(ctx)
		job.Sleep = w9gCancelSleeper(cancel)
		stats.metricsScript = []SystemMetricsRetentionCounts{{SystemMetricsSamples: 1}}
		_, err := job.CleanupExpiredRetainedData(cancelCtx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if stats.metricsCalls != 1 {
			t.Fatalf("metrics calls = %d, want 1", stats.metricsCalls)
		}
	})

	t.Run("codex 满批后取消在循环顶部捕获", func(t *testing.T) {
		job, _, _, _, db, codex := w9gBaseJob(t, nil)
		cancelCtx, cancel := context.WithCancel(ctx)
		job.Sleep = w9gCancelSleeper(cancel)
		db.sessionsScript = []int64{0}
		db.codexScript = []*CodexContextExpiredCleanup{{DeletedSessions: CleanupBatchSize, HasMore: true}}
		_, err := job.CleanupExpiredRetainedData(cancelCtx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if db.codexCalls != 1 {
			t.Fatalf("codex calls = %d, want 1", db.codexCalls)
		}
		_ = codex
	})

	t.Run("codex 存储调用期间取消在 yield 捕获", func(t *testing.T) {
		job, _, _, _, db, codex := w9gBaseJob(t, nil)
		cancelCtx, cancel := context.WithCancel(ctx)
		job.CodexStorage = &w9gCancelAfterCodexStorage{inner: codex, cancel: cancel}
		db.codexScript = []*CodexContextExpiredCleanup{{DeletedSessions: 1, StorageKeys: []string{"k"}}}
		_, err := job.CleanupExpiredRetainedData(cancelCtx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
}

// ---------- PostgreSQL 高性能投递链路 ----------

func TestW9GPostgresStageFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("settings 失败上抛", func(t *testing.T) {
		job, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
			j.Mode = ModePostgres
			j.Settings = func(context.Context) (map[string]any, error) { return nil, errors.New("settings down") }
		})
		if _, err := job.Run(ctx); err == nil || err.Error() != "settings down" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("timezone 失败上抛", func(t *testing.T) {
		job, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
			j.Mode = ModePostgres
			j.Timezone = func(context.Context) (string, error) { return "", errors.New("tz down") }
		})
		if _, err := job.Run(ctx); err == nil || err.Error() != "tz down" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("policy 非法上抛", func(t *testing.T) {
		job, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
			j.Mode = ModePostgres
			j.Settings = func(context.Context) (map[string]any, error) {
				settings := validPolicySettings()
				settings[SettingPublicApiLogRetentionDays] = 0
				return settings, nil
			}
		})
		if _, err := job.Run(ctx); err == nil || !strings.Contains(err.Error(), "publicApiLogRetentionDays") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("enqueuer 未初始化上抛", func(t *testing.T) {
		job, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
			j.Mode = ModePostgres
			j.Enqueuer = nil
		})
		if _, err := job.Run(ctx); err == nil || !strings.Contains(err.Error(), "enqueuer 未初始化") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("publicApiLogs 清理失败上抛", func(t *testing.T) {
		job, public, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) { j.Mode = ModePostgres })
		public.err = errors.New("pg logs down")
		if _, err := job.Run(ctx); err == nil || err.Error() != "pg logs down" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("timezone 不可解析上抛", func(t *testing.T) {
		job, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
			j.Mode = ModePostgres
			j.Timezone = func(context.Context) (string, error) { return "Mars/Phobos", nil }
		})
		if _, err := job.Run(ctx); err == nil || !strings.Contains(err.Error(), "统计时区不存在") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("usage stats 清理失败上抛", func(t *testing.T) {
		stats := &fakeStatsWriter{}
		job, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
			j.Mode = ModePostgres
			j.Stats = &w9gFailingStats{fakeStatsWriter: stats, usageErr: errors.New("pg stats down")}
		})
		if _, err := job.Run(ctx); err == nil || err.Error() != "pg stats down" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("metrics 清理失败上抛", func(t *testing.T) {
		stats := &fakeStatsWriter{}
		job, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
			j.Mode = ModePostgres
			j.Stats = &w9gFailingStats{fakeStatsWriter: stats, metricsErr: errors.New("pg metrics down")}
		})
		if _, err := job.Run(ctx); err == nil || err.Error() != "pg metrics down" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("sessions 清理失败上抛", func(t *testing.T) {
		job, _, _, _, db, _ := w9gBaseJob(t, func(j *DataRetentionJob) { j.Mode = ModePostgres })
		db.sessionsErr = errors.New("pg sessions down")
		if _, err := job.Run(ctx); err == nil || err.Error() != "pg sessions down" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("codex 状态清理失败上抛", func(t *testing.T) {
		job, _, _, _, db, _ := w9gBaseJob(t, func(j *DataRetentionJob) { j.Mode = ModePostgres })
		db.codexErr = errors.New("pg codex down")
		if _, err := job.Run(ctx); err == nil || err.Error() != "pg codex down" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("codex nil 清理结果返回 0", func(t *testing.T) {
		job, _, _, _, db, codex := w9gBaseJob(t, func(j *DataRetentionJob) { j.Mode = ModePostgres })
		db.codexScript = []*CodexContextExpiredCleanup{nil}
		if _, err := job.Run(ctx); err != nil {
			t.Fatal(err)
		}
		if codex.calls != 0 {
			t.Fatal("nil 清理结果不得触达存储处理器")
		}
	})

	t.Run("codex 存储处理失败上抛", func(t *testing.T) {
		job, _, _, _, db, codex := w9gBaseJob(t, func(j *DataRetentionJob) { j.Mode = ModePostgres })
		db.codexScript = []*CodexContextExpiredCleanup{{DeletedSessions: 1, StorageKeys: []string{"k"}}}
		codex.processErr = errors.New("pg storage down")
		if _, err := job.Run(ctx); err == nil || err.Error() != "pg storage down" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("codex 满批续跑后睡眠失败上抛", func(t *testing.T) {
		sleeper := &countingSleeper{err: errors.New("pause aborted")}
		job, public, _, stats, db, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
			j.Mode = ModePostgres
			j.Sleep = sleeper.sleep
		})
		public.script = []int64{0}
		stats.usageScript = []UsageStatsRetentionCounts{{}}
		stats.metricsScript = []SystemMetricsRetentionCounts{{}}
		db.sessionsScript = []int64{0}
		db.codexScript = []*CodexContextExpiredCleanup{{DeletedSessions: CleanupBatchSize, StorageKeys: []string{"k"}, HasMore: true}}
		if _, err := job.Run(ctx); err == nil || err.Error() != "pause aborted" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("publicApiLogs 满批耗尽配额", func(t *testing.T) {
		job, public, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) { j.Mode = ModePostgres })
		public.script = []int64{CleanupBatchSize}
		if _, err := job.Run(ctx); err != nil {
			t.Fatal(err)
		}
		if public.calls != CleanupMaxBatchesPerRun {
			t.Fatalf("public calls = %d, want %d", public.calls, CleanupMaxBatchesPerRun)
		}
	})

	t.Run("usage stats 满批耗尽配额", func(t *testing.T) {
		job, _, _, stats, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) { j.Mode = ModePostgres })
		stats.usageScript = make([]UsageStatsRetentionCounts, CleanupMaxBatchesPerRun)
		for index := range stats.usageScript {
			stats.usageScript[index] = UsageStatsRetentionCounts{UsageStatsMinute: 1}
		}
		if _, err := job.Run(ctx); err != nil {
			t.Fatal(err)
		}
		if stats.usageCalls != CleanupMaxBatchesPerRun {
			t.Fatalf("stats calls = %d, want %d", stats.usageCalls, CleanupMaxBatchesPerRun)
		}
	})

	t.Run("非 worker 角色跳过 postgres 投递", func(t *testing.T) {
		job, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) {
			j.Mode = ModePostgres
			j.ProcessRole = "server"
		})
		if err := job.EnqueuePostgresMaintenanceJobs(ctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("投递前取消上抛", func(t *testing.T) {
		job, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) { j.Mode = ModePostgres })
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := job.Run(cancelled); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		job2, _, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) { j.Mode = ModePostgres })
		cancelled2, cancel2 := context.WithCancel(ctx)
		cancel2()
		if err := job2.EnqueuePostgresMaintenanceJobs(cancelled2); !errors.Is(err, context.Canceled) {
			t.Fatalf("EnqueuePostgresMaintenanceJobs(cancelled) err = %v", err)
		}
	})

	t.Run("sleep 中取消在循环顶部捕获", func(t *testing.T) {
		job, public, _, _, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) { j.Mode = ModePostgres })
		public.script = []int64{CleanupBatchSize}
		cancelCtx, cancel := context.WithCancel(ctx)
		job.Sleep = w9gCancelSleeper(cancel)
		if _, err := job.Run(cancelCtx); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if public.calls != 1 {
			t.Fatalf("public calls = %d, want 1", public.calls)
		}
	})

	t.Run("stats 循环顶部取消捕获", func(t *testing.T) {
		job, public, _, stats, _, _ := w9gBaseJob(t, func(j *DataRetentionJob) { j.Mode = ModePostgres })
		public.script = []int64{0}
		stats.usageScript = []UsageStatsRetentionCounts{{UsageStatsMinute: 1}}
		cancelCtx, cancel := context.WithCancel(ctx)
		job.Sleep = w9gCancelSleeper(cancel)
		if _, err := job.Run(cancelCtx); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
}

// ---------- 批次辅助函数（直测） ----------

func TestW9GBatchHelpers(t *testing.T) {
	ctx := context.Background()
	batch := func(context.Context) (int64, error) { return CleanupBatchSize, nil }

	// normalizeMaxBatches 下限取 1。
	total, err := cleanupCountedRetentionBatches(ctx, 0, CleanupBatchSize, batch, nil)
	if err != nil || total != CleanupBatchSize {
		t.Fatalf("total=%d err=%v", total, err)
	}

	// 满批耗尽配额 + 最后一批后不再睡眠；中途睡眠失败上抛。
	sleeper := &countingSleeper{}
	total, err = cleanupCountedRetentionBatches(ctx, 3, CleanupBatchSize, batch, sleeper.sleep)
	if err != nil || total != 3*CleanupBatchSize {
		t.Fatalf("total=%d err=%v", total, err)
	}
	if sleeper.pauses != 2 {
		t.Fatalf("pauses = %d, want 2（最后一批后不睡）", sleeper.pauses)
	}
	if _, err := cleanupCountedRetentionBatches(ctx, 3, CleanupBatchSize, batch,
		func(context.Context) error { return errors.New("pause aborted") }); err == nil {
		t.Fatal("批次间睡眠失败必须上抛")
	}

	// ctx 取消上抛。
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := cleanupCountedRetentionBatches(cancelled, 3, CleanupBatchSize, batch, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if err := runRetentionBatches(cancelled, 3, CleanupBatchSize, batch, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("runRetentionBatches err = %v", err)
	}

	// runRetentionBatches 满批耗尽。
	if err := runRetentionBatches(ctx, 3, CleanupBatchSize, batch, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// yieldToEventLoop 透传取消。
	if err := yieldToEventLoop(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("yieldToEventLoop err = %v", err)
	}

	// pauseCleanupBatch 空转。
	if err := pauseCleanupBatch(ctx, time.Millisecond); err != nil {
		t.Fatalf("pause err = %v", err)
	}
	if err := pauseCleanupBatch(cancelled, time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("pause cancelled err = %v", err)
	}

	// 暂停窗口内取消：select 的 ctx.Done 臂。
	pauseCtx, pauseCancel := context.WithCancel(ctx)
	go func() {
		time.Sleep(2 * time.Millisecond)
		pauseCancel()
	}()
	if err := pauseCleanupBatch(pauseCtx, 50*time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("pause mid-cancel err = %v", err)
	}
	pauseCancel()

	if normalizeMaxBatches(-5) != 1 || normalizeMaxBatches(4) != 4 {
		t.Fatal("normalizeMaxBatches 语义错误")
	}
}

// ---------- 截断键与时区辅助 ----------

func TestW9GCutoffHelpers(t *testing.T) {
	// nil location 回落 UTC。
	now := time.Date(2026, 9, 4, 20, 30, 0, 0, time.UTC)
	if got := dateKey(now, nil); got != "2026-09-04" {
		t.Fatalf("dateKey(nil) = %q", got)
	}
	if got := hourKey(now, nil); got != "2026-09-04T20" {
		t.Fatalf("hourKey(nil) = %q", got)
	}
	if got := minuteKey(now, nil); got != "2026-09-04T20:30" {
		t.Fatalf("minuteKey(nil) = %q", got)
	}
	if got := monthKey(now, nil); got != "2026-09" {
		t.Fatalf("monthKey(nil) = %q", got)
	}

	// 周一折算：任取一个周日，weekKey 必须折回前一周一。
	sunday := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	if sunday.Weekday() != time.Sunday {
		t.Fatalf("测试基准日应为周日, got %v", sunday.Weekday())
	}
	if got := weekKey(sunday, time.UTC); got != "2026-08-31" {
		t.Fatalf("weekKey(sunday) = %q, want 2026-08-31", got)
	}
	if got := weekKey(time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC), time.UTC); got != "2026-09-07" {
		t.Fatalf("weekKey(monday) = %q, want 同日", got)
	}
}

// ---------- chat / expired account 守卫 ----------

func TestW9GChatRetentionGuards(t *testing.T) {
	ctx := context.Background()
	var nilJob *ChatRetentionJob
	if err := nilJob.Run(ctx); err == nil || !strings.Contains(err.Error(), "db service 未初始化") {
		t.Fatalf("nil receiver err = %v", err)
	}
	if err := (&ChatRetentionJob{}).Run(ctx); err == nil || !strings.Contains(err.Error(), "db service 未初始化") {
		t.Fatalf("nil DB err = %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	job := &ChatRetentionJob{DB: &fakeDbService{}}
	if err := job.Run(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled err = %v", err)
	}

	// nil Clock/Logger 走默认值；DB 返回结果时正常完成。
	db := &fakeDbService{chatResult: &ChatRetentionResult{DeletedMessages: 2}}
	ok := &ChatRetentionJob{DB: db, RetentionDays: 3, Timeout: time.Second}
	if err := ok.Run(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// DB 错误上抛。
	if err := (&ChatRetentionJob{DB: &fakeDbService{chatErr: errors.New("chat down")}, Timeout: time.Second}).Run(ctx); err == nil {
		t.Fatal("chat db 错误必须上抛")
	}
}

func TestW9GExpiredAccountGuards(t *testing.T) {
	ctx := context.Background()
	logger, buffer := newTestLogger()

	if err := (&ExpiredDeletedAccountJob{Logger: logger}).Run(ctx); err == nil || !strings.Contains(err.Error(), "db service 未初始化") {
		t.Fatalf("nil DB err = %v", err)
	}
	assertLogContains(t, buffer, "background_expired_deleted_account_cleanup_failed")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := (&ExpiredDeletedAccountJob{DB: &fakeDbService{}}).Run(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled err = %v", err)
	}

	// nil Enqueuer → missingEnqueuer 落投递失败告警；nil Clock 走默认时钟。
	db := &fakeDbService{expiredSummary: &ExpiredDeletedAccountSummary{
		Attempted: 1,
		RecordCleanupTargets: []ExpiredDeletedAccountTarget{{
			AccountID: "acc-1", SystemAccountID: "sys-1",
			RelatedAccountIDs: []string{"rel-1"},
		}},
	}}
	job := &ExpiredDeletedAccountJob{DB: db, Logger: logger}
	if err := job.Run(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertLogContains(t, buffer, "background_expired_deleted_account_record_cleanup_enqueue_failed",
		"worker_dispatch_failed", "background_expired_deleted_account_cleanup_completed")

	// nil Logger 的完成日志路径。
	db2 := &fakeDbService{expiredSummary: &ExpiredDeletedAccountSummary{Attempted: 1}}
	if err := (&ExpiredDeletedAccountJob{DB: db2}).Run(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------- 记录维护执行器 ----------

func TestW9GRunnerExecutorGuards(t *testing.T) {
	ctx := context.Background()
	now := fixedNow()

	// RunOnce 归一化失败上抛。
	runner := &RecordMaintenanceRunner{Clock: fixedClock(now), Logger: discardLogger()}
	if _, err := runner.RunOnce(ctx, RecordMaintenanceJob{Type: JobTypeUsageRecordsCleanup, CutoffAt: "nope"}); err == nil {
		t.Fatal("归一化失败必须上抛")
	}

	// account related 清理错误上抛。
	accountErr := &RecordMaintenanceRunner{Clock: fixedClock(now), Logger: discardLogger()}
	accountErr.Executor = RecordMaintenanceExecutor{RelatedRecords: &scriptedRelatedCleaner{err: errors.New("account shard down")}}
	if _, err := accountErr.RunOnce(ctx, RecordMaintenanceJob{Type: JobTypeAccountRelatedCleanup, AccountID: "a", SystemAccountID: "s"}); err == nil {
		t.Fatal("account 清理错误必须上抛")
	}

	// usageRecords 清理错误上抛。
	usageErr := &RecordMaintenanceRunner{Clock: fixedClock(now), Logger: discardLogger()}
	usageErr.Executor = RecordMaintenanceExecutor{UsageRecords: &fakeUsageRecords{err: errors.New("usage down")}}
	job, err := UsageRecordsCleanupJob(ISOString(now.Add(-48*time.Hour)), 10, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := usageErr.RunOnce(ctx, job); err == nil {
		t.Fatal("usage 清理错误必须上抛")
	}

	// 未初始化 executor 走 missing stub。
	bare := &RecordMaintenanceRunner{Clock: fixedClock(now), Logger: discardLogger()}
	if _, err := bare.RunOnce(ctx, RecordMaintenanceJob{Type: JobTypeAPIKeyRelatedCleanup, APIKeyID: "k", SystemAccountID: "s"}); err == nil {
		t.Fatal("missing relatedRecords 必须 fail-closed")
	}
	if _, err := bare.RunOnce(ctx, job); err == nil {
		t.Fatal("missing usageRecords 必须 fail-closed")
	}
	nbJob, err := NormalizeRecordMaintenanceJob(RecordMaintenanceJob{
		Type: JobTypeNonBusinessDataCleanup, CutoffAt: ISOString(now.Add(-48 * time.Hour)), BatchSize: 10, MaxBatches: 1,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bare.RunOnce(ctx, nbJob); err == nil {
		t.Fatal("missing nonBusinessData 必须 fail-closed")
	}
	if _, err := bare.RunOnce(ctx, validSnapshotJob(now)); err == nil {
		t.Fatal("missing statsWriter 必须 fail-closed")
	}

	// 非业务数据 stats 批失败上抛。
	statsDown := &RecordMaintenanceRunner{Clock: fixedClock(now), Logger: discardLogger()}
	statsDown.Executor = RecordMaintenanceExecutor{
		NonBusinessData: &scriptedNonBusinessCleaner{batches: []NonBusinessDataCleanupCounts{{}}},
		StatsWriter:     &w9gFailingStats{fakeStatsWriter: &fakeStatsWriter{}, nonBizErr: errors.New("nb stats down")},
	}
	if _, err := statsDown.RunOnce(ctx, nbJob); err == nil || err.Error() != "nb stats down" {
		t.Fatalf("err = %v", err)
	}

	// 快照 Upsert 失败上抛。
	upsertDown := &RecordMaintenanceRunner{Clock: fixedClock(now), Logger: discardLogger()}
	upsertDown.Executor = RecordMaintenanceExecutor{StatsWriter: &w9gFailingStats{fakeStatsWriter: &fakeStatsWriter{}, upsertErr: errors.New("upsert down")}}
	if _, err := upsertDown.RunOnce(ctx, validSnapshotJob(now)); err == nil || err.Error() != "upsert down" {
		t.Fatalf("err = %v", err)
	}

	// nil Logger 走 slog.Default()。
	defaultLog := &RecordMaintenanceRunner{Clock: fixedClock(now)}
	defaultLog.Executor = RecordMaintenanceExecutor{RelatedRecords: &scriptedRelatedCleaner{result: RelatedCleanupResult{DeletedRows: 1}}}
	if _, err := defaultLog.RunOnce(ctx, RecordMaintenanceJob{Type: JobTypeAPIKeyRelatedCleanup, APIKeyID: "k", SystemAccountID: "s"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestW9GRunnerBatchLoops(t *testing.T) {
	now := fixedNow()
	old := ISOString(now.Add(-48 * time.Hour))

	t.Run("usage 清理 ctx 取消", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		runner := &RecordMaintenanceRunner{Clock: fixedClock(now), Logger: discardLogger()}
		runner.Executor = RecordMaintenanceExecutor{UsageRecords: &fakeUsageRecords{}}
		job, err := UsageRecordsCleanupJob(old, 10, 3, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.RunOnce(cancelled, job); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("usage 批 BlockedReason 透传", func(t *testing.T) {
		usage := &fakeUsageRecords{script: []UsageRecordsBatch{{BlockedReason: "等待统计安全游标追平"}}}
		runner := &RecordMaintenanceRunner{Clock: fixedClock(now), Logger: discardLogger()}
		runner.Executor = RecordMaintenanceExecutor{UsageRecords: usage}
		job, err := UsageRecordsCleanupJob(old, 10, 3, now)
		if err != nil {
			t.Fatal(err)
		}
		result, err := runner.RunOnce(context.Background(), job)
		if err != nil {
			t.Fatal(err)
		}
		if result["blockedReason"] != "等待统计安全游标追平" {
			t.Fatalf("blockedReason = %v", result["blockedReason"])
		}
	})

	t.Run("usage 满批耗尽配额", func(t *testing.T) {
		usage := &fakeUsageRecords{script: []UsageRecordsBatch{{DeletedRows: CleanupBatchSize, HasMore: true}}}
		runner := &RecordMaintenanceRunner{Clock: fixedClock(now), Logger: discardLogger()}
		runner.Executor = RecordMaintenanceExecutor{UsageRecords: usage}
		job, err := UsageRecordsCleanupJob(old, CleanupBatchSize, CleanupMaxBatchesPerRun, now)
		if err != nil {
			t.Fatal(err)
		}
		result, err := runner.RunOnce(context.Background(), job)
		if err != nil {
			t.Fatal(err)
		}
		if usage.calls != CleanupMaxBatchesPerRun {
			t.Fatalf("calls = %d", usage.calls)
		}
		if result["hasMore"] != true {
			t.Fatalf("hasMore = %v, want true", result["hasMore"])
		}
	})

	t.Run("非业务数据 ctx 取消", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		runner := &RecordMaintenanceRunner{Clock: fixedClock(now), Logger: discardLogger()}
		runner.Executor = RecordMaintenanceExecutor{
			NonBusinessData: &scriptedNonBusinessCleaner{batches: []NonBusinessDataCleanupCounts{{}}},
			StatsWriter:     &fakeStatsWriter{},
		}
		job, err := NormalizeRecordMaintenanceJob(RecordMaintenanceJob{
			Type: JobTypeNonBusinessDataCleanup, CutoffAt: old, BatchSize: 10, MaxBatches: 3,
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.RunOnce(cancelled, job); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("非业务数据数据面失败上抛", func(t *testing.T) {
		runner := &RecordMaintenanceRunner{Clock: fixedClock(now), Logger: discardLogger()}
		runner.Executor = RecordMaintenanceExecutor{
			NonBusinessData: errNonBusinessCleaner{},
			StatsWriter:     &fakeStatsWriter{},
		}
		job, err := NormalizeRecordMaintenanceJob(RecordMaintenanceJob{
			Type: JobTypeNonBusinessDataCleanup, CutoffAt: old, BatchSize: 10, MaxBatches: 3,
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.RunOnce(context.Background(), job); err == nil {
			t.Fatal("数据面错误必须上抛")
		}
	})

	t.Run("非业务数据满批耗尽配额", func(t *testing.T) {
		cleaner := &scriptedNonBusinessCleaner{batches: []NonBusinessDataCleanupCounts{
			{DeletedRows: 1, HasMore: true},
		}}
		runner := &RecordMaintenanceRunner{Clock: fixedClock(now), Logger: discardLogger()}
		runner.Executor = RecordMaintenanceExecutor{NonBusinessData: cleaner, StatsWriter: &fakeStatsWriter{}}
		job, err := NormalizeRecordMaintenanceJob(RecordMaintenanceJob{
			Type: JobTypeNonBusinessDataCleanup, CutoffAt: old, BatchSize: 10, MaxBatches: CleanupMaxBatchesPerRun,
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.RunOnce(context.Background(), job); err != nil {
			t.Fatal(err)
		}
		if cleaner.calls != CleanupMaxBatchesPerRun {
			t.Fatalf("cleaner calls = %d, want %d", cleaner.calls, CleanupMaxBatchesPerRun)
		}
	})

	t.Run("安全游标字段进入日志与结果", func(t *testing.T) {
		logger, buffer := newTestLogger()
		cleaner := &scriptedRelatedCleaner{result: RelatedCleanupResult{
			DeletedRows: 4, SafetyCursorCreatedAt: "2026-09-01T00:00:00.000Z", SafetyCursorID: "cursor-1",
		}}
		runner := &RecordMaintenanceRunner{Mode: ModeSQLite, Clock: fixedClock(now), Logger: logger}
		runner.Executor = RecordMaintenanceExecutor{RelatedRecords: cleaner, StatsWriter: &fakeStatsWriter{}}
		result, err := runner.RunOnce(context.Background(), RecordMaintenanceJob{
			Type: JobTypeAccountRelatedCleanup, AccountID: "acc-1", SystemAccountID: "sys-1",
			RelatedAccountIDs: []string{"rel"}, AuthorizationIDs: []string{"auth"}, TeamScopeIDs: []string{"team"},
		})
		if err != nil {
			t.Fatal(err)
		}
		assertLogContains(t, buffer, "safetyCursorCreatedAt", "safetyCursorId")
		if result["safetyCursorId"] != "cursor-1" || result["relatedAccountIds"] == nil {
			t.Fatalf("result = %+v", result)
		}
	})
}

type errNonBusinessCleaner struct{}

func (errNonBusinessCleaner) CleanupBefore(context.Context, string, int) (NonBusinessDataCleanupCounts, error) {
	return NonBusinessDataCleanupCounts{}, errors.New("dataset cleaner down")
}

type errDeleter struct{}

func (errDeleter) DeleteStorageKeys(context.Context, []string) (StorageKeyDeletionResult, error) {
	return StorageKeyDeletionResult{}, errors.New("deleter down")
}

func TestW9GSnapshotUpsertBatch(t *testing.T) {
	now := fixedNow()
	logger, buffer := newTestLogger()
	stats := &fakeStatsWriter{}
	runner := &RecordMaintenanceRunner{Mode: ModeSQLite, Clock: fixedClock(now), Logger: logger}
	runner.Executor = RecordMaintenanceExecutor{StatsWriter: stats}

	jobs := []RecordMaintenanceJob{validSnapshotJob(now), validSnapshotJob(now)}
	jobs[1].AccountID = "acc-2"
	result, err := runner.RunAccountUsageSnapshotUpserts(context.Background(), jobs)
	if err != nil {
		t.Fatal(err)
	}
	if result["upsertedCount"] != 2 || stats.usageCalls != 0 {
		t.Fatalf("result = %v usageCalls = %d", result["upsertedCount"], stats.usageCalls)
	}
	assertLogContains(t, buffer, "record_maintenance_account_usage_snapshots_upserted")

	// 归一化失败上抛。
	if _, err := runner.RunAccountUsageSnapshotUpserts(context.Background(), []RecordMaintenanceJob{{Type: "mystery"}}); err == nil {
		t.Fatal("归一化失败必须上抛")
	}

	// Upsert 失败上抛。
	failing := &RecordMaintenanceRunner{Mode: ModeSQLite, Clock: fixedClock(now), Logger: discardLogger()}
	failing.Executor = RecordMaintenanceExecutor{StatsWriter: &w9gFailingStats{fakeStatsWriter: &fakeStatsWriter{}, upsertErr: errors.New("batch upsert down")}}
	if _, err := failing.RunAccountUsageSnapshotUpserts(context.Background(), jobs); err == nil {
		t.Fatal("批量 Upsert 失败必须上抛")
	}
}

// ---------- 校验与解析 ----------

func TestW9GJobValidation(t *testing.T) {
	// regex 通过但日历非法（2 月 30 日）。
	if _, ok := parseRfc3339Instant("2026-02-30T20:30:00Z"); ok {
		t.Fatal("2026-02-30 必须被 time.Parse 拒绝")
	}

	// Decode 校验：account 相关缺字段。
	if _, err := DecodeRecordMaintenanceJob(mustJobJSON(t, map[string]any{
		"type": JobTypeAccountRelatedCleanup, "accountId": "a",
	})); err == nil {
		t.Fatal("account 相关缺 systemAccountId 必须拒绝")
	}

	// Decode 校验：usage 清理缺批次配置。
	if _, err := DecodeRecordMaintenanceJob(mustJobJSON(t, map[string]any{
		"type": JobTypeUsageRecordsCleanup, "cutoffAt": "2026-09-04T20:30:00Z",
	})); err == nil {
		t.Fatal("usage 清理缺 batch 配置必须拒绝")
	}

	// Decode 校验：快照缺字段/非法 updatedAt。
	if _, err := DecodeRecordMaintenanceJob(mustJobJSON(t, map[string]any{
		"type": JobTypeAccountUsageSnapshotUpsert, "kind": "openai_codex",
		"snapshot": map[string]any{}, "updatedAt": "2026-09-04T20:30:00Z",
	})); err == nil {
		t.Fatal("快照缺 accountId 必须拒绝")
	}
	if _, err := DecodeRecordMaintenanceJob(mustJobJSON(t, map[string]any{
		"type": JobTypeAccountUsageSnapshotUpsert, "accountId": "a", "kind": "openai_codex",
		"snapshot": map[string]any{}, "updatedAt": "nope",
	})); err == nil {
		t.Fatal("快照非法 updatedAt 必须拒绝")
	}

	// APIKeyRelatedCleanupJob 构造器。
	job, err := APIKeyRelatedCleanupJob("key-1", "sys-1", fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	if job.Type != JobTypeAPIKeyRelatedCleanup || job.APIKeyID != "key-1" || job.ID == "" {
		t.Fatalf("job = %+v", job)
	}
}

func TestW9GSettingNumberIntegerTypes(t *testing.T) {
	settings := validPolicySettings()
	base := int64(30)
	for _, value := range []any{
		int8(base), int16(base), int32(base), int(base), int64(base),
		uint(base), uint32(base), uint64(base),
		float64(base),
	} {
		settings[SettingPublicApiLogRetentionDays] = value
		got, err := SettingNumber(settings, SettingPublicApiLogRetentionDays, 1, 365)
		if err != nil || got != base {
			t.Fatalf("value %T: got=%d err=%v", value, got, err)
		}
	}

	// 非整数 float64 / json.Number / 其他类型都拒绝（json.Number 合法值另测）。
	for _, value := range []any{1.5, json.Number("abc"), "30", true} {
		settings[SettingPublicApiLogRetentionDays] = value
		if _, err := SettingNumber(settings, SettingPublicApiLogRetentionDays, 1, 365); err == nil {
			t.Fatalf("value %T 必须拒绝", value)
		}
	}

	// json.Number 整数值被接受。
	settings[SettingPublicApiLogRetentionDays] = json.Number("120")
	if got, err := SettingNumber(settings, SettingPublicApiLogRetentionDays, 1, 365); err != nil || got != 120 {
		t.Fatalf("json.Number: got=%d err=%v", got, err)
	}

	if errNotInteger.Error() != "value is not an integer" {
		t.Fatalf("errNotInteger = %q", errNotInteger.Error())
	}
}

// ---------- Codex context 存储清理 ----------

func TestW9GCodexStorageDeleterEdgePaths(t *testing.T) {
	root := t.TempDir()

	// 非法 storage key 记入失败清单。
	result, err := NewFilesystemKeyDeleter(root).DeleteStorageKeys(context.Background(), []string{"../escape.json"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) != 1 || result.Failures[0].StorageKey != "../escape.json" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.SucceededStorageKeys) != 0 {
		t.Fatalf("succeeded = %+v", result.SucceededStorageKeys)
	}

	// 超长文件名：Lstat 错误不属于 ErrNotExist，记入失败清单。
	longKey := strings.Repeat("x", 300) + ".json"
	result, err = NewFilesystemKeyDeleter(root).DeleteStorageKeys(context.Background(), []string{longKey})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) != 1 || result.Deleted != 0 {
		t.Fatalf("long key result = %+v", result)
	}

	// 已占用文件（无共享位句柄）删除失败记入失败清单（Windows 语义）。
	if runtime.GOOS == "windows" {
		existing := filepath.Join(root, "locked.json")
		if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		handle, err := windows.CreateFile(windows.StringToUTF16Ptr(existing),
			windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			t.Skipf("打开句柄失败（环境限制）: %v", err)
		}
		result, err := NewFilesystemKeyDeleter(root).DeleteStorageKeys(context.Background(), []string{"locked.json"})
		_ = windows.CloseHandle(handle)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Failures) != 1 || result.Deleted != 0 {
			t.Fatalf("locked result = %+v", result)
		}
		if len(result.SucceededStorageKeys) != 0 {
			t.Fatalf("失败 key 不得进入 succeeded: %+v", result.SucceededStorageKeys)
		}
		if _, statErr := os.Lstat(existing); statErr != nil {
			t.Fatalf("删除失败后文件必须保留: %v", statErr)
		}
	}
}

func TestW9GCodexProcessorGuards(t *testing.T) {
	ctx := context.Background()

	// Deleter 未初始化。
	if _, err := (&CodexContextStorageProcessor{}).ProcessBatch(ctx, nil); err == nil {
		t.Fatal("nil deleter 必须 fail-closed")
	}

	// Deleter 自身失败上抛。
	failing := &CodexContextStorageProcessor{Deleter: errDeleter{}, DB: &fakeDbService{}}
	if _, err := failing.ProcessBatch(ctx, []string{"k"}); err == nil || err.Error() != "deleter down" {
		t.Fatalf("err = %v", err)
	}

	// 非业务数据 runner 的 statsWriter 未初始化走 missing stub。
	now := fixedNow()
	runner := &RecordMaintenanceRunner{Mode: ModeSQLite, Clock: fixedClock(now), Logger: discardLogger()}
	runner.Executor = RecordMaintenanceExecutor{
		NonBusinessData: &scriptedNonBusinessCleaner{batches: []NonBusinessDataCleanupCounts{{}}},
	}
	job, err := NormalizeRecordMaintenanceJob(RecordMaintenanceJob{
		Type: JobTypeNonBusinessDataCleanup, CutoffAt: ISOString(now.Add(-48 * time.Hour)), BatchSize: 10, MaxBatches: 1,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunOnce(ctx, job); err == nil || !strings.Contains(err.Error(), "retention stats writer 未初始化") {
		t.Fatalf("err = %v", err)
	}

	// 删除存在失败时经默认 logger 告警（nil Logger 路径）。
	root := t.TempDir()
	if runtime.GOOS == "windows" {
		existing := filepath.Join(root, "locked.json")
		if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		handle, err := windows.CreateFile(windows.StringToUTF16Ptr(existing),
			windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			t.Skipf("打开句柄失败（环境限制）: %v", err)
		}
		processor := &CodexContextStorageProcessor{
			Deleter: NewFilesystemKeyDeleter(root),
			DB:      &fakeDbService{},
		}
		_, err = processor.ProcessBatch(ctx, []string{"locked.json"})
		_ = windows.CloseHandle(handle)
		if err != nil {
			t.Fatalf("失败仅延迟重试，不得上抛: %v", err)
		}
	}
}
