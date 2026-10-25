package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/cleanuprepo"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobssettings"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// wireRetentionFamily 把 retention/cleanup 家族的四个任务翻转为 GoWired：
// data-retention-cleanup、chat-retention-cleanup、expired-deleted-account-cleanup、
// api-key/account-record-cleanup-retry。仓储实现在 internal/cleanuprepo
// （Node backend/src/storage 清理侧的移植），组合根只做句柄打开与 port 适配。

const retentionFamilyBatchSize = retention.CleanupBatchSize

func (a *workerAssembly) wireRetentionFamily(ctx context.Context) error {
	family := &retentionFamily{assembly: a}

	// PostgreSQL 唯一形态：六句柄（business/stats/dataset/chat/usage-catalog/
	// task-runs 保留清理）全部来自共享 URL 池，schema 限定由 cleanuprepo.DB
	// 的 Postgres 语义承担。原 sqlite 六库文件臂与 openDual 双模分派已随
	// sqlite 形态退役删除。
	openCleanupDB := func(label string) (*cleanuprepo.DB, error) {
		handle, err := a.acquirePool(a.config.PostgresURL, label)
		if err != nil {
			return nil, err
		}
		return &cleanuprepo.DB{DB: handle.DB(), Postgres: true}, nil
	}

	business, err := openCleanupDB("retention-business")
	if err != nil {
		return err
	}
	stats, err := openCleanupDB("retention-stats")
	if err != nil {
		return err
	}
	dataset, err := openCleanupDB("retention-dataset")
	if err != nil {
		return err
	}
	chat, err := openCleanupDB("retention-chat")
	if err != nil {
		return err
	}
	usageCatalog, err := openCleanupDB("retention-usage-catalog")
	if err != nil {
		return err
	}
	// 缺陷1修复：background_task_runs / background_job_leases 的保留清理句柄
	//（两表在 juhe_stats schema，与 stats 同池）。句柄进统一关闭链。
	taskRunsRetentionDB, err := openCleanupDB("retention-task-runs")
	if err != nil {
		return err
	}

	// usage-shards 清理消费面已随 sqlite 分片形态删除（D14 验证：PG 写侧落
	// juhe_usage 分区，分片文件只在 sqlite 形态存在，ShardStore 在 PG 装配
	// 无行为依赖）。
	usageRecords := &cleanuprepo.UsageRecordsStore{Catalog: usageCatalog, Stats: stats}
	statsRetention := &cleanuprepo.StatsRetentionStore{DB: stats}

	// D-45（BUG-0175）：数据保留的组合根设置/时区源接真实 system_settings
	// 读模型（business 库）。此前 timezone 固定 Asia/Shanghai、retention 策略
	// 固定一套与 DEFAULT_SYSTEM_SETTINGS 不一致的硬编码值（自述临时边界），
	// 运营改设置完全不生效。
	retentionSettings := newRetentionSettingsRuntime(business.DB, a.logger)
	family.timezone = retentionSettings.location
	family.usageTimezone = retentionSettings.timezoneName
	family.retentionSettings = retentionSettings

	family.retentionStatsStore = statsRetention
	family.taskRunsRetention = &cleanuprepo.TaskRunsRetentionStore{DB: taskRunsRetentionDB}
	recordCleanup := &cleanuprepo.RecordCleanupStore{
		Dataset:      dataset,
		Stats:        stats,
		UsageCatalog: usageCatalog,
		Business:     business,
		Now:          family.now,
		Timezone:     retentionSettings.location,
	}
	family.recordCleanup = recordCleanup

	// 孤儿授权实例扫尾内聚于 DeletedAccountStore（orphanSweep 按
	// Business.Postgres 分派，PG bulk 终态语义），组合根不持模式开关。
	deletedAccounts := &cleanuprepo.DeletedAccountStore{
		Business: business,
		Dataset:  dataset,
		Stats:    stats,
		Now:      family.now,
	}

	codexStore := &cleanuprepo.CodexContextStore{
		PG:  usageCatalog,
		Now: family.now,
	}
	if codexStore.PG == nil {
		return fmt.Errorf("retention codex context PG 句柄未初始化")
	}
	family.codex = codexStore

	publicApiLogs := &cleanuprepo.PublicApiLogsStore{DB: dataset}
	systemSessions := &cleanuprepo.SystemSessionsStore{DB: business}
	nonBusinessDataset := &cleanuprepo.NonBusinessDatasetStore{
		Dataset:      dataset,
		UsageCatalog: usageCatalog,
		Stats:        stats,
		UsageRecords: usageRecords,
		Timezone:     retentionSettings.location,
	}
	chatStore := &cleanuprepo.ChatStore{DB: chat, AssetsRoot: a.config.ChatAssetsRoot, Now: family.now}

	// 本地 record maintenance 队列（Node worker 本地队列 + flush 循环等价；
	// Redis Stream / IPC 分派按 Go 总设计消灭）。
	queue := newRecordMaintenanceQueue(queueLimits{
		MaxItems: a.config.RecordMaintenanceQueueMaxItems,
		MaxBytes: a.config.RecordMaintenanceQueueMaxMb * 1024 * 1024,
	})
	// statsWriter 恒暴露 Executor.StatsWriter（P0 修复语义固化：
	// account_usage_snapshot_upsert 通道要求该写入面恒接线）。
	runner := &retention.RecordMaintenanceRunner{
		Clock:  func() time.Time { return family.now() },
		Logger: a.logger,
		Executor: retention.RecordMaintenanceExecutor{
			RelatedRecords:  &familyRelatedCleaner{family: family},
			UsageRecords:    usageRecords,
			NonBusinessData: nonBusinessDataset,
			StatsWriter:     &familyStatsWriter{family: family},
		},
	}
	// StatsWriter 双模式恒接线（P0 修复：account_usage_snapshot_upsert 通道
	// 此前 PG 模式为 nil、SQLite 模式为 stub，两条 driver 的快照任务都会以
	// 「retention stats writer 未初始化」失败并占死 record_maintenance_jobs
	// drain 队头）。真实现是 cleanuprepo.RecordCleanupStore.
	// UpsertAccountUsageSnapshots（jobregistry 注册表登记 GoWired、owner=
	// cleanuprepo）；关联清理的 statsWriter 参数仍按 Node 语义由
	// familyRelatedCleaner 忽略（PG 清理链在事务内承担 stats 半区，不经该
	// 回调）。
	family.queue = queue
	family.runner = runner

	// DB service 端口适配（Node db-service 的 Go 单进程直连等价）。
	dbService := &familyDbService{
		family:         family,
		chat:           chatStore,
		systemSessions: systemSessions,
		codex:          codexStore,
		deleted:        deletedAccounts,
		logger:         a.logger,
	}
	family.dbService = dbService

	// ---- 任务登记 ----
	dataRetentionJob := retention.NewDataRetentionJob("worker", a.config.WorkerRole)
	dataRetentionJob.Logger = a.logger
	dataRetentionJob.Clock = func() time.Time { return family.now() }
	dataRetentionJob.Settings = retentionSettings.settings
	dataRetentionJob.Timezone = family.usageTimezone
	dataRetentionJob.PublicApiLogs = publicApiLogs
	dataRetentionJob.UsageRecords = usageRecords
	dataRetentionJob.Stats = &familyStatsWriter{family: family}
	dataRetentionJob.DB = dbService
	dataRetentionJob.Enqueuer = &queueEnqueuer{queue: queue}
	dataRetentionJob.CodexStorage = &codexStorageProcessor{db: dbService, store: codexStore, segmentRoot: a.config.CodexContextRoot, logger: a.logger}

	a.scheduleWiredJob("data-retention-cleanup", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		// 缺陷1修复：先执行 jobs 运行历史/租约保留清理，再跑既有数据保留任务；
		// 两半共用同一调度节拍（fixedDelay 10min、storage-maintenance lane、
		// 5min 超时与失败退避）。
		if err := family.cleanupTaskRunsRetention(taskCtx); err != nil {
			return jobsched.TaskResult{}, err
		}
		if _, err := dataRetentionJob.Run(taskCtx); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})

	chatRetentionJob := &retention.ChatRetentionJob{
		DB:            dbService,
		Clock:         func() time.Time { return family.now() },
		Logger:        a.logger,
		RetentionDays: a.config.ChatRetentionDays,
	}
	a.scheduleWiredJob("chat-retention-cleanup", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		if err := chatRetentionJob.Run(taskCtx); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})

	expiredJob := &retention.ExpiredDeletedAccountJob{
		DB:       dbService,
		Enqueuer: &queueEnqueuer{queue: queue},
		Clock:    func() time.Time { return family.now() },
		Logger:   a.logger,
	}
	a.scheduleWiredJob("expired-deleted-account-cleanup", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		if err := expiredJob.Run(taskCtx); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})

	// media-jobs-retention（媒体设计 §8.2，M2）：media_jobs TTL 保留清理，复用
	// retention-business 句柄（同业务库 PG 方言）；表缺席（老库未跑
	// maintenance 迁移）按零任务快速返回（job 内部判 IsMediaJobsTableMissing）。
	mediaJobsRetention := &retention.MediaJobsRetentionJob{
		Store:  &retention.MediaJobsSQLStore{DB: business.DB, Postgres: true},
		Clock:  func() time.Time { return family.now() },
		Logger: a.logger,
	}
	a.scheduleWiredJob("media-jobs-retention", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		if _, err := mediaJobsRetention.Run(taskCtx); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})

	retryJob := &retention.RecordCleanupRetryJob{
		Clock:   func() time.Time { return family.now() },
		Logger:  a.logger,
		APIKey:  &familyAPIKeyRetryer{family: family},
		Account: &familyAccountRetryer{family: family},
		Stats:   runner.Executor.StatsWriter,
	}
	a.scheduleWiredJob("api-key-record-cleanup-retry", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		if err := retryJob.RunAPIKey(taskCtx); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})
	a.scheduleWiredJob("account-record-cleanup-retry", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		if err := retryJob.RunAccount(taskCtx); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})

	// ---- 本地队列 flush 循环（supervisor 组件；Node flushTimer 等价） ----
	stopFlush := make(chan struct{})
	family.flushStop = stopFlush
	a.addCloser(func() error {
		close(stopFlush)
		queue.drainShutdown(family.runMaintenanceOnce)
		return nil
	})
	safego.Go("juheaijobs.retentionFlushLoop", func() { family.flushLoop(stopFlush, queue) })

	// ---- record_maintenance_jobs 交接表 drain（gateway cleanup POST 持久通道） ----
	if err := a.wireRecordMaintenanceTableDrain(family, dataset); err != nil {
		return err
	}

	a.retention = family
	return nil
}

// checkpointSQLite 已随 sqlite 形态退役删除（原 WAL PASSIVE checkpoint +
// PRAGMA optimize 只服务 sqlite 库文件的清理后收尾）。

// retentionFamily 持有家族内共享对象（供组合根测试/运维入口访问）。
type retentionFamily struct {
	assembly *workerAssembly

	timezone func(ctx context.Context) (*time.Location, error)
	// usageTimezone 是字符串时区源（retention.TimezoneSource，D-45 接真实
	// system_settings）；retentionSettings 持有 settings/时区共用读模型。
	usageTimezone       retention.TimezoneSource
	retentionSettings   *retentionSettingsRuntime
	retentionStatsStore *cleanuprepo.StatsRetentionStore
	// taskRunsRetention 是 background_task_runs / background_job_leases 的
	// 保留清理仓储（缺陷1修复；随 data-retention-cleanup 节拍执行）。
	taskRunsRetention *cleanuprepo.TaskRunsRetentionStore

	recordCleanup *cleanuprepo.RecordCleanupStore
	codex         *cleanuprepo.CodexContextStore
	dbService     *familyDbService
	queue         *recordMaintenanceQueue
	runner        *retention.RecordMaintenanceRunner
	// runMu 串行化 record-maintenance 执行面：本地队列 flushLoop 与
	// record_maintenance_jobs 表 drain 各自是单 goroutine，但共享同一
	// runner；Node flushing 标志保证同一时刻只有一轮 flush，这里以互斥
	// 保持同语义。
	runMu     sync.Mutex
	flushStop chan struct{}
}

func (f *retentionFamily) now() time.Time { return time.Now() }

// runMaintenanceOnce 是 record-maintenance 执行面的唯一入口：本地队列与
// record_maintenance_jobs 表 drain 共用，互斥保持 Node flushing 单飞语义。
func (f *retentionFamily) runMaintenanceOnce(ctx context.Context, job retention.RecordMaintenanceJob) (map[string]any, error) {
	f.runMu.Lock()
	defer f.runMu.Unlock()
	return f.runner.RunOnce(ctx, job)
}

// runMaintenanceSnapshotUpserts 是批量入口的同一互斥面：连续
// account_usage_snapshot_upsert 段一次 stats-writer 往返（D5，Node
// record-maintenance-queue.service.ts:846-869 合并语义）。
func (f *retentionFamily) runMaintenanceSnapshotUpserts(ctx context.Context, jobs []retention.RecordMaintenanceJob) (map[string]any, error) {
	f.runMu.Lock()
	defer f.runMu.Unlock()
	return f.runner.RunAccountUsageSnapshotUpserts(ctx, jobs)
}

func (f *retentionFamily) retentionStats() *cleanuprepo.StatsRetentionStore {
	return f.retentionStatsStore
}

// taskRunsRetentionDays 是 background_task_runs / background_job_leases 的
// 滚动保留窗口。此前两表全仓无删除路径（PG 模式 GoWired 任务每轮写运行
// 历史，1s 投影任务日增 8 万+ 行，表无限膨胀）。两表无聚合游标依赖，不挂
// 游标放行门；窗口深度取 30 天（原「gateway 侧只读近期样本」论证随
// 2026-10-14 监控改造删除，窗口值保持不变）。
const taskRunsRetentionDays = 30

// taskRunsRetentionBatchSize / taskRunsRetentionMaxPasses 是单轮清理的批次
// 上限：每批 5000 行、最多 200 批（单轮上限 100 万行），受任务 5min 超时与
// ctx 取消约束；未清完的行由下一轮 data-retention-cleanup 继续收敛。
const (
	taskRunsRetentionBatchSize = 5000
	taskRunsRetentionMaxPasses = 200
)

// cleanupTaskRunsRetention 执行一轮 jobs 运行历史/租约保留清理：
// background_task_runs 按 created_at、background_job_leases 按 lease_until，
// 共用同一 30 天滚动窗口，最旧优先、受批次上限约束。失败让本轮任务以错误
// 收场（走既有失败退避），不静默降级。
func (f *retentionFamily) cleanupTaskRunsRetention(ctx context.Context) error {
	store := f.taskRunsRetention
	if store == nil {
		return nil
	}
	// RFC3339 毫秒 UTC 文本，与 taskruns.FormatInstant 的列格式一致
	// （TEXT 列按字典序比较，同格式字符串与时间序等价）。
	cutoff := time.Now().UTC().AddDate(0, 0, -taskRunsRetentionDays).Format("2006-01-02T15:04:05.000Z07:00")
	var deletedRuns, deletedLeases int64
	for pass := 0; pass < taskRunsRetentionMaxPasses; pass++ {
		if ctx.Err() != nil {
			break
		}
		deleted, err := store.CleanupRunsBefore(ctx, cutoff, taskRunsRetentionBatchSize)
		if err != nil {
			return fmt.Errorf("清理 background_task_runs 失败: %w", err)
		}
		deletedRuns += deleted
		if deleted < taskRunsRetentionBatchSize {
			break
		}
	}
	for pass := 0; pass < taskRunsRetentionMaxPasses; pass++ {
		if ctx.Err() != nil {
			break
		}
		deleted, err := store.CleanupExpiredLeasesBefore(ctx, cutoff, taskRunsRetentionBatchSize)
		if err != nil {
			return fmt.Errorf("清理 background_job_leases 失败: %w", err)
		}
		deletedLeases += deleted
		if deleted < taskRunsRetentionBatchSize {
			break
		}
	}
	if deletedRuns > 0 || deletedLeases > 0 {
		f.assembly.logger.Info("后台任务运行历史保留清理完成",
			"event", "background_task_runs_retention_cleanup",
			"deletedTaskRuns", deletedRuns,
			"deletedLeases", deletedLeases,
			"retentionDays", taskRunsRetentionDays,
			"cutoff", cutoff)
	}
	return nil
}

// retentionSettingsRuntime 是数据保留组合根的 system_settings 读模型
// （D-45，BUG-0175；Node usageStatsTimezoneAsync + getSettingsAsync 的 Go
// 承接）：
//   - retention 策略数值经 jobssettings.Source（background-jobs settingsNumber
//     语义：缺行回落 DEFAULT_SYSTEM_SETTINGS、非法值任务失败；PG 读失败按
//     驱动语义回落默认并告警）；数值边界交给 retention.LoadPolicy
//     fail-closed 校验，这里只要求整数。
//   - usageStatsTimezone（字符串）沿用同一组失败语义：缺行走
//     DEFAULT_SYSTEM_SETTINGS 的 "Asia/Shanghai" 种子值；PG 读失败回落默认
//     并告警（postgresBackgroundJobSettingValue 等价）；非法 JSON / 非法时区
//     任务失败。60s 缓存对齐 usageStatsTimezoneCacheTtlMs。
type retentionSettingsRuntime struct {
	source *jobssettings.Source
	db     *sql.DB
	warn   jobssettings.WarnFunc

	mu          sync.Mutex
	tzValue     string
	tzExpiresAt time.Time
	tzWarned    bool
}

// retentionPolicySettingKeys 是 retention.LoadPolicy 消费的设置键
// （retention/policy.go Setting* 常量的镜像，保持组合根与策略解耦）。
var retentionPolicySettingKeys = []string{
	"publicApiLogRetentionDays",
	"usageRecordRetentionDays",
	"usageStatsMinuteRetentionHours",
	"usageStatsHourlyRetentionDays",
	"usageStatsDailyRetentionDays",
	"usageStatsWeeklyRetentionWeeks",
	"usageStatsMonthlyRetentionMonths",
	"usageRankSnapshotRetentionDays",
}

func newRetentionSettingsRuntime(business *sql.DB, logger *slog.Logger) *retentionSettingsRuntime {
	return &retentionSettingsRuntime{
		source: jobssettings.NewSource(jobssettings.Options{
			DB:   business,
			Mode: jobssettings.Postgres,
			Warn: jobssettingsWarn(logger),
		}),
		db:   business,
		warn: jobssettingsWarn(logger),
	}
}

// settings implements retention.SettingsSource.
func (r *retentionSettingsRuntime) settings(ctx context.Context) (map[string]any, error) {
	settings := make(map[string]any, len(retentionPolicySettingKeys))
	for _, key := range retentionPolicySettingKeys {
		// 全整数域读取：越界/非法由 retention.LoadPolicy 以 Node 同款错误
		// fail-closed，这里不做二次边界。
		value, err := r.source.Number(ctx, key, math.MinInt, math.MaxInt)
		if err != nil {
			return nil, err
		}
		settings[key] = value
	}
	return settings, nil
}

// timezoneName implements retention.TimezoneSource（usageStatsTimezoneAsync）。
func (r *retentionSettingsRuntime) timezoneName(ctx context.Context) (string, error) {
	r.mu.Lock()
	if r.tzValue != "" && time.Now().Before(r.tzExpiresAt) {
		value := r.tzValue
		r.mu.Unlock()
		return value, nil
	}
	r.mu.Unlock()

	timezone, err := r.readTimezoneSetting(ctx)
	if err != nil {
		return "", err
	}
	if _, locationErr := time.LoadLocation(timezone); locationErr != nil {
		return "", fmt.Errorf("统计时区不存在：%s", timezone)
	}
	r.mu.Lock()
	r.tzValue = timezone
	r.tzExpiresAt = time.Now().Add(retentionTimezoneCacheTTL)
	r.mu.Unlock()
	return timezone, nil
}

// retentionTimezoneCacheTTL mirrors usageStatsTimezoneCacheTtlMs
// (usage-stats-helpers.ts, 60_000).
const retentionTimezoneCacheTTL = 60 * time.Second

// defaultRetentionTimezone 取 DEFAULT_SYSTEM_SETTINGS 里 usageStatsTimezone
// 的种子值（与 maintenance 种子 pgSeedSystemSettings 的 "Asia/Shanghai" 同一
// 约定，seed_consistency_test.go 锁定；末位字面量仅在映射键被删时兜底，
// 同样保持种子值，避免回退时区再次漂移）。
func defaultRetentionTimezone() string {
	if value, ok := jobssettings.DefaultSystemSettings["usageStatsTimezone"].(string); ok && value != "" {
		return value
	}
	return "Asia/Shanghai"
}

// readTimezoneSetting resolves the raw usageStatsTimezone setting with the
// jobssettings failure semantics.
func (r *retentionSettingsRuntime) readTimezoneSetting(ctx context.Context) (string, error) {
	query := `SELECT value_json FROM juhe_business.system_settings WHERE system_account_id = 'sys_admin' AND key = 'usageStatsTimezone' LIMIT 1`
	var rawValue sql.NullString
	readErr := r.db.QueryRowContext(ctx, query).Scan(&rawValue)
	if readErr == nil || readErr == sql.ErrNoRows {
		if readErr == nil && rawValue.Valid && strings.TrimSpace(rawValue.String) != "" {
			var value string
			if err := json.Unmarshal([]byte(rawValue.String), &value); err != nil {
				return "", fmt.Errorf("系统设置 usageStatsTimezone 无效: %w", err)
			}
			if strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value), nil
			}
		}
		// 缺行走 DEFAULT_SYSTEM_SETTINGS 的种子值（schema 播种保证行存在，
		// 与 jobssettings.Source 的缺行回落一致）。
		return defaultRetentionTimezone(), nil
	}
	// postgresBackgroundJobSettingValue：快照刷新失败告警后保持默认。
	r.warnOnce("background_job_settings_snapshot_refresh_failed",
		"后台任务系统设置快照刷新失败，将临时使用默认设置", readErr)
	return defaultRetentionTimezone(), nil
}

func (r *retentionSettingsRuntime) warnOnce(event, message string, err error) {
	r.mu.Lock()
	alreadyWarned := r.tzWarned
	r.tzWarned = true
	r.mu.Unlock()
	if alreadyWarned || r.warn == nil {
		return
	}
	r.warn(event, map[string]any{"error": err.Error()}, message)
}

// location 是 *time.Location 视角的同一读模型（statsagg /
// cleanuprepo 的 StatsTimezone 消费面）。
func (r *retentionSettingsRuntime) location(ctx context.Context) (*time.Location, error) {
	timezone, err := r.timezoneName(ctx)
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("统计时区不存在：%s", timezone)
	}
	return location, nil
}

// ---- ports 适配 ----

// familyRelatedCleaner 把 record-maintenance runner 的关联清理分派回 family。
type familyRelatedCleaner struct {
	family *retentionFamily
}

func (c *familyRelatedCleaner) CleanupApiKeyRelated(ctx context.Context, job retention.RecordMaintenanceJob, statsWriter retention.StatsWriter) (retention.RelatedCleanupResult, error) {
	if c.family.recordCleanup == nil {
		return retention.RelatedCleanupResult{}, fmt.Errorf("retention record cleanup store 未初始化")
	}
	return c.family.recordCleanup.CleanupAPIKeyRelatedPostgres(ctx, job.APIKeyID, job.SystemAccountID)
}

func (c *familyRelatedCleaner) CleanupAccountRelated(ctx context.Context, job retention.RecordMaintenanceJob, statsWriter retention.StatsWriter) (retention.RelatedCleanupResult, error) {
	if c.family.recordCleanup == nil {
		return retention.RelatedCleanupResult{}, fmt.Errorf("retention record cleanup store 未初始化")
	}
	return c.family.recordCleanup.CleanupAccountRelatedPostgres(ctx, retention.ExpiredDeletedAccountTarget{
		AccountID:         job.AccountID,
		SystemAccountID:   job.SystemAccountID,
		RelatedAccountIDs: job.RelatedAccountIDs,
		AuthorizationIDs:  job.AuthorizationIDs,
		TeamScopeIDs:      job.TeamScopeIDs,
	})
}

type familyAPIKeyRetryer struct {
	family *retentionFamily
}

func (r *familyAPIKeyRetryer) CleanupPendingTargets(ctx context.Context, limit int, statsWriter retention.StatsWriter) (retention.PendingCleanupSummary, error) {
	return r.family.recordCleanup.CleanupPendingAPIKeyTargetsPostgres(ctx, limit)
}

type familyAccountRetryer struct {
	family *retentionFamily
}

func (r *familyAccountRetryer) CleanupPendingTargets(ctx context.Context, limit int, statsWriter retention.StatsWriter) (retention.PendingCleanupSummary, error) {
	return r.family.recordCleanup.CleanupPendingAccountTargetsPostgres(ctx, limit)
}

// familyStatsWriter 是 retention.StatsWriter 的清理侧实现（非业务数据 stats 半区
// 与记录清理扣减结算；usage stats/metrics retention 走 StatsRetentionStore）。
type familyStatsWriter struct {
	family *retentionFamily
}

func (w *familyStatsWriter) store() *cleanuprepo.StatsRetentionStore { return nil }

func (w *familyStatsWriter) CleanupUsageStatsRetention(ctx context.Context, input retention.UsageStatsRetentionInput) (retention.UsageStatsRetentionCounts, error) {
	return w.family.retentionStats().CleanupUsageStatsRetention(ctx, input)
}

func (w *familyStatsWriter) CleanupNonBusinessStatsData(ctx context.Context, cutoffAt string, limit int) (retention.NonBusinessDataCleanupCounts, error) {
	location, err := w.family.timezone(ctx)
	if err != nil {
		return retention.NonBusinessDataCleanupCounts{}, err
	}
	return w.family.retentionStats().CleanupNonBusinessStatsData(ctx, cutoffAt, limit, location)
}

func (w *familyStatsWriter) CleanupDeletedApiKeyRecordStats(ctx context.Context, input retention.DeletedApiKeyRecordStatsCleanupInput) error {
	location, err := w.family.timezone(ctx)
	if err != nil {
		return err
	}
	return w.family.recordCleanup.CleanupAPIKeyRecordStatsData(ctx, input.Target, input.Rows, input.UpdatedAt, input.ShardDeleted, location)
}

func (w *familyStatsWriter) CleanupDeletedAccountRecordStats(ctx context.Context, input retention.DeletedAccountRecordStatsCleanupInput) error {
	location, err := w.family.timezone(ctx)
	if err != nil {
		return err
	}
	return w.family.recordCleanup.CleanupAccountRecordStatsData(ctx, input.Target, input.Rows, input.UpdatedAt, input.ShardDeleted, location)
}

// UpsertAccountUsageSnapshots 接真实现：cleanuprepo.RecordCleanupStore.
// UpsertAccountUsageSnapshots（owners 归属查 business accounts，Stats 事务
// 批量 upsert account_usage_snapshots）。输入类型与 retention port 共用
// retention.AccountUsageSnapshotUpsertInput，适配器只绑定 business 句柄；
// recordCleanup 未装配时按 executor-missing 契约显式报错（不静默成功）。
func (w *familyStatsWriter) UpsertAccountUsageSnapshots(ctx context.Context, inputs []retention.AccountUsageSnapshotUpsertInput) error {
	if w.family.recordCleanup == nil {
		return fmt.Errorf("retention record cleanup store 未初始化")
	}
	return w.family.recordCleanup.UpsertAccountUsageSnapshots(ctx, w.family.recordCleanup.Business, inputs)
}

// familyDbService 适配 retention.DbService。
type familyDbService struct {
	family         *retentionFamily
	chat           *cleanuprepo.ChatStore
	systemSessions *cleanuprepo.SystemSessionsStore
	codex          *cleanuprepo.CodexContextStore
	deleted        *cleanuprepo.DeletedAccountStore
	logger         interface{ Warn(msg string, args ...any) }
}

func (d *familyDbService) CleanupChatRetention(ctx context.Context, input retention.ChatRetentionInput) (*retention.ChatRetentionResult, error) {
	return d.chat.CleanupRetention(ctx, input)
}

func (d *familyDbService) CleanupExpiredSystemSessions(ctx context.Context, expiredBefore string, limit int) (int64, error) {
	return d.systemSessions.CleanupExpired(ctx, expiredBefore, limit)
}

func (d *familyDbService) CleanupExpiredCodexContextStates(ctx context.Context, expiredBefore string, limit int) (*retention.CodexContextExpiredCleanup, error) {
	result, err := d.codex.CleanupExpiredStates(ctx, expiredBefore, limit)
	if err != nil || result == nil {
		return nil, err
	}
	return &retention.CodexContextExpiredCleanup{
		DeletedSessions:  result.DeletedSessions,
		DeletedResponses: result.DeletedResponses,
		DeletedCompacts:  result.DeletedCompacts,
		StorageKeys:      result.StorageKeys,
		HasMore:          result.HasMore,
	}, nil
}

func (d *familyDbService) SettleCodexContextStorageCleanup(ctx context.Context, settlement retention.CodexContextSettlement) (retention.CodexContextSettlementResult, error) {
	failures := make([]cleanuprepo.SettlementFailure, 0, len(settlement.Failures))
	for _, failure := range settlement.Failures {
		failures = append(failures, cleanuprepo.SettlementFailure{StorageKey: failure.StorageKey, Error: failure.Error})
	}
	result, err := d.codex.SettleStorageCleanup(ctx, cleanuprepo.Settlement{
		SucceededStorageKeys: settlement.SucceededStorageKeys,
		Failures:             failures,
		Now:                  settlement.Now,
	})
	if err != nil {
		return retention.CodexContextSettlementResult{}, err
	}
	return retention.CodexContextSettlementResult{Acknowledged: result.Acknowledged, Deferred: result.Deferred}, nil
}

func (d *familyDbService) CleanupExpiredDeletedAccounts(ctx context.Context) (*retention.ExpiredDeletedAccountSummary, error) {
	return d.deleted.CleanupExpired(ctx)
}

// codexStorageProcessor 复用 retention.FilesystemKeyDeleter 的文件删除 +
// DB service 结算（Node processCodexContextStorageCleanupBatch 等价）。
type codexStorageProcessor struct {
	db     retention.DbService
	store  *cleanuprepo.CodexContextStore
	logger interface{ Warn(msg string, args ...any) }
	// segmentRoot 是 codex-context segments 根（JUHE_AI_CODEX_CONTEXT_ROOT，
	// gateway runtime.go 同名 env 同派生默认）。storageKey 是相对该根的路径
	// （sessions/<safe>/segments/<hour>.json.gz，gateway
	// gatewaycodex.SegmentStorageKey），不能用 store.ShardRoot（state-shards
	// 根）——根错位时删除永远 miss 并按「不存在=成功」结算，segment 文件
	// 永不清理（存储泄漏）。
	segmentRoot string
}

func (p *codexStorageProcessor) ProcessBatch(ctx context.Context, storageKeys []string) (int64, error) {
	deleter := retention.NewCodexContextStorageProcessor(p.segmentRoot, nil, nil)
	deletion, err := deleter.Deleter.DeleteStorageKeys(ctx, storageKeys)
	if err != nil {
		return 0, err
	}
	if _, err := p.db.SettleCodexContextStorageCleanup(ctx, retention.CodexContextSettlement{
		SucceededStorageKeys: deletion.SucceededStorageKeys,
		Failures:             deletion.Failures,
	}); err != nil {
		return deletion.Deleted, err
	}
	if len(deletion.Failures) > 0 {
		p.logger.Warn("Codex Context 状态文件删除失败，已持久化等待重试",
			"event", "codex_context_storage_cleanup_deferred",
			"failedCount", len(deletion.Failures))
	}
	return deletion.Deleted, nil
}

// ---- 本地 record maintenance 队列 ----

type queueLimits struct {
	MaxItems int
	MaxBytes int
}

type queuedMaintenanceJob struct {
	job   retention.RecordMaintenanceJob
	bytes int
}

// recordMaintenanceQueue 照 Node pendingJobs 本地队列（bounded、drop 显式）。
type recordMaintenanceQueue struct {
	limits queueLimits
	mutex  sync.Mutex
	jobs   []queuedMaintenanceJob
	bytes  int
}

func newRecordMaintenanceQueue(limits queueLimits) *recordMaintenanceQueue {
	if limits.MaxItems <= 0 {
		limits.MaxItems = 5000
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = 32 * 1024 * 1024
	}
	return &recordMaintenanceQueue{limits: limits}
}

func estimateJobBytes(job retention.RecordMaintenanceJob) int {
	// w14j：RecordMaintenanceJob 为纯数据 struct，json.Marshal 不可能失败，
	// 原 err 回退分支不可达，按覆盖率清理授权删除。
	serialized, _ := json.Marshal(job)
	return len(serialized)
}

func (q *recordMaintenanceQueue) enqueue(job retention.RecordMaintenanceJob) (bool, string) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	item := queuedMaintenanceJob{job: job, bytes: estimateJobBytes(job)}
	if item.bytes > q.limits.MaxBytes {
		return false, "oversize"
	}
	if len(q.jobs) >= q.limits.MaxItems || q.bytes+item.bytes > q.limits.MaxBytes {
		return false, "worker_local_queue_full"
	}
	q.jobs = append(q.jobs, item)
	q.bytes += item.bytes
	return true, ""
}

func (q *recordMaintenanceQueue) takeBatch(max int) []retention.RecordMaintenanceJob {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	count := len(q.jobs)
	if count > max {
		count = max
	}
	batch := make([]retention.RecordMaintenanceJob, 0, count)
	for index := 0; index < count; index++ {
		batch = append(batch, q.jobs[index].job)
		q.bytes -= q.jobs[index].bytes
	}
	q.jobs = q.jobs[count:]
	return batch
}

func (q *recordMaintenanceQueue) size() int {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	return len(q.jobs)
}

// drainShutdown 停机排空：尽力执行完剩余任务（Node flushRecordMaintenanceQueueForShutdown）。
func (q *recordMaintenanceQueue) drainShutdown(run func(context.Context, retention.RecordMaintenanceJob) (map[string]any, error)) {
	for {
		batch := q.takeBatch(100)
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		for _, job := range batch {
			if _, err := run(ctx, job); err != nil {
				cancel()
				return
			}
		}
		cancel()
	}
}

// queueEnqueuer 适配 retention.RecordMaintenanceEnqueuer（本地队列路径）。
type queueEnqueuer struct {
	queue *recordMaintenanceQueue
}

func (e *queueEnqueuer) Enqueue(ctx context.Context, job retention.RecordMaintenanceJob) retention.EnqueueResult {
	queued, droppedReason := e.queue.enqueue(job)
	return retention.EnqueueResult{Queued: queued, DroppedReason: droppedReason}
}

func (e *queueEnqueuer) EnqueueAsync(ctx context.Context, job retention.RecordMaintenanceJob) error {
	queued, droppedReason := e.queue.enqueue(job)
	if !queued {
		return fmt.Errorf("数据维护任务投递失败：%s", droppedReason)
	}
	return nil
}

// flushLoop 照 flushRecordMaintenanceQueue 的定时循环（100ms 节拍）。
func (f *retentionFamily) flushLoop(stop <-chan struct{}, queue *recordMaintenanceQueue) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		drain:
			for {
				batch := queue.takeBatch(10)
				if len(batch) == 0 {
					break
				}
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				for _, job := range batch {
					if _, err := f.runMaintenanceOnce(ctx, job); err != nil {
						f.assembly.logger.Error("数据维护队列执行失败，已保留任务等待重试",
							"event", "record_maintenance_queue_flush_failed",
							"jobType", job.Type, "jobId", job.ID, "error", err)
						// Node：失败任务保留队头等待重试——这里重新入队。
						_, _ = queue.enqueue(job)
						// w16j 发现的失败重试热旋：原 break 只跳出批次循环，
						// 外层 drain 立即取回同一失败任务，在单个 tick 内零延迟
						// 无限重试且永不回到 select 观察 stop（CPU 空转 + 日志
						// 洪水，goroutine 随宿主进程永久泄漏）。改为跳出 drain，
						// 等下一个 100ms tick 再重试，与上方“定时循环（100ms
						// 节拍）”语义一致。
						cancel()
						break drain
					}
				}
				cancel()
			}
		}
	}
}

// familyTimezoneClock 已随 sqlite 派生窗口刷新臂删除（DerivedWindows 同批移除）。
