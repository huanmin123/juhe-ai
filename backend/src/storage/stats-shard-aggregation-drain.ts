import { listUsageRecordShardLocations } from './usage-record-shards.js'

export const statsAggregationMaxShardsPerBatch = 16

export interface SqliteShardAggregationDrainTracker {
  readonly emptyBatchLimit: number
  observe(processed: number): boolean
}

export function createSqliteShardAggregationDrainTracker(
  batchSize: number,
  shardLocationCount = listUsageRecordShardLocations().length
): SqliteShardAggregationDrainTracker {
  const normalizedBatchSize = Math.max(1, Math.trunc(batchSize))
  const shardScanWindowSize = Math.min(statsAggregationMaxShardsPerBatch, normalizedBatchSize)
  const emptyBatchLimit = Math.max(1, Math.ceil(Math.max(0, shardLocationCount) / shardScanWindowSize))
  let consecutiveEmptyBatches = 0

  return {
    emptyBatchLimit,
    observe(processed: number): boolean {
      consecutiveEmptyBatches = processed > 0 ? 0 : consecutiveEmptyBatches + 1
      return consecutiveEmptyBatches >= emptyBatchLimit
    }
  }
}
