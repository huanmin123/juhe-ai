import { existsSync, mkdirSync, readdirSync } from 'node:fs'
import { dirname, join, normalize, resolve } from 'node:path'
import { DatabaseSync, type SQLInputValue } from 'node:sqlite'

import { defaultUsageShardRoot, runtimeConfig } from '../config/runtime.js'

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

const usageRecordShardSchemaVersion = 1
const sqliteBusyTimeoutMs = 5000
const shardDatabases = new Map<string, DatabaseSync>()

export function usageRecordShardCount(): number {
  return Math.max(1, Math.trunc(runtimeConfig.usageShardCount))
}

export function usageRecordShardRoot(): string {
  if (normalize(runtimeConfig.usageShardRoot).toLowerCase() === normalize(defaultUsageShardRoot).toLowerCase()) {
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

export function listUsageRecordShardLocations(window: UsageRecordShardQueryWindow = {}): UsageRecordShardLocation[] {
  const root = usageRecordShardRoot()
  if (!existsSync(root)) {
    return []
  }
  const startDateKey = window.startAt ? bucketDateKeyFromIso(window.startAt) : undefined
  const endDateKey = window.endAt ? bucketDateKeyFromIso(window.endAt) : undefined
  const locations: UsageRecordShardLocation[] = []
  for (const filePath of walkSqliteFiles(root)) {
    const location = usageRecordShardLocationFromPath(filePath)
    if (!location) continue
    if (startDateKey && location.bucketDateKey < startDateKey) continue
    if (endDateKey && location.bucketDateKey > endDateKey) continue
    locations.push(location)
  }
  return locations.sort((left, right) => {
    if (left.bucketDateKey !== right.bucketDateKey) {
      return left.bucketDateKey.localeCompare(right.bucketDateKey)
    }
    return left.shardId - right.shardId
  })
}

export function listRecentUsageRecordShardLocations(dayWindow = 7): UsageRecordShardLocation[] {
  const days = Math.max(1, Math.min(Math.trunc(dayWindow), 31))
  const now = Date.now()
  const locations: UsageRecordShardLocation[] = []
  for (let dayOffset = 0; dayOffset < days; dayOffset += 1) {
    const bucketDateKey = bucketDateKeyFromDate(new Date(now - dayOffset * 24 * 60 * 60 * 1000))
    for (let shardId = 0; shardId < usageRecordShardCount(); shardId += 1) {
      const location = usageRecordShardLocation(bucketDateKey, shardId)
      if (existsSync(location.filePath)) {
        locations.push(location)
      }
    }
  }
  return locations.sort((left, right) => {
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
  const parsedShardId = parseUsageRecordShardId(id)
  const directLocation = parsedShardId || createdAt
    ? usageRecordShardLocationForRecord(id, createdAt)
    : undefined
  if (directLocation && existsSync(directLocation.filePath)) {
    const row = getUsageRecordShardDatabase(directLocation)
      .prepare(selectSql)
      .get(...params) as T | undefined
    if (row) return row
  }
  for (const location of listUsageRecordShardLocations()) {
    if (directLocation?.shardKey === location.shardKey) continue
    const row = getUsageRecordShardDatabase(location)
      .prepare(selectSql)
      .get(...params) as T | undefined
    if (row) return row
  }
  return undefined
}

export function updateUsageRecordCacheReadCost(input: {
  id: string
  createdAt?: string
  sourceShardKey?: string
  cacheReadCostUsd: number
}): void {
  const location = input.sourceShardKey
    ? usageRecordShardLocationFromKey(input.sourceShardKey)
    : usageRecordShardLocationForRecord(input.id, input.createdAt)
  const locations = location ? [location] : listUsageRecordShardLocations()
  for (const candidate of locations) {
    if (!existsSync(candidate.filePath)) continue
    const result = getUsageRecordShardDatabase(candidate)
      .prepare('UPDATE usage_records SET cache_read_cost_usd = ? WHERE id = ?')
      .run(input.cacheReadCostUsd, input.id)
    if (Number(result.changes ?? 0) > 0) {
      return
    }
  }
  if (location) {
    for (const candidate of listUsageRecordShardLocations()) {
      if (candidate.shardKey === location.shardKey) continue
      const result = getUsageRecordShardDatabase(candidate)
        .prepare('UPDATE usage_records SET cache_read_cost_usd = ? WHERE id = ?')
        .run(input.cacheReadCostUsd, input.id)
      if (Number(result.changes ?? 0) > 0) {
        return
      }
    }
  }
}

export function usageRecordShardExistsForId(id: string, createdAt?: string): boolean {
  const location = usageRecordShardLocationForRecord(id, createdAt)
  return existsSync(location.filePath)
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

function parseUsageRecordShardId(id: string): { bucketDateKey: string; shardId: number } | undefined {
  const match = /^usage_(\d{8})_s(\d+)_/.exec(id.trim())
  if (!match) return undefined
  const shardId = Number(match[2])
  if (!Number.isInteger(shardId) || shardId < 0) return undefined
  return { bucketDateKey: match[1], shardId }
}

function usageRecordShardLocationFromPath(filePath: string): UsageRecordShardLocation | undefined {
  const normalized = filePath.replace(/\\/g, '/')
  const match = /(?:^|\/)usage-(\d{8})-s(\d+)\.sqlite3$/i.exec(normalized)
  if (!match) return undefined
  return usageRecordShardLocation(match[1], Number(match[2]))
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

function formatShardId(shardId: number): string {
  return String(Math.max(0, Math.trunc(shardId))).padStart(2, '0')
}

function walkSqliteFiles(root: string): string[] {
  const files: string[] = []
  const stack = [root]
  while (stack.length > 0) {
    const current = stack.pop()
    if (!current) continue
    for (const entry of readdirSync(current, { withFileTypes: true })) {
      const path = join(current, entry.name)
      if (entry.isDirectory()) {
        stack.push(path)
      } else if (entry.isFile() && /^usage-\d{8}-s\d+\.sqlite3$/i.test(entry.name)) {
        files.push(path)
      }
    }
  }
  return files
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
      system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
      trace_id TEXT NOT NULL,
      traffic_source TEXT NOT NULL DEFAULT 'gateway',
      client_ip TEXT,
      api_key_id TEXT,
      group_id TEXT,
      account_id TEXT,
      endpoint TEXT,
      provider_code TEXT,
      model TEXT,
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
  `)

  const columns = database.prepare('PRAGMA table_info(usage_records)').all() as Array<{ name?: string }>
  if (!columns.some((column) => column.name === 'traffic_source')) {
    database.exec("ALTER TABLE usage_records ADD COLUMN traffic_source TEXT NOT NULL DEFAULT 'gateway'")
  }

  void usageRecordShardSchemaVersion
}
