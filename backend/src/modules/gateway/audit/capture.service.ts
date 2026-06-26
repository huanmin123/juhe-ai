import { createHash, randomUUID } from 'node:crypto'
import type { Request, Response } from 'express'

import { nowIso } from '../../../storage/database.js'
import { logger } from '../../../shared/logger.js'
import { sanitizeUrlCredentialsForLog, sanitizeUrlForLog } from '../../../shared/request-context.js'
import type {
  AuditLogAttemptInput,
  AuditLogInput,
  AuditLogPayloadInput,
  AuditOutcome,
  AuditPayloadPartType,
  OpenAIAccountSecret
} from '../../../storage/repositories.js'
import { enqueueAuditLog } from '../../audit-logs/audit-log-queue.service.js'
import { readAuditLogSettings } from '../../audit-logs/audit-log-settings.js'
import { requestModel, requestStream } from '../request/metadata.js'
import {
  headersToSafeObject,
  sanitizeHeaderRecord,
  sanitizeHeaderValue
} from '../upstream/headers.js'
import {
  normalizeOpenAIGatewayTrafficSource,
  type OpenAIGatewayTrafficSource
} from '../usage/traffic-source.js'
import { OPENAI_PROTOCOL_CODE } from '../../../domain/provider-protocol.js'
import { resolveCatalogPricingModel } from '../../model-pricing/model-catalog.service.js'
import { resolveGatewayUsageModel } from '../../providers/drivers/registry.js'
import { gatewayRequestEndpointFamily } from '../protocols/openai-v1/model-mapping.js'

type RawBodyRequest = Request & { rawBody?: Buffer }

interface AuditCaptureContextInput {
  req: Request
  traceId: string
  clientIp?: string
  startedAtMs: number
  trafficSource?: OpenAIGatewayTrafficSource
  captureMode?: 'default' | 'metadata_only'
}

interface AuditGatewayContext {
  systemAccountId?: string
  apiKeyId?: string
  groupId?: string
  accountId?: string
  providerCode?: string
  upstreamModel?: string
  pricingModel?: string
  modelMappingApplied?: boolean
  modelMappingSource?: string
  trafficSource?: OpenAIGatewayTrafficSource
}

interface FinalizeAuditInput {
  outcome: AuditOutcome
  success: boolean
  statusCode?: number
  responseHeaders?: Record<string, string | string[]> | Headers
  responseBody?: Buffer | string
  responsePartType?: Extract<AuditPayloadPartType, 'gateway_response' | 'gateway_error'>
  errorPhase?: string
  errorCode?: string
  errorMessage?: string
  accountId?: string
  firstTokenMs?: number
}

interface StartAttemptInput {
  account: OpenAIAccountSecret
  attemptIndex: number
  upstreamUrl: string
  method: string
  headers: Headers
  body?: Buffer | string
}

interface CompleteAttemptInput {
  statusCode?: number
  responseHeaders?: Headers
  responseBody?: Buffer | string
  success: boolean
  errorPhase?: string
  errorCode?: string
  errorMessage?: string
}

interface AddGatewayMetadataInput {
  metadata: Record<string, unknown>
  label?: string
}

interface OmitPayloadBodiesInput extends AddGatewayMetadataInput {
  partTypes?: AuditPayloadPartType[]
}

type PendingAuditPayloadInput = Omit<AuditLogPayloadInput, 'sequenceIndex'>

interface AuditAttemptState {
  tempId: string
  attempt: AuditLogAttemptInput
  requestPayload?: PendingAuditPayloadInput
  requestPayloadCaptured: boolean
  startedAtMs: number
  completed: boolean
}

let activeAuditCaptureCount = 0

const failedAuditFullBodyLimitBytes = 2 * 1024 * 1024
const successAuditFullBodyLimitBytes = 512 * 1024
const auditBodySummaryEdgeBytes = 256 * 1024
const auditBodySummaryTextPreviewBytes = 4 * 1024
const auditJsonSummaryParseMaxBytes = 512 * 1024
const auditJsonSummaryMaxKeys = 50
const auditInlineSha256MaxBytes = 1024 * 1024
const auditPayloadSummaryContentType = 'application/json; audit=payload-summary'
const auditActiveCaptureHardLimitBytes = 64 * 1024 * 1024

export class AuditCaptureContext {
  private readonly req: Request
  private readonly traceId: string
  private readonly clientIp?: string
  private readonly startedAtMs: number
  private readonly startedAtIso: string
  private readonly trafficSource: OpenAIGatewayTrafficSource
  private readonly sampleBucket: number
  private readonly successCaptureSelected: boolean
  private readonly successHotRetentionEnabled: boolean
  private readonly metadataOnly: boolean
  private readonly capturePayloadBodies: boolean
  private readonly enabled: boolean
  private readonly successSampleRate: number
  private readonly activeCaptureMaxBytes: number
  private readonly payloads: AuditLogPayloadInput[] = []
  private readonly attempts: AuditLogAttemptInput[] = []
  private gatewayContext: AuditGatewayContext = { providerCode: OPENAI_PROTOCOL_CODE }
  private activeAttemptByTempId = new Map<string, AuditAttemptState>()
  private finalized = false
  private hadFailedAttempt = false
  private clientAborted = false
  private overflowed = false
  private approximateBytes = 0
  private sequenceIndex = 0
  private clientRequestPayloadCaptured = false

  constructor(input: AuditCaptureContextInput) {
    const settings = readAuditLogSettings()
    this.enabled = settings.enabled
    this.successSampleRate = settings.successSampleRate
    this.activeCaptureMaxBytes = Math.min(settings.activeCaptureMaxBytes, auditActiveCaptureHardLimitBytes)
    this.req = input.req
    this.traceId = input.traceId
    this.clientIp = input.clientIp
    this.startedAtMs = input.startedAtMs
    this.startedAtIso = new Date(input.startedAtMs).toISOString()
    this.trafficSource = normalizeOpenAIGatewayTrafficSource(input.trafficSource)
    this.sampleBucket = sampleBucketForTraceId(this.traceId)
    this.successCaptureSelected = this.sampleBucket < Math.round(this.successSampleRate * 10000)
    this.successHotRetentionEnabled = settings.successHotRetentionHours > 0
    this.metadataOnly = input.captureMode === 'metadata_only'
    this.capturePayloadBodies = settings.fullBodyCaptureEnabled && !this.metadataOnly
    if (!this.enabled) {
      return
    }
    activeAuditCaptureCount += 1
    this.gatewayContext.trafficSource = this.trafficSource
    if (this.metadataOnly) {
      this.addPayload({
        partType: 'gateway_metadata',
        body: JSON.stringify({
          type: 'gateway_metadata',
          label: 'traffic_source',
          metadata: { trafficSource: this.trafficSource, captureMode: 'metadata_only' }
        }),
        contentType: 'application/json; audit=gateway-metadata'
      })
      return
    }
    if (this.shouldCaptureSuccessPayloads()) {
      this.addClientRequestPayload()
    }
  }

  bindContext(context: AuditGatewayContext): void {
    this.gatewayContext = {
      ...this.gatewayContext,
      ...Object.fromEntries(Object.entries(context).filter(([, value]) => value !== undefined))
    }
  }

  markClientAborted(): void {
    this.clientAborted = true
  }

  shouldCaptureSuccessPayloads(): boolean {
    return this.enabled && this.capturePayloadBodies && (
      this.successHotRetentionEnabled
      || this.successCaptureSelected
      || this.hadFailedAttempt
    )
  }

  addGatewayMetadata(input: AddGatewayMetadataInput): void {
    if (!this.enabled) return
    this.addPayload({
      partType: 'gateway_metadata',
      body: JSON.stringify({
        type: 'gateway_metadata',
        label: input.label,
        metadata: input.metadata
      }),
      contentType: 'application/json; audit=gateway-metadata'
    })
  }

  omitPayloadBodies(input: OmitPayloadBodiesInput): void {
    if (!this.enabled) return
    const partTypes = input.partTypes ? new Set(input.partTypes) : undefined
    let omittedPayloadCount = 0
    let omittedBodyBytes = 0
    for (const payload of this.payloads) {
      if (partTypes && !partTypes.has(payload.partType)) {
        continue
      }
      if (!shouldOmitExistingPayloadBody(payload.partType) || payload.body === undefined) {
        continue
      }
      const bodyBuffer = bodyToBuffer(payload.body)
      omittedPayloadCount += 1
      omittedBodyBytes += bodyBuffer.byteLength
      payload.bodySha256 = payload.bodySha256 ?? sha256BufferIfSmall(bodyBuffer)
      payload.rawBodySizeBytes = payload.rawBodySizeBytes ?? bodyBuffer.byteLength
      payload.captureStatus = 'hash_only'
      payload.body = undefined
      payload.contentEncoding = undefined
    }
    if (omittedPayloadCount > 0) {
      this.recalculateApproximateBytes()
    }
    this.addGatewayMetadata({
      label: input.label,
      metadata: {
        ...input.metadata,
        auditBodyPayloadsOmitted: true,
        omittedPayloadCount,
        omittedBodyBytes
      }
    })
  }

  startAttempt(input: StartAttemptInput): string {
    if (!this.enabled) return ''
    this.bindContext({ providerCode: input.account.providerCode })
    this.bindContext(auditModelAccounting(input.account, requestModel(this.req), this.gatewayContext.systemAccountId, gatewayRequestEndpointFamily(this.req)))
    const tempId = `attempt_${input.attemptIndex}_${Date.now()}_${Math.random().toString(16).slice(2, 8)}`
    const startedAtMs = Date.now()
    const attempt: AuditLogAttemptInput = {
      id: `audatt_${Date.now()}_${randomUUID()}`,
      tempId,
      attemptIndex: input.attemptIndex,
      accountId: input.account.id,
      accountOwnerSystemAccountId: input.account.accountOwnerSystemAccountId,
      groupId: this.gatewayContext.groupId,
      proxyUrl: sanitizeUrlCredentialsForLog(input.account.proxyUrl),
      providerCode: input.account.providerCode,
      upstreamMethod: input.method,
      upstreamUrl: sanitizeUrlCredentialsForLog(input.upstreamUrl) ?? 'unknown',
      startedAt: new Date(startedAtMs).toISOString()
    }
    this.attempts.push(attempt)
    const requestPayload: PendingAuditPayloadInput = {
      attemptTempId: tempId,
      partType: 'upstream_request',
      headers: headersToSafeObject(input.headers),
      body: input.body,
      contentType: input.headers.get('content-type') ?? undefined,
      contentEncoding: input.headers.get('content-encoding') ?? undefined
    }
    const state: AuditAttemptState = { tempId, attempt, requestPayload, requestPayloadCaptured: false, startedAtMs, completed: false }
    this.activeAttemptByTempId.set(tempId, state)
    if (this.shouldCaptureSuccessPayloads()) {
      this.addClientRequestPayload()
      this.addPayload(requestPayload)
      state.requestPayloadCaptured = true
    }
    return tempId
  }

  completeAttempt(tempId: string, input: CompleteAttemptInput): void {
    if (!this.enabled) return
    const state = this.activeAttemptByTempId.get(tempId)
    if (!state || state.completed) return

    state.completed = true
    const endedAtMs = Date.now()
    state.attempt.endedAt = new Date(endedAtMs).toISOString()
    state.attempt.durationMs = endedAtMs - state.startedAtMs
    state.attempt.upstreamStatusCode = input.statusCode
    state.attempt.success = input.success
    state.attempt.errorPhase = input.errorPhase
    state.attempt.errorCode = sanitizeOptionalDiagnosticMessage(input.errorCode)
    state.attempt.errorMessage = sanitizeOptionalDiagnosticMessage(input.errorMessage)
    if (!input.success) {
      this.hadFailedAttempt = true
    }
    if (this.capturePayloadBodies && !input.success && state.requestPayload && !state.requestPayloadCaptured) {
      this.addPayload(state.requestPayload)
      state.requestPayloadCaptured = true
    }
    if (
      this.capturePayloadBodies
      && (!input.success || this.shouldCaptureSuccessPayloads())
      && (input.responseHeaders || input.responseBody !== undefined)
    ) {
      this.addPayload({
        attemptTempId: tempId,
        partType: 'upstream_response',
        headers: input.responseHeaders ? headersToSafeObject(input.responseHeaders) : undefined,
        body: input.responseBody,
        contentType: input.responseHeaders?.get('content-type') ?? undefined,
        contentEncoding: input.responseHeaders?.get('content-encoding') ?? undefined
      })
    }
  }

  finalize(input: FinalizeAuditInput): void {
    if (this.finalized) return
    this.finalized = true
    if (!this.enabled) return
    activeAuditCaptureCount = Math.max(0, activeAuditCaptureCount - 1)

    const endedAtMs = Date.now()
    const clientAborted = this.clientAborted && !input.success
    const outcome = clientAborted
      ? 'client_aborted'
      : input.success && this.hadFailedAttempt
        ? 'success_after_retry'
        : input.outcome
    const success = input.success && outcome !== 'client_aborted'
    if (input.accountId) {
      this.bindContext({ accountId: input.accountId })
    }
    const shouldCapture = this.metadataOnly || outcome !== 'success' || this.successHotRetentionEnabled || this.successCaptureSelected
    if (!shouldCapture) {
      logger.debug({
        event: 'gateway_audit_capture_skipped',
        traceId: this.traceId,
        outcome,
        success,
        successHotRetentionEnabled: this.successHotRetentionEnabled,
        successCaptureSelected: this.successCaptureSelected,
        sampleBucket: this.sampleBucket
      }, '网关审计捕获已按采样策略跳过')
      return
    }

    const shouldCapturePayloadBodies = this.capturePayloadBodies && (
      outcome !== 'success'
      || this.successHotRetentionEnabled
      || this.successCaptureSelected
    )
    if (outcome !== 'success' || this.shouldCaptureSuccessPayloads()) {
      this.addClientRequestPayload()
    }
    if (shouldCapturePayloadBodies && (input.responseBody !== undefined || input.responseHeaders)) {
      this.addPayload({
        partType: input.responsePartType ?? (input.success ? 'gateway_response' : 'gateway_error'),
        headers: input.responseHeaders ? normalizeSafeHeaders(input.responseHeaders) : undefined,
        body: input.responseBody,
        contentType: headerValue(input.responseHeaders, 'content-type'),
        contentEncoding: headerValue(input.responseHeaders, 'content-encoding')
      })
    }
    this.applyPayloadRetention(outcome === 'success' ? 'success' : 'failure')
    const sanitizedOriginalUrl = sanitizeUrlForLog(this.req.originalUrl)
    const auditLog: AuditLogInput = {
      id: `audit_${Date.now()}_${randomUUID()}`,
      traceId: this.traceId,
      ...this.gatewayContext,
      accountId: input.accountId ?? this.gatewayContext.accountId,
      providerCode: this.gatewayContext.providerCode,
      trafficSource: this.gatewayContext.trafficSource ?? this.trafficSource,
      method: this.req.method.toUpperCase(),
      path: sanitizedOriginalUrl.split('?')[0] || this.req.path,
      queryString: sanitizedOriginalUrl.includes('?') ? sanitizedOriginalUrl.split('?').slice(1).join('?') : undefined,
      model: requestModel(this.req),
      upstreamModel: this.gatewayContext.upstreamModel,
      pricingModel: this.gatewayContext.pricingModel,
      modelMappingApplied: this.gatewayContext.modelMappingApplied,
      modelMappingSource: this.gatewayContext.modelMappingSource,
      stream: requestStream(this.req),
      clientIp: this.clientIp,
      userAgent: this.req.header('user-agent'),
      auditOutcome: outcome,
      success,
      finalStatusCode: input.statusCode,
      errorPhase: clientAborted ? input.errorPhase ?? 'client' : input.errorPhase,
      errorCode: sanitizeOptionalDiagnosticMessage(input.errorCode),
      errorMessage: clientAborted
        ? sanitizeOptionalDiagnosticMessage(input.errorMessage) ?? 'Client aborted request'
        : sanitizeOptionalDiagnosticMessage(input.errorMessage),
      sampleBucket: this.sampleBucket,
      sampleReason: this.sampleReasonForOutcome(outcome),
      captureStatus: this.overflowed ? 'overflow' : 'complete',
      startedAt: this.startedAtIso,
      endedAt: new Date(endedAtMs).toISOString(),
      durationMs: endedAtMs - this.startedAtMs,
      firstTokenMs: input.firstTokenMs,
      attempts: this.attempts,
      payloads: this.payloads
    }
    logger.debug({
      event: 'gateway_audit_capture_finalized',
      traceId: this.traceId,
      outcome,
      success,
      payloadCount: this.payloads.length,
      attemptCount: this.attempts.length,
      captureStatus: auditLog.captureStatus,
      sampleReason: auditLog.sampleReason
    }, '网关审计捕获已完成，准备投递')
    enqueueAuditLog(auditLog)
  }

  private addClientRequestPayload(): void {
    if (!this.enabled || !this.capturePayloadBodies || this.clientRequestPayloadCaptured) return
    const rawBody = (this.req as RawBodyRequest).rawBody
    this.clientRequestPayloadCaptured = true
    this.addPayload({
      partType: 'client_request',
      headers: requestHeadersToObject(this.req),
      body: rawBody,
      contentType: this.req.header('content-type'),
      contentEncoding: this.req.header('content-encoding')
    })
  }

  private addPayload(payload: Omit<AuditLogPayloadInput, 'sequenceIndex'>): void {
    if (!this.enabled) return
    if (this.overflowed) return
    summarizePayloadForLimit(payload, failedAuditFullBodyLimitBytes)
    const nextApproximateBytes = this.approximateBytes + estimatePayloadBytes(payload)
    if (nextApproximateBytes > this.activeCaptureMaxBytes) {
      this.overflowed = true
      this.payloads.length = 0
      this.approximateBytes = nextApproximateBytes
      return
    }
    this.payloads.push({
      ...payload,
      id: `audpay_${Date.now()}_${randomUUID()}`,
      sequenceIndex: this.sequenceIndex,
      createdAt: nowIso()
    })
    this.approximateBytes = nextApproximateBytes
    this.sequenceIndex += 1
  }

  private recalculateApproximateBytes(): void {
    this.approximateBytes = this.payloads.reduce((total, payload) => total + estimatePayloadBytes(payload), 0)
  }

  private applyPayloadRetention(mode: 'success' | 'failure'): void {
    const fullBodyLimit = mode === 'success'
      ? successAuditFullBodyLimitBytes
      : failedAuditFullBodyLimitBytes
    for (const payload of this.payloads) {
      summarizePayloadForLimit(payload, fullBodyLimit)
    }
    this.recalculateApproximateBytes()
  }

  private sampleReasonForOutcome(outcome: AuditOutcome): string {
    if (this.metadataOnly) {
      return `${this.trafficSource}_metadata_only`
    }
    if (outcome !== 'success') {
      return 'full_capture'
    }
    if (this.successCaptureSelected) {
      return `success_sample_${this.successSampleRate}`
    }
    return 'success_hot_full_retention'
  }
}

function sanitizeOptionalDiagnosticMessage(value: string | undefined): string | undefined {
  return value
}

function auditModelAccounting(
  account: OpenAIAccountSecret,
  requestedModel: string | undefined,
  fallbackSystemAccountId: string | undefined,
  sourceEndpointFamily: ReturnType<typeof gatewayRequestEndpointFamily>
): Pick<AuditGatewayContext, 'upstreamModel' | 'pricingModel' | 'modelMappingApplied' | 'modelMappingSource'> {
  const resolved = resolveGatewayUsageModel(account, requestedModel, sourceEndpointFamily)
  const upstreamModel = resolved.upstreamModel ?? requestedModel
  const catalogSystemAccountId = account.accountOwnerSystemAccountId || fallbackSystemAccountId
  return {
    upstreamModel,
    pricingModel: upstreamModel
      ? resolveCatalogPricingModel({
        providerCode: account.providerCode,
        systemAccountId: catalogSystemAccountId,
        model: upstreamModel
      })
      : undefined,
    modelMappingApplied: resolved.modelMappingApplied,
    modelMappingSource: resolved.modelMappingSource
  }
}

function shouldOmitExistingPayloadBody(partType: AuditPayloadPartType): boolean {
  return partType !== 'gateway_metadata'
}

export function createAuditCapture(input: AuditCaptureContextInput): AuditCaptureContext {
  return new AuditCaptureContext(input)
}

export function getActiveAuditCaptureCount(): number {
  return activeAuditCaptureCount
}

export function responseHeadersToObject(res: Response): Record<string, string | string[]> {
  const output: Record<string, string | string[]> = {}
  for (const [name, value] of Object.entries(res.getHeaders())) {
    if (value === undefined) continue
    if (Array.isArray(value)) {
      output[name] = value.map(String)
    } else {
      output[name] = String(value)
    }
  }
  return output
}

function requestHeadersToObject(req: Request): Record<string, string | string[]> {
  const output: Record<string, string | string[]> = {}
  for (const [name, value] of Object.entries(req.headers)) {
    if (value === undefined) continue
    output[name] = sanitizeHeaderValue(name, Array.isArray(value) ? value : String(value))
  }
  return output
}

function normalizeSafeHeaders(headers: Record<string, string | string[]> | Headers): Record<string, string | string[]> {
  if (headers instanceof Headers) {
    return headersToSafeObject(headers)
  }
  return sanitizeHeaderRecord(headers)
}

function headerValue(headers: Record<string, string | string[]> | Headers | undefined, name: string): string | undefined {
  if (!headers) return undefined
  if (headers instanceof Headers) return headers.get(name) ?? undefined
  const value = headers[name] ?? headers[name.toLowerCase()]
  return Array.isArray(value) ? value.join(', ') : value
}

function summarizePayloadForLimit(payload: Omit<AuditLogPayloadInput, 'sequenceIndex'>, fullBodyLimitBytes: number): void {
  if (payload.partType === 'gateway_metadata' || payload.body === undefined) {
    return
  }
  if (payload.captureStatus && payload.captureStatus !== 'complete') {
    updateExistingPayloadSummaryLimit(payload, fullBodyLimitBytes)
    return
  }
  const bodyBuffer = bodyToBuffer(payload.body)
  const originalBodySizeBytes = payload.rawBodySizeBytes ?? bodyBuffer.byteLength
  if (originalBodySizeBytes <= fullBodyLimitBytes) {
    return
  }
  const originalContentType = payload.contentType
  const originalContentEncoding = payload.contentEncoding
  const originalSha256 = payload.bodySha256 ?? sha256Buffer(bodyBuffer)
  payload.body = JSON.stringify(buildPayloadSummary({
    body: bodyBuffer,
    originalSha256,
    originalBodySizeBytes,
    originalContentType,
    originalContentEncoding,
    fullBodyLimitBytes
  }))
  payload.bodySha256 = originalSha256
  payload.rawBodySizeBytes = originalBodySizeBytes
  payload.captureStatus = 'summary_only'
  payload.contentType = auditPayloadSummaryContentType
  payload.contentEncoding = undefined
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

function buildPayloadSummary(input: {
  body: Buffer
  originalSha256?: string
  originalBodySizeBytes: number
  originalContentType?: string
  originalContentEncoding?: string
  fullBodyLimitBytes: number
}): Record<string, unknown> {
  const head = input.body.subarray(0, Math.min(auditBodySummaryEdgeBytes, input.body.byteLength))
  const tailStart = Math.max(0, input.body.byteLength - auditBodySummaryEdgeBytes)
  const tail = input.body.subarray(tailStart)
  const hasSeparatedTail = tailStart >= head.byteLength
  const retainedBodyBytes = head.byteLength + (hasSeparatedTail ? tail.byteLength : Math.max(0, input.body.byteLength - head.byteLength))
  const summary: Record<string, unknown> = {
    type: 'audit_payload_summary',
    captureStatus: 'summary_only',
    reason: 'body_exceeded_full_capture_limit',
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

function summarizeJsonPayload(body: Buffer, contentType?: string, contentEncoding?: string): Record<string, unknown> | undefined {
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

function sampleBucketForTraceId(traceId: string): number {
  const digest = createHash('sha256').update(traceId).digest()
  return digest.readUInt32BE(0) % 10000
}

function estimatePayloadBytes(payload: Omit<AuditLogPayloadInput, 'sequenceIndex'>): number {
  const body = payload.body
  const bodyBytes = payloadBodyByteLength(body)
  const headerBytes = payload.headers ? estimateHeadersBytes(payload.headers) : 0
  return bodyBytes + headerBytes + 512
}

function payloadBodyByteLength(body: Buffer | string | undefined): number {
  return Buffer.isBuffer(body) ? body.byteLength : typeof body === 'string' ? Buffer.byteLength(body, 'utf8') : 0
}

function bodyToBuffer(body: Buffer | string): Buffer {
  return Buffer.isBuffer(body) ? body : Buffer.from(body, 'utf8')
}

function sha256Buffer(buffer: Buffer): string {
  return createHash('sha256').update(buffer).digest('hex')
}

function sha256BufferIfSmall(buffer: Buffer): string | undefined {
  return buffer.byteLength <= auditInlineSha256MaxBytes ? sha256Buffer(buffer) : undefined
}

function estimateHeadersBytes(headers: Record<string, string | string[]>): number {
  let bytes = 2
  for (const [name, value] of Object.entries(headers)) {
    bytes += Buffer.byteLength(name, 'utf8') + 4
    if (Array.isArray(value)) {
      bytes += 2
      for (const item of value) {
        bytes += Buffer.byteLength(item, 'utf8') + 3
      }
    } else {
      bytes += Buffer.byteLength(value, 'utf8') + 2
    }
  }
  return bytes
}
