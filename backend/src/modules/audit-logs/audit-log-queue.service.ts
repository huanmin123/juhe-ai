import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import { nowIso } from '../../storage/database.js'
import { createAuditLogsBatch, type AuditLogInput } from '../../storage/repositories.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { sendAuditLogsToWorker } from '../background/background-ipc.js'
import { readAuditLogSettings } from './audit-log-settings.js'

const auditLogRetryDelayMs = 5000

let pendingAuditLogs: QueuedAuditLog[] = []
let flushTimer: NodeJS.Timeout | undefined
let flushing = false
let droppedSuccessCount = 0
let droppedFailureCount = 0
let droppedOverflowCount = 0
let droppedOversizeCount = 0
let pendingBytes = 0
let lastFlushSuccessAt: string | undefined
let lastFlushError: string | undefined

interface QueuedAuditLog {
  input: AuditLogInput
  bytes: number
  success: boolean
}

interface AuditLogFlushOptions {
  drain?: boolean
  retryOnFailure?: boolean
}

export interface AuditLogQueueRuntime {
  queueLength: number
  queueBytes: number
  flushLastSuccessAt?: string
  flushLastError?: string
  droppedSuccessCount: number
  droppedFailureCount: number
  droppedOverflowCount: number
  droppedOversizeCount: number
  successRetentionDays: number
  failureRetentionDays: number
  errorGroupRetentionDays: number
}

export function recordDroppedAuditCapture(input: {
  traceId: string
  auditOutcome: string
  success: boolean
  bytes: number
  reason: 'active_capture_overflow' | 'gateway_body_rejected'
  method?: string
  path?: string
  queryString?: string
  statusCode?: number
  errorPhase?: string
  errorCode?: string
  errorMessage?: string
}): void {
  const timestamp = nowIso()
  enqueueAuditLog({
    id: `audit_${Date.now()}_${randomUUID()}`,
    traceId: input.traceId,
    auditOutcome: input.auditOutcome as AuditLogInput['auditOutcome'],
    success: input.success,
    method: input.method?.toUpperCase() ?? 'UNKNOWN',
    path: input.path ?? 'unknown',
    queryString: input.queryString,
    finalStatusCode: input.statusCode,
    errorPhase: input.errorPhase,
    errorCode: input.errorCode,
    errorMessage: input.errorMessage,
    sampleBucket: 0,
    sampleReason: input.reason,
    captureStatus: input.reason === 'gateway_body_rejected' ? 'overflow' : 'dropped',
    startedAt: timestamp,
    endedAt: timestamp,
    attempts: [],
    payloads: []
  })
}

export function enqueueAuditLog(input: AuditLogInput): void {
  const queuedInput = normalizeAuditLogInput(input)
  if (runtimeConfig.processRole === 'server') {
    if (!sendAuditLogsToWorker([queuedInput])) {
      recordDrop({
        input: queuedInput,
        bytes: estimateAuditLogBytes(queuedInput),
        success: queuedInput.success
      }, 'overflow')
    }
    return
  }

  enqueueAuditLogLocal(queuedInput)
}

export function enqueueAuditLogsLocal(inputs: AuditLogInput[]): void {
  assertLocalAuditLogWriteAllowed('enqueueAuditLogsLocal')
  for (const input of inputs) {
    enqueueAuditLogLocal(normalizeAuditLogInput(input))
  }
}

function enqueueAuditLogLocal(input: AuditLogInput): void {
  assertLocalAuditLogWriteAllowed('enqueueAuditLogLocal')
  const settings = readAuditLogSettings()
  if (!settings.enabled) {
    return
  }
  const queued: QueuedAuditLog = {
    input,
    bytes: estimateAuditLogBytes(input),
    success: input.success
  }
  if (queued.bytes > settings.queueMaxBytes) {
    recordDrop(queued, 'oversize')
    return
  }

  pendingAuditLogs.push(queued)
  pendingBytes += queued.bytes
  enforceAuditQueueLimits(settings)
  scheduleAuditLogFlush(pendingAuditLogs.length >= settings.batchSize ? 0 : settings.flushIntervalSeconds * 1000)
}

export function flushAuditLogQueue(options: AuditLogFlushOptions = {}): void {
  if (runtimeConfig.processRole === 'server') {
    return
  }
  if (flushing || pendingAuditLogs.length === 0) return

  if (flushTimer) {
    clearTimeout(flushTimer)
    flushTimer = undefined
  }

  flushing = true
  let shouldRetry = false
  let failed = false
  try {
    const settings = readAuditLogSettings()
    do {
      const batch = pendingAuditLogs.splice(0, settings.batchSize)
      if (batch.length === 0) break
      pendingBytes -= sumQueuedBytes(batch)

      try {
        createAuditLogsBatch(batch.map((item) => item.input))
        lastFlushSuccessAt = nowIso()
        lastFlushError = undefined
      } catch (error) {
        failed = true
        pendingAuditLogs = [...batch, ...pendingAuditLogs]
        pendingBytes = sumQueuedBytes(pendingAuditLogs)
        enforceAuditQueueLimits(settings)
        lastFlushError = error instanceof Error ? error.message : String(error)
        logger.error(errorLogFields(error, {
          event: 'audit_log_queue_flush_failed',
          batchSize: batch.length,
          pendingCount: pendingAuditLogs.length,
          pendingBytes
        }), '审计日志队列写入失败')
        shouldRetry = options.retryOnFailure !== false
        break
      }
    } while (options.drain && pendingAuditLogs.length > 0)
  } finally {
    flushing = false
  }

  if (pendingAuditLogs.length > 0 && (!failed || shouldRetry)) {
    scheduleAuditLogFlush(shouldRetry ? auditLogRetryDelayMs : 0)
  }
}

export function flushAllAuditLogQueue(): void {
  flushAuditLogQueue({ drain: true, retryOnFailure: false })
}

export function getAuditLogQueueRuntime(): AuditLogQueueRuntime {
  const settings = readAuditLogSettings()
  return {
    queueLength: pendingAuditLogs.length,
    queueBytes: pendingBytes,
    flushLastSuccessAt: lastFlushSuccessAt,
    flushLastError: lastFlushError,
    droppedSuccessCount,
    droppedFailureCount,
    droppedOverflowCount,
    droppedOversizeCount,
    successRetentionDays: settings.successRetentionDays,
    failureRetentionDays: settings.failureRetentionDays,
    errorGroupRetentionDays: settings.errorGroupRetentionDays
  }
}

function enforceAuditQueueLimits(settings = readAuditLogSettings()): void {
  while (pendingAuditLogs.length > settings.queueMaxItems || pendingBytes > settings.queueMaxBytes) {
    const successIndex = pendingAuditLogs.findIndex((item) => item.success)
    const dropIndex = successIndex >= 0 ? successIndex : 0
    const [dropped] = pendingAuditLogs.splice(dropIndex, 1)
    if (!dropped) break
    pendingBytes -= dropped.bytes
    recordDrop(dropped, 'overflow')
  }
}

function recordDrop(item: QueuedAuditLog, reason: 'overflow' | 'oversize'): void {
  if (item.success) {
    droppedSuccessCount += 1
  } else {
    droppedFailureCount += 1
  }
  if (reason === 'overflow') {
    droppedOverflowCount += 1
  } else {
    droppedOversizeCount += 1
  }
  logger.warn({
    event: 'audit_log_queue_dropped',
    reason,
    traceId: item.input.traceId,
    auditOutcome: item.input.auditOutcome,
    success: item.success,
    bytes: item.bytes,
    pendingCount: pendingAuditLogs.length,
    pendingBytes,
    droppedSuccessCount,
    droppedFailureCount,
    droppedOverflowCount,
    droppedOversizeCount
  }, '审计日志已丢弃')
}

function scheduleAuditLogFlush(delayMs: number): void {
  if (runtimeConfig.processRole === 'server') {
    return
  }
  if (delayMs <= 0 && flushTimer) {
    clearTimeout(flushTimer)
    flushTimer = undefined
  }
  if (flushTimer || flushing) return
  flushTimer = setTimeout(() => {
    flushTimer = undefined
    flushAuditLogQueue()
  }, delayMs)
  flushTimer.unref()
}

function estimateAuditLogBytes(input: AuditLogInput): number {
  return input.payloads.reduce((sum, payload) => {
    const body = payload.body
    const bodyBytes = Buffer.isBuffer(body) ? body.byteLength : typeof body === 'string' ? Buffer.byteLength(body, 'utf8') : 0
    const headerBytes = payload.headers ? estimateHeadersBytes(payload.headers) : 0
    return sum + bodyBytes + headerBytes + 512
  }, 1024 + input.attempts.length * 512)
}

function estimateHeadersBytes(headers: Record<string, string | string[]>): number {
  let bytes = 2
  for (const [name, value] of Object.entries(headers)) {
    bytes += Buffer.byteLength(name, 'utf8') + 4
    if (Array.isArray(value)) {
      bytes += 2
      for (const item of value) {
        bytes += Buffer.byteLength(item, 'utf8') + 3
      }
    } else {
      bytes += Buffer.byteLength(value, 'utf8') + 2
    }
  }
  return bytes
}

function sumQueuedBytes(items: QueuedAuditLog[]): number {
  return items.reduce((sum, item) => sum + item.bytes, 0)
}

function normalizeAuditLogInput(input: AuditLogInput): AuditLogInput {
  return {
    ...input,
    id: input.id ?? `audit_${Date.now()}_${randomUUID()}`,
    attempts: input.attempts.map((attempt) => ({
      ...attempt,
      id: attempt.id ?? `audatt_${Date.now()}_${randomUUID()}`
    })),
    payloads: input.payloads.map((payload) => ({
      ...payload,
      id: payload.id ?? `audpay_${Date.now()}_${randomUUID()}`
    })),
    createdAt: input.createdAt ?? nowIso()
  }
}

function assertLocalAuditLogWriteAllowed(operation: string): void {
  if (runtimeConfig.processRole === 'server') {
    throw new Error(`server 角色禁止直接同步写入 SQLite：${operation} 必须投递 background worker`)
  }
}
