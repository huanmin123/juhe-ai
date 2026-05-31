import { Router } from 'express'

import { badRequest, ok, sendNotFound } from '../../shared/http.js'
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
import { readAuditLogSettings, type AuditFullBodyCaptureConfigInput } from './audit-log-settings.js'
import { requestServerRuntimeSnapshot, updateServerAuditFullBodyCaptureConfig } from '../db-service/db-service-ipc.js'

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
  const settings = readAuditLogSettings()
  if (typeof serverRuntime?.audit?.fullBodyCaptureEnabled === 'boolean') {
    settings.fullBodyCaptureEnabled = serverRuntime.audit.fullBodyCaptureEnabled
    if (serverRuntime.audit.fullBodyCapture) {
      settings.fullBodyCapture = serverRuntime.audit.fullBodyCapture
    }
  }
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
    droppedOversizeCount: auditLogQueue?.droppedOversizeCount ?? null,
    activeCaptureCount: serverRuntime?.activeAuditCaptureCount ?? null,
    worker: {
      available: Boolean(workerSnapshot ?? workerRuntime),
      snapshotAvailable: workerSnapshotAvailable,
      pid: workerSnapshot?.pid ?? workerRuntime?.pid,
      ready: workerSnapshot?.ready ?? workerRuntime?.ready ?? null,
      pendingMessageCount: workerRuntime?.pendingMessageCount ?? null
    },
    settings
  }))
})

auditLogsRouter.patch('/runtime/full-body-capture', async (req, res) => {
  const parsed = parseAuditFullBodyCaptureUpdate(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.message))
    return
  }

  const result = await updateServerAuditFullBodyCaptureConfig(parsed.data)
  if (!result) {
    res.status(503).json({ message: '临时全量捕获运行期切换失败，请稍后重试' })
    return
  }

  res.json(ok({
    fullBodyCaptureEnabled: result.fullBodyCaptureEnabled,
    fullBodyCapture: result.fullBodyCapture,
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
  const rawStatusCode = finiteNumberQueryValue(query.statusCode)
  return {
    page: Number.isInteger(rawPage) ? rawPage : undefined,
    pageSize: Number.isInteger(rawPageSize) ? rawPageSize : undefined,
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

function isHttpStatusCode(value: unknown): value is number {
  return Number.isInteger(value) && Number(value) >= 100 && Number(value) <= 599
}

function parseAuditFullBodyCaptureUpdate(body: unknown): { success: true; data: AuditFullBodyCaptureConfigInput } | { success: false; message: string } {
  if (typeof body !== 'object' || body === null || Array.isArray(body)) {
    return { success: false, message: '临时全量捕获参数无效' }
  }
  const record = body as Record<string, unknown>
  if (typeof record.enabled !== 'boolean') {
    return { success: false, message: '临时全量捕获参数无效' }
  }
  const enabled = record.enabled
  const scope = record.scope === 'account' ? 'account' : 'global'
  const accountId = typeof record.accountId === 'string' ? record.accountId.trim() : ''
  if (enabled && scope === 'account' && !accountId) {
    return { success: false, message: '请选择要定向捕获的 AI 账户' }
  }

  const durationMinutes = typeof record.durationMinutes === 'number' && Number.isFinite(record.durationMinutes)
    ? Math.min(Math.max(Math.trunc(record.durationMinutes), 1), 24 * 60)
    : undefined
  const expiresAt = typeof record.expiresAt === 'string' ? record.expiresAt : undefined
  if (enabled && durationMinutes === undefined && expiresAt !== undefined && !Number.isFinite(Date.parse(expiresAt))) {
    return { success: false, message: '临时全量捕获过期时间无效' }
  }

  return {
    success: true,
    data: {
      enabled,
      scope,
      accountId: scope === 'account' ? accountId : undefined,
      includeSuccess: record.includeSuccess === true,
      durationMinutes,
      expiresAt
    }
  }
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
