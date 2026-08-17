import type { DatabaseSync } from 'node:sqlite'

import { requiredRfc3339Instant } from '../shared/rfc3339.js'
import { minuteKey, usageStatsTimezone } from './usage-stats-helpers.js'
import { canonicalUsageStatsRecordCreatedAt, usageStatsRecordCreatedAt } from './usage-stats-time-buckets.js'
import type { UsageStatsRecordRow } from './usage-stats-types.js'

export function upsertAccountQualityMinuteStats(database: DatabaseSync, row: UsageStatsRecordRow, updatedAt: string): void {
  const createdAt = usageStatsRecordCreatedAt(row)
  const createdAtIso = canonicalUsageStatsRecordCreatedAt(row)
  const normalizedUpdatedAt = requiredRfc3339Instant(updatedAt, '账号质量统计 updatedAt')
  if (!row.account_id || !row.api_key_id) {
    return
  }
  const statMinute = minuteKey(createdAt, usageStatsTimezone())
  const success = row.success === 1
  const firstTokenMsValue = Number(row.first_token_ms ?? NaN)
  const hasFirstTokenSample = success && Number.isFinite(firstTokenMsValue) && firstTokenMsValue >= 0
  const firstTokenMs = hasFirstTokenSample ? firstTokenMsValue : 0
  const firstTokenCount = hasFirstTokenSample ? 1 : 0
  const statsSystemAccountId = accountQualityStatsSystemAccountId(row)
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
    statsSystemAccountId,
    row.provider_code ?? 'unknown',
    statMinute,
    success ? 1 : 0,
    success ? 0 : 1,
    firstTokenMs,
    firstTokenCount,
    createdAtIso,
    success ? createdAtIso : null,
    success ? null : createdAtIso,
    success ? null : row.error_message ?? null,
    normalizedUpdatedAt
  )
  markAccountQualityDirty(database, row.account_id, normalizedUpdatedAt)
}

export function subtractAccountQualityMinuteStats(database: DatabaseSync, row: UsageStatsRecordRow, updatedAt: string): void {
  const createdAt = usageStatsRecordCreatedAt(row)
  const normalizedUpdatedAt = requiredRfc3339Instant(updatedAt, '账号质量统计 updatedAt')
  if (!row.account_id || !row.api_key_id) {
    return
  }
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
  `).run(success ? 1 : 0, success ? 0 : 1, firstTokenMs, firstTokenCount, normalizedUpdatedAt, row.account_id, statMinute)
  database.prepare(`
    DELETE FROM account_quality_minute_stats
    WHERE account_id = ? AND stat_minute = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND first_token_ms_sum = 0 AND first_token_ms_count = 0
  `).run(row.account_id, statMinute)
  markAccountQualityDirty(database, row.account_id, normalizedUpdatedAt)
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

function markAccountQualityDirty(database: DatabaseSync, accountId: string, updatedAt: string): void {
  database.prepare(`
    INSERT INTO account_quality_dirty_accounts (account_id, first_dirty_at, updated_at)
    VALUES (?, ?, ?)
    ON CONFLICT(account_id) DO UPDATE SET
      updated_at = excluded.updated_at
  `).run(accountId, updatedAt, updatedAt)
}
