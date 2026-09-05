import type { AuditLogAttemptInput, AuditLogInput, AuditLogPayloadInput } from '../../storage/audit-log-types.js'
import { summarizeAuditPayloadForLimit } from './audit-payload-summary.js'

export const auditLogGoInputMaxBytes = 4 * 1024 * 1024
const auditLogGoInputFineGrainedPayloadLimit = 256

export interface PreparedAuditLogGoInput {
  input: AuditLogInput
  body: Buffer
}

/**
 * Produces the exact JSON envelope consumed by the F3 sidecar. This is a
 * one-shot input budget, not a queue: it never stores, retries, or falls back.
 */
export function prepareAuditLogForGoInput(input: AuditLogInput, limits: {
  successFullBodyLimitBytes: number
  problemFullBodyLimitBytes: number
}): PreparedAuditLogGoInput {
  let prepared: AuditLogInput = {
    ...truncateAuditLogStrings(input),
    attempts: input.attempts.map(truncateAttemptStrings),
    payloads: input.payloads.map((payload) => preparePayload(payload, input, limits))
  }
  let body = encodeEnvelope(prepared)
  if (body.byteLength <= auditLogGoInputMaxBytes) return { input: prepared, body }
  if (prepared.payloads.length > auditLogGoInputFineGrainedPayloadLimit) {
    return prepareRangeTombstone(prepared)
  }

  const bodyIndexes = prepared.payloads
    .map((payload, index) => ({ index, bytes: bodyBytes(payload.body) }))
    .filter((item) => item.bytes > 0)
    .sort((left, right) => right.bytes - left.bytes)
  for (const item of bodyIndexes) {
    const payload = prepared.payloads[item.index]
    if (!payload) continue
    summarizeAuditPayloadForLimit(payload, bodyWindow(limits, input), {
      force: true,
      includeGatewayMetadata: true,
      reason: 'transport_message_budget'
    })
    body = encodeEnvelope(prepared)
    if (body.byteLength <= auditLogGoInputMaxBytes) return { input: prepared, body }
  }

  for (const payload of prepared.payloads) {
    if (payload.captureStatus !== 'summary_only') continue
    summarizeAuditPayloadForLimit(payload, 4 * 1024, {
      force: true,
      includeGatewayMetadata: true,
      reason: 'transport_message_budget'
    })
  }
  body = encodeEnvelope(prepared)
  if (body.byteLength <= auditLogGoInputMaxBytes) return { input: prepared, body }

  let structureDropped = false
  for (const item of prepared.payloads
    .map((payload, index) => ({ index, bytes: headerBytes(payload.headers) }))
    .filter((item) => item.bytes > 0)
    .sort((left, right) => right.bytes - left.bytes)) {
    const payload = prepared.payloads[item.index]
    if (!payload) continue
    prepared.payloads[item.index] = transportTombstone(payload)
    structureDropped = true
    if (structureDropped) prepared = markStructureDropped(prepared)
    body = encodeEnvelope(prepared)
    if (body.byteLength <= auditLogGoInputMaxBytes) return { input: prepared, body }
  }

  for (const index of payloadIndexesFromMiddle(prepared.payloads.length)) {
    if (body.byteLength <= auditLogGoInputMaxBytes) break
    const payload = prepared.payloads[index]
    if (!payload) continue
    prepared.payloads[index] = transportTombstone(payload)
    structureDropped = true
    prepared = markStructureDropped(prepared)
    body = encodeEnvelope(prepared)
  }
  if (body.byteLength <= auditLogGoInputMaxBytes) return { input: prepared, body }

  return prepareRangeTombstone(prepared)
}

function prepareRangeTombstone(input: AuditLogInput): PreparedAuditLogGoInput {
  const prepared = markStructureDropped({
    ...input,
    attempts: [],
    payloads: [transportRangeTombstone(input.payloads)]
  })
  const body = encodeEnvelope(prepared)
  if (body.byteLength > auditLogGoInputMaxBytes) {
    throw new Error('F3 Go 审计输入在 tombstone 收敛后仍超过 4MiB')
  }
  return { input: prepared, body }
}

function preparePayload(payload: AuditLogPayloadInput, input: AuditLogInput, limits: {
  successFullBodyLimitBytes: number
  problemFullBodyLimitBytes: number
}): AuditLogPayloadInput {
  const prepared = truncatePayloadStrings({ ...payload })
  summarizeAuditPayloadForLimit(prepared, bodyWindow(limits, input))
  return prepared
}

function bodyWindow(limits: { successFullBodyLimitBytes: number; problemFullBodyLimitBytes: number }, input: AuditLogInput): number {
  return input.auditOutcome === 'success' ? limits.successFullBodyLimitBytes : limits.problemFullBodyLimitBytes
}

function encodeEnvelope(auditLog: AuditLogInput): Buffer {
  return Buffer.from(JSON.stringify({ schemaVersion: 1, auditLog }), 'utf8')
}

function transportTombstone(payload: AuditLogPayloadInput): AuditLogPayloadInput {
  return {
    ...truncatePayloadStrings(payload),
    headers: undefined,
    body: undefined,
    captureStatus: 'dropped',
    dropReason: 'transport_budget'
  }
}

function transportRangeTombstone(payloads: AuditLogPayloadInput[]): AuditLogPayloadInput {
  const first = payloads[0]
  const last = payloads[payloads.length - 1]
  return {
    id: `transport-budget-${first?.sequenceIndex ?? 0}-${last?.sequenceIndex ?? 0}`,
    partType: 'gateway_metadata',
    sequenceIndex: first?.sequenceIndex ?? 0,
    contentType: 'application/json; audit=transport-budget-tombstone',
    rawBodySizeBytes: payloads.reduce((total, payload) => total + Math.max(0, payload.rawBodySizeBytes ?? bodyBytes(payload.body)), 0),
    captureStatus: 'dropped',
    dropReason: 'transport_budget',
    createdAt: first?.createdAt
  }
}

function payloadIndexesFromMiddle(length: number): number[] {
  const indexes: number[] = []
  for (let offset = 0; offset < length; offset += 1) {
    const middle = Math.floor((length - 1) / 2)
    const left = middle - offset
    const right = middle + offset + 1
    if (left >= 0) indexes.push(left)
    if (right < length) indexes.push(right)
  }
  return indexes
}

function markStructureDropped(input: AuditLogInput): AuditLogInput {
  return { ...input, captureStatus: input.captureStatus === 'overflow' ? 'overflow' : 'dropped' }
}

function bodyBytes(body: Buffer | string | undefined): number {
  if (body === undefined) return 0
  return Buffer.isBuffer(body) ? body.byteLength : Buffer.byteLength(body, 'utf8')
}

function headerBytes(headers: Record<string, string | string[]> | undefined): number {
  return headers ? Buffer.byteLength(JSON.stringify(headers), 'utf8') : 0
}

function truncatePayloadStrings(payload: AuditLogPayloadInput): AuditLogPayloadInput {
  return {
    ...payload,
    id: truncateOptionalString(payload.id, 256),
    attemptTempId: truncateOptionalString(payload.attemptTempId, 256),
    contentType: truncateOptionalString(payload.contentType, 512),
    contentEncoding: truncateOptionalString(payload.contentEncoding, 128),
    createdAt: truncateOptionalString(payload.createdAt, 128)
  }
}

function truncateAuditLogStrings(input: AuditLogInput): AuditLogInput {
  return {
    ...input,
    id: truncateOptionalString(input.id, 256),
    traceId: truncateString(input.traceId, 256),
    conversationKey: truncateOptionalString(input.conversationKey, 256),
    sessionId: truncateOptionalString(input.sessionId, 512),
    sessionClientType: truncateOptionalString(input.sessionClientType, 128),
    systemAccountId: truncateOptionalString(input.systemAccountId, 256),
    apiKeyId: truncateOptionalString(input.apiKeyId, 256),
    groupId: truncateOptionalString(input.groupId, 256),
    accountId: truncateOptionalString(input.accountId, 256),
    providerCode: truncateOptionalString(input.providerCode, 128),
    method: truncateString(input.method, 32),
    path: truncateString(input.path, 2048),
    queryString: truncateOptionalString(input.queryString, 4096),
    model: truncateOptionalString(input.model, 512),
    upstreamModel: truncateOptionalString(input.upstreamModel, 512),
    pricingModel: truncateOptionalString(input.pricingModel, 512),
    modelMappingSource: truncateOptionalString(input.modelMappingSource, 128),
    clientIp: truncateOptionalString(input.clientIp, 256),
    userAgent: truncateOptionalString(input.userAgent, 2048),
    errorPhase: truncateOptionalString(input.errorPhase, 256),
    errorCode: truncateOptionalString(input.errorCode, 512),
    errorMessage: truncateOptionalString(input.errorMessage, 4096),
    sampleReason: truncateString(input.sampleReason, 1024),
    startedAt: truncateString(input.startedAt, 128),
    endedAt: truncateString(input.endedAt, 128),
    httpCompletedAt: truncateOptionalString(input.httpCompletedAt, 128),
    createdAt: truncateOptionalString(input.createdAt, 128)
  }
}

function truncateAttemptStrings(attempt: AuditLogAttemptInput): AuditLogAttemptInput {
  return {
    ...attempt,
    id: truncateOptionalString(attempt.id, 256),
    tempId: truncateOptionalString(attempt.tempId, 256),
    accountId: truncateOptionalString(attempt.accountId, 256),
    accountOwnerSystemAccountId: truncateOptionalString(attempt.accountOwnerSystemAccountId, 256),
    groupId: truncateOptionalString(attempt.groupId, 256),
    proxyUrl: truncateOptionalString(attempt.proxyUrl, 2048),
    providerCode: truncateOptionalString(attempt.providerCode, 128),
    model: truncateOptionalString(attempt.model, 512),
    upstreamModel: truncateOptionalString(attempt.upstreamModel, 512),
    pricingModel: truncateOptionalString(attempt.pricingModel, 512),
    modelMappingSource: truncateOptionalString(attempt.modelMappingSource, 128),
    upstreamMethod: truncateString(attempt.upstreamMethod, 32),
    upstreamUrl: truncateString(attempt.upstreamUrl, 4096),
    errorPhase: truncateOptionalString(attempt.errorPhase, 256),
    errorCode: truncateOptionalString(attempt.errorCode, 512),
    errorMessage: truncateOptionalString(attempt.errorMessage, 4096),
    startedAt: truncateString(attempt.startedAt, 128),
    endedAt: truncateOptionalString(attempt.endedAt, 128)
  }
}

function truncateOptionalString(value: string | undefined, maxBytes: number): string | undefined {
  return typeof value === 'string' ? truncateString(value, maxBytes) : undefined
}

function truncateString(value: string, maxBytes: number): string {
  if (Buffer.byteLength(value, 'utf8') <= maxBytes) return value
  return `${Buffer.from(value).subarray(0, Math.max(0, maxBytes - 32)).toString('utf8')}...[truncated]`
}
