import type { DatabaseSync } from 'node:sqlite'

import { canAccessAll, currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { getDatabase, newId, nowIso } from './database.js'
import { averageFromSum, dateKey, hourKey } from './usage-stats-helpers.js'
import { emptyStatsAggregateMathRow, mapSystemMetricsHourly, mapSystemMetricsLatest, usageSummaryWithMath } from './usage-stats-mappers.js'
import { aggregateUsageStatsRecord } from './usage-stats-writers.js'
import {
  GLOBAL_STATS_SCOPE_ID,
  GLOBAL_STATS_SYSTEM_ACCOUNT_ID,
  type AccountUsageAggregateRow,
  type StatsAggregateMathRow,
  type StatsJobStateRow,
  type SystemMetricsOverview,
  type SystemMetricsSampleInput,
  type UsageStatsOverview,
  type UsageStatsRecordRow
} from './usage-stats-types.js'

export type { SystemMetricsOverview, SystemMetricsSampleInput, UsageStatsOverview } from './usage-stats-types.js'

const USAGE_STATS_CURSOR_SAFETY_DELAY_SECONDS = 5

export function aggregateUsageStatsBatch(limit = 2000): number {
  const database = getDatabase()
  const state = usageStatsJobState(database)
  const safeCreatedBefore = usageStatsSafeCreatedBefore()
  const rows = database
    .prepare(`
      SELECT *
      FROM usage_records
      WHERE created_at <= ?
        AND (created_at > ? OR (created_at = ? AND id > ?))
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all(safeCreatedBefore, state.cursorCreatedAt, state.cursorCreatedAt, state.cursorId, Math.max(1, limit)) as unknown as UsageStatsRecordRow[]

  if (!rows.length) {
    updateStatsJobState(database, {
      lastSuccessAt: nowIso(),
      lagSeconds: latestUsageRecordLagSeconds(database, safeCreatedBefore, state.cursorCreatedAt, state.cursorId)
    })
    return 0
  }

  const updatedAt = nowIso()
  database.exec('BEGIN')
  try {
    for (const row of rows) {
      aggregateUsageStatsRecord(database, row, updatedAt)
    }
    const last = rows[rows.length - 1]
    updateStatsJobState(database, {
      cursorCreatedAt: last.created_at,
      cursorId: last.id,
      lastSuccessAt: updatedAt,
      lagSeconds: statsLagSecondsFromCursor(last.created_at)
    })
    cleanupStatsCache(database, updatedAt)
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    updateStatsJobState(database, {
      lastErrorMessage: error instanceof Error ? error.message : 'Usage stats aggregation failed',
      lagSeconds: latestUsageStatsLagSeconds()
    })
    throw error
  }

  return rows.length
}

export function refreshGroupAccountStatsCache(): void {
  const database = getDatabase()
  const updatedAt = nowIso()
  database.exec('BEGIN')
  try {
    database.prepare('DELETE FROM group_account_stats').run()
    const activeAuthorizationUntil = updatedAt
    database.prepare(`
      INSERT INTO group_account_stats (
        system_account_id, group_id, total, available, active, disabled, error,
        rate_limited, current_concurrency, concurrency_limit, updated_at
      )
      SELECT
        groups.system_account_id,
        groups.id,
        SUM(CASE
          WHEN accounts.id IS NOT NULL
            AND (
              accounts.system_account_id = groups.system_account_id
              OR (
                account_authorizations.status = 'active'
                AND (account_authorizations.expires_at IS NULL OR account_authorizations.expires_at > ?)
              )
            )
          THEN 1 ELSE 0
        END) AS total,
        SUM(CASE
          WHEN accounts.id IS NOT NULL
            AND (
              accounts.system_account_id = groups.system_account_id
              OR (
                account_authorizations.status = 'active'
                AND (account_authorizations.expires_at IS NULL OR account_authorizations.expires_at > ?)
              )
            )
            AND accounts.status = 'active'
            AND accounts.schedulable = 1
            AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?)
          THEN 1 ELSE 0
        END) AS available,
        SUM(CASE
          WHEN accounts.id IS NOT NULL
            AND (
              accounts.system_account_id = groups.system_account_id
              OR (
                account_authorizations.status = 'active'
                AND (account_authorizations.expires_at IS NULL OR account_authorizations.expires_at > ?)
              )
            )
            AND accounts.status = 'active'
          THEN 1 ELSE 0
        END) AS active,
        SUM(CASE
          WHEN accounts.id IS NOT NULL
            AND (
              accounts.system_account_id = groups.system_account_id
              OR (
                account_authorizations.status = 'active'
                AND (account_authorizations.expires_at IS NULL OR account_authorizations.expires_at > ?)
              )
            )
            AND accounts.status = 'disabled'
          THEN 1 ELSE 0
        END) AS disabled,
        SUM(CASE
          WHEN accounts.id IS NOT NULL
            AND (
              accounts.system_account_id = groups.system_account_id
              OR (
                account_authorizations.status = 'active'
                AND (account_authorizations.expires_at IS NULL OR account_authorizations.expires_at > ?)
              )
            )
            AND accounts.status NOT IN ('active', 'disabled')
          THEN 1 ELSE 0
        END) AS error,
        SUM(CASE
          WHEN accounts.id IS NOT NULL
            AND (
              accounts.system_account_id = groups.system_account_id
              OR (
                account_authorizations.status = 'active'
                AND (account_authorizations.expires_at IS NULL OR account_authorizations.expires_at > ?)
              )
            )
            AND accounts.status = 'rate_limited'
          THEN 1 ELSE 0
        END) AS rate_limited,
        0 AS current_concurrency,
        COALESCE(SUM(CASE
          WHEN accounts.id IS NOT NULL
            AND (
              accounts.system_account_id = groups.system_account_id
              OR (
                account_authorizations.status = 'active'
                AND (account_authorizations.expires_at IS NULL OR account_authorizations.expires_at > ?)
              )
            )
          THEN accounts.concurrency_limit ELSE 0
        END), 0) AS concurrency_limit,
        ? AS updated_at
      FROM groups
      LEFT JOIN group_accounts
        ON group_accounts.group_id = groups.id
        AND group_accounts.system_account_id = groups.system_account_id
        AND group_accounts.enabled = 1
      LEFT JOIN accounts
        ON accounts.id = group_accounts.account_id
      LEFT JOIN resource_authorizations account_authorizations
        ON account_authorizations.id = group_accounts.account_authorization_id
      GROUP BY groups.system_account_id, groups.id
    `).run(
      activeAuthorizationUntil,
      activeAuthorizationUntil,
      updatedAt,
      activeAuthorizationUntil,
      activeAuthorizationUntil,
      activeAuthorizationUntil,
      activeAuthorizationUntil,
      activeAuthorizationUntil,
      updatedAt
    )
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
}

export function insertSystemMetricsSample(input: SystemMetricsSampleInput): void {
  const database = getDatabase()
  const sampledAt = nowIso()
  const statHour = hourKey(new Date(sampledAt))
  database.exec('BEGIN')
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
    cleanupSystemMetrics(database)
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
}

export function latestUsageStatsLagSeconds(): number {
  const row = getDatabase()
    .prepare("SELECT lag_seconds FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'")
    .get() as unknown as { lag_seconds?: number } | undefined
  return Number(row?.lag_seconds ?? 0)
}

export function getUsageStatsOverview(access?: AccessScope): UsageStatsOverview {
  const database = getDatabase()
  const statsScope = usageOverviewStatsScope(access)
  const today = dateKey(new Date())
  const sinceHour = hourKey(new Date(Date.now() - 24 * 60 * 60 * 1000))

  const todayRow = database.prepare(`
    SELECT scope_id AS account_id, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, total_cost_usd AS total_cost,
      duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count, last_used_at
    FROM usage_stats_daily
    WHERE system_account_id = ? AND scope_type = 'system_account' AND scope_id = ? AND stat_date = ?
  `).get(statsScope.systemAccountId, statsScope.scopeId, today) as unknown as AccountUsageAggregateRow & StatsAggregateMathRow | undefined

  const totalRow = database.prepare(`
    SELECT scope_id AS account_id, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, total_cost_usd AS total_cost,
      duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count, last_used_at
    FROM usage_stats_totals
    WHERE system_account_id = ? AND scope_type = 'system_account' AND scope_id = ?
  `).get(statsScope.systemAccountId, statsScope.scopeId) as unknown as AccountUsageAggregateRow & StatsAggregateMathRow | undefined

  const hourlyRows = database.prepare(`
    SELECT stat_hour, request_count, error_count, input_tokens, output_tokens, cache_read_tokens,
      total_cost_usd AS total_cost, duration_ms_sum, duration_ms_count
    FROM usage_stats_hourly
    WHERE system_account_id = ? AND scope_type = 'system_account' AND scope_id = ? AND stat_hour >= ?
    ORDER BY stat_hour ASC
  `).all(statsScope.systemAccountId, statsScope.scopeId, sinceHour) as unknown as Array<StatsAggregateMathRow & { stat_hour: string; error_count: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; total_cost: number }>

  const modelRows = database.prepare(`
    SELECT provider_code, model, request_count, input_tokens, output_tokens, cache_read_tokens,
      total_cost_usd AS total_cost
    FROM usage_model_daily
    WHERE system_account_id = ? AND stat_date = ?
    ORDER BY request_count DESC LIMIT 10
  `).all(statsScope.systemAccountId, today) as unknown as Array<{ provider_code: string; model: string; request_count: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; total_cost: number }>

  const errorRows = database.prepare(`
    SELECT provider_code, error_code, status_code, error_message, error_count
    FROM usage_error_daily
    WHERE system_account_id = ? AND stat_date = ?
    ORDER BY error_count DESC LIMIT 10
  `).all(statsScope.systemAccountId, today) as unknown as Array<{ provider_code: string; error_code: string; status_code: number; error_message: string | null; error_count: number }>

  return {
    today: usageSummaryWithMath(todayRow ?? emptyStatsAggregateMathRow()),
    totals: usageSummaryWithMath(totalRow ?? emptyStatsAggregateMathRow()),
    hourlyTrend: hourlyRows.map((row) => ({
      statHour: row.stat_hour,
      requestCount: Number(row.request_count ?? 0),
      totalTokens: Number(row.input_tokens ?? 0) + Number(row.output_tokens ?? 0) + Number(row.cache_read_tokens ?? 0),
      totalCost: Number(row.total_cost ?? 0),
      averageDurationMs: averageFromSum(row.duration_ms_sum, row.duration_ms_count),
      errorCount: Number(row.error_count ?? 0)
    })),
    modelDistribution: modelRows.map((row) => ({
      providerCode: row.provider_code,
      model: row.model,
      requestCount: Number(row.request_count ?? 0),
      totalTokens: Number(row.input_tokens ?? 0) + Number(row.output_tokens ?? 0) + Number(row.cache_read_tokens ?? 0),
      totalCost: Number(row.total_cost ?? 0)
    })),
    errors: errorRows.map((row) => ({
      providerCode: row.provider_code,
      errorCode: row.error_code,
      statusCode: row.status_code || undefined,
      errorMessage: row.error_message ?? undefined,
      errorCount: Number(row.error_count ?? 0)
    })),
    statsLagSeconds: latestUsageStatsLagSeconds()
  }
}

export function getSystemMetricsOverview(): SystemMetricsOverview {
  const database = getDatabase()
  const latest = database.prepare('SELECT * FROM system_metrics_samples ORDER BY sampled_at DESC LIMIT 1').get() as unknown as Record<string, unknown> | undefined
  const sinceHour = hourKey(new Date(Date.now() - 24 * 60 * 60 * 1000))
  const rows = database.prepare('SELECT * FROM system_metrics_hourly WHERE stat_hour >= ? ORDER BY stat_hour ASC').all(sinceHour) as unknown as Array<Record<string, unknown>>
  return {
    latest: latest ? mapSystemMetricsLatest(latest) : undefined,
    hourlyTrend: rows.map(mapSystemMetricsHourly)
  }
}

function usageOverviewStatsScope(access?: AccessScope): { systemAccountId: string; scopeId: string } {
  const scopedId = scopedSystemAccountId(access)
  if (scopedId) {
    return { systemAccountId: scopedId, scopeId: scopedId }
  }
  if (canAccessAll(access)) {
    return { systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeId: GLOBAL_STATS_SCOPE_ID }
  }
  const systemAccountId = currentSystemAccountId(access)
  return { systemAccountId, scopeId: systemAccountId }
}

function usageStatsJobState(database: DatabaseSync): { cursorCreatedAt: string; cursorId: string } {
  const row = database
    .prepare("SELECT cursor_created_at, cursor_id FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'")
    .get() as unknown as StatsJobStateRow | undefined
  return { cursorCreatedAt: row?.cursor_created_at ?? '', cursorId: row?.cursor_id ?? '' }
}

function usageStatsSafeCreatedBefore(): string {
  return new Date(Date.now() - USAGE_STATS_CURSOR_SAFETY_DELAY_SECONDS * 1000).toISOString()
}

function latestUsageRecordLagSeconds(database: DatabaseSync, safeCreatedBefore: string, cursorCreatedAt: string, cursorId: string): number {
  const latest = database
    .prepare(`
      SELECT created_at
      FROM usage_records
      WHERE created_at <= ?
        AND (created_at > ? OR (created_at = ? AND id > ?))
      ORDER BY created_at DESC, id DESC
      LIMIT 1
    `)
    .get(safeCreatedBefore, cursorCreatedAt, cursorCreatedAt, cursorId) as unknown as { created_at?: string } | undefined
  return latest?.created_at ? statsLagSecondsFromCursor(latest.created_at) : 0
}

function upsertSystemMetricsHourly(database: DatabaseSync, statHour: string, input: SystemMetricsSampleInput, updatedAt: string): void {
  database.prepare(`
    INSERT INTO system_metrics_hourly (
      stat_hour, sample_count, cpu_percent_sum, cpu_percent_max, memory_used_percent_sum,
      memory_used_percent_max, process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum,
      process_heap_used_bytes_max, event_loop_lag_ms_sum, event_loop_lag_ms_max,
      network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_max, network_rx_bytes_per_sec_count,
      network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_max, network_tx_bytes_per_sec_count,
      network_rx_total_bytes_max, network_tx_total_bytes_max,
      db_file_bytes_max, stats_lag_seconds_max, updated_at
    )
    VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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

function updateStatsJobState(database: DatabaseSync, input: { cursorCreatedAt?: string; cursorId?: string; lastSuccessAt?: string; lastErrorMessage?: string; lagSeconds?: number }): void {
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('global', '', 'usage_stats_aggregation', ?, ?, ?, ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = COALESCE(excluded.cursor_created_at, stats_job_state.cursor_created_at),
      cursor_id = COALESCE(excluded.cursor_id, stats_job_state.cursor_id),
      last_success_at = COALESCE(excluded.last_success_at, stats_job_state.last_success_at),
      last_error_message = excluded.last_error_message,
      lag_seconds = excluded.lag_seconds,
      updated_at = excluded.updated_at
  `).run(input.cursorCreatedAt ?? null, input.cursorId ?? null, input.lastSuccessAt ?? null, input.lastErrorMessage ?? null, input.lagSeconds ?? 0, nowIso())
}

function cleanupStatsCache(database: DatabaseSync, now: string): void {
  const dailyRetentionDays = settingsNumberValue('usageStatsDailyRetentionDays', 180, 7, 3650)
  const hourlyRetentionDays = settingsNumberValue('usageStatsHourlyRetentionDays', 14, 1, 365)
  const dailyCutoff = dateKey(new Date(Date.parse(now) - dailyRetentionDays * 24 * 60 * 60 * 1000))
  const hourlyCutoff = hourKey(new Date(Date.parse(now) - hourlyRetentionDays * 24 * 60 * 60 * 1000))
  database.prepare('DELETE FROM usage_stats_daily WHERE stat_date < ?').run(dailyCutoff)
  database.prepare('DELETE FROM usage_model_daily WHERE stat_date < ?').run(dailyCutoff)
  database.prepare('DELETE FROM usage_error_daily WHERE stat_date < ?').run(dailyCutoff)
  database.prepare('DELETE FROM usage_stats_hourly WHERE stat_hour < ?').run(hourlyCutoff)
}

function cleanupSystemMetrics(database: DatabaseSync): void {
  const retentionDays = settingsNumberValue('systemMetricsRetentionDays', 14, 1, 365)
  const cutoff = new Date(Date.now() - retentionDays * 24 * 60 * 60 * 1000).toISOString()
  const hourlyCutoff = hourKey(new Date(Date.now() - 180 * 24 * 60 * 60 * 1000))
  database.prepare('DELETE FROM system_metrics_samples WHERE sampled_at < ?').run(cutoff)
  database.prepare('DELETE FROM system_metrics_hourly WHERE stat_hour < ?').run(hourlyCutoff)
}

function statsLagSecondsFromCursor(cursorCreatedAt: string): number {
  const cursorTime = Date.parse(cursorCreatedAt)
  return Number.isFinite(cursorTime) ? Math.max(0, Math.floor((Date.now() - cursorTime) / 1000)) : 0
}

function settingsNumberValue(key: string, fallback: number, min: number, max: number): number {
  const value = getSettingsValue(key)
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? Math.min(Math.max(Math.trunc(number), min), max) : fallback
}

function getSettingsValue(key: string): unknown {
  const row = getDatabase()
    .prepare('SELECT value_json FROM system_settings WHERE system_account_id = ? AND key = ?')
    .get(currentSystemAccountId(), key) as unknown as { value_json?: string } | undefined
  if (!row?.value_json) return undefined
  try {
    return JSON.parse(row.value_json) as unknown
  } catch {
    return undefined
  }
}
