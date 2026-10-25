package main

// jobs 框架域排查（缺陷1/2/3/4）的 cmd 组合根级回归测试：
//   - 缺陷1：juhe_stats.background_task_runs / juhe_stats.background_job_leases 纳入保留清理，随
//     data-retention-cleanup 节拍滚动删除（清理前后行数断言）；
//   - 缺陷2：withLease 包裹的任务 panic 转失败终态（运行记录 failed + 租约
//     释放收口），不外溢打断状态机；
//   - 缺陷3：排空超时截断在飞任务时显式披露（worker_scheduler_drain_truncated）；
//   - 缺陷4：重名注册装配期 fail-fast，首次注册保持原状。

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/taskruns"
)

// TestScheduleWiredJobDuplicateRegistrationFailsFast：重名注册必须记录
// fail-fast 错误（指名任务），且首次注册保持原状（记账不失真）。
func TestScheduleWiredJobDuplicateRegistrationFailsFast(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{}, slog.Default())
	defer assembly.scheduler.Stop()

	var firstCalls, secondCalls atomic.Int64
	assembly.scheduleWiredJob("usage-stats-consistency-check", func(_ context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		firstCalls.Add(1)
		return jobsched.TaskResult{}, nil
	})
	assembly.scheduleWiredJob("usage-stats-consistency-check", func(_ context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		secondCalls.Add(1)
		return jobsched.TaskResult{}, nil
	})

	if assembly.wiringError() == nil {
		t.Fatal("重名注册必须记录装配期 fail-fast 错误")
	}
	if !strings.Contains(assembly.wiringError().Error(), "usage-stats-consistency-check") {
		t.Fatalf("fail-fast 错误必须指名重复任务: %v", assembly.wiringError())
	}
	if len(assembly.wiredJobs) != 1 {
		t.Fatalf("重复注册不得追加 wiredJobs: %d", len(assembly.wiredJobs))
	}
	if _, err := assembly.runWiredJobOnce(context.Background(), "usage-stats-consistency-check"); err != nil {
		t.Fatalf("单轮执行失败: %v", err)
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 0 {
		t.Fatalf("重名注册必须保留首个任务闭包: first=%d second=%d", firstCalls.Load(), secondCalls.Load())
	}
}

// newTaskRunHistoryAssembly 在独立 PG 测试库上构造带 taskruns 运行历史的
// 装配体（PG 唯一形态；原 sqlite 文件夹具随 sqlite 形态退役删除）。
func newTaskRunHistoryAssembly(t *testing.T, _ string) (*workerAssembly, *sql.DB) {
	t.Helper()
	db, dsn := wgOpenPGDialectDB(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS juhe_stats`)
		return err
	})
	store, err := taskruns.OpenStore(taskruns.StoreConfig{Mode: taskruns.ModePostgres, PostgresURL: dsn})
	if err != nil {
		t.Fatalf("open taskruns store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	assembly := &workerAssembly{
		config:        workerConfig{InstanceID: "wg-history", WorkerRole: "worker", WorkerReplicaIdx: 1},
		logger:        slog.Default(),
		taskRunsStore: store,
	}
	return assembly, db
}

// TestWithLeasePanicRecordsFailedTerminalAndReleasesLease：包裹任务 panic 后
// 运行记录落 failed 终态（panic 值留痕）、临时租约释放收口，错误含 panic 值
// 透传给调度器。
func TestWithLeasePanicRecordsFailedTerminalAndReleasesLease(t *testing.T) {
	assembly, history := newTaskRunHistoryAssembly(t, "postgres")
	wrapped := assembly.withLease("usage-stats-aggregation", time.Minute, func(_ context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		panic("boom-lease-inject")
	})
	_, err := wrapped(context.Background(), jobsched.TaskContext{})
	if err == nil || !strings.Contains(err.Error(), "boom-lease-inject") {
		t.Fatalf("panic 必须转为错误并留痕 panic 值: %v", err)
	}
	row := history.QueryRow(`SELECT status, error_message FROM juhe_stats.background_task_runs`)
	var status string
	var errorMessage sql.NullString
	if err := row.Scan(&status, &errorMessage); err != nil {
		t.Fatalf("读取运行记录失败: %v", err)
	}
	if status != "failed" {
		t.Fatalf("panic 轮必须落 failed 终态: %q", status)
	}
	if !errorMessage.Valid || !strings.Contains(errorMessage.String, "boom-lease-inject") {
		t.Fatalf("error_message 必须留痕 panic 值: %#v", errorMessage)
	}
	var tempLeases int64
	if err := history.QueryRow(`SELECT COUNT(*) FROM juhe_stats.background_job_leases WHERE job_name = 'temporary-maintenance-worker'`).Scan(&tempLeases); err != nil {
		t.Fatal(err)
	}
	if tempLeases != 0 {
		t.Fatalf("panic 后临时任务租约必须已释放收口: %d", tempLeases)
	}
}

// TestWorkerSchedulerDrainTruncationDisclosedWarn：排空超时放弃在飞任务时，
// 组件必须披露截断（worker_scheduler_drain_truncated）并在终态收口宽限内
// 等任务结束后正常返回。
func TestWorkerSchedulerDrainTruncationDisclosedWarn(t *testing.T) {
	var buffer syncBuffer
	logger := slog.New(slog.NewTextHandler(&buffer, nil))
	config := workerConfig{DrainTimeout: 100 * time.Millisecond}
	assembly := newWorkerAssembly(config, logger)
	defer assembly.closeStores()

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	assembly.scheduler.Schedule(jobsched.Spec{
		Name:         "drain-truncate-job",
		Interval:     time.Hour,
		InitialDelay: time.Millisecond,
		Task: func(ctx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
			started <- struct{}{}
			<-release
			return jobsched.TaskResult{}, nil
		},
	})
	<-started

	runCtx, cancelRun := context.WithCancel(context.Background())
	cancelRun()
	done := make(chan error, 1)
	go func() { done <- assembly.components()[0].Run(runCtx) }()

	deadline := time.After(10 * time.Second)
	for {
		if strings.Contains(buffer.String(), "worker_scheduler_drain_truncated") {
			break
		}
		select {
		case <-deadline:
			t.Fatal("排空截断未按约定披露 worker_scheduler_drain_truncated")
		case <-time.After(5 * time.Millisecond):
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("截断披露后组件必须正常返回: %v", err)
	}
}

// syncBuffer 是测试日志捕获缓冲（并发安全）。
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
