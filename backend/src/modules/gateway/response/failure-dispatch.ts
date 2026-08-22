import type { Request } from 'express'

import { getRequestLogger, sanitizeUrlCredentialsForLog } from '../../../shared/request-context.js'
import {
  type UpstreamAttempt
} from '../upstream/attempt.js'
import {
  decideAccountErrorPolicy,
  accountErrorPayloadSummary,
  type AccountErrorPolicyDecision,
  type GatewaySettings
} from '../policy/account-error-policy.service.js'
import type { AuditCaptureContext } from '../audit/capture.service.js'
import {
  applyAccountErrorHandlingWithCacheInvalidation,
  persistOpenAICodexHeadersIfNeeded
} from '../runtime/account-effects.js'
import {
  readUpstreamBodyForPolicyInspection
} from '../upstream/body.js'
import { downstreamConnectionClosedMessage } from './client-abort.js'
import { gatewayRequestAbortSource } from '../request/abort-attribution.js'
import { forgetOpenAIAccountForSessionAsync } from '../runtime/session-affinity.service.js'
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
import {
  isAccountDiagnosticTrafficSource,
  isAccountProbeTrafficSource
} from '../usage/traffic-source.js'
import {
  shouldRecordAbortedUpstreamAttempt,
} from '../dispatch/helpers.js'
import {
  type ClientIpAccountAvoidanceTracker
} from '../runtime/client-ip-account-avoidance.service.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { classifyGatewayUpstreamFailure } from './upstream-failure-classifier.js'
import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'
import { dispatchRequestFailureAccountHealthCheck } from './request-failure-health-check.js'
import { resolveOpenAIGatewayClientStrategy } from '../client-profiles/strategy.js'
import {
  rememberGatewayClientSourceFailureAsync
} from '../client-profiles/client-source-avoidance.service.js'
import {
  runGatewayClientSourceAvoidanceAvailabilityProbe
} from '../client-profiles/client-source-availability-probe.service.js'
import { parseGatewayProtocolErrorPayloadFromJsonValue } from '../protocols/registry.js'
import {
  parseGatewayNonStreamJsonBody,
  type GatewayNonStreamJsonBody
} from './non-stream-json-body.js'
import {
  recoverCodexEncryptedContentRequest,
  type CodexEncryptedContentRecoveryResult
} from '../request/codex-encrypted-content-recovery.js'
import type { ClientCompatibilityCapability } from '../../../domain/types.js'
import {
  captureGatewayAccountApiKeyFailureObservation,
  recordGatewayAccountApiKeyFailure
} from '../runtime/account-api-key-effects.service.js'
import type { AccountApiKeyPersistentMutationContext } from '../runtime/account-api-key-mutation-authority.js'
import { quotaRecoveryErrorCode } from '../policy/api-key-quota-recovery.js'

/**
 * Opaque HTTP failures may not retry a sibling API Key.  Account-level
 * candidate failover is handled by handleFailedUpstreamResponse instead.
 */
export function isOpaqueUpstreamFailoverAllowed(_req: Request): boolean {
  return false
}

/** `retry_next` only authorizes replay with another Key on the same account. */
export function accountErrorPolicyAllowsUpstreamReplayAfterDispatch(
  _req: Request,
  _lane: OpenAIGatewayRequestLane,
  _decision?: Pick<AccountErrorPolicyDecision, 'action'>
): boolean {
  return _decision?.action === 'retry_next'
}

/** Normalized failed-response facts used by account-state and failover handling. */
export type AccountFailureInput = {
  success: false
  statusCode: number
  headers: Headers | Record<string, string | string[]>
  bodyText: string
  settings: GatewaySettings
  trafficSource?: GatewayUsageContext['trafficSource']
  policyDecision?: AccountErrorPolicyDecision
  upstreamErrorSummary?: string
  upstreamErrorSummaryResolved?: boolean
}

interface ParsedFailureBodyFacts {
  parsedJsonBody: GatewayNonStreamJsonBody
  errorPayload: Record<string, unknown>
  upstreamErrorSummary?: string
  upstreamErrorSummaryResolved: true
}

interface HandleFailedUpstreamResponseInput {
  req: Request
  requestLane: OpenAIGatewayRequestLane
  usageContext: GatewayUsageContext
  auditCapture: AuditCaptureContext
  auditAttemptId: string
  account: UpstreamAccount
  upstreamUrl: string
  response: GatewayUpstreamResponse
  requestBody?: Buffer | string
  requestClientCompatibility?: ClientCompatibilityCapability
  settings: GatewaySettings
  attemptStartedAt: number
  attemptIndex: number
  auditAttemptIndex: number
  sessionAffinityKey?: string
  signal?: AbortSignal
  lastAttempt?: UpstreamAttempt
  clientIpAccountAvoidanceTracker?: ClientIpAccountAvoidanceTracker
  accountStateMutationEnabled?: boolean
  automaticAccountStateMutationEnabled?: boolean
  /**
   * A pre-commit transient response will be retried with the exact same
   * physical credential by the dispatcher.  It must not be interpreted as
   * evidence for rotating to a sibling Key or for recording Key avoidance.
   * Explicit retry_next policies deliberately retain their existing behavior.
   */
  deferAutomaticSameAccountKeyRotation?: boolean
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

type HandleFailedUpstreamResponseResult =
  | { action: 'retry' | 'skip_account'; failureKind: 'explicit_policy' | 'opaque_http'; lastAttempt: UpstreamAttempt; keyScopedFailure?: boolean; pendingApiKeyFailure?: PendingAccountApiKeyFailure; tryNextApiKeyForRequest?: boolean }
  | { action: 'retry_with_compatibility_recovery'; failureKind: 'compatibility_recovery'; lastAttempt: UpstreamAttempt; recovery: Extract<CodexEncryptedContentRecoveryResult, { action: 'retry_with_body_variant' }> }
  | { action: 'return_response'; response: GatewayUpstreamResponse }

export interface PendingAccountApiKeyFailure {
  account: UpstreamAccount
  status: 'temporary_unavailable' | 'rate_limited' | 'error'
  mutationContext?: AccountApiKeyPersistentMutationContext
  observationEpoch?: number
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
    signal
  } = input

  // Account diagnostics must observe the provider's actual terminal HTTP
  // response. Generic gateway takeover would hide the sampled 10/20/30 status
  // sequence and make the diagnostic result depend on routing candidates.
  if (isAccountDiagnosticTrafficSource(usageContext.trafficSource)) {
    return { action: 'return_response', response }
  }

  // Account diagnostics and non-gateway probes must observe their actual
  // response.  Customer gateway traffic follows the generic candidate
  // failover path for every complete non-2xx response; user policy matching
  // only decides whether to persist an account state/action.
  const gatewayFailoverEnabled = usageContext.trafficSource === 'gateway'
  if (!gatewayFailoverEnabled) {
    await forgetOpenAIAccountForSessionAsync(sessionAffinityKey, account.id)
    return { action: 'return_response', response }
  }

  const responseBodyRead = await readUpstreamBodyForPolicyInspection(response.body, {
    signal
  })
  const responseBody = responseBodyRead.body
  const responseBodyText = responseBodyRead.bodyText
  const diagnosticResponseBodyText = responseBodyRead.diagnosticBodyText
  const safeUpstreamUrl = sanitizeUrlCredentialsForLog(upstreamUrl) ?? 'unknown'
  const failureBodyFacts = parseFailureBodyFacts(responseBodyText, response.headers, account)
  const explicitPolicyDecision = decideAccountErrorPolicy(
    account,
    response.status,
    response.headers,
    responseBody,
    settings,
    { bodyText: responseBodyText, errorPayload: failureBodyFacts.errorPayload }
  )
  await responseBodyRead.close()
  const failureObservation = classifyGatewayUpstreamFailure({
    phase: 'upstream_response'
  })
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
    responseBodyTruncated: responseBodyRead.truncated,
    ...failureObservation
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
    responseBodyText: diagnosticResponseBodyText,
    parsedResponseBody: failureBodyFacts.parsedJsonBody
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
    bodyText: diagnosticResponseBodyText,
    errorPayload: failureBodyFacts.errorPayload
  })
  const compatibilityRecovery = await recoverCodexEncryptedContentRequest({
    req,
    account,
    requestClientCompatibility: input.requestClientCompatibility,
    body: input.requestBody,
    upstreamErrorText: responseBodyText,
    signal
  })
  if (compatibilityRecovery.action === 'retry_with_body_variant') {
    auditCapture.addGatewayMetadata({
      label: 'codex_encrypted_content_recovery_retry',
      metadata: {
        accountId: account.id,
        upstreamUrl: safeUpstreamUrl,
        transport: 'http',
        ...compatibilityRecovery.metadata
      }
    })
    return {
      action: 'retry_with_compatibility_recovery',
      failureKind: 'compatibility_recovery',
      lastAttempt,
      recovery: compatibilityRecovery
    }
  }
  if (compatibilityRecovery.action === 'not_recoverable' && compatibilityRecovery.signal) {
    auditCapture.addGatewayMetadata({
      label: 'codex_encrypted_content_recovery_skipped',
      metadata: {
        accountId: account.id,
        upstreamUrl: safeUpstreamUrl,
        transport: 'http',
        signal: compatibilityRecovery.signal,
        reason: compatibilityRecovery.reason
      }
    })
  }
  if (input.accountStateMutationEnabled !== false) {
    persistOpenAICodexHeadersIfNeeded(
      account,
      response.headers,
      usageContext.trafficSource === 'gateway' ? 'gateway_error' : usageContext.trafficSource
    )
  }

  const failureInput: AccountFailureInput = {
    success: false,
    statusCode: response.status,
    headers: response.headers,
    bodyText: responseBodyText,
    settings,
    trafficSource: usageContext.trafficSource,
    upstreamErrorSummary: failureBodyFacts.upstreamErrorSummary,
    upstreamErrorSummaryResolved: failureBodyFacts.upstreamErrorSummaryResolved
  }
  await forgetOpenAIAccountForSessionAsync(sessionAffinityKey, account.id)

  if (explicitPolicyDecision?.keyScoped && input.accountStateMutationEnabled !== false) {
    await recordGatewayAccountApiKeyFailure(account, {
      status: 'rate_limited',
      statusCode: response.status,
      errorCode: quotaRecoveryErrorCode(explicitPolicyDecision.quotaRecoveryMode ?? 'generic'),
      errorMessage: failureBodyFacts.upstreamErrorSummary,
      cooldownUntil: explicitPolicyDecision.cooldownUntil,
      quotaRecoveryMode: explicitPolicyDecision.quotaRecoveryMode ?? 'generic',
      trafficSource: usageContext.trafficSource,
      mutationContext: {
        authority: 'system_quota_policy',
        trafficSource: 'gateway'
      },
      traceId: usageContext.traceId,
      clientIp: usageContext.clientIp,
      apiKeyId: usageContext.apiKeyId,
      attemptStartedAt: new Date(attemptStartedAt).toISOString(),
      source: 'system_quota_policy'
    })
  }

  if (explicitPolicyDecision && input.accountStateMutationEnabled !== false) {
    auditCapture.addGatewayMetadata({
      label: 'account_error_policy_matched',
      metadata: {
        accountId: account.id,
        ruleId: explicitPolicyDecision.ruleId,
        ruleName: explicitPolicyDecision.ruleName,
        ruleSource: explicitPolicyDecision.ruleSource,
        action: explicitPolicyDecision.action,
        cooldownStatus: explicitPolicyDecision.cooldownStatus,
        keyScoped: explicitPolicyDecision.keyScoped,
        quotaRecoveryMode: explicitPolicyDecision.quotaRecoveryMode,
        quotaRecoveryHintSource: explicitPolicyDecision.quotaRecoveryHintSource
      }
    })
    if (explicitPolicyDecision.action !== 'retry_next') {
      await applyAccountErrorHandlingWithCacheInvalidation(account, {
        ...failureInput,
        policyDecision: explicitPolicyDecision
      })
    }
  }

  const automaticSameAccountKeyRotation = !explicitPolicyDecision
    && !input.deferAutomaticSameAccountKeyRotation
    && hasAlternativeAccountApiKeys(account)
  const sameAccountKeyRotation = hasAlternativeAccountApiKeys(account)
    && (explicitPolicyDecision?.action === 'retry_next' || explicitPolicyDecision?.keyScoped === true || automaticSameAccountKeyRotation)
  // A system quota decision has stronger semantic evidence than the deliberately
  // small fixed-model probe. Do not enqueue that probe, because a successful
  // low-context response must not compete with or clear the explicit error state.
  if (explicitPolicyDecision?.ruleSource !== 'system') {
    // A complete gateway HTTP failure is independent evidence that this account
    // needs the fixed-model availability confirmation. The request-level guard
    // also covers retry_next, whose candidate replay continues below.
    dispatchRequestFailureAccountHealthCheck(req, usageContext.trafficSource, account.id)
  }

  return {
    action: 'skip_account',
    failureKind: explicitPolicyDecision ? 'explicit_policy' : 'opaque_http',
    lastAttempt,
    // A state-changing policy makes the whole account unavailable, matching
    // ordinary temporary-unavailable/non-schedulable candidate filtering.
    // An explicit retry_next policy may continue with a sibling Key.  The
    // automatic path is deliberately disabled while the dispatcher owns a
    // bounded retry of the same physical credential.
    keyScopedFailure: sameAccountKeyRotation,
    // A completed failure alone remains neutral. It becomes Key-scoped shared
    // evidence only after a sibling Key of this same account succeeds.
    pendingApiKeyFailure: sameAccountKeyRotation
      && input.accountStateMutationEnabled !== false
      && explicitPolicyDecision?.keyScoped !== true
      && account.selectedApiKeyFingerprint
      && !account.apiKeyRuntimeStateDisabled
      ? {
          account,
          status: 'temporary_unavailable',
          mutationContext: {
            authority: 'confirmed_same_account_key_rotation',
            trafficSource: 'gateway'
          },
          observationEpoch: captureGatewayAccountApiKeyFailureObservation(account),
          statusCode: response.status,
          errorMessage: failureBodyFacts.upstreamErrorSummary
        }
      : undefined
  }
}

function parseFailureBodyFacts(
  bodyText: string,
  headers: Headers,
  account: UpstreamAccount
): ParsedFailureBodyFacts {
  const parsedJsonBody = parseGatewayNonStreamJsonBody(bodyText, headers)
  const errorPayload = parsedJsonBody.status === 'valid'
    ? parseGatewayProtocolErrorPayloadFromJsonValue(account, parsedJsonBody.value)
    : {}
  return {
    parsedJsonBody,
    errorPayload,
    upstreamErrorSummary: accountErrorPayloadSummary(errorPayload),
    upstreamErrorSummaryResolved: true
  }
}

export async function handleUpstreamRequestError(
  input: HandleUpstreamRequestErrorInput
): Promise<{ action: 'skip_account'; lastAttempt?: UpstreamAttempt; keyScopedFailure?: boolean }> {
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
    sessionAffinityKey,
    signal,
    error
  } = input

  if (signal?.aborted && gatewayRequestAbortSource(req)) {
    throw error
  }

  if (isUpstreamRequestAbortedError(error) || signal?.aborted) {
    let lastAttempt = input.lastAttempt
    await forgetOpenAIAccountForSessionAsync(sessionAffinityKey, account.id)
    if (shouldRecordAbortedUpstreamAttempt(error)) {
      const statusCode = lastAttempt?.accountId === account.id && lastAttempt.upstreamUrl === upstreamUrl
        ? lastAttempt.status
        : undefined
      await recordFailedUpstreamAttempt(req, usageContext, account, {
        upstreamUrl,
        startedAt: attemptStartedAt,
        statusCode,
        errorMessage: downstreamConnectionClosedMessage,
        failureAttribution: 'downstream_closed'
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
      errorPhase: 'downstream',
      errorMessage: downstreamConnectionClosedMessage
    })
    throw error
  }

  const message = formatUpstreamRequestErrorMessage(error)
  const safeUpstreamUrl = sanitizeUrlCredentialsForLog(upstreamUrl) ?? 'unknown'
  const transportErrorCode = sanitizeOptionalDiagnosticPayload(objectStringProperty(error, 'code'))
  const failureObservation = classifyGatewayUpstreamFailure({
    phase: 'upstream_request'
  })
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
    errorCode: transportErrorCode,
    errorMessage: message,
    ...failureObservation
  }, '网关请求上游失败')
  const lastAttempt: UpstreamAttempt = {
    ...(input.lastAttempt ?? {}),
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    upstreamUrl,
    message,
    errorCode: transportErrorCode,
    transportFailureKind: upstreamRequestFailureKind(error, input.lastAttempt)
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

  await forgetOpenAIAccountForSessionAsync(sessionAffinityKey, account.id)
  dispatchRequestFailureAccountHealthCheck(req, usageContext.trafficSource, account.id)
  scheduleGatewayClientSourceAvoidanceFailure({
    req,
    usageContext,
    account,
    errorCode: error instanceof Error ? error.name : undefined,
    message,
    observationId: `${auditAttemptId}:transport_failure`
  })
  const automaticApiKeyFailover = account.credentials?.api_key_strategy === 'failover'
  // The source-level failure was recorded above only when a validated source
  // key exists. The request callback still must not directly mutate proxy
  // health, local account suppression/precheck, client-IP avoidance, API-Key
  // state, or persistent account state; the shared health probe owns those
  // account-wide decisions, while the transport circuit consumes its own
  // independent transport evidence.
  return {
    action: 'skip_account',
    lastAttempt,
    keyScopedFailure: automaticApiKeyFailover && hasAlternativeAccountApiKeys(account)
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

function upstreamRequestFailureKind(
  error: unknown,
  previousAttempt: UpstreamAttempt | undefined
): UpstreamAttempt['transportFailureKind'] {
  const diagnostic = [
    error instanceof Error ? error.name : '',
    objectStringProperty(error, 'code') ?? '',
    error instanceof Error ? error.message : ''
  ].join(' ').toLowerCase()
  if (/timeout|timedout|timed out|etimedout|超时/.test(diagnostic)) return 'timeout'
  return previousAttempt?.status === undefined ? 'connection' : 'read_incomplete'
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

function scheduleGatewayClientSourceAvoidanceFailure(input: {
  req: Request
  usageContext: GatewayUsageContext
  account: UpstreamAccount
  errorCode?: string
  message?: string
  observationId: string
}): void {
  if (input.usageContext.trafficSource !== 'gateway') return
  const strategy = resolveOpenAIGatewayClientStrategy(input.req, {
    systemAccountId: input.usageContext.systemAccountId,
    apiKeyId: input.usageContext.apiKeyId,
    groupId: input.usageContext.groupId,
    endpoint: input.usageContext.endpoint,
    clientIp: input.usageContext.clientIp,
    providerCode: input.account.providerCode,
    providerProtocolProfileId: input.account.providerProtocolProfileId,
    protocolCode: input.account.protocolCode,
    protocolVersion: input.account.protocolVersion
  })
  if (!strategy.allowClientSourceAccountAvoidance) return
  void rememberGatewayClientSourceFailureAsync(strategy, input.account.id, {
    errorCode: input.errorCode,
    message: input.message,
    observationId: input.observationId
  }).then(async (failure) => {
    if (!failure?.activation) return
    await runGatewayClientSourceAvoidanceAvailabilityProbe({
      account: input.account,
      strategy,
      activation: failure.activation
    })
  }).catch((error) => {
    getRequestLogger().warn({
      event: 'gateway_client_source_avoidance_failure_schedule_failed',
      accountId: input.account.id,
      errorMessage: error instanceof Error ? error.message : String(error)
    }, '来源级失败避让未能投递探活，保留短期避让')
  })
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
