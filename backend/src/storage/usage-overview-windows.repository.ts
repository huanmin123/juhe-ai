import type { DatabaseSync } from 'node:sqlite'

import type { AccountUsageStatsRange } from '../domain/types.js'
import {
  aggregateUsageErrorRows,
  aggregateUsageModelRows,
  aggregateUsageRowsForRange,
  aggregateUsageTrendBuckets,
  type UsageErrorWindowRow,
  type UsageModelWindowRow,
  type UsageOverviewHourlyWindowRow,
  type UsageStatsDailyWindowRow
} from './usage-stats-window-aggregates.js'
import {
  rangeWindowKey,
  rowsByStatDate,
  rowsByStatHourDate,
  sortedMapEntries
} from './usage-stats-window-helpers.js'
import {
  GLOBAL_STATS_SCOPE_ID,
  GLOBAL_STATS_SYSTEM_ACCOUNT_ID
} from './usage-stats-types.js'

const maxUsageOverviewSnapshotScopes = 5000

export interface UsageOverviewWindowRefreshContext {
  ranges: AccountUsageStatsRange[]
  earliestDate: string
  todayKey: string
  updatedAt: string
  overviewScopes: Array<{ systemAccountId: string; scopeId: string }>
  uniqueSystemAccountIds: string[]
}

export function refreshUsageOverviewWindowSnapshots(database: DatabaseSync, context: UsageOverviewWindowRefreshContext): void {
  refreshUsageOverviewSummaryWindowSnapshots(database, context)
  refreshUsageOverviewTrendWindowSnapshots(database, context)
  refreshUsageModelRankWindowSnapshots(database, context)
  refreshUsageErrorRankWindowSnapshots(database, context)
}

export function usageOverviewSnapshotScopes(database: DatabaseSync): Array<{ systemAccountId: string; scopeId: string }> {
  const rows = database.prepare(`
    SELECT system_account_id, scope_id
    FROM usage_stats_totals
    WHERE scope_type = 'system_account'
    ORDER BY updated_at DESC, system_account_id ASC, scope_id ASC
    LIMIT ?
  `).all(maxUsageOverviewSnapshotScopes) as unknown as Array<{ system_account_id?: string | null; scope_id?: string | null }>
  const scopes = rows
    .map((row) => ({ systemAccountId: row.system_account_id ?? '', scopeId: row.scope_id ?? '' }))
    .filter((row) => row.systemAccountId && row.scopeId)
  if (!scopes.some((scope) => scope.systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID && scope.scopeId === GLOBAL_STATS_SCOPE_ID)) {
    scopes.push({ systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeId: GLOBAL_STATS_SCOPE_ID })
  }
  return scopes
}

function refreshUsageOverviewSummaryWindowSnapshots(database: DatabaseSync, context: UsageOverviewWindowRefreshContext): void {
  database.prepare('DELETE FROM usage_overview_summary_windows').run()
  for (const scope of context.overviewScopes) {
    refreshUsageOverviewSummaryWindows(database, scope, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
  }
}

function refreshUsageOverviewTrendWindowSnapshots(database: DatabaseSync, context: UsageOverviewWindowRefreshContext): void {
  database.prepare('DELETE FROM usage_overview_trend_windows').run()
  for (const scope of context.overviewScopes) {
    refreshUsageOverviewTrendWindows(database, scope, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
  }
}

function refreshUsageModelRankWindowSnapshots(database: DatabaseSync, context: UsageOverviewWindowRefreshContext): void {
  database.prepare('DELETE FROM usage_model_rank_windows').run()
  for (const systemAccountId of [...context.uniqueSystemAccountIds, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    refreshUsageModelRankWindows(database, systemAccountId, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
  }
}

function refreshUsageErrorRankWindowSnapshots(database: DatabaseSync, context: UsageOverviewWindowRefreshContext): void {
  database.prepare('DELETE FROM usage_error_rank_windows').run()
  for (const systemAccountId of [...context.uniqueSystemAccountIds, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    refreshUsageErrorRankWindows(database, systemAccountId, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
  }
}

function refreshUsageOverviewSummaryWindows(
  database: DatabaseSync,
  scope: { systemAccountId: string; scopeId: string },
  ranges: AccountUsageStatsRange[],
  earliestDate: string,
  todayKey: string,
  updatedAt: string
): void {
  const rows = database.prepare(`
    SELECT stat_date, request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
      cache_read_cost_usd, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max, last_used_at
    FROM usage_stats_daily
    WHERE system_account_id = ?
      AND scope_type = 'system_account'
      AND scope_id = ?
      AND stat_date >= ?
      AND stat_date <= ?
    ORDER BY stat_date ASC
  `).all(scope.systemAccountId, scope.scopeId, earliestDate, todayKey) as unknown as UsageStatsDailyWindowRow[]
  const rowsByDate = rowsByStatDate(rows)
  const insert = database.prepare(`
    INSERT INTO usage_overview_summary_windows (
      system_account_id, window_key, start_date, end_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count,
      last_used_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const range of ranges) {
    const aggregate = aggregateUsageRowsForRange(rowsByDate, range)
    insert.run(
      scope.systemAccountId,
      rangeWindowKey(range),
      range.startDate,
      range.endDate,
      aggregate.requestCount,
      aggregate.successCount,
      aggregate.errorCount,
      aggregate.inputTokens,
      aggregate.outputTokens,
      aggregate.cacheReadTokens,
      aggregate.cacheReadCostUsd,
      aggregate.totalCostUsd,
      aggregate.durationMsSum,
      aggregate.durationMsCount,
      aggregate.firstTokenMsSum,
      aggregate.firstTokenMsCount,
      aggregate.lastUsedAt ?? null,
      updatedAt
    )
  }
}

function refreshUsageOverviewTrendWindows(
  database: DatabaseSync,
  scope: { systemAccountId: string; scopeId: string },
  ranges: AccountUsageStatsRange[],
  earliestDate: string,
  todayKey: string,
  updatedAt: string
): void {
  const rows = database.prepare(`
    SELECT stat_hour, request_count, error_count, input_tokens, output_tokens, cache_read_tokens,
      cache_read_cost_usd, total_cost_usd, duration_ms_sum, duration_ms_count
    FROM usage_stats_hourly
    WHERE system_account_id = ?
      AND scope_type = 'system_account'
      AND scope_id = ?
      AND stat_hour >= ?
      AND stat_hour <= ?
    ORDER BY stat_hour ASC
  `).all(scope.systemAccountId, scope.scopeId, `${earliestDate}T00`, `${todayKey}T23`) as unknown as UsageOverviewHourlyWindowRow[]
  const rowsByDate = rowsByStatHourDate(rows)
  const insert = database.prepare(`
    INSERT INTO usage_overview_trend_windows (
      system_account_id, window_key, start_date, end_date, bucket_key, request_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const range of ranges) {
    const buckets = aggregateUsageTrendBuckets(rowsByDate, range)
    for (const [bucketKey, bucket] of sortedMapEntries(buckets)) {
      insert.run(
        scope.systemAccountId,
        rangeWindowKey(range),
        range.startDate,
        range.endDate,
        bucketKey,
        bucket.requestCount,
        bucket.errorCount,
        bucket.inputTokens,
        bucket.outputTokens,
        bucket.cacheReadTokens,
        bucket.cacheReadCostUsd,
        bucket.totalCostUsd,
        bucket.durationMsSum,
        bucket.durationMsCount,
        updatedAt
      )
    }
  }
}

function refreshUsageModelRankWindows(
  database: DatabaseSync,
  systemAccountId: string,
  ranges: AccountUsageStatsRange[],
  earliestDate: string,
  todayKey: string,
  updatedAt: string
): void {
  const rows = database.prepare(`
    SELECT stat_date, provider_code, model, request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd
    FROM usage_model_daily
    WHERE system_account_id = ?
      AND stat_date >= ?
      AND stat_date <= ?
    ORDER BY stat_date ASC
  `).all(systemAccountId, earliestDate, todayKey) as unknown as UsageModelWindowRow[]
  const rowsByDate = rowsByStatDate(rows)
  const insert = database.prepare(`
    INSERT INTO usage_model_rank_windows (
      system_account_id, window_key, start_date, end_date, rank, provider_code, model,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const range of ranges) {
    const rankedRows = aggregateUsageModelRows(rowsByDate, range)
    rankedRows.slice(0, 10).forEach((row, index) => {
      insert.run(
        systemAccountId,
        rangeWindowKey(range),
        range.startDate,
        range.endDate,
        index + 1,
        row.providerCode,
        row.model,
        row.requestCount,
        row.inputTokens,
        row.outputTokens,
        row.cacheReadTokens,
        row.cacheReadCostUsd,
        row.totalCostUsd,
        updatedAt
      )
    })
  }
}

function refreshUsageErrorRankWindows(
  database: DatabaseSync,
  systemAccountId: string,
  ranges: AccountUsageStatsRange[],
  earliestDate: string,
  todayKey: string,
  updatedAt: string
): void {
  const rows = database.prepare(`
    SELECT stat_date, error_group, provider_code, error_code, status_code, error_message, error_count
    FROM usage_error_daily
    WHERE system_account_id = ?
      AND stat_date >= ?
      AND stat_date <= ?
    ORDER BY stat_date ASC
  `).all(systemAccountId, earliestDate, todayKey) as unknown as UsageErrorWindowRow[]
  const rowsByDate = rowsByStatDate(rows)
  const insert = database.prepare(`
    INSERT INTO usage_error_rank_windows (
      system_account_id, window_key, start_date, end_date, rank, provider_code, error_code,
      status_code, error_message, error_count, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const range of ranges) {
    const rankedRows = aggregateUsageErrorRows(rowsByDate, range)
    rankedRows.slice(0, 10).forEach((row, index) => {
      insert.run(
        systemAccountId,
        rangeWindowKey(range),
        range.startDate,
        range.endDate,
        index + 1,
        row.providerCode,
        row.errorCode,
        row.statusCode,
        row.errorMessage ?? null,
        row.errorCount,
        updatedAt
      )
    })
  }
}
