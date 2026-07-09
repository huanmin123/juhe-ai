import type { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, getStatsDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

export const GROUP_ACCOUNT_STATS_DIRTY_ALL = '__all__'

const GROUP_ACCOUNT_STATS_DIRTY_ALL_CURSOR_PREFIX = 'all_cursor:'
const groupAccountStatsFullRefreshBatchLimit = 1000

export interface GroupAccountStatsDirtyRow {
  groupId: string
  reason: string | null
  updatedAt: string
}

export interface GroupAccountStatsDirtyStateWriter {
  markAllDirty(reason: string): Promise<void>
  deleteRows(rows: GroupAccountStatsDirtyRow[]): Promise<void>
  updateAllCursor(cursorGroupId: string): Promise<void>
}

interface GroupAccountStatsAccumulator {
  groupId: string
  systemAccountId: string
  total: number
  available: number
  active: number
  disabled: number
  error: number
  rateLimited: number
  concurrencyLimit: number
}

export function markGroupAccountStatsDirty(groupIds: Array<string | null | undefined> | string | null | undefined, reason = 'write'): void {
  const ids = uniqueGroupAccountStatsIds(Array.isArray(groupIds) ? groupIds : [groupIds])
  if (!ids.length) return
  const database = getBusinessDatabase()
  const updatedAt = nowIso()
  const insert = database.prepare(`
    INSERT INTO group_account_stats_dirty (group_id, reason, updated_at)
    VALUES (?, ?, ?)
    ON CONFLICT(group_id) DO UPDATE SET
      reason = excluded.reason,
      updated_at = excluded.updated_at
  `)
  for (const id of ids) {
    insert.run(id, reason, updatedAt)
  }
}

export function markAllGroupAccountStatsDirty(reason = 'write'): void {
  markGroupAccountStatsDirty(GROUP_ACCOUNT_STATS_DIRTY_ALL, reason)
}

export function markGroupAccountStatsDirtyByAccountIds(accountIds: Array<string | null | undefined>, reason = 'account_write'): void {
  const ids = uniqueGroupAccountStatsIds(accountIds)
  if (!ids.length) return
  const groupIds: string[] = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    groupIds.push(...(database.prepare(`
      SELECT DISTINCT group_id
      FROM group_accounts
      WHERE account_id IN (${sqlPlaceholders(chunk.length)})
    `).all(...chunk) as unknown as Array<{ group_id: string }>).map((row) => row.group_id))
  }
  markGroupAccountStatsDirty(groupIds, reason)
}

export function refreshDirtyGroupAccountStatsCache(limit = 1000): number {
  const businessDatabase = getBusinessDatabase()
  const statsDatabase = getStatsDatabase()
  const normalizedLimit = Math.max(1, Math.min(groupAccountStatsFullRefreshBatchLimit, Math.trunc(limit)))
  const allDirtyRows = loadAllGroupAccountStatsDirtyRows(businessDatabase)
  if (allDirtyRows.length > 0) {
    return refreshAllDirtyGroupAccountStatsCacheBatch(businessDatabase, allDirtyRows[0], normalizedLimit)
  }

  const rows = loadGroupAccountStatsDirtyRows(businessDatabase, normalizedLimit)
  if (!rows.length) {
    const hasStats = statsDatabase.prepare('SELECT 1 FROM group_account_stats LIMIT 1').get()
    if (!hasStats) {
      markAllGroupAccountStatsDirty('initial_cache_build')
      const initialAllDirtyRows = loadAllGroupAccountStatsDirtyRows(businessDatabase)
      return initialAllDirtyRows[0]
        ? refreshAllDirtyGroupAccountStatsCacheBatch(businessDatabase, initialAllDirtyRows[0], normalizedLimit)
        : 0
    }
    return 0
  }

  refreshGroupAccountStatsCache(rows.map((row) => row.groupId))
  deleteGroupAccountStatsDirtyRows(businessDatabase, rows)
  return rows.length
}

export async function refreshDirtyGroupAccountStatsCacheWithWriter(
  writer: GroupAccountStatsDirtyStateWriter,
  limit = 1000
): Promise<number> {
  if (runtimeConfig.databaseDriver !== 'sqlite') {
    throw new Error('refreshDirtyGroupAccountStatsCacheWithWriter 仅支持 SQLite 本地刷新；PostgreSQL 模式必须使用 refreshDirtyGroupAccountStatsCacheAsync')
  }
  const businessDatabase = getBusinessDatabase()
  const statsDatabase = getStatsDatabase()
  const normalizedLimit = Math.max(1, Math.min(groupAccountStatsFullRefreshBatchLimit, Math.trunc(limit)))
  const allDirtyRows = loadAllGroupAccountStatsDirtyRows(businessDatabase)
  if (allDirtyRows.length > 0) {
    return await refreshAllDirtyGroupAccountStatsCacheBatchWithWriter(businessDatabase, allDirtyRows[0], normalizedLimit, writer)
  }

  const rows = loadGroupAccountStatsDirtyRows(businessDatabase, normalizedLimit)
  if (!rows.length) {
    const hasStats = statsDatabase.prepare('SELECT 1 FROM group_account_stats LIMIT 1').get()
    if (!hasStats) {
      await writer.markAllDirty('initial_cache_build')
      const initialAllDirtyRows = loadAllGroupAccountStatsDirtyRows(businessDatabase)
      return initialAllDirtyRows[0]
        ? await refreshAllDirtyGroupAccountStatsCacheBatchWithWriter(businessDatabase, initialAllDirtyRows[0], normalizedLimit, writer)
        : 0
    }
    return 0
  }

  refreshGroupAccountStatsCache(rows.map((row) => row.groupId))
  await writer.deleteRows(rows)
  return rows.length
}

export async function refreshDirtyGroupAccountStatsCacheAsync(limit = 1000): Promise<number> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return refreshDirtyGroupAccountStatsCache(limit)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const normalizedLimit = Math.max(1, Math.min(groupAccountStatsFullRefreshBatchLimit, Math.trunc(limit)))
  const allDirtyRows = await loadAllGroupAccountStatsDirtyRowsAsync(client)
  if (allDirtyRows.length > 0) {
    return await refreshAllDirtyGroupAccountStatsCacheBatchAsync(client, allDirtyRows[0], normalizedLimit)
  }

  const rows = await loadGroupAccountStatsDirtyRowsAsync(client, normalizedLimit)
  if (!rows.length) {
    const hasStats = await client.one<{ exists?: number }>(`
      SELECT 1 AS "exists"
      FROM ${groupAccountStatsCacheTable(client, 'juhe_stats', 'group_account_stats')}
      LIMIT 1
    `)
    if (!hasStats) {
      await markAllGroupAccountStatsDirtyAsync('initial_cache_build')
      const initialAllDirtyRows = await loadAllGroupAccountStatsDirtyRowsAsync(client)
      return initialAllDirtyRows[0]
        ? await refreshAllDirtyGroupAccountStatsCacheBatchAsync(client, initialAllDirtyRows[0], normalizedLimit)
        : 0
    }
    return 0
  }

  await refreshGroupAccountStatsCacheAsync(rows.map((row) => row.groupId), client)
  await deleteGroupAccountStatsDirtyRowsAsync(rows, client)
  return rows.length
}

async function refreshAllDirtyGroupAccountStatsCacheBatchAsync(
  client: DatabaseClient,
  dirtyRow: GroupAccountStatsDirtyRow,
  limit: number
): Promise<number> {
  const cursorGroupId = groupAccountStatsAllCursor(dirtyRow.reason)
  const groups = await loadGroupAccountStatsGroupsPageAsync(client, cursorGroupId, limit)
  if (groups.length === 0) {
    await deleteGroupAccountStatsDirtyRowsAsync([dirtyRow], client)
    return 1
  }
  await refreshGroupAccountStatsCacheAsync(groups.map((group) => group.id), client)
  if (groups.length < limit) {
    await deleteGroupAccountStatsDirtyRowsAsync([dirtyRow], client)
    return 1
  }
  await updateGroupAccountStatsAllCursorAsync(groups[groups.length - 1].id, client)
  return 1
}

export async function markAllGroupAccountStatsDirtyAsync(reason = 'write', client?: DatabaseClient): Promise<void> {
  await markGroupAccountStatsDirtyAsync(GROUP_ACCOUNT_STATS_DIRTY_ALL, reason, client)
}

export async function markGroupAccountStatsDirtyAsync(
  groupIds: Array<string | null | undefined> | string | null | undefined,
  reason = 'write',
  client?: DatabaseClient
): Promise<void> {
  const ids = uniqueGroupAccountStatsIds(Array.isArray(groupIds) ? groupIds : [groupIds])
  if (!ids.length) return
  const databaseClient = client ?? createPostgresDatabaseClient(await getPostgresPool())
  const updatedAt = nowIso()
  for (const id of ids) {
    await databaseClient.execute(`
      INSERT INTO ${groupAccountStatsCacheTable(databaseClient, 'juhe_business', 'group_account_stats_dirty')} (group_id, reason, updated_at)
      VALUES (?, ?, ?)
      ON CONFLICT(group_id) DO UPDATE SET
        reason = excluded.reason,
        updated_at = excluded.updated_at
    `, [id, reason, updatedAt])
  }
}

export async function markGroupAccountStatsDirtyByAccountIdsAsync(
  accountIds: Array<string | null | undefined>,
  reason = 'account_write',
  client?: DatabaseClient
): Promise<void> {
  const ids = uniqueGroupAccountStatsIds(accountIds)
  if (!ids.length) return
  const databaseClient = client ?? createPostgresDatabaseClient(await getPostgresPool())
  const groupIds: string[] = []
  for (const chunk of chunkValues(ids, 900)) {
    const rows = await databaseClient.query<{ group_id: string }>(`
      SELECT DISTINCT group_id
      FROM ${groupAccountStatsCacheTable(databaseClient, 'juhe_business', 'group_accounts')}
      WHERE account_id IN (${chunk.map(() => '?').join(', ')})
    `, chunk)
    groupIds.push(...rows.map((row) => row.group_id))
  }
  await markGroupAccountStatsDirtyAsync(groupIds, reason, databaseClient)
}

async function loadAllGroupAccountStatsDirtyRowsAsync(client: DatabaseClient): Promise<GroupAccountStatsDirtyRow[]> {
  const row = await client.one<{ group_id: string; reason: string | null; updated_at: string }>(`
    SELECT group_id, reason, updated_at
    FROM ${groupAccountStatsCacheTable(client, 'juhe_business', 'group_account_stats_dirty')}
    WHERE group_id = ?
    LIMIT 1
  `, [GROUP_ACCOUNT_STATS_DIRTY_ALL])
  return row ? [mapGroupAccountStatsDirtyRow(row)] : []
}

async function loadGroupAccountStatsDirtyRowsAsync(
  client: DatabaseClient,
  limit: number
): Promise<GroupAccountStatsDirtyRow[]> {
  const normalizedLimit = Math.max(1, Math.trunc(limit))
  const rows = await client.query<{ group_id: string; reason: string | null; updated_at: string }>(`
    SELECT group_id, reason, updated_at
    FROM ${groupAccountStatsCacheTable(client, 'juhe_business', 'group_account_stats_dirty')}
    WHERE group_id <> ?
    ORDER BY updated_at ASC, group_id ASC
    LIMIT ?
  `, [GROUP_ACCOUNT_STATS_DIRTY_ALL, normalizedLimit])
  return rows
    .map((row) => mapGroupAccountStatsDirtyRow(row))
    .sort((left, right) => left.updatedAt.localeCompare(right.updatedAt) || left.groupId.localeCompare(right.groupId))
    .slice(0, normalizedLimit)
}

export async function deleteGroupAccountStatsDirtyRowsAsync(
  rows: GroupAccountStatsDirtyRow[],
  client?: DatabaseClient
): Promise<void> {
  const databaseClient = client ?? createPostgresDatabaseClient(await getPostgresPool())
  for (const row of rows) {
    await databaseClient.execute(`
      DELETE FROM ${groupAccountStatsCacheTable(databaseClient, 'juhe_business', 'group_account_stats_dirty')}
      WHERE group_id = ?
        AND updated_at = ?
    `, [row.groupId, row.updatedAt])
  }
}

export async function updateGroupAccountStatsAllCursorAsync(
  cursorGroupId: string,
  client?: DatabaseClient
): Promise<void> {
  const databaseClient = client ?? createPostgresDatabaseClient(await getPostgresPool())
  await databaseClient.execute(`
    UPDATE ${groupAccountStatsCacheTable(databaseClient, 'juhe_business', 'group_account_stats_dirty')}
    SET reason = ?,
        updated_at = ?
    WHERE group_id = ?
  `, [`${GROUP_ACCOUNT_STATS_DIRTY_ALL_CURSOR_PREFIX}${cursorGroupId}`, nowIso(), GROUP_ACCOUNT_STATS_DIRTY_ALL])
}

async function loadGroupAccountStatsGroupsPageAsync(
  client: DatabaseClient,
  cursorGroupId: string | undefined,
  limit: number
): Promise<Array<{ id: string; system_account_id: string }>> {
  const cursorClause = cursorGroupId ? 'WHERE id > ?' : ''
  const params = cursorGroupId ? [cursorGroupId, Math.max(1, Math.trunc(limit))] : [Math.max(1, Math.trunc(limit))]
  return await client.query<{ id: string; system_account_id: string }>(`
    SELECT id, system_account_id
    FROM ${groupAccountStatsCacheTable(client, 'juhe_business', 'groups')}
    ${cursorClause}
    ORDER BY id ASC
    LIMIT ?
  `, params)
}

async function refreshGroupAccountStatsCacheAsync(groupIds: Array<string | null | undefined>, client?: DatabaseClient): Promise<void> {
  const databaseClient = client ?? createPostgresDatabaseClient(await getPostgresPool())
  const targetGroupIds = uniqueGroupAccountStatsIds(groupIds)
  if (!targetGroupIds.length) return
  const updatedAt = nowIso()
  for (const chunk of chunkValues(targetGroupIds, 500)) {
    const placeholders = sqlPlaceholders(chunk.length)
    const rows = await clientGroupAccountStatsRows(databaseClient, placeholders, chunk, updatedAt)
    await databaseClient.execute(`
      DELETE FROM ${groupAccountStatsCacheTable(databaseClient, 'juhe_stats', 'group_account_stats')}
      WHERE group_id IN (${placeholders})
    `, chunk)
    for (const row of rows) {
      await databaseClient.execute(`
        INSERT INTO ${groupAccountStatsCacheTable(databaseClient, 'juhe_stats', 'group_account_stats')} (
          system_account_id, group_id, total, available, active, disabled, error,
          rate_limited, current_concurrency, concurrency_limit, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)
      `, [
        row.system_account_id,
        row.group_id,
        Number(row.total ?? 0),
        Number(row.available ?? 0),
        Number(row.active ?? 0),
        Number(row.disabled ?? 0),
        Number(row.error ?? 0),
        Number(row.rate_limited ?? 0),
        Number(row.concurrency_limit ?? 0),
        updatedAt
      ])
    }
  }
}

async function clientGroupAccountStatsRows(
  client: DatabaseClient,
  placeholders: string,
  groupIds: string[],
  updatedAt: string
): Promise<Array<{
  group_id: string
  system_account_id: string
  total: number
  available: number
  active: number
  disabled: number
  error: number
  rate_limited: number
  concurrency_limit: number
}>> {
  return await client.query<{
    group_id: string
    system_account_id: string
    total: number
    available: number
    active: number
    disabled: number
    error: number
    rate_limited: number
    concurrency_limit: number
  }>(`
    SELECT
      groups.id AS group_id,
      groups.system_account_id,
      COALESCE(COUNT(accounts.id) FILTER (WHERE ${groupAccountStatsAuthorizedPredicate(updatedAt)}), 0) AS total,
      COALESCE(COUNT(accounts.id) FILTER (WHERE ${groupAccountStatsAuthorizedPredicate(updatedAt)} AND accounts.status = 'active' AND accounts.schedulable = 1 AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?)), 0) AS available,
      COALESCE(COUNT(accounts.id) FILTER (WHERE ${groupAccountStatsAuthorizedPredicate(updatedAt)} AND accounts.status = 'active'), 0) AS active,
      COALESCE(COUNT(accounts.id) FILTER (WHERE ${groupAccountStatsAuthorizedPredicate(updatedAt)} AND accounts.status = 'disabled'), 0) AS disabled,
      COALESCE(COUNT(accounts.id) FILTER (WHERE ${groupAccountStatsAuthorizedPredicate(updatedAt)} AND accounts.status <> 'active' AND accounts.status <> 'disabled'), 0) AS error,
      COALESCE(COUNT(accounts.id) FILTER (WHERE ${groupAccountStatsAuthorizedPredicate(updatedAt)} AND accounts.status = 'rate_limited'), 0) AS rate_limited,
      COALESCE(SUM(accounts.concurrency_limit) FILTER (WHERE ${groupAccountStatsAuthorizedPredicate(updatedAt)}), 0) AS concurrency_limit
    FROM ${groupAccountStatsCacheTable(client, 'juhe_business', 'groups')} groups
    LEFT JOIN ${groupAccountStatsCacheTable(client, 'juhe_business', 'group_accounts')} group_accounts
      ON group_accounts.group_id = groups.id
      AND group_accounts.enabled = 1
    LEFT JOIN ${groupAccountStatsCacheTable(client, 'juhe_business', 'accounts')} accounts
      ON accounts.id = group_accounts.account_id
      AND accounts.deleted_at IS NULL
    LEFT JOIN ${groupAccountStatsCacheTable(client, 'juhe_business', 'resource_authorizations')} resource_authorization_rows
      ON resource_authorization_rows.id = group_accounts.account_authorization_id
    WHERE groups.id IN (${placeholders})
    GROUP BY groups.id, groups.system_account_id
  `, [
    updatedAt,
    ...groupAccountStatsAuthorizedPredicateParams(updatedAt),
    ...groupAccountStatsAuthorizedPredicateParams(updatedAt),
    ...groupAccountStatsAuthorizedPredicateParams(updatedAt),
    ...groupAccountStatsAuthorizedPredicateParams(updatedAt),
    ...groupAccountStatsAuthorizedPredicateParams(updatedAt),
    ...groupAccountStatsAuthorizedPredicateParams(updatedAt),
    ...groupAccountStatsAuthorizedPredicateParams(updatedAt),
    ...groupIds
  ])
}

function groupAccountStatsAuthorizedPredicate(_updatedAt: string): string {
  return `accounts.id IS NOT NULL
    AND (
      (
        group_accounts.account_authorization_id IS NOT NULL
        AND resource_authorization_rows.status = 'active'
        AND (resource_authorization_rows.expires_at IS NULL OR resource_authorization_rows.expires_at > ?)
      )
      OR (
        group_accounts.account_authorization_id IS NULL
        AND accounts.system_account_id = groups.system_account_id
      )
    )`
}

function groupAccountStatsAuthorizedPredicateParams(updatedAt: string): string[] {
  return [updatedAt]
}

function groupAccountStatsCacheTable(client: DatabaseClient, schema: 'juhe_business' | 'juhe_stats', tableName: string): string {
  return client.dialect.qualifyTable(schema, tableName)
}

export function refreshGroupAccountStatsCache(groupIds?: Array<string | null | undefined>): void {
  const database = getStatsDatabase()
  const businessDatabase = getBusinessDatabase()
  const updatedAt = nowIso()
  const targetGroupIds = groupIds === undefined ? undefined : uniqueGroupAccountStatsIds(groupIds)
  if (targetGroupIds && !targetGroupIds.length) return
  const groups = loadGroupAccountStatsGroups(businessDatabase, targetGroupIds)
  const refreshGroupIds = targetGroupIds ?? groups.map((group) => group.id)
  const groupAccountRows = loadGroupAccountStatsRows(businessDatabase, refreshGroupIds)
  const statsByGroup = new Map<string, GroupAccountStatsAccumulator>()
  for (const group of groups) {
    statsByGroup.set(group.id, emptyGroupAccountStatsAccumulator(group.id, group.system_account_id))
  }
  for (const row of groupAccountRows) {
    const stats = statsByGroup.get(row.group_id) ?? emptyGroupAccountStatsAccumulator(row.group_id, row.group_system_account_id)
    statsByGroup.set(row.group_id, stats)
    if (!row.account_id || !row.account_system_account_id) continue
    const authorizationActive = row.authorization_status === 'active' && (!row.authorization_expires_at || row.authorization_expires_at > updatedAt)
    const authorized = row.account_authorization_id
      ? authorizationActive
      : row.account_system_account_id === row.group_system_account_id
    if (!authorized) continue
    stats.total += 1
    stats.concurrencyLimit += Number(row.concurrency_limit ?? 0)
    if (row.status === 'active') {
      stats.active += 1
      if (row.schedulable === 1 && (!row.cooldown_until || row.cooldown_until <= updatedAt)) {
        stats.available += 1
      }
    } else if (row.status === 'disabled') {
      stats.disabled += 1
    } else {
      stats.error += 1
    }
    if (row.status === 'rate_limited') {
      stats.rateLimited += 1
    }
  }
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    deleteGroupAccountStatsRows(database, refreshGroupIds)
    const insert = database.prepare(`
      INSERT INTO group_account_stats (
        system_account_id, group_id, total, available, active, disabled, error,
        rate_limited, current_concurrency, concurrency_limit, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)
    `)
    for (const stats of statsByGroup.values()) {
      insert.run(
        stats.systemAccountId,
        stats.groupId,
        stats.total,
        stats.available,
        stats.active,
        stats.disabled,
        stats.error,
        stats.rateLimited,
        stats.concurrencyLimit,
        updatedAt
      )
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function loadAllGroupAccountStatsDirtyRows(businessDatabase: DatabaseSync): GroupAccountStatsDirtyRow[] {
  const businessRow = businessDatabase
    .prepare('SELECT group_id, reason, updated_at FROM group_account_stats_dirty WHERE group_id = ? LIMIT 1')
    .get(GROUP_ACCOUNT_STATS_DIRTY_ALL) as unknown as { group_id: string; reason: string | null; updated_at: string } | undefined
  return businessRow ? [mapGroupAccountStatsDirtyRow(businessRow)] : []
}

function refreshAllDirtyGroupAccountStatsCacheBatch(
  businessDatabase: DatabaseSync,
  dirtyRow: GroupAccountStatsDirtyRow,
  limit: number
): number {
  const cursorGroupId = groupAccountStatsAllCursor(dirtyRow.reason)
  const groups = loadGroupAccountStatsGroupsPage(businessDatabase, cursorGroupId, limit)
  if (groups.length === 0) {
    deleteGroupAccountStatsDirtyRows(businessDatabase, [dirtyRow])
    return 1
  }
  refreshGroupAccountStatsCache(groups.map((group) => group.id))
  if (groups.length < limit) {
    deleteGroupAccountStatsDirtyRows(businessDatabase, [dirtyRow])
    return 1
  }
  updateGroupAccountStatsAllCursor(businessDatabase, groups[groups.length - 1].id)
  return 1
}

async function refreshAllDirtyGroupAccountStatsCacheBatchWithWriter(
  businessDatabase: DatabaseSync,
  dirtyRow: GroupAccountStatsDirtyRow,
  limit: number,
  writer: GroupAccountStatsDirtyStateWriter
): Promise<number> {
  const cursorGroupId = groupAccountStatsAllCursor(dirtyRow.reason)
  const groups = loadGroupAccountStatsGroupsPage(businessDatabase, cursorGroupId, limit)
  if (groups.length === 0) {
    await writer.deleteRows([dirtyRow])
    return 1
  }
  refreshGroupAccountStatsCache(groups.map((group) => group.id))
  if (groups.length < limit) {
    await writer.deleteRows([dirtyRow])
    return 1
  }
  await writer.updateAllCursor(groups[groups.length - 1].id)
  return 1
}

function groupAccountStatsAllCursor(reason: string | null | undefined): string | undefined {
  return reason?.startsWith(GROUP_ACCOUNT_STATS_DIRTY_ALL_CURSOR_PREFIX)
    ? reason.slice(GROUP_ACCOUNT_STATS_DIRTY_ALL_CURSOR_PREFIX.length)
    : undefined
}

export function updateGroupAccountStatsAllCursor(businessDatabase: DatabaseSync, cursorGroupId: string): void {
  businessDatabase
    .prepare(`
      UPDATE group_account_stats_dirty
      SET reason = ?,
          updated_at = ?
      WHERE group_id = ?
    `)
    .run(`${GROUP_ACCOUNT_STATS_DIRTY_ALL_CURSOR_PREFIX}${cursorGroupId}`, nowIso(), GROUP_ACCOUNT_STATS_DIRTY_ALL)
}

export function updateGroupAccountStatsAllCursorLocal(cursorGroupId: string): void {
  updateGroupAccountStatsAllCursor(getBusinessDatabase(), cursorGroupId)
}

function loadGroupAccountStatsDirtyRows(
  businessDatabase: DatabaseSync,
  limit: number
): GroupAccountStatsDirtyRow[] {
  const normalizedLimit = Math.max(1, Math.trunc(limit))
  const businessRows = businessDatabase
    .prepare('SELECT group_id, reason, updated_at FROM group_account_stats_dirty WHERE group_id <> ? ORDER BY updated_at ASC, group_id ASC LIMIT ?')
    .all(GROUP_ACCOUNT_STATS_DIRTY_ALL, normalizedLimit) as unknown as Array<{ group_id: string; reason: string | null; updated_at: string }>
  return businessRows
    .map((row) => mapGroupAccountStatsDirtyRow(row))
    .sort((left, right) => left.updatedAt.localeCompare(right.updatedAt) || left.groupId.localeCompare(right.groupId))
    .slice(0, normalizedLimit)
}

function mapGroupAccountStatsDirtyRow(row: { group_id: string; reason?: string | null; updated_at: string }): GroupAccountStatsDirtyRow {
  return {
    groupId: row.group_id,
    reason: row.reason ?? null,
    updatedAt: row.updated_at
  }
}

export function deleteGroupAccountStatsDirtyRows(
  businessDatabase: DatabaseSync,
  rows: GroupAccountStatsDirtyRow[]
): void {
  const deleteBusinessDirty = businessDatabase.prepare('DELETE FROM group_account_stats_dirty WHERE group_id = ? AND updated_at = ?')
  for (const row of rows) {
    deleteBusinessDirty.run(row.groupId, row.updatedAt)
  }
}

export function deleteGroupAccountStatsDirtyRowsLocal(rows: GroupAccountStatsDirtyRow[]): void {
  deleteGroupAccountStatsDirtyRows(getBusinessDatabase(), rows)
}

function loadGroupAccountStatsGroups(
  database: DatabaseSync,
  groupIds?: string[]
): Array<{ id: string; system_account_id: string }> {
  if (!groupIds) {
    return loadGroupAccountStatsGroupsPage(database, undefined, groupAccountStatsFullRefreshBatchLimit)
  }
  const rows: Array<{ id: string; system_account_id: string }> = []
  for (const chunk of chunkValues(groupIds, 900)) {
    rows.push(...database.prepare(`
      SELECT id, system_account_id
      FROM groups
      WHERE id IN (${sqlPlaceholders(chunk.length)})
    `).all(...chunk) as unknown as Array<{ id: string; system_account_id: string }>)
  }
  return rows
}

function loadGroupAccountStatsGroupsPage(
  database: DatabaseSync,
  cursorGroupId: string | undefined,
  limit: number
): Array<{ id: string; system_account_id: string }> {
  const cursorClause = cursorGroupId ? 'WHERE id > ?' : ''
  const params = cursorGroupId ? [cursorGroupId, Math.max(1, Math.trunc(limit))] : [Math.max(1, Math.trunc(limit))]
  return database.prepare(`
    SELECT id, system_account_id
    FROM groups
    ${cursorClause}
    ORDER BY id ASC
    LIMIT ?
  `).all(...params) as unknown as Array<{ id: string; system_account_id: string }>
}

function loadGroupAccountStatsRows(
  database: DatabaseSync,
  groupIds?: string[]
): Array<{
  group_id: string
  account_id: string | null
  account_authorization_id: string | null
  group_system_account_id: string
  account_system_account_id: string | null
  status: string | null
  schedulable: number | null
  cooldown_until: string | null
  concurrency_limit: number | null
  authorization_status: string | null
  authorization_expires_at: string | null
}> {
  const rows: Array<{
    group_id: string
    account_id: string | null
    account_authorization_id: string | null
    group_system_account_id: string
    account_system_account_id: string | null
    status: string | null
    schedulable: number | null
    cooldown_until: string | null
    concurrency_limit: number | null
    authorization_status: string | null
    authorization_expires_at: string | null
  }> = []
  const chunks = groupIds ? chunkValues(groupIds, 900) : [undefined]
  for (const chunk of chunks) {
    const where = chunk ? `AND group_accounts.group_id IN (${sqlPlaceholders(chunk.length)})` : ''
    rows.push(...database.prepare(`
      SELECT
        group_accounts.group_id,
        group_accounts.account_id,
        group_accounts.account_authorization_id,
        groups.system_account_id AS group_system_account_id,
        accounts.system_account_id AS account_system_account_id,
        accounts.status,
        accounts.schedulable,
        accounts.cooldown_until,
        accounts.concurrency_limit,
        resource_authorization_rows.status AS authorization_status,
        resource_authorization_rows.expires_at AS authorization_expires_at
      FROM group_accounts
      INNER JOIN groups ON groups.id = group_accounts.group_id
      LEFT JOIN accounts ON accounts.id = group_accounts.account_id
      LEFT JOIN resource_authorizations resource_authorization_rows
        ON resource_authorization_rows.id = group_accounts.account_authorization_id
      WHERE group_accounts.enabled = 1
        AND accounts.deleted_at IS NULL
        ${where}
    `).all(...(chunk ?? [])) as unknown as typeof rows)
  }
  return rows
}

function deleteGroupAccountStatsRows(database: DatabaseSync, groupIds: string[]): void {
  for (const chunk of chunkValues(groupIds, 900)) {
    database.prepare(`DELETE FROM group_account_stats WHERE group_id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk)
  }
}

function uniqueGroupAccountStatsIds(values: Array<string | null | undefined>): string[] {
  return [...new Set(values.map((value) => value?.trim()).filter((value): value is string => Boolean(value)))]
}

function emptyGroupAccountStatsAccumulator(groupId: string, systemAccountId: string): GroupAccountStatsAccumulator {
  return {
    groupId,
    systemAccountId,
    total: 0,
    available: 0,
    active: 0,
    disabled: 0,
    error: 0,
    rateLimited: 0,
    concurrencyLimit: 0
  }
}
