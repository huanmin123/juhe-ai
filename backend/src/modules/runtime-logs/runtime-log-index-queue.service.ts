import { createHash } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import { estimateJsonLikeBytes } from '../../shared/queue-size.js'
import { fixedRetryPolicy, retryDelayMs } from '../../shared/retry-policy.js'
import { nowIso } from '../../storage/database.js'
import {
  createRuntimeLogsBatch,
  runtimeLogIndexRetentionDays,
  type RuntimeLogIndexInput,
  type RuntimeLogLevel
} from '../../storage/runtime-logs.repository.js'
import { sendRuntimeLogLineToWorker } from '../background/background-ipc.js'

const runtimeLogFlushIntervalMs = 200
const runtimeLogRetryPolicy = fixedRetryPolicy('runtime_log_index_queue_flush', 1000)
const runtimeLogBatchSize = 500
const runtimeLogShutdownFlushMaxBatches = 1
const runtimeLogMaxRawJsonChars = 128 * 1024
const runtimeLogQueueMaxItems = 5_000
const runtimeLogQueueMaxBytes = 32 * 1024 * 1024

interface QueuedRuntimeLog {
  input: RuntimeLogIndexInput
  bytes: number
}

let pendingRuntimeLogs: QueuedRuntimeLog[] = []
let pendingRuntimeLogBytes = 0
let flushTimer: NodeJS.Timeout | undefined
let flushTimerDelayMs: number | undefined
let flushing = false
let droppedRuntimeLogCount = 0
let droppedRuntimeLogOverflowCount = 0
let droppedRuntimeLogOversizeCount = 0
let flushLastSuccessAt: string | undefined
let flushLastError: string | undefined
let shutdownHooksInstalled = false

interface RuntimeLogFlushOptions {
  drain?: boolean
  retryOnFailure?: boolean
  maxBatches?: number
}

export interface RuntimeLogLineIndexOptions {
  sourceKey?: string
  logFile?: string
  logOffset?: number
  lineNumber?: number
}

export interface RuntimeLogIndexRuntime {
  queueLength: number
  queueBytes: number
  droppedCount: number
  droppedOverflowCount: number
  droppedOversizeCount: number
  flushLastSuccessAt?: string
  flushLastError?: string
  retentionDays: number
}

export function enqueueRuntimeLogLine(rawLine: string, options: RuntimeLogLineIndexOptions = {}): void {
  if (runtimeConfig.processRole === 'server') {
    sendRuntimeLogLineToWorker(rawLine, options)
    return
  }

  enqueueRuntimeLogLineLocal(rawLine, options)
}

export function enqueueRuntimeLogLineLocal(rawLine: string, options: RuntimeLogLineIndexOptions = {}): void {
  const input = runtimeLogInputFromLine(rawLine, options)
  if (!input) return

  const queued = {
    input,
    bytes: estimateRuntimeLogBytes(input)
  }
  if (queued.bytes > runtimeLogQueueMaxBytes) {
    recordRuntimeLogDrop(queued, 'oversize')
    return
  }
  if (pendingRuntimeLogs.length >= runtimeLogQueueMaxItems || pendingRuntimeLogBytes + queued.bytes > runtimeLogQueueMaxBytes) {
    recordRuntimeLogDrop(queued, 'overflow')
    return
  }
  pendingRuntimeLogs.push(queued)
  pendingRuntimeLogBytes += queued.bytes

  scheduleRuntimeLogFlush(pendingRuntimeLogs.length >= runtimeLogBatchSize ? 0 : runtimeLogFlushIntervalMs)
}

export function flushRuntimeLogIndexQueue(options: RuntimeLogFlushOptions = {}): boolean {
  if (flushing || pendingRuntimeLogs.length === 0) {
    return true
  }

  if (flushTimer) {
    clearTimeout(flushTimer)
    flushTimer = undefined
    flushTimerDelayMs = undefined
  }

  flushing = true
  let shouldRetry = false
  let failed = false
  let flushedBatches = 0
  const maxBatches = normalizeMaxBatches(options.maxBatches)
  try {
    do {
      const batch = pendingRuntimeLogs.slice(0, runtimeLogBatchSize)
      if (batch.length === 0) {
        break
      }
      flushedBatches += 1
      const batchBytes = sumQueuedRuntimeLogBytes(batch)

      try {
        createRuntimeLogsBatch(batch.map((item) => item.input))
        pendingRuntimeLogs.splice(0, batch.length)
        pendingRuntimeLogBytes = Math.max(0, pendingRuntimeLogBytes - batchBytes)
        flushLastSuccessAt = nowIso()
        flushLastError = undefined
      } catch (error) {
        failed = true
        flushLastError = error instanceof Error ? error.message : String(error)
        writeRuntimeLogIndexError(`运行日志索引写入失败：${flushLastError}`)
        shouldRetry = options.retryOnFailure !== false
        break
      }
    } while (options.drain && pendingRuntimeLogs.length > 0 && flushedBatches < maxBatches)
  } finally {
    flushing = false
  }

  if (pendingRuntimeLogs.length > 0 && (!failed || shouldRetry)) {
    scheduleRuntimeLogFlush(shouldRetry ? retryDelayMs(runtimeLogRetryPolicy) : 0)
  }
  return !failed
}

export function flushAllRuntimeLogIndexQueue(): boolean {
  return flushRuntimeLogIndexQueue({ drain: true, retryOnFailure: false })
}

export function flushRuntimeLogIndexQueueForShutdown(): boolean {
  return flushRuntimeLogIndexQueue({ drain: true, retryOnFailure: false, maxBatches: runtimeLogShutdownFlushMaxBatches })
}

export function getRuntimeLogIndexRuntime(): RuntimeLogIndexRuntime {
  return {
    queueLength: pendingRuntimeLogs.length,
    queueBytes: pendingRuntimeLogBytes,
    droppedCount: droppedRuntimeLogCount,
    droppedOverflowCount: droppedRuntimeLogOverflowCount,
    droppedOversizeCount: droppedRuntimeLogOversizeCount,
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

  process.once('beforeExit', flushRuntimeLogIndexQueueForShutdown)
}

export function clearRuntimeLogIndexQueueForTest(): void {
  if (flushTimer) {
    clearTimeout(flushTimer)
    flushTimer = undefined
    flushTimerDelayMs = undefined
  }
  pendingRuntimeLogs = []
  pendingRuntimeLogBytes = 0
  flushing = false
  droppedRuntimeLogCount = 0
  droppedRuntimeLogOverflowCount = 0
  droppedRuntimeLogOversizeCount = 0
  flushLastSuccessAt = undefined
  flushLastError = undefined
  shutdownHooksInstalled = false
}

function runtimeLogInputFromLine(rawLine: string, options: RuntimeLogLineIndexOptions = {}): RuntimeLogIndexInput | undefined {
  const line = rawLine.trim()
  if (!line) return undefined
  const rawJson = truncateRawJson(line)
  const metadata = runtimeLogSourceMetadata(options)

  if (line.length > runtimeLogMaxRawJsonChars) {
    return fallbackRuntimeLogInput(rawJson, options.sourceKey ?? stableRuntimeLogSource(line, rawJson), metadata)
  }

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
    id: stableRuntimeLogId(options.sourceKey ?? line),
    ...metadata,
    time,
    level: normalizeLevel(parsed.level),
    traceId: stringValue(parsed.traceId),
    event: stringValue(parsed.event),
    message: stringValue(parsed.msg) ?? stringValue(parsed.message),
    errorMessage: stringValue(parsed.errorMessage) ?? errorMessageFromErr(parsed.err),
    rawJson,
    createdAt: time
  }
}

function fallbackRuntimeLogInput(rawJson: string, sourceKey: string, metadata: Pick<RuntimeLogIndexInput, 'logFile' | 'logOffset' | 'lineNumber'>): RuntimeLogIndexInput {
  const createdAt = nowIso()
  return {
    id: stableRuntimeLogId(sourceKey),
    ...metadata,
    time: createdAt,
    level: 'info',
    rawJson,
    createdAt
  }
}

function stableRuntimeLogSource(line: string, rawJson: string): string {
  return line.length > runtimeLogMaxRawJsonChars
    ? `${line.length}:${rawJson}`
    : line
}

function runtimeLogSourceMetadata(options: RuntimeLogLineIndexOptions): Pick<RuntimeLogIndexInput, 'logFile' | 'logOffset' | 'lineNumber'> {
  return {
    logFile: stringValue(options.logFile),
    logOffset: positiveIntegerOrUndefined(options.logOffset),
    lineNumber: positiveIntegerOrUndefined(options.lineNumber)
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

function positiveIntegerOrUndefined(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
    ? Math.trunc(value)
    : undefined
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

function normalizeMaxBatches(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : Number.POSITIVE_INFINITY
}

function scheduleRuntimeLogFlush(delayMs: number): void {
  if (delayMs <= 0 && flushTimer && (flushTimerDelayMs ?? 0) > 0) {
    clearTimeout(flushTimer)
    flushTimer = undefined
    flushTimerDelayMs = undefined
  }
  if (flushTimer || flushing) {
    return
  }
  flushTimer = setTimeout(() => {
    flushTimer = undefined
    flushTimerDelayMs = undefined
    flushRuntimeLogIndexQueue()
  }, delayMs)
  flushTimerDelayMs = delayMs
  flushTimer.unref()
}

function writeRuntimeLogIndexError(message: string): void {
  process.stderr.write(`[runtime-log-index] ${message}\n`)
}

function estimateRuntimeLogBytes(input: RuntimeLogIndexInput): number {
  return estimateJsonLikeBytes(input) + 256
}

function sumQueuedRuntimeLogBytes(items: QueuedRuntimeLog[]): number {
  return items.reduce((sum, item) => sum + item.bytes, 0)
}

function recordRuntimeLogDrop(item: QueuedRuntimeLog, reason: 'overflow' | 'oversize'): void {
  droppedRuntimeLogCount += 1
  if (reason === 'overflow') {
    droppedRuntimeLogOverflowCount += 1
  } else {
    droppedRuntimeLogOversizeCount += 1
  }
  if (droppedRuntimeLogCount > 10 && droppedRuntimeLogCount % 100 !== 0) {
    return
  }
  writeRuntimeLogIndexError(`运行日志索引队列达到保护上限，已丢弃新日志：reason=${reason} bytes=${item.bytes} pending=${pendingRuntimeLogs.length}`)
}
