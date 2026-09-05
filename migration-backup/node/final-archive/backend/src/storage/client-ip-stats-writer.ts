import type { DatabaseSync } from 'node:sqlite'

import { parseRfc3339Instant, requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../shared/rfc3339.js'
import type { DatabaseClient } from './database-client.js'
import {
  clientIpRegistryBucketCount,
  normalizeClientIpForStats,
  type NormalizedClientIp
} from './client-ip-normalization.js'
import {
  markClientIpRangeWindowsDirtyAsync,
  markClientIpRangeWindowsDirty,
  markCurrentClientIpUsageRangeWindowsStaleAsync,
  markCurrentClientIpUsageRangeWindowsStale
} from './client-ip-usage-range-windows.repository.js'
import { chunkValues } from './query-utils.js'
import { usageStatsAccumulatorFromRecord } from './usage-stats-aggregation.js'
import { dateKey, usageStatsTimezone } from './usage-stats-helpers.js'
import type {
  UsageStatsAccumulator,
  UsageStatsRecordRow
} from './usage-stats-types.js'

const ipRegistryBuckets = new Map<number, Set<string>>()

type DatabaseStatement = ReturnType<DatabaseSync['prepare']>

interface ClientIpAggregate {
  normalized: NormalizedClientIp
  statDate: string
  accountId?: string
  requestCount: number
  successCount: number
  errorCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheReadCostUsd: number
  cacheWriteTokens: number
  cacheWrite1hTokens: number
  cacheWriteCostUsd: number
  thinkingTokens: number
  inputImageTokens: number
  outputImageTokens: number
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
  accountDailyUpsert: DatabaseStatement
}

interface ClientIpAggregateBuildResult {
  ipAggregates: ClientIpAggregate[]
  accountAggregates: ClientIpAggregate[]
}

interface ClientIpRegistryAggregate {
  normalized: NormalizedClientIp
  firstSeenAt: string
  lastSeenAt: string
}

export function writeClientIpStatsAggregatesFromUsageRows(database: DatabaseSync, rows: UsageStatsRecordRow[], updatedAt: string): void {
  const normalizedUpdatedAt = requiredRfc3339Instant(updatedAt, '客户端 IP 统计 updatedAt')
  const aggregates = buildClientIpAggregates(rows)
  writeClientIpAggregates(database, aggregates.ipAggregates, aggregates.accountAggregates, normalizedUpdatedAt)
}

export async function writeClientIpStatsAggregatesFromUsageRowsAsync(
  client: DatabaseClient,
  rows: UsageStatsRecordRow[],
  updatedAt: string,
  timezone: string
): Promise<void> {
  const normalizedUpdatedAt = requiredRfc3339Instant(updatedAt, '客户端 IP 统计 updatedAt')
  const aggregates = buildClientIpAggregates(rows, timezone)
  await writeClientIpAggregatesAsync(client, aggregates.ipAggregates, aggregates.accountAggregates, normalizedUpdatedAt)
}

function buildClientIpAggregates(rows: UsageStatsRecordRow[], timezone = usageStatsTimezone()): ClientIpAggregateBuildResult {
  const ipAggregates = new Map<string, ClientIpAggregate>()
  const accountAggregates = new Map<string, ClientIpAggregate>()
  for (const row of rows) {
    const normalized = normalizeClientIpForStats(row.client_ip)
    if (!normalized) continue
    const createdAt = requiredRfc3339Instant(row.created_at, '使用记录 created_at')
    const createdAtDate = parseRfc3339Instant(createdAt)
    if (!createdAtDate) throw new Error('使用记录 created_at 必须是带 Z 或数值 offset 的 RFC3339 时间')
    const normalizedRow = row.created_at === createdAt ? row : { ...row, created_at: createdAt }
    const statDate = dateKey(createdAtDate, timezone)
    const key = `${normalized.ipHash}:${statDate}`
    const accumulator = usageStatsAccumulatorFromRecord(normalizedRow)
    const current = ipAggregates.get(key)
    if (current) {
      addAccumulatorToClientIpAggregate(current, accumulator, createdAt)
    } else {
      ipAggregates.set(key, {
        normalized,
        statDate,
        requestCount: accumulator.requestCount,
        successCount: accumulator.successCount,
        errorCount: accumulator.errorCount,
        inputTokens: accumulator.inputTokens,
        outputTokens: accumulator.outputTokens,
        cacheReadTokens: accumulator.cacheReadTokens,
        cacheReadCostUsd: accumulator.cacheReadCostUsd,
        cacheWriteTokens: accumulator.cacheWriteTokens,
        cacheWrite1hTokens: accumulator.cacheWrite1hTokens,
        cacheWriteCostUsd: accumulator.cacheWriteCostUsd,
        thinkingTokens: accumulator.thinkingTokens,
        inputImageTokens: accumulator.inputImageTokens,
        outputImageTokens: accumulator.outputImageTokens,
        totalCostUsd: accumulator.totalCostUsd,
        durationMsSum: accumulator.durationMsSum,
        durationMsCount: accumulator.durationMsCount,
        durationMsMax: accumulator.durationMsMax,
        firstTokenMsSum: accumulator.firstTokenMsSum,
        firstTokenMsCount: accumulator.firstTokenMsCount,
        firstSeenAt: createdAt,
        lastUsedAt: createdAt,
        lastErrorAt: accumulator.lastErrorAt
      })
    }
    const accountId = row.account_id?.trim()
    if (!accountId) continue
    const accountKey = `${normalized.ipHash}:${accountId}:${statDate}`
    const accountCurrent = accountAggregates.get(accountKey)
    if (accountCurrent) {
      addAccumulatorToClientIpAggregate(accountCurrent, accumulator, createdAt)
      continue
    }
    accountAggregates.set(accountKey, {
      normalized,
      statDate,
      accountId,
      requestCount: accumulator.requestCount,
      successCount: accumulator.successCount,
      errorCount: accumulator.errorCount,
      inputTokens: accumulator.inputTokens,
      outputTokens: accumulator.outputTokens,
      cacheReadTokens: accumulator.cacheReadTokens,
      cacheReadCostUsd: accumulator.cacheReadCostUsd,
      cacheWriteTokens: accumulator.cacheWriteTokens,
      cacheWrite1hTokens: accumulator.cacheWrite1hTokens,
      cacheWriteCostUsd: accumulator.cacheWriteCostUsd,
      thinkingTokens: accumulator.thinkingTokens,
      inputImageTokens: accumulator.inputImageTokens,
      outputImageTokens: accumulator.outputImageTokens,
      totalCostUsd: accumulator.totalCostUsd,
      durationMsSum: accumulator.durationMsSum,
      durationMsCount: accumulator.durationMsCount,
      durationMsMax: accumulator.durationMsMax,
      firstTokenMsSum: accumulator.firstTokenMsSum,
      firstTokenMsCount: accumulator.firstTokenMsCount,
      firstSeenAt: createdAt,
      lastUsedAt: createdAt,
      lastErrorAt: accumulator.lastErrorAt
    })
  }
  return {
    ipAggregates: [...ipAggregates.values()],
    accountAggregates: [...accountAggregates.values()]
  }
}

function writeClientIpAggregates(database: DatabaseSync, aggregates: ClientIpAggregate[], accountAggregates: ClientIpAggregate[], updatedAt: string): void {
  if (!aggregates.length && !accountAggregates.length) return
  const dirtyIpHashes = new Set<string>()
  const statements = prepareClientIpAggregateStatements(database)
  for (const aggregate of aggregates) {
    dirtyIpHashes.add(aggregate.normalized.ipHash)
    registerClientIp(statements, aggregate.normalized, aggregate.firstSeenAt, aggregate.lastUsedAt, updatedAt)
    upsertClientIpDaily(statements, aggregate, updatedAt)
  }
  for (const aggregate of accountAggregates) {
    dirtyIpHashes.add(aggregate.normalized.ipHash)
    upsertClientIpAccountDaily(statements, aggregate, updatedAt)
  }
  markClientIpRangeWindowsDirty(database, dirtyIpHashes, updatedAt)
  markCurrentClientIpUsageRangeWindowsStale(database)
}

async function writeClientIpAggregatesAsync(
  client: DatabaseClient,
  aggregates: ClientIpAggregate[],
  accountAggregates: ClientIpAggregate[],
  updatedAt: string
): Promise<void> {
  if (!aggregates.length && !accountAggregates.length) return
  const dirtyIpHashes = new Set<string>()
  for (const aggregate of aggregates) {
    dirtyIpHashes.add(aggregate.normalized.ipHash)
  }
  for (const aggregate of accountAggregates) {
    dirtyIpHashes.add(aggregate.normalized.ipHash)
  }
  await upsertClientIpRegistryAsync(client, registryAggregatesFromIpAggregates(aggregates), updatedAt)
  await upsertClientIpDailyAsync(client, aggregates, updatedAt)
  await upsertClientIpAccountDailyAsync(client, accountAggregates, updatedAt)
  await markClientIpRangeWindowsDirtyAsync(client, dirtyIpHashes, updatedAt)
  await markCurrentClientIpUsageRangeWindowsStaleAsync(client, updatedAt)
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
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
        cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
        last_used_at, last_error_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(ip_hash, stat_date) DO UPDATE SET
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
        duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum,
        duration_ms_count = duration_ms_count + excluded.duration_ms_count,
        duration_ms_max = MAX(duration_ms_max, excluded.duration_ms_max),
        first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
        first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
        last_used_at = CASE WHEN client_ip_stats_daily.last_used_at IS NULL OR excluded.last_used_at > client_ip_stats_daily.last_used_at THEN excluded.last_used_at ELSE client_ip_stats_daily.last_used_at END,
        last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN client_ip_stats_daily.last_error_at WHEN client_ip_stats_daily.last_error_at IS NULL OR excluded.last_error_at > client_ip_stats_daily.last_error_at THEN excluded.last_error_at ELSE client_ip_stats_daily.last_error_at END,
        updated_at = excluded.updated_at
    `),
    accountDailyUpsert: database.prepare(`
      INSERT INTO client_ip_account_stats_daily (
        ip_hash, account_id, stat_date, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
        cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
        last_used_at, last_error_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(ip_hash, account_id, stat_date) DO UPDATE SET
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
        duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum,
        duration_ms_count = duration_ms_count + excluded.duration_ms_count,
        duration_ms_max = MAX(duration_ms_max, excluded.duration_ms_max),
        first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
        first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
        last_used_at = CASE WHEN client_ip_account_stats_daily.last_used_at IS NULL OR excluded.last_used_at > client_ip_account_stats_daily.last_used_at THEN excluded.last_used_at ELSE client_ip_account_stats_daily.last_used_at END,
        last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN client_ip_account_stats_daily.last_error_at WHEN client_ip_account_stats_daily.last_error_at IS NULL OR excluded.last_error_at > client_ip_account_stats_daily.last_error_at THEN excluded.last_error_at ELSE client_ip_account_stats_daily.last_error_at END,
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
    aggregate.statDate,
    aggregate.requestCount,
    aggregate.successCount,
    aggregate.errorCount,
    aggregate.inputTokens,
    aggregate.outputTokens,
    aggregate.cacheReadTokens,
    aggregate.cacheReadCostUsd,
    aggregate.cacheWriteTokens,
    aggregate.cacheWrite1hTokens,
    aggregate.cacheWriteCostUsd,
    aggregate.thinkingTokens,
    aggregate.inputImageTokens,
    aggregate.outputImageTokens,
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

function upsertClientIpAccountDaily(statements: ClientIpAggregateStatements, aggregate: ClientIpAggregate, updatedAt: string): void {
  if (!aggregate.accountId) return
  statements.accountDailyUpsert.run(
    aggregate.normalized.ipHash,
    aggregate.accountId,
    aggregate.statDate,
    aggregate.requestCount,
    aggregate.successCount,
    aggregate.errorCount,
    aggregate.inputTokens,
    aggregate.outputTokens,
    aggregate.cacheReadTokens,
    aggregate.cacheReadCostUsd,
    aggregate.cacheWriteTokens,
    aggregate.cacheWrite1hTokens,
    aggregate.cacheWriteCostUsd,
    aggregate.thinkingTokens,
    aggregate.inputImageTokens,
    aggregate.outputImageTokens,
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

async function upsertClientIpRegistryAsync(client: DatabaseClient, entries: ClientIpRegistryAggregate[], updatedAt: string): Promise<void> {
  if (!entries.length) return
  const columns = [
    'ip_hash',
    'bucket_no',
    'aggregate_ip_key',
    'client_ip',
    'ip_version',
    'first_seen_at',
    'last_seen_at',
    'created_at',
    'updated_at'
  ]
  for (const chunk of chunkValues(entries, 500)) {
    await client.execute(`
      INSERT INTO ${statsTable(client, 'client_ip_registry')} (${columns.join(', ')})
      VALUES ${multiRowPlaceholders(chunk.length, columns.length)}
      ON CONFLICT(ip_hash) DO UPDATE SET
        bucket_no = EXCLUDED.bucket_no,
        aggregate_ip_key = EXCLUDED.aggregate_ip_key,
        client_ip = EXCLUDED.client_ip,
        ip_version = EXCLUDED.ip_version,
        first_seen_at = CASE WHEN client_ip_registry.first_seen_at > EXCLUDED.first_seen_at THEN EXCLUDED.first_seen_at ELSE client_ip_registry.first_seen_at END,
        last_seen_at = CASE WHEN client_ip_registry.last_seen_at < EXCLUDED.last_seen_at THEN EXCLUDED.last_seen_at ELSE client_ip_registry.last_seen_at END,
        updated_at = EXCLUDED.updated_at
    `, chunk.flatMap((entry) => [
      entry.normalized.ipHash,
      entry.normalized.bucketNo,
      entry.normalized.aggregateIpKey,
      entry.normalized.clientIp,
      entry.normalized.ipVersion,
      entry.firstSeenAt,
      entry.lastSeenAt,
      updatedAt,
      updatedAt
    ]))
  }
}

async function upsertClientIpDailyAsync(client: DatabaseClient, aggregates: ClientIpAggregate[], updatedAt: string): Promise<void> {
  if (!aggregates.length) return
  const columns = [
    'ip_hash',
    'stat_date',
    'request_count',
    'success_count',
    'error_count',
    'input_tokens',
    'output_tokens',
    'cache_read_tokens',
    'cache_read_cost_usd',
    'cache_write_tokens',
    'cache_write_1h_tokens',
    'cache_write_cost_usd',
    'thinking_tokens',
    'input_image_tokens',
    'output_image_tokens',
    'total_cost_usd',
    'duration_ms_sum',
    'duration_ms_count',
    'duration_ms_max',
    'first_token_ms_sum',
    'first_token_ms_count',
    'last_used_at',
    'last_error_at',
    'updated_at'
  ]
  for (const chunk of chunkValues(aggregates, 500)) {
    await client.execute(`
      INSERT INTO ${statsTable(client, 'client_ip_stats_daily')} (${columns.join(', ')})
      VALUES ${multiRowPlaceholders(chunk.length, columns.length)}
      ON CONFLICT(ip_hash, stat_date) DO UPDATE SET
        request_count = client_ip_stats_daily.request_count + EXCLUDED.request_count,
        success_count = client_ip_stats_daily.success_count + EXCLUDED.success_count,
        error_count = client_ip_stats_daily.error_count + EXCLUDED.error_count,
        input_tokens = client_ip_stats_daily.input_tokens + EXCLUDED.input_tokens,
        output_tokens = client_ip_stats_daily.output_tokens + EXCLUDED.output_tokens,
        cache_read_tokens = client_ip_stats_daily.cache_read_tokens + EXCLUDED.cache_read_tokens,
        cache_read_cost_usd = client_ip_stats_daily.cache_read_cost_usd + EXCLUDED.cache_read_cost_usd,
        cache_write_tokens = client_ip_stats_daily.cache_write_tokens + EXCLUDED.cache_write_tokens,
        cache_write_1h_tokens = client_ip_stats_daily.cache_write_1h_tokens + EXCLUDED.cache_write_1h_tokens,
        cache_write_cost_usd = client_ip_stats_daily.cache_write_cost_usd + EXCLUDED.cache_write_cost_usd,
        thinking_tokens = client_ip_stats_daily.thinking_tokens + EXCLUDED.thinking_tokens,
        input_image_tokens = client_ip_stats_daily.input_image_tokens + EXCLUDED.input_image_tokens,
        output_image_tokens = client_ip_stats_daily.output_image_tokens + EXCLUDED.output_image_tokens,
        total_cost_usd = client_ip_stats_daily.total_cost_usd + EXCLUDED.total_cost_usd,
        duration_ms_sum = client_ip_stats_daily.duration_ms_sum + EXCLUDED.duration_ms_sum,
        duration_ms_count = client_ip_stats_daily.duration_ms_count + EXCLUDED.duration_ms_count,
        duration_ms_max = GREATEST(client_ip_stats_daily.duration_ms_max, EXCLUDED.duration_ms_max),
        first_token_ms_sum = client_ip_stats_daily.first_token_ms_sum + EXCLUDED.first_token_ms_sum,
        first_token_ms_count = client_ip_stats_daily.first_token_ms_count + EXCLUDED.first_token_ms_count,
        last_used_at = CASE WHEN client_ip_stats_daily.last_used_at IS NULL OR EXCLUDED.last_used_at > client_ip_stats_daily.last_used_at THEN EXCLUDED.last_used_at ELSE client_ip_stats_daily.last_used_at END,
        last_error_at = CASE WHEN EXCLUDED.last_error_at IS NULL THEN client_ip_stats_daily.last_error_at WHEN client_ip_stats_daily.last_error_at IS NULL OR EXCLUDED.last_error_at > client_ip_stats_daily.last_error_at THEN EXCLUDED.last_error_at ELSE client_ip_stats_daily.last_error_at END,
        updated_at = EXCLUDED.updated_at
    `, chunk.flatMap((aggregate) => clientIpDailyParams(aggregate, updatedAt)))
  }
}

async function upsertClientIpAccountDailyAsync(client: DatabaseClient, aggregates: ClientIpAggregate[], updatedAt: string): Promise<void> {
  if (!aggregates.length) return
  const columns = [
    'ip_hash',
    'account_id',
    'stat_date',
    'request_count',
    'success_count',
    'error_count',
    'input_tokens',
    'output_tokens',
    'cache_read_tokens',
    'cache_read_cost_usd',
    'cache_write_tokens',
    'cache_write_1h_tokens',
    'cache_write_cost_usd',
    'thinking_tokens',
    'input_image_tokens',
    'output_image_tokens',
    'total_cost_usd',
    'duration_ms_sum',
    'duration_ms_count',
    'duration_ms_max',
    'first_token_ms_sum',
    'first_token_ms_count',
    'last_used_at',
    'last_error_at',
    'updated_at'
  ]
  for (const chunk of chunkValues(aggregates, 500)) {
    await client.execute(`
      INSERT INTO ${statsTable(client, 'client_ip_account_stats_daily')} (${columns.join(', ')})
      VALUES ${multiRowPlaceholders(chunk.length, columns.length)}
      ON CONFLICT(ip_hash, account_id, stat_date) DO UPDATE SET
        request_count = client_ip_account_stats_daily.request_count + EXCLUDED.request_count,
        success_count = client_ip_account_stats_daily.success_count + EXCLUDED.success_count,
        error_count = client_ip_account_stats_daily.error_count + EXCLUDED.error_count,
        input_tokens = client_ip_account_stats_daily.input_tokens + EXCLUDED.input_tokens,
        output_tokens = client_ip_account_stats_daily.output_tokens + EXCLUDED.output_tokens,
        cache_read_tokens = client_ip_account_stats_daily.cache_read_tokens + EXCLUDED.cache_read_tokens,
        cache_read_cost_usd = client_ip_account_stats_daily.cache_read_cost_usd + EXCLUDED.cache_read_cost_usd,
        cache_write_tokens = client_ip_account_stats_daily.cache_write_tokens + EXCLUDED.cache_write_tokens,
        cache_write_1h_tokens = client_ip_account_stats_daily.cache_write_1h_tokens + EXCLUDED.cache_write_1h_tokens,
        cache_write_cost_usd = client_ip_account_stats_daily.cache_write_cost_usd + EXCLUDED.cache_write_cost_usd,
        thinking_tokens = client_ip_account_stats_daily.thinking_tokens + EXCLUDED.thinking_tokens,
        input_image_tokens = client_ip_account_stats_daily.input_image_tokens + EXCLUDED.input_image_tokens,
        output_image_tokens = client_ip_account_stats_daily.output_image_tokens + EXCLUDED.output_image_tokens,
        total_cost_usd = client_ip_account_stats_daily.total_cost_usd + EXCLUDED.total_cost_usd,
        duration_ms_sum = client_ip_account_stats_daily.duration_ms_sum + EXCLUDED.duration_ms_sum,
        duration_ms_count = client_ip_account_stats_daily.duration_ms_count + EXCLUDED.duration_ms_count,
        duration_ms_max = GREATEST(client_ip_account_stats_daily.duration_ms_max, EXCLUDED.duration_ms_max),
        first_token_ms_sum = client_ip_account_stats_daily.first_token_ms_sum + EXCLUDED.first_token_ms_sum,
        first_token_ms_count = client_ip_account_stats_daily.first_token_ms_count + EXCLUDED.first_token_ms_count,
        last_used_at = CASE WHEN client_ip_account_stats_daily.last_used_at IS NULL OR EXCLUDED.last_used_at > client_ip_account_stats_daily.last_used_at THEN EXCLUDED.last_used_at ELSE client_ip_account_stats_daily.last_used_at END,
        last_error_at = CASE WHEN EXCLUDED.last_error_at IS NULL THEN client_ip_account_stats_daily.last_error_at WHEN client_ip_account_stats_daily.last_error_at IS NULL OR EXCLUDED.last_error_at > client_ip_account_stats_daily.last_error_at THEN EXCLUDED.last_error_at ELSE client_ip_account_stats_daily.last_error_at END,
        updated_at = EXCLUDED.updated_at
    `, chunk.flatMap((aggregate) => clientIpAccountDailyParams(aggregate, updatedAt)))
  }
}

function registryAggregatesFromIpAggregates(aggregates: ClientIpAggregate[]): ClientIpRegistryAggregate[] {
  const entries = new Map<string, ClientIpRegistryAggregate>()
  for (const aggregate of aggregates) {
    const current = entries.get(aggregate.normalized.ipHash)
    if (!current) {
      entries.set(aggregate.normalized.ipHash, {
        normalized: aggregate.normalized,
        firstSeenAt: aggregate.firstSeenAt,
        lastSeenAt: aggregate.lastUsedAt
      })
      continue
    }
    const aggregateFirstSeenAtMs = rfc3339InstantMilliseconds(aggregate.firstSeenAt)
    const currentFirstSeenAtMs = rfc3339InstantMilliseconds(current.firstSeenAt)
    const aggregateLastSeenAtMs = rfc3339InstantMilliseconds(aggregate.lastUsedAt)
    const currentLastSeenAtMs = rfc3339InstantMilliseconds(current.lastSeenAt)
    if (aggregateFirstSeenAtMs === undefined || currentFirstSeenAtMs === undefined || aggregateLastSeenAtMs === undefined || currentLastSeenAtMs === undefined) {
      throw new Error('客户端 IP 注册表时间必须是带 Z 或数值 offset 的 RFC3339 时间')
    }
    if (aggregateFirstSeenAtMs < currentFirstSeenAtMs) {
      current.firstSeenAt = aggregate.firstSeenAt
    }
    if (aggregateLastSeenAtMs > currentLastSeenAtMs) {
      current.lastSeenAt = aggregate.lastUsedAt
    }
  }
  return [...entries.values()]
}

function clientIpDailyParams(aggregate: ClientIpAggregate, updatedAt: string): unknown[] {
  return [
    aggregate.normalized.ipHash,
    aggregate.statDate,
    aggregate.requestCount,
    aggregate.successCount,
    aggregate.errorCount,
    aggregate.inputTokens,
    aggregate.outputTokens,
    aggregate.cacheReadTokens,
    aggregate.cacheReadCostUsd,
    aggregate.cacheWriteTokens,
    aggregate.cacheWrite1hTokens,
    aggregate.cacheWriteCostUsd,
    aggregate.thinkingTokens,
    aggregate.inputImageTokens,
    aggregate.outputImageTokens,
    aggregate.totalCostUsd,
    aggregate.durationMsSum,
    aggregate.durationMsCount,
    aggregate.durationMsMax,
    aggregate.firstTokenMsSum,
    aggregate.firstTokenMsCount,
    aggregate.lastUsedAt,
    aggregate.lastErrorAt ?? null,
    updatedAt
  ]
}

function clientIpAccountDailyParams(aggregate: ClientIpAggregate, updatedAt: string): unknown[] {
  return [
    aggregate.normalized.ipHash,
    aggregate.accountId,
    aggregate.statDate,
    aggregate.requestCount,
    aggregate.successCount,
    aggregate.errorCount,
    aggregate.inputTokens,
    aggregate.outputTokens,
    aggregate.cacheReadTokens,
    aggregate.cacheReadCostUsd,
    aggregate.cacheWriteTokens,
    aggregate.cacheWrite1hTokens,
    aggregate.cacheWriteCostUsd,
    aggregate.thinkingTokens,
    aggregate.inputImageTokens,
    aggregate.outputImageTokens,
    aggregate.totalCostUsd,
    aggregate.durationMsSum,
    aggregate.durationMsCount,
    aggregate.durationMsMax,
    aggregate.firstTokenMsSum,
    aggregate.firstTokenMsCount,
    aggregate.lastUsedAt,
    aggregate.lastErrorAt ?? null,
    updatedAt
  ]
}

function registryBucket(bucketNo: number): Set<string> {
  const normalizedBucket = Number.isInteger(bucketNo) ? Math.max(0, Math.min(clientIpRegistryBucketCount - 1, bucketNo)) : 0
  const current = ipRegistryBuckets.get(normalizedBucket)
  if (current) return current
  const bucket = new Set<string>()
  ipRegistryBuckets.set(normalizedBucket, bucket)
  return bucket
}

function multiRowPlaceholders(rowCount: number, columnCount: number): string {
  const row = `(${Array.from({ length: columnCount }, () => '?').join(', ')})`
  return Array.from({ length: rowCount }, () => row).join(', ')
}

function statsTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable('juhe_stats', tableName)
}

function addAccumulatorToClientIpAggregate(target: ClientIpAggregate, accumulator: UsageStatsAccumulator, createdAt: string): void {
  target.requestCount += accumulator.requestCount
  target.successCount += accumulator.successCount
  target.errorCount += accumulator.errorCount
  target.inputTokens += accumulator.inputTokens
  target.outputTokens += accumulator.outputTokens
  target.cacheReadTokens += accumulator.cacheReadTokens
  target.cacheReadCostUsd += accumulator.cacheReadCostUsd
  target.cacheWriteTokens += accumulator.cacheWriteTokens
  target.cacheWrite1hTokens += accumulator.cacheWrite1hTokens
  target.cacheWriteCostUsd += accumulator.cacheWriteCostUsd
  target.thinkingTokens += accumulator.thinkingTokens
  target.inputImageTokens += accumulator.inputImageTokens
  target.outputImageTokens += accumulator.outputImageTokens
  target.totalCostUsd += accumulator.totalCostUsd
  target.durationMsSum += accumulator.durationMsSum
  target.durationMsCount += accumulator.durationMsCount
  target.durationMsMax = Math.max(target.durationMsMax, accumulator.durationMsMax)
  target.firstTokenMsSum += accumulator.firstTokenMsSum
  target.firstTokenMsCount += accumulator.firstTokenMsCount
  const createdAtMs = rfc3339InstantMilliseconds(createdAt)
  if (createdAtMs === undefined) throw new Error('客户端 IP 统计 createdAt 必须是带 Z 或数值 offset 的 RFC3339 时间')
  const firstSeenAtMs = rfc3339InstantMilliseconds(target.firstSeenAt)
  const lastUsedAtMs = rfc3339InstantMilliseconds(target.lastUsedAt)
  if (firstSeenAtMs === undefined || lastUsedAtMs === undefined) {
    throw new Error('客户端 IP 统计聚合时间必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  if (createdAtMs < firstSeenAtMs) target.firstSeenAt = createdAt
  if (createdAtMs > lastUsedAtMs) target.lastUsedAt = createdAt
  if (accumulator.lastErrorAt) {
    const lastErrorAtMs = rfc3339InstantMilliseconds(accumulator.lastErrorAt)
    const targetLastErrorAtMs = target.lastErrorAt === undefined ? undefined : rfc3339InstantMilliseconds(target.lastErrorAt)
    if (lastErrorAtMs === undefined || (target.lastErrorAt !== undefined && targetLastErrorAtMs === undefined)) {
      throw new Error('客户端 IP 统计错误时间必须是带 Z 或数值 offset 的 RFC3339 时间')
    }
    if (targetLastErrorAtMs === undefined || lastErrorAtMs > targetLastErrorAtMs) {
      target.lastErrorAt = accumulator.lastErrorAt
    }
  }
}
