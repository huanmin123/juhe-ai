import type { DatabaseSync } from 'node:sqlite'

import { usageStatsAccumulatorFromRecord } from './usage-stats-aggregation.js'
import { usageModelTimeBuckets, type UsageStatsTimeBucketDefinition, type UsageStatsTimeKeys } from './usage-stats-time-buckets.js'
import { GLOBAL_STATS_SYSTEM_ACCOUNT_ID, type UsageStatsAccumulator, type UsageStatsRecordRow } from './usage-stats-types.js'

export function upsertUsageModelBuckets(database: DatabaseSync, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys, updatedAt: string): void {
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

export function subtractUsageModelBuckets(database: DatabaseSync, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys, updatedAt: string): void {
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

function upsertUsageModelBucket(database: DatabaseSync, bucket: UsageStatsTimeBucketDefinition, timeValue: string, systemAccountId: string, providerCode: string, model: string, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    INSERT INTO ${bucket.tableName} (system_account_id, ${bucket.columnName}, provider_code, model, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
      thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, ${bucket.columnName}, provider_code, model) DO UPDATE SET
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      cache_read_cost_usd = cache_read_cost_usd + excluded.cache_read_cost_usd,
      cache_write_tokens = cache_write_tokens + excluded.cache_write_tokens,
      cache_write_1h_tokens = cache_write_1h_tokens + excluded.cache_write_1h_tokens,
      cache_write_cost_usd = cache_write_cost_usd + excluded.cache_write_cost_usd,
      thinking_tokens = thinking_tokens + excluded.thinking_tokens,
      input_image_tokens = input_image_tokens + excluded.input_image_tokens,
      output_image_tokens = output_image_tokens + excluded.output_image_tokens,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      updated_at = excluded.updated_at
  `).run(
    systemAccountId,
    timeValue,
    providerCode,
    model,
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
  )
}

function subtractUsageModelBucket(database: DatabaseSync, bucket: UsageStatsTimeBucketDefinition, timeValue: string, systemAccountId: string, providerCode: string, model: string, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    UPDATE ${bucket.tableName}
    SET request_count = MAX(0, request_count - ?),
        success_count = MAX(0, success_count - ?),
        error_count = MAX(0, error_count - ?),
        input_tokens = MAX(0, input_tokens - ?),
        output_tokens = MAX(0, output_tokens - ?),
        cache_read_tokens = MAX(0, cache_read_tokens - ?),
        cache_read_cost_usd = MAX(0, cache_read_cost_usd - ?),
        cache_write_tokens = MAX(0, cache_write_tokens - ?),
        cache_write_1h_tokens = MAX(0, cache_write_1h_tokens - ?),
        cache_write_cost_usd = MAX(0, cache_write_cost_usd - ?),
        thinking_tokens = MAX(0, thinking_tokens - ?),
        input_image_tokens = MAX(0, input_image_tokens - ?),
        output_image_tokens = MAX(0, output_image_tokens - ?),
        total_cost_usd = MAX(0, total_cost_usd - ?),
        updated_at = ?
    WHERE system_account_id = ? AND ${bucket.columnName} = ? AND provider_code = ? AND model = ?
  `).run(
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
    updatedAt,
    systemAccountId,
    timeValue,
    providerCode,
    model
  )
  deleteEmptyUsageModelBucket(database, bucket, timeValue, systemAccountId, providerCode, model)
}

function deleteEmptyUsageModelBucket(database: DatabaseSync, bucket: UsageStatsTimeBucketDefinition, timeValue: string, systemAccountId: string, providerCode: string, model: string): void {
  database.prepare(`
    DELETE FROM ${bucket.tableName}
    WHERE system_account_id = ? AND ${bucket.columnName} = ? AND provider_code = ? AND model = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0
      AND cache_write_tokens = 0 AND cache_write_1h_tokens = 0 AND cache_write_cost_usd = 0
      AND thinking_tokens = 0 AND input_image_tokens = 0 AND output_image_tokens = 0 AND total_cost_usd = 0
  `).run(systemAccountId, timeValue, providerCode, model)
}
