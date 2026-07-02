import { runtimeConfig } from '../config/runtime.js'
import { createAppCache, createSharedJsonCache, throwIfRedisCacheIsRequired } from '../shared/cache.js'
import { errorLogFields, logger } from '../shared/logger.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getBusinessDatabase, getStatsDatabase } from './database.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

export type GroupAccountStatsRow = {
  system_account_id: string
  group_id: string
  total: number
  active: number
  disabled: number
  rate_limited: number
  error: number
  available: number
  current_concurrency: number
  concurrency_limit: number
}

function uniqueIds(values: string[]): string[] {
  return [...new Set(values)].filter(Boolean)
}

const groupAccountIdsCache = createAppCache<string, string[]>({
  name: 'lookup:group-account-ids',
  max: 10_000,
  ttlMs: 60 * 1000,
  updateAgeOnGet: true
})

const groupAccountIdsSharedCache = createSharedJsonCache<string[]>({
  name: 'lookup:group-account-ids',
  max: 10_000,
  ttlMs: 60 * 1000
})

export function loadGroupAccountIdsByGroupIds(groupIds: string[]): Map<string, string[]> {
  assertSyncGroupReadLoaderAllowed('loadGroupAccountIdsByGroupIds')
  const ids = uniqueIds(groupIds)
  if (!ids.length) return new Map()
  const result = new Map<string, string[]>()
  const missingIds: string[] = []
  for (const id of ids) {
    const cached = groupAccountIdsCache.get(id)
    if (cached !== undefined) {
      result.set(id, [...cached])
    } else {
      missingIds.push(id)
    }
  }
  if (!missingIds.length) return result

  const rows: Array<{ group_id: string; account_id: string }> = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(missingIds, 900)) {
    rows.push(...database
      .prepare(`
        SELECT group_accounts.group_id, group_accounts.account_id
        FROM group_accounts
        INNER JOIN groups ON groups.id = group_accounts.group_id
        INNER JOIN accounts ON accounts.id = group_accounts.account_id
        LEFT JOIN resource_authorizations resource_authorization_rows
          ON resource_authorization_rows.id = group_accounts.account_authorization_id
        WHERE group_accounts.enabled = 1
          AND group_accounts.group_id IN (${sqlPlaceholders(chunk.length)})
          AND accounts.deleted_at IS NULL
          AND (
            accounts.system_account_id = groups.system_account_id
            OR (
              resource_authorization_rows.status IN ('active', 'paused', 'expired')
            )
          )
        ORDER BY group_accounts.group_id ASC, group_accounts.created_at ASC, group_accounts.account_id ASC
      `)
      .all(...chunk) as unknown as Array<{ group_id: string; account_id: string }>)
  }
  const loaded = new Map<string, string[]>()
  for (const row of rows) {
    loaded.set(row.group_id, [...(loaded.get(row.group_id) ?? []), row.account_id])
  }
  for (const id of missingIds) {
    const accountIds = loaded.get(id) ?? []
    groupAccountIdsCache.set(id, accountIds)
    setGroupAccountIdsSharedCacheEntry(id, accountIds)
    result.set(id, [...accountIds])
  }
  return result
}

function assertSyncGroupReadLoaderAllowed(operation: string): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  throw new Error(`高性能模式禁止同步读取本地分组 loader：${operation} 必须使用 Redis async loader`)
}

export async function loadGroupAccountIdsByGroupIdsAsync(groupIds: string[]): Promise<Map<string, string[]>> {
  const ids = uniqueIds(groupIds)
  if (!ids.length) return new Map()
  const result = new Map<string, string[]>()
  const missingSharedIds: string[] = []
  if (runtimeConfig.cacheDriver !== 'redis') {
    for (const id of ids) {
      const cached = groupAccountIdsCache.get(id)
      if (cached !== undefined) {
        result.set(id, [...cached])
      } else {
        missingSharedIds.push(id)
      }
    }
  } else {
    missingSharedIds.push(...ids)
  }
  if (!missingSharedIds.length) return result

  const missingDatabaseIds: string[] = []
  for (const id of missingSharedIds) {
    const sharedCached = await getGroupAccountIdsSharedCacheEntry(id)
    if (sharedCached !== undefined) {
      groupAccountIdsCache.set(id, sharedCached)
      result.set(id, [...sharedCached])
    } else {
      missingDatabaseIds.push(id)
    }
  }
  if (!missingDatabaseIds.length) return result

  const loaded = await loadGroupAccountIdsFromDatabaseAsync(missingDatabaseIds)
  for (const id of missingDatabaseIds) {
    const accountIds = loaded.get(id) ?? []
    await setGroupAccountIdsSharedCacheEntryAsync(id, accountIds)
    groupAccountIdsCache.set(id, accountIds)
    result.set(id, [...accountIds])
  }
  return result
}

export function invalidateGroupAccountIdsCache(groupId?: string): void {
  const id = groupId?.trim()
  if (id) {
    groupAccountIdsCache.delete(id)
    deleteGroupAccountIdsSharedCacheEntry(id)
    return
  }
  groupAccountIdsCache.clear()
  clearGroupAccountIdsSharedCache()
}

export function loadGroupAccountStatsByGroupIds(groupIds: string[]): Map<string, GroupAccountStatsRow> {
  assertSyncGroupReadLoaderAllowed('loadGroupAccountStatsByGroupIds')
  const ids = uniqueIds(groupIds)
  if (!ids.length) return new Map()
  const rows: GroupAccountStatsRow[] = []
  const database = getStatsDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...database
      .prepare(`
        SELECT ${groupAccountStatsSelectColumns()}
        FROM group_account_stats
        WHERE group_id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(...chunk) as unknown as GroupAccountStatsRow[])
  }
  return new Map(rows.map((row) => [row.group_id, row]))
}

export async function loadGroupAccountStatsByGroupIdsAsync(groupIds: string[]): Promise<Map<string, GroupAccountStatsRow>> {
  const ids = uniqueIds(groupIds)
  if (!ids.length) return new Map()
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return loadGroupAccountStatsByGroupIds(ids)
  }
  const rows: GroupAccountStatsRow[] = []
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const tableName = client.dialect.qualifyTable('juhe_stats', 'group_account_stats')
  for (const chunk of chunkValues(ids, 500)) {
    rows.push(...await client.query<GroupAccountStatsRow>(`
      SELECT ${groupAccountStatsSelectColumns()}
      FROM ${tableName}
      WHERE group_id IN (${client.dialect.bindPlaceholders(chunk.length)})
    `, chunk))
  }
  return new Map(rows.map((row) => [row.group_id, row]))
}

function groupAccountStatsSelectColumns(): string {
  return [
    'system_account_id',
    'group_id',
    'total',
    'active',
    'disabled',
    'rate_limited',
    'error',
    'available',
    'current_concurrency',
    'concurrency_limit'
  ].join(', ')
}

async function loadGroupAccountIdsFromDatabaseAsync(ids: string[]): Promise<Map<string, string[]>> {
  const result = new Map<string, string[]>()
  const client = await groupReadLoaderDatabaseClient()
  const groupAccountsTable = groupReadLoaderTable(client, 'group_accounts')
  const groupsTable = groupReadLoaderTable(client, 'groups')
  const accountsTable = groupReadLoaderTable(client, 'accounts')
  const resourceAuthorizationsTable = groupReadLoaderTable(client, 'resource_authorizations')
  const rows: Array<{ group_id: string; account_id: string }> = []
  for (const chunk of chunkValues(ids, 500)) {
    rows.push(...await client.query<{ group_id: string; account_id: string }>(`
      SELECT group_accounts.group_id, group_accounts.account_id
      FROM ${groupAccountsTable} group_accounts
      INNER JOIN ${groupsTable} groups ON groups.id = group_accounts.group_id
      INNER JOIN ${accountsTable} accounts ON accounts.id = group_accounts.account_id
      LEFT JOIN ${resourceAuthorizationsTable} resource_authorization_rows
        ON resource_authorization_rows.id = group_accounts.account_authorization_id
      WHERE group_accounts.enabled = 1
        AND group_accounts.group_id IN (${client.dialect.bindPlaceholders(chunk.length)})
        AND accounts.deleted_at IS NULL
        AND (
          accounts.system_account_id = groups.system_account_id
          OR (
            resource_authorization_rows.status IN ('active', 'paused', 'expired')
          )
        )
      ORDER BY group_accounts.group_id ASC, group_accounts.created_at ASC, group_accounts.account_id ASC
    `, chunk))
  }
  for (const id of ids) {
    result.set(id, [])
  }
  for (const row of rows) {
    result.set(row.group_id, [...(result.get(row.group_id) ?? []), row.account_id])
  }
  return result
}

async function groupReadLoaderDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function groupReadLoaderTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

async function getGroupAccountIdsSharedCacheEntry(groupId: string): Promise<string[] | undefined> {
  try {
    const value = await groupAccountIdsSharedCache.get(groupId)
    return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : undefined
  } catch (error) {
    throwIfRedisCacheIsRequired(error)
    logger.warn({
      event: 'group_account_ids_shared_cache_read_failed',
      groupId,
      err: errorLogFields(error)
    }, '读取分组账号 ID Redis shared cache 失败')
    return undefined
  }
}

function setGroupAccountIdsSharedCacheEntry(groupId: string, accountIds: string[]): void {
  void setGroupAccountIdsSharedCacheEntryAsync(groupId, accountIds)
}

async function setGroupAccountIdsSharedCacheEntryAsync(groupId: string, accountIds: string[]): Promise<void> {
  try {
    await groupAccountIdsSharedCache.set(groupId, [...accountIds])
  } catch (error) {
    throwIfRedisCacheIsRequired(error)
    logger.warn({
      event: 'group_account_ids_shared_cache_write_failed',
      groupId,
      err: errorLogFields(error)
    }, '写入分组账号 ID Redis shared cache 失败')
  }
}

function deleteGroupAccountIdsSharedCacheEntry(groupId: string): void {
  void groupAccountIdsSharedCache.delete(groupId).catch((error) => {
    throwIfRedisCacheIsRequired(error)
    logger.warn({
      event: 'group_account_ids_shared_cache_delete_failed',
      groupId,
      err: errorLogFields(error)
    }, '删除分组账号 ID Redis shared cache 失败')
  })
}

function clearGroupAccountIdsSharedCache(): void {
  void groupAccountIdsSharedCache.clear().catch((error) => {
    throwIfRedisCacheIsRequired(error)
    logger.warn({
      event: 'group_account_ids_shared_cache_clear_failed',
      err: errorLogFields(error)
    }, '清理分组账号 ID Redis shared cache 失败')
  })
}
