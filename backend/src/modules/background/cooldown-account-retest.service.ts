import type { AccountSummary } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import {
  findAccountForTest,
  findRecentOpenAIRequestShapeForAccount
} from '../../storage/repositories.js'
import { testOpenAIAccount } from '../accounts/account-test.service.js'

interface CooldownAccountRetestQueueItem {
  accountId: string
  accountName: string
  groupId?: string
  model: string
}

const cooldownAccountRetestRetryPolicy = sequenceRetryPolicy('cooldown_account_retest_revival', [
  3_000,
  10_000,
  30_000
])

const cooldownAccountRetestQueue = createRetryQueue<CooldownAccountRetestQueueItem>({
  name: 'cooldown-account-retest',
  policy: cooldownAccountRetestRetryPolicy,
  concurrency: 1,
  run: runCooldownAccountRetestQueueItem,
  onRetryScheduled: (event) => {
    logger.warn({
      event: 'background_cooldown_account_retest_retry_scheduled',
      accountId: event.item.accountId,
      accountName: event.item.accountName,
      retryNumber: event.retryNumber,
      nextAttemptIndex: event.attemptIndex + 1,
      retryDelayMs: event.delayMs,
      retryAt: new Date(event.nextAttemptAtMs).toISOString()
    }, '冷却账户复测未通过，已加入异步重试队列')
  },
  onExhausted: (event) => {
    logger.warn({
      event: 'background_cooldown_account_retest_retry_exhausted',
      accountId: event.item.accountId,
      accountName: event.item.accountName,
      attemptCount: event.attemptIndex + 1
    }, '冷却账户复测重试已用尽，本轮保留冷却状态等待下个周期')
  }
})

export function enqueueCooldownAccountRetest(account: AccountSummary, model: string): boolean {
  return cooldownAccountRetestQueue.enqueue(account.id, {
    accountId: account.id,
    accountName: account.name,
    groupId: account.boundGroupId,
    model
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
      cooldownUntil: account?.cooldownUntil
    }, '冷却账户复测任务已失效，跳过队列项')
    return true
  }

  const result = await testOpenAIAccount(account, {
    model: item.model,
    diagnostics: 'limited',
    groupId: item.groupId ?? account.boundGroupId,
    requestShape: findRecentOpenAIRequestShapeForAccount(account.id, item.groupId ?? account.boundGroupId)
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

  logger.warn({
    event: 'background_cooldown_account_retest_failed',
    accountId: account.id,
    accountName: account.name,
    accountStatus: account.status,
    attemptIndex: context.attemptIndex,
    retryNumber: context.retryNumber,
    statusCode: result.statusCode,
    durationMs: result.durationMs,
    message: result.message
  }, '冷却账户复测未通过')
  return false
}

function isAccountDueForCooldownRetest(account: AccountSummary): boolean {
  if (account.status !== 'rate_limited' && account.status !== 'temporary_unavailable') {
    return false
  }
  if (!account.schedulable || !account.cooldownUntil) {
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
