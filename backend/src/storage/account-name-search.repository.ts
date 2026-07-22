import type { DatabaseSync } from 'node:sqlite'

import { scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

const accountNameSearchMinTermLength = 1
const accountNameSearchMaxGramLength = 3

export const maxAccountNameLength = 128

type AccountNameSearchValue = string | number

interface AccountNameSearchRow {
  id?: string
  system_account_id?: string
  name?: string
}

export function replaceAccountNameSearchTerms(
  database: DatabaseSync,
  accountId: string,
  systemAccountId: string,
  name: string,
  createdAt = nowIso()
): void {
  database.prepare('DELETE FROM account_name_search_terms WHERE account_id = ?').run(accountId)
  database.prepare('DELETE FROM account_name_search_documents WHERE account_id = ?').run(accountId)
  const normalizedName = normalizeAccountNameSearchText(name)
  if (!normalizedName) return

  database.prepare(`
    INSERT INTO account_name_search_documents (
      account_id, system_account_id, normalized_name, updated_at
    ) VALUES (?, ?, ?, ?)
  `).run(accountId, systemAccountId, normalizedName, createdAt)

  const terms = buildAccountNameSearchTermsFromNormalizedName(normalizedName)
  if (!terms.length) return
  const insert = database.prepare(`
    INSERT OR IGNORE INTO account_name_search_terms (
      account_id, system_account_id, term, created_at
    ) VALUES (?, ?, ?, ?)
  `)
  for (const term of terms) {
    insert.run(accountId, systemAccountId, term, createdAt)
  }
}

export async function replaceAccountNameSearchTermsAsync(
  client: DatabaseClient,
  accountId: string,
  systemAccountId: string,
  name: string,
  createdAt = nowIso()
): Promise<void> {
  await client.execute(`DELETE FROM ${accountNameSearchTable(client, 'account_name_search_terms')} WHERE account_id = ?`, [accountId])
  await client.execute(`DELETE FROM ${accountNameSearchTable(client, 'account_name_search_documents')} WHERE account_id = ?`, [accountId])
  const normalizedName = normalizeAccountNameSearchText(name)
  if (!normalizedName) return

  await client.execute(`
    INSERT INTO ${accountNameSearchTable(client, 'account_name_search_documents')} (
      account_id, system_account_id, normalized_name, updated_at
    ) VALUES (?, ?, ?, ?)
  `, [accountId, systemAccountId, normalizedName, createdAt])

  const terms = buildAccountNameSearchTermsFromNormalizedName(normalizedName)
  for (const term of terms) {
    if (client.driver === 'postgres') {
      await client.execute(`
        INSERT INTO ${accountNameSearchTable(client, 'account_name_search_terms')} (
          account_id, system_account_id, term, created_at
        ) VALUES (?, ?, ?, ?)
        ON CONFLICT (account_id, term) DO NOTHING
      `, [accountId, systemAccountId, term, createdAt])
    } else {
      await client.execute(`
        INSERT OR IGNORE INTO ${accountNameSearchTable(client, 'account_name_search_terms')} (
          account_id, system_account_id, term, created_at
        ) VALUES (?, ?, ?, ?)
      `, [accountId, systemAccountId, term, createdAt])
    }
  }
}

export function deleteAccountNameSearchTermsForAccounts(accountIds: string[], database: DatabaseSync): void {
  const ids = [...new Set(accountIds.map((id) => id.trim()).filter(Boolean))]
  if (!ids.length) return
  for (const chunk of chunkValues(ids, 900)) {
    database
      .prepare(`DELETE FROM account_name_search_terms WHERE account_id IN (${sqlPlaceholders(chunk.length)})`)
      .run(...chunk)
    database
      .prepare(`DELETE FROM account_name_search_documents WHERE account_id IN (${sqlPlaceholders(chunk.length)})`)
      .run(...chunk)
  }
}

export function accountNameContainsAccountIdSubquery(
  keyword: string | undefined,
  access: AccessScope | undefined
): { sql: string; params: AccountNameSearchValue[] } | undefined {
  const terms = accountNameSearchQueryTerms(keyword)
  if (!terms.length) return undefined

  const systemAccountId = scopedSystemAccountId(access)
  const systemAccountClause = systemAccountId ? 'AND search.system_account_id = ?' : ''
  const keywordContains = normalizeAccountNameSearchText(keyword)
  const params: AccountNameSearchValue[] = systemAccountId
    ? [...terms, systemAccountId, keywordContains, terms.length]
    : [...terms, keywordContains, terms.length]

  return {
    sql: `
      SELECT search.account_id
      FROM account_name_search_terms search INDEXED BY idx_account_name_search_terms_term_owner
      INNER JOIN account_name_search_documents documents ON documents.account_id = search.account_id
      INNER JOIN accounts ON accounts.id = search.account_id
      WHERE search.term IN (${sqlPlaceholders(terms.length)})
        ${systemAccountClause}
        AND instr(documents.normalized_name, ?) > 0
        AND accounts.deleted_at IS NULL
      GROUP BY search.account_id
      HAVING COUNT(DISTINCT search.term) = ?
    `,
    params
  }
}

export function rebuildAccountNameSearchTerms(database: DatabaseSync): { accountCount: number; termCount: number } {
  const rows = database
    .prepare(`
      SELECT id, system_account_id, name
      FROM accounts
      WHERE deleted_at IS NULL
      ORDER BY system_account_id ASC, id ASC
    `)
    .all() as unknown as AccountNameSearchRow[]

  const now = nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  let termCount = 0
  try {
    database.prepare('DELETE FROM account_name_search_terms').run()
    database.prepare('DELETE FROM account_name_search_documents').run()
    const insertDocument = database.prepare(`
      INSERT OR IGNORE INTO account_name_search_documents (
        account_id, system_account_id, normalized_name, updated_at
      ) VALUES (?, ?, ?, ?)
    `)
    const insert = database.prepare(`
      INSERT OR IGNORE INTO account_name_search_terms (
        account_id, system_account_id, term, created_at
      ) VALUES (?, ?, ?, ?)
    `)
    for (const row of rows) {
      if (!row.id || !row.system_account_id || typeof row.name !== 'string') continue
      const normalizedName = normalizeAccountNameSearchText(row.name)
      if (!normalizedName) continue
      insertDocument.run(row.id, row.system_account_id, normalizedName, now)
      for (const term of buildAccountNameSearchTermsFromNormalizedName(normalizedName)) {
        const result = insert.run(row.id, row.system_account_id, term, now)
        termCount += Number(result.changes ?? 0)
      }
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }

  return { accountCount: rows.length, termCount }
}

export async function rebuildAccountNameSearchTermsAsync(): Promise<{ accountCount: number; termCount: number }> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<AccountNameSearchRow>(`
    SELECT id, system_account_id, name
    FROM ${accountNameSearchTable(client, 'accounts')}
    WHERE deleted_at IS NULL
    ORDER BY system_account_id ASC, id ASC
  `)

  const now = nowIso()
  let termCount = 0
  await client.transaction(async (tx) => {
    await tx.execute(`DELETE FROM ${accountNameSearchTable(tx, 'account_name_search_terms')}`)
    await tx.execute(`DELETE FROM ${accountNameSearchTable(tx, 'account_name_search_documents')}`)
    for (const row of rows) {
      if (!row.id || !row.system_account_id || typeof row.name !== 'string') continue
      const normalizedName = normalizeAccountNameSearchText(row.name)
      if (!normalizedName) continue
      await tx.execute(`
        INSERT INTO ${accountNameSearchTable(tx, 'account_name_search_documents')} (
          account_id, system_account_id, normalized_name, updated_at
        ) VALUES (?, ?, ?, ?)
        ON CONFLICT(account_id) DO NOTHING
      `, [row.id, row.system_account_id, normalizedName, now])
      for (const term of buildAccountNameSearchTermsFromNormalizedName(normalizedName)) {
        const result = await tx.execute(`
          INSERT INTO ${accountNameSearchTable(tx, 'account_name_search_terms')} (
            account_id, system_account_id, term, created_at
          ) VALUES (?, ?, ?, ?)
          ON CONFLICT(account_id, term) DO NOTHING
        `, [row.id, row.system_account_id, term, now])
        termCount += result.changes
      }
    }
  })

  return { accountCount: rows.length, termCount }
}

export function buildAccountNameSearchTerms(name: unknown): string[] {
  const normalized = normalizeAccountNameSearchText(name)
  if (!normalized) return []
  return buildAccountNameSearchTermsFromNormalizedName(normalized)
}

function buildAccountNameSearchTermsFromNormalizedName(normalized: string): string[] {
  const terms = new Set<string>()
  for (let length = accountNameSearchMinTermLength; length <= accountNameSearchMaxGramLength; length += 1) {
    addAccountNameSearchGrams(terms, normalized, length)
  }
  return [...terms]
}

function accountNameSearchTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

export function accountNameSearchQueryTerms(keyword: unknown): string[] {
  const normalized = normalizeAccountNameSearchText(keyword)
  if (!normalized) return []
  if ([...normalized].length > maxAccountNameLength) return []

  const length = Math.min(accountNameSearchMaxGramLength, [...normalized].length)
  const terms = new Set<string>()
  addAccountNameSearchGrams(terms, normalized, length)
  return [...terms]
}

function addAccountNameSearchGrams(
  terms: Set<string>,
  value: string,
  length: number
): void {
  const chars = [...value]
  if (chars.length < length) return
  for (let index = 0; index + length <= chars.length; index += 1) {
    const term = chars.slice(index, index + length).join('')
    if (term.trim()) {
      terms.add(term)
    }
  }
}

export function normalizeAccountNameSearchText(value: unknown): string {
  return typeof value === 'string'
    ? value.normalize('NFKC').trim()
    : ''
}

export function escapeAccountNameSearchLike(value: string): string {
  return value.replace(/[\\%_]/g, (char) => `\\${char}`)
}
