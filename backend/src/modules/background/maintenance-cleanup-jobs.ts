import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { cleanupPendingDeletedAccountRecordTargetsAsync } from '../../storage/account-record-cleanup.js'
import { cleanupPendingDeletedApiKeyRecordTargetsAsync } from '../../storage/api-key-record-cleanup.js'
import { cleanupModelCheckRunsBeforeAsync } from '../../storage/data-retention.repository.js'
import { cleanupOperationLogsBeforeAsync } from '../../storage/operation-log-cleanup.repository.js'
import { cleanupPublicApiLogsBeforeAsync } from '../../storage/public-api-logs.repository.js'
import {
  runtimeLogIndexRetentionDaysFromSettings
} from '../../storage/runtime-logs.repository.js'
import { getSettingsAsync } from '../../storage/settings.repository.js'
import { tableMonitorSampleRetentionDays } from '../../storage/table-monitor.repository.js'
import { dateKey, hourKey, minuteKey, monthKey, usageStatsTimezoneAsync, weekKey } from '../../storage/usage-stats-helpers.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'
import { requestStatsWriter } from './background-stats-writer.js'
import { cleanupExpiredAuditHotRetentionData } from './audit-hot-retention-cleanup.service.js'
import { cleanupExpiredRetainedData } from './data-retention-cleanup.service.js'
import { readAuditLogSettings } from '../audit-logs/audit-log-settings.js'
import { deleteCodexContextStorageKeys } from '../gateway/codex-responses/chat-bridge-state.js'
import {
  DATA_RETENTION_CLEANUP_BATCH_PAUSE_MS,
  DATA_RETENTION_CLEANUP_BATCH_SIZE,
  DATA_RETENTION_CLEANUP_MAX_BATCHES_PER_RUN
} from './data-retention-cleanup.constants.js'
import { enqueueRecordMaintenanceJobAsync, enqueueRecordMaintenanceJobWithResult } from '../record-maintenance/record-maintenance-queue.service.js'
import { cleanupRuntimeLogIndexRetention } from '../runtime-logs/runtime-log-index-retention.service.js'

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
const expiredDeletedAccountCleanupDbServiceTimeoutMs = 60_000
let postgresDataRetentionDispatchRunning = false

interface PostgresRetentionPolicy {
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

export async function runApiKeyRecordCleanupRetry(): Promise<void> {
  try {
    const summary = await cleanupPendingDeletedApiKeyRecordTargetsAsync(1, runtimeConfig.databaseDriver === 'postgres'
      ? undefined
      : async (input) => {
          await requestStatsWriter({ type: 'cleanup_deleted_api_key_record_stats', input })
        })
    if (summary.attempted > 0) {
      logger.info({
        event: 'background_api_key_record_cleanup_retry_completed',
        ...summary
      }, '已删除 API Key 关联数据清理重试完成')
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_api_key_record_cleanup_retry_failed' }), '已删除 API Key 关联数据清理重试失败')
    throw error
  }
}

export async function runAccountRecordCleanupRetry(): Promise<void> {
  try {
    const summary = await cleanupPendingDeletedAccountRecordTargetsAsync(1, runtimeConfig.databaseDriver === 'postgres'
      ? undefined
      : async (input) => {
          await requestStatsWriter({ type: 'cleanup_deleted_account_record_stats', input })
        })
    if (summary.attempted > 0) {
      logger.info({
        event: 'background_account_record_cleanup_retry_completed',
        ...summary
      }, '已删除 AI 账户关联数据清理重试完成')
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_account_record_cleanup_retry_failed' }), '已删除 AI 账户关联数据清理重试失败')
    throw error
  }
}

export async function runDataRetentionCleanup(): Promise<void> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    await enqueuePostgresDataRetentionMaintenanceJobs()
    return
  }
  await cleanupExpiredRetainedData()
}

export async function runChatRetentionCleanup(): Promise<void> {
  const now = new Date()
  const result = await requestBackgroundWorkerDbService({
    type: 'cleanup_chat_retention',
    now: now.toISOString(),
    interruptedBefore: new Date(now.getTime() - 20 * 60_000).toISOString(),
    limit: 1000,
    retentionDays: runtimeConfig.chat.retentionDays
  }, { timeoutMs: 60_000, priority: 'low' })
  if (!result) throw new Error('DB service 未返回 AI 问答保留清理结果')
  if (result.droppedPartitions > 0 || result.deletedMessages > 0 || result.deletedConversations > 0 || result.recoveredTurns > 0 || result.recoveredCompactions > 0 || result.deletedCheckpoints > 0 || result.claimedAssets > 0 || result.failedAssets > 0) {
    logger.info({ event: 'chat_retention_cleanup_completed', retentionDays: runtimeConfig.chat.retentionDays, ...result }, 'AI 问答过期数据清理与中断轮次恢复完成')
  }
}

async function enqueuePostgresDataRetentionMaintenanceJobs(): Promise<void> {
  if (runtimeConfig.processRole !== 'worker') return
  if (postgresDataRetentionDispatchRunning) return
  postgresDataRetentionDispatchRunning = true
  try {
    const settings = await getSettingsAsync()
    const batchSize = DATA_RETENTION_CLEANUP_BATCH_SIZE
    const maxBatches = DATA_RETENTION_CLEANUP_MAX_BATCHES_PER_RUN
    const nowMs = Date.now()
    const retention = postgresRetentionPolicy(settings)
    const cutoffAt = new Date(nowMs - retention.usageRecordDays * dayMs).toISOString()
    const auditSettings = readAuditLogSettings()
    const nowAt = new Date(nowMs).toISOString()
    await enqueueRecordMaintenanceJobAsync({
      type: 'usage_records_cleanup',
      cutoffAt,
      batchSize,
      maxBatches
    })
    await enqueueRecordMaintenanceJobAsync({
      type: 'audit_retained_data_cleanup',
      nowAt,
      successHotRetentionHours: auditSettings.successHotRetentionHours,
      successRetentionDays: auditSettings.successRetentionDays,
      failureRetentionDays: auditSettings.problemRetentionDays,
      errorGroupRetentionDays: auditSettings.problemRetentionDays,
      successSampleBucketThreshold: Math.round(auditSettings.successSampleRate * 10000),
      batchSize,
      maxBatches
    })
    const datasetCleanup = await cleanupPostgresDatasetRetainedData({
      nowMs,
      retention,
      batchSize,
      maxBatches
    })
    const retainedCleanup = await cleanupPostgresStatsAndSharedRetainedData({
      nowMs,
      nowAt,
      timezone: await usageStatsTimezoneAsync(),
      retention,
      batchSize,
      maxBatches
    })
    logger.info({
      event: 'postgres_data_retention_maintenance_jobs_enqueued',
      cutoffAt,
      auditNowAt: nowAt,
      usageRecordRetentionDays: retention.usageRecordDays,
      batchSize,
      maxBatches,
      retainedCleanup: {
        ...datasetCleanup,
        ...retainedCleanup
      }
    }, 'PostgreSQL 高性能使用记录、审计、日志与统计保留维护任务已投递')
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'postgres_data_retention_maintenance_jobs_enqueue_failed'
    }), 'PostgreSQL 高性能数据保留维护任务投递失败')
    throw error
  } finally {
    postgresDataRetentionDispatchRunning = false
  }
}

async function cleanupPostgresDatasetRetainedData(input: {
  nowMs: number
  retention: PostgresRetentionPolicy
  batchSize: number
  maxBatches: number
}): Promise<Record<string, number>> {
  const result: Record<string, number> = {}
  result.operationLogs = await cleanupCountedRetentionBatches(
    input.maxBatches,
    () => cleanupOperationLogsBeforeAsync(cutoffIso(input.nowMs, input.retention.operationLogDays), input.batchSize),
    input.batchSize
  )
  result.publicApiLogs = await cleanupCountedRetentionBatches(
    input.maxBatches,
    () => cleanupPublicApiLogsBeforeAsync(cutoffIso(input.nowMs, input.retention.publicApiLogDays), input.batchSize),
    input.batchSize
  )
  const runtimeLogCleanup = await cleanupRuntimeLogIndexRetention({
    cutoffIso: cutoffIso(input.nowMs, input.retention.runtimeLogDays),
    batchSize: input.batchSize,
    maxBatches: input.maxBatches
  })
  result.runtimeLogs = runtimeLogCleanup.runtimeLogs
  result.runtimeLogFileCursors = runtimeLogCleanup.runtimeLogFileCursors
  await runRetentionBatches(input.maxBatches, async () => {
    const deleted = await cleanupModelCheckRunsBeforeAsync(cutoffIso(input.nowMs, input.retention.modelCheckDays), input.batchSize)
    addNumberResult(result, deleted)
    return deleted.modelCheckRuns
  }, input.batchSize)
  return result
}

async function cleanupPostgresStatsAndSharedRetainedData(input: {
  nowMs: number
  nowAt: string
  timezone: string
  retention: PostgresRetentionPolicy
  batchSize: number
  maxBatches: number
}): Promise<Record<string, number>> {
  const result: Record<string, number> = {}
  await runRetentionBatches(input.maxBatches, async () => {
    const deleted = await requestStatsWriter({
      type: 'cleanup_usage_stats_retention',
      input: usageStatsRetentionInput(input.nowMs, input.retention, input.timezone, input.batchSize)
    })
    addNumberResult(result, deleted)
    return sumNumbers(deleted)
  })
  await runRetentionBatches(input.maxBatches, async () => {
    const deleted = await requestStatsWriter({
      type: 'cleanup_system_metrics_retention',
      input: systemMetricsRetentionInput(input.nowMs, input.retention, input.timezone, input.batchSize)
    })
    addNumberResult(result, deleted)
    return sumNumbers(deleted)
  })
  await runRetentionBatches(input.maxBatches, async () => {
    const deleted = await requestStatsWriter({
      type: 'cleanup_table_storage_snapshots_retention',
      cutoffIso: cutoffIso(input.nowMs, input.retention.tableStorageSnapshotDays),
      limit: input.batchSize
    })
    result.tableStorageSnapshots = (result.tableStorageSnapshots ?? 0) + deleted.deleted
    return deleted.deleted
  }, input.batchSize)
  await runRetentionBatches(input.maxBatches, async () => {
    const deleted = await requestBackgroundWorkerDbService({
      type: 'cleanup_expired_system_sessions',
      expiredBefore: input.nowAt,
      limit: input.batchSize
    })
    const count = deleted?.deleted ?? 0
    result.systemSessions = (result.systemSessions ?? 0) + count
    return count
  }, input.batchSize)
  await runRetentionBatches(input.maxBatches, async () => {
    const deleted = await requestBackgroundWorkerDbService({
      type: 'cleanup_expired_codex_context_states',
      expiredBefore: input.nowAt,
      limit: input.batchSize
    })
    if (!deleted) return 0
    result.codexContextSessions = (result.codexContextSessions ?? 0) + deleted.deletedSessions
    result.codexContextResponses = (result.codexContextResponses ?? 0) + deleted.deletedResponses
    result.codexContextCompacts = (result.codexContextCompacts ?? 0) + deleted.deletedCompacts
    result.codexContextFiles = (result.codexContextFiles ?? 0) + await deleteCodexContextStorageKeys(deleted.storageKeys)
    return deleted.hasMore ? input.batchSize : 0
  }, input.batchSize)
  return result
}

async function cleanupCountedRetentionBatches(maxBatches: number, cleanupBatch: () => Promise<number>, fullBatchSize: number): Promise<number> {
  let total = 0
  const normalizedMaxBatches = Math.max(1, Math.trunc(maxBatches))
  for (let index = 0; index < normalizedMaxBatches; index += 1) {
    const deleted = await cleanupBatch()
    total += deleted
    if (deleted < fullBatchSize) {
      break
    }
    if (index < normalizedMaxBatches - 1) {
      await pauseRetentionCleanupBatch()
    }
  }
  return total
}

async function runRetentionBatches(maxBatches: number, cleanupBatch: () => Promise<number>, fullBatchSize = 1): Promise<void> {
  const normalizedMaxBatches = Math.max(1, Math.trunc(maxBatches))
  for (let index = 0; index < normalizedMaxBatches; index += 1) {
    const deleted = await cleanupBatch()
    if (deleted < fullBatchSize) {
      break
    }
    if (index < normalizedMaxBatches - 1) {
      await pauseRetentionCleanupBatch()
    }
  }
}

function pauseRetentionCleanupBatch(): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, DATA_RETENTION_CLEANUP_BATCH_PAUSE_MS))
}

function addNumberResult(result: Record<string, number>, deleted: object): void {
  for (const [key, value] of Object.entries(deleted as Record<string, unknown>)) {
    result[key] = (result[key] ?? 0) + Number(value ?? 0)
  }
}

function sumNumbers(deleted: object): number {
  return Object.values(deleted as Record<string, unknown>).reduce<number>((sum, value) => sum + Number(value ?? 0), 0)
}

function postgresRetentionPolicy(settings: Record<string, unknown>): PostgresRetentionPolicy {
  return {
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
}

function usageStatsRetentionInput(now: number, retention: PostgresRetentionPolicy, timezone: string, batchSize: number) {
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

function systemMetricsRetentionInput(now: number, retention: PostgresRetentionPolicy, timezone: string, batchSize: number) {
  return {
    samplesCutoffIso: cutoffIso(now, retention.systemMetricsSampleDays),
    hourlyCutoffHour: cutoffHourKey(now, retention.systemMetricsHourlyDays, timezone),
    trendWindowCutoffDate: cutoffDateKey(now, retention.fixedWindowDays, timezone),
    limit: batchSize
  }
}

function cutoffIso(now: number, retentionDays: number): string {
  return new Date(now - retentionDays * dayMs).toISOString()
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

export async function runAuditHotRetentionCleanup(): Promise<void> {
  if (runtimeConfig.databaseDriver === 'postgres') return
  await cleanupExpiredAuditHotRetentionData()
}

export async function runExpiredDeletedAccountCleanup(): Promise<void> {
  try {
    const summary = await requestBackgroundWorkerDbService(
      { type: 'cleanup_expired_deleted_accounts' },
      { timeoutMs: expiredDeletedAccountCleanupDbServiceTimeoutMs }
    )
    if (!summary) {
      throw new Error('DB service 未返回逻辑删除 AI 账户清理结果')
    }
    for (const target of summary.recordCleanupTargets ?? []) {
      const enqueueResult = enqueueRecordMaintenanceJobWithResult({
        type: 'account_related_cleanup',
        accountId: target.accountId,
        systemAccountId: target.systemAccountId,
        relatedAccountIds: target.relatedAccountIds,
        authorizationIds: target.authorizationIds,
        teamScopeIds: target.teamScopeIds
      })
      if (!enqueueResult.queued) {
        logger.warn({
          event: 'background_expired_deleted_account_record_cleanup_enqueue_failed',
          accountId: target.accountId,
          systemAccountId: target.systemAccountId,
          droppedReason: enqueueResult.droppedReason
        }, '逻辑删除 AI 账户物理清理发现关联记录未清空，投递记录清理失败')
      }
    }
    if (summary.attempted > 0 || summary.orphanedAuthorizationInstances > 0) {
      logger.info({
        event: 'background_expired_deleted_account_cleanup_completed',
        ...summary
      }, '逻辑删除 AI 账户过期物理清理与孤儿授权实例扫尾完成')
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_expired_deleted_account_cleanup_failed' }), '超过一个月的逻辑删除 AI 账户物理清理失败')
    throw error
  }
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
