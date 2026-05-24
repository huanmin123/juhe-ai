import type { AccountSummary } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import {
  findAccountForTest,
  findRecentOpenAIRequestShapeForAccount,
  recordCooldownAccountRetestFailure
} from '../../storage/repositories.js'
import { testOpenAIAccount } from '../accounts/account-test.service.js'

interface CooldownAccountRetestQueueItem {
  accountId: string
  accountName: string
  model: string
  maxPauseMinutes: number
  maxRecoveryHours: number
}

const cooldownAccountRetestRetryPolicy = sequenceRetryPolicy('cooldown_account_retest_revival', [], 0)

const cooldownAccountRetestQueue = createRetryQueue<CooldownAccountRetestQueueItem>({
  name: 'cooldown-account-retest',
  policy: cooldownAccountRetestRetryPolicy,
  concurrency: 1,
  run: runCooldownAccountRetestQueueItem,
  onExhausted: (event) => {
    logger.warn({
      event: 'background_cooldown_account_retest_retry_exhausted',
      accountId: event.item.accountId,
      accountName: event.item.accountName,
      attemptCount: event.attemptIndex + 1
    }, '冷却账户复测重试已用尽，本轮保留冷却状态等待下个周期')
  }
})

export function enqueueCooldownAccountRetest(
  account: AccountSummary,
  model: string,
  strategy: { maxPauseMinutes: number; maxRecoveryHours: number }
): boolean {
  return cooldownAccountRetestQueue.enqueue(account.id, {
    accountId: account.id,
    accountName: account.name,
    model,
    maxPauseMinutes: strategy.maxPauseMinutes,
    maxRecoveryHours: strategy.maxRecoveryHours
  })
}

export function getCooldownAccountRetestQueueSnapshot() {
  return cooldownAccountRetestQueue.snapshot()
}

async function runCooldownAccountRetestQueueItem(
  item: CooldownAccountRetestQueueItem,
  context: { attemptIndex: number; retryNumber: number }
) {
  const account = findAccountForTest(item.accountId)
  if (!account || !isAccountDueForCooldownRetest(account)) {
    logger.info({
      event: 'background_cooldown_account_retest_discarded',
      accountId: item.accountId,
      accountName: item.accountName,
      attemptIndex: context.attemptIndex,
      accountStatus: account?.status,
      boundGroupId: account?.boundGroupId,
      cooldownUntil: account?.cooldownUntil
    }, '冷却账户复测任务已失效，跳过队列项')
    return true
  }

  const groupId = account.boundGroupId
  const result = await testOpenAIAccount(account, {
    model: item.model,
    diagnostics: 'limited',
    groupId,
    requestShape: findRecentOpenAIRequestShapeForAccount(account.id, groupId),
    trafficSource: 'cooldown_retest',
    gatewaySettingsOverride: {
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0
    }
  })
  if (result.success) {
    logger.info({
      event: 'background_cooldown_account_retest_restored',
      accountId: account.id,
      accountName: account.name,
      attemptIndex: context.attemptIndex,
      retryNumber: context.retryNumber,
      statusCode: result.statusCode,
      durationMs: result.durationMs,
      accountStatus: result.accountStatus
    }, '冷却账户复测通过，账号已尝试恢复到可用状态')
    return true
  }

  const failure = recordCooldownAccountRetestFailure(account.id, {
    statusCode: result.statusCode,
    errorCode: result.errorCode,
    errorMessage: result.message,
    maxPauseMinutes: item.maxPauseMinutes,
    maxRecoveryHours: item.maxRecoveryHours
  })

  const logFields = {
    event: 'background_cooldown_account_retest_failed',
    accountId: account.id,
    accountName: account.name,
    accountStatus: account.status,
    attemptIndex: context.attemptIndex,
    retryNumber: context.retryNumber,
    statusCode: result.statusCode,
    errorCode: result.errorCode,
    durationMs: result.durationMs,
    retestFailureCount: failure.failureCount,
    retestAction: failure.action,
    recoveryStage: failure.recoveryStage,
    nextCooldownUntil: failure.cooldownUntil,
    nextBackoffSeconds: failure.backoffSeconds,
    maxPauseSeconds: failure.maxPauseSeconds,
    maxRecoverySeconds: failure.maxRecoverySeconds,
    maxedFailureCount: failure.maxedFailureCount,
    observationStartedAt: failure.observationStartedAt,
    observationElapsedSeconds: failure.observationElapsedSeconds,
    message: result.message
  }
  if (failure.action === 'error') {
    logger.warn(logFields, '冷却账户复测超过最长自动恢复观察，账号已转为异常')
  } else if (failure.recoveryStage === 'slow') {
    logger.warn(logFields, '冷却账户复测未通过，已进入慢速恢复通道')
  } else {
    logger.debug(logFields, '冷却账户快速恢复通道复测未通过，已按短退避等待下次复测')
  }
  return true
}

function isAccountDueForCooldownRetest(account: AccountSummary): boolean {
  if (account.status !== 'temporary_unavailable') {
    return false
  }
  if (!account.schedulable || !account.cooldownUntil) {
    return false
  }
  if (!account.boundGroupId) {
    return false
  }
  const cooldownUntilMs = Date.parse(account.cooldownUntil)
  if (!Number.isFinite(cooldownUntilMs) || cooldownUntilMs > Date.now()) {
    return false
  }
  if (account.accountExpiresAt) {
    const expiresAtMs = Date.parse(account.accountExpiresAt)
    if (Number.isFinite(expiresAtMs) && expiresAtMs <= Date.now()) {
      return false
    }
  }
  return true
}
