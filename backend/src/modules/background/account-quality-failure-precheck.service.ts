import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import type { AccountQualityFailurePrecheckCandidate } from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { testOpenAIAccountWithDiagnosticRetries } from '../accounts/account-test.service.js'
import { isDiagnosticTimeoutSignal } from '../accounts/account-diagnostic-retry-policy.js'
import {
  automaticAccountAvailabilityProbeFailed,
  automaticAccountProbeOutcome
} from '../accounts/automatic-account-probe-outcome.js'
import type { UpstreamAttempt } from '../gateway/upstream/attempt.js'
import { gatewayAccountRuntimeKey } from '../gateway/runtime/account-runtime-keys.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'
import {
  backgroundProbeDbServiceTimeoutMs,
  runWithBackgroundAccountAvailabilityProbe,
  runWithBackgroundFullDiagnosticSlot
} from './account-probe-limits.js'

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
  run: (item, context) => runWithBackgroundFullDiagnosticSlot(() => runAccountQualityFailurePrecheckQueueItem(item, context)),
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

export function setAccountQualityFailurePrecheckQueueConcurrency(concurrency: number): void {
  accountQualityFailurePrecheckQueue.setConcurrency(concurrency)
}

async function runAccountQualityFailurePrecheckQueueItem(
  item: AccountQualityFailurePrecheckQueueItem,
  context: { attemptIndex: number; retryNumber: number }
) {
  const accountAccess = { systemAccountId: item.systemAccountId, role: 'user' as const }
  const account = await loadAccountForTestViaDbService(item.accountId, accountAccess)
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
  const candidateAccount = await loadOpenAIAccountForGroupViaDbService(groupId, account.id, item.systemAccountId, {
    includeUnavailable: true,
    ignoreAvailability: true
  })
  const expectedDispatchRevision = candidateAccount?.dispatchRevision
  if (
    !candidateAccount
    || candidateAccount.status !== 'active'
    || !Number.isSafeInteger(expectedDispatchRevision)
    || expectedDispatchRevision! < 1
  ) {
    rememberQualityFailurePrechecked(item.accountId)
    logger.warn({
      event: 'background_account_quality_failure_precheck_discarded',
      accountId: account.id,
      accountName: account.name,
      dispatchRevision: expectedDispatchRevision
    }, '账户质量失败确认缺少当前调度代次，已跳过')
    return true
  }

  const precheckStartedAt = new Date().toISOString()
  return await runWithBackgroundAccountAvailabilityProbe(gatewayAccountRuntimeKey(account), async () => {
    let upstreamAttempt: UpstreamAttempt | undefined
    let diagnosticTimedOut = false
    const result = await testOpenAIAccountWithDiagnosticRetries(account, {
      diagnostics: 'full',
      groupId,
      systemAccountId: item.systemAccountId,
      trafficSource: 'runtime_recovery_probe',
      testEndpointMode: account.healthCheckEndpointMode,
      disableAccountStateMutation: true,
      retryAllFailures: true,
      onDiagnosticAttemptProgress: () => {
        upstreamAttempt = undefined
      },
      onDiagnosticAttemptResult: ({ signal }) => {
        diagnosticTimedOut = isDiagnosticTimeoutSignal(signal)
      },
      onUpstreamAttempt: (attempt) => {
        upstreamAttempt = attempt
      },
      candidateAccount,
      findAccountForTest: loadAccountForTestViaDbService,
      findOpenAIAccountForGroup: loadOpenAIAccountForGroupViaDbService,
      gatewaySettingsOverride: {
        temporaryUnschedulableRetryAttempts: 0,
        temporaryUnschedulableRetryIntervalSeconds: 0
      }
    })
    return { result, upstreamAttempt, diagnosticTimedOut }
  }, async ({ result, upstreamAttempt, diagnosticTimedOut }, { joined }) => {
    if (joined) {
      logger.debug({
        event: 'background_account_quality_failure_precheck_singleflight_joined',
        accountId: item.accountId,
        accountName: account.name
      }, '同一账户已有可用性探针执行，本轮质量失败确认复用其结果')
    }
    rememberQualityFailurePrechecked(item.accountId)
    const probeOutcome = automaticAccountProbeOutcome(result, { upstreamAttempt, timeout: diagnosticTimedOut })

    if (probeOutcome === 'complete_success') {
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

    if (!automaticAccountAvailabilityProbeFailed(probeOutcome)) {
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
      }, '账户近期频繁失败但后台探针未形成有效上游可用性结论，已跳过状态写入')
      return true
    }

    const updated = await requestBackgroundWorkerDbService({
      type: 'mark_account_precheck_temporary_unavailable',
      account: candidateAccount,
      reason: accountQualityFailurePrecheckReason(item, result),
      precheckStartedAt,
      expectedDispatchRevision: expectedDispatchRevision!,
      expectedStatus: 'active'
    }, backgroundProbeDbServiceTimeoutMs)
    logger.warn({
      event: 'background_account_quality_failure_precheck_marked',
      accountId: account.id,
      accountName: account.name,
      statusCode: result.statusCode,
      errorCode: result.errorCode,
      durationMs: result.durationMs,
      recentRequestCount: item.recentRequestCount,
      recentErrorCount: item.recentErrorCount,
      updated: updated?.updated ?? false,
      skippedReason: updated?.skippedReason
    }, '账户近期频繁失败且后台确认未通过，已尝试标记为临时不可调用')
    return true
  })
}

async function loadAccountForTestViaDbService(accountId: string, access?: AccessScope) {
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
