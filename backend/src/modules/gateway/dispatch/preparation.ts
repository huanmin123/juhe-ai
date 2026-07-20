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
  orderOpenAIAccountsByCodexTurnAvoidanceAsync
} from '../client-profiles/codex-turn-retry.service.js'
import {
  areGatewayAccountsCapacityBusyForLaneAsync,
  orderGatewayAccountsByLaneCapacityAvailabilityAsync,
  refreshGatewayAccountCurrentConcurrencyAsync
} from './capacity.js'
import { sendGatewayFailureResponse, sendQuotaExceededResponse } from '../response/failure-response.js'
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
import { gatewayErrorPayload } from '../response/responses.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import {
  areOpenAIHighConcurrencyAccountsBusyForLane,
  areOpenAIHighConcurrencyAccountsBusyForLaneAsync,
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
import type { RouteStrategySpeedFirstConfig } from '../../../domain/types.js'
import type { ServerRetryBudget } from '../runtime/server-retry-budget.js'

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
  }
  | { outcome: 'fallback'; context?: OpenAIGatewayDispatchContext }
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
  normalRouteSpeedFirstConfig?: RouteStrategySpeedFirstConfig
  clientIp?: string
  clientStrategy: OpenAIGatewayClientStrategyContext
  requestLane: OpenAIGatewayRequestLane
  serverRetryBudget: ServerRetryBudget
  signal?: AbortSignal
  ignoreAccountRuntimeSuppression?: boolean
  attemptFallback: (reason: string) => Promise<DispatchPreparationFallbackResult>
}): Promise<DispatchPreparationResult> {
  const sessionAffinityStartedAt = Date.now()
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
  const suppressionStartedAt = Date.now()
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
    const fallback = await input.attemptFallback('local_account_suppressed')
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
      return { outcome: 'fallback', context: fallback.context }
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
    const fallback = await input.attemptFallback('runtime_degraded')
    if (fallback.attempted) {
      return { outcome: 'fallback', context: fallback.context }
    }
  }

  const latencyDegradationStartedAt = Date.now()
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

  const proxyHealthStartedAt = Date.now()
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

  const clientIpAccountAvoidance = await orderOpenAIAccountsByClientIpAccountAvoidanceAsync(proxyHealthOrder.accounts, {
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId,
    groupId: input.groupId,
    clientIp: input.clientIp
  }, input.modelPriority)
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

  const codexTurnAvoidance = await orderOpenAIAccountsByCodexTurnAvoidanceAsync(
    clientIpAccountAvoidance.accounts,
    input.clientStrategy,
    input.modelPriority
  )
  if (codexTurnAvoidance.applied || codexTurnAvoidance.bypassedAllAvoided) {
    logger.warn({
      event: 'gateway_codex_turn_account_avoidance',
      applied: codexTurnAvoidance.applied,
      failureCount: codexTurnAvoidance.failureCount,
      avoidedAccountIds: codexTurnAvoidance.avoidedAccountIds,
      bypassedAllAvoided: codexTurnAvoidance.bypassedAllAvoided,
      groupId: input.groupId,
      systemAccountId: input.systemAccountId
    }, codexTurnAvoidance.applied
      ? 'Codex turn 级失败账号避让已应用到候选列表'
      : 'Codex turn 级失败账号避让无可用备选，保持原候选列表')
    input.auditCapture.addGatewayMetadata({
      label: 'codex_turn_account_avoidance',
      metadata: {
        applied: codexTurnAvoidance.applied,
        failureCount: codexTurnAvoidance.failureCount,
        avoidedAccountIds: codexTurnAvoidance.avoidedAccountIds,
        bypassedAllAvoided: codexTurnAvoidance.bypassedAllAvoided
      }
    })
  }

  const candidatePreparationStartedAt = Date.now()
  const readyPreparation = await prepareQuotaAndCapacityReadyAccounts({
    ...input,
    accounts: codexTurnAvoidance.accounts,
    dispatchOrderingOptions
  })
  logRequestStage('account.dispatch_candidates', {
    traceId: input.usageContext.traceId,
    groupId: input.groupId,
    outcome: readyPreparation.outcome,
    candidateAccountCount: readyPreparation.outcome === 'ready' ? readyPreparation.accounts.length : 0
  }, readyPreparation.outcome === 'ready' ? 'success' : 'expected_failure', candidatePreparationStartedAt)
  if (readyPreparation.outcome !== 'ready') {
    return readyPreparation
  }
  return {
    ...readyPreparation,
    precheckHalfOpenEligible,
    normalRouteLatencyDegradationApplied: latencyDegradationOrder.applied,
    codexTurnAccountAvoidanceApplied: codexTurnAvoidance.thresholdReached,
    codexTurnAvoidedAccountIds: codexTurnAvoidance.avoidedAccountIds
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
  clientIp?: string
  requestLane: OpenAIGatewayRequestLane
  signal?: AbortSignal
  dispatchOrderingOptions: OpenAIAccountDispatchOrderingOptions
  attemptFallback: (reason: string) => Promise<DispatchPreparationFallbackResult>
}): Promise<DispatchPreparationResult> {
  const quotaStartedAt = Date.now()
  let authorizationQuotaDeniedAccountCount = 0
  let accounts: UpstreamAccount[] = []
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
  const capacityStartedAt = Date.now()
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
      const fallback = await input.attemptFallback('authorization_quota_exceeded')
      if (fallback.attempted) {
        return { outcome: 'fallback', context: fallback.context }
      }
      await sendQuotaExceededResponse(input.req, input.res, input.auditCapture, input.usageContext, input.startedAt, AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE)
      return { outcome: 'completed' }
    }
    const statusCode = 503
    const responsePayload = gatewayErrorPayload('上游暂时不可用，请重试', 'service_unavailable', 'upstream_retryable_error')
    await sendGatewayFailureResponse({
      req: input.req,
      res: input.res,
      auditCapture: input.auditCapture,
      usageContext: input.usageContext,
      startedAt: input.startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'dispatch',
        errorCode: 'service_unavailable',
        errorMessage: '上游暂时不可用，请重试'
      }
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

  if (await areOpenAIHighConcurrencyAccountsBusyForLaneAsync(accounts, laneAwareDispatchOrderingOptions)) {
    const fallback = await input.attemptFallback('high_concurrency_group_busy')
    if (fallback.attempted) {
      return { outcome: 'fallback', context: fallback.context }
    }
  }

  if (input.dispatchOrderingOptions.groupType !== 'high_concurrency') {
    accounts = await orderGatewayAccountsByLaneCapacityAvailabilityAsync(
      accounts,
      input.requestLane,
      input.groupAccess.schedulingPolicy,
      input.dispatchOrderingOptions.modelPriority
    )
    if (await areGatewayAccountsCapacityBusyForLaneAsync(accounts, input.requestLane, input.groupAccess.schedulingPolicy)) {
      const fallback = await input.attemptFallback('group_capacity_busy')
      if (fallback.attempted) {
        return { outcome: 'fallback', context: fallback.context }
      }
    }
  }

  let releaseClientIpConcurrency = noop
  if (input.dispatchOrderingOptions.groupType === 'high_concurrency') {
    const clientIpConcurrency = await acquireHighConcurrencyClientIpSlot({
      systemAccountId: input.systemAccountId,
      groupId: input.groupId,
      apiKeyId: input.apiKeyId,
      clientIp: input.clientIp,
      policy: input.groupAccess.schedulingPolicy,
      signal: input.signal
    })
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
      const statusCode = 429
      const responsePayload = gatewayErrorPayload(clientIpConcurrencyFailureMessage(clientIpConcurrency), 'rate_limit_exceeded')
      await sendGatewayFailureResponse({
        req: input.req,
        res: input.res,
        auditCapture: input.auditCapture,
        usageContext: input.usageContext,
        startedAt: input.startedAt,
        statusCode,
        responsePayload,
        audit: {
          outcome: 'gateway_failed',
          errorPhase: 'dispatch',
          errorCode: 'rate_limit_exceeded',
          errorMessage: responsePayload.error.message
        },
        failureAttribution: 'gateway_capacity'
      })
      return { outcome: 'completed' }
    }
    releaseClientIpConcurrency = clientIpConcurrency.release
    if (input.signal?.aborted || input.res.writableEnded) {
      releaseClientIpConcurrency()
      return { outcome: 'completed' }
    }
  }

  if (await areOpenAIHighConcurrencyAccountsBusyForLaneAsync(accounts, laneAwareDispatchOrderingOptions)) {
    const queueWait = await waitForHighConcurrencyGroupCapacity({
      systemAccountId: input.systemAccountId,
      groupId: input.groupId,
      apiKeyId: input.apiKeyId,
      accountIds: gatewayAccountConcurrencyAccountIds(accounts),
      accountConcurrencyLimits: gatewayAccountConcurrencyLimitsByAccountId(accounts),
      lane: input.requestLane,
      policy: input.groupAccess.schedulingPolicy,
      signal: input.signal
    })
    input.auditCapture.addGatewayMetadata({
      label: 'high_concurrency_group_queue',
      metadata: {
        ...queueWait,
        lane: input.requestLane
      }
    })
    if (input.signal?.aborted || input.res.writableEnded) {
      releaseClientIpConcurrency()
      return { outcome: 'completed' }
    }
    accounts = await orderOpenAIAccountsBySessionAffinityAsync(
      await refreshGatewayAccountCurrentConcurrencyAsync(accounts),
      input.sessionAffinityKey,
      input.dispatchOrderingOptions
    )
  }

  if (await areOpenAIHighConcurrencyAccountsBusyForLaneAsync(accounts, laneAwareDispatchOrderingOptions)) {
    const fallback = await input.attemptFallback('high_concurrency_group_busy')
    if (fallback.attempted) {
      releaseClientIpConcurrency()
      return { outcome: 'fallback', context: fallback.context }
    }
    const statusCode = 429
    const responsePayload = gatewayErrorPayload('分组繁忙，请稍后重试', 'rate_limit_exceeded')
    releaseClientIpConcurrency()
    await sendGatewayFailureResponse({
      req: input.req,
      res: input.res,
      auditCapture: input.auditCapture,
      usageContext: input.usageContext,
      startedAt: input.startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'dispatch',
        errorCode: 'rate_limit_exceeded',
        errorMessage: responsePayload.error.message
      },
      failureAttribution: 'gateway_capacity'
    })
    return { outcome: 'completed' }
  }

  return {
    outcome: 'ready',
    accounts,
    releaseClientIpConcurrency
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
