import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import { nowIso } from '../../storage/database.js'
import { createUsageRecordsBatch, type UsageRecordInput } from '../../storage/repositories.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { sendUsageRecordsToWorker } from '../background/background-ipc.js'

const usageRecordFlushIntervalMs = 100
const usageRecordRetryDelayMs = 1000
const usageRecordBatchSize = 200
const usageRecordMaxPending = 10000

let pendingUsageRecords: UsageRecordInput[] = []
let flushTimer: NodeJS.Timeout | undefined
let flushing = false
let retainedOverflowWarningCount = 0
let flushFailureCount = 0
let shutdownHooksInstalled = false

interface UsageRecordFlushOptions {
  drain?: boolean
  retryOnFailure?: boolean
  maxBatches?: number
}

export function enqueueUsageRecord(input: UsageRecordInput): void {
  const queuedInput = normalizeUsageRecordInput(input)
  if (runtimeConfig.processRole === 'server' && sendUsageRecordsToWorker([queuedInput])) {
    return
  }

  enqueueUsageRecordLocal(queuedInput)
}

export function enqueueUsageRecordsLocal(inputs: UsageRecordInput[]): void {
  for (const input of inputs) {
    enqueueUsageRecordLocal(normalizeUsageRecordInput(input))
  }
}

function enqueueUsageRecordLocal(input: UsageRecordInput): void {
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
  if (flushing || pendingUsageRecords.length === 0) {
    return
  }

  if (flushTimer) {
    clearTimeout(flushTimer)
    flushTimer = undefined
  }

  flushing = true
  let shouldRetry = false
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

  if (pendingUsageRecords.length > 0) {
    scheduleUsageRecordFlush(shouldRetry ? usageRecordRetryDelayMs : 0)
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
    droppedCount: 0,
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
  if (flushTimer || flushing) {
    return
  }
  flushTimer = setTimeout(() => {
    flushTimer = undefined
    flushUsageRecordQueue()
  }, delayMs)
}

function normalizeUsageRecordInput(input: UsageRecordInput): UsageRecordInput {
  return {
    ...input,
    id: input.id ?? `usage_${Date.now()}_${randomUUID()}`,
    createdAt: input.createdAt ?? nowIso()
  }
}

function normalizeMaxBatches(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : Number.POSITIVE_INFINITY
}
