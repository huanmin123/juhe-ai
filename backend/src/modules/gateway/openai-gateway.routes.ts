import { Router, type NextFunction, type Request, type Response } from 'express'

import { createTraceId, getRequestLogger, getTraceId } from '../../shared/request-context.js'
import { errorLogFields } from '../../shared/logger.js'
import {
  buildUsageRequestSnapshot,
  extractClientIp,
  requestEndpoint
} from './openai-gateway-usage.js'
import {
  isEffectiveOpenAIStreamRequest
} from './openai-gateway-upstream.js'
import {
  gatewayErrorPayload,
  isOpenAIStreamContentType,
  sendGatewayErrorResponse
} from './openai-gateway-responses.js'
import { createAuditCapture } from './audit-capture.service.js'
import {
  type UpstreamAccount
} from './openai-gateway-route-helpers.js'
import {
  buildDiagnosticUpstreamError
} from './openai-gateway-error-helpers.js'
import {
  persistOpenAICodexHeadersIfNeeded
} from './openai-gateway-account-effects.js'
import {
  finalizeHandledUpstreamResponse,
  handleNonStreamUpstreamResponse,
  handleStreamUpstreamResponse,
  prepareUpstreamResponseForDownstream
} from './openai-gateway-response-finalization.js'
import { sendGatewayFailureResponse } from './openai-gateway-failure-response.js'
import { handleGatewayRequestKnownErrorResponse } from './openai-gateway-request-error-response.js'
import {
  prepareOpenAIGatewayDispatchContext,
  type OpenAIGatewayRequestIdentity
} from './openai-gateway-request-preflight.js'
import {
  fetchFirstAvailableUpstream,
  UpstreamAttemptError
} from './openai-gateway-upstream-dispatch.js'
import type { GatewaySettings } from './account-error-policy.service.js'
import { OpenAIOAuthCodexAdapterError } from './openai-oauth-codex-adapter.js'
import { recordClientIpErrorCircuitSample } from './openai-gateway-client-ip-error-circuit.service.js'
import type { GatewayFailureUsageContext } from './openai-gateway-usage-records.js'
import {
  normalizeOpenAIGatewayTrafficSource,
  type OpenAIGatewayTrafficSource
} from './openai-gateway-traffic-source.js'
import { resolveOpenAIGatewayRequestLane } from './openai-gateway-request-lane.js'

export const openAIGatewayRouter = Router()

export type { OpenAIGatewayRequestIdentity } from './openai-gateway-request-preflight.js'

export interface OpenAIGatewayHandleOptions {
  identity?: OpenAIGatewayRequestIdentity
  candidateAccounts?: UpstreamAccount[]
  disableSessionAffinity?: boolean
  exposeUpstreamDiagnostics?: boolean
  trafficSource?: OpenAIGatewayTrafficSource
  settingsOverride?: Partial<GatewaySettings>
}

export function handleGatewayDbServiceUnavailable(error: unknown, req: Request, res: Response, next: NextFunction): void {
  const message = dbServiceUnavailableMessage(error)
  if (!message || res.headersSent) {
    next(error)
    return
  }

  getRequestLogger().error(errorLogFields(error, {
    event: 'gateway_db_service_unavailable',
    endpoint: `${req.method.toUpperCase()} ${requestEndpoint(req)}`
  }), '网关 DB service 不可用')

  sendGatewayErrorResponse(res, 503, gatewayErrorPayload(message, 'service_unavailable'))
}

function dbServiceUnavailableMessage(error: unknown): string | undefined {
  if (!(error instanceof Error)) {
    return undefined
  }
  return /^本地数据库服务(暂时不可用|未就绪|请求超时|已退出)/.test(error.message)
    ? error.message
    : undefined
}

openAIGatewayRouter.all('*', async (req, res, next) => {
  try {
    await handleOpenAIGatewayRequest(req, res)
  } catch (error) {
    handleGatewayDbServiceUnavailable(error, req, res, next)
  }
})

export async function handleOpenAIGatewayRequest(
  req: Request,
  res: Response,
  options: OpenAIGatewayHandleOptions = {}
): Promise<void> {
  const startedAt = Date.now()
  const abortController = new AbortController()
  const traceId = getTraceId() ?? createTraceId()
  const clientIp = extractClientIp(req)
  const endpoint = requestEndpoint(req)
  const requestLane = resolveOpenAIGatewayRequestLane(req)
  const trafficSource = normalizeOpenAIGatewayTrafficSource(options.trafficSource)
  const requestSnapshot = buildUsageRequestSnapshot(req, traceId, clientIp)
  const auditCapture = createAuditCapture({
    req,
    traceId,
    clientIp,
    startedAtMs: startedAt,
    trafficSource,
    captureMode: trafficSource === 'cooldown_retest' ? 'metadata_only' : 'default'
  })
  req.once('aborted', () => {
    auditCapture.markClientAborted()
    abortController.abort()
  })
  res.once('close', () => {
    if (!res.writableEnded) {
      auditCapture.markClientAborted()
      abortController.abort()
    }
  })

  const preflight = await prepareOpenAIGatewayDispatchContext({
    req,
    res,
    auditCapture,
    options: { ...options, trafficSource, requestLane },
    startedAt,
    traceId,
    clientIp,
    endpoint,
    requestSnapshot,
    signal: abortController.signal
  })
  if (!preflight) {
    return
  }
  const {
    activeGatewaySettings,
    usageContext: gatewayUsageContext,
    accounts,
    sessionAffinityKey,
    clientStrategy,
    clientIpAccountAvoidanceTracker,
    releaseClientIpConcurrency
  } = preflight
  const releaseClientIpSlot = once(releaseClientIpConcurrency)
  res.once('finish', releaseClientIpSlot)
  res.once('close', releaseClientIpSlot)

  try {
    const upstreamResult = await fetchFirstAvailableUpstream(
      req,
      accounts,
      activeGatewaySettings,
      gatewayUsageContext,
      auditCapture,
      sessionAffinityKey,
      abortController.signal,
      clientIpAccountAvoidanceTracker,
      requestLane,
      preflight.groupSchedulingPolicy
    )
    const { account, response: upstreamResponse, upstreamUrl, auditAttemptId, releaseConcurrency, markFirstOutput } = upstreamResult

    try {
      const contentType = upstreamResponse.headers.get('content-type') ?? ''
      const shouldHandleAsStream = isOpenAIStreamContentType(contentType) || isEffectiveOpenAIStreamRequest(req, account)
      prepareUpstreamResponseForDownstream(res, upstreamResponse, shouldHandleAsStream)
      persistOpenAICodexHeadersIfNeeded(account, upstreamResponse.headers, gatewayUsageContext.trafficSource)

      const handledResponse = shouldHandleAsStream
        ? await handleStreamUpstreamResponse({
          req,
          res,
          account,
          upstreamResponse,
          upstreamUrl,
          auditAttemptId,
          auditCapture,
          settings: activeGatewaySettings,
          usageContext: gatewayUsageContext,
          startedAt,
          signal: abortController.signal,
          sessionAffinityKey,
          clientStrategy,
          markFirstOutput,
          clientIpAccountAvoidanceTracker
        })
        : await handleNonStreamUpstreamResponse({
          req,
          res,
          account,
          upstreamResponse,
          upstreamUrl,
          auditAttemptId,
          auditCapture,
          settings: activeGatewaySettings,
          usageContext: gatewayUsageContext,
          startedAt,
          signal: abortController.signal,
          sessionAffinityKey,
          markFirstOutput,
          clientIpAccountAvoidanceTracker
        })
      if (handledResponse.alreadyFinalized) {
        return
      }
      finalizeHandledUpstreamResponse({
        req,
        res,
        account,
        upstreamResponse,
        upstreamUrl,
        auditAttemptId,
        auditCapture,
        settings: activeGatewaySettings,
        usageContext: gatewayUsageContext,
        startedAt,
        signal: abortController.signal,
        result: handledResponse,
        clientIpAccountAvoidanceTracker
      })
    } finally {
      releaseConcurrency()
    }
  } catch (error) {
    recordKnownClientIpRequestError(error, gatewayUsageContext, auditCapture)
    if (handleGatewayRequestKnownErrorResponse({
      res,
      auditCapture,
      error,
      signal: abortController.signal
    })) {
      return
    }
    const lastAttempt = error instanceof UpstreamAttemptError ? error.lastAttempt : undefined
    const message = error instanceof Error ? error.message : '没有可用的上游账户'
    const diagnosticError = options.exposeUpstreamDiagnostics
      ? buildDiagnosticUpstreamError(lastAttempt, message)
      : undefined
    const statusCode = diagnosticError?.statusCode ?? 503
    const responsePayload = diagnosticError?.payload ?? gatewayErrorPayload('没有可用的上游账户', 'service_unavailable')
    sendGatewayFailureResponse({
      req,
      res,
      auditCapture,
      usageContext: gatewayUsageContext,
      startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'upstream_failed',
        errorPhase: 'dispatch',
        errorCode: 'service_unavailable',
        errorMessage: diagnosticError?.errorMessage ?? message
      },
      recordUsage: !lastAttempt,
      usageErrorMessage: message
    })
  } finally {
    releaseClientIpSlot()
  }
}

function once(callback: () => void): () => void {
  let called = false
  return () => {
    if (called) return
    called = true
    callback()
  }
}

function recordKnownClientIpRequestError(
  error: unknown,
  usageContext: GatewayFailureUsageContext,
  auditCapture: ReturnType<typeof createAuditCapture>
): void {
  const sample = clientIpRequestErrorSample(error)
  if (!sample) {
    return
  }
  const result = recordClientIpErrorCircuitSample({
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    clientIp: usageContext.clientIp,
    endpoint: usageContext.endpoint,
    reason: sample.reason,
    signature: sample.signature
  })
  if (!result.blocked) {
    return
  }
  getRequestLogger().warn({
    event: 'gateway_client_ip_error_circuit_opened',
    reason: sample.reason,
    retryAfterSeconds: result.retryAfterSeconds,
    failureCount: result.failureCount,
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    clientIp: usageContext.clientIp
  }, '客户端 IP 级错误熔断已打开')
  auditCapture.addGatewayMetadata({
    label: 'client_ip_error_circuit',
    metadata: {
      opened: true,
      reason: sample.reason,
      retryAfterSeconds: result.retryAfterSeconds,
      failureCount: result.failureCount
    }
  })
}

function clientIpRequestErrorSample(error: unknown): { reason: 'adapter_request_validation'; signature: string } | undefined {
  if (error instanceof OpenAIOAuthCodexAdapterError) {
    return {
      reason: 'adapter_request_validation',
      signature: [error.type, error.code].filter(Boolean).join('|') || error.message
    }
  }
  return undefined
}
