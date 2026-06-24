import type { DatabaseSync } from 'node:sqlite'

import { usageLatencyTimeBuckets, type UsageStatsTimeBucketDefinition, type UsageStatsTimeKeys } from './usage-stats-time-buckets.js'
import type { UsageStatsEntry, UsageStatsRecordRow } from './usage-stats-types.js'

type SqliteStatement = ReturnType<DatabaseSync['prepare']>
type LatencyMetricType = 'duration_ms' | 'first_token_ms'

export interface AggregatedLatencyEntry {
  bucket: UsageStatsTimeBucketDefinition
  systemAccountId: string
  scopeType: string
  scopeId: string
  metricType: LatencyMetricType
  timeValue: string
  bucketUpperBoundMs: number
  sampleCount: number
}

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

export function addAggregatedLatencyEntries(target: Map<string, AggregatedLatencyEntry>, entry: UsageStatsEntry, row: UsageStatsRecordRow, timeKeys: UsageStatsTimeKeys): void {
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

export function upsertAggregatedLatencyEntries(database: DatabaseSync, entries: Map<string, AggregatedLatencyEntry>, updatedAt: string): void {
  const statements = new Map<string, SqliteStatement>()
  for (const entry of entries.values()) {
    const statement = statements.get(entry.bucket.tableName) ?? prepareUsageLatencyBucketCountUpsertStatement(database, entry.bucket)
    statements.set(entry.bucket.tableName, statement)
    statement.run(entry.systemAccountId, entry.scopeType, entry.scopeId, entry.metricType, entry.timeValue, entry.bucketUpperBoundMs, entry.sampleCount, updatedAt)
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

function usageStatsEntryKey(systemAccountId: string, scopeType: string, scopeId: string): string {
  return `${systemAccountId}\u0000${scopeType}\u0000${scopeId}`
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
