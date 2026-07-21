import { write, writeSync } from 'node:fs'

const defaultMaxBytes = 4096
const asyncDiagnosticMaxBytes = 32 * 1024

type AsyncDiagnosticWrite = (line: string, callback: (error?: Error | null) => void) => void

let asyncDiagnosticActive = false
let asyncDiagnosticPendingCritical: string | undefined
let asyncDiagnosticPendingGeneral: string | undefined
let asyncDiagnosticDropped = 0
let asyncDiagnosticDropsToReport = 0
let asyncDiagnosticWriter: AsyncDiagnosticWrite = defaultAsyncDiagnosticWrite
const asyncDiagnosticDrainWaiters = new Set<() => void>()

export interface ProcessFatalDiagnosticInput {
  event: string
  error: unknown
  processRole: string
  pid: number
  secrets?: string[]
  maxBytes?: number
}

export function serializeProcessFatalDiagnostic(input: ProcessFatalDiagnosticInput): string {
  const maxBytes = Math.max(256, Math.trunc(input.maxBytes ?? defaultMaxBytes))
  const error = normalizeError(input.error)
  const message = error.message
  const base = {
    event: boundedOriginalField(input.event, 128),
    processRole: boundedOriginalField(input.processRole, 128),
    pid: input.pid,
    errorName: boundedOriginalField(error.name, 256),
    errorCode: error.code ? boundedOriginalField(error.code, 256) : undefined
  }
  let low = 0
  let high = Math.min(message.length, maxBytes)
  let selected = ''
  while (low <= high) {
    const midpoint = Math.floor((low + high) / 2)
    const candidate = `${JSON.stringify({ ...base, message: truncateUtf8(message, midpoint) })}\n`
    if (Buffer.byteLength(candidate, 'utf8') <= maxBytes) {
      selected = candidate
      low = midpoint + 1
    } else {
      high = midpoint - 1
    }
  }
  if (selected) return selected
  const fallback = `${JSON.stringify({ event: 'process_fatal', message: '[TRUNCATED]' })}\n`
  return Buffer.byteLength(fallback, 'utf8') <= maxBytes ? fallback : '{}\n'
}

function boundedOriginalField(value: string, maxBytes: number): string {
  return truncateUtf8(value, maxBytes)
}

export function writeProcessFatalDiagnostic(input: ProcessFatalDiagnosticInput): void {
  try {
    const line = serializeProcessFatalDiagnostic(input)
    try {
      writeSync(2, line)
    } catch {
      process.stderr.write(line)
    }
  } catch {
    try {
      writeSync(2, '{"event":"process_fatal_diagnostic_failed"}\n')
    } catch {
      // The process is already terminating; no further logging path is reliable here.
    }
  }
}

export function writeProcessDiagnosticAsync(input: ProcessFatalDiagnosticInput, priority: 'critical' | 'general' = 'critical'): void {
  try {
    enqueueAsyncDiagnostic(serializeAsyncDiagnosticLine(serializeProcessFatalDiagnostic({
      ...input,
      maxBytes: Math.min(asyncDiagnosticMaxBytes, input.maxBytes ?? defaultMaxBytes)
    })), priority)
  } catch {
    enqueueAsyncDiagnostic('{"event":"process_async_diagnostic_failed"}\n', priority)
  }
}

export function writeBoundedProcessDiagnosticLineAsync(line: string): void {
  enqueueAsyncDiagnostic(serializeAsyncDiagnosticLine(line), 'general')
}

export function processDiagnosticAsyncStatsForTest(): {
  active: boolean
  pending: boolean
  dropped: number
  pendingBytes: number
} {
  return {
    active: asyncDiagnosticActive,
    pending: asyncDiagnosticPendingCritical !== undefined || asyncDiagnosticPendingGeneral !== undefined,
    dropped: asyncDiagnosticDropped,
    pendingBytes: Buffer.byteLength(asyncDiagnosticPendingCritical ?? '', 'utf8')
      + Buffer.byteLength(asyncDiagnosticPendingGeneral ?? '', 'utf8')
  }
}

export function setProcessDiagnosticAsyncWriterForTest(writer?: AsyncDiagnosticWrite): void {
  asyncDiagnosticWriter = writer ?? defaultAsyncDiagnosticWrite
  asyncDiagnosticActive = false
  asyncDiagnosticPendingCritical = undefined
  asyncDiagnosticPendingGeneral = undefined
  asyncDiagnosticDropped = 0
  asyncDiagnosticDropsToReport = 0
  notifyAsyncDiagnosticDrained()
}

export async function drainProcessDiagnosticAsync(timeoutMs = 250): Promise<void> {
  if (!asyncDiagnosticActive && asyncDiagnosticPendingCritical === undefined && asyncDiagnosticPendingGeneral === undefined) return
  await new Promise<void>((resolve) => {
    const timeout = setTimeout(() => {
      asyncDiagnosticDrainWaiters.delete(finish)
      resolve()
    }, timeoutMs)
    const finish = () => {
      clearTimeout(timeout)
      asyncDiagnosticDrainWaiters.delete(finish)
      resolve()
    }
    asyncDiagnosticDrainWaiters.add(finish)
  })
}

export const drainProcessDiagnosticAsyncForTest = drainProcessDiagnosticAsync

function enqueueAsyncDiagnostic(line: string, priority: 'critical' | 'general'): void {
  if (asyncDiagnosticActive) {
    const target = priority === 'critical' ? asyncDiagnosticPendingCritical : asyncDiagnosticPendingGeneral
    if (target !== undefined) {
      asyncDiagnosticDropped += 1
      asyncDiagnosticDropsToReport += 1
    }
    if (priority === 'critical') asyncDiagnosticPendingCritical = line
    else asyncDiagnosticPendingGeneral = line
    return
  }
  writeAsyncDiagnostic(line)
}

function writeAsyncDiagnostic(line: string): void {
  const output = addAsyncDiagnosticDropCount(line)
  asyncDiagnosticActive = true
  let callbackCalled = false
  const done = () => {
    if (callbackCalled) return
    callbackCalled = true
    asyncDiagnosticActive = false
    const pending = asyncDiagnosticPendingCritical ?? asyncDiagnosticPendingGeneral
    if (asyncDiagnosticPendingCritical !== undefined) {
      asyncDiagnosticPendingCritical = undefined
    } else {
      asyncDiagnosticPendingGeneral = undefined
    }
    if (pending !== undefined) {
      writeAsyncDiagnostic(pending)
      return
    }
    notifyAsyncDiagnosticDrained()
  }
  try {
    asyncDiagnosticWriter(output, done)
  } catch {
    done()
  }
}

function defaultAsyncDiagnosticWrite(line: string, callback: (error?: Error | null) => void): void {
  write(2, line, (error) => callback(error))
}

function notifyAsyncDiagnosticDrained(): void {
  if (asyncDiagnosticActive || asyncDiagnosticPendingCritical !== undefined || asyncDiagnosticPendingGeneral !== undefined) return
  for (const waiter of [...asyncDiagnosticDrainWaiters]) waiter()
}

function serializeAsyncDiagnosticLine(line: string): string {
  const normalized = line.endsWith('\n') ? line.slice(0, -1) : line
  try {
    const value = JSON.parse(normalized) as Record<string, unknown>
    const bounded = JSON.stringify(value)
    if (Buffer.byteLength(`${bounded}\n`, 'utf8') <= asyncDiagnosticMaxBytes - 512) return `${bounded}\n`
    return `${JSON.stringify({
      event: typeof value.event === 'string' ? value.event : 'process_async_diagnostic_oversized',
      diagnosticTruncated: true,
      originalBytes: Buffer.byteLength(normalized, 'utf8'),
      message: typeof value.message === 'string' ? truncateUtf8(value.message, 256) : undefined
    })}\n`
  } catch {
    return `${JSON.stringify({
      event: 'process_async_diagnostic_invalid_line',
      diagnosticTruncated: true,
      originalBytes: Buffer.byteLength(normalized, 'utf8'),
      preview: truncateUtf8(normalized, 256)
    })}\n`
  }
}

function addAsyncDiagnosticDropCount(line: string): string {
  const dropped = asyncDiagnosticDropsToReport
  if (dropped <= 0) return line
  asyncDiagnosticDropsToReport = 0
  try {
    const normalized = line.endsWith('\n') ? line.slice(0, -1) : line
    const value = JSON.parse(normalized) as Record<string, unknown>
    value.asyncDiagnosticDropped = dropped
    return `${JSON.stringify(value)}\n`
  } catch {
    return `${JSON.stringify({
      event: 'process_async_diagnostic_drop_summary',
      asyncDiagnosticDropped: dropped,
      latest: truncateUtf8(line, 512)
    })}\n`
  }
}

function normalizeError(error: unknown): { name: string; code?: string; message: string } {
  if (error instanceof Error) {
    const name = safeErrorStringProperty(error, 'name') || 'Error'
    const code = safeErrorStringProperty(error, 'code')
    const message = safeErrorStringProperty(error, 'message') || name || 'unknown error'
    return { name, code, message }
  }
  return { name: 'NonError', message: diagnosticText(error) }
}

function safeErrorStringProperty(error: Error, key: string): string | undefined {
  try {
    const value = (error as unknown as Record<string, unknown>)[key]
    return typeof value === 'string' || typeof value === 'number' ? String(value) : undefined
  } catch {
    return `[unreadable ${key}]`
  }
}

function diagnosticText(value: unknown): string {
  if (typeof value === 'string') return value
  try {
    const serialized = JSON.stringify(value)
    return typeof serialized === 'string' ? serialized : String(value)
  } catch {
    try {
      return String(value)
    } catch {
      return '[unprintable thrown value]'
    }
  }
}

function truncateUtf8(value: string, maxBytes: number): string {
  if (maxBytes <= 0) return ''
  if (value.length <= maxBytes && Buffer.byteLength(value, 'utf8') <= maxBytes) return value
  let low = 0
  let high = Math.min(value.length, maxBytes)
  while (low < high) {
    const midpoint = Math.ceil((low + high) / 2)
    if (Buffer.byteLength(value.slice(0, midpoint), 'utf8') <= maxBytes) {
      low = midpoint
    } else {
      high = midpoint - 1
    }
  }
  let end = low
  if (end > 0) {
    const lastCodeUnit = value.charCodeAt(end - 1)
    if (lastCodeUnit >= 0xD800 && lastCodeUnit <= 0xDBFF) end -= 1
  }
  return value.slice(0, end)
}
