import { randomBytes } from 'node:crypto'

import type { AccountOAuthUsageSnapshot, AccountOAuthUsageWindow, AccountStatus, AccountSummary, AccountType, ApiKeySummary, ErrorPolicySummary, GroupSummary, ProviderCode, ProviderDefinition } from '../domain/types.js'
import { getRequestAuthContext, type RequestAuthContext } from '../modules/auth/request-context.js'
import { createApiKey, decryptJson, encryptJson, hashPassword, hashSecret, maskSecret, verifyPassword } from './crypto.js'
import { getDatabase, newId, nowIso } from './database.js'

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
  system_account_id: string
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
}

export interface SessionWithAccount {
  sessionId: string
  expiresAt: string
  account: SystemAccountSummary
}

export interface ProxyProfileSummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
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
  organizationId?: string
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
  updated_at: string
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

function buildSystemAccountScopeClause(access?: AccessScope, column = 'system_account_id'): { clause: string; params: Array<string> } {
  if (canAccessAll(access)) {
    return { clause: '', params: [] }
  }
  return { clause: ` AND ${column} = ?`, params: [currentSystemAccountId(access)] }
}

function buildSystemAccountWhereClause(access?: AccessScope, column = 'system_account_id'): { clause: string; params: Array<string> } {
  if (canAccessAll(access)) {
    return { clause: '', params: [] }
  }
  return { clause: ` WHERE ${column} = ?`, params: [currentSystemAccountId(access)] }
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

function groupOwnerAndProvider(groupId: string): { systemAccountId: string; providerCode: ProviderCode } | undefined {
  const row = getDatabase().prepare('SELECT system_account_id, provider_code FROM groups WHERE id = ?').get(groupId) as unknown as { system_account_id?: string; provider_code?: ProviderCode } | undefined
  return row?.system_account_id && row.provider_code ? { systemAccountId: row.system_account_id, providerCode: row.provider_code } : undefined
}

function apiKeySystemAccountId(apiKeyId: string): string | undefined {
  const row = getDatabase().prepare('SELECT system_account_id FROM api_keys WHERE id = ?').get(apiKeyId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id
}

function proxySystemAccountId(proxyProfileId: string): string | undefined {
  const row = getDatabase().prepare('SELECT system_account_id FROM proxy_profiles WHERE id = ?').get(proxyProfileId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id
}

function proxyProfileIdForOwner(proxyProfileId: string | undefined, systemAccountId: string): string | undefined {
  if (!proxyProfileId) return undefined
  return proxySystemAccountId(proxyProfileId) === systemAccountId ? proxyProfileId : undefined
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
  getDatabase()
    .prepare(`
      INSERT INTO system_accounts (
        id, username, display_name, role, status, password_hash, must_change_password, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(summary.id, summary.username, summary.displayName, summary.role, summary.status, hashPassword(input.password), summary.mustChangePassword ? 1 : 0, now, now)
  return summary
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
  for (const [key, value] of Object.entries(input)) {
    statement.run(key, JSON.stringify(value), now)
  }
  return listGlobalSettings()
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
  return hashSecret(`${providerCode}:${type}:${baseUrl.trim().replace(/\/+$/, '')}:${secret.trim()}`)
}

function defaultAccountConcurrencyLimit(): number {
  const value = getSettings().defaultAccountConcurrencyLimit
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  if (!Number.isFinite(number)) return 3
  return Math.min(Math.max(Math.trunc(number), 1), 999)
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
  const systemAccountId = currentSystemAccountId()
  const providerCode = String(input.providerCode ?? input.provider_code ?? 'openai')
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
  const initialStatus = normalizeAccountStatus(input.status, 'active')
  const initialCooldownUntil = isCoolingAccountStatus(initialStatus)
    ? new Date(Date.now() + defaultTemporaryUnschedulableMinutes() * 60_000).toISOString()
    : undefined
  const account: AccountSummary = {
    id,
    systemAccountId: includeSystemAccountFields() ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields() ? systemAccountNameMap().get(systemAccountId) : undefined,
    providerCode,
    name: String(input.name ?? `未命名 ${provider?.name ?? providerCode.toUpperCase()} 账户`),
    notes: optionalString(input.notes),
    type: accountType,
    credentials,
    status: initialStatus,
    concurrencyLimit: Number(input.concurrencyLimit ?? input.concurrency_limit ?? defaultAccountConcurrencyLimit()),
    currentConcurrency: 0,
    priority: Number(input.priority ?? input.prioritiy ?? input.priority_level ?? 0),
    proxyProfileId: proxyProfileIdForOwner(optionalString(input.proxyProfileId ?? input.proxy_profile_id), systemAccountId),
    passthroughEnabled: Boolean(input.passthroughEnabled ?? input.passthrough_enabled),
    errorPolicyId: optionalString(input.errorPolicyId ?? input.error_policy_id),
    schedulable: input.schedulable !== false,
    cooldownUntil: initialCooldownUntil,
    lastErrorMessage: initialCooldownUntil ? '创建时设置为临时不可调用' : undefined,
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

  getDatabase()
    .prepare(`
      INSERT INTO accounts (
        id, system_account_id, provider_code, name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
        proxy_profile_id, concurrency_limit, passthrough_enabled, error_policy_id,
        priority, schedulable, notes, cooldown_until, last_error_message, stream_failure_count, stream_failure_window_started_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
      account.cooldownUntil ?? null,
      account.lastErrorMessage ?? null,
      0,
      null,
      now,
      now
    )

  return account
}

export function findAccountByFingerprint(providerCode: string, type: string, baseUrl: string, apiKey: string): AccountSummary | undefined {
  const fingerprint = accountFingerprint(providerCode, type, baseUrl, apiKey)
  const scope = buildSystemAccountScopeClause()
  const row = getDatabase().prepare(`SELECT id FROM accounts WHERE credential_fingerprint = ?${scope.clause}`).get(fingerprint, ...scope.params) as unknown as { id?: string } | undefined
  if (!row?.id) {
    return undefined
  }
  return listAccounts().find((account) => account.id === row.id)
}

export function updateAccount(id: string, input: Record<string, unknown>): AccountSummary | undefined {
  const current = listAccounts().find((account) => account.id === id)
  if (!current) {
    return undefined
  }
  const systemAccountId = accountSystemAccountId(id) ?? currentSystemAccountId()

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

  const rawErrorPolicyId = Object.prototype.hasOwnProperty.call(input, 'errorPolicyId')
    ? input.errorPolicyId
    : Object.prototype.hasOwnProperty.call(input, 'error_policy_id')
      ? input.error_policy_id
      : undefined

  const hasStatusInput = Object.prototype.hasOwnProperty.call(input, 'status')
  const nextStatus = hasStatusInput ? normalizeAccountStatus(input.status, current.status) : current.status
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

  const next: AccountSummary = {
    ...current,
    name: typeof input.name === 'string' ? input.name : current.name,
    notes: optionalString(input.notes) ?? current.notes,
    credentials,
    status: nextStatus,
    concurrencyLimit: Number(input.concurrencyLimit ?? input.concurrency_limit ?? current.concurrencyLimit),
    priority: Number(input.priority ?? input.prioritiy ?? input.priority_level ?? current.priority),
    proxyProfileId: Object.prototype.hasOwnProperty.call(input, 'proxyProfileId') || Object.prototype.hasOwnProperty.call(input, 'proxy_profile_id')
      ? proxyProfileIdForOwner(optionalString(input.proxyProfileId ?? input.proxy_profile_id), systemAccountId)
      : current.proxyProfileId,
    passthroughEnabled: typeof input.passthroughEnabled === 'boolean' ? input.passthroughEnabled : current.passthroughEnabled,
    errorPolicyId: rawErrorPolicyId === undefined ? current.errorPolicyId : optionalString(rawErrorPolicyId),
    schedulable: typeof input.schedulable === 'boolean' ? input.schedulable : current.schedulable,
    cooldownUntil: nextCooldownUntil,
    lastErrorMessage: nextLastErrorMessage,
    lastUsedAt: current.lastUsedAt,
    usage: current.usage
  }

  getDatabase()
    .prepare(`
      UPDATE accounts
      SET name = ?, notes = ?, status = ?, credentials_encrypted = ?, credential_fingerprint = ?, credential_mask = ?,
          proxy_profile_id = ?, concurrency_limit = ?, passthrough_enabled = ?,
          error_policy_id = ?, priority = ?, schedulable = ?, cooldown_until = ?, last_error_message = ?, updated_at = ?
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
      next.cooldownUntil ?? null,
      next.lastErrorMessage ?? null,
      nowIso(),
      id,
      systemAccountId
    )

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
    accountIds: [],
    accountStats: emptyGroupAccountStats()
  }
  getDatabase()
    .prepare('INSERT INTO groups (id, system_account_id, name, provider_code, description, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)')
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
  const systemAccountId = accountSystemAccountId(accountId)
  if (!systemAccountId) {
    return undefined
  }

  if (groupId) {
    const group = groupOwnerAndProvider(groupId)
    if (!group || group.systemAccountId !== systemAccountId) {
      return undefined
    }
    if (group.providerCode !== current.providerCode) {
      return undefined
    }
  }

  database.prepare('DELETE FROM group_accounts WHERE account_id = ? AND system_account_id = ?').run(accountId, systemAccountId)
  if (groupId) {
    const now = nowIso()
    database
      .prepare('INSERT INTO group_accounts (system_account_id, group_id, account_id, weight, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, 1, ?, ?)')
      .run(systemAccountId, groupId, accountId, 1, now, now)
  }

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
  const groupId = String(input.groupId ?? input.group_id ?? 'grp_default_openai')
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

export function listProxies(access?: AccessScope): ProxyProfileSummary[] {
  const scope = buildSystemAccountWhereClause(access)
  const rows = getDatabase().prepare(`SELECT * FROM proxy_profiles${scope.clause} ORDER BY updated_at DESC`).all(...scope.params) as unknown as ProxyRow[]
  const accountNames = includeSystemAccountFields(access) ? systemAccountNameMap() : new Map<string, string>()
  return rows.map((row) => proxySummaryFromRow(row, includeSystemAccountFields(access), accountNames))
}

function proxySummaryFromRow(row: ProxyRow, includeOwner = false, accountNames = new Map<string, string>()): ProxyProfileSummary {
  return {
    id: row.id,
    systemAccountId: includeOwner ? row.system_account_id : undefined,
    systemAccountName: includeOwner ? accountNames.get(row.system_account_id) : undefined,
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
  const systemAccountId = currentSystemAccountId()
  const proxy: ProxyProfileSummary = {
    id: newId('proxy'),
    systemAccountId: includeSystemAccountFields() ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields() ? systemAccountNameMap().get(systemAccountId) : undefined,
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
      INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, username, password_encrypted, enabled, test_status, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(proxy.id, systemAccountId, proxy.name, proxy.type, proxy.host, proxy.port, proxy.username ?? null, input.password ? encryptJson({ password: input.password }) : null, proxy.enabled ? 1 : 0, proxy.testStatus, now, now)
  return proxy
}

export function updateProxy(id: string, input: Record<string, unknown>): ProxyProfileSummary | undefined {
  const current = listProxies().find((proxy) => proxy.id === id)
  if (!current) {
    return undefined
  }
  const systemAccountId = proxySystemAccountId(id)
  if (!systemAccountId) {
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
      WHERE id = ? AND system_account_id = ?
    `)
    .run(next.name, next.type, next.host, next.port, next.username ?? null, next.enabled ? 1 : 0, nowIso(), id, systemAccountId)
  return next
}

export function deleteProxy(id: string): boolean {
  const scope = buildSystemAccountScopeClause()
  const result = getDatabase().prepare(`DELETE FROM proxy_profiles WHERE id = ?${scope.clause}`).run(id, ...scope.params)
  return result.changes > 0
}

function proxyUrlForProfile(proxyProfileId?: string | null, systemAccountId?: string): string | undefined {
  if (!proxyProfileId) return undefined
  const ownerClause = systemAccountId ? ' AND system_account_id = ?' : ''
  const params = systemAccountId ? [proxyProfileId, systemAccountId] : [proxyProfileId]
  const row = getDatabase()
    .prepare(`SELECT type, host, port, username, password_encrypted FROM proxy_profiles WHERE id = ? AND enabled = 1${ownerClause}`)
    .get(...params) as unknown as ProxyRow | undefined
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

export function listUsageRecords(access?: AccessScope): UsageRecordSummary[] {
  const scope = buildSystemAccountWhereClause(access, 'ur.system_account_id')
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
      ORDER BY ur.created_at DESC
      LIMIT 200
    `)
    .all(...scope.params) as Array<Record<string, unknown>>
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
          AND (
            (status = 'active' AND (cooldown_until IS NULL OR cooldown_until <= ?))
            OR (status IN ('rate_limited', 'temporary_unavailable') AND cooldown_until IS NOT NULL AND cooldown_until <= ?)
          )
      `)
      .get(groupAccount.account_id, now, now) as unknown as { id: string; system_account_id: string; name: string; type: AccountType; status: AccountStatus; credentials_encrypted: string; proxy_profile_id: string | null; error_policy_id: string | null; cooldown_until: string | null; last_error_message: string | null } | undefined
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
      proxyUrl: proxyUrlForProfile(row.proxy_profile_id, row.system_account_id),
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
      INSERT INTO account_usage_snapshots (system_account_id, account_id, kind, source, snapshot_json, updated_at, created_at)
      VALUES (?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(account_id, kind) DO UPDATE SET
        system_account_id = excluded.system_account_id,
        source = excluded.source,
        snapshot_json = excluded.snapshot_json,
        updated_at = excluded.updated_at
    `)
    .run(
      systemAccountId,
      input.accountId,
      input.kind,
      input.source ?? null,
      JSON.stringify(input.snapshot),
      updatedAt,
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
  return proxyUrlForProfile(proxyProfileId, currentSystemAccountId())
}

function loadAccountUsageSummaries(access?: AccessScope): Map<string, AccountUsageSummary> {
  const scope = buildSystemAccountScopeClause(access)
  const rows = getDatabase().prepare(`
    SELECT
      account_id,
      COUNT(*) AS request_count,
      COUNT(DISTINCT api_key_id) AS client_count,
      COALESCE(SUM(COALESCE(input_tokens, 0)), 0) AS input_tokens,
      COALESCE(SUM(COALESCE(output_tokens, 0)), 0) AS output_tokens,
      COALESCE(SUM(COALESCE(cache_read_tokens, 0)), 0) AS cache_read_tokens,
      COALESCE(SUM(COALESCE(cost_usd, 0)), 0) AS total_cost,
      MAX(created_at) AS last_used_at
    FROM usage_records
    WHERE account_id IS NOT NULL${scope.clause}
    GROUP BY account_id
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
      group_id,
      COUNT(*) AS request_count,
      COUNT(DISTINCT api_key_id) AS client_count,
      COALESCE(SUM(COALESCE(input_tokens, 0)), 0) AS input_tokens,
      COALESCE(SUM(COALESCE(output_tokens, 0)), 0) AS output_tokens,
      COALESCE(SUM(COALESCE(cache_read_tokens, 0)), 0) AS cache_read_tokens,
      COALESCE(SUM(COALESCE(cost_usd, 0)), 0) AS total_cost,
      MAX(created_at) AS last_used_at
    FROM usage_records
    WHERE group_id IS NOT NULL AND created_at >= ?${scope.clause}
    GROUP BY group_id
  `).all(startOfTodayIso(), ...scope.params) as unknown as Array<AccountUsageAggregateRow & { group_id: string }>

  const result = new Map<string, AccountUsageSummary>()
  for (const row of rows) {
    result.set(row.group_id, usageSummaryFromAggregate(row))
  }
  return result
}

function loadOpenAICodexUsageSnapshots(access?: AccessScope): Map<string, AccountOAuthUsageSnapshot> {
  const scope = buildSystemAccountScopeClause(access)
  const rows = getDatabase().prepare(`
    SELECT account_id, kind, source, snapshot_json, updated_at
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
  const group = existingGroup ?? (input.dryRun ? { id: 'dry_run_group', name: groupName, enabled: true, accountIds: [] } : createGroup({ name: groupName, description: '从 sub2api 迁移的 OpenAI API Key 与 OAuth 账户' }))
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
      concurrencyLimit: 1,
      passthroughEnabled: true,
      schedulable: true,
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
    if (account.organizationId) credentials.organization_id = account.organizationId
    if (account.planType) credentials.plan_type = account.planType

    const created = createAccount({
      name: account.name,
      type: 'oauth',
      credentials,
      status: 'active',
      concurrencyLimit: 3,
      proxyProfileId: account.proxyProfileId,
      passthroughEnabled: true,
      schedulable: true,
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
  return key === 'apiKeyPrefix' || key === 'defaultOpenAIBaseUrl' || key === 'defaultErrorPolicyId' || key.startsWith('_migration')
}
