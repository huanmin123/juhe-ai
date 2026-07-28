import type { AuditLogInput, AuditLogPayloadInput } from '../../storage/audit-log-types.js'

interface SerializedAuditLogInput extends Omit<AuditLogInput, 'payloads'> {
  payloads: SerializedAuditLogPayloadInput[]
}

interface SerializedAuditLogPayloadInput extends Omit<AuditLogPayloadInput, 'body'> {
  body?: string | SerializedAuditLogBuffer
}

interface SerializedAuditLogBuffer {
  __juheAuditBuffer: true
  base64: string
}

const serializedAuditLogBufferEmptyBytes = Buffer.byteLength('{"__juheAuditBuffer":true,"base64":""}', 'utf8')
const serializedAuditLogBodyKeyBytes = Buffer.byteLength('"body":', 'utf8')

export function encodeAuditLogStreamPayload(input: AuditLogInput): string {
  return JSON.stringify(serializeAuditLogInput(input))
}

export function measureAuditLogStreamPayloadBaseBytes(input: AuditLogInput): number {
  return Buffer.byteLength(JSON.stringify(serializeAuditLogInput({ ...input, payloads: [] })), 'utf8')
}

export function measureAuditLogStreamPayloadItemBytes(payload: AuditLogPayloadInput): number {
  const { body, ...rest } = payload
  const restJson = JSON.stringify(rest)
  const restBytes = Buffer.byteLength(restJson, 'utf8')
  const bodyValueBytes = Buffer.isBuffer(body)
    ? serializedAuditLogBufferEmptyBytes + 4 * Math.ceil(body.byteLength / 3)
    : typeof body === 'string'
      ? Buffer.byteLength(JSON.stringify(body), 'utf8')
      : undefined
  if (bodyValueBytes === undefined) return restBytes
  return restBytes + serializedAuditLogBodyKeyBytes + bodyValueBytes + (restJson === '{}' ? 0 : 1)
}

export function decodeAuditLogStreamPayload(payload: string): AuditLogInput {
  return deserializeAuditLogInput(JSON.parse(payload) as SerializedAuditLogInput)
}

function serializeAuditLogInput(input: AuditLogInput): SerializedAuditLogInput {
  return {
    ...input,
    payloads: input.payloads.map(serializeAuditLogPayloadInput)
  }
}

function serializeAuditLogPayloadInput(payload: AuditLogPayloadInput): SerializedAuditLogPayloadInput {
  const { body, ...rest } = payload
  if (Buffer.isBuffer(body)) {
    return {
      ...rest,
      body: {
        __juheAuditBuffer: true,
        base64: body.toString('base64')
      }
    }
  }
  if (typeof body === 'string') {
    return {
      ...rest,
      body
    }
  }
  return rest
}

function deserializeAuditLogInput(input: SerializedAuditLogInput): AuditLogInput {
  return {
    ...input,
    payloads: input.payloads.map(deserializeAuditLogPayloadInput)
  }
}

function deserializeAuditLogPayloadInput(payload: SerializedAuditLogPayloadInput): AuditLogPayloadInput {
  const { body, ...rest } = payload
  if (isSerializedAuditLogBuffer(body)) {
    return {
      ...rest,
      body: Buffer.from(body.base64, 'base64')
    }
  }
  if (typeof body === 'string') {
    return {
      ...rest,
      body
    }
  }
  return rest
}

function isSerializedAuditLogBuffer(value: unknown): value is SerializedAuditLogBuffer {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return false
  }
  const record = value as Record<string, unknown>
  return record.__juheAuditBuffer === true && typeof record.base64 === 'string'
}
