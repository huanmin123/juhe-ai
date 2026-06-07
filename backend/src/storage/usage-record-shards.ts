import { mkdirSync } from 'node:fs'
import { unlink } from 'node:fs/promises'
import { dirname, isAbsolute, join, normalize, relative, resolve } from 'node:path'
import { DatabaseSync, type SQLInputValue } from 'node:sqlite'

import { defaultUsageShardRoot, runtimeConfig } from '../config/runtime.js'
import { getDatasetDatabase, nowIso } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { sqliteBusyTimeoutMs } from './sqlite-config.js'

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
  apiKeyId?: string | null
  accountId?: string | null
  groupId?: string | null
  model?: string | null
  trafficSource: string
  success: boolean
  statusCode?: number | null
  clientIp?: string | null
  firstTokenMs?: number | null
  durationMs?: number | null
  costUsd?: number | null
  createdAt: string
}

interface UsageRecordShardEntryScope {
  usageId: string
  shardKey: string
  systemAccountId: string
  apiKeyId?: string | null
  accountId?: string | null
}

const usageRecordShardSchemaVersion = 1
const usageRecordShardWindowMaxDays = 31
const shardDatabases = new Map<string, DatabaseSync>()

export function usageRecordShardCount(): number {
  return Math.max(1, Math.trunc(runtimeConfig.usageShardCount))
}

export function usageRecordShardRoot(): string {
  if (isDefaultUsageShardRoot(runtimeConfig.usageShardRoot)) {
    return resolve(dirname(runtimeConfig.datasetDatabasePath), 'usage-shards')
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

export function getUsageRecordShardDatabase(location: UsageRecordShardLocation): DatabaseSync {
  const cached = shardDatabases.get(location.filePath)
  if (cached) {
    return cached
  }
  mkdirSync(dirname(location.filePath), { recursive: true })
  const database = new DatabaseSync(location.filePath)
  configureUsageRecordShardDatabase(database)
  applyUsageRecordShardSchema(database)
  shardDatabases.set(location.filePath, database)
  registerUsageRecordShardLocation(location)
  return database
}

export function closeUsageRecordShardDatabases(): void {
  for (const database of shardDatabases.values()) {
    try {
      database.close()
    } catch {
    }
  }
  shardDatabases.clear()
}

export async function cleanupEmptyUsageRecordShardFilesBefore(cutoffAt: string, limit = 1000): Promise<EmptyUsageRecordShardFileCleanupResult> {
  const cutoffDate = bucketDateFromBucketDateKey(bucketDateKeyFromIso(cutoffAt))
  const batchLimit = Math.max(1, Math.trunc(limit))
  const rows = getDatasetDatabase()
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
    usageRecordShards += Number(getDatasetDatabase()
      .prepare(`DELETE FROM usage_record_shards WHERE shard_key IN (${sqlPlaceholders(chunk.length)})`)
      .run(...chunk).changes ?? 0)
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
  const database = getDatasetDatabase()
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
  const rows = getDatasetDatabase()
    .prepare(`
      SELECT shard_key, bucket_date, shard_id, file_path
      FROM usage_record_shards
      WHERE ${clauses.join(' AND ')}
      ORDER BY bucket_date ASC, shard_id ASC
      ${limitClause}
    `)
    .all(...params, ...limitParams) as Array<{ shard_key?: string; bucket_date?: string; shard_id?: number; file_path?: string }>
  return rows
    .map(usageRecordShardLocationFromRegistryRow)
    .filter((location): location is UsageRecordShardLocation => Boolean(location))
}

function listUsageRecordShardLocationsByScopeCatalog(input: {
  tableName: 'usage_record_account_shards' | 'usage_record_api_key_shards'
  whereClause: string
  params: string[]
  limit: number
}): UsageRecordShardLocationWindow {
  const normalizedLimit = Math.max(1, Math.trunc(input.limit))
  const rows = getDatasetDatabase()
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
  const timestamp = nowIso()
  getDatasetDatabase()
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
  const location = registeredUsageRecordShardLocationForLookup(id, createdAt) ?? findUsageRecordShardLocationByUsageId(id)
  if (!location) {
    return undefined
  }
  return getUsageRecordShardDatabase(location)
    .prepare(selectSql)
    .get(...params) as T | undefined
}

export function updateUsageRecordCacheReadCost(input: {
  id: string
  createdAt?: string
  sourceShardKey?: string
  cacheReadCostUsd: number
}): void {
  const location = input.sourceShardKey
    ? findRegisteredUsageRecordShardLocationByKey(input.sourceShardKey)
    : registeredUsageRecordShardLocationForLookup(input.id, input.createdAt) ?? findUsageRecordShardLocationByUsageId(input.id)
  if (!location) {
    return
  }
  getUsageRecordShardDatabase(location)
    .prepare('UPDATE usage_records SET cache_read_cost_usd = ? WHERE id = ?')
    .run(input.cacheReadCostUsd, input.id)
}

export function recordUsageRecordShardEntries(entries: UsageRecordShardEntryInput[]): void {
  if (entries.length === 0) return
  const timestamp = nowIso()
  const database = getDatasetDatabase()
  const statement = database.prepare(`
    INSERT INTO usage_record_shard_entries (
      usage_id, shard_key, system_account_id, api_key_id, account_id, group_id, model, traffic_source,
      success, status_code, client_ip, first_token_ms, duration_ms, cost_usd, created_at, indexed_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(usage_id) DO UPDATE SET
      shard_key = excluded.shard_key,
      system_account_id = excluded.system_account_id,
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
  `)
  for (const entry of entries) {
    statement.run(
      entry.id,
      entry.shardKey,
      entry.systemAccountId,
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
  upsertUsageRecordScopeShardCatalog(entries)
}

export function deleteUsageRecordShardEntries(ids: string[]): number {
  const normalizedIds = [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
  if (normalizedIds.length === 0) return 0
  const database = getDatasetDatabase()
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

function upsertUsageRecordScopeShardCatalog(entries: UsageRecordShardEntryInput[]): void {
  const database = getDatasetDatabase()
  const accountRows = new Map<string, { accountId: string; shardKey: string; createdAt: string }>()
  const apiKeyRows = new Map<string, { apiKeyId: string; systemAccountId: string; shardKey: string; createdAt: string }>()
  for (const entry of entries) {
    const accountId = entry.accountId?.trim()
    if (accountId) {
      const key = `${accountId}\u0000${entry.shardKey}`
      const existing = accountRows.get(key)
      if (!existing || entry.createdAt < existing.createdAt) {
        accountRows.set(key, { accountId, shardKey: entry.shardKey, createdAt: entry.createdAt })
      }
    }
    const apiKeyId = entry.apiKeyId?.trim()
    const systemAccountId = entry.systemAccountId.trim()
    if (apiKeyId && systemAccountId) {
      const key = `${apiKeyId}\u0000${systemAccountId}\u0000${entry.shardKey}`
      const existing = apiKeyRows.get(key)
      if (!existing || entry.createdAt < existing.createdAt) {
        apiKeyRows.set(key, { apiKeyId, systemAccountId, shardKey: entry.shardKey, createdAt: entry.createdAt })
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
  `)
  for (const row of accountRows.values()) {
    accountStatement.run(row.accountId, row.shardKey, row.createdAt, row.createdAt)
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
  `)
  for (const row of apiKeyRows.values()) {
    apiKeyStatement.run(row.apiKeyId, row.systemAccountId, row.shardKey, row.createdAt, row.createdAt)
  }
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
  return Boolean(registeredUsageRecordShardLocationForLookup(id, createdAt))
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

function registeredUsageRecordShardLocationForLookup(id: string, createdAt?: string): UsageRecordShardLocation | undefined {
  const location = usageRecordShardLocationForLookup(id, createdAt)
  return location ? findRegisteredUsageRecordShardLocationByKey(location.shardKey) : undefined
}

function findUsageRecordShardLocationByUsageId(id: string): UsageRecordShardLocation | undefined {
  const row = getDatasetDatabase()
    .prepare('SELECT shard_key FROM usage_record_shard_entries WHERE usage_id = ? LIMIT 1')
    .get(id) as { shard_key?: string } | undefined
  return row?.shard_key ? findRegisteredUsageRecordShardLocationByKey(row.shard_key) : undefined
}

function findRegisteredUsageRecordShardLocationByKey(shardKey: string): UsageRecordShardLocation | undefined {
  const row = getDatasetDatabase()
    .prepare(`
      SELECT shard_key, bucket_date, shard_id, file_path
      FROM usage_record_shards
      WHERE shard_key = ?
        AND status = 'active'
      LIMIT 1
    `)
    .get(shardKey.trim()) as { shard_key?: string; bucket_date?: string; shard_id?: number; file_path?: string } | undefined
  return row ? usageRecordShardLocationFromRegistryRow(row) : undefined
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
    PRAGMA journal_mode = WAL;
  `)
}

function applyUsageRecordShardSchema(database: DatabaseSync): void {
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
      model TEXT,
      upstream_model TEXT,
      pricing_model TEXT,
      model_mapping_applied INTEGER NOT NULL DEFAULT 0,
      model_mapping_source TEXT,
      stream INTEGER NOT NULL DEFAULT 0,
      status_code INTEGER,
      success INTEGER NOT NULL DEFAULT 0,
      first_token_ms INTEGER,
      duration_ms INTEGER,
      input_tokens INTEGER,
      output_tokens INTEGER,
      cache_read_tokens INTEGER,
      cache_read_cost_usd REAL,
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
    CREATE INDEX IF NOT EXISTS idx_usage_records_model_created_sort ON usage_records(model, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_model_created_sort ON usage_records(system_account_id, model, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_traffic_source_created ON usage_records(traffic_source, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_client_ip_created_sort ON usage_records(client_ip, created_at, id);
    CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_client_ip_created_sort ON usage_records(system_account_id, client_ip, created_at, id);
  `)

  void usageRecordShardSchemaVersion
}
