import { createHash } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import { nowIso } from '../../storage/database.js'
import {
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
const runtimeLogMaxRawJsonChars = 128 * 1024
const runtimeLogOverflowWarningIntervalMs = 5000

let pendingRuntimeLogs: RuntimeLogIndexInput[] = []
let flushTimer: NodeJS.Timeout | undefined
let flushTimerDelayMs: number | undefined
let flushing = false
let droppedRuntimeLogCount = 0
let lastOverflowWarningAtMs = 0
let suppressedOverflowWarningCount = 0
let flushLastSuccessAt: string | undefined
let flushLastError: string | undefined
let shutdownHooksInstalled = false

interface RuntimeLogFlushOptions {
  drain?: boolean
  retryOnFailure?: boolean
}

export interface RuntimeLogLineIndexOptions {
  sourceKey?: string
  logFile?: string
  logOffset?: number
  lineNumber?: number
}

export interface RuntimeLogIndexRuntime {
  queueLength: number
  droppedCount: number
  flushLastSuccessAt?: string
  flushLastError?: string
  retentionDays: number
}

export function enqueueRuntimeLogLine(rawLine: string, options: RuntimeLogLineIndexOptions = {}): void {
  if (runtimeConfig.processRole === 'server' && sendRuntimeLogLineToWorker(rawLine, options)) {
    return
  }

  enqueueRuntimeLogLineLocal(rawLine, options)
}

export function enqueueRuntimeLogLineLocal(rawLine: string, options: RuntimeLogLineIndexOptions = {}): void {
  const input = runtimeLogInputFromLine(rawLine, options)
  if (!input) return

  pendingRuntimeLogs.push(input)
  if (pendingRuntimeLogs.length > runtimeLogMaxPending) {
    const overflowCount = pendingRuntimeLogs.length - runtimeLogMaxPending
    pendingRuntimeLogs.splice(0, overflowCount)
    recordRuntimeLogOverflow(overflowCount)
  }

  scheduleRuntimeLogFlush(pendingRuntimeLogs.length >= runtimeLogBatchSize ? 0 : runtimeLogFlushIntervalMs)
}

export function flushRuntimeLogIndexQueue(options: RuntimeLogFlushOptions = {}): void {
  if (flushing || pendingRuntimeLogs.length === 0) {
    return
  }

  if (flushTimer) {
    clearTimeout(flushTimer)
    flushTimer = undefined
    flushTimerDelayMs = undefined
  }

  flushing = true
  let shouldRetry = false
  let failed = false
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
        failed = true
        pendingRuntimeLogs = [...batch, ...pendingRuntimeLogs]
        if (pendingRuntimeLogs.length > runtimeLogMaxPending) {
          const overflowCount = pendingRuntimeLogs.length - runtimeLogMaxPending
          pendingRuntimeLogs.splice(runtimeLogMaxPending, overflowCount)
          recordRuntimeLogOverflow(overflowCount)
        }
        flushLastError = error instanceof Error ? error.message : String(error)
        writeRuntimeLogIndexError(`运行日志索引写入失败：${flushLastError}`)
        shouldRetry = options.retryOnFailure !== false
        break
      }
    } while (options.drain && pendingRuntimeLogs.length > 0)
  } finally {
    flushing = false
  }

  if (pendingRuntimeLogs.length > 0 && (!failed || shouldRetry)) {
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

function recordRuntimeLogOverflow(overflowCount: number): void {
  droppedRuntimeLogCount += overflowCount

  const nowMs = Date.now()
  if (nowMs - lastOverflowWarningAtMs < runtimeLogOverflowWarningIntervalMs) {
    suppressedOverflowWarningCount += overflowCount
    return
  }

  const warningCount = overflowCount + suppressedOverflowWarningCount
  const suppressedText = suppressedOverflowWarningCount > 0
    ? `，含前序未重复提示 ${suppressedOverflowWarningCount} 条`
    : ''
  suppressedOverflowWarningCount = 0
  lastOverflowWarningAtMs = nowMs
  writeRuntimeLogIndexError(`运行日志索引队列已满，累计丢弃 ${warningCount} 条${suppressedText}`)
}
