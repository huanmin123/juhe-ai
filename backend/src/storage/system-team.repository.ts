import type { SystemTeamListResult, SystemTeamMemberSummary, SystemTeamSummary } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { notifyAuthorizationQuotaCacheInvalidation, notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { clearResourceAuthorizationLookupCaches } from './authorization-read-loaders.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { normalizeListPage, pagedTotalUpperBound, sqlPlaceholders, takePageRows, chunkValues } from './query-utils.js'
import {
  invalidateSystemAccountTeamMembershipLookupCache,
  invalidateSystemTeamLookupCache
} from './repository-lookups.js'
import type { SystemTeamMemberRow, SystemTeamRow } from './repository-row-types.js'
import {
  applyActiveTeamGrantsToMember,
  reactivateTeamGrantSources,
  revokeAllTeamSources,
  revokeTeamSourcesForMember
} from './resource-authorization-write-state.repository.js'
import {
  applyActiveTeamGrantsToMemberAsync,
  reactivateTeamGrantSourcesAsync,
  revokeAllTeamSourcesAsync,
  revokeTeamSourcesForMemberAsync
} from './resource-authorization-write.repository.js'
import { findSystemAccountById } from './system-accounts.repository.js'
import { maxSystemTeamListPageSize, maxSystemTeamMemberBatchSize, maxSystemTeamMembersPerTeam } from './system-team-limits.js'
import { markAllGroupAccountStatsDirty, markAllGroupAccountStatsDirtyAsync } from './usage-stats.repository.js'
import { optionalString } from './value-utils.js'

export interface SystemTeamListOptions {
  page?: number
  pageSize?: number
  keyword?: string
}

interface NormalizedSystemTeamListOptions {
  page: number
  pageSize: number
  keyword?: string
}

const systemTeamInputKeys = new Set(['name', 'description', 'status'])
const systemTeamMembersInputKeys = new Set(['systemAccountIds'])
const businessSchemaName = 'juhe_business'

export function listSystemTeams(access?: AccessScope): SystemTeamSummary[] {
  const rows = querySystemTeamRows(access, undefined, normalizeSystemTeamListOptions()).rows
  const members = listSystemTeamMembersForTeamIds(rows.map((row) => row.id), true)
  return rows.map((row) => systemTeamSummaryFromRow(row, members.get(row.id) ?? []))
}

export function listSystemTeamsPage(access?: AccessScope, options: SystemTeamListOptions = {}): SystemTeamListResult {
  const listOptions = normalizeSystemTeamListOptions(options)
  const rows = querySystemTeamRows(access, {
    limit: listOptions.pageSize + 1,
    offset: (listOptions.page - 1) * listOptions.pageSize
  }, listOptions).rows
  const pageRows = takePageRows(rows, listOptions.pageSize)
  const members = listSystemTeamMembersForTeamIds(pageRows.rows.map((row) => row.id), true)
  const items = pageRows.rows.map((row) => systemTeamSummaryFromRow(row, members.get(row.id) ?? []))
  return {
    items,
    total: pagedTotalUpperBound(listOptions.page, listOptions.pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: listOptions.page,
    pageSize: listOptions.pageSize
  }
}

export async function listSystemTeamsAsync(access?: AccessScope): Promise<SystemTeamSummary[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listSystemTeams(access)
  }
  const client = await getSystemTeamDatabaseClient()
  const rows = (await querySystemTeamRowsAsync(client, access, undefined, normalizeSystemTeamListOptions())).rows
  const members = await listSystemTeamMembersForTeamIdsAsync(client, rows.map((row) => row.id), true)
  return rows.map((row) => systemTeamSummaryFromRow(row, members.get(row.id) ?? []))
}

export async function listSystemTeamsPageAsync(access?: AccessScope, options: SystemTeamListOptions = {}): Promise<SystemTeamListResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listSystemTeamsPage(access, options)
  }
  const client = await getSystemTeamDatabaseClient()
  const listOptions = normalizeSystemTeamListOptions(options)
  const rows = (await querySystemTeamRowsAsync(client, access, {
    limit: listOptions.pageSize + 1,
    offset: (listOptions.page - 1) * listOptions.pageSize
  }, listOptions)).rows
  const pageRows = takePageRows(rows, listOptions.pageSize)
  const members = await listSystemTeamMembersForTeamIdsAsync(client, pageRows.rows.map((row) => row.id), true)
  const items = pageRows.rows.map((row) => systemTeamSummaryFromRow(row, members.get(row.id) ?? []))
  return {
    items,
    total: pagedTotalUpperBound(listOptions.page, listOptions.pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: listOptions.page,
    pageSize: listOptions.pageSize
  }
}

export function findSystemTeamSummary(id: string, access?: AccessScope): SystemTeamSummary | undefined {
  const scopedId = scopedSystemAccountId(access)
  const row = scopedId
    ? getBusinessDatabase()
      .prepare(`
        SELECT DISTINCT system_teams.*
        FROM system_teams
        INNER JOIN system_team_members ON system_team_members.team_id = system_teams.id
        WHERE system_teams.id = ?
          AND system_team_members.system_account_id = ?
          AND system_team_members.status = 'active'
        LIMIT 1
      `)
      .get(id, scopedId) as unknown as SystemTeamRow | undefined
    : getBusinessDatabase().prepare('SELECT * FROM system_teams WHERE id = ?').get(id) as unknown as SystemTeamRow | undefined
  if (!row) return undefined
  const members = listSystemTeamMembersForTeamIds([row.id], true)
  return systemTeamSummaryFromRow(row, members.get(row.id) ?? [])
}

export async function findSystemTeamSummaryAsync(id: string, access?: AccessScope): Promise<SystemTeamSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return findSystemTeamSummary(id, access)
  }
  const client = await getSystemTeamDatabaseClient()
  const row = await findSystemTeamRowForAccessAsync(client, id, access)
  if (!row) return undefined
  const members = await listSystemTeamMembersForTeamIdsAsync(client, [row.id], true)
  return systemTeamSummaryFromRow(row, members.get(row.id) ?? [])
}

export function createSystemTeam(input: Record<string, unknown>, access?: AccessScope): SystemTeamSummary {
  assertKnownInputKeys(input, systemTeamInputKeys, '系统团队')
  const name = normalizeSystemTeamName(input.name)
  const database = getBusinessDatabase()
  const now = nowIso()
  const id = newId('team')
  try {
    database
      .prepare('INSERT INTO system_teams (id, name, description, status, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)')
      .run(id, name, normalizeSystemTeamDescription(input.description), normalizeSystemTeamStatus(input.status, 'active'), currentSystemAccountId(access), now, now)
  } catch (error) {
    if (isDuplicateSystemTeamNameError(error)) {
      throw new Error('团队名称已存在')
    }
    throw error
  }
  const created = findSystemTeamSummary(id, access)
  if (!created) throw new Error('创建团队失败')
  invalidateSystemTeamLookupCache(id)
  return created
}

export async function createSystemTeamAsync(input: Record<string, unknown>, access?: AccessScope): Promise<SystemTeamSummary> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return createSystemTeam(input, access)
  }
  assertKnownInputKeys(input, systemTeamInputKeys, '系统团队')
  const name = normalizeSystemTeamName(input.name)
  const client = await getSystemTeamDatabaseClient()
  const now = nowIso()
  const id = newId('team')
  try {
    await client.transaction(async (tx) => {
      await tx.execute(`
        INSERT INTO ${systemTeamTable(tx, 'system_teams')} (
          id, name, description, status, created_by, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?)
      `, [
        id,
        name,
        normalizeSystemTeamDescription(input.description),
        normalizeSystemTeamStatus(input.status, 'active'),
        currentSystemAccountId(access),
        now,
        now
      ])
    })
  } catch (error) {
    if (isDuplicateSystemTeamNameError(error)) {
      throw new Error('团队名称已存在')
    }
    throw error
  }
  const created = await findSystemTeamSummaryAsync(id, access)
  if (!created) throw new Error('创建团队失败')
  invalidateSystemTeamLookupCache(id)
  return created
}

export function updateSystemTeam(id: string, input: Record<string, unknown>, access?: AccessScope): SystemTeamSummary | undefined {
  assertKnownInputKeys(input, systemTeamInputKeys, '系统团队')
  const database = getBusinessDatabase()
  const row = findSystemTeamRowForAccess(id, access)
  if (!row) return undefined
  const name = input.name === undefined ? row.name : normalizeSystemTeamName(input.name)
  const status = normalizeSystemTeamStatus(input.status, row.status)
  const now = nowIso()
  let authorizationChanged = false
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database
      .prepare('UPDATE system_teams SET name = ?, description = ?, status = ?, updated_at = ? WHERE id = ?')
      .run(name, input.description === undefined ? row.description : normalizeSystemTeamDescription(input.description), status, now, id)
    if (row.status !== 'disabled' && status === 'disabled') {
      revokeAllTeamSources(id, currentSystemAccountId(access), database, now, 'team_disabled')
      authorizationChanged = true
    }
    if (row.status === 'disabled' && status === 'active') {
      reactivateTeamGrantSources(id, access, database, now)
      authorizationChanged = true
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    if (isDuplicateSystemTeamNameError(error)) {
      throw new Error('团队名称已存在')
    }
    throw error
  }
  if (authorizationChanged) {
    refreshGroupAccountStatsAfterWrite('team_authorization_changed')
    invalidateAuthorizationRuntimeAfterBusinessWrite('team_authorization_changed')
  }
  invalidateSystemTeamLookupCache(id)
  invalidateSystemAccountTeamMembershipLookupCache()
  clearResourceAuthorizationLookupCaches()
  return findSystemTeamSummary(id, access)
}

export async function updateSystemTeamAsync(id: string, input: Record<string, unknown>, access?: AccessScope): Promise<SystemTeamSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return updateSystemTeam(id, input, access)
  }
  assertKnownInputKeys(input, systemTeamInputKeys, '系统团队')
  const client = await getSystemTeamDatabaseClient()
  let authorizationChanged = false
  await client.transaction(async (tx) => {
    const row = await findSystemTeamRowForAccessAsync(tx, id, access)
    if (!row) return
    const name = input.name === undefined ? row.name : normalizeSystemTeamName(input.name)
    const status = normalizeSystemTeamStatus(input.status, row.status)
    const now = nowIso()
    try {
      await tx.execute(`
        UPDATE ${systemTeamTable(tx, 'system_teams')}
        SET name = ?,
            description = ?,
            status = ?,
            updated_at = ?
        WHERE id = ?
      `, [
        name,
        input.description === undefined ? row.description : normalizeSystemTeamDescription(input.description),
        status,
        now,
        id
      ])
    } catch (error) {
      if (isDuplicateSystemTeamNameError(error)) {
        throw new Error('团队名称已存在')
      }
      throw error
    }
    if (row.status !== 'disabled' && status === 'disabled') {
      await revokeAllTeamSourcesAsync(id, currentSystemAccountId(access), tx, now, 'team_disabled')
      authorizationChanged = true
    }
    if (row.status === 'disabled' && status === 'active') {
      await reactivateTeamGrantSourcesAsync(id, access, tx, now)
      authorizationChanged = true
    }
  })
  if (authorizationChanged) {
    await refreshGroupAccountStatsAfterWriteAsync('team_authorization_changed')
    invalidateAuthorizationRuntimeAfterBusinessWrite('team_authorization_changed')
  }
  invalidateSystemTeamLookupCache(id)
  invalidateSystemAccountTeamMembershipLookupCache()
  clearResourceAuthorizationLookupCaches()
  return findSystemTeamSummaryAsync(id, access)
}

export function addSystemTeamMembers(teamId: string, input: Record<string, unknown>, access?: AccessScope): SystemTeamSummary | undefined {
  assertKnownInputKeys(input, systemTeamMembersInputKeys, '团队成员')
  const team = findSystemTeamRowForAccess(teamId, access, { activeOnly: true })
  if (!team) return undefined
  const systemAccountIds = normalizeSystemAccountIds(input.systemAccountIds)
  if (!systemAccountIds.length) throw new Error('请选择团队成员')
  if (systemAccountIds.length > maxSystemTeamMemberBatchSize) {
    throw new Error(`单次最多添加 ${maxSystemTeamMemberBatchSize} 个团队成员`)
  }
  const database = getBusinessDatabase()
  const existingActiveMemberRows = database.prepare(`
    SELECT system_account_id
    FROM system_team_members
    WHERE team_id = ?
      AND status = 'active'
    ORDER BY system_account_id ASC
    LIMIT ?
  `).all(teamId, maxSystemTeamMembersPerTeam + 1) as unknown as Array<{ system_account_id?: string }>
  if (existingActiveMemberRows.length > maxSystemTeamMembersPerTeam) {
    throw new Error(`授权团队最多支持 ${maxSystemTeamMembersPerTeam} 个成员，请先移除部分成员后再添加`)
  }
  const existingActiveMemberIds = new Set<string>()
  for (const memberRow of existingActiveMemberRows) {
    const systemAccountId = memberRow.system_account_id?.trim()
    if (systemAccountId) {
      existingActiveMemberIds.add(systemAccountId)
    }
  }
  const nextActiveMemberIds = systemAccountIds.filter((systemAccountId) => !existingActiveMemberIds.has(systemAccountId))
  if (existingActiveMemberIds.size + nextActiveMemberIds.length > maxSystemTeamMembersPerTeam) {
    throw new Error(`授权团队最多支持 ${maxSystemTeamMembersPerTeam} 个成员，请先移除部分成员后再添加`)
  }
  const now = nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const systemAccountId of systemAccountIds) {
      const account = findSystemAccountById(systemAccountId)
      if (!account || account.status !== 'active') throw new Error('团队成员不存在或已停用')
      const existing = database.prepare('SELECT * FROM system_team_members WHERE team_id = ? AND system_account_id = ? ORDER BY created_at DESC, id DESC LIMIT 1').get(teamId, systemAccountId) as unknown as SystemTeamMemberRow | undefined
      if (existing?.status === 'active') continue
      if (existing) {
        database.prepare("UPDATE system_team_members SET status = 'active', joined_at = ?, removed_at = NULL, updated_at = ? WHERE id = ?").run(now, now, existing.id)
      } else {
        database.prepare("INSERT INTO system_team_members (id, team_id, system_account_id, member_role, status, joined_at, removed_at, created_by, created_at, updated_at) VALUES (?, ?, ?, 'member', 'active', ?, NULL, ?, ?, ?)")
          .run(newId('teammem'), teamId, systemAccountId, now, currentSystemAccountId(access), now, now)
      }
      applyActiveTeamGrantsToMember(teamId, systemAccountId, access, database, now)
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshGroupAccountStatsAfterWrite('team_members_changed')
  invalidateAuthorizationRuntimeAfterBusinessWrite('team_members_changed')
  for (const systemAccountId of systemAccountIds) {
    invalidateSystemAccountTeamMembershipLookupCache(systemAccountId)
  }
  return findSystemTeamSummary(teamId, access)
}

export async function addSystemTeamMembersAsync(teamId: string, input: Record<string, unknown>, access?: AccessScope): Promise<SystemTeamSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return addSystemTeamMembers(teamId, input, access)
  }
  assertKnownInputKeys(input, systemTeamMembersInputKeys, '团队成员')
  const systemAccountIds = normalizeSystemAccountIds(input.systemAccountIds)
  if (!systemAccountIds.length) throw new Error('请选择团队成员')
  if (systemAccountIds.length > maxSystemTeamMemberBatchSize) {
    throw new Error(`单次最多添加 ${maxSystemTeamMemberBatchSize} 个团队成员`)
  }
  const client = await getSystemTeamDatabaseClient()
  let teamExists = false
  await client.transaction(async (tx) => {
    const team = await findSystemTeamRowForAccessAsync(tx, teamId, access, { activeOnly: true })
    if (!team) return
    teamExists = true
    await lockSystemTeamRowForUpdateAsync(tx, teamId)
    const existingActiveMemberRows = await tx.query<{ system_account_id?: string }>(`
      SELECT system_account_id
      FROM ${systemTeamTable(tx, 'system_team_members')}
      WHERE team_id = ?
        AND status = 'active'
      ORDER BY system_account_id ASC
      LIMIT ?
    `, [teamId, maxSystemTeamMembersPerTeam + 1])
    if (existingActiveMemberRows.length > maxSystemTeamMembersPerTeam) {
      throw new Error(`授权团队最多支持 ${maxSystemTeamMembersPerTeam} 个成员，请先移除部分成员后再添加`)
    }
    const existingActiveMemberIds = new Set<string>()
    for (const memberRow of existingActiveMemberRows) {
      const systemAccountId = memberRow.system_account_id?.trim()
      if (systemAccountId) {
        existingActiveMemberIds.add(systemAccountId)
      }
    }
    const nextActiveMemberIds = systemAccountIds.filter((systemAccountId) => !existingActiveMemberIds.has(systemAccountId))
    if (existingActiveMemberIds.size + nextActiveMemberIds.length > maxSystemTeamMembersPerTeam) {
      throw new Error(`授权团队最多支持 ${maxSystemTeamMembersPerTeam} 个成员，请先移除部分成员后再添加`)
    }
    const now = nowIso()
    for (const systemAccountId of systemAccountIds) {
      const account = await tx.one<{ id?: string; status?: string }>(`
        SELECT id, status
        FROM ${systemTeamTable(tx, 'system_accounts')}
        WHERE id = ?
        LIMIT 1
      `, [systemAccountId])
      if (!account || account.status !== 'active') throw new Error('团队成员不存在或已停用')
      const existing = await tx.one<SystemTeamMemberRow>(`
        SELECT *
        FROM ${systemTeamTable(tx, 'system_team_members')}
        WHERE team_id = ?
          AND system_account_id = ?
        ORDER BY created_at DESC, id DESC
        LIMIT 1
      `, [teamId, systemAccountId])
      if (existing?.status === 'active') continue
      if (existing) {
        await tx.execute(`
          UPDATE ${systemTeamTable(tx, 'system_team_members')}
          SET status = 'active',
              joined_at = ?,
              removed_at = NULL,
              updated_at = ?
          WHERE id = ?
        `, [now, now, existing.id])
      } else {
        await tx.execute(`
          INSERT INTO ${systemTeamTable(tx, 'system_team_members')} (
            id, team_id, system_account_id, member_role, status, joined_at, removed_at,
            created_by, created_at, updated_at
          ) VALUES (?, ?, ?, 'member', 'active', ?, NULL, ?, ?, ?)
        `, [newId('teammem'), teamId, systemAccountId, now, currentSystemAccountId(access), now, now])
      }
      await applyActiveTeamGrantsToMemberAsync(teamId, systemAccountId, access, tx, now)
    }
  })
  if (!teamExists) return undefined
  await refreshGroupAccountStatsAfterWriteAsync('team_members_changed')
  invalidateAuthorizationRuntimeAfterBusinessWrite('team_members_changed')
  for (const systemAccountId of systemAccountIds) {
    invalidateSystemAccountTeamMembershipLookupCache(systemAccountId)
  }
  return findSystemTeamSummaryAsync(teamId, access)
}

export function removeSystemTeamMember(teamId: string, memberId: string, access?: AccessScope): SystemTeamSummary | undefined {
  const database = getBusinessDatabase()
  const member = findActiveSystemTeamMemberForAccess(teamId, memberId, access)
  if (!member) return undefined
  const now = nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database.prepare("UPDATE system_team_members SET status = 'removed', removed_at = ?, updated_at = ? WHERE id = ?").run(now, now, memberId)
    revokeTeamSourcesForMember(teamId, member.system_account_id, currentSystemAccountId(access), database, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshGroupAccountStatsAfterWrite('team_members_changed')
  invalidateAuthorizationRuntimeAfterBusinessWrite('team_members_changed')
  invalidateSystemAccountTeamMembershipLookupCache(member.system_account_id)
  return findSystemTeamSummary(teamId, access)
}

export async function removeSystemTeamMemberAsync(teamId: string, memberId: string, access?: AccessScope): Promise<SystemTeamSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return removeSystemTeamMember(teamId, memberId, access)
  }
  const client = await getSystemTeamDatabaseClient()
  let removedSystemAccountId: string | undefined
  await client.transaction(async (tx) => {
    const member = await findActiveSystemTeamMemberForAccessAsync(tx, teamId, memberId, access)
    if (!member) return
    removedSystemAccountId = member.system_account_id
    const now = nowIso()
    await tx.execute(`
      UPDATE ${systemTeamTable(tx, 'system_team_members')}
      SET status = 'removed',
          removed_at = ?,
          updated_at = ?
      WHERE id = ?
    `, [now, now, memberId])
    await revokeTeamSourcesForMemberAsync(teamId, member.system_account_id, currentSystemAccountId(access), tx, now)
  })
  if (!removedSystemAccountId) return undefined
  await refreshGroupAccountStatsAfterWriteAsync('team_members_changed')
  invalidateAuthorizationRuntimeAfterBusinessWrite('team_members_changed')
  invalidateSystemAccountTeamMembershipLookupCache(removedSystemAccountId)
  return findSystemTeamSummaryAsync(teamId, access)
}

function findSystemTeamRowForAccess(id: string, access?: AccessScope, options: { activeOnly?: boolean } = {}): SystemTeamRow | undefined {
  const scopedId = scopedSystemAccountId(access)
  const activeClause = options.activeOnly ? " AND system_teams.status = 'active'" : ''
  if (scopedId) {
    return getBusinessDatabase().prepare(`
      SELECT DISTINCT system_teams.*
      FROM system_teams
      INNER JOIN system_team_members
        ON system_team_members.team_id = system_teams.id
      WHERE system_teams.id = ?
        AND system_team_members.system_account_id = ?
        AND system_team_members.status = 'active'
        ${activeClause}
      LIMIT 1
    `).get(id, scopedId) as unknown as SystemTeamRow | undefined
  }
  return getBusinessDatabase().prepare(`
    SELECT *
    FROM system_teams
    WHERE system_teams.id = ?${activeClause}
    LIMIT 1
  `).get(id) as unknown as SystemTeamRow | undefined
}

function findActiveSystemTeamMemberForAccess(teamId: string, memberId: string, access?: AccessScope): SystemTeamMemberRow | undefined {
  const scopedId = scopedSystemAccountId(access)
  const scopedClause = scopedId
    ? ` AND EXISTS (
        SELECT 1
        FROM system_team_members scoped_members
        WHERE scoped_members.team_id = system_teams.id
          AND scoped_members.system_account_id = ?
          AND scoped_members.status = 'active'
      )`
    : ''
  const params = scopedId ? [memberId, teamId, scopedId] : [memberId, teamId]
  return getBusinessDatabase().prepare(`
    SELECT system_team_members.*
    FROM system_team_members
    INNER JOIN system_teams
      ON system_teams.id = system_team_members.team_id
    WHERE system_team_members.id = ?
      AND system_team_members.team_id = ?
      AND system_team_members.status = 'active'
      ${scopedClause}
    LIMIT 1
  `).get(...params) as unknown as SystemTeamMemberRow | undefined
}

function querySystemTeamRows(access: AccessScope | undefined, pagination: { limit: number; offset: number } | undefined, options: Pick<NormalizedSystemTeamListOptions, 'keyword'>): { rows: SystemTeamRow[] } {
  const scopedId = scopedSystemAccountId(access)
  const clauses: string[] = []
  const params: string[] = []
  if (scopedId) {
    clauses.push(`EXISTS (
      SELECT 1
      FROM system_team_members
      WHERE system_team_members.team_id = system_teams.id
        AND system_team_members.system_account_id = ?
        AND system_team_members.status = 'active'
    )`)
    params.push(scopedId)
  }
  const keyword = options.keyword?.trim()
  if (keyword) {
    clauses.push('(system_teams.name >= ? AND system_teams.name < ?)')
    params.push(keyword, systemTeamTextPrefixUpperBound(keyword))
  }
  const whereClause = clauses.length ? ` WHERE ${clauses.join(' AND ')}` : ''
  const pageClause = pagination ? ' LIMIT ? OFFSET ?' : ''
  const pageParams = pagination ? [pagination.limit, pagination.offset] : []
  const rows = getBusinessDatabase()
    .prepare(`SELECT id, name, description, status, created_by, created_at, updated_at FROM system_teams${whereClause} ORDER BY status ASC, updated_at DESC, name ASC, id ASC${pageClause}`)
    .all(...params, ...pageParams) as unknown as SystemTeamRow[]
  return { rows }
}

async function querySystemTeamRowsAsync(client: DatabaseClient, access: AccessScope | undefined, pagination: { limit: number; offset: number } | undefined, options: Pick<NormalizedSystemTeamListOptions, 'keyword'>): Promise<{ rows: SystemTeamRow[] }> {
  const scopedId = scopedSystemAccountId(access)
  const clauses: string[] = []
  const params: unknown[] = []
  if (scopedId) {
    clauses.push(`EXISTS (
      SELECT 1
      FROM ${systemTeamTable(client, 'system_team_members')} scoped_members
      WHERE scoped_members.team_id = system_teams.id
        AND scoped_members.system_account_id = ?
        AND scoped_members.status = 'active'
    )`)
    params.push(scopedId)
  }
  const keyword = options.keyword?.trim()
  if (keyword) {
    if (client.driver === 'postgres') {
      clauses.push(`(
        system_teams.name COLLATE "C" >= ?
        AND system_teams.name COLLATE "C" < ?
        AND starts_with(system_teams.name, ?)
      )`)
      params.push(keyword, systemTeamTextPrefixUpperBound(keyword), keyword)
    } else {
      clauses.push('(system_teams.name >= ? AND system_teams.name < ?)')
      params.push(keyword, systemTeamTextPrefixUpperBound(keyword))
    }
  }
  const whereClause = clauses.length ? ` WHERE ${clauses.join(' AND ')}` : ''
  const pageClause = pagination ? ' LIMIT ? OFFSET ?' : ''
  const pageParams = pagination ? [pagination.limit, pagination.offset] : []
  const rows = await client.query<SystemTeamRow>(`
    SELECT id, name, description, status, created_by, created_at, updated_at
    FROM ${systemTeamTable(client, 'system_teams')} system_teams
    ${whereClause}
    ORDER BY status ASC, updated_at DESC, name ASC, id ASC
    ${pageClause}
  `, [...params, ...pageParams])
  return { rows }
}

async function findSystemTeamRowForAccessAsync(client: DatabaseClient, id: string, access?: AccessScope, options: { activeOnly?: boolean } = {}): Promise<SystemTeamRow | undefined> {
  const scopedId = scopedSystemAccountId(access)
  const activeClause = options.activeOnly ? " AND system_teams.status = 'active'" : ''
  if (scopedId) {
    return client.one<SystemTeamRow>(`
      SELECT DISTINCT system_teams.*
      FROM ${systemTeamTable(client, 'system_teams')} system_teams
      INNER JOIN ${systemTeamTable(client, 'system_team_members')} system_team_members
        ON system_team_members.team_id = system_teams.id
      WHERE system_teams.id = ?
        AND system_team_members.system_account_id = ?
        AND system_team_members.status = 'active'
        ${activeClause}
      LIMIT 1
    `, [id, scopedId])
  }
  return client.one<SystemTeamRow>(`
    SELECT *
    FROM ${systemTeamTable(client, 'system_teams')} system_teams
    WHERE system_teams.id = ?${activeClause}
    LIMIT 1
  `, [id])
}

async function lockSystemTeamRowForUpdateAsync(client: DatabaseClient, teamId: string): Promise<void> {
  if (client.driver !== 'postgres') return
  await client.one<{ id?: string }>(`
    SELECT id
    FROM ${systemTeamTable(client, 'system_teams')}
    WHERE id = ?
    FOR UPDATE
  `, [teamId])
}

async function findActiveSystemTeamMemberForAccessAsync(client: DatabaseClient, teamId: string, memberId: string, access?: AccessScope): Promise<SystemTeamMemberRow | undefined> {
  const scopedId = scopedSystemAccountId(access)
  const scopedClause = scopedId
    ? ` AND EXISTS (
        SELECT 1
        FROM ${systemTeamTable(client, 'system_team_members')} scoped_members
        WHERE scoped_members.team_id = system_teams.id
          AND scoped_members.system_account_id = ?
          AND scoped_members.status = 'active'
      )`
    : ''
  const params = scopedId ? [memberId, teamId, scopedId] : [memberId, teamId]
  return client.one<SystemTeamMemberRow>(`
    SELECT system_team_members.*
    FROM ${systemTeamTable(client, 'system_team_members')} system_team_members
    INNER JOIN ${systemTeamTable(client, 'system_teams')} system_teams
      ON system_teams.id = system_team_members.team_id
    WHERE system_team_members.id = ?
      AND system_team_members.team_id = ?
      AND system_team_members.status = 'active'
      ${scopedClause}
    LIMIT 1
  `, params)
}

function normalizeSystemTeamListOptions(options: SystemTeamListOptions = {}): NormalizedSystemTeamListOptions {
  const rawPage = options.page
  const rawPageSize = options.pageSize
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(maxSystemTeamListPageSize, Math.max(1, rawPageSize))
    : 20
  const page = normalizeListPage(rawPage, pageSize)
  return {
    page,
    pageSize,
    keyword: optionalString(options.keyword)
  }
}

function systemTeamSummaryFromRow(row: SystemTeamRow, members: SystemTeamMemberSummary[]): SystemTeamSummary {
  return { id: row.id, name: row.name, description: row.description ?? undefined, status: row.status, memberCount: members.length, activeMemberCount: members.filter((member) => member.status === 'active').length, members, createdBy: row.created_by, createdAt: row.created_at, updatedAt: row.updated_at }
}

function listSystemTeamMembersForTeamIds(teamIds: string[], activeOnly = false): Map<string, SystemTeamMemberSummary[]> {
  const ids = [...new Set(teamIds)].filter(Boolean)
  if (!ids.length) return new Map()
  const statusClause = activeOnly ? " AND system_team_members.status = 'active'" : ''
  const rows: Array<SystemTeamMemberRow & { display_name?: string; username?: string }> = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    const teamRows = database.prepare(`
      SELECT *
      FROM (
        SELECT ${systemTeamMemberSelectColumns('system_team_members')}, system_accounts.display_name, system_accounts.username,
          ROW_NUMBER() OVER (PARTITION BY system_team_members.team_id ORDER BY system_team_members.status ASC, system_team_members.joined_at ASC, system_team_members.id ASC) AS team_member_rank
        FROM system_team_members
        INNER JOIN system_accounts ON system_accounts.id = system_team_members.system_account_id
        WHERE system_team_members.team_id IN (${sqlPlaceholders(chunk.length)})${statusClause}
      )
      WHERE team_member_rank <= ?
    `).all(...chunk, maxSystemTeamMembersPerTeam) as unknown as Array<SystemTeamMemberRow & { display_name?: string; username?: string }>
    rows.push(...teamRows.sort(compareSystemTeamMembersForList))
  }
  const result = new Map<string, SystemTeamMemberSummary[]>()
  for (const row of rows) {
    const member: SystemTeamMemberSummary = { id: row.id, teamId: row.team_id, systemAccountId: row.system_account_id, systemAccountName: row.display_name, username: row.username, memberRole: 'member', status: row.status, joinedAt: row.joined_at, removedAt: row.removed_at ?? undefined, createdAt: row.created_at, updatedAt: row.updated_at }
    result.set(row.team_id, [...(result.get(row.team_id) ?? []), member])
  }
  return result
}

async function listSystemTeamMembersForTeamIdsAsync(client: DatabaseClient, teamIds: string[], activeOnly = false): Promise<Map<string, SystemTeamMemberSummary[]>> {
  const ids = [...new Set(teamIds)].filter(Boolean)
  if (!ids.length) return new Map()
  const statusClause = activeOnly ? " AND system_team_members.status = 'active'" : ''
  const rows: Array<SystemTeamMemberRow & { display_name?: string; username?: string }> = []
  for (const chunk of chunkValues(ids, 900)) {
    const teamRows = await client.query<SystemTeamMemberRow & { display_name?: string; username?: string }>(`
      SELECT *
      FROM (
        SELECT ${systemTeamMemberSelectColumns('system_team_members')}, system_accounts.display_name, system_accounts.username,
          ROW_NUMBER() OVER (PARTITION BY system_team_members.team_id ORDER BY system_team_members.status ASC, system_team_members.joined_at ASC, system_team_members.id ASC) AS team_member_rank
        FROM ${systemTeamTable(client, 'system_team_members')} system_team_members
        INNER JOIN ${systemTeamTable(client, 'system_accounts')} system_accounts
          ON system_accounts.id = system_team_members.system_account_id
        WHERE system_team_members.team_id IN (${sqlPlaceholders(chunk.length)})${statusClause}
      ) ranked_team_members
      WHERE team_member_rank <= ?
    `, [...chunk, maxSystemTeamMembersPerTeam])
    rows.push(...teamRows.sort(compareSystemTeamMembersForList))
  }
  const result = new Map<string, SystemTeamMemberSummary[]>()
  for (const row of rows) {
    const member: SystemTeamMemberSummary = { id: row.id, teamId: row.team_id, systemAccountId: row.system_account_id, systemAccountName: row.display_name, username: row.username, memberRole: 'member', status: row.status, joinedAt: row.joined_at, removedAt: row.removed_at ?? undefined, createdAt: row.created_at, updatedAt: row.updated_at }
    result.set(row.team_id, [...(result.get(row.team_id) ?? []), member])
  }
  return result
}

function compareSystemTeamMembersForList(left: SystemTeamMemberRow, right: SystemTeamMemberRow): number {
  const team = left.team_id.localeCompare(right.team_id)
  if (team !== 0) return team
  const status = left.status.localeCompare(right.status)
  if (status !== 0) return status
  const joinedAt = left.joined_at.localeCompare(right.joined_at)
  return joinedAt !== 0 ? joinedAt : left.id.localeCompare(right.id)
}

function systemTeamMemberSelectColumns(alias: string): string {
  return [
    'id',
    'team_id',
    'system_account_id',
    'member_role',
    'status',
    'joined_at',
    'removed_at',
    'created_by',
    'created_at',
    'updated_at'
  ].map((column) => `${alias}.${column}`).join(', ')
}

function normalizeSystemTeamName(value: unknown): string {
  if (typeof value !== 'string') {
    throw new Error('团队名称不能为空')
  }
  const name = value.trim()
  if (!name) {
    throw new Error('团队名称不能为空')
  }
  return name
}

function normalizeSystemTeamDescription(value: unknown): string | null {
  if (value === undefined || value === null) {
    return null
  }
  if (typeof value !== 'string') {
    throw new Error('团队说明必须是字符串')
  }
  const description = value.trim()
  return description || null
}

function normalizeSystemTeamStatus(value: unknown, fallback: string): 'active' | 'disabled' {
  if (value === undefined) {
    if (fallback === 'active' || fallback === 'disabled') return fallback
    throw new Error('团队状态无效')
  }
  if (value === 'active' || value === 'disabled') {
    return value
  }
  throw new Error('团队状态无效')
}

function normalizeSystemAccountIds(value: unknown): string[] {
  if (!Array.isArray(value)) {
    throw new Error('团队成员必须是系统账户 ID 数组')
  }
  const ids: string[] = []
  const seen = new Set<string>()
  for (const item of value) {
    if (typeof item !== 'string') {
      throw new Error('团队成员必须是系统账户 ID 数组')
    }
    const id = item.trim()
    if (!id) {
      throw new Error('团队成员 ID 不能为空')
    }
    if (seen.has(id)) {
      throw new Error('团队成员不能重复')
    }
    seen.add(id)
    ids.push(id)
  }
  return ids
}

function assertKnownInputKeys(input: Record<string, unknown>, allowedKeys: ReadonlySet<string>, label: string): void {
  const unknownKeys = Object.keys(input).filter((key) => !allowedKeys.has(key))
  if (unknownKeys.length) {
    throw new Error(`${label}包含未知字段：${unknownKeys.join('、')}`)
  }
}

function refreshGroupAccountStatsAfterWrite(reason: string): void {
  if (runtimeConfig.databaseDriver === 'postgres') return
  markAllGroupAccountStatsDirty(reason)
}

async function refreshGroupAccountStatsAfterWriteAsync(reason: string): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    refreshGroupAccountStatsAfterWrite(reason)
    return
  }
  await markAllGroupAccountStatsDirtyAsync(reason)
}

function invalidateAuthorizationRuntimeAfterBusinessWrite(reason: string): void {
  notifyGatewayRuntimeCacheInvalidation(reason)
  notifyAuthorizationQuotaCacheInvalidation(reason)
}

function systemTeamTextPrefixUpperBound(value: string): string {
  const chars = [...value]
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const codePoint = chars[index].codePointAt(0)
    if (codePoint === undefined || codePoint >= 0x10ffff) continue
    return `${chars.slice(0, index).join('')}${String.fromCodePoint(codePoint + 1)}`
  }
  return `${value}\u{10ffff}`
}

function isDuplicateSystemTeamNameError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  return error.message.includes('idx_system_teams_name_unique')
    || error.message.includes('idx_system_teams_name_unique_lower')
}

async function getSystemTeamDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function systemTeamTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}
