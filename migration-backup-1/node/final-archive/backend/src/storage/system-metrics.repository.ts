import { createHash } from 'node:crypto'
import type { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { processEventLoopRoleFromUnknown, type ProcessEventLoopRole } from '../shared/process-event-loop-monitor.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../shared/rfc3339.js'
import type { AccountUsageStatsRange } from '../domain/types.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getStatsDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues } from './query-utils.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import { averageFromSum, hourKey, usageStatsTimezone, usageStatsTimezoneAsync } from './usage-stats-helpers.js'
import { mapProcessEventLoopHourly, mapSystemMetricsHourly } from './usage-stats-mappers.js'
import { aggregateSystemMetricsRows, nullableNumber } from './usage-stats-metric-aggregates.js'
import { normalizeDefaultUsageStatsRange } from './usage-stats-runtime-helpers.js'
import type { ProcessEventLoopSampleInput, SystemMetricsOverview, SystemMetricsSampleInput, SystemMetricsTrendOverview } from './usage-stats-types.js'
import {
  compareText,
  rangeWindowKey,
  rowsByStatHourDate,
  rowsForDateRange,
  trendBucketKey,
  trendBucketHours
} from './usage-stats-window-helpers.js'

const PROCESS_EVENT_LOOP_ROLES: ProcessEventLoopRole[] = [
  'server',
  'ingest-worker',
  'stats-worker',
  'ops-worker',
  'db-service'
]
const PROCESS_EVENT_LOOP_PEAK_WINDOW_MS = 24 * 60 * 60 * 1000
const PROCESS_EVENT_LOOP_LATEST_FRESHNESS_MS = 2 * 60 * 1000
const SYSTEM_METRICS_EMPTY_SOURCE_WATERMARK = '0001-01-01T00:00:00.000Z'
const SYSTEM_METRICS_SOURCE_VERSION_PATTERN = /^v2:[a-f0-9]{64}$/

function normalizedSampledAt(value: string | undefined, label: string): string {
  return value === undefined ? nowIso() : requiredRfc3339Instant(value, label)
}

export interface SystemMetricsTrendWindowSnapshotContext {
  ranges: AccountUsageStatsRange[]
  earliestDate: string
  todayKey: string
  updatedAt: string
  sourceWatermark?: string
  previousSourceWatermark?: string
  sourceVersion?: string
  previousSourceVersion?: string
}

export interface SystemMetricsTrendSourceState {
  sourceWatermark: string
  sourceVersion: string
}

export function requiredSystemMetricsTrendSourceVersion(value: unknown, label = '系统指标 sourceVersion'): string {
  if (typeof value !== 'string' || !SYSTEM_METRICS_SOURCE_VERSION_PATTERN.test(value)) {
    throw new Error(`${label}必须是 v2: 后跟 64 位小写十六进制摘要`)
  }
  return value
}

export function refreshSystemMetricsTrendWindowSnapshotsStage(database: DatabaseSync, context: SystemMetricsTrendWindowSnapshotContext): void {
  const refresh = systemMetricsTrendWindowRefresh(database, context)
  if (!refresh.ranges.length) return
  if (refresh.full) {
    database.prepare('DELETE FROM system_metrics_trend_windows').run()
    database.prepare('DELETE FROM process_event_loop_trend_windows').run()
  } else {
    deleteSystemMetricsTrendWindowRanges(database, 'system_metrics_trend_windows', refresh.ranges)
    deleteSystemMetricsTrendWindowRanges(database, 'process_event_loop_trend_windows', refresh.ranges)
  }
  const bounds = systemMetricsTrendWindowBounds(refresh.ranges, context)
  refreshSystemMetricsTrendWindows(database, refresh.ranges, bounds.earliestDate, bounds.latestDate, context.updatedAt)
  refreshProcessEventLoopTrendWindows(database, refresh.ranges, bounds.earliestDate, bounds.latestDate, context.updatedAt)
}

export async function refreshSystemMetricsTrendWindowSnapshotsStageAsync(client: DatabaseClient, context: SystemMetricsTrendWindowSnapshotContext): Promise<void> {
  const refresh = await systemMetricsTrendWindowRefreshAsync(client, context)
  if (!refresh.ranges.length) return
  if (refresh.full) {
    await client.execute(`DELETE FROM ${statsTable(client, 'system_metrics_trend_windows')}`)
    await client.execute(`DELETE FROM ${statsTable(client, 'process_event_loop_trend_windows')}`)
  } else {
    await deleteSystemMetricsTrendWindowRangesAsync(client, 'system_metrics_trend_windows', refresh.ranges)
    await deleteSystemMetricsTrendWindowRangesAsync(client, 'process_event_loop_trend_windows', refresh.ranges)
  }
  const bounds = systemMetricsTrendWindowBounds(refresh.ranges, context)
  await refreshSystemMetricsTrendWindowsAsync(client, refresh.ranges, bounds.earliestDate, bounds.latestDate, context.updatedAt)
  await refreshProcessEventLoopTrendWindowsAsync(client, refresh.ranges, bounds.earliestDate, bounds.latestDate, context.updatedAt)
}

interface SystemMetricsTrendWindowRefresh {
  ranges: AccountUsageStatsRange[]
  full: boolean
}

type SystemMetricsTrendWindowTable = 'system_metrics_trend_windows' | 'process_event_loop_trend_windows'

function systemMetricsTrendWindowRefresh(
  database: DatabaseSync,
  context: SystemMetricsTrendWindowSnapshotContext
): SystemMetricsTrendWindowRefresh {
  const incrementalState = systemMetricsTrendIncrementalSourceState(context)
  if (!incrementalState || incrementalState.sourceWatermark.milliseconds < incrementalState.previousSourceWatermark.milliseconds) {
    return { ranges: context.ranges, full: true }
  }
  if (
    incrementalState.sourceWatermark.milliseconds === incrementalState.previousSourceWatermark.milliseconds
    && incrementalState.sourceVersion === incrementalState.previousSourceVersion
  ) {
    return { ranges: [], full: false }
  }
  const includePreviousWatermark = incrementalState.sourceWatermark.milliseconds === incrementalState.previousSourceWatermark.milliseconds
  const changedDates = database.prepare(`
    SELECT stat_date, updated_at
    FROM (
      SELECT DISTINCT substr(stat_hour, 1, 10) AS stat_date, updated_at
      FROM system_metrics_hourly
      WHERE updated_at IS NOT NULL
        AND stat_hour >= ?
        AND stat_hour <= ?
      UNION
      SELECT DISTINCT substr(stat_hour, 1, 10) AS stat_date, updated_at
      FROM process_event_loop_hourly
      WHERE updated_at IS NOT NULL
        AND stat_hour >= ?
        AND stat_hour <= ?
    ) changed_dates
    ORDER BY stat_date ASC
  `).all(
    `${context.earliestDate}T00`,
    `${context.todayKey}T23`,
    `${context.earliestDate}T00`,
    `${context.todayKey}T23`
  ) as unknown as Array<{ stat_date?: string | null; updated_at?: unknown }>
  return systemMetricsTrendWindowRefreshForChangedDates(
    context,
    systemMetricsTrendChangedDateValues(
      changedDates,
      incrementalState.previousSourceWatermark.milliseconds,
      includePreviousWatermark
    )
  )
}

async function systemMetricsTrendWindowRefreshAsync(
  client: DatabaseClient,
  context: SystemMetricsTrendWindowSnapshotContext
): Promise<SystemMetricsTrendWindowRefresh> {
  const incrementalState = systemMetricsTrendIncrementalSourceState(context)
  if (!incrementalState || incrementalState.sourceWatermark.milliseconds < incrementalState.previousSourceWatermark.milliseconds) {
    return { ranges: context.ranges, full: true }
  }
  if (
    incrementalState.sourceWatermark.milliseconds === incrementalState.previousSourceWatermark.milliseconds
    && incrementalState.sourceVersion === incrementalState.previousSourceVersion
  ) {
    return { ranges: [], full: false }
  }
  const includePreviousWatermark = incrementalState.sourceWatermark.milliseconds === incrementalState.previousSourceWatermark.milliseconds
  const changedDates = await client.query<{ stat_date?: string | null; updated_at?: unknown }>(`
    SELECT stat_date, updated_at
    FROM (
      SELECT DISTINCT substr(stat_hour, 1, 10) AS stat_date, updated_at
      FROM ${statsTable(client, 'system_metrics_hourly')}
      WHERE updated_at IS NOT NULL
        AND stat_hour >= ?
        AND stat_hour <= ?
      UNION
      SELECT DISTINCT substr(stat_hour, 1, 10) AS stat_date, updated_at
      FROM ${statsTable(client, 'process_event_loop_hourly')}
      WHERE updated_at IS NOT NULL
        AND stat_hour >= ?
        AND stat_hour <= ?
    ) changed_dates
    ORDER BY stat_date ASC
  `, [
    `${context.earliestDate}T00`,
    `${context.todayKey}T23`,
    `${context.earliestDate}T00`,
    `${context.todayKey}T23`
  ])
  return systemMetricsTrendWindowRefreshForChangedDates(
    context,
    systemMetricsTrendChangedDateValues(
      changedDates,
      incrementalState.previousSourceWatermark.milliseconds,
      includePreviousWatermark
    )
  )
}

function systemMetricsTrendWindowRefreshForChangedDates(
  context: SystemMetricsTrendWindowSnapshotContext,
  values: Array<string | null | undefined>
): SystemMetricsTrendWindowRefresh {
  const changedDates = [...new Set(values.filter((value): value is string => Boolean(value)))]
  if (!changedDates.length) {
    return { ranges: [], full: false }
  }
  const ranges = context.ranges.filter((range) => changedDates.some((statDate) => range.startDate <= statDate && range.endDate >= statDate))
  return { ranges, full: false }
}

interface SystemMetricsTrendSourceWatermarkValue {
  value: string
  milliseconds: number
}

interface SystemMetricsTrendIncrementalSourceState {
  previousSourceWatermark: SystemMetricsTrendSourceWatermarkValue
  sourceWatermark: SystemMetricsTrendSourceWatermarkValue
  previousSourceVersion: string
  sourceVersion: string
}

function systemMetricsTrendIncrementalSourceState(
  context: SystemMetricsTrendWindowSnapshotContext
): SystemMetricsTrendIncrementalSourceState | undefined {
  const previousSourceWatermark = systemMetricsTrendSourceWatermarkUpdatedAt(context.previousSourceWatermark, '系统指标 previousSourceWatermark')
  const sourceWatermark = systemMetricsTrendSourceWatermarkUpdatedAt(context.sourceWatermark, '系统指标 sourceWatermark')
  if (previousSourceWatermark === undefined && sourceWatermark === undefined) return undefined
  if (previousSourceWatermark === undefined || sourceWatermark === undefined) {
    throw new Error('系统指标增量刷新必须同时提供 previousSourceWatermark 和 sourceWatermark')
  }
  if (context.previousSourceVersion === undefined || context.sourceVersion === undefined) {
    throw new Error('系统指标增量刷新必须同时提供 previousSourceVersion 和 sourceVersion')
  }
  return {
    previousSourceWatermark,
    sourceWatermark,
    previousSourceVersion: requiredSystemMetricsTrendSourceVersion(context.previousSourceVersion, '系统指标 previousSourceVersion'),
    sourceVersion: requiredSystemMetricsTrendSourceVersion(context.sourceVersion, '系统指标 sourceVersion')
  }
}

function systemMetricsTrendSourceWatermarkUpdatedAt(
  watermark: string | undefined,
  label: string
): SystemMetricsTrendSourceWatermarkValue | undefined {
  if (watermark === undefined) return undefined
  const value = requiredRfc3339Instant(watermark, label)
  const milliseconds = rfc3339InstantMilliseconds(value)
  if (milliseconds === undefined) {
    throw new Error(`${label}必须是带 Z 或数值 offset 的 RFC3339 时间`)
  }
  return { value, milliseconds }
}

function systemMetricsTrendChangedDateValues(
  rows: Array<{ stat_date?: string | null; updated_at?: unknown }>,
  previousWatermarkMilliseconds: number,
  includePreviousWatermark: boolean
): Array<string | null | undefined> {
  return rows.flatMap((row) => {
    const updatedAt = requiredRfc3339Instant(row.updated_at, '系统指标趋势窗口源 updated_at')
    const updatedAtMilliseconds = rfc3339InstantMilliseconds(updatedAt)
    if (updatedAtMilliseconds === undefined) {
      throw new Error('系统指标趋势窗口源 updated_at必须是带 Z 或数值 offset 的 RFC3339 时间')
    }
    const changed = includePreviousWatermark
      ? updatedAtMilliseconds >= previousWatermarkMilliseconds
      : updatedAtMilliseconds > previousWatermarkMilliseconds
    return changed ? [row.stat_date] : []
  })
}

export function systemMetricsTrendSourceWatermark(database: DatabaseSync): string {
  const updatedAt = systemMetricsTrendMaxUpdatedAt([
    ...(database.prepare('SELECT updated_at FROM system_metrics_hourly WHERE updated_at IS NOT NULL').all() as unknown as Array<{ updated_at?: string | null }>),
    ...(database.prepare('SELECT updated_at FROM process_event_loop_hourly WHERE updated_at IS NOT NULL').all() as unknown as Array<{ updated_at?: string | null }>)
  ])
  return updatedAt
}

export async function systemMetricsTrendSourceWatermarkAsync(client: DatabaseClient): Promise<string> {
  const [systemRows, processRows] = await Promise.all([
    client.query<{ updated_at?: string | null }>(`SELECT updated_at FROM ${statsTable(client, 'system_metrics_hourly')} WHERE updated_at IS NOT NULL`),
    client.query<{ updated_at?: string | null }>(`SELECT updated_at FROM ${statsTable(client, 'process_event_loop_hourly')} WHERE updated_at IS NOT NULL`)
  ])
  const updatedAt = systemMetricsTrendMaxUpdatedAt([...systemRows, ...processRows])
  return updatedAt
}

export function systemMetricsTrendSourceState(database: DatabaseSync): SystemMetricsTrendSourceState {
  const systemRows = database.prepare('SELECT * FROM system_metrics_hourly ORDER BY stat_hour ASC').all() as unknown as Array<Record<string, unknown>>
  const processRows = database.prepare('SELECT * FROM process_event_loop_hourly ORDER BY stat_hour ASC, process_role ASC').all() as unknown as Array<Record<string, unknown>>
  return systemMetricsTrendSourceStateFromRows(systemRows, processRows)
}

export async function systemMetricsTrendSourceStateAsync(client: DatabaseClient): Promise<SystemMetricsTrendSourceState> {
  const [systemRows, processRows] = await Promise.all([
    client.query<Record<string, unknown>>(`SELECT * FROM ${statsTable(client, 'system_metrics_hourly')} ORDER BY stat_hour ASC`),
    client.query<Record<string, unknown>>(`SELECT * FROM ${statsTable(client, 'process_event_loop_hourly')} ORDER BY stat_hour ASC, process_role ASC`)
  ])
  return systemMetricsTrendSourceStateFromRows(systemRows, processRows)
}

function systemMetricsTrendSourceStateFromRows(
  systemRows: Array<Record<string, unknown>>,
  processRows: Array<Record<string, unknown>>
): SystemMetricsTrendSourceState {
  const sourceWatermark = systemMetricsTrendMaxUpdatedAt([...systemRows, ...processRows])
  const sourceVersion = `v2:${createHash('sha256')
    .update(stableWatermarkRows('system_metrics_hourly', systemMetricsTrendRowsAtWatermark(systemRows, sourceWatermark)))
    .update(stableWatermarkRows('process_event_loop_hourly', systemMetricsTrendRowsAtWatermark(processRows, sourceWatermark)))
    .digest('hex')}`
  return {
    sourceWatermark,
    sourceVersion: requiredSystemMetricsTrendSourceVersion(sourceVersion)
  }
}

function systemMetricsTrendRowsAtWatermark(
  rows: Array<Record<string, unknown>>,
  sourceWatermark: string
): Array<Record<string, unknown>> {
  return rows.filter((row) => {
    const updatedAt = row.updated_at
    if (updatedAt === undefined || updatedAt === null) return false
    return requiredRfc3339Instant(updatedAt, '系统指标 updated_at') === sourceWatermark
  })
}

function stableWatermarkRows(tableName: string, rows: Array<Record<string, unknown>>): string {
  return JSON.stringify([
    tableName,
    rows.map((row) => Object.keys(row).sort().map((key) => [key, stableWatermarkValue(row[key])]))
  ])
}

function stableWatermarkValue(value: unknown): unknown {
  if (typeof value === 'bigint') return value.toString()
  if (value instanceof Date) return value.toISOString()
  return value
}

function systemMetricsTrendMaxUpdatedAt(rows: Array<{ updated_at?: unknown } | undefined>): string {
  let updatedAt = SYSTEM_METRICS_EMPTY_SOURCE_WATERMARK
  let updatedAtMilliseconds = rfc3339InstantMilliseconds(updatedAt)
  if (updatedAtMilliseconds === undefined) {
    throw new Error('系统指标空 sourceWatermark 必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  for (const row of rows) {
    if (row?.updated_at === undefined || row.updated_at === null) continue
    const normalizedUpdatedAt = requiredRfc3339Instant(row.updated_at, '系统指标 updated_at')
    const normalizedUpdatedAtMilliseconds = rfc3339InstantMilliseconds(normalizedUpdatedAt)
    if (normalizedUpdatedAtMilliseconds === undefined) {
      throw new Error('系统指标 updated_at 必须是带 Z 或数值 offset 的 RFC3339 时间')
    }
    if (normalizedUpdatedAtMilliseconds > updatedAtMilliseconds) {
      updatedAt = normalizedUpdatedAt
      updatedAtMilliseconds = normalizedUpdatedAtMilliseconds
    }
  }
  return updatedAt
}

function systemMetricsTrendWindowBounds(
  ranges: AccountUsageStatsRange[],
  context: SystemMetricsTrendWindowSnapshotContext
): { earliestDate: string; latestDate: string } {
  return {
    earliestDate: ranges.reduce((earliest, range) => range.startDate < earliest ? range.startDate : earliest, ranges[0]?.startDate ?? context.earliestDate),
    latestDate: ranges.reduce((latest, range) => range.endDate > latest ? range.endDate : latest, ranges[0]?.endDate ?? context.todayKey)
  }
}

function deleteSystemMetricsTrendWindowRanges(
  database: DatabaseSync,
  tableName: SystemMetricsTrendWindowTable,
  ranges: AccountUsageStatsRange[]
): void {
  const windowKeys = [...new Set(ranges.map((range) => rangeWindowKey(range)))]
  for (const chunk of chunkValues(windowKeys, 250)) {
    database.prepare(`DELETE FROM ${tableName} WHERE window_key IN (${chunk.map(() => '?').join(', ')})`).run(...chunk)
  }
}

async function deleteSystemMetricsTrendWindowRangesAsync(
  client: DatabaseClient,
  tableName: SystemMetricsTrendWindowTable,
  ranges: AccountUsageStatsRange[]
): Promise<void> {
  const windowKeys = [...new Set(ranges.map((range) => rangeWindowKey(range)))]
  for (const chunk of chunkValues(windowKeys, 250)) {
    await client.execute(`DELETE FROM ${statsTable(client, tableName)} WHERE window_key IN (${chunk.map(() => '?').join(', ')})`, chunk)
  }
}

export function insertSystemMetricsSample(input: SystemMetricsSampleInput): void {
  const database = getStatsDatabase()
  const sampledAt = normalizedSampledAt(input.sampledAt, '系统指标 sampledAt')
  const statHour = hourKey(new Date(sampledAt), usageStatsTimezone())
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database
      .prepare(`
        INSERT INTO system_metrics_samples (
          sampled_at, cpu_percent, memory_used_percent, memory_total_bytes, memory_free_bytes,
          process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes, event_loop_lag_ms,
          network_rx_bytes_per_sec, network_tx_bytes_per_sec, network_rx_total_bytes, network_tx_total_bytes,
          db_file_bytes, stats_lag_seconds, id, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `)
      .run(
        sampledAt,
        input.cpuPercent ?? null,
        input.memoryUsedPercent ?? null,
        input.memoryTotalBytes ?? null,
        input.memoryFreeBytes ?? null,
        input.processRssBytes ?? null,
        input.processHeapUsedBytes ?? null,
        input.processHeapTotalBytes ?? null,
        input.eventLoopLagMs ?? null,
        input.networkRxBytesPerSecond ?? null,
        input.networkTxBytesPerSecond ?? null,
        input.networkRxTotalBytes ?? null,
        input.networkTxTotalBytes ?? null,
        input.dbFileBytes ?? null,
        input.statsLagSeconds ?? null,
        newId('metric'),
        sampledAt
      )
    upsertSystemMetricsHourly(database, statHour, input, sampledAt)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

export async function insertSystemMetricsSampleAsync(input: SystemMetricsSampleInput): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    insertSystemMetricsSample(input)
    return
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const timezone = await usageStatsTimezoneAsync()
  await client.transaction(async (tx) => {
    await insertSystemMetricsSampleWithClientAsync(tx, input, timezone)
  })
}

export function insertProcessEventLoopSample(input: ProcessEventLoopSampleInput): void {
  const eventLoopLagMs = nullableNumber(input.eventLoopLagMs)
  const processRssBytes = nullableNumber(input.processRssBytes)
  const processHeapUsedBytes = nullableNumber(input.processHeapUsedBytes)
  const processHeapTotalBytes = nullableNumber(input.processHeapTotalBytes)
  const processExternalBytes = nullableNumber(input.processExternalBytes)
  const processArrayBuffersBytes = nullableNumber(input.processArrayBuffersBytes)
  if (
    eventLoopLagMs === null
    && processRssBytes === null
    && processHeapUsedBytes === null
    && processHeapTotalBytes === null
    && processExternalBytes === null
    && processArrayBuffersBytes === null
  ) {
    return
  }

  const database = getStatsDatabase()
  const sampledAt = normalizedSampledAt(input.sampledAt, '进程事件循环 sampledAt')
  const statHour = hourKey(new Date(sampledAt), usageStatsTimezone())
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database
      .prepare(`
        INSERT INTO process_event_loop_samples (
          sampled_at, process_role, process_pid, event_loop_lag_ms,
          process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes,
          process_external_bytes, process_array_buffers_bytes, id, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `)
      .run(
        sampledAt,
        input.processRole,
        input.processPid ?? null,
        eventLoopLagMs,
        processRssBytes,
        processHeapUsedBytes,
        processHeapTotalBytes,
        processExternalBytes,
        processArrayBuffersBytes,
        newId('process_metric'),
        sampledAt
      )
    upsertProcessEventLoopHourly(database, statHour, {
      ...input,
      eventLoopLagMs: eventLoopLagMs ?? undefined,
      processRssBytes: processRssBytes ?? undefined,
      processHeapUsedBytes: processHeapUsedBytes ?? undefined,
      processHeapTotalBytes: processHeapTotalBytes ?? undefined,
      processExternalBytes: processExternalBytes ?? undefined,
      processArrayBuffersBytes: processArrayBuffersBytes ?? undefined
    }, sampledAt)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

export async function insertProcessEventLoopSampleAsync(input: ProcessEventLoopSampleInput): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    insertProcessEventLoopSample(input)
    return
  }
  const normalizedInput = normalizedProcessEventLoopSample(input)
  if (!normalizedInput) return
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const timezone = await usageStatsTimezoneAsync()
  await client.transaction(async (tx) => {
    await insertProcessEventLoopSampleWithClientAsync(tx, normalizedInput, timezone)
  })
}

export async function insertSystemMetricsSampleBatchAsync(
  input: SystemMetricsSampleInput,
  processEventLoopSamples: readonly ProcessEventLoopSampleInput[]
): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    const database = getStatsDatabase()
    const transactionStarted = beginDatabaseTransaction(database)
    try {
      insertSystemMetricsSample(input)
      for (const processSample of processEventLoopSamples) insertProcessEventLoopSample(processSample)
      commitDatabaseTransaction(database, transactionStarted)
    } catch (error) {
      rollbackDatabaseTransaction(database, transactionStarted)
      throw error
    }
    return
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await insertSystemMetricsSampleBatchWithClientAsync(
    client,
    input,
    processEventLoopSamples,
    await usageStatsTimezoneAsync()
  )
}

export async function insertSystemMetricsSampleBatchWithClientAsync(
  client: DatabaseClient,
  input: SystemMetricsSampleInput,
  processEventLoopSamples: readonly ProcessEventLoopSampleInput[],
  timezone: string
): Promise<void> {
  const normalizedProcessSamples = processEventLoopSamples
    .map(normalizedProcessEventLoopSample)
    .filter((sample): sample is NormalizedProcessEventLoopSample => Boolean(sample))
  await client.transaction(async (tx) => {
    await insertSystemMetricsSampleWithClientAsync(tx, input, timezone)
    for (const processSample of normalizedProcessSamples) {
      await insertProcessEventLoopSampleWithClientAsync(tx, processSample, timezone)
    }
  })
}

interface NormalizedProcessEventLoopSample extends ProcessEventLoopSampleInput {
  eventLoopLagMs?: number
  processRssBytes?: number
  processHeapUsedBytes?: number
  processHeapTotalBytes?: number
  processExternalBytes?: number
  processArrayBuffersBytes?: number
}

async function insertSystemMetricsSampleWithClientAsync(
  client: DatabaseClient,
  input: SystemMetricsSampleInput,
  timezone: string
): Promise<void> {
  const sampledAt = normalizedSampledAt(input.sampledAt, '系统指标 sampledAt')
  const statHour = hourKey(new Date(sampledAt), timezone)
  await client.execute(`
    INSERT INTO ${statsTable(client, 'system_metrics_samples')} (
      sampled_at, cpu_percent, memory_used_percent, memory_total_bytes, memory_free_bytes,
      process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes, event_loop_lag_ms,
      network_rx_bytes_per_sec, network_tx_bytes_per_sec, network_rx_total_bytes, network_tx_total_bytes,
      db_file_bytes, stats_lag_seconds, id, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `, [
    sampledAt,
    input.cpuPercent ?? null,
    input.memoryUsedPercent ?? null,
    input.memoryTotalBytes ?? null,
    input.memoryFreeBytes ?? null,
    input.processRssBytes ?? null,
    input.processHeapUsedBytes ?? null,
    input.processHeapTotalBytes ?? null,
    input.eventLoopLagMs ?? null,
    input.networkRxBytesPerSecond ?? null,
    input.networkTxBytesPerSecond ?? null,
    input.networkRxTotalBytes ?? null,
    input.networkTxTotalBytes ?? null,
    input.dbFileBytes ?? null,
    input.statsLagSeconds ?? null,
    newId('metric'),
    sampledAt
  ])
  await upsertSystemMetricsHourlyAsync(client, statHour, input, sampledAt)
}

async function insertProcessEventLoopSampleWithClientAsync(
  client: DatabaseClient,
  input: NormalizedProcessEventLoopSample,
  timezone: string
): Promise<void> {
  const sampledAt = normalizedSampledAt(input.sampledAt, '进程事件循环 sampledAt')
  const statHour = hourKey(new Date(sampledAt), timezone)
  await client.execute(`
    INSERT INTO ${statsTable(client, 'process_event_loop_samples')} (
      sampled_at, process_role, process_pid, event_loop_lag_ms,
      process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes,
      process_external_bytes, process_array_buffers_bytes, id, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `, [
    sampledAt,
    input.processRole,
    input.processPid ?? null,
    input.eventLoopLagMs ?? null,
    input.processRssBytes ?? null,
    input.processHeapUsedBytes ?? null,
    input.processHeapTotalBytes ?? null,
    input.processExternalBytes ?? null,
    input.processArrayBuffersBytes ?? null,
    newId('process_metric'),
    sampledAt
  ])
  await upsertProcessEventLoopHourlyAsync(client, statHour, input, sampledAt)
}

function normalizedProcessEventLoopSample(input: ProcessEventLoopSampleInput): NormalizedProcessEventLoopSample | undefined {
  const eventLoopLagMs = nullableNumber(input.eventLoopLagMs)
  const processRssBytes = nullableNumber(input.processRssBytes)
  const processHeapUsedBytes = nullableNumber(input.processHeapUsedBytes)
  const processHeapTotalBytes = nullableNumber(input.processHeapTotalBytes)
  const processExternalBytes = nullableNumber(input.processExternalBytes)
  const processArrayBuffersBytes = nullableNumber(input.processArrayBuffersBytes)
  if (
    eventLoopLagMs === null
    && processRssBytes === null
    && processHeapUsedBytes === null
    && processHeapTotalBytes === null
    && processExternalBytes === null
    && processArrayBuffersBytes === null
  ) return undefined
  return {
    ...input,
    eventLoopLagMs: eventLoopLagMs ?? undefined,
    processRssBytes: processRssBytes ?? undefined,
    processHeapUsedBytes: processHeapUsedBytes ?? undefined,
    processHeapTotalBytes: processHeapTotalBytes ?? undefined,
    processExternalBytes: processExternalBytes ?? undefined,
    processArrayBuffersBytes: processArrayBuffersBytes ?? undefined
  }
}

export function getSystemMetricsOverview(range: AccountUsageStatsRange = normalizeDefaultUsageStatsRange()): SystemMetricsOverview {
  const database = getStatsDatabase()
  const windowKey = rangeWindowKey(range)
  const rows = database.prepare(`
    SELECT bucket_key AS stat_hour, sample_count, cpu_percent_sum, cpu_percent_max, memory_used_percent_sum,
      memory_used_percent_max, process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum,
      process_heap_used_bytes_max, event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
      network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_max, network_rx_bytes_per_sec_count,
      network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_max, network_tx_bytes_per_sec_count,
      network_rx_total_bytes_max, network_tx_total_bytes_max,
      db_file_bytes_max, stats_lag_seconds_max
    FROM system_metrics_trend_windows
    WHERE window_key = ? AND start_date = ? AND end_date = ?
    ORDER BY bucket_key ASC
  `).all(windowKey, range.startDate, range.endDate) as unknown as Array<Record<string, unknown>>
  const processLatestRows = processEventLoopLatestRows(database)
  const processEventLoopStartedAt = processEventLoopPeakStartIso()
  const processRows = loadProcessEventLoopTrendWindowRows(database, range)
  const processEventLoopLatestStatus = buildProcessEventLoopStatus(processLatestRows)
  const processEventLoopPeakStatus = buildProcessEventLoopStatus(processEventLoopPeakRows(database, processEventLoopStartedAt))
  return {
    hourlyTrend: rows.map(mapSystemMetricsHourly),
    processEventLoopLatestStatus,
    processEventLoopPeakStatus,
    processEventLoopTrend: processRows
  }
}

export async function getSystemMetricsOverviewAsync(range: AccountUsageStatsRange = normalizeDefaultUsageStatsRange()): Promise<SystemMetricsOverview> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'get_system_metrics_overview_read_only',
      range
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getSystemMetricsOverview(range)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const windowKey = rangeWindowKey(range)
  const processEventLoopStartedAt = processEventLoopPeakStartIso()
  const [rows, processLatestRows, processRows, processPeakRows] = await Promise.all([
    client.query<Record<string, unknown>>(`
      SELECT bucket_key AS stat_hour, sample_count, cpu_percent_sum, cpu_percent_max, memory_used_percent_sum,
        memory_used_percent_max, process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum,
        process_heap_used_bytes_max, event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
        network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_max, network_rx_bytes_per_sec_count,
        network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_max, network_tx_bytes_per_sec_count,
        network_rx_total_bytes_max, network_tx_total_bytes_max,
        db_file_bytes_max, stats_lag_seconds_max
      FROM ${statsTable(client, 'system_metrics_trend_windows')}
      WHERE window_key = ? AND start_date = ? AND end_date = ?
      ORDER BY bucket_key ASC
    `, [windowKey, range.startDate, range.endDate]),
    processEventLoopLatestRowsAsync(client),
    loadProcessEventLoopTrendWindowRowsAsync(client, range),
    processEventLoopPeakRowsAsync(client, processEventLoopStartedAt)
  ])
  const processEventLoopLatestStatus = buildProcessEventLoopStatus(processLatestRows)
  const processEventLoopPeakStatus = buildProcessEventLoopStatus(processPeakRows)
  return {
    hourlyTrend: rows.map(mapSystemMetricsHourly),
    processEventLoopLatestStatus,
    processEventLoopPeakStatus,
    processEventLoopTrend: processRows
  }
}

export function getSystemMetricsTrend(range: AccountUsageStatsRange = normalizeDefaultUsageStatsRange()): SystemMetricsTrendOverview {
  const database = getStatsDatabase()
  const windowKey = rangeWindowKey(range)
  const rows = database.prepare(`
    SELECT bucket_key AS stat_hour, sample_count, cpu_percent_sum, memory_used_percent_sum,
      network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_count,
      network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_count
    FROM system_metrics_trend_windows
    WHERE window_key = ? AND start_date = ? AND end_date = ?
    ORDER BY bucket_key ASC
  `).all(windowKey, range.startDate, range.endDate) as unknown as Array<Record<string, unknown>>
  const latestRows = processEventLoopTrendLatestRows(database)
  const peakStartedAt = processEventLoopPeakStartIso()
  return {
    hourlyTrend: rows.map(mapSystemMetricsTrendHourly),
    processEventLoopLatestStatus: buildProcessEventLoopTrendLatestStatus(latestRows),
    processEventLoopPeakStatus: buildProcessEventLoopTrendPeakStatus(processEventLoopTrendPeakRows(database, peakStartedAt)),
    processEventLoopTrend: loadProcessEventLoopTrendRows(database, range)
  }
}

export async function getSystemMetricsTrendAsync(range: AccountUsageStatsRange = normalizeDefaultUsageStatsRange()): Promise<SystemMetricsTrendOverview> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({ type: 'get_system_metrics_trend_read_only', range })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') return getSystemMetricsTrend(range)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const windowKey = rangeWindowKey(range)
  const peakStartedAt = processEventLoopPeakStartIso()
  const [rows, latestRows, processRows, peakRows] = await Promise.all([
    client.query<Record<string, unknown>>(`
      SELECT bucket_key AS stat_hour, sample_count, cpu_percent_sum, memory_used_percent_sum,
        network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_count,
        network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_count
      FROM ${statsTable(client, 'system_metrics_trend_windows')}
      WHERE window_key = ? AND start_date = ? AND end_date = ?
      ORDER BY bucket_key ASC
    `, [windowKey, range.startDate, range.endDate]),
    processEventLoopTrendLatestRowsAsync(client),
    loadProcessEventLoopTrendRowsAsync(client, range),
    processEventLoopTrendPeakRowsAsync(client, peakStartedAt)
  ])
  return {
    hourlyTrend: rows.map(mapSystemMetricsTrendHourly),
    processEventLoopLatestStatus: buildProcessEventLoopTrendLatestStatus(latestRows),
    processEventLoopPeakStatus: buildProcessEventLoopTrendPeakStatus(peakRows),
    processEventLoopTrend: processRows
  }
}

function refreshSystemMetricsTrendWindows(
  database: DatabaseSync,
  ranges: AccountUsageStatsRange[],
  earliestDate: string,
  todayKey: string,
  updatedAt: string
): void {
  const rows = database.prepare(`
    SELECT ${systemMetricsHourlySelectColumns()}
    FROM system_metrics_hourly
    WHERE stat_hour >= ? AND stat_hour <= ?
    ORDER BY stat_hour ASC
  `).all(`${earliestDate}T00`, `${todayKey}T23`) as unknown as Array<Record<string, unknown>>
  const rowsByDate = rowsByStatHourDate(rows.map((row) => ({ ...row, stat_hour: String(row.stat_hour ?? '') })))
  const insert = database.prepare(`
    INSERT INTO system_metrics_trend_windows (
      window_key, start_date, end_date, bucket_key, sample_count, cpu_percent_sum, cpu_percent_max, memory_used_percent_sum,
      memory_used_percent_max, process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum,
      process_heap_used_bytes_max, event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
      network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_max, network_rx_bytes_per_sec_count,
      network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_max, network_tx_bytes_per_sec_count,
      network_rx_total_bytes_max, network_tx_total_bytes_max,
      db_file_bytes_max, stats_lag_seconds_max, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const range of ranges) {
    const buckets = aggregateSystemMetricsRows(rowsForDateRange(rowsByDate, range), trendBucketHours(range))
    for (const row of buckets.sort((left, right) => compareText(String(left.stat_hour ?? ''), String(right.stat_hour ?? '')))) {
      insert.run(
        rangeWindowKey(range),
        range.startDate,
        range.endDate,
        String(row.stat_hour ?? ''),
        Number(row.sample_count ?? 0),
        Number(row.cpu_percent_sum ?? 0),
        nullableNumber(row.cpu_percent_max),
        Number(row.memory_used_percent_sum ?? 0),
        nullableNumber(row.memory_used_percent_max),
        Number(row.process_rss_bytes_sum ?? 0),
        nullableNumber(row.process_rss_bytes_max),
        Number(row.process_heap_used_bytes_sum ?? 0),
        nullableNumber(row.process_heap_used_bytes_max),
        Number(row.event_loop_lag_ms_sum ?? 0),
        Number(row.event_loop_lag_ms_count ?? 0),
        nullableNumber(row.event_loop_lag_ms_max),
        Number(row.network_rx_bytes_per_sec_sum ?? 0),
        nullableNumber(row.network_rx_bytes_per_sec_max),
        Number(row.network_rx_bytes_per_sec_count ?? 0),
        Number(row.network_tx_bytes_per_sec_sum ?? 0),
        nullableNumber(row.network_tx_bytes_per_sec_max),
        Number(row.network_tx_bytes_per_sec_count ?? 0),
        nullableNumber(row.network_rx_total_bytes_max),
        nullableNumber(row.network_tx_total_bytes_max),
        nullableNumber(row.db_file_bytes_max),
        nullableNumber(row.stats_lag_seconds_max),
        updatedAt
      )
    }
  }
}

async function refreshSystemMetricsTrendWindowsAsync(
  client: DatabaseClient,
  ranges: AccountUsageStatsRange[],
  earliestDate: string,
  todayKey: string,
  updatedAt: string
): Promise<void> {
  const rows = await client.query<Record<string, unknown> & { stat_hour: string }>(`
    SELECT ${systemMetricsHourlySelectColumns()}
    FROM ${statsTable(client, 'system_metrics_hourly')}
    WHERE stat_hour >= ? AND stat_hour <= ?
    ORDER BY stat_hour ASC
  `, [`${earliestDate}T00`, `${todayKey}T23`])
  const rowsByDate = rowsByStatHourDate(rows.map((row) => ({ ...row, stat_hour: String(row.stat_hour ?? '') })))
  const insertRows: unknown[][] = []
  for (const range of ranges) {
    const buckets = aggregateSystemMetricsRows(rowsForDateRange(rowsByDate, range), trendBucketHours(range))
    for (const row of buckets.sort((left, right) => compareText(String(left.stat_hour ?? ''), String(right.stat_hour ?? '')))) {
      insertRows.push([
        rangeWindowKey(range),
        range.startDate,
        range.endDate,
        String(row.stat_hour ?? ''),
        Number(row.sample_count ?? 0),
        Number(row.cpu_percent_sum ?? 0),
        nullableNumber(row.cpu_percent_max),
        Number(row.memory_used_percent_sum ?? 0),
        nullableNumber(row.memory_used_percent_max),
        Number(row.process_rss_bytes_sum ?? 0),
        nullableNumber(row.process_rss_bytes_max),
        Number(row.process_heap_used_bytes_sum ?? 0),
        nullableNumber(row.process_heap_used_bytes_max),
        Number(row.event_loop_lag_ms_sum ?? 0),
        Number(row.event_loop_lag_ms_count ?? 0),
        nullableNumber(row.event_loop_lag_ms_max),
        Number(row.network_rx_bytes_per_sec_sum ?? 0),
        nullableNumber(row.network_rx_bytes_per_sec_max),
        Number(row.network_rx_bytes_per_sec_count ?? 0),
        Number(row.network_tx_bytes_per_sec_sum ?? 0),
        nullableNumber(row.network_tx_bytes_per_sec_max),
        Number(row.network_tx_bytes_per_sec_count ?? 0),
        nullableNumber(row.network_rx_total_bytes_max),
        nullableNumber(row.network_tx_total_bytes_max),
        nullableNumber(row.db_file_bytes_max),
        nullableNumber(row.stats_lag_seconds_max),
        updatedAt
      ])
    }
  }
  await insertRowsAsync(client, 'system_metrics_trend_windows', [
    'window_key', 'start_date', 'end_date', 'bucket_key', 'sample_count', 'cpu_percent_sum', 'cpu_percent_max',
    'memory_used_percent_sum', 'memory_used_percent_max', 'process_rss_bytes_sum', 'process_rss_bytes_max',
    'process_heap_used_bytes_sum', 'process_heap_used_bytes_max', 'event_loop_lag_ms_sum', 'event_loop_lag_ms_count',
    'event_loop_lag_ms_max', 'network_rx_bytes_per_sec_sum', 'network_rx_bytes_per_sec_max',
    'network_rx_bytes_per_sec_count', 'network_tx_bytes_per_sec_sum', 'network_tx_bytes_per_sec_max',
    'network_tx_bytes_per_sec_count', 'network_rx_total_bytes_max', 'network_tx_total_bytes_max',
    'db_file_bytes_max', 'stats_lag_seconds_max', 'updated_at'
  ], insertRows)
}

function refreshProcessEventLoopTrendWindows(
  database: DatabaseSync,
  ranges: AccountUsageStatsRange[],
  earliestDate: string,
  todayKey: string,
  updatedAt: string
): void {
  const rows = database.prepare(`
    SELECT ${processEventLoopHourlySelectColumns()}
    FROM process_event_loop_hourly
    WHERE stat_hour >= ? AND stat_hour <= ?
    ORDER BY stat_hour ASC, process_role ASC
  `).all(`${earliestDate}T00`, `${todayKey}T23`) as unknown as Array<Record<string, unknown>>
  const rowsByDate = rowsByStatHourDate(rows.map((row) => ({ ...row, stat_hour: String(row.stat_hour ?? '') })))
  const insert = database.prepare(`
    INSERT INTO process_event_loop_trend_windows (
      window_key, start_date, end_date, bucket_key, process_role, sample_count,
      event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
      process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum, process_heap_used_bytes_max,
      process_heap_total_bytes_sum, process_heap_total_bytes_max, process_external_bytes_sum, process_external_bytes_max,
      process_array_buffers_bytes_sum, process_array_buffers_bytes_max, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const range of ranges) {
    const buckets = aggregateProcessEventLoopRows(rowsForDateRange(rowsByDate, range), trendBucketHours(range))
    for (const row of buckets.sort((left, right) => compareText(String(left.stat_hour ?? ''), String(right.stat_hour ?? '')) || compareText(String(left.process_role ?? ''), String(right.process_role ?? '')))) {
      insert.run(
        rangeWindowKey(range),
        range.startDate,
        range.endDate,
        String(row.stat_hour ?? ''),
        String(row.process_role ?? ''),
        Number(row.sample_count ?? 0),
        Number(row.event_loop_lag_ms_sum ?? 0),
        Number(row.event_loop_lag_ms_count ?? 0),
        nullableNumber(row.event_loop_lag_ms_max),
        Number(row.process_rss_bytes_sum ?? 0),
        nullableNumber(row.process_rss_bytes_max),
        Number(row.process_heap_used_bytes_sum ?? 0),
        nullableNumber(row.process_heap_used_bytes_max),
        Number(row.process_heap_total_bytes_sum ?? 0),
        nullableNumber(row.process_heap_total_bytes_max),
        Number(row.process_external_bytes_sum ?? 0),
        nullableNumber(row.process_external_bytes_max),
        Number(row.process_array_buffers_bytes_sum ?? 0),
        nullableNumber(row.process_array_buffers_bytes_max),
        updatedAt
      )
    }
  }
}

async function refreshProcessEventLoopTrendWindowsAsync(
  client: DatabaseClient,
  ranges: AccountUsageStatsRange[],
  earliestDate: string,
  todayKey: string,
  updatedAt: string
): Promise<void> {
  const rows = await client.query<Record<string, unknown> & { stat_hour: string }>(`
    SELECT ${processEventLoopHourlySelectColumns()}
    FROM ${statsTable(client, 'process_event_loop_hourly')}
    WHERE stat_hour >= ? AND stat_hour <= ?
    ORDER BY stat_hour ASC, process_role ASC
  `, [`${earliestDate}T00`, `${todayKey}T23`])
  const rowsByDate = rowsByStatHourDate(rows.map((row) => ({ ...row, stat_hour: String(row.stat_hour ?? '') })))
  const insertRows: unknown[][] = []
  for (const range of ranges) {
    const buckets = aggregateProcessEventLoopRows(rowsForDateRange(rowsByDate, range), trendBucketHours(range))
    for (const row of buckets.sort((left, right) => compareText(String(left.stat_hour ?? ''), String(right.stat_hour ?? '')) || compareText(String(left.process_role ?? ''), String(right.process_role ?? '')))) {
      insertRows.push([
        rangeWindowKey(range),
        range.startDate,
        range.endDate,
        String(row.stat_hour ?? ''),
        String(row.process_role ?? ''),
        Number(row.sample_count ?? 0),
        Number(row.event_loop_lag_ms_sum ?? 0),
        Number(row.event_loop_lag_ms_count ?? 0),
        nullableNumber(row.event_loop_lag_ms_max),
        Number(row.process_rss_bytes_sum ?? 0),
        nullableNumber(row.process_rss_bytes_max),
        Number(row.process_heap_used_bytes_sum ?? 0),
        nullableNumber(row.process_heap_used_bytes_max),
        Number(row.process_heap_total_bytes_sum ?? 0),
        nullableNumber(row.process_heap_total_bytes_max),
        Number(row.process_external_bytes_sum ?? 0),
        nullableNumber(row.process_external_bytes_max),
        Number(row.process_array_buffers_bytes_sum ?? 0),
        nullableNumber(row.process_array_buffers_bytes_max),
        updatedAt
      ])
    }
  }
  await insertRowsAsync(client, 'process_event_loop_trend_windows', [
    'window_key', 'start_date', 'end_date', 'bucket_key', 'process_role', 'sample_count',
    'event_loop_lag_ms_sum', 'event_loop_lag_ms_count', 'event_loop_lag_ms_max',
    'process_rss_bytes_sum', 'process_rss_bytes_max', 'process_heap_used_bytes_sum', 'process_heap_used_bytes_max',
    'process_heap_total_bytes_sum', 'process_heap_total_bytes_max', 'process_external_bytes_sum',
    'process_external_bytes_max', 'process_array_buffers_bytes_sum', 'process_array_buffers_bytes_max', 'updated_at'
  ], insertRows)
}

function aggregateProcessEventLoopRows(rows: Array<Record<string, unknown>>, bucketHours: number): Array<Record<string, unknown>> {
  const buckets = new Map<string, Record<string, unknown>>()
  for (const row of rows) {
    const processRole = String(row.process_role ?? '')
    if (!processRoleFromValue(processRole)) continue
    const statHour = trendBucketKey(String(row.stat_hour ?? ''), bucketHours)
    const bucketKey = `${statHour}:${processRole}`
    const bucket = buckets.get(bucketKey) ?? { stat_hour: statHour, process_role: processRole, sample_count: 0, event_loop_lag_ms_sum: 0, event_loop_lag_ms_count: 0 }
    bucket.sample_count = Number(bucket.sample_count ?? 0) + Number(row.sample_count ?? 0)
    bucket.event_loop_lag_ms_sum = Number(bucket.event_loop_lag_ms_sum ?? 0) + Number(row.event_loop_lag_ms_sum ?? 0)
    bucket.event_loop_lag_ms_count = Number(bucket.event_loop_lag_ms_count ?? 0) + Number(row.event_loop_lag_ms_count ?? row.sample_count ?? 0)
    const value = nullableNumber(row.event_loop_lag_ms_max)
    const current = nullableNumber(bucket.event_loop_lag_ms_max)
    if (value !== null) {
      bucket.event_loop_lag_ms_max = current === null ? value : Math.max(current, value)
    }
    addProcessMemoryBucketMetric(bucket, row, 'process_rss_bytes')
    addProcessMemoryBucketMetric(bucket, row, 'process_heap_used_bytes')
    addProcessMemoryBucketMetric(bucket, row, 'process_heap_total_bytes')
    addProcessMemoryBucketMetric(bucket, row, 'process_external_bytes')
    addProcessMemoryBucketMetric(bucket, row, 'process_array_buffers_bytes')
    buckets.set(bucketKey, bucket)
  }
  return [...buckets.values()]
}

function addProcessMemoryBucketMetric(bucket: Record<string, unknown>, row: Record<string, unknown>, key: string): void {
  const sumKey = `${key}_sum`
  const maxKey = `${key}_max`
  bucket[sumKey] = Number(bucket[sumKey] ?? 0) + Number(row[sumKey] ?? 0)
  const value = nullableNumber(row[maxKey])
  const current = nullableNumber(bucket[maxKey])
  if (value !== null) {
    bucket[maxKey] = current === null ? value : Math.max(current, value)
  }
}

function systemMetricsHourlySelectColumns(): string {
  return [
    'stat_hour',
    'sample_count',
    'cpu_percent_sum',
    'cpu_percent_max',
    'memory_used_percent_sum',
    'memory_used_percent_max',
    'process_rss_bytes_sum',
    'process_rss_bytes_max',
    'process_heap_used_bytes_sum',
    'process_heap_used_bytes_max',
    'event_loop_lag_ms_sum',
    'event_loop_lag_ms_count',
    'event_loop_lag_ms_max',
    'network_rx_bytes_per_sec_sum',
    'network_rx_bytes_per_sec_max',
    'network_rx_bytes_per_sec_count',
    'network_tx_bytes_per_sec_sum',
    'network_tx_bytes_per_sec_max',
    'network_tx_bytes_per_sec_count',
    'network_rx_total_bytes_max',
    'network_tx_total_bytes_max',
    'db_file_bytes_max',
    'stats_lag_seconds_max'
  ].join(', ')
}

function processEventLoopHourlySelectColumns(): string {
  return [
    'stat_hour',
    'process_role',
    'sample_count',
    'event_loop_lag_ms_sum',
    'event_loop_lag_ms_count',
    'event_loop_lag_ms_max',
    'process_rss_bytes_sum',
    'process_rss_bytes_max',
    'process_heap_used_bytes_sum',
    'process_heap_used_bytes_max',
    'process_heap_total_bytes_sum',
    'process_heap_total_bytes_max',
    'process_external_bytes_sum',
    'process_external_bytes_max',
    'process_array_buffers_bytes_sum',
    'process_array_buffers_bytes_max'
  ].join(', ')
}

function processEventLoopPeakStartIso(): string {
  return new Date(Date.now() - PROCESS_EVENT_LOOP_PEAK_WINDOW_MS).toISOString()
}

function processEventLoopLatestStartIso(): string {
  return new Date(Date.now() - PROCESS_EVENT_LOOP_LATEST_FRESHNESS_MS).toISOString()
}

function loadProcessEventLoopTrendWindowRows(database: DatabaseSync, range: AccountUsageStatsRange): SystemMetricsOverview['processEventLoopTrend'] {
  const rows = database.prepare(`
    SELECT bucket_key AS stat_hour, process_role, sample_count, event_loop_lag_ms_sum,
      event_loop_lag_ms_count, event_loop_lag_ms_max,
      process_rss_bytes_sum, process_rss_bytes_max,
      process_heap_used_bytes_sum, process_heap_used_bytes_max,
      process_heap_total_bytes_sum, process_heap_total_bytes_max
    FROM process_event_loop_trend_windows
    WHERE window_key = ? AND start_date = ? AND end_date = ?
    ORDER BY bucket_key ASC, process_role ASC
  `).all(rangeWindowKey(range), range.startDate, range.endDate) as unknown as Array<Record<string, unknown>>
  return rows
    .filter((row) => Boolean(processRoleFromValue(row.process_role)))
    .map(mapProcessEventLoopHourly)
}

function mapSystemMetricsTrendHourly(row: Record<string, unknown>): SystemMetricsTrendOverview['hourlyTrend'][number] {
  const sampleCount = Number(row.sample_count ?? 0)
  return {
    statHour: String(row.stat_hour),
    cpuPercentAvg: averageFromSum(row.cpu_percent_sum, sampleCount),
    memoryUsedPercentAvg: averageFromSum(row.memory_used_percent_sum, sampleCount),
    networkRxBytesPerSecondAvg: averageFromSum(row.network_rx_bytes_per_sec_sum, row.network_rx_bytes_per_sec_count),
    networkTxBytesPerSecondAvg: averageFromSum(row.network_tx_bytes_per_sec_sum, row.network_tx_bytes_per_sec_count)
  }
}

function mapProcessEventLoopTrendRow(row: Record<string, unknown>): SystemMetricsTrendOverview['processEventLoopTrend'][number] {
  const mapped = mapProcessEventLoopHourly(row)
  return {
    statMinute: mapped.statMinute,
    processRole: mapped.processRole,
    eventLoopLagMsAvg: mapped.eventLoopLagMsAvg,
    eventLoopLagMsMax: mapped.eventLoopLagMsMax,
    processRssBytesAvg: mapped.processRssBytesAvg,
    processRssBytesMax: mapped.processRssBytesMax
  }
}

function loadProcessEventLoopTrendRows(database: DatabaseSync, range: AccountUsageStatsRange): SystemMetricsTrendOverview['processEventLoopTrend'] {
  const rows = database.prepare(`
    SELECT bucket_key AS stat_hour, process_role, sample_count, event_loop_lag_ms_sum,
      event_loop_lag_ms_count, event_loop_lag_ms_max, process_rss_bytes_sum, process_rss_bytes_max
    FROM process_event_loop_trend_windows
    WHERE window_key = ? AND start_date = ? AND end_date = ?
    ORDER BY bucket_key ASC, process_role ASC
  `).all(rangeWindowKey(range), range.startDate, range.endDate) as unknown as Array<Record<string, unknown>>
  return rows.filter((row) => Boolean(processRoleFromValue(row.process_role))).map(mapProcessEventLoopTrendRow)
}

async function loadProcessEventLoopTrendRowsAsync(client: DatabaseClient, range: AccountUsageStatsRange): Promise<SystemMetricsTrendOverview['processEventLoopTrend']> {
  const rows = await client.query<Record<string, unknown>>(`
    SELECT bucket_key AS stat_hour, process_role, sample_count, event_loop_lag_ms_sum,
      event_loop_lag_ms_count, event_loop_lag_ms_max, process_rss_bytes_sum, process_rss_bytes_max
    FROM ${statsTable(client, 'process_event_loop_trend_windows')}
    WHERE window_key = ? AND start_date = ? AND end_date = ?
    ORDER BY bucket_key ASC, process_role ASC
  `, [rangeWindowKey(range), range.startDate, range.endDate])
  return rows.filter((row) => Boolean(processRoleFromValue(row.process_role))).map(mapProcessEventLoopTrendRow)
}

async function loadProcessEventLoopTrendWindowRowsAsync(client: DatabaseClient, range: AccountUsageStatsRange): Promise<SystemMetricsOverview['processEventLoopTrend']> {
  const rows = await client.query<Record<string, unknown>>(`
    SELECT bucket_key AS stat_hour, process_role, sample_count, event_loop_lag_ms_sum,
      event_loop_lag_ms_count, event_loop_lag_ms_max,
      process_rss_bytes_sum, process_rss_bytes_max,
      process_heap_used_bytes_sum, process_heap_used_bytes_max,
      process_heap_total_bytes_sum, process_heap_total_bytes_max
    FROM ${statsTable(client, 'process_event_loop_trend_windows')}
    WHERE window_key = ? AND start_date = ? AND end_date = ?
    ORDER BY bucket_key ASC, process_role ASC
  `, [rangeWindowKey(range), range.startDate, range.endDate])
  return rows
    .filter((row) => Boolean(processRoleFromValue(row.process_role)))
    .map(mapProcessEventLoopHourly)
}

function processEventLoopPeakRows(database: DatabaseSync, startedAt: string): Array<Record<string, unknown>> {
  return database.prepare(`
    SELECT ${processEventLoopLatestSelectColumns()}
    FROM (
      SELECT ${processEventLoopLatestSelectColumns()},
        ROW_NUMBER() OVER (
          PARTITION BY process_role
          ORDER BY event_loop_lag_ms DESC, sampled_at DESC, id DESC
        ) AS role_rank
      FROM process_event_loop_samples INDEXED BY idx_process_event_loop_samples_sampled_at
      WHERE sampled_at >= ? AND event_loop_lag_ms IS NOT NULL
    )
    WHERE role_rank = 1
    LIMIT 256
  `).all(startedAt) as unknown as Array<Record<string, unknown>>
}

function processEventLoopTrendPeakRows(database: DatabaseSync, startedAt: string): Array<Record<string, unknown>> {
  return database.prepare(`
    SELECT process_role, process_pid, sampled_at, event_loop_lag_ms
    FROM (
      SELECT process_role, process_pid, sampled_at, event_loop_lag_ms,
        ROW_NUMBER() OVER (
          PARTITION BY process_role
          ORDER BY event_loop_lag_ms DESC, sampled_at DESC, id DESC
        ) AS role_rank
      FROM process_event_loop_samples INDEXED BY idx_process_event_loop_samples_sampled_at
      WHERE sampled_at >= ? AND event_loop_lag_ms IS NOT NULL
    )
    WHERE role_rank = 1
    LIMIT 256
  `).all(startedAt) as unknown as Array<Record<string, unknown>>
}

function processEventLoopLatestRows(database: DatabaseSync): Array<Record<string, unknown>> {
  return database.prepare(`
    SELECT ${processEventLoopLatestSelectColumns()}
    FROM (
      SELECT ${processEventLoopLatestSelectColumns()},
        ROW_NUMBER() OVER (
          PARTITION BY process_role
          ORDER BY sampled_at DESC, id DESC
        ) AS role_rank
      FROM process_event_loop_samples INDEXED BY idx_process_event_loop_samples_sampled_at
      WHERE sampled_at >= ?
    )
    WHERE role_rank = 1
    LIMIT 256
  `).all(processEventLoopLatestStartIso()) as unknown as Array<Record<string, unknown>>
}

function processEventLoopTrendLatestRows(database: DatabaseSync): Array<Record<string, unknown>> {
  return database.prepare(`
    SELECT ${processEventLoopTrendLatestSelectColumns()}
    FROM (
      SELECT ${processEventLoopTrendLatestSelectColumns()},
        ROW_NUMBER() OVER (
          PARTITION BY process_role
          ORDER BY sampled_at DESC, id DESC
        ) AS role_rank
      FROM process_event_loop_samples INDEXED BY idx_process_event_loop_samples_sampled_at
      WHERE sampled_at >= ?
    )
    WHERE role_rank = 1
    LIMIT 256
  `).all(processEventLoopLatestStartIso()) as unknown as Array<Record<string, unknown>>
}

async function processEventLoopLatestRowsAsync(client: DatabaseClient): Promise<Array<Record<string, unknown>>> {
  const rows = await client.query<Record<string, unknown>>(`
    SELECT DISTINCT ON (process_role) ${processEventLoopLatestSelectColumns()}
    FROM ${statsTable(client, 'process_event_loop_samples')}
    WHERE sampled_at >= ?
    ORDER BY process_role, sampled_at DESC, id DESC
    LIMIT 256
  `, [processEventLoopLatestStartIso()])
  return rows.filter((row) => Boolean(processRoleFromValue(row.process_role)))
}

async function processEventLoopTrendLatestRowsAsync(client: DatabaseClient): Promise<Array<Record<string, unknown>>> {
  const rows = await client.query<Record<string, unknown>>(`
    SELECT DISTINCT ON (process_role) ${processEventLoopTrendLatestSelectColumns()}
    FROM ${statsTable(client, 'process_event_loop_samples')}
    WHERE sampled_at >= ?
    ORDER BY process_role, sampled_at DESC, id DESC
    LIMIT 256
  `, [processEventLoopLatestStartIso()])
  return rows.filter((row) => Boolean(processRoleFromValue(row.process_role)))
}

async function processEventLoopPeakRowsAsync(client: DatabaseClient, startedAt: string): Promise<Array<Record<string, unknown>>> {
  const rows = await client.query<Record<string, unknown>>(`
    SELECT DISTINCT ON (process_role) ${processEventLoopLatestSelectColumns()}
    FROM ${statsTable(client, 'process_event_loop_samples')}
    WHERE sampled_at >= ?
      AND event_loop_lag_ms IS NOT NULL
    ORDER BY process_role, event_loop_lag_ms DESC, sampled_at DESC, id DESC
    LIMIT 256
  `, [startedAt])
  return rows.filter((row) => Boolean(processRoleFromValue(row.process_role)))
}

async function processEventLoopTrendPeakRowsAsync(client: DatabaseClient, startedAt: string): Promise<Array<Record<string, unknown>>> {
  const rows = await client.query<Record<string, unknown>>(`
    SELECT DISTINCT ON (process_role) process_role, process_pid, sampled_at, event_loop_lag_ms
    FROM ${statsTable(client, 'process_event_loop_samples')}
    WHERE sampled_at >= ? AND event_loop_lag_ms IS NOT NULL
    ORDER BY process_role, event_loop_lag_ms DESC, sampled_at DESC, id DESC
    LIMIT 256
  `, [startedAt])
  return rows.filter((row) => Boolean(processRoleFromValue(row.process_role)))
}

function processRoleFromValue(value: unknown): ProcessEventLoopRole | undefined {
  return processEventLoopRoleFromUnknown(value)
}

function buildProcessEventLoopStatus(rows: Array<Record<string, unknown>>): SystemMetricsOverview['processEventLoopLatestStatus'] {
  const statusByRole = new Map<ProcessEventLoopRole, {
    processPid?: number
    sampledAt: string
    eventLoopLagMs?: number
    processRssBytes?: number
    processHeapUsedBytes?: number
    processHeapTotalBytes?: number
    processExternalBytes?: number
    processArrayBuffersBytes?: number
  }>()
  for (const row of rows) {
    const processRole = processRoleFromValue(row.process_role)
    if (!processRole || statusByRole.has(processRole)) continue
    statusByRole.set(processRole, {
      processPid: nullableNumber(row.process_pid) ?? undefined,
      sampledAt: requiredRfc3339Instant(row.sampled_at, '系统指标进程采样 sampled_at'),
      eventLoopLagMs: nullableNumber(row.event_loop_lag_ms) ?? undefined,
      processRssBytes: nullableNumber(row.process_rss_bytes) ?? undefined,
      processHeapUsedBytes: nullableNumber(row.process_heap_used_bytes) ?? undefined,
      processHeapTotalBytes: nullableNumber(row.process_heap_total_bytes) ?? undefined,
      processExternalBytes: nullableNumber(row.process_external_bytes) ?? undefined,
      processArrayBuffersBytes: nullableNumber(row.process_array_buffers_bytes) ?? undefined
    })
  }
  const processRoles = runtimeConfig.runtimeMode === 'standalone'
    ? PROCESS_EVENT_LOOP_ROLES
    : [...statusByRole.keys()].sort(compareText)
  return processRoles.map((processRole) => {
    const row = statusByRole.get(processRole)
    if (!row) {
      return {
        processRole,
        sampleAvailable: false,
        processPid: null,
        sampledAt: null,
        eventLoopLagMs: null,
        processRssBytes: null,
        processHeapUsedBytes: null,
        processHeapTotalBytes: null,
        processExternalBytes: null,
        processArrayBuffersBytes: null
      }
    }
    return {
      processRole,
      sampleAvailable: true,
      processPid: row.processPid ?? null,
      sampledAt: row.sampledAt,
      eventLoopLagMs: row.eventLoopLagMs ?? null,
      processRssBytes: row.processRssBytes ?? null,
      processHeapUsedBytes: row.processHeapUsedBytes ?? null,
      processHeapTotalBytes: row.processHeapTotalBytes ?? null,
      processExternalBytes: row.processExternalBytes ?? null,
      processArrayBuffersBytes: row.processArrayBuffersBytes ?? null
    }
  })
}

function trendStatusRoles<T extends { processRole: ProcessEventLoopRole }>(rows: T[]): ProcessEventLoopRole[] {
  return runtimeConfig.runtimeMode === 'standalone'
    ? PROCESS_EVENT_LOOP_ROLES
    : rows.map((row) => row.processRole).sort(compareText)
}

function buildProcessEventLoopTrendLatestStatus(rows: Array<Record<string, unknown>>): SystemMetricsTrendOverview['processEventLoopLatestStatus'] {
  const mapped = rows.flatMap((row) => {
    const processRole = processRoleFromValue(row.process_role)
    return processRole ? [{
      processRole,
      sampleAvailable: true,
      processPid: nullableNumber(row.process_pid) ?? null,
      sampledAt: requiredRfc3339Instant(row.sampled_at, '系统指标趋势进程采样 sampled_at'),
      eventLoopLagMs: nullableNumber(row.event_loop_lag_ms),
      processRssBytes: nullableNumber(row.process_rss_bytes),
      processHeapUsedBytes: nullableNumber(row.process_heap_used_bytes),
      processHeapTotalBytes: nullableNumber(row.process_heap_total_bytes)
    }] : []
  })
  const byRole = new Map(mapped.map((row) => [row.processRole, row]))
  return trendStatusRoles(mapped).map((processRole) => byRole.get(processRole) ?? {
    processRole,
    sampleAvailable: false,
    processPid: null,
    sampledAt: null,
    eventLoopLagMs: null,
    processRssBytes: null,
    processHeapUsedBytes: null,
    processHeapTotalBytes: null
  })
}

function buildProcessEventLoopTrendPeakStatus(rows: Array<Record<string, unknown>>): SystemMetricsTrendOverview['processEventLoopPeakStatus'] {
  const mapped = rows.flatMap((row) => {
    const processRole = processRoleFromValue(row.process_role)
    return processRole ? [{
      processRole,
      sampleAvailable: true,
      processPid: nullableNumber(row.process_pid) ?? null,
      sampledAt: requiredRfc3339Instant(row.sampled_at, '系统指标峰值进程采样 sampled_at'),
      eventLoopLagMs: nullableNumber(row.event_loop_lag_ms)
    }] : []
  })
  const byRole = new Map(mapped.map((row) => [row.processRole, row]))
  return trendStatusRoles(mapped).map((processRole) => byRole.get(processRole) ?? {
    processRole,
    sampleAvailable: false,
    processPid: null,
    sampledAt: null,
    eventLoopLagMs: null
  })
}

function processEventLoopLatestSelectColumns(): string {
  return [
    'process_role',
    'process_pid',
    'sampled_at',
    'event_loop_lag_ms',
    'process_rss_bytes',
    'process_heap_used_bytes',
    'process_heap_total_bytes',
    'process_external_bytes',
    'process_array_buffers_bytes'
  ].join(', ')
}

function processEventLoopTrendLatestSelectColumns(): string {
  return [
    'process_role',
    'process_pid',
    'sampled_at',
    'event_loop_lag_ms',
    'process_rss_bytes',
    'process_heap_used_bytes',
    'process_heap_total_bytes'
  ].join(', ')
}

function upsertSystemMetricsHourly(database: DatabaseSync, statHour: string, input: SystemMetricsSampleInput, updatedAt: string): void {
  database.prepare(`
    INSERT INTO system_metrics_hourly (
      stat_hour, sample_count, cpu_percent_sum, cpu_percent_max, memory_used_percent_sum,
      memory_used_percent_max, process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum,
      process_heap_used_bytes_max, event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
      network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_max, network_rx_bytes_per_sec_count,
      network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_max, network_tx_bytes_per_sec_count,
      network_rx_total_bytes_max, network_tx_total_bytes_max,
      db_file_bytes_max, stats_lag_seconds_max, updated_at
    )
    VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(stat_hour) DO UPDATE SET
      sample_count = sample_count + 1,
      cpu_percent_sum = cpu_percent_sum + excluded.cpu_percent_sum,
      cpu_percent_max = CASE WHEN excluded.cpu_percent_max IS NULL THEN system_metrics_hourly.cpu_percent_max WHEN system_metrics_hourly.cpu_percent_max IS NULL OR excluded.cpu_percent_max > system_metrics_hourly.cpu_percent_max THEN excluded.cpu_percent_max ELSE system_metrics_hourly.cpu_percent_max END,
      memory_used_percent_sum = memory_used_percent_sum + excluded.memory_used_percent_sum,
      memory_used_percent_max = CASE WHEN excluded.memory_used_percent_max IS NULL THEN system_metrics_hourly.memory_used_percent_max WHEN system_metrics_hourly.memory_used_percent_max IS NULL OR excluded.memory_used_percent_max > system_metrics_hourly.memory_used_percent_max THEN excluded.memory_used_percent_max ELSE system_metrics_hourly.memory_used_percent_max END,
      process_rss_bytes_sum = process_rss_bytes_sum + excluded.process_rss_bytes_sum,
      process_rss_bytes_max = CASE WHEN excluded.process_rss_bytes_max IS NULL THEN system_metrics_hourly.process_rss_bytes_max WHEN system_metrics_hourly.process_rss_bytes_max IS NULL OR excluded.process_rss_bytes_max > system_metrics_hourly.process_rss_bytes_max THEN excluded.process_rss_bytes_max ELSE system_metrics_hourly.process_rss_bytes_max END,
      process_heap_used_bytes_sum = process_heap_used_bytes_sum + excluded.process_heap_used_bytes_sum,
      process_heap_used_bytes_max = CASE WHEN excluded.process_heap_used_bytes_max IS NULL THEN system_metrics_hourly.process_heap_used_bytes_max WHEN system_metrics_hourly.process_heap_used_bytes_max IS NULL OR excluded.process_heap_used_bytes_max > system_metrics_hourly.process_heap_used_bytes_max THEN excluded.process_heap_used_bytes_max ELSE system_metrics_hourly.process_heap_used_bytes_max END,
      event_loop_lag_ms_sum = event_loop_lag_ms_sum + excluded.event_loop_lag_ms_sum,
      event_loop_lag_ms_count = event_loop_lag_ms_count + excluded.event_loop_lag_ms_count,
      event_loop_lag_ms_max = CASE WHEN excluded.event_loop_lag_ms_max IS NULL THEN system_metrics_hourly.event_loop_lag_ms_max WHEN system_metrics_hourly.event_loop_lag_ms_max IS NULL OR excluded.event_loop_lag_ms_max > system_metrics_hourly.event_loop_lag_ms_max THEN excluded.event_loop_lag_ms_max ELSE system_metrics_hourly.event_loop_lag_ms_max END,
      network_rx_bytes_per_sec_sum = network_rx_bytes_per_sec_sum + excluded.network_rx_bytes_per_sec_sum,
      network_rx_bytes_per_sec_max = CASE WHEN excluded.network_rx_bytes_per_sec_max IS NULL THEN system_metrics_hourly.network_rx_bytes_per_sec_max WHEN system_metrics_hourly.network_rx_bytes_per_sec_max IS NULL OR excluded.network_rx_bytes_per_sec_max > system_metrics_hourly.network_rx_bytes_per_sec_max THEN excluded.network_rx_bytes_per_sec_max ELSE system_metrics_hourly.network_rx_bytes_per_sec_max END,
      network_rx_bytes_per_sec_count = network_rx_bytes_per_sec_count + excluded.network_rx_bytes_per_sec_count,
      network_tx_bytes_per_sec_sum = network_tx_bytes_per_sec_sum + excluded.network_tx_bytes_per_sec_sum,
      network_tx_bytes_per_sec_max = CASE WHEN excluded.network_tx_bytes_per_sec_max IS NULL THEN system_metrics_hourly.network_tx_bytes_per_sec_max WHEN system_metrics_hourly.network_tx_bytes_per_sec_max IS NULL OR excluded.network_tx_bytes_per_sec_max > system_metrics_hourly.network_tx_bytes_per_sec_max THEN excluded.network_tx_bytes_per_sec_max ELSE system_metrics_hourly.network_tx_bytes_per_sec_max END,
      network_tx_bytes_per_sec_count = network_tx_bytes_per_sec_count + excluded.network_tx_bytes_per_sec_count,
      network_rx_total_bytes_max = CASE WHEN excluded.network_rx_total_bytes_max IS NULL THEN system_metrics_hourly.network_rx_total_bytes_max WHEN system_metrics_hourly.network_rx_total_bytes_max IS NULL OR excluded.network_rx_total_bytes_max > system_metrics_hourly.network_rx_total_bytes_max THEN excluded.network_rx_total_bytes_max ELSE system_metrics_hourly.network_rx_total_bytes_max END,
      network_tx_total_bytes_max = CASE WHEN excluded.network_tx_total_bytes_max IS NULL THEN system_metrics_hourly.network_tx_total_bytes_max WHEN system_metrics_hourly.network_tx_total_bytes_max IS NULL OR excluded.network_tx_total_bytes_max > system_metrics_hourly.network_tx_total_bytes_max THEN excluded.network_tx_total_bytes_max ELSE system_metrics_hourly.network_tx_total_bytes_max END,
      db_file_bytes_max = CASE WHEN excluded.db_file_bytes_max IS NULL THEN system_metrics_hourly.db_file_bytes_max WHEN system_metrics_hourly.db_file_bytes_max IS NULL OR excluded.db_file_bytes_max > system_metrics_hourly.db_file_bytes_max THEN excluded.db_file_bytes_max ELSE system_metrics_hourly.db_file_bytes_max END,
      stats_lag_seconds_max = CASE WHEN excluded.stats_lag_seconds_max IS NULL THEN system_metrics_hourly.stats_lag_seconds_max WHEN system_metrics_hourly.stats_lag_seconds_max IS NULL OR excluded.stats_lag_seconds_max > system_metrics_hourly.stats_lag_seconds_max THEN excluded.stats_lag_seconds_max ELSE system_metrics_hourly.stats_lag_seconds_max END,
      updated_at = excluded.updated_at
  `).run(
    statHour,
    input.cpuPercent ?? 0,
    input.cpuPercent ?? null,
    input.memoryUsedPercent ?? 0,
    input.memoryUsedPercent ?? null,
    input.processRssBytes ?? 0,
    input.processRssBytes ?? null,
    input.processHeapUsedBytes ?? 0,
    input.processHeapUsedBytes ?? null,
    input.eventLoopLagMs ?? 0,
    input.eventLoopLagMs === undefined ? 0 : 1,
    input.eventLoopLagMs ?? null,
    input.networkRxBytesPerSecond ?? 0,
    input.networkRxBytesPerSecond ?? null,
    input.networkRxBytesPerSecond === undefined ? 0 : 1,
    input.networkTxBytesPerSecond ?? 0,
    input.networkTxBytesPerSecond ?? null,
    input.networkTxBytesPerSecond === undefined ? 0 : 1,
    input.networkRxTotalBytes ?? null,
    input.networkTxTotalBytes ?? null,
    input.dbFileBytes ?? null,
    input.statsLagSeconds ?? null,
    updatedAt
  )
}

async function upsertSystemMetricsHourlyAsync(client: DatabaseClient, statHour: string, input: SystemMetricsSampleInput, updatedAt: string): Promise<void> {
  await client.execute(`
    INSERT INTO ${statsTable(client, 'system_metrics_hourly')} (
      stat_hour, sample_count, cpu_percent_sum, cpu_percent_max, memory_used_percent_sum,
      memory_used_percent_max, process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum,
      process_heap_used_bytes_max, event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
      network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_max, network_rx_bytes_per_sec_count,
      network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_max, network_tx_bytes_per_sec_count,
      network_rx_total_bytes_max, network_tx_total_bytes_max,
      db_file_bytes_max, stats_lag_seconds_max, updated_at
    )
    VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(stat_hour) DO UPDATE SET
      sample_count = system_metrics_hourly.sample_count + 1,
      cpu_percent_sum = system_metrics_hourly.cpu_percent_sum + excluded.cpu_percent_sum,
      cpu_percent_max = CASE WHEN excluded.cpu_percent_max IS NULL THEN system_metrics_hourly.cpu_percent_max WHEN system_metrics_hourly.cpu_percent_max IS NULL OR excluded.cpu_percent_max > system_metrics_hourly.cpu_percent_max THEN excluded.cpu_percent_max ELSE system_metrics_hourly.cpu_percent_max END,
      memory_used_percent_sum = system_metrics_hourly.memory_used_percent_sum + excluded.memory_used_percent_sum,
      memory_used_percent_max = CASE WHEN excluded.memory_used_percent_max IS NULL THEN system_metrics_hourly.memory_used_percent_max WHEN system_metrics_hourly.memory_used_percent_max IS NULL OR excluded.memory_used_percent_max > system_metrics_hourly.memory_used_percent_max THEN excluded.memory_used_percent_max ELSE system_metrics_hourly.memory_used_percent_max END,
      process_rss_bytes_sum = system_metrics_hourly.process_rss_bytes_sum + excluded.process_rss_bytes_sum,
      process_rss_bytes_max = CASE WHEN excluded.process_rss_bytes_max IS NULL THEN system_metrics_hourly.process_rss_bytes_max WHEN system_metrics_hourly.process_rss_bytes_max IS NULL OR excluded.process_rss_bytes_max > system_metrics_hourly.process_rss_bytes_max THEN excluded.process_rss_bytes_max ELSE system_metrics_hourly.process_rss_bytes_max END,
      process_heap_used_bytes_sum = system_metrics_hourly.process_heap_used_bytes_sum + excluded.process_heap_used_bytes_sum,
      process_heap_used_bytes_max = CASE WHEN excluded.process_heap_used_bytes_max IS NULL THEN system_metrics_hourly.process_heap_used_bytes_max WHEN system_metrics_hourly.process_heap_used_bytes_max IS NULL OR excluded.process_heap_used_bytes_max > system_metrics_hourly.process_heap_used_bytes_max THEN excluded.process_heap_used_bytes_max ELSE system_metrics_hourly.process_heap_used_bytes_max END,
      event_loop_lag_ms_sum = system_metrics_hourly.event_loop_lag_ms_sum + excluded.event_loop_lag_ms_sum,
      event_loop_lag_ms_count = system_metrics_hourly.event_loop_lag_ms_count + excluded.event_loop_lag_ms_count,
      event_loop_lag_ms_max = CASE WHEN excluded.event_loop_lag_ms_max IS NULL THEN system_metrics_hourly.event_loop_lag_ms_max WHEN system_metrics_hourly.event_loop_lag_ms_max IS NULL OR excluded.event_loop_lag_ms_max > system_metrics_hourly.event_loop_lag_ms_max THEN excluded.event_loop_lag_ms_max ELSE system_metrics_hourly.event_loop_lag_ms_max END,
      network_rx_bytes_per_sec_sum = system_metrics_hourly.network_rx_bytes_per_sec_sum + excluded.network_rx_bytes_per_sec_sum,
      network_rx_bytes_per_sec_max = CASE WHEN excluded.network_rx_bytes_per_sec_max IS NULL THEN system_metrics_hourly.network_rx_bytes_per_sec_max WHEN system_metrics_hourly.network_rx_bytes_per_sec_max IS NULL OR excluded.network_rx_bytes_per_sec_max > system_metrics_hourly.network_rx_bytes_per_sec_max THEN excluded.network_rx_bytes_per_sec_max ELSE system_metrics_hourly.network_rx_bytes_per_sec_max END,
      network_rx_bytes_per_sec_count = system_metrics_hourly.network_rx_bytes_per_sec_count + excluded.network_rx_bytes_per_sec_count,
      network_tx_bytes_per_sec_sum = system_metrics_hourly.network_tx_bytes_per_sec_sum + excluded.network_tx_bytes_per_sec_sum,
      network_tx_bytes_per_sec_max = CASE WHEN excluded.network_tx_bytes_per_sec_max IS NULL THEN system_metrics_hourly.network_tx_bytes_per_sec_max WHEN system_metrics_hourly.network_tx_bytes_per_sec_max IS NULL OR excluded.network_tx_bytes_per_sec_max > system_metrics_hourly.network_tx_bytes_per_sec_max THEN excluded.network_tx_bytes_per_sec_max ELSE system_metrics_hourly.network_tx_bytes_per_sec_max END,
      network_tx_bytes_per_sec_count = system_metrics_hourly.network_tx_bytes_per_sec_count + excluded.network_tx_bytes_per_sec_count,
      network_rx_total_bytes_max = CASE WHEN excluded.network_rx_total_bytes_max IS NULL THEN system_metrics_hourly.network_rx_total_bytes_max WHEN system_metrics_hourly.network_rx_total_bytes_max IS NULL OR excluded.network_rx_total_bytes_max > system_metrics_hourly.network_rx_total_bytes_max THEN excluded.network_rx_total_bytes_max ELSE system_metrics_hourly.network_rx_total_bytes_max END,
      network_tx_total_bytes_max = CASE WHEN excluded.network_tx_total_bytes_max IS NULL THEN system_metrics_hourly.network_tx_total_bytes_max WHEN system_metrics_hourly.network_tx_total_bytes_max IS NULL OR excluded.network_tx_total_bytes_max > system_metrics_hourly.network_tx_total_bytes_max THEN excluded.network_tx_total_bytes_max ELSE system_metrics_hourly.network_tx_total_bytes_max END,
      db_file_bytes_max = CASE WHEN excluded.db_file_bytes_max IS NULL THEN system_metrics_hourly.db_file_bytes_max WHEN system_metrics_hourly.db_file_bytes_max IS NULL OR excluded.db_file_bytes_max > system_metrics_hourly.db_file_bytes_max THEN excluded.db_file_bytes_max ELSE system_metrics_hourly.db_file_bytes_max END,
      stats_lag_seconds_max = CASE WHEN excluded.stats_lag_seconds_max IS NULL THEN system_metrics_hourly.stats_lag_seconds_max WHEN system_metrics_hourly.stats_lag_seconds_max IS NULL OR excluded.stats_lag_seconds_max > system_metrics_hourly.stats_lag_seconds_max THEN excluded.stats_lag_seconds_max ELSE system_metrics_hourly.stats_lag_seconds_max END,
      updated_at = excluded.updated_at
  `, [
    statHour,
    input.cpuPercent ?? 0,
    input.cpuPercent ?? null,
    input.memoryUsedPercent ?? 0,
    input.memoryUsedPercent ?? null,
    input.processRssBytes ?? 0,
    input.processRssBytes ?? null,
    input.processHeapUsedBytes ?? 0,
    input.processHeapUsedBytes ?? null,
    input.eventLoopLagMs ?? 0,
    input.eventLoopLagMs === undefined ? 0 : 1,
    input.eventLoopLagMs ?? null,
    input.networkRxBytesPerSecond ?? 0,
    input.networkRxBytesPerSecond ?? null,
    input.networkRxBytesPerSecond === undefined ? 0 : 1,
    input.networkTxBytesPerSecond ?? 0,
    input.networkTxBytesPerSecond ?? null,
    input.networkTxBytesPerSecond === undefined ? 0 : 1,
    input.networkRxTotalBytes ?? null,
    input.networkTxTotalBytes ?? null,
    input.dbFileBytes ?? null,
    input.statsLagSeconds ?? null,
    updatedAt
  ])
}

function upsertProcessEventLoopHourly(
  database: DatabaseSync,
  statHour: string,
  input: ProcessEventLoopSampleInput,
  updatedAt: string
): void {
  const eventLoopLagMs = nullableNumber(input.eventLoopLagMs)
  const processRssBytes = nullableNumber(input.processRssBytes)
  const processHeapUsedBytes = nullableNumber(input.processHeapUsedBytes)
  const processHeapTotalBytes = nullableNumber(input.processHeapTotalBytes)
  const processExternalBytes = nullableNumber(input.processExternalBytes)
  const processArrayBuffersBytes = nullableNumber(input.processArrayBuffersBytes)
  database.prepare(`
    INSERT INTO process_event_loop_hourly (
      stat_hour, process_role, sample_count, event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
      process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum, process_heap_used_bytes_max,
      process_heap_total_bytes_sum, process_heap_total_bytes_max, process_external_bytes_sum, process_external_bytes_max,
      process_array_buffers_bytes_sum, process_array_buffers_bytes_max, updated_at
    )
    VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(stat_hour, process_role) DO UPDATE SET
      sample_count = sample_count + 1,
      event_loop_lag_ms_sum = event_loop_lag_ms_sum + excluded.event_loop_lag_ms_sum,
      event_loop_lag_ms_count = event_loop_lag_ms_count + excluded.event_loop_lag_ms_count,
      event_loop_lag_ms_max = CASE WHEN excluded.event_loop_lag_ms_max IS NULL THEN process_event_loop_hourly.event_loop_lag_ms_max WHEN process_event_loop_hourly.event_loop_lag_ms_max IS NULL OR excluded.event_loop_lag_ms_max > process_event_loop_hourly.event_loop_lag_ms_max THEN excluded.event_loop_lag_ms_max ELSE process_event_loop_hourly.event_loop_lag_ms_max END,
      process_rss_bytes_sum = process_rss_bytes_sum + excluded.process_rss_bytes_sum,
      process_rss_bytes_max = CASE WHEN excluded.process_rss_bytes_max IS NULL THEN process_event_loop_hourly.process_rss_bytes_max WHEN process_event_loop_hourly.process_rss_bytes_max IS NULL OR excluded.process_rss_bytes_max > process_event_loop_hourly.process_rss_bytes_max THEN excluded.process_rss_bytes_max ELSE process_event_loop_hourly.process_rss_bytes_max END,
      process_heap_used_bytes_sum = process_heap_used_bytes_sum + excluded.process_heap_used_bytes_sum,
      process_heap_used_bytes_max = CASE WHEN excluded.process_heap_used_bytes_max IS NULL THEN process_event_loop_hourly.process_heap_used_bytes_max WHEN process_event_loop_hourly.process_heap_used_bytes_max IS NULL OR excluded.process_heap_used_bytes_max > process_event_loop_hourly.process_heap_used_bytes_max THEN excluded.process_heap_used_bytes_max ELSE process_event_loop_hourly.process_heap_used_bytes_max END,
      process_heap_total_bytes_sum = process_heap_total_bytes_sum + excluded.process_heap_total_bytes_sum,
      process_heap_total_bytes_max = CASE WHEN excluded.process_heap_total_bytes_max IS NULL THEN process_event_loop_hourly.process_heap_total_bytes_max WHEN process_event_loop_hourly.process_heap_total_bytes_max IS NULL OR excluded.process_heap_total_bytes_max > process_event_loop_hourly.process_heap_total_bytes_max THEN excluded.process_heap_total_bytes_max ELSE process_event_loop_hourly.process_heap_total_bytes_max END,
      process_external_bytes_sum = process_external_bytes_sum + excluded.process_external_bytes_sum,
      process_external_bytes_max = CASE WHEN excluded.process_external_bytes_max IS NULL THEN process_event_loop_hourly.process_external_bytes_max WHEN process_event_loop_hourly.process_external_bytes_max IS NULL OR excluded.process_external_bytes_max > process_event_loop_hourly.process_external_bytes_max THEN excluded.process_external_bytes_max ELSE process_event_loop_hourly.process_external_bytes_max END,
      process_array_buffers_bytes_sum = process_array_buffers_bytes_sum + excluded.process_array_buffers_bytes_sum,
      process_array_buffers_bytes_max = CASE WHEN excluded.process_array_buffers_bytes_max IS NULL THEN process_event_loop_hourly.process_array_buffers_bytes_max WHEN process_event_loop_hourly.process_array_buffers_bytes_max IS NULL OR excluded.process_array_buffers_bytes_max > process_event_loop_hourly.process_array_buffers_bytes_max THEN excluded.process_array_buffers_bytes_max ELSE process_event_loop_hourly.process_array_buffers_bytes_max END,
      updated_at = excluded.updated_at
  `).run(
    statHour,
    input.processRole,
    eventLoopLagMs ?? 0,
    eventLoopLagMs === null ? 0 : 1,
    eventLoopLagMs,
    processRssBytes ?? 0,
    processRssBytes,
    processHeapUsedBytes ?? 0,
    processHeapUsedBytes,
    processHeapTotalBytes ?? 0,
    processHeapTotalBytes,
    processExternalBytes ?? 0,
    processExternalBytes,
    processArrayBuffersBytes ?? 0,
    processArrayBuffersBytes,
    updatedAt
  )
}

async function upsertProcessEventLoopHourlyAsync(
  client: DatabaseClient,
  statHour: string,
  input: ProcessEventLoopSampleInput,
  updatedAt: string
): Promise<void> {
  const eventLoopLagMs = nullableNumber(input.eventLoopLagMs)
  const processRssBytes = nullableNumber(input.processRssBytes)
  const processHeapUsedBytes = nullableNumber(input.processHeapUsedBytes)
  const processHeapTotalBytes = nullableNumber(input.processHeapTotalBytes)
  const processExternalBytes = nullableNumber(input.processExternalBytes)
  const processArrayBuffersBytes = nullableNumber(input.processArrayBuffersBytes)
  await client.execute(`
    INSERT INTO ${statsTable(client, 'process_event_loop_hourly')} (
      stat_hour, process_role, sample_count, event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
      process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum, process_heap_used_bytes_max,
      process_heap_total_bytes_sum, process_heap_total_bytes_max, process_external_bytes_sum, process_external_bytes_max,
      process_array_buffers_bytes_sum, process_array_buffers_bytes_max, updated_at
    )
    VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(stat_hour, process_role) DO UPDATE SET
      sample_count = process_event_loop_hourly.sample_count + 1,
      event_loop_lag_ms_sum = process_event_loop_hourly.event_loop_lag_ms_sum + excluded.event_loop_lag_ms_sum,
      event_loop_lag_ms_count = process_event_loop_hourly.event_loop_lag_ms_count + excluded.event_loop_lag_ms_count,
      event_loop_lag_ms_max = CASE WHEN excluded.event_loop_lag_ms_max IS NULL THEN process_event_loop_hourly.event_loop_lag_ms_max WHEN process_event_loop_hourly.event_loop_lag_ms_max IS NULL OR excluded.event_loop_lag_ms_max > process_event_loop_hourly.event_loop_lag_ms_max THEN excluded.event_loop_lag_ms_max ELSE process_event_loop_hourly.event_loop_lag_ms_max END,
      process_rss_bytes_sum = process_event_loop_hourly.process_rss_bytes_sum + excluded.process_rss_bytes_sum,
      process_rss_bytes_max = CASE WHEN excluded.process_rss_bytes_max IS NULL THEN process_event_loop_hourly.process_rss_bytes_max WHEN process_event_loop_hourly.process_rss_bytes_max IS NULL OR excluded.process_rss_bytes_max > process_event_loop_hourly.process_rss_bytes_max THEN excluded.process_rss_bytes_max ELSE process_event_loop_hourly.process_rss_bytes_max END,
      process_heap_used_bytes_sum = process_event_loop_hourly.process_heap_used_bytes_sum + excluded.process_heap_used_bytes_sum,
      process_heap_used_bytes_max = CASE WHEN excluded.process_heap_used_bytes_max IS NULL THEN process_event_loop_hourly.process_heap_used_bytes_max WHEN process_event_loop_hourly.process_heap_used_bytes_max IS NULL OR excluded.process_heap_used_bytes_max > process_event_loop_hourly.process_heap_used_bytes_max THEN excluded.process_heap_used_bytes_max ELSE process_event_loop_hourly.process_heap_used_bytes_max END,
      process_heap_total_bytes_sum = process_event_loop_hourly.process_heap_total_bytes_sum + excluded.process_heap_total_bytes_sum,
      process_heap_total_bytes_max = CASE WHEN excluded.process_heap_total_bytes_max IS NULL THEN process_event_loop_hourly.process_heap_total_bytes_max WHEN process_event_loop_hourly.process_heap_total_bytes_max IS NULL OR excluded.process_heap_total_bytes_max > process_event_loop_hourly.process_heap_total_bytes_max THEN excluded.process_heap_total_bytes_max ELSE process_event_loop_hourly.process_heap_total_bytes_max END,
      process_external_bytes_sum = process_event_loop_hourly.process_external_bytes_sum + excluded.process_external_bytes_sum,
      process_external_bytes_max = CASE WHEN excluded.process_external_bytes_max IS NULL THEN process_event_loop_hourly.process_external_bytes_max WHEN process_event_loop_hourly.process_external_bytes_max IS NULL OR excluded.process_external_bytes_max > process_event_loop_hourly.process_external_bytes_max THEN excluded.process_external_bytes_max ELSE process_event_loop_hourly.process_external_bytes_max END,
      process_array_buffers_bytes_sum = process_event_loop_hourly.process_array_buffers_bytes_sum + excluded.process_array_buffers_bytes_sum,
      process_array_buffers_bytes_max = CASE WHEN excluded.process_array_buffers_bytes_max IS NULL THEN process_event_loop_hourly.process_array_buffers_bytes_max WHEN process_event_loop_hourly.process_array_buffers_bytes_max IS NULL OR excluded.process_array_buffers_bytes_max > process_event_loop_hourly.process_array_buffers_bytes_max THEN excluded.process_array_buffers_bytes_max ELSE process_event_loop_hourly.process_array_buffers_bytes_max END,
      updated_at = excluded.updated_at
  `, [
    statHour,
    input.processRole,
    eventLoopLagMs ?? 0,
    eventLoopLagMs === null ? 0 : 1,
    eventLoopLagMs,
    processRssBytes ?? 0,
    processRssBytes,
    processHeapUsedBytes ?? 0,
    processHeapUsedBytes,
    processHeapTotalBytes ?? 0,
    processHeapTotalBytes,
    processExternalBytes ?? 0,
    processExternalBytes,
    processArrayBuffersBytes ?? 0,
    processArrayBuffersBytes,
    updatedAt
  ])
}

async function insertRowsAsync(client: DatabaseClient, tableName: string, columns: string[], rows: unknown[][]): Promise<void> {
  const columnList = columns.map((column) => client.dialect.quoteIdentifier(column)).join(', ')
  for (const chunk of chunkValues(rows, 250)) {
    if (chunk.length === 0) continue
    const placeholders = chunk
      .map((row) => `(${row.map(() => '?').join(', ')})`)
      .join(', ')
    await client.execute(`
      INSERT INTO ${statsTable(client, tableName)} (${columnList})
      VALUES ${placeholders}
    `, chunk.flat())
  }
}

function statsTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable('juhe_stats', tableName)
}
