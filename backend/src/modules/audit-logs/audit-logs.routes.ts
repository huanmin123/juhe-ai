import { Router } from 'express'

import { ok, sendNotFound } from '../../shared/http.js'
import {
  getAuditLogDetail,
  getAuditLogPayload,
  listAuditErrorGroupEvents,
  listAuditErrorGroups,
  type AuditErrorGroupListOptions,
  listAuditLogs,
  type AuditLogListOptions,
  type AuditOutcome
} from '../../storage/repositories.js'
import { getActiveAuditCaptureCount } from '../gateway/audit-capture.service.js'
import { readAuditLogSettings } from './audit-log-settings.js'
import { getBackgroundWorkerState, requestBackgroundWorkerSnapshot } from '../background/background-ipc.js'

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
  const workerSnapshot = await requestBackgroundWorkerSnapshot()
  const auditLogQueue = workerSnapshot?.auditLogQueue
  res.json(ok({
    queueLength: auditLogQueue?.queueLength ?? 0,
    queueBytes: auditLogQueue?.queueBytes ?? 0,
    flushLastSuccessAt: auditLogQueue?.flushLastSuccessAt,
    flushLastError: auditLogQueue?.flushLastError,
    droppedSuccessCount: auditLogQueue?.droppedSuccessCount ?? 0,
    droppedFailureCount: auditLogQueue?.droppedFailureCount ?? 0,
    droppedOverflowCount: auditLogQueue?.droppedOverflowCount ?? 0,
    droppedOversizeCount: auditLogQueue?.droppedOversizeCount ?? auditLogQueue?.droppedCount ?? 0,
    activeCaptureCount: getActiveAuditCaptureCount(),
    worker: {
      pid: workerSnapshot?.pid ?? getBackgroundWorkerState().pid,
      ready: workerSnapshot?.ready ?? getBackgroundWorkerState().ready,
      pendingMessageCount: getBackgroundWorkerState().pendingMessageCount
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

auditLogsRouter.get('/:id/payloads/:payloadId', (req, res) => {
  const payload = getAuditLogPayload(req.params.id, req.params.payloadId)
  if (!payload) {
    sendNotFound(res, '审计原文不存在')
    return
  }
  res.json(ok(payload))
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

function parseAuditLogListOptions(query: Record<string, unknown>): AuditLogListOptions {
  const rawPage = numberQueryValue(query.page)
  const rawPageSize = numberQueryValue(query.pageSize)
  const rawLimit = typeof query.limit === 'string' ? Number(query.limit) : undefined
  const rawStatusCode = typeof query.statusCode === 'string' ? Number(query.statusCode) : undefined
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
    clientIp: optionalQueryText(query.clientIp)
  }
}

function parseAuditErrorGroupListOptions(query: Record<string, unknown>): AuditErrorGroupListOptions {
  const rawPage = numberQueryValue(query.page)
  const rawPageSize = numberQueryValue(query.pageSize)
  const rawLimit = typeof query.limit === 'string' ? Number(query.limit) : undefined
  const rawStatusCode = typeof query.statusCode === 'string' ? Number(query.statusCode) : undefined
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

function numberQueryValue(value: unknown): number | undefined {
  const text = Array.isArray(value) ? value[0] : value
  const number = typeof text === 'string' ? Number(text) : undefined
  return typeof number === 'number' && Number.isFinite(number) ? number : undefined
}

function isHttpStatusCode(value: unknown): value is number {
  return Number.isInteger(value) && Number(value) >= 100 && Number(value) <= 599
}

function optionalQueryText(value: unknown): string | undefined {
  const text = Array.isArray(value) ? value[0] : value
  return typeof text === 'string' && text.trim() ? text.trim() : undefined
}
