import type { SystemAccountSummary, SystemTeamStatus } from '../domain/types.js'
import { createAppCache, type AppCache } from '../shared/cache.js'
import { getDatabase } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { systemAccountSummaryFromRow, type SystemAccountSummaryRow } from './system-account-mappers.js'

const lookupCacheTtlMs = 10 * 60 * 1000
const lookupCacheMax = 10_000

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

const accountLookupCache = createAppCache<string, BusinessResourceLookup>({
  name: 'lookup:account',
  max: lookupCacheMax,
  ttlMs: lookupCacheTtlMs,
  updateAgeOnGet: true
})

const groupLookupCache = createAppCache<string, BusinessResourceLookup>({
  name: 'lookup:group',
  max: lookupCacheMax,
  ttlMs: lookupCacheTtlMs,
  updateAgeOnGet: true
})

const apiKeyLookupCache = createAppCache<string, BusinessResourceLookup>({
  name: 'lookup:api-key',
  max: lookupCacheMax,
  ttlMs: lookupCacheTtlMs,
  updateAgeOnGet: true
})

const systemTeamLookupCache = createAppCache<string, SystemTeamLookup>({
  name: 'lookup:system-team',
  max: lookupCacheMax,
  ttlMs: lookupCacheTtlMs,
  updateAgeOnGet: true
})

const systemAccountTeamNamesCache = createAppCache<string, SystemAccountTeamNamesLookup>({
  name: 'lookup:system-account-team-names',
  max: lookupCacheMax,
  ttlMs: lookupCacheTtlMs,
  updateAgeOnGet: true
})

function uniqueIds(values: Array<string | null | undefined>): string[] {
  return [...new Set(values.map((value) => value?.trim()).filter((value): value is string => Boolean(value)))]
}

function loadRowsByIds<T>(ids: string[], sql: (chunk: string[]) => string): T[] {
  const rows: T[] = []
  const database = getDatabase()
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

function invalidateLookupCache<T extends { id: string }>(cache: AppCache<string, T>, id?: string): void {
  const normalizedId = id?.trim()
  if (normalizedId) {
    cache.delete(normalizedId)
    return
  }
  cache.clear()
}

export function loadSystemAccountNameMap(): Map<string, string> {
  const rows = getDatabase()
    .prepare('SELECT id, username, display_name FROM system_accounts ORDER BY created_at ASC, id ASC')
    .all() as unknown as Array<{ id: string; username: string; display_name: string }>
  for (const row of rows) {
    systemAccountPrincipalCache.set(row.id, { id: row.id, username: row.username, displayName: row.display_name })
  }
  return new Map(rows.map((row) => [row.id, row.display_name]))
}

export function loadSystemAccountsByIds(systemAccountIds: Array<string | undefined>): Map<string, SystemAccountSummary> {
  const ids = uniqueIds(systemAccountIds)
  if (!ids.length) return new Map()
  const rows = loadRowsByIds<SystemAccountSummaryRow>(ids, (chunk) => `
    SELECT id, username, display_name, description, role, status, must_change_password, last_login_at, created_at, updated_at
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

export function loadActiveSystemAccountTeamNameMapByIds(systemAccountIds: Array<string | undefined>): Map<string, string[]> {
  const teams = loadCachedRowsByIds(systemAccountIds, systemAccountTeamNamesCache, (ids) => {
    const rows: Array<{ system_account_id: string; name: string }> = []
    const database = getDatabase()
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

export function invalidateSystemAccountLookupCache(id?: string): void {
  invalidateLookupCache(systemAccountPrincipalCache, id)
}

export function invalidateAccountLookupCache(id?: string): void {
  invalidateLookupCache(accountLookupCache, id)
}

export function invalidateGroupLookupCache(id?: string): void {
  invalidateLookupCache(groupLookupCache, id)
}

export function invalidateApiKeyLookupCache(id?: string): void {
  invalidateLookupCache(apiKeyLookupCache, id)
}

export function invalidateSystemTeamLookupCache(id?: string): void {
  invalidateLookupCache(systemTeamLookupCache, id)
}

export function invalidateSystemAccountTeamMembershipLookupCache(systemAccountId?: string): void {
  invalidateLookupCache(systemAccountTeamNamesCache, systemAccountId)
}

export function clearRepositoryLookupCaches(): void {
  systemAccountPrincipalCache.clear()
  accountLookupCache.clear()
  groupLookupCache.clear()
  apiKeyLookupCache.clear()
  systemTeamLookupCache.clear()
  systemAccountTeamNamesCache.clear()
}
