import { errorLogFields, logger } from '../../shared/logger.js'
import {
  listAccountsDueForBalanceRefreshAsync,
  listAccountsNeedingBalanceRefreshRecoveryAsync,
  type AccountBalanceRefreshCandidate
} from '../../storage/account-balance.repository.js'
import { refreshAccountBalanceCandidate } from '../accounts/account-balance-query.service.js'
import { loadAccountRuntimeAvailabilityByKeys } from '../gateway/runtime/runtime-snapshot.service.js'

const refreshBatchSize = 12
const refreshConcurrency = 4
const recoveryBatchSize = 4
const refreshRunBudgetMs = 45_000
const refreshCandidateTimeoutMs = 20_000

export interface AccountBalanceRefreshRunSummary {
  outcome: 'success' | 'partial'
  selectedCount: number
  processedCount: number
  deferredCount: number
  candidateFailureCount: number
  durationMs: number
  warning?: string
}

interface AccountBalanceRefreshDependencies {
  listRecoveryCandidates?: (options: { limit: number }) => Promise<AccountBalanceRefreshCandidate[]>
  listDueCandidates?: (options: { limit: number }) => Promise<AccountBalanceRefreshCandidate[]>
  refreshCandidate?: (candidate: AccountBalanceRefreshCandidate) => Promise<unknown>
  loadRuntimeAvailability?: typeof loadAccountRuntimeAvailabilityByKeys
  runBudgetMs?: number
  candidateTimeoutMs?: number
  now?: () => number
}

class AccountBalanceRefreshCandidateTimeoutError extends Error {
  constructor(accountId: string) {
    super(`AI 账户余额刷新候选超时：${accountId}`)
    this.name = 'AccountBalanceRefreshCandidateTimeoutError'
  }
}

export async function runAccountBalanceRefresh(
  dependencies: AccountBalanceRefreshDependencies = {}
): Promise<AccountBalanceRefreshRunSummary> {
  const now = dependencies.now ?? Date.now
  const runBudgetMs = dependencies.runBudgetMs ?? refreshRunBudgetMs
  const candidateTimeoutMs = dependencies.candidateTimeoutMs ?? refreshCandidateTimeoutMs
  const refreshCandidate = dependencies.refreshCandidate ?? refreshAccountBalanceCandidate
  const startedAtMs = now()
  const recoveryCandidates = await (dependencies.listRecoveryCandidates ?? listAccountsNeedingBalanceRefreshRecoveryAsync)({ limit: recoveryBatchSize })
  const selectedCandidates = await (dependencies.listDueCandidates ?? listAccountsDueForBalanceRefreshAsync)({ limit: refreshBatchSize - recoveryCandidates.length })
  selectedCandidates.push(...recoveryCandidates)
  const candidates = await filterCallableBalanceRefreshCandidates(
    selectedCandidates,
    dependencies.loadRuntimeAvailability ?? loadAccountRuntimeAvailabilityByKeys
  )
  const runtimeDeferredCount = Math.max(0, selectedCandidates.length - candidates.length)
  let cursor = 0
  let candidateFailureCount = 0
  let processedCount = 0
  let taskFailed = false
  let taskFailure: unknown
  await Promise.all(Array.from({ length: Math.min(refreshConcurrency, candidates.length) }, async () => {
    while (cursor < candidates.length) {
      if (taskFailed) return
      const remainingRunMs = runBudgetMs - (now() - startedAtMs)
      if (remainingRunMs <= 0) return
      const candidate = candidates[cursor]
      cursor += 1
      try {
        await withTimeout(refreshCandidate(candidate), Math.min(candidateTimeoutMs, remainingRunMs), candidate.id)
        processedCount += 1
      } catch (error) {
        if (!(error instanceof AccountBalanceRefreshCandidateTimeoutError)) {
          taskFailed = true
          taskFailure = error
          return
        }
        candidateFailureCount += 1
        logger.warn(errorLogFields(error, {
          event: 'account_balance_refresh_failed',
          accountId: candidate.id,
          systemAccountId: candidate.systemAccountId
        }), 'AI 账户上游余额刷新失败')
      }
    }
  }))
  if (taskFailed) throw taskFailure
  const summary: AccountBalanceRefreshRunSummary = {
    outcome: candidateFailureCount > 0 ? 'partial' : 'success',
    selectedCount: selectedCandidates.length,
    processedCount,
    deferredCount: runtimeDeferredCount + Math.max(0, candidates.length - cursor),
    candidateFailureCount,
    durationMs: Math.max(0, now() - startedAtMs),
    ...(candidateFailureCount > 0
      ? { warning: `AI 账户余额刷新部分失败：${candidateFailureCount} 个候选失败，已完成 ${processedCount}/${candidates.length}` }
      : {})
  }
  if (summary.outcome === 'partial') {
    logger.warn({ event: 'account_balance_refresh_partial', ...summary }, summary.warning)
    return summary
  }
  logger.info({ event: 'account_balance_refresh_completed', ...summary }, 'AI 账户上游余额刷新完成')
  return summary
}

async function filterCallableBalanceRefreshCandidates(
  candidates: AccountBalanceRefreshCandidate[],
  loadRuntimeAvailability: typeof loadAccountRuntimeAvailabilityByKeys
): Promise<AccountBalanceRefreshCandidate[]> {
  if (candidates.length === 0) return candidates
  const runtime = await loadRuntimeAvailability(candidates.map((candidate) => candidate.id))
  if (!runtime.available) return candidates
  return candidates.filter((candidate) => {
    const status = runtime.values[candidate.id]?.status
    return status === undefined || status === 'normal' || status === 'degraded'
  })
}

async function withTimeout<T>(promise: Promise<T>, timeoutMs: number, accountId: string): Promise<T> {
  let timeout: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<never>((_resolve, reject) => {
        timeout = setTimeout(() => reject(new AccountBalanceRefreshCandidateTimeoutError(accountId)), Math.max(1, timeoutMs))
      })
    ])
  } finally {
    if (timeout) clearTimeout(timeout)
  }
}
