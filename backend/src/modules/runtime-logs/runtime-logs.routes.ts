import { Router } from 'express'

import { ok } from '../../shared/http.js'
import {
  getRuntimeLogFacets,
  listRuntimeLogs,
  type RuntimeLogLevel,
  type RuntimeLogListOptions
} from '../../storage/runtime-logs.repository.js'
import { getBackgroundWorkerState, requestBackgroundWorkerSnapshot } from '../background/background-ipc.js'
import { getDbServiceState, requestDbService } from '../db-service/db-service-ipc.js'
import { getGatewayAccountSideEffectState } from '../gateway/gateway-account-side-effects.service.js'
import { grepRuntimeLogFiles } from './runtime-log-grep.service.js'

export const runtimeLogsRouter = Router()

runtimeLogsRouter.use((req, res, next) => {
  req.setTimeout(0)
  res.setTimeout(0)
  next()
})

runtimeLogsRouter.get('/', async (req, res) => {
  const startedAt = performance.now()
  const result = listRuntimeLogs(parseRuntimeLogListOptions(req.query))
  const workerSnapshot = await requestBackgroundWorkerSnapshot()
  res.json(ok({
    ...result,
    elapsedMs: Math.round(performance.now() - startedAt),
    retentionDays: workerSnapshot?.runtimeLogIndexQueue.retentionDays ?? 3
  }))
})

runtimeLogsRouter.get('/facets', async (_req, res) => {
  const [workerSnapshot, dbServiceSnapshot] = await Promise.all([
    requestBackgroundWorkerSnapshot(),
    requestDbService({ type: 'status' }, { timeoutMs: 1000, fallbackToLocal: false }).catch(() => undefined)
  ])
  const dbServiceState = getDbServiceState()
  const workerState = getBackgroundWorkerState()
  const gatewayAccountSideEffects = getGatewayAccountSideEffectState()
  res.json(ok({
    ...getRuntimeLogFacets(),
    runtime: workerSnapshot?.runtimeLogIndexQueue ?? {
      queueLength: 0,
      droppedCount: 0,
      retentionDays: 3
    },
    worker: {
      pid: workerSnapshot?.pid ?? workerState.pid,
      ready: workerSnapshot?.ready ?? workerState.ready,
      pendingMessageCount: workerState.pendingMessageCount
    },
    dbService: {
      pid: dbServiceSnapshot?.pid ?? dbServiceState.pid,
      ready: dbServiceSnapshot?.ready ?? dbServiceState.ready,
      pendingRequestCount: dbServiceState.pendingRequestCount,
      timedOutRequestCount: dbServiceState.timedOutRequestCount,
      failedRequestCount: dbServiceState.failedRequestCount,
      fallbackCircuitOpenUntil: dbServiceState.fallbackCircuitOpenUntil,
      localFallbackActiveCount: dbServiceState.localFallbackActiveCount,
      localFallbackQueuedCount: dbServiceState.localFallbackQueuedCount,
      localFallbackRequestCount: dbServiceState.localFallbackRequestCount,
      localFallbackBypassedGuardCount: dbServiceState.localFallbackBypassedGuardCount,
      handledRequestCount: dbServiceSnapshot?.handledRequestCount,
      lastRequestAt: dbServiceSnapshot?.lastRequestAt,
      lastError: dbServiceSnapshot?.lastError
    },
    gatewayAccountSideEffects: {
      queueLength: gatewayAccountSideEffects.queueLength,
      processing: gatewayAccountSideEffects.processing,
      enqueuedCount: gatewayAccountSideEffects.enqueuedCount,
      completedCount: gatewayAccountSideEffects.completedCount,
      failedAttemptCount: gatewayAccountSideEffects.failedAttemptCount,
      droppedCount: gatewayAccountSideEffects.droppedCount,
      expiredCount: gatewayAccountSideEffects.expiredCount,
      localSuppressedAccountCount: gatewayAccountSideEffects.localSuppressedAccountCount,
      nextAttemptAt: gatewayAccountSideEffects.nextAttemptAt
    }
  }))
})

runtimeLogsRouter.get('/grep', async (req, res, next) => {
  try {
    const result = await grepRuntimeLogFiles(parseRuntimeLogGrepOptions(req.query))
    res.json(ok(result))
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
  const rawPage = numberQueryValue(query.page)
  const rawPageSize = numberQueryValue(query.pageSize)
  const rawLimit = numberQueryValue(query.limit)
  const rawLevel = optionalQueryText(query.level)?.toLowerCase()
  return {
    page: Number.isInteger(rawPage) ? rawPage : undefined,
    pageSize: Number.isInteger(rawPageSize) ? rawPageSize : undefined,
    limit: Number.isInteger(rawLimit) ? rawLimit : undefined,
    traceId: optionalQueryText(query.traceId),
    level: rawLevel && runtimeLogLevels.has(rawLevel as RuntimeLogLevel | 'all')
      ? rawLevel as RuntimeLogLevel | 'all'
      : undefined,
    event: optionalQueryText(query.event),
    keyword: optionalQueryText(query.keyword),
    startedAt: optionalQueryText(query.startedAt),
    endedAt: optionalQueryText(query.endedAt)
  }
}

function parseRuntimeLogGrepOptions(query: Record<string, unknown>): { keywords: string[]; limit?: number } {
  return {
    keywords: stringArrayQueryValues(query.keyword).concat(stringArrayQueryValues(query.keywords)),
    limit: numberQueryValue(query.limit)
  }
}

function stringArrayQueryValues(value: unknown): string[] {
  if (Array.isArray(value)) {
    return value.filter((item): item is string => typeof item === 'string')
  }
  return typeof value === 'string' ? [value] : []
}

function numberQueryValue(value: unknown): number | undefined {
  const text = Array.isArray(value) ? value[0] : value
  const number = typeof text === 'string' ? Number(text) : undefined
  return typeof number === 'number' && Number.isFinite(number) ? number : undefined
}

function optionalQueryText(value: unknown): string | undefined {
  const text = Array.isArray(value) ? value[0] : value
  return typeof text === 'string' && text.trim() ? text.trim() : undefined
}
