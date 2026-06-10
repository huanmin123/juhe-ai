import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import { nowIso } from '../../storage/database.js'
import { createAuditLogsBatch, createAuditLogsBatchAsync, type AuditLogInput } from '../../storage/repositories.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { sanitizeUrlForLog } from '../../shared/request-context.js'
import { fixedRetryPolicy, retryDelayMs } from '../../shared/retry-policy.js'
import { sendAuditLogsToWorker } from '../background/background-ipc.js'
import { readAuditLogSettings } from './audit-log-settings.js'

const auditLogRetryPolicy = fixedRetryPolicy('audit_log_queue_flush', 5000)
const auditLogFlushBatchMaxBytes = 8 * 1024 * 1024
const auditLogShutdownFlushMaxBatches = 1
const auditLogEstimateMaxBytes = 64 * 1024 * 1024 + 1
const auditLogEstimateMaxStringChars = 16 * 1024

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
let asyncFlushPromise: Promise<void> | undefined
let shutdownHooksInstalled = false

interface QueuedAuditLog {
  input: AuditLogInput
  bytes: number
  success: boolean
}

interface AuditLogFlushOptions {
  drain?: boolean
  retryOnFailure?: boolean
  maxBatches?: number
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
  successHotRetentionHours: number
  successRetentionDays: number
  failureRetentionDays: number
  errorGroupRetentionDays: number
}

export function recordDroppedAuditCapture(input: {
  traceId: string
  auditOutcome: string
  success: boolean
  bytes: number
  reason: 'active_capture_overflow' | 'gateway_auth_rejected' | 'gateway_body_rejected' | 'gateway_permission_rejected'
  method?: string
  path?: string
  queryString?: string
  statusCode?: number
  errorPhase?: string
  errorCode?: string
  errorMessage?: string
  clientIp?: string
  userAgent?: string
}): void {
  const timestamp = nowIso()
  const sanitizedUrl = sanitizeDroppedAuditUrl(input.path, input.queryString)
  enqueueAuditLog({
    id: `audit_${Date.now()}_${randomUUID()}`,
    traceId: input.traceId,
    auditOutcome: input.auditOutcome as AuditLogInput['auditOutcome'],
    success: input.success,
    method: input.method?.toUpperCase() ?? 'UNKNOWN',
    path: sanitizedUrl.path,
    queryString: sanitizedUrl.queryString,
    clientIp: input.clientIp,
    userAgent: input.userAgent,
    finalStatusCode: input.statusCode,
    errorPhase: input.errorPhase,
    errorCode: input.errorCode,
    errorMessage: input.errorMessage,
    sampleBucket: 0,
    sampleReason: input.reason,
    captureStatus: input.reason === 'gateway_body_rejected'
      ? 'overflow'
      : input.reason === 'gateway_auth_rejected' || input.reason === 'gateway_permission_rejected'
        ? 'complete'
        : 'dropped',
    startedAt: timestamp,
    endedAt: timestamp,
    attempts: [],
    payloads: []
  })
}

function sanitizeDroppedAuditUrl(path?: string, queryString?: string): { path: string; queryString?: string } {
  const rawPath = path?.trim() || 'unknown'
  const pathWithoutQuery = rawPath.split('?')[0] || 'unknown'
  const url = queryString ? `${pathWithoutQuery}?${queryString}` : rawPath
  const sanitized = sanitizeUrlForLog(url)
  const [sanitizedPath, ...queryParts] = sanitized.split('?')
  const sanitizedQuery = queryParts.length ? queryParts.join('?') : undefined
  return {
    path: sanitizedPath || 'unknown',
    queryString: sanitizedQuery
  }
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
  let flushedBatches = 0
  const maxBatches = normalizeMaxBatches(options.maxBatches)
  try {
    const settings = readAuditLogSettings()
    do {
      const { batch, bytes: batchBytes } = peekAuditLogFlushBatch(settings.batchSize, auditLogFlushBatchMaxBytes)
      if (batch.length === 0) break
      flushedBatches += 1

      try {
        createAuditLogsBatch(batch.map((item) => item.input))
        removeAuditLogFlushBatch(batch.length, batchBytes)
        lastFlushSuccessAt = nowIso()
        lastFlushError = undefined
      } catch (error) {
        failed = true
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
    } while (options.drain && pendingAuditLogs.length > 0 && flushedBatches < maxBatches)
  } finally {
    flushing = false
  }

  if (pendingAuditLogs.length > 0 && (!failed || shouldRetry)) {
    scheduleAuditLogFlush(shouldRetry ? retryDelayMs(auditLogRetryPolicy) : 0)
  }
}

export async function flushAuditLogQueueAsync(options: AuditLogFlushOptions = {}): Promise<void> {
  if (asyncFlushPromise) {
    await asyncFlushPromise
    if (!options.drain || pendingAuditLogs.length === 0) {
      return
    }
  }
  const promise = flushAuditLogQueueAsyncInner(options)
  asyncFlushPromise = promise
  try {
    await promise
  } finally {
    if (asyncFlushPromise === promise) {
      asyncFlushPromise = undefined
    }
  }
}

async function flushAuditLogQueueAsyncInner(options: AuditLogFlushOptions = {}): Promise<void> {
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
  let flushedBatches = 0
  const maxBatches = normalizeMaxBatches(options.maxBatches)
  try {
    const settings = readAuditLogSettings()
    do {
      const { batch, bytes: batchBytes } = peekAuditLogFlushBatch(settings.batchSize, auditLogFlushBatchMaxBytes)
      if (batch.length === 0) break
      flushedBatches += 1

      try {
        await createAuditLogsBatchAsync(batch.map((item) => item.input))
        removeAuditLogFlushBatch(batch.length, batchBytes)
        lastFlushSuccessAt = nowIso()
        lastFlushError = undefined
      } catch (error) {
        failed = true
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
    } while (options.drain && pendingAuditLogs.length > 0 && flushedBatches < maxBatches)
  } finally {
    flushing = false
  }

  if (pendingAuditLogs.length > 0 && (!failed || shouldRetry)) {
    scheduleAuditLogFlush(shouldRetry ? retryDelayMs(auditLogRetryPolicy) : 0)
  }
}

export function flushAllAuditLogQueue(): void {
  flushAuditLogQueue({ drain: true, retryOnFailure: false })
}

export async function flushAllAuditLogQueueAsync(): Promise<void> {
  await flushAuditLogQueueAsync({ drain: true, retryOnFailure: false })
}

export function installAuditLogQueueShutdownHooks(): void {
  if (shutdownHooksInstalled) {
    return
  }
  shutdownHooksInstalled = true

  process.once('beforeExit', () => {
    void flushAuditLogQueueForShutdown()
  })
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
    successHotRetentionHours: settings.successHotRetentionHours,
    successRetentionDays: settings.successRetentionDays,
    failureRetentionDays: settings.failureRetentionDays,
    errorGroupRetentionDays: settings.errorGroupRetentionDays
  }
}

export function clearAuditLogQueueForTest(): void {
  if (flushTimer) {
    clearTimeout(flushTimer)
    flushTimer = undefined
  }
  pendingAuditLogs = []
  pendingBytes = 0
  flushing = false
  droppedSuccessCount = 0
  droppedFailureCount = 0
  droppedOverflowCount = 0
  droppedOversizeCount = 0
  lastFlushSuccessAt = undefined
  lastFlushError = undefined
  asyncFlushPromise = undefined
  shutdownHooksInstalled = false
}

export async function flushAuditLogQueueForShutdown(): Promise<void> {
  try {
    await flushAuditLogQueueAsync({ drain: true, retryOnFailure: false, maxBatches: auditLogShutdownFlushMaxBatches })
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'audit_log_queue_shutdown_flush_failed',
      pendingCount: pendingAuditLogs.length,
      pendingBytes
    }), '审计日志队列退出前异步 flush 失败')
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

function peekAuditLogFlushBatch(maxItems: number, maxBytes: number): { batch: QueuedAuditLog[]; bytes: number } {
  const itemLimit = Math.max(1, Math.trunc(maxItems))
  const byteLimit = Math.max(1, Math.trunc(maxBytes))
  let count = 0
  let bytes = 0
  while (count < itemLimit && count < pendingAuditLogs.length) {
    const next = pendingAuditLogs[count]
    if (!next) break
    if (count > 0 && bytes + next.bytes > byteLimit) {
      break
    }
    bytes += next.bytes
    count += 1
  }
  const batch = pendingAuditLogs.slice(0, count)
  return { batch, bytes }
}

function removeAuditLogFlushBatch(count: number, bytes: number): void {
  pendingAuditLogs.splice(0, count)
  pendingBytes = Math.max(0, pendingBytes - bytes)
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
    void flushAuditLogQueueAsync().catch((error) => {
      lastFlushError = error instanceof Error ? error.message : String(error)
      logger.error(errorLogFields(error, {
        event: 'audit_log_queue_async_flush_unhandled_error',
        pendingCount: pendingAuditLogs.length,
        pendingBytes
      }), '审计日志异步 flush 未处理异常')
      if (pendingAuditLogs.length > 0) {
        scheduleAuditLogFlush(retryDelayMs(auditLogRetryPolicy))
      }
    })
  }, delayMs)
  flushTimer.unref()
}

function estimateAuditLogBytes(input: AuditLogInput): number {
  let bytes = 1024 + input.attempts.length * 512
  for (const payload of input.payloads) {
    const body = payload.body
    const bodyBytes = Buffer.isBuffer(body) ? body.byteLength : typeof body === 'string' ? boundedStringBytes(body, auditLogEstimateMaxBytes - bytes) : 0
    const headerBytes = payload.headers ? estimateHeadersBytes(payload.headers) : 0
    bytes = Math.min(auditLogEstimateMaxBytes, bytes + bodyBytes + headerBytes + 512)
    if (bytes >= auditLogEstimateMaxBytes) break
  }
  return bytes
}

function estimateHeadersBytes(headers: Record<string, string | string[]>): number {
  let bytes = 2
  for (const name in headers) {
    if (!Object.prototype.hasOwnProperty.call(headers, name)) continue
    const value = headers[name]
    bytes += boundedStringBytes(name, auditLogEstimateMaxBytes - bytes) + 4
    if (Array.isArray(value)) {
      bytes += 2
      for (const item of value) {
        bytes += boundedStringBytes(item, auditLogEstimateMaxBytes - bytes) + 3
        if (bytes >= auditLogEstimateMaxBytes) return bytes
      }
    } else {
      bytes += boundedStringBytes(value, auditLogEstimateMaxBytes - bytes) + 2
    }
    if (bytes >= auditLogEstimateMaxBytes) return bytes
  }
  return bytes
}

function boundedStringBytes(value: string, maxBytes: number): number {
  if (maxBytes <= 0) return 0
  if (value.length <= auditLogEstimateMaxStringChars) {
    return Math.min(maxBytes, Buffer.byteLength(value, 'utf8'))
  }
  return Math.min(maxBytes, value.length * 4)
}

function normalizeMaxBatches(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : Number.POSITIVE_INFINITY
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
