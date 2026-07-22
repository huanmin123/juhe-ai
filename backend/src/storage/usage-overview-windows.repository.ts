import type { DatabaseSync } from 'node:sqlite'

import type { AccountUsageStatsRange } from '../domain/types.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, rollbackDatabaseTransaction } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { chunkValues } from './query-utils.js'
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

interface UsageOverviewWindowRefreshOptions {
  endDate?: string
  minEndDate?: string
}

interface UsageOverviewIncrementalWindowRefresh {
  context: UsageOverviewWindowRefreshContext
  options: UsageOverviewWindowRefreshOptions
}

export function refreshUsageOverviewWindowSnapshots(database: DatabaseSync, context: UsageOverviewWindowRefreshContext, options: UsageOverviewWindowRefreshOptions = {}): void {
  refreshUsageOverviewSummaryWindowSnapshots(database, context, options)
  refreshUsageOverviewTrendWindowSnapshots(database, context, options)
  refreshUsageModelRankWindowSnapshots(database, context, options)
  refreshUsageErrorRankWindowSnapshots(database, context, options)
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

export async function usageOverviewSnapshotScopesAsync(client: DatabaseClient): Promise<Array<{ systemAccountId: string; scopeId: string }>> {
  const rows = await client.query<{ system_account_id?: string | null; scope_id?: string | null }>(`
    SELECT system_account_id, scope_id
    FROM ${statsTable(client, 'usage_stats_totals')}
    WHERE scope_type = 'system_account'
    ORDER BY updated_at DESC, system_account_id ASC, scope_id ASC
    LIMIT ?
  `, [maxUsageOverviewSnapshotScopes])
  const scopes = rows
    .map((row) => ({ systemAccountId: row.system_account_id ?? '', scopeId: row.scope_id ?? '' }))
    .filter((row) => row.systemAccountId && row.scopeId)
  if (!scopes.some((scope) => scope.systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID && scope.scopeId === GLOBAL_STATS_SCOPE_ID)) {
    scopes.push({ systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeId: GLOBAL_STATS_SCOPE_ID })
  }
  return scopes
}

export async function refreshUsageOverviewWindowSnapshotsAsync(client: DatabaseClient, context: UsageOverviewWindowRefreshContext, options: UsageOverviewWindowRefreshOptions = {}): Promise<void> {
  await refreshUsageOverviewSummaryWindowSnapshotsAsync(client, context, options)
  await refreshUsageOverviewTrendWindowSnapshotsAsync(client, context, options)
  await refreshUsageModelRankWindowSnapshotsAsync(client, context, options)
  await refreshUsageErrorRankWindowSnapshotsAsync(client, context, options)
}

export async function refreshUsageOverviewWindowSnapshotsInStages(
  database: DatabaseSync,
  context: UsageOverviewWindowRefreshContext,
  yieldToEventLoop: () => Promise<void>,
  previousSourceWatermark?: string,
  sourceWatermark?: string
): Promise<void> {
  const refresh = usageOverviewIncrementalWindowRefresh(database, context, previousSourceWatermark, sourceWatermark)
  if (!refresh) return
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    refreshUsageOverviewWindowSnapshots(database, refresh.context, refresh.options)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  await yieldToEventLoop()
}

export async function refreshUsageOverviewWindowSnapshotsIncrementalAsync(
  client: DatabaseClient,
  context: UsageOverviewWindowRefreshContext,
  previousSourceWatermark?: string,
  sourceWatermark?: string
): Promise<void> {
  const refresh = await usageOverviewIncrementalWindowRefreshAsync(client, context, previousSourceWatermark, sourceWatermark)
  if (!refresh) return
  await refreshUsageOverviewWindowSnapshotsAsync(client, refresh.context, refresh.options)
}

export function refreshUsageOverviewTodayWindowSnapshots(database: DatabaseSync, context: UsageOverviewWindowRefreshContext): void {
  const todayContext = usageOverviewTodayWindowRefreshContext(context)
  if (!todayContext) return
  refreshUsageOverviewSummaryWindowSnapshots(database, todayContext, { endDate: todayContext.todayKey })
  refreshUsageOverviewTrendWindowSnapshots(database, todayContext, { endDate: todayContext.todayKey })
  refreshUsageModelRankWindowSnapshots(database, todayContext, { endDate: todayContext.todayKey })
  refreshUsageErrorRankWindowSnapshots(database, todayContext, { endDate: todayContext.todayKey })
}

export async function refreshUsageOverviewTodayWindowSnapshotsAsync(client: DatabaseClient, context: UsageOverviewWindowRefreshContext): Promise<void> {
  const todayContext = usageOverviewTodayWindowRefreshContext(context)
  if (!todayContext) return
  await refreshUsageOverviewSummaryWindowSnapshotsAsync(client, todayContext, { endDate: todayContext.todayKey })
  await refreshUsageOverviewTrendWindowSnapshotsAsync(client, todayContext, { endDate: todayContext.todayKey })
  await refreshUsageModelRankWindowSnapshotsAsync(client, todayContext, { endDate: todayContext.todayKey })
  await refreshUsageErrorRankWindowSnapshotsAsync(client, todayContext, { endDate: todayContext.todayKey })
}

function usageOverviewTodayWindowRefreshContext(context: UsageOverviewWindowRefreshContext): UsageOverviewWindowRefreshContext | undefined {
  const ranges = context.ranges.filter((range) => range.endDate === context.todayKey)
  if (!ranges.length) return undefined
  return {
    ...context,
    ranges,
    earliestDate: ranges[0]?.startDate ?? context.todayKey
  }
}

function usageOverviewIncrementalWindowRefresh(
  database: DatabaseSync,
  context: UsageOverviewWindowRefreshContext,
  previousSourceWatermark?: string,
  sourceWatermark?: string
): UsageOverviewIncrementalWindowRefresh | undefined {
  const changedDate = usageOverviewChangedDate(database, context, previousSourceWatermark, sourceWatermark)
  return usageOverviewRefreshForChangedDate(context, changedDate)
}

async function usageOverviewIncrementalWindowRefreshAsync(
  client: DatabaseClient,
  context: UsageOverviewWindowRefreshContext,
  previousSourceWatermark?: string,
  sourceWatermark?: string
): Promise<UsageOverviewIncrementalWindowRefresh | undefined> {
  const changedDate = await usageOverviewChangedDateAsync(client, context, previousSourceWatermark, sourceWatermark)
  return usageOverviewRefreshForChangedDate(context, changedDate)
}

function usageOverviewRefreshForChangedDate(
  context: UsageOverviewWindowRefreshContext,
  changedDate: string | null | undefined
): UsageOverviewIncrementalWindowRefresh | undefined {
  if (changedDate === undefined) return undefined
  if (changedDate === null) {
    return { context, options: {} }
  }
  const minEndDate = context.ranges.find((range) => range.endDate >= changedDate)?.endDate
  if (!minEndDate) return undefined
  const ranges = context.ranges.filter((range) => range.endDate >= minEndDate)
  if (!ranges.length) return undefined
  return {
    context: {
      ...context,
      ranges,
      earliestDate: ranges.reduce((earliest, range) => range.startDate < earliest ? range.startDate : earliest, ranges[0]?.startDate ?? context.earliestDate)
    },
    options: { minEndDate }
  }
}

function usageOverviewChangedDate(
  database: DatabaseSync,
  context: UsageOverviewWindowRefreshContext,
  previousSourceWatermark?: string,
  sourceWatermark?: string
): string | null | undefined {
  if (!previousSourceWatermark) return null
  const previousUpdatedAt = overviewWindowSourceWatermarkUpdatedAt(previousSourceWatermark)
  const sourceUpdatedAt = overviewWindowSourceWatermarkUpdatedAt(sourceWatermark)
  if (!previousUpdatedAt) return null
  if (sourceUpdatedAt && sourceUpdatedAt < previousUpdatedAt) return null
  const row = database.prepare(`
    SELECT MIN(stat_date) AS stat_date
    FROM (
      SELECT stat_date
      FROM usage_stats_daily
      WHERE updated_at > ?
        AND stat_date >= ?
        AND stat_date <= ?
      UNION ALL
      SELECT substr(stat_hour, 1, 10) AS stat_date
      FROM usage_stats_hourly
      WHERE updated_at > ?
        AND stat_hour >= ?
        AND stat_hour <= ?
      UNION ALL
      SELECT stat_date
      FROM usage_model_daily
      WHERE updated_at > ?
        AND stat_date >= ?
        AND stat_date <= ?
      UNION ALL
      SELECT stat_date
      FROM usage_error_daily
      WHERE updated_at > ?
        AND stat_date >= ?
        AND stat_date <= ?
    ) changed_dates
  `).get(
    previousUpdatedAt,
    context.earliestDate,
    context.todayKey,
    previousUpdatedAt,
    `${context.earliestDate}T00`,
    `${context.todayKey}T23`,
    previousUpdatedAt,
    context.earliestDate,
    context.todayKey,
    previousUpdatedAt,
    context.earliestDate,
    context.todayKey
  ) as { stat_date?: string | null } | undefined
  const changedDate = row?.stat_date
  if (!changedDate && sourceWatermark !== previousSourceWatermark) return null
  return changedDate ?? undefined
}

async function usageOverviewChangedDateAsync(
  client: DatabaseClient,
  context: UsageOverviewWindowRefreshContext,
  previousSourceWatermark?: string,
  sourceWatermark?: string
): Promise<string | null | undefined> {
  if (!previousSourceWatermark) return null
  const previousUpdatedAt = overviewWindowSourceWatermarkUpdatedAt(previousSourceWatermark)
  const sourceUpdatedAt = overviewWindowSourceWatermarkUpdatedAt(sourceWatermark)
  if (!previousUpdatedAt) return null
  if (sourceUpdatedAt && sourceUpdatedAt < previousUpdatedAt) return null
  const row = await client.one<{ stat_date?: string | null }>(`
    SELECT MIN(stat_date) AS stat_date
    FROM (
      SELECT stat_date
      FROM ${statsTable(client, 'usage_stats_daily')}
      WHERE updated_at > ?
        AND stat_date >= ?
        AND stat_date <= ?
      UNION ALL
      SELECT substr(stat_hour, 1, 10) AS stat_date
      FROM ${statsTable(client, 'usage_stats_hourly')}
      WHERE updated_at > ?
        AND stat_hour >= ?
        AND stat_hour <= ?
      UNION ALL
      SELECT stat_date
      FROM ${statsTable(client, 'usage_model_daily')}
      WHERE updated_at > ?
        AND stat_date >= ?
        AND stat_date <= ?
      UNION ALL
      SELECT stat_date
      FROM ${statsTable(client, 'usage_error_daily')}
      WHERE updated_at > ?
        AND stat_date >= ?
        AND stat_date <= ?
    ) changed_dates
  `, [
    previousUpdatedAt,
    context.earliestDate,
    context.todayKey,
    previousUpdatedAt,
    `${context.earliestDate}T00`,
    `${context.todayKey}T23`,
    previousUpdatedAt,
    context.earliestDate,
    context.todayKey,
    previousUpdatedAt,
    context.earliestDate,
    context.todayKey
  ])
  const changedDate = row?.stat_date
  if (!changedDate && sourceWatermark !== previousSourceWatermark) return null
  return changedDate ?? undefined
}

function overviewWindowSourceWatermarkUpdatedAt(watermark?: string): string | undefined {
  if (!watermark) return undefined
  const [updatedAt] = watermark.split('|', 1)
  return updatedAt || undefined
}

function refreshUsageOverviewSummaryWindowSnapshots(database: DatabaseSync, context: UsageOverviewWindowRefreshContext, options: UsageOverviewWindowRefreshOptions = {}): void {
  deleteUsageOverviewWindowRows(database, 'usage_overview_summary_windows', options)
  for (const scope of context.overviewScopes) {
    refreshUsageOverviewSummaryWindows(database, scope, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
  }
}

function refreshUsageOverviewTrendWindowSnapshots(database: DatabaseSync, context: UsageOverviewWindowRefreshContext, options: UsageOverviewWindowRefreshOptions = {}): void {
  deleteUsageOverviewWindowRows(database, 'usage_overview_trend_windows', options)
  for (const scope of context.overviewScopes) {
    refreshUsageOverviewTrendWindows(database, scope, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
  }
}

function refreshUsageModelRankWindowSnapshots(database: DatabaseSync, context: UsageOverviewWindowRefreshContext, options: UsageOverviewWindowRefreshOptions = {}): void {
  deleteUsageOverviewWindowRows(database, 'usage_model_rank_windows', options)
  for (const systemAccountId of [...context.uniqueSystemAccountIds, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    refreshUsageModelRankWindows(database, systemAccountId, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
  }
}

function refreshUsageErrorRankWindowSnapshots(database: DatabaseSync, context: UsageOverviewWindowRefreshContext, options: UsageOverviewWindowRefreshOptions = {}): void {
  deleteUsageOverviewWindowRows(database, 'usage_error_rank_windows', options)
  for (const systemAccountId of [...context.uniqueSystemAccountIds, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    refreshUsageErrorRankWindows(database, systemAccountId, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
  }
}

function deleteUsageOverviewWindowRows(database: DatabaseSync, tableName: string, options: UsageOverviewWindowRefreshOptions): void {
  if (options.endDate) {
    database.prepare(`DELETE FROM ${tableName} WHERE end_date = ?`).run(options.endDate)
    return
  }
  if (options.minEndDate) {
    database.prepare(`DELETE FROM ${tableName} WHERE end_date >= ?`).run(options.minEndDate)
    return
  }
  database.prepare(`DELETE FROM ${tableName}`).run()
}

async function deleteUsageOverviewWindowRowsAsync(client: DatabaseClient, tableName: string, options: UsageOverviewWindowRefreshOptions): Promise<void> {
  if (options.endDate) {
    await client.execute(`DELETE FROM ${statsTable(client, tableName)} WHERE end_date = ?`, [options.endDate])
    return
  }
  if (options.minEndDate) {
    await client.execute(`DELETE FROM ${statsTable(client, tableName)} WHERE end_date >= ?`, [options.minEndDate])
    return
  }
  await client.execute(`DELETE FROM ${statsTable(client, tableName)}`)
}

async function refreshUsageOverviewSummaryWindowSnapshotsAsync(client: DatabaseClient, context: UsageOverviewWindowRefreshContext, options: UsageOverviewWindowRefreshOptions = {}): Promise<void> {
  await deleteUsageOverviewWindowRowsAsync(client, 'usage_overview_summary_windows', options)
  const insertRows: unknown[][] = []
  for (const scope of context.overviewScopes) {
    const rows = await client.query<UsageStatsDailyWindowRow>(`
      SELECT stat_date, request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
        cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
        thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
        first_token_ms_sum, first_token_ms_count, first_token_ms_max, last_used_at
      FROM ${statsTable(client, 'usage_stats_daily')}
      WHERE system_account_id = ?
        AND scope_type = 'system_account'
        AND scope_id = ?
        AND stat_date >= ?
        AND stat_date <= ?
      ORDER BY stat_date ASC
    `, [scope.systemAccountId, scope.scopeId, context.earliestDate, context.todayKey])
    const rowsByDate = rowsByStatDate(rows)
    for (const range of context.ranges) {
      const aggregate = aggregateUsageRowsForRange(rowsByDate, range)
      insertRows.push([
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
        aggregate.cacheWriteTokens,
        aggregate.cacheWrite1hTokens,
        aggregate.cacheWriteCostUsd,
        aggregate.thinkingTokens,
        aggregate.inputImageTokens,
        aggregate.outputImageTokens,
        aggregate.totalCostUsd,
        aggregate.durationMsSum,
        aggregate.durationMsCount,
        aggregate.firstTokenMsSum,
        aggregate.firstTokenMsCount,
        aggregate.lastUsedAt ?? null,
        context.updatedAt
      ])
    }
  }
  await insertRowsAsync(client, 'usage_overview_summary_windows', [
    'system_account_id', 'window_key', 'start_date', 'end_date', 'request_count', 'success_count', 'error_count',
    'input_tokens', 'output_tokens', 'cache_read_tokens', 'cache_read_cost_usd', 'cache_write_tokens', 'cache_write_1h_tokens', 'cache_write_cost_usd',
    'thinking_tokens', 'input_image_tokens', 'output_image_tokens', 'total_cost_usd',
    'duration_ms_sum', 'duration_ms_count', 'first_token_ms_sum', 'first_token_ms_count',
    'last_used_at', 'updated_at'
  ], insertRows)
}

async function refreshUsageOverviewTrendWindowSnapshotsAsync(client: DatabaseClient, context: UsageOverviewWindowRefreshContext, options: UsageOverviewWindowRefreshOptions = {}): Promise<void> {
  await deleteUsageOverviewWindowRowsAsync(client, 'usage_overview_trend_windows', options)
  const insertRows: unknown[][] = []
  for (const scope of context.overviewScopes) {
    const rows = await client.query<UsageOverviewHourlyWindowRow>(`
      SELECT stat_hour, request_count, error_count, input_tokens, output_tokens, cache_read_tokens,
        cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
        thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd, duration_ms_sum, duration_ms_count
      FROM ${statsTable(client, 'usage_stats_hourly')}
      WHERE system_account_id = ?
        AND scope_type = 'system_account'
        AND scope_id = ?
        AND stat_hour >= ?
        AND stat_hour <= ?
      ORDER BY stat_hour ASC
    `, [scope.systemAccountId, scope.scopeId, `${context.earliestDate}T00`, `${context.todayKey}T23`])
    const rowsByDate = rowsByStatHourDate(rows)
    for (const range of context.ranges) {
      const buckets = aggregateUsageTrendBuckets(rowsByDate, range)
      for (const [bucketKey, bucket] of sortedMapEntries(buckets)) {
        insertRows.push([
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
          bucket.cacheWriteTokens,
          bucket.cacheWrite1hTokens,
          bucket.cacheWriteCostUsd,
          bucket.thinkingTokens,
          bucket.inputImageTokens,
          bucket.outputImageTokens,
          bucket.totalCostUsd,
          bucket.durationMsSum,
          bucket.durationMsCount,
          context.updatedAt
        ])
      }
    }
  }
  await insertRowsAsync(client, 'usage_overview_trend_windows', [
    'system_account_id', 'window_key', 'start_date', 'end_date', 'bucket_key', 'request_count', 'error_count',
    'input_tokens', 'output_tokens', 'cache_read_tokens', 'cache_read_cost_usd', 'cache_write_tokens', 'cache_write_1h_tokens', 'cache_write_cost_usd',
    'thinking_tokens', 'input_image_tokens', 'output_image_tokens', 'total_cost_usd',
    'duration_ms_sum', 'duration_ms_count', 'updated_at'
  ], insertRows)
}

async function refreshUsageModelRankWindowSnapshotsAsync(client: DatabaseClient, context: UsageOverviewWindowRefreshContext, options: UsageOverviewWindowRefreshOptions = {}): Promise<void> {
  await deleteUsageOverviewWindowRowsAsync(client, 'usage_model_rank_windows', options)
  const insertRows: unknown[][] = []
  for (const systemAccountId of [...context.uniqueSystemAccountIds, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    const rows = await client.query<UsageModelWindowRow>(`
      SELECT stat_date, provider_code, model, request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
        cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd
      FROM ${statsTable(client, 'usage_model_daily')}
      WHERE system_account_id = ?
        AND stat_date >= ?
        AND stat_date <= ?
      ORDER BY stat_date ASC
    `, [systemAccountId, context.earliestDate, context.todayKey])
    const rowsByDate = rowsByStatDate(rows)
    for (const range of context.ranges) {
      const rankedRows = aggregateUsageModelRows(rowsByDate, range)
      rankedRows.slice(0, 10).forEach((row, index) => {
        insertRows.push([
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
          row.cacheWriteTokens,
          row.cacheWrite1hTokens,
          row.cacheWriteCostUsd,
          row.thinkingTokens,
          row.inputImageTokens,
          row.outputImageTokens,
          row.totalCostUsd,
          context.updatedAt
        ])
      })
    }
  }
  await insertRowsAsync(client, 'usage_model_rank_windows', [
    'system_account_id', 'window_key', 'start_date', 'end_date', 'rank', 'provider_code', 'model',
    'request_count', 'input_tokens', 'output_tokens', 'cache_read_tokens', 'cache_read_cost_usd', 'cache_write_tokens', 'cache_write_1h_tokens', 'cache_write_cost_usd',
    'thinking_tokens', 'input_image_tokens', 'output_image_tokens', 'total_cost_usd', 'updated_at'
  ], insertRows)
}

async function refreshUsageErrorRankWindowSnapshotsAsync(client: DatabaseClient, context: UsageOverviewWindowRefreshContext, options: UsageOverviewWindowRefreshOptions = {}): Promise<void> {
  await deleteUsageOverviewWindowRowsAsync(client, 'usage_error_rank_windows', options)
  const insertRows: unknown[][] = []
  for (const systemAccountId of [...context.uniqueSystemAccountIds, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    const rows = await client.query<UsageErrorWindowRow>(`
      SELECT stat_date, error_group, provider_code, error_code, status_code, error_message, error_count
      FROM ${statsTable(client, 'usage_error_daily')}
      WHERE system_account_id = ?
        AND stat_date >= ?
        AND stat_date <= ?
      ORDER BY stat_date ASC
    `, [systemAccountId, context.earliestDate, context.todayKey])
    const rowsByDate = rowsByStatDate(rows)
    for (const range of context.ranges) {
      const rankedRows = aggregateUsageErrorRows(rowsByDate, range)
      rankedRows.slice(0, 10).forEach((row, index) => {
        insertRows.push([
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
          context.updatedAt
        ])
      })
    }
  }
  await insertRowsAsync(client, 'usage_error_rank_windows', [
    'system_account_id', 'window_key', 'start_date', 'end_date', 'rank', 'provider_code', 'error_code',
    'status_code', 'error_message', 'error_count', 'updated_at'
  ], insertRows)
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
      cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
      thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
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
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
      thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
      duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count,
      last_used_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
      aggregate.cacheWriteTokens,
      aggregate.cacheWrite1hTokens,
      aggregate.cacheWriteCostUsd,
      aggregate.thinkingTokens,
      aggregate.inputImageTokens,
      aggregate.outputImageTokens,
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
      cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
      thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd, duration_ms_sum, duration_ms_count
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
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
      thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
      duration_ms_sum, duration_ms_count, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
        bucket.cacheWriteTokens,
        bucket.cacheWrite1hTokens,
        bucket.cacheWriteCostUsd,
        bucket.thinkingTokens,
        bucket.inputImageTokens,
        bucket.outputImageTokens,
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
    SELECT stat_date, provider_code, model, request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
      cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd
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
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
      thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
        row.cacheWriteTokens,
        row.cacheWrite1hTokens,
        row.cacheWriteCostUsd,
        row.thinkingTokens,
        row.inputImageTokens,
        row.outputImageTokens,
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
