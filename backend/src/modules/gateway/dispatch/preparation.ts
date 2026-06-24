import type { Request, Response } from 'express'

import { logger } from '../../../shared/logger.js'
import type { GroupUsageAccessMetadata } from '../../../storage/repositories.js'
import {
  AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE,
  checkGatewayAuthorizationQuotaBatchAsync
} from '../quota/authorization-quota.service.js'
import type { AuditCaptureContext } from '../audit/capture.service.js'
import {
  filterLocallySuppressedGatewayAccounts,
  orderGatewayAccountsByRuntimeDegradation
} from '../runtime/account-side-effects.service.js'
import {
  orderOpenAIAccountsByClientIpAccountAvoidance
} from '../runtime/client-ip-account-avoidance.service.js'
import {
  acquireHighConcurrencyClientIpSlot,
  type ClientIpConcurrencyDecision
} from '../runtime/client-ip-concurrency.service.js'
import {
  orderOpenAIAccountsByCodexTurnAvoidance
} from '../client-profiles/codex-turn-retry.service.js'
import {
  areGatewayAccountsCapacityBusyForLane,
  refreshGatewayAccountCurrentConcurrency
} from './capacity.js'
import { sendGatewayFailureResponse, sendQuotaExceededResponse } from '../response/failure-response.js'
import { waitForHighConcurrencyGroupCapacity } from '../runtime/high-concurrency-queue.service.js'
import { resolveLocalSuppressionFilter } from '../runtime/local-suppression-preflight.js'
import {
  orderGatewayAccountsByUpstreamBucketHealth
} from '../runtime/proxy-health.service.js'
import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'
import { gatewayErrorPayload } from '../response/responses.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import {
  areOpenAIHighConcurrencyAccountsBusyForLane,
  orderOpenAIAccountsBySessionAffinity,
  type OpenAIAccountDispatchOrderingOptions
} from '../runtime/session-affinity.service.js'
import type { OpenAIGatewayClientStrategyContext } from '../client-profiles/strategy.js'
import type { GatewayFailureUsageContext } from '../usage/records.js'
import type { OpenAIGatewayDispatchContext } from '../request/preflight.js'
import type { GatewayAccountModelPriority } from './model-filter.js'

export interface DispatchPreparationFallbackResult {
  attempted: boolean
  context?: OpenAIGatewayDispatchContext
}

export type DispatchPreparationResult =
  | {
    outcome: 'ready'
    accounts: UpstreamAccount[]
    releaseClientIpConcurrency: () => void
    codexTurnAccountAvoidanceApplied?: boolean
    codexTurnAvoidedAccountIds?: string[]
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
  clientIp?: string
  clientStrategy: OpenAIGatewayClientStrategyContext
  requestLane: OpenAIGatewayRequestLane
  signal?: AbortSignal
  attemptFallback: (reason: string) => Promise<DispatchPreparationFallbackResult>
}): Promise<DispatchPreparationResult> {
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

  const orderedCandidateAccounts = orderOpenAIAccountsBySessionAffinity(
    input.candidateAccounts,
    input.sessionAffinityKey,
    dispatchOrderingOptions
  )
  const initialLocalSuppressionFilter = filterLocallySuppressedGatewayAccounts(orderedCandidateAccounts)
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
  }

  const localSuppressionFilter = await resolveLocalSuppressionFilter({
    req: input.req,
    res: input.res,
    auditCapture: input.auditCapture,
    usageContext: input.usageContext,
    startedAt: input.startedAt,
    accounts: orderedCandidateAccounts,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId,
    groupId: input.groupId,
    signal: input.signal
  })
  if (!localSuppressionFilter) {
    return { outcome: 'completed' }
  }

  const runtimeDegradationOrder = orderGatewayAccountsByRuntimeDegradation(localSuppressionFilter.accounts)
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

  const proxyHealthOrder = orderGatewayAccountsByUpstreamBucketHealth(runtimeDegradationOrder.accounts)
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

  const clientIpAccountAvoidance = orderOpenAIAccountsByClientIpAccountAvoidance(proxyHealthOrder.accounts, {
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId,
    groupId: input.groupId,
    clientIp: input.clientIp
  })
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

  const codexTurnAvoidance = orderOpenAIAccountsByCodexTurnAvoidance(clientIpAccountAvoidance.accounts, input.clientStrategy)
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

  const readyPreparation = await prepareQuotaAndCapacityReadyAccounts({
    ...input,
    accounts: codexTurnAvoidance.accounts,
    dispatchOrderingOptions
  })
  if (readyPreparation.outcome !== 'ready') {
    return readyPreparation
  }
  return {
    ...readyPreparation,
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
  if (input.dispatchOrderingOptions.groupType === 'high_concurrency') {
    accounts = refreshGatewayAccountCurrentConcurrency(accounts)
  }

  if (accounts.length === 0) {
    if (authorizationQuotaDeniedAccountCount > 0) {
      const fallback = await input.attemptFallback('authorization_quota_exceeded')
      if (fallback.attempted) {
        return { outcome: 'fallback', context: fallback.context }
      }
      sendQuotaExceededResponse(input.req, input.res, input.auditCapture, input.usageContext, input.startedAt, AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE)
      return { outcome: 'completed' }
    }
    const statusCode = 503
    const responsePayload = gatewayErrorPayload('没有可用的上游账户', 'service_unavailable')
    sendGatewayFailureResponse({
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
        errorMessage: '没有可用的上游账户'
      }
    })
    return { outcome: 'completed' }
  }

  const laneAwareDispatchOrderingOptions = {
    ...input.dispatchOrderingOptions,
    requestLane: input.requestLane
  }

  if (areOpenAIHighConcurrencyAccountsBusyForLane(accounts, laneAwareDispatchOrderingOptions)) {
    accounts = orderOpenAIAccountsBySessionAffinity(
      refreshGatewayAccountCurrentConcurrency(accounts),
      input.sessionAffinityKey,
      input.dispatchOrderingOptions
    )
  }

  if (areOpenAIHighConcurrencyAccountsBusyForLane(accounts, laneAwareDispatchOrderingOptions)) {
    const fallback = await input.attemptFallback('high_concurrency_group_busy')
    if (fallback.attempted) {
      return { outcome: 'fallback', context: fallback.context }
    }
  }

  if (input.dispatchOrderingOptions.groupType !== 'high_concurrency'
    && areGatewayAccountsCapacityBusyForLane(accounts, input.requestLane, input.groupAccess.schedulingPolicy)) {
    const fallback = await input.attemptFallback('group_capacity_busy')
    if (fallback.attempted) {
      return { outcome: 'fallback', context: fallback.context }
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
      sendGatewayFailureResponse({
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
        }
      })
      return { outcome: 'completed' }
    }
    releaseClientIpConcurrency = clientIpConcurrency.release
    if (input.signal?.aborted || input.res.writableEnded) {
      releaseClientIpConcurrency()
      return { outcome: 'completed' }
    }
  }

  if (areOpenAIHighConcurrencyAccountsBusyForLane(accounts, laneAwareDispatchOrderingOptions)) {
    const queueWait = await waitForHighConcurrencyGroupCapacity({
      systemAccountId: input.systemAccountId,
      groupId: input.groupId,
      apiKeyId: input.apiKeyId,
      accountIds: accounts.map((account) => account.id),
      accountConcurrencyLimits: Object.fromEntries(accounts.map((account) => [account.id, account.concurrencyLimit])),
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
    accounts = orderOpenAIAccountsBySessionAffinity(
      refreshGatewayAccountCurrentConcurrency(accounts),
      input.sessionAffinityKey,
      input.dispatchOrderingOptions
    )
  }

  if (areOpenAIHighConcurrencyAccountsBusyForLane(accounts, laneAwareDispatchOrderingOptions)) {
    const statusCode = 429
    const responsePayload = gatewayErrorPayload('分组繁忙，请稍后重试', 'rate_limit_exceeded')
    releaseClientIpConcurrency()
    sendGatewayFailureResponse({
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
      }
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

function clientIpConcurrencyFailureMessage(decision: ClientIpConcurrencyDecision): string {
  if (decision.enabled && !decision.acquired && decision.reason === 'timeout') {
    return '当前 IP 并发排队等待超时，请稍后重试'
  }
  return '当前 IP 并发已达到分组限制，请稍后重试'
}

function noop(): void {}
