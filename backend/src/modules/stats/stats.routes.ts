import { Router } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText } from '../../shared/query-values.js'
import {
  getAccountUsageStatsOverviewPageAsync,
  type AccountListOptions,
  type AccountListSchedulableFilter
} from '../../storage/repositories.js'
import { getAccountUsageStatsTrendAsync } from '../../storage/account-usage.repository.js'
import {
  getAiPerformanceOverviewAsync,
  getSystemMetricsOverviewAsync,
  getUsageStatsOverviewAsync,
  listAiPerformanceAccountOptionsAsync,
} from '../../storage/usage-stats.repository.js'
import { dateKey, normalizeAccountUsageStatsRange, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'
import { fixedUsageStatsDefaultRange } from '../../storage/usage-stats-window-helpers.js'
import type { RedisStreamQueueRuntime } from '../../shared/redis-stream-queue.js'
import { getAuditLogRedisStreamRuntime } from '../audit-logs/audit-log-queue.service.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { buildBackgroundQueueHealthSnapshot, type BackgroundQueueHealthItem } from '../background/background-queue-health.service.js'
import { requestServerRuntimeSnapshot } from '../db-service/db-service-ipc.js'
import type { DbServiceRuntimeQueueSnapshot, DbServiceServerRuntimeSnapshot } from '../db-service/db-service-types.js'
import { getUsageRecordRedisStreamRuntime } from '../gateway/usage/record-queue.service.js'
import { getOperationLogRedisStreamRuntime } from '../operation-logs/operation-log-queue.service.js'
import { getPublicApiLogRedisStreamRuntime } from '../public-api-logs/public-api-log-queue.service.js'
import { getRecordMaintenanceRedisStreamRuntime } from '../record-maintenance/record-maintenance-queue.service.js'

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
  lastWarningAt?: string
  lastWarning?: string
  lastDurationMs?: number
  maxDurationMs?: number
  runCount: number
  successCount: number
  failureCount: number
  partialCount: number
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
  const parsed = usageOverviewQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '性能监控日期范围不合法')))
    return
  }
  try {
    const access = getRequestAccessScope(req.query.systemAccountId)
    const range = await normalizeStatsDateRangeAsync(parsed.data)
    const accountIds = parseAccountIds(req.query.accountIds)
    const overview = await getAiPerformanceOverviewAsync(access, range, accountIds)
    res.json(ok(overview))
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

statsRouter.get('/account-usage', async (req, res, next) => {
  try {
    const timezone = await usageStatsTimezoneAsync()
    const access = getRequestAccessScope(req.query.systemAccountId)
    const query = parseAccountUsageOptions(req.query, timezone)
    const overview = await getAccountUsageStatsOverviewPageAsync(access, query)
    res.json(ok(overview))
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

async function backgroundQueueRuntimeRows(runtime: DbServiceServerRuntimeSnapshot | undefined): Promise<BackgroundJobRuntimeRow[] | undefined> {
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

function accountBalanceSnapshotCleanupRuntimeRows(runtime: DbServiceServerRuntimeSnapshot): BackgroundJobRuntimeRow[] {
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

function queueHealthRuntimeRows(runtime: DbServiceServerRuntimeSnapshot): BackgroundJobRuntimeRow[] {
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

function dbServiceRuntimeQueueRows(runtime: DbServiceServerRuntimeSnapshot): BackgroundJobRuntimeRow[] {
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

function gatewayAccountSideEffectQueueRows(runtime: DbServiceServerRuntimeSnapshot): BackgroundJobRuntimeRow[] {
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

function highConcurrencyRuntimeQueueRows(runtime: DbServiceServerRuntimeSnapshot): BackgroundJobRuntimeRow[] {
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

statsRouter.get('/system-metrics/runtime', requireAdmin, async (_req, res, next) => {
  try {
    const liveRuntime = await requestServerRuntimeSnapshot(2500).catch(() => undefined)
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
    const runtimeSnapshotObservedAt = runtime?.observedAt
    const runtimeSnapshotAgeMs = runtimeSnapshotObservedAt
      ? Math.max(0, Date.now() - Date.parse(runtimeSnapshotObservedAt))
      : undefined
    res.json(ok({
      runtimeSnapshotAvailable: Boolean(runtime),
      runtimeSnapshotSource: runtime ? 'live' as const : undefined,
      runtimeSnapshotObservedAt,
      runtimeSnapshotAgeMs,
      runtimeSnapshotStale: runtimeSnapshotAgeMs === undefined ? undefined : runtimeSnapshotAgeMs > 10_000,
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
  } catch (error) {
    next(error)
  }
})

async function normalizeStatsDateRangeAsync(input: { startDate?: string; endDate?: string }) {
  const timezone = await usageStatsTimezoneAsync()
  const defaultRange = defaultAccountUsageDateRange(timezone)
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
  const startDate = input.startDate ?? input.endDate ?? today
  const endDate = input.endDate ?? input.startDate ?? today
  return normalizeAccountUsageStatsRange({ startDate, endDate }, timezone)
}
