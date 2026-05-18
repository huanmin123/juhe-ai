import type { SystemAccountSummary } from '../domain/types.js'
import { getDatabase } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { systemAccountSummaryFromRow, type SystemAccountSummaryRow } from './system-account-mappers.js'

function uniqueIds(values: string[]): string[] {
  return [...new Set(values)].filter(Boolean)
}

function loadRowsByIds<T>(ids: string[], sql: (chunk: string[]) => string): T[] {
  const rows: T[] = []
  const database = getDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...database.prepare(sql(chunk)).all(...chunk) as unknown as T[])
  }
  return rows
}

export function loadSystemAccountNameMap(): Map<string, string> {
  const rows = getDatabase()
    .prepare('SELECT id, username, display_name FROM system_accounts ORDER BY created_at ASC, id ASC')
    .all() as unknown as Array<{ id: string; username: string; display_name: string }>
  return new Map(rows.map((row) => [row.id, row.display_name || row.username]))
}

export function loadSystemAccountsByIds(systemAccountIds: string[]): Map<string, SystemAccountSummary> {
  const ids = uniqueIds(systemAccountIds)
  if (!ids.length) return new Map()
  const rows = loadRowsByIds<SystemAccountSummaryRow>(ids, (chunk) => `
    SELECT id, username, display_name, description, role, status, must_change_password, last_login_at, created_at, updated_at
    FROM system_accounts
    WHERE id IN (${sqlPlaceholders(chunk.length)})
  `)
  return new Map(rows.map((row) => [row.id, systemAccountSummaryFromRow(row)]))
}

export function loadAccountNameMap(accountIds: string[]): Map<string, string> {
  const ids = uniqueIds(accountIds)
  if (!ids.length) return new Map()
  const rows = loadRowsByIds<{ id: string; name: string }>(ids, (chunk) => `SELECT id, name FROM accounts WHERE id IN (${sqlPlaceholders(chunk.length)})`)
  return new Map(rows.map((row) => [row.id, row.name]))
}

export function loadGroupNameMap(groupIds: string[]): Map<string, string> {
  const ids = uniqueIds(groupIds)
  if (!ids.length) return new Map()
  const rows = loadRowsByIds<{ id: string; name: string }>(ids, (chunk) => `SELECT id, name FROM groups WHERE id IN (${sqlPlaceholders(chunk.length)})`)
  return new Map(rows.map((row) => [row.id, row.name]))
}

export function loadApiKeyNameMap(apiKeyIds: string[]): Map<string, string> {
  const ids = uniqueIds(apiKeyIds)
  if (!ids.length) return new Map()
  const rows = loadRowsByIds<{ id: string; name: string }>(ids, (chunk) => `SELECT id, name FROM api_keys WHERE id IN (${sqlPlaceholders(chunk.length)})`)
  return new Map(rows.map((row) => [row.id, row.name]))
}

export function loadSystemTeamNameMap(teamIds: string[]): Map<string, string> {
  const ids = uniqueIds(teamIds)
  if (!ids.length) return new Map()
  const rows = loadRowsByIds<{ id: string; name: string }>(ids, (chunk) => `SELECT id, name FROM system_teams WHERE id IN (${sqlPlaceholders(chunk.length)})`)
  return new Map(rows.map((row) => [row.id, row.name]))
}
