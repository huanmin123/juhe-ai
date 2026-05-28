import { existsSync, readFileSync, statSync } from 'node:fs'
import { execFile } from 'node:child_process'
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
  refreshUsageRankSnapshotsInStages
} from '../../storage/usage-stats.repository.js'
import { buildGatewayQuotaSnapshot } from '../../storage/gateway-quota-snapshot.repository.js'
import { collectTableStorageSnapshot } from '../../storage/table-monitor.repository.js'
import { refreshDueOpenAIOAuthAccessTokens } from '../openai-oauth/openai-oauth-access-token-refresh.service.js'
import { proxyLatencyRefreshBatchSize, proxyLatencyRefreshIntervalSeconds, refreshProxyLatencyBatch } from '../proxies/proxy-test.service.js'
import { flushUsageRecordQueue, pendingUsageRecordCount } from '../gateway/usage-record-queue.service.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'
import { flushRuntimeLogIndexQueue } from '../runtime-logs/runtime-log-index-queue.service.js'
import { ensureRuntimeLogFacetSnapshots } from '../../storage/runtime-logs.repository.js'
import { cleanupPendingDeletedAccountRecordTargets } from '../../storage/account-record-cleanup.js'
import { cleanupPendingDeletedApiKeyRecordTargets } from '../../storage/api-key-record-cleanup.js'
import { cleanupExpiredRetainedData } from './data-retention-cleanup.service.js'
import { requestServerProcessEventLoopSamples, sendGatewayQuotaSnapshotToServer } from './background-ipc.js'
import { enqueueCooldownAccountRetest, getCooldownAccountRetestQueueSnapshot } from './cooldown-account-retest.service.js'
import { WorkerScheduler } from './worker-scheduler.js'

let started = false
let usageStatsAggregationRunning = false
let missingRemoteProcessEventLoopSampleWarningCount = 0
let previousCpuSnapshot = cpuSnapshot()
let previousNetworkSnapshot: NetworkCounterSnapshot | undefined
const dailyIntervalMs = 24 * 60 * 60 * 1000
const usageRecordPreAggregationFlushMaxBatches = 2
const scheduler = new WorkerScheduler()

export function startBackgroundJobs(): void {
  if (started) return
  started = true

  scheduler.schedule({ name: 'usage-stats-aggregation', intervalMs: settingsNumber('statsAggregationIntervalSeconds', 60, 5, 3600) * 1000, task: runUsageStatsAggregation })
  scheduler.schedule({ name: 'group-account-stats-refresh', intervalMs: settingsNumber('groupAccountStatsRefreshIntervalSeconds', 60, 5, 3600) * 1000, task: runGroupAccountStatsRefresh })
  scheduler.schedule({ name: 'usage-rank-snapshots-refresh', intervalMs: 30 * 60 * 1000, task: runUsageRankSnapshotsRefresh })
  scheduler.schedule({ name: 'usage-stats-consistency-check', intervalMs: 60 * 60 * 1000, task: runUsageStatsConsistencyCheck })
  scheduler.schedule({ name: 'api-key-record-cleanup-retry', intervalMs: 60 * 1000, task: runApiKeyRecordCleanupRetry })
  scheduler.schedule({ name: 'account-record-cleanup-retry', intervalMs: 60 * 1000, task: runAccountRecordCleanupRetry })
  scheduler.schedule({ name: 'resource-authorization-expiry-sweep', intervalMs: 60 * 1000, task: runResourceAuthorizationExpirySweep })
  scheduler.schedule({ name: 'system-metrics-sample', intervalMs: settingsNumber('systemMetricsSampleIntervalSeconds', 30, 5, 3600) * 1000, task: runSystemMetricsSample })
  scheduler.schedule({ name: 'table-storage-monitor', intervalMs: 10 * 60 * 1000, task: runTableStorageMonitor })
  scheduler.schedule({ name: 'proxy-latency-refresh', intervalMs: proxyLatencyRefreshIntervalSeconds * 1000, task: runProxyLatencyRefresh })
  scheduler.schedule({ name: 'account-quality-refresh', intervalMs: settingsNumber('accountQualityRefreshIntervalSeconds', 600, 60, 3600) * 1000, task: runAccountQualityRefresh })
  scheduler.schedule({ name: 'openai-oauth-access-token-refresh', intervalMs: settingsNumber('oauthAccessTokenRefreshIntervalSeconds', 60, 10, 3600) * 1000, task: runOpenAIOAuthAccessTokenRefresh })
  scheduler.schedule({ name: 'cooldown-account-retest', intervalMs: settingsNumber('cooldownAccountRetestIntervalSeconds', 3, 1, 3600) * 1000, task: runCooldownAccountRetest })
  scheduler.schedule({ name: 'runtime-log-index-maintenance', intervalMs: 60 * 60 * 1000, task: runRuntimeLogIndexMaintenance })
  scheduler.schedule({ name: 'data-retention-cleanup', intervalMs: dailyIntervalMs, task: runDataRetentionCleanup })
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
    const batchSize = settingsNumber('statsAggregationBatchSize', 2000, 100, 10000)
    const maxBatches = settingsNumber('statsAggregationMaxBatchesPerRun', 5, 1, 100)
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
    const summary = cleanupPendingDeletedApiKeyRecordTargets(1)
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
    const summary = cleanupPendingDeletedAccountRecordTargets(1)
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
      dbFileBytes: databaseFileBytes(),
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
    const windowMinutes = settingsNumber('accountQualityWindowMinutes', 10, 1, 60)
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
  const batchSize = settingsNumber('cooldownAccountRetestBatchSize', 10, 1, 100)
  const model = settingsString('cooldownAccountRetestModel', 'gpt-5.5')
  const maxPauseMinutes = settingsNumber('defaultTemporaryUnschedulableMinutes', 5, 1, 1440)
  const maxRecoveryHours = settingsNumber('cooldownAccountRetestMaxBackoffHours', 24, 1, 24 * 30)
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

function settingsNumber(key: string, fallback: number, min: number, max: number): number {
  const value = getSettings()[key]
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? Math.min(Math.max(Math.trunc(number), min), max) : fallback
}

function settingsString(key: string, fallback: string): string {
  const value = getSettings()[key]
  return typeof value === 'string' && value.trim() ? value.trim() : fallback
}

function databaseFileBytes(): number | undefined {
  try {
    const databasePaths = new Set([runtimeConfig.databasePath, datasetDatabasePath(), statsDatabasePath()])
    let totalBytes = 0
    for (const databasePath of databasePaths) {
      totalBytes += existsSync(databasePath) ? statSync(databasePath).size : 0
    }
    return totalBytes || undefined
  } catch {
    return undefined
  }
}

async function runTableStorageMonitor(): Promise<void> {
  try {
    collectTableStorageSnapshot(nowIso(), {
      tableScanMode: 'cursor',
      maxTablesPerDatabase: settingsNumber('tableMonitorMaxTablesPerRun', 4, 0, 100)
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
      : readProcNetworkCounters()
  if (!counters) return undefined
  return { ...counters, sampledAtMs: Date.now() }
}

async function runUsageRankSnapshotsRefresh(): Promise<void> {
  try {
    await refreshUsageRankSnapshotsInStages({ yieldToEventLoop })
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_usage_rank_snapshots_refresh_failed' }), '用量排行快照刷新失败')
    throw error
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

function readProcNetworkCounters(): { rxBytes: number; txBytes: number } | undefined {
  const path = '/proc/net/dev'
  if (!existsSync(path)) return undefined
  try {
    const lines = readFileSync(path, 'utf8').split('\n').slice(2)
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
