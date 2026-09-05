import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import type { ScheduledJobLeaseFence } from '../../storage/scheduled-job-lease.repository.js'
import { listNormalRouteLatencyProbeCandidatesAsync } from '../gateway/runtime/normal-route-latency-degradation.service.js'
import { clearGatewayRuntimeCache } from '../gateway/runtime/runtime-cache.service.js'
import { requestStatsWriter } from './background-stats-writer.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'
import {
  backgroundProbeDbServiceTimeoutMs,
  globalSharedQueueConcurrency
} from './account-probe-limits.js'
import {
  enqueueAccountApiKeyCooldownRetest,
  getAccountApiKeyCooldownRetestQueueSnapshot
} from './account-api-key-cooldown-retest.service.js'
import {
  enqueueAccountQualityFailurePrecheck,
  getAccountQualityFailurePrecheckQueueSnapshot
} from './account-quality-failure-precheck.service.js'
import {
  enqueueNormalRouteSpeedFirstRecoveryProbe,
  getNormalRouteSpeedFirstRecoveryProbeQueueSnapshot
} from './normal-route-speed-first-recovery-probe.service.js'

const accountQualityFailurePrecheckBatchSize = runtimeConfig.background.accountQualityFailurePrecheckBatchSize
const normalRouteSpeedFirstRecoveryProbeBatchSize = runtimeConfig.background.normalRouteSpeedFirstRecoveryProbeBatchSize
let accountQualityFailurePrecheckOffset = 0

type SettingsNumberReader = (key: string, min: number, max: number) => number

interface AccountQualityRefreshDeps {
  settingsNumber: SettingsNumberReader
  ensureUsageRecordsIngestedBeforeStatsAggregation: () => Promise<void>
  yieldToEventLoop: () => Promise<void>
  signal: AbortSignal
  scheduledLease?: ScheduledJobLeaseFence
}

interface AccountRetestDeps {
  settingsNumber: SettingsNumberReader
}

export async function runAccountQualityRefresh(deps: AccountQualityRefreshDeps): Promise<void> {
  try {
    deps.signal.throwIfAborted()
    await deps.ensureUsageRecordsIngestedBeforeStatsAggregation()
    deps.signal.throwIfAborted()
    await deps.yieldToEventLoop()
    deps.signal.throwIfAborted()
    const windowMinutes = deps.settingsNumber('accountQualityWindowMinutes', 1, 60)
    const realtimeResult = await requestStatsWriter({
      type: 'refresh_account_quality',
      windowMinutes,
      failureCandidateLimit: accountQualityFailurePrecheckBatchSize,
      failureCandidateOffset: accountQualityFailurePrecheckOffset,
      scheduledLease: deps.scheduledLease
    }, 45_000)
    const failureCandidates = realtimeResult.failureCandidates
    accountQualityFailurePrecheckOffset = failureCandidates.length < accountQualityFailurePrecheckBatchSize
      ? 0
      : accountQualityFailurePrecheckOffset + failureCandidates.length
    const queueConcurrency = globalSharedQueueConcurrency
    let failurePrecheckEnqueuedCount = 0
    let failurePrecheckSkippedQueuedCount = 0
    for (const candidate of failureCandidates) {
      deps.signal.throwIfAborted()
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
        failurePrecheckQueueConcurrency: queueConcurrency,
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

export async function runAccountApiKeyCooldownRetest(deps: AccountRetestDeps): Promise<void> {
  const batchSize = runtimeConfig.background.accountApiKeyCooldownRetestBatchSize
  const queueConcurrency = globalSharedQueueConcurrency
  const maxRecoveryHours = deps.settingsNumber('cooldownAccountRetestMaxBackoffHours', 1, 24 * 30)
  const queueBeforeScan = getAccountApiKeyCooldownRetestQueueSnapshot()
  const availableQueueSlots = Math.max(
    0,
    queueConcurrency - queueBeforeScan.runningCount - queueBeforeScan.pendingCount
  )
  if (availableQueueSlots === 0) return
  const candidates = await requestBackgroundWorkerDbService({
    type: 'list_account_api_key_runtime_states_due_for_probe',
    limit: Math.min(batchSize, availableQueueSlots)
  }, backgroundProbeDbServiceTimeoutMs) ?? []
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
      retryQueueConcurrency: queueConcurrency,
      retryQueuePendingCount: queue.pendingCount,
      retryQueueRunningCount: queue.runningCount,
      retryQueueNextRunAt: queue.nextRunAt,
      elapsedMs: Date.now() - startedAtMs
    }, '账户内 API Key 复测候选已加入异步队列')
  }
}

export async function runNormalRouteSpeedFirstRecoveryProbe(): Promise<void> {
  const batchSize = normalRouteSpeedFirstRecoveryProbeBatchSize
  const queueConcurrency = globalSharedQueueConcurrency
  const candidates = await listNormalRouteLatencyProbeCandidatesAsync(batchSize)
  const startedAtMs = Date.now()
  let enqueuedCount = 0
  let skippedQueuedCount = 0
  for (const candidate of candidates) {
    if (enqueueNormalRouteSpeedFirstRecoveryProbe(candidate)) {
      enqueuedCount += 1
    } else {
      skippedQueuedCount += 1
    }
  }
  if (candidates.length > 0) {
    const queue = getNormalRouteSpeedFirstRecoveryProbeQueueSnapshot()
    logger.info({
      event: 'background_normal_route_speed_first_recovery_probe_completed',
      candidateCount: candidates.length,
      enqueuedCount,
      skippedQueuedCount,
      recoveryProbeQueueConcurrency: queueConcurrency,
      recoveryProbeQueuePendingCount: queue.pendingCount,
      recoveryProbeQueueRunningCount: queue.runningCount,
      recoveryProbeQueueNextRunAt: queue.nextRunAt,
      elapsedMs: Date.now() - startedAtMs
    }, '普通路由速度优先恢复探针候选已加入异步队列')
  }
}
