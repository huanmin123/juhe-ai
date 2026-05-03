import { existsSync, statSync } from 'node:fs'
import { cpus, freemem, totalmem } from 'node:os'

import type { AccountSummary } from '../../domain/types.js'
import { runtimeConfig } from '../../config/runtime.js'
import {
  aggregateUsageStatsBatch,
  getSettings,
  insertSystemMetricsSample,
  latestUsageStatsLagSeconds,
  listAccounts
} from '../../storage/repositories.js'
import {
  isOpenAIOAuthUsageSnapshotDue,
  refreshOpenAIOAuthUsageSnapshot
} from '../openai-oauth/openai-oauth-usage-refresh.service.js'

let started = false
let previousCpuSnapshot = cpuSnapshot()
let lastMetricsExpectedAt = Date.now()

export function startBackgroundJobs(): void {
  if (started) return
  started = true

  void runUsageStatsAggregation()
  void runSystemMetricsSample()
  void runOpenAIOAuthUsageRefresh()

  setInterval(() => { void runUsageStatsAggregation() }, settingsNumber('statsAggregationIntervalSeconds', 60, 5, 3600) * 1000)
  setInterval(() => { void runSystemMetricsSample() }, settingsNumber('systemMetricsSampleIntervalSeconds', 30, 5, 3600) * 1000)
  setInterval(() => { void runOpenAIOAuthUsageRefresh() }, settingsNumber('oauthUsageSnapshotRefreshIntervalSeconds', 300, 60, 86400) * 1000)
}

async function runUsageStatsAggregation(): Promise<void> {
  try {
    aggregateUsageStatsBatch(2000)
  } catch (error) {
    console.error('[background] usage stats aggregation failed', error)
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
      dbFileBytes: databaseFileBytes(),
      statsLagSeconds: latestUsageStatsLagSeconds()
    })
  } catch (error) {
    console.error('[background] system metrics sample failed', error)
  }
}

async function runOpenAIOAuthUsageRefresh(): Promise<void> {
  const candidates = openAIOAuthUsageRefreshCandidates()
  for (const account of candidates) {
    await refreshOpenAIOAuthUsageSnapshot(account)
  }
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
