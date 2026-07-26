import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { getDatasetDatabase } from '../../storage/database.js'
import { cleanupAuditHotSearchFilesBefore } from '../../storage/audit-log-hot-search-files.js'
import { cleanupAuditLogsByRetentionAsync } from '../../storage/audit-logs.repository.js'
import { cleanupOperationLogsBefore } from '../../storage/operation-logs.repository.js'
import { cleanupPublicApiLogsBefore } from '../../storage/public-api-logs.repository.js'
import {
  cleanupModelCheckRunsBefore,
  cleanupProcessedUsageRecordsBeforeWithResultAsync
} from '../../storage/data-retention.repository.js'
import { getSettings } from '../../storage/settings.repository.js'
import {
  runtimeLogIndexRetentionDaysFromSettings
} from '../../storage/runtime-logs.repository.js'
import { tableMonitorSampleRetentionDays } from '../../storage/table-monitor.repository.js'
import { checkpointSqliteWal, type SqliteWalCheckpointResult } from '../../storage/sqlite-maintenance.js'
import { checkpointOpenUsageRecordShardDatabases } from '../../storage/usage-record-shards.js'
import { dateKey, hourKey, minuteKey, monthKey, usageStatsTimezone, weekKey } from '../../storage/usage-stats-helpers.js'
import { readAuditLogSettings } from '../audit-logs/audit-log-settings.js'
import { auditSuccessRetentionCutoffIso } from '../audit-logs/audit-log-retention-policy.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'
import { requestStatsWriter } from './background-stats-writer.js'
import { deleteCodexContextStorageKeys } from '../gateway/codex-responses/chat-bridge-state.js'
import { cleanupRuntimeLogIndexRetention } from '../runtime-logs/runtime-log-index-retention.service.js'
import {
  DATA_RETENTION_CLEANUP_BATCH_PAUSE_MS,
  DATA_RETENTION_CLEANUP_BATCH_SIZE,
  DATA_RETENTION_CLEANUP_MAX_BATCHES_PER_RUN
} from './data-retention-cleanup.constants.js'

const dayMs = 24 * 60 * 60 * 1000
const usageRecordRetentionMaxDays = 180
const accountQualityMinuteRetentionHours = 24
const statsMinuteRetentionMaxHours = 24 * 14
const statsHourlyRetentionMaxDays = 180
const statsDailyRetentionMaxDays = 800
const statsWeeklyRetentionMaxWeeks = 260
const statsMonthlyRetentionMaxMonths = 60
const rankSnapshotRetentionMaxDays = 365
const systemMetricsRawRetentionMaxDays = 7
const statsRetentionMaxDays = 30
const snapshotRetentionMaxDays = 30
const operationLogRetentionMaxDays = 3650
const publicApiLogRetentionMaxDays = 365
const modelCheckRetentionMaxDays = 365
let cleanupRunning = false

interface DataRetentionPolicy {
  auditLogSuccessHotHours: number
  auditLogSuccessDays: number
  auditLogFailureDays: number
  auditErrorGroupDays: number
  operationLogDays: number
  publicApiLogDays: number
  runtimeLogDays: number
  modelCheckDays: number
  usageRecordDays: number
  statsMinuteHours: number
  statsHourlyDays: number
  statsDailyDays: number
  statsWeeklyWeeks: number
  statsMonthlyMonths: number
  rankSnapshotDays: number
  systemMetricsSampleDays: number
  systemMetricsHourlyDays: number
  accountUsageSnapshotDays: number
  fixedWindowDays: number
  tableStorageSnapshotDays: number
}

export interface DataRetentionCleanupResult {
  operationLogs: number
  publicApiLogs: number
  auditLogs: number
  auditHotSearchFiles: number
  runtimeLogs: number
  runtimeLogFileCursors: number
  modelCheckRuns: number
  modelCheckItems: number
  usageRecords: number
  accountQualityMinuteStats: number
  accountHealthHourly: number
  accountQualityHealthHourly: number
  usageStatsMinute: number
  usageModelMinute: number
  usageErrorMinute: number
  usageLatencyMinute: number
  usageStatsDaily: number
  usageModelDaily: number
  usageErrorDaily: number
  usageLatencyDaily: number
  usageStatsHourly: number
  usageModelHourly: number
  usageErrorHourly: number
  usageLatencyHourly: number
  usageStatsWeekly: number
  usageModelWeekly: number
  usageErrorWeekly: number
  usageLatencyWeekly: number
  usageStatsMonthly: number
  usageModelMonthly: number
  usageErrorMonthly: number
  usageLatencyMonthly: number
  authorizationTeamUsageSummaryDaily: number
  authorizationTeamUsageRangeWindows: number
  authorizationUserUsageSummaryDaily: number
  authorizationUserUsageRangeWindows: number
  usageRankSnapshots: number
  usageOverviewSummaryWindows: number
  usageOverviewTrendWindows: number
  usageModelRankWindows: number
  usageErrorRankWindows: number
  aiPerformanceSummaryWindows: number
  usageQuotaHourlyWindows: number
  usageScopeRangeWindows: number
  clientIpUsageRangeWindows: number
  clientIpRangeWindowDirtyIps: number
  clientIpAccountStatsDaily: number
  clientIpAccountUsageRangeWindows: number
  clientIpAccountRangeWindowDirtyIps: number
  accountUsageSnapshots: number
  systemMetricsSamples: number
  systemMetricsHourly: number
  systemMetricsTrendWindows: number
  processEventLoopSamples: number
  processEventLoopHourly: number
  processEventLoopTrendWindows: number
  tableStorageSnapshots: number
  systemSessions: number
  codexContextSessions: number
  codexContextResponses: number
  codexContextCompacts: number
  codexContextFiles: number
}

export async function cleanupExpiredRetainedData(): Promise<DataRetentionCleanupResult> {
  if (runtimeConfig.processRole !== 'worker') {
    return emptyCleanupResult()
  }
  if (cleanupRunning) {
    return emptyCleanupResult()
  }
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error('高性能模式禁止运行单机数据保留清理 worker；请使用 PostgreSQL 数据维护任务清理非业务数据，禁止静默跳过或回落 SQLite 清理链路')
  }

  cleanupRunning = true
  try {
    const settings = getSettings()
    const timezone = usageStatsTimezone()
    const batchSize = DATA_RETENTION_CLEANUP_BATCH_SIZE
    const maxBatches = DATA_RETENTION_CLEANUP_MAX_BATCHES_PER_RUN
    const now = Date.now()
    const auditSettings = readAuditLogSettings()
    const retention: DataRetentionPolicy = {
      auditLogSuccessHotHours: auditSettings.successHotRetentionHours,
      auditLogSuccessDays: auditSettings.successRetentionDays,
      auditLogFailureDays: auditSettings.problemRetentionDays,
      auditErrorGroupDays: auditSettings.problemRetentionDays,
      operationLogDays: settingNumber(settings, 'operationLogRetentionDays', 1, operationLogRetentionMaxDays),
      publicApiLogDays: settingNumber(settings, 'publicApiLogRetentionDays', 1, publicApiLogRetentionMaxDays),
      runtimeLogDays: runtimeLogIndexRetentionDaysFromSettings(settings),
      modelCheckDays: settingNumber(settings, 'modelCheckRetentionDays', 1, modelCheckRetentionMaxDays),
      usageRecordDays: settingNumber(settings, 'usageRecordRetentionDays', 1, usageRecordRetentionMaxDays),
      statsMinuteHours: settingNumber(settings, 'usageStatsMinuteRetentionHours', 1, statsMinuteRetentionMaxHours),
      statsHourlyDays: settingNumber(settings, 'usageStatsHourlyRetentionDays', 1, statsHourlyRetentionMaxDays),
      statsDailyDays: settingNumber(settings, 'usageStatsDailyRetentionDays', 1, statsDailyRetentionMaxDays),
      statsWeeklyWeeks: settingNumber(settings, 'usageStatsWeeklyRetentionWeeks', 1, statsWeeklyRetentionMaxWeeks),
      statsMonthlyMonths: settingNumber(settings, 'usageStatsMonthlyRetentionMonths', 1, statsMonthlyRetentionMaxMonths),
      rankSnapshotDays: settingNumber(settings, 'usageRankSnapshotRetentionDays', 1, rankSnapshotRetentionMaxDays),
      systemMetricsSampleDays: settingNumber(settings, 'systemMetricsRetentionDays', 1, systemMetricsRawRetentionMaxDays),
      systemMetricsHourlyDays: settingNumber(settings, 'systemMetricsHourlyRetentionDays', 1, statsRetentionMaxDays),
      accountUsageSnapshotDays: snapshotRetentionMaxDays,
      fixedWindowDays: statsRetentionMaxDays,
      tableStorageSnapshotDays: tableMonitorSampleRetentionDays
    }

    const result = emptyCleanupResult()
    if (runtimeConfig.workerRole === 'ingest-worker') {
      const datasetCleanup = await cleanupDatasetAndUsageRetainedData({
        now,
        retention,
        batchSize,
        maxBatches,
        successSampleBucketThreshold: Math.round(auditSettings.successSampleRate * 10000)
      })
      addCleanupResult(result, datasetCleanup)
      if (sumDeleted(datasetCleanup) > 0) {
        checkpointDatasetAndUsageDatabasesAfterDelete()
      }
    }
    if (runtimeConfig.workerRole === 'ingest-worker') {
      await cleanupRetentionInBatches(result, () => requestStatsWriter({
        type: 'cleanup_usage_stats_retention',
        input: usageStatsRetentionInput(now, retention, timezone, batchSize)
      }), maxBatches)
      await cleanupRetentionInBatches(result, () => requestStatsWriter({
        type: 'cleanup_system_metrics_retention',
        input: systemMetricsRetentionInput(now, retention, timezone, batchSize)
      }), maxBatches)
      const tableCleanup = await cleanupInBatches(async () => {
        const cleanupResult = await requestStatsWriter({
          type: 'cleanup_table_storage_snapshots_retention',
          cutoffIso: cutoffIso(now, retention.tableStorageSnapshotDays),
          limit: batchSize
        })
        return cleanupResult.deleted
      }, batchSize, maxBatches)
      result.tableStorageSnapshots = tableCleanup
      result.systemSessions = await cleanupInBatches(async () => {
        const cleanupResult = await requestBackgroundWorkerDbService({
          type: 'cleanup_expired_system_sessions',
          expiredBefore: new Date(now).toISOString(),
          limit: batchSize
        })
        return cleanupResult?.deleted ?? 0
      }, batchSize, maxBatches)
      await cleanupCodexContextStatesInBatches(result, new Date(now).toISOString(), batchSize, maxBatches)
    }

    logger.info({
      event: 'data_retention_cleanup_completed',
      deleted: result,
      retention,
      batchSize,
      maxBatches,
      workerRole: runtimeConfig.workerRole
    }, '数据保留清理完成')

    return result
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'data_retention_cleanup_failed' }), '数据保留清理失败')
    throw error
  } finally {
    cleanupRunning = false
  }
}

async function cleanupDatasetAndUsageRetainedData(input: {
  now: number
  retention: DataRetentionPolicy
  batchSize: number
  maxBatches: number
  successSampleBucketThreshold: number
}): Promise<Partial<Record<keyof DataRetentionCleanupResult, number>>> {
  const result = emptyCleanupResult()
  const { now, retention, batchSize, maxBatches } = input
  result.operationLogs = await cleanupInBatches(() => cleanupOperationLogsBefore(cutoffIso(now, retention.operationLogDays), batchSize), batchSize, maxBatches)
  await yieldToEventLoop()
  result.publicApiLogs = await cleanupInBatches(
    () => cleanupPublicApiLogsBefore(cutoffIso(now, retention.publicApiLogDays), batchSize),
    batchSize,
    maxBatches
  )
  await yieldToEventLoop()
  result.auditLogs = await cleanupInBatches(() => cleanupAuditLogsByRetentionAsync({
      successHotCutoffCreatedAt: cutoffHoursIso(now, retention.auditLogSuccessHotHours),
      successCutoffCreatedAt: auditSuccessRetentionCutoffIso(now, retention.auditLogSuccessHotHours, retention.auditLogSuccessDays),
      failureCutoffCreatedAt: cutoffIso(now, retention.auditLogFailureDays),
      errorGroupCutoffUpdatedAt: cutoffIso(now, retention.auditErrorGroupDays),
      successSampleBucketThreshold: input.successSampleBucketThreshold,
      limit: batchSize
    }), batchSize, maxBatches)
  await yieldToEventLoop()
  result.auditHotSearchFiles = await cleanupAuditHotSearchFilesBefore(cutoffHoursIso(now, retention.auditLogSuccessHotHours))
  await yieldToEventLoop()
  const runtimeLogCleanup = await cleanupRuntimeLogIndexRetention({
    cutoffIso: cutoffIso(now, retention.runtimeLogDays),
    batchSize,
    maxBatches
  })
  result.runtimeLogs = runtimeLogCleanup.runtimeLogs
  result.runtimeLogFileCursors = runtimeLogCleanup.runtimeLogFileCursors
  await yieldToEventLoop()
  await cleanupRetentionInBatches(
    result,
    () => cleanupModelCheckRunsBefore(cutoffIso(now, retention.modelCheckDays), batchSize),
    maxBatches
  )
  await yieldToEventLoop()
  const usageRecordCleanup = await cleanupProcessedUsageRecordsInBatches(cutoffIso(now, retention.usageRecordDays), batchSize, maxBatches)
  result.usageRecords = usageRecordCleanup.deletedRows
  if (usageRecordCleanup.blockedReason) {
    logger.warn({
      event: 'data_retention_usage_records_cleanup_blocked',
      blockedReason: usageRecordCleanup.blockedReason,
      cutoffCreatedAt: usageRecordCleanup.cutoffCreatedAt,
      deletedRows: usageRecordCleanup.deletedRows,
      batches: usageRecordCleanup.batches
    }, '使用记录保留清理被统计安全游标拦截')
  }
  return result
}

async function cleanupInBatches(cleanupBatch: () => number | Promise<number>, batchSize: number, maxBatches: number): Promise<number> {
  let total = 0
  for (let index = 0; index < maxBatches; index += 1) {
    const deleted = await cleanupBatch()
    total += deleted
    await yieldToEventLoop()
    if (deleted < batchSize) {
      break
    }
    await pauseBetweenCleanupBatches()
  }
  return total
}

async function cleanupProcessedUsageRecordsInBatches(cutoffCreatedAt: string, batchSize: number, maxBatches: number): Promise<{
  cutoffCreatedAt: string
  deletedRows: number
  batches: number
  blockedReason?: string
}> {
  let deletedRows = 0
  let batches = 0
  let blockedReason: string | undefined
  for (let index = 0; index < maxBatches; index += 1) {
    const batch = await cleanupProcessedUsageRecordsBeforeWithResultAsync(cutoffCreatedAt, batchSize)
    deletedRows += batch.deletedRows
    blockedReason = batch.blockedReason ?? blockedReason
    if (batch.deletedRows > 0) {
      batches += 1
    }
    await yieldToEventLoop()
    if (batch.blockedReason || batch.deletedRows < batchSize || !batch.hasMore) {
      break
    }
    await pauseBetweenCleanupBatches()
  }
  return {
    cutoffCreatedAt,
    deletedRows,
    batches,
    blockedReason
  }
}

async function cleanupRetentionInBatches(
  result: DataRetentionCleanupResult,
  cleanupBatch: () => Partial<Record<keyof DataRetentionCleanupResult, number>> | Promise<Partial<Record<keyof DataRetentionCleanupResult, number>>>,
  maxBatches: number
): Promise<void> {
  for (let index = 0; index < maxBatches; index += 1) {
    const deleted = await cleanupBatch()
    addCleanupResult(result, deleted)
    await yieldToEventLoop()
    if (sumDeleted(deleted) === 0) {
      break
    }
    await pauseBetweenCleanupBatches()
  }
}

async function cleanupCodexContextStatesInBatches(
  result: DataRetentionCleanupResult,
  expiredBefore: string,
  batchSize: number,
  maxBatches: number
): Promise<void> {
  for (let index = 0; index < maxBatches; index += 1) {
    const cleanupResult = await requestBackgroundWorkerDbService({
      type: 'cleanup_expired_codex_context_states',
      expiredBefore,
      limit: batchSize
    })
    if (!cleanupResult) {
      break
    }
    result.codexContextSessions += cleanupResult.deletedSessions
    result.codexContextResponses += cleanupResult.deletedResponses
    result.codexContextCompacts += cleanupResult.deletedCompacts
    result.codexContextFiles += await deleteCodexContextStorageKeys(cleanupResult.storageKeys)
    await yieldToEventLoop()
    if (!cleanupResult.hasMore || cleanupResult.deletedSessions < batchSize) {
      break
    }
    await pauseBetweenCleanupBatches()
  }
}

function usageStatsRetentionInput(now: number, retention: DataRetentionPolicy, timezone: string, batchSize: number): {
  accountQualityMinuteCutoffMinute: string
  minuteCutoffMinute: string
  hourlyCutoffHour: string
  dailyCutoffDate: string
  weeklyCutoffWeek: string
  monthlyCutoffMonth: string
  rankSnapshotCutoffIso: string
  windowCutoffDate: string
  windowCutoffIso: string
  limit: number
} {
  return {
    accountQualityMinuteCutoffMinute: cutoffMinuteKey(now, accountQualityMinuteRetentionHours, timezone),
    minuteCutoffMinute: cutoffMinuteKey(now, retention.statsMinuteHours, timezone),
    hourlyCutoffHour: cutoffHourKey(now, retention.statsHourlyDays, timezone),
    dailyCutoffDate: cutoffDateKey(now, retention.statsDailyDays, timezone),
    weeklyCutoffWeek: cutoffWeekKey(now, retention.statsWeeklyWeeks, timezone),
    monthlyCutoffMonth: cutoffMonthKey(now, retention.statsMonthlyMonths, timezone),
    rankSnapshotCutoffIso: cutoffIso(now, retention.rankSnapshotDays),
    windowCutoffDate: cutoffDateKey(now, retention.fixedWindowDays, timezone),
    windowCutoffIso: cutoffIso(now, retention.accountUsageSnapshotDays),
    limit: batchSize
  }
}

function systemMetricsRetentionInput(now: number, retention: DataRetentionPolicy, timezone: string, batchSize: number): {
  samplesCutoffIso: string
  hourlyCutoffHour: string
  trendWindowCutoffDate: string
  limit: number
} {
  return {
    samplesCutoffIso: cutoffIso(now, retention.systemMetricsSampleDays),
    hourlyCutoffHour: cutoffHourKey(now, retention.systemMetricsHourlyDays, timezone),
    trendWindowCutoffDate: cutoffDateKey(now, retention.fixedWindowDays, timezone),
    limit: batchSize
  }
}

function addCleanupResult(
  result: DataRetentionCleanupResult,
  deleted: Partial<Record<keyof DataRetentionCleanupResult, number>>
): void {
  for (const [key, value] of Object.entries(deleted) as Array<[keyof DataRetentionCleanupResult, number | undefined]>) {
    result[key] += value ?? 0
  }
}

function sumDeleted(deleted: Partial<Record<keyof DataRetentionCleanupResult, number>>): number {
  return Object.values(deleted).reduce((sum, value) => sum + Number(value ?? 0), 0)
}

function checkpointDatasetAndUsageDatabases(): SqliteWalCheckpointResult[] {
  const checkpoints: SqliteWalCheckpointResult[] = []
  const datasetCheckpoint = checkpointSqliteWal(getDatasetDatabase(), 'dataset')
  if (datasetCheckpoint) {
    checkpoints.push(datasetCheckpoint)
  }
  checkpoints.push(...checkpointOpenUsageRecordShardDatabases())
  return checkpoints
}

function checkpointDatasetAndUsageDatabasesAfterDelete(): void {
  try {
    const checkpoints = checkpointDatasetAndUsageDatabases()
    if (checkpoints.length > 0) {
      logger.info({
        event: 'data_retention_dataset_checkpoint_completed',
        checkpoints
      }, '数据集与使用记录分片 WAL checkpoint 完成')
    }
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'data_retention_dataset_checkpoint_failed'
    }), '数据集与使用记录分片 WAL checkpoint 失败，等待下一轮清理继续维护')
  }
}

function cutoffIso(now: number, retentionDays: number): string {
  return new Date(now - retentionDays * dayMs).toISOString()
}

function cutoffHoursIso(now: number, retentionHours: number): string {
  return new Date(now - retentionHours * 60 * 60 * 1000).toISOString()
}

function cutoffDateKey(now: number, retentionDays: number, timezone: string): string {
  return dateKey(new Date(now - retentionDays * dayMs), timezone)
}

function cutoffHourKey(now: number, retentionDays: number, timezone: string): string {
  return hourKey(new Date(now - retentionDays * dayMs), timezone)
}

function cutoffMinuteKey(now: number, retentionHours: number, timezone: string): string {
  return minuteKey(new Date(now - retentionHours * 60 * 60 * 1000), timezone)
}

function cutoffWeekKey(now: number, retentionWeeks: number, timezone: string): string {
  return weekKey(new Date(now - retentionWeeks * 7 * dayMs), timezone)
}

function cutoffMonthKey(now: number, retentionMonths: number, timezone: string): string {
  const date = new Date(now)
  date.setMonth(date.getMonth() - retentionMonths)
  return monthKey(date, timezone)
}

function settingNumber(settings: Record<string, unknown>, key: string, min: number, max: number): number {
  const value = settings[key]
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value)) {
    throw new Error(`系统设置 ${key} 必须是整数`)
  }
  if (value < min || value > max) {
    throw new Error(`系统设置 ${key} 必须在 ${min} 到 ${max} 之间`)
  }
  return value
}

function emptyCleanupResult(): DataRetentionCleanupResult {
  return {
    operationLogs: 0,
    publicApiLogs: 0,
    auditLogs: 0,
    auditHotSearchFiles: 0,
    runtimeLogs: 0,
    runtimeLogFileCursors: 0,
    modelCheckRuns: 0,
    modelCheckItems: 0,
    usageRecords: 0,
    accountQualityMinuteStats: 0,
    accountHealthHourly: 0,
    accountQualityHealthHourly: 0,
    usageStatsMinute: 0,
    usageModelMinute: 0,
    usageErrorMinute: 0,
    usageLatencyMinute: 0,
    usageStatsDaily: 0,
    usageModelDaily: 0,
    usageErrorDaily: 0,
    usageLatencyDaily: 0,
    usageStatsHourly: 0,
    usageModelHourly: 0,
    usageErrorHourly: 0,
    usageLatencyHourly: 0,
    usageStatsWeekly: 0,
    usageModelWeekly: 0,
    usageErrorWeekly: 0,
    usageLatencyWeekly: 0,
    usageStatsMonthly: 0,
    usageModelMonthly: 0,
    usageErrorMonthly: 0,
    usageLatencyMonthly: 0,
    authorizationTeamUsageSummaryDaily: 0,
    authorizationTeamUsageRangeWindows: 0,
    authorizationUserUsageSummaryDaily: 0,
    authorizationUserUsageRangeWindows: 0,
    usageRankSnapshots: 0,
    usageOverviewSummaryWindows: 0,
    usageOverviewTrendWindows: 0,
    usageModelRankWindows: 0,
    usageErrorRankWindows: 0,
    aiPerformanceSummaryWindows: 0,
    usageQuotaHourlyWindows: 0,
    usageScopeRangeWindows: 0,
    clientIpUsageRangeWindows: 0,
    clientIpRangeWindowDirtyIps: 0,
    clientIpAccountStatsDaily: 0,
    clientIpAccountUsageRangeWindows: 0,
    clientIpAccountRangeWindowDirtyIps: 0,
    accountUsageSnapshots: 0,
    systemMetricsSamples: 0,
    systemMetricsHourly: 0,
    systemMetricsTrendWindows: 0,
    processEventLoopSamples: 0,
    processEventLoopHourly: 0,
    processEventLoopTrendWindows: 0,
    tableStorageSnapshots: 0,
    systemSessions: 0,
    codexContextSessions: 0,
    codexContextResponses: 0,
    codexContextCompacts: 0,
    codexContextFiles: 0
  }
}

function yieldToEventLoop(): Promise<void> {
  return new Promise((resolve) => setImmediate(resolve))
}

function pauseBetweenCleanupBatches(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, DATA_RETENTION_CLEANUP_BATCH_PAUSE_MS))
}
