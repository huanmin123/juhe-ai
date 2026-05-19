import type { NextFunction, Request, Response } from 'express'

import { createTraceId, getRequestContext, getRequestLogger, sanitizeUrlForLog } from '../../shared/request-context.js'
import type { DbServiceGatewayRuntime } from '../db-service/db-service-types.js'
import { recordDroppedAuditCapture } from '../audit-logs/audit-log-queue.service.js'
import { readCachedGatewayRuntimeAsync } from './gateway-runtime-cache.service.js'
import { extractBearerToken, extractClientIp, requestStream } from './openai-gateway-usage.js'
import {
  gatewayErrorPayload,
  sendGatewayJsonError
} from './openai-gateway-responses.js'

export type GatewayRuntimeRequest = Request & {
  gatewayRuntime?: DbServiceGatewayRuntime
}

interface ResolveGatewayRuntimeOptions {
  closeConnectionOnAuthFailure?: boolean
}

export async function preResolveOpenAIGatewayRuntime(
  req: GatewayRuntimeRequest,
  res: Response,
  next: NextFunction
): Promise<void> {
  try {
    const runtime = await resolveGatewayRuntimeAsync(req, res, { closeConnectionOnAuthFailure: true })
    if (!runtime?.apiKey) {
      recordEarlyGatewayAuthFailure(req, res)
      return
    }
    req.gatewayRuntime = runtime
    next()
  } catch (error) {
    next(error)
  }
}

export async function resolveGatewayRuntimeAsync(
  req: GatewayRuntimeRequest,
  res: Response,
  options: ResolveGatewayRuntimeOptions = {}
): Promise<DbServiceGatewayRuntime | undefined> {
  if (req.gatewayRuntime?.apiKey) {
    return req.gatewayRuntime
  }
  const gatewayApiKey = extractBearerToken(req.header('authorization'))
  if (!gatewayApiKey) {
    getRequestLogger().warn({
      event: 'gateway_auth_failed',
      reason: 'missing_bearer_token',
      endpoint: `${req.method.toUpperCase()} ${sanitizeUrlForLog(req.originalUrl)}`
    }, '网关认证失败')
    prepareEarlyAuthFailureResponse(res, options)
    sendGatewayJsonError(res, 401, gatewayErrorPayload('缺少 Bearer Token', 'invalid_request_error'))
    return undefined
  }

  const runtime = await readCachedGatewayRuntimeAsync(gatewayApiKey)
  if (!runtime.apiKey) {
    getRequestLogger().warn({
      event: 'gateway_auth_failed',
      reason: 'invalid_api_key',
      endpoint: `${req.method.toUpperCase()} ${sanitizeUrlForLog(req.originalUrl)}`
    }, '网关认证失败')
    prepareEarlyAuthFailureResponse(res, options)
    sendGatewayJsonError(res, 401, gatewayErrorPayload('API Key 无效', 'invalid_request_error'))
    return undefined
  }

  return runtime
}

export function isOpenAIStreamRequest(req: Request): boolean {
  return requestStream(req)
}

function prepareEarlyAuthFailureResponse(res: Response, options: ResolveGatewayRuntimeOptions): void {
  if (options.closeConnectionOnAuthFailure && !res.headersSent) {
    res.setHeader('Connection', 'close')
  }
}

function recordEarlyGatewayAuthFailure(req: Request, res: Response): void {
  if (!res.headersSent) return
  const context = getRequestContext()
  const authErrorMessage = extractBearerToken(req.header('authorization')) ? 'API Key 无效' : '缺少 Bearer Token'
  recordDroppedAuditCapture({
    traceId: context?.traceId ?? createTraceId(),
    auditOutcome: 'gateway_failed',
    success: false,
    bytes: 0,
    reason: 'gateway_auth_rejected',
    method: req.method,
    path: req.originalUrl.split('?')[0] || req.path,
    queryString: req.originalUrl.includes('?') ? req.originalUrl.split('?').slice(1).join('?') : undefined,
    statusCode: res.statusCode,
    errorPhase: 'auth',
    errorCode: 'invalid_request_error',
    errorMessage: authErrorMessage,
    clientIp: context?.clientIp ?? extractClientIp(req),
    userAgent: req.header('user-agent')
  })
}
