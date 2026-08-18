import type { AccountBalanceQueryConfig, AccountBalanceSnapshot } from '../accounts/account-balance.types.js'
import { runtimeConfig } from '../../config/runtime.js'
import {
  queryBuiltinAccountBalance,
  runWithAccountBalanceLease,
  type AccountBalanceBuiltinQueryResult
} from '../accounts/account-balance-query.service.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import { runWithGlobalBackgroundConcurrencySlot } from '../../shared/concurrency-governor.js'
import {
  commitAccountBalanceDetectionDueAsync,
  enableDetectedAccountBalanceQueryAsync,
  findAccountBalanceDetectionCandidateAsync,
  listAccountsDueForBalanceAutoDetectionAsync,
  replaceAccountBalanceSnapshotIfCurrentAsync,
  type AccountBalanceDetectionCandidate
} from '../../storage/account-balance.repository.js'
import { mainDatabaseRuntimeInfo } from '../../storage/database.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'
import { requestStatsWriter } from './background-stats-writer.js'
import { accountBalanceGoOwnerEnabled } from './account-balance-handover.js'

const detectionIntervalMinutes = 5
const detectionRetryMinutes = 5
const detectionRecoveryBatchSize = runtimeConfig.background.accountBalanceAutoDetectionRecoveryBatchSize

interface AccountBalanceAutoDetectionQueueItem {
  accountId: string
  configRevision: number
}

export interface AccountBalanceDetectionResult {
  config: AccountBalanceQueryConfig
  snapshot: AccountBalanceSnapshot
}

export type AccountBalanceAutoDetectionOutcome = 'enabled' | 'unsupported' | 'retry' | 'stale' | 'lease_busy'

type AccountBalanceDetectionAttempt =
  | { kind: 'matched'; detected: AccountBalanceDetectionResult }
  | { kind: 'unsupported' }
  | { kind: 'retry'; error?: unknown }

const accountBalanceAutoDetectionQueue = createRetryQueue<AccountBalanceAutoDetectionQueueItem>({
  name: 'account-balance-auto-detect',
  policy: sequenceRetryPolicy('account_balance_auto_detect', [], 0),
  concurrency: runtimeConfig.concurrency.globalMax,
  run: async (item) => {
    const candidate = await findAccountBalanceDetectionCandidateAsync(item.accountId, item.configRevision)
    if (!candidate?.nextRefreshAt) return true
    await autoDetectAccountBalanceCandidate(candidate)
    return true
  },
  onExhausted: (event) => {
    logger.warn(errorLogFields(event.error, {
      event: 'account_balance_auto_detect_exhausted',
      accountId: event.item.accountId
    }), 'AI 账户余额自动探测失败，本次保持关闭')
  }
})

export function enqueueAccountBalanceAutoDetection(accountId: string, configRevision: number): boolean {
  if (accountBalanceGoOwnerEnabled()) return false
  const normalizedId = accountId.trim()
  if (!normalizedId || !Number.isInteger(configRevision) || configRevision < 1) return false
  return accountBalanceAutoDetectionQueue.enqueue(normalizedId, {
    accountId: normalizedId,
    configRevision
  })
}

export function getAccountBalanceAutoDetectionQueueSnapshot() {
  return accountBalanceAutoDetectionQueue.snapshot()
}

export async function detectAccountBalanceAdapter(
  candidate: AccountBalanceDetectionCandidate,
  dependencies: {
    queryBuiltin?: (candidate: Parameters<typeof queryBuiltinAccountBalance>[0]) => Promise<AccountBalanceBuiltinQueryResult>
  } = {}
): Promise<AccountBalanceDetectionResult | undefined> {
  const attempt = await detectAccountBalanceAdapterAttempt(candidate, dependencies)
  return attempt.kind === 'matched' ? attempt.detected : undefined
}

async function detectAccountBalanceAdapterAttempt(
  candidate: AccountBalanceDetectionCandidate,
  dependencies: Parameters<typeof detectAccountBalanceAdapter>[1]
): Promise<AccountBalanceDetectionAttempt> {
  const config: AccountBalanceQueryConfig = { adapter: 'builtin', intervalMinutes: detectionIntervalMinutes }
  try {
    const result = await (dependencies?.queryBuiltin ?? queryBuiltinAccountBalance)({ ...candidate, config })
    if (result.snapshot.status === 'fresh' || result.snapshot.status === 'unlimited') {
      return {
        kind: 'matched',
        detected: {
          config: { ...config, preferredBuiltinAdapter: result.adapter },
          snapshot: result.snapshot
        }
      }
    }
    return result.snapshot.status === 'unsupported'
      ? { kind: 'unsupported' }
      : { kind: 'retry' }
  } catch (error) {
    return { kind: 'retry', error }
  }
}

export async function autoDetectAccountBalanceCandidate(
  candidate: AccountBalanceDetectionCandidate,
  dependencies: Parameters<typeof detectAccountBalanceAdapter>[1] = {}
): Promise<AccountBalanceAutoDetectionOutcome> {
  if (accountBalanceGoOwnerEnabled()) return 'stale'
  const lease = await runWithAccountBalanceLease(candidate, async () => (
    await autoDetectAccountBalanceCandidateWithLease(candidate, dependencies)
  ))
  return lease.acquired ? lease.value : 'lease_busy'
}

async function autoDetectAccountBalanceCandidateWithLease(
  candidate: AccountBalanceDetectionCandidate,
  dependencies: Parameters<typeof detectAccountBalanceAdapter>[1]
): Promise<Exclude<AccountBalanceAutoDetectionOutcome, 'lease_busy'>> {
  const attempt = await detectAccountBalanceAdapterAttempt(candidate, dependencies)
  if (attempt.kind === 'unsupported') {
    return await completeAccountBalanceDetectionIntent(candidate, null) ? 'unsupported' : 'stale'
  }
  if (attempt.kind === 'retry') {
    const retryAt = new Date(Date.now() + detectionRetryMinutes * 60_000).toISOString()
    const deferred = await completeAccountBalanceDetectionIntent(candidate, retryAt)
    if (deferred) {
      logger.warn(errorLogFields(attempt.error, {
        event: 'account_balance_auto_detect_deferred',
        accountId: candidate.id,
        systemAccountId: candidate.systemAccountId,
        retryAt
      }), 'AI 账户余额自动探测暂时失败，已保留后续重试')
      return 'retry'
    }
    return candidate.nextRefreshAt ? 'stale' : 'retry'
  }
  const detected = attempt.detected
  const completedAt = new Date().toISOString()
  const nextRefreshAt = new Date(Date.now() + detected.config.intervalMinutes * 60_000).toISOString()
  const enableInput = {
    accountId: candidate.id,
    expectedConfigRevision: candidate.configRevision,
    expectedNextRefreshAt: candidate.nextRefreshAt ?? undefined,
    config: detected.config,
    nextRefreshAt
  }
  const enabled = await enableDetectedBalanceQuery(enableInput)
  if (!enabled) return 'stale'
  const snapshotInput = {
    accountId: candidate.id,
    systemAccountId: candidate.systemAccountId,
    expectedConfigRevision: candidate.configRevision,
    expectedConfig: detected.config,
    snapshot: { ...detected.snapshot, lastAttemptAt: completedAt, lastSuccessAt: completedAt },
    nextRefreshAfter: nextRefreshAt
  }
  const written = runtimeConfig.databaseDriver === 'postgres' || !mainDatabaseRuntimeInfo('stats').queryOnly
    ? await replaceAccountBalanceSnapshotIfCurrentAsync(snapshotInput)
    : (await requestStatsWriter({ type: 'replace_account_balance_snapshot_if_current', input: snapshotInput })).written
  if (!written) return 'stale'
  logger.info({
    event: 'account_balance_auto_detect_enabled',
    accountId: candidate.id,
    systemAccountId: candidate.systemAccountId,
    adapter: detected.config.preferredBuiltinAdapter,
    proxyProfileId: candidate.proxyProfileId
  }, 'AI 账户余额接口探测成功，已自动开启')
  return 'enabled'
}

export interface AccountBalanceAutoDetectionRecoverySummary {
  outcome: 'success' | 'partial'
  selectedCount: number
  enabledCount: number
  unsupportedCount: number
  retryCount: number
  staleCount: number
  deferredCount: number
}

export interface AccountBalanceAutoDetectionRecoveryDependencies {
  listCandidates?: (options: { limit: number }) => Promise<AccountBalanceDetectionCandidate[]>
  autoDetect?: (candidate: AccountBalanceDetectionCandidate) => Promise<AccountBalanceAutoDetectionOutcome>
  signal?: AbortSignal
}

/**
 * The in-memory queue is a low-latency path. This bounded sweep is the
 * durable path: it claims intents persisted with the first health success
 * after worker restarts and retries transport failures without rescanning all
 * accounts.
 */
export async function runAccountBalanceAutoDetectionRecovery(
  dependencies: AccountBalanceAutoDetectionRecoveryDependencies = {}
): Promise<AccountBalanceAutoDetectionRecoverySummary> {
  if (accountBalanceGoOwnerEnabled()) {
    return { outcome: 'success', selectedCount: 0, enabledCount: 0, unsupportedCount: 0, retryCount: 0, staleCount: 0, deferredCount: 0 }
  }
  const candidates = await (dependencies.listCandidates ?? listAccountsDueForBalanceAutoDetectionAsync)({
    limit: detectionRecoveryBatchSize
  })
  const autoDetect = dependencies.autoDetect ?? autoDetectAccountBalanceCandidate
  let enabledCount = 0
  let unsupportedCount = 0
  let retryCount = 0
  let staleCount = 0
  let deferredCount = 0
  for (const candidate of candidates) {
    if (dependencies.signal?.aborted) {
      deferredCount += 1
      continue
    }
    const outcome = await autoDetect(candidate)
    if (outcome === 'enabled') enabledCount += 1
    else if (outcome === 'unsupported') unsupportedCount += 1
    else if (outcome === 'retry') retryCount += 1
    else if (outcome === 'lease_busy') deferredCount += 1
    else staleCount += 1
  }
  const partial = retryCount > 0 || staleCount > 0 || deferredCount > 0
  const summary: AccountBalanceAutoDetectionRecoverySummary = {
    outcome: partial ? 'partial' : 'success',
    selectedCount: candidates.length,
    enabledCount,
    unsupportedCount,
    retryCount,
    staleCount,
    deferredCount
  }
  logger.info({ event: 'account_balance_auto_detect_recovery_completed', ...summary }, 'AI 账户余额自动探测补偿完成')
  return summary
}

async function enableDetectedBalanceQuery(input: Parameters<typeof enableDetectedAccountBalanceQueryAsync>[0]): Promise<boolean> {
  if (runtimeConfig.databaseDriver === 'postgres' || !mainDatabaseRuntimeInfo('business').queryOnly) {
    return await enableDetectedAccountBalanceQueryAsync(input)
  }
  const result = await requestBackgroundWorkerDbService({ type: 'enable_detected_account_balance_query', input })
  return result?.changed === true
}

async function completeAccountBalanceDetectionIntent(
  candidate: AccountBalanceDetectionCandidate,
  nextRefreshAt: string | null
): Promise<boolean> {
  if (!candidate.nextRefreshAt) return true
  const input = {
    accountId: candidate.id,
    expectedConfigRevision: candidate.configRevision,
    expectedNextRefreshAt: candidate.nextRefreshAt,
    nextRefreshAt
  }
  if (runtimeConfig.databaseDriver === 'postgres' || !mainDatabaseRuntimeInfo('business').queryOnly) {
    return await commitAccountBalanceDetectionDueAsync(input)
  }
  const result = await requestBackgroundWorkerDbService({ type: 'commit_account_balance_refresh', input: { detectionIntent: true, ...input } })
  return result?.changed === true
}
