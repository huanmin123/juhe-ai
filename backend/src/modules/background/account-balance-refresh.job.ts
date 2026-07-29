import { errorLogFields, logger } from '../../shared/logger.js'
import {
  listAccountsDueForBalanceRefreshAsync,
  listAccountsNeedingBalanceRefreshRecoveryAsync,
  type AccountBalanceRefreshCandidate
} from '../../storage/account-balance.repository.js'
import {
  deferAccountBalanceRefreshCandidate,
  refreshAccountBalanceCandidateWithOutcome,
  type AccountBalanceRefreshOutcome,
  type AccountBalanceRefreshResult
} from '../accounts/account-balance-query.service.js'
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
  refreshedCount: number
  leaseBusyCount: number
  staleCount: number
  failedCount: number
  unsupportedCount: number
  durationMs: number
  warning?: string
}

interface AccountBalanceRefreshDependencies {
  listRecoveryCandidates?: (options: { limit: number }) => Promise<AccountBalanceRefreshCandidate[]>
  listDueCandidates?: (options: { limit: number }) => Promise<AccountBalanceRefreshCandidate[]>
  refreshCandidate?: (
    candidate: AccountBalanceRefreshCandidate,
    context: { signal: AbortSignal; deadlineAtMs: number }
  ) => Promise<AccountBalanceRefreshResult | unknown>
  deferCandidate?: (candidate: AccountBalanceRefreshCandidate) => Promise<boolean>
  loadRuntimeAvailability?: typeof loadAccountRuntimeAvailabilityByKeys
  runBudgetMs?: number
  candidateTimeoutMs?: number
  now?: () => number
  signal?: AbortSignal
}

class AccountBalanceRefreshCandidateTimeoutError extends Error {
  constructor(accountId: string) {
    super(`AI 账户余额刷新候选超时：${accountId}`)
    this.name = 'AccountBalanceRefreshCandidateTimeoutError'
  }
}

export async function runAccountBalanceRefresh(
  dependencies: AccountBalanceRefreshDependencies = {},
  signal?: AbortSignal
): Promise<AccountBalanceRefreshRunSummary> {
  const runSignal = signal ?? dependencies.signal
  const now = dependencies.now ?? Date.now
  const runBudgetMs = dependencies.runBudgetMs ?? refreshRunBudgetMs
  const candidateTimeoutMs = dependencies.candidateTimeoutMs ?? refreshCandidateTimeoutMs
  const refreshCandidate = dependencies.refreshCandidate ?? refreshAccountBalanceCandidateWithOutcome
  const deferCandidate = dependencies.deferCandidate ?? deferAccountBalanceRefreshCandidate
  const startedAtMs = now()
  const recoveryCandidates = await (dependencies.listRecoveryCandidates ?? listAccountsNeedingBalanceRefreshRecoveryAsync)({ limit: recoveryBatchSize })
  const selectedCandidates = await (dependencies.listDueCandidates ?? listAccountsDueForBalanceRefreshAsync)({ limit: refreshBatchSize - recoveryCandidates.length })
  selectedCandidates.push(...recoveryCandidates)
  const { callableCandidates: candidates, deferredCandidates } = await partitionCallableBalanceRefreshCandidates(
    selectedCandidates,
    dependencies.loadRuntimeAvailability ?? loadAccountRuntimeAvailabilityByKeys
  )
  await Promise.all(deferredCandidates.map(async (candidate) => {
    await deferCandidate(candidate).catch(() => false)
  }))
  const runtimeDeferredCount = deferredCandidates.length
  let cursor = 0
  let processedCount = 0
  const outcomeCounts: Record<AccountBalanceRefreshOutcome, number> = {
    refreshed: 0,
    lease_busy: 0,
    stale: 0,
    failed: 0,
    unsupported: 0
  }
  await Promise.all(Array.from({ length: Math.min(refreshConcurrency, candidates.length) }, async () => {
    while (cursor < candidates.length) {
      if (runSignal?.aborted) return
      const remainingRunMs = runBudgetMs - (now() - startedAtMs)
      if (remainingRunMs <= 0) return
      const candidate = candidates[cursor]
      cursor += 1
      const candidateDeadlineAtMs = Math.min(startedAtMs + runBudgetMs, now() + candidateTimeoutMs)
      const candidateDeadlineMs = Math.max(1, candidateDeadlineAtMs - now())
      const candidateController = new AbortController()
      const abortCandidate = () => candidateController.abort(runSignal?.reason)
      runSignal?.addEventListener('abort', abortCandidate, { once: true })
      if (runSignal?.aborted) abortCandidate()
      const candidateTimeout = setTimeout(
        () => candidateController.abort(new AccountBalanceRefreshCandidateTimeoutError(candidate.id)),
        candidateDeadlineMs
      )
      try {
        const result = await refreshCandidate(candidate, {
          signal: candidateController.signal,
          deadlineAtMs: candidateDeadlineAtMs
        })
        const outcome = accountBalanceRefreshCandidateOutcome(result)
        outcomeCounts[outcome] += 1
        processedCount += 1
      } catch (error) {
        const outcome: AccountBalanceRefreshOutcome = candidateController.signal.aborted ? 'stale' : 'failed'
        outcomeCounts[outcome] += 1
        processedCount += 1
        logger.warn(errorLogFields(error, {
          event: 'account_balance_refresh_failed',
          accountId: candidate.id,
          systemAccountId: candidate.systemAccountId,
          outcome
        }), 'AI 账户上游余额刷新失败')
      } finally {
        clearTimeout(candidateTimeout)
        runSignal?.removeEventListener('abort', abortCandidate)
      }
    }
  }))
  const candidateFailureCount = outcomeCounts.stale + outcomeCounts.failed
  const deferredCount = runtimeDeferredCount + Math.max(0, candidates.length - cursor)
  const partial = candidateFailureCount > 0 || outcomeCounts.lease_busy > 0 || deferredCount > 0
  const summary: AccountBalanceRefreshRunSummary = {
    outcome: partial ? 'partial' : 'success',
    selectedCount: selectedCandidates.length,
    processedCount,
    deferredCount,
    candidateFailureCount,
    refreshedCount: outcomeCounts.refreshed,
    leaseBusyCount: outcomeCounts.lease_busy,
    staleCount: outcomeCounts.stale,
    failedCount: outcomeCounts.failed,
    unsupportedCount: outcomeCounts.unsupported,
    durationMs: Math.max(0, now() - startedAtMs),
    ...(partial
      ? { warning: `AI 账户余额刷新部分完成：失败 ${candidateFailureCount}，租约占用 ${outcomeCounts.lease_busy}，延期 ${deferredCount}` }
      : {})
  }
  if (summary.outcome === 'partial') {
    logger.warn({ event: 'account_balance_refresh_partial', ...summary }, summary.warning)
    return summary
  }
  logger.info({ event: 'account_balance_refresh_completed', ...summary }, 'AI 账户上游余额刷新完成')
  return summary
}

async function partitionCallableBalanceRefreshCandidates(
  candidates: AccountBalanceRefreshCandidate[],
  loadRuntimeAvailability: typeof loadAccountRuntimeAvailabilityByKeys
): Promise<{ callableCandidates: AccountBalanceRefreshCandidate[]; deferredCandidates: AccountBalanceRefreshCandidate[] }> {
  if (candidates.length === 0) return { callableCandidates: candidates, deferredCandidates: [] }
  const runtime = await loadRuntimeAvailability(candidates.map((candidate) => candidate.id))
  if (!runtime.available) return { callableCandidates: candidates, deferredCandidates: [] }
  const callableCandidates = candidates.filter((candidate) => {
    const status = runtime.values[candidate.id]?.status
    return status === undefined || status === 'normal' || status === 'degraded'
  })
  const callableIds = new Set(callableCandidates.map((candidate) => candidate.id))
  return {
    callableCandidates,
    deferredCandidates: candidates.filter((candidate) => !callableIds.has(candidate.id))
  }
}

function accountBalanceRefreshCandidateOutcome(result: unknown): AccountBalanceRefreshOutcome {
  if (!result || typeof result !== 'object') return 'refreshed'
  const outcome = (result as { outcome?: unknown }).outcome
  return outcome === 'refreshed'
    || outcome === 'lease_busy'
    || outcome === 'stale'
    || outcome === 'failed'
    || outcome === 'unsupported'
    ? outcome
    : 'refreshed'
}
