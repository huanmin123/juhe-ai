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

export const statsRouter = Router()

const usageOverviewQuerySchema = z.object({
  startDate: z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/, '开始日期格式应为 YYYY-MM-DD').optional(),
  endDate: z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/, '结束日期格式应为 YYYY-MM-DD').optional()
})

const aiPerformanceAccountOptionsQuerySchema = z.object({
  keyword: z.string().trim().optional(),
  limit: z.coerce.number().int().min(1).max(50).optional()
})

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

statsRouter.get('/system-metrics', requireAdmin, async (req, res) => {
  const parsed = usageOverviewQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '监控日期范围不合法')))
    return
  }
  const overview = getSystemMetricsOverview(normalizeStatsDateRange(parsed.data))
  const runtime = await requestServerRuntimeSnapshot(1000).catch(() => undefined)
  const workerSnapshot = runtime?.worker?.snapshot
  const metricsWorkerSnapshot = runtime?.metricsWorker?.snapshot
  const ingestWorkerSnapshot = runtime?.ingestWorker?.snapshot
  const statsWorkerSnapshot = runtime?.statsWorker?.snapshot
  const snapshotWorkerSnapshot = runtime?.snapshotWorker?.snapshot
  const probeWorkerSnapshot = runtime?.probeWorker?.snapshot
  const maintenanceWorkerSnapshot = runtime?.maintenanceWorker?.snapshot
  const backgroundJobGroups = [
    workerSnapshot?.jobs?.map((job) => ({ ...job, workerRole: workerSnapshot.workerRole })),
    metricsWorkerSnapshot?.jobs?.map((job) => ({ ...job, workerRole: metricsWorkerSnapshot.workerRole })),
    ingestWorkerSnapshot?.jobs?.map((job) => ({ ...job, workerRole: ingestWorkerSnapshot.workerRole })),
    statsWorkerSnapshot?.jobs?.map((job) => ({ ...job, workerRole: statsWorkerSnapshot.workerRole })),
    snapshotWorkerSnapshot?.jobs?.map((job) => ({ ...job, workerRole: snapshotWorkerSnapshot.workerRole })),
    probeWorkerSnapshot?.jobs?.map((job) => {
      const roleAwareJob = { ...job, workerRole: probeWorkerSnapshot.workerRole }
      if (job.name === 'account-health-check' && probeWorkerSnapshot.accountHealthCheckQueue) {
        return { ...roleAwareJob, retryQueue: probeWorkerSnapshot.accountHealthCheckQueue }
      }
      if (job.name === 'cooldown-account-retest' && probeWorkerSnapshot.cooldownAccountRetestQueue) {
        return { ...roleAwareJob, retryQueue: probeWorkerSnapshot.cooldownAccountRetestQueue }
      }
      if (job.name === 'account-api-key-cooldown-retest' && probeWorkerSnapshot.accountApiKeyCooldownRetestQueue) {
        return { ...roleAwareJob, retryQueue: probeWorkerSnapshot.accountApiKeyCooldownRetestQueue }
      }
      if (job.name === 'account-quality-refresh' && probeWorkerSnapshot.accountQualityFailurePrecheckQueue) {
        return { ...roleAwareJob, retryQueue: probeWorkerSnapshot.accountQualityFailurePrecheckQueue }
      }
      return roleAwareJob
    }),
    maintenanceWorkerSnapshot?.jobs?.map((job) => ({ ...job, workerRole: maintenanceWorkerSnapshot.workerRole }))
  ]
  const backgroundJobs = backgroundJobGroups.some(Array.isArray)
    ? backgroundJobGroups.flatMap((items) => items ?? [])
    : undefined
  res.json(ok({
    ...overview,
    runtimeSnapshotAvailable: Boolean(runtime),
    workerSnapshotAvailable: Boolean(workerSnapshot),
    metricsWorkerSnapshotAvailable: Boolean(metricsWorkerSnapshot),
    ingestWorkerSnapshotAvailable: Boolean(ingestWorkerSnapshot),
    statsWorkerSnapshotAvailable: Boolean(statsWorkerSnapshot),
    snapshotWorkerSnapshotAvailable: Boolean(snapshotWorkerSnapshot),
    probeWorkerSnapshotAvailable: Boolean(probeWorkerSnapshot),
    maintenanceWorkerSnapshotAvailable: Boolean(maintenanceWorkerSnapshot),
    metricsWorker: runtime?.metricsWorker
      ? {
        pid: runtime.metricsWorker.pid ?? null,
        ready: runtime.metricsWorker.ready,
        snapshotAvailable: Boolean(metricsWorkerSnapshot)
      }
      : null,
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
    snapshotWorker: runtime?.snapshotWorker
      ? {
        pid: runtime.snapshotWorker.pid ?? null,
        ready: runtime.snapshotWorker.ready,
        snapshotAvailable: Boolean(snapshotWorkerSnapshot)
      }
      : null,
    probeWorker: runtime?.probeWorker
      ? {
        pid: runtime.probeWorker.pid ?? null,
        ready: runtime.probeWorker.ready,
        snapshotAvailable: Boolean(probeWorkerSnapshot)
      }
      : null,
    maintenanceWorker: runtime?.maintenanceWorker
      ? {
        pid: runtime.maintenanceWorker.pid ?? null,
        ready: runtime.maintenanceWorker.ready,
        snapshotAvailable: Boolean(maintenanceWorkerSnapshot)
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
