import { createHash } from 'node:crypto'

import { estimateJsonLikeBytes } from '../../shared/queue-size.js'
import type { AuditLogInput } from '../../storage/repositories.js'

export const auditWorkerMessageMaxBytes = 4 * 1024 * 1024

const auditWorkerPayloadBodyInlineMaxBytes = 2 * 1024 * 1024
const auditWorkerMessageEstimateMaxBytes = 8 * 1024 * 1024 + 1
const auditWorkerMessageEstimateMaxNodes = 20_000

export function trimAuditLogsForWorkerIpc(items: AuditLogInput[]): AuditLogInput[] {
  return items.map(trimAuditLogForWorkerIpc)
}

function trimAuditLogForWorkerIpc(item: AuditLogInput): AuditLogInput {
  let next = trimAuditLogTopLevelStringsForWorkerIpc(item)
  next = trimAuditLogPayloadBodiesForWorkerIpc(next)
  if (estimateAuditLogBytes(next) <= auditWorkerMessageMaxBytes) {
    return next
  }
  next = trimAuditLogPayloadHeadersForWorkerIpc(next)
  if (estimateAuditLogBytes(next) <= auditWorkerMessageMaxBytes) {
    return next
  }
  next = trimAuditLogAttemptsForWorkerIpc(next)
  if (estimateAuditLogBytes(next) <= auditWorkerMessageMaxBytes) {
    return next
  }
  return trimAuditLogPayloadsForWorkerIpc(next)
}

function trimAuditLogTopLevelStringsForWorkerIpc(item: AuditLogInput): AuditLogInput {
  return {
    ...item,
    path: truncateAuditIpcString(item.path, 2048),
    queryString: truncateOptionalAuditIpcString(item.queryString, 4096),
    model: truncateOptionalAuditIpcString(item.model, 512),
    upstreamModel: truncateOptionalAuditIpcString(item.upstreamModel, 512),
    pricingModel: truncateOptionalAuditIpcString(item.pricingModel, 512),
    modelMappingSource: truncateOptionalAuditIpcString(item.modelMappingSource, 128),
    clientIp: truncateOptionalAuditIpcString(item.clientIp, 256),
    userAgent: truncateOptionalAuditIpcString(item.userAgent, 2048),
    errorPhase: truncateOptionalAuditIpcString(item.errorPhase, 256),
    errorCode: truncateOptionalAuditIpcString(item.errorCode, 512),
    errorMessage: truncateOptionalAuditIpcString(item.errorMessage, 4096),
    sampleReason: truncateAuditIpcString(item.sampleReason, 1024)
  }
}

function trimAuditLogPayloadBodiesForWorkerIpc(item: AuditLogInput): AuditLogInput {
  let bytes = 2048 + auditAttemptsBytes(item.attempts)
  let changed = false
  const payloads: AuditLogInput['payloads'] = item.payloads.map((payload) => {
    const headerBytes = payload.headers ? estimateJsonBytes(payload.headers) : 0
    const bodyBytes = auditPayloadBodyBytes(payload.body)
    const nextBytes = headerBytes + bodyBytes + 512
    const shouldDropBody = bodyBytes > auditWorkerPayloadBodyInlineMaxBytes || bytes + nextBytes > auditWorkerMessageMaxBytes
    bytes += shouldDropBody ? headerBytes + 512 : nextBytes
    if (!shouldDropBody) {
      return payload
    }
    changed = true
    return trimAuditPayloadBodyForWorkerIpc(payload, bodyBytes)
  })
  return changed ? markAuditLogDroppedForWorkerIpc(item, payloads) : item
}

function trimAuditLogPayloadHeadersForWorkerIpc(item: AuditLogInput): AuditLogInput {
  let changed = false
  const payloads: AuditLogInput['payloads'] = item.payloads.map((payload) => {
    if (!payload.headers) {
      return payload
    }
    changed = true
    return {
      ...payload,
      headers: undefined,
      captureStatus: payload.captureStatus === 'complete' || payload.captureStatus === undefined
        ? 'dropped' as const
        : payload.captureStatus
    }
  })
  return changed ? markAuditLogDroppedForWorkerIpc(item, payloads) : item
}

function trimAuditLogAttemptsForWorkerIpc(item: AuditLogInput): AuditLogInput {
  const attempts = item.attempts.map((attempt) => ({
    ...attempt,
    proxyUrl: truncateOptionalAuditIpcString(attempt.proxyUrl, 2048),
    upstreamUrl: truncateAuditIpcString(attempt.upstreamUrl, 4096),
    errorPhase: truncateOptionalAuditIpcString(attempt.errorPhase, 256),
    errorCode: truncateOptionalAuditIpcString(attempt.errorCode, 512),
    errorMessage: truncateOptionalAuditIpcString(attempt.errorMessage, 4096)
  }))
  const maxAttempts = 16
  const limitedAttempts = attempts.length <= maxAttempts
    ? attempts
    : [
        ...attempts.slice(0, maxAttempts - 1),
        attempts[attempts.length - 1]
      ]
  return limitedAttempts === item.attempts
    ? item
    : {
        ...item,
        attempts: limitedAttempts,
        captureStatus: item.captureStatus === 'overflow' ? 'overflow' : 'dropped'
      }
}

function trimAuditLogPayloadsForWorkerIpc(item: AuditLogInput): AuditLogInput {
  const maxPayloads = 32
  const payloads = item.payloads.map((payload) => trimAuditPayloadForWorkerIpc(payload))
  const limitedPayloads = payloads.length <= maxPayloads
    ? payloads
    : [
        ...payloads.slice(0, maxPayloads - 1),
        payloads[payloads.length - 1]
      ]
  return markAuditLogDroppedForWorkerIpc(item, limitedPayloads)
}

function trimAuditPayloadBodyForWorkerIpc(
  payload: AuditLogInput['payloads'][number],
  bodyBytes = auditPayloadBodyBytes(payload.body)
): AuditLogInput['payloads'][number] {
  const bodySha256 = payload.bodySha256 ?? auditPayloadBodySha256(payload.body)
  return {
    ...payload,
    body: undefined,
    bodySha256,
    contentEncoding: undefined,
    rawBodySizeBytes: payload.rawBodySizeBytes ?? bodyBytes,
    captureStatus: bodySha256 ? 'hash_only' as const : 'dropped' as const
  }
}

function trimAuditPayloadForWorkerIpc(payload: AuditLogInput['payloads'][number]): AuditLogInput['payloads'][number] {
  return {
    ...trimAuditPayloadBodyForWorkerIpc(payload),
    headers: undefined,
    contentType: truncateOptionalAuditIpcString(payload.contentType, 512),
    attemptTempId: truncateOptionalAuditIpcString(payload.attemptTempId, 128)
  }
}

function markAuditLogDroppedForWorkerIpc(
  item: AuditLogInput,
  payloads: AuditLogInput['payloads']
): AuditLogInput {
  return {
    ...item,
    payloads,
    captureStatus: item.captureStatus === 'overflow' ? 'overflow' : 'dropped'
  }
}

function auditPayloadBodyBytes(body: Buffer | string | undefined): number {
  if (body === undefined) return 0
  return Buffer.isBuffer(body) ? body.byteLength : estimateJsonBytes(body)
}

function auditPayloadBodySha256(body: Buffer | string | undefined): string | undefined {
  if (body === undefined) return undefined
  return createHash('sha256').update(Buffer.isBuffer(body) ? body : Buffer.from(body)).digest('hex')
}

function truncateOptionalAuditIpcString(value: string | undefined, maxBytes: number): string | undefined {
  return typeof value === 'string' ? truncateAuditIpcString(value, maxBytes) : undefined
}

function truncateAuditIpcString(value: string, maxBytes: number): string {
  if (Buffer.byteLength(value, 'utf8') <= maxBytes) {
    return value
  }
  return `${Buffer.from(value).subarray(0, Math.max(0, maxBytes - 32)).toString('utf8')}...[truncated]`
}

export function estimateAuditLogBytes(input: AuditLogInput): number {
  const payloadBytes = input.payloads.reduce((sum, payload) => {
    const body = payload.body
    const bodyBytes = Buffer.isBuffer(body) ? body.byteLength : typeof body === 'string' ? estimateJsonBytes(body) : 0
    const headerBytes = payload.headers ? estimateJsonBytes(payload.headers) : 0
    return Math.min(auditWorkerMessageEstimateMaxBytes, sum + bodyBytes + headerBytes + 512)
  }, 0)
  return Math.min(auditWorkerMessageEstimateMaxBytes, payloadBytes + auditAttemptsBytes(input.attempts) + estimateAuditTopLevelBytes(input) + 2048)
}

function estimateAuditTopLevelBytes(input: AuditLogInput): number {
  return estimateJsonBytes({
    traceId: input.traceId,
    trafficSource: input.trafficSource,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId,
    groupId: input.groupId,
    accountId: input.accountId,
    providerCode: input.providerCode,
    method: input.method,
    path: input.path,
    queryString: input.queryString,
    model: input.model,
    upstreamModel: input.upstreamModel,
    pricingModel: input.pricingModel,
    modelMappingApplied: input.modelMappingApplied,
    modelMappingSource: input.modelMappingSource,
    stream: input.stream,
    clientIp: input.clientIp,
    userAgent: input.userAgent,
    auditOutcome: input.auditOutcome,
    success: input.success,
    finalStatusCode: input.finalStatusCode,
    errorPhase: input.errorPhase,
    errorCode: input.errorCode,
    errorMessage: input.errorMessage,
    sampleBucket: input.sampleBucket,
    sampleReason: input.sampleReason,
    captureStatus: input.captureStatus,
    startedAt: input.startedAt,
    endedAt: input.endedAt,
    durationMs: input.durationMs,
    firstTokenMs: input.firstTokenMs,
    createdAt: input.createdAt
  })
}

function auditAttemptsBytes(attempts: AuditLogInput['attempts']): number {
  return attempts.reduce((sum, attempt) => Math.min(auditWorkerMessageEstimateMaxBytes, sum + estimateJsonBytes(attempt) + 128), 0)
}

function estimateJsonBytes(value: unknown): number {
  return estimateJsonLikeBytes(value, {
    maxBytes: auditWorkerMessageEstimateMaxBytes,
    maxNodes: auditWorkerMessageEstimateMaxNodes
  })
}
