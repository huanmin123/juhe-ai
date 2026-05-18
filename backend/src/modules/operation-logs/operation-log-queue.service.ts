import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { newId, nowIso } from '../../storage/database.js'
import { createOperationLogsBatch, type OperationLogInput } from '../../storage/repositories.js'
import { sendOperationLogsToWorker } from '../background/background-ipc.js'

const operationLogFlushIntervalMs = 100
const operationLogRetryDelayMs = 1000
const operationLogBatchSize = 200
const operationLogMaxPending = 10000

let pendingOperationLogs: OperationLogInput[] = []
let flushTimer: NodeJS.Timeout | undefined
let flushing = false
let retainedOverflowWarningCount = 0
let flushFailureCount = 0
let droppedDispatchCount = 0
let shutdownHooksInstalled = false

interface OperationLogFlushOptions {
  drain?: boolean
  retryOnFailure?: boolean
  maxBatches?: number
}

export function enqueueOperationLog(input: OperationLogInput): void {
  const queuedInput = normalizeOperationLogInput(input)
  if (runtimeConfig.processRole === 'server') {
    sendOperationLogsToWorker([queuedInput])
    return
  }

  if (runtimeConfig.processRole === 'db-service') {
    if (process.send) {
      try {
        process.send({
          type: 'background_worker_operation_logs',
          items: [queuedInput]
        }, (error) => {
          if (error) {
            recordOperationLogDispatchFailure(error)
          }
        })
      } catch (error) {
        recordOperationLogDispatchFailure(error)
      }
      return
    }
    recordOperationLogDispatchFailure(new Error('DB service 无父进程 IPC'))
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
  if (runtimeConfig.processRole !== 'worker') {
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
      const batch = pendingOperationLogs.splice(0, operationLogBatchSize)
      if (batch.length === 0) {
        break
      }
      flushedBatches += 1

      try {
        createOperationLogsBatch(batch)
      } catch (error) {
        failed = true
        pendingOperationLogs = [...batch, ...pendingOperationLogs]
        flushFailureCount += 1
        logger.error(errorLogFields(error, {
          event: 'operation_log_queue_flush_failed',
          batchSize: batch.length,
          pendingCount: pendingOperationLogs.length,
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
    scheduleOperationLogFlush(shouldRetry ? operationLogRetryDelayMs : 0)
  }
}

export function flushAllOperationLogQueue(): void {
  flushOperationLogQueue({ drain: true, retryOnFailure: false })
}

export function getOperationLogQueueRuntime(): {
  queueLength: number
  droppedCount: number
  retainedOverflowWarningCount: number
  flushFailureCount: number
} {
  return {
    queueLength: pendingOperationLogs.length,
    droppedCount: droppedDispatchCount,
    retainedOverflowWarningCount,
    flushFailureCount
  }
}

export function installOperationLogQueueShutdownHooks(): void {
  if (shutdownHooksInstalled) {
    return
  }
  shutdownHooksInstalled = true

  process.once('beforeExit', flushAllOperationLogQueue)
  process.once('exit', flushAllOperationLogQueue)

  process.once('SIGINT', () => exitAfterOperationLogFlush(0))
  process.once('SIGTERM', () => exitAfterOperationLogFlush(0))
}

function enqueueOperationLogLocal(input: OperationLogInput): void {
  assertLocalOperationLogWriteAllowed('enqueueOperationLogLocal')
  pendingOperationLogs.push(input)
  if (pendingOperationLogs.length > operationLogMaxPending) {
    const overflowCount = pendingOperationLogs.length - operationLogMaxPending
    retainedOverflowWarningCount += 1
    logger.warn({
      event: 'operation_log_queue_soft_limit_exceeded',
      overflowCount,
      retainedOverflowWarningCount,
      pendingCount: pendingOperationLogs.length
    }, '操作日志队列超过软上限，已保留待写入记录并触发立即落库')
    flushOperationLogQueue({ drain: true, maxBatches: 5 })
  }
  scheduleOperationLogFlush(pendingOperationLogs.length >= operationLogBatchSize ? 0 : operationLogFlushIntervalMs)
}

function exitAfterOperationLogFlush(exitCode: number): never {
  flushAllOperationLogQueue()
  process.exit(exitCode)
}

function scheduleOperationLogFlush(delayMs: number): void {
  if (runtimeConfig.processRole !== 'worker') {
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

function recordOperationLogDispatchFailure(error: unknown): void {
  droppedDispatchCount += 1
  logger.warn(errorLogFields(error, {
    event: 'operation_log_queue_dispatch_failed',
    droppedDispatchCount
  }), 'DB service 操作日志投递失败，已跳过投递')
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

function assertLocalOperationLogWriteAllowed(operation: string): void {
  if (runtimeConfig.processRole !== 'worker') {
    throw new Error(`${runtimeConfig.processRole} 角色禁止直接同步写入操作日志：${operation} 必须投递 background worker`)
  }
}
