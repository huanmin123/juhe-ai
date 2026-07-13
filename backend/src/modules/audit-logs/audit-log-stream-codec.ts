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

export function encodeAuditLogStreamPayload(input: AuditLogInput): string {
  return JSON.stringify(serializeAuditLogInput(input))
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
