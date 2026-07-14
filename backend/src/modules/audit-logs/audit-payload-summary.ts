import { createHash } from 'node:crypto'

import type { AuditLogPayloadInput } from '../../storage/audit-log-types.js'

export const auditBodySummaryEdgeBytes = 256 * 1024
export const auditPayloadSummaryContentType = 'application/json; audit=payload-summary'

const auditBodySummaryTextPreviewBytes = 4 * 1024
const auditJsonSummaryParseMaxBytes = 512 * 1024
const auditJsonSummaryMaxKeys = 50

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
  if (payload.captureStatus !== 'summary_only' || typeof payload.body !== 'string') {
    return
  }
  try {
    const summary = JSON.parse(payload.body) as Record<string, unknown>
    if (summary.type !== 'audit_payload_summary') {
      return
    }
    summary.fullBodyLimitBytes = fullBodyLimitBytes
    payload.body = JSON.stringify(summary)
  } catch {
    return
  }
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
  const head = input.body.subarray(0, Math.min(auditBodySummaryEdgeBytes, input.body.byteLength))
  const tailStart = Math.max(0, input.body.byteLength - auditBodySummaryEdgeBytes)
  const tail = input.body.subarray(tailStart)
  const hasSeparatedTail = tailStart >= head.byteLength
  const retainedBodyBytes = head.byteLength + (
    hasSeparatedTail
      ? tail.byteLength
      : Math.max(0, input.body.byteLength - head.byteLength)
  )
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
  const json = summarizeJsonPayload(input.body, input.originalContentType, input.originalContentEncoding)
  if (json) {
    summary.json = json
  }
  return summary
}

function summarizeJsonPayload(
  body: Buffer,
  contentType?: string,
  contentEncoding?: string
): Record<string, unknown> | undefined {
  if (!isJsonLikePayload(body, contentType, contentEncoding)) {
    return undefined
  }
  const headText = body.subarray(0, Math.min(body.byteLength, auditBodySummaryEdgeBytes)).toString('utf8')
  if (body.byteLength > auditJsonSummaryParseMaxBytes) {
    return {
      parseable: false,
      reason: 'body_too_large_for_inline_parse',
      topLevelType: inferJsonTopLevelType(headText),
      topLevelKeys: extractTopLevelObjectKeysFromJsonPrefix(headText)
    }
  }
  try {
    return summarizeParsedJsonValue(JSON.parse(body.toString('utf8')))
  } catch {
    return {
      parseable: false,
      reason: 'json_parse_failed',
      topLevelType: inferJsonTopLevelType(headText),
      topLevelKeys: extractTopLevelObjectKeysFromJsonPrefix(headText)
    }
  }
}

function summarizeParsedJsonValue(value: unknown): Record<string, unknown> {
  if (Array.isArray(value)) {
    return {
      parseable: true,
      topLevelType: 'array',
      topLevelLength: value.length,
      firstItemType: jsonValueType(value[0]),
      firstItemKeys: value[0] && typeof value[0] === 'object' && !Array.isArray(value[0])
        ? topLevelObjectKeys(value[0] as Record<string, unknown>).keys
        : undefined
    }
  }
  if (value && typeof value === 'object') {
    const keys = topLevelObjectKeys(value as Record<string, unknown>)
    return {
      parseable: true,
      topLevelType: 'object',
      topLevelKeyCountAtLeast: keys.countAtLeast,
      topLevelKeys: keys.keys,
      topLevelKeysTruncated: keys.truncated
    }
  }
  return {
    parseable: true,
    topLevelType: jsonValueType(value)
  }
}

function topLevelObjectKeys(value: Record<string, unknown>): { keys: string[]; countAtLeast: number; truncated: boolean } {
  const keys: string[] = []
  let countAtLeast = 0
  let truncated = false
  for (const key in value) {
    if (!Object.prototype.hasOwnProperty.call(value, key)) continue
    countAtLeast += 1
    if (countAtLeast > auditJsonSummaryMaxKeys) {
      truncated = true
      break
    }
    if (keys.length < auditJsonSummaryMaxKeys) {
      keys.push(key)
    }
  }
  return { keys, countAtLeast: Math.min(countAtLeast, auditJsonSummaryMaxKeys), truncated }
}

function isJsonLikePayload(body: Buffer, contentType?: string, contentEncoding?: string): boolean {
  const encoding = contentEncoding?.trim().toLowerCase()
  if (encoding && encoding !== 'identity') {
    return false
  }
  const normalizedContentType = contentType?.toLowerCase() ?? ''
  if (normalizedContentType.includes('json')) {
    return true
  }
  const head = body.subarray(0, Math.min(body.byteLength, 512)).toString('utf8')
  const firstChar = firstNonWhitespaceChar(head)
  return firstChar === '{' || firstChar === '['
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

function inferJsonTopLevelType(text: string): string {
  const firstChar = firstNonWhitespaceChar(text)
  if (firstChar === '{') return 'object'
  if (firstChar === '[') return 'array'
  if (firstChar === '"') return 'string'
  if (firstChar === 't' || firstChar === 'f') return 'boolean'
  if (firstChar === 'n') return 'null'
  if (firstChar && /[-0-9]/.test(firstChar)) return 'number'
  return 'unknown'
}

function extractTopLevelObjectKeysFromJsonPrefix(text: string): string[] {
  const keys: string[] = []
  let depth = 0
  let index = 0
  while (index < text.length && keys.length < auditJsonSummaryMaxKeys) {
    const char = text[index]
    if (char === '"') {
      const parsed = readJsonStringAt(text, index)
      if (!parsed) break
      const nextIndex = skipJsonWhitespace(text, parsed.end)
      if (depth === 1 && text[nextIndex] === ':') {
        keys.push(parsed.value)
      }
      index = parsed.end
      continue
    }
    if (char === '{' || char === '[') {
      depth += 1
    } else if (char === '}' || char === ']') {
      depth = Math.max(0, depth - 1)
    }
    index += 1
  }
  return [...new Set(keys)]
}

function readJsonStringAt(text: string, start: number): { value: string; end: number } | undefined {
  let escaped = false
  for (let index = start + 1; index < text.length; index += 1) {
    const char = text[index]
    if (escaped) {
      escaped = false
      continue
    }
    if (char === '\\') {
      escaped = true
      continue
    }
    if (char === '"') {
      const raw = text.slice(start, index + 1)
      try {
        return { value: JSON.parse(raw) as string, end: index + 1 }
      } catch {
        return { value: raw.slice(1, -1), end: index + 1 }
      }
    }
  }
}

function skipJsonWhitespace(text: string, start: number): number {
  let index = start
  while (index < text.length && /\s/.test(text[index])) {
    index += 1
  }
  return index
}

function firstNonWhitespaceChar(text: string): string {
  return text.trimStart().charAt(0)
}

function jsonValueType(value: unknown): string {
  return Array.isArray(value) ? 'array' : value === null ? 'null' : typeof value
}

function bodyToBuffer(body: Buffer | string): Buffer {
  return Buffer.isBuffer(body) ? body : Buffer.from(body, 'utf8')
}

function sha256Buffer(buffer: Buffer): string {
  return createHash('sha256').update(buffer).digest('hex')
}
