import type { DatabaseSync } from 'node:sqlite'

import type {
  AccountUsageStatsRange,
} from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { estimateProviderCacheReadCostUsd } from '../modules/model-pricing/model-pricing.service.js'
import { canAccessAll, currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { beginDatabaseTransaction, beginImmediateDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, getStatsDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { defaultRequestQuotaHourlyWindowHours, maxRequestQuotaHourlyWindowHours } from './request-quota-limits.js'
import {
  refreshSystemMetricsTrendWindowSnapshotsStage,
  refreshSystemMetricsTrendWindowSnapshotsStageAsync
} from './system-metrics.repository.js'
import { getUsageRecordShardDatabase, listUsageRecordShardLocationsPage, type UsageRecordShardLocation } from './usage-record-shards.js'
import { averageFromSum, dateKey, hourKey, minuteKey, monthKey, usageStatsTimezone, usageStatsTimezoneAsync, weekKey } from './usage-stats-helpers.js'
import { emptyStatsAggregateMathRow, usageSummaryWithMath } from './usage-stats-mappers.js'
import {
  refreshUsageOverviewWindowSnapshots,
  refreshUsageOverviewWindowSnapshotsAsync,
  usageOverviewSnapshotScopes,
  usageOverviewSnapshotScopesAsync
} from './usage-overview-windows.repository.js'
import {
  refreshAuthorizationUsageRangeWindowSnapshots,
  refreshAuthorizationUsageRangeWindowSnapshotsAsync,
  refreshAuthorizationUsageRangeWindowSnapshotsInStages,
  refreshUsageScopeRangeWindowSnapshotsAsync,
  refreshUsageScopeRangeWindowSnapshots,
  refreshUsageScopeRangeWindowSnapshotsInStages
} from './usage-range-windows.repository.js'
import { latestUsageStatsLagSeconds, normalizeDefaultUsageStatsRange } from './usage-stats-runtime-helpers.js'
import {
  refreshAccountLast7dRequestRankSnapshot,
  refreshAccountLast7dRequestRankSnapshotAsync,
  refreshApiKeyCurrentMonthCostRankSnapshot,
  refreshApiKeyCurrentMonthCostRankSnapshotAsync,
  refreshAuthorizationCurrentMonthCostRankSnapshot,
  refreshAuthorizationCurrentMonthCostRankSnapshotAsync,
  refreshCallerAccountLast7dRequestRankSnapshot,
  refreshCallerAccountLast7dRequestRankSnapshotAsync,
  refreshUsageQuotaHourlyWindowSnapshots
} from './usage-stats-snapshot-helpers.js'
import { aggregateUsageStatsRecords, createUsageStatsAggregationContext, extendUsageStatsAggregationContext } from './usage-stats-writers.js'
import { upsertAuthorizationUsageReportRowsAsync } from './usage-stats-authorization-daily-writer.js'
import { shouldAggregateUsageStatsRecord, usageStatsAccumulatorFromRecord, usageStatsEntries } from './usage-stats-aggregation.js'
import { addAggregatedLatencyEntries, type AggregatedLatencyEntry } from './usage-stats-latency-writer.js'
import { usageErrorTimeBuckets, usageModelTimeBuckets, usageStatsTimeBuckets, type UsageStatsTimeBucketDefinition, type UsageStatsTimeKeys } from './usage-stats-time-buckets.js'
import {
  aggregateUsageRowsForRange,
  type UsageStatsDailyWindowRow
} from './usage-stats-window-aggregates.js'
import {
  fixedUsageStatsRanges,
  HOUR_MS,
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
  type UsageStatsAccumulator,
  type UsageStatsEntry,
  type UsageStatsOverview,
  type UsageStatsRecordRow
} from './usage-stats-types.js'
import { statsParamsTail } from './usage-stats-writer-params.js'

export type { ProcessEventLoopSampleInput, SystemMetricsOverview, SystemMetricsSampleInput, UsageStatsOverview } from './usage-stats-types.js'
export {
  getAiPerformanceOverview,
  getAiPerformanceOverviewAsync,
  listAiPerformanceAccountOptions,
  listAiPerformanceAccountOptionsAsync
} from './usage-stats-ai-performance.repository.js'
export {
  getSystemMetricsOverview,
  getSystemMetricsOverviewAsync,
  insertProcessEventLoopSample,
  insertProcessEventLoopSampleAsync,
  insertSystemMetricsSample,
  insertSystemMetricsSampleAsync
} from './system-metrics.repository.js'
export {
  GROUP_ACCOUNT_STATS_DIRTY_ALL,
  markAllGroupAccountStatsDirty,
  markGroupAccountStatsDirty,
  markGroupAccountStatsDirtyByAccountIds,
  refreshDirtyGroupAccountStatsCache,
  refreshDirtyGroupAccountStatsCacheAsync,
  refreshDirtyGroupAccountStatsCacheWithWriter,
  refreshGroupAccountStatsCache
} from './group-account-stats-cache.repository.js'
export { latestUsageStatsLagSeconds, normalizeDefaultUsageStatsRange } from './usage-stats-runtime-helpers.js'

export const usageStatsCursorSafetyDelaySeconds = 15
const USAGE_STATS_CURSOR_SAFETY_DELAY_SECONDS = usageStatsCursorSafetyDelaySeconds
const USAGE_STATS_MAX_SHARDS_PER_BATCH = 16
const USAGE_RANK_SNAPSHOT_EMPTY_SOURCE_WATERMARK = '0000-00-00T00:00:00.000Z'
const USAGE_RANK_SNAPSHOT_JOB_STATE_SCOPE_TYPE = 'global'
const USAGE_RANK_SNAPSHOT_JOB_STATE_SCOPE_ID = ''
let usageStatsShardScanOffset = 0

export function aggregateUsageStatsBatch(limit = 2000, safeCreatedBeforeOverride?: string): number {
  const database = getStatsDatabase()
  const batchLimit = Math.max(1, limit)
  const shardLocationsWindow = usageStatsShardLocationsForBatch(batchLimit)
  const shardLocations = shardLocationsWindow.locations
  const safeCreatedBefore = safeCreatedBeforeOverride?.trim() || usageStatsSafeCreatedBefore()
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
        aggregateUsageStatsRecords(database, rows, updatedAt, aggregationContext)
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

export async function aggregateUsageStatsBatchAsync(limit = 2000, safeCreatedBeforeOverride?: string): Promise<number> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return aggregateUsageStatsBatch(limit, safeCreatedBeforeOverride)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const batchLimit = Math.max(1, Math.trunc(limit))
  const safeCreatedBefore = safeCreatedBeforeOverride?.trim() || usageStatsSafeCreatedBefore()
  const updatedAt = nowIso()
  const timezone = await usageStatsTimezoneAsync()
  try {
    return await client.transaction(async (tx) => {
      const state = await postgresStatsJobState(tx)
      const rows = (await tx.query<UsageStatsRecordRow>(`
        SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
        FROM juhe_usage.usage_records
        WHERE created_at <= ?
          AND (created_at > ? OR (created_at = ? AND id > ?))
        ORDER BY created_at ASC, id ASC
        LIMIT ?
      `, [safeCreatedBefore, state.cursorCreatedAt, state.cursorCreatedAt, state.cursorId, batchLimit]))
        .map(normalizePostgresUsageStatsRecordRow)

      if (rows.length === 0) {
        const lagSeconds = await latestPostgresUsageRecordLagSeconds(tx, safeCreatedBefore, state.cursorCreatedAt, state.cursorId)
        await updatePostgresStatsJobState(tx, {
          lastSuccessAt: updatedAt,
          lagSeconds
        })
        return 0
      }

      await aggregatePostgresUsageStatsRows(tx, rows, updatedAt, timezone)
      const last = rows[rows.length - 1]
      await updatePostgresStatsJobState(tx, {
        cursorCreatedAt: last.created_at,
        cursorId: last.id,
        lastSuccessAt: updatedAt,
        lagSeconds: statsLagSecondsFromCursor(last.created_at)
      })
      return rows.length
    })
  } catch (error) {
    await updatePostgresStatsJobState(client, {
      lastErrorMessage: error instanceof Error ? error.message : '用量统计 PG 聚合失败'
    }).catch(() => undefined)
    throw error
  }
}

interface PostgresAggregatedUsageStatsEntry {
  systemAccountId: string
  scopeType: string
  scopeId: string
  accumulator: UsageStatsAccumulator
}

interface PostgresAggregatedUsageStatsTimeEntry extends PostgresAggregatedUsageStatsEntry {
  bucket: UsageStatsTimeBucketDefinition
  timeValue: string
}

interface PostgresAggregatedUsageModelEntry {
  bucket: UsageStatsTimeBucketDefinition
  systemAccountId: string
  providerCode: string
  model: string
  timeValue: string
  accumulator: UsageStatsAccumulator
}

interface PostgresAggregatedUsageErrorEntry {
  bucket: UsageStatsTimeBucketDefinition
  systemAccountId: string
  timeValue: string
  errorGroup: string
  providerCode: string
  errorCode: string
  statusCode: number
  errorMessage?: string | null
  requestCount: number
  errorCount: number
}

interface PostgresAggregatedAccountQualityEntry {
  accountId: string
  systemAccountId: string
  providerCode: string
  statMinute: string
  requestCount: number
  successCount: number
  errorCount: number
  firstTokenMsSum: number
  firstTokenMsCount: number
  lastSampleAt: string
  lastSuccessAt?: string
  lastErrorAt?: string
  lastErrorMessage?: string
}

async function aggregatePostgresUsageStatsRows(client: DatabaseClient, rows: UsageStatsRecordRow[], updatedAt: string, timezone: string): Promise<void> {
  if (rows.length === 0) return
  const lookup = await createPostgresUsageStatsAuthorizationLookup(client, rows)
  const totalEntries = new Map<string, PostgresAggregatedUsageStatsEntry>()
  const timeEntries = new Map<string, PostgresAggregatedUsageStatsTimeEntry>()
  const latencyEntries = new Map<string, AggregatedLatencyEntry>()
  const modelEntries = new Map<string, PostgresAggregatedUsageModelEntry>()
  const errorEntries = new Map<string, PostgresAggregatedUsageErrorEntry>()
  const accountQualityEntries = new Map<string, PostgresAggregatedAccountQualityEntry>()

  for (const row of rows) {
    if (!shouldAggregateUsageStatsRecord(row)) {
      continue
    }
    applyPostgresEstimatedCacheReadCost(row)
    const timeKeys = postgresUsageStatsTimeKeys(row, timezone)
    for (const entry of usageStatsEntries(row, lookup)) {
      addPostgresAggregatedUsageStatsEntry(totalEntries, entry)
      for (const bucket of usageStatsTimeBuckets) {
        addPostgresAggregatedUsageStatsTimeEntry(timeEntries, bucket, timeKeys[bucket.valueKey], entry)
      }
      addAggregatedLatencyEntries(latencyEntries, entry, row, timeKeys)
    }
    addPostgresAggregatedUsageModelEntries(modelEntries, row, timeKeys)
    addPostgresAggregatedAccountQualityEntry(accountQualityEntries, row, timeKeys)
    if (row.account_authorization_id || row.group_authorization_id) {
      await upsertAuthorizationUsageReportRowsAsync(client, row, timeKeys.statDate, updatedAt, lookup)
    }
    if (row.success !== 1) {
      addPostgresAggregatedUsageErrorEntries(errorEntries, row, timeKeys)
    }
  }

  await upsertPostgresUsageStatsTotals(client, [...totalEntries.values()], updatedAt)
  for (const bucket of usageStatsTimeBuckets) {
    await upsertPostgresUsageStatsTimeBucket(
      client,
      bucket,
      [...timeEntries.values()].filter((entry) => entry.bucket.tableName === bucket.tableName),
      updatedAt
    )
  }
  await upsertPostgresUsageLatencyEntries(client, [...latencyEntries.values()], updatedAt)
  await upsertPostgresUsageModelEntries(client, [...modelEntries.values()], updatedAt)
  await upsertPostgresUsageErrorEntries(client, [...errorEntries.values()], updatedAt)
  await upsertPostgresAccountQualityEntries(client, [...accountQualityEntries.values()], updatedAt)
}

async function createPostgresUsageStatsAuthorizationLookup(
  client: DatabaseClient,
  rows: UsageStatsRecordRow[]
): Promise<{ accountAuthorizationResourceIds: Map<string, string>; accountAuthorizationInstanceAccountIds: Map<string, string> }> {
  const accountAuthorizationResourceIds = new Map<string, string>()
  const accountAuthorizationInstanceAccountIds = new Map<string, string>()
  const ids = uniqueNonEmptyIds(rows.map((row) => row.account_authorization_id))
  if (ids.length === 0) {
    return { accountAuthorizationResourceIds, accountAuthorizationInstanceAccountIds }
  }
  for (const chunk of chunkValues(ids, 900)) {
    const lookupRows = await client.query<{ id?: string | null; resource_id?: string | null; instance_account_id?: string | null }>(`
      SELECT
        authorizations.id,
        authorizations.resource_id,
        instance_accounts.id AS instance_account_id
      FROM juhe_business.resource_authorizations authorizations
      LEFT JOIN juhe_business.accounts instance_accounts
        ON instance_accounts.authorization_instance_authorization_id = authorizations.id
        AND instance_accounts.system_account_id = authorizations.grantee_system_account_id
      WHERE authorizations.resource_type = 'account'
        AND authorizations.id = ANY(?)
    `, [chunk])
    for (const row of lookupRows) {
      if (row.id && row.resource_id) {
        accountAuthorizationResourceIds.set(row.id, row.resource_id)
      }
      if (row.id && row.instance_account_id) {
        accountAuthorizationInstanceAccountIds.set(row.id, row.instance_account_id)
      }
    }
  }
  return { accountAuthorizationResourceIds, accountAuthorizationInstanceAccountIds }
}

function addPostgresAggregatedUsageStatsEntry(target: Map<string, PostgresAggregatedUsageStatsEntry>, entry: UsageStatsEntry): void {
  const key = postgresUsageStatsEntryKey(entry.systemAccountId, entry.scopeType, entry.scopeId)
  const existing = target.get(key)
  if (existing) {
    mergePostgresUsageStatsAccumulator(existing.accumulator, entry.accumulator)
    return
  }
  target.set(key, {
    systemAccountId: entry.systemAccountId,
    scopeType: entry.scopeType,
    scopeId: entry.scopeId,
    accumulator: { ...entry.accumulator }
  })
}

function addPostgresAggregatedUsageStatsTimeEntry(
  target: Map<string, PostgresAggregatedUsageStatsTimeEntry>,
  bucket: PostgresAggregatedUsageStatsTimeEntry['bucket'],
  timeValue: string,
  entry: UsageStatsEntry
): void {
  const key = `${bucket.tableName}\u0000${timeValue}\u0000${postgresUsageStatsEntryKey(entry.systemAccountId, entry.scopeType, entry.scopeId)}`
  const existing = target.get(key)
  if (existing) {
    mergePostgresUsageStatsAccumulator(existing.accumulator, entry.accumulator)
    return
  }
  target.set(key, {
    bucket,
    timeValue,
    systemAccountId: entry.systemAccountId,
    scopeType: entry.scopeType,
    scopeId: entry.scopeId,
    accumulator: { ...entry.accumulator }
  })
}

function addPostgresAggregatedUsageModelEntries(
  target: Map<string, PostgresAggregatedUsageModelEntry>,
  row: UsageStatsRecordRow,
  timeKeys: UsageStatsTimeKeys
): void {
  const model = row.model?.trim()
  if (!model) return
  const accumulator = usageStatsAccumulatorFromRecord(row)
  const providerCode = row.provider_code ?? 'unknown'
  for (const systemAccountId of [row.system_account_id, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    for (const bucket of usageModelTimeBuckets) {
      const timeValue = timeKeys[bucket.valueKey]
      const key = `${bucket.tableName}\u0000${timeValue}\u0000${systemAccountId}\u0000${providerCode}\u0000${model}`
      const existing = target.get(key)
      if (existing) {
        mergePostgresUsageStatsAccumulator(existing.accumulator, accumulator)
        continue
      }
      target.set(key, {
        bucket,
        systemAccountId,
        providerCode,
        model,
        timeValue,
        accumulator: { ...accumulator }
      })
    }
  }
}

function addPostgresAggregatedUsageErrorEntries(
  target: Map<string, PostgresAggregatedUsageErrorEntry>,
  row: UsageStatsRecordRow,
  timeKeys: UsageStatsTimeKeys
): void {
  const errorGroup = row.provider_code ?? 'unknown'
  const providerCode = row.provider_code ?? 'unknown'
  const errorCode = row.error_code ?? String(row.status_code ?? 'unknown')
  const statusCode = row.status_code ?? 0
  for (const systemAccountId of [row.system_account_id, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    for (const bucket of usageErrorTimeBuckets) {
      const timeValue = timeKeys[bucket.valueKey]
      const key = `${bucket.tableName}\u0000${timeValue}\u0000${systemAccountId}\u0000${errorGroup}\u0000${providerCode}\u0000${errorCode}\u0000${statusCode}`
      const existing = target.get(key)
      if (existing) {
        existing.requestCount += 1
        existing.errorCount += 1
        existing.errorMessage = row.error_message ?? existing.errorMessage
        continue
      }
      target.set(key, {
        bucket,
        systemAccountId,
        timeValue,
        errorGroup,
        providerCode,
        errorCode,
        statusCode,
        errorMessage: row.error_message ?? null,
        requestCount: 1,
        errorCount: 1
      })
    }
  }
}

function addPostgresAggregatedAccountQualityEntry(
  target: Map<string, PostgresAggregatedAccountQualityEntry>,
  row: UsageStatsRecordRow,
  timeKeys: UsageStatsTimeKeys
): void {
  if (!shouldRecordPostgresAccountQualityStats(row) || !row.account_id || !row.api_key_id) {
    return
  }
  const success = row.success === 1
  const firstTokenMsValue = Number(row.first_token_ms ?? NaN)
  const hasFirstTokenSample = success && Number.isFinite(firstTokenMsValue) && firstTokenMsValue >= 0
  const firstTokenMs = hasFirstTokenSample ? firstTokenMsValue : 0
  const statsSystemAccountId = postgresAccountQualityStatsSystemAccountId(row)
  const key = `${row.account_id}\u0000${timeKeys.statMinute}`
  const existing = target.get(key)
  if (!existing) {
    target.set(key, {
      accountId: row.account_id,
      systemAccountId: statsSystemAccountId,
      providerCode: row.provider_code ?? 'unknown',
      statMinute: timeKeys.statMinute,
      requestCount: 1,
      successCount: success ? 1 : 0,
      errorCount: success ? 0 : 1,
      firstTokenMsSum: firstTokenMs,
      firstTokenMsCount: hasFirstTokenSample ? 1 : 0,
      lastSampleAt: row.created_at,
      lastSuccessAt: success ? row.created_at : undefined,
      lastErrorAt: success ? undefined : row.created_at,
      lastErrorMessage: success ? undefined : row.error_message ?? undefined
    })
    return
  }
  existing.requestCount += 1
  existing.successCount += success ? 1 : 0
  existing.errorCount += success ? 0 : 1
  existing.firstTokenMsSum += firstTokenMs
  existing.firstTokenMsCount += hasFirstTokenSample ? 1 : 0
  if (row.created_at > existing.lastSampleAt) {
    existing.lastSampleAt = row.created_at
    existing.systemAccountId = statsSystemAccountId
    existing.providerCode = row.provider_code ?? 'unknown'
  }
  if (success) {
    existing.lastSuccessAt = maxOptionalIso(existing.lastSuccessAt, row.created_at)
  } else if (!existing.lastErrorAt || row.created_at >= existing.lastErrorAt) {
    existing.lastErrorAt = row.created_at
    existing.lastErrorMessage = row.error_message ?? undefined
  }
}

async function upsertPostgresUsageStatsTotals(client: DatabaseClient, entries: PostgresAggregatedUsageStatsEntry[], updatedAt: string): Promise<void> {
  if (entries.length === 0) return
  const columns = [
    'system_account_id',
    'scope_type',
    'scope_id',
    'request_count',
    'success_count',
    'error_count',
    'input_tokens',
    'output_tokens',
    'cache_read_tokens',
    'cache_read_cost_usd',
    'cache_write_tokens',
    'cache_write_1h_tokens',
    'cache_write_cost_usd',
    'thinking_tokens',
    'input_image_tokens',
    'output_image_tokens',
    'total_cost_usd',
    'duration_ms_sum',
    'duration_ms_count',
    'duration_ms_max',
    'first_token_ms_sum',
    'first_token_ms_count',
    'first_token_ms_max',
    'last_used_at',
    'last_error_at',
    'updated_at'
  ]
  for (const chunk of chunkValues(entries, 500)) {
    await client.execute(`
      INSERT INTO juhe_stats.usage_stats_totals (${columns.join(', ')})
      VALUES ${postgresMultiRowPlaceholders(chunk.length, columns.length)}
      ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
        request_count = usage_stats_totals.request_count + EXCLUDED.request_count,
        success_count = usage_stats_totals.success_count + EXCLUDED.success_count,
        error_count = usage_stats_totals.error_count + EXCLUDED.error_count,
        input_tokens = usage_stats_totals.input_tokens + EXCLUDED.input_tokens,
        output_tokens = usage_stats_totals.output_tokens + EXCLUDED.output_tokens,
        cache_read_tokens = usage_stats_totals.cache_read_tokens + EXCLUDED.cache_read_tokens,
        cache_read_cost_usd = usage_stats_totals.cache_read_cost_usd + EXCLUDED.cache_read_cost_usd,
        cache_write_tokens = usage_stats_totals.cache_write_tokens + EXCLUDED.cache_write_tokens,
        cache_write_1h_tokens = usage_stats_totals.cache_write_1h_tokens + EXCLUDED.cache_write_1h_tokens,
        cache_write_cost_usd = usage_stats_totals.cache_write_cost_usd + EXCLUDED.cache_write_cost_usd,
        thinking_tokens = usage_stats_totals.thinking_tokens + EXCLUDED.thinking_tokens,
        input_image_tokens = usage_stats_totals.input_image_tokens + EXCLUDED.input_image_tokens,
        output_image_tokens = usage_stats_totals.output_image_tokens + EXCLUDED.output_image_tokens,
        total_cost_usd = usage_stats_totals.total_cost_usd + EXCLUDED.total_cost_usd,
        duration_ms_sum = usage_stats_totals.duration_ms_sum + EXCLUDED.duration_ms_sum,
        duration_ms_count = usage_stats_totals.duration_ms_count + EXCLUDED.duration_ms_count,
        duration_ms_max = GREATEST(usage_stats_totals.duration_ms_max, EXCLUDED.duration_ms_max),
        first_token_ms_sum = usage_stats_totals.first_token_ms_sum + EXCLUDED.first_token_ms_sum,
        first_token_ms_count = usage_stats_totals.first_token_ms_count + EXCLUDED.first_token_ms_count,
        first_token_ms_max = GREATEST(usage_stats_totals.first_token_ms_max, EXCLUDED.first_token_ms_max),
        last_used_at = CASE WHEN EXCLUDED.last_used_at IS NULL THEN usage_stats_totals.last_used_at WHEN usage_stats_totals.last_used_at IS NULL OR EXCLUDED.last_used_at > usage_stats_totals.last_used_at THEN EXCLUDED.last_used_at ELSE usage_stats_totals.last_used_at END,
        last_error_at = CASE WHEN EXCLUDED.last_error_at IS NULL THEN usage_stats_totals.last_error_at WHEN usage_stats_totals.last_error_at IS NULL OR EXCLUDED.last_error_at > usage_stats_totals.last_error_at THEN EXCLUDED.last_error_at ELSE usage_stats_totals.last_error_at END,
        updated_at = EXCLUDED.updated_at
    `, chunk.flatMap((entry) => [
      entry.systemAccountId,
      entry.scopeType,
      entry.scopeId,
      ...statsParamsTail(entry.accumulator, updatedAt)
    ]))
  }
}

async function upsertPostgresUsageStatsTimeBucket(
  client: DatabaseClient,
  bucket: PostgresAggregatedUsageStatsTimeEntry['bucket'],
  entries: PostgresAggregatedUsageStatsTimeEntry[],
  updatedAt: string
): Promise<void> {
  if (entries.length === 0) return
  const columns = [
    'system_account_id',
    'scope_type',
    'scope_id',
    bucket.columnName,
    'request_count',
    'success_count',
    'error_count',
    'input_tokens',
    'output_tokens',
    'cache_read_tokens',
    'cache_read_cost_usd',
    'cache_write_tokens',
    'cache_write_1h_tokens',
    'cache_write_cost_usd',
    'thinking_tokens',
    'input_image_tokens',
    'output_image_tokens',
    'total_cost_usd',
    'duration_ms_sum',
    'duration_ms_count',
    'duration_ms_max',
    'first_token_ms_sum',
    'first_token_ms_count',
    'first_token_ms_max',
    'last_used_at',
    'last_error_at',
    'updated_at'
  ]
  for (const chunk of chunkValues(entries, 450)) {
    await client.execute(`
      INSERT INTO juhe_stats.${bucket.tableName} (${columns.join(', ')})
      VALUES ${postgresMultiRowPlaceholders(chunk.length, columns.length)}
      ON CONFLICT(system_account_id, scope_type, scope_id, ${bucket.columnName}) DO UPDATE SET
        request_count = ${bucket.tableName}.request_count + EXCLUDED.request_count,
        success_count = ${bucket.tableName}.success_count + EXCLUDED.success_count,
        error_count = ${bucket.tableName}.error_count + EXCLUDED.error_count,
        input_tokens = ${bucket.tableName}.input_tokens + EXCLUDED.input_tokens,
        output_tokens = ${bucket.tableName}.output_tokens + EXCLUDED.output_tokens,
        cache_read_tokens = ${bucket.tableName}.cache_read_tokens + EXCLUDED.cache_read_tokens,
        cache_read_cost_usd = ${bucket.tableName}.cache_read_cost_usd + EXCLUDED.cache_read_cost_usd,
        cache_write_tokens = ${bucket.tableName}.cache_write_tokens + EXCLUDED.cache_write_tokens,
        cache_write_1h_tokens = ${bucket.tableName}.cache_write_1h_tokens + EXCLUDED.cache_write_1h_tokens,
        cache_write_cost_usd = ${bucket.tableName}.cache_write_cost_usd + EXCLUDED.cache_write_cost_usd,
        thinking_tokens = ${bucket.tableName}.thinking_tokens + EXCLUDED.thinking_tokens,
        input_image_tokens = ${bucket.tableName}.input_image_tokens + EXCLUDED.input_image_tokens,
        output_image_tokens = ${bucket.tableName}.output_image_tokens + EXCLUDED.output_image_tokens,
        total_cost_usd = ${bucket.tableName}.total_cost_usd + EXCLUDED.total_cost_usd,
        duration_ms_sum = ${bucket.tableName}.duration_ms_sum + EXCLUDED.duration_ms_sum,
        duration_ms_count = ${bucket.tableName}.duration_ms_count + EXCLUDED.duration_ms_count,
        duration_ms_max = GREATEST(${bucket.tableName}.duration_ms_max, EXCLUDED.duration_ms_max),
        first_token_ms_sum = ${bucket.tableName}.first_token_ms_sum + EXCLUDED.first_token_ms_sum,
        first_token_ms_count = ${bucket.tableName}.first_token_ms_count + EXCLUDED.first_token_ms_count,
        first_token_ms_max = GREATEST(${bucket.tableName}.first_token_ms_max, EXCLUDED.first_token_ms_max),
        last_used_at = CASE WHEN EXCLUDED.last_used_at IS NULL THEN ${bucket.tableName}.last_used_at WHEN ${bucket.tableName}.last_used_at IS NULL OR EXCLUDED.last_used_at > ${bucket.tableName}.last_used_at THEN EXCLUDED.last_used_at ELSE ${bucket.tableName}.last_used_at END,
        last_error_at = CASE WHEN EXCLUDED.last_error_at IS NULL THEN ${bucket.tableName}.last_error_at WHEN ${bucket.tableName}.last_error_at IS NULL OR EXCLUDED.last_error_at > ${bucket.tableName}.last_error_at THEN EXCLUDED.last_error_at ELSE ${bucket.tableName}.last_error_at END,
        updated_at = EXCLUDED.updated_at
    `, chunk.flatMap((entry) => [
      entry.systemAccountId,
      entry.scopeType,
      entry.scopeId,
      entry.timeValue,
      ...statsParamsTail(entry.accumulator, updatedAt)
    ]))
  }
}

async function upsertPostgresUsageLatencyEntries(client: DatabaseClient, entries: AggregatedLatencyEntry[], updatedAt: string): Promise<void> {
  if (entries.length === 0) return
  const entriesByTable = groupByPostgresBucketTable(entries)
  for (const [tableName, tableEntries] of entriesByTable) {
    const bucket = tableEntries[0].bucket
    const columns = [
      'system_account_id',
      'scope_type',
      'scope_id',
      'metric_type',
      bucket.columnName,
      'bucket_upper_bound_ms',
      'sample_count',
      'updated_at'
    ]
    for (const chunk of chunkValues(tableEntries, 700)) {
      await client.execute(`
        INSERT INTO juhe_stats.${tableName} (${columns.join(', ')})
        VALUES ${postgresMultiRowPlaceholders(chunk.length, columns.length)}
        ON CONFLICT(system_account_id, scope_type, scope_id, metric_type, ${bucket.columnName}, bucket_upper_bound_ms) DO UPDATE SET
          sample_count = ${tableName}.sample_count + EXCLUDED.sample_count,
          updated_at = EXCLUDED.updated_at
      `, chunk.flatMap((entry) => [
        entry.systemAccountId,
        entry.scopeType,
        entry.scopeId,
        entry.metricType,
        entry.timeValue,
        entry.bucketUpperBoundMs,
        entry.sampleCount,
        updatedAt
      ]))
    }
  }
}

async function upsertPostgresUsageModelEntries(client: DatabaseClient, entries: PostgresAggregatedUsageModelEntry[], updatedAt: string): Promise<void> {
  if (entries.length === 0) return
  const entriesByTable = groupByPostgresBucketTable(entries)
  for (const [tableName, tableEntries] of entriesByTable) {
    const bucket = tableEntries[0].bucket
    const columns = [
      'system_account_id',
      bucket.columnName,
      'provider_code',
      'model',
      'request_count',
      'success_count',
      'error_count',
      'input_tokens',
      'output_tokens',
      'cache_read_tokens',
      'cache_read_cost_usd',
      'cache_write_tokens',
      'cache_write_1h_tokens',
      'cache_write_cost_usd',
      'thinking_tokens',
      'input_image_tokens',
      'output_image_tokens',
      'total_cost_usd',
      'updated_at'
    ]
    for (const chunk of chunkValues(tableEntries, 500)) {
      await client.execute(`
        INSERT INTO juhe_stats.${tableName} (${columns.join(', ')})
        VALUES ${postgresMultiRowPlaceholders(chunk.length, columns.length)}
        ON CONFLICT(system_account_id, ${bucket.columnName}, provider_code, model) DO UPDATE SET
          request_count = ${tableName}.request_count + EXCLUDED.request_count,
          success_count = ${tableName}.success_count + EXCLUDED.success_count,
          error_count = ${tableName}.error_count + EXCLUDED.error_count,
          input_tokens = ${tableName}.input_tokens + EXCLUDED.input_tokens,
          output_tokens = ${tableName}.output_tokens + EXCLUDED.output_tokens,
          cache_read_tokens = ${tableName}.cache_read_tokens + EXCLUDED.cache_read_tokens,
          cache_read_cost_usd = ${tableName}.cache_read_cost_usd + EXCLUDED.cache_read_cost_usd,
          cache_write_tokens = ${tableName}.cache_write_tokens + EXCLUDED.cache_write_tokens,
          cache_write_1h_tokens = ${tableName}.cache_write_1h_tokens + EXCLUDED.cache_write_1h_tokens,
          cache_write_cost_usd = ${tableName}.cache_write_cost_usd + EXCLUDED.cache_write_cost_usd,
          thinking_tokens = ${tableName}.thinking_tokens + EXCLUDED.thinking_tokens,
          input_image_tokens = ${tableName}.input_image_tokens + EXCLUDED.input_image_tokens,
          output_image_tokens = ${tableName}.output_image_tokens + EXCLUDED.output_image_tokens,
          total_cost_usd = ${tableName}.total_cost_usd + EXCLUDED.total_cost_usd,
          updated_at = EXCLUDED.updated_at
      `, chunk.flatMap((entry) => {
        const stats = entry.accumulator
        return [
          entry.systemAccountId,
          entry.timeValue,
          entry.providerCode,
          entry.model,
          stats.requestCount,
          stats.successCount,
          stats.errorCount,
          stats.inputTokens,
          stats.outputTokens,
          stats.cacheReadTokens,
          stats.cacheReadCostUsd,
          stats.cacheWriteTokens,
          stats.cacheWrite1hTokens,
          stats.cacheWriteCostUsd,
          stats.thinkingTokens,
          stats.inputImageTokens,
          stats.outputImageTokens,
          stats.totalCostUsd,
          updatedAt
        ]
      }))
    }
  }
}

async function upsertPostgresUsageErrorEntries(client: DatabaseClient, entries: PostgresAggregatedUsageErrorEntry[], updatedAt: string): Promise<void> {
  if (entries.length === 0) return
  const entriesByTable = groupByPostgresBucketTable(entries)
  for (const [tableName, tableEntries] of entriesByTable) {
    const bucket = tableEntries[0].bucket
    const columns = [
      'system_account_id',
      bucket.columnName,
      'error_group',
      'provider_code',
      'error_code',
      'status_code',
      'error_message',
      'request_count',
      'error_count',
      'updated_at'
    ]
    for (const chunk of chunkValues(tableEntries, 650)) {
      await client.execute(`
        INSERT INTO juhe_stats.${tableName} (${columns.join(', ')})
        VALUES ${postgresMultiRowPlaceholders(chunk.length, columns.length)}
        ON CONFLICT(system_account_id, ${bucket.columnName}, error_group, provider_code, error_code, status_code) DO UPDATE SET
          error_message = COALESCE(EXCLUDED.error_message, ${tableName}.error_message),
          request_count = ${tableName}.request_count + EXCLUDED.request_count,
          error_count = ${tableName}.error_count + EXCLUDED.error_count,
          updated_at = EXCLUDED.updated_at
      `, chunk.flatMap((entry) => [
        entry.systemAccountId,
        entry.timeValue,
        entry.errorGroup,
        entry.providerCode,
        entry.errorCode,
        entry.statusCode,
        entry.errorMessage ?? null,
        entry.requestCount,
        entry.errorCount,
        updatedAt
      ]))
    }
  }
}

async function upsertPostgresAccountQualityEntries(client: DatabaseClient, entries: PostgresAggregatedAccountQualityEntry[], updatedAt: string): Promise<void> {
  if (entries.length === 0) return
  const columns = [
    'account_id',
    'system_account_id',
    'provider_code',
    'stat_minute',
    'request_count',
    'success_count',
    'error_count',
    'first_token_ms_sum',
    'first_token_ms_count',
    'last_sample_at',
    'last_success_at',
    'last_error_at',
    'last_error_message',
    'updated_at'
  ]
  for (const chunk of chunkValues(entries, 450)) {
    await client.execute(`
      INSERT INTO juhe_stats.account_quality_minute_stats (${columns.join(', ')})
      VALUES ${postgresMultiRowPlaceholders(chunk.length, columns.length)}
      ON CONFLICT(account_id, stat_minute) DO UPDATE SET
        system_account_id = EXCLUDED.system_account_id,
        provider_code = EXCLUDED.provider_code,
        request_count = account_quality_minute_stats.request_count + EXCLUDED.request_count,
        success_count = account_quality_minute_stats.success_count + EXCLUDED.success_count,
        error_count = account_quality_minute_stats.error_count + EXCLUDED.error_count,
        first_token_ms_sum = account_quality_minute_stats.first_token_ms_sum + EXCLUDED.first_token_ms_sum,
        first_token_ms_count = account_quality_minute_stats.first_token_ms_count + EXCLUDED.first_token_ms_count,
        last_sample_at = CASE WHEN account_quality_minute_stats.last_sample_at IS NULL OR EXCLUDED.last_sample_at > account_quality_minute_stats.last_sample_at THEN EXCLUDED.last_sample_at ELSE account_quality_minute_stats.last_sample_at END,
        last_success_at = CASE WHEN EXCLUDED.last_success_at IS NULL THEN account_quality_minute_stats.last_success_at WHEN account_quality_minute_stats.last_success_at IS NULL OR EXCLUDED.last_success_at > account_quality_minute_stats.last_success_at THEN EXCLUDED.last_success_at ELSE account_quality_minute_stats.last_success_at END,
        last_error_at = CASE WHEN EXCLUDED.last_error_at IS NULL THEN account_quality_minute_stats.last_error_at WHEN account_quality_minute_stats.last_error_at IS NULL OR EXCLUDED.last_error_at > account_quality_minute_stats.last_error_at THEN EXCLUDED.last_error_at ELSE account_quality_minute_stats.last_error_at END,
        last_error_message = CASE WHEN EXCLUDED.last_error_at IS NULL THEN account_quality_minute_stats.last_error_message WHEN account_quality_minute_stats.last_error_at IS NULL OR EXCLUDED.last_error_at >= account_quality_minute_stats.last_error_at THEN EXCLUDED.last_error_message ELSE account_quality_minute_stats.last_error_message END,
        updated_at = EXCLUDED.updated_at
    `, chunk.flatMap((entry) => [
      entry.accountId,
      entry.systemAccountId,
      entry.providerCode,
      entry.statMinute,
      entry.requestCount,
      entry.successCount,
      entry.errorCount,
      entry.firstTokenMsSum,
      entry.firstTokenMsCount,
      entry.lastSampleAt,
      entry.lastSuccessAt ?? null,
      entry.lastErrorAt ?? null,
      entry.lastErrorMessage ?? null,
      updatedAt
    ]))
  }

  for (const chunk of chunkValues(entries, 900)) {
    await client.execute(`
      INSERT INTO juhe_stats.account_quality_dirty_accounts (account_id, first_dirty_at, updated_at)
      VALUES ${postgresMultiRowPlaceholders(chunk.length, 3)}
      ON CONFLICT(account_id) DO UPDATE SET
        updated_at = EXCLUDED.updated_at
    `, chunk.flatMap((entry) => [
      entry.accountId,
      updatedAt,
      updatedAt
    ]))
  }
}

async function postgresStatsJobState(client: DatabaseClient): Promise<{ cursorCreatedAt: string; cursorId: string }> {
  const row = await client.one<StatsJobStateRow>(`
    SELECT cursor_created_at, cursor_id, lag_seconds
    FROM juhe_stats.stats_job_state
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'
    LIMIT 1
  `)
  return { cursorCreatedAt: row?.cursor_created_at ?? '', cursorId: row?.cursor_id ?? '' }
}

async function updatePostgresStatsJobState(
  client: DatabaseClient,
  input: { cursorCreatedAt?: string; cursorId?: string; lastSuccessAt?: string; lastErrorMessage?: string; lagSeconds?: number }
): Promise<void> {
  await client.execute(`
    INSERT INTO juhe_stats.stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('global', '', 'usage_stats_aggregation', ?, ?, ?, ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = COALESCE(EXCLUDED.cursor_created_at, stats_job_state.cursor_created_at),
      cursor_id = COALESCE(EXCLUDED.cursor_id, stats_job_state.cursor_id),
      last_success_at = COALESCE(EXCLUDED.last_success_at, stats_job_state.last_success_at),
      last_error_message = EXCLUDED.last_error_message,
      lag_seconds = EXCLUDED.lag_seconds,
      updated_at = EXCLUDED.updated_at
  `, [input.cursorCreatedAt ?? null, input.cursorId ?? null, input.lastSuccessAt ?? null, input.lastErrorMessage ?? null, input.lagSeconds ?? null, nowIso()])
}

async function latestPostgresUsageRecordLagSeconds(client: DatabaseClient, safeCreatedBefore: string, cursorCreatedAt: string, cursorId: string): Promise<number> {
  const latest = await client.one<{ created_at?: string }>(`
    SELECT created_at
    FROM juhe_usage.usage_records
    WHERE created_at <= ?
      AND (created_at > ? OR (created_at = ? AND id > ?))
    ORDER BY created_at DESC, id DESC
    LIMIT 1
  `, [safeCreatedBefore, cursorCreatedAt, cursorCreatedAt, cursorId])
  return latest?.created_at ? statsLagSecondsFromCursor(latest.created_at) : 0
}

function postgresUsageStatsTimeKeys(row: UsageStatsRecordRow, timezone: string): UsageStatsTimeKeys {
  const createdAt = new Date(row.created_at)
  return {
    statMinute: minuteKey(createdAt, timezone),
    statHour: hourKey(createdAt, timezone),
    statDate: dateKey(createdAt, timezone),
    statWeek: weekKey(createdAt, timezone),
    statMonth: monthKey(createdAt, timezone)
  }
}

function normalizePostgresUsageStatsRecordRow(row: UsageStatsRecordRow): UsageStatsRecordRow {
  return {
    ...row,
    status_code: nullableNumber(row.status_code),
    success: Number(row.success ?? 0),
    first_token_ms: nullableNumber(row.first_token_ms),
    duration_ms: nullableNumber(row.duration_ms),
    input_tokens: nullableNumber(row.input_tokens),
    output_tokens: nullableNumber(row.output_tokens),
    cache_read_tokens: nullableNumber(row.cache_read_tokens),
    cache_read_cost_usd: nullableNumber(row.cache_read_cost_usd),
    cache_write_tokens: nullableNumber(row.cache_write_tokens),
    cache_write_1h_tokens: nullableNumber(row.cache_write_1h_tokens),
    cache_write_cost_usd: nullableNumber(row.cache_write_cost_usd),
    thinking_tokens: nullableNumber(row.thinking_tokens),
    input_image_tokens: nullableNumber(row.input_image_tokens),
    output_image_tokens: nullableNumber(row.output_image_tokens),
    cost_usd: nullableNumber(row.cost_usd)
  }
}

function applyPostgresEstimatedCacheReadCost(row: UsageStatsRecordRow): void {
  if (row.cache_read_cost_usd !== null && row.cache_read_cost_usd !== undefined) {
    return
  }
  const cacheReadCostUsd = estimateProviderCacheReadCostUsd({
    providerCode: row.provider_code ?? '',
    model: row.model ?? undefined,
    cacheReadTokens: row.cache_read_tokens ?? undefined
  }) ?? 0
  if (cacheReadCostUsd > 0) {
    row.cache_read_cost_usd = cacheReadCostUsd
  }
}

function shouldRecordPostgresAccountQualityStats(row: UsageStatsRecordRow): boolean {
  if (
    row.traffic_source === 'cooldown_retest'
    || row.traffic_source === 'hybrid_scoring'
    || row.traffic_source === 'hybrid_quality_scoring'
  ) {
    return false
  }
  if (row.success === 1) {
    return true
  }
  return row.failure_attribution === 'account_upstream'
    || row.failure_attribution === 'account_dependency'
}

function postgresAccountQualityStatsSystemAccountId(row: UsageStatsRecordRow): string {
  if (!row.account_access_type) {
    throw new Error(`使用记录 ${row.id} 缺少账户访问类型字段 account_access_type`)
  }
  if (row.account_access_type === 'account_authorized') {
    return row.system_account_id
  }
  if (!row.account_owner_system_account_id) {
    throw new Error(`使用记录 ${row.id} 缺少账户归属字段 account_owner_system_account_id`)
  }
  return row.account_owner_system_account_id
}

function groupByPostgresBucketTable<T extends { bucket: UsageStatsTimeBucketDefinition }>(entries: T[]): Map<string, T[]> {
  const result = new Map<string, T[]>()
  for (const entry of entries) {
    const tableEntries = result.get(entry.bucket.tableName)
    if (tableEntries) {
      tableEntries.push(entry)
    } else {
      result.set(entry.bucket.tableName, [entry])
    }
  }
  return result
}

async function listPostgresRequestQuotaHourlyWindowHours(client: DatabaseClient): Promise<number[]> {
  const rows = await client.query<{ window_hours?: number | string | null }>(`
    SELECT window_hours
    FROM juhe_business.request_quota_hourly_window_configs
    WHERE window_hours BETWEEN 1 AND ?
    ORDER BY window_hours ASC
    LIMIT ?
  `, [maxRequestQuotaHourlyWindowHours, maxRequestQuotaHourlyWindowHours])
  const windows = new Set<number>(defaultRequestQuotaHourlyWindowHours)
  for (const row of rows) {
    const hours = Number(row.window_hours)
    if (Number.isInteger(hours) && hours >= 1 && hours <= maxRequestQuotaHourlyWindowHours) {
      windows.add(hours)
    }
  }
  return [...windows].sort((left, right) => left - right)
}

function nullableNumber(value: unknown): number | null {
  if (value === null || value === undefined) return null
  const number = Number(value)
  return Number.isFinite(number) ? number : null
}

function mergePostgresUsageStatsAccumulator(target: UsageStatsAccumulator, source: UsageStatsAccumulator): void {
  target.requestCount += source.requestCount
  target.successCount += source.successCount
  target.errorCount += source.errorCount
  target.inputTokens += source.inputTokens
  target.outputTokens += source.outputTokens
  target.cacheReadTokens += source.cacheReadTokens
  target.cacheReadCostUsd += source.cacheReadCostUsd
  target.cacheWriteTokens += source.cacheWriteTokens
  target.cacheWrite1hTokens += source.cacheWrite1hTokens
  target.cacheWriteCostUsd += source.cacheWriteCostUsd
  target.thinkingTokens += source.thinkingTokens
  target.inputImageTokens += source.inputImageTokens
  target.outputImageTokens += source.outputImageTokens
  target.totalCostUsd += source.totalCostUsd
  target.durationMsSum += source.durationMsSum
  target.durationMsCount += source.durationMsCount
  target.durationMsMax = Math.max(target.durationMsMax, source.durationMsMax)
  target.firstTokenMsSum += source.firstTokenMsSum
  target.firstTokenMsCount += source.firstTokenMsCount
  target.firstTokenMsMax = Math.max(target.firstTokenMsMax, source.firstTokenMsMax)
  target.lastUsedAt = maxOptionalIso(target.lastUsedAt, source.lastUsedAt)
  target.lastErrorAt = maxOptionalIso(target.lastErrorAt, source.lastErrorAt)
}

function maxOptionalIso(left?: string, right?: string): string | undefined {
  if (!left) return right
  if (!right) return left
  return left >= right ? left : right
}

function postgresUsageStatsEntryKey(systemAccountId: string, scopeType: string, scopeId: string): string {
  return `${systemAccountId}\u0000${scopeType}\u0000${scopeId}`
}

function uniqueNonEmptyIds(values: Array<string | null | undefined>): string[] {
  return [...new Set(values.map((value) => value?.trim()).filter((value): value is string => Boolean(value)))]
}

function postgresMultiRowPlaceholders(rowCount: number, columnCount: number): string {
  const row = `(${Array.from({ length: columnCount }, () => '?').join(', ')})`
  return Array.from({ length: rowCount }, () => row).join(', ')
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

export async function refreshUsageQuotaHourlyWindowsCacheAsync(): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    refreshUsageQuotaHourlyWindowsCache()
    return
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const timezone = await usageStatsTimezoneAsync()
  await client.transaction(async (tx) => {
    const updatedAt = nowIso()
    await tx.execute('DELETE FROM juhe_stats.usage_quota_hourly_windows')
    for (const hours of await listPostgresRequestQuotaHourlyWindowHours(tx)) {
      await tx.execute(`
        INSERT INTO juhe_stats.usage_quota_hourly_windows (
          system_account_id, scope_type, scope_id, window_hours, total_cost_usd, updated_at
        )
        SELECT system_account_id, scope_type, scope_id, ?, COALESCE(SUM(total_cost_usd), 0), ?
        FROM juhe_stats.usage_stats_hourly
        WHERE stat_hour >= ?
        GROUP BY system_account_id, scope_type, scope_id
        HAVING COALESCE(SUM(total_cost_usd), 0) > 0
      `, [hours, updatedAt, hourKey(new Date(Date.now() - hours * HOUR_MS), timezone)])
    }
  })
}

export async function refreshUsageRankSnapshotsInStages(options: RefreshUsageRankSnapshotsInStagesOptions = {}): Promise<UsageRankSnapshotRefreshResult> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return refreshUsageRankSnapshotsInStagesPostgres(options)
  }
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

async function refreshUsageRankSnapshotsInStagesPostgres(options: RefreshUsageRankSnapshotsInStagesOptions = {}): Promise<UsageRankSnapshotRefreshResult> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const stages = selectUsageRankSnapshotStages(options.stageNames)
  const supportedPostgresStages = new Set<UsageRankSnapshotStageName>([
    'account_last7d_request_rank',
    'caller_account_last7d_request_rank',
    'api_key_current_month_cost_rank',
    'account_authorization_current_month_cost_rank',
    'group_authorization_current_month_cost_rank',
    'usage_overview_windows',
    'ai_performance_summary_windows',
    'system_metrics_trend_windows',
    'usage_scope_range_windows',
    'authorization_usage_range_windows'
  ])
  const unsupportedStages = stages.filter((stage) => !supportedPostgresStages.has(stage.name))
  if (unsupportedStages.length > 0) {
    throw new Error(`PostgreSQL 模式暂不支持刷新用量排行阶段: ${unsupportedStages.map((stage) => stage.name).join(', ')}`)
  }
  const yieldToEventLoop = options.yieldToEventLoop ?? defaultUsageSnapshotYield
  const jobName = options.jobName ?? usageRankSnapshotDefaultJobName(stages)
  const startedAt = Date.now()
  const sourceWatermark = options.skipIfUnchanged ? await usageRankSnapshotSourceWatermarkAsync(client, stages) : undefined
  const previousState = options.skipIfUnchanged && sourceWatermark !== undefined
    ? await usageRankSnapshotRefreshJobStateAsync(client, jobName)
    : undefined
  const context = await createUsageRankSnapshotContextAsync(client)

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
    const stage = stages[index]
    const stageStartedAt = Date.now()
    switch (stage.name) {
      case 'account_last7d_request_rank':
        await client.transaction(async (tx) => {
          await refreshAccountLast7dRequestRankSnapshotAsync(tx, context.snapshotAt, context.updatedAt, context.timezone)
        })
        break
      case 'caller_account_last7d_request_rank':
        await client.transaction(async (tx) => {
          await refreshCallerAccountLast7dRequestRankSnapshotAsync(tx, context.snapshotAt, context.updatedAt, context.timezone)
        })
        break
      case 'api_key_current_month_cost_rank':
        await client.transaction(async (tx) => {
          await refreshApiKeyCurrentMonthCostRankSnapshotAsync(tx, context.snapshotAt, context.updatedAt, context.timezone)
        })
        break
      case 'account_authorization_current_month_cost_rank':
        await client.transaction(async (tx) => {
          await refreshAuthorizationCurrentMonthCostRankSnapshotAsync(tx, 'account_authorization', context.snapshotAt, context.updatedAt, context.timezone)
        })
        break
      case 'group_authorization_current_month_cost_rank':
        await client.transaction(async (tx) => {
          await refreshAuthorizationCurrentMonthCostRankSnapshotAsync(tx, 'group_authorization', context.snapshotAt, context.updatedAt, context.timezone)
        })
        break
      case 'usage_overview_windows':
        await client.transaction(async (tx) => {
          await refreshUsageOverviewWindowSnapshotsAsync(tx, context)
        })
        break
      case 'ai_performance_summary_windows':
        await client.transaction(async (tx) => {
          await refreshAiPerformanceSummaryWindowSnapshotsAsync(tx, context)
        })
        break
      case 'system_metrics_trend_windows':
        await client.transaction(async (tx) => {
          await refreshSystemMetricsTrendWindowSnapshotsStageAsync(tx, context)
        })
        break
      case 'usage_scope_range_windows':
        await refreshUsageScopeRangeWindowSnapshotsAsync(client, context.updatedAt, context.timezone, yieldToEventLoop)
        break
      case 'authorization_usage_range_windows':
        await refreshAuthorizationUsageRangeWindowSnapshotsAsync(client, context.updatedAt, context.timezone, yieldToEventLoop)
        break
      default:
        throw new Error(`PostgreSQL 模式暂不支持刷新用量排行阶段: ${stage.name}`)
    }
    stageRuntimes.push({
      name: stage.name,
      durationMs: Date.now() - stageStartedAt
    })
    if (index < stages.length - 1) {
      await yieldToEventLoop()
    }
  }
  if (options.skipIfUnchanged && sourceWatermark !== undefined) {
    await updateUsageRankSnapshotRefreshJobStateAsync(client, jobName, {
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

async function usageRankSnapshotSourceWatermarkAsync(client: DatabaseClient, stages: UsageRankSnapshotStage[]): Promise<string> {
  const sourceTables = [...new Set(stages.flatMap((stage) => stage.sourceTables))]
  let watermark = USAGE_RANK_SNAPSHOT_EMPTY_SOURCE_WATERMARK
  for (const table of sourceTables) {
    const row = await client.one<{ updated_at?: string | null }>(`SELECT MAX(updated_at) AS updated_at FROM juhe_stats.${table}`)
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

async function usageRankSnapshotRefreshJobStateAsync(client: DatabaseClient, jobName: string): Promise<StatsJobStateRow | undefined> {
  return client.one<StatsJobStateRow>(
    'SELECT cursor_created_at, cursor_id, lag_seconds FROM juhe_stats.stats_job_state WHERE scope_type = ? AND scope_id = ? AND job_name = ?',
    [USAGE_RANK_SNAPSHOT_JOB_STATE_SCOPE_TYPE, USAGE_RANK_SNAPSHOT_JOB_STATE_SCOPE_ID, jobName]
  )
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

async function updateUsageRankSnapshotRefreshJobStateAsync(client: DatabaseClient, jobName: string, input: { sourceWatermark: string; refreshDate: string; lastSuccessAt: string }): Promise<void> {
  await client.execute(`
    INSERT INTO juhe_stats.stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = excluded.cursor_created_at,
      cursor_id = excluded.cursor_id,
      last_success_at = excluded.last_success_at,
      last_error_message = NULL,
      lag_seconds = NULL,
      updated_at = excluded.updated_at
  `, [
    USAGE_RANK_SNAPSHOT_JOB_STATE_SCOPE_TYPE,
    USAGE_RANK_SNAPSHOT_JOB_STATE_SCOPE_ID,
    jobName,
    input.sourceWatermark,
    input.refreshDate,
    input.lastSuccessAt,
    nowIso()
  ])
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

async function createUsageRankSnapshotContextAsync(client: DatabaseClient): Promise<UsageRankSnapshotContext> {
  const timezone = await usageStatsTimezoneAsync()
  const updatedAt = nowIso()
  const todayKey = dateKey(new Date(), timezone)
  const ranges = fixedUsageStatsRanges(timezone, todayKey)
  const earliestDate = ranges[0]?.startDate ?? todayKey
  const overviewScopes = await usageOverviewSnapshotScopesAsync(client)
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

async function refreshAiPerformanceSummaryWindowSnapshotsAsync(client: DatabaseClient, context: UsageRankSnapshotContext): Promise<void> {
  await client.execute(`DELETE FROM ${statsTable(client, 'ai_performance_summary_windows')}`)
  const rows: unknown[][] = []
  for (const systemAccountId of [...context.uniqueSystemAccountIds, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    rows.push(...await aiPerformanceSummaryWindowRowsAsync(client, systemAccountId, context.ranges, context.earliestDate, context.todayKey, context.updatedAt))
  }
  await insertAiPerformanceSummaryWindowRowsAsync(client, rows)
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

async function aiPerformanceSummaryWindowRowsAsync(
  client: DatabaseClient,
  systemAccountId: string,
  ranges: AccountUsageStatsRange[],
  earliestDate: string,
  todayKey: string,
  updatedAt: string
): Promise<unknown[][]> {
  const rows = await client.query<UsageStatsDailyWindowRow>(`
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
    FROM ${statsTable(client, 'usage_stats_daily')}
    WHERE system_account_id = ?
      AND scope_type = 'account'
      AND stat_date >= ?
      AND stat_date <= ?
    GROUP BY stat_date
    ORDER BY stat_date ASC
  `, [systemAccountId, earliestDate, todayKey])
  const rowsByDate = rowsByStatDate(rows)
  return ranges.map((range) => {
    const aggregate = aggregateUsageRowsForRange(rowsByDate, range)
    return [
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
    ]
  })
}

async function insertAiPerformanceSummaryWindowRowsAsync(client: DatabaseClient, rows: unknown[][]): Promise<void> {
  for (const chunk of chunkValues(rows, 250)) {
    if (chunk.length === 0) continue
    const placeholders = chunk
      .map((row) => `(${row.map(() => '?').join(', ')})`)
      .join(', ')
    await client.execute(`
      INSERT INTO ${statsTable(client, 'ai_performance_summary_windows')} (
        system_account_id, window_key, start_date, end_date, request_count,
        duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum,
        first_token_ms_count, first_token_ms_max, updated_at
      ) VALUES ${placeholders}
    `, chunk.flat())
  }
}

function statsTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable('juhe_stats', tableName)
}

const usageStatsConsistencyMetrics = [
  'request_count',
  'success_count',
  'error_count',
  'input_tokens',
  'output_tokens',
  'cache_read_tokens',
  'cache_read_cost_usd',
  'cache_write_tokens',
  'cache_write_1h_tokens',
  'cache_write_cost_usd',
  'thinking_tokens',
  'input_image_tokens',
  'output_image_tokens',
  'total_cost_usd'
] as const

type UsageStatsConsistencyMetric = typeof usageStatsConsistencyMetrics[number]

export interface UsageStatsConsistencyIssue {
  systemAccountId: string
  scopeType: string
  scopeId: string
  statDate: string
  metric: UsageStatsConsistencyMetric
  dailyValue: number
  hourlyValue: number
}

export function checkUsageStatsConsistency(sampleLimit = 20): UsageStatsConsistencyIssue[] {
  const database = getStatsDatabase()
  const samples = database.prepare(`
    SELECT system_account_id, scope_type, scope_id, stat_date,
      request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
      cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens,
      total_cost_usd
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
        COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens,
        COALESCE(SUM(cache_write_1h_tokens), 0) AS cache_write_1h_tokens,
        COALESCE(SUM(cache_write_cost_usd), 0) AS cache_write_cost_usd,
        COALESCE(SUM(thinking_tokens), 0) AS thinking_tokens,
        COALESCE(SUM(input_image_tokens), 0) AS input_image_tokens,
        COALESCE(SUM(output_image_tokens), 0) AS output_image_tokens,
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
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
      cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens,
      total_cost_usd AS total_cost,
      duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count, last_used_at
    FROM usage_overview_summary_windows
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
  `).get(statsScope.scopeId, statsScope.systemAccountId, windowKey, range.startDate, range.endDate) as unknown as AccountUsageAggregateRow & StatsAggregateMathRow | undefined

  const hourlyRows = database.prepare(`
    SELECT bucket_key AS stat_hour, request_count, error_count, input_tokens, output_tokens, cache_read_tokens,
      cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
      thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd AS total_cost, duration_ms_sum, duration_ms_count
    FROM usage_overview_trend_windows
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
    ORDER BY bucket_key ASC
  `).all(statsScope.systemAccountId, windowKey, range.startDate, range.endDate) as unknown as Array<StatsAggregateMathRow & { stat_hour: string; error_count: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_read_cost_usd: number; cache_write_tokens?: number; cache_write_1h_tokens?: number; cache_write_cost_usd?: number; thinking_tokens?: number; input_image_tokens?: number; output_image_tokens?: number; total_cost: number }>

  const modelRows = database.prepare(`
    SELECT provider_code, model,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
      cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens,
      total_cost_usd AS total_cost
    FROM usage_model_rank_windows
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
    ORDER BY rank ASC
  `).all(statsScope.systemAccountId, windowKey, range.startDate, range.endDate) as unknown as Array<{ provider_code: string; model: string; request_count: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_read_cost_usd: number; cache_write_tokens?: number; cache_write_1h_tokens?: number; cache_write_cost_usd?: number; thinking_tokens?: number; input_image_tokens?: number; output_image_tokens?: number; total_cost: number }>

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
      cacheReadTokens: Number(row.cache_read_tokens ?? 0),
      cacheWriteTokens: Number(row.cache_write_tokens ?? 0),
      cacheWrite1hTokens: Number(row.cache_write_1h_tokens ?? 0),
      cacheWriteCost: Number(row.cache_write_cost_usd ?? 0),
      thinkingTokens: Number(row.thinking_tokens ?? 0),
      inputImageTokens: Number(row.input_image_tokens ?? 0),
      outputImageTokens: Number(row.output_image_tokens ?? 0),
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

export async function getUsageStatsOverviewAsync(access?: AccessScope, range: AccountUsageStatsRange = normalizeDefaultUsageStatsRange()): Promise<UsageStatsOverview> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getUsageStatsOverview(access, range)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const statsScope = usageOverviewStatsScope(access)
  const windowKey = rangeWindowKey(range)

  const summaryRow = await client.one<AccountUsageAggregateRow & StatsAggregateMathRow>(`
    SELECT ? AS account_id, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
      cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens,
      total_cost_usd AS total_cost,
      duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count, last_used_at
    FROM juhe_stats.usage_overview_summary_windows
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
  `, [statsScope.scopeId, statsScope.systemAccountId, windowKey, range.startDate, range.endDate])

  const hourlyRows = await client.query<StatsAggregateMathRow & { stat_hour: string; error_count: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_read_cost_usd: number; cache_write_tokens?: number; cache_write_1h_tokens?: number; cache_write_cost_usd?: number; thinking_tokens?: number; input_image_tokens?: number; output_image_tokens?: number; total_cost: number }>(`
    SELECT bucket_key AS stat_hour, request_count, error_count, input_tokens, output_tokens, cache_read_tokens,
      cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
      thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd AS total_cost, duration_ms_sum, duration_ms_count
    FROM juhe_stats.usage_overview_trend_windows
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
    ORDER BY bucket_key ASC
  `, [statsScope.systemAccountId, windowKey, range.startDate, range.endDate])

  const modelRows = await client.query<{ provider_code: string; model: string; request_count: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_read_cost_usd: number; cache_write_tokens?: number; cache_write_1h_tokens?: number; cache_write_cost_usd?: number; thinking_tokens?: number; input_image_tokens?: number; output_image_tokens?: number; total_cost: number }>(`
    SELECT provider_code, model,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
      cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens,
      total_cost_usd AS total_cost
    FROM juhe_stats.usage_model_rank_windows
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
    ORDER BY rank ASC
  `, [statsScope.systemAccountId, windowKey, range.startDate, range.endDate])

  const errorRows = await client.query<{ provider_code: string; error_code: string; status_code: number; error_message: string | null; error_count: number }>(`
    SELECT provider_code, error_code, status_code, error_message, error_count
    FROM juhe_stats.usage_error_rank_windows
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
    ORDER BY rank ASC
  `, [statsScope.systemAccountId, windowKey, range.startDate, range.endDate])

  return {
    range,
    summary: usageSummaryWithMath(summaryRow ?? emptyStatsAggregateMathRow()),
    hourlyTrend: mapUsageTrendRows(hourlyRows),
    modelDistribution: modelRows.map((row) => ({
      providerCode: row.provider_code,
      model: row.model,
      requestCount: Number(row.request_count ?? 0),
      totalTokens: Number(row.input_tokens ?? 0) + Number(row.output_tokens ?? 0),
      cacheReadTokens: Number(row.cache_read_tokens ?? 0),
      cacheWriteTokens: Number(row.cache_write_tokens ?? 0),
      cacheWrite1hTokens: Number(row.cache_write_1h_tokens ?? 0),
      cacheWriteCost: Number(row.cache_write_cost_usd ?? 0),
      thinkingTokens: Number(row.thinking_tokens ?? 0),
      inputImageTokens: Number(row.input_image_tokens ?? 0),
      outputImageTokens: Number(row.output_image_tokens ?? 0),
      totalCost: Number(row.total_cost ?? 0)
    })),
    errors: errorRows.map((row) => ({
      providerCode: row.provider_code,
      errorCode: row.error_code,
      statusCode: row.status_code || undefined,
      errorMessage: row.error_message ?? undefined,
      errorCount: Number(row.error_count ?? 0)
    })),
    statsLagSeconds: await latestUsageStatsLagSecondsFromClientAsync(client)
  }
}

export async function latestUsageStatsLagSecondsForRuntime(): Promise<number | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return latestUsageStatsLagSeconds()
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return latestUsageStatsLagSecondsFromClientAsync(client)
}

function mapUsageTrendRows(
  rows: Array<StatsAggregateMathRow & { stat_hour: string; error_count: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_read_cost_usd: number; cache_write_tokens?: number; cache_write_1h_tokens?: number; cache_write_cost_usd?: number; thinking_tokens?: number; input_image_tokens?: number; output_image_tokens?: number; total_cost: number }>,
): UsageStatsOverview['hourlyTrend'] {
  return rows.map((row) => ({
    statHour: row.stat_hour,
    requestCount: Number(row.request_count ?? 0),
    totalTokens: Number(row.input_tokens ?? 0) + Number(row.output_tokens ?? 0),
    cacheReadTokens: Number(row.cache_read_tokens ?? 0),
    cacheWriteTokens: Number(row.cache_write_tokens ?? 0),
    cacheWrite1hTokens: Number(row.cache_write_1h_tokens ?? 0),
    cacheWriteCost: Number(row.cache_write_cost_usd ?? 0),
    thinkingTokens: Number(row.thinking_tokens ?? 0),
    inputImageTokens: Number(row.input_image_tokens ?? 0),
    outputImageTokens: Number(row.output_image_tokens ?? 0),
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

async function latestUsageStatsLagSecondsFromClientAsync(client: DatabaseClient): Promise<number | undefined> {
  const shardRow = await client.one<{ lag_seconds?: number | string | null }>(`
    SELECT MAX(lag_seconds) AS lag_seconds
    FROM juhe_stats.stats_job_state
    WHERE scope_type = 'usage_shard' AND job_name = 'usage_stats_aggregation'
  `)
  const shardLag = numberOrUndefined(shardRow?.lag_seconds)
  if (shardLag !== undefined) {
    return shardLag
  }
  const row = await client.one<{ lag_seconds?: number | string | null }>(`
    SELECT lag_seconds
    FROM juhe_stats.stats_job_state
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'
  `)
  return numberOrUndefined(row?.lag_seconds)
}

function numberOrUndefined(value: number | string | null | undefined): number | undefined {
  const number = Number(value)
  return Number.isFinite(number) ? number : undefined
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

interface ConsistencyStatsRow extends Record<UsageStatsConsistencyMetric, number> {
  systemAccountId: string
  scopeType: string
  scopeId: string
  statDate: string
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
    cache_write_tokens: Number(row.cache_write_tokens ?? 0),
    cache_write_1h_tokens: Number(row.cache_write_1h_tokens ?? 0),
    cache_write_cost_usd: Number(row.cache_write_cost_usd ?? 0),
    thinking_tokens: Number(row.thinking_tokens ?? 0),
    input_image_tokens: Number(row.input_image_tokens ?? 0),
    output_image_tokens: Number(row.output_image_tokens ?? 0),
    total_cost_usd: Number(row.total_cost_usd ?? 0)
  }
}

function compareConsistencyRows(daily: ConsistencyStatsRow, hourly: ConsistencyStatsRow): UsageStatsConsistencyIssue[] {
  const issues: UsageStatsConsistencyIssue[] = []
  for (const metric of usageStatsConsistencyMetrics) {
    const dailyValue = daily[metric]
    const hourlyValue = hourly[metric]
    const tolerance = consistencyMetricTolerance(metric)
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

function consistencyMetricTolerance(metric: UsageStatsConsistencyMetric): number {
  return metric === 'total_cost_usd' || metric === 'cache_read_cost_usd' || metric === 'cache_write_cost_usd'
    ? 0.000001
    : 0
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
