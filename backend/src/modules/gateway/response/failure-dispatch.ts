import type { Request } from 'express'

import { getRequestLogger, sanitizeUrlCredentialsForLog } from '../../../shared/request-context.js'
import {
  requestEndpoint
} from '../request/metadata.js'
import {
  type UpstreamAttempt
} from '../upstream/attempt.js'
import {
  accountErrorPolicyReason,
  decideAccountErrorPolicy,
  parseErrorPayload,
  type AccountErrorPolicyDecision,
  type GatewaySettings
} from '../policy/account-error-policy.service.js'
import type { AuditCaptureContext } from '../audit/capture.service.js'
import {
  applyAccountErrorHandlingWithCacheInvalidation,
  persistOpenAICodexHeadersIfNeeded
} from '../runtime/account-effects.js'
import { readUpstreamBodyLimited } from '../upstream/body.js'
import { downstreamConnectionClosedMessage } from './client-abort.js'
import {
  recordGatewayAccountFailureForPrecheck,
  suppressGatewayAccountLocally
} from '../runtime/account-side-effects.service.js'
import { forgetOpenAIAccountForSession } from '../runtime/session-affinity.service.js'
import {
  isEffectiveOpenAIStreamRequest,
  isUpstreamRequestAbortedError,
  type GatewayUpstreamResponse
} from '../upstream/request.js'
import { headersToObject } from '../upstream/headers.js'
import {
  recordFailedUpstreamAttempt,
  type GatewayUsageContext
} from '../usage/records.js'
import { isCooldownRetestTrafficSource } from '../usage/traffic-source.js'
import {
  rememberFailedProxyForDispatch,
  shouldRecordAbortedUpstreamAttempt,
} from '../dispatch/helpers.js'
import {
  rememberClientIpAccountPendingFailure,
  type ClientIpAccountAvoidanceTracker
} from '../runtime/client-ip-account-avoidance.service.js'
import {
  gatewayProxyKey,
  recordGatewayUpstreamBucketFailure
} from '../runtime/proxy-health.service.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import {
  decideGatewayCompatibilityRecovery,
  recordGatewayCompatibilityRecoveryDecision,
  type GatewayCompatibilityRecoveryState
} from '../client-profiles/compatibility-policy.js'

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
  retrySameAccount?: boolean
  requestBody?: Buffer | string
  compatibilityRecoveryState?: GatewayCompatibilityRecoveryState
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
  retrySameAccount?: boolean
}

type HandleFailedUpstreamResponseResult =
  | { action: 'retry' | 'skip_account'; lastAttempt: UpstreamAttempt }
  | { action: 'retry_with_body_variant'; lastAttempt: UpstreamAttempt; body: Buffer }

export async function handleFailedUpstreamResponse(
  input: HandleFailedUpstreamResponseInput
): Promise<HandleFailedUpstreamResponseResult> {
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
  const safeUpstreamUrl = sanitizeUrlCredentialsForLog(upstreamUrl) ?? 'unknown'
  if (responseBodyRead.truncated) {
    logGatewayFailureWarning(usageContext, {
      event: 'gateway_upstream_retry_error_body_truncated',
      accountId: account.id,
      statusCode: response.status,
      readBytes: responseBodyRead.readBytes,
      upstreamUrl: safeUpstreamUrl
    }, '上游失败响应体超过网关捕获上限，已截断用于重试诊断')
  }
  logGatewayFailureWarning(usageContext, {
    event: 'gateway_upstream_response_failed',
    accountId: account.id,
    accountType: account.type,
    upstreamUrl: safeUpstreamUrl,
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
  if (!responseBodyRead.truncated && input.compatibilityRecoveryState) {
    const compatibilityRecovery = await decideGatewayCompatibilityRecovery({
      req,
      account,
      upstreamUrl,
      body: input.requestBody,
      responseBodyText,
      parsedError,
      recoveryState: input.compatibilityRecoveryState,
      signal
    })
    recordGatewayCompatibilityRecoveryDecision(auditCapture, compatibilityRecovery)
    if (compatibilityRecovery.action === 'retry_with_body_variant') {
      return {
        action: 'retry_with_body_variant',
        lastAttempt,
        body: compatibilityRecovery.body
      }
    }
  }
  const policyDecision = responseBodyRead.truncated
    ? undefined
    : decideAccountErrorPolicy(account, response.status, response.headers, Buffer.from(responseBodyText), settings)
  const parsedErrorMessage = stringValue(parsedError.message)
  const diagnosticErrorMessage = diagnosticResponseBodyText
  if (input.retrySameAccount) {
    auditCapture.addGatewayMetadata({
      label: 'same_account_retry_response_failed',
      metadata: {
        accountId: account.id,
        upstreamUrl: safeUpstreamUrl,
        statusCode: response.status,
        attemptIndex,
        auditAttemptIndex
      }
    })
    return { action: 'retry', lastAttempt }
  }

  forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
  const accountStateMutationEnabled = input.accountStateMutationEnabled !== false
  if (accountStateMutationEnabled && usageContext.trafficSource === 'gateway') {
    recordGatewayUpstreamBucketFailure(account, '上游响应失败')
  }
  if (accountStateMutationEnabled && !isCooldownRetestTrafficSource(usageContext.trafficSource)) {
    const reason = responseBodyRead.truncated
      ? `上游账号返回非成功状态：HTTP ${response.status}`
      : errorPolicyFailureReason(response.status, policyDecision, parsedErrorMessage || diagnosticErrorMessage)
    const localSuppression = suppressGatewayAccountLocally(
      account,
      settings,
      reason
    )
    if (usageContext.trafficSource === 'gateway') {
      recordGatewayAccountFailureForPrecheck(account, settings, {
        systemAccountId: usageContext.systemAccountId,
        groupId: usageContext.groupId,
        apiKeyId: usageContext.apiKeyId,
        clientIp: usageContext.clientIp,
        endpoint: requestEndpoint(req),
        reason,
        statusCode: response.status,
        errorPolicyDecision: policyDecision,
        forcePrecheck: localSuppression.action === 'precheck_required'
      })
    } else {
      applyAccountErrorHandlingWithCacheInvalidation(account, failureInput)
    }
  }

  rememberClientIpAccountPendingFailure(clientIpAccountAvoidanceTracker, account, {
    statusCode: response.status,
    errorCode: stringValue(parsedError.code) || undefined,
    errorType: stringValue(parsedError.type) || undefined,
    errorPhase: 'upstream_response',
    errorMessage: parsedErrorMessage || diagnosticErrorMessage || undefined,
    endpoint: requestEndpoint(req)
  })

  return { action: 'skip_account', lastAttempt }
}

function errorPolicyFailureReason(
  statusCode: number,
  decision: AccountErrorPolicyDecision | undefined,
  fallbackMessage: string | undefined
): string {
  if (decision) {
    return accountErrorPolicyReason(statusCode, decision, fallbackMessage)
  }
  return fallbackMessage || `上游账号返回非成功状态：HTTP ${statusCode}`
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
    forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
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
  const safeUpstreamUrl = sanitizeUrlCredentialsForLog(upstreamUrl) ?? 'unknown'
  logGatewayFailureWarning(usageContext, {
    event: 'gateway_upstream_request_failed',
    accountId: account.id,
    accountType: account.type,
    upstreamUrl: safeUpstreamUrl,
    attemptIndex,
    auditAttemptIndex,
    elapsedMs: Date.now() - attemptStartedAt,
    stream: isEffectiveOpenAIStreamRequest(req, account),
    errorName: sanitizeOptionalDiagnosticPayload(error instanceof Error ? error.name : objectStringProperty(error, 'name')),
    errorCode: sanitizeOptionalDiagnosticPayload(objectStringProperty(error, 'code')),
    errorMessage: message
  }, '网关请求上游失败')
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
    upstreamUrl: safeUpstreamUrl,
    startedAt: attemptStartedAt,
    errorMessage: message
  })
  if (input.retrySameAccount) {
    auditCapture.addGatewayMetadata({
      label: 'same_account_retry_request_failed',
      metadata: {
        accountId: account.id,
        upstreamUrl: safeUpstreamUrl,
        attemptIndex,
        auditAttemptIndex,
        errorMessage: message
      }
    })
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
  const accountStateMutationEnabled = input.accountStateMutationEnabled !== false
  if (accountStateMutationEnabled && usageContext.trafficSource === 'gateway' && isRealUpstreamUrl(upstreamUrl)) {
    recordGatewayUpstreamBucketFailure(account, '上游请求异常', {
      bucketScope: gatewayProxyKey(account) ? 'proxy' : 'upstream'
    })
  }
  if (accountStateMutationEnabled && !isCooldownRetestTrafficSource(usageContext.trafficSource)) {
    const reason = `上游账号请求异常：${message}`
    const localSuppression = suppressGatewayAccountLocally(account, settings, reason)
    if (usageContext.trafficSource === 'gateway' && isRealUpstreamUrl(upstreamUrl)) {
      recordGatewayAccountFailureForPrecheck(account, settings, {
        systemAccountId: usageContext.systemAccountId,
        groupId: usageContext.groupId,
        apiKeyId: usageContext.apiKeyId,
        clientIp: usageContext.clientIp,
        endpoint: requestEndpoint(req),
        reason,
        forcePrecheck: localSuppression.action === 'precheck_required'
      })
    } else {
      applyAccountErrorHandlingWithCacheInvalidation(account, {
        success: false,
        errorMessage: message,
        settings,
        trafficSource: usageContext.trafficSource
      })
    }
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

function sanitizeOptionalDiagnosticPayload(value: string | undefined): string | undefined {
  return value
}

function isRealUpstreamUrl(value: string): boolean {
  return /^https?:\/\//i.test(value)
}
