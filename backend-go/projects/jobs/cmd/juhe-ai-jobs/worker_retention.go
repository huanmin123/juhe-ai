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
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// wireRetentionFamily 把 retention/cleanup 家族的四个任务翻转为 GoWired：
// data-retention-cleanup、chat-retention-cleanup、expired-deleted-account-cleanup、
// api-key/account-record-cleanup-retry。仓储实现在 internal/cleanuprepo
// （Node backend/src/storage 清理侧的移植），组合根只做句柄打开与 port 适配。

const retentionFamilyBatchSize = retention.CleanupBatchSize

func (a *workerAssembly) wireRetentionFamily(ctx context.Context) error {
	if !a.config.RetentionEnabled {
		return nil
	}
	postgres := a.config.Driver == "postgres"
	family := &retentionFamily{assembly: a, postgres: postgres}

	var closeHandles []func() error
	openDual := func(postgresPath string, sqlitePath string, label string, schema string) (*cleanuprepo.DB, error) {
		if postgres {
			handle, err := a.acquirePool(a.config.PostgresURL, label)
			if err != nil {
				return nil, err
			}
			return &cleanuprepo.DB{DB: handle.DB(), Postgres: true}, nil
		}
		if strings.TrimSpace(sqlitePath) == "" {
			return nil, fmt.Errorf("启用 JUHE_AI_JOBS_RETENTION_ENABLED 后 SQLite 模式必须配置 %s", postgresPath)
		}
		db, err := a.openSQLite(sqlitePath, label)
		if err != nil {
			return nil, err
		}
		return &cleanuprepo.DB{DB: db}, nil
	}

	business, err := openDual("JUHE_AI_POSTGRES_URL", a.config.BusinessSQLitePath, "retention-business", "juhe_business")
	if err != nil {
		return err
	}
	stats, err := openDual("JUHE_AI_POSTGRES_URL", a.config.StatsSQLitePath, "retention-stats", "juhe_stats")
	if err != nil {
		return err
	}
	dataset, err := openDual("JUHE_AI_POSTGRES_URL", a.config.DatasetSQLitePath, "retention-dataset", "juhe_dataset")
	if err != nil {
		return err
	}
	chat, err := openDual("JUHE_AI_POSTGRES_URL", a.config.ChatSQLitePath, "retention-chat", "juhe_chat")
	if err != nil {
		return err
	}
	usageCatalog, err := openDual("JUHE_AI_POSTGRES_URL", a.config.UsageCatalogSQLitePath, "retention-usage-catalog", "juhe_usage")
	if err != nil {
		return err
	}

	shards := cleanuprepo.NewShardStore(a.config.UsageShardRoot)

	usageRecords := &cleanuprepo.UsageRecordsStore{Catalog: usageCatalog, Stats: stats, Shards: shards}
	statsRetention := &cleanuprepo.StatsRetentionStore{DB: stats}
	if !postgres {
		statsDB := stats.DB
		statsRetention.Checkpoint = func(ctx context.Context) error { return checkpointSQLite(ctx, statsDB) }
	}

	// D-45（BUG-0175）：数据保留的组合根设置/时区源接真实 system_settings
	// 读模型（business 库）。此前 timezone 固定 Asia/Shanghai、retention 策略
	// 固定一套与 DEFAULT_SYSTEM_SETTINGS 不一致的硬编码值（自述临时边界），
	// 运营改设置完全不生效。
	retentionSettings := newRetentionSettingsRuntime(business.DB, postgres, a.logger)
	family.timezone = retentionSettings.location
	family.usageTimezone = retentionSettings.timezoneName
	family.retentionSettings = retentionSettings

	family.retentionStatsStore = statsRetention
	recordCleanup := &cleanuprepo.RecordCleanupStore{
		Dataset:        dataset,
		Stats:          stats,
		UsageCatalog:   usageCatalog,
		Business:       business,
		Shards:         shards,
		Now:            family.now,
		Timezone:       retentionSettings.location,
		DerivedWindows: nil,
		OnDerivedWindowsSkipped: func(reason string) {
			a.logger.Warn("record cleanup 跳过同步派生窗口刷新", "event", "retention_derived_windows_refresh_skipped", "reason", reason)
		},
	}
	// 派生窗口同步重算接入（BUG-0175 D-48：DerivedWindowRefresher 组合根固定
	// nil → 仅 PG 清理路径保持 nil——Node PG cleanup 只写脏范围标记、由调度式
	// 窗口刷新收敛；SQLite 清理链接 statsagg 配额小时窗 + 排行快照重算，
	// 对齐 refreshDeletedApiKey/AccountDerivedWindowsIfNeeded 的刷新半区）。
	if !postgres {
		recordCleanup.DerivedWindows = &retentionDerivedWindows{refresher: &statsagg.WindowRefresher{
			DB:         stats.DB,
			BusinessDB: business.DB,
			Dialect:    statsagg.Dialect{},
			Clock:      &familyTimezoneClock{family},
			Now:        family.now,
		}}
	}
	family.recordCleanup = recordCleanup

	deletedAccounts := &cleanuprepo.DeletedAccountStore{
		Business:           business,
		Dataset:            dataset,
		Stats:              stats,
		Records:            recordCleanup,
		OrphanSweepEnabled: postgres,
		OnOrphanSweepSkipped: func(ctx context.Context, reason string) {
			a.logger.Warn("逻辑删除 AI 账户物理清理跳过孤儿授权实例扫尾",
				"event", "background_expired_deleted_account_orphan_sweep_skipped", "reason", reason)
		},
		Now: family.now,
	}

	codexStore := &cleanuprepo.CodexContextStore{
		Postgres: postgres,
		PG: func() *cleanuprepo.DB {
			if postgres {
				return usageCatalog
			}
			return nil
		}(),
		ShardRoot:  a.config.CodexContextStateShardRoot,
		ShardCount: a.config.CodexContextStateShardCount,
		Now:        family.now,
	}
	if postgres && codexStore.PG == nil {
		return fmt.Errorf("retention codex context PG 句柄未初始化")
	}
	if !postgres {
		if strings.TrimSpace(a.config.CodexContextStateShardRoot) == "" {
			return fmt.Errorf("启用 JUHE_AI_JOBS_RETENTION_ENABLED 后 SQLite 模式必须配置 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT")
		}
		if a.config.CodexContextStateShardCount < 1 {
			return fmt.Errorf("JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT 必须 >= 1")
		}
	}
	family.codex = codexStore

	publicApiLogs := &cleanuprepo.PublicApiLogsStore{DB: dataset}
	systemSessions := &cleanuprepo.SystemSessionsStore{DB: business}
	nonBusinessDataset := &cleanuprepo.NonBusinessDatasetStore{
		Dataset:      dataset,
		UsageCatalog: usageCatalog,
		Stats:        stats,
		Shards:       shards,
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
	runner := &retention.RecordMaintenanceRunner{
		Mode:   retention.ModeSQLite,
		Clock:  func() time.Time { return family.now() },
		Logger: a.logger,
		Executor: retention.RecordMaintenanceExecutor{
			RelatedRecords:  &familyRelatedCleaner{family: family},
			UsageRecords:    usageRecords,
			NonBusinessData: nonBusinessDataset,
			StatsWriter:     nil,
		},
	}
	// sqlite 模式统计写回调直连；postgres 模式 Runner 传 nil（Node 同语义）。
	if !postgres {
		runner.Executor.StatsWriter = &familyStatsWriter{family: family}
	}
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
	dataRetentionJob := retention.NewDataRetentionJob(
		func() retention.Mode {
			if postgres {
				return retention.ModePostgres
			}
			return retention.ModeSQLite
		}(), "worker", a.config.WorkerRole)
	dataRetentionJob.Logger = a.logger
	dataRetentionJob.Clock = func() time.Time { return family.now() }
	dataRetentionJob.Settings = retentionSettings.settings
	dataRetentionJob.Timezone = family.usageTimezone
	dataRetentionJob.PublicApiLogs = publicApiLogs
	dataRetentionJob.UsageRecords = usageRecords
	dataRetentionJob.Stats = &familyStatsWriter{family: family}
	dataRetentionJob.DB = dbService
	dataRetentionJob.Enqueuer = &queueEnqueuer{queue: queue}
	dataRetentionJob.CodexStorage = &codexStorageProcessor{db: dbService, store: codexStore, logger: a.logger}
	if !postgres {
		dataRetentionJob.Checkpointer = &datasetCheckpointer{dataset: dataset.DB, usageCatalog: usageCatalog.DB, stats: stats.DB}
	}

	a.scheduleWiredJob("data-retention-cleanup", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
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

	retryJob := &retention.RecordCleanupRetryJob{
		Mode: func() retention.Mode {
			if postgres {
				return retention.ModePostgres
			}
			return retention.ModeSQLite
		}(),
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
		if err := codexStore.Close(); err != nil {
			return err
		}
		return shards.Close()
	})
	go family.flushLoop(stopFlush, queue)

	// ---- record_maintenance_jobs 交接表 drain（gateway cleanup POST 持久通道） ----
	if err := a.wireRecordMaintenanceTableDrain(family, business, dataset); err != nil {
		return err
	}

	family.closeHandles = closeHandles
	a.retention = family
	return nil
}

// checkpointSQLite 照 Node checkpointSqliteWal（sqlite-maintenance.ts:8-23，
// D-70，BUG-0175）：PASSIVE checkpoint + PRAGMA optimize。Go 曾用 TRUNCATE——
// 它会等待所有读事务退出并截断 WAL 文件，把清理循环变成阻塞窗口；PASSIVE
// 尽力搬移而不等待，WAL 收缩交给优化器建议的时机。返回的行计数不消费（Node
// 仅用于日志字段，Go 侧保持删除计数日志不变）。
func checkpointSQLite(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return nil
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE);"); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, "PRAGMA optimize;")
	return err
}

// retentionFamily 持有家族内共享对象（供组合根测试/运维入口访问）。
type retentionFamily struct {
	assembly *workerAssembly
	postgres bool

	timezone func(ctx context.Context) (*time.Location, error)
	// usageTimezone 是字符串时区源（retention.TimezoneSource，D-45 接真实
	// system_settings）；retentionSettings 持有 settings/时区共用读模型。
	usageTimezone       retention.TimezoneSource
	retentionSettings   *retentionSettingsRuntime
	retentionStatsStore *cleanuprepo.StatsRetentionStore

	recordCleanup *cleanuprepo.RecordCleanupStore
	codex         *cleanuprepo.CodexContextStore
	dbService     *familyDbService
	queue         *recordMaintenanceQueue
	runner        *retention.RecordMaintenanceRunner
	// runMu 串行化 record-maintenance 执行面：本地队列 flushLoop 与
	// record_maintenance_jobs 表 drain 各自是单 goroutine，但共享同一
	// runner；Node flushing 标志保证同一时刻只有一轮 flush，这里以互斥
	// 保持同语义。
	runMu        sync.Mutex
	flushStop    chan struct{}
	closeHandles []func() error
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

// retentionSettingsRuntime 是数据保留组合根的 system_settings 读模型
// （D-45，BUG-0175；Node usageStatsTimezoneAsync + getSettingsAsync 的 Go
// 承接）：
//   - retention 策略数值经 jobssettings.Source（background-jobs settingsNumber
//     语义：缺行回落 DEFAULT_SYSTEM_SETTINGS、非法值任务失败；SQLite 缺表 /
//     PG 读失败按驱动语义回落默认并告警）；数值边界交给 retention.LoadPolicy
//     fail-closed 校验，这里只要求整数。
//   - usageStatsTimezone（字符串）沿用同一组失败语义：缺行走
//     DEFAULT_SYSTEM_SETTINGS 的 "UTC" 种子值；SQLite 缺表 / PG 读失败回落
//     默认并告警（sqliteBackgroundJobSettingValue /
//     postgresBackgroundJobSettingValue 等价）；非法 JSON / 非法时区任务
//     失败。60s 缓存对齐 usageStatsTimezoneCacheTtlMs。
type retentionSettingsRuntime struct {
	source *jobssettings.Source
	db     *sql.DB
	dbMode jobssettings.Mode
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
	"systemMetricsRetentionDays",
	"systemMetricsHourlyRetentionDays",
}

func newRetentionSettingsRuntime(business *sql.DB, postgres bool, logger *slog.Logger) *retentionSettingsRuntime {
	mode := jobssettings.SQLite
	if postgres {
		mode = jobssettings.Postgres
	}
	return &retentionSettingsRuntime{
		source: jobssettings.NewSource(jobssettings.Options{
			DB:   business,
			Mode: mode,
			Warn: jobssettingsWarn(logger),
		}),
		db:     business,
		dbMode: mode,
		warn:   jobssettingsWarn(logger),
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
// 的种子值（Node 播种的是 host timezone；部署配置时区后总是携带显式行，
// 静态回落 UTC 与 jobssettingsdefaults 同一约定）。
func defaultRetentionTimezone() string {
	if value, ok := jobssettings.DefaultSystemSettings["usageStatsTimezone"].(string); ok && value != "" {
		return value
	}
	return "UTC"
}

// readTimezoneSetting resolves the raw usageStatsTimezone setting with the
// jobssettings failure semantics.
func (r *retentionSettingsRuntime) readTimezoneSetting(ctx context.Context) (string, error) {
	query := `SELECT value_json FROM system_settings WHERE system_account_id = 'sys_admin' AND key = 'usageStatsTimezone' LIMIT 1`
	if r.dbMode == jobssettings.Postgres {
		query = `SELECT value_json FROM juhe_business.system_settings WHERE system_account_id = 'sys_admin' AND key = 'usageStatsTimezone' LIMIT 1`
	}
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
	if r.dbMode == jobssettings.Postgres {
		// postgresBackgroundJobSettingValue：快照刷新失败告警后保持默认。
		r.warnOnce("background_job_settings_snapshot_refresh_failed",
			"后台任务系统设置快照刷新失败，将临时使用默认设置", readErr)
		return defaultRetentionTimezone(), nil
	}
	if isMissingSystemSettingsTable(readErr) {
		// sqliteBackgroundJobSettingValue：启动早期设置表可能尚未初始化——
		// 一次告警后临时使用默认。
		r.warnOnce("background_job_settings_table_missing_default",
			"后台任务启动时系统设置表尚未初始化，将临时使用默认设置", readErr)
		return defaultRetentionTimezone(), nil
	}
	return "", fmt.Errorf("读取 usageStatsTimezone 失败: %w", readErr)
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

func isMissingSystemSettingsTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table: system_settings")
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
	if c.family.postgres {
		return c.family.recordCleanup.CleanupAPIKeyRelatedPostgres(ctx, job.APIKeyID, job.SystemAccountID)
	}
	return c.family.recordCleanup.CleanupAPIKeyRelatedSQLite(ctx, job.APIKeyID, job.SystemAccountID, statsWriter)
}

func (c *familyRelatedCleaner) CleanupAccountRelated(ctx context.Context, job retention.RecordMaintenanceJob, statsWriter retention.StatsWriter) (retention.RelatedCleanupResult, error) {
	if c.family.recordCleanup == nil {
		return retention.RelatedCleanupResult{}, fmt.Errorf("retention record cleanup store 未初始化")
	}
	if c.family.postgres {
		return c.family.recordCleanup.CleanupAccountRelatedPostgres(ctx, retention.ExpiredDeletedAccountTarget{
			AccountID:         job.AccountID,
			SystemAccountID:   job.SystemAccountID,
			RelatedAccountIDs: job.RelatedAccountIDs,
			AuthorizationIDs:  job.AuthorizationIDs,
			TeamScopeIDs:      job.TeamScopeIDs,
		})
	}
	return c.family.recordCleanup.CleanupAccountRelatedSQLite(ctx, retention.ExpiredDeletedAccountTarget{
		AccountID:         job.AccountID,
		SystemAccountID:   job.SystemAccountID,
		RelatedAccountIDs: job.RelatedAccountIDs,
		AuthorizationIDs:  job.AuthorizationIDs,
		TeamScopeIDs:      job.TeamScopeIDs,
	}, statsWriter)
}

type familyAPIKeyRetryer struct {
	family *retentionFamily
}

func (r *familyAPIKeyRetryer) CleanupPendingTargets(ctx context.Context, limit int, statsWriter retention.StatsWriter) (retention.PendingCleanupSummary, error) {
	if r.family.postgres {
		return r.family.recordCleanup.CleanupPendingAPIKeyTargetsPostgres(ctx, limit)
	}
	return r.family.recordCleanup.CleanupPendingAPIKeyTargets(ctx, limit, statsWriter)
}

type familyAccountRetryer struct {
	family *retentionFamily
}

func (r *familyAccountRetryer) CleanupPendingTargets(ctx context.Context, limit int, statsWriter retention.StatsWriter) (retention.PendingCleanupSummary, error) {
	if r.family.postgres {
		return r.family.recordCleanup.CleanupPendingAccountTargetsPostgres(ctx, limit)
	}
	return r.family.recordCleanup.CleanupPendingAccountTargets(ctx, limit, statsWriter)
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

func (w *familyStatsWriter) CleanupSystemMetricsRetention(ctx context.Context, input retention.SystemMetricsRetentionInput) (retention.SystemMetricsRetentionCounts, error) {
	return w.family.retentionStats().CleanupSystemMetricsRetention(ctx, input)
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

func (w *familyStatsWriter) UpsertAccountUsageSnapshots(ctx context.Context, inputs []retention.AccountUsageSnapshotUpsertInput) error {
	return fmt.Errorf("retention account usage snapshot upsert 未接线（归 J2/J3 探针域，cleanuprepo 不承担）")
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
}

func (p *codexStorageProcessor) ProcessBatch(ctx context.Context, storageKeys []string) (int64, error) {
	deleter := retention.NewCodexContextStorageProcessor(p.store.ShardRoot, nil, nil)
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

type datasetCheckpointer struct {
	dataset, usageCatalog, stats *sql.DB
}

func (c *datasetCheckpointer) CheckpointAfterDelete(ctx context.Context) error {
	for _, db := range []*sql.DB{c.dataset, c.usageCatalog, c.stats} {
		if err := checkpointSQLite(ctx, db); err != nil {
			return err
		}
	}
	return nil
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
	serialized, err := json.Marshal(job)
	if err != nil {
		return 4096
	}
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
						break
					}
				}
				cancel()
			}
		}
	}
}

// familyTimezoneClock 适配 family 时区解析为 statsagg.StatsTimezoneProvider。
type familyTimezoneClock struct {
	family *retentionFamily
}

func (c *familyTimezoneClock) StatsTimezone(ctx context.Context) (*time.Location, error) {
	return c.family.timezone(ctx)
}

// retentionDerivedWindows 适配 cleanuprepo.DerivedWindowRefresher：复用
// statsagg 的配额小时窗与排行快照刷新（与 stats 家族同一套实现，句柄来自
// retention 家族自己的 stats/business 连接，两个家族启用开关独立）。
type retentionDerivedWindows struct {
	refresher *statsagg.WindowRefresher
}

func (d *retentionDerivedWindows) RefreshQuotaHourlyWindows(ctx context.Context) error {
	_, err := d.refresher.RunQuotaHourlyWindows(ctx)
	return err
}

func (d *retentionDerivedWindows) RefreshRankSnapshots(ctx context.Context) error {
	_, err := d.refresher.RunStages(ctx, nil, statsagg.RefreshOptions{})
	return err
}
