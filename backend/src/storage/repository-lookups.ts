import type { SystemAccountSummary, SystemTeamStatus } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import {
  canUseProcessLocalAppCacheAsFactSource,
  createAppCache,
  createSharedJsonCache,
  throwIfRedisCacheIsRequired,
  type AppCache,
  type SharedJsonCache
} from '../shared/cache.js'
import { errorLogFields, logger } from '../shared/logger.js'
import { getBusinessDatabase } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { systemAccountSummaryFromRow, type SystemAccountSummaryRow } from './system-account-mappers.js'

const lookupCacheTtlMs = 10 * 60 * 1000
const lookupCacheMax = 10_000
const businessSchemaName = 'juhe_business'

export interface SystemAccountPrincipalLookup {
  id: string
  username: string
  displayName: string
}

export interface BusinessResourceLookup {
  id: string
  name: string
  systemAccountId: string
  accountExpiresAt?: string
}

export interface SystemTeamLookup {
  id: string
  name: string
  status: SystemTeamStatus
}

export interface SystemAccountTeamNamesLookup {
  id: string
  teamNames: string[]
}

const systemAccountPrincipalCache = createAppCache<string, SystemAccountPrincipalLookup>({
  name: 'lookup:system-account-principal',
  max: lookupCacheMax,
  ttlMs: lookupCacheTtlMs,
  updateAgeOnGet: true
})

const systemAccountPrincipalSharedCache = createSharedJsonCache<SystemAccountPrincipalLookup>({
  name: 'lookup:system-account-principal',
  max: lookupCacheMax,
  ttlMs: lookupCacheTtlMs
})

const accountLookupCache = createAppCache<string, BusinessResourceLookup>({
  name: 'lookup:account',
  max: lookupCacheMax,
  ttlMs: lookupCacheTtlMs,
  updateAgeOnGet: true
})

const accountLookupSharedCache = createSharedJsonCache<BusinessResourceLookup>({
  name: 'lookup:account',
  max: lookupCacheMax,
  ttlMs: lookupCacheTtlMs
})

const groupLookupCache = createAppCache<string, BusinessResourceLookup>({
  name: 'lookup:group',
  max: lookupCacheMax,
  ttlMs: lookupCacheTtlMs,
  updateAgeOnGet: true
})

const groupLookupSharedCache = createSharedJsonCache<BusinessResourceLookup>({
  name: 'lookup:group',
  max: lookupCacheMax,
  ttlMs: lookupCacheTtlMs
})

const apiKeyLookupCache = createAppCache<string, BusinessResourceLookup>({
  name: 'lookup:api-key',
  max: lookupCacheMax,
  ttlMs: lookupCacheTtlMs,
  updateAgeOnGet: true
})

const apiKeyLookupSharedCache = createSharedJsonCache<BusinessResourceLookup>({
  name: 'lookup:api-key',
  max: lookupCacheMax,
  ttlMs: lookupCacheTtlMs
})

const systemTeamLookupCache = createAppCache<string, SystemTeamLookup>({
  name: 'lookup:system-team',
  max: lookupCacheMax,
  ttlMs: lookupCacheTtlMs,
  updateAgeOnGet: true
})

const systemTeamLookupSharedCache = createSharedJsonCache<SystemTeamLookup>({
  name: 'lookup:system-team',
  max: lookupCacheMax,
  ttlMs: lookupCacheTtlMs
})

const systemAccountTeamNamesCache = createAppCache<string, SystemAccountTeamNamesLookup>({
  name: 'lookup:system-account-team-names',
  max: lookupCacheMax,
  ttlMs: lookupCacheTtlMs,
  updateAgeOnGet: true
})

const systemAccountTeamNamesSharedCache = createSharedJsonCache<SystemAccountTeamNamesLookup>({
  name: 'lookup:system-account-team-names',
  max: lookupCacheMax,
  ttlMs: lookupCacheTtlMs
})

function uniqueIds(values: Array<string | null | undefined>): string[] {
  return [...new Set(values.map((value) => value?.trim()).filter((value): value is string => Boolean(value)))]
}

function loadRowsByIds<T>(ids: string[], sql: (chunk: string[]) => string): T[] {
  const rows: T[] = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...database.prepare(sql(chunk)).all(...chunk) as unknown as T[])
  }
  return rows
}

function loadCachedRowsByIds<T extends { id: string }>(
  values: Array<string | null | undefined>,
  cache: AppCache<string, T>,
  loadMissingRows: (ids: string[]) => T[]
): Map<string, T> {
  const ids = uniqueIds(values)
  if (!ids.length) return new Map()
  if (!canUseProcessLocalAppCacheAsFactSource()) {
    throw new Error('高性能模式禁止同步资源 lookup 绕过 Redis shared cache，必须使用 async lookup')
  }

  const result = new Map<string, T>()
  const missingIds: string[] = []
  for (const id of ids) {
    const cached = cache.get(id)
    if (cached !== undefined) {
      result.set(id, cached)
    } else {
      missingIds.push(id)
    }
  }

  if (!missingIds.length) return result

  for (const row of loadMissingRows(missingIds)) {
    cache.set(row.id, row)
    result.set(row.id, row)
  }
  return result
}

async function loadCachedRowsByIdsAsync<T extends { id: string }>(
  values: Array<string | null | undefined>,
  cache: AppCache<string, T>,
  sharedCache: SharedJsonCache<T>,
  loadMissingRows: (ids: string[]) => Promise<T[]>
): Promise<Map<string, T>> {
  const ids = uniqueIds(values)
  if (!ids.length) return new Map()

  if (runtimeConfig.cacheDriver === 'redis') {
    const result = new Map<string, T>()
    const dbMissIds: string[] = []
    const sharedResults = await Promise.all(ids.map(async (id) => {
      try {
        return [id, await sharedCache.get(id)] as const
      } catch (error) {
        throwIfRedisCacheIsRequired(error)
        logger.warn(errorLogFields(error, {
          event: 'repository_lookup_shared_cache_read_failed',
          cacheName: sharedCache.name
        }), '读取资源 lookup Redis 共享缓存失败')
        return [id, undefined] as const
      }
    }))
    for (const [id, cached] of sharedResults) {
      if (cached !== undefined) {
        result.set(id, cached)
      } else {
        dbMissIds.push(id)
      }
    }

    if (!dbMissIds.length) return result

    for (const row of await loadMissingRows(dbMissIds)) {
      await setLookupSharedCacheEntryAsync(sharedCache, row.id, row)
      result.set(row.id, row)
    }
    return result
  }

  const result = new Map<string, T>()
  const localMissIds: string[] = []
  for (const id of ids) {
    const cached = cache.get(id)
    if (cached !== undefined) {
      result.set(id, cached)
    } else {
      localMissIds.push(id)
    }
  }

  if (!localMissIds.length) return result

  const dbMissIds = localMissIds

  if (!dbMissIds.length) return result

  for (const row of await loadMissingRows(dbMissIds)) {
    cache.set(row.id, row)
    result.set(row.id, row)
  }
  return result
}

function invalidateLookupCache<T extends { id: string }>(cache: AppCache<string, T>, sharedCache: SharedJsonCache<T>, id?: string): void {
  const normalizedId = id?.trim()
  if (normalizedId) {
    cache.delete(normalizedId)
    deleteLookupSharedCacheEntry(sharedCache, normalizedId)
    return
  }
  cache.clear()
  clearLookupSharedCache(sharedCache)
}

export function loadSystemAccountsByIds(systemAccountIds: Array<string | undefined>): Map<string, SystemAccountSummary> {
  if (!canUseProcessLocalAppCacheAsFactSource()) {
    throw new Error('高性能模式禁止同步直读系统账户 lookup，必须使用 async repository lookup')
  }
  const ids = uniqueIds(systemAccountIds)
  if (!ids.length) return new Map()
  const rows = loadRowsByIds<SystemAccountSummaryRow>(ids, (chunk) => `
    SELECT id, username, display_name, description, role, status, must_change_password, image_generation_enabled, last_login_at, created_at, updated_at
    FROM system_accounts
    WHERE id IN (${sqlPlaceholders(chunk.length)})
  `)
  return new Map(rows.map((row) => [row.id, systemAccountSummaryFromRow(row)]))
}

export function loadSystemAccountPrincipalMapByIds(systemAccountIds: Array<string | undefined>): Map<string, SystemAccountPrincipalLookup> {
  return loadCachedRowsByIds(systemAccountIds, systemAccountPrincipalCache, (ids) => {
    const rows = loadRowsByIds<{ id: string; username: string; display_name: string }>(ids, (chunk) => `
      SELECT id, username, display_name
      FROM system_accounts
      WHERE id IN (${sqlPlaceholders(chunk.length)})
    `)
    return rows.map((row) => ({ id: row.id, username: row.username, displayName: row.display_name }))
  })
}

export function loadSystemAccountNameMapByIds(systemAccountIds: Array<string | undefined>): Map<string, string> {
  const accounts = loadSystemAccountPrincipalMapByIds(systemAccountIds)
  return new Map([...accounts].map(([id, account]) => [id, account.displayName]))
}

export async function loadSystemAccountPrincipalMapByIdsAsync(client: DatabaseClient, systemAccountIds: Array<string | undefined>): Promise<Map<string, SystemAccountPrincipalLookup>> {
  return loadCachedRowsByIdsAsync(systemAccountIds, systemAccountPrincipalCache, systemAccountPrincipalSharedCache, async (ids) => {
    const rows: Array<{ id: string; username: string; display_name: string }> = []
    for (const chunk of chunkValues(ids, 900)) {
      rows.push(...await client.query<{ id: string; username: string; display_name: string }>(`
        SELECT id, username, display_name
        FROM ${lookupTable(client, 'system_accounts')}
        WHERE id IN (${sqlPlaceholders(chunk.length)})
      `, chunk))
    }
    return rows.map((row) => ({ id: row.id, username: row.username, displayName: row.display_name }))
  })
}

export async function loadSystemAccountNameMapByIdsAsync(client: DatabaseClient, systemAccountIds: Array<string | undefined>): Promise<Map<string, string>> {
  const accounts = await loadSystemAccountPrincipalMapByIdsAsync(client, systemAccountIds)
  return new Map([...accounts].map(([id, account]) => [id, account.displayName]))
}

export function loadAccountLookupMap(accountIds: Array<string | undefined>): Map<string, BusinessResourceLookup> {
  return loadCachedRowsByIds(accountIds, accountLookupCache, (ids) => {
    const rows = loadRowsByIds<{ id: string; name: string; system_account_id: string; account_expires_at: string | null }>(ids, (chunk) => `
      SELECT id, name, system_account_id, account_expires_at
      FROM accounts
      WHERE id IN (${sqlPlaceholders(chunk.length)})
    `)
    return rows.map((row) => ({ id: row.id, name: row.name, systemAccountId: row.system_account_id, accountExpiresAt: row.account_expires_at ?? undefined }))
  })
}

export function loadAccountNameMap(accountIds: Array<string | undefined>): Map<string, string> {
  const accounts = loadAccountLookupMap(accountIds)
  return new Map([...accounts].map(([id, account]) => [id, account.name]))
}

export async function loadAccountLookupMapAsync(client: DatabaseClient, accountIds: Array<string | undefined>): Promise<Map<string, BusinessResourceLookup>> {
  return loadCachedRowsByIdsAsync(accountIds, accountLookupCache, accountLookupSharedCache, async (ids) => {
    const rows: Array<{ id: string; name: string; system_account_id: string; account_expires_at: string | null }> = []
    for (const chunk of chunkValues(ids, 900)) {
      rows.push(...await client.query<{ id: string; name: string; system_account_id: string; account_expires_at: string | null }>(`
        SELECT id, name, system_account_id, account_expires_at
        FROM ${lookupTable(client, 'accounts')}
        WHERE id IN (${sqlPlaceholders(chunk.length)})
      `, chunk))
    }
    return rows.map((row) => ({ id: row.id, name: row.name, systemAccountId: row.system_account_id, accountExpiresAt: row.account_expires_at ?? undefined }))
  })
}

export async function loadAccountNameMapAsync(client: DatabaseClient, accountIds: Array<string | undefined>): Promise<Map<string, string>> {
  const accounts = await loadAccountLookupMapAsync(client, accountIds)
  return new Map([...accounts].map(([id, account]) => [id, account.name]))
}

export function loadGroupLookupMap(groupIds: Array<string | undefined>): Map<string, BusinessResourceLookup> {
  return loadCachedRowsByIds(groupIds, groupLookupCache, (ids) => {
    const rows = loadRowsByIds<{ id: string; name: string; system_account_id: string }>(ids, (chunk) => `
      SELECT id, name, system_account_id
      FROM groups
      WHERE id IN (${sqlPlaceholders(chunk.length)})
    `)
    return rows.map((row) => ({ id: row.id, name: row.name, systemAccountId: row.system_account_id }))
  })
}

export function loadGroupNameMap(groupIds: Array<string | undefined>): Map<string, string> {
  const groups = loadGroupLookupMap(groupIds)
  return new Map([...groups].map(([id, group]) => [id, group.name]))
}

export async function loadGroupLookupMapAsync(client: DatabaseClient, groupIds: Array<string | undefined>): Promise<Map<string, BusinessResourceLookup>> {
  return loadCachedRowsByIdsAsync(groupIds, groupLookupCache, groupLookupSharedCache, async (ids) => {
    const rows: Array<{ id: string; name: string; system_account_id: string }> = []
    for (const chunk of chunkValues(ids, 900)) {
      rows.push(...await client.query<{ id: string; name: string; system_account_id: string }>(`
        SELECT id, name, system_account_id
        FROM ${lookupTable(client, 'groups')}
        WHERE id IN (${sqlPlaceholders(chunk.length)})
      `, chunk))
    }
    return rows.map((row) => ({ id: row.id, name: row.name, systemAccountId: row.system_account_id }))
  })
}

export async function loadGroupNameMapAsync(client: DatabaseClient, groupIds: Array<string | undefined>): Promise<Map<string, string>> {
  const groups = await loadGroupLookupMapAsync(client, groupIds)
  return new Map([...groups].map(([id, group]) => [id, group.name]))
}

export function loadApiKeyLookupMap(apiKeyIds: Array<string | undefined>): Map<string, BusinessResourceLookup> {
  return loadCachedRowsByIds(apiKeyIds, apiKeyLookupCache, (ids) => {
    const rows = loadRowsByIds<{ id: string; name: string; system_account_id: string }>(ids, (chunk) => `
      SELECT id, name, system_account_id
      FROM api_keys
      WHERE id IN (${sqlPlaceholders(chunk.length)})
    `)
    return rows.map((row) => ({ id: row.id, name: row.name, systemAccountId: row.system_account_id }))
  })
}

export function loadApiKeyNameMap(apiKeyIds: Array<string | undefined>): Map<string, string> {
  const apiKeys = loadApiKeyLookupMap(apiKeyIds)
  return new Map([...apiKeys].map(([id, apiKey]) => [id, apiKey.name]))
}

export async function loadApiKeyLookupMapAsync(client: DatabaseClient, apiKeyIds: Array<string | undefined>): Promise<Map<string, BusinessResourceLookup>> {
  return loadCachedRowsByIdsAsync(apiKeyIds, apiKeyLookupCache, apiKeyLookupSharedCache, async (ids) => {
    const rows: Array<{ id: string; name: string; system_account_id: string }> = []
    for (const chunk of chunkValues(ids, 900)) {
      rows.push(...await client.query<{ id: string; name: string; system_account_id: string }>(`
        SELECT id, name, system_account_id
        FROM ${lookupTable(client, 'api_keys')}
        WHERE id IN (${sqlPlaceholders(chunk.length)})
      `, chunk))
    }
    return rows.map((row) => ({ id: row.id, name: row.name, systemAccountId: row.system_account_id }))
  })
}

export async function loadApiKeyNameMapAsync(client: DatabaseClient, apiKeyIds: Array<string | undefined>): Promise<Map<string, string>> {
  const apiKeys = await loadApiKeyLookupMapAsync(client, apiKeyIds)
  return new Map([...apiKeys].map(([id, apiKey]) => [id, apiKey.name]))
}

export function loadSystemTeamLookupMap(teamIds: Array<string | undefined>): Map<string, SystemTeamLookup> {
  return loadCachedRowsByIds(teamIds, systemTeamLookupCache, (ids) => {
    const rows = loadRowsByIds<{ id: string; name: string; status: SystemTeamStatus }>(ids, (chunk) => `
      SELECT id, name, status
      FROM system_teams
      WHERE id IN (${sqlPlaceholders(chunk.length)})
    `)
    return rows.map((row) => ({ id: row.id, name: row.name, status: row.status }))
  })
}

export function loadSystemTeamNameMap(teamIds: Array<string | undefined>): Map<string, string> {
  const teams = loadSystemTeamLookupMap(teamIds)
  return new Map([...teams].map(([id, team]) => [id, team.name]))
}

export async function loadSystemTeamLookupMapAsync(client: DatabaseClient, teamIds: Array<string | undefined>): Promise<Map<string, SystemTeamLookup>> {
  return loadCachedRowsByIdsAsync(teamIds, systemTeamLookupCache, systemTeamLookupSharedCache, async (ids) => {
    const rows: Array<{ id: string; name: string; status: SystemTeamStatus }> = []
    for (const chunk of chunkValues(ids, 900)) {
      rows.push(...await client.query<{ id: string; name: string; status: SystemTeamStatus }>(`
        SELECT id, name, status
        FROM ${lookupTable(client, 'system_teams')}
        WHERE id IN (${sqlPlaceholders(chunk.length)})
      `, chunk))
    }
    return rows.map((row) => ({ id: row.id, name: row.name, status: row.status }))
  })
}

export async function loadSystemTeamNameMapAsync(client: DatabaseClient, teamIds: Array<string | undefined>): Promise<Map<string, string>> {
  const teams = await loadSystemTeamLookupMapAsync(client, teamIds)
  return new Map([...teams].map(([id, team]) => [id, team.name]))
}

export function loadActiveSystemAccountTeamNameMapByIds(systemAccountIds: Array<string | undefined>): Map<string, string[]> {
  const teams = loadCachedRowsByIds(systemAccountIds, systemAccountTeamNamesCache, (ids) => {
    const rows: Array<{ system_account_id: string; name: string }> = []
    const database = getBusinessDatabase()
    for (const chunk of chunkValues(ids, 900)) {
      rows.push(...database
        .prepare(`
          SELECT members.system_account_id, teams.name
          FROM system_team_members members
          INNER JOIN system_teams teams ON teams.id = members.team_id
          WHERE members.status = 'active'
            AND members.system_account_id IN (${sqlPlaceholders(chunk.length)})
          ORDER BY teams.name COLLATE NOCASE ASC, teams.id ASC
        `)
        .all(...chunk) as unknown as Array<{ system_account_id: string; name: string }>)
    }
    const namesByAccount = new Map<string, string[]>()
    for (const row of rows) {
      namesByAccount.set(row.system_account_id, [...(namesByAccount.get(row.system_account_id) ?? []), row.name])
    }
    return ids.map((id) => ({ id, teamNames: namesByAccount.get(id) ?? [] }))
  })
  return new Map([...teams].map(([id, item]) => [id, [...item.teamNames]]))
}

export async function loadActiveSystemAccountTeamNameMapByIdsAsync(client: DatabaseClient, systemAccountIds: Array<string | undefined>): Promise<Map<string, string[]>> {
  const teams = await loadCachedRowsByIdsAsync(systemAccountIds, systemAccountTeamNamesCache, systemAccountTeamNamesSharedCache, async (ids) => {
    const rows: Array<{ system_account_id: string; name: string }> = []
    for (const chunk of chunkValues(ids, 900)) {
      rows.push(...await client.query<{ system_account_id: string; name: string }>(`
        SELECT members.system_account_id, teams.name
        FROM ${lookupTable(client, 'system_team_members')} members
        INNER JOIN ${lookupTable(client, 'system_teams')} teams ON teams.id = members.team_id
        WHERE members.status = 'active'
          AND members.system_account_id IN (${sqlPlaceholders(chunk.length)})
        ORDER BY teams.name ASC, teams.id ASC
      `, chunk))
    }
    const namesByAccount = new Map<string, string[]>()
    for (const row of rows) {
      namesByAccount.set(row.system_account_id, [...(namesByAccount.get(row.system_account_id) ?? []), row.name])
    }
    return ids.map((id) => ({ id, teamNames: namesByAccount.get(id) ?? [] }))
  })
  return new Map([...teams].map(([id, item]) => [id, [...item.teamNames]]))
}

export function invalidateSystemAccountLookupCache(id?: string): void {
  invalidateLookupCache(systemAccountPrincipalCache, systemAccountPrincipalSharedCache, id)
}

export function invalidateAccountLookupCache(id?: string): void {
  invalidateLookupCache(accountLookupCache, accountLookupSharedCache, id)
}

export function invalidateGroupLookupCache(id?: string): void {
  invalidateLookupCache(groupLookupCache, groupLookupSharedCache, id)
}

export function invalidateApiKeyLookupCache(id?: string): void {
  invalidateLookupCache(apiKeyLookupCache, apiKeyLookupSharedCache, id)
}

export function invalidateSystemTeamLookupCache(id?: string): void {
  invalidateLookupCache(systemTeamLookupCache, systemTeamLookupSharedCache, id)
}

export function invalidateSystemAccountTeamMembershipLookupCache(systemAccountId?: string): void {
  invalidateLookupCache(systemAccountTeamNamesCache, systemAccountTeamNamesSharedCache, systemAccountId)
}

export function clearRepositoryLookupCaches(): void {
  systemAccountPrincipalCache.clear()
  accountLookupCache.clear()
  groupLookupCache.clear()
  apiKeyLookupCache.clear()
  systemTeamLookupCache.clear()
  systemAccountTeamNamesCache.clear()
  clearLookupSharedCache(systemAccountPrincipalSharedCache)
  clearLookupSharedCache(accountLookupSharedCache)
  clearLookupSharedCache(groupLookupSharedCache)
  clearLookupSharedCache(apiKeyLookupSharedCache)
  clearLookupSharedCache(systemTeamLookupSharedCache)
  clearLookupSharedCache(systemAccountTeamNamesSharedCache)
}

function lookupTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function setLookupSharedCacheEntry<T extends { id: string }>(cache: SharedJsonCache<T>, id: string, value: T): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  void setLookupSharedCacheEntryAsync(cache, id, value)
}

async function setLookupSharedCacheEntryAsync<T extends { id: string }>(cache: SharedJsonCache<T>, id: string, value: T): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  try {
    await cache.set(id, value, { ttlMs: lookupCacheTtlMs })
  } catch (error) {
    throwIfRedisCacheIsRequired(error)
    logger.warn(errorLogFields(error, {
      event: 'repository_lookup_shared_cache_write_failed',
      cacheName: cache.name
    }), '写入资源 lookup Redis 共享缓存失败')
  }
}

function deleteLookupSharedCacheEntry<T extends { id: string }>(cache: SharedJsonCache<T>, id: string): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  void cache.delete(id).catch((error) => {
    throwIfRedisCacheIsRequired(error)
    logger.warn(errorLogFields(error, {
      event: 'repository_lookup_shared_cache_delete_failed',
      cacheName: cache.name
    }), '删除资源 lookup Redis 共享缓存失败')
  })
}

function clearLookupSharedCache<T extends { id: string }>(cache: SharedJsonCache<T>): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  void cache.clear().catch((error) => {
    throwIfRedisCacheIsRequired(error)
    logger.warn(errorLogFields(error, {
      event: 'repository_lookup_shared_cache_clear_failed',
      cacheName: cache.name
    }), '清理资源 lookup Redis 共享缓存失败')
  })
}
