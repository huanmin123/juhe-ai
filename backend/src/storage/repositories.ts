import type { DatabaseSync } from 'node:sqlite'

import type { AccountAuthorizationUsageOverview, AccountGroupBindStatus, AccountStatus, AccountSummary, AccountTrafficMigrationSourceStatus, AccountUsageStatsOverview, AccountUsageSummary, AuthorizationStatus, GroupSummary, ProviderCode, ResourceAuthorizationResourceType, ResourceAuthorizationSourceStatus, ResourceAuthorizationSourceType, ResourceAuthorizationSummary, ResourceAuthorizationUsageDetail, ResourcePermissions, SystemAccountPrincipalSummary, SystemTeamMemberSummary, SystemTeamSummary } from '../domain/types.js'
import { loadAccountCurrentConcurrencyByIds, sumAccountCurrentConcurrency } from '../shared/account-concurrency.js'
import { buildSystemAccountScopeClause, buildSystemAccountWhereClause, canAccessAll, currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, scopedSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { normalizeAccountListOptions, type AccountListOptions } from './account-list-options.js'
import { accountCredentialsForList, listAccountRowsForAccess, listAccountRowsPageForAccess, loadAccountAuthorizationUsageSummaries } from './account-read.repository.js'
import { getAccountAuthorizationUsageOverview as buildAccountAuthorizationUsageOverview, getAccountUsageStatsOverview as buildAccountUsageStatsOverview } from './account-usage.repository.js'
import { updateAccountUsageSnapshotRefreshState, upsertAccountUsageSnapshot } from './account-usage-snapshot.repository.js'
import { createApiKeyRecord, deleteApiKey, listApiKeys, listApiKeysPage, updateApiKey } from './api-key.repository.js'
import { loadResourceAuthorizationSourcesByAuthorizationIds, loadResourceAuthorizationStatsByResourceIds } from './authorization-read-loaders.js'
import { decryptJson, encryptJson, hashSecret, maskSecret } from './crypto.js'
import { getDatabase, newId, nowIso } from './database.js'
import { defaultGroupIdForSystemAccount } from './default-group.repository.js'
import { listErrorPolicies } from './error-policy.repository.js'
import { clearGatewayApiKeyValidationCache } from './gateway-api-key.repository.js'
import { emptyGroupAccountStats, groupAccountStatsFromRow } from './group-account-stats.mapper.js'
import { listGroupRowsForAccess, loadGroupAuthorizationUsageSummaries } from './group-read.repository.js'
import { loadGroupAccountIdsByGroupIds, loadGroupAccountStatsByGroupIds } from './group-read-loaders.js'
import { loadOpenAICodexUsageSnapshotsByAccountIds } from './oauth-usage-loaders.js'
import { listProviders, providerPassthroughEnabled } from './provider.repository.js'
import { resolveEnabledProxyProfileId } from './proxy.repository.js'
import { sqlPlaceholders } from './query-utils.js'
import { loadAccountNameMap, loadGroupNameMap, loadSystemAccountNameMap, loadSystemAccountsByIds, loadSystemTeamNameMap } from './repository-lookups.js'
import { normalizeRequestQuotaLimits, parseRequestQuotaLimitsJson, requestQuotaLimitsJson } from './request-quota-limits.js'
import type { AccountFailureRow, AccountListRow, AccountRow, ResourceAuthorizationRow, ResourceAuthorizationSourceRow, SystemTeamMemberRow, SystemTeamRow, TeamResourceAuthorizationGrantRow } from './repository-row-types.js'
import { getSettings } from './settings.repository.js'
import type { SystemAccountRow } from './system-account-mappers.js'
import { findSystemAccountById } from './system-accounts.repository.js'
import { refreshGroupAccountStatsCache } from './usage-stats.repository.js'
import { emptyAccountUsageSummary, numberFromUnknown, todayDateKey, usageSummaryFromAggregate } from './usage-stats-helpers.js'
import { loadAccountUsageSummariesForScopes, loadGroupUsageSummariesForScopes, type UsageSummaryScopeRequest } from './usage-summary-loaders.js'
import { loadUsageByWindowForScopeRequests } from './usage-window-loaders.js'
import {
  jsonObjectOrNull,
  optionalNullableServerDateTimeIso,
  optionalNullableString,
  optionalServerDateTimeIso,
  optionalString,
  parseOptionalJsonObject
} from './value-utils.js'

const DEFAULT_ACCOUNT_CONCURRENCY_LIMIT = 20
const manualTrafficMigrationReason = '手动迁移流量'

export type { AccountListOptions, AccountListSchedulableFilter, AccountListSortDirection, AccountListSortField } from './account-list-options.js'

export class DuplicateAccountCredentialError extends Error {
  constructor() {
    super('账户凭据已被其他账户使用，不能重复添加')
    this.name = 'DuplicateAccountCredentialError'
  }
}

export class DefaultGroupReadonlyError extends Error {
  constructor() {
    super('默认分组不允许修改')
    this.name = 'DefaultGroupReadonlyError'
  }
}

export type { AccountUsageSummary, SystemAccountPrincipalSummary, SystemAccountRole, SystemAccountStatus, SystemAccountSummary } from '../domain/types.js'
export {
  createApiKeyRecord,
  deleteApiKey,
  listApiKeys,
  listApiKeysPage,
  updateApiKey
} from './api-key.repository.js'
export { listErrorPolicies } from './error-policy.repository.js'
export { listProviders } from './provider.repository.js'
export {
  createSession,
  createSystemAccount,
  findSessionByToken,
  findSystemAccountById,
  findSystemAccountByUsername,
  listSystemAccounts,
  revokeAllSessionsForAccount,
  revokeSession,
  touchSession,
  updateSystemAccount,
  updateSystemAccountLastLogin,
  verifySystemAccountCredentials,
  type SessionWithAccount
} from './system-accounts.repository.js'
export {
  createProxy,
  deleteProxy,
  getProxyTestConfig,
  listEnabledProxyTestConfigs,
  listProxyOptions,
  listProxies,
  ProxyInUseError,
  ProxyProfileUnavailableError,
  resolveEnabledProxyProfileId,
  resolveProxyUrlForProfile,
  resolveProxyUrlForProfileForSystemAccount,
  updateProxyTestState,
  updateProxy,
  type ProxyProfileOptionSummary,
  type ProxyProfileSummary,
  type ProxyProfileTestConfig
} from './proxy.repository.js'
export {
  getSettings,
  listPublicGlobalSettings,
  listGlobalSettings,
  updateGlobalSettings,
  updateSettings
} from './settings.repository.js'
export {
  clearGatewayApiKeyValidationCache,
  validateGatewayApiKey,
  type GatewayApiKeyRow
} from './gateway-api-key.repository.js'
export {
  updateAccountUsageSnapshotRefreshState,
  upsertAccountUsageSnapshot
} from './account-usage-snapshot.repository.js'
export {
  createUsageRecord,
  createUsageRecordsBatch,
  findRecentOpenAIRequestShapeForAccount,
  getUsageRecordDetail,
  listUsageRecords,
  type RecentOpenAIRequestShape,
  type UsageRecordInput,
  type UsageRecordListResult,
  type UsageRecordListOptions,
  type UsageRecordLogSnapshot,
  type UsageRecordSortDirection,
  type UsageRecordSortField,
  type UsageRecordSummary
} from './usage-records.repository.js'
export type {
  ApiKeyListOptions,
  ApiKeyListResult
} from './api-key.repository.js'
export {
  cleanupAuditLogsBefore,
  createAuditLogsBatch,
  getAuditLogDetail,
  getAuditLogPayload,
  listAuditLogs,
  type AuditLogAttemptInput,
  type AuditLogAttemptSummary,
  type AuditLogDetail,
  type AuditLogInput,
  type AuditLogListResult,
  type AuditLogListOptions,
  type AuditLogPayloadDetail,
  type AuditLogPayloadInput,
  type AuditLogPayloadSummary,
  type AuditLogSummary,
  type AuditOutcome,
  type AuditPayloadPartType
} from './audit-logs.repository.js'
export {
  cleanupRuntimeLogIndex,
  createRuntimeLogsBatch,
  getRuntimeLogFacets,
  listRuntimeLogs,
  runtimeLogIndexRetentionDays,
  type RuntimeLogFacets,
  type RuntimeLogIndexInput,
  type RuntimeLogLevel,
  type RuntimeLogListResult,
  type RuntimeLogListOptions,
  type RuntimeLogSummary
} from './runtime-logs.repository.js'
export {
  cleanupExpiredSystemSessions,
  cleanupProcessedUsageRecordsBefore,
  cleanupSystemMetricsBefore,
  cleanupUsageStatsBucketsBefore,
  type SystemMetricsRetentionCleanupResult,
  type UsageStatsRetentionCleanupResult
} from './data-retention.repository.js'
export {
  findOpenAIAccountForGroup,
  listOpenAIAccountsForGroup,
  resolveGroupUsageAccessMetadata,
  selectOpenAIAccountForGroup,
  type GroupUsageAccessMetadata,
  type OpenAIAccountSecret
} from './openai-account-selector.repository.js'
export {
  refreshAccountQualityFromUsage,
  type AccountQualityRealtimeRefreshResult
} from './account-quality.repository.js'

interface AccountUsageAggregateRow {
  account_id: string
  request_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  total_cost: number
  last_used_at: string | null
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

function accountSystemAccountId(accountId: string): string | undefined {
  const row = getDatabase().prepare('SELECT system_account_id FROM accounts WHERE id = ?').get(accountId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id
}

function canUseAccount(accountId: string, systemAccountId: string): boolean {
  const ownerId = accountSystemAccountId(accountId)
  if (ownerId === systemAccountId) return true
  return Boolean(activeResourceAuthorization('account', accountId, systemAccountId))
}

function accountRowForManage(accountId: string, access?: AccessScope): AccountRow | undefined {
  const row = getDatabase().prepare('SELECT * FROM accounts WHERE id = ?').get(accountId) as unknown as AccountRow | undefined
  if (!row || !canManageResourceOwner(row.system_account_id, access)) {
    return undefined
  }
  return row
}

function accountEnabledGroupId(accountId: string, systemAccountId: string): string | undefined {
  const row = getDatabase()
    .prepare(`
      SELECT group_id
      FROM group_accounts
      WHERE account_id = ?
        AND system_account_id = ?
        AND enabled = 1
      ORDER BY updated_at DESC
      LIMIT 1
    `)
    .get(accountId, systemAccountId) as unknown as { group_id?: string } | undefined
  return row?.group_id
}

function accountGroupBinding(accountId: string, systemAccountId: string): { groupId: string; groupName: string; groupBindStatus: AccountGroupBindStatus } | undefined {
  const row = getDatabase()
    .prepare(`
      SELECT
        group_accounts.group_id,
        group_accounts.account_authorization_id,
        groups.name AS group_name
      FROM group_accounts
      INNER JOIN groups ON groups.id = group_accounts.group_id
      WHERE group_accounts.account_id = ?
        AND group_accounts.system_account_id = ?
        AND group_accounts.enabled = 1
      ORDER BY group_accounts.updated_at DESC
      LIMIT 1
    `)
    .get(accountId, systemAccountId) as unknown as { group_id?: string; group_name?: string; account_authorization_id?: string | null } | undefined
  if (!row?.group_id) {
    return undefined
  }
  const ownerId = accountSystemAccountId(accountId)
  const authorization = ownerId && ownerId !== systemAccountId ? activeAccountAuthorization(accountId, systemAccountId) : undefined
  return {
    groupId: row.group_id,
    groupName: row.group_name ?? row.group_id,
    groupBindStatus: row.account_authorization_id && authorization?.id !== row.account_authorization_id ? 'authorization_unavailable' : 'bound'
  }
}

function accountGroupBindingFromRow(row: AccountListRow, systemAccountId?: string): { groupId: string; groupName: string; groupBindStatus: AccountGroupBindStatus } | undefined {
  if (!row.bound_group_id || row.binding_system_account_id !== systemAccountId) {
    return undefined
  }
  const accountOwnerId = row.system_account_id
  const authorization = accountOwnerId && systemAccountId && accountOwnerId !== systemAccountId
    ? activeAccountAuthorization(row.id, systemAccountId)
    : undefined
  return {
    groupId: row.bound_group_id,
    groupName: row.bound_group_name ?? row.bound_group_id,
    groupBindStatus: row.bound_group_account_authorization_id && authorization?.id !== row.bound_group_account_authorization_id ? 'authorization_unavailable' : 'bound'
  }
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

function groupOwnerAndProvider(groupId: string): { systemAccountId: string; providerCode: ProviderCode; name?: string } | undefined {
  const row = getDatabase().prepare('SELECT system_account_id, provider_code, name FROM groups WHERE id = ?').get(groupId) as unknown as { system_account_id?: string; provider_code?: ProviderCode; name?: string } | undefined
  return row?.system_account_id && row.provider_code ? { systemAccountId: row.system_account_id, providerCode: row.provider_code, name: row.name } : undefined
}

function apiKeySystemAccountId(apiKeyId: string): string | undefined {
  const row = getDatabase().prepare('SELECT system_account_id FROM api_keys WHERE id = ?').get(apiKeyId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id
}

function globalProxyProfileId(proxyProfileId: string | undefined): string | undefined {
  return resolveEnabledProxyProfileId(proxyProfileId)
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
  const result = getDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'disabled',
          schedulable = 0,
          super_priority_enabled = 0,
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
  if (Number(result.changes ?? 0) > 0) {
    refreshGroupAccountStatsAfterWrite()
  }
}

export function expireDueResourceAuthorizations(): number {
  const now = nowIso()
  const database = getDatabase()
  const result = database
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
  const changed = Number(result.changes ?? 0)
  if (changed > 0) {
    cleanupInactiveAuthorizationBindings(database)
  }
  return changed
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

function normalizeSuperPriorityInput(value: unknown, fallback: boolean): boolean {
  if (typeof value === 'boolean') return value
  if (value === 1 || value === '1') return true
  if (value === 0 || value === '0') return false
  return fallback
}

function effectiveSuperPriorityEnabled(status: AccountStatus, value: unknown): boolean {
  return status === 'active' && normalizeSuperPriorityInput(value, false)
}

function usageScope(rowKey: string, systemAccountId: string, scopeId: string): UsageSummaryScopeRequest {
  return { rowKey, systemAccountId, scopeId }
}

function sanitizeAuthorizationSourcesForViewer(sources: ResourceAuthorizationSummary['authorizationSources'], limited: boolean): ResourceAuthorizationSummary['authorizationSources'] {
  if (!sources) return sources
  if (!limited) return sources
  return sources.map((source) => ({
    id: source.id,
    authorizationId: source.authorizationId,
    sourceType: source.sourceType,
    status: source.status,
    activatedAt: source.activatedAt,
    endedReason: source.endedReason,
    createdBy: '',
    createdAt: source.createdAt,
    updatedAt: source.updatedAt
  }))
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

function writeSystemAccountId(access?: AccessScope): string {
  return manageableSystemAccountId(access) ?? currentSystemAccountId(access)
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

function isManualTrafficMigrationState(account: Pick<AccountSummary, 'lastErrorMessage' | 'status'>): boolean {
  return account.status === 'temporary_unavailable' && account.lastErrorMessage === manualTrafficMigrationReason
}

function refreshGroupAccountStatsAfterWrite(): void {
  if (getDatabase().isTransaction) {
    return
  }
  refreshGroupAccountStatsCache()
}

export interface AccountListResult {
  items: AccountSummary[]
  total: number
  page: number
  pageSize: number
}

export function listAccounts(access?: AccessScope, options?: AccountListOptions): AccountSummary[] {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  disableExpiredAccounts(access)
  const listOptions = normalizeAccountListOptions(options)
  const rows = listAccountRowsForAccess(access, listOptions)
  return accountSummariesFromRows(rows, access, viewerSystemAccountId)
}

export function listAccountsPage(access?: AccessScope, options?: AccountListOptions): AccountListResult {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  disableExpiredAccounts(access)
  const listOptions = normalizeAccountListOptions(options)
  const page = listAccountRowsPageForAccess(access, listOptions)
  return {
    items: accountSummariesFromRows(page.rows, access, viewerSystemAccountId),
    total: page.total,
    page: listOptions.page,
    pageSize: listOptions.pageSize
  }
}

function accountSummariesFromRows(rows: AccountListRow[], access: AccessScope | undefined, viewerSystemAccountId: string | undefined): AccountSummary[] {
  const accountIds = rows.map((row) => row.id)
  const currentConcurrencyByAccount = loadAccountCurrentConcurrencyByIds(accountIds)
  const accountUsageScopes = rows.map((row) => usageScope(row.id, row.system_account_id, row.id))
  const usageByAccount = loadAccountUsageSummariesForScopes(accountUsageScopes)
  const todayUsageByAccount = loadAccountUsageSummariesForScopes(accountUsageScopes, todayDateKey())
  const authorizationStatsByAccount = loadResourceAuthorizationStatsByResourceIds('account', accountIds)
  const authorizationScopes = rows
    .filter((row) => row.authorization_id)
    .map((row) => usageScope(row.authorization_id ?? '', row.system_account_id, row.authorization_id ?? ''))
  const usageByAuthorization = loadAccountAuthorizationUsageSummaries(authorizationScopes)
  const todayUsageByAuthorization = loadAccountAuthorizationUsageSummaries(authorizationScopes, todayDateKey())
  const sourcesByAuthorization = loadResourceAuthorizationSourcesByAuthorizationIds(rows.map((row) => row.authorization_id ?? ''))
  const oauthUsageByAccount = loadOpenAICodexUsageSnapshotsByAccountIds(rows.map((row) => row.id))
  const hasAuthorizedRows = rows.some((row) => row.access_type === 'authorized')
  const accountNames = includeSystemAccountFields(access) || hasAuthorizedRows ? loadSystemAccountNameMap() : new Map<string, string>()
  return rows.map((row) => {
    const isAuthorizedView = row.access_type === 'authorized'
    const usage = isAuthorizedView && row.authorization_id
      ? usageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : usageByAccount.get(row.id) ?? emptyAccountUsageSummary()
    const todayUsage = isAuthorizedView && row.authorization_id
      ? todayUsageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : todayUsageByAccount.get(row.id) ?? emptyAccountUsageSummary()
    const authorizationStats = authorizationStatsByAccount.get(row.id) ?? { authorizationCount: 0, authorizationTeamCount: 0 }
    const groupBindingSystemAccountId = row.access_type === 'authorized'
      ? viewerSystemAccountId
      : row.system_account_id
    const groupBinding = groupBindingSystemAccountId
      ? accountGroupBindingFromRow(row, groupBindingSystemAccountId) ?? accountGroupBinding(row.id, groupBindingSystemAccountId)
      : undefined
    return {
      id: row.id,
      systemAccountId: includeSystemAccountFields(access) ? row.system_account_id : undefined,
      systemAccountName: includeSystemAccountFields(access) ? accountNames.get(row.system_account_id) : undefined,
      ownerSystemAccountId: row.system_account_id,
      ownerSystemAccountName: accountNames.get(row.system_account_id),
      providerCode: row.provider_code,
      name: row.name,
      notes: isAuthorizedView ? undefined : row.notes ?? undefined,
      type: row.type,
      credentials: accountCredentialsForList(row),
      status: row.status,
      concurrencyLimit: isAuthorizedView ? 0 : row.concurrency_limit,
      currentConcurrency: isAuthorizedView ? 0 : (currentConcurrencyByAccount.get(row.id) ?? 0),
      priority: isAuthorizedView ? 0 : row.priority,
      superPriorityEnabled: !isAuthorizedView && row.status === 'active' && row.super_priority_enabled === 1,
      qualityScore: typeof row.quality_score === 'number' ? row.quality_score : undefined,
      qualityState: typeof row.quality_state === 'string' ? row.quality_state : undefined,
      qualityEwmaFirstTokenMs: typeof row.quality_ewma_first_token_ms === 'number' ? row.quality_ewma_first_token_ms : undefined,
      qualityRecentAvgFirstTokenMs: typeof row.quality_recent_avg_first_token_ms === 'number' ? row.quality_recent_avg_first_token_ms : undefined,
      qualityRecentRequestCount: typeof row.quality_recent_request_count === 'number' ? row.quality_recent_request_count : undefined,
      qualityRecentSuccessRate: typeof row.quality_recent_success_rate === 'number' ? row.quality_recent_success_rate : undefined,
      qualityUpdatedAt: row.quality_updated_at ?? undefined,
      proxyProfileId: isAuthorizedView ? undefined : row.proxy_profile_id ?? undefined,
      passthroughEnabled: isAuthorizedView ? false : row.passthrough_enabled === 1,
      errorPolicyId: isAuthorizedView ? undefined : row.error_policy_id ?? undefined,
      schedulable: isAuthorizedView ? row.status === 'active' : row.schedulable === 1,
      accountExpiresAt: isAuthorizedView ? undefined : row.account_expires_at ?? undefined,
      cooldownUntil: isAuthorizedView ? undefined : row.cooldown_until ?? undefined,
      lastErrorMessage: isAuthorizedView ? undefined : row.last_error_message ?? undefined,
      lastUsedAt: isAuthorizedView ? usage.lastUsedAt : row.last_used_at ?? usage.lastUsedAt,
      todayUsage,
      usage,
      oauthUsage: !isAuthorizedView && row.provider_code === 'openai' && row.type === 'oauth' ? oauthUsageByAccount.get(row.id) : undefined,
      accessType: row.access_type ?? 'owner',
      accountAuthorizationId: row.authorization_id ?? undefined,
      boundGroupId: groupBinding?.groupId,
      boundGroupName: groupBinding?.groupName,
      groupBindStatus: groupBinding?.groupBindStatus,
      authorizationStatus: row.authorization_status ?? undefined,
      authorizationSources: row.authorization_id ? sanitizeAuthorizationSourcesForViewer(sourcesByAuthorization.get(row.authorization_id) ?? [], isAuthorizedView) : undefined,
      permissions: isAuthorizedView && row.system_account_id !== viewerSystemAccountId ? authorizedPermissions() : ownerPermissions(),
      authorizationUsageAvailable: !isAuthorizedView && authorizationStats.authorizationCount > 0 && canManageResourceOwner(row.system_account_id, access),
      authorizationCount: isAuthorizedView ? 0 : authorizationStats.authorizationCount,
      authorizationTeamCount: isAuthorizedView ? 0 : authorizationStats.authorizationTeamCount
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

export function getAccountUsageStatsOverviewPage(access?: AccessScope, options?: AccountListOptions): AccountUsageStatsOverview {
  const accountPage = listAccountsPage(access, options)
  return buildAccountUsageStatsOverview({
    access,
    accounts: accountPage.items,
    total: accountPage.total,
    page: accountPage.page,
    pageSize: accountPage.pageSize,
    loadUsageByWindow: loadUsageByWindowForScopeRequests
  })
}

export function getAccountAuthorizationUsageOverview(accountId: string, access?: AccessScope): AccountAuthorizationUsageOverview | undefined {
  const accountRow = getDatabase().prepare('SELECT id, system_account_id, provider_code, name, type, status FROM accounts WHERE id = ?').get(accountId) as unknown as Pick<AccountRow, 'id' | 'system_account_id' | 'provider_code' | 'name' | 'type' | 'status'> | undefined
  if (!accountRow || !canManageResourceOwner(accountRow.system_account_id, access)) {
    return undefined
  }

  const authorizations = listResourceAuthorizations({ resourceType: 'account', resourceId: accountId, status: 'active' }, access)
  const accountNames = loadSystemAccountsByIds([accountRow.system_account_id])
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
        AND status IN ('rate_limited', 'temporary_unavailable')
        AND schedulable = 1
        AND cooldown_until IS NOT NULL
        AND cooldown_until <= ?
        AND (account_expires_at IS NULL OR account_expires_at > ?)
      ORDER BY cooldown_until ASC, priority ASC
      LIMIT ?
    `)
    .all(nowIso(), nowIso(), Math.max(1, Math.min(Math.trunc(limit), 200))) as unknown as AccountListRow[]
  const accountNames = loadSystemAccountNameMap()
  const currentConcurrencyByAccount = loadAccountCurrentConcurrencyByIds(rows.map((row) => row.id))
  return rows.map((row) => {
    const groupBinding = accountGroupBinding(row.id, row.system_account_id)
    return {
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
      currentConcurrency: currentConcurrencyByAccount.get(row.id) ?? 0,
      priority: row.priority,
      superPriorityEnabled: row.status === 'active' && row.super_priority_enabled === 1,
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
      accessType: 'owner' as const,
      boundGroupId: groupBinding?.groupId,
      boundGroupName: groupBinding?.groupName,
      groupBindStatus: groupBinding?.groupBindStatus,
      permissions: ownerPermissions()
    }
  })
}

export function createAccount(input: Record<string, unknown>, access?: AccessScope): AccountSummary {
  const now = nowIso()
  const id = newId('acc')
  const providerCode = String(input.providerCode ?? input.provider_code ?? 'openai')
  const explicitGroupId = typeof input.groupId === 'string' && input.groupId ? input.groupId : typeof input.group_id === 'string' && input.group_id ? input.group_id : undefined
  const explicitGroup = explicitGroupId ? groupOwnerAndProvider(explicitGroupId) : undefined
  const requestedSystemAccountId = writeSystemAccountId(access)
  const systemAccountId = explicitGroup && canManageResourceOwner(explicitGroup.systemAccountId, access) ? explicitGroup.systemAccountId : requestedSystemAccountId
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
    throw new Error('账户分组不能为空')
  }
  const group = explicitGroupId === groupId ? explicitGroup : groupOwnerAndProvider(groupId)
  if (!group || group.systemAccountId !== systemAccountId || group.providerCode !== providerCode) {
    throw new Error('账户分组无效')
  }
  const proxyProfileId = globalProxyProfileId(optionalString(input.proxyProfileId ?? input.proxy_profile_id))
  const account: AccountSummary = {
    id,
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields(access) ? loadSystemAccountNameMap().get(systemAccountId) : undefined,
    providerCode,
    name: String(input.name ?? `未命名 ${provider?.name ?? providerCode.toUpperCase()} 账户`),
    notes: optionalString(input.notes),
    type: accountType,
    credentials,
    status: nextStatus,
    concurrencyLimit: Number(input.concurrencyLimit ?? input.concurrency_limit ?? DEFAULT_ACCOUNT_CONCURRENCY_LIMIT),
    currentConcurrency: 0,
    priority: Number(input.priority ?? input.prioritiy ?? input.priority_level ?? 0),
    superPriorityEnabled: effectiveSuperPriorityEnabled(nextStatus, input.superPriorityEnabled ?? input.super_priority_enabled),
    proxyProfileId,
    passthroughEnabled: providerPassthroughEnabled(provider),
    errorPolicyId: optionalString(input.errorPolicyId ?? input.error_policy_id),
    schedulable: expiredByPackage ? false : input.schedulable !== false,
    accountExpiresAt: accountExpiresAt ?? undefined,
    cooldownUntil: expiredByPackage ? undefined : initialCooldownUntil,
    lastErrorMessage: expiredByPackage ? '账户套餐已过期，已自动停用' : initialCooldownUntil ? '创建时设置为临时不可调用' : undefined,
    lastUsedAt: undefined,
    todayUsage: emptyAccountUsageSummary(),
    usage: emptyAccountUsageSummary(),
    boundGroupId: groupId,
    boundGroupName: group.name ?? groupId,
    groupBindStatus: 'bound'
  }

  const database = getDatabase()
  database.exec('BEGIN')
  try {
    database
      .prepare(`
        INSERT INTO accounts (
          id, system_account_id, provider_code, name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
          proxy_profile_id, concurrency_limit, passthrough_enabled, error_policy_id,
          priority, super_priority_enabled, schedulable, notes, account_expires_at, cooldown_until, last_error_message, stream_failure_count, stream_failure_window_started_at, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
        account.superPriorityEnabled ? 1 : 0,
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
  refreshGroupAccountStatsAfterWrite()

  return account
}

export function updateAccount(id: string, input: Record<string, unknown>, access?: AccessScope): AccountSummary | undefined {
  const current = listAccounts(access).find((account) => account.id === id)
  if (!current) {
    return undefined
  }
  const systemAccountId = accountSystemAccountId(id) ?? currentSystemAccountId(access)
  if (!canManageResourceOwner(systemAccountId, access)) {
    return undefined
  }
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
  const hasNotesInput = Object.prototype.hasOwnProperty.call(input, 'notes')
  const rawErrorPolicyId = Object.prototype.hasOwnProperty.call(input, 'errorPolicyId')
    ? input.errorPolicyId
    : Object.prototype.hasOwnProperty.call(input, 'error_policy_id')
      ? input.error_policy_id
      : undefined

  const hasStatusInput = Object.prototype.hasOwnProperty.call(input, 'status')
  const requestedStatus = hasStatusInput ? normalizeAccountStatus(input.status, current.status) : current.status
  if (hasStatusInput && requestedStatus === 'active' && isCoolingAccountStatus(current.status)) {
    throw new Error('临时不可调用或限流中的账户不能通过启用账户恢复，请使用恢复正常或先执行实际测试')
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
  const hasSuperPriorityInput = Object.prototype.hasOwnProperty.call(input, 'superPriorityEnabled')
    || Object.prototype.hasOwnProperty.call(input, 'super_priority_enabled')
  const requestedSuperPriority = normalizeSuperPriorityInput(
    Object.prototype.hasOwnProperty.call(input, 'superPriorityEnabled') ? input.superPriorityEnabled : input.super_priority_enabled,
    current.superPriorityEnabled
  )
  if (hasSuperPriorityInput && requestedSuperPriority && nextStatus !== 'active') {
    throw new Error('只有正常状态的账户可以设置超级优先')
  }
  const nextSuperPriorityEnabled = nextStatus === 'active' ? requestedSuperPriority : false

  const next: AccountSummary = {
    ...current,
    name: typeof input.name === 'string' ? input.name : current.name,
    notes: hasNotesInput ? optionalNullableString(input.notes) ?? undefined : current.notes,
    credentials,
    status: nextStatus,
    concurrencyLimit: Number(input.concurrencyLimit ?? input.concurrency_limit ?? current.concurrencyLimit),
    priority: Number(input.priority ?? input.prioritiy ?? input.priority_level ?? current.priority),
    superPriorityEnabled: nextSuperPriorityEnabled,
    proxyProfileId: Object.prototype.hasOwnProperty.call(input, 'proxyProfileId') || Object.prototype.hasOwnProperty.call(input, 'proxy_profile_id')
      ? globalProxyProfileId(optionalString(input.proxyProfileId ?? input.proxy_profile_id))
      : current.proxyProfileId,
    passthroughEnabled: providerPassthroughEnabled(provider),
    errorPolicyId: rawErrorPolicyId === undefined ? current.errorPolicyId : optionalString(rawErrorPolicyId),
    schedulable: expiredByPackage
      ? false
      : hasStatusInput
        ? !['disabled', 'error'].includes(nextStatus)
        : typeof input.schedulable === 'boolean'
          ? input.schedulable
          : current.schedulable,
    accountExpiresAt: nextAccountExpiresAt ?? undefined,
    cooldownUntil: nextCooldownUntil,
    lastErrorMessage: nextLastErrorMessage,
    lastUsedAt: current.lastUsedAt,
    usage: current.usage
  }

  try {
    const result = getDatabase()
      .prepare(`
      UPDATE accounts
      SET name = ?, notes = ?, status = ?, credentials_encrypted = ?, credential_fingerprint = ?, credential_mask = ?,
            proxy_profile_id = ?, concurrency_limit = ?, passthrough_enabled = ?,
            error_policy_id = ?, priority = ?, super_priority_enabled = ?, schedulable = ?, account_expires_at = ?, cooldown_until = ?, last_error_message = ?, updated_at = ?
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
        next.superPriorityEnabled ? 1 : 0,
        next.schedulable ? 1 : 0,
        next.accountExpiresAt ?? null,
        next.cooldownUntil ?? null,
        next.lastErrorMessage ?? null,
        nowIso(),
        id,
        systemAccountId
      )
    if (Number(result.changes ?? 0) > 0) {
      refreshGroupAccountStatsAfterWrite()
    }
  } catch (error) {
    if (isDuplicateAccountCredentialError(error)) {
      throwDuplicateAccountCredentialError()
    }
    throw error
  }

  return next
}

export function deleteAccount(id: string, access?: AccessScope): boolean {
  const scope = buildSystemAccountScopeClause(access)
  const result = getDatabase().prepare(`DELETE FROM accounts WHERE id = ?${scope.clause}`).run(id, ...scope.params)
  if (Number(result.changes ?? 0) > 0) {
    refreshGroupAccountStatsAfterWrite()
  }
  return result.changes > 0
}

export function clearAccountFailureState(
  id: string,
  options: { preserveManualTrafficMigration?: boolean } = {},
  access?: AccessScope
): AccountSummary | undefined {
  const current = listAccounts(access).find((account) => account.id === id)
  if (!current) {
    return undefined
  }
  const ownerSystemAccountId = accountSystemAccountId(id)
  if (ownerSystemAccountId && !canManageResourceOwner(ownerSystemAccountId, access)) {
    return undefined
  }
  if (options.preserveManualTrafficMigration && isManualTrafficMigrationState(current)) {
    return current
  }

  const expiredByPackage = isAccountExpired(current.accountExpiresAt)
  if (expiredByPackage) {
    const result = getDatabase()
      .prepare(`
        UPDATE accounts
        SET status = 'disabled',
            schedulable = 0,
            super_priority_enabled = 0,
            cooldown_until = NULL,
            last_error_message = ?,
            stream_failure_count = 0,
            stream_failure_window_started_at = NULL,
            updated_at = ?
        WHERE id = ?
      `)
      .run('账户套餐已过期，已自动停用', nowIso(), id)
    if (Number(result.changes ?? 0) > 0) {
      refreshGroupAccountStatsAfterWrite()
    }
    return listAccounts(access).find((account) => account.id === id)
  }

  const result = getDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'active',
          schedulable = 1,
          cooldown_until = NULL,
          last_error_message = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
    `)
    .run(nowIso(), id)
  if (Number(result.changes ?? 0) > 0) {
    refreshGroupAccountStatsAfterWrite()
  }

  return listAccounts(access).find((account) => account.id === id)
}

export function markAccountCooldown(id: string, until: string, reason: string, status: AccountStatus = 'temporary_unavailable'): AccountSummary | undefined {
  const current = listAccounts().find((account) => account.id === id)
  if (!current) {
    return undefined
  }

  const expiredByPackage = isAccountExpired(current.accountExpiresAt)
  if (expiredByPackage) {
    const result = getDatabase()
      .prepare(`
        UPDATE accounts
        SET status = 'disabled',
            schedulable = 0,
            super_priority_enabled = 0,
            cooldown_until = NULL,
            last_error_message = ?,
            stream_failure_count = 0,
            stream_failure_window_started_at = NULL,
            updated_at = ?
        WHERE id = ?
      `)
      .run('账户套餐已过期，已自动停用', nowIso(), id)
    if (Number(result.changes ?? 0) > 0) {
      refreshGroupAccountStatsAfterWrite()
    }
    return listAccounts().find((account) => account.id === id)
  }

  const cooldownStatus: AccountStatus = status === 'rate_limited' ? 'rate_limited' : 'temporary_unavailable'

  const result = getDatabase()
    .prepare(`
      UPDATE accounts
      SET status = ?,
          schedulable = 1,
          super_priority_enabled = 0,
          cooldown_until = ?,
          last_error_message = ?,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
    `)
    .run(cooldownStatus, until, reason || null, nowIso(), id)
  if (Number(result.changes ?? 0) > 0) {
    refreshGroupAccountStatsAfterWrite()
  }

  return listAccounts().find((account) => account.id === id)
}

export function migrateAccountTraffic(input: {
  sourceAccountId: string
  targetAccountId: string
  sourceStatus: AccountTrafficMigrationSourceStatus
}, access?: AccessScope): { sourceAccount: AccountSummary; targetAccount: AccountSummary; sourceCooldownUntil?: string } | undefined {
  if (input.sourceAccountId === input.targetAccountId) {
    throw new Error('目标账户不能和当前账户相同')
  }

  const sourceRow = accountRowForManage(input.sourceAccountId, access)
  if (!sourceRow) {
    return undefined
  }
  const targetRow = accountRowForManage(input.targetAccountId, access)
  if (!targetRow) {
    return undefined
  }
  if (sourceRow.system_account_id !== targetRow.system_account_id) {
    throw new Error('目标账户必须和当前账户归属同一个系统账户')
  }
  if (sourceRow.provider_code !== targetRow.provider_code) {
    throw new Error('目标账户必须和当前账户属于同一个供应商')
  }
  const sourceGroupId = accountEnabledGroupId(sourceRow.id, sourceRow.system_account_id)
  const targetGroupId = accountEnabledGroupId(targetRow.id, targetRow.system_account_id)
  if (!sourceGroupId || !targetGroupId || sourceGroupId !== targetGroupId) {
    throw new Error('目标账户必须和当前账户在同一个分组内')
  }
  const targetCooldownUntil = targetRow.cooldown_until ?? undefined
  if (targetRow.status !== 'active' || targetRow.schedulable !== 1 || isAccountExpired(targetRow.account_expires_at) || isLaterIso(targetCooldownUntil, nowIso())) {
    throw new Error('目标账户当前不可调度，请选择正常可用的账户')
  }

  const now = nowIso()
  const reason = manualTrafficMigrationReason
  const sourceCooldownUntil = input.sourceStatus === 'temporary_unavailable'
    ? new Date(Date.now() + defaultTemporaryUnschedulableMinutes() * 60_000).toISOString()
    : null
  const database = getDatabase()
  database.exec('BEGIN')
  try {
    const updateResult = input.sourceStatus === 'disabled'
      ? database
        .prepare(`
          UPDATE accounts
          SET status = 'disabled',
              schedulable = 0,
              super_priority_enabled = 0,
              cooldown_until = NULL,
              last_error_message = ?,
              stream_failure_count = 0,
              stream_failure_window_started_at = NULL,
              updated_at = ?
          WHERE id = ? AND system_account_id = ?
        `)
        .run(reason, now, sourceRow.id, sourceRow.system_account_id)
      : database
        .prepare(`
          UPDATE accounts
          SET status = 'temporary_unavailable',
              super_priority_enabled = 0,
              cooldown_until = ?,
              last_error_message = ?,
              stream_failure_count = 0,
              stream_failure_window_started_at = NULL,
              updated_at = ?
          WHERE id = ? AND system_account_id = ?
        `)
        .run(sourceCooldownUntil, reason, now, sourceRow.id, sourceRow.system_account_id)
    if (Number(updateResult.changes ?? 0) <= 0) {
      database.exec('ROLLBACK')
      return undefined
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
  refreshGroupAccountStatsAfterWrite()

  const ownerAccess = { systemAccountId: sourceRow.system_account_id, role: 'user' as const }
  const sourceAccount = listAccounts(ownerAccess).find((account) => account.id === input.sourceAccountId)
  const targetAccount = listAccounts(ownerAccess).find((account) => account.id === input.targetAccountId)
  if (!sourceAccount || !targetAccount) {
    return undefined
  }
  return { sourceAccount, targetAccount, sourceCooldownUntil: sourceCooldownUntil ?? undefined }
}

export function markAccountDisabledByFailure(id: string, reason: string): AccountSummary | undefined {
  const current = listAccounts().find((account) => account.id === id)
  if (!current) {
    return undefined
  }

  const result = getDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'error',
          schedulable = 0,
          super_priority_enabled = 0,
          cooldown_until = NULL,
          last_error_message = ?,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
    `)
    .run(reason || null, nowIso(), id)
  if (Number(result.changes ?? 0) > 0) {
    refreshGroupAccountStatsAfterWrite()
  }

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
  refreshGroupAccountStatsAfterWrite()

  return { count, triggered: true, account: listAccounts().find((item) => item.id === input.accountId) }
}

export function listGroups(access?: AccessScope): GroupSummary[] {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const rows = listGroupRowsForAccess(access)
  const groupIds = rows.map((row) => row.id)
  const groupStatsByGroup = loadGroupAccountStatsByGroupIds(groupIds)
  const accountIdsByGroup = loadGroupAccountIdsByGroupIds(groupIds)
  const currentConcurrencyByAccount = loadAccountCurrentConcurrencyByIds([...accountIdsByGroup.values()].flat())
  const groupUsageScopes = rows.map((row) => usageScope(row.id, row.system_account_id, row.id))
  const groupAuthorizationScopes = rows
    .filter((row) => row.authorization_id)
    .map((row) => usageScope(row.authorization_id ?? '', row.system_account_id, row.authorization_id ?? ''))
  const todayUsageByGroup = loadGroupUsageSummariesForScopes(groupUsageScopes, todayDateKey())
  const totalUsageByGroup = loadGroupUsageSummariesForScopes(groupUsageScopes)
  const todayUsageByAuthorization = loadGroupAuthorizationUsageSummaries(groupAuthorizationScopes, todayDateKey())
  const totalUsageByAuthorization = loadGroupAuthorizationUsageSummaries(groupAuthorizationScopes)
  const sourcesByAuthorization = loadResourceAuthorizationSourcesByAuthorizationIds(rows.map((row) => row.authorization_id ?? ''))
  const accountNames = loadSystemAccountNameMap()
  return rows.map((row) => {
    const isAuthorizedView = row.access_type === 'authorized'
    const todayUsage = isAuthorizedView && row.authorization_id
      ? todayUsageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : todayUsageByGroup.get(row.id) ?? emptyAccountUsageSummary()
    const totalUsage = isAuthorizedView && row.authorization_id
      ? totalUsageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : totalUsageByGroup.get(row.id) ?? emptyAccountUsageSummary()
    const accountStats = groupAccountStatsFromRow(isAuthorizedView ? undefined : groupStatsByGroup.get(row.id), todayUsage, totalUsage)
    if (!isAuthorizedView) {
      accountStats.currentConcurrency = sumAccountCurrentConcurrency(accountIdsByGroup.get(row.id) ?? [], currentConcurrencyByAccount)
    }
    return {
      id: row.id,
      systemAccountId: includeSystemAccountFields(access) ? row.system_account_id : undefined,
      systemAccountName: includeSystemAccountFields(access) ? accountNames.get(row.system_account_id) : undefined,
      ownerSystemAccountId: row.system_account_id,
      ownerSystemAccountName: accountNames.get(row.system_account_id),
      name: row.name,
      providerCode: row.provider_code,
      description: isAuthorizedView ? undefined : row.description ?? undefined,
      enabled: row.enabled === 1,
      isDefault: isAuthorizedView ? false : row.is_default === 1,
      accountIds: isAuthorizedView ? [] : accountIdsByGroup.get(row.id) ?? [],
      accountStats,
      accessType: row.access_type ?? 'owner',
      groupAuthorizationId: row.authorization_id ?? undefined,
      authorizationStatus: row.authorization_status ?? undefined,
      authorizationSources: row.authorization_id ? sanitizeAuthorizationSourcesForViewer(sourcesByAuthorization.get(row.authorization_id) ?? [], isAuthorizedView) : undefined,
      permissions: isAuthorizedView && row.system_account_id !== viewerSystemAccountId ? authorizedPermissions() : ownerPermissions()
    }
  })
}

export function createGroup(input: Record<string, unknown>, access?: AccessScope): GroupSummary {
  const now = nowIso()
  const systemAccountId = writeSystemAccountId(access)
  const providerCode = String(input.providerCode ?? input.provider_code ?? 'openai')
  const group: GroupSummary = {
    id: newId('grp'),
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields(access) ? loadSystemAccountNameMap().get(systemAccountId) : undefined,
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

export function updateGroup(id: string, input: Record<string, unknown>, access?: AccessScope): GroupSummary | undefined {
  const current = listGroups(access).find((group) => group.id === id)
  if (!current) {
    return undefined
  }
  if (current.isDefault) {
    throw new DefaultGroupReadonlyError()
  }
  const systemAccountId = groupOwnerAndProvider(id)?.systemAccountId ?? currentSystemAccountId(access)
  if (!canManageResourceOwner(systemAccountId, access)) {
    return undefined
  }
  const hasDescriptionInput = Object.prototype.hasOwnProperty.call(input, 'description')
  const next: GroupSummary = {
    ...current,
    name: typeof input.name === 'string' ? input.name : current.name,
    providerCode: typeof input.providerCode === 'string' ? input.providerCode : typeof input.provider_code === 'string' ? input.provider_code : current.providerCode,
    description: hasDescriptionInput ? optionalNullableString(input.description) ?? undefined : current.description,
    enabled: typeof input.enabled === 'boolean' ? input.enabled : current.enabled
  }
  if (next.providerCode !== current.providerCode && current.accountStats.total > 0) {
    throw new Error('已有账户的分组不允许修改供应商')
  }
  const database = getDatabase()
  database
    .prepare('UPDATE groups SET name = ?, provider_code = ?, description = ?, enabled = ?, updated_at = ? WHERE id = ? AND system_account_id = ?')
    .run(next.name, next.providerCode, next.description ?? null, next.enabled ? 1 : 0, nowIso(), id, systemAccountId)
  return listGroups(access).find((group) => group.id === id)
}

export function deleteGroup(id: string, access?: AccessScope): boolean {
  const current = listGroups(access).find((group) => group.id === id)
  if (current?.isDefault) {
    throw new Error('默认分组不能删除')
  }
  const owner = groupOwnerAndProvider(id)
  if (!owner || !canManageResourceOwner(owner.systemAccountId, access)) {
    return false
  }
  const result = getDatabase().prepare('DELETE FROM groups WHERE id = ? AND system_account_id = ?').run(id, owner.systemAccountId)
  if (Number(result.changes ?? 0) > 0) {
    refreshGroupAccountStatsAfterWrite()
  }
  return result.changes > 0
}

export function setAccountGroup(accountId: string, groupId: string | null, access?: AccessScope): AccountSummary | undefined {
  const database = getDatabase()
  if (!groupId) {
    return undefined
  }
  const group = groupOwnerAndProvider(groupId)
  if (!group || !canManageResourceOwner(group.systemAccountId, access)) {
    return undefined
  }
  const current = listAccounts({ systemAccountId: group.systemAccountId, role: 'user' }).find((account) => account.id === accountId)
  if (!current) {
    return undefined
  }
  if (!canUseAccount(accountId, group.systemAccountId)) {
    return undefined
  }
  if (group.providerCode !== current.providerCode) {
    return undefined
  }
  const accountOwnerId = accountSystemAccountId(accountId)
  const accountAuthorization = accountOwnerId && accountOwnerId !== group.systemAccountId
    ? activeAccountAuthorization(accountId, group.systemAccountId)
    : undefined
  if (accountOwnerId !== group.systemAccountId && !accountAuthorization) {
    return undefined
  }

  database.prepare('DELETE FROM group_accounts WHERE account_id = ? AND system_account_id = ?').run(accountId, group.systemAccountId)
  const now = nowIso()
  database
    .prepare(`
      INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id, weight, enabled, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, 1, ?, ?)
      ON CONFLICT(group_id, account_id) DO UPDATE SET
        account_authorization_id = excluded.account_authorization_id,
        weight = excluded.weight,
        enabled = 1,
        updated_at = excluded.updated_at
    `)
    .run(group.systemAccountId, groupId, accountId, accountAuthorization?.id ?? null, 1, now, now)
  refreshGroupAccountStatsAfterWrite()

  return listAccounts({ systemAccountId: group.systemAccountId, role: 'user' }).find((account) => account.id === accountId)
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
  const accountOwnerId = accountSystemAccountId(accountId)
  const accountAuthorization = accountOwnerId && accountOwnerId !== current.systemAccountId
    ? activeAccountAuthorization(accountId, current.systemAccountId)
    : undefined
  if (accountOwnerId !== current.systemAccountId && !accountAuthorization) {
    return undefined
  }
  const now = nowIso()
  database.prepare('DELETE FROM group_accounts WHERE account_id = ? AND system_account_id = ?').run(accountId, current.systemAccountId)
  database
    .prepare(`
      INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id, weight, enabled, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, 1, ?, ?)
      ON CONFLICT(group_id, account_id) DO UPDATE SET account_authorization_id = excluded.account_authorization_id, enabled = 1, updated_at = excluded.updated_at
    `)
    .run(current.systemAccountId, groupId, accountId, accountAuthorization?.id ?? null, weight, now, now)
  refreshGroupAccountStatsAfterWrite()
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
          AND system_team_members.status = 'active'
        ORDER BY system_teams.status ASC, system_teams.updated_at DESC, system_teams.name ASC
      `)
      .all(scopedId) as unknown as SystemTeamRow[]
    : getDatabase().prepare('SELECT * FROM system_teams ORDER BY status ASC, updated_at DESC, name ASC').all() as unknown as SystemTeamRow[]
  const members = listSystemTeamMembersForTeamIds(rows.map((row) => row.id), true)
  return rows.map((row) => systemTeamSummaryFromRow(row, members.get(row.id) ?? [], access))
}

export function listAuthorizationGranteeAccounts(access?: AccessScope): SystemAccountPrincipalSummary[] {
  const database = getDatabase()
  if (canAccessAll(access)) {
    const rows = database.prepare('SELECT * FROM system_accounts ORDER BY status ASC, display_name ASC, username ASC').all() as unknown as SystemAccountRow[]
    return rows.map(systemAccountPrincipalSummaryFromRow)
  }
  const viewerId = currentSystemAccountId(access)
  const rows = database
    .prepare(`
      SELECT DISTINCT system_accounts.*
      FROM system_accounts
      WHERE system_accounts.id = ?
         OR system_accounts.id IN (
           SELECT active_members.system_account_id
           FROM system_team_members viewer_members
           INNER JOIN system_team_members active_members ON active_members.team_id = viewer_members.team_id
           INNER JOIN system_teams ON system_teams.id = viewer_members.team_id
           WHERE viewer_members.system_account_id = ?
             AND viewer_members.status = 'active'
             AND active_members.status = 'active'
             AND system_teams.status = 'active'
         )
      ORDER BY system_accounts.status ASC, system_accounts.display_name ASC, system_accounts.username ASC
    `)
    .all(viewerId, viewerId) as unknown as SystemAccountRow[]
  return rows.map(systemAccountPrincipalSummaryFromRow)
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
  if (!created) throw new Error('创建团队失败')
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
  let authorizationChanged = false
  database.exec('BEGIN')
  try {
    database
      .prepare('UPDATE system_teams SET name = ?, description = ?, status = ?, updated_at = ? WHERE id = ?')
      .run(name, input.description === undefined ? row.description : optionalNullableString(input.description), status, now, id)
    if (row.status !== 'disabled' && status === 'disabled') {
      revokeAllTeamSources(id, currentSystemAccountId(access), database, now, 'team_disabled')
      authorizationChanged = true
    }
    if (row.status === 'disabled' && status === 'active') {
      reactivateTeamGrantSources(id, access, database, now)
      authorizationChanged = true
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
  if (authorizationChanged) {
    refreshGroupAccountStatsAfterWrite()
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
  refreshGroupAccountStatsAfterWrite()
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
  refreshGroupAccountStatsAfterWrite()
  return listSystemTeams(access).find((item) => item.id === teamId)
}

export function listResourceAuthorizations(filters: Record<string, unknown> = {}, access?: AccessScope): ResourceAuthorizationSummary[] {
  expireDueResourceAuthorizations()
  const clauses: string[] = []
  const params: Array<string | number | null> = []
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
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
    if (!canAccessAll(access)) {
      clauses.push('EXISTS (SELECT 1 FROM system_team_members stm WHERE stm.team_id = ? AND stm.system_account_id = ? AND stm.status = \'active\')')
      params.push(teamId, currentSystemAccountId(access))
    }
    clauses.push("EXISTS (SELECT 1 FROM resource_authorization_sources ras WHERE ras.authorization_id = ra.id AND ras.source_type = 'team' AND ras.source_team_id = ? AND ras.status = 'active')")
    params.push(teamId)
  }
  const ownerSystemAccountId = optionalString(filters.resourceOwnerSystemAccountId ?? filters.resource_owner_system_account_id)
  if (ownerSystemAccountId) {
    clauses.push('ra.resource_owner_system_account_id = ?')
    params.push(ownerSystemAccountId)
  }
  const scopeSystemAccountId = scopedSystemAccountId(access)
  if (scopeSystemAccountId) {
    clauses.push('(ra.resource_owner_system_account_id = ? OR ra.grantee_system_account_id = ?)')
    params.push(scopeSystemAccountId, scopeSystemAccountId)
  } else if (!canAccessAll(access)) {
    clauses.push('(ra.resource_owner_system_account_id = ? OR ra.grantee_system_account_id = ?)')
    params.push(currentSystemAccountId(access), currentSystemAccountId(access))
  }
  const direction = authorizationDirectionFilter(filters.direction)
  const directionSystemAccountId = scopeSystemAccountId ?? (!canAccessAll(access) ? currentSystemAccountId(access) : undefined)
  if (direction && directionSystemAccountId) {
    clauses.push(direction === 'outbound' ? 'ra.resource_owner_system_account_id = ?' : 'ra.grantee_system_account_id = ?')
    params.push(directionSystemAccountId)
  }
  const where = clauses.length ? `WHERE ${clauses.join(' AND ')}` : ''
  const rows = getDatabase().prepare(`SELECT ra.* FROM resource_authorizations ra ${where} ORDER BY CASE ra.status WHEN 'active' THEN 0 WHEN 'paused' THEN 1 WHEN 'expired' THEN 2 WHEN 'revoked' THEN 3 ELSE 4 END, ra.updated_at DESC, ra.created_at DESC`).all(...params) as unknown as ResourceAuthorizationRow[]
  return resourceAuthorizationSummaries(rows).map((summary) => withResourceAuthorizationPermissions(
    sanitizeResourceAuthorizationSummaryForAccess(summary, access),
    viewerSystemAccountId,
    access
  ))
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
  refreshGroupAccountStatsAfterWrite()
  const ids = [...new Set(createdIds)]
  const created = ids.length ? listResourceAuthorizations({ status: 'all' }, access).find((item) => item.id === ids[0]) : undefined
  if (created) return created
  const fallback = listResourceAuthorizations({ resourceType, resourceId, teamId: granteeType === 'team' ? granteeId : undefined, status: 'all' }, access)[0]
  if (!fallback) throw new Error('创建资源授权失败')
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
      cleanupInactiveAuthorizationBindings(database)
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
  refreshGroupAccountStatsAfterWrite()
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
  const hasLimitsInput = Object.prototype.hasOwnProperty.call(input, 'limits')
  const nextExpiresAt = hasExpiresAtInput
    ? optionalNullableServerDateTimeIso(input.expiresAt ?? input.expires_at)
    : row.expires_at
  const nextLimits = hasLimitsInput
    ? requestQuotaLimitsJson(normalizeRequestQuotaLimits(input.limits))
    : row.limits_json
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
          limits_json = ?,
          updated_at = ?
      WHERE id = ?
    `)
    .run(nextStatus, nextExpiresAt, nextRevokedBy, nextRevokedAt, nextRevokedReason, nextLimits, now, authorizationId)
  updateEffectiveTeamGrantLimits(row, nextLimits, database, now)
  cleanupInactiveAuthorizationBindings(database)
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

function systemAccountPrincipalSummaryFromRow(row: SystemAccountRow): SystemAccountPrincipalSummary {
  return {
    id: row.id,
    username: row.username,
    displayName: row.display_name,
    status: row.status
  }
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

function authorizationDirectionFilter(value: unknown): 'outbound' | 'inbound' | undefined {
  return value === 'outbound' || value === 'inbound' ? value : undefined
}

function resourceOwnerSystemAccountId(resourceType: ResourceAuthorizationResourceType, resourceId: string): string | undefined {
  return resourceType === 'account' ? accountSystemAccountId(resourceId) : groupOwnerAndProvider(resourceId)?.systemAccountId
}

function activeTeamMemberRows(teamId: string, database = getDatabase()): SystemTeamMemberRow[] {
  return database.prepare(`
    SELECT system_team_members.*
    FROM system_team_members
    INNER JOIN system_accounts ON system_accounts.id = system_team_members.system_account_id
    WHERE system_team_members.team_id = ?
      AND system_team_members.status = 'active'
      AND system_accounts.status = 'active'
    ORDER BY system_team_members.joined_at ASC
  `).all(teamId) as unknown as SystemTeamMemberRow[]
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
  const nextLimitsJson = !isTeamSource && hasActiveTeamSource
    ? existing?.limits_json ?? null
    : requestQuotaLimitsJson(normalizeRequestQuotaLimits(input.limits))
  const nextRevokedBy = nextStatus === 'expired' ? existing?.revoked_by ?? input.actor : null
  const nextRevokedAt = nextStatus === 'expired' ? existing?.revoked_at ?? input.now : null
  const nextRevokedReason = nextStatus === 'expired' ? 'authorization_expired' : null
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
          limits_json = ?,
          model_policy_json = COALESCE(?, model_policy_json),
          revoked_by = ?,
          revoked_at = ?,
          revoked_reason = ?,
          updated_at = ?
      WHERE id = ?
    `).run(input.ownerSystemAccountId, nextStatus, nextEffectiveSourceType, nextEffectiveSourceTeamId, input.now, input.now, input.remark ?? null, input.expiresAt ?? null, nextLimitsJson, jsonObjectOrNull(input.modelPolicy), nextRevokedBy, nextRevokedAt, nextRevokedReason, input.now, authorizationId)
  } else {
    input.database.prepare(`
      INSERT INTO resource_authorizations (
        id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status,
        effective_source_type, effective_source_team_id, activated_at, last_source_changed_at,
        remark, expires_at, limits_json, model_policy_json,
        created_by, created_at, revoked_by, revoked_at, revoked_reason, updated_at
      ) VALUES (?, ?, ?, ?, ?, 'use', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `).run(authorizationId, input.resourceType, input.resourceId, input.ownerSystemAccountId, input.granteeSystemAccountId, nextStatus, nextEffectiveSourceType, nextEffectiveSourceTeamId, input.now, input.now, input.remark ?? null, nextExpiresAt, nextLimitsJson, jsonObjectOrNull(input.modelPolicy), input.actor, input.now, nextRevokedBy, nextRevokedAt, nextRevokedReason, input.now)
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
  if (!row) throw new Error('创建资源授权失败')
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
    cleanupInactiveAuthorizationBindings(database, [authorizationId])
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
  cleanupInactiveAuthorizationBindings(database, [authorizationId])
}

function cleanupInactiveAuthorizationBindings(database = getDatabase(), authorizationIds?: string[]): void {
  void database
  void authorizationIds
  clearGatewayApiKeyValidationCache()
  refreshGroupAccountStatsAfterWrite()
}

function upsertTeamResourceGrant(input: { resourceType: ResourceAuthorizationResourceType; resourceId: string; ownerSystemAccountId: string; teamId: string; remark?: string; expiresAt?: string | null; limits?: unknown; modelPolicy?: unknown; actor: string; now: string; database: DatabaseSync }): TeamResourceAuthorizationGrantRow {
  const existing = input.database.prepare("SELECT * FROM team_resource_authorization_grants WHERE resource_type = ? AND resource_id = ? AND team_id = ? AND status = 'active' LIMIT 1").get(input.resourceType, input.resourceId, input.teamId) as unknown as TeamResourceAuthorizationGrantRow | undefined
  const id = existing?.id ?? newId('teamgrant')
  if (existing) {
    input.database.prepare('UPDATE team_resource_authorization_grants SET remark = COALESCE(?, remark), expires_at = COALESCE(?, expires_at), limits_json = ?, model_policy_json = COALESCE(?, model_policy_json), updated_at = ? WHERE id = ?')
      .run(input.remark ?? null, input.expiresAt ?? null, requestQuotaLimitsJson(normalizeRequestQuotaLimits(input.limits)), jsonObjectOrNull(input.modelPolicy), input.now, id)
  } else {
    input.database.prepare("INSERT INTO team_resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id, team_id, scope, status, remark, expires_at, limits_json, model_policy_json, created_by, created_at, revoked_by, revoked_at, updated_at) VALUES (?, ?, ?, ?, ?, 'use', 'active', ?, ?, ?, ?, ?, ?, NULL, NULL, ?)")
      .run(id, input.resourceType, input.resourceId, input.ownerSystemAccountId, input.teamId, input.remark ?? null, input.expiresAt ?? null, requestQuotaLimitsJson(normalizeRequestQuotaLimits(input.limits)), jsonObjectOrNull(input.modelPolicy), input.actor, input.now, input.now)
  }
  const row = input.database.prepare('SELECT * FROM team_resource_authorization_grants WHERE id = ?').get(id) as unknown as TeamResourceAuthorizationGrantRow | undefined
  if (!row) throw new Error('创建团队授权失败')
  return row
}

function updateEffectiveTeamGrantLimits(row: ResourceAuthorizationRow, limitsJson: string | null, database: DatabaseSync, now: string): void {
  if (row.effective_source_type !== 'team' || !row.effective_source_team_id) {
    return
  }
  database.prepare(`
    UPDATE team_resource_authorization_grants
    SET limits_json = ?,
        updated_at = ?
    WHERE resource_type = ?
      AND resource_id = ?
      AND team_id = ?
      AND status = 'active'
  `).run(limitsJson, now, row.resource_type, row.resource_id, row.effective_source_team_id)
  database.prepare(`
    UPDATE resource_authorizations
    SET limits_json = ?,
        updated_at = ?
    WHERE resource_type = ?
      AND resource_id = ?
      AND effective_source_type = 'team'
      AND effective_source_team_id = ?
      AND status IN ('active', 'paused')
  `).run(limitsJson, now, row.resource_type, row.resource_id, row.effective_source_team_id)
}

function applyActiveTeamGrantsToMember(teamId: string, systemAccountId: string, access: AccessScope | undefined, database: DatabaseSync, now: string): void {
  const grants = database.prepare("SELECT * FROM team_resource_authorization_grants WHERE team_id = ? AND status = 'active'").all(teamId) as unknown as TeamResourceAuthorizationGrantRow[]
  const actor = currentSystemAccountId(access)
  for (const grant of grants) {
    if (grant.resource_owner_system_account_id === systemAccountId) continue
    upsertResourceAuthorizationForUser({ resourceType: grant.resource_type, resourceId: grant.resource_id, ownerSystemAccountId: grant.resource_owner_system_account_id, granteeSystemAccountId: systemAccountId, sourceType: 'team', sourceTeamId: teamId, remark: grant.remark ?? undefined, expiresAt: grant.expires_at, limits: parseRequestQuotaLimitsJson(grant.limits_json), modelPolicy: parseOptionalJsonObject(grant.model_policy_json ?? undefined), actor, now, database })
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
  const accountNames = loadAccountNameMap(rows.filter((row) => row.resource_type === 'account').map((row) => row.resource_id))
  const groupNames = loadGroupNameMap(rows.filter((row) => row.resource_type === 'group').map((row) => row.resource_id))
  const systemAccounts = loadSystemAccountsByIds(rows.flatMap((row) => [row.resource_owner_system_account_id, row.grantee_system_account_id]))
  const teamNames = loadSystemTeamNameMap(rows.map((row) => row.effective_source_team_id ?? ''))
  const sources = loadResourceAuthorizationSourcesByAuthorizationIds(rows.map((row) => row.id))
  const usage = loadResourceAuthorizationUsageSummaries(rows, todayDateKey())
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
      limits: parseRequestQuotaLimitsJson(row.limits_json),
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

function sanitizeResourceAuthorizationSummaryForAccess(summary: ResourceAuthorizationSummary, access?: AccessScope): ResourceAuthorizationSummary {
  if (canManageResourceOwner(summary.resourceOwnerSystemAccountId, access)) {
    return summary
  }
  const sources = sanitizeAuthorizationSourcesForViewer(summary.authorizationSources ?? summary.sources, true) ?? []
  return {
    ...summary,
    effectiveSourceTeamId: undefined,
    effectiveSourceTeamName: undefined,
    sources,
    authorizationSources: sources,
    createdBy: '',
    revokedBy: undefined
  }
}

function withResourceAuthorizationPermissions(summary: ResourceAuthorizationSummary, _viewerSystemAccountId: string | undefined, access?: AccessScope): ResourceAuthorizationSummary {
  const canManage = canManageResourceOwner(summary.resourceOwnerSystemAccountId, access)
  return {
    ...summary,
    permissions: {
      canEdit: canManage,
      canAuthorize: canManage
    }
  }
}

function loadResourceAuthorizationUsageSummaries(rows: ResourceAuthorizationRow[], statDate?: string): Map<string, AccountUsageSummary> {
  const accountScopes = rows
    .filter((row) => row.resource_type === 'account')
    .map((row) => usageScope(row.id, row.resource_owner_system_account_id, row.id))
  const groupScopes = rows
    .filter((row) => row.resource_type === 'group')
    .map((row) => usageScope(row.id, row.resource_owner_system_account_id, row.id))
  return new Map([
    ...loadAccountAuthorizationUsageSummaries(accountScopes, statDate),
    ...loadGroupAuthorizationUsageSummaries(groupScopes, statDate)
  ])
}

function loadResourceAuthorizationUsageDetails(authorization: ResourceAuthorizationSummary): ResourceAuthorizationUsageDetail[] {
  const scopeType = authorization.resourceType === 'account' ? 'account_authorization' : 'group_authorization'
  const rows = getDatabase().prepare(`
    SELECT
      ra.grantee_system_account_id AS system_account_id,
      COALESCE(stats.request_count, 0) AS request_count,
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
    WHERE ra.id = ?
  `).all(scopeType, todayDateKey(), authorization.id) as unknown as Array<AccountUsageAggregateRow & { system_account_id: string }>

  const systemAccounts = loadSystemAccountsByIds(rows.map((row) => row.system_account_id))
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
