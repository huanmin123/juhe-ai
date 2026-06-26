import type { DatabaseSync } from 'node:sqlite'

import type { AccountTagSummary } from '../domain/types.js'
import { currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { runtimeConfig } from '../config/runtime.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

export type { AccountTagSummary } from '../domain/types.js'

export class AccountTagInUseError extends Error {
  constructor() {
    super('标签已绑定账户，不能删除')
    this.name = 'AccountTagInUseError'
  }
}

const maxTagsPerAccount = 24
const maxTagNameLength = 40

type AccountTagRow = {
  id: string
  system_account_id: string
  name: string
  account_count?: number | null
  created_at: string
  updated_at: string
}

export function listAccountTags(access?: AccessScope): AccountTagSummary[] {
  const systemAccountId = accountTagOwnerSystemAccountId(access)
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT account_tags.id, account_tags.system_account_id, account_tags.name,
        COUNT(CASE
          WHEN active_accounts.id IS NOT NULL
            AND (
              active_accounts.authorization_instance_authorization_id IS NULL
              OR visible_authorizations.id IS NOT NULL
            )
          THEN active_accounts.id
        END) AS account_count,
        account_tags.created_at, account_tags.updated_at
      FROM account_tags
      LEFT JOIN account_tag_bindings
        ON account_tag_bindings.tag_id = account_tags.id
      LEFT JOIN accounts active_accounts
        ON active_accounts.id = account_tag_bindings.account_id
        AND active_accounts.deleted_at IS NULL
      LEFT JOIN resource_authorizations visible_authorizations
        ON visible_authorizations.id = active_accounts.authorization_instance_authorization_id
        AND visible_authorizations.grantee_system_account_id = active_accounts.system_account_id
        AND visible_authorizations.status IN ('active', 'paused', 'expired')
      WHERE account_tags.system_account_id = ?
      GROUP BY account_tags.id
      ORDER BY account_tags.name COLLATE NOCASE ASC, account_tags.id ASC
    `)
    .all(systemAccountId) as unknown as AccountTagRow[]
  return rows.map(accountTagSummaryFromRow)
}

export function deleteAccountTag(tagId: string, access?: AccessScope): boolean {
  const id = tagId.trim()
  if (!id) return false
  const systemAccountId = accountTagOwnerSystemAccountId(access)
  const database = getBusinessDatabase()
  const row = database
    .prepare('SELECT id FROM account_tags WHERE id = ? AND system_account_id = ? LIMIT 1')
    .get(id, systemAccountId) as unknown as { id?: string } | undefined
  if (!row?.id) return false
  const activeBinding = database
    .prepare(`
      SELECT 1
      FROM account_tag_bindings
      INNER JOIN accounts
        ON accounts.id = account_tag_bindings.account_id
        AND accounts.deleted_at IS NULL
      LEFT JOIN resource_authorizations visible_authorizations
        ON visible_authorizations.id = accounts.authorization_instance_authorization_id
        AND visible_authorizations.grantee_system_account_id = accounts.system_account_id
        AND visible_authorizations.status IN ('active', 'paused', 'expired')
      WHERE account_tag_bindings.tag_id = ?
        AND (
          accounts.authorization_instance_authorization_id IS NULL
          OR visible_authorizations.id IS NOT NULL
        )
      LIMIT 1
    `)
    .get(id) as unknown as { 1?: number } | undefined
  if (activeBinding) {
    throw new AccountTagInUseError()
  }
  const result = database
    .prepare('DELETE FROM account_tags WHERE id = ? AND system_account_id = ?')
    .run(id, systemAccountId)
  return Number(result.changes ?? 0) > 0
}

export function updateAccountTags(accountId: string, tagNamesInput: unknown, access?: AccessScope): AccountTagSummary[] | undefined {
  const database = getBusinessDatabase()
  const account = database
    .prepare('SELECT id, system_account_id FROM accounts WHERE id = ? AND deleted_at IS NULL LIMIT 1')
    .get(accountId) as unknown as { id?: string; system_account_id?: string } | undefined
  if (!account?.id || !account.system_account_id) return undefined
  if (account.system_account_id !== accountTagOwnerSystemAccountId(access)) return undefined
  const tagNames = normalizeAccountTagNamesInput(tagNamesInput) ?? []
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const tags = replaceAccountTags(account.id, account.system_account_id, tagNames, nowIso(), database)
    commitDatabaseTransaction(database, transactionStarted)
    return tags
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

export function replaceAccountTags(accountId: string, systemAccountId: string, tagNamesInput: unknown, now = nowIso(), database = getBusinessDatabase()): AccountTagSummary[] {
  const tagNames = normalizeAccountTagNamesInput(tagNamesInput) ?? []
  database
    .prepare('DELETE FROM account_tag_bindings WHERE account_id = ?')
    .run(accountId)
  if (!tagNames.length) return []
  const tags = tagNames.map((name) => upsertAccountTag(database, systemAccountId, name, now))
  const insertBinding = database.prepare(`
    INSERT OR IGNORE INTO account_tag_bindings (
      account_id, tag_id, system_account_id, created_at
    ) VALUES (?, ?, ?, ?)
  `)
  for (const tag of tags) {
    insertBinding.run(accountId, tag.id, systemAccountId, now)
  }
  return tags
}

export async function replaceAccountTagsAsync(client: DatabaseClient, accountId: string, systemAccountId: string, tagNamesInput: unknown, now = nowIso()): Promise<AccountTagSummary[]> {
  const tagNames = normalizeAccountTagNamesInput(tagNamesInput) ?? []
  await client.execute(`DELETE FROM ${accountTagsTable(client, 'account_tag_bindings')} WHERE account_id = ?`, [accountId])
  if (!tagNames.length) return []
  const tags: AccountTagSummary[] = []
  for (const name of tagNames) {
    tags.push(await upsertAccountTagAsync(client, systemAccountId, name, now))
  }
  for (const tag of tags) {
    if (client.driver === 'postgres') {
      await client.execute(`
        INSERT INTO ${accountTagsTable(client, 'account_tag_bindings')} (
          account_id, tag_id, system_account_id, created_at
        ) VALUES (?, ?, ?, ?)
        ON CONFLICT (account_id, tag_id) DO NOTHING
      `, [accountId, tag.id, systemAccountId, now])
    } else {
      await client.execute(`
        INSERT OR IGNORE INTO ${accountTagsTable(client, 'account_tag_bindings')} (
          account_id, tag_id, system_account_id, created_at
        ) VALUES (?, ?, ?, ?)
      `, [accountId, tag.id, systemAccountId, now])
    }
  }
  return tags
}

export function deleteAccountTagBindingsForAccounts(accountIds: string[], database = getBusinessDatabase()): void {
  const ids = [...new Set(accountIds.map((id) => id.trim()).filter(Boolean))]
  for (const chunk of chunkValues(ids, 900)) {
    database
      .prepare(`DELETE FROM account_tag_bindings WHERE account_id IN (${sqlPlaceholders(chunk.length)})`)
      .run(...chunk)
  }
}

export function loadAccountTagsByAccountIds(accountIds: string[]): Map<string, AccountTagSummary[]> {
  const ids = [...new Set(accountIds.map((id) => id.trim()).filter(Boolean))]
  const output = new Map<string, AccountTagSummary[]>()
  if (!ids.length) return output
  const rows: Array<AccountTagRow & { account_id: string }> = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...database
      .prepare(`
        SELECT account_tag_bindings.account_id,
          account_tags.id, account_tags.system_account_id, account_tags.name,
          account_tags.created_at, account_tags.updated_at
        FROM account_tag_bindings
        INNER JOIN account_tags ON account_tags.id = account_tag_bindings.tag_id
        WHERE account_tag_bindings.account_id IN (${sqlPlaceholders(chunk.length)})
        ORDER BY account_tags.name COLLATE NOCASE ASC, account_tags.id ASC
      `)
      .all(...chunk) as unknown as Array<AccountTagRow & { account_id: string }>)
  }
  for (const row of rows) {
    output.set(row.account_id, [...(output.get(row.account_id) ?? []), accountTagSummaryFromRow(row)])
  }
  return output
}

export async function loadAccountTagsByAccountIdsAsync(accountIds: string[]): Promise<Map<string, AccountTagSummary[]>> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return loadAccountTagsByAccountIds(accountIds)
  }
  const ids = [...new Set(accountIds.map((id) => id.trim()).filter(Boolean))]
  const output = new Map<string, AccountTagSummary[]>()
  if (!ids.length) return output
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows: Array<AccountTagRow & { account_id: string }> = []
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...await client.query<AccountTagRow & { account_id: string }>(`
      SELECT account_tag_bindings.account_id,
        account_tags.id, account_tags.system_account_id, account_tags.name,
        account_tags.created_at, account_tags.updated_at
      FROM ${accountTagsTable(client, 'account_tag_bindings')} account_tag_bindings
      INNER JOIN ${accountTagsTable(client, 'account_tags')} account_tags
        ON account_tags.id = account_tag_bindings.tag_id
      WHERE account_tag_bindings.account_id IN (${chunk.map(() => '?').join(', ')})
      ORDER BY lower(account_tags.name) ASC, account_tags.id ASC
    `, chunk))
  }
  for (const row of rows) {
    output.set(row.account_id, [...(output.get(row.account_id) ?? []), accountTagSummaryFromRow(row)])
  }
  return output
}

export function normalizeAccountTagNamesInput(value: unknown): string[] | undefined {
  if (value === undefined) return undefined
  if (!Array.isArray(value)) {
    throw new Error('账户标签必须是字符串数组')
  }
  const output: string[] = []
  const seen = new Set<string>()
  for (const item of value) {
    if (typeof item !== 'string') {
      throw new Error('账户标签必须是字符串数组')
    }
    const name = normalizeTagName(item)
    if (!name) continue
    const key = name.toLocaleLowerCase()
    if (seen.has(key)) continue
    if (output.length >= maxTagsPerAccount) {
      throw new Error(`单个账户最多配置 ${maxTagsPerAccount} 个标签`)
    }
    seen.add(key)
    output.push(name)
  }
  return output
}

function upsertAccountTag(database: DatabaseSync, systemAccountId: string, name: string, now: string): AccountTagSummary {
  const existing = database
    .prepare('SELECT id, system_account_id, name, created_at, updated_at FROM account_tags WHERE system_account_id = ? AND lower(name) = lower(?) LIMIT 1')
    .get(systemAccountId, name) as unknown as AccountTagRow | undefined
  if (existing) {
    if (existing.name !== name) {
      database
        .prepare('UPDATE account_tags SET name = ?, updated_at = ? WHERE id = ?')
        .run(name, now, existing.id)
      return accountTagSummaryFromRow({ ...existing, name, updated_at: now })
    }
    return accountTagSummaryFromRow(existing)
  }
  const row: AccountTagRow = {
    id: newId('acctag'),
    system_account_id: systemAccountId,
    name,
    created_at: now,
    updated_at: now
  }
  database
    .prepare(`
      INSERT INTO account_tags (
        id, system_account_id, name, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?)
    `)
    .run(row.id, row.system_account_id, row.name, row.created_at, row.updated_at)
  return accountTagSummaryFromRow(row)
}

async function upsertAccountTagAsync(client: DatabaseClient, systemAccountId: string, name: string, now: string): Promise<AccountTagSummary> {
  const existing = await client.one<AccountTagRow>(`
    SELECT id, system_account_id, name, created_at, updated_at
    FROM ${accountTagsTable(client, 'account_tags')}
    WHERE system_account_id = ? AND lower(name) = lower(?)
    LIMIT 1
  `, [systemAccountId, name])
  if (existing) {
    if (existing.name !== name) {
      await client.execute(`UPDATE ${accountTagsTable(client, 'account_tags')} SET name = ?, updated_at = ? WHERE id = ?`, [name, now, existing.id])
      return accountTagSummaryFromRow({ ...existing, name, updated_at: now })
    }
    return accountTagSummaryFromRow(existing)
  }
  const row: AccountTagRow = {
    id: newId('acctag'),
    system_account_id: systemAccountId,
    name,
    created_at: now,
    updated_at: now
  }
  await client.execute(`
    INSERT INTO ${accountTagsTable(client, 'account_tags')} (
      id, system_account_id, name, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?)
  `, [row.id, row.system_account_id, row.name, row.created_at, row.updated_at])
  return accountTagSummaryFromRow(row)
}

function accountTagOwnerSystemAccountId(access?: AccessScope): string {
  return scopedSystemAccountId(access) ?? currentSystemAccountId(access)
}

function normalizeTagName(value: string): string {
  const name = value.replace(/\s+/g, ' ').trim()
  if (!name) return ''
  if (name.length > maxTagNameLength) {
    throw new Error(`账户标签不能超过 ${maxTagNameLength} 个字符`)
  }
  return name
}

function accountTagSummaryFromRow(row: AccountTagRow): AccountTagSummary {
  return {
    id: row.id,
    name: row.name,
    accountCount: Math.max(0, Number(row.account_count ?? 0)),
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function accountTagsTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}
