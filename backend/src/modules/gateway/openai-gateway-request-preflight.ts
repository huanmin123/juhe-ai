import type { Request, Response } from 'express'

import { logger } from '../../shared/logger.js'
import { bindRequestContextFields } from '../../shared/request-context.js'
import type { GatewayApiKeyRow, GroupUsageAccessMetadata } from '../../storage/repositories.js'
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
import { finalizeGatewayAuthFailureAudit, sendOpenAIModelsGatewayResponse } from './openai-gateway-fixed-responses.js'
import { sendGatewayFailureResponse, sendQuotaExceededResponse } from './openai-gateway-failure-response.js'
import { gatewayErrorPayload } from './openai-gateway-responses.js'
import { resolveGatewayRuntimeAsync } from './openai-gateway-request.js'
import { isOpenAIModelsRequest, type UpstreamAccount } from './openai-gateway-route-helpers.js'
import {
  orderOpenAIAccountsBySessionAffinity,
  resolveOpenAIGatewaySessionAffinityKey
} from './openai-gateway-session-affinity.service.js'
import { type UsageRequestSnapshot } from './openai-gateway-usage.js'
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
}

export interface OpenAIGatewayDispatchContext {
  activeGatewaySettings: GatewaySettings
  usageContext: GatewayFailureUsageContext
  accounts: UpstreamAccount[]
  sessionAffinityKey?: string
  clientStrategy: OpenAIGatewayClientStrategyContext
}

export async function prepareOpenAIGatewayDispatchContext(
  input: PrepareOpenAIGatewayDispatchContextInput
): Promise<OpenAIGatewayDispatchContext | undefined> {
  const { req, res, auditCapture, options, startedAt, traceId, clientIp, endpoint, requestSnapshot } = input
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

  const activeGatewaySettings = gatewaySettings ?? await readCachedGatewaySettingsAsync()
  const { systemAccountId, apiKeyId, groupId } = identity
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
  const orderedCandidateAccounts = orderOpenAIAccountsBySessionAffinity(
    options.candidateAccounts ?? runtimeAccounts ?? await listCachedOpenAIAccountsForGroupAsync(groupId, systemAccountId),
    sessionAffinityKey
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
  const accounts: UpstreamAccount[] = []
  const accountQuotaDecisions = await checkGatewayAuthorizationQuotaBatchAsync({ groupAccess, accounts: candidateAccounts })
  for (const account of candidateAccounts) {
    const decision = accountQuotaDecisions.get(account.id) ?? { allowed: true }
    if (!decision.allowed) {
      authorizationQuotaDeniedAccountCount += 1
      continue
    }
    accounts.push(account)
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

  return {
    activeGatewaySettings,
    usageContext,
    accounts,
    sessionAffinityKey,
    clientStrategy
  }
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
