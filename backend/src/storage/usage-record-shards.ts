import { existsSync, mkdirSync } from 'node:fs'
import { unlink } from 'node:fs/promises'
import { dirname, isAbsolute, join, normalize, relative, resolve } from 'node:path'
import { DatabaseSync, type SQLInputValue } from 'node:sqlite'

import { defaultUsageShardRoot, runtimeConfig } from '../config/runtime.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getUsageCatalogDatabase, nowIso, rollbackDatabaseTransaction, sqliteWriterBoundaryStrictModeEnabled, usageCatalogDatabasePath } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { sqliteBusyTimeoutMs } from './sqlite-config.js'
import { checkpointSqliteWal, type SqliteWalCheckpointResult } from './sqlite-maintenance.js'

export interface UsageRecordShardLocation {
  shardKey: string
  bucketDate: string
  bucketDateKey: string
  shardId: number
  filePath: string
}

export interface UsageRecordShardQueryWindow {
  startAt?: string
  endAt?: string
}

export interface UsageRecordShardLocationWindow {
  locations: UsageRecordShardLocation[]
  hasMore: boolean
}

export interface UsageRecordShardLocationPage {
  locations: UsageRecordShardLocation[]
  hasMore: boolean
  total: number
}

export interface EmptyUsageRecordShardFileCleanupResult {
  usageRecordShards: number
  usageShardFiles: number
  hasMore: boolean
}

export interface UsageRecordShardEntryInput {
  id: string
  shardKey: string
  systemAccountId: string
  traceId: string
  apiKeyId?: string | null
  accountId?: string | null
  groupId?: string | null
  model?: string | null
  trafficSource: string
  success: boolean
  failureAttribution?: string | null
  statusCode?: number | null
  clientIp?: string | null
  firstTokenMs?: number | null
  durationMs?: number | null
  costUsd?: number | null
  createdAt: string
}

export interface UsageRecordShardWriteRow {
  id: string
  params: SQLInputValue[]
  accountId?: string
  accountLastUsedAt?: string
  accountHealthSuccessAt?: string
}

export interface UsageRecordShardWriteResult {
  insertedRows: number
  accountLastUsedAt: Array<{ accountId: string; lastUsedAt: string }>
  accountHealthSuccessAt: Array<{ accountId: string; successAt: string }>
}

interface UsageRecordShardEntryScope {
  usageId: string
  shardKey: string
  systemAccountId: string
  apiKeyId?: string | null
  accountId?: string | null
}

const usageRecordShardSchemaVersion = 3
const usageRecordShardWindowMaxDays = 31
const shardDatabases = new Map<string, DatabaseSync>()
const registeredUsageRecordShardKeys = new Set<string>()
const usageRecordInsertSql = `
  INSERT INTO usage_records (
    id, system_account_id, trace_id, traffic_source, client_ip, api_key_id, group_id, account_id, endpoint, provider_code, provider_protocol_profile_id, usage_semantic, model, upstream_model, pricing_model, model_mapping_applied, model_mapping_source, stream,
    status_code, success, failure_attribution, first_token_ms, duration_ms, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, cost_usd, error_code, error_message,
    request_snapshot_json, response_snapshot_json,
    account_owner_system_account_id, group_owner_system_account_id, account_access_type, group_access_type,
    account_authorization_id, account_authorization_source_type, account_authorization_source_team_id,
    group_authorization_id, group_authorization_source_type, group_authorization_source_team_id,
    created_at
  ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  ON CONFLICT(id) DO NOTHING
`

export function usageRecordShardCount(): number {
  return Math.max(1, Math.trunc(runtimeConfig.usageShardCount))
}

export function usageRecordShardRoot(): string {
  if (isDefaultUsageShardRoot(runtimeConfig.usageShardRoot)) {
    return resolve(dirname(usageCatalogDatabasePath()), 'usage-shards')
  }
  return resolve(runtimeConfig.usageShardRoot)
}

export function generateUsageRecordId(createdAt: string, entropy: string): string {
  const bucketDateKey = bucketDateKeyFromIso(createdAt)
  const shardId = stableShardId(entropy)
  return `usage_${bucketDateKey}_s${formatShardId(shardId)}_${Date.now()}_${entropy.replace(/[^a-zA-Z0-9]/g, '').slice(0, 24)}`
}

export function usageRecordShardLocationForRecord(id: string, createdAt?: string): UsageRecordShardLocation {
  const parsed = parseUsageRecordShardId(id)
  const bucketDateKey = parsed?.bucketDateKey ?? bucketDateKeyFromIso(createdAt)
  const shardId = parsed?.shardId ?? stableShardId(id)
  return usageRecordShardLocation(bucketDateKey, shardId)
}

export function usageRecordShardLocationFromKey(shardKey: string): UsageRecordShardLocation | undefined {
  const match = /^(\d{8}):s(\d+)$/.exec(shardKey.trim())
  if (!match) return undefined
  return usageRecordShardLocation(match[1], Number(match[2]))
}

export function findRegisteredUsageRecordShardLocation(shardKey: string): UsageRecordShardLocation | undefined {
  return findRegisteredUsageRecordShardLocationByKey(shardKey)
}

export function getUsageRecordShardDatabase(location: UsageRecordShardLocation, options: { registerLocation?: boolean } = {}): DatabaseSync {
  const cached = shardDatabases.get(location.filePath)
  if (cached) {
    return cached
  }
  mkdirSync(dirname(location.filePath), { recursive: true })
  const database = new DatabaseSync(location.filePath)
  configureUsageRecordShardDatabase(database)
  if (shouldApplyUsageRecordShardSchema()) {
    applyUsageRecordShardSchema(database)
  }
  shardDatabases.set(location.filePath, database)
  if (shouldApplyUsageRecordShardSchema() && options.registerLocation !== false) {
    registerUsageRecordShardLocation(location)
  }
  return database
}

export function currentProcessOwnsUsageShardWriter(): boolean {
  return runtimeConfig.processRole === 'worker' && runtimeConfig.workerRole === 'ingest-worker'
}

export function closeUsageRecordShardDatabases(): void {
  for (const database of shardDatabases.values()) {
    try {
      database.close()
    } catch {
    }
  }
  shardDatabases.clear()
  registeredUsageRecordShardKeys.clear()
}

export interface UsageRecordShardSchemaExecutor {
  exec(sql: string): void
}

export function applyUsageRecordShardBaseSchema(database: UsageRecordShardSchemaExecutor): void {
  database.exec(`
    CREATE TABLE IF NOT EXISTS usage_records (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      trace_id TEXT NOT NULL,
      traffic_source TEXT NOT NULL,
      client_ip TEXT,
      api_key_id TEXT,
      group_id TEXT,
      account_id TEXT,
      endpoint TEXT,
      provider_code TEXT,
      provider_protocol_profile_id TEXT,
      usage_semantic TEXT,
      model TEXT,
      upstream_model TEXT,
      pricing_model TEXT,
      model_mapping_applied INTEGER NOT NULL DEFAULT 0,
      model_mapping_source TEXT,
      stream INTEGER NOT NULL DEFAULT 0,
      status_code INTEGER,
      success INTEGER NOT NULL DEFAULT 0,
      failure_attribution TEXT,
      first_token_ms INTEGER,
      duration_ms INTEGER,
      input_tokens INTEGER,
      output_tokens INTEGER,
      cache_read_tokens INTEGER,
      cache_read_cost_usd REAL,
      cache_write_tokens INTEGER,
      cache_write_1h_tokens INTEGER,
      cache_write_cost_usd REAL,
      thinking_tokens INTEGER,
      input_image_tokens INTEGER,
      output_image_tokens INTEGER,
      cost_usd REAL,
      error_code TEXT,
      error_message TEXT,
      request_snapshot_json TEXT,
      response_snapshot_json TEXT,
      account_owner_system_account_id TEXT,
      group_owner_system_account_id TEXT,
      account_access_type TEXT,
      group_access_type TEXT,
      account_authorization_id TEXT,
      account_authorization_source_type TEXT,
      account_authorization_source_team_id TEXT,
      group_authorization_id TEXT,
      group_authorization_source_type TEXT,
      group_authorization_source_team_id TEXT,
      created_at TEXT NOT NULL
    );

    CREATE INDEX IF NOT EXISTS idx_usage_records_created_at ON usage_records(created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_created_at ON usage_records(system_account_id, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_created_sort ON usage_records(system_account_id, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_account_owner ON usage_records(account_owner_system_account_id, account_id, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_records_group_owner ON usage_records(group_owner_system_account_id, group_id, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_records_group_real_usage ON usage_records(group_id, created_at, api_key_id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_group_created_sort ON usage_records(group_id, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_group_created_sort ON usage_records(system_account_id, group_id, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_account_authorization ON usage_records(account_authorization_id, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_records_group_authorization ON usage_records(group_authorization_id, created_at);
    CREATE INDEX IF NOT EXISTS idx_usage_records_first_token_sort ON usage_records(first_token_ms, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_duration_sort ON usage_records(duration_ms, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_cost_sort ON usage_records(cost_usd, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_first_token_sort ON usage_records(system_account_id, first_token_ms, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_duration_sort ON usage_records(system_account_id, duration_ms, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_cost_sort ON usage_records(system_account_id, cost_usd, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_stats_cursor ON usage_records(created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_api_key_created_sort ON usage_records(api_key_id, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_account_created_sort ON usage_records(account_id, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_trace_created_sort ON usage_records(trace_id, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_trace_created_sort ON usage_records(system_account_id, trace_id, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_model_created_sort ON usage_records(model, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_model_created_sort ON usage_records(system_account_id, model, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_traffic_source_created ON usage_records(traffic_source, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_client_ip_created_sort ON usage_records(client_ip, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_client_ip_created_sort ON usage_records(system_account_id, client_ip, created_at, id);
  `)
}

export function checkpointOpenUsageRecordShardDatabases(): SqliteWalCheckpointResult[] {
  const results: SqliteWalCheckpointResult[] = []
  for (const [filePath, database] of shardDatabases.entries()) {
    const result = checkpointSqliteWal(database, `usage-shard:${filePath}`)
    if (result) {
      results.push(result)
    }
  }
  return results
}

export async function cleanupEmptyUsageRecordShardFilesBefore(cutoffAt: string, limit = 1000): Promise<EmptyUsageRecordShardFileCleanupResult> {
  const cutoffDate = bucketDateFromBucketDateKey(bucketDateKeyFromIso(cutoffAt))
  const batchLimit = Math.max(1, Math.trunc(limit))
  const rows = getUsageCatalogDatabase()
    .prepare(`
      SELECT s.shard_key, s.bucket_date, s.shard_id, s.file_path
      FROM usage_record_shards s
      WHERE s.status = 'active'
        AND s.bucket_date <= ?
        AND NOT EXISTS (
          SELECT 1
          FROM usage_record_shard_entries ue
          WHERE ue.shard_key = s.shard_key
          LIMIT 1
        )
      ORDER BY s.bucket_date ASC, s.shard_id ASC
      LIMIT ?
    `)
    .all(cutoffDate, batchLimit + 1) as Array<{ shard_key?: string; bucket_date?: string; shard_id?: number; file_path?: string }>
  const locations = rows
    .slice(0, batchLimit)
    .map(usageRecordShardLocationFromRegistryRow)
    .filter((location): location is UsageRecordShardLocation => Boolean(location))
  if (locations.length === 0) {
    return { usageRecordShards: 0, usageShardFiles: 0, hasMore: rows.length > batchLimit }
  }

  closeUsageRecordShardDatabases()
  let usageShardFiles = 0
  for (const location of locations) {
    usageShardFiles += await deleteUsageShardFileSet(location.filePath)
  }

  let usageRecordShards = 0
  const shardKeys = locations.map((location) => location.shardKey)
  for (const chunk of chunkValues(shardKeys, 900)) {
    usageRecordShards += Number(getUsageCatalogDatabase()
      .prepare(`DELETE FROM usage_record_shards WHERE shard_key IN (${sqlPlaceholders(chunk.length)})`)
      .run(...chunk).changes ?? 0)
    for (const shardKey of chunk) {
      registeredUsageRecordShardKeys.delete(shardKey)
    }
  }

  return {
    usageRecordShards,
    usageShardFiles,
    hasMore: rows.length > batchLimit
  }
}

export function listUsageRecordShardLocations(window: UsageRecordShardQueryWindow = {}): UsageRecordShardLocation[] {
  const startDateKey = window.startAt ? bucketDateKeyFromIso(window.startAt) : undefined
  const endDateKey = window.endAt ? bucketDateKeyFromIso(window.endAt) : undefined
  if (startDateKey || endDateKey) {
    return listUsageRecordShardLocationsForDateWindow(startDateKey, endDateKey)
  }
  return listRegisteredUsageRecordShardLocations()
}

export function listUsageRecordShardLocationsPage(input: { offset?: number; limit: number }): UsageRecordShardLocationPage {
  const limit = Math.max(1, Math.trunc(input.limit))
  const offset = Math.max(0, Math.trunc(input.offset ?? 0))
  const database = getUsageCatalogDatabase()
  const totalRow = database
    .prepare("SELECT COUNT(*) AS total FROM usage_record_shards WHERE status = 'active'")
    .get() as { total?: number } | undefined
  const total = Math.max(0, Number(totalRow?.total ?? 0))
  if (total === 0) {
    return { locations: [], hasMore: false, total: 0 }
  }

  const normalizedOffset = offset % total
  const requestedRows = Math.min(limit + 1, total)
  const firstRows = listRegisteredUsageRecordShardLocations({
    offset: normalizedOffset,
    limit: requestedRows
  })
  const wrappedRows = firstRows.length < requestedRows && normalizedOffset > 0
    ? listRegisteredUsageRecordShardLocations({
        offset: 0,
        limit: requestedRows - firstRows.length
      })
    : []
  const rows = [...firstRows, ...wrappedRows]
  return {
    locations: rows.slice(0, limit),
    hasMore: total > limit,
    total
  }
}

export function listUsageRecordShardLocationsForAccount(accountId: string, limit = 64): UsageRecordShardLocationWindow {
  const normalizedAccountId = accountId.trim()
  if (!normalizedAccountId) {
    return { locations: [], hasMore: false }
  }
  return listUsageRecordShardLocationsByScopeCatalog({
    tableName: 'usage_record_account_shards',
    whereClause: 'c.account_id = ?',
    params: [normalizedAccountId],
    limit
  })
}

export function listUsageRecordShardLocationsForApiKey(apiKeyId: string, systemAccountId: string, limit = 64): UsageRecordShardLocationWindow {
  const normalizedApiKeyId = apiKeyId.trim()
  const normalizedSystemAccountId = systemAccountId.trim()
  if (!normalizedApiKeyId || !normalizedSystemAccountId) {
    return { locations: [], hasMore: false }
  }
  return listUsageRecordShardLocationsByScopeCatalog({
    tableName: 'usage_record_api_key_shards',
    whereClause: 'c.api_key_id = ? AND c.system_account_id = ?',
    params: [normalizedApiKeyId, normalizedSystemAccountId],
    limit
  })
}

function listUsageRecordShardLocationsForDateWindow(startDateKey?: string, endDateKey?: string): UsageRecordShardLocation[] {
  const endKey = endDateKey ?? startDateKey ?? bucketDateKeyFromDate(new Date())
  const startKey = startDateKey ?? endKey
  const startMs = bucketDateKeyToUtcMs(startKey)
  const endMs = bucketDateKeyToUtcMs(endKey)
  const ascendingStartMs = Math.min(startMs, endMs)
  const ascendingEndMs = Math.max(startMs, endMs)
  const totalDays = Math.floor((ascendingEndMs - ascendingStartMs) / dayMs) + 1
  const days = Math.max(1, Math.min(totalDays, usageRecordShardWindowMaxDays))
  const boundedStartMs = ascendingEndMs - (days - 1) * dayMs
  return listRegisteredUsageRecordShardLocations({
    startBucketDate: bucketDateFromBucketDateKey(bucketDateKeyFromDate(new Date(boundedStartMs))),
    endBucketDate: bucketDateFromBucketDateKey(bucketDateKeyFromDate(new Date(ascendingEndMs)))
  })
}

function listRegisteredUsageRecordShardLocations(input: {
  startBucketDate?: string
  endBucketDate?: string
  offset?: number
  limit?: number
} = {}): UsageRecordShardLocation[] {
  const clauses = ["status = 'active'"]
  const params: SQLInputValue[] = []
  if (input.startBucketDate) {
    clauses.push('bucket_date >= ?')
    params.push(input.startBucketDate)
  }
  if (input.endBucketDate) {
    clauses.push('bucket_date <= ?')
    params.push(input.endBucketDate)
  }
  const normalizedLimit = typeof input.limit === 'number' ? Math.max(1, Math.trunc(input.limit)) : undefined
  const normalizedOffset = typeof input.offset === 'number' ? Math.max(0, Math.trunc(input.offset)) : undefined
  const limitClause = normalizedLimit === undefined
    ? ''
    : normalizedOffset === undefined
      ? 'LIMIT ?'
      : 'LIMIT ? OFFSET ?'
  const limitParams: SQLInputValue[] = normalizedLimit === undefined
    ? []
    : normalizedOffset === undefined
      ? [normalizedLimit]
      : [normalizedLimit, normalizedOffset]
  const rows = getUsageCatalogDatabase()
    .prepare(`
      SELECT shard_key, bucket_date, shard_id, file_path
      FROM usage_record_shards
      WHERE ${clauses.join(' AND ')}
      ORDER BY bucket_date ASC, shard_id ASC
      ${limitClause}
    `)
    .all(...params, ...limitParams) as Array<{ shard_key?: string; bucket_date?: string; shard_id?: number; file_path?: string }>
  const locations = rows
    .map(usageRecordShardLocationFromRegistryRow)
    .filter((location): location is UsageRecordShardLocation => Boolean(location))
  rememberUsageRecordShardLocations(locations)
  return locations
}

function listUsageRecordShardLocationsByScopeCatalog(input: {
  tableName: 'usage_record_account_shards' | 'usage_record_api_key_shards'
  whereClause: string
  params: string[]
  limit: number
}): UsageRecordShardLocationWindow {
  const normalizedLimit = Math.max(1, Math.trunc(input.limit))
  const rows = getUsageCatalogDatabase()
    .prepare(`
      SELECT s.shard_key, s.bucket_date, s.shard_id, s.file_path
      FROM ${input.tableName} c
      JOIN usage_record_shards s ON s.shard_key = c.shard_key
      WHERE s.status = 'active'
        AND ${input.whereClause}
      ORDER BY c.first_created_at ASC, s.shard_id ASC
      LIMIT ?
    `)
    .all(...input.params, normalizedLimit + 1) as Array<{ shard_key?: string; bucket_date?: string; shard_id?: number; file_path?: string }>
  const locations = rows
    .slice(0, normalizedLimit)
    .map(usageRecordShardLocationFromRegistryRow)
    .filter((location): location is UsageRecordShardLocation => Boolean(location))
  rememberUsageRecordShardLocations(locations)
  return {
    locations,
    hasMore: rows.length > normalizedLimit
  }
}

function usageRecordShardLocationFromRegistryRow(row: {
  shard_key?: string
  bucket_date?: string
  shard_id?: number
  file_path?: string
}): UsageRecordShardLocation | undefined {
  const shardKey = row.shard_key?.trim()
  const bucketDate = row.bucket_date?.trim()
  const filePath = row.file_path?.trim()
  const shardId = Number(row.shard_id)
  if (!shardKey || !bucketDate || !filePath || !Number.isInteger(shardId)) {
    return undefined
  }
  const bucketDateKey = bucketDate.replace(/-/g, '')
  return { shardKey, bucketDate, bucketDateKey, shardId, filePath }
}

function registerUsageRecordShardLocation(location: UsageRecordShardLocation): void {
  if (registeredUsageRecordShardKeys.has(location.shardKey)) {
    return
  }
  const timestamp = nowIso()
  registerUsageRecordShardLocationInDatabase(getUsageCatalogDatabase(), location, timestamp)
  registeredUsageRecordShardKeys.add(location.shardKey)
}

export function registerUsageRecordShardLocations(locations: UsageRecordShardLocation[]): void {
  const uniqueLocations = uniqueUsageRecordShardLocations(locations)
    .filter((location) => !registeredUsageRecordShardKeys.has(location.shardKey))
  if (uniqueLocations.length === 0) return
  const timestamp = nowIso()
  const database = getUsageCatalogDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const location of uniqueLocations) {
      registerUsageRecordShardLocationInDatabase(database, location, timestamp)
    }
    commitDatabaseTransaction(database, transactionStarted)
    rememberUsageRecordShardLocations(uniqueLocations)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function registerUsageRecordShardLocationInDatabase(database: DatabaseSync, location: UsageRecordShardLocation, timestamp: string): void {
  database
    .prepare(`
      INSERT INTO usage_record_shards (
        shard_key, bucket_date, shard_id, file_path, schema_version, status,
        first_seen_at, last_write_at, last_error_message, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, 'active', ?, NULL, NULL, ?, ?)
      ON CONFLICT(shard_key) DO UPDATE SET
        bucket_date = excluded.bucket_date,
        shard_id = excluded.shard_id,
        file_path = excluded.file_path,
        schema_version = excluded.schema_version,
        status = 'active',
        updated_at = excluded.updated_at
    `)
    .run(location.shardKey, location.bucketDate, location.shardId, location.filePath, usageRecordShardSchemaVersion, timestamp, timestamp, timestamp)
}

export function listRecentUsageRecordShardLocations(dayWindow = 7): UsageRecordShardLocation[] {
  const days = Math.max(1, Math.min(Math.trunc(dayWindow), 31))
  const now = Date.now()
  const endBucketDateKey = bucketDateKeyFromDate(new Date(now))
  const startBucketDateKey = bucketDateKeyFromDate(new Date(now - (days - 1) * dayMs))
  return listRegisteredUsageRecordShardLocations({
    startBucketDate: bucketDateFromBucketDateKey(startBucketDateKey),
    endBucketDate: bucketDateFromBucketDateKey(endBucketDateKey)
  }).sort((left, right) => {
    if (left.bucketDateKey !== right.bucketDateKey) {
      return right.bucketDateKey.localeCompare(left.bucketDateKey)
    }
    return left.shardId - right.shardId
  })
}

export function queryUsageRecordShardById<T extends Record<string, unknown>>(
  id: string,
  selectSql: string,
  params: SQLInputValue[] = [],
  createdAt?: string
): T | undefined {
  const directLocation = usageRecordShardLocationForLookup(id, createdAt)
  if (directLocation) {
    const row = queryUsageRecordShardAtLocation<T>(directLocation, selectSql, params)
    if (row) {
      return row
    }
  }
  const fallbackLocation = directLocation
    ? findRegisteredUsageRecordShardLocationByKey(directLocation.shardKey)
    : findUsageRecordShardLocationByUsageId(id)
  if (!fallbackLocation) {
    return undefined
  }
  return queryUsageRecordShardAtLocation<T>(fallbackLocation, selectSql, params)
}

export function updateUsageRecordCacheReadCost(input: {
  id: string
  createdAt?: string
  sourceShardKey?: string
  cacheReadCostUsd: number
}): void {
  const location = input.sourceShardKey
    ? findRegisteredUsageRecordShardLocationByKey(input.sourceShardKey)
    : usageRecordShardLocationForLookup(input.id, input.createdAt) ?? findUsageRecordShardLocationByUsageId(input.id)
  if (!location) {
    return
  }
  const database = usageRecordShardDatabaseIfOpenOrExists(location)
  if (!database) {
    return
  }
  database
    .prepare('UPDATE usage_records SET cache_read_cost_usd = ? WHERE id = ?')
    .run(input.cacheReadCostUsd, input.id)
}

export function recordUsageRecordShardEntries(entries: UsageRecordShardEntryInput[], options: { locations?: UsageRecordShardLocation[] } = {}): void {
  const uniqueEntries = uniqueUsageRecordShardEntries(entries)
  if (uniqueEntries.length === 0) return
  const timestamp = nowIso()
  const database = getUsageCatalogDatabase()
  const statement = database.prepare(`
    INSERT INTO usage_record_shard_entries (
      usage_id, shard_key, system_account_id, trace_id, api_key_id, account_id, group_id, model, traffic_source,
      success, status_code, client_ip, first_token_ms, duration_ms, cost_usd, created_at, indexed_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(usage_id) DO UPDATE SET
      shard_key = excluded.shard_key,
      system_account_id = excluded.system_account_id,
      trace_id = excluded.trace_id,
      api_key_id = excluded.api_key_id,
      account_id = excluded.account_id,
      group_id = excluded.group_id,
      model = excluded.model,
      traffic_source = excluded.traffic_source,
      success = excluded.success,
      status_code = excluded.status_code,
      client_ip = excluded.client_ip,
      first_token_ms = excluded.first_token_ms,
      duration_ms = excluded.duration_ms,
      cost_usd = excluded.cost_usd,
      created_at = excluded.created_at,
      indexed_at = excluded.indexed_at
    WHERE usage_record_shard_entries.shard_key IS NOT excluded.shard_key
      OR usage_record_shard_entries.system_account_id IS NOT excluded.system_account_id
      OR usage_record_shard_entries.trace_id IS NOT excluded.trace_id
      OR usage_record_shard_entries.api_key_id IS NOT excluded.api_key_id
      OR usage_record_shard_entries.account_id IS NOT excluded.account_id
      OR usage_record_shard_entries.group_id IS NOT excluded.group_id
      OR usage_record_shard_entries.model IS NOT excluded.model
      OR usage_record_shard_entries.traffic_source IS NOT excluded.traffic_source
      OR usage_record_shard_entries.success IS NOT excluded.success
      OR usage_record_shard_entries.status_code IS NOT excluded.status_code
      OR usage_record_shard_entries.client_ip IS NOT excluded.client_ip
      OR usage_record_shard_entries.first_token_ms IS NOT excluded.first_token_ms
      OR usage_record_shard_entries.duration_ms IS NOT excluded.duration_ms
      OR usage_record_shard_entries.cost_usd IS NOT excluded.cost_usd
      OR usage_record_shard_entries.created_at IS NOT excluded.created_at
  `)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const locations = uniqueUsageRecordShardLocations([
      ...(options.locations ?? []),
      ...uniqueEntries
        .map((entry) => usageRecordShardLocationFromKey(entry.shardKey))
        .filter((location): location is UsageRecordShardLocation => Boolean(location))
    ]).filter((location) => !registeredUsageRecordShardKeys.has(location.shardKey))
    for (const location of locations) {
      registerUsageRecordShardLocationInDatabase(database, location, timestamp)
    }
    for (const entry of uniqueEntries) {
      statement.run(
        entry.id,
        entry.shardKey,
        entry.systemAccountId,
        entry.traceId,
        entry.apiKeyId ?? null,
        entry.accountId ?? null,
        entry.groupId ?? null,
        entry.model ?? null,
        entry.trafficSource,
        entry.success ? 1 : 0,
        entry.statusCode ?? null,
        entry.clientIp ?? null,
        entry.firstTokenMs ?? null,
        entry.durationMs ?? null,
        entry.costUsd ?? null,
        entry.createdAt,
        timestamp
      )
    }
    upsertUsageRecordScopeShardCatalog(database, uniqueEntries)
    commitDatabaseTransaction(database, transactionStarted)
    rememberUsageRecordShardLocations(locations)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

export function deleteUsageRecordShardEntries(ids: string[]): number {
  const normalizedIds = [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
  if (normalizedIds.length === 0) return 0
  const database = getUsageCatalogDatabase()
  let deletedRows = 0
  for (const chunk of chunkValues(normalizedIds, 900)) {
    const scopes = listUsageRecordShardEntryScopes(database, chunk)
    deletedRows += Number(database
      .prepare(`DELETE FROM usage_record_shard_entries WHERE usage_id IN (${sqlPlaceholders(chunk.length)})`)
      .run(...chunk).changes ?? 0)
    cleanupUsageRecordScopeShardCatalog(database, scopes)
  }
  return deletedRows
}

function upsertUsageRecordScopeShardCatalog(database: DatabaseSync, entries: UsageRecordShardEntryInput[]): void {
  const accountRows = new Map<string, { accountId: string; shardKey: string; firstCreatedAt: string; lastSeenAt: string }>()
  const apiKeyRows = new Map<string, { apiKeyId: string; systemAccountId: string; shardKey: string; firstCreatedAt: string; lastSeenAt: string }>()
  for (const entry of entries) {
    const accountId = entry.accountId?.trim()
    if (accountId) {
      const key = `${accountId}\u0000${entry.shardKey}`
      const existing = accountRows.get(key)
      if (existing) {
        if (entry.createdAt < existing.firstCreatedAt) existing.firstCreatedAt = entry.createdAt
        if (entry.createdAt > existing.lastSeenAt) existing.lastSeenAt = entry.createdAt
      } else {
        accountRows.set(key, {
          accountId,
          shardKey: entry.shardKey,
          firstCreatedAt: entry.createdAt,
          lastSeenAt: entry.createdAt
        })
      }
    }
    const apiKeyId = entry.apiKeyId?.trim()
    const systemAccountId = entry.systemAccountId.trim()
    if (apiKeyId && systemAccountId) {
      const key = `${apiKeyId}\u0000${systemAccountId}\u0000${entry.shardKey}`
      const existing = apiKeyRows.get(key)
      if (existing) {
        if (entry.createdAt < existing.firstCreatedAt) existing.firstCreatedAt = entry.createdAt
        if (entry.createdAt > existing.lastSeenAt) existing.lastSeenAt = entry.createdAt
      } else {
        apiKeyRows.set(key, {
          apiKeyId,
          systemAccountId,
          shardKey: entry.shardKey,
          firstCreatedAt: entry.createdAt,
          lastSeenAt: entry.createdAt
        })
      }
    }
  }

  const accountStatement = database.prepare(`
    INSERT INTO usage_record_account_shards (account_id, shard_key, first_created_at, last_seen_at)
    VALUES (?, ?, ?, ?)
    ON CONFLICT(account_id, shard_key) DO UPDATE SET
      first_created_at = CASE
        WHEN excluded.first_created_at < usage_record_account_shards.first_created_at THEN excluded.first_created_at
        ELSE usage_record_account_shards.first_created_at
      END,
      last_seen_at = CASE
        WHEN excluded.last_seen_at > usage_record_account_shards.last_seen_at THEN excluded.last_seen_at
        ELSE usage_record_account_shards.last_seen_at
      END
    WHERE excluded.first_created_at < usage_record_account_shards.first_created_at
      OR excluded.last_seen_at > usage_record_account_shards.last_seen_at
  `)
  for (const row of accountRows.values()) {
    accountStatement.run(row.accountId, row.shardKey, row.firstCreatedAt, row.lastSeenAt)
  }

  const apiKeyStatement = database.prepare(`
    INSERT INTO usage_record_api_key_shards (api_key_id, system_account_id, shard_key, first_created_at, last_seen_at)
    VALUES (?, ?, ?, ?, ?)
    ON CONFLICT(api_key_id, system_account_id, shard_key) DO UPDATE SET
      first_created_at = CASE
        WHEN excluded.first_created_at < usage_record_api_key_shards.first_created_at THEN excluded.first_created_at
        ELSE usage_record_api_key_shards.first_created_at
      END,
      last_seen_at = CASE
        WHEN excluded.last_seen_at > usage_record_api_key_shards.last_seen_at THEN excluded.last_seen_at
        ELSE usage_record_api_key_shards.last_seen_at
      END
    WHERE excluded.first_created_at < usage_record_api_key_shards.first_created_at
      OR excluded.last_seen_at > usage_record_api_key_shards.last_seen_at
  `)
  for (const row of apiKeyRows.values()) {
    apiKeyStatement.run(row.apiKeyId, row.systemAccountId, row.shardKey, row.firstCreatedAt, row.lastSeenAt)
  }
}

export function writeUsageRecordShardRows(
  location: UsageRecordShardLocation,
  rows: UsageRecordShardWriteRow[],
  options: { registerLocation?: boolean } = {}
): UsageRecordShardWriteResult {
  if (rows.length === 0) {
    return { insertedRows: 0, accountLastUsedAt: [], accountHealthSuccessAt: [] }
  }
  const database = getUsageRecordShardDatabase(location, options)
  const insertStatement = database.prepare(usageRecordInsertSql)
  const transactionStarted = beginDatabaseTransaction(database)
  const accountLastUsedAt = new Map<string, string>()
  const accountHealthSuccessAt = new Map<string, string>()
  let insertedRows = 0
  try {
    for (const row of rows) {
      const result = insertStatement.run(...row.params)
      if (Number(result.changes ?? 0) > 0) {
        insertedRows += 1
      }
      collectUsageRecordWriteSideEffects(accountLastUsedAt, accountHealthSuccessAt, row)
    }
    commitDatabaseTransaction(database, transactionStarted)
    return {
      insertedRows,
      accountLastUsedAt: [...accountLastUsedAt.entries()].map(([accountId, lastUsedAt]) => ({ accountId, lastUsedAt })),
      accountHealthSuccessAt: [...accountHealthSuccessAt.entries()].map(([accountId, successAt]) => ({ accountId, successAt }))
    }
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function collectUsageRecordWriteSideEffects(
  accountLastUsedAt: Map<string, string>,
  accountHealthSuccessAt: Map<string, string>,
  row: UsageRecordShardWriteRow
): void {
  if (!row.accountId) {
    return
  }
  if (row.accountLastUsedAt) {
    mergeMaxIsoValue(accountLastUsedAt, row.accountId, row.accountLastUsedAt)
  }
  if (row.accountHealthSuccessAt) {
    mergeMaxIsoValue(accountHealthSuccessAt, row.accountId, row.accountHealthSuccessAt)
  }
}

function mergeMaxIsoValue(target: Map<string, string>, key: string, value: string): void {
  const previous = target.get(key)
  if (!previous || value > previous) {
    target.set(key, value)
  }
}

function uniqueUsageRecordShardLocations(locations: UsageRecordShardLocation[]): UsageRecordShardLocation[] {
  const unique = new Map<string, UsageRecordShardLocation>()
  for (const location of locations) {
    unique.set(location.shardKey, location)
  }
  return [...unique.values()]
}

function rememberUsageRecordShardLocations(locations: UsageRecordShardLocation[]): void {
  for (const location of locations) {
    registeredUsageRecordShardKeys.add(location.shardKey)
  }
}

function uniqueUsageRecordShardEntries(entries: UsageRecordShardEntryInput[]): UsageRecordShardEntryInput[] {
  const unique = new Map<string, UsageRecordShardEntryInput>()
  for (const entry of entries) {
    const id = entry.id.trim()
    if (!id) continue
    unique.set(id, entry)
  }
  return [...unique.values()]
}

function listUsageRecordShardEntryScopes(database: DatabaseSync, ids: string[]): UsageRecordShardEntryScope[] {
  if (ids.length === 0) return []
  const rows = database
    .prepare(`
      SELECT usage_id, shard_key, system_account_id, api_key_id, account_id
      FROM usage_record_shard_entries
      WHERE usage_id IN (${sqlPlaceholders(ids.length)})
    `)
    .all(...ids) as Array<{
      usage_id?: string | null
      shard_key?: string | null
      system_account_id?: string | null
      api_key_id?: string | null
      account_id?: string | null
    }>
  return rows
    .map((row) => ({
      usageId: String(row.usage_id ?? ''),
      shardKey: String(row.shard_key ?? ''),
      systemAccountId: String(row.system_account_id ?? ''),
      apiKeyId: typeof row.api_key_id === 'string' ? row.api_key_id : undefined,
      accountId: typeof row.account_id === 'string' ? row.account_id : undefined
    }))
    .filter((row) => row.usageId && row.shardKey && row.systemAccountId)
}

function cleanupUsageRecordScopeShardCatalog(database: DatabaseSync, scopes: UsageRecordShardEntryScope[]): void {
  const accountScopes = new Set<string>()
  const apiKeyScopes = new Set<string>()
  for (const scope of scopes) {
    const accountId = scope.accountId?.trim()
    if (accountId) {
      accountScopes.add(`${accountId}\u0000${scope.shardKey}`)
    }
    const apiKeyId = scope.apiKeyId?.trim()
    if (apiKeyId) {
      apiKeyScopes.add(`${apiKeyId}\u0000${scope.systemAccountId}\u0000${scope.shardKey}`)
    }
  }

  const deleteAccountScopeStatement = database.prepare(`
    DELETE FROM usage_record_account_shards
    WHERE account_id = ?
      AND shard_key = ?
      AND NOT EXISTS (
        SELECT 1
        FROM usage_record_shard_entries
        WHERE account_id = ?
          AND shard_key = ?
        LIMIT 1
      )
  `)
  for (const key of accountScopes) {
    const [accountId, shardKey] = key.split('\u0000')
    if (accountId && shardKey) {
      deleteAccountScopeStatement.run(accountId, shardKey, accountId, shardKey)
    }
  }

  const deleteApiKeyScopeStatement = database.prepare(`
    DELETE FROM usage_record_api_key_shards
    WHERE api_key_id = ?
      AND system_account_id = ?
      AND shard_key = ?
      AND NOT EXISTS (
        SELECT 1
        FROM usage_record_shard_entries
        WHERE api_key_id = ?
          AND system_account_id = ?
          AND shard_key = ?
        LIMIT 1
      )
  `)
  for (const key of apiKeyScopes) {
    const [apiKeyId, systemAccountId, shardKey] = key.split('\u0000')
    if (apiKeyId && systemAccountId && shardKey) {
      deleteApiKeyScopeStatement.run(apiKeyId, systemAccountId, shardKey, apiKeyId, systemAccountId, shardKey)
    }
  }
}

export function usageRecordShardExistsForId(id: string, createdAt?: string): boolean {
  const location = usageRecordShardLocationForLookup(id, createdAt)
  if (!location) {
    return false
  }
  return Boolean(usageRecordShardDatabaseIfOpenOrExists(location) ?? findRegisteredUsageRecordShardLocationByKey(location.shardKey))
}

function queryUsageRecordShardAtLocation<T extends Record<string, unknown>>(
  location: UsageRecordShardLocation,
  selectSql: string,
  params: SQLInputValue[]
): T | undefined {
  const database = usageRecordShardDatabaseIfOpenOrExists(location)
  if (!database) {
    return undefined
  }
  return database
    .prepare(selectSql)
    .get(...params) as T | undefined
}

function usageRecordShardDatabaseIfOpenOrExists(location: UsageRecordShardLocation): DatabaseSync | undefined {
  const cached = shardDatabases.get(location.filePath)
  if (cached) {
    return cached
  }
  const target = usageShardFilePath(location.filePath)
  if (!existsSync(target)) {
    return undefined
  }
  return getUsageRecordShardDatabase(location, { registerLocation: false })
}

function usageRecordShardLocation(bucketDateKey: string, shardIdInput: number): UsageRecordShardLocation {
  const shardId = Math.max(0, Math.trunc(shardIdInput))
  const bucketDate = `${bucketDateKey.slice(0, 4)}-${bucketDateKey.slice(4, 6)}-${bucketDateKey.slice(6, 8)}`
  const shardKey = `${bucketDateKey}:s${formatShardId(shardId)}`
  const filePath = join(
    usageRecordShardRoot(),
    bucketDateKey.slice(0, 4),
    bucketDateKey.slice(4, 6),
    bucketDateKey.slice(6, 8),
    `usage-${bucketDateKey}-s${formatShardId(shardId)}.sqlite3`
  )
  return { shardKey, bucketDate, bucketDateKey, shardId, filePath }
}

async function deleteUsageShardFileSet(filePath: string): Promise<number> {
  const target = usageShardFilePath(filePath)
  let deleted = 0
  for (const path of [target, `${target}-wal`, `${target}-shm`, `${target}-journal`]) {
    deleted += await unlinkIfExists(path)
  }
  return deleted
}

function usageShardFilePath(filePath: string): string {
  const root = usageRecordShardRoot()
  const target = resolve(filePath)
  const relativePath = relative(root, target)
  if (relativePath.startsWith('..') || isAbsolute(relativePath)) {
    throw new Error('usage shard 存储路径非法')
  }
  return target
}

async function unlinkIfExists(path: string): Promise<number> {
  try {
    await unlink(path)
    return 1
  } catch {
    return 0
  }
}

function isDefaultUsageShardRoot(value: string): boolean {
  const normalized = normalize(value).toLowerCase()
  return normalized === normalize(defaultUsageShardRoot).toLowerCase()
}

function parseUsageRecordShardId(id: string): { bucketDateKey: string; shardId: number } | undefined {
  const match = /^usage_(\d{8})_s(\d+)_/.exec(id.trim())
  if (!match) return undefined
  const shardId = Number(match[2])
  if (!Number.isInteger(shardId) || shardId < 0) return undefined
  return { bucketDateKey: match[1], shardId }
}

function usageRecordShardLocationForLookup(id: string, createdAt?: string): UsageRecordShardLocation | undefined {
  const parsedShardId = parseUsageRecordShardId(id)
  if (!parsedShardId && !createdAt) {
    return undefined
  }
  return usageRecordShardLocationForRecord(id, createdAt)
}

function findUsageRecordShardLocationByUsageId(id: string): UsageRecordShardLocation | undefined {
  const row = getUsageCatalogDatabase()
    .prepare('SELECT shard_key FROM usage_record_shard_entries WHERE usage_id = ? LIMIT 1')
    .get(id) as { shard_key?: string } | undefined
  return row?.shard_key ? findRegisteredUsageRecordShardLocationByKey(row.shard_key) : undefined
}

function findRegisteredUsageRecordShardLocationByKey(shardKey: string): UsageRecordShardLocation | undefined {
  const row = getUsageCatalogDatabase()
    .prepare(`
      SELECT shard_key, bucket_date, shard_id, file_path
      FROM usage_record_shards
      WHERE shard_key = ?
        AND status = 'active'
      LIMIT 1
    `)
    .get(shardKey.trim()) as { shard_key?: string; bucket_date?: string; shard_id?: number; file_path?: string } | undefined
  const location = row ? usageRecordShardLocationFromRegistryRow(row) : undefined
  if (location) {
    registeredUsageRecordShardKeys.add(location.shardKey)
  }
  return location
}

function stableShardId(value: string): number {
  return stableHash(value) % usageRecordShardCount()
}

function stableHash(value: string): number {
  let hash = 2166136261
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index)
    hash = Math.imul(hash, 16777619)
  }
  return hash >>> 0
}

function bucketDateKeyFromIso(value?: string): string {
  const parsed = value ? new Date(value) : new Date()
  const date = Number.isFinite(parsed.getTime()) ? parsed : new Date()
  return bucketDateKeyFromDate(date)
}

function bucketDateKeyFromDate(date: Date): string {
  return `${date.getUTCFullYear()}${String(date.getUTCMonth() + 1).padStart(2, '0')}${String(date.getUTCDate()).padStart(2, '0')}`
}

function bucketDateFromBucketDateKey(value: string): string {
  return `${value.slice(0, 4)}-${value.slice(4, 6)}-${value.slice(6, 8)}`
}

const dayMs = 24 * 60 * 60 * 1000

function bucketDateKeyToUtcMs(value: string): number {
  const match = /^(\d{4})(\d{2})(\d{2})$/.exec(value)
  if (!match) return Date.now()
  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const time = Date.UTC(year, month - 1, day)
  return Number.isFinite(time) ? time : Date.now()
}

function formatShardId(shardId: number): string {
  return String(Math.max(0, Math.trunc(shardId))).padStart(2, '0')
}

function configureUsageRecordShardDatabase(database: DatabaseSync): void {
  database.exec(`
    PRAGMA busy_timeout = ${sqliteBusyTimeoutMs};
  `)
  if (sqliteWriterBoundaryStrictModeEnabled() && !currentProcessOwnsUsageShardWriter()) {
    database.exec('PRAGMA query_only = ON')
    return
  }
  database.exec('PRAGMA journal_mode = WAL')
}

function shouldApplyUsageRecordShardSchema(): boolean {
  return currentProcessOwnsUsageShardWriter() || !sqliteWriterBoundaryStrictModeEnabled()
}

function applyUsageRecordShardSchema(database: DatabaseSync): void {
  applyUsageRecordShardBaseSchema(database)

  ensureUsageRecordShardColumns(database)
  database.exec(`
    CREATE INDEX IF NOT EXISTS idx_usage_records_provider_protocol_profile_created_at ON usage_records(provider_protocol_profile_id, created_at);
  `)

  void usageRecordShardSchemaVersion
}

function ensureUsageRecordShardColumns(database: DatabaseSync): void {
  const rows = database
    .prepare('PRAGMA table_info(usage_records)')
    .all() as Array<{ name?: string }>
  const existing = new Set(rows.map((row) => String(row.name ?? '')))
  const columns: Array<{ name: string; definition: string }> = [
    { name: 'usage_semantic', definition: 'TEXT' },
    { name: 'cache_write_tokens', definition: 'INTEGER' },
    { name: 'cache_write_1h_tokens', definition: 'INTEGER' },
    { name: 'cache_write_cost_usd', definition: 'REAL' },
    { name: 'thinking_tokens', definition: 'INTEGER' },
    { name: 'provider_protocol_profile_id', definition: 'TEXT' }
  ]
  for (const column of columns) {
    if (existing.has(column.name)) continue
    database.exec(`ALTER TABLE usage_records ADD COLUMN ${column.name} ${column.definition}`)
  }
}
