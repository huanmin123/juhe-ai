import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { finiteNumberQueryValue, optionalQueryText } from '../../shared/query-values.js'
import type { RuntimeLogLevel, RuntimeLogListOptions } from '../../storage/runtime-logs.repository.js'
import { buildBackgroundQueueHealthSnapshot } from '../background/background-queue-health.service.js'
import { requestDbService, requestServerRuntimeSnapshot } from '../db-service/db-service-ipc.js'
import { getRuntimeLogGrepRuntime, grepRuntimeLogFiles } from './runtime-log-grep.service.js'

export const runtimeLogsRouter = Router()
const runtimeLogRouteTimeoutMs = 120_000

runtimeLogsRouter.use((req, res, next) => {
  req.setTimeout(runtimeLogRouteTimeoutMs)
  res.setTimeout(runtimeLogRouteTimeoutMs)
  next()
})

runtimeLogsRouter.get('/', async (req, res, next) => {
  try {
    const startedAt = performance.now()
    const result = await requestDbService({
      type: 'list_runtime_logs',
      options: parseRuntimeLogListOptions(req.query)
    })
    res.json(ok({
      ...result,
      elapsedMs: Math.round(performance.now() - startedAt),
      retentionDays: null,
      retentionDaysSource: 'unavailable',
      runtimeAvailable: false,
      workerSnapshotAvailable: false,
      runtimeLogIndexQueueAvailable: false
    }))
  } catch (error) {
    next(error)
  }
})

runtimeLogsRouter.get('/facets', async (_req, res, next) => {
  try {
    const [serverRuntime, dbServiceSnapshot, facets, grepRuntime] = await Promise.all([
      requestServerRuntimeSnapshot(),
      requestDbService({ type: 'status' }, { timeoutMs: 1000 }).catch(() => undefined),
      requestDbService({ type: 'get_runtime_log_facets' }),
      getRuntimeLogGrepRuntime()
    ])
    const workerSnapshot = serverRuntime?.ingestWorker?.snapshot
    const workerRuntime = serverRuntime?.ingestWorker
    const runtimeLogIndexQueue = workerSnapshot?.runtimeLogIndexQueue
    const dbServiceState = serverRuntime?.dbService
    const gatewayAccountSideEffects = serverRuntime?.gatewayAccountSideEffects
    const queueHealth = buildBackgroundQueueHealthSnapshot(serverRuntime)
    res.json(ok({
      ...facets,
      runtimeAvailable: Boolean(serverRuntime),
      workerSnapshotAvailable: Boolean(workerSnapshot),
      runtimeLogIndexQueueAvailable: Boolean(runtimeLogIndexQueue),
      runtime: runtimeLogIndexQueue ?? null,
      worker: {
        available: Boolean(workerSnapshot ?? workerRuntime),
        snapshotAvailable: Boolean(workerSnapshot),
        pid: workerSnapshot?.pid ?? workerRuntime?.pid,
        ready: workerSnapshot?.ready ?? workerRuntime?.ready ?? null,
        pendingMessageCount: workerRuntime?.pendingMessageCount ?? null
      },
      dbService: {
        statusAvailable: Boolean(dbServiceSnapshot),
        stateAvailable: Boolean(dbServiceState),
        pid: dbServiceSnapshot?.pid ?? dbServiceState?.pid,
        ready: dbServiceSnapshot?.ready ?? dbServiceState?.ready ?? null,
        pendingRequestCount: dbServiceState?.pendingRequestCount ?? dbServiceSnapshot?.pendingRequestCount ?? null,
        pendingDatasetWriteRequestCount: dbServiceState?.pendingDatasetWriteRequestCount,
        oldestDatasetWriteRequestMs: dbServiceState?.oldestDatasetWriteRequestMs,
        timedOutDatasetWriteRequestCount: dbServiceState?.timedOutDatasetWriteRequestCount,
        rejectedDatasetWriteRequestCount: dbServiceState?.rejectedDatasetWriteRequestCount,
        timedOutRequestCount: dbServiceState?.timedOutRequestCount ?? null,
        failedRequestCount: dbServiceState?.failedRequestCount ?? dbServiceSnapshot?.failedRequestCount ?? null,
        queuedRequestCount: dbServiceSnapshot?.queuedRequestCount ?? dbServiceState?.queuedRequestCount,
        queuedRequestBytes: dbServiceSnapshot?.queuedRequestBytes ?? dbServiceState?.queuedRequestBytes,
        queuedHighRequestCount: dbServiceSnapshot?.queuedHighRequestCount ?? dbServiceState?.queuedHighRequestCount,
        queuedNormalRequestCount: dbServiceSnapshot?.queuedNormalRequestCount ?? dbServiceState?.queuedNormalRequestCount,
        queuedLowRequestCount: dbServiceSnapshot?.queuedLowRequestCount ?? dbServiceState?.queuedLowRequestCount,
        oldestQueuedMs: dbServiceSnapshot?.oldestQueuedMs ?? dbServiceState?.oldestQueuedMs,
        lastQueueWaitMs: dbServiceSnapshot?.lastQueueWaitMs ?? dbServiceState?.lastQueueWaitMs,
        maxQueueWaitMs: dbServiceSnapshot?.maxQueueWaitMs ?? dbServiceState?.maxQueueWaitMs,
        queueRejectedCount: dbServiceSnapshot?.queueRejectedCount ?? dbServiceState?.queueRejectedCount,
        queueExpiredCount: dbServiceSnapshot?.queueExpiredCount ?? dbServiceState?.queueExpiredCount,
        activeConcurrentRequestCount: dbServiceSnapshot?.activeConcurrentRequestCount ?? dbServiceState?.activeConcurrentRequestCount,
        maxActiveConcurrentRequestCount: dbServiceSnapshot?.maxActiveConcurrentRequestCount ?? dbServiceState?.maxActiveConcurrentRequestCount,
        lastExecMs: dbServiceSnapshot?.lastExecMs ?? dbServiceState?.lastExecMs,
        maxExecMs: dbServiceSnapshot?.maxExecMs ?? dbServiceState?.maxExecMs,
        slowOpCount: dbServiceSnapshot?.slowOpCount ?? dbServiceState?.slowOpCount,
        lastSlowOpType: dbServiceSnapshot?.lastSlowOpType ?? dbServiceState?.lastSlowOpType,
        lastSlowOpMs: dbServiceSnapshot?.lastSlowOpMs ?? dbServiceState?.lastSlowOpMs,
        lastSlowOpAt: dbServiceSnapshot?.lastSlowOpAt ?? dbServiceState?.lastSlowOpAt,
        unavailableCircuitOpenUntil: dbServiceState?.unavailableCircuitOpenUntil,
        httpHost: dbServiceSnapshot?.httpHost ?? dbServiceState?.httpHost,
        httpPort: dbServiceSnapshot?.httpPort ?? dbServiceState?.httpPort,
        handledRequestCount: dbServiceSnapshot?.handledRequestCount,
        lastRequestAt: dbServiceSnapshot?.lastRequestAt,
        lastError: dbServiceSnapshot?.lastError
      },
      queueHealth,
      grep: grepRuntime,
      gatewayAccountSideEffectsAvailable: Boolean(gatewayAccountSideEffects),
      gatewayAccountSideEffects: gatewayAccountSideEffects
        ? {
            queueLength: numberField(gatewayAccountSideEffects, 'queueLength'),
            processing: booleanField(gatewayAccountSideEffects, 'processing'),
            enqueuedCount: numberField(gatewayAccountSideEffects, 'enqueuedCount'),
            completedCount: numberField(gatewayAccountSideEffects, 'completedCount'),
            failedAttemptCount: numberField(gatewayAccountSideEffects, 'failedAttemptCount'),
            droppedCount: numberField(gatewayAccountSideEffects, 'droppedCount'),
            expiredCount: numberField(gatewayAccountSideEffects, 'expiredCount'),
            localSuppressedAccountCount: numberField(gatewayAccountSideEffects, 'localSuppressedAccountCount'),
            nextAttemptAt: stringField(gatewayAccountSideEffects, 'nextAttemptAt')
          }
        : null
    }))
  } catch (error) {
    next(error)
  }
})

runtimeLogsRouter.get('/grep', async (req, res, next) => {
  try {
    const result = await grepRuntimeLogFiles(parseRuntimeLogGrepOptions(req.query))
    res.json(ok(result))
  } catch (error) {
    next(error)
  }
})

runtimeLogsRouter.get('/:id', async (req, res, next) => {
  try {
    const detail = await requestDbService({
      type: 'get_runtime_log_detail',
      id: req.params.id
    })
    if (!detail) {
      res.status(404).json({ message: '运行日志不存在' })
      return
    }
    res.json(ok(detail))
  } catch (error) {
    next(error)
  }
})

const runtimeLogLevels = new Set<RuntimeLogLevel | 'all'>([
  'all',
  'trace',
  'debug',
  'info',
  'warn',
  'error',
  'fatal'
])

function parseRuntimeLogListOptions(query: Record<string, unknown>): RuntimeLogListOptions {
  const rawPage = finiteNumberQueryValue(query.page)
  const rawPageSize = finiteNumberQueryValue(query.pageSize)
  const rawLevel = optionalQueryText(query.level)?.toLowerCase()
  const timeRange = dateTimeRangeQueryValue(query.startAt, query.endAt)
  return {
    page: Number.isInteger(rawPage) ? rawPage : undefined,
    pageSize: Number.isInteger(rawPageSize) ? rawPageSize : undefined,
    traceId: optionalQueryText(query.traceId),
    level: rawLevel && runtimeLogLevels.has(rawLevel as RuntimeLogLevel | 'all')
      ? rawLevel as RuntimeLogLevel | 'all'
      : undefined,
    event: optionalQueryText(query.event),
    keyword: optionalQueryText(query.keyword),
    startAt: timeRange.startAt,
    endAt: timeRange.endAt
  }
}

function parseRuntimeLogGrepOptions(query: Record<string, unknown>): { keywords: string[]; limit?: number; startAt?: string; endAt?: string } {
  return {
    keywords: stringArrayQueryValues(query.keywords),
    limit: finiteNumberQueryValue(query.limit),
    startAt: optionalQueryText(query.startAt),
    endAt: optionalQueryText(query.endAt)
  }
}

function stringArrayQueryValues(value: unknown): string[] {
  if (Array.isArray(value)) {
    return value.filter((item): item is string => typeof item === 'string')
  }
  return typeof value === 'string' ? [value] : []
}

function dateTimeRangeQueryValue(startValue: unknown, endValue: unknown): { startAt?: string; endAt?: string } {
  const startAt = dateTimeQueryValue(startValue)
  const endAt = dateTimeQueryValue(endValue)
  if (startAt && endAt && startAt > endAt) {
    return { startAt: endAt, endAt: startAt }
  }
  return { startAt, endAt }
}

function dateTimeQueryValue(value: unknown): string | undefined {
  const text = optionalQueryText(value)
  if (!text) return undefined
  const time = Date.parse(text)
  return Number.isNaN(time) ? undefined : new Date(time).toISOString()
}

function numberField(record: Record<string, unknown>, key: string): number {
  const value = record[key]
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function booleanField(record: Record<string, unknown>, key: string): boolean {
  return record[key] === true
}

function stringField(record: Record<string, unknown>, key: string): string | undefined {
  const value = record[key]
  return typeof value === 'string' ? value : undefined
}
