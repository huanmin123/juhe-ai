import type { DatabaseSync } from 'node:sqlite'

import type { AccountUsageSummary } from '../domain/types.js'
import { canAccessAll, currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { getDatabase, newId, nowIso } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import {
  averageFromSum,
  dateKey,
  emptyAccountUsageSummary,
  emptyUsageByWindow,
  hourKey,
  numberFromUnknown,
  USAGE_STATS_WINDOWS,
  usageSummaryFromAggregate
} from './usage-stats-helpers.js'

const USAGE_STATS_CURSOR_SAFETY_DELAY_SECONDS = 5
const GLOBAL_STATS_SYSTEM_ACCOUNT_ID = 'global'
const GLOBAL_STATS_SCOPE_ID = 'global'

interface AccountUsageAggregateRow {
  account_id: string
  request_count: number
  client_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  total_cost: number
  last_used_at: string | null
}

interface UsageStatsRecordRow {
  id: string
  system_account_id: string
  request_id: string
  client_ip: string | null
  api_key_id: string | null
  group_id: string | null
  account_id: string | null
  endpoint: string | null
  provider_code: string | null
  model: string | null
  status_code: number | null
  success: number
  first_token_ms: number | null
  duration_ms: number | null
  input_tokens: number | null
  output_tokens: number | null
  cache_read_tokens: number | null
  cost_usd: number | null
  error_code: string | null
  error_message: string | null
  account_owner_system_account_id: string | null
  group_owner_system_account_id: string | null
  account_access_type: string | null
  group_access_type: string | null
  account_authorization_id: string | null
  group_authorization_id: string | null
  created_at: string
}

interface StatsJobStateRow {
  cursor_created_at: string | null
  cursor_id: string | null
  lag_seconds: number
}

interface UsageStatsAccumulator {
  requestCount: number
  successCount: number
  errorCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  totalCostUsd: number
  durationMsSum: number
  durationMsCount: number
  firstTokenMsSum: number
  firstTokenMsCount: number
  lastUsedAt?: string
  lastErrorAt?: string
}

interface StatsAggregateMathRow {
  request_count: number
  success_count: number
  error_count: number
  client_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  total_cost: number
  duration_ms_sum: number
  duration_ms_count: number
  first_token_ms_sum: number
  first_token_ms_count: number
  last_used_at: string | null
}

export interface SystemMetricsSampleInput {
  cpuPercent?: number
  memoryUsedPercent?: number
  memoryTotalBytes?: number
  memoryFreeBytes?: number
  processRssBytes?: number
  processHeapUsedBytes?: number
  processHeapTotalBytes?: number
  eventLoopLagMs?: number
  networkRxBytesPerSecond?: number
  networkTxBytesPerSecond?: number
  networkRxTotalBytes?: number
  networkTxTotalBytes?: number
  dbFileBytes?: number
  statsLagSeconds?: number
}

export interface UsageStatsOverview {
  today: AccountUsageSummary & { successCount: number; errorCount: number; errorRate: number; averageDurationMs?: number; averageFirstTokenMs?: number }
  totals: AccountUsageSummary & { successCount: number; errorCount: number; errorRate: number; averageDurationMs?: number; averageFirstTokenMs?: number }
  hourlyTrend: Array<{ statHour: string; requestCount: number; totalTokens: number; totalCost: number; averageDurationMs?: number; errorCount: number }>
  modelDistribution: Array<{ model: string; providerCode: string; requestCount: number; totalTokens: number; totalCost: number }>
  errors: Array<{ errorCode: string; providerCode: string; statusCode?: number; errorMessage?: string; errorCount: number }>
  statsLagSeconds: number
}

export interface SystemMetricsOverview {
  latest?: {
    sampledAt: string
    cpuPercent?: number
    memoryUsedPercent?: number
    memoryTotalBytes?: number
    memoryFreeBytes?: number
    processRssBytes?: number
    processHeapUsedBytes?: number
    processHeapTotalBytes?: number
    eventLoopLagMs?: number
    networkRxBytesPerSecond?: number
    networkTxBytesPerSecond?: number
    networkRxTotalBytes?: number
    networkTxTotalBytes?: number
    dbFileBytes?: number
    statsLagSeconds?: number
  }
  hourlyTrend: Array<{
    statHour: string
    sampleCount: number
    cpuPercentAvg?: number
    cpuPercentMax?: number
    memoryUsedPercentAvg?: number
    memoryUsedPercentMax?: number
    eventLoopLagMsAvg?: number
    eventLoopLagMsMax?: number
    networkRxBytesPerSecondAvg?: number
    networkRxBytesPerSecondMax?: number
    networkTxBytesPerSecondAvg?: number
    networkTxBytesPerSecondMax?: number
    networkRxTotalBytesMax?: number
    networkTxTotalBytesMax?: number
    processRssBytesMax?: number
    processHeapUsedBytesMax?: number
    dbFileBytesMax?: number
    statsLagSecondsMax?: number
  }>
}

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
    SELECT scope_id AS account_id, request_count, success_count, error_count, client_count,
      input_tokens, output_tokens, cache_read_tokens, total_cost_usd AS total_cost,
      duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count, last_used_at
    FROM usage_stats_daily
    WHERE system_account_id = ? AND scope_type = 'system_account' AND scope_id = ? AND stat_date = ?
  `).get(statsScope.systemAccountId, statsScope.scopeId, today) as unknown as AccountUsageAggregateRow & StatsAggregateMathRow | undefined

  const totalRow = database.prepare(`
    SELECT scope_id AS account_id, request_count, success_count, error_count, client_count,
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
    latest: latest
      ? {
          sampledAt: String(latest.sampled_at),
          cpuPercent: numberFromUnknown(latest.cpu_percent),
          memoryUsedPercent: numberFromUnknown(latest.memory_used_percent),
          memoryTotalBytes: numberFromUnknown(latest.memory_total_bytes),
          memoryFreeBytes: numberFromUnknown(latest.memory_free_bytes),
          processRssBytes: numberFromUnknown(latest.process_rss_bytes),
          processHeapUsedBytes: numberFromUnknown(latest.process_heap_used_bytes),
          processHeapTotalBytes: numberFromUnknown(latest.process_heap_total_bytes),
          eventLoopLagMs: numberFromUnknown(latest.event_loop_lag_ms),
          networkRxBytesPerSecond: numberFromUnknown(latest.network_rx_bytes_per_sec),
          networkTxBytesPerSecond: numberFromUnknown(latest.network_tx_bytes_per_sec),
          networkRxTotalBytes: numberFromUnknown(latest.network_rx_total_bytes),
          networkTxTotalBytes: numberFromUnknown(latest.network_tx_total_bytes),
          dbFileBytes: numberFromUnknown(latest.db_file_bytes),
          statsLagSeconds: numberFromUnknown(latest.stats_lag_seconds)
        }
      : undefined,
    hourlyTrend: rows.map((row) => {
      const sampleCount = Number(row.sample_count ?? 0)
      return {
        statHour: String(row.stat_hour),
        sampleCount,
        cpuPercentAvg: averageFromSum(row.cpu_percent_sum, sampleCount),
        cpuPercentMax: numberFromUnknown(row.cpu_percent_max),
        memoryUsedPercentAvg: averageFromSum(row.memory_used_percent_sum, sampleCount),
        memoryUsedPercentMax: numberFromUnknown(row.memory_used_percent_max),
        eventLoopLagMsAvg: averageFromSum(row.event_loop_lag_ms_sum, sampleCount),
        eventLoopLagMsMax: numberFromUnknown(row.event_loop_lag_ms_max),
        networkRxBytesPerSecondAvg: averageFromSum(row.network_rx_bytes_per_sec_sum, row.network_rx_bytes_per_sec_count),
        networkRxBytesPerSecondMax: numberFromUnknown(row.network_rx_bytes_per_sec_max),
        networkTxBytesPerSecondAvg: averageFromSum(row.network_tx_bytes_per_sec_sum, row.network_tx_bytes_per_sec_count),
        networkTxBytesPerSecondMax: numberFromUnknown(row.network_tx_bytes_per_sec_max),
        networkRxTotalBytesMax: numberFromUnknown(row.network_rx_total_bytes_max),
        networkTxTotalBytesMax: numberFromUnknown(row.network_tx_total_bytes_max),
        processRssBytesMax: numberFromUnknown(row.process_rss_bytes_max),
        processHeapUsedBytesMax: numberFromUnknown(row.process_heap_used_bytes_max),
        dbFileBytesMax: numberFromUnknown(row.db_file_bytes_max),
        statsLagSecondsMax: numberFromUnknown(row.stats_lag_seconds_max)
      }
    })
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

function aggregateUsageStatsRecord(database: DatabaseSync, row: UsageStatsRecordRow, updatedAt: string): void {
  const createdAt = new Date(row.created_at)
  const statDate = dateKey(createdAt)
  const statHour = hourKey(createdAt)
  for (const entry of usageStatsEntries(row)) {
    upsertUsageStatsTotal(database, entry.systemAccountId, entry.scopeType, entry.scopeId, entry.accumulator, updatedAt)
    upsertUsageStatsDaily(database, entry.systemAccountId, entry.scopeType, entry.scopeId, statDate, entry.accumulator, updatedAt)
    upsertUsageStatsHourly(database, entry.systemAccountId, entry.scopeType, entry.scopeId, statHour, entry.accumulator, updatedAt)
    upsertUsageStatsClient(database, row, entry.systemAccountId, entry.scopeType, entry.scopeId, 'all')
    upsertUsageStatsClient(database, row, entry.systemAccountId, entry.scopeType, entry.scopeId, statDate)
  }
  if (row.model) upsertUsageModelDaily(database, row, statDate, updatedAt)
  if (row.success !== 1) upsertUsageErrorDaily(database, row, statDate, updatedAt)
}

function usageStatsEntries(row: UsageStatsRecordRow): Array<{ systemAccountId: string; scopeType: string; scopeId: string; accumulator: UsageStatsAccumulator }> {
  const accumulator = usageStatsAccumulatorFromRecord(row)
  const callerSystemAccountId = row.system_account_id
  const accountOwnerSystemAccountId = row.account_owner_system_account_id ?? callerSystemAccountId
  const groupOwnerSystemAccountId = row.group_owner_system_account_id ?? callerSystemAccountId
  const entries = [
    { systemAccountId: callerSystemAccountId, scopeType: 'system_account', scopeId: callerSystemAccountId, accumulator },
    { systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeType: 'system_account', scopeId: GLOBAL_STATS_SCOPE_ID, accumulator }
  ]
  if (row.provider_code) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'provider', scopeId: row.provider_code, accumulator })
  if (row.group_id) entries.push({ systemAccountId: groupOwnerSystemAccountId, scopeType: 'group', scopeId: row.group_id, accumulator })
  if (row.account_id) entries.push({ systemAccountId: accountOwnerSystemAccountId, scopeType: 'account', scopeId: row.account_id, accumulator })
  if (row.account_authorization_id && accountOwnerSystemAccountId !== callerSystemAccountId) entries.push({ systemAccountId: accountOwnerSystemAccountId, scopeType: 'account_authorization', scopeId: row.account_authorization_id, accumulator })
  if (row.group_authorization_id && groupOwnerSystemAccountId !== callerSystemAccountId) entries.push({ systemAccountId: groupOwnerSystemAccountId, scopeType: 'group_authorization', scopeId: row.group_authorization_id, accumulator })
  if (row.api_key_id) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'api_key', scopeId: row.api_key_id, accumulator })
  if (row.model) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'model', scopeId: row.model, accumulator })
  if (row.endpoint) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'endpoint', scopeId: row.endpoint, accumulator })
  return entries
}

function usageStatsAccumulatorFromRecord(row: UsageStatsRecordRow): UsageStatsAccumulator {
  const success = row.success === 1
  return {
    requestCount: 1,
    successCount: success ? 1 : 0,
    errorCount: success ? 0 : 1,
    inputTokens: Math.max(0, Number(row.input_tokens ?? 0)),
    outputTokens: Math.max(0, Number(row.output_tokens ?? 0)),
    cacheReadTokens: Math.max(0, Number(row.cache_read_tokens ?? 0)),
    totalCostUsd: Math.max(0, Number(row.cost_usd ?? 0)),
    durationMsSum: row.duration_ms === null ? 0 : Math.max(0, Number(row.duration_ms ?? 0)),
    durationMsCount: row.duration_ms === null ? 0 : 1,
    firstTokenMsSum: row.first_token_ms === null ? 0 : Math.max(0, Number(row.first_token_ms ?? 0)),
    firstTokenMsCount: row.first_token_ms === null ? 0 : 1,
    lastUsedAt: row.created_at,
    lastErrorAt: success ? undefined : row.created_at
  }
}

function upsertUsageStatsTotal(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, total_cost_usd, duration_ms_sum, duration_ms_count,
      first_token_ms_sum, first_token_ms_count, last_used_at, last_error_at, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum,
      duration_ms_count = duration_ms_count + excluded.duration_ms_count,
      first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
      first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
      last_used_at = CASE WHEN excluded.last_used_at IS NULL THEN usage_stats_totals.last_used_at WHEN usage_stats_totals.last_used_at IS NULL OR excluded.last_used_at > usage_stats_totals.last_used_at THEN excluded.last_used_at ELSE usage_stats_totals.last_used_at END,
      last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN usage_stats_totals.last_error_at WHEN usage_stats_totals.last_error_at IS NULL OR excluded.last_error_at > usage_stats_totals.last_error_at THEN excluded.last_error_at ELSE usage_stats_totals.last_error_at END,
      updated_at = excluded.updated_at
  `).run(systemAccountId, scopeType, scopeId, ...statsParamsTail(stats, updatedAt))
}

function upsertUsageStatsDaily(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string, statDate: string, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, total_cost_usd, duration_ms_sum, duration_ms_count,
      first_token_ms_sum, first_token_ms_count, last_used_at, last_error_at, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id, stat_date) DO UPDATE SET
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum,
      duration_ms_count = duration_ms_count + excluded.duration_ms_count,
      first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
      first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
      last_used_at = CASE WHEN excluded.last_used_at IS NULL THEN usage_stats_daily.last_used_at WHEN usage_stats_daily.last_used_at IS NULL OR excluded.last_used_at > usage_stats_daily.last_used_at THEN excluded.last_used_at ELSE usage_stats_daily.last_used_at END,
      last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN usage_stats_daily.last_error_at WHEN usage_stats_daily.last_error_at IS NULL OR excluded.last_error_at > usage_stats_daily.last_error_at THEN excluded.last_error_at ELSE usage_stats_daily.last_error_at END,
      updated_at = excluded.updated_at
  `).run(systemAccountId, scopeType, scopeId, statDate, ...statsParamsTail(stats, updatedAt))
}

function upsertUsageStatsHourly(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string, statHour: string, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    INSERT INTO usage_stats_hourly (system_account_id, scope_type, scope_id, stat_hour, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, total_cost_usd, duration_ms_sum, duration_ms_count,
      first_token_ms_sum, first_token_ms_count, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id, stat_hour) DO UPDATE SET
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum,
      duration_ms_count = duration_ms_count + excluded.duration_ms_count,
      first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
      first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
      updated_at = excluded.updated_at
  `).run(systemAccountId, scopeType, scopeId, statHour, stats.requestCount, stats.successCount, stats.errorCount, stats.inputTokens, stats.outputTokens, stats.cacheReadTokens, stats.totalCostUsd, stats.durationMsSum, stats.durationMsCount, stats.firstTokenMsSum, stats.firstTokenMsCount, updatedAt)
}

function statsParamsTail(stats: UsageStatsAccumulator, updatedAt: string): Array<number | string | null> {
  return [stats.requestCount, stats.successCount, stats.errorCount, stats.inputTokens, stats.outputTokens, stats.cacheReadTokens, stats.totalCostUsd, stats.durationMsSum, stats.durationMsCount, stats.firstTokenMsSum, stats.firstTokenMsCount, stats.lastUsedAt ?? null, stats.lastErrorAt ?? null, updatedAt]
}

function upsertUsageStatsClient(database: DatabaseSync, row: UsageStatsRecordRow, systemAccountId: string, scopeType: string, scopeId: string, statBucket: string): void {
  const clientKey = row.api_key_id ?? row.client_ip ?? ''
  if (!clientKey) return
  const result = database.prepare(`
    INSERT OR IGNORE INTO usage_stats_clients (system_account_id, scope_type, scope_id, stat_bucket, client_key, first_seen_at, last_seen_at)
    VALUES (?, ?, ?, ?, ?, ?, ?)
  `).run(systemAccountId, scopeType, scopeId, statBucket, clientKey, row.created_at, row.created_at)
  if (result.changes <= 0) {
    database.prepare(`
      UPDATE usage_stats_clients
      SET last_seen_at = ?
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_bucket = ? AND client_key = ?
    `).run(row.created_at, systemAccountId, scopeType, scopeId, statBucket, clientKey)
    return
  }
  if (statBucket === 'all') {
    database.prepare('UPDATE usage_stats_totals SET client_count = client_count + 1 WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?').run(systemAccountId, scopeType, scopeId)
    return
  }
  database.prepare('UPDATE usage_stats_daily SET client_count = client_count + 1 WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_date = ?').run(systemAccountId, scopeType, scopeId, statBucket)
}

function upsertUsageModelDaily(database: DatabaseSync, row: UsageStatsRecordRow, statDate: string, updatedAt: string): void {
  const stats = usageStatsAccumulatorFromRecord(row)
  for (const systemAccountId of [row.system_account_id, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    database.prepare(`
      INSERT INTO usage_model_daily (system_account_id, stat_date, provider_code, model, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, total_cost_usd, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(system_account_id, stat_date, provider_code, model) DO UPDATE SET
        request_count = request_count + excluded.request_count,
        success_count = success_count + excluded.success_count,
        error_count = error_count + excluded.error_count,
        input_tokens = input_tokens + excluded.input_tokens,
        output_tokens = output_tokens + excluded.output_tokens,
        cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
        total_cost_usd = total_cost_usd + excluded.total_cost_usd,
        updated_at = excluded.updated_at
    `).run(systemAccountId, statDate, row.provider_code ?? 'unknown', row.model ?? 'unknown', stats.requestCount, stats.successCount, stats.errorCount, stats.inputTokens, stats.outputTokens, stats.cacheReadTokens, stats.totalCostUsd, updatedAt)
  }
}

function upsertUsageErrorDaily(database: DatabaseSync, row: UsageStatsRecordRow, statDate: string, updatedAt: string): void {
  const errorGroup = row.provider_code ?? 'unknown'
  const errorCode = row.error_code ?? String(row.status_code ?? 'unknown')
  for (const systemAccountId of [row.system_account_id, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    database.prepare(`
      INSERT INTO usage_error_daily (system_account_id, stat_date, error_group, provider_code, error_code, status_code, error_message, request_count, error_count, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1, ?)
      ON CONFLICT(system_account_id, stat_date, error_group, error_code) DO UPDATE SET
        provider_code = excluded.provider_code,
        status_code = excluded.status_code,
        error_message = COALESCE(excluded.error_message, usage_error_daily.error_message),
        request_count = request_count + excluded.request_count,
        error_count = error_count + excluded.error_count,
        updated_at = excluded.updated_at
    `).run(systemAccountId, statDate, errorGroup, row.provider_code ?? 'unknown', errorCode, row.status_code ?? 0, row.error_message ?? null, updatedAt)
  }
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
  database.prepare("DELETE FROM usage_stats_clients WHERE stat_bucket <> 'all' AND stat_bucket < ?").run(dailyCutoff)
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

function usageSummaryWithMath(row: AccountUsageAggregateRow & StatsAggregateMathRow): AccountUsageSummary & { successCount: number; errorCount: number; errorRate: number; averageDurationMs?: number; averageFirstTokenMs?: number } {
  const summary = usageSummaryFromAggregate(row)
  const successCount = Number(row.success_count ?? 0)
  const errorCount = Number(row.error_count ?? 0)
  const requestCount = Number(row.request_count ?? 0)
  return {
    ...summary,
    successCount,
    errorCount,
    errorRate: requestCount > 0 ? errorCount / requestCount : 0,
    averageDurationMs: averageFromSum(row.duration_ms_sum, row.duration_ms_count),
    averageFirstTokenMs: averageFromSum(row.first_token_ms_sum, row.first_token_ms_count)
  }
}

function emptyStatsAggregateMathRow(): AccountUsageAggregateRow & StatsAggregateMathRow {
  return {
    account_id: '',
    request_count: 0,
    success_count: 0,
    error_count: 0,
    client_count: 0,
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    total_cost: 0,
    duration_ms_sum: 0,
    duration_ms_count: 0,
    first_token_ms_sum: 0,
    first_token_ms_count: 0,
    last_used_at: null
  }
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
