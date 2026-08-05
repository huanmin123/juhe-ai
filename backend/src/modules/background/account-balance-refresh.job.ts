import { errorLogFields, logger } from '../../shared/logger.js'
import { runtimeConfig } from '../../config/runtime.js'
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
import { UpstreamRequestAbortedError } from '../gateway/upstream/request.js'
import { loadAccountRuntimeAvailabilityByKeys } from '../gateway/runtime/runtime-snapshot.service.js'
import { runWithGlobalBackgroundConcurrencySlot } from '../../shared/concurrency-governor.js'

const refreshBatchSize = runtimeConfig.background.accountBalanceRefreshBatchSize
const refreshConcurrency = runtimeConfig.concurrency.globalMax
const recoveryBatchSize = runtimeConfig.background.accountBalanceRefreshRecoveryBatchSize
const refreshRunBudgetMs = 45_000
const refreshCandidateTimeoutMs = 20_000

export interface AccountBalanceRefreshRunSummary {
  outcome: 'success'
  selectedCount: number
  processedCount: number
  deferredCount: number
  diagnosticCount: number
  unfinishedCount: number
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
    await deferCandidate(candidate)
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
        if (!isCandidateTimeoutAbort(error, candidateController.signal.reason)) throw error
        outcomeCounts.stale += 1
        processedCount += 1
        logger.info(errorLogFields(error, {
          event: 'account_balance_refresh_candidate_diagnostic',
          accountId: candidate.id,
          systemAccountId: candidate.systemAccountId,
          outcome: 'stale'
        }), 'AI 账户上游余额查询在候选截止前未完成')
      } finally {
        clearTimeout(candidateTimeout)
        runSignal?.removeEventListener('abort', abortCandidate)
      }
    }
  }))
  const diagnosticCount = outcomeCounts.stale + outcomeCounts.failed + outcomeCounts.unsupported
  const deferredCount = runtimeDeferredCount + Math.max(0, candidates.length - cursor)
  const unfinishedCount = outcomeCounts.lease_busy + deferredCount
  const summary: AccountBalanceRefreshRunSummary = {
    outcome: 'success',
    selectedCount: selectedCandidates.length,
    processedCount,
    deferredCount,
    diagnosticCount,
    unfinishedCount,
    refreshedCount: outcomeCounts.refreshed,
    leaseBusyCount: outcomeCounts.lease_busy,
    staleCount: outcomeCounts.stale,
    failedCount: outcomeCounts.failed,
    unsupportedCount: outcomeCounts.unsupported,
    durationMs: Math.max(0, now() - startedAtMs)
  }
  logger.info({ event: 'account_balance_refresh_completed', ...summary }, 'AI 账户上游余额刷新轮次完成')
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
  if (!result || typeof result !== 'object') {
    throw new Error('AI 账户余额刷新候选返回结果无效：必须返回包含已知 outcome 的对象')
  }
  const outcome = (result as { outcome?: unknown }).outcome
  return outcome === 'refreshed'
    || outcome === 'lease_busy'
    || outcome === 'stale'
    || outcome === 'failed'
    || outcome === 'unsupported'
    ? outcome
    : invalidAccountBalanceRefreshCandidateOutcome()
}

function isCandidateTimeout(reason: unknown): reason is AccountBalanceRefreshCandidateTimeoutError {
  return reason instanceof AccountBalanceRefreshCandidateTimeoutError
}

function isCandidateTimeoutAbort(error: unknown, reason: unknown): boolean {
  if (!isCandidateTimeout(reason)) return false
  return error === reason
    || error instanceof UpstreamRequestAbortedError
    || (error instanceof DOMException && error.name === 'AbortError')
}

function invalidAccountBalanceRefreshCandidateOutcome(): never {
  throw new Error('AI 账户余额刷新候选返回结果无效：outcome 未知或缺失')
}
