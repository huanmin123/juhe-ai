import { createHash } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import { nowIso } from '../../storage/database.js'
import {
  cleanupRuntimeLogIndex,
  createRuntimeLogsBatch,
  runtimeLogIndexRetentionDays,
  type RuntimeLogIndexInput,
  type RuntimeLogLevel
} from '../../storage/runtime-logs.repository.js'
import { sendRuntimeLogLineToWorker } from '../background/background-ipc.js'

const runtimeLogFlushIntervalMs = 200
const runtimeLogRetryDelayMs = 1000
const runtimeLogBatchSize = 500
const runtimeLogMaxPending = 20000
const runtimeLogCleanupIntervalMs = 60 * 60 * 1000
const runtimeLogMaxRawJsonChars = 128 * 1024

let pendingRuntimeLogs: RuntimeLogIndexInput[] = []
let flushTimer: NodeJS.Timeout | undefined
let flushing = false
let droppedRuntimeLogCount = 0
let flushLastSuccessAt: string | undefined
let flushLastError: string | undefined
let lastCleanupAtMs = 0
let shutdownHooksInstalled = false

interface RuntimeLogFlushOptions {
  drain?: boolean
  retryOnFailure?: boolean
}

export interface RuntimeLogIndexRuntime {
  queueLength: number
  droppedCount: number
  flushLastSuccessAt?: string
  flushLastError?: string
  retentionDays: number
}

export function enqueueRuntimeLogLine(rawLine: string, options: { sourceKey?: string } = {}): void {
  if (runtimeConfig.processRole === 'server' && sendRuntimeLogLineToWorker(rawLine)) {
    return
  }

  enqueueRuntimeLogLineLocal(rawLine, options)
}

export function enqueueRuntimeLogLineLocal(rawLine: string, options: { sourceKey?: string } = {}): void {
  const input = runtimeLogInputFromLine(rawLine, options.sourceKey)
  if (!input) return

  pendingRuntimeLogs.push(input)
  if (pendingRuntimeLogs.length > runtimeLogMaxPending) {
    const overflowCount = pendingRuntimeLogs.length - runtimeLogMaxPending
    pendingRuntimeLogs.splice(0, overflowCount)
    droppedRuntimeLogCount += overflowCount
    writeRuntimeLogIndexError(`运行日志索引队列已满，丢弃 ${overflowCount} 条`)
  }

  scheduleRuntimeLogFlush(pendingRuntimeLogs.length >= runtimeLogBatchSize ? 0 : runtimeLogFlushIntervalMs)
}

export function flushRuntimeLogIndexQueue(options: RuntimeLogFlushOptions = {}): void {
  if (flushing || pendingRuntimeLogs.length === 0) {
    maybeCleanupRuntimeLogIndex()
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
      const batch = pendingRuntimeLogs.splice(0, runtimeLogBatchSize)
      if (batch.length === 0) {
        break
      }

      try {
        createRuntimeLogsBatch(batch)
        flushLastSuccessAt = nowIso()
        flushLastError = undefined
      } catch (error) {
        pendingRuntimeLogs = [...batch, ...pendingRuntimeLogs].slice(0, runtimeLogMaxPending)
        flushLastError = error instanceof Error ? error.message : String(error)
        writeRuntimeLogIndexError(`运行日志索引写入失败：${flushLastError}`)
        shouldRetry = options.retryOnFailure !== false
        break
      }
    } while (options.drain && pendingRuntimeLogs.length > 0)
  } finally {
    flushing = false
  }

  maybeCleanupRuntimeLogIndex()

  if (pendingRuntimeLogs.length > 0) {
    scheduleRuntimeLogFlush(shouldRetry ? runtimeLogRetryDelayMs : 0)
  }
}

export function flushAllRuntimeLogIndexQueue(): void {
  flushRuntimeLogIndexQueue({ drain: true, retryOnFailure: false })
}

export function getRuntimeLogIndexRuntime(): RuntimeLogIndexRuntime {
  return {
    queueLength: pendingRuntimeLogs.length,
    droppedCount: droppedRuntimeLogCount,
    flushLastSuccessAt,
    flushLastError,
    retentionDays: runtimeLogIndexRetentionDays
  }
}

export function installRuntimeLogIndexQueueShutdownHooks(): void {
  if (shutdownHooksInstalled) {
    return
  }
  shutdownHooksInstalled = true

  process.once('beforeExit', flushAllRuntimeLogIndexQueue)
  process.once('exit', flushAllRuntimeLogIndexQueue)
}

function runtimeLogInputFromLine(rawLine: string, sourceKey?: string): RuntimeLogIndexInput | undefined {
  const line = rawLine.trim()
  if (!line) return undefined

  let parsed: Record<string, unknown>
  try {
    const value = JSON.parse(line) as unknown
    if (typeof value !== 'object' || value === null || Array.isArray(value)) {
      return undefined
    }
    parsed = value as Record<string, unknown>
  } catch {
    return undefined
  }

  const time = stringValue(parsed.time) ?? nowIso()
  return {
    id: stableRuntimeLogId(sourceKey ?? line),
    time,
    level: normalizeLevel(parsed.level),
    traceId: stringValue(parsed.traceId),
    event: stringValue(parsed.event),
    message: stringValue(parsed.msg) ?? stringValue(parsed.message),
    errorMessage: stringValue(parsed.errorMessage) ?? errorMessageFromErr(parsed.err),
    rawJson: truncateRawJson(line),
    createdAt: time
  }
}

function stableRuntimeLogId(value: string): string {
  const digest = createHash('sha256').update(value).digest('hex')
  return `rtlog_${digest.slice(0, 32)}`
}

function normalizeLevel(value: unknown): RuntimeLogLevel | string {
  if (typeof value === 'string' && value.trim()) {
    return value.trim().toLowerCase()
  }
  if (typeof value !== 'number') return 'info'
  if (value >= 60) return 'fatal'
  if (value >= 50) return 'error'
  if (value >= 40) return 'warn'
  if (value >= 30) return 'info'
  if (value >= 20) return 'debug'
  return 'trace'
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function errorMessageFromErr(value: unknown): string | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return undefined
  }
  const record = value as Record<string, unknown>
  return stringValue(record.message)
}

function truncateRawJson(value: string): string {
  if (value.length <= runtimeLogMaxRawJsonChars) return value
  return `${value.slice(0, runtimeLogMaxRawJsonChars)}...[truncated]`
}

function maybeCleanupRuntimeLogIndex(): void {
  const now = Date.now()
  if (now - lastCleanupAtMs < runtimeLogCleanupIntervalMs) {
    return
  }
  lastCleanupAtMs = now
  try {
    cleanupRuntimeLogIndex()
  } catch (error) {
    flushLastError = error instanceof Error ? error.message : String(error)
    writeRuntimeLogIndexError(`运行日志索引清理失败：${flushLastError}`)
  }
}

function scheduleRuntimeLogFlush(delayMs: number): void {
  if (flushTimer || flushing) {
    return
  }
  flushTimer = setTimeout(() => {
    flushTimer = undefined
    flushRuntimeLogIndexQueue()
  }, delayMs)
  flushTimer.unref()
}

function writeRuntimeLogIndexError(message: string): void {
  process.stderr.write(`[runtime-log-index] ${message}\n`)
}
