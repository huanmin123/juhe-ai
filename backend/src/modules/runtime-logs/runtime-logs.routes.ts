import { Router } from 'express'

import { ok } from '../../shared/http.js'
import type { RuntimeLogLevel, RuntimeLogListOptions } from '../../storage/runtime-logs.repository.js'
import { requestDbService, requestServerRuntimeSnapshot } from '../db-service/db-service-ipc.js'
import { getRuntimeLogGrepRuntime, grepRuntimeLogFiles } from './runtime-log-grep.service.js'

export const runtimeLogsRouter = Router()

runtimeLogsRouter.use((req, res, next) => {
  req.setTimeout(0)
  res.setTimeout(0)
  next()
})

runtimeLogsRouter.get('/', async (req, res, next) => {
  try {
    const startedAt = performance.now()
    const [result, serverRuntime] = await Promise.all([
      requestDbService({
        type: 'list_runtime_logs',
        options: parseRuntimeLogListOptions(req.query)
      }),
      requestServerRuntimeSnapshot()
    ])
    res.json(ok({
      ...result,
      elapsedMs: Math.round(performance.now() - startedAt),
      retentionDays: serverRuntime?.worker?.snapshot?.runtimeLogIndexQueue.retentionDays ?? 3
    }))
  } catch (error) {
    next(error)
  }
})

runtimeLogsRouter.get('/facets', async (_req, res, next) => {
  try {
    const [serverRuntime, dbServiceSnapshot, facets] = await Promise.all([
      requestServerRuntimeSnapshot(),
      requestDbService({ type: 'status' }, { timeoutMs: 1000 }).catch(() => undefined),
      requestDbService({ type: 'get_runtime_log_facets' })
    ])
    const workerSnapshot = serverRuntime?.worker?.snapshot
    const dbServiceState = serverRuntime?.dbService
    const gatewayAccountSideEffects = serverRuntime?.gatewayAccountSideEffects ?? {}
    res.json(ok({
      ...facets,
      runtime: workerSnapshot?.runtimeLogIndexQueue ?? {
        queueLength: 0,
        droppedCount: 0,
        retentionDays: 3
      },
      worker: {
        pid: workerSnapshot?.pid ?? serverRuntime?.worker?.pid,
        ready: workerSnapshot?.ready ?? serverRuntime?.worker?.ready ?? false,
        pendingMessageCount: serverRuntime?.worker?.pendingMessageCount ?? 0
      },
      dbService: {
        pid: dbServiceSnapshot?.pid ?? dbServiceState?.pid,
        ready: dbServiceSnapshot?.ready ?? dbServiceState?.ready ?? false,
        pendingRequestCount: dbServiceState?.pendingRequestCount ?? dbServiceSnapshot?.pendingRequestCount ?? 0,
        timedOutRequestCount: dbServiceState?.timedOutRequestCount ?? 0,
        failedRequestCount: dbServiceState?.failedRequestCount ?? dbServiceSnapshot?.failedRequestCount ?? 0,
        unavailableCircuitOpenUntil: dbServiceState?.unavailableCircuitOpenUntil,
        httpHost: dbServiceSnapshot?.httpHost ?? dbServiceState?.httpHost,
        httpPort: dbServiceSnapshot?.httpPort ?? dbServiceState?.httpPort,
        handledRequestCount: dbServiceSnapshot?.handledRequestCount,
        lastRequestAt: dbServiceSnapshot?.lastRequestAt,
        lastError: dbServiceSnapshot?.lastError
      },
      grep: getRuntimeLogGrepRuntime(),
      gatewayAccountSideEffects: {
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
  const timeRange = dateTimeRangeQueryValue(query.startAt, query.endAt)
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
    startAt: timeRange.startAt,
    endAt: timeRange.endAt
  }
}

function parseRuntimeLogGrepOptions(query: Record<string, unknown>): { keywords: string[]; limit?: number; startAt?: string; endAt?: string } {
  return {
    keywords: stringArrayQueryValues(query.keyword).concat(stringArrayQueryValues(query.keywords)),
    limit: numberQueryValue(query.limit),
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

function numberQueryValue(value: unknown): number | undefined {
  const text = Array.isArray(value) ? value[0] : value
  const number = typeof text === 'string' ? Number(text) : undefined
  return typeof number === 'number' && Number.isFinite(number) ? number : undefined
}

function optionalQueryText(value: unknown): string | undefined {
  const text = Array.isArray(value) ? value[0] : value
  return typeof text === 'string' && text.trim() ? text.trim() : undefined
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
