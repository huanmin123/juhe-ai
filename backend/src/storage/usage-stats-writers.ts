import type { DatabaseSync } from 'node:sqlite'

import { estimateProviderCacheReadCostUsd } from '../modules/model-pricing/model-pricing.service.js'
import { getBusinessDatabase } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { shouldAggregateUsageStatsRecord, usageStatsEntries, type UsageStatsAuthorizationLookup } from './usage-stats-aggregation.js'
import { subtractAuthorizationUsageReportRows, upsertAuthorizationUsageReportRows } from './usage-stats-authorization-daily-writer.js'
import { subtractAccountQualityMinuteStats, upsertAccountQualityMinuteStats } from './usage-stats-account-quality-writer.js'
import { subtractUsageErrorBuckets, upsertUsageErrorBuckets } from './usage-stats-error-writer.js'
import { subtractUsageLatencyEntry, upsertUsageLatencyEntry } from './usage-stats-latency-writer.js'
import { subtractUsageModelBuckets, upsertUsageModelBuckets } from './usage-stats-model-writer.js'
import { usageStatsTimeBuckets, usageStatsTimeKeys, type UsageStatsTimeBucketDefinition, type UsageStatsTimeKeys } from './usage-stats-time-buckets.js'
import { statsParamsTail, statsSubtractParams } from './usage-stats-writer-params.js'
import { updateUsageRecordCacheReadCost } from './usage-record-shards.js'
import type { UsageStatsAccumulator, UsageStatsEntry, UsageStatsRecordRow } from './usage-stats-types.js'

export interface UsageStatsAggregationContext extends UsageStatsAuthorizationLookup {
  usageStatsUpsertStatements?: UsageStatsUpsertStatements
  accountAuthorizationResourceIds?: Map<string, string>
  accountAuthorizationInstanceAccountIds?: Map<string, string>
}

type SqliteStatement = ReturnType<DatabaseSync['prepare']>

interface UsageStatsUpsertStatements {
  database: DatabaseSync
  total: SqliteStatement
  timeBuckets: Map<string, SqliteStatement>
}

export function createUsageStatsAggregationContext(rows: UsageStatsRecordRow[]): UsageStatsAggregationContext {
  const context: UsageStatsAggregationContext = {
    accountAuthorizationResourceIds: new Map(),
    accountAuthorizationInstanceAccountIds: new Map()
  }
  extendUsageStatsAggregationContext(context, rows)
  return context
}

export function extendUsageStatsAggregationContext(context: UsageStatsAggregationContext, rows: UsageStatsRecordRow[]): UsageStatsAggregationContext {
  const accountAuthorizationIds = uniqueIds(rows.map((row) => row.account_authorization_id))
    .filter((id) => !context.accountAuthorizationResourceIds?.has(id))
  if (!context.accountAuthorizationResourceIds) {
    context.accountAuthorizationResourceIds = new Map()
  }
  if (!context.accountAuthorizationInstanceAccountIds) {
    context.accountAuthorizationInstanceAccountIds = new Map()
  }
  if (!accountAuthorizationIds.length) {
    return context
  }
  const lookup = loadUsageStatsAccountAuthorizationLookup(accountAuthorizationIds)
  for (const [id, resourceId] of lookup.accountAuthorizationResourceIds) {
    context.accountAuthorizationResourceIds.set(id, resourceId)
  }
  for (const [id, instanceAccountId] of lookup.accountAuthorizationInstanceAccountIds ?? new Map<string, string>()) {
    context.accountAuthorizationInstanceAccountIds.set(id, instanceAccountId)
  }
  return context
}

function loadUsageStatsAccountAuthorizationLookup(accountAuthorizationIds: string[]): UsageStatsAuthorizationLookup & { accountAuthorizationResourceIds: Map<string, string> } {
  const accountAuthorizationResourceIds = new Map<string, string>()
  const accountAuthorizationInstanceAccountIds = new Map<string, string>()
  if (!accountAuthorizationIds.length) {
    return { accountAuthorizationResourceIds, accountAuthorizationInstanceAccountIds }
  }
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(accountAuthorizationIds, 900)) {
    const rows = database.prepare(`
      SELECT
        authorizations.id,
        authorizations.resource_id,
        instance_accounts.id AS instance_account_id
      FROM resource_authorizations authorizations
      LEFT JOIN accounts instance_accounts
        ON instance_accounts.authorization_instance_authorization_id = authorizations.id
        AND instance_accounts.system_account_id = authorizations.grantee_system_account_id
      WHERE authorizations.resource_type = 'account'
        AND authorizations.id IN (${sqlPlaceholders(chunk.length)})
    `).all(...chunk) as unknown as Array<{
      id?: string | null
      resource_id?: string | null
      instance_account_id?: string | null
    }>
    for (const row of rows) {
      if (!row.id) continue
      if (row.resource_id) {
        accountAuthorizationResourceIds.set(row.id, row.resource_id)
      }
      if (row.instance_account_id) {
        accountAuthorizationInstanceAccountIds.set(row.id, row.instance_account_id)
      }
    }
  }
  return { accountAuthorizationResourceIds, accountAuthorizationInstanceAccountIds }
}

function uniqueIds(values: Array<string | null | undefined>): string[] {
  return [...new Set(values.map((value) => value?.trim()).filter((value): value is string => Boolean(value)))]
}

export function aggregateUsageStatsRecord(database: DatabaseSync, row: UsageStatsRecordRow, updatedAt: string, context?: UsageStatsAggregationContext): void {
  if (!shouldAggregateUsageStatsRecord(row)) {
    return
  }
  persistEstimatedCacheReadCost(row)

  const timeKeys = usageStatsTimeKeys(row)
  for (const entry of usageStatsEntries(row, context)) {
    upsertUsageStatsEntry(database, entry, timeKeys, updatedAt, context)
    upsertUsageLatencyEntry(database, entry, row, timeKeys, updatedAt)
  }
  upsertAuthorizationUsageReportRows(database, row, timeKeys.statDate, updatedAt, context)
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

  const timeKeys = usageStatsTimeKeys(row)
  const context = createUsageStatsAggregationContext([row])
  for (const entry of usageStatsEntries(row, context)) {
    subtractUsageStatsEntry(database, entry, timeKeys, updatedAt)
    subtractUsageLatencyEntry(database, entry, row, timeKeys, updatedAt)
  }
  subtractAuthorizationUsageReportRows(database, row, timeKeys.statDate, updatedAt, context)
  subtractUsageModelBuckets(database, row, timeKeys, updatedAt)
  if (row.success !== 1) {
    subtractUsageErrorBuckets(database, row, timeKeys, updatedAt)
  }
  subtractAccountQualityMinuteStats(database, row, updatedAt)
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

function prepareUsageStatsTimeBucketUpsertStatement(database: DatabaseSync, bucket: UsageStatsTimeBucketDefinition): SqliteStatement {
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

function upsertUsageStatsTimeBucket(database: DatabaseSync, bucket: UsageStatsTimeBucketDefinition, timeValue: string, entry: UsageStatsEntry, updatedAt: string, statement = prepareUsageStatsTimeBucketUpsertStatement(database, bucket)): void {
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

function subtractUsageStatsTimeBucket(database: DatabaseSync, bucket: UsageStatsTimeBucketDefinition, timeValue: string, entry: UsageStatsEntry, updatedAt: string): void {
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

function deleteEmptyUsageStatsTotal(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string): void {
  database.prepare(`
    DELETE FROM usage_stats_totals
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0 AND total_cost_usd = 0
  `).run(systemAccountId, scopeType, scopeId)
}

function deleteEmptyUsageStatsTimeBucket(database: DatabaseSync, bucket: UsageStatsTimeBucketDefinition, timeValue: string, systemAccountId: string, scopeType: string, scopeId: string): void {
  database.prepare(`
    DELETE FROM ${bucket.tableName}
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND ${bucket.columnName} = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0 AND total_cost_usd = 0
  `).run(systemAccountId, scopeType, scopeId, timeValue)
}
