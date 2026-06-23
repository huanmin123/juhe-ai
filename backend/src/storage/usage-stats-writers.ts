import type { DatabaseSync } from 'node:sqlite'

import { estimateProviderCacheReadCostUsd } from '../modules/model-pricing/model-pricing.service.js'
import { getBusinessDatabase } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { shouldAggregateUsageStatsRecord, usageStatsAccumulatorFromRecord, usageStatsEntries, type UsageStatsAuthorizationLookup } from './usage-stats-aggregation.js'
import { subtractAuthorizationUsageReportRows, upsertAuthorizationUsageReportRows } from './usage-stats-authorization-daily-writer.js'
import { subtractAccountQualityMinuteStats, upsertAccountQualityMinuteStats } from './usage-stats-account-quality-writer.js'
import { subtractUsageErrorBuckets, upsertUsageErrorBuckets } from './usage-stats-error-writer.js'
import { subtractUsageLatencyEntry, upsertUsageLatencyEntry } from './usage-stats-latency-writer.js'
import { subtractUsageModelBuckets, upsertUsageModelBuckets } from './usage-stats-model-writer.js'
import { usageLatencyTimeBuckets, usageModelTimeBuckets, usageStatsTimeBuckets, usageStatsTimeKeys, type UsageStatsTimeBucketDefinition, type UsageStatsTimeKeys } from './usage-stats-time-buckets.js'
import { statsParamsTail, statsSubtractParams } from './usage-stats-writer-params.js'
import { GLOBAL_STATS_SYSTEM_ACCOUNT_ID, type UsageStatsAccumulator, type UsageStatsEntry, type UsageStatsRecordRow } from './usage-stats-types.js'

export interface UsageStatsAggregationContext extends UsageStatsAuthorizationLookup {
  usageStatsUpsertStatements?: UsageStatsUpsertStatements
  accountAuthorizationResourceIds?: Map<string, string>
  accountAuthorizationInstanceAccountIds?: Map<string, string>
}

type SqliteStatement = ReturnType<DatabaseSync['prepare']>
type LatencyMetricType = 'duration_ms' | 'first_token_ms'

interface UsageStatsUpsertStatements {
  database: DatabaseSync
  total: SqliteStatement
  timeBuckets: Map<string, SqliteStatement>
}

interface AggregatedUsageStatsEntry {
  systemAccountId: string
  scopeType: string
  scopeId: string
  accumulator: UsageStatsAccumulator
}

interface AggregatedUsageStatsTimeEntry extends AggregatedUsageStatsEntry {
  bucket: UsageStatsTimeBucketDefinition
  timeValue: string
}

interface AggregatedLatencyEntry {
  bucket: UsageStatsTimeBucketDefinition
  systemAccountId: string
  scopeType: string
  scopeId: string
  metricType: LatencyMetricType
  timeValue: string
  bucketUpperBoundMs: number
  sampleCount: number
}

interface AggregatedModelEntry {
  bucket: UsageStatsTimeBucketDefinition
  systemAccountId: string
  providerCode: string
  model: string
  timeValue: string
  accumulator: UsageStatsAccumulator
}

interface AggregatedAccountQualityEntry {
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

const latencyBucketUpperBoundsMs = [100, 250, 500, 1000, 2000, 5000, 10000, 30000, 60000, -1] as const

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
  if (shouldRecordAccountQualityStats(row)) {
    upsertAccountQualityMinuteStats(database, row, updatedAt)
  }
}

export function aggregateUsageStatsRecords(database: DatabaseSync, rows: UsageStatsRecordRow[], updatedAt: string, context?: UsageStatsAggregationContext): void {
  if (rows.length === 0) return
  const totalEntries = new Map<string, AggregatedUsageStatsEntry>()
  const timeEntries = new Map<string, AggregatedUsageStatsTimeEntry>()
  const latencyEntries = new Map<string, AggregatedLatencyEntry>()
  const modelEntries = new Map<string, AggregatedModelEntry>()
  const accountQualityEntries = new Map<string, AggregatedAccountQualityEntry>()

  for (const row of rows) {
    if (!shouldAggregateUsageStatsRecord(row)) {
      continue
    }
    persistEstimatedCacheReadCost(row)
    const timeKeys = usageStatsTimeKeys(row)
    for (const entry of usageStatsEntries(row, context)) {
      addAggregatedUsageStatsEntry(totalEntries, entry)
      for (const bucket of usageStatsTimeBuckets) {
        addAggregatedUsageStatsTimeEntry(timeEntries, bucket, timeKeys[bucket.valueKey], entry)
      }
      addAggregatedLatencyEntries(latencyEntries, entry, row, timeKeys)
    }
    addAggregatedUsageModelEntries(modelEntries, row, timeKeys)
    addAggregatedAccountQualityEntry(accountQualityEntries, row, timeKeys)
    if (row.account_authorization_id || row.group_authorization_id) {
      upsertAuthorizationUsageReportRows(database, row, timeKeys.statDate, updatedAt, context)
    }
    if (row.success !== 1) {
      upsertUsageErrorBuckets(database, row, timeKeys, updatedAt)
    }
  }

  const statements = usageStatsUpsertStatementsFor(database, context)
  for (const entry of totalEntries.values()) {
    upsertUsageStatsTotal(database, entry.systemAccountId, entry.scopeType, entry.scopeId, entry.accumulator, updatedAt, statements?.total)
  }
  for (const entry of timeEntries.values()) {
    upsertUsageStatsTimeBucket(database, entry.bucket, entry.timeValue, entry, updatedAt, statements?.timeBuckets.get(entry.bucket.tableName))
  }
  upsertAggregatedLatencyEntries(database, latencyEntries, updatedAt)
  upsertAggregatedModelEntries(database, modelEntries, updatedAt)
  upsertAggregatedAccountQualityEntries(database, accountQualityEntries, updatedAt)
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
  if (shouldRecordAccountQualityStats(row)) {
    subtractAccountQualityMinuteStats(database, row, updatedAt)
  }
}

function shouldRecordAccountQualityStats(row: UsageStatsRecordRow): boolean {
  return row.traffic_source !== 'cooldown_retest'
    && row.traffic_source !== 'hybrid_scoring'
    && row.traffic_source !== 'hybrid_quality_scoring'
}

function addAggregatedUsageStatsEntry(target: Map<string, AggregatedUsageStatsEntry>, entry: UsageStatsEntry): void {
  const key = usageStatsEntryKey(entry.systemAccountId, entry.scopeType, entry.scopeId)
  const existing = target.get(key)
  if (existing) {
    mergeAccumulator(existing.accumulator, entry.accumulator)
    return
  }
  target.set(key, {
    systemAccountId: entry.systemAccountId,
    scopeType: entry.scopeType,
    scopeId: entry.scopeId,
    accumulator: cloneAccumulator(entry.accumulator)
  })
}

function addAggregatedUsageStatsTimeEntry(target: Map<string, AggregatedUsageStatsTimeEntry>, bucket: UsageStatsTimeBucketDefinition, timeValue: string, entry: UsageStatsEntry): void {
  const key = `${bucket.tableName}\u0000${timeValue}\u0000${usageStatsEntryKey(entry.systemAccountId, entry.scopeType, entry.scopeId)}`
  const existing = target.get(key)
  if (existing) {
    mergeAccumulator(existing.accumulator, entry.accumulator)
    return
  }
  target.set(key, {
    bucket,
    timeValue,
    systemAccountId: entry.systemAccountId,
    scopeType: entry.scopeType,
    scopeId: entry.scopeId,
    accumulator: cloneAccumulator(entry.accumulator)
  })
}

function addAggregatedLatencyEntries(target: Map<string, AggregatedLatencyEntry>, entry: UsageStatsEntry, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys): void {
  for (const sample of latencySamples(row)) {
    for (const bucket of usageLatencyTimeBuckets) {
      const timeValue = timeKeys[bucket.valueKey]
      const key = `${bucket.tableName}\u0000${timeValue}\u0000${usageStatsEntryKey(entry.systemAccountId, entry.scopeType, entry.scopeId)}\u0000${sample.metricType}\u0000${sample.bucketUpperBoundMs}`
      const existing = target.get(key)
      if (existing) {
        existing.sampleCount += 1
        continue
      }
      target.set(key, {
        bucket,
        systemAccountId: entry.systemAccountId,
        scopeType: entry.scopeType,
        scopeId: entry.scopeId,
        metricType: sample.metricType,
        timeValue,
        bucketUpperBoundMs: sample.bucketUpperBoundMs,
        sampleCount: 1
      })
    }
  }
}

function addAggregatedUsageModelEntries(target: Map<string, AggregatedModelEntry>, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys): void {
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
        mergeAccumulator(existing.accumulator, accumulator)
        continue
      }
      target.set(key, {
        bucket,
        systemAccountId,
        providerCode,
        model,
        timeValue,
        accumulator: cloneAccumulator(accumulator)
      })
    }
  }
}

function addAggregatedAccountQualityEntry(target: Map<string, AggregatedAccountQualityEntry>, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys): void {
  if (!shouldRecordAccountQualityStats(row) || !row.account_id || !row.api_key_id) {
    return
  }
  const success = row.success === 1
  const firstTokenMsValue = Number(row.first_token_ms ?? NaN)
  const hasFirstTokenSample = success && Number.isFinite(firstTokenMsValue) && firstTokenMsValue >= 0
  const firstTokenMs = hasFirstTokenSample ? firstTokenMsValue : 0
  const statsSystemAccountId = accountQualityStatsSystemAccountId(row)
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
    existing.lastSuccessAt = maxIso(existing.lastSuccessAt, row.created_at)
  } else if (!existing.lastErrorAt || row.created_at >= existing.lastErrorAt) {
    existing.lastErrorAt = row.created_at
    existing.lastErrorMessage = row.error_message ?? undefined
  }
}

function upsertAggregatedLatencyEntries(database: DatabaseSync, entries: Map<string, AggregatedLatencyEntry>, updatedAt: string): void {
  const statements = new Map<string, SqliteStatement>()
  for (const entry of entries.values()) {
    const statement = statements.get(entry.bucket.tableName) ?? prepareUsageLatencyBucketCountUpsertStatement(database, entry.bucket)
    statements.set(entry.bucket.tableName, statement)
    statement.run(entry.systemAccountId, entry.scopeType, entry.scopeId, entry.metricType, entry.timeValue, entry.bucketUpperBoundMs, entry.sampleCount, updatedAt)
  }
}

function prepareUsageLatencyBucketCountUpsertStatement(database: DatabaseSync, bucket: UsageStatsTimeBucketDefinition): SqliteStatement {
  return database.prepare(`
    INSERT INTO ${bucket.tableName} (system_account_id, scope_type, scope_id, metric_type, ${bucket.columnName}, bucket_upper_bound_ms, sample_count, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id, metric_type, ${bucket.columnName}, bucket_upper_bound_ms) DO UPDATE SET
      sample_count = sample_count + excluded.sample_count,
      updated_at = excluded.updated_at
  `)
}

function upsertAggregatedModelEntries(database: DatabaseSync, entries: Map<string, AggregatedModelEntry>, updatedAt: string): void {
  const statements = new Map<string, SqliteStatement>()
  for (const entry of entries.values()) {
    const statement = statements.get(entry.bucket.tableName) ?? prepareUsageModelBucketAggregateUpsertStatement(database, entry.bucket)
    statements.set(entry.bucket.tableName, statement)
    const stats = entry.accumulator
    statement.run(
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
      stats.totalCostUsd,
      updatedAt
    )
  }
}

function prepareUsageModelBucketAggregateUpsertStatement(database: DatabaseSync, bucket: UsageStatsTimeBucketDefinition): SqliteStatement {
  return database.prepare(`
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
  `)
}

function upsertAggregatedAccountQualityEntries(database: DatabaseSync, entries: Map<string, AggregatedAccountQualityEntry>, updatedAt: string): void {
  if (entries.size === 0) return
  const upsertStatement = database.prepare(`
    INSERT INTO account_quality_minute_stats (
      account_id, system_account_id, provider_code, stat_minute,
      request_count, success_count, error_count, first_token_ms_sum, first_token_ms_count,
      last_sample_at, last_success_at, last_error_at, last_error_message, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
  `)
  const dirtyStatement = database.prepare(`
    INSERT INTO account_quality_dirty_accounts (account_id, first_dirty_at, updated_at)
    VALUES (?, ?, ?)
    ON CONFLICT(account_id) DO UPDATE SET
      updated_at = excluded.updated_at
  `)
  for (const entry of entries.values()) {
    upsertStatement.run(
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
    )
    dirtyStatement.run(entry.accountId, updatedAt, updatedAt)
  }
}

function accountQualityStatsSystemAccountId(row: UsageStatsRecordRow): string {
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

function usageStatsEntryKey(systemAccountId: string, scopeType: string, scopeId: string): string {
  return `${systemAccountId}\u0000${scopeType}\u0000${scopeId}`
}

function cloneAccumulator(accumulator: UsageStatsAccumulator): UsageStatsAccumulator {
  return { ...accumulator }
}

function mergeAccumulator(target: UsageStatsAccumulator, source: UsageStatsAccumulator): void {
  target.requestCount += source.requestCount
  target.successCount += source.successCount
  target.errorCount += source.errorCount
  target.inputTokens += source.inputTokens
  target.outputTokens += source.outputTokens
  target.cacheReadTokens += source.cacheReadTokens
  target.cacheReadCostUsd += source.cacheReadCostUsd
  target.totalCostUsd += source.totalCostUsd
  target.durationMsSum += source.durationMsSum
  target.durationMsCount += source.durationMsCount
  target.durationMsMax = Math.max(target.durationMsMax, source.durationMsMax)
  target.firstTokenMsSum += source.firstTokenMsSum
  target.firstTokenMsCount += source.firstTokenMsCount
  target.firstTokenMsMax = Math.max(target.firstTokenMsMax, source.firstTokenMsMax)
  target.lastUsedAt = maxIso(target.lastUsedAt, source.lastUsedAt)
  target.lastErrorAt = maxIso(target.lastErrorAt, source.lastErrorAt)
}

function maxIso(left?: string, right?: string): string | undefined {
  if (!left) return right
  if (!right) return left
  return left >= right ? left : right
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
