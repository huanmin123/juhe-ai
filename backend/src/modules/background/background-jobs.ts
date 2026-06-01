import { execFile } from 'node:child_process'
import { readFile, stat as statFile } from 'node:fs/promises'
import { cpus, freemem, platform, totalmem } from 'node:os'

import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { buildProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import { datasetDatabasePath, nowIso, statsDatabasePath } from '../../storage/database.js'
import {
  expireDueResourceAuthorizations,
  getSettings,
  listAccountsDueForCooldownRetest,
  refreshAccountQualityFromUsage
} from '../../storage/repositories.js'
import {
  aggregateUsageStatsBatch,
  checkUsageStatsConsistency,
  insertProcessEventLoopSample,
  insertSystemMetricsSample,
  latestUsageStatsLagSeconds,
  refreshDirtyGroupAccountStatsCache,
  refreshUsageQuotaHourlyWindowsCache,
  refreshUsageRankSnapshotsInStages,
  type UsageRankSnapshotStageName
} from '../../storage/usage-stats.repository.js'
import {
  aggregateClientIpStatsBatch,
  refreshClientIpUsageRangeWindows
} from '../../storage/client-ip-stats.repository.js'
import { buildGatewayQuotaSnapshot } from '../../storage/gateway-quota-snapshot.repository.js'
import { collectTableStorageSnapshot } from '../../storage/table-monitor.repository.js'
import { refreshDueOpenAIOAuthAccessTokens } from '../openai-oauth/openai-oauth-access-token-refresh.service.js'
import { proxyLatencyRefreshBatchSize, proxyLatencyRefreshIntervalSeconds, refreshProxyLatencyBatch } from '../proxies/proxy-test.service.js'
import { flushUsageRecordQueue, pendingUsageRecordCount } from '../gateway/usage-record-queue.service.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'
import { flushRuntimeLogIndexQueue } from '../runtime-logs/runtime-log-index-queue.service.js'
import { ensureRuntimeLogFacetSnapshots } from '../../storage/runtime-logs.repository.js'
import { cleanupPendingDeletedAccountRecordTargetsAsync } from '../../storage/account-record-cleanup.js'
import { cleanupPendingDeletedApiKeyRecordTargetsAsync } from '../../storage/api-key-record-cleanup.js'
import { cleanupExpiredRetainedData } from './data-retention-cleanup.service.js'
import { requestServerProcessEventLoopSamples, sendGatewayQuotaSnapshotToServer } from './background-ipc.js'
import { enqueueCooldownAccountRetest, getCooldownAccountRetestQueueSnapshot } from './cooldown-account-retest.service.js'
import { WorkerScheduler } from './worker-scheduler.js'

let started = false
let usageStatsAggregationRunning = false
let clientIpStatsAggregationRunning = false
let usageRankSnapshotsRefreshRunning = false
let missingRemoteProcessEventLoopSampleWarningCount = 0
let previousCpuSnapshot = cpuSnapshot()
let previousNetworkSnapshot: NetworkCounterSnapshot | undefined
const dailyIntervalMs = 24 * 60 * 60 * 1000
const secondMs = 1000
const minuteMs = 60 * secondMs
const usageRecordPreAggregationFlushMaxBatches = 2
const clientIpStatsAggregationBatchSizeCap = 1000
const clientIpStatsAggregationMaxBatchesCap = 10
const clientIpStatsAggregationMaxRunMs = 5000
const usageRankSnapshotSlowStageMs = 1000
const usageRankSnapshotCoreStageNames: UsageRankSnapshotStageName[] = [
  'account_last7d_request_rank',
  'caller_account_last7d_request_rank',
  'api_key_current_month_cost_rank',
  'account_authorization_current_month_cost_rank',
  'group_authorization_current_month_cost_rank',
  'ai_performance_summary_windows'
]
const systemMetricsTrendStageNames: UsageRankSnapshotStageName[] = ['system_metrics_trend_windows']
const usageOverviewWindowStageNames: UsageRankSnapshotStageName[] = ['usage_overview_windows']
const usageScopeRangeWindowStageNames: UsageRankSnapshotStageName[] = ['usage_scope_range_windows']
const authorizationUsageRangeWindowStageNames: UsageRankSnapshotStageName[] = ['authorization_usage_range_windows']
const scheduler = new WorkerScheduler()

export function startBackgroundJobs(): void {
  if (started) return
  started = true

  scheduler.schedule({ name: 'usage-stats-aggregation', intervalMs: settingsNumber('statsAggregationIntervalSeconds', 5, 3600) * secondMs, task: runUsageStatsAggregation })
  scheduler.schedule({ name: 'client-ip-stats-aggregation', intervalMs: settingsNumber('statsAggregationIntervalSeconds', 5, 3600) * secondMs, initialDelayMs: 8 * secondMs, task: runClientIpStatsAggregation })
  scheduler.schedule({ name: 'group-account-stats-refresh', intervalMs: settingsNumber('groupAccountStatsRefreshIntervalSeconds', 5, 3600) * secondMs, initialDelayMs: 16 * secondMs, task: runGroupAccountStatsRefresh })
  scheduler.schedule({ name: 'usage-rank-snapshots-refresh', intervalMs: 30 * minuteMs, initialDelayMs: 2 * minuteMs + 30 * secondMs, task: () => runUsageRankSnapshotsRefresh('usage-rank-snapshots-refresh', usageRankSnapshotCoreStageNames) })
  scheduler.schedule({ name: 'system-metrics-trend-windows-refresh', intervalMs: 30 * minuteMs, initialDelayMs: 3 * minuteMs + 20 * secondMs, task: () => runUsageRankSnapshotsRefresh('system-metrics-trend-windows-refresh', systemMetricsTrendStageNames) })
  scheduler.schedule({ name: 'usage-overview-windows-refresh', intervalMs: 30 * minuteMs, initialDelayMs: 4 * minuteMs + 10 * secondMs, task: () => runUsageRankSnapshotsRefresh('usage-overview-windows-refresh', usageOverviewWindowStageNames) })
  scheduler.schedule({ name: 'usage-scope-range-windows-refresh', intervalMs: 30 * minuteMs, initialDelayMs: 5 * minuteMs, task: () => runUsageRankSnapshotsRefresh('usage-scope-range-windows-refresh', usageScopeRangeWindowStageNames) })
  scheduler.schedule({ name: 'authorization-usage-range-windows-refresh', intervalMs: 30 * minuteMs, initialDelayMs: 5 * minuteMs + 50 * secondMs, task: () => runUsageRankSnapshotsRefresh('authorization-usage-range-windows-refresh', authorizationUsageRangeWindowStageNames) })
  scheduler.schedule({ name: 'usage-stats-consistency-check', intervalMs: 60 * minuteMs, initialDelayMs: 11 * minuteMs, task: runUsageStatsConsistencyCheck })
  scheduler.schedule({ name: 'api-key-record-cleanup-retry', intervalMs: minuteMs, initialDelayMs: 24 * secondMs, task: runApiKeyRecordCleanupRetry })
  scheduler.schedule({ name: 'account-record-cleanup-retry', intervalMs: minuteMs, initialDelayMs: 42 * secondMs, task: runAccountRecordCleanupRetry })
  scheduler.schedule({ name: 'resource-authorization-expiry-sweep', intervalMs: minuteMs, initialDelayMs: 54 * secondMs, task: runResourceAuthorizationExpirySweep })
  scheduler.schedule({ name: 'system-metrics-sample', intervalMs: settingsNumber('systemMetricsSampleIntervalSeconds', 5, 3600) * secondMs, initialDelayMs: 5 * secondMs, task: runSystemMetricsSample })
  scheduler.schedule({ name: 'table-storage-monitor', intervalMs: 10 * minuteMs, initialDelayMs: 3 * minuteMs, task: runTableStorageMonitor })
  scheduler.schedule({ name: 'proxy-latency-refresh', intervalMs: proxyLatencyRefreshIntervalSeconds * secondMs, initialDelayMs: 4 * minuteMs, task: runProxyLatencyRefresh })
  scheduler.schedule({ name: 'account-quality-refresh', intervalMs: settingsNumber('accountQualityRefreshIntervalSeconds', 60, 3600) * secondMs, initialDelayMs: 75 * secondMs, task: runAccountQualityRefresh })
  scheduler.schedule({ name: 'openai-oauth-access-token-refresh', intervalMs: settingsNumber('oauthAccessTokenRefreshIntervalSeconds', 10, 3600) * secondMs, initialDelayMs: 35 * secondMs, task: runOpenAIOAuthAccessTokenRefresh })
  scheduler.schedule({ name: 'cooldown-account-retest', intervalMs: settingsNumber('cooldownAccountRetestIntervalSeconds', 1, 3600) * secondMs, initialDelayMs: 2 * secondMs, task: runCooldownAccountRetest })
  scheduler.schedule({ name: 'runtime-log-index-maintenance', intervalMs: 60 * minuteMs, initialDelayMs: 7 * minuteMs, task: runRuntimeLogIndexMaintenance })
  scheduler.schedule({ name: 'data-retention-cleanup', intervalMs: dailyIntervalMs, initialDelayMs: 13 * minuteMs, task: runDataRetentionCleanup })
}

export function getBackgroundJobRuntimeSnapshots() {
  return scheduler.snapshots()
}

async function runUsageStatsAggregation(): Promise<void> {
  if (usageStatsAggregationRunning) return
  usageStatsAggregationRunning = true
  try {
    flushUsageRecordsBeforeStatsAggregation()
    await yieldToEventLoop()
    const batchSize = settingsNumber('statsAggregationBatchSize', 100, 10000)
    const maxBatches = settingsNumber('statsAggregationMaxBatchesPerRun', 1, 100)
    for (let index = 0; index < maxBatches; index += 1) {
      const processed = aggregateUsageStatsBatch(batchSize)
      if (processed < batchSize) break
      await yieldToEventLoop()
    }
    await yieldToEventLoop()
    refreshUsageQuotaHourlyWindowsCache()
    sendGatewayQuotaSnapshotToServer(buildGatewayQuotaSnapshot())
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_usage_stats_aggregation_failed' }), '用量统计聚合失败')
    throw error
  } finally {
    usageStatsAggregationRunning = false
  }
}

async function runClientIpStatsAggregation(): Promise<void> {
  if (clientIpStatsAggregationRunning) return
  clientIpStatsAggregationRunning = true
  try {
    flushUsageRecordsBeforeStatsAggregation()
    await yieldToEventLoop()
    const batchSize = Math.min(settingsNumber('statsAggregationBatchSize', 100, 10000), clientIpStatsAggregationBatchSizeCap)
    const maxBatches = Math.min(settingsNumber('statsAggregationMaxBatchesPerRun', 1, 100), clientIpStatsAggregationMaxBatchesCap)
    const startedAtMs = Date.now()
    for (let index = 0; index < maxBatches; index += 1) {
      const processed = aggregateClientIpStatsBatch(batchSize)
      if (processed < batchSize) break
      await yieldToEventLoop()
      if (Date.now() - startedAtMs >= clientIpStatsAggregationMaxRunMs) break
    }
    await yieldToEventLoop()
    refreshClientIpUsageRangeWindows()
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_client_ip_stats_aggregation_failed' }), 'IP 统计聚合失败')
    throw error
  } finally {
    clientIpStatsAggregationRunning = false
  }
}

function flushUsageRecordsBeforeStatsAggregation(): void {
  flushUsageRecordQueue({
    drain: true,
    retryOnFailure: false,
    maxBatches: usageRecordPreAggregationFlushMaxBatches
  })
  const pendingCount = pendingUsageRecordCount()
  if (pendingCount > 0) {
    throw new Error(`使用记录队列仍有 ${pendingCount} 条未落库，本轮跳过统计聚合，避免统计游标越过排队记录`)
  }
}

async function runGroupAccountStatsRefresh(): Promise<void> {
  try {
    refreshDirtyGroupAccountStatsCache()
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_group_account_stats_refresh_failed' }), '分组账户统计刷新失败')
    throw error
  }
}

async function runResourceAuthorizationExpirySweep(): Promise<void> {
  try {
    expireDueResourceAuthorizations()
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_resource_authorization_expiry_sweep_failed' }), '资源授权过期扫描失败')
    throw error
  }
}

async function runApiKeyRecordCleanupRetry(): Promise<void> {
  try {
    const summary = await cleanupPendingDeletedApiKeyRecordTargetsAsync(1)
    if (summary.attempted > 0) {
      logger.info({
        event: 'background_api_key_record_cleanup_retry_completed',
        ...summary
      }, '已删除 API Key 关联数据清理重试完成')
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_api_key_record_cleanup_retry_failed' }), '已删除 API Key 关联数据清理重试失败')
    throw error
  }
}

async function runAccountRecordCleanupRetry(): Promise<void> {
  try {
    const summary = await cleanupPendingDeletedAccountRecordTargetsAsync(1)
    if (summary.attempted > 0) {
      logger.info({
        event: 'background_account_record_cleanup_retry_completed',
        ...summary
      }, '已删除 AI 账户关联数据清理重试完成')
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_account_record_cleanup_retry_failed' }), '已删除 AI 账户关联数据清理重试失败')
    throw error
  }
}

async function runSystemMetricsSample(): Promise<void> {
  const memoryMetrics = await currentMemoryMetrics()
  const memoryUsage = process.memoryUsage()
  const networkMetrics = await currentNetworkMetrics()
  const remoteProcessSamples = await requestServerProcessEventLoopSamples().catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'background_system_metrics_remote_event_loop_sample_failed'
    }), '系统指标远端事件循环采样失败')
    return undefined
  })
  if (!remoteProcessSamples || remoteProcessSamples.length === 0) {
    missingRemoteProcessEventLoopSampleWarningCount += 1
    if (missingRemoteProcessEventLoopSampleWarningCount === 1 || missingRemoteProcessEventLoopSampleWarningCount % 10 === 0) {
      logger.warn({
        event: 'background_system_metrics_remote_event_loop_sample_missing',
        missingRemoteProcessEventLoopSampleWarningCount
      }, '系统指标远端事件循环采样缺失')
    }
  } else {
    missingRemoteProcessEventLoopSampleWarningCount = 0
  }
  const processEventLoopSamples = remoteProcessSamples ?? []
  const workerEventLoopSample = buildProcessEventLoopSample('worker')
  try {
    insertSystemMetricsSample({
      cpuPercent: currentCpuPercent(),
      memoryUsedPercent: memoryMetrics.memoryUsedPercent,
      memoryTotalBytes: memoryMetrics.memoryTotalBytes,
      memoryFreeBytes: memoryMetrics.memoryFreeBytes,
      processRssBytes: memoryUsage.rss,
      processHeapUsedBytes: memoryUsage.heapUsed,
      processHeapTotalBytes: memoryUsage.heapTotal,
      eventLoopLagMs: workerEventLoopSample.eventLoopLagMs,
      ...networkMetrics,
      dbFileBytes: await databaseFileBytes(),
      statsLagSeconds: latestUsageStatsLagSeconds()
    })
    for (const sample of [workerEventLoopSample, ...processEventLoopSamples]) {
      insertProcessEventLoopSample(sample)
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_system_metrics_sample_failed' }), '系统指标采样失败')
    throw error
  }
}

interface MemoryMetricsSample {
  memoryTotalBytes: number
  memoryFreeBytes: number
  memoryUsedPercent?: number
}

async function currentMemoryMetrics(): Promise<MemoryMetricsSample> {
  const memoryTotalBytes = totalmem()
  if (platform() === 'darwin') {
    const darwinMetrics = await readDarwinMemoryMetrics(memoryTotalBytes)
    if (darwinMetrics) return darwinMetrics
  }

  const memoryFreeBytes = freemem()
  return {
    memoryTotalBytes,
    memoryFreeBytes,
    memoryUsedPercent: percentUsed(memoryTotalBytes, memoryFreeBytes)
  }
}

async function readDarwinMemoryMetrics(memoryTotalBytes: number): Promise<MemoryMetricsSample | undefined> {
  try {
    const stdout = await execFileText('vm_stat', [], 3000)
    return parseDarwinVmStat(stdout, memoryTotalBytes)
  } catch {
    return undefined
  }
}

function parseDarwinVmStat(output: string, memoryTotalBytes: number): MemoryMetricsSample | undefined {
  const pageSize = Number(output.match(/page size of\s+(\d+)\s+bytes/i)?.[1])
  if (!Number.isFinite(pageSize) || pageSize <= 0 || memoryTotalBytes <= 0) return undefined

  const pages = new Map<string, number>()
  for (const line of output.split('\n')) {
    const match = line.match(/^\s*"?([^":]+)"?:\s+([0-9.]+)\.?\s*$/)
    if (!match) continue
    const value = Number(match[2].replace(/\./g, ''))
    if (Number.isFinite(value)) {
      pages.set(match[1].trim().toLowerCase(), value)
    }
  }

  const anonymousPages = pages.get('anonymous pages')
  const wiredPages = pages.get('pages wired down')
  const compressorPages = pages.get('pages occupied by compressor')
  if (anonymousPages !== undefined && wiredPages !== undefined && compressorPages !== undefined) {
    const usedBytes = clampBytes((anonymousPages + wiredPages + compressorPages) * pageSize, memoryTotalBytes)
    const memoryFreeBytes = memoryTotalBytes - usedBytes
    return {
      memoryTotalBytes,
      memoryFreeBytes,
      memoryUsedPercent: percentUsed(memoryTotalBytes, memoryFreeBytes)
    }
  }

  const freePages = pages.get('pages free')
  const inactivePages = pages.get('pages inactive')
  const speculativePages = pages.get('pages speculative')
  if (freePages === undefined || inactivePages === undefined || speculativePages === undefined) return undefined

  const memoryFreeBytes = clampBytes((freePages + inactivePages + speculativePages) * pageSize, memoryTotalBytes)
  return {
    memoryTotalBytes,
    memoryFreeBytes,
    memoryUsedPercent: percentUsed(memoryTotalBytes, memoryFreeBytes)
  }
}

function percentUsed(memoryTotalBytes: number, memoryFreeBytes: number): number | undefined {
  return memoryTotalBytes > 0 ? ((memoryTotalBytes - memoryFreeBytes) / memoryTotalBytes) * 100 : undefined
}

function clampBytes(value: number, max: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.min(Math.max(Math.round(value), 0), max)
}

async function runOpenAIOAuthAccessTokenRefresh(): Promise<void> {
  try {
    const result = await refreshDueOpenAIOAuthAccessTokens()
    if (result.refreshed > 0 || result.failed > 0 || result.exceptioned > 0 || result.cooldowned > 0) {
      logger.info({
        event: 'background_openai_oauth_access_token_refresh_completed',
        ...result
      }, 'OpenAI OAuth Access Token 刷新完成')
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_openai_oauth_access_token_refresh_failed' }), 'OpenAI OAuth Access Token 刷新失败')
    throw error
  }
}

async function runProxyLatencyRefresh(): Promise<void> {
  try {
    await refreshProxyLatencyBatch(proxyLatencyRefreshBatchSize)
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_proxy_latency_refresh_failed' }), '代理延迟刷新失败')
    throw error
  }
}

async function runAccountQualityRefresh(): Promise<void> {
  try {
    flushUsageRecordQueue({
      drain: true,
      retryOnFailure: false,
      maxBatches: usageRecordPreAggregationFlushMaxBatches
    })
    await yieldToEventLoop()
    const windowMinutes = settingsNumber('accountQualityWindowMinutes', 1, 60)
    const realtimeResult = refreshAccountQualityFromUsage(windowMinutes)
    if (realtimeResult.refreshed > 0 || realtimeResult.removed > 0) {
      clearGatewayRuntimeCache()
      logger.info({
        event: 'background_account_quality_refresh_completed',
        realtimeRefreshed: realtimeResult.refreshed,
        realtimeRemoved: realtimeResult.removed
      }, '账户质量缓存刷新完成')
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_account_quality_refresh_failed' }), '账户质量缓存刷新失败')
    throw error
  }
}

async function runCooldownAccountRetest(): Promise<void> {
  const batchSize = settingsNumber('cooldownAccountRetestBatchSize', 1, 100)
  const model = settingsString('cooldownAccountRetestModel')
  const maxPauseMinutes = settingsNumber('defaultTemporaryUnschedulableMinutes', 1, 1440)
  const maxRecoveryHours = settingsNumber('cooldownAccountRetestMaxBackoffHours', 1, 24 * 30)
  const candidates = listAccountsDueForCooldownRetest(batchSize)
  const startedAtMs = Date.now()
  let enqueuedCount = 0
  let skippedQueuedCount = 0
  for (const account of candidates) {
    if (enqueueCooldownAccountRetest(account, model, { maxPauseMinutes, maxRecoveryHours })) {
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

async function runRuntimeLogIndexMaintenance(): Promise<void> {
  try {
    flushRuntimeLogIndexQueue({ drain: true, retryOnFailure: false })
    ensureRuntimeLogFacetSnapshots()
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_runtime_log_index_maintenance_failed' }), '运行日志索引维护失败')
    throw error
  }
}

async function runDataRetentionCleanup(): Promise<void> {
  await cleanupExpiredRetainedData()
}

function settingsNumber(key: string, min: number, max: number): number {
  const value = getSettings()[key]
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value)) {
    throw new Error(`系统设置 ${key} 必须是整数`)
  }
  if (value < min || value > max) {
    throw new Error(`系统设置 ${key} 必须在 ${min} 到 ${max} 之间`)
  }
  return value
}

function settingsString(key: string): string {
  const value = getSettings()[key]
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`系统设置 ${key} 必须是非空字符串`)
  }
  return value.trim()
}

async function databaseFileBytes(): Promise<number | undefined> {
  try {
    const databasePaths = new Set([runtimeConfig.databasePath, datasetDatabasePath(), statsDatabasePath()])
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

async function runTableStorageMonitor(): Promise<void> {
  try {
    collectTableStorageSnapshot(nowIso(), {
      tableScanMode: 'cursor',
      maxTablesPerDatabase: settingsNumber('tableMonitorMaxTablesPerRun', 0, 100)
    })
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_table_storage_monitor_failed' }), '表数据监控采样失败')
    throw error
  }
}

interface CpuSnapshot {
  idle: number
  total: number
}

function cpuSnapshot(): CpuSnapshot {
  let idle = 0
  let total = 0
  for (const cpu of cpus()) {
    idle += cpu.times.idle
    total += Object.values(cpu.times).reduce((sum, value) => sum + value, 0)
  }
  return { idle, total }
}

function currentCpuPercent(): number | undefined {
  const next = cpuSnapshot()
  const idleDelta = next.idle - previousCpuSnapshot.idle
  const totalDelta = next.total - previousCpuSnapshot.total
  previousCpuSnapshot = next
  if (totalDelta <= 0) return undefined
  return Math.min(100, Math.max(0, (1 - idleDelta / totalDelta) * 100))
}

interface NetworkCounterSnapshot {
  rxBytes: number
  txBytes: number
  sampledAtMs: number
}

interface NetworkMetricsSample {
  networkRxBytesPerSecond?: number
  networkTxBytesPerSecond?: number
  networkRxTotalBytes?: number
  networkTxTotalBytes?: number
}

async function currentNetworkMetrics(): Promise<NetworkMetricsSample> {
  const next = await readNetworkCounterSnapshot()
  if (!next) return {}

  const previous = previousNetworkSnapshot
  previousNetworkSnapshot = next
  if (!previous) {
    return {
      networkRxTotalBytes: next.rxBytes,
      networkTxTotalBytes: next.txBytes
    }
  }

  const elapsedSeconds = (next.sampledAtMs - previous.sampledAtMs) / 1000
  if (elapsedSeconds <= 0 || next.rxBytes < previous.rxBytes || next.txBytes < previous.txBytes) {
    return {
      networkRxTotalBytes: next.rxBytes,
      networkTxTotalBytes: next.txBytes
    }
  }

  return {
    networkRxBytesPerSecond: (next.rxBytes - previous.rxBytes) / elapsedSeconds,
    networkTxBytesPerSecond: (next.txBytes - previous.txBytes) / elapsedSeconds,
    networkRxTotalBytes: next.rxBytes,
    networkTxTotalBytes: next.txBytes
  }
}

async function readNetworkCounterSnapshot(): Promise<NetworkCounterSnapshot | undefined> {
  const currentPlatform = platform()
  const counters = currentPlatform === 'win32'
    ? await readWindowsNetworkCounters()
    : currentPlatform === 'darwin'
      ? await readDarwinNetworkCounters()
      : await readProcNetworkCounters()
  if (!counters) return undefined
  return { ...counters, sampledAtMs: Date.now() }
}

async function runUsageRankSnapshotsRefresh(jobName: string, stageNames: UsageRankSnapshotStageName[]): Promise<void> {
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
    const result = await refreshUsageRankSnapshotsInStages({
      yieldToEventLoop,
      stageNames,
      skipIfUnchanged: true,
      jobName
    })
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
    const issues = checkUsageStatsConsistency(20)
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

async function readProcNetworkCounters(): Promise<{ rxBytes: number; txBytes: number } | undefined> {
  const path = '/proc/net/dev'
  try {
    const lines = (await readFile(path, 'utf8')).split('\n').slice(2)
    let rxBytes = 0
    let txBytes = 0
    for (const line of lines) {
      const [ifacePart, dataPart] = line.split(':')
      if (!ifacePart || !dataPart) continue
      if (ifacePart.trim() === 'lo') continue
      const fields = dataPart.trim().split(/\s+/).map((value) => Number(value))
      if (fields.length < 16 || !Number.isFinite(fields[0]) || !Number.isFinite(fields[8])) continue
      rxBytes += fields[0]
      txBytes += fields[8]
    }
    return rxBytes > 0 || txBytes > 0 ? { rxBytes, txBytes } : undefined
  } catch {
    return undefined
  }
}

function isMissingFileError(error: unknown): boolean {
  return typeof error === 'object' && error !== null && 'code' in error && (error as { code?: unknown }).code === 'ENOENT'
}

async function readWindowsNetworkCounters(): Promise<{ rxBytes: number; txBytes: number } | undefined> {
  const command = `
$adapters = Get-NetAdapter -ErrorAction SilentlyContinue | Where-Object { $_.Status -eq 'Up' -and $_.Name -notmatch 'Loopback' }
$stats = foreach ($adapter in $adapters) { Get-NetAdapterStatistics -Name $adapter.Name -ErrorAction SilentlyContinue }
$rx = ($stats | Measure-Object -Property ReceivedBytes -Sum).Sum
$tx = ($stats | Measure-Object -Property SentBytes -Sum).Sum
if ($null -eq $rx) { $rx = 0 }
if ($null -eq $tx) { $tx = 0 }
[pscustomobject]@{ rxBytes = [double]$rx; txBytes = [double]$tx } | ConvertTo-Json -Compress
`.trim()
  try {
    const stdout = await execFileText('powershell.exe', ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-Command', command], 5000)
    const parsed = JSON.parse(stdout) as { rxBytes?: unknown; txBytes?: unknown }
    const rxBytes = numberValue(parsed.rxBytes)
    const txBytes = numberValue(parsed.txBytes)
    return rxBytes !== undefined && txBytes !== undefined ? { rxBytes, txBytes } : undefined
  } catch {
    return undefined
  }
}

async function readDarwinNetworkCounters(): Promise<{ rxBytes: number; txBytes: number } | undefined> {
  try {
    return parseDarwinNetworkCounters(await execFileText('netstat', ['-ibn'], 5000))
  } catch {
    return undefined
  }
}

function parseDarwinNetworkCounters(output: string): { rxBytes: number; txBytes: number } | undefined {
  let rxBytes = 0
  let txBytes = 0

  for (const line of output.split('\n')) {
    const fields = line.trim().split(/\s+/)
    if (fields.length < 10 || fields[2] === undefined || !fields[2].startsWith('<Link#')) continue

    const interfaceName = fields[0]
    if (!interfaceName || interfaceName === 'lo0' || interfaceName.endsWith('*')) continue

    const rxValue = numberValue(fields[6])
    const txValue = numberValue(fields[9])
    if (rxValue === undefined || txValue === undefined) continue

    rxBytes += rxValue
    txBytes += txValue
  }

  return rxBytes > 0 || txBytes > 0 ? { rxBytes, txBytes } : undefined
}

function execFileText(file: string, args: string[], timeout: number): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(file, args, { timeout, windowsHide: true }, (error, stdout) => {
      if (error) {
        reject(error)
        return
      }
      resolve(stdout.toString())
    })
  })
}

function numberValue(value: unknown): number | undefined {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? number : undefined
}

function yieldToEventLoop(): Promise<void> {
  return new Promise((resolve) => setImmediate(resolve))
}
