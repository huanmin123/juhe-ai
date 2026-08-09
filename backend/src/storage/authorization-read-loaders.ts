import type {
  ResourceAuthorizationResourceType,
  ResourceAuthorizationSourceStatus,
  ResourceAuthorizationSourceSummary,
  ResourceAuthorizationSourceType
} from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { clearSharedJsonCacheInBackground, createAppCache, createSharedJsonCache } from '../shared/cache.js'
import { getBusinessDatabase } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

interface ResourceAuthorizationSourceRow {
  id: string
  authorization_id: string
  source_type: ResourceAuthorizationSourceType
  source_team_id: string | null
  status: ResourceAuthorizationSourceStatus
  activated_at: string | null
  ended_at: string | null
  ended_reason: string | null
  created_by: string
  created_at: string
  revoked_by: string | null
  revoked_at: string | null
  updated_at: string
}

export interface ResourceAuthorizationStats {
  authorizationCount: number
  authorizationTeamCount: number
}

function uniqueIds(values: string[]): string[] {
  return [...new Set(values)].filter(Boolean)
}

export async function loadSharedCacheEntriesByBatches<T>(
  values: string[],
  load: (id: string) => Promise<T | undefined>
): Promise<Array<readonly [string, T | undefined]>> {
  const ids = uniqueIds(values)
  const result: Array<readonly [string, T | undefined]> = []
  for (const chunk of chunkValues(ids, 100)) {
    result.push(...await Promise.all(chunk.map(async (id) => [id, await load(id)] as const)))
  }
  return result
}

const authorizationStatsCache = createAppCache<string, ResourceAuthorizationStats>({
  name: 'lookup:resource-authorization-stats',
  max: 10_000,
  ttlMs: 5 * 60 * 1000,
  updateAgeOnGet: true
})

const authorizationSourcesCache = createAppCache<string, ResourceAuthorizationSourceSummary[]>({
  name: 'lookup:resource-authorization-sources',
  max: 10_000,
  ttlMs: 5 * 60 * 1000,
  updateAgeOnGet: true
})

const authorizationStatsSharedCache = createSharedJsonCache<ResourceAuthorizationStats>({
  name: 'lookup:resource-authorization-stats',
  max: 10_000,
  ttlMs: 5 * 60 * 1000
})

const authorizationSourcesSharedCache = createSharedJsonCache<ResourceAuthorizationSourceSummary[]>({
  name: 'lookup:resource-authorization-sources',
  max: 10_000,
  ttlMs: 5 * 60 * 1000
})

const emptyAuthorizationStats: ResourceAuthorizationStats = {
  authorizationCount: 0,
  authorizationTeamCount: 0
}

export function loadResourceAuthorizationStatsByResourceIds(resourceType: ResourceAuthorizationResourceType, resourceIds: string[]): Map<string, ResourceAuthorizationStats> {
  assertSyncAuthorizationReadLoaderAllowed('loadResourceAuthorizationStatsByResourceIds')
  const ids = uniqueIds(resourceIds)
  if (!ids.length) return new Map()
  const result = new Map<string, ResourceAuthorizationStats>()
  const missingIds: string[] = []
  for (const id of ids) {
    const cached = authorizationStatsCache.get(resourceAuthorizationStatsCacheKey(resourceType, id))
    if (cached !== undefined) {
      result.set(id, cached)
    } else {
      missingIds.push(id)
    }
  }
  if (!missingIds.length) return result

  const rows: Array<{ resource_id: string; authorization_count: number; authorization_team_count: number }> = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(missingIds, 900)) {
    rows.push(...database.prepare(`
      SELECT
        ra.resource_id,
        COUNT(DISTINCT ra.id) AS authorization_count,
        COUNT(DISTINCT CASE WHEN ras.source_type = 'team' AND ras.status = 'active' THEN ras.source_team_id END) AS authorization_team_count
      FROM resource_authorizations ra
      LEFT JOIN resource_authorization_sources ras
        ON ras.authorization_id = ra.id
        AND ras.source_type = 'team'
        AND ras.status = 'active'
      WHERE ra.resource_type = ?
        AND ra.status = 'active'
        AND ra.resource_id IN (${sqlPlaceholders(chunk.length)})
      GROUP BY ra.resource_id
    `).all(resourceType, ...chunk) as unknown as Array<{ resource_id: string; authorization_count: number; authorization_team_count: number }>)
  }
  const loaded = new Map(rows.map((row) => [row.resource_id, {
    authorizationCount: Number(row.authorization_count ?? 0),
    authorizationTeamCount: Number(row.authorization_team_count ?? 0)
  }]))
  for (const id of missingIds) {
    const stats = loaded.get(id) ?? emptyAuthorizationStats
    const cacheKey = resourceAuthorizationStatsCacheKey(resourceType, id)
    authorizationStatsCache.set(cacheKey, stats)
    setAuthorizationStatsSharedCacheEntry(cacheKey, stats)
    result.set(id, stats)
  }
  return result
}

export async function loadResourceAuthorizationStatsByResourceIdsAsync(resourceType: ResourceAuthorizationResourceType, resourceIds: string[]): Promise<Map<string, ResourceAuthorizationStats>> {
  const ids = uniqueIds(resourceIds)
  if (!ids.length) return new Map()
  const result = new Map<string, ResourceAuthorizationStats>()
  const missingSharedIds: string[] = []
  if (runtimeConfig.cacheDriver !== 'redis') {
    for (const id of ids) {
      const cacheKey = resourceAuthorizationStatsCacheKey(resourceType, id)
      const cached = authorizationStatsCache.get(cacheKey)
      if (cached !== undefined) {
        result.set(id, cached)
      } else {
        missingSharedIds.push(id)
      }
    }
  } else {
    missingSharedIds.push(...ids)
  }
  if (!missingSharedIds.length) return result

  const missingDatabaseIds: string[] = []
  const sharedResults = await loadSharedCacheEntriesByBatches(missingSharedIds, async (id) => {
    const cacheKey = resourceAuthorizationStatsCacheKey(resourceType, id)
    return getAuthorizationStatsSharedCacheEntry(cacheKey)
  })
  for (const [id, sharedCached] of sharedResults) {
    const cacheKey = resourceAuthorizationStatsCacheKey(resourceType, id)
    if (sharedCached !== undefined) {
      authorizationStatsCache.set(cacheKey, sharedCached)
      result.set(id, sharedCached)
    } else {
      missingDatabaseIds.push(id)
    }
  }
  if (!missingDatabaseIds.length) return result

  const loaded = await loadResourceAuthorizationStatsFromDatabaseAsync(resourceType, missingDatabaseIds)
  for (const id of missingDatabaseIds) {
    const stats = loaded.get(id) ?? emptyAuthorizationStats
    const cacheKey = resourceAuthorizationStatsCacheKey(resourceType, id)
    await setAuthorizationStatsSharedCacheEntryAsync(cacheKey, stats)
    authorizationStatsCache.set(cacheKey, stats)
    result.set(id, stats)
  }
  return result
}

export function loadResourceAuthorizationSourcesByAuthorizationIds(authorizationIds: string[]): Map<string, ResourceAuthorizationSourceSummary[]> {
  assertSyncAuthorizationReadLoaderAllowed('loadResourceAuthorizationSourcesByAuthorizationIds')
  const ids = uniqueIds(authorizationIds)
  if (!ids.length) return new Map()
  const result = new Map<string, ResourceAuthorizationSourceSummary[]>()
  const missingIds: string[] = []
  for (const id of ids) {
    const cached = authorizationSourcesCache.get(id)
    if (cached !== undefined) {
      result.set(id, [...cached])
    } else {
      missingIds.push(id)
    }
  }
  if (!missingIds.length) return result

  const rows: Array<ResourceAuthorizationSourceRow & { team_name?: string | null }> = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(missingIds, 900)) {
    rows.push(...database.prepare(`
      SELECT ${resourceAuthorizationSourceSelectColumns('ras')}, system_teams.name AS team_name
      FROM resource_authorization_sources ras
      LEFT JOIN system_teams ON system_teams.id = ras.source_team_id
      WHERE ras.authorization_id IN (${sqlPlaceholders(chunk.length)})
      ORDER BY ras.status ASC, ras.created_at ASC, ras.id ASC
    `).all(...chunk) as unknown as Array<ResourceAuthorizationSourceRow & { team_name?: string | null }>)
  }
  const loaded = new Map<string, ResourceAuthorizationSourceSummary[]>()
  for (const row of rows) {
    const summary: ResourceAuthorizationSourceSummary = {
      id: row.id,
      authorizationId: row.authorization_id,
      sourceType: row.source_type,
      sourceTeamId: row.source_team_id ?? undefined,
      sourceTeamName: row.team_name ?? undefined,
      status: row.status,
      activatedAt: row.activated_at ?? undefined,
      endedAt: row.ended_at ?? undefined,
      endedReason: row.ended_reason ?? undefined,
      createdBy: row.created_by,
      createdAt: row.created_at,
      revokedBy: row.revoked_by ?? undefined,
      revokedAt: row.revoked_at ?? undefined,
      updatedAt: row.updated_at
    }
    loaded.set(row.authorization_id, [...(loaded.get(row.authorization_id) ?? []), summary])
  }
  for (const id of missingIds) {
    const sources = loaded.get(id) ?? []
    authorizationSourcesCache.set(id, sources)
    setAuthorizationSourcesSharedCacheEntry(id, sources)
    result.set(id, [...sources])
  }
  return result
}

function assertSyncAuthorizationReadLoaderAllowed(operation: string): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  throw new Error(`高性能模式禁止同步读取本地授权 loader：${operation} 必须使用 Redis async loader`)
}

export async function loadResourceAuthorizationSourcesByAuthorizationIdsAsync(authorizationIds: string[]): Promise<Map<string, ResourceAuthorizationSourceSummary[]>> {
  const ids = uniqueIds(authorizationIds)
  if (!ids.length) return new Map()
  const result = new Map<string, ResourceAuthorizationSourceSummary[]>()
  const missingSharedIds: string[] = []
  if (runtimeConfig.cacheDriver !== 'redis') {
    for (const id of ids) {
      const cached = authorizationSourcesCache.get(id)
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
  const sharedResults = await loadSharedCacheEntriesByBatches(
    missingSharedIds,
    async (id) => getAuthorizationSourcesSharedCacheEntry(id)
  )
  for (const [id, sharedCached] of sharedResults) {
    if (sharedCached !== undefined) {
      authorizationSourcesCache.set(id, sharedCached)
      result.set(id, [...sharedCached])
    } else {
      missingDatabaseIds.push(id)
    }
  }
  if (!missingDatabaseIds.length) return result

  const loaded = await loadResourceAuthorizationSourcesFromDatabaseAsync(missingDatabaseIds)
  for (const id of missingDatabaseIds) {
    const sources = loaded.get(id) ?? []
    await setAuthorizationSourcesSharedCacheEntryAsync(id, sources)
    authorizationSourcesCache.set(id, sources)
    result.set(id, [...sources])
  }
  return result
}

export function invalidateResourceAuthorizationStatsCache(resourceType?: ResourceAuthorizationResourceType, resourceIds?: string | string[]): void {
  if (!resourceType || resourceIds === undefined) {
    authorizationStatsCache.clear()
    clearAuthorizationStatsSharedCache()
    return
  }
  const ids = Array.isArray(resourceIds) ? resourceIds : [resourceIds]
  for (const id of uniqueIds(ids)) {
    const cacheKey = resourceAuthorizationStatsCacheKey(resourceType, id)
    authorizationStatsCache.delete(cacheKey)
    deleteAuthorizationStatsSharedCacheEntry(cacheKey)
  }
}

export function invalidateResourceAuthorizationSourceCache(authorizationIds?: string | string[]): void {
  if (authorizationIds === undefined) {
    authorizationSourcesCache.clear()
    clearAuthorizationSourcesSharedCache()
    return
  }
  const ids = Array.isArray(authorizationIds) ? authorizationIds : [authorizationIds]
  for (const id of uniqueIds(ids)) {
    authorizationSourcesCache.delete(id)
    deleteAuthorizationSourcesSharedCacheEntry(id)
  }
}

export function clearResourceAuthorizationLookupCaches(): void {
  authorizationStatsCache.clear()
  authorizationSourcesCache.clear()
  clearAuthorizationStatsSharedCache()
  clearAuthorizationSourcesSharedCache()
}

function resourceAuthorizationStatsCacheKey(resourceType: ResourceAuthorizationResourceType, resourceId: string): string {
  return `${resourceType}:${resourceId}`
}

function resourceAuthorizationSourceSelectColumns(alias: string): string {
  return [
    'id',
    'authorization_id',
    'source_type',
    'source_team_id',
    'status',
    'activated_at',
    'ended_at',
    'ended_reason',
    'created_by',
    'created_at',
    'revoked_by',
    'revoked_at',
    'updated_at'
  ].map((column) => `${alias}.${column}`).join(', ')
}

async function loadResourceAuthorizationStatsFromDatabaseAsync(
  resourceType: ResourceAuthorizationResourceType,
  resourceIds: string[]
): Promise<Map<string, ResourceAuthorizationStats>> {
  const client = await authorizationReadLoaderDatabaseClient()
  const resourceAuthorizationsTable = authorizationReadLoaderTable(client, 'resource_authorizations')
  const resourceAuthorizationSourcesTable = authorizationReadLoaderTable(client, 'resource_authorization_sources')
  const rows: Array<{ resource_id: string; authorization_count: number; authorization_team_count: number }> = []
  for (const chunk of chunkValues(resourceIds, 500)) {
    rows.push(...await client.query<{ resource_id: string; authorization_count: number; authorization_team_count: number }>(`
      SELECT
        ra.resource_id,
        COUNT(DISTINCT ra.id) AS authorization_count,
        COUNT(DISTINCT CASE WHEN ras.source_type = 'team' AND ras.status = 'active' THEN ras.source_team_id END) AS authorization_team_count
      FROM ${resourceAuthorizationsTable} ra
      LEFT JOIN ${resourceAuthorizationSourcesTable} ras
        ON ras.authorization_id = ra.id
        AND ras.source_type = 'team'
        AND ras.status = 'active'
      WHERE ra.resource_type = ?
        AND ra.status = 'active'
        AND ra.resource_id IN (${client.dialect.bindPlaceholders(chunk.length)})
      GROUP BY ra.resource_id
    `, [resourceType, ...chunk]))
  }
  return new Map(rows.map((row) => [row.resource_id, {
    authorizationCount: Number(row.authorization_count ?? 0),
    authorizationTeamCount: Number(row.authorization_team_count ?? 0)
  }]))
}

async function loadResourceAuthorizationSourcesFromDatabaseAsync(authorizationIds: string[]): Promise<Map<string, ResourceAuthorizationSourceSummary[]>> {
  const client = await authorizationReadLoaderDatabaseClient()
  const resourceAuthorizationSourcesTable = authorizationReadLoaderTable(client, 'resource_authorization_sources')
  const systemTeamsTable = authorizationReadLoaderTable(client, 'system_teams')
  const rows: Array<ResourceAuthorizationSourceRow & { team_name?: string | null }> = []
  for (const chunk of chunkValues(authorizationIds, 500)) {
    rows.push(...await client.query<ResourceAuthorizationSourceRow & { team_name?: string | null }>(`
      SELECT ${resourceAuthorizationSourceSelectColumns('ras')}, system_teams.name AS team_name
      FROM ${resourceAuthorizationSourcesTable} ras
      LEFT JOIN ${systemTeamsTable} system_teams ON system_teams.id = ras.source_team_id
      WHERE ras.authorization_id IN (${client.dialect.bindPlaceholders(chunk.length)})
      ORDER BY ras.status ASC, ras.created_at ASC, ras.id ASC
    `, chunk))
  }
  return authorizationSourcesByAuthorizationIdFromRows(rows)
}

function authorizationSourcesByAuthorizationIdFromRows(rows: Array<ResourceAuthorizationSourceRow & { team_name?: string | null }>): Map<string, ResourceAuthorizationSourceSummary[]> {
  const loaded = new Map<string, ResourceAuthorizationSourceSummary[]>()
  for (const row of rows) {
    const summary: ResourceAuthorizationSourceSummary = {
      id: row.id,
      authorizationId: row.authorization_id,
      sourceType: row.source_type,
      sourceTeamId: row.source_team_id ?? undefined,
      sourceTeamName: row.team_name ?? undefined,
      status: row.status,
      activatedAt: row.activated_at ?? undefined,
      endedAt: row.ended_at ?? undefined,
      endedReason: row.ended_reason ?? undefined,
      createdBy: row.created_by,
      createdAt: row.created_at,
      revokedBy: row.revoked_by ?? undefined,
      revokedAt: row.revoked_at ?? undefined,
      updatedAt: row.updated_at
    }
    loaded.set(row.authorization_id, [...(loaded.get(row.authorization_id) ?? []), summary])
  }
  return loaded
}

async function authorizationReadLoaderDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function authorizationReadLoaderTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

async function getAuthorizationStatsSharedCacheEntry(cacheKey: string): Promise<ResourceAuthorizationStats | undefined> {
  const value = await authorizationStatsSharedCache.get(cacheKey)
  return normalizeAuthorizationStats(value)
}

function setAuthorizationStatsSharedCacheEntry(cacheKey: string, stats: ResourceAuthorizationStats): void {
  void setAuthorizationStatsSharedCacheEntryAsync(cacheKey, stats)
}

async function setAuthorizationStatsSharedCacheEntryAsync(cacheKey: string, stats: ResourceAuthorizationStats): Promise<void> {
  await authorizationStatsSharedCache.set(cacheKey, cloneAuthorizationStats(stats))
}

function deleteAuthorizationStatsSharedCacheEntry(cacheKey: string): void {
  void authorizationStatsSharedCache.delete(cacheKey)
}

function clearAuthorizationStatsSharedCache(): void {
  clearSharedJsonCacheInBackground(
    authorizationStatsSharedCache,
    'authorization_stats_shared_cache_clear_failed',
    '授权统计 Redis shared cache 清理失败'
  )
}

async function getAuthorizationSourcesSharedCacheEntry(authorizationId: string): Promise<ResourceAuthorizationSourceSummary[] | undefined> {
  const value = await authorizationSourcesSharedCache.get(authorizationId)
  return Array.isArray(value) ? value.map(cloneAuthorizationSourceSummary).filter((item): item is ResourceAuthorizationSourceSummary => Boolean(item)) : undefined
}

function setAuthorizationSourcesSharedCacheEntry(authorizationId: string, sources: ResourceAuthorizationSourceSummary[]): void {
  void setAuthorizationSourcesSharedCacheEntryAsync(authorizationId, sources)
}

async function setAuthorizationSourcesSharedCacheEntryAsync(authorizationId: string, sources: ResourceAuthorizationSourceSummary[]): Promise<void> {
  await authorizationSourcesSharedCache.set(authorizationId, sources.map((source) => ({ ...source })))
}

function deleteAuthorizationSourcesSharedCacheEntry(authorizationId: string): void {
  void authorizationSourcesSharedCache.delete(authorizationId)
}

function clearAuthorizationSourcesSharedCache(): void {
  clearSharedJsonCacheInBackground(
    authorizationSourcesSharedCache,
    'authorization_sources_shared_cache_clear_failed',
    '授权来源 Redis shared cache 清理失败'
  )
}

function normalizeAuthorizationStats(value: ResourceAuthorizationStats | undefined): ResourceAuthorizationStats | undefined {
  if (!value || typeof value !== 'object') return undefined
  return {
    authorizationCount: Math.max(0, Number(value.authorizationCount ?? 0)),
    authorizationTeamCount: Math.max(0, Number(value.authorizationTeamCount ?? 0))
  }
}

function cloneAuthorizationStats(value: ResourceAuthorizationStats): ResourceAuthorizationStats {
  return {
    authorizationCount: Math.max(0, Number(value.authorizationCount ?? 0)),
    authorizationTeamCount: Math.max(0, Number(value.authorizationTeamCount ?? 0))
  }
}

function cloneAuthorizationSourceSummary(value: ResourceAuthorizationSourceSummary | undefined): ResourceAuthorizationSourceSummary | undefined {
  if (!value || typeof value !== 'object') return undefined
  return {
    id: value.id,
    authorizationId: value.authorizationId,
    sourceType: value.sourceType,
    sourceTeamId: value.sourceTeamId,
    sourceTeamName: value.sourceTeamName,
    status: value.status,
    activatedAt: value.activatedAt,
    endedAt: value.endedAt,
    endedReason: value.endedReason,
    createdBy: value.createdBy,
    createdAt: value.createdAt,
    revokedBy: value.revokedBy,
    revokedAt: value.revokedAt,
    updatedAt: value.updatedAt
  }
}
