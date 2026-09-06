package main

// F3-1 组合根级测试：系统设置驱动的调度间隔真正生效。Node
// background-jobs.ts 在 scheduler.schedule 时用 settingsNumber 固定间隔，
// Go 组合根在同一时点（装配期）经 jobssettings 读模型解析；本组测试用
// mock 与真实 SQLite 读模型分别锁定该契约。

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobregistry"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
)

func newScheduleTestAssembly(t *testing.T) *workerAssembly {
	t.Helper()
	return &workerAssembly{
		config: workerConfig{
			Enabled:    true,
			Driver:     "sqlite",
			WorkerRole: "stats-worker",
		},
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		wiredTasks: map[string]jobsched.Task{},
		scheduler:  jobsched.NewScheduler(jobsched.Options{StableSeed: "schedule-test"}),
	}
}

func scheduledIntervalMS(t *testing.T, assembly *workerAssembly, name string) int64 {
	t.Helper()
	for _, snapshot := range assembly.scheduler.Snapshots() {
		if snapshot.Name == name {
			return snapshot.IntervalMS
		}
	}
	t.Fatalf("任务 %s 未注册", name)
	return 0
}

func noopScheduleTask(_ context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
	return jobsched.TaskResult{}, nil
}

// TestScheduleWiredJobSettingsInterval：mock 设置源返回的间隔覆盖注册表
// 默认（改设置 → 间隔生效的组合根契约）。
func TestScheduleWiredJobSettingsInterval(t *testing.T) {
	assembly := newScheduleTestAssembly(t)
	assembly.scheduleIntervals = func(jobName string) (time.Duration, bool) {
		if jobName == "account-api-key-cooldown-retest" {
			return 7 * time.Second, true
		}
		return 0, false
	}
	assembly.scheduleWiredJob("account-api-key-cooldown-retest", noopScheduleTask)
	if got := scheduledIntervalMS(t, assembly, "account-api-key-cooldown-retest"); got != int64(7*time.Second/time.Millisecond) {
		t.Fatalf("设置间隔必须生效: got %dms want 7000ms", got)
	}
	// 非设置驱动 job 不受影响。
	assembly.scheduleWiredJob("background-task-run-reconcile", noopScheduleTask)
	if got := scheduledIntervalMS(t, assembly, "background-task-run-reconcile"); got != int64((5*time.Minute)/time.Millisecond) {
		t.Fatalf("非设置驱动 job 必须保持注册表默认: got %dms", got)
	}
}

// TestScheduleWiredJobNilSettingsKeepsRegistryInterval：无设置读模型时按
// 注册表默认间隔（cooldown-retest 默认 3s）。
func TestScheduleWiredJobNilSettingsKeepsRegistryInterval(t *testing.T) {
	assembly := newScheduleTestAssembly(t)
	assembly.scheduleWiredJob("account-api-key-cooldown-retest", noopScheduleTask)
	if got := scheduledIntervalMS(t, assembly, "account-api-key-cooldown-retest"); got != int64(jobregistry.CooldownRetestInterval/time.Millisecond) {
		t.Fatalf("nil 设置源必须回落注册表默认: got %dms want %dms", got, jobregistry.CooldownRetestInterval/time.Millisecond)
	}
}

// TestWireScheduleSettingsReadsSQLiteSettings：真实 jobssettings SQLite 读
// 模型下，system_settings 的间隔值在装配期生效；改设置后重新装配读到新值
// （Node schedule() 时固定间隔的等价语义）。
func TestWireScheduleSettingsReadsSQLiteSettings(t *testing.T) {
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	business, err := sql.Open("sqlite", businessPath)
	if err != nil {
		t.Fatal(err)
	}
	defer business.Close()
	if _, err := business.Exec(`CREATE TABLE system_settings (system_account_id TEXT, key TEXT, value_json TEXT)`); err != nil {
		t.Fatal(err)
	}
	insertSetting := func(seconds string) {
		t.Helper()
		if _, err := business.Exec(`INSERT INTO system_settings (system_account_id, key, value_json)
			VALUES ('sys_admin', 'cooldownAccountRetestIntervalSeconds', ?)`, seconds); err != nil {
			t.Fatal(err)
		}
	}
	newAssemblyOverDB := func() *workerAssembly {
		assembly := newScheduleTestAssembly(t)
		assembly.config.BusinessSQLitePath = businessPath
		if err := assembly.wireScheduleSettings(); err != nil {
			t.Fatal(err)
		}
		// 直构装配体没有 buildWorkerAssembly 的 closers 注册，TempDir 清理
		// 前先关掉 wireScheduleSettings 打开的 SQLite 句柄。
		t.Cleanup(func() {
			for _, db := range assembly.sqliteDBs {
				_ = db.Close()
			}
		})
		return assembly
	}

	insertSetting("9")
	assembly := newAssemblyOverDB()
	assembly.scheduleWiredJob("account-api-key-cooldown-retest", noopScheduleTask)
	if got := scheduledIntervalMS(t, assembly, "account-api-key-cooldown-retest"); got != 9000 {
		t.Fatalf("设置 9s 必须生效: got %dms", got)
	}

	// 改设置 → 下次装配生效（间隔在 schedule() 时固定，Node 同语义）。
	if _, err := business.Exec(`UPDATE system_settings SET value_json = '12'
		WHERE key = 'cooldownAccountRetestIntervalSeconds'`); err != nil {
		t.Fatal(err)
	}
	reassembled := newAssemblyOverDB()
	reassembled.scheduleWiredJob("account-api-key-cooldown-retest", noopScheduleTask)
	if got := scheduledIntervalMS(t, reassembled, "account-api-key-cooldown-retest"); got != 12000 {
		t.Fatalf("改设置后重新装配必须读到新间隔: got %dms want 12000ms", got)
	}
}

// TestJobsScheduleIntervalSpecsMatchRegistry：组合根边界表与注册表 job→键
// 权威表必须一致。
func TestJobsScheduleIntervalSpecsMatchRegistry(t *testing.T) {
	for name := range jobsScheduleIntervalSpecs {
		if _, ok := jobregistry.SettingsIntervalJobNames()[name]; !ok {
			t.Fatalf("组合根间隔表含注册表未登记的 job: %s", name)
		}
	}
	for name := range jobregistry.SettingsIntervalJobNames() {
		if _, ok := jobsScheduleIntervalSpecs[name]; !ok {
			t.Fatalf("注册表设置间隔 job 缺少组合根边界: %s", name)
		}
	}
}
