import type { Request, Response } from 'express'

import { loadAccountCurrentConcurrencyByIds } from '../../shared/account-concurrency.js'
import { logger } from '../../shared/logger.js'
import { bindRequestContextFields } from '../../shared/request-context.js'
import { findActiveGatewayApiKeyById, type GatewayApiKeyRow, type GroupUsageAccessMetadata } from '../../storage/repositories.js'
import {
  listCachedOpenAIAccountsForGroupAsync,
  readCachedGatewaySettingsAsync,
  resolveCachedGroupUsageAccessMetadataAsync
} from './gateway-runtime-cache.service.js'
import { type GatewaySettings } from './account-error-policy.service.js'
import { API_KEY_QUOTA_EXCEEDED_MESSAGE, checkGatewayApiKeyQuotaAsync } from './api-key-quota.service.js'
import {
  AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE,
  checkGatewayAuthorizationQuotaAsync,
  checkGatewayAuthorizationQuotaBatchAsync
} from './authorization-quota.service.js'
import { type AuditCaptureContext } from './audit-capture.service.js'
import { filterLocallySuppressedGatewayAccounts } from './gateway-account-side-effects.service.js'
import { getGatewayRequestBodyState } from './openai-gateway-request-body.js'
import {
  orderOpenAIAccountsByCodexTurnAvoidance
} from './openai-gateway-codex-turn-retry.service.js'
import {
  openAIGatewayClientStrategyAuditMetadata,
  resolveOpenAIGatewayClientStrategy,
  type OpenAIGatewayClientStrategyContext
} from './openai-gateway-client-strategy.js'
import { acquireHighConcurrencyClientIpSlot, type ClientIpConcurrencyDecision } from './openai-gateway-client-ip-concurrency.service.js'
import { waitForHighConcurrencyGroupCapacity } from './openai-gateway-high-concurrency-queue.service.js'
import { finalizeGatewayAuthFailureAudit, sendOpenAIModelsGatewayResponse } from './openai-gateway-fixed-responses.js'
import { sendGatewayFailureResponse, sendQuotaExceededResponse } from './openai-gateway-failure-response.js'
import { filterGatewayAccountsByRequestedModel, gatewayModelFilterFailureMessage } from './openai-gateway-model-filter.js'
import { gatewayErrorPayload } from './openai-gateway-responses.js'
import { resolveGatewayRuntimeAsync } from './openai-gateway-request.js'
import { isOpenAIModelsRequest, type UpstreamAccount } from './openai-gateway-route-helpers.js'
import {
  areOpenAIHighConcurrencyAccountsHardBusy,
  orderOpenAIAccountsBySessionAffinity,
  resolveOpenAIGatewaySessionAffinityKey
} from './openai-gateway-session-affinity.service.js'
import { requestModel, type UsageRequestSnapshot } from './openai-gateway-usage.js'
import {
  groupUsageMetadata,
  type GatewayFailureUsageContext
} from './openai-gateway-usage-records.js'

export interface OpenAIGatewayRequestIdentity {
  systemAccountId: string
  groupId: string
  apiKeyId?: string
}

interface OpenAIGatewayRequestPreflightOptions {
  identity?: OpenAIGatewayRequestIdentity
  candidateAccounts?: UpstreamAccount[]
  disableSessionAffinity?: boolean
  settingsOverride?: Partial<GatewaySettings>
}

interface PrepareOpenAIGatewayDispatchContextInput {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  options: OpenAIGatewayRequestPreflightOptions
  startedAt: number
  traceId: string
  clientIp?: string
  endpoint: string
  requestSnapshot: UsageRequestSnapshot
  signal?: AbortSignal
}

export interface OpenAIGatewayDispatchContext {
  activeGatewaySettings: GatewaySettings
  usageContext: GatewayFailureUsageContext
  accounts: UpstreamAccount[]
  sessionAffinityKey?: string
  clientStrategy: OpenAIGatewayClientStrategyContext
  releaseClientIpConcurrency: () => void
}

export async function prepareOpenAIGatewayDispatchContext(
  input: PrepareOpenAIGatewayDispatchContextInput
): Promise<OpenAIGatewayDispatchContext | undefined> {
  const { req, res, auditCapture, options, startedAt, traceId, clientIp, endpoint, requestSnapshot, signal } = input
  let gatewaySettings: GatewaySettings | undefined
  let apiKeyRecord: GatewayApiKeyRow | undefined
  let runtimeGroupAccess: GroupUsageAccessMetadata | undefined
  let runtimeAccounts: UpstreamAccount[] | undefined

  const identity = options.identity ?? await (async () => {
    const runtime = await resolveGatewayRuntimeAsync(req, res)
    if (!runtime?.apiKey) {
      finalizeGatewayAuthFailureAudit(req, res, auditCapture)
      return undefined
    }
    gatewaySettings = runtime.settings
    apiKeyRecord = runtime.apiKey
    runtimeGroupAccess = runtime.groupAccess
    runtimeAccounts = runtime.accounts
    return {
      systemAccountId: runtime.apiKey.system_account_id,
      apiKeyId: runtime.apiKey.id,
      groupId: runtime.apiKey.group_id
    }
  })()
  if (!identity) {
    return undefined
  }

  const activeGatewaySettings = mergeGatewaySettings(
    gatewaySettings ?? await readCachedGatewaySettingsAsync(),
    options.settingsOverride
  )
  const { systemAccountId, apiKeyId, groupId } = identity
  apiKeyRecord = apiKeyRecord ?? (apiKeyId ? findActiveGatewayApiKeyById(apiKeyId) : undefined)
  const apiKeyUnavailable = Boolean(apiKeyId && !apiKeyRecord)
  auditCapture.bindContext({
    systemAccountId,
    apiKeyId,
    groupId,
    providerCode: 'openai'
  })
  bindRequestContextFields({
    systemAccountId,
    apiKeyId,
    groupId
  })

  const groupAccess = runtimeGroupAccess ?? await resolveCachedGroupUsageAccessMetadataAsync(groupId, systemAccountId)
  const baseUsageContext = buildGatewayUsageContext({
    traceId,
    clientIp,
    identity,
    endpoint,
    requestSnapshot
  })
  const clientStrategy = resolveOpenAIGatewayClientStrategy(req, {
    systemAccountId,
    apiKeyId,
    groupId,
    endpoint
  })
  if (clientStrategy.clientProfile === 'codex') {
    auditCapture.addGatewayMetadata({
      label: 'client_strategy',
      metadata: openAIGatewayClientStrategyAuditMetadata(clientStrategy)
    })
  }
  if (!groupAccess) {
    const statusCode = 403
    const responsePayload = gatewayErrorPayload('API Key 绑定的分组授权不可用', 'forbidden')
    sendGatewayFailureResponse({
      req,
      res,
      auditCapture,
      usageContext: baseUsageContext,
      startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'authorization',
        errorCode: 'forbidden',
        errorMessage: 'API Key 绑定的分组授权不可用'
      }
    })
    return undefined
  }

  if (apiKeyUnavailable) {
    const statusCode = 401
    const responsePayload = gatewayErrorPayload('API Key 不可用或已过期', 'invalid_api_key')
    sendGatewayFailureResponse({
      req,
      res,
      auditCapture,
      usageContext: baseUsageContext,
      startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'authorization',
        errorCode: 'invalid_api_key',
        errorMessage: responsePayload.error.message
      }
    })
    return undefined
  }

  const groupUsageFields = groupUsageMetadata(groupAccess)
  const usageContext = buildGatewayUsageContext({
    traceId,
    clientIp,
    identity,
    groupUsageFields,
    endpoint,
    requestSnapshot
  })

  const bodyState = getGatewayRequestBodyState(req)
  if (bodyState?.jsonParseStatus === 'invalid_json') {
    const statusCode = 400
    const responsePayload = gatewayErrorPayload('请求体不是合法 JSON', 'invalid_request_error')
    sendGatewayFailureResponse({
      req,
      res,
      auditCapture,
      usageContext,
      startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'request_validation',
        errorCode: 'invalid_json',
        errorMessage: responsePayload.error.message
      }
    })
    return undefined
  }

  const quotaDecision = apiKeyRecord ? await checkGatewayApiKeyQuotaAsync(apiKeyRecord) : { allowed: true }
  if (!quotaDecision.allowed) {
    const statusCode = 429
    const responsePayload = gatewayErrorPayload(quotaDecision.message ?? API_KEY_QUOTA_EXCEEDED_MESSAGE, 'rate_limit_exceeded')
    sendGatewayFailureResponse({
      req,
      res,
      auditCapture,
      usageContext,
      startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'quota',
        errorCode: 'rate_limit_exceeded',
        errorMessage: responsePayload.error.message
      }
    })
    return undefined
  }

  const groupAuthorizationQuotaDecision = await checkGatewayAuthorizationQuotaAsync({ groupAccess })
  if (!groupAuthorizationQuotaDecision.allowed) {
    sendQuotaExceededResponse(
      req,
      res,
      auditCapture,
      usageContext,
      startedAt,
      groupAuthorizationQuotaDecision.message ?? AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE
    )
    return undefined
  }

  if (isOpenAIModelsRequest(req)) {
    sendOpenAIModelsGatewayResponse({
      res,
      auditCapture,
      usageContext,
      startedAt
    })
    return undefined
  }

  const rawSessionAffinityKey = resolveOpenAIGatewaySessionAffinityKey(req, {
    systemAccountId,
    apiKeyId,
    groupId
  })
  const sessionAffinityKey = options.disableSessionAffinity ? undefined : rawSessionAffinityKey
  const rawCandidateAccounts = options.candidateAccounts ?? runtimeAccounts ?? await listCachedOpenAIAccountsForGroupAsync(groupId, systemAccountId)
  const dispatchOrderingOptions = {
    groupType: groupAccess.groupType,
    schedulingPolicy: groupAccess.schedulingPolicy
  }
  let releaseClientIpConcurrency = noop
  const modelFilter = filterGatewayAccountsByRequestedModel(rawCandidateAccounts, requestModel(req))
  if (modelFilter.skippedCount > 0) {
    auditCapture.addGatewayMetadata({
      label: 'account_model_filter',
      metadata: {
        requestedModel: modelFilter.requestedModel,
        skippedCount: modelFilter.skippedCount,
        limitedAccountCount: modelFilter.limitedAccountCount,
        remainingCount: modelFilter.accounts.length,
        reason: modelFilter.reason
      }
    })
  }
  if (rawCandidateAccounts.length > 0 && modelFilter.accounts.length === 0) {
    const statusCode = 400
    const message = gatewayModelFilterFailureMessage(modelFilter)
    const responsePayload = gatewayErrorPayload(message, 'invalid_request_error')
    sendGatewayFailureResponse({
      req,
      res,
      auditCapture,
      usageContext,
      startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'request_validation',
        errorCode: modelFilter.reason ?? 'unsupported_model',
        errorMessage: message
      }
    })
    return undefined
  }
  const orderedCandidateAccounts = orderOpenAIAccountsBySessionAffinity(
    modelFilter.accounts,
    sessionAffinityKey,
    dispatchOrderingOptions
  )
  const localSuppressionFilter = filterLocallySuppressedGatewayAccounts(orderedCandidateAccounts)
  if (localSuppressionFilter.suppressedCount > 0) {
    logger.warn({
      event: 'gateway_local_account_suppression_applied',
      suppressedCount: localSuppressionFilter.suppressedCount,
      bypassedAllSuppressed: localSuppressionFilter.bypassedAllSuppressed,
      groupId,
      systemAccountId
    }, '网关本地短期屏蔽账号已应用到候选列表')
  }

  const codexTurnAvoidance = orderOpenAIAccountsByCodexTurnAvoidance(localSuppressionFilter.accounts, clientStrategy)
  if (codexTurnAvoidance.applied || codexTurnAvoidance.bypassedAllAvoided) {
    logger.warn({
      event: 'gateway_codex_turn_account_avoidance',
      applied: codexTurnAvoidance.applied,
      failureCount: codexTurnAvoidance.failureCount,
      avoidedAccountIds: codexTurnAvoidance.avoidedAccountIds,
      bypassedAllAvoided: codexTurnAvoidance.bypassedAllAvoided,
      groupId,
      systemAccountId
    }, codexTurnAvoidance.applied
      ? 'Codex turn 级失败账号避让已应用到候选列表'
      : 'Codex turn 级失败账号避让无可用备选，保持原候选列表')
    auditCapture.addGatewayMetadata({
      label: 'codex_turn_account_avoidance',
      metadata: {
        applied: codexTurnAvoidance.applied,
        failureCount: codexTurnAvoidance.failureCount,
        avoidedAccountIds: codexTurnAvoidance.avoidedAccountIds,
        bypassedAllAvoided: codexTurnAvoidance.bypassedAllAvoided
      }
    })
  }

  const candidateAccounts = codexTurnAvoidance.accounts
  let authorizationQuotaDeniedAccountCount = 0
  let accounts: UpstreamAccount[] = []
  const accountQuotaDecisions = await checkGatewayAuthorizationQuotaBatchAsync({ groupAccess, accounts: candidateAccounts })
  for (const account of candidateAccounts) {
    const decision = accountQuotaDecisions.get(account.id) ?? { allowed: true }
    if (!decision.allowed) {
      authorizationQuotaDeniedAccountCount += 1
      continue
    }
    accounts.push(account)
  }
  if (dispatchOrderingOptions.groupType === 'high_concurrency') {
    accounts = refreshGatewayAccountCurrentConcurrency(accounts)
  }

  if (accounts.length === 0) {
    if (authorizationQuotaDeniedAccountCount > 0) {
      sendQuotaExceededResponse(req, res, auditCapture, usageContext, startedAt, AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE)
      return undefined
    }
    const statusCode = 503
    const responsePayload = gatewayErrorPayload('没有可用的上游账户', 'service_unavailable')
    sendGatewayFailureResponse({
      req,
      res,
      auditCapture,
      usageContext,
      startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'dispatch',
        errorCode: 'service_unavailable',
        errorMessage: '没有可用的上游账户'
      }
    })
    return undefined
  }

  if (dispatchOrderingOptions.groupType === 'high_concurrency') {
    const clientIpConcurrency = await acquireHighConcurrencyClientIpSlot({
      systemAccountId,
      groupId,
      apiKeyId,
      clientIp,
      policy: groupAccess.schedulingPolicy,
      signal
    })
    if (clientIpConcurrency.enabled) {
      auditCapture.addGatewayMetadata({
        label: 'high_concurrency_client_ip',
        metadata: clientIpConcurrencyAuditMetadata(clientIpConcurrency)
      })
    }
    if (!clientIpConcurrency.acquired) {
      if (signal?.aborted || res.writableEnded) {
        return undefined
      }
      const statusCode = 429
      const responsePayload = gatewayErrorPayload(clientIpConcurrencyFailureMessage(clientIpConcurrency), 'rate_limit_exceeded')
      sendGatewayFailureResponse({
        req,
        res,
        auditCapture,
        usageContext,
        startedAt,
        statusCode,
        responsePayload,
        audit: {
          outcome: 'gateway_failed',
          errorPhase: 'dispatch',
          errorCode: 'rate_limit_exceeded',
          errorMessage: responsePayload.error.message
        }
      })
      return undefined
    }
    releaseClientIpConcurrency = clientIpConcurrency.release
    if (signal?.aborted || res.writableEnded) {
      releaseClientIpConcurrency()
      return undefined
    }
  }

  if (areOpenAIHighConcurrencyAccountsHardBusy(accounts, dispatchOrderingOptions)) {
    accounts = orderOpenAIAccountsBySessionAffinity(
      refreshGatewayAccountCurrentConcurrency(accounts),
      sessionAffinityKey,
      dispatchOrderingOptions
    )
  }

  if (areOpenAIHighConcurrencyAccountsHardBusy(accounts, dispatchOrderingOptions)) {
    const queueWait = await waitForHighConcurrencyGroupCapacity({
      systemAccountId,
      groupId,
      apiKeyId,
      accountIds: accounts.map((account) => account.id),
      policy: groupAccess.schedulingPolicy,
      signal
    })
    auditCapture.addGatewayMetadata({
      label: 'high_concurrency_group_queue',
      metadata: queueWait
    })
    if (signal?.aborted || res.writableEnded) {
      releaseClientIpConcurrency()
      return undefined
    }
    accounts = orderOpenAIAccountsBySessionAffinity(
      refreshGatewayAccountCurrentConcurrency(accounts),
      sessionAffinityKey,
      dispatchOrderingOptions
    )
  }

  if (areOpenAIHighConcurrencyAccountsHardBusy(accounts, dispatchOrderingOptions)) {
    const statusCode = 429
    const responsePayload = gatewayErrorPayload('分组繁忙，请稍后重试', 'rate_limit_exceeded')
    releaseClientIpConcurrency()
    sendGatewayFailureResponse({
      req,
      res,
      auditCapture,
      usageContext,
      startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'dispatch',
        errorCode: 'rate_limit_exceeded',
        errorMessage: responsePayload.error.message
      }
    })
    return undefined
  }

  return {
    activeGatewaySettings,
    usageContext,
    accounts,
    sessionAffinityKey,
    clientStrategy,
    releaseClientIpConcurrency
  }
}

function refreshGatewayAccountCurrentConcurrency(accounts: UpstreamAccount[]): UpstreamAccount[] {
  const concurrency = loadAccountCurrentConcurrencyByIds(accounts.map((account) => account.id))
  return accounts.map((account) => ({
    ...account,
    currentConcurrency: concurrency.get(account.id) ?? 0
  }))
}

function mergeGatewaySettings(base: GatewaySettings, override?: Partial<GatewaySettings>): GatewaySettings {
  if (!override) return base
  return {
    defaultTemporaryUnschedulableMinutes: override.defaultTemporaryUnschedulableMinutes ?? base.defaultTemporaryUnschedulableMinutes,
    temporaryUnschedulableRetryIntervalSeconds: override.temporaryUnschedulableRetryIntervalSeconds ?? base.temporaryUnschedulableRetryIntervalSeconds,
    temporaryUnschedulableRetryAttempts: override.temporaryUnschedulableRetryAttempts ?? base.temporaryUnschedulableRetryAttempts,
    streamCircuitBreakerEnabled: override.streamCircuitBreakerEnabled ?? base.streamCircuitBreakerEnabled,
    streamRequestTimeoutSeconds: override.streamRequestTimeoutSeconds ?? base.streamRequestTimeoutSeconds,
    streamIdleTimeoutSeconds: override.streamIdleTimeoutSeconds ?? base.streamIdleTimeoutSeconds,
    streamFailureThresholdCount: override.streamFailureThresholdCount ?? base.streamFailureThresholdCount,
    streamFailureThresholdWindowMinutes: override.streamFailureThresholdWindowMinutes ?? base.streamFailureThresholdWindowMinutes
  }
}

function clientIpConcurrencyAuditMetadata(decision: ClientIpConcurrencyDecision): Record<string, unknown> {
  if (!decision.enabled) {
    return { enabled: false }
  }
  if (decision.acquired) {
    return {
      enabled: true,
      acquired: true,
      current: decision.current,
      limit: decision.limit,
      waitedMs: decision.waitedMs,
      queued: decision.queued,
      queueSizeBeforeAcquire: decision.queueSizeBeforeAcquire
    }
  }
  return {
    enabled: true,
    acquired: false,
    reason: decision.reason,
    current: decision.current,
    limit: decision.limit,
    waitedMs: decision.waitedMs,
    queueSize: decision.queueSize
  }
}

function clientIpConcurrencyFailureMessage(decision: ClientIpConcurrencyDecision): string {
  if (decision.enabled && !decision.acquired && decision.reason === 'timeout') {
    return '当前 IP 并发排队等待超时，请稍后重试'
  }
  return '当前 IP 并发已达到分组限制，请稍后重试'
}

function buildGatewayUsageContext(input: {
  traceId: string
  clientIp?: string
  identity: OpenAIGatewayRequestIdentity
  groupUsageFields?: ReturnType<typeof groupUsageMetadata>
  endpoint: string
  requestSnapshot: UsageRequestSnapshot
}): GatewayFailureUsageContext {
  const { traceId, clientIp, identity, groupUsageFields, endpoint, requestSnapshot } = input
  return {
    traceId,
    clientIp,
    systemAccountId: identity.systemAccountId,
    apiKeyId: identity.apiKeyId,
    groupId: identity.groupId,
    ...groupUsageFields,
    endpoint,
    requestSnapshot
  }
}

function noop(): void {}
