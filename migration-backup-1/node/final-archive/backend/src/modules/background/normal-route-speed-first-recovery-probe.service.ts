import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'
import { rfc3339InstantMilliseconds } from '../../shared/rfc3339.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { testOpenAIAccount } from '../accounts/account-test.service.js'
import {
  transportProbeMeetsFirstByteTarget,
  transportProbeOutcomeFromAccountTestResult,
  type TransportProbeOutcome
} from '../accounts/automatic-account-probe-outcome.js'
import type { OpenAIAccountSecret } from '../../storage/repositories.js'
import type { UpstreamAttempt } from '../gateway/upstream/attempt.js'
import {
  acquireNormalRouteLatencyProbeClaimAsync,
  deferNormalRouteLatencyProbeCandidateAsync,
  discardNormalRouteLatencyProbeCandidateAsync,
  normalRouteLatencyProbeClaimRenewIntervalMs,
  recordNormalRouteRecoveryProbeSuccessAsync,
  recordNormalRouteProbeFailureAsync,
  releaseNormalRouteLatencyProbeClaimAsync,
  renewNormalRouteLatencyProbeClaimAsync,
  type NormalRouteLatencyProbeCandidate
} from '../gateway/runtime/normal-route-latency-degradation.service.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'
import { backgroundProbeDbServiceTimeoutMs, globalSharedQueueConcurrency, runWithBackgroundFullDiagnosticSlot } from './account-probe-limits.js'

interface NormalRouteSpeedFirstRecoveryProbeQueueItem extends NormalRouteLatencyProbeCandidate {}

const normalRouteSpeedFirstRecoveryProbeRetryPolicy = sequenceRetryPolicy('normal_route_speed_first_recovery_probe', [], 0)

const normalRouteSpeedFirstRecoveryProbeQueue = createRetryQueue<NormalRouteSpeedFirstRecoveryProbeQueueItem>({
  name: 'normal-route-speed-first-recovery-probe',
  policy: normalRouteSpeedFirstRecoveryProbeRetryPolicy,
  concurrency: globalSharedQueueConcurrency,
  run: async (item, context) => await runWithBackgroundFullDiagnosticSlot(
    () => runNormalRouteSpeedFirstRecoveryProbeQueueItem(item, context)
  ),
  onExhausted: (event) => {
    logger.warn({
      event: 'background_normal_route_speed_first_recovery_probe_exhausted',
      accountId: event.item.accountId,
      accountName: event.item.accountName,
      routeStrategyId: event.item.scope.routeStrategyId,
      groupId: event.item.scope.groupId,
      attemptCount: event.attemptIndex + 1
    }, '普通路由速度优先恢复探针已用尽，本轮保留降级状态等待下次探针')
  }
})

export function enqueueNormalRouteSpeedFirstRecoveryProbe(candidate: NormalRouteLatencyProbeCandidate): boolean {
  return normalRouteSpeedFirstRecoveryProbeQueue.enqueue(candidate.stateKey, candidate)
}

export function getNormalRouteSpeedFirstRecoveryProbeQueueSnapshot() {
  return normalRouteSpeedFirstRecoveryProbeQueue.snapshot()
}

async function runNormalRouteSpeedFirstRecoveryProbeQueueItem(
  item: NormalRouteSpeedFirstRecoveryProbeQueueItem,
  context: { attemptIndex: number; retryNumber: number }
) {
  const claim = await acquireNormalRouteLatencyProbeClaimAsync(item)
  if (!claim) {
    logger.debug({
      event: 'background_normal_route_speed_first_recovery_probe_claim_busy',
      accountId: item.accountId,
      routeStrategyId: item.scope.routeStrategyId,
      groupId: item.scope.groupId,
      generation: item.generation,
      attemptIndex: context.attemptIndex,
      retryNumber: context.retryNumber
    }, '普通路由速度优先恢复探针已由其他节点领用，本节点跳过重复上游探测')
    return true
  }

  let claimLost = false
  const ensureClaim = async (phase: string): Promise<boolean> => {
    if (claimLost) return false
    try {
      const renewed = await renewNormalRouteLatencyProbeClaimAsync(claim)
      if (renewed) return true
    } catch (error) {
      logger.warn({
        event: 'background_normal_route_speed_first_recovery_probe_claim_renew_failed',
        accountId: item.accountId,
        routeStrategyId: item.scope.routeStrategyId,
        groupId: item.scope.groupId,
        generation: item.generation,
        phase,
        error
      }, '普通路由速度优先恢复探针 claim 续租失败，停止提交该节点的探测结果')
      claimLost = true
      return false
    }
    claimLost = true
    logger.warn({
      event: 'background_normal_route_speed_first_recovery_probe_claim_lost',
      accountId: item.accountId,
      routeStrategyId: item.scope.routeStrategyId,
      groupId: item.scope.groupId,
      generation: item.generation,
      phase
    }, '普通路由速度优先恢复探针 claim 已失效，停止提交该节点的探测结果')
    return false
  }
  const renewTimer = setInterval(() => {
    void ensureClaim('heartbeat')
  }, normalRouteLatencyProbeClaimRenewIntervalMs)
  renewTimer.unref()

  try {
    return await runNormalRouteSpeedFirstRecoveryProbeQueueItemClaimed(item, context, ensureClaim)
  } finally {
    clearInterval(renewTimer)
    try {
      await releaseNormalRouteLatencyProbeClaimAsync(claim)
    } catch (error) {
      logger.warn({
        event: 'background_normal_route_speed_first_recovery_probe_claim_release_failed',
        accountId: item.accountId,
        routeStrategyId: item.scope.routeStrategyId,
        groupId: item.scope.groupId,
        generation: item.generation,
        error
      }, '普通路由速度优先恢复探针 claim 释放失败，将等待 TTL 自动过期')
    }
  }
}

async function runNormalRouteSpeedFirstRecoveryProbeQueueItemClaimed(
  item: NormalRouteSpeedFirstRecoveryProbeQueueItem,
  context: { attemptIndex: number; retryNumber: number },
  ensureClaim: (phase: string) => Promise<boolean>
) {
  if (!await ensureClaim('before_account_load')) return true
  const account = await loadAccountForTestViaDbService(item.accountId, {
    systemAccountId: item.scope.systemAccountId,
    role: 'user'
  })
  const accountForInvalidLog = account
  if (!isNormalRouteSpeedFirstProbeAccountEligible(account)) {
    if (!await ensureClaim('before_discard_ineligible_account')) return true
    await discardNormalRouteLatencyProbeCandidateAsync(item)
    logger.debug({
      event: 'background_normal_route_speed_first_recovery_probe_discarded',
      accountId: item.accountId,
      accountName: item.accountName,
      routeStrategyId: item.scope.routeStrategyId,
      groupId: item.scope.groupId,
      accountStatus: accountForInvalidLog?.status,
      schedulable: accountForInvalidLog?.schedulable
    }, '普通路由速度优先恢复探针目标已失效，已清理运行态降级状态')
    return true
  }

  const candidateAccount = await loadOpenAIAccountForGroupViaDbService(
    item.scope.groupId,
    item.accountId,
    item.scope.systemAccountId,
    { ignoreAvailability: true }
  )
  if (!candidateAccount) {
    if (!await ensureClaim('before_discard_missing_group_account')) return true
    await discardNormalRouteLatencyProbeCandidateAsync(item)
    logger.debug({
      event: 'background_normal_route_speed_first_recovery_probe_account_missing',
      accountId: item.accountId,
      accountName: item.accountName,
      routeStrategyId: item.scope.routeStrategyId,
      groupId: item.scope.groupId
    }, '普通路由速度优先恢复探针账号已不在目标分组内，已清理运行态降级状态')
    return true
  }

  if (!await ensureClaim('before_upstream_probe')) return true
  let upstreamAttempt: UpstreamAttempt | undefined
  const result = await testOpenAIAccount(account, {
    diagnostics: 'limited',
    groupId: item.scope.groupId,
    systemAccountId: item.scope.systemAccountId,
    trafficSource: 'runtime_recovery_probe',
    testEndpointMode: account.healthCheckEndpointMode,
    candidateAccount,
    disableAccountStateMutation: true,
    onUpstreamAttempt: (attempt) => {
      upstreamAttempt = attempt
    },
    findAccountForTest: loadAccountForTestViaDbService,
    findOpenAIAccountForGroup: loadOpenAIAccountForGroupViaDbService,
    gatewaySettingsOverride: {
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0,
      textFirstResponseTimeoutSeconds: probeTimeoutSeconds(item.config.firstByteDeadlineMs),
      noAvailableAccountWaitTimeoutSeconds: probeTimeoutSeconds(item.config.firstByteDeadlineMs),
      textUncommittedAttemptMaxLifetimeSeconds: Math.max(60, probeTimeoutSeconds(item.config.firstByteDeadlineMs))
    }
  })

  const transportOutcome = transportProbeOutcomeFromAccountTestResult(result, { upstreamAttempt })
  const firstByteMs = result.firstTokenMs
  if (transportProbeMeetsFirstByteTarget(result, transportOutcome, item.config.firstByteDeadlineMs)) {
    if (!await ensureClaim('before_record_success')) return true
    const recovery = await recordNormalRouteRecoveryProbeSuccessAsync(candidateAccount, item, firstByteMs)
    logger.info({
      event: recovery?.cleared
        ? 'background_normal_route_speed_first_recovery_probe_restored'
        : 'background_normal_route_speed_first_recovery_probe_passed',
      accountId: item.accountId,
      accountName: item.accountName,
      routeStrategyId: item.scope.routeStrategyId,
      groupId: item.scope.groupId,
      firstByteMs,
      thresholdMs: item.config.firstByteDeadlineMs,
      recoverySuccessCount: recovery?.recoverySuccessCount ?? item.recoverySuccessCount + 1,
      requiredRecoverySuccessCount: recovery?.requiredRecoverySuccessCount ?? item.config.recoverySuccessCount,
      cleared: recovery?.cleared ?? false,
      statusCode: result.statusCode,
      durationMs: result.durationMs,
      attemptIndex: context.attemptIndex,
      retryNumber: context.retryNumber
    }, recovery?.cleared ? '普通路由速度优先恢复探针达标，账号已恢复正常调度' : '普通路由速度优先恢复探针达标，继续累计恢复次数')
    return true
  }

  // An incomplete/unknown transport result cannot prove the account is still
  // slow. It must discard the current two-probe window, rather than join a
  // preceding failure into an FF lease renewal.
  if (normalRouteSpeedFirstRecoveryProbeRequiresWindowReset(result, transportOutcome)) {
    if (!await ensureClaim('before_defer_neutral_result')) return true
    const deferred = await deferNormalRouteLatencyProbeCandidateAsync(item)
    logger.debug({
      event: 'background_normal_route_speed_first_recovery_probe_neutral',
      accountId: item.accountId,
      accountName: item.accountName,
      routeStrategyId: item.scope.routeStrategyId,
      groupId: item.scope.groupId,
      transportOutcome: transportOutcome.kind,
      statusCode: result.statusCode,
      firstByteMs,
      deferred
    }, '普通路由速度优先恢复探针收到中性结果，已保留降级状态并顺延探针')
    return true
  }

  if (!await ensureClaim('before_record_failure')) return true
  await recordNormalRouteProbeFailureAsync(item, probeFailureReason(result, item.config.firstByteDeadlineMs))
  logger.debug({
    event: 'background_normal_route_speed_first_recovery_probe_failed',
    accountId: item.accountId,
    accountName: item.accountName,
    routeStrategyId: item.scope.routeStrategyId,
    groupId: item.scope.groupId,
    transportOutcome: transportOutcome.kind,
    transportFailureKind: transportOutcome.kind === 'framing_complete' ? undefined : transportOutcome.failureKind,
    statusCode: result.statusCode,
    errorCode: result.errorCode,
    firstByteMs,
    thresholdMs: item.config.firstByteDeadlineMs,
    durationMs: result.durationMs,
    attemptIndex: context.attemptIndex,
    retryNumber: context.retryNumber,
    message: result.message
  }, '普通路由速度优先恢复探针未达标，已顺延下次探针')
  return true
}

export function normalRouteSpeedFirstRecoveryProbeRequiresWindowReset(
  result: Pick<AccountTestResult, 'success'>,
  transportOutcome: TransportProbeOutcome
): boolean {
  return transportOutcome.kind !== 'framing_complete' || !result.success
}

async function loadAccountForTestViaDbService(accountId: string, access?: AccessScope): Promise<AccountSummary | undefined> {
  return await requestBackgroundWorkerDbService({
    type: 'find_account_for_test',
    accountId,
    access
  }, backgroundProbeDbServiceTimeoutMs)
}

async function loadOpenAIAccountForGroupViaDbService(
  groupId: string,
  accountId: string,
  systemAccountId: string,
  options: { includeUnavailable?: boolean; ignoreAvailability?: boolean } = { ignoreAvailability: true }
): Promise<OpenAIAccountSecret | undefined> {
  return await requestBackgroundWorkerDbService({
    type: 'find_openai_account_for_group',
    groupId,
    accountId,
    systemAccountId,
    includeUnavailable: options.includeUnavailable,
    ignoreAvailability: options.ignoreAvailability
  }, backgroundProbeDbServiceTimeoutMs)
}

function isNormalRouteSpeedFirstProbeAccountEligible(account: AccountSummary | undefined): account is AccountSummary {
  if (!account) return false
  if (account.status !== 'active' || !account.schedulable) return false
  if (account.accountExpiresAt) {
    const expiresAtMs = rfc3339InstantMilliseconds(account.accountExpiresAt)
    if (expiresAtMs === undefined) throw new Error(`速度优先恢复探针 accountExpiresAt 必须是带 Z 或数值 offset 的 RFC3339 时间：${account.accountExpiresAt}`)
    if (expiresAtMs <= Date.now()) return false
  }
  if (account.effectiveAvailability && !account.effectiveAvailability.available) return false
  return true
}

function probeTimeoutSeconds(firstByteDeadlineMs: number): number {
  return Math.max(10, Math.ceil((firstByteDeadlineMs + 10_000) / 1000))
}

function probeFailureReason(result: AccountTestResult, thresholdMs: number): string {
  const parts = [`普通路由速度优先恢复探针未满足 ${thresholdMs}ms 首字阈值`]
  if (result.firstTokenMs !== undefined) {
    parts.push(`首字 ${result.firstTokenMs}ms`)
  }
  if (typeof result.statusCode === 'number') {
    parts.push(`HTTP ${Math.trunc(result.statusCode)}`)
  }
  if (result.errorCode) {
    parts.push(result.errorCode)
  }
  if (result.message) {
    parts.push(result.message)
  }
  return parts.join('；').slice(0, 1000)
}
