import type { Request } from 'express'

import { getRequestLogger, sanitizeUrlCredentialsForLog } from '../../../shared/request-context.js'
import {
  requestEndpoint
} from '../request/metadata.js'
import {
  type UpstreamAttempt
} from '../upstream/attempt.js'
import {
  type GatewaySettings
} from '../policy/account-error-policy.service.js'
import type { AuditCaptureContext } from '../audit/capture.service.js'
import {
  applyAccountErrorHandlingWithCacheInvalidation,
  persistOpenAICodexHeadersIfNeeded
} from '../runtime/account-effects.js'
import { recordGatewayAccountApiKeyLocalFailure } from '../runtime/account-api-key-effects.service.js'
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
import { isAccountProbeTrafficSource } from '../usage/traffic-source.js'
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
  recordGatewayUpstreamBucketFailureAsync
} from '../runtime/proxy-health.service.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import {
  parseGatewayProtocolErrorPayload
} from '../protocols/registry.js'

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
  | { action: 'retry' | 'skip_account'; lastAttempt: UpstreamAttempt; keyScopedFailure?: boolean; pendingApiKeyFailure?: PendingAccountApiKeyFailure }

export interface PendingAccountApiKeyFailure {
  account: UpstreamAccount
  status: 'temporary_unavailable' | 'rate_limited' | 'error'
  statusCode?: number
  errorCode?: string
  errorMessage?: string
  cooldownUntil?: string
}

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
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId,
      protocolCode: account.protocolCode,
      protocolVersion: account.protocolVersion,
      upstreamUrl,
      status: response.status
    }),
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
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
  await recordFailedUpstreamAttempt(req, usageContext, account, {
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
    parsedError = parseGatewayProtocolErrorPayload(account, responseBodyText, response.headers)
  }
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
  const isolateAccountApiKeyFailure = hasAlternativeAccountApiKeys(account)
  const responseKeyFailoverEligible = accountStateMutationEnabled
    && isolateAccountApiKeyFailure
    && !isAccountProbeTrafficSource(usageContext.trafficSource)
    && isRealUpstreamUrl(upstreamUrl)
  const apiKeyFailureStatus = 'temporary_unavailable'
  if (responseKeyFailoverEligible) {
    await recordGatewayAccountApiKeyLocalFailure(account, {
      status: apiKeyFailureStatus,
      errorMessage: parsedErrorMessage || diagnosticErrorMessage || undefined
    })
  }
  if (accountStateMutationEnabled && usageContext.trafficSource === 'gateway') {
    await recordGatewayUpstreamBucketFailureAsync(account, '上游响应失败')
  }
  if (accountStateMutationEnabled && !isAccountProbeTrafficSource(usageContext.trafficSource) && !isolateAccountApiKeyFailure) {
    const reason = responseBodyRead.truncated
      ? `上游账号返回非成功状态：HTTP ${response.status}`
      : parsedErrorMessage || diagnosticErrorMessage || `上游账号返回非成功状态：HTTP ${response.status}`
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
        forcePrecheck: localSuppression.action === 'precheck_required',
        localSuppressionDelayMs: localSuppression.delayMs
      })
    } else {
      await applyAccountErrorHandlingWithCacheInvalidation(account, failureInput)
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

  return {
    action: 'skip_account',
    lastAttempt,
    keyScopedFailure: responseKeyFailoverEligible,
    pendingApiKeyFailure: responseKeyFailoverEligible
      ? {
          account,
          status: apiKeyFailureStatus,
          statusCode: response.status,
          errorCode: stringValue(parsedError.code) || undefined,
          errorMessage: parsedErrorMessage || diagnosticErrorMessage || undefined,
        }
      : undefined
  }
}

export async function handleUpstreamRequestError(
  input: HandleUpstreamRequestErrorInput
): Promise<{ action: 'retry' | 'skip_account'; lastAttempt?: UpstreamAttempt; keyScopedFailure?: boolean }> {
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
      await recordFailedUpstreamAttempt(req, usageContext, account, {
        upstreamUrl,
        startedAt: attemptStartedAt,
        statusCode,
        errorMessage: downstreamConnectionClosedMessage,
        failureAttribution: 'client_lifecycle'
      })
      lastAttempt = {
        accountId: account.id,
        accountName: account.name,
        providerCode: account.providerCode,
        providerProtocolProfileId: account.providerProtocolProfileId,
        protocolCode: account.protocolCode,
        protocolVersion: account.protocolVersion,
        upstreamUrl,
        status: statusCode,
        message: downstreamConnectionClosedMessage
      }
    }
    completeOrRecordFailedAttempt({
      req,
      auditCapture,
      auditAttemptId,
      account,
      upstreamUrl,
      attemptStartedAt,
      auditAttemptIndex,
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
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    upstreamUrl,
    message
  }
  completeOrRecordFailedAttempt({
    req,
    auditCapture,
    auditAttemptId,
    account,
    upstreamUrl,
    attemptStartedAt,
    auditAttemptIndex,
    success: false,
    errorPhase: 'upstream_request',
    errorMessage: message
  })
  await recordFailedUpstreamAttempt(req, usageContext, account, {
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
  const isolateAccountApiKeyFailure = hasAlternativeAccountApiKeys(account)
  if (accountStateMutationEnabled && usageContext.trafficSource === 'gateway' && isRealUpstreamUrl(upstreamUrl)) {
    await recordGatewayUpstreamBucketFailureAsync(account, '上游请求异常', {
      bucketScope: gatewayProxyKey(account) ? 'proxy' : 'upstream'
    })
  }
  if (accountStateMutationEnabled && !isAccountProbeTrafficSource(usageContext.trafficSource) && !isolateAccountApiKeyFailure) {
    const reason = `上游账号请求异常：${message}`
    const localSuppression = suppressGatewayAccountLocally(account, settings, reason)
    if (usageContext.trafficSource === 'gateway' && shouldRecordPrecheckForRequestFailure(upstreamUrl)) {
      recordGatewayAccountFailureForPrecheck(account, settings, {
        systemAccountId: usageContext.systemAccountId,
        groupId: usageContext.groupId,
        apiKeyId: usageContext.apiKeyId,
        clientIp: usageContext.clientIp,
        endpoint: requestEndpoint(req),
        reason,
        forcePrecheck: localSuppression.action === 'precheck_required',
        localSuppressionDelayMs: localSuppression.delayMs
      })
    } else {
      await applyAccountErrorHandlingWithCacheInvalidation(account, {
        success: false,
        errorMessage: message,
        settings,
        trafficSource: usageContext.trafficSource
      })
    }
  }
  if (isRealUpstreamUrl(upstreamUrl)) {
    rememberFailedProxyForDispatch(failedProxyDispatchKeys, account, message)
  }
  return {
    action: 'skip_account',
    lastAttempt,
    keyScopedFailure: false
  }
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
  if (isAccountProbeTrafficSource(usageContext?.trafficSource)) {
    logger.debug(enrichedFields, message)
    return
  }
  logger.warn(enrichedFields, message)
}

function hasAlternativeAccountApiKeys(account: UpstreamAccount): boolean {
  return Boolean(account.selectedApiKeyFingerprint) && (account.apiKeys?.length ?? 0) > 1
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

function shouldRecordPrecheckForRequestFailure(upstreamUrl: string): boolean {
  return isRealUpstreamUrl(upstreamUrl) || upstreamUrl === 'account:preparation'
}

function completeOrRecordFailedAttempt(input: {
  req: Request
  auditCapture: AuditCaptureContext
  auditAttemptId: string
  account: UpstreamAccount
  upstreamUrl: string
  attemptStartedAt: number
  auditAttemptIndex: number
  success: false
  statusCode?: number
  errorPhase: string
  errorCode?: string
  errorMessage?: string
}): void {
  const {
    req,
    auditCapture,
    auditAttemptId,
    account,
    upstreamUrl,
    attemptStartedAt,
    auditAttemptIndex,
    statusCode,
    errorPhase,
    errorCode,
    errorMessage
  } = input
  if (auditAttemptId) {
    auditCapture.completeAttempt(auditAttemptId, {
      statusCode,
      success: false,
      errorPhase,
      errorCode,
      errorMessage
    })
    return
  }
  auditCapture.recordFailedDispatchAttempt({
    account,
    attemptIndex: auditAttemptIndex,
    upstreamUrl,
    method: req.method,
    startedAtMs: attemptStartedAt,
    statusCode,
    errorPhase,
    errorCode,
    errorMessage,
    requestForModelAccounting: req
  })
}
