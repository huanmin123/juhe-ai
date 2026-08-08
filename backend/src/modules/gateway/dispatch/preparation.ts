import type { Request, Response } from 'express'

import { logger } from '../../../shared/logger.js'
import { logRequestStage } from '../../../shared/request-context.js'
import type { GroupUsageAccessMetadata } from '../../../storage/repositories.js'
import {
  AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE,
  checkGatewayAuthorizationQuotaBatchAsync
} from '../quota/authorization-quota.service.js'
import type { AuditCaptureContext } from '../audit/capture.service.js'
import {
  filterGatewayAccountRuntimeSuppressionsAsync,
  orderGatewayAccountsByRuntimeDegradation,
  type LocalAccountSuppressionFilterResult
} from '../runtime/account-side-effects.service.js'
import {
  orderOpenAIAccountsByClientIpAccountAvoidanceAsync
} from '../runtime/client-ip-account-avoidance.service.js'
import {
  acquireHighConcurrencyClientIpSlot,
  type ClientIpConcurrencyDecision
} from '../runtime/client-ip-concurrency.service.js'
import {
  orderOpenAIAccountsByClientSourceAvoidanceAsync
} from '../client-profiles/client-source-avoidance.service.js'
import {
  areGatewayAccountsCapacityBusyForLaneAsync,
  orderGatewayAccountsByLaneCapacityAvailabilityAsync,
  refreshGatewayAccountCurrentConcurrencyAsync
} from './capacity.js'
import { waitForHighConcurrencyGroupCapacity } from '../runtime/high-concurrency-queue.service.js'
import { resolveLocalSuppressionFilter } from '../runtime/local-suppression-preflight.js'
import {
  orderGatewayAccountsByUpstreamBucketHealthAsync
} from '../runtime/proxy-health.service.js'
import {
  normalRouteLatencyDegradationScope,
  orderGatewayAccountsByNormalRouteLatencyDegradationAsync
} from '../runtime/normal-route-latency-degradation.service.js'
import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import {
  areOpenAIHighConcurrencyAccountsBusyForLane,
  areOpenAIHighConcurrencyAccountsBusyForLaneAsync,
  claimOpenAIAccountForSessionAsync,
  orderOpenAIAccountsBySessionAffinity,
  orderOpenAIAccountsBySessionAffinityAsync,
  type OpenAIAccountDispatchOrderingOptions
} from '../runtime/session-affinity.service.js'
import type { OpenAIGatewayClientStrategyContext } from '../client-profiles/strategy.js'
import type { GatewayFailureUsageContext } from '../usage/records.js'
import { isAccountProbeTrafficSource } from '../usage/traffic-source.js'
import type { OpenAIGatewayDispatchContext } from '../request/preflight.js'
import {
  gatewayAccountConcurrencyAccountIds,
  gatewayAccountConcurrencyLimitsByAccountId
} from './account-concurrency-identity.js'
import type { GatewayAccountModelPriority } from './model-filter.js'
import type { NormalRouteSpeedFirstRuntimeConfig } from '../runtime/normal-route-latency-degradation.service.js'
import type { ServerRetryBudget } from '../runtime/server-retry-budget.js'
import { requestModel } from '../request/metadata.js'
import type {
  GatewayRequestWallBudget,
  RouteCoordinationBudget,
  GatewayRouteCoordinatorOwner
} from '../routing/route-coordination.js'
import {
  orderGatewayAccountsByHotQualityAsync,
  type GatewayHotQualityExplorationReservation
} from '../runtime/hot-quality-runtime.service.js'

export interface DispatchPreparationFallbackResult {
  attempted: boolean
  context?: OpenAIGatewayDispatchContext
}

export type DispatchPreparationResult =
  | {
    outcome: 'ready'
    accounts: UpstreamAccount[]
    releaseClientIpConcurrency: () => void
    normalRouteLatencyDegradationApplied?: boolean
    codexTurnAccountAvoidanceApplied?: boolean
    codexTurnAvoidedAccountIds?: string[]
    precheckHalfOpenEligible?: boolean
    hotQualityExplorationReservation?: GatewayHotQualityExplorationReservation
    settleHotQualityExplorationAfterDispatch?: (outcome: 'dispatched' | 'not_dispatched') => Promise<void>
  }
  | { outcome: 'fallback'; reason: string; context?: OpenAIGatewayDispatchContext }
  | { outcome: 'completed' }

export async function prepareOpenAIGatewayDispatchAccounts(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  candidateAccounts: UpstreamAccount[]
  modelPriority: GatewayAccountModelPriority
  sessionAffinityKey?: string
  groupAccess: GroupUsageAccessMetadata
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  routeStrategyId?: string
  normalRouteSpeedFirstConfig?: NormalRouteSpeedFirstRuntimeConfig
  clientIp?: string
  clientStrategy: OpenAIGatewayClientStrategyContext
  requestLane: OpenAIGatewayRequestLane
  serverRetryBudget: ServerRetryBudget
  routeCoordinationBudget: RouteCoordinationBudget
  gatewayRequestWallBudget: GatewayRequestWallBudget
  signal?: AbortSignal
  ignoreAccountRuntimeSuppression?: boolean
  routeCoordinator: GatewayRouteCoordinatorOwner<OpenAIGatewayDispatchContext>
}): Promise<DispatchPreparationResult> {
  const sessionAffinityStartedAt = performance.now()
  const dispatchOrderingOptions = {
    groupType: input.groupAccess.groupType,
    schedulingPolicy: input.groupAccess.schedulingPolicy,
    modelPriority: input.modelPriority,
    trafficMigrationScope: {
      systemAccountId: input.systemAccountId,
      apiKeyId: input.apiKeyId,
      groupId: input.groupId
    }
  }

  const orderedCandidateAccounts = await orderOpenAIAccountsBySessionAffinityAsync(
    input.candidateAccounts,
    input.sessionAffinityKey,
    dispatchOrderingOptions
  )
  logRequestStage('account.session_affinity', {
    traceId: input.usageContext.traceId,
    groupId: input.groupId,
    candidateAccountCount: orderedCandidateAccounts.length,
    applied: Boolean(input.sessionAffinityKey)
  }, 'success', sessionAffinityStartedAt)
  const suppressionStartedAt = performance.now()
  const bypassLocalSuppression = input.ignoreAccountRuntimeSuppression === true || isAccountProbeTrafficSource(input.usageContext.trafficSource)
  const initialLocalSuppressionFilter = bypassLocalSuppression
    ? localSuppressionBypassResult(orderedCandidateAccounts)
    : await filterGatewayAccountRuntimeSuppressionsAsync(orderedCandidateAccounts)
  logRequestStage('account.runtime_suppression', {
    traceId: input.usageContext.traceId,
    groupId: input.groupId,
    candidateAccountCount: initialLocalSuppressionFilter.accounts.length,
    suppressedCount: initialLocalSuppressionFilter.suppressedCount,
    bypassed: bypassLocalSuppression
  }, 'success', suppressionStartedAt)
  let precheckHalfOpenEligible = false
  if (initialLocalSuppressionFilter.allSuppressed) {
    const fallback = await requestRouteFallback(input, 'local_account_suppressed')
    if (fallback.attempted) {
      logger.warn({
        event: 'gateway_local_account_suppression_fallback',
        suppressedCount: initialLocalSuppressionFilter.suppressedCount,
        suppressedAccountIds: initialLocalSuppressionFilter.suppressedAccountIds,
        nextRetryAfterMs: initialLocalSuppressionFilter.nextRetryAfterMs,
        groupId: input.groupId,
        systemAccountId: input.systemAccountId,
        apiKeyId: input.apiKeyId
      }, '当前号池候选账号均处于本地短期屏蔽，已在派发前尝试后备号池')
      input.auditCapture.addGatewayMetadata({
        label: 'local_account_suppression',
        metadata: {
          suppressedCount: initialLocalSuppressionFilter.suppressedCount,
          suppressedAccountIds: initialLocalSuppressionFilter.suppressedAccountIds,
          allSuppressed: true,
          nextRetryAfterMs: initialLocalSuppressionFilter.nextRetryAfterMs,
          fallbackAttempted: true
        }
      })
      return { outcome: 'fallback', reason: 'local_account_suppressed', context: fallback.context }
    }
    precheckHalfOpenEligible = initialLocalSuppressionFilter.precheckSuppressedAccountIds?.length === orderedCandidateAccounts.length
      && (initialLocalSuppressionFilter.configuredPolicySuppressedAccountIds?.length ?? 0) === 0
  }

  const localSuppressionFilter = bypassLocalSuppression
    ? localSuppressionBypassResult(orderedCandidateAccounts)
    : precheckHalfOpenEligible
      ? localSuppressionBypassResult(orderedCandidateAccounts)
      : await resolveLocalSuppressionFilter({
        req: input.req,
        res: input.res,
        auditCapture: input.auditCapture,
        usageContext: input.usageContext,
        startedAt: input.startedAt,
        accounts: orderedCandidateAccounts,
        systemAccountId: input.systemAccountId,
        apiKeyId: input.apiKeyId,
        groupId: input.groupId,
        serverRetryBudget: input.serverRetryBudget,
        routeCoordinationBudget: input.routeCoordinationBudget,
        gatewayRequestWallBudget: input.gatewayRequestWallBudget,
        routeCoordinator: input.routeCoordinator,
        signal: input.signal
      })
  if (!localSuppressionFilter) {
    return { outcome: 'completed' }
  }

  const runtimeDegradationOrder = orderGatewayAccountsByRuntimeDegradation(localSuppressionFilter.accounts, {
    modelRankByAccountId: input.modelPriority.rankByAccountId
  })
  if (runtimeDegradationOrder.applied || runtimeDegradationOrder.bypassedAllDegraded) {
    logger.warn({
      event: runtimeDegradationOrder.applied
        ? 'gateway_runtime_degradation_order_applied'
        : 'gateway_runtime_degradation_fallback_only',
      applied: runtimeDegradationOrder.applied,
      degradedCount: runtimeDegradationOrder.degradedCount,
      degradedAccountIds: runtimeDegradationOrder.degradedAccountIds,
      bypassedAllDegraded: runtimeDegradationOrder.bypassedAllDegraded,
      groupId: input.groupId,
      systemAccountId: input.systemAccountId,
      apiKeyId: input.apiKeyId
    }, runtimeDegradationOrder.applied
      ? '账号运行态降级已应用到候选排序，降级账号仅作为兜底候选'
      : '当前号池候选账号均为运行态降级，准备尝试后备号池')
    input.auditCapture.addGatewayMetadata({
      label: 'runtime_account_degradation',
      metadata: {
        applied: runtimeDegradationOrder.applied,
        degradedCount: runtimeDegradationOrder.degradedCount,
        degradedAccountIds: runtimeDegradationOrder.degradedAccountIds,
        bypassedAllDegraded: runtimeDegradationOrder.bypassedAllDegraded
      }
    })
  }

  if (runtimeDegradationOrder.bypassedAllDegraded) {
    const fallback = await requestRouteFallback(input, 'runtime_degraded')
    if (fallback.attempted) {
      return { outcome: 'fallback', reason: 'runtime_degraded', context: fallback.context }
    }
  }

  const latencyDegradationStartedAt = performance.now()
  const latencyDegradationOrder = await orderGatewayAccountsByNormalRouteLatencyDegradationAsync(
    runtimeDegradationOrder.accounts,
    normalRouteLatencyDegradationScope({
      systemAccountId: input.systemAccountId,
      routeStrategyId: input.routeStrategyId,
      groupId: input.groupId
    }),
    input.normalRouteSpeedFirstConfig,
    input.modelPriority
  )
  logRequestStage('account.latency_degradation', {
    traceId: input.usageContext.traceId,
    groupId: input.groupId,
    candidateAccountCount: latencyDegradationOrder.accounts.length,
    applied: latencyDegradationOrder.applied,
    bypassedAllDegraded: latencyDegradationOrder.bypassedAllDegraded
  }, 'success', latencyDegradationStartedAt)
  if (latencyDegradationOrder.applied || latencyDegradationOrder.bypassedAllDegraded) {
    logger.warn({
      event: latencyDegradationOrder.applied
        ? 'gateway_normal_route_latency_degradation_order_applied'
        : 'gateway_normal_route_latency_degradation_bypassed',
      applied: latencyDegradationOrder.applied,
      degradedAccountIds: latencyDegradationOrder.degradedAccountIds,
      bypassedAllDegraded: latencyDegradationOrder.bypassedAllDegraded,
      groupId: input.groupId,
      routeStrategyId: input.routeStrategyId,
      systemAccountId: input.systemAccountId,
      apiKeyId: input.apiKeyId
    }, latencyDegradationOrder.applied
      ? '普通路由速度优先已将首字慢速账号排到候选末尾'
      : '普通路由速度优先无未降级候选，保持原候选顺序')
    input.auditCapture.addGatewayMetadata({
      label: 'normal_route_latency_degradation',
      metadata: {
        applied: latencyDegradationOrder.applied,
        degradedAccountIds: latencyDegradationOrder.degradedAccountIds,
        bypassedAllDegraded: latencyDegradationOrder.bypassedAllDegraded
      }
    })
  }

  const proxyHealthStartedAt = performance.now()
  const proxyHealthOrder = await orderGatewayAccountsByUpstreamBucketHealthAsync(latencyDegradationOrder.accounts, input.modelPriority)
  logRequestStage('account.proxy_health', {
    traceId: input.usageContext.traceId,
    groupId: input.groupId,
    candidateAccountCount: proxyHealthOrder.accounts.length,
    applied: proxyHealthOrder.applied,
    bypassedAllAvoided: proxyHealthOrder.bypassedAllAvoided
  }, 'success', proxyHealthStartedAt)
  if (proxyHealthOrder.applied || proxyHealthOrder.bypassedAllAvoided) {
    logger.warn({
      event: proxyHealthOrder.applied
        ? 'gateway_upstream_bucket_health_avoidance_applied'
        : 'gateway_upstream_bucket_health_avoidance_bypassed',
      applied: proxyHealthOrder.applied,
      avoidedBucketKeys: proxyHealthOrder.avoidedBucketKeys,
      avoidedProxyKeys: proxyHealthOrder.avoidedProxyKeys,
      avoidedAccountIds: proxyHealthOrder.avoidedAccountIds,
      halfOpenBucketKeys: proxyHealthOrder.halfOpenBucketKeys,
      halfOpenAccountIds: proxyHealthOrder.halfOpenAccountIds,
      bypassedAllAvoided: proxyHealthOrder.bypassedAllAvoided,
      groupId: input.groupId,
      systemAccountId: input.systemAccountId,
      apiKeyId: input.apiKeyId
    }, proxyHealthOrder.applied
      ? '上游桶运行态避让已应用到候选列表'
      : '上游桶运行态避让无可用备选，保持原候选列表')
    input.auditCapture.addGatewayMetadata({
      label: 'upstream_bucket_health_avoidance',
      metadata: {
        applied: proxyHealthOrder.applied,
        avoidedBucketKeys: proxyHealthOrder.avoidedBucketKeys,
        avoidedProxyKeys: proxyHealthOrder.avoidedProxyKeys,
        avoidedAccountIds: proxyHealthOrder.avoidedAccountIds,
        halfOpenBucketKeys: proxyHealthOrder.halfOpenBucketKeys,
        halfOpenAccountIds: proxyHealthOrder.halfOpenAccountIds,
        bypassedAllAvoided: proxyHealthOrder.bypassedAllAvoided
      }
    })
  }

  const clientIpAvoidanceStartedAt = performance.now()
  const clientIpAccountAvoidance = await orderOpenAIAccountsByClientIpAccountAvoidanceAsync(proxyHealthOrder.accounts, {
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId,
    groupId: input.groupId,
    clientIp: input.clientIp
  }, input.modelPriority)
  logRequestStage('account.client_ip_avoidance', {
    traceId: input.usageContext.traceId,
    groupId: input.groupId,
    candidateAccountCount: clientIpAccountAvoidance.accounts.length,
    applied: clientIpAccountAvoidance.applied,
    avoidedAccountCount: clientIpAccountAvoidance.avoidedAccountIds.length,
    bypassedAllAvoided: clientIpAccountAvoidance.bypassedAllAvoided,
    clientIpPresent: Boolean(input.clientIp)
  }, 'success', clientIpAvoidanceStartedAt)
  if (clientIpAccountAvoidance.applied || clientIpAccountAvoidance.bypassedAllAvoided) {
    logger.warn({
      event: clientIpAccountAvoidance.applied
        ? 'gateway_client_ip_account_avoidance_applied'
        : 'gateway_client_ip_account_avoidance_bypassed',
      applied: clientIpAccountAvoidance.applied,
      avoidedAccountIds: clientIpAccountAvoidance.avoidedAccountIds,
      bypassedAllAvoided: clientIpAccountAvoidance.bypassedAllAvoided,
      groupId: input.groupId,
      systemAccountId: input.systemAccountId,
      apiKeyId: input.apiKeyId,
      clientIp: input.clientIp
    }, clientIpAccountAvoidance.applied
      ? '客户端 IP 级账号回避已应用到候选列表'
      : '客户端 IP 级账号回避无可用备选，保持原候选列表')
    input.auditCapture.addGatewayMetadata({
      label: 'client_ip_account_avoidance',
      metadata: {
        applied: clientIpAccountAvoidance.applied,
        avoidedAccountIds: clientIpAccountAvoidance.avoidedAccountIds,
        bypassedAllAvoided: clientIpAccountAvoidance.bypassedAllAvoided
      }
    })
  }

  const clientSourceAvoidanceStartedAt = performance.now()
  const clientSourceAvoidance = await orderOpenAIAccountsByClientSourceAvoidanceAsync(
    clientIpAccountAvoidance.accounts,
    input.clientStrategy,
    input.modelPriority
  )
  logRequestStage('account.client_source_avoidance', {
    traceId: input.usageContext.traceId,
    groupId: input.groupId,
    candidateAccountCount: clientSourceAvoidance.accounts.length,
    applied: clientSourceAvoidance.applied,
    failureCount: clientSourceAvoidance.failureCount,
    avoidedAccountCount: clientSourceAvoidance.avoidedAccountIds.length,
    bypassedAllAvoided: clientSourceAvoidance.bypassedAllAvoided
  }, 'success', clientSourceAvoidanceStartedAt)
  if (clientSourceAvoidance.applied || clientSourceAvoidance.bypassedAllAvoided) {
    logger.warn({
      event: 'gateway_client_source_account_avoidance',
      applied: clientSourceAvoidance.applied,
      failureCount: clientSourceAvoidance.failureCount,
      avoidedAccountIds: clientSourceAvoidance.avoidedAccountIds,
      bypassedAllAvoided: clientSourceAvoidance.bypassedAllAvoided,
      groupId: input.groupId,
      systemAccountId: input.systemAccountId
    }, clientSourceAvoidance.applied
      ? '客户端来源级失败账号避让已应用到候选列表'
      : '客户端来源级失败账号避让无可用备选，保持原候选列表')
    input.auditCapture.addGatewayMetadata({
      label: 'client_source_account_avoidance',
      metadata: {
        applied: clientSourceAvoidance.applied,
        failureCount: clientSourceAvoidance.failureCount,
        avoidedAccountIds: clientSourceAvoidance.avoidedAccountIds,
        bypassedAllAvoided: clientSourceAvoidance.bypassedAllAvoided
      }
    })
  }

  const candidatePreparationStartedAt = performance.now()
  const readyPreparation = await prepareQuotaAndCapacityReadyAccounts({
    ...input,
    accounts: clientSourceAvoidance.accounts,
    dispatchOrderingOptions,
    latencyDegradedAccountIds: new Set(latencyDegradationOrder.degradedAccountIds),
    hotQualityMode: input.normalRouteSpeedFirstConfig ? 'speed_first' : 'cost_first',
    eligibleFirstPrimaryDispatch: input.usageContext.trafficSource === 'gateway'
  })
  logRequestStage('account.dispatch_candidates', {
    traceId: input.usageContext.traceId,
    groupId: input.groupId,
    preparationOutcome: readyPreparation.outcome,
    candidateAccountCount: readyPreparation.outcome === 'ready' ? readyPreparation.accounts.length : 0,
    ...(readyPreparation.outcome === 'ready' ? {} : {
      failureReason: `account_dispatch_${readyPreparation.outcome}`,
      decisionInputs: { groupId: input.groupId, candidateAccountCount: clientSourceAvoidance.accounts.length }
    })
  }, readyPreparation.outcome === 'ready' ? 'success' : 'expected_failure', candidatePreparationStartedAt)
  if (readyPreparation.outcome !== 'ready') {
    return readyPreparation
  }
  let readyAccounts = readyPreparation.accounts
  try {
    const proposedAccountId = readyAccounts[0]?.id
    const claimedAccountId = proposedAccountId
      ? await claimOpenAIAccountForSessionAsync(input.sessionAffinityKey, proposedAccountId, {
          systemAccountId: input.systemAccountId,
          apiKeyId: input.apiKeyId,
          groupId: input.groupId
        })
      : undefined
    if (claimedAccountId && claimedAccountId !== proposedAccountId && readyAccounts.some(account => account.id === claimedAccountId)) {
      readyAccounts = await orderOpenAIAccountsBySessionAffinityAsync(
        readyAccounts,
        input.sessionAffinityKey,
        dispatchOrderingOptions
      )
    }
    if (input.sessionAffinityKey && proposedAccountId) {
      input.auditCapture.addGatewayMetadata({
        label: 'session_affinity_claim',
        metadata: {
          proposedAccountId,
          claimedAccountId,
          winnerAvailable: claimedAccountId ? readyAccounts.some(account => account.id === claimedAccountId) : false,
          applied: readyAccounts[0]?.id === claimedAccountId
        }
      })
    }
  } catch (error) {
    readyPreparation.releaseClientIpConcurrency()
    await readyPreparation.settleHotQualityExplorationAfterDispatch?.('not_dispatched')
    throw error
  }
  return {
    ...readyPreparation,
    accounts: readyAccounts,
    precheckHalfOpenEligible,
    normalRouteLatencyDegradationApplied: latencyDegradationOrder.applied,
    codexTurnAccountAvoidanceApplied: clientSourceAvoidance.thresholdReached,
    codexTurnAvoidedAccountIds: clientSourceAvoidance.avoidedAccountIds,
    hotQualityExplorationReservation: readyPreparation.hotQualityExplorationReservation,
    settleHotQualityExplorationAfterDispatch: readyPreparation.settleHotQualityExplorationAfterDispatch
  }
}

async function prepareQuotaAndCapacityReadyAccounts(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  accounts: UpstreamAccount[]
  sessionAffinityKey?: string
  groupAccess: GroupUsageAccessMetadata
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  routeStrategyId?: string
  clientIp?: string
  requestLane: OpenAIGatewayRequestLane
  serverRetryBudget: ServerRetryBudget
  gatewayRequestWallBudget: GatewayRequestWallBudget
  signal?: AbortSignal
  dispatchOrderingOptions: OpenAIAccountDispatchOrderingOptions
  routeCoordinator: GatewayRouteCoordinatorOwner<OpenAIGatewayDispatchContext>
  latencyDegradedAccountIds: ReadonlySet<string>
  hotQualityMode: 'cost_first' | 'speed_first'
  eligibleFirstPrimaryDispatch: boolean
}): Promise<DispatchPreparationResult> {
  const quotaStartedAt = performance.now()
  let authorizationQuotaDeniedAccountCount = 0
  let accounts: UpstreamAccount[] = []
  let hotQualityExplorationReservation: GatewayHotQualityExplorationReservation | undefined
  let settleHotQualityExplorationAfterDispatch: ((outcome: 'dispatched' | 'not_dispatched') => Promise<void>) | undefined
  const accountQuotaDecisions = await checkGatewayAuthorizationQuotaBatchAsync({ groupAccess: input.groupAccess, accounts: input.accounts })
  for (const account of input.accounts) {
    const decision = accountQuotaDecisions.get(account.id) ?? { allowed: true }
    if (!decision.allowed) {
      authorizationQuotaDeniedAccountCount += 1
      continue
    }
    accounts.push(account)
  }
  logRequestStage('quota.batch_decision', {
    traceId: input.usageContext.traceId,
    groupId: input.groupId,
    candidateAccountCount: input.accounts.length,
    allowedAccountCount: accounts.length,
    deniedAccountCount: authorizationQuotaDeniedAccountCount
  }, authorizationQuotaDeniedAccountCount > 0 ? 'expected_failure' : 'success', quotaStartedAt)
  const capacityStartedAt = performance.now()
  if (input.dispatchOrderingOptions.groupType === 'high_concurrency') {
    accounts = await refreshGatewayAccountCurrentConcurrencyAsync(accounts)
  }
  logRequestStage('capacity.account_snapshot', {
    traceId: input.usageContext.traceId,
    groupId: input.groupId,
    candidateAccountCount: accounts.length,
    groupType: input.dispatchOrderingOptions.groupType
  }, 'success', capacityStartedAt)

  if (accounts.length === 0) {
    if (authorizationQuotaDeniedAccountCount > 0) {
      const fallback = await requestRouteFallback(input, 'authorization_quota_exceeded')
      if (fallback.attempted) {
        return { outcome: 'fallback', reason: 'authorization_quota_exceeded', context: fallback.context }
      }
      await input.routeCoordinator.completeFailure({
        statusCode: 429,
        message: AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE,
        errorType: 'rate_limit_exceeded',
        errorCode: 'rate_limit_exceeded',
        errorPhase: 'quota'
      })
      return { outcome: 'completed' }
    }
    await input.routeCoordinator.completeFailure({
      statusCode: 503,
      message: '没有可用的上游账户',
      errorType: 'service_unavailable',
      errorCode: 'no_available_upstream_account',
      errorPhase: 'dispatch'
    })
    return { outcome: 'completed' }
  }

  const laneAwareDispatchOrderingOptions = {
    ...input.dispatchOrderingOptions,
    requestLane: input.requestLane
  }

  if (await areOpenAIHighConcurrencyAccountsBusyForLaneAsync(accounts, laneAwareDispatchOrderingOptions)) {
    accounts = await orderOpenAIAccountsBySessionAffinityAsync(
      await refreshGatewayAccountCurrentConcurrencyAsync(accounts),
      input.sessionAffinityKey,
      input.dispatchOrderingOptions
    )
  }

  const applyHotQualityOrder = async (): Promise<void> => {
    const hotQualityOrder = await orderGatewayAccountsByHotQualityAsync({
      accounts,
      modelPriority: input.dispatchOrderingOptions.modelPriority ?? { sourceEndpointFamily: 'chat_completions', rankByAccountId: new Map() },
      mode: input.hotQualityMode,
      systemAccountId: input.systemAccountId,
      routeStrategyId: input.routeStrategyId,
      groupId: input.groupId,
      requestLane: input.requestLane,
      model: requestModel(input.req),
      requestId: input.usageContext.traceId,
      latencyDegradedAccountIds: input.latencyDegradedAccountIds,
      eligibleFirstPrimaryDispatch: input.eligibleFirstPrimaryDispatch
    })
    accounts = hotQualityOrder.accounts
    if (input.hotQualityMode === 'speed_first' && input.latencyDegradedAccountIds.size > 0) {
      accounts = [
        ...accounts.filter((account) => !input.latencyDegradedAccountIds.has(account.id)),
        ...accounts.filter((account) => input.latencyDegradedAccountIds.has(account.id))
      ]
    }
    hotQualityExplorationReservation = hotQualityOrder.explorationReservation
    settleHotQualityExplorationAfterDispatch = hotQualityOrder.settleExplorationAfterDispatch
    if (hotQualityOrder.dispatchIntent === 'same_tier_exploration' || hotQualityOrder.qualityReorderedTierKeys.length > 0) {
      input.auditCapture.addGatewayMetadata({
        label: 'hot_quality_candidate_selection',
        metadata: {
          dispatchIntent: hotQualityOrder.dispatchIntent,
          selectedAccountId: hotQualityOrder.selectedAccountId,
          explorationStatus: hotQualityOrder.explorationStatus,
          qualityReorderedTierKeys: hotQualityOrder.qualityReorderedTierKeys,
          latencyDegradedOverrideApplied: hotQualityOrder.latencyDegradedOverrideApplied
        }
      })
    }
  }

  if (input.dispatchOrderingOptions.groupType === 'high_concurrency') {
    await applyHotQualityOrder()
  }

  if (await areOpenAIHighConcurrencyAccountsBusyForLaneAsync(accounts, laneAwareDispatchOrderingOptions)) {
    const fallback = await requestRouteFallback(input, 'high_concurrency_group_busy')
    if (fallback.attempted) {
      return { outcome: 'fallback', reason: 'high_concurrency_group_busy', context: fallback.context }
    }
  }

  if (input.dispatchOrderingOptions.groupType !== 'high_concurrency') {
    if (await areGatewayAccountsCapacityBusyForLaneAsync(
      accounts,
      input.requestLane,
      input.groupAccess.schedulingPolicy
    )) {
      const fallback = await requestRouteFallback(input, 'group_capacity_busy')
      if (fallback.attempted) {
        return { outcome: 'fallback', reason: 'group_capacity_busy', context: fallback.context }
      }
    }
    accounts = await orderGatewayAccountsByLaneCapacityAvailabilityAsync(
      accounts,
      input.requestLane,
      input.groupAccess.schedulingPolicy,
      input.dispatchOrderingOptions.modelPriority
    )
    await applyHotQualityOrder()
    // Keep busy candidates in the dispatch context. The upstream dispatcher
    // owns bounded capacity waiting and can acquire whichever account becomes
    // free; treating this snapshot as a route fallback would discard the group
    // before its own queue has a chance to observe the release.
  }

  let releaseClientIpConcurrency = noop
  let clientIpConcurrencyReleased = false
  const releaseClientIpConcurrencyOnce = (): void => {
    if (clientIpConcurrencyReleased) return
    clientIpConcurrencyReleased = true
    releaseClientIpConcurrency()
  }
  try {
    if (input.dispatchOrderingOptions.groupType === 'high_concurrency') {
      const clientIpConcurrencyStartedAt = performance.now()
      const clientIpConcurrency = await acquireHighConcurrencyClientIpSlot({
        systemAccountId: input.systemAccountId,
        groupId: input.groupId,
        apiKeyId: input.apiKeyId,
        clientIp: input.clientIp,
        policy: input.groupAccess.schedulingPolicy,
        signal: input.signal
      })
      if (clientIpConcurrency.acquired) {
        releaseClientIpConcurrency = clientIpConcurrency.release
      }
      logRequestStage('capacity.client_ip_concurrency', {
        traceId: input.usageContext.traceId,
        groupId: input.groupId,
        enabled: clientIpConcurrency.enabled,
        acquired: clientIpConcurrency.acquired,
        ...(clientIpConcurrency.enabled ? {
          current: clientIpConcurrency.current,
          limit: clientIpConcurrency.limit
        } : {}),
        ...(!clientIpConcurrency.acquired ? {
          failureReason: `client_ip_concurrency_${clientIpConcurrency.reason}`,
          decisionInputs: clientIpConcurrencyAuditMetadata(clientIpConcurrency)
        } : {})
      }, input.signal?.aborted
        ? 'aborted'
        : clientIpConcurrency.acquired
          ? 'success'
          : 'expected_failure', clientIpConcurrencyStartedAt)
      if (clientIpConcurrency.enabled) {
        input.auditCapture.addGatewayMetadata({
          label: 'high_concurrency_client_ip',
          metadata: clientIpConcurrencyAuditMetadata(clientIpConcurrency)
        })
      }
      if (!clientIpConcurrency.acquired) {
        if (input.signal?.aborted || input.res.writableEnded) {
          return { outcome: 'completed' }
        }
        await input.routeCoordinator.completeFailure({
          statusCode: 429,
          message: clientIpConcurrencyFailureMessage(clientIpConcurrency),
          errorType: 'rate_limit_exceeded',
          errorCode: 'rate_limit_exceeded',
          errorPhase: 'dispatch',
          failureAttribution: 'gateway_capacity'
        })
        return { outcome: 'completed' }
      }
      if (input.signal?.aborted || input.res.writableEnded) {
        releaseClientIpConcurrencyOnce()
        return { outcome: 'completed' }
      }
    } else {
      logRequestStage('capacity.client_ip_concurrency', {
        traceId: input.usageContext.traceId,
        groupId: input.groupId,
        groupType: input.dispatchOrderingOptions.groupType
      }, 'skipped')
    }

    if (await areOpenAIHighConcurrencyAccountsBusyForLaneAsync(accounts, laneAwareDispatchOrderingOptions)) {
      const queueWaitStartedAtMs = Date.now()
      input.serverRetryBudget.beginNoAvailableWait(queueWaitStartedAtMs)
      let queueWait: Awaited<ReturnType<typeof waitForHighConcurrencyGroupCapacity>>
      try {
        const serverRetryRemainingMs = input.serverRetryBudget.remainingMs(queueWaitStartedAtMs)
        const maxWaitMs = input.requestLane === 'image'
          ? serverRetryRemainingMs
          : Math.min(
              serverRetryRemainingMs,
              input.gatewayRequestWallBudget.availableDecisionMs({ nowMs: queueWaitStartedAtMs })
            )
        queueWait = await waitForHighConcurrencyGroupCapacity({
          systemAccountId: input.systemAccountId,
          groupId: input.groupId,
          apiKeyId: input.apiKeyId,
          accountIds: gatewayAccountConcurrencyAccountIds(accounts),
          accountConcurrencyLimits: gatewayAccountConcurrencyLimitsByAccountId(accounts),
          lane: input.requestLane,
          policy: input.groupAccess.schedulingPolicy,
          maxWaitMs,
          signal: input.signal
        })
      } finally {
        input.serverRetryBudget.pauseNoAvailableWait()
      }
      input.auditCapture.addGatewayMetadata({
        label: 'high_concurrency_group_queue',
        metadata: {
          ...queueWait,
          lane: input.requestLane
        }
      })
      if (input.signal?.aborted || input.res.writableEnded) {
        releaseClientIpConcurrencyOnce()
        return { outcome: 'completed' }
      }
      accounts = await orderOpenAIAccountsBySessionAffinityAsync(
        await refreshGatewayAccountCurrentConcurrencyAsync(accounts),
        input.sessionAffinityKey,
        input.dispatchOrderingOptions
      )
    }

    if (await areOpenAIHighConcurrencyAccountsBusyForLaneAsync(accounts, laneAwareDispatchOrderingOptions)) {
      const fallback = await requestRouteFallback(input, 'high_concurrency_group_busy')
      if (fallback.attempted) {
        releaseClientIpConcurrencyOnce()
        return { outcome: 'fallback', reason: 'high_concurrency_group_busy', context: fallback.context }
      }
      releaseClientIpConcurrencyOnce()
      await input.routeCoordinator.completeFailure({
        statusCode: 429,
        message: '分组繁忙，请稍后重试',
        errorType: 'rate_limit_exceeded',
        errorCode: 'rate_limit_exceeded',
        errorPhase: 'dispatch',
        failureAttribution: 'gateway_capacity'
      })
      return { outcome: 'completed' }
    }

      return {
        outcome: 'ready',
        accounts,
      releaseClientIpConcurrency: releaseClientIpConcurrencyOnce,
      hotQualityExplorationReservation,
      settleHotQualityExplorationAfterDispatch
      }
  } catch (error) {
    releaseClientIpConcurrencyOnce()
    throw error
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

function localSuppressionBypassResult(accounts: UpstreamAccount[]): LocalAccountSuppressionFilterResult<UpstreamAccount> {
  return {
    accounts,
    suppressedCount: 0,
    allSuppressed: false,
    suppressedAccountIds: [],
    acquiredHalfOpenLeases: []
  }
}

function clientIpConcurrencyFailureMessage(decision: ClientIpConcurrencyDecision): string {
  if (decision.enabled && !decision.acquired && decision.reason === 'timeout') {
    return '当前 IP 并发排队等待超时，请稍后重试'
  }
  return '当前 IP 并发已达到分组限制，请稍后重试'
}

function noop(): void {}

async function requestRouteFallback(
  input: {
    routeCoordinator: GatewayRouteCoordinatorOwner<OpenAIGatewayDispatchContext>
  },
  reason: string
): Promise<DispatchPreparationFallbackResult> {
  return input.routeCoordinator.requestFallback(reason)
}
