import type { Request } from 'express'

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
import {
  upstreamErrorFeatureActionLogMessage,
  upstreamErrorFeatureAuditMetadata
} from './openai-gateway-audit-metadata.js'
import {
  buildUpstreamFailureSignature,
  headersFromObjectForPolicy,
  type ClientVisibleUpstreamErrorResponse,
  type UpstreamFailureSignature
} from './openai-gateway-error-helpers.js'
import { forgetOpenAIAccountForSession } from './openai-gateway-session-affinity.service.js'
import {
  matchUpstreamErrorFeatureRule,
  openAIUpstreamErrorFeatureRules,
  type UpstreamErrorFeatureDecision
} from './openai-gateway-upstream-error-rules.js'
import { isEffectiveOpenAIStreamRequest, type GatewayUpstreamResponse } from './openai-gateway-upstream.js'
import { headersToObject, requestEndpoint, type UpstreamAttempt } from './openai-gateway-usage.js'
import {
  recordFailedUpstreamAttempt,
  type GatewayUsageContext
} from './openai-gateway-usage-records.js'
import { waitBeforeTemporaryUnschedulableRetry } from './openai-gateway-dispatch-helpers.js'
import type { UpstreamAccount } from './openai-gateway-route-helpers.js'

export class UpstreamRejectedRequestError extends Error {
  constructor(
    message: string,
    readonly lastAttempt: UpstreamAttempt,
    readonly response: ClientVisibleUpstreamErrorResponse,
    readonly upstreamErrorFeature?: UpstreamErrorFeatureDecision
  ) {
    super(message)
  }
}

export interface DeferredAccountFailure {
  account: UpstreamAccount
  input: AccountFailureInput
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
  retryAttempts: number
  sessionAffinityKey?: string
  signal?: AbortSignal
  lastAttempt?: UpstreamAttempt
  deferredAccountFailures: DeferredAccountFailure[]
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
    retryAttempts,
    sessionAffinityKey,
    signal,
    deferredAccountFailures
  } = input

  const responseBodyRead = await readUpstreamBodyLimited(response.body, {
    startedAt: attemptStartedAt,
    signal
  })
  const responseBody = responseBodyRead.body
  const responseBodyText = responseBodyRead.bodyText
  const diagnosticResponseBodyText = responseBodyRead.diagnosticBodyText
  if (responseBodyRead.truncated) {
    getRequestLogger().warn({
      event: 'gateway_upstream_retry_error_body_truncated',
      accountId: account.id,
      statusCode: response.status,
      readBytes: responseBodyRead.readBytes,
      upstreamUrl
    }, '上游失败响应体超过网关捕获上限，已截断用于重试诊断')
  }
  getRequestLogger().warn({
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
  if (!responseBodyRead.truncated) {
    const parsedError = parseErrorPayload(responseBodyText, response.headers)
    const featureDecision = matchUpstreamErrorFeatureRule(openAIUpstreamErrorFeatureRules, {
      provider: 'openai',
      endpoint: requestEndpoint(req),
      stream: isEffectiveOpenAIStreamRequest(req, account),
      statusCode: response.status,
      bodyText: responseBodyText,
      parsedError
    })
    if (featureDecision) {
      getRequestLogger().warn({
        event: 'gateway_upstream_error_feature_matched',
        accountId: account.id,
        accountName: account.name,
        accountType: account.type,
        upstreamUrl,
        attemptIndex,
        auditAttemptIndex,
        statusCode: response.status,
        ruleId: featureDecision.ruleId,
        ruleName: featureDecision.ruleName,
        action: featureDecision.action,
        accountPolicy: featureDecision.accountPolicy,
        upstreamErrorType: featureDecision.upstreamErrorType,
        upstreamErrorCode: featureDecision.upstreamErrorCode,
        upstreamErrorMessage: featureDecision.upstreamErrorMessage
      }, upstreamErrorFeatureActionLogMessage(featureDecision))
      auditCapture.addGatewayMetadata({
        label: 'upstream_error_feature',
        metadata: upstreamErrorFeatureAuditMetadata(featureDecision)
      })
      throw new UpstreamRejectedRequestError(
        `命中上游错误响应特征规则 ${featureDecision.ruleName}，判定为请求级失败`,
        lastAttempt,
        {
          statusCode: response.status,
          headers: response.headers,
          body: responseBody,
          bodyText: responseBodyText
        },
        featureDecision
      )
    }
  }

  if (hasAccountErrorPolicyDecision(account, failureInput)) {
    forgetOpenAIAccountForSession(sessionAffinityKey, account.id)
    applyAccountErrorHandlingWithCacheInvalidation(account, failureInput)
    return { action: 'skip_account', lastAttempt }
  }

  if (attemptIndex < retryAttempts) {
    getRequestLogger().warn({
      event: 'gateway_upstream_same_account_retry_scheduled',
      accountId: account.id,
      accountName: account.name,
      accountType: account.type,
      upstreamUrl,
      attemptIndex,
      nextAttemptIndex: attemptIndex + 1,
      statusCode: response.status,
      retryIntervalSeconds: settings.temporaryUnschedulableRetryIntervalSeconds
    }, '上游未知失败未命中策略，先按临时不可调用标准同账号重试')
    await waitBeforeTemporaryUnschedulableRetry(settings)
    return { action: 'retry', lastAttempt }
  }

  deferUnknownAccountFailureOrRejectRequest(deferredAccountFailures, {
    account,
    input: failureInput,
    signature: buildUpstreamFailureSignature(response.headers, responseBodyText),
    lastAttempt,
    response: responseBodyRead.truncated ? undefined : {
      statusCode: response.status,
      headers: response.headers,
      body: responseBody,
      bodyText: responseBodyText
    }
  })

  return { action: 'skip_account', lastAttempt }
}

export function flushDeferredAccountFailures(deferredAccountFailures: DeferredAccountFailure[], sessionAffinityKey?: string): void {
  while (deferredAccountFailures.length > 0) {
    const failure = deferredAccountFailures.shift()
    if (!failure) {
      continue
    }
    forgetOpenAIAccountForSession(sessionAffinityKey, failure.account.id)
    applyAccountErrorHandlingWithCacheInvalidation(failure.account, failure.input)
  }
}

function hasAccountErrorPolicyDecision(account: UpstreamAccount, input: AccountFailureInput): boolean {
  const headers = input.headers instanceof Headers ? input.headers : headersFromObjectForPolicy(input.headers)
  return Boolean(decideAccountErrorPolicy(account, input.statusCode, headers, Buffer.from(input.bodyText), input.settings))
}

function deferUnknownAccountFailureOrRejectRequest(
  deferredAccountFailures: DeferredAccountFailure[],
  failure: DeferredAccountFailure
): void {
  const matchedFailure = failure.signature
    ? deferredAccountFailures.find((item) => item.account.id !== failure.account.id && item.signature?.key === failure.signature?.key)
    : undefined

  if (matchedFailure && failure.signature && failure.response) {
    getRequestLogger().warn({
      event: 'gateway_request_failure_signature_confirmed',
      firstAccountId: matchedFailure.account.id,
      firstAccountName: matchedFailure.account.name,
      secondAccountId: failure.account.id,
      secondAccountName: failure.account.name,
      statusCode: failure.lastAttempt.status,
      failureSignature: failure.signature.label
    }, '多个上游账号返回一致错误，按请求级失败返回客户端')
    throw new UpstreamRejectedRequestError(
      '多个上游账号返回一致错误，判定为请求级失败：' + failure.signature.label,
      failure.lastAttempt,
      failure.response
    )
  }

  deferredAccountFailures.push(failure)
}
