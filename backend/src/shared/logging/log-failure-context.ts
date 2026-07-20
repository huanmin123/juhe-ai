import { createHash } from 'node:crypto'

import type { FailureClass } from './log-event-contract.js'

const maxStringLength = 8 * 1024
const maxCauseDepth = 4
const sensitiveKeyPattern = /(?:authorization|proxy.?authorization|cookie|set-cookie|password|secret|token|api.?key|credential|private.?key)/i

interface FailureCaptureOptions {
  stageSnapshot?: Record<string, unknown>
  queueSnapshot?: Record<string, unknown>
  retryState?: Record<string, unknown>
  decisionInputs?: Record<string, unknown>
}

interface CapturedError {
  name: string
  message: string
  stack?: string
  code?: string
  cause?: CapturedError
}

export interface UnexpectedFailureContext {
  failureClass: 'unexpected'
  error?: CapturedError
  stageSnapshot?: Record<string, unknown>
  queueSnapshot?: Record<string, unknown>
  retryState?: Record<string, unknown>
  decisionInputs?: Record<string, unknown>
  redactedFields: string[]
  fieldSizes: Record<string, number>
  fieldHashes: Record<string, string>
  truncationReason?: string
}

export interface ExpectedFailureContext {
  failureClass: 'expected'
  reasonCode: string
  decisionInputs: Record<string, unknown>
  redactedFields: string[]
  fieldSizes: Record<string, number>
  fieldHashes: Record<string, string>
  truncationReason?: string
}

interface CaptureState {
  redactedFields: string[]
  fieldSizes: Record<string, number>
  fieldHashes: Record<string, string>
  truncated: boolean
}

export function captureUnexpectedFailureContext(error: unknown, options: FailureCaptureOptions = {}): UnexpectedFailureContext {
  const state = createState()
  const context: UnexpectedFailureContext = {
    failureClass: 'unexpected',
    error: captureError(error, state),
    redactedFields: state.redactedFields,
    fieldSizes: state.fieldSizes,
    fieldHashes: state.fieldHashes
  }
  if (options.stageSnapshot) context.stageSnapshot = sanitizeRecord(options.stageSnapshot, 'stageSnapshot', state)
  if (options.queueSnapshot) context.queueSnapshot = sanitizeRecord(options.queueSnapshot, 'queueSnapshot', state)
  if (options.retryState) context.retryState = sanitizeRecord(options.retryState, 'retryState', state)
  if (options.decisionInputs) context.decisionInputs = sanitizeRecord(options.decisionInputs, 'decisionInputs', state)
  if (state.truncated) context.truncationReason = 'field_limit'
  return context
}

export function captureExpectedFailureContext(reasonCode: string, decisionInputs: Record<string, unknown>): ExpectedFailureContext {
  if (!reasonCode.trim()) throw new Error('可预知失败必须提供 reasonCode')
  const state = createState()
  const sanitized = sanitizeValue(decisionInputs, 'decisionInputs', state) as Record<string, unknown>
  return {
    failureClass: 'expected',
    reasonCode,
    decisionInputs: sanitized,
    redactedFields: state.redactedFields,
    fieldSizes: state.fieldSizes,
    fieldHashes: state.fieldHashes,
    ...(state.truncated ? { truncationReason: 'field_limit' } : {})
  }
}

export function isFailureClass(value: unknown): value is FailureClass {
  return value === 'expected' || value === 'unexpected' || value === 'aborted' || value === 'infrastructure'
}

function captureError(value: unknown, state: CaptureState, depth = 0): CapturedError | undefined {
  if (!(value instanceof Error)) {
    if (value === undefined || value === null) return undefined
    return { name: 'NonErrorThrown', message: truncateString(String(value), 'error.message', state) }
  }
  const code = 'code' in value && typeof value.code === 'string' ? value.code : undefined
  const captured: CapturedError = {
    name: truncateString(value.name || 'Error', 'error.name', state),
    message: truncateString(value.message, 'error.message', state),
    ...(value.stack ? { stack: truncateString(value.stack, 'error.stack', state) } : {}),
    ...(code ? { code: truncateString(code, 'error.code', state) } : {})
  }
  if (depth < maxCauseDepth && value.cause !== undefined) {
    captured.cause = captureError(value.cause, state, depth + 1)
  } else if (value.cause !== undefined) {
    state.truncated = true
  }
  return captured
}

function sanitizeValue(value: unknown, path: string, state: CaptureState, depth = 0): unknown {
  if (typeof value === 'string') return truncateString(value, path, state)
  if (value === null || typeof value !== 'object') return value
  if (depth >= 8) {
    state.truncated = true
    return '[truncated: depth limit]'
  }
  if (Array.isArray(value)) return value.slice(0, 100).map((item, index) => sanitizeValue(item, `${path}[${index}]`, state, depth + 1))
  const output: Record<string, unknown> = {}
  for (const [key, nested] of Object.entries(value).slice(0, 100)) {
    const nestedPath = `${path}.${key}`
    if (sensitiveKeyPattern.test(key)) {
      state.redactedFields.push(nestedPath)
      continue
    }
    output[key] = sanitizeValue(nested, nestedPath, state, depth + 1)
  }
  return output
}

function truncateString(value: string, path: string, state: CaptureState): string {
  const size = Buffer.byteLength(value)
  if (size <= maxStringLength) return value
  state.truncated = true
  state.fieldSizes[path] = size
  state.fieldHashes[path] = createHash('sha256').update(value).digest('hex')
  return Buffer.from(value).subarray(0, maxStringLength).toString('utf8')
}

function sanitizeRecord(value: Record<string, unknown>, path: string, state: CaptureState): Record<string, unknown> {
  return sanitizeValue(value, path, state) as Record<string, unknown>
}

function createState(): CaptureState {
  return { redactedFields: [], fieldSizes: {}, fieldHashes: {}, truncated: false }
}
