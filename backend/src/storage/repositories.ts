import type { DatabaseSync } from 'node:sqlite'

import type { AccountGroupBindStatus, AccountGroupOptionSummary, AccountOptionSummary, AccountStatus, AccountSummary, AccountTrafficMigrationSourceStatus, AccountUsageStatsOverview, AccountUsageStatsRange, AccountUsageSummary, AuthorizationStatus, GroupListResult, GroupOptionSummary, GroupSummary, ResourceAuthorizationListResult, ResourceAuthorizationResourceType, ResourceAuthorizationSourceStatus, ResourceAuthorizationSourceType, ResourceAuthorizationSummary, ResourceAuthorizationUsageDetail, ResourcePermissions, SystemAccountPrincipalSummary, SystemTeamMemberSummary, SystemTeamPrincipalSummary, SystemTeamSummary } from '../domain/types.js'
import { loadAccountCurrentConcurrencyByIds, sumAccountCurrentConcurrency } from '../shared/account-concurrency.js'
import { buildSystemAccountScopeClause, canAccessAll, currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, scopedSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { normalizeAccountListOptions, normalizeAccountOptionListOptions, type AccountListOptions } from './account-list-options.js'
import { accountCredentialsForList, findAccountRowForAccess, hydrateAccountRowsFromRecordDatabase, listAccountRowsForAccess, listAccountRowsPageForAccess, loadAccountAuthorizationUsageSummaries } from './account-read.repository.js'
import {
  getAccountUsageStatsOverview as buildAccountUsageStatsOverview,
  getAccountUsageStatsOverviewPageFromWindows as buildAccountUsageStatsOverviewPageFromWindows
} from './account-usage.repository.js'
import { updateAccountUsageSnapshotRefreshState, upsertAccountUsageSnapshot } from './account-usage-snapshot.repository.js'
import { createApiKeyRecord, deleteApiKey, findApiKeySummary, listApiKeys, listApiKeysPage, updateApiKey } from './api-key.repository.js'
import { loadResourceAuthorizationSourcesByAuthorizationIds, loadResourceAuthorizationStatsByResourceIds } from './authorization-read-loaders.js'
import { decryptJson, encryptJson, hashSecret, maskSecret } from './crypto.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatabase, getRecordDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { defaultGroupIdForSystemAccount } from './default-group.repository.js'
import { listErrorPolicies } from './error-policy.repository.js'
import { emptyGroupAccountStats, groupAccountStatsFromRow } from './group-account-stats.mapper.js'
import { findGroupRowForAccess, listGroupRowsForAccess, listGroupRowsPageForAccess, loadGroupAuthorizationUsageSummaries, type GroupListOptions } from './group-read.repository.js'
import { loadGroupAccountIdsByGroupIds, loadGroupAccountStatsByGroupIds } from './group-read-loaders.js'
import { loadOpenAICodexUsageSnapshotsByAccountIds } from './oauth-usage-loaders.js'
import { listProviders, providerPassthroughEnabled } from './provider.repository.js'
import { resolveEnabledProxyProfileId } from './proxy.repository.js'
import { chunkValues, compatiblePagedTotal, sqlPlaceholders, takePageRows } from './query-utils.js'
import {
  accountSystemAccountId,
  activeAccountAuthorization,
  activeGroupAuthorization,
  activeResourceAuthorization,
  canManageResourceOwner,
  groupOwnerAndProvider,
  isResourceAuthorizationExpired,
  resolveAccountSystemAccountId,
  sanitizeAuthorizationSourcesForViewer,
  usageScope
} from './resource-authorization-helpers.js'
import {
  normalizeResourceType
} from './resource-authorization-list-helpers.js'
import { findResourceAuthorizationSummary, listResourceAuthorizationSummaries, listResourceAuthorizationSummariesPage, type ResourceAuthorizationListOptions } from './resource-authorization-read.repository.js'
import {
  activeTeamMemberRows,
  applyActiveTeamGrantsToMember,
  cleanupInactiveAuthorizationBindings,
  deactivateAuthorizationIfNoActiveSources,
  expireDueResourceAuthorizations,
  reactivateTeamGrantSources,
  revokeAllTeamSources,
  revokeResourceAuthorizationGrant,
  revokeTeamSourcesForMember,
  syncResourceAuthorizationGrantRuntime,
  upsertResourceAuthorizationForUser,
  upsertResourceAuthorizationGrant
} from './resource-authorization-write-state.repository.js'
import { loadSystemAccountNameMap, loadSystemAccountsByIds } from './repository-lookups.js'
import { normalizeRequestQuotaLimits, parseRequestQuotaLimitsJson, requestQuotaLimitsJson } from './request-quota-limits.js'
import type { AccountFailureRow, AccountListRow, AccountRow, GroupListRow, ResourceAuthorizationGrantRow, ResourceAuthorizationRow, ResourceAuthorizationSourceRow, SystemTeamMemberRow, SystemTeamRow } from './repository-row-types.js'
import { getSettings } from './settings.repository.js'
import { systemAccountPrincipalSummaryFromRow } from './system-account-mappers.js'
import type { SystemAccountRow } from './system-account-mappers.js'
import { findSystemAccountById } from './system-accounts.repository.js'
import { refreshGroupAccountStatsCache } from './usage-stats.repository.js'
import { GLOBAL_STATS_SYSTEM_ACCOUNT_ID } from './usage-stats-types.js'
import { emptyAccountUsageSummary, normalizeAccountUsageStatsRange, todayDateKey, usageStatsTimezone } from './usage-stats-helpers.js'
import { loadAccountUsageSummariesForScopes, loadGroupUsageSummariesForScopes, loadUsageRangeSummaryForScope, type UsageSummaryScopeRequest } from './usage-summary-loaders.js'
import { loadUsageDailySeriesForScopeRequests } from './usage-window-loaders.js'
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
const defaultResourceAuthorizationUsageDetailPageSize = 200

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

export interface ResourceAuthorizationUsageOptions {
  range?: AccountUsageStatsRange
  page?: number
  pageSize?: number
}

export type { AccountUsageSummary, SystemAccountPrincipalSummary, SystemAccountRole, SystemAccountStatus, SystemAccountSummary } from '../domain/types.js'
export {
  createAnnouncement,
  deleteAnnouncement,
  findAnnouncement,
  listAnnouncements,
  listPublicAnnouncements,
  markPublicAnnouncementsRead,
  publishAnnouncement,
  unpublishAnnouncement,
  updateAnnouncement,
  type AnnouncementReadResult,
  type AnnouncementInput
} from './announcements.repository.js'
export {
  cleanupDeletedApiKeyRelatedRecordData,
  type DeletedApiKeyRecordCleanupTarget
} from './api-key-record-cleanup.js'
export {
  createApiKeyRecord,
  deleteApiKey,
  deleteApiKeyWithRelatedCleanup,
  findApiKeySummary,
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
  listSystemAccountOptions,
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
  findProxy,
  getProxyTestConfig,
  listEnabledProxyTestConfigs,
  listProxyOptions,
  listProxies,
  ProxyInUseError,
  ProxyProfileUnavailableError,
  resolveEnabledProxyProfileId,
  resolveProxyUrlsForProfiles,
  resolveProxyUrlForProfile,
  resolveProxyUrlForProfileForSystemAccount,
  updateProxyTestState,
  updateProxy,
  type ProxyProfileUrlResolution,
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
  expireDueResourceAuthorizations
} from './resource-authorization-write-state.repository.js'
export {
  type AccountUsageSnapshotUpsertInput,
  updateAccountUsageSnapshotRefreshState,
  upsertAccountUsageSnapshot,
  upsertAccountUsageSnapshots
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
  cleanupAuditLogsByRetention,
  cleanupAuditLogsBefore,
  cleanupUnreferencedAuditPayloadBlobs,
  createAuditLogsBatch,
  getAuditLogDetail,
  getAuditLogPayload,
  listAuditErrorGroupEvents,
  listAuditErrorGroups,
  listAuditLogs,
  type AuditErrorGroupListOptions,
  type AuditErrorGroupListResult,
  type AuditErrorGroupSummary,
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
  cleanupOperationLogsBefore,
  createOperationLog,
  createOperationLogsBatch,
  getOperationLogDetail,
  getOperationLogDetailForViewer,
  listOperationLogs,
  listOperationLogsForViewer,
  type OperationLogChange,
  type OperationLogDetail,
  type OperationLogDetailLevel,
  type OperationLogInput,
  type OperationLogListOptions,
  type OperationLogListResult,
  type OperationLogMode,
  type OperationLogSummary,
  type OperationLogTargetInput,
  type OperationLogTargetRelation,
  type OperationLogTargetSummary,
  type OperationLogViewerInput,
  type OperationLogViewerSummary,
  type OperationLogVisibilityReason,
  type OperationLogVisibilityScope
} from './operation-logs.repository.js'
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
  cleanupProcessedUsageRecordsBeforeWithResult,
  cleanupSystemMetricsBefore,
  cleanupUsageStatsBucketsBefore,
  type ProcessedUsageRecordsCleanupBatchResult,
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
export { resolveAccountSystemAccountId } from './resource-authorization-helpers.js'

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
      ORDER BY updated_at DESC, group_id ASC, account_id ASC
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
      ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC, group_accounts.account_id ASC
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
  const activeAuthorizationId = accountOwnerId && systemAccountId && accountOwnerId !== systemAccountId
    ? row.authorization_id ?? undefined
    : undefined
  return {
    groupId: row.bound_group_id,
    groupName: row.bound_group_name ?? row.bound_group_id,
    groupBindStatus: row.bound_group_account_authorization_id && activeAuthorizationId !== row.bound_group_account_authorization_id ? 'authorization_unavailable' : 'bound'
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

function globalProxyProfileId(proxyProfileId: string | undefined): string | undefined {
  return resolveEnabledProxyProfileId(proxyProfileId)
}

function isAccountExpired(accountExpiresAt: string | null | undefined, now = Date.now()): boolean {
  if (!accountExpiresAt) return false
  const timestamp = Date.parse(accountExpiresAt)
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
          cooldown_until = NULL,
          last_error_code = NULL,
          last_error_message = ?,
          updated_at = ?
      WHERE account_expires_at IS NOT NULL
        AND account_expires_at <= ?
        AND (
          status <> 'disabled'
          OR schedulable <> 0
          OR cooldown_until IS NOT NULL
          OR last_error_code IS NOT NULL
          OR last_error_message IS NULL
        )${scope.clause}
    `)
    .run('账户套餐已过期，已自动停用', now, now, ...scope.params)
  if (Number(result.changes ?? 0) > 0) {
    refreshGroupAccountStatsAfterWrite()
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

function isHardUnavailableAccountStatus(status: AccountStatus): boolean {
  return status === 'disabled' || status === 'error'
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

function normalizeFallbackInput(value: unknown, fallback: boolean): boolean {
  return normalizeSuperPriorityInput(value, fallback)
}

function normalizedGroupAccountLocalStatus(value: unknown): AccountStatus | undefined {
  return normalizeAccountStatus(value, 'active')
}

function authorizedAccountEffectiveStatus(ownerStatus: AccountStatus, localStatus: AccountStatus | undefined, localCooldownUntil: string | null | undefined): AccountStatus {
  if (ownerStatus !== 'active') {
    return ownerStatus
  }
  if (localStatus === 'temporary_unavailable' && localCooldownUntil && !isLaterIso(localCooldownUntil, nowIso())) {
    return 'active'
  }
  return localStatus ?? 'active'
}

function authorizedBindingSystemAccountId(access?: AccessScope): string {
  return scopedSystemAccountId(access) ?? currentSystemAccountId(access)
}

function isLaterIso(value?: string, current?: string): boolean {
  if (!value) return false
  if (!current) return true
  const nextTime = Date.parse(value)
  const currentTime = Date.parse(current)
  return Number.isFinite(nextTime) && (!Number.isFinite(currentTime) || nextTime > currentTime)
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

function normalizedEntityName(value: unknown, fallback: string): string {
  return typeof value === 'string' && value.trim() ? value.trim() : fallback
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

function isDuplicateAccountNameError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  return error.message.includes('idx_accounts_owner_provider_name_unique_lower')
}

function assertAccountNameAvailable(systemAccountId: string, providerCode: string, name: string, excludeId?: string): void {
  const params: string[] = [systemAccountId, providerCode, name]
  const excludeClause = excludeId ? ' AND id <> ?' : ''
  if (excludeId) {
    params.push(excludeId)
  }
  const row = getDatabase()
    .prepare(`SELECT id FROM accounts WHERE system_account_id = ? AND provider_code = ? AND lower(name) = lower(?)${excludeClause} LIMIT 1`)
    .get(...params) as { id?: string } | undefined
  if (row?.id) {
    throw new Error(`同一供应商下账户名称已存在：${name}`)
  }
}

function isDuplicateGroupNameError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  return error.message.includes('idx_groups_owner_provider_name_unique_lower')
}

function assertGroupNameAvailable(systemAccountId: string, providerCode: string, name: string, excludeId?: string): void {
  const params: string[] = [systemAccountId, providerCode, name]
  const excludeClause = excludeId ? ' AND id <> ?' : ''
  if (excludeId) {
    params.push(excludeId)
  }
  const row = getDatabase()
    .prepare(`SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? AND lower(name) = lower(?)${excludeClause} LIMIT 1`)
    .get(...params) as { id?: string } | undefined
  if (row?.id) {
    throw new Error(`同一供应商下分组名称已存在：${name}`)
  }
}

function defaultTemporaryUnschedulableMinutes(): number {
  const value = getSettings().defaultTemporaryUnschedulableMinutes
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  if (!Number.isFinite(number)) return 5
  return Math.min(Math.max(Math.trunc(number), 1), 1440)
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
  hasMore: boolean
  page: number
  pageSize: number
}

interface AccountSummaryBuildOptions {
  includeCredentials?: boolean
}

export function listAccounts(access?: AccessScope, options?: AccountListOptions): AccountSummary[] {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  disableExpiredAccounts(access)
  const listOptions = normalizeAccountListOptions(options)
  const rows = hydrateAccountRowsFromRecordDatabase(listAccountRowsForAccess(access, listOptions))
  return accountSummariesFromRows(rows, access, viewerSystemAccountId)
}

export function listAccountsPage(access?: AccessScope, options?: AccountListOptions): AccountListResult {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  disableExpiredAccounts(access)
  const listOptions = normalizeAccountListOptions(options)
  const databasePage = listAccountRowsPageForAccess(access, listOptions, { includeCredentials: false })
  const page = {
    rows: hydrateAccountRowsFromRecordDatabase(databasePage.rows),
    total: databasePage.total
  }
  const rows = page.rows
  return {
    items: accountSummariesFromRows(rows, access, viewerSystemAccountId, { includeCredentials: false }),
    total: page.total,
    hasMore: page.total > listOptions.page * listOptions.pageSize,
    page: listOptions.page,
    pageSize: listOptions.pageSize
  }
}

export function listAccountOptions(access?: AccessScope, options?: AccountListOptions): AccountOptionSummary[] {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  disableExpiredAccounts(access)
  const listOptions = normalizeAccountOptionListOptions(options)
  const rows = listAccountRowsPageForAccess(access, listOptions, { includeCredentials: false, includeTotal: false }).rows
  return accountOptionSummariesFromRows(rows, access, viewerSystemAccountId)
}

export function findAccountSummary(accountId: string, access?: AccessScope): AccountSummary | undefined {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  disableExpiredAccounts(access)
  const listOptions = normalizeAccountListOptions({ page: 1, pageSize: 1 })
  const row = findAccountRowForAccess(access, accountId, listOptions)
  if (!row) return undefined
  const hydratedRows = hydrateAccountRowsFromRecordDatabase([row])
  return accountSummariesFromRows(hydratedRows, access, viewerSystemAccountId)[0]
}

function accountOptionSummariesFromRows(rows: AccountListRow[], access: AccessScope | undefined, viewerSystemAccountId: string | undefined): AccountOptionSummary[] {
  const hasAuthorizedRows = rows.some((row) => row.access_type === 'authorized')
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const accountNames = shouldIncludeSystemAccountFields || hasAuthorizedRows ? loadSystemAccountNameMap() : new Map<string, string>()
  return rows.map((row) => {
    const isAuthorizedView = row.access_type === 'authorized'
    return {
      id: row.id,
      systemAccountId: shouldIncludeSystemAccountFields ? row.system_account_id : undefined,
      systemAccountName: shouldIncludeSystemAccountFields ? accountNames.get(row.system_account_id) : undefined,
      ownerSystemAccountId: row.system_account_id,
      ownerSystemAccountName: accountNames.get(row.system_account_id),
      providerCode: row.provider_code,
      name: row.name,
      type: row.type,
      status: row.status,
      accessType: row.access_type ?? 'owner',
      accountAuthorizationId: row.authorization_id ?? undefined,
      authorizationStatus: row.authorization_status ?? undefined,
      permissions: isAuthorizedView && row.system_account_id !== viewerSystemAccountId ? authorizedPermissions() : ownerPermissions()
    }
  })
}

function accountSummariesFromRows(
  rows: AccountListRow[],
  access: AccessScope | undefined,
  viewerSystemAccountId: string | undefined,
  options: AccountSummaryBuildOptions = {}
): AccountSummary[] {
  const includeCredentials = options.includeCredentials ?? true
  const timezone = usageStatsTimezone()
  const accountIds = rows.map((row) => row.id)
  const currentConcurrencyByAccount = loadAccountCurrentConcurrencyByIds(accountIds)
  const accountUsageScopes = rows.map((row) => usageScope(row.id, row.system_account_id, row.id))
  const usageByAccount = loadAccountUsageSummariesForScopes(accountUsageScopes)
  const todayUsageByAccount = loadAccountUsageSummariesForScopes(accountUsageScopes, todayDateKey(timezone))
  const authorizationStatsByAccount = loadResourceAuthorizationStatsByResourceIds('account', accountIds)
  const authorizationScopes = rows
    .filter((row) => row.authorization_id)
    .map((row) => usageScope(row.authorization_id ?? '', row.system_account_id, row.authorization_id ?? ''))
  const usageByAuthorization = loadAccountAuthorizationUsageSummaries(authorizationScopes)
  const todayUsageByAuthorization = loadAccountAuthorizationUsageSummaries(authorizationScopes, todayDateKey(timezone))
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
    const authorizedLocalStatus = normalizedGroupAccountLocalStatus(row.bound_group_local_status)
    const effectiveAuthorizedStatus = isAuthorizedView && groupBinding
      ? authorizedAccountEffectiveStatus(row.status, authorizedLocalStatus, row.bound_group_local_cooldown_until)
      : row.status
    const effectiveAuthorizedSchedulable = isAuthorizedView
      ? row.status === 'active' && effectiveAuthorizedStatus === 'active'
      : row.schedulable === 1
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
      credentials: accountCredentialsForList(row, includeCredentials),
      status: effectiveAuthorizedStatus,
      concurrencyLimit: isAuthorizedView ? 0 : row.concurrency_limit,
      currentConcurrency: isAuthorizedView ? 0 : (currentConcurrencyByAccount.get(row.id) ?? 0),
      priority: isAuthorizedView ? 0 : row.priority,
      superPriorityEnabled: isAuthorizedView
        ? row.bound_group_local_super_priority_enabled === 1
        : row.super_priority_enabled === 1,
      fallbackEnabled: isAuthorizedView
        ? row.bound_group_local_fallback_enabled === 1
        : row.fallback_enabled === 1,
      qualityScore: typeof row.quality_score === 'number' ? row.quality_score : undefined,
      qualityState: typeof row.quality_state === 'string' ? row.quality_state : undefined,
      qualityEwmaFirstTokenMs: typeof row.quality_ewma_first_token_ms === 'number' ? row.quality_ewma_first_token_ms : undefined,
      qualityRecentAvgFirstTokenMs: typeof row.quality_recent_avg_first_token_ms === 'number' ? row.quality_recent_avg_first_token_ms : undefined,
      qualityRecentRequestCount: typeof row.quality_recent_request_count === 'number' ? row.quality_recent_request_count : undefined,
      qualityRecentSuccessRate: typeof row.quality_recent_success_rate === 'number' ? row.quality_recent_success_rate : undefined,
      qualityUpdatedAt: row.quality_updated_at ?? undefined,
      proxyProfileId: row.proxy_profile_id ?? undefined,
      passthroughEnabled: isAuthorizedView ? false : row.passthrough_enabled === 1,
      errorPolicyId: isAuthorizedView ? undefined : row.error_policy_id ?? undefined,
      schedulable: effectiveAuthorizedSchedulable,
      accountExpiresAt: isAuthorizedView ? undefined : row.account_expires_at ?? undefined,
      cooldownUntil: isAuthorizedView ? row.bound_group_local_cooldown_until ?? undefined : row.cooldown_until ?? undefined,
      lastErrorCode: isAuthorizedView ? (effectiveAuthorizedStatus === row.status ? row.last_error_code ?? undefined : undefined) : row.last_error_code ?? undefined,
      lastErrorMessage: isAuthorizedView ? row.bound_group_local_last_error_message ?? undefined : row.last_error_message ?? undefined,
      streamFailureCount: isAuthorizedView ? 0 : Math.max(0, Number(row.stream_failure_count ?? 0)),
      streamFailureWindowStartedAt: isAuthorizedView ? undefined : row.stream_failure_window_started_at ?? undefined,
      localStatus: isAuthorizedView ? authorizedLocalStatus : undefined,
      localCooldownUntil: isAuthorizedView ? row.bound_group_local_cooldown_until ?? undefined : undefined,
      localLastErrorMessage: isAuthorizedView ? row.bound_group_local_last_error_message ?? undefined : undefined,
      lastUsedAt: isAuthorizedView ? usage.lastUsedAt : row.last_used_at ?? usage.lastUsedAt,
      todayUsage,
      usage,
      oauthUsage: row.provider_code === 'openai' && row.type === 'oauth' ? oauthUsageByAccount.get(row.id) : undefined,
      accessType: row.access_type ?? 'owner',
      accountAuthorizationId: row.authorization_id ?? undefined,
      boundGroupId: groupBinding?.groupId,
      boundGroupName: groupBinding?.groupName,
      groupBindStatus: groupBinding?.groupBindStatus,
      bindingSystemAccountId: isAuthorizedView && groupBinding ? groupBindingSystemAccountId : undefined,
      authorizationStatus: row.authorization_status ?? undefined,
      authorizationSources: row.authorization_id ? sanitizeAuthorizationSourcesForViewer(sourcesByAuthorization.get(row.authorization_id) ?? [], isAuthorizedView) : undefined,
      permissions: isAuthorizedView && row.system_account_id !== viewerSystemAccountId ? authorizedPermissions() : ownerPermissions(),
      authorizationUsageAvailable: !isAuthorizedView && authorizationStats.authorizationCount > 0 && canManageResourceOwner(row.system_account_id, access),
      authorizationCount: isAuthorizedView ? 0 : authorizationStats.authorizationCount,
      authorizationTeamCount: isAuthorizedView ? 0 : authorizationStats.authorizationTeamCount
    }
  })
}

export function getAccountUsageStatsOverview(access?: AccessScope, range?: AccountUsageStatsRange): AccountUsageStatsOverview {
  const accountRows = listAccounts(access)
  const defaultTrendAccountIds = loadAccountUsageDefaultTrendAccountIds(access)
  const overview = buildAccountUsageStatsOverview({
    access,
    accounts: accountRows,
    range: range ?? normalizeAccountUsageStatsRange({}, usageStatsTimezone()),
    defaultTrendAccountIds,
    loadUsageDailySeries: loadUsageDailySeriesForScopeRequests
  })
  return withAllAccountsDefaultTrendIds(access, overview)
}

export function getAccountUsageStatsOverviewPage(access?: AccessScope, options?: AccountListOptions & { range?: AccountUsageStatsRange }): AccountUsageStatsOverview {
  const listOptions = normalizeAccountListOptions(options)
  const defaultTrendAccountIds = loadAccountUsageDefaultTrendAccountIds(access)
  const range = options?.range ?? normalizeAccountUsageStatsRange({}, usageStatsTimezone())
  const overview = buildAccountUsageStatsOverviewPageFromWindows({
    access,
    range,
    page: listOptions.page,
    pageSize: listOptions.pageSize,
    keyword: listOptions.keyword,
    type: listOptions.type,
    defaultTrendAccountIds,
  })
  return withAllAccountsDefaultTrendIds(access, overview)
}

function withAllAccountsDefaultTrendIds(access: AccessScope | undefined, overview: AccountUsageStatsOverview): AccountUsageStatsOverview {
  if (overview.defaultTrendAccountIds.length > 0) return overview
  const defaultTrendAccountIds = allAccountsDefaultTrendAccountIds(access, overview.rows)
  return defaultTrendAccountIds ? { ...overview, defaultTrendAccountIds } : overview
}

function allAccountsDefaultTrendAccountIds(access: AccessScope | undefined, rows: AccountUsageStatsOverview['rows']): string[] | undefined {
  if (scopedSystemAccountId(access) || !canAccessAll(access)) return undefined
  return rows
    .filter((row) => row.rangeUsage.requestCount > 0 || row.rangeUsage.totalTokens > 0 || row.rangeUsage.totalCost > 0)
    .slice(0, 10)
    .map((row) => row.id)
}

function loadAccountUsageDefaultTrendAccountIds(access?: AccessScope): string[] {
  const scopedId = scopedSystemAccountId(access)
  const systemAccountId = scopedId ?? (canAccessAll(access) ? GLOBAL_STATS_SYSTEM_ACCOUNT_ID : undefined)
  if (!systemAccountId) return []
  const scopeType = scopedId ? 'caller_account' : 'account'
  const database = getRecordDatabase()
  const rows = database.prepare(`
    SELECT scope_id
    FROM usage_rank_snapshots
    WHERE system_account_id = ?
      AND scope_type = ?
      AND window_key = 'last7d'
      AND metric = 'request_count'
      AND snapshot_at = (
        SELECT MAX(snapshot_at)
        FROM usage_rank_snapshots
        WHERE system_account_id = ?
          AND scope_type = ?
          AND window_key = 'last7d'
          AND metric = 'request_count'
      )
    ORDER BY rank ASC
    LIMIT 10
  `).all(systemAccountId, scopeType, systemAccountId, scopeType) as unknown as Array<{ scope_id?: string }>
  return rows.map((row) => row.scope_id).filter((id): id is string => Boolean(id))
}

export function findAccountForTest(accountId: string, access?: AccessScope): AccountSummary | undefined {
  const visibleAccount = findAccountSummary(accountId, access)
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
      ORDER BY cooldown_until ASC, priority ASC, created_at ASC, id ASC
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
      superPriorityEnabled: row.super_priority_enabled === 1,
      fallbackEnabled: row.fallback_enabled === 1,
      proxyProfileId: row.proxy_profile_id ?? undefined,
      passthroughEnabled: row.passthrough_enabled === 1,
      errorPolicyId: row.error_policy_id ?? undefined,
      schedulable: row.schedulable === 1,
      accountExpiresAt: row.account_expires_at ?? undefined,
      cooldownUntil: row.cooldown_until ?? undefined,
      lastErrorCode: row.last_error_code ?? undefined,
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
  const providerCode = String(input.providerCode ?? input.provider_code ?? 'openai').trim() || 'openai'
  const explicitGroupId = typeof input.groupId === 'string' && input.groupId ? input.groupId : typeof input.group_id === 'string' && input.group_id ? input.group_id : undefined
  const explicitGroup = explicitGroupId ? groupOwnerAndProvider(explicitGroupId) : undefined
  const requestedSystemAccountId = writeSystemAccountId(access)
  const systemAccountId = explicitGroup && canManageResourceOwner(explicitGroup.systemAccountId, access) ? explicitGroup.systemAccountId : requestedSystemAccountId
  const provider = listProviders().find((item) => item.code === providerCode)
  const credentials = typeof input.credentials === 'object' && input.credentials !== null ? input.credentials as Record<string, unknown> : {}
  const credentialMap = credentials as Record<string, unknown>
  const accountType = String(input.type ?? 'api_key').trim() || 'api_key'
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
  const createSuperPriorityEnabled = normalizeSuperPriorityInput(input.superPriorityEnabled ?? input.super_priority_enabled, false)
  const createFallbackEnabled = normalizeFallbackInput(input.fallbackEnabled ?? input.fallback_enabled, false)
  if (nextStatus !== 'active' && (createSuperPriorityEnabled || createFallbackEnabled)) {
    throw new Error('只有正常状态的账户可以设置超级优先或降级备用')
  }
  if (createSuperPriorityEnabled && createFallbackEnabled) {
    throw new Error('超级优先和降级备用不能同时开启')
  }
  const account: AccountSummary = {
    id,
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields(access) ? loadSystemAccountNameMap().get(systemAccountId) : undefined,
    providerCode,
    name: normalizedEntityName(input.name, `未命名 ${provider?.name ?? providerCode.toUpperCase()} 账户`),
    notes: optionalString(input.notes),
    type: accountType,
    credentials,
    status: nextStatus,
    concurrencyLimit: Number(input.concurrencyLimit ?? input.concurrency_limit ?? DEFAULT_ACCOUNT_CONCURRENCY_LIMIT),
    currentConcurrency: 0,
    priority: Number(input.priority ?? input.priority_level ?? 0),
    superPriorityEnabled: createSuperPriorityEnabled,
    fallbackEnabled: createFallbackEnabled,
    proxyProfileId,
    passthroughEnabled: providerPassthroughEnabled(provider),
    errorPolicyId: optionalString(input.errorPolicyId ?? input.error_policy_id),
    schedulable: expiredByPackage || isHardUnavailableAccountStatus(nextStatus) ? false : input.schedulable !== false,
    accountExpiresAt: accountExpiresAt ?? undefined,
    cooldownUntil: expiredByPackage ? undefined : initialCooldownUntil,
    lastErrorCode: undefined,
    lastErrorMessage: expiredByPackage ? '账户套餐已过期，已自动停用' : initialCooldownUntil ? '创建时设置为临时不可调用' : undefined,
    lastUsedAt: undefined,
    todayUsage: emptyAccountUsageSummary(),
    usage: emptyAccountUsageSummary(),
    boundGroupId: groupId,
    boundGroupName: group.name ?? groupId,
    groupBindStatus: 'bound'
  }

  const database = getDatabase()
  assertAccountNameAvailable(systemAccountId, providerCode, account.name)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database
      .prepare(`
        INSERT INTO accounts (
          id, system_account_id, provider_code, name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
          proxy_profile_id, concurrency_limit, passthrough_enabled, error_policy_id,
          priority, super_priority_enabled, fallback_enabled, schedulable, notes, account_expires_at, cooldown_until, last_error_code, last_error_message, stream_failure_count, stream_failure_window_started_at, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
        account.fallbackEnabled ? 1 : 0,
        account.schedulable ? 1 : 0,
        optionalString(input.notes) ?? null,
        account.accountExpiresAt ?? null,
        account.cooldownUntil ?? null,
        account.lastErrorCode ?? null,
        account.lastErrorMessage ?? null,
        0,
        null,
        now,
        now
      )
    database
      .prepare('INSERT INTO group_accounts (system_account_id, group_id, account_id, weight, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, 1, ?, ?)')
      .run(systemAccountId, groupId, account.id, 1, now, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    if (isDuplicateAccountCredentialError(error)) {
      throwDuplicateAccountCredentialError()
    }
    if (isDuplicateAccountNameError(error)) {
      throw new Error(`同一供应商下账户名称已存在：${account.name}`)
    }
    throw error
  }
  refreshGroupAccountStatsAfterWrite()

  return account
}

export function updateAccount(id: string, input: Record<string, unknown>, access?: AccessScope): AccountSummary | undefined {
  const current = findAccountSummary(id, access)
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
  if (hasStatusInput && current.status === 'error' && requestedStatus !== 'error') {
    throw new Error('异常账户不能通过编辑切换状态，请使用恢复异常')
  }
  if (hasStatusInput && requestedStatus === 'active' && (isCoolingAccountStatus(current.status) || current.status === 'error')) {
    throw new Error('临时不可调用、限流中或异常账户不能通过启用账户恢复，请使用恢复正常或恢复异常')
  }
  const nextStatus = expiredByPackage ? 'disabled' : requestedStatus
  let nextCooldownUntil = current.cooldownUntil
  let nextLastErrorCode = current.lastErrorCode
  let nextLastErrorMessage = current.lastErrorMessage
  if (hasStatusInput) {
    if (nextStatus === 'active') {
      nextCooldownUntil = undefined
      nextLastErrorCode = undefined
      nextLastErrorMessage = undefined
    } else if (nextStatus === 'disabled' || nextStatus === 'error') {
      nextCooldownUntil = undefined
      if (nextStatus === 'disabled') {
        nextLastErrorCode = undefined
      }
    } else if (isCoolingAccountStatus(nextStatus) && !nextCooldownUntil) {
      nextCooldownUntil = new Date(Date.now() + defaultTemporaryUnschedulableMinutes() * 60_000).toISOString()
      nextLastErrorCode = undefined
      nextLastErrorMessage = nextLastErrorMessage ?? '手动设置为临时不可调用'
    }
  }
  if (expiredByPackage) {
    nextCooldownUntil = undefined
    nextLastErrorCode = undefined
    nextLastErrorMessage = '账户套餐已过期，已自动停用'
  }
  const hasSuperPriorityInput = Object.prototype.hasOwnProperty.call(input, 'superPriorityEnabled')
    || Object.prototype.hasOwnProperty.call(input, 'super_priority_enabled')
  const requestedSuperPriority = normalizeSuperPriorityInput(
    Object.prototype.hasOwnProperty.call(input, 'superPriorityEnabled') ? input.superPriorityEnabled : input.super_priority_enabled,
    current.superPriorityEnabled
  )
  if (hasSuperPriorityInput && requestedSuperPriority && nextStatus !== 'active' && !current.superPriorityEnabled) {
    throw new Error('只有正常状态的账户可以设置超级优先')
  }
  let nextSuperPriorityEnabled = requestedSuperPriority
  const hasFallbackInput = Object.prototype.hasOwnProperty.call(input, 'fallbackEnabled')
    || Object.prototype.hasOwnProperty.call(input, 'fallback_enabled')
  const requestedFallback = normalizeFallbackInput(
    Object.prototype.hasOwnProperty.call(input, 'fallbackEnabled') ? input.fallbackEnabled : input.fallback_enabled,
    current.fallbackEnabled
  )
  if (hasFallbackInput && requestedFallback && nextStatus !== 'active' && !current.fallbackEnabled) {
    throw new Error('只有正常状态的账户可以设置降级备用')
  }
  if (hasSuperPriorityInput && requestedSuperPriority && hasFallbackInput && requestedFallback) {
    throw new Error('超级优先和降级备用不能同时开启')
  }
  let nextFallbackEnabled = requestedFallback
  if (hasSuperPriorityInput && nextSuperPriorityEnabled) {
    nextFallbackEnabled = false
  }
  if (hasFallbackInput && nextFallbackEnabled) {
    nextSuperPriorityEnabled = false
  }

  const next: AccountSummary = {
    ...current,
    name: typeof input.name === 'string' && input.name.trim() ? input.name.trim() : current.name,
    notes: hasNotesInput ? optionalNullableString(input.notes) ?? undefined : current.notes,
    credentials,
    status: nextStatus,
    concurrencyLimit: Number(input.concurrencyLimit ?? input.concurrency_limit ?? current.concurrencyLimit),
    priority: Number(input.priority ?? input.priority_level ?? current.priority),
    superPriorityEnabled: nextSuperPriorityEnabled,
    fallbackEnabled: nextFallbackEnabled,
    proxyProfileId: Object.prototype.hasOwnProperty.call(input, 'proxyProfileId') || Object.prototype.hasOwnProperty.call(input, 'proxy_profile_id')
      ? globalProxyProfileId(optionalString(input.proxyProfileId ?? input.proxy_profile_id))
      : current.proxyProfileId,
    passthroughEnabled: providerPassthroughEnabled(provider),
    errorPolicyId: rawErrorPolicyId === undefined ? current.errorPolicyId : optionalString(rawErrorPolicyId),
    schedulable: expiredByPackage || isHardUnavailableAccountStatus(nextStatus)
      ? false
      : hasStatusInput
        ? true
        : typeof input.schedulable === 'boolean'
          ? input.schedulable
          : current.schedulable,
    accountExpiresAt: nextAccountExpiresAt ?? undefined,
    cooldownUntil: nextCooldownUntil,
    lastErrorCode: nextLastErrorCode,
    lastErrorMessage: nextLastErrorMessage,
    lastUsedAt: current.lastUsedAt,
    usage: current.usage
  }

  assertAccountNameAvailable(systemAccountId, next.providerCode, next.name, id)
  try {
    const result = getDatabase()
      .prepare(`
      UPDATE accounts
      SET name = ?, notes = ?, status = ?, credentials_encrypted = ?, credential_fingerprint = ?, credential_mask = ?,
            proxy_profile_id = ?, concurrency_limit = ?, passthrough_enabled = ?,
            error_policy_id = ?, priority = ?, super_priority_enabled = ?, fallback_enabled = ?, schedulable = ?, account_expires_at = ?, cooldown_until = ?, last_error_code = ?, last_error_message = ?, updated_at = ?
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
        next.fallbackEnabled ? 1 : 0,
        next.schedulable ? 1 : 0,
        next.accountExpiresAt ?? null,
        next.cooldownUntil ?? null,
        next.lastErrorCode ?? null,
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
    if (isDuplicateAccountNameError(error)) {
      throw new Error(`同一供应商下账户名称已存在：${next.name}`)
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
  access?: AccessScope
): AccountSummary | undefined {
  const current = findAccountSummary(id, access)
  if (!current) {
    return undefined
  }
  const ownerSystemAccountId = accountSystemAccountId(id)
  if (ownerSystemAccountId && !canManageResourceOwner(ownerSystemAccountId, access)) {
    return undefined
  }
  const expiredByPackage = isAccountExpired(current.accountExpiresAt)
  if (current.status === 'disabled' && !expiredByPackage) {
    return current
  }
  if (expiredByPackage) {
    const result = getDatabase()
      .prepare(`
        UPDATE accounts
        SET status = 'disabled',
            schedulable = 0,
            cooldown_until = NULL,
            last_error_code = NULL,
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
    return findAccountSummary(id, access)
  }

  const result = getDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'active',
          schedulable = 1,
          cooldown_until = NULL,
          last_error_code = NULL,
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

  return findAccountSummary(id, access)
}

export function clearAccountStreamFailureState(id: string): boolean {
  const result = getDatabase()
    .prepare(`
      UPDATE accounts
      SET stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          last_error_code = CASE
            WHEN status = 'active' THEN NULL
            ELSE last_error_code
          END,
          last_error_message = CASE
            WHEN status = 'active' THEN NULL
            ELSE last_error_message
          END,
          updated_at = ?
      WHERE id = ?
        AND status NOT IN ('disabled', 'error')
        AND (
          stream_failure_count > 0
          OR stream_failure_window_started_at IS NOT NULL
          OR (status = 'active' AND last_error_code IS NOT NULL)
          OR (status = 'active' AND last_error_message IS NOT NULL)
        )
    `)
    .run(nowIso(), id)
  return Number(result.changes ?? 0) > 0
}

export function markAccountCooldown(id: string, until: string, reason: string, status: AccountStatus = 'temporary_unavailable'): AccountSummary | undefined {
  const current = findAccountSummary(id)
  if (!current) {
    return undefined
  }
  if (isHardUnavailableAccountStatus(current.status)) {
    return undefined
  }

  const expiredByPackage = isAccountExpired(current.accountExpiresAt)
  if (expiredByPackage) {
    const result = getDatabase()
      .prepare(`
        UPDATE accounts
        SET status = 'disabled',
            schedulable = 0,
            cooldown_until = NULL,
            last_error_code = NULL,
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
    return findAccountSummary(id)
  }

  const cooldownStatus: AccountStatus = status === 'rate_limited' ? 'rate_limited' : 'temporary_unavailable'

  const result = getDatabase()
    .prepare(`
      UPDATE accounts
      SET status = ?,
          schedulable = 1,
          cooldown_until = ?,
          last_error_code = NULL,
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

  return findAccountSummary(id)
}

export function migrateAccountTraffic(input: {
  sourceAccountId: string
  targetAccountId: string
  sourceStatus: AccountTrafficMigrationSourceStatus
}, access?: AccessScope): { sourceAccount: AccountSummary; targetAccount: AccountSummary; sourceCooldownUntil?: string } | undefined {
  const sourceVisibleAccount = findAccountSummary(input.sourceAccountId, access)
  if (sourceVisibleAccount?.accessType === 'authorized') {
    return migrateAuthorizedAccountBindingTraffic(input, access)
  }
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
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const updateResult = input.sourceStatus === 'disabled'
      ? database
        .prepare(`
          UPDATE accounts
          SET status = 'disabled',
              schedulable = 0,
              cooldown_until = NULL,
              last_error_code = NULL,
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
              cooldown_until = ?,
              last_error_code = NULL,
              last_error_message = ?,
              stream_failure_count = 0,
              stream_failure_window_started_at = NULL,
              updated_at = ?
          WHERE id = ? AND system_account_id = ?
        `)
        .run(sourceCooldownUntil, reason, now, sourceRow.id, sourceRow.system_account_id)
    if (Number(updateResult.changes ?? 0) <= 0) {
      rollbackDatabaseTransaction(database, transactionStarted)
      return undefined
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshGroupAccountStatsAfterWrite()

  const ownerAccess = { systemAccountId: sourceRow.system_account_id, role: 'user' as const }
  const sourceAccount = findAccountSummary(input.sourceAccountId, ownerAccess)
  const targetAccount = findAccountSummary(input.targetAccountId, ownerAccess)
  if (!sourceAccount || !targetAccount) {
    return undefined
  }
  return { sourceAccount, targetAccount, sourceCooldownUntil: sourceCooldownUntil ?? undefined }
}

export function updateAuthorizedAccountBindingDispatch(
  accountId: string,
  input: { superPriorityEnabled?: boolean; fallbackEnabled?: boolean; clearFailureState?: boolean },
  access?: AccessScope
): AccountSummary | undefined {
  const systemAccountId = authorizedBindingSystemAccountId(access)
  const current = findAccountSummary(accountId, access)
  if (current?.accessType !== 'authorized' || !current.boundGroupId) {
    throw new Error('授权账户需要先绑定到你的分组')
  }
  if (current.status !== 'active' && (input.superPriorityEnabled === true || input.fallbackEnabled === true)) {
    throw new Error('只有正常状态的授权账户可以调整调度标记')
  }
  const hasSuperPriorityInput = Object.prototype.hasOwnProperty.call(input, 'superPriorityEnabled')
  const hasFallbackInput = Object.prototype.hasOwnProperty.call(input, 'fallbackEnabled')
  const nextSuperPriority = hasSuperPriorityInput ? input.superPriorityEnabled === true : current.superPriorityEnabled
  const nextFallback = hasFallbackInput ? input.fallbackEnabled === true : current.fallbackEnabled
  if (nextSuperPriority && nextFallback) {
    throw new Error('超级优先和降级备用不能同时开启')
  }
  const clearFailureState = input.clearFailureState === true
  const now = nowIso()
  const result = getDatabase()
    .prepare(`
      UPDATE group_accounts
      SET local_status = ?,
          local_cooldown_until = ?,
          local_last_error_message = ?,
          local_super_priority_enabled = ?,
          local_fallback_enabled = ?,
          updated_at = ?
      WHERE account_id = ?
        AND system_account_id = ?
        AND enabled = 1
        AND account_authorization_id IS NOT NULL
    `)
    .run(
      clearFailureState ? 'active' : current.localStatus ?? 'active',
      clearFailureState ? null : current.localCooldownUntil ?? null,
      clearFailureState ? null : current.localLastErrorMessage ?? null,
      nextSuperPriority ? 1 : 0,
      nextFallback ? 1 : 0,
      now,
      accountId,
      systemAccountId
    )
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  refreshGroupAccountStatsAfterWrite()
  return findAccountSummary(accountId, { systemAccountId, role: 'user' })
}

function migrateAuthorizedAccountBindingTraffic(input: {
  sourceAccountId: string
  targetAccountId: string
  sourceStatus: AccountTrafficMigrationSourceStatus
}, access?: AccessScope): { sourceAccount: AccountSummary; targetAccount: AccountSummary; sourceCooldownUntil?: string } | undefined {
  const systemAccountId = authorizedBindingSystemAccountId(access)
  if (input.sourceAccountId === input.targetAccountId) {
    throw new Error('目标账户不能和当前账户相同')
  }
  const accountAccess = { systemAccountId, role: 'user' as const }
  const sourceAccount = findAccountSummary(input.sourceAccountId, accountAccess)
  const targetAccount = findAccountSummary(input.targetAccountId, accountAccess)
  if (sourceAccount?.accessType !== 'authorized') {
    return undefined
  }
  if (!sourceAccount || !targetAccount) {
    return undefined
  }
  if (!sourceAccount.boundGroupId || !targetAccount.boundGroupId || sourceAccount.boundGroupId !== targetAccount.boundGroupId) {
    throw new Error('目标账户必须和当前账户在你的同一个分组内')
  }
  if (sourceAccount.providerCode !== targetAccount.providerCode) {
    throw new Error('目标账户必须和当前账户属于同一个供应商')
  }
  if (targetAccount.status !== 'active' || !targetAccount.schedulable || isLaterIso(targetAccount.cooldownUntil, nowIso())) {
    throw new Error('目标账户当前不可调度，请选择正常可用的账户')
  }
  const sourceCooldownUntil = input.sourceStatus === 'temporary_unavailable'
    ? new Date(Date.now() + defaultTemporaryUnschedulableMinutes() * 60_000).toISOString()
    : null
  const now = nowIso()
  const sourceLocalStatus = input.sourceStatus === 'disabled' ? 'disabled' : 'temporary_unavailable'
  const result = getDatabase()
    .prepare(`
      UPDATE group_accounts
      SET local_status = ?,
          local_cooldown_until = ?,
          local_last_error_message = ?,
          updated_at = ?
      WHERE account_id = ?
        AND system_account_id = ?
        AND group_id = ?
        AND enabled = 1
        AND account_authorization_id IS NOT NULL
    `)
    .run(sourceLocalStatus, sourceCooldownUntil, manualTrafficMigrationReason, now, sourceAccount.id, systemAccountId, sourceAccount.boundGroupId)
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  refreshGroupAccountStatsAfterWrite()
  const nextSource = findAccountSummary(input.sourceAccountId, accountAccess)
  const nextTarget = findAccountSummary(input.targetAccountId, accountAccess)
  return nextSource && nextTarget ? { sourceAccount: nextSource, targetAccount: nextTarget, sourceCooldownUntil: sourceCooldownUntil ?? undefined } : undefined
}

export function markAccountException(
  id: string,
  errorCode: string,
  reason: string,
  options: { preserveDisabled?: boolean } = {}
): AccountSummary | undefined {
  const current = findAccountSummary(id)
  if (!current) {
    return undefined
  }
  if (current.status === 'disabled' && options.preserveDisabled !== false) {
    return undefined
  }

  const result = getDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'error',
          schedulable = 0,
          cooldown_until = NULL,
          last_error_code = ?,
          last_error_message = ?,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
    `)
    .run(errorCode || null, reason || null, nowIso(), id)
  if (Number(result.changes ?? 0) > 0) {
    refreshGroupAccountStatsAfterWrite()
  }

  return findAccountSummary(id)
}

export function markAccountDisabledByFailure(id: string, reason: string): AccountSummary | undefined {
  const current = findAccountSummary(id)
  if (!current || current.status === 'error') {
    return undefined
  }
  return markAccountException(id, 'upstream_failure', reason)
}

export function recordAccountStreamFailure(input: {
  accountId: string
  thresholdCount: number
  thresholdWindowMinutes: number
  action: 'cooldown' | 'disable' | 'none'
  cooldownMinutes: number
  reason: string
}): { count: number; triggered: boolean; account?: AccountSummary } {
  const row = getDatabase().prepare('SELECT id, status, stream_failure_count, stream_failure_window_started_at FROM accounts WHERE id = ?').get(input.accountId) as unknown as AccountFailureRow | undefined
  if (!row) {
    return { count: 0, triggered: false }
  }
  if (isHardUnavailableAccountStatus(row.status)) {
    return { count: Math.max(0, row.stream_failure_count), triggered: false, account: findAccountSummary(input.accountId) }
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
    return { count, triggered: false, account: findAccountSummary(input.accountId) }
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

  return { count, triggered: true, account: findAccountSummary(input.accountId) }
}

export function listGroups(access?: AccessScope): GroupSummary[] {
  return buildGroupSummaries(listGroupRowsForAccess(access), access)
}

export function listGroupsPage(access?: AccessScope, options?: GroupListOptions): GroupListResult {
  const page = listGroupRowsPageForAccess(access, options)
  return {
    items: buildGroupSummaries(page.rows, access),
    total: page.total,
    hasMore: page.hasMore,
    page: page.page,
    pageSize: page.pageSize
  }
}

export function listGroupOptions(access?: AccessScope): GroupOptionSummary[] {
  return buildGroupOptionSummaries(listGroupRowsForAccess(access), access)
}

export function listAccountGroupOptions(access?: AccessScope): AccountGroupOptionSummary[] {
  const rows = listGroupRowsForAccess(access)
  const accountIdsByGroup = loadGroupAccountIdsByGroupIds(rows.map((row) => row.id))
  return buildGroupOptionSummaries(rows, access).map((group) => ({
    ...group,
    accountIds: accountIdsByGroup.get(group.id) ?? []
  }))
}

export function findGroupSummary(id: string, access?: AccessScope): GroupSummary | undefined {
  const row = findGroupRowForAccess(access, id)
  return row ? buildGroupSummaries([row], access)[0] : undefined
}

function buildGroupOptionSummaries(rows: GroupListRow[], access?: AccessScope): GroupOptionSummary[] {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const hasAuthorizedRows = rows.some((row) => row.access_type === 'authorized')
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const accountNames = shouldIncludeSystemAccountFields || hasAuthorizedRows ? loadSystemAccountNameMap() : new Map<string, string>()
  return rows.map((row) => {
    const isAuthorizedView = row.access_type === 'authorized'
    return {
      id: row.id,
      systemAccountId: shouldIncludeSystemAccountFields ? row.system_account_id : undefined,
      systemAccountName: shouldIncludeSystemAccountFields ? accountNames.get(row.system_account_id) : undefined,
      ownerSystemAccountId: row.system_account_id,
      ownerSystemAccountName: accountNames.get(row.system_account_id),
      name: row.name,
      providerCode: row.provider_code,
      enabled: row.enabled === 1,
      isDefault: isAuthorizedView ? false : row.is_default === 1,
      accessType: row.access_type ?? 'owner',
      groupAuthorizationId: row.authorization_id ?? undefined,
      authorizationStatus: row.authorization_status ?? undefined,
      permissions: isAuthorizedView && row.system_account_id !== viewerSystemAccountId ? authorizedPermissions() : ownerPermissions()
    }
  })
}

function buildGroupSummaries(rows: GroupListRow[], access?: AccessScope): GroupSummary[] {
  const timezone = usageStatsTimezone()
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const groupIds = rows.map((row) => row.id)
  const groupStatsByGroup = loadGroupAccountStatsByGroupIds(groupIds)
  const accountIdsByGroup = loadGroupAccountIdsByGroupIds(groupIds)
  const currentConcurrencyByAccount = loadAccountCurrentConcurrencyByIds([...accountIdsByGroup.values()].flat())
  const groupUsageScopes = rows.map((row) => usageScope(row.id, row.system_account_id, row.id))
  const groupAuthorizationScopes = rows
    .filter((row) => row.authorization_id)
    .map((row) => usageScope(row.authorization_id ?? '', row.system_account_id, row.authorization_id ?? ''))
  const todayUsageByGroup = loadGroupUsageSummariesForScopes(groupUsageScopes, todayDateKey(timezone))
  const totalUsageByGroup = loadGroupUsageSummariesForScopes(groupUsageScopes)
  const todayUsageByAuthorization = loadGroupAuthorizationUsageSummaries(groupAuthorizationScopes, todayDateKey(timezone))
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
  const providerCode = String(input.providerCode ?? input.provider_code ?? 'openai').trim() || 'openai'
  const name = normalizedEntityName(input.name, '未命名分组')
  assertGroupNameAvailable(systemAccountId, providerCode, name)
  const group: GroupSummary = {
    id: newId('grp'),
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields(access) ? loadSystemAccountNameMap().get(systemAccountId) : undefined,
    name,
    providerCode,
    description: optionalString(input.description),
    enabled: input.enabled !== false,
    isDefault: false,
    accountIds: [],
    accountStats: emptyGroupAccountStats()
  }
  try {
    getDatabase()
      .prepare('INSERT INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)')
      .run(group.id, systemAccountId, group.name, group.providerCode, group.description ?? null, group.enabled ? 1 : 0, now, now)
  } catch (error) {
    if (isDuplicateGroupNameError(error)) {
      throw new Error(`同一供应商下分组名称已存在：${group.name}`)
    }
    throw error
  }
  return group
}

export function updateGroup(id: string, input: Record<string, unknown>, access?: AccessScope): GroupSummary | undefined {
  const current = findGroupSummary(id, access)
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
    name: typeof input.name === 'string' && input.name.trim() ? input.name.trim() : current.name,
    providerCode: typeof input.providerCode === 'string' && input.providerCode.trim()
      ? input.providerCode.trim()
      : typeof input.provider_code === 'string' && input.provider_code.trim()
        ? input.provider_code.trim()
        : current.providerCode,
    description: hasDescriptionInput ? optionalNullableString(input.description) ?? undefined : current.description,
    enabled: typeof input.enabled === 'boolean' ? input.enabled : current.enabled
  }
  if (next.providerCode !== current.providerCode && current.accountStats.total > 0) {
    throw new Error('已有账户的分组不允许修改供应商')
  }
  assertGroupNameAvailable(systemAccountId, next.providerCode, next.name, id)
  const database = getDatabase()
  try {
    database
      .prepare('UPDATE groups SET name = ?, provider_code = ?, description = ?, enabled = ?, updated_at = ? WHERE id = ? AND system_account_id = ?')
      .run(next.name, next.providerCode, next.description ?? null, next.enabled ? 1 : 0, nowIso(), id, systemAccountId)
  } catch (error) {
    if (isDuplicateGroupNameError(error)) {
      throw new Error(`同一供应商下分组名称已存在：${next.name}`)
    }
    throw error
  }
  return findGroupSummary(id, access)
}

export function deleteGroup(id: string, access?: AccessScope): boolean {
  const current = findGroupSummary(id, access)
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
  const current = findAccountSummary(accountId, { systemAccountId: group.systemAccountId, role: 'user' })
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

  return findAccountSummary(accountId, { systemAccountId: group.systemAccountId, role: 'user' })
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
  return findGroupSummary(groupId)
}

export function listSystemTeams(access?: AccessScope): SystemTeamSummary[] {
  const scopedId = scopedSystemAccountId(access)
  const rows = scopedId
    ? getDatabase()
      .prepare(`
        SELECT DISTINCT system_teams.id, system_teams.name, system_teams.description, system_teams.status, system_teams.created_by, system_teams.created_at, system_teams.updated_at
        FROM system_teams
        INNER JOIN system_team_members ON system_team_members.team_id = system_teams.id
        WHERE system_team_members.system_account_id = ?
          AND system_team_members.status = 'active'
        ORDER BY system_teams.status ASC, system_teams.updated_at DESC, system_teams.name ASC, system_teams.id ASC
      `)
      .all(scopedId) as unknown as SystemTeamRow[]
    : getDatabase().prepare('SELECT id, name, description, status, created_by, created_at, updated_at FROM system_teams ORDER BY status ASC, updated_at DESC, name ASC, id ASC').all() as unknown as SystemTeamRow[]
  const members = listSystemTeamMembersForTeamIds(rows.map((row) => row.id), true)
  return rows.map((row) => systemTeamSummaryFromRow(row, members.get(row.id) ?? [], access))
}

export function findSystemTeamSummary(id: string, access?: AccessScope): SystemTeamSummary | undefined {
  const scopedId = scopedSystemAccountId(access)
  const row = scopedId
    ? getDatabase()
      .prepare(`
        SELECT DISTINCT system_teams.*
        FROM system_teams
        INNER JOIN system_team_members ON system_team_members.team_id = system_teams.id
        WHERE system_teams.id = ?
          AND system_team_members.system_account_id = ?
          AND system_team_members.status = 'active'
        LIMIT 1
      `)
      .get(id, scopedId) as unknown as SystemTeamRow | undefined
    : getDatabase().prepare('SELECT * FROM system_teams WHERE id = ?').get(id) as unknown as SystemTeamRow | undefined
  if (!row) return undefined
  const members = listSystemTeamMembersForTeamIds([row.id], true)
  return systemTeamSummaryFromRow(row, members.get(row.id) ?? [], access)
}

export function listAuthorizationGranteeAccounts(access?: AccessScope): SystemAccountPrincipalSummary[] {
  const database = getDatabase()
  const rows = database.prepare('SELECT id, username, display_name, status FROM system_accounts ORDER BY status ASC, display_name ASC, username ASC, id ASC').all() as unknown as Array<Pick<SystemAccountRow, 'id' | 'username' | 'display_name' | 'status'>>
  return rows.map(systemAccountPrincipalSummaryFromRow)
}

export function listAuthorizationGranteeTeams(access?: AccessScope): SystemTeamPrincipalSummary[] {
  const database = getDatabase()
  const rows = database.prepare('SELECT id, name, status FROM system_teams ORDER BY status ASC, name ASC, id ASC').all() as unknown as SystemTeamRow[]
  return rows.map(systemTeamPrincipalSummaryFromRow)
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
  const created = findSystemTeamSummary(id, access)
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
  const transactionStarted = beginDatabaseTransaction(database)
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
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  if (authorizationChanged) {
    refreshGroupAccountStatsAfterWrite()
  }
  return findSystemTeamSummary(id, access)
}

export function addSystemTeamMembers(teamId: string, input: Record<string, unknown>, access?: AccessScope): SystemTeamSummary | undefined {
  const team = getDatabase().prepare("SELECT * FROM system_teams WHERE id = ? AND status = 'active'").get(teamId) as unknown as SystemTeamRow | undefined
  if (!team) return undefined
  const systemAccountIds = normalizeSystemAccountIds(input.systemAccountIds ?? input.systemAccountId ?? input.memberIds)
  if (!systemAccountIds.length) throw new Error('请选择团队成员')
  const database = getDatabase()
  const now = nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const systemAccountId of systemAccountIds) {
      const account = findSystemAccountById(systemAccountId)
      if (!account || account.status !== 'active') throw new Error('团队成员不存在或已停用')
      const existing = database.prepare('SELECT * FROM system_team_members WHERE team_id = ? AND system_account_id = ? ORDER BY created_at DESC, id DESC LIMIT 1').get(teamId, systemAccountId) as unknown as SystemTeamMemberRow | undefined
      if (existing?.status === 'active') continue
      if (existing) {
        database.prepare("UPDATE system_team_members SET status = 'active', joined_at = ?, removed_at = NULL, updated_at = ? WHERE id = ?").run(now, now, existing.id)
      } else {
        database.prepare("INSERT INTO system_team_members (id, team_id, system_account_id, member_role, status, joined_at, removed_at, created_by, created_at, updated_at) VALUES (?, ?, ?, 'member', 'active', ?, NULL, ?, ?, ?)")
          .run(newId('teammem'), teamId, systemAccountId, now, currentSystemAccountId(access), now, now)
      }
      applyActiveTeamGrantsToMember(teamId, systemAccountId, access, database, now)
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshGroupAccountStatsAfterWrite()
  return findSystemTeamSummary(teamId, access)
}

export function removeSystemTeamMember(teamId: string, memberId: string, access?: AccessScope): SystemTeamSummary | undefined {
  const database = getDatabase()
  const member = database.prepare("SELECT * FROM system_team_members WHERE id = ? AND team_id = ? AND status = 'active'").get(memberId, teamId) as unknown as SystemTeamMemberRow | undefined
  if (!member) return undefined
  const now = nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database.prepare("UPDATE system_team_members SET status = 'removed', removed_at = ?, updated_at = ? WHERE id = ?").run(now, now, memberId)
    revokeTeamSourcesForMember(teamId, member.system_account_id, currentSystemAccountId(access), database, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshGroupAccountStatsAfterWrite()
  return findSystemTeamSummary(teamId, access)
}

export function listResourceAuthorizations(filters: Record<string, unknown> = {}, access?: AccessScope, options: ResourceAuthorizationListOptions = {}): ResourceAuthorizationSummary[] {
  expireDueResourceAuthorizations()
  return listResourceAuthorizationSummaries(filters, access, options)
}

export function listResourceAuthorizationsPage(filters: Record<string, unknown> = {}, access?: AccessScope, options: ResourceAuthorizationListOptions = {}): ResourceAuthorizationListResult {
  expireDueResourceAuthorizations()
  return listResourceAuthorizationSummariesPage(filters, access, options)
}

export function findResourceAuthorization(authorizationId: string, access?: AccessScope, options: ResourceAuthorizationListOptions = {}): ResourceAuthorizationSummary | undefined {
  expireDueResourceAuthorizations()
  return findResourceAuthorizationSummary(authorizationId, access, options)
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
  let createdGrantId: string | undefined
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    if (granteeType === 'team') {
      const team = database.prepare("SELECT * FROM system_teams WHERE id = ? AND status = 'active'").get(granteeId) as unknown as SystemTeamRow | undefined
      if (!team) throw new Error('团队不存在或已停用')
      const members = activeTeamMemberRows(granteeId, database).filter((member) => member.system_account_id !== ownerSystemAccountId)
      if (!members.length) throw new Error('团队暂无可授权成员，请先添加非归属人成员后再授权')
      const grant = upsertResourceAuthorizationGrant({ resourceType, resourceId, ownerSystemAccountId, granteeType, granteeId, remark: optionalString(input.remark), expiresAt: optionalNullableServerDateTimeIso(input.expiresAt ?? input.expires_at), limits: input.limits, modelPolicy: input.modelPolicy ?? input.model_policy, actor, now, database })
      createdGrantId = grant.id
      for (const member of members) {
        upsertResourceAuthorizationForUser({ resourceType, resourceId, ownerSystemAccountId, granteeSystemAccountId: member.system_account_id, sourceType: 'team', sourceTeamId: granteeId, remark: optionalString(input.remark), expiresAt: optionalNullableServerDateTimeIso(input.expiresAt ?? input.expires_at), limits: input.limits, modelPolicy: input.modelPolicy ?? input.model_policy, actor, now, database })
      }
    } else {
      const grantee = findSystemAccountById(granteeId)
      if (!grantee || grantee.status !== 'active') throw new Error('被授权用户不存在或已停用')
      if (granteeId === ownerSystemAccountId) throw new Error('不能授权给资源所有者自己')
      const grant = upsertResourceAuthorizationGrant({ resourceType, resourceId, ownerSystemAccountId, granteeType, granteeId, remark: optionalString(input.remark), expiresAt: optionalNullableServerDateTimeIso(input.expiresAt ?? input.expires_at), limits: input.limits, modelPolicy: input.modelPolicy ?? input.model_policy, actor, now, database })
      createdGrantId = grant.id
      upsertResourceAuthorizationForUser({ resourceType, resourceId, ownerSystemAccountId, granteeSystemAccountId: granteeId, sourceType: 'manual', remark: optionalString(input.remark), expiresAt: optionalNullableServerDateTimeIso(input.expiresAt ?? input.expires_at), limits: input.limits, modelPolicy: input.modelPolicy ?? input.model_policy, actor, now, database })
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshGroupAccountStatsAfterWrite()
  const created = createdGrantId ? findResourceAuthorization(createdGrantId, access) : undefined
  if (created) return created
  const fallback = listResourceAuthorizations({ resourceType, resourceId, granteeSystemAccountId: granteeType === 'system_account' ? granteeId : undefined, teamId: granteeType === 'team' ? granteeId : undefined, status: 'all' }, access)[0]
  if (!fallback) throw new Error('创建资源授权失败')
  return fallback
}

export function revokeResourceAuthorization(authorizationId: string, input: Record<string, unknown> = {}, access?: AccessScope): ResourceAuthorizationSummary | undefined {
  const database = getDatabase()
  const grant = database.prepare('SELECT * FROM resource_authorization_grants WHERE id = ?').get(authorizationId) as unknown as ResourceAuthorizationGrantRow | undefined
  if (!grant || !canManageResourceOwner(grant.resource_owner_system_account_id, access)) return undefined
  const now = nowIso()
  const actor = currentSystemAccountId(access)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    revokeResourceAuthorizationGrant(grant, actor, database, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshGroupAccountStatsAfterWrite()
  return findResourceAuthorization(authorizationId, access)
}

export function updateResourceAuthorization(authorizationId: string, input: Record<string, unknown> = {}, access?: AccessScope): ResourceAuthorizationSummary | undefined {
  expireDueResourceAuthorizations()
  const database = getDatabase()
  const grant = database.prepare('SELECT * FROM resource_authorization_grants WHERE id = ?').get(authorizationId) as unknown as ResourceAuthorizationGrantRow | undefined
  if (!grant || !canManageResourceOwner(grant.resource_owner_system_account_id, access)) return undefined
  const now = nowIso()
  const hasExpiresAtInput = Object.prototype.hasOwnProperty.call(input, 'expiresAt')
    || Object.prototype.hasOwnProperty.call(input, 'expires_at')
  const hasLimitsInput = Object.prototype.hasOwnProperty.call(input, 'limits')
  const nextExpiresAt = hasExpiresAtInput
    ? optionalNullableServerDateTimeIso(input.expiresAt ?? input.expires_at)
    : grant.expires_at
  const nextLimits = hasLimitsInput
    ? requestQuotaLimitsJson(normalizeRequestQuotaLimits(input.limits))
    : grant.limits_json
  const rawStatus = optionalString(input.status)
  const requestedStatus = rawStatus === 'active' || rawStatus === 'paused' || rawStatus === 'expired' || rawStatus === 'revoked'
    ? rawStatus
    : undefined
  if (grant.status === 'revoked' && requestedStatus === 'active') {
    throw new Error('已回收授权不能直接恢复，请重新新增授权')
  }
  if (grant.status === 'expired' && requestedStatus === 'active' && !hasExpiresAtInput) {
    throw new Error('到期授权恢复时请同时调整过期时间')
  }
  const expiredByTime = isResourceAuthorizationExpired(nextExpiresAt)
  const nextStatus: AuthorizationStatus = expiredByTime
    ? 'expired'
    : requestedStatus === 'active' || requestedStatus === 'paused'
      ? requestedStatus
      : grant.status === 'expired' && hasExpiresAtInput
        ? 'active'
        : grant.status === 'paused'
          ? 'paused'
        : grant.status
  const nextRevokedAt = nextStatus === 'active' || nextStatus === 'paused' ? null : grant.revoked_at ?? now
  const nextRevokedBy = nextStatus === 'active' || nextStatus === 'paused' ? null : grant.revoked_by ?? currentSystemAccountId(access)

  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database
      .prepare(`
        UPDATE resource_authorization_grants
        SET status = ?,
            expires_at = ?,
            revoked_by = ?,
            revoked_at = ?,
            limits_json = ?,
            updated_at = ?
        WHERE id = ?
      `)
      .run(nextStatus, nextExpiresAt, nextRevokedBy, nextRevokedAt, nextLimits, now, authorizationId)
    syncResourceAuthorizationGrantRuntime({ ...grant, status: nextStatus, expires_at: nextExpiresAt, limits_json: nextLimits, revoked_by: nextRevokedBy, revoked_at: nextRevokedAt, updated_at: now }, currentSystemAccountId(access), database, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  cleanupInactiveAuthorizationBindings(database)
  return findResourceAuthorization(authorizationId, access)
}

export function getResourceAuthorizationUsage(authorizationId: string, access?: AccessScope, options: ResourceAuthorizationUsageOptions = {}): ResourceAuthorizationSummary | undefined {
  const authorization = findResourceAuthorization(authorizationId, access, { includeUsage: false })
  if (!authorization) return undefined
  const range = options.range ?? normalizeAccountUsageStatsRange({}, usageStatsTimezone())
  const detail = loadResourceAuthorizationUsageDetail(authorization, range, options)
  return {
    ...authorization,
    usage: detail.usage,
    lastUsedAt: detail.usage.lastUsedAt,
    usageBySystemAccount: detail.usageBySystemAccount,
    usageBySystemAccountTotal: detail.usageBySystemAccountTotal,
    usageBySystemAccountPage: detail.usageBySystemAccountPage,
    usageBySystemAccountPageSize: detail.usageBySystemAccountPageSize,
    usageBySystemAccountHasMore: detail.usageBySystemAccountHasMore,
    usageRange: range
  }
}

function systemTeamSummaryFromRow(row: SystemTeamRow, members: SystemTeamMemberSummary[], _access?: AccessScope): SystemTeamSummary {
  return { id: row.id, name: row.name, description: row.description ?? undefined, status: row.status, memberCount: members.length, activeMemberCount: members.filter((member) => member.status === 'active').length, members, createdBy: row.created_by, createdAt: row.created_at, updatedAt: row.updated_at }
}

function systemTeamPrincipalSummaryFromRow(row: SystemTeamRow): SystemTeamPrincipalSummary {
  return {
    id: row.id,
    name: row.name,
    status: row.status
  }
}

function listSystemTeamMembersForTeamIds(teamIds: string[], activeOnly = false): Map<string, SystemTeamMemberSummary[]> {
  const ids = [...new Set(teamIds)].filter(Boolean)
  if (!ids.length) return new Map()
  const statusClause = activeOnly ? " AND system_team_members.status = 'active'" : ''
  const rows: Array<SystemTeamMemberRow & { display_name?: string; username?: string }> = []
  const database = getDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...database.prepare(`SELECT system_team_members.*, system_accounts.display_name, system_accounts.username FROM system_team_members INNER JOIN system_accounts ON system_accounts.id = system_team_members.system_account_id WHERE system_team_members.team_id IN (${sqlPlaceholders(chunk.length)})${statusClause} ORDER BY system_team_members.status ASC, system_team_members.joined_at ASC, system_team_members.id ASC`).all(...chunk) as unknown as Array<SystemTeamMemberRow & { display_name?: string; username?: string }>)
  }
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

function resourceOwnerSystemAccountId(resourceType: ResourceAuthorizationResourceType, resourceId: string): string | undefined {
  return resourceType === 'account' ? accountSystemAccountId(resourceId) : groupOwnerAndProvider(resourceId)?.systemAccountId
}

function loadResourceAuthorizationUsageDetail(
  authorization: ResourceAuthorizationSummary,
  range: AccountUsageStatsRange,
  options: ResourceAuthorizationUsageOptions = {}
): {
  usage: AccountUsageSummary
  usageBySystemAccount: ResourceAuthorizationUsageDetail[]
  usageBySystemAccountTotal: number
  usageBySystemAccountPage: number
  usageBySystemAccountPageSize: number
  usageBySystemAccountHasMore: boolean
} {
  const pageOptions = normalizeResourceAuthorizationUsagePageOptions(options)
  if (authorization.granteeType === 'team') {
    return loadResourceAuthorizationGrantUsageDetailForTeam(authorization, range, pageOptions)
  }
  const granteeSystemAccountId = authorization.granteeSystemAccountId
  if (!granteeSystemAccountId) {
    return emptyResourceAuthorizationUsageDetailPage(pageOptions)
  }
  const runtime = getDatabase().prepare(`
    SELECT *
    FROM resource_authorizations
    WHERE resource_type = ?
      AND resource_id = ?
      AND grantee_system_account_id = ?
    LIMIT 1
  `).get(authorization.resourceType, authorization.resourceId, granteeSystemAccountId) as unknown as ResourceAuthorizationRow | undefined
  if (!runtime) {
    return emptyResourceAuthorizationUsageDetailPage(pageOptions)
  }
  const scopeType = authorization.resourceType === 'account' ? 'account_authorization' : 'group_authorization'
  const rangeUsage = loadUsageRangeSummaryForScope({
    systemAccountId: authorization.resourceOwnerSystemAccountId,
    scopeType,
    scopeId: runtime.id,
    range
  })
  const account = loadSystemAccountsByIds([granteeSystemAccountId]).get(granteeSystemAccountId)
  const usageBySystemAccount: ResourceAuthorizationUsageDetail[] = [{
    systemAccountId: granteeSystemAccountId,
    systemAccountName: account?.displayName ?? account?.username ?? authorization.granteeSystemAccountName,
    username: account?.username ?? authorization.granteeUsername,
    ...rangeUsage,
    rangeUsage
  }]

  return {
    usage: rangeUsage,
    usageBySystemAccount: pageOptions.page === 1 ? usageBySystemAccount.sort((left, right) => {
      const leftTime = left.lastUsedAt ? Date.parse(left.lastUsedAt) : 0
      const rightTime = right.lastUsedAt ? Date.parse(right.lastUsedAt) : 0
      if (rightTime !== leftTime) {
        return rightTime - leftTime
      }
      return left.systemAccountId.localeCompare(right.systemAccountId)
    }) : [],
    usageBySystemAccountTotal: 1,
    usageBySystemAccountPage: pageOptions.page,
    usageBySystemAccountPageSize: pageOptions.pageSize,
    usageBySystemAccountHasMore: false
  }
}

function loadResourceAuthorizationGrantUsageDetailForTeam(
  authorization: ResourceAuthorizationSummary,
  range: AccountUsageStatsRange,
  pageOptions: { page: number; pageSize: number }
): {
  usage: AccountUsageSummary
  usageBySystemAccount: ResourceAuthorizationUsageDetail[]
  usageBySystemAccountTotal: number
  usageBySystemAccountPage: number
  usageBySystemAccountPageSize: number
  usageBySystemAccountHasMore: boolean
} {
  const teamId = authorization.granteeTeamId
  if (!teamId) {
    return emptyResourceAuthorizationUsageDetailPage(pageOptions)
  }
  const database = getDatabase()
  const rows = database.prepare(`
    SELECT DISTINCT ra.*
    FROM resource_authorizations ra
    INNER JOIN resource_authorization_sources ras
      ON ras.authorization_id = ra.id
      AND ras.source_type = 'team'
      AND ras.source_team_id = ?
    WHERE ra.resource_type = ?
      AND ra.resource_id = ?
      AND ra.resource_owner_system_account_id = ?
    ORDER BY ra.created_at ASC, ra.id ASC
    LIMIT ? OFFSET ?
  `).all(
    teamId,
    authorization.resourceType,
    authorization.resourceId,
    authorization.resourceOwnerSystemAccountId,
    pageOptions.pageSize + 1,
    (pageOptions.page - 1) * pageOptions.pageSize
  ) as unknown as ResourceAuthorizationRow[]
  const pageRows = takePageRows(rows, pageOptions.pageSize)
  const usageBySystemAccount = buildRuntimeAuthorizationUsageDetails(authorization, pageRows.rows, range)
  const scopeType = authorization.resourceType === 'account' ? 'account_authorization_team' : 'group_authorization_team'
  const rangeUsage = loadUsageRangeSummaryForScope({
    systemAccountId: authorization.resourceOwnerSystemAccountId,
    scopeType,
    scopeId: `${authorization.resourceId}:${teamId}`,
    range
  })
  return {
    usage: rangeUsage,
    usageBySystemAccount: usageBySystemAccount.sort((left, right) => {
      const leftTime = left.lastUsedAt ? Date.parse(left.lastUsedAt) : 0
      const rightTime = right.lastUsedAt ? Date.parse(right.lastUsedAt) : 0
      if (rightTime !== leftTime) {
        return rightTime - leftTime
      }
      return left.systemAccountId.localeCompare(right.systemAccountId)
    }),
    usageBySystemAccountTotal: compatiblePagedTotal(pageOptions.page, pageOptions.pageSize, usageBySystemAccount.length, pageRows.hasMore),
    usageBySystemAccountPage: pageOptions.page,
    usageBySystemAccountPageSize: pageOptions.pageSize,
    usageBySystemAccountHasMore: pageRows.hasMore
  }
}

function buildRuntimeAuthorizationUsageDetails(
  authorization: ResourceAuthorizationSummary,
  rows: ResourceAuthorizationRow[],
  range: AccountUsageStatsRange
): ResourceAuthorizationUsageDetail[] {
  if (!rows.length) return []
  const scopes = rows.map((row) => usageScope(row.id, authorization.resourceOwnerSystemAccountId, row.id))
  const usageByAuthorization = authorization.resourceType === 'account'
    ? loadAccountAuthorizationUsageSummaries(scopes, range)
    : loadGroupAuthorizationUsageSummaries(scopes, range)
  const accounts = loadSystemAccountsByIds(rows.map((row) => row.grantee_system_account_id ?? ''))
  return rows.flatMap((row) => {
    const systemAccountId = row.grantee_system_account_id
    if (!systemAccountId) return []
    const account = accounts.get(systemAccountId)
    const rangeUsage = usageByAuthorization.get(row.id) ?? emptyAccountUsageSummary()
    return [{
      systemAccountId,
      systemAccountName: account?.displayName ?? account?.username ?? systemAccountId,
      username: account?.username,
      ...rangeUsage,
      rangeUsage
    }]
  })
}

function normalizeResourceAuthorizationUsagePageOptions(options: ResourceAuthorizationUsageOptions): { page: number; pageSize: number } {
  const page = typeof options.page === 'number' && Number.isInteger(options.page) ? Math.max(1, options.page) : 1
  const pageSize = typeof options.pageSize === 'number' && Number.isInteger(options.pageSize)
    ? Math.max(1, options.pageSize)
    : defaultResourceAuthorizationUsageDetailPageSize
  return { page, pageSize }
}

function emptyResourceAuthorizationUsageDetailPage(pageOptions: { page: number; pageSize: number }) {
  return {
    usage: emptyAccountUsageSummary(),
    usageBySystemAccount: [],
    usageBySystemAccountTotal: 0,
    usageBySystemAccountPage: pageOptions.page,
    usageBySystemAccountPageSize: pageOptions.pageSize,
    usageBySystemAccountHasMore: false
  }
}
