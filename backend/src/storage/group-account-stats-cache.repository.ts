import type { DatabaseSync } from 'node:sqlite'

import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, getStatsDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
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
