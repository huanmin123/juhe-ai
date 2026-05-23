import type { Request } from 'express'

import { errorLogFields } from '../../shared/logger.js'
import { getRequestLogger } from '../../shared/request-context.js'
import { retryDelayMs, shouldRetryPolicyAttempt, type RetryPolicy } from '../../shared/retry-policy.js'
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
import {
  buildUpstreamFailureSignature,
  headersFromObjectForPolicy,
  type ClientVisibleUpstreamErrorResponse,
  type UpstreamFailureSignature
} from './openai-gateway-error-helpers.js'
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
  waitBeforeTemporaryUnschedulableRetry
} from './openai-gateway-dispatch-helpers.js'
import {
  rememberClientIpAccountPendingFailure,
  type ClientIpAccountAvoidanceTracker
} from './openai-gateway-client-ip-account-avoidance.service.js'
import {
  isHighConfidenceProxyRequestError,
  recordGatewayProxyFailure
} from './openai-gateway-proxy-health.service.js'
import type { UpstreamAccount } from './openai-gateway-route-helpers.js'

export class UpstreamRejectedRequestError extends Error {
  constructor(
    message: string,
    readonly lastAttempt: UpstreamAttempt,
    readonly response: ClientVisibleUpstreamErrorResponse,
    readonly failureSignature?: UpstreamFailureSignature,
    readonly confirmedAccountIds: string[] = []
  ) {
    super(message)
  }
}

export interface DeferredAccountFailure {
  account: UpstreamAccount
  signature?: UpstreamFailureSignature
  lastAttempt: UpstreamAttempt
  response?: ClientVisibleUpstreamErrorResponse
}

export type AccountFailureInput = {
  success: false
  statusCode: number
  headers: Headers | Record<string, string | string[]>
  bodyText: string
  settings: GatewaySettings
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
  retryPolicy: RetryPolicy
  sessionAffinityKey?: string
  signal?: AbortSignal
  lastAttempt?: UpstreamAttempt
  deferredAccountFailures: DeferredAccountFailure[]
  requestFailureSignatureConfirmationThreshold: number
  clientIpAccountAvoidanceTracker?: ClientIpAccountAvoidanceTracker
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
  retryPolicy: RetryPolicy
  sessionAffinityKey?: string
  signal?: AbortSignal
  lastAttempt?: UpstreamAttempt
  failedProxyDispatchKeys: Map<string, string>
  error: unknown
  clientIpAccountAvoidanceTracker?: ClientIpAccountAvoidanceTracker
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
    retryPolicy,
    sessionAffinityKey,
    signal,
    deferredAccountFailures,
    requestFailureSignatureConfirmationThreshold,
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
  persistOpenAICodexHeadersIfNeeded(account, response.headers, 'gateway_error')

  const failureInput: AccountFailureInput = {
    success: false,
    statusCode: response.status,
    headers: response.headers,
    bodyText: responseBodyText,
    settings
  }
  let parsedError: Record<string, unknown> = {}
  if (!responseBodyRead.truncated) {
    parsedError = parseErrorPayload(responseBodyText, response.headers)
  }

  if (hasAccountErrorPolicyDecision(account, failureInput)) {
    forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
    applyAccountErrorHandlingWithCacheInvalidation(account, failureInput)
    return { action: 'skip_account', lastAttempt }
  }

  if (shouldRetryPolicyAttempt(attemptIndex, retryPolicy)) {
    logGatewayFailureWarning(usageContext, {
      event: 'gateway_upstream_same_account_retry_scheduled',
      accountId: account.id,
      accountName: account.name,
      accountType: account.type,
      upstreamUrl,
      attemptIndex,
      nextAttemptIndex: attemptIndex + 1,
      statusCode: response.status,
      retryDelayMs: retryDelayMs(retryPolicy, attemptIndex + 1),
      retryIntervalSeconds: settings.temporaryUnschedulableRetryIntervalSeconds
    }, '上游未知失败未命中策略，先按短重试策略同账号重试')
    await waitBeforeTemporaryUnschedulableRetry(retryPolicy, attemptIndex + 1)
    return { action: 'retry', lastAttempt }
  }

  deferUnknownAccountFailureOrRejectRequest(
    deferredAccountFailures,
    {
      account,
      signature: buildUpstreamFailureSignature(response.headers, responseBodyText),
      lastAttempt,
      response: responseBodyRead.truncated ? undefined : {
        statusCode: response.status,
        headers: response.headers,
        body: responseBody,
        bodyText: responseBodyText
      }
    },
    requestFailureSignatureConfirmationThreshold
  )
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
    retryPolicy,
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
        errorMessage: '请求已取消'
      })
      lastAttempt = {
        accountId: account.id,
        accountName: account.name,
        upstreamUrl,
        status: statusCode,
        message: '请求已取消'
      }
    }
    auditCapture.completeAttempt(auditAttemptId, {
      success: false,
      errorPhase: 'client',
      errorMessage: '请求已取消'
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
  if (shouldRetryPolicyAttempt(attemptIndex, retryPolicy)) {
    await waitBeforeTemporaryUnschedulableRetry(retryPolicy, attemptIndex + 1)
    return { action: 'retry', lastAttempt }
  }
  if (isRealUpstreamUrl(upstreamUrl)) {
    rememberClientIpAccountPendingFailure(clientIpAccountAvoidanceTracker, account, {
      errorPhase: 'upstream_request',
      errorMessage: message,
      endpoint: requestEndpoint(req)
    })
  }
  forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
  if (isHighConfidenceProxyRequestError(error)) {
    recordGatewayProxyFailure(account, message)
  }
  rememberFailedProxyForDispatch(failedProxyDispatchKeys, account, message)
  return { action: 'skip_account', lastAttempt }
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

export function flushDeferredAccountFailures(deferredAccountFailures: DeferredAccountFailure[], sessionAffinityKey?: string): void {
  while (deferredAccountFailures.length > 0) {
    const failure = deferredAccountFailures.shift()
    if (!failure) {
      continue
    }
    forgetOpenAIAccountForSession(sessionAffinityKey, failure.account.id)
  }
}

function hasAccountErrorPolicyDecision(account: UpstreamAccount, input: AccountFailureInput): boolean {
  const headers = input.headers instanceof Headers ? input.headers : headersFromObjectForPolicy(input.headers)
  return Boolean(decideAccountErrorPolicy(account, input.statusCode, headers, Buffer.from(input.bodyText), input.settings))
}

function deferUnknownAccountFailureOrRejectRequest(
  deferredAccountFailures: DeferredAccountFailure[],
  failure: DeferredAccountFailure,
  confirmationThreshold: number
): void {
  const matchedFailures = failure.signature
    ? deferredAccountFailures.filter((item) => item.account.id !== failure.account.id && item.signature?.key === failure.signature?.key)
    : []
  const confirmedFailures = [...matchedFailures, failure]
  const confirmedAccountIds = [...new Set(confirmedFailures.map((item) => item.account.id))]
  const threshold = Math.max(2, Math.trunc(confirmationThreshold))

  if (failure.signature && failure.response && confirmedAccountIds.length >= threshold) {
    const firstFailure = matchedFailures[0]
    logGatewayFailureWarning(undefined, {
      event: 'gateway_request_failure_signature_confirmed',
      firstAccountId: firstFailure?.account.id,
      firstAccountName: firstFailure?.account.name,
      lastAccountId: failure.account.id,
      lastAccountName: failure.account.name,
      confirmedAccountIds,
      confirmationThreshold: threshold,
      statusCode: failure.lastAttempt.status,
      failureSignature: failure.signature.label
    }, '多个上游账号返回一致错误，按请求级失败返回客户端')
    throw new UpstreamRejectedRequestError(
      '多个上游账号返回一致错误，判定为请求级失败：' + failure.signature.label,
      failure.lastAttempt,
      failure.response,
      failure.signature,
      confirmedAccountIds
    )
  }

  deferredAccountFailures.push(failure)
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
