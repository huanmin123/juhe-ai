import { newId } from './database.js'
import {
  prepareAuditPayloadBlob,
  prepareAuditPayloadBlobAsync,
  type PreparedAuditPayloadBlob
} from './audit-log-payload-blobs.js'
import type {
  AuditLogPayloadInput,
  AuditPayloadCaptureStatus,
  AuditPayloadDropReason,
  AuditPayloadPartType
} from './audit-log-types.js'
import { stableJsonStringify } from './audit-log-stable-json.js'

export interface PreparedAuditPayload {
  id: string
  attemptTempId?: string
  partType: AuditPayloadPartType
  sequenceIndex: number
  contentType?: string
  contentEncoding?: string
  headersBlob?: PreparedAuditPayloadBlob
  bodyBlob?: PreparedAuditPayloadBlob
  headersSha256?: string
  bodySha256?: string
  rawSizeBytes: number
  compressedSizeBytes: number
  captureStatus: AuditPayloadCaptureStatus
  dropReason?: AuditPayloadDropReason
  createdAt: string
}

const auditHeadersContentType = 'application/json; audit=headers'

export function preparePayloadInput(
  payload: AuditLogPayloadInput,
  fallbackIndex: number,
  fallbackCreatedAt: string
): PreparedAuditPayload {
  const headersBlob = payload.headers
    ? prepareAuditPayloadBlob(Buffer.from(stableJsonStringify(payload.headers), 'utf8'), auditHeadersContentType)
    : undefined
  const bodyBuffer = bodyToBuffer(payload.body)
  const bodyBlob = prepareAuditPayloadBlob(bodyBuffer, payload.contentType, payload.contentEncoding)
  const rawBodySizeBytes = normalizePayloadSizeBytes(payload.rawBodySizeBytes, bodyBlob?.rawSizeBytes ?? 0)
  const rawSizeBytes = (headersBlob?.rawSizeBytes ?? 0) + rawBodySizeBytes
  const compressedSizeBytes = (headersBlob?.compressedSizeBytes ?? 0) + (bodyBlob?.compressedSizeBytes ?? 0)
  const bodySha256 = payload.bodySha256 ?? bodyBlob?.sha256
  return {
    id: payload.id ?? newId('audpay'),
    attemptTempId: payload.attemptTempId,
    partType: payload.partType,
    sequenceIndex: payload.sequenceIndex ?? fallbackIndex,
    contentType: payload.contentType,
    contentEncoding: payload.contentEncoding,
    headersBlob,
    bodyBlob,
    headersSha256: headersBlob?.sha256,
    bodySha256,
    rawSizeBytes,
    compressedSizeBytes,
    captureStatus: payload.captureStatus ?? 'complete',
    dropReason: payload.dropReason,
    createdAt: payload.createdAt ?? fallbackCreatedAt
  }
}

export async function preparePayloadInputAsync(
  payload: AuditLogPayloadInput,
  fallbackIndex: number,
  fallbackCreatedAt: string
): Promise<PreparedAuditPayload> {
  const headersBlob = payload.headers
    ? await prepareAuditPayloadBlobAsync(Buffer.from(stableJsonStringify(payload.headers), 'utf8'), auditHeadersContentType)
    : undefined
  const bodyBuffer = bodyToBuffer(payload.body)
  const bodyBlob = await prepareAuditPayloadBlobAsync(bodyBuffer, payload.contentType, payload.contentEncoding)
  const rawBodySizeBytes = normalizePayloadSizeBytes(payload.rawBodySizeBytes, bodyBlob?.rawSizeBytes ?? 0)
  const rawSizeBytes = (headersBlob?.rawSizeBytes ?? 0) + rawBodySizeBytes
  const compressedSizeBytes = (headersBlob?.compressedSizeBytes ?? 0) + (bodyBlob?.compressedSizeBytes ?? 0)
  const bodySha256 = payload.bodySha256 ?? bodyBlob?.sha256
  return {
    id: payload.id ?? newId('audpay'),
    attemptTempId: payload.attemptTempId,
    partType: payload.partType,
    sequenceIndex: payload.sequenceIndex ?? fallbackIndex,
    contentType: payload.contentType,
    contentEncoding: payload.contentEncoding,
    headersBlob,
    bodyBlob,
    headersSha256: headersBlob?.sha256,
    bodySha256,
    rawSizeBytes,
    compressedSizeBytes,
    captureStatus: payload.captureStatus ?? 'complete',
    dropReason: payload.dropReason,
    createdAt: payload.createdAt ?? fallbackCreatedAt
  }
}

function bodyToBuffer(body: Buffer | string | undefined): Buffer | undefined {
  if (body === undefined) return undefined
  return Buffer.isBuffer(body) ? body : Buffer.from(body, 'utf8')
}

function normalizePayloadSizeBytes(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value)
    ? Math.max(0, Math.trunc(value))
    : fallback
}
