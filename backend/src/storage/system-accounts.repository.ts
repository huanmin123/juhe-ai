import { randomBytes } from 'node:crypto'

import { isAdminRole, isSuperAdminRole, type SystemAccountListItem, type SystemAccountMutationResult, type SystemAccountOptionSummary, type SystemAccountRole, type SystemAccountStatus, type SystemAccountSummary, type UserRequestLimits } from '../domain/types.js'
import { normalizeUserRequestLimits, parseUserRequestLimitsJson, serializeUserRequestLimits } from '../domain/user-request-limits.js'
import { hashPassword, hashPasswordAsync, hashSecret, verifyPassword, verifyPasswordAsync } from './crypto.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { ensureDefaultBuiltInGroupsForSystemAccount } from './default-group.repository.js'
import { ensureChatApiKeyForSystemAccount, ensureChatApiKeyForSystemAccountAsync, ensureDefaultApiKeysForSystemAccount, ensureDefaultApiKeysForSystemAccountAsync } from './api-key.repository.js'
import { clearGatewayApiKeyValidationCache } from './gateway-api-key.repository.js'
import { getPostgresPool } from './postgres-client.js'
import { ensureDefaultRouteStrategiesForSystemAccount, ensureDefaultRouteStrategiesForSystemAccountAsync } from './route-strategy.repository.js'
import {
  notifyGatewayApiKeyValidationCacheInvalidationAsync,
  notifyGatewayRuntimeCacheInvalidation
} from '../shared/gateway-cache-invalidation.js'
import { runtimeConfig } from '../config/runtime.js'
import { errorLogFields, logger } from '../shared/logger.js'
import { DEFAULT_BUILT_IN_GROUPS } from './schema-defaults.js'
import { normalizeListPage, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { invalidateSystemAccountLookupCache } from './repository-lookups.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import { systemAccountListItemFromRow, systemAccountOptionSummaryFromRow, systemAccountSummaryFromRow, type SystemAccountRow, type SystemAccountSummaryRow } from './system-account-mappers.js'
import { optionalString } from './value-utils.js'
import { temporaryAccessTokenPrefix } from '../modules/auth/temporary-access-token.js'

interface SystemSessionRow {
  id: string
  system_account_id: string
  token_hash: string
  expires_at: string
  created_at: string
  last_seen_at: string
}

type SystemSessionAccountRow = SystemSessionRow & Omit<SystemAccountRow, 'id'> & { account_id: string }

const sessionTouchMinIntervalMs = 60 * 1000
const defaultSystemAccountOptionLimit = 50
const defaultSystemAccountPageSize = 20
const maxSystemAccountPageSize = 100
const businessSchemaName = 'juhe_business'
const whitespacePattern = /\s/
const systemAccountCreateInputKeys = new Set(['username', 'displayName', 'description', 'password', 'role', 'status', 'mustChangePassword', 'imageGenerationEnabled', 'requestLimits'])
const systemAccountUpdateInputKeys = new Set(['displayName', 'description', 'password', 'role', 'status', 'mustChangePassword', 'imageGenerationEnabled', 'requestLimits'])
const systemAccountManagementPatchInputKeys = new Set(['displayName', 'description', 'password', 'role', 'status', 'mustChangePassword', 'imageGenerationEnabled', 'requestLimits'])

export type SystemAccountManagementPatchField = 'displayName'
  | 'description'
  | 'password'
  | 'role'
  | 'status'
  | 'mustChangePassword'
  | 'imageGenerationEnabled'
  | 'requestLimits'

export interface SystemAccountManagementPatchChange {
  field: SystemAccountManagementPatchField
  before: unknown
  after: unknown
}

export interface SystemAccountManagementPatchOutcome {
  kind: 'no_op' | 'updated'
  result: SystemAccountMutationResult
  resourceName: string
  changes: SystemAccountManagementPatchChange[]
}

export class SystemAccountManagementPatchConflictError extends Error {
  constructor(readonly currentUpdatedAt?: string) {
    super('系统账户已被其他操作修改，请刷新后重试')
    this.name = 'SystemAccountManagementPatchConflictError'
  }
}

interface SystemAccountManagementPatchRow {
  id: string
  updated_at: string
  display_name: string
  description?: string | null
  role?: SystemAccountRole
  status?: SystemAccountStatus
  must_change_password?: number | boolean
  image_generation_enabled?: number | boolean
  request_limits_json?: string | null
  password_hash?: string
}

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
  items: SystemAccountListItem[]
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

export interface VerifiedSystemAccountCredentials {
  account: SystemAccountSummary
  credentialRevision: string
}

export function listSystemAccounts(): SystemAccountListItem[] {
  return listSystemAccountsPage({ page: 1, pageSize: maxSystemAccountPageSize }).items
}

export async function listSystemAccountsAsync(): Promise<SystemAccountListItem[]> {
  return (await listSystemAccountsPageAsync({ page: 1, pageSize: maxSystemAccountPageSize })).items
}

export function listSystemAccountsPage(options: SystemAccountListOptions = {}): SystemAccountListResult {
  return listSystemAccountsPageReadOnly(options)
}

export function listSystemAccountsPageReadOnly(options: SystemAccountListOptions = {}): SystemAccountListResult {
  const normalized = normalizeSystemAccountListOptions(options)
  const keywordFilter = buildSystemAccountListKeywordFilter(normalized.keyword)
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT id, username, display_name, description, role, status, must_change_password, image_generation_enabled, request_limits_json, last_login_at, updated_at
      FROM system_accounts
      ${keywordFilter.clause}
      ORDER BY updated_at DESC, id DESC
      LIMIT ? OFFSET ?
    `)
    .all(...keywordFilter.params, normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize) as unknown as Array<Omit<SystemAccountSummaryRow, 'created_at'>>
  const pageRows = takePageRows(rows, normalized.pageSize)
  const items = pageRows.rows.map(systemAccountListItemFromRow)
  return {
    items,
    total: pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

export async function listSystemAccountsPageAsync(options: SystemAccountListOptions = {}): Promise<SystemAccountListResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'list_system_accounts_page_read_only',
        options
      })
    }
    return listSystemAccountsPageReadOnly(options)
  }
  const normalized = normalizeSystemAccountListOptions(options)
  const client = await getSystemAccountDatabaseClient()
  const keywordFilter = buildSystemAccountListKeywordFilterForClient(client, normalized.keyword)
  const rows = await client.query<Omit<SystemAccountSummaryRow, 'created_at'>>(`
    SELECT id, username, display_name, description, role, status, must_change_password, image_generation_enabled, request_limits_json, last_login_at,
      updated_at
    FROM ${systemAccountTable(client, 'system_accounts')}
    ${keywordFilter.clause}
    ORDER BY updated_at DESC, id DESC
    LIMIT ? OFFSET ?
  `, [...keywordFilter.params, normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize])
  const pageRows = takePageRows(rows, normalized.pageSize)
  const items = pageRows.rows.map(systemAccountListItemFromRow)
  return {
    items,
    total: pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

export function listSystemAccountOptions(options: SystemAccountOptionListOptions = {}): SystemAccountOptionSummary[] {
  return listSystemAccountOptionsReadOnly(options)
}

export function listSystemAccountOptionsReadOnly(options: SystemAccountOptionListOptions = {}): SystemAccountOptionSummary[] {
  const optionFilter = buildSystemAccountOptionFilter(options)
  const limitClause = systemAccountOptionLimitClause(options.limit)
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT id, display_name, status
      FROM system_accounts
      ${optionFilter.clause}
      ORDER BY status ASC, display_name ASC, username ASC, id ASC
      ${limitClause.clause}
    `)
    .all(...optionFilter.params, ...limitClause.params) as unknown as Array<Pick<SystemAccountRow, 'id' | 'display_name' | 'status'>>
  return rows.map(systemAccountOptionSummaryFromRow)
}

export async function listSystemAccountOptionsAsync(options: SystemAccountOptionListOptions = {}): Promise<SystemAccountOptionSummary[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'list_system_account_options_read_only',
        options
      })
    }
    return listSystemAccountOptionsReadOnly(options)
  }
  const client = await getSystemAccountDatabaseClient()
  const optionFilter = buildSystemAccountOptionFilterForClient(client, options)
  const limitClause = systemAccountOptionLimitClause(options.limit)
  const rows = await client.query<Pick<SystemAccountRow, 'id' | 'display_name' | 'status'>>(`
    SELECT id, display_name, status
    FROM ${systemAccountTable(client, 'system_accounts')}
    ${optionFilter.clause}
    ORDER BY status ASC, display_name ASC, username ASC, id ASC
    ${limitClause.clause}
  `, [...optionFilter.params, ...limitClause.params])
  return rows.map(systemAccountOptionSummaryFromRow)
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
      SELECT id, username, display_name, description, role, status, must_change_password, image_generation_enabled, request_limits_json, last_login_at, created_at, updated_at
      FROM system_accounts
      WHERE id = ?
    `)
    .get(id) as unknown as SystemAccountSummaryRow | undefined
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
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'find_system_account_by_id_read_only',
      id
    })
  }
  const client = await getSystemAccountDatabaseClient()
  return findSystemAccountByIdWithClient(client, id)
}

async function findSystemAccountByIdWithClient(client: DatabaseClient, id: string, lockRow = false): Promise<SystemAccountSummary | undefined> {
  const row = await client.one<SystemAccountSummaryRow>(`
    SELECT id, username, display_name, description, role, status, must_change_password, image_generation_enabled, request_limits_json, last_login_at, created_at, updated_at
    FROM ${systemAccountTable(client, 'system_accounts')}
    WHERE id = ?${lockRow && client.driver === 'postgres' ? ' FOR UPDATE' : ''}
  `, [id])
  return row ? systemAccountSummaryFromRow(row) : undefined
}

export function findSystemAccountByUsername(username: string): (SystemAccountSummary & { passwordHash: string }) | undefined {
  const row = getBusinessDatabase().prepare(`
    SELECT id, username, display_name, description, role, status, password_hash, must_change_password, image_generation_enabled, request_limits_json, last_login_at, created_at, updated_at
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
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'find_system_account_by_username_read_only',
      username
    })
  }
  const client = await getSystemAccountDatabaseClient()
  return findSystemAccountByUsernameInClientAsync(client, username)
}

export async function findSystemAccountByUsernameInClientAsync(
  client: DatabaseClient,
  username: string
): Promise<(SystemAccountSummary & { passwordHash: string }) | undefined> {
  const row = await client.one<SystemAccountRow>(`
    SELECT id, username, display_name, description, role, status, password_hash, must_change_password, image_generation_enabled, request_limits_json, last_login_at, created_at, updated_at
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
  return (await verifySystemAccountCredentialsForSessionAsync(username, password))?.account
}

export async function verifySystemAccountCredentialsForSessionAsync(
  username: string,
  password: string
): Promise<VerifiedSystemAccountCredentials | undefined> {
  const account = await findSystemAccountByUsernameAsync(username)
  if (!account || account.status !== 'active') {
    return undefined
  }
  if (!await verifyPasswordAsync(password, account.passwordHash)) {
    return undefined
  }
  const { passwordHash, ...summary } = account
  return {
    account: summary,
    credentialRevision: hashSecret(passwordHash)
  }
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
  requestLimits?: UserRequestLimits | null
}): SystemAccountSummary {
  const password = normalizeSystemAccountPassword(input.password)
  return createSystemAccountWithPasswordHash(input, hashPassword(password))
}

export type SystemAccountWithPasswordHashInput = {
  username: string
  displayName: string
  description?: string | null
  password?: string
  role?: SystemAccountRole
  status?: SystemAccountStatus
  mustChangePassword?: boolean
  imageGenerationEnabled?: boolean
  requestLimits?: UserRequestLimits | null
}

export async function createSystemAccountAsync(input: SystemAccountWithPasswordHashInput & { password: string }): Promise<SystemAccountSummary> {
  const passwordHash = await hashPasswordAsync(normalizeSystemAccountPassword(input.password))
  return createSystemAccountWithPasswordHashAsync(input, passwordHash)
}

export function createSystemAccountWithPasswordHash(input: SystemAccountWithPasswordHashInput, passwordHash: string): SystemAccountSummary {
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
    requestLimits: normalizeUserRequestLimits(input.requestLimits),
    createdAt: now,
    updatedAt: now
  }
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database
      .prepare(`
        INSERT INTO system_accounts (
          id, username, display_name, description, role, status, password_hash, must_change_password, image_generation_enabled, request_limits_json, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
      .run(summary.id, summary.username, summary.displayName, summary.description ?? null, summary.role, summary.status, passwordHash, summary.mustChangePassword ? 1 : 0, summary.imageGenerationEnabled ? 1 : 0, serializeUserRequestLimits(summary.requestLimits), now, now)
    ensureDefaultBuiltInGroupsForSystemAccount(summary.id, now)
    ensureDefaultRouteStrategiesForSystemAccount(summary.id, now)
    ensureDefaultApiKeysForSystemAccount(summary.id, now)
    ensureChatApiKeyForSystemAccount(summary.id, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  invalidateSystemAccountLookupCache(summary.id)
  return summary
}

export async function createSystemAccountWithPasswordHashAsync(input: SystemAccountWithPasswordHashInput, passwordHash: string): Promise<SystemAccountSummary> {
  const client = await getSystemAccountDatabaseClient()
  const summary = await client.transaction(async (tx) => createSystemAccountWithPasswordHashInClientAsync(tx, input, passwordHash))
  invalidateSystemAccountLookupCache(summary.id)
  return summary
}

export async function createSystemAccountWithPasswordHashInClientAsync(
  client: DatabaseClient,
  input: SystemAccountWithPasswordHashInput,
  passwordHash: string
): Promise<SystemAccountSummary> {
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
    requestLimits: normalizeUserRequestLimits(input.requestLimits),
    createdAt: now,
    updatedAt: now
  }
  await ensureSystemAccountUsernameUniqueAsync(client, username)
  await ensureSystemAccountDisplayNameUniqueAsync(client, displayName)
  await client.execute(`
      INSERT INTO ${systemAccountTable(client, 'system_accounts')} (
        id, username, display_name, description, role, status, password_hash, must_change_password, image_generation_enabled, request_limits_json, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, [summary.id, summary.username, summary.displayName, summary.description ?? null, summary.role, summary.status, passwordHash, summary.mustChangePassword ? 1 : 0, summary.imageGenerationEnabled ? 1 : 0, serializeUserRequestLimits(summary.requestLimits), now, now])
  await ensureDefaultBuiltInGroupsForSystemAccountAsync(client, summary.id, now)
  await ensureDefaultRouteStrategiesForSystemAccountAsync(client, summary.id, now)
  await ensureDefaultApiKeysForSystemAccountAsync(client, summary.id, now)
  await ensureChatApiKeyForSystemAccountAsync(summary.id, now, client)
  return summary
}

export async function patchSystemAccountManagementAsync(
  id: string,
  input: Record<string, unknown>,
  expectedUpdatedAt: string,
  passwordHash?: string
): Promise<SystemAccountManagementPatchOutcome | undefined> {
  assertKnownInputKeys(input, systemAccountManagementPatchInputKeys, '系统账户更新参数')
  if (Object.hasOwn(input, 'password') && typeof input.password === 'string') {
    normalizeSystemAccountPassword(input.password)
  }
  if (Object.hasOwn(input, 'password') && !passwordHash) {
    throw new Error('登录密码不能为空')
  }

  const client = await getSystemAccountDatabaseClient()
  const outcome = await client.transaction(async (tx) => {
    await lockActiveSuperAdminInvariantForPatchAsync(tx, input)
    const lockClause = tx.driver === 'postgres' ? ' FOR UPDATE' : ''
    const current = await tx.one<SystemAccountManagementPatchRow>(`
      SELECT ${systemAccountManagementPatchSelectColumns(input).join(', ')}
      FROM ${systemAccountTable(tx, 'system_accounts')}
      WHERE id = ?
      LIMIT 1${lockClause}
    `, [id])
    if (!current) return undefined
    if (!systemAccountRevisionsEqual(current.updated_at, expectedUpdatedAt)) {
      throw new SystemAccountManagementPatchConflictError(current.updated_at)
    }

    const assignments: string[] = []
    const params: unknown[] = []
    const changes: SystemAccountManagementPatchChange[] = []
    const result: SystemAccountMutationResult = { id, updatedAt: current.updated_at }
    const roleRelevant = Object.hasOwn(input, 'role') || Object.hasOwn(input, 'status') || Object.hasOwn(input, 'mustChangePassword')
    const currentRole = roleRelevant
      ? requiredSystemAccountPatchValue(current, 'role', input, ['role', 'status', 'mustChangePassword'])
      : undefined
    const nextRole = Object.hasOwn(input, 'role') && currentRole
      ? normalizeSystemAccountRole(input.role, currentRole)
      : currentRole

    if (Object.hasOwn(input, 'displayName')) {
      const nextValue = normalizeRequiredText(input.displayName, '用户名称')
      if (nextValue !== current.display_name) {
        await ensureSystemAccountDisplayNameUniqueAsync(tx, nextValue, id)
        appendSystemAccountPatchChange(assignments, params, changes, 'displayName', 'display_name', current.display_name, nextValue, nextValue)
        result.displayName = nextValue
      }
    }
    if (Object.hasOwn(input, 'description')) {
      const currentValue = current.description ?? null
      const nextValue = normalizeNullableText(input.description, '说明')
      if (nextValue !== currentValue) {
        appendSystemAccountPatchChange(assignments, params, changes, 'description', 'description', currentValue, nextValue, nextValue)
        result.description = nextValue
      }
    }
    if (Object.hasOwn(input, 'role') && currentRole && nextRole && nextRole !== currentRole) {
      appendSystemAccountPatchChange(assignments, params, changes, 'role', 'role', currentRole, nextRole, nextRole)
      result.role = nextRole
    }

    const statusRelevant = Object.hasOwn(input, 'role') || Object.hasOwn(input, 'status')
    const currentStatus = statusRelevant
      ? requiredSystemAccountPatchValue(current, 'status', input, ['role', 'status'])
      : undefined
    const nextStatus = Object.hasOwn(input, 'status') && currentStatus
      ? normalizeSystemAccountStatus(input.status, currentStatus)
      : currentStatus
    if (Object.hasOwn(input, 'status') && currentStatus && nextStatus && nextStatus !== currentStatus) {
      appendSystemAccountPatchChange(assignments, params, changes, 'status', 'status', currentStatus, nextStatus, nextStatus)
      result.status = nextStatus
    }
    if ((Object.hasOwn(input, 'role') || Object.hasOwn(input, 'status')) && currentRole && nextRole && currentStatus && nextStatus) {
      await ensureActiveSuperAdminRemainsAsync(tx, { role: currentRole, status: currentStatus }, { role: nextRole, status: nextStatus }, id)
    }

    if (Object.hasOwn(input, 'mustChangePassword') || Object.hasOwn(input, 'role')) {
      if (!currentRole || !nextRole) throw new Error('系统账户 PATCH 内部投影缺少角色')
      const currentValue = systemAccountPatchBoolean(requiredSystemAccountPatchValue(current, 'must_change_password', input, ['mustChangePassword', 'role'])) && !isAdminRole(currentRole)
      const nextValue = normalizeSystemAccountMustChangePassword(
        Object.hasOwn(input, 'mustChangePassword') ? input.mustChangePassword : currentValue,
        currentValue,
        nextRole
      )
      if (nextValue !== currentValue) {
        appendSystemAccountPatchChange(assignments, params, changes, 'mustChangePassword', 'must_change_password', currentValue, nextValue, nextValue ? 1 : 0)
        result.mustChangePassword = nextValue
      }
    }
    if (Object.hasOwn(input, 'imageGenerationEnabled')) {
      const currentValue = systemAccountPatchBoolean(requiredSystemAccountPatchValue(current, 'image_generation_enabled', input, ['imageGenerationEnabled']))
      const nextValue = normalizeOptionalBoolean(input.imageGenerationEnabled, currentValue, '支持图像生成')
      if (nextValue !== currentValue) {
        appendSystemAccountPatchChange(assignments, params, changes, 'imageGenerationEnabled', 'image_generation_enabled', currentValue, nextValue, nextValue ? 1 : 0)
        result.imageGenerationEnabled = nextValue
      }
    }
    if (Object.hasOwn(input, 'requestLimits')) {
      const currentValue = parseUserRequestLimitsJson(requiredSystemAccountPatchValue(current, 'request_limits_json', input, ['requestLimits']))
      const nextValue = normalizeUserRequestLimits(input.requestLimits)
      const currentSerialized = serializeUserRequestLimits(currentValue)
      const nextSerialized = serializeUserRequestLimits(nextValue)
      if (nextSerialized !== currentSerialized) {
        appendSystemAccountPatchChange(assignments, params, changes, 'requestLimits', 'request_limits_json', currentValue, nextValue, nextSerialized)
        result.requestLimits = nextValue ?? null
      }
    }
    if (Object.hasOwn(input, 'password')) {
      const currentPasswordHash = requiredSystemAccountPatchValue(current, 'password_hash', input, ['password'])
      const password = typeof input.password === 'string' ? input.password : ''
      if (!await verifyPasswordAsync(password, currentPasswordHash)) {
        assignments.push('password_hash = ?')
        params.push(passwordHash)
        changes.push({ field: 'password', before: undefined, after: true })
      }
    }

    if (!assignments.length) {
      return {
        kind: 'no_op' as const,
        result,
        resourceName: current.display_name,
        changes
      }
    }

    const updatedAt = nextSystemAccountUpdatedAt(current.updated_at)
    const applied = await tx.execute(`
      UPDATE ${systemAccountTable(tx, 'system_accounts')}
      SET ${assignments.join(', ')}, updated_at = ?
      WHERE id = ? AND ${systemAccountPatchRevisionPredicate()}
    `, [...params, updatedAt, id, current.updated_at])
    if (applied.changes !== 1) {
      throw new SystemAccountManagementPatchConflictError()
    }
    if (systemAccountPatchRevokesSessions(changes)) {
      await tx.execute(`
        DELETE FROM ${systemAccountTable(tx, 'system_sessions')}
        WHERE system_account_id = ?
      `, [id])
    }
    result.updatedAt = updatedAt
    return {
      kind: 'updated' as const,
      result,
      resourceName: result.displayName ?? current.display_name,
      changes
    }
  })

  if (!outcome || outcome.kind === 'no_op') return outcome
  if (outcome.changes.some((change) => change.field === 'displayName')) {
    invalidateSystemAccountLookupCache(id)
  }
  const runtimeReason = systemAccountManagementRuntimeInvalidationReason(outcome.changes)
  if (runtimeReason) {
    notifyGatewayRuntimeCacheInvalidation(runtimeReason)
    try {
      await notifyGatewayApiKeyValidationCacheInvalidationAsync(undefined, runtimeReason)
    } catch (error) {
      logger.error(errorLogFields(error, {
        event: 'system_account_management_patch_api_key_validation_cache_invalidation_failed',
        systemAccountId: id,
        reason: runtimeReason
      }), '系统账户已提交，API Key validation cache 失效失败')
      outcome.result.apiKeyValidationCacheInvalidationFailed = true
    }
  }
  return outcome
}

function systemAccountManagementPatchSelectColumns(input: Record<string, unknown>): string[] {
  const columns = new Set([
    'id',
    'updated_at',
    'display_name'
  ])
  if (Object.hasOwn(input, 'description')) columns.add('description')
  if (Object.hasOwn(input, 'role') || Object.hasOwn(input, 'status') || Object.hasOwn(input, 'mustChangePassword')) columns.add('role')
  if (Object.hasOwn(input, 'role') || Object.hasOwn(input, 'status')) columns.add('status')
  if (Object.hasOwn(input, 'role') || Object.hasOwn(input, 'mustChangePassword')) columns.add('must_change_password')
  if (Object.hasOwn(input, 'imageGenerationEnabled')) columns.add('image_generation_enabled')
  if (Object.hasOwn(input, 'requestLimits')) columns.add('request_limits_json')
  if (Object.hasOwn(input, 'password')) columns.add('password_hash')
  return [...columns]
}

function systemAccountPatchRevisionPredicate(): string {
  return 'updated_at = ?'
}

function requiredSystemAccountPatchValue<K extends keyof SystemAccountManagementPatchRow>(
  row: SystemAccountManagementPatchRow,
  key: K,
  input: Record<string, unknown>,
  requiredFor: SystemAccountManagementPatchField[]
): Exclude<SystemAccountManagementPatchRow[K], undefined> {
  if (!Object.hasOwn(row, key)) {
    const submitted = requiredFor.find((field) => Object.hasOwn(input, field)) ?? String(key)
    throw new Error(`系统账户 PATCH 内部投影缺少字段：${submitted}`)
  }
  return row[key] as Exclude<SystemAccountManagementPatchRow[K], undefined>
}

function appendSystemAccountPatchChange(
  assignments: string[],
  params: unknown[],
  changes: SystemAccountManagementPatchChange[],
  field: SystemAccountManagementPatchField,
  column: string,
  before: unknown,
  after: unknown,
  databaseValue: unknown
): void {
  assignments.push(`${column} = ?`)
  params.push(databaseValue)
  changes.push({ field, before, after })
}

function systemAccountPatchBoolean(value: unknown): boolean {
  return value === true || value === 1 || value === '1' || value === 'true'
}

function nextSystemAccountUpdatedAt(currentUpdatedAt: string): string {
  const now = nowIso()
  const nowMs = Date.parse(now)
  const currentMs = Date.parse(currentUpdatedAt)
  if (!Number.isFinite(currentMs) || nowMs > currentMs) return now
  return new Date(currentMs + 1).toISOString()
}

function systemAccountRevisionsEqual(left: string, right: string): boolean {
  return normalizedSystemAccountRevisionToken(left) === normalizedSystemAccountRevisionToken(right)
}

function normalizedSystemAccountRevisionToken(value: string): string {
  const utcMatch = /^(.*?)(?:\.(\d+))?Z$/i.exec(value)
  if (!utcMatch) return value
  const fraction = (utcMatch[2] ?? '').replace(/0+$/, '')
  return `${utcMatch[1]}${fraction ? `.${fraction}` : ''}Z`
}

function systemAccountManagementRuntimeInvalidationReason(changes: SystemAccountManagementPatchChange[]): string | undefined {
  if (changes.some((change) => change.field === 'status')) return 'system_account_status_changed'
  if (changes.some((change) => change.field === 'imageGenerationEnabled')) return 'system_account_image_generation_changed'
  if (changes.some((change) => change.field === 'requestLimits')) return 'system_account_request_limits_changed'
  return undefined
}

function systemAccountPatchRevokesSessions(changes: SystemAccountManagementPatchChange[]): boolean {
  return changes.some((change) => change.field === 'password'
    || (change.field === 'status' && change.after === 'disabled'))
}

export function updateSystemAccount(id: string, input: {
  displayName?: string
  description?: string | null
  role?: SystemAccountRole
  status?: SystemAccountStatus
  mustChangePassword?: boolean
  imageGenerationEnabled?: boolean
  requestLimits?: UserRequestLimits | null
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
  requestLimits?: UserRequestLimits | null
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
  requestLimits?: UserRequestLimits | null
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
    imageGenerationEnabled: normalizeOptionalBoolean(input.imageGenerationEnabled, current.imageGenerationEnabled, '支持图像生成'),
    requestLimits: Object.prototype.hasOwnProperty.call(input, 'requestLimits') ? normalizeUserRequestLimits(input.requestLimits) : current.requestLimits
  }
  const now = nowIso()
  ensureActiveSuperAdminRemains(current, next, id)
  ensureSystemAccountDisplayNameUnique(next.displayName, id)
  if (passwordHash) {
    getBusinessDatabase()
      .prepare(`
        UPDATE system_accounts
        SET display_name = ?, description = ?, role = ?, status = ?, password_hash = ?, must_change_password = ?, image_generation_enabled = ?, request_limits_json = ?, updated_at = ?
        WHERE id = ?
      `)
      .run(next.displayName, next.description ?? null, next.role, next.status, passwordHash, next.mustChangePassword ? 1 : 0, next.imageGenerationEnabled ? 1 : 0, serializeUserRequestLimits(next.requestLimits), now, id)
  } else {
    getBusinessDatabase()
      .prepare(`
        UPDATE system_accounts
        SET display_name = ?, description = ?, role = ?, status = ?, must_change_password = ?, image_generation_enabled = ?, request_limits_json = ?, updated_at = ?
        WHERE id = ?
      `)
      .run(next.displayName, next.description ?? null, next.role, next.status, next.mustChangePassword ? 1 : 0, next.imageGenerationEnabled ? 1 : 0, serializeUserRequestLimits(next.requestLimits), now, id)
  }
  invalidateSystemAccountLookupCache(id)
  if (gatewayAccountRuntimeChanged(current, next)) {
    clearGatewayApiKeyValidationCache()
    notifyGatewayRuntimeCacheInvalidation(gatewayAccountRuntimeInvalidationReason(current, next))
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
  requestLimits?: UserRequestLimits | null
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
    await lockActiveSuperAdminInvariantForPatchAsync(tx, input)
    current = await findSystemAccountByIdWithClient(tx, id, true)
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
      imageGenerationEnabled: normalizeOptionalBoolean(input.imageGenerationEnabled, current.imageGenerationEnabled, '支持图像生成'),
      requestLimits: Object.prototype.hasOwnProperty.call(input, 'requestLimits') ? normalizeUserRequestLimits(input.requestLimits) : current.requestLimits
    }
    const now = nowIso()
    await ensureActiveSuperAdminRemainsAsync(tx, current, next, id)
    await ensureSystemAccountDisplayNameUniqueAsync(tx, next.displayName, id)
    if (passwordHash) {
      await tx.execute(`
        UPDATE ${systemAccountTable(tx, 'system_accounts')}
        SET display_name = ?, description = ?, role = ?, status = ?, password_hash = ?, must_change_password = ?, image_generation_enabled = ?, request_limits_json = ?, updated_at = ?
        WHERE id = ?
      `, [next.displayName, next.description ?? null, next.role, next.status, passwordHash, next.mustChangePassword ? 1 : 0, next.imageGenerationEnabled ? 1 : 0, serializeUserRequestLimits(next.requestLimits), now, id])
    } else {
      await tx.execute(`
        UPDATE ${systemAccountTable(tx, 'system_accounts')}
        SET display_name = ?, description = ?, role = ?, status = ?, must_change_password = ?, image_generation_enabled = ?, request_limits_json = ?, updated_at = ?
        WHERE id = ?
      `, [next.displayName, next.description ?? null, next.role, next.status, next.mustChangePassword ? 1 : 0, next.imageGenerationEnabled ? 1 : 0, serializeUserRequestLimits(next.requestLimits), now, id])
    }
    updated = { ...next, updatedAt: now }
  })
  if (!updated) {
    return undefined
  }
  invalidateSystemAccountLookupCache(id)
  if (current && gatewayAccountRuntimeChanged(current, updated)) {
    const reason = gatewayAccountRuntimeInvalidationReason(current, updated)
    notifyGatewayRuntimeCacheInvalidation(reason)
    await notifyGatewayApiKeyValidationCacheInvalidationAsync(undefined, reason)
  }
  return updated
}

export function updateSystemAccountLastLogin(id: string): void {
  getBusinessDatabase()
    .prepare('UPDATE system_accounts SET last_login_at = ? WHERE id = ?')
    .run(nowIso(), id)
}

export async function updateSystemAccountLastLoginAsync(id: string): Promise<void> {
  const client = await getSystemAccountDatabaseClient()
  const now = nowIso()
  await client.execute(`
    UPDATE ${systemAccountTable(client, 'system_accounts')}
    SET last_login_at = ?
    WHERE id = ?
  `, [now, id])
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

export async function createTemporaryAccessTokenAsync(
  systemAccountId: string,
  credentialRevision: string,
  ttlSeconds: number
): Promise<{ token: string; sessionId: string; expiresAt: string } | undefined> {
  const token = `${temporaryAccessTokenPrefix}${randomBytes(32).toString('base64url')}`
  const sessionId = newId('tmp_sess')
  const now = new Date()
  const nowText = now.toISOString()
  const expiresAt = new Date(now.getTime() + Math.max(1, Math.trunc(ttlSeconds)) * 1000).toISOString()
  const client = await getSystemAccountDatabaseClient()
  return client.transaction(async (tx) => {
    const lockClause = tx.driver === 'postgres' ? ' FOR UPDATE' : ''
    const account = await tx.one<Pick<SystemAccountRow, 'status' | 'password_hash'>>(`
      SELECT status, password_hash
      FROM ${systemAccountTable(tx, 'system_accounts')}
      WHERE id = ?
      LIMIT 1${lockClause}
    `, [systemAccountId])
    if (!account || account.status !== 'active' || hashSecret(account.password_hash) !== credentialRevision) {
      return undefined
    }
    await tx.execute(`
      INSERT INTO ${systemAccountTable(tx, 'system_sessions')} (id, system_account_id, token_hash, expires_at, created_at, last_seen_at)
      VALUES (?, ?, ?, ?, ?, ?)
    `, [sessionId, systemAccountId, hashSecret(token), expiresAt, nowText, nowText])
    await tx.execute(`
      UPDATE ${systemAccountTable(tx, 'system_accounts')}
      SET last_login_at = ?
      WHERE id = ?
    `, [nowText, systemAccountId])
    return { token, sessionId, expiresAt }
  })
}

export async function createAuthenticatedSessionAsync(
  systemAccountId: string,
  credentialRevision: string,
  ttlDays = 14
): Promise<{ token: string; sessionId: string; expiresAt: string } | undefined> {
  const token = randomBytes(32).toString('base64url')
  const sessionId = newId('sess')
  const now = new Date()
  const nowText = now.toISOString()
  const expiresAt = new Date(now.getTime() + Math.max(1, ttlDays) * 24 * 60 * 60 * 1000).toISOString()
  const client = await getSystemAccountDatabaseClient()
  return client.transaction(async (tx) => {
    const lockClause = tx.driver === 'postgres' ? ' FOR UPDATE' : ''
    const account = await tx.one<Pick<SystemAccountRow, 'status' | 'password_hash'>>(`
      SELECT status, password_hash
      FROM ${systemAccountTable(tx, 'system_accounts')}
      WHERE id = ?
      LIMIT 1${lockClause}
    `, [systemAccountId])
    if (!account || account.status !== 'active' || hashSecret(account.password_hash) !== credentialRevision) {
      return undefined
    }
    await tx.execute(`
      INSERT INTO ${systemAccountTable(tx, 'system_sessions')} (id, system_account_id, token_hash, expires_at, created_at, last_seen_at)
      VALUES (?, ?, ?, ?, ?, ?)
    `, [sessionId, systemAccountId, hashSecret(token), expiresAt, nowText, nowText])
    await tx.execute(`
      UPDATE ${systemAccountTable(tx, 'system_accounts')}
      SET last_login_at = ?
      WHERE id = ?
    `, [nowText, systemAccountId])
    return { token, sessionId, expiresAt }
  })
}

export function findSessionByToken(token: string): (SessionWithAccount & { tokenHash: string }) | undefined {
  return findSessionByTokenReadOnly(hashSecret(token))
}

export function findSessionByTokenReadOnly(tokenHash: string): (SessionWithAccount & { tokenHash: string }) | undefined {
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
        sa.request_limits_json,
        sa.last_login_at,
        sa.created_at,
        sa.updated_at
      FROM system_sessions ss
      INNER JOIN system_accounts sa ON sa.id = ss.system_account_id
      WHERE ss.token_hash = ?
    `)
    .get(tokenHash) as unknown as SystemSessionAccountRow | undefined
  return sessionWithAccountFromRow(row)
}

export async function findSessionByTokenAsync(token: string): Promise<(SessionWithAccount & { tokenHash: string }) | undefined> {
  const tokenHash = hashSecret(token)
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return await requestSqliteReadWorker({
        type: 'find_session_by_token_read_only',
        tokenHash
      })
    }
    return findSessionByTokenReadOnly(tokenHash)
  }
  const client = await getSystemAccountDatabaseClient()
  const sessionsTable = systemAccountTable(client, 'system_sessions')
  const accountsTable = systemAccountTable(client, 'system_accounts')
  const row = await client.one<SystemSessionAccountRow>(`
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
      sa.request_limits_json,
      sa.last_login_at,
      sa.created_at,
      sa.updated_at
    FROM ${sessionsTable} ss
    INNER JOIN ${accountsTable} sa ON sa.id = ss.system_account_id
    WHERE ss.token_hash = ?
  `, [tokenHash])
  return sessionWithAccountFromRow(row)
}

function sessionWithAccountFromRow(row: SystemSessionAccountRow | undefined): (SessionWithAccount & { tokenHash: string }) | undefined {
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
        id, system_account_id, name, provider_code,
        description, enabled, is_default, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?)
    `, [
      newId('grp'),
      systemAccountId,
      group.name,
      group.providerCode,
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

async function lockActiveSuperAdminInvariantForPatchAsync(
  client: DatabaseClient,
  input: Record<string, unknown>
): Promise<void> {
  if (client.driver !== 'postgres' || (!Object.hasOwn(input, 'role') && !Object.hasOwn(input, 'status'))) {
    return
  }
  await client.execute(
    'SELECT pg_advisory_xact_lock(hashtextextended(?, 0))',
    ['juhe-ai:system-accounts:active-super-admin']
  )
}

function gatewayAccountRuntimeChanged(current: SystemAccountSummary, next: SystemAccountSummary): boolean {
  return next.status !== current.status
    || next.imageGenerationEnabled !== current.imageGenerationEnabled
    || serializeUserRequestLimits(next.requestLimits) !== serializeUserRequestLimits(current.requestLimits)
}

function gatewayAccountRuntimeInvalidationReason(current: SystemAccountSummary, next: SystemAccountSummary): string {
  if (next.status !== current.status) return 'system_account_status_changed'
  if (next.imageGenerationEnabled !== current.imageGenerationEnabled) return 'system_account_image_generation_changed'
  return 'system_account_request_limits_changed'
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
