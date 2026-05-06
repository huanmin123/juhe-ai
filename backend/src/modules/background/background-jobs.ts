import { existsSync, readFileSync, statSync } from 'node:fs'
import { execFile } from 'node:child_process'
import { cpus, freemem, platform, totalmem } from 'node:os'

import type { AccountSummary } from '../../domain/types.js'
import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import {
  expireDueResourceAuthorizations,
  getSettings,
  listAccountsDueForCooldownRetest,
  listAccounts
} from '../../storage/repositories.js'
import {
  aggregateUsageStatsBatch,
  insertSystemMetricsSample,
  latestUsageStatsLagSeconds,
  refreshGroupAccountStatsCache
} from '../../storage/usage-stats.repository.js'
import { testOpenAIAccount } from '../accounts/account-test.service.js'
import {
  isOpenAIOAuthUsageSnapshotDue,
  refreshOpenAIOAuthUsageSnapshot
} from '../openai-oauth/openai-oauth-usage-refresh.service.js'
import { refreshProxyLatencyBatch } from '../proxies/proxy-test.service.js'
import { flushUsageRecordQueue } from '../gateway/usage-record-queue.service.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'
import { flushRuntimeLogIndexQueue } from '../runtime-logs/runtime-log-index-queue.service.js'
import { cleanupExpiredRetainedData } from './data-retention-cleanup.service.js'
import { WorkerScheduler } from './worker-scheduler.js'

let started = false
let usageStatsAggregationRunning = false
let previousCpuSnapshot = cpuSnapshot()
let previousNetworkSnapshot: NetworkCounterSnapshot | undefined
let lastMetricsExpectedAt = Date.now()
const dailyIntervalMs = 24 * 60 * 60 * 1000
const scheduler = new WorkerScheduler()

export function startBackgroundJobs(): void {
  if (started) return
  started = true

  scheduler.schedule({ name: 'usage-stats-aggregation', intervalMs: settingsNumber('statsAggregationIntervalSeconds', 60, 5, 3600) * 1000, task: runUsageStatsAggregation })
  scheduler.schedule({ name: 'group-account-stats-refresh', intervalMs: settingsNumber('groupAccountStatsRefreshIntervalSeconds', 60, 5, 3600) * 1000, task: runGroupAccountStatsRefresh })
  scheduler.schedule({ name: 'resource-authorization-expiry-sweep', intervalMs: 60 * 1000, task: runResourceAuthorizationExpirySweep })
  scheduler.schedule({ name: 'system-metrics-sample', intervalMs: settingsNumber('systemMetricsSampleIntervalSeconds', 30, 5, 3600) * 1000, task: runSystemMetricsSample })
  scheduler.schedule({ name: 'proxy-latency-refresh', intervalMs: settingsNumber('proxyLatencyRefreshIntervalSeconds', 60, 10, 3600) * 1000, task: runProxyLatencyRefresh })
  scheduler.schedule({ name: 'openai-oauth-usage-refresh', intervalMs: settingsNumber('oauthUsageSnapshotRefreshIntervalSeconds', 300, 60, 86400) * 1000, task: runOpenAIOAuthUsageRefresh })
  scheduler.schedule({ name: 'cooldown-account-retest', intervalMs: settingsNumber('cooldownAccountRetestIntervalSeconds', 60, 10, 3600) * 1000, task: runCooldownAccountRetest })
  scheduler.schedule({ name: 'runtime-log-index-maintenance', intervalMs: 60 * 60 * 1000, task: runRuntimeLogIndexMaintenance })
  scheduler.schedule({ name: 'data-retention-cleanup', intervalMs: dailyIntervalMs, task: runDataRetentionCleanup })
}

async function runUsageStatsAggregation(): Promise<void> {
  if (usageStatsAggregationRunning) return
  usageStatsAggregationRunning = true
  try {
    flushUsageRecordQueue({ drain: true, retryOnFailure: false })
    const batchSize = settingsNumber('statsAggregationBatchSize', 2000, 100, 10000)
    const maxBatches = settingsNumber('statsAggregationMaxBatchesPerRun', 5, 1, 100)
    for (let index = 0; index < maxBatches; index += 1) {
      const processed = aggregateUsageStatsBatch(batchSize)
      if (processed < batchSize) break
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_usage_stats_aggregation_failed' }), 'Usage stats aggregation failed')
  } finally {
    usageStatsAggregationRunning = false
  }
}

async function runGroupAccountStatsRefresh(): Promise<void> {
  try {
    refreshGroupAccountStatsCache()
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_group_account_stats_refresh_failed' }), 'Group account stats refresh failed')
  }
}

async function runResourceAuthorizationExpirySweep(): Promise<void> {
  try {
    const changed = expireDueResourceAuthorizations()
    if (changed > 0) {
      clearGatewayRuntimeCache()
    }
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_resource_authorization_expiry_sweep_failed' }), 'Resource authorization expiry sweep failed')
  }
}

async function runSystemMetricsSample(): Promise<void> {
  const now = Date.now()
  const lagMs = Math.max(0, now - lastMetricsExpectedAt)
  lastMetricsExpectedAt = now + settingsNumber('systemMetricsSampleIntervalSeconds', 30, 5, 3600) * 1000
  const memoryTotalBytes = totalmem()
  const memoryFreeBytes = freemem()
  const memoryUsedPercent = memoryTotalBytes > 0 ? ((memoryTotalBytes - memoryFreeBytes) / memoryTotalBytes) * 100 : undefined
  const memoryUsage = process.memoryUsage()
  const networkMetrics = await currentNetworkMetrics()
  try {
    insertSystemMetricsSample({
      cpuPercent: currentCpuPercent(),
      memoryUsedPercent,
      memoryTotalBytes,
      memoryFreeBytes,
      processRssBytes: memoryUsage.rss,
      processHeapUsedBytes: memoryUsage.heapUsed,
      processHeapTotalBytes: memoryUsage.heapTotal,
      eventLoopLagMs: lagMs,
      ...networkMetrics,
      dbFileBytes: databaseFileBytes(),
      statsLagSeconds: latestUsageStatsLagSeconds()
    })
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_system_metrics_sample_failed' }), 'System metrics sample failed')
  }
}

async function runOpenAIOAuthUsageRefresh(): Promise<void> {
  const candidates = openAIOAuthUsageRefreshCandidates()
  for (const account of candidates) {
    await refreshOpenAIOAuthUsageSnapshot(account)
  }
}

async function runProxyLatencyRefresh(): Promise<void> {
  try {
    await refreshProxyLatencyBatch(settingsNumber('proxyLatencyRefreshBatchSize', 20, 1, 100))
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_proxy_latency_refresh_failed' }), 'Proxy latency refresh failed')
  }
}

async function runCooldownAccountRetest(): Promise<void> {
  const candidates = listAccountsDueForCooldownRetest(settingsNumber('cooldownAccountRetestBatchSize', 10, 1, 100))
  for (const account of candidates) {
    try {
      await testOpenAIAccount(account, { model: settingsString('cooldownAccountRetestModel', 'gpt-5.5'), prompt: 'hi' })
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'background_cooldown_account_retest_failed',
        accountId: account.id
      }), 'Cooldown account retest failed')
    }
  }
}

async function runRuntimeLogIndexMaintenance(): Promise<void> {
  try {
    flushRuntimeLogIndexQueue({ drain: true, retryOnFailure: false })
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'background_runtime_log_index_maintenance_failed' }), 'Runtime log index maintenance failed')
  }
}

async function runDataRetentionCleanup(): Promise<void> {
  cleanupExpiredRetainedData()
}

function openAIOAuthUsageRefreshCandidates(): AccountSummary[] {
  const now = Date.now()
  const ttlMs = settingsNumber('oauthUsageSnapshotTtlSeconds', 900, 60, 86400) * 1000
  const limit = settingsNumber('oauthUsageSnapshotPerAccountConcurrency', 1, 1, 20) * Math.max(1, listSystemAccountCount())
  return listAccounts()
    .filter((account) => account.providerCode === 'openai' && account.type === 'oauth')
    .filter((account) => account.status !== 'disabled' && account.schedulable)
    .filter((account) => isOpenAIOAuthUsageSnapshotDue(account, now, ttlMs))
    .slice(0, limit)
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

function listSystemAccountCount(): number {
  return Math.max(1, new Set(listAccounts().map((account) => account.systemAccountId ?? 'sys_admin')).size)
}

function databaseFileBytes(): number | undefined {
  try {
    return existsSync(runtimeConfig.databasePath) ? statSync(runtimeConfig.databasePath).size : undefined
  } catch {
    return undefined
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
  const counters = platform() === 'win32' ? await readWindowsNetworkCounters() : readProcNetworkCounters()
  if (!counters) return undefined
  return { ...counters, sampledAtMs: Date.now() }
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
