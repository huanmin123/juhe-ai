import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { cleanupAuditLogsBefore } from '../../storage/audit-logs.repository.js'
import { cleanupOperationLogsBefore } from '../../storage/operation-logs.repository.js'
import {
  cleanupExpiredSystemSessions,
  cleanupProcessedUsageRecordsBefore,
  cleanupSystemMetricsBefore,
  cleanupUsageStatsBucketsBefore
} from '../../storage/data-retention.repository.js'
import { getSettings } from '../../storage/settings.repository.js'
import { cleanupRuntimeLogIndex, runtimeLogIndexRetentionDays } from '../../storage/runtime-logs.repository.js'
import { dateKey, hourKey } from '../../storage/usage-stats-helpers.js'
import { readAuditLogSettings } from '../audit-logs/audit-log-settings.js'

const dayMs = 24 * 60 * 60 * 1000
const usageRecordRetentionMaxDays = 7
const statsRetentionMaxDays = 30
const systemMetricsRawRetentionMaxDays = 7
const operationLogRetentionMaxDays = 3650
const defaultCleanupBatchSize = 10000
const defaultCleanupMaxBatchesPerRun = 10

let cleanupRunning = false

export interface DataRetentionCleanupResult {
  operationLogs: number
  auditLogs: number
  runtimeLogs: number
  usageRecords: number
  usageStatsDaily: number
  usageModelDaily: number
  usageErrorDaily: number
  usageStatsHourly: number
  usageModelHourly: number
  usageErrorHourly: number
  systemMetricsSamples: number
  systemMetricsHourly: number
  systemSessions: number
}

export function cleanupExpiredRetainedData(): DataRetentionCleanupResult {
  if (runtimeConfig.processRole !== 'worker') {
    return emptyCleanupResult()
  }
  if (cleanupRunning) {
    return emptyCleanupResult()
  }

  cleanupRunning = true
  try {
    const settings = getSettings()
    const batchSize = settingNumber(settings, 'dataRetentionCleanupBatchSize', defaultCleanupBatchSize, 100, 50000)
    const maxBatches = settingNumber(settings, 'dataRetentionCleanupMaxBatchesPerRun', defaultCleanupMaxBatchesPerRun, 1, 100)
    const now = Date.now()
    const retention = {
      auditLogDays: readAuditLogSettings().retentionDays,
      operationLogDays: settingNumber(settings, 'operationLogRetentionDays', 365, 1, operationLogRetentionMaxDays),
      runtimeLogDays: runtimeLogIndexRetentionDays,
      usageRecordDays: settingNumber(settings, 'usageRecordRetentionDays', 7, 1, usageRecordRetentionMaxDays),
      statsDailyDays: settingNumber(settings, 'usageStatsDailyRetentionDays', 30, 1, statsRetentionMaxDays),
      statsHourlyDays: settingNumber(settings, 'usageStatsHourlyRetentionDays', 30, 1, statsRetentionMaxDays),
      systemMetricsSampleDays: settingNumber(settings, 'systemMetricsRetentionDays', 7, 1, systemMetricsRawRetentionMaxDays),
      systemMetricsHourlyDays: settingNumber(settings, 'systemMetricsHourlyRetentionDays', 30, 1, statsRetentionMaxDays)
    }

    const result = emptyCleanupResult()
    result.operationLogs = cleanupInBatches(() => cleanupOperationLogsBefore(cutoffIso(now, retention.operationLogDays), batchSize), batchSize, maxBatches)
    result.auditLogs = cleanupInBatches(() => cleanupAuditLogsBefore(cutoffIso(now, retention.auditLogDays), batchSize), batchSize, maxBatches)
    result.runtimeLogs = cleanupInBatches(() => cleanupRuntimeLogIndex(cutoffIso(now, retention.runtimeLogDays), batchSize), batchSize, maxBatches)
    result.usageRecords = cleanupInBatches(() => cleanupProcessedUsageRecordsBefore(cutoffIso(now, retention.usageRecordDays), batchSize), batchSize, maxBatches)

    const stats = cleanupUsageStatsBucketsBefore({
      dailyCutoffDate: cutoffDateKey(now, retention.statsDailyDays),
      hourlyCutoffHour: cutoffHourKey(now, retention.statsHourlyDays)
    })
    Object.assign(result, stats)

    const metrics = cleanupSystemMetricsBefore({
      samplesCutoffIso: cutoffIso(now, retention.systemMetricsSampleDays),
      hourlyCutoffHour: cutoffHourKey(now, retention.systemMetricsHourlyDays)
    })
    Object.assign(result, metrics)

    result.systemSessions = cleanupExpiredSystemSessions(new Date(now).toISOString())

    logger.info({
      event: 'data_retention_cleanup_completed',
      deleted: result,
      retention,
      batchSize,
      maxBatches
    }, '数据保留清理完成')

    return result
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'data_retention_cleanup_failed' }), '数据保留清理失败')
    throw error
  } finally {
    cleanupRunning = false
  }
}

function cleanupInBatches(cleanupBatch: () => number, batchSize: number, maxBatches: number): number {
  let total = 0
  for (let index = 0; index < maxBatches; index += 1) {
    const deleted = cleanupBatch()
    total += deleted
    if (deleted < batchSize) {
      break
    }
  }
  return total
}

function cutoffIso(now: number, retentionDays: number): string {
  return new Date(now - retentionDays * dayMs).toISOString()
}

function cutoffDateKey(now: number, retentionDays: number): string {
  return dateKey(new Date(now - retentionDays * dayMs))
}

function cutoffHourKey(now: number, retentionDays: number): string {
  return hourKey(new Date(now - retentionDays * dayMs))
}

function settingNumber(settings: Record<string, unknown>, key: string, fallback: number, min: number, max: number): number {
  const value = settings[key]
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? Math.min(Math.max(Math.trunc(number), min), max) : fallback
}

function emptyCleanupResult(): DataRetentionCleanupResult {
  return {
    operationLogs: 0,
    auditLogs: 0,
    runtimeLogs: 0,
    usageRecords: 0,
    usageStatsDaily: 0,
    usageModelDaily: 0,
    usageErrorDaily: 0,
    usageStatsHourly: 0,
    usageModelHourly: 0,
    usageErrorHourly: 0,
    systemMetricsSamples: 0,
    systemMetricsHourly: 0,
    systemSessions: 0
  }
}
