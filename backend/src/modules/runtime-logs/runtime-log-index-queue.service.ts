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
const runtimeLogBatchMaxBytes = 512 * 1024
const runtimeLogDefaultFlushMaxBatches = 1
const runtimeLogShutdownFlushMaxBatches = 1
const runtimeLogMaxRawJsonChars = 128 * 1024
const runtimeLogQueueMaxItems = 5_000
const runtimeLogQueueMaxBytes = 32 * 1024 * 1024
const runtimeLogQueueSampleDropItemHighWater = Math.floor(runtimeLogQueueMaxItems * 0.8)
const runtimeLogQueueSampleDropByteHighWater = Math.floor(runtimeLogQueueMaxBytes * 0.8)

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
let droppedRuntimeLogSampledCount = 0
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
  droppedSampledCount: number
  flushLastSuccessAt?: string
  flushLastError?: string
  retentionDays: number
}

export function enqueueRuntimeLogLine(rawLine: string, options: RuntimeLogLineIndexOptions = {}): void {
  if (runtimeConfig.processRole === 'db-service') {
    sendRuntimeLogLineFromDbServiceToServer(rawLine, options)
    return
  }
  if (shouldDispatchRuntimeLogToIngestWorker()) {
    sendRuntimeLogLineToWorker(rawLine, options)
    return
  }

  enqueueRuntimeLogLineLocal(rawLine, options)
}

export function enqueueRuntimeLogLineLocal(rawLine: string, options: RuntimeLogLineIndexOptions = {}): void {
  assertLocalRuntimeLogIndexAllowed('enqueueRuntimeLogLineLocal')
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
  if (shouldSampleDropRuntimeLog(queued)) {
    recordRuntimeLogDrop(queued, 'sampled')
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
  if (!isRuntimeLogIngestWorker()) {
    return true
  }
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
      const batch = peekRuntimeLogFlushBatch()
      if (batch.length === 0) {
        break
      }
      flushedBatches += 1
      const batchBytes = sumQueuedRuntimeLogBytes(batch)

      try {
        createRuntimeLogsBatch(batch.map((item) => item.input))
        removeRuntimeLogFlushBatch(batch, batchBytes)
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
  return flushRuntimeLogIndexQueue({ drain: true, retryOnFailure: false, maxBatches: Number.POSITIVE_INFINITY })
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
    droppedSampledCount: droppedRuntimeLogSampledCount,
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
  droppedRuntimeLogSampledCount = 0
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
  if (value === Number.POSITIVE_INFINITY) return Number.POSITIVE_INFINITY
  return typeof value === 'number' && Number.isFinite(value)
    ? Math.max(1, Math.trunc(value))
    : runtimeLogDefaultFlushMaxBatches
}

function scheduleRuntimeLogFlush(delayMs: number): void {
  if (!isRuntimeLogIngestWorker()) {
    return
  }
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

function sendRuntimeLogLineFromDbServiceToServer(rawLine: string, options: RuntimeLogLineIndexOptions): void {
  if (!process.send || process.connected === false) {
    writeRuntimeLogIndexError('DB service 无法投递运行日志索引到 ingest-worker，已丢弃日志行')
    return
  }
  try {
    process.send({
      type: 'background_worker_runtime_log_line',
      line: rawLine,
      sourceKey: options.sourceKey,
      logFile: options.logFile,
      logOffset: options.logOffset,
      lineNumber: options.lineNumber
    }, (error) => {
      if (error) {
        writeRuntimeLogIndexError(`DB service 投递运行日志索引到 ingest-worker 失败：${error.message}`)
      }
    })
  } catch (error) {
    writeRuntimeLogIndexError(`DB service 投递运行日志索引到 ingest-worker 失败：${error instanceof Error ? error.message : String(error)}`)
  }
}

function assertLocalRuntimeLogIndexAllowed(operation: string): void {
  if (!isRuntimeLogIngestWorker()) {
    throw new Error(`${runtimeConfig.processRole}/${runtimeConfig.workerRole} 角色禁止直接写入运行日志索引：${operation} 必须投递 ingest-worker`)
  }
}

function isRuntimeLogIngestWorker(): boolean {
  return runtimeConfig.processRole === 'worker' && runtimeConfig.workerRole === 'ingest-worker'
}

function shouldDispatchRuntimeLogToIngestWorker(): boolean {
  return runtimeConfig.processRole === 'server'
    || (runtimeConfig.processRole === 'worker' && runtimeConfig.workerRole !== 'ingest-worker')
}

function estimateRuntimeLogBytes(input: RuntimeLogIndexInput): number {
  return estimateJsonLikeBytes(input) + 256
}

function peekRuntimeLogFlushBatch(): QueuedRuntimeLog[] {
  const batch: QueuedRuntimeLog[] = []
  let batchBytes = 0
  for (const item of pendingRuntimeLogs) {
    if (batch.length >= runtimeLogBatchSize) {
      break
    }
    if (batch.length > 0 && batchBytes + item.bytes > runtimeLogBatchMaxBytes) {
      break
    }
    batch.push(item)
    batchBytes += item.bytes
    if (batchBytes >= runtimeLogBatchMaxBytes) {
      break
    }
  }
  return batch
}

function removeRuntimeLogFlushBatch(batch: QueuedRuntimeLog[], batchBytes: number): void {
  pendingRuntimeLogs.splice(0, batch.length)
  pendingRuntimeLogBytes = Math.max(0, pendingRuntimeLogBytes - batchBytes)
}

function sumQueuedRuntimeLogBytes(items: QueuedRuntimeLog[]): number {
  return items.reduce((sum, item) => sum + item.bytes, 0)
}

function shouldSampleDropRuntimeLog(item: QueuedRuntimeLog): boolean {
  if (!isLowPriorityRuntimeLogLevel(item.input.level)) {
    return false
  }
  return pendingRuntimeLogs.length >= runtimeLogQueueSampleDropItemHighWater
    || pendingRuntimeLogBytes + item.bytes >= runtimeLogQueueSampleDropByteHighWater
}

function isLowPriorityRuntimeLogLevel(level: RuntimeLogLevel | string): boolean {
  const normalized = String(level || '').toLowerCase()
  return normalized === 'trace' || normalized === 'debug' || normalized === 'info'
}

function recordRuntimeLogDrop(item: QueuedRuntimeLog, reason: 'overflow' | 'oversize' | 'sampled'): void {
  droppedRuntimeLogCount += 1
  if (reason === 'overflow') {
    droppedRuntimeLogOverflowCount += 1
  } else if (reason === 'oversize') {
    droppedRuntimeLogOversizeCount += 1
  } else {
    droppedRuntimeLogSampledCount += 1
  }
  if (droppedRuntimeLogCount > 10 && droppedRuntimeLogCount % 100 !== 0) {
    return
  }
  writeRuntimeLogIndexError(`运行日志索引队列达到保护上限，已丢弃新日志：reason=${reason} bytes=${item.bytes} pending=${pendingRuntimeLogs.length}`)
}
