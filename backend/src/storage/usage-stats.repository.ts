import type { DatabaseSync } from 'node:sqlite'

import type {
  AccountUsageStatsRange,
} from '../domain/types.js'
import { canAccessAll, currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { beginDatabaseTransaction, beginImmediateDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, getStatsDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { refreshSystemMetricsTrendWindowSnapshotsStage } from './system-metrics.repository.js'
import { getUsageRecordShardDatabase, listUsageRecordShardLocationsPage, type UsageRecordShardLocation } from './usage-record-shards.js'
import { averageFromSum, dateKey, usageStatsTimezone } from './usage-stats-helpers.js'
import { emptyStatsAggregateMathRow, usageSummaryWithMath } from './usage-stats-mappers.js'
import { refreshUsageOverviewWindowSnapshots, usageOverviewSnapshotScopes } from './usage-overview-windows.repository.js'
import {
  refreshAuthorizationUsageRangeWindowSnapshots,
  refreshAuthorizationUsageRangeWindowSnapshotsInStages,
  refreshUsageScopeRangeWindowSnapshots,
  refreshUsageScopeRangeWindowSnapshotsInStages
} from './usage-range-windows.repository.js'
import { latestUsageStatsLagSeconds, normalizeDefaultUsageStatsRange } from './usage-stats-runtime-helpers.js'
import {
  refreshAccountLast7dRequestRankSnapshot,
  refreshApiKeyCurrentMonthCostRankSnapshot,
  refreshAuthorizationCurrentMonthCostRankSnapshot,
  refreshCallerAccountLast7dRequestRankSnapshot,
  refreshUsageQuotaHourlyWindowSnapshots
} from './usage-stats-snapshot-helpers.js'
import { aggregateUsageStatsRecord, createUsageStatsAggregationContext, extendUsageStatsAggregationContext } from './usage-stats-writers.js'
import {
  aggregateUsageRowsForRange,
  type UsageStatsDailyWindowRow
} from './usage-stats-window-aggregates.js'
import {
  fixedUsageStatsRanges,
  nextDateKey,
  rangeWindowKey,
  rowsByStatDate
} from './usage-stats-window-helpers.js'
import {
  GLOBAL_STATS_SCOPE_ID,
  GLOBAL_STATS_SYSTEM_ACCOUNT_ID,
  USAGE_STATS_RECORD_SELECT_COLUMNS,
  type AccountUsageAggregateRow,
  type StatsAggregateMathRow,
  type StatsJobStateRow,
  type UsageStatsOverview,
  type UsageStatsRecordRow
} from './usage-stats-types.js'

export type { ProcessEventLoopSampleInput, SystemMetricsOverview, SystemMetricsSampleInput, UsageStatsOverview } from './usage-stats-types.js'
export { getAiPerformanceOverview, listAiPerformanceAccountOptions } from './usage-stats-ai-performance.repository.js'
export { getSystemMetricsOverview, insertProcessEventLoopSample, insertSystemMetricsSample } from './system-metrics.repository.js'
export {
  GROUP_ACCOUNT_STATS_DIRTY_ALL,
  markAllGroupAccountStatsDirty,
  markGroupAccountStatsDirty,
  markGroupAccountStatsDirtyByAccountIds,
  refreshDirtyGroupAccountStatsCache,
  refreshDirtyGroupAccountStatsCacheWithWriter,
  refreshGroupAccountStatsCache
} from './group-account-stats-cache.repository.js'
export { latestUsageStatsLagSeconds, normalizeDefaultUsageStatsRange } from './usage-stats-runtime-helpers.js'

const USAGE_STATS_CURSOR_SAFETY_DELAY_SECONDS = 5
const USAGE_STATS_MAX_SHARDS_PER_BATCH = 16
const USAGE_RANK_SNAPSHOT_EMPTY_SOURCE_WATERMARK = '0000-00-00T00:00:00.000Z'
const USAGE_RANK_SNAPSHOT_JOB_STATE_SCOPE_TYPE = 'global'
const USAGE_RANK_SNAPSHOT_JOB_STATE_SCOPE_ID = ''
let usageStatsShardScanOffset = 0

export function aggregateUsageStatsBatch(limit = 2000): number {
  const database = getStatsDatabase()
  const batchLimit = Math.max(1, limit)
  const shardLocationsWindow = usageStatsShardLocationsForBatch(batchLimit)
  const shardLocations = shardLocationsWindow.locations
  const safeCreatedBefore = usageStatsSafeCreatedBefore()
  const transactionStarted = beginImmediateDatabaseTransaction(database)
  let processedRows = 0
  try {
    const updatedAt = nowIso()
    if (shardLocations.length === 0) {
      updateStatsJobState(database, {
        lastSuccessAt: updatedAt,
        lagSeconds: 0
      })
      commitDatabaseTransaction(database, transactionStarted)
      return 0
    }

    const scannedAllShardLocations = !shardLocationsWindow.hasMore
    const perShardLimit = Math.max(1, Math.ceil(batchLimit / shardLocations.length))
    let globalCursor: { created_at: string; id: string } | undefined
    let maxLagSeconds = 0
    let aggregationContext: ReturnType<typeof createUsageStatsAggregationContext> | undefined
    const shardsWithMoreRows: UsageRecordShardLocation[] = []
    const processShard = (location: UsageRecordShardLocation, limitForShard: number): boolean => {
      if (processedRows >= batchLimit) return false
      const state = usageStatsShardJobState(database, location.shardKey)
      const shardDatabase = getUsageRecordShardDatabase(location)
      const rowLimit = Math.max(1, Math.min(limitForShard, batchLimit - processedRows))
      const rows = shardDatabase
        .prepare(`
          SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
          FROM usage_records
          WHERE created_at <= ?
            AND (created_at > ? OR (created_at = ? AND id > ?))
          ORDER BY created_at ASC, id ASC
          LIMIT ?
        `)
        .all(safeCreatedBefore, state.cursorCreatedAt, state.cursorCreatedAt, state.cursorId, rowLimit) as unknown as UsageStatsRecordRow[]

      if (rows.length > 0) {
        for (const row of rows) {
          row.source_shard_key = location.shardKey
        }
        aggregationContext = aggregationContext
          ? extendUsageStatsAggregationContext(aggregationContext, rows)
          : createUsageStatsAggregationContext(rows)
        for (const row of rows) {
          aggregateUsageStatsRecord(database, row, updatedAt, aggregationContext)
        }
        processedRows += rows.length
        const last = rows[rows.length - 1]
        updateUsageStatsShardJobState(database, location, {
          cursorCreatedAt: last.created_at,
          cursorId: last.id,
          lastSuccessAt: updatedAt,
          lagSeconds: statsLagSecondsFromCursor(last.created_at)
        })
        globalCursor = latestCursor(globalCursor, { created_at: last.created_at, id: last.id })
        maxLagSeconds = Math.max(maxLagSeconds, statsLagSecondsFromCursor(last.created_at))
        return rows.length >= rowLimit
      }

      const lagSeconds = latestUsageRecordLagSeconds(shardDatabase, safeCreatedBefore, state.cursorCreatedAt, state.cursorId)
      updateUsageStatsShardJobState(database, location, {
        lastSuccessAt: updatedAt,
        lagSeconds
      })
      maxLagSeconds = Math.max(maxLagSeconds, lagSeconds)
      return false
    }

    for (const location of shardLocations) {
      if (processedRows >= batchLimit) break
      if (processShard(location, perShardLimit)) {
        shardsWithMoreRows.push(location)
      }
    }
    while (processedRows < batchLimit && shardsWithMoreRows.length > 0) {
      const candidates = shardsWithMoreRows.splice(0, shardsWithMoreRows.length)
      for (const location of candidates) {
        if (processedRows >= batchLimit) break
        if (processShard(location, batchLimit - processedRows)) {
          shardsWithMoreRows.push(location)
        }
      }
    }
    updateStatsJobState(database, {
      cursorCreatedAt: globalCursor?.created_at,
      cursorId: globalCursor?.id,
      lastSuccessAt: updatedAt,
      lagSeconds: scannedAllShardLocations ? maxLagSeconds : Math.max(maxLagSeconds, latestUsageStatsLagSeconds() ?? maxLagSeconds)
    })
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    updateStatsJobState(database, {
      lastErrorMessage: error instanceof Error ? error.message : '用量统计聚合失败',
      lagSeconds: latestUsageStatsLagSeconds()
    })
    throw error
  }

  return processedRows
}

function usageStatsShardLocationsForBatch(batchLimit: number): ReturnType<typeof listUsageRecordShardLocationsPage> {
  const maxShardCount = Math.max(1, Math.min(USAGE_STATS_MAX_SHARDS_PER_BATCH, Math.trunc(batchLimit)))
  const window = listUsageRecordShardLocationsPage({
    offset: usageStatsShardScanOffset,
    limit: maxShardCount
  })
  usageStatsShardScanOffset = window.total > 0
    ? (usageStatsShardScanOffset + window.locations.length) % window.total
    : 0
  return window
}

interface UsageRankSnapshotContext {
  timezone: string
  updatedAt: string
  snapshotAt: string
  ranges: AccountUsageStatsRange[]
  todayKey: string
  earliestDate: string
  overviewScopes: Array<{ systemAccountId: string; scopeId: string }>
  uniqueSystemAccountIds: string[]
  sourceWatermark?: string
  previousSourceWatermark?: string
}

export type UsageRankSnapshotStageName =
  | 'account_last7d_request_rank'
  | 'caller_account_last7d_request_rank'
  | 'api_key_current_month_cost_rank'
  | 'account_authorization_current_month_cost_rank'
  | 'group_authorization_current_month_cost_rank'
  | 'usage_overview_windows'
  | 'ai_performance_summary_windows'
  | 'system_metrics_trend_windows'
  | 'usage_scope_range_windows'
  | 'authorization_usage_range_windows'

type UsageRankSnapshotSourceTable =
  | 'usage_stats_totals'
  | 'usage_stats_daily'
  | 'usage_stats_hourly'
  | 'usage_stats_monthly'
  | 'usage_model_daily'
  | 'usage_error_daily'
  | 'authorization_team_usage_summary_daily'
  | 'authorization_user_usage_summary_daily'
  | 'system_metrics_hourly'
  | 'process_event_loop_hourly'

interface UsageRankSnapshotStage {
  name: UsageRankSnapshotStageName
  sourceTables: UsageRankSnapshotSourceTable[]
  run: (database: DatabaseSync, context: UsageRankSnapshotContext) => void
  runInBackground?: (database: DatabaseSync, context: UsageRankSnapshotContext, options: UsageRankSnapshotBackgroundStageOptions) => Promise<void>
}

interface UsageRankSnapshotBackgroundStageOptions {
  yieldToEventLoop: () => Promise<void>
}

export interface UsageRankSnapshotStageRuntime {
  name: string
  durationMs: number
}

export interface UsageRankSnapshotRefreshResult {
  durationMs: number
  stages: UsageRankSnapshotStageRuntime[]
  skipped?: boolean
  skipReason?: 'source_watermark_unchanged'
  sourceWatermark?: string
  refreshDate?: string
  jobName?: string
}

export interface RefreshUsageRankSnapshotsInStagesOptions {
  yieldToEventLoop?: () => Promise<void>
  stageNames?: readonly UsageRankSnapshotStageName[]
  skipIfUnchanged?: boolean
  jobName?: string
}

export function refreshUsageRankSnapshots(): void {
  const database = getStatsDatabase()
  const context = createUsageRankSnapshotContext(database)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const stage of usageRankSnapshotStages()) {
      stage.run(database, context)
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

export function refreshUsageQuotaHourlyWindowsCache(): void {
  const database = getStatsDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    refreshUsageQuotaHourlyWindowSnapshots(database, nowIso(), usageStatsTimezone())
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

export async function refreshUsageRankSnapshotsInStages(options: RefreshUsageRankSnapshotsInStagesOptions = {}): Promise<UsageRankSnapshotRefreshResult> {
  const database = getStatsDatabase()
  const context = createUsageRankSnapshotContext(database)
  const stages = selectUsageRankSnapshotStages(options.stageNames)
  const yieldToEventLoop = options.yieldToEventLoop ?? defaultUsageSnapshotYield
  const jobName = options.jobName ?? usageRankSnapshotDefaultJobName(stages)
  const startedAt = Date.now()
  const sourceWatermark = options.skipIfUnchanged ? usageRankSnapshotSourceWatermark(database, stages) : undefined
  const previousState = options.skipIfUnchanged && sourceWatermark !== undefined
    ? usageRankSnapshotRefreshJobState(database, jobName)
    : undefined
  if (previousState && previousState.cursor_created_at === sourceWatermark && previousState.cursor_id === context.todayKey) {
    return {
      durationMs: Date.now() - startedAt,
      stages: [],
      skipped: true,
      skipReason: 'source_watermark_unchanged',
      sourceWatermark,
      refreshDate: context.todayKey,
      jobName
    }
  }
  if (stages.length === 1 && previousState?.cursor_id === context.todayKey && previousState.cursor_created_at) {
    context.sourceWatermark = sourceWatermark
    context.previousSourceWatermark = previousState.cursor_created_at
  }
  const stageRuntimes: UsageRankSnapshotStageRuntime[] = []
  for (let index = 0; index < stages.length; index += 1) {
    const stageStartedAt = Date.now()
    await runUsageRankSnapshotStageInBackground(database, context, stages[index], yieldToEventLoop)
    stageRuntimes.push({
      name: stages[index].name,
      durationMs: Date.now() - stageStartedAt
    })
    if (index < stages.length - 1) {
      await yieldToEventLoop()
    }
  }
  if (options.skipIfUnchanged && sourceWatermark !== undefined) {
    updateUsageRankSnapshotRefreshJobState(database, jobName, {
      sourceWatermark,
      refreshDate: context.todayKey,
      lastSuccessAt: nowIso()
    })
  }
  return {
    durationMs: Date.now() - startedAt,
    stages: stageRuntimes,
    skipped: false,
    sourceWatermark,
    refreshDate: context.todayKey,
    jobName
  }
}

function selectUsageRankSnapshotStages(stageNames?: readonly UsageRankSnapshotStageName[]): UsageRankSnapshotStage[] {
  const stages = usageRankSnapshotStages()
  if (!stageNames) return stages
  if (stageNames.length === 0) {
    throw new Error('用量排行快照刷新至少需要一个阶段')
  }
  const selectedStageNames = new Set<UsageRankSnapshotStageName>(stageNames)
  const selectedStages = stages.filter((stage) => selectedStageNames.has(stage.name))
  const missingStageNames = stageNames.filter((stageName) => !selectedStages.some((stage) => stage.name === stageName))
  if (missingStageNames.length > 0) {
    throw new Error(`未知用量排行快照刷新阶段: ${missingStageNames.join(', ')}`)
  }
  return selectedStages
}

function usageRankSnapshotDefaultJobName(stages: UsageRankSnapshotStage[]): string {
  const allStageCount = usageRankSnapshotStages().length
  if (stages.length === allStageCount) {
    return 'usage_rank_snapshots_refresh'
  }
  return `usage_rank_snapshots_refresh:${stages.map((stage) => stage.name).join('+')}`
}

function usageRankSnapshotSourceWatermark(database: DatabaseSync, stages: UsageRankSnapshotStage[]): string {
  const sourceTables = [...new Set(stages.flatMap((stage) => stage.sourceTables))]
  let watermark = USAGE_RANK_SNAPSHOT_EMPTY_SOURCE_WATERMARK
  for (const table of sourceTables) {
    const row = database.prepare(`SELECT MAX(updated_at) AS updated_at FROM ${table}`).get() as { updated_at?: string | null } | undefined
    const updatedAt = row?.updated_at
    if (typeof updatedAt === 'string' && updatedAt > watermark) {
      watermark = updatedAt
    }
  }
  return watermark
}

function usageRankSnapshotRefreshJobState(database: DatabaseSync, jobName: string): StatsJobStateRow | undefined {
  return database
    .prepare('SELECT cursor_created_at, cursor_id, lag_seconds FROM stats_job_state WHERE scope_type = ? AND scope_id = ? AND job_name = ?')
    .get(USAGE_RANK_SNAPSHOT_JOB_STATE_SCOPE_TYPE, USAGE_RANK_SNAPSHOT_JOB_STATE_SCOPE_ID, jobName) as unknown as StatsJobStateRow | undefined
}

function updateUsageRankSnapshotRefreshJobState(database: DatabaseSync, jobName: string, input: { sourceWatermark: string; refreshDate: string; lastSuccessAt: string }): void {
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = excluded.cursor_created_at,
      cursor_id = excluded.cursor_id,
      last_success_at = excluded.last_success_at,
      last_error_message = NULL,
      lag_seconds = NULL,
      updated_at = excluded.updated_at
  `).run(
    USAGE_RANK_SNAPSHOT_JOB_STATE_SCOPE_TYPE,
    USAGE_RANK_SNAPSHOT_JOB_STATE_SCOPE_ID,
    jobName,
    input.sourceWatermark,
    input.refreshDate,
    input.lastSuccessAt,
    nowIso()
  )
}

async function runUsageRankSnapshotStageInBackground(
  database: DatabaseSync,
  context: UsageRankSnapshotContext,
  stage: UsageRankSnapshotStage,
  yieldToEventLoop: () => Promise<void>
): Promise<void> {
  if (stage.runInBackground) {
    await stage.runInBackground(database, context, { yieldToEventLoop })
    return
  }

  const transactionStarted = beginDatabaseTransaction(database)
  try {
    stage.run(database, context)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function createUsageRankSnapshotContext(database: DatabaseSync): UsageRankSnapshotContext {
  const timezone = usageStatsTimezone()
  const updatedAt = nowIso()
  const todayKey = dateKey(new Date(), timezone)
  const ranges = fixedUsageStatsRanges(timezone, todayKey)
  const earliestDate = ranges[0]?.startDate ?? todayKey
  const overviewScopes = usageOverviewSnapshotScopes(database)
  const uniqueSystemAccountIds = [...new Set(overviewScopes.map((scope) => scope.systemAccountId).filter((id) => id !== GLOBAL_STATS_SYSTEM_ACCOUNT_ID))]
  return {
    timezone,
    updatedAt,
    snapshotAt: updatedAt,
    ranges,
    todayKey,
    earliestDate,
    overviewScopes,
    uniqueSystemAccountIds
  }
}

function usageRankSnapshotStages(): UsageRankSnapshotStage[] {
  return [
    {
      name: 'account_last7d_request_rank',
      sourceTables: ['usage_stats_daily'],
      run: (database, context) => refreshAccountLast7dRequestRankSnapshot(database, context.snapshotAt, context.updatedAt, context.timezone)
    },
    {
      name: 'caller_account_last7d_request_rank',
      sourceTables: ['usage_stats_daily'],
      run: (database, context) => refreshCallerAccountLast7dRequestRankSnapshot(database, context.snapshotAt, context.updatedAt, context.timezone)
    },
    {
      name: 'api_key_current_month_cost_rank',
      sourceTables: ['usage_stats_monthly'],
      run: (database, context) => refreshApiKeyCurrentMonthCostRankSnapshot(database, context.snapshotAt, context.updatedAt, context.timezone)
    },
    {
      name: 'account_authorization_current_month_cost_rank',
      sourceTables: ['usage_stats_monthly'],
      run: (database, context) => refreshAuthorizationCurrentMonthCostRankSnapshot(database, 'account_authorization', context.snapshotAt, context.updatedAt, context.timezone)
    },
    {
      name: 'group_authorization_current_month_cost_rank',
      sourceTables: ['usage_stats_monthly'],
      run: (database, context) => refreshAuthorizationCurrentMonthCostRankSnapshot(database, 'group_authorization', context.snapshotAt, context.updatedAt, context.timezone)
    },
    {
      name: 'usage_overview_windows',
      sourceTables: ['usage_stats_totals', 'usage_stats_daily', 'usage_stats_hourly', 'usage_model_daily', 'usage_error_daily'],
      run: refreshUsageOverviewWindowSnapshots
    },
    {
      name: 'ai_performance_summary_windows',
      sourceTables: ['usage_stats_daily'],
      run: refreshAiPerformanceSummaryWindowSnapshots
    },
    {
      name: 'system_metrics_trend_windows',
      sourceTables: ['system_metrics_hourly', 'process_event_loop_hourly'],
      run: refreshSystemMetricsTrendWindowSnapshotsStage
    },
    {
      name: 'usage_scope_range_windows',
      sourceTables: ['usage_stats_daily'],
      run: (database, context) => refreshUsageScopeRangeWindowSnapshots(database, context.updatedAt, context.timezone),
      runInBackground: (database, context, options) => refreshUsageScopeRangeWindowSnapshotsInStages(database, context.updatedAt, context.timezone, options.yieldToEventLoop, context.previousSourceWatermark, context.sourceWatermark)
    },
    {
      name: 'authorization_usage_range_windows',
      sourceTables: ['authorization_team_usage_summary_daily', 'authorization_user_usage_summary_daily'],
      run: (database, context) => refreshAuthorizationUsageRangeWindowSnapshots(database, context.updatedAt, context.timezone),
      runInBackground: (database, context, options) => refreshAuthorizationUsageRangeWindowSnapshotsInStages(database, context.updatedAt, context.timezone, options.yieldToEventLoop)
    }
  ]
}

function refreshAiPerformanceSummaryWindowSnapshots(database: DatabaseSync, context: UsageRankSnapshotContext): void {
  database.prepare('DELETE FROM ai_performance_summary_windows').run()
  for (const systemAccountId of [...context.uniqueSystemAccountIds, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    refreshAiPerformanceSummaryWindows(database, systemAccountId, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
  }
}

function defaultUsageSnapshotYield(): Promise<void> {
  return new Promise((resolve) => setImmediate(resolve))
}

function refreshAiPerformanceSummaryWindows(
  database: DatabaseSync,
  systemAccountId: string,
  ranges: AccountUsageStatsRange[],
  earliestDate: string,
  todayKey: string,
  updatedAt: string
): void {
  const rows = database.prepare(`
    SELECT stat_date,
      COALESCE(SUM(request_count), 0) AS request_count,
      COALESCE(SUM(success_count), 0) AS success_count,
      COALESCE(SUM(error_count), 0) AS error_count,
      COALESCE(SUM(input_tokens), 0) AS input_tokens,
      COALESCE(SUM(output_tokens), 0) AS output_tokens,
      COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
      COALESCE(SUM(total_cost_usd), 0) AS total_cost_usd,
      COALESCE(SUM(duration_ms_sum), 0) AS duration_ms_sum,
      COALESCE(SUM(duration_ms_count), 0) AS duration_ms_count,
      COALESCE(MAX(duration_ms_max), 0) AS duration_ms_max,
      COALESCE(SUM(first_token_ms_sum), 0) AS first_token_ms_sum,
      COALESCE(SUM(first_token_ms_count), 0) AS first_token_ms_count,
      COALESCE(MAX(first_token_ms_max), 0) AS first_token_ms_max,
      MAX(last_used_at) AS last_used_at
    FROM usage_stats_daily
    WHERE system_account_id = ?
      AND scope_type = 'account'
      AND stat_date >= ?
      AND stat_date <= ?
    GROUP BY stat_date
    ORDER BY stat_date ASC
  `).all(systemAccountId, earliestDate, todayKey) as unknown as UsageStatsDailyWindowRow[]
  const rowsByDate = rowsByStatDate(rows)
  const insert = database.prepare(`
    INSERT INTO ai_performance_summary_windows (
      system_account_id, window_key, start_date, end_date, request_count, duration_ms_sum, duration_ms_count,
      duration_ms_max, first_token_ms_sum, first_token_ms_count, first_token_ms_max, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const range of ranges) {
    const aggregate = aggregateUsageRowsForRange(rowsByDate, range)
    insert.run(
      systemAccountId,
      rangeWindowKey(range),
      range.startDate,
      range.endDate,
      aggregate.requestCount,
      aggregate.durationMsSum,
      aggregate.durationMsCount,
      aggregate.durationMsMax,
      aggregate.firstTokenMsSum,
      aggregate.firstTokenMsCount,
      aggregate.firstTokenMsMax,
      updatedAt
    )
  }
}

export interface UsageStatsConsistencyIssue {
  systemAccountId: string
  scopeType: string
  scopeId: string
  statDate: string
  metric: 'request_count' | 'success_count' | 'error_count' | 'input_tokens' | 'output_tokens' | 'cache_read_tokens' | 'cache_read_cost_usd' | 'total_cost_usd'
  dailyValue: number
  hourlyValue: number
}

export function checkUsageStatsConsistency(sampleLimit = 20): UsageStatsConsistencyIssue[] {
  const database = getStatsDatabase()
  const samples = database.prepare(`
    SELECT system_account_id, scope_type, scope_id, stat_date,
      request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd
    FROM usage_stats_daily
    WHERE stat_date < ?
    ORDER BY updated_at DESC, stat_date DESC, system_account_id ASC, scope_type ASC, scope_id ASC
    LIMIT ?
  `).all(dateKey(new Date(), usageStatsTimezone()), boundedConsistencySampleLimit(sampleLimit)) as unknown as Array<Record<string, unknown>>
  const issues: UsageStatsConsistencyIssue[] = []
  for (const sample of samples) {
    const daily = consistencyStatsRow(sample)
    const hourly = database.prepare(`
      SELECT
        COALESCE(SUM(request_count), 0) AS request_count,
        COALESCE(SUM(success_count), 0) AS success_count,
        COALESCE(SUM(error_count), 0) AS error_count,
        COALESCE(SUM(input_tokens), 0) AS input_tokens,
        COALESCE(SUM(output_tokens), 0) AS output_tokens,
        COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
        COALESCE(SUM(cache_read_cost_usd), 0) AS cache_read_cost_usd,
        COALESCE(SUM(total_cost_usd), 0) AS total_cost_usd
      FROM usage_stats_hourly
      WHERE system_account_id = ?
        AND scope_type = ?
        AND scope_id = ?
        AND stat_hour >= ?
        AND stat_hour < ?
    `).get(
      daily.systemAccountId,
      daily.scopeType,
      daily.scopeId,
      `${daily.statDate}T00`,
      `${nextDateKey(daily.statDate)}T00`
    ) as unknown as Record<string, unknown> | undefined
    issues.push(...compareConsistencyRows(daily, consistencyStatsRow(hourly ?? {})))
  }
  return issues
}

export function getUsageStatsOverview(access?: AccessScope, range: AccountUsageStatsRange = normalizeDefaultUsageStatsRange()): UsageStatsOverview {
  const database = getStatsDatabase()
  const statsScope = usageOverviewStatsScope(access)
  const windowKey = rangeWindowKey(range)

  const summaryRow = database.prepare(`
    SELECT ? AS account_id, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd AS total_cost,
      duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count, last_used_at
    FROM usage_overview_summary_windows
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
  `).get(statsScope.scopeId, statsScope.systemAccountId, windowKey, range.startDate, range.endDate) as unknown as AccountUsageAggregateRow & StatsAggregateMathRow | undefined

  const hourlyRows = database.prepare(`
    SELECT bucket_key AS stat_hour, request_count, error_count, input_tokens, output_tokens, cache_read_tokens,
      cache_read_cost_usd, total_cost_usd AS total_cost, duration_ms_sum, duration_ms_count
    FROM usage_overview_trend_windows
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
    ORDER BY bucket_key ASC
  `).all(statsScope.systemAccountId, windowKey, range.startDate, range.endDate) as unknown as Array<StatsAggregateMathRow & { stat_hour: string; error_count: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_read_cost_usd: number; total_cost: number }>

  const modelRows = database.prepare(`
    SELECT provider_code, model,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd AS total_cost
    FROM usage_model_rank_windows
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
    ORDER BY rank ASC
  `).all(statsScope.systemAccountId, windowKey, range.startDate, range.endDate) as unknown as Array<{ provider_code: string; model: string; request_count: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_read_cost_usd: number; total_cost: number }>

  const errorRows = database.prepare(`
    SELECT provider_code, error_code, status_code, error_message, error_count
    FROM usage_error_rank_windows
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
    ORDER BY rank ASC
  `).all(statsScope.systemAccountId, windowKey, range.startDate, range.endDate) as unknown as Array<{ provider_code: string; error_code: string; status_code: number; error_message: string | null; error_count: number }>

  return {
    range,
    summary: usageSummaryWithMath(summaryRow ?? emptyStatsAggregateMathRow()),
    hourlyTrend: mapUsageTrendRows(hourlyRows),
    modelDistribution: modelRows.map((row) => ({
      providerCode: row.provider_code,
      model: row.model,
      requestCount: Number(row.request_count ?? 0),
      totalTokens: Number(row.input_tokens ?? 0) + Number(row.output_tokens ?? 0),
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

function mapUsageTrendRows(
  rows: Array<StatsAggregateMathRow & { stat_hour: string; error_count: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_read_cost_usd: number; total_cost: number }>,
): UsageStatsOverview['hourlyTrend'] {
  return rows.map((row) => ({
    statHour: row.stat_hour,
    requestCount: Number(row.request_count ?? 0),
    totalTokens: Number(row.input_tokens ?? 0) + Number(row.output_tokens ?? 0),
    totalCost: Number(row.total_cost ?? 0),
    averageDurationMs: averageFromSum(row.duration_ms_sum, row.duration_ms_count),
    errorCount: Number(row.error_count ?? 0)
  }))
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

function usageStatsShardJobState(database: DatabaseSync, shardKey: string): { cursorCreatedAt: string; cursorId: string } {
  const row = database
    .prepare("SELECT cursor_created_at, cursor_id FROM stats_job_state WHERE scope_type = 'usage_shard' AND scope_id = ? AND job_name = 'usage_stats_aggregation'")
    .get(shardKey) as unknown as StatsJobStateRow | undefined
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

function latestCursor(
  current: { created_at: string; id: string } | undefined,
  next: { created_at: string; id: string }
): { created_at: string; id: string } {
  if (!current) return next
  if (next.created_at > current.created_at) return next
  if (next.created_at === current.created_at && next.id > current.id) return next
  return current
}

interface ConsistencyStatsRow {
  systemAccountId: string
  scopeType: string
  scopeId: string
  statDate: string
  request_count: number
  success_count: number
  error_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_read_cost_usd: number
  total_cost_usd: number
}

function consistencyStatsRow(row: Record<string, unknown>): ConsistencyStatsRow {
  return {
    systemAccountId: String(row.system_account_id ?? ''),
    scopeType: String(row.scope_type ?? ''),
    scopeId: String(row.scope_id ?? ''),
    statDate: String(row.stat_date ?? ''),
    request_count: Number(row.request_count ?? 0),
    success_count: Number(row.success_count ?? 0),
    error_count: Number(row.error_count ?? 0),
    input_tokens: Number(row.input_tokens ?? 0),
    output_tokens: Number(row.output_tokens ?? 0),
    cache_read_tokens: Number(row.cache_read_tokens ?? 0),
    cache_read_cost_usd: Number(row.cache_read_cost_usd ?? 0),
    total_cost_usd: Number(row.total_cost_usd ?? 0)
  }
}

function compareConsistencyRows(daily: ConsistencyStatsRow, hourly: ConsistencyStatsRow): UsageStatsConsistencyIssue[] {
  const metrics: UsageStatsConsistencyIssue['metric'][] = ['request_count', 'success_count', 'error_count', 'input_tokens', 'output_tokens', 'cache_read_tokens', 'cache_read_cost_usd', 'total_cost_usd']
  const issues: UsageStatsConsistencyIssue[] = []
  for (const metric of metrics) {
    const dailyValue = daily[metric]
    const hourlyValue = hourly[metric]
    const tolerance = metric === 'total_cost_usd' || metric === 'cache_read_cost_usd' ? 0.000001 : 0
    if (Math.abs(dailyValue - hourlyValue) <= tolerance) continue
    issues.push({
      systemAccountId: daily.systemAccountId,
      scopeType: daily.scopeType,
      scopeId: daily.scopeId,
      statDate: daily.statDate,
      metric,
      dailyValue,
      hourlyValue
    })
  }
  return issues
}

function boundedConsistencySampleLimit(value: number): number {
  const number = Number(value)
  return Number.isFinite(number) ? Math.min(Math.max(Math.trunc(number), 1), 100) : 20
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
  `).run(input.cursorCreatedAt ?? null, input.cursorId ?? null, input.lastSuccessAt ?? null, input.lastErrorMessage ?? null, input.lagSeconds ?? null, nowIso())
}

function updateUsageStatsShardJobState(database: DatabaseSync, location: UsageRecordShardLocation, input: { cursorCreatedAt?: string; cursorId?: string; lastSuccessAt?: string; lastErrorMessage?: string; lagSeconds?: number }): void {
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('usage_shard', ?, 'usage_stats_aggregation', ?, ?, ?, ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = COALESCE(excluded.cursor_created_at, stats_job_state.cursor_created_at),
      cursor_id = COALESCE(excluded.cursor_id, stats_job_state.cursor_id),
      last_success_at = COALESCE(excluded.last_success_at, stats_job_state.last_success_at),
      last_error_message = excluded.last_error_message,
      lag_seconds = excluded.lag_seconds,
      updated_at = excluded.updated_at
  `).run(location.shardKey, input.cursorCreatedAt ?? null, input.cursorId ?? null, input.lastSuccessAt ?? null, input.lastErrorMessage ?? null, input.lagSeconds ?? null, nowIso())
}

function statsLagSecondsFromCursor(cursorCreatedAt: string): number {
  const cursorTime = Date.parse(cursorCreatedAt)
  return Number.isFinite(cursorTime) ? Math.max(0, Math.floor((Date.now() - cursorTime) / 1000)) : 0
}
