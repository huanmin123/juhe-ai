import { randomBytes } from 'node:crypto'

import { isAdminRole, isSuperAdminRole, type SystemAccountPrincipalSummary, type SystemAccountRole, type SystemAccountStatus, type SystemAccountSummary } from '../domain/types.js'
import { hashPassword, hashPasswordAsync, hashSecret, verifyPassword, verifyPasswordAsync } from './crypto.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { ensureDefaultBuiltInGroupsForSystemAccount } from './default-group.repository.js'
import { ensureDefaultApiKeysForSystemAccount, ensureDefaultApiKeysForSystemAccountAsync } from './api-key.repository.js'
import { clearGatewayApiKeyValidationCache } from './gateway-api-key.repository.js'
import { getPostgresPool } from './postgres-client.js'
import { ensureDefaultRouteStrategiesForSystemAccount, ensureDefaultRouteStrategiesForSystemAccountAsync } from './route-strategy.repository.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { runtimeConfig } from '../config/runtime.js'
import { DEFAULT_BUILT_IN_GROUPS } from './schema-defaults.js'
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
const businessSchemaName = 'juhe_business'
const whitespacePattern = /\s/
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

export async function listSystemAccountsAsync(): Promise<SystemAccountSummary[]> {
  return (await listSystemAccountsPageAsync({ page: 1, pageSize: maxSystemAccountPageSize })).items
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

export async function listSystemAccountsPageAsync(options: SystemAccountListOptions = {}): Promise<SystemAccountListResult> {
  const normalized = normalizeSystemAccountListOptions(options)
  const client = await getSystemAccountDatabaseClient()
  const keywordFilter = buildSystemAccountListKeywordFilterForClient(client, normalized.keyword)
  const rows = await client.query<SystemAccountRow>(`
    SELECT id, username, display_name, description, role, status, must_change_password, image_generation_enabled, last_login_at, created_at, updated_at
    FROM ${systemAccountTable(client, 'system_accounts')}
    ${keywordFilter.clause}
    ORDER BY updated_at DESC, id DESC
    LIMIT ? OFFSET ?
  `, [...keywordFilter.params, normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize])
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

export async function listSystemAccountOptionsAsync(options: SystemAccountOptionListOptions = {}): Promise<SystemAccountPrincipalSummary[]> {
  const client = await getSystemAccountDatabaseClient()
  const optionFilter = buildSystemAccountOptionFilterForClient(client, options)
  const limitClause = systemAccountOptionLimitClause(options.limit)
  const rows = await client.query<Pick<SystemAccountRow, 'id' | 'username' | 'display_name' | 'status'>>(`
    SELECT id, username, display_name, status
    FROM ${systemAccountTable(client, 'system_accounts')}
    ${optionFilter.clause}
    ORDER BY status ASC, display_name ASC, username ASC, id ASC
    ${limitClause.clause}
  `, [...optionFilter.params, ...limitClause.params])
  return rows.map(systemAccountPrincipalSummaryFromRow)
}

function buildSystemAccountListKeywordFilter(keyword?: string): { clause: string; params: string[] } {
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

function buildSystemAccountListKeywordFilterForClient(client: DatabaseClient, keyword?: string): { clause: string; params: string[] } {
  if (client.driver === 'sqlite') {
    return buildSystemAccountListKeywordFilter(keyword)
  }
  const text = optionalString(keyword)
  if (!text) return { clause: '', params: [] }
  const prefix = `${escapeLikePrefix(text)}%`
  return {
    clause: `WHERE (
      lower(username) = lower(?)
      OR lower(username) LIKE lower(?) ESCAPE '\\'
      OR lower(display_name) = lower(?)
      OR lower(display_name) LIKE lower(?) ESCAPE '\\'
    )`,
    params: [text, prefix, text, prefix]
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

function buildSystemAccountKeywordFilterForClient(client: DatabaseClient, keyword?: string): { clause: string; params: string[] } {
  if (client.driver === 'sqlite') {
    return buildSystemAccountKeywordFilter(keyword)
  }
  const text = optionalString(keyword)
  if (!text) return { clause: '', params: [] }
  const prefix = `${escapeLikePrefix(text)}%`
  return {
    clause: `WHERE (
      lower(username) = lower(?)
      OR lower(username) LIKE lower(?) ESCAPE '\\'
      OR lower(display_name) = lower(?)
      OR lower(display_name) LIKE lower(?) ESCAPE '\\'
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

function buildSystemAccountOptionFilterForClient(client: DatabaseClient, options: SystemAccountOptionListOptions): { clause: string; params: string[] } {
  const clauses: string[] = []
  const params: string[] = []
  const ids = normalizeTextList(options.ids)
  if (ids.length) {
    clauses.push(`id IN (${client.dialect.bindPlaceholders(ids.length)})`)
    params.push(...ids)
  }
  const keyword = buildSystemAccountKeywordFilterForClient(client, options.keyword)
  if (keyword.clause) {
    clauses.push(keyword.clause.replace(/^WHERE\s+/i, ''))
    params.push(...keyword.params)
  }
  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

export async function findSystemAccountByIdAsync(id: string): Promise<SystemAccountSummary | undefined> {
  const client = await getSystemAccountDatabaseClient()
  return findSystemAccountByIdWithClient(client, id)
}

async function findSystemAccountByIdWithClient(client: DatabaseClient, id: string): Promise<SystemAccountSummary | undefined> {
  const row = await client.one<SystemAccountRow>(`
    SELECT id, username, display_name, description, role, status, password_hash, must_change_password, image_generation_enabled, last_login_at, created_at, updated_at
    FROM ${systemAccountTable(client, 'system_accounts')}
    WHERE id = ?
  `, [id])
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

export async function findSystemAccountByUsernameAsync(username: string): Promise<(SystemAccountSummary & { passwordHash: string }) | undefined> {
  const client = await getSystemAccountDatabaseClient()
  const row = await client.one<SystemAccountRow>(`
    SELECT id, username, display_name, description, role, status, password_hash, must_change_password, image_generation_enabled, last_login_at, created_at, updated_at
    FROM ${systemAccountTable(client, 'system_accounts')}
    WHERE lower(username) = lower(?)
  `, [username])
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

async function ensureSystemAccountUsernameUniqueAsync(client: DatabaseClient, username: string, excludeId?: string): Promise<void> {
  const row = await client.one<{ id?: string }>(`
    SELECT id
    FROM ${systemAccountTable(client, 'system_accounts')}
    WHERE lower(username) = lower(?) AND id <> ?
    LIMIT 1
  `, [username, excludeId ?? ''])
  if (row?.id) throw new Error('用户账户已存在')
}

async function ensureSystemAccountDisplayNameUniqueAsync(client: DatabaseClient, displayName: string, excludeId?: string): Promise<void> {
  const row = await client.one<{ id?: string }>(`
    SELECT id
    FROM ${systemAccountTable(client, 'system_accounts')}
    WHERE lower(display_name) = lower(?) AND id <> ?
    LIMIT 1
  `, [displayName, excludeId ?? ''])
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
  const account = await findSystemAccountByUsernameAsync(username)
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
  return createSystemAccountWithPasswordHashAsync(input, passwordHash)
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
  if (input.password !== undefined) {
    normalizeSystemAccountPassword(input.password)
  }
  const now = nowIso()
  const id = newId('sysacc')
  const username = normalizeRequiredText(input.username, '用户账户')
  const displayName = normalizeRequiredText(input.displayName, '用户名称')
  const database = getBusinessDatabase()
  ensureSystemAccountUsernameUnique(username, undefined, database)
  ensureSystemAccountDisplayNameUnique(displayName, undefined, database)
  const role = normalizeSystemAccountRole(input.role, 'user')
  const summary: SystemAccountSummary = {
    id,
    username,
    displayName,
    description: normalizeNullableText(input.description, '说明') ?? undefined,
    role,
    status: normalizeSystemAccountStatus(input.status, 'active'),
    mustChangePassword: normalizeSystemAccountMustChangePassword(input.mustChangePassword, true, role),
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
    ensureDefaultBuiltInGroupsForSystemAccount(summary.id, now)
    ensureDefaultRouteStrategiesForSystemAccount(summary.id, now)
    ensureDefaultApiKeysForSystemAccount(summary.id, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  invalidateSystemAccountLookupCache(summary.id)
  return summary
}

export async function createSystemAccountWithPasswordHashAsync(input: {
  username: string
  displayName: string
  description?: string | null
  password?: string
  role?: SystemAccountRole
  status?: SystemAccountStatus
  mustChangePassword?: boolean
  imageGenerationEnabled?: boolean
}, passwordHash: string): Promise<SystemAccountSummary> {
  assertKnownInputKeys(input, systemAccountCreateInputKeys, '系统账户')
  if (input.password !== undefined) {
    normalizeSystemAccountPassword(input.password)
  }
  const now = nowIso()
  const id = newId('sysacc')
  const username = normalizeRequiredText(input.username, '用户账户')
  const displayName = normalizeRequiredText(input.displayName, '用户名称')
  const role = normalizeSystemAccountRole(input.role, 'user')
  const summary: SystemAccountSummary = {
    id,
    username,
    displayName,
    description: normalizeNullableText(input.description, '说明') ?? undefined,
    role,
    status: normalizeSystemAccountStatus(input.status, 'active'),
    mustChangePassword: normalizeSystemAccountMustChangePassword(input.mustChangePassword, true, role),
    imageGenerationEnabled: normalizeOptionalBoolean(input.imageGenerationEnabled, false, '支持图像生成'),
    createdAt: now,
    updatedAt: now
  }
  const client = await getSystemAccountDatabaseClient()
  await client.transaction(async (tx) => {
    await ensureSystemAccountUsernameUniqueAsync(tx, username)
    await ensureSystemAccountDisplayNameUniqueAsync(tx, displayName)
    await tx.execute(`
      INSERT INTO ${systemAccountTable(tx, 'system_accounts')} (
        id, username, display_name, description, role, status, password_hash, must_change_password, image_generation_enabled, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, [summary.id, summary.username, summary.displayName, summary.description ?? null, summary.role, summary.status, passwordHash, summary.mustChangePassword ? 1 : 0, summary.imageGenerationEnabled ? 1 : 0, now, now])
    await ensureDefaultBuiltInGroupsForSystemAccountAsync(tx, summary.id, now)
    await ensureDefaultRouteStrategiesForSystemAccountAsync(tx, summary.id, now)
    await ensureDefaultApiKeysForSystemAccountAsync(tx, summary.id, now)
  })
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
  return updateSystemAccountWithPasswordHashAsync(id, input, passwordHash)
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
  if (input.password !== undefined) {
    normalizeSystemAccountPassword(input.password)
  }
  if (Object.prototype.hasOwnProperty.call(input, 'password') && input.password !== undefined && !passwordHash) {
    throw new Error('登录密码不能为空')
  }
  const current = findSystemAccountById(id)
  if (!current) {
    return undefined
  }

  const nextRole = normalizeSystemAccountRole(input.role, current.role)
  const next = {
    ...current,
    displayName: input.displayName === undefined ? current.displayName : normalizeRequiredText(input.displayName, '用户名称'),
    description: input.description === undefined ? current.description : normalizeNullableText(input.description, '说明') ?? undefined,
    role: nextRole,
    status: normalizeSystemAccountStatus(input.status, current.status),
    mustChangePassword: normalizeSystemAccountMustChangePassword(input.mustChangePassword, current.mustChangePassword, nextRole),
    imageGenerationEnabled: normalizeOptionalBoolean(input.imageGenerationEnabled, current.imageGenerationEnabled, '支持图像生成')
  }
  const now = nowIso()
  ensureActiveSuperAdminRemains(current, next, id)
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

export async function updateSystemAccountWithPasswordHashAsync(id: string, input: {
  displayName?: string
  description?: string | null
  role?: SystemAccountRole
  status?: SystemAccountStatus
  mustChangePassword?: boolean
  imageGenerationEnabled?: boolean
  password?: string
}, passwordHash?: string): Promise<SystemAccountSummary | undefined> {
  assertKnownInputKeys(input, systemAccountUpdateInputKeys, '系统账户')
  if (input.password !== undefined) {
    normalizeSystemAccountPassword(input.password)
  }
  if (Object.prototype.hasOwnProperty.call(input, 'password') && input.password !== undefined && !passwordHash) {
    throw new Error('登录密码不能为空')
  }
  const client = await getSystemAccountDatabaseClient()
  let current: SystemAccountSummary | undefined
  let updated: SystemAccountSummary | undefined
  await client.transaction(async (tx) => {
    current = await findSystemAccountByIdWithClient(tx, id)
    if (!current) {
      return
    }
    const nextRole = normalizeSystemAccountRole(input.role, current.role)
    const next = {
      ...current,
      displayName: input.displayName === undefined ? current.displayName : normalizeRequiredText(input.displayName, '用户名称'),
      description: input.description === undefined ? current.description : normalizeNullableText(input.description, '说明') ?? undefined,
      role: nextRole,
      status: normalizeSystemAccountStatus(input.status, current.status),
      mustChangePassword: normalizeSystemAccountMustChangePassword(input.mustChangePassword, current.mustChangePassword, nextRole),
      imageGenerationEnabled: normalizeOptionalBoolean(input.imageGenerationEnabled, current.imageGenerationEnabled, '支持图像生成')
    }
    const now = nowIso()
    await ensureActiveSuperAdminRemainsAsync(tx, current, next, id)
    await ensureSystemAccountDisplayNameUniqueAsync(tx, next.displayName, id)
    if (passwordHash) {
      await tx.execute(`
        UPDATE ${systemAccountTable(tx, 'system_accounts')}
        SET display_name = ?, description = ?, role = ?, status = ?, password_hash = ?, must_change_password = ?, image_generation_enabled = ?, updated_at = ?
        WHERE id = ?
      `, [next.displayName, next.description ?? null, next.role, next.status, passwordHash, next.mustChangePassword ? 1 : 0, next.imageGenerationEnabled ? 1 : 0, now, id])
    } else {
      await tx.execute(`
        UPDATE ${systemAccountTable(tx, 'system_accounts')}
        SET display_name = ?, description = ?, role = ?, status = ?, must_change_password = ?, image_generation_enabled = ?, updated_at = ?
        WHERE id = ?
      `, [next.displayName, next.description ?? null, next.role, next.status, next.mustChangePassword ? 1 : 0, next.imageGenerationEnabled ? 1 : 0, now, id])
    }
    updated = { ...next, updatedAt: now }
  })
  if (!updated) {
    return undefined
  }
  invalidateSystemAccountLookupCache(id)
  if (current && (updated.status !== current.status || updated.imageGenerationEnabled !== current.imageGenerationEnabled)) {
    clearGatewayApiKeyValidationCache()
    notifyGatewayRuntimeCacheInvalidation(updated.status !== current.status ? 'system_account_status_changed' : 'system_account_image_generation_changed')
  }
  return updated
}

export function updateSystemAccountLastLogin(id: string): void {
  getBusinessDatabase()
    .prepare('UPDATE system_accounts SET last_login_at = ?, updated_at = ? WHERE id = ?')
    .run(nowIso(), nowIso(), id)
}

export async function updateSystemAccountLastLoginAsync(id: string): Promise<void> {
  const client = await getSystemAccountDatabaseClient()
  const now = nowIso()
  await client.execute(`
    UPDATE ${systemAccountTable(client, 'system_accounts')}
    SET last_login_at = ?, updated_at = ?
    WHERE id = ?
  `, [now, now, id])
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

export async function createSessionAsync(systemAccountId: string, ttlDays = 14): Promise<{ token: string; sessionId: string; expiresAt: string }> {
  const token = randomBytes(32).toString('base64url')
  const sessionId = newId('sess')
  const now = new Date()
  const expiresAt = new Date(now.getTime() + Math.max(1, ttlDays) * 24 * 60 * 60 * 1000).toISOString()
  const client = await getSystemAccountDatabaseClient()
  await client.execute(`
    INSERT INTO ${systemAccountTable(client, 'system_sessions')} (id, system_account_id, token_hash, expires_at, created_at, last_seen_at)
    VALUES (?, ?, ?, ?, ?, ?)
  `, [sessionId, systemAccountId, hashSecret(token), expiresAt, now.toISOString(), now.toISOString()])
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

export async function findSessionByTokenAsync(token: string): Promise<(SessionWithAccount & { tokenHash: string }) | undefined> {
  const client = await getSystemAccountDatabaseClient()
  const sessionsTable = systemAccountTable(client, 'system_sessions')
  const accountsTable = systemAccountTable(client, 'system_accounts')
  const row = await client.one<SystemSessionRow & Omit<SystemAccountRow, 'id'> & { account_id: string }>(`
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
    FROM ${sessionsTable} ss
    INNER JOIN ${accountsTable} sa ON sa.id = ss.system_account_id
    WHERE ss.token_hash = ?
  `, [hashSecret(token)])
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

export async function touchSessionAsync(sessionId: string, lastSeenAt?: string): Promise<void> {
  const nowMs = Date.now()
  if (lastSeenAt && Number.isFinite(Date.parse(lastSeenAt)) && nowMs - Date.parse(lastSeenAt) < sessionTouchMinIntervalMs) {
    return
  }

  const cutoff = new Date(nowMs - sessionTouchMinIntervalMs).toISOString()
  const client = await getSystemAccountDatabaseClient()
  await client.execute(`
    UPDATE ${systemAccountTable(client, 'system_sessions')}
    SET last_seen_at = ?
    WHERE id = ? AND last_seen_at < ?
  `, [new Date(nowMs).toISOString(), sessionId, cutoff])
}

export function revokeSession(token: string): void {
  getBusinessDatabase().prepare('DELETE FROM system_sessions WHERE token_hash = ?').run(hashSecret(token))
}

export async function revokeSessionAsync(token: string): Promise<void> {
  const client = await getSystemAccountDatabaseClient()
  await client.execute(`
    DELETE FROM ${systemAccountTable(client, 'system_sessions')}
    WHERE token_hash = ?
  `, [hashSecret(token)])
}

export function revokeAllSessionsForAccount(systemAccountId: string): void {
  getBusinessDatabase().prepare('DELETE FROM system_sessions WHERE system_account_id = ?').run(systemAccountId)
}

export async function revokeAllSessionsForAccountAsync(systemAccountId: string): Promise<void> {
  const client = await getSystemAccountDatabaseClient()
  await client.execute(`
    DELETE FROM ${systemAccountTable(client, 'system_sessions')}
    WHERE system_account_id = ?
  `, [systemAccountId])
}

export function revokeOtherSessionsForAccount(systemAccountId: string, keepSessionId: string): void {
  getBusinessDatabase()
    .prepare('DELETE FROM system_sessions WHERE system_account_id = ? AND id <> ?')
    .run(systemAccountId, keepSessionId)
}

export async function revokeOtherSessionsForAccountAsync(systemAccountId: string, keepSessionId: string): Promise<void> {
  const client = await getSystemAccountDatabaseClient()
  await client.execute(`
    DELETE FROM ${systemAccountTable(client, 'system_sessions')}
    WHERE system_account_id = ? AND id <> ?
  `, [systemAccountId, keepSessionId])
}

async function getSystemAccountDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function systemAccountTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

async function ensureDefaultBuiltInGroupsForSystemAccountAsync(client: DatabaseClient, systemAccountId: string, timestamp = nowIso()): Promise<void> {
  const groupsTable = systemAccountTable(client, 'groups')
  for (const group of DEFAULT_BUILT_IN_GROUPS) {
    const existing = await client.one<{ id?: string }>(`
      SELECT id
      FROM ${groupsTable}
      WHERE system_account_id = ? AND provider_code = ? AND is_default = 1
      ORDER BY updated_at DESC, id ASC
      LIMIT 1
    `, [systemAccountId, group.providerCode])
    if (existing?.id) {
      continue
    }
    await client.execute(`
      INSERT INTO ${groupsTable} (
        id, system_account_id, name, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
        description, enabled, is_default, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?)
    `, [
      newId('grp'),
      systemAccountId,
      group.name,
      group.providerCode,
      group.providerProtocolProfileId,
      group.protocolCode,
      group.protocolVersion,
      group.description,
      timestamp,
      timestamp
    ])
  }
}

function normalizeRequiredText(value: unknown, label: string): string {
  if (typeof value !== 'string') {
    throw new Error(`${label}不能为空`)
  }
  const rawText = value
  const text = rawText.trim()
  if (!text) {
    throw new Error(`${label}不能为空`)
  }
  if (rawText !== text || hasWhitespace(text)) {
    throw new Error(`${label}不能包含空格`)
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
  if (value === 'super_admin' || value === 'admin' || value === 'user') {
    return value
  }
  throw new Error('系统账户角色无效')
}

function ensureActiveSuperAdminRemains(
  current: Pick<SystemAccountSummary, 'role' | 'status'>,
  next: Pick<SystemAccountSummary, 'role' | 'status'>,
  id: string
): void {
  if (!isSuperAdminRole(current.role) || (isSuperAdminRole(next.role) && next.status === 'active')) {
    return
  }

  const row = getBusinessDatabase()
    .prepare("SELECT COUNT(*) AS count FROM system_accounts WHERE id <> ? AND role = 'super_admin' AND status = 'active'")
    .get(id) as unknown as { count?: number } | undefined
  if (Number(row?.count ?? 0) < 1) {
    throw new Error('至少保留一个启用的超级管理员')
  }
}

async function ensureActiveSuperAdminRemainsAsync(
  client: DatabaseClient,
  current: Pick<SystemAccountSummary, 'role' | 'status'>,
  next: Pick<SystemAccountSummary, 'role' | 'status'>,
  id: string
): Promise<void> {
  if (!isSuperAdminRole(current.role) || (isSuperAdminRole(next.role) && next.status === 'active')) {
    return
  }

  const row = await client.one<{ count?: number }>(`
    SELECT COUNT(*) AS count
    FROM ${systemAccountTable(client, 'system_accounts')}
    WHERE id <> ? AND role = 'super_admin' AND status = 'active'
  `, [id])
  if (Number(row?.count ?? 0) < 1) {
    throw new Error('至少保留一个启用的超级管理员')
  }
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

function normalizeSystemAccountMustChangePassword(value: unknown, fallback: boolean, role: SystemAccountRole): boolean {
  return !isAdminRole(role) && normalizeOptionalBoolean(value, fallback, '下次登录改密')
}

function normalizeSystemAccountPassword(value: unknown): string {
  if (typeof value !== 'string' || value.length < 4) {
    throw new Error('登录密码不能少于 4 个字符')
  }
  if (hasWhitespace(value)) {
    throw new Error('登录密码不能包含空格')
  }
  return value
}

function hasWhitespace(value: string): boolean {
  return whitespacePattern.test(value)
}

function assertKnownInputKeys(input: object, allowedKeys: ReadonlySet<string>, label: string): void {
  const unknownKeys = Object.keys(input as Record<string, unknown>).filter((key) => !allowedKeys.has(key))
  if (unknownKeys.length) {
    throw new Error(`${label}包含未知字段：${unknownKeys.join('、')}`)
  }
}
