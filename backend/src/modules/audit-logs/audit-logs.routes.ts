import { Router } from 'express'

import { ok, sendNotFound } from '../../shared/http.js'
import { finiteNumberQueryValue, optionalQueryText } from '../../shared/query-values.js'
import {
  getAuditLogDetail,
  getAuditLogPayload,
  listAuditErrorGroupEvents,
  listAuditErrorGroups,
  type AuditErrorGroupListOptions,
  listAuditLogs,
  type AuditLogListOptions,
  type AuditOutcome,
  type AuditTrafficSource
} from '../../storage/repositories.js'
import { readAuditLogSettings } from './audit-log-settings.js'
import { requestServerRuntimeSnapshot } from '../db-service/db-service-ipc.js'

export const auditLogsRouter = Router()

auditLogsRouter.use((req, res, next) => {
  req.setTimeout(0)
  res.setTimeout(0)
  next()
})

auditLogsRouter.get('/', (req, res) => {
  res.json(ok(listAuditLogs(parseAuditLogListOptions(req.query))))
})

auditLogsRouter.get('/runtime', async (_req, res) => {
  const serverRuntime = await requestServerRuntimeSnapshot()
  const workerSnapshot = serverRuntime?.worker?.snapshot
  const auditLogQueue = workerSnapshot?.auditLogQueue
  const workerRuntime = serverRuntime?.worker
  const runtimeAvailable = Boolean(serverRuntime)
  const workerSnapshotAvailable = Boolean(workerSnapshot)
  const auditLogQueueAvailable = Boolean(auditLogQueue)
  res.json(ok({
    runtimeAvailable,
    workerSnapshotAvailable,
    auditLogQueueAvailable,
    activeCaptureAvailable: serverRuntime?.activeAuditCaptureCount !== undefined,
    unavailableReason: auditLogRuntimeUnavailableReason(runtimeAvailable, workerSnapshotAvailable, auditLogQueueAvailable),
    queueLength: auditLogQueue?.queueLength ?? null,
    queueBytes: auditLogQueue?.queueBytes ?? null,
    flushLastSuccessAt: auditLogQueue?.flushLastSuccessAt,
    flushLastError: auditLogQueue?.flushLastError,
    droppedSuccessCount: auditLogQueue?.droppedSuccessCount ?? null,
    droppedFailureCount: auditLogQueue?.droppedFailureCount ?? null,
    droppedOverflowCount: auditLogQueue?.droppedOverflowCount ?? null,
    droppedOversizeCount: auditLogQueue?.droppedOversizeCount ?? auditLogQueue?.droppedCount ?? null,
    activeCaptureCount: serverRuntime?.activeAuditCaptureCount ?? null,
    worker: {
      available: Boolean(workerSnapshot ?? workerRuntime),
      snapshotAvailable: workerSnapshotAvailable,
      pid: workerSnapshot?.pid ?? workerRuntime?.pid,
      ready: workerSnapshot?.ready ?? workerRuntime?.ready ?? null,
      pendingMessageCount: workerRuntime?.pendingMessageCount ?? null
    },
    settings: readAuditLogSettings()
  }))
})

auditLogsRouter.get('/error-groups', (req, res) => {
  res.json(ok(listAuditErrorGroups(parseAuditErrorGroupListOptions(req.query))))
})

auditLogsRouter.get('/error-groups/:id/events', (req, res) => {
  res.json(ok(listAuditErrorGroupEvents(req.params.id, parseAuditLogListOptions(req.query))))
})

auditLogsRouter.get('/:id', (req, res) => {
  const detail = getAuditLogDetail(req.params.id)
  if (!detail) {
    sendNotFound(res, '审计日志不存在')
    return
  }
  res.json(ok(detail))
})

auditLogsRouter.get('/:id/payloads/:payloadId', async (req, res, next) => {
  try {
    const payload = await getAuditLogPayload(req.params.id, req.params.payloadId, {
      offset: finiteNumberQueryValue(req.query.offset),
      limit: finiteNumberQueryValue(req.query.limit)
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
  'gateway_failed',
  'upstream_failed',
  'stream_failed',
  'client_aborted'
])
const auditTrafficSources = new Set<AuditTrafficSource>(['gateway', 'manual_account_test', 'cooldown_retest'])

function parseAuditLogListOptions(query: Record<string, unknown>): AuditLogListOptions {
  const rawPage = finiteNumberQueryValue(query.page)
  const rawPageSize = finiteNumberQueryValue(query.pageSize)
  const rawLimit = finiteNumberQueryValue(query.limit)
  const rawStatusCode = finiteNumberQueryValue(query.statusCode)
  return {
    page: Number.isInteger(rawPage) ? rawPage : undefined,
    pageSize: Number.isInteger(rawPageSize) ? rawPageSize : undefined,
    limit: Number.isInteger(rawLimit) ? rawLimit : undefined,
    traceId: optionalQueryText(query.traceId),
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
    trafficSource: auditTrafficSourceQueryValue(query.trafficSource)
  }
}

function parseAuditErrorGroupListOptions(query: Record<string, unknown>): AuditErrorGroupListOptions {
  const rawPage = finiteNumberQueryValue(query.page)
  const rawPageSize = finiteNumberQueryValue(query.pageSize)
  const rawLimit = finiteNumberQueryValue(query.limit)
  const rawStatusCode = finiteNumberQueryValue(query.statusCode)
  return {
    page: Number.isInteger(rawPage) ? rawPage : undefined,
    pageSize: Number.isInteger(rawPageSize) ? rawPageSize : undefined,
    limit: Number.isInteger(rawLimit) ? rawLimit : undefined,
    path: optionalQueryText(query.path),
    model: optionalQueryText(query.model),
    statusCode: isHttpStatusCode(rawStatusCode) ? rawStatusCode : undefined,
    systemAccountId: optionalQueryText(query.systemAccountId),
    apiKeyId: optionalQueryText(query.apiKeyId),
    groupId: optionalQueryText(query.groupId),
    accountId: optionalQueryText(query.accountId)
  }
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
  runtimeAvailable: boolean,
  workerSnapshotAvailable: boolean,
  auditLogQueueAvailable: boolean
): string | undefined {
  if (!runtimeAvailable) return 'server_runtime_unavailable'
  if (!workerSnapshotAvailable) return 'worker_snapshot_unavailable'
  if (!auditLogQueueAvailable) return 'audit_log_queue_unavailable'
  return undefined
}
