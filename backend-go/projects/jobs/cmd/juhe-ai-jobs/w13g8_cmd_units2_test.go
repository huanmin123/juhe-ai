// 波次 w13g8 第二批 cmd 单元批测：
//   - loadWorkerConfig 剩余范围校验与 env 解析臂（worker_config.go）；
//   - retention 组合根端口适配器的错误分支、codex 结算、checkpoint、
//     flushLoop 失败重试（worker_retention.go；SQLite 进程内）；
//   - balanceDetectRuntime 提交/启用/快照替换的围栏与错误分支
//     （worker_balance_detect.go）。
//
// 不可达语句归因（见文末登记）：normalizeBalanceConfigJSON 对
// balanceConfigJSON struct 的 json.Marshal 不会失败，其错误返回分支与
// EnableDetectedQuery 的 normalize 错误分支为防御性不可达；retention
// location() 的 LoadLocation 失败分支同理（timezoneName 已预校验）。
package main

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/cleanuprepo"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// TestW13G8LoadWorkerConfigRemainingArms 覆盖 loadWorkerConfig 尚未覆盖的
// 范围校验与解析失败臂。
func TestW13G8LoadWorkerConfigRemainingArms(t *testing.T) {
	base := map[string]string{
		"JUHE_AI_DATABASE_PATH": filepath.Join(t.TempDir(), "business.sqlite3"),
	}
	for _, test := range []struct {
		name     string
		patch    map[string]string
		fragment string
	}{
		{"chat 保留天数非法", map[string]string{"JUHE_AI_CHAT_RETENTION_DAYS": "0"}, "JUHE_AI_CHAT_RETENTION_DAYS"},
		{"chat 保留天数越界", map[string]string{"JUHE_AI_CHAT_RETENTION_DAYS": "366"}, "JUHE_AI_CHAT_RETENTION_DAYS"},
		{"chat 保留天数非整数", map[string]string{"JUHE_AI_CHAT_RETENTION_DAYS": "w13g8"}, "JUHE_AI_CHAT_RETENTION_DAYS"},
		{"维护批量越界", map[string]string{"JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_BATCH_SIZE": "20000"}, "RECORD_MAINTENANCE_BATCH_SIZE"},
		{"队列条目非整数", map[string]string{"JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_QUEUE_MAX_ITEMS": "x"}, "QUEUE_MAX_ITEMS"},
		{"队列内存非整数", map[string]string{"JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_QUEUE_MAX_MB": "y"}, "QUEUE_MAX_MB"},
		{"关停冲刷批非整数", map[string]string{"JUHE_AI_BACKGROUND_RECORD_MAINTENANCE_SHUTDOWN_FLUSH_MAX_BATCHES": "z"}, "SHUTDOWN_FLUSH_MAX_BATCHES"},
		{"探针并发越界", map[string]string{"JUHE_AI_JOBS_PROBE_CONCURRENCY": "5097"}, "JUHE_AI_JOBS_PROBE_CONCURRENCY"},
		{"投影间隔越界", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED": "true", "JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_INTERVAL_MS": "999"}, "PROJECTION_INTERVAL_MS"},
		{"投影批量越界", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED": "true", "JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_BATCH_SIZE": "101"}, "PROJECTION_BATCH_SIZE"},
		{"投影轮次越界", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED": "true", "JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_MAX_BATCHES_PER_RUN": "401"}, "MAX_BATCHES_PER_RUN"},
		{"投影并发越界", map[string]string{"JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED": "true", "JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_WORKER_CONCURRENCY": "9"}, "WORKER_CONCURRENCY"},
		{"开关非布尔", map[string]string{"JUHE_AI_JOBS_PROBE_ENABLED": "maybe"}, "JUHE_AI_JOBS_PROBE_ENABLED"},
		{"stats 缺路径", map[string]string{"JUHE_AI_JOBS_STATS_ENABLED": "true", "JUHE_AI_DATABASE_PATH": "", "JUHE_AI_STATS_DATABASE_PATH": ""}, "JUHE_AI_STATS_DATABASE_PATH"},
		// 下方用例按 loadWorkerConfig 的 SQLite 门禁顺序（stats → oauth →
		// task-runs → usage-writer → retention → balance → probe）禁用无关
		// 家族，确保 patch 命中的是目标门禁分支而非前置拦截。
		{"oauth 缺路径", map[string]string{"JUHE_AI_JOBS_OAUTH_ENABLED": "true", "JUHE_AI_JOBS_STATS_ENABLED": "false", "JUHE_AI_DATABASE_PATH": ""}, "JUHE_AI_JOBS_OAUTH_ENABLED"},
		{"task-runs 缺路径", map[string]string{"JUHE_AI_JOBS_TASK_RUNS_ENABLED": "true", "JUHE_AI_JOBS_STATS_ENABLED": "false", "JUHE_AI_JOBS_OAUTH_ENABLED": "false", "JUHE_AI_TASK_RUNS_DATABASE_PATH": ""}, "JUHE_AI_TASK_RUNS_DATABASE_PATH"},
		{"usage-writer 缺 catalog", map[string]string{"JUHE_AI_JOBS_USAGE_WRITER_ENABLED": "true", "JUHE_AI_JOBS_STATS_ENABLED": "false", "JUHE_AI_JOBS_OAUTH_ENABLED": "false", "JUHE_AI_JOBS_TASK_RUNS_ENABLED": "false", "JUHE_AI_USAGE_CATALOG_DATABASE_PATH": "", "JUHE_AI_USAGE_SHARD_ROOT": ""}, "JUHE_AI_USAGE_CATALOG_DATABASE_PATH"},
		{"usage-writer 缺业务库", map[string]string{"JUHE_AI_JOBS_USAGE_WRITER_ENABLED": "true", "JUHE_AI_JOBS_STATS_ENABLED": "false", "JUHE_AI_JOBS_OAUTH_ENABLED": "false", "JUHE_AI_JOBS_TASK_RUNS_ENABLED": "false", "JUHE_AI_USAGE_CATALOG_DATABASE_PATH": "w13g8-catalog.sqlite3", "JUHE_AI_USAGE_SHARD_ROOT": "w13g8-shards", "JUHE_AI_DATABASE_PATH": ""}, "usage 记录的业务库副作用"},
		{"retention 缺 dataset", map[string]string{"JUHE_AI_JOBS_RETENTION_ENABLED": "true", "JUHE_AI_JOBS_STATS_ENABLED": "false", "JUHE_AI_JOBS_OAUTH_ENABLED": "false", "JUHE_AI_JOBS_TASK_RUNS_ENABLED": "false", "JUHE_AI_JOBS_USAGE_WRITER_ENABLED": "false", "JUHE_AI_DATASET_DATABASE_PATH": ""}, "JUHE_AI_DATASET_DATABASE_PATH"},
		{"retention 缺 chat", map[string]string{"JUHE_AI_JOBS_RETENTION_ENABLED": "true", "JUHE_AI_JOBS_STATS_ENABLED": "false", "JUHE_AI_JOBS_OAUTH_ENABLED": "false", "JUHE_AI_JOBS_TASK_RUNS_ENABLED": "false", "JUHE_AI_JOBS_USAGE_WRITER_ENABLED": "false", "JUHE_AI_DATASET_DATABASE_PATH": "w13g8-dataset.sqlite3", "JUHE_AI_CHAT_DATABASE_PATH": ""}, "JUHE_AI_CHAT_DATABASE_PATH"},
		{"retention 缺 codex shard", map[string]string{"JUHE_AI_JOBS_RETENTION_ENABLED": "true", "JUHE_AI_JOBS_STATS_ENABLED": "false", "JUHE_AI_JOBS_OAUTH_ENABLED": "false", "JUHE_AI_JOBS_TASK_RUNS_ENABLED": "false", "JUHE_AI_JOBS_USAGE_WRITER_ENABLED": "false", "JUHE_AI_DATASET_DATABASE_PATH": "w13g8-dataset.sqlite3", "JUHE_AI_CHAT_DATABASE_PATH": "w13g8-chat.sqlite3", "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT": ""}, "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"},
		{"balance 缺路径", map[string]string{"JUHE_AI_JOBS_BALANCE_DETECT_ENABLED": "true", "JUHE_AI_JOBS_STATS_ENABLED": "false", "JUHE_AI_JOBS_OAUTH_ENABLED": "false", "JUHE_AI_JOBS_TASK_RUNS_ENABLED": "false", "JUHE_AI_JOBS_USAGE_WRITER_ENABLED": "false", "JUHE_AI_JOBS_RETENTION_ENABLED": "false", "JUHE_AI_DATABASE_PATH": ""}, "JUHE_AI_JOBS_BALANCE_DETECT_ENABLED"},
		{"probe 缺 secret", map[string]string{"JUHE_AI_JOBS_PROBE_ENABLED": "true", "JUHE_AI_JOBS_STATS_ENABLED": "false", "JUHE_AI_JOBS_OAUTH_ENABLED": "false", "JUHE_AI_JOBS_TASK_RUNS_ENABLED": "false", "JUHE_AI_JOBS_USAGE_WRITER_ENABLED": "false", "JUHE_AI_JOBS_RETENTION_ENABLED": "false", "JUHE_AI_JOBS_BALANCE_DETECT_ENABLED": "false", "JUHE_AI_SECRET": ""}, "JUHE_AI_SECRET"},
	} {
		t.Run(test.name, func(t *testing.T) {
			getenv := func(key string) string {
				if value, ok := test.patch[key]; ok {
					return value
				}
				if value, ok := base[key]; ok {
					return value
				}
				return ""
			}
			config, err := loadWorkerConfig(getenv)
			if err == nil {
				t.Fatalf("必须报配置错误，得到合法配置 %+v", config)
			}
			if !strings.Contains(err.Error(), test.fragment) {
				t.Fatalf("错误必须包含 %q: %v", test.fragment, err)
			}
		})
	}
}

// TestW13G8RetentionPortArms 驱动 retention 组合根端口适配器的错误与结算分支。
func TestW13G8RetentionPortArms(t *testing.T) {
	dir := t.TempDir()
	seedDataRetention(t, dir)
	assembly, err := buildWorkerAssembly(retentionTestConfig(dir), slog.Default())
	if err != nil {
		t.Fatalf("build worker assembly: %v", err)
	}
	defer assembly.closeStores()
	family := assembly.retention
	if family == nil {
		t.Fatal("retention family 必须装配")
	}
	ctx := context.Background()
	// recordCleanup 未初始化的显式错误分支。
	nilCleaner := &familyRelatedCleaner{family: &retentionFamily{}}
	if _, err := nilCleaner.CleanupApiKeyRelated(ctx, retention.RecordMaintenanceJob{}, nil); err == nil || !strings.Contains(err.Error(), "未初始化") {
		t.Fatalf("nil recordCleanup 必须报错: %v", err)
	}
	if _, err := nilCleaner.CleanupAccountRelated(ctx, retention.RecordMaintenanceJob{}, nil); err == nil || !strings.Contains(err.Error(), "未初始化") {
		t.Fatalf("nil recordCleanup 必须报错: %v", err)
	}
	// family.timezone 失败 → 三个 stats 清理端口错误透传。
	failingTZ := &retentionFamily{assembly: assembly, timezone: func(context.Context) (*time.Location, error) {
		return nil, context.DeadlineExceeded
	}}
	writer := &familyStatsWriter{family: failingTZ}
	if _, err := writer.CleanupNonBusinessStatsData(ctx, "2020-01-01T00:00:00.000Z", 10); err == nil {
		t.Fatal("时区失败必须透传")
	}
	if err := writer.CleanupDeletedApiKeyRecordStats(ctx, retention.DeletedApiKeyRecordStatsCleanupInput{}); err == nil {
		t.Fatal("时区失败必须透传")
	}
	if err := writer.CleanupDeletedAccountRecordStats(ctx, retention.DeletedAccountRecordStatsCleanupInput{}); err == nil {
		t.Fatal("时区失败必须透传")
	}
	// codex 端口：过期状态清理 + 结算映射（seedDataRetention 已建过期行）。
	if _, err := family.dbService.CleanupExpiredCodexContextStates(ctx, "2100-01-01T00:00:00.000Z", 10); err != nil {
		t.Fatalf("codex 过期清理必须成功: %v", err)
	}
	settlement, err := family.dbService.SettleCodexContextStorageCleanup(ctx, retention.CodexContextSettlement{
		SucceededStorageKeys: []string{"resp/old.bin"},
		Failures:             []retention.CodexContextCleanupFailure{{StorageKey: "resp/missing.bin", Error: "w13g8 missing"}},
	})
	if err != nil {
		t.Fatalf("codex 结算必须成功: %v", err)
	}
	if settlement.Acknowledged < 1 {
		t.Fatalf("结算必须确认成功键: %+v", settlement)
	}
	// 结算失败透传分支：codex store 指向不存在 shard 目录。
	brokenCodex := &cleanuprepo.CodexContextStore{ShardRoot: filepath.Join(dir, "w13g8-missing-shards"), ShardCount: 1}
	brokenService := &familyDbService{codex: brokenCodex}
	if _, err := brokenService.CleanupExpiredCodexContextStates(ctx, "2100-01-01T00:00:00.000Z", 10); err == nil {
		t.Fatal("shard 目录缺失必须报错")
	}
	// ProcessBatch：删除不存在文件 → 失败条目走 warn 分支仍返回计数。
	processor := &codexStorageProcessor{db: family.dbService, store: family.codex, logger: assembly.logger}
	deleted, err := processor.ProcessBatch(ctx, []string{"w13g8/not-exist.bin"})
	if err != nil {
		t.Fatalf("缺失文件必须进入失败结算而非错误: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("缺失文件不得计为已删除: %d", deleted)
	}
	// CheckpointAfterDelete：对已关闭连接 checkpoint 必须报错。
	closed := openTestSQLite(t, filepath.Join(dir, "closed.sqlite3"))
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	checkpointer := &datasetCheckpointer{dataset: closed, usageCatalog: closed, stats: closed}
	if err := checkpointer.CheckpointAfterDelete(ctx); err == nil {
		t.Fatal("已关闭连接的 checkpoint 必须报错")
	}
}

// TestW13G8FlushLoopRetriesFailedJob 驱动 flushLoop 的失败重入队分支
// （非法任务类型使 runner 报错，任务保留队头）。
func TestW13G8FlushLoopRetriesFailedJob(t *testing.T) {
	dir := t.TempDir()
	seedDataRetention(t, dir)
	assembly, err := buildWorkerAssembly(retentionTestConfig(dir), slog.Default())
	if err != nil {
		t.Fatalf("build worker assembly: %v", err)
	}
	defer assembly.closeStores()
	family := assembly.retention
	queue := newRecordMaintenanceQueue(queueLimits{MaxItems: 10, MaxBytes: 1 << 20})
	stop := make(chan struct{})
	go family.flushLoop(stop, queue)
	if queued, _ := queue.enqueue(retention.RecordMaintenanceJob{Type: "w13g8-unknown-type"}); !queued {
		t.Fatal("空队列必须接受任务")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if queue.size() == 1 {
			// 失败任务已重入队（flushLoop 错误分支执行过至少一次）。
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if queue.size() != 1 {
		t.Fatalf("失败任务必须保留队头等待重试: size=%d", queue.size())
	}
	close(stop)
}

// TestW13G8BalanceRuntimeStoreErrorArms 驱动 balanceDetectRuntime 提交/启用
// 的围栏解析与执行错误分支。
func TestW13G8BalanceRuntimeStoreErrorArms(t *testing.T) {
	runtime, _, _ := wgNewBalanceRuntime(t, "", false)
	ctx := context.Background()
	// 缺 due 围栏。
	if _, err := runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{AccountID: "acc-w13g8"}); err == nil || !strings.Contains(err.Error(), "due 围栏") {
		t.Fatalf("缺围栏必须报错: %v", err)
	}
	// expected 围栏非法时间。
	bad := "w13g8-not-a-time"
	if _, err := runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{AccountID: "acc-w13g8", ExpectedNextRefreshAt: &bad}); err == nil {
		t.Fatal("非法围栏时间必须报错")
	}
	// next 非法时间。
	badNext := "w13g8-not-a-time"
	expect := "2026-09-18T00:00:00.000Z"
	if _, err := runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{AccountID: "acc-w13g8", ExpectedNextRefreshAt: &expect, NextRefreshAt: &badNext}); err == nil {
		t.Fatal("非法 next 时间必须报错")
	}
	// EnableDetectedQuery：next 非法 + fence 非法。
	if _, err := runtime.EnableDetectedQuery(ctx, opsjobs.BalanceEnableInput{AccountID: "acc-w13g8", ExpectedConfigRevision: 1, NextRefreshAt: badNext}); err == nil {
		t.Fatal("非法 next 必须报错")
	}
	if _, err := runtime.EnableDetectedQuery(ctx, opsjobs.BalanceEnableInput{AccountID: "acc-w13g8", ExpectedConfigRevision: 1, NextRefreshAt: expect, ExpectedNextRefreshAt: &bad}); err == nil {
		t.Fatal("非法 fence 必须报错")
	}
	// 底层连接关闭后 → 执行错误分支。
	if err := runtime.business.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{AccountID: "acc-w13g8", ExpectedNextRefreshAt: &expect}); err == nil {
		t.Fatal("连接关闭后必须报错")
	}
	if _, err := runtime.EnableDetectedQuery(ctx, opsjobs.BalanceEnableInput{AccountID: "acc-w13g8", ExpectedConfigRevision: 1, NextRefreshAt: expect}); err == nil {
		t.Fatal("连接关闭后必须报错")
	}
	if _, err := runtime.ReplaceSnapshotIfCurrent(ctx, opsjobs.BalanceSnapshotInput{AccountID: "acc-w13g8", ExpectedConfigRevision: 1}); err == nil {
		t.Fatal("连接关闭后必须报错")
	}
}

// TestW13G8CheckpointSQLiteNilAndClosed 覆盖 checkpointSQLite 的 nil 短路
// 与错误分支（成功分支由 retention round 测试覆盖）。
func TestW13G8CheckpointSQLiteNilAndClosed(t *testing.T) {
	if err := checkpointSQLite(context.Background(), nil); err != nil {
		t.Fatalf("nil db 必须短路成功: %v", err)
	}
	path := filepath.Join(t.TempDir(), "closed.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkpointSQLite(context.Background(), db); err == nil {
		t.Fatal("已关闭连接必须报错")
	}
}
