import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import { nowIso } from '../../storage/database.js'
import { createUsageRecordsBatch, type UsageRecordInput } from '../../storage/repositories.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { sanitizeUrlForLog } from '../../shared/request-context.js'
import { fixedRetryPolicy, retryDelayMs } from '../../shared/retry-policy.js'
import { sendUsageRecordsToWorker } from '../background/background-ipc.js'
import { isSensitiveHeaderName } from './openai-gateway-usage.js'

const usageRecordFlushIntervalMs = 100
const usageRecordRetryPolicy = fixedRetryPolicy('usage_record_queue_flush', 1000)
const usageRecordBatchSize = 200
const usageRecordMaxPending = 10000

let pendingUsageRecords: UsageRecordInput[] = []
let flushTimer: NodeJS.Timeout | undefined
let flushing = false
let retainedOverflowWarningCount = 0
let flushFailureCount = 0
let droppedDispatchCount = 0
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
      recordUsageRecordDispatchFailure(new Error('后台 worker IPC 队列已满或不可用'), queuedInput)
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
  pendingUsageRecords.push(input)
  if (pendingUsageRecords.length > usageRecordMaxPending) {
    const overflowCount = pendingUsageRecords.length - usageRecordMaxPending
    retainedOverflowWarningCount += 1
    logger.warn({
      event: 'usage_record_queue_soft_limit_exceeded',
      overflowCount,
      retainedOverflowWarningCount,
      pendingCount: pendingUsageRecords.length
    }, '使用记录队列超过软上限，已保留待写入记录并触发立即落库')
    flushUsageRecordQueue({ drain: true, maxBatches: 5 })
  }
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
      const batch = pendingUsageRecords.splice(0, usageRecordBatchSize)
      if (batch.length === 0) {
        break
      }
      flushedBatches += 1

      try {
        createUsageRecordsBatch(batch)
      } catch (error) {
        failed = true
        pendingUsageRecords = [...batch, ...pendingUsageRecords]
        flushFailureCount += 1
        logger.error(errorLogFields(error, {
          event: 'usage_record_queue_flush_failed',
          batchSize: batch.length,
          pendingCount: pendingUsageRecords.length,
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

export function getUsageRecordQueueRuntime(): {
  queueLength: number
  droppedCount: number
  retainedOverflowWarningCount: number
  flushFailureCount: number
} {
  return {
    queueLength: pendingUsageRecords.length,
    droppedCount: droppedDispatchCount,
    retainedOverflowWarningCount,
    flushFailureCount
  }
}

export function installUsageRecordQueueShutdownHooks(): void {
  if (shutdownHooksInstalled) {
    return
  }
  shutdownHooksInstalled = true

  process.once('beforeExit', flushAllUsageRecordQueue)
  process.once('exit', flushAllUsageRecordQueue)

  process.once('SIGINT', () => exitAfterUsageRecordFlush(0))
  process.once('SIGTERM', () => exitAfterUsageRecordFlush(0))
}

function exitAfterUsageRecordFlush(exitCode: number): never {
  flushAllUsageRecordQueue()
  process.exit(exitCode)
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
  return {
    ...input,
    id: input.id ?? `usage_${Date.now()}_${randomUUID()}`,
    requestSnapshot: sanitizeUsageRecordSnapshot(input.requestSnapshot),
    responseSnapshot: sanitizeUsageRecordSnapshot(input.responseSnapshot),
    createdAt: input.createdAt ?? nowIso()
  }
}

export function pendingUsageRecordCount(): number {
  return pendingUsageRecords.length
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
  const entries = Object.entries(value as Record<string, unknown>)
  for (let index = 0; index < entries.length && index < usageSnapshotMaxObjectKeys; index += 1) {
    const [key, item] = entries[index]
    context.bytes += Buffer.byteLength(key, 'utf8') + 4
    context.depth += 1
    output[key] = sanitizeSnapshotValue(sanitizeSnapshotField(key, item), context)
    context.depth -= 1
    if (context.bytes >= usageSnapshotMaxBytes) break
  }
  if (entries.length > Object.keys(output).length || context.truncated) {
    output._truncated = true
  }
  return output
}

function sanitizeSnapshotString(value: string, context: SnapshotSanitizeContext): string {
  const bytes = Buffer.byteLength(value, 'utf8')
  const remaining = Math.max(0, usageSnapshotMaxBytes - context.bytes)
  const limit = Math.min(usageSnapshotMaxStringBytes, remaining)
  if (bytes <= limit) {
    context.bytes += bytes
    return value
  }
  context.truncated = true
  const suffix = `...[truncated ${bytes - limit} bytes]`
  const prefixBytes = Math.max(0, limit - Buffer.byteLength(suffix, 'utf8'))
  const truncated = value.slice(0, prefixBytes)
  context.bytes += Buffer.byteLength(truncated, 'utf8') + Buffer.byteLength(suffix, 'utf8')
  return `${truncated}${suffix}`
}

function sanitizeSnapshotField(key: string, value: unknown): unknown {
  if (isSensitiveHeaderName(key) || isSensitiveSnapshotFieldName(key)) {
    return redactedSnapshotSensitiveValue(value)
  }
  if (isUrlSnapshotFieldName(key) && typeof value === 'string') {
    return sanitizeUrlForLog(value)
  }
  return value
}

function isSensitiveSnapshotFieldName(name: string): boolean {
  const compact = name.trim().toLowerCase().replace(/[\s_-]+/g, '')
  return sensitiveSnapshotFieldNames.has(compact)
}

function isUrlSnapshotFieldName(name: string): boolean {
  const compact = name.trim().toLowerCase().replace(/[\s_-]+/g, '')
  return compact === 'originalurl' || compact === 'upstreamurl' || compact === 'url'
}

function redactedSnapshotSensitiveValue(value: unknown): string | string[] {
  if (Array.isArray(value)) {
    return value.map(() => '[redacted]')
  }
  return '[redacted]'
}

const sensitiveSnapshotFieldNames = new Set([
  'authorization',
  'proxyauthorization',
  'cookie',
  'setcookie',
  'apikey',
  'openaiapikey',
  'password',
  'secret',
  'token',
  'accesstoken',
  'refreshtoken',
  'session'
])

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

function normalizeMaxBatches(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : Number.POSITIVE_INFINITY
}

function assertLocalUsageRecordWriteAllowed(operation: string): void {
  if (runtimeConfig.processRole === 'server') {
    throw new Error(`server 角色禁止直接同步写入 SQLite：${operation} 必须投递 background worker`)
  }
}
