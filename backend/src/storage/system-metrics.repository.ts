import type { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import type { ProcessEventLoopRole } from '../shared/process-event-loop-monitor.js'
import type { AccountUsageStatsRange } from '../domain/types.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getStatsDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues } from './query-utils.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import { hourKey, usageStatsTimezone, usageStatsTimezoneAsync } from './usage-stats-helpers.js'
import { mapProcessEventLoopHourly, mapSystemMetricsHourly } from './usage-stats-mappers.js'
import { aggregateSystemMetricsRows, nullableNumber } from './usage-stats-metric-aggregates.js'
import { normalizeDefaultUsageStatsRange } from './usage-stats-runtime-helpers.js'
import type { ProcessEventLoopSampleInput, SystemMetricsOverview, SystemMetricsSampleInput } from './usage-stats-types.js'
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

export interface SystemMetricsTrendWindowSnapshotContext {
  ranges: AccountUsageStatsRange[]
  earliestDate: string
  todayKey: string
  updatedAt: string
}

export function refreshSystemMetricsTrendWindowSnapshotsStage(database: DatabaseSync, context: SystemMetricsTrendWindowSnapshotContext): void {
  database.prepare('DELETE FROM system_metrics_trend_windows').run()
  database.prepare('DELETE FROM process_event_loop_trend_windows').run()
  refreshSystemMetricsTrendWindows(database, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
  refreshProcessEventLoopTrendWindows(database, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
}

export async function refreshSystemMetricsTrendWindowSnapshotsStageAsync(client: DatabaseClient, context: SystemMetricsTrendWindowSnapshotContext): Promise<void> {
  await client.execute(`DELETE FROM ${statsTable(client, 'system_metrics_trend_windows')}`)
  await client.execute(`DELETE FROM ${statsTable(client, 'process_event_loop_trend_windows')}`)
  await refreshSystemMetricsTrendWindowsAsync(client, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
  await refreshProcessEventLoopTrendWindowsAsync(client, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
}

export function insertSystemMetricsSample(input: SystemMetricsSampleInput): void {
  const database = getStatsDatabase()
  const sampledAt = input.sampledAt ?? nowIso()
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
  const sampledAt = input.sampledAt ?? nowIso()
  const statHour = hourKey(new Date(sampledAt), await usageStatsTimezoneAsync())
  await client.transaction(async (tx) => {
    await tx.execute(`
      INSERT INTO ${statsTable(tx, 'system_metrics_samples')} (
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
    await upsertSystemMetricsHourlyAsync(tx, statHour, input, sampledAt)
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
  const sampledAt = input.sampledAt ?? nowIso()
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

  const client = createPostgresDatabaseClient(await getPostgresPool())
  const sampledAt = input.sampledAt ?? nowIso()
  const statHour = hourKey(new Date(sampledAt), await usageStatsTimezoneAsync())
  await client.transaction(async (tx) => {
    await tx.execute(`
      INSERT INTO ${statsTable(tx, 'process_event_loop_samples')} (
        sampled_at, process_role, process_pid, event_loop_lag_ms,
        process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes,
        process_external_bytes, process_array_buffers_bytes, id, created_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, [
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
    ])
    await upsertProcessEventLoopHourlyAsync(tx, statHour, {
      ...input,
      eventLoopLagMs: eventLoopLagMs ?? undefined,
      processRssBytes: processRssBytes ?? undefined,
      processHeapUsedBytes: processHeapUsedBytes ?? undefined,
      processHeapTotalBytes: processHeapTotalBytes ?? undefined,
      processExternalBytes: processExternalBytes ?? undefined,
      processArrayBuffersBytes: processArrayBuffersBytes ?? undefined
    }, sampledAt)
  })
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
  const processLatestStatement = database.prepare(`
    SELECT ${processEventLoopLatestSelectColumns()}
    FROM process_event_loop_samples
    WHERE process_role = ?
    ORDER BY sampled_at DESC, id DESC
    LIMIT 1
  `)
  const processLatestRows = PROCESS_EVENT_LOOP_ROLES
    .map((role) => processLatestStatement.get(role) as unknown as Record<string, unknown> | undefined)
    .filter((row): row is Record<string, unknown> => Boolean(row))
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
  const peakStatement = database.prepare(`
    SELECT ${processEventLoopLatestSelectColumns()}
    FROM process_event_loop_samples INDEXED BY idx_process_event_loop_samples_role_peak
    WHERE process_role = ?
      AND sampled_at >= ?
      AND event_loop_lag_ms IS NOT NULL
    ORDER BY event_loop_lag_ms DESC, sampled_at DESC, id DESC
    LIMIT 1
  `)
  return PROCESS_EVENT_LOOP_ROLES
    .map((role) => peakStatement.get(role, startedAt) as unknown as Record<string, unknown> | undefined)
    .filter((row): row is Record<string, unknown> => Boolean(row))
}

async function processEventLoopLatestRowsAsync(client: DatabaseClient): Promise<Array<Record<string, unknown>>> {
  return client.query<Record<string, unknown>>(`
    SELECT DISTINCT ON (process_role) ${processEventLoopLatestSelectColumns()}
    FROM ${statsTable(client, 'process_event_loop_samples')}
    WHERE process_role IN (${PROCESS_EVENT_LOOP_ROLES.map(() => '?').join(', ')})
    ORDER BY process_role, sampled_at DESC, id DESC
  `, PROCESS_EVENT_LOOP_ROLES)
}

async function processEventLoopPeakRowsAsync(client: DatabaseClient, startedAt: string): Promise<Array<Record<string, unknown>>> {
  return client.query<Record<string, unknown>>(`
    SELECT DISTINCT ON (process_role) ${processEventLoopLatestSelectColumns()}
    FROM ${statsTable(client, 'process_event_loop_samples')}
    WHERE process_role IN (${PROCESS_EVENT_LOOP_ROLES.map(() => '?').join(', ')})
      AND sampled_at >= ?
      AND event_loop_lag_ms IS NOT NULL
    ORDER BY process_role, event_loop_lag_ms DESC, sampled_at DESC, id DESC
  `, [...PROCESS_EVENT_LOOP_ROLES, startedAt])
}

function processRoleFromValue(value: unknown): ProcessEventLoopRole | undefined {
  return PROCESS_EVENT_LOOP_ROLES.find((role) => role === value)
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
      sampledAt: String(row.sampled_at ?? ''),
      eventLoopLagMs: nullableNumber(row.event_loop_lag_ms) ?? undefined,
      processRssBytes: nullableNumber(row.process_rss_bytes) ?? undefined,
      processHeapUsedBytes: nullableNumber(row.process_heap_used_bytes) ?? undefined,
      processHeapTotalBytes: nullableNumber(row.process_heap_total_bytes) ?? undefined,
      processExternalBytes: nullableNumber(row.process_external_bytes) ?? undefined,
      processArrayBuffersBytes: nullableNumber(row.process_array_buffers_bytes) ?? undefined
    })
  }
  return PROCESS_EVENT_LOOP_ROLES.map((processRole) => {
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
