import { randomBytes } from 'node:crypto'

import type { SystemAccountPrincipalSummary, SystemAccountRole, SystemAccountStatus, SystemAccountSummary } from '../domain/types.js'
import { hashPassword, hashPasswordAsync, hashSecret, verifyPassword, verifyPasswordAsync } from './crypto.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { ensureDefaultOpenAIGroupForSystemAccount } from './default-group.repository.js'
import { clearGatewayApiKeyValidationCache } from './gateway-api-key.repository.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { normalizeListPage, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { invalidateSystemAccountLookupCache } from './repository-lookups.js'
import { systemAccountPrincipalSummaryFromRow, systemAccountSummaryFromRow, type SystemAccountRow } from './system-account-mappers.js'
import { optionalString } from './value-utils.js'

interface SystemSessionRow {
  id: string
  system_account_id: string
  token_hash: string
  expires_at: string
  created_at: string
  last_seen_at: string
}

const sessionTouchMinIntervalMs = 60 * 1000
const defaultSystemAccountOptionLimit = 50
const defaultSystemAccountPageSize = 20
const maxSystemAccountPageSize = 100
const systemAccountCreateInputKeys = new Set(['username', 'displayName', 'description', 'password', 'role', 'status', 'mustChangePassword', 'imageGenerationEnabled'])
const systemAccountUpdateInputKeys = new Set(['displayName', 'description', 'password', 'role', 'status', 'mustChangePassword', 'imageGenerationEnabled'])

export interface SystemAccountOptionListOptions {
  ids?: string[]
  keyword?: string
  limit?: number
}

export interface SystemAccountListOptions {
  page?: number
  pageSize?: number
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
  return listSystemAccountsPage({ page: 1, pageSize: maxSystemAccountPageSize }).items
}

export function listSystemAccountsPage(options: SystemAccountListOptions = {}): SystemAccountListResult {
  const normalized = normalizeSystemAccountListOptions(options)
  const keywordFilter = buildSystemAccountListKeywordFilter(normalized.keyword)
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT id, username, display_name, description, role, status, must_change_password, image_generation_enabled, last_login_at, created_at, updated_at
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
    total: pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

export function listSystemAccountOptions(options: SystemAccountOptionListOptions = {}): SystemAccountPrincipalSummary[] {
  const optionFilter = buildSystemAccountOptionFilter(options)
  const limitClause = systemAccountOptionLimitClause(options.limit)
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT id, username, display_name, status
      FROM system_accounts
      ${optionFilter.clause}
      ORDER BY status ASC, display_name ASC, username ASC, id ASC
      ${limitClause.clause}
    `)
    .all(...optionFilter.params, ...limitClause.params) as unknown as Array<Pick<SystemAccountRow, 'id' | 'username' | 'display_name' | 'status'>>
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
      username COLLATE NOCASE = ?
      OR username LIKE ? ESCAPE '\\'
      OR display_name COLLATE NOCASE = ?
      OR display_name LIKE ? ESCAPE '\\'
    )`,
    params: [text, prefix, text, prefix]
  }
}

function buildSystemAccountOptionFilter(options: SystemAccountOptionListOptions): { clause: string; params: string[] } {
  const clauses: string[] = []
  const params: string[] = []
  const ids = normalizeTextList(options.ids)
  if (ids.length) {
    clauses.push(`id IN (${sqlPlaceholders(ids.length)})`)
    params.push(...ids)
  }
  const keyword = buildSystemAccountKeywordFilter(options.keyword)
  if (keyword.clause) {
    clauses.push(keyword.clause.replace(/^WHERE\s+/i, ''))
    params.push(...keyword.params)
  }
  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

function normalizeTextList(values?: string[]): string[] {
  if (!values?.length) return []
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))]
    .sort()
    .slice(0, 50)
}

function normalizeSystemAccountListOptions(options: SystemAccountListOptions): Required<Pick<SystemAccountListOptions, 'page' | 'pageSize'>> & Pick<SystemAccountListOptions, 'keyword'> {
  const rawPageSize = options.pageSize
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(maxSystemAccountPageSize, Math.max(1, rawPageSize))
    : defaultSystemAccountPageSize
  const page = normalizeListPage(options.page, pageSize)
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
  const row = getBusinessDatabase()
    .prepare(`
      SELECT id, username, display_name, description, role, status, must_change_password, image_generation_enabled, last_login_at, created_at, updated_at
      FROM system_accounts
      WHERE id = ?
    `)
    .get(id) as unknown as SystemAccountRow | undefined
  return row ? systemAccountSummaryFromRow(row) : undefined
}

export function findSystemAccountByUsername(username: string): (SystemAccountSummary & { passwordHash: string }) | undefined {
  const row = getBusinessDatabase().prepare(`
    SELECT id, username, display_name, description, role, status, password_hash, must_change_password, image_generation_enabled, last_login_at, created_at, updated_at
    FROM system_accounts
    WHERE lower(username) = lower(?)
  `).get(username) as unknown as SystemAccountRow | undefined
  if (!row) {
    return undefined
  }
  const summary = systemAccountSummaryFromRow(row)
  return { ...summary, passwordHash: row.password_hash }
}

function ensureSystemAccountUsernameUnique(username: string, excludeId?: string, database = getBusinessDatabase()): void {
  const row = database
    .prepare('SELECT id FROM system_accounts WHERE lower(username) = lower(?) AND id <> ? LIMIT 1')
    .get(username, excludeId ?? '') as unknown as { id?: string } | undefined
  if (row?.id) throw new Error('用户账户已存在')
}

function ensureSystemAccountDisplayNameUnique(displayName: string, excludeId?: string, database = getBusinessDatabase()): void {
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

export async function verifySystemAccountCredentialsAsync(username: string, password: string): Promise<SystemAccountSummary | undefined> {
  const account = findSystemAccountByUsername(username)
  if (!account || account.status !== 'active') {
    return undefined
  }
  return await verifyPasswordAsync(password, account.passwordHash) ? account : undefined
}

export function createSystemAccount(input: {
  username: string
  displayName: string
  description?: string | null
  password: string
  role?: SystemAccountRole
  status?: SystemAccountStatus
  mustChangePassword?: boolean
  imageGenerationEnabled?: boolean
}): SystemAccountSummary {
  const password = normalizeSystemAccountPassword(input.password)
  return createSystemAccountWithPasswordHash(input, hashPassword(password))
}

export async function createSystemAccountAsync(input: {
  username: string
  displayName: string
  description?: string | null
  password: string
  role?: SystemAccountRole
  status?: SystemAccountStatus
  mustChangePassword?: boolean
  imageGenerationEnabled?: boolean
}): Promise<SystemAccountSummary> {
  const passwordHash = await hashPasswordAsync(normalizeSystemAccountPassword(input.password))
  return createSystemAccountWithPasswordHash(input, passwordHash)
}

export function createSystemAccountWithPasswordHash(input: {
  username: string
  displayName: string
  description?: string | null
  password?: string
  role?: SystemAccountRole
  status?: SystemAccountStatus
  mustChangePassword?: boolean
  imageGenerationEnabled?: boolean
}, passwordHash: string): SystemAccountSummary {
  assertKnownInputKeys(input, systemAccountCreateInputKeys, '系统账户')
  const now = nowIso()
  const id = newId('sysacc')
  const username = normalizeRequiredText(input.username, '用户账户')
  const displayName = normalizeRequiredText(input.displayName, '用户名称')
  const database = getBusinessDatabase()
  ensureSystemAccountUsernameUnique(username, undefined, database)
  ensureSystemAccountDisplayNameUnique(displayName, undefined, database)
  const summary: SystemAccountSummary = {
    id,
    username,
    displayName,
    description: normalizeNullableText(input.description, '说明') ?? undefined,
    role: normalizeSystemAccountRole(input.role, 'user'),
    status: normalizeSystemAccountStatus(input.status, 'active'),
    mustChangePassword: normalizeOptionalBoolean(input.mustChangePassword, true, '下次登录改密'),
    imageGenerationEnabled: normalizeOptionalBoolean(input.imageGenerationEnabled, false, '支持图像生成'),
    createdAt: now,
    updatedAt: now
  }
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database
      .prepare(`
        INSERT INTO system_accounts (
          id, username, display_name, description, role, status, password_hash, must_change_password, image_generation_enabled, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `)
      .run(summary.id, summary.username, summary.displayName, summary.description ?? null, summary.role, summary.status, passwordHash, summary.mustChangePassword ? 1 : 0, summary.imageGenerationEnabled ? 1 : 0, now, now)
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
  imageGenerationEnabled?: boolean
  password?: string
}): SystemAccountSummary | undefined {
  const hasPasswordInput = Object.prototype.hasOwnProperty.call(input, 'password')
  const passwordHash = hasPasswordInput ? hashPassword(normalizeSystemAccountPassword(input.password)) : undefined
  return updateSystemAccountWithPasswordHash(id, input, passwordHash)
}

export async function updateSystemAccountAsync(id: string, input: {
  displayName?: string
  description?: string | null
  role?: SystemAccountRole
  status?: SystemAccountStatus
  mustChangePassword?: boolean
  imageGenerationEnabled?: boolean
  password?: string
}): Promise<SystemAccountSummary | undefined> {
  const hasPasswordInput = Object.prototype.hasOwnProperty.call(input, 'password')
  const passwordHash = hasPasswordInput ? await hashPasswordAsync(normalizeSystemAccountPassword(input.password)) : undefined
  return updateSystemAccountWithPasswordHash(id, input, passwordHash)
}

export function updateSystemAccountWithPasswordHash(id: string, input: {
  displayName?: string
  description?: string | null
  role?: SystemAccountRole
  status?: SystemAccountStatus
  mustChangePassword?: boolean
  imageGenerationEnabled?: boolean
  password?: string
}, passwordHash?: string): SystemAccountSummary | undefined {
  assertKnownInputKeys(input, systemAccountUpdateInputKeys, '系统账户')
  if (Object.prototype.hasOwnProperty.call(input, 'password') && input.password !== undefined && !passwordHash) {
    throw new Error('登录密码不能为空')
  }
  const current = findSystemAccountById(id)
  if (!current) {
    return undefined
  }

  const next = {
    ...current,
    displayName: input.displayName === undefined ? current.displayName : normalizeRequiredText(input.displayName, '用户名称'),
    description: input.description === undefined ? current.description : normalizeNullableText(input.description, '说明') ?? undefined,
    role: normalizeSystemAccountRole(input.role, current.role),
    status: normalizeSystemAccountStatus(input.status, current.status),
    mustChangePassword: normalizeOptionalBoolean(input.mustChangePassword, current.mustChangePassword, '下次登录改密'),
    imageGenerationEnabled: normalizeOptionalBoolean(input.imageGenerationEnabled, current.imageGenerationEnabled, '支持图像生成')
  }
  const now = nowIso()
  ensureSystemAccountDisplayNameUnique(next.displayName, id)
  if (passwordHash) {
    getBusinessDatabase()
      .prepare(`
        UPDATE system_accounts
        SET display_name = ?, description = ?, role = ?, status = ?, password_hash = ?, must_change_password = ?, image_generation_enabled = ?, updated_at = ?
        WHERE id = ?
      `)
      .run(next.displayName, next.description ?? null, next.role, next.status, passwordHash, next.mustChangePassword ? 1 : 0, next.imageGenerationEnabled ? 1 : 0, now, id)
  } else {
    getBusinessDatabase()
      .prepare(`
        UPDATE system_accounts
        SET display_name = ?, description = ?, role = ?, status = ?, must_change_password = ?, image_generation_enabled = ?, updated_at = ?
        WHERE id = ?
      `)
      .run(next.displayName, next.description ?? null, next.role, next.status, next.mustChangePassword ? 1 : 0, next.imageGenerationEnabled ? 1 : 0, now, id)
  }
  invalidateSystemAccountLookupCache(id)
  if (next.status !== current.status || next.imageGenerationEnabled !== current.imageGenerationEnabled) {
    clearGatewayApiKeyValidationCache()
    notifyGatewayRuntimeCacheInvalidation(next.status !== current.status ? 'system_account_status_changed' : 'system_account_image_generation_changed')
  }
  return { ...next, updatedAt: now }
}

export function updateSystemAccountLastLogin(id: string): void {
  getBusinessDatabase()
    .prepare('UPDATE system_accounts SET last_login_at = ?, updated_at = ? WHERE id = ?')
    .run(nowIso(), nowIso(), id)
}

export function createSession(systemAccountId: string, ttlDays = 14): { token: string; sessionId: string; expiresAt: string } {
  const token = randomBytes(32).toString('base64url')
  const sessionId = newId('sess')
  const now = new Date()
  const expiresAt = new Date(now.getTime() + Math.max(1, ttlDays) * 24 * 60 * 60 * 1000).toISOString()
  getBusinessDatabase()
    .prepare(`
      INSERT INTO system_sessions (id, system_account_id, token_hash, expires_at, created_at, last_seen_at)
      VALUES (?, ?, ?, ?, ?, ?)
    `)
    .run(sessionId, systemAccountId, hashSecret(token), expiresAt, now.toISOString(), now.toISOString())
  return { token, sessionId, expiresAt }
}

export function findSessionByToken(token: string): (SessionWithAccount & { tokenHash: string }) | undefined {
  const row = getBusinessDatabase()
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
        sa.image_generation_enabled,
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
  getBusinessDatabase()
    .prepare('UPDATE system_sessions SET last_seen_at = ? WHERE id = ? AND last_seen_at < ?')
    .run(new Date(nowMs).toISOString(), sessionId, cutoff)
}

export function revokeSession(token: string): void {
  getBusinessDatabase().prepare('DELETE FROM system_sessions WHERE token_hash = ?').run(hashSecret(token))
}

export function revokeAllSessionsForAccount(systemAccountId: string): void {
  getBusinessDatabase().prepare('DELETE FROM system_sessions WHERE system_account_id = ?').run(systemAccountId)
}

function normalizeRequiredText(value: unknown, label: string): string {
  if (typeof value !== 'string') {
    throw new Error(`${label}不能为空`)
  }
  const text = value.trim()
  if (!text) {
    throw new Error(`${label}不能为空`)
  }
  return text
}

function normalizeNullableText(value: unknown, label: string): string | null {
  if (value === undefined || value === null) {
    return null
  }
  if (typeof value !== 'string') {
    throw new Error(`${label}必须是字符串`)
  }
  const text = value.trim()
  return text || null
}

function normalizeSystemAccountRole(value: unknown, fallback: SystemAccountRole): SystemAccountRole {
  if (value === undefined) return fallback
  if (value === 'admin' || value === 'user') {
    return value
  }
  throw new Error('系统账户角色无效')
}

function normalizeSystemAccountStatus(value: unknown, fallback: SystemAccountStatus): SystemAccountStatus {
  if (value === undefined) return fallback
  if (value === 'active' || value === 'disabled') {
    return value
  }
  throw new Error('系统账户状态无效')
}

function normalizeOptionalBoolean(value: unknown, fallback: boolean, label: string): boolean {
  if (value === undefined) return fallback
  if (typeof value !== 'boolean') {
    throw new Error(`${label}必须是布尔值`)
  }
  return value
}

function normalizeSystemAccountPassword(value: unknown): string {
  if (typeof value !== 'string' || value.length < 4) {
    throw new Error('登录密码不能少于 4 个字符')
  }
  return value
}

function assertKnownInputKeys(input: object, allowedKeys: ReadonlySet<string>, label: string): void {
  const unknownKeys = Object.keys(input as Record<string, unknown>).filter((key) => !allowedKeys.has(key))
  if (unknownKeys.length) {
    throw new Error(`${label}包含未知字段：${unknownKeys.join('、')}`)
  }
}
