import { EventEmitter } from 'node:events'

import type { Request, Response } from 'express'

import type { ClientCompatibilityCapability } from '../../../domain/types.js'
import type { GatewayApiKeyRow, OpenAIAccountSecret } from '../../../storage/repositories.js'
import { responseHeadersToObject, type AuditCaptureContext, createAuditCapture } from '../audit/capture.service.js'
import { resolveOpenAIGatewayClientStrategy } from '../client-profiles/strategy.js'
import { prepareOpenAIGatewayDispatchAccounts } from '../dispatch/preparation.js'
import { fetchFirstAvailableUpstream, UpstreamAttemptError } from '../dispatch/upstream-dispatch.js'
import { readCachedGatewaySettingsAsync } from '../runtime/runtime-cache.service.js'
import { createClientIpAccountAvoidanceTracker } from '../runtime/client-ip-account-avoidance.service.js'
import { groupUsageMetadata, type GatewayFailureUsageContext } from '../usage/records.js'
import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'
import { buildUsageRequestSnapshot } from '../usage/snapshots.js'
import { emptyUsage, type ParsedUsage } from '../usage/types.js'
import { orderGatewayApiKeyGroupBindingsForDispatchAsync } from '../routing/api-key-group-route-selector.service.js'
import { selectGatewayModelTargetGroup } from '../routing/model-target-group-selector.js'
import { readUpstreamBodyLimited } from '../upstream/body.js'
import { parseOpenAIUsageFromJsonBuffer } from '../protocols/openai-v1/usage.js'

type HybridAuxiliaryTrafficSource = Extract<OpenAIGatewayTrafficSource, 'hybrid_scoring' | 'hybrid_quality_scoring'>

export type HybridAuxiliaryDispatchResult =
  | {
    outcome: 'success'
    account: OpenAIAccountSecret
    groupId: string
    statusCode: number
    responseBody: Buffer
    responseBodyText: string
    responseBodyTruncated: boolean
    usage: ParsedUsage
    finish: (input: HybridAuxiliaryDispatchFinishInput) => Promise<void>
  }
  | {
    outcome: 'failed'
    errorCode: string
    errorMessage: string
    account?: OpenAIAccountSecret
    groupId?: string
    statusCode?: number
    shouldRecordUsage?: boolean
  }

export interface HybridAuxiliaryDispatchFinishInput {
  success: boolean
  errorCode?: string
  errorMessage?: string
}

export async function dispatchHybridAuxiliaryChatCompletion(input: {
  req: Request
  apiKeyRecord: GatewayApiKeyRow
  targetModel: string
  traceId: string
  clientIp?: string
  endpoint: string
  trafficSource: HybridAuxiliaryTrafficSource
  timeoutMs: number
  responseMaxBytes: number
  noAccountErrorCode: string
  noAccountErrorMessage: string
  dispatchErrorCode: string
  dispatchErrorMessage: string
  httpErrorCode?: string
  responseTooLargeMessage: string
  signal?: AbortSignal
  requestClientCompatibility?: ClientCompatibilityCapability
}): Promise<HybridAuxiliaryDispatchResult> {
  const selection = await selectGatewayModelTargetGroup({
    req: input.req,
    apiKeyRecord: input.apiKeyRecord,
    bindings: await orderGatewayApiKeyGroupBindingsForDispatchAsync(input.apiKeyRecord),
    targetModel: input.targetModel,
    requestClientCompatibility: input.requestClientCompatibility ?? 'openai_standard'
  })
  if (!selection) {
    return {
      outcome: 'failed',
      errorCode: input.noAccountErrorCode,
      errorMessage: input.noAccountErrorMessage
    }
  }

  const startedAt = Date.now()
  const response = new InternalGatewayResponse(startedAt)
  const auditCapture = createAuditCapture({
    req: input.req,
    traceId: input.traceId,
    clientIp: input.clientIp,
    startedAtMs: startedAt,
    trafficSource: input.trafficSource,
    captureMode: 'metadata_only'
  })
  const endpoint = `${input.endpoint}#${input.trafficSource === 'hybrid_quality_scoring' ? 'hybrid-quality-scoring' : 'hybrid-scoring'}`
  const usageContext: GatewayFailureUsageContext = {
    traceId: input.traceId,
    trafficSource: input.trafficSource,
    clientIp: input.clientIp,
    systemAccountId: input.apiKeyRecord.system_account_id,
    apiKeyId: input.apiKeyRecord.id,
    groupId: selection.groupId,
    ...groupUsageMetadata(selection.groupAccess),
    endpoint,
    requestSnapshot: buildUsageRequestSnapshot(input.req, input.traceId, input.clientIp)
  }
  auditCapture.bindContext({
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    providerCode: selection.groupAccess.providerCode,
    trafficSource: input.trafficSource
  })

  const clientStrategy = resolveOpenAIGatewayClientStrategy(input.req, {
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    endpoint,
    providerCode: selection.groupAccess.providerCode
  })
  const dispatchSignal = hybridAuxiliaryAbortSignal(input.signal, input.timeoutMs)
  const settings = await hybridAuxiliaryGatewaySettings(input.timeoutMs)
  const preparation = await prepareOpenAIGatewayDispatchAccounts({
    req: input.req,
    res: response.asResponse(),
    auditCapture,
    usageContext,
    startedAt,
    candidateAccounts: selection.accounts,
    modelPriority: selection.modelFilter.modelPriority,
    sessionAffinityKey: undefined,
    groupAccess: selection.groupAccess,
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: selection.groupId,
    clientIp: undefined,
    clientStrategy,
    requestLane: 'text',
    requestDeadlineAtMs: startedAt + input.timeoutMs,
    signal: dispatchSignal,
    attemptFallback: async () => ({ attempted: false })
  })
  if (preparation.outcome !== 'ready') {
    return {
      outcome: 'failed',
      errorCode: input.dispatchErrorCode,
      errorMessage: internalResponseErrorMessage(response, input.dispatchErrorMessage),
      groupId: selection.groupId,
      statusCode: response.statusCode
    }
  }

  const clientIpAccountAvoidanceTracker = createClientIpAccountAvoidanceTracker({
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: selection.groupId,
    clientIp: undefined
  })
  try {
    const dispatch = await fetchFirstAvailableUpstream(
      input.req,
      preparation.accounts,
      settings,
      usageContext,
      auditCapture,
      undefined,
      dispatchSignal,
      clientIpAccountAvoidanceTracker,
      'text',
      selection.groupAccess.schedulingPolicy,
      true,
      clientStrategy.requestClientCompatibility,
      selection.modelFilter.modelPriority
    )
    let released = false
    const release = () => {
      if (released) return
      released = true
      dispatch.releaseConcurrency()
    }
    try {
      const body = await readUpstreamBodyLimited(dispatch.response.body, {
        maxBytes: input.responseMaxBytes,
        startedAt,
        signal: dispatchSignal,
        onFirstByte: dispatch.markFirstOutput
      })
      release()
      if (body.truncated) {
        const finish = createFinish({
          auditCapture,
          auditAttemptId: dispatch.auditAttemptId,
          account: dispatch.account,
          statusCode: dispatch.response.status,
          headers: dispatch.response.headers,
          body: body.body,
          firstTokenMs: body.firstByteMs,
          confirmSameAccountApiKeyFailures: dispatch.confirmSameAccountApiKeyFailures
        })
        await finish({ success: false, errorCode: input.dispatchErrorCode, errorMessage: input.responseTooLargeMessage })
        return {
          outcome: 'failed',
          errorCode: input.dispatchErrorCode,
          errorMessage: input.responseTooLargeMessage,
          account: dispatch.account,
          groupId: selection.groupId,
          statusCode: dispatch.response.status,
          shouldRecordUsage: true
        }
      }
      return {
        outcome: 'success',
        account: dispatch.account,
        groupId: selection.groupId,
        statusCode: dispatch.response.status,
        responseBody: body.body,
        responseBodyText: body.bodyText,
        responseBodyTruncated: body.truncated,
        usage: parseOpenAIUsageFromJsonBuffer(body.body),
        finish: createFinish({
          auditCapture,
          auditAttemptId: dispatch.auditAttemptId,
          account: dispatch.account,
          statusCode: dispatch.response.status,
          headers: dispatch.response.headers,
          body: body.body,
          firstTokenMs: body.firstByteMs,
          confirmSameAccountApiKeyFailures: dispatch.confirmSameAccountApiKeyFailures
        })
      }
    } catch (error) {
      release()
      const message = error instanceof Error ? error.message : String(error)
      auditCapture.completeAttempt(dispatch.auditAttemptId, {
        statusCode: dispatch.response.status,
        responseHeaders: dispatch.response.headers,
        success: false,
        errorPhase: 'upstream_response',
        errorCode: input.dispatchErrorCode,
        errorMessage: message
      })
      auditCapture.finalize({
        outcome: 'upstream_failed',
        success: false,
        statusCode: dispatch.response.status,
        responseHeaders: dispatch.response.headers,
        errorPhase: 'upstream_response',
        errorCode: input.dispatchErrorCode,
        errorMessage: message,
        accountId: dispatch.account.id
      })
      return {
        outcome: 'failed',
        errorCode: input.dispatchErrorCode,
        errorMessage: message,
        account: dispatch.account,
        groupId: selection.groupId,
        statusCode: dispatch.response.status,
        shouldRecordUsage: true
      }
    }
  } catch (error) {
    const isAttemptError = error instanceof UpstreamAttemptError
    const statusCode = isAttemptError ? error.lastAttempt?.status : undefined
    const errorCode = isAttemptError && typeof statusCode === 'number'
      ? input.httpErrorCode ?? input.dispatchErrorCode
      : input.dispatchErrorCode
    const message = error instanceof UpstreamAttemptError
      ? error.message
      : error instanceof Error ? error.message : String(error)
    auditCapture.finalize({
      outcome: 'upstream_failed',
      success: false,
      statusCode,
      responseHeaders: responseHeadersToObject(response.asResponse()),
      responseBody: response.bodyText(),
      responsePartType: 'gateway_error',
      errorPhase: 'dispatch',
      errorCode,
      errorMessage: message,
      accountId: error instanceof UpstreamAttemptError ? error.lastAttempt?.accountId : undefined
    })
    return {
      outcome: 'failed',
      errorCode,
      errorMessage: message,
      groupId: selection.groupId,
      statusCode
    }
  }
}

function createFinish(input: {
  auditCapture: AuditCaptureContext
  auditAttemptId: string
  account: OpenAIAccountSecret
  statusCode: number
  headers: Headers
  body: Buffer
  firstTokenMs?: number
  confirmSameAccountApiKeyFailures: () => Promise<void>
}): (finish: HybridAuxiliaryDispatchFinishInput) => Promise<void> {
  let finished = false
  return async (finish) => {
    if (finished) return
    finished = true
    input.auditCapture.completeAttempt(input.auditAttemptId, {
      statusCode: input.statusCode,
      responseHeaders: input.headers,
      responseBody: input.body,
      success: finish.success,
      errorPhase: finish.success ? undefined : 'upstream_response',
      errorCode: finish.errorCode,
      errorMessage: finish.errorMessage
    })
    if (finish.success) {
      await input.confirmSameAccountApiKeyFailures()
    }
    input.auditCapture.finalize({
      outcome: finish.success ? 'success' : 'upstream_failed',
      success: finish.success,
      statusCode: input.statusCode,
      responseHeaders: input.headers,
      responseBody: input.body,
      responsePartType: finish.success ? 'gateway_response' : 'gateway_error',
      errorPhase: finish.success ? undefined : 'upstream_response',
      errorCode: finish.errorCode,
      errorMessage: finish.errorMessage,
      accountId: input.account.id,
      firstTokenMs: input.firstTokenMs
    })
  }
}

async function hybridAuxiliaryGatewaySettings(timeoutMs: number) {
  const base = await readCachedGatewaySettingsAsync()
  return {
    ...base,
    streamRequestTimeoutSeconds: Math.max(1, Math.ceil(timeoutMs / 1000)),
    streamClientTotalWaitTimeoutSeconds: Math.max(10, Math.ceil(timeoutMs / 1000)),
    streamMaxLifetimeSeconds: Math.max(60, Math.ceil(timeoutMs / 1000))
  }
}

function hybridAuxiliaryAbortSignal(parent: AbortSignal | undefined, timeoutMs: number): AbortSignal {
  const timeoutSignal = AbortSignal.timeout(Math.max(1, timeoutMs))
  return parent ? AbortSignal.any([parent, timeoutSignal]) : timeoutSignal
}

function internalResponseErrorMessage(response: InternalGatewayResponse, fallback: string): string {
  const text = response.bodyText()
  if (!text) return fallback
  try {
    const parsed = JSON.parse(text) as { error?: { message?: unknown } }
    return typeof parsed.error?.message === 'string' && parsed.error.message.trim()
      ? parsed.error.message.trim()
      : fallback
  } catch {
    return text.slice(0, 500) || fallback
  }
}

export function emptyHybridAuxiliaryUsage(): ParsedUsage {
  return emptyUsage()
}

class InternalGatewayResponse extends EventEmitter {
  statusCode = 200
  writableEnded = false
  destroyed = false
  headersSent = false
  writableLength = 0
  writableHighWaterMark = 16 * 1024
  locals: Record<string, unknown> = {}
  private readonly headers = new Map<string, string | string[]>()
  private readonly chunks: Buffer[] = []

  constructor(private readonly startedAt: number) {
    super()
  }

  status(code: number): this {
    this.statusCode = code
    return this
  }

  setHeader(name: string, value: number | string | readonly string[]): this {
    this.headers.set(name.toLowerCase(), Array.isArray(value) ? value.map(String) : String(value))
    return this
  }

  getHeader(name: string): string | string[] | undefined {
    return this.headers.get(name.toLowerCase())
  }

  hasHeader(name: string): boolean {
    return this.headers.has(name.toLowerCase())
  }

  getHeaders(): Record<string, string | string[]> {
    return Object.fromEntries(this.headers.entries())
  }

  json(value: unknown): this {
    if (!this.hasHeader('content-type')) {
      this.setHeader('content-type', 'application/json; charset=utf-8')
    }
    return this.send(Buffer.from(JSON.stringify(value), 'utf8'))
  }

  send(value?: Buffer | string | object): this {
    if (Buffer.isBuffer(value)) {
      this.write(value)
    } else if (typeof value === 'string') {
      this.write(value)
    } else if (value !== undefined) {
      this.write(JSON.stringify(value))
    }
    return this.end()
  }

  write(value: Buffer | string | Uint8Array): boolean {
    if (this.writableEnded || this.destroyed) return false
    this.headersSent = true
    const buffer = Buffer.isBuffer(value) ? value : Buffer.from(value)
    this.chunks.push(buffer)
    this.writableLength += buffer.byteLength
    return true
  }

  end(value?: Buffer | string | Uint8Array): this {
    if (value !== undefined) {
      this.write(value)
    }
    if (!this.writableEnded) {
      this.headersSent = true
      this.writableEnded = true
      this.emit('finish')
      this.emit('close')
    }
    return this
  }

  destroy(): this {
    if (!this.destroyed) {
      this.destroyed = true
      this.emit('close')
    }
    return this
  }

  bodyText(): string {
    return Buffer.concat(this.chunks).toString('utf8')
  }

  firstTokenMs(): number | undefined {
    return this.chunks.length > 0 ? Date.now() - this.startedAt : undefined
  }

  asResponse(): Response {
    return this as unknown as Response
  }
}
