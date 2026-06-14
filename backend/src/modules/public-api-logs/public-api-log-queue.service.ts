import { errorLogFields, logger } from '../../shared/logger.js'
import { runtimeConfig } from '../../config/runtime.js'
import { createPublicApiLogsBatch, type PublicApiLogInput } from '../../storage/public-api-logs.repository.js'
import { sendPublicApiLogsToWorker } from '../background/background-ipc.js'

const publicApiLogQueueMaxSize = 5000
const publicApiLogFlushBatchSize = 50
const publicApiLogDropWarnInterval = 100
const publicApiLogRetryDelayMs = 1000

const publicApiLogQueue: PublicApiLogInput[] = []
let flushScheduled = false
let flushRetryTimer: NodeJS.Timeout | undefined
let flushing = false
let droppedPublicApiLogCount = 0
let publicApiLogFlushFailureCount = 0

export function enqueuePublicApiLog(input: PublicApiLogInput): boolean {
  if (runtimeConfig.processRole === 'server' || runtimeConfig.processRole === 'db-service') {
    return sendPublicApiLogsToWorker([input])
  }
  return enqueuePublicApiLogsLocal([input])
}

export function enqueuePublicApiLogsLocal(inputs: PublicApiLogInput[]): boolean {
  assertLocalPublicApiLogWriteAllowed('enqueuePublicApiLogsLocal')
  let queued = true
  for (const input of inputs) {
    queued = enqueuePublicApiLogLocal(input) && queued
  }
  return queued
}

function enqueuePublicApiLogLocal(input: PublicApiLogInput): boolean {
  if (publicApiLogQueue.length >= publicApiLogQueueMaxSize) {
    droppedPublicApiLogCount += 1
    if (droppedPublicApiLogCount === 1 || droppedPublicApiLogCount % publicApiLogDropWarnInterval === 0) {
      logger.warn({
        event: 'public_api_log_queue_overflow',
        queueSize: publicApiLogQueue.length,
        droppedPublicApiLogCount,
        traceId: input.traceId,
        path: input.path
      }, '公开接口日志队列已满，丢弃日志记录')
    }
    return false
  }

  publicApiLogQueue.push(input)
  schedulePublicApiLogFlush(0)
  return true
}

export function getPublicApiLogQueueRuntime(): {
  queueLength: number
  droppedCount: number
  flushFailureCount: number
} {
  return {
    queueLength: publicApiLogQueue.length,
    droppedCount: droppedPublicApiLogCount,
    flushFailureCount: publicApiLogFlushFailureCount
  }
}

export function installPublicApiLogQueueShutdownHooks(): void {
  process.once('beforeExit', () => {
    flushPublicApiLogQueueForTest()
  })
}

export function flushPublicApiLogQueueForTest(): void {
  while (publicApiLogQueue.length > 0) {
    if (!flushPublicApiLogQueueBatch()) {
      break
    }
  }
  flushScheduled = false
  if (flushRetryTimer) {
    clearTimeout(flushRetryTimer)
    flushRetryTimer = undefined
  }
}

function schedulePublicApiLogFlush(delayMs: number): void {
  if (flushScheduled) {
    return
  }
  flushScheduled = true
  if (delayMs <= 0) {
    setImmediate(flushPublicApiLogQueue)
    return
  }
  flushRetryTimer = setTimeout(() => {
    flushRetryTimer = undefined
    flushPublicApiLogQueue()
  }, delayMs)
  flushRetryTimer.unref()
}

function flushPublicApiLogQueue(): void {
  flushScheduled = false
  if (flushing) {
    return
  }
  flushing = true
  let success = false
  try {
    success = flushPublicApiLogQueueBatch()
  } finally {
    flushing = false
  }
  if (publicApiLogQueue.length > 0) {
    schedulePublicApiLogFlush(success ? 0 : publicApiLogRetryDelayMs)
  }
}

function flushPublicApiLogQueueBatch(): boolean {
  const batch = publicApiLogQueue.slice(0, publicApiLogFlushBatchSize)
  if (batch.length === 0) {
    return true
  }
  try {
    createPublicApiLogsBatch(batch)
    publicApiLogQueue.splice(0, batch.length)
    return true
  } catch (error) {
    publicApiLogFlushFailureCount += 1
    const first = batch[0]
    logger.warn(errorLogFields(error, {
      event: 'public_api_log_batch_write_failed',
      batchSize: batch.length,
      pendingCount: publicApiLogQueue.length,
      flushFailureCount: publicApiLogFlushFailureCount,
      method: first?.method,
      path: first?.path,
      statusCode: first?.statusCode,
      traceId: first?.traceId
    }), '公开接口日志批量写入失败，已保留批次等待重试')
    return false
  }
}

function assertLocalPublicApiLogWriteAllowed(operation: string): void {
  if (runtimeConfig.processRole !== 'worker' || runtimeConfig.workerRole !== 'ingest-worker') {
    throw new Error(`${operation} 只能在 ingest-worker 本地执行`)
  }
}
