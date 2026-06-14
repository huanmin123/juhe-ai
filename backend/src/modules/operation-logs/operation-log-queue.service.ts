import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { estimateJsonLikeBytes } from '../../shared/queue-size.js'
import { fixedRetryPolicy, retryDelayMs } from '../../shared/retry-policy.js'
import { newId, nowIso } from '../../storage/database.js'
import { createOperationLogsBatch, type OperationLogInput } from '../../storage/repositories.js'
import { sendOperationLogsToWorker } from '../background/background-ipc.js'

const operationLogFlushIntervalMs = 100
const operationLogRetryPolicy = fixedRetryPolicy('operation_log_queue_flush', 1000)
const operationLogBatchSize = 200
const operationLogShutdownFlushMaxBatches = 1
const operationLogQueueMaxItems = 5_000
const operationLogQueueMaxBytes = 32 * 1024 * 1024

interface QueuedOperationLog {
  input: OperationLogInput
  bytes: number
}

let pendingOperationLogs: QueuedOperationLog[] = []
let pendingOperationLogBytes = 0
let flushTimer: NodeJS.Timeout | undefined
let flushing = false
let retainedOverflowWarningCount = 0
let flushFailureCount = 0
let droppedDispatchCount = 0
let droppedOverflowCount = 0
let droppedOversizeCount = 0
let shutdownHooksInstalled = false

interface OperationLogFlushOptions {
  drain?: boolean
  retryOnFailure?: boolean
  maxBatches?: number
}

export function enqueueOperationLog(input: OperationLogInput): void {
  const queuedInput = normalizeOperationLogInput(input)
  if (shouldDispatchOperationLogToIngestWorker()) {
    if (!sendOperationLogsToWorker([queuedInput])) {
      recordOperationLogDispatchFailure(new Error('ingest-worker IPC 不可用'), queuedInput)
    }
    return
  }

  if (runtimeConfig.processRole === 'db-service') {
    if (process.send && process.connected !== false) {
      try {
        process.send({
          type: 'background_worker_operation_logs',
          items: [queuedInput]
        }, (error) => {
          if (error) {
            recordOperationLogDispatchFailure(error, queuedInput)
          }
        })
      } catch (error) {
        recordOperationLogDispatchFailure(error, queuedInput)
      }
      return
    }
    recordOperationLogDispatchFailure(new Error('DB service 无父进程 IPC'), queuedInput)
    return
  }

  enqueueOperationLogLocal(queuedInput)
}

export function enqueueOperationLogsLocal(inputs: OperationLogInput[]): void {
  assertLocalOperationLogWriteAllowed('enqueueOperationLogsLocal')
  for (const input of inputs) {
    enqueueOperationLogLocal(normalizeOperationLogInput(input))
  }
}

export function flushOperationLogQueue(options: OperationLogFlushOptions = {}): void {
  if (!isOperationLogIngestWorker()) {
    return
  }
  if (flushing || pendingOperationLogs.length === 0) {
    return
  }

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
    do {
      const batch = pendingOperationLogs.slice(0, operationLogBatchSize)
      if (batch.length === 0) {
        break
      }
      flushedBatches += 1
      const batchBytes = sumQueuedOperationLogBytes(batch)

      try {
        createOperationLogsBatch(batch.map((item) => item.input))
        pendingOperationLogs.splice(0, batch.length)
        pendingOperationLogBytes = Math.max(0, pendingOperationLogBytes - batchBytes)
      } catch (error) {
        failed = true
        flushFailureCount += 1
        logger.error(errorLogFields(error, {
          event: 'operation_log_queue_flush_failed',
          batchSize: batch.length,
          pendingCount: pendingOperationLogs.length,
          pendingBytes: pendingOperationLogBytes,
          flushFailureCount
        }), '操作日志队列写入失败，已保留记录等待重试')
        shouldRetry = options.retryOnFailure !== false
        break
      }
    } while (options.drain && pendingOperationLogs.length > 0 && flushedBatches < maxBatches)
  } finally {
    flushing = false
  }

  if (pendingOperationLogs.length > 0 && (!failed || shouldRetry)) {
    scheduleOperationLogFlush(shouldRetry ? retryDelayMs(operationLogRetryPolicy) : 0)
  }
}

export function flushAllOperationLogQueue(): void {
  flushOperationLogQueue({ drain: true, retryOnFailure: false })
}

export function flushOperationLogQueueForShutdown(): void {
  flushOperationLogQueue({ drain: true, retryOnFailure: false, maxBatches: operationLogShutdownFlushMaxBatches })
}

export function getOperationLogQueueRuntime(): {
  queueLength: number
  queueBytes: number
  droppedCount: number
  retainedOverflowWarningCount: number
  droppedOverflowCount: number
  droppedOversizeCount: number
  flushFailureCount: number
} {
  return {
    queueLength: pendingOperationLogs.length,
    queueBytes: pendingOperationLogBytes,
    droppedCount: droppedDispatchCount + droppedOverflowCount + droppedOversizeCount,
    retainedOverflowWarningCount,
    droppedOverflowCount,
    droppedOversizeCount,
    flushFailureCount
  }
}

export function installOperationLogQueueShutdownHooks(): void {
  if (shutdownHooksInstalled) {
    return
  }
  shutdownHooksInstalled = true

  process.once('beforeExit', flushOperationLogQueueForShutdown)
}

function enqueueOperationLogLocal(input: OperationLogInput): void {
  assertLocalOperationLogWriteAllowed('enqueueOperationLogLocal')
  const queued = {
    input,
    bytes: estimateOperationLogBytes(input)
  }
  if (queued.bytes > operationLogQueueMaxBytes) {
    recordOperationLogLocalDrop(queued, 'oversize')
    return
  }
  if (pendingOperationLogs.length >= operationLogQueueMaxItems || pendingOperationLogBytes + queued.bytes > operationLogQueueMaxBytes) {
    recordOperationLogLocalDrop(queued, 'overflow')
    return
  }
  pendingOperationLogs.push(queued)
  pendingOperationLogBytes += queued.bytes
  scheduleOperationLogFlush(pendingOperationLogs.length >= operationLogBatchSize ? 0 : operationLogFlushIntervalMs)
}

function scheduleOperationLogFlush(delayMs: number): void {
  if (!isOperationLogIngestWorker()) {
    return
  }
  if (flushTimer || flushing) {
    return
  }
  flushTimer = setTimeout(() => {
    flushTimer = undefined
    flushOperationLogQueue()
  }, delayMs)
  flushTimer.unref()
}

function recordOperationLogDispatchFailure(error: unknown, input?: OperationLogInput): void {
  droppedDispatchCount += 1
  logger.warn(errorLogFields(error, {
    event: 'operation_log_queue_dispatch_failed',
    operationLogId: input?.id,
    actorSystemAccountId: input?.actorSystemAccountId,
    module: input?.module,
    action: input?.action,
    operationKey: input?.operationKey,
    resourceType: input?.resourceType,
    droppedDispatchCount
  }), '操作日志投递后台 worker 失败，已跳过投递')
}

export function clearOperationLogQueueForTest(): void {
  if (flushTimer) {
    clearTimeout(flushTimer)
    flushTimer = undefined
  }
  pendingOperationLogs = []
  pendingOperationLogBytes = 0
  flushing = false
  retainedOverflowWarningCount = 0
  flushFailureCount = 0
  droppedDispatchCount = 0
  droppedOverflowCount = 0
  droppedOversizeCount = 0
  shutdownHooksInstalled = false
}

function normalizeOperationLogInput(input: OperationLogInput): OperationLogInput {
  return {
    ...input,
    id: input.id ?? newId('oplog'),
    createdAt: input.createdAt ?? nowIso()
  }
}

export function isOperationLogInput(value: unknown): value is OperationLogInput {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return false
  }
  const record = value as Record<string, unknown>
  return typeof record.actorSystemAccountId === 'string'
    && typeof record.actorRole === 'string'
    && typeof record.module === 'string'
    && typeof record.action === 'string'
    && typeof record.operationKey === 'string'
    && typeof record.resourceType === 'string'
    && typeof record.summary === 'string'
}

function normalizeMaxBatches(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : Number.POSITIVE_INFINITY
}

function estimateOperationLogBytes(input: OperationLogInput): number {
  return estimateJsonLikeBytes(input) + 256
}

function sumQueuedOperationLogBytes(items: QueuedOperationLog[]): number {
  return items.reduce((sum, item) => sum + item.bytes, 0)
}

function recordOperationLogLocalDrop(item: QueuedOperationLog, reason: 'overflow' | 'oversize'): void {
  if (reason === 'overflow') {
    droppedOverflowCount += 1
    retainedOverflowWarningCount += 1
  } else {
    droppedOversizeCount += 1
  }
  const droppedCount = droppedDispatchCount + droppedOverflowCount + droppedOversizeCount
  if (droppedCount > 10 && droppedCount % 100 !== 0) {
    return
  }
  logger.warn({
    event: 'operation_log_queue_dropped',
    reason,
    operationLogId: item.input.id,
    actorSystemAccountId: item.input.actorSystemAccountId,
    module: item.input.module,
    action: item.input.action,
    operationKey: item.input.operationKey,
    resourceType: item.input.resourceType,
    bytes: item.bytes,
    pendingCount: pendingOperationLogs.length,
    pendingBytes: pendingOperationLogBytes,
    droppedOverflowCount,
    droppedOversizeCount
  }, '操作日志队列达到保护上限，已丢弃新记录')
}

function assertLocalOperationLogWriteAllowed(operation: string): void {
  if (!isOperationLogIngestWorker()) {
    throw new Error(`${runtimeConfig.processRole}/${runtimeConfig.workerRole} 角色禁止直接写入操作日志：${operation} 必须投递 ingest-worker`)
  }
}

function isOperationLogIngestWorker(): boolean {
  return runtimeConfig.processRole === 'worker' && runtimeConfig.workerRole === 'ingest-worker'
}

function shouldDispatchOperationLogToIngestWorker(): boolean {
  return runtimeConfig.processRole === 'server'
    || (runtimeConfig.processRole === 'worker' && runtimeConfig.workerRole !== 'ingest-worker')
}
