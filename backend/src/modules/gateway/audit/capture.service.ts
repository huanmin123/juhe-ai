import { createHash, randomUUID } from 'node:crypto'
import type { Request, Response } from 'express'

import { nowIso } from '../../../storage/database.js'
import { runtimeConfig } from '../../../config/runtime.js'
import { logger } from '../../../shared/logger.js'
import { logRequestStage, sanitizeUrlCredentialsForLog, sanitizeUrlForLog } from '../../../shared/request-context.js'
import type {
  AuditLogAttemptInput,
  AuditLogInput,
  AuditLogPayloadInput,
  AuditOutcome,
  AuditPayloadPartType,
  OpenAIAccountSecret
} from '../../../storage/repositories.js'
import { enqueueAuditLog } from '../../audit-logs/audit-log-queue.service.js'
import {
  auditBodySummaryEdgeBytes,
  summarizeAuditPayloadForLimit
} from '../../audit-logs/audit-payload-summary.js'
import { readAuditLogSettings } from '../../audit-logs/audit-log-settings.js'
import { requestModel, requestStream } from '../request/metadata.js'
import {
  headersToSafeObject,
  isUncapturedHeaderName,
  sanitizeHeaderRecord,
  sanitizeHeaderValue
} from '../upstream/headers.js'
import {
  normalizeOpenAIGatewayTrafficSource,
  type OpenAIGatewayTrafficSource
} from '../usage/traffic-source.js'
import { OPENAI_PROTOCOL_CODE } from '../../../domain/provider-protocol.js'
import type {
  AccountModelMappingSourceEndpointFamily,
  AccountModelMappingUpstreamEndpointFamily
} from '../../../domain/types.js'
import { resolveCatalogPricingModel } from '../../model-pricing/model-catalog.service.js'
import { resolveGatewayUsageModel } from '../../providers/drivers/registry.js'
import { gatewayRequestEndpointFamily } from '../protocols/openai-v1/model-mapping.js'

type RawBodyRequest = Request & { rawBody?: Buffer }

interface AuditCaptureContextInput {
  req: Request
  res?: Response
  httpCompletion?: GatewayHttpCompletionObserver
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
  sourceEndpointFamily?: AccountModelMappingSourceEndpointFamily
  upstreamEndpointFamily?: AccountModelMappingUpstreamEndpointFamily
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
  requestForModelAccounting?: Request
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

interface FailedDispatchAttemptInput {
  account: OpenAIAccountSecret
  attemptIndex: number
  upstreamUrl: string
  method: string
  startedAtMs: number
  statusCode?: number
  errorPhase: string
  errorCode?: string
  errorMessage?: string
  requestForModelAccounting?: Request
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
const gatewayHttpCompletionObservers = new WeakMap<Response, GatewayHttpCompletionObserver>()

const auditInlineSha256MaxBytes = 1024 * 1024
const auditActiveCaptureHardLimitBytes = 64 * 1024 * 1024

export class AuditCaptureContext {
  private readonly req: Request
  private readonly httpCompletion?: GatewayHttpCompletionObserver
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
  private readonly successFullBodyLimitBytes: number
  private readonly problemFullBodyLimitBytes: number
  private readonly payloads: AuditLogPayloadInput[] = []
  private readonly attempts: AuditLogAttemptInput[] = []
  private gatewayContext: AuditGatewayContext = { providerCode: OPENAI_PROTOCOL_CODE }
  private activeAttemptByTempId = new Map<string, AuditAttemptState>()
  private finalized = false
  private hadFailedAttempt = false
  private clientAborted = false
  private overflowed = false
  private approximateBytes = 0
  private residentPayloadBytes = 0
  private sequenceIndex = 0
  private clientRequestPayloadCaptured = false
  private httpCompletedAtMs?: number
  private pendingFinalizeInput?: FinalizeAuditInput
  private releaseHttpCompletionListener?: () => void
  private activeCaptureRegistered = false

  constructor(input: AuditCaptureContextInput) {
    const settings = readAuditLogSettings()
    this.enabled = settings.enabled
    this.successSampleRate = settings.successSampleRate
    this.activeCaptureMaxBytes = Math.min(settings.activeCaptureMaxBytes, auditActiveCaptureHardLimitBytes)
    this.successFullBodyLimitBytes = settings.successFullBodyLimitBytes
    this.problemFullBodyLimitBytes = settings.problemFullBodyLimitBytes
    this.req = input.req
    this.httpCompletion = input.httpCompletion ?? (input.res ? observeGatewayHttpCompletion(input.res) : undefined)
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
    } else if (this.shouldCaptureSuccessPayloads()) {
      this.addClientRequestPayload()
    }
    activeAuditCaptureCount += 1
    this.activeCaptureRegistered = true
    this.releaseHttpCompletionListener = this.httpCompletion?.onCompleted(this.markHttpCompleted)
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
    const accountingRequest = input.requestForModelAccounting ?? this.req
    const requestedModel = requestModel(accountingRequest)
    const accounting = auditModelAccounting(input.account, requestedModel, this.gatewayContext.systemAccountId, gatewayRequestEndpointFamily(accountingRequest))
    this.bindContext({ providerCode: input.account.providerCode })
    this.bindContext(accounting)
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
      model: requestedModel,
      upstreamModel: accounting.upstreamModel,
      pricingModel: accounting.pricingModel,
      modelMappingApplied: accounting.modelMappingApplied,
      modelMappingSource: accounting.modelMappingSource,
      sourceEndpointFamily: accounting.sourceEndpointFamily,
      upstreamEndpointFamily: accounting.upstreamEndpointFamily,
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

  recordFailedDispatchAttempt(input: FailedDispatchAttemptInput): string {
    if (!this.enabled) return ''
    const accountingRequest = input.requestForModelAccounting ?? this.req
    const requestedModel = requestModel(accountingRequest)
    const accounting = auditModelAccounting(input.account, requestedModel, this.gatewayContext.systemAccountId, gatewayRequestEndpointFamily(accountingRequest))
    this.bindContext({ providerCode: input.account.providerCode })
    this.bindContext(accounting)
    const tempId = `attempt_${input.attemptIndex}_${Date.now()}_${Math.random().toString(16).slice(2, 8)}`
    const endedAtMs = Date.now()
    const sanitizedUpstreamUrl = sanitizeUrlCredentialsForLog(input.upstreamUrl) ?? input.upstreamUrl.trim()
    const attempt: AuditLogAttemptInput = {
      id: `audatt_${Date.now()}_${randomUUID()}`,
      tempId,
      attemptIndex: input.attemptIndex,
      accountId: input.account.id,
      accountOwnerSystemAccountId: input.account.accountOwnerSystemAccountId,
      groupId: this.gatewayContext.groupId,
      proxyUrl: sanitizeUrlCredentialsForLog(input.account.proxyUrl),
      providerCode: input.account.providerCode,
      model: requestedModel,
      upstreamModel: accounting.upstreamModel,
      pricingModel: accounting.pricingModel,
      modelMappingApplied: accounting.modelMappingApplied,
      modelMappingSource: accounting.modelMappingSource,
      sourceEndpointFamily: accounting.sourceEndpointFamily,
      upstreamEndpointFamily: accounting.upstreamEndpointFamily,
      upstreamMethod: input.method,
      upstreamUrl: sanitizedUpstreamUrl || 'unknown',
      upstreamStatusCode: input.statusCode,
      success: false,
      errorPhase: input.errorPhase,
      errorCode: sanitizeOptionalDiagnosticMessage(input.errorCode),
      errorMessage: sanitizeOptionalDiagnosticMessage(input.errorMessage),
      startedAt: new Date(input.startedAtMs).toISOString(),
      endedAt: new Date(endedAtMs).toISOString(),
      durationMs: endedAtMs - input.startedAtMs
    }
    this.attempts.push(attempt)
    this.hadFailedAttempt = true
    return tempId
  }

  finalize(input: FinalizeAuditInput): void {
    if (this.finalized) return
    this.finalized = true
    if (!this.enabled) {
      const finalizeStartedAt = performance.now()
      logRequestStage('audit.finalize', {
        traceId: this.traceId,
        auditEnabled: false,
        skipped: true
      }, 'skipped', finalizeStartedAt)
      return
    }

    this.pendingFinalizeInput = input
    if (!this.httpCompletion || this.httpCompletedAtMs !== undefined) {
      this.flushFinalizedAudit()
      return
    }
    const completedAtMs = this.httpCompletion.completedAtMs()
    if (completedAtMs !== undefined) {
      this.markHttpCompleted(completedAtMs)
    }
  }

  cancel(): void {
    if (this.finalized) return
    this.finalized = true
    this.pendingFinalizeInput = undefined
    this.releaseActiveCapture()
  }

  private readonly markHttpCompleted = (completedAtMs: number): void => {
    if (this.httpCompletedAtMs !== undefined) return
    this.httpCompletedAtMs = completedAtMs
    if (this.pendingFinalizeInput) {
      this.flushFinalizedAudit()
    }
  }

  private flushFinalizedAudit(): void {
    const input = this.pendingFinalizeInput
    if (!input) return
    const finalizeStartedAt = performance.now()
    this.pendingFinalizeInput = undefined
    this.releaseActiveCapture()

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
    const shouldCapture = outcome !== 'success' || this.successHotRetentionEnabled || this.successCaptureSelected
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
      logRequestStage('audit.finalize', {
        traceId: this.traceId,
        outcome,
        success,
        skipped: true,
        sampleBucket: this.sampleBucket
      }, 'success', finalizeStartedAt)
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
      sourceEndpointFamily: this.gatewayContext.sourceEndpointFamily,
      upstreamEndpointFamily: this.gatewayContext.upstreamEndpointFamily,
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
      httpCompletedAt: this.httpCompletedAtMs === undefined ? undefined : new Date(this.httpCompletedAtMs).toISOString(),
      httpDurationMs: this.httpCompletedAtMs === undefined ? undefined : this.httpCompletedAtMs - this.startedAtMs,
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
    logRequestStage('audit.finalize', {
      traceId: this.traceId,
      outcome,
      success,
      payloadCount: this.payloads.length,
      attemptCount: this.attempts.length,
      captureStatus: auditLog.captureStatus,
      ...(!success ? {
        failureReason: outcome,
        terminalExpectedFailure: true,
        decisionInputs: {
          statusCode: input.statusCode,
          errorPhase: input.errorPhase,
          errorCode: input.errorCode,
          attemptCount: this.attempts.length,
          clientAborted
        }
      } : {})
    }, success ? 'success' : 'expected_failure', finalizeStartedAt)
  }

  private releaseActiveCapture(): void {
    if (!this.activeCaptureRegistered) return
    this.activeCaptureRegistered = false
    activeAuditCaptureCount = Math.max(0, activeAuditCaptureCount - 1)
    this.releaseHttpCompletionListener?.()
    this.releaseHttpCompletionListener = undefined
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
    if (!shouldOffloadAuditPayloadRetention()) {
      summarizeAuditPayloadForLimit(payload, this.problemFullBodyLimitBytes)
    }
    const nextResidentPayloadBytes = this.residentPayloadBytes + estimatePayloadBytes(payload)
    if (nextResidentPayloadBytes > this.activeCaptureMaxBytes) {
      this.markResidentPayloadOverflow(nextResidentPayloadBytes)
      return
    }
    const nextApproximateBytes = this.approximateBytes + estimateRetainedPayloadBytes(payload, this.problemFullBodyLimitBytes)
    if (nextApproximateBytes > this.activeCaptureMaxBytes) {
      this.markResidentPayloadOverflow(nextResidentPayloadBytes)
      return
    }
    this.payloads.push({
      ...payload,
      id: `audpay_${Date.now()}_${randomUUID()}`,
      sequenceIndex: this.sequenceIndex,
      createdAt: nowIso()
    })
    this.approximateBytes = nextApproximateBytes
    this.residentPayloadBytes = nextResidentPayloadBytes
    this.sequenceIndex += 1
  }

  private markResidentPayloadOverflow(residentPayloadBytes: number): void {
    this.overflowed = true
    this.payloads.length = 0
    const body = JSON.stringify({
      type: 'gateway_metadata',
      label: 'active_capture_overflow',
      metadata: {
        auditBodyPayloadsOmitted: true,
        residentPayloadBytes,
        activeCaptureMaxBytes: this.activeCaptureMaxBytes
      }
    })
    const payload: AuditLogPayloadInput = {
      id: `audpay_${Date.now()}_${randomUUID()}`,
      partType: 'gateway_metadata',
      sequenceIndex: this.sequenceIndex,
      body,
      contentType: 'application/json; audit=gateway-metadata',
      captureStatus: 'overflow',
      createdAt: nowIso()
    }
    this.payloads.push(payload)
    this.approximateBytes = estimatePayloadBytes(payload)
    this.residentPayloadBytes = this.approximateBytes
    this.sequenceIndex += 1
  }

  private recalculateApproximateBytes(): void {
    this.approximateBytes = this.payloads.reduce((total, payload) => total + estimatePayloadBytes(payload), 0)
    this.residentPayloadBytes = this.approximateBytes
  }

  private applyPayloadRetention(mode: 'success' | 'failure'): void {
    const fullBodyLimit = mode === 'success'
      ? this.successFullBodyLimitBytes
      : this.problemFullBodyLimitBytes
    if (shouldOffloadAuditPayloadRetention()) {
      this.approximateBytes = this.payloads.reduce(
        (total, payload) => total + estimateRetainedPayloadBytes(payload, fullBodyLimit),
        0
      )
      return
    }
    for (const payload of this.payloads) {
      summarizeAuditPayloadForLimit(payload, fullBodyLimit)
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
): Pick<AuditGatewayContext, 'upstreamModel' | 'pricingModel' | 'modelMappingApplied' | 'modelMappingSource' | 'sourceEndpointFamily' | 'upstreamEndpointFamily'> {
  const resolved = resolveGatewayUsageModel(account, requestedModel, sourceEndpointFamily)
  const upstreamModel = resolved.upstreamModel ?? requestedModel
  const catalogSystemAccountId = account.accountOwnerSystemAccountId || fallbackSystemAccountId
  return {
    upstreamModel,
    pricingModel: upstreamModel && runtimeConfig.cacheDriver !== 'redis'
      ? resolveCatalogPricingModel({
        providerCode: account.providerCode,
        systemAccountId: catalogSystemAccountId,
        model: upstreamModel
      })
      : undefined,
    modelMappingApplied: resolved.modelMappingApplied,
    modelMappingSource: resolved.modelMappingSource,
    sourceEndpointFamily: resolved.sourceEndpointFamily,
    upstreamEndpointFamily: resolved.upstreamEndpointFamily
  }
}

function shouldOmitExistingPayloadBody(partType: AuditPayloadPartType): boolean {
  return partType !== 'gateway_metadata'
}

export function createAuditCapture(input: AuditCaptureContextInput): AuditCaptureContext {
  return new AuditCaptureContext(input)
}

export function observeGatewayHttpCompletion(res: Response): GatewayHttpCompletionObserver {
  const existing = gatewayHttpCompletionObservers.get(res)
  if (existing) return existing
  let completedAtMs: number | undefined
  let resolveCompletion: ((value: number) => void) | undefined
  const listeners = new Set<(value: number) => void>()
  const completion = new Promise<number>((resolvePromise) => {
    resolveCompletion = resolvePromise
  })
  const complete = (): void => {
    if (completedAtMs !== undefined) return
    completedAtMs = Date.now()
    res.off('finish', complete)
    res.off('close', complete)
    resolveCompletion?.(completedAtMs)
    resolveCompletion = undefined
    for (const listener of listeners) listener(completedAtMs)
    listeners.clear()
  }
  res.once('finish', complete)
  res.once('close', complete)
  const observer: GatewayHttpCompletionObserver = {
    completedAtMs: () => completedAtMs,
    wait: async () => await completion,
    onCompleted(listener) {
      if (completedAtMs !== undefined) {
        listener(completedAtMs)
        return () => undefined
      }
      listeners.add(listener)
      return () => listeners.delete(listener)
    }
  }
  gatewayHttpCompletionObservers.set(res, observer)
  if (res.writableFinished || res.destroyed) complete()
  return observer
}

export function getActiveAuditCaptureCount(): number {
  return activeAuditCaptureCount
}

export async function waitForActiveAuditCapturesIdle(timeoutMs = 10_000): Promise<boolean> {
  const deadline = Date.now() + Math.max(1, timeoutMs)
  while (activeAuditCaptureCount > 0) {
    if (Date.now() >= deadline) return false
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 5))
  }
  return true
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
    if (isUncapturedHeaderName(name)) continue
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

export interface GatewayHttpCompletionObserver {
  completedAtMs(): number | undefined
  wait(): Promise<number>
  onCompleted(listener: (completedAtMs: number) => void): () => void
}

function estimateRetainedPayloadBytes(
  payload: Omit<AuditLogPayloadInput, 'sequenceIndex'>,
  fullBodyLimitBytes: number
): number {
  if (
    !shouldOffloadAuditPayloadRetention()
    || payload.partType === 'gateway_metadata'
    || payload.body === undefined
    || (payload.captureStatus !== undefined && payload.captureStatus !== 'complete')
  ) {
    return estimatePayloadBytes(payload)
  }
  const bodyBytes = payloadBodyByteLength(payload.body)
  const retainedBodyBytes = bodyBytes <= fullBodyLimitBytes
    ? bodyBytes
    : Math.min(bodyBytes, fullBodyLimitBytes, auditBodySummaryEdgeBytes * 2)
  const headerBytes = payload.headers ? estimateHeadersBytes(payload.headers) : 0
  return retainedBodyBytes + headerBytes + 512
}

function shouldOffloadAuditPayloadRetention(): boolean {
  return runtimeConfig.processRole === 'server'
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
