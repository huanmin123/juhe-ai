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
  if (isDeletedApiKeyRecord(database, row)) {
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
  if (row.model) {
    upsertUsageModelDaily(database, row, statDate, updatedAt)
    upsertUsageModelHourly(database, row, statHour, updatedAt)
  }
  if (row.success !== 1) {
    upsertUsageErrorDaily(database, row, statDate, updatedAt)
    upsertUsageErrorHourly(database, row, statHour, updatedAt)
  }
}

export function subtractUsageStatsRecord(database: DatabaseSync, row: UsageStatsRecordRow, updatedAt: string): void {
  if (!shouldAggregateUsageStatsRecord(row)) {
    return
  }

  const createdAt = new Date(row.created_at)
  const statDate = dateKey(createdAt)
  const statHour = hourKey(createdAt)
  for (const entry of usageStatsEntries(row)) {
    subtractUsageStatsTotal(database, entry.systemAccountId, entry.scopeType, entry.scopeId, entry.accumulator, updatedAt)
    subtractUsageStatsDaily(database, entry.systemAccountId, entry.scopeType, entry.scopeId, statDate, entry.accumulator, updatedAt)
    subtractUsageStatsHourly(database, entry.systemAccountId, entry.scopeType, entry.scopeId, statHour, entry.accumulator, updatedAt)
  }
  if (row.model) {
    subtractUsageModelDaily(database, row, statDate, updatedAt)
    subtractUsageModelHourly(database, row, statHour, updatedAt)
  }
  if (row.success !== 1) {
    subtractUsageErrorDaily(database, row, statDate, updatedAt)
    subtractUsageErrorHourly(database, row, statHour, updatedAt)
  }
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

function subtractUsageStatsTotal(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    UPDATE usage_stats_totals
    SET request_count = MAX(0, request_count - ?),
        success_count = MAX(0, success_count - ?),
        error_count = MAX(0, error_count - ?),
        input_tokens = MAX(0, input_tokens - ?),
        output_tokens = MAX(0, output_tokens - ?),
        cache_read_tokens = MAX(0, cache_read_tokens - ?),
        total_cost_usd = MAX(0, total_cost_usd - ?),
        duration_ms_sum = MAX(0, duration_ms_sum - ?),
        duration_ms_count = MAX(0, duration_ms_count - ?),
        first_token_ms_sum = MAX(0, first_token_ms_sum - ?),
        first_token_ms_count = MAX(0, first_token_ms_count - ?),
        last_used_at = CASE WHEN request_count <= ? THEN NULL ELSE last_used_at END,
        last_error_at = CASE WHEN error_count <= ? THEN NULL ELSE last_error_at END,
        updated_at = ?
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
  `).run(...statsCountParams(stats), stats.requestCount, stats.errorCount, updatedAt, systemAccountId, scopeType, scopeId)
  deleteEmptyUsageStatsTotal(database, systemAccountId, scopeType, scopeId)
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

function subtractUsageStatsDaily(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string, statDate: string, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    UPDATE usage_stats_daily
    SET request_count = MAX(0, request_count - ?),
        success_count = MAX(0, success_count - ?),
        error_count = MAX(0, error_count - ?),
        input_tokens = MAX(0, input_tokens - ?),
        output_tokens = MAX(0, output_tokens - ?),
        cache_read_tokens = MAX(0, cache_read_tokens - ?),
        total_cost_usd = MAX(0, total_cost_usd - ?),
        duration_ms_sum = MAX(0, duration_ms_sum - ?),
        duration_ms_count = MAX(0, duration_ms_count - ?),
        first_token_ms_sum = MAX(0, first_token_ms_sum - ?),
        first_token_ms_count = MAX(0, first_token_ms_count - ?),
        last_used_at = CASE WHEN request_count <= ? THEN NULL ELSE last_used_at END,
        last_error_at = CASE WHEN error_count <= ? THEN NULL ELSE last_error_at END,
        updated_at = ?
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_date = ?
  `).run(...statsCountParams(stats), stats.requestCount, stats.errorCount, updatedAt, systemAccountId, scopeType, scopeId, statDate)
  deleteEmptyUsageStatsDaily(database, systemAccountId, scopeType, scopeId, statDate)
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

function subtractUsageStatsHourly(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string, statHour: string, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    UPDATE usage_stats_hourly
    SET request_count = MAX(0, request_count - ?),
        success_count = MAX(0, success_count - ?),
        error_count = MAX(0, error_count - ?),
        input_tokens = MAX(0, input_tokens - ?),
        output_tokens = MAX(0, output_tokens - ?),
        cache_read_tokens = MAX(0, cache_read_tokens - ?),
        total_cost_usd = MAX(0, total_cost_usd - ?),
        duration_ms_sum = MAX(0, duration_ms_sum - ?),
        duration_ms_count = MAX(0, duration_ms_count - ?),
        first_token_ms_sum = MAX(0, first_token_ms_sum - ?),
        first_token_ms_count = MAX(0, first_token_ms_count - ?),
        updated_at = ?
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_hour = ?
  `).run(...statsCountParams(stats), updatedAt, systemAccountId, scopeType, scopeId, statHour)
  deleteEmptyUsageStatsHourly(database, systemAccountId, scopeType, scopeId, statHour)
}

function statsParamsTail(stats: UsageStatsAccumulator, updatedAt: string): Array<number | string | null> {
  return [stats.requestCount, stats.successCount, stats.errorCount, stats.inputTokens, stats.outputTokens, stats.cacheReadTokens, stats.totalCostUsd, stats.durationMsSum, stats.durationMsCount, stats.firstTokenMsSum, stats.firstTokenMsCount, stats.lastUsedAt ?? null, stats.lastErrorAt ?? null, updatedAt]
}

function statsCountParams(stats: UsageStatsAccumulator): number[] {
  return [stats.requestCount, stats.successCount, stats.errorCount, stats.inputTokens, stats.outputTokens, stats.cacheReadTokens, stats.totalCostUsd, stats.durationMsSum, stats.durationMsCount, stats.firstTokenMsSum, stats.firstTokenMsCount]
}

function isDeletedApiKeyRecord(database: DatabaseSync, row: UsageStatsRecordRow): boolean {
  if (!row.api_key_id) return false
  const apiKey = database.prepare('SELECT id FROM api_keys WHERE id = ?').get(row.api_key_id) as unknown as { id?: string } | undefined
  return !apiKey?.id
}

function deleteEmptyUsageStatsTotal(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string): void {
  database.prepare(`
    DELETE FROM usage_stats_totals
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND total_cost_usd = 0
  `).run(systemAccountId, scopeType, scopeId)
}

function deleteEmptyUsageStatsDaily(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string, statDate: string): void {
  database.prepare(`
    DELETE FROM usage_stats_daily
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_date = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND total_cost_usd = 0
  `).run(systemAccountId, scopeType, scopeId, statDate)
}

function deleteEmptyUsageStatsHourly(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string, statHour: string): void {
  database.prepare(`
    DELETE FROM usage_stats_hourly
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_hour = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND total_cost_usd = 0
  `).run(systemAccountId, scopeType, scopeId, statHour)
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

function subtractUsageModelDaily(database: DatabaseSync, row: UsageStatsRecordRow, statDate: string, updatedAt: string): void {
  const stats = usageStatsAccumulatorFromRecord(row)
  const providerCode = row.provider_code ?? 'unknown'
  const model = row.model ?? 'unknown'
  for (const systemAccountId of [row.system_account_id, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    database.prepare(`
      UPDATE usage_model_daily
      SET request_count = MAX(0, request_count - ?),
          success_count = MAX(0, success_count - ?),
          error_count = MAX(0, error_count - ?),
          input_tokens = MAX(0, input_tokens - ?),
          output_tokens = MAX(0, output_tokens - ?),
          cache_read_tokens = MAX(0, cache_read_tokens - ?),
          total_cost_usd = MAX(0, total_cost_usd - ?),
          updated_at = ?
      WHERE system_account_id = ? AND stat_date = ? AND provider_code = ? AND model = ?
    `).run(stats.requestCount, stats.successCount, stats.errorCount, stats.inputTokens, stats.outputTokens, stats.cacheReadTokens, stats.totalCostUsd, updatedAt, systemAccountId, statDate, providerCode, model)
    deleteEmptyUsageModelDaily(database, systemAccountId, statDate, providerCode, model)
  }
}

function upsertUsageModelHourly(database: DatabaseSync, row: UsageStatsRecordRow, statHour: string, updatedAt: string): void {
  const model = row.model?.trim()
  if (!model) return
  const stats = usageStatsAccumulatorFromRecord(row)
  for (const systemAccountId of [row.system_account_id, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    database.prepare(`
      INSERT INTO usage_model_hourly (system_account_id, stat_hour, provider_code, model, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, total_cost_usd, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(system_account_id, stat_hour, provider_code, model) DO UPDATE SET
        request_count = request_count + excluded.request_count,
        success_count = success_count + excluded.success_count,
        error_count = error_count + excluded.error_count,
        input_tokens = input_tokens + excluded.input_tokens,
        output_tokens = output_tokens + excluded.output_tokens,
        cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
        total_cost_usd = total_cost_usd + excluded.total_cost_usd,
        updated_at = excluded.updated_at
    `).run(systemAccountId, statHour, row.provider_code ?? 'unknown', model, stats.requestCount, stats.successCount, stats.errorCount, stats.inputTokens, stats.outputTokens, stats.cacheReadTokens, stats.totalCostUsd, updatedAt)
  }
}

function subtractUsageModelHourly(database: DatabaseSync, row: UsageStatsRecordRow, statHour: string, updatedAt: string): void {
  const model = row.model?.trim()
  if (!model) return
  const stats = usageStatsAccumulatorFromRecord(row)
  const providerCode = row.provider_code ?? 'unknown'
  for (const systemAccountId of [row.system_account_id, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    database.prepare(`
      UPDATE usage_model_hourly
      SET request_count = MAX(0, request_count - ?),
          success_count = MAX(0, success_count - ?),
          error_count = MAX(0, error_count - ?),
          input_tokens = MAX(0, input_tokens - ?),
          output_tokens = MAX(0, output_tokens - ?),
          cache_read_tokens = MAX(0, cache_read_tokens - ?),
          total_cost_usd = MAX(0, total_cost_usd - ?),
          updated_at = ?
      WHERE system_account_id = ? AND stat_hour = ? AND provider_code = ? AND model = ?
    `).run(stats.requestCount, stats.successCount, stats.errorCount, stats.inputTokens, stats.outputTokens, stats.cacheReadTokens, stats.totalCostUsd, updatedAt, systemAccountId, statHour, providerCode, model)
    deleteEmptyUsageModelHourly(database, systemAccountId, statHour, providerCode, model)
  }
}

function deleteEmptyUsageModelDaily(database: DatabaseSync, systemAccountId: string, statDate: string, providerCode: string, model: string): void {
  database.prepare(`
    DELETE FROM usage_model_daily
    WHERE system_account_id = ? AND stat_date = ? AND provider_code = ? AND model = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND total_cost_usd = 0
  `).run(systemAccountId, statDate, providerCode, model)
}

function deleteEmptyUsageModelHourly(database: DatabaseSync, systemAccountId: string, statHour: string, providerCode: string, model: string): void {
  database.prepare(`
    DELETE FROM usage_model_hourly
    WHERE system_account_id = ? AND stat_hour = ? AND provider_code = ? AND model = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND total_cost_usd = 0
  `).run(systemAccountId, statHour, providerCode, model)
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

function subtractUsageErrorDaily(database: DatabaseSync, row: UsageStatsRecordRow, statDate: string, updatedAt: string): void {
  const errorGroup = row.provider_code ?? 'unknown'
  const errorCode = row.error_code ?? String(row.status_code ?? 'unknown')
  for (const systemAccountId of [row.system_account_id, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    database.prepare(`
      UPDATE usage_error_daily
      SET request_count = MAX(0, request_count - 1),
          error_count = MAX(0, error_count - 1),
          updated_at = ?
      WHERE system_account_id = ? AND stat_date = ? AND error_group = ? AND error_code = ?
    `).run(updatedAt, systemAccountId, statDate, errorGroup, errorCode)
    deleteEmptyUsageErrorDaily(database, systemAccountId, statDate, errorGroup, errorCode)
  }
}

function upsertUsageErrorHourly(database: DatabaseSync, row: UsageStatsRecordRow, statHour: string, updatedAt: string): void {
  const errorGroup = row.provider_code ?? 'unknown'
  const errorCode = row.error_code ?? String(row.status_code ?? 'unknown')
  for (const systemAccountId of [row.system_account_id, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    database.prepare(`
      INSERT INTO usage_error_hourly (system_account_id, stat_hour, error_group, provider_code, error_code, status_code, error_message, request_count, error_count, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1, ?)
      ON CONFLICT(system_account_id, stat_hour, error_group, error_code) DO UPDATE SET
        provider_code = excluded.provider_code,
        status_code = excluded.status_code,
        error_message = COALESCE(excluded.error_message, usage_error_hourly.error_message),
        request_count = request_count + excluded.request_count,
        error_count = error_count + excluded.error_count,
        updated_at = excluded.updated_at
    `).run(systemAccountId, statHour, errorGroup, row.provider_code ?? 'unknown', errorCode, row.status_code ?? 0, row.error_message ?? null, updatedAt)
  }
}

function subtractUsageErrorHourly(database: DatabaseSync, row: UsageStatsRecordRow, statHour: string, updatedAt: string): void {
  const errorGroup = row.provider_code ?? 'unknown'
  const errorCode = row.error_code ?? String(row.status_code ?? 'unknown')
  for (const systemAccountId of [row.system_account_id, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    database.prepare(`
      UPDATE usage_error_hourly
      SET request_count = MAX(0, request_count - 1),
          error_count = MAX(0, error_count - 1),
          updated_at = ?
      WHERE system_account_id = ? AND stat_hour = ? AND error_group = ? AND error_code = ?
    `).run(updatedAt, systemAccountId, statHour, errorGroup, errorCode)
    deleteEmptyUsageErrorHourly(database, systemAccountId, statHour, errorGroup, errorCode)
  }
}

function deleteEmptyUsageErrorDaily(database: DatabaseSync, systemAccountId: string, statDate: string, errorGroup: string, errorCode: string): void {
  database.prepare(`
    DELETE FROM usage_error_daily
    WHERE system_account_id = ? AND stat_date = ? AND error_group = ? AND error_code = ?
      AND request_count = 0 AND error_count = 0
  `).run(systemAccountId, statDate, errorGroup, errorCode)
}

function deleteEmptyUsageErrorHourly(database: DatabaseSync, systemAccountId: string, statHour: string, errorGroup: string, errorCode: string): void {
  database.prepare(`
    DELETE FROM usage_error_hourly
    WHERE system_account_id = ? AND stat_hour = ? AND error_group = ? AND error_code = ?
      AND request_count = 0 AND error_count = 0
  `).run(systemAccountId, statHour, errorGroup, errorCode)
}
