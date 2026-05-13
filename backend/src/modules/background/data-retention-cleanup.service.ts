import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { cleanupAuditLogsByRetention } from '../../storage/audit-logs.repository.js'
import { cleanupOperationLogsBefore } from '../../storage/operation-logs.repository.js'
import {
  cleanupExpiredSystemSessions,
  cleanupProcessedUsageRecordsBefore,
  cleanupSystemMetricsBefore,
  cleanupUsageStatsBucketsBefore
} from '../../storage/data-retention.repository.js'
import { getSettings } from '../../storage/settings.repository.js'
import { cleanupRuntimeLogIndex, runtimeLogIndexRetentionDays } from '../../storage/runtime-logs.repository.js'
import { cleanupTableStorageSnapshotsBefore, tableMonitorSampleRetentionDays } from '../../storage/table-monitor.repository.js'
import { dateKey, hourKey, minuteKey, monthKey, usageStatsTimezone, weekKey } from '../../storage/usage-stats-helpers.js'
import { getDatabase } from '../../storage/database.js'
import { readAuditLogSettings } from '../audit-logs/audit-log-settings.js'

const dayMs = 24 * 60 * 60 * 1000
const usageRecordRetentionMaxDays = 7
const statsMinuteRetentionMaxHours = 24 * 14
const statsHourlyRetentionMaxDays = 180
const statsDailyRetentionMaxDays = 800
const statsWeeklyRetentionMaxWeeks = 260
const statsMonthlyRetentionMaxMonths = 60
const rankSnapshotRetentionMaxDays = 365
const systemMetricsRawRetentionMaxDays = 7
const statsRetentionMaxDays = 30
const operationLogRetentionMaxDays = 3650
const defaultCleanupBatchSize = 10000
const defaultCleanupMaxBatchesPerRun = 10

let cleanupRunning = false

export interface DataRetentionCleanupResult {
  operationLogs: number
  auditLogs: number
  runtimeLogs: number
  usageRecords: number
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
  usageRankSnapshots: number
  usageOverviewSummaryWindows: number
  usageOverviewTrendWindows: number
  usageModelRankWindows: number
  usageErrorRankWindows: number
  aiPerformanceSummaryWindows: number
  usageQuotaHourlyWindows: number
  usageScopeRangeWindows: number
  systemMetricsSamples: number
  systemMetricsHourly: number
  tableStorageSnapshots: number
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
    const timezone = usageStatsTimezone()
    const batchSize = settingNumber(settings, 'dataRetentionCleanupBatchSize', defaultCleanupBatchSize, 100, 50000)
    const maxBatches = settingNumber(settings, 'dataRetentionCleanupMaxBatchesPerRun', defaultCleanupMaxBatchesPerRun, 1, 100)
    const now = Date.now()
    const retention = {
      auditLogSuccessDays: readAuditLogSettings().successRetentionDays,
      auditLogFailureDays: readAuditLogSettings().failureRetentionDays,
      auditErrorGroupDays: readAuditLogSettings().errorGroupRetentionDays,
      operationLogDays: settingNumber(settings, 'operationLogRetentionDays', 365, 1, operationLogRetentionMaxDays),
      runtimeLogDays: runtimeLogIndexRetentionDays,
      usageRecordDays: settingNumber(settings, 'usageRecordRetentionDays', 7, 1, usageRecordRetentionMaxDays),
      statsMinuteHours: settingNumber(settings, 'usageStatsMinuteRetentionHours', 48, 1, statsMinuteRetentionMaxHours),
      statsHourlyDays: settingNumber(settings, 'usageStatsHourlyRetentionDays', 60, 1, statsHourlyRetentionMaxDays),
      statsDailyDays: settingNumber(settings, 'usageStatsDailyRetentionDays', 400, 1, statsDailyRetentionMaxDays),
      statsWeeklyWeeks: settingNumber(settings, 'usageStatsWeeklyRetentionWeeks', 104, 1, statsWeeklyRetentionMaxWeeks),
      statsMonthlyMonths: settingNumber(settings, 'usageStatsMonthlyRetentionMonths', 24, 1, statsMonthlyRetentionMaxMonths),
      rankSnapshotDays: settingNumber(settings, 'usageRankSnapshotRetentionDays', 30, 1, rankSnapshotRetentionMaxDays),
      systemMetricsSampleDays: settingNumber(settings, 'systemMetricsRetentionDays', 7, 1, systemMetricsRawRetentionMaxDays),
      systemMetricsHourlyDays: settingNumber(settings, 'systemMetricsHourlyRetentionDays', 30, 1, statsRetentionMaxDays),
      tableStorageSnapshotDays: tableMonitorSampleRetentionDays
    }

    const result = emptyCleanupResult()
    result.operationLogs = cleanupInBatches(() => cleanupOperationLogsBefore(cutoffIso(now, retention.operationLogDays), batchSize), batchSize, maxBatches)
    result.auditLogs = cleanupInBatches(() => cleanupAuditLogsByRetention({
      successCutoffCreatedAt: cutoffIso(now, retention.auditLogSuccessDays),
      failureCutoffCreatedAt: cutoffIso(now, retention.auditLogFailureDays),
      errorGroupCutoffUpdatedAt: cutoffIso(now, retention.auditErrorGroupDays),
      limit: batchSize
    }), batchSize, maxBatches)
    result.runtimeLogs = cleanupInBatches(() => cleanupRuntimeLogIndex(cutoffIso(now, retention.runtimeLogDays), batchSize), batchSize, maxBatches)
    result.usageRecords = cleanupInBatches(() => cleanupProcessedUsageRecordsBefore(cutoffIso(now, retention.usageRecordDays), batchSize), batchSize, maxBatches)

    const stats = cleanupUsageStatsBucketsBefore({
      minuteCutoffMinute: cutoffMinuteKey(now, retention.statsMinuteHours, timezone),
      hourlyCutoffHour: cutoffHourKey(now, retention.statsHourlyDays, timezone),
      dailyCutoffDate: cutoffDateKey(now, retention.statsDailyDays, timezone),
      weeklyCutoffWeek: cutoffWeekKey(now, retention.statsWeeklyWeeks, timezone),
      monthlyCutoffMonth: cutoffMonthKey(now, retention.statsMonthlyMonths, timezone),
      rankSnapshotCutoffIso: cutoffIso(now, retention.rankSnapshotDays)
    })
    Object.assign(result, stats)

    const metrics = cleanupSystemMetricsBefore({
      samplesCutoffIso: cutoffIso(now, retention.systemMetricsSampleDays),
      hourlyCutoffHour: cutoffHourKey(now, retention.systemMetricsHourlyDays, timezone)
    })
    Object.assign(result, metrics)

    result.tableStorageSnapshots = cleanupInBatches(
      () => cleanupTableStorageSnapshotsBefore(cutoffIso(now, retention.tableStorageSnapshotDays), batchSize),
      batchSize,
      maxBatches
    )

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
    usageRankSnapshots: 0,
    usageOverviewSummaryWindows: 0,
    usageOverviewTrendWindows: 0,
    usageModelRankWindows: 0,
    usageErrorRankWindows: 0,
    aiPerformanceSummaryWindows: 0,
    usageQuotaHourlyWindows: 0,
    usageScopeRangeWindows: 0,
    systemMetricsSamples: 0,
    systemMetricsHourly: 0,
    tableStorageSnapshots: 0,
    systemSessions: 0
  }
}
