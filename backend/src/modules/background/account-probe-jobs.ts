import { errorLogFields, logger } from '../../shared/logger.js'
import {
  listAccountQualityFailurePrecheckCandidates,
  listAccountsDueForCooldownRetest,
  refreshAccountQualityFromUsage
} from '../../storage/repositories.js'
import { listAccountApiKeyRuntimeStatesDueForProbe } from '../../storage/account-api-key-runtime-state.repository.js'
import { clearGatewayRuntimeCache } from '../gateway/runtime/runtime-cache.service.js'
import { enqueueAccountApiKeyCooldownRetest, getAccountApiKeyCooldownRetestQueueSnapshot } from './account-api-key-cooldown-retest.service.js'
import { enqueueAccountQualityFailurePrecheck, getAccountQualityFailurePrecheckQueueSnapshot } from './account-quality-failure-precheck.service.js'
import { enqueueCooldownAccountRetest, getCooldownAccountRetestQueueSnapshot } from './cooldown-account-retest.service.js'

const accountQualityFailurePrecheckBatchSize = 10

type SettingsNumberReader = (key: string, min: number, max: number) => number

interface AccountQualityRefreshDeps {
  settingsNumber: SettingsNumberReader
  ensureUsageRecordsIngestedBeforeStatsAggregation: () => Promise<void>
  yieldToEventLoop: () => Promise<void>
}

interface AccountRetestDeps {
  settingsNumber: SettingsNumberReader
}

export async function runAccountQualityRefresh(deps: AccountQualityRefreshDeps): Promise<void> {
  try {
    await deps.ensureUsageRecordsIngestedBeforeStatsAggregation()
    await deps.yieldToEventLoop()
    const windowMinutes = deps.settingsNumber('accountQualityWindowMinutes', 1, 60)
    const realtimeResult = refreshAccountQualityFromUsage(windowMinutes)
    const failureCandidates = listAccountQualityFailurePrecheckCandidates(accountQualityFailurePrecheckBatchSize)
    let failurePrecheckEnqueuedCount = 0
    let failurePrecheckSkippedQueuedCount = 0
    for (const candidate of failureCandidates) {
      if (enqueueAccountQualityFailurePrecheck(candidate)) {
        failurePrecheckEnqueuedCount += 1
      } else {
        failurePrecheckSkippedQueuedCount += 1
      }
    }
    if (realtimeResult.refreshed > 0 || realtimeResult.removed > 0 || failureCandidates.length > 0) {
      clearGatewayRuntimeCache()
      const queue = getAccountQualityFailurePrecheckQueueSnapshot()
      logger.info({
        event: 'background_account_quality_refresh_completed',
        realtimeRefreshed: realtimeResult.refreshed,
        realtimeRemoved: realtimeResult.removed,
        failureCandidateCount: failureCandidates.length,
        failurePrecheckEnqueuedCount,
        failurePrecheckSkippedQueuedCount,
        failurePrecheckQueuePendingCount: queue.pendingCount,
        failurePrecheckQueueRunningCount: queue.runningCount,
        failurePrecheckQueueNextRunAt: queue.nextRunAt
      }, '账户质量缓存刷新完成')
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_account_quality_refresh_failed' }), '账户质量缓存刷新失败')
    throw error
  }
}

export async function runCooldownAccountRetest(deps: AccountRetestDeps): Promise<void> {
  const batchSize = deps.settingsNumber('cooldownAccountRetestBatchSize', 1, 100)
  const maxPauseMinutes = deps.settingsNumber('defaultTemporaryUnschedulableMinutes', 1, 1440)
  const maxRecoveryHours = deps.settingsNumber('cooldownAccountRetestMaxBackoffHours', 1, 24 * 30)
  const candidates = listAccountsDueForCooldownRetest(batchSize)
  const startedAtMs = Date.now()
  let enqueuedCount = 0
  let skippedQueuedCount = 0
  for (const account of candidates) {
    if (enqueueCooldownAccountRetest(account, { maxPauseMinutes, maxRecoveryHours })) {
      enqueuedCount += 1
    } else {
      skippedQueuedCount += 1
    }
  }
  if (candidates.length > 0) {
    const queue = getCooldownAccountRetestQueueSnapshot()
    logger.info({
      event: 'background_cooldown_account_retest_completed',
      candidateCount: candidates.length,
      enqueuedCount,
      skippedQueuedCount,
      retryQueuePendingCount: queue.pendingCount,
      retryQueueRunningCount: queue.runningCount,
      retryQueueNextRunAt: queue.nextRunAt,
      elapsedMs: Date.now() - startedAtMs
    }, '冷却账户复测候选已加入异步队列')
  }
}

export async function runAccountApiKeyCooldownRetest(deps: AccountRetestDeps): Promise<void> {
  const batchSize = deps.settingsNumber('cooldownAccountRetestBatchSize', 1, 100)
  const maxRecoveryHours = deps.settingsNumber('cooldownAccountRetestMaxBackoffHours', 1, 24 * 30)
  const candidates = listAccountApiKeyRuntimeStatesDueForProbe(batchSize)
  const startedAtMs = Date.now()
  let enqueuedCount = 0
  let skippedQueuedCount = 0
  for (const candidate of candidates) {
    if (enqueueAccountApiKeyCooldownRetest(candidate, { maxRecoveryHours })) {
      enqueuedCount += 1
    } else {
      skippedQueuedCount += 1
    }
  }
  if (candidates.length > 0) {
    const queue = getAccountApiKeyCooldownRetestQueueSnapshot()
    logger.info({
      event: 'background_account_api_key_cooldown_retest_completed',
      candidateCount: candidates.length,
      enqueuedCount,
      skippedQueuedCount,
      retryQueuePendingCount: queue.pendingCount,
      retryQueueRunningCount: queue.runningCount,
      retryQueueNextRunAt: queue.nextRunAt,
      elapsedMs: Date.now() - startedAtMs
    }, '账户内 API Key 复测候选已加入异步队列')
  }
}
