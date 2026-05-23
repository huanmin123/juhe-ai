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
import {
  inspectGatewayPreAuthCircuit,
  recordGatewayPreAuthFailure,
  type GatewayCircuitDecision,
  type GatewayPreAuthFailureReason
} from './openai-gateway-client-ip-error-circuit.service.js'

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
  const clientIp = extractClientIp(req)
  const authorization = req.header('authorization')
  const preAuthDecision = inspectGatewayPreAuthCircuit({ clientIp, authorization })
  if (preAuthDecision.blocked) {
    getRequestLogger().warn({
      event: 'gateway_pre_auth_error_circuit_blocked',
      reason: preAuthDecision.reason,
      retryAfterSeconds: preAuthDecision.retryAfterSeconds,
      failureCount: preAuthDecision.failureCount,
      endpoint: `${req.method.toUpperCase()} ${sanitizeUrlForLog(req.originalUrl)}`
    }, '网关认证前来源保护已短路请求')
    prepareEarlyAuthFailureResponse(res, options)
    sendPreAuthCircuitResponse(res, preAuthDecision)
    return undefined
  }
  const gatewayApiKey = extractBearerToken(authorization)
  if (!gatewayApiKey) {
    const failureDecision = recordPreAuthFailure(req, res, 'missing_bearer_token', options)
    if (failureDecision.blocked) {
      return undefined
    }
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
    const failureDecision = recordPreAuthFailure(req, res, 'invalid_api_key', options)
    if (failureDecision.blocked) {
      return undefined
    }
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

function recordPreAuthFailure(
  req: Request,
  res: Response,
  reason: GatewayPreAuthFailureReason,
  options: ResolveGatewayRuntimeOptions
): GatewayCircuitDecision {
  const decision = recordGatewayPreAuthFailure({
    clientIp: extractClientIp(req),
    authorization: req.header('authorization'),
    reason
  })
  if (!decision.blocked) {
    return decision
  }
  getRequestLogger().warn({
    event: 'gateway_pre_auth_error_circuit_opened',
    reason: decision.reason,
    retryAfterSeconds: decision.retryAfterSeconds,
    failureCount: decision.failureCount,
    endpoint: `${req.method.toUpperCase()} ${sanitizeUrlForLog(req.originalUrl)}`
  }, '网关认证前来源保护已进入短期熔断')
  prepareEarlyAuthFailureResponse(res, options)
  sendPreAuthCircuitResponse(res, decision)
  return decision
}

function sendPreAuthCircuitResponse(res: Response, decision: GatewayCircuitDecision): void {
  const message = '当前来源短时间认证失败过多，请稍后重试'
  if (decision.retryAfterSeconds && !res.headersSent) {
    res.setHeader('Retry-After', String(decision.retryAfterSeconds))
  }
  setGatewayAuthFailureAudit(res, {
    errorMessage: message,
    errorCode: 'client_ip_pre_auth_circuit_open'
  })
  sendGatewayJsonError(res, 429, gatewayErrorPayload(message, 'rate_limit_exceeded', 'client_ip_pre_auth_circuit_open'))
}

function setGatewayAuthFailureAudit(res: Response, input: { errorMessage: string; errorCode: string }): void {
  const locals = res.locals as Record<string, unknown>
  locals.gatewayAuthFailureErrorMessage = input.errorMessage
  locals.gatewayAuthFailureErrorCode = input.errorCode
}

function recordEarlyGatewayAuthFailure(req: Request, res: Response): void {
  if (!res.headersSent) return
  const context = getRequestContext()
  const locals = res.locals as Record<string, unknown>
  const authErrorMessage = typeof locals.gatewayAuthFailureErrorMessage === 'string'
    ? locals.gatewayAuthFailureErrorMessage
    : extractBearerToken(req.header('authorization')) ? 'API Key 无效' : '缺少 Bearer Token'
  const authErrorCode = typeof locals.gatewayAuthFailureErrorCode === 'string'
    ? locals.gatewayAuthFailureErrorCode
    : 'invalid_request_error'
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
    errorCode: authErrorCode,
    errorMessage: authErrorMessage,
    clientIp: context?.clientIp ?? extractClientIp(req),
    userAgent: req.header('user-agent')
  })
}
