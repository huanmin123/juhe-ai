import type { DatabaseSync } from 'node:sqlite'

import { usageLatencyTimeBuckets, type UsageStatsTimeBucketDefinition, type UsageStatsTimeKeys } from './usage-stats-time-buckets.js'
import type { UsageStatsEntry, UsageStatsRecordRow } from './usage-stats-types.js'

type LatencyMetricType = 'duration_ms' | 'first_token_ms'

const latencyBucketUpperBoundsMs = [100, 250, 500, 1000, 2000, 5000, 10000, 30000, 60000, -1] as const

export function upsertUsageLatencyEntry(database: DatabaseSync, entry: UsageStatsEntry, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys, updatedAt: string): void {
  const metrics = latencySamples(row)
  for (const metric of metrics) {
    for (const bucket of usageLatencyTimeBuckets) {
      upsertUsageLatencyBucket(database, bucket, timeKeys[bucket.valueKey], entry, metric.metricType, metric.bucketUpperBoundMs, updatedAt)
    }
  }
}

export function subtractUsageLatencyEntry(database: DatabaseSync, entry: UsageStatsEntry, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys, updatedAt: string): void {
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

function upsertUsageLatencyBucket(database: DatabaseSync, bucket: UsageStatsTimeBucketDefinition, timeValue: string, entry: UsageStatsEntry, metricType: LatencyMetricType, bucketUpperBoundMs: number, updatedAt: string): void {
  database.prepare(`
    INSERT INTO ${bucket.tableName} (system_account_id, scope_type, scope_id, metric_type, ${bucket.columnName}, bucket_upper_bound_ms, sample_count, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, 1, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id, metric_type, ${bucket.columnName}, bucket_upper_bound_ms) DO UPDATE SET
      sample_count = sample_count + excluded.sample_count,
      updated_at = excluded.updated_at
  `).run(entry.systemAccountId, entry.scopeType, entry.scopeId, metricType, timeValue, bucketUpperBoundMs, updatedAt)
}

function subtractUsageLatencyBucket(database: DatabaseSync, bucket: UsageStatsTimeBucketDefinition, timeValue: string, entry: UsageStatsEntry, metricType: LatencyMetricType, bucketUpperBoundMs: number, updatedAt: string): void {
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
