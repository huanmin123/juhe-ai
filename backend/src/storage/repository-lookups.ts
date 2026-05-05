import type { SystemAccountSummary } from '../domain/types.js'
import { getDatabase } from './database.js'
import { sqlPlaceholders } from './query-utils.js'
import { systemAccountSummaryFromRow, type SystemAccountRow } from './system-account-mappers.js'

function uniqueIds(values: string[]): string[] {
  return [...new Set(values)].filter(Boolean)
}

export function loadSystemAccountNameMap(): Map<string, string> {
  const rows = getDatabase()
    .prepare('SELECT id, username, display_name FROM system_accounts ORDER BY created_at ASC')
    .all() as unknown as Array<{ id: string; username: string; display_name: string }>
  return new Map(rows.map((row) => [row.id, row.display_name || row.username]))
}

export function loadSystemAccountsByIds(systemAccountIds: string[]): Map<string, SystemAccountSummary> {
  const ids = uniqueIds(systemAccountIds)
  if (!ids.length) return new Map()
  const rows = getDatabase().prepare(`SELECT * FROM system_accounts WHERE id IN (${sqlPlaceholders(ids.length)})`).all(...ids) as unknown as SystemAccountRow[]
  return new Map(rows.map((row) => [row.id, systemAccountSummaryFromRow(row)]))
}

export function loadAccountNameMap(accountIds: string[]): Map<string, string> {
  const ids = uniqueIds(accountIds)
  if (!ids.length) return new Map()
  const rows = getDatabase()
    .prepare(`SELECT id, name FROM accounts WHERE id IN (${sqlPlaceholders(ids.length)})`)
    .all(...ids) as unknown as Array<{ id: string; name: string }>
  return new Map(rows.map((row) => [row.id, row.name]))
}

export function loadGroupNameMap(groupIds: string[]): Map<string, string> {
  const ids = uniqueIds(groupIds)
  if (!ids.length) return new Map()
  const rows = getDatabase()
    .prepare(`SELECT id, name FROM groups WHERE id IN (${sqlPlaceholders(ids.length)})`)
    .all(...ids) as unknown as Array<{ id: string; name: string }>
  return new Map(rows.map((row) => [row.id, row.name]))
}

export function loadSystemTeamNameMap(teamIds: string[]): Map<string, string> {
  const ids = uniqueIds(teamIds)
  if (!ids.length) return new Map()
  const rows = getDatabase()
    .prepare(`SELECT id, name FROM system_teams WHERE id IN (${sqlPlaceholders(ids.length)})`)
    .all(...ids) as unknown as Array<{ id: string; name: string }>
  return new Map(rows.map((row) => [row.id, row.name]))
}
