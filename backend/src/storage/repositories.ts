import { randomBytes } from 'node:crypto'
import type { DatabaseSync } from 'node:sqlite'

import type { AccountOAuthUsageSnapshot, AccountOAuthUsageWindow, AccountStatus, AccountSummary, AccountType, ApiKeySummary, ErrorPolicySummary, GroupSummary, ProviderCode, ProviderDefinition } from '../domain/types.js'
import { getRequestAuthContext, type RequestAuthContext } from '../modules/auth/request-context.js'
import { createApiKey, decryptJson, encryptJson, hashPassword, hashSecret, maskSecret, verifyPassword } from './crypto.js'
import { getDatabase, newId, nowIso } from './database.js'

const DEFAULT_ACCOUNT_CONCURRENCY_LIMIT = 20

interface ProviderRow {
  id: string
  code: ProviderCode
  name: string
  enabled: number
  base_url: string
  account_types_json: string
  capabilities_json: string
}

interface AccountRow {
  id: string
  system_account_id: string
  provider_code: ProviderCode
  name: string
  notes: string | null
  type: AccountType
  status: AccountStatus
  credential_mask: string
  credentials_encrypted: string
  proxy_profile_id: string | null
  concurrency_limit: number
  passthrough_enabled: number
  error_policy_id: string | null
  priority: number
  schedulable: number
  account_expires_at: string | null
  last_used_at: string | null
  cooldown_until: string | null
  last_error_message: string | null
  stream_failure_count: number
  stream_failure_window_started_at: string | null
}

interface AccountFailureRow {
  id: string
  stream_failure_count: number
  stream_failure_window_started_at: string | null
}

export class DuplicateAccountCredentialError extends Error {
  constructor() {
    super('账户凭据已被其他账户使用，不能重复添加')
    this.name = 'DuplicateAccountCredentialError'
  }
}

interface GatewayApiKeyRow {
  id: string
  system_account_id: string
  group_id: string
  status: 'active' | 'disabled'
  expires_at: string | null
}

interface GroupAccountRow {
  account_id: string
}

interface GroupRow {
  id: string
  system_account_id: string
  name: string
  provider_code: ProviderCode
  description: string | null
  enabled: number
  is_default: number
}

interface ApiKeyRow {
  id: string
  system_account_id: string
  name: string
  key_prefix: string
  key_secret_encrypted: string | null
  status: 'active' | 'disabled'
  group_id: string
  expires_at: string | null
}

interface ProxyRow {
  id: string
  name: string
  type: string
  host: string
  port: number
  username: string | null
  password_encrypted?: string | null
  enabled: number
  test_status: string
  last_tested_at: string | null
}

interface ErrorPolicyRow {
  id: string
  system_account_id: string
  name: string
  enabled: number
  rules_json: string
}

export type SystemAccountRole = 'admin' | 'user'
export type SystemAccountStatus = 'active' | 'disabled'

const DEFAULT_OPENAI_GROUP_NAME = '默认 OpenAI 分组'
const DEFAULT_OPENAI_GROUP_DESCRIPTION = '第一期默认分组'

export interface SystemAccountSummary {
  id: string
  username: string
  displayName: string
  role: SystemAccountRole
  status: SystemAccountStatus
  mustChangePassword: boolean
  lastLoginAt?: string
  createdAt: string
  updatedAt: string
}

interface SystemAccountRow {
  id: string
  username: string
  display_name: string
  role: SystemAccountRole
  status: SystemAccountStatus
  password_hash: string
  must_change_password: number
  last_login_at: string | null
  created_at: string
  updated_at: string
}

export interface AccessScope {
  systemAccountId: string
  role: SystemAccountRole
  systemAccountFilterId?: string
}

export interface SessionWithAccount {
  sessionId: string
  expiresAt: string
  account: SystemAccountSummary
}

export interface ProxyProfileSummary {
  id: string
  name: string
  type: string
  host: string
  port: number
  username?: string
  enabled: boolean
  testStatus: string
  lastTestedAt?: string
}

export interface UsageRecordLogSnapshot {
  [key: string]: unknown
}

export interface UsageRecordSummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  requestId: string
  clientIp?: string
  apiKeyId?: string
  apiKeyName?: string
  groupId?: string
  groupName?: string
  accountId?: string
  accountName?: string
  endpoint?: string
  providerCode?: string
  model?: string
  stream: boolean
  statusCode?: number
  success: boolean
  firstTokenMs?: number
  durationMs?: number
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  costUsd?: number
  errorCode?: string
  errorMessage?: string
  requestSnapshot?: UsageRecordLogSnapshot
  responseSnapshot?: UsageRecordLogSnapshot
  createdAt: string
}

export type UsageRecordSortField = 'createdAt' | 'firstTokenMs' | 'durationMs' | 'costUsd'
export type UsageRecordSortDirection = 'asc' | 'desc'

export interface UsageRecordListOptions {
  sortBy?: UsageRecordSortField
  sortOrder?: UsageRecordSortDirection
  limit?: number
}

export interface OpenAIAccountSecret {
  id: string
  systemAccountId: string
  name: string
  type: AccountType
  status: AccountStatus
  baseUrl: string
  apiKey: string
  refreshToken?: string
  clientId?: string
  proxyUrl?: string
  errorPolicyId?: string
  cooldownUntil?: string
  lastErrorMessage?: string
  expiresAt?: string
  credentials: Record<string, unknown>
}

export interface AccountUsageSummary {
  requestCount: number
  clientCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  totalTokens: number
  totalCost: number
  lastUsedAt?: string
}

interface GroupAccountStats {
  total: number
  available: number
  active: number
  disabled: number
  error: number
  rateLimited: number
  currentConcurrency: number
  concurrencyLimit: number
  todayUsage: AccountUsageSummary
  usage: AccountUsageSummary
}

export interface MigrationAccountInput {
  name: string
  description?: string
  baseUrl: string
  apiKey: string
}

export interface MigrationOAuthAccountInput {
  name: string
  description?: string
  accessToken: string
  refreshToken: string
  idToken?: string
  expiresAt?: string
  clientId?: string
  email?: string
  chatgptAccountId?: string
  chatgptUserId?: string
  planType?: string
  proxyProfileId?: string
}

interface AccountUsageAggregateRow {
  account_id: string
  request_count: number
  client_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  total_cost: number
  last_used_at: string | null
}

interface AccountUsageSnapshotRow {
  system_account_id: string
  account_id: string
  kind: string
  source: string | null
  snapshot_json: string
  refresh_status: string | null
  last_attempt_at: string | null
  last_success_at: string | null
  next_refresh_after: string | null
  last_error_message: string | null
  updated_at: string
}

interface UsageStatsRecordRow {
  id: string
  system_account_id: string
  request_id: string
  client_ip: string | null
  api_key_id: string | null
  group_id: string | null
  account_id: string | null
  endpoint: string | null
  provider_code: string | null
  model: string | null
  status_code: number | null
  success: number
  first_token_ms: number | null
  duration_ms: number | null
  input_tokens: number | null
  output_tokens: number | null
  cache_read_tokens: number | null
  cost_usd: number | null
  error_code: string | null
  error_message: string | null
  created_at: string
}

interface StatsJobStateRow {
  cursor_created_at: string | null
  cursor_id: string | null
  lag_seconds: number
}

interface GlobalSettingRow {
  key: string
  value_json: string
  updated_at: string
}

interface SystemSessionRow {
  id: string
  system_account_id: string
  token_hash: string
  expires_at: string
  created_at: string
  last_seen_at: string
}

function systemAccountSummaryFromRow(row: SystemAccountRow): SystemAccountSummary {
  return {
    id: row.id,
    username: row.username,
    displayName: row.display_name,
    role: row.role,
    status: row.status,
    mustChangePassword: row.must_change_password === 1,
    lastLoginAt: row.last_login_at ?? undefined,
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function resolveAccessScope(access?: AccessScope): AccessScope | undefined {
  if (access) return access
  const context = getRequestAuthContext()
  return context ? { systemAccountId: context.systemAccountId, role: context.role } : undefined
}

function currentSystemAccountId(access?: AccessScope): string {
  return resolveAccessScope(access)?.systemAccountId ?? 'sys_admin'
}

function canAccessAll(access?: AccessScope): boolean {
  const scope = resolveAccessScope(access)
  return !scope || scope.role === 'admin'
}

function scopedSystemAccountId(access?: AccessScope): string | undefined {
  const scope = resolveAccessScope(access)
  if (!scope) return undefined
  if (scope.role === 'admin') {
    const filterId = scope.systemAccountFilterId?.trim()
    return filterId || undefined
  }
  return scope.systemAccountId
}

function buildSystemAccountScopeClause(access?: AccessScope, column = 'system_account_id'): { clause: string; params: Array<string> } {
  const systemAccountId = scopedSystemAccountId(access)
  if (!systemAccountId) {
    return { clause: '', params: [] }
  }
  return { clause: ` AND ${column} = ?`, params: [systemAccountId] }
}

function buildSystemAccountWhereClause(access?: AccessScope, column = 'system_account_id'): { clause: string; params: Array<string> } {
  const systemAccountId = scopedSystemAccountId(access)
  if (!systemAccountId) {
    return { clause: '', params: [] }
  }
  return { clause: ` WHERE ${column} = ?`, params: [systemAccountId] }
}

function includeSystemAccountFields(access?: AccessScope): boolean {
  return canAccessAll(access)
}

function systemAccountNameMap(): Map<string, string> {
  return new Map(listSystemAccounts().map((account) => [account.id, account.displayName || account.username]))
}

function accountSystemAccountId(accountId: string): string | undefined {
  const row = getDatabase().prepare('SELECT system_account_id FROM accounts WHERE id = ?').get(accountId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id
}

export function resolveAccountSystemAccountId(accountId: string): string | undefined {
  return accountSystemAccountId(accountId)
}

function groupOwnerAndProvider(groupId: string): { systemAccountId: string; providerCode: ProviderCode } | undefined {
  const row = getDatabase().prepare('SELECT system_account_id, provider_code FROM groups WHERE id = ?').get(groupId) as unknown as { system_account_id?: string; provider_code?: ProviderCode } | undefined
  return row?.system_account_id && row.provider_code ? { systemAccountId: row.system_account_id, providerCode: row.provider_code } : undefined
}

function defaultOpenAIGroupIdForSystemAccount(systemAccountId: string): string | undefined {
  const row = getDatabase()
    .prepare('SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? AND (is_default = 1 OR name = ?) ORDER BY is_default DESC LIMIT 1')
    .get(systemAccountId, 'openai', DEFAULT_OPENAI_GROUP_NAME) as unknown as { id?: string } | undefined
  return row?.id
}

function defaultGroupIdForSystemAccount(providerCode: string, systemAccountId: string): string | undefined {
  const row = getDatabase()
    .prepare('SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? ORDER BY is_default DESC, updated_at DESC LIMIT 1')
    .get(systemAccountId, providerCode) as unknown as { id?: string } | undefined
  if (row?.id) {
    return row.id
  }
  return providerCode === 'openai' ? defaultOpenAIGroupIdForSystemAccount(systemAccountId) : undefined
}

function apiKeySystemAccountId(apiKeyId: string): string | undefined {
  const row = getDatabase().prepare('SELECT system_account_id FROM api_keys WHERE id = ?').get(apiKeyId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id
}

function globalProxyProfileId(proxyProfileId: string | undefined): string | undefined {
  if (!proxyProfileId) return undefined
  const row = getDatabase().prepare('SELECT id FROM proxy_profiles WHERE id = ?').get(proxyProfileId) as unknown as { id?: string } | undefined
  return row?.id
}

function canSetGlobalProxyProfile(access?: AccessScope): boolean {
  const scope = resolveAccessScope(access)
  return !scope || scope.role === 'admin'
}

export function listSystemAccounts(): SystemAccountSummary[] {
  const rows = getDatabase().prepare('SELECT * FROM system_accounts ORDER BY created_at ASC').all() as unknown as SystemAccountRow[]
  return rows.map(systemAccountSummaryFromRow)
}

export function findSystemAccountById(id: string): SystemAccountSummary | undefined {
  const row = getDatabase().prepare('SELECT * FROM system_accounts WHERE id = ?').get(id) as unknown as SystemAccountRow | undefined
  return row ? systemAccountSummaryFromRow(row) : undefined
}

export function findSystemAccountByUsername(username: string): (SystemAccountSummary & { passwordHash: string }) | undefined {
  const row = getDatabase().prepare('SELECT * FROM system_accounts WHERE lower(username) = lower(?)').get(username) as unknown as SystemAccountRow | undefined
  if (!row) {
    return undefined
  }
  const summary = systemAccountSummaryFromRow(row)
  return { ...summary, passwordHash: row.password_hash }
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
  password: string
  role?: SystemAccountRole
  status?: SystemAccountStatus
  mustChangePassword?: boolean
}): SystemAccountSummary {
  const now = nowIso()
  const id = newId('sysacc')
  const summary: SystemAccountSummary = {
    id,
    username: input.username.trim(),
    displayName: input.displayName.trim() || input.username.trim(),
    role: input.role ?? 'user',
    status: input.status ?? 'active',
    mustChangePassword: input.mustChangePassword ?? true,
    createdAt: now,
    updatedAt: now
  }
  const database = getDatabase()
  database.exec('BEGIN')
  try {
    database
      .prepare(`
        INSERT INTO system_accounts (
          id, username, display_name, role, status, password_hash, must_change_password, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
      `)
      .run(summary.id, summary.username, summary.displayName, summary.role, summary.status, hashPassword(input.password), summary.mustChangePassword ? 1 : 0, now, now)
    ensureDefaultOpenAIGroupForSystemAccount(summary.id, now)
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
  return summary
}

function ensureDefaultOpenAIGroupForSystemAccount(systemAccountId: string, timestamp = nowIso()): void {
  if (defaultOpenAIGroupIdForSystemAccount(systemAccountId)) {
    return
  }

  getDatabase()
    .prepare('INSERT INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?)')
    .run(newId('grp'), systemAccountId, DEFAULT_OPENAI_GROUP_NAME, 'openai', DEFAULT_OPENAI_GROUP_DESCRIPTION, timestamp, timestamp)
}

export function updateSystemAccount(id: string, input: {
  username?: string
  displayName?: string
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
    username: input.username?.trim() || current.username,
    displayName: input.displayName?.trim() || current.displayName,
    role: input.role ?? current.role,
    status: input.status ?? current.status,
    mustChangePassword: input.mustChangePassword ?? current.mustChangePassword
  }
  const now = nowIso()
  if (input.password) {
    getDatabase()
      .prepare(`
        UPDATE system_accounts
        SET username = ?, display_name = ?, role = ?, status = ?, password_hash = ?, must_change_password = ?, updated_at = ?
        WHERE id = ?
      `)
      .run(next.username, next.displayName, next.role, next.status, hashPassword(input.password), next.mustChangePassword ? 1 : 0, now, id)
  } else {
    getDatabase()
      .prepare(`
        UPDATE system_accounts
        SET username = ?, display_name = ?, role = ?, status = ?, must_change_password = ?, updated_at = ?
        WHERE id = ?
      `)
      .run(next.username, next.displayName, next.role, next.status, next.mustChangePassword ? 1 : 0, now, id)
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
    tokenHash: row.token_hash,
    account: systemAccountSummaryFromRow({ ...row, id: row.account_id })
  }
}

export function touchSession(sessionId: string): void {
  getDatabase()
    .prepare('UPDATE system_sessions SET last_seen_at = ? WHERE id = ?')
    .run(nowIso(), sessionId)
}

export function revokeSession(token: string): void {
  getDatabase().prepare('DELETE FROM system_sessions WHERE token_hash = ?').run(hashSecret(token))
}

export function revokeAllSessionsForAccount(systemAccountId: string): void {
  getDatabase().prepare('DELETE FROM system_sessions WHERE system_account_id = ?').run(systemAccountId)
}

export function listGlobalSettings(): Record<string, unknown> {
  const rows = getDatabase().prepare('SELECT key, value_json, updated_at FROM global_settings ORDER BY key ASC').all() as unknown as Array<GlobalSettingRow>
  return Object.fromEntries(rows.map((row) => [row.key, JSON.parse(row.value_json) as unknown]))
}

export function updateGlobalSettings(input: Record<string, unknown>): Record<string, unknown> {
  const statement = getDatabase().prepare('INSERT INTO global_settings (key, value_json, updated_at) VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at')
  const now = nowIso()
  for (const [key, value] of Object.entries(pickGlobalSettings(input))) {
    statement.run(key, JSON.stringify(value), now)
  }
  return listGlobalSettings()
}

function pickGlobalSettings(input: Record<string, unknown>): Record<string, unknown> {
  const allowedKeys = new Set(['appName', 'appIcon'])
  return Object.fromEntries(Object.entries(input).filter(([key]) => allowedKeys.has(key)))
}

export interface MigrationResult {
  imported: number
  skipped: number
  importedApiKey?: number
  importedOAuth?: number
  skippedApiKey?: number
  skippedOAuth?: number
  accountIds: string[]
  apiKey?: string
  gatewayApiKeyId?: string
  gatewayApiKeyName?: string
  gatewayApiKeyCreated?: boolean
  groupId: string
  groupName: string
}

function parseJsonArray(value: string): string[] {
  const parsed = JSON.parse(value) as unknown
  return Array.isArray(parsed) ? parsed.map(String) : []
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value.length > 0 ? value : undefined
}

function optionalNullableString(value: unknown): string | null {
  if (value === undefined || value === null) return null
  if (typeof value !== 'string') return null
  return value.trim().length > 0 ? value : null
}

function isAccountExpired(accountExpiresAt: string | null | undefined, now = Date.now()): boolean {
  if (!accountExpiresAt) return false
  const timestamp = Date.parse(accountExpiresAt)
  return Number.isFinite(timestamp) && timestamp <= now
}

function disableExpiredAccounts(access?: AccessScope): void {
  const scope = buildSystemAccountScopeClause(access)
  const now = nowIso()
  getDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'disabled',
          schedulable = 0,
          cooldown_until = NULL,
          last_error_message = ?,
          updated_at = ?
      WHERE account_expires_at IS NOT NULL
        AND account_expires_at <= ?
        AND status <> 'disabled'${scope.clause}
    `)
    .run('账户套餐已过期，已自动停用', now, now, ...scope.params)
}

function parseOptionalJsonObject(value: unknown): Record<string, unknown> | undefined {
  if (typeof value !== 'string' || !value.trim()) return undefined
  try {
    const parsed = JSON.parse(value) as unknown
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
      ? parsed as Record<string, unknown>
      : undefined
  } catch {
    return undefined
  }
}

const accountStatusValues: readonly AccountStatus[] = ['active', 'disabled', 'error', 'rate_limited', 'temporary_unavailable']
const coolingAccountStatusValues: readonly AccountStatus[] = ['rate_limited', 'temporary_unavailable']

function normalizeAccountStatus(value: unknown, fallback: AccountStatus): AccountStatus {
  return typeof value === 'string' && accountStatusValues.includes(value as AccountStatus)
    ? value as AccountStatus
    : fallback
}

function isCoolingAccountStatus(status: AccountStatus): boolean {
  return coolingAccountStatusValues.includes(status)
}

function boolInt(value: unknown, fallback: boolean): number {
  return typeof value === 'boolean' ? (value ? 1 : 0) : fallback ? 1 : 0
}

function emptyAccountUsageSummary(): AccountUsageSummary {
  return {
    requestCount: 0,
    clientCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

function emptyGroupAccountStats(): GroupAccountStats {
  return {
    total: 0,
    available: 0,
    active: 0,
    disabled: 0,
    error: 0,
    rateLimited: 0,
    currentConcurrency: 0,
    concurrencyLimit: 0,
    todayUsage: emptyAccountUsageSummary(),
    usage: emptyAccountUsageSummary()
  }
}

function groupAccountStats(accounts: AccountSummary[], todayUsage?: AccountUsageSummary): GroupAccountStats {
  const stats = emptyGroupAccountStats()
  stats.total = accounts.length
  stats.todayUsage = todayUsage ?? emptyAccountUsageSummary()
  for (const account of accounts) {
    if (account.status === 'active') {
      stats.active += 1
    } else if (account.status === 'disabled') {
      stats.disabled += 1
    } else if (account.status === 'rate_limited') {
      stats.rateLimited += 1
      stats.error += 1
    } else {
      stats.error += 1
    }
    if (account.status === 'active' && account.schedulable && !isAccountCooling(account)) {
      stats.available += 1
    }
    stats.currentConcurrency += account.currentConcurrency
    stats.concurrencyLimit += account.concurrencyLimit
    stats.usage.requestCount += account.usage.requestCount
    stats.usage.clientCount += account.usage.clientCount
    stats.usage.inputTokens += account.usage.inputTokens
    stats.usage.outputTokens += account.usage.outputTokens
    stats.usage.cacheReadTokens += account.usage.cacheReadTokens
    stats.usage.totalTokens += account.usage.totalTokens
    stats.usage.totalCost += account.usage.totalCost
    if (isLaterIso(account.usage.lastUsedAt, stats.usage.lastUsedAt)) {
      stats.usage.lastUsedAt = account.usage.lastUsedAt
    }
  }
  return stats
}

function isAccountCooling(account: AccountSummary): boolean {
  if (!account.cooldownUntil) return false
  const cooldownUntil = Date.parse(account.cooldownUntil)
  return Number.isFinite(cooldownUntil) && cooldownUntil > Date.now()
}

function isLaterIso(value?: string, current?: string): boolean {
  if (!value) return false
  if (!current) return true
  const nextTime = Date.parse(value)
  const currentTime = Date.parse(current)
  return Number.isFinite(nextTime) && (!Number.isFinite(currentTime) || nextTime > currentTime)
}

function validAccountIdsForGroup(providerCode: string, accountIds: string[], systemAccountId = currentSystemAccountId()): string[] {
  const uniqueIds = [...new Set(accountIds)]
  const accountsById = new Map(listAccounts().map((account) => [account.id, account]))
  return uniqueIds.filter((accountId) => {
    const account = accountsById.get(accountId)
    return account?.providerCode === providerCode && accountSystemAccountId(accountId) === systemAccountId
  })
}

function runDelete(sql: string, id: string): boolean {
  const result = getDatabase().prepare(sql).run(id)
  return result.changes > 0
}

function accountFingerprint(providerCode: string, type: string, baseUrl: string, secret: string): string {
  void providerCode
  void type
  void baseUrl
  return hashSecret(secret.trim())
}

function isDuplicateAccountCredentialError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  const databaseError = error as Error & { code?: string }
  return databaseError.message.includes('UNIQUE constraint failed: accounts.credential_fingerprint')
}

function throwDuplicateAccountCredentialError(): never {
  throw new DuplicateAccountCredentialError()
}

function defaultTemporaryUnschedulableMinutes(): number {
  const value = getSettings().defaultTemporaryUnschedulableMinutes
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  if (!Number.isFinite(number)) return 5
  return Math.min(Math.max(Math.trunc(number), 1), 1440)
}

export function listProviders(): ProviderDefinition[] {
  const rows = getDatabase().prepare('SELECT * FROM providers ORDER BY name ASC').all() as unknown as ProviderRow[]
  return rows.map((row) => ({
    id: row.id,
    code: row.code,
    name: row.name,
    enabled: row.enabled === 1,
    baseUrl: row.base_url,
    accountTypes: parseJsonArray(row.account_types_json) as AccountType[],
    capabilities: parseJsonArray(row.capabilities_json)
  }))
}

export function listAccounts(access?: AccessScope): AccountSummary[] {
  const scope = buildSystemAccountWhereClause(access)
  disableExpiredAccounts(access)
  const rows = getDatabase().prepare(`SELECT * FROM accounts${scope.clause} ORDER BY priority ASC, updated_at DESC`).all(...scope.params) as unknown as AccountRow[]
  const usageByAccount = loadAccountUsageSummaries(access)
  const oauthUsageByAccount = loadOpenAICodexUsageSnapshots(access)
  const accountNames = includeSystemAccountFields(access) ? systemAccountNameMap() : new Map<string, string>()
  return rows.map((row) => ({
    id: row.id,
    systemAccountId: includeSystemAccountFields(access) ? row.system_account_id : undefined,
    systemAccountName: includeSystemAccountFields(access) ? accountNames.get(row.system_account_id) : undefined,
    providerCode: row.provider_code,
    name: row.name,
    notes: row.notes ?? undefined,
    type: row.type,
    credentials: decryptJson<Record<string, unknown>>(row.credentials_encrypted),
    status: row.status,
    concurrencyLimit: row.concurrency_limit,
    currentConcurrency: 0,
    priority: row.priority,
    proxyProfileId: row.proxy_profile_id ?? undefined,
    passthroughEnabled: row.passthrough_enabled === 1,
    errorPolicyId: row.error_policy_id ?? undefined,
    schedulable: row.schedulable === 1,
    accountExpiresAt: row.account_expires_at ?? undefined,
    cooldownUntil: row.cooldown_until ?? undefined,
    lastErrorMessage: row.last_error_message ?? undefined,
    lastUsedAt: row.last_used_at ?? usageByAccount.get(row.id)?.lastUsedAt,
    usage: usageByAccount.get(row.id) ?? emptyAccountUsageSummary(),
    oauthUsage: row.provider_code === 'openai' && row.type === 'oauth' ? oauthUsageByAccount.get(row.id) : undefined
  }))
}

export function listErrorPolicies(access?: AccessScope): ErrorPolicySummary[] {
  const scope = buildSystemAccountWhereClause(access)
  const rows = getDatabase().prepare(`SELECT id, system_account_id, name, enabled, rules_json FROM error_policies${scope.clause} ORDER BY name ASC`).all(...scope.params) as unknown as ErrorPolicyRow[]
  return rows.map((row) => ({
    id: row.id,
    name: row.name,
    enabled: row.enabled === 1,
    rules: parseJsonRules(row.rules_json)
  }))
}

function parseJsonRules(value: string): Array<Record<string, unknown>> {
  try {
    const parsed = JSON.parse(value) as unknown
    if (!Array.isArray(parsed)) return []
    return parsed.filter((item): item is Record<string, unknown> => typeof item === 'object' && item !== null && !Array.isArray(item))
  } catch {
    return []
  }
}

export function createAccount(input: Record<string, unknown>): AccountSummary {
  const now = nowIso()
  const id = newId('acc')
  const providerCode = String(input.providerCode ?? input.provider_code ?? 'openai')
  const explicitGroupId = typeof input.groupId === 'string' && input.groupId ? input.groupId : typeof input.group_id === 'string' && input.group_id ? input.group_id : undefined
  const explicitGroup = explicitGroupId ? groupOwnerAndProvider(explicitGroupId) : undefined
  const systemAccountId = explicitGroup?.systemAccountId ?? currentSystemAccountId()
  const provider = listProviders().find((item) => item.code === providerCode)
  const credentials = typeof input.credentials === 'object' && input.credentials !== null ? input.credentials as Record<string, unknown> : {}
  const credentialMap = credentials as Record<string, unknown>
  const accountType = String(input.type ?? 'api_key')
  const credentialSource = accountType === 'oauth'
    ? credentialMap.refresh_token ?? credentialMap.access_token ?? ''
    : credentialMap.api_key ?? ''
  const baseUrl = String(credentialMap.base_url ?? provider?.baseUrl ?? 'https://api.openai.com/v1')
  const credentialFingerprint = typeof credentialSource === 'string' && credentialSource.trim()
    ? accountFingerprint(providerCode, accountType, baseUrl, credentialSource)
    : null
  const accountExpiresAt = optionalNullableString(input.accountExpiresAt ?? input.account_expires_at)
  const initialStatus = normalizeAccountStatus(input.status, 'active')
  const expiredByPackage = isAccountExpired(accountExpiresAt)
  const nextStatus = expiredByPackage ? 'disabled' : initialStatus
  const initialCooldownUntil = isCoolingAccountStatus(initialStatus)
    ? new Date(Date.now() + defaultTemporaryUnschedulableMinutes() * 60_000).toISOString()
    : undefined
  const groupId = explicitGroupId ?? defaultGroupIdForSystemAccount(providerCode, systemAccountId)
  if (!groupId) {
    throw new Error('Account group is required')
  }
  const group = explicitGroupId === groupId ? explicitGroup : groupOwnerAndProvider(groupId)
  if (!group || group.systemAccountId !== systemAccountId || group.providerCode !== providerCode) {
    throw new Error('Invalid account group')
  }
  const access = resolveAccessScope()
  const proxyProfileId = canSetGlobalProxyProfile(access) ? globalProxyProfileId(optionalString(input.proxyProfileId ?? input.proxy_profile_id)) : undefined
  const account: AccountSummary = {
    id,
    systemAccountId: includeSystemAccountFields() ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields() ? systemAccountNameMap().get(systemAccountId) : undefined,
    providerCode,
    name: String(input.name ?? `未命名 ${provider?.name ?? providerCode.toUpperCase()} 账户`),
    notes: optionalString(input.notes),
    type: accountType,
    credentials,
    status: nextStatus,
    concurrencyLimit: Number(input.concurrencyLimit ?? input.concurrency_limit ?? DEFAULT_ACCOUNT_CONCURRENCY_LIMIT),
    currentConcurrency: 0,
    priority: Number(input.priority ?? input.prioritiy ?? input.priority_level ?? 0),
    proxyProfileId,
    passthroughEnabled: Boolean(input.passthroughEnabled ?? input.passthrough_enabled),
    errorPolicyId: optionalString(input.errorPolicyId ?? input.error_policy_id),
    schedulable: expiredByPackage ? false : input.schedulable !== false,
    accountExpiresAt: accountExpiresAt ?? undefined,
    cooldownUntil: expiredByPackage ? undefined : initialCooldownUntil,
    lastErrorMessage: expiredByPackage ? '账户套餐已过期，已自动停用' : initialCooldownUntil ? '创建时设置为临时不可调用' : undefined,
    lastUsedAt: undefined,
    usage: {
      requestCount: 0,
      clientCount: 0,
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      totalTokens: 0,
      totalCost: 0
    }
  }

  const database = getDatabase()
  database.exec('BEGIN')
  try {
    database
      .prepare(`
        INSERT INTO accounts (
          id, system_account_id, provider_code, name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
          proxy_profile_id, concurrency_limit, passthrough_enabled, error_policy_id,
          priority, schedulable, notes, account_expires_at, cooldown_until, last_error_message, stream_failure_count, stream_failure_window_started_at, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `)
      .run(
        account.id,
        systemAccountId,
        account.providerCode,
        account.name,
        account.type,
        account.status,
        encryptJson(credentials),
        credentialFingerprint,
        maskSecret(credentialSource),
        account.proxyProfileId ?? null,
        account.concurrencyLimit,
        account.passthroughEnabled ? 1 : 0,
        account.errorPolicyId ?? null,
        account.priority,
        account.schedulable ? 1 : 0,
        optionalString(input.notes) ?? null,
        account.accountExpiresAt ?? null,
        account.cooldownUntil ?? null,
        account.lastErrorMessage ?? null,
        0,
        null,
        now,
        now
      )
    database
      .prepare('INSERT INTO group_accounts (system_account_id, group_id, account_id, weight, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, 1, ?, ?)')
      .run(systemAccountId, groupId, account.id, 1, now, now)
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    if (isDuplicateAccountCredentialError(error)) {
      throwDuplicateAccountCredentialError()
    }
    throw error
  }

  return account
}

export function findAccountByFingerprint(providerCode: string, type: string, baseUrl: string, apiKey: string): AccountSummary | undefined {
  const fingerprint = accountFingerprint(providerCode, type, baseUrl, apiKey)
  const row = getDatabase().prepare('SELECT id FROM accounts WHERE credential_fingerprint = ?').get(fingerprint) as unknown as { id?: string } | undefined
  if (!row?.id) {
    return undefined
  }
  return listAccounts().find((account) => account.id === row.id) ?? listAccounts({ role: 'admin', systemAccountId: currentSystemAccountId() }).find((account) => account.id === row.id)
}

export function updateAccount(id: string, input: Record<string, unknown>): AccountSummary | undefined {
  const current = listAccounts().find((account) => account.id === id)
  if (!current) {
    return undefined
  }
  const systemAccountId = accountSystemAccountId(id) ?? currentSystemAccountId()
  const access = resolveAccessScope()

  const credentials = typeof input.credentials === 'object' && input.credentials !== null
    ? input.credentials as Record<string, unknown>
    : current.credentials
  const credentialSource = current.type === 'oauth'
    ? credentials.refresh_token ?? credentials.access_token ?? ''
    : credentials.api_key ?? ''
  const baseUrl = String(credentials.base_url ?? 'https://api.openai.com/v1')
  const credentialFingerprint = typeof credentialSource === 'string' && credentialSource.trim()
    ? accountFingerprint(current.providerCode, current.type, baseUrl, credentialSource)
    : null
  const hasAccountExpiresAtInput = Object.prototype.hasOwnProperty.call(input, 'accountExpiresAt')
    || Object.prototype.hasOwnProperty.call(input, 'account_expires_at')
  const nextAccountExpiresAt = hasAccountExpiresAtInput
    ? optionalNullableString(input.accountExpiresAt ?? input.account_expires_at)
    : current.accountExpiresAt ?? null
  const expiredByPackage = isAccountExpired(nextAccountExpiresAt)

  const rawErrorPolicyId = Object.prototype.hasOwnProperty.call(input, 'errorPolicyId')
    ? input.errorPolicyId
    : Object.prototype.hasOwnProperty.call(input, 'error_policy_id')
      ? input.error_policy_id
      : undefined

  const hasStatusInput = Object.prototype.hasOwnProperty.call(input, 'status')
  const requestedStatus = hasStatusInput ? normalizeAccountStatus(input.status, current.status) : current.status
  const nextStatus = expiredByPackage ? 'disabled' : requestedStatus
  let nextCooldownUntil = current.cooldownUntil
  let nextLastErrorMessage = current.lastErrorMessage
  if (hasStatusInput) {
    if (nextStatus === 'active') {
      nextCooldownUntil = undefined
      nextLastErrorMessage = undefined
    } else if (nextStatus === 'disabled' || nextStatus === 'error') {
      nextCooldownUntil = undefined
    } else if (isCoolingAccountStatus(nextStatus) && !nextCooldownUntil) {
      nextCooldownUntil = new Date(Date.now() + defaultTemporaryUnschedulableMinutes() * 60_000).toISOString()
      nextLastErrorMessage = nextLastErrorMessage ?? '手动设置为临时不可调用'
    }
  }
  if (expiredByPackage) {
    nextCooldownUntil = undefined
    nextLastErrorMessage = '账户套餐已过期，已自动停用'
  }

  const next: AccountSummary = {
    ...current,
    name: typeof input.name === 'string' ? input.name : current.name,
    notes: optionalString(input.notes) ?? current.notes,
    credentials,
    status: nextStatus,
    concurrencyLimit: Number(input.concurrencyLimit ?? input.concurrency_limit ?? current.concurrencyLimit),
    priority: Number(input.priority ?? input.prioritiy ?? input.priority_level ?? current.priority),
    proxyProfileId: canSetGlobalProxyProfile(access)
      ? (Object.prototype.hasOwnProperty.call(input, 'proxyProfileId') || Object.prototype.hasOwnProperty.call(input, 'proxy_profile_id')
        ? globalProxyProfileId(optionalString(input.proxyProfileId ?? input.proxy_profile_id))
        : current.proxyProfileId)
      : current.proxyProfileId,
    passthroughEnabled: typeof input.passthroughEnabled === 'boolean' ? input.passthroughEnabled : current.passthroughEnabled,
    errorPolicyId: rawErrorPolicyId === undefined ? current.errorPolicyId : optionalString(rawErrorPolicyId),
    schedulable: expiredByPackage ? false : typeof input.schedulable === 'boolean' ? input.schedulable : current.schedulable,
    accountExpiresAt: nextAccountExpiresAt ?? undefined,
    cooldownUntil: nextCooldownUntil,
    lastErrorMessage: nextLastErrorMessage,
    lastUsedAt: current.lastUsedAt,
    usage: current.usage
  }

  try {
    getDatabase()
      .prepare(`
      UPDATE accounts
      SET name = ?, notes = ?, status = ?, credentials_encrypted = ?, credential_fingerprint = ?, credential_mask = ?,
            proxy_profile_id = ?, concurrency_limit = ?, passthrough_enabled = ?,
            error_policy_id = ?, priority = ?, schedulable = ?, account_expires_at = ?, cooldown_until = ?, last_error_message = ?, updated_at = ?
        WHERE id = ? AND system_account_id = ?
      `)
      .run(
        next.name,
        next.notes ?? null,
        next.status,
        encryptJson(credentials),
        credentialFingerprint,
        maskSecret(credentialSource),
        next.proxyProfileId ?? null,
        next.concurrencyLimit,
        next.passthroughEnabled ? 1 : 0,
        next.errorPolicyId ?? null,
        next.priority,
        next.schedulable ? 1 : 0,
        next.accountExpiresAt ?? null,
        next.cooldownUntil ?? null,
        next.lastErrorMessage ?? null,
        nowIso(),
        id,
        systemAccountId
      )
  } catch (error) {
    if (isDuplicateAccountCredentialError(error)) {
      throwDuplicateAccountCredentialError()
    }
    throw error
  }

  return next
}

export function deleteAccount(id: string): boolean {
  const scope = buildSystemAccountScopeClause()
  const result = getDatabase().prepare(`DELETE FROM accounts WHERE id = ?${scope.clause}`).run(id, ...scope.params)
  return result.changes > 0
}

export function clearAccountFailureState(id: string): AccountSummary | undefined {
  const current = listAccounts().find((account) => account.id === id)
  if (!current) {
    return undefined
  }

  getDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'active',
          cooldown_until = NULL,
          last_error_message = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
    `)
    .run(nowIso(), id)

  return listAccounts().find((account) => account.id === id)
}

export function markAccountCooldown(id: string, until: string, reason: string, status: AccountStatus = 'temporary_unavailable'): AccountSummary | undefined {
  const current = listAccounts().find((account) => account.id === id)
  if (!current) {
    return undefined
  }

  const cooldownStatus: AccountStatus = status === 'rate_limited' ? 'rate_limited' : 'temporary_unavailable'

  getDatabase()
    .prepare(`
      UPDATE accounts
      SET status = ?,
          cooldown_until = ?,
          last_error_message = ?,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
    `)
    .run(cooldownStatus, until, reason || null, nowIso(), id)

  return listAccounts().find((account) => account.id === id)
}

export function markAccountDisabledByFailure(id: string, reason: string): AccountSummary | undefined {
  const current = listAccounts().find((account) => account.id === id)
  if (!current) {
    return undefined
  }

  getDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'error',
          schedulable = 0,
          cooldown_until = NULL,
          last_error_message = ?,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
    `)
    .run(reason || null, nowIso(), id)

  return listAccounts().find((account) => account.id === id)
}

export function recordAccountStreamFailure(input: {
  accountId: string
  thresholdCount: number
  thresholdWindowMinutes: number
  action: 'cooldown' | 'disable' | 'none'
  cooldownMinutes: number
  reason: string
}): { count: number; triggered: boolean; account?: AccountSummary } {
  const row = getDatabase().prepare('SELECT id, stream_failure_count, stream_failure_window_started_at FROM accounts WHERE id = ?').get(input.accountId) as unknown as AccountFailureRow | undefined
  if (!row) {
    return { count: 0, triggered: false }
  }

  const now = new Date()
  const nowIsoValue = now.toISOString()
  const thresholdMs = Math.max(1, input.thresholdWindowMinutes) * 60_000
  const startedAt = row.stream_failure_window_started_at ? new Date(row.stream_failure_window_started_at) : undefined
  const windowValid = startedAt !== undefined && !Number.isNaN(startedAt.getTime()) && now.getTime() - startedAt.getTime() < thresholdMs
  const count = windowValid ? Math.max(0, row.stream_failure_count) + 1 : 1
  const windowStartedAt = windowValid ? row.stream_failure_window_started_at : nowIsoValue

  getDatabase()
    .prepare(`
      UPDATE accounts
      SET stream_failure_count = ?,
          stream_failure_window_started_at = ?,
          last_error_message = ?,
          updated_at = ?
      WHERE id = ?
    `)
    .run(count, windowStartedAt, input.reason || null, nowIsoValue, input.accountId)

  const triggered = count >= Math.max(1, input.thresholdCount) && input.action !== 'none'
  if (!triggered) {
    return { count, triggered: false, account: listAccounts().find((item) => item.id === input.accountId) }
  }

  if (input.action === 'cooldown') {
    const until = new Date(now.getTime() + Math.max(1, input.cooldownMinutes) * 60_000).toISOString()
    markAccountCooldown(input.accountId, until, input.reason)
  } else {
    markAccountDisabledByFailure(input.accountId, input.reason)
  }

  getDatabase()
    .prepare(`
      UPDATE accounts
      SET stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
    `)
    .run(nowIsoValue, input.accountId)

  return { count, triggered: true, account: listAccounts().find((item) => item.id === input.accountId) }
}

export function listGroups(access?: AccessScope): GroupSummary[] {
  const database = getDatabase()
  const scope = buildSystemAccountWhereClause(access)
  const rows = database.prepare(`SELECT * FROM groups${scope.clause} ORDER BY updated_at DESC`).all(...scope.params) as unknown as GroupRow[]
  const accountScope = buildSystemAccountScopeClause(access)
  const accountRows = database.prepare(`SELECT group_id, account_id FROM group_accounts WHERE enabled = 1${accountScope.clause}`).all(...accountScope.params) as Array<{ group_id: string; account_id: string }>
  const accountsById = new Map(listAccounts(access).map((account) => [account.id, account]))
  const todayUsageByGroup = loadTodayGroupUsageSummaries(access)
  const accountNames = includeSystemAccountFields(access) ? systemAccountNameMap() : new Map<string, string>()
  return rows.map((row) => ({
    id: row.id,
    systemAccountId: includeSystemAccountFields(access) ? row.system_account_id : undefined,
    systemAccountName: includeSystemAccountFields(access) ? accountNames.get(row.system_account_id) : undefined,
    name: row.name,
    providerCode: row.provider_code,
    description: row.description ?? undefined,
    enabled: row.enabled === 1,
    isDefault: row.is_default === 1,
    accountIds: accountRows
      .filter((item) => item.group_id === row.id && accountsById.get(item.account_id)?.providerCode === row.provider_code)
      .map((item) => item.account_id),
    accountStats: groupAccountStats(
      accountRows
        .filter((item) => item.group_id === row.id)
        .map((item) => item.account_id)
        .map((accountId) => accountsById.get(accountId))
        .filter((account): account is AccountSummary => account !== undefined && account.providerCode === row.provider_code),
      todayUsageByGroup.get(row.id)
    )
  }))
}

export function createGroup(input: Record<string, unknown>): GroupSummary {
  const now = nowIso()
  const systemAccountId = currentSystemAccountId()
  const providerCode = String(input.providerCode ?? input.provider_code ?? 'openai')
  const group: GroupSummary = {
    id: newId('grp'),
    systemAccountId: includeSystemAccountFields() ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields() ? systemAccountNameMap().get(systemAccountId) : undefined,
    name: String(input.name ?? '未命名分组'),
    providerCode,
    description: optionalString(input.description),
    enabled: input.enabled !== false,
    isDefault: false,
    accountIds: [],
    accountStats: emptyGroupAccountStats()
  }
  getDatabase()
    .prepare('INSERT INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)')
    .run(group.id, systemAccountId, group.name, group.providerCode, group.description ?? null, group.enabled ? 1 : 0, now, now)
  return group
}

export function findGroupByName(name: string): GroupSummary | undefined {
  return listGroups().find((group) => group.name === name)
}

export function updateGroup(id: string, input: Record<string, unknown>): GroupSummary | undefined {
  const current = listGroups().find((group) => group.id === id)
  if (!current) {
    return undefined
  }
  const systemAccountId = groupOwnerAndProvider(id)?.systemAccountId ?? currentSystemAccountId()
  const next: GroupSummary = {
    ...current,
    name: typeof input.name === 'string' ? input.name : current.name,
    providerCode: typeof input.providerCode === 'string' ? input.providerCode : typeof input.provider_code === 'string' ? input.provider_code : current.providerCode,
    description: optionalString(input.description) ?? current.description,
    enabled: typeof input.enabled === 'boolean' ? input.enabled : current.enabled
  }
  const database = getDatabase()
  database
    .prepare('UPDATE groups SET name = ?, provider_code = ?, description = COALESCE(?, description), enabled = ?, updated_at = ? WHERE id = ? AND system_account_id = ?')
    .run(next.name, next.providerCode, optionalString(input.description) ?? null, next.enabled ? 1 : 0, nowIso(), id, systemAccountId)
  database
    .prepare('DELETE FROM group_accounts WHERE group_id = ? AND system_account_id = ? AND account_id IN (SELECT id FROM accounts WHERE provider_code <> ? OR system_account_id <> ?)')
    .run(id, systemAccountId, next.providerCode, systemAccountId)
  return listGroups().find((group) => group.id === id)
}

export function deleteGroup(id: string): boolean {
  const current = listGroups().find((group) => group.id === id)
  if (current?.isDefault) {
    throw new Error('Default group cannot be deleted')
  }
  const scope = buildSystemAccountScopeClause()
  const result = getDatabase().prepare(`DELETE FROM groups WHERE id = ?${scope.clause}`).run(id, ...scope.params)
  return result.changes > 0
}

export function setAccountGroup(accountId: string, groupId: string | null): AccountSummary | undefined {
  const database = getDatabase()
  const current = listAccounts().find((account) => account.id === accountId)
  if (!current) {
    return undefined
  }
  if (!groupId) {
    return undefined
  }
  const systemAccountId = accountSystemAccountId(accountId)
  if (!systemAccountId) {
    return undefined
  }

  const group = groupOwnerAndProvider(groupId)
  if (!group || group.systemAccountId !== systemAccountId) {
    return undefined
  }
  if (group.providerCode !== current.providerCode) {
    return undefined
  }

  database.prepare('DELETE FROM group_accounts WHERE account_id = ? AND system_account_id = ?').run(accountId, systemAccountId)
  const now = nowIso()
  database
    .prepare('INSERT INTO group_accounts (system_account_id, group_id, account_id, weight, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, 1, ?, ?)')
    .run(systemAccountId, groupId, accountId, 1, now, now)

  return listAccounts().find((account) => account.id === accountId)
}

export function addAccountToGroup(groupId: string, accountId: string, weight = 1): GroupSummary | undefined {
  const database = getDatabase()
  const current = groupOwnerAndProvider(groupId)
  if (!current) {
    return undefined
  }
  if (!validAccountIdsForGroup(current.providerCode, [accountId], current.systemAccountId).includes(accountId)) {
    return undefined
  }
  const now = nowIso()
  database.prepare('DELETE FROM group_accounts WHERE account_id = ? AND system_account_id = ?').run(accountId, current.systemAccountId)
  database
    .prepare(`
      INSERT INTO group_accounts (system_account_id, group_id, account_id, weight, enabled, created_at, updated_at)
      VALUES (?, ?, ?, ?, 1, ?, ?)
      ON CONFLICT(group_id, account_id) DO UPDATE SET enabled = 1, updated_at = excluded.updated_at
    `)
    .run(current.systemAccountId, groupId, accountId, weight, now, now)
  return listGroups().find((group) => group.id === groupId)
}

export function listApiKeys(access?: AccessScope): ApiKeySummary[] {
  const scope = buildSystemAccountWhereClause(access)
  const rows = getDatabase().prepare(`SELECT * FROM api_keys${scope.clause} ORDER BY updated_at DESC`).all(...scope.params) as unknown as ApiKeyRow[]
  const accountNames = includeSystemAccountFields(access) ? systemAccountNameMap() : new Map<string, string>()
  return rows.map((row) => ({
    id: row.id,
    systemAccountId: includeSystemAccountFields(access) ? row.system_account_id : undefined,
    systemAccountName: includeSystemAccountFields(access) ? accountNames.get(row.system_account_id) : undefined,
    name: row.name,
    keyPrefix: row.key_prefix,
    key: decryptApiKeySecret(row.key_secret_encrypted),
    status: row.status,
    groupId: row.group_id,
    expiresAt: row.expires_at ?? undefined
  }))
}

function decryptApiKeySecret(value: string | null | undefined): string {
  if (!value) {
    return ''
  }
  const decrypted = decryptJson<{ key?: unknown }>(value)
  return typeof decrypted.key === 'string' ? decrypted.key : ''
}

export function createApiKeyRecord(input: Record<string, unknown>): ApiKeySummary & { key: string } {
  const now = nowIso()
  const key = createApiKey()
  const keyPrefix = key.slice(0, 8)
  const systemAccountId = currentSystemAccountId()
  const explicitGroupId = typeof input.groupId === 'string' && input.groupId ? input.groupId : typeof input.group_id === 'string' && input.group_id ? input.group_id : undefined
  const groupId = explicitGroupId ?? defaultOpenAIGroupIdForSystemAccount(systemAccountId)
  if (!groupId) {
    throw new Error('Invalid API key group')
  }
  const group = groupOwnerAndProvider(groupId)
  if (!group || group.systemAccountId !== systemAccountId) {
    throw new Error('Invalid API key group')
  }
  const record: ApiKeySummary & { key: string } = {
    id: newId('key'),
    systemAccountId: includeSystemAccountFields() ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields() ? systemAccountNameMap().get(systemAccountId) : undefined,
    name: String(input.name ?? '未命名 API Key'),
    keyPrefix,
    status: input.status === 'disabled' ? 'disabled' : 'active',
    groupId,
    expiresAt: optionalString(input.expiresAt ?? input.expires_at),
    key
  }
  getDatabase()
    .prepare(`
      INSERT INTO api_keys (id, system_account_id, name, key_hash, key_prefix, key_secret_encrypted, status, group_id, expires_at, scopes_json, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(record.id, systemAccountId, record.name, hashSecret(key), record.keyPrefix, encryptJson({ key }), record.status, record.groupId, record.expiresAt ?? null, JSON.stringify(input.scopes ?? []), now, now)
  return record
}
function findApiKeyByGroupAndName(groupId: string, name: string): ApiKeySummary | undefined {
  return listApiKeys().find((apiKey) => apiKey.groupId === groupId && apiKey.name === name)
}

export function validateGatewayApiKey(key: string): GatewayApiKeyRow | undefined {
  const row = getDatabase().prepare('SELECT id, system_account_id, group_id, status, expires_at FROM api_keys WHERE key_hash = ?').get(hashSecret(key)) as unknown as GatewayApiKeyRow | undefined
  if (!row || row.status !== 'active') {
    return undefined
  }
  if (row.expires_at && Date.parse(row.expires_at) <= Date.now()) {
    return undefined
  }
  return row
}

export function updateApiKey(id: string, input: Record<string, unknown>): ApiKeySummary | undefined {
  const current = listApiKeys().find((apiKey) => apiKey.id === id)
  if (!current) {
    return undefined
  }
  const systemAccountId = apiKeySystemAccountId(id)
  if (!systemAccountId) {
    return undefined
  }
  const nextGroupId = typeof input.groupId === 'string' ? input.groupId : typeof input.group_id === 'string' ? input.group_id : current.groupId
  const nextGroup = groupOwnerAndProvider(nextGroupId)
  if (!nextGroup || nextGroup.systemAccountId !== systemAccountId) {
    return undefined
  }
  const next: ApiKeySummary = {
    ...current,
    name: typeof input.name === 'string' ? input.name : current.name,
    status: input.status === 'disabled' ? 'disabled' : input.status === 'active' ? 'active' : current.status,
    groupId: nextGroupId,
    expiresAt: optionalString(input.expiresAt ?? input.expires_at) ?? current.expiresAt
  }
  getDatabase()
    .prepare('UPDATE api_keys SET name = ?, status = ?, group_id = ?, expires_at = ?, updated_at = ? WHERE id = ? AND system_account_id = ?')
    .run(next.name, next.status, next.groupId, next.expiresAt ?? null, nowIso(), id, systemAccountId)
  return next
}

export function deleteApiKey(id: string): boolean {
  const scope = buildSystemAccountScopeClause()
  const result = getDatabase().prepare(`DELETE FROM api_keys WHERE id = ?${scope.clause}`).run(id, ...scope.params)
  return result.changes > 0
}

export function listProxies(): ProxyProfileSummary[] {
  const rows = getDatabase().prepare('SELECT * FROM proxy_profiles ORDER BY updated_at DESC').all() as unknown as ProxyRow[]
  return rows.map(proxySummaryFromRow)
}

function proxySummaryFromRow(row: ProxyRow): ProxyProfileSummary {
  return {
    id: row.id,
    name: row.name,
    type: row.type,
    host: row.host,
    port: row.port,
    username: row.username ?? undefined,
    enabled: row.enabled === 1,
    testStatus: row.test_status,
    lastTestedAt: row.last_tested_at ?? undefined
  }
}

export function createProxy(input: Record<string, unknown>): ProxyProfileSummary {
  const now = nowIso()
  const proxy: ProxyProfileSummary = {
    id: newId('proxy'),
    name: String(input.name ?? '未命名代理'),
    type: String(input.type ?? 'http'),
    host: String(input.host ?? ''),
    port: Number(input.port ?? 0),
    username: optionalString(input.username),
    enabled: input.enabled !== false,
    testStatus: 'unknown'
  }
  getDatabase()
    .prepare(`
      INSERT INTO proxy_profiles (id, name, type, host, port, username, password_encrypted, enabled, test_status, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(proxy.id, proxy.name, proxy.type, proxy.host, proxy.port, proxy.username ?? null, input.password ? encryptJson({ password: input.password }) : null, proxy.enabled ? 1 : 0, proxy.testStatus, now, now)
  return proxy
}

export function updateProxy(id: string, input: Record<string, unknown>): ProxyProfileSummary | undefined {
  const current = listProxies().find((proxy) => proxy.id === id)
  if (!current) {
    return undefined
  }
  const next: ProxyProfileSummary = {
    ...current,
    name: typeof input.name === 'string' ? input.name : current.name,
    type: typeof input.type === 'string' ? input.type : current.type,
    host: typeof input.host === 'string' ? input.host : current.host,
    port: Number(input.port ?? current.port),
    username: optionalString(input.username) ?? current.username,
    enabled: typeof input.enabled === 'boolean' ? input.enabled : current.enabled
  }
  getDatabase()
    .prepare(`
      UPDATE proxy_profiles
      SET name = ?, type = ?, host = ?, port = ?, username = ?, enabled = ?, updated_at = ?
      WHERE id = ?
    `)
    .run(next.name, next.type, next.host, next.port, next.username ?? null, next.enabled ? 1 : 0, nowIso(), id)
  return next
}

export function deleteProxy(id: string): boolean {
  const result = getDatabase().prepare('DELETE FROM proxy_profiles WHERE id = ?').run(id)
  return result.changes > 0
}

function proxyUrlForProfile(proxyProfileId?: string | null): string | undefined {
  if (!proxyProfileId) return undefined
  const row = getDatabase()
    .prepare('SELECT type, host, port, username, password_encrypted FROM proxy_profiles WHERE id = ? AND enabled = 1')
    .get(proxyProfileId) as unknown as ProxyRow | undefined
  if (!row) return undefined
  const protocol = row.type === 'socks5h' ? 'socks5h' : row.type === 'socks5' ? 'socks5h' : row.type
  const credentials = row.username
    ? `${encodeURIComponent(row.username)}${proxyPassword(row) ? `:${encodeURIComponent(proxyPassword(row) ?? '')}` : ''}@`
    : ''
  return `${protocol}://${credentials}${row.host}:${row.port}`
}

function proxyPassword(row: ProxyRow): string | undefined {
  if (!row.password_encrypted) return undefined
  const decrypted = decryptJson<{ password?: unknown }>(row.password_encrypted)
  return typeof decrypted.password === 'string' ? decrypted.password : undefined
}

const usageRecordSortColumns: Record<UsageRecordSortField, string> = {
  createdAt: 'ur.created_at',
  firstTokenMs: 'ur.first_token_ms',
  durationMs: 'ur.duration_ms',
  costUsd: 'ur.cost_usd'
}

const usageRecordDefaultLimit = 200
const usageRecordMaxLimit = 500

export function listUsageRecords(access?: AccessScope, options?: UsageRecordListOptions): UsageRecordSummary[] {
  const scope = buildSystemAccountWhereClause(access, 'ur.system_account_id')
  const listOptions = normalizeUsageRecordListOptions(options)
  const orderClause = buildUsageRecordOrderClause(listOptions)
  const accountNames = includeSystemAccountFields(access) ? systemAccountNameMap() : new Map<string, string>()
  const rows = getDatabase()
    .prepare(`
      SELECT
        ur.*,
        ak.name AS api_key_name,
        g.name AS group_name,
        a.name AS account_name
      FROM usage_records ur
      LEFT JOIN api_keys ak ON ak.id = ur.api_key_id
      LEFT JOIN groups g ON g.id = ur.group_id
      LEFT JOIN accounts a ON a.id = ur.account_id
      ${scope.clause}
      ${orderClause}
      LIMIT ?
    `)
    .all(...scope.params, listOptions.limit) as Array<Record<string, unknown>>
  return rows.map((row) => {
    const requestSnapshot = parseOptionalJsonObject(row.request_snapshot_json)
    const inputTokens = typeof row.input_tokens === 'number' ? row.input_tokens : undefined
    const outputTokens = typeof row.output_tokens === 'number' ? row.output_tokens : undefined
    const cacheReadTokens = typeof row.cache_read_tokens === 'number' ? row.cache_read_tokens : undefined
    const model = optionalString(row.model)
    const stream = row.stream === 1
    const statusCode = typeof row.status_code === 'number' ? row.status_code : undefined
    const success = row.success === 1
    return {
      id: String(row.id),
      systemAccountId: includeSystemAccountFields(access) ? optionalString(row.system_account_id) : undefined,
      systemAccountName: includeSystemAccountFields(access) ? accountNames.get(String(row.system_account_id)) : undefined,
      requestId: String(row.request_id),
      clientIp: optionalString(row.client_ip),
      apiKeyId: optionalString(row.api_key_id),
      apiKeyName: optionalString(row.api_key_name),
      groupId: optionalString(row.group_id),
      groupName: optionalString(row.group_name),
      accountId: optionalString(row.account_id),
      accountName: optionalString(row.account_name),
      endpoint: optionalString(row.endpoint) ?? endpointFromSnapshot(requestSnapshot) ?? inferLegacyEndpoint({ model, success, statusCode, inputTokens, outputTokens, cacheReadTokens }),
      providerCode: optionalString(row.provider_code),
      model,
      stream,
      statusCode,
      success,
      firstTokenMs: typeof row.first_token_ms === 'number' ? row.first_token_ms : undefined,
      durationMs: typeof row.duration_ms === 'number' ? row.duration_ms : undefined,
      inputTokens,
      outputTokens,
      cacheReadTokens,
      costUsd: typeof row.cost_usd === 'number' ? row.cost_usd : undefined,
      errorCode: optionalString(row.error_code),
      errorMessage: optionalString(row.error_message),
      requestSnapshot,
      responseSnapshot: parseOptionalJsonObject(row.response_snapshot_json),
      createdAt: String(row.created_at)
    }
  })
}

function normalizeUsageRecordListOptions(options?: UsageRecordListOptions): Required<UsageRecordListOptions> {
  const sortBy = options?.sortBy && Object.prototype.hasOwnProperty.call(usageRecordSortColumns, options.sortBy)
    ? options.sortBy
    : 'createdAt'
  const sortOrder = options?.sortOrder === 'asc' ? 'asc' : 'desc'
  const rawLimit = options?.limit
  const limit = typeof rawLimit === 'number' && Number.isInteger(rawLimit)
    ? Math.min(usageRecordMaxLimit, Math.max(1, rawLimit))
    : usageRecordDefaultLimit
  return { sortBy, sortOrder, limit }
}

function buildUsageRecordOrderClause(options: Required<UsageRecordListOptions>): string {
  const direction = options.sortOrder === 'asc' ? 'ASC' : 'DESC'
  if (options.sortBy === 'createdAt') {
    return `ORDER BY ur.created_at ${direction}, ur.id ${direction}`
  }
  return `ORDER BY ${usageRecordSortColumns[options.sortBy]} ${direction}, ur.created_at ${direction}, ur.id ${direction}`
}

function endpointFromSnapshot(snapshot?: Record<string, unknown>): string | undefined {
  const method = typeof snapshot?.method === 'string' ? snapshot.method.toUpperCase() : undefined
  const originalUrl = typeof snapshot?.originalUrl === 'string' ? snapshot.originalUrl.split('?')[0] : undefined
  const path = typeof snapshot?.path === 'string' ? snapshot.path : undefined
  const endpoint = originalUrl ?? path
  return endpoint ? `${method ?? 'GET'} ${endpoint}` : undefined
}

function inferLegacyEndpoint(input: {
  model?: string
  success: boolean
  statusCode?: number
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
}): string | undefined {
  if (input.success && input.statusCode === 200 && !input.model && input.inputTokens === undefined && input.outputTokens === undefined && input.cacheReadTokens === undefined) return 'GET /v1/models'
  return undefined
}

export function selectOpenAIAccountForGroup(groupId: string): OpenAIAccountSecret | undefined {
  return listOpenAIAccountsForGroup(groupId)[0]
}

export function listOpenAIAccountsForGroup(groupId: string): OpenAIAccountSecret[] {
  const database = getDatabase()
  const now = nowIso()
  disableExpiredAccounts(resolveAccessScope())
  const groupAccountRows = database
    .prepare(`
      SELECT group_accounts.account_id
      FROM group_accounts
      INNER JOIN accounts ON accounts.id = group_accounts.account_id
      WHERE group_accounts.group_id = ? AND group_accounts.enabled = 1
      ORDER BY accounts.priority ASC, group_accounts.weight DESC, group_accounts.created_at ASC
    `)
    .all(groupId) as unknown as GroupAccountRow[]

  const accounts: OpenAIAccountSecret[] = []
  for (const groupAccount of groupAccountRows) {
    const row = database
      .prepare(`
        SELECT id, system_account_id, name, type, status, credentials_encrypted, proxy_profile_id, error_policy_id, cooldown_until, last_error_message
        FROM accounts
        WHERE id = ?
          AND provider_code = 'openai'
          AND type IN ('api_key', 'oauth')
          AND schedulable = 1
          AND (account_expires_at IS NULL OR account_expires_at > ?)
          AND (
            (status = 'active' AND (cooldown_until IS NULL OR cooldown_until <= ?))
            OR (status IN ('rate_limited', 'temporary_unavailable') AND cooldown_until IS NOT NULL AND cooldown_until <= ?)
          )
      `)
      .get(groupAccount.account_id, now, now, now) as unknown as { id: string; system_account_id: string; name: string; type: AccountType; status: AccountStatus; credentials_encrypted: string; proxy_profile_id: string | null; error_policy_id: string | null; cooldown_until: string | null; last_error_message: string | null } | undefined
    if (!row) {
      continue
    }
    const credentials = decryptJson<Record<string, unknown>>(row.credentials_encrypted)
    const apiKey = row.type === 'oauth'
      ? typeof credentials.access_token === 'string' ? credentials.access_token : ''
      : typeof credentials.api_key === 'string' ? credentials.api_key : ''
    if (!apiKey) {
      continue
    }
    accounts.push({
      id: row.id,
      systemAccountId: row.system_account_id,
      name: row.name,
      type: row.type,
      status: row.status,
      baseUrl: typeof credentials.base_url === 'string' && credentials.base_url ? credentials.base_url : 'https://api.openai.com/v1',
      apiKey,
      refreshToken: typeof credentials.refresh_token === 'string' ? credentials.refresh_token : undefined,
      clientId: typeof credentials.client_id === 'string' ? credentials.client_id : undefined,
      proxyUrl: proxyUrlForProfile(row.proxy_profile_id),
      errorPolicyId: row.error_policy_id ?? undefined,
      cooldownUntil: row.cooldown_until ?? undefined,
      lastErrorMessage: row.last_error_message ?? undefined,
      expiresAt: typeof credentials.expires_at === 'string' ? credentials.expires_at : undefined,
      credentials
    })
  }

  return accounts
}

export function createUsageRecord(input: {
  systemAccountId?: string
  requestId: string
  clientIp?: string
  apiKeyId?: string
  groupId?: string
  accountId?: string
  endpoint?: string
  providerCode?: string
  model?: string
  stream?: boolean
  statusCode?: number
  success: boolean
  firstTokenMs?: number
  durationMs?: number
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  costUsd?: number
  errorCode?: string
  errorMessage?: string
  requestSnapshot?: unknown
  responseSnapshot?: unknown
}): void {
  const now = nowIso()
  const systemAccountId = input.systemAccountId ?? systemAccountIdForUsage(input)
  getDatabase()
    .prepare(`
      INSERT INTO usage_records (
        id, system_account_id, request_id, client_ip, api_key_id, group_id, account_id, endpoint, provider_code, model, stream,
        status_code, success, first_token_ms, duration_ms, input_tokens, output_tokens, cache_read_tokens, cost_usd, error_code, error_message,
        request_snapshot_json, response_snapshot_json, created_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      newId('usage'),
      systemAccountId,
      input.requestId,
      input.clientIp ?? null,
      input.apiKeyId ?? null,
      input.groupId ?? null,
      input.accountId ?? null,
      input.endpoint ?? null,
      input.providerCode ?? null,
      input.model ?? null,
      input.stream ? 1 : 0,
      input.statusCode ?? null,
      input.success ? 1 : 0,
      input.firstTokenMs ?? null,
      input.durationMs ?? null,
      input.inputTokens ?? null,
      input.outputTokens ?? null,
      input.cacheReadTokens ?? null,
      input.costUsd ?? null,
      input.errorCode ?? null,
      input.errorMessage ?? null,
      input.requestSnapshot ? JSON.stringify(input.requestSnapshot) : null,
      input.responseSnapshot ? JSON.stringify(input.responseSnapshot) : null,
      now
    )

  if (input.accountId) {
    getDatabase()
      .prepare('UPDATE accounts SET last_used_at = ?, updated_at = ? WHERE id = ?')
      .run(now, now, input.accountId)
  }
}

export function upsertAccountUsageSnapshot(input: {
  accountId: string
  kind: 'openai_codex'
  source?: string
  snapshot: Record<string, unknown>
  updatedAt?: string
}): void {
  const now = nowIso()
  const updatedAt = input.updatedAt ?? now
  const systemAccountId = accountSystemAccountId(input.accountId) ?? currentSystemAccountId()
  getDatabase()
    .prepare(`
      INSERT INTO account_usage_snapshots (
        system_account_id, account_id, kind, source, snapshot_json, refresh_status,
        last_success_at, last_error_message, updated_at, created_at
      )
      VALUES (?, ?, ?, ?, ?, 'fresh', ?, NULL, ?, ?)
      ON CONFLICT(system_account_id, account_id, kind) DO UPDATE SET
        system_account_id = excluded.system_account_id,
        source = excluded.source,
        snapshot_json = excluded.snapshot_json,
        refresh_status = 'fresh',
        last_success_at = excluded.last_success_at,
        last_error_message = NULL,
        updated_at = excluded.updated_at
    `)
    .run(
      systemAccountId,
      input.accountId,
      input.kind,
      input.source ?? null,
      JSON.stringify(input.snapshot),
      updatedAt,
      updatedAt,
      now
    )
}

export function updateAccountUsageSnapshotRefreshState(input: {
  accountId: string
  kind: 'openai_codex'
  status: 'pending' | 'fresh' | 'failed' | 'rate_limited'
  attemptedAt?: string
  successAt?: string
  nextRefreshAfter?: string
  errorMessage?: string
}): void {
  const now = nowIso()
  const systemAccountId = accountSystemAccountId(input.accountId) ?? currentSystemAccountId()
  getDatabase()
    .prepare(`
      INSERT INTO account_usage_snapshots (
        system_account_id, account_id, kind, source, snapshot_json, refresh_status,
        last_attempt_at, last_success_at, next_refresh_after, last_error_message, updated_at, created_at
      )
      VALUES (?, ?, ?, NULL, '{}', ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(system_account_id, account_id, kind) DO UPDATE SET
        refresh_status = excluded.refresh_status,
        last_attempt_at = COALESCE(excluded.last_attempt_at, account_usage_snapshots.last_attempt_at),
        last_success_at = COALESCE(excluded.last_success_at, account_usage_snapshots.last_success_at),
        next_refresh_after = excluded.next_refresh_after,
        last_error_message = excluded.last_error_message,
        updated_at = excluded.updated_at
    `)
    .run(
      systemAccountId,
      input.accountId,
      input.kind,
      input.status,
      input.attemptedAt ?? null,
      input.successAt ?? (input.status === 'fresh' ? now : null),
      input.nextRefreshAfter ?? null,
      input.errorMessage ?? null,
      now,
      now
    )
}

function systemAccountIdForUsage(input: { apiKeyId?: string; groupId?: string; accountId?: string }): string {
  const database = getDatabase()
  if (input.apiKeyId) {
    const row = database.prepare('SELECT system_account_id FROM api_keys WHERE id = ?').get(input.apiKeyId) as unknown as { system_account_id?: string } | undefined
    if (row?.system_account_id) return row.system_account_id
  }
  if (input.groupId) {
    const row = database.prepare('SELECT system_account_id FROM groups WHERE id = ?').get(input.groupId) as unknown as { system_account_id?: string } | undefined
    if (row?.system_account_id) return row.system_account_id
  }
  if (input.accountId) {
    const row = database.prepare('SELECT system_account_id FROM accounts WHERE id = ?').get(input.accountId) as unknown as { system_account_id?: string } | undefined
    if (row?.system_account_id) return row.system_account_id
  }
  return currentSystemAccountId()
}

export function resolveProxyUrlForProfile(proxyProfileId?: string | null): string | undefined {
  return proxyUrlForProfile(proxyProfileId)
}

export function resolveProxyUrlForProfileForSystemAccount(proxyProfileId: string | undefined | null, _systemAccountId: string): string | undefined {
  return proxyUrlForProfile(proxyProfileId)
}

function loadAccountUsageSummaries(access?: AccessScope): Map<string, AccountUsageSummary> {
  const scope = buildSystemAccountScopeClause(access)
  const rows = getDatabase().prepare(`
    SELECT
      scope_id AS account_id,
      request_count,
      client_count,
      input_tokens,
      output_tokens,
      cache_read_tokens,
      total_cost_usd AS total_cost,
      last_used_at
    FROM usage_stats_totals
    WHERE scope_type = 'account'${scope.clause}
  `).all(...scope.params) as unknown as AccountUsageAggregateRow[]

  const result = new Map<string, AccountUsageSummary>()
  for (const row of rows) {
    result.set(row.account_id, usageSummaryFromAggregate(row))
  }
  return result
}

function loadTodayGroupUsageSummaries(access?: AccessScope): Map<string, AccountUsageSummary> {
  const scope = buildSystemAccountScopeClause(access)
  const rows = getDatabase().prepare(`
    SELECT
      scope_id AS group_id,
      request_count,
      client_count,
      input_tokens,
      output_tokens,
      cache_read_tokens,
      total_cost_usd AS total_cost,
      last_used_at
    FROM usage_stats_daily
    WHERE scope_type = 'group' AND stat_date = ?${scope.clause}
  `).all(todayDateKey(), ...scope.params) as unknown as Array<AccountUsageAggregateRow & { group_id: string }>

  const result = new Map<string, AccountUsageSummary>()
  for (const row of rows) {
    result.set(row.group_id, usageSummaryFromAggregate(row))
  }
  return result
}

function loadOpenAICodexUsageSnapshots(access?: AccessScope): Map<string, AccountOAuthUsageSnapshot> {
  const scope = buildSystemAccountScopeClause(access)
  const rows = getDatabase().prepare(`
    SELECT
      account_id, kind, source, snapshot_json, refresh_status,
      last_attempt_at, last_success_at, next_refresh_after, last_error_message, updated_at
    FROM account_usage_snapshots
    WHERE kind = 'openai_codex'${scope.clause}
  `).all(...scope.params) as unknown as AccountUsageSnapshotRow[]

  const result = new Map<string, AccountOAuthUsageSnapshot>()
  for (const row of rows) {
    const snapshot = parseOptionalJsonObject(row.snapshot_json)
    if (!snapshot) continue
    result.set(row.account_id, {
      kind: 'openai_codex',
      source: row.source ?? optionalString(snapshot.source),
      updatedAt: row.updated_at,
      refreshStatus: row.refresh_status ?? undefined,
      lastAttemptAt: row.last_attempt_at ?? undefined,
      lastSuccessAt: row.last_success_at ?? undefined,
      nextRefreshAfter: row.next_refresh_after ?? undefined,
      lastErrorMessage: row.last_error_message ?? undefined,
      fiveHour: oauthUsageWindowFromSnapshot(snapshot, '5h', row.updated_at),
      sevenDay: oauthUsageWindowFromSnapshot(snapshot, '7d', row.updated_at)
    })
  }
  return result
}

function usageSummaryFromAggregate(row: AccountUsageAggregateRow): AccountUsageSummary {
  const inputTokens = Number(row.input_tokens ?? 0)
  const outputTokens = Number(row.output_tokens ?? 0)
  const cacheReadTokens = Number(row.cache_read_tokens ?? 0)
  return {
    requestCount: Number(row.request_count ?? 0),
    clientCount: Number(row.client_count ?? 0),
    inputTokens,
    outputTokens,
    cacheReadTokens,
    totalTokens: inputTokens + outputTokens,
    totalCost: Number(row.total_cost ?? 0),
    lastUsedAt: row.last_used_at ?? undefined
  }
}

function oauthUsageWindowFromSnapshot(snapshot: Record<string, unknown>, window: '5h' | '7d', updatedAt: string): AccountOAuthUsageWindow | undefined {
  const utilization = numberFromUnknown(snapshot[`codex_${window}_used_percent`])
  if (utilization === undefined) return undefined
  const resetAt = optionalString(snapshot[`codex_${window}_reset_at`]) ?? resetAtFromSeconds(updatedAt, numberFromUnknown(snapshot[`codex_${window}_reset_after_seconds`]))
  const remainingSeconds = resetAt ? Math.max(0, Math.ceil((Date.parse(resetAt) - Date.now()) / 1000)) : 0
  const isExpired = resetAt ? Date.parse(resetAt) <= Date.now() : false
  return {
    utilization: isExpired ? 0 : utilization,
    resetsAt: resetAt,
    remainingSeconds,
    windowMinutes: numberFromUnknown(snapshot[`codex_${window}_window_minutes`])
  }
}

function resetAtFromSeconds(updatedAt: string, seconds?: number): string | undefined {
  if (seconds === undefined || seconds <= 0) return undefined
  const baseTime = Date.parse(updatedAt)
  if (!Number.isFinite(baseTime)) return undefined
  return new Date(baseTime + seconds * 1000).toISOString()
}

function numberFromUnknown(value: unknown): number | undefined {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? number : undefined
}

function startOfTodayIso(): string {
  const date = new Date()
  date.setHours(0, 0, 0, 0)
  return date.toISOString()
}

function todayDateKey(): string {
  return dateKey(new Date())
}

function dateKey(date: Date): string {
  return date.toISOString().slice(0, 10)
}

function hourKey(date: Date): string {
  return date.toISOString().slice(0, 13)
}

export function importOpenAIApiKeyAccounts(input: {
  accounts: MigrationAccountInput[]
  oauthAccounts?: MigrationOAuthAccountInput[]
  groupName?: string
  createGatewayApiKey?: boolean
  gatewayApiKeyName?: string
  dryRun?: boolean
}): MigrationResult {
  const groupName = input.groupName ?? '迁移 OpenAI 账户分组'
  const existingGroup = findGroupByName(groupName)
  const group = existingGroup ?? (input.dryRun ? { id: 'dry_run_group', name: groupName, enabled: true, accountIds: [] } : createGroup({ name: groupName, description: '从外部数据导入的 OpenAI API Key 与 OAuth 账户' }))
  let imported = 0
  let skipped = 0
  let importedApiKey = 0
  let importedOAuth = 0
  let skippedApiKey = 0
  let skippedOAuth = 0
  const accountIds = new Set(group.accountIds)

  for (const account of input.accounts) {
    const existing = findAccountByFingerprint('openai', 'api_key', account.baseUrl, account.apiKey)
    if (existing) {
      skipped += 1
      skippedApiKey += 1
      accountIds.add(existing.id)
      continue
    }
    if (input.dryRun) {
      imported += 1
      importedApiKey += 1
      continue
    }
    const created = createAccount({
      name: account.name,
      type: 'api_key',
      credentials: {
        api_key: account.apiKey,
        base_url: account.baseUrl
      },
      status: 'active',
      concurrencyLimit: DEFAULT_ACCOUNT_CONCURRENCY_LIMIT,
      passthroughEnabled: true,
      schedulable: true,
      groupId: group.id,
      notes: account.description
    })
    imported += 1
    importedApiKey += 1
    accountIds.add(created.id)
  }

  for (const account of input.oauthAccounts ?? []) {
    const existing = findAccountByFingerprint('openai', 'oauth', 'https://api.openai.com/v1', account.refreshToken)
    if (existing) {
      skipped += 1
      skippedOAuth += 1
      accountIds.add(existing.id)
      continue
    }
    if (input.dryRun) {
      imported += 1
      importedOAuth += 1
      continue
    }

    const credentials: Record<string, unknown> = {
      access_token: account.accessToken,
      refresh_token: account.refreshToken,
      base_url: 'https://api.openai.com/v1'
    }
    if (account.idToken) credentials.id_token = account.idToken
    if (account.expiresAt) credentials.expires_at = account.expiresAt
    if (account.clientId) credentials.client_id = account.clientId
    if (account.email) credentials.email = account.email
    if (account.chatgptAccountId) credentials.chatgpt_account_id = account.chatgptAccountId
    if (account.chatgptUserId) credentials.chatgpt_user_id = account.chatgptUserId
    if (account.planType) credentials.plan_type = account.planType

    const created = createAccount({
      name: account.name,
      type: 'oauth',
      credentials,
      status: 'active',
      concurrencyLimit: DEFAULT_ACCOUNT_CONCURRENCY_LIMIT,
      proxyProfileId: account.proxyProfileId,
      passthroughEnabled: true,
      schedulable: true,
      groupId: group.id,
      notes: account.description
    })
    imported += 1
    importedOAuth += 1
    accountIds.add(created.id)
  }

  if (!input.dryRun) {
    for (const accountId of accountIds) {
      addAccountToGroup(group.id, accountId)
    }
  }

  const result: MigrationResult = {
    imported,
    skipped,
    importedApiKey,
    importedOAuth,
    skippedApiKey,
    skippedOAuth,
    accountIds: [...accountIds],
    groupId: group.id,
    groupName: group.name
  }

  if (input.createGatewayApiKey && !input.dryRun) {
    const gatewayApiKeyName = input.gatewayApiKeyName ?? '迁移 OpenAI 网关 Key'
    const existingGatewayApiKey = findApiKeyByGroupAndName(group.id, gatewayApiKeyName)
    result.gatewayApiKeyName = gatewayApiKeyName
    if (existingGatewayApiKey) {
      result.gatewayApiKeyId = existingGatewayApiKey.id
      result.apiKey = existingGatewayApiKey.key || undefined
      result.gatewayApiKeyCreated = false
    } else {
      const createdGatewayApiKey = createApiKeyRecord({
        name: gatewayApiKeyName,
        groupId: group.id,
        status: 'active',
      })
      result.apiKey = createdGatewayApiKey.key
      result.gatewayApiKeyId = createdGatewayApiKey.id
      result.gatewayApiKeyCreated = true
    }
  }

  return result
}

export function getSettings(access?: AccessScope): Record<string, unknown> {
  const systemAccountId = currentSystemAccountId(access)
  const rows = getDatabase().prepare('SELECT key, value_json FROM system_settings WHERE system_account_id = ? ORDER BY key ASC').all(systemAccountId) as Array<{ key: string; value_json: string }>
  return Object.fromEntries(rows.filter((row) => !isHiddenSystemSetting(row.key)).map((row) => [row.key, JSON.parse(row.value_json) as unknown]))
}

export function updateSettings(input: Record<string, unknown>, access?: AccessScope): Record<string, unknown> {
  const systemAccountId = currentSystemAccountId(access)
  const statement = getDatabase().prepare(`
    INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
    VALUES (?, ?, ?, ?)
    ON CONFLICT(system_account_id, key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at
  `)
  const now = nowIso()
  for (const [key, value] of Object.entries(input)) {
    if (isHiddenSystemSetting(key)) {
      continue
    }
    statement.run(systemAccountId, key, JSON.stringify(value), now)
  }
  return getSettings(access)
}

function isHiddenSystemSetting(key: string): boolean {
  return key === 'apiKeyPrefix' || key === 'defaultOpenAIBaseUrl' || key === 'defaultErrorPolicyId' || key === 'defaultAccountConcurrencyLimit' || key.startsWith('_migration')
}

interface UsageStatsAccumulator {
  requestCount: number
  successCount: number
  errorCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  totalCostUsd: number
  durationMsSum: number
  durationMsCount: number
  firstTokenMsSum: number
  firstTokenMsCount: number
  lastUsedAt?: string
  lastErrorAt?: string
}

interface StatsAggregateMathRow {
  request_count: number
  success_count: number
  error_count: number
  client_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  total_cost: number
  duration_ms_sum: number
  duration_ms_count: number
  first_token_ms_sum: number
  first_token_ms_count: number
  last_used_at: string | null
}

interface SystemMetricsSampleInput {
  cpuPercent?: number
  memoryUsedPercent?: number
  memoryTotalBytes?: number
  memoryFreeBytes?: number
  processRssBytes?: number
  processHeapUsedBytes?: number
  processHeapTotalBytes?: number
  eventLoopLagMs?: number
  networkRxBytesPerSecond?: number
  networkTxBytesPerSecond?: number
  networkRxTotalBytes?: number
  networkTxTotalBytes?: number
  dbFileBytes?: number
  statsLagSeconds?: number
}

export interface UsageStatsOverview {
  today: AccountUsageSummary & { successCount: number; errorCount: number; errorRate: number; averageDurationMs?: number; averageFirstTokenMs?: number }
  totals: AccountUsageSummary & { successCount: number; errorCount: number; errorRate: number; averageDurationMs?: number; averageFirstTokenMs?: number }
  hourlyTrend: Array<{ statHour: string; requestCount: number; totalTokens: number; totalCost: number; averageDurationMs?: number; errorCount: number }>
  modelDistribution: Array<{ model: string; providerCode: string; requestCount: number; totalTokens: number; totalCost: number }>
  errors: Array<{ errorCode: string; providerCode: string; statusCode?: number; errorMessage?: string; errorCount: number }>
  statsLagSeconds: number
}

export interface SystemMetricsOverview {
  latest?: {
    sampledAt: string
    cpuPercent?: number
    memoryUsedPercent?: number
    memoryTotalBytes?: number
    memoryFreeBytes?: number
    processRssBytes?: number
    processHeapUsedBytes?: number
    processHeapTotalBytes?: number
    eventLoopLagMs?: number
    networkRxBytesPerSecond?: number
    networkTxBytesPerSecond?: number
    networkRxTotalBytes?: number
    networkTxTotalBytes?: number
    dbFileBytes?: number
    statsLagSeconds?: number
  }
  hourlyTrend: Array<{
    statHour: string
    sampleCount: number
    cpuPercentAvg?: number
    cpuPercentMax?: number
    memoryUsedPercentAvg?: number
    memoryUsedPercentMax?: number
    eventLoopLagMsAvg?: number
    eventLoopLagMsMax?: number
    networkRxBytesPerSecondAvg?: number
    networkRxBytesPerSecondMax?: number
    networkTxBytesPerSecondAvg?: number
    networkTxBytesPerSecondMax?: number
    networkRxTotalBytesMax?: number
    networkTxTotalBytesMax?: number
    processRssBytesMax?: number
    processHeapUsedBytesMax?: number
    dbFileBytesMax?: number
    statsLagSecondsMax?: number
  }>
}

export function aggregateUsageStatsBatch(limit = 2000): number {
  const database = getDatabase()
  const state = usageStatsJobState(database)
  const rows = database
    .prepare(`
      SELECT *
      FROM usage_records
      WHERE created_at > ? OR (created_at = ? AND id > ?)
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all(state.cursorCreatedAt, state.cursorCreatedAt, state.cursorId, Math.max(1, limit)) as unknown as UsageStatsRecordRow[]

  if (!rows.length) {
    updateStatsJobState(database, { lastSuccessAt: nowIso(), lagSeconds: 0 })
    return 0
  }

  const updatedAt = nowIso()
  database.exec('BEGIN')
  try {
    for (const row of rows) {
      aggregateUsageStatsRecord(database, row, updatedAt)
    }
    const last = rows[rows.length - 1]
    updateStatsJobState(database, {
      cursorCreatedAt: last.created_at,
      cursorId: last.id,
      lastSuccessAt: updatedAt,
      lagSeconds: statsLagSecondsFromCursor(last.created_at)
    })
    cleanupStatsCache(database, updatedAt)
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    updateStatsJobState(database, {
      lastErrorMessage: error instanceof Error ? error.message : 'Usage stats aggregation failed',
      lagSeconds: latestUsageStatsLagSeconds()
    })
    throw error
  }

  return rows.length
}

export function insertSystemMetricsSample(input: SystemMetricsSampleInput): void {
  const database = getDatabase()
  const sampledAt = nowIso()
  const statHour = hourKey(new Date(sampledAt))
  database.exec('BEGIN')
  try {
    database
      .prepare(`
        INSERT INTO system_metrics_samples (
          sampled_at, cpu_percent, memory_used_percent, memory_total_bytes, memory_free_bytes,
          process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes, event_loop_lag_ms,
          network_rx_bytes_per_sec, network_tx_bytes_per_sec, network_rx_total_bytes, network_tx_total_bytes,
          db_file_bytes, stats_lag_seconds, id, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `)
      .run(
        sampledAt,
        input.cpuPercent ?? null,
        input.memoryUsedPercent ?? null,
        input.memoryTotalBytes ?? null,
        input.memoryFreeBytes ?? null,
        input.processRssBytes ?? null,
        input.processHeapUsedBytes ?? null,
        input.processHeapTotalBytes ?? null,
        input.eventLoopLagMs ?? null,
        input.networkRxBytesPerSecond ?? null,
        input.networkTxBytesPerSecond ?? null,
        input.networkRxTotalBytes ?? null,
        input.networkTxTotalBytes ?? null,
        input.dbFileBytes ?? null,
        input.statsLagSeconds ?? null,
        newId('metric'),
        sampledAt
      )
    upsertSystemMetricsHourly(database, statHour, input, sampledAt)
    cleanupSystemMetrics(database)
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
}

export function latestUsageStatsLagSeconds(): number {
  const row = getDatabase()
    .prepare("SELECT lag_seconds FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'")
    .get() as unknown as { lag_seconds?: number } | undefined
  return Number(row?.lag_seconds ?? 0)
}

export function getUsageStatsOverview(access?: AccessScope): UsageStatsOverview {
  const database = getDatabase()
  const effectiveAccess = resolveAccessScope(access)
  const systemAccountIds = visibleSystemAccountIds(effectiveAccess)
  const placeholders = sqlPlaceholders(systemAccountIds.length)
  const today = todayDateKey()
  const sinceHour = hourKey(new Date(Date.now() - 24 * 60 * 60 * 1000))

  const todayRow = database.prepare(`
    SELECT COALESCE(SUM(request_count), 0) AS request_count, COALESCE(SUM(success_count), 0) AS success_count,
      COALESCE(SUM(error_count), 0) AS error_count, COALESCE(SUM(client_count), 0) AS client_count,
      COALESCE(SUM(input_tokens), 0) AS input_tokens, COALESCE(SUM(output_tokens), 0) AS output_tokens,
      COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens, COALESCE(SUM(total_cost_usd), 0) AS total_cost,
      COALESCE(SUM(duration_ms_sum), 0) AS duration_ms_sum, COALESCE(SUM(duration_ms_count), 0) AS duration_ms_count,
      COALESCE(SUM(first_token_ms_sum), 0) AS first_token_ms_sum, COALESCE(SUM(first_token_ms_count), 0) AS first_token_ms_count,
      MAX(last_used_at) AS last_used_at
    FROM usage_stats_daily
    WHERE scope_type = 'system_account' AND stat_date = ? AND system_account_id IN (${placeholders})
  `).get(today, ...systemAccountIds) as unknown as AccountUsageAggregateRow & StatsAggregateMathRow

  const totalRow = database.prepare(`
    SELECT COALESCE(SUM(request_count), 0) AS request_count, COALESCE(SUM(success_count), 0) AS success_count,
      COALESCE(SUM(error_count), 0) AS error_count, COALESCE(SUM(client_count), 0) AS client_count,
      COALESCE(SUM(input_tokens), 0) AS input_tokens, COALESCE(SUM(output_tokens), 0) AS output_tokens,
      COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens, COALESCE(SUM(total_cost_usd), 0) AS total_cost,
      COALESCE(SUM(duration_ms_sum), 0) AS duration_ms_sum, COALESCE(SUM(duration_ms_count), 0) AS duration_ms_count,
      COALESCE(SUM(first_token_ms_sum), 0) AS first_token_ms_sum, COALESCE(SUM(first_token_ms_count), 0) AS first_token_ms_count,
      MAX(last_used_at) AS last_used_at
    FROM usage_stats_totals
    WHERE scope_type = 'system_account' AND system_account_id IN (${placeholders})
  `).get(...systemAccountIds) as unknown as AccountUsageAggregateRow & StatsAggregateMathRow

  const hourlyRows = database.prepare(`
    SELECT stat_hour, COALESCE(SUM(request_count), 0) AS request_count, COALESCE(SUM(error_count), 0) AS error_count,
      COALESCE(SUM(input_tokens), 0) AS input_tokens, COALESCE(SUM(output_tokens), 0) AS output_tokens,
      COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens, COALESCE(SUM(total_cost_usd), 0) AS total_cost,
      COALESCE(SUM(duration_ms_sum), 0) AS duration_ms_sum, COALESCE(SUM(duration_ms_count), 0) AS duration_ms_count
    FROM usage_stats_hourly
    WHERE scope_type = 'system_account' AND stat_hour >= ? AND system_account_id IN (${placeholders})
    GROUP BY stat_hour ORDER BY stat_hour ASC
  `).all(sinceHour, ...systemAccountIds) as unknown as Array<StatsAggregateMathRow & { stat_hour: string; error_count: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; total_cost: number }>

  const modelRows = database.prepare(`
    SELECT provider_code, model, COALESCE(SUM(request_count), 0) AS request_count,
      COALESCE(SUM(input_tokens), 0) AS input_tokens, COALESCE(SUM(output_tokens), 0) AS output_tokens,
      COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens, COALESCE(SUM(total_cost_usd), 0) AS total_cost
    FROM usage_model_daily
    WHERE stat_date = ? AND system_account_id IN (${placeholders})
    GROUP BY provider_code, model ORDER BY request_count DESC LIMIT 10
  `).all(today, ...systemAccountIds) as unknown as Array<{ provider_code: string; model: string; request_count: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; total_cost: number }>

  const errorRows = database.prepare(`
    SELECT provider_code, error_code, MAX(status_code) AS status_code, MAX(error_message) AS error_message, COALESCE(SUM(error_count), 0) AS error_count
    FROM usage_error_daily
    WHERE stat_date = ? AND system_account_id IN (${placeholders})
    GROUP BY provider_code, error_code ORDER BY error_count DESC LIMIT 10
  `).all(today, ...systemAccountIds) as unknown as Array<{ provider_code: string; error_code: string; status_code: number; error_message: string | null; error_count: number }>

  return {
    today: usageSummaryWithMath(todayRow),
    totals: usageSummaryWithMath(totalRow),
    hourlyTrend: hourlyRows.map((row) => ({
      statHour: row.stat_hour,
      requestCount: Number(row.request_count ?? 0),
      totalTokens: Number(row.input_tokens ?? 0) + Number(row.output_tokens ?? 0) + Number(row.cache_read_tokens ?? 0),
      totalCost: Number(row.total_cost ?? 0),
      averageDurationMs: averageFromSum(row.duration_ms_sum, row.duration_ms_count),
      errorCount: Number(row.error_count ?? 0)
    })),
    modelDistribution: modelRows.map((row) => ({
      providerCode: row.provider_code,
      model: row.model,
      requestCount: Number(row.request_count ?? 0),
      totalTokens: Number(row.input_tokens ?? 0) + Number(row.output_tokens ?? 0) + Number(row.cache_read_tokens ?? 0),
      totalCost: Number(row.total_cost ?? 0)
    })),
    errors: errorRows.map((row) => ({
      providerCode: row.provider_code,
      errorCode: row.error_code,
      statusCode: row.status_code || undefined,
      errorMessage: row.error_message ?? undefined,
      errorCount: Number(row.error_count ?? 0)
    })),
    statsLagSeconds: latestUsageStatsLagSeconds()
  }
}

export function getSystemMetricsOverview(): SystemMetricsOverview {
  const database = getDatabase()
  const latest = database.prepare('SELECT * FROM system_metrics_samples ORDER BY sampled_at DESC LIMIT 1').get() as unknown as Record<string, unknown> | undefined
  const sinceHour = hourKey(new Date(Date.now() - 24 * 60 * 60 * 1000))
  const rows = database.prepare('SELECT * FROM system_metrics_hourly WHERE stat_hour >= ? ORDER BY stat_hour ASC').all(sinceHour) as unknown as Array<Record<string, unknown>>
  return {
    latest: latest
      ? {
          sampledAt: String(latest.sampled_at),
          cpuPercent: numberFromUnknown(latest.cpu_percent),
          memoryUsedPercent: numberFromUnknown(latest.memory_used_percent),
          memoryTotalBytes: numberFromUnknown(latest.memory_total_bytes),
          memoryFreeBytes: numberFromUnknown(latest.memory_free_bytes),
          processRssBytes: numberFromUnknown(latest.process_rss_bytes),
          processHeapUsedBytes: numberFromUnknown(latest.process_heap_used_bytes),
          processHeapTotalBytes: numberFromUnknown(latest.process_heap_total_bytes),
          eventLoopLagMs: numberFromUnknown(latest.event_loop_lag_ms),
          networkRxBytesPerSecond: numberFromUnknown(latest.network_rx_bytes_per_sec),
          networkTxBytesPerSecond: numberFromUnknown(latest.network_tx_bytes_per_sec),
          networkRxTotalBytes: numberFromUnknown(latest.network_rx_total_bytes),
          networkTxTotalBytes: numberFromUnknown(latest.network_tx_total_bytes),
          dbFileBytes: numberFromUnknown(latest.db_file_bytes),
          statsLagSeconds: numberFromUnknown(latest.stats_lag_seconds)
        }
      : undefined,
    hourlyTrend: rows.map((row) => {
      const sampleCount = Number(row.sample_count ?? 0)
      return {
        statHour: String(row.stat_hour),
        sampleCount,
        cpuPercentAvg: averageFromSum(row.cpu_percent_sum, sampleCount),
        cpuPercentMax: numberFromUnknown(row.cpu_percent_max),
        memoryUsedPercentAvg: averageFromSum(row.memory_used_percent_sum, sampleCount),
        memoryUsedPercentMax: numberFromUnknown(row.memory_used_percent_max),
        eventLoopLagMsAvg: averageFromSum(row.event_loop_lag_ms_sum, sampleCount),
        eventLoopLagMsMax: numberFromUnknown(row.event_loop_lag_ms_max),
        networkRxBytesPerSecondAvg: averageFromSum(row.network_rx_bytes_per_sec_sum, row.network_rx_bytes_per_sec_count),
        networkRxBytesPerSecondMax: numberFromUnknown(row.network_rx_bytes_per_sec_max),
        networkTxBytesPerSecondAvg: averageFromSum(row.network_tx_bytes_per_sec_sum, row.network_tx_bytes_per_sec_count),
        networkTxBytesPerSecondMax: numberFromUnknown(row.network_tx_bytes_per_sec_max),
        networkRxTotalBytesMax: numberFromUnknown(row.network_rx_total_bytes_max),
        networkTxTotalBytesMax: numberFromUnknown(row.network_tx_total_bytes_max),
        processRssBytesMax: numberFromUnknown(row.process_rss_bytes_max),
        processHeapUsedBytesMax: numberFromUnknown(row.process_heap_used_bytes_max),
        dbFileBytesMax: numberFromUnknown(row.db_file_bytes_max),
        statsLagSecondsMax: numberFromUnknown(row.stats_lag_seconds_max)
      }
    })
  }
}

function usageStatsJobState(database: DatabaseSync): { cursorCreatedAt: string; cursorId: string } {
  const row = database
    .prepare("SELECT cursor_created_at, cursor_id FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'")
    .get() as unknown as StatsJobStateRow | undefined
  return { cursorCreatedAt: row?.cursor_created_at ?? '', cursorId: row?.cursor_id ?? '' }
}

function aggregateUsageStatsRecord(database: DatabaseSync, row: UsageStatsRecordRow, updatedAt: string): void {
  const createdAt = new Date(row.created_at)
  const statDate = dateKey(createdAt)
  const statHour = hourKey(createdAt)
  for (const entry of usageStatsEntries(row)) {
    upsertUsageStatsTotal(database, row.system_account_id, entry.scopeType, entry.scopeId, entry.accumulator, updatedAt)
    upsertUsageStatsDaily(database, row.system_account_id, entry.scopeType, entry.scopeId, statDate, entry.accumulator, updatedAt)
    upsertUsageStatsHourly(database, row.system_account_id, entry.scopeType, entry.scopeId, statHour, entry.accumulator, updatedAt)
    upsertUsageStatsClient(database, row, entry.scopeType, entry.scopeId, 'all')
    upsertUsageStatsClient(database, row, entry.scopeType, entry.scopeId, statDate)
  }
  if (row.model) upsertUsageModelDaily(database, row, statDate, updatedAt)
  if (row.success !== 1) upsertUsageErrorDaily(database, row, statDate, updatedAt)
}

function usageStatsEntries(row: UsageStatsRecordRow): Array<{ scopeType: string; scopeId: string; accumulator: UsageStatsAccumulator }> {
  const accumulator = usageStatsAccumulatorFromRecord(row)
  const entries = [{ scopeType: 'system_account', scopeId: row.system_account_id, accumulator }]
  if (row.provider_code) entries.push({ scopeType: 'provider', scopeId: row.provider_code, accumulator })
  if (row.group_id) entries.push({ scopeType: 'group', scopeId: row.group_id, accumulator })
  if (row.account_id) entries.push({ scopeType: 'account', scopeId: row.account_id, accumulator })
  if (row.api_key_id) entries.push({ scopeType: 'api_key', scopeId: row.api_key_id, accumulator })
  if (row.model) entries.push({ scopeType: 'model', scopeId: row.model, accumulator })
  if (row.endpoint) entries.push({ scopeType: 'endpoint', scopeId: row.endpoint, accumulator })
  return entries
}

function usageStatsAccumulatorFromRecord(row: UsageStatsRecordRow): UsageStatsAccumulator {
  const success = row.success === 1
  return {
    requestCount: 1,
    successCount: success ? 1 : 0,
    errorCount: success ? 0 : 1,
    inputTokens: Math.max(0, Number(row.input_tokens ?? 0)),
    outputTokens: Math.max(0, Number(row.output_tokens ?? 0)),
    cacheReadTokens: Math.max(0, Number(row.cache_read_tokens ?? 0)),
    totalCostUsd: Math.max(0, Number(row.cost_usd ?? 0)),
    durationMsSum: row.duration_ms === null ? 0 : Math.max(0, Number(row.duration_ms ?? 0)),
    durationMsCount: row.duration_ms === null ? 0 : 1,
    firstTokenMsSum: row.first_token_ms === null ? 0 : Math.max(0, Number(row.first_token_ms ?? 0)),
    firstTokenMsCount: row.first_token_ms === null ? 0 : 1,
    lastUsedAt: row.created_at,
    lastErrorAt: success ? undefined : row.created_at
  }
}

function upsertUsageStatsTotal(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, total_cost_usd, duration_ms_sum, duration_ms_count,
      first_token_ms_sum, first_token_ms_count, last_used_at, last_error_at, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum,
      duration_ms_count = duration_ms_count + excluded.duration_ms_count,
      first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
      first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
      last_used_at = CASE WHEN excluded.last_used_at IS NULL THEN usage_stats_totals.last_used_at WHEN usage_stats_totals.last_used_at IS NULL OR excluded.last_used_at > usage_stats_totals.last_used_at THEN excluded.last_used_at ELSE usage_stats_totals.last_used_at END,
      last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN usage_stats_totals.last_error_at WHEN usage_stats_totals.last_error_at IS NULL OR excluded.last_error_at > usage_stats_totals.last_error_at THEN excluded.last_error_at ELSE usage_stats_totals.last_error_at END,
      updated_at = excluded.updated_at
  `).run(systemAccountId, scopeType, scopeId, ...statsParamsTail(stats, updatedAt))
}

function upsertUsageStatsDaily(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string, statDate: string, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, total_cost_usd, duration_ms_sum, duration_ms_count,
      first_token_ms_sum, first_token_ms_count, last_used_at, last_error_at, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id, stat_date) DO UPDATE SET
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum,
      duration_ms_count = duration_ms_count + excluded.duration_ms_count,
      first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
      first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
      last_used_at = CASE WHEN excluded.last_used_at IS NULL THEN usage_stats_daily.last_used_at WHEN usage_stats_daily.last_used_at IS NULL OR excluded.last_used_at > usage_stats_daily.last_used_at THEN excluded.last_used_at ELSE usage_stats_daily.last_used_at END,
      last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN usage_stats_daily.last_error_at WHEN usage_stats_daily.last_error_at IS NULL OR excluded.last_error_at > usage_stats_daily.last_error_at THEN excluded.last_error_at ELSE usage_stats_daily.last_error_at END,
      updated_at = excluded.updated_at
  `).run(systemAccountId, scopeType, scopeId, statDate, ...statsParamsTail(stats, updatedAt))
}

function upsertUsageStatsHourly(database: DatabaseSync, systemAccountId: string, scopeType: string, scopeId: string, statHour: string, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    INSERT INTO usage_stats_hourly (system_account_id, scope_type, scope_id, stat_hour, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, total_cost_usd, duration_ms_sum, duration_ms_count,
      first_token_ms_sum, first_token_ms_count, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id, stat_hour) DO UPDATE SET
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum,
      duration_ms_count = duration_ms_count + excluded.duration_ms_count,
      first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
      first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
      updated_at = excluded.updated_at
  `).run(systemAccountId, scopeType, scopeId, statHour, stats.requestCount, stats.successCount, stats.errorCount, stats.inputTokens, stats.outputTokens, stats.cacheReadTokens, stats.totalCostUsd, stats.durationMsSum, stats.durationMsCount, stats.firstTokenMsSum, stats.firstTokenMsCount, updatedAt)
}

function statsParamsTail(stats: UsageStatsAccumulator, updatedAt: string): Array<number | string | null> {
  return [stats.requestCount, stats.successCount, stats.errorCount, stats.inputTokens, stats.outputTokens, stats.cacheReadTokens, stats.totalCostUsd, stats.durationMsSum, stats.durationMsCount, stats.firstTokenMsSum, stats.firstTokenMsCount, stats.lastUsedAt ?? null, stats.lastErrorAt ?? null, updatedAt]
}

function upsertUsageStatsClient(database: DatabaseSync, row: UsageStatsRecordRow, scopeType: string, scopeId: string, statBucket: string): void {
  const clientKey = row.api_key_id ?? row.client_ip ?? ''
  if (!clientKey) return
  const result = database.prepare(`
    INSERT OR IGNORE INTO usage_stats_clients (system_account_id, scope_type, scope_id, stat_bucket, client_key, first_seen_at, last_seen_at)
    VALUES (?, ?, ?, ?, ?, ?, ?)
  `).run(row.system_account_id, scopeType, scopeId, statBucket, clientKey, row.created_at, row.created_at)
  if (result.changes <= 0) {
    database.prepare(`
      UPDATE usage_stats_clients
      SET last_seen_at = ?
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_bucket = ? AND client_key = ?
    `).run(row.created_at, row.system_account_id, scopeType, scopeId, statBucket, clientKey)
    return
  }
  if (statBucket === 'all') {
    database.prepare('UPDATE usage_stats_totals SET client_count = client_count + 1 WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?').run(row.system_account_id, scopeType, scopeId)
    return
  }
  database.prepare('UPDATE usage_stats_daily SET client_count = client_count + 1 WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_date = ?').run(row.system_account_id, scopeType, scopeId, statBucket)
}

function upsertUsageModelDaily(database: DatabaseSync, row: UsageStatsRecordRow, statDate: string, updatedAt: string): void {
  const stats = usageStatsAccumulatorFromRecord(row)
  database.prepare(`
    INSERT INTO usage_model_daily (system_account_id, stat_date, provider_code, model, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, total_cost_usd, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, stat_date, model) DO UPDATE SET
      provider_code = excluded.provider_code,
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      updated_at = excluded.updated_at
  `).run(row.system_account_id, statDate, row.provider_code ?? 'unknown', row.model ?? 'unknown', stats.requestCount, stats.successCount, stats.errorCount, stats.inputTokens, stats.outputTokens, stats.cacheReadTokens, stats.totalCostUsd, updatedAt)
}

function upsertUsageErrorDaily(database: DatabaseSync, row: UsageStatsRecordRow, statDate: string, updatedAt: string): void {
  const errorGroup = row.provider_code ?? 'unknown'
  const errorCode = row.error_code ?? String(row.status_code ?? 'unknown')
  database.prepare(`
    INSERT INTO usage_error_daily (system_account_id, stat_date, error_group, provider_code, error_code, status_code, error_message, request_count, error_count, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1, ?)
    ON CONFLICT(system_account_id, stat_date, error_group, error_code) DO UPDATE SET
      provider_code = excluded.provider_code,
      status_code = excluded.status_code,
      error_message = COALESCE(excluded.error_message, usage_error_daily.error_message),
      request_count = request_count + excluded.request_count,
      error_count = error_count + excluded.error_count,
      updated_at = excluded.updated_at
  `).run(row.system_account_id, statDate, errorGroup, row.provider_code ?? 'unknown', errorCode, row.status_code ?? 0, row.error_message ?? null, updatedAt)
}

function upsertSystemMetricsHourly(database: DatabaseSync, statHour: string, input: SystemMetricsSampleInput, updatedAt: string): void {
  database.prepare(`
    INSERT INTO system_metrics_hourly (
      stat_hour, sample_count, cpu_percent_sum, cpu_percent_max, memory_used_percent_sum,
      memory_used_percent_max, process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum,
      process_heap_used_bytes_max, event_loop_lag_ms_sum, event_loop_lag_ms_max,
      network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_max, network_rx_bytes_per_sec_count,
      network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_max, network_tx_bytes_per_sec_count,
      network_rx_total_bytes_max, network_tx_total_bytes_max,
      db_file_bytes_max, stats_lag_seconds_max, updated_at
    )
    VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(stat_hour) DO UPDATE SET
      sample_count = sample_count + 1,
      cpu_percent_sum = cpu_percent_sum + excluded.cpu_percent_sum,
      cpu_percent_max = CASE WHEN excluded.cpu_percent_max IS NULL THEN system_metrics_hourly.cpu_percent_max WHEN system_metrics_hourly.cpu_percent_max IS NULL OR excluded.cpu_percent_max > system_metrics_hourly.cpu_percent_max THEN excluded.cpu_percent_max ELSE system_metrics_hourly.cpu_percent_max END,
      memory_used_percent_sum = memory_used_percent_sum + excluded.memory_used_percent_sum,
      memory_used_percent_max = CASE WHEN excluded.memory_used_percent_max IS NULL THEN system_metrics_hourly.memory_used_percent_max WHEN system_metrics_hourly.memory_used_percent_max IS NULL OR excluded.memory_used_percent_max > system_metrics_hourly.memory_used_percent_max THEN excluded.memory_used_percent_max ELSE system_metrics_hourly.memory_used_percent_max END,
      process_rss_bytes_sum = process_rss_bytes_sum + excluded.process_rss_bytes_sum,
      process_rss_bytes_max = CASE WHEN excluded.process_rss_bytes_max IS NULL THEN system_metrics_hourly.process_rss_bytes_max WHEN system_metrics_hourly.process_rss_bytes_max IS NULL OR excluded.process_rss_bytes_max > system_metrics_hourly.process_rss_bytes_max THEN excluded.process_rss_bytes_max ELSE system_metrics_hourly.process_rss_bytes_max END,
      process_heap_used_bytes_sum = process_heap_used_bytes_sum + excluded.process_heap_used_bytes_sum,
      process_heap_used_bytes_max = CASE WHEN excluded.process_heap_used_bytes_max IS NULL THEN system_metrics_hourly.process_heap_used_bytes_max WHEN system_metrics_hourly.process_heap_used_bytes_max IS NULL OR excluded.process_heap_used_bytes_max > system_metrics_hourly.process_heap_used_bytes_max THEN excluded.process_heap_used_bytes_max ELSE system_metrics_hourly.process_heap_used_bytes_max END,
      event_loop_lag_ms_sum = event_loop_lag_ms_sum + excluded.event_loop_lag_ms_sum,
      event_loop_lag_ms_max = CASE WHEN excluded.event_loop_lag_ms_max IS NULL THEN system_metrics_hourly.event_loop_lag_ms_max WHEN system_metrics_hourly.event_loop_lag_ms_max IS NULL OR excluded.event_loop_lag_ms_max > system_metrics_hourly.event_loop_lag_ms_max THEN excluded.event_loop_lag_ms_max ELSE system_metrics_hourly.event_loop_lag_ms_max END,
      network_rx_bytes_per_sec_sum = network_rx_bytes_per_sec_sum + excluded.network_rx_bytes_per_sec_sum,
      network_rx_bytes_per_sec_max = CASE WHEN excluded.network_rx_bytes_per_sec_max IS NULL THEN system_metrics_hourly.network_rx_bytes_per_sec_max WHEN system_metrics_hourly.network_rx_bytes_per_sec_max IS NULL OR excluded.network_rx_bytes_per_sec_max > system_metrics_hourly.network_rx_bytes_per_sec_max THEN excluded.network_rx_bytes_per_sec_max ELSE system_metrics_hourly.network_rx_bytes_per_sec_max END,
      network_rx_bytes_per_sec_count = network_rx_bytes_per_sec_count + excluded.network_rx_bytes_per_sec_count,
      network_tx_bytes_per_sec_sum = network_tx_bytes_per_sec_sum + excluded.network_tx_bytes_per_sec_sum,
      network_tx_bytes_per_sec_max = CASE WHEN excluded.network_tx_bytes_per_sec_max IS NULL THEN system_metrics_hourly.network_tx_bytes_per_sec_max WHEN system_metrics_hourly.network_tx_bytes_per_sec_max IS NULL OR excluded.network_tx_bytes_per_sec_max > system_metrics_hourly.network_tx_bytes_per_sec_max THEN excluded.network_tx_bytes_per_sec_max ELSE system_metrics_hourly.network_tx_bytes_per_sec_max END,
      network_tx_bytes_per_sec_count = network_tx_bytes_per_sec_count + excluded.network_tx_bytes_per_sec_count,
      network_rx_total_bytes_max = CASE WHEN excluded.network_rx_total_bytes_max IS NULL THEN system_metrics_hourly.network_rx_total_bytes_max WHEN system_metrics_hourly.network_rx_total_bytes_max IS NULL OR excluded.network_rx_total_bytes_max > system_metrics_hourly.network_rx_total_bytes_max THEN excluded.network_rx_total_bytes_max ELSE system_metrics_hourly.network_rx_total_bytes_max END,
      network_tx_total_bytes_max = CASE WHEN excluded.network_tx_total_bytes_max IS NULL THEN system_metrics_hourly.network_tx_total_bytes_max WHEN system_metrics_hourly.network_tx_total_bytes_max IS NULL OR excluded.network_tx_total_bytes_max > system_metrics_hourly.network_tx_total_bytes_max THEN excluded.network_tx_total_bytes_max ELSE system_metrics_hourly.network_tx_total_bytes_max END,
      db_file_bytes_max = CASE WHEN excluded.db_file_bytes_max IS NULL THEN system_metrics_hourly.db_file_bytes_max WHEN system_metrics_hourly.db_file_bytes_max IS NULL OR excluded.db_file_bytes_max > system_metrics_hourly.db_file_bytes_max THEN excluded.db_file_bytes_max ELSE system_metrics_hourly.db_file_bytes_max END,
      stats_lag_seconds_max = CASE WHEN excluded.stats_lag_seconds_max IS NULL THEN system_metrics_hourly.stats_lag_seconds_max WHEN system_metrics_hourly.stats_lag_seconds_max IS NULL OR excluded.stats_lag_seconds_max > system_metrics_hourly.stats_lag_seconds_max THEN excluded.stats_lag_seconds_max ELSE system_metrics_hourly.stats_lag_seconds_max END,
      updated_at = excluded.updated_at
  `).run(
    statHour,
    input.cpuPercent ?? 0,
    input.cpuPercent ?? null,
    input.memoryUsedPercent ?? 0,
    input.memoryUsedPercent ?? null,
    input.processRssBytes ?? 0,
    input.processRssBytes ?? null,
    input.processHeapUsedBytes ?? 0,
    input.processHeapUsedBytes ?? null,
    input.eventLoopLagMs ?? 0,
    input.eventLoopLagMs ?? null,
    input.networkRxBytesPerSecond ?? 0,
    input.networkRxBytesPerSecond ?? null,
    input.networkRxBytesPerSecond === undefined ? 0 : 1,
    input.networkTxBytesPerSecond ?? 0,
    input.networkTxBytesPerSecond ?? null,
    input.networkTxBytesPerSecond === undefined ? 0 : 1,
    input.networkRxTotalBytes ?? null,
    input.networkTxTotalBytes ?? null,
    input.dbFileBytes ?? null,
    input.statsLagSeconds ?? null,
    updatedAt
  )
}

function updateStatsJobState(database: DatabaseSync, input: { cursorCreatedAt?: string; cursorId?: string; lastSuccessAt?: string; lastErrorMessage?: string; lagSeconds?: number }): void {
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('global', '', 'usage_stats_aggregation', ?, ?, ?, ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = COALESCE(excluded.cursor_created_at, stats_job_state.cursor_created_at),
      cursor_id = COALESCE(excluded.cursor_id, stats_job_state.cursor_id),
      last_success_at = COALESCE(excluded.last_success_at, stats_job_state.last_success_at),
      last_error_message = excluded.last_error_message,
      lag_seconds = excluded.lag_seconds,
      updated_at = excluded.updated_at
  `).run(input.cursorCreatedAt ?? null, input.cursorId ?? null, input.lastSuccessAt ?? null, input.lastErrorMessage ?? null, input.lagSeconds ?? 0, nowIso())
}

function cleanupStatsCache(database: DatabaseSync, now: string): void {
  const dailyRetentionDays = settingsNumberValue('usageStatsDailyRetentionDays', 180, 7, 3650)
  const hourlyRetentionDays = settingsNumberValue('usageStatsHourlyRetentionDays', 14, 1, 365)
  const dailyCutoff = dateKey(new Date(Date.parse(now) - dailyRetentionDays * 24 * 60 * 60 * 1000))
  const hourlyCutoff = hourKey(new Date(Date.parse(now) - hourlyRetentionDays * 24 * 60 * 60 * 1000))
  database.prepare('DELETE FROM usage_stats_daily WHERE stat_date < ?').run(dailyCutoff)
  database.prepare('DELETE FROM usage_model_daily WHERE stat_date < ?').run(dailyCutoff)
  database.prepare('DELETE FROM usage_error_daily WHERE stat_date < ?').run(dailyCutoff)
  database.prepare('DELETE FROM usage_stats_hourly WHERE stat_hour < ?').run(hourlyCutoff)
  database.prepare("DELETE FROM usage_stats_clients WHERE stat_bucket <> 'all' AND stat_bucket < ?").run(dailyCutoff)
}

function cleanupSystemMetrics(database: DatabaseSync): void {
  const retentionDays = settingsNumberValue('systemMetricsRetentionDays', 14, 1, 365)
  const cutoff = new Date(Date.now() - retentionDays * 24 * 60 * 60 * 1000).toISOString()
  const hourlyCutoff = hourKey(new Date(Date.now() - 180 * 24 * 60 * 60 * 1000))
  database.prepare('DELETE FROM system_metrics_samples WHERE sampled_at < ?').run(cutoff)
  database.prepare('DELETE FROM system_metrics_hourly WHERE stat_hour < ?').run(hourlyCutoff)
}

function statsLagSecondsFromCursor(cursorCreatedAt: string): number {
  const cursorTime = Date.parse(cursorCreatedAt)
  return Number.isFinite(cursorTime) ? Math.max(0, Math.floor((Date.now() - cursorTime) / 1000)) : 0
}

function visibleSystemAccountIds(access?: AccessScope): string[] {
  const scopedId = scopedSystemAccountId(access)
  if (scopedId) return [scopedId]
  if (canAccessAll(access)) {
    const ids = listSystemAccounts().map((account) => account.id)
    return ids.length ? ids : ['sys_admin']
  }
  return [currentSystemAccountId(access)]
}

function sqlPlaceholders(count: number): string {
  return Array.from({ length: Math.max(1, count) }, () => '?').join(',')
}

function usageSummaryWithMath(row: AccountUsageAggregateRow & StatsAggregateMathRow): AccountUsageSummary & { successCount: number; errorCount: number; errorRate: number; averageDurationMs?: number; averageFirstTokenMs?: number } {
  const summary = usageSummaryFromAggregate(row)
  const successCount = Number(row.success_count ?? 0)
  const errorCount = Number(row.error_count ?? 0)
  const requestCount = Number(row.request_count ?? 0)
  return {
    ...summary,
    successCount,
    errorCount,
    errorRate: requestCount > 0 ? errorCount / requestCount : 0,
    averageDurationMs: averageFromSum(row.duration_ms_sum, row.duration_ms_count),
    averageFirstTokenMs: averageFromSum(row.first_token_ms_sum, row.first_token_ms_count)
  }
}

function averageFromSum(sum: unknown, count: unknown): number | undefined {
  const numericSum = Number(sum ?? 0)
  const numericCount = Number(count ?? 0)
  return numericCount > 0 ? Math.round(numericSum / numericCount) : undefined
}

function settingsNumberValue(key: string, fallback: number, min: number, max: number): number {
  const value = getSettings()[key]
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? Math.min(Math.max(Math.trunc(number), min), max) : fallback
}



