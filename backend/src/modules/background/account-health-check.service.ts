import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import type { AccountHealthCheckSettings } from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { testOpenAIAccountWithDiagnosticRetries } from '../accounts/account-test.service.js'
import {
  automaticAccountAvailabilityProbeFailed,
  automaticAccountProbeOutcome
} from '../accounts/automatic-account-probe-outcome.js'
import type { UpstreamAttempt } from '../gateway/upstream/attempt.js'
import { gatewayAccountRuntimeKey } from '../gateway/runtime/account-runtime-keys.js'
import { requestBackgroundWorkerDbService, sendAccountRuntimeClearToServer } from './background-ipc.js'
import {
  accountHealthCheckTriggerPriority,
  type AccountHealthCheckTriggerReason
} from '../accounts/account-health-check-trigger.js'
import { enqueueAccountBalanceAutoDetection } from './account-balance-auto-detect.service.js'
import {
  backgroundProbeDbServiceTimeoutMs,
  runWithBackgroundAccountAvailabilityProbe,
  runWithBackgroundFullDiagnosticSlot
} from './account-probe-limits.js'

interface AccountHealthCheckQueueItem extends AccountHealthCheckSettings {
  accountId: string
  accountName: string
  configRevision: number
  maxPauseMinutes: number
  reason: AccountHealthCheckTriggerReason
}

const accountHealthCheckRetryPolicy = sequenceRetryPolicy('account_health_check', [], 0)
const requestFailureHealthCheckCooldownMs = 5 * 60_000
const recentRequestFailureHealthChecks = new Map<string, number>()
let lastRequestFailureHealthCheckCleanupAt = 0

const accountHealthCheckQueue = createRetryQueue<AccountHealthCheckQueueItem>({
  name: 'account-health-check',
  policy: accountHealthCheckRetryPolicy,
  concurrency: 10,
  reservedPriorityConcurrency: {
    priorityAtMost: accountHealthCheckTriggerPriority('request_failure'),
    slots: 3
  },
  run: (item, context) => runWithBackgroundFullDiagnosticSlot(() => runAccountHealthCheckQueueItem(item, context)),
  onExhausted: (event) => {
    logger.warn(errorLogFields(event.error, {
      event: 'background_account_health_check_exhausted',
      accountId: event.item.accountId,
      accountName: event.item.accountName,
      attemptCount: event.attemptIndex + 1
    }), '账号健康检测任务已用尽，本轮跳过')
  }
})

export function enqueueAccountHealthCheck(
  account: AccountSummary,
  settings: AccountHealthCheckSettings & { maxPauseMinutes: number },
  reason: AccountHealthCheckTriggerReason
): boolean {
  const effectiveReason = account.status === 'pending_test' ? 'activation' : reason
  const now = Date.now()
  if (effectiveReason === 'request_failure') {
    cleanupRecentRequestFailureHealthChecks(now)
    const lastTriggeredAt = recentRequestFailureHealthChecks.get(account.id)
    if (lastTriggeredAt !== undefined && now - lastTriggeredAt < requestFailureHealthCheckCooldownMs) {
      return false
    }
  }
  const enqueued = accountHealthCheckQueue.enqueue(account.id, {
    accountId: account.id,
    accountName: account.name,
    configRevision: account.configRevision ?? 1,
    intervalHours: settings.intervalHours,
    jitterMinutes: settings.jitterMinutes,
    failureThreshold: effectiveReason === 'request_failure' ? 1 : settings.failureThreshold,
    maxPauseMinutes: settings.maxPauseMinutes,
    reason: effectiveReason
  }, {
    priority: accountHealthCheckTriggerPriority(effectiveReason),
    replaceExisting: effectiveReason !== 'scheduled',
    replaceExistingOnlyIfHigherPriority: effectiveReason === 'request_failure'
  })
  if (effectiveReason === 'request_failure') {
    recentRequestFailureHealthChecks.set(account.id, now)
  }
  return enqueued
}

function cleanupRecentRequestFailureHealthChecks(now: number): void {
  if (now - lastRequestFailureHealthCheckCleanupAt < 60_000) return
  lastRequestFailureHealthCheckCleanupAt = now
  const cutoff = now - requestFailureHealthCheckCooldownMs
  for (const [accountId, triggeredAt] of recentRequestFailureHealthChecks) {
    if (triggeredAt <= cutoff) recentRequestFailureHealthChecks.delete(accountId)
  }
}

export async function enqueueAccountHealthCheckById(
  accountId: string,
  settings: AccountHealthCheckSettings & { maxPauseMinutes: number },
  reason: AccountHealthCheckTriggerReason
): Promise<boolean> {
  const normalizedId = accountId.trim()
  if (!normalizedId) return false
  const account = await requestBackgroundWorkerDbService({
    type: 'find_account_for_health_check',
    accountId: normalizedId,
    ignoreSchedule: reason !== 'scheduled'
  }, backgroundProbeDbServiceTimeoutMs)
  return account ? enqueueAccountHealthCheck(account, settings, reason) : false
}

export function getAccountHealthCheckQueueSnapshot() {
  return accountHealthCheckQueue.snapshot()
}

export function setAccountHealthCheckQueueConcurrency(concurrency: number): void {
  accountHealthCheckQueue.setConcurrency(concurrency)
}

async function runAccountHealthCheckQueueItem(
  item: AccountHealthCheckQueueItem,
  context: { attemptIndex: number; retryNumber: number }
) {
  const account = await accountForHealthCheckQueueItem(item)
  if (!isAccountHealthCheckEligible(account, item.reason)) {
    logger.debug({
      event: 'background_account_health_check_discarded',
      accountId: item.accountId,
      accountName: item.accountName,
      accountStatus: account?.status,
      schedulable: account?.schedulable,
      boundGroupId: account?.boundGroupId,
      nextHealthCheckAt: account?.nextHealthCheckAt,
      effectiveAvailabilityStatus: account?.effectiveAvailability?.status
    }, '账号健康检测任务已失效，跳过')
    return true
  }
  if ((account.configRevision ?? 1) !== item.configRevision) {
    logger.debug({
      event: 'background_account_health_check_stale_config_discarded',
      accountId: item.accountId,
      accountName: item.accountName,
      queuedConfigRevision: item.configRevision,
      currentConfigRevision: account.configRevision ?? 1
    }, '账号配置已变化，丢弃旧健康检测任务')
    return true
  }

  const groupId = account.boundGroupId
  const observedAt = new Date().toISOString()
  return await runWithBackgroundAccountAvailabilityProbe(gatewayAccountRuntimeKey(account), async () => {
    let upstreamAttempt: UpstreamAttempt | undefined
    const result = await testOpenAIAccountWithDiagnosticRetries(account, {
      diagnostics: 'limited',
      groupId,
      trafficSource: 'account_health_check',
      testEndpointMode: account.healthCheckEndpointMode,
      disableAccountStateMutation: true,
      retryAllFailures: true,
      onDiagnosticAttemptProgress: () => {
        upstreamAttempt = undefined
      },
      onUpstreamAttempt: (attempt) => {
        upstreamAttempt = attempt
      },
      findAccountForTest: loadAccountForTestViaDbService,
      findOpenAIAccountForGroup: loadOpenAIAccountForGroupViaDbService,
      gatewaySettingsOverride: {
        temporaryUnschedulableRetryAttempts: 0,
        temporaryUnschedulableRetryIntervalSeconds: 0
      }
    })
    return { result, upstreamAttempt }
  }, async ({ result, upstreamAttempt }, { joined }) => {
    if (joined) {
      logger.debug({
        event: 'background_account_health_check_singleflight_joined',
        accountId: item.accountId,
        accountName: item.accountName,
        reason: item.reason
      }, '同一账户已有可用性探针执行，本轮健康检查复用其结果')
    }
    const probeOutcome = automaticAccountProbeOutcome(result, { upstreamAttempt })
    const availabilityProbeFailed = automaticAccountAvailabilityProbeFailed(probeOutcome)

    if (probeOutcome === 'complete_success') {
      const healthCheckResult = await requestBackgroundWorkerDbService({
        type: 'record_account_health_check_success',
        accountId: account.id,
        input: {
          intervalHours: item.intervalHours,
          jitterMinutes: item.jitterMinutes,
          failureThreshold: item.failureThreshold,
          statusCode: result.statusCode,
          expectedConfigRevision: item.configRevision,
          traceId: result.traceId
        }
      }, backgroundProbeDbServiceTimeoutMs)
      const changed = healthCheckResult?.changed ?? false
      sendAccountRuntimeClearToServer({ accountId: account.id })
      logger.info({
        event: 'background_account_health_check_passed',
        accountId: account.id,
        accountName: account.name,
        statusCode: result.statusCode,
        durationMs: result.durationMs,
        changed,
        attemptIndex: context.attemptIndex,
        retryNumber: context.retryNumber
      }, '账号健康检测通过，已顺延下次检测')
      if (changed && account.status === 'pending_test') {
        enqueueAccountBalanceAutoDetection(account.id, item.configRevision)
      }
      return true
    }

    const failure = await requestBackgroundWorkerDbService({
      type: 'record_account_health_check_failure',
      accountId: account.id,
      input: {
        intervalHours: item.intervalHours,
        jitterMinutes: item.jitterMinutes,
        failureThreshold: item.failureThreshold,
        statusCode: result.statusCode,
        errorCode: result.errorCode,
        errorMessage: result.message,
        countTowardsThreshold: availabilityProbeFailed,
        expectedConfigRevision: item.configRevision,
        observedAt,
        traceId: result.traceId
      }
    }, backgroundProbeDbServiceTimeoutMs)

    let markedTemporaryUnavailable = false
    if (account.status !== 'pending_test' && failure?.reachedThreshold && availabilityProbeFailed) {
      const updated = await requestBackgroundWorkerDbService({
        type: 'mark_account_test_temporary_unavailable',
        accountId: account.id,
        reason: accountHealthCheckTemporaryUnavailableReason(failure.failureCount, result),
        traceId: result.traceId,
        access: { systemAccountId: account.systemAccountId ?? '', role: 'user' },
        healthCheckGuard: {
          configRevision: item.configRevision,
          checkedAt: failure.checkedAt,
          failureCount: failure.failureCount,
          observedAt
        }
      }, backgroundProbeDbServiceTimeoutMs)
      markedTemporaryUnavailable = updated?.updated ?? false
    }

    const logFields = {
      event: 'background_account_health_check_failed',
      accountId: account.id,
      accountName: account.name,
      statusCode: result.statusCode,
      errorCode: result.errorCode,
      durationMs: result.durationMs,
      failureCount: failure?.failureCount ?? 0,
      reachedThreshold: failure?.reachedThreshold ?? false,
      failureStartedAt: failure?.failureStartedAt,
      transitionedToError: failure?.transitionedToError ?? false,
      accountFailureEligible: result.accountFailureEligible,
      nextHealthCheckAt: failure?.nextHealthCheckAt,
      markedTemporaryUnavailable,
      attemptIndex: context.attemptIndex,
      retryNumber: context.retryNumber,
      message: result.message,
      traceId: result.traceId
    }
    if (failure?.transitionedToError) {
      logger.error(logFields, '账号激活检查从首次失败起已持续 24 小时，账户已转为异常')
    } else if (account.status !== 'pending_test' && failure?.reachedThreshold && availabilityProbeFailed) {
      logger.warn(logFields, '账号健康检测连续失败，已尝试标记为临时不可调用')
    } else {
      logger.warn(logFields, '账号健康检测失败，已记录失败并安排短间隔复检')
    }
    return true
  })
}

async function accountForHealthCheckQueueItem(item: AccountHealthCheckQueueItem): Promise<AccountSummary | undefined> {
  return await requestBackgroundWorkerDbService({
    type: 'find_account_for_health_check',
    accountId: item.accountId,
    ignoreSchedule: item.reason !== 'scheduled'
  }, backgroundProbeDbServiceTimeoutMs)
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
) {
  return await requestBackgroundWorkerDbService({
    type: 'find_openai_account_for_group',
    groupId,
    accountId,
    systemAccountId,
    includeUnavailable: options.includeUnavailable,
    ignoreAvailability: options.ignoreAvailability
  }, backgroundProbeDbServiceTimeoutMs)
}

function isAccountHealthCheckEligible(
  account: AccountSummary | undefined,
  reason: AccountHealthCheckTriggerReason
): account is AccountSummary & { boundGroupId: string } {
  if (!account) return false
  if (!['active', 'pending_test'].includes(account.status) || !account.boundGroupId) return false
  if (account.status === 'active' && !account.schedulable) return false
  if (account.accountExpiresAt) {
    const expiresAtMs = Date.parse(account.accountExpiresAt)
    if (Number.isFinite(expiresAtMs) && expiresAtMs <= Date.now()) return false
  }
  if (reason === 'scheduled' && account.nextHealthCheckAt) {
    const nextMs = Date.parse(account.nextHealthCheckAt)
    if (Number.isFinite(nextMs) && nextMs > Date.now()) return false
  }
  if (account.status === 'active' && account.effectiveAvailability && !account.effectiveAvailability.available) return false
  return true
}

function accountHealthCheckTemporaryUnavailableReason(failureCount: number, result: AccountTestResult): string {
  const parts = [`后台健康检测连续失败 ${failureCount} 次，已标记为临时不可调用`]
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
