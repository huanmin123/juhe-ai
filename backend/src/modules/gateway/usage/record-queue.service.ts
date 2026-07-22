import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../../config/runtime.js'
import { nowIso } from '../../../storage/database.js'
import {
  createUsageRecordsBatch,
  createUsageRecordsBatchAsync,
  freezeUsageRecordPricingFactsAsync,
  type UsageRecordInput
} from '../../../storage/repositories.js'
import { generateUsageRecordId } from '../../../storage/usage-record-shards.js'
import { closeUsageRecordWriterPool, getUsageRecordWriterPoolRuntime, usageRecordWriterPoolEnabled } from '../../../storage/usage-record-writer-pool.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import { estimateJsonLikeBytes } from '../../../shared/queue-size.js'
import { RedisStreamQueue, type RedisStreamMessage, type RedisStreamQueueRuntime } from '../../../shared/redis-stream-queue.js'
import { redisStreamQueueContracts } from '../../../shared/redis-stream-drain.js'
import { runRedisEnqueueWithBoundedRetry } from '../../../shared/redis-enqueue-retry.js'
import { fixedRetryPolicy, retryDelayMs } from '../../../shared/retry-policy.js'
import { sendUsageRecordsToWorkerAsync } from '../../background/background-ipc.js'
import { sanitizeHeaderRecord } from '../upstream/headers.js'

const usageRecordFlushIntervalMs = 500
const usageRecordRetryPolicy = fixedRetryPolicy('usage_record_queue_flush', 1000)
const usageRecordBatchSize = 1000
const usageRecordFlushBatchMaxBytes = 8 * 1024 * 1024
const usageRecordShutdownFlushMaxBatches = 100
const usageRecordQueueMaxItems = 10_000
const usageRecordQueueMaxBytes = 64 * 1024 * 1024
const usageRecordAdmissionWaiterMaxItems = 2048
const usageRecordEstimateMaxBytes = usageRecordQueueMaxBytes + 1
const slowUsageRecordFlushMs = 500
const usageRecordRedisStreamKey = redisStreamQueueContracts.usageRecords.streamKey
const usageRecordRedisStreamGroup = redisStreamQueueContracts.usageRecords.groupName
const usageRecordRedisConsumerErrorRetryMs = 1000

interface QueuedUsageRecord {
  input: UsageRecordInput
  bytes: number
  enqueuedAt: number
}

let pendingUsageRecords: QueuedUsageRecord[] = []
let pendingUsageRecordBytes = 0
let flushTimer: NodeJS.Timeout | undefined
let flushing = false
let activeAsyncFlush: Promise<void> | undefined
const activeConcurrentFlushes = new Set<Promise<void>>()
let retainedOverflowWarningCount = 0
let flushFailureCount = 0
let lastFlushMs = 0
let maxFlushMs = 0
let slowFlushCount = 0
let lastSlowFlushAt: string | undefined
let droppedDispatchCount = 0
let droppedOverflowCount = 0
let droppedOversizeCount = 0
let admissionWaitCount = 0
let rejectedAdmissionCount = 0
const usageRecordAdmissionWaiters: Array<{
  queued: QueuedUsageRecord
  resolve: () => void
  reject: (error: Error) => void
}> = []
let shutdownHooksInstalled = false
let allowDbServiceLocalUsageRecordWriteForTest = false
let usageRecordRedisStreamQueueInstance: RedisStreamQueue<UsageRecordInput> | undefined
let usageRecordRedisConsumerStarted = false
let usageRecordRedisConsumerStopping = false
let usageRecordRedisConsumerPromise: Promise<void> | undefined

interface UsageRecordFlushOptions {
  drain?: boolean
  retryOnFailure?: boolean
  maxBatches?: number
}

export async function enqueueUsageRecord(input: UsageRecordInput): Promise<void> {
  const queuedInput = normalizeUsageRecordInput(input)
  if (shouldEnqueueUsageRecordToRedisStream()) {
    const frozenInput = await freezeUsageRecordPricingFactsAsync(queuedInput)
    await enqueueUsageRecordToRedisStream(frozenInput)
    return
  }
  if (shouldDispatchUsageRecordToIngestWorker()) {
    try {
      await sendUsageRecordsToWorkerAsync([queuedInput])
    } catch (error) {
      recordUsageRecordDispatchFailure(error, queuedInput)
      throw error
    }
    return
  }

  if (runtimeConfig.processRole === 'db-service' && !isDbServiceLocalUsageRecordWriteAllowedForTest()) {
    await sendUsageRecordFromDbServiceToServer(queuedInput)
    return
  }

  await enqueueUsageRecordLocal(queuedInput)
}

export function enqueueUsageRecordsLocal(inputs: UsageRecordInput[]): Promise<void> {
  assertLocalUsageRecordWriteAllowed('enqueueUsageRecordsLocal')
  for (let index = 0; index < inputs.length; index += 1) {
    const input = inputs[index]
    if (!input) continue
    const admission = enqueueUsageRecordLocal(normalizeUsageRecordInput(input))
    if (admission) {
      return admission.then(() => enqueueUsageRecordsLocal(inputs.slice(index + 1)))
    }
  }
  return Promise.resolve()
}

export function startUsageRecordRedisStreamConsumer(): void {
  if (!shouldUseRedisStreamUsageRecordQueue() || !isUsageRecordIngestWorker() || usageRecordRedisConsumerStarted) {
    return
  }
  usageRecordRedisConsumerStarted = true
  usageRecordRedisConsumerStopping = false
  usageRecordRedisConsumerPromise = runUsageRecordRedisStreamConsumer().catch((error) => {
    logger.error(errorLogFields(error, {
      event: 'usage_record_redis_stream_consumer_stopped'
    }), 'Redis Stream 使用记录消费循环异常退出')
  }).finally(() => {
    usageRecordRedisConsumerStarted = false
    usageRecordRedisConsumerPromise = undefined
  })
}

export async function stopUsageRecordRedisStreamConsumer(): Promise<void> {
  usageRecordRedisConsumerStopping = true
  const queue = usageRecordRedisStreamQueueInstance
  if (queue) {
    await queue.closeConsumer().catch(() => undefined)
  }
  if (usageRecordRedisConsumerPromise) {
    await usageRecordRedisConsumerPromise.catch(() => undefined)
  }
}

function enqueueUsageRecordLocal(input: UsageRecordInput): Promise<void> | undefined {
  assertLocalUsageRecordWriteAllowed('enqueueUsageRecordLocal')
  const queued = {
    input,
    bytes: estimateUsageRecordBytes(input),
    enqueuedAt: Date.now()
  }
  if (queued.bytes > usageRecordQueueMaxBytes) {
    recordUsageRecordLocalDrop(queued, 'oversize')
    throw new Error('单条使用记录超过本地队列容量上限')
  }
  if (pendingUsageRecords.length >= usageRecordQueueMaxItems || pendingUsageRecordBytes + queued.bytes > usageRecordQueueMaxBytes) {
    if (usageRecordAdmissionWaiters.length >= usageRecordAdmissionWaiterMaxItems) {
      rejectedAdmissionCount += 1
      recordUsageRecordLocalDrop(queued, 'overflow')
      throw new Error('使用记录本地队列 admission 等待已满')
    }
    admissionWaitCount += 1
    return new Promise<void>((resolvePromise, rejectPromise) => {
      usageRecordAdmissionWaiters.push({ queued, resolve: resolvePromise, reject: rejectPromise })
    })
  }

  pendingUsageRecords.push(queued)
  pendingUsageRecordBytes += queued.bytes
  scheduleUsageRecordFlush(pendingUsageRecords.length >= usageRecordBatchSize ? 0 : usageRecordFlushIntervalMs)
  return undefined
}

export function flushUsageRecordQueue(options: UsageRecordFlushOptions = {}): void {
  if (usageRecordWriterPoolEnabled() || shouldUseConcurrentUsageRecordFlush()) {
    void flushUsageRecordQueueAsync(options)
    return
  }
  flushUsageRecordQueueSync(options)
}

function flushUsageRecordQueueSync(options: UsageRecordFlushOptions = {}): void {
  if (!isLocalUsageRecordWriteAllowed()) {
    return
  }
  if (flushing || pendingUsageRecords.length === 0) {
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
      const { batch, bytes: batchBytes } = peekUsageRecordFlushBatch(usageRecordBatchSize, usageRecordFlushBatchMaxBytes)
      if (batch.length === 0) {
        break
      }
      flushedBatches += 1

      try {
        const startedAt = Date.now()
        createUsageRecordsBatch(batch.map((item) => item.input))
        recordUsageRecordFlushDuration(Date.now() - startedAt)
        removeUsageRecordFlushBatch(batch.length, batchBytes)
        flushFailureCount = 0
      } catch (error) {
        failed = true
        flushFailureCount += 1
        logger.error(errorLogFields(error, {
          event: 'usage_record_queue_flush_failed',
          batchSize: batch.length,
          pendingCount: pendingUsageRecords.length,
          pendingBytes: pendingUsageRecordBytes,
          flushFailureCount
        }), '使用记录队列写入失败，已保留记录等待重试')
        shouldRetry = options.retryOnFailure !== false
        break
      }
    } while (options.drain && pendingUsageRecords.length > 0 && flushedBatches < maxBatches)
  } finally {
    flushing = false
  }

  if (pendingUsageRecords.length > 0 && (!failed || shouldRetry)) {
    scheduleUsageRecordFlush(shouldRetry ? retryDelayMs(usageRecordRetryPolicy) : 0)
  }
}

export async function flushUsageRecordQueueAsync(options: UsageRecordFlushOptions = {}): Promise<void> {
  if (shouldUseConcurrentUsageRecordFlush()) {
    await flushUsageRecordQueueConcurrentAsync(options)
    return
  }
  if (!isLocalUsageRecordWriteAllowed()) {
    return
  }
  if (activeAsyncFlush) {
    await activeAsyncFlush
    return
  }
  if (flushing || pendingUsageRecords.length === 0) {
    return
  }

  activeAsyncFlush = runUsageRecordQueueAsyncFlush(options).finally(() => {
    activeAsyncFlush = undefined
  })
  await activeAsyncFlush
}

async function runUsageRecordQueueAsyncFlush(options: UsageRecordFlushOptions = {}): Promise<void> {
  if (!isLocalUsageRecordWriteAllowed()) {
    return
  }
  if (flushing || pendingUsageRecords.length === 0) {
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
      const { batch, bytes: batchBytes } = peekUsageRecordFlushBatch(usageRecordBatchSize, usageRecordFlushBatchMaxBytes)
      if (batch.length === 0) {
        break
      }
      flushedBatches += 1

      try {
        const startedAt = Date.now()
        await createUsageRecordsBatchAsync(batch.map((item) => item.input))
        recordUsageRecordFlushDuration(Date.now() - startedAt)
        removeUsageRecordFlushBatch(batch.length, batchBytes)
        flushFailureCount = 0
      } catch (error) {
        failed = true
        flushFailureCount += 1
        logger.error(errorLogFields(error, {
          event: 'usage_record_queue_flush_failed',
          batchSize: batch.length,
          pendingCount: pendingUsageRecords.length,
          pendingBytes: pendingUsageRecordBytes,
          flushFailureCount
        }), '使用记录队列写入失败，已保留记录等待重试')
        shouldRetry = options.retryOnFailure !== false
        break
      }
    } while (options.drain && pendingUsageRecords.length > 0 && flushedBatches < maxBatches)
  } finally {
    flushing = false
  }

  if (pendingUsageRecords.length > 0 && (!failed || shouldRetry)) {
    scheduleUsageRecordFlush(shouldRetry ? retryDelayMs(usageRecordRetryPolicy) : 0)
  }
}

export function flushAllUsageRecordQueue(): void {
  flushUsageRecordQueue({ drain: true, retryOnFailure: false })
}

export async function flushAllUsageRecordQueueAsync(): Promise<void> {
  if (usageRecordWriterPoolEnabled() || shouldUseConcurrentUsageRecordFlush()) {
    await waitForActiveUsageRecordFlush()
    await flushUsageRecordQueueAsync({ drain: true, retryOnFailure: false })
    return
  }
  flushUsageRecordQueueSync({ drain: true, retryOnFailure: false })
}

export async function flushUsageRecordQueueForShutdown(): Promise<void> {
  if (usageRecordWriterPoolEnabled() || shouldUseConcurrentUsageRecordFlush()) {
    await waitForActiveUsageRecordFlush()
    await flushUsageRecordQueueAsync({ drain: true, retryOnFailure: false, maxBatches: usageRecordShutdownFlushMaxBatches })
    return
  }
  flushUsageRecordQueueSync({ drain: true, retryOnFailure: false, maxBatches: usageRecordShutdownFlushMaxBatches })
}

async function waitForActiveUsageRecordFlush(): Promise<void> {
  if (activeAsyncFlush) {
    await activeAsyncFlush
  }
  while (activeConcurrentFlushes.size > 0) {
    await Promise.race(activeConcurrentFlushes)
  }
}

export function getUsageRecordQueueRuntime(): {
  queueLength: number
  queueBytes: number
  oldestCreatedAt?: string
  droppedCount: number
  retainedOverflowWarningCount: number
  droppedOverflowCount: number
  droppedOversizeCount: number
  admissionWaitCount: number
  admissionWaiterCount: number
  rejectedAdmissionCount: number
  flushFailureCount: number
  oldestQueuedMs: number
  lastFlushMs: number
  maxFlushMs: number
  slowFlushCount: number
  lastSlowFlushAt?: string
  writerPoolEnabled: boolean
  writerPoolWorkerCount: number
  writerPoolQueueLength: number
  writerPoolActiveJobs: number
  writerPoolHandledJobs: number
  writerPoolFailedJobs: number
  writerPoolRejectedJobs: number
  writerPoolOldestQueuedMs: number
  writerPoolMaxQueueWaitMs: number
  writerPoolMaxRunMs: number
} {
  const writerPoolRuntime = getUsageRecordWriterPoolRuntime()
  return {
    queueLength: pendingUsageRecords.length,
    queueBytes: pendingUsageRecordBytes,
    oldestCreatedAt: oldestUsageRecordCreatedAt(),
    droppedCount: droppedDispatchCount + droppedOverflowCount + droppedOversizeCount,
    retainedOverflowWarningCount,
    droppedOverflowCount,
    droppedOversizeCount,
    admissionWaitCount,
    admissionWaiterCount: usageRecordAdmissionWaiters.length,
    rejectedAdmissionCount,
    flushFailureCount,
    oldestQueuedMs: oldestUsageRecordQueuedMs(),
    lastFlushMs,
    maxFlushMs,
    slowFlushCount,
    lastSlowFlushAt,
    writerPoolEnabled: writerPoolRuntime.enabled,
    writerPoolWorkerCount: writerPoolRuntime.workerCount,
    writerPoolQueueLength: writerPoolRuntime.queueLength,
    writerPoolActiveJobs: writerPoolRuntime.activeJobs,
    writerPoolHandledJobs: writerPoolRuntime.handledJobs,
    writerPoolFailedJobs: writerPoolRuntime.failedJobs,
    writerPoolRejectedJobs: writerPoolRuntime.rejectedJobs,
    writerPoolOldestQueuedMs: writerPoolRuntime.oldestQueuedMs,
    writerPoolMaxQueueWaitMs: writerPoolRuntime.maxQueueWaitMs,
    writerPoolMaxRunMs: writerPoolRuntime.maxRunMs
  }
}

export async function getUsageRecordRedisStreamRuntime(): Promise<RedisStreamQueueRuntime | undefined> {
  if (!shouldUseRedisStreamUsageRecordQueue()) {
    return undefined
  }
  return await usageRecordRedisStreamQueue().inspectRuntime()
}

export async function getUsageRecordRedisStreamOldestCreatedAt(): Promise<string | undefined> {
  if (!shouldUseRedisStreamUsageRecordQueue()) {
    return undefined
  }
  const inspection = await usageRecordRedisStreamQueue().inspectBacklog(512)
  if (inspection.pendingTruncated || inspection.undeliveredTruncated) {
    logger.warn({
      event: 'usage_record_redis_stream_backlog_inspection_truncated',
      pendingCount: inspection.runtime.pendingCount,
      lag: inspection.runtime.lag,
      scannedMessages: inspection.messages.length
    }, 'Redis Stream 使用记录 backlog 超过统计保护扫描上限，本轮统计使用保守安全边界')
    return '1970-01-01T00:00:00.000Z'
  }
  let oldest: string | undefined
  for (const message of inspection.messages) {
    const createdAt = normalizeUsageRecordCreatedAtForBacklog(message.payload.createdAt)
    if (!createdAt) continue
    if (!oldest || Date.parse(createdAt) < Date.parse(oldest)) {
      oldest = createdAt
    }
  }
  return oldest
}

export function installUsageRecordQueueShutdownHooks(): void {
  if (shutdownHooksInstalled) {
    return
  }
  shutdownHooksInstalled = true

  process.once('beforeExit', () => {
    void flushUsageRecordQueueForShutdown().finally(() => {
      void closeUsageRecordWriterPool()
    })
  })
}

function scheduleUsageRecordFlush(delayMs: number): void {
  if (!isLocalUsageRecordWriteAllowed()) {
    return
  }
  if (flushTimer || flushing) {
    return
  }
  flushTimer = setTimeout(() => {
    flushTimer = undefined
    flushUsageRecordQueue()
  }, delayMs)
  flushTimer.unref()
}

function recordUsageRecordDispatchFailure(error: unknown, input: UsageRecordInput): void {
  droppedDispatchCount += 1
  logger.warn(errorLogFields(error, {
    event: 'usage_record_queue_dispatch_failed',
    usageRecordId: input.id,
    traceId: input.traceId,
    trafficSource: input.trafficSource,
    systemAccountId: input.systemAccountId,
    endpoint: input.endpoint,
    statusCode: input.statusCode,
    errorCode: input.errorCode,
    droppedDispatchCount
  }), '使用记录投递后台 worker 失败，调用方将收到明确失败')
}

async function sendUsageRecordFromDbServiceToServer(input: UsageRecordInput): Promise<void> {
  if (typeof process.send !== 'function' || process.connected === false) {
    const error = new Error('DB service 无父进程 IPC')
    recordUsageRecordDispatchFailure(error, input)
    throw error
  }
  await new Promise<void>((resolvePromise, rejectPromise) => {
    try {
      process.send?.({
        type: 'background_worker_usage_records',
        items: [input]
      }, (error) => {
        if (!error) {
          resolvePromise()
          return
        }
        recordUsageRecordDispatchFailure(error, input)
        rejectPromise(error)
      })
    } catch (error) {
      recordUsageRecordDispatchFailure(error, input)
      rejectPromise(error)
    }
  })
}

async function enqueueUsageRecordToRedisStream(input: UsageRecordInput): Promise<void> {
  try {
    await runRedisEnqueueWithBoundedRetry(() => usageRecordRedisStreamQueue().enqueue(input))
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'usage_record_redis_stream_enqueue_failed',
      traceId: input.traceId,
      apiKeyId: input.apiKeyId,
      accountId: input.accountId
    }), '使用记录写入 Redis Stream 失败，高性能模式禁止回退 IPC 或本地队列')
    throw error
  }
}

async function runUsageRecordRedisStreamConsumer(): Promise<void> {
  const queue = usageRecordRedisStreamQueue()
  while (!usageRecordRedisConsumerStopping) {
    try {
      const claimed = await queue.claimPending()
      const messages = claimed.length > 0 ? claimed : await queue.readNew()
      if (messages.length === 0) {
        continue
      }
      await flushUsageRecordRedisStreamMessages(messages)
    } catch (error) {
      if (usageRecordRedisConsumerStopping) {
        break
      }
      flushFailureCount += 1
      logger.error(errorLogFields(error, {
        event: 'usage_record_redis_stream_consume_failed',
        flushFailureCount
      }), 'Redis Stream 使用记录消费失败，稍后重试')
      await delay(usageRecordRedisConsumerErrorRetryMs)
    }
  }
}

async function flushUsageRecordRedisStreamMessages(messages: Array<RedisStreamMessage<UsageRecordInput>>): Promise<void> {
  if (messages.length === 0) return
  const queue = usageRecordRedisStreamQueue()
  const startedAt = Date.now()
  try {
    const inputs = messages.map((message) => normalizeUsageRecordInput(message.payload))
    await createUsageRecordsBatchAsync(inputs)
    recordUsageRecordFlushDuration(Date.now() - startedAt)
    flushFailureCount = 0
    await queue.ack(messages.map((message) => message.id))
  } catch (error) {
    flushFailureCount += 1
    logger.error(errorLogFields(error, {
      event: 'usage_record_redis_stream_flush_failed',
      batchSize: messages.length,
      firstMessageId: messages[0]?.id,
      flushFailureCount
    }), 'Redis Stream 使用记录落库失败，消息保持 pending 等待重投')
    await delay(usageRecordRedisConsumerErrorRetryMs)
  }
}

function usageRecordRedisStreamQueue(): RedisStreamQueue<UsageRecordInput> {
  if (!usageRecordRedisStreamQueueInstance) {
    usageRecordRedisStreamQueueInstance = new RedisStreamQueue<UsageRecordInput>({
      streamKey: usageRecordRedisStreamKey,
      groupName: usageRecordRedisStreamGroup
    })
  }
  return usageRecordRedisStreamQueueInstance
}

function shouldUseRedisStreamUsageRecordQueue(): boolean {
  return runtimeConfig.queueDriver === 'redis_stream'
}

function shouldEnqueueUsageRecordToRedisStream(): boolean {
  return shouldUseRedisStreamUsageRecordQueue()
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms)
    timer.unref()
  })
}

function normalizeUsageRecordInput(input: UsageRecordInput): UsageRecordInput {
  const createdAt = normalizeUsageRecordCreatedAt(input.createdAt)
  const normalized: UsageRecordInput = {
    ...input,
    id: input.id ?? generateUsageRecordId(createdAt, randomUUID()),
    errorCode: sanitizeUsageRecordErrorMessage(input.errorCode),
    errorMessage: sanitizeUsageRecordErrorMessage(input.errorMessage),
    requestSnapshot: sanitizeUsageRecordSnapshot(input.requestSnapshot),
    responseSnapshot: sanitizeUsageRecordSnapshot(input.responseSnapshot),
    createdAt
  }
  const accountId = normalizedUsageScopeValue(input.accountId)
  const accountOwnerSystemAccountId = normalizedUsageScopeValue(input.accountOwnerSystemAccountId)
  const accountAccessType = normalizedAccountAccessType(input.accountAccessType)
  const accountAuthorizationId = accountAccessType === 'account_authorized'
    ? normalizedUsageScopeValue(input.accountAuthorizationId)
    : undefined
  const accountAuthorizationSourceType = normalizedAuthorizationSourceType(input.accountAuthorizationSourceType)
  const accountAuthorizationSourceTeamId = normalizedUsageScopeValue(input.accountAuthorizationSourceTeamId)
  if (!accountId || !accountOwnerSystemAccountId || !accountAccessType || (accountAccessType === 'account_authorized' && !accountAuthorizationId)
    || (accountAuthorizationId && !authorizationSourcePairIsValid(accountAuthorizationSourceType, accountAuthorizationSourceTeamId))) {
    normalized.accountId = undefined
    normalized.accountOwnerSystemAccountId = undefined
    normalized.accountAccessType = undefined
    normalized.accountAuthorizationId = undefined
    normalized.accountAuthorizationSourceType = undefined
    normalized.accountAuthorizationSourceTeamId = undefined
  } else {
    normalized.accountId = accountId
    normalized.accountOwnerSystemAccountId = accountOwnerSystemAccountId
    normalized.accountAccessType = accountAccessType
    normalized.accountAuthorizationId = accountAuthorizationId
    normalized.accountAuthorizationSourceType = accountAuthorizationId
      ? accountAuthorizationSourceType
      : undefined
    normalized.accountAuthorizationSourceTeamId = accountAuthorizationId
      ? accountAuthorizationSourceTeamId
      : undefined
  }
  const groupId = normalizedUsageScopeValue(input.groupId)
  const groupOwnerSystemAccountId = normalizedUsageScopeValue(input.groupOwnerSystemAccountId)
  const groupAccessType = normalizedGroupAccessType(input.groupAccessType)
  const groupAuthorizationId = groupAccessType === 'authorized'
    ? normalizedUsageScopeValue(input.groupAuthorizationId)
    : undefined
  const groupAuthorizationSourceType = normalizedAuthorizationSourceType(input.groupAuthorizationSourceType)
  const groupAuthorizationSourceTeamId = normalizedUsageScopeValue(input.groupAuthorizationSourceTeamId)
  if (!groupId || !groupOwnerSystemAccountId || !groupAccessType || (groupAccessType === 'authorized' && !groupAuthorizationId)
    || (groupAuthorizationId && !authorizationSourcePairIsValid(groupAuthorizationSourceType, groupAuthorizationSourceTeamId))) {
    normalized.groupId = undefined
    normalized.groupOwnerSystemAccountId = undefined
    normalized.groupAccessType = undefined
    normalized.groupAuthorizationId = undefined
    normalized.groupAuthorizationSourceType = undefined
    normalized.groupAuthorizationSourceTeamId = undefined
  } else {
    normalized.groupId = groupId
    normalized.groupOwnerSystemAccountId = groupOwnerSystemAccountId
    normalized.groupAccessType = groupAccessType
    normalized.groupAuthorizationId = groupAuthorizationId
    normalized.groupAuthorizationSourceType = groupAuthorizationId
      ? groupAuthorizationSourceType
      : undefined
    normalized.groupAuthorizationSourceTeamId = groupAuthorizationId
      ? groupAuthorizationSourceTeamId
      : undefined
  }
  if (normalized.accountAccessType === 'group_authorized'
    && (!normalized.groupId || normalized.groupAccessType !== 'authorized' || !normalized.groupAuthorizationId)) {
    normalized.accountId = undefined
    normalized.accountOwnerSystemAccountId = undefined
    normalized.accountAccessType = undefined
    normalized.accountAuthorizationId = undefined
    normalized.accountAuthorizationSourceType = undefined
    normalized.accountAuthorizationSourceTeamId = undefined
  }
  return normalized
}

function normalizedUsageScopeValue(value: string | undefined): string | undefined {
  const normalized = value?.trim()
  return normalized || undefined
}

function normalizedAccountAccessType(value: UsageRecordInput['accountAccessType']): UsageRecordInput['accountAccessType'] {
  return value === 'owner' || value === 'account_authorized' || value === 'group_authorized'
    ? value
    : undefined
}

function normalizedGroupAccessType(value: UsageRecordInput['groupAccessType']): UsageRecordInput['groupAccessType'] {
  return value === 'owner' || value === 'authorized' ? value : undefined
}

function normalizedAuthorizationSourceType(
  value: UsageRecordInput['accountAuthorizationSourceType']
): UsageRecordInput['accountAuthorizationSourceType'] {
  return value === 'manual' || value === 'team' ? value : undefined
}

function authorizationSourcePairIsValid(
  sourceType: UsageRecordInput['accountAuthorizationSourceType'],
  sourceTeamId: string | undefined
): boolean {
  if (sourceType === 'team') return Boolean(sourceTeamId)
  return !sourceTeamId
}

function normalizeUsageRecordCreatedAt(value: unknown): string {
  return normalizeUsageRecordCreatedAtForBacklog(value) ?? nowIso()
}

function normalizeUsageRecordCreatedAtForBacklog(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const trimmed = value.trim()
  if (!trimmed) return undefined
  const time = Date.parse(trimmed)
  return Number.isFinite(time) ? new Date(time).toISOString() : undefined
}

export function pendingUsageRecordCount(): number {
  return pendingUsageRecords.length
}

export function peekPendingUsageRecordForTest(): UsageRecordInput | undefined {
  const input = pendingUsageRecords[0]?.input
  return input ? { ...input } : undefined
}

export function isUsageRecordInput(value: unknown): value is UsageRecordInput {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return false
  }
  const record = value as Record<string, unknown>
  return typeof record.traceId === 'string'
    && typeof record.trafficSource === 'string'
    && typeof record.success === 'boolean'
}

export function clearUsageRecordQueueForTest(): void {
  if (flushTimer) {
    clearTimeout(flushTimer)
    flushTimer = undefined
  }
  pendingUsageRecords = []
  pendingUsageRecordBytes = 0
  flushing = false
  activeAsyncFlush = undefined
  activeConcurrentFlushes.clear()
  retainedOverflowWarningCount = 0
  flushFailureCount = 0
  lastFlushMs = 0
  maxFlushMs = 0
  slowFlushCount = 0
  lastSlowFlushAt = undefined
  droppedDispatchCount = 0
  droppedOverflowCount = 0
  droppedOversizeCount = 0
  admissionWaitCount = 0
  rejectedAdmissionCount = 0
  while (usageRecordAdmissionWaiters.length > 0) {
    usageRecordAdmissionWaiters.shift()?.reject(new Error('使用记录本地队列已由测试清理'))
  }
  shutdownHooksInstalled = false
  allowDbServiceLocalUsageRecordWriteForTest = false
  usageRecordRedisConsumerStopping = true
  usageRecordRedisConsumerStarted = false
  usageRecordRedisConsumerPromise = undefined
  void usageRecordRedisStreamQueueInstance?.closeConsumer().catch(() => undefined)
  usageRecordRedisStreamQueueInstance = undefined
}

export function releaseUsageRecordQueueCapacityForTest(count = 1): void {
  const normalizedCount = Math.max(0, Math.min(Math.trunc(count), pendingUsageRecords.length))
  if (normalizedCount <= 0) return
  const bytes = pendingUsageRecords.slice(0, normalizedCount).reduce((total, item) => total + item.bytes, 0)
  removeUsageRecordFlushBatch(normalizedCount, bytes)
}

export function setDbServiceUsageRecordLocalWriteAllowedForTest(value: boolean): void {
  allowDbServiceLocalUsageRecordWriteForTest = value
}

function sanitizeUsageRecordSnapshot(value: unknown): unknown {
  if (value === undefined || value === null) {
    return undefined
  }
  return sanitizeSnapshotValue(value, {
    depth: 0,
    bytes: 0,
    truncated: false,
    seen: new WeakSet<object>()
  })
}

function sanitizeUsageRecordErrorMessage(value: string | undefined): string | undefined {
  return value
}

function sanitizeSnapshotValue(value: unknown, context: SnapshotSanitizeContext): unknown {
  if (context.bytes >= usageSnapshotMaxBytes) {
    context.truncated = true
    return '[truncated]'
  }
  if (typeof value === 'string') {
    return sanitizeSnapshotString(value, context)
  }
  if (typeof value === 'number' || typeof value === 'boolean' || value === null || value === undefined) {
    context.bytes += 8
    return value
  }
  if (Buffer.isBuffer(value)) {
    context.bytes += Math.min(value.byteLength, usageSnapshotMaxStringBytes)
    return {
      _buffer: true,
      bytes: value.byteLength,
      truncated: value.byteLength > usageSnapshotMaxStringBytes
    }
  }
  if (value instanceof Date) {
    return sanitizeSnapshotString(value.toISOString(), context)
  }
  if (typeof value !== 'object') {
    return sanitizeSnapshotString(String(value), context)
  }
  if (context.seen.has(value)) {
    return '[circular]'
  }
  if (context.depth >= usageSnapshotMaxDepth) {
    context.truncated = true
    return '[depth_truncated]'
  }
  context.seen.add(value)
  if (Array.isArray(value)) {
    const items: unknown[] = []
    for (let index = 0; index < value.length && index < usageSnapshotMaxArrayItems; index += 1) {
      context.depth += 1
      items.push(sanitizeSnapshotValue(value[index], context))
      context.depth -= 1
      if (context.bytes >= usageSnapshotMaxBytes) break
    }
    if (value.length > items.length) {
      context.truncated = true
      items.push(`[${value.length - items.length} items truncated]`)
    }
    return items
  }
  const output: Record<string, unknown> = {}
  let visitedKeys = 0
  let truncatedByKeyLimit = false
  const record = value as Record<string, unknown>
  for (const key in record) {
    if (!Object.prototype.hasOwnProperty.call(record, key)) {
      continue
    }
    if (visitedKeys >= usageSnapshotMaxObjectKeys) {
      truncatedByKeyLimit = true
      break
    }
    context.bytes += boundedStringByteLength(key, usageSnapshotMaxBytes - context.bytes) + 4
    context.depth += 1
    output[key] = sanitizeSnapshotValue(sanitizeSnapshotField(key, record[key]), context)
    context.depth -= 1
    visitedKeys += 1
    if (context.bytes >= usageSnapshotMaxBytes) break
  }
  if (truncatedByKeyLimit || context.truncated || context.bytes >= usageSnapshotMaxBytes) {
    output._truncated = true
  }
  return output
}

function sanitizeSnapshotString(value: string, context: SnapshotSanitizeContext): string {
  const remaining = Math.max(0, usageSnapshotMaxBytes - context.bytes)
  const limit = Math.min(usageSnapshotMaxStringBytes, remaining)
  const bytes = boundedStringByteLength(value, limit + 1)
  if (bytes <= limit) {
    context.bytes += bytes
    return value
  }
  context.truncated = true
  const suffix = `...[truncated ${bytes - limit} bytes]`
  const prefixBytes = Math.max(0, limit - Buffer.byteLength(suffix, 'utf8'))
  const truncated = sliceStringByUtf8Bytes(value, prefixBytes)
  context.bytes += boundedStringByteLength(truncated, prefixBytes) + Buffer.byteLength(suffix, 'utf8')
  return `${truncated}${suffix}`
}

function sanitizeSnapshotField(key: string, value: unknown): unknown {
  if (isHeaderSnapshotField(key, value)) {
    return sanitizeHeaderRecord(value as Record<string, string | string[]>)
  }
  return value
}

function isHeaderSnapshotField(key: string, value: unknown): boolean {
  return key.toLowerCase() === 'headers'
    && typeof value === 'object'
    && value !== null
    && !Array.isArray(value)
    && !Buffer.isBuffer(value)
}

interface SnapshotSanitizeContext {
  depth: number
  bytes: number
  truncated: boolean
  seen: WeakSet<object>
}

const usageSnapshotMaxBytes = 64 * 1024
const usageSnapshotMaxStringBytes = 16 * 1024
const usageSnapshotMaxArrayItems = 50
const usageSnapshotMaxObjectKeys = 80
const usageSnapshotMaxDepth = 6
const exactSnapshotStringByteLengthMaxChars = 16 * 1024

function boundedStringByteLength(value: string, maxBytes: number): number {
  if (maxBytes <= 0) return 0
  if (value.length <= exactSnapshotStringByteLengthMaxChars) {
    return Math.min(maxBytes, Buffer.byteLength(value, 'utf8'))
  }
  return Math.min(maxBytes, value.length * 4)
}

function sliceStringByUtf8Bytes(value: string, maxBytes: number): string {
  if (maxBytes <= 0) return ''
  let bytes = 0
  let index = 0
  while (index < value.length) {
    const codePoint = value.codePointAt(index)
    if (codePoint === undefined) break
    const charBytes = codePointUtf8ByteLength(codePoint)
    if (bytes + charBytes > maxBytes) break
    bytes += charBytes
    index += codePoint > 0xffff ? 2 : 1
  }
  return value.slice(0, index)
}

function codePointUtf8ByteLength(codePoint: number): number {
  if (codePoint <= 0x7f) return 1
  if (codePoint <= 0x7ff) return 2
  if (codePoint <= 0xffff) return 3
  return 4
}

function normalizeMaxBatches(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : Number.POSITIVE_INFINITY
}

function peekUsageRecordFlushBatch(maxItems: number, maxBytes: number): { batch: QueuedUsageRecord[]; bytes: number } {
  const itemLimit = Math.max(1, Math.trunc(maxItems))
  const byteLimit = Math.max(1, Math.trunc(maxBytes))
  let count = 0
  let bytes = 0
  while (count < itemLimit && count < pendingUsageRecords.length) {
    const next = pendingUsageRecords[count]
    if (!next) break
    if (count > 0 && bytes + next.bytes > byteLimit) {
      break
    }
    bytes += next.bytes
    count += 1
  }
  const batch = pendingUsageRecords.slice(0, count)
  return { batch, bytes }
}

function removeUsageRecordFlushBatch(count: number, bytes: number): void {
  pendingUsageRecords.splice(0, count)
  pendingUsageRecordBytes = Math.max(0, pendingUsageRecordBytes - bytes)
  admitWaitingUsageRecords()
}

function admitWaitingUsageRecords(): void {
  while (usageRecordAdmissionWaiters.length > 0) {
    const waiter = usageRecordAdmissionWaiters[0]
    if (!waiter) break
    if (pendingUsageRecords.length >= usageRecordQueueMaxItems) break
    if (pendingUsageRecordBytes + waiter.queued.bytes > usageRecordQueueMaxBytes) break
    usageRecordAdmissionWaiters.shift()
    pendingUsageRecords.push(waiter.queued)
    pendingUsageRecordBytes += waiter.queued.bytes
    waiter.resolve()
  }
}

async function flushUsageRecordQueueConcurrentAsync(options: UsageRecordFlushOptions = {}): Promise<void> {
  if (!isLocalUsageRecordWriteAllowed()) {
    return
  }
  if (flushTimer) {
    clearTimeout(flushTimer)
    flushTimer = undefined
  }

  const spawned = new Set<Promise<void>>()
  const maxBatches = normalizeMaxBatches(options.maxBatches)
  let spawnedBatches = 0

  while (
    pendingUsageRecords.length > 0
    && activeConcurrentFlushes.size < usageRecordConcurrentFlushLimit()
    && spawnedBatches < maxBatches
  ) {
    const { batch, bytes } = takeUsageRecordFlushBatch(usageRecordBatchSize, usageRecordFlushBatchMaxBytes)
    if (batch.length === 0) break
    spawnedBatches += 1
    const promise = flushUsageRecordBatchAsync(batch, bytes, options)
    activeConcurrentFlushes.add(promise)
    spawned.add(promise)
    void promise.finally(() => {
      activeConcurrentFlushes.delete(promise)
    })
  }

  if (options.drain) {
    while ((pendingUsageRecords.length > 0 && spawnedBatches < maxBatches) || activeConcurrentFlushes.size > 0) {
      if (activeConcurrentFlushes.size > 0) {
        await Promise.race(activeConcurrentFlushes)
      }
      while (
        pendingUsageRecords.length > 0
        && activeConcurrentFlushes.size < usageRecordConcurrentFlushLimit()
        && spawnedBatches < maxBatches
      ) {
        const { batch, bytes } = takeUsageRecordFlushBatch(usageRecordBatchSize, usageRecordFlushBatchMaxBytes)
        if (batch.length === 0) break
        spawnedBatches += 1
        const promise = flushUsageRecordBatchAsync(batch, bytes, options)
        activeConcurrentFlushes.add(promise)
        spawned.add(promise)
        void promise.finally(() => {
          activeConcurrentFlushes.delete(promise)
        })
      }
      if (pendingUsageRecords.length === 0 && activeConcurrentFlushes.size === 0) {
        break
      }
      if (spawnedBatches >= maxBatches) {
        break
      }
    }
  }

  if (spawned.size > 0) {
    await Promise.allSettled(spawned)
  }
  if (pendingUsageRecords.length > 0 && !options.drain) {
    scheduleUsageRecordFlush(0)
  }
}

async function flushUsageRecordBatchAsync(batch: QueuedUsageRecord[], batchBytes: number, options: UsageRecordFlushOptions): Promise<void> {
  try {
    const startedAt = Date.now()
    await createUsageRecordsBatchAsync(batch.map((item) => item.input))
    recordUsageRecordFlushDuration(Date.now() - startedAt)
    flushFailureCount = 0
  } catch (error) {
    requeueUsageRecordFlushBatch(batch, batchBytes)
    flushFailureCount += 1
    logger.error(errorLogFields(error, {
      event: 'usage_record_queue_flush_failed',
      batchSize: batch.length,
      pendingCount: pendingUsageRecords.length,
      pendingBytes: pendingUsageRecordBytes,
      flushFailureCount
    }), '使用记录队列写入失败，已保留记录等待重试')
    if (options.retryOnFailure !== false) {
      scheduleUsageRecordFlush(retryDelayMs(usageRecordRetryPolicy))
    }
  }
}

function takeUsageRecordFlushBatch(maxItems: number, maxBytes: number): { batch: QueuedUsageRecord[]; bytes: number } {
  const { batch, bytes } = peekUsageRecordFlushBatch(maxItems, maxBytes)
  if (batch.length > 0) {
    removeUsageRecordFlushBatch(batch.length, bytes)
  }
  return { batch, bytes }
}

function requeueUsageRecordFlushBatch(batch: QueuedUsageRecord[], bytes: number): void {
  pendingUsageRecords.unshift(...batch)
  pendingUsageRecordBytes += bytes
}

function shouldUseConcurrentUsageRecordFlush(): boolean {
  return runtimeConfig.databaseDriver === 'postgres'
}

function usageRecordConcurrentFlushLimit(): number {
  return Math.max(1, Math.min(Math.trunc(runtimeConfig.postgres.writeMaxConcurrency), 100))
}

function estimateUsageRecordBytes(input: UsageRecordInput): number {
  return estimateJsonLikeBytes(input, { maxBytes: usageRecordEstimateMaxBytes }) + 256
}

function recordUsageRecordFlushDuration(durationMs: number): void {
  const rounded = Math.max(0, Math.round(durationMs))
  lastFlushMs = rounded
  maxFlushMs = Math.max(maxFlushMs, rounded)
  if (rounded >= slowUsageRecordFlushMs) {
    slowFlushCount += 1
    lastSlowFlushAt = new Date().toISOString()
  }
}

function oldestUsageRecordQueuedMs(): number {
  const first = pendingUsageRecords[0]
  return first ? Math.max(0, Date.now() - first.enqueuedAt) : 0
}

function oldestUsageRecordCreatedAt(): string | undefined {
  let oldest: string | undefined
  for (const item of pendingUsageRecords) {
    const createdAt = item.input.createdAt?.trim()
    if (!createdAt) continue
    if (!oldest || createdAt < oldest) {
      oldest = createdAt
    }
  }
  return oldest
}

function recordUsageRecordLocalDrop(item: QueuedUsageRecord, reason: 'overflow' | 'oversize'): void {
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
    event: 'usage_record_queue_dropped',
    reason,
    usageRecordId: item.input.id,
    traceId: item.input.traceId,
    trafficSource: item.input.trafficSource,
    systemAccountId: item.input.systemAccountId,
    endpoint: item.input.endpoint,
    statusCode: item.input.statusCode,
    bytes: item.bytes,
    pendingCount: pendingUsageRecords.length,
    pendingBytes: pendingUsageRecordBytes,
    droppedOverflowCount,
    droppedOversizeCount
  }, '使用记录队列达到保护上限，已丢弃新记录')
}

function assertLocalUsageRecordWriteAllowed(operation: string): void {
  if (shouldUseRedisStreamUsageRecordQueue()) {
    throw new Error(`Redis Stream queue driver 下禁止写入使用记录本地队列：${operation}`)
  }
  if (!isLocalUsageRecordWriteAllowed()) {
    throw new Error(`${runtimeConfig.processRole}/${runtimeConfig.workerRole} 角色禁止直接写入使用记录：${operation} 必须投递 ingest-worker`)
  }
}

function isLocalUsageRecordWriteAllowed(): boolean {
  return isUsageRecordIngestWorker() || isDbServiceLocalUsageRecordWriteAllowedForTest()
}

function isUsageRecordIngestWorker(): boolean {
  return runtimeConfig.processRole === 'worker' && runtimeConfig.workerRole === 'ingest-worker'
}

function isDbServiceLocalUsageRecordWriteAllowedForTest(): boolean {
  return allowDbServiceLocalUsageRecordWriteForTest && runtimeConfig.processRole === 'db-service'
}

function shouldDispatchUsageRecordToIngestWorker(): boolean {
  return runtimeConfig.processRole === 'server'
    || (runtimeConfig.processRole === 'worker' && runtimeConfig.workerRole !== 'ingest-worker')
}
