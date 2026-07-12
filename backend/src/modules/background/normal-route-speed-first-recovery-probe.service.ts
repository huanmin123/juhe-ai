import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { testOpenAIAccount } from '../accounts/account-test.service.js'
import type { OpenAIAccountSecret } from '../../storage/repositories.js'
import {
  discardNormalRouteLatencyProbeCandidateAsync,
  recordNormalRouteFirstByteSuccessAsync,
  recordNormalRouteProbeFailureAsync,
  type NormalRouteLatencyProbeCandidate
} from '../gateway/runtime/normal-route-latency-degradation.service.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'
import { backgroundProbeDbServiceTimeoutMs } from './account-probe-limits.js'

interface NormalRouteSpeedFirstRecoveryProbeQueueItem extends NormalRouteLatencyProbeCandidate {}

const normalRouteSpeedFirstRecoveryProbeRetryPolicy = sequenceRetryPolicy('normal_route_speed_first_recovery_probe', [], 0)

const normalRouteSpeedFirstRecoveryProbeQueue = createRetryQueue<NormalRouteSpeedFirstRecoveryProbeQueueItem>({
  name: 'normal-route-speed-first-recovery-probe',
  policy: normalRouteSpeedFirstRecoveryProbeRetryPolicy,
  concurrency: 1,
  run: runNormalRouteSpeedFirstRecoveryProbeQueueItem,
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

export function setNormalRouteSpeedFirstRecoveryProbeQueueConcurrency(concurrency: number): void {
  normalRouteSpeedFirstRecoveryProbeQueue.setConcurrency(concurrency)
}

async function runNormalRouteSpeedFirstRecoveryProbeQueueItem(
  item: NormalRouteSpeedFirstRecoveryProbeQueueItem,
  context: { attemptIndex: number; retryNumber: number }
) {
  const account = await loadAccountForTestViaDbService(item.accountId, {
    systemAccountId: item.scope.systemAccountId,
    role: 'user'
  })
  const accountForInvalidLog = account
  if (!isNormalRouteSpeedFirstProbeAccountEligible(account)) {
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

  const result = await testOpenAIAccount(account, {
    diagnostics: 'limited',
    groupId: item.scope.groupId,
    systemAccountId: item.scope.systemAccountId,
    trafficSource: 'runtime_recovery_probe',
    candidateAccount,
    disableAccountStateMutation: true,
    findAccountForTest: loadAccountForTestViaDbService,
    findOpenAIAccountForGroup: loadOpenAIAccountForGroupViaDbService,
    gatewaySettingsOverride: {
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0,
      streamRequestTimeoutSeconds: probeTimeoutSeconds(item.config.firstByteThresholdMs),
      streamClientTotalWaitTimeoutSeconds: probeTimeoutSeconds(item.config.firstByteThresholdMs),
      streamMaxLifetimeSeconds: Math.max(60, probeTimeoutSeconds(item.config.firstByteThresholdMs))
    }
  })

  const firstByteMs = result.firstTokenMs
  if (result.success && firstByteMs !== undefined && firstByteMs <= item.config.firstByteThresholdMs) {
    const recovery = await recordNormalRouteFirstByteSuccessAsync(candidateAccount, item.scope, item.config, firstByteMs)
    logger.info({
      event: recovery?.cleared
        ? 'background_normal_route_speed_first_recovery_probe_restored'
        : 'background_normal_route_speed_first_recovery_probe_passed',
      accountId: item.accountId,
      accountName: item.accountName,
      routeStrategyId: item.scope.routeStrategyId,
      groupId: item.scope.groupId,
      firstByteMs,
      thresholdMs: item.config.firstByteThresholdMs,
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

  await recordNormalRouteProbeFailureAsync(item, probeFailureReason(result, item.config.firstByteThresholdMs))
  logger.debug({
    event: 'background_normal_route_speed_first_recovery_probe_failed',
    accountId: item.accountId,
    accountName: item.accountName,
    routeStrategyId: item.scope.routeStrategyId,
    groupId: item.scope.groupId,
    success: result.success,
    statusCode: result.statusCode,
    errorCode: result.errorCode,
    firstByteMs,
    thresholdMs: item.config.firstByteThresholdMs,
    durationMs: result.durationMs,
    attemptIndex: context.attemptIndex,
    retryNumber: context.retryNumber,
    message: result.message
  }, '普通路由速度优先恢复探针未达标，已顺延下次探针')
  return true
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
    const expiresAtMs = Date.parse(account.accountExpiresAt)
    if (Number.isFinite(expiresAtMs) && expiresAtMs <= Date.now()) return false
  }
  if (account.effectiveAvailability && !account.effectiveAvailability.available) return false
  return true
}

function probeTimeoutSeconds(firstByteThresholdMs: number): number {
  return Math.max(10, Math.ceil((firstByteThresholdMs + 10_000) / 1000))
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
