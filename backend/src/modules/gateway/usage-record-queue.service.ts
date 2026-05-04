import { nowIso } from '../../storage/database.js'
import { createUsageRecordsBatch, type UsageRecordInput } from '../../storage/repositories.js'

const usageRecordFlushIntervalMs = 100
const usageRecordRetryDelayMs = 1000
const usageRecordBatchSize = 200
const usageRecordMaxPending = 10000

let pendingUsageRecords: UsageRecordInput[] = []
let flushTimer: NodeJS.Timeout | undefined
let flushing = false
let droppedUsageRecordCount = 0
let shutdownHooksInstalled = false

interface UsageRecordFlushOptions {
  drain?: boolean
  retryOnFailure?: boolean
}

export function enqueueUsageRecord(input: UsageRecordInput): void {
  pendingUsageRecords.push({ ...input, createdAt: input.createdAt ?? nowIso() })
  if (pendingUsageRecords.length > usageRecordMaxPending) {
    const overflowCount = pendingUsageRecords.length - usageRecordMaxPending
    pendingUsageRecords.splice(0, overflowCount)
    droppedUsageRecordCount += overflowCount
    console.error(`[gateway] usage record queue overflow, dropped ${overflowCount} records; ${droppedUsageRecordCount} dropped in total`)
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
  try {
    do {
      const batch = pendingUsageRecords.splice(0, usageRecordBatchSize)
      if (batch.length === 0) {
        break
      }

      try {
        createUsageRecordsBatch(batch)
      } catch (error) {
        pendingUsageRecords = [...batch, ...pendingUsageRecords].slice(0, usageRecordMaxPending)
        console.error('[gateway] usage record queue flush failed', error)
        shouldRetry = options.retryOnFailure !== false
        break
      }
    } while (options.drain && pendingUsageRecords.length > 0)
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
