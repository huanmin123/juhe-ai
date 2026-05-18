import { getDatabase, getRecordDatabase, nowIso } from './database.js'
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

export function loadGroupAccountIdsByGroupIds(groupIds: string[]): Map<string, string[]> {
  const ids = uniqueIds(groupIds)
  if (!ids.length) return new Map()
  const now = nowIso()
  const rows: Array<{ group_id: string; account_id: string }> = []
  const database = getDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...database
      .prepare(`
        SELECT group_accounts.group_id, group_accounts.account_id
        FROM group_accounts
        INNER JOIN groups ON groups.id = group_accounts.group_id
        INNER JOIN accounts ON accounts.id = group_accounts.account_id
        LEFT JOIN resource_authorizations account_authorizations
          ON account_authorizations.id = group_accounts.account_authorization_id
        WHERE group_accounts.enabled = 1
          AND group_accounts.group_id IN (${sqlPlaceholders(chunk.length)})
          AND (
            accounts.system_account_id = groups.system_account_id
            OR (
              account_authorizations.status = 'active'
              AND (account_authorizations.expires_at IS NULL OR account_authorizations.expires_at > ?)
            )
          )
        ORDER BY group_accounts.group_id ASC, group_accounts.created_at ASC, group_accounts.account_id ASC
      `)
      .all(...chunk, now) as unknown as Array<{ group_id: string; account_id: string }>)
  }
  const result = new Map<string, string[]>()
  for (const row of rows) {
    result.set(row.group_id, [...(result.get(row.group_id) ?? []), row.account_id])
  }
  return result
}

export function loadGroupAccountStatsByGroupIds(groupIds: string[]): Map<string, GroupAccountStatsRow> {
  const ids = uniqueIds(groupIds)
  if (!ids.length) return new Map()
  const rows: GroupAccountStatsRow[] = []
  const database = getRecordDatabase()
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
