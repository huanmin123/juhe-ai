import type { Request } from 'express'

import { errorLogFields } from '../../shared/logger.js'
import { getRequestLogger } from '../../shared/request-context.js'
import {
  decideAccountErrorPolicy,
  parseErrorPayload,
  type GatewaySettings
} from './account-error-policy.service.js'
import type { AuditCaptureContext } from './audit-capture.service.js'
import {
  applyAccountErrorHandlingWithCacheInvalidation,
  persistOpenAICodexHeadersIfNeeded
} from './openai-gateway-account-effects.js'
import { readUpstreamBodyLimited } from './openai-gateway-body.js'
import { downstreamConnectionClosedMessage } from './openai-gateway-client-abort.js'
import { recordGatewayAccountFailureForPrecheck, suppressGatewayAccountLocally } from './gateway-account-side-effects.service.js'
import { forgetOpenAIAccountForSession } from './openai-gateway-session-affinity.service.js'
import {
  isEffectiveOpenAIStreamRequest,
  isUpstreamRequestAbortedError,
  type GatewayUpstreamResponse
} from './openai-gateway-upstream.js'
import { headersToObject, requestEndpoint, type UpstreamAttempt } from './openai-gateway-usage.js'
import {
  recordFailedUpstreamAttempt,
  type GatewayUsageContext
} from './openai-gateway-usage-records.js'
import { isCooldownRetestTrafficSource } from './openai-gateway-traffic-source.js'
import {
  rememberFailedProxyForDispatch,
  shouldRecordAbortedUpstreamAttempt,
} from './openai-gateway-dispatch-helpers.js'
import {
  rememberClientIpAccountPendingFailure,
  type ClientIpAccountAvoidanceTracker
} from './openai-gateway-client-ip-account-avoidance.service.js'
import {
  gatewayProxyKey,
  recordGatewayUpstreamBucketFailure
} from './openai-gateway-proxy-health.service.js'
import type { UpstreamAccount } from './openai-gateway-route-helpers.js'

export type AccountFailureInput = {
  success: false
  statusCode: number
  headers: Headers | Record<string, string | string[]>
  bodyText: string
  settings: GatewaySettings
  trafficSource?: GatewayUsageContext['trafficSource']
}

interface HandleFailedUpstreamResponseInput {
  req: Request
  usageContext: GatewayUsageContext
  auditCapture: AuditCaptureContext
  auditAttemptId: string
  account: UpstreamAccount
  upstreamUrl: string
  response: GatewayUpstreamResponse
  settings: GatewaySettings
  attemptStartedAt: number
  attemptIndex: number
  auditAttemptIndex: number
  sessionAffinityKey?: string
  signal?: AbortSignal
  lastAttempt?: UpstreamAttempt
  clientIpAccountAvoidanceTracker?: ClientIpAccountAvoidanceTracker
  accountStateMutationEnabled?: boolean
}

interface HandleUpstreamRequestErrorInput {
  req: Request
  usageContext: GatewayUsageContext
  auditCapture: AuditCaptureContext
  auditAttemptId: string
  account: UpstreamAccount
  upstreamUrl: string
  attemptStartedAt: number
  attemptIndex: number
  auditAttemptIndex: number
  settings: GatewaySettings
  sessionAffinityKey?: string
  signal?: AbortSignal
  lastAttempt?: UpstreamAttempt
  failedProxyDispatchKeys: Map<string, string>
  error: unknown
  clientIpAccountAvoidanceTracker?: ClientIpAccountAvoidanceTracker
  accountStateMutationEnabled?: boolean
}

export async function handleFailedUpstreamResponse(
  input: HandleFailedUpstreamResponseInput
): Promise<{ action: 'retry' | 'skip_account'; lastAttempt: UpstreamAttempt }> {
  const {
    req,
    usageContext,
    auditCapture,
    auditAttemptId,
    account,
    upstreamUrl,
    response,
    settings,
    attemptStartedAt,
    attemptIndex,
    auditAttemptIndex,
    sessionAffinityKey,
    signal,
    clientIpAccountAvoidanceTracker
  } = input

  const responseBodyRead = await readUpstreamBodyLimited(response.body, {
    startedAt: attemptStartedAt,
    signal
  })
  const responseBody = responseBodyRead.body
  const responseBodyText = responseBodyRead.bodyText
  const diagnosticResponseBodyText = responseBodyRead.diagnosticBodyText
  if (responseBodyRead.truncated) {
    logGatewayFailureWarning(usageContext, {
      event: 'gateway_upstream_retry_error_body_truncated',
      accountId: account.id,
      statusCode: response.status,
      readBytes: responseBodyRead.readBytes,
      upstreamUrl
    }, '上游失败响应体超过网关捕获上限，已截断用于重试诊断')
  }
  logGatewayFailureWarning(usageContext, {
    event: 'gateway_upstream_response_failed',
    accountId: account.id,
    accountType: account.type,
    upstreamUrl,
    attemptIndex,
    auditAttemptIndex,
    statusCode: response.status,
    contentType: response.headers.get('content-type'),
    elapsedMs: Date.now() - attemptStartedAt,
    responseBodyBytes: responseBody.byteLength,
    responseBodyTruncated: responseBodyRead.truncated
  }, '上游返回非成功状态')

  const lastAttempt: UpstreamAttempt = {
    ...(input.lastAttempt ?? {
      accountId: account.id,
      accountName: account.name,
      upstreamUrl,
      status: response.status
    }),
    responseHeaders: headersToObject(response.headers),
    responseBodyText: diagnosticResponseBodyText
  }

  auditCapture.completeAttempt(auditAttemptId, {
    statusCode: response.status,
    responseHeaders: response.headers,
    responseBody,
    success: false,
    errorPhase: 'upstream_response',
    errorMessage: diagnosticResponseBodyText
  })
  recordFailedUpstreamAttempt(req, usageContext, account, {
    upstreamUrl,
    startedAt: attemptStartedAt,
    statusCode: response.status,
    headers: response.headers,
    bodyText: diagnosticResponseBodyText
  })
  persistOpenAICodexHeadersIfNeeded(
    account,
    response.headers,
    usageContext.trafficSource === 'gateway' ? 'gateway_error' : usageContext.trafficSource
  )

  const failureInput: AccountFailureInput = {
    success: false,
    statusCode: response.status,
    headers: response.headers,
    bodyText: responseBodyText,
    settings,
    trafficSource: usageContext.trafficSource
  }
  let parsedError: Record<string, unknown> = {}
  if (!responseBodyRead.truncated) {
    parsedError = parseErrorPayload(responseBodyText, response.headers)
  }
  forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
  const accountStateMutationEnabled = input.accountStateMutationEnabled !== false
  const upstreamBucketFailure = accountStateMutationEnabled && usageContext.trafficSource === 'gateway'
    ? recordGatewayUpstreamBucketFailure(account, '上游响应失败')
    : undefined
  const policyDecision = accountStateMutationEnabled && usageContext.trafficSource === 'manual_account_test'
    ? decideAccountErrorPolicy(account, failureInput.statusCode, response.headers, Buffer.from(responseBodyText), settings)
    : undefined
  if (policyDecision) {
    if (policyDecisionChangesRuntimeAvailability(policyDecision.action)) {
      suppressGatewayAccountLocally(
        account,
        settings,
        responseBodyRead.truncated
          ? `上游账号返回非成功状态：HTTP ${response.status}`
          : `上游账号返回非成功状态：HTTP ${response.status}`
      )
    }
    applyAccountErrorHandlingWithCacheInvalidation(account, failureInput)
  } else if (accountStateMutationEnabled && usageContext.trafficSource === 'gateway' && upstreamBucketFailure?.suspected !== true) {
    recordGatewayAccountFailureForPrecheck(account, settings, {
      systemAccountId: usageContext.systemAccountId,
      groupId: usageContext.groupId,
      apiKeyId: usageContext.apiKeyId,
      clientIp: usageContext.clientIp,
      endpoint: requestEndpoint(req),
      statusCode: response.status,
      reason: responseBodyRead.truncated
        ? `上游账号返回非成功状态：HTTP ${response.status}`
        : stringValue(parsedError.message) || diagnosticResponseBodyText || `上游账号返回非成功状态：HTTP ${response.status}`
    })
  }

  rememberClientIpAccountPendingFailure(clientIpAccountAvoidanceTracker, account, {
    statusCode: response.status,
    errorCode: stringValue(parsedError.code) || undefined,
    errorType: stringValue(parsedError.type) || undefined,
    errorPhase: 'upstream_response',
    errorMessage: stringValue(parsedError.message) || diagnosticResponseBodyText || undefined,
    endpoint: requestEndpoint(req)
  })

  return { action: 'skip_account', lastAttempt }
}

export async function handleUpstreamRequestError(
  input: HandleUpstreamRequestErrorInput
): Promise<{ action: 'retry' | 'skip_account'; lastAttempt?: UpstreamAttempt }> {
  const {
    req,
    usageContext,
    auditCapture,
    auditAttemptId,
    account,
    upstreamUrl,
    attemptStartedAt,
    attemptIndex,
    auditAttemptIndex,
    settings,
    sessionAffinityKey,
    signal,
    failedProxyDispatchKeys,
    error,
    clientIpAccountAvoidanceTracker
  } = input

  if (isUpstreamRequestAbortedError(error) || signal?.aborted) {
    let lastAttempt = input.lastAttempt
    if (shouldRecordAbortedUpstreamAttempt(error)) {
      const statusCode = lastAttempt?.accountId === account.id && lastAttempt.upstreamUrl === upstreamUrl
        ? lastAttempt.status
        : undefined
      recordFailedUpstreamAttempt(req, usageContext, account, {
        upstreamUrl,
        startedAt: attemptStartedAt,
        statusCode,
        errorMessage: downstreamConnectionClosedMessage
      })
      lastAttempt = {
        accountId: account.id,
        accountName: account.name,
        upstreamUrl,
        status: statusCode,
        message: downstreamConnectionClosedMessage
      }
    }
    auditCapture.completeAttempt(auditAttemptId, {
      success: false,
      errorPhase: 'client',
      errorMessage: downstreamConnectionClosedMessage
    })
    throw error
  }

  const message = formatUpstreamRequestErrorMessage(error)
  logGatewayFailureWarning(usageContext, errorLogFields(error, {
    event: 'gateway_upstream_request_failed',
    accountId: account.id,
    accountType: account.type,
    upstreamUrl,
    attemptIndex,
    auditAttemptIndex,
    elapsedMs: Date.now() - attemptStartedAt,
    stream: isEffectiveOpenAIStreamRequest(req, account)
  }), '网关请求上游失败')
  const lastAttempt: UpstreamAttempt = {
    accountId: account.id,
    accountName: account.name,
    upstreamUrl,
    message
  }
  auditCapture.completeAttempt(auditAttemptId, {
    success: false,
    errorPhase: 'upstream_request',
    errorMessage: message
  })
  recordFailedUpstreamAttempt(req, usageContext, account, {
    upstreamUrl,
    startedAt: attemptStartedAt,
    errorMessage: message
  })
  if (isRealUpstreamUrl(upstreamUrl)) {
    rememberClientIpAccountPendingFailure(clientIpAccountAvoidanceTracker, account, {
      errorPhase: 'upstream_request',
      errorMessage: message,
      endpoint: requestEndpoint(req)
    })
  }
  forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
  const upstreamBucketFailure = input.accountStateMutationEnabled !== false && usageContext.trafficSource === 'gateway' && isRealUpstreamUrl(upstreamUrl)
    ? recordGatewayUpstreamBucketFailure(account, '上游请求异常', {
        bucketScope: gatewayProxyKey(account) ? 'proxy' : 'upstream'
      })
    : undefined
  if (input.accountStateMutationEnabled !== false && usageContext.trafficSource === 'gateway' && isRealUpstreamUrl(upstreamUrl) && upstreamBucketFailure?.suspected !== true) {
    recordGatewayAccountFailureForPrecheck(account, settings, {
      systemAccountId: usageContext.systemAccountId,
      groupId: usageContext.groupId,
      apiKeyId: usageContext.apiKeyId,
      clientIp: usageContext.clientIp,
      endpoint: requestEndpoint(req),
      reason: `上游账号请求异常：${message}`
    })
  }
  if (input.accountStateMutationEnabled !== false && upstreamBucketFailure?.suspected !== true && shouldSuppressAccountForUpstreamRequestError(account)) {
    suppressGatewayAccountLocally(account, settings, `上游账号请求异常：${message}`)
  }
  rememberFailedProxyForDispatch(failedProxyDispatchKeys, account, message)
  return { action: 'skip_account', lastAttempt }
}

function shouldSuppressAccountForUpstreamRequestError(account: UpstreamAccount): boolean {
  // 直连传输错误通常是上游或本机连接压力，不是账号本身异常；长时间屏蔽会在高并发下把可用账号误伤清空。
  return Boolean(gatewayProxyKey(account))
}

export function formatUpstreamRequestErrorMessage(error: unknown): string {
  const explicitMessage = error instanceof Error ? error.message.trim() : stringValue(error)
  if (explicitMessage) {
    return explicitMessage
  }

  const code = objectStringProperty(error, 'code')
  if (code) {
    return `请求失败：${code}`
  }

  const name = error instanceof Error ? error.name : objectStringProperty(error, 'name')
  if (name && name !== 'Error') {
    return `请求失败：${name}`
  }

  return '请求失败'
}

function policyDecisionChangesRuntimeAvailability(action: 'retry_next' | 'cooldown' | 'disable'): boolean {
  return action === 'cooldown' || action === 'disable'
}

function logGatewayFailureWarning(
  usageContext: GatewayUsageContext | undefined,
  fields: Record<string, unknown>,
  message: string
): void {
  const logger = getRequestLogger()
  const enrichedFields = {
    ...fields,
    trafficSource: usageContext?.trafficSource
  }
  if (isCooldownRetestTrafficSource(usageContext?.trafficSource)) {
    logger.debug(enrichedFields, message)
    return
  }
  logger.warn(enrichedFields, message)
}

function objectStringProperty(value: unknown, key: string): string | undefined {
  if (typeof value !== 'object' || value === null) {
    return undefined
  }
  const property = (value as Record<string, unknown>)[key]
  return typeof property === 'string' && property.trim() ? property.trim() : undefined
}

function stringValue(value: unknown): string {
  return typeof value === 'string' && value.trim() ? value.trim() : ''
}

function isRealUpstreamUrl(value: string): boolean {
  return /^https?:\/\//i.test(value)
}
