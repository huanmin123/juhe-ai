import { randomUUID } from 'node:crypto'

import { errorLogFields, logger } from '../../shared/logger.js'
import { estimateJsonLikeBytes } from '../../shared/queue-size.js'
import { RedisStreamQueue, type RedisStreamMessage, type RedisStreamQueueRuntime } from '../../shared/redis-stream-queue.js'
import { redisStreamQueueContracts } from '../../shared/redis-stream-drain.js'
import { runRedisEnqueueWithBoundedRetry } from '../../shared/redis-enqueue-retry.js'
import { runtimeConfig } from '../../config/runtime.js'
import { createPublicApiLogsBatch, createPublicApiLogsBatchAsync, type PublicApiLogInput } from '../../storage/public-api-logs.repository.js'
import { sendPublicApiLogsToWorker } from '../background/background-ipc.js'

const publicApiLogQueueMaxSize = 5000
const publicApiLogQueueMaxBytes = 32 * 1024 * 1024
const publicApiLogEstimateMaxBytes = publicApiLogQueueMaxBytes + 1
const publicApiLogFlushBatchSize = 50
const publicApiLogShutdownFlushMaxBatches = 100
const publicApiLogDropWarnInterval = 100
const publicApiLogRetryDelayMs = 1000
const publicApiLogRedisStreamKey = redisStreamQueueContracts.publicApiLogs.streamKey
const publicApiLogRedisStreamGroup = redisStreamQueueContracts.publicApiLogs.groupName
const publicApiLogRedisConsumerErrorRetryMs = 1000
const publicApiLogRedisStopWaitMs = 2000

interface QueuedPublicApiLog {
  input: PublicApiLogInput
  bytes: number
}

const publicApiLogQueue: QueuedPublicApiLog[] = []
let publicApiLogQueueBytes = 0
let flushScheduled = false
let flushRetryTimer: NodeJS.Timeout | undefined
let flushing = false
let droppedPublicApiLogCount = 0
let publicApiLogFlushFailureCount = 0
let publicApiLogRedisStreamQueueInstance: RedisStreamQueue<PublicApiLogInput> | undefined
let publicApiLogRedisConsumerStarted = false
let publicApiLogRedisConsumerStopping = false
let publicApiLogRedisConsumerPromise: Promise<void> | undefined

export function enqueuePublicApiLog(input: PublicApiLogInput): boolean {
  const stableInput = ensurePublicApiLogQueueId(input)
  if (shouldEnqueuePublicApiLogToRedisStream()) {
    void enqueuePublicApiLogToRedisStream(stableInput).catch((error) => {
      recordPublicApiLogDispatchFailure(error, stableInput)
    })
    return true
  }
  if (runtimeConfig.processRole === 'server') {
    return sendPublicApiLogsToWorker([stableInput])
  }
  if (runtimeConfig.processRole === 'db-service') {
    return sendPublicApiLogToParent(stableInput)
  }
  return enqueuePublicApiLogsLocal([stableInput])
}

function ensurePublicApiLogQueueId(input: PublicApiLogInput): PublicApiLogInput {
  if (input.id !== undefined) return input
  return {
    ...input,
    id: `publog_${Date.now()}_${randomUUID()}`
  }
}

export function startPublicApiLogRedisStreamConsumer(): void {
  if (!shouldUseRedisStreamPublicApiLogQueue() || !isPublicApiLogIngestWorker() || publicApiLogRedisConsumerStarted) {
    return
  }
  publicApiLogRedisConsumerStarted = true
  publicApiLogRedisConsumerStopping = false
  publicApiLogRedisConsumerPromise = runPublicApiLogRedisStreamConsumer().catch((error) => {
    logger.error(errorLogFields(error, {
      event: 'public_api_log_redis_stream_consumer_stopped'
    }), 'Redis Stream 公开接口日志消费循环异常退出')
  }).finally(() => {
    publicApiLogRedisConsumerStarted = false
    publicApiLogRedisConsumerPromise = undefined
  })
}

export async function stopPublicApiLogRedisStreamConsumer(): Promise<void> {
  publicApiLogRedisConsumerStopping = true
  const queue = publicApiLogRedisStreamQueueInstance
  if (queue) {
    await queue.closeConsumer().catch(() => undefined)
  }
  if (publicApiLogRedisConsumerPromise) {
    await Promise.race([
      publicApiLogRedisConsumerPromise.catch(() => undefined),
      delay(publicApiLogRedisStopWaitMs)
    ])
  }
}

export function enqueuePublicApiLogsLocal(inputs: PublicApiLogInput[]): boolean {
  assertLocalPublicApiLogWriteAllowed('enqueuePublicApiLogsLocal')
  let queued = true
  for (const input of inputs) {
    queued = enqueuePublicApiLogLocal(ensurePublicApiLogQueueId(input)) && queued
  }
  return queued
}

function enqueuePublicApiLogLocal(input: PublicApiLogInput): boolean {
  const queued = {
    input,
    bytes: estimatePublicApiLogBytes(input)
  }
  if (queued.bytes > publicApiLogQueueMaxBytes || publicApiLogQueue.length >= publicApiLogQueueMaxSize || publicApiLogQueueBytes + queued.bytes > publicApiLogQueueMaxBytes) {
    droppedPublicApiLogCount += 1
    if (droppedPublicApiLogCount === 1 || droppedPublicApiLogCount % publicApiLogDropWarnInterval === 0) {
      logger.warn({
        event: 'public_api_log_queue_overflow',
        queueSize: publicApiLogQueue.length,
        queueBytes: publicApiLogQueueBytes,
        itemBytes: queued.bytes,
        droppedPublicApiLogCount,
        traceId: input.traceId,
        path: input.path
      }, '公开接口日志队列已满，丢弃日志记录')
    }
    return false
  }

  publicApiLogQueue.push(queued)
  publicApiLogQueueBytes += queued.bytes
  schedulePublicApiLogFlush(0)
  return true
}

export function getPublicApiLogQueueRuntime(): {
  queueLength: number
  queueBytes: number
  droppedCount: number
  flushFailureCount: number
} {
  return {
    queueLength: publicApiLogQueue.length,
    queueBytes: publicApiLogQueueBytes,
    droppedCount: droppedPublicApiLogCount,
    flushFailureCount: publicApiLogFlushFailureCount
  }
}

export function installPublicApiLogQueueShutdownHooks(): void {
  process.once('beforeExit', () => {
    flushPublicApiLogQueueForShutdown()
  })
}

export function flushPublicApiLogQueueForTest(): void {
  flushPublicApiLogQueueBatches({ drain: true })
  clearPublicApiLogFlushTimers()
}

export function flushPublicApiLogQueueForShutdown(): void {
  flushPublicApiLogQueueBatches({ drain: true, maxBatches: publicApiLogShutdownFlushMaxBatches })
  clearPublicApiLogFlushTimers()
}

export function clearPublicApiLogQueueForTest(): void {
  publicApiLogQueue.splice(0, publicApiLogQueue.length)
  publicApiLogQueueBytes = 0
  droppedPublicApiLogCount = 0
  publicApiLogFlushFailureCount = 0
  flushing = false
  publicApiLogRedisConsumerStopping = true
  publicApiLogRedisConsumerStarted = false
  publicApiLogRedisConsumerPromise = undefined
  void publicApiLogRedisStreamQueueInstance?.closeConsumer().catch(() => undefined)
  publicApiLogRedisStreamQueueInstance = undefined
  clearPublicApiLogFlushTimers()
}

export function isPublicApiLogInput(value: unknown): value is PublicApiLogInput {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return false
  }
  const record = value as Partial<PublicApiLogInput>
  return typeof record.method === 'string'
    && typeof record.path === 'string'
    && typeof record.startedAt === 'string'
    && typeof record.endedAt === 'string'
}

function clearPublicApiLogFlushTimers(): void {
  if (flushRetryTimer) {
    clearTimeout(flushRetryTimer)
    flushRetryTimer = undefined
  }
  flushScheduled = false
}

function flushPublicApiLogQueueBatches(options: { drain?: boolean; maxBatches?: number } = {}): void {
  if (runtimeConfig.databaseDriver === 'postgres') {
    void flushPublicApiLogQueueBatchesAsync(options)
    return
  }
  const maxBatches = typeof options.maxBatches === 'number' && Number.isFinite(options.maxBatches)
    ? Math.max(1, Math.trunc(options.maxBatches))
    : Number.POSITIVE_INFINITY
  let flushedBatches = 0
  while (publicApiLogQueue.length > 0 && flushedBatches < maxBatches) {
    flushedBatches += 1
    if (!flushPublicApiLogQueueBatch()) {
      break
    }
    if (!options.drain) {
      break
    }
  }
}

async function flushPublicApiLogQueueBatchesAsync(options: { drain?: boolean; maxBatches?: number } = {}): Promise<void> {
  const maxBatches = typeof options.maxBatches === 'number' && Number.isFinite(options.maxBatches)
    ? Math.max(1, Math.trunc(options.maxBatches))
    : Number.POSITIVE_INFINITY
  let flushedBatches = 0
  while (publicApiLogQueue.length > 0 && flushedBatches < maxBatches) {
    flushedBatches += 1
    if (!(await flushPublicApiLogQueueBatchAsync())) {
      break
    }
    if (!options.drain) {
      break
    }
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
  if (runtimeConfig.databaseDriver === 'postgres') {
    void flushPublicApiLogQueueAsync()
    return
  }
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

async function flushPublicApiLogQueueAsync(): Promise<void> {
  flushScheduled = false
  if (flushing) {
    return
  }
  flushing = true
  let success = false
  try {
    success = await flushPublicApiLogQueueBatchAsync()
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
  const batchBytes = sumQueuedPublicApiLogBytes(batch)
  try {
    createPublicApiLogsBatch(batch.map((item) => item.input))
    publicApiLogQueue.splice(0, batch.length)
    publicApiLogQueueBytes = Math.max(0, publicApiLogQueueBytes - batchBytes)
    return true
  } catch (error) {
    publicApiLogFlushFailureCount += 1
    const first = batch[0]?.input
    logger.warn(errorLogFields(error, {
      event: 'public_api_log_batch_write_failed',
      batchSize: batch.length,
      batchBytes,
      pendingCount: publicApiLogQueue.length,
      pendingBytes: publicApiLogQueueBytes,
      flushFailureCount: publicApiLogFlushFailureCount,
      method: first?.method,
      path: first?.path,
      statusCode: first?.statusCode,
      traceId: first?.traceId
    }), '公开接口日志批量写入失败，已保留批次等待重试')
    return false
  }
}

async function enqueuePublicApiLogToRedisStream(input: PublicApiLogInput): Promise<void> {
  try {
    await runRedisEnqueueWithBoundedRetry(() => publicApiLogRedisStreamQueue().enqueue(input))
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'public_api_log_redis_stream_enqueue_failed',
      traceId: input.traceId,
      method: input.method,
      path: input.path,
      statusCode: input.statusCode
    }), '公开接口日志写入 Redis Stream 失败，高性能模式禁止回退 IPC 或本地队列')
    throw error
  }
}

async function runPublicApiLogRedisStreamConsumer(): Promise<void> {
  const queue = publicApiLogRedisStreamQueue()
  while (!publicApiLogRedisConsumerStopping) {
    try {
      const claimed = await queue.claimPending()
      const messages = claimed.length > 0 ? claimed : await queue.readNew()
      if (messages.length === 0) {
        continue
      }
      await flushPublicApiLogRedisStreamMessages(messages)
    } catch (error) {
      if (publicApiLogRedisConsumerStopping) {
        break
      }
      publicApiLogFlushFailureCount += 1
      logger.error(errorLogFields(error, {
        event: 'public_api_log_redis_stream_consume_failed',
        flushFailureCount: publicApiLogFlushFailureCount
      }), 'Redis Stream 公开接口日志消费失败，稍后重试')
      await delay(publicApiLogRedisConsumerErrorRetryMs)
    }
  }
}

async function flushPublicApiLogRedisStreamMessages(messages: Array<RedisStreamMessage<PublicApiLogInput>>): Promise<void> {
  if (messages.length === 0) return
  const queue = publicApiLogRedisStreamQueue()
  try {
    await createPublicApiLogsBatchAsync(messages.map((message) => message.payload))
    publicApiLogFlushFailureCount = 0
    await queue.ack(messages.map((message) => message.id))
  } catch (error) {
    publicApiLogFlushFailureCount += 1
    logger.error(errorLogFields(error, {
      event: 'public_api_log_redis_stream_flush_failed',
      batchSize: messages.length,
      firstMessageId: messages[0]?.id,
      flushFailureCount: publicApiLogFlushFailureCount
    }), 'Redis Stream 公开接口日志落库失败，消息保持 pending 等待重投')
  }
}

function publicApiLogRedisStreamQueue(): RedisStreamQueue<PublicApiLogInput> {
  if (!publicApiLogRedisStreamQueueInstance) {
    publicApiLogRedisStreamQueueInstance = new RedisStreamQueue<PublicApiLogInput>({
      streamKey: publicApiLogRedisStreamKey,
      groupName: publicApiLogRedisStreamGroup,
      readCount: publicApiLogFlushBatchSize
    })
  }
  return publicApiLogRedisStreamQueueInstance
}

export async function getPublicApiLogRedisStreamRuntime(): Promise<RedisStreamQueueRuntime | undefined> {
  if (!shouldUseRedisStreamPublicApiLogQueue()) return undefined
  return await publicApiLogRedisStreamQueue().inspectRuntime()
}

async function flushPublicApiLogQueueBatchAsync(): Promise<boolean> {
  const batch = publicApiLogQueue.slice(0, publicApiLogFlushBatchSize)
  if (batch.length === 0) {
    return true
  }
  const batchBytes = sumQueuedPublicApiLogBytes(batch)
  try {
    await createPublicApiLogsBatchAsync(batch.map((item) => item.input))
    publicApiLogQueue.splice(0, batch.length)
    publicApiLogQueueBytes = Math.max(0, publicApiLogQueueBytes - batchBytes)
    return true
  } catch (error) {
    publicApiLogFlushFailureCount += 1
    const first = batch[0]?.input
    logger.warn(errorLogFields(error, {
      event: 'public_api_log_batch_write_failed',
      batchSize: batch.length,
      batchBytes,
      pendingCount: publicApiLogQueue.length,
      pendingBytes: publicApiLogQueueBytes,
      flushFailureCount: publicApiLogFlushFailureCount,
      method: first?.method,
      path: first?.path,
      statusCode: first?.statusCode,
      traceId: first?.traceId
    }), '公开接口日志批量写入失败，已保留批次等待重试')
    return false
  }
}

function sumQueuedPublicApiLogBytes(items: QueuedPublicApiLog[]): number {
  return items.reduce((sum, item) => sum + item.bytes, 0)
}

function estimatePublicApiLogBytes(input: PublicApiLogInput): number {
  return estimateJsonLikeBytes(input, {
    maxBytes: publicApiLogEstimateMaxBytes,
    maxNodes: 20_000
  })
}

function sendPublicApiLogToParent(input: PublicApiLogInput): boolean {
  if (process.send && process.connected !== false) {
    try {
      process.send({
        type: 'background_worker_public_api_logs',
        items: [input]
      }, (error) => {
        if (error) {
          recordPublicApiLogDispatchFailure(error, input)
        }
      })
      return true
    } catch (error) {
      recordPublicApiLogDispatchFailure(error, input)
      return false
    }
  }
  recordPublicApiLogDispatchFailure(new Error('DB service 无父进程 IPC'), input)
  return false
}

function recordPublicApiLogDispatchFailure(error: unknown, input: PublicApiLogInput): void {
  droppedPublicApiLogCount += 1
  logger.warn(errorLogFields(error, {
    event: 'public_api_log_dispatch_failed',
    droppedPublicApiLogCount,
    traceId: input.traceId,
    method: input.method,
    path: input.path
  }), '公开接口日志投递 ingest-worker 失败，已丢弃日志记录')
}

function assertLocalPublicApiLogWriteAllowed(operation: string): void {
  if (shouldUseRedisStreamPublicApiLogQueue()) {
    throw new Error(`Redis Stream queue driver 下禁止写入公开接口日志本地队列：${operation}`)
  }
  if (!isPublicApiLogIngestWorker()) {
    throw new Error(`${operation} 只能在 ingest-worker 本地执行`)
  }
}

function isPublicApiLogIngestWorker(): boolean {
  return runtimeConfig.processRole === 'worker'
    && (runtimeConfig.workerRole === 'ingest-worker' || runtimeConfig.workerRole === 'log-worker')
}

function shouldUseRedisStreamPublicApiLogQueue(): boolean {
  return runtimeConfig.queueDriver === 'redis_stream'
}

function shouldEnqueuePublicApiLogToRedisStream(): boolean {
  return shouldUseRedisStreamPublicApiLogQueue()
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms)
    timer.unref()
  })
}
