import {
  findAccountBalanceRefreshCandidateAsync,
  findAccountBalanceManualRefreshCandidateAsync,
  findAccountBalanceDetectionCandidateAsync,
  persistAccountBalanceRefreshWithSnapshotAsync,
  commitAccountBalanceDetectionDueAsync,
  replaceAccountBalanceSnapshotAsync,
  enableDetectedAccountBalanceQueryWithSnapshotAsync
} from '../../storage/account-balance.repository.js'
import { listAccountBalanceJobsOutcomes, type AccountBalanceJobsOutcome, type AccountBalanceJobsOutcomeCursor, type AccountBalanceJobsStoreSource } from '../../storage/account-balance-jobs-outcome.repository.js'
import type { AccountBalanceQueryConfig, AccountBalanceSnapshot } from '../accounts/account-balance.types.js'
import { normalizeAccountBalanceConfig } from '../accounts/account-balance-config.js'
import { requiredRfc3339Instant } from '../../shared/rfc3339.js'

export interface AccountBalanceJobsProjectionResult { projected: boolean; reason?: 'stale' | 'unsupported' | 'not_found' | 'invalid_fence' }

/** Read-only jobs outcome consumer. It is intentionally not registered with
 * the existing Node scheduler; callers enable it only after Go owner/drain
 * gates are verified. Business and stats writes remain Node-owned. */
export async function projectAccountBalanceJobsOutcome(outcome: AccountBalanceJobsOutcome): Promise<AccountBalanceJobsProjectionResult> {
  if (outcome.trigger === 'first_probe') return await projectFirstProbe(outcome)
  const candidate = outcome.trigger === 'manual'
    ? await findAccountBalanceManualRefreshCandidateAsync(outcome.accountId)
    : await findAccountBalanceRefreshCandidateAsync(outcome.accountId)
  if (!candidate || candidate.systemAccountId !== outcome.systemAccountId || candidate.configRevision !== outcome.configRevision) return { projected: false, reason: 'stale' }
  const candidateInputVersion = candidate.inputVersion ?? 0
  if (candidateInputVersion > 0 && candidateInputVersion !== outcome.inputVersion) return { projected: false, reason: 'stale' }
  if (outcome.expectedInput !== undefined && candidateInputVersion > 0 && candidateInputVersion !== outcome.expectedInput) return { projected: false, reason: 'stale' }
  if (outcome.expectedConfig !== undefined && candidate.configRevision !== outcome.expectedConfig) return { projected: false, reason: 'stale' }
  if (outcome.trigger !== 'manual' && outcome.expectedNextRefreshAt !== undefined && canonical(outcome.expectedNextRefreshAt) !== canonical(candidate.nextRefreshAt)) return { projected: false, reason: 'stale' }
  const snapshot = normalizeSnapshot(outcome.snapshot)
  const nextRefreshAt = outcome.nextRefreshAt
  const nextConfig = projectedRefreshConfig(candidate.config, outcome.adapter)
  // Legacy Node manual refresh deliberately does not fence the scheduled due
  // value; only periodic Go outcomes carry an expected due fence.
  const committed = await persistAccountBalanceRefreshWithSnapshotAsync({ accountId: outcome.accountId, systemAccountId: outcome.systemAccountId, expectedConfigRevision: outcome.configRevision, expectedConfig: normalizeAccountBalanceConfig(candidate.config), expectedNextRefreshAt: outcome.trigger === 'manual' ? undefined : candidate.nextRefreshAt, nextConfig, nextRefreshAt, snapshot })
  return committed ? { projected: true } : { projected: false, reason: 'stale' }
}

async function projectFirstProbe(outcome: AccountBalanceJobsOutcome): Promise<AccountBalanceJobsProjectionResult> {
  const candidate = await findAccountBalanceDetectionCandidateAsync(outcome.accountId, outcome.configRevision)
  if (!candidate || candidate.systemAccountId !== outcome.systemAccountId || !candidate.nextRefreshAt) return { projected: false, reason: 'stale' }
  const candidateInputVersion = candidate.inputVersion ?? 0
  if (candidateInputVersion > 0 && candidateInputVersion !== outcome.inputVersion) return { projected: false, reason: 'stale' }
  if (outcome.expectedInput !== undefined && candidateInputVersion > 0 && candidateInputVersion !== outcome.expectedInput) return { projected: false, reason: 'stale' }
  if (outcome.expectedConfig !== undefined && candidate.configRevision !== outcome.expectedConfig) return { projected: false, reason: 'stale' }
  if (outcome.expectedNextRefreshAt !== undefined && canonical(outcome.expectedNextRefreshAt) !== canonical(candidate.nextRefreshAt)) return { projected: false, reason: 'stale' }
  const snapshot = normalizeSnapshot(outcome.snapshot)
  if (snapshot.status === 'unsupported') {
    const changed = await commitAccountBalanceDetectionDueAsync({ accountId: outcome.accountId, expectedConfigRevision: outcome.configRevision, expectedNextRefreshAt: candidate.nextRefreshAt, nextRefreshAt: null })
    return changed ? { projected: true } : { projected: false, reason: 'stale' }
  }
  if (snapshot.status !== 'fresh' && snapshot.status !== 'unlimited') {
    const retryAt = outcome.nextRefreshAt ?? candidate.nextRefreshAt
    const deferred = await commitAccountBalanceDetectionDueAsync({ accountId: outcome.accountId, expectedConfigRevision: outcome.configRevision, expectedNextRefreshAt: candidate.nextRefreshAt, nextRefreshAt: retryAt })
    return deferred ? { projected: true } : { projected: false, reason: 'stale' }
  }
  const nextRefreshAt = outcome.nextRefreshAt
  if (!nextRefreshAt) return { projected: false, reason: 'invalid_fence' }
  const config = { adapter: 'builtin' as const, intervalMinutes: 5, ...(outcome.adapter ? { preferredBuiltinAdapter: outcome.adapter as 'sub2api' | 'newapi' | 'openai_billing' | 'litellm' | 'user_balance' } : {}) }
  const enabled = await enableDetectedAccountBalanceQueryWithSnapshotAsync({ accountId: outcome.accountId, systemAccountId: outcome.systemAccountId, expectedConfigRevision: outcome.configRevision, expectedNextRefreshAt: candidate.nextRefreshAt, config, nextRefreshAt, snapshot })
  return enabled ? { projected: true } : { projected: false, reason: 'stale' }
}

export async function drainAccountBalanceJobsOutcomesOnce(source: AccountBalanceJobsStoreSource, after: AccountBalanceJobsOutcomeCursor | undefined, limit: number): Promise<{ cursor?: AccountBalanceJobsOutcomeCursor; projected: number }> {
  const outcomes = await listAccountBalanceJobsOutcomes(source, { ...(after ? { after } : {}), limit })
  let cursor = after
  let projected = 0
  for (const outcome of outcomes) {
    const result = await projectAccountBalanceJobsOutcome(outcome)
    if (!result.projected && result.reason !== 'stale') break
    cursor = { observedAt: outcome.storageObservedAt, outcomeId: outcome.outcomeId }
    if (result.projected) projected += 1
  }
  return { ...(cursor ? { cursor } : {}), projected }
}

function normalizeSnapshot(value: Record<string, unknown>): AccountBalanceSnapshot {
  const status = value.status
  if (status !== 'pending' && status !== 'refreshing' && status !== 'fresh' && status !== 'unlimited' && status !== 'unsupported' && status !== 'failed') throw new Error('J2 snapshot status 无效')
  return {
    status,
    ...(typeof value.remainingUsd === 'string' ? { remainingUsd: value.remainingUsd } : {}),
    ...(typeof value.rawRemaining === 'string' ? { rawRemaining: value.rawRemaining } : {}),
    ...(value.rawUnit === 'usd' || value.rawUnit === 'cny' || value.rawUnit === 'quota' ? { rawUnit: value.rawUnit } : {}),
    ...(typeof value.basis === 'string' ? { basis: value.basis as AccountBalanceSnapshot['basis'] } : {}),
    ...(typeof value.errorMessage === 'string' ? { errorMessage: value.errorMessage } : {}),
    ...(typeof value.lastAttemptAt === 'string' ? { lastAttemptAt: value.lastAttemptAt } : {}),
    ...(typeof value.lastSuccessAt === 'string' ? { lastSuccessAt: value.lastSuccessAt } : {}),
    ...(Number.isInteger(value.consecutiveTransientFailures) ? { consecutiveTransientFailures: value.consecutiveTransientFailures as number } : {}),
    ...(typeof value.lastTransientErrorMessage === 'string' ? { lastTransientErrorMessage: value.lastTransientErrorMessage } : {}),
    ...(typeof value.lastTransientFailureAt === 'string' ? { lastTransientFailureAt: value.lastTransientFailureAt } : {})
  }
}

function canonical(value: string | null | undefined): string | null | undefined {
  return value === undefined || value === null ? value : requiredRfc3339Instant(value)
}

function projectedRefreshConfig(config: AccountBalanceQueryConfig, adapter: AccountBalanceJobsOutcome['adapter']): AccountBalanceQueryConfig {
  const normalized = normalizeAccountBalanceConfig(config)
  if (normalized.adapter !== 'builtin' || !isBuiltinAdapter(adapter)) return normalized
  return { ...normalized, preferredBuiltinAdapter: adapter }
}

function isBuiltinAdapter(adapter: AccountBalanceJobsOutcome['adapter']): adapter is 'sub2api' | 'newapi' | 'openai_billing' | 'litellm' | 'user_balance' {
  return adapter === 'sub2api' || adapter === 'newapi' || adapter === 'openai_billing' || adapter === 'litellm' || adapter === 'user_balance'
}
