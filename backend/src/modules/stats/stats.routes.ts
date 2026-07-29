import { Router, type NextFunction, type Request, type Response } from 'express'
import { z } from 'zod'

import type { AccountUsageStatsRange } from '../../domain/types.js'
import { badRequest, firstIssueMessage, ok, sendNotFound } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText } from '../../shared/query-values.js'
import {
  getAccountUsageStatsOverviewPageAsync,
  type AccountListOptions,
  type AccountListSchedulableFilter
} from '../../storage/repositories.js'
import { getAccountUsageStatsSummaryAsync, getAccountUsageStatsTrendAsync, listAccountUsageOptionsAsync } from '../../storage/account-usage.repository.js'
import { getAiHealthHourDetailAsync, getAiHealthListAsync } from '../../storage/account-health-monitor.repository.js'
import {
  getAiPerformanceBaseAsync,
  getAiPerformanceSeriesAsync,
  getUsageStatsOverviewDailyTrendAsync,
  getUsageStatsOverviewErrorsAsync,
  getUsageStatsOverviewHourlyTrendAsync,
  getUsageStatsOverviewModelDistributionAsync,
  getUsageStatsOverviewSummaryAsync,
  getSystemMetricsOverviewAsync,
  getSystemMetricsTrendAsync,
  getUsageStatsOverviewAsync,
  listAiPerformanceAccountOptionsAsync,
} from '../../storage/usage-stats.repository.js'
import { dateKey, normalizeAccountUsageStatsRange, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'
import { DAY_MS, fixedUsageStatsDefaultRange } from '../../storage/usage-stats-window-helpers.js'
import type { RedisStreamQueueRuntime } from '../../shared/redis-stream-queue.js'
import { getAuditLogRedisStreamRuntime } from '../audit-logs/audit-log-queue.service.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { buildBackgroundQueueHealthSnapshot, type BackgroundQueueHealthItem } from '../background/background-queue-health.service.js'
import { requestServerSystemMetricsRuntimeSnapshot } from '../db-service/db-service-ipc.js'
import type { DbServiceRuntimeQueueSnapshot, DbServiceSystemMetricsRuntimeSnapshot } from '../db-service/db-service-types.js'
import { getUsageRecordRedisStreamRuntime } from '../gateway/usage/record-queue.service.js'
import { getOperationLogRedisStreamRuntime } from '../operation-logs/operation-log-queue.service.js'
import { getPublicApiLogRedisStreamRuntime } from '../public-api-logs/public-api-log-queue.service.js'
import { getRecordMaintenanceRedisStreamRuntime } from '../record-maintenance/record-maintenance-queue.service.js'

export const statsRouter = Router()

type UsageOverviewSectionLoader<T> = (access: AccessScope | undefined, range: AccountUsageStatsRange) => Promise<T>

const usageOverviewQuerySchema = z.object({
  startDate: z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/, '开始日期格式应为 YYYY-MM-DD').optional(),
  endDate: z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/, '结束日期格式应为 YYYY-MM-DD').optional()
})

const aiPerformanceAccountOptionsQuerySchema = z.object({
  keyword: z.string().trim().optional(),
  limit: z.coerce.number().int().min(1).max(50).optional()
})

const aiHealthQuerySchema = z.object({
  hours: z.coerce.number().int().min(1).max(31 * 24).optional(),
  keyword: z.string().trim().max(200).optional(),
  page: z.coerce.number().int().min(1).optional(),
  pageSize: z.coerce.number().int().min(10).max(50).optional()
})

const aiHealthHourDetailQuerySchema = z.object({
  accountId: z.string().trim().min(1).max(200),
  statHour: z.string().trim().regex(/^\d{4}-\d{2}-\d{2}T(?:[01]\d|2[0-3])$/, '统计小时格式应为 YYYY-MM-DDTHH')
})

const accountUsageOptionsQuerySchema = z.object({
  keyword: z.string().trim().max(200).optional(),
  limit: z.coerce.number().int().min(1).max(50).optional()
})

async function handleUsageOverviewSectionRequest<T>(
  req: Request,
  res: Response,
  next: NextFunction,
  load: UsageOverviewSectionLoader<T>
): Promise<void> {
  const parsed = usageOverviewQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '统计日期范围不合法')))
    return
  }
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const range = await normalizeUsageOverviewDateRangeAsync(parsed.data)
    res.json(ok(await load(access, range)))
  } catch (error) {
    next(error)
  }
}

interface BackgroundScheduledJobSnapshot {
  name: string
  intervalMs: number
  initialDelayMs?: number
  stablePhaseOffsetMs?: number
  scheduleMode?: 'fixedRate' | 'fixedDelay'
  overlapPolicy?: 'skip' | 'coalesceOne'
  timeoutMs?: number
  resourceLane?: string
  running: boolean
  pending?: boolean
  queuedForLane?: boolean
  timedOut?: boolean
  overdueMs?: number
  nextRunAt?: string
  runningSince?: string
  lastScheduledAt?: string
  lastStartedAt?: string
  lastFinishedAt?: string
  lastSuccessAt?: string
  lastErrorAt?: string
  lastError?: string
  lastWarningAt?: string
  lastWarning?: string
  lastSkipAt?: string
  lastSkipReason?: string
  lastOutcome?: 'success' | 'partial' | 'failure' | 'timeout' | 'skipped'
  leaseState?: 'not_required' | 'acquired' | 'busy' | 'lost'
  lastDurationMs?: number
  maxDurationMs?: number
  consecutiveFailureCount?: number
  runCount: number
  successCount: number
  failureCount: number
  partialCount: number
  skippedCount: number
  taskSkippedCount?: number
  coalescedCount?: number
  timedOutCount?: number
}

interface BackgroundRetryQueueSnapshot {
  name: string
  pendingCount: number
  runningCount: number
  nextRunAt?: string
}

interface BackgroundLocalQueueSnapshot extends DbServiceRuntimeQueueSnapshot {
  name: string
  queueType?: string
  nextRunAt?: string
  runningCount?: number
  consumers?: number
  rejectedCount?: number
  expiredCount?: number
  timedOutCount?: number
  failedCount?: number
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

statsRouter.get('/usage-overview', async (req, res, next) => {
  const parsed = usageOverviewQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '统计日期范围不合法')))
    return
  }
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const range = await normalizeUsageOverviewDateRangeAsync(parsed.data)
    const overview = await getUsageStatsOverviewAsync(access, range)
    res.json(ok(overview))
  } catch (error) {
    next(error)
  }
})

statsRouter.get('/usage-overview/summary', async (req, res, next) => {
  await handleUsageOverviewSectionRequest(req, res, next, (access, range) => getUsageStatsOverviewSummaryAsync(access, range))
})

statsRouter.get('/usage-overview/daily-trend', async (req, res, next) => {
  await handleUsageOverviewSectionRequest(req, res, next, (access, range) => getUsageStatsOverviewDailyTrendAsync(access, range))
})

statsRouter.get('/usage-overview/hourly-trend', async (req, res, next) => {
  await handleUsageOverviewSectionRequest(req, res, next, (access, range) => getUsageStatsOverviewHourlyTrendAsync(access, range))
})

statsRouter.get('/usage-overview/model-distribution', async (req, res, next) => {
  await handleUsageOverviewSectionRequest(req, res, next, (access, range) => getUsageStatsOverviewModelDistributionAsync(access, range))
})

statsRouter.get('/usage-overview/errors', async (req, res, next) => {
  await handleUsageOverviewSectionRequest(req, res, next, (access, range) => getUsageStatsOverviewErrorsAsync(access, range))
})

statsRouter.get('/usage-window', async (_req, res, next) => {
  try {
    const timezone = await usageStatsTimezoneAsync()
    const range = fixedUsageStatsDefaultRange(timezone)
    res.json(ok({
      timezone,
      startDate: range.startDate,
      endDate: range.endDate,
      days: range.days,
      maxDays: range.maxDays
    }))
  } catch (error) {
    next(error)
  }
})

statsRouter.get('/ai-performance', async (req, res, next) => {
  if (hasAiPerformanceAccountIdsQuery(req.query)) {
    res.status(400).json(badRequest('AI 性能基础数据不接受 accountIds，请使用 /ai-performance/series'))
    return
  }
  const parsed = usageOverviewQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '性能监控日期范围不合法')))
    return
  }
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const range = await normalizeStatsDateRangeAsync(parsed.data)
    res.json(ok(await getAiPerformanceBaseAsync(access, range)))
  } catch (error) {
    next(error)
  }
})

statsRouter.get('/ai-performance/series', async (req, res, next) => {
  const parsed = usageOverviewQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '性能监控日期范围不合法')))
    return
  }
  const accountIds = parseAiPerformanceSeriesAccountIds(req.query)
  if (!accountIds.success) {
    res.status(400).json(badRequest(accountIds.message))
    return
  }
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const range = await normalizeStatsDateRangeAsync(parsed.data)
    res.json(ok(await getAiPerformanceSeriesAsync(access, range, accountIds.data)))
  } catch (error) {
    next(error)
  }
})

statsRouter.get('/ai-performance/accounts', async (req, res, next) => {
  const parsed = aiPerformanceAccountOptionsQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'AI账户筛选参数不合法')))
    return
  }
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const query = {
      keyword: parsed.data.keyword,
      accountIds: parseAccountIds(req.query.accountIds),
      limit: parsed.data.limit
    }
    const options = await listAiPerformanceAccountOptionsAsync(access, query)
    res.json(ok(options))
  } catch (error) {
    next(error)
  }
})

statsRouter.get('/ai-health', async (req, res, next) => {
  const parsed = aiHealthQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'AI 健康监控参数不合法')))
    return
  }
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    res.json(ok(await getAiHealthListAsync(access, parsed.data)))
  } catch (error) {
    next(error)
  }
})

statsRouter.get('/ai-health/hour-detail', async (req, res, next) => {
  const parsed = aiHealthHourDetailQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'AI 健康详情参数不合法')))
    return
  }
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const detail = await getAiHealthHourDetailAsync(access, parsed.data.accountId, parsed.data.statHour)
    if (!detail) {
      sendNotFound(res, 'AI 账户不存在或不可访问')
      return
    }
    res.json(ok(detail))
  } catch (error) {
    next(error)
  }
})

statsRouter.get('/account-usage', async (req, res, next) => {
  try {
    if (req.query.includeSummary !== undefined) {
      res.status(400).json(badRequest('account-usage 列表不支持 includeSummary，请使用 /account-usage/summary'))
      return
    }
    const timezone = await usageStatsTimezoneAsync()
    const access = getRequestAccessScope(req.query.systemAccountId)
    const query = parseAccountUsageOptions(req.query, timezone)
    const overview = await getAccountUsageStatsOverviewPageAsync(access, query)
    res.json(ok(overview))
  } catch (error) {
    next(error)
  }
})

statsRouter.get('/account-usage/options', async (req, res, next) => {
  const parsed = accountUsageOptionsQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '账户候选参数不合法')))
    return
  }
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const options = await listAccountUsageOptionsAsync(access, {
      keyword: parsed.data.keyword,
      limit: parsed.data.limit,
      selectedIds: parseAccountIds(req.query.selectedIds ?? req.query['selectedIds[]']).slice(0, 20)
    })
    res.json(ok(options))
  } catch (error) {
    next(error)
  }
})

statsRouter.get('/account-usage/summary', async (req, res, next) => {
  try {
    const timezone = await usageStatsTimezoneAsync()
    const access = getRequestAccessScope(req.query.systemAccountId)
    const range = normalizeAccountUsageStatsRange({
      startDate: optionalQueryText(req.query.startDate),
      endDate: optionalQueryText(req.query.endDate)
    }, timezone)
    res.json(ok(await getAccountUsageStatsSummaryAsync(access, range)))
  } catch (error) {
    next(error)
  }
})

statsRouter.get('/account-usage/trend', async (req, res, next) => {
  try {
    const timezone = await usageStatsTimezoneAsync()
    const access = getRequestAccessScope(req.query.systemAccountId)
    const range = normalizeAccountUsageStatsRange({
      startDate: optionalQueryText(req.query.startDate),
      endDate: optionalQueryText(req.query.endDate)
    }, timezone)
    const accountIds = parseAccountIds(req.query.accountIds).slice(0, 10)
    const trend = await getAccountUsageStatsTrendAsync(access, range, accountIds)
    res.json(ok(trend))
  } catch (error) {
    next(error)
  }
})

function parseAccountUsageOptions(query: Record<string, unknown>, timezone: string): Omit<AccountListOptions, 'type'> & { range: ReturnType<typeof normalizeAccountUsageStatsRange>; accountIds?: string[] } {
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
  const range = fixedUsageStatsDefaultRange(timezone)
  return { startDate: range.startDate, endDate: range.endDate }
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

function hasAiPerformanceAccountIdsQuery(query: Record<string, unknown>): boolean {
  return Object.keys(query).some((key) => key === 'accountIds' || key.startsWith('accountIds['))
}

function parseAiPerformanceSeriesAccountIds(query: Record<string, unknown>):
  | { success: true; data: string[] }
  | { success: false; message: string } {
  const unsupportedKey = Object.keys(query).find((key) => key.startsWith('accountIds[') && key !== 'accountIds[]')
  if (unsupportedKey) {
    return { success: false, message: 'accountIds 仅支持重复参数 accountIds=value' }
  }
  const rawValues = [query.accountIds, query['accountIds[]']]
    .flatMap((value) => Array.isArray(value) ? value : value === undefined ? [] : [value])
  if (rawValues.length < 1 || rawValues.length > 20) {
    return { success: false, message: 'accountIds 必须重复传入 1 到 20 个' }
  }
  const ids: string[] = []
  const seen = new Set<string>()
  for (const rawValue of rawValues) {
    if (typeof rawValue !== 'string' || rawValue.includes(',')) {
      return { success: false, message: 'accountIds 不接受 CSV，必须使用重复参数' }
    }
    const id = rawValue.trim()
    if (!id) {
      return { success: false, message: 'accountIds 不能为空' }
    }
    if (!seen.has(id)) {
      seen.add(id)
      ids.push(id)
    }
  }
  return { success: true, data: ids }
}

function backgroundJobsFromSnapshot(snapshot: BackgroundJobsSnapshot | undefined): BackgroundJobRuntimeRow[] | undefined {
  return snapshot?.jobs?.map((job) => ({ ...job, workerRole: snapshot.workerRole }))
}

function numberValue(value: unknown): number {
  const number = typeof value === 'string' ? Number(value.trim()) : value
  return typeof number === 'number' && Number.isFinite(number) ? number : 0
}

function optionalNumberValue(value: unknown): number | undefined {
  const number = typeof value === 'string' && value.trim() ? Number(value.trim()) : value
  return typeof number === 'number' && Number.isFinite(number) ? number : undefined
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
  queue: (DbServiceRuntimeQueueSnapshot & Record<string, unknown>) | undefined,
  options: { queueType?: string; runningCount?: number; nextRunAt?: string; lastError?: string } = {}
): BackgroundJobRuntimeRow | undefined {
  if (!queue) return undefined
  const queueLength = numberValue(queue.queueLength)
  const flushFailureCount = numberValue(queue.flushFailureCount)
  const completedCount = numberValue(queue.completedCount)
  const runningCount = optionalNumberValue(options.runningCount)
  return emptyBackgroundJobRow({
    name,
    workerRole,
    running: queueLength > 0 || numberValue(runningCount) > 0,
    lastSuccessAt: queue.flushLastSuccessAt,
    lastFinishedAt: queue.flushLastSuccessAt,
    lastError: options.lastError ?? (typeof queue.flushLastError === 'string' ? queue.flushLastError : undefined),
    runCount: completedCount + flushFailureCount,
    successCount: completedCount,
    failureCount: flushFailureCount,
    localQueue: {
      ...queue,
      name,
      queueType: options.queueType,
      runningCount,
      consumers: optionalNumberValue(queue.consumers),
      nextRunAt: options.nextRunAt
    }
  })
}

async function backgroundQueueRuntimeRows(runtime: DbServiceSystemMetricsRuntimeSnapshot | undefined): Promise<BackgroundJobRuntimeRow[] | undefined> {
  if (!runtime) return undefined
  return [
    ...queueHealthRuntimeRows(runtime),
    ...await redisStreamRuntimeQueueRows(),
    ...dbServiceRuntimeQueueRows(runtime),
    ...gatewayAccountSideEffectQueueRows(runtime),
    ...accountBalanceSnapshotCleanupRuntimeRows(runtime),
    ...highConcurrencyRuntimeQueueRows(runtime)
  ]
}

function accountBalanceSnapshotCleanupRuntimeRows(runtime: DbServiceSystemMetricsRuntimeSnapshot): BackgroundJobRuntimeRow[] {
  const state = runtime.accountBalanceSnapshotCleanup
  if (!state) return []
  return [
    localQueueBackgroundJobRow('AI 账户余额旧快照清理', 'server', {
      queueLength: state.pendingCount,
      completedCount: state.completedCount,
      failedCount: state.failedAttemptCount,
      flushLastSuccessAt: state.lastSuccessAt,
      flushLastError: state.lastError,
      suppressedAccountCount: state.suppressedAccountCount,
      exhaustedAccountCount: state.exhaustedAccountCount,
      exhaustedCount: state.exhaustedCount
    }, {
      queueType: 'local',
      runningCount: state.runningCount,
      nextRunAt: state.nextRunAt,
      lastError: state.lastError
    })
  ].filter((row): row is BackgroundJobRuntimeRow => Boolean(row))
}

async function redisStreamRuntimeQueueRows(): Promise<BackgroundJobRuntimeRow[]> {
  const runtimes = await Promise.all([
    redisStreamRuntime('Redis Stream 使用记录', getUsageRecordRedisStreamRuntime),
    redisStreamRuntime('Redis Stream 审计日志', getAuditLogRedisStreamRuntime),
    redisStreamRuntime('Redis Stream 操作日志', getOperationLogRedisStreamRuntime),
    redisStreamRuntime('Redis Stream 公开接口日志', getPublicApiLogRedisStreamRuntime),
    redisStreamRuntime('Redis Stream 数据维护', getRecordMaintenanceRedisStreamRuntime),
  ])
  return runtimes.filter((row): row is BackgroundJobRuntimeRow => Boolean(row))
}

async function redisStreamRuntime(
  name: string,
  loadRuntime: () => Promise<RedisStreamQueueRuntime | undefined>
): Promise<BackgroundJobRuntimeRow | undefined> {
  let runtime: RedisStreamQueueRuntime | undefined
  try {
    runtime = await loadRuntime()
  } catch (error) {
    return localQueueBackgroundJobRow(name, 'ingest-worker', {}, {
      queueType: 'redis',
      lastError: error instanceof Error ? error.message : String(error)
    })
  }
  if (!runtime) return undefined
  const pendingCount = numberValue(runtime.pendingCount)
  const lag = numberValue(runtime.lag)
  return localQueueBackgroundJobRow(name, 'ingest-worker', {
    queueLength: pendingCount + lag,
    pendingCount,
    redisLag: lag,
    consumers: numberValue(runtime.consumers),
    lastDeliveredId: runtime.lastDeliveredId,
    entriesRead: runtime.entriesRead,
    oldestPendingId: runtime.oldestPendingId,
    newestPendingId: runtime.newestPendingId
  }, { queueType: 'redis' })
}

function queueHealthRuntimeRows(runtime: DbServiceSystemMetricsRuntimeSnapshot): BackgroundJobRuntimeRow[] {
  const queueHealth = buildBackgroundQueueHealthSnapshot(runtime)
  return [...queueHealth.workerQueues, ...queueHealth.serverIpcQueues]
    .map(backgroundQueueHealthRuntimeRow)
    .filter((row): row is BackgroundJobRuntimeRow => Boolean(row))
}

export function backgroundQueueHealthRuntimeRow(item: BackgroundQueueHealthItem): BackgroundJobRuntimeRow | undefined {
  if (item.status === 'unavailable' && item.queueLength === null && item.queueBytes === null) return undefined
  return localQueueBackgroundJobRow(item.label, workerRoleFromQueueHealthItem(item), {
    queueLength: item.queueLength ?? undefined,
    queueBytes: item.queueBytes ?? undefined,
    droppedCount: item.droppedCount ?? undefined,
    droppedSuccessCount: item.droppedSuccessCount ?? undefined,
    droppedFailureCount: item.droppedFailureCount ?? undefined,
    droppedOverflowCount: item.droppedOverflowCount ?? undefined,
    droppedOversizeCount: item.droppedOversizeCount ?? undefined,
    rejectedCount: item.rejectedCount ?? undefined,
    flushFailureCount: item.flushFailureCount ?? undefined,
    flushLastError: item.flushLastError,
    oldestQueuedMs: item.oldestQueuedMs ?? undefined,
    lastFlushMs: item.lastFlushMs ?? undefined,
    maxFlushMs: item.maxFlushMs ?? undefined,
    slowFlushCount: item.slowFlushCount ?? undefined,
    lastSlowFlushAt: item.lastSlowFlushAt,
    writerPoolEnabled: item.writerPoolEnabled ?? undefined,
    writerPoolWorkerCount: item.writerPoolWorkerCount ?? undefined,
    writerPoolQueueLength: item.writerPoolQueueLength ?? undefined,
    writerPoolActiveJobs: item.writerPoolActiveJobs ?? undefined,
    writerPoolHandledJobs: item.writerPoolHandledJobs ?? undefined,
    writerPoolFailedJobs: item.writerPoolFailedJobs ?? undefined,
    writerPoolRejectedJobs: item.writerPoolRejectedJobs ?? undefined,
    writerPoolOldestQueuedMs: item.writerPoolOldestQueuedMs ?? undefined,
    writerPoolMaxQueueWaitMs: item.writerPoolMaxQueueWaitMs ?? undefined,
    writerPoolMaxRunMs: item.writerPoolMaxRunMs ?? undefined,
    pendingWriteRequestCount: item.pendingWriteRequestCount ?? undefined,
    pendingWriteOldestQueuedMs: item.oldestPendingWriteMs ?? undefined,
    discoveredFileCount: item.discoveredFileCount ?? undefined,
    pendingFileCount: item.pendingFileCount ?? undefined,
    pendingBytes: item.pendingBytes ?? undefined,
    oldestPendingMtime: item.oldestPendingMtime,
    currentFile: item.currentFile,
    currentOffset: item.currentOffset ?? undefined,
    lastReadAt: item.lastReadAt,
    lastCommitAt: item.lastCommitAt,
    lastError: item.lastError,
    protectedRotatedFileCount: item.protectedRotatedFileCount ?? undefined
  }, { queueType: item.source === 'server_ipc' ? 'ipc' : 'local' })
}

function workerRoleFromQueueHealthItem(item: BackgroundQueueHealthItem): string {
  if (item.source === 'server_ipc') return 'server'
  if (item.key.includes('Stats') || item.label.includes('stats')) return 'stats-worker'
  return 'ingest-worker'
}

function dbServiceRuntimeQueueRows(runtime: DbServiceSystemMetricsRuntimeSnapshot): BackgroundJobRuntimeRow[] {
  const dbService = runtime.dbService
  if (!dbService) return []
  const codexWriterPool = dbService.codexContextStateWriterPool
  return [
    localQueueBackgroundJobRow('DB service 请求队列', 'db-service', {
      queueLength: dbService.queuedRequestCount,
      queueBytes: dbService.queuedRequestBytes,
      oldestQueuedMs: dbService.oldestQueuedMs,
      rejectedCount: dbService.queueRejectedCount,
      expiredCount: dbService.queueExpiredCount,
      runningCount: dbService.activeConcurrentRequestCount,
      maxActiveConcurrentRequestCount: dbService.maxActiveConcurrentRequestCount,
      lastFlushMs: dbService.lastExecMs,
      maxFlushMs: dbService.maxExecMs,
      slowFlushCount: dbService.slowOpCount,
      lastSlowFlushAt: dbService.lastSlowOpAt,
      lastSlowOpType: dbService.lastSlowOpType,
      queueHighCount: dbService.queuedHighRequestCount,
      queueNormalCount: dbService.queuedNormalRequestCount,
      queueLowCount: dbService.queuedLowRequestCount
    }, {
      queueType: 'request',
      runningCount: dbService.activeConcurrentRequestCount
    }),
    localQueueBackgroundJobRow('DB service dataset-writer pending', 'db-service', {
      queueLength: dbService.pendingDatasetWriteRequestCount,
      oldestQueuedMs: dbService.oldestDatasetWriteRequestMs,
      rejectedCount: dbService.rejectedDatasetWriteRequestCount,
      timedOutCount: dbService.timedOutDatasetWriteRequestCount
    }, { queueType: 'request' }),
    localQueueBackgroundJobRow('DB service 事件循环采样 pending', 'db-service', {
      queueLength: dbService.pendingProcessEventLoopRequestCount,
      timedOutCount: dbService.timedOutProcessEventLoopRequestCount,
      failedCount: dbService.failedProcessEventLoopRequestCount
    }, { queueType: 'request' }),
    localQueueBackgroundJobRow('DB service server runtime snapshot pending', 'db-service', {
      queueLength: dbService.pendingServerRuntimeRequestCount,
      timedOutCount: dbService.timedOutServerRuntimeRequestCount,
      failedCount: dbService.failedServerRuntimeRequestCount
    }, { queueType: 'request' }),
    localQueueBackgroundJobRow('DB service Codex 状态写入池', 'db-service', codexWriterPool
      ? {
        queueLength: codexWriterPool.batchItemCount,
        completedCount: codexWriterPool.handledJobs,
        rejectedCount: codexWriterPool.rejectedJobs,
        failedCount: codexWriterPool.failedBatches,
        writerPoolEnabled: codexWriterPool.enabled,
        writerPoolWorkerCount: codexWriterPool.workerCount,
        writerPoolQueueLength: codexWriterPool.queueLength,
        writerPoolActiveJobs: codexWriterPool.activeJobs,
        writerPoolHandledJobs: codexWriterPool.handledJobs,
        writerPoolFailedJobs: codexWriterPool.failedJobs,
        writerPoolRejectedJobs: codexWriterPool.rejectedJobs,
        writerPoolOldestQueuedMs: codexWriterPool.oldestQueuedMs,
        writerPoolMaxQueueWaitMs: codexWriterPool.maxQueueWaitMs,
        writerPoolMaxRunMs: codexWriterPool.maxRunMs,
        batchKeyCount: codexWriterPool.batchKeyCount,
        flushedBatches: codexWriterPool.flushedBatches,
        flushedBatchItems: codexWriterPool.flushedBatchItems
      }
      : undefined, { queueType: 'writer' })
  ].filter((row): row is BackgroundJobRuntimeRow => Boolean(row))
}

function gatewayAccountSideEffectQueueRows(runtime: DbServiceSystemMetricsRuntimeSnapshot): BackgroundJobRuntimeRow[] {
  const state = runtime.gatewayAccountSideEffects
  if (!state) return []
  return [
    localQueueBackgroundJobRow('网关账号副作用队列', 'server', {
      queueLength: numberValue(state.queueLength),
      completedCount: numberValue(state.completedCount),
      droppedCount: numberValue(state.droppedCount),
      expiredCount: numberValue(state.expiredCount),
      failedCount: numberValue(state.failedAttemptCount),
      coalescedCount: numberValue(state.coalescedCount),
      canceledBySuccessCount: numberValue(state.canceledBySuccessCount),
      skippedHealthySuccessCount: numberValue(state.skippedHealthySuccessCount),
      precheckPendingAccountCount: numberValue(state.precheckPendingAccountCount),
      degradedAccountCount: numberValue(state.degradedAccountCount),
      localSuppressedAccountCount: numberValue(state.localSuppressedAccountCount)
    }, {
      queueType: 'gateway',
      runningCount: state.processing === true ? 1 : 0,
      nextRunAt: typeof state.nextAttemptAt === 'string' ? state.nextAttemptAt : undefined
    })
  ].filter((row): row is BackgroundJobRuntimeRow => Boolean(row))
}

function highConcurrencyRuntimeQueueRows(runtime: DbServiceSystemMetricsRuntimeSnapshot): BackgroundJobRuntimeRow[] {
  return (runtime.highConcurrencyQueues ?? [])
    .map((queue) => localQueueBackgroundJobRow(`高并发短队列 ${queue.lane} ${queue.groupKey}`, 'server', {
      queueLength: queue.queueSize,
      perApiKeyQueueSize: queue.perApiKeyQueueSize
    }, { queueType: 'concurrency' }))
    .filter((row): row is BackgroundJobRuntimeRow => Boolean(row))
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
    partialCount: 0,
    skippedCount: 0,
    retryQueue: input.retryQueue,
    localQueue: input.localQueue
  }
}

function systemMetricsRuntimeJobRow(row: BackgroundJobRuntimeRow): BackgroundJobRuntimeRow {
  const localQueue = row.localQueue as (BackgroundLocalQueueSnapshot & Record<string, unknown>) | undefined
  return {
    name: row.name,
    workerRole: row.workerRole,
    intervalMs: row.intervalMs,
    resourceLane: row.resourceLane,
    running: row.running,
    pending: row.pending,
    queuedForLane: row.queuedForLane,
    timedOut: row.timedOut,
    nextRunAt: row.nextRunAt,
    lastStartedAt: row.lastStartedAt,
    lastFinishedAt: row.lastFinishedAt,
    lastSuccessAt: row.lastSuccessAt,
    lastErrorAt: row.lastErrorAt,
    lastError: row.lastError,
    lastWarningAt: row.lastWarningAt,
    lastWarning: row.lastWarning,
    lastOutcome: row.lastOutcome,
    leaseState: row.leaseState,
    lastDurationMs: row.lastDurationMs,
    maxDurationMs: row.maxDurationMs,
    runCount: row.runCount,
    successCount: row.successCount,
    failureCount: row.failureCount,
    partialCount: row.partialCount,
    skippedCount: row.skippedCount,
    taskSkippedCount: row.taskSkippedCount,
    coalescedCount: row.coalescedCount,
    timedOutCount: row.timedOutCount,
    retryQueue: row.retryQueue
      ? {
        name: row.retryQueue.name,
        pendingCount: row.retryQueue.pendingCount,
        runningCount: row.retryQueue.runningCount,
        nextRunAt: row.retryQueue.nextRunAt
      }
      : undefined,
    localQueue: localQueue
      ? {
        name: row.localQueue?.name ?? row.name,
        queueType: typeof localQueue.queueType === 'string' ? localQueue.queueType : undefined,
        queueLength: optionalNumberValue(localQueue.queueLength),
        queueBytes: optionalNumberValue(localQueue.queueBytes),
        completedCount: optionalNumberValue(localQueue.completedCount),
        droppedCount: optionalNumberValue(localQueue.droppedCount),
        rejectedCount: optionalNumberValue(localQueue.rejectedCount),
        expiredCount: optionalNumberValue(localQueue.expiredCount),
        timedOutCount: optionalNumberValue(localQueue.timedOutCount),
        failedCount: optionalNumberValue(localQueue.failedCount),
        flushFailureCount: optionalNumberValue(localQueue.flushFailureCount),
        oldestQueuedMs: optionalNumberValue(localQueue.oldestQueuedMs),
        writerPoolQueueLength: optionalNumberValue(localQueue.writerPoolQueueLength),
        writerPoolActiveJobs: optionalNumberValue(localQueue.writerPoolActiveJobs),
        writerPoolFailedJobs: optionalNumberValue(localQueue.writerPoolFailedJobs),
        writerPoolRejectedJobs: optionalNumberValue(localQueue.writerPoolRejectedJobs),
        writerPoolOldestQueuedMs: optionalNumberValue(localQueue.writerPoolOldestQueuedMs),
        pendingWriteRequestCount: optionalNumberValue(localQueue.pendingWriteRequestCount),
        pendingWriteOldestQueuedMs: optionalNumberValue(localQueue.pendingWriteOldestQueuedMs),
        runningCount: optionalNumberValue(localQueue.runningCount),
        consumers: optionalNumberValue(localQueue.consumers),
        nextRunAt: typeof localQueue.nextRunAt === 'string' ? localQueue.nextRunAt : undefined,
        flushLastSuccessAt: row.localQueue?.flushLastSuccessAt,
        flushLastError: row.localQueue?.flushLastError
      }
      : undefined
  }
}

statsRouter.get('/system-metrics', requireAdmin, async (req, res, next) => {
  const parsed = usageOverviewQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '监控日期范围不合法')))
    return
  }
  try {
    const overview = await getSystemMetricsOverviewAsync(await normalizeSystemMetricsDateRangeAsync(parsed.data))
    res.json(ok(overview))
  } catch (error) {
    next(error)
  }
})

statsRouter.get('/system-metrics/trend', requireAdmin, async (req, res, next) => {
  const parsed = usageOverviewQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '监控日期范围不合法')))
    return
  }
  try {
    res.json(ok(await getSystemMetricsTrendAsync(await normalizeSystemMetricsDateRangeAsync(parsed.data))))
  } catch (error) {
    next(error)
  }
})

statsRouter.get('/system-metrics/runtime', requireAdmin, async (_req, res, next) => {
  try {
    const liveRuntime = await requestServerSystemMetricsRuntimeSnapshot(2500).catch(() => undefined)
    const runtime = liveRuntime
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
      )
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
        if (job.name === 'normal-route-speed-first-recovery-probe' && opsWorkerSnapshot.normalRouteSpeedFirstRecoveryProbeQueue) {
          return { ...roleAwareJob, retryQueue: opsWorkerSnapshot.normalRouteSpeedFirstRecoveryProbeQueue }
        }
        return roleAwareJob
      }),
      await backgroundQueueRuntimeRows(runtime),
      backgroundQueueRows.length > 0 ? backgroundQueueRows : undefined
    ]
    const backgroundJobs = backgroundJobGroups.some(Array.isArray)
      ? backgroundJobGroups.flatMap((items) => items ?? [])
      : undefined
    const runtimeSnapshotAgeMs = runtime?.observedAt
      ? Math.max(0, Date.now() - Date.parse(runtime.observedAt))
      : undefined
    res.json(ok({
      runtimeSnapshotAvailable: Boolean(runtime),
      runtimeSnapshotStale: runtimeSnapshotAgeMs === undefined ? undefined : runtimeSnapshotAgeMs > 10_000,
      ingestWorkerSnapshotAvailable: Boolean(ingestWorkerSnapshot),
      statsWorkerSnapshotAvailable: Boolean(statsWorkerSnapshot),
      opsWorkerSnapshotAvailable: Boolean(opsWorkerSnapshot),
      backgroundJobsAvailable: Array.isArray(backgroundJobs),
      backgroundJobs: backgroundJobs?.map(systemMetricsRuntimeJobRow) ?? null
    }))
  } catch (error) {
    next(error)
  }
})

async function normalizeStatsDateRangeAsync(input: { startDate?: string; endDate?: string }) {
  const timezone = await usageStatsTimezoneAsync()
  const now = Date.now()
  const defaultRange = {
    startDate: dateKey(new Date(now - 2 * DAY_MS), timezone),
    endDate: dateKey(new Date(now), timezone)
  }
  const startDate = input.startDate ?? input.endDate ?? defaultRange.startDate
  const endDate = input.endDate ?? input.startDate ?? defaultRange.endDate
  return normalizeAccountUsageStatsRange({ startDate, endDate }, timezone)
}

async function normalizeSystemMetricsDateRangeAsync(input: { startDate?: string; endDate?: string }) {
  const timezone = await usageStatsTimezoneAsync()
  const today = dateKey(new Date(), timezone)
  const startDate = input.startDate ?? input.endDate ?? today
  const endDate = input.endDate ?? input.startDate ?? today
  return normalizeAccountUsageStatsRange({ startDate, endDate }, timezone)
}

async function normalizeUsageOverviewDateRangeAsync(input: { startDate?: string; endDate?: string }) {
  const timezone = await usageStatsTimezoneAsync()
  const today = dateKey(new Date(), timezone)
  if (!input.startDate && !input.endDate) return fixedUsageStatsDefaultRange(timezone, today)
  const startDate = input.startDate ?? input.endDate ?? today
  const endDate = input.endDate ?? input.startDate ?? today
  return normalizeAccountUsageStatsRange({ startDate, endDate }, timezone)
}
