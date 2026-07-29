import { Router } from 'express'

import { ok, sendNotFound } from '../../shared/http.js'
import { finiteNumberQueryValue, optionalQueryText } from '../../shared/query-values.js'
import {
  getAuditLogDetailSupplementAsync,
  getAuditLogPayload,
  listAuditErrorGroupEventsAsync,
  listAuditErrorGroupsAsync,
  listAuditLogsByIdsAsync,
  type AuditErrorGroupListOptions,
  listAuditLogsAsync,
  type AuditLogListOptions,
  type AuditOutcome,
  type AuditTrafficSource
} from '../../storage/repositories.js'
import { readAuditLogSettings } from './audit-log-settings.js'
import { grepAuditHotSearchFiles } from '../../storage/audit-log-hot-search-files.js'
import { requestServerRuntimeSnapshot } from '../db-service/db-service-ipc.js'
import { requireAdmin } from '../auth/auth.middleware.js'

export const auditLogsRouter = Router()
const auditLogRouteTimeoutMs = 120_000

auditLogsRouter.use((req, res, next) => {
  req.setTimeout(auditLogRouteTimeoutMs)
  res.setTimeout(auditLogRouteTimeoutMs)
  next()
})
auditLogsRouter.use(requireAdmin)

auditLogsRouter.get('/', async (req, res, next) => {
  try {
    res.json(ok(await listAuditLogsAsync(parseAuditLogListOptions(req.query))))
  } catch (error) {
    next(error)
  }
})

auditLogsRouter.get('/search-hot', async (req, res, next) => {
  try {
    const grepResult = await grepAuditHotSearchFiles(parseAuditHotSearchOptions(req.query))
    const items = await listAuditLogsByIdsAsync(grepResult.auditLogIds)
    res.json(ok({
      items,
      total: grepResult.truncated ? Math.max(items.length + 1, grepResult.auditLogIds.length) : items.length,
      hasMore: grepResult.truncated,
      page: 1,
      pageSize: grepResult.limit,
      available: grepResult.available,
      elapsedMs: grepResult.elapsedMs,
      keywords: grepResult.keywords,
      startAt: grepResult.startAt,
      endAt: grepResult.endAt,
      limit: grepResult.limit,
      truncated: grepResult.truncated,
      scannedFileCount: grepResult.scannedFileCount,
      message: grepResult.message
    }))
  } catch (error) {
    next(error)
  }
})

auditLogsRouter.get('/runtime', async (_req, res, next) => {
  try {
    const serverRuntime = await requestServerRuntimeSnapshot()
    const workerSnapshot = serverRuntime?.ingestWorker?.snapshot
    const auditLogQueue = workerSnapshot?.auditLogQueue
    const workerRuntime = serverRuntime?.ingestWorker
    const runtimeAvailable = Boolean(serverRuntime)
    const workerSnapshotAvailable = Boolean(workerSnapshot)
    const auditLogQueueAvailable = Boolean(auditLogQueue)
    const settings = readAuditLogSettings()
    res.json(ok({
      enabled: settings.enabled,
      runtimeAvailable,
      workerSnapshotAvailable,
      auditLogQueueAvailable,
      activeCaptureAvailable: serverRuntime?.activeAuditCaptureCount !== undefined,
      unavailableReason: auditLogRuntimeUnavailableReason(settings.enabled, runtimeAvailable, workerSnapshotAvailable, auditLogQueueAvailable),
      queueLength: auditLogQueue?.queueLength ?? null,
      queueBytes: auditLogQueue?.queueBytes ?? null,
      flushLastSuccessAt: auditLogQueue?.flushLastSuccessAt,
      flushLastError: auditLogQueue?.flushLastError,
      droppedSuccessCount: auditLogQueue?.droppedSuccessCount ?? null,
      droppedFailureCount: auditLogQueue?.droppedFailureCount ?? null,
      droppedOverflowCount: auditLogQueue?.droppedOverflowCount ?? null,
      droppedOversizeCount: auditLogQueue?.droppedOversizeCount ?? null,
      activeCaptureCount: serverRuntime?.activeAuditCaptureCount ?? null,
      transport: serverRuntime?.auditLogTransport
        ? { available: true, ...serverRuntime.auditLogTransport }
        : {
          available: false,
          queuedJobs: null,
          queuedBytes: null,
          activeJobs: null,
          activeBytes: null,
          workerCount: null,
          completedCount: null,
          failedCount: null,
          rejectedCount: null,
          pendingDispatchCount: null
        },
      worker: {
        available: Boolean(workerSnapshot ?? workerRuntime),
        snapshotAvailable: workerSnapshotAvailable,
        pid: workerSnapshot?.pid ?? workerRuntime?.pid,
        ready: workerSnapshot?.ready ?? workerRuntime?.ready ?? null,
        pendingMessageCount: workerRuntime?.pendingMessageCount ?? null
      },
      settings
    }))
  } catch (error) {
    next(error)
  }
})

auditLogsRouter.get('/error-groups', async (req, res, next) => {
  try {
    res.json(ok(await listAuditErrorGroupsAsync(parseAuditErrorGroupListOptions(req.query))))
  } catch (error) {
    next(error)
  }
})

auditLogsRouter.get('/error-groups/:id/events', async (req, res, next) => {
  try {
    res.json(ok(await listAuditErrorGroupEventsAsync(req.params.id, parseAuditLogListOptions(req.query))))
  } catch (error) {
    next(error)
  }
})

auditLogsRouter.get('/:id', async (req, res, next) => {
  try {
    const detail = await getAuditLogDetailSupplementAsync(req.params.id)
    if (!detail) {
      sendNotFound(res, '审计日志不存在')
      return
    }
    res.json(ok(detail))
  } catch (error) {
    next(error)
  }
})

auditLogsRouter.get('/:id/payloads/:payloadId', async (req, res, next) => {
  try {
    const payload = await getAuditLogPayload(req.params.id, req.params.payloadId, {
      full: true
    })
    if (!payload) {
      sendNotFound(res, '审计原文不存在')
      return
    }
    res.json(ok(payload))
  } catch (error) {
    next(error)
  }
})

const auditOutcomes = new Set<AuditOutcome | 'all'>([
  'all',
  'success',
  'success_after_retry',
  'gateway_succeeded',
  'gateway_failed',
  'upstream_failed',
  'stream_failed',
  'client_aborted'
])
const auditTrafficSources = new Set<AuditTrafficSource>(['gateway', 'manual_account_test', 'account_health_check', 'runtime_recovery_probe', 'cooldown_retest', 'hybrid_scoring', 'hybrid_quality_scoring'])

function parseAuditLogListOptions(query: Record<string, unknown>): AuditLogListOptions {
  const rawPage = finiteNumberQueryValue(query.page)
  const rawPageSize = finiteNumberQueryValue(query.pageSize)
  const rawStatusCode = finiteNumberQueryValue(query.statusCode)
  return {
    page: Number.isInteger(rawPage) ? rawPage : undefined,
    pageSize: Number.isInteger(rawPageSize) ? rawPageSize : undefined,
    traceId: optionalQueryText(query.traceId),
    sessionId: optionalQueryText(query.sessionId),
    sessionClientType: optionalQueryText(query.sessionClientType),
    errorGroupId: optionalQueryText(query.errorGroupId),
    outcome: typeof query.outcome === 'string' && auditOutcomes.has(query.outcome as AuditOutcome | 'all')
      ? query.outcome as AuditOutcome | 'all'
      : undefined,
    statusCode: isHttpStatusCode(rawStatusCode) ? rawStatusCode : undefined,
    path: optionalQueryText(query.path),
    model: optionalQueryText(query.model),
    systemAccountId: optionalQueryText(query.systemAccountId),
    apiKeyId: optionalQueryText(query.apiKeyId),
    groupId: optionalQueryText(query.groupId),
    accountId: optionalQueryText(query.accountId),
    clientIp: optionalQueryText(query.clientIp),
    startAt: optionalQueryText(query.startAt),
    endAt: optionalQueryText(query.endAt),
    trafficSource: auditTrafficSourceQueryValue(query.trafficSource)
  }
}

function parseAuditErrorGroupListOptions(query: Record<string, unknown>): AuditErrorGroupListOptions {
  const rawPage = finiteNumberQueryValue(query.page)
  const rawPageSize = finiteNumberQueryValue(query.pageSize)
  const rawStatusCode = finiteNumberQueryValue(query.statusCode)
  return {
    page: Number.isInteger(rawPage) ? rawPage : undefined,
    pageSize: Number.isInteger(rawPageSize) ? rawPageSize : undefined,
    path: optionalQueryText(query.path),
    model: optionalQueryText(query.model),
    statusCode: isHttpStatusCode(rawStatusCode) ? rawStatusCode : undefined,
    systemAccountId: optionalQueryText(query.systemAccountId),
    apiKeyId: optionalQueryText(query.apiKeyId),
    groupId: optionalQueryText(query.groupId),
    accountId: optionalQueryText(query.accountId)
  }
}

function parseAuditHotSearchOptions(query: Record<string, unknown>): { keywords: string[]; limit?: number; startAt?: string; endAt?: string } {
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

function isHttpStatusCode(value: unknown): value is number {
  return Number.isInteger(value) && Number(value) >= 100 && Number(value) <= 599
}

function auditTrafficSourceQueryValue(value: unknown): AuditTrafficSource | undefined {
  return typeof value === 'string' && auditTrafficSources.has(value as AuditTrafficSource)
    ? value as AuditTrafficSource
    : undefined
}

function auditLogRuntimeUnavailableReason(
  enabled: boolean,
  runtimeAvailable: boolean,
  workerSnapshotAvailable: boolean,
  auditLogQueueAvailable: boolean
): string | undefined {
  if (!enabled) return 'audit_disabled'
  if (!runtimeAvailable) return 'server_runtime_unavailable'
  if (!workerSnapshotAvailable) return 'worker_snapshot_unavailable'
  if (!auditLogQueueAvailable) return 'audit_log_queue_unavailable'
  return undefined
}
