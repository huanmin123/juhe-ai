import type { DatabaseSync } from 'node:sqlite'

import type { AccountGroupBindStatus, AccountGroupOptionSummary, AccountOptionSummary, AccountStatus, AccountSummary, AccountTrafficMigrationSourceStatus, AccountType, AccountUsageStatsOverview, AccountUsageStatsRange, AccountUsageSummary, AuthorizationStatus, GroupListResult, GroupOptionSummary, GroupSchedulingPolicy, GroupSummary, GroupType, ResourceAuthorizationListResult, ResourceAuthorizationResourceType, ResourceAuthorizationSourceStatus, ResourceAuthorizationSourceType, ResourceAuthorizationSummary, ResourceAuthorizationUsageDetail, ResourcePermissions, SystemAccountPrincipalSummary, SystemTeamListResult, SystemTeamMemberSummary, SystemTeamPrincipalSummary, SystemTeamSummary } from '../domain/types.js'
export type { GroupOptionSummary } from '../domain/types.js'
import { groupSchedulingPolicyJson, normalizeGroupType, parseGroupSchedulingPolicyJson } from '../domain/group-scheduling.js'
import { normalizeAccountErrorHandlingRules } from '../modules/accounts/account-error-policy-validation.js'
import { normalizeAccountStreamInterceptRules } from '../modules/accounts/account-stream-intercept-policy-validation.js'
import { listProviderModelPricing } from '../modules/model-pricing/model-pricing.service.js'
import { loadAccountCurrentConcurrencyByIds, sumAccountCurrentConcurrency } from '../shared/account-concurrency.js'
import { notifyAuthorizationQuotaCacheInvalidation, notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { buildSystemAccountScopeClause, canAccessAll, currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, scopedSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { accountCredentialFingerprint, accountIdentityFingerprint } from './account-identity.js'
import { accountStatusFilterValues, normalizeAccountListOptions, normalizeAccountOptionListOptions, type AccountListOptions, type AccountOptionListOptions } from './account-list-options.js'
import { cleanupDeletedAccountDetachedStats, type DeletedAccountRecordCleanupTarget } from './account-record-cleanup.js'
import { loadSupportedModelsByAccountIds, normalizeAccountSupportedModelsInput, replaceAccountSupportedModels } from './account-supported-models.repository.js'
import {
  accountAvailabilityScheduleFromRequest,
  accountAvailabilityScheduleJson,
  isAccountAvailabilityScheduleAllowed,
  isAccountAvailabilityScheduleInputPresent,
  parseAccountAvailabilityScheduleJson
} from './account-availability-schedule.js'
import { accountCredentialsForList, findAccountRowForAccess, hydrateAccountRowsWithRuntimeState, listAccountRowsForAccess, listAccountRowsPageForAccess, loadAccountAuthorizationUsageSummaries } from './account-read.repository.js'
import { maxAccountExpirySweepBatchSize } from './account-sweep-limits.js'
import {
  getAccountUsageStatsOverview as buildAccountUsageStatsOverview,
  getAccountUsageStatsOverviewPageFromWindows as buildAccountUsageStatsOverviewPageFromWindows
} from './account-usage.repository.js'
import { updateAccountUsageSnapshotRefreshState, upsertAccountUsageSnapshot } from './account-usage-snapshot.repository.js'
import { maxGroupDeleteAffectedApiKeyRoutes } from './api-key-group-binding-limits.js'
import { createApiKeyRecord, deleteApiKey, findApiKeySummary, listApiKeys, listApiKeysPage, updateApiKey } from './api-key.repository.js'
import { clearResourceAuthorizationLookupCaches, loadResourceAuthorizationSourcesByAuthorizationIds, loadResourceAuthorizationStatsByResourceIds } from './authorization-read-loaders.js'
import { decryptJson, encryptJson, maskSecret } from './crypto.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, getStatsDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { listErrorPolicies } from './error-policy.repository.js'
import { emptyGroupAccountStats, groupAccountStatsFromRow } from './group-account-stats.mapper.js'
import { findGroupRowForAccess, listGroupOptionRowsForAccess, listGroupRowsForAccess, listGroupRowsPageForAccess, loadGroupAuthorizationUsageSummaries, type GroupListOptions, type GroupOptionListOptions } from './group-read.repository.js'
import { invalidateGroupAccountIdsCache, loadGroupAccountIdsByGroupIds, loadGroupAccountStatsByGroupIds } from './group-read-loaders.js'
import { loadOpenAICodexUsageSnapshotsByAccountIds } from './oauth-usage-loaders.js'
import { listProviders } from './provider.repository.js'
import { resolveEnabledProxyProfileId } from './proxy.repository.js'
import { chunkValues, normalizeListPage, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { isRequestQuotaExceeded, loadRequestQuotaCostsBatch, requestQuotaCostKey, type RequestQuotaCostInput } from './request-quota-checker.js'
import {
  accountSystemAccountId,
  activeAccountAuthorization,
  activeGroupAuthorization,
  activeResourceAuthorization,
  activeResourceAuthorizationById,
  canManageResourceOwner,
  groupOwnerAndProvider,
  isResourceAuthorizationExpired,
  resourceAuthorizationSelectColumns,
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
  assertActiveTeamGrantFanoutWithinLimit,
  cleanupInactiveAuthorizationBindings,
  deactivateAuthorizationIfNoActiveSources,
  expireDueResourceAuthorizations,
  reactivateTeamGrantSources,
  revokeAllTeamSources,
  revokeResourceAuthorizationGrant,
  revokeTeamSourcesForMember,
  returnResourceAuthorizationGrant,
  syncAccountAuthorizationInstanceNamesForSourceAccount,
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
import { maxSystemTeamListPageSize, maxSystemTeamMemberBatchSize, maxSystemTeamMembersPerTeam } from './system-team-limits.js'
import { markAllGroupAccountStatsDirty, markGroupAccountStatsDirty, markGroupAccountStatsDirtyByAccountIds } from './usage-stats.repository.js'
import { GLOBAL_STATS_SYSTEM_ACCOUNT_ID } from './usage-stats-types.js'
import { emptyAccountUsageSummary, normalizeAccountUsageStatsRange, todayDateKey, usageStatsTimezone, usageSummaryFromAggregate } from './usage-stats-helpers.js'
import { loadAccountUsageSummariesForScopes, loadGroupUsageSummariesForScopes, loadUsageRangeSummaryForScope, type UsageSummaryScopeRequest } from './usage-summary-loaders.js'
import { loadUsageDailySeriesForScopeRequests } from './usage-window-loaders.js'
import {
  nullableServerDateTimeIso,
  optionalNullableServerDateTimeIso,
  optionalNullableString,
  optionalServerDateTimeIso,
  optionalString
} from './value-utils.js'

const DEFAULT_ACCOUNT_CONCURRENCY_LIMIT = 20
const manualTrafficMigrationReason = '手动迁移流量'
const defaultResourceAuthorizationUsageDetailPageSize = 200
const temporaryUnavailableInitialBackoffSeconds = 3
const temporaryUnavailableFastThresholdSeconds = 60
const temporaryUnavailableBackoffMultiplier = 2
const internalAccountReadAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'admin' }
type AccountOptionFilterValue = string | number
const currentIsoSql = "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')"

interface AccountOptionRow {
  id: string
  system_account_id: string
  provider_code: string
  name: string
  type: string
  status: AccountStatus
  schedulable: number
  account_expires_at: string | null
  cooldown_until?: string | null
  priority: number
  created_at: string
  authorization_instance_source_account_id?: string | null
  authorization_instance_authorization_id?: string | null
  authorization_instance_owner_system_account_id?: string | null
  access_type: 'owner' | 'authorized'
  authorization_id: string | null
  authorization_status: AuthorizationStatus | null
  authorization_expires_at?: string | null
  authorization_resource_owner_system_account_id?: string | null
  authorization_resource_id?: string | null
}

interface OpenAIOAuthRefreshCandidateRow {
  id: string
  system_account_id: string
  provider_code: string
  name: string
  type: string
  status: AccountStatus
  credentials_encrypted: string
  proxy_profile_id: string | null
  error_policy_id: string | null
  concurrency_limit: number
  priority: number
  super_priority_enabled: number
  fallback_enabled: number
  schedulable: number
  account_expires_at: string | null
  cooldown_until: string | null
  last_error_code: string | null
  last_error_message: string | null
}

export type { AccountListOptions, AccountOptionListOptions, AccountListSchedulableFilter, AccountListSortDirection, AccountListSortField } from './account-list-options.js'

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
  listAnnouncementsPage,
  listPublicAnnouncements,
  markPublicAnnouncementsRead,
  publishAnnouncement,
  unpublishAnnouncement,
  updateAnnouncement,
  type AnnouncementReadResult,
  type AnnouncementInput,
  type AnnouncementListResult
} from './announcements.repository.js'
export {
  cleanupDeletedAccountDetachedStats,
  cleanupDeletedAccountRelatedRecordData,
  cleanupDeletedAccountRelatedRecordDataAsync,
  cleanupPendingDeletedAccountRecordTargets,
  cleanupPendingDeletedAccountRecordTargetsAsync,
  listDeletedAccountRecordCleanupTargets,
  registerDeletedAccountRecordCleanupTarget,
  type DeletedAccountDetachedStatsCleanupTarget,
  type DeletedAccountRecordCleanupResult,
  type DeletedAccountRecordCleanupTarget,
  type PendingDeletedAccountRecordCleanupSummary
} from './account-record-cleanup.js'
export {
  cleanupDeletedApiKeyRelatedRecordData,
  cleanupDeletedApiKeyRelatedRecordDataAsync,
  cleanupPendingDeletedApiKeyRecordTargets,
  cleanupPendingDeletedApiKeyRecordTargetsAsync,
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
  createSystemAccountAsync,
  createSystemAccountWithPasswordHash,
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
  updateSystemAccountAsync,
  updateSystemAccountLastLogin,
  updateSystemAccountWithPasswordHash,
  verifySystemAccountCredentials,
  verifySystemAccountCredentialsAsync,
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
  isGatewayApiKeyScheduleInactive,
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
  cleanupAuditLogsByRetentionAsync,
  cleanupAuditLogsBefore,
  cleanupAuditLogsBeforeAsync,
  cleanupUnreferencedAuditPayloadBlobs,
  cleanupUnreferencedAuditPayloadBlobsAsync,
  createAuditLogsBatch,
  createAuditLogsBatchAsync,
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
  hasOpenAIAccountAvailabilityScheduleForGroup,
  listOpenAIAccountsForGroup,
  listOpenAIAccountsForGroupResult,
  resolveGroupUsageAccessMetadata,
  selectOpenAIAccountForGroup,
  type GroupUsageAccessMetadata,
  type OpenAIAccountSecret,
  type OpenAIAccountsForGroupResult
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
  const row = getBusinessDatabase()
    .prepare('SELECT system_account_id, authorization_instance_authorization_id FROM accounts WHERE id = ? LIMIT 1')
    .get(accountId) as unknown as { system_account_id?: string; authorization_instance_authorization_id?: string | null } | undefined
  if (!row?.system_account_id) return false
  if (row.authorization_instance_authorization_id) {
    return Boolean(activeResourceAuthorizationById(row.authorization_instance_authorization_id, systemAccountId))
  }
  if (row.system_account_id === systemAccountId) return true
  return Boolean(activeResourceAuthorization('account', accountId, systemAccountId))
}

function authorizationInstanceRuntimeAuthorization(accountId: string, systemAccountId: string, database = getBusinessDatabase()): ResourceAuthorizationRow | undefined {
  const row = database
    .prepare('SELECT authorization_instance_authorization_id FROM accounts WHERE id = ? AND system_account_id = ? LIMIT 1')
    .get(accountId, systemAccountId) as unknown as { authorization_instance_authorization_id?: string | null } | undefined
  return row?.authorization_instance_authorization_id
    ? activeResourceAuthorizationById(row.authorization_instance_authorization_id, systemAccountId)
    : undefined
}

function accountBindingAuthorizationId(accountId: string, systemAccountId: string, account?: AccountSummary): string | undefined {
  if (account?.accountAuthorizationId) {
    return activeResourceAuthorizationById(account.accountAuthorizationId, systemAccountId)?.id
  }
  const instanceAuthorization = authorizationInstanceRuntimeAuthorization(accountId, systemAccountId)
  if (instanceAuthorization?.id) {
    return instanceAuthorization.id
  }
  const ownerId = accountSystemAccountId(accountId)
  if (ownerId && ownerId !== systemAccountId) {
    return activeAccountAuthorization(accountId, systemAccountId)?.id
  }
  return undefined
}

function accountBindingRequiresAuthorization(accountId: string, systemAccountId: string, account?: AccountSummary): boolean {
  if (account?.accessType === 'authorized' || account?.accountAuthorizationId || account?.authorizationInstanceSourceAccountId) {
    return true
  }
  const row = getBusinessDatabase()
    .prepare('SELECT system_account_id, authorization_instance_authorization_id FROM accounts WHERE id = ? LIMIT 1')
    .get(accountId) as unknown as { system_account_id?: string; authorization_instance_authorization_id?: string | null } | undefined
  if (!row?.system_account_id) return false
  return row.system_account_id !== systemAccountId || Boolean(row.authorization_instance_authorization_id)
}

function accountRowForManage(accountId: string, access?: AccessScope): AccountRow | undefined {
  const row = getBusinessDatabase().prepare('SELECT * FROM accounts WHERE id = ?').get(accountId) as unknown as AccountRow | undefined
  if (!row || !canManageResourceOwner(row.system_account_id, access)) {
    return undefined
  }
  return row
}

function accountEnabledGroupId(accountId: string, systemAccountId: string): string | undefined {
  const row = getBusinessDatabase()
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
  const row = getBusinessDatabase()
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
  const authorization = ownerId && ownerId !== systemAccountId ? activeAccountAuthorization(accountId, systemAccountId) : authorizationInstanceRuntimeAuthorization(accountId, systemAccountId)
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
  const activeAuthorizationId = accountOwnerId && systemAccountId && row.authorization_instance_authorization_id
    ? row.authorization_id ?? undefined
    : accountOwnerId && systemAccountId && accountOwnerId !== systemAccountId
    ? row.authorization_id ?? undefined
    : undefined
  return {
    groupId: row.bound_group_id,
    groupName: row.bound_group_name ?? '',
    groupBindStatus: row.bound_group_account_authorization_id && activeAuthorizationId !== row.bound_group_account_authorization_id ? 'authorization_unavailable' : 'bound'
  }
}

function accountResourceFactAccountId(row: AccountListRow): string {
  if (row.access_type !== 'authorized') return row.id
  return row.authorization_instance_source_account_id && row.source_provider_code
    ? row.authorization_instance_source_account_id
    : ''
}

function accountResourceProviderCode(row: AccountListRow): AccountListRow['provider_code'] {
  return row.access_type === 'authorized' && row.source_provider_code
    ? row.source_provider_code
    : row.provider_code
}

function accountResourceType(row: AccountListRow): AccountListRow['type'] {
  return row.access_type === 'authorized' && row.source_type
    ? row.source_type
    : row.type
}

function accountResourceConcurrencyLimit(row: AccountListRow): number {
  if (row.access_type !== 'authorized') return Number(row.concurrency_limit)
  return Number(row.source_concurrency_limit ?? 0)
}

function accountResourceProxyProfileId(row: AccountListRow): string | null {
  return row.access_type === 'authorized' ? row.source_proxy_profile_id ?? null : row.proxy_profile_id
}

function accountResourceErrorPolicyId(row: AccountListRow): string | null {
  return row.access_type === 'authorized' ? row.source_error_policy_id ?? null : row.error_policy_id
}

function accountRuntimeCredentialsFromRow(row: AccountListRow): Record<string, unknown> {
  if (row.access_type === 'authorized') {
    return row.source_credentials_encrypted ? decryptJson<Record<string, unknown>>(row.source_credentials_encrypted) : {}
  }
  return decryptJson<Record<string, unknown>>(row.credentials_encrypted)
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
  const authorization = activeResourceAuthorizationById(input.authorizationId, input.systemAccountId)
    ?? activeAccountAuthorization(input.accountId, input.systemAccountId)
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
    const authorizationUnavailableMessage = authorizedAuthorizationUnavailableMessage(account)
    if (authorizationUnavailableMessage) return authorizationUnavailableMessage
    if (account.authorizationQuotaExceeded) return '授权额度已用完，当前账户不能调用'
    const instanceUnavailableMessage = authorizedAccountInstanceUnavailableMessage(account)
    if (instanceUnavailableMessage) return instanceUnavailableMessage
    if (account.status === 'disabled') return '授权账户已停用，当前不可用'
    if (account.status === 'error') return '授权账户状态异常，当前不可用'
    if (isCoolingAccountStatus(account.status) || isLaterIso(account.cooldownUntil, nowIso())) return '授权账户暂时不可调用，恢复前不会参与调度'
    if (!account.schedulable) return '授权账户暂时不可调用，恢复前不会参与调度'
    return undefined
  }
  if (isAccountExpired(account.accountExpiresAt) || account.lastErrorCode === 'account_expired') return '账户已到期，当前不可用'
  if (account.status === 'disabled') return '账户已停用，当前不可用'
  if (account.status === 'error') return '账户处于异常状态，当前不可用'
  if (isCoolingAccountStatus(account.status) || !account.schedulable || isLaterIso(account.cooldownUntil, nowIso())) return '账户暂时不可调用，恢复前不会参与调度'
  return undefined
}

export function accountTestUnavailableMessage(account: AccountSummary): string | undefined {
  if (account.accessType !== 'authorized') return undefined
  if (account.permissions?.canUse === false) return '当前账户无可用权限'
  if (!account.boundGroupId) return '授权账户需要先绑定到你的分组'
  if (account.groupBindStatus === 'authorization_unavailable') return '当前分组绑定的授权已失效，请重新绑定分组或联系授权人'
  const authorizationUnavailableMessage = authorizedAuthorizationUnavailableMessage(account)
  if (authorizationUnavailableMessage) return authorizationUnavailableMessage
  if (account.authorizationQuotaExceeded) return '授权额度已用完，当前账户不能调用'
  const instanceUnavailableMessage = authorizedAccountInstanceUnavailableMessage(account, { includeRuntimeState: false })
  if (instanceUnavailableMessage) return instanceUnavailableMessage
  if (account.status === 'error') return '账户处于异常状态，当前不可用'
  if (isCoolingAccountStatus(account.status) || isLaterIso(account.cooldownUntil, nowIso())) {
    if (canTestAuthorizedInstanceFailureState(account)) {
      return undefined
    }
    return '账户暂时不可调用，恢复前不会参与调度'
  }
  return undefined
}

function canTestAuthorizedInstanceFailureState(account: AccountSummary): boolean {
  if (account.accessType !== 'authorized' || !account.boundGroupId) return false
  if (account.status === 'active' || account.status === 'disabled') return false
  return isAuthorizedInstanceAvailable(account)
}

function disableExpiredAccounts(access?: AccessScope, limit = maxAccountExpirySweepBatchSize): number {
  const scope = buildSystemAccountScopeClause(access)
  const now = nowIso()
  const requestedBatchSize = Math.trunc(limit)
  const batchSize = Number.isFinite(requestedBatchSize)
    ? Math.max(1, Math.min(requestedBatchSize, maxAccountExpirySweepBatchSize))
    : maxAccountExpirySweepBatchSize
  const database = getBusinessDatabase()
  const rows = database
    .prepare(`
      SELECT id
      FROM accounts
      WHERE account_expires_at IS NOT NULL
        AND account_expires_at <= ?
        AND (
          status <> 'disabled'
          OR schedulable <> 0
          OR cooldown_until IS NOT NULL
          OR last_error_code IS NOT NULL
          OR last_error_message IS NULL
        )${scope.clause}
      ORDER BY account_expires_at ASC, updated_at ASC, id ASC
      LIMIT ?
    `)
    .all(now, ...scope.params, batchSize) as unknown as Array<{ id: string }>
  const expiredIds = rows.map((row) => row.id).filter(Boolean)
  if (!expiredIds.length) return 0

  const result = database
    .prepare(`
      UPDATE accounts
      SET status = 'disabled',
          schedulable = 0,
          cooldown_until = NULL,
          last_error_code = 'account_expired',
          last_error_message = ?,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = NULL,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          updated_at = ?
      WHERE id IN (${sqlPlaceholders(expiredIds.length)})
    `)
    .run('账户套餐已过期，已自动停用', now, ...expiredIds)
  const changed = Number(result.changes ?? 0)
  if (changed > 0) {
    refreshGroupAccountStatsAfterWrite({ all: true, reason: 'account_expired' })
    invalidateGatewayRuntimeAfterBusinessWrite('account_expired')
  }
  return changed
}

const accountStatusValues: readonly AccountStatus[] = ['active', 'disabled', 'error', 'rate_limited', 'temporary_unavailable']
const coolingAccountStatusValues: readonly AccountStatus[] = ['rate_limited', 'temporary_unavailable']

function normalizeAccountStatus(value: unknown): AccountStatus {
  if (typeof value === 'string' && accountStatusValues.includes(value as AccountStatus)) {
    return value as AccountStatus
  }
  throw new Error('账户状态无效')
}

function normalizedAccountStatusInput(value: unknown, fallback: AccountStatus): AccountStatus {
  if (value === undefined) return fallback
  if (typeof value === 'string' && accountStatusValues.includes(value as AccountStatus)) {
    return value as AccountStatus
  }
  throw new Error('账户状态无效')
}

function isCoolingAccountStatus(status: AccountStatus): boolean {
  return coolingAccountStatusValues.includes(status)
}

function isHardUnavailableAccountStatus(status: AccountStatus): boolean {
  return status === 'disabled' || status === 'error'
}

function optionalNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function normalizedAccountType(value: unknown): string {
  if (typeof value !== 'string') {
    throw new Error('账户类型不能为空')
  }
  const accountType = value.trim()
  if (!accountType) {
    throw new Error('账户类型不能为空')
  }
  return accountType
}

function normalizedDispatchPriority(value: unknown): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 0) {
    throw new Error('优先级必须是大于等于 0 的整数')
  }
  return value
}

function normalizedOptionalDispatchPriority(value: unknown, fallback: number): number {
  return value === undefined ? fallback : normalizedDispatchPriority(value)
}

function normalizedPositiveIntegerInput(value: unknown, fallback: number, label: string): number {
  if (value === undefined) return fallback
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1) {
    throw new Error(`${label}必须是大于 0 的整数`)
  }
  return value
}

function normalizeBooleanDispatchInput(value: unknown, fallback: boolean, label: string): boolean {
  if (value === undefined) return fallback
  if (typeof value === 'boolean') return value
  throw new Error(`${label}必须是布尔值`)
}

function hasOwnInput(input: Record<string, unknown>, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(input, key)
}

function normalizeOptionalBooleanInput(input: Record<string, unknown>, key: string, fallback: boolean, label: string): boolean {
  if (!hasOwnInput(input, key)) return fallback
  const value = input[key]
  if (typeof value === 'boolean') return value
  throw new Error(`${label}必须是布尔值`)
}

function normalizeOptionalRequiredTextInput(input: Record<string, unknown>, key: string, fallback: string, label: string): string {
  if (!hasOwnInput(input, key)) return fallback
  return requiredTextInput(input[key], label)
}

function normalizeNullableTextInput(value: unknown, label: string): string | undefined {
  try {
    return optionalNullableString(value) ?? undefined
  } catch {
    throw new Error(`${label}必须是字符串`)
  }
}

function normalizeNullableIdInput(value: unknown, label: string): string | undefined {
  if (value === undefined || value === null) return undefined
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`${label}无效`)
  }
  return value.trim()
}

function assertKnownInputKeys(input: Record<string, unknown>, allowedKeys: ReadonlySet<string>, label: string): void {
  const unknownKeys = Object.keys(input).filter((key) => !allowedKeys.has(key))
  if (unknownKeys.length) {
    throw new Error(`${label}包含未知字段：${unknownKeys.join('、')}`)
  }
}

function normalizeSuperPriorityInput(value: unknown, fallback: boolean): boolean {
  return normalizeBooleanDispatchInput(value, fallback, '超级优先')
}

function normalizeFallbackInput(value: unknown, fallback: boolean): boolean {
  return normalizeBooleanDispatchInput(value, fallback, '降级备用')
}

function authorizationRuntimeBlockingStatus(status?: AuthorizationStatus | null, expiresAt?: string | null): AccountStatus | undefined {
  if (status && status !== 'active') return 'disabled'
  if (isResourceAuthorizationExpired(expiresAt)) return 'disabled'
  return undefined
}

function authorizedAuthorizationUnavailableMessage(account: AccountSummary): string | undefined {
  if (account.accessType !== 'authorized') return undefined
  if (account.authorizationStatus === 'expired' || isResourceAuthorizationExpired(account.authorizationExpiresAt)) return '授权已到期，当前账户不能调用'
  if (account.authorizationStatus === 'paused') return '授权已暂停，当前账户不能调用'
  return undefined
}

function authorizedAccountInstanceUnavailableMessage(account: AccountSummary, options: { includeRuntimeState?: boolean } = {}): string | undefined {
  if (account.accessType !== 'authorized') return undefined
  if (isAccountExpired(account.accountExpiresAt)) return '授权账户已到期，当前不可用'
  if (options.includeRuntimeState === false) return undefined
  return undefined
}

function isAuthorizedInstanceAvailable(account: AccountSummary): boolean {
  return !authorizedAccountInstanceUnavailableMessage(account)
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

function latestIsoText(left?: string | null, right?: string | null): string | undefined {
  if (!left) return right ?? undefined
  if (!right) return left
  return right > left ? right : left
}

function writeSystemAccountId(access?: AccessScope): string {
  return manageableSystemAccountId(access) ?? currentSystemAccountId(access)
}

function validAccountIdsForGroup(providerCode: string, accountIds: string[], systemAccountId: string): string[] {
  const uniqueIds = [...new Set(accountIds)]
  const accountsById = new Map<string, { provider_code?: string }>()
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(uniqueIds, 900)) {
    const rows = database.prepare(`
      SELECT id, provider_code
      FROM accounts
      WHERE system_account_id = ?
        AND id IN (${sqlPlaceholders(chunk.length)})
    `).all(systemAccountId, ...chunk) as Array<{ id?: string; provider_code?: string }>
    for (const row of rows) {
      if (row.id) {
        accountsById.set(row.id, row)
      }
    }
  }
  return uniqueIds.filter((accountId) => {
    const account = accountsById.get(accountId)
    return account?.provider_code === providerCode && canUseAccount(accountId, systemAccountId)
  })
}

function runDelete(sql: string, id: string): boolean {
  const result = getBusinessDatabase().prepare(sql).run(id)
  return result.changes > 0
}

function requiredTextInput(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`${label}不能为空`)
  }
  return value.trim()
}

const apiKeyAccountCredentialKeys = new Set([
  'api_key',
  'base_url',
  'error_handling_rules',
  'stream_intercept_rules'
])

const oauthAccountCredentialKeys = new Set([
  'access_token',
  'refresh_token',
  'expires_at',
  'client_id',
  'id_token',
  'email',
  'account_id',
  'chatgpt_user_id',
  'plan_type',
  'base_url',
  'error_handling_rules',
  'stream_intercept_rules'
])

export function normalizeAccountCredentialsForWrite(accountType: string, value: unknown): Record<string, unknown> {
  const input = accountCredentialsRecord(value)
  assertKnownInputKeys(input, accountCredentialAllowedKeys(accountType), '账户凭据')
  if (accountType === 'api_key') {
    return normalizeApiKeyAccountCredentials(input)
  }
  if (accountType === 'oauth') {
    return normalizeOAuthAccountCredentials(input)
  }
  throw new Error(`账户类型 ${accountType} 不支持凭据写入`)
}

function accountCredentialsRecord(value: unknown): Record<string, unknown> {
  if (value === undefined) return {}
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error('账户凭据必须是对象')
  }
  return value as Record<string, unknown>
}

function accountCredentialAllowedKeys(accountType: string): ReadonlySet<string> {
  if (accountType === 'api_key') return apiKeyAccountCredentialKeys
  if (accountType === 'oauth') return oauthAccountCredentialKeys
  throw new Error(`账户类型 ${accountType} 不支持凭据写入`)
}

function normalizeApiKeyAccountCredentials(input: Record<string, unknown>): Record<string, unknown> {
  const credentials: Record<string, unknown> = {
    api_key: requiredTextInput(input.api_key, 'API Key'),
    base_url: requiredTextInput(input.base_url, 'Base URL')
  }
  normalizeAccountCredentialPolicies(input, credentials)
  return credentials
}

function normalizeOAuthAccountCredentials(input: Record<string, unknown>): Record<string, unknown> {
  const accessToken = optionalCredentialText(input.access_token, 'Access Token')
  const refreshToken = optionalCredentialText(input.refresh_token, 'Refresh Token')
  if (!refreshToken && !accessToken) {
    throw new Error('OAuth 凭据不能为空')
  }

  const credentials: Record<string, unknown> = {
    base_url: requiredTextInput(input.base_url, 'Base URL')
  }
  if (accessToken) credentials.access_token = accessToken
  if (refreshToken) credentials.refresh_token = refreshToken
  const expiresAt = optionalCredentialDateTime(input.expires_at, 'Access Token 到期时间')
  if (expiresAt) credentials.expires_at = expiresAt
  copyOptionalCredentialText(input, credentials, 'client_id', 'OAuth client_id')
  copyOptionalCredentialText(input, credentials, 'id_token', 'OAuth id_token')
  copyOptionalCredentialText(input, credentials, 'email', 'OAuth email')
  copyOptionalCredentialText(input, credentials, 'account_id', 'OpenAI account_id')
  copyOptionalCredentialText(input, credentials, 'chatgpt_user_id', 'OpenAI chatgpt_user_id')
  copyOptionalCredentialText(input, credentials, 'plan_type', 'OpenAI plan_type')
  normalizeAccountCredentialPolicies(input, credentials)
  return credentials
}

function normalizeAccountCredentialPolicies(input: Record<string, unknown>, credentials: Record<string, unknown>): void {
  if (Object.prototype.hasOwnProperty.call(input, 'error_handling_rules')) {
    credentials.error_handling_rules = normalizeAccountErrorHandlingRules(input.error_handling_rules)
  }
  if (Object.prototype.hasOwnProperty.call(input, 'stream_intercept_rules')) {
    credentials.stream_intercept_rules = normalizeAccountStreamInterceptRules(input.stream_intercept_rules)
  }
}

function optionalCredentialText(value: unknown, label: string): string | undefined {
  if (value === undefined) return undefined
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`${label}不能为空`)
  }
  return value.trim()
}

function optionalCredentialDateTime(value: unknown, label: string): string | undefined {
  if (value === undefined) return undefined
  const normalized = optionalServerDateTimeIso(value)
  if (!normalized) {
    throw new Error(`${label}必须是有效时间字符串`)
  }
  return normalized
}

function copyOptionalCredentialText(input: Record<string, unknown>, output: Record<string, unknown>, key: string, label: string): void {
  const value = optionalCredentialText(input[key], label)
  if (value) output[key] = value
}

function requiredAccountCredentialSource(accountType: string, credentials: Record<string, unknown>): string {
  if (accountType === 'oauth') {
    return requiredTextInput(credentials.refresh_token ?? credentials.access_token, 'OAuth 凭据')
  }
  if (accountType === 'api_key') {
    return requiredTextInput(credentials.api_key, 'API Key')
  }
  return requiredTextInput(credentials.api_key ?? credentials.refresh_token ?? credentials.access_token, '账户凭据')
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
  return databaseError.message.includes('UNIQUE constraint failed: accounts.account_identity_fingerprint')
    || databaseError.message.includes('idx_accounts_identity_fingerprint')
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
  const row = getBusinessDatabase()
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
  const row = getBusinessDatabase()
    .prepare(`SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? AND lower(name) = lower(?)${excludeClause} LIMIT 1`)
    .get(...params) as { id?: string } | undefined
  if (row?.id) {
    throw new Error(`同一供应商下分组名称已存在：${name}`)
  }
}

function defaultTemporaryUnschedulableMinutes(): number {
  const value = getSettings().defaultTemporaryUnschedulableMinutes
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value)) {
    throw new Error('defaultTemporaryUnschedulableMinutes 必须是整数')
  }
  if (value < 1 || value > 1440) {
    throw new Error('defaultTemporaryUnschedulableMinutes 必须在 1 到 1440 之间')
  }
  return value
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
  return isCoolingAccountStatus(status) ? new Date(nowMs).toISOString() : undefined
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
  const rows = hydrateAccountRowsWithRuntimeState(listAccountRowsForAccess(access, listOptions), { includeCredentials: true })
  return accountSummariesFromRows(rows, access, viewerSystemAccountId)
}

export function listAccountsPage(access?: AccessScope, options?: AccountListOptions): AccountListResult {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  disableExpiredAccounts(access)
  const listOptions = normalizeAccountListOptions(options)
  const databasePage = listAccountRowsPageForAccess(access, listOptions, { includeCredentials: false })
  const page = {
    rows: hydrateAccountRowsWithRuntimeState(databasePage.rows, { includeCredentials: false }),
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

export function listAccountOptions(access?: AccessScope, options?: AccountOptionListOptions): AccountOptionSummary[] {
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
  const hydratedRows = hydrateAccountRowsWithRuntimeState([row], { includeCredentials: true })
  return accountSummariesFromRows(hydratedRows, access, viewerSystemAccountId)[0]
}

function findInternalAccountSummary(accountId: string): AccountSummary | undefined {
  return findAccountSummary(accountId, internalAccountReadAccess)
}

function accountOptionSummariesFromRows(rows: AccountOptionRow[], access: AccessScope | undefined, viewerSystemAccountId: string | undefined): AccountOptionSummary[] {
  const hasAuthorizedRows = rows.some((row) => row.access_type === 'authorized')
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const accountNames = shouldIncludeSystemAccountFields || hasAuthorizedRows
    ? loadSystemAccountNameMapByIds(rows.flatMap((row) => [
        row.system_account_id,
        row.authorization_resource_owner_system_account_id ?? '',
        row.authorization_instance_owner_system_account_id ?? ''
      ]))
    : new Map<string, string>()
  return rows.map((row) => {
    const isAuthorizedView = row.access_type === 'authorized'
    const effectiveStatus = isAuthorizedView
      ? authorizationRuntimeBlockingStatus(row.authorization_status, row.authorization_expires_at) ?? row.status
      : row.status
    return {
      id: row.id,
      systemAccountId: shouldIncludeSystemAccountFields ? row.system_account_id : undefined,
      systemAccountName: shouldIncludeSystemAccountFields ? accountNames.get(row.system_account_id) : undefined,
      ownerSystemAccountId: isAuthorizedView ? row.authorization_resource_owner_system_account_id ?? row.authorization_instance_owner_system_account_id ?? row.system_account_id : row.system_account_id,
      ownerSystemAccountName: accountNames.get(isAuthorizedView ? row.authorization_resource_owner_system_account_id ?? row.authorization_instance_owner_system_account_id ?? row.system_account_id : row.system_account_id),
      providerCode: row.provider_code,
      name: row.name,
      type: row.type,
      status: effectiveStatus,
      accessType: row.access_type ?? 'owner',
      accountAuthorizationId: row.authorization_id ?? undefined,
      authorizationInstanceSourceAccountId: isAuthorizedView ? row.authorization_instance_source_account_id ?? undefined : undefined,
      authorizationInstanceOwnerSystemAccountId: isAuthorizedView ? row.authorization_instance_owner_system_account_id ?? row.authorization_resource_owner_system_account_id ?? undefined : undefined,
      authorizationStatus: row.authorization_status ?? undefined,
      authorizationExpiresAt: row.authorization_expires_at ?? undefined,
      accountExpiresAt: row.account_expires_at ?? undefined,
      permissions: isAuthorizedView ? authorizedPermissions() : ownerPermissions()
    }
  })
}

function queryAccountOptionRowsForAccess(access: AccessScope | undefined, options: ReturnType<typeof normalizeAccountOptionListOptions>): AccountOptionRow[] {
  const database = getBusinessDatabase()
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
      SELECT ${accountOptionSelectColumns()}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status, NULL AS authorization_expires_at
      FROM accounts
      WHERE 1 = 1${filters.clause}
    `, filters.params)
  }
  if (!viewerSystemAccountId) {
    throw new Error('缺少系统账户上下文')
  }

  const ownerId = ownerSystemAccountId ?? viewerSystemAccountId
  const ownerFilters = buildAccountOptionFilters(options, 'accounts.system_account_id')
  const authorizedFilters = buildAccountOptionFilters(options, '?', [viewerSystemAccountId], true)
  return queryRows(`
      SELECT ${accountOptionSelectColumns()}, 'owner' AS access_type,
      NULL AS authorization_id, NULL AS authorization_status, NULL AS authorization_expires_at,
      NULL AS authorization_resource_owner_system_account_id, NULL AS authorization_resource_id
    FROM accounts
    WHERE accounts.system_account_id = ?
      AND accounts.authorization_instance_authorization_id IS NULL${ownerFilters.clause}
    UNION ALL
    SELECT ${accountOptionSelectColumns()}, 'authorized' AS access_type,
      ra.id AS authorization_id, ra.status AS authorization_status, ra.expires_at AS authorization_expires_at,
      ra.resource_owner_system_account_id AS authorization_resource_owner_system_account_id,
      ra.resource_id AS authorization_resource_id
    FROM accounts
    INNER JOIN resource_authorizations ra ON ra.id = accounts.authorization_instance_authorization_id
    WHERE accounts.system_account_id = ?
      AND ra.resource_type = 'account'
      AND ra.grantee_system_account_id = ?
      AND ra.status IN ('active', 'paused', 'expired')
      AND accounts.authorization_instance_authorization_id IS NOT NULL${authorizedFilters.clause}
  `, [ownerId, ...ownerFilters.params, ownerId, viewerSystemAccountId, ...authorizedFilters.params])
}

function accountOptionSelectColumns(): string {
  return [
    'accounts.id',
    'accounts.system_account_id',
    'accounts.provider_code',
    'accounts.name',
    'accounts.type',
    'accounts.status',
    'accounts.schedulable',
    'accounts.account_expires_at',
    'accounts.cooldown_until',
    'accounts.priority',
    'accounts.created_at',
    'accounts.authorization_instance_source_account_id',
    'accounts.authorization_instance_authorization_id',
    'accounts.authorization_instance_owner_system_account_id'
  ].join(', ')
}

function buildAccountOptionFilters(
  options: ReturnType<typeof normalizeAccountOptionListOptions>,
  groupBindingSystemAccountExpression: string,
  groupBindingSystemAccountParams: string[] = [],
  authorizedView = false
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
  const authorizedStatusExpression = `CASE
    WHEN ra.status <> 'active'
      OR (ra.expires_at IS NOT NULL AND ra.expires_at <= ${currentIsoSql})
    THEN 'disabled'
    WHEN accounts.account_expires_at IS NOT NULL AND accounts.account_expires_at <= ${currentIsoSql} THEN 'disabled'
    WHEN accounts.status IN ('disabled', 'error', 'rate_limited', 'temporary_unavailable') THEN accounts.status
    WHEN accounts.schedulable <> 1 THEN 'disabled'
    WHEN accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until > ${currentIsoSql} THEN 'temporary_unavailable'
    ELSE accounts.status
  END`
  const authorizedBindingAvailableExpression = `option_group_bindings.group_id IS NOT NULL
    AND option_group_bindings.account_authorization_id IS NOT NULL
    AND option_group_bindings.account_authorization_id = ra.id`
  const authorizedBindingUnavailableExpression = `option_group_bindings.group_id IS NULL
    OR option_group_bindings.account_authorization_id IS NULL
    OR option_group_bindings.account_authorization_id <> ra.id`
  const authorizedAuthorizationAvailableExpression = `ra.status = 'active'
    AND (ra.expires_at IS NULL OR ra.expires_at > ${currentIsoSql})`
  const authorizedAccountAvailableExpression = `accounts.schedulable = 1
    AND accounts.status = 'active'
    AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ${currentIsoSql})
    AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ${currentIsoSql})`
  const authorizedAccountHardUnavailableExpression = `accounts.schedulable <> 1
    OR accounts.status IN ('disabled', 'error')
    OR (accounts.account_expires_at IS NOT NULL AND accounts.account_expires_at <= ${currentIsoSql})`
  const authorizedAccountCoolingExpression = `accounts.status IN ('rate_limited', 'temporary_unavailable')
    OR (accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until > ${currentIsoSql})`
  if (statuses.length === 1) {
    clauses.push(authorizedView ? `${authorizedStatusExpression} = ?` : 'accounts.status = ?')
    params.push(statuses[0])
  } else if (statuses.length > 1) {
    clauses.push(authorizedView
      ? `${authorizedStatusExpression} IN (${statuses.map(() => '?').join(', ')})`
      : `accounts.status IN (${statuses.map(() => '?').join(', ')})`)
    params.push(...statuses)
  }
  if (options.schedulable === 'enabled') {
    if (authorizedView) {
      clauses.push(`${authorizedBindingAvailableExpression}
        AND ${authorizedAuthorizationAvailableExpression}
        AND ${authorizedAccountAvailableExpression}`)
    } else {
      clauses.push(`accounts.status = 'active'
        AND accounts.schedulable = 1
        AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ${currentIsoSql})`)
    }
  } else if (options.schedulable === 'disabled') {
    if (authorizedView) {
      clauses.push(`(${authorizedBindingUnavailableExpression}
        OR ${authorizedStatusExpression} IN ('disabled', 'error')
      )`)
    } else {
      clauses.push("(accounts.status = 'disabled' OR accounts.schedulable <> 1)")
    }
  } else if (options.schedulable === 'cooling') {
    if (authorizedView) {
      clauses.push(`${authorizedBindingAvailableExpression}
        AND ${authorizedAuthorizationAvailableExpression}
        AND NOT (${authorizedAccountHardUnavailableExpression})
        AND (${authorizedAccountCoolingExpression})`)
    } else {
      clauses.push(`(accounts.status IN ('rate_limited', 'temporary_unavailable')
        OR (accounts.cooldown_until IS NOT NULL AND accounts.cooldown_until > ${currentIsoSql}))`)
    }
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
  const oauthUsageByAccount = loadOpenAICodexUsageSnapshotsByAccountIds(rows.map((row) => accountResourceFactAccountId(row)))
  const hasAuthorizedRows = rows.some((row) => row.access_type === 'authorized')
  const accountNames = includeSystemAccountFields(access) || hasAuthorizedRows
    ? loadSystemAccountNameMapByIds(rows.flatMap((row) => [
        row.system_account_id,
        row.authorization_resource_owner_system_account_id ?? '',
        row.authorization_instance_owner_system_account_id ?? ''
      ]))
    : new Map<string, string>()
  return rows.map((row) => {
    const isAuthorizedView = row.access_type === 'authorized'
    const usage = isAuthorizedView && row.authorization_id
      ? usageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : usageByAccount.get(row.id) ?? emptyAccountUsageSummary()
    const todayUsage = isAuthorizedView && row.authorization_id
      ? todayUsageByAuthorization.get(row.authorization_id) ?? emptyAccountUsageSummary()
      : todayUsageByAccount.get(row.id) ?? emptyAccountUsageSummary()
    const authorizationStats = authorizationStatsByAccount.get(row.id) ?? { authorizationCount: 0, authorizationTeamCount: 0 }
    const groupBindingSystemAccountId = row.system_account_id
    const groupBinding = groupBindingSystemAccountId
      ? accountGroupBindingFromRow(row, groupBindingSystemAccountId) ?? accountGroupBinding(row.id, groupBindingSystemAccountId)
      : undefined
    const effectiveAuthorizedStatus = isAuthorizedView
      ? authorizationRuntimeBlockingStatus(row.authorization_status, row.authorization_expires_at) ?? row.status
      : row.status
    const effectiveAuthorizedSchedulable = isAuthorizedView
      ? Boolean(groupBinding && groupBinding.groupBindStatus === 'bound')
        && authorizationRuntimeBlockingStatus(row.authorization_status, row.authorization_expires_at) === undefined
        && Boolean(accountResourceFactAccountId(row))
        && row.status === 'active'
        && row.schedulable === 1
        && !isLaterIso(row.cooldown_until ?? undefined, nowIso())
      : row.schedulable === 1
    const displayOwnerSystemAccountId = isAuthorizedView
      ? row.authorization_resource_owner_system_account_id ?? row.authorization_instance_owner_system_account_id ?? row.system_account_id
      : row.system_account_id
    const resourceProviderCode = accountResourceProviderCode(row)
    const resourceType = accountResourceType(row)
    const dispatchPriority = isAuthorizedView ? Number(row.bound_group_local_priority ?? row.priority ?? 0) : row.priority
    const dispatchSuperPriorityEnabled = isAuthorizedView ? row.bound_group_local_super_priority_enabled === 1 : row.super_priority_enabled === 1
    const dispatchFallbackEnabled = isAuthorizedView ? row.bound_group_local_fallback_enabled === 1 : row.fallback_enabled === 1
    const availabilitySchedule = parseAccountAvailabilityScheduleJson(row.availability_schedule_json)
    return {
      id: row.id,
      systemAccountId: includeSystemAccountFields(access) ? row.system_account_id : undefined,
      systemAccountName: includeSystemAccountFields(access) ? accountNames.get(row.system_account_id) : undefined,
      ownerSystemAccountId: displayOwnerSystemAccountId,
      ownerSystemAccountName: accountNames.get(displayOwnerSystemAccountId),
      providerCode: resourceProviderCode,
      name: row.name,
      notes: isAuthorizedView ? undefined : row.notes ?? undefined,
      type: resourceType,
      credentials: accountCredentialsForList(row, includeCredentials),
      status: effectiveAuthorizedStatus,
      concurrencyLimit: accountResourceConcurrencyLimit(row),
      currentConcurrency: currentConcurrencyByAccount.get(row.id) ?? 0,
      priority: dispatchPriority,
      superPriorityEnabled: dispatchSuperPriorityEnabled,
      fallbackEnabled: dispatchFallbackEnabled,
      supportedModels: [...(row.supported_models ?? [])],
      qualityScore: typeof row.quality_score === 'number' ? row.quality_score : undefined,
      qualityState: typeof row.quality_state === 'string' ? row.quality_state : undefined,
      qualityEwmaFirstTokenMs: typeof row.quality_ewma_first_token_ms === 'number' ? row.quality_ewma_first_token_ms : undefined,
      qualityRecentAvgFirstTokenMs: typeof row.quality_recent_avg_first_token_ms === 'number' ? row.quality_recent_avg_first_token_ms : undefined,
      qualityRecentRequestCount: typeof row.quality_recent_request_count === 'number' ? row.quality_recent_request_count : undefined,
      qualityRecentSuccessRate: typeof row.quality_recent_success_rate === 'number' ? row.quality_recent_success_rate : undefined,
      qualityUpdatedAt: row.quality_updated_at ?? undefined,
      proxyProfileId: accountResourceProxyProfileId(row) ?? undefined,
      errorPolicyId: isAuthorizedView ? undefined : accountResourceErrorPolicyId(row) ?? undefined,
      schedulable: effectiveAuthorizedSchedulable,
      availabilitySchedule,
      accountExpiresAt: row.account_expires_at ?? undefined,
      cooldownUntil: row.cooldown_until ?? undefined,
      lastErrorCode: isAuthorizedView ? undefined : row.last_error_code ?? undefined,
      lastErrorMessage: row.last_error_message ?? undefined,
      cooldownRetestFailureCount: isAuthorizedView ? 0 : Math.max(0, Number(row.cooldown_retest_failure_count ?? 0)),
      cooldownRetestObservationStartedAt: isAuthorizedView ? undefined : row.cooldown_retest_observation_started_at ?? undefined,
      cooldownRetestLastAt: isAuthorizedView ? undefined : row.cooldown_retest_last_at ?? undefined,
      cooldownRetestLastStatusCode: isAuthorizedView ? undefined : optionalNumber(row.cooldown_retest_last_status_code),
      streamFailureCount: Math.max(0, Number(row.stream_failure_count ?? 0)),
      streamFailureWindowStartedAt: row.stream_failure_window_started_at ?? undefined,
      lastUsedAt: isAuthorizedView ? usage.lastUsedAt : row.last_used_at ?? undefined,
      todayUsage,
      usage,
      oauthUsage: resourceProviderCode === 'openai' && resourceType === 'oauth' ? oauthUsageByAccount.get(accountResourceFactAccountId(row)) : undefined,
      accessType: row.access_type ?? 'owner',
      accountAuthorizationId: row.authorization_id ?? undefined,
      authorizationInstanceSourceAccountId: isAuthorizedView ? row.authorization_instance_source_account_id ?? undefined : undefined,
      authorizationInstanceOwnerSystemAccountId: isAuthorizedView ? row.authorization_instance_owner_system_account_id ?? row.authorization_resource_owner_system_account_id ?? undefined : undefined,
      boundGroupId: groupBinding?.groupId,
      boundGroupName: groupBinding?.groupName,
      groupBindStatus: groupBinding?.groupBindStatus,
      bindingSystemAccountId: isAuthorizedView && groupBinding ? groupBindingSystemAccountId : undefined,
      authorizationStatus: row.authorization_status ?? undefined,
      authorizationExpiresAt: row.authorization_expires_at ?? undefined,
      authorizationLimits: parseRequestQuotaLimitsJson(row.authorization_limits_json),
      authorizationQuotaExceeded: row.authorization_id ? quotaExceededByAuthorization.get(row.authorization_id) : undefined,
      authorizationSources: row.authorization_id ? sanitizeAuthorizationSourcesForViewer(sourcesByAuthorization.get(row.authorization_id) ?? [], isAuthorizedView) : undefined,
      permissions: isAuthorizedView ? authorizedPermissions() : ownerPermissions(),
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
  const database = getBusinessDatabase()
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
  const accountAccess = access ?? internalAccountReadAccess
  const visibleAccount = findAccountSummary(accountId, accountAccess)
  if (!visibleAccount?.permissions?.canUse) {
    return undefined
  }
  const row = getBusinessDatabase().prepare('SELECT * FROM accounts WHERE id = ?').get(accountId) as unknown as AccountRow | undefined
  if (!row) {
    return undefined
  }
  const resourceRow = row.authorization_instance_source_account_id
    ? getBusinessDatabase().prepare('SELECT * FROM accounts WHERE id = ?').get(row.authorization_instance_source_account_id) as unknown as AccountRow | undefined
    : undefined
  if (row.authorization_instance_authorization_id && !resourceRow) {
    return undefined
  }
  const credentialsRow = resourceRow ?? row
  return {
    ...visibleAccount,
    credentials: decryptJson<Record<string, unknown>>(credentialsRow.credentials_encrypted),
    proxyProfileId: credentialsRow.proxy_profile_id ?? undefined,
    errorPolicyId: credentialsRow.error_policy_id ?? undefined
  }
}

export function listAccountsDueForCooldownRetest(limit = 20): AccountSummary[] {
  disableExpiredAccounts()
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT accounts.*, CASE WHEN accounts.authorization_instance_authorization_id IS NOT NULL THEN 'authorized' ELSE 'owner' END AS access_type,
        accounts.authorization_instance_authorization_id AS authorization_id,
        NULL AS authorization_status
      FROM accounts
      WHERE provider_code = 'openai'
        AND type IN ('api_key', 'oauth')
        AND status IN ('temporary_unavailable', 'rate_limited')
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
  const scheduledRows = rows.filter((row) => isAccountAvailabilityScheduleAllowed(row.availability_schedule_json))
  const hydratedRows = hydrateAccountRowsWithRuntimeState(scheduledRows, { includeCredentials: true })
    .filter((row) => row.access_type !== 'authorized' || Boolean(row.source_provider_code))
  const accountNames = loadSystemAccountNameMapByIds(hydratedRows.map((row) => row.system_account_id))
  const currentConcurrencyByAccount = loadAccountCurrentConcurrencyByIds(hydratedRows.map((row) => row.id))
  const supportedModelsByAccountId = loadSupportedModelsByAccountIds(hydratedRows.map((row) => accountResourceFactAccountId(row)))
  return hydratedRows.map((row) => {
    const groupBinding = accountGroupBinding(row.id, row.system_account_id)
    return {
      id: row.id,
      systemAccountId: row.system_account_id,
      systemAccountName: accountNames.get(row.system_account_id),
      ownerSystemAccountId: row.system_account_id,
      ownerSystemAccountName: accountNames.get(row.system_account_id),
      providerCode: accountResourceProviderCode(row),
      name: row.name,
      notes: row.notes ?? undefined,
      type: accountResourceType(row),
      credentials: accountRuntimeCredentialsFromRow(row),
      status: row.status,
      concurrencyLimit: accountResourceConcurrencyLimit(row),
      currentConcurrency: currentConcurrencyByAccount.get(row.id) ?? 0,
      priority: row.priority,
      superPriorityEnabled: row.super_priority_enabled === 1,
      fallbackEnabled: row.fallback_enabled === 1,
      supportedModels: supportedModelsByAccountId.get(accountResourceFactAccountId(row)) ?? [],
      proxyProfileId: accountResourceProxyProfileId(row) ?? undefined,
      errorPolicyId: accountResourceErrorPolicyId(row) ?? undefined,
      schedulable: row.schedulable === 1,
      availabilitySchedule: parseAccountAvailabilityScheduleJson(row.availability_schedule_json),
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

export function listOpenAIOAuthAccountsDueForAccessTokenRefresh(input: {
  leadSeconds: number
  limit: number
  stoppedErrorCode: string
}): AccountSummary[] {
  const leadMs = Math.max(0, Math.trunc(input.leadSeconds)) * 1000
  const dueBefore = new Date(Date.now() + leadMs).toISOString()
  const limit = Math.max(1, Math.min(Math.trunc(input.limit), 500))
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT id, system_account_id, provider_code, name, type, status, credentials_encrypted,
        proxy_profile_id, error_policy_id, concurrency_limit, priority,
        super_priority_enabled, fallback_enabled, schedulable, account_expires_at, cooldown_until,
        last_error_code, last_error_message
      FROM accounts
      WHERE authorization_instance_authorization_id IS NULL
        AND provider_code = 'openai'
        AND type = 'oauth'
        AND oauth_refresh_token_present = 1
        AND (status <> 'error' OR last_error_code IS NULL OR last_error_code <> ?)
        AND (oauth_access_token_expires_at IS NULL OR oauth_access_token_expires_at <= ?)
      ORDER BY oauth_access_token_expires_at IS NOT NULL ASC, oauth_access_token_expires_at ASC, updated_at ASC, id ASC
      LIMIT ?
    `)
    .all(input.stoppedErrorCode, dueBefore, limit) as unknown as OpenAIOAuthRefreshCandidateRow[]
  return openAIOAuthRefreshCandidateSummaries(rows)
}

export function listOpenAIOAuthStoppedRefreshExceptionAccounts(input: {
  stoppedErrorCode: string
  limit?: number
}): AccountSummary[] {
  const limit = Math.max(1, Math.min(Math.trunc(input.limit ?? 200), 500))
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT id, system_account_id, provider_code, name, type, status, credentials_encrypted,
        proxy_profile_id, error_policy_id, concurrency_limit, priority,
        super_priority_enabled, fallback_enabled, schedulable, account_expires_at, cooldown_until,
        last_error_code, last_error_message
      FROM accounts
      WHERE authorization_instance_authorization_id IS NULL
        AND provider_code = 'openai'
        AND type = 'oauth'
        AND status = 'error'
        AND last_error_code = ?
      ORDER BY updated_at ASC, id ASC
      LIMIT ?
    `)
    .all(input.stoppedErrorCode, limit) as unknown as OpenAIOAuthRefreshCandidateRow[]
  return openAIOAuthRefreshCandidateSummaries(rows)
}

function openAIOAuthRefreshCandidateSummaries(rows: OpenAIOAuthRefreshCandidateRow[]): AccountSummary[] {
  return rows.map((row) => ({
    id: row.id,
    systemAccountId: row.system_account_id,
    providerCode: 'openai',
    name: row.name,
    type: 'oauth',
    credentials: decryptJson<Record<string, unknown>>(row.credentials_encrypted),
    status: row.status,
    concurrencyLimit: row.concurrency_limit,
    currentConcurrency: 0,
    priority: row.priority,
    superPriorityEnabled: row.super_priority_enabled === 1,
    fallbackEnabled: row.fallback_enabled === 1,
    supportedModels: [],
    proxyProfileId: row.proxy_profile_id ?? undefined,
    errorPolicyId: row.error_policy_id ?? undefined,
    schedulable: row.schedulable === 1,
    accountExpiresAt: row.account_expires_at ?? undefined,
    cooldownUntil: row.cooldown_until ?? undefined,
    lastErrorCode: row.last_error_code ?? undefined,
    lastErrorMessage: row.last_error_message ?? undefined,
    todayUsage: emptyAccountUsageSummary(),
    usage: emptyAccountUsageSummary(),
    accessType: 'owner' as const,
    permissions: ownerPermissions()
  }))
}

function openAIOAuthRefreshMetadata(accountType: string, credentials: Record<string, unknown>): {
  accessTokenExpiresAt: string | null
  refreshTokenPresent: boolean
} {
  if (accountType !== 'oauth') {
    return { accessTokenExpiresAt: null, refreshTokenPresent: false }
  }
  const refreshToken = optionalString(credentials.refresh_token)
  const accessToken = optionalString(credentials.access_token)
  const expiresAt = accessToken ? optionalServerDateTimeIso(credentials.expires_at) : undefined
  return {
    accessTokenExpiresAt: expiresAt ?? null,
    refreshTokenPresent: Boolean(refreshToken)
  }
}

const accountCreateInputKeys = new Set([
  'providerCode',
  'name',
  'type',
  'credentials',
  'supportedModels',
  'status',
  'concurrencyLimit',
  'priority',
  'superPriorityEnabled',
  'fallbackEnabled',
  'proxyProfileId',
  'errorPolicyId',
  'schedulable',
  'groupId',
  'accountExpiresAt',
  'availabilitySchedule',
  'notes'
])

const accountUpdateInputKeys = new Set([
  'name',
  'credentials',
  'supportedModels',
  'status',
  'concurrencyLimit',
  'priority',
  'superPriorityEnabled',
  'fallbackEnabled',
  'proxyProfileId',
  'errorPolicyId',
  'schedulable',
  'accountExpiresAt',
  'availabilitySchedule',
  'notes'
])

export function createAccount(input: Record<string, unknown>, access?: AccessScope): AccountSummary {
  assertKnownInputKeys(input, accountCreateInputKeys, '账户创建参数')
  const nowMs = Date.now()
  const now = new Date(nowMs).toISOString()
  const id = newId('acc')
  const providerCode = requiredTextInput(input.providerCode, '供应商')
  const explicitGroupId = hasOwnInput(input, 'groupId') ? normalizeNullableIdInput(input.groupId, '账户分组') : undefined
  const explicitGroup = explicitGroupId ? groupOwnerAndProvider(explicitGroupId) : undefined
  const requestedSystemAccountId = writeSystemAccountId(access)
  const systemAccountId = explicitGroup && canManageResourceOwner(explicitGroup.systemAccountId, access) ? explicitGroup.systemAccountId : requestedSystemAccountId
  const provider = listProviders().find((item) => item.code === providerCode)
  if (!provider) {
    throw new Error(`不支持的供应商：${providerCode}`)
  }
  const accountType = normalizedAccountType(input.type)
  if (!provider.accountTypes.includes(accountType as AccountType)) {
    throw new Error(`供应商 ${providerCode} 不支持账户类型 ${accountType}`)
  }
  const credentials = normalizeAccountCredentialsForWrite(accountType, input.credentials)
  const credentialSource = requiredAccountCredentialSource(accountType, credentials)
  const baseUrl = requiredTextInput(credentials.base_url, 'Base URL')
  const credentialFingerprint = typeof credentialSource === 'string' && credentialSource.trim()
    ? accountCredentialFingerprint(credentialSource)
    : null
  const accountIdentity = typeof credentialSource === 'string' && credentialSource.trim()
    ? accountIdentityFingerprint({ providerCode, type: accountType, baseUrl, secret: credentialSource })
    : null
  const oauthRefreshMetadata = openAIOAuthRefreshMetadata(accountType, credentials)
  const accountExpiresAt = hasOwnInput(input, 'accountExpiresAt')
    ? nullableServerDateTimeIso(input.accountExpiresAt, '账户套餐到期时间')
    : null
  const availabilitySchedule = accountAvailabilityScheduleFromRequest(input)
  const supportedModels = normalizeAccountSupportedModelsForProvider(input.supportedModels, providerCode) ?? []
  const initialStatus = normalizedAccountStatusInput(input.status, 'active')
  const expiredByPackage = isAccountExpired(accountExpiresAt)
  const nextStatus = expiredByPackage ? 'disabled' : initialStatus
  const initialCooldownUntil = initialCooldownUntilForStatus(initialStatus, nowMs)
  const initialObservationStartedAt = expiredByPackage ? undefined : cooldownRetestObservationStartedAtForStatus(initialStatus, nowMs)
  const groupId = explicitGroupId
  if (!groupId) {
    throw new Error('账户分组不能为空')
  }
  const group = explicitGroupId === groupId ? explicitGroup : groupOwnerAndProvider(groupId)
  if (!group || group.systemAccountId !== systemAccountId || group.providerCode !== providerCode) {
    throw new Error('账户分组无效')
  }
  const proxyProfileId = globalProxyProfileId(normalizeNullableIdInput(input.proxyProfileId, '代理配置'))
  const createSuperPriorityEnabled = normalizeSuperPriorityInput(input.superPriorityEnabled, false)
  const createFallbackEnabled = normalizeFallbackInput(input.fallbackEnabled, false)
  if (nextStatus !== 'active' && (createSuperPriorityEnabled || createFallbackEnabled)) {
    throw new Error('只有正常状态的账户可以设置超级优先或降级备用')
  }
  if (createSuperPriorityEnabled && createFallbackEnabled) {
    throw new Error('超级优先和降级备用不能同时开启')
  }
  const createSchedulable = normalizeOptionalBooleanInput(input, 'schedulable', true, '账户是否参与调度')
  const account: AccountSummary = {
    id,
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields(access) ? loadSystemAccountNameMapByIds([systemAccountId]).get(systemAccountId) : undefined,
    providerCode,
    name: requiredTextInput(input.name, '账户名称'),
    notes: normalizeNullableTextInput(input.notes, '账户备注'),
    type: accountType,
    credentials,
    status: nextStatus,
    concurrencyLimit: normalizedPositiveIntegerInput(input.concurrencyLimit, DEFAULT_ACCOUNT_CONCURRENCY_LIMIT, '并发限制'),
    currentConcurrency: 0,
    priority: normalizedOptionalDispatchPriority(input.priority, 0),
    superPriorityEnabled: createSuperPriorityEnabled,
    fallbackEnabled: createFallbackEnabled,
    supportedModels,
    proxyProfileId,
    errorPolicyId: normalizeNullableIdInput(input.errorPolicyId, '错误处理策略'),
    schedulable: expiredByPackage || isHardUnavailableAccountStatus(nextStatus) ? false : createSchedulable,
    availabilitySchedule,
    accountExpiresAt: accountExpiresAt ?? undefined,
    cooldownUntil: expiredByPackage ? undefined : initialCooldownUntil,
    lastErrorCode: expiredByPackage ? 'account_expired' : undefined,
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

  const database = getBusinessDatabase()
  assertAccountNameAvailable(systemAccountId, providerCode, account.name)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database
      .prepare(`
        INSERT INTO accounts (
          id, system_account_id, provider_code, name, type, status, credentials_encrypted, credential_fingerprint, account_identity_fingerprint, credential_mask,
          oauth_access_token_expires_at, oauth_refresh_token_present, proxy_profile_id, concurrency_limit, error_policy_id,
          priority, super_priority_enabled, fallback_enabled, schedulable, availability_schedule_json, notes, account_expires_at, cooldown_until, last_error_code, last_error_message,
          cooldown_retest_observation_started_at, stream_failure_count, stream_failure_window_started_at, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
        accountIdentity,
        maskSecret(credentialSource),
        oauthRefreshMetadata.accessTokenExpiresAt,
        oauthRefreshMetadata.refreshTokenPresent ? 1 : 0,
        account.proxyProfileId ?? null,
        account.concurrencyLimit,
        account.errorPolicyId ?? null,
        account.priority,
        account.superPriorityEnabled ? 1 : 0,
        account.fallbackEnabled ? 1 : 0,
        account.schedulable ? 1 : 0,
        accountAvailabilityScheduleJson(account.availabilitySchedule),
        account.notes ?? null,
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
      .prepare(`
        INSERT INTO group_accounts (
          system_account_id, group_id, account_id,
          local_priority, local_super_priority_enabled, local_fallback_enabled,
          enabled, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)
      `)
      .run(
        systemAccountId,
        groupId,
        account.id,
        account.priority,
        account.superPriorityEnabled ? 1 : 0,
        account.fallbackEnabled ? 1 : 0,
        now,
        now
      )
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
  assertKnownInputKeys(input, accountUpdateInputKeys, '账户更新参数')
  const current = findAccountSummary(id, access)
  if (!current) {
    return undefined
  }
  if (current.accessType === 'authorized' || current.accountAuthorizationId) {
    return undefined
  }
  const systemAccountId = accountSystemAccountId(id)
  if (!systemAccountId) {
    throw new Error('账户归属数据异常，请清理后再编辑')
  }
  if (!canManageResourceOwner(systemAccountId, access)) {
    return undefined
  }
  const credentials = hasOwnInput(input, 'credentials')
    ? normalizeAccountCredentialsForWrite(current.type, input.credentials)
    : normalizeAccountCredentialsForWrite(current.type, current.credentials)
  const credentialSource = requiredAccountCredentialSource(current.type, credentials)
  const baseUrl = requiredTextInput(credentials.base_url, 'Base URL')
  const credentialFingerprint = typeof credentialSource === 'string' && credentialSource.trim()
    ? accountCredentialFingerprint(credentialSource)
    : null
  const accountIdentity = typeof credentialSource === 'string' && credentialSource.trim()
    ? accountIdentityFingerprint({ providerCode: current.providerCode, type: current.type, baseUrl, secret: credentialSource })
    : null
  const oauthRefreshMetadata = openAIOAuthRefreshMetadata(current.type, credentials)
  const hasAccountExpiresAtInput = hasOwnInput(input, 'accountExpiresAt')
  const nextAccountExpiresAt = hasAccountExpiresAtInput
    ? nullableServerDateTimeIso(input.accountExpiresAt, '账户套餐到期时间')
    : current.accountExpiresAt ?? null
  const expiredByPackage = isAccountExpired(nextAccountExpiresAt)

  const hasSupportedModelsInput = hasOwnInput(input, 'supportedModels')
  const nextSupportedModels = hasSupportedModelsInput
    ? normalizeAccountSupportedModelsForProvider(input.supportedModels, current.providerCode) ?? []
    : current.supportedModels ?? []
  const hasAvailabilityScheduleInput = isAccountAvailabilityScheduleInputPresent(input)
  const nextAvailabilitySchedule = hasAvailabilityScheduleInput
    ? accountAvailabilityScheduleFromRequest(input)
    : current.availabilitySchedule
  const hasPriorityInput = hasOwnInput(input, 'priority')
  const hasNotesInput = hasOwnInput(input, 'notes')
  const rawErrorPolicyId = hasOwnInput(input, 'errorPolicyId')
    ? input.errorPolicyId
    : undefined

  const hasStatusInput = hasOwnInput(input, 'status')
  const requestedStatus = hasStatusInput ? normalizedAccountStatusInput(input.status, current.status) : current.status
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
        nextLastErrorMessage = undefined
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
    nextLastErrorCode = 'account_expired'
    nextLastErrorMessage = '账户套餐已过期，已自动停用'
    nextCooldownRetestObservationStartedAt = undefined
    clearCooldownRetestState = true
  }
  const hasSuperPriorityInput = hasOwnInput(input, 'superPriorityEnabled')
  const requestedSuperPriority = normalizeSuperPriorityInput(
    input.superPriorityEnabled,
    current.superPriorityEnabled
  )
  if (hasSuperPriorityInput && requestedSuperPriority && nextStatus !== 'active' && !current.superPriorityEnabled) {
    throw new Error('只有正常状态的账户可以设置超级优先')
  }
  let nextSuperPriorityEnabled = requestedSuperPriority
  const hasFallbackInput = hasOwnInput(input, 'fallbackEnabled')
  const requestedFallback = normalizeFallbackInput(
    input.fallbackEnabled,
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

  const requestedSchedulable = normalizeOptionalBooleanInput(input, 'schedulable', current.schedulable, '账户是否参与调度')
  const next: AccountSummary = {
    ...current,
    name: normalizeOptionalRequiredTextInput(input, 'name', current.name, '账户名称'),
    notes: hasNotesInput ? normalizeNullableTextInput(input.notes, '账户备注') : current.notes,
    credentials,
    status: nextStatus,
    concurrencyLimit: normalizedPositiveIntegerInput(input.concurrencyLimit, current.concurrencyLimit, '并发限制'),
    priority: normalizedOptionalDispatchPriority(input.priority, current.priority),
    superPriorityEnabled: nextSuperPriorityEnabled,
    fallbackEnabled: nextFallbackEnabled,
    supportedModels: nextSupportedModels,
    proxyProfileId: hasOwnInput(input, 'proxyProfileId')
      ? globalProxyProfileId(normalizeNullableIdInput(input.proxyProfileId, '代理配置'))
      : current.proxyProfileId,
    errorPolicyId: rawErrorPolicyId === undefined ? current.errorPolicyId : normalizeNullableIdInput(rawErrorPolicyId, '错误处理策略'),
    schedulable: expiredByPackage || isHardUnavailableAccountStatus(nextStatus)
      ? false
      : hasStatusInput
        ? true
        : requestedSchedulable,
    availabilitySchedule: nextAvailabilitySchedule,
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
  const database = getBusinessDatabase()
  const updatedAt = nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  let renamedAuthorizationInstanceIds: string[] = []
  try {
    const result = database
      .prepare(`
      UPDATE accounts
      SET name = ?, notes = ?, status = ?, credentials_encrypted = ?, credential_fingerprint = ?, account_identity_fingerprint = ?, credential_mask = ?,
            oauth_access_token_expires_at = ?, oauth_refresh_token_present = ?,
            proxy_profile_id = ?, concurrency_limit = ?,
            error_policy_id = ?, priority = ?, super_priority_enabled = ?, fallback_enabled = ?, schedulable = ?, availability_schedule_json = ?, account_expires_at = ?, cooldown_until = ?, last_error_code = ?, last_error_message = ?,
            cooldown_retest_failure_count = ?, cooldown_retest_observation_started_at = ?, cooldown_retest_last_at = ?, cooldown_retest_last_status_code = ?, updated_at = ?
        WHERE id = ? AND system_account_id = ?
      `)
      .run(
        next.name,
        next.notes ?? null,
        next.status,
        encryptJson(credentials),
        credentialFingerprint,
        accountIdentity,
        maskSecret(credentialSource),
        oauthRefreshMetadata.accessTokenExpiresAt,
        oauthRefreshMetadata.refreshTokenPresent ? 1 : 0,
        next.proxyProfileId ?? null,
        next.concurrencyLimit,
        next.errorPolicyId ?? null,
        next.priority,
        next.superPriorityEnabled ? 1 : 0,
        next.fallbackEnabled ? 1 : 0,
        next.schedulable ? 1 : 0,
        accountAvailabilityScheduleJson(next.availabilitySchedule),
        next.accountExpiresAt ?? null,
        next.cooldownUntil ?? null,
        next.lastErrorCode ?? null,
        next.lastErrorMessage ?? null,
        next.cooldownRetestFailureCount ?? 0,
        next.cooldownRetestObservationStartedAt ?? null,
        next.cooldownRetestLastAt ?? null,
        next.cooldownRetestLastStatusCode ?? null,
        updatedAt,
        id,
        systemAccountId
    )
    if (Number(result.changes ?? 0) > 0 && next.name !== current.name) {
      renamedAuthorizationInstanceIds = syncAccountAuthorizationInstanceNamesForSourceAccount(database, id, next.name, next.providerCode, updatedAt)
    }
    if (Number(result.changes ?? 0) > 0 && hasSupportedModelsInput) {
      replaceAccountSupportedModels(id, next.providerCode, nextSupportedModels)
    }
    if (Number(result.changes ?? 0) > 0 && (hasPriorityInput || hasSuperPriorityInput || hasFallbackInput)) {
      database.prepare(`
        UPDATE group_accounts
        SET local_priority = ?,
            local_super_priority_enabled = ?,
            local_fallback_enabled = ?,
            updated_at = ?
        WHERE account_id = ?
          AND system_account_id = ?
          AND enabled = 1
      `).run(
        next.priority,
        next.superPriorityEnabled ? 1 : 0,
        next.fallbackEnabled ? 1 : 0,
        updatedAt,
        id,
        systemAccountId
      )
    }
    commitDatabaseTransaction(database, transactionStarted)
    if (Number(result.changes ?? 0) > 0) {
      refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_updated' })
      invalidateAccountLookupCache(id)
      for (const instanceId of renamedAuthorizationInstanceIds) {
        invalidateAccountLookupCache(instanceId)
      }
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
  authorization_instance_authorization_id?: string | null
}

export function deleteAccountWithRelatedCleanup(id: string, access?: AccessScope): AccountDeleteResult {
  const scope = buildSystemAccountScopeClause(access)
  const database = getBusinessDatabase()
  const row = database
    .prepare(`SELECT id, system_account_id, authorization_instance_authorization_id FROM accounts WHERE id = ?${scope.clause}`)
    .get(id, ...scope.params) as unknown as AccountDeleteRow | undefined
  if (!row) {
    return { deleted: false }
  }
  if (row.authorization_instance_authorization_id) {
    return { deleted: false }
  }
  const cleanupTarget = buildDeletedAccountCleanupTarget(database, row)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    detachAuthorizationInstancesFromDeletedSourceAccount(database, row.id)
    const result = database.prepare('DELETE FROM accounts WHERE id = ? AND system_account_id = ?').run(row.id, row.system_account_id)
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
  void database
  return {
    accountId: row.id,
    systemAccountId: row.system_account_id,
    authorizationIds: [],
    teamScopeIds: []
  }
}

function detachAuthorizationInstancesFromDeletedSourceAccount(database: DatabaseSync, sourceAccountId: string): void {
  const updatedAt = nowIso()
  database
    .prepare(`
      UPDATE accounts
      SET authorization_instance_source_account_id = NULL,
          updated_at = ?
      WHERE authorization_instance_source_account_id = ?
    `)
    .run(updatedAt, sourceAccountId)
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
  const accountAccess = access ?? internalAccountReadAccess
  const current = findAccountSummary(id, accountAccess)
  if (!current) {
    return { changed: false }
  }
  const ownerSystemAccountId = accountSystemAccountId(id)
  if (ownerSystemAccountId && !canManageResourceOwner(ownerSystemAccountId, accountAccess)) {
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
    const result = getBusinessDatabase()
      .prepare(`
        UPDATE accounts
        SET status = 'disabled',
            schedulable = 0,
            cooldown_until = NULL,
            last_error_code = 'account_expired',
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
    return { account: findAccountSummary(id, accountAccess), changed }
  }

  const result = getBusinessDatabase()
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

  return { account: findAccountSummary(id, accountAccess), changed }
}

export function clearAuthorizedAccountBindingFailureState(
  id: string,
  access?: AccessScope
): AccountFailureStateClearResult {
  const current = findAccountSummary(id, access)
  if (!current || current.accessType !== 'authorized' || !current.boundGroupId || !current.accountAuthorizationId) {
    return { account: current, changed: false }
  }
  if (current.status === 'disabled') {
    return { account: current, changed: false }
  }
  const hasFailureState = current.status !== 'active'
    || Boolean(current.cooldownUntil)
    || Boolean(current.lastErrorMessage)
    || Boolean(current.streamFailureCount)
    || Boolean(current.streamFailureWindowStartedAt)
  if (!hasFailureState) {
    return { account: current, changed: false }
  }
  return clearAuthorizedAccountBindingFailureStateByContext({
    accountId: id,
    systemAccountId: authorizedBindingSystemAccountId(access),
    groupId: current.boundGroupId,
    accountAuthorizationId: current.accountAuthorizationId
  })
}

export interface AuthorizedAccountBindingRuntimeTarget {
  accountId: string
  systemAccountId?: string
  groupId?: string
  accountAuthorizationId?: string
}

export interface AccountPrecheckMutationState {
  status: AccountStatus
  updatedAt?: string
  lastUsedAt?: string
}

function normalizedAuthorizedAccountBindingRuntimeTarget(
  input: AuthorizedAccountBindingRuntimeTarget
): Required<AuthorizedAccountBindingRuntimeTarget> | undefined {
  const accountId = input.accountId?.trim()
  const systemAccountId = input.systemAccountId?.trim()
  const groupId = input.groupId?.trim()
  const accountAuthorizationId = input.accountAuthorizationId?.trim()
  if (!accountId || !systemAccountId || !groupId || !accountAuthorizationId) {
    return undefined
  }
  return { accountId, systemAccountId, groupId, accountAuthorizationId }
}

function authorizedAccountRuntimeBindingExists(target: Required<AuthorizedAccountBindingRuntimeTarget>): boolean {
  const row = getBusinessDatabase()
    .prepare(`
      SELECT accounts.id
      FROM accounts
      INNER JOIN group_accounts
        ON group_accounts.account_id = accounts.id
        AND group_accounts.system_account_id = ?
        AND group_accounts.group_id = ?
        AND group_accounts.enabled = 1
        AND group_accounts.account_authorization_id = ?
      WHERE accounts.id = ?
        AND accounts.system_account_id = ?
        AND accounts.authorization_instance_authorization_id = ?
      LIMIT 1
    `)
    .get(target.systemAccountId, target.groupId, target.accountAuthorizationId, target.accountId, target.systemAccountId, target.accountAuthorizationId) as unknown as { id?: string } | undefined
  return Boolean(row?.id)
}

export function getAccountPrecheckMutationState(input: {
  accountId: string
  authorizedBinding?: AuthorizedAccountBindingRuntimeTarget
}): AccountPrecheckMutationState | undefined {
  const target = input.authorizedBinding
    ? normalizedAuthorizedAccountBindingRuntimeTarget(input.authorizedBinding)
    : undefined
  if (target) {
    const row = getBusinessDatabase()
      .prepare(`
        SELECT
          accounts.status,
          accounts.updated_at,
          accounts.last_used_at
        FROM group_accounts
        INNER JOIN accounts ON accounts.id = group_accounts.account_id
        WHERE group_accounts.account_id = ?
          AND group_accounts.system_account_id = ?
          AND group_accounts.group_id = ?
          AND group_accounts.enabled = 1
          AND group_accounts.account_authorization_id = ?
          AND accounts.system_account_id = ?
          AND accounts.authorization_instance_authorization_id = ?
        LIMIT 1
      `)
      .get(target.accountId, target.systemAccountId, target.groupId, target.accountAuthorizationId, target.systemAccountId, target.accountAuthorizationId) as unknown as {
        status?: AccountStatus | null
        updated_at?: string | null
        last_used_at?: string | null
      } | undefined
    if (!row) {
      return undefined
    }
    return {
      status: normalizeAccountStatus(row.status),
      updatedAt: row.updated_at ?? undefined,
      lastUsedAt: row.last_used_at ?? undefined
    }
  }

  const row = getBusinessDatabase()
    .prepare('SELECT status, updated_at, last_used_at FROM accounts WHERE id = ? LIMIT 1')
    .get(input.accountId) as unknown as {
      status?: AccountStatus | null
      updated_at?: string | null
      last_used_at?: string | null
    } | undefined
  if (!row) {
    return undefined
  }
  return {
    status: normalizeAccountStatus(row.status),
    updatedAt: row.updated_at ?? undefined,
    lastUsedAt: row.last_used_at ?? undefined
  }
}

export function clearAuthorizedAccountBindingFailureStateByContext(
  input: AuthorizedAccountBindingRuntimeTarget,
  options: ClearAccountFailureStateOptions = {}
): AccountFailureStateClearResult {
  const target = normalizedAuthorizedAccountBindingRuntimeTarget(input)
  if (!target) {
    return { changed: false }
  }
  const now = nowIso()
  const result = getBusinessDatabase()
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
        AND system_account_id = ?
        AND authorization_instance_authorization_id = ?
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
        AND EXISTS (
          SELECT 1
          FROM group_accounts
          WHERE group_accounts.account_id = accounts.id
            AND group_accounts.system_account_id = ?
            AND group_accounts.group_id = ?
            AND group_accounts.enabled = 1
            AND group_accounts.account_authorization_id = ?
        )
    `)
    .run(now, target.accountId, target.systemAccountId, target.accountAuthorizationId, options.allowErrorRestore === false ? 0 : 1, target.systemAccountId, target.groupId, target.accountAuthorizationId)
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    refreshGroupAccountStatsAfterWrite({ groupIds: [target.groupId], accountIds: [target.accountId], reason: 'authorized_account_restored' })
    invalidateAccountLookupCache(target.accountId)
    invalidateGatewayRuntimeAfterBusinessWrite('authorized_account_restored')
  }
  return {
    account: findAccountSummary(target.accountId, { systemAccountId: target.systemAccountId, role: 'user' }),
    changed
  }
}

export function markAuthorizedAccountBindingCooldownByContext(
  input: AuthorizedAccountBindingRuntimeTarget & { cooldownUntil: string; reason: string; status?: AccountStatus }
): AccountSummary | undefined {
  const target = normalizedAuthorizedAccountBindingRuntimeTarget(input)
  if (!target) {
    return undefined
  }
  const cooldownStatus: AccountStatus = input.status === 'rate_limited' ? 'rate_limited' : 'temporary_unavailable'
  const cooldownUntil = cooldownStatus === 'temporary_unavailable'
    ? initialTemporaryUnavailableCooldownUntil()
    : input.cooldownUntil
  const now = nowIso()
  const result = getBusinessDatabase()
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
        AND system_account_id = ?
        AND authorization_instance_authorization_id = ?
        AND status NOT IN ('disabled', 'error')
        AND EXISTS (
          SELECT 1
          FROM group_accounts
          WHERE group_accounts.account_id = accounts.id
            AND group_accounts.system_account_id = ?
            AND group_accounts.group_id = ?
            AND group_accounts.enabled = 1
            AND group_accounts.account_authorization_id = ?
        )
    `)
    .run(cooldownStatus, cooldownUntil, input.reason || null, cooldownRetestObservationStartedAtForStatus(cooldownStatus) ?? null, now, target.accountId, target.systemAccountId, target.accountAuthorizationId, target.systemAccountId, target.groupId, target.accountAuthorizationId)
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  refreshGroupAccountStatsAfterWrite({ groupIds: [target.groupId], accountIds: [target.accountId], reason: 'authorized_account_cooldown' })
  invalidateAccountLookupCache(target.accountId)
  invalidateGatewayRuntimeAfterBusinessWrite('authorized_account_cooldown')
  return findAccountSummary(target.accountId, { systemAccountId: target.systemAccountId, role: 'user' })
}

export function markAuthorizedAccountBindingDisabledByFailure(
  input: AuthorizedAccountBindingRuntimeTarget & { reason: string }
): AccountSummary | undefined {
  const target = normalizedAuthorizedAccountBindingRuntimeTarget(input)
  if (!target) {
    return undefined
  }
  const now = nowIso()
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'error',
          schedulable = 0,
          cooldown_until = NULL,
          last_error_code = 'upstream_failure',
          last_error_message = ?,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = NULL,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
        AND system_account_id = ?
        AND authorization_instance_authorization_id = ?
        AND status <> 'disabled'
        AND EXISTS (
          SELECT 1
          FROM group_accounts
          WHERE group_accounts.account_id = accounts.id
            AND group_accounts.system_account_id = ?
            AND group_accounts.group_id = ?
            AND group_accounts.enabled = 1
            AND group_accounts.account_authorization_id = ?
        )
    `)
    .run(input.reason || null, now, target.accountId, target.systemAccountId, target.accountAuthorizationId, target.systemAccountId, target.groupId, target.accountAuthorizationId)
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  refreshGroupAccountStatsAfterWrite({ groupIds: [target.groupId], accountIds: [target.accountId], reason: 'authorized_account_exception' })
  invalidateAccountLookupCache(target.accountId)
  invalidateGatewayRuntimeAfterBusinessWrite('authorized_account_exception')
  return findAccountSummary(target.accountId, { systemAccountId: target.systemAccountId, role: 'user' })
}

export function clearAuthorizedAccountBindingStreamFailureState(input: AuthorizedAccountBindingRuntimeTarget): boolean {
  const target = normalizedAuthorizedAccountBindingRuntimeTarget(input)
  if (!target) {
    return false
  }
  const result = getBusinessDatabase()
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
        AND system_account_id = ?
        AND authorization_instance_authorization_id = ?
        AND status NOT IN ('disabled', 'error')
        AND (
          stream_failure_count > 0
          OR stream_failure_window_started_at IS NOT NULL
          OR (status = 'active' AND last_error_code IS NOT NULL)
          OR (status = 'active' AND last_error_message IS NOT NULL)
        )
        AND EXISTS (
          SELECT 1
          FROM group_accounts
          WHERE group_accounts.account_id = accounts.id
            AND group_accounts.system_account_id = ?
            AND group_accounts.group_id = ?
            AND group_accounts.enabled = 1
            AND group_accounts.account_authorization_id = ?
        )
    `)
    .run(nowIso(), target.accountId, target.systemAccountId, target.accountAuthorizationId, target.systemAccountId, target.groupId, target.accountAuthorizationId)
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    invalidateAccountLookupCache(target.accountId)
    invalidateGatewayRuntimeAfterBusinessWrite('authorized_account_stream_failure_cleared')
  }
  return changed
}

export function clearAccountStreamFailureState(id: string): boolean {
  const result = getBusinessDatabase()
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
  action: 'retry_immediately' | 'cooldown' | 'exception' | 'discard'
  changed: boolean
  failureCount: number
  account?: AccountSummary
  cooldownUntil?: string
  backoffSeconds?: number
  backoffMinutes?: number
  recoveryStage?: 'fast' | 'slow'
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
  const current = findInternalAccountSummary(id)
  const errorCode = normalizedCooldownRetestErrorCode(input)
  const testErrorMessage = normalizedCooldownRetestErrorMessage(input, errorCode)
  if (!current || !isCoolingAccountStatus(current.status)) {
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

  if (recovery.observationElapsedSeconds >= recovery.maxRecoverySeconds) {
    const exceptionMessage = cooldownRetestExceptionMessage(failureCount, recovery.maxRecoverySeconds, testErrorMessage)
    const result = getBusinessDatabase()
      .prepare(`
        UPDATE accounts
        SET status = 'error',
            schedulable = 0,
            cooldown_until = NULL,
            last_error_code = 'cooldown_retest_max_recovery_exceeded',
            last_error_message = ?,
            cooldown_retest_failure_count = ?,
            cooldown_retest_observation_started_at = COALESCE(cooldown_retest_observation_started_at, ?),
            cooldown_retest_last_at = ?,
            cooldown_retest_last_status_code = ?,
            stream_failure_count = 0,
            stream_failure_window_started_at = NULL,
            updated_at = ?
        WHERE id = ?
          AND status = ?
      `)
      .run(exceptionMessage, failureCount, observationStartedAt, now, lastStatusCode, now, id, current.status)
    const changed = Number(result.changes ?? 0) > 0
    if (changed) {
      refreshGroupAccountStatsAfterWrite({ accountIds: [id], reason: 'account_cooldown_retest_exception' })
      invalidateAccountLookupCache(id)
      invalidateGatewayRuntimeAfterBusinessWrite('account_cooldown_retest_exception')
    }
    return {
      action: 'exception',
      changed,
      failureCount,
      account: failureAccountSummary(id, current),
      recoveryStage: recovery.stage,
      fastThresholdSeconds: recovery.fastThresholdSeconds,
      maxPauseSeconds: recovery.maxPauseSeconds,
      maxRecoverySeconds: recovery.maxRecoverySeconds,
      maxedFailureCount: recovery.maxedFailureCount,
      observationStartedAt: recovery.observationStartedAt,
      observationElapsedSeconds: recovery.observationElapsedSeconds,
      errorCode: 'cooldown_retest_max_recovery_exceeded',
      errorMessage: exceptionMessage
    }
  }

  const cooldownUntil = new Date(nowDate.getTime() + recovery.backoffSeconds * 1000).toISOString()
  const cooldownMessage = cooldownRetestCooldownMessage(failureCount, recovery.backoffSeconds, recovery.stage, testErrorMessage)
  const result = getBusinessDatabase()
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
        AND status = ?
    `)
    .run(cooldownUntil, errorCode, cooldownMessage, failureCount, observationStartedAt, now, lastStatusCode, now, id, current.status)
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
  return findInternalAccountSummary(id) ?? fallback
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

function cooldownRetestExceptionMessage(failureCount: number, maxRecoverySeconds: number, lastError: string): string {
  return `后台冷却复测连续失败 ${failureCount} 次，已超过最大恢复观察窗口 ${formatDurationSeconds(maxRecoverySeconds)}，已停止自动复测并标记为异常；最后错误：${lastError}`.slice(0, 1000)
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
  const parsed = typeof value === 'number' ? value : NaN
  if (!Number.isFinite(parsed)) return fallback
  return Math.min(Math.max(Math.trunc(parsed), min), max)
}

export function markAccountTestTemporaryUnavailable(
  account: AccountSummary,
  reason: string,
  access?: AccessScope
): AccountSummary | undefined {
  const current = findAccountSummary(account.id, access)
  if (!current || (current.status !== 'active' && !isCoolingAccountStatus(current.status))) {
    return undefined
  }
  if (current.status === 'active' && !current.schedulable) {
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
  if (!account.boundGroupId || !account.accountAuthorizationId) {
    return undefined
  }
  const systemAccountId = authorizedBindingSystemAccountId(access)
  return markAuthorizedAccountBindingCooldownByContext({
    accountId: account.id,
    systemAccountId,
    groupId: account.boundGroupId,
    accountAuthorizationId: account.accountAuthorizationId,
    cooldownUntil,
    reason,
    status: 'temporary_unavailable'
  })
}

export function markAccountCooldown(id: string, until: string, reason: string, status: AccountStatus = 'temporary_unavailable'): AccountSummary | undefined {
  const current = findInternalAccountSummary(id)
  if (!current) {
    return undefined
  }
  if (isHardUnavailableAccountStatus(current.status)) {
    return undefined
  }

  const expiredByPackage = isAccountExpired(current.accountExpiresAt)
  if (expiredByPackage) {
    const result = getBusinessDatabase()
      .prepare(`
        UPDATE accounts
        SET status = 'disabled',
            schedulable = 0,
            cooldown_until = NULL,
            last_error_code = 'account_expired',
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
    return findInternalAccountSummary(id)
  }

  const cooldownStatus: AccountStatus = status === 'rate_limited' ? 'rate_limited' : 'temporary_unavailable'
  const cooldownNowMs = Date.now()
  const cooldownNow = new Date(cooldownNowMs).toISOString()
  const cooldownUntil = cooldownStatus === 'temporary_unavailable'
    ? initialTemporaryUnavailableCooldownUntil(cooldownNowMs)
    : until
  const cooldownObservationStartedAt = cooldownRetestObservationStartedAtForStatus(cooldownStatus, cooldownNowMs)

  const result = getBusinessDatabase()
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

  return findInternalAccountSummary(id)
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
  const database = getBusinessDatabase()
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
  input: { status?: 'active' | 'disabled'; priority?: number; superPriorityEnabled?: boolean; fallbackEnabled?: boolean; clearFailureState?: boolean },
  access?: AccessScope
): AccountSummary | undefined {
  const systemAccountId = authorizedBindingSystemAccountId(access)
  const current = findAccountSummary(accountId, access)
  if (current?.accessType !== 'authorized' || !current.boundGroupId || !current.accountAuthorizationId) {
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
  const hasPriorityInput = Object.prototype.hasOwnProperty.call(input, 'priority')
  const nextPriority = hasPriorityInput ? normalizedDispatchPriority(input.priority) : current.priority
  const nextSuperPriority = hasSuperPriorityInput ? input.superPriorityEnabled === true : current.superPriorityEnabled
  const nextFallback = hasFallbackInput ? input.fallbackEnabled === true : current.fallbackEnabled
  if (nextSuperPriority && nextFallback) {
    throw new Error('超级优先和降级备用不能同时开启')
  }
  const hasStatusInput = Object.prototype.hasOwnProperty.call(input, 'status')
  const nextStatus: AccountStatus = hasStatusInput
    ? input.status === 'disabled' ? 'disabled' : 'active'
    : input.clearFailureState === true
      ? 'active'
      : current.status
  const shouldClearFailureState = input.clearFailureState === true || hasStatusInput
  const nextSchedulable = hasStatusInput
    ? input.status === 'disabled' ? 0 : 1
    : input.clearFailureState === true
      ? 1
      : current.schedulable ? 1 : 0
  const now = nowIso()
  const database = getBusinessDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    let accountChanges = 0
    if (shouldClearFailureState) {
      const result = database.prepare(`
        UPDATE accounts
        SET status = ?,
            schedulable = ?,
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
          AND system_account_id = ?
          AND authorization_instance_authorization_id = ?
          AND EXISTS (
            SELECT 1
            FROM group_accounts
            WHERE group_accounts.account_id = accounts.id
              AND group_accounts.system_account_id = ?
              AND group_accounts.group_id = ?
              AND group_accounts.enabled = 1
              AND group_accounts.account_authorization_id = ?
          )
      `)
        .run(nextStatus, nextSchedulable, now, accountId, systemAccountId, current.accountAuthorizationId, systemAccountId, current.boundGroupId, current.accountAuthorizationId)
      accountChanges = Number(result.changes ?? 0)
      if (accountChanges <= 0) {
        rollbackDatabaseTransaction(database, transactionStarted)
        return undefined
      }
    }
    const dispatchResult = database.prepare(`
        UPDATE group_accounts
        SET local_priority = ?,
            local_super_priority_enabled = ?,
            local_fallback_enabled = ?,
            updated_at = ?
        WHERE account_id = ?
          AND system_account_id = ?
          AND group_id = ?
          AND enabled = 1
          AND account_authorization_id = ?
      `)
      .run(nextPriority, nextSuperPriority ? 1 : 0, nextFallback ? 1 : 0, now, accountId, systemAccountId, current.boundGroupId, current.accountAuthorizationId)
    if (Number(dispatchResult.changes ?? 0) <= 0 && accountChanges <= 0) {
      rollbackDatabaseTransaction(database, transactionStarted)
      return undefined
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshGroupAccountStatsAfterWrite({ groupIds: [current.boundGroupId], accountIds: [accountId], reason: 'authorized_binding_dispatch' })
  invalidateAccountLookupCache(accountId)
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
  if (!sourceAccount.boundGroupId || !sourceAccount.accountAuthorizationId || !targetAccount.boundGroupId || sourceAccount.boundGroupId !== targetAccount.boundGroupId) {
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
  const sourceStatus: AccountStatus = input.sourceStatus === 'disabled' ? 'disabled' : 'temporary_unavailable'
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET status = ?,
          schedulable = ?,
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
        AND system_account_id = ?
        AND authorization_instance_authorization_id = ?
        AND EXISTS (
          SELECT 1
          FROM group_accounts
          WHERE group_accounts.account_id = accounts.id
            AND group_accounts.system_account_id = ?
            AND group_accounts.group_id = ?
            AND group_accounts.enabled = 1
            AND group_accounts.account_authorization_id = ?
        )
    `)
    .run(
      sourceStatus,
      sourceStatus === 'disabled' ? 0 : 1,
      sourceCooldownUntil,
      manualTrafficMigrationReason,
      cooldownRetestObservationStartedAtForStatus(sourceStatus) ?? null,
      now,
      sourceAccount.id,
      systemAccountId,
      sourceAccount.accountAuthorizationId,
      systemAccountId,
      sourceAccount.boundGroupId,
      sourceAccount.accountAuthorizationId
    )
  if (Number(result.changes ?? 0) <= 0) {
    return undefined
  }
  refreshGroupAccountStatsAfterWrite({ groupIds: [sourceAccount.boundGroupId], accountIds: [sourceAccount.id], reason: 'authorized_binding_migration' })
  invalidateAccountLookupCache(sourceAccount.id)
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
  const current = findInternalAccountSummary(id)
  if (!current) {
    return undefined
  }
  if (current.status === 'disabled' && options.preserveDisabled !== false) {
    return undefined
  }

  const result = getBusinessDatabase()
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

  return findInternalAccountSummary(id)
}

export function markAccountDisabledByFailure(id: string, reason: string): AccountSummary | undefined {
  const current = findInternalAccountSummary(id)
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
  const row = getBusinessDatabase().prepare('SELECT id, status, stream_failure_count, stream_failure_window_started_at FROM accounts WHERE id = ?').get(input.accountId) as unknown as AccountFailureRow | undefined
  if (!row) {
    return { count: 0, triggered: false }
  }
  if (isHardUnavailableAccountStatus(row.status)) {
    return { count: Math.max(0, row.stream_failure_count), triggered: false, account: findInternalAccountSummary(input.accountId) }
  }

  const now = new Date()
  const nowIsoValue = now.toISOString()
  const thresholdMs = Math.max(1, input.thresholdWindowMinutes) * 60_000
  const startedAt = row.stream_failure_window_started_at ? new Date(row.stream_failure_window_started_at) : undefined
  const windowValid = startedAt !== undefined && !Number.isNaN(startedAt.getTime()) && now.getTime() - startedAt.getTime() < thresholdMs
  const count = windowValid ? Math.max(0, row.stream_failure_count) + 1 : 1
  const windowStartedAt = windowValid ? row.stream_failure_window_started_at : nowIsoValue

  getBusinessDatabase()
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
    return { count, triggered: false, account: findInternalAccountSummary(input.accountId) }
  }

  if (input.action === 'cooldown') {
    const until = new Date(now.getTime() + Math.max(1, input.cooldownMinutes) * 60_000).toISOString()
    markAccountCooldown(input.accountId, until, input.reason)
  } else {
    markAccountDisabledByFailure(input.accountId, input.reason)
  }

  getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          updated_at = ?
      WHERE id = ?
    `)
    .run(nowIsoValue, input.accountId)
  refreshGroupAccountStatsAfterWrite({ accountIds: [input.accountId], reason: 'stream_failure_threshold' })

  return { count, triggered: true, account: findInternalAccountSummary(input.accountId) }
}

export function recordAuthorizedAccountBindingStreamFailure(input: AuthorizedAccountBindingRuntimeTarget & {
  thresholdCount: number
  thresholdWindowMinutes: number
  action: 'cooldown' | 'disable' | 'none'
  cooldownMinutes: number
  reason: string
}): { count: number; triggered: boolean; account?: AccountSummary } {
  const target = normalizedAuthorizedAccountBindingRuntimeTarget(input)
  if (!target || !authorizedAccountRuntimeBindingExists(target)) {
    return { count: 0, triggered: false }
  }
  const result = recordAccountStreamFailure({
    accountId: target.accountId,
    thresholdCount: input.thresholdCount,
    thresholdWindowMinutes: input.thresholdWindowMinutes,
    action: input.action,
    cooldownMinutes: input.cooldownMinutes,
    reason: input.reason
  })
  return {
    count: result.count,
    triggered: result.triggered,
    account: findAccountSummary(target.accountId, { systemAccountId: target.systemAccountId, role: 'user' }) ?? result.account
  }
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

export function listGroupOptions(access?: AccessScope, options?: GroupOptionListOptions): GroupOptionSummary[] {
  return buildGroupOptionSummaries(listGroupOptionRowsForAccess(access, options), access)
}

export function listAccountGroupOptions(access?: AccessScope, options?: GroupOptionListOptions): AccountGroupOptionSummary[] {
  const rows = listGroupOptionRowsForAccess(access, options)
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
      authorizationExpiresAt: row.authorization_expires_at ?? undefined,
      authorizationLimits: parseRequestQuotaLimitsJson(row.authorization_limits_json),
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
    const accountStats = groupAccountStatsFromRow(groupStatsByGroup.get(row.id), todayUsage, totalUsage)
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
      description: row.description ?? undefined,
      enabled: row.enabled === 1,
      isDefault: isAuthorizedView ? false : row.is_default === 1,
      groupType: groupTypeFromRow(row),
      schedulingPolicy: groupSchedulingPolicyFromRow(row),
      accountIds: isAuthorizedView ? [] : accountIdsByGroup.get(row.id) ?? [],
      accountStats,
      accessType: row.access_type ?? 'owner',
      groupAuthorizationId: row.authorization_id ?? undefined,
      authorizationStatus: row.authorization_status ?? undefined,
      authorizationExpiresAt: row.authorization_expires_at ?? undefined,
      authorizationLimits: parseRequestQuotaLimitsJson(row.authorization_limits_json),
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
  return hasOwnInput(input, 'schedulingPolicy')
}

function groupSchedulingPolicyInput(input: Record<string, unknown>): unknown {
  return input.schedulingPolicy
}

const groupCreateInputKeys = new Set([
  'name',
  'providerCode',
  'description',
  'enabled',
  'groupType',
  'schedulingPolicy'
])

const groupUpdateInputKeys = new Set([
  'name',
  'providerCode',
  'description',
  'enabled',
  'groupType',
  'schedulingPolicy'
])

export function createGroup(input: Record<string, unknown>, access?: AccessScope): GroupSummary {
  assertKnownInputKeys(input, groupCreateInputKeys, '分组创建参数')
  const now = nowIso()
  const systemAccountId = writeSystemAccountId(access)
  const providerCode = requiredTextInput(input.providerCode, '供应商')
  const groupType = normalizeGroupType(input.groupType)
  const schedulingPolicyJson = groupSchedulingPolicyJson(groupSchedulingPolicyInput(input), groupType)
  const name = requiredTextInput(input.name, '分组名称')
  const enabled = normalizeOptionalBooleanInput(input, 'enabled', true, '分组启用状态')
  assertGroupNameAvailable(systemAccountId, providerCode, name)
  const group: GroupSummary = {
    id: newId('grp'),
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields(access) ? loadSystemAccountNameMapByIds([systemAccountId]).get(systemAccountId) : undefined,
    name,
    providerCode,
    description: normalizeNullableTextInput(input.description, '分组说明'),
    enabled,
    isDefault: false,
    groupType,
    schedulingPolicy: parseGroupSchedulingPolicyJson(schedulingPolicyJson, groupType),
    accountIds: [],
    accountStats: emptyGroupAccountStats()
  }
  try {
    getBusinessDatabase()
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
  assertKnownInputKeys(input, groupUpdateInputKeys, '分组更新参数')
  const current = findGroupSummary(id, access)
  if (!current) {
    return undefined
  }
  if (current.isDefault) {
    throw new DefaultGroupReadonlyError()
  }
  const systemAccountId = groupOwnerAndProvider(id)?.systemAccountId
  if (!systemAccountId) {
    throw new Error('分组归属数据异常，请清理后再编辑')
  }
  if (!canManageResourceOwner(systemAccountId, access)) {
    return undefined
  }
  const hasDescriptionInput = hasOwnInput(input, 'description')
  const hasGroupTypeInput = hasOwnInput(input, 'groupType')
  const hasSchedulingPolicyInput = hasGroupSchedulingPolicyInput(input)
  const nextGroupType = hasGroupTypeInput ? normalizeGroupType(input.groupType) : current.groupType
  const nextSchedulingPolicyInput = hasSchedulingPolicyInput ? groupSchedulingPolicyInput(input) : current.schedulingPolicy
  const next: GroupSummary = {
    ...current,
    name: normalizeOptionalRequiredTextInput(input, 'name', current.name, '分组名称'),
    providerCode: normalizeOptionalRequiredTextInput(input, 'providerCode', current.providerCode, '供应商'),
    description: hasDescriptionInput ? normalizeNullableTextInput(input.description, '分组说明') : current.description,
    enabled: normalizeOptionalBooleanInput(input, 'enabled', current.enabled, '分组启用状态'),
    groupType: nextGroupType,
    schedulingPolicy: parseGroupSchedulingPolicyJson(groupSchedulingPolicyJson(nextSchedulingPolicyInput, nextGroupType), nextGroupType)
  }
  if (next.providerCode !== current.providerCode && current.accountStats.total > 0) {
    throw new Error('已有账户的分组不允许修改供应商')
  }
  assertGroupNameAvailable(systemAccountId, next.providerCode, next.name, id)
  const database = getBusinessDatabase()
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

export interface DeletedGroupApiKeyRouteChange {
  apiKeyId: string
  apiKeyName: string
  removedGroupId: string
  removedGroupName?: string
  removedBindingStatus?: string
}

export interface DeleteGroupResult {
  deleted: boolean
  affectedApiKeyRoutes: DeletedGroupApiKeyRouteChange[]
}

export function deleteGroup(id: string, access?: AccessScope): DeleteGroupResult {
  const current = findGroupSummary(id, access)
  if (current?.isDefault) {
    throw new Error('默认分组不能删除')
  }
  const owner = groupOwnerAndProvider(id)
  if (!owner || !canManageResourceOwner(owner.systemAccountId, access)) {
    return { deleted: false, affectedApiKeyRoutes: [] }
  }
  const database = getBusinessDatabase()
  let deleted = false
  const affectedApiKeyRoutes = preserveApiKeyRoutesBeforeGroupDelete(database, id, owner.systemAccountId, current?.name)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database.prepare('DELETE FROM api_key_group_bindings WHERE group_id = ? AND system_account_id = ?').run(id, owner.systemAccountId)
    const result = database.prepare('DELETE FROM groups WHERE id = ? AND system_account_id = ?').run(id, owner.systemAccountId)
    deleted = Number(result.changes ?? 0) > 0
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  if (deleted) {
    refreshGroupAccountStatsAfterWrite({ groupIds: [id], reason: 'group_deleted' })
    invalidateGroupLookupCache(id)
    invalidateGroupAccountIdsCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('group_deleted')
  }
  return { deleted, affectedApiKeyRoutes: deleted ? affectedApiKeyRoutes : [] }
}

type ApiKeyAffectedByGroupDeleteRow = {
  id: string
  name: string
  targetBindingStatus?: string | null
}

function preserveApiKeyRoutesBeforeGroupDelete(
  database: DatabaseSync,
  groupId: string,
  systemAccountId: string,
  groupName?: string
): DeletedGroupApiKeyRouteChange[] {
  const affectedApiKeys = database
    .prepare(`
      SELECT
        api_key_group_bindings.api_key_id AS id,
        api_keys.name,
        api_key_group_bindings.status AS targetBindingStatus
      FROM api_key_group_bindings
      INNER JOIN api_keys
        ON api_keys.id = api_key_group_bindings.api_key_id
        AND api_keys.system_account_id = api_key_group_bindings.system_account_id
      WHERE api_key_group_bindings.system_account_id = ?
        AND api_key_group_bindings.group_id = ?
      ORDER BY api_key_group_bindings.api_key_id ASC
      LIMIT ?
    `)
    .all(systemAccountId, groupId, maxGroupDeleteAffectedApiKeyRoutes + 1) as unknown as ApiKeyAffectedByGroupDeleteRow[]
  if (!affectedApiKeys.length) return []
  if (affectedApiKeys.length > maxGroupDeleteAffectedApiKeyRoutes) {
    throw new Error(`该分组关联的 API Key 超过 ${maxGroupDeleteAffectedApiKeyRoutes} 个，请先分批解除绑定后再删除分组`)
  }

  const activeBindingCountByApiKeyId = loadActiveApiKeyGroupCountExcludingGroup(
    database,
    groupId,
    systemAccountId,
    affectedApiKeys.map((apiKey) => apiKey.id)
  )
  const blockers = affectedApiKeys.filter((apiKey) => {
    if (apiKey.targetBindingStatus !== 'active') return false
    return (activeBindingCountByApiKeyId.get(apiKey.id) ?? 0) === 0
  })
  if (blockers.length) {
    const names = blockers.slice(0, 3).map((apiKey) => apiKey.name).join('、')
    const suffix = blockers.length > 3 ? ` 等 ${blockers.length} 个` : ''
    throw new Error(`删除分组前，请先为以下 API Key 添加或启用其他分组：${names}${suffix}`)
  }

  return affectedApiKeys.map((apiKey) => {
    return {
      apiKeyId: apiKey.id,
      apiKeyName: apiKey.name,
      removedGroupId: groupId,
      removedGroupName: groupName,
      removedBindingStatus: apiKey.targetBindingStatus ?? undefined
    }
  })
}

function loadActiveApiKeyGroupCountExcludingGroup(
  database: DatabaseSync,
  groupId: string,
  systemAccountId: string,
  apiKeyIds: string[]
): Map<string, number> {
  const result = new Map<string, number>()
  const uniqueIds = [...new Set(apiKeyIds.filter(Boolean))]
  for (const chunk of chunkValues(uniqueIds, 500)) {
    const rows = database
      .prepare(`
        SELECT
          api_key_group_bindings.api_key_id AS apiKeyId,
          COUNT(*) AS activeBindingCount
        FROM api_key_group_bindings
        INNER JOIN groups
          ON groups.id = api_key_group_bindings.group_id
          AND groups.system_account_id = api_key_group_bindings.system_account_id
          AND groups.enabled = 1
        WHERE api_key_group_bindings.system_account_id = ?
          AND api_key_group_bindings.status = 'active'
          AND api_key_group_bindings.group_id <> ?
          AND api_key_group_bindings.api_key_id IN (${sqlPlaceholders(chunk.length)})
        GROUP BY api_key_group_bindings.api_key_id
    `)
      .all(systemAccountId, groupId, ...chunk) as unknown as Array<{ apiKeyId: string; activeBindingCount: number }>
    for (const row of rows) {
      result.set(row.apiKeyId, Number(row.activeBindingCount) || 0)
    }
  }
  return result
}

export function setAccountGroup(
  accountId: string,
  groupId: string | null,
  access?: AccessScope
): AccountSummary | undefined {
  const database = getBusinessDatabase()
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
  const accountAuthorizationId = accountBindingAuthorizationId(accountId, group.systemAccountId, current)
  if (accountBindingRequiresAuthorization(accountId, group.systemAccountId, current) && !accountAuthorizationId) {
    return undefined
  }

  const previousGroupId = accountEnabledGroupId(accountId, group.systemAccountId)
  database.prepare('DELETE FROM group_accounts WHERE account_id = ? AND system_account_id = ?').run(accountId, group.systemAccountId)
  const now = nowIso()
  database
    .prepare(`
      INSERT INTO group_accounts (
        system_account_id, group_id, account_id, account_authorization_id,
        local_priority, local_super_priority_enabled, local_fallback_enabled,
        enabled, created_at, updated_at
      )
      VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
      ON CONFLICT(group_id, account_id) DO UPDATE SET
        account_authorization_id = excluded.account_authorization_id,
        local_priority = excluded.local_priority,
        local_super_priority_enabled = excluded.local_super_priority_enabled,
        local_fallback_enabled = excluded.local_fallback_enabled,
        enabled = 1,
        updated_at = excluded.updated_at
    `)
    .run(
      group.systemAccountId,
      groupId,
      accountId,
      accountAuthorizationId ?? null,
      current.priority,
      current.superPriorityEnabled ? 1 : 0,
      current.fallbackEnabled ? 1 : 0,
      now,
      now
    )
  refreshGroupAccountStatsAfterWrite({ groupIds: [previousGroupId, groupId], reason: 'group_account_binding' })
  if (previousGroupId && previousGroupId !== groupId) {
    invalidateGroupAccountIdsCache(previousGroupId)
  }
  invalidateGroupAccountIdsCache(groupId)
  invalidateGatewayRuntimeAfterBusinessWrite('group_account_binding')

  return findAccountSummary(accountId, { systemAccountId: group.systemAccountId, role: 'user' })
}

export function addAccountToGroup(groupId: string, accountId: string): GroupSummary | undefined {
  const database = getBusinessDatabase()
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
  const account = findAccountSummary(accountId, { systemAccountId: current.systemAccountId, role: 'user' })
  if (!account) {
    return undefined
  }
  const accountAuthorizationId = accountBindingAuthorizationId(accountId, current.systemAccountId)
  if (accountBindingRequiresAuthorization(accountId, current.systemAccountId) && !accountAuthorizationId) {
    return undefined
  }
  const now = nowIso()
  const previousGroupId = accountEnabledGroupId(accountId, current.systemAccountId)
  database.prepare('DELETE FROM group_accounts WHERE account_id = ? AND system_account_id = ?').run(accountId, current.systemAccountId)
  database
    .prepare(`
      INSERT INTO group_accounts (
        system_account_id, group_id, account_id, account_authorization_id,
        local_priority, local_super_priority_enabled, local_fallback_enabled,
        enabled, created_at, updated_at
      )
      VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
      ON CONFLICT(group_id, account_id) DO UPDATE SET
        account_authorization_id = excluded.account_authorization_id,
        local_priority = excluded.local_priority,
        local_super_priority_enabled = excluded.local_super_priority_enabled,
        local_fallback_enabled = excluded.local_fallback_enabled,
        enabled = 1,
        updated_at = excluded.updated_at
    `)
    .run(
      current.systemAccountId,
      groupId,
      accountId,
      accountAuthorizationId ?? null,
      account.priority,
      account.superPriorityEnabled ? 1 : 0,
      account.fallbackEnabled ? 1 : 0,
      now,
      now
    )
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
  const rows = getBusinessDatabase()
    .prepare(`SELECT id, name, description, status, created_by, created_at, updated_at FROM system_teams${whereClause} ORDER BY status ASC, updated_at DESC, name ASC, id ASC${pageClause}`)
    .all(...params, ...pageParams) as unknown as SystemTeamRow[]
  return { rows }
}

function normalizeSystemTeamListOptions(options: SystemTeamListOptions = {}): NormalizedSystemTeamListOptions {
  const rawPage = options.page
  const rawPageSize = options.pageSize
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(maxSystemTeamListPageSize, Math.max(1, rawPageSize))
    : 20
  const page = normalizeListPage(rawPage, pageSize)
  return {
    page,
    pageSize,
    keyword: optionalString(options.keyword)
  }
}

export function findSystemTeamSummary(id: string, access?: AccessScope): SystemTeamSummary | undefined {
  const scopedId = scopedSystemAccountId(access)
  const row = scopedId
    ? getBusinessDatabase()
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
    : getBusinessDatabase().prepare('SELECT * FROM system_teams WHERE id = ?').get(id) as unknown as SystemTeamRow | undefined
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
  const database = getBusinessDatabase()
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
  const database = getBusinessDatabase()
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

interface AuthorizationGranteeGroupOptionListOptions extends AuthorizationPrincipalOptionListOptions {
  granteeSystemAccountId?: string
  providerCode?: string
  preferDefault?: boolean
}

export function listAuthorizationGranteeGroups(access?: AccessScope, options: AuthorizationGranteeGroupOptionListOptions = {}): GroupOptionSummary[] {
  void access
  const granteeSystemAccountId = optionalString(options.granteeSystemAccountId)
  if (!granteeSystemAccountId) return []
  const grantee = findSystemAccountById(granteeSystemAccountId)
  if (!grantee || grantee.status !== 'active') return []
  const filter = buildAuthorizationGranteeGroupFilter(options, granteeSystemAccountId)
  const limitClause = authorizationPrincipalOptionLimitClause(options.limit)
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT ${authorizationGranteeGroupSelectColumns()}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
      FROM groups
      ${filter.clause}
      ${options.preferDefault === false ? 'ORDER BY groups.updated_at DESC, groups.id DESC' : 'ORDER BY groups.is_default DESC, groups.updated_at DESC, groups.id DESC'}
      ${limitClause.clause}
    `)
    .all(...filter.params, ...limitClause.params) as unknown as GroupListRow[]
  return buildGroupOptionSummaries(rows, access).map((group) => ({
    ...group,
    permissions: authorizedPermissions()
  }))
}

function authorizationGranteeGroupSelectColumns(): string {
  return [
    'id',
    'system_account_id',
    'name',
    'provider_code',
    'description',
    'enabled',
    'is_default',
    'group_type',
    'scheduling_policy_json',
    'created_at',
    'updated_at'
  ].map((column) => `groups.${column}`).join(', ')
}

function buildSystemAccountPrincipalFilter(options: AuthorizationPrincipalOptionListOptions): { clause: string; params: string[] } {
  return buildPrincipalFilter(options, buildSystemAccountPrincipalKeywordFilter)
}

function buildSystemTeamPrincipalFilter(options: AuthorizationPrincipalOptionListOptions): { clause: string; params: string[] } {
  return buildPrincipalFilter(options, buildSystemTeamPrincipalKeywordFilter)
}

function buildAuthorizationGranteeGroupFilter(options: AuthorizationGranteeGroupOptionListOptions, granteeSystemAccountId: string): { clause: string; params: string[] } {
  const clauses = ['groups.system_account_id = ?', 'groups.enabled = 1']
  const params = [granteeSystemAccountId]
  const ids = normalizeTextList(options.ids)
  if (ids.length) {
    clauses.push(`groups.id IN (${sqlPlaceholders(ids.length)})`)
    params.push(...ids)
  }
  const providerCode = optionalString(options.providerCode)
  if (providerCode) {
    clauses.push('groups.provider_code COLLATE NOCASE = ?')
    params.push(providerCode)
  }
  const keyword = optionalString(options.keyword)
  if (keyword) {
    const prefix = `${escapeLikePrefix(keyword)}%`
    clauses.push(`(
      groups.name COLLATE NOCASE = ?
      OR groups.name LIKE ? ESCAPE '\\'
    )`)
    params.push(keyword, prefix)
  }
  return {
    clause: `WHERE ${clauses.join(' AND ')}`,
    params
  }
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

const systemTeamInputKeys = new Set(['name', 'description', 'status'])
const systemTeamMembersInputKeys = new Set(['systemAccountIds'])

export function createSystemTeam(input: Record<string, unknown>, access?: AccessScope): SystemTeamSummary {
  assertKnownInputKeys(input, systemTeamInputKeys, '系统团队')
  const name = normalizeSystemTeamName(input.name)
  const database = getBusinessDatabase()
  ensureSystemTeamNameUnique(name, undefined, database)
  const now = nowIso()
  const id = newId('team')
  database
    .prepare('INSERT INTO system_teams (id, name, description, status, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)')
    .run(id, name, normalizeSystemTeamDescription(input.description), normalizeSystemTeamStatus(input.status, 'active'), currentSystemAccountId(access), now, now)
  const created = findSystemTeamSummary(id, access)
  if (!created) throw new Error('创建团队失败')
  invalidateSystemTeamLookupCache(id)
  return created
}

export function updateSystemTeam(id: string, input: Record<string, unknown>, access?: AccessScope): SystemTeamSummary | undefined {
  assertKnownInputKeys(input, systemTeamInputKeys, '系统团队')
  const database = getBusinessDatabase()
  const row = database.prepare('SELECT * FROM system_teams WHERE id = ?').get(id) as unknown as SystemTeamRow | undefined
  if (!row) return undefined
  const name = input.name === undefined ? row.name : normalizeSystemTeamName(input.name)
  ensureSystemTeamNameUnique(name, id, database)
  const status = normalizeSystemTeamStatus(input.status, row.status)
  const now = nowIso()
  let authorizationChanged = false
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database
      .prepare('UPDATE system_teams SET name = ?, description = ?, status = ?, updated_at = ? WHERE id = ?')
      .run(name, input.description === undefined ? row.description : normalizeSystemTeamDescription(input.description), status, now, id)
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
  assertKnownInputKeys(input, systemTeamMembersInputKeys, '团队成员')
  const team = getBusinessDatabase().prepare("SELECT * FROM system_teams WHERE id = ? AND status = 'active'").get(teamId) as unknown as SystemTeamRow | undefined
  if (!team) return undefined
  const systemAccountIds = normalizeSystemAccountIds(input.systemAccountIds)
  if (!systemAccountIds.length) throw new Error('请选择团队成员')
  if (systemAccountIds.length > maxSystemTeamMemberBatchSize) {
    throw new Error(`单次最多添加 ${maxSystemTeamMemberBatchSize} 个团队成员`)
  }
  const database = getBusinessDatabase()
  const existingActiveMemberRows = database.prepare(`
    SELECT system_account_id
    FROM system_team_members
    WHERE team_id = ?
      AND status = 'active'
    ORDER BY system_account_id ASC
    LIMIT ?
  `).all(teamId, maxSystemTeamMembersPerTeam + 1) as unknown as Array<{ system_account_id?: string }>
  if (existingActiveMemberRows.length > maxSystemTeamMembersPerTeam) {
    throw new Error(`授权团队最多支持 ${maxSystemTeamMembersPerTeam} 个成员，请先移除部分成员后再添加`)
  }
  const existingActiveMemberIds = new Set<string>()
  for (const memberRow of existingActiveMemberRows) {
    const systemAccountId = memberRow.system_account_id?.trim()
    if (systemAccountId) {
      existingActiveMemberIds.add(systemAccountId)
    }
  }
  const nextActiveMemberIds = systemAccountIds.filter((systemAccountId) => !existingActiveMemberIds.has(systemAccountId))
  if (existingActiveMemberIds.size + nextActiveMemberIds.length > maxSystemTeamMembersPerTeam) {
    throw new Error(`授权团队最多支持 ${maxSystemTeamMembersPerTeam} 个成员，请先移除部分成员后再添加`)
  }
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
  const database = getBusinessDatabase()
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

const resourceAuthorizationCreateInputKeys = new Set(['resourceType', 'resourceId', 'granteeType', 'granteeId', 'targetGroupId', 'remark', 'expiresAt', 'limits'])
const resourceAuthorizationUpdateInputKeys = new Set(['status', 'expiresAt', 'limits'])

export function createResourceAuthorization(input: Record<string, unknown>, access?: AccessScope): ResourceAuthorizationSummary {
  assertKnownInputKeys(input, resourceAuthorizationCreateInputKeys, '资源授权')
  const resourceType = normalizeResourceType(input.resourceType)
  const resourceId = normalizeRequiredTextInput(input.resourceId, '授权资源')
  if (!resourceType || !resourceId) throw new Error('请选择授权资源')
  const ownerSystemAccountId = resourceOwnerSystemAccountId(resourceType, resourceId)
  if (!ownerSystemAccountId || !canManageResourceOwner(ownerSystemAccountId, access)) throw new Error('授权资源不存在')
  const granteeType = normalizeResourceAuthorizationGranteeType(input.granteeType)
  const granteeId = normalizeRequiredTextInput(input.granteeId, '被授权对象')
  if (!granteeId) throw new Error('请选择被授权对象')
  const database = getBusinessDatabase()
  const now = nowIso()
  const expiresAt = normalizeResourceAuthorizationExpiresAtInput(input.expiresAt)
  validateResourceAuthorizationExpiresAt(resourceType, resourceId, expiresAt, Date.parse(now))
  const actor = currentSystemAccountId(access)
  const targetGroupId = normalizeOptionalTextInput(input.targetGroupId, '目标分组')
  const remark = normalizeOptionalTextInput(input.remark, '授权备注', { allowBlank: true })
  if (!targetGroupId && resourceType === 'account' && granteeType === 'system_account') {
    throw new Error('授权 AI 账户给个人时必须选择目标分组')
  }
  if (targetGroupId && (resourceType !== 'account' || granteeType !== 'system_account')) {
    throw new Error('只有授权 AI 账户给个人时可以指定目标分组')
  }
  let createdGrantId: string | undefined
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    if (granteeType === 'team') {
      const team = database.prepare("SELECT * FROM system_teams WHERE id = ? AND status = 'active'").get(granteeId) as unknown as SystemTeamRow | undefined
      if (!team) throw new Error('团队不存在或已停用')
      const members = activeTeamMemberRows(granteeId, database).filter((member) => member.system_account_id !== ownerSystemAccountId)
      if (!members.length) throw new Error('团队暂无可授权成员，请先添加非归属人成员后再授权')
      const grant = upsertResourceAuthorizationGrant({ resourceType, resourceId, ownerSystemAccountId, granteeType, granteeId, remark, expiresAt, limits: input.limits, actor, now, database })
      assertActiveTeamGrantFanoutWithinLimit(granteeId, database)
      createdGrantId = grant.id
      for (const member of members) {
        upsertResourceAuthorizationForUser({ resourceType, resourceId, ownerSystemAccountId, granteeSystemAccountId: member.system_account_id, sourceType: 'team', sourceTeamId: granteeId, remark, expiresAt, limits: input.limits, actor, now, database })
      }
    } else {
      const grantee = findSystemAccountById(granteeId)
      if (!grantee || grantee.status !== 'active') throw new Error('被授权用户不存在或已停用')
      if (granteeId === ownerSystemAccountId) throw new Error('不能授权给资源所有者自己')
      const grant = upsertResourceAuthorizationGrant({ resourceType, resourceId, ownerSystemAccountId, granteeType, granteeId, remark, expiresAt, limits: input.limits, actor, now, database })
      createdGrantId = grant.id
      upsertResourceAuthorizationForUser({ resourceType, resourceId, ownerSystemAccountId, granteeSystemAccountId: granteeId, sourceType: 'manual', targetGroupId, remark, expiresAt, limits: input.limits, actor, now, database })
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
  throw new Error('创建资源授权失败')
}

export function revokeResourceAuthorization(authorizationId: string, access?: AccessScope): ResourceAuthorizationSummary | undefined {
  const database = getBusinessDatabase()
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

export function returnResourceAuthorizationForGrantee(authorizationId: string, access?: AccessScope): ResourceAuthorizationRow | undefined {
  expireDueResourceAuthorizations()
  const granteeSystemAccountId = userVisibleSystemAccountId(access)
  if (!granteeSystemAccountId) return undefined
  const database = getBusinessDatabase()
  const authorization = findReturnableRuntimeAuthorizationForGrant(authorizationId, granteeSystemAccountId, database)
  if (!authorization || authorization.resource_owner_system_account_id === granteeSystemAccountId) {
    return undefined
  }
  const now = nowIso()
  const actor = currentSystemAccountId(access)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const directGrants = database
      .prepare(`
        SELECT *
        FROM resource_authorization_grants
        WHERE resource_type = ?
          AND resource_id = ?
          AND resource_owner_system_account_id = ?
          AND grantee_type = 'system_account'
          AND grantee_system_account_id = ?
          AND status NOT IN ('revoked', 'returned')
      `)
      .all(
        authorization.resource_type,
        authorization.resource_id,
        authorization.resource_owner_system_account_id,
        granteeSystemAccountId
      ) as unknown as ResourceAuthorizationGrantRow[]
    for (const grant of directGrants) {
      returnResourceAuthorizationGrant(grant, actor, database, now)
    }
    database
      .prepare(`
        UPDATE resource_authorization_sources
        SET status = 'revoked',
            ended_at = COALESCE(ended_at, ?),
            ended_reason = COALESCE(ended_reason, 'grantee_returned'),
            revoked_by = ?,
            revoked_at = ?,
            updated_at = ?
        WHERE authorization_id = ?
          AND status IN ('active', 'superseded')
      `)
      .run(now, actor, now, now, authorization.id)
    database
      .prepare(`
        UPDATE resource_authorizations
        SET status = 'returned',
            effective_source_type = NULL,
            effective_source_team_id = NULL,
            revoked_by = ?,
            revoked_at = ?,
            revoked_reason = 'grantee_returned',
            last_source_changed_at = ?,
            updated_at = ?
        WHERE id = ?
          AND grantee_system_account_id = ?
      `)
      .run(actor, now, now, now, authorization.id, granteeSystemAccountId)
    cleanupInactiveAuthorizationBindings(database, [authorization.id])
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshGroupAccountStatsAfterWrite({ all: true, reason: 'resource_authorization_returned' })
  invalidateGroupAccountIdsCache()
  clearResourceAuthorizationLookupCaches()
  invalidateAuthorizationRuntimeAfterBusinessWrite('resource_authorization_returned')
  return database
    .prepare(`SELECT ${resourceAuthorizationSelectColumns()} FROM resource_authorizations WHERE id = ? LIMIT 1`)
    .get(authorization.id) as unknown as ResourceAuthorizationRow | undefined
}

function findReturnableRuntimeAuthorizationForGrant(authorizationId: string, granteeSystemAccountId: string, database: DatabaseSync): ResourceAuthorizationRow | undefined {
  const grant = database
    .prepare(`
      SELECT *
      FROM resource_authorization_grants
      WHERE id = ?
        AND grantee_type = 'system_account'
        AND grantee_system_account_id = ?
        AND status <> 'revoked'
      LIMIT 1
    `)
    .get(authorizationId, granteeSystemAccountId) as unknown as ResourceAuthorizationGrantRow | undefined
  if (!grant) return undefined
  return database
    .prepare(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM resource_authorizations
      WHERE resource_type = ?
        AND resource_id = ?
        AND resource_owner_system_account_id = ?
        AND grantee_system_account_id = ?
      LIMIT 1
    `)
    .get(grant.resource_type, grant.resource_id, grant.resource_owner_system_account_id, granteeSystemAccountId) as unknown as ResourceAuthorizationRow | undefined
}

export function updateResourceAuthorization(authorizationId: string, input: Record<string, unknown> = {}, access?: AccessScope): ResourceAuthorizationSummary | undefined {
  assertKnownInputKeys(input, resourceAuthorizationUpdateInputKeys, '资源授权')
  expireDueResourceAuthorizations()
  const database = getBusinessDatabase()
  const grant = database.prepare('SELECT * FROM resource_authorization_grants WHERE id = ?').get(authorizationId) as unknown as ResourceAuthorizationGrantRow | undefined
  if (!grant || !canManageResourceOwner(grant.resource_owner_system_account_id, access)) return undefined
  const now = nowIso()
  const hasExpiresAtInput = Object.prototype.hasOwnProperty.call(input, 'expiresAt')
  const hasLimitsInput = Object.prototype.hasOwnProperty.call(input, 'limits')
  const nextExpiresAt = hasExpiresAtInput
    ? normalizeResourceAuthorizationExpiresAtInput(input.expiresAt)
    : grant.expires_at
  const nextLimits = hasLimitsInput
    ? requestQuotaLimitsJson(normalizeRequestQuotaLimits(input.limits))
    : grant.limits_json
  const requestedStatus = Object.prototype.hasOwnProperty.call(input, 'status')
    ? normalizeResourceAuthorizationStatus(input.status)
    : undefined
  validateResourceAuthorizationExpiresAt(grant.resource_type, grant.resource_id, nextExpiresAt, Date.parse(now), { allowExpired: requestedStatus === 'expired' })
  if (grant.status === 'expired' && requestedStatus === 'active' && !hasExpiresAtInput) {
    throw new Error('到期授权恢复时请同时调整过期时间')
  }
  const expiredByTime = isResourceAuthorizationExpired(nextExpiresAt)
  const nextStatus: AuthorizationStatus = expiredByTime
    ? 'expired'
    : requestedStatus === 'active' || requestedStatus === 'paused' || requestedStatus === 'revoked' || requestedStatus === 'returned'
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
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    const teamRows = database.prepare(`
      SELECT *
      FROM (
        SELECT ${systemTeamMemberSelectColumns('system_team_members')}, system_accounts.display_name, system_accounts.username,
          ROW_NUMBER() OVER (PARTITION BY system_team_members.team_id ORDER BY system_team_members.status ASC, system_team_members.joined_at ASC, system_team_members.id ASC) AS team_member_rank
        FROM system_team_members
        INNER JOIN system_accounts ON system_accounts.id = system_team_members.system_account_id
        WHERE system_team_members.team_id IN (${sqlPlaceholders(chunk.length)})${statusClause}
      )
      WHERE team_member_rank <= ?
    `).all(...chunk, maxSystemTeamMembersPerTeam) as unknown as Array<SystemTeamMemberRow & { display_name?: string; username?: string }>
    rows.push(...teamRows.sort(compareSystemTeamMembersForList))
  }
  const result = new Map<string, SystemTeamMemberSummary[]>()
  for (const row of rows) {
    const member: SystemTeamMemberSummary = { id: row.id, teamId: row.team_id, systemAccountId: row.system_account_id, systemAccountName: row.display_name, username: row.username, memberRole: 'member', status: row.status, joinedAt: row.joined_at, removedAt: row.removed_at ?? undefined, createdAt: row.created_at, updatedAt: row.updated_at }
    result.set(row.team_id, [...(result.get(row.team_id) ?? []), member])
  }
  return result
}

function compareSystemTeamMembersForList(left: SystemTeamMemberRow, right: SystemTeamMemberRow): number {
  const team = left.team_id.localeCompare(right.team_id)
  if (team !== 0) return team
  const status = left.status.localeCompare(right.status)
  if (status !== 0) return status
  const joinedAt = left.joined_at.localeCompare(right.joined_at)
  return joinedAt !== 0 ? joinedAt : left.id.localeCompare(right.id)
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

function ensureSystemTeamNameUnique(name: string, excludeId?: string, database = getBusinessDatabase()): void {
  const row = database
    .prepare('SELECT id FROM system_teams WHERE lower(name) = lower(?) AND id <> ? LIMIT 1')
    .get(name, excludeId ?? '') as unknown as { id?: string } | undefined
  if (row?.id) throw new Error('团队名称已存在')
}

function normalizeSystemTeamName(value: unknown): string {
  if (typeof value !== 'string') {
    throw new Error('团队名称不能为空')
  }
  const name = value.trim()
  if (!name) {
    throw new Error('团队名称不能为空')
  }
  return name
}

function normalizeSystemTeamDescription(value: unknown): string | null {
  if (value === undefined || value === null) {
    return null
  }
  if (typeof value !== 'string') {
    throw new Error('团队说明必须是字符串')
  }
  const description = value.trim()
  return description || null
}

function normalizeSystemTeamStatus(value: unknown, fallback: string): 'active' | 'disabled' {
  if (value === undefined) {
    if (fallback === 'active' || fallback === 'disabled') return fallback
    throw new Error('团队状态无效')
  }
  if (value === 'active' || value === 'disabled') {
    return value
  }
  throw new Error('团队状态无效')
}

function normalizeSystemAccountIds(value: unknown): string[] {
  if (!Array.isArray(value)) {
    throw new Error('团队成员必须是系统账户 ID 数组')
  }
  const ids: string[] = []
  const seen = new Set<string>()
  for (const item of value) {
    if (typeof item !== 'string') {
      throw new Error('团队成员必须是系统账户 ID 数组')
    }
    const id = item.trim()
    if (!id) {
      throw new Error('团队成员 ID 不能为空')
    }
    if (seen.has(id)) {
      throw new Error('团队成员不能重复')
    }
    seen.add(id)
    ids.push(id)
  }
  return ids
}

function normalizeRequiredTextInput(value: unknown, label: string): string {
  if (typeof value !== 'string') {
    throw new Error(`${label}不能为空`)
  }
  const text = value.trim()
  if (!text) {
    throw new Error(`${label}不能为空`)
  }
  return text
}

function normalizeOptionalTextInput(value: unknown, label: string, options: { allowBlank?: boolean } = {}): string | undefined {
  if (value === undefined) {
    return undefined
  }
  if (value === null) {
    throw new Error(`${label}不能为空`)
  }
  if (typeof value !== 'string') {
    throw new Error(`${label}必须是字符串`)
  }
  const text = value.trim()
  if (!text) {
    if (options.allowBlank) return undefined
    throw new Error(`${label}不能为空`)
  }
  return text
}

function normalizeResourceAuthorizationGranteeType(value: unknown): 'team' | 'system_account' {
  if (value === 'team' || value === 'system_account') {
    return value
  }
  throw new Error('被授权对象类型无效')
}

function normalizeResourceAuthorizationStatus(value: unknown): AuthorizationStatus {
  if (value === 'active' || value === 'paused' || value === 'expired' || value === 'revoked' || value === 'returned') {
    return value
  }
  throw new Error('授权状态无效')
}

function normalizeResourceAuthorizationExpiresAtInput(value: unknown): string | null {
  if (value === undefined || value === null) {
    return null
  }
  if (typeof value !== 'string') {
    throw new Error('过期时间格式不正确')
  }
  const text = value.trim()
  if (!text) {
    throw new Error('过期时间格式不正确')
  }
  const normalized = optionalNullableServerDateTimeIso(text)
  if (!normalized) {
    throw new Error('过期时间格式不正确')
  }
  return normalized
}

function resourceOwnerSystemAccountId(resourceType: ResourceAuthorizationResourceType, resourceId: string): string | undefined {
  if (resourceType !== 'account') return groupOwnerAndProvider(resourceId)?.systemAccountId
  const row = getBusinessDatabase()
    .prepare('SELECT system_account_id, authorization_instance_authorization_id FROM accounts WHERE id = ? LIMIT 1')
    .get(resourceId) as unknown as { system_account_id?: string; authorization_instance_authorization_id?: string | null } | undefined
  if (!row?.system_account_id || row.authorization_instance_authorization_id) return undefined
  return row.system_account_id
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
  const account = getBusinessDatabase()
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
  const runtime = getBusinessDatabase().prepare(`
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
    systemAccountId: authorizationUsageStatsSystemAccountId(authorization.resourceType, authorization.resourceOwnerSystemAccountId, granteeSystemAccountId),
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
  const database = getBusinessDatabase()
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
  const rangeUsage = loadAuthorizationTeamUsageRangeSummary(authorization, teamId, range)
    ?? loadUsageRangeSummaryForScope({
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
  const scopes = rows.map((row) => usageScope(
    row.id,
    authorizationUsageStatsSystemAccountId(row.resource_type, row.resource_owner_system_account_id, row.grantee_system_account_id ?? undefined),
    row.id
  ))
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

function authorizationUsageStatsSystemAccountId(
  resourceType: ResourceAuthorizationResourceType,
  resourceOwnerSystemAccountId: string,
  granteeSystemAccountId?: string
): string {
  return resourceType === 'account'
    ? granteeSystemAccountId ?? resourceOwnerSystemAccountId
    : resourceOwnerSystemAccountId
}

function loadAuthorizationTeamUsageRangeSummary(
  authorization: ResourceAuthorizationSummary,
  teamId: string,
  range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>
): AccountUsageSummary | undefined {
  const row = getStatsDatabase().prepare(`
    SELECT request_count, input_tokens, output_tokens, cache_read_tokens,
      cache_read_cost_usd, total_cost_usd AS total_cost, last_used_at
    FROM authorization_team_usage_range_windows
    WHERE system_account_id = ?
      AND start_date = ?
      AND end_date = ?
      AND team_filter_id = ?
      AND resource_filter_type = ?
      AND resource_filter_id = ?
    LIMIT 1
  `).get(
    authorization.resourceOwnerSystemAccountId,
    range.startDate,
    range.endDate,
    teamId,
    authorization.resourceType,
    authorization.resourceId
  ) as unknown as Parameters<typeof usageSummaryFromAggregate>[0] | undefined
  return row ? usageSummaryFromAggregate(row) : undefined
}

function normalizeResourceAuthorizationUsagePageOptions(options: ResourceAuthorizationUsageOptions): { page: number; pageSize: number } {
  const pageSize = typeof options.pageSize === 'number' && Number.isInteger(options.pageSize)
    ? Math.min(200, Math.max(1, options.pageSize))
    : defaultResourceAuthorizationUsageDetailPageSize
  const page = normalizeListPage(options.page, pageSize)
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
