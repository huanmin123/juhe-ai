import type { DatabaseSync } from 'node:sqlite'

import type { AccountGroupBindStatus, AccountGroupOptionSummary, AccountOptionSummary, AccountStatus, AccountSummary, AccountTrafficMigrationSourceStatus, AccountUsageStatsOverview, AccountUsageStatsRange, AccountUsageSummary, AuthorizationStatus, GroupListResult, GroupOptionSummary, GroupSchedulingPolicy, GroupSummary, GroupType, ResourceAuthorizationListResult, ResourceAuthorizationResourceType, ResourceAuthorizationSourceStatus, ResourceAuthorizationSourceType, ResourceAuthorizationSummary, ResourceAuthorizationUsageDetail, ResourcePermissions, SystemAccountPrincipalSummary, SystemTeamListResult, SystemTeamMemberSummary, SystemTeamPrincipalSummary, SystemTeamSummary } from '../domain/types.js'
export type { GroupOptionSummary } from '../domain/types.js'
import { groupSchedulingPolicyJson, normalizeGroupType, parseGroupSchedulingPolicyJson } from '../domain/group-scheduling.js'
import { listProviderModelPricing } from '../modules/model-pricing/model-pricing.service.js'
import { loadAccountCurrentConcurrencyByIds, sumAccountCurrentConcurrency } from '../shared/account-concurrency.js'
import { notifyAuthorizationQuotaCacheInvalidation, notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { buildSystemAccountScopeClause, canAccessAll, currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, scopedSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { accountStatusFilterValues, normalizeAccountListOptions, normalizeAccountOptionListOptions, type AccountListOptions } from './account-list-options.js'
import { cleanupDeletedAccountDetachedStats, type DeletedAccountRecordCleanupTarget } from './account-record-cleanup.js'
import { loadSupportedModelsByAccountIds, normalizeAccountSupportedModelsInput, replaceAccountSupportedModels } from './account-supported-models.repository.js'
import { accountCredentialsForList, findAccountRowForAccess, hydrateAccountRowsFromRecordDatabase, listAccountRowsForAccess, listAccountRowsPageForAccess, loadAccountAuthorizationUsageSummaries } from './account-read.repository.js'
import {
  getAccountUsageStatsOverview as buildAccountUsageStatsOverview,
  getAccountUsageStatsOverviewPageFromWindows as buildAccountUsageStatsOverviewPageFromWindows
} from './account-usage.repository.js'
import { updateAccountUsageSnapshotRefreshState, upsertAccountUsageSnapshot } from './account-usage-snapshot.repository.js'
import { createApiKeyRecord, deleteApiKey, findApiKeySummary, listApiKeys, listApiKeysPage, updateApiKey } from './api-key.repository.js'
import { clearResourceAuthorizationLookupCaches, loadResourceAuthorizationSourcesByAuthorizationIds, loadResourceAuthorizationStatsByResourceIds } from './authorization-read-loaders.js'
import { decryptJson, encryptJson, hashSecret, maskSecret } from './crypto.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatabase, getStatsDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { defaultGroupIdForSystemAccount } from './default-group.repository.js'
import { listErrorPolicies } from './error-policy.repository.js'
import { emptyGroupAccountStats, groupAccountStatsFromRow } from './group-account-stats.mapper.js'
import { findGroupRowForAccess, listGroupRowsForAccess, listGroupRowsPageForAccess, loadGroupAuthorizationUsageSummaries, type GroupListOptions } from './group-read.repository.js'
import { invalidateGroupAccountIdsCache, loadGroupAccountIdsByGroupIds, loadGroupAccountStatsByGroupIds } from './group-read-loaders.js'
import { loadOpenAICodexUsageSnapshotsByAccountIds } from './oauth-usage-loaders.js'
import { listProviders, providerPassthroughEnabled } from './provider.repository.js'
import { resolveEnabledProxyProfileId } from './proxy.repository.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { isRequestQuotaExceeded, loadRequestQuotaCostsBatch, requestQuotaCostKey, type RequestQuotaCostInput } from './request-quota-checker.js'
import {
  accountSystemAccountId,
  activeAccountAuthorization,
  activeGroupAuthorization,
  activeResourceAuthorization,
  canManageResourceOwner,
  groupOwnerAndProvider,
  isResourceAuthorizationExpired,
  resourceAuthorizationSelectColumns,
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
import {
  invalidateAccountLookupCache,
  invalidateGroupLookupCache,
  invalidateSystemAccountTeamMembershipLookupCache,
  invalidateSystemTeamLookupCache,
  loadSystemAccountNameMapByIds,
  loadSystemAccountPrincipalMapByIds
} from './repository-lookups.js'
import { hasEnabledRequestQuotaLimit, normalizeRequestQuotaLimits, parseRequestQuotaLimitsJson, requestQuotaLimitsJson } from './request-quota-limits.js'
import type { AccountFailureRow, AccountListRow, AccountRow, GroupListRow, ResourceAuthorizationGrantRow, ResourceAuthorizationRow, ResourceAuthorizationSourceRow, SystemTeamMemberRow, SystemTeamRow } from './repository-row-types.js'
import { getSettings } from './settings.repository.js'
import { systemAccountPrincipalSummaryFromRow } from './system-account-mappers.js'
import type { SystemAccountRow } from './system-account-mappers.js'
import { findSystemAccountById } from './system-accounts.repository.js'
import { markAllGroupAccountStatsDirty, markGroupAccountStatsDirty, markGroupAccountStatsDirtyByAccountIds } from './usage-stats.repository.js'
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
const temporaryUnavailableInitialBackoffSeconds = 3
const temporaryUnavailableFastThresholdSeconds = 60
const temporaryUnavailableBackoffMultiplier = 2
type AccountOptionFilterValue = string | number

interface AccountOptionRow {
  id: string
  system_account_id: string
  provider_code: string
  name: string
  type: string
  status: AccountStatus
  account_expires_at: string | null
  priority: number
  created_at: string
  access_type: 'owner' | 'authorized'
  authorization_id: string | null
  authorization_status: AuthorizationStatus | null
}

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
  cleanupDeletedAccountDetachedStats,
  cleanupDeletedAccountRelatedRecordData,
  cleanupPendingDeletedAccountRecordTargets,
  listDeletedAccountRecordCleanupTargets,
  registerDeletedAccountRecordCleanupTarget,
  type DeletedAccountDetachedStatsCleanupTarget,
  type DeletedAccountRecordCleanupResult,
  type DeletedAccountRecordCleanupTarget,
  type PendingDeletedAccountRecordCleanupSummary
} from './account-record-cleanup.js'
export {
  cleanupDeletedApiKeyRelatedRecordData,
  cleanupPendingDeletedApiKeyRecordTargets,
  getDeletedApiKeyRecordCleanupQueueSummary,
  listDeletedApiKeyRecordCleanupQueueTargets,
  listDeletedApiKeyRecordCleanupTargets,
  registerDeletedApiKeyRecordCleanupTarget,
  type DeletedApiKeyRecordCleanupQueueSummary,
  type DeletedApiKeyRecordCleanupQueueTarget,
  type DeletedApiKeyRecordCleanupResult,
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
  listSystemAccountsPage,
  revokeAllSessionsForAccount,
  revokeSession,
  touchSession,
  updateSystemAccount,
  updateSystemAccountLastLogin,
  verifySystemAccountCredentials,
  type SessionWithAccount,
  type SystemAccountListOptions,
  type SystemAccountListResult
} from './system-accounts.repository.js'
export {
  createProxy,
  deleteProxy,
  findProxy,
  getProxyTestConfig,
  listEnabledProxyTestConfigs,
  listProxyOptions,
  listProxies,
  listProxiesPage,
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
  type ProxyProfileListOptions,
  type ProxyProfileListResult,
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
  findActiveGatewayApiKeyById,
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
  type UsageRecordSummary,
  type UsageRecordTrafficSource
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
  type AuditPayloadPartType,
  type AuditTrafficSource
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
  cleanupRuntimeLogFileCursorsBefore,
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
  cleanupModelCheckRunsBefore,
  cleanupProcessedUsageRecordsBefore,
  cleanupProcessedUsageRecordsBeforeWithResult,
  cleanupSystemMetricsBefore,
  cleanupUsageStatsBucketsBefore,
  type ModelCheckRetentionCleanupResult,
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
  createModelCheckItems,
  createModelCheckRun,
  finishModelCheckRun,
  getModelCheckRunDetail,
  listModelCheckRuns,
  type ModelCheckItemCreateInput,
  type ModelCheckRunCreateInput,
  type ModelCheckRunFinishInput,
  type ModelCheckRunListOptions
} from './model-checks.repository.js'
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
    groupName: row.group_name ?? '',
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
    groupName: row.bound_group_name ?? '',
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

function accountDispatchUnavailableMessage(account: AccountSummary, options: { requireAuthorizedBinding?: boolean } = {}): string | undefined {
  if (account.permissions?.canUse === false) return '当前账户无可用权限'
  if (account.accessType === 'authorized') {
    if (options.requireAuthorizedBinding && !account.boundGroupId) return '授权账户需要先绑定到你的分组'
    if (account.groupBindStatus === 'authorization_unavailable') return '当前分组绑定的授权已失效，请重新绑定分组或联系授权人'
    if (account.authorizationQuotaExceeded) return '授权额度已用完，当前账户不能调用'
  }
  if (isAccountExpired(account.accountExpiresAt) || account.lastErrorCode === 'account_expired') return '账户已到期，当前不可用'
  if (account.status === 'disabled') return account.accessType === 'authorized' && account.localStatus === 'disabled' ? '当前分组已停用该授权账户，当前不可用' : '账户已停用，当前不可用'
  if (account.status === 'error') return '账户处于异常状态，当前不可用'
  if (isCoolingAccountStatus(account.status) || !account.schedulable || isLaterIso(account.cooldownUntil, nowIso())) return '账户暂时不可调用，恢复前不会参与调度'
  return undefined
}

export function accountTestUnavailableMessage(account: AccountSummary): string | undefined {
  if (account.accessType !== 'authorized') return undefined
  if (account.permissions?.canUse === false) return '当前账户无可用权限'
  if (!account.boundGroupId) return '授权账户需要先绑定到你的分组'
  if (account.groupBindStatus === 'authorization_unavailable') return '当前分组绑定的授权已失效，请重新绑定分组或联系授权人'
  if (account.authorizationQuotaExceeded) return '授权额度已用完，当前账户不能调用'
  if (isAccountExpired(account.accountExpiresAt) || account.lastErrorCode === 'account_expired') return '账户已到期，当前不可用'
  if (account.status === 'error') return '账户处于异常状态，当前不可用'
  if (isCoolingAccountStatus(account.status) || (!account.schedulable && account.status !== 'disabled') || isLaterIso(account.cooldownUntil, nowIso())) {
    return '账户暂时不可调用，恢复前不会参与调度'
  }
  return undefined
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
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = NULL,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
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
    refreshGroupAccountStatsAfterWrite({ all: true, reason: 'account_expired' })
    invalidateGatewayRuntimeAfterBusinessWrite('account_expired')
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

function optionalNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function positiveOptionalInteger(value: unknown): number | undefined {
  const numeric = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : Number.NaN
  if (!Number.isFinite(numeric) || numeric <= 0) {
    return undefined
  }
  return Math.trunc(numeric)
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

function normalizeAccountSupportedModelsForProvider(value: unknown, providerCode: string): string[] | undefined {
  const models = normalizeAccountSupportedModelsInput(value)
  if (!models?.length) return models

  const providerModels = new Set(listProviderModelPricing(providerCode).map((item) => item.model))
  const invalidModels = models.filter((model) => !providerModels.has(model))
  if (invalidModels.length > 0) {
    throw new Error(`账户支持模型不在供应商模型目录中：${invalidModels.slice(0, 5).join('、')}`)
  }
  return models
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

function defaultTemporaryUnschedulableMaxPauseSeconds(): number {
  return defaultTemporaryUnschedulableMinutes() * 60
}

function initialTemporaryUnavailableCooldownUntil(nowMs = Date.now()): string {
  return new Date(nowMs + temporaryUnavailableInitialBackoffSeconds * 1000).toISOString()
}

function initialCooldownUntilForStatus(status: AccountStatus, nowMs = Date.now()): string | undefined {
  if (status === 'temporary_unavailable') {
    return initialTemporaryUnavailableCooldownUntil(nowMs)
  }
  if (status === 'rate_limited') {
    return new Date(nowMs + defaultTemporaryUnschedulableMinutes() * 60_000).toISOString()
  }
  return undefined
}

function cooldownRetestObservationStartedAtForStatus(status: AccountStatus, nowMs = Date.now()): string | undefined {
  return status === 'temporary_unavailable' ? new Date(nowMs).toISOString() : undefined
}

function refreshGroupAccountStatsAfterWrite(input: {
  groupIds?: Array<string | null | undefined>
  accountIds?: Array<string | null | undefined>
  all?: boolean
  reason?: string
} = {}): void {
  const reason = input.reason ?? 'business_write'
  if (input.all) {
    markAllGroupAccountStatsDirty(reason)
    return
  }
  if (input.groupIds?.length) {
    markGroupAccountStatsDirty(input.groupIds, reason)
  }
  if (input.accountIds?.length) {
    markGroupAccountStatsDirtyByAccountIds(input.accountIds, reason)
  }
  if (!input.groupIds?.length && !input.accountIds?.length) {
    markAllGroupAccountStatsDirty(reason)
  }
}

function invalidateGatewayRuntimeAfterBusinessWrite(reason: string): void {
  notifyGatewayRuntimeCacheInvalidation(reason)
}

function invalidateAuthorizationRuntimeAfterBusinessWrite(reason: string): void {
  notifyGatewayRuntimeCacheInvalidation(reason)
  notifyAuthorizationQuotaCacheInvalidation(reason)
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
  const rows = queryAccountOptionRowsForAccess(access, listOptions)
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

function accountOptionSummariesFromRows(rows: AccountOptionRow[], access: AccessScope | undefined, viewerSystemAccountId: string | undefined): AccountOptionSummary[] {
  const hasAuthorizedRows = rows.some((row) => row.access_type === 'authorized')
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const accountNames = shouldIncludeSystemAccountFields || hasAuthorizedRows ? loadSystemAccountNameMapByIds(rows.map((row) => row.system_account_id)) : new Map<string, string>()
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
      accountExpiresAt: row.account_expires_at ?? undefined,
      permissions: isAuthorizedView && row.system_account_id !== viewerSystemAccountId ? authorizedPermissions() : ownerPermissions()
    }
  })
}

function queryAccountOptionRowsForAccess(access: AccessScope | undefined, options: ReturnType<typeof normalizeAccountOptionListOptions>): AccountOptionRow[] {
  const database = getDatabase()
  const ownerSystemAccountId = manageableSystemAccountId(access)
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const limit = options.pageSize
  const offset = (options.page - 1) * options.pageSize
  const queryRows = (selectSql: string, params: AccountOptionFilterValue[]): AccountOptionRow[] => database
    .prepare(`
      SELECT *
      FROM (
        ${selectSql}
      ) account_option_rows
      ORDER BY CASE WHEN account_option_rows.access_type = 'authorized' THEN 0 ELSE account_option_rows.priority END ASC,
        account_option_rows.created_at ASC,
        account_option_rows.id ASC
      LIMIT ? OFFSET ?
    `)
    .all(...params, limit, offset) as unknown as AccountOptionRow[]

  if (!ownerSystemAccountId && canAccessAll(access)) {
    const filters = buildAccountOptionFilters(options, 'accounts.system_account_id')
    return queryRows(`
      SELECT ${accountOptionSelectColumns()}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
      FROM accounts
      WHERE 1 = 1${filters.clause}
    `, filters.params)
  }
  if (!viewerSystemAccountId) {
    const filters = buildAccountOptionFilters(options, 'accounts.system_account_id')
    return queryRows(`
      SELECT ${accountOptionSelectColumns()}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
      FROM accounts
      WHERE 1 = 1${filters.clause}
    `, filters.params)
  }

  const ownerId = ownerSystemAccountId ?? viewerSystemAccountId
  const ownerFilters = buildAccountOptionFilters(options, 'accounts.system_account_id')
  const authorizedFilters = buildAccountOptionFilters(options, '?', [viewerSystemAccountId])
  return queryRows(`
    SELECT ${accountOptionSelectColumns()}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
    FROM accounts
    WHERE accounts.system_account_id = ?${ownerFilters.clause}
    UNION ALL
    SELECT ${accountOptionSelectColumns()}, 'authorized' AS access_type, ra.id AS authorization_id, ra.status AS authorization_status
    FROM resource_authorizations ra
    INNER JOIN accounts ON accounts.id = ra.resource_id
    WHERE ra.resource_type = 'account'
      AND ra.grantee_system_account_id = ?
      AND ra.status = 'active'
      AND (ra.expires_at IS NULL OR ra.expires_at > ?)
      AND accounts.system_account_id <> ?${authorizedFilters.clause}
  `, [ownerId, ...ownerFilters.params, viewerSystemAccountId, nowIso(), ownerId, ...authorizedFilters.params])
}

function accountOptionSelectColumns(): string {
  return [
    'accounts.id',
    'accounts.system_account_id',
    'accounts.provider_code',
    'accounts.name',
    'accounts.type',
    'accounts.status',
    'accounts.account_expires_at',
    'accounts.priority',
    'accounts.created_at'
  ].join(', ')
}

function buildAccountOptionFilters(
  options: ReturnType<typeof normalizeAccountOptionListOptions>,
  groupBindingSystemAccountExpression: string,
  groupBindingSystemAccountParams: string[] = []
): { clause: string; params: AccountOptionFilterValue[] } {
  const clauses: string[] = []
  const params: AccountOptionFilterValue[] = []
  if (options.ids.length) {
    clauses.push(`accounts.id IN (${sqlPlaceholders(options.ids.length)})`)
    params.push(...options.ids)
  }
  const keyword = options.keyword?.trim()
  if (keyword) {
    const keywordPrefix = `${escapeLikePrefix(keyword)}%`
    clauses.push(`(
      accounts.name COLLATE NOCASE = ?
      OR accounts.name LIKE ? ESCAPE '\\'
    )`)
    params.push(
      keyword,
      keywordPrefix
    )
  }
  const groupId = options.groupId?.trim()
  if (groupId) {
    clauses.push(`EXISTS (
      SELECT 1
      FROM group_accounts option_group_accounts
      WHERE option_group_accounts.account_id = accounts.id
        AND option_group_accounts.system_account_id = ${groupBindingSystemAccountExpression}
        AND option_group_accounts.group_id = ?
        AND option_group_accounts.enabled = 1
    )`)
    params.push(...groupBindingSystemAccountParams, groupId)
  }
  if (options.type && options.type !== 'all') {
    clauses.push('accounts.type = ?')
    params.push(options.type)
  }
  const statuses = accountStatusFilterValues(options.status)
  if (statuses.length === 1) {
    clauses.push('accounts.status = ?')
    params.push(statuses[0])
  } else if (statuses.length > 1) {
    clauses.push(`accounts.status IN (${statuses.map(() => '?').join(', ')})`)
    params.push(...statuses)
  }
  if (options.schedulable === 'enabled') {
    clauses.push("accounts.status = 'active' AND accounts.schedulable = 1 AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?)")
    params.push(nowIso())
  } else if (options.schedulable === 'disabled') {
    clauses.push("(accounts.status = 'disabled' OR accounts.schedulable <> 1)")
  } else if (options.schedulable === 'cooling') {
    clauses.push("(accounts.status IN ('rate_limited', 'temporary_unavailable') OR (accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until > ?))")
    params.push(nowIso())
  }
  return {
    clause: clauses.length ? ` AND ${clauses.join(' AND ')}` : '',
    params
  }
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
  const quotaExceededByAuthorization = loadAuthorizationQuotaExceededByAuthorizationId(rows)
  const sourcesByAuthorization = loadResourceAuthorizationSourcesByAuthorizationIds(rows.map((row) => row.authorization_id ?? ''))
  const oauthUsageByAccount = loadOpenAICodexUsageSnapshotsByAccountIds(rows.map((row) => row.id))
  const hasAuthorizedRows = rows.some((row) => row.access_type === 'authorized')
  const accountNames = includeSystemAccountFields(access) || hasAuthorizedRows ? loadSystemAccountNameMapByIds(rows.map((row) => row.system_account_id)) : new Map<string, string>()
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
      ? row.schedulable === 1 && row.status === 'active' && effectiveAuthorizedStatus === 'active'
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
      concurrencyLimit: row.concurrency_limit,
      currentConcurrency: currentConcurrencyByAccount.get(row.id) ?? 0,
      priority: isAuthorizedView ? 0 : row.priority,
      superPriorityEnabled: isAuthorizedView
        ? row.bound_group_local_super_priority_enabled === 1
        : row.super_priority_enabled === 1,
      fallbackEnabled: isAuthorizedView
        ? row.bound_group_local_fallback_enabled === 1
        : row.fallback_enabled === 1,
      supportedModels: [...(row.supported_models ?? [])],
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
      accountExpiresAt: row.account_expires_at ?? undefined,
      cooldownUntil: isAuthorizedView ? row.bound_group_local_cooldown_until ?? undefined : row.cooldown_until ?? undefined,
      lastErrorCode: isAuthorizedView ? (effectiveAuthorizedStatus === row.status ? row.last_error_code ?? undefined : undefined) : row.last_error_code ?? undefined,
      lastErrorMessage: isAuthorizedView ? row.bound_group_local_last_error_message ?? undefined : row.last_error_message ?? undefined,
      cooldownRetestFailureCount: isAuthorizedView ? 0 : Math.max(0, Number(row.cooldown_retest_failure_count ?? 0)),
      cooldownRetestObservationStartedAt: isAuthorizedView ? undefined : row.cooldown_retest_observation_started_at ?? undefined,
      cooldownRetestLastAt: isAuthorizedView ? undefined : row.cooldown_retest_last_at ?? undefined,
      cooldownRetestLastStatusCode: isAuthorizedView ? undefined : optionalNumber(row.cooldown_retest_last_status_code),
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
      authorizationExpiresAt: row.authorization_expires_at ?? undefined,
      authorizationLimits: parseRequestQuotaLimitsJson(row.authorization_limits_json),
      authorizationQuotaExceeded: row.authorization_id ? quotaExceededByAuthorization.get(row.authorization_id) : undefined,
      authorizationSources: row.authorization_id ? sanitizeAuthorizationSourcesForViewer(sourcesByAuthorization.get(row.authorization_id) ?? [], isAuthorizedView) : undefined,
      permissions: isAuthorizedView && row.system_account_id !== viewerSystemAccountId ? authorizedPermissions() : ownerPermissions(),
      authorizationUsageAvailable: !isAuthorizedView && authorizationStats.authorizationCount > 0 && canManageResourceOwner(row.system_account_id, access),
      authorizationCount: isAuthorizedView ? 0 : authorizationStats.authorizationCount,
      authorizationTeamCount: isAuthorizedView ? 0 : authorizationStats.authorizationTeamCount
    }
  })
}

function loadAuthorizationQuotaExceededByAuthorizationId(rows: AccountListRow[]): Map<string, boolean> {
  const now = new Date()
  const output = new Map<string, boolean>()
  const checks: Array<{
    authorizationId: string
    limits: ReturnType<typeof parseRequestQuotaLimitsJson>
    input: RequestQuotaCostInput
  }> = []
  const teamGrantLimitJsonByAuthorizationId = loadTeamAuthorizationGrantLimitJsonByAuthorizationId(rows)
  for (const row of rows) {
    if (!row.authorization_id) continue
    output.set(row.authorization_id, false)
    const limits = parseRequestQuotaLimitsJson(row.authorization_limits_json)
    if (hasEnabledRequestQuotaLimit(limits)) {
      checks.push({
        authorizationId: row.authorization_id,
        limits,
        input: {
          systemAccountId: row.system_account_id,
          scopeType: 'account_authorization',
          scopeId: row.authorization_id,
          now,
          hourlyWindowHours: limits.hourly?.hours
        }
      })
    }
    const teamId = row.authorization_effective_source_team_id
    if (!teamId) continue
    const teamLimits = parseRequestQuotaLimitsJson(teamGrantLimitJsonByAuthorizationId.get(row.authorization_id))
    if (!hasEnabledRequestQuotaLimit(teamLimits)) continue
    checks.push({
      authorizationId: row.authorization_id,
      limits: teamLimits,
      input: {
        systemAccountId: row.system_account_id,
        scopeType: 'account_authorization_team',
        scopeId: `${row.id}:${teamId}`,
        now,
        hourlyWindowHours: teamLimits.hourly?.hours
      }
    })
  }
  if (!checks.length) return output
  const costsByKey = loadRequestQuotaCostsBatch(getStatsDatabase(), checks.map((check) => check.input))
  for (const check of checks) {
    const costs = costsByKey.get(requestQuotaCostKey(check.input))
    if (costs && isRequestQuotaExceeded(check.limits, costs)) {
      output.set(check.authorizationId, true)
    }
  }
  return output
}

function loadTeamAuthorizationGrantLimitJsonByAuthorizationId(rows: AccountListRow[]): Map<string, string | null> {
  const ids = [...new Set(rows
    .filter((row) => row.authorization_id && row.authorization_effective_source_team_id)
    .map((row) => row.authorization_id as string))]
  if (!ids.length) return new Map()
  const output = new Map<string, string | null>()
  const database = getDatabase()
  const now = nowIso()
  for (const chunk of chunkValues(ids, 900)) {
    const teamRows = database.prepare(`
      SELECT ra.id AS authorization_id, grant_rows.limits_json
      FROM resource_authorizations ra
      INNER JOIN resource_authorization_grants grant_rows
        ON grant_rows.resource_type = ra.resource_type
        AND grant_rows.resource_id = ra.resource_id
        AND grant_rows.grantee_type = 'team'
        AND grant_rows.grantee_team_id = ra.effective_source_team_id
        AND grant_rows.status = 'active'
        AND (grant_rows.expires_at IS NULL OR grant_rows.expires_at > ?)
      WHERE ra.status = 'active'
        AND (ra.expires_at IS NULL OR ra.expires_at > ?)
        AND ra.effective_source_team_id IS NOT NULL
        AND ra.id IN (${sqlPlaceholders(chunk.length)})
    `).all(now, now, ...chunk) as unknown as Array<{ authorization_id?: string; limits_json?: string | null }>
    for (const row of teamRows) {
      if (row.authorization_id) {
        output.set(row.authorization_id, row.limits_json ?? null)
      }
    }
  }
  return output
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

export function getAccountUsageStatsOverviewPage(access?: AccessScope, options?: AccountListOptions & { range?: AccountUsageStatsRange; accountIds?: string[] }): AccountUsageStatsOverview {
  const listOptions = normalizeAccountListOptions(options)
  const defaultTrendAccountIds = loadAccountUsageDefaultTrendAccountIds(access)
  const range = options?.range ?? normalizeAccountUsageStatsRange({}, usageStatsTimezone())
  const overview = buildAccountUsageStatsOverviewPageFromWindows({
    access,
    range,
    page: listOptions.page,
    pageSize: listOptions.pageSize,
    keyword: listOptions.keyword,
    accountIds: options?.accountIds,
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
  const database = getStatsDatabase()
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
        AND status = 'temporary_unavailable'
        AND schedulable = 1
        AND cooldown_until IS NOT NULL
        AND cooldown_until <= ?
        AND (account_expires_at IS NULL OR account_expires_at > ?)
        AND EXISTS (
          SELECT 1
          FROM group_accounts
          WHERE group_accounts.account_id = accounts.id
            AND group_accounts.system_account_id = accounts.system_account_id
            AND group_accounts.enabled = 1
        )
      ORDER BY cooldown_until ASC, priority ASC, created_at ASC, id ASC
      LIMIT ?
    `)
    .all(nowIso(), nowIso(), Math.max(1, Math.min(Math.trunc(limit), 200))) as unknown as AccountListRow[]
  const accountNames = loadSystemAccountNameMapByIds(rows.map((row) => row.system_account_id))
  const currentConcurrencyByAccount = loadAccountCurrentConcurrencyByIds(rows.map((row) => row.id))
  const supportedModelsByAccountId = loadSupportedModelsByAccountIds(rows.map((row) => row.id))
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
      supportedModels: supportedModelsByAccountId.get(row.id) ?? [],
      proxyProfileId: row.proxy_profile_id ?? undefined,
      passthroughEnabled: row.passthrough_enabled === 1,
      errorPolicyId: row.error_policy_id ?? undefined,
      schedulable: row.schedulable === 1,
      accountExpiresAt: row.account_expires_at ?? undefined,
      cooldownUntil: row.cooldown_until ?? undefined,
      lastErrorCode: row.last_error_code ?? undefined,
      lastErrorMessage: row.last_error_message ?? undefined,
      cooldownRetestFailureCount: Math.max(0, Number(row.cooldown_retest_failure_count ?? 0)),
      cooldownRetestObservationStartedAt: row.cooldown_retest_observation_started_at ?? undefined,
      cooldownRetestLastAt: row.cooldown_retest_last_at ?? undefined,
      cooldownRetestLastStatusCode: optionalNumber(row.cooldown_retest_last_status_code),
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
  const nowMs = Date.now()
  const now = new Date(nowMs).toISOString()
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
  const supportedModels = normalizeAccountSupportedModelsForProvider(input.supportedModels ?? input.supported_models, providerCode) ?? []
  const initialStatus = normalizeAccountStatus(input.status, 'active')
  const expiredByPackage = isAccountExpired(accountExpiresAt)
  const nextStatus = expiredByPackage ? 'disabled' : initialStatus
  const initialCooldownUntil = initialCooldownUntilForStatus(initialStatus, nowMs)
  const initialObservationStartedAt = expiredByPackage ? undefined : cooldownRetestObservationStartedAtForStatus(initialStatus, nowMs)
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
    systemAccountName: includeSystemAccountFields(access) ? loadSystemAccountNameMapByIds([systemAccountId]).get(systemAccountId) : undefined,
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
    supportedModels,
    proxyProfileId,
    passthroughEnabled: providerPassthroughEnabled(provider),
    errorPolicyId: optionalString(input.errorPolicyId ?? input.error_policy_id),
    schedulable: expiredByPackage || isHardUnavailableAccountStatus(nextStatus) ? false : input.schedulable !== false,
    accountExpiresAt: accountExpiresAt ?? undefined,
    cooldownUntil: expiredByPackage ? undefined : initialCooldownUntil,
    lastErrorCode: undefined,
    lastErrorMessage: expiredByPackage ? '账户套餐已过期，已自动停用' : initialCooldownUntil ? '创建时设置为临时不可调用' : undefined,
    cooldownRetestFailureCount: 0,
    cooldownRetestObservationStartedAt: initialObservationStartedAt,
    cooldownRetestLastAt: undefined,
    cooldownRetestLastStatusCode: undefined,
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
          priority, super_priority_enabled, fallback_enabled, schedulable, notes, account_expires_at, cooldown_until, last_error_code, last_error_message,
          cooldown_retest_observation_started_at, stream_failure_count, stream_failure_window_started_at, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
        account.cooldownRetestObservationStartedAt ?? null,
        0,
        null,
        now,
        now
      )
    database
      .prepare('INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)')
      .run(systemAccountId, groupId, account.id, now, now)
    replaceAccountSupportedModels(account.id, providerCode, supportedModels)
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
  refreshGroupAccountStatsAfterWrite({ groupIds: [groupId], reason: 'account_created' })
  invalidateAccountLookupCache(account.id)
  invalidateGroupAccountIdsCache(groupId)
  invalidateGatewayRuntimeAfterBusinessWrite('account_created')

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
  const hasSupportedModelsInput = Object.prototype.hasOwnProperty.call(input, 'supportedModels')
    || Object.prototype.hasOwnProperty.call(input, 'supported_models')
  const nextSupportedModels = hasSupportedModelsInput
    ? normalizeAccountSupportedModelsForProvider(input.supportedModels ?? input.supported_models, current.providerCode) ?? []
    : current.supportedModels ?? []
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
  let nextCooldownRetestObservationStartedAt = current.cooldownRetestObservationStartedAt
  let clearCooldownRetestState = false
  if (hasStatusInput) {
    if (nextStatus === 'active') {
      nextCooldownUntil = undefined
      nextLastErrorCode = undefined
      nextLastErrorMessage = undefined
      nextCooldownRetestObservationStartedAt = undefined
      clearCooldownRetestState = true
    } else if (nextStatus === 'disabled' || nextStatus === 'error') {
      nextCooldownUntil = undefined
      nextCooldownRetestObservationStartedAt = undefined
      if (nextStatus === 'disabled') {
        nextLastErrorCode = undefined
        clearCooldownRetestState = true
      }
    } else if (isCoolingAccountStatus(nextStatus) && (nextStatus !== current.status || !nextCooldownUntil)) {
      const cooldownNowMs = Date.now()
      nextCooldownUntil = initialCooldownUntilForStatus(nextStatus, cooldownNowMs)
      nextCooldownRetestObservationStartedAt = cooldownRetestObservationStartedAtForStatus(nextStatus, cooldownNowMs)
      nextLastErrorCode = undefined
      nextLastErrorMessage = nextStatus === 'temporary_unavailable' ? '手动设置为临时不可调用' : '手动设置为限流中'
      clearCooldownRetestState = nextStatus === 'temporary_unavailable'
    }
  }
  if (expiredByPackage) {
    nextCooldownUntil = undefined
    nextLastErrorCode = undefined
    nextLastErrorMessage = '账户套餐已过期，已自动停用'
    nextCooldownRetestObservationStartedAt = undefined
    clearCooldownRetestState = true
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
    supportedModels: nextSupportedModels,
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
    cooldownRetestFailureCount: clearCooldownRetestState ? 0 : current.cooldownRetestFailureCount,
    cooldownRetestObservationStartedAt: nextCooldownRetestObservationStartedAt,
    cooldownRetestLastAt: clearCooldownRetestState ? undefined : current.cooldownRetestLastAt,
    cooldownRetestLastStatusCode: clearCooldownRetestState ? undefined : current.cooldownRetestLastStatusCode,
    lastUsedAt: current.lastUsedAt,
    usage: current.usage
  }

  assertAccountNameAvailable(systemAccountId, next.providerCode, next.name, id)
  const database = getDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const result = database
      .prepare(`
      UPDATE accounts
      SET name = ?, notes = ?, status = ?, credentials_encrypted = ?, credential_fingerprint = ?, credential_mask = ?,
            proxy_profile_id = ?, concurrency_limit = ?, passthrough_enabled = ?,
            error_policy_id = ?, priority = ?, super_priority_enabled = ?, fallback_enabled = ?, schedulable = ?, account_expires_at = ?, cooldown_until = ?, last_error_code = ?, last_error_message = ?,
            cooldown_retest_failure_count = ?, cooldown_retest_observation_started_at = ?, cooldown_retest_last_at = ?, cooldown_retest_last_status_code = ?, updated_at = ?
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
        next.cooldownRetestFailureCount ?? 0,
        next.cooldownRetestObservationStartedAt ?? null,
        next.cooldownRetestLastAt ?? null,
        next.cooldownRetestLastStatusCode ?? null,
        nowIso(),
        id,
        systemAccountId
    )
    if (Number(result.changes ?? 0) > 0 && hasSupportedModelsInput) {
      replaceAccountSupportedModels(id, next.providerCode, nextSupportedModels)
    }
    commitDatabaseTransaction(database, transactionStarted)
    if (Number(result.changes ?? 0) > 0) {
      refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_updated' })
      invalidateAccountLookupCache(id)
      invalidateGatewayRuntimeAfterBusinessWrite('account_updated')
    }
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
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
  return deleteAccountWithRelatedCleanup(id, access).deleted
}

export interface AccountDeleteResult {
  deleted: boolean
  cleanupTarget?: DeletedAccountRecordCleanupTarget
}

interface AccountDeleteRow {
  id: string
  system_account_id: string
}

interface AccountDeleteAuthorizationRow {
  id?: string | null
  effective_source_team_id?: string | null
}

interface AccountDeleteAuthorizationTeamSourceRow {
  source_team_id?: string | null
}

interface AccountDeleteAuthorizationGrantRow {
  grantee_team_id?: string | null
}

export function deleteAccountWithRelatedCleanup(id: string, access?: AccessScope): AccountDeleteResult {
  const scope = buildSystemAccountScopeClause(access)
  const database = getDatabase()
  const row = database
    .prepare(`SELECT id, system_account_id FROM accounts WHERE id = ?${scope.clause}`)
    .get(id, ...scope.params) as unknown as AccountDeleteRow | undefined
  if (!row) {
    return { deleted: false }
  }
  const cleanupTarget = buildDeletedAccountCleanupTarget(database, row)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const result = database.prepare('DELETE FROM accounts WHERE id = ? AND system_account_id = ?').run(row.id, row.system_account_id)
    if (Number(result.changes ?? 0) > 0) {
      database
        .prepare("DELETE FROM resource_authorization_grants WHERE resource_type = 'account' AND resource_id = ? AND resource_owner_system_account_id = ?")
        .run(row.id, row.system_account_id)
      database
        .prepare("DELETE FROM resource_authorizations WHERE resource_type = 'account' AND resource_id = ? AND resource_owner_system_account_id = ?")
        .run(row.id, row.system_account_id)
    }
    commitDatabaseTransaction(database, transactionStarted)
    if (Number(result.changes ?? 0) > 0) {
      refreshGroupAccountStatsAfterWrite({ all: true, reason: 'account_deleted' })
      invalidateAccountLookupCache(id)
      invalidateGroupAccountIdsCache()
      clearResourceAuthorizationLookupCaches()
      invalidateGatewayRuntimeAfterBusinessWrite('account_deleted')
      invalidateAuthorizationRuntimeAfterBusinessWrite('account_deleted')
    }
    return {
      deleted: Number(result.changes ?? 0) > 0,
      cleanupTarget: Number(result.changes ?? 0) > 0 ? cleanupTarget : undefined
    }
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function buildDeletedAccountCleanupTarget(database: DatabaseSync, row: AccountDeleteRow): DeletedAccountRecordCleanupTarget {
  const authorizationRows = database
    .prepare(`
      SELECT id, effective_source_team_id
      FROM resource_authorizations
      WHERE resource_type = 'account'
        AND resource_id = ?
        AND resource_owner_system_account_id = ?
    `)
    .all(row.id, row.system_account_id) as unknown as AccountDeleteAuthorizationRow[]
  const teamSourceRows = database
    .prepare(`
      SELECT DISTINCT ras.source_team_id
      FROM resource_authorization_sources ras
      INNER JOIN resource_authorizations ra ON ra.id = ras.authorization_id
      WHERE ra.resource_type = 'account'
        AND ra.resource_id = ?
        AND ra.resource_owner_system_account_id = ?
        AND ras.source_team_id IS NOT NULL
    `)
    .all(row.id, row.system_account_id) as unknown as AccountDeleteAuthorizationTeamSourceRow[]
  const grantRows = database
    .prepare(`
      SELECT grantee_team_id
      FROM resource_authorization_grants
      WHERE resource_type = 'account'
        AND resource_id = ?
        AND resource_owner_system_account_id = ?
        AND grantee_type = 'team'
        AND grantee_team_id IS NOT NULL
    `)
    .all(row.id, row.system_account_id) as unknown as AccountDeleteAuthorizationGrantRow[]
  const authorizationIds = uniqueAccountDeleteValues(authorizationRows.map((authorization) => String(authorization.id ?? '')))
  const teamIds = uniqueAccountDeleteValues([
    ...authorizationRows.map((authorization) => String(authorization.effective_source_team_id ?? '')),
    ...teamSourceRows.map((source) => String(source.source_team_id ?? '')),
    ...grantRows.map((grant) => String(grant.grantee_team_id ?? ''))
  ])
  return {
    accountId: row.id,
    systemAccountId: row.system_account_id,
    authorizationIds,
    teamScopeIds: teamIds.map((teamId) => `${row.id}:${teamId}`)
  }
}

function uniqueAccountDeleteValues(values: string[]): string[] {
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))]
}

interface ClearAccountFailureStateOptions {
  allowErrorRestore?: boolean
}

export interface AccountFailureStateClearResult {
  account?: AccountSummary
  changed: boolean
}

export function clearAccountFailureState(
  id: string,
  access?: AccessScope,
  options: ClearAccountFailureStateOptions = {}
): AccountSummary | undefined {
  return clearAccountFailureStateResult(id, access, options).account
}

export function clearAccountFailureStateResult(
  id: string,
  access?: AccessScope,
  options: ClearAccountFailureStateOptions = {}
): AccountFailureStateClearResult {
  const current = findAccountSummary(id, access)
  if (!current) {
    return { changed: false }
  }
  const ownerSystemAccountId = accountSystemAccountId(id)
  if (ownerSystemAccountId && !canManageResourceOwner(ownerSystemAccountId, access)) {
    return { changed: false }
  }
  const expiredByPackage = isAccountExpired(current.accountExpiresAt)
  if (current.status === 'disabled' && !expiredByPackage) {
    return { account: current, changed: false }
  }
  if (current.status === 'error' && options.allowErrorRestore === false) {
    return { account: current, changed: false }
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
            cooldown_retest_failure_count = 0,
            cooldown_retest_observation_started_at = NULL,
            cooldown_retest_last_at = NULL,
            cooldown_retest_last_status_code = NULL,
            stream_failure_count = 0,
            stream_failure_window_started_at = NULL,
            updated_at = ?
        WHERE id = ?
      `)
      .run('账户套餐已过期，已自动停用', nowIso(), id)
    const changed = Number(result.changes ?? 0) > 0
    if (changed) {
      refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_expired' })
      invalidateGatewayRuntimeAfterBusinessWrite('account_expired')
    }
    return { account: findAccountSummary(id, access), changed }
  }

  const result = getDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'active',
          schedulable = 1,
          cooldown_until = NULL,
          last_error_code = NULL,
          last_error_message = NULL,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = NULL,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
        AND status <> 'disabled'
        AND (? = 1 OR status <> 'error')
        AND (
          status <> 'active'
          OR schedulable <> 1
          OR cooldown_until IS NOT NULL
          OR last_error_code IS NOT NULL
          OR last_error_message IS NOT NULL
          OR cooldown_retest_failure_count > 0
          OR cooldown_retest_observation_started_at IS NOT NULL
          OR cooldown_retest_last_at IS NOT NULL
          OR cooldown_retest_last_status_code IS NOT NULL
          OR stream_failure_count > 0
          OR stream_failure_window_started_at IS NOT NULL
        )
    `)
    .run(nowIso(), id, options.allowErrorRestore === false ? 0 : 1)
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_restored' })
    invalidateAccountLookupCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('account_restored')
  }

  return { account: findAccountSummary(id, access), changed }
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
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    invalidateGatewayRuntimeAfterBusinessWrite('account_stream_failure_cleared')
  }
  return changed
}

export interface CooldownAccountRetestFailureInput {
  statusCode?: number
  errorCode?: string
  errorMessage?: string
  initialBackoffSeconds?: number
  fastThresholdSeconds?: number
  maxPauseMinutes?: number
  maxRecoveryHours?: number
  backoffMultiplier?: number
}

export interface CooldownAccountRetestFailureResult {
  action: 'retry_immediately' | 'cooldown' | 'error' | 'discard'
  changed: boolean
  failureCount: number
  account?: AccountSummary
  cooldownUntil?: string
  backoffSeconds?: number
  backoffMinutes?: number
  recoveryStage?: 'fast' | 'slow' | 'error'
  fastThresholdSeconds?: number
  maxPauseSeconds?: number
  maxRecoverySeconds?: number
  maxedFailureCount?: number
  observationStartedAt?: string
  observationElapsedSeconds?: number
  errorCode: string
  errorMessage: string
}

export function recordCooldownAccountRetestFailure(id: string, input: CooldownAccountRetestFailureInput): CooldownAccountRetestFailureResult {
  const current = findAccountSummary(id)
  const errorCode = normalizedCooldownRetestErrorCode(input)
  const testErrorMessage = normalizedCooldownRetestErrorMessage(input, errorCode)
  if (!current || current.status !== 'temporary_unavailable') {
    return {
      action: 'discard',
      changed: false,
      failureCount: current?.cooldownRetestFailureCount ?? 0,
      account: current,
      errorCode,
      errorMessage: testErrorMessage
    }
  }

  const nowDate = new Date()
  const now = nowDate.toISOString()
  const failureCount = Math.max(0, current.cooldownRetestFailureCount ?? 0) + 1
  const lastStatusCode = typeof input.statusCode === 'number' && Number.isFinite(input.statusCode) ? Math.trunc(input.statusCode) : null
  const observationStartedAt = current.cooldownRetestObservationStartedAt ?? now
  const recovery = cooldownRetestRecoveryPlan(failureCount, input, nowDate, observationStartedAt)
  if (recovery.shouldMarkError) {
    const finalMessage = cooldownRetestExhaustedMessage(failureCount, recovery.backoffSeconds, recovery.maxRecoverySeconds, recovery.observationElapsedSeconds, testErrorMessage)
    const result = getDatabase()
      .prepare(`
        UPDATE accounts
        SET status = 'error',
            schedulable = 0,
            cooldown_until = NULL,
            last_error_code = ?,
            last_error_message = ?,
            cooldown_retest_failure_count = ?,
            cooldown_retest_observation_started_at = COALESCE(cooldown_retest_observation_started_at, ?),
            cooldown_retest_last_at = ?,
            cooldown_retest_last_status_code = ?,
            stream_failure_count = 0,
            stream_failure_window_started_at = NULL,
            updated_at = ?
        WHERE id = ?
          AND status = 'temporary_unavailable'
      `)
      .run(errorCode, finalMessage, failureCount, observationStartedAt, now, lastStatusCode, now, id)
    const changed = Number(result.changes ?? 0) > 0
    if (changed) {
      refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_cooldown_retest_exhausted' })
      invalidateAccountLookupCache(id)
      invalidateGatewayRuntimeAfterBusinessWrite('account_cooldown_retest_exhausted')
    }
    return {
      action: 'error',
      changed,
      failureCount,
      account: failureAccountSummary(id, current),
      errorCode,
      errorMessage: finalMessage,
      recoveryStage: 'error',
      backoffSeconds: recovery.backoffSeconds,
      backoffMinutes: secondsToCeilMinutes(recovery.backoffSeconds),
      fastThresholdSeconds: recovery.fastThresholdSeconds,
      maxPauseSeconds: recovery.maxPauseSeconds,
      maxRecoverySeconds: recovery.maxRecoverySeconds,
      maxedFailureCount: recovery.maxedFailureCount,
      observationStartedAt: recovery.observationStartedAt,
      observationElapsedSeconds: recovery.observationElapsedSeconds
    }
  }

  const cooldownUntil = new Date(nowDate.getTime() + recovery.backoffSeconds * 1000).toISOString()
  const cooldownMessage = cooldownRetestCooldownMessage(failureCount, recovery.backoffSeconds, recovery.stage, testErrorMessage)
  const result = getDatabase()
    .prepare(`
      UPDATE accounts
      SET schedulable = 1,
          cooldown_until = ?,
          last_error_code = ?,
          last_error_message = ?,
          cooldown_retest_failure_count = ?,
          cooldown_retest_observation_started_at = COALESCE(cooldown_retest_observation_started_at, ?),
          cooldown_retest_last_at = ?,
          cooldown_retest_last_status_code = ?,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
        AND status = 'temporary_unavailable'
    `)
    .run(cooldownUntil, errorCode, cooldownMessage, failureCount, observationStartedAt, now, lastStatusCode, now, id)
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_cooldown_retest_backoff' })
    invalidateAccountLookupCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('account_cooldown_retest_backoff')
  }
  const action = recovery.stage === 'fast' ? 'retry_immediately' : 'cooldown'
  return {
    action,
    changed,
    failureCount,
    account: failureAccountSummary(id, current),
    cooldownUntil,
    backoffSeconds: recovery.backoffSeconds,
    backoffMinutes: secondsToCeilMinutes(recovery.backoffSeconds),
    recoveryStage: recovery.stage,
    fastThresholdSeconds: recovery.fastThresholdSeconds,
    maxPauseSeconds: recovery.maxPauseSeconds,
    maxRecoverySeconds: recovery.maxRecoverySeconds,
    maxedFailureCount: recovery.maxedFailureCount,
    observationStartedAt: recovery.observationStartedAt,
    observationElapsedSeconds: recovery.observationElapsedSeconds,
    errorCode,
    errorMessage: cooldownMessage
  }
}

function normalizedCooldownRetestErrorCode(input: CooldownAccountRetestFailureInput): string {
  const code = optionalString(input.errorCode)
  if (code) return code.slice(0, 120)
  if (typeof input.statusCode === 'number' && Number.isFinite(input.statusCode)) {
    return `http_${Math.trunc(input.statusCode)}`
  }
  return 'cooldown_retest_failed'
}

function failureAccountSummary(id: string, fallback: AccountSummary): AccountSummary {
  return findAccountSummary(id) ?? fallback
}

function normalizedCooldownRetestErrorMessage(input: CooldownAccountRetestFailureInput, errorCode: string): string {
  const message = optionalString(input.errorMessage) ?? '后台冷却复测失败'
  const parts: string[] = []
  if (typeof input.statusCode === 'number' && Number.isFinite(input.statusCode)) {
    parts.push(`HTTP ${Math.trunc(input.statusCode)}`)
  }
  if (errorCode && !errorCode.startsWith('http_') && !message.includes(errorCode)) {
    parts.push(errorCode)
  }
  parts.push(message)
  return parts.join('；').slice(0, 1000)
}

interface CooldownRetestRecoveryPlan {
  stage: 'fast' | 'slow'
  shouldMarkError: boolean
  backoffSeconds: number
  fastThresholdSeconds: number
  maxPauseSeconds: number
  maxRecoverySeconds: number
  maxedFailureCount: number
  observationStartedAt: string
  observationElapsedSeconds: number
}

function cooldownRetestRecoveryPlan(failureCount: number, input: CooldownAccountRetestFailureInput, nowDate: Date, observationStartedAt: string): CooldownRetestRecoveryPlan {
  const initialBackoffSeconds = boundedInteger(input.initialBackoffSeconds, temporaryUnavailableInitialBackoffSeconds, 1, 3600)
  const fastThresholdSeconds = boundedInteger(input.fastThresholdSeconds, temporaryUnavailableFastThresholdSeconds, initialBackoffSeconds, 3600)
  const maxPauseSeconds = boundedInteger(input.maxPauseMinutes, defaultTemporaryUnschedulableMinutes(), 1, 1440) * 60
  const maxRecoverySeconds = boundedInteger(input.maxRecoveryHours, 24, 1, 24 * 30) * 60 * 60
  const multiplier = boundedInteger(input.backoffMultiplier, temporaryUnavailableBackoffMultiplier, 2, 10)
  const exponent = Math.max(0, Math.min(failureCount, 30))
  const uncappedBackoffSeconds = Math.min(Number.MAX_SAFE_INTEGER, initialBackoffSeconds * Math.pow(multiplier, exponent))
  const backoffSeconds = Math.min(uncappedBackoffSeconds, maxPauseSeconds)
  const stage = backoffSeconds <= fastThresholdSeconds ? 'fast' : 'slow'
  const firstMaxedFailureCount = firstCappedBackoffFailureCount(initialBackoffSeconds, multiplier, maxPauseSeconds)
  const maxedFailureCount = failureCount >= firstMaxedFailureCount ? failureCount - firstMaxedFailureCount + 1 : 0
  const observationElapsedSeconds = cooldownRetestObservationElapsedSeconds(observationStartedAt, nowDate)
  return {
    stage,
    shouldMarkError: stage === 'slow' && observationElapsedSeconds >= maxRecoverySeconds,
    backoffSeconds,
    fastThresholdSeconds,
    maxPauseSeconds,
    maxRecoverySeconds,
    maxedFailureCount,
    observationStartedAt,
    observationElapsedSeconds
  }
}

function cooldownRetestObservationElapsedSeconds(observationStartedAt: string, nowDate: Date): number {
  const startedAtMs = Date.parse(observationStartedAt)
  if (!Number.isFinite(startedAtMs)) {
    return 0
  }
  return Math.max(0, Math.floor((nowDate.getTime() - startedAtMs) / 1000))
}

function firstCappedBackoffFailureCount(initialBackoffSeconds: number, multiplier: number, maxPauseSeconds: number): number {
  if (initialBackoffSeconds >= maxPauseSeconds) {
    return 1
  }
  const raw = Math.ceil(Math.log(maxPauseSeconds / initialBackoffSeconds) / Math.log(multiplier))
  return Math.max(1, raw)
}

function cooldownRetestCooldownMessage(failureCount: number, backoffSeconds: number, stage: 'fast' | 'slow', lastError: string): string {
  const stageText = stage === 'fast' ? '快速恢复通道' : '慢速恢复通道'
  return `后台冷却复测连续失败 ${failureCount} 次，${stageText}下次复测延后 ${formatDurationSeconds(backoffSeconds)}；最后错误：${lastError}`.slice(0, 1000)
}

function cooldownRetestExhaustedMessage(failureCount: number, backoffSeconds: number, maxRecoverySeconds: number, observationElapsedSeconds: number, lastError: string): string {
  return `后台冷却复测连续失败 ${failureCount} 次，已观察 ${formatDurationSeconds(observationElapsedSeconds)}，超过最长自动恢复观察 ${formatDurationSeconds(maxRecoverySeconds)}，转为异常；最后一次退避 ${formatDurationSeconds(backoffSeconds)}；最后错误：${lastError}`.slice(0, 1000)
}

function secondsToCeilMinutes(seconds: number): number {
  return Math.max(1, Math.ceil(Math.max(1, seconds) / 60))
}

function formatDurationSeconds(seconds: number): string {
  const safeSeconds = Math.max(1, Math.trunc(seconds))
  if (safeSeconds < 60) return `${safeSeconds} 秒`
  const minutes = Math.floor(safeSeconds / 60)
  const restSeconds = safeSeconds % 60
  if (minutes < 60) {
    return restSeconds > 0 ? `${minutes} 分钟 ${restSeconds} 秒` : `${minutes} 分钟`
  }
  const hours = Math.floor(minutes / 60)
  const restMinutes = minutes % 60
  return restMinutes > 0 ? `${hours} 小时 ${restMinutes} 分钟` : `${hours} 小时`
}

function boundedInteger(value: unknown, fallback: number, min: number, max: number): number {
  const parsed = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  if (!Number.isFinite(parsed)) return fallback
  return Math.min(Math.max(Math.trunc(parsed), min), max)
}

export function markAccountTestTemporaryUnavailable(
  account: AccountSummary,
  reason: string,
  access?: AccessScope
): AccountSummary | undefined {
  const current = findAccountSummary(account.id, access)
  if (!current || current.status !== 'active' || !current.schedulable) {
    return undefined
  }
  const cooldownUntil = initialTemporaryUnavailableCooldownUntil()
  const message = reason.slice(0, 1000)
  if (current.accessType === 'authorized') {
    return markAuthorizedAccountBindingTemporaryUnavailable(current, cooldownUntil, message, access)
  }
  return markAccountCooldown(current.id, cooldownUntil, message, 'temporary_unavailable')
}

function markAuthorizedAccountBindingTemporaryUnavailable(
  account: AccountSummary,
  cooldownUntil: string,
  reason: string,
  access?: AccessScope
): AccountSummary | undefined {
  if (!account.boundGroupId) {
    return undefined
  }
  const systemAccountId = authorizedBindingSystemAccountId(access)
  const now = nowIso()
  const result = getDatabase()
    .prepare(`
      UPDATE group_accounts
      SET local_status = 'temporary_unavailable',
          local_cooldown_until = ?,
          local_last_error_message = ?,
          updated_at = ?
      WHERE account_id = ?
        AND system_account_id = ?
        AND group_id = ?
        AND enabled = 1
        AND account_authorization_id IS NOT NULL
    `)
    .run(cooldownUntil, reason || null, now, account.id, systemAccountId, account.boundGroupId)
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  refreshGroupAccountStatsAfterWrite({ groupIds: [account.boundGroupId], reason: 'account_test_cooldown' })
  invalidateGatewayRuntimeAfterBusinessWrite('account_test_cooldown')
  return findAccountSummary(account.id, access)
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
            cooldown_retest_failure_count = 0,
            cooldown_retest_observation_started_at = NULL,
            cooldown_retest_last_at = NULL,
            cooldown_retest_last_status_code = NULL,
            stream_failure_count = 0,
            stream_failure_window_started_at = NULL,
            updated_at = ?
        WHERE id = ?
      `)
      .run('账户套餐已过期，已自动停用', nowIso(), id)
    if (Number(result.changes ?? 0) > 0) {
      refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_expired' })
      invalidateAccountLookupCache(id)
      invalidateGatewayRuntimeAfterBusinessWrite('account_expired')
    }
    return findAccountSummary(id)
  }

  const cooldownStatus: AccountStatus = status === 'rate_limited' ? 'rate_limited' : 'temporary_unavailable'
  const cooldownNowMs = Date.now()
  const cooldownNow = new Date(cooldownNowMs).toISOString()
  const cooldownUntil = cooldownStatus === 'temporary_unavailable'
    ? initialTemporaryUnavailableCooldownUntil(cooldownNowMs)
    : until
  const cooldownObservationStartedAt = cooldownRetestObservationStartedAtForStatus(cooldownStatus, cooldownNowMs)

  const result = getDatabase()
    .prepare(`
      UPDATE accounts
      SET status = ?,
          schedulable = 1,
          cooldown_until = ?,
          last_error_code = NULL,
          last_error_message = ?,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = ?,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
    `)
    .run(cooldownStatus, cooldownUntil, reason || null, cooldownObservationStartedAt ?? null, cooldownNow, id)
  if (Number(result.changes ?? 0) > 0) {
    refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_cooldown' })
    invalidateAccountLookupCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('account_cooldown')
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

  const nowMs = Date.now()
  const now = new Date(nowMs).toISOString()
  const reason = manualTrafficMigrationReason
  const sourceCooldownUntil = input.sourceStatus === 'temporary_unavailable'
    ? initialTemporaryUnavailableCooldownUntil(nowMs)
    : null
  const sourceObservationStartedAt = input.sourceStatus === 'temporary_unavailable'
    ? cooldownRetestObservationStartedAtForStatus('temporary_unavailable', nowMs)
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
              cooldown_retest_failure_count = 0,
              cooldown_retest_observation_started_at = NULL,
              cooldown_retest_last_at = NULL,
              cooldown_retest_last_status_code = NULL,
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
              cooldown_retest_failure_count = 0,
              cooldown_retest_observation_started_at = ?,
              cooldown_retest_last_at = NULL,
              cooldown_retest_last_status_code = NULL,
              stream_failure_count = 0,
              stream_failure_window_started_at = NULL,
              updated_at = ?
          WHERE id = ? AND system_account_id = ?
        `)
        .run(sourceCooldownUntil, reason, sourceObservationStartedAt ?? null, now, sourceRow.id, sourceRow.system_account_id)
    if (Number(updateResult.changes ?? 0) <= 0) {
      rollbackDatabaseTransaction(database, transactionStarted)
      return undefined
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshGroupAccountStatsAfterWrite({ accountIds: [sourceRow.id], reason: 'traffic_migration' })
  invalidateGatewayRuntimeAfterBusinessWrite('traffic_migration')

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
  input: { status?: 'active' | 'disabled'; superPriorityEnabled?: boolean; fallbackEnabled?: boolean; clearFailureState?: boolean },
  access?: AccessScope
): AccountSummary | undefined {
  const systemAccountId = authorizedBindingSystemAccountId(access)
  const current = findAccountSummary(accountId, access)
  if (current?.accessType !== 'authorized' || !current.boundGroupId) {
    throw new Error('授权账户需要先绑定到你的分组')
  }
  const enablingDispatchFlag = input.superPriorityEnabled === true || input.fallbackEnabled === true
  if (enablingDispatchFlag) {
    const unavailableMessage = accountDispatchUnavailableMessage(current, { requireAuthorizedBinding: true })
    if (unavailableMessage) {
      throw new Error(unavailableMessage)
    }
  }
  const hasSuperPriorityInput = Object.prototype.hasOwnProperty.call(input, 'superPriorityEnabled')
  const hasFallbackInput = Object.prototype.hasOwnProperty.call(input, 'fallbackEnabled')
  const nextSuperPriority = hasSuperPriorityInput ? input.superPriorityEnabled === true : current.superPriorityEnabled
  const nextFallback = hasFallbackInput ? input.fallbackEnabled === true : current.fallbackEnabled
  if (nextSuperPriority && nextFallback) {
    throw new Error('超级优先和降级备用不能同时开启')
  }
  const hasStatusInput = Object.prototype.hasOwnProperty.call(input, 'status')
  const nextLocalStatus: AccountStatus = hasStatusInput
    ? input.status === 'disabled' ? 'disabled' : 'active'
    : input.clearFailureState === true
      ? 'active'
      : current.localStatus ?? 'active'
  const shouldClearLocalFailureState = input.clearFailureState === true || hasStatusInput
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
      nextLocalStatus,
      shouldClearLocalFailureState ? null : current.localCooldownUntil ?? null,
      shouldClearLocalFailureState ? null : current.localLastErrorMessage ?? null,
      nextSuperPriority ? 1 : 0,
      nextFallback ? 1 : 0,
      now,
      accountId,
      systemAccountId
    )
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  refreshGroupAccountStatsAfterWrite({ groupIds: [current.boundGroupId], reason: 'authorized_binding_dispatch' })
  invalidateGatewayRuntimeAfterBusinessWrite('authorized_binding_dispatch')
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
  const targetUnavailableMessage = accountDispatchUnavailableMessage(targetAccount, { requireAuthorizedBinding: targetAccount.accessType === 'authorized' })
  if (targetUnavailableMessage) {
    throw new Error(targetUnavailableMessage)
  }
  const sourceCooldownUntil = input.sourceStatus === 'temporary_unavailable'
    ? initialTemporaryUnavailableCooldownUntil()
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
  refreshGroupAccountStatsAfterWrite({ groupIds: [sourceAccount.boundGroupId], reason: 'authorized_binding_migration' })
  invalidateGatewayRuntimeAfterBusinessWrite('authorized_binding_migration')
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
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = NULL,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
    `)
    .run(errorCode || null, reason || null, nowIso(), id)
  if (Number(result.changes ?? 0) > 0) {
    refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_exception' })
    invalidateAccountLookupCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('account_exception')
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
  refreshGroupAccountStatsAfterWrite({ accountIds: [input.accountId], reason: 'stream_failure_threshold' })

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

export function listGroupOptions(access?: AccessScope, options?: GroupListOptions): GroupOptionSummary[] {
  return buildGroupOptionSummaries(listGroupRowsForAccess(access, options), access)
}

export function listAccountGroupOptions(access?: AccessScope, options?: GroupListOptions): AccountGroupOptionSummary[] {
  const rows = listGroupRowsForAccess(access, options)
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
  const accountNames = shouldIncludeSystemAccountFields || hasAuthorizedRows ? loadSystemAccountNameMapByIds(rows.map((row) => row.system_account_id)) : new Map<string, string>()
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
      groupType: groupTypeFromRow(row),
      schedulingPolicy: groupSchedulingPolicyFromRow(row),
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
  const accountNames = loadSystemAccountNameMapByIds(rows.map((row) => row.system_account_id))
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
      groupType: groupTypeFromRow(row),
      schedulingPolicy: groupSchedulingPolicyFromRow(row),
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

function groupTypeFromRow(row: Pick<GroupListRow, 'group_type'>): GroupType {
  return normalizeGroupType(row.group_type)
}

function groupSchedulingPolicyFromRow(row: Pick<GroupListRow, 'group_type' | 'scheduling_policy_json'>): GroupSchedulingPolicy | undefined {
  return parseGroupSchedulingPolicyJson(row.scheduling_policy_json, groupTypeFromRow(row))
}

function hasGroupSchedulingPolicyInput(input: Record<string, unknown>): boolean {
  return Object.prototype.hasOwnProperty.call(input, 'schedulingPolicy')
    || Object.prototype.hasOwnProperty.call(input, 'scheduling_policy')
    || Object.prototype.hasOwnProperty.call(input, 'schedulingPolicyJson')
    || Object.prototype.hasOwnProperty.call(input, 'scheduling_policy_json')
}

function groupSchedulingPolicyInput(input: Record<string, unknown>): unknown {
  return input.schedulingPolicy ?? input.scheduling_policy ?? input.schedulingPolicyJson ?? input.scheduling_policy_json
}

export function createGroup(input: Record<string, unknown>, access?: AccessScope): GroupSummary {
  const now = nowIso()
  const systemAccountId = writeSystemAccountId(access)
  const providerCode = String(input.providerCode ?? input.provider_code ?? 'openai').trim() || 'openai'
  const groupType = normalizeGroupType(input.groupType ?? input.group_type)
  const schedulingPolicyJson = groupSchedulingPolicyJson(groupSchedulingPolicyInput(input), groupType)
  const name = normalizedEntityName(input.name, '未命名分组')
  assertGroupNameAvailable(systemAccountId, providerCode, name)
  const group: GroupSummary = {
    id: newId('grp'),
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields(access) ? loadSystemAccountNameMapByIds([systemAccountId]).get(systemAccountId) : undefined,
    name,
    providerCode,
    description: optionalString(input.description),
    enabled: input.enabled !== false,
    isDefault: false,
    groupType,
    schedulingPolicy: parseGroupSchedulingPolicyJson(schedulingPolicyJson, groupType),
    accountIds: [],
    accountStats: emptyGroupAccountStats()
  }
  try {
    getDatabase()
      .prepare('INSERT INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, group_type, scheduling_policy_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)')
      .run(group.id, systemAccountId, group.name, group.providerCode, group.description ?? null, group.enabled ? 1 : 0, group.groupType, schedulingPolicyJson, now, now)
  } catch (error) {
    if (isDuplicateGroupNameError(error)) {
      throw new Error(`同一供应商下分组名称已存在：${group.name}`)
    }
    throw error
  }
  invalidateGroupLookupCache(group.id)
  invalidateGatewayRuntimeAfterBusinessWrite('group_created')
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
  const hasGroupTypeInput = Object.prototype.hasOwnProperty.call(input, 'groupType') || Object.prototype.hasOwnProperty.call(input, 'group_type')
  const hasSchedulingPolicyInput = hasGroupSchedulingPolicyInput(input)
  const nextGroupType = hasGroupTypeInput ? normalizeGroupType(input.groupType ?? input.group_type) : current.groupType
  const nextSchedulingPolicyInput = hasSchedulingPolicyInput ? groupSchedulingPolicyInput(input) : current.schedulingPolicy
  const next: GroupSummary = {
    ...current,
    name: typeof input.name === 'string' && input.name.trim() ? input.name.trim() : current.name,
    providerCode: typeof input.providerCode === 'string' && input.providerCode.trim()
      ? input.providerCode.trim()
      : typeof input.provider_code === 'string' && input.provider_code.trim()
        ? input.provider_code.trim()
        : current.providerCode,
    description: hasDescriptionInput ? optionalNullableString(input.description) ?? undefined : current.description,
    enabled: typeof input.enabled === 'boolean' ? input.enabled : current.enabled,
    groupType: nextGroupType,
    schedulingPolicy: parseGroupSchedulingPolicyJson(groupSchedulingPolicyJson(nextSchedulingPolicyInput, nextGroupType), nextGroupType)
  }
  if (next.providerCode !== current.providerCode && current.accountStats.total > 0) {
    throw new Error('已有账户的分组不允许修改供应商')
  }
  assertGroupNameAvailable(systemAccountId, next.providerCode, next.name, id)
  const database = getDatabase()
  try {
    database
      .prepare('UPDATE groups SET name = ?, provider_code = ?, description = ?, enabled = ?, group_type = ?, scheduling_policy_json = ?, updated_at = ? WHERE id = ? AND system_account_id = ?')
      .run(next.name, next.providerCode, next.description ?? null, next.enabled ? 1 : 0, next.groupType, groupSchedulingPolicyJson(nextSchedulingPolicyInput, nextGroupType), nowIso(), id, systemAccountId)
  } catch (error) {
    if (isDuplicateGroupNameError(error)) {
      throw new Error(`同一供应商下分组名称已存在：${next.name}`)
    }
    throw error
  }
  invalidateGroupLookupCache(id)
  invalidateGatewayRuntimeAfterBusinessWrite('group_updated')
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
    refreshGroupAccountStatsAfterWrite({ groupIds: [id], reason: 'group_deleted' })
    invalidateGroupLookupCache(id)
    invalidateGroupAccountIdsCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('group_deleted')
  }
  return result.changes > 0
}

export function setAccountGroup(
  accountId: string,
  groupId: string | null,
  access?: AccessScope
): AccountSummary | undefined {
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

  const previousGroupId = accountEnabledGroupId(accountId, group.systemAccountId)
  database.prepare('DELETE FROM group_accounts WHERE account_id = ? AND system_account_id = ?').run(accountId, group.systemAccountId)
  const now = nowIso()
  database
    .prepare(`
      INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at)
      VALUES (?, ?, ?, ?, 1, ?, ?)
      ON CONFLICT(group_id, account_id) DO UPDATE SET
        account_authorization_id = excluded.account_authorization_id,
        enabled = 1,
        updated_at = excluded.updated_at
    `)
    .run(group.systemAccountId, groupId, accountId, accountAuthorization?.id ?? null, now, now)
  refreshGroupAccountStatsAfterWrite({ groupIds: [previousGroupId, groupId], reason: 'group_account_binding' })
  if (previousGroupId && previousGroupId !== groupId) {
    invalidateGroupAccountIdsCache(previousGroupId)
  }
  invalidateGroupAccountIdsCache(groupId)
  invalidateGatewayRuntimeAfterBusinessWrite('group_account_binding')

  return findAccountSummary(accountId, { systemAccountId: group.systemAccountId, role: 'user' })
}

export function addAccountToGroup(groupId: string, accountId: string): GroupSummary | undefined {
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
  const previousGroupId = accountEnabledGroupId(accountId, current.systemAccountId)
  database.prepare('DELETE FROM group_accounts WHERE account_id = ? AND system_account_id = ?').run(accountId, current.systemAccountId)
  database
    .prepare(`
      INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at)
      VALUES (?, ?, ?, ?, 1, ?, ?)
      ON CONFLICT(group_id, account_id) DO UPDATE SET account_authorization_id = excluded.account_authorization_id, enabled = 1, updated_at = excluded.updated_at
    `)
    .run(current.systemAccountId, groupId, accountId, accountAuthorization?.id ?? null, now, now)
  refreshGroupAccountStatsAfterWrite({ groupIds: [previousGroupId, groupId], reason: 'group_account_binding' })
  if (previousGroupId && previousGroupId !== groupId) {
    invalidateGroupAccountIdsCache(previousGroupId)
  }
  invalidateGroupAccountIdsCache(groupId)
  invalidateGatewayRuntimeAfterBusinessWrite('group_account_binding')
  return findGroupSummary(groupId)
}

export interface SystemTeamListOptions {
  page?: number
  pageSize?: number
  limit?: number
  keyword?: string
}

interface NormalizedSystemTeamListOptions {
  page: number
  pageSize: number
  keyword?: string
}

export function listSystemTeams(access?: AccessScope): SystemTeamSummary[] {
  const rows = querySystemTeamRows(access, undefined, normalizeSystemTeamListOptions()).rows
  const members = listSystemTeamMembersForTeamIds(rows.map((row) => row.id), true)
  return rows.map((row) => systemTeamSummaryFromRow(row, members.get(row.id) ?? [], access))
}

export function listSystemTeamsPage(access?: AccessScope, options: SystemTeamListOptions = {}): SystemTeamListResult {
  const listOptions = normalizeSystemTeamListOptions(options)
  const rows = querySystemTeamRows(access, {
    limit: listOptions.pageSize + 1,
    offset: (listOptions.page - 1) * listOptions.pageSize
  }, listOptions).rows
  const pageRows = takePageRows(rows, listOptions.pageSize)
  const members = listSystemTeamMembersForTeamIds(pageRows.rows.map((row) => row.id), true)
  const items = pageRows.rows.map((row) => systemTeamSummaryFromRow(row, members.get(row.id) ?? [], access))
  return {
    items,
    total: pagedTotalUpperBound(listOptions.page, listOptions.pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: listOptions.page,
    pageSize: listOptions.pageSize
  }
}

function querySystemTeamRows(access: AccessScope | undefined, pagination: { limit: number; offset: number } | undefined, options: Pick<NormalizedSystemTeamListOptions, 'keyword'>): { rows: SystemTeamRow[] } {
  const scopedId = scopedSystemAccountId(access)
  const clauses: string[] = []
  const params: string[] = []
  if (scopedId) {
    clauses.push(`EXISTS (
      SELECT 1
      FROM system_team_members
      WHERE system_team_members.team_id = system_teams.id
        AND system_team_members.system_account_id = ?
        AND system_team_members.status = 'active'
    )`)
    params.push(scopedId)
  }
  const keyword = options.keyword?.trim()
  if (keyword) {
    const prefix = `${escapeLikePrefix(keyword)}%`
    clauses.push(`(
      system_teams.name COLLATE NOCASE = ?
      OR system_teams.name LIKE ? ESCAPE '\\'
    )`)
    params.push(keyword, prefix)
  }
  const whereClause = clauses.length ? ` WHERE ${clauses.join(' AND ')}` : ''
  const pageClause = pagination ? ' LIMIT ? OFFSET ?' : ''
  const pageParams = pagination ? [pagination.limit, pagination.offset] : []
  const rows = getDatabase()
    .prepare(`SELECT id, name, description, status, created_by, created_at, updated_at FROM system_teams${whereClause} ORDER BY status ASC, updated_at DESC, name ASC, id ASC${pageClause}`)
    .all(...params, ...pageParams) as unknown as SystemTeamRow[]
  return { rows }
}

function normalizeSystemTeamListOptions(options: SystemTeamListOptions = {}): NormalizedSystemTeamListOptions {
  const rawPage = options.page
  const rawPageSize = options.pageSize ?? options.limit
  const page = typeof rawPage === 'number' && Number.isInteger(rawPage) ? Math.max(1, rawPage) : 1
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(200, Math.max(1, rawPageSize))
    : 20
  return {
    page,
    pageSize,
    keyword: optionalString(options.keyword)
  }
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

interface AuthorizationPrincipalOptionListOptions {
  ids?: string[]
  keyword?: string
  limit?: number
}

export function listAuthorizationGranteeAccounts(access?: AccessScope, options: AuthorizationPrincipalOptionListOptions = {}): SystemAccountPrincipalSummary[] {
  void access
  const database = getDatabase()
  const principalFilter = buildSystemAccountPrincipalFilter(options)
  const limitClause = authorizationPrincipalOptionLimitClause(options.limit)
  const rows = database.prepare(`
    SELECT id, username, display_name, status
    FROM system_accounts
    ${principalFilter.clause}
    ORDER BY status ASC, display_name ASC, username ASC, id ASC
    ${limitClause.clause}
  `).all(...principalFilter.params, ...limitClause.params) as unknown as Array<Pick<SystemAccountRow, 'id' | 'username' | 'display_name' | 'status'>>
  return rows.map(systemAccountPrincipalSummaryFromRow)
}

export function listAuthorizationGranteeTeams(access?: AccessScope, options: AuthorizationPrincipalOptionListOptions = {}): SystemTeamPrincipalSummary[] {
  void access
  const database = getDatabase()
  const principalFilter = buildSystemTeamPrincipalFilter(options)
  const limitClause = authorizationPrincipalOptionLimitClause(options.limit)
  const rows = database.prepare(`
    SELECT id, name, status
    FROM system_teams
    ${principalFilter.clause}
    ORDER BY status ASC, name ASC, id ASC
    ${limitClause.clause}
  `).all(...principalFilter.params, ...limitClause.params) as unknown as SystemTeamRow[]
  return rows.map(systemTeamPrincipalSummaryFromRow)
}

function buildSystemAccountPrincipalFilter(options: AuthorizationPrincipalOptionListOptions): { clause: string; params: string[] } {
  return buildPrincipalFilter(options, buildSystemAccountPrincipalKeywordFilter)
}

function buildSystemTeamPrincipalFilter(options: AuthorizationPrincipalOptionListOptions): { clause: string; params: string[] } {
  return buildPrincipalFilter(options, buildSystemTeamPrincipalKeywordFilter)
}

function buildPrincipalFilter(
  options: AuthorizationPrincipalOptionListOptions,
  keywordFilterBuilder: (keyword?: string) => { clause: string; params: string[] }
): { clause: string; params: string[] } {
  const clauses: string[] = []
  const params: string[] = []
  const ids = normalizeTextList(options.ids)
  if (ids.length) {
    clauses.push(`id IN (${sqlPlaceholders(ids.length)})`)
    params.push(...ids)
  }
  const keywordFilter = keywordFilterBuilder(options.keyword)
  if (keywordFilter.clause) {
    clauses.push(keywordFilter.clause.replace(/^WHERE\s+/i, ''))
    params.push(...keywordFilter.params)
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

function buildSystemAccountPrincipalKeywordFilter(keyword?: string): { clause: string; params: string[] } {
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

function buildSystemTeamPrincipalKeywordFilter(keyword?: string): { clause: string; params: string[] } {
  const text = optionalString(keyword)
  if (!text) return { clause: '', params: [] }
  const prefix = `${escapeLikePrefix(text)}%`
  return {
    clause: `WHERE (
      name COLLATE NOCASE = ?
      OR name LIKE ? ESCAPE '\\'
    )`,
    params: [text, prefix]
  }
}

function authorizationPrincipalOptionLimitClause(limit?: number): { clause: string; params: number[] } {
  const safeLimit = typeof limit === 'number' && Number.isInteger(limit)
    ? Math.min(50, Math.max(1, limit))
    : 50
  return { clause: 'LIMIT ?', params: [safeLimit] }
}

function escapeLikePrefix(value: string): string {
  return value.replace(/[\\%_]/g, (char) => `\\${char}`)
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
  invalidateSystemTeamLookupCache(id)
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
    refreshGroupAccountStatsAfterWrite({ all: true, reason: 'team_authorization_changed' })
    invalidateAuthorizationRuntimeAfterBusinessWrite('team_authorization_changed')
  }
  invalidateSystemTeamLookupCache(id)
  invalidateSystemAccountTeamMembershipLookupCache()
  clearResourceAuthorizationLookupCaches()
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
  refreshGroupAccountStatsAfterWrite({ all: true, reason: 'team_members_changed' })
  invalidateAuthorizationRuntimeAfterBusinessWrite('team_members_changed')
  for (const systemAccountId of systemAccountIds) {
    invalidateSystemAccountTeamMembershipLookupCache(systemAccountId)
  }
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
  refreshGroupAccountStatsAfterWrite({ all: true, reason: 'team_members_changed' })
  invalidateAuthorizationRuntimeAfterBusinessWrite('team_members_changed')
  invalidateSystemAccountTeamMembershipLookupCache(member.system_account_id)
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
  const expiresAt = optionalNullableServerDateTimeIso(input.expiresAt ?? input.expires_at)
  validateResourceAuthorizationExpiresAt(resourceType, resourceId, expiresAt, Date.parse(now))
  const actor = currentSystemAccountId(access)
  let createdGrantId: string | undefined
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    if (granteeType === 'team') {
      const team = database.prepare("SELECT * FROM system_teams WHERE id = ? AND status = 'active'").get(granteeId) as unknown as SystemTeamRow | undefined
      if (!team) throw new Error('团队不存在或已停用')
      const members = activeTeamMemberRows(granteeId, database).filter((member) => member.system_account_id !== ownerSystemAccountId)
      if (!members.length) throw new Error('团队暂无可授权成员，请先添加非归属人成员后再授权')
      const grant = upsertResourceAuthorizationGrant({ resourceType, resourceId, ownerSystemAccountId, granteeType, granteeId, remark: optionalString(input.remark), expiresAt, limits: input.limits, modelPolicy: input.modelPolicy ?? input.model_policy, actor, now, database })
      createdGrantId = grant.id
      for (const member of members) {
        upsertResourceAuthorizationForUser({ resourceType, resourceId, ownerSystemAccountId, granteeSystemAccountId: member.system_account_id, sourceType: 'team', sourceTeamId: granteeId, remark: optionalString(input.remark), expiresAt, limits: input.limits, modelPolicy: input.modelPolicy ?? input.model_policy, actor, now, database })
      }
    } else {
      const grantee = findSystemAccountById(granteeId)
      if (!grantee || grantee.status !== 'active') throw new Error('被授权用户不存在或已停用')
      if (granteeId === ownerSystemAccountId) throw new Error('不能授权给资源所有者自己')
      const grant = upsertResourceAuthorizationGrant({ resourceType, resourceId, ownerSystemAccountId, granteeType, granteeId, remark: optionalString(input.remark), expiresAt, limits: input.limits, modelPolicy: input.modelPolicy ?? input.model_policy, actor, now, database })
      createdGrantId = grant.id
      upsertResourceAuthorizationForUser({ resourceType, resourceId, ownerSystemAccountId, granteeSystemAccountId: granteeId, sourceType: 'manual', remark: optionalString(input.remark), expiresAt, limits: input.limits, modelPolicy: input.modelPolicy ?? input.model_policy, actor, now, database })
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshGroupAccountStatsAfterWrite({ all: true, reason: 'resource_authorization_created' })
  invalidateAuthorizationRuntimeAfterBusinessWrite('resource_authorization_created')
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
  refreshGroupAccountStatsAfterWrite({ all: true, reason: 'resource_authorization_revoked' })
  invalidateAuthorizationRuntimeAfterBusinessWrite('resource_authorization_revoked')
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
  validateResourceAuthorizationExpiresAt(grant.resource_type, grant.resource_id, nextExpiresAt, Date.parse(now), { allowExpired: requestedStatus === 'expired' })
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
  refreshGroupAccountStatsAfterWrite({ all: true, reason: 'resource_authorization_updated' })
  invalidateAuthorizationRuntimeAfterBusinessWrite('resource_authorization_updated')
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
    rows.push(...database.prepare(`
      SELECT ${systemTeamMemberSelectColumns('system_team_members')}, system_accounts.display_name, system_accounts.username
      FROM system_team_members
      INNER JOIN system_accounts ON system_accounts.id = system_team_members.system_account_id
      WHERE system_team_members.team_id IN (${sqlPlaceholders(chunk.length)})${statusClause}
      ORDER BY system_team_members.status ASC, system_team_members.joined_at ASC, system_team_members.id ASC
    `).all(...chunk) as unknown as Array<SystemTeamMemberRow & { display_name?: string; username?: string }>)
  }
  const result = new Map<string, SystemTeamMemberSummary[]>()
  for (const row of rows) {
    const member: SystemTeamMemberSummary = { id: row.id, teamId: row.team_id, systemAccountId: row.system_account_id, systemAccountName: row.display_name, username: row.username, memberRole: 'member', status: row.status, joinedAt: row.joined_at, removedAt: row.removed_at ?? undefined, createdAt: row.created_at, updatedAt: row.updated_at }
    result.set(row.team_id, [...(result.get(row.team_id) ?? []), member])
  }
  return result
}

function systemTeamMemberSelectColumns(alias: string): string {
  return [
    'id',
    'team_id',
    'system_account_id',
    'member_role',
    'status',
    'joined_at',
    'removed_at',
    'created_by',
    'created_at',
    'updated_at'
  ].map((column) => `${alias}.${column}`).join(', ')
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

function validateResourceAuthorizationExpiresAt(
  resourceType: ResourceAuthorizationResourceType,
  resourceId: string,
  expiresAt: string | null,
  now = Date.now(),
  options: { allowExpired?: boolean } = {}
): void {
  if (!expiresAt) return
  const expiresAtMs = Date.parse(expiresAt)
  if (!Number.isFinite(expiresAtMs)) throw new Error('授权到期时间格式不正确')
  if (!options.allowExpired && expiresAtMs <= now) throw new Error('授权到期时间不能早于当前时间')
  if (resourceType !== 'account') return
  const account = getDatabase()
    .prepare('SELECT account_expires_at FROM accounts WHERE id = ?')
    .get(resourceId) as unknown as { account_expires_at?: string | null } | undefined
  if (!account?.account_expires_at) return
  const accountExpiresAtMs = Date.parse(account.account_expires_at)
  if (Number.isFinite(accountExpiresAtMs) && expiresAtMs > accountExpiresAtMs) {
    throw new Error('授权到期时间不能晚于账户到期时间')
  }
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
    SELECT ${resourceAuthorizationSelectColumns()}
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
  const account = loadSystemAccountPrincipalMapByIds([granteeSystemAccountId]).get(granteeSystemAccountId)
  const usageBySystemAccount: ResourceAuthorizationUsageDetail[] = [{
    systemAccountId: granteeSystemAccountId,
    systemAccountName: account?.displayName ?? authorization.granteeSystemAccountName,
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
    usageBySystemAccountTotal: pagedTotalUpperBound(pageOptions.page, pageOptions.pageSize, usageBySystemAccount.length, pageRows.hasMore),
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
  const accounts = loadSystemAccountPrincipalMapByIds(rows.map((row) => row.grantee_system_account_id ?? ''))
  return rows.flatMap((row) => {
    const systemAccountId = row.grantee_system_account_id
    if (!systemAccountId) return []
    const account = accounts.get(systemAccountId)
    const rangeUsage = usageByAuthorization.get(row.id) ?? emptyAccountUsageSummary()
    return [{
      systemAccountId,
      systemAccountName: account?.displayName,
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
