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

export const openAIGatewayRouter = Router()

export type { OpenAIGatewayRequestIdentity } from './openai-gateway-request-preflight.js'

export interface OpenAIGatewayHandleOptions {
  identity?: OpenAIGatewayRequestIdentity
  candidateAccounts?: UpstreamAccount[]
  disableSessionAffinity?: boolean
  exposeUpstreamDiagnostics?: boolean
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
  return /^DB service (暂时不可用|未就绪|请求超时|请求队列已满|已退出)/.test(error.message)
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
  const requestSnapshot = buildUsageRequestSnapshot(req, traceId, clientIp)
  const auditCapture = createAuditCapture({ req, traceId, clientIp, startedAtMs: startedAt })
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
    options,
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
  const { activeGatewaySettings, usageContext: gatewayUsageContext, accounts, sessionAffinityKey, clientStrategy } = preflight

  try {
    const upstreamResult = await fetchFirstAvailableUpstream(
      req,
      accounts,
      activeGatewaySettings,
      gatewayUsageContext,
      auditCapture,
      sessionAffinityKey,
      abortController.signal
    )
    const { account, response: upstreamResponse, upstreamUrl, auditAttemptId, releaseConcurrency, markFirstOutput } = upstreamResult

    try {
      const contentType = upstreamResponse.headers.get('content-type') ?? ''
      const shouldHandleAsStream = isOpenAIStreamContentType(contentType) || isEffectiveOpenAIStreamRequest(req, account)
      prepareUpstreamResponseForDownstream(res, upstreamResponse, shouldHandleAsStream)
      persistOpenAICodexHeadersIfNeeded(account, upstreamResponse.headers, 'gateway')

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
          markFirstOutput
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
          markFirstOutput
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
        result: handledResponse
      })
    } finally {
      releaseConcurrency()
    }
  } catch (error) {
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
  }
}
