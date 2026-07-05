import { errorLogFields, logger } from '../../shared/logger.js'
import { listAccountApiKeyRuntimeStatesDueForProbeAsync } from '../../storage/account-api-key-runtime-state.repository.js'
import { listNormalRouteLatencyProbeCandidatesAsync } from '../gateway/runtime/normal-route-latency-degradation.service.js'
import { clearGatewayRuntimeCache } from '../gateway/runtime/runtime-cache.service.js'
import { requestStatsWriter } from './background-stats-writer.js'
import { requestBackgroundWorkerDbService } from './background-ipc.js'
import {
  enqueueAccountApiKeyCooldownRetest,
  getAccountApiKeyCooldownRetestQueueSnapshot,
  setAccountApiKeyCooldownRetestQueueConcurrency
} from './account-api-key-cooldown-retest.service.js'
import {
  enqueueAccountHealthCheck,
  getAccountHealthCheckQueueSnapshot,
  setAccountHealthCheckQueueConcurrency
} from './account-health-check.service.js'
import {
  enqueueAccountQualityFailurePrecheck,
  getAccountQualityFailurePrecheckQueueSnapshot,
  setAccountQualityFailurePrecheckQueueConcurrency
} from './account-quality-failure-precheck.service.js'
import {
  enqueueCooldownAccountRetest,
  getCooldownAccountRetestQueueSnapshot,
  setCooldownAccountRetestQueueConcurrency
} from './cooldown-account-retest.service.js'
import {
  enqueueNormalRouteSpeedFirstRecoveryProbe,
  getNormalRouteSpeedFirstRecoveryProbeQueueSnapshot,
  setNormalRouteSpeedFirstRecoveryProbeQueueConcurrency
} from './normal-route-speed-first-recovery-probe.service.js'

const accountQualityFailurePrecheckBatchSize = 10
const normalRouteSpeedFirstRecoveryProbeBatchSize = 10
const maxOpsExternalIoConcurrency = 10
const maxOpsFullDiagnosticConcurrency = 3
const backgroundProbeDbServiceTimeoutMs = 10_000

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
    const realtimeResult = await requestStatsWriter({
      type: 'refresh_account_quality',
      windowMinutes,
      failureCandidateLimit: accountQualityFailurePrecheckBatchSize
    })
    const failureCandidates = realtimeResult.failureCandidates
    const queueConcurrency = boundedOpsQueueConcurrency(accountQualityFailurePrecheckBatchSize, maxOpsFullDiagnosticConcurrency)
    setAccountQualityFailurePrecheckQueueConcurrency(queueConcurrency)
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

export async function runCooldownAccountRetest(deps: AccountRetestDeps): Promise<void> {
  const batchSize = deps.settingsNumber('cooldownAccountRetestBatchSize', 1, 100)
  const queueConcurrency = boundedOpsQueueConcurrency(batchSize)
  setCooldownAccountRetestQueueConcurrency(queueConcurrency)
  const maxPauseMinutes = deps.settingsNumber('defaultTemporaryUnschedulableMinutes', 1, 1440)
  const maxRecoveryHours = deps.settingsNumber('cooldownAccountRetestMaxBackoffHours', 1, 24 * 30)
  const longTermIntervalHours = deps.settingsNumber('cooldownAccountRetestLongTermIntervalHours', 1, 24 * 30)
  const candidates = await requestBackgroundWorkerDbService({
    type: 'list_accounts_due_for_cooldown_retest',
    limit: batchSize
  }, backgroundProbeDbServiceTimeoutMs) ?? []
  const startedAtMs = Date.now()
  let enqueuedCount = 0
  let skippedQueuedCount = 0
  for (const account of candidates) {
    if (enqueueCooldownAccountRetest(account, { maxPauseMinutes, maxRecoveryHours, longTermIntervalHours })) {
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
      retryQueueConcurrency: queueConcurrency,
      retryQueuePendingCount: queue.pendingCount,
      retryQueueRunningCount: queue.runningCount,
      retryQueueNextRunAt: queue.nextRunAt,
      elapsedMs: Date.now() - startedAtMs
    }, '冷却账户复测候选已加入异步队列')
  }
}

export async function runAccountHealthCheck(deps: AccountRetestDeps): Promise<void> {
  const batchSize = deps.settingsNumber('accountHealthCheckBatchSize', 1, 100)
  const queueConcurrency = boundedOpsQueueConcurrency(batchSize)
  setAccountHealthCheckQueueConcurrency(queueConcurrency)
  const intervalHours = deps.settingsNumber('accountHealthCheckIntervalHours', 1, 168)
  const jitterMinutes = deps.settingsNumber('accountHealthCheckJitterMinutes', 0, 1440)
  const failureThreshold = deps.settingsNumber('accountHealthCheckFailureThreshold', 1, 10)
  const maxPauseMinutes = deps.settingsNumber('defaultTemporaryUnschedulableMinutes', 1, 1440)
  const candidates = await requestBackgroundWorkerDbService({
    type: 'list_accounts_due_for_health_check',
    input: {
      limit: batchSize,
      intervalHours,
      jitterMinutes,
      failureThreshold
    }
  }, backgroundProbeDbServiceTimeoutMs) ?? []
  const startedAtMs = Date.now()
  let enqueuedCount = 0
  let skippedQueuedCount = 0
  for (const account of candidates) {
    if (enqueueAccountHealthCheck(account, { intervalHours, jitterMinutes, failureThreshold, maxPauseMinutes })) {
      enqueuedCount += 1
    } else {
      skippedQueuedCount += 1
    }
  }
  if (candidates.length > 0) {
    const queue = getAccountHealthCheckQueueSnapshot()
    logger.info({
      event: 'background_account_health_check_candidates_completed',
      candidateCount: candidates.length,
      enqueuedCount,
      skippedQueuedCount,
      healthCheckQueueConcurrency: queueConcurrency,
      healthCheckQueuePendingCount: queue.pendingCount,
      healthCheckQueueRunningCount: queue.runningCount,
      healthCheckQueueNextRunAt: queue.nextRunAt,
      elapsedMs: Date.now() - startedAtMs
    }, '账号健康检测候选已加入异步队列')
  }
}

export async function runAccountApiKeyCooldownRetest(deps: AccountRetestDeps): Promise<void> {
  const batchSize = deps.settingsNumber('cooldownAccountRetestBatchSize', 1, 100)
  const queueConcurrency = boundedOpsQueueConcurrency(batchSize)
  setAccountApiKeyCooldownRetestQueueConcurrency(queueConcurrency)
  const maxRecoveryHours = deps.settingsNumber('cooldownAccountRetestMaxBackoffHours', 1, 24 * 30)
  const candidates = await listAccountApiKeyRuntimeStatesDueForProbeAsync(batchSize)
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
  const queueConcurrency = boundedOpsQueueConcurrency(batchSize)
  setNormalRouteSpeedFirstRecoveryProbeQueueConcurrency(queueConcurrency)
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

function boundedOpsQueueConcurrency(batchSize: number, maxConcurrency = maxOpsExternalIoConcurrency): number {
  const normalizedBatchSize = Number.isFinite(batchSize) ? Math.trunc(batchSize) : 1
  const normalizedMaxConcurrency = Number.isFinite(maxConcurrency) ? Math.trunc(maxConcurrency) : maxOpsExternalIoConcurrency
  return Math.max(1, Math.min(normalizedBatchSize, normalizedMaxConcurrency))
}
