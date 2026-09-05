import { EventEmitter } from 'node:events'

import type { Request, Response } from 'express'

import type { ClientCompatibilityCapability } from '../../../domain/types.js'
import type { GatewayApiKeyRow, OpenAIAccountSecret } from '../../../storage/repositories.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import { responseHeadersToObject, type AuditCaptureContext, createAuditCapture } from '../audit/capture.service.js'
import { resolveOpenAIGatewayClientStrategy } from '../client-profiles/strategy.js'
import { prepareOpenAIGatewayDispatchAccounts } from '../dispatch/preparation.js'
import { fetchFirstAvailableUpstream, UpstreamAttemptError } from '../dispatch/upstream-dispatch.js'
import { readCachedGatewaySettingsAsync } from '../runtime/runtime-cache.service.js'
import { createClientIpAccountAvoidanceTracker } from '../runtime/client-ip-account-avoidance.service.js'
import { ServerRetryBudget } from '../runtime/server-retry-budget.js'
import { groupUsageMetadata, type GatewayFailureUsageContext } from '../usage/records.js'
import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'
import { buildUsageRequestSnapshot } from '../usage/snapshots.js'
import { emptyUsage, type ParsedUsage } from '../usage/types.js'
import { orderGatewayApiKeyGroupBindingsForDispatchAsync } from '../routing/api-key-group-route-selector.service.js'
import { selectGatewayModelTargetGroup } from '../routing/model-target-group-selector.js'
import {
  GatewayRequestAttemptTracker,
  GatewayRequestWallBudget,
  RouteCoordinationBudget
} from '../routing/route-coordination.js'
import { readUpstreamBodyLimited } from '../upstream/body.js'
import {
  parseOpenAIUsageFromJsonTextFragment,
  parseOpenAIUsageFromJsonValue
} from '../protocols/openai-v1/usage.js'
import { sendGatewayFailureResponse } from '../response/failure-response.js'
import { gatewayErrorPayload } from '../response/responses.js'
import {
  gatewayNonStreamJsonBodyFromValue,
  gatewayNonStreamJsonBodyReceiver,
  parseGatewayNonStreamJsonBody,
  type GatewayNonStreamJsonBody
} from '../response/non-stream-json-body.js'
import { parseGatewayProtocolErrorPayloadFromJsonValue } from '../protocols/registry.js'

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
    parsedResponseBody: GatewayNonStreamJsonBody
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

export function parseHybridAuxiliaryResponse(
  bodyText: string,
  headers: Headers
): { parsedResponseBody: GatewayNonStreamJsonBody; usage: ParsedUsage } {
  const parsedResponseBody = parseGatewayNonStreamJsonBody(bodyText, headers)
  return {
    parsedResponseBody,
    usage: parsedResponseBody.status === 'valid'
      ? parseOpenAIUsageFromJsonValue(parsedResponseBody.value)
      : parseOpenAIUsageFromJsonTextFragment(bodyText)
  }
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
  const auxiliaryBudgetMs = Math.max(1, Math.trunc(input.timeoutMs))
  const serverRetryBudget = new ServerRetryBudget(auxiliaryBudgetMs)
  const gatewayRequestWallBudget = new GatewayRequestWallBudget({
    requestAcceptedAtMs: startedAt,
    budgetMs: auxiliaryBudgetMs
  })
  const routeCoordinationBudget = new RouteCoordinationBudget({
    requestId: input.traceId,
    budgetId: `${input.traceId}:${input.trafficSource}:route-coordination`,
    budgetMs: Math.min(auxiliaryBudgetMs, 3_000)
  })
  const auxiliaryRequestCoordination = {
    scope: 'internal_hybrid_auxiliary' as const,
    reason: input.trafficSource,
    serverRetryBudget,
    gatewayRequestWallBudget,
    routeCoordinationBudget,
    requestAttemptTracker: new GatewayRequestAttemptTracker()
  }
  auditCapture.addGatewayMetadata({
    label: 'gateway_internal_request_coordination',
    metadata: {
      scope: auxiliaryRequestCoordination.scope,
      reason: auxiliaryRequestCoordination.reason,
      independentFromParentRequest: true
    }
  })
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
    serverRetryBudget,
    routeCoordinationBudget,
    gatewayRequestWallBudget,
    signal: dispatchSignal,
    routeCoordinator: {
      requestFallback: async () => ({ attempted: false }),
      completeFailure: async (failure) => {
        if (failure.retryAfterMs !== undefined) {
          response.asResponse().setHeader('Retry-After', String(Math.max(1, Math.ceil(failure.retryAfterMs / 1000))))
        }
        const responsePayload = gatewayErrorPayload(failure.message, failure.errorType, failure.errorCode)
        await sendGatewayFailureResponse({
          req: input.req,
          res: response.asResponse(),
          auditCapture,
          usageContext,
          startedAt,
          statusCode: failure.statusCode,
          responsePayload,
          audit: {
            outcome: 'gateway_failed',
            errorPhase: failure.errorPhase,
            errorCode: failure.errorCode,
            errorMessage: failure.message
          },
          failureAttribution: failure.failureAttribution
        })
      }
    }
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
      selection.modelFilter.modelPriority,
      undefined,
      false,
      auxiliaryRequestCoordination,
      false
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
      dispatch.hotQualityAttempt.markFirstByte(body.firstByteMs)
      const opaqueUpstreamResponse = !dispatch.response.ok && !body.truncated
      if (body.truncated) {
        auditCapture.addGatewayMetadata({
          label: 'hybrid_auxiliary_response_limit',
          metadata: {
            accountId: dispatch.account.id,
            responseMaxBytes: input.responseMaxBytes,
            capturedBytes: body.body.byteLength,
            readBytes: body.readBytes,
            failureScope: 'none'
          }
        })
      }
      await dispatch.hotQualityAttempt.recordTerminal({
        outcomeClass: body.truncated
          ? 'unknown'
          : opaqueUpstreamResponse
            ? 'upstream_response_failure'
            : 'completed_response',
        failureScope: 'none',
        source: body.truncated
          ? 'request_lifecycle'
          : opaqueUpstreamResponse
            ? 'upstream_response'
            : 'gateway_transport',
        firstByteMs: body.firstByteMs
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
          confirmSameAccountApiKeyFailures: dispatch.confirmSameAccountApiKeyFailures,
          confirmHalfOpenSuccess: dispatch.confirmHalfOpenSuccess,
          releaseHalfOpenLease: dispatch.releaseHalfOpenLease
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
      if (opaqueUpstreamResponse) {
        const upstreamFailure = hybridAuxiliaryUpstreamFailure({
          account: dispatch.account,
          bodyText: body.bodyText,
          headers: dispatch.response.headers,
          statusCode: dispatch.response.status,
          fallbackErrorCode: input.dispatchErrorCode
        })
        const finish = createFinish({
          auditCapture,
          auditAttemptId: dispatch.auditAttemptId,
          account: dispatch.account,
          statusCode: dispatch.response.status,
          headers: dispatch.response.headers,
          body: body.body,
          firstTokenMs: body.firstByteMs,
          confirmSameAccountApiKeyFailures: dispatch.confirmSameAccountApiKeyFailures,
          confirmHalfOpenSuccess: dispatch.confirmHalfOpenSuccess,
          releaseHalfOpenLease: dispatch.releaseHalfOpenLease
        })
        await finish({
          success: false,
          errorCode: upstreamFailure.errorCode,
          errorMessage: upstreamFailure.errorMessage
        })
        return {
          outcome: 'failed',
          errorCode: upstreamFailure.errorCode,
          errorMessage: upstreamFailure.errorMessage,
          account: dispatch.account,
          groupId: selection.groupId,
          statusCode: dispatch.response.status,
          shouldRecordUsage: true
        }
      }
      const parsedResponse = parseHybridAuxiliaryResponse(body.bodyText, dispatch.response.headers)
      return {
        outcome: 'success',
        account: dispatch.account,
        groupId: selection.groupId,
        statusCode: dispatch.response.status,
        responseBody: body.body,
        responseBodyText: body.bodyText,
        responseBodyTruncated: body.truncated,
        parsedResponseBody: parsedResponse.parsedResponseBody,
        usage: parsedResponse.usage,
        finish: createFinish({
          auditCapture,
          auditAttemptId: dispatch.auditAttemptId,
          account: dispatch.account,
          statusCode: dispatch.response.status,
          headers: dispatch.response.headers,
          body: body.body,
          firstTokenMs: body.firstByteMs,
          confirmSameAccountApiKeyFailures: dispatch.confirmSameAccountApiKeyFailures,
          confirmHalfOpenSuccess: dispatch.confirmHalfOpenSuccess,
          releaseHalfOpenLease: dispatch.releaseHalfOpenLease
        })
      }
    } catch (error) {
      release()
      await dispatch.hotQualityAttempt.recordTerminal({
        outcomeClass: dispatchSignal.aborted ? 'client_cancellation' : 'read_interruption',
        failureScope: dispatchSignal.aborted ? 'none' : 'protocol_model',
        source: dispatchSignal.aborted ? 'request_lifecycle' : 'gateway_transport'
      })
      await dispatch.releaseHalfOpenLease()
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

function hybridAuxiliaryUpstreamFailure(input: {
  account: OpenAIAccountSecret
  bodyText: string
  headers: Headers
  statusCode: number
  fallbackErrorCode: string
}): { errorCode: string; errorMessage: string } {
  const parsedBody = parseGatewayNonStreamJsonBody(input.bodyText, input.headers)
  const errorPayload = parsedBody.status === 'valid'
    ? parseGatewayProtocolErrorPayloadFromJsonValue(input.account, parsedBody.value)
    : {}
  const errorCode = typeof errorPayload.code === 'string' && errorPayload.code.trim()
    ? errorPayload.code
    : input.fallbackErrorCode
  const errorMessage = typeof errorPayload.message === 'string' && errorPayload.message.trim()
    ? errorPayload.message
    : input.bodyText.trim() || `上游返回 HTTP ${input.statusCode}`
  return { errorCode, errorMessage }
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
  confirmHalfOpenSuccess: () => Promise<boolean>
  releaseHalfOpenLease: () => Promise<boolean>
}): (finish: HybridAuxiliaryDispatchFinishInput) => Promise<void> {
  let finished = false
  return async (finish) => {
    if (finished) return
    finished = true
    let leaseSettled = false
    let finishError: unknown
    try {
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
        await input.confirmHalfOpenSuccess()
        leaseSettled = true
        await input.confirmSameAccountApiKeyFailures()
      } else {
        await input.releaseHalfOpenLease()
        leaseSettled = true
      }
    } catch (error) {
      finishError = error
    } finally {
      if (!leaseSettled) {
        try {
          await input.releaseHalfOpenLease()
          leaseSettled = true
        } catch (error) {
          finishError ??= error
        }
      }
      try {
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
      } catch (error) {
        finishError ??= error
      }
    }
    if (finishError) {
      logger.warn(errorLogFields(finishError, {
        event: 'hybrid_auxiliary_finish_side_effect_failed',
        accountId: input.account.id,
        auditAttemptId: input.auditAttemptId,
        success: finish.success
      }), '混合辅助上游结果已收尾，但部分运行态/审计副作用失败')
    }
  }
}

async function hybridAuxiliaryGatewaySettings(timeoutMs: number) {
  const base = await readCachedGatewaySettingsAsync()
  return {
    ...base,
    textFirstResponseTimeoutSeconds: Math.max(1, Math.ceil(timeoutMs / 1000)),
    noAvailableAccountWaitTimeoutSeconds: Math.max(10, Math.ceil(timeoutMs / 1000)),
    textUncommittedAttemptMaxLifetimeSeconds: Math.max(60, Math.ceil(timeoutMs / 1000))
  }
}

function hybridAuxiliaryAbortSignal(parent: AbortSignal | undefined, timeoutMs: number): AbortSignal {
  const timeoutSignal = AbortSignal.timeout(Math.max(1, timeoutMs))
  return parent ? AbortSignal.any([parent, timeoutSignal]) : timeoutSignal
}

function internalResponseErrorMessage(response: InternalGatewayResponse, fallback: string): string {
  const text = response.bodyText()
  if (!text) return fallback
  const parsedBody = response.nonStreamJsonBody() ?? parseGatewayNonStreamJsonBody(
    text,
    new Headers({ 'content-type': String(response.getHeader('content-type') ?? 'application/json') })
  )
  const root = parsedBody.status === 'valid' && typeof parsedBody.value === 'object'
    && parsedBody.value !== null && !Array.isArray(parsedBody.value)
    ? parsedBody.value as Record<string, unknown>
    : undefined
  const error = root?.error && typeof root.error === 'object' && !Array.isArray(root.error)
    ? root.error as Record<string, unknown>
    : undefined
  return typeof error?.message === 'string' && error.message.trim()
    ? error.message.trim()
    : parsedBody.status === 'invalid' ? text.slice(0, 500) || fallback : fallback
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
  private parsedNonStreamJsonBody: GatewayNonStreamJsonBody | undefined

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
    this.parsedNonStreamJsonBody = gatewayNonStreamJsonBodyFromValue(value)
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

  [gatewayNonStreamJsonBodyReceiver](body: GatewayNonStreamJsonBody): void {
    this.parsedNonStreamJsonBody = body
  }

  nonStreamJsonBody(): GatewayNonStreamJsonBody | undefined {
    return this.parsedNonStreamJsonBody
  }

  firstTokenMs(): number | undefined {
    return this.chunks.length > 0 ? Date.now() - this.startedAt : undefined
  }

  asResponse(): Response {
    return this as unknown as Response
  }
}
