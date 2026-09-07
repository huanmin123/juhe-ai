const redacted = '[REDACTED]'

export interface CodexContextWriterStderrSnapshot {
  summary: string
  capturedBytes: number
  truncated: boolean
}

export interface CodexContextWriterStderrCapture {
  append: (chunk: Buffer | string) => void
  snapshot: () => CodexContextWriterStderrSnapshot
}

export function createCodexContextWriterStderrCapture(input: {
  maxBytes: number
  secrets?: string[]
}): CodexContextWriterStderrCapture {
  const maxBytes = Math.max(0, Math.trunc(input.maxBytes))
  const chunks: Buffer[] = []
  let capturedBytes = 0
  let truncated = false

  return {
    append(chunk) {
      const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
      const remaining = Math.max(0, maxBytes - capturedBytes)
      if (buffer.length > remaining) truncated = true
      if (remaining <= 0) return
      const bounded = buffer.subarray(0, remaining)
      chunks.push(bounded)
      capturedBytes += bounded.length
    },
    snapshot() {
      if (truncated) {
        return {
          summary: `[stderr truncated after exceeding ${maxBytes} bytes]`,
          capturedBytes,
          truncated
        }
      }
      const raw = Buffer.concat(chunks, capturedBytes).toString('utf8')
      return {
        summary: truncateUtf8(sanitizeCodexContextWriterDiagnostic(raw, input.secrets), maxBytes),
        capturedBytes,
        truncated
      }
    }
  }
}

export function sanitizeCodexContextWriterDiagnostic(value: unknown, secrets: string[] = []): string {
  let output = diagnosticText(value)
  for (const secret of secrets) {
    const normalized = secret.trim()
    if (normalized.length >= 6) output = output.split(normalized).join(redacted)
  }
  return output
    .replace(/(["'](?:proxy[-_ ]?authorization|authorization)["']\s*:\s*)["'][^"'\r\n]*["']/gi, `$1"${redacted}"`)
    .replace(/(?<![?&A-Za-z0-9_-])((?:proxy[-_ ]?authorization|authorization)\s*[:=]\s*)[^\r\n;]+/gi, `$1${redacted}`)
    .replace(/\b([A-Za-z][A-Za-z0-9+.-]*:\/\/)[^\s/?#@]+@/g, `$1${redacted}@`)
    .replace(/\bBearer\s+[A-Za-z0-9._~+/=-]{6,}/gi, `Bearer ${redacted}`)
    .replace(/((?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|secret|password)\s*["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;?&#/]+)/gi, `$1${redacted}`)
    .replace(/([?&][^&#=\s]*(?:token|key|secret|password|pwd|auth|signature|credentials?)[^&#=\s]*=)[^&#\s]*/gi, `$1${redacted}`)
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, '')
    .trim()
}

export function boundedCodexContextWriterDiagnostic(value: unknown, input: {
  maxBytes: number
  secrets?: string[]
}): string {
  return truncateUtf8(sanitizeCodexContextWriterDiagnostic(value, input.secrets), Math.max(0, Math.trunc(input.maxBytes)))
}

export function serializeCodexContextWriterFatalDiagnostic(input: {
  kind: string
  error: unknown
  maxBytes: number
  secrets?: string[]
}): string {
  const maxBytes = Math.max(128, Math.trunc(input.maxBytes))
  const sanitized = sanitizeCodexContextWriterDiagnostic(input.error, input.secrets)
  let low = 0
  let high = Math.min(Buffer.byteLength(sanitized, 'utf8'), maxBytes)
  let selected = ''
  while (low <= high) {
    const midpoint = Math.floor((low + high) / 2)
    const summary = truncateUtf8(sanitized, midpoint)
    const candidate = fatalDiagnosticLine(input.kind, summary)
    if (Buffer.byteLength(candidate, 'utf8') <= maxBytes) {
      selected = candidate
      low = midpoint + 1
    } else {
      high = midpoint - 1
    }
  }
  if (selected) return selected
  const fallback = fatalDiagnosticLine('fatal', '[TRUNCATED]')
  return Buffer.byteLength(fallback, 'utf8') <= maxBytes ? fallback : '{"event":"codex_context_state_writer_fatal"}\n'
}

function diagnosticText(value: unknown): string {
  if (value instanceof Error) return value.stack || value.message || value.name
  if (typeof value === 'string') return value
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}

function truncateUtf8(value: string, maxBytes: number): string {
  if (maxBytes <= 0) return ''
  const buffer = Buffer.from(value)
  if (buffer.length <= maxBytes) return value
  return buffer.subarray(0, maxBytes).toString('utf8').replace(/\uFFFD$/u, '')
}

function fatalDiagnosticLine(kind: string, summary: string): string {
  return `${JSON.stringify({ event: 'codex_context_state_writer_fatal', kind, summary })}\n`
}
