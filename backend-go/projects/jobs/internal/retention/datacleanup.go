package retention

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// DataRetentionJob mirrors runDataRetentionCleanup and its two halves:
// cleanupExpiredRetainedData (single-node sqlite chain) and
// enqueuePostgresDataRetentionMaintenanceJobs (high-performance dispatch).
//
// The publicApiLogs / usageRecords / stats / metrics / sessions / codex
// stages run in the Node order, share the single batch rhythm
// (batchSize=1000, maxBatches=20, pause=25ms) and stay behind per-domain
// ports so a slow or failing store never blocks another domain's job.
type DataRetentionJob struct {
	Mode        Mode
	ProcessRole string
	WorkerRole  string

	Settings SettingsSource
	Timezone TimezoneSource
	Clock    Clock
	Sleep    Sleeper
	Logger   *slog.Logger

	PublicApiLogs PublicApiLogsCleaner
	UsageRecords  UsageRecordsCleaner
	Stats         StatsWriter
	DB            DbService
	Enqueuer      RecordMaintenanceEnqueuer
	CodexStorage  CodexContextStorageBatchProcessor
	Checkpointer  DatasetCheckpointer

	sqliteRunning   bool
	postgresRunning bool
	runningMutex    sync.Mutex
}

// CodexContextStorageBatchProcessor mirrors processCodexContextStorageCleanupBatch.
type CodexContextStorageBatchProcessor interface {
	ProcessBatch(ctx context.Context, storageKeys []string) (int64, error)
}

// NewDataRetentionJob builds the job; now/sleep/logger fall back to real
// time, the fixed pause and the default logger.
func NewDataRetentionJob(mode Mode, processRole, workerRole string) *DataRetentionJob {
	return &DataRetentionJob{
		Mode:        mode,
		ProcessRole: processRole,
		WorkerRole:  workerRole,
	}
}

// Run mirrors runDataRetentionCleanup: postgres dispatches the PostgreSQL
// maintenance jobs, sqlite runs the in-process cleanup chain.
func (j *DataRetentionJob) Run(ctx context.Context) (CleanupResult, error) {
	if err := ctx.Err(); err != nil {
		return EmptyCleanupResult(), err
	}
	if j.Mode == ModePostgres {
		if err := j.EnqueuePostgresMaintenanceJobs(ctx); err != nil {
			return EmptyCleanupResult(), err
		}
		return EmptyCleanupResult(), nil
	}
	return j.CleanupExpiredRetainedData(ctx)
}

func (j *DataRetentionJob) logger() *slog.Logger {
	if j.Logger != nil {
		return j.Logger
	}
	return slog.Default()
}

func (j *DataRetentionJob) clock() time.Time {
	if j.Clock != nil {
		return j.Clock()
	}
	return time.Now()
}

func (j *DataRetentionJob) sleep(ctx context.Context) error {
	if j.Sleep == nil {
		return pauseCleanupBatch(ctx, CleanupBatchPause)
	}
	return j.Sleep(ctx)
}

func (j *DataRetentionJob) begin(stage string) bool {
	j.runningMutex.Lock()
	defer j.runningMutex.Unlock()
	if stage == "postgres" {
		if j.postgresRunning {
			return false
		}
		j.postgresRunning = true
		return true
	}
	if j.sqliteRunning {
		return false
	}
	j.sqliteRunning = true
	return true
}

func (j *DataRetentionJob) end(stage string) {
	j.runningMutex.Lock()
	defer j.runningMutex.Unlock()
	if stage == "postgres" {
		j.postgresRunning = false
		return
	}
	j.sqliteRunning = false
}

// CleanupExpiredRetainedData mirrors cleanupExpiredRetainedData. A second
// concurrent invocation returns an empty result instead of queueing behind
// the running one; postgres mode is rejected outright.
func (j *DataRetentionJob) CleanupExpiredRetainedData(ctx context.Context) (CleanupResult, error) {
	if err := ctx.Err(); err != nil {
		return EmptyCleanupResult(), err
	}
	if j.ProcessRole != "worker" {
		return EmptyCleanupResult(), nil
	}
	if !j.begin("sqlite") {
		return EmptyCleanupResult(), nil
	}
	defer j.end("sqlite")
	if j.Mode == ModePostgres {
		return EmptyCleanupResult(), errors.New("高性能模式禁止运行单机数据保留清理 worker；请使用 PostgreSQL 数据维护任务清理非业务数据，禁止静默跳过或回落 SQLite 清理链路")
	}

	result, err := j.runSQLiteStages(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return result, err
		}
		j.logger().Error("数据保留清理失败", "event", "data_retention_cleanup_failed", "error", err)
		return result, err
	}
	return result, nil
}

func (j *DataRetentionJob) runSQLiteStages(ctx context.Context) (CleanupResult, error) {
	settings, err := j.loadSettings(ctx)
	if err != nil {
		return EmptyCleanupResult(), err
	}
	timezone, err := j.loadTimezone(ctx)
	if err != nil {
		return EmptyCleanupResult(), err
	}
	policy, err := LoadPolicy(settings)
	if err != nil {
		return EmptyCleanupResult(), err
	}
	nowMillis := j.clock().UnixMilli()
	batchSize := CleanupBatchSize
	maxBatches := CleanupMaxBatchesPerRun

	result := EmptyCleanupResult()
	if j.WorkerRole == "ingest-worker" {
		dataset, err := j.cleanupDatasetAndUsage(ctx, nowMillis, policy, batchSize, maxBatches)
		result.Add(dataset)
		if err != nil {
			return result, err
		}
		if dataset.Sum() > 0 {
			j.checkpointAfterDelete(ctx)
		}

		statsLocation, err := LoadUsageStatsTimezone(timezone)
		if err != nil {
			return result, err
		}
		if err := j.cleanupRetentionInBatches(ctx, &result, maxBatches, func(ctx context.Context) (UsageStatsRetentionCounts, error) {
			return j.stats().CleanupUsageStatsRetention(ctx, usageStatsRetentionInput(nowMillis, policy, statsLocation, batchSize))
		}); err != nil {
			return result, err
		}
		if err := j.cleanupRetentionInBatchesSystemMetrics(ctx, &result, maxBatches, func(ctx context.Context) (SystemMetricsRetentionCounts, error) {
			return j.stats().CleanupSystemMetricsRetention(ctx, systemMetricsRetentionInput(nowMillis, policy, statsLocation, batchSize))
		}); err != nil {
			return result, err
		}
		systemSessions, err := j.cleanupInBatches(ctx, batchSize, maxBatches, func(ctx context.Context) (int64, error) {
			return j.db().CleanupExpiredSystemSessions(ctx, ISOString(time.UnixMilli(nowMillis)), batchSize)
		})
		result.SystemSessions += systemSessions
		if err != nil {
			return result, err
		}
		if err := j.cleanupCodexContextStatesInBatches(ctx, &result, ISOString(time.UnixMilli(nowMillis)), batchSize, maxBatches); err != nil {
			return result, err
		}
	}

	logger := j.logger()
	logger.Info("数据保留清理完成",
		append([]any{
			"event", "data_retention_cleanup_completed",
			"batchSize", batchSize,
			"maxBatches", maxBatches,
			"workerRole", j.WorkerRole,
		}, result.Values()...)...)
	return result, nil
}

func (j *DataRetentionJob) loadSettings(ctx context.Context) (map[string]any, error) {
	if j.Settings == nil {
		return nil, errors.New("retention settings source 未初始化")
	}
	settings, err := j.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if settings == nil {
		return nil, errors.New("retention settings source 未初始化")
	}
	return settings, nil
}

func (j *DataRetentionJob) loadTimezone(ctx context.Context) (string, error) {
	if j.Timezone == nil {
		return "", errors.New("系统设置缺少 usageStatsTimezone")
	}
	timezone, err := j.Timezone(ctx)
	if err != nil {
		return "", err
	}
	if timezone == "" {
		return "", errors.New("系统设置缺少 usageStatsTimezone")
	}
	return timezone, nil
}

func (j *DataRetentionJob) stats() StatsWriter {
	if j.Stats == nil {
		return missingStatsWriter{}
	}
	return j.Stats
}

func (j *DataRetentionJob) publicApiLogs() PublicApiLogsCleaner {
	if j.PublicApiLogs == nil {
		return missingPublicApiLogs{}
	}
	return j.PublicApiLogs
}

func (j *DataRetentionJob) usageRecords() UsageRecordsCleaner {
	if j.UsageRecords == nil {
		return missingUsageRecords{}
	}
	return j.UsageRecords
}

type missingPublicApiLogs struct{}

func (missingPublicApiLogs) CleanupBefore(context.Context, string, int) (int64, error) {
	return 0, errors.New("retention public API logs cleaner 未初始化")
}

type missingUsageRecords struct{}

func (missingUsageRecords) CleanupProcessedBefore(context.Context, string, int) (UsageRecordsBatch, error) {
	return UsageRecordsBatch{}, errors.New("retention usage records cleaner 未初始化")
}

func (j *DataRetentionJob) db() DbService {
	if j.DB == nil {
		return missingDbService{}
	}
	return j.DB
}

type missingStatsWriter struct{}

func (missingStatsWriter) CleanupUsageStatsRetention(context.Context, UsageStatsRetentionInput) (UsageStatsRetentionCounts, error) {
	return UsageStatsRetentionCounts{}, errors.New("retention stats writer 未初始化")
}

func (missingStatsWriter) CleanupSystemMetricsRetention(context.Context, SystemMetricsRetentionInput) (SystemMetricsRetentionCounts, error) {
	return SystemMetricsRetentionCounts{}, errors.New("retention stats writer 未初始化")
}

func (missingStatsWriter) CleanupNonBusinessStatsData(context.Context, string, int) (NonBusinessDataCleanupCounts, error) {
	return NonBusinessDataCleanupCounts{}, errors.New("retention stats writer 未初始化")
}

func (missingStatsWriter) CleanupDeletedApiKeyRecordStats(context.Context, DeletedApiKeyRecordStatsCleanupInput) error {
	return errors.New("retention stats writer 未初始化")
}

func (missingStatsWriter) CleanupDeletedAccountRecordStats(context.Context, DeletedAccountRecordStatsCleanupInput) error {
	return errors.New("retention stats writer 未初始化")
}

func (missingStatsWriter) UpsertAccountUsageSnapshots(context.Context, []AccountUsageSnapshotUpsertInput) error {
	return errors.New("retention stats writer 未初始化")
}

type missingDbService struct{}

func (missingDbService) CleanupChatRetention(context.Context, ChatRetentionInput) (*ChatRetentionResult, error) {
	return nil, errors.New("retention db service 未初始化")
}

func (missingDbService) CleanupExpiredSystemSessions(context.Context, string, int) (int64, error) {
	return 0, errors.New("retention db service 未初始化")
}

func (missingDbService) CleanupExpiredCodexContextStates(context.Context, string, int) (*CodexContextExpiredCleanup, error) {
	return nil, errors.New("retention db service 未初始化")
}

func (missingDbService) SettleCodexContextStorageCleanup(context.Context, CodexContextSettlement) (CodexContextSettlementResult, error) {
	return CodexContextSettlementResult{}, errors.New("retention db service 未初始化")
}

func (missingDbService) CleanupExpiredDeletedAccounts(context.Context) (*ExpiredDeletedAccountSummary, error) {
	return nil, errors.New("retention db service 未初始化")
}

// cleanupDatasetAndUsage mirrors cleanupDatasetAndUsageRetainedData: the
// publicApiLogs stage followed by the usage-records stage, separated by
// yields and abort checks.
func (j *DataRetentionJob) cleanupDatasetAndUsage(ctx context.Context, nowMillis int64, policy Policy, batchSize, maxBatches int) (CleanupResult, error) {
	result := EmptyCleanupResult()
	publicApiLogs, err := j.cleanupInBatches(ctx, batchSize, maxBatches, func(ctx context.Context) (int64, error) {
		return j.publicApiLogs().CleanupBefore(ctx, cutoffISO(nowMillis, policy.PublicApiLogDays), batchSize)
	})
	if err != nil {
		return result, err
	}
	result.PublicApiLogs = publicApiLogs

	usageRecords, err := j.cleanupProcessedUsageRecordsInBatches(ctx, cutoffISO(nowMillis, policy.UsageRecordDays), batchSize, maxBatches)
	result.UsageRecords += usageRecords.deletedRows
	if err != nil {
		return result, err
	}
	if usageRecords.blockedReason != "" {
		j.logger().Warn("使用记录保留清理被统计安全游标拦截",
			"event", "data_retention_usage_records_cleanup_blocked",
			"blockedReason", usageRecords.blockedReason,
			"cutoffCreatedAt", usageRecords.cutoffCreatedAt,
			"deletedRows", usageRecords.deletedRows,
			"batches", usageRecords.batches,
		)
	}
	return result, nil
}

type processedUsageRecordsCleanup struct {
	cutoffCreatedAt string
	deletedRows     int64
	batches         int
	blockedReason   string
}

// cleanupProcessedUsageRecordsInBatches mirrors
// cleanupProcessedUsageRecordsInBatches: the latest blocked reason wins, a
// blocked or non-full batch ends the loop.
func (j *DataRetentionJob) cleanupProcessedUsageRecordsInBatches(ctx context.Context, cutoffCreatedAt string, batchSize, maxBatches int) (processedUsageRecordsCleanup, error) {
	outcome := processedUsageRecordsCleanup{cutoffCreatedAt: cutoffCreatedAt}
	for index := 0; index < maxBatches; index++ {
		if err := ctx.Err(); err != nil {
			return outcome, err
		}
		batch, err := j.usageRecords().CleanupProcessedBefore(ctx, cutoffCreatedAt, batchSize)
		if err != nil {
			return outcome, err
		}
		outcome.deletedRows += batch.DeletedRows
		if batch.BlockedReason != "" {
			outcome.blockedReason = batch.BlockedReason
		}
		if batch.DeletedRows > 0 {
			outcome.batches++
		}
		if err := yieldToEventLoop(ctx); err != nil {
			return outcome, err
		}
		if err := ctx.Err(); err != nil {
			return outcome, err
		}
		if batch.BlockedReason != "" || batch.DeletedRows < int64(batchSize) || !batch.HasMore {
			return outcome, nil
		}
		if err := j.sleep(ctx); err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

// cleanupInBatches mirrors cleanupInBatches over a single-number cleanup
// (publicApiLogs, expired system sessions): a non-full batch ends the run.
func (j *DataRetentionJob) cleanupInBatches(ctx context.Context, batchSize, maxBatches int, cleanupBatch func(ctx context.Context) (int64, error)) (int64, error) {
	var total int64
	for index := 0; index < maxBatches; index++ {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		deleted, err := cleanupBatch(ctx)
		if err != nil {
			return total, err
		}
		total += deleted
		if err := yieldToEventLoop(ctx); err != nil {
			return total, err
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if deleted < int64(batchSize) {
			return total, nil
		}
		if err := j.sleep(ctx); err != nil {
			return total, err
		}
	}
	return total, nil
}

// cleanupRetentionInBatches mirrors cleanupRetentionInBatches: multi-counter
// stats/metrics batches continue while the batch sum is non-zero.
func (j *DataRetentionJob) cleanupRetentionInBatches(ctx context.Context, result *CleanupResult, maxBatches int, cleanupBatch func(ctx context.Context) (UsageStatsRetentionCounts, error)) error {
	for index := 0; index < maxBatches; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		deleted, err := cleanupBatch(ctx)
		if err != nil {
			return err
		}
		result.AddUsageStats(deleted)
		if err := yieldToEventLoop(ctx); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if deleted.Sum() == 0 {
			return nil
		}
		if err := j.sleep(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (j *DataRetentionJob) cleanupRetentionInBatchesSystemMetrics(ctx context.Context, result *CleanupResult, maxBatches int, cleanupBatch func(ctx context.Context) (SystemMetricsRetentionCounts, error)) error {
	for index := 0; index < maxBatches; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		deleted, err := cleanupBatch(ctx)
		if err != nil {
			return err
		}
		result.AddSystemMetrics(deleted)
		if err := yieldToEventLoop(ctx); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if deleted.Sum() == 0 {
			return nil
		}
		if err := j.sleep(ctx); err != nil {
			return err
		}
	}
	return nil
}

// cleanupCodexContextStatesInBatches mirrors
// cleanupCodexContextStatesInBatches: each batch deletes expired state rows
// then settles the storage file deletions before deciding to continue.
func (j *DataRetentionJob) cleanupCodexContextStatesInBatches(ctx context.Context, result *CleanupResult, expiredBefore string, batchSize, maxBatches int) error {
	for index := 0; index < maxBatches; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		cleanup, err := j.db().CleanupExpiredCodexContextStates(ctx, expiredBefore, batchSize)
		if err != nil {
			return err
		}
		if cleanup == nil {
			return nil
		}
		result.CodexContextSessions += cleanup.DeletedSessions
		result.CodexContextResponses += cleanup.DeletedResponses
		result.CodexContextCompacts += cleanup.DeletedCompacts
		files, err := j.codexStorage().ProcessBatch(ctx, cleanup.StorageKeys)
		result.CodexContextFiles += files
		if err != nil {
			return err
		}
		if err := yieldToEventLoop(ctx); err != nil {
			return err
		}
		if !cleanup.HasMore || cleanup.DeletedSessions < int64(batchSize) {
			return nil
		}
		if err := j.sleep(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (j *DataRetentionJob) codexStorage() CodexContextStorageBatchProcessor {
	if j.CodexStorage != nil {
		return j.CodexStorage
	}
	return missingCodexStorage{}
}

type missingCodexStorage struct{}

func (missingCodexStorage) ProcessBatch(context.Context, []string) (int64, error) {
	return 0, errors.New("retention codex context storage processor 未初始化")
}

func (j *DataRetentionJob) checkpointAfterDelete(ctx context.Context) {
	if j.Checkpointer == nil {
		return
	}
	if err := j.Checkpointer.CheckpointAfterDelete(ctx); err != nil {
		j.logger().Warn("数据集与使用记录分片 WAL checkpoint 失败，等待下一轮清理继续维护",
			"event", "data_retention_dataset_checkpoint_failed", "error", err)
		return
	}
	j.logger().Info("数据集与使用记录分片 WAL checkpoint 完成", "event", "data_retention_dataset_checkpoint_completed")
}

// EnqueuePostgresMaintenanceJobs mirrors enqueuePostgresDataRetentionMaintenanceJobs:
// enqueue the usage-records cleanup job, then run the PostgreSQL publicApiLogs,
// stats, metrics, sessions and codex-context cleanup stages directly.
func (j *DataRetentionJob) EnqueuePostgresMaintenanceJobs(ctx context.Context) error {
	if j.ProcessRole != "worker" {
		return nil
	}
	if !j.begin("postgres") {
		return nil
	}
	defer j.end("postgres")
	if err := ctx.Err(); err != nil {
		return err
	}

	if _, err := j.runPostgresStages(ctx); err != nil {
		if ctx.Err() != nil {
			return err
		}
		j.logger().Error("PostgreSQL 高性能数据保留维护任务投递失败",
			"event", "postgres_data_retention_maintenance_jobs_enqueue_failed", "error", err)
		return err
	}
	return nil
}

func (j *DataRetentionJob) runPostgresStages(ctx context.Context) (CleanupResult, error) {
	settings, err := j.loadSettings(ctx)
	if err != nil {
		return EmptyCleanupResult(), err
	}
	timezone, err := j.loadTimezone(ctx)
	if err != nil {
		return EmptyCleanupResult(), err
	}
	policy, err := LoadPolicy(settings)
	if err != nil {
		return EmptyCleanupResult(), err
	}
	nowMillis := j.clock().UnixMilli()
	nowAt := ISOString(time.UnixMilli(nowMillis))
	batchSize := CleanupBatchSize
	maxBatches := CleanupMaxBatchesPerRun
	cutoffAt := cutoffISO(nowMillis, policy.UsageRecordDays)

	if j.Enqueuer == nil {
		return EmptyCleanupResult(), errors.New("retention record maintenance enqueuer 未初始化")
	}
	usageRecordsJob, err := UsageRecordsCleanupJob(cutoffAt, batchSize, maxBatches, j.clock())
	if err != nil {
		return EmptyCleanupResult(), err
	}
	if err := j.Enqueuer.EnqueueAsync(ctx, usageRecordsJob); err != nil {
		return EmptyCleanupResult(), err
	}

	result := EmptyCleanupResult()
	publicApiLogs, err := cleanupCountedRetentionBatches(ctx, maxBatches, batchSize, func(ctx context.Context) (int64, error) {
		return j.publicApiLogs().CleanupBefore(ctx, cutoffISO(nowMillis, policy.PublicApiLogDays), batchSize)
	}, j.sleep)
	if err != nil {
		return result, err
	}
	result.PublicApiLogs = publicApiLogs

	statsLocation, err := LoadUsageStatsTimezone(timezone)
	if err != nil {
		return result, err
	}
	if err := runRetentionBatches(ctx, maxBatches, 1, func(ctx context.Context) (int64, error) {
		deleted, err := j.stats().CleanupUsageStatsRetention(ctx, usageStatsRetentionInput(nowMillis, policy, statsLocation, batchSize))
		if err != nil {
			return 0, err
		}
		result.AddUsageStats(deleted)
		return deleted.Sum(), nil
	}, j.sleep); err != nil {
		return result, err
	}
	if err := runRetentionBatches(ctx, maxBatches, 1, func(ctx context.Context) (int64, error) {
		deleted, err := j.stats().CleanupSystemMetricsRetention(ctx, systemMetricsRetentionInput(nowMillis, policy, statsLocation, batchSize))
		if err != nil {
			return 0, err
		}
		result.AddSystemMetrics(deleted)
		return deleted.Sum(), nil
	}, j.sleep); err != nil {
		return result, err
	}
	if err := runRetentionBatches(ctx, maxBatches, batchSize, func(ctx context.Context) (int64, error) {
		deleted, err := j.db().CleanupExpiredSystemSessions(ctx, nowAt, batchSize)
		if err != nil {
			return 0, err
		}
		result.SystemSessions += deleted
		return deleted, nil
	}, j.sleep); err != nil {
		return result, err
	}
	if err := runRetentionBatches(ctx, maxBatches, batchSize, func(ctx context.Context) (int64, error) {
		cleanup, err := j.db().CleanupExpiredCodexContextStates(ctx, nowAt, batchSize)
		if err != nil {
			return 0, err
		}
		if cleanup == nil {
			return 0, nil
		}
		result.CodexContextSessions += cleanup.DeletedSessions
		result.CodexContextResponses += cleanup.DeletedResponses
		result.CodexContextCompacts += cleanup.DeletedCompacts
		files, err := j.codexStorage().ProcessBatch(ctx, cleanup.StorageKeys)
		result.CodexContextFiles += files
		if err != nil {
			return 0, err
		}
		if cleanup.HasMore {
			return int64(batchSize), nil
		}
		return 0, nil
	}, j.sleep); err != nil {
		return result, err
	}

	j.logger().Info("PostgreSQL 高性能使用记录、审计、日志与统计保留维护任务已投递",
		"event", "postgres_data_retention_maintenance_jobs_enqueued",
		"cutoffAt", cutoffAt,
		"usageRecordRetentionDays", policy.UsageRecordDays,
		"batchSize", batchSize,
		"maxBatches", maxBatches,
		"retainedCleanup", result.Map(),
	)
	return result, nil
}

// cleanupCountedRetentionBatches mirrors cleanupCountedRetentionBatches: like
// cleanupInBatches but with a normalized max-batches floor of 1 and no pause
// after the final allowed batch.
func cleanupCountedRetentionBatches(ctx context.Context, maxBatches, fullBatchSize int, cleanupBatch func(ctx context.Context) (int64, error), sleep Sleeper) (int64, error) {
	var total int64
	normalized := normalizeMaxBatches(maxBatches)
	for index := 0; index < normalized; index++ {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		deleted, err := cleanupBatch(ctx)
		if err != nil {
			return total, err
		}
		total += deleted
		if deleted < int64(fullBatchSize) {
			return total, nil
		}
		if index < normalized-1 {
			if sleep != nil {
				if err := sleep(ctx); err != nil {
					return total, err
				}
			}
		}
	}
	return total, nil
}

// runRetentionBatches mirrors runRetentionBatches: single-counter decision
// semantics without result accumulation (the closure accumulates).
func runRetentionBatches(ctx context.Context, maxBatches, fullBatchSize int, cleanupBatch func(ctx context.Context) (int64, error), sleep Sleeper) error {
	normalized := normalizeMaxBatches(maxBatches)
	for index := 0; index < normalized; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		deleted, err := cleanupBatch(ctx)
		if err != nil {
			return err
		}
		if deleted < int64(fullBatchSize) {
			return nil
		}
		if index < normalized-1 {
			if sleep != nil {
				if err := sleep(ctx); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func normalizeMaxBatches(maxBatches int) int {
	if maxBatches < 1 {
		return 1
	}
	return maxBatches
}

// yieldToEventLoop mirrors yieldToEventLoop: it only gives the scheduler a
// slice; abort is re-checked by the caller right after.
func yieldToEventLoop(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// pauseCleanupBatch mirrors pauseBetweenCleanupBatches with the fixed 25ms
// pause; an abort while waiting surfaces as ctx.Err().
func pauseCleanupBatch(ctx context.Context, pause time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(pause)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func usageStatsRetentionInput(nowMillis int64, policy Policy, location *time.Location, batchSize int) UsageStatsRetentionInput {
	return UsageStatsRetentionInput{
		AccountQualityMinuteCutoffMinute: cutoffMinuteKey(nowMillis, accountQualityMinuteRetentionHours, location),
		MinuteCutoffMinute:               cutoffMinuteKey(nowMillis, policy.StatsMinuteHours, location),
		HourlyCutoffHour:                 cutoffHourKey(nowMillis, policy.StatsHourlyDays, location),
		DailyCutoffDate:                  cutoffDateKey(nowMillis, policy.StatsDailyDays, location),
		WeeklyCutoffWeek:                 cutoffWeekKey(nowMillis, policy.StatsWeeklyWeeks, location),
		MonthlyCutoffMonth:               cutoffMonthKeyHost(time.UnixMilli(nowMillis), policy.StatsMonthlyMonths, time.Local, location),
		RankSnapshotCutoffIso:            cutoffISO(nowMillis, policy.RankSnapshotDays),
		WindowCutoffDate:                 cutoffDateKey(nowMillis, policy.FixedWindowDays, location),
		WindowCutoffIso:                  cutoffISO(nowMillis, policy.AccountUsageSnapshotDays),
		Limit:                            batchSize,
	}
}

func systemMetricsRetentionInput(nowMillis int64, policy Policy, location *time.Location, batchSize int) SystemMetricsRetentionInput {
	return SystemMetricsRetentionInput{
		SamplesCutoffIso:      cutoffISO(nowMillis, policy.SystemMetricsSampleDays),
		HourlyCutoffHour:      cutoffHourKey(nowMillis, policy.SystemMetricsHourlyDays, location),
		TrendWindowCutoffDate: cutoffDateKey(nowMillis, policy.FixedWindowDays, location),
		Limit:                 batchSize,
	}
}
