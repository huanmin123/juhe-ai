import { createHash, randomUUID } from 'node:crypto'
import type { Request, Response } from 'express'

import { nowIso } from '../../storage/database.js'
import { sanitizeUrlForLog } from '../../shared/request-context.js'
import type {
  AuditLogAttemptInput,
  AuditLogInput,
  AuditLogPayloadInput,
  AuditOutcome,
  AuditPayloadPartType,
  OpenAIAccountSecret
} from '../../storage/repositories.js'
import { enqueueAuditLog } from '../audit-logs/audit-log-queue.service.js'
import { readAuditLogSettings } from '../audit-logs/audit-log-settings.js'
import {
  headersToSafeObject,
  requestModel,
  requestStream,
  sanitizeHeaderRecord,
  sanitizeHeaderValue
} from './openai-gateway-usage.js'
import {
  normalizeOpenAIGatewayTrafficSource,
  type OpenAIGatewayTrafficSource
} from './openai-gateway-traffic-source.js'

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

interface AuditAttemptState {
  tempId: string
  attempt: AuditLogAttemptInput
  startedAtMs: number
  completed: boolean
}

let activeAuditCaptureCount = 0

export class AuditCaptureContext {
  private readonly req: Request
  private readonly traceId: string
  private readonly clientIp?: string
  private readonly startedAtMs: number
  private readonly startedAtIso: string
  private readonly trafficSource: OpenAIGatewayTrafficSource
  private readonly metadataOnly: boolean
  private readonly enabled: boolean
  private readonly successSampleRate: number
  private readonly activeCaptureMaxBytes: number
  private readonly payloads: AuditLogPayloadInput[] = []
  private readonly attempts: AuditLogAttemptInput[] = []
  private gatewayContext: AuditGatewayContext = {}
  private activeAttemptByTempId = new Map<string, AuditAttemptState>()
  private finalized = false
  private hadFailedAttempt = false
  private clientAborted = false
  private overflowed = false
  private approximateBytes = 0
  private sequenceIndex = 0

  constructor(input: AuditCaptureContextInput) {
    const settings = readAuditLogSettings()
    this.enabled = settings.enabled
    this.successSampleRate = settings.successSampleRate
    this.activeCaptureMaxBytes = settings.activeCaptureMaxBytes
    this.req = input.req
    this.traceId = input.traceId
    this.clientIp = input.clientIp
    this.startedAtMs = input.startedAtMs
    this.startedAtIso = new Date(input.startedAtMs).toISOString()
    this.trafficSource = normalizeOpenAIGatewayTrafficSource(input.trafficSource)
    this.metadataOnly = input.captureMode === 'metadata_only'
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
    this.addPayload({
      partType: 'client_request',
      headers: requestHeadersToObject(input.req),
      body: (input.req as RawBodyRequest).rawBody,
      contentType: input.req.header('content-type'),
      contentEncoding: input.req.header('content-encoding')
    })
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

  startAttempt(input: StartAttemptInput): string {
    if (!this.enabled) return ''
    const tempId = `attempt_${input.attemptIndex}_${Date.now()}_${Math.random().toString(16).slice(2, 8)}`
    const startedAtMs = Date.now()
    const attempt: AuditLogAttemptInput = {
      id: `audatt_${Date.now()}_${randomUUID()}`,
      tempId,
      attemptIndex: input.attemptIndex,
      accountId: input.account.id,
      accountOwnerSystemAccountId: input.account.accountOwnerSystemAccountId,
      groupId: this.gatewayContext.groupId,
      proxyUrl: input.account.proxyUrl,
      providerCode: 'openai',
      upstreamMethod: input.method,
      upstreamUrl: input.upstreamUrl,
      startedAt: new Date(startedAtMs).toISOString()
    }
    this.attempts.push(attempt)
    this.activeAttemptByTempId.set(tempId, { tempId, attempt, startedAtMs, completed: false })
    if (!this.metadataOnly) {
      this.addPayload({
        attemptTempId: tempId,
        partType: 'upstream_request',
        headers: headersToSafeObject(input.headers),
        body: input.body,
        contentType: input.headers.get('content-type') ?? undefined,
        contentEncoding: input.headers.get('content-encoding') ?? undefined
      })
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
    state.attempt.errorCode = input.errorCode
    state.attempt.errorMessage = input.errorMessage
    if (!input.success) {
      this.hadFailedAttempt = true
    }
    if (!this.metadataOnly && (input.responseHeaders || input.responseBody !== undefined)) {
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
    const sampleBucket = sampleBucketForTraceId(this.traceId)
    const shouldCapture = this.metadataOnly || outcome !== 'success' || sampleBucket < Math.round(this.successSampleRate * 10000)
    if (!shouldCapture) {
      return
    }

    if (input.accountId) {
      this.bindContext({ accountId: input.accountId })
    }
    if (!this.metadataOnly && (input.responseBody !== undefined || input.responseHeaders)) {
      this.addPayload({
        partType: input.responsePartType ?? (input.success ? 'gateway_response' : 'gateway_error'),
        headers: input.responseHeaders ? normalizeSafeHeaders(input.responseHeaders) : undefined,
        body: input.responseBody,
        contentType: headerValue(input.responseHeaders, 'content-type'),
        contentEncoding: headerValue(input.responseHeaders, 'content-encoding')
      })
    }
    const sanitizedOriginalUrl = sanitizeUrlForLog(this.req.originalUrl)
    const auditLog: AuditLogInput = {
      id: `audit_${Date.now()}_${randomUUID()}`,
      traceId: this.traceId,
      ...this.gatewayContext,
      accountId: input.accountId ?? this.gatewayContext.accountId,
      providerCode: this.gatewayContext.providerCode ?? 'openai',
      trafficSource: this.gatewayContext.trafficSource ?? this.trafficSource,
      method: this.req.method.toUpperCase(),
      path: sanitizedOriginalUrl.split('?')[0] || this.req.path,
      queryString: sanitizedOriginalUrl.includes('?') ? sanitizedOriginalUrl.split('?').slice(1).join('?') : undefined,
      model: requestModel(this.req),
      stream: requestStream(this.req),
      clientIp: this.clientIp,
      userAgent: this.req.header('user-agent'),
      auditOutcome: outcome,
      success,
      finalStatusCode: input.statusCode,
      errorPhase: clientAborted ? input.errorPhase ?? 'client' : input.errorPhase,
      errorCode: input.errorCode,
      errorMessage: clientAborted ? input.errorMessage ?? 'Client aborted request' : input.errorMessage,
      sampleBucket,
      sampleReason: this.metadataOnly ? `${this.trafficSource}_metadata_only` : outcome === 'success' ? `success_sample_${this.successSampleRate}` : 'full_capture',
      captureStatus: this.overflowed ? 'overflow' : 'complete',
      startedAt: this.startedAtIso,
      endedAt: new Date(endedAtMs).toISOString(),
      durationMs: endedAtMs - this.startedAtMs,
      firstTokenMs: input.firstTokenMs,
      attempts: this.attempts,
      payloads: this.payloads
    }
    enqueueAuditLog(auditLog)
  }

  private addPayload(payload: Omit<AuditLogPayloadInput, 'sequenceIndex'>): void {
    if (!this.enabled) return
    if (this.overflowed) return
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

function sampleBucketForTraceId(traceId: string): number {
  const digest = createHash('sha256').update(traceId).digest()
  return digest.readUInt32BE(0) % 10000
}

function estimatePayloadBytes(payload: Omit<AuditLogPayloadInput, 'sequenceIndex'>): number {
  const body = payload.body
  const bodyBytes = Buffer.isBuffer(body) ? body.byteLength : typeof body === 'string' ? Buffer.byteLength(body, 'utf8') : 0
  const headerBytes = payload.headers ? estimateHeadersBytes(payload.headers) : 0
  return bodyBytes + headerBytes + 512
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
