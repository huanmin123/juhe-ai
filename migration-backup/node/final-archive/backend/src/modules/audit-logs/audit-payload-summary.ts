import { createHash } from 'node:crypto'

import type { AuditLogPayloadInput } from '../../storage/audit-log-types.js'

export const auditBodySummaryEdgeBytes = 256 * 1024
export const auditPayloadSummaryContentType = 'application/json; audit=payload-summary'

const auditBodySummaryTextPreviewBytes = 4 * 1024

type AuditPayloadSummaryReason = 'body_exceeded_full_capture_limit' | 'transport_message_budget'

export function summarizeAuditPayloadForLimit(
  payload: Omit<AuditLogPayloadInput, 'sequenceIndex'>,
  fullBodyLimitBytes: number,
  options: { force?: boolean; includeGatewayMetadata?: boolean; reason?: AuditPayloadSummaryReason } = {}
): boolean {
  if ((!options.includeGatewayMetadata && payload.partType === 'gateway_metadata') || payload.body === undefined) {
    return false
  }
  if (payload.captureStatus && payload.captureStatus !== 'complete') {
    updateExistingPayloadSummaryLimit(payload, fullBodyLimitBytes)
    return false
  }
  const bodyBuffer = bodyToBuffer(payload.body)
  const originalBodySizeBytes = payload.rawBodySizeBytes ?? bodyBuffer.byteLength
  if (fullBodyLimitBytes === 0) {
    payload.bodySha256 = payload.bodySha256 ?? sha256Buffer(bodyBuffer)
    payload.rawBodySizeBytes = originalBodySizeBytes
    payload.captureStatus = 'hash_only'
    payload.body = undefined
    payload.contentEncoding = undefined
    return true
  }
  if (!options.force && originalBodySizeBytes <= fullBodyLimitBytes) {
    return false
  }
  const originalContentType = payload.contentType
  const originalContentEncoding = payload.contentEncoding
  const originalSha256 = payload.bodySha256 ?? sha256Buffer(bodyBuffer)
  payload.body = JSON.stringify(buildAuditPayloadSummary({
    body: bodyBuffer,
    originalSha256,
    originalBodySizeBytes,
    originalContentType,
    originalContentEncoding,
    fullBodyLimitBytes,
    reason: options.reason ?? 'body_exceeded_full_capture_limit'
  }))
  payload.bodySha256 = originalSha256
  payload.rawBodySizeBytes = originalBodySizeBytes
  payload.captureStatus = 'summary_only'
  payload.contentType = auditPayloadSummaryContentType
  payload.contentEncoding = undefined
  return true
}

function updateExistingPayloadSummaryLimit(
  payload: Omit<AuditLogPayloadInput, 'sequenceIndex'>,
  fullBodyLimitBytes: number
): void {
  if (fullBodyLimitBytes === 0 && payload.captureStatus === 'summary_only') {
    payload.captureStatus = 'hash_only'
    payload.body = undefined
    payload.contentEncoding = undefined
    return
  }
  if (payload.captureStatus !== 'summary_only' || typeof payload.body !== 'string') {
    return
  }
  try {
    const summary = JSON.parse(payload.body) as Record<string, unknown>
    if (summary.type !== 'audit_payload_summary') {
      return
    }
    shrinkExistingPayloadSummary(summary, fullBodyLimitBytes)
    summary.fullBodyLimitBytes = fullBodyLimitBytes
    payload.body = JSON.stringify(summary)
  } catch {
    return
  }
}

function shrinkExistingPayloadSummary(summary: Record<string, unknown>, fullBodyLimitBytes: number): void {
  const head = decodedSummaryWindow(summary.headBase64)
  const tail = decodedSummaryWindow(summary.tailBase64)
  if (!head || !tail) return
  const retainedBytes = Math.min(
    head.byteLength + tail.byteLength,
    Math.max(0, Math.trunc(fullBodyLimitBytes))
  )
  const headBytes = Math.min(head.byteLength, Math.ceil(retainedBytes / 2))
  const tailBytes = Math.min(tail.byteLength, Math.floor(retainedBytes / 2))
  const nextHead = head.subarray(0, headBytes)
  const nextTail = tail.subarray(Math.max(0, tail.byteLength - tailBytes))
  const originalSizeBytes = numericSummaryValue(summary.originalSizeBytes)
  summary.retainedHeadBytes = nextHead.byteLength
  summary.retainedTailBytes = nextTail.byteLength
  summary.omittedMiddleBytes = Math.max(0, originalSizeBytes - nextHead.byteLength - nextTail.byteLength)
  summary.headBase64 = nextHead.toString('base64')
  summary.tailBase64 = nextTail.toString('base64')
  if (summary.textPreview && typeof summary.textPreview === 'object' && !Array.isArray(summary.textPreview)) {
    summary.textPreview = {
      head: textPreview(nextHead),
      tail: textPreview(nextTail)
    }
  }
}

function decodedSummaryWindow(value: unknown): Buffer | undefined {
  if (typeof value !== 'string') return undefined
  try {
    return Buffer.from(value, 'base64')
  } catch {
    return undefined
  }
}

function numericSummaryValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0
}

function buildAuditPayloadSummary(input: {
  body: Buffer
  originalSha256?: string
  originalBodySizeBytes: number
  originalContentType?: string
  originalContentEncoding?: string
  fullBodyLimitBytes: number
  reason: AuditPayloadSummaryReason
}): Record<string, unknown> {
  const retainedBodyBytes = Math.min(
    input.body.byteLength,
    auditBodySummaryEdgeBytes * 2,
    Math.max(0, Math.trunc(input.fullBodyLimitBytes))
  )
  const headBytes = Math.ceil(retainedBodyBytes / 2)
  const tailBytes = Math.min(Math.floor(retainedBodyBytes / 2), input.body.byteLength - headBytes)
  const head = input.body.subarray(0, headBytes)
  const tail = input.body.subarray(input.body.byteLength - tailBytes)
  const summary: Record<string, unknown> = {
    type: 'audit_payload_summary',
    captureStatus: 'summary_only',
    reason: input.reason,
    fullBodyLimitBytes: input.fullBodyLimitBytes,
    originalSha256: input.originalSha256,
    originalSizeBytes: input.originalBodySizeBytes,
    originalContentType: input.originalContentType,
    originalContentEncoding: input.originalContentEncoding,
    retainedHeadBytes: head.byteLength,
    retainedTailBytes: tail.byteLength,
    omittedMiddleBytes: Math.max(0, input.originalBodySizeBytes - retainedBodyBytes),
    headBase64: head.toString('base64'),
    tailBase64: tail.toString('base64')
  }
  if (isTextLikePayload(input.originalContentType, input.originalContentEncoding)) {
    summary.textPreview = {
      head: textPreview(head),
      tail: textPreview(tail)
    }
  }
  return summary
}

function isTextLikePayload(contentType?: string, contentEncoding?: string): boolean {
  const encoding = contentEncoding?.trim().toLowerCase()
  if (encoding && encoding !== 'identity') {
    return false
  }
  const type = contentType?.toLowerCase() ?? ''
  return type.includes('json')
    || type.includes('text')
    || type.includes('xml')
    || type.includes('event-stream')
    || type.includes('javascript')
    || type.includes('x-www-form-urlencoded')
}

function textPreview(buffer: Buffer): string {
  return buffer.subarray(0, Math.min(buffer.byteLength, auditBodySummaryTextPreviewBytes)).toString('utf8')
}

function bodyToBuffer(body: Buffer | string): Buffer {
  return Buffer.isBuffer(body) ? body : Buffer.from(body, 'utf8')
}

function sha256Buffer(buffer: Buffer): string {
  return createHash('sha256').update(buffer).digest('hex')
}
