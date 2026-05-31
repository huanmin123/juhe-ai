import type { DatabaseSync } from 'node:sqlite'

import type { ProcessRole } from '../config/runtime.js'
import type {
  AccountUsageStatsRange,
} from '../domain/types.js'
import { canAccessAll, currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { beginDatabaseTransaction, beginImmediateDatabaseTransaction, commitDatabaseTransaction, getDatabase, getStatsDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { getUsageRecordShardDatabase, listUsageRecordShardLocations, type UsageRecordShardLocation } from './usage-record-shards.js'
import { averageFromSum, dateKey, hourKey, usageStatsTimezone } from './usage-stats-helpers.js'
import { emptyStatsAggregateMathRow, mapProcessEventLoopHourly, mapSystemMetricsHourly, mapSystemMetricsLatest, usageSummaryWithMath } from './usage-stats-mappers.js'
import { aggregateSystemMetricsRows, nullableNumber } from './usage-stats-metric-aggregates.js'
import { latestUsageStatsLagSeconds, normalizeDefaultUsageStatsRange } from './usage-stats-runtime-helpers.js'
import {
  refreshAccountLast7dRequestRankSnapshot,
  refreshApiKeyCurrentMonthCostRankSnapshot,
  refreshAuthorizationCurrentMonthCostRankSnapshot,
  refreshCallerAccountLast7dRequestRankSnapshot,
  refreshUsageQuotaHourlyWindowSnapshots
} from './usage-stats-snapshot-helpers.js'
import { aggregateUsageStatsRecord, createUsageStatsAggregationContext } from './usage-stats-writers.js'
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
  compareText,
  fixedUsageStatsDateKeys,
  fixedUsageStatsRanges,
  nextDateKey,
  rangeWindowKey,
  rowsByStatDate,
  rowsByStatHourDate,
  rowsForDateRange,
  sortedMapEntries,
  trendBucketKey,
  trendBucketHours
} from './usage-stats-window-helpers.js'
import {
  GLOBAL_STATS_SCOPE_ID,
  GLOBAL_STATS_SYSTEM_ACCOUNT_ID,
  USAGE_STATS_RECORD_SELECT_COLUMNS,
  type AccountUsageAggregateRow,
  type ProcessEventLoopSampleInput,
  type StatsAggregateMathRow,
  type StatsJobStateRow,
  type SystemMetricsOverview,
  type SystemMetricsSampleInput,
  type UsageStatsOverview,
  type UsageStatsRecordRow
} from './usage-stats-types.js'

export type { SystemMetricsOverview, SystemMetricsSampleInput, UsageStatsOverview } from './usage-stats-types.js'
export { getAiPerformanceOverview, listAiPerformanceAccountOptions } from './usage-stats-ai-performance.repository.js'
export { latestUsageStatsLagSeconds, normalizeDefaultUsageStatsRange } from './usage-stats-runtime-helpers.js'

const USAGE_STATS_CURSOR_SAFETY_DELAY_SECONDS = 5
const PROCESS_EVENT_LOOP_ROLES: ProcessRole[] = ['server', 'worker', 'db-service']
const PROCESS_EVENT_LOOP_PEAK_WINDOW_MS = 24 * 60 * 60 * 1000
const USAGE_RANGE_WINDOW_STAGED_YIELD_EVERY = 1
const USAGE_RANK_SNAPSHOT_EMPTY_SOURCE_WATERMARK = '0000-00-00T00:00:00.000Z'
const USAGE_RANK_SNAPSHOT_JOB_STATE_SCOPE_TYPE = 'global'
const USAGE_RANK_SNAPSHOT_JOB_STATE_SCOPE_ID = ''
export const GROUP_ACCOUNT_STATS_DIRTY_ALL = '__all__'
let usageStatsShardScanOffset = 0

interface GroupAccountStatsDirtyRow {
  groupId: string
  updatedAt: string
}

export function aggregateUsageStatsBatch(limit = 2000): number {
  const database = getStatsDatabase()
  const shardLocations = listUsageRecordShardLocations()
  const batchLimit = Math.max(1, limit)
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

    const orderedShardLocations = orderedUsageStatsShardLocations(shardLocations)
    const perShardLimit = Math.max(1, Math.ceil(batchLimit / orderedShardLocations.length))
    let globalCursor: { created_at: string; id: string } | undefined
    let maxLagSeconds = 0
    let aggregationContext: ReturnType<typeof createUsageStatsAggregationContext> | undefined
    const shardsWithMoreRows: UsageRecordShardLocation[] = []
    const processShard = (location: UsageRecordShardLocation, limitForShard: number, updateIgnoredCursor: boolean): boolean => {
      if (processedRows >= batchLimit) return false
      const state = usageStatsShardJobState(database, location.shardKey)
      const shardDatabase = getUsageRecordShardDatabase(location)
      const rowLimit = Math.max(1, Math.min(limitForShard, batchLimit - processedRows))
      const rows = shardDatabase
        .prepare(`
          SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
          FROM usage_records
          WHERE created_at <= ?
            AND COALESCE(traffic_source, 'gateway') <> 'cooldown_retest'
            AND (created_at > ? OR (created_at = ? AND id > ?))
          ORDER BY created_at ASC, id ASC
          LIMIT ?
        `)
        .all(safeCreatedBefore, state.cursorCreatedAt, state.cursorCreatedAt, state.cursorId, rowLimit) as unknown as UsageStatsRecordRow[]

      if (rows.length > 0) {
        for (const row of rows) {
          row.source_shard_key = location.shardKey
        }
        aggregationContext ??= createUsageStatsAggregationContext(rows)
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

      if (!updateIgnoredCursor) return false
      const ignoredCursor = latestIgnoredUsageRecordCursor(shardDatabase, safeCreatedBefore, state.cursorCreatedAt, state.cursorId)
      const cursorCreatedAt = ignoredCursor?.created_at ?? state.cursorCreatedAt
      const cursorId = ignoredCursor?.id ?? state.cursorId
      const lagSeconds = latestUsageRecordLagSeconds(shardDatabase, safeCreatedBefore, cursorCreatedAt, cursorId)
      updateUsageStatsShardJobState(database, location, {
        cursorCreatedAt: ignoredCursor?.created_at,
        cursorId: ignoredCursor?.id,
        lastSuccessAt: updatedAt,
        lagSeconds
      })
      if (ignoredCursor) {
        globalCursor = latestCursor(globalCursor, ignoredCursor)
      }
      maxLagSeconds = Math.max(maxLagSeconds, lagSeconds)
      return false
    }

    for (const location of orderedShardLocations) {
      if (processedRows >= batchLimit) break
      if (processShard(location, perShardLimit, true)) {
        shardsWithMoreRows.push(location)
      }
    }
    while (processedRows < batchLimit && shardsWithMoreRows.length > 0) {
      const candidates = shardsWithMoreRows.splice(0, shardsWithMoreRows.length)
      for (const location of candidates) {
        if (processedRows >= batchLimit) break
        if (processShard(location, batchLimit - processedRows, false)) {
          shardsWithMoreRows.push(location)
        }
      }
    }
    updateStatsJobState(database, {
      cursorCreatedAt: globalCursor?.created_at,
      cursorId: globalCursor?.id,
      lastSuccessAt: updatedAt,
      lagSeconds: maxLagSeconds
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

function orderedUsageStatsShardLocations(locations: UsageRecordShardLocation[]): UsageRecordShardLocation[] {
  if (locations.length <= 1) return locations
  const startIndex = usageStatsShardScanOffset % locations.length
  usageStatsShardScanOffset = (startIndex + 1) % locations.length
  return [...locations.slice(startIndex), ...locations.slice(0, startIndex)]
}

export function markGroupAccountStatsDirty(groupIds: Array<string | null | undefined> | string | null | undefined, reason = 'write'): void {
  const ids = uniqueGroupAccountStatsIds(Array.isArray(groupIds) ? groupIds : [groupIds])
  if (!ids.length) return
  const database = getDatabase()
  const updatedAt = nowIso()
  const insert = database.prepare(`
    INSERT INTO group_account_stats_dirty (group_id, reason, updated_at)
    VALUES (?, ?, ?)
    ON CONFLICT(group_id) DO UPDATE SET
      reason = excluded.reason,
      updated_at = excluded.updated_at
  `)
  for (const id of ids) {
    insert.run(id, reason, updatedAt)
  }
}

export function markAllGroupAccountStatsDirty(reason = 'write'): void {
  markGroupAccountStatsDirty(GROUP_ACCOUNT_STATS_DIRTY_ALL, reason)
}

export function markGroupAccountStatsDirtyByAccountIds(accountIds: Array<string | null | undefined>, reason = 'account_write'): void {
  const ids = uniqueGroupAccountStatsIds(accountIds)
  if (!ids.length) return
  const groupIds: string[] = []
  const database = getDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    groupIds.push(...(database.prepare(`
      SELECT DISTINCT group_id
      FROM group_accounts
      WHERE account_id IN (${sqlPlaceholders(chunk.length)})
    `).all(...chunk) as unknown as Array<{ group_id: string }>).map((row) => row.group_id))
  }
  markGroupAccountStatsDirty(groupIds, reason)
}

export function refreshDirtyGroupAccountStatsCache(limit = 1000): number {
  const businessDatabase = getDatabase()
  const statsDatabase = getStatsDatabase()
  const allDirtyRows = loadAllGroupAccountStatsDirtyRows(businessDatabase)
  if (allDirtyRows.length > 0) {
    refreshGroupAccountStatsCache()
    deleteGroupAccountStatsDirtyRows(businessDatabase, allDirtyRows)
    return 1
  }

  const rows = loadGroupAccountStatsDirtyRows(businessDatabase, limit)
  if (!rows.length) {
    const hasStats = statsDatabase.prepare('SELECT 1 FROM group_account_stats LIMIT 1').get()
    if (!hasStats) {
      refreshGroupAccountStatsCache()
    }
    return 0
  }

  refreshGroupAccountStatsCache(rows.map((row) => row.groupId))
  deleteGroupAccountStatsDirtyRows(businessDatabase, rows)
  return rows.length
}

function loadAllGroupAccountStatsDirtyRows(businessDatabase: DatabaseSync): GroupAccountStatsDirtyRow[] {
  const businessRow = businessDatabase
    .prepare('SELECT group_id, updated_at FROM group_account_stats_dirty WHERE group_id = ? LIMIT 1')
    .get(GROUP_ACCOUNT_STATS_DIRTY_ALL) as unknown as { group_id: string; updated_at: string } | undefined
  return businessRow ? [mapGroupAccountStatsDirtyRow(businessRow)] : []
}

function loadGroupAccountStatsDirtyRows(
  businessDatabase: DatabaseSync,
  limit: number
): GroupAccountStatsDirtyRow[] {
  const normalizedLimit = Math.max(1, Math.trunc(limit))
  const businessRows = businessDatabase
    .prepare('SELECT group_id, updated_at FROM group_account_stats_dirty WHERE group_id <> ? ORDER BY updated_at ASC, group_id ASC LIMIT ?')
    .all(GROUP_ACCOUNT_STATS_DIRTY_ALL, normalizedLimit) as unknown as Array<{ group_id: string; updated_at: string }>
  return businessRows
    .map((row) => mapGroupAccountStatsDirtyRow(row))
    .sort((left, right) => left.updatedAt.localeCompare(right.updatedAt) || left.groupId.localeCompare(right.groupId))
    .slice(0, normalizedLimit)
}

function mapGroupAccountStatsDirtyRow(row: { group_id: string; updated_at: string }): GroupAccountStatsDirtyRow {
  return {
    groupId: row.group_id,
    updatedAt: row.updated_at
  }
}

function deleteGroupAccountStatsDirtyRows(
  businessDatabase: DatabaseSync,
  rows: GroupAccountStatsDirtyRow[]
): void {
  const deleteBusinessDirty = businessDatabase.prepare('DELETE FROM group_account_stats_dirty WHERE group_id = ? AND updated_at = ?')
  for (const row of rows) {
    deleteBusinessDirty.run(row.groupId, row.updatedAt)
  }
}

export function refreshGroupAccountStatsCache(groupIds?: Array<string | null | undefined>): void {
  const database = getStatsDatabase()
  const businessDatabase = getDatabase()
  const updatedAt = nowIso()
  const targetGroupIds = groupIds === undefined ? undefined : uniqueGroupAccountStatsIds(groupIds)
  if (targetGroupIds && !targetGroupIds.length) return
  const groups = loadGroupAccountStatsGroups(businessDatabase, targetGroupIds)
  const groupAccountRows = loadGroupAccountStatsRows(businessDatabase, targetGroupIds)
  const statsByGroup = new Map<string, GroupAccountStatsAccumulator>()
  for (const group of groups) {
    statsByGroup.set(group.id, emptyGroupAccountStatsAccumulator(group.id, group.system_account_id))
  }
  for (const row of groupAccountRows) {
    const stats = statsByGroup.get(row.group_id) ?? emptyGroupAccountStatsAccumulator(row.group_id, row.group_system_account_id)
    statsByGroup.set(row.group_id, stats)
    if (!row.account_id || !row.account_system_account_id) continue
    const authorizationActive = row.authorization_status === 'active' && (!row.authorization_expires_at || row.authorization_expires_at > updatedAt)
    const authorized = row.account_authorization_id
      ? authorizationActive
      : row.account_system_account_id === row.group_system_account_id
    if (!authorized) continue
    stats.total += 1
    stats.concurrencyLimit += Number(row.concurrency_limit ?? 0)
    if (row.status === 'active') {
      stats.active += 1
      if (row.schedulable === 1 && (!row.cooldown_until || row.cooldown_until <= updatedAt)) {
        stats.available += 1
      }
    } else if (row.status === 'disabled') {
      stats.disabled += 1
    } else {
      stats.error += 1
    }
    if (row.status === 'rate_limited') {
      stats.rateLimited += 1
    }
  }
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    if (targetGroupIds) {
      deleteGroupAccountStatsRows(database, targetGroupIds)
    } else {
      database.prepare('DELETE FROM group_account_stats').run()
    }
    const insert = database.prepare(`
      INSERT INTO group_account_stats (
        system_account_id, group_id, total, available, active, disabled, error,
        rate_limited, current_concurrency, concurrency_limit, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)
    `)
    for (const stats of statsByGroup.values()) {
      insert.run(
        stats.systemAccountId,
        stats.groupId,
        stats.total,
        stats.available,
        stats.active,
        stats.disabled,
        stats.error,
        stats.rateLimited,
        stats.concurrencyLimit,
        updatedAt
      )
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function loadGroupAccountStatsGroups(
  database: DatabaseSync,
  groupIds?: string[]
): Array<{ id: string; system_account_id: string }> {
  if (!groupIds) {
    return database.prepare('SELECT id, system_account_id FROM groups').all() as unknown as Array<{ id: string; system_account_id: string }>
  }
  const rows: Array<{ id: string; system_account_id: string }> = []
  for (const chunk of chunkValues(groupIds, 900)) {
    rows.push(...database.prepare(`
      SELECT id, system_account_id
      FROM groups
      WHERE id IN (${sqlPlaceholders(chunk.length)})
    `).all(...chunk) as unknown as Array<{ id: string; system_account_id: string }>)
  }
  return rows
}

function loadGroupAccountStatsRows(
  database: DatabaseSync,
  groupIds?: string[]
): Array<{
  group_id: string
  account_id: string | null
  account_authorization_id: string | null
  group_system_account_id: string
  account_system_account_id: string | null
  status: string | null
  schedulable: number | null
  cooldown_until: string | null
  concurrency_limit: number | null
  authorization_status: string | null
  authorization_expires_at: string | null
}> {
  const rows: Array<{
    group_id: string
    account_id: string | null
    account_authorization_id: string | null
    group_system_account_id: string
    account_system_account_id: string | null
    status: string | null
    schedulable: number | null
    cooldown_until: string | null
    concurrency_limit: number | null
    authorization_status: string | null
    authorization_expires_at: string | null
  }> = []
  const chunks = groupIds ? chunkValues(groupIds, 900) : [undefined]
  for (const chunk of chunks) {
    const where = chunk ? `AND group_accounts.group_id IN (${sqlPlaceholders(chunk.length)})` : ''
    rows.push(...database.prepare(`
      SELECT
        group_accounts.group_id,
        group_accounts.account_id,
        group_accounts.account_authorization_id,
        groups.system_account_id AS group_system_account_id,
        accounts.system_account_id AS account_system_account_id,
        accounts.status,
        accounts.schedulable,
        accounts.cooldown_until,
        accounts.concurrency_limit,
        account_authorizations.status AS authorization_status,
        account_authorizations.expires_at AS authorization_expires_at
      FROM group_accounts
      INNER JOIN groups ON groups.id = group_accounts.group_id
      LEFT JOIN accounts ON accounts.id = group_accounts.account_id
      LEFT JOIN resource_authorizations account_authorizations
        ON account_authorizations.id = group_accounts.account_authorization_id
      WHERE group_accounts.enabled = 1
        ${where}
    `).all(...(chunk ?? [])) as unknown as typeof rows)
  }
  return rows
}

function deleteGroupAccountStatsRows(database: DatabaseSync, groupIds: string[]): void {
  for (const chunk of chunkValues(groupIds, 900)) {
    database.prepare(`DELETE FROM group_account_stats WHERE group_id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk)
  }
}

function uniqueGroupAccountStatsIds(values: Array<string | null | undefined>): string[] {
  return [...new Set(values.map((value) => value?.trim()).filter((value): value is string => Boolean(value)))]
}

interface GroupAccountStatsAccumulator {
  groupId: string
  systemAccountId: string
  total: number
  available: number
  active: number
  disabled: number
  error: number
  rateLimited: number
  concurrencyLimit: number
}

function emptyGroupAccountStatsAccumulator(groupId: string, systemAccountId: string): GroupAccountStatsAccumulator {
  return {
    groupId,
    systemAccountId,
    total: 0,
    available: 0,
    active: 0,
    disabled: 0,
    error: 0,
    rateLimited: 0,
    concurrencyLimit: 0
  }
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
  if (options.skipIfUnchanged && sourceWatermark !== undefined) {
    const state = usageRankSnapshotRefreshJobState(database, jobName)
    if (state?.cursor_created_at === sourceWatermark && state.cursor_id === context.todayKey) {
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
      runInBackground: (database, context, options) => refreshUsageScopeRangeWindowSnapshotsInStages(database, context.updatedAt, context.timezone, options.yieldToEventLoop)
    },
    {
      name: 'authorization_usage_range_windows',
      sourceTables: ['authorization_team_usage_summary_daily', 'authorization_user_usage_summary_daily'],
      run: (database, context) => refreshAuthorizationUsageRangeWindowSnapshots(database, context.updatedAt, context.timezone),
      runInBackground: (database, context, options) => refreshAuthorizationUsageRangeWindowSnapshotsInStages(database, context.updatedAt, context.timezone, options.yieldToEventLoop)
    }
  ]
}

function refreshUsageOverviewWindowSnapshots(database: DatabaseSync, context: UsageRankSnapshotContext): void {
  refreshUsageOverviewSummaryWindowSnapshots(database, context)
  refreshUsageOverviewTrendWindowSnapshots(database, context)
  refreshUsageModelRankWindowSnapshots(database, context)
  refreshUsageErrorRankWindowSnapshots(database, context)
}

function refreshUsageOverviewSummaryWindowSnapshots(database: DatabaseSync, context: UsageRankSnapshotContext): void {
  database.prepare('DELETE FROM usage_overview_summary_windows').run()
  for (const scope of context.overviewScopes) {
    refreshUsageOverviewSummaryWindows(database, scope, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
  }
}

function refreshUsageOverviewTrendWindowSnapshots(database: DatabaseSync, context: UsageRankSnapshotContext): void {
  database.prepare('DELETE FROM usage_overview_trend_windows').run()
  for (const scope of context.overviewScopes) {
    refreshUsageOverviewTrendWindows(database, scope, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
  }
}

function refreshUsageModelRankWindowSnapshots(database: DatabaseSync, context: UsageRankSnapshotContext): void {
  database.prepare('DELETE FROM usage_model_rank_windows').run()
  for (const systemAccountId of [...context.uniqueSystemAccountIds, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    refreshUsageModelRankWindows(database, systemAccountId, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
  }
}

function refreshUsageErrorRankWindowSnapshots(database: DatabaseSync, context: UsageRankSnapshotContext): void {
  database.prepare('DELETE FROM usage_error_rank_windows').run()
  for (const systemAccountId of [...context.uniqueSystemAccountIds, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    refreshUsageErrorRankWindows(database, systemAccountId, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
  }
}

function refreshAiPerformanceSummaryWindowSnapshots(database: DatabaseSync, context: UsageRankSnapshotContext): void {
  database.prepare('DELETE FROM ai_performance_summary_windows').run()
  for (const systemAccountId of [...context.uniqueSystemAccountIds, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    refreshAiPerformanceSummaryWindows(database, systemAccountId, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
  }
}

function refreshSystemMetricsTrendWindowSnapshotsStage(database: DatabaseSync, context: UsageRankSnapshotContext): void {
  database.prepare('DELETE FROM system_metrics_trend_windows').run()
  database.prepare('DELETE FROM process_event_loop_trend_windows').run()
  refreshSystemMetricsTrendWindows(database, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
  refreshProcessEventLoopTrendWindows(database, context.ranges, context.earliestDate, context.todayKey, context.updatedAt)
}

function defaultUsageSnapshotYield(): Promise<void> {
  return new Promise((resolve) => setImmediate(resolve))
}

function usageOverviewSnapshotScopes(database: DatabaseSync): Array<{ systemAccountId: string; scopeId: string }> {
  const rows = database.prepare(`
    SELECT DISTINCT system_account_id, scope_id
    FROM usage_stats_totals
    WHERE scope_type = 'system_account'
  `).all() as unknown as Array<{ system_account_id?: string | null; scope_id?: string | null }>
  const scopes = rows
    .map((row) => ({ systemAccountId: row.system_account_id ?? '', scopeId: row.scope_id ?? '' }))
    .filter((row) => row.systemAccountId && row.scopeId)
  if (!scopes.some((scope) => scope.systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID && scope.scopeId === GLOBAL_STATS_SCOPE_ID)) {
    scopes.push({ systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeId: GLOBAL_STATS_SCOPE_ID })
  }
  return scopes
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
      event_loop_lag_ms_sum, event_loop_lag_ms_max, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
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
        nullableNumber(row.event_loop_lag_ms_max),
        updatedAt
      )
    }
  }
}

function aggregateProcessEventLoopRows(rows: Array<Record<string, unknown>>, bucketHours: number): Array<Record<string, unknown>> {
  const buckets = new Map<string, Record<string, unknown>>()
  for (const row of rows) {
    const processRole = String(row.process_role ?? '')
    if (!processRole) continue
    const statHour = trendBucketKey(String(row.stat_hour ?? ''), bucketHours)
    const bucketKey = `${statHour}:${processRole}`
    const bucket = buckets.get(bucketKey) ?? { stat_hour: statHour, process_role: processRole, sample_count: 0, event_loop_lag_ms_sum: 0 }
    bucket.sample_count = Number(bucket.sample_count ?? 0) + Number(row.sample_count ?? 0)
    bucket.event_loop_lag_ms_sum = Number(bucket.event_loop_lag_ms_sum ?? 0) + Number(row.event_loop_lag_ms_sum ?? 0)
    const value = nullableNumber(row.event_loop_lag_ms_max)
    const current = nullableNumber(bucket.event_loop_lag_ms_max)
    if (value !== null) {
      bucket.event_loop_lag_ms_max = current === null ? value : Math.max(current, value)
    }
    buckets.set(bucketKey, bucket)
  }
  return [...buckets.values()]
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
    'event_loop_lag_ms_max'
  ].join(', ')
}

function refreshUsageScopeRangeWindowSnapshots(database: DatabaseSync, updatedAt: string, timezone: string): void {
  const todayKey = dateKey(new Date(), timezone)
  const dates = fixedUsageStatsDateKeys(timezone, todayKey)
  if (!dates.length) return
  database.prepare('DELETE FROM usage_scope_range_windows WHERE end_date >= ? AND end_date <= ?').run(dates[0], todayKey)
  const insert = database.prepare(`
    INSERT INTO usage_scope_range_windows (
      system_account_id, scope_type, scope_id, start_date, end_date,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
    )
    SELECT
      system_account_id,
      scope_type,
      scope_id,
      ?,
      ?,
      COALESCE(SUM(request_count), 0),
      COALESCE(SUM(input_tokens), 0),
      COALESCE(SUM(output_tokens), 0),
      COALESCE(SUM(cache_read_tokens), 0),
      COALESCE(SUM(cache_read_cost_usd), 0),
      COALESCE(SUM(total_cost_usd), 0),
      MAX(last_used_at),
      ?
    FROM usage_stats_daily
    WHERE stat_date >= ?
      AND stat_date <= ?
    GROUP BY system_account_id, scope_type, scope_id
    HAVING COALESCE(SUM(request_count), 0) > 0
      OR COALESCE(SUM(input_tokens), 0) > 0
      OR COALESCE(SUM(output_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
      OR COALESCE(SUM(total_cost_usd), 0) > 0
  `)
  for (let startIndex = 0; startIndex < dates.length; startIndex += 1) {
    for (let endIndex = startIndex; endIndex < dates.length; endIndex += 1) {
      const startDate = dates[startIndex]
      const rangeEndDate = dates[endIndex]
      insert.run(startDate, rangeEndDate, updatedAt, startDate, rangeEndDate)
    }
  }
}

function refreshAuthorizationUsageRangeWindowSnapshots(database: DatabaseSync, updatedAt: string, timezone: string): void {
  const todayKey = dateKey(new Date(), timezone)
  const dates = fixedUsageStatsDateKeys(timezone, todayKey)
  if (!dates.length) return
  database.prepare('DELETE FROM authorization_team_usage_range_windows WHERE end_date >= ? AND end_date <= ?').run(dates[0], todayKey)
  database.prepare('DELETE FROM authorization_user_usage_range_windows WHERE end_date >= ? AND end_date <= ?').run(dates[0], todayKey)

  const insertTeamRange = database.prepare(`
    INSERT INTO authorization_team_usage_range_windows (
      system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
    )
    SELECT
      system_account_id,
      ?,
      ?,
      team_filter_id,
      resource_filter_type,
      resource_filter_id,
      COALESCE(SUM(request_count), 0),
      COALESCE(SUM(input_tokens), 0),
      COALESCE(SUM(output_tokens), 0),
      COALESCE(SUM(cache_read_tokens), 0),
      COALESCE(SUM(cache_read_cost_usd), 0),
      COALESCE(SUM(total_cost_usd), 0),
      MAX(last_used_at),
      ?
    FROM authorization_team_usage_summary_daily
    WHERE stat_date >= ?
      AND stat_date <= ?
    GROUP BY system_account_id, team_filter_id, resource_filter_type, resource_filter_id
    HAVING COALESCE(SUM(request_count), 0) > 0
      OR COALESCE(SUM(input_tokens), 0) > 0
      OR COALESCE(SUM(output_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
      OR COALESCE(SUM(total_cost_usd), 0) > 0
  `)
  const insertUserRange = database.prepare(`
    INSERT INTO authorization_user_usage_range_windows (
      system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
    )
    SELECT
      system_account_id,
      ?,
      ?,
      team_filter_id,
      grantee_filter_system_account_id,
      resource_filter_type,
      resource_filter_id,
      COALESCE(SUM(request_count), 0),
      COALESCE(SUM(input_tokens), 0),
      COALESCE(SUM(output_tokens), 0),
      COALESCE(SUM(cache_read_tokens), 0),
      COALESCE(SUM(cache_read_cost_usd), 0),
      COALESCE(SUM(total_cost_usd), 0),
      MAX(last_used_at),
      ?
    FROM authorization_user_usage_summary_daily
    WHERE stat_date >= ?
      AND stat_date <= ?
    GROUP BY system_account_id, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id
    HAVING COALESCE(SUM(request_count), 0) > 0
      OR COALESCE(SUM(input_tokens), 0) > 0
      OR COALESCE(SUM(output_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
      OR COALESCE(SUM(total_cost_usd), 0) > 0
  `)
  for (let startIndex = 0; startIndex < dates.length; startIndex += 1) {
    for (let endIndex = startIndex; endIndex < dates.length; endIndex += 1) {
      const startDate = dates[startIndex]
      const rangeEndDate = dates[endIndex]
      insertTeamRange.run(startDate, rangeEndDate, updatedAt, startDate, rangeEndDate)
      insertUserRange.run(startDate, rangeEndDate, updatedAt, startDate, rangeEndDate)
    }
  }
}

async function refreshUsageScopeRangeWindowSnapshotsInStages(
  database: DatabaseSync,
  updatedAt: string,
  timezone: string,
  yieldToEventLoop: () => Promise<void>
): Promise<void> {
  const todayKey = dateKey(new Date(), timezone)
  const dates = fixedUsageStatsDateKeys(timezone, todayKey)
  if (!dates.length) return
  const tempTableName = 'usage_scope_range_windows_refresh_tmp'
  prepareUsageScopeRangeWindowRefreshTempTable(database, tempTableName)
  try {
    database.prepare(`DELETE FROM ${tempTableName}`).run()
    const insert = database.prepare(`
      INSERT INTO ${tempTableName} (
        system_account_id, scope_type, scope_id, start_date, end_date,
        request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
      )
      SELECT
        system_account_id,
        scope_type,
        scope_id,
        ?,
        ?,
        COALESCE(SUM(request_count), 0),
        COALESCE(SUM(input_tokens), 0),
        COALESCE(SUM(output_tokens), 0),
        COALESCE(SUM(cache_read_tokens), 0),
        COALESCE(SUM(cache_read_cost_usd), 0),
        COALESCE(SUM(total_cost_usd), 0),
        MAX(last_used_at),
        ?
      FROM usage_stats_daily
      WHERE stat_date >= ?
        AND stat_date <= ?
      GROUP BY system_account_id, scope_type, scope_id
      HAVING COALESCE(SUM(request_count), 0) > 0
        OR COALESCE(SUM(input_tokens), 0) > 0
        OR COALESCE(SUM(output_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
        OR COALESCE(SUM(total_cost_usd), 0) > 0
    `)
    let processedRanges = 0
    for (let startIndex = 0; startIndex < dates.length; startIndex += 1) {
      for (let endIndex = startIndex; endIndex < dates.length; endIndex += 1) {
        const startDate = dates[startIndex]
        const rangeEndDate = dates[endIndex]
        insert.run(startDate, rangeEndDate, updatedAt, startDate, rangeEndDate)
        processedRanges += 1
        if (processedRanges % USAGE_RANGE_WINDOW_STAGED_YIELD_EVERY === 0) {
          await yieldToEventLoop()
        }
      }
    }
    publishUsageScopeRangeWindowSnapshots(database, dates[0], todayKey, tempTableName)
  } finally {
    clearTemporaryRangeWindowTable(database, tempTableName)
  }
}

async function refreshAuthorizationUsageRangeWindowSnapshotsInStages(
  database: DatabaseSync,
  updatedAt: string,
  timezone: string,
  yieldToEventLoop: () => Promise<void>
): Promise<void> {
  const todayKey = dateKey(new Date(), timezone)
  const dates = fixedUsageStatsDateKeys(timezone, todayKey)
  if (!dates.length) return
  const teamTempTableName = 'authorization_team_usage_range_windows_refresh_tmp'
  const userTempTableName = 'authorization_user_usage_range_windows_refresh_tmp'
  prepareAuthorizationUsageRangeWindowRefreshTempTables(database, teamTempTableName, userTempTableName)
  try {
    database.prepare(`DELETE FROM ${teamTempTableName}`).run()
    database.prepare(`DELETE FROM ${userTempTableName}`).run()

    const insertTeamRange = database.prepare(`
      INSERT INTO ${teamTempTableName} (
        system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
        request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
      )
      SELECT
        system_account_id,
        ?,
        ?,
        team_filter_id,
        resource_filter_type,
        resource_filter_id,
        COALESCE(SUM(request_count), 0),
        COALESCE(SUM(input_tokens), 0),
        COALESCE(SUM(output_tokens), 0),
        COALESCE(SUM(cache_read_tokens), 0),
        COALESCE(SUM(cache_read_cost_usd), 0),
        COALESCE(SUM(total_cost_usd), 0),
        MAX(last_used_at),
        ?
      FROM authorization_team_usage_summary_daily
      WHERE stat_date >= ?
        AND stat_date <= ?
      GROUP BY system_account_id, team_filter_id, resource_filter_type, resource_filter_id
      HAVING COALESCE(SUM(request_count), 0) > 0
        OR COALESCE(SUM(input_tokens), 0) > 0
        OR COALESCE(SUM(output_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
        OR COALESCE(SUM(total_cost_usd), 0) > 0
    `)
    const insertUserRange = database.prepare(`
      INSERT INTO ${userTempTableName} (
        system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
        request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
      )
      SELECT
        system_account_id,
        ?,
        ?,
        team_filter_id,
        grantee_filter_system_account_id,
        resource_filter_type,
        resource_filter_id,
        COALESCE(SUM(request_count), 0),
        COALESCE(SUM(input_tokens), 0),
        COALESCE(SUM(output_tokens), 0),
        COALESCE(SUM(cache_read_tokens), 0),
        COALESCE(SUM(cache_read_cost_usd), 0),
        COALESCE(SUM(total_cost_usd), 0),
        MAX(last_used_at),
        ?
      FROM authorization_user_usage_summary_daily
      WHERE stat_date >= ?
        AND stat_date <= ?
      GROUP BY system_account_id, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id
      HAVING COALESCE(SUM(request_count), 0) > 0
        OR COALESCE(SUM(input_tokens), 0) > 0
        OR COALESCE(SUM(output_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
        OR COALESCE(SUM(total_cost_usd), 0) > 0
    `)
    let processedRanges = 0
    for (let startIndex = 0; startIndex < dates.length; startIndex += 1) {
      for (let endIndex = startIndex; endIndex < dates.length; endIndex += 1) {
        const startDate = dates[startIndex]
        const rangeEndDate = dates[endIndex]
        insertTeamRange.run(startDate, rangeEndDate, updatedAt, startDate, rangeEndDate)
        insertUserRange.run(startDate, rangeEndDate, updatedAt, startDate, rangeEndDate)
        processedRanges += 1
        if (processedRanges % USAGE_RANGE_WINDOW_STAGED_YIELD_EVERY === 0) {
          await yieldToEventLoop()
        }
      }
    }
    publishAuthorizationUsageRangeWindowSnapshots(database, dates[0], todayKey, teamTempTableName, userTempTableName)
  } finally {
    clearTemporaryRangeWindowTable(database, teamTempTableName)
    clearTemporaryRangeWindowTable(database, userTempTableName)
  }
}

function prepareUsageScopeRangeWindowRefreshTempTable(database: DatabaseSync, tableName: string): void {
  database.prepare(`
    CREATE TEMP TABLE IF NOT EXISTS ${tableName} (
      system_account_id TEXT NOT NULL,
      scope_type TEXT NOT NULL,
      scope_id TEXT NOT NULL DEFAULT '',
      start_date TEXT NOT NULL,
      end_date TEXT NOT NULL,
      request_count INTEGER NOT NULL DEFAULT 0,
      input_tokens INTEGER NOT NULL DEFAULT 0,
      output_tokens INTEGER NOT NULL DEFAULT 0,
      cache_read_tokens INTEGER NOT NULL DEFAULT 0,
      cache_read_cost_usd REAL NOT NULL DEFAULT 0,
      total_cost_usd REAL NOT NULL DEFAULT 0,
      last_used_at TEXT,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, scope_type, scope_id, start_date, end_date)
    )
  `).run()
}

function prepareAuthorizationUsageRangeWindowRefreshTempTables(database: DatabaseSync, teamTableName: string, userTableName: string): void {
  database.prepare(`
    CREATE TEMP TABLE IF NOT EXISTS ${teamTableName} (
      system_account_id TEXT NOT NULL,
      start_date TEXT NOT NULL,
      end_date TEXT NOT NULL,
      team_filter_id TEXT NOT NULL DEFAULT '',
      resource_filter_type TEXT NOT NULL DEFAULT 'all',
      resource_filter_id TEXT NOT NULL DEFAULT '',
      request_count INTEGER NOT NULL DEFAULT 0,
      input_tokens INTEGER NOT NULL DEFAULT 0,
      output_tokens INTEGER NOT NULL DEFAULT 0,
      cache_read_tokens INTEGER NOT NULL DEFAULT 0,
      cache_read_cost_usd REAL NOT NULL DEFAULT 0,
      total_cost_usd REAL NOT NULL DEFAULT 0,
      last_used_at TEXT,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id)
    )
  `).run()
  database.prepare(`
    CREATE TEMP TABLE IF NOT EXISTS ${userTableName} (
      system_account_id TEXT NOT NULL,
      start_date TEXT NOT NULL,
      end_date TEXT NOT NULL,
      team_filter_id TEXT NOT NULL DEFAULT '',
      grantee_filter_system_account_id TEXT NOT NULL DEFAULT '',
      resource_filter_type TEXT NOT NULL DEFAULT 'all',
      resource_filter_id TEXT NOT NULL DEFAULT '',
      request_count INTEGER NOT NULL DEFAULT 0,
      input_tokens INTEGER NOT NULL DEFAULT 0,
      output_tokens INTEGER NOT NULL DEFAULT 0,
      cache_read_tokens INTEGER NOT NULL DEFAULT 0,
      cache_read_cost_usd REAL NOT NULL DEFAULT 0,
      total_cost_usd REAL NOT NULL DEFAULT 0,
      last_used_at TEXT,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id)
    )
  `).run()
}

function publishUsageScopeRangeWindowSnapshots(database: DatabaseSync, startDate: string, endDate: string, tempTableName: string): void {
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database.prepare('DELETE FROM usage_scope_range_windows WHERE end_date >= ? AND end_date <= ?').run(startDate, endDate)
    database.prepare(`
      INSERT INTO usage_scope_range_windows (
        system_account_id, scope_type, scope_id, start_date, end_date,
        request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
      )
      SELECT
        system_account_id,
        scope_type,
        scope_id,
        start_date,
        end_date,
        request_count,
        input_tokens,
        output_tokens,
        cache_read_tokens,
        cache_read_cost_usd,
        total_cost_usd,
        last_used_at,
        updated_at
      FROM ${tempTableName}
    `).run()
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function publishAuthorizationUsageRangeWindowSnapshots(
  database: DatabaseSync,
  startDate: string,
  endDate: string,
  teamTempTableName: string,
  userTempTableName: string
): void {
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database.prepare('DELETE FROM authorization_team_usage_range_windows WHERE end_date >= ? AND end_date <= ?').run(startDate, endDate)
    database.prepare('DELETE FROM authorization_user_usage_range_windows WHERE end_date >= ? AND end_date <= ?').run(startDate, endDate)
    database.prepare(`
      INSERT INTO authorization_team_usage_range_windows (
        system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
        request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
      )
      SELECT
        system_account_id,
        start_date,
        end_date,
        team_filter_id,
        resource_filter_type,
        resource_filter_id,
        request_count,
        input_tokens,
        output_tokens,
        cache_read_tokens,
        cache_read_cost_usd,
        total_cost_usd,
        last_used_at,
        updated_at
      FROM ${teamTempTableName}
    `).run()
    database.prepare(`
      INSERT INTO authorization_user_usage_range_windows (
        system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
        request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
      )
      SELECT
        system_account_id,
        start_date,
        end_date,
        team_filter_id,
        grantee_filter_system_account_id,
        resource_filter_type,
        resource_filter_id,
        request_count,
        input_tokens,
        output_tokens,
        cache_read_tokens,
        cache_read_cost_usd,
        total_cost_usd,
        last_used_at,
        updated_at
      FROM ${userTempTableName}
    `).run()
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function clearTemporaryRangeWindowTable(database: DatabaseSync, tableName: string): void {
  try {
    database.prepare(`DELETE FROM ${tableName}`).run()
  } catch {
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

export function insertSystemMetricsSample(input: SystemMetricsSampleInput): void {
  const database = getStatsDatabase()
  const sampledAt = nowIso()
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

export function insertProcessEventLoopSample(input: ProcessEventLoopSampleInput): void {
  const eventLoopLagMs = input.eventLoopLagMs
  if (eventLoopLagMs === undefined || !Number.isFinite(eventLoopLagMs)) {
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
          sampled_at, process_role, process_pid, event_loop_lag_ms, id, created_at
        ) VALUES (?, ?, ?, ?, ?, ?)
      `)
      .run(
        sampledAt,
        input.processRole,
        input.processPid ?? null,
        eventLoopLagMs,
        newId('process_metric'),
        sampledAt
      )
    upsertProcessEventLoopHourly(database, statHour, input.processRole, eventLoopLagMs, sampledAt)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

export function getUsageStatsOverview(access?: AccessScope, range: AccountUsageStatsRange = normalizeDefaultUsageStatsRange()): UsageStatsOverview {
  const database = getStatsDatabase()
  const statsScope = usageOverviewStatsScope(access)
  const windowKey = rangeWindowKey(range)

  const summaryRow = database.prepare(`
    SELECT ? AS account_id, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd AS cache_read_cost, cache_read_cost_usd, total_cost_usd AS total_cost,
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

export function getSystemMetricsOverview(range: AccountUsageStatsRange = normalizeDefaultUsageStatsRange()): SystemMetricsOverview {
  const database = getStatsDatabase()
  const latest = database.prepare(`
    SELECT ${systemMetricsLatestSelectColumns()}
    FROM system_metrics_samples
    ORDER BY sampled_at DESC, id DESC
    LIMIT 1
  `).get() as unknown as Record<string, unknown> | undefined
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
    latest: latest ? mapSystemMetricsLatest(latest) : undefined,
    hourlyTrend: rows.map(mapSystemMetricsHourly),
    processEventLoopLatestStatus,
    processEventLoopPeakStatus,
    processEventLoopTrend: processRows,
    backgroundJobs: []
  }
}

function processEventLoopPeakStartIso(): string {
  return new Date(Date.now() - PROCESS_EVENT_LOOP_PEAK_WINDOW_MS).toISOString()
}

function loadProcessEventLoopTrendWindowRows(database: DatabaseSync, range: AccountUsageStatsRange): SystemMetricsOverview['processEventLoopTrend'] {
  const rows = database.prepare(`
    SELECT bucket_key AS stat_hour, process_role, sample_count, event_loop_lag_ms_sum, event_loop_lag_ms_max
    FROM process_event_loop_trend_windows
    WHERE window_key = ? AND start_date = ? AND end_date = ?
    ORDER BY bucket_key ASC, process_role ASC
  `).all(rangeWindowKey(range), range.startDate, range.endDate) as unknown as Array<Record<string, unknown>>
  return rows.map(mapProcessEventLoopHourly)
}

function processEventLoopPeakRows(database: DatabaseSync, startedAt: string): Array<Record<string, unknown>> {
  const peakStatement = database.prepare(`
    SELECT ${processEventLoopLatestSelectColumns()}
    FROM process_event_loop_samples
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

function processRoleFromValue(value: unknown): ProcessRole | undefined {
  return PROCESS_EVENT_LOOP_ROLES.find((role) => role === value)
}

function buildProcessEventLoopStatus(rows: Array<Record<string, unknown>>): SystemMetricsOverview['processEventLoopLatestStatus'] {
  const statusByRole = new Map<ProcessRole, { processPid?: number; sampledAt: string; eventLoopLagMs?: number }>()
  for (const row of rows) {
    const processRole = processRoleFromValue(row.process_role)
    if (!processRole || statusByRole.has(processRole)) continue
    statusByRole.set(processRole, {
      processPid: nullableNumber(row.process_pid) ?? undefined,
      sampledAt: String(row.sampled_at ?? ''),
      eventLoopLagMs: nullableNumber(row.event_loop_lag_ms) ?? undefined
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
        eventLoopLagMs: null
      }
    }
    return {
      processRole,
      sampleAvailable: true,
      processPid: row.processPid ?? null,
      sampledAt: row.sampledAt,
      eventLoopLagMs: row.eventLoopLagMs ?? null
    }
  })
}

function systemMetricsLatestSelectColumns(): string {
  return [
    'sampled_at',
    'cpu_percent',
    'memory_used_percent',
    'memory_total_bytes',
    'memory_free_bytes',
    'process_rss_bytes',
    'process_heap_used_bytes',
    'process_heap_total_bytes',
    'event_loop_lag_ms',
    'network_rx_bytes_per_sec',
    'network_tx_bytes_per_sec',
    'network_rx_total_bytes',
    'network_tx_total_bytes',
    'db_file_bytes',
    'stats_lag_seconds'
  ].join(', ')
}

function processEventLoopLatestSelectColumns(): string {
  return [
    'process_role',
    'process_pid',
    'sampled_at',
    'event_loop_lag_ms'
  ].join(', ')
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

function latestIgnoredUsageRecordCursor(database: DatabaseSync, safeCreatedBefore: string, cursorCreatedAt: string, cursorId: string): { created_at: string; id: string } | undefined {
  const latest = database
    .prepare(`
      SELECT created_at, id
      FROM usage_records
      WHERE created_at <= ?
        AND COALESCE(traffic_source, 'gateway') = 'cooldown_retest'
        AND (created_at > ? OR (created_at = ? AND id > ?))
      ORDER BY created_at DESC, id DESC
      LIMIT 1
    `)
    .get(safeCreatedBefore, cursorCreatedAt, cursorCreatedAt, cursorId) as unknown as { created_at?: string; id?: string } | undefined
  return latest?.created_at && latest.id ? { created_at: latest.created_at, id: latest.id } : undefined
}

function latestUsageRecordLagSeconds(database: DatabaseSync, safeCreatedBefore: string, cursorCreatedAt: string, cursorId: string): number {
  const latest = database
    .prepare(`
      SELECT created_at
      FROM usage_records
      WHERE created_at <= ?
        AND COALESCE(traffic_source, 'gateway') <> 'cooldown_retest'
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

function upsertProcessEventLoopHourly(
  database: DatabaseSync,
  statHour: string,
  processRole: ProcessEventLoopSampleInput['processRole'],
  eventLoopLagMs: number,
  updatedAt: string
): void {
  database.prepare(`
    INSERT INTO process_event_loop_hourly (
      stat_hour, process_role, sample_count, event_loop_lag_ms_sum, event_loop_lag_ms_max, updated_at
    )
    VALUES (?, ?, 1, ?, ?, ?)
    ON CONFLICT(stat_hour, process_role) DO UPDATE SET
      sample_count = sample_count + 1,
      event_loop_lag_ms_sum = event_loop_lag_ms_sum + excluded.event_loop_lag_ms_sum,
      event_loop_lag_ms_max = CASE WHEN excluded.event_loop_lag_ms_max IS NULL THEN process_event_loop_hourly.event_loop_lag_ms_max WHEN process_event_loop_hourly.event_loop_lag_ms_max IS NULL OR excluded.event_loop_lag_ms_max > process_event_loop_hourly.event_loop_lag_ms_max THEN excluded.event_loop_lag_ms_max ELSE process_event_loop_hourly.event_loop_lag_ms_max END,
      updated_at = excluded.updated_at
  `).run(
    statHour,
    processRole,
    eventLoopLagMs,
    eventLoopLagMs,
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
