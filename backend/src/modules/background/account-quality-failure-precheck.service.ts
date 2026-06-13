import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import {
  findAccountForTest,
  findRecentOpenAIRequestShapeForAccount,
  markAccountTestTemporaryUnavailable,
  type AccountQualityFailurePrecheckCandidate
} from '../../storage/repositories.js'
import { preferredSystemAccountTestModel, testOpenAIAccountWithDiagnosticRetries } from '../accounts/account-test.service.js'

interface AccountQualityFailurePrecheckQueueItem extends AccountQualityFailurePrecheckCandidate {
  enqueuedAt: string
}

const accountQualityFailurePrecheckRetryPolicy = sequenceRetryPolicy('account_quality_failure_precheck', [], 0)
const recentQualityFailurePrecheckRetentionMs = 30 * 60_000
const recentQualityFailurePrechecks = new Map<string, number>()

const accountQualityFailurePrecheckQueue = createRetryQueue<AccountQualityFailurePrecheckQueueItem>({
  name: 'account-quality-failure-precheck',
  policy: accountQualityFailurePrecheckRetryPolicy,
  concurrency: 1,
  run: runAccountQualityFailurePrecheckQueueItem,
  onExhausted: (event) => {
    logger.warn({
      event: 'background_account_quality_failure_precheck_exhausted',
      accountId: event.item.accountId,
      recentRequestCount: event.item.recentRequestCount,
      recentErrorCount: event.item.recentErrorCount,
      attemptCount: event.attemptIndex + 1
    }, '账户质量失败确认任务已用尽，本轮跳过')
  }
})

export function enqueueAccountQualityFailurePrecheck(candidate: AccountQualityFailurePrecheckCandidate): boolean {
  cleanupRecentQualityFailurePrechecks()
  if (wasRecentlyQualityFailurePrechecked(candidate.accountId)) {
    return false
  }
  return accountQualityFailurePrecheckQueue.enqueue(candidate.accountId, {
    ...candidate,
    enqueuedAt: new Date().toISOString()
  })
}

export function getAccountQualityFailurePrecheckQueueSnapshot() {
  return accountQualityFailurePrecheckQueue.snapshot()
}

async function runAccountQualityFailurePrecheckQueueItem(
  item: AccountQualityFailurePrecheckQueueItem,
  context: { attemptIndex: number; retryNumber: number }
) {
  const accountAccess = { systemAccountId: item.systemAccountId, role: 'user' as const }
  const account = findAccountForTest(item.accountId, accountAccess)
  if (!isAccountQualityFailurePrecheckEligible(account)) {
    rememberQualityFailurePrechecked(item.accountId)
    logger.debug({
      event: 'background_account_quality_failure_precheck_discarded',
      accountId: item.accountId,
      accountStatus: account?.status,
      schedulable: account?.schedulable,
      boundGroupId: account?.boundGroupId,
      effectiveAvailabilityStatus: account?.effectiveAvailability?.status
    }, '账户质量失败确认任务已失效，跳过')
    return true
  }

  const groupId = account.boundGroupId
  const result = await testOpenAIAccountWithDiagnosticRetries(account, {
    model: preferredSystemAccountTestModel(account),
    diagnostics: 'full',
    groupId,
    requestShape: findRecentOpenAIRequestShapeForAccount(account.id, groupId),
    trafficSource: 'cooldown_retest',
    disableAccountStateMutation: true,
    gatewaySettingsOverride: {
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0
    }
  })
  rememberQualityFailurePrechecked(item.accountId)

  if (result.success) {
    logger.info({
      event: 'background_account_quality_failure_precheck_recovered',
      accountId: account.id,
      accountName: account.name,
      statusCode: result.statusCode,
      durationMs: result.durationMs,
      recentRequestCount: item.recentRequestCount,
      recentErrorCount: item.recentErrorCount,
      attemptIndex: context.attemptIndex,
      retryNumber: context.retryNumber
    }, '账户近期频繁失败但后台确认通过，保留正常状态')
    return true
  }

  if (result.accountFailureEligible === false) {
    logger.warn({
      event: 'background_account_quality_failure_precheck_ineligible_failure_discarded',
      accountId: account.id,
      accountName: account.name,
      statusCode: result.statusCode,
      errorCode: result.errorCode,
      durationMs: result.durationMs,
      message: result.message,
      recentRequestCount: item.recentRequestCount,
      recentErrorCount: item.recentErrorCount
    }, '账户近期频繁失败但确认失败不属于账号失败，已跳过状态写入')
    return true
  }

  const updated = markAccountTestTemporaryUnavailable(account, accountQualityFailurePrecheckReason(item, result), accountAccess)
  logger.warn({
    event: 'background_account_quality_failure_precheck_marked',
    accountId: account.id,
    accountName: account.name,
    statusCode: result.statusCode,
    errorCode: result.errorCode,
    durationMs: result.durationMs,
    recentRequestCount: item.recentRequestCount,
    recentErrorCount: item.recentErrorCount,
    accountStatus: updated?.status,
    updated: Boolean(updated)
  }, '账户近期频繁失败且后台确认未通过，已尝试标记为临时不可调用')
  return true
}

function isAccountQualityFailurePrecheckEligible(account: AccountSummary | undefined): account is AccountSummary & { boundGroupId: string } {
  if (!account) return false
  if (account.status !== 'active' || !account.schedulable || !account.boundGroupId) return false
  if (account.accountExpiresAt) {
    const expiresAtMs = Date.parse(account.accountExpiresAt)
    if (Number.isFinite(expiresAtMs) && expiresAtMs <= Date.now()) return false
  }
  if (account.effectiveAvailability && !account.effectiveAvailability.available) return false
  return true
}

function accountQualityFailurePrecheckReason(
  item: AccountQualityFailurePrecheckQueueItem,
  result: AccountTestResult
): string {
  const parts = [
    `近期质量频繁失败，后台确认失败后标记为临时不可调用`,
    `近窗口 ${item.recentRequestCount} 次请求失败 ${item.recentErrorCount} 次`
  ]
  if (item.successRate !== undefined) {
    parts.push(`成功率 ${Math.round(item.successRate * 100)}%`)
  }
  if (item.lastErrorAt) {
    parts.push(`最后业务失败 ${item.lastErrorAt}`)
  }
  if (typeof result.statusCode === 'number') {
    parts.push(`确认 HTTP ${Math.trunc(result.statusCode)}`)
  }
  if (result.errorCode) {
    parts.push(result.errorCode)
  }
  if (result.message) {
    parts.push(result.message)
  } else if (item.lastErrorMessage) {
    parts.push(item.lastErrorMessage)
  }
  return parts.join('；').slice(0, 1000)
}

function wasRecentlyQualityFailurePrechecked(accountId: string): boolean {
  const checkedAtMs = recentQualityFailurePrechecks.get(accountId)
  return checkedAtMs !== undefined && Date.now() - checkedAtMs < recentQualityFailurePrecheckRetentionMs
}

function rememberQualityFailurePrechecked(accountId: string): void {
  recentQualityFailurePrechecks.set(accountId, Date.now())
}

function cleanupRecentQualityFailurePrechecks(): void {
  const cutoffMs = Date.now() - recentQualityFailurePrecheckRetentionMs
  for (const [accountId, checkedAtMs] of recentQualityFailurePrechecks) {
    if (checkedAtMs < cutoffMs) {
      recentQualityFailurePrechecks.delete(accountId)
    }
  }
}
