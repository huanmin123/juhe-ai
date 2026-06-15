import type { DatabaseSync } from 'node:sqlite'

import { usageErrorTimeBuckets, type UsageStatsTimeBucketDefinition, type UsageStatsTimeKeys } from './usage-stats-time-buckets.js'
import { GLOBAL_STATS_SYSTEM_ACCOUNT_ID, type UsageStatsRecordRow } from './usage-stats-types.js'

export function upsertUsageErrorBuckets(database: DatabaseSync, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys, updatedAt: string): void {
  const errorGroup = row.provider_code ?? 'unknown'
  const providerCode = row.provider_code ?? 'unknown'
  const errorCode = row.error_code ?? String(row.status_code ?? 'unknown')
  for (const systemAccountId of [row.system_account_id, GLOBAL_STATS_SYSTEM_ACCOUNT_ID]) {
    for (const bucket of usageErrorTimeBuckets) {
      upsertUsageErrorBucket(database, bucket, timeKeys[bucket.valueKey], systemAccountId, errorGroup, providerCode, errorCode, row.status_code ?? 0, row.error_message ?? null, updatedAt)
    }
  }
}

export function subtractUsageErrorBuckets(database: DatabaseSync, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys, updatedAt: string): void {
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

function upsertUsageErrorBucket(database: DatabaseSync, bucket: UsageStatsTimeBucketDefinition, timeValue: string, systemAccountId: string, errorGroup: string, providerCode: string, errorCode: string, statusCode: number, errorMessage: string | null, updatedAt: string): void {
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

function subtractUsageErrorBucket(database: DatabaseSync, bucket: UsageStatsTimeBucketDefinition, timeValue: string, systemAccountId: string, errorGroup: string, providerCode: string, errorCode: string, statusCode: number, updatedAt: string): void {
  database.prepare(`
    UPDATE ${bucket.tableName}
    SET request_count = MAX(0, request_count - 1),
        error_count = MAX(0, error_count - 1),
        updated_at = ?
    WHERE system_account_id = ? AND ${bucket.columnName} = ? AND error_group = ? AND provider_code = ? AND error_code = ? AND status_code = ?
  `).run(updatedAt, systemAccountId, timeValue, errorGroup, providerCode, errorCode, statusCode)
  deleteEmptyUsageErrorBucket(database, bucket, timeValue, systemAccountId, errorGroup, providerCode, errorCode, statusCode)
}

function deleteEmptyUsageErrorBucket(database: DatabaseSync, bucket: UsageStatsTimeBucketDefinition, timeValue: string, systemAccountId: string, errorGroup: string, providerCode: string, errorCode: string, statusCode: number): void {
  database.prepare(`
    DELETE FROM ${bucket.tableName}
    WHERE system_account_id = ? AND ${bucket.columnName} = ? AND error_group = ? AND provider_code = ? AND error_code = ? AND status_code = ?
      AND request_count = 0 AND error_count = 0
  `).run(systemAccountId, timeValue, errorGroup, providerCode, errorCode, statusCode)
}
