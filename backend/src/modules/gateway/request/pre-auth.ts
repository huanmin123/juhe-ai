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
  getTraceId,
  logRequestStage,
  sanitizeUrlForLog
} from '../../../shared/request-context.js'
import type { DbServiceGatewayRuntime } from '../../db-service/db-service-types.js'
import { dispatchAuditLogToGo } from '../../audit-logs/audit-log-go-input.service.js'
import { readAuditLogSettings } from '../../audit-logs/audit-log-settings.js'
import { nowIso } from '../../../storage/database.js'
import { randomUUID } from 'node:crypto'
import type { AuditLogInput } from '../../../storage/audit-log-types.js'
import { readCachedGatewayRuntimeAsync } from '../runtime/runtime-cache.service.js'
import { inspectClientIpPolicy, recordClientIpPolicyHitAsync } from '../runtime/client-ip-policy-cache.service.js'
import { startUserRequestLimitCoordinator } from '../runtime/user-request-limit-coordinator.js'
import { userRequestLimitCounter, type UserRequestLimitDecision } from '../runtime/user-request-limit-counter.js'
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
  inspectGatewayPreAuthCircuitAsync,
  recordGatewayPreAuthFailureAsync,
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
import { errorLogFields } from '../../../shared/logger.js'
import {
  validateGatewayApiKeyAsync,
  type GatewayApiKeyRow
} from '../../../storage/gateway-api-key.repository.js'

export type GatewayRuntimeRequest = Request & {
  gatewayRuntime?: DbServiceGatewayRuntime
}

startUserRequestLimitCoordinator()

interface ResolveGatewayRuntimeOptions {
  closeConnectionOnAuthFailure?: boolean
  inspectClientIpPolicyAfterRuntime?: boolean
}

export async function preResolveGatewayRuntime(
  req: GatewayRuntimeRequest,
  res: Response,
  next: NextFunction
): Promise<void> {
  const stageStartedAt = performance.now()
  let resolutionReason: string | undefined
  let resolutionError: unknown
  let resolutionOutcome: 'success' | 'expected_failure' | 'unexpected_failure' = 'success'
  try {
    if (isGatewayModelsRequest(req)) {
      resolutionReason = 'models_endpoint'
      next()
      return
    }
    const runtime = await resolveGatewayRuntimeAsync(req, res, {
      closeConnectionOnAuthFailure: true,
      inspectClientIpPolicyAfterRuntime: false
    })
    if (!runtime?.apiKey) {
      resolutionOutcome = 'expected_failure'
      resolutionReason = 'runtime_unresolved'
      recordEarlyGatewayAuthFailure(req, res)
      return
    }
    if (isImageGenerationDisabledForApiKey(runtime.apiKey, resolveOpenAIGatewayRequestLane(req))) {
      resolutionOutcome = 'expected_failure'
      resolutionReason = 'image_generation_disabled'
      sendEarlyImageGenerationDisabledResponse(req, res)
      return
    }
    if (await rejectCachedClientIpBlacklist(req, res, extractClientIp(req), { closeConnectionOnAuthFailure: true }, { cacheOnly: true })) {
      resolutionOutcome = 'expected_failure'
      resolutionReason = 'client_ip_blacklisted'
      return
    }
    const userRequestLimitDecision = userRequestLimitCounter.consume({
      systemAccountId: runtime.apiKey.system_account_id,
      settings: runtime.settings,
      overrides: runtime.apiKey.system_account_request_limits
    })
    if (!userRequestLimitDecision.allowed) {
      resolutionOutcome = 'expected_failure'
      resolutionReason = 'user_request_limit_exceeded'
      sendUserRequestLimitExceededResponse(req, res, userRequestLimitDecision)
      return
    }
    req.gatewayRuntime = runtime
    next()
  } catch (error) {
    resolutionOutcome = 'unexpected_failure'
    resolutionReason = error instanceof Error ? error.name : 'NonErrorThrown'
    resolutionError = error
    next(error)
  } finally {
    logRequestStage('runtime_resolution', {
      traceId: getTraceId(),
      resolved: Boolean(req.gatewayRuntime?.apiKey),
      reason: resolutionReason,
      ...(resolutionOutcome === 'unexpected_failure' ? { error: resolutionError } : {}),
      ...(resolutionOutcome === 'expected_failure' ? { failureReason: resolutionReason } : {})
    }, resolutionOutcome, stageStartedAt)
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
  const preAuthDecision = await inspectGatewayPreAuthCircuitAsync({ clientIp, authorization: gatewayAuthSource })
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
    const failureDecision = await recordPreAuthFailure(req, res, 'missing_bearer_token', options)
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
    const failureDecision = await recordPreAuthFailure(req, res, 'invalid_api_key', options)
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
  if (options.inspectClientIpPolicyAfterRuntime !== false && await rejectCachedClientIpBlacklist(req, res, clientIp, options, { cacheOnly: true })) {
    return undefined
  }

  return runtime
}

export async function resolveGatewayApiKeyForModelsAsync(
  req: GatewayRuntimeRequest,
  res: Response,
  options: ResolveGatewayRuntimeOptions = {}
): Promise<GatewayApiKeyRow | undefined> {
  const clientIp = extractClientIp(req)
  const authorization = req.header('authorization')
  const gatewayAuthSource = gatewayPreAuthSource(req, authorization)
  if (await rejectCachedClientIpBlacklist(req, res, clientIp, options, { cacheOnly: true })) {
    return undefined
  }
  const preAuthDecision = await inspectGatewayPreAuthCircuitAsync({ clientIp, authorization: gatewayAuthSource })
  if (preAuthDecision.blocked) {
    prepareEarlyAuthFailureResponse(res, options)
    sendPreAuthCircuitResponse(req, res, preAuthDecision)
    return undefined
  }
  const gatewayApiKey = extractGatewayApiKey(req, authorization)
  if (!gatewayApiKey) {
    await rejectMissingOrInvalidGatewayCredential(req, res, 'missing_bearer_token', options)
    return undefined
  }
  const apiKey = await validateGatewayApiKeyAsync(gatewayApiKey)
  if (!apiKey) {
    await rejectMissingOrInvalidGatewayCredential(req, res, 'invalid_api_key', options)
    return undefined
  }
  if (options.inspectClientIpPolicyAfterRuntime !== false
    && await rejectCachedClientIpBlacklist(req, res, clientIp, options, { cacheOnly: true })) {
    return undefined
  }
  return apiKey
}

async function rejectMissingOrInvalidGatewayCredential(
  req: Request,
  res: Response,
  reason: Extract<GatewayPreAuthFailureReason, 'missing_bearer_token' | 'invalid_api_key'>,
  options: ResolveGatewayRuntimeOptions
): Promise<void> {
  const failureDecision = await recordPreAuthFailure(req, res, reason, options)
  if (failureDecision.blocked) return
  getRequestLogger().warn({
    event: 'gateway_auth_failed',
    reason,
    endpoint: `${req.method.toUpperCase()} ${sanitizeUrlForLog(req.originalUrl)}`
  }, '网关认证失败')
  prepareEarlyAuthFailureResponse(res, options)
  sendGatewayJsonError(
    res,
    401,
    gatewayErrorPayload(reason === 'invalid_api_key' ? 'API Key 无效' : '缺少访问令牌', 'invalid_request_error'),
    { protocol: gatewayErrorProtocolForRequest(req) }
  )
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
  const blacklistPolicy = ipPolicyDecision.blacklistPolicy
  getRequestLogger().warn({
    event: 'gateway_client_ip_blacklist_blocked',
    policyId: blacklistPolicy.id,
    ipHash: blacklistPolicy.ipHash,
    endpoint: `${req.method.toUpperCase()} ${sanitizeUrlForLog(req.originalUrl)}`
  }, '网关来源 IP 命中管理员封禁')
  recordClientIpPolicyHitAsync(blacklistPolicy).catch((error) => {
    getRequestLogger().warn(errorLogFields(error, {
      event: 'gateway_client_ip_blacklist_hit_record_failed',
      policyId: blacklistPolicy.id,
      ipHash: blacklistPolicy.ipHash
    }), '记录 IP 封禁命中失败，已继续返回封禁响应')
  })
  prepareEarlyAuthFailureResponse(res, options)
  sendClientIpBlacklistResponse(req, res, {
    reason: blacklistPolicy.reason,
    clientIp: ipPolicyDecision.normalizedIp?.clientIp ?? blacklistPolicy.clientIp,
    aggregateIpKey: ipPolicyDecision.normalizedIp?.aggregateIpKey ?? blacklistPolicy.aggregateIpKey
  })
  return true
}

async function recordPreAuthFailure(
  req: Request,
  res: Response,
  reason: GatewayPreAuthFailureReason,
  options: ResolveGatewayRuntimeOptions
): Promise<GatewayCircuitDecision> {
  const decision = await recordGatewayPreAuthFailureAsync({
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
  dispatchDroppedAuditCapture({
    traceId: context?.traceId ?? createTraceId(),
    trafficSource: 'gateway',
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
  dispatchDroppedAuditCapture({
    traceId: context?.traceId ?? createTraceId(),
    trafficSource: 'gateway',
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

function sendUserRequestLimitExceededResponse(req: Request, res: Response, decision: UserRequestLimitDecision): void {
  const message = `你的${userRequestLimitWindowLabel(decision.window)}请求数已达到 ${decision.limit ?? 0} 次，请联系管理员提升额度。`
  if (decision.retryAfterSeconds) {
    res.setHeader('Retry-After', String(decision.retryAfterSeconds))
  }
  setGatewayAuthFailureAudit(res, {
    errorMessage: message,
    errorCode: 'user_request_limit_exceeded'
  })
  sendGatewayJsonError(
    res,
    429,
    gatewayErrorPayload(message, 'rate_limit_exceeded', 'user_request_limit_exceeded'),
    { protocol: gatewayErrorProtocolForRequest(req) }
  )
  const context = getRequestContext()
  dispatchDroppedAuditCapture({
    traceId: context?.traceId ?? createTraceId(),
    trafficSource: 'gateway',
    auditOutcome: 'gateway_failed',
    success: false,
    bytes: 0,
    reason: 'user_request_limit_exceeded',
    method: req.method,
    path: req.originalUrl.split('?')[0] || req.path,
    queryString: req.originalUrl.includes('?') ? req.originalUrl.split('?').slice(1).join('?') : undefined,
    statusCode: 429,
    errorPhase: 'authorization',
    errorCode: 'user_request_limit_exceeded',
    errorMessage: message,
    clientIp: context?.clientIp ?? extractClientIp(req),
    userAgent: req.header('user-agent')
  })
}

function userRequestLimitWindowLabel(window: UserRequestLimitDecision['window']): string {
  if (window === 'perMinute') return '每分钟'
  if (window === 'perDay') return '每日'
  if (window === 'perWeek') return '每周'
  return '每月'
}

function dispatchDroppedAuditCapture(input: {
  traceId: string
  trafficSource: AuditLogInput['trafficSource']
  auditOutcome: AuditLogInput['auditOutcome']
  success: boolean
  bytes: number
  reason: 'gateway_auth_rejected' | 'gateway_permission_rejected' | 'user_request_limit_exceeded'
  method?: string
  path?: string
  queryString?: string
  statusCode?: number
  errorPhase?: string
  errorCode?: string
  errorMessage?: string
  clientIp?: string
  userAgent?: string
}): void {
  if (!readAuditLogSettings().enabled) return
  const timestamp = nowIso()
  const rawPath = input.path?.trim() || 'unknown'
  const [path, ...queryParts] = sanitizeUrlForLog(input.queryString ? `${rawPath}?${input.queryString}` : rawPath).split('?')
  dispatchAuditLogToGo({
    id: `audit_${Date.now()}_${randomUUID()}`,
    lifecycleStatus: 'finalized',
    traceId: input.traceId,
    trafficSource: input.trafficSource,
    auditOutcome: input.auditOutcome,
    success: input.success,
    method: input.method?.toUpperCase() ?? 'UNKNOWN',
    path: path || 'unknown',
    queryString: queryParts.length ? queryParts.join('?') : undefined,
    clientIp: input.clientIp,
    userAgent: input.userAgent,
    finalStatusCode: input.statusCode,
    errorPhase: input.errorPhase,
    errorCode: input.errorCode,
    errorMessage: input.errorMessage,
    sampleBucket: 0,
    sampleReason: input.reason,
    captureStatus: 'complete',
    startedAt: timestamp,
    endedAt: timestamp,
    attempts: [],
    payloads: []
  })
}

function gatewayErrorProtocolForRequest(req: Request): 'openai' | 'anthropic' | 'gemini' {
  return gatewayProtocolClientErrorProtocolForRequest(req)
}
