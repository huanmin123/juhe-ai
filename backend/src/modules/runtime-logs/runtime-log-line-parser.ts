import { createHash } from 'node:crypto'

import { nowIso } from '../../storage/database.js'
import type { RuntimeLogIndexInput, RuntimeLogLevel } from '../../storage/runtime-logs.repository.js'

export interface RuntimeLogLineIndexOptions {
  sourceKey?: string
  logFile?: string
  logOffset?: number
  lineNumber?: number
}

export function parseRuntimeLogLineForIndex(
  rawLine: string,
  options: RuntimeLogLineIndexOptions = {}
): RuntimeLogIndexInput | undefined {
  const line = rawLine.trim()
  if (!line) return undefined
  const rawJson = line
  const metadata = runtimeLogSourceMetadata(options)

  let parsed: Record<string, unknown>
  try {
    const value = JSON.parse(line) as unknown
    if (typeof value !== 'object' || value === null || Array.isArray(value)) {
      return fallbackRuntimeLogInput(rawJson, options.sourceKey ?? line, metadata, '运行日志行不是 JSON 对象')
    }
    parsed = value as Record<string, unknown>
  } catch {
    return fallbackRuntimeLogInput(rawJson, options.sourceKey ?? line, metadata, '运行日志行不是有效 JSON')
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

function fallbackRuntimeLogInput(
  rawJson: string,
  sourceKey: string,
  metadata: Pick<RuntimeLogIndexInput, 'logFile' | 'logOffset' | 'lineNumber'>,
  errorMessage: string
): RuntimeLogIndexInput {
  const createdAt = nowIso()
  return {
    id: stableRuntimeLogId(sourceKey),
    ...metadata,
    time: createdAt,
    level: 'warn',
    event: 'runtime_log_parse_failed',
    message: '运行日志文件包含无法解析的完整行',
    errorMessage,
    rawJson,
    createdAt
  }
}

function runtimeLogSourceMetadata(
  options: RuntimeLogLineIndexOptions
): Pick<RuntimeLogIndexInput, 'logFile' | 'logOffset' | 'lineNumber'> {
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
  if (typeof value === 'string' && value.trim()) return value.trim().toLowerCase()
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
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? Math.trunc(value) : undefined
}

function errorMessageFromErr(value: unknown): string | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined
  return stringValue((value as Record<string, unknown>).message)
}
