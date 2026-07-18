import { writeSync } from 'node:fs'

const redacted = '[REDACTED]'
const defaultMaxBytes = 4096

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
  const sanitizedMessage = sanitizeDiagnostic(error.message, input.secrets)
  const base = {
    event: boundedSanitizedField(input.event, input.secrets, 128),
    processRole: boundedSanitizedField(input.processRole, input.secrets, 128),
    pid: input.pid,
    errorName: boundedSanitizedField(error.name, input.secrets, 256),
    errorCode: error.code ? boundedSanitizedField(error.code, input.secrets, 256) : undefined
  }
  let low = 0
  let high = Buffer.byteLength(sanitizedMessage, 'utf8')
  let selected = ''
  while (low <= high) {
    const midpoint = Math.floor((low + high) / 2)
    const candidate = `${JSON.stringify({ ...base, message: truncateUtf8(sanitizedMessage, midpoint) })}\n`
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

function boundedSanitizedField(value: string, secrets: string[] | undefined, maxBytes: number): string {
  return truncateUtf8(sanitizeDiagnostic(value, secrets), maxBytes)
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

function normalizeError(error: unknown): { name: string; code?: string; message: string } {
  if (error instanceof Error) {
    const code = 'code' in error && typeof error.code === 'string' ? error.code : undefined
    return { name: error.name || 'Error', code, message: error.message || error.name || 'unknown error' }
  }
  return { name: 'NonError', message: diagnosticText(error) }
}

function sanitizeDiagnostic(value: string, secrets: string[] = []): string {
  let output = value
  for (const secret of secrets) {
    const normalized = secret.trim()
    if (normalized.length >= 6) output = output.split(normalized).join(redacted)
  }
  return output
    .replace(/\bBearer\s+[A-Za-z0-9._~+/=-]{6,}/gi, `Bearer ${redacted}`)
    .replace(/((?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|secret|password)\s*["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;?&#/]+)/gi, `$1${redacted}`)
    .replace(/([?&][^&#=\s]*(?:token|key|secret|password|pwd|auth|signature|credentials?)[^&#=\s]*=)[^&#\s]*/gi, `$1${redacted}`)
    .replace(/\b([A-Za-z][A-Za-z0-9+.-]*:\/\/)[^\s/?#@]+@/g, `$1${redacted}@`)
    .replace(/[\u0000-\u001f\u007f]/g, ' ')
    .trim()
}

function diagnosticText(value: unknown): string {
  if (typeof value === 'string') return value
  try {
    const serialized = JSON.stringify(value)
    return typeof serialized === 'string' ? serialized : String(value)
  } catch {
    return String(value)
  }
}

function truncateUtf8(value: string, maxBytes: number): string {
  const buffer = Buffer.from(value)
  if (buffer.length <= maxBytes) return value
  return buffer.subarray(0, Math.max(0, maxBytes)).toString('utf8').replace(/\uFFFD$/u, '')
}
