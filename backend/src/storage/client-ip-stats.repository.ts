import type { DatabaseSync } from 'node:sqlite'

import { beginImmediateDatabaseTransaction, commitDatabaseTransaction, getStatsDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import { getUsageRecordShardDatabase, listUsageRecordShardLocationsPage, type UsageRecordShardLocation } from './usage-record-shards.js'
import { dateKey, usageStatsTimezone } from './usage-stats-helpers.js'
import {
  USAGE_STATS_RECORD_SELECT_COLUMNS,
  type StatsJobStateRow,
  type UsageStatsAccumulator,
  type UsageStatsRecordRow
} from './usage-stats-types.js'
import { usageStatsAccumulatorFromRecord } from './usage-stats-aggregation.js'
import {
  clientIpRegistryBucketCount,
  normalizeClientIpForStats,
  type NormalizedClientIp
} from './client-ip-normalization.js'
import {
  listClientIpStats as listClientIpStatsFromWindow,
  type ClientIpStatsListOptions,
  type ClientIpStatsListResult
} from './client-ip-stats-list.repository.js'
import {
  markClientIpRangeWindowsDirty,
  markCurrentClientIpUsageRangeWindowsStale
} from './client-ip-usage-range-windows.repository.js'

export { normalizeClientIpForStats, type NormalizedClientIp } from './client-ip-normalization.js'
export type {
  ClientIpLastUsedSortScope,
  ClientIpPolicyFilter,
  ClientIpStatsListOptions,
  ClientIpStatsListResult,
  ClientIpStatsRow,
  ClientIpStatsSortField,
  ClientIpUsageSummary
} from './client-ip-stats-list.repository.js'
export {
  createClientIpPolicy,
  disableClientIpPolicies,
  findActiveClientIpPolicyByHash,
  listActiveClientIpPolicies,
  recordClientIpPolicyHits,
  type ActiveClientIpPolicy,
  type ClientIpPolicyDisableInput,
  type ClientIpPolicyHitInput,
  type ClientIpPolicyMutationInput,
  type ClientIpPolicyStatus,
  type ClientIpPolicySummary
} from './client-ip-policy.repository.js'
export {
  clearClientIpRangeWindowDirtyMemoryForTest,
  pendingClientIpRangeWindowDirtyCountForTest,
  rebuildClientIpUsageRangeWindows,
  refreshClientIpUsageRangeWindows
} from './client-ip-usage-range-windows.repository.js'

const clientIpStatsJobName = 'client_ip_stats_aggregation'
const cursorSafetyDelaySeconds = 5
const clientIpStatsMaxShardsPerBatch = 16
let clientIpStatsShardScanOffset = 0
const ipRegistryBuckets = new Map<number, Set<string>>()

type DatabaseStatement = ReturnType<DatabaseSync['prepare']>

interface ClientIpAggregate {
  normalized: NormalizedClientIp
  requestCount: number
  successCount: number
  errorCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheReadCostUsd: number
  totalCostUsd: number
  durationMsSum: number
  durationMsCount: number
  durationMsMax: number
  firstTokenMsSum: number
  firstTokenMsCount: number
  firstSeenAt: string
  lastUsedAt: string
  lastErrorAt?: string
}

interface ClientIpAggregateStatements {
  registerInsert: DatabaseStatement
  registerUpdate: DatabaseStatement
  dailyUpsert: DatabaseStatement
}

export function aggregateClientIpStatsBatch(limit = 2000): number {
  const database = getStatsDatabase()
  const batchLimit = Math.max(1, Math.trunc(limit))
  const shardLocationsWindow = clientIpStatsShardLocationsForBatch(batchLimit)
  const shardLocations = shardLocationsWindow.locations
  const scannedAllShardLocations = !shardLocationsWindow.hasMore
  const safeCreatedBefore = clientIpStatsSafeCreatedBefore()
  const transactionStarted = beginImmediateDatabaseTransaction(database)
  let processedRows = 0
  try {
    const updatedAt = nowIso()
    if (shardLocations.length === 0) {
      updateClientIpStatsJobState(database, {
        lastSuccessAt: updatedAt,
        lagSeconds: 0
      })
      commitDatabaseTransaction(database, transactionStarted)
      return 0
    }

    const perShardLimit = Math.max(1, Math.ceil(batchLimit / shardLocations.length))
    let globalCursor: { created_at: string; id: string } | undefined
    let maxLagSeconds = 0
    const shardsWithMoreRows: UsageRecordShardLocation[] = []
    const processShard = (location: UsageRecordShardLocation, limitForShard: number, updateIgnoredCursor: boolean): boolean => {
      if (processedRows >= batchLimit) return false
      const state = clientIpStatsShardJobState(database, location.shardKey)
      const shardDatabase = getUsageRecordShardDatabase(location)
      const rowLimit = Math.max(1, Math.min(limitForShard, batchLimit - processedRows))
      const rows = shardDatabase
        .prepare(`
          SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
          FROM usage_records
          WHERE created_at <= ?
            AND traffic_source <> 'cooldown_retest'
            AND (created_at > ? OR (created_at = ? AND id > ?))
          ORDER BY created_at ASC, id ASC
          LIMIT ?
        `)
        .all(safeCreatedBefore, state.cursorCreatedAt, state.cursorCreatedAt, state.cursorId, rowLimit) as unknown as UsageStatsRecordRow[]

      if (rows.length > 0) {
        const aggregates = buildClientIpAggregates(rows)
        writeClientIpAggregates(database, aggregates, updatedAt)
        processedRows += rows.length
        const last = rows[rows.length - 1]
        updateClientIpStatsShardJobState(database, location, {
          cursorCreatedAt: last.created_at,
          cursorId: last.id,
          lastSuccessAt: updatedAt,
          lagSeconds: cursorLagSecondsFromCreatedAt(last.created_at)
        })
        globalCursor = latestCursor(globalCursor, { created_at: last.created_at, id: last.id })
        maxLagSeconds = Math.max(maxLagSeconds, cursorLagSecondsFromCreatedAt(last.created_at))
        return rows.length >= rowLimit
      }

      if (!updateIgnoredCursor) return false
      const ignoredCursor = latestIgnoredUsageRecordCursor(shardDatabase, safeCreatedBefore, state.cursorCreatedAt, state.cursorId)
      const cursorCreatedAt = ignoredCursor?.created_at ?? state.cursorCreatedAt
      const cursorId = ignoredCursor?.id ?? state.cursorId
      const lagSeconds = latestUsageRecordLagSeconds(shardDatabase, safeCreatedBefore, cursorCreatedAt, cursorId)
      updateClientIpStatsShardJobState(database, location, {
        cursorCreatedAt: ignoredCursor?.created_at,
        cursorId: ignoredCursor?.id,
        lastSuccessAt: updatedAt,
        lagSeconds
      })
      if (ignoredCursor) {
        globalCursor = latestCursor(globalCursor, ignoredCursor)
      }
      maxLagSeconds = Math.max(maxLagSeconds, lagSeconds)
      return false
    }

    for (const location of shardLocations) {
      if (processedRows >= batchLimit) break
      if (processShard(location, perShardLimit, true)) {
        shardsWithMoreRows.push(location)
      }
    }
    while (processedRows < batchLimit && shardsWithMoreRows.length > 0) {
      const candidates = shardsWithMoreRows.splice(0, shardsWithMoreRows.length)
      for (const location of candidates) {
        if (processedRows >= batchLimit) break
        if (processShard(location, batchLimit - processedRows, false)) {
          shardsWithMoreRows.push(location)
        }
      }
    }
    updateClientIpStatsJobState(database, {
      cursorCreatedAt: globalCursor?.created_at,
      cursorId: globalCursor?.id,
      lastSuccessAt: updatedAt,
      lagSeconds: scannedAllShardLocations
        ? maxLagSeconds
        : Math.max(maxLagSeconds, latestClientIpStatsLagSeconds() ?? maxLagSeconds)
    })
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    updateClientIpStatsJobState(database, {
      lastErrorMessage: error instanceof Error ? error.message : 'IP 统计聚合失败',
      lagSeconds: latestClientIpStatsLagSeconds()
    })
    throw error
  }
  return processedRows
}

export function listClientIpStats(options: ClientIpStatsListOptions = {}): ClientIpStatsListResult {
  return listClientIpStatsFromWindow(options)
}

export function latestClientIpStatsLagSeconds(): number | undefined {
  const row = getStatsDatabase()
    .prepare("SELECT lag_seconds FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?")
    .get(clientIpStatsJobName) as unknown as { lag_seconds?: number | null } | undefined
  const value = row?.lag_seconds
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function buildClientIpAggregates(rows: UsageStatsRecordRow[]): ClientIpAggregate[] {
  const aggregates = new Map<string, ClientIpAggregate>()
  for (const row of rows) {
    const normalized = normalizeClientIpForStats(row.client_ip)
    if (!normalized) continue
    const statDate = dateKey(new Date(row.created_at), usageStatsTimezone())
    const key = `${normalized.ipHash}:${statDate}`
    const accumulator = usageStatsAccumulatorFromRecord(row)
    const current = aggregates.get(key)
    if (current) {
      addAccumulatorToClientIpAggregate(current, accumulator, row.created_at)
      continue
    }
    aggregates.set(key, {
      normalized,
      requestCount: accumulator.requestCount,
      successCount: accumulator.successCount,
      errorCount: accumulator.errorCount,
      inputTokens: accumulator.inputTokens,
      outputTokens: accumulator.outputTokens,
      cacheReadTokens: accumulator.cacheReadTokens,
      cacheReadCostUsd: accumulator.cacheReadCostUsd,
      totalCostUsd: accumulator.totalCostUsd,
      durationMsSum: accumulator.durationMsSum,
      durationMsCount: accumulator.durationMsCount,
      durationMsMax: accumulator.durationMsMax,
      firstTokenMsSum: accumulator.firstTokenMsSum,
      firstTokenMsCount: accumulator.firstTokenMsCount,
      firstSeenAt: row.created_at,
      lastUsedAt: row.created_at,
      lastErrorAt: accumulator.lastErrorAt
    })
  }
  return [...aggregates.values()]
}

function writeClientIpAggregates(database: DatabaseSync, aggregates: ClientIpAggregate[], updatedAt: string): void {
  if (!aggregates.length) return
  const dirtyIpHashes = new Set<string>()
  const statements = prepareClientIpAggregateStatements(database)
  for (const aggregate of aggregates) {
    dirtyIpHashes.add(aggregate.normalized.ipHash)
    registerClientIp(statements, aggregate.normalized, aggregate.firstSeenAt, aggregate.lastUsedAt, updatedAt)
    upsertClientIpDaily(statements, aggregate, updatedAt)
  }
  markCurrentClientIpUsageRangeWindowsStale(database)
  markClientIpRangeWindowsDirty(database, dirtyIpHashes, updatedAt)
}

function prepareClientIpAggregateStatements(database: DatabaseSync): ClientIpAggregateStatements {
  return {
    registerInsert: database.prepare(`
      INSERT OR IGNORE INTO client_ip_registry (
        ip_hash, bucket_no, aggregate_ip_key, client_ip, ip_version,
        first_seen_at, last_seen_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    `),
    registerUpdate: database.prepare(`
      UPDATE client_ip_registry
      SET client_ip = ?,
        first_seen_at = CASE WHEN first_seen_at > ? THEN ? ELSE first_seen_at END,
        last_seen_at = CASE WHEN last_seen_at < ? THEN ? ELSE last_seen_at END,
        updated_at = ?
      WHERE ip_hash = ?
    `),
    dailyUpsert: database.prepare(`
      INSERT INTO client_ip_stats_daily (
        ip_hash, stat_date, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
        last_used_at, last_error_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(ip_hash, stat_date) DO UPDATE SET
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
        last_used_at = CASE WHEN client_ip_stats_daily.last_used_at IS NULL OR excluded.last_used_at > client_ip_stats_daily.last_used_at THEN excluded.last_used_at ELSE client_ip_stats_daily.last_used_at END,
        last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN client_ip_stats_daily.last_error_at WHEN client_ip_stats_daily.last_error_at IS NULL OR excluded.last_error_at > client_ip_stats_daily.last_error_at THEN excluded.last_error_at ELSE client_ip_stats_daily.last_error_at END,
        updated_at = excluded.updated_at
    `)
  }
}

function registerClientIp(
  statements: ClientIpAggregateStatements,
  normalized: NormalizedClientIp,
  firstSeenAt: string,
  lastSeenAt: string,
  updatedAt: string
): void {
  const bucket = registryBucket(normalized.bucketNo)
  if (!bucket.has(normalized.ipHash)) {
    statements.registerInsert.run(
      normalized.ipHash,
      normalized.bucketNo,
      normalized.aggregateIpKey,
      normalized.clientIp,
      normalized.ipVersion,
      firstSeenAt,
      lastSeenAt,
      updatedAt,
      updatedAt
    )
    bucket.add(normalized.ipHash)
  }
  statements.registerUpdate.run(
    normalized.clientIp,
    firstSeenAt,
    firstSeenAt,
    lastSeenAt,
    lastSeenAt,
    updatedAt,
    normalized.ipHash
  )
}

function upsertClientIpDaily(statements: ClientIpAggregateStatements, aggregate: ClientIpAggregate, updatedAt: string): void {
  statements.dailyUpsert.run(
    aggregate.normalized.ipHash,
    dateKey(new Date(aggregate.firstSeenAt), usageStatsTimezone()),
    aggregate.requestCount,
    aggregate.successCount,
    aggregate.errorCount,
    aggregate.inputTokens,
    aggregate.outputTokens,
    aggregate.cacheReadTokens,
    aggregate.cacheReadCostUsd,
    aggregate.totalCostUsd,
    aggregate.durationMsSum,
    aggregate.durationMsCount,
    aggregate.durationMsMax,
    aggregate.firstTokenMsSum,
    aggregate.firstTokenMsCount,
    aggregate.lastUsedAt,
    aggregate.lastErrorAt ?? null,
    updatedAt
  )
}

function registryBucket(bucketNo: number): Set<string> {
  const normalizedBucket = Number.isInteger(bucketNo) ? Math.max(0, Math.min(clientIpRegistryBucketCount - 1, bucketNo)) : 0
  const current = ipRegistryBuckets.get(normalizedBucket)
  if (current) return current
  const bucket = new Set<string>()
  ipRegistryBuckets.set(normalizedBucket, bucket)
  return bucket
}

function addAccumulatorToClientIpAggregate(target: ClientIpAggregate, accumulator: UsageStatsAccumulator, createdAt: string): void {
  target.requestCount += accumulator.requestCount
  target.successCount += accumulator.successCount
  target.errorCount += accumulator.errorCount
  target.inputTokens += accumulator.inputTokens
  target.outputTokens += accumulator.outputTokens
  target.cacheReadTokens += accumulator.cacheReadTokens
  target.cacheReadCostUsd += accumulator.cacheReadCostUsd
  target.totalCostUsd += accumulator.totalCostUsd
  target.durationMsSum += accumulator.durationMsSum
  target.durationMsCount += accumulator.durationMsCount
  target.durationMsMax = Math.max(target.durationMsMax, accumulator.durationMsMax)
  target.firstTokenMsSum += accumulator.firstTokenMsSum
  target.firstTokenMsCount += accumulator.firstTokenMsCount
  if (createdAt < target.firstSeenAt) target.firstSeenAt = createdAt
  if (createdAt > target.lastUsedAt) target.lastUsedAt = createdAt
  if (accumulator.lastErrorAt && (!target.lastErrorAt || accumulator.lastErrorAt > target.lastErrorAt)) {
    target.lastErrorAt = accumulator.lastErrorAt
  }
}

function clientIpStatsShardLocationsForBatch(batchLimit: number): ReturnType<typeof listUsageRecordShardLocationsPage> {
  const maxShardCount = Math.max(1, Math.min(clientIpStatsMaxShardsPerBatch, Math.trunc(batchLimit)))
  const window = listUsageRecordShardLocationsPage({
    offset: clientIpStatsShardScanOffset,
    limit: maxShardCount
  })
  clientIpStatsShardScanOffset = window.total > 0
    ? (clientIpStatsShardScanOffset + window.locations.length) % window.total
    : 0
  return window
}

function clientIpStatsShardJobState(database: DatabaseSync, shardKey: string): { cursorCreatedAt: string; cursorId: string } {
  const row = database
    .prepare("SELECT cursor_created_at, cursor_id FROM stats_job_state WHERE scope_type = 'usage_shard' AND scope_id = ? AND job_name = ?")
    .get(shardKey, clientIpStatsJobName) as unknown as StatsJobStateRow | undefined
  return { cursorCreatedAt: row?.cursor_created_at ?? '', cursorId: row?.cursor_id ?? '' }
}

function updateClientIpStatsJobState(database: DatabaseSync, input: { cursorCreatedAt?: string; cursorId?: string; lastSuccessAt?: string; lastErrorMessage?: string; lagSeconds?: number }): void {
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('global', '', ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = COALESCE(excluded.cursor_created_at, stats_job_state.cursor_created_at),
      cursor_id = COALESCE(excluded.cursor_id, stats_job_state.cursor_id),
      last_success_at = COALESCE(excluded.last_success_at, stats_job_state.last_success_at),
      last_error_message = excluded.last_error_message,
      lag_seconds = excluded.lag_seconds,
      updated_at = excluded.updated_at
  `).run(clientIpStatsJobName, input.cursorCreatedAt ?? null, input.cursorId ?? null, input.lastSuccessAt ?? null, input.lastErrorMessage ?? null, input.lagSeconds ?? null, nowIso())
}

function updateClientIpStatsShardJobState(database: DatabaseSync, location: UsageRecordShardLocation, input: { cursorCreatedAt?: string; cursorId?: string; lastSuccessAt?: string; lastErrorMessage?: string; lagSeconds?: number }): void {
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('usage_shard', ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = COALESCE(excluded.cursor_created_at, stats_job_state.cursor_created_at),
      cursor_id = COALESCE(excluded.cursor_id, stats_job_state.cursor_id),
      last_success_at = COALESCE(excluded.last_success_at, stats_job_state.last_success_at),
      last_error_message = excluded.last_error_message,
      lag_seconds = excluded.lag_seconds,
      updated_at = excluded.updated_at
  `).run(location.shardKey, clientIpStatsJobName, input.cursorCreatedAt ?? null, input.cursorId ?? null, input.lastSuccessAt ?? null, input.lastErrorMessage ?? null, input.lagSeconds ?? null, nowIso())
}

function clientIpStatsSafeCreatedBefore(): string {
  return new Date(Date.now() - cursorSafetyDelaySeconds * 1000).toISOString()
}

function latestIgnoredUsageRecordCursor(database: DatabaseSync, safeCreatedBefore: string, cursorCreatedAt: string, cursorId: string): { created_at: string; id: string } | undefined {
  const latest = database
    .prepare(`
      SELECT created_at, id
      FROM usage_records
      WHERE created_at <= ?
        AND traffic_source = 'cooldown_retest'
        AND (created_at > ? OR (created_at = ? AND id > ?))
      ORDER BY created_at DESC, id DESC
      LIMIT 1
    `)
    .get(safeCreatedBefore, cursorCreatedAt, cursorCreatedAt, cursorId) as unknown as { created_at?: string; id?: string } | undefined
  return latest?.created_at && latest.id ? { created_at: latest.created_at, id: latest.id } : undefined
}

function latestUsageRecordLagSeconds(database: DatabaseSync, safeCreatedBefore: string, cursorCreatedAt: string, cursorId: string): number {
  const latest = database
    .prepare(`
      SELECT created_at
      FROM usage_records
      WHERE created_at <= ?
        AND traffic_source <> 'cooldown_retest'
        AND (created_at > ? OR (created_at = ? AND id > ?))
      ORDER BY created_at DESC, id DESC
      LIMIT 1
    `)
    .get(safeCreatedBefore, cursorCreatedAt, cursorCreatedAt, cursorId) as unknown as { created_at?: string } | undefined
  return latest?.created_at ? cursorLagSecondsFromCreatedAt(latest.created_at) : 0
}

function latestCursor(
  current: { created_at: string; id: string } | undefined,
  next: { created_at: string; id: string }
): { created_at: string; id: string } {
  if (!current) return next
  if (next.created_at > current.created_at) return next
  if (next.created_at === current.created_at && next.id > current.id) return next
  return current
}

function cursorLagSecondsFromCreatedAt(cursorCreatedAt: string): number {
  const cursorTime = Date.parse(cursorCreatedAt)
  return Number.isFinite(cursorTime) ? Math.max(0, Math.floor((Date.now() - cursorTime) / 1000)) : 0
}
