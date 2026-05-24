import type { DatabaseSync } from 'node:sqlite'

import { estimateProviderCacheReadCostUsd } from '../modules/model-pricing/model-pricing.service.js'
import { dateKey, hourKey, minuteKey, monthKey, usageStatsTimezone, weekKey } from './usage-stats-helpers.js'
import { shouldAggregateUsageStatsRecord, usageStatsAccumulatorFromRecord, usageStatsEntries } from './usage-stats-aggregation.js'
import { updateUsageRecordCacheReadCost } from './usage-record-shards.js'
import {
  GLOBAL_STATS_SYSTEM_ACCOUNT_ID,
  type UsageStatsAccumulator,
  type UsageStatsEntry,
  type UsageStatsRecordRow
} from './usage-stats-types.js'

interface UsageStatsTimeKeys {
  statMinute: string
  statHour: string
  statDate: string
  statWeek: string
  statMonth: string
}

interface TimeBucketDefinition {
  tableName: string
  columnName: string
  valueKey: keyof UsageStatsTimeKeys
}

type LatencyMetricType = 'duration_ms' | 'first_token_ms'
type AuthorizationReportResourceType = 'all' | 'account' | 'group'

interface AuthorizationReportRow {
  authorizationId: string
  ownerSystemAccountId: string
  granteeSystemAccountId: string
  resourceType: 'account' | 'group'
  resourceId: string
  hitAccountId: string
  hitAccountOwnerSystemAccountId: string
  sourceType?: string | null
  sourceTeamId?: string | null
}

interface AuthorizationReportResourceFilter {
  resourceFilterType: AuthorizationReportResourceType
  resourceFilterId: string
}

interface AuthorizationReportSummaryKey {
  teamFilterId?: string
  granteeFilterSystemAccountId?: string
  resourceFilterType: AuthorizationReportResourceType
  resourceFilterId: string
}

export interface UsageStatsAggregationContext {
  usageStatsUpsertStatements?: UsageStatsUpsertStatements
}

type SqliteStatement = ReturnType<DatabaseSync['prepare']>

interface UsageStatsUpsertStatements {
  database: DatabaseSync
  total: SqliteStatement
  timeBuckets: Map<string, SqliteStatement>
}

const usageStatsTimeBuckets: TimeBucketDefinition[] = [
  { tableName: 'usage_stats_minute', columnName: 'stat_minute', valueKey: 'statMinute' },
  { tableName: 'usage_stats_hourly', columnName: 'stat_hour', valueKey: 'statHour' },
  { tableName: 'usage_stats_daily', columnName: 'stat_date', valueKey: 'statDate' },
  { tableName: 'usage_stats_weekly', columnName: 'stat_week', valueKey: 'statWeek' },
  { tableName: 'usage_stats_monthly', columnName: 'stat_month', valueKey: 'statMonth' }
]

const usageModelTimeBuckets: TimeBucketDefinition[] = [
  { tableName: 'usage_model_minute', columnName: 'stat_minute', valueKey: 'statMinute' },
  { tableName: 'usage_model_hourly', columnName: 'stat_hour', valueKey: 'statHour' },
  { tableName: 'usage_model_daily', columnName: 'stat_date', valueKey: 'statDate' },
  { tableName: 'usage_model_weekly', columnName: 'stat_week', valueKey: 'statWeek' },
  { tableName: 'usage_model_monthly', columnName: 'stat_month', valueKey: 'statMonth' }
]

const usageErrorTimeBuckets: TimeBucketDefinition[] = [
  { tableName: 'usage_error_minute', columnName: 'stat_minute', valueKey: 'statMinute' },
  { tableName: 'usage_error_hourly', columnName: 'stat_hour', valueKey: 'statHour' },
  { tableName: 'usage_error_daily', columnName: 'stat_date', valueKey: 'statDate' },
  { tableName: 'usage_error_weekly', columnName: 'stat_week', valueKey: 'statWeek' },
  { tableName: 'usage_error_monthly', columnName: 'stat_month', valueKey: 'statMonth' }
]

const usageLatencyTimeBuckets: TimeBucketDefinition[] = [
  { tableName: 'usage_latency_minute', columnName: 'stat_minute', valueKey: 'statMinute' },
  { tableName: 'usage_latency_hourly', columnName: 'stat_hour', valueKey: 'statHour' },
  { tableName: 'usage_latency_daily', columnName: 'stat_date', valueKey: 'statDate' },
  { tableName: 'usage_latency_weekly', columnName: 'stat_week', valueKey: 'statWeek' },
  { tableName: 'usage_latency_monthly', columnName: 'stat_month', valueKey: 'statMonth' }
]

const latencyBucketUpperBoundsMs = [100, 250, 500, 1000, 2000, 5000, 10000, 30000, 60000, -1] as const

export function createUsageStatsAggregationContext(rows: UsageStatsRecordRow[]): UsageStatsAggregationContext {
  void rows
  return {}
}

export function aggregateUsageStatsRecord(database: DatabaseSync, row: UsageStatsRecordRow, updatedAt: string, context?: UsageStatsAggregationContext): void {
  if (!shouldAggregateUsageStatsRecord(row)) {
    return
  }
  persistEstimatedCacheReadCost(row)

  const timeKeys = usageStatsTimeKeys(database, row)
  for (const entry of usageStatsEntries(row)) {
    upsertUsageStatsEntry(database, entry, timeKeys, updatedAt, context)
    upsertUsageLatencyEntry(database, entry, row, timeKeys, updatedAt)
  }
  upsertAuthorizationUsageReportRows(database, row, timeKeys, updatedAt)
  upsertUsageModelBuckets(database, row, timeKeys, updatedAt)
  if (row.success !== 1) {
    upsertUsageErrorBuckets(database, row, timeKeys, updatedAt)
  }
  upsertAccountQualityMinuteStats(database, row, updatedAt)
}

export function subtractUsageStatsRecord(database: DatabaseSync, row: UsageStatsRecordRow, updatedAt: string): void {
  if (!shouldAggregateUsageStatsRecord(row)) {
    return
  }

  const timeKeys = usageStatsTimeKeys(database, row)
  for (const entry of usageStatsEntries(row)) {
    subtractUsageStatsEntry(database, entry, timeKeys, updatedAt)
    subtractUsageLatencyEntry(database, entry, row, timeKeys, updatedAt)
  }
  subtractAuthorizationUsageReportRows(database, row, timeKeys, updatedAt)
  subtractUsageModelBuckets(database, row, timeKeys, updatedAt)
  if (row.success !== 1) {
    subtractUsageErrorBuckets(database, row, timeKeys, updatedAt)
  }
  subtractAccountQualityMinuteStats(database, row, updatedAt)
}

function usageStatsTimeKeys(database: DatabaseSync, row: UsageStatsRecordRow): UsageStatsTimeKeys {
  const createdAt = new Date(row.created_at)
  const timezone = usageStatsTimezone()
  return {
    statMinute: minuteKey(createdAt, timezone),
    statHour: hourKey(createdAt, timezone),
    statDate: dateKey(createdAt, timezone),
    statWeek: weekKey(createdAt, timezone),
    statMonth: monthKey(createdAt, timezone)
  }
}

function persistEstimatedCacheReadCost(row: UsageStatsRecordRow): void {
  if (row.cache_read_cost_usd !== null && row.cache_read_cost_usd !== undefined) {
    return
  }
  const cacheReadCostUsd = estimateProviderCacheReadCostUsd({
    providerCode: row.provider_code ?? '',
    model: row.model ?? undefined,
    cacheReadTokens: row.cache_read_tokens ?? undefined
  }) ?? 0
  if (cacheReadCostUsd <= 0) {
    return
  }
  updateUsageRecordCacheReadCost({
    id: row.id,
    createdAt: row.created_at,
    sourceShardKey: row.source_shard_key ?? undefined,
    cacheReadCostUsd
  })
  row.cache_read_cost_usd = cacheReadCostUsd
}

function upsertUsageStatsEntry(database: DatabaseSync, entry: UsageStatsEntry, timeKeys: UsageStatsTimeKeys, updatedAt: string, context?: UsageStatsAggregationContext): void {
  const statements = usageStatsUpsertStatementsFor(database, context)
  upsertUsageStatsTotal(database, entry.systemAccountId, entry.scopeType, entry.scopeId, entry.accumulator, updatedAt, statements?.total)
  for (const bucket of usageStatsTimeBuckets) {
    upsertUsageStatsTimeBucket(database, bucket, timeKeys[bucket.valueKey], entry, updatedAt, statements?.timeBuckets.get(bucket.tableName))
  }
}

function subtractUsageStatsEntry(database: DatabaseSync, entry: UsageStatsEntry, timeKeys: UsageStatsTimeKeys, updatedAt: string): void {
  subtractUsageStatsTotal(database, entry.systemAccountId, entry.scopeType, entry.scopeId, entry.accumulator, updatedAt)
  for (const bucket of usageStatsTimeBuckets) {
    subtractUsageStatsTimeBucket(database, bucket, timeKeys[bucket.valueKey], entry, updatedAt)
  }
}

function usageStatsUpsertStatementsFor(database: DatabaseSync, context?: UsageStatsAggregationContext): UsageStatsUpsertStatements | undefined {
  if (!context) return undefined
  const cached = context.usageStatsUpsertStatements
  if (cached?.database === database) {
    return cached
  }
  const statements: UsageStatsUpsertStatements = {
    database,
    total: prepareUsageStatsTotalUpsertStatement(database),
    timeBuckets: new Map(usageStatsTimeBuckets.map((bucket) => [bucket.tableName, prepareUsageStatsTimeBucketUpsertStatement(database, bucket)]))
  }
  context.usageStatsUpsertStatements = statements
  return statements
}

function prepareUsageStatsTotalUpsertStatement(database: DatabaseSync): SqliteStatement {
  return database.prepare(`
    INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max, last_used_at, last_error_at, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      cache_read_cost_usd = cache_read_cost_usd + excluded.cache_read_cost_usd,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum,
      duration_ms_count = duration_ms_count + excluded.duration_ms_count,
      duration_ms_max = MAX(duration_ms_max, excluded.duration_ms_max),
      first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
      first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
      first_token_ms_max = MAX(first_token_ms_max, excluded.first_token_ms_max),
      last_used_at = CASE WHEN excluded.last_used_at IS NULL THEN usage_stats_totals.last_used_at WHEN usage_stats_totals.last_used_at IS NULL OR excluded.last_used_at > usage_stats_totals.last_used_at THEN excluded.last_used_at ELSE usage_stats_totals.last_used_at END,
      last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN usage_stats_totals.last_error_at WHEN usage_stats_totals.last_error_at IS NULL OR excluded.last_error_at > usage_stats_totals.last_error_at THEN excluded.last_error_at ELSE usage_stats_totals.last_error_at END,
      updated_at = excluded.updated_at
  `)
}

function upsertUsageStatsTotal(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string, stats: UsageStatsAccumulator, updatedAt: string, statement = prepareUsageStatsTotalUpsertStatement(database)): void {
  statement.run(systemAccountId, scopeType, scopeId, ...statsParamsTail(stats, updatedAt))
}

function prepareUsageStatsTimeBucketUpsertStatement(database: DatabaseSync, bucket: TimeBucketDefinition): SqliteStatement {
  const { tableName, columnName } = bucket
  return database.prepare(`
    INSERT INTO ${tableName} (system_account_id, scope_type, scope_id, ${columnName}, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max, last_used_at, last_error_at, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id, ${columnName}) DO UPDATE SET
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      cache_read_cost_usd = cache_read_cost_usd + excluded.cache_read_cost_usd,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum,
      duration_ms_count = duration_ms_count + excluded.duration_ms_count,
      duration_ms_max = MAX(duration_ms_max, excluded.duration_ms_max),
      first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
      first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
      first_token_ms_max = MAX(first_token_ms_max, excluded.first_token_ms_max),
      last_used_at = CASE WHEN excluded.last_used_at IS NULL THEN ${tableName}.last_used_at WHEN ${tableName}.last_used_at IS NULL OR excluded.last_used_at > ${tableName}.last_used_at THEN excluded.last_used_at ELSE ${tableName}.last_used_at END,
      last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN ${tableName}.last_error_at WHEN ${tableName}.last_error_at IS NULL OR excluded.last_error_at > ${tableName}.last_error_at THEN excluded.last_error_at ELSE ${tableName}.last_error_at END,
      updated_at = excluded.updated_at
  `)
}

function upsertUsageStatsTimeBucket(database: DatabaseSync, bucket: TimeBucketDefinition, timeValue: string, entry: UsageStatsEntry, updatedAt: string, statement = prepareUsageStatsTimeBucketUpsertStatement(database, bucket)): void {
  statement.run(entry.systemAccountId, entry.scopeType, entry.scopeId, timeValue, ...statsParamsTail(entry.accumulator, updatedAt))
}

function subtractUsageStatsTotal(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    UPDATE usage_stats_totals
    SET request_count = MAX(0, request_count - ?),
        success_count = MAX(0, success_count - ?),
        error_count = MAX(0, error_count - ?),
        input_tokens = MAX(0, input_tokens - ?),
        output_tokens = MAX(0, output_tokens - ?),
        cache_read_tokens = MAX(0, cache_read_tokens - ?),
        cache_read_cost_usd = MAX(0, cache_read_cost_usd - ?),
        total_cost_usd = MAX(0, total_cost_usd - ?),
        duration_ms_sum = MAX(0, duration_ms_sum - ?),
        duration_ms_count = MAX(0, duration_ms_count - ?),
        duration_ms_max = CASE WHEN duration_ms_count <= ? THEN 0 ELSE duration_ms_max END,
        first_token_ms_sum = MAX(0, first_token_ms_sum - ?),
        first_token_ms_count = MAX(0, first_token_ms_count - ?),
        first_token_ms_max = CASE WHEN first_token_ms_count <= ? THEN 0 ELSE first_token_ms_max END,
        last_used_at = CASE WHEN request_count <= ? THEN NULL ELSE last_used_at END,
        last_error_at = CASE WHEN error_count <= ? THEN NULL ELSE last_error_at END,
        updated_at = ?
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
  `).run(...statsSubtractParams(stats), updatedAt, systemAccountId, scopeType, scopeId)
  deleteEmptyUsageStatsTotal(database, systemAccountId, scopeType, scopeId)
}

function subtractUsageStatsTimeBucket(database: DatabaseSync, bucket: TimeBucketDefinition, timeValue: string, entry: UsageStatsEntry, updatedAt: string): void {
  const { tableName, columnName } = bucket
  database.prepare(`
    UPDATE ${tableName}
    SET request_count = MAX(0, request_count - ?),
        success_count = MAX(0, success_count - ?),
        error_count = MAX(0, error_count - ?),
        input_tokens = MAX(0, input_tokens - ?),
        output_tokens = MAX(0, output_tokens - ?),
        cache_read_tokens = MAX(0, cache_read_tokens - ?),
        cache_read_cost_usd = MAX(0, cache_read_cost_usd - ?),
        total_cost_usd = MAX(0, total_cost_usd - ?),
        duration_ms_sum = MAX(0, duration_ms_sum - ?),
        duration_ms_count = MAX(0, duration_ms_count - ?),
        duration_ms_max = CASE WHEN duration_ms_count <= ? THEN 0 ELSE duration_ms_max END,
        first_token_ms_sum = MAX(0, first_token_ms_sum - ?),
        first_token_ms_count = MAX(0, first_token_ms_count - ?),
        first_token_ms_max = CASE WHEN first_token_ms_count <= ? THEN 0 ELSE first_token_ms_max END,
        last_used_at = CASE WHEN request_count <= ? THEN NULL ELSE last_used_at END,
        last_error_at = CASE WHEN error_count <= ? THEN NULL ELSE last_error_at END,
        updated_at = ?
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND ${columnName} = ?
  `).run(...statsSubtractParams(entry.accumulator), updatedAt, entry.systemAccountId, entry.scopeType, entry.scopeId, timeValue)
  deleteEmptyUsageStatsTimeBucket(database, bucket, timeValue, entry.systemAccountId, entry.scopeType, entry.scopeId)
}

function statsParamsTail(stats: UsageStatsAccumulator, updatedAt: string): Array<number | string | null> {
  return [
    stats.requestCount,
    stats.successCount,
    stats.errorCount,
    stats.inputTokens,
    stats.outputTokens,
    stats.cacheReadTokens,
    stats.cacheReadCostUsd,
    stats.totalCostUsd,
    stats.durationMsSum,
    stats.durationMsCount,
    stats.durationMsMax,
    stats.firstTokenMsSum,
    stats.firstTokenMsCount,
    stats.firstTokenMsMax,
    stats.lastUsedAt ?? null,
    stats.lastErrorAt ?? null,
    updatedAt
  ]
}

function statsSubtractParams(stats: UsageStatsAccumulator): number[] {
  return [
    stats.requestCount,
    stats.successCount,
    stats.errorCount,
    stats.inputTokens,
    stats.outputTokens,
    stats.cacheReadTokens,
    stats.cacheReadCostUsd,
    stats.totalCostUsd,
    stats.durationMsSum,
    stats.durationMsCount,
    stats.durationMsCount,
    stats.firstTokenMsSum,
    stats.firstTokenMsCount,
    stats.firstTokenMsCount,
    stats.requestCount,
    stats.errorCount
  ]
}

function deleteEmptyUsageStatsTotal(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string): void {
  database.prepare(`
    DELETE FROM usage_stats_totals
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0 AND total_cost_usd = 0
  `).run(systemAccountId, scopeType, scopeId)
}

function deleteEmptyUsageStatsTimeBucket(database: DatabaseSync, bucket: TimeBucketDefinition, timeValue: string, systemAccountId: string, scopeType: string, scopeId: string): void {
  database.prepare(`
    DELETE FROM ${bucket.tableName}
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND ${bucket.columnName} = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0 AND total_cost_usd = 0
  `).run(systemAccountId, scopeType, scopeId, timeValue)
}

function upsertAuthorizationUsageReportRows(database: DatabaseSync, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys, updatedAt: string): void {
  const stats = usageStatsAccumulatorFromRecord(row)
  for (const reportRow of authorizationReportRows(row)) {
    const reportScopeRows = authorizationReportScopeRows(reportRow)
    const filters = authorizationReportResourceFilters(reportRow)
    for (const scopedReportRow of reportScopeRows) {
      upsertAuthorizationSummaryRows(database, scopedReportRow, filters, stats, timeKeys.statDate, updatedAt)
    }
  }
}

function subtractAuthorizationUsageReportRows(database: DatabaseSync, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys, updatedAt: string): void {
  const stats = usageStatsAccumulatorFromRecord(row)
  for (const reportRow of authorizationReportRows(row)) {
    const reportScopeRows = authorizationReportScopeRows(reportRow)
    const filters = authorizationReportResourceFilters(reportRow)
    for (const scopedReportRow of reportScopeRows) {
      subtractAuthorizationSummaryRows(database, scopedReportRow, filters, stats, timeKeys.statDate, updatedAt)
    }
  }
}

function authorizationReportRows(row: UsageStatsRecordRow): AuthorizationReportRow[] {
  const rows: AuthorizationReportRow[] = []
  const seen = new Set<string>()
  if (row.account_authorization_id && row.account_id && row.account_owner_system_account_id && row.account_owner_system_account_id !== row.system_account_id) {
    addAuthorizationReportRow(rows, seen, {
      authorizationId: `account:${row.account_authorization_id}`,
      ownerSystemAccountId: row.account_owner_system_account_id,
      granteeSystemAccountId: row.system_account_id,
      resourceType: 'account',
      resourceId: row.account_id,
      hitAccountId: row.account_id,
      hitAccountOwnerSystemAccountId: row.account_owner_system_account_id,
      sourceType: row.account_authorization_source_type,
      sourceTeamId: row.account_authorization_source_team_id
    })
  }
  if (row.group_authorization_id && row.group_id && row.group_owner_system_account_id && row.group_owner_system_account_id !== row.system_account_id) {
    addAuthorizationReportRow(rows, seen, {
      authorizationId: `group:${row.group_authorization_id}`,
      ownerSystemAccountId: row.group_owner_system_account_id,
      granteeSystemAccountId: row.system_account_id,
      resourceType: 'group',
      resourceId: row.group_id,
      hitAccountId: row.account_id ?? '',
      hitAccountOwnerSystemAccountId: row.account_owner_system_account_id ?? row.group_owner_system_account_id,
      sourceType: row.group_authorization_source_type,
      sourceTeamId: row.group_authorization_source_team_id
    })
  }
  return rows
}

function addAuthorizationReportRow(rows: AuthorizationReportRow[], seen: Set<string>, row: AuthorizationReportRow): void {
  const key = row.authorizationId
  if (seen.has(key)) return
  seen.add(key)
  rows.push(row)
}

function authorizationReportScopeRows(row: AuthorizationReportRow): AuthorizationReportRow[] {
  return row.ownerSystemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID
    ? [row]
    : [row, { ...row, ownerSystemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID }]
}

function authorizationReportResourceFilters(row: AuthorizationReportRow): AuthorizationReportResourceFilter[] {
  return [
    { resourceFilterType: 'all', resourceFilterId: '' },
    { resourceFilterType: row.resourceType, resourceFilterId: '' },
    { resourceFilterType: row.resourceType, resourceFilterId: row.resourceId }
  ]
}

function upsertAuthorizationSummaryRows(
  database: DatabaseSync,
  row: AuthorizationReportRow,
  filters: AuthorizationReportResourceFilter[],
  stats: UsageStatsAccumulator,
  statDate: string,
  updatedAt: string
): void {
  const userSummaryKeys: AuthorizationReportSummaryKey[] = []
  const teamSummaryKeys: AuthorizationReportSummaryKey[] = []
  for (const filter of filters) {
    userSummaryKeys.push({ teamFilterId: '', granteeFilterSystemAccountId: '', ...filter })
    userSummaryKeys.push({ teamFilterId: '', granteeFilterSystemAccountId: row.granteeSystemAccountId, ...filter })
    if (row.sourceType === 'team' && row.sourceTeamId) {
      teamSummaryKeys.push({ teamFilterId: '', ...filter })
      teamSummaryKeys.push({ teamFilterId: row.sourceTeamId, ...filter })
      userSummaryKeys.push({ teamFilterId: row.sourceTeamId, granteeFilterSystemAccountId: '', ...filter })
      userSummaryKeys.push({ teamFilterId: row.sourceTeamId, granteeFilterSystemAccountId: row.granteeSystemAccountId, ...filter })
    }
  }
  for (const key of teamSummaryKeys) {
    upsertAuthorizationTeamUsageSummaryRow(database, row.ownerSystemAccountId, statDate, key, stats, updatedAt)
  }
  for (const key of userSummaryKeys) {
    upsertAuthorizationUserUsageSummaryRow(database, row.ownerSystemAccountId, statDate, key, stats, updatedAt)
  }
}

function subtractAuthorizationSummaryRows(
  database: DatabaseSync,
  row: AuthorizationReportRow,
  filters: AuthorizationReportResourceFilter[],
  stats: UsageStatsAccumulator,
  statDate: string,
  updatedAt: string
): void {
  const userSummaryKeys: AuthorizationReportSummaryKey[] = []
  const teamSummaryKeys: AuthorizationReportSummaryKey[] = []
  for (const filter of filters) {
    userSummaryKeys.push({ teamFilterId: '', granteeFilterSystemAccountId: '', ...filter })
    userSummaryKeys.push({ teamFilterId: '', granteeFilterSystemAccountId: row.granteeSystemAccountId, ...filter })
    if (row.sourceType === 'team' && row.sourceTeamId) {
      teamSummaryKeys.push({ teamFilterId: '', ...filter })
      teamSummaryKeys.push({ teamFilterId: row.sourceTeamId, ...filter })
      userSummaryKeys.push({ teamFilterId: row.sourceTeamId, granteeFilterSystemAccountId: '', ...filter })
      userSummaryKeys.push({ teamFilterId: row.sourceTeamId, granteeFilterSystemAccountId: row.granteeSystemAccountId, ...filter })
    }
  }
  for (const key of teamSummaryKeys) {
    subtractAuthorizationTeamUsageSummaryRow(database, row.ownerSystemAccountId, statDate, key, stats, updatedAt)
  }
  for (const key of userSummaryKeys) {
    subtractAuthorizationUserUsageSummaryRow(database, row.ownerSystemAccountId, statDate, key, stats, updatedAt)
  }
}

function upsertAuthorizationTeamUsageSummaryRow(database: DatabaseSync, systemAccountId: string, statDate: string, key: AuthorizationReportSummaryKey, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    INSERT INTO authorization_team_usage_summary_daily (
      system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id, row_count,
      request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count, first_token_ms_max,
      last_used_at, last_error_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id) DO UPDATE SET
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      cache_read_cost_usd = cache_read_cost_usd + excluded.cache_read_cost_usd,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum,
      duration_ms_count = duration_ms_count + excluded.duration_ms_count,
      duration_ms_max = MAX(duration_ms_max, excluded.duration_ms_max),
      first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
      first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
      first_token_ms_max = MAX(first_token_ms_max, excluded.first_token_ms_max),
      last_used_at = CASE WHEN excluded.last_used_at IS NULL THEN authorization_team_usage_summary_daily.last_used_at WHEN authorization_team_usage_summary_daily.last_used_at IS NULL OR excluded.last_used_at > authorization_team_usage_summary_daily.last_used_at THEN excluded.last_used_at ELSE authorization_team_usage_summary_daily.last_used_at END,
      last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN authorization_team_usage_summary_daily.last_error_at WHEN authorization_team_usage_summary_daily.last_error_at IS NULL OR excluded.last_error_at > authorization_team_usage_summary_daily.last_error_at THEN excluded.last_error_at ELSE authorization_team_usage_summary_daily.last_error_at END,
      updated_at = excluded.updated_at
  `).run(systemAccountId, statDate, key.teamFilterId ?? '', key.resourceFilterType, key.resourceFilterId, ...statsParamsTail(stats, updatedAt))
}

function subtractAuthorizationTeamUsageSummaryRow(database: DatabaseSync, systemAccountId: string, statDate: string, key: AuthorizationReportSummaryKey, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    UPDATE authorization_team_usage_summary_daily
    SET request_count = MAX(0, request_count - ?),
        success_count = MAX(0, success_count - ?),
        error_count = MAX(0, error_count - ?),
        input_tokens = MAX(0, input_tokens - ?),
        output_tokens = MAX(0, output_tokens - ?),
        cache_read_tokens = MAX(0, cache_read_tokens - ?),
        cache_read_cost_usd = MAX(0, cache_read_cost_usd - ?),
        total_cost_usd = MAX(0, total_cost_usd - ?),
        duration_ms_sum = MAX(0, duration_ms_sum - ?),
        duration_ms_count = MAX(0, duration_ms_count - ?),
        duration_ms_max = CASE WHEN duration_ms_count <= ? THEN 0 ELSE duration_ms_max END,
        first_token_ms_sum = MAX(0, first_token_ms_sum - ?),
        first_token_ms_count = MAX(0, first_token_ms_count - ?),
        first_token_ms_max = CASE WHEN first_token_ms_count <= ? THEN 0 ELSE first_token_ms_max END,
        last_used_at = CASE WHEN request_count <= ? THEN NULL ELSE last_used_at END,
        last_error_at = CASE WHEN error_count <= ? THEN NULL ELSE last_error_at END,
        updated_at = ?
    WHERE system_account_id = ? AND stat_date = ? AND team_filter_id = ? AND resource_filter_type = ? AND resource_filter_id = ?
  `).run(...statsSubtractParams(stats), updatedAt, systemAccountId, statDate, key.teamFilterId ?? '', key.resourceFilterType, key.resourceFilterId)
  deleteEmptyAuthorizationTeamUsageSummaryRow(database, systemAccountId, statDate, key)
}

function upsertAuthorizationUserUsageSummaryRow(database: DatabaseSync, systemAccountId: string, statDate: string, key: AuthorizationReportSummaryKey, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    INSERT INTO authorization_user_usage_summary_daily (
      system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id, row_count,
      request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count, first_token_ms_max,
      last_used_at, last_error_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id) DO UPDATE SET
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      cache_read_cost_usd = cache_read_cost_usd + excluded.cache_read_cost_usd,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum,
      duration_ms_count = duration_ms_count + excluded.duration_ms_count,
      duration_ms_max = MAX(duration_ms_max, excluded.duration_ms_max),
      first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
      first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
      first_token_ms_max = MAX(first_token_ms_max, excluded.first_token_ms_max),
      last_used_at = CASE WHEN excluded.last_used_at IS NULL THEN authorization_user_usage_summary_daily.last_used_at WHEN authorization_user_usage_summary_daily.last_used_at IS NULL OR excluded.last_used_at > authorization_user_usage_summary_daily.last_used_at THEN excluded.last_used_at ELSE authorization_user_usage_summary_daily.last_used_at END,
      last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN authorization_user_usage_summary_daily.last_error_at WHEN authorization_user_usage_summary_daily.last_error_at IS NULL OR excluded.last_error_at > authorization_user_usage_summary_daily.last_error_at THEN excluded.last_error_at ELSE authorization_user_usage_summary_daily.last_error_at END,
      updated_at = excluded.updated_at
  `).run(systemAccountId, statDate, key.teamFilterId ?? '', key.granteeFilterSystemAccountId ?? '', key.resourceFilterType, key.resourceFilterId, ...statsParamsTail(stats, updatedAt))
}

function subtractAuthorizationUserUsageSummaryRow(database: DatabaseSync, systemAccountId: string, statDate: string, key: AuthorizationReportSummaryKey, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    UPDATE authorization_user_usage_summary_daily
    SET request_count = MAX(0, request_count - ?),
        success_count = MAX(0, success_count - ?),
        error_count = MAX(0, error_count - ?),
        input_tokens = MAX(0, input_tokens - ?),
        output_tokens = MAX(0, output_tokens - ?),
        cache_read_tokens = MAX(0, cache_read_tokens - ?),
        cache_read_cost_usd = MAX(0, cache_read_cost_usd - ?),
        total_cost_usd = MAX(0, total_cost_usd - ?),
        duration_ms_sum = MAX(0, duration_ms_sum - ?),
        duration_ms_count = MAX(0, duration_ms_count - ?),
        duration_ms_max = CASE WHEN duration_ms_count <= ? THEN 0 ELSE duration_ms_max END,
        first_token_ms_sum = MAX(0, first_token_ms_sum - ?),
        first_token_ms_count = MAX(0, first_token_ms_count - ?),
        first_token_ms_max = CASE WHEN first_token_ms_count <= ? THEN 0 ELSE first_token_ms_max END,
        last_used_at = CASE WHEN request_count <= ? THEN NULL ELSE last_used_at END,
        last_error_at = CASE WHEN error_count <= ? THEN NULL ELSE last_error_at END,
        updated_at = ?
    WHERE system_account_id = ? AND stat_date = ? AND team_filter_id = ? AND grantee_filter_system_account_id = ? AND resource_filter_type = ? AND resource_filter_id = ?
  `).run(...statsSubtractParams(stats), updatedAt, systemAccountId, statDate, key.teamFilterId ?? '', key.granteeFilterSystemAccountId ?? '', key.resourceFilterType, key.resourceFilterId)
  deleteEmptyAuthorizationUserUsageSummaryRow(database, systemAccountId, statDate, key)
}

function deleteEmptyAuthorizationTeamUsageSummaryRow(database: DatabaseSync, systemAccountId: string, statDate: string, key: AuthorizationReportSummaryKey): void {
  database.prepare(`
    DELETE FROM authorization_team_usage_summary_daily
    WHERE system_account_id = ? AND stat_date = ? AND team_filter_id = ? AND resource_filter_type = ? AND resource_filter_id = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0 AND total_cost_usd = 0
  `).run(systemAccountId, statDate, key.teamFilterId ?? '', key.resourceFilterType, key.resourceFilterId)
}

function deleteEmptyAuthorizationUserUsageSummaryRow(database: DatabaseSync, systemAccountId: string, statDate: string, key: AuthorizationReportSummaryKey): void {
  database.prepare(`
    DELETE FROM authorization_user_usage_summary_daily
    WHERE system_account_id = ? AND stat_date = ? AND team_filter_id = ? AND grantee_filter_system_account_id = ? AND resource_filter_type = ? AND resource_filter_id = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0 AND total_cost_usd = 0
  `).run(systemAccountId, statDate, key.teamFilterId ?? '', key.granteeFilterSystemAccountId ?? '', key.resourceFilterType, key.resourceFilterId)
}

function upsertAccountQualityMinuteStats(database: DatabaseSync, row: UsageStatsRecordRow, updatedAt: string): void {
  if (!row.account_id || !row.api_key_id) {
    return
  }
  const createdAt = new Date(row.created_at)
  const statMinute = minuteKey(createdAt, usageStatsTimezone())
  const success = row.success === 1
  const firstTokenMsValue = Number(row.first_token_ms ?? NaN)
  const hasFirstTokenSample = success && Number.isFinite(firstTokenMsValue) && firstTokenMsValue >= 0
  const firstTokenMs = hasFirstTokenSample ? firstTokenMsValue : 0
  const firstTokenCount = hasFirstTokenSample ? 1 : 0
  database.prepare(`
    INSERT INTO account_quality_minute_stats (
      account_id, system_account_id, provider_code, stat_minute,
      request_count, success_count, error_count, first_token_ms_sum, first_token_ms_count,
      last_sample_at, last_success_at, last_error_at, last_error_message, updated_at
    )
    VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(account_id, stat_minute) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      provider_code = excluded.provider_code,
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
      first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
      last_sample_at = CASE WHEN account_quality_minute_stats.last_sample_at IS NULL OR excluded.last_sample_at > account_quality_minute_stats.last_sample_at THEN excluded.last_sample_at ELSE account_quality_minute_stats.last_sample_at END,
      last_success_at = CASE WHEN excluded.last_success_at IS NULL THEN account_quality_minute_stats.last_success_at WHEN account_quality_minute_stats.last_success_at IS NULL OR excluded.last_success_at > account_quality_minute_stats.last_success_at THEN excluded.last_success_at ELSE account_quality_minute_stats.last_success_at END,
      last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN account_quality_minute_stats.last_error_at WHEN account_quality_minute_stats.last_error_at IS NULL OR excluded.last_error_at > account_quality_minute_stats.last_error_at THEN excluded.last_error_at ELSE account_quality_minute_stats.last_error_at END,
      last_error_message = CASE WHEN excluded.last_error_at IS NULL THEN account_quality_minute_stats.last_error_message WHEN account_quality_minute_stats.last_error_at IS NULL OR excluded.last_error_at >= account_quality_minute_stats.last_error_at THEN excluded.last_error_message ELSE account_quality_minute_stats.last_error_message END,
      updated_at = excluded.updated_at
  `).run(
    row.account_id,
    row.account_owner_system_account_id ?? row.system_account_id,
    row.provider_code ?? 'unknown',
    statMinute,
    success ? 1 : 0,
    success ? 0 : 1,
    firstTokenMs,
    firstTokenCount,
    row.created_at,
    success ? row.created_at : null,
    success ? null : row.created_at,
    success ? null : row.error_message ?? null,
    updatedAt
  )
}

function subtractAccountQualityMinuteStats(database: DatabaseSync, row: UsageStatsRecordRow, updatedAt: string): void {
  if (!row.account_id || !row.api_key_id) {
    return
  }
  const createdAt = new Date(row.created_at)
  const statMinute = minuteKey(createdAt, usageStatsTimezone())
  const success = row.success === 1
  const firstTokenMsValue = Number(row.first_token_ms ?? NaN)
  const hasFirstTokenSample = success && Number.isFinite(firstTokenMsValue) && firstTokenMsValue >= 0
  const firstTokenMs = hasFirstTokenSample ? firstTokenMsValue : 0
  const firstTokenCount = hasFirstTokenSample ? 1 : 0
  database.prepare(`
    UPDATE account_quality_minute_stats
    SET request_count = MAX(0, request_count - 1),
        success_count = MAX(0, success_count - ?),
        error_count = MAX(0, error_count - ?),
        first_token_ms_sum = MAX(0, first_token_ms_sum - ?),
        first_token_ms_count = MAX(0, first_token_ms_count - ?),
        updated_at = ?
    WHERE account_id = ? AND stat_minute = ?
  `).run(success ? 1 : 0, success ? 0 : 1, firstTokenMs, firstTokenCount, updatedAt, row.account_id, statMinute)
  database.prepare(`
    DELETE FROM account_quality_minute_stats
    WHERE account_id = ? AND stat_minute = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND first_token_ms_sum = 0 AND first_token_ms_count = 0
  `).run(row.account_id, statMinute)
}

function upsertUsageModelBuckets(database: DatabaseSync, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys, updatedAt: string): void {
  const model = row.model?.trim()
  if (!model) return
  const stats = usageStatsAccumulatorFromRecord(row)
  const providerCode = row.provider_code ?? 'unknown'
  for (const systemAccountId of [row.system_account_id, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    for (const bucket of usageModelTimeBuckets) {
      upsertUsageModelBucket(database, bucket, timeKeys[bucket.valueKey], systemAccountId, providerCode, model, stats, updatedAt)
    }
  }
}

function subtractUsageModelBuckets(database: DatabaseSync, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys, updatedAt: string): void {
  const model = row.model?.trim()
  if (!model) return
  const stats = usageStatsAccumulatorFromRecord(row)
  const providerCode = row.provider_code ?? 'unknown'
  for (const systemAccountId of [row.system_account_id, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    for (const bucket of usageModelTimeBuckets) {
      subtractUsageModelBucket(database, bucket, timeKeys[bucket.valueKey], systemAccountId, providerCode, model, stats, updatedAt)
    }
  }
}

function upsertUsageModelBucket(database: DatabaseSync, bucket: TimeBucketDefinition, timeValue: string, systemAccountId: string, providerCode: string, model: string, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    INSERT INTO ${bucket.tableName} (system_account_id, ${bucket.columnName}, provider_code, model, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, ${bucket.columnName}, provider_code, model) DO UPDATE SET
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      cache_read_cost_usd = cache_read_cost_usd + excluded.cache_read_cost_usd,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      updated_at = excluded.updated_at
  `).run(systemAccountId, timeValue, providerCode, model, stats.requestCount, stats.successCount, stats.errorCount, stats.inputTokens, stats.outputTokens, stats.cacheReadTokens, stats.cacheReadCostUsd, stats.totalCostUsd, updatedAt)
}

function subtractUsageModelBucket(database: DatabaseSync, bucket: TimeBucketDefinition, timeValue: string, systemAccountId: string, providerCode: string, model: string, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    UPDATE ${bucket.tableName}
    SET request_count = MAX(0, request_count - ?),
        success_count = MAX(0, success_count - ?),
        error_count = MAX(0, error_count - ?),
        input_tokens = MAX(0, input_tokens - ?),
        output_tokens = MAX(0, output_tokens - ?),
        cache_read_tokens = MAX(0, cache_read_tokens - ?),
        cache_read_cost_usd = MAX(0, cache_read_cost_usd - ?),
        total_cost_usd = MAX(0, total_cost_usd - ?),
        updated_at = ?
    WHERE system_account_id = ? AND ${bucket.columnName} = ? AND provider_code = ? AND model = ?
  `).run(stats.requestCount, stats.successCount, stats.errorCount, stats.inputTokens, stats.outputTokens, stats.cacheReadTokens, stats.cacheReadCostUsd, stats.totalCostUsd, updatedAt, systemAccountId, timeValue, providerCode, model)
  deleteEmptyUsageModelBucket(database, bucket, timeValue, systemAccountId, providerCode, model)
}

function deleteEmptyUsageModelBucket(database: DatabaseSync, bucket: TimeBucketDefinition, timeValue: string, systemAccountId: string, providerCode: string, model: string): void {
  database.prepare(`
    DELETE FROM ${bucket.tableName}
    WHERE system_account_id = ? AND ${bucket.columnName} = ? AND provider_code = ? AND model = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0 AND total_cost_usd = 0
  `).run(systemAccountId, timeValue, providerCode, model)
}

function upsertUsageErrorBuckets(database: DatabaseSync, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys, updatedAt: string): void {
  const errorGroup = row.provider_code ?? 'unknown'
  const providerCode = row.provider_code ?? 'unknown'
  const errorCode = row.error_code ?? String(row.status_code ?? 'unknown')
  for (const systemAccountId of [row.system_account_id, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    for (const bucket of usageErrorTimeBuckets) {
      upsertUsageErrorBucket(database, bucket, timeKeys[bucket.valueKey], systemAccountId, errorGroup, providerCode, errorCode, row.status_code ?? 0, row.error_message ?? null, updatedAt)
    }
  }
}

function subtractUsageErrorBuckets(database: DatabaseSync, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys, updatedAt: string): void {
  const errorGroup = row.provider_code ?? 'unknown'
  const providerCode = row.provider_code ?? 'unknown'
  const errorCode = row.error_code ?? String(row.status_code ?? 'unknown')
  const statusCode = row.status_code ?? 0
  for (const systemAccountId of [row.system_account_id, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    for (const bucket of usageErrorTimeBuckets) {
      subtractUsageErrorBucket(database, bucket, timeKeys[bucket.valueKey], systemAccountId, errorGroup, providerCode, errorCode, statusCode, updatedAt)
    }
  }
}

function upsertUsageErrorBucket(database: DatabaseSync, bucket: TimeBucketDefinition, timeValue: string, systemAccountId: string, errorGroup: string, providerCode: string, errorCode: string, statusCode: number, errorMessage: string | null, updatedAt: string): void {
  database.prepare(`
    INSERT INTO ${bucket.tableName} (system_account_id, ${bucket.columnName}, error_group, provider_code, error_code, status_code, error_message, request_count, error_count, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1, ?)
    ON CONFLICT(system_account_id, ${bucket.columnName}, error_group, provider_code, error_code, status_code) DO UPDATE SET
      error_message = COALESCE(excluded.error_message, ${bucket.tableName}.error_message),
      request_count = request_count + excluded.request_count,
      error_count = error_count + excluded.error_count,
      updated_at = excluded.updated_at
  `).run(systemAccountId, timeValue, errorGroup, providerCode, errorCode, statusCode, errorMessage, updatedAt)
}

function subtractUsageErrorBucket(database: DatabaseSync, bucket: TimeBucketDefinition, timeValue: string, systemAccountId: string, errorGroup: string, providerCode: string, errorCode: string, statusCode: number, updatedAt: string): void {
  database.prepare(`
    UPDATE ${bucket.tableName}
    SET request_count = MAX(0, request_count - 1),
        error_count = MAX(0, error_count - 1),
        updated_at = ?
    WHERE system_account_id = ? AND ${bucket.columnName} = ? AND error_group = ? AND provider_code = ? AND error_code = ? AND status_code = ?
  `).run(updatedAt, systemAccountId, timeValue, errorGroup, providerCode, errorCode, statusCode)
  deleteEmptyUsageErrorBucket(database, bucket, timeValue, systemAccountId, errorGroup, providerCode, errorCode, statusCode)
}

function deleteEmptyUsageErrorBucket(database: DatabaseSync, bucket: TimeBucketDefinition, timeValue: string, systemAccountId: string, errorGroup: string, providerCode: string, errorCode: string, statusCode: number): void {
  database.prepare(`
    DELETE FROM ${bucket.tableName}
    WHERE system_account_id = ? AND ${bucket.columnName} = ? AND error_group = ? AND provider_code = ? AND error_code = ? AND status_code = ?
      AND request_count = 0 AND error_count = 0
  `).run(systemAccountId, timeValue, errorGroup, providerCode, errorCode, statusCode)
}

function upsertUsageLatencyEntry(database: DatabaseSync, entry: UsageStatsEntry, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys, updatedAt: string): void {
  const metrics = latencySamples(row)
  for (const metric of metrics) {
    for (const bucket of usageLatencyTimeBuckets) {
      upsertUsageLatencyBucket(database, bucket, timeKeys[bucket.valueKey], entry, metric.metricType, metric.bucketUpperBoundMs, updatedAt)
    }
  }
}

function subtractUsageLatencyEntry(database: DatabaseSync, entry: UsageStatsEntry, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys, updatedAt: string): void {
  const metrics = latencySamples(row)
  for (const metric of metrics) {
    for (const bucket of usageLatencyTimeBuckets) {
      subtractUsageLatencyBucket(database, bucket, timeKeys[bucket.valueKey], entry, metric.metricType, metric.bucketUpperBoundMs, updatedAt)
    }
  }
}

function latencySamples(row: UsageStatsRecordRow): Array<{ metricType: LatencyMetricType; bucketUpperBoundMs: number }> {
  const samples: Array<{ metricType: LatencyMetricType; bucketUpperBoundMs: number }> = []
  const durationMs = finiteNonNegativeNumber(row.duration_ms)
  if (durationMs !== undefined) {
    samples.push({ metricType: 'duration_ms', bucketUpperBoundMs: latencyBucketUpperBound(durationMs) })
  }
  const firstTokenMs = finiteNonNegativeNumber(row.first_token_ms)
  if (firstTokenMs !== undefined) {
    samples.push({ metricType: 'first_token_ms', bucketUpperBoundMs: latencyBucketUpperBound(firstTokenMs) })
  }
  return samples
}

function finiteNonNegativeNumber(value: unknown): number | undefined {
  const number = Number(value ?? NaN)
  return Number.isFinite(number) && number >= 0 ? number : undefined
}

function latencyBucketUpperBound(value: number): number {
  return latencyBucketUpperBoundsMs.find((upperBound) => upperBound === -1 || value <= upperBound) ?? -1
}

function upsertUsageLatencyBucket(database: DatabaseSync, bucket: TimeBucketDefinition, timeValue: string, entry: UsageStatsEntry, metricType: LatencyMetricType, bucketUpperBoundMs: number, updatedAt: string): void {
  database.prepare(`
    INSERT INTO ${bucket.tableName} (system_account_id, scope_type, scope_id, metric_type, ${bucket.columnName}, bucket_upper_bound_ms, sample_count, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, 1, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id, metric_type, ${bucket.columnName}, bucket_upper_bound_ms) DO UPDATE SET
      sample_count = sample_count + excluded.sample_count,
      updated_at = excluded.updated_at
  `).run(entry.systemAccountId, entry.scopeType, entry.scopeId, metricType, timeValue, bucketUpperBoundMs, updatedAt)
}

function subtractUsageLatencyBucket(database: DatabaseSync, bucket: TimeBucketDefinition, timeValue: string, entry: UsageStatsEntry, metricType: LatencyMetricType, bucketUpperBoundMs: number, updatedAt: string): void {
  database.prepare(`
    UPDATE ${bucket.tableName}
    SET sample_count = MAX(0, sample_count - 1),
        updated_at = ?
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
      AND metric_type = ? AND ${bucket.columnName} = ? AND bucket_upper_bound_ms = ?
  `).run(updatedAt, entry.systemAccountId, entry.scopeType, entry.scopeId, metricType, timeValue, bucketUpperBoundMs)
  database.prepare(`
    DELETE FROM ${bucket.tableName}
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
      AND metric_type = ? AND ${bucket.columnName} = ? AND bucket_upper_bound_ms = ?
      AND sample_count = 0
  `).run(entry.systemAccountId, entry.scopeType, entry.scopeId, metricType, timeValue, bucketUpperBoundMs)
}
