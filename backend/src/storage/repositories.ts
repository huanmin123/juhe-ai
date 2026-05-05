import { randomBytes } from 'node:crypto'
import type { DatabaseSync } from 'node:sqlite'

import type { AccountAuthorizationUsageOverview, AccountOAuthUsageSnapshot, AccountOAuthUsageWindow, AccountStatus, AccountSummary, AccountType, AccountUsageStatsOverview, ApiKeySummary, AuthorizationStatus, ErrorPolicySummary, GroupSummary, ProviderCode, ProviderDefinition, ResourceAuthorizationResourceType, ResourceAuthorizationSourceStatus, ResourceAuthorizationSourceSummary, ResourceAuthorizationSourceType, ResourceAuthorizationSummary, ResourceAuthorizationUsageDetail, ResourcePermissions, SystemTeamMemberStatus, SystemTeamMemberSummary, SystemTeamStatus, SystemTeamSummary, UsageByWindow } from '../domain/types.js'
import { createAppCache } from '../shared/cache.js'
import { buildSystemAccountScopeClause, buildSystemAccountWhereClause, canAccessAll, currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, resolveAccessScope, scopedSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { getAccountAuthorizationUsageOverview as buildAccountAuthorizationUsageOverview, getAccountUsageStatsOverview as buildAccountUsageStatsOverview } from './account-usage.repository.js'
import { createApiKey, decryptJson, encryptJson, hashPassword, hashSecret, maskSecret, verifyPassword } from './crypto.js'
import { getDatabase, newId, nowIso } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { addUsageSummaries, dateKey, emptyAccountUsageSummary, emptyUsageByWindow, mergeUsageSummaryMaps, numberFromUnknown, todayDateKey, USAGE_STATS_WINDOWS, usageSummaryFromAggregate } from './usage-stats-helpers.js'
import {
  jsonObjectOrNull,
  optionalNullableServerDateTimeIso,
  optionalNullableString,
  optionalServerDateTimeIso,
  optionalString,
  parseJsonArray,
  parseJsonRules,
  parseOptionalJsonObject
} from './value-utils.js'

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

type GatewayApiKeyCacheEntry = {
  row: GatewayApiKeyRow
  forceRevalidateAtMs: number
}

interface GroupAccountRow {
  account_id: string
}

interface SystemTeamRow {
  id: string
  name: string
  description: string | null
  status: SystemTeamStatus
  created_by: string
  created_at: string
  updated_at: string
}

interface SystemTeamMemberRow {
  id: string
  team_id: string
  system_account_id: string
  member_role: 'member'
  status: SystemTeamMemberStatus
  joined_at: string
  removed_at: string | null
  created_by: string
  created_at: string
  updated_at: string
}

interface ResourceAuthorizationRow {
  id: string
  resource_type: ResourceAuthorizationResourceType
  resource_id: string
  resource_owner_system_account_id: string
  grantee_system_account_id: string
  scope: 'use'
  status: AuthorizationStatus
  effective_source_type: ResourceAuthorizationSourceType | null
  effective_source_team_id: string | null
  activated_at: string | null
  last_source_changed_at: string | null
  remark: string | null
  expires_at: string | null
  limits_json: string | null
  model_policy_json: string | null
  created_by: string
  created_at: string
  revoked_by: string | null
  revoked_at: string | null
  revoked_reason: string | null
  updated_at: string
}

interface ResourceAuthorizationSourceRow {
  id: string
  authorization_id: string
  source_type: ResourceAuthorizationSourceType
  source_team_id: string | null
  status: ResourceAuthorizationSourceStatus
  activated_at: string | null
  ended_at: string | null
  ended_reason: string | null
  created_by: string
  created_at: string
  revoked_by: string | null
  revoked_at: string | null
  updated_at: string
}

interface TeamResourceAuthorizationGrantRow {
  id: string
  resource_type: ResourceAuthorizationResourceType
  resource_id: string
  resource_owner_system_account_id: string
  team_id: string
  scope: 'use'
  status: AuthorizationStatus
  remark: string | null
  expires_at: string | null
  limits_json: string | null
  model_policy_json: string | null
  created_by: string
  created_at: string
  revoked_by: string | null
  revoked_at: string | null
  updated_at: string
}

type AccountListRow = AccountRow & {
  access_type?: 'owner' | 'authorized'
  authorization_id?: string | null
  authorization_status?: AuthorizationStatus | null
}

type GroupListRow = GroupRow & {
  access_type?: 'owner' | 'authorized'
  authorization_id?: string | null
  authorization_status?: AuthorizationStatus | null
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
const GATEWAY_API_KEY_CACHE_TTL_MS = 60_000
const GATEWAY_API_KEY_CACHE_MAX_STALE_MS = 5 * 60_000
const gatewayApiKeyCache = createAppCache<string, GatewayApiKeyCacheEntry>({
  name: 'gateway:api-key-validation',
  max: 10000,
  ttlMs: GATEWAY_API_KEY_CACHE_TTL_MS,
  updateAgeOnGet: true
})

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
  accountOwnerSystemAccountId: string
  groupOwnerSystemAccountId: string
  accountAccessType: 'owner' | 'account_authorized' | 'group_authorized'
  groupAccessType: 'owner' | 'authorized'
  accountAuthorizationId?: string
  groupAuthorizationId?: string
  name: string
  type: AccountType
  status: AccountStatus
  baseUrl: string
  apiKey: string
  refreshToken?: string
  clientId?: string
  proxyUrl?: string
  passthroughEnabled: boolean
  errorPolicyId?: string
  cooldownUntil?: string
  lastErrorMessage?: string
  expiresAt?: string
  credentials: Record<string, unknown>
}

export interface GroupUsageAccessMetadata {
  groupOwnerSystemAccountId: string
  groupAccessType: 'owner' | 'authorized'
  groupAuthorizationId?: string
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

function ownerPermissions(): ResourcePermissions {
  return {
    canUse: true,
    canEdit: true,
    canDelete: true,
    canAuthorize: true,
    canViewCredentials: true,
    canManageAccounts: true
  }
}

function authorizedPermissions(): ResourcePermissions {
  return {
    canUse: true,
    canEdit: false,
    canDelete: false,
    canAuthorize: false,
    canViewCredentials: false,
    canManageAccounts: false
  }
}

function systemAccountNameMap(): Map<string, string> {
  return new Map(listSystemAccounts().map((account) => [account.id, account.displayName || account.username]))
}

function accountSystemAccountId(accountId: string): string | undefined {
  const row = getDatabase().prepare('SELECT system_account_id FROM accounts WHERE id = ?').get(accountId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id
}

function canUseAccount(accountId: string, systemAccountId: string): boolean {
  const ownerId = accountSystemAccountId(accountId)
  if (ownerId === systemAccountId) return true
  return Boolean(activeResourceAuthorization('account', accountId, systemAccountId))
}

function canScheduleAuthorizedAccount(input: {
  accountId: string
  accountAccessType: 'owner' | 'account_authorized' | 'group_authorized'
  authorizationId?: string
  systemAccountId: string
}): boolean {
  if (input.accountAccessType === 'owner' || input.accountAccessType === 'group_authorized') {
    return true
  }
  if (!input.authorizationId) {
    return false
  }
  const authorization = activeAccountAuthorization(input.accountId, input.systemAccountId)
  return authorization?.id === input.authorizationId
}

function canUseGroup(groupId: string, systemAccountId: string): boolean {
  const group = groupOwnerAndProvider(groupId)
  if (group?.systemAccountId === systemAccountId) return true
  return Boolean(activeResourceAuthorization('group', groupId, systemAccountId))
}

function activeAccountAuthorization(accountId: string, granteeSystemAccountId: string): ResourceAuthorizationRow | undefined {
  return activeResourceAuthorization('account', accountId, granteeSystemAccountId)
}

function activeGroupAuthorization(groupId: string, granteeSystemAccountId: string): ResourceAuthorizationRow | undefined {
  return activeResourceAuthorization('group', groupId, granteeSystemAccountId)
}

function activeResourceAuthorization(resourceType: ResourceAuthorizationResourceType, resourceId: string, granteeSystemAccountId: string): ResourceAuthorizationRow | undefined {
  const now = nowIso()
  return getDatabase()
    .prepare("SELECT * FROM resource_authorizations WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?) LIMIT 1")
    .get(resourceType, resourceId, granteeSystemAccountId, now) as unknown as ResourceAuthorizationRow | undefined
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
    role: input.role ?? 'user',
    status: input.status ?? 'active',
    mustChangePassword: input.mustChangePassword ?? true,
    createdAt: now,
    updatedAt: now
  }
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
    displayName: input.displayName?.trim() || current.displayName,
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
        SET display_name = ?, role = ?, status = ?, password_hash = ?, must_change_password = ?, updated_at = ?
        WHERE id = ?
      `)
      .run(next.displayName, next.role, next.status, hashPassword(input.password), next.mustChangePassword ? 1 : 0, now, id)
  } else {
    getDatabase()
      .prepare(`
        UPDATE system_accounts
        SET display_name = ?, role = ?, status = ?, must_change_password = ?, updated_at = ?
        WHERE id = ?
      `)
      .run(next.displayName, next.role, next.status, next.mustChangePassword ? 1 : 0, now, id)
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

function isAccountExpired(accountExpiresAt: string | null | undefined, now = Date.now()): boolean {
  if (!accountExpiresAt) return false
  const timestamp = Date.parse(accountExpiresAt)
  return Number.isFinite(timestamp) && timestamp <= now
}

function isResourceAuthorizationExpired(expiresAt: string | null | undefined, now = Date.now()): boolean {
  if (!expiresAt) return false
  const timestamp = Date.parse(expiresAt)
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
        AND (
          status <> 'disabled'
          OR schedulable <> 0
          OR cooldown_until IS NOT NULL
          OR last_error_message IS NULL
        )${scope.clause}
    `)
    .run('账户套餐已过期，已自动停用', now, now, ...scope.params)
}

export function expireDueResourceAuthorizations(): number {
  const now = nowIso()
  const result = getDatabase()
    .prepare(`
      UPDATE resource_authorizations
      SET status = 'expired',
          revoked_at = COALESCE(revoked_at, ?),
          revoked_reason = 'authorization_expired',
          updated_at = ?
      WHERE status IN ('active', 'paused')
        AND expires_at IS NOT NULL
        AND expires_at <= ?
    `)
    .run(now, now, now)
  return Number(result.changes ?? 0)
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

type GroupAccountStatsRow = {
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

function groupAccountStatsFromRow(row: GroupAccountStatsRow | undefined, todayUsage?: AccountUsageSummary, totalUsage?: AccountUsageSummary): GroupAccountStats {
  return {
    total: Number(row?.total ?? 0),
    available: Number(row?.available ?? 0),
    active: Number(row?.active ?? 0),
    disabled: Number(row?.disabled ?? 0),
    error: Number(row?.error ?? 0),
    rateLimited: Number(row?.rate_limited ?? 0),
    currentConcurrency: Number(row?.current_concurrency ?? 0),
    concurrencyLimit: Number(row?.concurrency_limit ?? 0),
    todayUsage: todayUsage ?? emptyAccountUsageSummary(),
    usage: totalUsage ?? emptyAccountUsageSummary()
  }
}

function isLaterIso(value?: string, current?: string): boolean {
  if (!value) return false
  if (!current) return true
  const nextTime = Date.parse(value)
  const currentTime = Date.parse(current)
  return Number.isFinite(nextTime) && (!Number.isFinite(currentTime) || nextTime > currentTime)
}

function canManageResourceOwner(ownerSystemAccountId: string, access?: AccessScope): boolean {
  const scopedOwnerId = manageableSystemAccountId(access)
  if (scopedOwnerId) return scopedOwnerId === ownerSystemAccountId
  return canAccessAll(access)
}

function validAccountIdsForGroup(providerCode: string, accountIds: string[], systemAccountId = currentSystemAccountId()): string[] {
  const uniqueIds = [...new Set(accountIds)]
  const accountsById = new Map(listAccounts({ systemAccountId, role: 'user' }).map((account) => [account.id, account]))
  return uniqueIds.filter((accountId) => {
    const account = accountsById.get(accountId)
    return account?.providerCode === providerCode && canUseAccount(accountId, systemAccountId)
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
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  disableExpiredAccounts(access)
  const rows = listAccountRowsForAccess(access)
  const accountIds = rows.map((row) => row.id)
  const usageByAccount = loadAccountUsageSummariesByAccountIds(accountIds)
  const todayUsageByAccount = loadAccountUsageSummariesByAccountIds(accountIds, todayDateKey())
  const authorizationStatsByAccount = loadResourceAuthorizationStatsByResourceIds('account', accountIds)
  const authorizationIds = rows.map((row) => row.authorization_id ?? '')
  const usageByAuthorization = loadAccountAuthorizationUsageSummaries(authorizationIds)
  const todayUsageByAuthorization = loadAccountAuthorizationUsageSummaries(authorizationIds, todayDateKey())
  const sourcesByAuthorization = loadResourceAuthorizationSourcesByAuthorizationIds(rows.map((row) => row.authorization_id ?? ''))
  const oauthUsageByAccount = loadOpenAICodexUsageSnapshotsByAccountIds(rows.map((row) => row.id))
  const hasAuthorizedRows = rows.some((row) => row.access_type === 'authorized')
  const accountNames = includeSystemAccountFields(access) || hasAuthorizedRows ? systemAccountNameMap() : new Map<string, string>()
  return rows.map((row) => {
    const usage = row.access_type === 'authorized' && row.authorization_id
      ? usageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : usageByAccount.get(row.id) ?? emptyAccountUsageSummary()
    const todayUsage = row.access_type === 'authorized' && row.authorization_id
      ? todayUsageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : todayUsageByAccount.get(row.id) ?? emptyAccountUsageSummary()
    const authorizationStats = authorizationStatsByAccount.get(row.id) ?? { authorizationCount: 0, authorizationTeamCount: 0 }
    return {
    id: row.id,
    systemAccountId: includeSystemAccountFields(access) ? row.system_account_id : undefined,
    systemAccountName: includeSystemAccountFields(access) ? accountNames.get(row.system_account_id) : undefined,
    ownerSystemAccountId: row.system_account_id,
    ownerSystemAccountName: accountNames.get(row.system_account_id),
    providerCode: row.provider_code,
    name: row.name,
    notes: row.notes ?? undefined,
    type: row.type,
    credentials: accountCredentialsForList(row),
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
    lastUsedAt: row.access_type === 'authorized' ? usage.lastUsedAt : row.last_used_at ?? usage.lastUsedAt,
    todayUsage,
    usage,
    oauthUsage: row.provider_code === 'openai' && row.type === 'oauth' ? oauthUsageByAccount.get(row.id) : undefined,
    accessType: row.access_type ?? 'owner',
    accountAuthorizationId: row.authorization_id ?? undefined,
    authorizationStatus: row.authorization_status ?? undefined,
    authorizationSources: row.authorization_id ? sourcesByAuthorization.get(row.authorization_id) ?? [] : undefined,
    permissions: row.access_type === 'authorized' && row.system_account_id !== viewerSystemAccountId ? authorizedPermissions() : ownerPermissions(),
    authorizationUsageAvailable: row.access_type !== 'authorized' && authorizationStats.authorizationCount > 0 && canManageResourceOwner(row.system_account_id, access),
    authorizationCount: authorizationStats.authorizationCount,
    authorizationTeamCount: authorizationStats.authorizationTeamCount
    }
  })
}

export function getAccountUsageStatsOverview(access?: AccessScope): AccountUsageStatsOverview {
  const accountRows = listAccounts(access)
  return buildAccountUsageStatsOverview({
    access,
    accounts: accountRows,
    loadUsageByWindow: loadUsageByWindowForScopeRequests
  })
}

export function getAccountAuthorizationUsageOverview(accountId: string, access?: AccessScope): AccountAuthorizationUsageOverview | undefined {
  const accountRow = getDatabase().prepare('SELECT id, system_account_id, provider_code, name, type, status FROM accounts WHERE id = ?').get(accountId) as unknown as Pick<AccountRow, 'id' | 'system_account_id' | 'provider_code' | 'name' | 'type' | 'status'> | undefined
  if (!accountRow || !canManageResourceOwner(accountRow.system_account_id, access)) {
    return undefined
  }

  const authorizations = listResourceAuthorizations({ resourceType: 'account', resourceId: accountId, status: 'active' }, access)
  const accountNames = systemAccountRowsByIds([accountRow.system_account_id])
  const owner = accountNames.get(accountRow.system_account_id)
  return buildAccountAuthorizationUsageOverview({
    account: {
      id: accountRow.id,
      systemAccountId: accountRow.system_account_id,
      name: accountRow.name,
      providerCode: accountRow.provider_code
    },
    authorizations,
    ownerName: owner?.displayName ?? owner?.username,
    loadUsageByWindow: loadUsageByWindowForScopeRequests
  })
}

export function findAccountForTest(accountId: string, access?: AccessScope): AccountSummary | undefined {
  const visibleAccount = listAccounts(access).find((account) => account.id === accountId)
  if (!visibleAccount?.permissions?.canUse) {
    return undefined
  }
  const row = getDatabase().prepare('SELECT * FROM accounts WHERE id = ?').get(accountId) as unknown as AccountRow | undefined
  if (!row) {
    return undefined
  }
  return {
    ...visibleAccount,
    credentials: decryptJson<Record<string, unknown>>(row.credentials_encrypted),
    proxyProfileId: row.proxy_profile_id ?? undefined,
    passthroughEnabled: row.passthrough_enabled === 1,
    errorPolicyId: row.error_policy_id ?? undefined
  }
}

export function listAccountsDueForCooldownRetest(limit = 20): AccountSummary[] {
  disableExpiredAccounts()
  const rows = getDatabase()
    .prepare(`
      SELECT accounts.*, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
      FROM accounts
      WHERE provider_code = 'openai'
        AND type IN ('api_key', 'oauth')
        AND schedulable = 1
        AND status IN ('rate_limited', 'temporary_unavailable')
        AND cooldown_until IS NOT NULL
        AND cooldown_until <= ?
        AND (account_expires_at IS NULL OR account_expires_at > ?)
      ORDER BY cooldown_until ASC, priority ASC
      LIMIT ?
    `)
    .all(nowIso(), nowIso(), Math.max(1, Math.min(Math.trunc(limit), 200))) as unknown as AccountListRow[]
  const accountNames = systemAccountNameMap()
  return rows.map((row) => ({
    id: row.id,
    systemAccountId: row.system_account_id,
    systemAccountName: accountNames.get(row.system_account_id),
    ownerSystemAccountId: row.system_account_id,
    ownerSystemAccountName: accountNames.get(row.system_account_id),
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
    lastUsedAt: row.last_used_at ?? undefined,
    todayUsage: emptyAccountUsageSummary(),
    usage: emptyAccountUsageSummary(),
    oauthUsage: undefined,
    accessType: 'owner',
    permissions: ownerPermissions()
  }))
}

function listAccountRowsForAccess(access?: AccessScope): AccountListRow[] {
  const ownerSystemAccountId = manageableSystemAccountId(access)
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  if (!ownerSystemAccountId && canAccessAll(access)) {
    return getDatabase()
      .prepare("SELECT accounts.*, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status FROM accounts ORDER BY priority ASC, updated_at DESC")
      .all() as unknown as AccountListRow[]
  }
  if (!viewerSystemAccountId) {
    return getDatabase()
      .prepare("SELECT accounts.*, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status FROM accounts ORDER BY priority ASC, updated_at DESC")
      .all() as unknown as AccountListRow[]
  }
  return getDatabase()
    .prepare(`
      SELECT * FROM (
        SELECT accounts.*, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
        FROM accounts
        WHERE accounts.system_account_id = ?
        UNION ALL
        SELECT accounts.*, 'authorized' AS access_type, ra.id AS authorization_id, ra.status AS authorization_status
        FROM resource_authorizations ra
        INNER JOIN accounts ON accounts.id = ra.resource_id
        WHERE ra.resource_type = 'account'
          AND ra.grantee_system_account_id = ?
          AND ra.status = 'active'
          AND (ra.expires_at IS NULL OR ra.expires_at > ?)
          AND accounts.system_account_id <> ?
      )
      ORDER BY priority ASC, updated_at DESC
    `)
    .all(ownerSystemAccountId ?? viewerSystemAccountId, viewerSystemAccountId, nowIso(), ownerSystemAccountId ?? viewerSystemAccountId) as unknown as AccountListRow[]
}

function loadResourceAuthorizationStatsByResourceIds(resourceType: ResourceAuthorizationResourceType, resourceIds: string[]): Map<string, { authorizationCount: number; authorizationTeamCount: number }> {
  const ids = [...new Set(resourceIds)].filter(Boolean)
  if (!ids.length) return new Map()
  const rows = getDatabase().prepare(`
    SELECT
      ra.resource_id,
      COUNT(DISTINCT ra.id) AS authorization_count,
      COUNT(DISTINCT CASE WHEN ras.source_type = 'team' AND ras.status = 'active' THEN ras.source_team_id END) AS authorization_team_count
    FROM resource_authorizations ra
    LEFT JOIN resource_authorization_sources ras
      ON ras.authorization_id = ra.id
      AND ras.source_type = 'team'
      AND ras.status = 'active'
    WHERE ra.resource_type = ?
      AND ra.status = 'active'
      AND ra.resource_id IN (${sqlPlaceholders(ids.length)})
    GROUP BY ra.resource_id
  `).all(resourceType, ...ids) as unknown as Array<{ resource_id: string; authorization_count: number; authorization_team_count: number }>
  return new Map(rows.map((row) => [row.resource_id, {
    authorizationCount: Number(row.authorization_count ?? 0),
    authorizationTeamCount: Number(row.authorization_team_count ?? 0)
  }]))
}

function accountCredentialsForList(row: AccountListRow): Record<string, unknown> {
  const credentials = decryptJson<Record<string, unknown>>(row.credentials_encrypted)
  if (row.access_type !== 'authorized') {
    return credentials
  }
  return typeof credentials.base_url === 'string' && credentials.base_url ? { base_url: credentials.base_url } : {}
}

function accountNameMap(accountIds: string[]): Map<string, string> {
  const ids = [...new Set(accountIds)].filter(Boolean)
  if (!ids.length) return new Map()
  const rows = getDatabase()
    .prepare(`SELECT id, name FROM accounts WHERE id IN (${sqlPlaceholders(ids.length)})`)
    .all(...ids) as unknown as Array<{ id: string; name: string }>
  return new Map(rows.map((row) => [row.id, row.name]))
}

function loadAccountAuthorizationUsageSummaries(authorizationIds: string[], statDate?: string): Map<string, AccountUsageSummary> {
  return loadAuthorizationUsageSummariesByIds(authorizationIds, 'account_authorization', statDate)
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

function providerPassthroughEnabled(_provider?: ProviderDefinition): boolean {
  return true
}

export function createAccount(input: Record<string, unknown>): AccountSummary {
  const now = nowIso()
  const id = newId('acc')
  const providerCode = String(input.providerCode ?? input.provider_code ?? 'openai')
  const explicitGroupId = typeof input.groupId === 'string' && input.groupId ? input.groupId : typeof input.group_id === 'string' && input.group_id ? input.group_id : undefined
  const explicitGroup = explicitGroupId ? groupOwnerAndProvider(explicitGroupId) : undefined
  const requestedSystemAccountId = currentSystemAccountId()
  const systemAccountId = explicitGroup && canManageResourceOwner(explicitGroup.systemAccountId) ? explicitGroup.systemAccountId : requestedSystemAccountId
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
  const accountExpiresAt = optionalNullableServerDateTimeIso(input.accountExpiresAt ?? input.account_expires_at)
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
    passthroughEnabled: providerPassthroughEnabled(provider),
    errorPolicyId: optionalString(input.errorPolicyId ?? input.error_policy_id),
    schedulable: expiredByPackage ? false : input.schedulable !== false,
    accountExpiresAt: accountExpiresAt ?? undefined,
    cooldownUntil: expiredByPackage ? undefined : initialCooldownUntil,
    lastErrorMessage: expiredByPackage ? '账户套餐已过期，已自动停用' : initialCooldownUntil ? '创建时设置为临时不可调用' : undefined,
    lastUsedAt: undefined,
    todayUsage: emptyAccountUsageSummary(),
    usage: emptyAccountUsageSummary()
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

export function updateAccount(id: string, input: Record<string, unknown>): AccountSummary | undefined {
  const current = listAccounts().find((account) => account.id === id)
  if (!current) {
    return undefined
  }
  const systemAccountId = accountSystemAccountId(id) ?? currentSystemAccountId()
  if (!canManageResourceOwner(systemAccountId)) {
    return undefined
  }
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
    ? optionalNullableServerDateTimeIso(input.accountExpiresAt ?? input.account_expires_at)
    : current.accountExpiresAt ?? null
  const expiredByPackage = isAccountExpired(nextAccountExpiresAt)

  const provider = listProviders().find((item) => item.code === current.providerCode)
  const rawErrorPolicyId = Object.prototype.hasOwnProperty.call(input, 'errorPolicyId')
    ? input.errorPolicyId
    : Object.prototype.hasOwnProperty.call(input, 'error_policy_id')
      ? input.error_policy_id
      : undefined

  const hasStatusInput = Object.prototype.hasOwnProperty.call(input, 'status')
  const requestedStatus = hasStatusInput ? normalizeAccountStatus(input.status, current.status) : current.status
  if (hasStatusInput && requestedStatus === 'active' && isCoolingAccountStatus(current.status)) {
    throw new Error('临时不可调用或限流中的账户不能手动启用，请等待后台复测或先执行实际测试')
  }
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
    passthroughEnabled: providerPassthroughEnabled(provider),
    errorPolicyId: rawErrorPolicyId === undefined ? current.errorPolicyId : optionalString(rawErrorPolicyId),
    schedulable: expiredByPackage ? false : hasStatusInput ? nextStatus === 'active' : typeof input.schedulable === 'boolean' ? input.schedulable : current.schedulable,
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
  const ownerSystemAccountId = accountSystemAccountId(id)
  if (ownerSystemAccountId && !canManageResourceOwner(ownerSystemAccountId)) {
    return undefined
  }

  const expiredByPackage = isAccountExpired(current.accountExpiresAt)
  if (expiredByPackage) {
    getDatabase()
      .prepare(`
        UPDATE accounts
        SET status = 'disabled',
            schedulable = 0,
            cooldown_until = NULL,
            last_error_message = ?,
            stream_failure_count = 0,
            stream_failure_window_started_at = NULL,
            updated_at = ?
        WHERE id = ?
      `)
      .run('账户套餐已过期，已自动停用', nowIso(), id)
    return listAccounts().find((account) => account.id === id)
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

  const expiredByPackage = isAccountExpired(current.accountExpiresAt)
  if (expiredByPackage) {
    getDatabase()
      .prepare(`
        UPDATE accounts
        SET status = 'disabled',
            schedulable = 0,
            cooldown_until = NULL,
            last_error_message = ?,
            stream_failure_count = 0,
            stream_failure_window_started_at = NULL,
            updated_at = ?
        WHERE id = ?
      `)
      .run('账户套餐已过期，已自动停用', nowIso(), id)
    return listAccounts().find((account) => account.id === id)
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
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const rows = listGroupRowsForAccess(access)
  const groupIds = rows.map((row) => row.id)
  const groupStatsByGroup = loadGroupAccountStatsByGroupIds(groupIds)
  const accountIdsByGroup = loadGroupAccountIdsByGroupIds(groupIds)
  const todayUsageByGroup = loadGroupUsageSummariesByGroupIds(rows.map((row) => row.id), todayDateKey())
  const totalUsageByGroup = loadGroupUsageSummariesByGroupIds(rows.map((row) => row.id))
  const sourcesByAuthorization = loadResourceAuthorizationSourcesByAuthorizationIds(rows.map((row) => row.authorization_id ?? ''))
  const accountNames = systemAccountNameMap()
  return rows.map((row) => ({
    id: row.id,
    systemAccountId: includeSystemAccountFields(access) ? row.system_account_id : undefined,
    systemAccountName: includeSystemAccountFields(access) ? accountNames.get(row.system_account_id) : undefined,
    ownerSystemAccountId: row.system_account_id,
    ownerSystemAccountName: accountNames.get(row.system_account_id),
    name: row.name,
    providerCode: row.provider_code,
    description: row.description ?? undefined,
    enabled: row.enabled === 1,
    isDefault: row.is_default === 1,
    accountIds: accountIdsByGroup.get(row.id) ?? [],
    accountStats: groupAccountStatsFromRow(groupStatsByGroup.get(row.id), todayUsageByGroup.get(row.id), totalUsageByGroup.get(row.id)),
    accessType: row.access_type ?? 'owner',
    groupAuthorizationId: row.authorization_id ?? undefined,
    authorizationStatus: row.authorization_status ?? undefined,
    authorizationSources: row.authorization_id ? sourcesByAuthorization.get(row.authorization_id) ?? [] : undefined,
    permissions: row.access_type === 'authorized' && row.system_account_id !== viewerSystemAccountId ? authorizedPermissions() : ownerPermissions()
  }))
}

function listGroupRowsForAccess(access?: AccessScope): GroupListRow[] {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const ownerSystemAccountId = manageableSystemAccountId(access)
  if (!viewerSystemAccountId && canAccessAll(access)) {
    return getDatabase()
      .prepare("SELECT groups.*, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status FROM groups ORDER BY updated_at DESC")
      .all() as unknown as GroupListRow[]
  }
  if (!viewerSystemAccountId) {
    return getDatabase()
      .prepare("SELECT groups.*, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status FROM groups ORDER BY updated_at DESC")
      .all() as unknown as GroupListRow[]
  }
  return getDatabase()
    .prepare(`
      SELECT * FROM (
        SELECT groups.*, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
        FROM groups
        WHERE groups.system_account_id = ?
        UNION ALL
        SELECT groups.*, 'authorized' AS access_type, ra.id AS authorization_id, ra.status AS authorization_status
        FROM resource_authorizations ra
        INNER JOIN groups ON groups.id = ra.resource_id
        WHERE ra.resource_type = 'group'
          AND ra.grantee_system_account_id = ?
          AND ra.status = 'active'
          AND (ra.expires_at IS NULL OR ra.expires_at > ?)
          AND groups.system_account_id <> ?
      )
      ORDER BY updated_at DESC
    `)
    .all(ownerSystemAccountId ?? viewerSystemAccountId, viewerSystemAccountId, nowIso(), ownerSystemAccountId ?? viewerSystemAccountId) as unknown as GroupListRow[]
}

function loadGroupAccountIdsByGroupIds(groupIds: string[]): Map<string, string[]> {
  const ids = [...new Set(groupIds)].filter(Boolean)
  if (!ids.length) return new Map()
  const rows = getDatabase()
    .prepare(`SELECT group_id, account_id FROM group_accounts WHERE enabled = 1 AND group_id IN (${sqlPlaceholders(ids.length)}) ORDER BY group_id ASC, created_at ASC`)
    .all(...ids) as unknown as Array<{ group_id: string; account_id: string }>
  const result = new Map<string, string[]>()
  for (const row of rows) {
    result.set(row.group_id, [...(result.get(row.group_id) ?? []), row.account_id])
  }
  return result
}

function loadGroupAccountStatsByGroupIds(groupIds: string[]): Map<string, GroupAccountStatsRow> {
  const ids = [...new Set(groupIds)].filter(Boolean)
  if (!ids.length) return new Map()
  const rows = getDatabase()
    .prepare(`
      SELECT *
      FROM group_account_stats
      WHERE group_id IN (${sqlPlaceholders(ids.length)})
    `)
    .all(...ids) as unknown as GroupAccountStatsRow[]
  return new Map(rows.map((row) => [row.group_id, row]))
}

function groupNameMap(groupIds: string[]): Map<string, string> {
  const ids = [...new Set(groupIds)].filter(Boolean)
  if (!ids.length) return new Map()
  const rows = getDatabase()
    .prepare(`SELECT id, name FROM groups WHERE id IN (${sqlPlaceholders(ids.length)})`)
    .all(...ids) as unknown as Array<{ id: string; name: string }>
  return new Map(rows.map((row) => [row.id, row.name]))
}

function loadGroupAuthorizationUsageSummaries(authorizationIds: string[], statDate?: string): Map<string, AccountUsageSummary> {
  return loadAuthorizationUsageSummariesByIds(authorizationIds, 'group_authorization', statDate)
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

export function updateGroup(id: string, input: Record<string, unknown>): GroupSummary | undefined {
  const current = listGroups().find((group) => group.id === id)
  if (!current) {
    return undefined
  }
  const systemAccountId = groupOwnerAndProvider(id)?.systemAccountId ?? currentSystemAccountId()
  if (!canManageResourceOwner(systemAccountId)) {
    return undefined
  }
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
  const owner = groupOwnerAndProvider(id)
  if (!owner || !canManageResourceOwner(owner.systemAccountId)) {
    return false
  }
  const result = getDatabase().prepare('DELETE FROM groups WHERE id = ? AND system_account_id = ?').run(id, owner.systemAccountId)
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
  const group = groupOwnerAndProvider(groupId)
  if (!group || !canManageResourceOwner(group.systemAccountId)) {
    return undefined
  }
  if (!canUseAccount(accountId, group.systemAccountId)) {
    return undefined
  }
  if (group.providerCode !== current.providerCode) {
    return undefined
  }

  database.prepare('DELETE FROM group_accounts WHERE account_id = ? AND system_account_id = ?').run(accountId, group.systemAccountId)
  const now = nowIso()
  database
    .prepare('INSERT INTO group_accounts (system_account_id, group_id, account_id, weight, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, 1, ?, ?)')
    .run(group.systemAccountId, groupId, accountId, 1, now, now)

  return listAccounts().find((account) => account.id === accountId)
}

export function addAccountToGroup(groupId: string, accountId: string, weight = 1): GroupSummary | undefined {
  const database = getDatabase()
  const current = groupOwnerAndProvider(groupId)
  if (!current) {
    return undefined
  }
  if (!canManageResourceOwner(current.systemAccountId)) {
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

export function listSystemTeams(access?: AccessScope): SystemTeamSummary[] {
  const scopedId = scopedSystemAccountId(access)
  const rows = scopedId
    ? getDatabase()
      .prepare(`
        SELECT DISTINCT system_teams.*
        FROM system_teams
        INNER JOIN system_team_members ON system_team_members.team_id = system_teams.id
        WHERE system_team_members.system_account_id = ?
        ORDER BY system_teams.status ASC, system_teams.updated_at DESC, system_teams.name ASC
      `)
      .all(scopedId) as unknown as SystemTeamRow[]
    : getDatabase().prepare('SELECT * FROM system_teams ORDER BY status ASC, updated_at DESC, name ASC').all() as unknown as SystemTeamRow[]
  const members = listSystemTeamMembersForTeamIds(rows.map((row) => row.id), true)
  return rows.map((row) => systemTeamSummaryFromRow(row, members.get(row.id) ?? [], access))
}

export function createSystemTeam(input: Record<string, unknown>, access?: AccessScope): SystemTeamSummary {
  const name = optionalString(input.name)
  if (!name) throw new Error('团队名称不能为空')
  const database = getDatabase()
  ensureSystemTeamNameUnique(name, undefined, database)
  const now = nowIso()
  const id = newId('team')
  database
    .prepare('INSERT INTO system_teams (id, name, description, status, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)')
    .run(id, name, optionalString(input.description) ?? null, input.status === 'disabled' ? 'disabled' : 'active', currentSystemAccountId(access), now, now)
  const created = listSystemTeams(access).find((team) => team.id === id)
  if (!created) throw new Error('Create system team failed')
  return created
}

export function updateSystemTeam(id: string, input: Record<string, unknown>, access?: AccessScope): SystemTeamSummary | undefined {
  const database = getDatabase()
  const row = database.prepare('SELECT * FROM system_teams WHERE id = ?').get(id) as unknown as SystemTeamRow | undefined
  if (!row) return undefined
  const name = optionalString(input.name) ?? row.name
  ensureSystemTeamNameUnique(name, id, database)
  const status = input.status === 'disabled' ? 'disabled' : input.status === 'active' ? 'active' : row.status
  const now = nowIso()
  database.exec('BEGIN')
  try {
    database
      .prepare('UPDATE system_teams SET name = ?, description = ?, status = ?, updated_at = ? WHERE id = ?')
      .run(name, input.description === undefined ? row.description : optionalNullableString(input.description), status, now, id)
    if (row.status !== 'disabled' && status === 'disabled') {
      revokeAllTeamSources(id, currentSystemAccountId(access), database, now, 'team_disabled')
    }
    if (row.status === 'disabled' && status === 'active') {
      reactivateTeamGrantSources(id, access, database, now)
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
  return listSystemTeams(access).find((team) => team.id === id)
}

export function addSystemTeamMembers(teamId: string, input: Record<string, unknown>, access?: AccessScope): SystemTeamSummary | undefined {
  const team = getDatabase().prepare("SELECT * FROM system_teams WHERE id = ? AND status = 'active'").get(teamId) as unknown as SystemTeamRow | undefined
  if (!team) return undefined
  const systemAccountIds = normalizeSystemAccountIds(input.systemAccountIds ?? input.systemAccountId ?? input.memberIds)
  if (!systemAccountIds.length) throw new Error('请选择团队成员')
  const database = getDatabase()
  const now = nowIso()
  database.exec('BEGIN')
  try {
    for (const systemAccountId of systemAccountIds) {
      const account = findSystemAccountById(systemAccountId)
      if (!account || account.status !== 'active') throw new Error('团队成员不存在或已停用')
      const existing = database.prepare('SELECT * FROM system_team_members WHERE team_id = ? AND system_account_id = ? ORDER BY created_at DESC LIMIT 1').get(teamId, systemAccountId) as unknown as SystemTeamMemberRow | undefined
      if (existing?.status === 'active') continue
      if (existing) {
        database.prepare("UPDATE system_team_members SET status = 'active', joined_at = ?, removed_at = NULL, updated_at = ? WHERE id = ?").run(now, now, existing.id)
      } else {
        database.prepare("INSERT INTO system_team_members (id, team_id, system_account_id, member_role, status, joined_at, removed_at, created_by, created_at, updated_at) VALUES (?, ?, ?, 'member', 'active', ?, NULL, ?, ?, ?)")
          .run(newId('teammem'), teamId, systemAccountId, now, currentSystemAccountId(access), now, now)
      }
      applyActiveTeamGrantsToMember(teamId, systemAccountId, access, database, now)
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
  return listSystemTeams(access).find((item) => item.id === teamId)
}

export function removeSystemTeamMember(teamId: string, memberId: string, access?: AccessScope): SystemTeamSummary | undefined {
  const database = getDatabase()
  const member = database.prepare("SELECT * FROM system_team_members WHERE id = ? AND team_id = ? AND status = 'active'").get(memberId, teamId) as unknown as SystemTeamMemberRow | undefined
  if (!member) return undefined
  const now = nowIso()
  database.exec('BEGIN')
  try {
    database.prepare("UPDATE system_team_members SET status = 'removed', removed_at = ?, updated_at = ? WHERE id = ?").run(now, now, memberId)
    revokeTeamSourcesForMember(teamId, member.system_account_id, currentSystemAccountId(access), database, now)
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
  return listSystemTeams(access).find((item) => item.id === teamId)
}

export function listResourceAuthorizations(filters: Record<string, unknown> = {}, access?: AccessScope): ResourceAuthorizationSummary[] {
  expireDueResourceAuthorizations()
  const clauses: string[] = []
  const params: Array<string | number | null> = []
  const resourceType = normalizeResourceType(filters.resourceType ?? filters.resource_type)
  if (resourceType) { clauses.push('ra.resource_type = ?'); params.push(resourceType) }
  const resourceId = optionalString(filters.resourceId ?? filters.resource_id)
  if (resourceId) { clauses.push('ra.resource_id = ?'); params.push(resourceId) }
  const granteeSystemAccountId = optionalString(filters.granteeSystemAccountId ?? filters.grantee_system_account_id)
  if (granteeSystemAccountId) { clauses.push('ra.grantee_system_account_id = ?'); params.push(granteeSystemAccountId) }
  const status = filters.status === 'active'
    ? 'active'
    : filters.status === 'paused'
      ? 'paused'
      : filters.status === 'expired'
        ? 'expired'
        : filters.status === 'revoked'
          ? 'revoked'
          : undefined
  if (status) { clauses.push('ra.status = ?'); params.push(status) }
  const teamId = optionalString(filters.teamId ?? filters.team_id)
  if (teamId) {
    clauses.push("EXISTS (SELECT 1 FROM resource_authorization_sources ras WHERE ras.authorization_id = ra.id AND ras.source_type = 'team' AND ras.source_team_id = ? AND ras.status = 'active')")
    params.push(teamId)
  }
  const ownerSystemAccountId = scopedSystemAccountId(access)
  if (ownerSystemAccountId) { clauses.push('ra.resource_owner_system_account_id = ?'); params.push(ownerSystemAccountId) }
  else if (!canAccessAll(access)) { clauses.push('ra.resource_owner_system_account_id = ?'); params.push(currentSystemAccountId(access)) }
  const where = clauses.length ? `WHERE ${clauses.join(' AND ')}` : ''
  const rows = getDatabase().prepare(`SELECT ra.* FROM resource_authorizations ra ${where} ORDER BY CASE ra.status WHEN 'active' THEN 0 WHEN 'paused' THEN 1 WHEN 'expired' THEN 2 WHEN 'revoked' THEN 3 ELSE 4 END, ra.updated_at DESC, ra.created_at DESC`).all(...params) as unknown as ResourceAuthorizationRow[]
  return resourceAuthorizationSummaries(rows)
}

export function createResourceAuthorization(input: Record<string, unknown>, access?: AccessScope): ResourceAuthorizationSummary {
  const resourceType = normalizeResourceType(input.resourceType ?? input.resource_type)
  const resourceId = optionalString(input.resourceId ?? input.resource_id)
  if (!resourceType || !resourceId) throw new Error('请选择授权资源')
  const ownerSystemAccountId = resourceOwnerSystemAccountId(resourceType, resourceId)
  if (!ownerSystemAccountId || !canManageResourceOwner(ownerSystemAccountId, access)) throw new Error('授权资源不存在')
  const granteeType = input.granteeType === 'team' || input.grantee_type === 'team' ? 'team' : 'system_account'
  const granteeId = optionalString(input.granteeId ?? input.grantee_id ?? input.granteeSystemAccountId ?? input.grantee_system_account_id ?? input.teamId ?? input.team_id)
  if (!granteeId) throw new Error('请选择被授权对象')
  const database = getDatabase()
  const now = nowIso()
  const actor = currentSystemAccountId(access)
  const createdIds: string[] = []
  database.exec('BEGIN')
  try {
    if (granteeType === 'team') {
      const team = database.prepare("SELECT * FROM system_teams WHERE id = ? AND status = 'active'").get(granteeId) as unknown as SystemTeamRow | undefined
      if (!team) throw new Error('团队不存在或已停用')
      const members = activeTeamMemberRows(granteeId, database).filter((member) => member.system_account_id !== ownerSystemAccountId)
      if (!members.length) throw new Error('团队暂无可授权成员，请先添加非归属人成员后再授权')
      upsertTeamResourceGrant({ resourceType, resourceId, ownerSystemAccountId, teamId: granteeId, remark: optionalString(input.remark), expiresAt: optionalNullableServerDateTimeIso(input.expiresAt ?? input.expires_at), limits: input.limits, modelPolicy: input.modelPolicy ?? input.model_policy, actor, now, database })
      for (const member of members) {
        const authorization = upsertResourceAuthorizationForUser({ resourceType, resourceId, ownerSystemAccountId, granteeSystemAccountId: member.system_account_id, sourceType: 'team', sourceTeamId: granteeId, remark: optionalString(input.remark), expiresAt: optionalNullableServerDateTimeIso(input.expiresAt ?? input.expires_at), limits: input.limits, modelPolicy: input.modelPolicy ?? input.model_policy, actor, now, database })
        createdIds.push(authorization.id)
      }
    } else {
      const grantee = findSystemAccountById(granteeId)
      if (!grantee || grantee.status !== 'active') throw new Error('被授权用户不存在或已停用')
      if (granteeId === ownerSystemAccountId) throw new Error('不能授权给资源所有者自己')
      const authorization = upsertResourceAuthorizationForUser({ resourceType, resourceId, ownerSystemAccountId, granteeSystemAccountId: granteeId, sourceType: 'manual', remark: optionalString(input.remark), expiresAt: optionalNullableServerDateTimeIso(input.expiresAt ?? input.expires_at), limits: input.limits, modelPolicy: input.modelPolicy ?? input.model_policy, actor, now, database })
      createdIds.push(authorization.id)
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
  const ids = [...new Set(createdIds)]
  const created = ids.length ? listResourceAuthorizations({ status: 'all' }, access).find((item) => item.id === ids[0]) : undefined
  if (created) return created
  const fallback = listResourceAuthorizations({ resourceType, resourceId, teamId: granteeType === 'team' ? granteeId : undefined, status: 'all' }, access)[0]
  if (!fallback) throw new Error('Create resource authorization failed')
  return fallback
}

export function revokeResourceAuthorization(authorizationId: string, input: Record<string, unknown> = {}, access?: AccessScope): ResourceAuthorizationSummary | undefined {
  const database = getDatabase()
  const row = database.prepare('SELECT * FROM resource_authorizations WHERE id = ?').get(authorizationId) as unknown as ResourceAuthorizationRow | undefined
  if (!row || !canManageResourceOwner(row.resource_owner_system_account_id, access)) return undefined
  const now = nowIso()
  const actor = currentSystemAccountId(access)
  const revokeAll = input.revokeAll === true || input.revoke_all === true
  const sourceType = normalizeSourceType(input.sourceType ?? input.source_type)
  const sourceTeamId = optionalString(input.sourceTeamId ?? input.source_team_id ?? input.teamId ?? input.team_id)
  database.exec('BEGIN')
  try {
    if (revokeAll || !sourceType) {
      database.prepare("UPDATE resource_authorization_sources SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = COALESCE(ended_reason, 'authorization_revoked'), revoked_by = ?, revoked_at = ?, updated_at = ? WHERE authorization_id = ? AND status IN ('active', 'superseded')").run(now, actor, now, now, authorizationId)
      database.prepare("UPDATE resource_authorizations SET status = 'revoked', effective_source_type = NULL, effective_source_team_id = NULL, revoked_by = ?, revoked_at = ?, revoked_reason = 'authorization_revoked', last_source_changed_at = ?, updated_at = ? WHERE id = ?").run(actor, now, now, now, authorizationId)
    } else {
      const params: Array<string | number | null> = [actor, now, now, authorizationId, sourceType]
      let sql = "UPDATE resource_authorization_sources SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = COALESCE(ended_reason, 'source_revoked'), revoked_by = ?, revoked_at = ?, updated_at = ? WHERE authorization_id = ? AND source_type = ? AND status = 'active'"
      params.unshift(now)
      if (sourceType === 'team') { sql += ' AND source_team_id = ?'; params.push(sourceTeamId ?? '') }
      database.prepare(sql).run(...params)
      if (sourceType === 'team' && sourceTeamId) {
        database
          .prepare("UPDATE team_resource_authorization_grants SET status = 'revoked', revoked_by = ?, revoked_at = ?, updated_at = ? WHERE resource_type = ? AND resource_id = ? AND team_id = ? AND status = 'active'")
          .run(actor, now, now, row.resource_type, row.resource_id, sourceTeamId)
        revokeTeamGrantSources(row.resource_type, row.resource_id, sourceTeamId, actor, database, now)
      }
      refreshResourceAuthorizationEffectiveSource(authorizationId, actor, now, database)
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
  return listResourceAuthorizations({ status: 'all' }, access).find((item) => item.id === authorizationId)
}

export function updateResourceAuthorization(authorizationId: string, input: Record<string, unknown> = {}, access?: AccessScope): ResourceAuthorizationSummary | undefined {
  expireDueResourceAuthorizations()
  const database = getDatabase()
  const row = database.prepare('SELECT * FROM resource_authorizations WHERE id = ?').get(authorizationId) as unknown as ResourceAuthorizationRow | undefined
  if (!row || !canManageResourceOwner(row.resource_owner_system_account_id, access)) return undefined
  const now = nowIso()
  const hasExpiresAtInput = Object.prototype.hasOwnProperty.call(input, 'expiresAt')
    || Object.prototype.hasOwnProperty.call(input, 'expires_at')
  const nextExpiresAt = hasExpiresAtInput
    ? optionalNullableServerDateTimeIso(input.expiresAt ?? input.expires_at)
    : row.expires_at
  const rawStatus = optionalString(input.status)
  const requestedStatus = rawStatus === 'active' || rawStatus === 'paused' || rawStatus === 'expired' || rawStatus === 'revoked'
    ? rawStatus
    : undefined
  if (row.status === 'revoked' && requestedStatus === 'active') {
    throw new Error('已回收授权不能直接恢复，请重新新增授权')
  }
  const expiredByTime = isResourceAuthorizationExpired(nextExpiresAt)
  const nextStatus: AuthorizationStatus = expiredByTime
    ? 'expired'
    : requestedStatus === 'active' || requestedStatus === 'paused'
      ? requestedStatus
      : row.status === 'expired' && hasExpiresAtInput
        ? 'active'
        : row.status === 'paused'
          ? 'paused'
        : row.status
  const nextRevokedReason = nextStatus === 'expired'
    ? 'authorization_expired'
    : nextStatus === 'paused'
      ? 'authorization_paused'
      : nextStatus === 'revoked'
        ? row.revoked_reason ?? 'authorization_revoked'
        : null
  const nextRevokedAt = nextStatus === 'active' || nextStatus === 'paused' ? null : row.revoked_at ?? now
  const nextRevokedBy = nextStatus === 'active' || nextStatus === 'paused' ? null : row.revoked_by ?? currentSystemAccountId(access)

  database
    .prepare(`
      UPDATE resource_authorizations
      SET status = ?,
          expires_at = ?,
          revoked_by = ?,
          revoked_at = ?,
          revoked_reason = ?,
          updated_at = ?
      WHERE id = ?
    `)
    .run(nextStatus, nextExpiresAt, nextRevokedBy, nextRevokedAt, nextRevokedReason, now, authorizationId)
  return listResourceAuthorizations({ status: 'all' }, access).find((item) => item.id === authorizationId)
}

export function getResourceAuthorizationUsage(authorizationId: string, access?: AccessScope): ResourceAuthorizationSummary | undefined {
  const authorization = listResourceAuthorizations({ status: 'all' }, access).find((item) => item.id === authorizationId)
  if (!authorization) return undefined
  return {
    ...authorization,
    usageBySystemAccount: loadResourceAuthorizationUsageDetails(authorization)
  }
}

function systemTeamSummaryFromRow(row: SystemTeamRow, members: SystemTeamMemberSummary[], _access?: AccessScope): SystemTeamSummary {
  return { id: row.id, name: row.name, description: row.description ?? undefined, status: row.status, memberCount: members.length, activeMemberCount: members.filter((member) => member.status === 'active').length, members, createdBy: row.created_by, createdAt: row.created_at, updatedAt: row.updated_at }
}

function listSystemTeamMembersForTeamIds(teamIds: string[], activeOnly = false): Map<string, SystemTeamMemberSummary[]> {
  const ids = [...new Set(teamIds)].filter(Boolean)
  if (!ids.length) return new Map()
  const statusClause = activeOnly ? " AND system_team_members.status = 'active'" : ''
  const rows = getDatabase().prepare(`SELECT system_team_members.*, system_accounts.display_name, system_accounts.username FROM system_team_members INNER JOIN system_accounts ON system_accounts.id = system_team_members.system_account_id WHERE system_team_members.team_id IN (${sqlPlaceholders(ids.length)})${statusClause} ORDER BY system_team_members.status ASC, system_team_members.joined_at ASC`).all(...ids) as unknown as Array<SystemTeamMemberRow & { display_name?: string; username?: string }>
  const result = new Map<string, SystemTeamMemberSummary[]>()
  for (const row of rows) {
    const member: SystemTeamMemberSummary = { id: row.id, teamId: row.team_id, systemAccountId: row.system_account_id, systemAccountName: row.display_name, username: row.username, memberRole: 'member', status: row.status, joinedAt: row.joined_at, removedAt: row.removed_at ?? undefined, createdAt: row.created_at, updatedAt: row.updated_at }
    result.set(row.team_id, [...(result.get(row.team_id) ?? []), member])
  }
  return result
}

function ensureSystemTeamNameUnique(name: string, excludeId?: string, database = getDatabase()): void {
  const row = database
    .prepare('SELECT id FROM system_teams WHERE lower(name) = lower(?) AND id <> ? LIMIT 1')
    .get(name, excludeId ?? '') as unknown as { id?: string } | undefined
  if (row?.id) throw new Error('团队名称已存在')
}

function normalizeSystemAccountIds(value: unknown): string[] {
  if (Array.isArray(value)) return [...new Set(value.map((item) => typeof item === 'string' ? item.trim() : '').filter(Boolean))]
  return typeof value === 'string' && value.trim() ? [value.trim()] : []
}

function normalizeResourceType(value: unknown): ResourceAuthorizationResourceType | undefined {
  return value === 'account' || value === 'group' ? value : undefined
}

function normalizeSourceType(value: unknown): ResourceAuthorizationSourceType | undefined {
  return value === 'manual' || value === 'team' ? value : undefined
}

function resourceOwnerSystemAccountId(resourceType: ResourceAuthorizationResourceType, resourceId: string): string | undefined {
  return resourceType === 'account' ? accountSystemAccountId(resourceId) : groupOwnerAndProvider(resourceId)?.systemAccountId
}

function activeTeamMemberRows(teamId: string, database = getDatabase()): SystemTeamMemberRow[] {
  return database.prepare("SELECT * FROM system_team_members WHERE team_id = ? AND status = 'active' ORDER BY joined_at ASC").all(teamId) as unknown as SystemTeamMemberRow[]
}

function upsertResourceAuthorizationForUser(input: { resourceType: ResourceAuthorizationResourceType; resourceId: string; ownerSystemAccountId: string; granteeSystemAccountId: string; sourceType: ResourceAuthorizationSourceType; sourceTeamId?: string; remark?: string; expiresAt?: string | null; limits?: unknown; modelPolicy?: unknown; actor: string; now: string; database: DatabaseSync }): ResourceAuthorizationRow {
  if (input.granteeSystemAccountId === input.ownerSystemAccountId) throw new Error('不能授权给资源所有者自己')
  const existing = input.database.prepare('SELECT * FROM resource_authorizations WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ? LIMIT 1').get(input.resourceType, input.resourceId, input.granteeSystemAccountId) as unknown as ResourceAuthorizationRow | undefined
  const authorizationId = existing?.id ?? newId('rauth')
  const isTeamSource = input.sourceType === 'team'
  const hasActiveTeamSource = existing ? hasActiveTeamAuthorizationSource(input.database, authorizationId) : false
  const nextEffectiveSourceType = isTeamSource || hasActiveTeamSource ? 'team' : 'manual'
  const nextEffectiveSourceTeamId = isTeamSource ? input.sourceTeamId ?? null : firstActiveTeamSourceId(input.database, authorizationId)
  const nextExpiresAt = input.expiresAt ?? existing?.expires_at ?? null
  const existingStatus = existing?.status
  const nextStatus: AuthorizationStatus = isResourceAuthorizationExpired(nextExpiresAt)
    ? 'expired'
    : existingStatus === 'paused'
      ? 'paused'
      : 'active'
  if (existing) {
    input.database.prepare(`
      UPDATE resource_authorizations
      SET resource_owner_system_account_id = ?,
          status = ?,
          effective_source_type = COALESCE(?, effective_source_type),
          effective_source_team_id = ?,
          activated_at = COALESCE(activated_at, ?),
          last_source_changed_at = ?,
          remark = COALESCE(?, remark),
          expires_at = COALESCE(?, expires_at),
          limits_json = COALESCE(?, limits_json),
          model_policy_json = COALESCE(?, model_policy_json),
          revoked_by = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_by, ?) ELSE NULL END,
          revoked_at = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_at, ?) ELSE NULL END,
          revoked_reason = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'authorization_expired' ELSE NULL END,
          updated_at = ?
      WHERE id = ?
    `).run(input.ownerSystemAccountId, nextStatus, nextEffectiveSourceType, nextEffectiveSourceTeamId, input.now, input.now, input.remark ?? null, input.expiresAt ?? null, jsonObjectOrNull(input.limits), jsonObjectOrNull(input.modelPolicy), input.now, authorizationId)
  } else {
    input.database.prepare(`
      INSERT INTO resource_authorizations (
        id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status,
        effective_source_type, effective_source_team_id, activated_at, last_source_changed_at,
        remark, expires_at, limits_json, model_policy_json,
        created_by, created_at, revoked_by, revoked_at, revoked_reason, updated_at
      ) VALUES (?, ?, ?, ?, ?, 'use', 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?)
    `).run(authorizationId, input.resourceType, input.resourceId, input.ownerSystemAccountId, input.granteeSystemAccountId, nextStatus, nextEffectiveSourceType, nextEffectiveSourceTeamId, input.now, input.now, input.remark ?? null, nextExpiresAt, jsonObjectOrNull(input.limits), jsonObjectOrNull(input.modelPolicy), input.actor, input.now, input.now)
  }
  upsertResourceAuthorizationSource(input.database, authorizationId, input.sourceType, input.sourceTeamId, input.actor, input.now, isTeamSource ? 'active' : hasActiveTeamSource ? 'superseded' : 'active')
  if (isTeamSource) {
    input.database.prepare(`
      UPDATE resource_authorization_sources
      SET status = 'superseded',
          ended_at = COALESCE(ended_at, ?),
          ended_reason = COALESCE(ended_reason, 'covered_by_team'),
          updated_at = ?
      WHERE authorization_id = ? AND source_type = 'manual' AND status = 'active'
    `).run(input.now, input.now, authorizationId)
  }
  refreshResourceAuthorizationEffectiveSource(authorizationId, input.actor, input.now, input.database)
  const row = input.database.prepare('SELECT * FROM resource_authorizations WHERE id = ?').get(authorizationId) as unknown as ResourceAuthorizationRow | undefined
  if (!row) throw new Error('Create resource authorization failed')
  return row
}

function hasActiveTeamAuthorizationSource(database: DatabaseSync, authorizationId: string): boolean {
  const row = database
    .prepare("SELECT id FROM resource_authorization_sources WHERE authorization_id = ? AND source_type = 'team' AND status = 'active' LIMIT 1")
    .get(authorizationId) as unknown as { id?: string } | undefined
  return Boolean(row?.id)
}

function hasAnyActiveAuthorizationSource(database: DatabaseSync, authorizationId: string): boolean {
  const row = database
    .prepare("SELECT id FROM resource_authorization_sources WHERE authorization_id = ? AND status = 'active' LIMIT 1")
    .get(authorizationId) as unknown as { id?: string } | undefined
  return Boolean(row?.id)
}

function firstActiveTeamSourceId(database: DatabaseSync, authorizationId: string): string | null {
  const row = database
    .prepare("SELECT source_team_id FROM resource_authorization_sources WHERE authorization_id = ? AND source_type = 'team' AND status = 'active' ORDER BY activated_at ASC, created_at ASC LIMIT 1")
    .get(authorizationId) as unknown as { source_team_id?: string | null } | undefined
  return row?.source_team_id ?? null
}

function upsertResourceAuthorizationSource(database: DatabaseSync, authorizationId: string, sourceType: ResourceAuthorizationSourceType, sourceTeamId: string | undefined, actor: string, now: string, requestedStatus: ResourceAuthorizationSourceStatus): void {
  const existing = database.prepare("SELECT * FROM resource_authorization_sources WHERE authorization_id = ? AND source_type = ? AND COALESCE(source_team_id, '') = COALESCE(?, '') ORDER BY created_at DESC LIMIT 1").get(authorizationId, sourceType, sourceTeamId ?? null) as unknown as ResourceAuthorizationSourceRow | undefined
  if (existing) {
    database.prepare(`
      UPDATE resource_authorization_sources
      SET status = ?,
          activated_at = COALESCE(activated_at, ?),
          ended_at = CASE WHEN ? = 'active' THEN NULL ELSE COALESCE(ended_at, ?) END,
          ended_reason = CASE WHEN ? = 'active' THEN NULL ELSE COALESCE(ended_reason, ?) END,
          revoked_by = CASE WHEN ? = 'active' THEN NULL ELSE revoked_by END,
          revoked_at = CASE WHEN ? = 'active' THEN NULL ELSE revoked_at END,
          updated_at = ?
      WHERE id = ?
    `).run(requestedStatus, now, requestedStatus, now, requestedStatus, requestedStatus === 'superseded' ? 'covered_by_team' : null, requestedStatus, requestedStatus, now, existing.id)
    return
  }
  database.prepare(`
    INSERT INTO resource_authorization_sources (
      id, authorization_id, source_type, source_team_id, status, activated_at, ended_at, ended_reason,
      created_by, created_at, revoked_by, revoked_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?)
  `).run(newId('rauthsrc'), authorizationId, sourceType, sourceTeamId ?? null, requestedStatus, now, requestedStatus === 'active' ? null : now, requestedStatus === 'superseded' ? 'covered_by_team' : null, actor, now, now)
}

function refreshResourceAuthorizationEffectiveSource(authorizationId: string, actor: string, now: string, database = getDatabase()): void {
  if (!hasAnyActiveAuthorizationSource(database, authorizationId)) {
    database.prepare(`
      UPDATE resource_authorizations
      SET status = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'expired' ELSE 'revoked' END,
          effective_source_type = NULL,
          effective_source_team_id = NULL,
          revoked_by = COALESCE(revoked_by, ?),
          revoked_at = COALESCE(revoked_at, ?),
          revoked_reason = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'authorization_expired' ELSE 'authorization_revoked' END,
          last_source_changed_at = ?,
          updated_at = ?
      WHERE id = ?
    `).run(now, actor, now, now, now, now, authorizationId)
    return
  }
  const activeTeamSource = database.prepare(`
    SELECT source_team_id
    FROM resource_authorization_sources
    WHERE authorization_id = ? AND source_type = 'team' AND status = 'active'
    ORDER BY activated_at ASC, created_at ASC
    LIMIT 1
  `).get(authorizationId) as unknown as { source_team_id?: string | null } | undefined

  if (activeTeamSource?.source_team_id) {
    database.prepare(`
      UPDATE resource_authorizations
      SET status = CASE
            WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'expired'
            WHEN status = 'paused' THEN 'paused'
            ELSE 'active'
          END,
          effective_source_type = 'team',
          effective_source_team_id = ?,
          revoked_by = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_by, ?) ELSE NULL END,
          revoked_at = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_at, ?) ELSE NULL END,
          revoked_reason = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'authorization_expired' ELSE NULL END,
          last_source_changed_at = ?,
          updated_at = ?
      WHERE id = ?
    `).run(now, activeTeamSource.source_team_id, now, actor, now, now, now, now, now, authorizationId)
    return
  }

  const activeManualSource = database.prepare(`
    SELECT id
    FROM resource_authorization_sources
    WHERE authorization_id = ? AND source_type = 'manual' AND status = 'active'
    ORDER BY activated_at ASC, created_at ASC
    LIMIT 1
  `).get(authorizationId) as unknown as { id?: string } | undefined

  if (activeManualSource?.id) {
    database.prepare(`
      UPDATE resource_authorizations
      SET status = CASE
            WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'expired'
            WHEN status = 'paused' THEN 'paused'
            ELSE 'active'
          END,
          effective_source_type = 'manual',
          effective_source_team_id = NULL,
          revoked_by = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_by, ?) ELSE NULL END,
          revoked_at = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_at, ?) ELSE NULL END,
          revoked_reason = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'authorization_expired' ELSE NULL END,
          last_source_changed_at = ?,
          updated_at = ?
      WHERE id = ?
    `).run(now, now, actor, now, now, now, now, now, authorizationId)
    return
  }

  database.prepare(`
    UPDATE resource_authorizations
    SET status = 'revoked',
        effective_source_type = NULL,
        effective_source_team_id = NULL,
        revoked_by = COALESCE(revoked_by, ?),
        revoked_at = COALESCE(revoked_at, ?),
        revoked_reason = COALESCE(revoked_reason, 'no_active_source'),
        last_source_changed_at = ?,
        updated_at = ?
    WHERE id = ?
  `).run(actor, now, now, now, authorizationId)
}

function upsertTeamResourceGrant(input: { resourceType: ResourceAuthorizationResourceType; resourceId: string; ownerSystemAccountId: string; teamId: string; remark?: string; expiresAt?: string | null; limits?: unknown; modelPolicy?: unknown; actor: string; now: string; database: DatabaseSync }): TeamResourceAuthorizationGrantRow {
  const existing = input.database.prepare("SELECT * FROM team_resource_authorization_grants WHERE resource_type = ? AND resource_id = ? AND team_id = ? AND status = 'active' LIMIT 1").get(input.resourceType, input.resourceId, input.teamId) as unknown as TeamResourceAuthorizationGrantRow | undefined
  const id = existing?.id ?? newId('teamgrant')
  if (existing) {
    input.database.prepare('UPDATE team_resource_authorization_grants SET remark = COALESCE(?, remark), expires_at = COALESCE(?, expires_at), limits_json = COALESCE(?, limits_json), model_policy_json = COALESCE(?, model_policy_json), updated_at = ? WHERE id = ?')
      .run(input.remark ?? null, input.expiresAt ?? null, jsonObjectOrNull(input.limits), jsonObjectOrNull(input.modelPolicy), input.now, id)
  } else {
    input.database.prepare("INSERT INTO team_resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id, team_id, scope, status, remark, expires_at, limits_json, model_policy_json, created_by, created_at, revoked_by, revoked_at, updated_at) VALUES (?, ?, ?, ?, ?, 'use', 'active', ?, ?, ?, ?, ?, ?, NULL, NULL, ?)")
      .run(id, input.resourceType, input.resourceId, input.ownerSystemAccountId, input.teamId, input.remark ?? null, input.expiresAt ?? null, jsonObjectOrNull(input.limits), jsonObjectOrNull(input.modelPolicy), input.actor, input.now, input.now)
  }
  const row = input.database.prepare('SELECT * FROM team_resource_authorization_grants WHERE id = ?').get(id) as unknown as TeamResourceAuthorizationGrantRow | undefined
  if (!row) throw new Error('Create team authorization grant failed')
  return row
}

function applyActiveTeamGrantsToMember(teamId: string, systemAccountId: string, access: AccessScope | undefined, database: DatabaseSync, now: string): void {
  const grants = database.prepare("SELECT * FROM team_resource_authorization_grants WHERE team_id = ? AND status = 'active'").all(teamId) as unknown as TeamResourceAuthorizationGrantRow[]
  const actor = currentSystemAccountId(access)
  for (const grant of grants) {
    if (grant.resource_owner_system_account_id === systemAccountId) continue
    upsertResourceAuthorizationForUser({ resourceType: grant.resource_type, resourceId: grant.resource_id, ownerSystemAccountId: grant.resource_owner_system_account_id, granteeSystemAccountId: systemAccountId, sourceType: 'team', sourceTeamId: teamId, remark: grant.remark ?? undefined, expiresAt: grant.expires_at, limits: parseOptionalJsonObject(grant.limits_json ?? undefined), modelPolicy: parseOptionalJsonObject(grant.model_policy_json ?? undefined), actor, now, database })
  }
}

function revokeTeamSourcesForMember(teamId: string, systemAccountId: string, actor: string, database: DatabaseSync, now: string): void {
  const rows = database.prepare("SELECT ras.authorization_id FROM resource_authorization_sources ras INNER JOIN resource_authorizations ra ON ra.id = ras.authorization_id WHERE ras.source_type = 'team' AND ras.source_team_id = ? AND ras.status = 'active' AND ra.grantee_system_account_id = ?").all(teamId, systemAccountId) as unknown as Array<{ authorization_id: string }>
  for (const row of rows) {
    database.prepare("UPDATE resource_authorization_sources SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = COALESCE(ended_reason, 'member_removed'), revoked_by = ?, revoked_at = ?, updated_at = ? WHERE authorization_id = ? AND source_type = 'team' AND source_team_id = ? AND status = 'active'").run(now, actor, now, now, row.authorization_id, teamId)
    refreshResourceAuthorizationEffectiveSource(row.authorization_id, actor, now, database)
  }
}

function revokeTeamGrantSources(resourceType: ResourceAuthorizationResourceType, resourceId: string, teamId: string, actor: string, database: DatabaseSync, now: string): void {
  const rows = database.prepare("SELECT ras.authorization_id FROM resource_authorization_sources ras INNER JOIN resource_authorizations ra ON ra.id = ras.authorization_id WHERE ras.source_type = 'team' AND ras.source_team_id = ? AND ras.status = 'active' AND ra.resource_type = ? AND ra.resource_id = ?").all(teamId, resourceType, resourceId) as unknown as Array<{ authorization_id: string }>
  for (const row of rows) {
    database.prepare("UPDATE resource_authorization_sources SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = COALESCE(ended_reason, 'team_revoked'), revoked_by = ?, revoked_at = ?, updated_at = ? WHERE authorization_id = ? AND source_type = 'team' AND source_team_id = ? AND status = 'active'").run(now, actor, now, now, row.authorization_id, teamId)
    refreshResourceAuthorizationEffectiveSource(row.authorization_id, actor, now, database)
  }
}

function revokeAllTeamSources(teamId: string, actor: string, database: DatabaseSync, now: string, reason: string): void {
  const rows = database.prepare("SELECT DISTINCT authorization_id FROM resource_authorization_sources WHERE source_type = 'team' AND source_team_id = ? AND status = 'active'").all(teamId) as unknown as Array<{ authorization_id: string }>
  for (const row of rows) {
    database.prepare(`
      UPDATE resource_authorization_sources
      SET status = 'revoked',
          ended_at = COALESCE(ended_at, ?),
          ended_reason = COALESCE(ended_reason, ?),
          revoked_by = ?,
          revoked_at = ?,
          updated_at = ?
      WHERE authorization_id = ? AND source_type = 'team' AND source_team_id = ? AND status = 'active'
    `).run(now, reason, actor, now, now, row.authorization_id, teamId)
    refreshResourceAuthorizationEffectiveSource(row.authorization_id, actor, now, database)
  }
}

function reactivateTeamGrantSources(teamId: string, access: AccessScope | undefined, database: DatabaseSync, now: string): void {
  const memberRows = activeTeamMemberRows(teamId, database)
  for (const member of memberRows) {
    applyActiveTeamGrantsToMember(teamId, member.system_account_id, access, database, now)
  }
}

function deactivateAuthorizationIfNoActiveSources(authorizationId: string, actor: string, now: string, database = getDatabase()): void {
  refreshResourceAuthorizationEffectiveSource(authorizationId, actor, now, database)
}

function resourceAuthorizationSummaries(rows: ResourceAuthorizationRow[]): ResourceAuthorizationSummary[] {
  const accountNames = accountNameMap(rows.filter((row) => row.resource_type === 'account').map((row) => row.resource_id))
  const groupNames = groupNameMap(rows.filter((row) => row.resource_type === 'group').map((row) => row.resource_id))
  const systemAccounts = systemAccountRowsByIds(rows.flatMap((row) => [row.resource_owner_system_account_id, row.grantee_system_account_id]))
  const teamNames = systemTeamNameMap(rows.map((row) => row.effective_source_team_id ?? ''))
  const sources = loadResourceAuthorizationSourcesByAuthorizationIds(rows.map((row) => row.id))
  const usage = loadResourceAuthorizationUsageSummaries(rows.map((row) => row.id), todayDateKey())
  return rows.map((row) => {
    const owner = systemAccounts.get(row.resource_owner_system_account_id)
    const grantee = systemAccounts.get(row.grantee_system_account_id)
    const authorizationSources = sources.get(row.id) ?? []
    return {
      id: row.id,
      resourceType: row.resource_type,
      resourceId: row.resource_id,
      resourceName: row.resource_type === 'account' ? accountNames.get(row.resource_id) : groupNames.get(row.resource_id),
      resourceOwnerSystemAccountId: row.resource_owner_system_account_id,
      resourceOwnerSystemAccountName: owner?.displayName ?? owner?.username,
      granteeSystemAccountId: row.grantee_system_account_id,
      granteeSystemAccountName: grantee?.displayName ?? grantee?.username,
      granteeUsername: grantee?.username,
      scope: 'use',
      status: row.status,
      remark: row.remark ?? undefined,
      expiresAt: row.expires_at ?? undefined,
      limits: parseOptionalJsonObject(row.limits_json ?? undefined),
      modelPolicy: parseOptionalJsonObject(row.model_policy_json ?? undefined),
      effectiveSourceType: row.effective_source_type ?? undefined,
      effectiveSourceTeamId: row.effective_source_team_id ?? undefined,
      effectiveSourceTeamName: row.effective_source_team_id ? teamNames.get(row.effective_source_team_id) : undefined,
      activatedAt: row.activated_at ?? undefined,
      lastSourceChangedAt: row.last_source_changed_at ?? undefined,
      sources: authorizationSources,
      authorizationSources,
      usage: usage.get(row.id) ?? emptyAccountUsageSummary(),
      createdBy: row.created_by,
      createdAt: row.created_at,
      revokedBy: row.revoked_by ?? undefined,
      revokedAt: row.revoked_at ?? undefined,
      revokedReason: row.revoked_reason ?? undefined,
      updatedAt: row.updated_at
    }
  })
}

function systemTeamNameMap(teamIds: string[]): Map<string, string> {
  const ids = [...new Set(teamIds)].filter(Boolean)
  if (!ids.length) return new Map()
  const rows = getDatabase()
    .prepare(`SELECT id, name FROM system_teams WHERE id IN (${sqlPlaceholders(ids.length)})`)
    .all(...ids) as unknown as Array<{ id: string; name: string }>
  return new Map(rows.map((row) => [row.id, row.name]))
}

function loadResourceAuthorizationSourcesByAuthorizationIds(authorizationIds: string[]): Map<string, ResourceAuthorizationSourceSummary[]> {
  const ids = [...new Set(authorizationIds)].filter(Boolean)
  if (!ids.length) return new Map()
  const rows = getDatabase().prepare(`SELECT ras.*, system_teams.name AS team_name FROM resource_authorization_sources ras LEFT JOIN system_teams ON system_teams.id = ras.source_team_id WHERE ras.authorization_id IN (${sqlPlaceholders(ids.length)}) ORDER BY ras.status ASC, ras.created_at ASC`).all(...ids) as unknown as Array<ResourceAuthorizationSourceRow & { team_name?: string | null }>
  const result = new Map<string, ResourceAuthorizationSourceSummary[]>()
  for (const row of rows) {
    const summary: ResourceAuthorizationSourceSummary = { id: row.id, authorizationId: row.authorization_id, sourceType: row.source_type, sourceTeamId: row.source_team_id ?? undefined, sourceTeamName: row.team_name ?? undefined, status: row.status, activatedAt: row.activated_at ?? undefined, endedAt: row.ended_at ?? undefined, endedReason: row.ended_reason ?? undefined, createdBy: row.created_by, createdAt: row.created_at, revokedBy: row.revoked_by ?? undefined, revokedAt: row.revoked_at ?? undefined, updatedAt: row.updated_at }
    result.set(row.authorization_id, [...(result.get(row.authorization_id) ?? []), summary])
  }
  return result
}

function loadResourceAuthorizationUsageSummaries(authorizationIds: string[], statDate?: string): Map<string, AccountUsageSummary> {
  const ids = [...new Set(authorizationIds)].filter(Boolean)
  if (!ids.length) return new Map()
  return mergeUsageSummaryMaps(
    loadAuthorizationUsageSummariesByIds(ids, 'account_authorization', statDate),
    loadAuthorizationUsageSummariesByIds(ids, 'group_authorization', statDate)
  )
}

function loadResourceAuthorizationUsageDetails(authorization: ResourceAuthorizationSummary): ResourceAuthorizationUsageDetail[] {
  const scopeType = authorization.resourceType === 'account' ? 'account_authorization' : 'group_authorization'
  const rows = getDatabase().prepare(`
    SELECT
      ra.grantee_system_account_id AS system_account_id,
      COALESCE(stats.request_count, 0) AS request_count,
      COALESCE(stats.client_count, 0) AS client_count,
      COALESCE(stats.input_tokens, 0) AS input_tokens,
      COALESCE(stats.output_tokens, 0) AS output_tokens,
      COALESCE(stats.cache_read_tokens, 0) AS cache_read_tokens,
      COALESCE(stats.total_cost_usd, 0) AS total_cost,
      stats.last_used_at AS last_used_at
    FROM resource_authorizations ra
    LEFT JOIN usage_stats_daily stats
      ON stats.system_account_id = ra.resource_owner_system_account_id
      AND stats.scope_type = ?
      AND stats.scope_id = ra.id
      AND stats.stat_date = ?
    WHERE ra.resource_type = ? AND ra.resource_id = ?
  `).all(scopeType, todayDateKey(), authorization.resourceType, authorization.resourceId) as unknown as Array<AccountUsageAggregateRow & { system_account_id: string }>

  const systemAccounts = systemAccountRowsByIds(rows.map((row) => row.system_account_id))
  const details = rows.map((row) => {
    const account = systemAccounts.get(row.system_account_id)
    return {
      systemAccountId: row.system_account_id,
      systemAccountName: account?.displayName ?? account?.username,
      username: account?.username,
      ...usageSummaryFromAggregate(row)
    }
  })

  if (!details.some((detail) => detail.systemAccountId === authorization.granteeSystemAccountId)) {
    details.push({
      systemAccountId: authorization.granteeSystemAccountId,
      systemAccountName: authorization.granteeSystemAccountName,
      username: authorization.granteeUsername,
      ...emptyAccountUsageSummary()
    })
  }

  return details.sort((left, right) => {
    const leftTime = left.lastUsedAt ? Date.parse(left.lastUsedAt) : 0
    const rightTime = right.lastUsedAt ? Date.parse(right.lastUsedAt) : 0
    if (rightTime !== leftTime) {
      return rightTime - leftTime
    }
    return left.systemAccountId.localeCompare(right.systemAccountId)
  })
}

function systemAccountRowsByIds(systemAccountIds: string[]): Map<string, SystemAccountSummary> {
  const ids = [...new Set(systemAccountIds)].filter(Boolean)
  if (!ids.length) return new Map()
  const rows = getDatabase().prepare(`SELECT * FROM system_accounts WHERE id IN (${sqlPlaceholders(ids.length)})`).all(...ids) as unknown as SystemAccountRow[]
  return new Map(rows.map((row) => [row.id, systemAccountSummaryFromRow(row)]))
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
  if (!group || !canUseGroup(groupId, systemAccountId)) {
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
    expiresAt: optionalServerDateTimeIso(input.expiresAt ?? input.expires_at),
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
export function validateGatewayApiKey(key: string): GatewayApiKeyRow | undefined {
  if (!key.startsWith('sk-')) {
    return undefined
  }
  const keyHash = hashSecret(key)
  const now = Date.now()
  const cached = gatewayApiKeyCache.get(keyHash)
  if (cached && cached.forceRevalidateAtMs > now && !isGatewayApiKeyRowExpired(cached.row, now)) {
    return cached.row
  }

  const row = getDatabase().prepare('SELECT id, system_account_id, group_id, status, expires_at FROM api_keys WHERE key_hash = ?').get(keyHash) as unknown as GatewayApiKeyRow | undefined
  if (!row || row.status !== 'active') {
    gatewayApiKeyCache.delete(keyHash)
    return undefined
  }
  if (isGatewayApiKeyRowExpired(row, now)) {
    gatewayApiKeyCache.delete(keyHash)
    return undefined
  }
  gatewayApiKeyCache.set(keyHash, {
    row,
    forceRevalidateAtMs: now + GATEWAY_API_KEY_CACHE_MAX_STALE_MS
  }, { ttlMs: gatewayApiKeyCacheTtlMs(now, row) })
  return row
}

function isGatewayApiKeyRowExpired(row: GatewayApiKeyRow, now = Date.now()): boolean {
  if (!row.expires_at) return false
  const expiresAt = Date.parse(row.expires_at)
  return Number.isFinite(expiresAt) && expiresAt <= now
}

function gatewayApiKeyCacheTtlMs(now: number, row: GatewayApiKeyRow): number {
  if (!row.expires_at) return GATEWAY_API_KEY_CACHE_TTL_MS
  const keyExpiresAt = Date.parse(row.expires_at)
  return Number.isFinite(keyExpiresAt) ? Math.max(1, Math.min(GATEWAY_API_KEY_CACHE_TTL_MS, keyExpiresAt - now)) : GATEWAY_API_KEY_CACHE_TTL_MS
}

function invalidateGatewayApiKeyCacheById(id: string): void {
  for (const [keyHash, entry] of gatewayApiKeyCache.entries()) {
    if (entry.row.id === id) {
      gatewayApiKeyCache.delete(keyHash)
    }
  }
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
  if (!nextGroup || !canUseGroup(nextGroupId, systemAccountId)) {
    return undefined
  }
  const next: ApiKeySummary = {
    ...current,
    name: typeof input.name === 'string' ? input.name : current.name,
    status: input.status === 'disabled' ? 'disabled' : input.status === 'active' ? 'active' : current.status,
    groupId: nextGroupId,
    expiresAt: optionalServerDateTimeIso(input.expiresAt ?? input.expires_at) ?? current.expiresAt
  }
  getDatabase()
    .prepare('UPDATE api_keys SET name = ?, status = ?, group_id = ?, expires_at = ?, updated_at = ? WHERE id = ? AND system_account_id = ?')
    .run(next.name, next.status, next.groupId, next.expiresAt ?? null, nowIso(), id, systemAccountId)
  invalidateGatewayApiKeyCacheById(id)
  return next
}

export function deleteApiKey(id: string): boolean {
  const scope = buildSystemAccountScopeClause()
  const result = getDatabase().prepare(`DELETE FROM api_keys WHERE id = ?${scope.clause}`).run(id, ...scope.params)
  if (result.changes > 0) {
    invalidateGatewayApiKeyCacheById(id)
  }
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
      endpoint: optionalString(row.endpoint) ?? endpointFromSnapshot(requestSnapshot),
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

export function selectOpenAIAccountForGroup(groupId: string, systemAccountId = currentSystemAccountId()): OpenAIAccountSecret | undefined {
  return listOpenAIAccountsForGroup(groupId, systemAccountId)[0]
}

export function resolveGroupUsageAccessMetadata(groupId: string, systemAccountId: string): GroupUsageAccessMetadata | undefined {
  const group = groupOwnerAndProvider(groupId)
  if (!group) return undefined
  if (group.systemAccountId === systemAccountId) {
    return { groupOwnerSystemAccountId: group.systemAccountId, groupAccessType: 'owner' }
  }
  const authorization = activeGroupAuthorization(groupId, systemAccountId)
  if (!authorization) return undefined
  return {
    groupOwnerSystemAccountId: group.systemAccountId,
    groupAccessType: 'authorized',
    groupAuthorizationId: authorization.id
  }
}

export function listOpenAIAccountsForGroup(groupId: string, systemAccountId = currentSystemAccountId()): OpenAIAccountSecret[] {
  const database = getDatabase()
  const now = nowIso()
  const groupAccess = resolveGroupUsageAccessMetadata(groupId, systemAccountId)
  if (!groupAccess) {
    return []
  }
  disableExpiredAccounts({ systemAccountId: groupAccess.groupOwnerSystemAccountId, role: 'user' })
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
        SELECT id, system_account_id, name, type, status, credentials_encrypted, proxy_profile_id, passthrough_enabled, error_policy_id, cooldown_until, last_error_message
        FROM accounts
        WHERE id = ?
          AND provider_code = 'openai'
          AND type IN ('api_key', 'oauth')
          AND schedulable = 1
          AND (account_expires_at IS NULL OR account_expires_at > ?)
          AND status = 'active'
          AND (cooldown_until IS NULL OR cooldown_until <= ?)
      `)
      .get(groupAccount.account_id, now, now) as unknown as { id: string; system_account_id: string; name: string; type: AccountType; status: AccountStatus; credentials_encrypted: string; proxy_profile_id: string | null; passthrough_enabled: number; error_policy_id: string | null; cooldown_until: string | null; last_error_message: string | null } | undefined
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
    const accountAccess = resolveOpenAIAccountAccess(row.id, row.system_account_id, systemAccountId, groupAccess)
    if (!accountAccess) {
      continue
    }
    if (!canScheduleAuthorizedAccount({
      accountId: row.id,
      accountAccessType: accountAccess.accountAccessType,
      authorizationId: accountAccess.accountAuthorizationId,
      systemAccountId
    })) {
      continue
    }
    accounts.push({
      id: row.id,
      systemAccountId: row.system_account_id,
      accountOwnerSystemAccountId: row.system_account_id,
      groupOwnerSystemAccountId: groupAccess.groupOwnerSystemAccountId,
      accountAccessType: accountAccess.accountAccessType,
      groupAccessType: groupAccess.groupAccessType,
      accountAuthorizationId: accountAccess.accountAuthorizationId,
      groupAuthorizationId: groupAccess.groupAuthorizationId,
      name: row.name,
      type: row.type,
      status: row.status,
      baseUrl: typeof credentials.base_url === 'string' && credentials.base_url ? credentials.base_url : 'https://api.openai.com/v1',
      apiKey,
      refreshToken: typeof credentials.refresh_token === 'string' ? credentials.refresh_token : undefined,
      clientId: typeof credentials.client_id === 'string' ? credentials.client_id : undefined,
      proxyUrl: proxyUrlForProfile(row.proxy_profile_id),
      passthroughEnabled: row.passthrough_enabled === 1,
      errorPolicyId: row.error_policy_id ?? undefined,
      cooldownUntil: row.cooldown_until ?? undefined,
      lastErrorMessage: row.last_error_message ?? undefined,
      expiresAt: typeof credentials.expires_at === 'string' ? credentials.expires_at : undefined,
      credentials
    })
  }

  return accounts
}

function resolveOpenAIAccountAccess(
  accountId: string,
  accountOwnerSystemAccountId: string,
  callerSystemAccountId: string,
  groupAccess: GroupUsageAccessMetadata
): { accountAccessType: 'owner' | 'account_authorized' | 'group_authorized'; accountAuthorizationId?: string } | undefined {
  if (accountOwnerSystemAccountId === callerSystemAccountId) {
    return { accountAccessType: 'owner' }
  }
  if (groupAccess.groupAccessType === 'authorized') {
    return accountOwnerSystemAccountId === groupAccess.groupOwnerSystemAccountId
      ? { accountAccessType: 'group_authorized' }
      : undefined
  }
  const authorization = activeAccountAuthorization(accountId, callerSystemAccountId)
  return authorization ? { accountAccessType: 'account_authorized', accountAuthorizationId: authorization.id } : undefined
}

export interface UsageRecordInput {
  systemAccountId?: string
  requestId: string
  clientIp?: string
  apiKeyId?: string
  groupId?: string
  accountId?: string
  accountOwnerSystemAccountId?: string
  groupOwnerSystemAccountId?: string
  accountAccessType?: 'owner' | 'account_authorized' | 'group_authorized'
  groupAccessType?: 'owner' | 'authorized'
  accountAuthorizationId?: string
  groupAuthorizationId?: string
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
  createdAt?: string
}

export function createUsageRecord(input: UsageRecordInput): void {
  createUsageRecordsBatch([input])
}

export function createUsageRecordsBatch(inputs: UsageRecordInput[]): void {
  if (inputs.length === 0) {
    return
  }

  const database = getDatabase()
  const insertStatement = database.prepare(`
    INSERT INTO usage_records (
      id, system_account_id, request_id, client_ip, api_key_id, group_id, account_id, endpoint, provider_code, model, stream,
      status_code, success, first_token_ms, duration_ms, input_tokens, output_tokens, cache_read_tokens, cost_usd, error_code, error_message,
      request_snapshot_json, response_snapshot_json,
      account_owner_system_account_id, group_owner_system_account_id, account_access_type, group_access_type, account_authorization_id, group_authorization_id,
      created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const updateAccountStatement = database.prepare('UPDATE accounts SET last_used_at = ?, updated_at = ? WHERE id = ?')
  const accountLastUsedAt = new Map<string, string>()

  database.exec('BEGIN')
  try {
    for (const input of inputs) {
      const now = input.createdAt ?? nowIso()
      const systemAccountId = input.systemAccountId ?? systemAccountIdForUsage(input)
      const accessMetadata = usageAccessMetadata({ ...input, systemAccountId })
      insertStatement.run(
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
        accessMetadata.accountOwnerSystemAccountId ?? null,
        accessMetadata.groupOwnerSystemAccountId ?? null,
        accessMetadata.accountAccessType ?? null,
        accessMetadata.groupAccessType ?? null,
        accessMetadata.accountAuthorizationId ?? null,
        accessMetadata.groupAuthorizationId ?? null,
        now
      )

      if (input.accountId) {
        const previous = accountLastUsedAt.get(input.accountId)
        if (!previous || now > previous) {
          accountLastUsedAt.set(input.accountId, now)
        }
      }
    }

    for (const [accountId, lastUsedAt] of accountLastUsedAt) {
      updateAccountStatement.run(lastUsedAt, lastUsedAt, accountId)
    }

    database.exec('COMMIT')
  } catch (error) {
    try {
      database.exec('ROLLBACK')
    } catch {
    }
    throw error
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

function loadAccountUsageSummariesByAccountIds(accountIds: string[], statDate?: string): Map<string, AccountUsageSummary> {
  const ids = [...new Set(accountIds)].filter(Boolean)
  if (!ids.length) return new Map()
  const sourceTable = statDate ? 'usage_stats_daily' : 'usage_stats_totals'
  const dateClause = statDate ? ' AND stat_date = ?' : ''
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
    FROM ${sourceTable}
    WHERE scope_type = 'account'${dateClause} AND scope_id IN (${sqlPlaceholders(ids.length)})
  `).all(...(statDate ? [statDate, ...ids] : ids)) as unknown as AccountUsageAggregateRow[]

  const result = new Map<string, AccountUsageSummary>()
  for (const row of rows) {
    result.set(row.account_id, usageSummaryFromAggregate(row))
  }
  return result
}

function loadGroupUsageSummariesByGroupIds(groupIds: string[], statDate?: string): Map<string, AccountUsageSummary> {
  const ids = [...new Set(groupIds)].filter(Boolean)
  if (!ids.length) return new Map()
  const sourceTable = statDate ? 'usage_stats_daily' : 'usage_stats_totals'
  const dateClause = statDate ? ' AND stat_date = ?' : ''
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
    FROM ${sourceTable}
    WHERE scope_type = 'group'${dateClause} AND scope_id IN (${sqlPlaceholders(ids.length)})
  `).all(...(statDate ? [statDate, ...ids] : ids)) as unknown as Array<AccountUsageAggregateRow & { group_id: string }>

  const result = new Map<string, AccountUsageSummary>()
  for (const row of rows) {
    result.set(row.group_id, usageSummaryFromAggregate(row))
  }
  return result
}

function loadAuthorizationUsageSummariesByIds(authorizationIds: string[], scopeType: 'account_authorization' | 'group_authorization', statDate?: string): Map<string, AccountUsageSummary> {
  const ids = [...new Set(authorizationIds)].filter(Boolean)
  if (!ids.length) return new Map()
  const sourceTable = statDate ? 'usage_stats_daily' : 'usage_stats_totals'
  const dateClause = statDate ? ' AND stat_date = ?' : ''
  const rows = getDatabase().prepare(`
    SELECT
      scope_id AS authorization_id,
      request_count,
      client_count,
      input_tokens,
      output_tokens,
      cache_read_tokens,
      total_cost_usd AS total_cost,
      last_used_at
    FROM ${sourceTable}
    WHERE scope_type = ?${dateClause} AND scope_id IN (${sqlPlaceholders(ids.length)})
  `).all(...(statDate ? [scopeType, statDate, ...ids] : [scopeType, ...ids])) as unknown as Array<AccountUsageAggregateRow & { authorization_id: string }>

  const result = new Map<string, AccountUsageSummary>()
  for (const row of rows) {
    result.set(row.authorization_id, usageSummaryFromAggregate(row))
  }
  return result
}

interface UsageStatsScopeRequest {
  rowKey: string
  systemAccountId: string
  scopeType: string
  scopeId: string
}

type UsageStatsScopeAggregateRow = AccountUsageAggregateRow & {
  system_account_id: string
  scope_type: string
  scope_id: string
  stat_date?: string
}

function loadUsageByWindowForScopeRequests(scopes: UsageStatsScopeRequest[]): Map<string, UsageByWindow> {
  const validScopes = scopes.filter((scope) => scope.rowKey && scope.systemAccountId && scope.scopeType && scope.scopeId)
  const result = new Map<string, UsageByWindow>()
  const rowKeysByScopeMapKey = new Map<string, Set<string>>()
  const scopeRowsByMapKey = new Map<string, UsageStatsScopeRequest>()
  for (const scope of validScopes) {
    result.set(scope.rowKey, emptyUsageByWindow())
    const mapKey = usageStatsScopeMapKey(scope)
    scopeRowsByMapKey.set(mapKey, scope)
    const rowKeys = rowKeysByScopeMapKey.get(mapKey) ?? new Set<string>()
    rowKeys.add(scope.rowKey)
    rowKeysByScopeMapKey.set(mapKey, rowKeys)
  }
  const normalizedScopes = [...scopeRowsByMapKey.values()]
  if (!normalizedScopes.length) return result

  const database = getDatabase()
  const scopesBySystemAccountId = new Map<string, UsageStatsScopeRequest[]>()
  for (const scope of normalizedScopes) {
    scopesBySystemAccountId.set(scope.systemAccountId, [...(scopesBySystemAccountId.get(scope.systemAccountId) ?? []), scope])
  }
  const totalRows: UsageStatsScopeAggregateRow[] = []
  const dailyRows: UsageStatsScopeAggregateRow[] = []
  const maxDailyStartDate = usageWindowStartDate(30)

  for (const [systemAccountId, systemScopes] of scopesBySystemAccountId) {
    const scopeTypes = [...new Set(systemScopes.map((scope) => scope.scopeType))]
    const scopeIds = [...new Set(systemScopes.map((scope) => scope.scopeId))]
    for (const scopeIdChunk of chunkValues(scopeIds, 400)) {
      totalRows.push(...database.prepare(`
        SELECT
          system_account_id,
          scope_type,
          scope_id,
          scope_id AS account_id,
          request_count,
          client_count,
          input_tokens,
          output_tokens,
          cache_read_tokens,
          total_cost_usd AS total_cost,
          last_used_at
        FROM usage_stats_totals
        WHERE system_account_id = ?
          AND scope_type IN (${sqlPlaceholders(scopeTypes.length)})
          AND scope_id IN (${sqlPlaceholders(scopeIdChunk.length)})
      `).all(systemAccountId, ...scopeTypes, ...scopeIdChunk) as unknown as UsageStatsScopeAggregateRow[])

      dailyRows.push(...database.prepare(`
        SELECT
          system_account_id,
          scope_type,
          scope_id,
          scope_id AS account_id,
          stat_date,
          request_count,
          client_count,
          input_tokens,
          output_tokens,
          cache_read_tokens,
          total_cost_usd AS total_cost,
          last_used_at
        FROM usage_stats_daily
        WHERE system_account_id = ?
          AND scope_type IN (${sqlPlaceholders(scopeTypes.length)})
          AND scope_id IN (${sqlPlaceholders(scopeIdChunk.length)})
          AND stat_date >= ?
      `).all(systemAccountId, ...scopeTypes, ...scopeIdChunk, maxDailyStartDate) as unknown as UsageStatsScopeAggregateRow[])
    }
  }

  for (const row of totalRows) {
    const rowKeys = rowKeysByScopeMapKey.get(usageStatsRowMapKey(row))
    if (!rowKeys) continue
    for (const rowKey of rowKeys) {
      const usageByWindow = result.get(rowKey)
      if (usageByWindow) {
        usageByWindow.total = usageSummaryFromAggregate(row)
      }
    }
  }

  const startDateByWindow = new Map(USAGE_STATS_WINDOWS
    .filter((window) => window.days)
    .map((window) => [window.key, usageWindowStartDate(window.days ?? 1)]))
  for (const row of dailyRows) {
    const rowKeys = rowKeysByScopeMapKey.get(usageStatsRowMapKey(row))
    if (!rowKeys || !row.stat_date) continue
    const rowUsage = usageSummaryFromAggregate(row)
    for (const rowKey of rowKeys) {
      const usageByWindow = result.get(rowKey)
      if (!usageByWindow) continue
      for (const window of USAGE_STATS_WINDOWS) {
        if (!window.days) continue
        const startDate = startDateByWindow.get(window.key)
        if (startDate && row.stat_date >= startDate) {
          usageByWindow[window.key] = addUsageSummaries(usageByWindow[window.key], rowUsage)
        }
      }
    }
  }

  return result
}

function usageStatsScopeMapKey(scope: Pick<UsageStatsScopeRequest, 'systemAccountId' | 'scopeType' | 'scopeId'>): string {
  return `${scope.systemAccountId}\u0000${scope.scopeType}\u0000${scope.scopeId}`
}

function usageStatsRowMapKey(row: { system_account_id: string; scope_type: string; scope_id: string }): string {
  return `${row.system_account_id}\u0000${row.scope_type}\u0000${row.scope_id}`
}

function usageWindowStartDate(days: number): string {
  const date = new Date()
  date.setDate(date.getDate() - Math.max(0, Math.trunc(days) - 1))
  return dateKey(date)
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

function loadOpenAICodexUsageSnapshotsByAccountIds(accountIds: string[]): Map<string, AccountOAuthUsageSnapshot> {
  const ids = [...new Set(accountIds)].filter(Boolean)
  if (!ids.length) return new Map()
  const rows = getDatabase().prepare(`
    SELECT
      account_id, kind, source, snapshot_json, refresh_status,
      last_attempt_at, last_success_at, next_refresh_after, last_error_message, updated_at
    FROM account_usage_snapshots
    WHERE kind = 'openai_codex' AND account_id IN (${sqlPlaceholders(ids.length)})
  `).all(...ids) as unknown as AccountUsageSnapshotRow[]

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
  return key === 'apiKeyPrefix' || key === 'defaultOpenAIBaseUrl' || key === 'defaultErrorPolicyId' || key === 'defaultAccountConcurrencyLimit'
}

function usageAccessMetadata(input: {
  systemAccountId: string
  groupId?: string
  accountId?: string
  accountOwnerSystemAccountId?: string
  groupOwnerSystemAccountId?: string
  accountAccessType?: 'owner' | 'account_authorized' | 'group_authorized'
  groupAccessType?: 'owner' | 'authorized'
  accountAuthorizationId?: string
  groupAuthorizationId?: string
}): {
  accountOwnerSystemAccountId?: string
  groupOwnerSystemAccountId?: string
  accountAccessType?: 'owner' | 'account_authorized' | 'group_authorized'
  groupAccessType?: 'owner' | 'authorized'
  accountAuthorizationId?: string
  groupAuthorizationId?: string
} {
  const groupOwnerSystemAccountId = input.groupOwnerSystemAccountId ?? (input.groupId ? groupOwnerAndProvider(input.groupId)?.systemAccountId : undefined)
  const groupAuthorization = input.groupAuthorizationId
    ? undefined
    : input.groupId && groupOwnerSystemAccountId !== input.systemAccountId
      ? activeGroupAuthorization(input.groupId, input.systemAccountId)
      : undefined
  const groupAuthorizationId = input.groupAuthorizationId ?? groupAuthorization?.id
  const groupAccessType = input.groupAccessType
    ?? (groupOwnerSystemAccountId
      ? groupOwnerSystemAccountId === input.systemAccountId
        ? 'owner'
        : groupAuthorization
          ? 'authorized'
          : undefined
      : undefined)
  const accountOwnerSystemAccountId = input.accountOwnerSystemAccountId ?? (input.accountId ? accountSystemAccountId(input.accountId) : undefined)
  const accountAuthorization = input.accountAuthorizationId
    ? undefined
    : input.accountId && accountOwnerSystemAccountId !== input.systemAccountId && groupAccessType !== 'authorized'
      ? activeAccountAuthorization(input.accountId, input.systemAccountId)
      : undefined
  const accountAccessType = input.accountAccessType
    ?? (accountOwnerSystemAccountId
      ? accountOwnerSystemAccountId === input.systemAccountId
        ? 'owner'
        : groupAccessType === 'authorized' && groupOwnerSystemAccountId === accountOwnerSystemAccountId
          ? 'group_authorized'
          : accountAuthorization
            ? 'account_authorized'
            : undefined
      : undefined)
  return {
    accountOwnerSystemAccountId,
    groupOwnerSystemAccountId,
    accountAccessType,
    groupAccessType,
    accountAuthorizationId: accountAccessType === 'account_authorized' ? input.accountAuthorizationId ?? accountAuthorization?.id : undefined,
    groupAuthorizationId
  }
}
