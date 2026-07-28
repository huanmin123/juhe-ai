import { randomUUID } from 'node:crypto'
import { stat as statFile } from 'node:fs/promises'

import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { buildProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import { runIfNodeOwnsWorkerJob } from '../../shared/worker-owner.js'
import {
  performanceProcessMetricsTopologyComplete,
  readPerformanceProcessEventLoopSamples
} from '../../shared/performance-process-metrics-registry.js'
import { datasetDatabasePath, nowIso, statsDatabasePath, usageCatalogDatabasePath } from '../../storage/database.js'
import { getSettings, getSettingsAsync } from '../../storage/repositories.js'
import {
  latestUsageStatsLagSecondsForRuntime,
  usageStatsCursorSafetyDelaySeconds,
  type UsageRankSnapshotStageName
} from '../../storage/usage-stats.repository.js'
import { dateKey, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'
import {
  releaseScheduledJobLease,
  renewScheduledJobLease,
  ScheduledJobLeaseLostError,
  tryAcquireScheduledJobLease,
  type ScheduledJobLeaseFence
} from '../../storage/scheduled-job-lease.repository.js'
import { refreshDueOpenAIOAuthAccessTokens } from '../openai-oauth/openai-oauth-access-token-refresh.service.js'
import { proxyLatencyRefreshBatchSize, proxyLatencyRefreshIntervalSeconds, refreshProxyLatencyBatch } from '../proxies/proxy-test.service.js'
import { clearGatewayRuntimeCache } from '../gateway/runtime/runtime-cache.service.js'
import { ensureRuntimeLogFacetSnapshots } from '../../storage/runtime-logs.repository.js'
import { requestBackgroundWorkerDbService, requestIngestWorkerDrainStatus, requestServerProcessEventLoopSamples } from './background-ipc.js'
import type { BackgroundWorkerIngestDrainStatus } from './background-ipc.types.js'
import { requestStatsWriter } from './background-stats-writer.js'
import { backgroundScheduledJobName } from './background-job-registry.js'
import { DEFAULT_SYSTEM_SETTINGS } from '../../storage/schema-defaults.js'
import { enqueueAccountHealthCheckById, setAccountHealthCheckQueueConcurrency } from './account-health-check.service.js'
import type { AccountHealthCheckTriggerReason } from '../accounts/account-health-check-trigger.js'
import {
  runAccountApiKeyCooldownRetest,
  runAccountHealthCheck,
  runAccountQualityRefresh,
  runCooldownAccountRetest,
  runNormalRouteSpeedFirstRecoveryProbe
} from './account-probe-jobs.js'
import {
  accountApiKeyCooldownRetestStartupDelayMs,
  backgroundFullDiagnosticConcurrency,
  cooldownAccountRetestStartupDelayMs,
  normalRouteSpeedFirstProbeStartupDelayMs
} from './account-probe-limits.js'
import {
  runAccountRecordCleanupRetry,
  runApiKeyRecordCleanupRetry,
  runAuditHotRetentionCleanup,
  runChatRetentionCleanup,
  runDataRetentionCleanup,
  runExpiredDeletedAccountCleanup
} from './maintenance-cleanup-jobs.js'
import { currentCpuPercent, currentMemoryMetrics, currentNetworkMetrics } from './system-metrics-sampler.service.js'
import { WorkerScheduler, type WorkerScheduledJobTaskResult } from './worker-scheduler.js'
import { getUsageRecordRedisStreamOldestCreatedAt } from '../gateway/usage/record-queue.service.js'
import { DATA_RETENTION_CLEANUP_INTERVAL_MINUTES } from './data-retention-cleanup.constants.js'
import { runAccountBalanceRefresh } from './account-balance-refresh.job.js'
import {
  backgroundTaskRunReconcileInitialDelayMs,
  backgroundTaskRunReconcileIntervalMs,
  runBackgroundTaskRunReconcile
} from './background-task-run-reconcile.job.js'
import {
  installDefaultScheduledAccountCircuitRecoveryResolver,
  runScheduledAccountCircuitRecovery
} from './account-circuit-recovery.service.js'
import { runGatewayAccountCircuitControlPlaneMaintenance } from '../gateway/runtime/account-circuit.service.js'
import {
  retryFailedModelQualityHealthSyncs,
  runDueModelQualityRecoveries,
  runDueModelQualityScheduledChecks
} from './model-quality-scheduled-check.service.js'

let started = false
let startGeneration = 0
let usageStatsAggregationRunning = false
let modelTrustAggregationRunning = false
let clientIpStatsAggregationRunning = false
let usageRankSnapshotsRefreshRunning = false
let groupAccountStatsStartupDirtyMarked = false
let missingRemoteProcessEventLoopSampleWarningCount = 0
let usageHotWindowRefreshPending = false
let lastUsageHotWindowRefreshStartedAtMs = 0
let lastUsageHotWindowDateKey: string | undefined
interface UsageStatsAggregationSafety {
  safeCreatedBefore: string
}

const dailyIntervalMs = 24 * 60 * 60 * 1000
const secondMs = 1000
const minuteMs = 60 * secondMs
const usageRankSnapshotRefreshIntervalMs = 30 * minuteMs
const usageOverviewWindowRefreshIntervalMs = 5 * minuteMs
const coldUsageRangeWindowRefreshIntervalMs = 6 * 60 * minuteMs
const usageScopeRangeWindowInitialDelayMs = 31 * minuteMs
const authorizationUsageRangeWindowInitialDelayMs = 43 * minuteMs
const clientIpStatsAggregationBatchSizeCap = 1000
const clientIpStatsAggregationMaxBatchesCap = 10
const clientIpStatsAggregationMaxRunMs = 5000
const usageStatsAggregationMaxRunMs = 4500
const usageStatsOnlineFreshnessMaxIntervalSeconds = 60
const usageHotWindowRefreshTimeoutMs = 30_000
const usageHotWindowRefreshJobName = 'usage_hot_window_refresh'
const usageRankSnapshotSlowStageMs = 1000
const usageRankSnapshotCoreStageNames: UsageRankSnapshotStageName[] = [
  'account_last7d_request_rank',
  'caller_account_last7d_request_rank',
  'api_key_current_month_cost_rank',
  'account_authorization_current_month_cost_rank',
  'group_authorization_current_month_cost_rank',
  'ai_performance_summary_windows'
]
const postgresUsageRankSnapshotCoreStageNames: UsageRankSnapshotStageName[] = usageRankSnapshotCoreStageNames.filter(
  (stageName) => stageName !== 'ai_performance_summary_windows'
)
const aiPerformanceSummaryWindowStageNames: UsageRankSnapshotStageName[] = ['ai_performance_summary_windows']
const systemMetricsTrendStageNames: UsageRankSnapshotStageName[] = ['system_metrics_trend_windows']
const usageOverviewWindowStageNames: UsageRankSnapshotStageName[] = ['usage_overview_windows']
const usageScopeRangeWindowStageNames: UsageRankSnapshotStageName[] = ['usage_scope_range_windows']
const authorizationUsageRangeWindowStageNames: UsageRankSnapshotStageName[] = ['authorization_usage_range_windows']
const scheduler = new WorkerScheduler({
  stableInstanceId: runtimeConfig.instanceId,
  workerRole: `${runtimeConfig.workerRole}:${runtimeConfig.workerReplicaIndex}`
})
const defaultSystemSettingsByKey = new Map<string, unknown>(DEFAULT_SYSTEM_SETTINGS.map(([key, value]) => [key, value]))
const backgroundJobSettingsSnapshotTtlMs = 60_000
let backgroundJobSettingsSnapshot: Record<string, unknown> | undefined
let backgroundJobSettingsSnapshotLoadedAt = 0
let backgroundJobSettingsRefreshPromise: Promise<void> | undefined
let sqliteSettingsTableMissingWarningLogged = false

export function startBackgroundJobs(): void {
  if (started) return
  started = true
  const generation = ++startGeneration
  if (runtimeConfig.databaseDriver === 'postgres') {
    void refreshBackgroundJobSettingsSnapshotIfNeeded()
      .then(() => {
        if (!started || generation !== startGeneration) return
        scheduleBackgroundJobs()
      })
      .catch((error) => handleBackgroundJobsStartError(error, generation))
    return
  }
  scheduleBackgroundJobs()
}

export async function stopBackgroundJobs(): Promise<{ drained: boolean; activeCount: number }> {
  if (!started) return { drained: true, activeCount: 0 }
  started = false
  startGeneration += 1
  return await scheduler.stopAndDrain(10_000)
}

async function runWithPostgresScheduledLease(
  jobName: string,
  ttlMs: number,
  signal: AbortSignal,
  task: (signal: AbortSignal, scheduledLease?: ScheduledJobLeaseFence) => void | WorkerScheduledJobTaskResult | Promise<void | WorkerScheduledJobTaskResult>
): Promise<void | WorkerScheduledJobTaskResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') return await task(signal, undefined)

  const ownerId = `${runtimeConfig.instanceId}:${runtimeConfig.workerRole}:${runtimeConfig.workerReplicaIndex}:${randomUUID()}`
  const acquired = await tryAcquireScheduledJobLease({
    jobName,
    ownerId,
    runId: randomUUID(),
    ttlMs
  })
  if (!acquired.acquired) {
    return { outcome: 'skipped', warning: `lease_busy:${acquired.reason}`, leaseState: 'busy' }
  }

  let lease = acquired.lease
  let leaseLostError: ScheduledJobLeaseLostError | undefined
  let stopped = false
  let heartbeatTimer: NodeJS.Timeout | undefined
  let heartbeatInFlight: Promise<void> | undefined
  const controller = new AbortController()
  const forwardAbort = () => controller.abort(signal.reason)
  if (signal.aborted) forwardAbort()
  else signal.addEventListener('abort', forwardAbort, { once: true })

  const scheduleHeartbeat = () => {
    if (stopped || controller.signal.aborted) return
    heartbeatTimer = setTimeout(() => {
      heartbeatInFlight = renewLease()
    }, Math.max(secondMs, Math.trunc(ttlMs / 3)))
    heartbeatTimer.unref()
  }
  const renewLease = async (): Promise<void> => {
    try {
      const renewed = await renewScheduledJobLease(lease, ttlMs)
      if (!renewed) {
        leaseLostError = new ScheduledJobLeaseLostError(lease)
        controller.abort(leaseLostError)
        return
      }
      lease = renewed
      scheduleHeartbeat()
    } catch (error) {
      leaseLostError = error instanceof ScheduledJobLeaseLostError
        ? error
        : new ScheduledJobLeaseLostError(lease)
      controller.abort(leaseLostError)
      logger.error(errorLogFields(error, {
        event: 'background_job_lease_renew_failed',
        jobName,
        leaseKey: lease.leaseKey
      }), '后台任务租约续期失败，已请求取消当前任务')
    } finally {
      heartbeatInFlight = undefined
    }
  }

  scheduleHeartbeat()
  let result: void | WorkerScheduledJobTaskResult | undefined
  let taskError: unknown
  try {
    result = await task(controller.signal, {
      leaseKey: lease.leaseKey,
      ownerId: lease.ownerId,
      fencingToken: lease.fencingToken
    })
  } catch (error) {
    taskError = error
  } finally {
    stopped = true
    if (heartbeatTimer) clearTimeout(heartbeatTimer)
    if (heartbeatInFlight) await heartbeatInFlight
    signal.removeEventListener('abort', forwardAbort)
  }

  let released = false
  if (!leaseLostError) {
    try {
      released = await releaseScheduledJobLease(lease)
    } catch (error) {
      logger.error(errorLogFields(error, {
        event: 'background_job_lease_release_failed',
        jobName,
        leaseKey: lease.leaseKey
      }), '后台任务完成但租约释放失败，等待 TTL 自动过期')
    }
  }
  if (taskError) throw taskError
  if (leaseLostError) throw leaseLostError
  if (!released) {
    return { outcome: 'partial', warning: '任务完成但租约释放未命中', leaseState: 'lost' }
  }
  return result
    ? { ...result, leaseState: 'acquired' }
    : { outcome: 'success', leaseState: 'acquired' }
}

function scheduleBackgroundJobs(): void {
  switch (runtimeConfig.workerRole) {
    case 'usage-worker':
      scheduleUsageIngestJobs()
      return
    case 'log-worker':
      scheduleLogIngestJobs()
      return
    case 'ingest-worker':
      scheduleUsageIngestJobs()
      scheduleLogIngestJobs()
      return
    case 'stats-worker':
      scheduler.schedule({
        name: backgroundScheduledJobName('background-task-run-reconcile'),
        intervalMs: backgroundTaskRunReconcileIntervalMs,
        initialDelayMs: backgroundTaskRunReconcileInitialDelayMs,
        scheduleMode: 'fixedDelay',
        timeoutMs: 2 * minuteMs,
        failureBackoff: { baseMs: 5 * secondMs, maxMs: 5 * minuteMs },
        task: runBackgroundTaskRunReconcile
      })
      scheduler.schedule({ name: backgroundScheduledJobName('model-trust-observation-aggregation'), intervalMs: 30 * secondMs, initialDelayMs: 12 * secondMs, stablePhaseWindowMs: 5 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'stats-online', timeoutMs: 3 * minuteMs, failureBackoff: { baseMs: secondMs, maxMs: minuteMs }, task: ({ signal }) => runWithPostgresScheduledLease('model-trust-observation-aggregation', 10 * minuteMs, signal, runModelTrustAggregation) })
      if (isPostgresHighPerformanceMode()) {
        scheduler.schedule({ name: backgroundScheduledJobName('system-metrics-sample'), intervalMs: settingsNumber('systemMetricsSampleIntervalSeconds', 5, 3600) * secondMs, initialDelayMs: 4 * secondMs, overlapPolicy: 'coalesceOne', timeoutMs: 20 * secondMs, task: runSystemMetricsSample })
        scheduler.schedule({ name: backgroundScheduledJobName('usage-stats-aggregation'), intervalMs: usageStatsOnlineAggregationIntervalSeconds() * secondMs, initialDelayMs: 3 * secondMs, stablePhaseWindowMs: 2 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'stats-online', timeoutMs: 20 * secondMs, failureBackoff: { baseMs: secondMs, maxMs: minuteMs }, task: ({ signal }) => runWithPostgresScheduledLease('usage-stats-aggregation', minuteMs, signal, runUsageStatsAggregation) })
        scheduler.schedule({ name: backgroundScheduledJobName('client-ip-stats-aggregation'), intervalMs: settingsNumber('statsAggregationIntervalSeconds', 5, 3600) * secondMs, initialDelayMs: 8 * secondMs, stablePhaseWindowMs: 2 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'stats-online', timeoutMs: 20 * secondMs, failureBackoff: { baseMs: secondMs, maxMs: minuteMs }, task: ({ signal }) => runWithPostgresScheduledLease('client-ip-stats-aggregation', minuteMs, signal, runClientIpStatsAggregation) })
        scheduler.schedule({ name: backgroundScheduledJobName('group-account-stats-refresh'), intervalMs: settingsNumber('groupAccountStatsRefreshIntervalSeconds', 5, 3600) * secondMs, initialDelayMs: 16 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'stats-online', timeoutMs: 30 * secondMs, failureBackoff: { baseMs: secondMs, maxMs: minuteMs }, task: ({ signal }) => runWithPostgresScheduledLease('group-account-stats-refresh', 2 * minuteMs, signal, runGroupAccountStatsRefresh) })
        scheduleUsageHotWindowRefreshJob()
        scheduler.schedule({ name: backgroundScheduledJobName('usage-rank-snapshots-refresh'), intervalMs: usageRankSnapshotRefreshIntervalMs, initialDelayMs: 2 * minuteMs + 30 * secondMs, stablePhaseWindowMs: 30 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'stats-heavy', timeoutMs: 10 * minuteMs, failureBackoff: { baseMs: 30 * secondMs, maxMs: 10 * minuteMs }, task: ({ signal }) => runLeasedUsageRankSnapshotsRefresh('usage-rank-snapshots-refresh', postgresUsageRankSnapshotCoreStageNames, signal) })
        scheduler.schedule({ name: backgroundScheduledJobName('ai-performance-summary-windows-refresh'), intervalMs: 5 * minuteMs, initialDelayMs: 3 * minuteMs, stablePhaseWindowMs: 30 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'stats-heavy', timeoutMs: minuteMs, failureBackoff: { baseMs: 15 * secondMs, maxMs: 5 * minuteMs }, task: ({ signal }) => runLeasedUsageRankSnapshotsRefresh('ai-performance-summary-windows-refresh', aiPerformanceSummaryWindowStageNames, signal, 5 * minuteMs) })
        scheduler.schedule({ name: backgroundScheduledJobName('system-metrics-trend-windows-refresh'), intervalMs: usageRankSnapshotRefreshIntervalMs, initialDelayMs: 3 * minuteMs + 20 * secondMs, stablePhaseWindowMs: 30 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'stats-heavy', timeoutMs: 10 * minuteMs, failureBackoff: { baseMs: 30 * secondMs, maxMs: 10 * minuteMs }, task: ({ signal }) => runLeasedUsageRankSnapshotsRefresh('system-metrics-trend-windows-refresh', systemMetricsTrendStageNames, signal) })
        scheduler.schedule({ name: backgroundScheduledJobName('usage-overview-windows-refresh'), intervalMs: usageOverviewWindowRefreshIntervalMs, initialDelayMs: 4 * minuteMs + 10 * secondMs, stablePhaseWindowMs: 30 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'stats-heavy', timeoutMs: 10 * minuteMs, failureBackoff: { baseMs: 30 * secondMs, maxMs: 10 * minuteMs }, task: ({ signal }) => runLeasedUsageRankSnapshotsRefresh('usage-overview-windows-refresh', usageOverviewWindowStageNames, signal) })
        scheduler.schedule({ name: backgroundScheduledJobName('authorization-usage-range-windows-refresh'), intervalMs: coldUsageRangeWindowRefreshIntervalMs, initialDelayMs: authorizationUsageRangeWindowInitialDelayMs, stablePhaseWindowMs: 30 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'stats-heavy', timeoutMs: 10 * minuteMs, failureBackoff: { baseMs: minuteMs, maxMs: 30 * minuteMs }, task: ({ signal }) => runLeasedUsageRankSnapshotsRefresh('authorization-usage-range-windows-refresh', authorizationUsageRangeWindowStageNames, signal) })
        logger.info({
          event: 'background_cold_range_window_refresh_disabled',
          driver: runtimeConfig.databaseDriver,
          hotStages: usageScopeRangeWindowStageNames
        }, 'PG 高性能模式跳过在线冷历史范围窗口重刷，热窗口刷新保持今日范围数据新鲜')
        scheduleAccountQualityRefreshJob()
        scheduler.schedule({ name: backgroundScheduledJobName('table-storage-monitor'), intervalMs: 10 * minuteMs, initialDelayMs: 90 * secondMs, stablePhaseWindowMs: 30 * secondMs, scheduleMode: 'fixedDelay', overlapPolicy: 'coalesceOne', resourceLane: 'stats-heavy', timeoutMs: 2 * minuteMs, failureBackoff: { baseMs: 30 * secondMs, maxMs: 10 * minuteMs }, task: ({ signal }) => runWithPostgresScheduledLease('table-storage-monitor', 5 * minuteMs, signal, runTableStorageMonitor) })
        scheduler.schedule({ name: backgroundScheduledJobName('usage-stats-consistency-check'), intervalMs: 60 * minuteMs, initialDelayMs: 11 * minuteMs, task: runUsageStatsConsistencyCheck })
        return
      }
      scheduler.schedule({ name: backgroundScheduledJobName('system-metrics-sample'), intervalMs: settingsNumber('systemMetricsSampleIntervalSeconds', 5, 3600) * secondMs, initialDelayMs: 4 * secondMs, overlapPolicy: 'coalesceOne', timeoutMs: 20 * secondMs, task: runSystemMetricsSample })
      scheduler.schedule({ name: backgroundScheduledJobName('usage-stats-aggregation'), intervalMs: usageStatsOnlineAggregationIntervalSeconds() * secondMs, initialDelayMs: 3 * secondMs, stablePhaseWindowMs: 2 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'stats-online', timeoutMs: 20 * secondMs, failureBackoff: { baseMs: secondMs, maxMs: minuteMs }, task: ({ signal }) => runWithPostgresScheduledLease('usage-stats-aggregation', minuteMs, signal, runUsageStatsAggregation) })
      scheduler.schedule({ name: backgroundScheduledJobName('client-ip-stats-aggregation'), intervalMs: settingsNumber('statsAggregationIntervalSeconds', 5, 3600) * secondMs, initialDelayMs: 8 * secondMs, stablePhaseWindowMs: 2 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'stats-online', timeoutMs: 20 * secondMs, failureBackoff: { baseMs: secondMs, maxMs: minuteMs }, task: ({ signal }) => runWithPostgresScheduledLease('client-ip-stats-aggregation', minuteMs, signal, runClientIpStatsAggregation) })
      scheduler.schedule({ name: backgroundScheduledJobName('group-account-stats-refresh'), intervalMs: settingsNumber('groupAccountStatsRefreshIntervalSeconds', 5, 3600) * secondMs, initialDelayMs: 16 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'stats-online', timeoutMs: 30 * secondMs, failureBackoff: { baseMs: secondMs, maxMs: minuteMs }, task: ({ signal }) => runWithPostgresScheduledLease('group-account-stats-refresh', 2 * minuteMs, signal, runGroupAccountStatsRefresh) })
      scheduleUsageHotWindowRefreshJob()
      scheduler.schedule({ name: backgroundScheduledJobName('usage-rank-snapshots-refresh'), intervalMs: usageRankSnapshotRefreshIntervalMs, initialDelayMs: 2 * minuteMs + 30 * secondMs, stablePhaseWindowMs: 30 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'stats-heavy', timeoutMs: 10 * minuteMs, failureBackoff: { baseMs: 30 * secondMs, maxMs: 10 * minuteMs }, task: ({ signal }) => runLeasedUsageRankSnapshotsRefresh('usage-rank-snapshots-refresh', usageRankSnapshotCoreStageNames, signal) })
      scheduler.schedule({ name: backgroundScheduledJobName('system-metrics-trend-windows-refresh'), intervalMs: usageRankSnapshotRefreshIntervalMs, initialDelayMs: 3 * minuteMs + 20 * secondMs, stablePhaseWindowMs: 30 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'stats-heavy', timeoutMs: 10 * minuteMs, failureBackoff: { baseMs: 30 * secondMs, maxMs: 10 * minuteMs }, task: ({ signal }) => runLeasedUsageRankSnapshotsRefresh('system-metrics-trend-windows-refresh', systemMetricsTrendStageNames, signal) })
      scheduler.schedule({ name: backgroundScheduledJobName('usage-overview-windows-refresh'), intervalMs: usageRankSnapshotRefreshIntervalMs, initialDelayMs: 4 * minuteMs + 10 * secondMs, stablePhaseWindowMs: 30 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'stats-heavy', timeoutMs: 10 * minuteMs, failureBackoff: { baseMs: 30 * secondMs, maxMs: 10 * minuteMs }, task: ({ signal }) => runLeasedUsageRankSnapshotsRefresh('usage-overview-windows-refresh', usageOverviewWindowStageNames, signal) })
      scheduler.schedule({ name: backgroundScheduledJobName('usage-scope-range-windows-refresh'), intervalMs: coldUsageRangeWindowRefreshIntervalMs, initialDelayMs: usageScopeRangeWindowInitialDelayMs, stablePhaseWindowMs: 30 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'stats-heavy', timeoutMs: 10 * minuteMs, failureBackoff: { baseMs: minuteMs, maxMs: 30 * minuteMs }, task: ({ signal }) => runLeasedUsageRankSnapshotsRefresh('usage-scope-range-windows-refresh', usageScopeRangeWindowStageNames, signal) })
      scheduler.schedule({ name: backgroundScheduledJobName('authorization-usage-range-windows-refresh'), intervalMs: coldUsageRangeWindowRefreshIntervalMs, initialDelayMs: authorizationUsageRangeWindowInitialDelayMs, stablePhaseWindowMs: 30 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'stats-heavy', timeoutMs: 10 * minuteMs, failureBackoff: { baseMs: minuteMs, maxMs: 30 * minuteMs }, task: ({ signal }) => runLeasedUsageRankSnapshotsRefresh('authorization-usage-range-windows-refresh', authorizationUsageRangeWindowStageNames, signal) })
      scheduleAccountQualityRefreshJob()
      scheduler.schedule({ name: backgroundScheduledJobName('table-storage-monitor'), intervalMs: 10 * minuteMs, initialDelayMs: 90 * secondMs, stablePhaseWindowMs: 30 * secondMs, scheduleMode: 'fixedDelay', overlapPolicy: 'coalesceOne', resourceLane: 'stats-heavy', timeoutMs: 2 * minuteMs, failureBackoff: { baseMs: 30 * secondMs, maxMs: 10 * minuteMs }, task: ({ signal }) => runWithPostgresScheduledLease('table-storage-monitor', 5 * minuteMs, signal, runTableStorageMonitor) })
      scheduler.schedule({ name: backgroundScheduledJobName('usage-stats-consistency-check'), intervalMs: 60 * minuteMs, initialDelayMs: 11 * minuteMs, task: runUsageStatsConsistencyCheck })
      return
    case 'ops-worker':
      installDefaultScheduledAccountCircuitRecoveryResolver()
      scheduler.schedule({ name: backgroundScheduledJobName('chat-retention-cleanup'), intervalMs: 10 * minuteMs, initialDelayMs: 270 * secondMs, stablePhaseWindowMs: 30 * secondMs, scheduleMode: 'fixedDelay', resourceLane: 'storage-maintenance', timeoutMs: 2 * minuteMs, failureBackoff: { baseMs: 30 * secondMs, maxMs: 10 * minuteMs }, task: ({ signal }) => runWithPostgresScheduledLease('chat-retention-cleanup', 5 * minuteMs, signal, runChatRetentionCleanup) })
      scheduler.schedule({ name: backgroundScheduledJobName('api-key-availability-schedule-status-sync'), intervalMs: 10 * secondMs, initialDelayMs: secondMs, task: runApiKeyAvailabilityScheduleStatusSync })
      scheduler.schedule({ name: backgroundScheduledJobName('account-availability-schedule-status-sync'), intervalMs: 10 * secondMs, initialDelayMs: 2 * secondMs, task: runAccountAvailabilityScheduleStatusSync })
      scheduler.schedule({ name: backgroundScheduledJobName('resource-authorization-expiry-sweep'), intervalMs: minuteMs, initialDelayMs: 54 * secondMs, task: runResourceAuthorizationExpirySweep })
      scheduler.schedule({ name: backgroundScheduledJobName('expired-deleted-account-cleanup'), intervalMs: dailyIntervalMs, initialDelayMs: 14 * minuteMs, task: runExpiredDeletedAccountCleanup })
      scheduler.schedule({ name: backgroundScheduledJobName('account-health-check'), intervalMs: minuteMs, initialDelayMs: 90 * secondMs, stablePhaseWindowMs: 10 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'account-diagnostic-scanner', timeoutMs: 55 * secondMs, task: () => runAccountHealthCheck({ settingsNumber }) })
      scheduler.schedule({ name: backgroundScheduledJobName('model-quality-scheduled-check'), intervalMs: minuteMs, initialDelayMs: 45 * secondMs, stablePhaseWindowMs: 5 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'model-quality', timeoutMs: 20 * minuteMs, failureBackoff: { baseMs: minuteMs, maxMs: 15 * minuteMs }, task: async ({ signal }) => modelQualityBatchOutcome(await runDueModelQualityScheduledChecks(signal), '模型质量定时检查') })
      scheduler.schedule({ name: backgroundScheduledJobName('model-quality-recovery'), intervalMs: minuteMs, initialDelayMs: 55 * secondMs, stablePhaseWindowMs: 5 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'model-quality', timeoutMs: 20 * minuteMs, failureBackoff: { baseMs: minuteMs, maxMs: 15 * minuteMs }, task: async ({ signal }) => modelQualityBatchOutcome(await runDueModelQualityRecoveries(signal), '模型质量恢复检查') })
      scheduler.schedule({ name: backgroundScheduledJobName('model-quality-health-sync-retry'), intervalMs: minuteMs, initialDelayMs: 58 * secondMs, stablePhaseWindowMs: 2 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'model-quality', timeoutMs: 2 * minuteMs, failureBackoff: { baseMs: minuteMs, maxMs: 15 * minuteMs }, task: async ({ signal }) => modelQualityBatchOutcome(await retryFailedModelQualityHealthSyncs(signal), '模型质量健康同步补偿') })
      scheduler.schedule({ name: backgroundScheduledJobName('account-balance-refresh'), intervalMs: minuteMs, initialDelayMs: 20 * secondMs, stablePhaseWindowMs: 5 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'external-account-maintenance', timeoutMs: 60 * secondMs, failureBackoff: { baseMs: 10 * secondMs, maxMs: 5 * minuteMs }, task: ({ signal }) => runAccountBalanceRefresh({ signal }) })
      scheduleCooldownAccountRetestJob()
      scheduler.schedule({ name: backgroundScheduledJobName('account-api-key-cooldown-retest'), intervalMs: settingsNumber('cooldownAccountRetestIntervalSeconds', 1, 3600) * secondMs, initialDelayMs: accountApiKeyCooldownRetestStartupDelayMs, task: () => runAccountApiKeyCooldownRetest({ settingsNumber }) })
      scheduler.schedule({ name: backgroundScheduledJobName('normal-route-speed-first-recovery-probe'), intervalMs: 10 * secondMs, initialDelayMs: normalRouteSpeedFirstProbeStartupDelayMs, task: runNormalRouteSpeedFirstRecoveryProbe })
      scheduler.schedule({ name: backgroundScheduledJobName('account-circuit-control-plane-maintenance'), intervalMs: 5 * secondMs, initialDelayMs: secondMs, task: runAccountCircuitControlPlaneMaintenance })
      scheduler.schedule({ name: backgroundScheduledJobName('account-circuit-recovery'), intervalMs: 5 * secondMs, initialDelayMs: 5 * secondMs, task: runScheduledAccountCircuitRecovery })
      scheduler.schedule({ name: backgroundScheduledJobName('proxy-latency-refresh'), intervalMs: proxyLatencyRefreshIntervalSeconds * secondMs, initialDelayMs: 4 * minuteMs, stablePhaseWindowMs: 30 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'external-account-maintenance', timeoutMs: 60 * secondMs, failureBackoff: { baseMs: 30 * secondMs, maxMs: 10 * minuteMs }, task: ({ signal }) => runProxyLatencyRefresh(signal) })
      scheduler.schedule({ name: backgroundScheduledJobName('openai-oauth-access-token-refresh'), intervalMs: settingsNumber('oauthAccessTokenRefreshIntervalSeconds', 10, 3600) * secondMs, initialDelayMs: 35 * secondMs, stablePhaseWindowMs: 5 * secondMs, overlapPolicy: 'coalesceOne', resourceLane: 'external-account-maintenance', timeoutMs: 90 * secondMs, failureBackoff: { baseMs: 10 * secondMs, maxMs: 5 * minuteMs }, task: ({ signal }) => runOpenAIOAuthAccessTokenRefresh(signal) })
      return
    default:
      return
  }
}

function scheduleCooldownAccountRetestJob(): void {
  const jobName = 'cooldown-account-retest'
  const ownership = runIfNodeOwnsWorkerJob(runtimeConfig.ownerLock, jobName, () => {
    scheduler.schedule({
      name: backgroundScheduledJobName(jobName),
      intervalMs: settingsNumber('cooldownAccountRetestIntervalSeconds', 1, 3600) * secondMs,
      initialDelayMs: cooldownAccountRetestStartupDelayMs,
      task: () => runCooldownAccountRetest({ settingsNumber })
    })
  })
  if (!ownership.nodeOwns) {
    const logFields = {
      event: 'background_worker_job_owner_drained',
      jobName,
      ownerLockEnabled: runtimeConfig.ownerLock.enabled,
      manifestPath: runtimeConfig.ownerLock.manifestPath,
      configuredDeploymentEpoch: runtimeConfig.ownerLock.deploymentEpoch,
      resolvedOwner: ownership.resolvedOwner,
      manifestSchemaVersion: ownership.schemaVersion,
      reason: ownership.reason,
      detail: ownership.detail
    }
    if (ownership.reason === 'manifest_owner_go') {
      logger.info(logFields, '后台任务 owner 已切换到 Go，Node 不再注册账户冷却复测任务')
    } else {
      logger.error(logFields, '后台任务 owner 无法安全确认，Node 按 fail-closed 策略不注册账户冷却复测任务')
    }
    return
  }
}

function scheduleUsageIngestJobs(): void {
  scheduler.schedule({ name: backgroundScheduledJobName('api-key-record-cleanup-retry'), intervalMs: minuteMs, initialDelayMs: 24 * secondMs, task: runApiKeyRecordCleanupRetry })
  scheduler.schedule({ name: backgroundScheduledJobName('account-record-cleanup-retry'), intervalMs: minuteMs, initialDelayMs: 42 * secondMs, task: runAccountRecordCleanupRetry })
  scheduler.schedule({
    name: backgroundScheduledJobName('data-retention-cleanup'),
    intervalMs: DATA_RETENTION_CLEANUP_INTERVAL_MINUTES * minuteMs,
    initialDelayMs: 450 * secondMs,
    stablePhaseWindowMs: minuteMs,
    scheduleMode: 'fixedDelay',
    resourceLane: 'storage-maintenance',
    timeoutMs: 5 * minuteMs,
    failureBackoff: { baseMs: minuteMs, maxMs: 30 * minuteMs },
    task: ({ signal }) => runWithPostgresScheduledLease('data-retention-cleanup', 10 * minuteMs, signal, runDataRetentionCleanup)
  })
}

function scheduleAccountQualityRefreshJob(): void {
  scheduler.schedule({
    name: backgroundScheduledJobName('account-quality-refresh'),
    intervalMs: settingsNumber('accountQualityRefreshIntervalSeconds', 60, 3600) * secondMs,
    initialDelayMs: 75 * secondMs,
    stablePhaseWindowMs: 30 * secondMs,
    overlapPolicy: 'coalesceOne',
    resourceLane: 'stats-online',
    timeoutMs: minuteMs,
    failureBackoff: { baseMs: 5 * secondMs, maxMs: 5 * minuteMs },
    task: ({ signal }) => runWithPostgresScheduledLease(
      'account-quality-refresh',
      5 * minuteMs,
      signal,
      (leaseSignal, scheduledLease) => runAccountQualityRefresh({
        settingsNumber,
        ensureUsageRecordsIngestedBeforeStatsAggregation: ensureUsageRecordsSafeForStatsAggregation,
        yieldToEventLoop,
        signal: leaseSignal,
        scheduledLease
      })
    )
  })
}

function scheduleLogIngestJobs(): void {
  scheduler.schedule({ name: backgroundScheduledJobName('audit-hot-retention-cleanup'), intervalMs: minuteMs, initialDelayMs: 13 * secondMs, task: runAuditHotRetentionCleanup })
  if (runtimeConfig.log.indexEnabled) {
    scheduler.schedule({ name: backgroundScheduledJobName('runtime-log-index-maintenance'), intervalMs: 60 * minuteMs, initialDelayMs: 9 * minuteMs, stablePhaseWindowMs: minuteMs, scheduleMode: 'fixedDelay', resourceLane: 'storage-maintenance', timeoutMs: 10 * minuteMs, failureBackoff: { baseMs: minuteMs, maxMs: 30 * minuteMs }, task: runRuntimeLogIndexMaintenance })
  }
}

async function runAccountCircuitControlPlaneMaintenance(): Promise<void> {
  const processed = await runGatewayAccountCircuitControlPlaneMaintenance(100)
  if (processed > 0) {
    logger.debug({
      event: 'gateway_account_circuit_control_plane_maintenance_completed',
      processed
    }, '账户电路 control-plane 投影与对账完成')
  }
}

function modelQualityBatchOutcome(
  result: { completed: number; failed: number },
  label: string
): WorkerScheduledJobTaskResult {
  if (result.failed > 0) {
    return {
      outcome: 'partial',
      warning: `${label}部分完成：成功 ${result.completed}，失败 ${result.failed}`
    }
  }
  return { outcome: 'success' }
}

function handleBackgroundJobsStartError(error: unknown, generation: number): void {
  if (generation !== startGeneration || !started) return
  started = false
  logger.error(errorLogFields(error, { event: 'background_jobs_start_failed' }), '后台任务启动失败')
  setImmediate(() => { throw error })
}

export async function triggerAccountHealthCheckNow(
  accountId: string,
  reason: AccountHealthCheckTriggerReason
): Promise<boolean> {
  const batchSize = settingsNumber('accountHealthCheckBatchSize', 1, 100)
  setAccountHealthCheckQueueConcurrency(Math.max(1, Math.min(batchSize, backgroundFullDiagnosticConcurrency)))
  return await enqueueAccountHealthCheckById(accountId, {
    intervalHours: settingsNumber('accountHealthCheckIntervalHours', 1, 168),
    jitterMinutes: settingsNumber('accountHealthCheckJitterMinutes', 0, 1440),
    failureThreshold: settingsNumber('accountHealthCheckFailureThreshold', 1, 10),
    maxPauseMinutes: settingsNumber('defaultTemporaryUnschedulableMinutes', 1, 1440)
  }, reason)
}

function isPostgresHighPerformanceMode(): boolean {
  return runtimeConfig.databaseDriver === 'postgres'
}

function usageStatsOnlineAggregationIntervalSeconds(): number {
  return Math.min(
    settingsNumber('statsAggregationIntervalSeconds', 5, 3600),
    usageStatsOnlineFreshnessMaxIntervalSeconds
  )
}

function usageHotWindowRefreshMinIntervalMs(): number {
  return settingsNumber('usageHotWindowRefreshIntervalSeconds', 60, 3600) * secondMs
}

function scheduleUsageHotWindowRefreshJob(): void {
  scheduler.schedule({
    name: backgroundScheduledJobName('usage-hot-window-refresh'),
    intervalMs: usageHotWindowRefreshMinIntervalMs(),
    initialDelayMs: 25 * secondMs,
    stablePhaseWindowMs: 10 * secondMs,
    overlapPolicy: 'coalesceOne',
    resourceLane: 'stats-heavy',
    timeoutMs: usageHotWindowRefreshTimeoutMs + 5 * secondMs,
    failureBackoff: { baseMs: 10 * secondMs, maxMs: 5 * minuteMs },
    task: ({ signal }) => runScheduledUsageHotWindowRefresh(signal)
  })
}

export function getBackgroundJobRuntimeSnapshots() {
  return scheduler.snapshots()
}

async function runUsageStatsAggregation(_signal: AbortSignal, scheduledLease?: ScheduledJobLeaseFence): Promise<void> {
  if (usageStatsAggregationRunning) return
  usageStatsAggregationRunning = true
  try {
    const safety = await usageStatsAggregationSafety()
    await yieldToEventLoop()
    const batchSize = settingsNumber('statsAggregationBatchSize', 100, 10000)
    const maxBatches = settingsNumber('statsAggregationMaxBatchesPerRun', 1, 100)
    const result = await requestStatsWriter({
      type: 'aggregate_usage_stats',
      batchSize,
      maxBatches,
      maxRunMs: usageStatsAggregationMaxRunMs,
      safeCreatedBefore: safety.safeCreatedBefore,
      scheduledLease
    }, Math.max(10_000, usageStatsAggregationMaxRunMs + 5_000))
    if (result.processed > 0) usageHotWindowRefreshPending = true
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_usage_stats_aggregation_failed' }), '用量统计聚合失败')
    throw error
  } finally {
    usageStatsAggregationRunning = false
  }
}

async function runModelTrustAggregation(signal: AbortSignal, scheduledLease?: ScheduledJobLeaseFence): Promise<void | WorkerScheduledJobTaskResult> {
  if (modelTrustAggregationRunning) return
  modelTrustAggregationRunning = true
  const startedAtMs = Date.now()
  const maxRunMs = 2 * minuteMs
  const batchSize = 100
  try {
    for (let index = 0; index < 10; index += 1) {
      throwIfBackgroundJobAborted(signal, 'model-trust-observation-aggregation')
      if (Date.now() - startedAtMs >= maxRunMs) {
        return { outcome: 'partial', warning: `模型可信 observation 聚合达到 ${maxRunMs}ms 单轮预算` }
      }
      const result = await requestStatsWriter({
        type: 'aggregate_model_trust_observations',
        batchSize,
        scheduledLease
      }, 45 * secondMs) as { processed?: number }
      if ((result.processed ?? 0) < batchSize) break
      throwIfBackgroundJobAborted(signal, 'model-trust-observation-aggregation')
      await yieldToEventLoop()
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_model_trust_aggregation_failed' }), '模型可信 observation 增量聚合失败')
    throw error
  } finally {
    modelTrustAggregationRunning = false
  }
}

async function runScheduledUsageHotWindowRefresh(signal: AbortSignal): Promise<WorkerScheduledJobTaskResult> {
  throwIfBackgroundJobAborted(signal, 'usage-hot-window-refresh')
  const todayKey = dateKey(new Date(), await usageStatsTimezoneAsync())
  const dateChanged = lastUsageHotWindowDateKey !== todayKey
  if (runtimeConfig.databaseDriver !== 'postgres' && !dateChanged && !usageHotWindowRefreshPending) {
    return { outcome: 'skipped', warning: 'no_pending_usage_window_refresh' }
  }

  const reason = dateChanged ? 'date_changed' : 'usage_stats_processed'
  if (reason !== 'date_changed' && Date.now() - lastUsageHotWindowRefreshStartedAtMs < usageHotWindowRefreshMinIntervalMs()) {
    return { outcome: 'skipped', warning: 'usage_window_refresh_throttled' }
  }

  const result = await runWithPostgresScheduledLease(
    'usage-hot-window-refresh',
    2 * minuteMs,
    signal,
    (leaseSignal, scheduledLease) => runUsageHotWindowRefresh(reason, todayKey, leaseSignal, scheduledLease)
  )
  return result ?? { outcome: 'success' }
}

async function runUsageHotWindowRefresh(
  reason: 'usage_stats_processed' | 'date_changed',
  todayKey: string,
  signal: AbortSignal,
  scheduledLease?: ScheduledJobLeaseFence
): Promise<WorkerScheduledJobTaskResult> {
  throwIfBackgroundJobAborted(signal, usageHotWindowRefreshJobName)
  if (usageRankSnapshotsRefreshRunning) {
    usageHotWindowRefreshPending = true
    logger.debug({
      event: 'background_usage_hot_window_refresh_busy',
      reason
    }, '热用量窗口刷新等待已有窗口刷新结束')
    return { outcome: 'skipped', warning: 'usage_rank_snapshot_refresh_busy' }
  }

  usageHotWindowRefreshPending = false
  usageRankSnapshotsRefreshRunning = true
  lastUsageHotWindowDateKey = todayKey
  lastUsageHotWindowRefreshStartedAtMs = Date.now()
  try {
    const result = await requestStatsWriter({
      type: 'refresh_hot_usage_windows',
      jobName: usageHotWindowRefreshJobName,
      scheduledLease
    }, usageHotWindowRefreshTimeoutMs)
    throwIfBackgroundJobAborted(signal, usageHotWindowRefreshJobName)
    if (result.skipped) {
      logger.debug({
        event: 'background_usage_hot_window_refresh_skipped',
        reason,
        sourceWatermark: result.sourceWatermark,
        refreshDate: result.refreshDate,
        durationMs: result.durationMs
      }, '热用量窗口刷新无新增聚合数据，跳过本轮')
      return { outcome: 'skipped', warning: result.skipReason ?? 'source_watermark_unchanged' }
    }
    const slowStages = result.stages.filter((stage) => stage.durationMs >= usageRankSnapshotSlowStageMs)
    if (slowStages.length > 0) {
      logger.warn({
        event: 'background_usage_hot_window_refresh_slow_stages',
        reason,
        durationMs: result.durationMs,
        slowStageCount: slowStages.length,
        slowStages
      }, '热用量窗口刷新存在耗时偏高阶段')
    }
    logger.info({
      event: 'background_usage_hot_window_refresh_completed',
      reason,
      durationMs: result.durationMs,
      stageCount: result.stages.length,
      sourceWatermark: result.sourceWatermark,
      refreshDate: result.refreshDate
    }, '热用量窗口刷新完成')
    return { outcome: 'success' }
  } catch (error) {
    usageHotWindowRefreshPending = true
    logger.error(errorLogFields(error, { event: 'background_usage_hot_window_refresh_failed', reason }), '热用量窗口刷新失败')
    throw error
  } finally {
    usageRankSnapshotsRefreshRunning = false
  }
}

function throwIfBackgroundJobAborted(signal: AbortSignal, jobName: string): void {
  if (!signal.aborted) return
  throw signal.reason instanceof Error
    ? signal.reason
    : new Error(`后台任务已取消：${jobName}`)
}

async function runClientIpStatsAggregation(signal: AbortSignal, scheduledLease?: ScheduledJobLeaseFence): Promise<void> {
  if (clientIpStatsAggregationRunning) return
  clientIpStatsAggregationRunning = true
  try {
    throwIfBackgroundJobAborted(signal, 'client-ip-stats-aggregation')
    await ensureUsageRecordsSafeForStatsAggregation()
    throwIfBackgroundJobAborted(signal, 'client-ip-stats-aggregation')
    await yieldToEventLoop()
    const batchSize = Math.min(settingsNumber('statsAggregationBatchSize', 100, 10000), clientIpStatsAggregationBatchSizeCap)
    const maxBatches = Math.min(settingsNumber('statsAggregationMaxBatchesPerRun', 1, 100), clientIpStatsAggregationMaxBatchesCap)
    await requestStatsWriter({
      type: 'aggregate_client_ip_stats',
      batchSize,
      maxBatches,
      maxRunMs: clientIpStatsAggregationMaxRunMs,
      scheduledLease
    }, Math.max(10_000, clientIpStatsAggregationMaxRunMs + 5_000))
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_client_ip_stats_aggregation_failed' }), 'IP 统计聚合失败')
    throw error
  } finally {
    clientIpStatsAggregationRunning = false
  }
}

async function ensureUsageRecordsSafeForStatsAggregation(): Promise<void> {
  await usageStatsAggregationSafety()
}

async function usageStatsAggregationSafety(): Promise<UsageStatsAggregationSafety> {
  const status = await requestIngestWorkerDrainStatus(6000)
  const flushFailureCount = status?.snapshot?.usageRecordQueue.flushFailureCount ?? 0
  if (!status?.ready || !status.snapshot) {
    throw new Error('ingest-worker 使用记录队列快照不可用，本轮跳过统计聚合，避免统计游标越过排队记录')
  }
  const defaultSafeCreatedBefore = defaultUsageStatsSafeCreatedBeforeIso()
  if (flushFailureCount > 0) {
    throw new Error(`使用记录 ingest 队列已有 ${flushFailureCount} 次写入失败，本轮跳过统计聚合，等待写入队列恢复`)
  }
  const oldestPendingCreatedAt = oldestIso(
    oldestPendingUsageRecordCreatedAt(status),
    await oldestRedisStreamUsageRecordCreatedAtForStatsAggregation()
  )
  return {
    safeCreatedBefore: usageStatsSafeCreatedBeforeForPendingBacklog(defaultSafeCreatedBefore, oldestPendingCreatedAt)
  }
}

async function oldestRedisStreamUsageRecordCreatedAtForStatsAggregation(): Promise<string | undefined> {
  if (runtimeConfig.queueDriver !== 'redis_stream') {
    return undefined
  }
  return await getUsageRecordRedisStreamOldestCreatedAt()
}

function oldestPendingUsageRecordCreatedAt(status: BackgroundWorkerIngestDrainStatus): string | undefined {
  return oldestIso(
    status.pendingQueues.usageRecords.oldestCreatedAt,
    status.snapshot?.usageRecordQueue.oldestCreatedAt
  )
}

function oldestIso(left?: string, right?: string): string | undefined {
  const normalizedLeft = normalizeIsoTime(left)
  const normalizedRight = normalizeIsoTime(right)
  if (!normalizedLeft) return normalizedRight
  if (!normalizedRight) return normalizedLeft
  return Date.parse(normalizedLeft) <= Date.parse(normalizedRight) ? normalizedLeft : normalizedRight
}

function usageStatsSafeCreatedBeforeForPendingBacklog(defaultSafeCreatedBefore: string, oldestPendingCreatedAt: string | undefined): string {
  const normalizedOldestPendingCreatedAt = normalizeIsoTime(oldestPendingCreatedAt)
  if (!normalizedOldestPendingCreatedAt || normalizedOldestPendingCreatedAt > defaultSafeCreatedBefore) {
    return defaultSafeCreatedBefore
  }
  const oldestPendingTime = Date.parse(normalizedOldestPendingCreatedAt)
  if (!Number.isFinite(oldestPendingTime)) {
    return defaultSafeCreatedBefore
  }
  return new Date(Math.max(0, oldestPendingTime - 1)).toISOString()
}

function normalizeIsoTime(value: string | undefined): string | undefined {
  const trimmed = value?.trim()
  if (!trimmed) return undefined
  const time = Date.parse(trimmed)
  return Number.isFinite(time) ? new Date(time).toISOString() : undefined
}

function defaultUsageStatsSafeCreatedBeforeIso(): string {
  const safetyMs = usageStatsCursorSafetyDelaySeconds * 1000
  return new Date(Date.now() - safetyMs).toISOString()
}

async function runGroupAccountStatsRefresh(_signal: AbortSignal, scheduledLease?: ScheduledJobLeaseFence): Promise<void> {
  try {
    if (!groupAccountStatsStartupDirtyMarked && runtimeConfig.databaseDriver === 'postgres') {
      await requestBackgroundWorkerDbService({ type: 'mark_all_group_account_stats_dirty', reason: 'stats_worker_startup_refresh' })
      groupAccountStatsStartupDirtyMarked = true
    }
    await requestStatsWriter({ type: 'refresh_group_account_stats', scheduledLease })
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_group_account_stats_refresh_failed' }), '分组账户统计刷新失败')
    throw error
  }
}

async function runResourceAuthorizationExpirySweep(): Promise<void> {
  try {
    await requestBackgroundWorkerDbService({ type: 'expire_due_resource_authorizations' })
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_resource_authorization_expiry_sweep_failed' }), '资源授权过期扫描失败')
    throw error
  }
}

async function runApiKeyAvailabilityScheduleStatusSync(): Promise<void> {
  try {
    const result = await requestBackgroundWorkerDbService({ type: 'sync_api_key_availability_schedule_statuses' })
    if (!result) {
      throw new Error('DB service 未返回 API Key 时间计划同步结果')
    }
    if (result.changedIds.length > 0) {
      clearGatewayRuntimeCache()
      logger.info({
        event: 'background_api_key_availability_schedule_status_sync_completed',
        scanned: result.scanned,
        activated: result.activated,
        disabled: result.disabled,
        changedCount: result.changedIds.length,
        invalid: result.invalid
      }, 'API Key 时间计划边界执行完成')
    }
    if (result.invalid > 0) {
      logger.warn({
        event: 'background_api_key_availability_schedule_status_sync_invalid',
        invalid: result.invalid,
        apiKeyIds: result.invalidIds.slice(0, 20)
      }, 'API Key 时间计划存在无效配置，已跳过')
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_api_key_availability_schedule_status_sync_failed' }), 'API Key 时间计划边界执行失败')
    throw error
  }
}

async function runAccountAvailabilityScheduleStatusSync(): Promise<void> {
  try {
    const result = await requestBackgroundWorkerDbService({ type: 'sync_account_availability_schedule_statuses' })
    if (!result) {
      throw new Error('DB service 未返回账户时间计划同步结果')
    }
    if (result.changedIds.length > 0) {
      clearGatewayRuntimeCache()
      logger.info({
        event: 'background_account_availability_schedule_status_sync_completed',
        scanned: result.scanned,
        activated: result.activated,
        disabled: result.disabled,
        changedCount: result.changedIds.length,
        invalid: result.invalid
      }, '账户时间计划运行态同步完成')
    }
    if (result.invalid > 0) {
      logger.warn({
        event: 'background_account_availability_schedule_status_sync_invalid',
        invalid: result.invalid,
        accountIds: result.invalidIds.slice(0, 20)
      }, '账户时间计划存在无效配置，已按不可调度处理')
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_account_availability_schedule_status_sync_failed' }), '账户时间计划运行态同步失败')
    throw error
  }
}

async function runSystemMetricsSample(): Promise<void> {
  const memoryMetrics = await currentMemoryMetrics()
  const memoryUsage = process.memoryUsage()
  const networkMetrics = await currentNetworkMetrics()
  const registryProcessSamples = runtimeConfig.runtimeMode === 'performance'
    ? await readPerformanceProcessEventLoopSamples().catch((error) => {
      logger.warn(errorLogFields(error, {
        event: 'background_system_metrics_registry_event_loop_sample_failed'
      }), '高性能进程指标注册表读取失败，回退 IPC 采样')
      return []
    })
    : []
  const registryComplete = runtimeConfig.runtimeMode === 'performance'
    && performanceProcessMetricsTopologyComplete(registryProcessSamples, runtimeConfig.topology)
  const ipcProcessSamples = registryComplete
    ? undefined
    : await requestServerProcessEventLoopSamples().catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'background_system_metrics_remote_event_loop_sample_failed'
    }), '系统指标远端事件循环采样失败')
    return undefined
  })
  const remoteProcessSamples = [
    ...registryProcessSamples,
    ...(ipcProcessSamples ?? [])
  ]
  const remoteProcessSamplesComplete = runtimeConfig.runtimeMode === 'performance'
    ? performanceProcessMetricsTopologyComplete(remoteProcessSamples, runtimeConfig.topology)
    : remoteProcessSamples.length > 0
  if (!remoteProcessSamplesComplete) {
    missingRemoteProcessEventLoopSampleWarningCount += 1
    if (missingRemoteProcessEventLoopSampleWarningCount === 1 || missingRemoteProcessEventLoopSampleWarningCount % 10 === 0) {
      logger.warn({
        event: 'background_system_metrics_remote_event_loop_sample_missing',
        missingRemoteProcessEventLoopSampleWarningCount,
        registryProcessSampleCount: registryProcessSamples.length,
        ipcProcessSampleCount: ipcProcessSamples?.length ?? 0,
        observedProcessRoles: [...new Set(remoteProcessSamples.map((sample) => sample.processRole))].sort()
      }, '系统指标远端事件循环采样不完整')
    }
  } else {
    missingRemoteProcessEventLoopSampleWarningCount = 0
  }
  const localProcessEventLoopSample = buildProcessEventLoopSample()
  const processEventLoopSamples = mergeProcessEventLoopSamples([
    localProcessEventLoopSample,
    ...(remoteProcessSamples ?? [])
  ])
  try {
    await requestStatsWriter({
      type: 'record_system_metrics_sample',
      sample: {
      cpuPercent: currentCpuPercent(),
      memoryUsedPercent: memoryMetrics.memoryUsedPercent,
      memoryTotalBytes: memoryMetrics.memoryTotalBytes,
      memoryFreeBytes: memoryMetrics.memoryFreeBytes,
      processRssBytes: memoryUsage.rss,
      processHeapUsedBytes: memoryUsage.heapUsed,
      processHeapTotalBytes: memoryUsage.heapTotal,
      eventLoopLagMs: localProcessEventLoopSample.eventLoopLagMs,
      ...networkMetrics,
      dbFileBytes: await databaseFileBytes(),
      statsLagSeconds: await latestUsageStatsLagSecondsForRuntime()
      },
      processEventLoopSamples
    })
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_system_metrics_sample_failed' }), '系统指标采样失败')
    throw error
  }
}

function mergeProcessEventLoopSamples(samples: ReturnType<typeof buildProcessEventLoopSample>[]): ReturnType<typeof buildProcessEventLoopSample>[] {
  const latestByRole = new Map<string, ReturnType<typeof buildProcessEventLoopSample>>()
  for (const sample of samples) {
    const existing = latestByRole.get(sample.processRole)
    if (!existing || sample.sampledAt > existing.sampledAt) {
      latestByRole.set(sample.processRole, sample)
    }
  }
  return [...latestByRole.values()]
}

async function runOpenAIOAuthAccessTokenRefresh(signal?: AbortSignal): Promise<WorkerScheduledJobTaskResult> {
  try {
    const result = await refreshDueOpenAIOAuthAccessTokens({ signal })
    if (result.refreshed > 0 || result.failed > 0 || result.exceptioned > 0 || result.cooldowned > 0) {
      logger.info({
        event: 'background_openai_oauth_access_token_refresh_completed',
        ...result
      }, 'OpenAI OAuth Access Token 刷新完成')
    }
    if (result.failed > 0 || result.deferredBudget > 0) {
      return {
        outcome: 'partial',
        warning: `OAuth 刷新部分完成：失败 ${result.failed}，预算延期 ${result.deferredBudget}`
      }
    }
    return { outcome: 'success' }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_openai_oauth_access_token_refresh_failed' }), 'OpenAI OAuth Access Token 刷新失败')
    throw error
  }
}

async function runProxyLatencyRefresh(signal?: AbortSignal): Promise<WorkerScheduledJobTaskResult> {
  try {
    const result = await refreshProxyLatencyBatch({ limit: proxyLatencyRefreshBatchSize, signal })
    if (result.processedCount > 0 || result.warning) {
      logger.info({ event: 'background_proxy_latency_refresh_completed', ...result }, '代理延迟刷新完成')
    }
    return result
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_proxy_latency_refresh_failed' }), '代理延迟刷新失败')
    throw error
  }
}

async function runRuntimeLogIndexMaintenance(): Promise<void> {
  try {
    if (!runtimeConfig.log.indexEnabled) {
      return
    }
    if (runtimeConfig.databaseDriver !== 'postgres') {
      ensureRuntimeLogFacetSnapshots()
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_runtime_log_index_maintenance_failed' }), '运行日志索引维护失败')
    throw error
  }
}

function settingsNumber(key: string, min: number, max: number): number {
  const value = runtimeConfig.databaseDriver === 'postgres'
    ? postgresBackgroundJobSettingValue(key)
    : sqliteBackgroundJobSettingValue(key)
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value)) {
    throw new Error(`系统设置 ${key} 必须是整数`)
  }
  if (value < min || value > max) {
    throw new Error(`系统设置 ${key} 必须在 ${min} 到 ${max} 之间`)
  }
  return value
}

function sqliteBackgroundJobSettingValue(key: string): unknown {
  try {
    return getSettings()[key]
  } catch (error) {
    if (!isMissingSystemSettingsTableError(error)) {
      throw error
    }
    if (!sqliteSettingsTableMissingWarningLogged) {
      sqliteSettingsTableMissingWarningLogged = true
      logger.warn(errorLogFields(error, {
        event: 'background_job_settings_table_missing_default'
      }), '后台任务启动时系统设置表尚未初始化，将临时使用默认设置')
    }
    return defaultSystemSettingsByKey.get(key)
  }
}

function postgresBackgroundJobSettingValue(key: string): unknown {
  void refreshBackgroundJobSettingsSnapshotIfNeeded()
  return backgroundJobSettingsSnapshot?.[key] ?? defaultSystemSettingsByKey.get(key)
}

function isMissingSystemSettingsTableError(error: unknown): boolean {
  return error instanceof Error && error.message.includes('no such table: system_settings')
}

function refreshBackgroundJobSettingsSnapshotIfNeeded(): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') return Promise.resolve()
  if (backgroundJobSettingsRefreshPromise) return backgroundJobSettingsRefreshPromise
  if (backgroundJobSettingsSnapshot && Date.now() - backgroundJobSettingsSnapshotLoadedAt < backgroundJobSettingsSnapshotTtlMs) return Promise.resolve()
  backgroundJobSettingsRefreshPromise = getSettingsAsync()
    .then((settings) => {
      backgroundJobSettingsSnapshot = settings
      backgroundJobSettingsSnapshotLoadedAt = Date.now()
    })
    .catch((error) => {
      logger.warn(errorLogFields(error, { event: 'background_job_settings_snapshot_refresh_failed' }), '后台任务系统设置快照刷新失败，将临时使用默认设置')
    })
    .finally(() => {
      backgroundJobSettingsRefreshPromise = undefined
    })
  return backgroundJobSettingsRefreshPromise
}

async function databaseFileBytes(): Promise<number | undefined> {
  try {
    const databasePaths = new Set([runtimeConfig.databasePath, datasetDatabasePath(), usageCatalogDatabasePath(), statsDatabasePath()])
    let totalBytes = 0
    for (const databasePath of databasePaths) {
      totalBytes += await fileSize(databasePath)
    }
    return totalBytes || undefined
  } catch {
    return undefined
  }
}

async function fileSize(path: string): Promise<number> {
  try {
    return (await statFile(path)).size
  } catch (error) {
    return isMissingFileError(error) ? 0 : 0
  }
}

async function runTableStorageMonitor(_signal: AbortSignal, scheduledLease?: ScheduledJobLeaseFence): Promise<void> {
  try {
    await requestStatsWriter({
      type: 'collect_table_storage_snapshot',
      sampledAt: nowIso(),
      options: {
      tableScanMode: 'cursor',
      maxTablesPerDatabase: settingsNumber('tableMonitorMaxTablesPerRun', 0, 100)
      },
      scheduledLease
    }, 20_000)
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_table_storage_monitor_failed' }), '表数据监控采样失败')
    throw error
  }
}

async function runLeasedUsageRankSnapshotsRefresh(
  jobName: Parameters<typeof backgroundScheduledJobName>[0],
  stageNames: UsageRankSnapshotStageName[],
  signal: AbortSignal,
  leaseTtlMs = 15 * minuteMs
): Promise<void | WorkerScheduledJobTaskResult> {
  return await runWithPostgresScheduledLease(jobName, leaseTtlMs, signal, async (_leaseSignal, scheduledLease) => {
    await runUsageRankSnapshotsRefresh(backgroundScheduledJobName(jobName), stageNames, scheduledLease)
  })
}

async function runUsageRankSnapshotsRefresh(jobName: string, stageNames: UsageRankSnapshotStageName[], scheduledLease?: ScheduledJobLeaseFence): Promise<void> {
  if (usageRankSnapshotsRefreshRunning) {
    logger.debug({
      event: 'background_usage_rank_snapshots_refresh_busy',
      jobName,
      stageNames
    }, '用量排行快照刷新仍在运行，跳过本轮')
    return
  }
  usageRankSnapshotsRefreshRunning = true
  try {
    const result = await requestStatsWriter({
      type: 'refresh_usage_rank_snapshots',
      stageNames,
      jobName,
      scheduledLease
    }, 30_000)
    if (result.skipped) {
      logger.debug({
        event: 'background_usage_rank_snapshots_refresh_skipped',
        jobName,
        stageNames,
        sourceWatermark: result.sourceWatermark,
        refreshDate: result.refreshDate,
        skipReason: result.skipReason,
        durationMs: result.durationMs
      }, '用量排行快照刷新无新增聚合数据，跳过本轮')
      return
    }
    const slowStages = result.stages.filter((stage) => stage.durationMs >= usageRankSnapshotSlowStageMs)
    if (slowStages.length > 0) {
      logger.warn({
        event: 'background_usage_rank_snapshots_refresh_slow_stages',
        jobName,
        durationMs: result.durationMs,
        slowStageCount: slowStages.length,
        slowStages
      }, '用量排行快照刷新存在耗时偏高阶段')
    }
    logger.info({
      event: 'background_usage_rank_snapshots_refresh_completed',
      jobName,
      durationMs: result.durationMs,
      stageCount: result.stages.length,
      sourceWatermark: result.sourceWatermark,
      refreshDate: result.refreshDate,
      topStages: [...result.stages]
        .sort((left, right) => right.durationMs - left.durationMs)
        .slice(0, 5)
    }, '用量排行快照刷新完成')
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_usage_rank_snapshots_refresh_failed', jobName, stageNames }), '用量排行快照刷新失败')
    throw error
  } finally {
    usageRankSnapshotsRefreshRunning = false
  }
}

async function runUsageStatsConsistencyCheck(): Promise<void> {
  try {
    const issues = await requestStatsWriter({
      type: 'check_usage_stats_consistency',
      limit: 20
    })
    if (!issues.length) return
    logger.warn({
      event: 'usage_stats_consistency_mismatch',
      issueCount: issues.length,
      issues: issues.slice(0, 20)
    }, '用量统计聚合桶一致性校验发现差异')
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_usage_stats_consistency_check_failed' }), '用量统计聚合桶一致性校验失败')
    throw error
  }
}

function isMissingFileError(error: unknown): boolean {
  return typeof error === 'object' && error !== null && 'code' in error && (error as { code?: unknown }).code === 'ENOENT'
}

function yieldToEventLoop(): Promise<void> {
  return new Promise((resolve) => setImmediate(resolve))
}
