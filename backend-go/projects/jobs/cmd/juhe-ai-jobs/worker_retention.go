package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/cleanuprepo"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
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

	timezoneSource := family.timezoneSource(stats)
	shards := cleanuprepo.NewShardStore(a.config.UsageShardRoot)

	usageRecords := &cleanuprepo.UsageRecordsStore{Catalog: usageCatalog, Stats: stats, Shards: shards}
	statsRetention := &cleanuprepo.StatsRetentionStore{DB: stats}
	if !postgres {
		statsDB := stats.DB
		statsRetention.Checkpoint = func(ctx context.Context) error { return checkpointSQLite(ctx, statsDB) }
	}

	family.timezone = family.timezoneSource(stats)
	family.retentionStatsStore = statsRetention
	recordCleanup := &cleanuprepo.RecordCleanupStore{
		Dataset:        dataset,
		Stats:          stats,
		UsageCatalog:   usageCatalog,
		Business:       business,
		Shards:         shards,
		Now:            family.now,
		Timezone:       timezoneSource,
		DerivedWindows: nil,
		OnDerivedWindowsSkipped: func(reason string) {
			a.logger.Warn("record cleanup 跳过同步派生窗口刷新", "event", "retention_derived_windows_refresh_skipped", "reason", reason)
		},
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
		Timezone:     timezoneSource,
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
	dataRetentionJob.Settings = family.settingsSource()
	dataRetentionJob.Timezone = family.usageTimezoneSource()
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
		queue.drainShutdown(runner)
		if err := codexStore.Close(); err != nil {
			return err
		}
		return shards.Close()
	})
	go family.flushLoop(stopFlush, queue, runner)

	family.closeHandles = closeHandles
	a.retention = family
	return nil
}

func checkpointSQLite(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return nil
	}
	_, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE);")
	return err
}

// retentionFamily 持有家族内共享对象（供组合根测试/运维入口访问）。
type retentionFamily struct {
	assembly *workerAssembly
	postgres bool

	timezone            func(ctx context.Context) (*time.Location, error)
	retentionStatsStore *cleanuprepo.StatsRetentionStore

	recordCleanup *cleanuprepo.RecordCleanupStore
	codex         *cleanuprepo.CodexContextStore
	dbService     *familyDbService
	queue         *recordMaintenanceQueue
	runner        *retention.RecordMaintenanceRunner
	flushStop     chan struct{}
	closeHandles  []func() error
}

func (f *retentionFamily) now() time.Time { return time.Now() }

func (f *retentionFamily) retentionStats() *cleanuprepo.StatsRetentionStore {
	return f.retentionStatsStore
}

func (f *retentionFamily) timezoneSource(stats *cleanuprepo.DB) func(ctx context.Context) (*time.Location, error) {
	// Node usageStatsTimezone()：设置缺失时 fail closed；组合根暂以默认时区
	// Asia/Shanghai（Node DEFAULT usageStatsTimezone）解析。
	return func(ctx context.Context) (*time.Location, error) {
		return time.LoadLocation("Asia/Shanghai")
	}
}

func (f *retentionFamily) usageTimezoneSource() retention.TimezoneSource {
	return func(ctx context.Context) (string, error) {
		return "Asia/Shanghai", nil
	}
}

func (f *retentionFamily) settingsSource() retention.SettingsSource {
	// Node getSettingsAsync 读 system_settings；jobs 侧暂无设置读模型，
	// 以 Node DEFAULT_SYSTEM_SETTINGS 的保留策略默认值供策略加载
	// （与 staticSettings.number 同一边界，接入设置存储时只需替换）。
	return func(ctx context.Context) (map[string]any, error) {
		return map[string]any{
			"publicApiLogRetentionDays":        30,
			"usageRecordRetentionDays":         90,
			"usageStatsMinuteRetentionHours":   24,
			"usageStatsHourlyRetentionDays":    30,
			"usageStatsDailyRetentionDays":     180,
			"usageStatsWeeklyRetentionWeeks":   104,
			"usageStatsMonthlyRetentionMonths": 24,
			"usageRankSnapshotRetentionDays":   90,
			"systemMetricsRetentionDays":       7,
			"systemMetricsHourlyRetentionDays": 30,
		}, nil
	}
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
func (q *recordMaintenanceQueue) drainShutdown(runner *retention.RecordMaintenanceRunner) {
	for {
		batch := q.takeBatch(100)
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		for _, job := range batch {
			if _, err := runner.RunOnce(ctx, job); err != nil {
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
func (f *retentionFamily) flushLoop(stop <-chan struct{}, queue *recordMaintenanceQueue, runner *retention.RecordMaintenanceRunner) {
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
					if _, err := runner.RunOnce(ctx, job); err != nil {
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
