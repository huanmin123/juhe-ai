import { createHash } from 'node:crypto'
import { isIP } from 'node:net'
import type { DatabaseSync, SQLInputValue } from 'node:sqlite'

import type { AccountUsageStatsRange } from '../domain/types.js'
import { beginDatabaseTransaction, beginImmediateDatabaseTransaction, commitDatabaseTransaction, getStatsDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders } from './query-utils.js'
import { getUsageRecordShardDatabase, listUsageRecordShardLocationsPage, type UsageRecordShardLocation } from './usage-record-shards.js'
import { dateKey, normalizeAccountUsageStatsRange, startOfZonedDateKeyIso, usageStatsTimezone } from './usage-stats-helpers.js'
import { fixedUsageStatsDateKeys, nextDateKey } from './usage-stats-window-helpers.js'
import {
  USAGE_STATS_RECORD_SELECT_COLUMNS,
  type StatsJobStateRow,
  type UsageStatsAccumulator,
  type UsageStatsRecordRow
} from './usage-stats-types.js'
import { usageStatsAccumulatorFromRecord } from './usage-stats-aggregation.js'

export type ClientIpPolicyStatus = 'active' | 'disabled'
export type ClientIpStatsSortField = 'requestCount' | 'successCount' | 'errorCount' | 'errorRate' | 'totalTokens' | 'totalCost' | 'activeDays' | 'lastUsedAt'
export type ClientIpPolicyFilter = 'all' | 'normal' | 'blacklisted'
export type ClientIpLastUsedSortScope = 'range' | 'global'

export interface NormalizedClientIp {
  clientIp: string
  aggregateIpKey: string
  ipVersion: 4
  ipHash: string
  bucketNo: number
}

export interface ClientIpUsageSummary {
  requestCount: number
  successCount: number
  errorCount: number
  errorRate: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheReadCost: number
  totalTokens: number
  totalCost: number
  activeDays: number
  averageDurationMs?: number
  averageFirstTokenMs?: number
  maxDurationMs?: number
  lastUsedAt?: string
  lastErrorAt?: string
}

export interface ClientIpStatsRow {
  ipHash: string
  aggregateIpKey: string
  lastSeenAt?: string
  status: ClientIpPolicyFilter
  rangeUsage: ClientIpUsageSummary
}

export interface ClientIpStatsListOptions {
  page?: number
  pageSize?: number
  keyword?: string
  status?: ClientIpPolicyFilter
  startDate?: string
  endDate?: string
  lastUsedStartDate?: string
  lastUsedEndDate?: string
  lastUsedSortScope?: ClientIpLastUsedSortScope
  sortField?: ClientIpStatsSortField
  sortOrder?: 'asc' | 'desc'
}

export interface ClientIpStatsListResult {
  items: ClientIpStatsRow[]
  pageUpperBound: number
  hasMore: boolean
  page: number
  pageSize: number
  range: AccountUsageStatsRange
  rangeReady: boolean
}

export interface ClientIpPolicySummary {
  id: string
  ipHash: string
  status: ClientIpPolicyStatus
  reason?: string
  expiresAt?: string
  createdBySystemAccountId: string
  createdAt: string
  updatedAt: string
  disabledAt?: string
  disabledBySystemAccountId?: string
  disabledReason?: string
}

export interface ActiveClientIpPolicy {
  id: string
  ipHash: string
  aggregateIpKey: string
  clientIp: string
  reason?: string
  expiresAt?: string
}

export interface ClientIpPolicyMutationInput {
  ipHash: string
  reason?: string
  expiresAt?: string
  actorSystemAccountId: string
}

export interface ClientIpPolicyDisableInput {
  ipHash: string
  reason?: string
  actorSystemAccountId: string
}

export interface ClientIpPolicyHitInput {
  ipHash: string
  policyId: string
  hitCount?: number
  hitAt?: string
}

const clientIpStatsJobName = 'client_ip_stats_aggregation'
const clientIpRangeWindowJobName = 'client_ip_range_window_refresh'
const clientIpRangeWindowScopeType = 'client_ip_range_window'
const clientIpRegistryBucketCount = 4096
const cursorSafetyDelaySeconds = 5
const clientIpRangeWindowDirtyLimit = 1000
const clientIpRangeWindowChunkSize = 200
const clientIpStatsMaxListWindowRows = 1001
const clientIpStatsMaxShardsPerBatch = 16
let clientIpStatsShardScanOffset = 0
const ipRegistryBuckets = new Map<number, Set<string>>()
const clientIpRangeWindowDirtyIpHashes = new Set<string>()

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

interface ClientIpRangeWhere {
  clause: string
  params: SQLInputValue[]
}

export function normalizeClientIpForStats(value?: string | null): NormalizedClientIp | undefined {
  const normalizedIp = normalizePlainClientIp(value)
  if (!normalizedIp) return undefined
  const version = isIP(normalizedIp)
  if (version === 4) {
    const clientIp = normalizeIpv4(normalizedIp)
    if (!clientIp) return undefined
    return clientIpIdentity(clientIp, clientIp, 4)
  }
  return undefined
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

export function refreshClientIpUsageRangeWindows(options: { full?: boolean; dirtyLimit?: number } = {}): void {
  const database = getStatsDatabase()
  const windows = currentClientIpRangeWindows()
  if (!windows.length) return
  const updatedAt = nowIso()
  if (options.full) {
    for (const window of windows) {
      refreshClientIpUsageRangeWindow(database, window.startDate, window.endDate, updatedAt)
    }
    clearAllClientIpRangeWindowDirtyIpHashes(database)
    return
  }
  const dirtyIpHashes = takeClientIpRangeWindowDirtyIpHashes(database, options.dirtyLimit ?? clientIpRangeWindowDirtyLimit)
  if (!dirtyIpHashes.length) {
    if (hasStaleClientIpUsageRangeWindows(database, windows)) {
      for (const window of windows) {
        refreshClientIpUsageRangeWindow(database, window.startDate, window.endDate, updatedAt)
      }
      clearAllClientIpRangeWindowDirtyIpHashes(database)
    }
    return
  }
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const window of windows) {
      refreshClientIpUsageRangeWindowForIps(database, window.startDate, window.endDate, dirtyIpHashes, updatedAt)
    }
    clearClientIpRangeWindowDirtyIpHashes(database, dirtyIpHashes)
    if (!hasPendingClientIpRangeWindowDirtyIpHashes(database)) {
      markClientIpUsageRangeWindowsReady(database, windows, updatedAt)
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    markClientIpRangeWindowsDirty(database, dirtyIpHashes)
    throw error
  }
}

export function rebuildClientIpUsageRangeWindows(): void {
  refreshClientIpUsageRangeWindows({ full: true })
}

export function pendingClientIpRangeWindowDirtyCountForTest(): number {
  const row = getStatsDatabase()
    .prepare('SELECT COUNT(*) AS total FROM client_ip_range_window_dirty_ips')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

export function clearClientIpRangeWindowDirtyMemoryForTest(): void {
  clientIpRangeWindowDirtyIpHashes.clear()
}

function currentClientIpRangeWindows(): Array<{ startDate: string; endDate: string }> {
  const timezone = usageStatsTimezone()
  const todayKey = dateKey(new Date(), timezone)
  const dates = fixedUsageStatsDateKeys(timezone, todayKey)
  if (!dates.length) return []
  const windows = [
    { startDate: todayKey, endDate: todayKey },
    { startDate: dates[Math.max(0, dates.length - 7)], endDate: todayKey },
    { startDate: dates[0], endDate: todayKey }
  ]
  const seen = new Set<string>()
  const result: Array<{ startDate: string; endDate: string }> = []
  for (const window of windows) {
    const key = `${window.startDate}:${window.endDate}`
    if (seen.has(key)) continue
    seen.add(key)
    result.push(window)
  }
  return result
}

export function listClientIpStats(options: ClientIpStatsListOptions = {}): ClientIpStatsListResult {
  const database = getStatsDatabase()
  const timezone = usageStatsTimezone()
  const range = normalizeAccountUsageStatsRange(options, timezone)
  const lastUsedRange = normalizeClientIpLastUsedRange(options, timezone)
  const rangeReady = clientIpUsageRangeWindowReady(database, range.startDate, range.endDate)
  const pageSize = boundedPageSize(options.pageSize)
  const page = boundedPage(options.page, pageSize)
  if (!rangeReady) {
    return {
      items: [],
      pageUpperBound: 0,
      hasMore: false,
      page,
      pageSize,
      range,
      rangeReady
    }
  }
  const offset = (page - 1) * pageSize
  const policyNow = nowIso()
  const where = buildClientIpRangeWhere(options, range, policyNow, lastUsedRange, timezone)
  const orderBy = clientIpStatsOrderBy(options.sortField, options.sortOrder, options.lastUsedSortScope)
  const fromClause = clientIpStatsFromClause(options)
  const rows = database.prepare(`
    SELECT
      registry.ip_hash, registry.aggregate_ip_key, registry.last_seen_at AS registry_last_seen_at,
      range_stats.request_count, range_stats.success_count, range_stats.error_count,
      range_stats.input_tokens, range_stats.output_tokens, range_stats.cache_read_tokens,
      range_stats.cache_read_cost_usd, range_stats.total_cost_usd,
      range_stats.duration_ms_sum, range_stats.duration_ms_count, range_stats.duration_ms_max,
      range_stats.average_duration_ms,
      range_stats.first_token_ms_sum, range_stats.first_token_ms_count,
      range_stats.average_first_token_ms,
      range_stats.active_days, range_stats.last_used_at, range_stats.last_error_at,
      CASE WHEN ${activePolicyExistsSql('registry.ip_hash')} THEN 1 ELSE 0 END AS blacklisted
    FROM ${fromClause}
    ${where.clause}
    ORDER BY ${orderBy}
    LIMIT ? OFFSET ?
  `).all(policyNow, ...where.params, pageSize + 1, offset) as unknown as ClientIpStatsRangeRow[]
  const pageRows = rows.slice(0, pageSize)
  const hasMore = rows.length > pageSize
  return {
    items: pageRows.map(mapClientIpStatsRangeRow),
    pageUpperBound: pagedTotalUpperBound(page, pageSize, pageRows.length, hasMore),
    hasMore,
    page,
    pageSize,
    range,
    rangeReady
  }
}

export function createClientIpPolicy(input: ClientIpPolicyMutationInput): ClientIpPolicySummary {
  const ipHash = normalizeIpHash(input.ipHash)
  if (!ipHash) {
    throw new Error('IP 标识无效')
  }
  const database = getStatsDatabase()
  const registry = database.prepare('SELECT ip_hash FROM client_ip_registry WHERE ip_hash = ?').get(ipHash) as { ip_hash?: string } | undefined
  if (!registry) {
    throw new Error('IP 不存在')
  }
  const id = newId('ip_policy')
  const now = nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database.prepare(`
      UPDATE client_ip_policies
      SET status = 'disabled',
        disabled_at = ?,
        disabled_by_system_account_id = ?,
        disabled_reason = ?,
        updated_at = ?
      WHERE ip_hash = ?
        AND status = 'active'
    `).run(now, input.actorSystemAccountId, '被新的封禁策略替换', now, ipHash)
    database.prepare(`
      INSERT INTO client_ip_policies (
        id, ip_hash, status, reason, expires_at,
        created_by_system_account_id, created_at, updated_at
      ) VALUES (?, ?, 'active', ?, ?, ?, ?, ?)
    `).run(
      id,
      ipHash,
      normalizeOptionalText(input.reason) ?? null,
      normalizeOptionalIso(input.expiresAt) ?? null,
      input.actorSystemAccountId,
      now,
      now
    )
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  return mapClientIpPolicyRow(database.prepare('SELECT * FROM client_ip_policies WHERE id = ?').get(id) as unknown as ClientIpPolicyRow)
}

export function disableClientIpPolicies(input: ClientIpPolicyDisableInput): { disabledCount: number } {
  const ipHash = normalizeIpHash(input.ipHash)
  if (!ipHash) {
    throw new Error('IP 标识无效')
  }
  const now = nowIso()
  const params: SQLInputValue[] = [
    now,
    input.actorSystemAccountId,
    normalizeOptionalText(input.reason) ?? '管理员解除策略',
    now,
    ipHash
  ]
  const result = getStatsDatabase().prepare(`
    UPDATE client_ip_policies
    SET status = 'disabled',
      disabled_at = ?,
      disabled_by_system_account_id = ?,
      disabled_reason = ?,
      updated_at = ?
    WHERE ip_hash = ?
      AND status = 'active'
  `).run(...params)
  return { disabledCount: Number(result.changes ?? 0) }
}

export function listActiveClientIpPolicies(): ActiveClientIpPolicy[] {
  const now = nowIso()
  const params: SQLInputValue[] = [now]
  const rows = getStatsDatabase().prepare(`
    SELECT policies.id, policies.ip_hash, policies.reason, policies.expires_at,
      registry.aggregate_ip_key, registry.client_ip
    FROM client_ip_policies policies
    INNER JOIN client_ip_registry registry ON registry.ip_hash = policies.ip_hash
    WHERE policies.status = 'active'
      AND (policies.expires_at IS NULL OR policies.expires_at > ?)
    ORDER BY policies.created_at DESC, policies.id DESC
  `).all(...params) as unknown as Array<{
    id: string
    ip_hash: string
    reason: string | null
    expires_at: string | null
    aggregate_ip_key: string
    client_ip: string
  }>
  return rows.map(mapActiveClientIpPolicyRow)
}

export function findActiveClientIpPolicyByHash(inputIpHash: string): ActiveClientIpPolicy | undefined {
  const ipHash = normalizeIpHash(inputIpHash)
  if (!ipHash) {
    return undefined
  }
  const now = nowIso()
  const row = getStatsDatabase().prepare(`
    SELECT policies.id, policies.ip_hash, policies.reason, policies.expires_at,
      registry.aggregate_ip_key, registry.client_ip
    FROM client_ip_policies policies
    INNER JOIN client_ip_registry registry ON registry.ip_hash = policies.ip_hash
    WHERE policies.ip_hash = ?
      AND policies.status = 'active'
      AND (policies.expires_at IS NULL OR policies.expires_at > ?)
    ORDER BY policies.created_at DESC, policies.id DESC
    LIMIT 1
  `).get(ipHash, now) as unknown as {
    id: string
    ip_hash: string
    reason: string | null
    expires_at: string | null
    aggregate_ip_key: string
    client_ip: string
  } | undefined
  return row ? mapActiveClientIpPolicyRow(row) : undefined
}

export function recordClientIpPolicyHits(hits: ClientIpPolicyHitInput[]): { recorded: number } {
  if (!hits.length) return { recorded: 0 }
  const database = getStatsDatabase()
  const updatedAt = nowIso()
  const insert = database.prepare(`
    INSERT INTO client_ip_policy_hits (
      ip_hash, stat_date, policy_id, hit_count, last_hit_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?)
    ON CONFLICT(ip_hash, stat_date, policy_id) DO UPDATE SET
      hit_count = hit_count + excluded.hit_count,
      last_hit_at = CASE
        WHEN client_ip_policy_hits.last_hit_at IS NULL OR excluded.last_hit_at > client_ip_policy_hits.last_hit_at THEN excluded.last_hit_at
        ELSE client_ip_policy_hits.last_hit_at
      END,
      updated_at = excluded.updated_at
  `)
  const transactionStarted = beginDatabaseTransaction(database)
  let recorded = 0
  try {
    for (const hit of hits) {
      const ipHash = normalizeIpHash(hit.ipHash)
      const policyId = normalizeOptionalText(hit.policyId)
      if (!ipHash || !policyId) continue
      const hitAt = normalizeOptionalIso(hit.hitAt) ?? updatedAt
      insert.run(
        ipHash,
        dateKey(new Date(hitAt), usageStatsTimezone()),
        policyId,
        Math.max(1, Math.trunc(Number(hit.hitCount ?? 1))),
        hitAt,
        updatedAt
      )
      recorded += 1
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  return { recorded }
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

function refreshClientIpUsageRangeWindow(database: DatabaseSync, startDate: string, endDate: string, updatedAt: string): void {
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database.prepare('DELETE FROM client_ip_usage_range_windows WHERE start_date = ? AND end_date = ?').run(startDate, endDate)
    database.prepare(`
      INSERT INTO client_ip_usage_range_windows (
        ip_hash, start_date, end_date, request_count, success_count, error_count,
        input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
        duration_ms_sum, duration_ms_count, duration_ms_max, average_duration_ms,
        first_token_ms_sum, first_token_ms_count, average_first_token_ms,
        active_days, last_used_at, last_error_at, updated_at
      )
      SELECT
        ip_hash,
        ?,
        ?,
        COALESCE(SUM(request_count), 0),
        COALESCE(SUM(success_count), 0),
        COALESCE(SUM(error_count), 0),
        COALESCE(SUM(input_tokens), 0),
        COALESCE(SUM(output_tokens), 0),
        COALESCE(SUM(cache_read_tokens), 0),
        COALESCE(SUM(cache_read_cost_usd), 0),
        COALESCE(SUM(total_cost_usd), 0),
        COALESCE(SUM(duration_ms_sum), 0),
        COALESCE(SUM(duration_ms_count), 0),
        COALESCE(MAX(duration_ms_max), 0),
        CASE WHEN COALESCE(SUM(duration_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(duration_ms_sum), 0) AS REAL) / COALESCE(SUM(duration_ms_count), 0) ELSE NULL END,
        COALESCE(SUM(first_token_ms_sum), 0),
        COALESCE(SUM(first_token_ms_count), 0),
        CASE WHEN COALESCE(SUM(first_token_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(first_token_ms_sum), 0) AS REAL) / COALESCE(SUM(first_token_ms_count), 0) ELSE NULL END,
        COALESCE(SUM(CASE WHEN request_count > 0 THEN 1 ELSE 0 END), 0),
        MAX(last_used_at),
        MAX(last_error_at),
        ?
      FROM client_ip_stats_daily
      WHERE stat_date >= ?
        AND stat_date <= ?
      GROUP BY ip_hash
      HAVING COALESCE(SUM(request_count), 0) > 0
        OR COALESCE(SUM(input_tokens), 0) > 0
        OR COALESCE(SUM(output_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
        OR COALESCE(SUM(total_cost_usd), 0) > 0
    `).run(startDate, endDate, updatedAt, startDate, endDate)
    markClientIpUsageRangeWindowReady(database, startDate, endDate, updatedAt)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function refreshClientIpUsageRangeWindowForIps(database: DatabaseSync, startDate: string, endDate: string, ipHashes: string[], updatedAt: string): void {
  if (!ipHashes.length) return
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const chunk of chunkValues(ipHashes, clientIpRangeWindowChunkSize)) {
      const placeholders = sqlPlaceholders(chunk.length)
      database.prepare(`
        DELETE FROM client_ip_usage_range_windows
        WHERE start_date = ?
          AND end_date = ?
          AND ip_hash IN (${placeholders})
      `).run(startDate, endDate, ...chunk)
      database.prepare(`
        INSERT INTO client_ip_usage_range_windows (
          ip_hash, start_date, end_date, request_count, success_count, error_count,
          input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
          duration_ms_sum, duration_ms_count, duration_ms_max, average_duration_ms,
          first_token_ms_sum, first_token_ms_count, average_first_token_ms,
          active_days, last_used_at, last_error_at, updated_at
        )
        SELECT
          ip_hash,
          ?,
          ?,
          COALESCE(SUM(request_count), 0),
          COALESCE(SUM(success_count), 0),
          COALESCE(SUM(error_count), 0),
          COALESCE(SUM(input_tokens), 0),
          COALESCE(SUM(output_tokens), 0),
          COALESCE(SUM(cache_read_tokens), 0),
          COALESCE(SUM(cache_read_cost_usd), 0),
          COALESCE(SUM(total_cost_usd), 0),
          COALESCE(SUM(duration_ms_sum), 0),
          COALESCE(SUM(duration_ms_count), 0),
          COALESCE(MAX(duration_ms_max), 0),
          CASE WHEN COALESCE(SUM(duration_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(duration_ms_sum), 0) AS REAL) / COALESCE(SUM(duration_ms_count), 0) ELSE NULL END,
          COALESCE(SUM(first_token_ms_sum), 0),
          COALESCE(SUM(first_token_ms_count), 0),
          CASE WHEN COALESCE(SUM(first_token_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(first_token_ms_sum), 0) AS REAL) / COALESCE(SUM(first_token_ms_count), 0) ELSE NULL END,
          COALESCE(SUM(CASE WHEN request_count > 0 THEN 1 ELSE 0 END), 0),
          MAX(last_used_at),
          MAX(last_error_at),
          ?
        FROM client_ip_stats_daily
        WHERE stat_date >= ?
          AND stat_date <= ?
          AND ip_hash IN (${placeholders})
        GROUP BY ip_hash
      HAVING COALESCE(SUM(request_count), 0) > 0
        OR COALESCE(SUM(input_tokens), 0) > 0
        OR COALESCE(SUM(output_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_tokens), 0) > 0
        OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
        OR COALESCE(SUM(total_cost_usd), 0) > 0
      `).run(startDate, endDate, updatedAt, startDate, endDate, ...chunk)
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function clientIpUsageRangeWindowReady(database: DatabaseSync, startDate: string, endDate: string): boolean {
  const windowState = database.prepare(`
    SELECT last_success_at
    FROM stats_job_state
    WHERE scope_type = ?
      AND scope_id = ?
      AND job_name = ?
    LIMIT 1
  `).get(clientIpRangeWindowScopeType, clientIpRangeWindowScopeId(startDate, endDate), clientIpRangeWindowJobName) as unknown as { last_success_at?: string | null } | undefined
  if (windowState) return Boolean(windowState.last_success_at)
  if (hasPendingClientIpRangeWindowDirtyIpHashes(database)) return false
  const row = database.prepare('SELECT 1 FROM client_ip_usage_range_windows WHERE start_date = ? AND end_date = ? LIMIT 1')
    .get(startDate, endDate) as unknown as { 1?: number } | undefined
  return Boolean(row)
}

function markClientIpUsageRangeWindowReady(database: DatabaseSync, startDate: string, endDate: string, updatedAt: string): void {
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, last_success_at, updated_at)
    VALUES (?, ?, ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      last_success_at = excluded.last_success_at,
      last_error_message = NULL,
      updated_at = excluded.updated_at
  `).run(clientIpRangeWindowScopeType, clientIpRangeWindowScopeId(startDate, endDate), clientIpRangeWindowJobName, updatedAt, updatedAt)
}

function markClientIpUsageRangeWindowsReady(database: DatabaseSync, windows: Array<{ startDate: string; endDate: string }>, updatedAt: string): void {
  for (const window of windows) {
    markClientIpUsageRangeWindowReady(database, window.startDate, window.endDate, updatedAt)
  }
}

function hasStaleClientIpUsageRangeWindows(database: DatabaseSync, windows: Array<{ startDate: string; endDate: string }>): boolean {
  const statement = database.prepare(`
    SELECT last_success_at
    FROM stats_job_state
    WHERE scope_type = ?
      AND scope_id = ?
      AND job_name = ?
    LIMIT 1
  `)
  for (const window of windows) {
    const row = statement.get(clientIpRangeWindowScopeType, clientIpRangeWindowScopeId(window.startDate, window.endDate), clientIpRangeWindowJobName) as
      | { last_success_at?: string | null }
      | undefined
    if (row && !row.last_success_at) return true
  }
  return false
}

function markCurrentClientIpUsageRangeWindowsStale(database: DatabaseSync): void {
  const windows = currentClientIpRangeWindows()
  if (!windows.length) return
  const updatedAt = nowIso()
  const staleStatement = database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, last_success_at, updated_at)
    VALUES (?, ?, ?, NULL, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      last_success_at = NULL,
      updated_at = excluded.updated_at
  `)
  for (const window of windows) {
    staleStatement.run(clientIpRangeWindowScopeType, clientIpRangeWindowScopeId(window.startDate, window.endDate), clientIpRangeWindowJobName, updatedAt)
  }
}

function clientIpRangeWindowScopeId(startDate: string, endDate: string): string {
  return `${startDate}:${endDate}`
}

function markClientIpRangeWindowsDirty(database: DatabaseSync, ipHashes: Iterable<string>, updatedAt = nowIso()): void {
  const dirtyStatement = database.prepare(`
    INSERT INTO client_ip_range_window_dirty_ips (ip_hash, updated_at)
    VALUES (?, ?)
    ON CONFLICT(ip_hash) DO UPDATE SET
      updated_at = excluded.updated_at
  `)
  for (const ipHash of ipHashes) {
    clientIpRangeWindowDirtyIpHashes.add(ipHash)
    dirtyStatement.run(ipHash, updatedAt)
  }
}

function takeClientIpRangeWindowDirtyIpHashes(database: DatabaseSync, limit: number): string[] {
  const max = Math.max(1, Math.trunc(limit))
  const result: string[] = []
  for (const ipHash of clientIpRangeWindowDirtyIpHashes) {
    result.push(ipHash)
    if (result.length >= max) break
  }
  if (result.length < max) {
    const rows = database.prepare(`
      SELECT ip_hash
      FROM client_ip_range_window_dirty_ips
      ORDER BY updated_at ASC, ip_hash ASC
      LIMIT ?
    `).all(max) as Array<{ ip_hash?: string }>
    for (const row of rows) {
      const ipHash = row.ip_hash
      if (!ipHash || result.includes(ipHash)) continue
      result.push(ipHash)
      clientIpRangeWindowDirtyIpHashes.add(ipHash)
      if (result.length >= max) break
    }
  }
  return result
}

function clearClientIpRangeWindowDirtyIpHashes(database: DatabaseSync, ipHashes: string[]): void {
  if (!ipHashes.length) return
  for (const chunk of chunkValues(ipHashes, clientIpRangeWindowChunkSize)) {
    const placeholders = sqlPlaceholders(chunk.length)
    database.prepare(`DELETE FROM client_ip_range_window_dirty_ips WHERE ip_hash IN (${placeholders})`).run(...chunk)
    for (const ipHash of chunk) {
      clientIpRangeWindowDirtyIpHashes.delete(ipHash)
    }
  }
}

function clearAllClientIpRangeWindowDirtyIpHashes(database: DatabaseSync): void {
  clientIpRangeWindowDirtyIpHashes.clear()
  database.prepare('DELETE FROM client_ip_range_window_dirty_ips').run()
}

function hasPendingClientIpRangeWindowDirtyIpHashes(database: DatabaseSync): boolean {
  if (clientIpRangeWindowDirtyIpHashes.size > 0) return true
  const row = database.prepare('SELECT 1 FROM client_ip_range_window_dirty_ips LIMIT 1').get() as { 1?: number } | undefined
  return Boolean(row)
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

function buildClientIpRangeWhere(
  options: ClientIpStatsListOptions,
  range: AccountUsageStatsRange,
  policyNow: string,
  lastUsedRange: AccountUsageStatsRange | undefined,
  timezone: string
): ClientIpRangeWhere {
  const clauses = ['range_stats.start_date = ?', 'range_stats.end_date = ?']
  const params: SQLInputValue[] = [range.startDate, range.endDate]
  const lastUsedWindow = lastUsedRange ? clientIpLastUsedIsoWindow(lastUsedRange, timezone) : undefined
  if (lastUsedWindow) {
    clauses.push('registry.last_seen_at >= ? AND registry.last_seen_at < ?')
    params.push(lastUsedWindow.startIso, lastUsedWindow.endExclusiveIso)
  }
  const keyword = options.keyword?.trim()
  if (keyword) {
    const keywordPrefix = `${escapeSqlLike(keyword)}%`
    clauses.push("(registry.aggregate_ip_key = ? OR registry.aggregate_ip_key LIKE ? ESCAPE '\\' OR registry.client_ip = ? OR registry.client_ip LIKE ? ESCAPE '\\')")
    params.push(keyword, keywordPrefix, keyword, keywordPrefix)
  }
  const status = options.status ?? 'all'
  if (status === 'blacklisted') {
    clauses.push(activePolicyExistsSql('registry.ip_hash'))
    params.push(policyNow)
  } else if (status === 'normal') {
    clauses.push(`NOT ${activePolicyExistsSql('registry.ip_hash')}`)
    params.push(policyNow)
  }
  return {
    clause: `WHERE ${clauses.join(' AND ')}`,
    params
  }
}

function normalizeClientIpLastUsedRange(options: ClientIpStatsListOptions, timezone: string): AccountUsageStatsRange | undefined {
  if (!options.lastUsedStartDate && !options.lastUsedEndDate) return undefined
  return normalizeAccountUsageStatsRange({
    startDate: options.lastUsedStartDate,
    endDate: options.lastUsedEndDate
  }, timezone)
}

function clientIpLastUsedIsoWindow(range: AccountUsageStatsRange, timezone: string): { startIso: string; endExclusiveIso: string } | undefined {
  const startIso = startOfZonedDateKeyIso(range.startDate, timezone)
  const endExclusiveIso = startOfZonedDateKeyIso(nextDateKey(range.endDate), timezone)
  if (!startIso || !endExclusiveIso) return undefined
  return { startIso, endExclusiveIso }
}

function activePolicyExistsSql(ipHashExpression: string): string {
  return `EXISTS (
    SELECT 1
    FROM client_ip_policies active_policies
    WHERE active_policies.status = 'active'
      AND active_policies.ip_hash = ${ipHashExpression}
      AND (active_policies.expires_at IS NULL OR active_policies.expires_at > ?)
    LIMIT 1
  )`
}

function clientIpStatsFromClause(options: ClientIpStatsListOptions): string {
  if (options.sortField === 'lastUsedAt' && options.lastUsedSortScope === 'global') {
    return 'client_ip_registry registry INDEXED BY idx_client_ip_registry_last_seen INNER JOIN client_ip_usage_range_windows range_stats ON registry.ip_hash = range_stats.ip_hash'
  }
  return 'client_ip_usage_range_windows range_stats INNER JOIN client_ip_registry registry ON registry.ip_hash = range_stats.ip_hash'
}

function clientIpStatsOrderBy(field: ClientIpStatsSortField | undefined, order: 'asc' | 'desc' | undefined, lastUsedSortScope: ClientIpLastUsedSortScope = 'range'): string {
  const direction = order === 'asc' ? 'ASC' : 'DESC'
  switch (field) {
    case 'successCount':
      return `range_stats.success_count ${direction}, range_stats.ip_hash ASC`
    case 'errorCount':
      return `range_stats.error_count ${direction}, range_stats.ip_hash ASC`
    case 'errorRate':
      return `CASE WHEN range_stats.request_count > 0 THEN CAST(range_stats.error_count AS REAL) / range_stats.request_count ELSE 0 END ${direction}, range_stats.ip_hash ASC`
    case 'totalTokens':
      return `(range_stats.input_tokens + range_stats.output_tokens) ${direction}, range_stats.ip_hash ASC`
    case 'activeDays':
      return `range_stats.active_days ${direction}, range_stats.ip_hash ASC`
    case 'lastUsedAt':
      return lastUsedSortScope === 'global'
        ? `registry.last_seen_at ${direction}, registry.ip_hash ${direction === 'ASC' ? 'DESC' : 'ASC'}`
        : `range_stats.last_used_at ${direction}, range_stats.ip_hash ASC`
    case 'requestCount':
      return `range_stats.request_count ${direction}, range_stats.ip_hash ASC`
    case 'totalCost':
      return `range_stats.total_cost_usd ${direction}, range_stats.ip_hash ASC`
    default:
      return 'range_stats.request_count DESC, range_stats.ip_hash ASC'
  }
}

function mapClientIpStatsRangeRow(row: ClientIpStatsRangeRow): ClientIpStatsRow {
  const rangeUsage = usageSummaryFromRow(row)
  const blacklisted = Number(row.blacklisted ?? 0) > 0
  return {
    ipHash: row.ip_hash,
    aggregateIpKey: row.aggregate_ip_key,
    lastSeenAt: row.registry_last_seen_at ?? undefined,
    status: blacklisted ? 'blacklisted' : 'normal',
    rangeUsage
  }
}

function mapActiveClientIpPolicyRow(row: {
  id: string
  ip_hash: string
  reason: string | null
  expires_at: string | null
  aggregate_ip_key: string
  client_ip: string
}): ActiveClientIpPolicy {
  return {
    id: row.id,
    ipHash: row.ip_hash,
    aggregateIpKey: row.aggregate_ip_key,
    clientIp: row.client_ip,
    reason: row.reason ?? undefined,
    expiresAt: row.expires_at ?? undefined
  }
}

function mapClientIpPolicyRow(row: ClientIpPolicyRow): ClientIpPolicySummary {
  return {
    id: row.id,
    ipHash: row.ip_hash,
    status: row.status === 'disabled' ? 'disabled' : 'active',
    reason: row.reason ?? undefined,
    expiresAt: row.expires_at ?? undefined,
    createdBySystemAccountId: row.created_by_system_account_id,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
    disabledAt: row.disabled_at ?? undefined,
    disabledBySystemAccountId: row.disabled_by_system_account_id ?? undefined,
    disabledReason: row.disabled_reason ?? undefined
  }
}

function usageSummaryFromRow(row: Partial<ClientIpStatsUsageRow> | undefined): ClientIpUsageSummary {
  const requestCount = Number(row?.request_count ?? 0)
  const successCount = Number(row?.success_count ?? 0)
  const errorCount = Number(row?.error_count ?? 0)
  const inputTokens = Number(row?.input_tokens ?? 0)
  const outputTokens = Number(row?.output_tokens ?? 0)
  const durationMsCount = Number(row?.duration_ms_count ?? 0)
  const firstTokenMsCount = Number(row?.first_token_ms_count ?? 0)
  const durationMsMax = Number(row?.duration_ms_max ?? 0)
  const averageDurationMs = row?.average_duration_ms == null
    ? (durationMsCount > 0 ? Number(row?.duration_ms_sum ?? 0) / durationMsCount : undefined)
    : Number(row.average_duration_ms)
  const averageFirstTokenMs = row?.average_first_token_ms == null
    ? (firstTokenMsCount > 0 ? Number(row?.first_token_ms_sum ?? 0) / firstTokenMsCount : undefined)
    : Number(row.average_first_token_ms)
  return {
    requestCount,
    successCount,
    errorCount,
    errorRate: requestCount > 0 ? errorCount / requestCount : 0,
    inputTokens,
    outputTokens,
    cacheReadTokens: Number(row?.cache_read_tokens ?? 0),
    cacheReadCost: Number(row?.cache_read_cost_usd ?? 0),
    totalTokens: inputTokens + outputTokens,
    totalCost: Number(row?.total_cost_usd ?? 0),
    activeDays: Number(row?.active_days ?? 0),
    averageDurationMs: Number.isFinite(averageDurationMs) ? averageDurationMs : undefined,
    averageFirstTokenMs: Number.isFinite(averageFirstTokenMs) ? averageFirstTokenMs : undefined,
    maxDurationMs: durationMsCount > 0 && durationMsMax > 0 ? durationMsMax : undefined,
    lastUsedAt: row?.last_used_at ?? undefined,
    lastErrorAt: row?.last_error_at ?? undefined
  }
}

function normalizePlainClientIp(value?: string | null): string | undefined {
  if (!value) return undefined
  let ip = value.trim()
  if (!ip) return undefined
  if (ip.includes(',')) {
    ip = ip.split(',')[0].trim()
  }
  const zoneIndex = ip.indexOf('%')
  if (zoneIndex > 0) {
    ip = ip.slice(0, zoneIndex)
  }
  if (ip.startsWith('[')) {
    const end = ip.indexOf(']')
    if (end > 0) ip = ip.slice(1, end)
  }
  if (/^\d{1,3}(?:\.\d{1,3}){3}:\d+$/.test(ip)) {
    ip = ip.replace(/:\d+$/, '')
  }
  if (ip.toLowerCase().startsWith('::ffff:')) {
    ip = ip.slice('::ffff:'.length)
  }
  return ip.toLowerCase()
}

function normalizeIpv4(value: string): string | undefined {
  if (isIP(value) !== 4) return undefined
  const parts = value.split('.').map((part) => Number(part))
  if (parts.length !== 4 || parts.some((part) => !Number.isInteger(part) || part < 0 || part > 255)) return undefined
  return parts.join('.')
}

function clientIpIdentity(clientIp: string, aggregateIpKey: string, ipVersion: 4): NormalizedClientIp {
  const ipHash = createHash('sha256').update(`client-ip:${aggregateIpKey}`).digest('hex')
  return {
    clientIp,
    aggregateIpKey,
    ipVersion,
    ipHash,
    bucketNo: Number.parseInt(ipHash.slice(0, 8), 16) % clientIpRegistryBucketCount
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

function boundedPage(value: unknown, pageSize: number): number {
  const number = Number(value)
  const maxPage = Math.max(1, Math.floor((clientIpStatsMaxListWindowRows - 1) / Math.max(1, Math.trunc(pageSize))))
  return Number.isFinite(number) ? Math.min(Math.max(1, Math.trunc(number)), maxPage) : 1
}

function boundedPageSize(value: unknown): number {
  const number = Number(value)
  return Number.isFinite(number) ? Math.min(Math.max(1, Math.trunc(number)), 100) : 20
}

function normalizeOptionalText(value?: string | null): string | undefined {
  const text = value?.trim()
  return text || undefined
}

function normalizeOptionalIso(value?: string | null): string | undefined {
  const text = normalizeOptionalText(value)
  if (!text) return undefined
  const time = Date.parse(text)
  return Number.isFinite(time) ? new Date(time).toISOString() : undefined
}

function normalizeIpHash(value: string): string | undefined {
  const text = value.trim().toLowerCase()
  return /^[0-9a-f]{64}$/.test(text) ? text : undefined
}

function escapeSqlLike(value: string): string {
  return value.replace(/[\\%_]/g, (match) => `\\${match}`)
}

interface ClientIpStatsUsageRow {
  request_count: number
  success_count: number
  error_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_read_cost_usd: number
  total_cost_usd: number
  duration_ms_sum: number
  duration_ms_count: number
  duration_ms_max: number
  average_duration_ms: number | null
  first_token_ms_sum: number
  first_token_ms_count: number
  average_first_token_ms: number | null
  active_days: number
  last_used_at: string | null
  last_error_at: string | null
}

interface ClientIpStatsRangeRow extends ClientIpStatsUsageRow {
  ip_hash: string
  aggregate_ip_key: string
  registry_last_seen_at: string | null
  blacklisted: number
}

interface ClientIpPolicyRow {
  id: string
  ip_hash: string
  status: string
  reason: string | null
  expires_at: string | null
  created_by_system_account_id: string
  created_at: string
  updated_at: string
  disabled_at: string | null
  disabled_by_system_account_id: string | null
  disabled_reason: string | null
}
