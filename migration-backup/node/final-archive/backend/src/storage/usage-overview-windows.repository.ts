import type { DatabaseSync } from 'node:sqlite'

import type { AccountUsageStatsRange } from '../domain/types.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, rollbackDatabaseTransaction } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { chunkValues } from './query-utils.js'
import { runDerivedWindowRolloverSeedPages } from './usage-derived-window-rollover.js'
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
  sortedMapEntries,
  trendBucketHours
} from './usage-stats-window-helpers.js'
import {
  GLOBAL_STATS_SCOPE_ID,
  GLOBAL_STATS_SYSTEM_ACCOUNT_ID
} from './usage-stats-types.js'

const maxUsageOverviewSnapshotScopes = 5000
const usageOverviewRolloverRowsPerScope = 1185
const usageOverviewRolloverSnapshotRowBudget = 33_180
// 28 scopes * 288 dedicated runs/day = 8064 rollover scopes/day.
// Batched source reads keep this budget to at most 33,180 published rows and
// about 162 repository SQL statements even when all eight seed pages are used.
const usageOverviewDirtyClaimLimit = Math.floor(usageOverviewRolloverSnapshotRowBudget / usageOverviewRolloverRowsPerScope)
const usageOverviewDirtyCandidateLimit = usageOverviewDirtyClaimLimit * 4

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
  systemAccountIds?: string[]
  minEndDateBySystemAccountId?: Map<string, string>
  rangesBySystemAccountId?: Map<string, AccountUsageStatsRange[]>
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
  await seedUsageOverviewRolloverDirtyScopesAsync(client, context)
  const dirtyCandidates = await client.query<{
    system_account_id: string
    scope_id: string
    min_changed_date: string
    generation: number | string
  }>(`
    SELECT system_account_id, scope_id, min_changed_date, generation
    FROM ${statsTable(client, 'usage_overview_dirty_scopes')}
    ORDER BY first_dirty_at, system_account_id
    LIMIT ${usageOverviewDirtyCandidateLimit}
    FOR UPDATE SKIP LOCKED
  `)
  if (dirtyCandidates.length === 0) return
  const dirtyWork: Array<{ dirty: typeof dirtyCandidates[number]; ranges: AccountUsageStatsRange[] }> = []
  let snapshotRowCount = 0
  for (const dirty of dirtyCandidates) {
    const ranges = context.ranges.filter((range) => range.endDate >= dirty.min_changed_date)
    const estimatedRows = ranges.reduce((total, range) => total + estimatedUsageOverviewSnapshotRows(range), 0)
    if (dirtyWork.length > 0 && snapshotRowCount + estimatedRows > usageOverviewRolloverSnapshotRowBudget) break
    dirtyWork.push({ dirty, ranges })
    snapshotRowCount += estimatedRows
  }
  const dirtyRows = dirtyWork.map((work) => work.dirty)
  const systemAccountIds = dirtyRows.map((row) => row.system_account_id)
  const rangesBySystemAccountId = new Map(dirtyWork.map((work) => [work.dirty.system_account_id, work.ranges]))
  const refreshContext: UsageOverviewWindowRefreshContext = {
    ...context,
    earliestDate: dirtyWork.reduce((earliestDate, work) => {
      const rangeStartDate = work.ranges[0]?.startDate
      return rangeStartDate && rangeStartDate < earliestDate ? rangeStartDate : earliestDate
    }, context.todayKey),
    overviewScopes: dirtyRows.map((row) => ({ systemAccountId: row.system_account_id, scopeId: row.scope_id })),
    uniqueSystemAccountIds: systemAccountIds.filter((id) => id !== GLOBAL_STATS_SYSTEM_ACCOUNT_ID)
  }
  await refreshUsageOverviewWindowSnapshotsAsync(client, refreshContext, {
    systemAccountIds,
    minEndDateBySystemAccountId: new Map(dirtyRows.map((row) => [row.system_account_id, row.min_changed_date])),
    rangesBySystemAccountId
  })
  const claimedValues = dirtyRows.map(() => '(?, ?::bigint)').join(', ')
  await client.execute(`
    DELETE FROM ${statsTable(client, 'usage_overview_dirty_scopes')} dirty
    USING (VALUES ${claimedValues}) AS claimed(system_account_id, generation)
    WHERE dirty.system_account_id = claimed.system_account_id
      AND dirty.generation = claimed.generation
  `, dirtyRows.flatMap((row) => [row.system_account_id, row.generation]))
}

async function seedUsageOverviewRolloverDirtyScopesAsync(
  client: DatabaseClient,
  context: UsageOverviewWindowRefreshContext
): Promise<void> {
  const stateTable = statsTable(client, 'stats_job_state')
  const state = await client.one<{ cursor_created_at?: string | null; cursor_id?: string | null }>(`
    SELECT cursor_created_at, cursor_id FROM ${stateTable}
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_overview_daily_seed'
  `)
  const startsNewDay = state?.cursor_created_at !== context.todayKey
  const cursor = startsNewDay ? '' : state.cursor_id ?? ''
  if (cursor === '__done__') return

  const seedScopes = async (scopes: Array<{ system_account_id: string; scope_id: string }>): Promise<void> => {
    await client.execute(`
      INSERT INTO ${statsTable(client, 'usage_overview_dirty_scopes')} (
        system_account_id, scope_id, min_changed_date, generation, first_dirty_at, updated_at
      ) VALUES ${scopes.map(() => '(?, ?, ?, 1, ?, ?)').join(', ')}
      ON CONFLICT(system_account_id) DO UPDATE SET
        scope_id = EXCLUDED.scope_id,
        min_changed_date = LEAST(usage_overview_dirty_scopes.min_changed_date, EXCLUDED.min_changed_date),
        generation = usage_overview_dirty_scopes.generation + 1,
        updated_at = EXCLUDED.updated_at
    `, scopes.flatMap((scope) => [scope.system_account_id, scope.scope_id, context.todayKey, context.updatedAt, context.updatedAt]))
  }

  if (startsNewDay) {
    await seedScopes([{ system_account_id: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scope_id: GLOBAL_STATS_SCOPE_ID }])
  }
  const progress = await runDerivedWindowRolloverSeedPages({
    cursor,
    loadPage: (pageCursor, pageSize) => client.query<{ system_account_id: string; scope_id: string }>(`
      SELECT system_account_id, scope_id
      FROM ${statsTable(client, 'usage_stats_totals')}
      WHERE scope_type = 'system_account'
        AND system_account_id <> ?
        AND system_account_id > ?
      ORDER BY system_account_id
      LIMIT ?
    `, [GLOBAL_STATS_SYSTEM_ACCOUNT_ID, pageCursor, pageSize]),
    seedPage: seedScopes
  })
  await client.execute(`
    INSERT INTO ${stateTable} (
      scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, updated_at
    ) VALUES ('global', '', 'usage_overview_daily_seed', ?, ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = EXCLUDED.cursor_created_at,
      cursor_id = EXCLUDED.cursor_id,
      last_success_at = EXCLUDED.last_success_at,
      updated_at = EXCLUDED.updated_at
  `, [context.todayKey, progress.nextCursor, context.updatedAt, context.updatedAt])
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
  if (options.minEndDateBySystemAccountId?.size) {
    const values = [...options.minEndDateBySystemAccountId]
    const placeholders = values.map(() => '(?, ?)').join(', ')
    await client.execute(`
      DELETE FROM ${statsTable(client, tableName)} target
      USING (VALUES ${placeholders}) AS dirty(system_account_id, min_end_date)
      WHERE target.system_account_id = dirty.system_account_id
        AND target.end_date >= dirty.min_end_date
    `, values.flat())
    return
  }
  const conditions: string[] = []
  const params: unknown[] = []
  if (options.systemAccountIds?.length) {
    conditions.push('system_account_id = ANY(?::text[])')
    params.push(options.systemAccountIds)
  }
  if (options.endDate) {
    conditions.push('end_date = ?')
    params.push(options.endDate)
  } else if (options.minEndDate) {
    conditions.push('end_date >= ?')
    params.push(options.minEndDate)
  }
  await client.execute(`DELETE FROM ${statsTable(client, tableName)}${conditions.length ? ` WHERE ${conditions.join(' AND ')}` : ''}`, params)
}

async function refreshUsageOverviewSummaryWindowSnapshotsAsync(client: DatabaseClient, context: UsageOverviewWindowRefreshContext, options: UsageOverviewWindowRefreshOptions = {}): Promise<void> {
  await deleteUsageOverviewWindowRowsAsync(client, 'usage_overview_summary_windows', options)
  const insertRows: unknown[][] = []
  const selectedScopes = context.overviewScopes
  const selectedScopeValues = selectedScopes.map(() => '(?, ?)').join(', ')
  const sourceRows = selectedScopes.length === 0 ? [] : await client.query<UsageStatsDailyWindowRow & { system_account_id: string; scope_id: string }>(`
    WITH selected_scopes(system_account_id, scope_id) AS (VALUES ${selectedScopeValues})
    SELECT daily.system_account_id, daily.scope_id, daily.stat_date,
      daily.request_count, daily.success_count, daily.error_count, daily.input_tokens, daily.output_tokens,
      daily.cache_read_tokens, daily.cache_read_cost_usd, daily.cache_write_tokens, daily.cache_write_1h_tokens,
      daily.cache_write_cost_usd, daily.thinking_tokens, daily.input_image_tokens, daily.output_image_tokens,
      daily.total_cost_usd, daily.duration_ms_sum, daily.duration_ms_count, daily.duration_ms_max,
      daily.first_token_ms_sum, daily.first_token_ms_count, daily.first_token_ms_max, daily.last_used_at
    FROM ${statsTable(client, 'usage_stats_daily')} daily
    INNER JOIN selected_scopes selected
      ON selected.system_account_id = daily.system_account_id
      AND selected.scope_id = daily.scope_id
    WHERE daily.scope_type = 'system_account'
      AND daily.stat_date >= ?
      AND daily.stat_date <= ?
    ORDER BY daily.system_account_id, daily.scope_id, daily.stat_date
  `, [...selectedScopes.flatMap((scope) => [scope.systemAccountId, scope.scopeId]), context.earliestDate, context.todayKey])
  const sourceRowsByScope = groupRowsByKey(sourceRows, (row) => overviewScopeKey(row.system_account_id, row.scope_id))
  for (const scope of context.overviewScopes) {
    const rows = sourceRowsByScope.get(overviewScopeKey(scope.systemAccountId, scope.scopeId)) ?? []
    const rowsByDate = rowsByStatDate(rows)
    const ranges = options.rangesBySystemAccountId?.get(scope.systemAccountId) ?? context.ranges
    for (const range of ranges) {
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
  const selectedScopes = context.overviewScopes
  const selectedScopeValues = selectedScopes.map(() => '(?, ?)').join(', ')
  const sourceRows = selectedScopes.length === 0 ? [] : await client.query<UsageOverviewHourlyWindowRow & { system_account_id: string; scope_id: string }>(`
    WITH selected_scopes(system_account_id, scope_id) AS (VALUES ${selectedScopeValues})
    SELECT hourly.system_account_id, hourly.scope_id, hourly.stat_hour,
      hourly.request_count, hourly.error_count, hourly.input_tokens, hourly.output_tokens,
      hourly.cache_read_tokens, hourly.cache_read_cost_usd, hourly.cache_write_tokens,
      hourly.cache_write_1h_tokens, hourly.cache_write_cost_usd, hourly.thinking_tokens,
      hourly.input_image_tokens, hourly.output_image_tokens, hourly.total_cost_usd,
      hourly.duration_ms_sum, hourly.duration_ms_count
    FROM ${statsTable(client, 'usage_stats_hourly')} hourly
    INNER JOIN selected_scopes selected
      ON selected.system_account_id = hourly.system_account_id
      AND selected.scope_id = hourly.scope_id
    WHERE hourly.scope_type = 'system_account'
      AND hourly.stat_hour >= ?
      AND hourly.stat_hour <= ?
    ORDER BY hourly.system_account_id, hourly.scope_id, hourly.stat_hour
  `, [...selectedScopes.flatMap((scope) => [scope.systemAccountId, scope.scopeId]), `${context.earliestDate}T00`, `${context.todayKey}T23`])
  const sourceRowsByScope = groupRowsByKey(sourceRows, (row) => overviewScopeKey(row.system_account_id, row.scope_id))
  for (const scope of context.overviewScopes) {
    const rows = sourceRowsByScope.get(overviewScopeKey(scope.systemAccountId, scope.scopeId)) ?? []
    const rowsByDate = rowsByStatHourDate(rows)
    const ranges = options.rangesBySystemAccountId?.get(scope.systemAccountId) ?? context.ranges
    for (const range of ranges) {
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
  const systemAccountIds = options.systemAccountIds ?? [...context.uniqueSystemAccountIds, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]
  const sourceRows = systemAccountIds.length === 0 ? [] : await client.query<UsageModelWindowRow & { system_account_id: string }>(`
    SELECT system_account_id, stat_date, provider_code, model, request_count, input_tokens, output_tokens,
      cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens,
      cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd
    FROM ${statsTable(client, 'usage_model_daily')}
    WHERE system_account_id = ANY(?::text[])
      AND stat_date >= ?
      AND stat_date <= ?
    ORDER BY system_account_id, stat_date
  `, [systemAccountIds, context.earliestDate, context.todayKey])
  const sourceRowsBySystemAccount = groupRowsByKey(sourceRows, (row) => row.system_account_id)
  for (const systemAccountId of systemAccountIds) {
    const rows = sourceRowsBySystemAccount.get(systemAccountId) ?? []
    const rowsByDate = rowsByStatDate(rows)
    const ranges = options.rangesBySystemAccountId?.get(systemAccountId) ?? context.ranges
    for (const range of ranges) {
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
  const systemAccountIds = options.systemAccountIds ?? [...context.uniqueSystemAccountIds, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]
  const sourceRows = systemAccountIds.length === 0 ? [] : await client.query<UsageErrorWindowRow & { system_account_id: string }>(`
    SELECT system_account_id, stat_date, error_group, provider_code, error_code, status_code, error_message, error_count
    FROM ${statsTable(client, 'usage_error_daily')}
    WHERE system_account_id = ANY(?::text[])
      AND stat_date >= ?
      AND stat_date <= ?
    ORDER BY system_account_id, stat_date
  `, [systemAccountIds, context.earliestDate, context.todayKey])
  const sourceRowsBySystemAccount = groupRowsByKey(sourceRows, (row) => row.system_account_id)
  for (const systemAccountId of systemAccountIds) {
    const rows = sourceRowsBySystemAccount.get(systemAccountId) ?? []
    const rowsByDate = rowsByStatDate(rows)
    const ranges = options.rangesBySystemAccountId?.get(systemAccountId) ?? context.ranges
    for (const range of ranges) {
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

function overviewScopeKey(systemAccountId: string, scopeId: string): string {
  return `${systemAccountId}\u0000${scopeId}`
}

function groupRowsByKey<T>(rows: T[], keyOf: (row: T) => string): Map<string, T[]> {
  const grouped = new Map<string, T[]>()
  for (const row of rows) {
    const key = keyOf(row)
    const values = grouped.get(key) ?? []
    values.push(row)
    grouped.set(key, values)
  }
  return grouped
}

function estimatedUsageOverviewSnapshotRows(range: AccountUsageStatsRange): number {
  const trendRows = Math.ceil(range.days * 24 / trendBucketHours(range))
  return 1 + trendRows + 10 + 10
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
