import { randomBytes } from 'node:crypto'

import type { SystemAccountPrincipalSummary, SystemAccountRole, SystemAccountStatus, SystemAccountSummary } from '../domain/types.js'
import { hashPassword, hashSecret, verifyPassword } from './crypto.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { ensureDefaultOpenAIGroupForSystemAccount } from './default-group.repository.js'
import { clearGatewayApiKeyValidationCache } from './gateway-api-key.repository.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { compatiblePagedTotal, takePageRows } from './query-utils.js'
import { invalidateSystemAccountLookupCache } from './repository-lookups.js'
import { systemAccountPrincipalSummaryFromRow, systemAccountSummaryFromRow, type SystemAccountRow } from './system-account-mappers.js'
import { optionalNullableString, optionalString } from './value-utils.js'

interface SystemSessionRow {
  id: string
  system_account_id: string
  token_hash: string
  expires_at: string
  created_at: string
  last_seen_at: string
}

const sessionTouchMinIntervalMs = 60 * 1000
const defaultSystemAccountOptionLimit = 500
const defaultSystemAccountPageSize = 20
const maxSystemAccountPageSize = 100

export interface SystemAccountOptionListOptions {
  keyword?: string
  limit?: number
}

export interface SystemAccountListOptions {
  page?: number
  pageSize?: number
  limit?: number
  keyword?: string
}

export interface SystemAccountListResult {
  items: SystemAccountSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface SessionWithAccount {
  sessionId: string
  expiresAt: string
  lastSeenAt: string
  account: SystemAccountSummary
}

export function listSystemAccounts(): SystemAccountSummary[] {
  const rows = getDatabase()
    .prepare(`
      SELECT id, username, display_name, description, role, status, must_change_password, last_login_at, created_at, updated_at
      FROM system_accounts
      ORDER BY created_at ASC, id ASC
    `)
    .all() as unknown as SystemAccountRow[]
  return rows.map(systemAccountSummaryFromRow)
}

export function listSystemAccountsPage(options: SystemAccountListOptions = {}): SystemAccountListResult {
  const normalized = normalizeSystemAccountListOptions(options)
  const keywordFilter = buildSystemAccountListKeywordFilter(normalized.keyword)
  const rows = getDatabase()
    .prepare(`
      SELECT id, username, display_name, description, role, status, must_change_password, last_login_at, created_at, updated_at
      FROM system_accounts
      ${keywordFilter.clause}
      ORDER BY updated_at DESC, id DESC
      LIMIT ? OFFSET ?
    `)
    .all(...keywordFilter.params, normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize) as unknown as SystemAccountRow[]
  const pageRows = takePageRows(rows, normalized.pageSize)
  const items = pageRows.rows.map(systemAccountSummaryFromRow)
  return {
    items,
    total: compatiblePagedTotal(normalized.page, normalized.pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

export function listSystemAccountOptions(options: SystemAccountOptionListOptions = {}): SystemAccountPrincipalSummary[] {
  const keywordFilter = buildSystemAccountKeywordFilter(options.keyword)
  const limitClause = systemAccountOptionLimitClause(options.limit)
  const rows = getDatabase()
    .prepare(`
      SELECT id, username, display_name, status
      FROM system_accounts
      ${keywordFilter.clause}
      ORDER BY status ASC, display_name ASC, username ASC, id ASC
      ${limitClause.clause}
    `)
    .all(...keywordFilter.params, ...limitClause.params) as unknown as Array<Pick<SystemAccountRow, 'id' | 'username' | 'display_name' | 'status'>>
  return rows.map(systemAccountPrincipalSummaryFromRow)
}

function buildSystemAccountListKeywordFilter(keyword?: string): { clause: string; params: string[] } {
  const text = optionalString(keyword)
  if (!text) return { clause: '', params: [] }
  const prefix = `${escapeLikePrefix(text)}%`
  return {
    clause: `WHERE (
      display_name COLLATE NOCASE = ?
      OR display_name LIKE ? ESCAPE '\\'
    )`,
    params: [text, prefix]
  }
}

function buildSystemAccountKeywordFilter(keyword?: string): { clause: string; params: string[] } {
  const text = optionalString(keyword)
  if (!text) return { clause: '', params: [] }
  const prefix = `${escapeLikePrefix(text)}%`
  return {
    clause: `WHERE (
      id = ?
      OR id LIKE ? ESCAPE '\\'
      OR username COLLATE NOCASE = ?
      OR username LIKE ? ESCAPE '\\'
      OR display_name COLLATE NOCASE = ?
      OR display_name LIKE ? ESCAPE '\\'
    )`,
    params: [text, prefix, text, prefix, text, prefix]
  }
}

function normalizeSystemAccountListOptions(options: SystemAccountListOptions): Required<Pick<SystemAccountListOptions, 'page' | 'pageSize'>> & Pick<SystemAccountListOptions, 'keyword'> {
  const page = typeof options.page === 'number' && Number.isInteger(options.page) ? Math.max(1, options.page) : 1
  const rawPageSize = options.pageSize ?? options.limit
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(maxSystemAccountPageSize, Math.max(1, rawPageSize))
    : defaultSystemAccountPageSize
  return {
    page,
    pageSize,
    keyword: optionalString(options.keyword)
  }
}

function systemAccountOptionLimitClause(limit?: number): { clause: string; params: number[] } {
  const safeLimit = typeof limit === 'number' && Number.isInteger(limit)
    ? Math.min(defaultSystemAccountOptionLimit, Math.max(1, limit))
    : defaultSystemAccountOptionLimit
  return { clause: 'LIMIT ?', params: [safeLimit] }
}

function escapeLikePrefix(value: string): string {
  return value.replace(/[\\%_]/g, (char) => `\\${char}`)
}

export function findSystemAccountById(id: string): SystemAccountSummary | undefined {
  const row = getDatabase()
    .prepare(`
      SELECT id, username, display_name, description, role, status, must_change_password, last_login_at, created_at, updated_at
      FROM system_accounts
      WHERE id = ?
    `)
    .get(id) as unknown as SystemAccountRow | undefined
  return row ? systemAccountSummaryFromRow(row) : undefined
}

export function findSystemAccountByUsername(username: string): (SystemAccountSummary & { passwordHash: string }) | undefined {
  const row = getDatabase().prepare(`
    SELECT id, username, display_name, description, role, status, password_hash, must_change_password, last_login_at, created_at, updated_at
    FROM system_accounts
    WHERE lower(username) = lower(?)
  `).get(username) as unknown as SystemAccountRow | undefined
  if (!row) {
    return undefined
  }
  const summary = systemAccountSummaryFromRow(row)
  return { ...summary, passwordHash: row.password_hash }
}

function ensureSystemAccountUsernameUnique(username: string, excludeId?: string, database = getDatabase()): void {
  const row = database
    .prepare('SELECT id FROM system_accounts WHERE lower(username) = lower(?) AND id <> ? LIMIT 1')
    .get(username, excludeId ?? '') as unknown as { id?: string } | undefined
  if (row?.id) throw new Error('用户账户已存在')
}

function ensureSystemAccountDisplayNameUnique(displayName: string, excludeId?: string, database = getDatabase()): void {
  const row = database
    .prepare('SELECT id FROM system_accounts WHERE lower(display_name) = lower(?) AND id <> ? LIMIT 1')
    .get(displayName, excludeId ?? '') as unknown as { id?: string } | undefined
  if (row?.id) throw new Error('用户名称已存在')
}

export function verifySystemAccountCredentials(username: string, password: string): SystemAccountSummary | undefined {
  const account = findSystemAccountByUsername(username)
  if (!account || account.status !== 'active') {
    return undefined
  }
  return verifyPassword(password, account.passwordHash) ? account : undefined
}

export function createSystemAccount(input: {
  username: string
  displayName: string
  description?: string | null
  password: string
  role?: SystemAccountRole
  status?: SystemAccountStatus
  mustChangePassword?: boolean
}): SystemAccountSummary {
  const now = nowIso()
  const id = newId('sysacc')
  const username = input.username.trim()
  const displayName = input.displayName.trim() || username
  const database = getDatabase()
  ensureSystemAccountUsernameUnique(username, undefined, database)
  ensureSystemAccountDisplayNameUnique(displayName, undefined, database)
  const summary: SystemAccountSummary = {
    id,
    username,
    displayName,
    description: optionalString(input.description),
    role: input.role ?? 'user',
    status: input.status ?? 'active',
    mustChangePassword: input.mustChangePassword ?? true,
    createdAt: now,
    updatedAt: now
  }
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database
      .prepare(`
        INSERT INTO system_accounts (
          id, username, display_name, description, role, status, password_hash, must_change_password, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `)
      .run(summary.id, summary.username, summary.displayName, summary.description ?? null, summary.role, summary.status, hashPassword(input.password), summary.mustChangePassword ? 1 : 0, now, now)
    ensureDefaultOpenAIGroupForSystemAccount(summary.id, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  invalidateSystemAccountLookupCache(summary.id)
  return summary
}

export function updateSystemAccount(id: string, input: {
  displayName?: string
  description?: string | null
  role?: SystemAccountRole
  status?: SystemAccountStatus
  mustChangePassword?: boolean
  password?: string
}): SystemAccountSummary | undefined {
  const current = findSystemAccountById(id)
  if (!current) {
    return undefined
  }

  const next = {
    ...current,
    displayName: input.displayName?.trim() || current.displayName,
    description: input.description === undefined ? current.description : optionalNullableString(input.description) ?? undefined,
    role: input.role ?? current.role,
    status: input.status ?? current.status,
    mustChangePassword: input.mustChangePassword ?? current.mustChangePassword
  }
  const now = nowIso()
  ensureSystemAccountDisplayNameUnique(next.displayName, id)
  if (input.password) {
    getDatabase()
      .prepare(`
        UPDATE system_accounts
        SET display_name = ?, description = ?, role = ?, status = ?, password_hash = ?, must_change_password = ?, updated_at = ?
        WHERE id = ?
      `)
      .run(next.displayName, next.description ?? null, next.role, next.status, hashPassword(input.password), next.mustChangePassword ? 1 : 0, now, id)
  } else {
    getDatabase()
      .prepare(`
        UPDATE system_accounts
        SET display_name = ?, description = ?, role = ?, status = ?, must_change_password = ?, updated_at = ?
        WHERE id = ?
      `)
      .run(next.displayName, next.description ?? null, next.role, next.status, next.mustChangePassword ? 1 : 0, now, id)
  }
  invalidateSystemAccountLookupCache(id)
  if (next.status !== current.status) {
    clearGatewayApiKeyValidationCache()
    notifyGatewayRuntimeCacheInvalidation('system_account_status_changed')
  }
  return { ...next, updatedAt: now }
}

export function updateSystemAccountLastLogin(id: string): void {
  getDatabase()
    .prepare('UPDATE system_accounts SET last_login_at = ?, updated_at = ? WHERE id = ?')
    .run(nowIso(), nowIso(), id)
}

export function createSession(systemAccountId: string, ttlDays = 14): { token: string; sessionId: string; expiresAt: string } {
  const token = randomBytes(32).toString('base64url')
  const sessionId = newId('sess')
  const now = new Date()
  const expiresAt = new Date(now.getTime() + Math.max(1, ttlDays) * 24 * 60 * 60 * 1000).toISOString()
  getDatabase()
    .prepare(`
      INSERT INTO system_sessions (id, system_account_id, token_hash, expires_at, created_at, last_seen_at)
      VALUES (?, ?, ?, ?, ?, ?)
    `)
    .run(sessionId, systemAccountId, hashSecret(token), expiresAt, now.toISOString(), now.toISOString())
  return { token, sessionId, expiresAt }
}

export function findSessionByToken(token: string): (SessionWithAccount & { tokenHash: string }) | undefined {
  const row = getDatabase()
    .prepare(`
      SELECT
        ss.id AS id,
        ss.token_hash,
        ss.expires_at,
        ss.created_at AS session_created_at,
        ss.last_seen_at,
        sa.id AS account_id,
        sa.username,
        sa.display_name,
        sa.role,
        sa.status,
        sa.password_hash,
        sa.must_change_password,
        sa.last_login_at,
        sa.created_at,
        sa.updated_at
      FROM system_sessions ss
      INNER JOIN system_accounts sa ON sa.id = ss.system_account_id
      WHERE ss.token_hash = ?
    `)
    .get(hashSecret(token)) as unknown as (SystemSessionRow & Omit<SystemAccountRow, 'id'> & { account_id: string }) | undefined
  if (!row) {
    return undefined
  }
  if (Date.parse(row.expires_at) <= Date.now() || row.status !== 'active') {
    return undefined
  }
  return {
    sessionId: row.id,
    expiresAt: row.expires_at,
    lastSeenAt: row.last_seen_at,
    tokenHash: row.token_hash,
    account: systemAccountSummaryFromRow({ ...row, id: row.account_id })
  }
}

export function touchSession(sessionId: string, lastSeenAt?: string): void {
  const nowMs = Date.now()
  if (lastSeenAt && Number.isFinite(Date.parse(lastSeenAt)) && nowMs - Date.parse(lastSeenAt) < sessionTouchMinIntervalMs) {
    return
  }

  const cutoff = new Date(nowMs - sessionTouchMinIntervalMs).toISOString()
  getDatabase()
    .prepare('UPDATE system_sessions SET last_seen_at = ? WHERE id = ? AND last_seen_at < ?')
    .run(new Date(nowMs).toISOString(), sessionId, cutoff)
}

export function revokeSession(token: string): void {
  getDatabase().prepare('DELETE FROM system_sessions WHERE token_hash = ?').run(hashSecret(token))
}

export function revokeAllSessionsForAccount(systemAccountId: string): void {
  getDatabase().prepare('DELETE FROM system_sessions WHERE system_account_id = ?').run(systemAccountId)
}
