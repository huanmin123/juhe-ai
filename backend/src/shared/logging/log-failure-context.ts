import { createHash } from 'node:crypto'

import type { FailureClass } from './log-event-contract.js'

const maxStringLength = 8 * 1024
const maxCauseDepth = 4
const maxEventBytes = 64 * 1024
const maxObjectDepth = 8
const maxCollectionEntries = 100
const maxHashInputLength = 8 * 1024
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
  remainingBytes: number
  seen: WeakSet<object>
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
  if (state.truncated) context.truncationReason = 'field_or_event_limit'
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
    ...(state.truncated ? { truncationReason: 'field_or_event_limit' } : {})
  }
}

export function isFailureClass(value: unknown): value is FailureClass {
  return value === 'expected' || value === 'unexpected' || value === 'aborted' || value === 'infrastructure'
}

function captureError(value: unknown, state: CaptureState, depth = 0): CapturedError | undefined {
  if (!(value instanceof Error)) {
    if (value === undefined || value === null) return undefined
    return { name: 'NonErrorThrown', message: truncateString(safePrimitiveDescription(value), 'error.message', state) }
  }
  const name = safeErrorStringProperty(value, 'name', state)
  const message = safeErrorStringProperty(value, 'message', state)
  const stack = safeErrorStringProperty(value, 'stack', state)
  const code = safeErrorStringProperty(value, 'code', state)
  const captured: CapturedError = {
    name: truncateString(name || 'Error', 'error.name', state),
    message: truncateString(message || '', 'error.message', state),
    ...(stack ? { stack: truncateString(stack, 'error.stack', state) } : {}),
    ...(code ? { code: truncateString(code, 'error.code', state) } : {})
  }
  const causeDescriptor = safeOwnPropertyDescriptor(value, 'cause', state)
  const cause = causeDescriptor && 'value' in causeDescriptor ? causeDescriptor.value : undefined
  if (depth < maxCauseDepth && cause !== undefined) {
    captured.cause = captureError(cause, state, depth + 1)
  } else if (cause !== undefined) {
    state.truncated = true
  }
  return captured
}

function sanitizeValue(value: unknown, path: string, state: CaptureState, depth = 0): unknown {
  if (state.remainingBytes <= 0) {
    state.truncated = true
    return '[truncated: event byte budget]'
  }
  if (typeof value === 'string') return truncateString(value, path, state)
  if (value === null || typeof value !== 'object') {
    consumeBudget(16, state)
    return value
  }
  if (depth >= maxObjectDepth) {
    state.truncated = true
    return '[truncated: depth limit]'
  }
  if (state.seen.has(value)) {
    state.truncated = true
    return '[truncated: circular reference]'
  }
  state.seen.add(value)
  if (Array.isArray(value)) {
    const output: unknown[] = []
    for (let index = 0; index <= maxCollectionEntries; index += 1) {
      const descriptor = safeOwnPropertyDescriptor(value, String(index), state)
      if (!descriptor) break
      if (index === maxCollectionEntries) {
        state.truncated = true
        break
      }
      output.push('value' in descriptor
        ? sanitizeValue(descriptor.value, `${path}[${index}]`, state, depth + 1)
        : unreadableAccessor(state))
    }
    return output
  }
  const output: Record<string, unknown> = {}
  let entryCount = 0
  let scannedKeyCount = 0
  try {
    for (const key in value) {
      scannedKeyCount += 1
      if (scannedKeyCount > maxCollectionEntries) {
        state.truncated = true
        break
      }
      if (!Object.prototype.hasOwnProperty.call(value, key)) continue
      if (entryCount >= maxCollectionEntries) {
        state.truncated = true
        break
      }
      entryCount += 1
      if (state.remainingBytes <= 0) {
        state.truncated = true
        output._truncated = 'event byte budget'
        break
      }
      const boundedKey = boundedUTF8Prefix(key, Math.min(maxStringLength, state.remainingBytes))
      const nestedPath = `${path}.${boundedKey}`
      consumeBudget(Buffer.byteLength(boundedKey, 'utf8'), state)
      const descriptor = safeOwnPropertyDescriptor(value, key, state)
      if (!descriptor) {
        output[boundedKey] = '[unreadable: property descriptor]'
      } else if ('value' in descriptor) {
        output[boundedKey] = sanitizeValue(descriptor.value, nestedPath, state, depth + 1)
      } else {
        output[boundedKey] = unreadableAccessor(state)
      }
    }
  } catch {
    state.truncated = true
    output._truncated = 'property enumeration failed'
  }
  return output
}

function truncateString(value: string, path: string, state: CaptureState): string {
  const allowed = Math.min(maxStringLength, Math.max(0, state.remainingBytes))
  if (value.length <= Math.floor(allowed / 3)) {
    const size = Buffer.byteLength(value, 'utf8')
    consumeBudget(size, state)
    return value
  }
  const output = boundedUTF8Prefix(value, allowed)
  if (output.length === value.length) {
    consumeBudget(Buffer.byteLength(output, 'utf8'), state)
    return output
  }
  state.truncated = true
  // Full byte length/hash would make failure capture proportional to hostile input size.
  state.fieldSizes[path] = value.length
  state.fieldHashes[path] = createHash('sha256').update(value.slice(0, maxHashInputLength)).digest('hex')
  consumeBudget(Buffer.byteLength(output, 'utf8'), state)
  return output
}

function boundedUTF8Prefix(value: string, maxBytes: number): string {
  if (maxBytes <= 0) return ''
  const boundedInput = value.slice(0, maxBytes)
  const buffer = Buffer.from(boundedInput, 'utf8')
  if (buffer.length <= maxBytes) return boundedInput
  return buffer.subarray(0, maxBytes).toString('utf8').replace(/\uFFFD$/u, '')
}

function safePrimitiveDescription(value: unknown): string {
  switch (typeof value) {
    case 'string': return value
    case 'number': return Number.isFinite(value) ? String(value) : '[non-finite number]'
    case 'boolean': return value ? 'true' : 'false'
    case 'bigint': return `${value}n`
    case 'symbol': return '[symbol]'
    case 'function': return '[function]'
    case 'object': return '[non-Error object thrown]'
    default: return '[unknown thrown value]'
  }
}

function safeOwnPropertyDescriptor(value: object, key: PropertyKey, state: CaptureState): PropertyDescriptor | undefined {
  try {
    return Object.getOwnPropertyDescriptor(value, key)
  } catch {
    state.truncated = true
    return undefined
  }
}

function safeErrorStringProperty(error: Error, key: string, state: CaptureState): string | undefined {
  let current: object | null = error
  for (let depth = 0; current && depth <= maxCauseDepth; depth += 1) {
    const descriptor = safeOwnPropertyDescriptor(current, key, state)
    if (descriptor) {
      if ('value' in descriptor && typeof descriptor.value === 'string') return descriptor.value
      if (!('value' in descriptor)) state.truncated = true
      return undefined
    }
    try {
      current = Object.getPrototypeOf(current)
    } catch {
      state.truncated = true
      return undefined
    }
  }
  return undefined
}

function unreadableAccessor(state: CaptureState): string {
  state.truncated = true
  return '[unreadable: accessor]'
}

function sanitizeRecord(value: Record<string, unknown>, path: string, state: CaptureState): Record<string, unknown> {
  return sanitizeValue(value, path, state) as Record<string, unknown>
}

function createState(): CaptureState {
  return {
    redactedFields: [],
    fieldSizes: {},
    fieldHashes: {},
    truncated: false,
    remainingBytes: maxEventBytes,
    seen: new WeakSet<object>()
  }
}

function consumeBudget(bytes: number, state: CaptureState): void {
  state.remainingBytes = Math.max(0, state.remainingBytes - bytes)
  if (state.remainingBytes === 0) state.truncated = true
}
