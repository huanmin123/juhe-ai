import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import type { AccountHealthCheckSettings } from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { testOpenAIAccountWithDiagnosticRetries } from '../accounts/account-test.service.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'

interface AccountHealthCheckQueueItem extends AccountHealthCheckSettings {
  accountId: string
  accountName: string
  maxPauseMinutes: number
}

const accountHealthCheckRetryPolicy = sequenceRetryPolicy('account_health_check', [], 0)

const accountHealthCheckQueue = createRetryQueue<AccountHealthCheckQueueItem>({
  name: 'account-health-check',
  policy: accountHealthCheckRetryPolicy,
  concurrency: 1,
  run: runAccountHealthCheckQueueItem,
  onExhausted: (event) => {
    logger.warn({
      event: 'background_account_health_check_exhausted',
      accountId: event.item.accountId,
      accountName: event.item.accountName,
      attemptCount: event.attemptIndex + 1
    }, '账号健康检测任务已用尽，本轮跳过')
  }
})

export function enqueueAccountHealthCheck(
  account: AccountSummary,
  settings: AccountHealthCheckSettings & { maxPauseMinutes: number }
): boolean {
  return accountHealthCheckQueue.enqueue(account.id, {
    accountId: account.id,
    accountName: account.name,
    intervalHours: settings.intervalHours,
    jitterMinutes: settings.jitterMinutes,
    failureThreshold: settings.failureThreshold,
    maxPauseMinutes: settings.maxPauseMinutes
  })
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
  if (!isAccountHealthCheckEligible(account)) {
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

  const groupId = account.boundGroupId
  const result = await testOpenAIAccountWithDiagnosticRetries(account, {
    diagnostics: 'limited',
    groupId,
    trafficSource: 'cooldown_retest',
    disableAccountStateMutation: true,
    findAccountForTest: loadAccountForTestViaDbService,
    findOpenAIAccountForGroup: loadOpenAIAccountForGroupViaDbService,
    gatewaySettingsOverride: {
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0
    }
  })

  if (result.success) {
    const healthCheckResult = await requestBackgroundWorkerDbService({
      type: 'record_account_health_check_success',
      accountId: account.id,
      input: {
      intervalHours: item.intervalHours,
      jitterMinutes: item.jitterMinutes,
      failureThreshold: item.failureThreshold,
      statusCode: result.statusCode
      }
    })
    const changed = healthCheckResult?.changed ?? false
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
      errorMessage: result.message
    }
  })

  let markedTemporaryUnavailable = false
  if (failure?.reachedThreshold && result.accountFailureEligible !== false) {
    const updated = await requestBackgroundWorkerDbService({
      type: 'mark_account_test_temporary_unavailable',
      accountId: account.id,
      reason: accountHealthCheckTemporaryUnavailableReason(failure.failureCount, result),
      access: { systemAccountId: account.systemAccountId ?? '', role: 'user' }
    })
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
    accountFailureEligible: result.accountFailureEligible,
    nextHealthCheckAt: failure?.nextHealthCheckAt,
    markedTemporaryUnavailable,
    attemptIndex: context.attemptIndex,
    retryNumber: context.retryNumber,
    message: result.message
  }
  if (failure?.reachedThreshold && result.accountFailureEligible !== false) {
    logger.warn(logFields, '账号健康检测连续失败，已尝试标记为临时不可调用')
  } else {
    logger.warn(logFields, '账号健康检测失败，已记录失败并安排短间隔复检')
  }
  return true
}

async function accountForHealthCheckQueueItem(item: AccountHealthCheckQueueItem): Promise<AccountSummary | undefined> {
  return await requestBackgroundWorkerDbService({
    type: 'find_account_for_health_check',
    accountId: item.accountId
  }, 10_000)
}

async function loadAccountForTestViaDbService(accountId: string, access?: AccessScope): Promise<AccountSummary | undefined> {
  return await requestBackgroundWorkerDbService({
    type: 'find_account_for_test',
    accountId,
    access
  }, 10_000)
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
  }, 10_000)
}

function isAccountHealthCheckEligible(account: AccountSummary | undefined): account is AccountSummary & { boundGroupId: string } {
  if (!account) return false
  if (account.status !== 'active' || !account.schedulable || !account.boundGroupId) return false
  if (account.healthCheckEnabled === false) return false
  if (account.accountExpiresAt) {
    const expiresAtMs = Date.parse(account.accountExpiresAt)
    if (Number.isFinite(expiresAtMs) && expiresAtMs <= Date.now()) return false
  }
  if (account.nextHealthCheckAt) {
    const nextMs = Date.parse(account.nextHealthCheckAt)
    if (Number.isFinite(nextMs) && nextMs > Date.now()) return false
  }
  if (account.effectiveAvailability && !account.effectiveAvailability.available) return false
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
