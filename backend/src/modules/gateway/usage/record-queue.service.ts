import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../../config/runtime.js'
import { nowIso } from '../../../storage/database.js'
import { createUsageRecordsBatch, type UsageRecordInput } from '../../../storage/repositories.js'
import { generateUsageRecordId } from '../../../storage/usage-record-shards.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import { estimateJsonLikeBytes } from '../../../shared/queue-size.js'
import { fixedRetryPolicy, retryDelayMs } from '../../../shared/retry-policy.js'
import { sendUsageRecordsToWorker } from '../../background/background-ipc.js'

const usageRecordFlushIntervalMs = 500
const usageRecordRetryPolicy = fixedRetryPolicy('usage_record_queue_flush', 1000)
const usageRecordBatchSize = 1000
const usageRecordFlushBatchMaxBytes = 8 * 1024 * 1024
const usageRecordShutdownFlushMaxBatches = 1
const usageRecordQueueMaxItems = 10_000
const usageRecordQueueMaxBytes = 64 * 1024 * 1024
const usageRecordEstimateMaxBytes = usageRecordQueueMaxBytes + 1

interface QueuedUsageRecord {
  input: UsageRecordInput
  bytes: number
}

let pendingUsageRecords: QueuedUsageRecord[] = []
let pendingUsageRecordBytes = 0
let flushTimer: NodeJS.Timeout | undefined
let flushing = false
let retainedOverflowWarningCount = 0
let flushFailureCount = 0
let droppedDispatchCount = 0
let droppedOverflowCount = 0
let droppedOversizeCount = 0
let shutdownHooksInstalled = false

interface UsageRecordFlushOptions {
  drain?: boolean
  retryOnFailure?: boolean
  maxBatches?: number
}

export function enqueueUsageRecord(input: UsageRecordInput): void {
  const queuedInput = normalizeUsageRecordInput(input)
  if (runtimeConfig.processRole === 'server') {
    if (!sendUsageRecordsToWorker([queuedInput])) {
      recordUsageRecordDispatchFailure(new Error('后台 worker IPC 不可用'), queuedInput)
    }
    return
  }

  enqueueUsageRecordLocal(queuedInput)
}

export function enqueueUsageRecordsLocal(inputs: UsageRecordInput[]): void {
  assertLocalUsageRecordWriteAllowed('enqueueUsageRecordsLocal')
  for (const input of inputs) {
    enqueueUsageRecordLocal(normalizeUsageRecordInput(input))
  }
}

function enqueueUsageRecordLocal(input: UsageRecordInput): void {
  assertLocalUsageRecordWriteAllowed('enqueueUsageRecordLocal')
  const queued = {
    input,
    bytes: estimateUsageRecordBytes(input)
  }
  if (queued.bytes > usageRecordQueueMaxBytes) {
    recordUsageRecordLocalDrop(queued, 'oversize')
    return
  }
  if (pendingUsageRecords.length >= usageRecordQueueMaxItems || pendingUsageRecordBytes + queued.bytes > usageRecordQueueMaxBytes) {
    recordUsageRecordLocalDrop(queued, 'overflow')
    return
  }

  pendingUsageRecords.push(queued)
  pendingUsageRecordBytes += queued.bytes
  scheduleUsageRecordFlush(pendingUsageRecords.length >= usageRecordBatchSize ? 0 : usageRecordFlushIntervalMs)
}

export function flushUsageRecordQueue(options: UsageRecordFlushOptions = {}): void {
  if (runtimeConfig.processRole === 'server') {
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
        createUsageRecordsBatch(batch.map((item) => item.input))
        removeUsageRecordFlushBatch(batch.length, batchBytes)
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

export function flushUsageRecordQueueForShutdown(): void {
  flushUsageRecordQueue({ drain: true, retryOnFailure: false, maxBatches: usageRecordShutdownFlushMaxBatches })
}

export function getUsageRecordQueueRuntime(): {
  queueLength: number
  queueBytes: number
  droppedCount: number
  retainedOverflowWarningCount: number
  droppedOverflowCount: number
  droppedOversizeCount: number
  flushFailureCount: number
} {
  return {
    queueLength: pendingUsageRecords.length,
    queueBytes: pendingUsageRecordBytes,
    droppedCount: droppedDispatchCount + droppedOverflowCount + droppedOversizeCount,
    retainedOverflowWarningCount,
    droppedOverflowCount,
    droppedOversizeCount,
    flushFailureCount
  }
}

export function installUsageRecordQueueShutdownHooks(): void {
  if (shutdownHooksInstalled) {
    return
  }
  shutdownHooksInstalled = true

  process.once('beforeExit', flushUsageRecordQueueForShutdown)
}

function scheduleUsageRecordFlush(delayMs: number): void {
  if (runtimeConfig.processRole === 'server') {
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
  }), '使用记录投递后台 worker 失败，已跳过投递')
}

function normalizeUsageRecordInput(input: UsageRecordInput): UsageRecordInput {
  const createdAt = input.createdAt ?? nowIso()
  return {
    ...input,
    id: input.id ?? generateUsageRecordId(createdAt, randomUUID()),
    errorCode: sanitizeUsageRecordErrorMessage(input.errorCode),
    errorMessage: sanitizeUsageRecordErrorMessage(input.errorMessage),
    requestSnapshot: sanitizeUsageRecordSnapshot(input.requestSnapshot),
    responseSnapshot: sanitizeUsageRecordSnapshot(input.responseSnapshot),
    createdAt
  }
}

export function pendingUsageRecordCount(): number {
  return pendingUsageRecords.length
}

export function clearUsageRecordQueueForTest(): void {
  if (flushTimer) {
    clearTimeout(flushTimer)
    flushTimer = undefined
  }
  pendingUsageRecords = []
  pendingUsageRecordBytes = 0
  flushing = false
  retainedOverflowWarningCount = 0
  flushFailureCount = 0
  droppedDispatchCount = 0
  droppedOverflowCount = 0
  droppedOversizeCount = 0
  shutdownHooksInstalled = false
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
  return value
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
}

function estimateUsageRecordBytes(input: UsageRecordInput): number {
  return estimateJsonLikeBytes(input, { maxBytes: usageRecordEstimateMaxBytes }) + 256
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
  if (runtimeConfig.processRole === 'server') {
    throw new Error(`server 角色禁止直接同步写入 SQLite：${operation} 必须投递 background worker`)
  }
}
