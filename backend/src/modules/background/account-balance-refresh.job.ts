import { errorLogFields, logger } from '../../shared/logger.js'
import {
  listAccountsDueForBalanceRefreshAsync,
  listAccountsNeedingBalanceRefreshRecoveryAsync,
  type AccountBalanceRefreshCandidate
} from '../../storage/account-balance.repository.js'
import { refreshAccountBalanceCandidate } from '../accounts/account-balance-query.service.js'

const refreshBatchSize = 12
const refreshConcurrency = 4
const recoveryBatchSize = 4
const refreshRunBudgetMs = 45_000
const refreshCandidateTimeoutMs = 20_000

export interface AccountBalanceRefreshRunSummary {
  selectedCount: number
  processedCount: number
  deferredCount: number
  infrastructureFailureCount: number
  durationMs: number
}

interface AccountBalanceRefreshDependencies {
  listRecoveryCandidates?: (options: { limit: number }) => Promise<AccountBalanceRefreshCandidate[]>
  listDueCandidates?: (options: { limit: number }) => Promise<AccountBalanceRefreshCandidate[]>
  refreshCandidate?: (candidate: AccountBalanceRefreshCandidate) => Promise<unknown>
  runBudgetMs?: number
  candidateTimeoutMs?: number
  now?: () => number
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
  const candidates = await (dependencies.listDueCandidates ?? listAccountsDueForBalanceRefreshAsync)({ limit: refreshBatchSize - recoveryCandidates.length })
  candidates.push(...recoveryCandidates)
  let cursor = 0
  let infrastructureFailureCount = 0
  let processedCount = 0
  await Promise.all(Array.from({ length: Math.min(refreshConcurrency, candidates.length) }, async () => {
    while (cursor < candidates.length) {
      const remainingRunMs = runBudgetMs - (now() - startedAtMs)
      if (remainingRunMs <= 0) return
      const candidate = candidates[cursor]
      cursor += 1
      try {
        await withTimeout(refreshCandidate(candidate), Math.min(candidateTimeoutMs, remainingRunMs), candidate.id)
        processedCount += 1
      } catch (error) {
        infrastructureFailureCount += 1
        logger.warn(errorLogFields(error, {
          event: 'account_balance_refresh_failed',
          accountId: candidate.id,
          systemAccountId: candidate.systemAccountId
        }), 'AI 账户上游余额刷新失败')
      }
    }
  }))
  const summary: AccountBalanceRefreshRunSummary = {
    selectedCount: candidates.length,
    processedCount,
    deferredCount: Math.max(0, candidates.length - cursor),
    infrastructureFailureCount,
    durationMs: Math.max(0, now() - startedAtMs)
  }
  if (infrastructureFailureCount > 0) {
    throw new Error(`AI 账户余额刷新存在 ${infrastructureFailureCount} 个基础设施失败；已处理 ${processedCount}/${candidates.length} 个候选`)
  }
  logger.info({ event: 'account_balance_refresh_completed', ...summary }, 'AI 账户上游余额刷新完成')
  return summary
}

async function withTimeout<T>(promise: Promise<T>, timeoutMs: number, accountId: string): Promise<T> {
  let timeout: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<never>((_resolve, reject) => {
        timeout = setTimeout(() => reject(new Error(`AI 账户余额刷新候选超时：${accountId}`)), Math.max(1, timeoutMs))
      })
    ])
  } finally {
    if (timeout) clearTimeout(timeout)
  }
}
