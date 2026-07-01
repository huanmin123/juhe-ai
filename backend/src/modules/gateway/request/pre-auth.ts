import type { NextFunction, Request, Response } from 'express'

import {
  extractBearerToken,
  extractClientIp,
  requestStream
} from './metadata.js'
import {
  createTraceId,
  getRequestContext,
  getRequestLogger,
  sanitizeUrlForLog
} from '../../../shared/request-context.js'
import type { DbServiceGatewayRuntime } from '../../db-service/db-service-types.js'
import { recordDroppedAuditCapture } from '../../audit-logs/audit-log-queue.service.js'
import { readCachedGatewayRuntimeAsync } from '../runtime/runtime-cache.service.js'
import { inspectClientIpPolicy, recordClientIpPolicyHitAsync } from '../runtime/client-ip-policy-cache.service.js'
import {
  gatewayErrorPayload,
  sendGatewayJsonError
} from '../response/responses.js'
import {
  imageGenerationDisabledCode,
  imageGenerationDisabledMessage,
  isImageGenerationDisabledForApiKey
} from './image-permission.js'
import { resolveOpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'
import { GEMINI_PROTOCOL_CODE } from '../../../domain/provider-protocol.js'
import {
  inspectGatewayPreAuthCircuit,
  recordGatewayPreAuthFailure,
  type GatewayCircuitDecision,
  type GatewayPreAuthFailureReason
} from '../runtime/client-ip-error-circuit.service.js'
import {
  gatewayProtocolClientErrorProtocolForRequest,
  isGatewayProtocolNativeRequest
} from '../protocols/registry.js'
import { isOpenAIModelsRequest } from '../protocols/openai-v1/route-helpers.js'
import { isAnthropicModelsRequest } from '../protocols/anthropic-v1/route-helpers.js'
import { isGeminiModelsRequest } from '../protocols/gemini-v1beta/route-helpers.js'

export type GatewayRuntimeRequest = Request & {
  gatewayRuntime?: DbServiceGatewayRuntime
}

interface ResolveGatewayRuntimeOptions {
  closeConnectionOnAuthFailure?: boolean
  inspectClientIpPolicyAfterRuntime?: boolean
}

export async function preResolveGatewayRuntime(
  req: GatewayRuntimeRequest,
  res: Response,
  next: NextFunction
): Promise<void> {
  try {
    if (isGatewayModelsRequest(req)) {
      next()
      return
    }
    const runtime = await resolveGatewayRuntimeAsync(req, res, {
      closeConnectionOnAuthFailure: true,
      inspectClientIpPolicyAfterRuntime: false
    })
    if (!runtime?.apiKey) {
      recordEarlyGatewayAuthFailure(req, res)
      return
    }
    if (isImageGenerationDisabledForApiKey(runtime.apiKey, resolveOpenAIGatewayRequestLane(req))) {
      sendEarlyImageGenerationDisabledResponse(req, res)
      return
    }
    if (await rejectCachedClientIpBlacklist(req, res, extractClientIp(req), { closeConnectionOnAuthFailure: true }, { cacheOnly: false })) {
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
  const gatewayAuthSource = gatewayPreAuthSource(req, authorization)
  if (await rejectCachedClientIpBlacklist(req, res, clientIp, options, { cacheOnly: true })) {
    return undefined
  }
  const preAuthDecision = inspectGatewayPreAuthCircuit({ clientIp, authorization: gatewayAuthSource })
  if (preAuthDecision.blocked) {
    getRequestLogger().warn({
      event: 'gateway_pre_auth_error_circuit_blocked',
      reason: preAuthDecision.reason,
      retryAfterSeconds: preAuthDecision.retryAfterSeconds,
      failureCount: preAuthDecision.failureCount,
      endpoint: `${req.method.toUpperCase()} ${sanitizeUrlForLog(req.originalUrl)}`
    }, '网关认证前来源保护已短路请求')
    prepareEarlyAuthFailureResponse(res, options)
    sendPreAuthCircuitResponse(req, res, preAuthDecision)
    return undefined
  }
  const gatewayApiKey = extractGatewayApiKey(req, authorization)
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
    sendGatewayJsonError(res, 401, gatewayErrorPayload('缺少访问令牌', 'invalid_request_error'), {
      protocol: gatewayErrorProtocolForRequest(req)
    })
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
    sendGatewayJsonError(res, 401, gatewayErrorPayload('API Key 无效', 'invalid_request_error'), {
      protocol: gatewayErrorProtocolForRequest(req)
    })
    return undefined
  }
  if (options.inspectClientIpPolicyAfterRuntime !== false && await rejectCachedClientIpBlacklist(req, res, clientIp, options, { cacheOnly: false })) {
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

async function rejectCachedClientIpBlacklist(
  req: Request,
  res: Response,
  clientIp: string | undefined,
  options: ResolveGatewayRuntimeOptions,
  policyOptions: { cacheOnly: boolean }
): Promise<boolean> {
  const ipPolicyDecision = await inspectClientIpPolicy(clientIp, { cacheOnly: policyOptions.cacheOnly })
  if (!ipPolicyDecision.blocked || !ipPolicyDecision.blacklistPolicy) {
    return false
  }
  getRequestLogger().warn({
    event: 'gateway_client_ip_blacklist_blocked',
    policyId: ipPolicyDecision.blacklistPolicy.id,
    ipHash: ipPolicyDecision.blacklistPolicy.ipHash,
    endpoint: `${req.method.toUpperCase()} ${sanitizeUrlForLog(req.originalUrl)}`
  }, '网关来源 IP 命中管理员封禁')
  recordClientIpPolicyHitAsync(ipPolicyDecision.blacklistPolicy)
  prepareEarlyAuthFailureResponse(res, options)
  sendClientIpBlacklistResponse(req, res, {
    reason: ipPolicyDecision.blacklistPolicy.reason,
    clientIp: ipPolicyDecision.normalizedIp?.clientIp ?? ipPolicyDecision.blacklistPolicy.clientIp,
    aggregateIpKey: ipPolicyDecision.normalizedIp?.aggregateIpKey ?? ipPolicyDecision.blacklistPolicy.aggregateIpKey
  })
  return true
}

function recordPreAuthFailure(
  req: Request,
  res: Response,
  reason: GatewayPreAuthFailureReason,
  options: ResolveGatewayRuntimeOptions
): GatewayCircuitDecision {
  const decision = recordGatewayPreAuthFailure({
    clientIp: extractClientIp(req),
    authorization: gatewayPreAuthSource(req, req.header('authorization')),
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
  sendPreAuthCircuitResponse(req, res, decision)
  return decision
}

export function extractGatewayApiKey(req: Request, authorization?: string): string | undefined {
  return extractBearerToken(authorization)
    ?? headerToken(req, 'x-api-key')
    ?? geminiNativeGatewayApiKey(req)
}

function gatewayPreAuthSource(req: Request, authorization?: string): string | undefined {
  const bearer = extractBearerToken(authorization)
  if (bearer) return authorization
  const apiKey = headerToken(req, 'x-api-key')
  if (apiKey) return `x-api-key ${apiKey}`
  const geminiKey = geminiNativeGatewayApiKey(req)
  if (geminiKey) return `gemini-key ${geminiKey}`
  return authorization
}

function headerToken(req: Request, name: string): string | undefined {
  const value = req.header(name)
  const text = typeof value === 'string' ? value.trim() : ''
  return text || undefined
}

function geminiNativeGatewayApiKey(req: Request): string | undefined {
  if (!isGatewayProtocolNativeRequest(req, GEMINI_PROTOCOL_CODE)) {
    return undefined
  }
  return headerToken(req, 'x-goog-api-key') ?? queryToken(req, 'key')
}

function isGatewayModelsRequest(req: Request): boolean {
  return isOpenAIModelsRequest(req) || isAnthropicModelsRequest(req) || isGeminiModelsRequest(req)
}

function queryToken(req: Request, name: string): string | undefined {
  const queryIndex = req.originalUrl.indexOf('?')
  if (queryIndex < 0) return undefined
  const value = new URLSearchParams(req.originalUrl.slice(queryIndex + 1)).get(name)
  const text = typeof value === 'string' ? value.trim() : ''
  return text || undefined
}

function sendPreAuthCircuitResponse(req: Request, res: Response, decision: GatewayCircuitDecision): void {
  const message = '当前来源短时间认证失败过多，请稍后重试'
  if (decision.retryAfterSeconds && !res.headersSent) {
    res.setHeader('Retry-After', String(decision.retryAfterSeconds))
  }
  setGatewayAuthFailureAudit(res, {
    errorMessage: message,
    errorCode: 'client_ip_pre_auth_circuit_open'
  })
  sendGatewayJsonError(res, 429, gatewayErrorPayload(message, 'rate_limit_exceeded', 'client_ip_pre_auth_circuit_open'), {
    protocol: gatewayErrorProtocolForRequest(req)
  })
}

function sendClientIpBlacklistResponse(req: Request, res: Response, input: { reason?: string; clientIp?: string; aggregateIpKey?: string }): void {
  const ipText = blacklistIpMessage(input.clientIp, input.aggregateIpKey)
  const message = input.reason
    ? `当前来源${ipText}已被管理员封禁：${input.reason}`
    : `当前来源${ipText}已被管理员封禁`
  setGatewayAuthFailureAudit(res, {
    errorMessage: message,
    errorCode: 'client_ip_blacklisted'
  })
  const payload = gatewayErrorPayload(message, 'forbidden', 'client_ip_blacklisted')
  if (input.clientIp) {
    payload.error.client_ip = input.clientIp
  }
  if (input.aggregateIpKey && input.aggregateIpKey !== input.clientIp) {
    payload.error.aggregate_ip_key = input.aggregateIpKey
  }
  sendGatewayJsonError(res, 403, payload, {
    protocol: gatewayErrorProtocolForRequest(req)
  })
}

function blacklistIpMessage(clientIp?: string, aggregateIpKey?: string): string {
  const displayIp = clientIp?.trim()
  const displayRange = aggregateIpKey?.trim()
  if (!displayIp && !displayRange) return ''
  if (displayIp && displayRange && displayIp !== displayRange) {
    return ` IP ${displayIp}（封禁范围：${displayRange}）`
  }
  return ` IP ${displayIp || displayRange}`
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
    : extractBearerToken(req.header('authorization')) ? 'API Key 无效' : '缺少访问令牌'
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

function sendEarlyImageGenerationDisabledResponse(req: Request, res: Response): void {
  if (!res.headersSent) {
    sendGatewayJsonError(
      res,
      403,
      gatewayErrorPayload(imageGenerationDisabledMessage, 'forbidden', imageGenerationDisabledCode),
      { protocol: gatewayErrorProtocolForRequest(req) }
    )
  }
  const context = getRequestContext()
  recordDroppedAuditCapture({
    traceId: context?.traceId ?? createTraceId(),
    auditOutcome: 'gateway_failed',
    success: false,
    bytes: 0,
    reason: 'gateway_permission_rejected',
    method: req.method,
    path: req.originalUrl.split('?')[0] || req.path,
    queryString: req.originalUrl.includes('?') ? req.originalUrl.split('?').slice(1).join('?') : undefined,
    statusCode: 403,
    errorPhase: 'authorization',
    errorCode: imageGenerationDisabledCode,
    errorMessage: imageGenerationDisabledMessage,
    clientIp: context?.clientIp ?? extractClientIp(req),
    userAgent: req.header('user-agent')
  })
}

function gatewayErrorProtocolForRequest(req: Request): 'openai' | 'anthropic' | 'gemini' {
  return gatewayProtocolClientErrorProtocolForRequest(req)
}
