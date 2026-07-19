import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import { nowIso } from '../../storage/database.js'
import type { AuditLogInput } from '../../storage/audit-log-types.js'
import { createAuditLogsBatch, createAuditLogsBatchAsync } from '../../storage/repositories.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { RedisStreamQueue, type RedisStreamMessage, type RedisStreamQueueRuntime } from '../../shared/redis-stream-queue.js'
import { sanitizeUrlForLog } from '../../shared/request-context.js'
import { fixedRetryPolicy, retryDelayMs } from '../../shared/retry-policy.js'
import { sendAuditLogsToWorker } from '../background/background-ipc.js'
import { buildAuditLogTransportCapacityFallback } from './audit-log-capacity-fallback.js'
import { readAuditLogSettings } from './audit-log-settings.js'
import { decodeAuditLogStreamPayload, encodeAuditLogStreamPayload } from './audit-log-stream-codec.js'
import {
  AuditLogTransportError,
  AuditLogTransportQueueFullError,
  encodeAuditLogForRedisStreamInWorker,
  prepareAuditLogForIpcInWorker,
  stopAuditLogTransportWorker
} from './audit-log-transport.service.js'

const auditLogRetryPolicy = fixedRetryPolicy('audit_log_queue_flush', 5000)
const auditLogFlushBatchMaxBytes = 8 * 1024 * 1024
const auditLogScheduledFlushMaxBatches = 20
const auditLogFlushBatchYieldMs = 5
const auditLogShutdownFlushMaxBatches = 100
const auditLogEstimateMaxBytes = 64 * 1024 * 1024 + 1
const auditLogEstimateMaxStringChars = 16 * 1024
const auditLogInlineTransportMaxBytes = 256 * 1024
const auditLogPostgresFlushBatchSize = 25
const auditLogPostgresRedisConsumerConcurrency = 1
const auditLogRedisStreamKey = 'juhe-ai:queue:audit-logs'
const auditLogRedisStreamGroup = 'juhe-ai:audit-log-writers'
const auditLogRedisConsumerErrorRetryMs = 1000

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
let allowDbServiceLocalAuditLogWriteForTest = false
let auditLogRedisStreamQueueInstance: RedisStreamQueue<AuditLogInput> | undefined
let auditLogRedisConsumerQueueInstances: Array<RedisStreamQueue<AuditLogInput> | undefined> = []
let auditLogRedisConsumerStarted = false
let auditLogRedisConsumerStopping = false
let auditLogRedisConsumerPromises: Array<Promise<void>> = []
const pendingAuditLogServerDispatches = new Set<Promise<void>>()

class AuditLogCapacityFallbackDispatchError extends AuditLogTransportError {
}

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
  problemRetentionDays: number
  successFullBodyLimitBytes: number
  problemFullBodyLimitBytes: number
}

export function recordDroppedAuditCapture(input: {
  traceId: string
  trafficSource?: AuditLogInput['trafficSource']
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
  contentType?: string
  clientIp?: string
  userAgent?: string
  systemAccountId?: string
  apiKeyId?: string
  groupId?: string
  providerCode?: string
}): void {
  const timestamp = nowIso()
  const sanitizedUrl = sanitizeDroppedAuditUrl(input.path, input.queryString)
  enqueueAuditLog({
    id: `audit_${Date.now()}_${randomUUID()}`,
    traceId: input.traceId,
    trafficSource: input.trafficSource,
    auditOutcome: input.auditOutcome as AuditLogInput['auditOutcome'],
    success: input.success,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId,
    groupId: input.groupId,
    providerCode: input.providerCode,
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
    payloads: input.reason === 'gateway_body_rejected' && input.statusCode === 413
      ? [{
          partType: 'client_request',
          sequenceIndex: 0,
          contentType: input.contentType,
          rawBodySizeBytes: input.bytes,
          captureStatus: 'overflow'
        }]
      : []
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
  if (runtimeConfig.processRole === 'server' && estimateAuditLogBytes(queuedInput) > auditLogInlineTransportMaxBytes) {
    trackAuditLogServerDispatch(
      dispatchAuditLogFromServerWithCapacityFallback(queuedInput),
      (error) => handleAuditLogServerDispatchError(queuedInput, error)
    )
    return
  }
  if (shouldEnqueueAuditLogToRedisStream()) {
    const dispatch = enqueueAuditLogToRedisStream(queuedInput)
    if (runtimeConfig.processRole === 'server') {
      trackAuditLogServerDispatch(dispatch, (error) => handleAuditLogServerDispatchError(queuedInput, error))
    } else {
      void dispatch.catch((error) => handleAuditLogServerDispatchError(queuedInput, error))
    }
    return
  }
  if (shouldDispatchAuditLogToIngestWorker()) {
    const dispatched = sendAuditLogsToWorker([queuedInput])
    logger.debug({
      event: 'audit_log_dispatch_to_ingest_worker',
      traceId: queuedInput.traceId,
      auditOutcome: queuedInput.auditOutcome,
      success: queuedInput.success,
      dispatched
    }, '审计日志已投递 ingest-worker')
    if (!dispatched) {
      recordAuditLogDispatchFailure(queuedInput)
    }
    return
  }

  if (runtimeConfig.processRole === 'db-service' && !isDbServiceLocalAuditLogWriteAllowedForTest()) {
    if (!sendAuditLogFromDbServiceToServer(queuedInput)) {
      recordAuditLogDispatchFailure(queuedInput)
    }
    return
  }

  enqueueAuditLogLocal(queuedInput)
}

function trackAuditLogServerDispatch(dispatch: Promise<void>, onError: (error: unknown) => void): void {
  pendingAuditLogServerDispatches.add(dispatch)
  void dispatch
    .catch(onError)
    .finally(() => pendingAuditLogServerDispatches.delete(dispatch))
}

export function getAuditLogServerDispatchPendingCount(): number {
  return pendingAuditLogServerDispatches.size
}

export async function waitForAuditLogServerDispatchIdle(timeoutMs = 10_000): Promise<boolean> {
  const deadline = Date.now() + Math.max(1, timeoutMs)
  while (pendingAuditLogServerDispatches.size > 0) {
    if (Date.now() >= deadline) return false
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 5))
  }
  await new Promise<void>((resolvePromise) => setImmediate(resolvePromise))
  return true
}

function handleAuditLogServerDispatchError(input: AuditLogInput, error: unknown): void {
  if (error instanceof AuditLogCapacityFallbackDispatchError) {
    logger.error(errorLogFields(error, {
      event: 'audit_log_transport_capacity_fallback_failed',
      auditLogId: input.id,
      traceId: input.traceId,
      auditOutcome: input.auditOutcome
    }), '审计传输容量降级记录仍无法投递，已明确记录整条审计丢弃')
    recordAuditLogDispatchFailure(input)
    return
  }
  if (error instanceof AuditLogTransportQueueFullError) {
    logger.error(errorLogFields(error, {
      event: 'audit_log_transport_capacity_rejected',
      auditLogId: input.id,
      traceId: input.traceId,
      auditOutcome: input.auditOutcome
    }), '审计传输 worker 容量不足，已明确记录本条审计丢弃')
    recordAuditLogDispatchFailure(input)
    return
  }
  if (error instanceof AuditLogTransportError) {
    logger.error(errorLogFields(error, {
      event: 'audit_log_transport_failed',
      auditLogId: input.id,
      traceId: input.traceId,
      auditOutcome: input.auditOutcome
    }), '审计传输 worker 处理失败，已明确记录本条审计丢弃')
    recordAuditLogDispatchFailure(input)
    return
  }
  logger.error(errorLogFields(error, {
    event: 'audit_log_transport_unexpected_failure',
    auditLogId: input.id,
    traceId: input.traceId,
    auditOutcome: input.auditOutcome
  }), '审计日志传输发生未分类失败，已记录丢弃并保持进程运行')
  recordAuditLogDispatchFailure(input)
}

async function dispatchAuditLogFromServerWithCapacityFallback(input: AuditLogInput): Promise<void> {
  try {
    await dispatchAuditLogFromServer(input)
    return
  } catch (error) {
    if (!(error instanceof AuditLogTransportQueueFullError)) {
      throw error
    }
    const fallback = buildAuditLogTransportCapacityFallback(input)
    logger.warn(errorLogFields(error, {
      event: 'audit_log_transport_capacity_rejected',
      auditLogId: input.id,
      traceId: input.traceId,
      auditOutcome: input.auditOutcome,
      originalBytes: estimateAuditLogBytes(input),
      fallbackBytes: estimateAuditLogBytes(fallback),
      fallbackPayloadCount: fallback.payloads.length,
      fallbackAttemptCount: fallback.attempts.length
    }), '审计传输 worker 容量不足，已移除正文并按有界元数据降级重试')
    await dispatchAuditLogCapacityFallbackFromServer(fallback)
    logger.warn({
      event: 'audit_log_transport_capacity_fallback_enqueued',
      auditLogId: fallback.id,
      traceId: fallback.traceId,
      auditOutcome: fallback.auditOutcome,
      fallbackBytes: estimateAuditLogBytes(fallback)
    }, '审计传输容量降级记录已成功投递')
  }
}

async function dispatchAuditLogCapacityFallbackFromServer(input: AuditLogInput): Promise<void> {
  if (shouldEnqueueAuditLogToRedisStream()) {
    await enqueueAuditLogToRedisStream(input)
    return
  }
  if (shouldDispatchAuditLogToIngestWorker()) {
    const dispatched = sendAuditLogsToWorker([input])
    if (!dispatched) {
      throw new AuditLogCapacityFallbackDispatchError('审计传输容量降级记录投递 ingest-worker 失败')
    }
    return
  }
  enqueueAuditLogLocal(input)
}

async function dispatchAuditLogFromServer(input: AuditLogInput): Promise<void> {
  if (shouldEnqueueAuditLogToRedisStream()) {
    const encoded = await encodeAuditLogForRedisStreamInWorker(input)
    await enqueueAuditLogToRedisStream(input, encoded)
    return
  }
  if (shouldDispatchAuditLogToIngestWorker()) {
    const prepared = await prepareAuditLogForIpcInWorker(input)
    const dispatched = sendAuditLogsToWorker([prepared])
    logger.debug({
      event: 'audit_log_dispatch_to_ingest_worker',
      traceId: prepared.traceId,
      auditOutcome: prepared.auditOutcome,
      success: prepared.success,
      dispatched
    }, '审计日志已投递 ingest-worker')
    if (!dispatched) {
      recordAuditLogDispatchFailure(prepared)
    }
    return
  }
  enqueueAuditLogLocal(input)
}

export function enqueueAuditLogsLocal(inputs: AuditLogInput[]): void {
  assertLocalAuditLogWriteAllowed('enqueueAuditLogsLocal')
  for (const input of inputs) {
    enqueueAuditLogLocal(normalizeAuditLogInput(input))
  }
}

export function startAuditLogRedisStreamConsumer(): void {
  if (!shouldUseRedisStreamAuditLogQueue() || !isAuditLogIngestWorker() || auditLogRedisConsumerStarted) {
    return
  }
  auditLogRedisConsumerStarted = true
  auditLogRedisConsumerStopping = false
  const concurrency = auditLogRedisConsumerConcurrency()
  auditLogRedisConsumerPromises = Array.from({ length: concurrency }, (_, index) => (
    runAuditLogRedisStreamConsumer(index).catch((error) => {
      logger.error(errorLogFields(error, {
        event: 'audit_log_redis_stream_consumer_stopped',
        consumerIndex: index
      }), 'Redis Stream 审计日志消费循环异常退出')
    })
  ))
  void Promise.all(auditLogRedisConsumerPromises).finally(() => {
    auditLogRedisConsumerStarted = false
    auditLogRedisConsumerPromises = []
  })
}

export async function stopAuditLogRedisStreamConsumer(): Promise<void> {
  auditLogRedisConsumerStopping = true
  const queues = [
    auditLogRedisStreamQueueInstance,
    ...auditLogRedisConsumerQueueInstances
  ].filter((queue): queue is RedisStreamQueue<AuditLogInput> => Boolean(queue))
  await Promise.all(queues.map((queue) => queue.closeConsumer().catch(() => undefined)))
  if (auditLogRedisConsumerPromises.length > 0) {
    await Promise.all(auditLogRedisConsumerPromises).catch(() => undefined)
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
  logger.debug({
    event: 'audit_log_local_queue_enqueued',
    traceId: input.traceId,
    auditOutcome: input.auditOutcome,
    success: input.success,
    queueLength: pendingAuditLogs.length,
    queueBytes: pendingBytes
  }, '审计日志已进入本地写入队列')
  enforceAuditQueueLimits(settings)
  const batchSize = effectiveAuditLogFlushBatchSize(settings.batchSize)
  scheduleAuditLogFlush(pendingAuditLogs.length >= batchSize ? 0 : settings.flushIntervalSeconds * 1000)
}

export function flushAuditLogQueue(options: AuditLogFlushOptions = {}): void {
  if (!isLocalAuditLogWriteAllowed()) {
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
      const { batch, bytes: batchBytes } = peekAuditLogFlushBatch(effectiveAuditLogFlushBatchSize(settings.batchSize), auditLogFlushBatchMaxBytes)
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
  if (!isLocalAuditLogWriteAllowed()) {
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
      const { batch, bytes: batchBytes } = peekAuditLogFlushBatch(effectiveAuditLogFlushBatchSize(settings.batchSize), auditLogFlushBatchMaxBytes)
      if (batch.length === 0) break
      flushedBatches += 1

      try {
        await createAuditLogsBatchAsync(batch.map((item) => item.input))
        removeAuditLogFlushBatch(batch.length, batchBytes)
        lastFlushSuccessAt = nowIso()
        lastFlushError = undefined
        logger.debug({
          event: 'audit_log_queue_async_flush_completed',
          batchSize: batch.length,
          batchBytes,
          pendingCount: pendingAuditLogs.length,
          pendingBytes
        }, '审计日志队列异步 flush 完成')
        if (options.drain && pendingAuditLogs.length > 0 && flushedBatches < maxBatches) {
          await yieldBetweenAuditLogFlushBatches()
        }
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
    problemRetentionDays: settings.problemRetentionDays,
    successFullBodyLimitBytes: settings.successFullBodyLimitBytes,
    problemFullBodyLimitBytes: settings.problemFullBodyLimitBytes
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
  allowDbServiceLocalAuditLogWriteForTest = false
  auditLogRedisConsumerStopping = true
  auditLogRedisConsumerStarted = false
  auditLogRedisConsumerPromises = []
  void auditLogRedisStreamQueueInstance?.closeConsumer().catch(() => undefined)
  for (const queue of auditLogRedisConsumerQueueInstances) {
    void queue?.closeConsumer().catch(() => undefined)
  }
  auditLogRedisStreamQueueInstance = undefined
  auditLogRedisConsumerQueueInstances = []
  void stopAuditLogTransportWorker().catch(() => undefined)
}

export function setDbServiceAuditLogLocalWriteAllowedForTest(value: boolean): void {
  allowDbServiceLocalAuditLogWriteForTest = value
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

function recordAuditLogDispatchFailure(input: AuditLogInput): void {
  recordDrop({
    input,
    bytes: estimateAuditLogBytes(input),
    success: input.success
  }, 'overflow')
}

async function enqueueAuditLogToRedisStream(input: AuditLogInput, encodedPayload?: string): Promise<void> {
  try {
    const queue = auditLogRedisStreamQueue()
    if (encodedPayload === undefined) {
      await queue.enqueue(input)
    } else {
      await queue.enqueueEncoded(encodedPayload)
    }
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'audit_log_redis_stream_enqueue_failed',
      auditLogId: input.id,
      traceId: input.traceId,
      auditOutcome: input.auditOutcome,
      success: input.success
    }), '审计日志写入 Redis Stream 失败，高性能模式禁止回退 IPC 或本地队列')
    throw error
  }
}

async function runAuditLogRedisStreamConsumer(consumerIndex: number): Promise<void> {
  const queue = auditLogRedisStreamQueue(consumerIndex)
  while (!auditLogRedisConsumerStopping) {
    try {
      const claimed = await queue.claimPending()
      const messages = claimed.length > 0 ? claimed : await queue.readNew()
      if (messages.length === 0) {
        continue
      }
      await flushAuditLogRedisStreamMessages(queue, messages)
    } catch (error) {
      if (auditLogRedisConsumerStopping) {
        break
      }
      lastFlushError = error instanceof Error ? error.message : String(error)
      logger.error(errorLogFields(error, {
        event: 'audit_log_redis_stream_consume_failed',
        consumerIndex
      }), 'Redis Stream 审计日志消费失败，稍后重试')
      await delay(auditLogRedisConsumerErrorRetryMs)
    }
  }
}

async function flushAuditLogRedisStreamMessages(
  queue: RedisStreamQueue<AuditLogInput>,
  messages: Array<RedisStreamMessage<AuditLogInput>>
): Promise<void> {
  if (messages.length === 0) return
  const inputs = messages.map((message) => normalizeAuditLogInput(message.payload))
  try {
    await createAuditLogsBatchAsync(inputs)
    lastFlushSuccessAt = nowIso()
    lastFlushError = undefined
    await queue.ack(messages.map((message) => message.id))
  } catch (error) {
    lastFlushError = error instanceof Error ? error.message : String(error)
    logger.error(errorLogFields(error, {
      event: 'audit_log_redis_stream_flush_failed',
      batchSize: messages.length,
      firstMessageId: messages[0]?.id
    }), 'Redis Stream 审计日志落库失败，消息保持 pending 等待重投')
  }
}

function auditLogRedisStreamQueue(consumerIndex?: number): RedisStreamQueue<AuditLogInput> {
  if (typeof consumerIndex === 'number') {
    if (!auditLogRedisConsumerQueueInstances[consumerIndex]) {
      auditLogRedisConsumerQueueInstances[consumerIndex] = new RedisStreamQueue<AuditLogInput>({
        streamKey: auditLogRedisStreamKey,
        groupName: auditLogRedisStreamGroup,
        consumerName: `${auditLogRedisStreamGroup}:${process.pid}:${consumerIndex}`,
        readCount: runtimeConfig.databaseDriver === 'postgres' ? auditLogPostgresFlushBatchSize : undefined,
        encode: encodeAuditLogStreamPayload,
        decode: decodeAuditLogStreamPayload
      })
    }
    return auditLogRedisConsumerQueueInstances[consumerIndex]
  }
  if (!auditLogRedisStreamQueueInstance) {
    auditLogRedisStreamQueueInstance = new RedisStreamQueue<AuditLogInput>({
      streamKey: auditLogRedisStreamKey,
      groupName: auditLogRedisStreamGroup,
      readCount: runtimeConfig.databaseDriver === 'postgres' ? auditLogPostgresFlushBatchSize : undefined,
      encode: encodeAuditLogStreamPayload,
      decode: decodeAuditLogStreamPayload
    })
  }
  return auditLogRedisStreamQueueInstance
}

export async function getAuditLogRedisStreamRuntime(): Promise<RedisStreamQueueRuntime | undefined> {
  if (!shouldUseRedisStreamAuditLogQueue()) return undefined
  return await auditLogRedisStreamQueue().inspectRuntime()
}

function auditLogRedisConsumerConcurrency(): number {
  return runtimeConfig.databaseDriver === 'postgres' ? auditLogPostgresRedisConsumerConcurrency : 1
}

function sendAuditLogFromDbServiceToServer(input: AuditLogInput): boolean {
  if (typeof process.send !== 'function' || process.connected === false) {
    return false
  }
  try {
    process.send({
      type: 'background_worker_audit_logs',
      items: [input]
    }, (error) => {
      if (error) {
        recordAuditLogDispatchFailure(input)
      }
    })
    return true
  } catch {
    recordAuditLogDispatchFailure(input)
    return true
  }
}

export function isAuditLogInput(value: unknown): value is AuditLogInput {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return false
  }
  const record = value as Record<string, unknown>
  return typeof record.traceId === 'string'
    && typeof record.method === 'string'
    && typeof record.path === 'string'
    && typeof record.auditOutcome === 'string'
    && typeof record.success === 'boolean'
    && typeof record.sampleBucket === 'number'
    && typeof record.sampleReason === 'string'
    && Array.isArray(record.attempts)
    && Array.isArray(record.payloads)
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
    void flushAuditLogQueueAsync({ drain: true, maxBatches: auditLogScheduledFlushMaxBatches }).catch((error) => {
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

function yieldBetweenAuditLogFlushBatches(): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, auditLogFlushBatchYieldMs).unref()
  })
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

function shouldUseRedisStreamAuditLogQueue(): boolean {
  return runtimeConfig.queueDriver === 'redis_stream'
}

function shouldEnqueueAuditLogToRedisStream(): boolean {
  return shouldUseRedisStreamAuditLogQueue()
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms)
    timer.unref()
  })
}

function effectiveAuditLogFlushBatchSize(settingsBatchSize: number): number {
  const normalized = Math.max(1, Math.trunc(settingsBatchSize))
  return runtimeConfig.databaseDriver === 'postgres'
    ? Math.min(normalized, auditLogPostgresFlushBatchSize)
    : normalized
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
  if (shouldUseRedisStreamAuditLogQueue()) {
    throw new Error(`Redis Stream queue driver 下禁止写入审计日志本地队列：${operation}`)
  }
  if (!isLocalAuditLogWriteAllowed()) {
    throw new Error(`${runtimeConfig.processRole}/${runtimeConfig.workerRole} 角色禁止直接写入审计日志：${operation} 必须投递 ingest-worker`)
  }
}

function isLocalAuditLogWriteAllowed(): boolean {
  return isAuditLogIngestWorker() || isDbServiceLocalAuditLogWriteAllowedForTest()
}

function isAuditLogIngestWorker(): boolean {
  return runtimeConfig.processRole === 'worker' && runtimeConfig.workerRole === 'ingest-worker'
}

function isDbServiceLocalAuditLogWriteAllowedForTest(): boolean {
  return allowDbServiceLocalAuditLogWriteForTest && runtimeConfig.processRole === 'db-service'
}

function shouldDispatchAuditLogToIngestWorker(): boolean {
  return runtimeConfig.processRole === 'server'
    || (runtimeConfig.processRole === 'worker' && runtimeConfig.workerRole !== 'ingest-worker')
}
