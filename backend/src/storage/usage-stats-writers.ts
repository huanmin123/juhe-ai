import type { DatabaseSync } from 'node:sqlite'

import { dateKey, hourKey } from './usage-stats-helpers.js'
import { shouldAggregateUsageStatsRecord, usageStatsAccumulatorFromRecord, usageStatsEntries } from './usage-stats-aggregation.js'
import {
  GLOBAL_STATS_SYSTEM_ACCOUNT_ID,
  type UsageStatsAccumulator,
  type UsageStatsRecordRow
} from './usage-stats-types.js'

export function aggregateUsageStatsRecord(database: DatabaseSync, row: UsageStatsRecordRow, updatedAt: string): void {
  if (!shouldAggregateUsageStatsRecord(row)) {
    return
  }

  const createdAt = new Date(row.created_at)
  const statDate = dateKey(createdAt)
  const statHour = hourKey(createdAt)
  for (const entry of usageStatsEntries(row)) {
    upsertUsageStatsTotal(database, entry.systemAccountId, entry.scopeType, entry.scopeId, entry.accumulator, updatedAt)
    upsertUsageStatsDaily(database, entry.systemAccountId, entry.scopeType, entry.scopeId, statDate, entry.accumulator, updatedAt)
    upsertUsageStatsHourly(database, entry.systemAccountId, entry.scopeType, entry.scopeId, statHour, entry.accumulator, updatedAt)
  }
  if (row.model) upsertUsageModelDaily(database, row, statDate, updatedAt)
  if (row.success !== 1) upsertUsageErrorDaily(database, row, statDate, updatedAt)
}

function upsertUsageStatsTotal(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, total_cost_usd, duration_ms_sum, duration_ms_count,
      first_token_ms_sum, first_token_ms_count, last_used_at, last_error_at, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum,
      duration_ms_count = duration_ms_count + excluded.duration_ms_count,
      first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
      first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
      last_used_at = CASE WHEN excluded.last_used_at IS NULL THEN usage_stats_totals.last_used_at WHEN usage_stats_totals.last_used_at IS NULL OR excluded.last_used_at > usage_stats_totals.last_used_at THEN excluded.last_used_at ELSE usage_stats_totals.last_used_at END,
      last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN usage_stats_totals.last_error_at WHEN usage_stats_totals.last_error_at IS NULL OR excluded.last_error_at > usage_stats_totals.last_error_at THEN excluded.last_error_at ELSE usage_stats_totals.last_error_at END,
      updated_at = excluded.updated_at
  `).run(systemAccountId, scopeType, scopeId, ...statsParamsTail(stats, updatedAt))
}

function upsertUsageStatsDaily(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string, statDate: string, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, total_cost_usd, duration_ms_sum, duration_ms_count,
      first_token_ms_sum, first_token_ms_count, last_used_at, last_error_at, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id, stat_date) DO UPDATE SET
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum,
      duration_ms_count = duration_ms_count + excluded.duration_ms_count,
      first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
      first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
      last_used_at = CASE WHEN excluded.last_used_at IS NULL THEN usage_stats_daily.last_used_at WHEN usage_stats_daily.last_used_at IS NULL OR excluded.last_used_at > usage_stats_daily.last_used_at THEN excluded.last_used_at ELSE usage_stats_daily.last_used_at END,
      last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN usage_stats_daily.last_error_at WHEN usage_stats_daily.last_error_at IS NULL OR excluded.last_error_at > usage_stats_daily.last_error_at THEN excluded.last_error_at ELSE usage_stats_daily.last_error_at END,
      updated_at = excluded.updated_at
  `).run(systemAccountId, scopeType, scopeId, statDate, ...statsParamsTail(stats, updatedAt))
}

function upsertUsageStatsHourly(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string, statHour: string, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    INSERT INTO usage_stats_hourly (system_account_id, scope_type, scope_id, stat_hour, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, total_cost_usd, duration_ms_sum, duration_ms_count,
      first_token_ms_sum, first_token_ms_count, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id, stat_hour) DO UPDATE SET
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum,
      duration_ms_count = duration_ms_count + excluded.duration_ms_count,
      first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
      first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
      updated_at = excluded.updated_at
  `).run(systemAccountId, scopeType, scopeId, statHour, stats.requestCount, stats.successCount, stats.errorCount, stats.inputTokens, stats.outputTokens, stats.cacheReadTokens, stats.totalCostUsd, stats.durationMsSum, stats.durationMsCount, stats.firstTokenMsSum, stats.firstTokenMsCount, updatedAt)
}

function statsParamsTail(stats: UsageStatsAccumulator, updatedAt: string): Array<number | string | null> {
  return [stats.requestCount, stats.successCount, stats.errorCount, stats.inputTokens, stats.outputTokens, stats.cacheReadTokens, stats.totalCostUsd, stats.durationMsSum, stats.durationMsCount, stats.firstTokenMsSum, stats.firstTokenMsCount, stats.lastUsedAt ?? null, stats.lastErrorAt ?? null, updatedAt]
}

function upsertUsageModelDaily(database: DatabaseSync, row: UsageStatsRecordRow, statDate: string, updatedAt: string): void {
  const stats = usageStatsAccumulatorFromRecord(row)
  for (const systemAccountId of [row.system_account_id, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    database.prepare(`
      INSERT INTO usage_model_daily (system_account_id, stat_date, provider_code, model, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, total_cost_usd, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(system_account_id, stat_date, provider_code, model) DO UPDATE SET
        request_count = request_count + excluded.request_count,
        success_count = success_count + excluded.success_count,
        error_count = error_count + excluded.error_count,
        input_tokens = input_tokens + excluded.input_tokens,
        output_tokens = output_tokens + excluded.output_tokens,
        cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
        total_cost_usd = total_cost_usd + excluded.total_cost_usd,
        updated_at = excluded.updated_at
    `).run(systemAccountId, statDate, row.provider_code ?? 'unknown', row.model ?? 'unknown', stats.requestCount, stats.successCount, stats.errorCount, stats.inputTokens, stats.outputTokens, stats.cacheReadTokens, stats.totalCostUsd, updatedAt)
  }
}

function upsertUsageErrorDaily(database: DatabaseSync, row: UsageStatsRecordRow, statDate: string, updatedAt: string): void {
  const errorGroup = row.provider_code ?? 'unknown'
  const errorCode = row.error_code ?? String(row.status_code ?? 'unknown')
  for (const systemAccountId of [row.system_account_id, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    database.prepare(`
      INSERT INTO usage_error_daily (system_account_id, stat_date, error_group, provider_code, error_code, status_code, error_message, request_count, error_count, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1, ?)
      ON CONFLICT(system_account_id, stat_date, error_group, error_code) DO UPDATE SET
        provider_code = excluded.provider_code,
        status_code = excluded.status_code,
        error_message = COALESCE(excluded.error_message, usage_error_daily.error_message),
        request_count = request_count + excluded.request_count,
        error_count = error_count + excluded.error_count,
        updated_at = excluded.updated_at
    `).run(systemAccountId, statDate, errorGroup, row.provider_code ?? 'unknown', errorCode, row.status_code ?? 0, row.error_message ?? null, updatedAt)
  }
}
