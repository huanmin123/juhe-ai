import type { AccountBalanceQueryConfig, AccountBalanceSnapshot } from '../accounts/account-balance.types.js'
import { runtimeConfig } from '../../config/runtime.js'
import { queryBuiltinAccountBalance, type AccountBalanceBuiltinQueryResult } from '../accounts/account-balance-query.service.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { createRetryQueue } from '../../shared/retry-queue.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'
import {
  enableDetectedAccountBalanceQueryAsync,
  findAccountBalanceDetectionCandidateAsync,
  replaceAccountBalanceSnapshotIfCurrentAsync,
  type AccountBalanceDetectionCandidate
} from '../../storage/account-balance.repository.js'
import { mainDatabaseRuntimeInfo } from '../../storage/database.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'
import { requestStatsWriter } from './background-stats-writer.js'

const detectionIntervalMinutes = 5

interface AccountBalanceAutoDetectionQueueItem {
  accountId: string
  configRevision: number
}

export interface AccountBalanceDetectionResult {
  config: AccountBalanceQueryConfig
  snapshot: AccountBalanceSnapshot
}

const accountBalanceAutoDetectionQueue = createRetryQueue<AccountBalanceAutoDetectionQueueItem>({
  name: 'account-balance-auto-detect',
  policy: sequenceRetryPolicy('account_balance_auto_detect', [], 0),
  concurrency: 2,
  run: async (item) => {
    const candidate = await findAccountBalanceDetectionCandidateAsync(item.accountId, item.configRevision)
    if (!candidate) return true
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
  const config: AccountBalanceQueryConfig = { adapter: 'builtin', intervalMinutes: detectionIntervalMinutes }
  try {
    const result = await (dependencies.queryBuiltin ?? queryBuiltinAccountBalance)({ ...candidate, config })
    return {
      config: { ...config, preferredBuiltinAdapter: result.adapter },
      snapshot: result.snapshot
    }
  } catch {
    return undefined
  }
}

export async function autoDetectAccountBalanceCandidate(
  candidate: AccountBalanceDetectionCandidate,
  dependencies: Parameters<typeof detectAccountBalanceAdapter>[1] = {}
): Promise<'enabled' | 'unsupported' | 'stale'> {
  const detected = await detectAccountBalanceAdapter(candidate, dependencies)
  if (!detected) return 'unsupported'
  const completedAt = new Date().toISOString()
  const nextRefreshAt = new Date(Date.now() + detected.config.intervalMinutes * 60_000).toISOString()
  const enableInput = {
    accountId: candidate.id,
    expectedConfigRevision: candidate.configRevision,
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

async function enableDetectedBalanceQuery(input: Parameters<typeof enableDetectedAccountBalanceQueryAsync>[0]): Promise<boolean> {
  if (runtimeConfig.databaseDriver === 'postgres' || !mainDatabaseRuntimeInfo('business').queryOnly) {
    return await enableDetectedAccountBalanceQueryAsync(input)
  }
  const result = await requestBackgroundWorkerDbService({ type: 'enable_detected_account_balance_query', input })
  return result?.changed === true
}
