import { Router } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText } from '../../shared/query-values.js'
import {
  getAccountUsageStatsOverviewPage,
  type AccountListOptions,
  type AccountListSchedulableFilter
} from '../../storage/repositories.js'
import {
  getAiPerformanceOverview,
  getSystemMetricsOverview,
  getUsageStatsOverview,
  listAiPerformanceAccountOptions,
} from '../../storage/usage-stats.repository.js'
import { normalizeAccountUsageStatsRange, todayDateKey, usageStatsTimezone } from '../../storage/usage-stats-helpers.js'
import { fixedUsageStatsDateKeys } from '../../storage/usage-stats-window-helpers.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { requestServerRuntimeSnapshot } from '../db-service/db-service-ipc.js'
import type { DbServiceRuntimeQueueSnapshot } from '../db-service/db-service-types.js'

export const statsRouter = Router()

const usageOverviewQuerySchema = z.object({
  startDate: z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/, '开始日期格式应为 YYYY-MM-DD').optional(),
  endDate: z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/, '结束日期格式应为 YYYY-MM-DD').optional()
})

const aiPerformanceAccountOptionsQuerySchema = z.object({
  keyword: z.string().trim().optional(),
  limit: z.coerce.number().int().min(1).max(50).optional()
})

interface BackgroundScheduledJobSnapshot {
  name: string
  intervalMs: number
  running: boolean
  lastStartedAt?: string
  lastFinishedAt?: string
  lastSuccessAt?: string
  lastErrorAt?: string
  lastError?: string
  lastDurationMs?: number
  maxDurationMs?: number
  runCount: number
  successCount: number
  failureCount: number
  skippedCount: number
}

interface BackgroundRetryQueueSnapshot {
  name: string
  pendingCount: number
  runningCount: number
  nextRunAt?: string
}

interface BackgroundLocalQueueSnapshot extends DbServiceRuntimeQueueSnapshot {
  name: string
}

interface BackgroundJobRuntimeRow extends BackgroundScheduledJobSnapshot {
  workerRole?: string
  retryQueue?: BackgroundRetryQueueSnapshot
  localQueue?: BackgroundLocalQueueSnapshot
}

interface BackgroundJobsSnapshot {
  workerRole?: string
  jobs?: BackgroundScheduledJobSnapshot[]
}

statsRouter.get('/usage-overview', (req, res) => {
  const parsed = usageOverviewQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '统计日期范围不合法')))
    return
  }
  res.json(ok(getUsageStatsOverview(getRequestAccessScope(req.query.systemAccountId), normalizeStatsDateRange(parsed.data))))
})

statsRouter.get('/ai-performance', (req, res) => {
  const parsed = usageOverviewQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '性能监控日期范围不合法')))
    return
  }
  res.json(ok(getAiPerformanceOverview(getRequestAccessScope(req.query.systemAccountId), normalizeStatsDateRange(parsed.data), parseAccountIds(req.query.accountIds))))
})

statsRouter.get('/ai-performance/accounts', (req, res) => {
  const parsed = aiPerformanceAccountOptionsQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'AI账户筛选参数不合法')))
    return
  }
  res.json(ok(listAiPerformanceAccountOptions(getRequestAccessScope(req.query.systemAccountId), {
    keyword: parsed.data.keyword,
    accountIds: parseAccountIds(req.query.accountIds),
    limit: parsed.data.limit
  })))
})

statsRouter.get('/account-usage', (req, res) => {
  res.json(ok(getAccountUsageStatsOverviewPage(getRequestAccessScope(req.query.systemAccountId), parseAccountUsageOptions(req.query))))
})

function parseAccountUsageOptions(query: Record<string, unknown>): Omit<AccountListOptions, 'type'> & { range: ReturnType<typeof normalizeAccountUsageStatsRange>; accountIds?: string[] } {
  const timezone = usageStatsTimezone()
  const startDate = optionalQueryText(query.startDate)
  const endDate = optionalQueryText(query.endDate)
  const range = normalizeAccountUsageStatsRange(
    startDate || endDate ? { startDate, endDate } : defaultAccountUsageDateRange(timezone),
    timezone
  )
  const pageSize = integerQueryValue(query.pageSize)
  return {
    page: integerQueryValue(query.page),
    pageSize: pageSize ?? 10,
    keyword: optionalQueryText(query.keyword),
    accountIds: parseAccountIds(query.accountIds),
    schedulable: schedulableQueryValue(query.schedulable),
    range
  }
}

function defaultAccountUsageDateRange(timezone: string): { startDate: string; endDate: string } {
  const dateKeys = fixedUsageStatsDateKeys(timezone)
  const today = todayDateKey(timezone)
  return {
    startDate: dateKeys[0] ?? today,
    endDate: dateKeys[dateKeys.length - 1] ?? today
  }
}

function schedulableQueryValue(value: unknown): AccountListSchedulableFilter | undefined {
  const text = optionalQueryText(value)
  return text === 'all' || text === 'enabled' || text === 'disabled' || text === 'cooling' ? text : undefined
}

function parseAccountIds(value: unknown): string[] {
  const rawValues = Array.isArray(value) ? value : typeof value === 'string' ? [value] : []
  const seen = new Set<string>()
  const ids: string[] = []
  for (const rawValue of rawValues) {
    if (typeof rawValue !== 'string') continue
    for (const item of rawValue.split(',')) {
      const id = item.trim()
      if (!id || seen.has(id)) continue
      seen.add(id)
      ids.push(id)
    }
  }
  return ids
}

function backgroundJobsFromSnapshot(snapshot: BackgroundJobsSnapshot | undefined): BackgroundJobRuntimeRow[] | undefined {
  return snapshot?.jobs?.map((job) => ({ ...job, workerRole: snapshot.workerRole }))
}

function retryQueueBackgroundJobRow(
  name: string,
  workerRole: string | undefined,
  queue: BackgroundRetryQueueSnapshot | undefined
): BackgroundJobRuntimeRow | undefined {
  if (!queue) return undefined
  return emptyBackgroundJobRow({
    name,
    workerRole,
    running: queue.runningCount > 0,
    retryQueue: queue
  })
}

function localQueueBackgroundJobRow(
  name: string,
  workerRole: string | undefined,
  queue: DbServiceRuntimeQueueSnapshot | undefined
): BackgroundJobRuntimeRow | undefined {
  if (!queue) return undefined
  const queueLength = numberValue(queue.queueLength)
  const flushFailureCount = numberValue(queue.flushFailureCount)
  const completedCount = numberValue(queue.completedCount)
  return emptyBackgroundJobRow({
    name,
    workerRole,
    running: queueLength > 0,
    lastSuccessAt: queue.flushLastSuccessAt,
    lastFinishedAt: queue.flushLastSuccessAt,
    lastError: typeof queue.flushLastError === 'string' ? queue.flushLastError : undefined,
    runCount: completedCount + flushFailureCount,
    successCount: completedCount,
    failureCount: flushFailureCount,
    localQueue: { ...queue, name }
  })
}

function emptyBackgroundJobRow(input: {
  name: string
  workerRole?: string
  running?: boolean
  lastFinishedAt?: string
  lastSuccessAt?: string
  lastError?: string
  runCount?: number
  successCount?: number
  failureCount?: number
  retryQueue?: BackgroundRetryQueueSnapshot
  localQueue?: BackgroundLocalQueueSnapshot
}): BackgroundJobRuntimeRow {
  return {
    name: input.name,
    workerRole: input.workerRole,
    intervalMs: 0,
    running: input.running ?? false,
    lastFinishedAt: input.lastFinishedAt,
    lastSuccessAt: input.lastSuccessAt,
    lastError: input.lastError,
    runCount: input.runCount ?? 0,
    successCount: input.successCount ?? 0,
    failureCount: input.failureCount ?? 0,
    skippedCount: 0,
    retryQueue: input.retryQueue,
    localQueue: input.localQueue
  }
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

statsRouter.get('/system-metrics', requireAdmin, async (req, res) => {
  const parsed = usageOverviewQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '监控日期范围不合法')))
    return
  }
  const overview = getSystemMetricsOverview(normalizeStatsDateRange(parsed.data))
  const runtime = await requestServerRuntimeSnapshot(1000).catch(() => undefined)
  const ingestWorkerSnapshot = runtime?.ingestWorker?.snapshot
  const statsWorkerSnapshot = runtime?.statsWorker?.snapshot
  const opsWorkerSnapshot = runtime?.opsWorker?.snapshot
  const accountQualityFailurePrecheckSnapshot = opsWorkerSnapshot?.accountQualityFailurePrecheckQueue
    ?? statsWorkerSnapshot?.accountQualityFailurePrecheckQueue
  const backgroundQueueRows = [
    retryQueueBackgroundJobRow('manual-account-test-queue', opsWorkerSnapshot?.workerRole, opsWorkerSnapshot?.manualAccountTestQueue),
    retryQueueBackgroundJobRow(
      'account-quality-failure-precheck-queue',
      accountQualityFailurePrecheckSnapshot ? (opsWorkerSnapshot?.workerRole ?? statsWorkerSnapshot?.workerRole) : undefined,
      accountQualityFailurePrecheckSnapshot
    ),
    localQueueBackgroundJobRow('record-maintenance-ingest-queue', ingestWorkerSnapshot?.workerRole, ingestWorkerSnapshot?.recordMaintenanceQueue),
    localQueueBackgroundJobRow('record-maintenance-stats-queue', statsWorkerSnapshot?.workerRole, statsWorkerSnapshot?.recordMaintenanceQueue)
  ].filter((row): row is BackgroundJobRuntimeRow => Boolean(row))
  const backgroundJobGroups = [
    backgroundJobsFromSnapshot(ingestWorkerSnapshot),
    backgroundJobsFromSnapshot(statsWorkerSnapshot),
    opsWorkerSnapshot?.jobs?.map((job) => {
      const roleAwareJob = { ...job, workerRole: opsWorkerSnapshot.workerRole }
      if (job.name === 'account-health-check' && opsWorkerSnapshot.accountHealthCheckQueue) {
        return { ...roleAwareJob, retryQueue: opsWorkerSnapshot.accountHealthCheckQueue }
      }
      if (job.name === 'cooldown-account-retest' && opsWorkerSnapshot.cooldownAccountRetestQueue) {
        return { ...roleAwareJob, retryQueue: opsWorkerSnapshot.cooldownAccountRetestQueue }
      }
      if (job.name === 'account-api-key-cooldown-retest' && opsWorkerSnapshot.accountApiKeyCooldownRetestQueue) {
        return { ...roleAwareJob, retryQueue: opsWorkerSnapshot.accountApiKeyCooldownRetestQueue }
      }
      return roleAwareJob
    }),
    backgroundQueueRows.length > 0 ? backgroundQueueRows : undefined
  ]
  const backgroundJobs = backgroundJobGroups.some(Array.isArray)
    ? backgroundJobGroups.flatMap((items) => items ?? [])
    : undefined
  res.json(ok({
    ...overview,
    runtimeSnapshotAvailable: Boolean(runtime),
    ingestWorkerSnapshotAvailable: Boolean(ingestWorkerSnapshot),
    statsWorkerSnapshotAvailable: Boolean(statsWorkerSnapshot),
    opsWorkerSnapshotAvailable: Boolean(opsWorkerSnapshot),
    ingestWorker: runtime?.ingestWorker
      ? {
        pid: runtime.ingestWorker.pid ?? null,
        ready: runtime.ingestWorker.ready,
        snapshotAvailable: Boolean(ingestWorkerSnapshot)
      }
      : null,
    statsWorker: runtime?.statsWorker
      ? {
        pid: runtime.statsWorker.pid ?? null,
        ready: runtime.statsWorker.ready,
        snapshotAvailable: Boolean(statsWorkerSnapshot)
      }
      : null,
    opsWorker: runtime?.opsWorker
      ? {
        pid: runtime.opsWorker.pid ?? null,
        ready: runtime.opsWorker.ready,
        snapshotAvailable: Boolean(opsWorkerSnapshot)
      }
      : null,
    backgroundJobsAvailable: Array.isArray(backgroundJobs),
    backgroundJobs: backgroundJobs ?? null
  }))
})

function normalizeStatsDateRange(input: { startDate?: string; endDate?: string }) {
  const timezone = usageStatsTimezone()
  const today = todayDateKey(timezone)
  const startDate = input.startDate ?? input.endDate ?? today
  const endDate = input.endDate ?? input.startDate ?? today
  return normalizeAccountUsageStatsRange({ startDate, endDate }, timezone)
}
